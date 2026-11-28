import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The personal Agents on the Messages page (`LarkAgentFeed`, web `modules/lark/mixed-feed.test.ts`): put in with the
/// Feishu chats by their latest message without moving a chat, waiting for the next page when older than every chat
/// loaded, on the shelf once pinned, and alone under the "AI Assistants" pill.
@MainActor struct LarkAgentFeedTests {
    private let base = Date(timeIntervalSince1970: 1_800_000_000)
    private func at(_ h: Double) -> Date { base.addingTimeInterval(h * 3600) }
    private func chat(_ id: String, _ h: Double, unread: Int = 0, name: String? = nil, preview: String = "") -> LarkConversation {
        var c = LarkConversation(id: id, name: name ?? id, isGroup: false, unread: unread, preview: preview)
        c.updatedAt = at(h)
        return c
    }
    private func agent(_ id: String, _ h: Double?, unread: Int = 0, title: String? = nil, preview: String? = nil, mine: Bool? = nil) -> PersonalAgentSession {
        PersonalAgentSession(id: id, source: "example", sessionKey: id, provider: "grok_bot", title: title ?? id, canSend: true,
                             unreadCount: unread, rankTimeMs: h.map { Int64(at($0).timeIntervalSince1970 * 1000) } ?? 0,
                             previewText: preview, previewIsMine: mine)
    }
    private func ids(_ rows: [LarkFeedRow]) -> [String] { rows.map(\.id) }

    @Test func agentsGoBeforeTheFirstOlderChatWithoutMovingAChat() {
        let rows = LarkAgentFeed.interleave([chat("c5", 5), chat("c8", 8), chat("c2", 2)], [agent("a6", 6), agent("a9", 9), agent("a1", 1)])
        #expect(ids(rows) == ["agent:a9", "agent:a6", "c5", "c8", "c2", "agent:a1"])
    }

    @Test func olderAgentsWaitForTheNextFeishuPage() {
        let loaded = [chat("c9", 9), chat("c5", 5)]
        let agents = [agent("a7", 7), agent("a5", 5), agent("a2", 2), agent("none", nil)]
        let paged = LarkAgentFeed.rows(chats: loaded, loaded: loaded, more: true, agents: agents, query: "", agentsOnly: false, unreadOnly: false)
        #expect(ids(paged) == ["c9", "agent:a7", "c5", "agent:a5"])
        let all = LarkAgentFeed.rows(chats: loaded, loaded: loaded, more: false, agents: agents, query: "", agentsOnly: false, unreadOnly: false)
        #expect(ids(all) == ["c9", "agent:a7", "c5", "agent:a5", "agent:a2", "agent:none"])
        // nothing of Feishu loaded yet: no Agent jumps in ahead of the first page
        #expect(LarkAgentFeed.rows(chats: [], loaded: [], more: true, agents: agents, query: "", agentsOnly: false, unreadOnly: false).isEmpty)
    }

    @Test func aSearchListsEveryMatchingAgentByNameServiceOrLatestMessage() {
        let loaded = [chat("c9", 9)]
        let agents = [agent("a1", 1, title: "Helper", preview: "Draft ready"), agent("a2", 2, title: "Other")]
        #expect(ids(LarkAgentFeed.rows(chats: loaded, loaded: loaded, more: true, agents: agents, query: "draft", agentsOnly: false, unreadOnly: false)) == ["c9", "agent:a1"])
        #expect(ids(LarkAgentFeed.rows(chats: [], loaded: loaded, more: true, agents: agents, query: "grok", agentsOnly: false, unreadOnly: false)) == ["agent:a2", "agent:a1"])
    }

    @Test func unreadAndAgentsOnlyFilters() {
        let loaded = [chat("c9", 9, unread: 2)]
        let agents = [agent("a7", 7, unread: 1), agent("a8", 8)]
        #expect(ids(LarkAgentFeed.rows(chats: loaded, loaded: loaded, more: false, agents: agents, query: "", agentsOnly: false, unreadOnly: true)) == ["c9", "agent:a7"])
        #expect(ids(LarkAgentFeed.rows(chats: loaded, loaded: loaded, more: true, agents: agents, query: "", agentsOnly: true, unreadOnly: false)) == ["agent:a8", "agent:a7"])
    }

