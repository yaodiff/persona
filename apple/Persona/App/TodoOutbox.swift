import Foundation
import Observation
import PersonaWire
#if os(iOS)
import UIKit
#endif

/// The To-dos list's writes that wait on this device (#10465; docs/apple/ios.md "To-dos (V1, #10465)"): a tick, Someday
/// and a delete wait out their Undo window (`Theme.Motion.todoUndo`), so Undo sends nothing and a to-do deleted by
/// mistake is never gone from the hub; a reopen, a "Remove from Someday" and a new to-do go at once, and one that takes
/// back a write still held here takes that one back instead, so nothing goes. Each write keeps its op id
/// (`X-Persona-Op-Id`) from the tap on.
///
/// - Kept on disk (`TodoOutboxFile`), written in the order it changed: a write the app was quit or ended with goes once
///   the hub next answers a read of the list, unless its to-do changed meanwhile (`base`).
/// - Sent when its window ends, every one at once when the scene leaves the foreground, and again after the hub answers
///   a read (`hubAnswered`) for one that could not reach it.
/// - What an answer means (`outcome`): the hub has it; nothing reached the hub (sent again); nobody knows (a tick,
///   Someday or a delete is sent again, being the same write twice; a new to-do never is, since the hub makes one for
///   each request it takes: it stays "Not sure it was saved" until a read lists it or the owner tries again); or the hub
///   turned it down (dropped, and said).
/// - A write the hub has stays until a read of the list that began after its answer has landed: until the hub's view
///   says the same, the list draws it (the tick struck through, the row gone or in Someday, the new row) and the numbers
///   count it (`pending`).
@MainActor @Observable
final class TodoOutbox {
    static let shared = TodoOutbox(deps: .live)

    nonisolated enum Write: Codable, Equatable, Sendable {
        /// `PATCH …/todos/{id}` with `done`: the tick, and a reopen.
        case done(Bool)
        /// `PUT` (true) or `DELETE` (false) `…/someday`, on no set day.
        case someday(Bool)
        /// `DELETE …/todos/{id}`.
        case delete
        /// `POST /api/v1/todos`, under the to-do's own id here (`newID`) until the hub answers with its own. Made in a
        /// To-dos place, it starts there in the same write (#10465, proposal 3.3 "Capture default"): due on a day or at
        /// a moment, or in a knowledge-base project. A write kept on disk before these existed reads as none of them.
        case create(title: String, notes: String?, dueAt: Date? = nil, dueDate: String? = nil, project: String? = nil)
        /// An answer to one of the to-do's own asks (proposal 3.7, "Three outcomes"): the asks route while the hub lists
        /// `todo_asks`, else today's accept and retry routes. Nothing undoes a decision on the hub, so it waits out its Undo
        /// window here.
        case answer(AskAnswer)

        /// The write that takes this one back once the hub has it; none for a delete or an answer.
        func reverse() -> Write? {
            switch self {
            case .done(let done): .done(!done)
            case .someday(let on): .someday(!on)
            case .delete, .answer: nil
            case .create: .delete
            }
        }

        /// A change to a to-do the hub has (not a new one).
        var edits: Bool {
            if case .create = self { false } else { true }
        }

        /// A new to-do made in a knowledge-base project.
        var inProject: Bool {
            guard case .create(_, _, _, _, let project?) = self else { return false }
            return !project.isEmpty
        }
    }

    /// One answer to a to-do's own ask, as it is held and sent.
    nonisolated struct AskAnswer: Codable, Equatable, Sendable {
        /// The id the row's view lists (`todo:decision@…`, `todo:retry@…`): the asks route's, and the counts' overlay's.
        var ask: String
        /// `OwnerAskKind`: a decision or a retry.
        var kind: String
        /// The round the accept route takes (the decision's `asked_at`, byte for byte); none for a retry.
        var round: String?
        /// One of a decision's options, by its label; the recommended one takes the recommendation.
        var option: String?
        /// One of the fixed answers (`TodoAsk.Response`).
        var response: String?
        /// What the row says while it is held ("You chose …").
        var label: String
        /// The step whose ask this is, lifted onto its parent's row (`step:<id>/…`): today's routes answer it on the step,
        /// and the device keeps it by the step's round, as the tray does.
        var step: String? = nil

        /// The asks route's body: none for a retry, which is the ask's one action.
        var body: TodoAskAnswer { TodoAskAnswer(option: option, response: response) }
    }

