//! Decoding the hub's continuity payloads from the bytes the host received (docs/plans/plan-rust-core.md, decision 2):
//! the latest / older page, the catch-up page, an opened fold's page and the WebSocket `items` frame. No IO and no
//! clock; the results are the inputs `continuity` already takes.
//!
//! iOS is the reference (decision 7). Each decoder accepts what the named Swift type accepts and refuses what it
//! refuses, with the differences listed under "Known differences from iOS" below. Nothing here was executed on iOS:
//! the rules were read from the Swift sources and from how Foundation's `JSONDecoder` treats a synthesized
//! `Decodable` (swift-foundation's `JSONDecoder.swift`, whose shipped revision may differ). The shared wire fixtures
//! replayed in `tests/transport_fixtures.rs` are derived from the iOS sources in the same way and are checked by the
//! hub's golden test and the Android replay; no iOS test reads those cases yet, and all of them are accepted
//! payloads, so every refusal rule rests on the unit tests in this module.
//!
//! | Decoder | Swift type | Required | Optional (`null` or absent is "no value") |
//! | --- | --- | --- | --- |
//! | [`decode_latest`] | `APIClient.ItemsResponse` | `items`, `has_more` | `history`, `to_seq` |
//! | [`decode_newer`] | `APIClient.NewerResponse` | `items` | `more_after`, `history`, `from_seq`, `to_seq` |
//! | [`decode_range`] | `Range` in `APIClient.foldedWork` | `items` | `more` |
//! | [`decode_frame`] | `ServerEvent.decode` and its `Envelope` | `type`, `machine_id`, `session_id` | `items`, `backlog`, `has_more`, `more_after`, `history`, `from_seq`, `to_seq` |
//!
//! Shared rules:
//!
//! - The root is a JSON object. Unknown keys are ignored, whatever they hold.
//! - A known key of the wrong type refuses the whole payload, as does an item iOS's `Item` refuses
//!   ([`Item::from_wire`] lists exactly which item rules are enforced).
//! - A whole number is an integer literal inside the 64-bit range (`8`).
//! - A key that appears more than once keeps its first value, at the root, in an item and in an item's `fold`, as
//!   Foundation's keyed container does. The later values must still be JSON.
//! - Every item's JSON is kept byte for byte.
//!
//! # Known differences from iOS
//!
//! - **Envelope keys an `items` frame does not use are not checked.** iOS's `Envelope` is one struct for every event
//!   type, so iOS also drops an `items` frame when a key only another event uses (`rows`, `features`, `fleet`, …) is
//!   present with the wrong type. [`decode_frame`] checks the keys an `items` frame carries and ignores the rest: the
//!   hub never sends those on an `items` frame, the design's target is tolerant decoding (D5 in
//!   `docs/archive/redesign/README.md`), and following the other keys would tie this decoder to every unrelated
//!   addition to the Swift struct.
//! - **Nested item structs are not checked**: see [`Item::from_wire`].
//! - **Raw JSON keys can refuse on iOS.** The envelope's `args`, `account`, `action`, `answered_by`, `reason`,
//!   `answers`, `resolution`, `manager` and `changed`, and an item's `tool_input` and `source`, are `JSONValue` on
//!   iOS, which reads every number in them as a `Double`: a number that overflows a double or rounds to zero
//!   (`1e999`, `1e-999`) anywhere under one of them refuses the frame or the page there. The core does not read
//!   those keys and accepts it.
//! - **Whole numbers written with a fraction or an exponent** (`8.0`, `8e0`), and `-0`. Foundation takes them as
//!   the integer. The core refuses them: the hub writes integer literals only, and reading an integer through a
//!   double would need the parser's slower, larger exact float reader to keep `9007199254740991.0` from becoming a
//!   neighbour.
//! - **Numbers beyond a double on a key the core reads** (`"to_seq":1e999`) refuse the payload on both sides, but the
//!   core reports `malformed_json` for it rather than `wrong_type`, and `unreadable` inside an item.
//! - **An item's `fold` is also read as a tree**, to compare a fold that arrives again. That refuses an item whose
//!   fold nests deeper than 128 levels or holds a number beyond a double under any key, also one iOS never reads.
//! - **Nesting depth.** Foundation bounds how deep JSON may nest. The core bounds it only in that tree; elsewhere it
//!   skips values without recursing.
//! - **Encodings.** The core takes UTF-8 without a byte order mark. Foundation also takes a mark and UTF-16 / UTF-32.
//! - **Unknown kinds.** iOS reads an unknown item `kind` as `unknown`; the core keeps the hub's string.
//! - **Frames of other types.** [`decode_frame`] reports [`Frame::Other`] for any JSON object whose first `type` is
//!   a string other than `items`, without checking the rest of its envelope; iOS drops such a frame when an
//!   envelope key has the wrong type. Decoders for those frames come in later phases.