    @Test func pinnedAgentsJoinThePinnedChatsOnTheShelf() {
        let agents = [agent("a9", 9), agent("a1", 1), agent("a4", 4)]
        let shelf = LarkAgentFeed.shelf(chats: [chat("p8", 8)], agents: agents, pins: ["a1", "a9", "gone"], agentsOnly: false)
        #expect(ids(shelf) == ["agent:a9", "p8", "agent:a1"])
        #expect(ids(LarkAgentFeed.shelf(chats: [chat("p8", 8)], agents: agents, pins: ["a1"], agentsOnly: true)) == ["agent:a1"])
        #expect(shelf.map(\.unread) == [0, 0, 0])
    }

    @Test func thePreviewIsOneLineAndMarksTheUsersOwn() {
        #expect(LarkAgentFeed.preview(agent("a", 1, preview: " hello\n world ")) == "hello world")
        #expect(LarkAgentFeed.preview(agent("a", 1, preview: "done", mine: true)) == String(localized: "Me: \("done")"))
        #expect(LarkAgentFeed.preview(agent("a", 1)) == nil)
    }

    @Test func theHubsPreviewFieldsDecodeAndStayOptional() throws {
        let full = try JSON.decoder.decode(PersonalAgentSession.self, from: Data("""
        {"id":"example:a","source":"example","sessionKey":"a","provider":"muse","title":"A","canSend":true,"unreadCount":0,
         "rankTimeMs":5,"previewText":"Hi","previewIsMine":true,"onTopRankTimeMs":42}
        """.utf8))
        #expect(full.previewText == "Hi" && full.previewIsMine == true && full.onTopRankTimeMs == 42)
        let old = try JSON.decoder.decode(PersonalAgentSession.self, from: Data("""
        {"id":"example:a","source":"example","sessionKey":"a","provider":"muse","title":"A","canSend":true,"unreadCount":0,"rankTimeMs":5}
        """.utf8))
        #expect(old.previewText == nil && old.previewIsMine == nil && old.onTopRankTimeMs == nil)
    }

    @Test func theChargeIsDrawnWheneverThereIsAFigureWhateverShowSays() {
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 70, show: false))?.percent == 70)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 70))?.tone == .ok)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", remaining: 0.15))?.tone == .low)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 20))?.tone == .low)
        let used = PersonalAgentCharge(PersonalAgentQuota(state: "exhausted"))
        #expect(used?.level == 0 && used?.percent == 0 && used?.tone == .exhausted)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "exhausted", percent: 40))?.tone == .exhausted)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 60, stale: true))?.tone == .stale)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 140))?.level == 1)
        #expect(PersonalAgentCharge(PersonalAgentQuota(state: "unknown")) == nil)
        #expect(PersonalAgentCharge(nil) == nil)
        let resets = PersonalAgentCharge(PersonalAgentQuota(state: "ok", percent: 50, resetsAt: .number(1_800_000_000_000)))
        #expect(resets?.resetsAt == Date(timeIntervalSince1970: 1_800_000_000))
    }

    @Test func aQuotaInAnotherShapeNeverBreaksTheList() throws {
        let rows = try JSON.decoder.decode([PersonalAgentSession].self, from: Data("""
        [{"id":"x:a","source":"x","sessionKey":"a","provider":"muse","title":"A","canSend":true,"unreadCount":0,"rankTimeMs":5,
          "quota":{"state":"low","percent":"fifteen","remaining":0.15,"buckets":[{"id":"day"}],"show":false}},
         {"id":"x:b","source":"x","sessionKey":"b","provider":"muse","title":"B","canSend":true,"unreadCount":0,"rankTimeMs":5,"quota":"n/a"}]
        """.utf8))
        #expect(rows.count == 2)
        #expect(rows[0].charge?.percent == 15 && rows[0].charge?.tone == .low)
        #expect(rows[1].charge == nil)
    }

    @Test func theFeedModelKeepsItsListWhenAReadFails() {
        let model = LarkAgentFeedModel(sessions: [agent("a", 1)])
        #expect(model.sessions.map(\.id) == ["a"])
    }
}
