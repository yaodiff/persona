//! Replays `spec/fixtures/wire`'s continuity payloads as `Action::WirePayload`: the JSON exactly as the file carries
//! it, decoded once by `persona_core::transport` on the worker. Each item's JSON must reach the patch byte for byte,
//! and a payload that is fed again as the equivalent hand-built `ChatEvent` must give the same patch and effects.

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use persona_core_runtime::wire::{
    self, Action, ChatEvent, ChatPatch, ChatSnapshot, Effect, ListOp, Payload, WireBatch,
};
use persona_core_runtime::{Host, Runtime};
use serde_json::{Map, Value, json};

const CHAT: &str = "m1/s1";

#[derive(Default)]
struct Recorder {
    effects: Mutex<Vec<Effect>>,
}

impl Host for Recorder {
    fn changed(&self, _version: u64) {}
    fn run(&self, effect: &[u8]) {
        self.effects
            .lock()
            .unwrap()
            .push(wire::decode(effect).expect("effect decodes"));
    }
}

fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../spec/fixtures/wire")
}

/// One in-scope case, with the source text of its `input` exactly as the file holds it. Re-serialising a parsed
/// `Value` would prove nothing about byte preservation, so the raw text is cut out of the document itself.
struct Case {
    name: String,
    decoder: String,
    input: Value,
    input_text: String,
    expected: Map<String, Value>,
}

fn cases() -> Vec<Case> {
    let mut result = Vec::new();
    for file in ["items.json", "item_events.json", "compatibility.json"] {
        let path = fixture_dir().join(file);
        let source = std::fs::read_to_string(&path).unwrap();
        let document: Value = serde_json::from_str(&source).unwrap();
        for case in document.as_array().unwrap() {
            let object = case.as_object().unwrap();
            let decoder = object["decoder"].as_str().unwrap().to_owned();
            let is_items_frame = matches!(decoder.as_str(), "event" | "compat_event")
                && object["input"]["type"].as_str() == Some("items");
            if !matches!(decoder.as_str(), "items" | "items_after" | "items_range") && !is_items_frame {
                continue;
            }
            let input_text = raw_value_text(
                &source,
                &format!("\"name\": \"{}\"", object["name"].as_str().unwrap()),
                "input",
            );
            result.push(Case {
                name: object["name"].as_str().unwrap().to_owned(),
                decoder,
                input: object["input"].clone(),
                input_text,
                expected: object["expected"].as_object().unwrap().clone(),
            });
        }
    }
    result
}

/// The text of `key`'s value inside the case object that contains `marker`, found by a small JSON scanner so the
/// original bytes (whitespace, escapes, number spellings) survive. Falls back to a clear failure if the shape changes.
fn raw_value_text(source: &str, marker: &str, key: &str) -> String {
    let start = source.find(marker).unwrap_or_else(|| panic!("case marker {marker}"));
    let after = &source[start..];
    let key_at = after
        .find(&format!("\"{key}\""))
        .unwrap_or_else(|| panic!("key {key} after {marker}"));
    let after_key = &after[key_at + key.len() + 2..];
    let colon = after_key.find(':').expect("a colon after the key");
    let value_start = after_key[colon + 1..]
        .find(|c: char| !c.is_whitespace())
        .expect("a value")
        + colon
        + 1;
    let value = &after_key[value_start..];
    let end = scan_value_end(value);
    value[..end].to_owned()
}

/// The length of the JSON value at the start of `text`, handling strings and nesting. Only used for test inputs.
fn scan_value_end(text: &str) -> usize {
    let bytes = text.as_bytes();
    let mut index = 0;
    let mut depth = 0i32;
    let mut in_string = false;
    let mut escaped = false;
    while index < bytes.len() {
        let byte = bytes[index];
        if in_string {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                in_string = false;
            }
            index += 1;
            continue;
        }
        match byte {
            b'"' => in_string = true,
            b'{' | b'[' => depth += 1,
            b'}' | b']' => {
                depth -= 1;
                if depth == 0 {
                    return index + 1;
                }
            }
            b',' | b' ' | b'\n' | b'\r' | b'\t' if depth == 0 => return index,
            _ => {}
        }
        index += 1;
    }
    text.len()
}

