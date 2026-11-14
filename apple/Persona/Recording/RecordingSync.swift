import Foundation
import Observation
#if canImport(UIKit)
import UIKit
#endif

/// The hub's side of a recording (docs/protocol/protocol.md「Recording sync」, #3235): what `PUT /api/v1/recordings/{id}` takes.
/// Text only: the audio never leaves the device.
nonisolated struct RecordingUpload: Encodable, Sendable {
    struct Bookmark: Encodable, Sendable { var at: Double; var label = "" }
    struct Segment: Encodable, Sendable {
        var start: Double; var end: Double; var text: String
        /// Persona's echo (#5905, `RecordingAttendance.echo`): `true` only, and only to a hub that lists `meeting_echo`.
        var echo: Bool? = nil
    }
    /// A stretch Persona spoke in and what it said (`RecordingSpan`): the hub marks the echo by them too. No `text` for a
    /// stretch saved before #5905 (the hub keeps the time rule for it).
    struct Spoken: Encodable, Sendable {
        var start: Double; var end: Double; var text: String?
        init(_ span: RecordingSpan) { start = span.start; end = span.end; text = span.text }
    }
    /// The calendar event the device matched (docs/assistant/meeting-minutes.md「Field contract」): a hint for the hub, which
    /// prefers its own `meetings` / `events`. No event id: the hub can't see this calendar.
    struct CalendarHint: Encodable, Sendable {
        var title: String?
        var attendees: [String]?
        var location: String?
    }
    var revision: Int64
    var startedAt: Date
    var endedAt: Date?
    var duration: Double
    var language: String
    var device: String
    var title: String
    var bookmarks: [Bookmark]
    var segments: [Segment]
    var complete: Bool
    /// "meeting" (the hub writes it minutes) or "note".
    var kind: String
    /// Only when the device matched a calendar event.
    var calendar: CalendarHint?
    /// A session the API names (`{machine_id, session_id}`).
    struct SessionRef: Codable, Equatable, Sendable {
        var machineID: String
        var sessionID: String
        enum CodingKeys: String, CodingKey { case machineID = "machine_id", sessionID = "session_id" }
    }
    /// Persona's part in the meeting (docs/assistant/meeting-minutes.md「Attendance fields」): only on a phone meeting
    /// recording Persona was asked to attend, so every other upload is exactly as before. `off` once turned off.
    var attendance: String?
    /// The session that attends; left out for the main chat.
    var attendingSession: SessionRef?
    /// A room meeting's role (#3862, docs/assistant/meeting-minutes.md「Upload fields」): the meeting's own room attends.
    struct RoomUpload: Encodable, Sendable { var attendance: String }
    /// On every upload of a room meeting, whatever the role (`off` included), in place of `attendance` and
    /// `attending_session`: an older manager after a rollback reads only those, so it sees a meeting that is off.
    var room: RoomUpload?
    /// A room meeting's private mark as it started (the hub takes it only while it has none).
    var privateMark: Bool?
    /// Where Persona spoke in the meeting (#5905, docs/assistant/meeting-minutes.md「Persona's echo」): only to a hub that
    /// lists `meeting_echo`, and only when it spoke, so every other upload is exactly as before.
    var spoken: [Spoken]?
    /// The transcription mode the meeting asked for as it started (#3862 phase 5, docs/assistant/meeting-minutes.md
    /// 「Choosing the mode」): only on the uploads of a meeting started while the hub listed `meeting_transcription`, so
    /// every other upload is exactly as before. The hub takes the first upload's and ignores the rest: sent on each, so
    /// an upload lost on its way changes nothing.
    var transcription: String?
    /// In hub mode, the meeting's role changes and the stretches Persona spoke in (`MeetingTimeline`), whole, on every
    /// upload, the `complete: true` one included.
    var timeline: MeetingTimeline?
    /// The hub takes echo lines marked (`meeting_echo`): they go with `echo: true`; else they are left out, as before.
    private let marksEcho: Bool

    enum CodingKeys: String, CodingKey {
        case revision, duration, language, device, title, bookmarks, segments, complete, kind, calendar, attendance, room, spoken
        case transcription, timeline
        case startedAt = "started_at", endedAt = "ended_at", attendingSession = "attending_session", privateMark = "private"
    }

    /// `hubEcho`: the hub lists `meeting_echo` (`RecordingSync` reads it from the hub's `hello`; by default the last one
    /// kept, `MeetingAttendanceDefaults.hubEcho`).
    init(_ entry: RecordingEntry, hubEcho: Bool = MeetingAttendanceDefaults.hubEcho()) {
        marksEcho = hubEcho
        revision = entry.revision
        startedAt = entry.createdAt
        endedAt = entry.active ? nil : entry.createdAt.addingTimeInterval(entry.duration)
        duration = entry.duration
        language = entry.locale
        device = entry.source.rawValue
        title = entry.title
        bookmarks = entry.marks.map { Bookmark(at: $0) }
        // Persona's echo goes marked to a hub that keeps it out of the minutes itself, and not at all to one that doesn't.
        segments = entry.markedSentences.compactMap { s, echo in
            let text = s.text.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty, !echo || hubEcho else { return nil }
            return Segment(start: s.start, end: s.end, text: text, echo: echo ? true : nil)
        }
        if hubEcho, let spans = entry.attendance?.spoken, !spans.isEmpty { spoken = spans.map(Spoken.init) }
        complete = entry.transcribedAll
        kind = entry.isMeeting ? "meeting" : "note"
        if let meeting = entry.meeting, meeting.eventID != nil || meeting.title != nil {
            calendar = CalendarHint(title: meeting.title, attendees: meeting.attendees.isEmpty ? nil : meeting.attendees,
                                    location: meeting.location)
        }
        if let a = Self.attendance(entry) {
            if a.room {
                room = RoomUpload(attendance: a.role.rawValue)
                privateMark = a.privateMark ?? false
                if let mode = a.transcription {
                    transcription = mode.rawValue
                    if mode == .hub { timeline = MeetingTimeline(a) }
                }
            } else {
                // Never an attending role without an explicit target (#3862, the owner's rule after a real meeting): the hub
                // reads `attending_session` left out as the main chat, so with no session named and the main chat not
                // picked for this meeting the role goes as off (`sentRole`), and the meeting's words never fill the main
                // chat by default.
                let role = a.sentRole
                attendance = role.rawValue
                if role.attends, let key = a.key { attendingSession = SessionRef(machineID: key.machineID, sessionID: key.sessionID) }
            }
        }
    }

    /// Persona is saying an answer now (`MeetingSpeaker.openSpan`, or the meeting call's voice: `MeetingCall.openSpan`):
    /// the lines its stretch so far echoes are marked (left out, to a hub without `meeting_echo`), and the stretch goes
    /// with the others.
    mutating func speaking(_ open: RecordingSpan) {
        let echoes = { (s: Segment) in s.echo != true && open.echoes(RecordingSentence(start: s.start, end: s.end, text: s.text)) }
        guard marksEcho else {
            segments.removeAll(where: echoes)
            return
        }
        for i in segments.indices where echoes(segments[i]) { segments[i].echo = true }
        spoken = (spoken ?? []) + [Spoken(open)]
    }

    /// The attendance an upload of `entry` carries: a phone meeting's, once Persona was asked to attend it.
    static func attendance(_ entry: RecordingEntry) -> RecordingAttendance? {
        guard entry.isMeeting, entry.source == .phone else { return nil }
        return entry.attendance
    }

    /// What the hub must have stored before live lines and questions go (`role|session`; `room|role` for a room meeting).
    /// The role as sent (`sentRole`): a meeting sent as off settles as off, so nothing waits on a role the hub never got.
    static func attendanceMark(_ entry: RecordingEntry) -> String? {
        attendance(entry).map { a in
            let role = a.sentRole
            return a.room ? "room|\(role.rawValue)" : "\(role.rawValue)|\(role.attends ? a.session ?? "" : "")"
        }
    }
}