    nonisolated struct Entry: Codable, Equatable, Identifiable, Sendable {
        /// The op id: `X-Persona-Op-Id` on every send of it.
        var id: UUID
        /// The hub-level group it was made on: it goes to that hub, and is drawn only while that hub's pages are shown.
        var hub: String
        /// The to-do's id; a new one's own (`TodoOutbox.newID`) until the hub answers.
        var todo: String
        /// The to-do's assistant (its `manager` stamp; a new one's, the assistant it is made for). nil with one.
        var manager: String?
        var write: Write
        var made: Date
        /// When its Undo window ends: sent then.
        var due: Date
        /// The to-do's `updated_at` as the list had it at the tap: a write sent again goes only if it is still that.
        var base: Date?
        /// It goes again when the hub next answers a read, not on the timer: it could not reach the hub, nobody knows
        /// whether the hub has it, or the app was ended with it.
        var waits = false
        /// How many sends ended with nobody knowing whether the hub took it: after `TodoOutbox.tries` it is let go, and
        /// said.
        var unknown = 0
        /// A send of it began (kept on disk before a new to-do's goes, so a launch never sends one the hub may have).
        var sent = false
        /// The hub has it: never sent again; it stays until a read that began after the answer has landed.
        var answered = false
        /// A new to-do the hub may or may not have made: never sent again unless the owner tries again; a read that lists
        /// it settles it.
        var unsure = false
        /// Taken back while nobody knew whether the hub had it: a new to-do a read later lists is deleted then.
        var undone = false
        /// A new to-do's id on the hub, once it answered: the list has its own row for it then.
        var created: String?

        /// A new to-do as the list draws it until the hub has it: with its due date and project, so the place it was
        /// made in holds it at once (`TodosPlaces.holds`), and its project's place counts it.
        var newItem: TodoItem? {
            guard case .create(let title, let notes, let dueAt, let dueDate, let project) = write, created == nil else { return nil }
            return TodoItem(id: todo, title: title, notes: notes, createdAt: made, updatedAt: made, dueAt: dueAt, dueDate: dueDate,
                            manager: manager, project: project)
        }
    }

    /// How a send went.
    nonisolated enum Sent: Equatable, Sendable {
        /// The hub has it (a new to-do: under this id).
        case done(created: String?)
        /// Nothing reached the hub's assistant (no connection, the assistant away, the request turned away unread): kept,
        /// to go again with the same op id.
        case notSent
        /// It may have reached it: nobody knows whether it was done.
        case unknown
        /// The hub turned it down: dropped, and said. `project`: what it turned down is the project a new to-do was made
        /// in (a 400 to one that carried one: the hub takes the title and date as the app sends them).
        case refused(String, project: Bool = false)
        /// An answer whose ask no longer waits (answered elsewhere, a newer round, the to-do gone): the first answer
        /// anywhere wins, so it is let go without a word here; the row says "Answered elsewhere".
        case elsewhere
        /// An answer nobody can say reached the Agent: the hub said so (502 `send_in_doubt`, `delivered: "unknown"`, which
        /// its receipt says again to a repeat), or `tries` sends ended unanswered. Never sent again, and never said as not
        /// sent, since the words may be with the Agent: the row says it is not sure, and the next read of the view shows
        /// whether the ask still waits.
        case inDoubt
    }

    /// A to-do as the list last read it: what a write sent again is checked against.
    nonisolated struct Standing: Equatable, Sendable {
        var title: String
        var updatedAt: Date?
        var done: Bool
        var someday: Bool
        /// The asks its view lists: an answer to one it no longer lists is moot. nil while the row has no view (a manager
        /// reconnecting): nothing is known of its asks then.
        var asks: Set<String>? = nil

        /// The to-do is already as `write` asks (the write landed before, or someone did the same); for an answer, its
        /// view no longer lists the ask.
        func has(_ write: Write) -> Bool {
            switch write {
            case .done(let d): done == d
            case .someday(let on): someday == on
            case .delete, .create: false
            case .answer(let a): asks.map { !$0.contains(a.ask) } ?? false
            }
        }
    }

    /// A write's answer from the hub, told as it comes, before the list is read again: the new to-do's id, and how many
    /// reads of the list had begun (`TodoStore.readsBegun`), so the write is let go only once a later one has landed.
    nonisolated struct Answer: Equatable, Sendable {
        var created: String?
        var read: Int
    }

    struct Deps {
        /// What was kept on disk, read off the main actor.
        var load: @Sendable () async -> [Entry]
        /// Writes snapshot `number` of every write (a later number always wins); the task ends once it is written.
        var save: @MainActor (_ entries: [Entry], _ number: Int) -> Task<Void, Never>
        /// The hub-level group shown now.
        var hub: @MainActor () -> String?
        /// Sends it; `answered` is told as the hub answers, before the list is read again.
        var send: @MainActor (_ entry: Entry, _ answered: @escaping @MainActor (Answer) -> Void) async -> Sent
        /// A write the hub turned down, or that could not be saved: said where the owner is. `project`: the hub turned
        /// down the project a new to-do was made in (`Sent.refused`).
        var refused: @MainActor (_ entry: Entry, _ message: String, _ project: Bool) -> Void
        /// How an answer's send ended (`.done`, `.elsewhere`, `.refused`), or that it was let go unsent because its ask
        /// no longer waits (`.elsewhere`): the asks' state on this device follows (`DecisionAnswers.settled`).
        var settled: @MainActor (Entry, Sent) -> Void = { _, _ in }
        /// A to-do as the list has it now; nil when the list has none (gone from the hub).
        var standing: @MainActor (String) -> Standing? = { _ in nil }
        /// The shown hub's to-dos, for a new to-do nobody knew the hub made.
        var listed: @MainActor () -> [TodoItem] = { [] }
        var now: @MainActor () -> Date = { Date() }
        /// The app's own: it sends every write as the scene leaves the foreground.
        var followsScene = false