fn items_of(input: &Value) -> Vec<Value> {
    input["items"].as_array().cloned().unwrap_or_default()
}

/// The chat id a payload is routed to. For a WebSocket frame the core compares it with the frame's own
/// `machine_id` and `session_id`, so a frame is routed by the id it carries; HTTP pages use the chat the client
/// asked about.
fn chat_for(case: &Case) -> String {
    if case.decoder == "items" || case.decoder == "items_after" || case.decoder == "items_range" {
        return CHAT.into();
    }
    case.input["machine_id"].as_str().unwrap_or(CHAT).to_owned()
}

fn open(host: &Arc<Recorder>) -> Runtime {
    open_chat(host, CHAT)
}

fn open_chat(host: &Arc<Recorder>, chat: &str) -> Runtime {
    let runtime = Runtime::new(host.clone());
    assert!(runtime.dispatch(&wire::encode(&Action::OpenChat {
        chat: chat.into(),
        snapshot: ChatSnapshot::default(),
    })));
    // The open is its own change; drain it before any payload is dispatched so the next drain reports only that
    // payload's effects.
    assert!(runtime.flush(Duration::from_secs(5)));
    runtime.drain(CHAT);
    host.effects.lock().unwrap().clear();
    runtime
}

fn drain(runtime: &Runtime, host: &Arc<Recorder>) -> (ChatPatch, Vec<Effect>) {
    drain_chat(runtime, host, CHAT)
}

fn drain_chat(runtime: &Runtime, host: &Arc<Recorder>, chat: &str) -> (ChatPatch, Vec<Effect>) {
    assert!(runtime.flush(Duration::from_secs(5)), "the worker did not finish");
    // Drain the open's own patch first, so this call returns only what the payload changed.
    let patch = wire::decode(&runtime.drain(chat)).unwrap();
    let effects = std::mem::take(&mut *host.effects.lock().unwrap());
    (patch, effects)
}

fn payload_kind(decoder: &str) -> &'static str {
    match decoder {
        "items" => "latest",
        "items_after" => "newer",
        "items_range" => "range",
        _ => "frame",
    }
}

fn payload(kind: &str, body: String) -> Payload {
    match kind {
        "latest" => Payload::Latest { body },
        "newer" => Payload::Newer { body },
        "reloaded" => Payload::Reloaded { body },
        "older" => Payload::Older { body },
        "range" => Payload::Range { fold_id: None, body },
        "frame" => Payload::Frame { body },
        other => panic!("unknown payload kind {other}"),
    }
}

fn fetch(effect: &Effect) -> &persona_core::continuity::Request {
    match effect {
        Effect::Fetch { request, .. } => request,
        Effect::Rejected { reason } => panic!("rejected: {reason}"),
    }
}

