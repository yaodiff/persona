//! Rules read from the Swift decoders that the shared wire fixtures do not cover, and input that is not a payload at
//! all. The expectations in the first group were derived by reading the cited Swift and swift-foundation's
//! `JSONDecoder.swift`, not by running anything on iOS.

use super::*;

const ITEM: &str = r#"{"id":"a#0","seq":3,"role":"assistant","kind":"text","text":"hello"}"#;

fn frame(extra: &str) -> String {
    format!(r#"{{"type":"items","machine_id":"m1","session_id":"s1","items":[{ITEM}]{extra}}}"#)
}

fn refused<T: std::fmt::Debug>(result: Result<T, DecodeError>) -> String {
    result.expect_err("refused").to_string()
}

fn items_frame(json: &str) -> ItemsFrame {
    match decode_frame(json.as_bytes()).expect("frame") {
        Frame::Items(frame) => frame,
        Frame::Other => panic!("not an items frame"),
    }
}

// `ServerEvent.decode` (apple/Shared/LiveConnection.swift:230-231, 247-250): the envelope decodes first, the key
// needs both ids, and the defaults are `items ?? []`, `backlog ?? false`, `moreAfter ?? false`.
#[test]
fn frame_defaults_and_key() {
    let frame = items_frame(r#"{"type":"items","machine_id":"","session_id":"s1"}"#);
    assert_eq!((frame.machine_id.as_str(), frame.session_id.as_str()), ("", "s1"));
    assert!(frame.items.is_empty() && !frame.backlog && !frame.more_after);
    assert_eq!(
        (frame.has_more, frame.history, frame.from_seq, frame.to_seq),
        (None, None, None, None)
    );

    let nulls = r#"{"type":"items","machine_id":"m1","session_id":"s1","items":null,"backlog":null,"has_more":null,
        "more_after":null,"history":null,"from_seq":null,"to_seq":null,"revs":null,"text":null}"#;
    let frame = items_frame(nulls);
    assert!(frame.items.is_empty() && !frame.backlog && !frame.more_after && frame.has_more.is_none());

    assert_eq!(
        refused(decode_frame(br#"{"type":"items","session_id":"s1"}"#)),
        "missing_key: machine_id"
    );
    assert_eq!(
        refused(decode_frame(br#"{"type":"items","machine_id":"m1","session_id":null}"#)),
        "missing_key: session_id"
    );
    assert_eq!(
        refused(decode_frame(br#"{"type":"items","machine_id":7,"session_id":"s1"}"#)),
        "wrong_type: machine_id"
    );
}

// `Envelope.type` is a non-optional `String` (LiveConnection.swift:103): without it no event decodes.
#[test]
fn frame_type() {
    assert_eq!(refused(decode_frame(br#"{"machine_id":"m1"}"#)), "missing_key: type");
    assert_eq!(refused(decode_frame(br#"{"type":null}"#)), "missing_key: type");
    assert_eq!(refused(decode_frame(br#"{"type":1}"#)), "wrong_type: type");
    assert!(matches!(
        decode_frame(br#"{"type":"future_event","to_seq":"x"}"#),
        Ok(Frame::Other)
    ));
    assert!(matches!(decode_frame(br#"{"type":""}"#), Ok(Frame::Other)));
    // An escaped spelling is still the same string, in the value and in the key.
    assert!(matches!(
        decode_frame(br#"{"type":"it\u0065ms","machine_id":"m1","session_id":"s1"}"#),
        Ok(Frame::Items(_))
    ));
    assert!(matches!(
        decode_frame(br#"{"\u0074ype":"items","machine_id":"m1","session_id":"s1"}"#),
        Ok(Frame::Items(_))
    ));
}

/// How iOS's `Envelope` declares a key the core checks.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Declared {
    Str,
    Bool,
    Int,
    Items,
}

impl Declared {
    /// Values the declaration refuses, and values it takes (`null` is tried for every key).
    fn samples(self) -> (&'static [&'static str], &'static [&'static str]) {
        match self {
            Declared::Str => (&["7", "true", r#"["a"]"#, r#"{"a":"b"}"#], &[r#""x""#, r#""""#]),
            Declared::Bool => (&["1", "0", r#""true""#, "[]", "{}"], &["true", "false"]),
            Declared::Int => (
                &[r#""1""#, "1.5", "1.0", "1e2", "true", "1e30", "[1]", "{}"],
                &["1", "-1"],
            ),
            Declared::Items => (&["{}", r#""x""#, "1", "true"], &["[]"]),
        }
    }
}

/// The keys of iOS's `Envelope` (apple/Shared/LiveConnection.swift) an `items` frame carries, as Swift declares them.
const ENVELOPE: [(&str, Declared); 10] = [
    ("type", Declared::Str),
    ("machine_id", Declared::Str),
    ("session_id", Declared::Str),
    ("items", Declared::Items),
    ("backlog", Declared::Bool),
    ("has_more", Declared::Bool),
    ("more_after", Declared::Bool),
    ("history", Declared::Str),
    ("from_seq", Declared::Int),
    ("to_seq", Declared::Int),
];

/// Envelope keys only other events use, of every declared shape (a string, a boolean, a whole number, a list, a map,
/// a struct, raw JSON), and a key nobody declares. The core does not check them on an `items` frame: the documented
/// difference from iOS, which refuses the frame when one of the declared ones has the wrong type.
const OTHER_EVENTS: [&str; 9] = [
    "text", "git", "rows", "features", "revs", "fleet", "machines", "args", "future",
];

/// An `items` frame with `key` set to `value`, in place of the frame's own value when it has one.
fn frame_with(key: &str, value: &str) -> String {
    let mut pairs: Vec<(&str, String)> = vec![
        ("type", r#""items""#.to_owned()),
        ("machine_id", r#""m1""#.to_owned()),
        ("session_id", r#""s1""#.to_owned()),
        ("items", format!("[{ITEM}]")),
    ];
    match pairs.iter_mut().find(|(name, _)| *name == key) {
        Some(pair) => pair.1 = value.to_owned(),
        None => pairs.push((key, value.to_owned())),
    }
    let body: Vec<String> = pairs
        .iter()
        .map(|(name, value)| format!(r#""{name}":{value}"#))
        .collect();
    format!("{{{}}}", body.join(","))
}

// A wrong type on a key the frame carries refuses it, with every kind of wrong value for the key's type
// (LiveConnection.swift: `Envelope` decodes as a whole before `ServerEvent.decode` reads it).
#[test]
fn frame_wrong_types_on_envelope_keys() {
    assert_eq!(
        Envelope::KEYS,
        ENVELOPE.map(|(key, _)| key),
        "the keys `Envelope` reads are the ones tried here"
    );
    let mut refusals = 0;
    for (key, declared) in ENVELOPE {
        let (wrong, right) = declared.samples();
        for value in wrong {
            assert_eq!(
                refused(decode_frame(frame_with(key, value).as_bytes())),
                format!("wrong_type: {key}"),
                "{key}: {value}"
            );
            refusals += 1;
        }
        for value in right {
            match decode_frame(frame_with(key, value).as_bytes()) {
                Ok(Frame::Items(_)) => {}
                Ok(Frame::Other) => assert_eq!(key, "type", "{key}: {value}"),
                Err(error) => panic!("{key}: {value}: {error}"),
            }
        }
        // `null` is "no value" for every key; only the three the frame needs then refuse it.
        match decode_frame(frame_with(key, "null").as_bytes()) {
            Ok(Frame::Items(_)) => assert!(!matches!(key, "type" | "machine_id" | "session_id"), "{key}"),
            Ok(Frame::Other) => panic!("{key}: null"),
            Err(error) => assert_eq!(error.to_string(), format!("missing_key: {key}")),
        }
    }
    assert_eq!(refusals, 51, "wrong values tried");
}

// The documented difference: keys only other events use take anything on an `items` frame, also what the parser
// could not hold as a value (the core skips them unread).
#[test]
fn keys_of_other_events_are_not_checked() {
    for key in OTHER_EVENTS {
        for value in [
            "7",
            "1.5",
            "1e999",
            r#""x""#,
            r#""\ud83d""#,
            r#"["x",1]"#,
            "{}",
            "false",
            "null",
            r#"{"deep":[1,2,{"x":null}]}"#,
        ] {
            assert_eq!(items_frame(&frame_with(key, value)).items.len(), 1, "{key}: {value}");
        }
    }
}

// Frames of other types are not this decoder's: whatever their envelope holds, they are `Other`.
#[test]
fn other_frames_are_not_checked() {
    for json in [
        r#"{"type":"hello","to_seq":"x","items":7}"#,
        r#"{"type":"hello","id":"1","id":"2"}"#,
        r#"{"type":"hello","to_seq":1e999}"#,
        r#"{"type":"hello","history":"\ud83d"}"#,
        r#"{"type":"hello","items":[1,{"id":3}]}"#,
        r#"{"type":"hello","zz":"\ud83d","zz":1e999}"#,
        // The first `type` is the frame's.
        r#"{"type":"hello","type":"items","to_seq":1e999}"#,
    ] {
        assert!(matches!(decode_frame(json.as_bytes()), Ok(Frame::Other)), "{json}");
    }
    // An items frame is read in full, and a frame that is not JSON or names no other type is nobody's.
    for (json, code) in [
        (
            r#"{"type":"items","machine_id":"m","session_id":"s","to_seq":1e999}"#,
            "malformed_json",
        ),
        (
            r#"{"type":"items","type":"hello","machine_id":"m","session_id":"s","history":"\ud83d"}"#,
            "malformed_json",
        ),
        (r#"{"type":"hello","to_seq":1e999,}"#, "malformed_json"),
        (r#"{"type":"hello","to_seq":1e999"#, "malformed_json"),
        (r#"{"type":"hello","to_seq":1"#, "truncated_json"),
        (r#"{"to_seq":1e999}"#, "malformed_json"),
        (r#"{"type":7,"to_seq":1e999}"#, "malformed_json"),
    ] {
        assert_eq!(
            refused(decode_frame(json.as_bytes())).split(':').next(),
            Some(code),
            "{json}"
        );
    }
}

// `ItemsResponse` (apple/Shared/APIClient.swift:599-607): `items` and `has_more` are required.
#[test]
fn latest_page_keys() {
    let page = decode_latest(format!(r#"{{"items":[{ITEM}],"has_more":true}}"#).as_bytes()).unwrap();
    assert_eq!(
        (page.items.len(), page.has_more, page.history, page.to_seq),
        (1, true, None, None)
    );
    assert_eq!(refused(decode_latest(br#"{"items":[]}"#)), "missing_key: has_more");
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":null}"#)),
        "missing_key: has_more"
    );
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":"false"}"#)),
        "wrong_type: has_more"
    );
    assert_eq!(refused(decode_latest(br#"{"has_more":false}"#)), "missing_key: items");
    assert_eq!(
        refused(decode_latest(br#"{"items":null,"has_more":false}"#)),
        "missing_key: items"
    );
    assert_eq!(
        refused(decode_latest(br#"{"items":{},"has_more":false}"#)),
        "wrong_type: items"
    );
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":false,"to_seq":"9"}"#)),
        "wrong_type: to_seq"
    );
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":false,"history":1}"#)),
        "wrong_type: history"
    );
    // Not keys of this response: another response's keys are unknown here.
    assert!(decode_latest(br#"{"items":[],"has_more":false,"more_after":"x","from_seq":"x","more":3}"#).is_ok());
}

// `NewerResponse` (APIClient.swift:998-1010) and `Range` (APIClient.swift:941): only `items` is required.
#[test]
fn newer_and_range_page_keys() {
    let page = decode_newer(br#"{"items":[]}"#).unwrap();
    assert_eq!(
        (page.more_after, page.history.as_deref(), page.from_seq, page.to_seq),
        (None, None, None, None)
    );
    assert!(!page.into_batch().more_after);
    let page = decode_newer(br#"{"items":[],"more_after":false,"from_seq":0,"to_seq":0,"has_more":"x"}"#).unwrap();
    assert_eq!(
        (page.more_after, page.from_seq, page.to_seq),
        (Some(false), Some(0), Some(0))
    );
    assert_eq!(refused(decode_newer(br#"{"more_after":true}"#)), "missing_key: items");
    assert_eq!(
        refused(decode_newer(br#"{"items":[],"more_after":1}"#)),
        "wrong_type: more_after"
    );
    assert_eq!(
        refused(decode_newer(br#"{"items":[],"from_seq":[]}"#)),
        "wrong_type: from_seq"
    );

    assert!(!decode_range(br#"{"items":[]}"#).unwrap().into_parts().1);
    assert_eq!(decode_range(br#"{"items":[],"more":null}"#).unwrap().more, None);
    assert_eq!(
        decode_range(br#"{"items":[],"more":true,"has_more":7}"#).unwrap().more,
        Some(true)
    );
    assert_eq!(refused(decode_range(br#"{"more":true}"#)), "missing_key: items");
    assert_eq!(
        refused(decode_range(br#"{"items":[],"more":"true"}"#)),
        "wrong_type: more"
    );
}

// A whole number is an integer literal inside the 64-bit range. Foundation's `JSONDecoder` also reads an `Int` from
// a whole number written with a fraction or an exponent (swift-foundation, `JSONDecoder.swift`,
// `unwrapFixedWidthInteger` and its slow path); the core refuses that spelling (the module's "Known differences").
#[test]
fn whole_numbers() {
    for (text, want) in [
        ("8", 8),
        ("-8", -8),
        ("0", 0),
        ("9223372036854775807", i64::MAX),
        ("-9223372036854775808", i64::MIN),
        ("9007199254740993", 9_007_199_254_740_993),
    ] {
        let page = decode_latest(format!(r#"{{"items":[],"has_more":false,"to_seq":{text}}}"#).as_bytes());
        assert_eq!(page.expect(text).to_seq, Some(want), "{text}");
    }
    for text in [
        "8.5",
        "1e30",
        // Outside the 64-bit range. Foundation refuses these too.
        "9223372036854775808",
        "-9223372036854775809",
        "18446744073709551616",
        "123456789012345678901234567890",
        // Whole, but not an integer literal: Foundation takes these, the core does not.
        "8.0",
        "8e0",
        "80E-1",
        "-0.0",
        // The parser holds negative zero as a double, so the literal `-0` goes the same way.
        "-0",
        "1e-999",
        "9007199254740991.0",
        "9007199254740993.0",
        "9223372036854775807.0",
        "1e18",
        r#""8""#,
        "true",
        "[8]",
    ] {
        let page = decode_latest(format!(r#"{{"items":[],"has_more":false,"to_seq":{text}}}"#).as_bytes());
        assert_eq!(refused(page), "wrong_type: to_seq", "{text}");
    }
    // Beyond a double: the parser refuses the document. On an unknown key the number is never read.
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":false,"to_seq":1e999}"#))
            .split(':')
            .next(),
        Some("malformed_json")
    );
    assert!(decode_latest(br#"{"items":[],"has_more":false,"future":1e999}"#).is_ok());
    // The same rule inside an item and its fold.
    let item = |seq: &str, through: &str| {
        let fold = format!(r#"{{"items":1,"tools":0,"thoughts":0,"through_seq":{through},"through_id":"b"}}"#);
        let item = format!(r#"{{"id":"a","seq":{seq},"role":"assistant","kind":"fold","fold":{fold}}}"#);
        decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())
    };
    let read = item("9007199254740993", "9007199254740995").unwrap().items.remove(0);
    assert_eq!(
        (read.seq, read.fold.unwrap().through_seq),
        (9_007_199_254_740_993, 9_007_199_254_740_995)
    );
    assert_eq!(refused(item("3.0", "1")), "bad_item: items[0] wrong_type seq");
    assert_eq!(
        refused(item("1", "3.0")),
        "bad_item: items[0] wrong_type fold.through_seq"
    );
    assert_eq!(refused(item("1e999", "1")), "bad_item: items[0] unreadable");
}

// `Item` and `Fold` are synthesized `Decodable`s (apple/Shared/Models.swift:2104-2152, 1930-1950); `ItemKind` takes
// any string (Models.swift:1919-1926). One refused item refuses the payload.
#[test]
fn item_rules() {
    let page = |item: &str| decode_latest(format!(r#"{{"items":[{ITEM},{item}],"has_more":false}}"#).as_bytes());
    for (item, fault) in [
        (r#"{"seq":1,"role":"user","kind":"text"}"#, "missing_key id"),
        (r#"{"id":"a","role":"user","kind":"text"}"#, "missing_key seq"),
        (r#"{"id":"a","seq":1,"kind":"text"}"#, "missing_key role"),
        (r#"{"id":"a","seq":1,"role":"user"}"#, "missing_key kind"),
        (
            r#"{"id":"a","seq":null,"role":"user","kind":"text"}"#,
            "missing_key seq",
        ),
        (r#"{"id":1,"seq":1,"role":"user","kind":"text"}"#, "wrong_type id"),
        (r#"{"id":"a","seq":"1","role":"user","kind":"text"}"#, "wrong_type seq"),
        (r#"{"id":"a","seq":1.5,"role":"user","kind":"text"}"#, "wrong_type seq"),
        (r#"{"id":"a","seq":1,"role":5,"kind":"text"}"#, "wrong_type role"),
        (r#"{"id":"a","seq":1,"role":"user","kind":7}"#, "wrong_type kind"),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","text":1}"#,
            "wrong_type text",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","tool_use_id":1}"#,
            "wrong_type tool_use_id",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","tool_name":[]}"#,
            "wrong_type tool_name",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","from":{}}"#,
            "wrong_type from",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","client_msg_id":0}"#,
            "wrong_type client_msg_id",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","is_error":"no"}"#,
            "wrong_type is_error",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","queued":1}"#,
            "wrong_type queued",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","wake":"x"}"#,
            "wrong_type wake",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","supervision_user":0}"#,
            "wrong_type supervision_user",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","full_size":"9"}"#,
            "wrong_type full_size",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":"x"}"#,
            "wrong_type fold",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":[]}"#,
            "wrong_type fold",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{}}"#,
            "missing_key fold.items",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"thoughts":0,"through_seq":2,"through_id":"b"}}"#,
            "missing_key fold.tools",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"through_seq":2,"through_id":"b"}}"#,
            "missing_key fold.thoughts",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_id":"b"}}"#,
            "missing_key fold.through_seq",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_seq":2}}"#,
            "missing_key fold.through_id",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":"0","thoughts":0,"through_seq":2,"through_id":"b"}}"#,
            "wrong_type fold.tools",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":3}}"#,
            "wrong_type fold.through_id",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":"b","supervision_user":1}}"#,
            "wrong_type fold.supervision_user",
        ),
        ("null", "not_object"),
        ("7", "not_object"),
        (r#"["a",1,"user","text"]"#, "not_object"),
        // What the parser cannot hold, in a key the core reads, in a key's name, or anywhere in a fold.
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","text":"\ud83d"}"#,
            "unreadable",
        ),
        (r#"{"id":"\udc00","seq":1,"role":"user","kind":"text"}"#, "unreadable"),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","full_size":1e999}"#,
            "unreadable",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"text","\ud83d":1}"#,
            "unreadable",
        ),
        (
            r#"{"id":"a","seq":1,"role":"user","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":"b","zz":1e999}}"#,
            "unreadable",
        ),
    ] {
        assert_eq!(refused(page(item)), format!("bad_item: items[1] {fault}"), "{item}");
    }
    // What iOS takes: nulls on optional keys, an unknown kind, unknown keys.
    let kept = r#"{ "id":"f#0", "seq":4, "role":"assistant", "kind":"future_kind", "text":null, "tool_use_id":null,
        "is_error":null, "full_size":null, "supervision_user":null, "ts":17, "future":[{"a":1e999}],
        "fold":{"items":3,"tools":1,"thoughts":1,"through_seq":6,"through_id":"g#2","until":5,"supervision_user":true} }"#;
    let item = page(kept).unwrap().items.remove(1);
    assert_eq!(
        (item.id.as_str(), item.seq, item.kind.as_str(), item.role.as_str()),
        ("f#0", 4, "future_kind", "assistant")
    );
    assert_eq!(item.tool_use_id, None);
    let fold = item.fold.as_ref().expect("fold");
    assert_eq!((fold.through_seq, fold.through_id.as_str()), (6, "g#2"));
    assert!(item.has_supervision_evidence() && !item.supervision_user);
    assert_eq!(item.json(), kept, "the item's bytes are kept, whitespace included");
    let item = page(r#"{"id":"a","seq":1,"role":"user","kind":"image","tool_use_id":"t1"}"#)
        .unwrap()
        .items
        .remove(1);
    assert_eq!(item.tool_use_id.as_deref(), Some("t1"));
    assert!(
        page(r#"{"id":"a","seq":1,"role":"user","kind":"text","fold":null}"#)
            .unwrap()
            .items[1]
            .fold
            .is_none()
    );
    // What the parser cannot hold is not looked at in a key the core does not read.
    assert!(page(r#"{"id":"a","seq":1,"role":"user","kind":"tool_use","tool_input":"\ud83d","source":1e999}"#).is_ok());
}

// `Item.supervisionUser` and `Fold.supervisionUser` (Models.swift): the item's own flag is evidence by itself, and
// so is its fold's; `false` is not.
#[test]
fn supervision_evidence() {
    const FOLD: &str = r#""items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":"b""#;
    for (extra, own, evidence) in [
        (r#","supervision_user":true"#.to_owned(), true, true),
        (r#","supervision_user":false"#.to_owned(), false, false),
        (String::new(), false, false),
        (format!(r#","fold":{{{FOLD}}}"#), false, false),
        (format!(r#","fold":{{{FOLD},"supervision_user":false}}"#), false, false),
        (format!(r#","fold":{{{FOLD},"supervision_user":true}}"#), false, true),
        (
            format!(r#","supervision_user":true,"fold":{{{FOLD},"supervision_user":false}}"#),
            true,
            true,
        ),
        (
            format!(r#","supervision_user":false,"fold":{{{FOLD},"supervision_user":true}}"#),
            false,
            true,
        ),
    ] {
        let item = format!(r#"{{"id":"a","seq":1,"role":"user","kind":"text"{extra}}}"#);
        let item = decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())
            .unwrap()
            .items
            .remove(0);
        assert_eq!(
            (item.supervision_user, item.has_supervision_evidence()),
            (own, evidence),
            "{extra}"
        );
    }
}

// One refused item refuses the page at once: the first one is named and the rest is skipped, not read.
#[test]
fn the_first_refused_item_is_reported() {
    let page = format!(r#"{{"items":[{ITEM},{{"id":"b"}},7,{{"id":"c"}},null],"has_more":false}}"#);
    assert_eq!(
        refused(decode_latest(page.as_bytes())),
        "bad_item: items[1] missing_key seq"
    );
    assert_eq!(
        refused(decode_range(br#"{"items":[1,1,1,1]}"#)),
        "bad_item: items[0] not_object"
    );
    assert_eq!(
        refused(decode_frame(
            frame_with("items", &format!("[{ITEM},{ITEM},[]]")).as_bytes()
        )),
        "bad_item: items[2] not_object"
    );
    // The page's own keys are reported before its items, and broken JSON after the refused item is still broken.
    assert_eq!(
        refused(decode_latest(br#"{"items":[1],"has_more":"x"}"#)),
        "wrong_type: has_more"
    );
    assert_eq!(
        refused(decode_range(br#"{"items":[1,{"id":],"more":true}"#))
            .split(':')
            .next(),
        Some("malformed_json")
    );
    assert_eq!(
        refused(decode_range(br#"{"items":[1,2"#)).split(':').next(),
        Some("truncated_json")
    );
    let junk = format!(r#"{{"items":[{}1]}}"#, "1,".repeat(200_000));
    assert_eq!(refused(decode_range(junk.as_bytes())), "bad_item: items[0] not_object");
}

// The documented difference: structs the core does not read are not checked.
#[test]
fn nested_structs_the_core_does_not_check() {
    let item = r#"{"id":"a","seq":1,"role":"user","kind":"image","image":"x","task":3,"compact":[],"injected":1,"model_refusal":false}"#;
    assert!(decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes()).is_ok());
    assert!(decode_frame(frame(r#","sessions":"x","fleet":7,"interaction":[]"#).as_bytes()).is_ok());
    // iOS reads these as `JSONValue`, whose numbers must fit a double; the core does not read them.
    assert!(decode_frame(frame(r#","args":1e999,"account":{"a":[1e-999]}"#).as_bytes()).is_ok());
}

#[test]
fn an_error_never_repeats_the_payload() {
    let secret = "s3cret-words";
    for payload in [
        format!(r#"{{"items":[{{"id":"{secret}","seq":"{secret}","role":"user","kind":"text"}}],"has_more":false}}"#),
        format!(r#"{{"items":[],"has_more":"{secret}"}}"#),
        format!(r#"{{"items":[],"has_more":false,"history":["{secret}"]}}"#),
        format!(r#"{{"items":[],"has_more":"{secret}","has_more":false}}"#),
        format!(r#"{{"items":[],"has_more":false,"{secret}\ud83d":1}}"#),
        format!(r#"{{"items":[],"has_more":false,"history":"{secret}\ud83d"}}"#),
        format!(r#"{{"items":[{{"id":"{secret}\ud83d","seq":1,"role":"user","kind":"text"}}],"has_more":false}}"#),
        format!(r#"{{"items":[],"has_more":false,"x":"{secret}"#),
        format!(r#"["{secret}"]"#),
        format!(r#""{secret}""#),
    ] {
        let error = decode_latest(payload.as_bytes()).expect_err(&payload);
        let shown = format!("{error} {error:?} {} {}", error.code(), error.detail());
        assert!(!shown.contains(secret), "{shown}");
    }
}

fn all(bytes: &[u8]) -> [String; 4] {
    [
        refused(decode_latest(bytes)),
        refused(decode_newer(bytes)),
        refused(decode_range(bytes)),
        refused(decode_frame(bytes)),
    ]
}

#[test]
fn not_json() {
    for (bytes, code) in [
        (&b""[..], "truncated_json"),
        (b"   ", "truncated_json"),
        (b"{", "truncated_json"),
        (br#"{"items":[{"id":"a","se"#, "truncated_json"),
        (br#"{"items":[],"has_more":false"#, "truncated_json"),
        (br#"{"items":[],"has_more":false} x"#, "malformed_json"),
        (br#"{"items":[],"has_more":false}{}"#, "malformed_json"),
        (br#"{"items":[,],"has_more":false}"#, "malformed_json"),
        (br#"{'items':[]}"#, "malformed_json"),
        (br#"{"items":[01],"has_more":false}"#, "malformed_json"),
        (br#"{"items":[],"x":1,}"#, "malformed_json"),
        (br#"{"items":[],7:1}"#, "malformed_json"),
        (b"nul", "truncated_json"),
        (b"[1,", "truncated_json"),
        (b"\xef\xbb\xbf{}", "malformed_json"),
        (b"{\"items\":[],\"has_more\":false,\"x\":\"\xff\"}", "invalid_utf8"),
        (b"\xff\xfe{\x00}\x00", "invalid_utf8"),
    ] {
        for error in all(bytes) {
            assert_eq!(
                error.split(':').next(),
                Some(code),
                "{:?}",
                String::from_utf8_lossy(bytes)
            );
        }
    }
}

#[test]
fn root_is_not_an_object() {
    for text in [
        "null",
        "true",
        "7",
        r#""items""#,
        "[]",
        r#"[[],false,null,null]"#,
        r#" [ {"type":"items"} ] "#,
    ] {
        assert_eq!(
            all(text.as_bytes()),
            ["not_object", "not_object", "not_object", "not_object"],
            "{text}"
        );
    }
}

// Foundation's keyed container keeps the first value of a key that appears again (swift-foundation,
// `JSONDecoder.swift`, `KeyedContainer.stringify`: `result[key]._setIfNil(to: value)`). Not executed on iOS here.
#[test]
fn a_repeated_key_keeps_its_first_value() {
    let page = decode_latest(
        br#"{"items":[],"has_more":true,"has_more":false,"to_seq":4,"to_seq":"x","history":"h1","history":"h2","items":7}"#,
    )
    .unwrap();
    assert_eq!(
        (page.items.len(), page.has_more, page.to_seq, page.history.as_deref()),
        (0, true, Some(4), Some("h1"))
    );
    // The first value is the one checked, also when it is wrong or `null`.
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":"x","has_more":false}"#)),
        "wrong_type: has_more"
    );
    assert_eq!(
        refused(decode_latest(br#"{"items":[],"has_more":null,"has_more":false}"#)),
        "missing_key: has_more"
    );
    let page = decode_newer(br#"{"items":[],"to_seq":null,"to_seq":1,"more_after":true,"more_after":false}"#).unwrap();
    assert_eq!((page.to_seq, page.more_after), (None, Some(true)));
    assert_eq!(
        decode_range(br#"{"items":[],"more":true,"more":false}"#).unwrap().more,
        Some(true)
    );
    // A later value is skipped, but the document must still be JSON.
    assert_eq!(
        refused(decode_range(br#"{"items":[],"more":true,"more":fals}"#))
            .split(':')
            .next(),
        Some("malformed_json")
    );
    // A key the decoder does not read may repeat too.
    assert!(decode_range(br#"{"items":[],"x":1,"x":2}"#).is_ok());

    let frame = items_frame(
        r#"{"type":"items","machine_id":"m1","machine_id":"m2","session_id":"s1","type":"hello","backlog":true,"backlog":0}"#,
    );
    assert_eq!((frame.machine_id.as_str(), frame.backlog), ("m1", true));
    assert!(matches!(
        decode_frame(br#"{"type":"hello","type":"items"}"#),
        Ok(Frame::Other)
    ));

    // In an item and in its fold.
    let item = r#"{"id":"a#1","id":"b#2","seq":1,"seq":"x","role":"assistant","kind":"fold","kind":"text","tool_use_id":null,"tool_use_id":"t","supervision_user":true,"supervision_user":false,"fold":{"items":1,"tools":0,"thoughts":0,"through_seq":2,"through_seq":9,"through_id":"x#1","through_id":"y#2"},"fold":null}"#;
    let read = decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())
        .unwrap()
        .items
        .remove(0);
    assert_eq!(
        (
            read.id.as_str(),
            read.seq,
            read.kind.as_str(),
            read.tool_use_id.as_deref()
        ),
        ("a#1", 1, "fold", None)
    );
    let fold = read.fold.as_ref().expect("the first fold");
    assert_eq!((fold.through_seq, fold.through_id.as_str()), (2, "x#1"));
    assert!(read.supervision_user);
    assert_eq!(read.json(), item, "the bytes are kept, repeats included");

    let with_fold = |fold: &str| {
        let item = format!(r#"{{"id":"a","seq":1,"role":"assistant","kind":"fold","fold":{fold}}}"#);
        decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes()).map(|mut page| page.items.remove(0))
    };
    let item = with_fold(
        r#"{"items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":"b","supervision_user":null,"supervision_user":true}"#,
    );
    assert!(!item.unwrap().has_supervision_evidence());
    assert_eq!(
        refused(with_fold(
            r#"{"items":1,"tools":0,"thoughts":0,"through_seq":"x","through_seq":2,"through_id":"b"}"#
        )),
        "bad_item: items[0] wrong_type fold.through_seq"
    );
    // `"fold":null` first: no fold, whatever follows.
    let item = r#"{"id":"a","seq":1,"role":"user","kind":"text","fold":null,"fold":7}"#;
    let page = decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes()).unwrap();
    assert!(page.items[0].fold.is_none());
}

#[test]
fn deep_nesting_does_not_overflow_the_stack() {
    let depth = 200_000;
    let deep = format!("{}{}", "[".repeat(depth), "]".repeat(depth));
    // Skipped without recursion: on an unknown key, and inside an item's JSON the core does not read.
    assert!(decode_range(format!(r#"{{"items":[],"x":{deep}}}"#).as_bytes()).is_ok());
    let item = format!(r#"{{"id":"a","seq":1,"role":"user","kind":"tool_use","tool_input":{deep}}}"#);
    let page = decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes()).unwrap();
    assert_eq!(page.items[0].json(), item);
    // Read into a tree, so bounded: an item's fold.
    let fold = r#""items":1,"tools":0,"thoughts":0,"through_seq":2,"through_id":"b""#;
    let item = format!(r#"{{"id":"a","seq":1,"role":"user","kind":"fold","fold":{{{fold},"x":{deep}}}}}"#);
    assert_eq!(
        refused(decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())),
        "bad_item: items[0] unreadable"
    );
    // A known key holding a deep value of the wrong type is skipped the same way: at the root, in an item, in a
    // fold, in a repeated key and in a frame of another type.
    assert_eq!(
        refused(decode_range(format!(r#"{{"items":[],"more":{deep}}}"#).as_bytes())),
        "wrong_type: more"
    );
    let item = format!(r#"{{"id":"a","seq":1,"role":"user","kind":"text","text":{deep}}}"#);
    assert_eq!(
        refused(decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())),
        "bad_item: items[0] wrong_type text"
    );
    let item = format!(r#"{{"id":"a","seq":1,"role":"user","kind":"fold","fold":{{"items":{deep}}}}}"#);
    assert_eq!(
        refused(decode_range(format!(r#"{{"items":[{item}]}}"#).as_bytes())),
        "bad_item: items[0] wrong_type fold.items"
    );
    assert!(decode_range(format!(r#"{{"items":[],"more":true,"more":{deep}}}"#).as_bytes()).is_ok());
    assert!(matches!(
        decode_frame(format!(r#"{{"type":"hello","revs":{deep},"items":{deep}}}"#).as_bytes()),
        Ok(Frame::Other)
    ));
    // Unbalanced.
    for error in all("[".repeat(depth).as_bytes()) {
        assert_eq!(error.split(':').next(), Some("truncated_json"));
    }
    for error in all(format!(r#"{{"x":{}"#, "{\"a\":".repeat(depth)).as_bytes()) {
        assert_eq!(error.split(':').next(), Some("truncated_json"));
    }
}

#[test]
fn a_large_page_keeps_every_item() {
    let items: Vec<String> = (0..5000)
        .map(|n| format!(r#"{{"id":"l{n}#0","seq":{n},"role":"assistant","kind":"text","text":"line \"{n}\" é\n"}}"#))
        .collect();
    let body = format!(r#"{{"has_more":true,"to_seq":4999,"items":[{}]}}"#, items.join(" ,\n"));
    let page = decode_latest(body.as_bytes()).unwrap();
    assert_eq!(page.items.len(), 5000);
    assert!(page.items.iter().zip(&items).all(|(item, raw)| item.json() == raw));
}
