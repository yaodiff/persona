//! A host stand-in for the HarmonyOS NAPI entry points, so the generated ArkTS codec can meet the real core's bytes
//! without an SDK, a HAP or a device (#7450). It drives the runtime through `persona_core_ohos::registry` exactly as
//! `core_config`, `core_create`, `core_dispatch`, `core_flush`, `core_drain` and `core_close` do, with a `Host` that
//! records every callback. `harmony/tests/core-host/replay.test.mjs` and `transport.test.mjs` are its clients. The
//! transport command also decodes raw payloads directly with `persona_core::transport`. It proves nothing about the
//! NAPI glue itself (argument conversion, thread-safe functions), which only a device build links.
//!
//! # Framing
//!
//! Both directions carry frames: a `u32` little-endian length, then that many bytes. A frame is one tag byte followed
//! by its fields. Integers are little-endian; the last field of a frame takes the rest of it, so no field carries a
//! length of its own. A frame is 1 byte to 16 MiB; the helper never writes a larger one either.
//!
//! Commands, read from stdin one at a time:
//!
//! | Tag | Command | Fields | Ok value |
//! | --- | --- | --- | --- |
//! | 1 | config | none | the default `Config`'s bytes |
//! | 2 | create | config bytes | handle `i64` |
//! | 3 | dispatch | handle `i64`, action bytes | queued `u8` (0 or 1) |
//! | 4 | flush | handle `i64`, timeout `u32` milliseconds (at most 60,000) | idle `u8` (0 or 1) |
//! | 5 | drain | handle `i64`, chat as UTF-8 | the chat's `ChatPatch` bytes |
//! | 6 | close | handle `i64` | none |
//! | 7 | reencode | wire type `u8`, bytes | Rust's encoding of the value it decoded |
//! | 8 | samples | none | none, after one `sample` record per value |
//! | 9 | transport | payload kind `u8`, UTF-8 JSON body | decoded result as UTF-8 JSON, including an `error` object on refusal |
//! | 10 | sort | UTF-8 JSON `sort::Input` | the `sort::Projection` as UTF-8 JSON |
//!
//! Wire types for `reencode`: 1 `Action`, 2 `ChatEvent`, 3 `ChatPatch`, 4 `ChatSnapshot`, 5 `Config`, 6 `Effect`,
//! 7 `ListOp`, 8 `Request`, 9 `WireBatch`, 10 `Payload`. It uses the decoder the worker uses for actions, which
//! refuses trailing bytes.
//!
//! Transport kinds are 1 `Latest`, 2 `Newer`, 3 `Reloaded`, 4 `Older`, 5 `Range`, 6 `Frame`. `Latest`, `Reloaded`
//! and `Older` all use `decode_latest`. The JSON result has `accepted`; a refused payload has `error` with only
//! `code` and `detail`, never payload content. A page has `history`, `from_seq`, `to_seq`, `has_more`, `more_after`,
//! `backlog` and `item_count`, each `null` or a boolean when the page or its batch does not carry the field. Every
//! item has `id`, `seq` and its kept raw `json`. A frame has `frame` (`"other"` or `"items"`); an items frame also
//! has `machine_id`, `session_id` and the batch fields above. Range exposes its `more` flag as `more_after`.
//!
//! Records, written to stdout:
//!
//! | Tag | Record | Fields |
//! | --- | --- | --- |
//! | 1 | ok | the command's value |
//! | 2 | error | a message as UTF-8 |
//! | 3 | changed | handle `i64`, version `u64` |
//! | 4 | effect | handle `i64`, the `Effect`'s bytes |
//! | 5 | sample | name length `u8`, name, the value's bytes |
//! | 6 | end | none |
//!
//! Every command is answered by exactly one `ok` or `error` record, the last record written for it, and stdout is
//! flushed after it. `error` is the registry or the decoder refusing a well-formed command (a closed handle, a
//! config or value that does not decode); the helper goes on with the next command.
//!
//! # Callback order
//!
//! The core calls its host on the worker thread, at any time after `dispatch` returns. To keep the output a function
//! of the input alone, a handle's `changed` and `effect` records are written, in the order the callbacks happened,
//! only immediately before the result of that handle's `flush` or `close`. After a `flush` that returns 1 the worker
//! is idle, so those records are everything the commands before it caused; after one that returns 0 they are only
//! what had happened by then. At the end of input the helper closes the handles still open, lowest first, writes
//! their remaining records and then `end`, and exits 0.
//!
//! # Malformed input
//!
//! A script that is not well formed ends the helper with exit status 2 and one line on stderr: a frame that is
//! empty, too long or cut short, an unknown tag or wire type, a field that is too short, bytes after a command's last
//! field, a chat that is not UTF-8, a timeout over the limit, or more than 64 handles open at once.