/// Keeps the knowledge base's copy of each recording's transcript in step with this device: uploads every new revision
/// (partial transcripts too), deletes what is deleted here, retries with backoff while the hub is away, and drops
/// recordings (audio included) once they are older than the hub's retention (`days`, 14 by default).
/// What was sent is kept in `sync.json` beside the recordings, so a relaunch only sends what changed.
@Observable
final class RecordingSync {
    static let shared = RecordingSync()

    nonisolated struct Store: Codable, Equatable, Sendable {
        /// The revision the hub has (stored, or newer than ours: stale).
        var synced: [UUID: Int64] = [:]
        /// Revisions the hub refused (400): not sent again until the recording changes.
        var rejected: [UUID: Int64] = [:]
        /// Deleted here, the hub not told yet.
        var deletes: Set<UUID> = []
        /// The hub's retention in days (`GET /api/v1/recordings`).
        var days = RecordingSync.defaultDays
        /// `days` came from the hub at least once: until then nothing here is dropped for its age (the hub may keep
        /// recordings longer than the default).
        var daysKnown = false
        /// All-day recordings: the hub ids of each day's parts past its first (whose id is the day's own), so deleting the
        /// day deletes every part there (docs/assistant/day-recap.md "Parts"). nil in a file from before.
        var dayParts: [UUID: [UUID]]? = nil
    }