pub(crate) mod seen;

use std::borrow::Cow;

use serde::Deserialize;
use serde::de::{IgnoredAny, SeqAccess};
use serde_json::value::RawValue;

pub use self::seen::KeyFault;
use self::seen::{Seen, Shape, keyed, opens_object};
use crate::continuity::Batch;
use crate::item::{Item, ItemFault};

/// Why a payload was refused: a short code and protocol key names for diagnostics. It never carries payload content
/// such as ids or message text, and it is not text for people.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DecodeError {
    /// The bytes are not UTF-8.
    InvalidUtf8 { valid_up_to: usize },
    /// The bytes are not one JSON value: a syntax error, an early end or trailing bytes. Also a value under a key
    /// the decoder reads that the parser cannot hold: a number beyond a double's range, or an escape that is not a
    /// character (a lone surrogate), in that key or in the name of any key of the root.
    Malformed {
        truncated: bool,
        line: usize,
        column: usize,
    },
    /// The root is valid JSON but not an object.
    NotObject,
    /// A key of the payload is missing, `null` where a value is required, or of the wrong type.
    Key(KeyFault),
    /// The item at `index` of `items` was refused.
    Item { index: usize, fault: ItemFault },
}

impl DecodeError {
    /// A stable reason code.
    pub fn code(&self) -> &'static str {
        match self {
            DecodeError::InvalidUtf8 { .. } => "invalid_utf8",
            DecodeError::Malformed { truncated: true, .. } => "truncated_json",
            DecodeError::Malformed { truncated: false, .. } => "malformed_json",
            DecodeError::NotObject => "not_object",
            DecodeError::Key(KeyFault::Missing(_)) => "missing_key",
            DecodeError::Key(KeyFault::WrongType(_)) => "wrong_type",
            DecodeError::Item { .. } => "bad_item",
        }
    }

    /// Where it went wrong: positions and protocol key names only.
    pub fn detail(&self) -> String {
        match self {
            DecodeError::InvalidUtf8 { valid_up_to } => format!("byte {valid_up_to}"),
            DecodeError::Malformed { line, column, .. } => format!("line {line} column {column}"),
            DecodeError::NotObject => String::new(),
            DecodeError::Key(KeyFault::Missing(key) | KeyFault::WrongType(key)) => (*key).to_owned(),
            DecodeError::Item { index, fault } => {
                let fault = match fault {
                    ItemFault::NotObject => "not_object".to_owned(),
                    ItemFault::Unreadable => "unreadable".to_owned(),
                    ItemFault::Key(KeyFault::Missing(key)) => format!("missing_key {key}"),
                    ItemFault::Key(KeyFault::WrongType(key)) => format!("wrong_type {key}"),
                };
                format!("items[{index}] {fault}")
            }
        }
    }
}

impl std::fmt::Display for DecodeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let detail = self.detail();
        if detail.is_empty() {
            f.write_str(self.code())
        } else {
            write!(f, "{}: {detail}", self.code())
        }
    }
}

impl std::error::Error for DecodeError {}

impl From<KeyFault> for DecodeError {
    fn from(fault: KeyFault) -> Self {
        DecodeError::Key(fault)
    }
}

/// The latest page, or an older one (`GET …/items`, with or without `before_seq`): iOS's `APIClient.ItemsResponse`.
#[derive(Debug)]
pub struct LatestPage {
    pub items: Vec<Item>,
    pub has_more: bool,
    pub history: Option<String>,
    pub to_seq: Option<i64>,
}

impl LatestPage {
    /// The page as `Session::received` takes the first one: a backlog counted back from the end, which continues
    /// nothing. `Session::reload` and `Session::older` take the fields as they are.
    pub fn into_batch(self) -> Batch {
        Batch {
            items: self.items,
            history: self.history,
            from_seq: None,
            to_seq: self.to_seq,
            backlog: true,
            has_more: Some(self.has_more),
            more_after: false,
        }
    }
}

/// A catch-up page (`GET …/items?after_seq=`): iOS's `APIClient.NewerResponse`.
#[derive(Debug)]
pub struct NewerPage {
    pub items: Vec<Item>,
    pub more_after: Option<bool>,
    pub history: Option<String>,
    pub from_seq: Option<i64>,
    pub to_seq: Option<i64>,
}

