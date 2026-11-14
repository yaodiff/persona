//! Replays the Agents list projection fixtures, with the fixture names as the case-set oracle.
//!
//! The expected values are only read from `spec/fixtures/`; none are derived from the Rust implementation.

use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};

use persona_core::sort::Input;
use serde_json::Value;

fn fixture_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../spec/fixtures/sort")
}

fn documents() -> Vec<(String, Value)> {
    let mut paths: Vec<PathBuf> = fs::read_dir(fixture_dir())
        .expect("spec/fixtures/sort")
        .map(|entry| entry.expect("directory entry").path())
        .filter(|path| path.extension().is_some_and(|extension| extension == "json"))
        .collect();
    paths.sort();
    paths
        .into_iter()
        .map(|path| {
            let name = path.file_name().expect("file name").to_string_lossy().into_owned();
            let document =
                serde_json::from_str(&fs::read_to_string(path).expect("fixture file")).expect("fixture JSON");
            (name, document)
        })
        .collect()
}

fn names(documents: &[(String, Value)]) -> BTreeSet<String> {
    let mut result = BTreeSet::new();
    for (_, document) in documents {
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("case name").to_owned();
            assert!(result.insert(name.clone()), "duplicate sort case name: {name}");
        }
    }
    result
}

#[test]
fn fixture_manifest_has_unique_names_and_projection_shape() {
    let documents = documents();
    let actual = names(&documents);
    let files: BTreeSet<&str> = documents.iter().map(|(name, _)| name.as_str()).collect();
    assert_eq!(
        files,
        BTreeSet::from([
            "filters.json",
            "folding.json",
            "grouping.json",
            "hub_state.json",
            "ordering.json",
            "session_state.json",
        ]),
        "sort fixture file set changed"
    );
    // A manifest snapshot, not a count of the cases the loop happens to run. Adding, dropping or renaming any case fails
    // here; the shape assertions below also pin each operation rather than allowing a misfiled case to pass unnoticed.
    let expected: BTreeSet<String> = [
        "active_space_uses_rank_not_slots",
        "client_state_precedence",
        "compact_expands_idle_without_reordering",
        "cross_machine_same_session_id",
        "cross_machine_same_session_id_reversed",
        "empty_list",
        "filter_all",
        "filter_needsyou",
        "filter_unread",
        "filter_working",
        "folded_machine_hides_rows",
        "folded_space_keeps_counts",
        "folded_state_and_expanded_idle",
        "hub_blocked",
        "hub_done_seen_after",
        "hub_done_seen_at_tolerance",
        "hub_done_seen_before_tolerance",
        "hub_done_seen_equal",
        "hub_done_unseen",
        "hub_idle",
        "hub_offline",
        "hub_pending_offline",
        "hub_unknown",
        "hub_working_seen",
        "kept_cannot_escape_base",
        "machine_search_before_counts",
        "machine_uses_registry_order",
        "mute_allow_and_explicit_override",
        "pinned_offline_is_visible",
        "pinned_sessions_use_pin_order_once",
        "quick_filter_ignores_rank",
        "quick_spaces_use_updated_time",
        "rank_all_states_and_mutes",
        "search_expands_idle",
        "space_header_counts",
        "space_time_tie",
        "space_time_tie_reversed",
        "spaces_pin_busy_muted_quiet",
        "unread_keeps_seen_rows",
        "unread_reselected_drops_seen_rows",
        "waiting_requires_quiet_state",
        "working_since_then_id",
        "working_since_then_id_reversed",
    ]
    .into_iter()
    .map(str::to_owned)
    .collect();
    assert_eq!(actual, expected, "sort fixture manifest changed");

    for (file, document) in &documents {
        assert_eq!(document["schema_version"], 1, "{file}: schema");
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let operation = document["operation"].as_str().expect("operation");
            let expected_operation = match name {
                "client_state_precedence" | "waiting_requires_quiet_state" => "session_state",
                name if name.starts_with("hub_") => "hub_state",
                _ => "agents_list",
            };
            assert_eq!(operation, expected_operation, "{file}/{name}: operation");
            assert!(case["input"].is_object(), "{file}/{name}: input shape");
            assert!(case["expected"].is_object(), "{file}/{name}: expected shape");
            if operation == "agents_list" {
                for key in ["sessions", "machines", "pins", "mutes", "settings"] {
                    assert!(case["input"].get(key).is_some(), "{file}/{name}: missing input.{key}");
                }
                for key in ["counts", "groups", "unread_shown"] {
                    assert!(
                        case["expected"].get(key).is_some(),
                        "{file}/{name}: missing expected.{key}"
                    );
                }
                for group in case["expected"]["groups"].as_array().expect("groups") {
                    for key in ["id", "title", "rows", "visible_rows", "folded"] {
                        assert!(group.get(key).is_some(), "{file}/{name}: missing group.{key}");
                    }
                }
            }
        }
    }
}