        static var live: Deps {
            Deps(load: { await TodoOutboxFile.shared.load() },
                 save: { entries, number in Task(priority: .utility) { await TodoOutboxFile.shared.save(entries, number: number) } },
                 hub: { AppModel.shared.hub?.group },
                 send: { entry, answered in await TodoStore.shared.send(entry, answered: answered) },
                 refused: { entry, message, project in
                     Haptics.error()
                     // A new to-do the hub turned down: what was typed comes back in its sheet (Edit).
                     let said = TodoOutbox.refusal(entry.write, message: message, project: project)
                     let edit = said.draft.map { draft in
                         Toast.Action(title: String(localized: "Edit")) { TodoOutbox.shared.edit(draft) }
                     }
                     AppModel.shared.toast = Toast(said.message, style: .error, action: edit)
                 },
                 settled: { entry, sent in DecisionAnswers.shared.settled(entry, sent) },
                 standing: { id in
                     let store = TodoStore.shared
                     guard let item = store.byID[id] else { return nil }
                     let view = store.views[id]
                     return Standing(title: item.title, updatedAt: item.updatedAt,
                                     done: view.map { $0.phase == TodoPhase.done } ?? item.isDone,
                                     someday: view?.section == TodoListSection.someday,
                                     asks: view.map { Set($0.asks?.map(\.id) ?? []) })
                 },
                 listed: { TodoStore.shared.todos },
                 followsScene: true)
        }
    }

    /// Every write still here, each hub's, in the order they were made.
    private(set) var entries: [Entry] = []
    /// Taken back while being sent: drawn as taken back at once; once the hub has it, its reverse goes.
    private(set) var takenBack: Set<UUID> = []
    /// What a new to-do the hub turned down said: back in New To-Do's sheet (the toast's Edit), due when it was due (a
    /// day, or a moment) and in its project, unless the project is what the hub turned down (#10465).
    nonisolated struct Draft: Equatable, Sendable {
        var title: String
        var notes: String?
        var dueAt: Date?
        var dueDate: String?
        var project: String?
    }

    /// What the owner is told of a write the hub turned down and, for a new to-do, what its sheet opens with again (the
    /// toast's Edit). One whose project the hub turned down (`project`: gone or renamed meanwhile) opens without it, and
    /// the toast says so, or every Add would be turned down the same way; its project can be set again from its row.
    /// Any other refusal (a role that may not add to-dos, say) keeps the project and says what the hub said.
    nonisolated static func refusal(_ write: Write, message: String, project refused: Bool = false) -> (message: String, draft: Draft?) {
        guard case .create(let title, let notes, let dueAt, let dueDate, let project) = write else { return (message, nil) }
        guard refused, project != nil else {
            return (message, Draft(title: title, notes: notes, dueAt: dueAt, dueDate: dueDate, project: project))
        }
        return (String(localized: "Couldn't put “\(title)” in its project. Edit adds it without one. [To-dos Toast]"),
                Draft(title: title, notes: notes, dueAt: dueAt, dueDate: dueDate))
    }

    /// A new to-do the hub turned down, whose words go back into New To-Do (the toast's Edit).
    private(set) var draft: Draft?
    @ObservationIgnored private let deps: Deps
    /// Being sent now: never sent twice at once.
    @ObservationIgnored private var sending: Set<UUID> = []
    /// The writes the hub took lately, with the new to-do's id for a create: an Undo that comes after they left takes
    /// them back with their reverse.
    @ObservationIgnored private var landed: [UUID: (entry: Entry, created: String?)] = [:]
    /// How many reads of the list had begun when each write the hub has was answered.
    @ObservationIgnored private var answeredAt: [UUID: Int] = [:]
    /// The latest read of the list that has landed (`hubAnswered`).
    @ObservationIgnored private var lastRead = 0
    @ObservationIgnored private var timer: Task<Void, Never>?
    @ObservationIgnored private var timedFor: Date?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private var idle: [@MainActor () -> Void] = []
    /// What was kept on disk is in: until then nothing is written (it would write over it), and the hubs forgotten and
    /// the reads answered meanwhile wait for it.
    @ObservationIgnored private var loaded = false
    @ObservationIgnored private var loading: Task<Void, Never>?
    @ObservationIgnored private var forgetOnLoad: (all: Bool, groups: Set<String>) = (false, [])
    @ObservationIgnored private var readOnLoad: Int?
    @ObservationIgnored private var saves = 0
    @ObservationIgnored private var lastSave: Task<Void, Never>?

    nonisolated static let stateName = "todo-outbox"
    /// A change to a to-do older than this (its hub never came back) is let go: what it changes may be long gone.
    static let keepFor: TimeInterval = 12 * 3600
    /// A new to-do that never reached the hub is kept longer: nothing can have changed under it.
    static let keepNewFor: TimeInterval = 7 * 24 * 3600
    /// A new to-do taken back while nobody knew whether the hub made it is looked for this long, then forgotten.
    static let unsureFor: TimeInterval = 10 * 60
    /// The hub's clock may be this far from this device's: a new to-do it made is matched within that of its request.
    static let clockSlack: TimeInterval = 5 * 60
    /// How long after the request the hub may still make a new to-do: its relay waits this long for the assistant
    /// (`server/internal/hub/bus_forward.go`), so a to-do made later is another one, the owner's own doing.
    static let relayWait: TimeInterval = 5 * 60
    /// Sends that end with nobody knowing whether the hub took them: after this many, a write is let go and said.
    static let tries = 5

    init(deps: Deps) {
        self.deps = deps
        loading = Task { [weak self, load = deps.load] in
            let kept = await load()
            self?.loadedFromDisk(kept)
        }
    }

