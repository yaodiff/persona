import Foundation
import Testing
@testable import Persona

/// Thread (#2128): the hub's tree decoded, its rows in reading order, and the route asked.
struct InboxLineTests {
    private func item(_ id: String, _ title: String, manager: String? = nil) -> String {
        let owner = manager.map { ",\"manager\":\"\($0)\"" } ?? ""
        return #"{"id":"\#(id)","source":"examplechat","kind":"group","account":"examplechat","account_name":"Example Chat","title":"\#(title)","preview":"","count":1\#(owner)}"#
    }

    @Test func decodesTheTreeAndAnItemsParent() throws {
        let json = """
        {"node":{"kind":"inbox","id":"b"},"item":\(item("b", "示例 B").dropLast()),"parent":{"kind":"inbox","id":"a","by":"continues"}},
         "ancestors":[{"kind":"inbox","id":"a","by":"continues","item":\(item("a", "示例 A"))},{"kind":"todo","id":"t1","by":"user"}],
         "children":[\(item("c", "示例 C"))]}
        """
        let tree = try JSON.decoder.decode(InboxTree.self, from: Data(json.utf8))
        #expect(tree.item?.parent == InboxParent(kind: "inbox", id: "a", by: "continues"))
        #expect(tree.ancestors.map(\.parent.id) == ["a", "t1"])
        #expect(tree.ancestors[0].item?.title == "示例 A" && tree.ancestors[1].item == nil)
        #expect(tree.children.map(\.id) == ["c"])

        let rows = InboxLine.rows(tree, todoTitle: { $0 == "t1" ? "示例待办" : nil })
        #expect(rows.map { [$0.label, $0.title] } == [["待办", "示例待办"], ["事件", "示例 A"], ["下一步", "示例 C"]])
        #expect(rows.map(\.up) == [true, true, false])
        #expect(rows[0].target == .todo("t1"))
        // The displayed farthest row detaches its lower item, not the item named by that row.
        #expect(rows.map { $0.detach?.id } == ["a", "b", "c"])
        guard case .item(let a)? = rows[1].target, case .item(let c)? = rows[2].target else {
            Issue.record("items open as items")
            return
        }
        #expect(a.id == "a" && c.id == "c")
    }

    @Test func aSessionOpensItsChatWithoutARawIDAndAGoneItemOpensNothing() {
        let tree = InboxTree(node: InboxParent(kind: "inbox", id: "b"), ancestors: [
            .init(parent: InboxParent(kind: "inbox", id: "gone")),
            .init(parent: InboxParent(kind: "session", id: "m1/s1")),
            .init(parent: InboxParent(kind: "session", id: "nosplit")),
        ])
        let rows = InboxLine.rows(tree, sessionTitle: { $0.sessionID == "s1" ? "示例会话" : nil })
        #expect(rows.map(\.title) == ["", "示例会话", "已不在收件箱里"])
        #expect(rows[0].label == InboxLine.label("session"))
        #expect(rows[0].target == nil)
        #expect(rows[1].target == .session(SessionKey(machineID: "m1", sessionID: "s1")) && rows[1].label == "Agent 会话")
        #expect(rows[2].target == nil)
        // A session lower end has no detach write; an inbox lower end still has one when its item is gone.
        #expect(rows.map { $0.detach?.id } == [nil, "gone", "b"])
        #expect(InboxLine.rows(InboxTree(node: InboxParent(kind: "inbox", id: "x"))).isEmpty) // no line: nothing drawn
    }

