import Foundation
import Observation

/// Which of the assistant's conversations scheduled tasks and follow-ups started (#2838; the web does the same, #2824).
/// They are listed on Scheduled Tasks, under each task, and marked as a task's wherever else they are (#5195). Known
/// from the tasks (their own conversation, the last run's) and the runs (each run's), less the conversations tasks came
/// from (Continue in Original Chat: the user's own).
/// The run history is capped (the hub keeps the last 200), so an older run's conversation is known by its title as
/// well: 「MMDD | 定时 | <the task's title>」, the title the hub gives it (titles.go). The title alone is not enough: 「定时」 is
/// one of the kinds the model may give the user's own conversations too.
nonisolated struct AssistantTaskChats: Equatable, Sendable {
    var ids: Set<String> = []
    /// The tasks' titles.
    var topics: [String] = []

    static let none = AssistantTaskChats()

    init() {}

    init(tasks: [APIClient.ScheduledTask], runs: [APIClient.ScheduleRun]) {
        let cameFrom = Set(tasks.compactMap(\.sessionID))
        var ids = Set<String>()
        func add(_ sid: String?) { if let sid, !sid.isEmpty, !cameFrom.contains(sid) { ids.insert(sid) } }
        for t in tasks {
            add(t.runSessionID)
            add(t.lastSessionID)
        }
        for r in runs { add(r.sessionID) }
        self.ids = ids
        topics = tasks.map { $0.title.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    /// Whether a scheduled task started this conversation.
    func contains(sessionID: String, title: String) -> Bool {
        if ids.contains(sessionID) { return true }
        let parts = title.split(separator: "|", maxSplits: 2).map { $0.trimmingCharacters(in: .whitespaces) }
        guard parts.count == 3, parts[0].count == 4, parts[0].allSatisfy(\.isASCII), parts[0].allSatisfy(\.isNumber),
              parts[1] == "定时", !parts[2].isEmpty else { return false } // i18n-ignore: the hub's place name
        // The hub cuts the task's title to fit: either may be the longer.
        return topics.contains { $0.hasPrefix(parts[2]) || parts[2].hasPrefix($0) }
    }

    func contains(_ s: Session) -> Bool { contains(sessionID: s.sessionID, title: s.title) }

    /// All Chats' Mine on a hub that never says who opened a conversation: the conversations without the tasks', except
    /// the open one (`current`), which keeps its row.
    func recent(_ sessions: [Session], current: SessionKey?) -> [Session] {
        sessions.filter { $0.key == current || !contains($0) }
    }

    /// One conversation a task's runs went into, and when a run last went into it.
    struct Chat: Equatable, Sendable {
        var key: SessionKey
        var at: Date?
    }

    /// A task's conversations, at most `limit`, each once: its own one first when it has one, then the runs' (newest
    /// first, as the hub lists them). The one it came from is the user's own and stays out. `machineID`: the
    /// assistant's, for a run that does not say.
    static func chats(of task: APIClient.ScheduledTask, runs: [APIClient.ScheduleRun], machineID: String, limit: Int = 3) -> [Chat] {
        var out: [Chat] = []
        func add(_ sid: String?, _ machine: String?, _ at: Date?) {
            guard let sid, !sid.isEmpty, sid != task.sessionID else { return }
            if let i = out.firstIndex(where: { $0.key.sessionID == sid }) {
                if out[i].at == nil { out[i].at = at }
            } else {
                out.append(Chat(key: SessionKey(machineID: machine ?? machineID, sessionID: sid), at: at))
            }
        }
        add(task.runSessionID, task.runMachineID, nil)
        for r in runs where r.refID == task.id { add(r.sessionID, r.machineID, r.at) }
        if let first = out.first, first.at == nil, first.key.sessionID == task.lastSessionID { out[0].at = task.lastRun }
        return Array(out.prefix(limit))
    }

    /// What a chat's link says: when a run last went into it (Today 09:05, Yesterday 21:30, Nov 1 09:00), else its place.
    static func label(_ chat: Chat, index: Int, now: Date = .now) -> String {
        guard let at = chat.at else { return String(localized: "Chat \(index + 1)") }
        let cal = Calendar.current
        let time = String(format: "%02d:%02d", cal.component(.hour, from: at), cal.component(.minute, from: at))
        let days = cal.dateComponents([.day], from: cal.startOfDay(for: at), to: cal.startOfDay(for: now)).day ?? 0
        switch days {
        case 0: return String(localized: "Today \(time)")
        case 1: return String(localized: "Yesterday \(time)")
        default: return String(localized: "\(at.formatted(.dateTime.month().day())) \(time)", comment: "A day and a time of day")
        }
    }
}

/// The scheduled tasks' conversations the sidebar leaves out, read from the hub when the sidebar shows (and the list of
/// conversations changed), and whenever Scheduled Tasks has read the tasks and runs anyway.
@Observable final class AssistantTaskChatsStore {
    static let shared = AssistantTaskChatsStore()

    /// By page (group and the manager the Persona page shows): each manager has its own tasks.
    private var byPage: [String: AssistantTaskChats] = [:]
    /// The page a read is on its way for: one for a page left meanwhile doesn't land.
    @ObservationIgnored private var loading: (page: String, task: Task<AssistantTaskChats?, Never>)?

    /// What is known of the page `api` reads.
    func known(on api: APIClient?) -> AssistantTaskChats { api.flatMap { byPage[Self.page($0)] } ?? .none }

    func update(tasks: [APIClient.ScheduledTask], runs: [APIClient.ScheduleRun], on api: APIClient) {
        let next = AssistantTaskChats(tasks: tasks, runs: runs)
        if next != byPage[Self.page(api)] { byPage[Self.page(api)] = next }
    }

    /// Reads the tasks and runs again; what was known stays when the hub cannot say (offline, no tasks there).
    func refresh(_ api: APIClient?) async {
        guard let api, !Task.isCancelled else { return }
        let page = Self.page(api)
        if let loading, loading.page == page, !loading.task.isCancelled { return }
        loading?.task.cancel()
        let read = Task<AssistantTaskChats?, Never> {
            async let r = try? api.scheduleRuns()
            guard let tasks = try? await api.scheduledTasks(), let runs = await r, !Task.isCancelled else { return nil }
            return AssistantTaskChats(tasks: tasks, runs: runs)
        }
        loading = (page, read)
        // SwiftUI replaces its task even on the same page when the chat count changes.
        // Mark the cancelled read immediately so its replacement can start while the transport unwinds.
        let next = await withTaskCancellationHandler {
            await read.value
        } onCancel: { read.cancel() }
        guard loading?.task == read else { return }
        loading = nil
        if !Task.isCancelled, let next, next != byPage[page] { byPage[page] = next }
    }

    private static func page(_ api: APIClient) -> String { api.group + "\u{0}" + (api.shownManager ?? "") }

    /// When a sidebar reads them again (its `.task` id): as it comes out (nothing while it is in), when a conversation
    /// came or went, and for another page, another hub's as well as another manager's (#11657: two hubs with as many
    /// chats shared a key, and a sidebar that stays out, the iPad's column, kept knowing only the hub before's tasks).
    nonisolated static func readKey(out: Bool, group: String?, manager: String?, conversations: Int) -> String {
        out ? (group ?? "") + "\u{0}" + (manager ?? "") + "|\(conversations)" : ""
    }
}
