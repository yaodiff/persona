import Foundation
import Testing
@testable import Persona
import PersonaWire

/// What a test's outbox did: what it sent, what it kept on disk, what it said, and how the next send goes. Its `send`
/// answers as the store does: told as the hub answers (`answered`), with the reads begun so far (`read`).
@MainActor private final class OutboxProbe {
    var hub: String? = "g"
    var sent: [TodoOutbox.Entry] = []
    var saved: [TodoOutbox.Entry] = []
    var refusals: [String] = []
    var answer: TodoOutbox.Sent = .done(created: nil)
    /// A create's new id, told as the hub answers.
    var created: String?
    /// The reads of the list begun so far, as `TodoStore.readsBegun`.
    var read = 0
    /// How the to-dos stand on the hub, as the list has them.
    var standing: [String: TodoOutbox.Standing] = [:]
    var listed: [TodoItem] = []
    /// Holds the next sends until let go (`release`), as a request the app is ended during.
    var holding = false
    private var held: [CheckedContinuation<Void, Never>] = []

    func release() {
        holding = false
        let waiting = held
        held = []
        for c in waiting { c.resume() }
    }

    func outbox(loading: [TodoOutbox.Entry] = []) -> TodoOutbox {
        TodoOutbox(deps: .init(load: { loading },
                               save: { [weak self] entries, _ in self?.saved = entries; return Task {} },
                               hub: { [weak self] in self?.hub },
                               send: { [weak self] entry, answered in
                                   guard let self else { return .notSent }
                                   sent.append(entry)
                                   if holding { await withCheckedContinuation { held.append($0) } }
                                   if case .done(let created) = answer { answered(.init(created: created ?? self.created, read: read)) }
                                   return answer
                               },
                               refused: { [weak self] _, message, _ in self?.refusals.append(message) },
                               standing: { [weak self] id in self?.standing[id] },
                               listed: { [weak self] in self?.listed ?? [] }))
    }

    /// An outbox with what was kept already in.
    func ready(loading: [TodoOutbox.Entry] = []) async -> TodoOutbox {
        let outbox = outbox(loading: loading)
        await outbox.ready()
        return outbox
    }
}

/// The To-dos list's held writes (#10465, `TodoOutbox`): a write waits out its Undo window and then goes, Undo within
/// it sends nothing, leaving the foreground sends everything at once, a write kept on disk goes again once the hub
/// answers a read with its op id, a new to-do is never sent twice on its own, and what the hub turns down is dropped
/// and said.
@MainActor
struct TodoOutboxTests {
    private let window: Duration = .milliseconds(200)