use std::collections::BTreeMap;
use std::fmt::Debug;
use std::io::{self, BufReader, BufWriter, Read, Write};
use std::panic::{AssertUnwindSafe, catch_unwind};
use std::process::ExitCode;
use std::sync::{Arc, Mutex, PoisonError};

use persona_core_ohos::registry;
use persona_core_runtime::Host;
use persona_core_runtime::transport::{self, Frame, LatestPage, NewerPage, RangePage};
use persona_core_runtime::wire::{
    self, Action, ChatEvent, ChatPatch, ChatSnapshot, Config, Effect, Item, ListOp, Payload, Request, WireBatch,
};

const MAX_FRAME: usize = 16 * 1024 * 1024;
const MAX_FLUSH_MS: u32 = 60_000;
const MAX_OPEN: usize = 64;

const CONFIG: u8 = 1;
const CREATE: u8 = 2;
const DISPATCH: u8 = 3;
const FLUSH: u8 = 4;
const DRAIN: u8 = 5;
const CLOSE: u8 = 6;
const REENCODE: u8 = 7;
const SAMPLES: u8 = 8;
const TRANSPORT: u8 = 9;
const SORT: u8 = 10;

const OK: u8 = 1;
const ERROR: u8 = 2;
const CHANGED: u8 = 3;
const EFFECT: u8 = 4;
const SAMPLE: u8 = 5;
const END: u8 = 6;

enum Callback {
    Changed(u64),
    Effect(Vec<u8>),
}

/// One handle's callbacks, in the order the worker made them.
#[derive(Default)]
struct Recorder {
    log: Mutex<Vec<Callback>>,
}

impl Recorder {
    fn push(&self, callback: Callback) {
        self.log.lock().unwrap_or_else(PoisonError::into_inner).push(callback);
    }

    fn take(&self) -> Vec<Callback> {
        std::mem::take(&mut *self.log.lock().unwrap_or_else(PoisonError::into_inner))
    }
}

// Only these two callbacks are implemented. A wake-up the trait later delivers through another method that defaults
// to `changed` is therefore still recorded as a `changed`.
impl Host for Recorder {
    fn changed(&self, version: u64) {
        self.push(Callback::Changed(version));
    }

    fn run(&self, effect: &[u8]) {
        self.push(Callback::Effect(effect.to_vec()));
    }
}

/// Reads one frame's bytes. `None` is the end of input at a frame boundary.
fn read_frame(input: &mut impl Read) -> Result<Option<Vec<u8>>, String> {
    let mut prefix = [0u8; 4];
    let mut filled = 0;
    while let Some(rest) = prefix.get_mut(filled..).filter(|rest| !rest.is_empty()) {
        match input.read(rest) {
            Ok(0) if filled == 0 => return Ok(None),
            Ok(0) => return Err("the input ends inside a frame's length".into()),
            Ok(count) => filled += count,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => {}
            Err(error) => return Err(format!("cannot read the input: {error}")),
        }
    }
    let length = u32::from_le_bytes(prefix);
    let length = usize::try_from(length).ok().filter(|length| *length <= MAX_FRAME);
    let Some(length) = length else {
        return Err(format!("a frame is longer than {MAX_FRAME} bytes"));
    };
    if length == 0 {
        return Err("a frame is empty".into());
    }
    let mut frame = vec![0u8; length];
    match input.read_exact(&mut frame) {
        Ok(()) => Ok(Some(frame)),
        Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => Err("the input ends inside a frame".into()),
        Err(error) => Err(format!("cannot read the input: {error}")),
    }
}

