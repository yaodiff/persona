import Foundation

/// Persona's part in a meeting being recorded (#2981; the contract is docs/assistant/meeting-minutes.md「Attending a
/// meeting」, the phone's side docs/apple/recording.md「Persona in meetings」): off, listening in, or taking part. Chosen
/// each time a meeting recording starts, pre-selected from the hub's `meeting_attendance_default`. Offered only by a hub
/// that lists the `meeting_attendance` feature; only a phone's meeting recording has one (notes and watch recordings never).
nonisolated enum MeetingAttendance: String, Codable, CaseIterable, Sendable {
    /// Today's behaviour: the hub writes minutes after the meeting, and nothing reaches it while the meeting goes on.
    case off
    /// Persona follows the meeting and answers on screen only. It never speaks.
    case listener
    /// Persona also says its answers out loud, through a connected headset or the loudspeaker.
    case participant

    var attends: Bool { self != .off }
    var speaks: Bool { self == .participant }

    /// The segmented choice's word.
    var title: String {
        switch self {
        case .off: String(localized: "Off")
        case .listener: String(localized: "Listen [Meeting Role]")
        case .participant: String(localized: "Take Part [Meeting Role]")
        }
    }

    /// One plain sentence on what the choice does.
    var detail: String {
        switch self {
        case .off: String(localized: "Persona stays out of the meeting. The minutes come after it ends.")
        case .listener: String(localized: "Persona follows the meeting. Ask it from this phone; it answers on screen and never speaks.")
        case .participant: String(localized: "Persona follows the meeting and answers out loud when you tap Ask Persona.")
        }
    }
}