    /// The id a new to-do has on this device until the hub names it.
    static func newID() -> String { "new-" + UUID().uuidString.lowercased() }

    /// Waits until what was kept on disk is in (tests).
    func ready() async { await loading?.value }

    // MARK: What the list draws

    /// The shown hub's writes, the latest for each to-do; a new one's only until the hub's row for it is in. Not one
    /// taken back.
    var writes: [String: Entry] {
        guard let hub = deps.hub() else { return [:] }
        var out: [String: Entry] = [:]
        for entry in entries where entry.hub == hub && entry.created == nil && !entry.undone && !takenBack.contains(entry.id) {
            if case .answer = entry.write { continue } // said by the row's answers, not as a change to the to-do
            out[entry.todo] = entry
        }
        return out
    }

    /// The shown hub's answers still here, the asks they answer by to-do (`TodoCountsSource`): each ask counts as answered
    /// from the tap on, also after a relaunch, until its answer is let go.
    var answering: [String: Set<String>] {
        guard let hub = deps.hub() else { return [:] }
        var out: [String: Set<String>] = [:]
        for entry in entries where entry.hub == hub && !takenBack.contains(entry.id) {
            if case .answer(let a) = entry.write, !a.ask.isEmpty { out[entry.todo, default: []].insert(a.ask) }
        }
        return out
    }

    /// The latest answer still here on hub `hub` that `matches`: `DecisionAnswers` finds a round's by its key. Read on
    /// every draw of a decision row, so it walks the entries from the newest without building a list (P4).
    func lastAnswer(hub: String, where matches: (Entry, AskAnswer) -> Bool) -> (entry: Entry, answer: AskAnswer)? {
        for entry in entries.reversed() where entry.hub == hub && !takenBack.contains(entry.id) {
            if case .answer(let a) = entry.write, matches(entry, a) { return (entry, a) }
        }
        return nil
    }

    /// What the list's rows and every number show of the shown hub's writes (`TodoCountsSource`): a deleted to-do is
    /// gone, one put in Someday is there, a new one is listed.
    var pending: [String: TodoCountsSource.Pending] {
        guard let hub = deps.hub() else { return [:] }
        var out: [String: TodoCountsSource.Pending] = [:]
        for entry in entries where entry.hub == hub && !entry.undone && !takenBack.contains(entry.id) {
            switch entry.write {
            case .delete: out[entry.todo] = .gone
            case .someday(true) where out[entry.todo] != .gone: out[entry.todo] = .someday
            case .someday(false) where out[entry.todo] == .someday: out[entry.todo] = nil
            case .create: if let item = entry.newItem { out[entry.todo] = .added(item) }
            default: break
            }
        }
        return out
    }

    /// The ids the hub gave the shown hub's new to-dos whose writes are still here: their rows take the place of the ones
    /// drawn for them, so the list does not fade them in as rows that moved.
    var named: Set<String> {
        guard let hub = deps.hub() else { return [] }
        return Set(entries.filter { $0.hub == hub }.compactMap(\.created))
    }

    /// Whether write `id` still waits out its window here, unsent: Undo on it sends nothing.
    func holds(_ id: UUID) -> Bool {
        entries.contains { $0.id == id && !$0.sent && !$0.answered } && !takenBack.contains(id)
    }

    // MARK: Writing

    /// Holds `write` on to-do `todo` for `hold` (none: it goes now), for the hub shown now or `hub`. A write that takes
    /// back one of the to-do's still held here takes that one back instead and sends nothing. nil without a hub, and when
    /// it took one back.
    @discardableResult
    func add(_ write: Write, todo: String, manager: String?, hold: Duration = .zero, hub: String? = nil) -> Entry? {
        guard let hub = hub ?? deps.hub() else { return nil }
        if hold <= .zero, let held = entries.last(where: { $0.hub == hub && $0.todo == todo && $0.write.reverse() == write
                                                          && !$0.answered && !$0.undone && !takenBack.contains($0.id) }),
           takeBack(held.id) {
            return nil
        }
        let now = deps.now()
        let shown = hub == deps.hub()
        var entry = Entry(id: UUID(), hub: hub, todo: todo, manager: manager, write: write, made: now,
                          due: now.addingTimeInterval(hold.seconds), base: shown ? deps.standing(todo)?.updatedAt : nil)
        // Another hub's (an Undo after its pages went): it goes once that hub is shown and answers again.
        entry.waits = !shown
        entries.append(entry)
        save()
        followScene()
        if hold <= .zero { sendOne(entry) } else { armTimer() }
        return entry
    }

    /// Undo: a write still here and unsent is let go and nothing is sent; one being sent, or one the hub has or may have,
    /// is taken back with its reverse (a reopen, Someday's removal, a new to-do's delete). false: it cannot be (a delete
    /// the hub has).
    @discardableResult
    func takeBack(_ id: UUID) -> Bool {
        if let i = entries.firstIndex(where: { $0.id == id }) {
            let entry = entries[i]
            if sending.contains(id) {
                guard entry.write.reverse() != nil else { return false }
                takenBack.insert(id) // drawn taken back now; its reverse goes once the hub answers
                return true
            }
            if entry.unsure {
                entries[i].undone = true
                save()
                return true
            }
            if entry.answered || (entry.sent && entry.write.edits) {
                // The hub has it, or may have: its reverse goes.
                guard let back = entry.write.reverse() else { return false }
                entries.remove(at: i)
                answeredAt[id] = nil
                add(back, todo: entry.created ?? entry.todo, manager: entry.manager, hub: entry.hub)
                return true
            }
            entries.remove(at: i)
            save()
            armTimer()
            return true
        }
        guard let (entry, created) = landed.removeValue(forKey: id), let back = entry.write.reverse() else { return false }
        add(back, todo: created ?? entry.todo, manager: entry.manager, hub: entry.hub)
        return true
    }

