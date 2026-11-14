//! Replays the shared wire fixtures (`spec/fixtures/wire`) through `transport`, and checks that a continuity step
//! built from payload bytes is the step built from a hand-made batch (`spec/fixtures/continuity`).
//!
//! What the wire replay does and does not show: every in-scope case is an accepted payload whose expectations were
//! derived from the iOS sources (`derived: true`), not recorded from an iOS run, so the replay never exercises a
//! refusal; those rules are tested in `src/transport/tests.rs`. Of the expected fields, only the ones the decoder
//! produced count as checks of the decoder: an item field the core does not read is compared with the item's kept
//! JSON, which shows the bytes were kept and nothing more.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use persona_core::Item;
use persona_core::continuity::{Batch, Session, Step};
use persona_core::transport::{self, Frame};
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::{Map, Value, json};

fn fixtures(domain: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../spec/fixtures")
        .join(domain)
}

/// One wire case, with its input kept as the bytes in the file.
#[derive(Deserialize)]
struct WireCase {
    name: String,
    decoder: String,
    input: Box<RawValue>,
    expected: Expected,
}

#[derive(Deserialize)]
struct Expected {
    accepted: bool,
    #[serde(default)]
    fields: Map<String, Value>,
}

/// The cases this decoder owns: the three HTTP decoders, and every event whose `type` is `items`. Adding or removing
/// one in the fixtures must change this list.
const IN_SCOPE: [&str; 17] = [
    // items.json
    "items_all_kinds",
    "items_folded_work",
    "items_folded_tasks",
    "items_inline_image",
    "items_task_notifications",
    "items_reply_models",
    "items_http_after",
    "items_fold_range",
    "items_recorded_turn_duration",
    // item_events.json
    "items_live_lazy",
    "items_empty_advance",
    "items_backlog_latest",
    "items_backlog_catchup",
    "items_backlog_capped",
    "items_live_recorded_turn_duration",
    // compatibility.json
    "items_legacy_null",
    "item_unknown_kind",
];

/// Events of other types in the same files. The frame decoder must call each "not an items frame", whatever iOS makes
/// of it; whether iOS accepts them is for the decoders of later phases.
const OTHER_FRAMES: [&str; 8] = [
    "unknown_event_ignored",
    "hello_unknown_field",
    "hello_missing_features",
    "fleet_bad_optional_stats",
    "fleet_unknown_values",
    "hello_wrong_known_type",
    "session_bad_optional_field",
    "interaction_unknown_kind",
];

/// Expectations that describe iOS's typed model rather than the payload, as `(case, pointer, iOS value, core value)`.
/// The core keeps each item's JSON and its own `kind` string, so it cannot produce the iOS value; each entry is
/// checked against the core value given here instead of being skipped.
///
/// - `ItemKind.init(from:)` (`apple/Shared/Models.swift`) reads a kind it does not know as `unknown`. The core keeps
///   the hub's string, which no rule of the core tells apart from `unknown`.
const CLIENT_TYPED: [(&str, &str, &str, &str); 1] = [("item_unknown_kind", "/items/0/kind", "unknown", "future_kind")];

/// The item fields the decoder reads; `project_items` takes exactly these from the decoded item.
const ITEM_FIELDS_READ: [&str; 7] = [
    "id",
    "seq",
    "kind",
    "role",
    "tool_use_id",
    "fold/through_seq",
    "fold/through_id",
];

/// Where an expected field's value comes from in the projection.
#[derive(Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
enum Origin {
    /// A value the decoder produced.
    Decoded,
    /// An item field the core does not read: the item's kept JSON, already compared byte for byte with the input.
    KeptJson,
    /// `/type`: written by this test once the decoder returned `Frame::Items`.
    FrameType,
}

fn origin(pointer: &str) -> Origin {
    if pointer == "/type" {
        return Origin::FrameType;
    }
    // `/items/<n>/<field>`; `/items` and `/items/<n>` themselves are the decoder's.
    let field = pointer
        .strip_prefix("/items/")
        .and_then(|rest| rest.split_once('/'))
        .map(|(_, field)| field);
    match field {
        Some(field) if !ITEM_FIELDS_READ.contains(&field) => Origin::KeptJson,
        _ => Origin::Decoded,
    }
}

