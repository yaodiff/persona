import Foundation
import Testing
@testable import Persona

/// Which pushes the app takes back from the notification center once their interaction is settled (#3513).
struct NotificationWithdrawalTests {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)

    private func delivered(_ identifier: String, _ id: String, group: String = "", at seconds: TimeInterval = 0,
                           occurrence: String? = nil) -> NotificationWithdrawal.Delivered {
        .init(identifier: identifier, group: group, interactionID: id, date: t0.addingTimeInterval(seconds),
              interactionCreatedAt: occurrence)
    }

    private func interaction(_ id: String, occurrence: String) throws -> Interaction {
        try JSONDecoder().decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"m","session_id":"s","kind":"permission","created_at":"\#(occurrence)"}"#.utf8))
    }

    private func deliveredTodo(_ identifier: String, _ todoID: String, group: String = "", at seconds: TimeInterval = 0)
        -> NotificationWithdrawal.Delivered {
        .init(identifier: identifier, group: group, todoID: todoID, date: t0.addingTimeInterval(seconds))
    }

    @Test func anAnsweredInteractionTakesItsPushAlong() {
        // interaction_resolved (answered on the computer) or answered in the app: ix1 left the list.
        let out = NotificationWithdrawal.settled(gone: ["ix1"], pending: ["ix2"],
                                                 delivered: [delivered("n1", "ix1"), delivered("n2", "ix2"), delivered("n3", "ix3")])
        #expect(out == ["n1"])
    }

    @Test func anInteractionBackInTheListKeepsItsPush() {
        // The answer did not go through and the card came back before the notification center answered.
        #expect(NotificationWithdrawal.settled(gone: ["ix1"], pending: ["ix1"], delivered: [delivered("n1", "ix1")]).isEmpty)
    }

    @Test func theSameIDInAnotherGroupIsAnotherInteraction() {
        let pushes = [delivered("home", "ix1"), delivered("work", "ix1", group: "g2")]
        #expect(NotificationWithdrawal.settled(gone: ["g2|ix1"], pending: ["ix1"], delivered: pushes) == ["work"])
        #expect(NotificationWithdrawal.settled(gone: ["ix1"], pending: ["g2|ix1"], delivered: pushes) == ["home"])
        // A group forgotten: all of its interactions left at once.
        #expect(NotificationWithdrawal.settled(gone: ["g2|ix1", "g2|ix9"], pending: ["ix1"], delivered: pushes) == ["work"])
    }

    @Test func modelRemovalLeavesAReplacementAlertBeforeItsModelEvent() throws {
        let firstAt = "2026-01-02T03:04:05.000000001Z"
        let secondAt = "2026-01-02T03:04:05.000000002Z"
        let first = try interaction("ix1", occurrence: firstAt)
        let second = try interaction("ix1", occurrence: secondAt)
        let pushes = [delivered("old", "ix1", occurrence: firstAt), delivered("new", "ix1", occurrence: secondAt)]
        let gone = [first.uid: first]
        #expect(NotificationWithdrawal.settledOccurrences(gone: gone, pending: [:], delivered: pushes) == ["old"])
        #expect(NotificationWithdrawal.settledOccurrences(gone: gone, pending: [second.uid: second], delivered: pushes) == ["old"])
        #expect(NotificationWithdrawal.settledOccurrences(gone: gone, pending: [first.uid: first], delivered: [pushes[0]]).isEmpty)
    }

    @Test func aGroupsWholeListTakesBackWhatItNoLongerHas() {
        // Reconnected: the list says ix2 is still pending; ix1 was answered while the socket was down.
        let out = NotificationWithdrawal.stale(listedAt: ["": t0.addingTimeInterval(60)], pending: ["ix2"],
                                               delivered: [delivered("n1", "ix1"), delivered("n2", "ix2")])
        #expect(out == ["n1"])
    }

    @Test func aPushNewerThanTheListStays() {
        // Delivered after the list was asked for: its interaction can be ahead of its event.
        let out = NotificationWithdrawal.stale(listedAt: ["": t0], pending: [], delivered: [delivered("n1", "ix1", at: 5)])
        #expect(out.isEmpty)
    }

    @Test func onlyGroupsWithAWholeListAreChecked() {
        // g2 has not answered yet: its list is the cache of the last run, which says nothing about its pushes.
        let pushes = [delivered("home", "ix1"), delivered("work", "ix7", group: "g2")]
        let out = NotificationWithdrawal.stale(listedAt: ["": t0.addingTimeInterval(60)], pending: [], delivered: pushes)
        #expect(out == ["home"])
        let both = NotificationWithdrawal.stale(listedAt: ["": t0.addingTimeInterval(60), "g2": t0.addingTimeInterval(60)],
                                                pending: ["g2|ix7"], delivered: pushes)
        #expect(both == ["home"])
    }

    @Test func uidMatchesTheInteractions() {
        #expect(delivered("n", "ix1").uid == "ix1")
        #expect(delivered("n", "ix1", group: "g2").uid == "g2|ix1")
    }

    @Test func aWithdrawalPushTakesBackWhatItNames() {
        // The hub says ix1 and ix3 are over (#4349); the suspended app still lists ix1 as pending.
        let pushes = [delivered("n1", "ix1"), delivered("n2", "ix2"), delivered("n3", "ix3", group: "g2")]
        #expect(NotificationWithdrawal.resolved(["ix1", "ix3"], group: "", delivered: pushes) == ["n1"])
        #expect(NotificationWithdrawal.resolved(["ix1", "ix3"], group: "g2", delivered: pushes) == ["n3"])
        // A push that did not say whose: the ids in any group.
        #expect(NotificationWithdrawal.resolved(["ix1", "ix3"], group: nil, delivered: pushes) == ["n1", "n3"])
        #expect(NotificationWithdrawal.resolved([], group: nil, delivered: pushes).isEmpty)
    }

    @Test func aDelayedWithdrawalDoesNotRemoveItsSameIDReplacement() {
        let first = "2026-01-02T03:04:05.000000001Z"
        let second = "2026-01-02T03:04:05.000000002Z"
        let pushes = [delivered("old", "ix1", occurrence: first), delivered("new", "ix1", occurrence: second)]
        let over = [NotificationWithdrawal.Resolved(interactionID: "ix1", interactionCreatedAt: first)]
        #expect(NotificationWithdrawal.resolvedOccurrences(over, group: "", delivered: pushes) == ["old"])
        // A pre-occurrence alert can still be collected after an upgrade; a known replacement cannot.
        let legacy = delivered("legacy", "ix1")
        #expect(NotificationWithdrawal.resolvedOccurrences(over, group: "", delivered: [legacy]) == ["legacy"])
    }

    @Test func anAssistantCardsPushWaitsForItsWithdrawal() {
        // A decision's push (card_id): never in the list of pending interactions, so neither a list without it nor a
        // settled interaction takes it back; the hub's withdrawal push naming it does.
        var card = delivered("n1", "todo-ask:t1")
        card.ownerCard = true
        let pushes = [card, delivered("n2", "ix2")]
        #expect(NotificationWithdrawal.stale(listedAt: ["": t0.addingTimeInterval(60)], pending: [], delivered: pushes) == ["n2"])
        #expect(NotificationWithdrawal.settled(gone: ["todo-ask:t1"], pending: [], delivered: pushes).isEmpty)
        #expect(NotificationWithdrawal.resolved(["todo-ask:t1"], group: "", delivered: pushes) == ["n1"])
    }

    @Test func unansweredTodoDecisionsAreNeverRemovedByStaleOrSettled() {
        // To-do decisions and needs-user pushes never appear in the interactions list (#13916), so a full card-list
        // fetch (`stale`) or interaction removal (`settled`) must never sweep them.
        let pushes = [
            delivered("ix-note", "ix1"),
            deliveredTodo("decide-note", "todo_1"),
            deliveredTodo("needs-user-note", "todo_2", group: "g2"),
        ]
        #expect(deliveredTodo("decide-note", "todo_1").uid == nil)
        #expect(NotificationWithdrawal.stale(
            listedAt: ["": t0.addingTimeInterval(60), "g2": t0.addingTimeInterval(60)],
            pending: [],
            delivered: pushes
        ) == ["ix-note"])
        #expect(NotificationWithdrawal.settled(
            gone: ["ix1", "todo_1", "todo-ask:todo_1", "g2|todo_2"],
            pending: [],
            delivered: pushes
        ) == ["ix-note"])
    }

    @Test func todoAskResolvedRemovesOnlyMatchingTodoNotifications() {
        let pushes = [
            deliveredTodo("decide-1", "todo_1"),
            deliveredTodo("needs-1", "todo_1"),
            deliveredTodo("decide-2", "todo_2"),
            deliveredTodo("work-1", "todo_1", group: "g2"),
            delivered("card-ix1", "ix1"),
            delivered("card-same-id", "todo_1"),
        ]
        // Default group: removes both decision and needs-user pushes for todo_1, leaves todo_2, g2, and cards alone.
        #expect(NotificationWithdrawal.resolved(["todo-ask:todo_1"], group: "", delivered: pushes) == ["decide-1", "needs-1"])
        // Specific group: removes only that group's push.
        #expect(NotificationWithdrawal.resolved(["todo-ask:todo_1"], group: "g2", delivered: pushes) == ["work-1"])
        // Unscoped withdrawal: removes todo_1 in all groups, never touching todo_2 or interaction cards.
        #expect(NotificationWithdrawal.resolved(["todo-ask:todo_1"], group: nil, delivered: pushes) == ["decide-1", "needs-1", "work-1"])
        // Mixed batch with both interaction ids and todo-ask ids removes both in one pass.
        #expect(NotificationWithdrawal.resolved(["ix1", "todo-ask:todo_1"], group: "", delivered: pushes) == ["decide-1", "needs-1", "card-ix1"])
        // A bare interaction id matching a to-do id removes only the interaction card, not the to-do pushes.
        #expect(NotificationWithdrawal.resolved(["todo_1"], group: nil, delivered: pushes) == ["card-same-id"])
        // Empty or whitespace-only todo-ask ids are ignored safely.
        #expect(NotificationWithdrawal.resolved(["todo-ask:", "todo-ask:   "], group: nil, delivered: pushes).isEmpty)
        #expect(NotificationWithdrawal.todoID(fromResolved: "todo-ask:todo_1") == "todo_1")
        #expect(NotificationWithdrawal.todoID(fromResolved: "todo-ask:  ") == nil)
        #expect(NotificationWithdrawal.todoID(fromResolved: "ix1") == nil)
    }

    @Test func rawParsesInteractionTodoIDAndLegacyDecisionPayloads() {
        let ixRaw = NotificationWithdrawal.raw(
            identifier: "n-ix",
            date: t0,
            userInfo: ["interaction_id": "ix_1", "machine_id": "m1", "session_id": "s1"]
        )
        #expect(ixRaw?.target == .interaction("ix_1"))
        #expect(ixRaw?.ownerCard == false)
        #expect(ixRaw?.ref == PushRef(["machine_id": "m1", "session_id": "s1"]))

        // Assistant owner cards (plan waiting for Start, failed start) carry card_id + interaction_id (#14388).
        let planRaw = NotificationWithdrawal.raw(
            identifier: "n-plan",
            date: t0,
            userInfo: ["card_id": "plan_x", "interaction_id": "plan_x", "machine_id": "m1", "session_id": "s1"]
        )
        #expect(planRaw?.target == .interaction("plan_x"))
        #expect(planRaw?.ownerCard == true)

        let failedRaw = NotificationWithdrawal.raw(
            identifier: "n-fail",
            date: t0,
            userInfo: ["card_id": "task-failed:todo_9", "interaction_id": "task-failed:todo_9",
                       "machine_id": "m1", "session_id": "s1"]
        )
        #expect(failedRaw?.target == .interaction("task-failed:todo_9"))
        #expect(failedRaw?.ownerCard == true)

        // Decision / needs-user push carrying card_id: todo-ask:<id> (with or without todo_id).
        let cardTodoRaw = NotificationWithdrawal.raw(
            identifier: "n-card-todo",
            date: t0,
            userInfo: ["card_id": "todo-ask:todo_card", "machine_id": "m1", "session_id": "s1"]
        )
        #expect(cardTodoRaw?.target == .todo("todo_card"))
        #expect(cardTodoRaw?.ownerCard == true)

        // Needs-user push: carries todo_id without interaction_id.
        let needsUserRaw = NotificationWithdrawal.raw(
            identifier: "n-needs",
            date: t0,
            userInfo: ["todo_id": "todo_1", "machine_id": "m1", "session_id": "s1", "hub_id": "hub-a"]
        )
        #expect(needsUserRaw?.target == .todo("todo_1"))
        #expect(needsUserRaw?.ownerCard == true)
        #expect(needsUserRaw?.hubID == "hub-a")

        // Decision push with card_id, top-level todo_id, and nested todo.id (no interaction_id).
        let decideRaw = NotificationWithdrawal.raw(
            identifier: "n-decide",
            date: t0,
            userInfo: ["card_id": "todo-ask:todo_2", "todo_id": "todo_2",
                       "todo": ["id": "todo_2", "asked_at": "2026-11-23T00:00:00Z"],
                       "machine_id": "m1", "session_id": "s1"]
        )
        #expect(decideRaw?.target == .todo("todo_2"))
        #expect(decideRaw?.ownerCard == true)

        // Older hub decision push without top-level todo_id falls back to todo.id.
        let legacyDecideRaw = NotificationWithdrawal.raw(
            identifier: "n-legacy",
            date: t0,
            userInfo: ["todo": ["id": "todo_legacy"], "machine_id": "m1", "session_id": "s1"]
        )
        #expect(legacyDecideRaw?.target == .todo("todo_legacy"))
        #expect(legacyDecideRaw?.ownerCard == true)

        // Standalone to-do push without machine_id/session_id still resolves via hub_id / sealed group.
        let standaloneRaw = NotificationWithdrawal.raw(
            identifier: "n-standalone",
            date: t0,
            userInfo: ["todo_id": "todo_3", "hub_id": "hub-b", PushCrypto.groupKey: "g2"]
        )
        #expect(standaloneRaw?.target == .todo("todo_3"))
        #expect(standaloneRaw?.ownerCard == true)
        #expect(standaloneRaw?.ref == nil)
        #expect(standaloneRaw?.hubID == "hub-b")
        #expect(standaloneRaw?.sealedGroup == "g2")

        // Plain notifications and blank todo_id payloads are ignored.
        #expect(NotificationWithdrawal.raw(identifier: "n-plain", date: t0, userInfo: ["machine_id": "m1", "session_id": "s1"]) == nil)
        #expect(NotificationWithdrawal.raw(identifier: "n-empty", date: t0, userInfo: ["todo_id": "   ", "machine_id": "m1", "session_id": "s1"]) == nil)
    }

    @Test @MainActor func appModelHandlesTodoAskInteractionResolvedWithoutPollutingInteractionState() {
        let model = AppModel()
        #expect(model.withdrawTodoAsk("todo-ask:todo_1", group: "") == true)
        #expect(model.withdrawTodoAsk("ix_1", group: "") == false)
        model.handle(.interactionResolved("todo-ask:todo_1", assistant: nil), group: "")
        #expect(model.interactions.isEmpty)
        #expect(model.delegatedAnswers.isEmpty)
    }
}