    /// Try Again on a new to-do nobody knew the hub made: the owner sends it again, with the same op id.
    func retry(_ id: UUID) {
        guard let i = entries.firstIndex(where: { $0.id == id && $0.unsure && !$0.undone }) else { return }
        entries[i].unsure = false
        entries[i].waits = false
        save()
        sendOne(entries[i])
    }

    /// New To-Do reopened with what a refused one said (the toast's Edit); the sheet takes it (`tookDraft`).
    func edit(_ draft: Draft) { self.draft = draft }
    func tookDraft() { draft = nil }

    /// The hub answered read `read` of the list, which has landed: the writes it shows are let go, a new to-do nobody
    /// knew it made is found in it, and the writes whose window ended go, those that could not reach it again.
    func hubAnswered(read: Int) {
        guard loaded else {
            readOnLoad = max(readOnLoad ?? 0, read)
            return
        }
        lastRead = max(lastRead, read)
        var changed = false
        for entry in entries where entry.answered && (answeredAt[entry.id] ?? 0) < read {
            retire(entry)
            changed = true
        }
        changed = settleUnsure() || changed
        changed = expire() || changed
        changed = settleAnsweredElsewhere() || changed
        if changed { save() }
        let now = deps.now()
        sendPicked { !$0.answered && !$0.unsure && !$0.undone && $0.due <= now }
    }

    /// The scene leaves the foreground: every write goes now, its window or not (iOS may end the app before it ends),
    /// under a background task that lasts until every send under way is answered.
    func sendAllNow() {
        guard !entries.isEmpty || !sending.isEmpty else { return }
        #if os(iOS)
        let app = UIApplication.shared
        var task = UIBackgroundTaskIdentifier.invalid
        task = app.beginBackgroundTask(withName: "todo-outbox") {
            app.endBackgroundTask(task)
            task = .invalid
        }
        sendPicked({ !$0.answered && !$0.unsure && !$0.undone }) { [weak self] in
            self?.whenIdle {
                if task != .invalid { app.endBackgroundTask(task) }
            }
        }
        #else
        sendPicked { !$0.answered && !$0.unsure && !$0.undone }
        #endif
    }

    /// The writes made on hubs that are gone (forgotten; nil: every hub, the app unpaired): never sent, and off the disk.
    func forget(_ groups: Set<String>?) {
        let gone = { (entry: Entry) in groups?.contains(entry.hub) ?? true }
        if !loaded {
            if let groups { forgetOnLoad.groups.formUnion(groups) } else { forgetOnLoad.all = true }
        }
        for entry in entries where gone(entry) { if case .answer = entry.write { deps.settled(entry, .refused("")) } }
        entries.removeAll(where: gone)
        landed = landed.filter { !gone($0.value.entry) }
        takenBack = takenBack.filter { id in entries.contains { $0.id == id } }
        save()
        armTimer()
    }

    /// `forget` on the app's outbox, only while this build may show V1 (`TodosV1.allowedHere`): a Release build before the
    /// switch-on never keeps a write, so its outbox is not made just to read no file and write an empty one.
    static func forgetHubs(_ groups: Set<String>?) {
        guard TodosV1.allowedHere else { return }
        shared.forget(groups)
    }

    // MARK: Sending

    /// Sends the shown hub's writes `which` picks, not those being sent; `finished` once they are answered.
    private func sendPicked(_ which: (Entry) -> Bool, finished: (@MainActor () -> Void)? = nil) {
        let hub = deps.hub()
        let picked = entries.filter { $0.hub == hub && !sending.contains($0.id) && which($0) }
        guard !picked.isEmpty else { finished?(); return }
        var left = picked.count
        for entry in picked {
            sendOne(entry) {
                left -= 1
                if left == 0 { finished?() }
            }
        }
    }

    /// Sends `entry` through the shown hub's client: another hub's waits until its pages are shown.
    private func sendOne(_ entry: Entry, answered done: (@MainActor () -> Void)? = nil) {
        guard entry.hub == deps.hub(), !sending.contains(entry.id), stillWanted(entry),
              let i = entries.firstIndex(where: { $0.id == entry.id })
        else { done?(); return }
        sending.insert(entry.id)
        entries[i].sent = true
        save()
        let wait = entry.write.edits ? nil : lastSave // a new to-do is on disk as sent before it goes
        armTimer()
        Task { [weak self] in
            await wait?.value
            guard let self else { return }
            let sent = await deps.send(entry) { [weak self] answer in self?.answered(entry.id, answer) }
            sending.remove(entry.id)
            finish(entry, sent)
            done?()
            if sending.isEmpty {
                let waiting = idle
                idle = []
                for run in waiting { run() }
            }
        }
    }