#[test]
fn wire_payloads_decode_once_and_keep_the_item_bytes() {
    let all = cases();
    let names: Vec<&str> = all.iter().map(|case| case.name.as_str()).collect();
    let expected_names: Vec<&str> = vec![
        "items_all_kinds",
        "items_folded_work",
        "items_folded_tasks",
        "items_inline_image",
        "items_task_notifications",
        "items_reply_models",
        "items_http_after",
        "items_fold_range",
        "items_recorded_turn_duration",
        "items_live_lazy",
        "items_empty_advance",
        "items_backlog_latest",
        "items_backlog_catchup",
        "items_backlog_capped",
        "items_live_recorded_turn_duration",
        "items_legacy_null",
        "item_unknown_kind",
    ];
    assert_eq!(names, expected_names, "the in-scope case set");
    let mut checked_items = 0;
    for case in &all {
        assert_eq!(
            case.expected["accepted"],
            json!(true),
            "{}: the corpus holds only accepted payloads",
            case.name
        );
        let host = Arc::new(Recorder::default());
        let chat = chat_for(case);
        let runtime = open_chat(&host, &chat);
        assert!(
            runtime.dispatch(&wire::encode(&Action::WirePayload {
                chat: chat.clone(),
                payload: payload(payload_kind(&case.decoder), case.input_text.clone()),
            })),
            "{}: dispatch",
            case.name
        );
        let (patch, effects) = drain_chat(&runtime, &host, &chat);
        let items = items_of(&case.input);
        // Every item the fixture carries arrives, in order, with its JSON exactly as the hub wrote it. A re-encoded
        // or re-ordered item fails here even when its decoded values would be equal.
        let inserted: Vec<&ListOp> = patch.ops.iter().collect();
        if case.decoder == "items_range" {
            // A range page without an opened fold is ignored, as iOS's caller never sends one.
            assert!(inserted.is_empty(), "{}: a range page without an open fold", case.name);
        } else {
            assert_eq!(inserted.len(), items.len(), "{}: every item arrives", case.name);
            let raws = raw_items(&case.input_text);
            assert_eq!(raws.len(), items.len(), "{}: the scanner finds every item", case.name);
            for (op, (index, item)) in inserted.iter().zip(items.iter().enumerate()) {
                let (json, id) = match op {
                    ListOp::Insert { json, id, .. } | ListOp::Update { json, id, .. } => (json, id),
                    ListOp::Remove { .. } => panic!("{}: a removal for a fresh chat", case.name),
                };
                assert_eq!(id, item["id"].as_str().unwrap(), "{}: the item's id", case.name);
                let want = raws[index].clone();
                assert_eq!(json, &want, "{}: the item's bytes are kept", case.name);
                checked_items += 1;
            }
        }
        // The page-level fields the fixture asserts, from the decoded patch.
        for (pointer, want) in case.expected["fields"].as_object().unwrap() {
            let got = match pointer.as_str() {
                "/covered" => Some(json!(patch.covered)),
                "/history" => Some(patch.history.clone().map_or(Value::Null, Value::from)),
                "/has_more" => Some(patch.has_more.map_or(Value::Null, Value::from)),
                "/loaded_once" => Some(json!(patch.loaded_once)),
                _ => None,
            };
            if let Some(got) = got {
                assert_eq!(&got, want, "{}: {pointer}", case.name);
            }
        }
        for effect in &effects {
            fetch(effect);
        }
    }
    assert!(checked_items > 30, "the corpus covers many items: {checked_items}");
    println!(
        "wire payloads: {} cases, {checked_items} items checked byte for byte",
        all.len()
    );
}

/// The raw text of one item inside an `input` document, by its id, for the byte comparison.
/// Every item's raw text inside an `input` document, in the order they appear, found by scanning the `items`
/// array rather than by id: a repeated id must not compare one item against another's source text.
fn raw_items(input_text: &str) -> Vec<String> {
    let items_at = input_text.find("\"items\"").expect("the items key");
    let Some(array_start) = input_text[items_at..].find('[').map(|at| at + items_at) else {
        return Vec::new(); // `items: null`, as in items_legacy_null.
    };
    let array = &input_text[array_start..];
    let mut result = Vec::new();
    let mut index = 1; // past the opening bracket
    while index < array.len() {
        let rest = &array[index..];
        let Some(item_start) = rest.find(|c: char| !c.is_whitespace()) else {
            break;
        };
        let start = index + item_start;
        if array.as_bytes()[start] == b']' {
            break;
        }
        let end = scan_value_end(&array[start..]);
        result.push(array[start..start + end].to_owned());
        index = start + end;
        // Past the comma, if there is one.
        let rest = &array[index..];
        let Some(comma) = rest.find(|c: char| !c.is_whitespace()) else {
            break;
        };
        if array.as_bytes()[index + comma] == b',' {
            index += comma + 1;
        } else {
            break;
        }
    }
    result
}