/// The attendance of one meeting recording, kept with the recording on this phone: the role, the session that attends,
/// the phone's live log of what it sent the hub, and the stretches Persona spoke in.
nonisolated struct RecordingAttendance: Codable, Hashable, Sendable {
    var role: MeetingAttendance
    /// The attending session's `SessionKey.path`; nil is the main chat only when `mainPicked`, else nobody chosen yet.
    var session: String?
    /// Its name when it was chosen.
    var sessionTitle: String?
    /// The stretches of the recording's clock the role was listener or participant: only a sentence that ends inside
    /// one enters the live log (the contract: what is said while attending is off is never sent). The last one is open
    /// (`end` nil) while attending.
    var periods: [RecordingPeriod] = []
    /// The phone's append-only live log (`POST /api/v1/recordings/{id}/live`): a sentence's position is its index here,
    /// never renumbered. A re-transcription or an echo line kept out of it (`echo`) changes nothing already in it.
    var live: [RecordingSentence] = []
    /// The segments whose sentences were already looked at for the log, so a segment transcribed again adds nothing.
    var liveSegments: [Int] = []
    /// Where on the recording Persona said an answer out loud, and what it said: the microphone may have heard it, so a
    /// sentence that overlaps one and says its words is Persona's echo, not the meeting's (`echo`, #5905): hidden in the
    /// transcript, kept out of the live log, uploaded marked (or left out, to a hub without `meeting_echo`).
    var spoken: [RecordingSpan] = []
    /// Why the phone stopped attending on its own, said on the recording: the hub can't follow meetings (an older
    /// manager), or it takes no more live lines for this one.
    var stopped: StopReason?
    /// A room meeting (#3862): started while the hub listed `meeting_conversation`. Its uploads carry `room` and `private`
    /// (never the top-level `attendance` / `attending_session`); it stays one for life.
    var room: Bool = false
    /// Started while the hub listed `meeting_photos` (phase 3): the page offers the camera and the library.
    var photos: Bool = false
    /// Started while the hub listed `meeting_live_view` (phase 2): the page shows the room's topics, the Live Activity the
    /// current topic and the latest line, and while Persona attends the recording writes 15-second segments.
    var liveView: Bool = false
    /// Started while the hub listed `meeting_highlights` beside `meeting_live_view` (phase 2b): the page's top card shows
    /// the room's highlights once it set any, and `GET …/live` reads them. Only ever with `liveView`.
    var highlights: Bool = false
    /// The start sheet's Private meeting choice, sent as the upload's `private` (the hub takes it only while it has no mark).
    var privateMark: Bool? = nil
    /// The owner picked the main chat to attend this meeting (#3862): the only way it ever attends one. A meeting's words
    /// fill the chat they are fed to, so the main chat never gets them by default; no session named without this pick
    /// means nobody was chosen, and the meeting goes as off (`sentRole`).
    var mainPicked: Bool = false
    /// The transcription mode the meeting asked for as it started (#3862 phase 5, docs/assistant/meeting-minutes.md
    /// 「Choosing the mode」), taken only while the hub listed `meeting_transcription` beside `meeting_conversation` and
    /// `meeting_live_view`: the flag's snapshot, kept for life. Its uploads name it (`transcription`); nil: the hub
    /// didn't list it, and they name no mode (the hub's `device`). Which mode is in effect is the upload's `200`'s word
    /// (`MeetingTranscription`): never `hub` unless asked for here, so a meeting's audio leaves the phone only by choice.
    var transcription: MeetingTranscriptionMode? = nil
    /// Every role this meeting had, with where it began on the recording's clock (the first at the start): the hub's
    /// timeline in hub mode (`timeline.roles`, the audio socket's `start` and `role`). Kept only for a meeting that asked
    /// for a transcription mode (`transcription`), so every other attendance stays as it was.
    var roles: [RecordingRoleChange] = []
    /// The stretches of the recording's clock that were hub-mode meeting time (#3862 phase 5): one opens as a meeting
    /// that asked for `hub` starts or as the recording becomes one (`openHubTime`), and closes when it turns into a Note,
    /// when an older manager rolls it back, and at Stop (`closeHubTime`); the last is open (`end` nil) while it goes on.
    /// Only audio inside them ever leaves the phone after the meeting (the gap uploads): what was recorded as a Note, or
    /// after a rollback, stays here whatever the hub lacks. Empty: none, so nothing is uploaded.
    var hubTime: [RecordingPeriod] = []
    /// An older manager answered an upload of this meeting (a `200` without `transcription`, docs/assistant/
    /// meeting-minutes.md「Choosing the mode」): `device` for the rest of the recording, kept with it so a relaunch with the
    /// newer manager back never streams or uploads its audio again.
    var hubRolledBack = false
    /// Hub transcription stopped for this meeting (a fatal `error` the phone stops on, an unknown code included, or a
    /// refused socket; docs/assistant/meeting-minutes.md「Audio from the phone」): where on its clock, and the code. Kept with
    /// the recording, so its hub-mode time ends there for good, also after a relaunch: no socket, and no audio recorded
    /// from then on goes up, live or as a gap. Nil while it may run.
    var hubStopped: RecordingHubStop?

    nonisolated enum StopReason: String, Codable, Sendable {
        case unsupported
        case liveFull = "live_full"
    }

    init(role: MeetingAttendance, session: String? = nil, sessionTitle: String? = nil, mainPicked: Bool = false, at: Double = 0) {
        self.role = .off; self.session = session; self.sessionTitle = sessionTitle; self.mainPicked = mainPicked
        set(role, at: at)
    }

    /// A room meeting's attendance as it starts: no attending session (the meeting's own room attends), whatever the role.
    /// `highlights` counts only beside `liveView`, as the hub lists it only beside `meeting_live_view`. `transcription`:
    /// the mode asked for, when the hub lists `meeting_transcription` (phase 5); its role is the first of `roles`.
    static func room(_ role: MeetingAttendance, private privateMark: Bool, photos: Bool, liveView: Bool = false,
                     highlights: Bool = false, transcription: MeetingTranscriptionMode? = nil, at: Double = 0) -> RecordingAttendance {
        var a = RecordingAttendance(role: role, at: at)
        a.room = true
        a.photos = photos
        a.liveView = liveView
        a.highlights = liveView && highlights
        a.privateMark = privateMark
        a.transcription = transcription
        if transcription != nil { a.roles = [RecordingRoleChange(at: max(0, at), attendance: role)] }
        a.openHubTime(at: at)
        return a
    }

    enum CodingKeys: String, CodingKey {
        case role, session, sessionTitle, periods, live, liveSegments, spoken, stopped, room, photos, liveView, highlights,
             privateMark, mainPicked
        case transcription, roles, hubTime, hubRolledBack, hubStopped
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // A role a later app wrote and this one doesn't know reads as off: nothing attends.
        role = (try? c.decode(MeetingAttendance.self, forKey: .role)) ?? .off
        session = try c.decodeIfPresent(String.self, forKey: .session)
        sessionTitle = try c.decodeIfPresent(String.self, forKey: .sessionTitle)
        periods = try c.decodeIfPresent([RecordingPeriod].self, forKey: .periods) ?? []
        live = try c.decodeIfPresent([RecordingSentence].self, forKey: .live) ?? []
        liveSegments = try c.decodeIfPresent([Int].self, forKey: .liveSegments) ?? []
        spoken = try c.decodeIfPresent([RecordingSpan].self, forKey: .spoken) ?? []
        stopped = try? c.decodeIfPresent(StopReason.self, forKey: .stopped)
        // Written by #3862 apps; an older file has none, and is no room meeting.
        room = (try? c.decodeIfPresent(Bool.self, forKey: .room)) ?? false
        photos = (try? c.decodeIfPresent(Bool.self, forKey: .photos)) ?? false
        liveView = (try? c.decodeIfPresent(Bool.self, forKey: .liveView)) ?? false
        // Written since phase 2b; a file from before has none, and its meeting shows no highlights.
        highlights = (try? c.decodeIfPresent(Bool.self, forKey: .highlights)) ?? false
        privateMark = try? c.decodeIfPresent(Bool.self, forKey: .privateMark)
        // Written since #3862; a file from before has none: its main chat was never picked, so it goes as off.
        mainPicked = (try? c.decodeIfPresent(Bool.self, forKey: .mainPicked)) ?? false
        // Phase 5: a mode a later app wrote and this one doesn't know names none (the upload sends no mode).
        transcription = try? c.decodeIfPresent(MeetingTranscriptionMode.self, forKey: .transcription)
        roles = (try? c.decodeIfPresent([RecordingRoleChange].self, forKey: .roles)) ?? []
        // None read: no hub-mode time, so none of its audio is ever uploaded after the meeting.
        hubTime = (try? c.decodeIfPresent([RecordingPeriod].self, forKey: .hubTime)) ?? []
        hubRolledBack = (try? c.decodeIfPresent(Bool.self, forKey: .hubRolledBack)) ?? false
        hubStopped = try? c.decodeIfPresent(RecordingHubStop.self, forKey: .hubStopped)
    }

    /// The recording is a meeting that asked for `hub` from `at` on (its start, or a Note turned into a meeting): a
    /// stretch of hub-mode time opens, unless one is open already, an older manager rolled the meeting back or hub
    /// transcription stopped for it.
    mutating func openHubTime(at: Double) {
        guard transcription == .hub, !hubRolledBack, hubStopped == nil, hubTime.last.map({ $0.end != nil }) ?? true else { return }
        hubTime.append(RecordingPeriod(start: max(0, at, hubTime.last?.end ?? 0)))
    }

    /// Hub-mode time ends at `at` (the recording turned into a Note, or stopped): the open stretch closes there.
    mutating func closeHubTime(at: Double) {
        guard let last = hubTime.indices.last, hubTime[last].end == nil else { return }
        hubTime[last].end = max(at, hubTime[last].start)
    }

    /// An older manager answered at `at`: hub-mode time ends, for the rest of the recording.
    mutating func rollBackHub(at: Double) {
        closeHubTime(at: at)
        hubRolledBack = true
    }

    /// Hub transcription stopped for this meeting at `at`, with `code`: hub-mode time ends there, for the rest of the
    /// recording (the first stop is kept).
    mutating func stopHub(at: Double, code: String) {
        closeHubTime(at: at)
        if hubStopped == nil { hubStopped = RecordingHubStop(at: max(0, at), code: code) }
    }

    /// The hub-mode stretches as `[from, to]` pairs on the recording's clock, an open one ending at `end` (the recording's
    /// length), none past where hub transcription stopped: what gap uploads are clipped to.
    func hubStretches(until end: Double) -> [(from: Double, to: Double)] {
        hubTime.compactMap { p in
            let to = min(p.end ?? end, end, hubStopped?.at ?? end)
            return to > p.start ? (p.start, to) : nil
        }
    }

    /// The attending session, nil for the main chat (when `mainPicked`) or nobody.
    var key: SessionKey? { session.flatMap(SessionKey.init(path:)) }

    /// Who attends is known: the meeting's own room, a session named (one the API can name: a malformed path such as ""
    /// would go as no `attending_session`, which the hub reads as the main chat), or the main chat picked for this meeting.
    var hasAttendee: Bool { room || key != nil || mainPicked }

    /// The role the hub is told and the phone acts on (the upload, its stored mark, live lines, asks): `role`, except an
    /// attending one with nobody chosen to attend, which goes as off. The structural half of "never the main chat by
    /// default" (#3862): `attending_session` left out means the main chat to the hub, so an attending role without a
    /// target is never sent, whatever built the attendance.
    var sentRole: MeetingAttendance { role.attends && !hasAttendee ? .off : role }

    /// Sets the role at `at` (seconds on the recording's clock), opening or closing the attending period; a meeting that
    /// asked for a transcription mode also keeps the change in `roles` (the hub's timeline).
    mutating func set(_ next: MeetingAttendance, at: Double) {
        let open = periods.last.map { $0.end == nil } ?? false
        if next.attends, !open { periods.append(RecordingPeriod(start: max(0, at))) }
        if !next.attends, open, let last = periods.indices.last { periods[last].end = max(at, periods[last].start) }
        if transcription != nil, roles.last?.attendance != next {
            roles.append(RecordingRoleChange(at: max(0, at, roles.last?.at ?? 0), attendance: next))
        }
        role = next
        if next.attends { stopped = nil }
    }

    /// The recording writes its short segments (#3862 phase 2, docs/assistant/meeting-minutes.md「Phase 2: live view」), so
    /// a line reaches the page and the room within about 15–30 seconds, in the foreground and the background
    /// (`RecordingAudioSpec.meetingLiveSegmentSeconds`): a room meeting started with the live view that Persona attends as
    /// the asks and the Live Activity see it (`MeetingAsk.canAsk`: its role attends, and the last upload's `200` named the
    /// room, not `roomMissing`, `RecordingSync.roomMissing`: an older manager, which takes no line or ask), and whose lines
    /// still go to the hub (`stopped` nil: after `live_full` questions still work, but no line reaches the room, so
    /// shorter segments would only cost battery). A new recording has no answer yet: `roomMissing` false.
    func shortSegments(roomMissing: Bool) -> Bool { room && liveView && sentRole.attends && stopped == nil && !roomMissing }

    /// The sentence ended while Persona attended.
    func attended(_ line: RecordingSentence) -> Bool {
        periods.contains { $0.holds(line.end) }
    }

    /// Persona's echo (#5905): the line is what Persona said in a stretch it spoke in (`open`: one still going).
    func echo(_ line: RecordingSentence, open: RecordingSpan? = nil) -> Bool {
        spoken.contains { $0.echoes(line) } || (open?.echoes(line) ?? false)
    }
}