    /// `run` once no send is under way.
    private func whenIdle(_ run: @escaping @MainActor () -> Void) {
        if sending.isEmpty { run() } else { idle.append(run) }
    }

    /// A write that waited (it could not reach the hub, or the app was ended with it) on a to-do that changed since the
    /// owner made it is let go: what the owner saw is not what is there now. Said, unless the to-do is already as it
    /// asks. One whose to-do is gone from the list is let go quietly: there is nothing left to change.
    private func stillWanted(_ entry: Entry) -> Bool {
        if case .answer = entry.write {
            // An answer goes unless it never left and a read shows its ask answered meanwhile (`answeredElsewhere`). One
            // whose send began may have reached the hub, so it goes again with its op id and the receipt says the first
            // answer; with the row or its view missing (a manager reconnecting), the hub's own answer says whether the ask
            // still waits.
            guard answeredElsewhere(entry) else { return true }
            drop(entry.id)
            deps.settled(entry, .elsewhere)
            return false
        }
        guard entry.waits, entry.write.edits else { return true }
        guard let now = deps.standing(entry.todo) else {
            drop(entry.id)
            return false
        }
        guard let base = entry.base, let at = now.updatedAt, at > base else { return true }
        drop(entry.id)
        if !now.has(entry.write) {
            deps.refused(entry, String(localized: "“\(now.title)” changed in the meantime, so your change wasn't saved. [To-dos Toast]"),
                         false)
        }
        return false
    }

    /// An answer none of whose sends began, to an ask the to-do's view (read since) no longer lists: answered on another
    /// device, asked again, or the to-do closed. Not one without the ask's id, whose row or view is missing, or that may
    /// have reached the hub: those go, and the hub's answer says.
    private func answeredElsewhere(_ entry: Entry) -> Bool {
        guard case .answer(let a) = entry.write, !a.ask.isEmpty, !entry.sent, !sending.contains(entry.id) else { return false }
        return deps.standing(entry.todo)?.has(entry.write) == true
    }

    /// A read landed: the answers held on the shown hub whose ask it shows answered meanwhile say so now, not when their
    /// window ends. true: some were let go.
    private func settleAnsweredElsewhere() -> Bool {
        let hub = deps.hub()
        let gone = entries.filter { $0.hub == hub && answeredElsewhere($0) }
        guard !gone.isEmpty else { return false }
        for entry in gone {
            entries.removeAll { $0.id == entry.id }
            deps.settled(entry, .elsewhere)
        }
        return true
    }

    /// The hub answered write `id`: kept as answered (never sent again) until a later read shows it. One taken back
    /// meanwhile is let go and its reverse goes now (a new to-do's delete, once the hub names it).
    private func answered(_ id: UUID, _ answer: Answer) {
        guard let i = entries.firstIndex(where: { $0.id == id }) else { return }
        if takenBack.remove(id) != nil, let back = entries[i].write.reverse() {
            let entry = entries.remove(at: i)
            add(back, todo: answer.created ?? entry.todo, manager: entry.manager, hub: entry.hub)
            return
        }
        answeredAt[id] = answer.read
        entries[i].answered = true
        entries[i].created = answer.created
        save()
    }

    private func finish(_ entry: Entry, _ sent: Sent) {
        defer { armTimer() }
        if case .done(let created) = sent {
            // Told no answer (a to-do already gone): as if it had been, its reverse going now if it was taken back.
            if entries.first(where: { $0.id == entry.id })?.answered == false {
                answered(entry.id, Answer(created: created, read: lastRead))
            }
            takenBack.remove(entry.id)
            if let kept = entries.first(where: { $0.id == entry.id }), (answeredAt[entry.id] ?? 0) < lastRead {
                retire(kept) // a read that began after its answer landed while it was being confirmed
                save()
            }
            if case .answer = entry.write { deps.settled(entry, sent) }
            return
        }
        let back = takenBack.remove(entry.id) != nil
        guard let i = entries.firstIndex(where: { $0.id == entry.id }) else { return }
        switch sent {
        case .done:
            return
        case .elsewhere, .inDoubt:
            entries.remove(at: i)
            save()
            deps.settled(entry, sent)
        case .notSent:
            if back {
                entries.remove(at: i) // it never reached the hub: nothing went, so nothing goes
            } else {
                // Nothing went: an Undo now sends nothing, and the hub's next answer sends it again.
                entries[i].sent = false
                entries[i].waits = true
            }
            save()
        case .unknown:
            if !entry.write.edits {
                // The hub makes a new to-do for each request it takes: never sent again on its own.
                entries[i].unsure = true
                entries[i].undone = back
            } else if back {
                // It may have landed: its reverse goes, which changes nothing if it did not.
                let taken = entries.remove(at: i)
                if let reverse = taken.write.reverse() { add(reverse, todo: taken.todo, manager: taken.manager, hub: taken.hub) }
            } else if entries[i].unknown + 1 >= Self.tries {
                let given = entries.remove(at: i)
                if case .answer = given.write {
                    // It may be with the Agent: never said as not sent.
                    deps.settled(given, .inDoubt)
                } else {
                    deps.refused(given, String(localized: "Couldn't save your change to “\(deps.standing(given.todo)?.title ?? "")”. [To-dos Toast]"),
                                 false)
                }
            } else {
                entries[i].unknown += 1
                entries[i].waits = true
            }
            save()
        case .refused(let message, let project):
            entries.remove(at: i)
            save()
            if !back { deps.refused(entry, message, project) }
            if case .answer = entry.write { deps.settled(entry, sent) }
        }
    }