#[test]
fn a_byte_payload_and_a_hand_built_event_agree() {
    for case in cases().into_iter().filter(|case| case.decoder != "items_range") {
        let kind = payload_kind(&case.decoder);
        let host_bytes = Arc::new(Recorder::default());
        let chat = chat_for(&case);
        let runtime_bytes = open_chat(&host_bytes, &chat);
        assert!(runtime_bytes.dispatch(&wire::encode(&Action::WirePayload {
            chat: chat.clone(),
            payload: payload(kind, case.input_text.clone()),
        })));
        let (patch_bytes, effects_bytes) = drain_chat(&runtime_bytes, &host_bytes, &chat);

        let host_event = Arc::new(Recorder::default());
        let runtime_event = open_chat(&host_event, &chat);
        let items_json = serde_json::to_string(&items_of(&case.input)).unwrap();
        let batch = WireBatch {
            items_json,
            history: case.input["history"].as_str().map(str::to_owned),
            from_seq: case.input["from_seq"].as_i64(),
            to_seq: case.input["to_seq"].as_i64(),
            backlog: case.input["backlog"].as_bool().unwrap_or(false),
            has_more: case.input["has_more"].as_bool(),
            more_after: case.input["more_after"].as_bool().unwrap_or(false),
        };
        let event = match kind {
            "latest" => ChatEvent::Latest { batch },
            "newer" => ChatEvent::After { batch },
            "frame" => ChatEvent::Pushed { batch },
            other => panic!("{other}"),
        };
        assert!(runtime_event.dispatch(&wire::encode(&Action::Chat {
            chat: chat.clone(),
            event
        })));
        let (patch_event, effects_event) = drain_chat(&runtime_event, &host_event, &chat);
        // The patch versions match because both runtimes saw the same number of changes.
        assert_eq!(patch_bytes.version, patch_event.version, "{}: version", case.name);
        assert_eq!(patch_bytes.covered, patch_event.covered, "{}: covered", case.name);
        assert_eq!(patch_bytes.history, patch_event.history, "{}: history", case.name);
        assert_eq!(patch_bytes.has_more, patch_event.has_more, "{}: has_more", case.name);
        assert_eq!(
            patch_bytes.loaded_once, patch_event.loaded_once,
            "{}: loaded_once",
            case.name
        );
        assert_eq!(patch_bytes.ops.len(), patch_event.ops.len(), "{}: op count", case.name);
        for (a, b) in patch_bytes.ops.iter().zip(&patch_event.ops) {
            let (ai, bi) = match (a, b) {
                (ListOp::Insert { id: a, .. }, ListOp::Insert { id: b, .. })
                | (ListOp::Update { id: a, .. }, ListOp::Update { id: b, .. }) => (a, b),
                (ListOp::Remove { index: a }, ListOp::Remove { index: b }) => {
                    assert_eq!(a, b, "{}: remove index", case.name);
                    continue;
                }
                _ => panic!("{}: different op kinds", case.name),
            };
            assert_eq!(ai, bi, "{}: op id", case.name);
        }
        let requests = |effects: &[Effect]| {
            effects
                .iter()
                .map(|effect| format!("{:?}", fetch(effect)))
                .collect::<Vec<_>>()
        };
        assert_eq!(
            requests(&effects_bytes),
            requests(&effects_event),
            "{}: requests",
            case.name
        );
    }
}

#[test]
fn a_frame_that_is_not_items_is_ignored_and_a_bad_frame_is_dropped() {
    for (body, label) in [
        (r#"{"type":"hello","features":["a"]}"#, "another event type"),
        (
            r#"{"type":"items","machine_id":"m1","session_id":"s1","items":[{"id":"a#0","seq":"x"}]}"#,
            "an item iOS refuses",
        ),
        (
            r#"{"type":"items","machine_id":"m1","session_id":"s1","has_more":"yes"}"#,
            "a wrong-typed key",
        ),
        (r#"not json"#, "not JSON"),
    ] {
        let host = Arc::new(Recorder::default());
        let runtime = open(&host);
        assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
            chat: CHAT.into(),
            payload: Payload::Frame { body: body.into() },
        })));
        let (patch, effects) = drain(&runtime, &host);
        assert!(patch.ops.is_empty(), "{label}: nothing changes");
        assert!(effects.is_empty(), "{label}: a dropped frame is not an error");
    }
}