impl NewerPage {
    /// The page as `Session::after` takes it. It says nothing about older history.
    pub fn into_batch(self) -> Batch {
        Batch {
            items: self.items,
            history: self.history,
            from_seq: self.from_seq,
            to_seq: self.to_seq,
            backlog: false,
            has_more: None,
            more_after: self.more_after == Some(true),
        }
    }
}

/// A page of an opened fold (`GET …/items?from_seq=&through_seq=`): `Range` in iOS's `APIClient.foldedWork`.
#[derive(Debug)]
pub struct RangePage {
    pub items: Vec<Item>,
    pub more: Option<bool>,
}

impl RangePage {
    /// The arguments of `Session::range`.
    pub fn into_parts(self) -> (Vec<Item>, bool) {
        (self.items, self.more == Some(true))
    }
}

/// A WebSocket `items` frame: iOS's `ServerEvent.items`.
#[derive(Debug)]
pub struct ItemsFrame {
    pub machine_id: String,
    pub session_id: String,
    pub items: Vec<Item>,
    pub backlog: bool,
    pub has_more: Option<bool>,
    pub history: Option<String>,
    pub from_seq: Option<i64>,
    pub to_seq: Option<i64>,
    pub more_after: bool,
}

impl ItemsFrame {
    /// The frame as `Session::received` takes it.
    pub fn into_batch(self) -> Batch {
        Batch {
            items: self.items,
            history: self.history,
            from_seq: self.from_seq,
            to_seq: self.to_seq,
            backlog: self.backlog,
            has_more: self.has_more,
            more_after: self.more_after,
        }
    }
}

/// What a WebSocket frame was.
#[derive(Debug)]
pub enum Frame {
    Items(ItemsFrame),
    /// A frame of another type: not this decoder's, and not an error.
    Other,
}

/// `items`: every element read as an item, or the first one refused. An element is read as soon as the array
/// reaches it, so a page whose first element is refused costs no more than skipping the rest.
struct Items(Result<Vec<Item>, (usize, ItemFault)>);

impl<'de> Shape<'de> for Items {
    fn seq<A: SeqAccess<'de>>(mut seq: A) -> Result<Option<Self>, A::Error> {
        let mut items = Vec::new();
        while let Some(raw) = seq.next_element::<&'de RawValue>()? {
            match Item::from_wire(raw) {
                Ok(item) => items.push(item),
                Err(fault) => {
                    let index = items.len();
                    // The rest must still be JSON, but is not read.
                    while seq.next_element::<IgnoredAny>()?.is_some() {}
                    return Ok(Some(Items(Err((index, fault)))));
                }
            }
        }
        Ok(Some(Items(Ok(items))))
    }
}

fn read_items(items: Option<Items>) -> Result<Vec<Item>, DecodeError> {
    match items {
        None => Ok(Vec::new()),
        Some(Items(Ok(items))) => Ok(items),
        Some(Items(Err((index, fault)))) => Err(DecodeError::Item { index, fault }),
    }
}

fn utf8(bytes: &[u8]) -> Result<&str, DecodeError> {
    std::str::from_utf8(bytes).map_err(|e| DecodeError::InvalidUtf8 {
        valid_up_to: e.valid_up_to(),
    })
}

/// Parses the root object into `T`, a [`keyed!`] struct: no key's type can fail it, so every error left is the
/// document's.
fn root<'a, T: Deserialize<'a>>(json: &'a str) -> Result<T, DecodeError> {
    let malformed = |e: serde_json::Error| DecodeError::Malformed {
        truncated: e.is_eof(),
        line: e.line(),
        column: e.column(),
    };
    if !opens_object(json) {
        return Err(match serde_json::from_str::<IgnoredAny>(json) {
            Ok(_) => DecodeError::NotObject,
            Err(e) => malformed(e),
        });
    }
    serde_json::from_str(json).map_err(malformed)
}

keyed! {
    struct LatestBody<'a> {
        items = "items": Seen<Items>,
        has_more = "has_more": Seen<bool>,
        history = "history": Seen<Cow<'a, str>>,
        to_seq = "to_seq": Seen<i64>,
    }
}

/// Decodes the latest page or an older page.
pub fn decode_latest(bytes: &[u8]) -> Result<LatestPage, DecodeError> {
    let body: LatestBody<'_> = root(utf8(bytes)?)?;
    let items = body.items.required("items")?;
    let has_more = body.has_more.required("has_more")?;
    let history = body.history.optional("history")?.map(Cow::into_owned);
    let to_seq = body.to_seq.optional("to_seq")?;
    Ok(LatestPage {
        items: read_items(Some(items))?,
        has_more,
        history,
        to_seq,
    })
}