/// A stretch of a recording Persona spoke in, in seconds on its clock (`RecordingSentence.start`), with what it said.
nonisolated struct RecordingSpan: Codable, Hashable, Sendable {
    var start: Double
    var end: Double
    /// The answer said in it (#5905); nil (or empty) for a stretch saved before, which keeps the time rule (`holds`).
    var text: String? = nil

    /// The line is Persona's echo by this stretch (`MeetingEcho`): it overlaps it, widened by `MeetingEcho.margin` on both
    /// sides, and its words are the answer's. A stretch without text: at least half of the line is inside it (`holds`).
    func echoes(_ line: RecordingSentence) -> Bool {
        guard let text, !text.isEmpty else { return holds(line) }
        return overlaps(line) && MeetingEcho.matches(line.text, spoken: text)
    }

    /// The line overlaps the stretch widened by `MeetingEcho.margin`; a line of no length, when it starts inside.
    func overlaps(_ line: RecordingSentence) -> Bool {
        let from = start - MeetingEcho.margin, to = end + MeetingEcho.margin
        guard line.end > line.start else { return line.start >= from && line.start <= to }
        return line.end >= from && line.start <= to
    }

    /// At least half of the line was said inside this stretch, with no margin (the rule before #5905, for a stretch with
    /// no text); a line of no length, when it starts inside.
    func holds(_ line: RecordingSentence) -> Bool {
        let length = line.end - line.start
        guard length > 0 else { return line.start >= start && line.start <= end }
        let inside = min(line.end, end) - max(line.start, start)
        return inside / length >= 0.5
    }
}

