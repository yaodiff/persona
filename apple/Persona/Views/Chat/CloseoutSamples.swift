// i18n-ignore-file: DEBUG-only page; its sample content is data, not interface text.
#if DEBUG
import SwiftUI
import PersonaWire

extension View {
    /// `-showCloseoutSample confirmed|contradicted|blocked|rows|orphaned|decision|armed`: one state of the closeout
    /// card (#3612), the list rows' line, "Nobody is working on this" (#3618) or the answerable decision row (#3804) in a
    /// sheet, with made-up content; no hub. For screenshots in each style (`-ui.style`) and appearance (`-ui.scheme`).
    /// The same states on their real screens need a hub that sends them (the mock hub's `MOCK_CLOSEOUT=1` and
    /// `MOCK_ORPHANED=1`, docs/apple/ios.md "DEBUG launch arguments").
    func closeoutSample() -> some View { modifier(CloseoutSampleSheet()) }
}

private struct CloseoutSampleSheet: ViewModifier {
    @State private var sample = UserDefaults.standard.string(forKey: "showCloseoutSample").flatMap { CloseoutSampleKind(rawValue: $0) }

    func body(content: Content) -> some View {
        content.sheet(item: $sample) { CloseoutSamplePage(kind: $0) }
    }
}

private enum CloseoutSampleKind: String, Identifiable {
    case confirmed, contradicted, blocked, rows, orphaned, decision, armed
    var id: String { rawValue }
}