/// The decoded items as the wire object iOS's model would re-encode to: the preserved JSON, with the fields the core
/// reads ([`ITEM_FIELDS_READ`]) replaced by what the core read.
fn project_items(items: &[Item], input_items: &[&RawValue], label: &str) -> Value {
    assert_eq!(items.len(), input_items.len(), "{label}: item count");
    let projected = items
        .iter()
        .zip(input_items)
        .map(|(item, raw)| {
            assert_eq!(item.json(), raw.get(), "{label}: item JSON is kept byte for byte");
            let mut value: Value = serde_json::from_str(item.json()).expect("item JSON");
            let object = value.as_object_mut().expect("item object");
            object.insert("id".into(), json!(item.id));
            object.insert("seq".into(), json!(item.seq));
            object.insert("kind".into(), json!(item.kind));
            object.insert("role".into(), json!(item.role));
            object.insert("tool_use_id".into(), json!(item.tool_use_id));
            if let Some(range) = &item.fold {
                let fold = object["fold"].as_object_mut().expect("fold object");
                fold.insert("through_seq".into(), json!(range.through_seq));
                fold.insert("through_id".into(), json!(range.through_id));
            } else {
                assert!(
                    object.get("fold").is_none_or(Value::is_null),
                    "{label}: a fold in the JSON was not read"
                );
            }
            value
        })
        .collect();
    Value::Array(projected)
}

/// The items of a case's input, read independently of `transport`.
fn input_items(input: &RawValue) -> Vec<&RawValue> {
    #[derive(Deserialize)]
    struct Body<'a> {
        #[serde(borrow, default)]
        items: Option<Vec<&'a RawValue>>,
    }
    serde_json::from_str::<Body<'_>>(input.get())
        .expect("input")
        .items
        .unwrap_or_default()
}

/// Decodes one in-scope case and projects the result, or `None` when the decoder refused it.
fn decode(case: &WireCase) -> Option<Value> {
    let bytes = case.input.get().as_bytes();
    let raws = input_items(&case.input);
    let label = &case.name;
    match case.decoder.as_str() {
        "items" => transport::decode_latest(bytes).ok().map(|page| {
            json!({
                "items": project_items(&page.items, &raws, label),
                "has_more": page.has_more,
                "history": page.history,
                "to_seq": page.to_seq,
            })
        }),
        "items_after" => transport::decode_newer(bytes).ok().map(|page| {
            json!({
                "items": project_items(&page.items, &raws, label),
                "more_after": page.more_after,
                "history": page.history,
                "from_seq": page.from_seq,
                "to_seq": page.to_seq,
            })
        }),
        "items_range" => transport::decode_range(bytes).ok().map(|page| {
            json!({
                "items": project_items(&page.items, &raws, label),
                "more": page.more,
            })
        }),
        "event" | "compat_event" => match transport::decode_frame(bytes) {
            Ok(Frame::Items(frame)) => Some(json!({
                "type": "items",
                "machine_id": frame.machine_id,
                "session_id": frame.session_id,
                "items": project_items(&frame.items, &raws, label),
                "backlog": frame.backlog,
                "has_more": frame.has_more,
                "history": frame.history,
                "from_seq": frame.from_seq,
                "to_seq": frame.to_seq,
                "more_after": frame.more_after,
            })),
            Ok(Frame::Other) => panic!("{label}: an items frame was not recognised"),
            Err(_) => None,
        },
        other => panic!("{label}: decoder {other} is not in scope"),
    }
}

#[test]
fn wire_fixtures() {
    let (mut replayed, mut others) = (BTreeSet::new(), BTreeSet::new());
    let mut fields = std::collections::BTreeMap::<Origin, usize>::new();
    let mut typed = 0;
    for file in ["items.json", "item_events.json", "compatibility.json"] {
        let text = std::fs::read_to_string(fixtures("wire").join(file)).expect(file);
        let cases: Vec<WireCase> = serde_json::from_str(&text).expect(file);
        for case in &cases {
            let is_event = matches!(case.decoder.as_str(), "event" | "compat_event");
            let input: Value = serde_json::from_str(case.input.get()).expect("input");
            if is_event && input["type"] != "items" {
                assert!(
                    matches!(transport::decode_frame(case.input.get().as_bytes()), Ok(Frame::Other)),
                    "{}: a frame of another type is not an items frame and not an error",
                    case.name
                );
                others.insert(case.name.clone());
                continue;
            }
            if !is_event && !matches!(case.decoder.as_str(), "items" | "items_after" | "items_range") {
                continue;
            }
            let decoded = decode(case);
            assert_eq!(decoded.is_some(), case.expected.accepted, "{}: accepted", case.name);
            if let Some(decoded) = decoded {
                for (pointer, want) in &case.expected.fields {
                    // A missing value and null are the same in the fixtures.
                    let got = decoded.pointer(pointer).cloned().unwrap_or(Value::Null);
                    let exception = CLIENT_TYPED
                        .iter()
                        .find(|(name, at, _, _)| *name == case.name && at == pointer);
                    if let Some((_, _, ios, core)) = exception {
                        assert_eq!(want, &json!(ios), "{} {pointer}: the iOS expectation moved", case.name);
                        assert_eq!(got, json!(core), "{} {pointer}: the core's value", case.name);
                        typed += 1;
                    } else {
                        assert_eq!(&got, want, "{} {pointer}", case.name);
                    }
                    *fields.entry(origin(pointer)).or_default() += 1;
                }
            } else {
                assert!(
                    case.expected.fields.is_empty(),
                    "{}: fields on a refused case",
                    case.name
                );
            }
            replayed.insert(case.name.clone());
        }
    }
    let want: BTreeSet<String> = IN_SCOPE.iter().map(|name| (*name).to_owned()).collect();
    assert_eq!(replayed, want, "the in-scope wire cases");
    let want: BTreeSet<String> = OTHER_FRAMES.iter().map(|name| (*name).to_owned()).collect();
    assert_eq!(others, want, "the frames of other types");
    assert_eq!(typed, CLIENT_TYPED.len(), "every client-typed exception is used");
    let count = |origin| fields.get(&origin).copied().unwrap_or(0);
    assert!(count(Origin::Decoded) > 0, "no field checked the decoder");
    eprintln!(
        "wire: {} cases replayed, all accepted payloads; {} fields from decoded values ({typed} client-typed), {} \
         item fields read back from the kept JSON, {} frame types; {} other frames",
        replayed.len(),
        count(Origin::Decoded),
        count(Origin::KeptJson),
        count(Origin::FrameType),
        others.len()
    );
}