    /// Write `entry` left: the hub's view shows it. An Undo after this sends its reverse.
    private func retire(_ entry: Entry) {
        entries.removeAll { $0.id == entry.id }
        answeredAt[entry.id] = nil
        landed[entry.id] = (entry, entry.created)
        if landed.count > 32, let oldest = landed.min(by: { $0.value.entry.made < $1.value.entry.made })?.key {
            landed[oldest] = nil
        }
    }

    private func drop(_ id: UUID) {
        entries.removeAll { $0.id == id }
        save()
    }

    /// A new to-do nobody knew the hub made, which the read now lists (its title and notes, made while the hub could
    /// still have been making it, and no other write's): its own row takes the place of the one drawn for it, or, taken
    /// back meanwhile, it is deleted. One taken back that no read listed is forgotten after `unsureFor`. true: something
    /// changed.
    private func settleUnsure() -> Bool {
        let hub = deps.hub()
        guard entries.contains(where: { $0.unsure && $0.hub == hub }) else { return false }
        let rows = deps.listed(), now = deps.now()
        var claimed = Set(entries.compactMap(\.created)).union(landed.values.compactMap(\.created))
        var changed = false
        for entry in entries where entry.unsure && entry.hub == hub {
            guard case .create(let title, let notes, _, _, _) = entry.write else { continue }
            let since = entry.made.addingTimeInterval(-Self.clockSlack)
            let until = entry.made.addingTimeInterval(Self.clockSlack + Self.relayWait)
            if let row = rows.first(where: { row in
                guard !claimed.contains(row.id), row.title == title, (row.notes ?? "") == (notes ?? ""), let at = row.createdAt
                else { return false }
                return at >= since && at <= until
            }) {
                claimed.insert(row.id)
                entries.removeAll { $0.id == entry.id }
                if entry.undone {
                    add(.delete, todo: row.id, manager: entry.manager, hub: entry.hub)
                } else {
                    landed[entry.id] = (entry, row.id)
                }
                changed = true
            } else if entry.undone && now.timeIntervalSince(entry.made) > Self.unsureFor {
                entries.removeAll { $0.id == entry.id }
                changed = true
            }
        }
        return changed
    }

    /// Writes whose hub never came back in time are let go (`keepFor`, `keepNewFor`). An answer that never left says it
    /// was not sent and its buttons come back; one whose send began may be with the Agent, so it says it is not sure,
    /// never that it was not sent. true: some were.
    private func expire() -> Bool {
        let now = deps.now()
        let gone = entries.filter { entry in
            !entry.answered && !sending.contains(entry.id)
                && now.timeIntervalSince(entry.made) >= (entry.write.edits ? Self.keepFor : Self.keepNewFor)
        }
        guard !gone.isEmpty else { return false }
        let ids = Set(gone.map(\.id))
        entries.removeAll { ids.contains($0.id) }
        for entry in gone {
            guard case .answer = entry.write else { continue }
            if entry.sent {
                deps.settled(entry, .inDoubt)
            } else {
                let why = String(localized: "Not connected to a hub [Todo Store 04f822]")
                deps.refused(entry, String(localized: "Couldn't send your answer. \(why)"), false)
                deps.settled(entry, .refused(""))
            }
        }
        return true
    }

    /// What the last run kept, in: a write sent then is one nobody knows the hub took (a new to-do: "Not sure it was
    /// saved"); every other one goes once the hub answers a read, checked against its to-do then.
    private func loadedFromDisk(_ kept: [Entry]) {
        let forgotten = forgetOnLoad
        let mine = Set(entries.map(\.id))
        let prepared: [Entry] = kept.compactMap { entry in
            guard !mine.contains(entry.id), !forgotten.all, !forgotten.groups.contains(entry.hub) else { return nil }
            var entry = entry
            if !entry.answered {
                // `sent` on disk is a send that began and whose answer never came (`.notSent` clears it), whether or not
                // it had waited before: a new to-do then may be on the hub.
                if !entry.write.edits && entry.sent {
                    entry.unsure = true
                    entry.waits = false
                } else {
                    entry.waits = true
                }
            }
            return entry
        }
        entries = prepared + entries
        loaded = true
        _ = expire()
        save()
        followScene()
        armTimer()
        if let read = readOnLoad {
            readOnLoad = nil
            hubAnswered(read: read)
        }
    }

    /// Writes every write down, numbered (`Deps.save`); not before what was kept is in.
    private func save() {
        guard loaded else { return }
        saves += 1
        lastSave = deps.save(entries, saves)
    }