keyed! {
    struct NewerBody<'a> {
        items = "items": Seen<Items>,
        more_after = "more_after": Seen<bool>,
        history = "history": Seen<Cow<'a, str>>,
        from_seq = "from_seq": Seen<i64>,
        to_seq = "to_seq": Seen<i64>,
    }
}

/// Decodes a catch-up page.
pub fn decode_newer(bytes: &[u8]) -> Result<NewerPage, DecodeError> {
    let body: NewerBody<'_> = root(utf8(bytes)?)?;
    let items = body.items.required("items")?;
    let more_after = body.more_after.optional("more_after")?;
    let history = body.history.optional("history")?.map(Cow::into_owned);
    let from_seq = body.from_seq.optional("from_seq")?;
    let to_seq = body.to_seq.optional("to_seq")?;
    Ok(NewerPage {
        items: read_items(Some(items))?,
        more_after,
        history,
        from_seq,
        to_seq,
    })
}

keyed! {
    struct RangeBody<'a> {
        items = "items": Seen<Items>,
        more = "more": Seen<bool>,
    }
}

/// Decodes a page of an opened fold.
pub fn decode_range(bytes: &[u8]) -> Result<RangePage, DecodeError> {
    let body: RangeBody<'_> = root(utf8(bytes)?)?;
    let items = body.items.required("items")?;
    let more = body.more.optional("more")?;
    Ok(RangePage {
        items: read_items(Some(items))?,
        more,
    })
}

keyed! {
    /// The keys of iOS's `Envelope` (`apple/Shared/LiveConnection.swift`) that an `items` frame carries.
    struct Envelope<'a> {
        kind = "type": Seen<Cow<'a, str>>,
        machine_id = "machine_id": Seen<Cow<'a, str>>,
        session_id = "session_id": Seen<Cow<'a, str>>,
        items = "items": Seen<Items>,
        backlog = "backlog": Seen<bool>,
        has_more = "has_more": Seen<bool>,
        more_after = "more_after": Seen<bool>,
        history = "history": Seen<Cow<'a, str>>,
        from_seq = "from_seq": Seen<i64>,
        to_seq = "to_seq": Seen<i64>,
    }
}

keyed! {
    struct TypeOnly<'a> {
        kind = "type": Seen<Cow<'a, str>>,
    }
}

/// Whether the object, which did not parse as an envelope, is still JSON that names another type.
fn names_another_type(json: &str) -> bool {
    serde_json::from_str::<TypeOnly<'_>>(json)
        .is_ok_and(|only| matches!(only.kind, Seen::Value(kind) if kind != "items"))
}

/// Decodes a WebSocket frame. A frame of another type is [`Frame::Other`], whatever the rest of it holds.
pub fn decode_frame(bytes: &[u8]) -> Result<Frame, DecodeError> {
    let json = utf8(bytes)?;
    let e: Envelope<'_> = match root(json) {
        Ok(envelope) => envelope,
        // A value the envelope could not read (a number beyond a double, an escape that is not a character) belongs
        // to a later phase's decoder when the frame is of another type.
        Err(DecodeError::Malformed { .. }) if opens_object(json) && names_another_type(json) => {
            return Ok(Frame::Other);
        }
        Err(error) => return Err(error),
    };
    if e.kind.required("type")? != "items" {
        return Ok(Frame::Other);
    }
    // iOS decodes the whole envelope before it asks for the session key, so a wrong type is reported first.
    let items = e.items.optional("items")?;
    let backlog = e.backlog.optional("backlog")?;
    let has_more = e.has_more.optional("has_more")?;
    let more_after = e.more_after.optional("more_after")?;
    let history = e.history.optional("history")?;
    let from_seq = e.from_seq.optional("from_seq")?;
    let to_seq = e.to_seq.optional("to_seq")?;
    let machine_id = e.machine_id.optional("machine_id")?;
    let session_id = e.session_id.optional("session_id")?;
    // The envelope decoded; the frame still needs both halves of its session key.
    let machine_id = machine_id.ok_or(KeyFault::Missing("machine_id"))?;
    let session_id = session_id.ok_or(KeyFault::Missing("session_id"))?;
    Ok(Frame::Items(ItemsFrame {
        machine_id: machine_id.into_owned(),
        session_id: session_id.into_owned(),
        items: read_items(items)?,
        backlog: backlog == Some(true),
        has_more,
        history: history.map(Cow::into_owned),
        from_seq,
        to_seq,
        more_after: more_after == Some(true),
    }))
}

#[cfg(test)]
mod tests;