fn items(object: &Map<String, Value>) -> Vec<Item> {
    match object.get("items") {
        Some(value) => Item::list_from_json(&value.to_string()).expect("fixture items"),
        None => Vec::new(),
    }
}
fn text(object: &Map<String, Value>, key: &str) -> Option<String> {
    object.get(key).and_then(Value::as_str).map(str::to_owned)
}
fn number(object: &Map<String, Value>, key: &str) -> Option<i64> {
    object.get(key).and_then(Value::as_i64)
}
fn boolean(object: &Map<String, Value>, key: &str) -> Option<bool> {
    object.get(key).and_then(Value::as_bool)
}
fn flag(object: &Map<String, Value>, key: &str) -> bool {
    boolean(object, key) == Some(true)
}

fn batch(event: &Map<String, Value>, backlog: bool) -> Batch {
    Batch {
        items: items(event),
        history: text(event, "history"),
        from_seq: number(event, "from_seq"),
        to_seq: number(event, "to_seq"),
        backlog,
        has_more: boolean(event, "has_more"),
        more_after: flag(event, "more_after"),
    }
}

/// The step as `tests/continuity_fixtures.rs` builds it, from a hand-made batch.
fn by_hand(session: &mut Session, event: &Map<String, Value>) -> Step {
    match text(event, "kind").as_deref() {
        Some("http_latest") => session.received(batch(event, true)),
        Some("ws_items") => session.received(batch(event, flag(event, "backlog"))),
        Some("http_after") => session.after(batch(event, false)),
        Some("http_reload") => session.reload(
            items(event),
            text(event, "history"),
            number(event, "to_seq"),
            boolean(event, "has_more").expect("http_reload has_more"),
        ),
        Some("http_older") => session.older(items(event), boolean(event, "has_more").expect("http_older has_more")),
        Some("http_range") => session.range("", items(event), flag(event, "more")),
        _ => local(session, event),
    }
}

/// Events that carry no payload.
fn local(session: &mut Session, event: &Map<String, Value>) -> Step {
    match text(event, "kind").as_deref() {
        Some("continue_fill") => session.continue_fill(),
        Some("load_older") => session.load_older(),
        Some("reconnect") => session.reconnect(),
        Some("open_fold") => session.open_fold(&text(event, "fold_id").expect("open_fold fold_id")),
        other => panic!("unknown event {other:?}"),
    }
}

/// `http_latest` steps that no HTTP page can carry: they leave `has_more` out, which the corpus uses for a store-level
/// backlog without pagination, while iOS's `ItemsResponse` requires the key. As `(case, step)`. Transport must refuse
/// each one for exactly that reason; the step is then applied from the hand-made batch.
const NOT_A_WIRE_PAGE: [(&str, usize); 3] = [
    ("opened_work_survives_repeated_fold_page", 2),
    ("opened_work_survives_repeated_fold_page", 3),
    ("duplicate_cache_and_batch_keep_newest", 1),
];