#[test]
fn a_refused_page_reports_keys_not_content() {
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let secret = "SYNTHETIC-MESSAGE-TEXT";
    let body = format!(r#"{{"items":[],"has_more":false,"to_seq":"{secret}"}}"#);
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body },
    })));
    let (_, effects) = drain(&runtime, &host);
    assert_eq!(effects.len(), 1);
    match &effects[0] {
        Effect::Rejected { reason } => {
            assert!(reason.starts_with("refused payload: wrong_type"), "{reason}");
            assert!(reason.contains("to_seq"), "{reason}");
            assert!(
                !reason.contains(secret),
                "the refusal never repeats the payload: {reason}"
            );
        }
        Effect::Fetch { .. } => panic!("a refused page asks for nothing"),
    }
}

#[test]
fn dispatch_only_queues_the_bytes() {
    // A large payload's dispatch cost must not grow with its size: decoding happens on the worker. The body is a
    // real page with many items, and the comparison is against dispatching a tiny action on the same runtime.
    let item = |index: usize| {
        format!(
            r#"{{"id":"i#{index}","seq":{index},"role":"assistant","kind":"text","text":"{}"}}"#,
            "x".repeat(400)
        )
    };
    let body = format!(
        r#"{{"items":[{}],"has_more":true,"to_seq":4000}}"#,
        (0..500).map(item).collect::<Vec<_>>().join(",")
    );
    let action = wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body },
    });
    let small = wire::encode(&Action::ContinueFill { chat: CHAT.into() });
    assert!(action.len() > 200_000, "the payload is large: {}", action.len());
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let measure = |bytes: &[u8]| -> u128 {
        // Warm up, then take the best of a few runs so a scheduler blip does not decide the result.
        let mut best = u128::MAX;
        for _ in 0..8 {
            let start = std::time::Instant::now();
            assert!(runtime.dispatch(bytes));
            best = best.min(start.elapsed().as_nanos());
            assert!(runtime.flush(Duration::from_secs(10)));
        }
        best
    };
    let large = measure(&action);
    let small = measure(&small);
    println!("dispatch: large payload {large} ns, small action {small} ns (host, best of 8)");
    // A copy of the bytes is all dispatch may do; decoding 500 items would cost orders of magnitude more.
    assert!(
        large < small.saturating_mul(50) + 200_000,
        "dispatch must not decode: {large} vs {small} ns"
    );
}

#[test]
fn a_frame_is_routed_by_its_full_chat_path() {
    // iOS's `SessionKey.path` is `machine_id/session_id`: the whole string routes the frame, and the ids alone do
    // not match a different session on the same machine.
    let host = Arc::new(Recorder::default());
    let runtime = open_chat(&host, "m1/s1");
    let body = r#"{"type":"items","machine_id":"m1","session_id":"s1","items":[{"id":"a#0","seq":1,"role":"user","kind":"text"}],"to_seq":1}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: "m1/s1".into(),
        payload: Payload::Frame { body: body.into() },
    })));
    let (patch, effects) = drain(&runtime, &host);
    assert_eq!(patch.ops.len(), 1, "the frame reaches its chat");
    assert!(effects.is_empty(), "a matching chat is not rejected");
}

#[test]
fn a_frame_is_routed_by_a_hub_group_path() {
    // iOS prefixes a non-default hub group as `group|machine_id/session_id`; that form must route too, and a
    // different group's frame must not land in this chat.
    let host = Arc::new(Recorder::default());
    let runtime = open_chat(&host, "work|m1/s1");
    let body = r#"{"type":"items","machine_id":"m1","session_id":"s1","items":[{"id":"a#0","seq":1,"role":"user","kind":"text"}],"to_seq":1}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: "work|m1/s1".into(),
        payload: Payload::Frame { body: body.into() },
    })));
    let (patch, effects) = drain_chat(&runtime, &host, "work|m1/s1");
    assert_eq!(patch.ops.len(), 1, "the grouped chat receives its frame");
    assert!(effects.is_empty(), "a matching grouped path is not rejected");

    // A frame carries no group, so any group prefix routes to that chat; what must still be refused is a frame
    // for a different machine or session.
    let host = Arc::new(Recorder::default());
    let runtime = open_chat(&host, "work|other/s1");
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: "work|other/s1".into(),
        payload: Payload::Frame { body: body.into() },
    })));
    let (_, effects) = drain_chat(&runtime, &host, "work|other/s1");
    assert!(matches!(&effects[..], [Effect::Rejected { reason }] if reason.contains("another chat")));
}