    @Test func todoAndSessionRowsNeverShowRawIDsOrRepeatTheirKind() {
        let tree = InboxTree(node: InboxParent(kind: "inbox", id: "self"), ancestors: [
            .init(parent: InboxParent(kind: "todo", id: "raw-todo-id")),
            .init(parent: InboxParent(kind: "session", id: "machine/raw-session-id")),
        ])
        let unnamed = InboxLine.rows(tree)
        #expect(unnamed.map(\.title) == ["", ""])
        #expect(unnamed.map(\.label) == [InboxLine.label("session"), InboxLine.label("todo")])
        #expect(unnamed[0].target == .session(SessionKey(machineID: "machine", sessionID: "raw-session-id")))
        #expect(unnamed[1].target == .todo("raw-todo-id"))
        // The session row's lower end is a to-do; the to-do row's lower end is this inbox item.
        #expect(unnamed.map { $0.detach?.id } == [nil, "self"])

        let namedLikeTheirKind = InboxLine.rows(
            tree,
            todoTitle: { _ in InboxLine.label("todo") },
            sessionTitle: { _ in InboxLine.label("session") })
        #expect(namedLikeTheirKind.map(\.title) == ["", ""])
        #expect(namedLikeTheirKind.map(\.label) == unnamed.map(\.label))
    }

    @Test func everyDetachTargetsThePairsLowerInboxItemWithItsManager() throws {
        let json = """
        {"node":{"kind":"inbox","id":"self"},"item":\(item("self", "Current", manager: "root")),
         "ancestors":[
           {"kind":"inbox","id":"near","by":"user","item":\(item("near", "Near", manager: "near-manager"))},
           {"kind":"inbox","id":"middle","by":"user","item":\(item("middle", "Middle"))},
           {"kind":"todo","id":"todo-top","by":"user"}
         ],
         "children":[\(item("child", "Child", manager: "child-manager"))]}
        """
        let tree = try JSON.decoder.decode(InboxTree.self, from: Data(json.utf8))
        let rows = InboxLine.rows(tree, todoTitle: { _ in "Top to-do" })

        // Display order is farthest to nearest, then the child. Pairing still uses the original nearest-first tree.
        #expect(rows.map(\.id) == ["up:todo:todo-top", "up:inbox:middle", "up:inbox:near", "down:child"])
        #expect(rows.map { $0.detach?.id } == ["middle", "near", "self", "child"])
        #expect(rows.map { $0.detach?.manager } == ["root", "near-manager", "root", "child-manager"])
    }

    @Test func anUnstampedTreeKeepsTheOpenedItemsManagerForEveryAction() throws {
        // The manager endpoint returns items without the merged list's manager stamps.
        let json = """
        {"node":{"kind":"inbox","id":"self"},"item":\(item("self", "Current")),
         "ancestors":[{"kind":"inbox","id":"near","item":\(item("near", "Near"))},{"kind":"todo","id":"top"}],
         "children":[\(item("child", "Child"))]}
        """
        let raw = try JSON.decoder.decode(InboxTree.self, from: Data(json.utf8))
        #expect(raw.item?.manager == nil && raw.ancestors[0].item?.manager == nil && raw.children[0].manager == nil)

        let stamped = InboxLine.stamping(raw, manager: "manager-a")
        #expect(stamped.item?.manager == "manager-a")
        #expect(stamped.ancestors.compactMap { $0.item?.manager } == ["manager-a"])
        #expect(stamped.children.map(\.manager) == ["manager-a"])
        let rows = InboxLine.rows(stamped)
        #expect(rows.map { $0.detach?.id } == ["near", "self", "child"])
        #expect(rows.map { $0.detach?.manager } == ["manager-a", "manager-a", "manager-a"])
    }

    @Test func asksTheTreeRouteWithIdsKeptReadable() {
        #expect(APIClient.inboxTreePath("inbox", "lark:F1:900") == "/api/v1/inbox/tree/inbox/lark:F1:900")
        #expect(APIClient.inboxTreePath("session", "m 1/s1") == "/api/v1/inbox/tree/session/m%201/s1")
        #expect(APIClient.inboxTreePath("inbox", "gh:o/r#1") == "/api/v1/inbox/tree/inbox/gh:o%2Fr%231")
    }
}
