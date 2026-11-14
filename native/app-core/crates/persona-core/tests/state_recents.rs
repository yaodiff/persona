//! Replays the complete state and recents fixture sets. Every expectation is read from the checked-in JSON;
//! exact per-file case sets catch a renamed, duplicated, added or dropped case instead of silently passing a subset.

use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use persona_core::recents::{
    Machine, RecentSetup, open_conversation, record_setup, setup_cards, setup_model, shown_conversations,
};
use persona_core::state::{AgentState, ClientSession, HubSession, session_state, utc_timestamp};
use serde::Deserialize;
use serde_json::{Value, json};

fn fixture(path: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../spec/fixtures")
        .join(path)
}

#[derive(Deserialize)]
struct Document {
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    operation: Option<String>,
    input: Value,
    expected: Value,
}

fn cases(path: &str, expected_names: &[&str]) -> Vec<Case> {
    let document: Document =
        serde_json::from_str(&std::fs::read_to_string(fixture(path)).expect("read fixture")).expect("decode fixture");
    let mut counts = BTreeMap::new();
    for case in &document.cases {
        *counts.entry(case.name.clone()).or_insert(0_usize) += 1;
    }
    let duplicates: BTreeSet<_> = counts
        .into_iter()
        .filter(|(_, count)| *count != 1)
        .map(|(name, _)| name)
        .collect();
    assert!(duplicates.is_empty(), "{path}: duplicate case names: {duplicates:?}");
    let actual: BTreeSet<_> = document.cases.iter().map(|case| case.name.as_str()).collect();
    let expected: BTreeSet<_> = expected_names.iter().copied().collect();
    assert_eq!(actual, expected, "{path}: exact case-name set");
    document.cases
}

fn string_list(value: &Value) -> Vec<String> {
    serde_json::from_value(value.clone()).expect("string list")
}

fn setups(value: &Value) -> Vec<RecentSetup> {
    serde_json::from_value(value.clone()).expect("setup list")
}

#[test]
fn session_state_fixtures() {
    for case in cases(
        "sort/session_state.json",
        &["client_state_precedence", "waiting_requires_quiet_state"],
    ) {
        let sessions: Vec<ClientSession> = serde_json::from_value(case.input["sessions"].clone()).expect("sessions");
        let got: Vec<Value> = sessions
            .iter()
            .map(|session| {
                let row = session_state(session);
                json!({
                    "id": row.id,
                    "state": row.state.as_str(),
                    "is_live": row.is_live,
                    "is_quiet": row.is_quiet,
                    "is_waiting": row.is_waiting,
                    "sort_date": row.sort_date,
                })
            })
            .collect();
        assert_eq!(json!({ "sessions": got }), case.expected, "session_state/{}", case.name);
    }
}

#[test]
fn hub_state_fixtures() {
    let names = [
        "hub_blocked",
        "hub_pending_offline",
        "hub_working_seen",
        "hub_done_unseen",
        "hub_done_seen_before_tolerance",
        "hub_done_seen_at_tolerance",
        "hub_done_seen_equal",
        "hub_done_seen_after",
        "hub_idle",
        "hub_offline",
        "hub_unknown",
    ];
    for case in cases("sort/hub_state.json", &names) {
        let input = &case.input;
        let session = &input["session"];
        let timestamp = |value: &Value| value.as_str().and_then(utc_timestamp);
        let got = AgentState::from_hub(&HubSession {
            status: session["status"].as_str().expect("status"),
            pending_interactions: session["pending_interactions"].as_i64().expect("pending_interactions"),
            updated_at: timestamp(&session["updated_at"]),
            seen_at: timestamp(&input["seen_at"]),
        });
        assert_eq!(
            json!({ "state": got.as_str() }),
            case.expected,
            "hub_state/{}",
            case.name
        );
    }
}

#[test]
fn recent_conversation_fixtures() {
    let names = [
        "open_into_empty",
        "open_goes_first",
        "open_again_moves_to_front",
        "open_the_first_again",
        "keeps_ten",
        "ignores_empty",
        "shown_skips_the_current",
        "shown_skips_the_missing",
        "shown_up_to_the_limit",
        "shown_without_a_current",
        "shown_nothing_else",
    ];
    for case in cases("recents/recent_conversations.json", &names) {
        match case.operation.as_deref() {
            Some("open") => {
                let after = open_conversation(
                    &string_list(&case.input["before"]),
                    case.input["opened"].as_str().unwrap(),
                );
                assert_eq!(
                    json!({ "after": after }),
                    case.expected,
                    "recent_conversations/{}",
                    case.name
                );
            }
            Some("shown") => {
                let existing: BTreeSet<String> = string_list(&case.input["existing"]).into_iter().collect();
                let shown = shown_conversations(
                    &string_list(&case.input["entries"]),
                    case.input["current"].as_str().unwrap(),
                    |key| existing.contains(key),
                    usize::try_from(case.input["limit"].as_u64().unwrap()).unwrap(),
                );
                assert_eq!(
                    json!({ "shown": shown }),
                    case.expected,
                    "recent_conversations/{}",
                    case.name
                );
            }
            operation => panic!("recent_conversations/{}: unknown operation {operation:?}", case.name),
        }
    }
}