/// The same step from the bytes of the payload the hub would send for the event. `None` for events without one.
fn by_bytes(session: &mut Session, event: &Map<String, Value>, case: &str, index: usize) -> Option<Step> {
    let kind = text(event, "kind").expect("kind");
    if NOT_A_WIRE_PAGE.contains(&(case, index)) {
        assert_eq!(kind, "http_latest", "{case} step {index}");
        let mut payload = event.clone();
        payload.remove("kind");
        let refused = transport::decode_latest(&serde_json::to_vec(&payload).expect("payload")).expect_err("refused");
        assert_eq!(refused.to_string(), "missing_key: has_more", "{case} step {index}");
        return Some(by_hand(session, event));
    }
    // The event's own `kind` is the fixture's, not the payload's.
    let mut payload = event.clone();
    payload.remove("kind");
    if kind == "ws_items" {
        payload.insert("type".into(), json!("items"));
        payload.insert("machine_id".into(), json!("m1"));
        payload.insert("session_id".into(), json!("s1"));
    }
    let bytes = serde_json::to_vec(&payload).expect("payload");
    let step = match kind.as_str() {
        "http_latest" => session.received(transport::decode_latest(&bytes).expect("latest").into_batch()),
        "ws_items" => match transport::decode_frame(&bytes).expect("frame") {
            Frame::Items(frame) => {
                assert_eq!((frame.machine_id.as_str(), frame.session_id.as_str()), ("m1", "s1"));
                session.received(frame.into_batch())
            }
            Frame::Other => panic!("an items frame was not recognised"),
        },
        "http_after" => session.after(transport::decode_newer(&bytes).expect("newer").into_batch()),
        "http_reload" => {
            let page = transport::decode_latest(&bytes).expect("reload");
            session.reload(page.items, page.history, page.to_seq, page.has_more)
        }
        "http_older" => {
            let page = transport::decode_latest(&bytes).expect("older");
            session.older(page.items, page.has_more)
        }
        "http_range" => {
            let (items, more) = transport::decode_range(&bytes).expect("range").into_parts();
            session.range("", items, more)
        }
        _ => return None,
    };
    Some(step)
}

fn state(session: &Session) -> Value {
    let reducer = session.reducer();
    json!({
        "items": reducer.items().iter().map(|i| i.json().to_owned()).collect::<Vec<_>>(),
        "covered": reducer.covered(),
        "history": reducer.history(),
        "has_more": reducer.has_more(),
        "loaded_once": reducer.loaded_once(),
        "behind": session.behind(),
    })
}

#[test]
fn continuity_steps_from_bytes_match_hand_built_batches() {
    let mut files: Vec<PathBuf> = std::fs::read_dir(fixtures("continuity"))
        .expect("spec/fixtures/continuity")
        .map(|entry| entry.expect("entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    let mut through_transport = std::collections::BTreeMap::<String, usize>::new();
    let (mut cases, mut steps) = (0, 0);
    for file in files {
        let document: Value = serde_json::from_str(&std::fs::read_to_string(&file).unwrap()).unwrap();
        let file_name = file.file_name().unwrap().to_string_lossy().into_owned();
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let initial = case["input"]["initial"].as_object().expect("initial");
            let mut sessions = [Session::new(), Session::new()];
            for session in &mut sessions {
                session.restore(
                    items(initial),
                    text(initial, "history"),
                    number(initial, "covered").expect("covered"),
                    boolean(initial, "has_more"),
                    flag(initial, "loaded_once"),
                );
            }
            let [hand, bytes] = &mut sessions;
            for (index, event) in case["input"]["events"].as_array().expect("events").iter().enumerate() {
                let label = format!("{file_name}/{name} step {index}");
                let event = event.as_object().expect("event");
                let want = by_hand(hand, event);
                let got = match by_bytes(bytes, event, name, index) {
                    Some(step) => {
                        let kind = if NOT_A_WIRE_PAGE.contains(&(name, index)) {
                            "refused: not a wire page".to_owned()
                        } else {
                            text(event, "kind").unwrap()
                        };
                        *through_transport.entry(kind).or_default() += 1;
                        step
                    }
                    None => local(bytes, event),
                };
                assert_eq!(got, want, "{label}: step");
                assert_eq!(state(bytes), state(hand), "{label}: state");
                steps += 1;
            }
            cases += 1;
        }
    }
    // Every payload event of the corpus went through transport; a new event kind must be placed on purpose.
    let want: std::collections::BTreeMap<String, usize> = [
        ("http_after", 13),
        ("http_latest", 10),
        ("http_older", 1),
        ("http_range", 5),
        ("http_reload", 5),
        ("refused: not a wire page", NOT_A_WIRE_PAGE.len()),
        ("ws_items", 44),
    ]
    .into_iter()
    .map(|(kind, count)| (kind.to_owned(), count))
    .collect();
    assert_eq!(through_transport, want, "payload events decoded from bytes");
    assert_eq!((cases, steps), (38, 94), "the continuity corpus");
    eprintln!("continuity through transport: {cases} cases, {steps} steps, 78 decoded from bytes");
}