private struct CloseoutSamplePage: View {
    var kind: CloseoutSampleKind
    @State private var answers = DecisionAnswers(api: { _ in nil }, failed: { _ in }, taken: {})

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.l) { sample }
                    .padding(Theme.Space.page)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .themedPage()
            .navigationTitle(Text(verbatim: "Sample: \(kind.rawValue)"))
            .navigationBarTitleDisplayMode(.inline)
        }
        .accessibilityIdentifier("closeout-sample-\(kind.rawValue)")
    }

    @ViewBuilder private var sample: some View {
        switch kind {
        case .confirmed:
            CloseoutCard(closeout: Self.confirmed, artifacts: Self.artifacts)
        case .contradicted:
            CloseoutCard(closeout: Self.contradicted, artifacts: [Self.artifacts[0]])
        case .blocked:
            CloseoutCard(closeout: Self.blocked, artifacts: [WorkArtifact(kind: "branch", ref: "feat/split-users")])
        case .rows:
            VStack(spacing: Theme.Space.m) {
                ForEach(Self.rows) { SlotRow(session: $0).styled(.avatar, dense: false) }
            }
        case .orphaned:
            ForEach(Self.orphaned, id: \.id) { todo in
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(todo.title).font(Theme.Font.bodyStrong).lineLimit(1)
                    Text(TaskState.followup.todoLabel).font(Theme.Font.captionMedium).foregroundStyle(.secondary)
                    OrphanedLine(todo: todo, state: .followup)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .card(padding: Theme.Space.l)
            }
        case .decision:
            DecisionRow(todo: Self.task, ask: Self.ask(why: "priority"), group: "", open: {}, answers: answers)
                .card(padding: Theme.Space.l)
        case .armed:
            DecisionRow(todo: Self.task, ask: Self.ask(why: "irreversible"), group: "", open: {}, answers: answers)
                .card(padding: Theme.Space.l)
                // The first tap on an option of a step that cannot be undone arms it.
                .task { await answers.choose(0, of: Self.ask(why: "irreversible"), todo: Self.task.id, group: "") }
        }
    }

    // MARK: Made-up content

    private static let artifacts = [WorkArtifact(kind: "pr", ref: "https://github.com/example/app/pull/31"),
                                    WorkArtifact(kind: "commit", ref: "4f2a91c")]

    private static let confirmed = Closeout(
        status: "done",
        did: ["Report export now streams page by page instead of loading everything into memory",
              "Added a 500,000-row load test for the export endpoint"],
        merged: "yes", deployed: "yes",
        verified: "Exported 500,000 rows in production in 11 s (it timed out at 94 s before)",
        checked: .init(merged: "yes", deployed: "yes"))

    private static let contradicted = Closeout(
        status: "done",
        did: ["Upgraded the payment SDK from v3 to v4; callbacks verify with the new API"],
        merged: "yes", deployed: "no",
        verified: "212 unit tests pass; not tried in the sandbox",
        missing: "Someone has to run a real payment in the sandbox before release",
        checked: .init(merged: "no"))

    private static let blocked = Closeout(
        status: "blocked", waiting: "the DBA to approve the migration window",
        did: ["Wrote a repeatable migration script that splits the users table"],
        merged: "no", verified: "Ran on staging: 1.2 million rows in 3 min 40 s")

    private static func session(_ id: String, title: String, state: String, last: String, work: [String: Any]) -> Session? {
        let fields: [String: Any] = ["machine_id": "example", "session_id": id, "title": title, "cwd": "/example/app",
                                     "status": "done", "state": state, "agent": "claude", "pending_interactions": 0,
                                     "last_text": last, "work": work,
                                     "updated_at": ISO8601DateFormatter().string(from: Date().addingTimeInterval(-600))]
        guard let data = try? JSONSerialization.data(withJSONObject: fields) else { return nil }
        return try? JSON.decoder.decode(Session.self, from: data)
    }

    /// A done turn the hub confirmed, one whose claim it found untrue (not finished: no phase), a blocked one nobody
    /// checked, and a not-done one with a line of the closeout's own.
    private static var rows: [Session] {
        [session("c1", title: "Fix the report export timeout", state: "idle", last: "",
                 work: ["phase": "done", "line": "Export streams page by page; 500,000 rows in 11 s",
                        "closeout": ["status": "done", "merged": "yes", "deployed": "yes", "checked": ["merged": "yes", "deployed": "yes"]]]),
         session("c2", title: "Upgrade the payment SDK", state: "unread", last: "",
                 work: ["phase": "", "line": "Says merged, but the pull request is not merged",
                        "closeout": ["status": "done", "merged": "yes", "deployed": "no", "checked": ["merged": "no"]]]),
         session("c3", title: "Split the users table", state: "idle", last: "",
                 work: ["phase": "blocked", "on": "external", "line": "Waiting for the DBA's migration window",
                        "wait": "the DBA", "closeout": ["status": "blocked", "merged": "no", "waiting": "the DBA"]]),
         session("c4", title: "Highlight search results", state: "unread", last: "",
                 work: ["phase": "", "line": "Not finished: needs you to pick a tokenizer",
                        "closeout": ["status": "not_done", "missing": "needs you to pick a tokenizer"]])]
            .compactMap { $0 }
    }

    private static let orphaned = [
        TodoItem(id: "o1", title: "Check PR #42", orphaned: TodoOrphaned(reason: "error")),
        TodoItem(id: "o2", title: "Move the blog to Astro", orphaned: TodoOrphaned(reason: "offline")),
        TodoItem(id: "o3", title: "Write the release notes", orphaned: TodoOrphaned(reason: "closed")),
    ]

    private static let task = TodoItem(id: "d1", title: "Release 2.4")
    private static let askedAt = ISO8601DateFormatter().string(from: Date().addingTimeInterval(-3600))
    private static let deadline = ISO8601DateFormatter().string(from: Date().addingTimeInterval(5 * 3600))

    private static func ask(why: String) -> TodoAsk {
        let irreversible = why == "irreversible"
        return TodoAsk(
            outcome: "decide", verdict: "Needs your decision: release 2.4", recommend: "Ship to beta", askedAt: askedAt,
            question: irreversible ? "Release 2.4 to production now?" : "Ship 2.4 to beta today, or wait for the crash fix?",
            whyOwner: why,
            options: [.init(label: "Ship to beta", leadsTo: "Testers get it tonight; the crash fix follows as 2.4.1"),
                      .init(label: "Wait for the fix", leadsTo: "Beta slips to Thursday"),
                      .init(label: "Cancel the release", leadsTo: "The branch is kept; nothing ships")],
            costOfWaiting: "the beta slot closes at 18:00",
            background: "From your message in the main chat on Monday: “get 2.4 out this week”. The build passed; one crash is open.",
            deadline: irreversible ? nil : deadline, defaultAction: irreversible ? "never" : "taken",
            responses: TodoAsk.Response.allCases.map(\.rawValue))
    }
}
#endif