/// How a meeting is transcribed (#3862 phase 5, docs/assistant/meeting-minutes.md「Choosing the mode」): on this phone
/// (`device`, privacy mode: the meeting's audio never leaves it) or by the hub (`hub`: the audio goes to the hub, which
/// transcribes it with the speech service it is set up with, and the phone's own text fills its gaps).
nonisolated enum MeetingTranscriptionMode: String, Codable, CaseIterable, Sendable {
    case device
    case hub
}

/// A role a meeting took on at `at` (seconds on the recording's clock): one entry of the hub's timeline
/// (`{"at", "attendance"}`, docs/assistant/meeting-minutes.md「The timeline」).
nonisolated struct RecordingRoleChange: Codable, Hashable, Sendable {
    var at: Double
    var attendance: MeetingAttendance
}

/// A stretch with an open end: attending since `start`, until `end` (nil while it goes on).
nonisolated struct RecordingPeriod: Codable, Hashable, Sendable {
    var start: Double
    var end: Double?

    func holds(_ time: Double) -> Bool { time >= start && end.map { time <= $0 } ?? true }
}

/// Where on a recording's clock hub transcription stopped for it, and why (`RecordingAttendance.hubStopped`): the fatal
/// `error`'s code, or `http_<status>` for a refused socket.
nonisolated struct RecordingHubStop: Codable, Hashable, Sendable {
    var at: Double
    var code: String
}