#[test]
fn a_backlog_marks_the_chat_verified_and_a_gap_clears_it() {
    // Android's `verified` rule, owned by the core now: a complete backlog verifies, a batch with a gap clears it,
    // and a catch-up only preserves an already-verified chat.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let latest = r#"{"items":[{"id":"a#0","seq":1,"role":"user","kind":"text"}],"has_more":false,"to_seq":1}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: latest.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    assert!(patch.verified, "a complete latest page verifies the chat");
    let pushed =
        r#"{"type":"items","machine_id":"m1","session_id":"s1","items":[],"from_seq":5,"to_seq":6,"backlog":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Frame { body: pushed.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    assert!(!patch.verified, "a batch with a gap clears verification");
}

#[test]
fn a_successful_catch_up_restores_verified() {
    // Android's `catchUp`: a gap clears verification, and the complete after page that closes it restores it,
    // provided no live push overtook the covered point.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let latest = r#"{"items":[{"id":"a#0","seq":1,"role":"user","kind":"text"}],"has_more":false,"to_seq":1}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: latest.into() },
    })));
    drain(&runtime, &host);
    // A push with a gap clears verification.
    let pushed =
        r#"{"type":"items","machine_id":"m1","session_id":"s1","items":[],"from_seq":4,"to_seq":5,"backlog":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Frame { body: pushed.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    assert!(!patch.verified, "a gap clears verification");
    // The catch-up page that closes the gap restores it.
    let after = r#"{"items":[{"id":"b#0","seq":2,"role":"user","kind":"text"},{"id":"c#0","seq":3,"role":"user","kind":"text"}],"from_seq":1,"to_seq":5}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Newer { body: after.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    assert!(patch.verified, "the completed catch-up restores verification");
}

#[test]
fn a_hub_changed_input_starts_a_fill() {
    // iOS's `settle(.fill)`: a hub signalled change fetches after the covered point, without an extra re-read.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let latest = r#"{"items":[{"id":"a#0","seq":4,"role":"user","kind":"text"}],"has_more":false,"to_seq":4}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: latest.into() },
    })));
    drain(&runtime, &host);
    assert!(runtime.dispatch(&wire::encode(&Action::HubChanged { chat: CHAT.into() })));
    let (_, effects) = drain(&runtime, &host);
    assert!(
        matches!(
            &effects[..],
            [Effect::Fetch {
                request: persona_core::continuity::Request::FetchAfter { after_seq: 4 },
                ..
            }]
        ),
        "the hub-changed input asks for what comes after: {effects:?}"
    );
}

#[test]
fn an_ack_is_echoed_even_when_nothing_changed() {
    // A no-op input must still be acknowledged, or an asynchronous host cannot tell it was applied.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let (patch, _) = drain(&runtime, &host);
    assert_eq!(patch.acked, None);
    assert!(runtime.dispatch(&wire::encode(&Action::Ack {
        chat: CHAT.into(),
        seq: 7
    })));
    let (patch, _) = drain(&runtime, &host);
    assert_eq!(patch.acked, Some(7), "the acknowledgement reaches the patch");
    assert!(runtime.dispatch(&wire::encode(&Action::Ack {
        chat: CHAT.into(),
        seq: 3
    })));
    let (patch, _) = drain(&runtime, &host);
    assert_eq!(patch.acked, Some(7), "an older ack never moves the watermark back");
}

