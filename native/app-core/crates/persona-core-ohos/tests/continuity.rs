use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::mpsc::{Receiver, Sender, channel};

use persona_core_ohos::registry;

use persona_core_runtime::Host;
use persona_core_runtime::wire::{
    self, Action, ChatEvent, ChatPatch, ChatSnapshot, Effect, ListOp, Request, WireBatch,
};
use serde_json::{Map, Value, json};

struct Recorder {
    effects: Sender<Vec<u8>>,
    changes: Sender<u64>,
}

impl Host for Recorder {
    fn changed(&self, version: u64) {
        let _ = self.changes.send(version);
    }
    fn run(&self, effect: &[u8]) {
        let _ = self.effects.send(effect.to_vec());
    }
}

fn items_json(object: &Map<String, Value>) -> String {
    object.get("items").map_or_else(|| "[]".to_owned(), Value::to_string)
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
fn batch(event: &Map<String, Value>) -> WireBatch {
    WireBatch {
        items_json: items_json(event),
        history: text(event, "history"),
        from_seq: number(event, "from_seq"),
        to_seq: number(event, "to_seq"),
        backlog: flag(event, "backlog"),
        has_more: boolean(event, "has_more"),
        more_after: flag(event, "more_after"),
    }
}

fn request_json(request: &Request) -> Value {
    match request {
        Request::FetchAfter { after_seq } => json!({"kind": "fetch_after", "after_seq": after_seq}),
        Request::FetchLatest { fold_work } => json!({"kind": "fetch_latest", "fold_work": fold_work}),
        Request::Subscribe { since_seq, fold_work } => {
            let mut request = json!({"kind": "subscribe", "fold_work": fold_work});
            if let Some(since) = since_seq {
                request["since_seq"] = json!(since);
            }
            request
        }
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

const CHAT: &str = "fixture/continuity";

#[test]
fn continuity_fixtures_through_napi_runtime_registry() {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../spec/fixtures/continuity");
    let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .filter(|path| path.extension().is_some_and(|extension| extension == "json"))
        .collect();
    files.sort();
    let mut steps = 0;
    let mut cases = 0;
    for file in files {
        let document: Value = serde_json::from_str(&std::fs::read_to_string(&file).unwrap()).unwrap();
        for case in document["cases"].as_array().unwrap() {
            cases += 1;
            let name = case["name"].as_str().unwrap();
            let (effects, effect_receiver): (Sender<Vec<u8>>, Receiver<Vec<u8>>) = channel();
            let (changes, _change_receiver) = channel();
            let host = Arc::new(Recorder { effects, changes });
            let handle = registry::create(&registry::config(), host).unwrap();
            let initial = case["input"]["initial"].as_object().unwrap();
            let snapshot = ChatSnapshot {
                items_json: items_json(initial),
                history: text(initial, "history"),
                covered: number(initial, "covered").unwrap(),
                has_more: boolean(initial, "has_more"),
                loaded_once: flag(initial, "loaded_once"),
            };
            let action = Action::OpenChat {
                chat: CHAT.into(),
                snapshot,
            };
            assert!(registry::dispatch(handle, &wire::encode(&action)).unwrap());
            let mut list: Vec<String> = Vec::new();
            let drain = |handle: i64, list: &mut Vec<String>| -> ChatPatch {
                assert!(
                    registry::flush(handle, 5000).unwrap(),
                    "{name}: the worker did not finish"
                );
                let patch: ChatPatch = wire::decode(&registry::drain(handle, CHAT).unwrap()).unwrap();
                for op in &patch.ops {
                    match op {
                        ListOp::Remove { index } => {
                            list.remove(*index as usize);
                        }
                        ListOp::Insert { index, json, .. } => list.insert(*index as usize, json.clone()),
                        ListOp::Update { index, json, .. } => list[*index as usize] = json.clone(),
                    }
                }
                patch
            };
            drain(handle, &mut list);
            while effect_receiver.try_recv().is_ok() {}
            for (index, (event, want)) in case["input"]["events"]
                .as_array()
                .unwrap()
                .iter()
                .zip(case["expected"].as_array().unwrap())
                .enumerate()
            {
                let label = format!("{name} step {index}");
                let event = event.as_object().unwrap();
                let chat_event = match text(event, "kind").as_deref().unwrap() {
                    "http_latest" => Some(ChatEvent::Latest { batch: batch(event) }),
                    "ws_items" => Some(ChatEvent::Pushed { batch: batch(event) }),
                    "http_after" => Some(ChatEvent::After { batch: batch(event) }),
                    "http_reload" => Some(ChatEvent::Reloaded {
                        items_json: items_json(event),
                        history: text(event, "history"),
                        to_seq: number(event, "to_seq"),
                        has_more: boolean(event, "has_more").unwrap(),
                    }),
                    "http_older" => Some(ChatEvent::Older {
                        items_json: items_json(event),
                        has_more: boolean(event, "has_more").unwrap(),
                    }),
                    "http_range" => Some(ChatEvent::Range {
                        fold_id: None,
                        items_json: items_json(event),
                        more: flag(event, "more"),
                    }),
                    "reconnect" => Some(ChatEvent::Reconnected),
                    "load_older" => Some(ChatEvent::LoadOlder),
                    "open_fold" => Some(ChatEvent::OpenFold {
                        fold_id: text(event, "fold_id").unwrap(),
                    }),
                    "continue_fill" => None,
                    other => panic!("{label}: unknown event {other}"),
                };
                let action = match chat_event {
                    Some(event) => Action::Chat {
                        chat: CHAT.into(),
                        event,
                    },
                    None => Action::ContinueFill { chat: CHAT.into() },
                };
                assert!(registry::dispatch(handle, &wire::encode(&action)).unwrap());
                let patch = drain(handle, &mut list);
                let mut requests = Vec::new();
                while let Ok(bytes) = effect_receiver.try_recv() {
                    match wire::decode(&bytes).unwrap() {
                        Effect::Fetch { chat, request } => {
                            assert_eq!(chat, CHAT);
                            requests.push(request_json(&request));
                        }
                        Effect::Rejected { reason } => panic!("{label}: rejected: {reason}"),
                    }
                }
                let got: Vec<Value> = list.iter().map(|json| serde_json::from_str(json).unwrap()).collect();
                assert_eq!(Value::Array(got), want["items"], "{label} items");
                assert_eq!(json!(patch.covered), want["covered"], "{label} covered");
                assert_eq!(json!(patch.history), want["history"], "{label} history");
                assert_eq!(json!(patch.has_more), want["has_more"], "{label} has_more");
                assert_eq!(json!(patch.loaded_once), want["loaded_once"], "{label} loaded_once");
                assert_eq!(Value::Array(requests), want["actions"], "{label} actions");
                steps += 1;
            }
            registry::close(handle).unwrap();
        }
    }
    assert_eq!((cases, steps), (38, 94), "every continuity case and step replays");
}