fn write_record(output: &mut impl Write, tag: u8, fields: &[&[u8]]) -> Result<(), String> {
    let length = 1 + fields.iter().map(|field| field.len()).sum::<usize>();
    if length > MAX_FRAME {
        return Err(format!("a record would be longer than {MAX_FRAME} bytes"));
    }
    let prefix = u32::try_from(length).map_err(|_| "a record is too long".to_owned())?;
    let mut write = |bytes: &[u8]| {
        output
            .write_all(bytes)
            .map_err(|error| format!("cannot write the output: {error}"))
    };
    write(&prefix.to_le_bytes())?;
    write(&[tag])?;
    fields.iter().try_for_each(|field| write(field))
}

/// A command's fields, taken from the front.
struct Fields<'a> {
    command: &'static str,
    bytes: &'a [u8],
}

impl<'a> Fields<'a> {
    fn fixed<const N: usize>(&mut self) -> Result<[u8; N], String> {
        let Some((head, rest)) = self.bytes.split_first_chunk::<N>() else {
            return Err(format!("{}: a field is too short", self.command));
        };
        self.bytes = rest;
        Ok(*head)
    }

    fn handle(&mut self) -> Result<i64, String> {
        self.fixed().map(i64::from_le_bytes)
    }

    fn rest(self) -> &'a [u8] {
        self.bytes
    }

    fn end(self) -> Result<(), String> {
        if self.bytes.is_empty() {
            Ok(())
        } else {
            Err(format!("{}: bytes follow the last field", self.command))
        }
    }
}

/// Writes the handle's callbacks so far, in the order they happened.
fn write_callbacks(output: &mut impl Write, handle: i64, recorder: Option<&Arc<Recorder>>) -> Result<(), String> {
    let Some(recorder) = recorder else { return Ok(()) };
    for callback in recorder.take() {
        match callback {
            Callback::Changed(version) => {
                write_record(output, CHANGED, &[&handle.to_le_bytes(), &version.to_le_bytes()])?;
            }
            Callback::Effect(effect) => write_record(output, EFFECT, &[&handle.to_le_bytes(), &effect])?,
        }
    }
    Ok(())
}

/// The command's one result. The registry's error type differs between the host and the device build; both are `Debug`.
fn write_result<E: Debug>(output: &mut impl Write, result: Result<Vec<u8>, E>) -> Result<(), String> {
    match result {
        Ok(value) => write_record(output, OK, &[&value]),
        Err(error) => write_record(output, ERROR, &[format!("{error:?}").as_bytes()]),
    }
}

fn reencode(kind: u8, bytes: &[u8]) -> Result<Result<Vec<u8>, String>, String> {
    Ok(match kind {
        1 => wire::decode::<Action>(bytes).map(|value| wire::encode(&value)),
        2 => wire::decode::<ChatEvent>(bytes).map(|value| wire::encode(&value)),
        3 => wire::decode::<ChatPatch>(bytes).map(|value| wire::encode(&value)),
        4 => wire::decode::<ChatSnapshot>(bytes).map(|value| wire::encode(&value)),
        5 => wire::decode::<Config>(bytes).map(|value| wire::encode(&value)),
        6 => wire::decode::<Effect>(bytes).map(|value| wire::encode(&value)),
        7 => wire::decode::<ListOp>(bytes).map(|value| wire::encode(&value)),
        8 => wire::decode::<Request>(bytes).map(|value| wire::encode(&value)),
        9 => wire::decode::<WireBatch>(bytes).map(|value| wire::encode(&value)),
        10 => wire::decode::<Payload>(bytes).map(|value| wire::encode(&value)),
        other => return Err(format!("reencode: unknown wire type {other}")),
    })
}