    /// Once there is a write: the app's outbox sends it as the scene leaves the foreground.
    private func followScene() {
        #if os(iOS)
        guard deps.followsScene, observers.isEmpty, !entries.isEmpty else { return }
        let center = NotificationCenter.default
        for name in [UIApplication.didEnterBackgroundNotification, UIApplication.willTerminateNotification] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { @Sendable [weak self] _ in
                MainActor.assumeIsolated { self?.sendAllNow() }
            })
        }
        #endif
    }

    /// One timer, to the end of the soonest window among the shown hub's writes that wait for it.
    private func armTimer() {
        let hub = deps.hub()
        let next = entries.filter { $0.hub == hub && !$0.waits && !$0.sent && !$0.answered && !sending.contains($0.id) }
            .map(\.due).min()
        guard next != timedFor else { return }
        timer?.cancel()
        timedFor = next
        guard let next else {
            timer = nil
            return
        }
        let wait = max(0, next.timeIntervalSince(deps.now()))
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled, let self else { return }
            timedFor = nil
            timer = nil
            let now = deps.now()
            sendPicked { !$0.waits && !$0.sent && !$0.answered && $0.due <= now }
            armTimer()
        }
    }

    // MARK: How a send went

    /// What an error from the hub means for a write, as docs/apple/ios.md "To-dos (V1, #10465)" reads every to-do write's
    /// answer: a to-do already gone is what a change to it comes to (a delete is done, a tick on it moot); a request that
    /// reached nothing goes again; a refusal is dropped and said; anything else may have been done.
    nonisolated static func outcome(_ error: Error, of write: Write) -> Sent {
        let api = error as? APIError
        if case .answer(let a) = write {
            // The ask no longer waits: the first answer anywhere won. Today's accept route says so with a bare 409.
            if TodoAskRefusal.answeredElsewhere(error) || (api?.status == 409 && a.kind != OwnerAskKind.retry) { return .elsewhere }
            if let api {
                // The words reached the Agent and what followed failed: taken, and never sent again.
                if api.fields["delivered"] == "yes" { return .done(created: nil) }
                // Nobody can say whether they did: the receipt says the same to a repeat, so it is not sent again.
                if api.fields["delivered"] == "unknown" { return .inDoubt }
                // A refusal the hub's assistant gave (a node's 423, 502 `accept_not_taken`): the receipt says the same to
                // a repeat, so it is dropped and said rather than sent again. Not one that reached no assistant at all.
                let away = api.status == 0 || [408, 425, 429].contains(api.status) || api.code == "assistant_offline"
                if !away, (400..<500).contains(api.status) || (api.status >= 500 && api.fields["delivered"] == "none") {
                    return .refused(api.message)
                }
            }
        }
        if let api, write.edits, TodoNotThere.deleted(api) { return .done(created: nil) }
        if reachedNothing(error) { return .notSent }
        if let api, (400..<500).contains(api.status) { return .refused(api.message, project: api.status == 400 && write.inProject) }
        return .unknown
    }

    /// Nothing reached the hub's assistant: no connection was made, the request never left (no hub), the assistant is
    /// away (`assistant_offline`), the hub turned it away unread (408, 425, 429) or a server error said nothing was
    /// delivered. A 4xx business refusal (such as 422 `todo_close_not_confirmed` or 409 `todo_close_active`) is never
    /// retried as unsent even when it carries `delivered: "none"`. A connection lost or a timeout once it was sent is
    /// not this: the hub may have it.
    nonisolated static func reachedNothing(_ error: Error) -> Bool {
        if let api = error as? APIError {
            if api.status == 0 || [408, 425, 429].contains(api.status) || api.code == "assistant_offline" { return true }
            return api.status >= 500 && api.fields["delivered"] == "none"
        }
        let ns = error as NSError
        return ns.domain == NSURLErrorDomain && [
            NSURLErrorNotConnectedToInternet, NSURLErrorCannotConnectToHost, NSURLErrorCannotFindHost,
            NSURLErrorDNSLookupFailed, NSURLErrorDataNotAllowed, NSURLErrorInternationalRoamingOff, NSURLErrorCallIsActive,
            NSURLErrorSecureConnectionFailed,
        ].contains(ns.code)
    }
}

/// The outbox's file (StateCache `todo-outbox`), read and written off the main actor one at a time. Each snapshot is
/// numbered: the outbox writes one on every change, and an older one that comes late never replaces a newer one.
actor TodoOutboxFile {
    static let shared = TodoOutboxFile()
    private let folder: URL?
    private var written = 0

    init(folder: URL? = nil) {
        self.folder = folder
    }

    func load() -> [TodoOutbox.Entry] {
        StateCache.load(TodoOutbox.stateName, as: [TodoOutbox.Entry].self, in: folder) ?? []
    }

    /// Writes snapshot `number`, unless a later one is written already.
    func save(_ entries: [TodoOutbox.Entry], number: Int) {
        guard number > written else { return }
        written = number
        StateCache.saveNow(TodoOutbox.stateName, entries, in: folder)
    }
}

extension TodoStore {
    /// Sends one of the list's held writes (`TodoOutbox`) through the to-do's own assistant, with its op id, and reads the
    /// list again after it, so the views that show it can land before the outbox lets it go.
    func send(_ entry: TodoOutbox.Entry, answered: @escaping @MainActor (TodoOutbox.Answer) -> Void = { _ in },
              api injected: APIClient? = nil) async -> TodoOutbox.Sent {
        do {
            let created = try await write(entry.write, todo: entry.todo, manager: entry.manager, opID: entry.id,
                                          answered: answered, api: injected)
            return .done(created: created)
        } catch {
            return TodoOutbox.outcome(error, of: entry.write)
        }
    }
}

private extension Duration {
    var seconds: TimeInterval {
        let (whole, atto) = components
        return TimeInterval(whole) + TimeInterval(atto) / 1e18
    }
}