    enum Status: Equatable {
        /// Nothing to upload yet (no text).
        case waiting
        case pending
        case uploading
        case synced
        case rejected
        case expired
    }

    nonisolated static let defaultDays = 14

    private(set) var store: Store
    private(set) var uploading: UUID?
    /// The attendance each recording's last stored upload carried, as the hub said it took it (`attendanceMark`): live
    /// lines and questions wait for it (docs/assistant/meeting-minutes.md「Live sentences」).
    private(set) var attendanceStored: [UUID: String] = [:]
    /// Why the hub stored a recording's attendance as off (`attendance_problem`), when it said.
    private(set) var attendanceProblem: [UUID: String] = [:]
    /// Room meetings (#3862) whose last upload `200` had no `room`: an older manager after a rollback, which stored the
    /// meeting with Persona off. Not for good: the uploads keep carrying `room`, and no live line or question goes until
    /// a `200` has it again.
    private(set) var roomMissing: Set<UUID> = []
    /// Recordings the hub has stored an upload with `ended_at` of in this run: until then it answers the follow-up
    /// `409 meeting_running`.
    private(set) var endedStored: Set<UUID> = []
    /// Called after an upload of an attending recording was stored: its live lines can go.
    @ObservationIgnored var onAttendanceStored: ((UUID) -> Void)?
    /// The `room` (and `private`, when there) of every upload `200` of a room meeting (`MeetingConversation.start`).
    @ObservationIgnored var onRoom: ((UUID, MeetingRoom, Bool?) -> Void)?
    /// The `transcription` (and `transcription_problem`) of every upload `200` of a meeting that named a mode (#3862 phase
    /// 5, `MeetingTranscription.start`), before the attendance it carried counts as stored: in hub mode no live line goes.
    @ObservationIgnored var onTranscription: ((RecordingEntry, MeetingTranscriptionRule.Answer) -> Void)?
    /// Every save of a recording, after this sync's own reaction to it (the live log of a meeting Persona attends).
    @ObservationIgnored var onChange: ((UUID) -> Void)?
    /// What a recording may send changed: a save (its role, Meeting / Note, the phone stopping attending: `stopped`) or
    /// `roomMissing`. The segment length of the one being recorded follows it (`MeetingRecorder.followSegments`, #3862
    /// phase 2: 15-second segments only while its lines go to the room).
    @ObservationIgnored var onSegments: ((UUID) -> Void)?
    /// Uploaded first, without the save debounce: a recording whose attendance the hub doesn't have yet.
    @ObservationIgnored private var urgent: Set<UUID> = []
    /// Configuration refusals need a foreground/manual retry, not a timer or another transcript save.
    private(set) var automaticRetryPaused = false
    private var lastProblem: String?
    /// Pending deletes are durable and visible even after a relaunch; a local delete is not a remote acknowledgement.
    var problem: String? {
        guard !store.deletes.isEmpty else { return lastProblem }
        return [lastProblem, String(localized: "Pending deletions have not been removed from Knowledge yet.")].compactMap { $0 }.joined(separator: " ")
    }

    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let api: () -> APIClient?
    /// The connected hub's `hello.features` (empty while there is none).
    @ObservationIgnored private let hubFeatures: () -> Set<String>
    @ObservationIgnored private let file: URL
    @ObservationIgnored private let now: () -> Date
    /// All-day recordings go their own way (`RecordingDaySync`): this hands them over.
    @ObservationIgnored private let daySync: () -> RecordingDaySync
    /// The hub's `recording_days` changed: the watch is told how long it may keep audio (`recordingDayKeepDays`).
    @ObservationIgnored private let recordingDaysChanged: (Int) -> Void
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var again = false
    /// `worker` is only waiting out a backoff: a new reason to sync cuts it short.
    @ObservationIgnored private var backingOff = false
    /// `worker` is only waiting out the save debounce.
    @ObservationIgnored private var debouncing = false
    @ObservationIgnored private var failures = 0
    @ObservationIgnored private var lastReconcile: Date?
    @ObservationIgnored private var started = false

