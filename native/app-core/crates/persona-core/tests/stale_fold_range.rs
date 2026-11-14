use persona_core::Item;
use persona_core::continuity::{Batch, Request, Session, Step};

fn items(json: &str) -> Vec<Item> {
    Item::list_from_json(json).unwrap()
}

fn fold(id: &str, seq: i64, through_seq: i64, through_id: &str) -> String {
    format!(
        r#"{{"id":"{id}","seq":{seq},"role":"assistant","kind":"fold","fold":{{"items":2,"tools":1,"thoughts":1,"through_seq":{through_seq},"through_id":"{through_id}"}}}}"#
    )
}

fn ids(session: &Session) -> Vec<&str> {
    session.reducer().items().iter().map(|item| item.id.as_str()).collect()
}

fn stale_response_after_fold_change(
    initial_through_seq: i64,
    initial_through_id: &str,
    current_through_seq: i64,
    current_through_id: &str,
) -> (Session, Step) {
    let mut session = Session::new();
    session.restore(
        items(&format!(
            "[{}]",
            fold("fold:2#0", 2, initial_through_seq, initial_through_id)
        )),
        Some("h".into()),
        3,
        Some(false),
        true,
    );
    let opened = session.open_fold("fold:2#0");
    assert_eq!(
        opened.requests,
        vec![Request::FetchRange {
            fold_id: Some("fold:2#0".into()),
            from_seq: 2,
            through_seq: initial_through_seq,
        }]
    );

    // While that request is in flight, a latest page changes the fold and adds the final reply at line 5.
    let latest = session.received(Batch {
        items: items(&format!(
            "[{},{}]",
            fold("fold:2#0", 2, current_through_seq, current_through_id),
            r#"{"id":"5#0","seq":5,"role":"assistant","kind":"text","text":"reply"}"#
        )),
        history: Some("h".into()),
        from_seq: None,
        to_seq: Some(5),
        backlog: true,
        has_more: Some(false),
        more_after: false,
    });
    assert!(latest.requests.is_empty());
    assert_eq!(session.reducer().covered(), 5);
    assert!(session.verified());

    // This is a valid response to the original request, but its range is no longer current.
    let stale = session.range(
        "fold:2#0",
        items(
            r#"[
              {"id":"2#0","seq":2,"role":"assistant","kind":"thinking","text":"old plan"},
              {"id":"3#0","seq":3,"role":"assistant","kind":"tool_use","tool_use_id":"call","tool_name":"Read"}
            ]"#,
        ),
        false,
    );
    (session, stale)
}

fn assert_restarts_current_range(session: &Session, stale: Step, through_seq: i64) {
    assert_eq!(
        stale.requests,
        vec![Request::FetchRange {
            fold_id: Some("fold:2#0".into()),
            from_seq: 2,
            through_seq,
        }]
    );
    assert_eq!(ids(session), ["fold:2#0", "5#0"]);
    assert_eq!(session.reducer().covered(), 5);
    assert!(session.verified());
}

#[test]
fn stale_range_does_not_replace_a_grown_fold() {
    let (session, stale) = stale_response_after_fold_change(3, "3#0", 4, "4#0");

    // Discard the old pages and restart at 2..4, preserving the fold and chat coverage until that response arrives.
    assert_restarts_current_range(&session, stale, 4);
}

#[test]
fn stale_range_does_not_replace_a_fold_whose_through_id_changed() {
    let (session, stale) = stale_response_after_fold_change(3, "3#0", 3, "3#1");

    // The sequence bound alone is insufficient: a later block on the same line is also a different range.
    assert_restarts_current_range(&session, stale, 3);
}

#[test]
fn unchanged_fold_still_opens_across_multiple_pages() {
    let mut session = Session::new();
    session.restore(
        items(&format!("[{}]", fold("fold:2#0", 2, 4, "4#0"))),
        Some("h".into()),
        4,
        Some(false),
        true,
    );
    session.open_fold("fold:2#0");

    let first = session.range(
        "fold:2#0",
        items(
            r#"[
              {"id":"2#0","seq":2,"role":"assistant","kind":"thinking","text":"plan"},
              {"id":"3#0","seq":3,"role":"assistant","kind":"tool_use","tool_use_id":"call","tool_name":"Read"}
            ]"#,
        ),
        true,
    );
    assert_eq!(
        first.requests,
        vec![Request::FetchRange {
            fold_id: Some("fold:2#0".into()),
            from_seq: 4,
            through_seq: 4,
        }]
    );
    assert_eq!(ids(&session), ["fold:2#0"]);

    let last = session.range(
        "fold:2#0",
        items(r#"[{"id":"4#0","seq":4,"role":"assistant","kind":"text","text":"result"}]"#),
        false,
    );
    assert!(last.requests.is_empty());
    assert_eq!(ids(&session), ["2#0", "3#0", "4#0"]);
    assert_eq!(session.reducer().covered(), 4);
}

#[test]
fn unchanged_parallel_folds_still_complete_in_either_order() {
    let mut session = Session::new();
    session.restore(
        items(&format!(
            "[{},{},{}]",
            fold("fold:a#0", 2, 3, "3#0"),
            fold("fold:b#0", 5, 6, "6#0"),
            r#"{"id":"7#0","seq":7,"role":"assistant","kind":"text","text":"reply"}"#
        )),
        Some("h".into()),
        7,
        Some(false),
        true,
    );
    assert_eq!(session.open_fold("fold:a#0").requests.len(), 1);
    assert_eq!(session.open_fold("fold:b#0").requests.len(), 1);

    let second = session.range(
        "fold:b#0",
        items(
            r#"[
              {"id":"5#0","seq":5,"role":"assistant","kind":"thinking","text":"plan b"},
              {"id":"6#0","seq":6,"role":"assistant","kind":"text","text":"work b"}
            ]"#,
        ),
        false,
    );
    assert!(second.requests.is_empty());
    assert_eq!(ids(&session), ["fold:a#0", "5#0", "6#0", "7#0"]);

    let first = session.range(
        "fold:a#0",
        items(
            r#"[
              {"id":"2#0","seq":2,"role":"assistant","kind":"thinking","text":"plan a"},
              {"id":"3#0","seq":3,"role":"assistant","kind":"text","text":"work a"}
            ]"#,
        ),
        false,
    );
    assert!(first.requests.is_empty());
    assert_eq!(ids(&session), ["2#0", "3#0", "5#0", "6#0", "7#0"]);
    assert_eq!(session.reducer().covered(), 7);
}