#[test]
fn two_folds_open_independently_and_complete_in_either_order() {
    // iOS opens different folds independently; the second opening must not cancel the first, and each completes
    // against its own id.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let fold = |id: &str, seq: u32, through: u32| {
        format!(
            r#"{{"id":"{id}","seq":{seq},"role":"assistant","kind":"fold","fold":{{"items":1,"tools":0,"thoughts":0,"through_seq":{through},"through_id":"x#0"}}}}"#
        )
    };
    let first = format!(
        r#"{{"items":[{},{}],"has_more":false,"to_seq":6}}"#,
        fold("fold:a#0", 2, 4),
        fold("fold:b#0", 5, 6)
    );
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: first },
    })));
    drain(&runtime, &host);
    assert!(runtime.dispatch(&wire::encode(&Action::Chat {
        chat: CHAT.into(),
        event: ChatEvent::OpenFold {
            fold_id: "fold:a#0".into()
        },
    })));
    assert!(runtime.dispatch(&wire::encode(&Action::Chat {
        chat: CHAT.into(),
        event: ChatEvent::OpenFold {
            fold_id: "fold:b#0".into()
        },
    })));
    let (_, effects) = drain(&runtime, &host);
    assert_eq!(effects.len(), 2, "both folds are fetched independently: {effects:?}");
    // Answer the second fold first; the first must still complete afterwards.
    let page_b = r#"{"items":[{"id":"b1#0","seq":6,"role":"assistant","kind":"text","text":"work b"}],"more":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Range {
            fold_id: Some("fold:b#0".into()),
            body: page_b.into()
        },
    })));
    let page_a = r#"{"items":[{"id":"a1#0","seq":3,"role":"assistant","kind":"text","text":"work a"}],"more":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Range {
            fold_id: Some("fold:a#0".into()),
            body: page_a.into()
        },
    })));
    let (patch, _) = drain(&runtime, &host);
    let jsons: Vec<&str> = patch
        .ops
        .iter()
        .filter_map(|op| match op {
            ListOp::Insert { json, .. } | ListOp::Update { json, .. } => Some(json.as_str()),
            ListOp::Remove { .. } => None,
        })
        .collect();
    assert!(
        jsons.iter().any(|json| json.contains("work a")),
        "fold a completes: {jsons:?}"
    );
    assert!(
        jsons.iter().any(|json| json.contains("work b")),
        "fold b completes: {jsons:?}"
    );
}

#[test]
fn a_frame_for_another_chat_is_rejected() {
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let body = r#"{"type":"items","machine_id":"other","session_id":"other","items":[]}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Frame { body: body.into() },
    })));
    let (_, effects) = drain(&runtime, &host);
    assert!(matches!(&effects[..], [Effect::Rejected { reason }] if reason.contains("another chat")));
}

#[test]
fn a_range_page_reaches_an_open_fold() {
    // A range page without an opened fold is ignored, but the real path must be covered: open a fold first, then
    // answer it with the wire corpus's range page, and require the opened work and its continuation request.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let fold = r#"{"id":"fold:entry_thinking#0","seq":2,"role":"assistant","kind":"fold","fold":{"items":3,"tools":1,"thoughts":1,"through_seq":4,"through_id":"entry_result#0"}}"#;
    let first = format!(r#"{{"items":[{fold}],"has_more":false,"to_seq":2}}"#);
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: first },
    })));
    drain(&runtime, &host);
    assert!(runtime.dispatch(&wire::encode(&Action::Chat {
        chat: CHAT.into(),
        event: ChatEvent::OpenFold {
            fold_id: "fold:entry_thinking#0".into()
        },
    })));
    let (_, effects) = drain(&runtime, &host);
    assert!(
        matches!(
            &effects[..],
            [Effect::Fetch {
                request: persona_core::continuity::Request::FetchRange {
                    from_seq: 2,
                    through_seq: 4,
                    ..
                },
                ..
            }]
        ),
        "opening the fold asks for its range: {effects:?}"
    );
    let page = r#"{"items":[{"id":"entry_text#0","seq":3,"role":"user","kind":"text","text":"inside the fold"}],"more":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Range {
            fold_id: None,
            body: page.into()
        },
    })));
    let (patch, effects) = drain(&runtime, &host);
    assert!(!patch.ops.is_empty(), "the opened fold publishes its items: {patch:?}");
    assert!(patch.ops.iter().any(|op| matches!(op, ListOp::Insert { json, .. } | ListOp::Update { json, .. } if json.contains("inside the fold"))),
        "the range page's item arrives: {patch:?}");
    assert!(
        effects.is_empty(),
        "a complete range asks for nothing more: {effects:?}"
    );
}

