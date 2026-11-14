import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Messages Persona types in for the model (`Injected`, docs/protocol/protocol.md「Messages injected by Persona」): read off the text the way
/// the hub does (the shared cases in spec/fixtures/injected), taken from the hub's mark, never from a line the user sent,
/// and drawn as a line of their own.
@MainActor
struct InjectedTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixtures: Decodable {
        struct Case: Decodable {
            struct Input: Decodable { var text: String }
            var name: String
            var input: Input
            var expected: Injected?
        }
        var version: Int
        var cases: [Case]
    }

    private static let notice = """
        🔔 任务「整理发布说明」做完了（MacBook）
        （这是 Persona 的任务通知，不是用户发的消息。这个任务是从主对话派出去的，用户设置了这类任务只在主对话里通知：手机不会为它另外推送，灵动岛上也没有，要靠你告诉用户。）

        Agent 最后说的：
        整理好了。

        你来决定要不要告诉用户、怎么说：一两句话说结果。
        """

    private func user(_ seq: Int, _ text: String, clientMsgID: String? = nil, queued: Bool? = nil) -> Item {
        var it = Item(id: "\(seq)#0", seq: seq, ts: nil, role: "user", kind: .text, text: text, toolUseID: nil, toolName: nil,
                      toolInput: nil, isError: nil, image: nil)
        it.clientMsgID = clientMsgID
        it.queued = queued
        return it
    }

    /// Every shared case: what the hub would send for it (fields left out are empty), or nothing.
    @Test func readsTheSharedFixturesLikeTheHub() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/injected/cases.json"))
        let fixtures = try JSONDecoder().decode(Fixtures.self, from: data)
        #expect(fixtures.version == 1)
        #expect(fixtures.cases.count >= 10)
        for c in fixtures.cases {
            #expect(Injected.parse(c.input.text) == c.expected, "\(c.name)")
        }
    }

    @Test func decodesTheHubsMark() throws {
        let json = #"{"id":"u1#0","seq":3,"role":"user","kind":"text","text":"⏰ 午间整理（定时任务）\n\n看邮件","#
            + #""injected":{"kind":"followup","state":"daily","title":"午间整理","line":"定时任务「午间整理」开始运行","detail":"看邮件"}}"#
        let it = try JSON.decoder.decode(Item.self, from: Data(json.utf8))
        #expect(it.injected == Injected(kind: "followup", state: "daily", title: "午间整理", line: "定时任务「午间整理」开始运行", detail: "看邮件"))
        let todo = try JSON.decoder.decode(Injected.self, from: Data(#"{"kind":"task_notice","state":"done","todo_id":"td_1","line":"x"}"#.utf8))
        #expect(todo.todoID == "td_1")
        // Kept in the chat cache with the rest of the item.
        #expect(try JSON.decoder.decode(Item.self, from: JSONEncoder().encode(it)).injected == it.injected)
    }

    @Test func theHubsMarkWinsAndTheUsersLinesStayTheirs() {
        var marked = user(1, "随便什么")
        marked.injected = Injected(kind: "system", state: "lost_tasks", line: "node 重启过，后台任务停了")
        #expect(Injected.of(marked)?.state == "lost_tasks")
        #expect(Injected.of(user(2, Self.notice))?.line == "任务「整理发布说明」做完了")
        // Sent from an app (it has the id the app sent it with): the user's, whatever it says.
        #expect(Injected.of(user(3, Self.notice, clientMsgID: "C0FFEE")) == nil)
        var peer = user(4, Self.notice)
        peer.from = "另一个会话"
        #expect(Injected.of(peer) == nil)
        var answer = user(5, Self.notice)
        answer.role = "assistant"
        #expect(Injected.of(answer) == nil)
        #expect(Injected.of(user(6, "🔔 任务「买菜」做完了\n帮我记一下")) == nil)
    }

    /// A pending bubble is never taken for a notice that came in with the same words.
    @Test func aNoticeIsNoPendingMessagesArrival() {
        let p = PendingMessage(text: Self.notice, images: [], createdAt: nil)
        #expect(!PendingMessage.arrived(p, as: user(1, Self.notice)))
        #expect(PendingMessage.arrived(PendingMessage(text: "你好", images: [], createdAt: nil), as: user(2, "你好")))
    }

    /// A finished turn of several messages: each is its own line, a notice among them too (#2512).
    @Test func aNoticeIsALineOfItsOwn() {
        let items = [user(1, "先做这个"),
                     Item(id: "2#0", seq: 2, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: "a", toolName: "Bash",
                          toolInput: nil, isError: nil, image: nil),
                     user(3, Self.notice, queued: true),
                     Item(id: "4#0", seq: 4, ts: nil, role: "assistant", kind: .text, text: "好了", toolUseID: nil, toolName: nil,
                          toolInput: nil, isError: nil, image: nil)]
        let lines = ChatLines(openWork: []).build(ChatStore.rebuild(items, settled: nil).blocks, live: nil)
        let users = lines.compactMap { line -> Item? in if case .row(.user(let i)) = line.content { i } else { nil } }
        #expect(users.map(\.id) == ["1#0", "3#0"])
        #expect(Injected.of(users[1]) != nil)
        // Without the notice the two messages are two bubbles as well.
        var plain = items
        plain[2] = user(3, "再那样", queued: true)
        let apart = ChatLines(openWork: []).build(ChatStore.rebuild(plain, settled: nil).blocks, live: nil)
        #expect(apart.filter { if case .row(.user) = $0.content { true } else { false } }.count == 2)
    }

    @Test func aNoticeOpensItsTask() {
        let main = "main-conv"
        let older = TodoItem(id: "td_old", title: "整理发布说明", createdAt: Date(timeIntervalSince1970: 1), origin: main)
        let newer = TodoItem(id: "td_new", title: "整理发布说明", createdAt: Date(timeIntervalSince1970: 2), origin: main)
        let elsewhere = TodoItem(id: "td_other", title: "整理发布说明", createdAt: Date(timeIntervalSince1970: 3), origin: "another")
        let todos = [older, newer, elsewhere]
        let n = Injected.parse(Self.notice)!
        // The newest task of that title this conversation opened.
        #expect(InjectedLine.task(of: n, todos: todos, conversation: main) == "td_new")
        // The hub's todo id when the app has that todo.
        var named = n
        named.todoID = "td_old"
        #expect(InjectedLine.task(of: named, todos: todos, conversation: main) == "td_old")
        named.todoID = "td_gone"
        #expect(InjectedLine.task(of: named, todos: todos, conversation: main) == "td_new")
        // None of that title: not tappable. A scheduled run never opens a task.
        #expect(InjectedLine.task(of: n, todos: [], conversation: main) == nil)
        let run = Injected.parse("⏰ 整理发布说明（定时任务）\n\n整理")!
        #expect(InjectedLine.task(of: run, todos: todos, conversation: main) == nil)
    }

    @Test func eachKindHasItsMark() {
        func icon(_ text: String) -> String? { Injected.parse(text).map(InjectedLook.icon) }
        #expect(icon(Self.notice) == Theme.Icon.successFilled)
        #expect(icon(Self.notice.replacingOccurrences(of: "做完了", with: "在等用户")) == Theme.Icon.waitsForYou)
        #expect(icon(Self.notice.replacingOccurrences(of: "做完了", with: "没开起来")) == Theme.Icon.errorFilled)
        #expect(icon("⏰ 早报（定时任务）\n\n早报") == Theme.Icon.schedule)
        #expect(icon("⏰（Persona 叫醒你，第 1 次）你上一轮停下时说在等「CI」。") == Theme.Icon.wakeUp)
        #expect(icon("⚠️ 运行你的 node 刚重启过（换了版本），……：\n- npm test") == Theme.Icon.infoFilled)
    }
}