fn items_json(items: &[Item]) -> Vec<serde_json::Value> {
    items
        .iter()
        .map(|item| {
            serde_json::json!({
                "id": item.id,
                "seq": item.seq,
                "json": item.json(),
            })
        })
        .collect()
}

fn page_json(
    items: &[Item],
    history: Option<String>,
    from_seq: Option<i64>,
    to_seq: Option<i64>,
    backlog: Option<bool>,
    has_more: Option<bool>,
    more_after: Option<bool>,
) -> serde_json::Value {
    serde_json::json!({
        "accepted": true,
        "history": history,
        "from_seq": from_seq,
        "to_seq": to_seq,
        "backlog": backlog,
        "has_more": has_more,
        "more_after": more_after,
        "item_count": items.len(),
        "items": items_json(items),
    })
}

fn latest_json(page: LatestPage, backlog: Option<bool>) -> serde_json::Value {
    let LatestPage {
        items,
        has_more,
        history,
        to_seq,
    } = page;
    page_json(&items, history, None, to_seq, backlog, Some(has_more), None)
}

fn newer_json(page: NewerPage) -> serde_json::Value {
    let NewerPage {
        items,
        more_after,
        history,
        from_seq,
        to_seq,
    } = page;
    page_json(&items, history, from_seq, to_seq, Some(false), None, more_after)
}

fn range_json(page: RangePage) -> serde_json::Value {
    let RangePage { items, more } = page;
    page_json(&items, None, None, None, None, None, more)
}

/// A transport decoder's result, without any payload content in a refusal.
fn decode_transport(kind: u8, body: &[u8]) -> Result<serde_json::Value, String> {
    let decoded = match kind {
        1 | 3 | 4 => transport::decode_latest(body).map(|page| latest_json(page, (kind == 1).then_some(true))),
        2 => transport::decode_newer(body).map(newer_json),
        5 => transport::decode_range(body).map(range_json),
        6 => transport::decode_frame(body).map(|frame| match frame {
            Frame::Other => serde_json::json!({"accepted": true, "frame": "other"}),
            Frame::Items(frame) => serde_json::json!({
                "accepted": true,
                "frame": "items",
                "machine_id": frame.machine_id,
                "session_id": frame.session_id,
                "history": frame.history,
                "from_seq": frame.from_seq,
                "to_seq": frame.to_seq,
                "backlog": frame.backlog,
                "has_more": frame.has_more,
                "more_after": frame.more_after,
                "item_count": frame.items.len(),
                "items": items_json(&frame.items),
            }),
        }),
        other => return Err(format!("transport: unknown payload kind {other}")),
    };
    decoded.map_err(|error| serde_json::json!({"error": {"code": error.code(), "detail": error.detail()}}).to_string())
}

fn transport_json(kind: u8, body: &[u8]) -> Vec<u8> {
    let result = catch_unwind(AssertUnwindSafe(|| decode_transport(kind, body))).unwrap_or_else(|_| {
        Err(serde_json::json!({"error": {"code": "helper_error", "detail": "decode did not complete"}}).to_string())
    });
    match result {
        Ok(value) => value.to_string().into_bytes(),
        Err(message) => {
            debug_assert!(serde_json::from_str::<serde_json::Value>(&message).is_ok());
            message.into_bytes()
        }
    }
}