#[test]
fn an_older_page_loads_history() {
    // `Payload::Older` must reach `Session::older`, not `received`: it extends the list backwards and answers
    // whether older history remains.
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let latest = r#"{"items":[{"id":"b#0","seq":5,"role":"user","kind":"text"}],"has_more":true,"to_seq":5}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Latest { body: latest.into() },
    })));
    drain(&runtime, &host);
    let older = r#"{"items":[{"id":"a#0","seq":2,"role":"user","kind":"text","text":"earlier"}],"has_more":false}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Older { body: older.into() },
    })));
    let (patch, effects) = drain(&runtime, &host);
    assert_eq!(patch.has_more, Some(false), "the older page answers has_more");
    assert!(
        patch
            .ops
            .iter()
            .any(|op| matches!(op, ListOp::Insert { json, .. } if json.contains("earlier"))),
        "the older item arrives: {patch:?}"
    );
    assert!(effects.is_empty(), "an older page asks for nothing: {effects:?}");
}

#[test]
fn a_reloaded_page_replaces_the_transcript() {
    let host = Arc::new(Recorder::default());
    let runtime = open(&host);
    let first =
        r#"{"items":[{"id":"a#0","seq":1,"role":"assistant","kind":"text","text":"old"}],"has_more":false,"to_seq":1}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Reloaded { body: first.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    assert_eq!(patch.ops.len(), 1);
    let second =
        r#"{"items":[{"id":"b#0","seq":2,"role":"assistant","kind":"text","text":"new"}],"has_more":false,"to_seq":2}"#;
    assert!(runtime.dispatch(&wire::encode(&Action::WirePayload {
        chat: CHAT.into(),
        payload: Payload::Reloaded { body: second.into() },
    })));
    let (patch, _) = drain(&runtime, &host);
    let list: Vec<String> = patch
        .ops
        .iter()
        .filter_map(|op| match op {
            ListOp::Insert { json, .. } | ListOp::Update { json, .. } => Some(json.clone()),
            ListOp::Remove { .. } => None,
        })
        .collect();
    assert!(
        list.iter().all(|json| json.contains("b#0")),
        "the replacement holds only the new item: {list:?}"
    );
}

#[test]
fn the_raw_scanner_keeps_duplicate_ids_apart() {
    let input =
        r#"{"items":[{"id":"a#0","seq":1,"text":"first"},{"id":"a#0","seq":2,"text":"second"}],"has_more":false}"#;
    let raws = raw_items(input);
    assert_eq!(raws.len(), 2);
    assert!(raws[0].contains("first"), "the first item's own text: {}", raws[0]);
    assert!(raws[1].contains("second"), "the second item's own text: {}", raws[1]);
    assert_eq!(raw_items(r#"{"items":null}"#).len(), 0);
}

#[test]
fn the_raw_text_scanner_covers_the_whole_input() {
    for case in cases() {
        let reparsed: Value = serde_json::from_str(&case.input_text).expect("the raw text is JSON");
        assert_eq!(reparsed, case.input, "{}: the raw text is the case's input", case.name);
        for (raw, item) in raw_items(&case.input_text).into_iter().zip(items_of(&case.input)) {
            let reparsed: Value = serde_json::from_str(&raw).expect("the item's raw text is JSON");
            assert_eq!(reparsed, item, "{}: the item's raw text", case.name);
        }
    }
}
