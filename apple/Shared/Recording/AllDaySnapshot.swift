import Foundation

/// What the watch app last knew about all-day recording (docs/assistant/day-recap.md), for the All-Day Recording
/// complication (`AllDayRecordingComplication`). The app writes it into the watch App Group on every change of state
/// and, while it records, every few minutes as a heartbeat; the complication only reads it. Self-contained (Foundation
/// only, its own `State`), since the widget extension compiles this file without the rest of `Shared`.
nonisolated struct AllDaySnapshot: Codable, Equatable, Sendable {
    /// The mode as the watch shows it: off, recording, paused by the user, paused by the system (a call, Siri) or a
    /// failed resume, and stopped by itself (battery, storage, the app ended) while the user still wants it on.
    nonisolated enum State: String, Codable, Sendable { case off, recording, paused, interrupted, stopped }

    /// What the watch offers, from the phone's `recordingDay` (docs/assistant/day-recap.md「Watch and phone
    /// capabilities」, `AllDayAlertPolicy.access`): here so the face, which builds nothing else of the app, says it too.
    nonisolated enum Access: String, Codable, Sendable {
        /// Nothing offers All-Day Recording: the phone doesn't take day recordings (a phone from before, or one that
        /// hasn't said yet).
        case hidden
        /// Offered, but a start waits for the user to agree on the iPhone: the watch explains it and points there.
        case needsConsent
        /// One tap starts or continues it.
        case ready
    }

    var state: State
    /// When `state` began: the face counts a recording's time from here without being reloaded.
    var since: Date?
    /// When the app last wrote it. While recording that is the latest heartbeat, so a recording snapshot that stopped
    /// being refreshed (the app died without a word) turns into "Check recording" `staleAfter` later.
    var updatedAt: Date
    /// The local date being recorded, `YYYY-MM-DD`.
    var day: String?
    /// Chunks the iPhone has not acknowledged yet.
    var pending: Int?
    /// What a tap may do while nothing records (`AllDayAlertPolicy.access` when it was written); nil in a snapshot
    /// from before it was kept, read as `ready`, which is what the face said then.
    var access: Access?

    /// The complication's WidgetKit kind: the watch app reloads only its timelines, not the other faces'.
    static let widgetKind = "persona.watch.all-day-recording"
    /// A recording snapshot older than this no longer stands for a recording. The app refreshes it about every 10
    /// minutes while recording (`AllDayAlertPolicy.heartbeatEvery`); the rest is room for WidgetKit's scheduling.
    static let staleAfter: TimeInterval = 25 * 60
    static let fileName = "allday.json"
    /// Info.plist key of the App Group both bundles share: the watch-only group (as `WatchPetSnapshot.groupKey`).
    static let groupKey = "PersonaAppGroup"

    /// What a face shows at a moment: the state, or `stale` once a recording snapshot is too old to trust.
    nonisolated enum Face: Equatable, Sendable {
        /// Off. `access`: whether a tap can start it (docs/assistant/day-recap.md「End to end」: offered only once the
        /// phone takes day recordings and its user agreed); nil before the watch app has written anything.
        case off(Access?)
        case recording(since: Date)
        case paused
        /// Stopped by itself while the user still wants it on; `startable` false while a tap can't continue it yet
        /// (the iPhone's consent is missing, a new version of it included).
        case stopped(startable: Bool)
        /// A recording nobody has confirmed since `lastHeard`.
        case stale(lastHeard: Date)

        /// Where a tap on the face goes: start when off and ready, continue when paused or stopped and ready,
        /// otherwise the page, which says what is missing (a stale face may still be recording, or the app may have
        /// died: opening it finds out).
        var link: AllDayLink.Action {
            switch self {
            case .off(.ready?): .start
            case .paused, .stopped(startable: true): .resume
            case .off, .stopped, .recording, .stale: .open
            }
        }
    }

    /// When this snapshot stops standing for a recording; nil for the other states, which don't age.
    var staleDate: Date? { state == .recording ? updatedAt.addingTimeInterval(Self.staleAfter) : nil }

    func face(at date: Date) -> Face {
        switch state {
        case .off: .off(access ?? .ready)
        case .recording:
            if let staleDate, date >= staleDate { .stale(lastHeard: updatedAt) } else { .recording(since: since ?? updatedAt) }
        case .paused, .interrupted: .paused
        case .stopped: .stopped(startable: (access ?? .ready) == .ready)
        }
    }

    /// The face for a snapshot that may not exist yet (the watch app hasn't run since it was installed): the mode's
    /// name, opening the app, which writes the real one as it launches.
    static func face(_ snapshot: AllDaySnapshot?, at date: Date) -> Face { snapshot?.face(at: date) ?? .off(nil) }

    /// Whether writing this over `old` changes what a face draws: a recording always does (its heartbeat moves the
    /// stale moment on), anything else only with another state or offer. A launch or a context from the phone that
    /// changes neither reloads no timeline (WidgetKit's reloads are budgeted).
    func redraws(over old: AllDaySnapshot?) -> Bool {
        guard let old, state != .recording else { return true }
        return old.state != state || (old.access ?? .ready) != (access ?? .ready)
    }

    /// The provider's entry dates for a read at `now`: now, then the moment a recording goes stale while that is still
    /// ahead. A snapshot already stale gets one entry, which shows it stale at once.
    func timeline(now: Date) -> [Date] {
        guard let staleDate, staleDate > now else { return [now] }
        return [now, staleDate]
    }

    /// `AllDay/` in the App Group container; nil without the group (an unsigned build).
    static var directory: URL? {
        guard let group = Bundle.main.object(forInfoDictionaryKey: groupKey) as? String, !group.isEmpty,
              let root = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: group) else { return nil }
        return root.appending(path: "AllDay", directoryHint: .isDirectory)
    }

    /// The widget provider's read; the app never reads it back (it knows its own state).
    static func read() -> AllDaySnapshot? {
        guard let url = directory?.appending(path: fileName),
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(AllDaySnapshot.self, from: data)
    }

    /// Replaces the file atomically; false without the group or when the write failed. Called off the main thread, one
    /// write at a time (the watch app's publisher queue).
    func write() -> Bool {
        guard let directory = Self.directory, let data = try? JSONEncoder().encode(self) else { return false }
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try data.write(to: directory.appending(path: Self.fileName), options: .atomic)
            return true
        } catch {
            return false
        }
    }
}

/// The links the All-Day Recording complication opens (`persona://allday?action=start|continue|open`), and how the
/// watch app reads them. A host of its own: `persona://record` is the Record complication's.
nonisolated enum AllDayLink {
    nonisolated enum Action: String, Sendable {
        case start
        case resume = "continue"
        case open
    }

    static let host = "allday"

    static func url(_ action: Action) -> URL {
        var parts = URLComponents()
        parts.scheme = "persona"
        parts.host = host
        parts.queryItems = [URLQueryItem(name: "action", value: action.rawValue)]
        return parts.url ?? URL(fileURLWithPath: "/")
    }

    /// The action of an all-day link, whatever its scheme (the caller checks that); a link without a known action
    /// opens the page. nil for any other link.
    static func action(of url: URL) -> Action? {
        guard url.host()?.lowercased() == host else { return nil }
        let value = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "action" }?.value
        return value.flatMap(Action.init(rawValue:)) ?? .open
    }
}