/// Values built here, never decoded from a client: the bytes the ArkTS codec must produce for, and read back as, the
/// same values written by hand in the test. Every enum variant appears, and neighbouring fields differ, so a codec
/// that swaps two variants or two fields, or gives an integer another width, cannot agree with these bytes.
fn samples() -> Vec<(&'static str, Vec<u8>)> {
    let snapshot = ChatSnapshot {
        items_json: r#"[{"id":"s#0","text":"snapshot é ✓ 🌍"}]"#.into(),
        history: Some("snapshot-history".into()),
        covered: 4_294_967_303,
        has_more: Some(true),
        loaded_once: false,
    };
    let batch = WireBatch {
        items_json: r#"[{"id":"b#0"}]"#.into(),
        history: Some("batch-history".into()),
        from_seq: Some(-3),
        to_seq: Some(8_589_934_597),
        backlog: true,
        has_more: Some(false),
        more_after: false,
    };
    let bare_batch = WireBatch {
        items_json: "[]".into(),
        history: None,
        from_seq: None,
        to_seq: None,
        backlog: false,
        has_more: None,
        more_after: true,
    };
    let insert = ListOp::Insert {
        index: 258,
        id: "7#1".into(),
        json: r#"{"id":"7#1","kind":"text"}"#.into(),
    };
    let update = ListOp::Update {
        index: 65_537,
        id: "7#2".into(),
        json: r#"{"id":"7#2","text":"new"}"#.into(),
    };
    let range = Request::FetchRange {
        fold_id: Some("fold:3#0".into()),
        from_seq: 2,
        through_seq: 8_589_934_600,
    };
    let event = |event: ChatEvent| wire::encode(&event);
    let request = |request: Request| wire::encode(&request);
    vec![
        (
            "Action.open_chat",
            wire::encode(&Action::OpenChat {
                chat: "chat/open".into(),
                snapshot: snapshot.clone(),
            }),
        ),
        (
            "Action.close_chat",
            wire::encode(&Action::CloseChat {
                chat: "chat/close".into(),
            }),
        ),
        (
            "Action.chat",
            wire::encode(&Action::Chat {
                chat: "chat/event".into(),
                event: ChatEvent::OpenFold {
                    fold_id: "fold:3#0".into(),
                },
            }),
        ),
        (
            "Action.continue_fill",
            wire::encode(&Action::ContinueFill {
                chat: "chat/fill".into(),
            }),
        ),
        (
            "Action.hub_changed",
            wire::encode(&Action::HubChanged {
                chat: "chat/hub".into(),
            }),
        ),
        (
            "Action.ack",
            wire::encode(&Action::Ack {
                chat: "chat/ack".into(),
                seq: 4_294_967_301,
            }),
        ),
        (
            "Action.wire_payload",
            wire::encode(&Action::WirePayload {
                chat: "chat/payload".into(),
                payload: Payload::Newer {
                    body: r#"{"items":[],"more_after":true}"#.into(),
                },
            }),
        ),
        ("ChatEvent.latest", event(ChatEvent::Latest { batch: batch.clone() })),
        (
            "ChatEvent.pushed",
            event(ChatEvent::Pushed {
                batch: bare_batch.clone(),
            }),
        ),
        ("ChatEvent.after", event(ChatEvent::After { batch: batch.clone() })),
        (
            "ChatEvent.reloaded",
            event(ChatEvent::Reloaded {
                items_json: r#"[{"id":"r#0"}]"#.into(),
                history: Some("reloaded-history".into()),
                to_seq: Some(77),
                has_more: true,
            }),
        ),
        (
            "ChatEvent.reloaded_bare",
            event(ChatEvent::Reloaded {
                items_json: "[]".into(),
                history: None,
                to_seq: None,
                has_more: false,
            }),
        ),
        (
            "ChatEvent.older",
            event(ChatEvent::Older {
                items_json: r#"[{"id":"o#0"}]"#.into(),
                has_more: true,
            }),
        ),
        (
            "ChatEvent.range",
            event(ChatEvent::Range {
                fold_id: None,
                items_json: r#"[{"id":"g#0"}]"#.into(),
                more: false,
            }),
        ),
        ("ChatEvent.reconnected", event(ChatEvent::Reconnected)),
        ("ChatEvent.load_older", event(ChatEvent::LoadOlder)),
        (
            "ChatEvent.open_fold",
            event(ChatEvent::OpenFold {
                fold_id: "fold:12#0".into(),
            }),
        ),
        ("ChatEvent.fill_failed", event(ChatEvent::FillFailed)),
        ("ChatEvent.older_failed", event(ChatEvent::OlderFailed)),
        (
            "ChatEvent.range_failed",
            event(ChatEvent::RangeFailed { fold_id: None }),
        ),
        (
            "ChatPatch.full",
            wire::encode(&ChatPatch {
                version: 4_294_967_298,
                covered: -9,
                history: Some("patch-history".into()),
                has_more: Some(true),
                loaded_once: false,
                verified: true,
                acked: Some(11),
                ops: vec![ListOp::Remove { index: 3 }, insert.clone(), update.clone()],
            }),
        ),
        (
            "ChatPatch.bare",
            wire::encode(&ChatPatch {
                version: 0,
                covered: 0,
                history: None,
                has_more: None,
                loaded_once: true,
                verified: false,
                acked: None,
                ops: Vec::new(),
            }),
        ),
        ("ChatSnapshot.full", wire::encode(&snapshot)),
        (
            "ChatSnapshot.bare",
            wire::encode(&ChatSnapshot {
                items_json: String::new(),
                history: None,
                covered: -2,
                has_more: None,
                loaded_once: true,
            }),
        ),
        ("Config.default", wire::encode(&Config::default())),
        (
            "Effect.fetch",
            wire::encode(&Effect::Fetch {
                chat: "chat/effect".into(),
                request: range.clone(),
            }),
        ),
        (
            "Effect.rejected",
            wire::encode(&Effect::Rejected {
                reason: "rejected ✗".into(),
            }),
        ),
        ("ListOp.remove", wire::encode(&ListOp::Remove { index: u32::MAX })),
        ("ListOp.insert", wire::encode(&insert)),
        ("ListOp.update", wire::encode(&update)),
        ("Payload.latest", wire::encode(&Payload::Latest { body: "{}".into() })),
        (
            "Payload.newer",
            wire::encode(&Payload::Newer {
                body: r#"{"items":[],"more_after":true}"#.into(),
            }),
        ),
        (
            "Payload.reloaded",
            wire::encode(&Payload::Reloaded {
                body: r#"{"items":[],"has_more":false}"#.into(),
            }),
        ),
        (
            "Payload.older",
            wire::encode(&Payload::Older {
                body: r#"{"items":[],"has_more":true}"#.into(),
            }),
        ),
        (
            "Payload.range",
            wire::encode(&Payload::Range {
                fold_id: None,
                body: r#"{"items":[],"more":true}"#.into(),
            }),
        ),
        (
            "Payload.frame",
            wire::encode(&Payload::Frame {
                body: r#"{"type":"items","machine_id":"m1","session_id":"s1"}"#.into(),
            }),
        ),
        (
            "Request.fetch_after",
            request(Request::FetchAfter {
                after_seq: 4_294_967_301,
            }),
        ),
        (
            "Request.fetch_latest",
            request(Request::FetchLatest { fold_work: true }),
        ),
        (
            "Request.subscribe",
            request(Request::Subscribe {
                since_seq: Some(41),
                fold_work: false,
            }),
        ),
        (
            "Request.subscribe_from_start",
            request(Request::Subscribe {
                since_seq: None,
                fold_work: true,
            }),
        ),
        (
            "Request.fetch_older",
            request(Request::FetchOlder {
                before_seq: 12,
                fold_work: false,
            }),
        ),
        ("Request.fetch_range", request(range)),
        ("WireBatch.full", wire::encode(&batch)),
        ("WireBatch.bare", wire::encode(&bare_batch)),
    ]
}

