import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The main conversation's Compact Context / New Main Chat (#1762): what the chat shows for a compaction, the divider's words, and
/// the session's `compaction` from the hub.
struct MainConversationActionsTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func run(_ state: CompactionRun.State, finishedAgo: TimeInterval? = nil, error: String? = nil) -> CompactionRun {
        CompactionRun(state: state, error: error, startedAt: now.addingTimeInterval(-120),
                      finishedAt: finishedAgo.map { now.addingTimeInterval(-$0) })
    }

    @Test func runningAlwaysShows() {
        #expect(MainConversationActions.shown(run(.running), seen: nil, now: now) == .running)
    }

    @Test func doneShowsOnlyWhenSeenOrJustNow() {
        let old = run(.done, finishedAgo: 3600)
        #expect(MainConversationActions.shown(old, seen: nil, now: now) == nil, "a done one from long ago is not shown on every open")
        #expect(MainConversationActions.shown(old, seen: .done, now: now) == nil)
        if case .done = MainConversationActions.shown(old, seen: .running, now: now) {} else { Issue.record("seen finishing: shown") }
        if case .done = MainConversationActions.shown(run(.done, finishedAgo: 10), seen: nil, now: now) {} else { Issue.record("just now: shown") }
    }

    @Test func failedShowsUntilDismissed() {
        let failed = run(.failed, finishedAgo: 60, error: "  没写下结果 ")
        guard case .failed(let key, let error) = MainConversationActions.shown(failed, seen: nil, now: now) else {
            Issue.record("a recent failure shows"); return
        }
        #expect(error == "没写下结果")
        #expect(MainConversationActions.shown(failed, seen: nil, now: now, dismissed: key) == nil)
        #expect(MainConversationActions.shown(run(.failed, finishedAgo: 3600), seen: nil, now: now) == nil, "an old failure stays away")
        if case .failed(_, let e) = MainConversationActions.shown(run(.failed, finishedAgo: 3600), seen: .running, now: now) {
            #expect(e == "未知错误")
        } else { Issue.record("seen failing: shown") }
    }

    @Test func dividerTitle() {
        #expect(Compaction(trigger: "manual", preTokens: 181_234).dividerTitle == "以上内容已压缩 · 手动 · 压缩前 18.1 万 token")
        #expect(Compaction(trigger: "auto", preTokens: 150_000).dividerTitle == "以上内容已压缩 · 自动 · 压缩前 15 万 token")
        #expect(Compaction(trigger: nil, preTokens: 8_500).dividerTitle == "以上内容已压缩 · 压缩前 8,500 token")
        #expect(Compaction().dividerTitle == "以上内容已压缩")
    }

    @Test func archivedTitle() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        let d = cal.date(from: DateComponents(year: 2026, month: 11, day: 4, hour: 9))!
        #expect(MainConversationActions.archivedTitle(d, calendar: cal) == "旧主对话 · 至 11月4日")
    }

    @Test func sessionDecodesCompaction() throws {
        let json = #"""
        {"machine_id":"m","session_id":"s","assistant":true,
         "compaction":{"state":"failed","error":"超时","started_at":"2026-11-04T10:00:00Z","finished_at":"2026-11-04T10:10:00Z"}}
        """#
        let s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        #expect(s.compaction?.state == .failed)
        #expect(s.compaction?.error == "超时")
        #expect(s.compaction?.finishedAt != nil)
        let odd = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s","compaction":{"state":"later"}}"#.utf8))
        #expect(odd.compaction == nil, "a state this app does not know drops the compaction, not the session")
        let none = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s"}"#.utf8))
        #expect(none.compaction == nil)
    }
}