    @Test func aHeldWriteGoesWhenItsWindowEnds() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        let entry = try #require(outbox.add(.done(true), todo: "t1", manager: nil, hold: window))
        #expect(probe.sent.isEmpty, "nothing goes before the window ends")
        #expect(probe.saved.map(\.id) == [entry.id], "kept on disk from the tap")
        try await waitForOutbox { !probe.sent.isEmpty }
        #expect(probe.sent.map(\.id) == [entry.id] && probe.sent.first?.write == .done(true))
        try await waitForOutbox { outbox.entries.first?.answered == true }
        #expect(probe.saved.first?.answered == true, "on disk as answered: a launch never sends it again")
        outbox.hubAnswered(read: 1)
        #expect(outbox.entries.isEmpty && probe.saved.isEmpty, "a read begun after its answer shows it")
    }

    @Test func undoWithinTheWindowSendsNothing() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        let entry = try #require(outbox.add(.delete, todo: "t1", manager: nil, hold: window))
        #expect(outbox.pending["t1"] == .gone, "the row goes at once")
        #expect(outbox.holds(entry.id))
        #expect(outbox.takeBack(entry.id))
        #expect(outbox.entries.isEmpty && outbox.pending.isEmpty)
        try await Task.sleep(for: .milliseconds(400)) // past the window: a proof that nothing went needs it to have passed
        #expect(probe.sent.isEmpty)
    }

    /// Someday, then Remove from Someday inside its window: the second takes the first back, and nothing goes.
    @Test func somedayTakenBackWithinItsWindowSendsNothing() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        try #require(outbox.add(.someday(true), todo: "t1", manager: nil, hold: window) != nil)
        #expect(outbox.pending["t1"] == .someday)
        #expect(outbox.add(.someday(false), todo: "t1", manager: nil) == nil, "taken back, not a write of its own")
        #expect(outbox.entries.isEmpty && outbox.pending.isEmpty)
        try await Task.sleep(for: .milliseconds(400)) // past the window: nothing may have gone
        #expect(probe.sent.isEmpty)
    }

    /// The scene leaves the foreground: every write goes now, its window or not.
    @Test func leavingTheForegroundSendsEveryWriteNow() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        outbox.add(.someday(true), todo: "t1", manager: nil, hold: .seconds(30))
        outbox.add(.done(true), todo: "t2", manager: nil, hold: .seconds(30))
        outbox.sendAllNow()
        try await waitForOutbox { probe.sent.count == 2 }
        #expect(Set(probe.sent.map(\.todo)) == ["t1", "t2"])
    }

    /// One that did not reach the hub waits, kept on disk; the next launch sends it again, with the same op id, once the
    /// hub answers a read.
    @Test func aWriteKeptOnDiskGoesAgainOnceTheHubAnswersWithItsOpID() async throws {
        let first = OutboxProbe()
        first.answer = .notSent
        let before = await first.ready()
        let entry = try #require(before.add(.done(true), todo: "t1", manager: "work", hold: .zero))
        try await waitForOutbox { before.entries.first?.waits == true }
        #expect(first.saved.first?.waits == true && first.saved.first?.sent == false, "nothing reached the hub")
        // The app ends; the next launch reads what was kept.
        let next = OutboxProbe()
        next.standing = ["t1": .init(title: "Renew passport", updatedAt: nil, done: false, someday: false)]
        let after = await next.ready(loading: first.saved)
        #expect(after.writes["t1"]?.id == entry.id, "the row is still drawn ticked")
        try await Task.sleep(for: .milliseconds(100)) // a timer that sent it would have by now
        #expect(next.sent.isEmpty, "a write that waits for the hub is not sent on the timer")
        after.hubAnswered(read: 1)
        try await waitForOutbox { !next.sent.isEmpty }
        #expect(next.sent.map(\.id) == [entry.id] && next.sent.first?.manager == "work")
    }

    /// A write that waited on a to-do that changed since the tap is let go, and said unless the to-do is as it asks.
    @Test func aWriteThatWaitedOnAToDoThatChangedIsLetGo() async throws {
        let probe = OutboxProbe()
        let tapped = Date(timeIntervalSince1970: 1_800_000_000)
        probe.standing = ["t1": .init(title: "Renew passport", updatedAt: tapped, done: false, someday: false),
                          "t2": .init(title: "Buy stamps", updatedAt: tapped, done: false, someday: false)]
        probe.answer = .notSent
        let outbox = await probe.ready()
        outbox.add(.done(true), todo: "t1", manager: nil)
        outbox.add(.done(true), todo: "t2", manager: nil)
        try await waitForOutbox { outbox.entries.allSatisfy(\.waits) && probe.sent.count == 2 }
        // Meanwhile on the hub: t1 renamed and still open, t2 closed elsewhere.
        probe.standing["t1"]?.updatedAt = tapped.addingTimeInterval(60)
        probe.standing["t2"] = .init(title: "Buy stamps", updatedAt: tapped.addingTimeInterval(60), done: true, someday: false)
        probe.answer = .done(created: nil)
        outbox.hubAnswered(read: 1)
        #expect(outbox.entries.isEmpty && probe.sent.count == 2, "neither goes again")
        #expect(probe.refusals.count == 1 && probe.refusals.first?.contains("Renew passport") == true,
                "only the one whose change is lost is said: \(probe.refusals)")
    }

    @Test func whatTheHubTurnsDownIsDroppedAndSaid() async throws {
        let probe = OutboxProbe()
        probe.answer = .refused("This to-do is closed")
        let outbox = await probe.ready()
        outbox.add(.someday(true), todo: "t1", manager: nil)
        try await waitForOutbox { !probe.refusals.isEmpty }
        #expect(outbox.entries.isEmpty && probe.refusals == ["This to-do is closed"])
    }

    /// A write the hub keeps failing with nobody knowing whether it took it is sent again only so often, then said.
    @Test func aWriteTheHubKeepsFailingIsLetGoAndSaid() async throws {
        let probe = OutboxProbe()
        probe.standing = ["t1": .init(title: "Renew passport", updatedAt: nil, done: false, someday: false)]
        probe.answer = .unknown
        let outbox = await probe.ready()
        outbox.add(.someday(true), todo: "t1", manager: nil)
        for n in 1..<TodoOutbox.tries {
            try await waitForOutbox { probe.sent.count == n && outbox.entries.first.map { $0.unknown == n } == true }
            outbox.hubAnswered(read: n)
        }
        try await waitForOutbox { outbox.entries.isEmpty }
        #expect(probe.sent.count == TodoOutbox.tries && probe.refusals.count == 1)
    }

    /// Undo after the hub has it takes it back with its reverse, on the hub it was made on; a delete the hub has cannot
    /// be.
    @Test func undoAfterTheHubHasItSendsTheReverse() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        let tick = try #require(outbox.add(.done(true), todo: "t1", manager: nil))
        try await waitForOutbox { outbox.entries.first?.answered == true }
        outbox.hubAnswered(read: 1)
        probe.hub = "other" // another hub's pages came up meanwhile
        #expect(outbox.takeBack(tick.id))
        #expect(outbox.entries.last?.write == .done(false) && outbox.entries.last?.hub == "g", "for the hub it was made on")
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(probe.sent.count == 1, "not through the other hub's client")
        probe.hub = "g"
        probe.standing = ["t1": .init(title: "Renew passport", updatedAt: nil, done: true, someday: false)]
        outbox.hubAnswered(read: 2)
        try await waitForOutbox { probe.sent.count == 2 }
        #expect(probe.sent.last?.write == .done(false) && probe.sent.last?.todo == "t1")
        let delete = try #require(outbox.add(.delete, todo: "t2", manager: nil))
        try await waitForOutbox { probe.sent.count == 3 && !outbox.entries.contains { $0.id == delete.id && !$0.answered } }
        #expect(!outbox.takeBack(delete.id))
    }

    /// Undo on a write nobody knows the hub took sends its reverse: it may have landed.
    @Test func undoAfterAnUnknownAnswerSendsTheReverse() async throws {
        let probe = OutboxProbe()
        probe.answer = .unknown
        let outbox = await probe.ready()
        let tick = try #require(outbox.add(.done(true), todo: "t1", manager: nil))
        try await waitForOutbox { outbox.entries.first?.waits == true }
        probe.answer = .done(created: nil)
        #expect(outbox.takeBack(tick.id))
        try await waitForOutbox { probe.sent.count == 2 }
        #expect(probe.sent.last?.write == .done(false))
    }

    /// Undo while it is being sent draws it taken back at once; its reverse goes once the hub has it.
    @Test func undoWhileItIsSentIsDrawnAtOnce() async throws {
        let probe = OutboxProbe()
        probe.holding = true
        let outbox = await probe.ready()
        let tick = try #require(outbox.add(.done(true), todo: "t1", manager: nil))
        try await waitForOutbox { probe.sent.count == 1 }
        #expect(outbox.takeBack(tick.id))
        #expect(outbox.writes["t1"] == nil, "drawn as taken back before the hub answers")
        probe.release()
        try await waitForOutbox { probe.sent.count == 2 }
        #expect(probe.sent.last?.write == .done(false))
    }

    /// A write stays drawn until a read of the list begun after its answer has landed, not just the next one to land.
    @Test func aWriteStaysDrawnUntilAReadAfterItsAnswerLands() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        probe.read = 3 // three reads begun; the third still out as the hub answers
        outbox.add(.done(true), todo: "t1", manager: nil)
        outbox.add(.done(true), todo: "t2", manager: nil)
        try await waitForOutbox { outbox.entries.count == 2 && outbox.entries.allSatisfy(\.answered) }
        outbox.hubAnswered(read: 3)
        #expect(outbox.writes.keys.sorted() == ["t1", "t2"], "a read begun before the answers lands: still drawn")
        outbox.hubAnswered(read: 4)
        #expect(outbox.entries.isEmpty)
    }

    /// A new to-do is listed until the hub names it; the hub's row takes its place in the same step, and Undo deletes
    /// the to-do the hub made.
    @Test func aNewToDoIsListedUntilTheHubNamesIt() async throws {
        let probe = OutboxProbe()
        probe.created = "t9"
        let outbox = await probe.ready()
        let id = TodoOutbox.newID()
        // Not sent yet: no hub for a moment.
        probe.hub = nil
        #expect(outbox.add(.create(title: "Renew passport", notes: nil), todo: id, manager: nil) == nil, "no hub, no write")
        probe.hub = "g"
        let entry = try #require(outbox.add(.create(title: "Renew passport", notes: nil), todo: id, manager: nil))
        guard case .added(let item)? = outbox.pending[id] else { Issue.record("not listed"); return }
        #expect(item.title == "Renew passport")
        try await waitForOutbox { outbox.entries.first?.created == "t9" }
        #expect(outbox.pending.isEmpty && outbox.writes.isEmpty && outbox.named == ["t9"])
        outbox.hubAnswered(read: 1)
        #expect(outbox.entries.isEmpty)
        #expect(outbox.takeBack(entry.id))
        try await waitForOutbox { probe.sent.count == 2 }
        #expect(probe.sent.last?.write == .delete && probe.sent.last?.todo == "t9")
    }

    /// A new to-do whose answer never came is never sent again on its own: it says it is not sure, and the read that
    /// lists it settles it. Taken back meanwhile, the to-do the hub made is deleted once a read lists it.
    @Test func aNewToDoWhoseAnswerNeverCameIsNotSentAgain() async throws {
        let probe = OutboxProbe()
        probe.answer = .unknown
        let outbox = await probe.ready()
        let made = Date()
        let id = TodoOutbox.newID(), other = TodoOutbox.newID()
        outbox.add(.create(title: "Buy stamps", notes: nil), todo: id, manager: nil)
        let undone = try #require(outbox.add(.create(title: "Call the bank", notes: "about the card"), todo: other, manager: nil))
        try await waitForOutbox { outbox.entries.count == 2 && outbox.entries.allSatisfy(\.unsure) }
        #expect(outbox.writes[id]?.unsure == true, "drawn: Not sure it was saved")
        #expect(outbox.takeBack(undone.id) && outbox.pending[other] == nil, "taken back: off the list at once")
        probe.answer = .done(created: nil)
        outbox.hubAnswered(read: 1)
        #expect(probe.sent.count == 2, "a read that does not list them sends nothing")
        probe.listed = [TodoItem(id: "t9", title: "Buy stamps", createdAt: made, updatedAt: made),
                        TodoItem(id: "t10", title: "Call the bank", notes: "about the card", createdAt: made, updatedAt: made)]
        outbox.hubAnswered(read: 2)
        try await waitForOutbox { probe.sent.count == 3 }
        #expect(probe.sent.last?.write == .delete && probe.sent.last?.todo == "t10", "the one taken back is deleted")
        #expect(!outbox.entries.contains { $0.unsure })
        #expect(probe.sent.filter { !$0.write.edits }.count == 2, "each new to-do was asked for once")
    }

    /// A new to-do made with the same words long after one nobody knew the hub made is another: never taken for it, so
    /// one taken back never deletes the owner's later to-do.
    @Test func aLaterToDoWithTheSameWordsIsAnother() async throws {
        let probe = OutboxProbe()
        probe.answer = .unknown
        let outbox = await probe.ready()
        let made = Date()
        let entry = try #require(outbox.add(.create(title: "Buy stamps", notes: nil), todo: TodoOutbox.newID(), manager: nil))
        try await waitForOutbox { outbox.entries.first?.unsure == true }
        #expect(outbox.takeBack(entry.id))
        let later = made.addingTimeInterval(TodoOutbox.clockSlack + TodoOutbox.relayWait + 60)
        probe.listed = [TodoItem(id: "t9", title: "Buy stamps", createdAt: later, updatedAt: later)]
        probe.answer = .done(created: nil)
        outbox.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100)) // a delete would have begun by now
        #expect(probe.sent.count == 1 && outbox.entries.count == 1, "the later to-do is left alone")
    }

    /// The app is ended while a new to-do is out, or once the hub answered it: neither is sent again at the next launch.
    @Test func aNewToDoTheAppWasEndedWithIsNotSentAgain() async throws {
        let first = OutboxProbe()
        first.holding = true
        let before = await first.ready()
        let out = TodoOutbox.newID()
        before.add(.create(title: "Buy stamps", notes: nil), todo: out, manager: nil)
        try await waitForOutbox { first.sent.count == 1 && first.saved.first?.sent == true }
        // Ended with it out: the next launch cannot know whether the hub made it.
        let next = OutboxProbe()
        let after = await next.ready(loading: first.saved)
        after.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(next.sent.isEmpty && after.writes[out]?.unsure == true)
        // Ended once the hub answered it, before the list was read again.
        first.created = "t9"
        first.release()
        try await waitForOutbox { first.saved.first?.answered == true }
        let third = OutboxProbe()
        let again = await third.ready(loading: first.saved)
        again.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(third.sent.isEmpty && again.entries.isEmpty)
    }

    /// A new to-do that first reached nothing, sent again once the hub answers, and the app ended while that second send
    /// is out: it waited before, yet the hub may have it now, so the next launch does not send it again.
    @Test func aNewToDoSentAgainAndEndedMidSendIsNotSentAgain() async throws {
        let first = OutboxProbe()
        first.answer = .notSent
        let before = await first.ready()
        let out = TodoOutbox.newID()
        before.add(.create(title: "Buy stamps", notes: nil), todo: out, manager: nil)
        try await waitForOutbox { first.sent.count == 1 && before.entries.first?.waits == true }
        first.holding = true
        first.answer = .done(created: "t9")
        before.hubAnswered(read: 1)
        try await waitForOutbox { first.sent.count == 2 && first.saved.first?.sent == true }
        // Ended with the second send out.
        let next = OutboxProbe()
        let after = await next.ready(loading: first.saved)
        after.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(next.sent.isEmpty, "never sent a third time")
        #expect(after.writes[out]?.unsure == true, "drawn as Not sure it was saved")
        first.release()
    }

    /// A new to-do kept on disk unsent, sent at the next launch, and the app ended again mid-send: the launch after that
    /// does not send it again.
    @Test func aNewToDoFromDiskEndedMidSendIsNotSentAgain() async throws {
        let at = Date()
        let out = TodoOutbox.newID()
        let kept = TodoOutbox.Entry(id: UUID(), hub: "g", todo: out, manager: nil,
                                    write: .create(title: "Buy stamps", notes: nil), made: at, due: at)
        let second = OutboxProbe()
        second.holding = true
        let launch = await second.ready(loading: [kept])
        launch.hubAnswered(read: 1)
        try await waitForOutbox { second.sent.count == 1 && second.saved.first?.sent == true }
        // Ended with it out.
        let third = OutboxProbe()
        let again = await third.ready(loading: second.saved)
        again.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(third.sent.isEmpty && again.writes[out]?.unsure == true)
        second.release()
    }

    /// Only the shown hub's writes go, and are drawn.
    @Test func onlyTheShownHubsWritesGo() async throws {
        let probe = OutboxProbe()
        probe.hub = "other"
        let outbox = await probe.ready()
        outbox.add(.done(true), todo: "t1", manager: nil, hold: .seconds(30))
        probe.hub = "g"
        #expect(outbox.writes.isEmpty)
        outbox.sendAllNow()
        try await Task.sleep(for: .milliseconds(100)) // a send would have begun by now
        #expect(probe.sent.isEmpty && outbox.entries.count == 1)
    }

    /// A hub forgotten, or the app unpaired: its writes never go, and leave the disk.
    @Test func forgettingAHubDropsItsWrites() async throws {
        let probe = OutboxProbe()
        let outbox = await probe.ready()
        outbox.add(.done(true), todo: "t1", manager: nil, hold: .seconds(30))
        probe.hub = "other"
        outbox.add(.someday(true), todo: "t2", manager: nil, hold: .seconds(30))
        outbox.forget(["g"])
        #expect(outbox.entries.map(\.todo) == ["t2"] && probe.saved.map(\.todo) == ["t2"])
        outbox.forget(nil)
        #expect(outbox.entries.isEmpty && probe.saved.isEmpty)
    }

    /// The file keeps the latest snapshot whatever order the writes come in.
    @Test func theFileKeepsTheLatestSnapshot() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: "todo-outbox-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let file = TodoOutboxFile(folder: folder)
        let at = Date(timeIntervalSince1970: 1_800_000_000)
        let a = TodoOutbox.Entry(id: UUID(), hub: "g", todo: "t1", write: .done(true), made: at, due: at)
        let b = TodoOutbox.Entry(id: UUID(), hub: "g", todo: "t2", write: .delete, made: at, due: at)
        await file.save([a, b], number: 2)
        await file.save([a], number: 1) // an older snapshot that came late
        #expect(await file.load() == [a, b])
    }

    /// Which answers drop a write and which keep it, with the bodies the hub sends: a to-do already gone is what a change
    /// to it comes to; nothing reached goes again; a refusal is dropped; anything else may have been done.
    @Test func howASendWent() {
        let gone = APIError(status: 404, message: "todo not found", fields: ["code": "not_found"])
        #expect(TodoOutbox.outcome(gone, of: .delete) == .done(created: nil))
        #expect(TodoOutbox.outcome(gone, of: .done(true)) == .done(created: nil))
        #expect(TodoOutbox.outcome(APIError(status: 404, message: "no", fields: ["code": TodoNotThere.goneCode]), of: .someday(true))
            == .done(created: nil))
        #expect(TodoOutbox.outcome(gone, of: .create(title: "A", notes: nil)) == .refused("todo not found"))
        #expect(TodoOutbox.outcome(APIError(status: 404, message: "no manager", fields: ["code": "no_manager"]), of: .delete)
            == .refused("no manager"))
        #expect(TodoOutbox.outcome(APIError(status: 409, message: "closed"), of: .someday(true)) == .refused("closed"))
        // A new to-do's project is what a 400 to one made in a project turned down; another refusal is not about it.
        let inKyoto = TodoOutbox.Write.create(title: "Book the ryokan", notes: nil, project: "kyoto")
        #expect(TodoOutbox.outcome(APIError(status: 400, message: "no project"), of: inKyoto) == .refused("no project", project: true))
        #expect(TodoOutbox.outcome(APIError(status: 403, message: "read-only"), of: inKyoto) == .refused("read-only"))
        #expect(TodoOutbox.outcome(APIError(status: 400, message: "empty"), of: .create(title: "A", notes: nil)) == .refused("empty"))
        #expect(TodoOutbox.outcome(APIError(status: 0, message: "no hub"), of: .done(true)) == .notSent)
        #expect(TodoOutbox.outcome(APIError(status: 503, message: "away", fields: ["code": "assistant_offline"]),
                                   of: .create(title: "A", notes: nil)) == .notSent)
        #expect(TodoOutbox.outcome(APIError(status: 429, message: "busy"), of: .delete) == .notSent)
        #expect(TodoOutbox.outcome(APIError(status: 502, message: "relay"), of: .create(title: "A", notes: nil)) == .unknown)
        #expect(TodoOutbox.outcome(APIError(status: 502, message: "doubt", fields: ["code": "assistant_in_doubt"]), of: .done(true))
            == .unknown)
        let unknown = APIError(status: 202, message: "?", fields: ["code": OpReceipt.unknownCode])
        #expect(TodoOutbox.outcome(unknown, of: .create(title: "A", notes: nil)) == .unknown)
        #expect(TodoOutbox.outcome(URLError(.notConnectedToInternet), of: .create(title: "A", notes: nil)) == .notSent)
        #expect(TodoOutbox.outcome(URLError(.cannotConnectToHost), of: .done(true)) == .notSent)
        #expect(TodoOutbox.outcome(URLError(.networkConnectionLost), of: .create(title: "A", notes: nil)) == .unknown)
        #expect(TodoOutbox.outcome(URLError(.timedOut), of: .create(title: "A", notes: nil)) == .unknown)
        // Active-work close refusals (422 todo_close_not_confirmed, 409 todo_close_active) and 4xx refusals carrying
        // `delivered: "none"` are definitive refusals, never retried as unsent; 5xx with `delivered: "none"` still retries.
        let unconfirmed = APIError(status: 422, message: "Confirm before closing", fields: ["code": "todo_close_not_confirmed"])
        let unconfirmedNothing = APIError(status: 422, message: "Confirm before closing",
                                          fields: ["code": "todo_close_not_confirmed", "delivered": "none"])
        let closeActive = APIError(status: 409, message: "Active work running",
                                   fields: ["code": "todo_close_active", "delivered": "none"])
        let outsidePending = APIError(status: 409, message: "Decision pending",
                                      fields: ["code": "outside_decision_pending", "delivered": "none"])
        let ask = TodoOutbox.AskAnswer(ask: "todo:decision@1", kind: OwnerAskKind.decision, round: "1", label: "Pass")
        #expect(TodoOutbox.outcome(unconfirmed, of: .done(true)) == .refused("Confirm before closing"))
        #expect(TodoOutbox.outcome(unconfirmedNothing, of: .done(true)) == .refused("Confirm before closing"))
        #expect(TodoOutbox.outcome(unconfirmedNothing, of: .answer(ask)) == .refused("Confirm before closing"))
        #expect(TodoOutbox.outcome(closeActive, of: .done(true)) == .refused("Active work running"))
        #expect(TodoOutbox.outcome(outsidePending, of: .delete) == .refused("Decision pending"))
        #expect(TodoOutbox.outcome(APIError(status: 503, message: "unreachable", fields: ["delivered": "none"]),
                                   of: .done(true)) == .notSent)
    }

    /// A close refused for unconfirmed active work (422 `todo_close_not_confirmed`) sends with `todoCloseConfirmWait`,
    /// drops the entry immediately, surfaces the refusal, and never retries on later list reads.
    @Test func aCloseRefusedForActiveWorkIsDroppedAndNotRetried() async throws {
        let hub = FakeHub()
        hub.on("PATCH", "/api/v1/todos/t1") { _ in
            throw APIError(status: 422, message: "The task stays open until you confirm.",
                           fields: ["code": "todo_close_not_confirmed", "delivered": "none"])
        }
        let store = TodoStore(cachedTodos: [TodoItem(id: "t1", title: "Deploy check")], cacheWriter: nil, hubListChanged: {})
        defer { store.forget() }
        let probe = OutboxProbe()
        let outbox = TodoOutbox(deps: .init(
            load: { [] },
            save: { _, _ in Task {} },
            hub: { "g" },
            send: { entry, answered in await store.send(entry, answered: answered, api: hub.client) },
            refused: { _, message, _ in probe.refusals.append(message) },
            standing: { id in
                store.byID[id].map { .init(title: $0.title, updatedAt: $0.updatedAt, done: $0.isDone, someday: false) }
            },
            listed: { store.todos }
        ))
        await outbox.ready()
        outbox.add(.done(true), todo: "t1", manager: nil, hold: .zero)
        try await waitForOutbox { !probe.refusals.isEmpty && outbox.entries.isEmpty }
        let patch = try #require(hub.sent("PATCH", "/api/v1/todos/t1").first)
        #expect(patch.timeout == APIClient.todoCloseConfirmWait && patch.timeout > 45)
        #expect(probe.refusals == ["The task stays open until you confirm."])
        outbox.hubAnswered(read: 1)
        try await Task.sleep(for: .milliseconds(100))
        #expect(hub.sent("PATCH", "/api/v1/todos/t1").count == 1 && outbox.entries.isEmpty)
    }

    /// A new to-do kept on disk before a write carried the place it was made in (#10465) still loads, made nowhere in
    /// particular; one made in a place keeps its due date and project through the file, and its drawn row has them.
    @Test func aNewToDoKeptBeforePlacesStillLoads() throws {
        let decode = { (json: String) in try JSONDecoder().decode(TodoOutbox.Write.self, from: Data(json.utf8)) }
        #expect(try decode(#"{"create":{"title":"Buy stamps","notes":"at the post office"}}"#)
            == .create(title: "Buy stamps", notes: "at the post office"))
        #expect(try decode(#"{"create":{"title":"Buy stamps"}}"#) == .create(title: "Buy stamps", notes: nil))
        let at = Date(timeIntervalSince1970: 1_795_000_000)
        let placed = TodoOutbox.Write.create(title: "Renew passport", notes: nil, dueAt: at, project: "kyoto")
        let entry = TodoOutbox.Entry(id: UUID(), hub: "g", todo: TodoOutbox.newID(), manager: nil, write: placed, made: at, due: at)
        let kept = try JSONDecoder().decode(TodoOutbox.Entry.self, from: JSONEncoder().encode(entry))
        #expect(kept == entry)
        #expect(kept.newItem?.dueAt == at && kept.newItem?.dueDate == nil && kept.newItem?.project == "kyoto")
        let day = TodoOutbox.Write.create(title: "Renew passport", notes: nil, dueDate: "2026-11-18")
        #expect(TodoOutbox.Entry(id: UUID(), hub: "g", todo: "new-1", manager: nil, write: day, made: at, due: at).newItem?.dueDate
            == "2026-11-18")
    }

    /// A new to-do the hub turned down comes back in its sheet (the toast's Edit) due when it was due, a moment or a
    /// day, and in its project. One whose project the hub turned down comes back without it, and the toast says so, or
    /// every Add would be turned down again.
    @Test func aRefusedNewToDoComesBackDueAsItWasWithoutTheProjectItWasRefusedFor() {
        let at = Date(timeIntervalSince1970: 1_795_090_000)
        let write = TodoOutbox.Write.create(title: "Book the ryokan", notes: "Two nights", dueAt: at, project: "kyoto")
        let inProject = TodoOutbox.refusal(write, message: "no knowledge-base project \"kyoto\"", project: true)
        #expect(inProject.draft == TodoOutbox.Draft(title: "Book the ryokan", notes: "Two nights", dueAt: at))
        #expect(inProject.message.contains("Book the ryokan") && !inProject.message.contains("knowledge-base"), "\(inProject.message)")
        let notAllowed = TodoOutbox.refusal(write, message: "read-only role cannot modify todos")
        #expect(notAllowed.draft == TodoOutbox.Draft(title: "Book the ryokan", notes: "Two nights", dueAt: at, project: "kyoto"))
        #expect(notAllowed.message == "read-only role cannot modify todos")
        let dated = TodoOutbox.refusal(.create(title: "Renew passport", notes: nil, dueDate: "2026-11-20"), message: "refused")
        #expect(dated.draft == TodoOutbox.Draft(title: "Renew passport", dueDate: "2026-11-20") && dated.message == "refused")
        let tick = TodoOutbox.refusal(.done(true), message: "This to-do is closed")
        #expect(tick.draft == nil && tick.message == "This to-do is closed")
    }
}

/// Waits (up to 5 s) for something the outbox does.
@MainActor
private func waitForOutbox(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<250 where !condition() { try await Task.sleep(for: .milliseconds(20)) }
    try #require(condition(), "it did not happen in time")
}