#[test]
fn recent_setup_fixtures() {
    let names = [
        "record_into_empty",
        "record_moves_the_same_setup_first",
        "record_keeps_twenty",
        "record_differs_by_model",
        "cards_skip_unknown_machines",
        "cards_at_most_three",
        "cards_offline_shown",
        "model_of_the_latest_same_launcher",
        "model_other_launcher_is_default",
        "model_unknown_machine",
    ];
    for case in cases("recents/recent_setups.json", &names) {
        match case.operation.as_deref() {
            Some("record") => {
                let setup: RecentSetup = serde_json::from_value(case.input["setup"].clone()).expect("setup");
                let after = record_setup(&setups(&case.input["before"]), &setup);
                assert_eq!(json!({ "after": after }), case.expected, "recent_setups/{}", case.name);
            }
            Some("cards") => {
                let machines: Vec<Machine> = serde_json::from_value(case.input["machines"].clone()).expect("machines");
                let cards = setup_cards(&setups(&case.input["entries"]), &machines);
                assert_eq!(json!({ "cards": cards }), case.expected, "recent_setups/{}", case.name);
            }
            Some("model") => {
                let entries = setups(&case.input["entries"]);
                let model = setup_model(
                    &entries,
                    case.input["machine"].as_str().unwrap(),
                    case.input["launcher"].as_str().unwrap(),
                );
                assert_eq!(json!({ "model": model }), case.expected, "recent_setups/{}", case.name);
            }
            operation => panic!("recent_setups/{}: unknown operation {operation:?}", case.name),
        }
    }
}

#[test]
fn state_and_recents_mutations() {
    // Independently specified boundary values: two seconds before completion is read, one nanosecond earlier is not.
    let updated = utc_timestamp("2026-01-01T00:00:20Z").unwrap();
    let at_tolerance = AgentState::from_hub(&HubSession {
        status: "done",
        pending_interactions: 0,
        updated_at: Some(updated),
        seen_at: utc_timestamp("2026-01-01T00:00:18Z"),
    });
    let before_tolerance = AgentState::from_hub(&HubSession {
        status: "done",
        pending_interactions: 0,
        updated_at: Some(updated),
        seen_at: utc_timestamp("2026-01-01T00:00:17.999999999Z"),
    });
    assert_eq!(at_tolerance, AgentState::Idle);
    assert_eq!(before_tolerance, AgentState::Unread);

    // Waiting is quiet-only, and work wakes a session only when the hub will resume it without a user decision.
    let waiting_session = |work: Value| {
        let session: ClientSession = serde_json::from_value(json!({
            "machine_id": "m", "session_id": "s", "state": "idle", "work": work,
        }))
        .unwrap();
        session_state(&session).is_waiting
    };
    assert!(waiting_session(
        json!({"phase": "blocked", "on": "external", "wake_at": "2026-01-01T00:00:20Z"})
    ));
    assert!(waiting_session(json!({"phase": "blocked", "on": "external"})));
    assert!(!waiting_session(
        json!({"phase": "blocked", "on": "external", "wait": "deployment"})
    ));
    assert!(!waiting_session(
        json!({"phase": "blocked", "on": "user", "wake_at": "2026-01-01T00:00:20Z"})
    ));

    let workflow_session = |workflow: Value| {
        let session: ClientSession = serde_json::from_value(json!({
            "machine_id": "m", "session_id": "s", "state": "idle", "workflows": [workflow],
        }))
        .unwrap();
        session_state(&session).is_waiting
    };
    assert!(workflow_session(json!({"task_id": "w", "status": "running"})));
    assert!(!workflow_session(
        json!({"task_id": "w", "running": false, "status": "running"})
    ));
    assert!(!workflow_session(json!({"status": "running"})));

    let before = vec!["one".to_owned(), "two".to_owned(), "three".to_owned()];
    let after = open_conversation(&before, "two");
    assert_eq!(after, vec!["two", "one", "three"]);
    assert_eq!(after.iter().filter(|key| key.as_str() == "two").count(), 1);
    let duplicate_entries = vec!["two".to_owned(), "one".to_owned(), "two".to_owned()];
    assert_ne!(
        after, duplicate_entries,
        "a non-deduplicating LRU would produce the wrong list"
    );
}