fn projection_names(documents: &[(String, Value)]) -> BTreeSet<String> {
    documents
        .iter()
        .filter(|(_, document)| document["operation"] == "agents_list")
        .flat_map(|(_, document)| document["cases"].as_array().expect("cases").iter())
        .map(|case| case["name"].as_str().expect("name").to_owned())
        .collect()
}

#[test]
fn agents_list_projection_replays_every_fixture() {
    let mut replayed = BTreeSet::new();
    for (file, document) in documents() {
        if document["operation"] != "agents_list" {
            continue;
        }
        for case in document["cases"].as_array().expect("cases") {
            let name = case["name"].as_str().expect("name");
            let input: Input = serde_json::from_value(case["input"].clone())
                .unwrap_or_else(|error| panic!("{file}/{name}: input: {error}"));
            let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
            assert_projection(&actual, &case["expected"], &format!("{file}/{name}"));
            assert!(replayed.insert(name.to_owned()), "replayed {name} twice");
        }
    }
    let documents = documents();
    // The declared document operation, not a case-name prefix or a relayed count, determines the replay subset.
    let expected = projection_names(&documents);
    assert_eq!(replayed, expected, "projection replay set differs from the manifest");
    let count = replayed.len();
    eprintln!("sort projection: {count} cases replayed, with full expected-object equality");
}

#[test]
fn awaiting_reply_is_not_folded_like_plain_idle() {
    let document: Value =
        serde_json::from_str(&fs::read_to_string(fixture_dir().join("folding.json")).expect("folding")).expect("JSON");
    let case = document["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["name"] == "folded_state_and_expanded_idle")
        .expect("idle fixture");
    let mut input = case["input"].clone();
    input["sessions"]
        .as_array_mut()
        .expect("sessions")
        .iter_mut()
        .find(|session| session["session_id"] == "quiet")
        .expect("idle session")["awaits_reply"] = Value::Bool(true);
    let input: Input = serde_json::from_value(input).expect("mutated fixture input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
    // Derived from the specified iOS rank rule (idle + awaits_reply + unmuted), not from this Rust implementation.
    assert_eq!(actual["groups"][1]["id"], "state/awaiting");
    assert_eq!(actual["groups"][1]["rows"], serde_json::json!(["machine-a/quiet"]));
    assert_eq!(
        actual["groups"][1]["visible_rows"],
        serde_json::json!(["machine-a/quiet"])
    );
}

#[test]
fn held_in_list_keeps_position_and_stays_visible() {
    // An Unread agent that is opened keeps its rank-1 position and is not folded into idle, per
    // `AppModel.heldInList` (#2213). It does NOT enter the Pinned section; it stays in its rank section.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "a", "state": "idle", "updated_at": "2026-01-01T00:00:01Z"},
            {"machine_id": "m", "session_id": "b", "state": "working", "updated_at": "2026-01-01T00:00:02Z"},
            {"machine_id": "m", "session_id": "c", "state": "idle", "updated_at": "2026-01-01T00:00:03Z"},
        ],
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {
            "grouping": "state",
            "quick": "all",
            "held": "m/a",
        }
    }))
    .expect("held input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    // "a" is idle but held → keeps rank 1 (unread), stays visible, NOT in the pinned section.
    let groups = actual["groups"].as_array().expect("groups");
    // No pinned section should exist (nothing is explicitly pinned).
    assert!(
        !groups.iter().any(|g| g["id"] == "pinned"),
        "held agent does not create a pinned section"
    );
    // "a" appears in the unread section (rank 1), not idle.
    let unread = groups.iter().find(|g| g["id"] == "state/unread").expect("unread group");
    assert!(
        unread["rows"]
            .as_array()
            .expect("unread rows")
            .iter()
            .any(|v| v == "m/a"),
        "held agent keeps unread rank"
    );
    // "c" is idle without held → goes into state/idle.
    let idle = groups.iter().find(|g| g["id"] == "state/idle").expect("idle group");
    assert!(idle["rows"].as_array().expect("idle rows").iter().any(|v| v == "m/c"));
}