/// The hub's side of attending (docs/assistant/meeting-minutes.md「Attendance fields」).
nonisolated enum MeetingAttendanceDefaults {
    /// Listed in `hello.features` by a hub whose manager takes everything about attending: only then the phone offers a role.
    static let feature = "meeting_attendance"
    /// The last the phone heard from the hub, for a start without the app in front (Siri, the Control, the Action
    /// button): whether it offers attending, and its default role. Rarely changes, so a user default.
    static let offeredKey = "recording.meeting.offered"
    static let roleKey = "recording.meeting.defaultRole"
    /// The session picked last (`SessionKey.path`, `main` for the main chat): the next meeting starts with it again while
    /// it can still attend.
    static let sessionKey = "recording.meeting.attendee"
    /// Its name then, for a start before the hub has listed it.
    static let sessionTitleKey = "recording.meeting.attendeeTitle"
    static let main = "main"

    /// The role a meeting starts with when nothing is picked: the hub's default while it offers attending, else off.
    static func cachedRole(_ store: UserDefaults = .standard) -> MeetingAttendance {
        guard store.bool(forKey: offeredKey) else { return .off }
        return store.string(forKey: roleKey).flatMap(MeetingAttendance.init(rawValue:)) ?? .off
    }

    /// Keeps what the hub said: whether it lists the feature, and `meeting_attendance_default` when known.
    static func remember(offered: Bool, role: MeetingAttendance?, _ store: UserDefaults = .standard) {
        if store.object(forKey: offeredKey) == nil || store.bool(forKey: offeredKey) != offered { store.set(offered, forKey: offeredKey) }
        if let role, store.string(forKey: roleKey) != role.rawValue { store.set(role.rawValue, forKey: roleKey) }
    }

    /// Listed by a hub whose manager has the meeting conversation (#3862, docs/assistant/meeting-minutes.md「Flags and
    /// compatibility」): a meeting started then is a room meeting.
    static let conversationFeature = "meeting_conversation"
    /// …and photos in the meeting (phase 3); only ever with `meeting_conversation`.
    static let photosFeature = "meeting_photos"
    /// …and the live view (phase 2: topics, the latest line, 15-second segments); only ever with `meeting_conversation`.
    static let liveViewFeature = "meeting_live_view"
    /// …and the highlights (phase 2b: the page's top card); only ever with `meeting_live_view`.
    static let highlightsFeature = "meeting_highlights"
    /// Listed by a hub whose manager takes Persona's echo marked on an upload (#5905: `echo` on segments, `spoken`), with
    /// `meeting_attendance`: only then the upload carries echo lines, else it leaves them out.
    static let echoFeature = "meeting_echo"
    /// Whether the last hub's `hello` listed it, for an upload while the socket is down.
    static let echoKey = "recording.meeting.echo"

    /// The last hub heard listed `meeting_echo`.
    static func hubEcho(_ store: UserDefaults = .standard) -> Bool { store.bool(forKey: echoKey) }

    /// …and hub transcription (phase 5: the upload's `transcription` and `timeline`, the audio socket, gap uploads); only
    /// ever with `meeting_conversation` and `meeting_live_view`.
    static let transcriptionFeature = "meeting_transcription"

    /// Whether the hub listed them, for a start without the app in front, as `offeredKey`.
    static let conversationKey = "recording.meeting.conversation"
    static let photosKey = "recording.meeting.photos"
    static let liveViewKey = "recording.meeting.liveView"
    static let highlightsKey = "recording.meeting.highlights"
    static let transcriptionKey = "recording.meeting.transcription"
    /// The hub's `meeting_transcription_default` and `meeting_transcription_ready` as last read (`GET /api/v1/assistant`),
    /// for a one-tap start before the settings are in.
    static let transcriptionDefaultKey = "recording.meeting.transcriptionDefault"
    static let transcriptionReadyKey = "recording.meeting.transcriptionReady"
    /// The iPhone's own **Hub transcription on Wi-Fi only** (off by default, kept on this phone, never on the hub):
    /// with it on and no Wi-Fi, no audio leaves the phone; that time goes up as gaps once on Wi-Fi.
    static let wifiOnlyKey = "recording.meeting.transcriptionWiFiOnly"

    /// Keeps whether the hub lists them (a start without the app in front reads them).
    static func remember(conversation: Bool, photos: Bool, liveView: Bool = false, highlights: Bool = false,
                         transcription: Bool = false,
                         _ store: UserDefaults = .standard) {
        if store.object(forKey: conversationKey) == nil || store.bool(forKey: conversationKey) != conversation { store.set(conversation, forKey: conversationKey) }
        if store.object(forKey: photosKey) == nil || store.bool(forKey: photosKey) != photos { store.set(photos, forKey: photosKey) }
        if store.object(forKey: liveViewKey) == nil || store.bool(forKey: liveViewKey) != liveView { store.set(liveView, forKey: liveViewKey) }
        if store.object(forKey: highlightsKey) == nil || store.bool(forKey: highlightsKey) != highlights { store.set(highlights, forKey: highlightsKey) }
        if store.object(forKey: transcriptionKey) == nil || store.bool(forKey: transcriptionKey) != transcription {
            store.set(transcription, forKey: transcriptionKey)
        }
    }

    /// Keeps the hub's transcription settings as read (each only when known).
    static func remember(transcriptionDefault: MeetingTranscriptionMode?, ready: Bool?, _ store: UserDefaults = .standard) {
        if let transcriptionDefault, store.string(forKey: transcriptionDefaultKey) != transcriptionDefault.rawValue {
            store.set(transcriptionDefault.rawValue, forKey: transcriptionDefaultKey)
        }
        if let ready, store.object(forKey: transcriptionReadyKey) == nil || store.bool(forKey: transcriptionReadyKey) != ready {
            store.set(ready, forKey: transcriptionReadyKey)
        }
    }

    /// The mode a meeting started with nothing picked asks for (pure, for the tests; docs/assistant/meeting-minutes.md
    /// 「Choosing the mode」): nil while the hub doesn't list `meeting_transcription` (no mode is named), `picked` when the
    /// start sheet offered one (`hub` only while the hub is ready, which the sheet's choice already says), else the hub's
    /// default, `device` while the hub isn't ready.
    static func startMode(listed: Bool, hubDefault: MeetingTranscriptionMode?, ready: Bool,
                          picked: MeetingTranscriptionMode? = nil) -> MeetingTranscriptionMode? {
        guard listed else { return nil }
        let wanted = picked ?? hubDefault ?? .device
        return wanted == .hub && ready ? .hub : .device
    }

    /// Keeps every meeting flag of a hub's `hello` as it arrives (`features`, not empty: an empty list says nothing), so a
    /// start without the app in front (Siri, a Control, the Action button) goes by the last hub it heard, not by the last
    /// one whose Recordings page was open: a stale `meeting_conversation` would start a room meeting an older manager
    /// takes as off. `role`: the hub's default role, when its settings are in.
    static func remember(hello features: Set<String>, role: MeetingAttendance?, _ store: UserDefaults = .standard) {
        let offered = features.contains(feature)
        remember(offered: offered, role: role, store)
        let conversation = offered && features.contains(conversationFeature)
        let liveView = conversation && features.contains(liveViewFeature)
        remember(conversation: conversation, photos: conversation && features.contains(photosFeature),
                 liveView: liveView, highlights: liveView && features.contains(highlightsFeature),
                 transcription: liveView && features.contains(transcriptionFeature), store)
        let echo = offered && features.contains(echoFeature)
        if store.object(forKey: echoKey) == nil || store.bool(forKey: echoKey) != echo { store.set(echo, forKey: echoKey) }
    }
}