fn run(input: &mut impl Read, output: &mut impl Write) -> Result<(), String> {
    let mut open: BTreeMap<i64, Arc<Recorder>> = BTreeMap::new();
    while let Some(frame) = read_frame(input)? {
        let Some((tag, bytes)) = frame.split_first() else {
            return Err("a frame is empty".into());
        };
        let fields = |command| Fields { command, bytes };
        match *tag {
            CONFIG => {
                fields("config").end()?;
                write_record(output, OK, &[&registry::config()])?;
            }
            CREATE => {
                if open.len() >= MAX_OPEN {
                    return Err(format!("create: more than {MAX_OPEN} handles are open"));
                }
                let recorder = Arc::new(Recorder::default());
                let created = registry::create(fields("create").rest(), recorder.clone());
                if let Ok(handle) = &created {
                    open.insert(*handle, recorder);
                }
                write_result(output, created.map(|handle| handle.to_le_bytes().to_vec()))?;
            }
            DISPATCH => {
                let mut fields = fields("dispatch");
                let handle = fields.handle()?;
                let queued = registry::dispatch(handle, fields.rest());
                write_result(output, queued.map(|queued| vec![u8::from(queued)]))?;
            }
            FLUSH => {
                let mut fields = fields("flush");
                let handle = fields.handle()?;
                let timeout = u32::from_le_bytes(fields.fixed()?);
                fields.end()?;
                if timeout > MAX_FLUSH_MS {
                    return Err(format!("flush: the timeout is over {MAX_FLUSH_MS} ms"));
                }
                let idle = registry::flush(handle, timeout);
                write_callbacks(output, handle, open.get(&handle))?;
                write_result(output, idle.map(|idle| vec![u8::from(idle)]))?;
            }
            DRAIN => {
                let mut fields = fields("drain");
                let handle = fields.handle()?;
                let Ok(chat) = std::str::from_utf8(fields.rest()) else {
                    return Err("drain: the chat is not UTF-8".into());
                };
                write_result(output, registry::drain(handle, chat))?;
            }
            CLOSE => {
                let mut fields = fields("close");
                let handle = fields.handle()?;
                fields.end()?;
                let closed = registry::close(handle);
                write_callbacks(output, handle, open.remove(&handle).as_ref())?;
                write_result(output, closed.map(|()| Vec::new()))?;
            }
            REENCODE => {
                let mut fields = fields("reencode");
                let [kind] = fields.fixed()?;
                write_result(output, reencode(kind, fields.rest())?)?;
            }
            SAMPLES => {
                fields("samples").end()?;
                for (name, bytes) in samples() {
                    let length = u8::try_from(name.len()).map_err(|_| "samples: a name is too long".to_owned())?;
                    write_record(output, SAMPLE, &[&[length], name.as_bytes(), &bytes])?;
                }
                write_record(output, OK, &[])?;
            }
            TRANSPORT => {
                let mut fields = fields("transport");
                let [kind] = fields.fixed()?;
                write_result(
                    output,
                    serde_json::from_slice::<serde_json::Value>(&transport_json(kind, fields.rest()))
                        .map(|value| value.to_string().into_bytes()),
                )?;
            }
            SORT => {
                let fields = fields("sort");
                let input = fields.rest();
                let result = catch_unwind(AssertUnwindSafe(|| {
                    let Ok(input) = serde_json::from_slice::<persona_core::sort::Input>(input) else {
                        return Err("sort: input does not decode".to_owned());
                    };
                    let projection = persona_core::sort::project(&input);
                    serde_json::to_string(&projection).map_err(|e| e.to_string())
                }))
                .unwrap_or_else(|_| Err("sort: projection panicked".to_owned()));
                write_result(output, result.map(|json| json.into_bytes()))?;
            }
            other => return Err(format!("unknown command {other}")),
        }
        output
            .flush()
            .map_err(|error| format!("cannot write the output: {error}"))?;
    }
    for (handle, recorder) in open {
        // Closing joins the worker, so every callback it made is in the recorder by now.
        let _ = registry::close(handle);
        write_callbacks(output, handle, Some(&recorder))?;
    }
    write_record(output, END, &[])?;
    output
        .flush()
        .map_err(|error| format!("cannot write the output: {error}"))
}

fn main() -> ExitCode {
    let mut input = BufReader::new(io::stdin().lock());
    let mut output = BufWriter::new(io::stdout().lock());
    match run(&mut input, &mut output) {
        Ok(()) => ExitCode::SUCCESS,
        Err(message) => {
            // Results already written stay readable; the failure is the exit status and this line.
            let _ = output.flush();
            let _ = writeln!(io::stderr(), "host_replay: {message}");
            ExitCode::from(2)
        }
    }
}