    init(library: RecordingLibrary = .shared, api: @escaping () -> APIClient? = { AppModel.shared.api },
         now: @escaping () -> Date = { .now }, daySync: @escaping () -> RecordingDaySync = { RecordingDaySync.shared },
         recordingDaysChanged: @escaping (Int) -> Void = { RecordingDayPhoneCapabilities.shared.recordingDaysChanged($0) },
         hubFeatures: @escaping () -> Set<String> = { AppModel.shared.hubFeatures }) {
        self.library = library; self.api = api; self.now = now; self.daySync = daySync; self.hubFeatures = hubFeatures
        self.recordingDaysChanged = recordingDaysChanged
        file = library.root.appendingPathComponent("sync.json")
        store = (try? Data(contentsOf: file)).flatMap { try? JSONDecoder().decode(Store.self, from: $0) } ?? Store()
    }

    /// From `PhoneRecordingReceiver.start`: every save is a reason to sync, and so is coming back to the foreground.
    func start() {
        guard !started else { return }
        started = true
        library.didChange = { [weak self] id in
            guard let self else { return }
            // A day's index row (its language, its end): the day's own upload, never this one's pass.
            if self.library.entry(id)?.isDay == true {
                self.daySync().changed(id)
            } else {
                self.kick()
            }
            self.onSegments?(id)
            self.onChange?(id)
        }
        #if canImport(UIKit) && !os(watchOS)
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            Task { @MainActor in RecordingSync.shared.kick(reconcile: true) }
        }
        #endif
        purgeExpired()
        kick(reconcile: true)
    }

    func status(_ entry: RecordingEntry) -> Status {
        // An all-day recording's text isn't in its entry (`RecordingDaySummaries` has its state): nothing here to send.
        if entry.isDay { return .waiting }
        if isExpired(entry) { return .expired }
        if uploading == entry.id { return .uploading }
        if let have = store.synced[entry.id], have >= entry.revision { return .synced }
        if store.rejected[entry.id] == entry.revision { return .rejected }
        return Self.uploadable(entry) ? .pending : .waiting
    }

    /// The hub this upload goes to takes Persona's echo marked (`meeting_echo`, #5905), by its own `hello` only. Before
    /// that hello (an upload at launch, another hub just picked) nothing says so, and a "yes" kept from the last hub heard
    /// may be another hub's: the echo lines are left out, as for an older hub. That loses only Persona's own words on the
    /// hub's copy, never the meeting's, and an older hub never gets them into its minutes.
    private func hubEcho() -> Bool {
        hubFeatures().contains(MeetingAttendanceDefaults.echoFeature)
    }

    /// Text worth sending: at least one sentence (an empty transcript is noise in the knowledge base). A meeting Persona
    /// attends goes before it has any, so the hub knows the role (the contract's first exception).
    nonisolated static func uploadable(_ entry: RecordingEntry) -> Bool {
        guard !entry.isDay else { return false }
        if RecordingUpload.attendance(entry) != nil { return true }
        return entry.sentences.contains { !$0.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    }

    /// The revision of `id` the hub last stored (ms, the entry's `revision` when it went), or nil when none was. The meeting
    /// page counts the hub's 12 hours from it: a meeting whose `ended_at` never landed ends then (#3862).
    func storedRevision(_ id: UUID) -> Int64? { store.synced[id] }

    /// The hub has stored the attendance `entry` carries now.
    func attendanceSettled(_ entry: RecordingEntry) -> Bool {
        guard let mark = RecordingUpload.attendanceMark(entry) else { return false }
        return attendanceStored[entry.id] == mark
    }

    /// The attendance of `id` changed (started, turned on or off, another session): it goes now, ahead of the others.
    func attendanceChanged(_ id: UUID) {
        urgent.insert(id)
        kick(now: true)
    }

    /// The hub doesn't have this recording with its attendance (`recording_not_found`, `not_attending`): it goes again now,
    /// even if this phone thinks it was stored.
    func reupload(_ id: UUID) {
        store.synced[id] = nil
        attendanceStored[id] = nil
        attendanceChanged(id)
    }

    /// Past the hub's retention. An all-day recording never is here: `RecordingDayKeeper` purges it by its day, whether
    /// or not it is still open.
    func isExpired(_ entry: RecordingEntry) -> Bool {
        store.daysKnown && !entry.isDay && !entry.active && entry.createdAt < now().addingTimeInterval(-Double(store.days) * 86_400)
    }

    /// Deleted by the user: gone here at once (audio and text), and from the knowledge base when the hub is reachable.
    /// A day recording goes whole (docs/assistant/day-recap.md「Deleting」), also when the hub deleted one of its parts:
    /// every part of it from the hub, its transcription and upload here, and the watch's copy (`RecordingDaySync.removedHere`).
    /// `day`: the caller knows it is a day recording (the day upload's `410`), also when the library has no row for it
    /// yet (one is written only after its first chunk was stored): without the row, nothing would ask the watch.
    func delete(_ id: UUID, day isDay: Bool = false) throws {
        let day = isDay || library.entry(id)?.isDay == true
        try library.remove(id)
        store.deletes.insert(id)
        if day {
            store.deletes.formUnion(store.dayParts?[id] ?? [])
            store.dayParts?[id] = nil
            daySync().removedHere(id)
        }
        forget(id)
        save()
        kick()
    }

    /// A day's hub ids past its first part (`RecordingDaySync` starts one on `413 day_full`), kept for `delete`.
    func noteDayParts(_ day: UUID, _ hubIDs: [UUID]) {
        guard store.dayParts?[day] != hubIDs else { return }
        var parts = store.dayParts ?? [:]
        parts[day] = hubIDs
        store.dayParts = parts
        save()
    }

    /// Runs a pass soon (several saves in a row become one upload). `reconcile`: also ask the hub what it has, at most
    /// every ten minutes.
    func kick(reconcile: Bool = false, now immediately: Bool = false) {
        if reconcile, automaticRetryPaused { automaticRetryPaused = false; lastReconcile = nil }
        guard !automaticRetryPaused else { return }
        if reconcile, lastReconcile.map({ now().timeIntervalSince($0) > 600 }) ?? true { lastReconcile = nil }
        if backingOff { worker?.cancel(); worker = nil; backingOff = false }
        // Waiting out the save debounce: now is now (a pass already under way takes the urgent ones next).
        if immediately, debouncing { worker?.cancel(); worker = nil; debouncing = false }
        guard worker == nil else { again = true; return }
        debouncing = !immediately
        worker = Task { [weak self] in
            if !immediately { try? await Task.sleep(for: .seconds(1)) }
            guard !Task.isCancelled else { return }
            self?.debouncing = false
            await self?.runPasses()
        }
    }

    /// One pass now, for tests (no debounce, no retry timer).
    func syncNow(retryPaused: Bool = false) async {
        if retryPaused { automaticRetryPaused = false; lastReconcile = nil }
        await pass()
    }

    private func runPasses() async {
        repeat {
            again = false
            await pass()
        } while again && !automaticRetryPaused && !Task.isCancelled
        worker = nil
        guard needsWork, !automaticRetryPaused, api()?.config.isDemo == false else { failures = 0; return }
        // The hub was away or refused for now: try again later, longer each time (5 s … 10 min).
        failures += 1
        let delay = min(600, 5 * pow(2, Double(min(failures, 8) - 1)))
        backingOff = true
        worker = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, let self else { return }
            self.backingOff = false
            self.worker = nil
            self.kick()
        }
    }

    private var needsWork: Bool {
        !store.deletes.isEmpty || library.entries.contains { status($0) == .pending }
    }

    private func pass() async {
        guard !automaticRetryPaused else { return }
        purgeExpired()
        guard let api = api(), !api.config.isDemo else {
            lastProblem = api() == nil ? nil : String(localized: "Demo mode doesn't sync recordings")
            return
        }
        if lastReconcile == nil {
            guard await reconcile(api) else { return }
            lastReconcile = now()
        }
        // A meeting whose attendance the hub doesn't have yet goes first, also between the others: its live lines and
        // questions wait for it.
        guard await uploadUrgent(api) else { return }
        for id in store.deletes.sorted(by: { $0.uuidString < $1.uuidString }) {
            guard await sendDelete(id, api) else { return }
        }
        for entry in library.entries where status(entry) == .pending {
            guard await uploadUrgent(api) else { return }
            guard let latest = library.entry(entry.id), status(latest) == .pending else { continue }
            guard await upload(latest, api) else { return }
        }
        lastProblem = nil
    }

    /// `GET /api/v1/recordings`: the hub's retention, what it already has, and what was deleted elsewhere.
    private func reconcile(_ api: APIClient) async -> Bool {
        do {
            let answer: ReconcileAnswer = try await api.get("/api/v1/recordings")
            if let days = answer.days, (1...365).contains(days), !store.daysKnown || store.days != days {
                store.days = days
                store.daysKnown = true
                recordingDaysChanged(days)
            }
            let rows = answer.recordings ?? []
            let have = Dictionary(rows.filter { $0.kind != "day" }.compactMap { r in UUID(uuidString: r.id).map { ($0, r.revision) } },
                                  uniquingKeysWith: max)
            // The hub's word replaces ours: one it lost (a new hub) goes again, one it has newer is not resent.
            store.synced = have.filter { library.entry($0.key) != nil && library.entry($0.key)?.isDay != true }
            handDays(rows, deleted: (answer.deleted ?? []).compactMap(UUID.init(uuidString:)))
            for id in (answer.deleted ?? []).compactMap(UUID.init(uuidString:)) where library.entry(id).map({ !$0.isDay }) == true {
                deletedOnHub(id)
            }
            save()
            purgeExpired()
            return true
        } catch {
            note(error)
            return false
        }
    }

    private nonisolated struct ReconcileAnswer: Decodable, Sendable {
        nonisolated struct Row: Decodable, Sendable {
            var id: String
            var revision: Int64
            // All-day recordings (docs/assistant/day-recap.md "Reconciliation").
            var kind: String?
            var part: Int?
            var next: Int?
            var base: Int64?
            var recapState: String?
            var recapFinal: Bool?
            var recapFinalAt: String?

            private nonisolated enum CodingKeys: String, CodingKey {
                case id, revision, kind, part, next, base
                case recapState = "recap_state", recapFinal = "recap_final", recapFinalAt = "recap_final_at"
            }

            init(from decoder: Decoder) throws {
                // Tolerant past `id` and `revision`: one odd field of a day row must not cost the whole list.
                let c = try decoder.container(keyedBy: CodingKeys.self)
                id = try c.decode(String.self, forKey: .id)
                revision = try c.decode(Int64.self, forKey: .revision)
                kind = try? c.decodeIfPresent(String.self, forKey: .kind)
                part = try? c.decodeIfPresent(Int.self, forKey: .part)
                next = try? c.decodeIfPresent(Int.self, forKey: .next)
                base = try? c.decodeIfPresent(Int64.self, forKey: .base)
                recapState = try? c.decodeIfPresent(String.self, forKey: .recapState)
                recapFinal = try? c.decodeIfPresent(Bool.self, forKey: .recapFinal)
                recapFinalAt = try? c.decodeIfPresent(String.self, forKey: .recapFinalAt)
            }
        }

        var days: Int?
        var recordings: [Row]?
        var deleted: [String]?
    }

    /// Day recordings go to their own upload: each local day with the rows of its parts (none when the hub listed none).
    /// A day one of whose parts the hub deleted goes whole, everywhere (docs/assistant/day-recap.md「Deleting」).
    private func handDays(_ rows: [ReconcileAnswer.Row], deleted: [UUID]) {
        let days = library.entries.filter(\.isDay).map(\.id)
        // Drop the part lists of days that are gone here.
        if let parts = store.dayParts { store.dayParts = parts.filter { library.entry($0.key) != nil } }
        guard !days.isEmpty else { return }
        var owner: [UUID: UUID] = [:]
        for day in days {
            owner[day] = day
            for part in store.dayParts?[day] ?? [] { owner[part] = day }
        }
        var byDay: [UUID: [RecordingDayHubRow]] = Dictionary(uniqueKeysWithValues: days.map { ($0, []) })
        for row in rows {
            guard let id = UUID(uuidString: row.id), let day = owner[id] else { continue }
            byDay[day, default: []].append(RecordingDayHubRow(id: id, revision: row.revision, part: row.part, next: row.next,
                                                              base: row.base, recapState: row.recapState, recapFinal: row.recapFinal,
                                                              recapFinalAt: row.recapFinalAt.flatMap { RecordingDayWire.date($0) }))
        }
        let gone = Set(deleted.compactMap { owner[$0] })
        for day in gone { byDay[day] = nil }
        daySync().reconciled(byDay)
        for day in gone.sorted(by: { $0.uuidString < $1.uuidString }) { try? delete(day) }
    }

    private func uploadUrgent(_ api: APIClient) async -> Bool {
        for id in urgent.sorted(by: { $0.uuidString < $1.uuidString }) {
            urgent.remove(id)
            guard let entry = library.entry(id), status(entry) == .pending else { continue }
            guard await upload(entry, api) else { urgent.insert(id); return false }
        }
        return true
    }

    private func upload(_ entry: RecordingEntry, _ api: APIClient) async -> Bool {
        struct Answer: Decodable {
            var revision: Int64?; var status: String?; var attendance: String?; var attendanceProblem: String?
            enum CodingKeys: String, CodingKey { case revision, status, attendance, attendanceProblem = "attendance_problem" }
        }
        /// A room meeting's part of the answer (#3862), read on its own so the rest reads exactly as before; a field of
        /// another shape is left out.
        struct RoomAnswer: Decodable {
            var room: MeetingRoom?; var privateMark: Bool?
            enum CodingKeys: String, CodingKey { case room, privateMark = "private" }
            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                room = try? c.decodeIfPresent(MeetingRoom.self, forKey: .room)
                privateMark = try? c.decodeIfPresent(Bool.self, forKey: .privateMark)
            }
        }
        /// The mode in effect (#3862 phase 5), read on its own: a field of another shape reads as absent.
        struct TranscriptionAnswer: Decodable {
            var transcription: String?; var problem: String?
            enum CodingKeys: String, CodingKey { case transcription, problem = "transcription_problem" }
            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                transcription = try? c.decodeIfPresent(String.self, forKey: .transcription)
                problem = try? c.decodeIfPresent(String.self, forKey: .problem)
            }
        }
        uploading = entry.id
        defer { uploading = nil }
        var body = RecordingUpload(entry, hubEcho: hubEcho())
        #if os(iOS)
        // Persona is saying an answer in this meeting right now: what the microphone hears of it isn't the meeting's.
        // The meeting call's voice (#3862 phase 4) as well: each open stretch, with what it is saying, marks its echo.
        for open in [MeetingSpeaker.shared.openSpan(for: entry.id), MeetingCall.shared.openSpan(for: entry.id)].compactMap({ $0 }) {
            body.speaking(open)
        }
        #endif
        do {
            let data = try await api.request("PUT", "/api/v1/recordings/\(entry.id.uuidString.lowercased())", body: body, timeout: 60)
            let answer = try? JSON.decoder.decode(Answer.self, from: data)
            // Deleted while it was on its way: the delete that follows removes it there too.
            guard library.entry(entry.id) != nil else { return true }
            switch answer?.status {
            case "expired":
                if let latest = library.entry(entry.id), isExpired(latest) { purge(latest.id) }
                store.synced[entry.id] = body.revision // the hub will not keep it; nothing more to send
            default: // stored, stale (it has a newer one)
                store.synced[entry.id] = max(body.revision, answer?.revision ?? 0)
            }
            store.rejected[entry.id] = nil
            save()
            if body.endedAt != nil, !endedStored.contains(entry.id) { endedStored.insert(entry.id) }
            if body.transcription != nil {
                // Absent from the answer (or no answer to read): an older manager, which transcribes nothing.
                let mode = try? JSON.decoder.decode(TranscriptionAnswer.self, from: data)
                onTranscription?(library.entry(entry.id) ?? entry,
                                 MeetingTranscriptionRule.Answer(mode: mode?.transcription, problem: mode?.problem))
            }
            if body.room != nil {
                let room = try? JSON.decoder.decode(RoomAnswer.self, from: data)
                roomAnswered(entry, sent: body, answer?.attendance, problem: answer?.attendanceProblem, room: room?.room,
                             privateMark: room?.privateMark)
            } else if body.attendance != nil {
                attendanceAnswered(entry, sent: body, answer?.attendance, problem: answer?.attendanceProblem)
            }
            return true
        } catch let error as APIError where error.status == 410 {
            deletedOnHub(entry.id)
            save()
            return true
        } catch let error as APIError where error.status == 400 || error.status == 413 {
            store.rejected[entry.id] = body.revision
            save()
            lastProblem = String(localized: "Knowledge didn't accept a recording: \(error.message)")
            return true
        } catch {
            note(error)
            return false
        }
    }

    /// What the hub said about the attendance an upload carried. A `200` without `attendance` is an older manager: it took
    /// the transcript and nothing else, so the phone stops attending this meeting (the contract's rule).
    private func attendanceAnswered(_ entry: RecordingEntry, sent body: RecordingUpload, _ took: String?, problem: String?) {
        guard let took else {
            if body.attendance != MeetingAttendance.off.rawValue {
                library.change(entry.id) { e in
                    guard var a = e.attendance else { return }
                    a.set(.off, at: e.duration)
                    a.stopped = .unsupported
                    e.attendance = a
                }
            }
            attendanceStored[entry.id] = nil
            return
        }
        attendanceProblem[entry.id] = problem
        guard let mark = RecordingUpload.attendanceMark(entry) else { return }
        // Stored as asked, or as off (a problem): either way what the hub holds now.
        attendanceStored[entry.id] = took == body.attendance ? mark : "\(took)|"
        if took == body.attendance, took != MeetingAttendance.off.rawValue { onAttendanceStored?(entry.id) }
    }

    /// What the hub said about a room meeting's upload (#3862). A `200` without `room` is an older manager after a
    /// rollback: unlike #2981's rule nothing stops for good; the meeting just waits (no live lines, no questions) until a
    /// `200` carries `room` again.
    private func roomAnswered(_ entry: RecordingEntry, sent body: RecordingUpload, _ took: String?, problem: String?,
                              room: MeetingRoom?, privateMark: Bool?) {
        guard let room else {
            if !roomMissing.contains(entry.id) {
                roomMissing.insert(entry.id)
                onSegments?(entry.id)
            }
            attendanceStored[entry.id] = nil
            return
        }
        if roomMissing.contains(entry.id) {
            roomMissing.remove(entry.id)
            onSegments?(entry.id)
        }
        attendanceProblem[entry.id] = problem
        onRoom?(entry.id, room, privateMark)
        guard let mark = RecordingUpload.attendanceMark(entry), let sent = body.room?.attendance else { return }
        // Stored as asked, or as another role (a problem): either way what the hub holds now.
        attendanceStored[entry.id] = took == sent ? mark : "room|\(took ?? "")"
        if took == sent, sent != MeetingAttendance.off.rawValue { onAttendanceStored?(entry.id) }
    }

    private func sendDelete(_ id: UUID, _ api: APIClient) async -> Bool {
        do {
            _ = try await api.request("DELETE", "/api/v1/recordings/\(id.uuidString.lowercased())")
        } catch let error as APIError where error.status == 410 {
            // The hub confirms it was already deleted. An unsupported 404 does not confirm a remote deletion.
        } catch {
            note(error)
            return false
        }
        store.deletes.remove(id)
        save()
        return true
    }

    /// Deleted in the knowledge base (the web, another device): the text there is the truth, so it goes here too.
    private func deletedOnHub(_ id: UUID) {
        RecordingTranscription.shared.cancel(id)
        try? library.remove(id)
        forget(id)
    }

    /// Past the retention: the hub drops it, and the audio here goes with it.
    func purgeExpired() {
        let old = library.entries.filter(isExpired)
        guard !old.isEmpty else { return }
        for entry in old { purge(entry.id) }
        save()
    }

    private func purge(_ id: UUID) {
        RecordingTranscription.shared.cancel(id)
        try? library.remove(id)
        forget(id)
    }

    private func forget(_ id: UUID) {
        store.synced[id] = nil
        store.rejected[id] = nil
        attendanceStored[id] = nil
        attendanceProblem[id] = nil
        if roomMissing.contains(id) { roomMissing.remove(id) }
        if endedStored.contains(id) { endedStored.remove(id) }
    }

    private func note(_ error: Error) {
        if error.isCancellation { return }
        if let e = error as? APIError {
            automaticRetryPaused = e.code == "kb_disabled" || e.code == "kb_read_only"
                || (400..<500).contains(e.status) && e.status != 408 && e.status != 429
            if e.businessMessage != nil {
                lastProblem = e.localizedDescription
                // Use the shared classification, including a request id, without raw internal diagnostics.
            } else if e.status == 404 {
                lastProblem = String(localized: "Recording sync isn't supported by this hub. Update the hub, then reopen the app to retry.")
            } else if e.status == 401 || e.status == 403 {
                lastProblem = String(localized: "Recording sync needs access. Check this device's permissions and pairing, then reopen the app to retry.")
            } else {
                lastProblem = String(localized: "Recording sync couldn't finish. Try again after checking the hub's diagnostics.")
            }
        } else if error.isTimeout {
            lastProblem = String(localized: "The request timed out. Try again later.")
        } else if error.isOffline {
            lastProblem = String(localized: "Can't reach the hub. Recording text will sync automatically later.")
        } else {
            lastProblem = String(localized: "Recording sync couldn't finish. Try again after checking the hub's diagnostics.")
        }
    }

    private func save() {
        do { try JSONEncoder().encode(store).write(to: file, options: .atomic) } catch { lastProblem = String(localized: "Sync record wasn't saved") }
    }
}