/// The room conversation's state as the hub reports it (`room` on the upload's 200, `GET/POST …/live`, `detail.meta`;
/// docs/assistant/meeting-minutes.md「Fields on `GET …/live` and `detail.meta`」).
nonisolated struct MeetingRoom: Codable, Hashable, Sendable {
    nonisolated enum State: String, Sendable { case none, starting, ready, unavailable, ended, deleted }
    var state: String
    var machineID: String?
    var sessionID: String?
    /// Why it is `unavailable` (an open set: `runtime_unsupported`, `node_too_old`, `machine_offline`, `start_failed`).
    var problem: String?

    enum CodingKeys: String, CodingKey { case state, machineID = "machine_id", sessionID = "session_id", problem }

    /// An open set: a value the app doesn't know reads as `starting`.
    var known: State { State(rawValue: state) ?? .starting }

    /// The room's conversation once it has ids (never with `none` / `deleted`).
    func key(group: String) -> SessionKey? {
        guard known != State.none, known != State.deleted, let machineID, !machineID.isEmpty, let sessionID, !sessionID.isEmpty else { return nil }
        return SessionKey(group: group, machineID: machineID, sessionID: sessionID)
    }
}

nonisolated extension MeetingRoom {
    /// Leniently: a field of another shape is left out rather than losing the answer it came in (a missing state is one
    /// the app doesn't know).
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = (try? c.decodeIfPresent(String.self, forKey: .state)) ?? State.starting.rawValue
        machineID = try? c.decodeIfPresent(String.self, forKey: .machineID)
        sessionID = try? c.decodeIfPresent(String.self, forKey: .sessionID)
        problem = try? c.decodeIfPresent(String.self, forKey: .problem)
    }
}