#[test]
fn since_query_filters_by_updated_at() {
    let base_sessions = serde_json::json!([
        {"machine_id": "m", "session_id": "recent", "state": "idle", "updated_at": "2026-01-01T12:00:00Z"},
        {"machine_id": "m", "session_id": "old", "state": "idle", "updated_at": "2025-12-31T00:00:00Z"},
    ]);
    let base = serde_json::json!({
        "sessions": base_sessions,
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {"grouping": "state", "quick": "all", "expanded_idle": ["state/idle"]},
    });

    // Without a `since:` token both sessions appear.
    let input: Input = serde_json::from_value(base.clone()).expect("no since");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let rows = &actual["groups"][0]["rows"];
    assert_eq!(rows.as_array().expect("rows").len(), 2);

    // With `since:24h` and `now` at 2026-01-01T18:00:00Z, only the recent one (12:00) matches.
    let mut with_since = base.clone();
    with_since["settings"]["query"] = serde_json::json!("since:24h");
    with_since["settings"]["now"] = serde_json::json!("2026-01-01T18:00:00Z");
    let input: Input = serde_json::from_value(with_since).expect("since input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let rows = &actual["groups"][0]["rows"];
    assert_eq!(rows, &serde_json::json!(["m/recent"]));

    // Without `now` the since query matches nothing (no clock).
    let mut no_now = base.clone();
    no_now["settings"]["query"] = serde_json::json!("since:24h");
    let input: Input = serde_json::from_value(no_now).expect("no now");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let total: usize = actual["groups"]
        .as_array()
        .expect("groups")
        .iter()
        .map(|g| g["rows"].as_array().map_or(0, |a| a.len()))
        .sum();
    assert_eq!(total, 0, "since: without now must match nothing");
}

#[test]
fn background_waiting_quick_filter_shows_only_rank_4() {
    // BackgroundWaiting quick filter should only show rank-4 (background waiting) section.
    let input: Input = serde_json::from_value(serde_json::json!({
        "sessions": [
            {"machine_id": "m", "session_id": "bg", "state": "working", "background_waiting": true, "updated_at": "2026-01-01T00:00:01Z"},
            {"machine_id": "m", "session_id": "w", "state": "working", "updated_at": "2026-01-01T00:00:02Z"},
            {"machine_id": "m", "session_id": "n", "state": "needsYou", "updated_at": "2026-01-01T00:00:03Z"},
        ],
        "machines": [{"id": "m", "name": "Machine"}],
        "pins": {},
        "mutes": {},
        "settings": {"grouping": "state", "quick": "backgroundWaiting"},
    }))
    .expect("bg input");
    let actual = serde_json::to_value(persona_core::sort::project(&input)).expect("projection");
    let group_ids: Vec<&str> = actual["groups"]
        .as_array()
        .expect("groups")
        .iter()
        .map(|g| g["id"].as_str().expect("id"))
        .collect();
    assert_eq!(
        group_ids,
        vec!["state/background"],
        "only the background-waiting section appears"
    );
    assert_eq!(actual["groups"][0]["rows"], serde_json::json!(["m/bg"]));
}

#[test]
fn fixture_assertion_catches_a_wrong_sort_comparison() {
    let document: Value =
        serde_json::from_str(&fs::read_to_string(fixture_dir().join("ordering.json")).expect("ordering"))
            .expect("JSON");
    let case = document["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["name"] == "working_since_then_id")
        .expect("date-order fixture");
    let input: Input = serde_json::from_value(case["input"].clone()).expect("input");
    let correct = serde_json::to_value(persona_core::sort::project(&input)).expect("projection JSON");
    assert_eq!(
        correct, case["expected"],
        "the reference comparison must agree with the fixture"
    );

    // Deliberately reverse the production comparison, then run it against the fixture-derived row set and expectation.
    // The assertion helper is shared with the replay test: the mutation must be a mismatch, not merely a code review.
    let input: Input = serde_json::from_value(case["input"].clone()).expect("mutation fixture input");
    let wrong = serde_json::to_value(persona_core::sort::project_with_order(&input)).expect("projection JSON");
    let failure = std::panic::catch_unwind(|| {
        assert_projection(
            &wrong,
            &case["expected"],
            "ordering.json/working_since_then_id (oldest first)",
        );
    });
    assert!(
        failure.is_err(),
        "reversing the date comparison must fail the shared fixture assertion"
    );
}

fn assert_projection(actual: &Value, expected: &Value, label: &str) {
    assert_eq!(actual, expected, "{label}: complete projection");
}
