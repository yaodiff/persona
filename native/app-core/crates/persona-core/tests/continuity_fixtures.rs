//! Replays every case of `spec/fixtures/continuity`, comparing the whole state and the requests after every step.

use std::path::{Path, PathBuf};

use persona_core::Item;
use persona_core::continuity::{Batch, Gap, Request, Session, Step};
use serde_json::{Map, Value, json};

fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../spec/fixtures/continuity")
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

fn request_json(request: &Request) -> Value {
    match request {
        Request::FetchAfter { after_seq } => json!({"kind": "fetch_after", "after_seq": after_seq}),
        Request::FetchLatest { fold_work } => json!({"kind": "fetch_latest", "fold_work": fold_work}),
        Request::Subscribe { since_seq, fold_work } => match since_seq {
            Some(since) => json!({"kind": "subscribe", "fold_work": fold_work, "since_seq": since}),
            None => json!({"kind": "subscribe", "fold_work": fold_work}),
        },
        Request::FetchOlder { before_seq, fold_work } => {
            json!({"kind": "fetch_older", "before_seq": before_seq, "fold_work": fold_work})
        }
        Request::FetchRange {
            from_seq, through_seq, ..
        } => {
            json!({"kind": "fetch_range", "from_seq": from_seq, "through_seq": through_seq})
        }
    }
}

fn step(session: &mut Session, event: &Map<String, Value>, label: &str) -> Step {
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
        Some("continue_fill") => session.continue_fill(),
        Some("load_older") => session.load_older(),
        Some("reconnect") => session.reconnect(),
        Some("open_fold") => session.open_fold(&text(event, "fold_id").expect("open_fold fold_id")),
        Some("http_range") => session.range("", items(event), flag(event, "more")),
        other => panic!("{label}: unknown event {other:?}"),
    }
}

#[test]
fn continuity_fixtures() {
    let mut files: Vec<PathBuf> = std::fs::read_dir(fixture_dir())
        .expect("spec/fixtures/continuity")
        .map(|entry| entry.expect("entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    assert!(!files.is_empty(), "continuity fixtures are required");
    let (mut cases, mut steps) = (0, 0);
    for file in files {
        let document: Value = serde_json::from_str(&std::fs::read_to_string(&file).unwrap()).unwrap();
        let file_name = file.file_name().unwrap().to_string_lossy().into_owned();
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let initial = case["input"]["initial"].as_object().expect("initial");
            let mut session = Session::new();
            session.restore(
                items(initial),
                text(initial, "history"),
                number(initial, "covered").expect("covered"),
                boolean(initial, "has_more"),
                flag(initial, "loaded_once"),
            );
            let events = case["input"]["events"].as_array().expect("events");
            let expected = case["expected"].as_array().expect("expected");
            assert_eq!(events.len(), expected.len(), "{file_name}/{name}: step count");
            for (index, (event, want)) in events.iter().zip(expected).enumerate() {
                let label = format!("{file_name}/{name} step {index}");
                let result = step(&mut session, event.as_object().unwrap(), &label);
                let reducer = session.reducer();
                let got_items: Vec<Value> = reducer
                    .items()
                    .iter()
                    .map(|i| serde_json::from_str(i.json()).unwrap())
                    .collect();
                assert_eq!(&Value::Array(got_items), &want["items"], "{label} items");
                let ids: Vec<&str> = reducer.items().iter().map(|i| i.id.as_str()).collect();
                assert_eq!(json!(ids), want["item_ids"], "{label} item_ids");
                assert_eq!(json!(reducer.covered()), want["covered"], "{label} covered");
                assert_eq!(json!(reducer.history()), want["history"], "{label} history");
                assert_eq!(json!(reducer.has_more()), want["has_more"], "{label} has_more");
                assert_eq!(json!(reducer.loaded_once()), want["loaded_once"], "{label} loaded_once");
                let gap = match result.gap.unwrap_or(Gap::None) {
                    Gap::None => "none",
                    Gap::Fill => "fill",
                    Gap::Reload => "reload",
                };
                assert_eq!(json!(gap), want["gap"], "{label} gap");
                let requests: Vec<Value> = result.requests.iter().map(request_json).collect();
                assert_eq!(Value::Array(requests), want["actions"], "{label} actions");
                steps += 1;
            }
            cases += 1;
        }
    }
    eprintln!("continuity: {cases} cases, {steps} steps replayed");
}