/// One of the meeting's topics as the room set them (`topics` of `GET …/live`, #3862 phase 2;
/// docs/assistant/meeting-minutes.md「Phase 2: live view」): `{"title", "state", "at"?, "note"?}`, in the room's order.
nonisolated struct MeetingTopic: Codable, Hashable, Sendable {
    nonisolated enum State: String, Sendable { case done, current, open }
    var title: String
    var state: String
    /// Seconds from the start of the recording when the topic began.
    var at: Double?
    var note: String?

    enum CodingKeys: String, CodingKey { case title, state, at, note }

    /// `done`, `current` or `open`; a value the app doesn't know reads as `open` (neither done nor being discussed).
    var known: State { State(rawValue: state) ?? .open }

    /// The topic being discussed now: the first `current` (the hub keeps at most one).
    static func current(_ topics: [MeetingTopic]) -> MeetingTopic? { topics.first { $0.known == .current } }

    /// A topic with its identity in the list (`listed`).
    nonisolated struct Listed: Identifiable, Hashable, Sendable {
        var id: String
        var topic: MeetingTopic
    }

    /// The topics with an identity each (pure, for the tests and the page's chips; docs/apple/performance.md P6): the room
    /// replaces the whole list, inserting and reordering topics, so a place in it is no identity. A topic is its title,
    /// which stays while it goes from open to now to done (`at` comes only once it begins); the hub doesn't promise
    /// unique titles, so a title listed again gets its count ("Q&A", "Q&A #2", …), and one that is taken already the
    /// next free count: the ids are unique whatever the room sends.
    static func listed(_ topics: [MeetingTopic]) -> [Listed] {
        var used = Set<String>()
        return topics.map { topic in
            var id = topic.title, n = 1
            while used.contains(id) {
                n += 1
                id = "\(topic.title) #\(n)"
            }
            used.insert(id)
            return Listed(id: id, topic: topic)
        }
    }
}

nonisolated extension MeetingTopic {
    /// Leniently: a topic without a title is no topic (the list drops it), any other field of another shape is left out.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let title = (try? c.decodeIfPresent(String.self, forKey: .title))?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !title.isEmpty else {
            throw DecodingError.dataCorruptedError(forKey: .title, in: c, debugDescription: "a topic without a title")
        }
        self.title = title
        state = (try? c.decodeIfPresent(String.self, forKey: .state)) ?? State.open.rawValue
        at = (try? c.decodeIfPresent(Double.self, forKey: .at)).flatMap { $0.isFinite && $0 >= 0 ? $0 : nil }
        note = try? c.decodeIfPresent(String.self, forKey: .note)
    }
}

/// One of the meeting's highlights as the room set them (`highlights` of `GET …/live`, #3862 phase 2b;
/// docs/assistant/meeting-minutes.md「Phase 2b: highlights」): `{"text", "at", "end"}`, a point the meeting came to and
/// the minute of the transcript it was made in, from the start of that minute's first line (`at`) to the end of its last
/// (`end`), in seconds from the recording's start.
nonisolated struct MeetingHighlight: Codable, Hashable, Sendable {
    var text: String
    var at: Double
    var end: Double

    enum CodingKeys: String, CodingKey { case text, at, end }

    init(text: String, at: Double, end: Double? = nil) {
        self.text = text
        self.at = at
        self.end = end ?? at
    }
}

nonisolated extension MeetingHighlight {
    /// Leniently: a highlight without words, or without a time it can go back to (`at` absent, of another shape, not
    /// finite or below 0), is no highlight (the list drops it); an `end` that isn't a finite time from `at` on is `at`.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let text = (try? c.decodeIfPresent(String.self, forKey: .text))?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !text.isEmpty else {
            throw DecodingError.dataCorruptedError(forKey: .text, in: c, debugDescription: "a highlight without words")
        }
        guard let at = try? c.decodeIfPresent(Double.self, forKey: .at), at.isFinite, at >= 0 else {
            throw DecodingError.dataCorruptedError(forKey: .at, in: c, debugDescription: "a highlight without a time")
        }
        self.text = text
        self.at = at
        let end = try? c.decodeIfPresent(Double.self, forKey: .end)
        self.end = end.flatMap { $0.isFinite && $0 >= at ? $0 : nil } ?? at
    }
}
