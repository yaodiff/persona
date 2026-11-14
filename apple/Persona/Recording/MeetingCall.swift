#if os(iOS)
import Foundation
import Observation
import OSLog

/// Realtime voice in the meeting (#3862 phase 4, docs/assistant/meeting-minutes.md「Phase 4: realtime voice in the
/// meeting」; the phone's side docs/apple/recording.md「Realtime voice (phase 4)」): the meeting call and Talk.
///
/// **The call.** While this phone records a room meeting Persona takes part in, whose room is `ready`, and the hub's voice
/// extension lists `meeting` (`MeetingCallRule.wanted`), a protocol 2 call runs in meeting mode, bound to the room
/// (`VoiceConversation` with `VoiceMeeting`), on the recorder's capture (`MeetingCallAudio`): it never claims the
/// microphone, keeps no screen on, never ends after idle minutes, and pauses with the recording (its socket stays). It
/// reconnects as protocol 2 does, then every 30 seconds while the meeting records; a call that ended by itself starts
/// again 30 seconds later unless the end says it never can (`MeetingCallRule.retries`). It ends with Stop or another role.
///
/// **Talk.** Held on the page, or a window the Lock Screen opens (`MeetingTalkWindow`). With the call live a span is a
/// `talk on` … `talk off` pair, and between them the recorder's audio goes up (`VoiceUplink.talk`), only the samples
/// captured inside the span (`MeetingTalkTap`); otherwise (a listener, no `meeting`, the call down, or a span opened
/// before the owner was told what Talk sends: `MeetingCallRule.toCall`) the segment is cut at the span's start and end,
/// transcribed on the phone, and asked with `…/ask` (`spoken: true`), answered as any ask (aloud for a participant,
/// `MeetingSpeaker`). A
/// span the call loses midway (its socket dropped) goes on that way. A hold ends when the finger lifts, when the page's
/// Talk goes (the page left, Just Me), at the activity's Done, or after 60 seconds.
///
/// **Echo.** Every stretch the call's voice plays in is kept on the recording with what the voice said in it, the call's
/// answers (`RecordingAttendance.spoken`, `MeetingCallEcho`), as `MeetingSpeaker` keeps an answer it says: a sentence that
/// overlaps one and says those words is Persona's echo (#5905, `RecordingSpan.echoes`, which widens the stretch itself),
/// hidden in the transcript, kept out of the live log and uploaded marked; what anyone says over it stays the meeting's.
@MainActor @Observable final class MeetingCall {
    static let shared = MeetingCall()

    /// The meeting call of the recording being made, live, connecting or reconnecting; nil without one.
    private(set) var call: VoiceConversation?
    /// The recording the call belongs to.
    private(set) var callRecording: UUID?
    /// The Talk span being held or a talk window, nil when none is open.
    private(set) var talk: Talk?
    /// A word under Talk after a span, for a moment.
    private(set) var note: Note?
    /// The owner was shown what Talk sends (the Wording on the meeting page, under Talk) as a span went to the voice
    /// service: the note stops, and a span from anywhere, the Lock Screen's included, may go to the call
    /// (`MeetingTalkPrivacy`). Read from the defaults once, at the start.
    private(set) var privacySeen = false
    /// The latest answer a meeting call of the recording said, kept once that call went (`answer(for:)`).
    private var keptAnswer: (recording: UUID, caption: VoiceCaption)?

    nonisolated struct Talk: Equatable, Sendable {
        var id = UUID()
        var recording: UUID
        /// Held on the page, or a talk window (VoiceOver on the page, the Lock Screen's tap).
        var source: MeetingTalkSource
        /// It goes to the meeting call (else it is asked without one).
        var viaCall: Bool
        /// Where it began on the recording's clock.
        var start: Double
        var opened: Date

        /// A talk window, not a hold: it ends on its own (`MeetingTalkWindow`) or on a tap.
        var window: Bool { source != .hold }

        /// The span once the call no longer carries it (its socket dropped, the call ended or went): asked without the
        /// call from here, over its whole length (`MeetingTalkWords.question` takes the sentences at least half inside
        /// it). nil while the call carries it, or for one that never went to the call.
        func lost(callTalking: Bool) -> Talk? {
            guard viaCall, !callTalking else { return nil }
            var asked = self
            asked.viaCall = false
            return asked
        }
    }

    nonisolated enum Note: Equatable, Sendable {
        /// Asked without the call, and the transcript holds no words for the span.
        case nothingHeard
        /// Let go almost at once: Talk is held while speaking.
        case holdLonger
    }

    /// A span shorter than this was a tap, not a hold.
    static let shortestHold: TimeInterval = 0.5
    /// How often a talk window looks at the microphone's level.
    static let windowTick: Duration = .milliseconds(100)
    /// How long a note stays under Talk.
    static let noteShown: Duration = .seconds(4)
    /// The longest one stretch of the call's voice counts for (as `MeetingSpeaker.longestAnswer`).
    static let longestVoice: Double = 150
    /// The longest a hold stays open (the talk window's limit): a finger that never lifts, or a hold the page lost, ends.
    static let longestHold: TimeInterval = MeetingTalkWindow.limit
    /// How long `talk off` waits for the tap's buffer that holds the release: a buffer is at most 0.512 s (4096 frames
    /// at a Bluetooth headset's 8 kHz), and it reaches the tap a moment after its last sample. Later, the tail is cut.
    static let tailWait: TimeInterval = 0.7

    @ObservationIgnored private let tap = MeetingTalkTap()
    @ObservationIgnored private var started = false
    @ObservationIgnored private var listening = false
    @ObservationIgnored private var window: MeetingTalkWindow?
    /// The meeting page has the Wording on screen under Talk, for this recording, since then (`showingPrivacy`).
    @ObservationIgnored private var privacyOnScreen: (recording: UUID, since: Date)?
    @ObservationIgnored private var windowTask: Task<Void, Never>?
    @ObservationIgnored private var holdTask: Task<Void, Never>?
    @ObservationIgnored private var noteTask: Task<Void, Never>?
    /// A span let go on the call whose `talk off` waits for the tap's last buffer of it (`closeOnCall`).
    @ObservationIgnored private var talkOff: (recording: UUID, task: Task<Void, Never>)?
    /// The call's audio, to stop what it plays when the recording pauses.
    @ObservationIgnored private var callAudio: MeetingCallAudio?
    /// The recording whose pause has stopped Persona's voice already (`sync`).
    @ObservationIgnored private var quietedPause: UUID?
    /// A call that ended by itself: the next may start then (`VoiceMeeting.retryEvery` later), or never for this recording.
    @ObservationIgnored private var retry: (recording: UUID, at: Date?)?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    /// Asking the hub whether its voice extension takes meeting calls, for the meeting being recorded (`probe`).
    @ObservationIgnored private var asked: Asked?
    @ObservationIgnored private var probeTask: Task<Void, Never>?
    /// Where the call's voice began playing on the recording's clock, while it plays.
    @ObservationIgnored private var voiceStart: (recording: UUID, at: Double)?
    /// The call's answers (its voice's captions) a closed stretch of its voice was kept with: a later stretch carries only
    /// what came after (`MeetingCallEcho`). For the call that runs now: another call's captions start again.
    @ObservationIgnored private var keptAnswers: Set<String> = []

    private struct Asked {
        var recording: UUID
        var tries = 0
        /// When the next ask may go; nil: one is out, or no more for this recording.
        var next: Date? = .distantPast
    }
    private let log = Logger(subsystem: "persona", category: "meeting-call")

    private var capture: RecordingCapture { .shared }
    private var library: RecordingLibrary { .shared }

    #if DEBUG
    /// The made-up room meetings (`-showMeetingSample room-…`): the first-use note shows again, for the screenshots.
    func forgetPrivacyNote() {
        privacySeen = false
        UserDefaults.standard.removeObject(forKey: MeetingTalkPrivacy.seenKey)
    }
    #endif

    /// From `MeetingRecorder.start()` at launch.
    func start() {
        guard !started else { return }
        started = true
        privacySeen = MeetingTalkPrivacy.isSeen
        observe()
    }

    private func observe() {
        withObservationTracking { sync() } onChange: {
            Task { @MainActor in MeetingCall.shared.observe() }
        }
    }

    // MARK: What the page and the Live Activity ask

    /// Talk is offered for `entry` now: a room meeting Persona can be asked in, recording (not paused) on this phone.
    func canTalk(_ entry: RecordingEntry) -> Bool {
        capture.recordingID == entry.id && capture.state == .recording
            && MeetingCallRule.talkOffered(entry, roomMissing: RecordingSync.shared.roomMissing.contains(entry.id))
    }

    /// The recording's meeting call is up: Talk and typed questions go to it.
    func isLive(_ recording: UUID) -> Bool { callRecording == recording && call?.connected == true }

    /// The recording's call, live or on its way up, for the page's line.
    func call(for recording: UUID) -> VoiceConversation? { callRecording == recording ? call : nil }

    /// The privacy texts' rule (`MeetingCallRule.talkSendsAudio`) for the group's hub now.
    func voiceExtension(group: String) -> String? {
        VoiceSupport.shared.probes[group].flatMap { MeetingCallRule.extensionID(extensions: $0.extensions, info: $0.info) }
    }

    /// The latest answer the recording's meeting call said aloud (its voice's newest final caption), kept after that call
    /// went until the recording ends: the Live Activity's third line (`RecordingActivity`). The room's, never Just Me.
    func answer(for recording: UUID) -> VoiceCaption? {
        if callRecording == recording, let said = call?.lastAnswer { return said }
        return keptAnswer?.recording == recording ? keptAnswer?.caption : nil
    }

    /// A typed question while the call is live: a `text` frame (the fast layer hands it to the room and says the
    /// answer). False when the call isn't live: the caller asks through `…/ask`.
    @discardableResult func say(_ text: String, recording: UUID) -> Bool {
        guard isLive(recording), let call, MeetingAsk.hasWords(text) else { return false }
        call.say(String(String.UnicodeScalarView(text.trimmingCharacters(in: .whitespacesAndNewlines).unicodeScalars.prefix(MeetingAsk.maxQuestion))))
        return true
    }

    /// Stops what the call's voice says (never the room's work).
    func interruptVoice(_ recording: UUID) {
        guard callRecording == recording else { return }
        call?.interruptSpeech()
    }

    /// The stretch the call's voice is playing in now, with what it is saying (its answers not kept with an earlier
    /// stretch, the one being said included, whole as far as it came): a sentence transcribed meanwhile is checked against
    /// it (`MeetingLive`, the upload). As it plays: the echo rule widens it (`MeetingEcho.margin`), never this.
    func openSpan(for recording: UUID) -> RecordingSpan? {
        guard let voiceStart, voiceStart.recording == recording else { return nil }
        let end = min(max(capture.timeline, voiceStart.at), voiceStart.at + Self.longestVoice)
        let said = callRecording == recording ? MeetingCallEcho.said(call?.captions ?? [], kept: keptAnswers) : nil
        return RecordingSpan(start: voiceStart.at, end: end, text: said)
    }

    // MARK: Talk

    /// The meeting page put the Wording under Talk on screen (`true`), or took it off: drawn, not merely due, so a span
    /// goes to the call only after the owner could see it (`MeetingTalkPrivacy.shown`).
    func showingPrivacy(_ showing: Bool, recording: UUID) {
        if showing {
            if privacyOnScreen?.recording != recording { privacyOnScreen = (recording, .now) }
        } else if privacyOnScreen?.recording == recording {
            privacyOnScreen = nil
        }
    }

    /// The Wording is on the recording's page now, and has been for `MeetingTalkPrivacy.shownFor`.
    func privacyShown(_ recording: UUID, at now: Date = .now) -> Bool {
        guard let privacyOnScreen, privacyOnScreen.recording == recording else { return false }
        return MeetingTalkPrivacy.shown(since: privacyOnScreen.since, at: now)
    }

    /// Opens a Talk span from `source`: to the call when it is live and the owner was told what Talk sends (they saw the
    /// Wording go with a span before, or the page shows it now; never the Lock Screen's alone: `MeetingCallRule.toCall`),
    /// else for a question asked without it, whose audio stays on the phone. A span that goes to the call while the page
    /// shows the Wording marks it seen. False when Talk isn't offered now, or a span is already open.
    @discardableResult func beginTalk(_ recording: UUID, from source: MeetingTalkSource) -> Bool {
        guard talk == nil, let entry = library.entry(recording), canTalk(entry) else { return false }
        // Where the press lies on the tap's clock: a buffer's samples from before it stay out of the span.
        let position = capture.listenPosition(.press)
        let at = capture.timeline
        // Read before anything changes the page: the Wording under Talk as the finger went down.
        let shown = source != .activity && privacyShown(recording)
        // A span let go a moment ago: its `talk off` goes first.
        sendTalkOff()
        let live = isLive(recording)
        // What Persona is saying gives way: the owner speaks now.
        if live, call?.speaking == true { call?.interruptSpeech() }
        MeetingSpeaker.shared.stop()
        let viaCall = MeetingCallRule.toCall(live: live, source: source, privacySeen: privacySeen, privacyShown: shown)
            && call?.talk(true) == true
        if viaCall {
            tap.open(at: position)
        } else {
            // Without the call, the span's words are a segment of their own, transcribed as soon as it ends.
            capture.flushSegment()
        }
        let opened = Talk(recording: recording, source: source, viaCall: viaCall, start: at, opened: .now)
        talk = opened
        showNote(nil)
        // Only a span that started while the Wording showed: one that went because it was seen before changes nothing.
        if viaCall, shown, !privacySeen {
            privacySeen = true
            MeetingTalkPrivacy.seen()
        }
        if opened.window { openWindow(room: tap.room(before: position)) } else { capHold(opened.id) }
        log.info("talk: \(String(describing: source), privacy: .public) opened, \(viaCall ? "to the call" : "asked without it", privacy: .public)")
        return true
    }

    /// Closes the open span (the hold let go, the window's end). `ask: false` drops one asked without the call (the
    /// recording stopped under it).
    func endTalk(ask: Bool = true) {
        guard var talk else { return }
        self.talk = nil
        windowTask?.cancel()
        windowTask = nil
        window = nil
        holdTask?.cancel()
        holdTask = nil
        let end = capture.timeline
        let short = talk.source == .hold && Date.now.timeIntervalSince(talk.opened) < Self.shortestHold
        // The call lost it before `sync` saw it: asked without the call, as `fellOff` has it.
        if let lost = talk.lost(callTalking: callTalking(talk.recording)) {
            log.notice("talk: the meeting call lost the span, asked without it")
            tap.clear()
            talk = lost
        }
        if talk.viaCall {
            closeOnCall(talk.recording, through: capture.listenPosition(.release))
        } else {
            capture.flushSegment()
            if ask, !short { Task { await askWithout(talk, end: end) } }
        }
        if short { showNote(.holdLonger) }
    }

    /// The page's Talk went with a hold still open (the page left, Just Me turned on, the button gone before the finger
    /// lifted): the hold ends as if let go. A talk window stays: it ends on its own.
    func endHold(_ recording: UUID) {
        guard let talk, talk.recording == recording, talk.source == .hold else { return }
        endTalk()
    }

    /// The Lock Screen's and the island's Talk / Done (`TalkInMeetingIntent`): `open` is the action the button showed
    /// (`MeetingCallRule.activityTap`). Talk opens a talk window, Done ends the open span, hold or window; a tap that no
    /// longer matches does nothing.
    func activityTap(_ recording: UUID, open: Bool?) {
        switch MeetingCallRule.activityTap(open: open, spanOpen: talk?.recording == recording) {
        case .open: beginTalk(recording, from: .activity)
        case .close: endTalk()
        case .ignore: log.info("talk: a Live Activity tap that no longer matches, ignored")
        }
    }

    /// The call stops carrying the open span (its socket dropped, the call ended): what was said goes on into a segment
    /// cut here, so it is transcribed now, and the span is asked without the call when it ends.
    private func fellOff() {
        guard var talk, talk.viaCall else { return }
        log.notice("talk: the meeting call lost the span, asked without it")
        tap.clear()
        talk.viaCall = false
        self.talk = talk
        capture.flushSegment()
    }

    /// The recording's call carries a span now.
    private func callTalking(_ recording: UUID) -> Bool { callRecording == recording && call?.talking == true }

    /// `talk off` once the tap has sent the span's audio up to the release (`end`, on the tap's clock): the buffer that
    /// holds it comes a moment later, at most `tailWait`. Meanwhile nothing after `end` goes.
    private func closeOnCall(_ recording: UUID, through end: Double) {
        tap.close(at: end)
        talkOff?.task.cancel()
        let task = Task { [weak self] in
            let deadline = Date.now.addingTimeInterval(Self.tailWait)
            while let self, !Task.isCancelled, !self.tap.covers(end), Date.now < deadline {
                try? await Task.sleep(for: .milliseconds(20))
            }
            guard !Task.isCancelled else { return }
            self?.sendTalkOff()
        }
        talkOff = (recording, task)
    }

    /// The `talk off` of a span let go on the call, now (it waited for its last buffer, a new span opens, the call goes).
    private func sendTalkOff() {
        guard let pending = talkOff else { return }
        talkOff = nil
        pending.task.cancel()
        tap.clear()
        if callRecording == pending.recording { call?.talk(false) }
    }

    /// A hold ends after `longestHold` whatever happens to the finger.
    private func capHold(_ id: UUID) {
        holdTask?.cancel()
        holdTask = Task { [weak self] in
            guard (try? await Task.sleep(for: .seconds(Self.longestHold))) != nil, let self, self.talk?.id == id else { return }
            self.log.notice("talk: a hold reached its limit")
            self.endTalk()
        }
    }

    /// `room`: what the tap heard just before the window opened, so the room's own talking isn't taken for the owner's.
    private func openWindow(room: MeetingTalkWindow.Room) {
        window = MeetingTalkWindow(opened: .now, room: room)
        windowTask?.cancel()
        windowTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.windowTick)
                guard let self, !Task.isCancelled, var window = self.window else { return }
                let end = window.heard(self.tap.level, at: .now)
                self.window = window
                if let end {
                    self.log.info("talk: window ended (\(String(describing: end), privacy: .public))")
                    self.endTalk()
                    return
                }
            }
        }
    }

    /// A span asked without the call: once its segment has its text, the words said in it go as a spoken question.
    private func askWithout(_ talk: Talk, end: Double) async {
        let span = RecordingSpan(start: talk.start, end: max(end, talk.start))
        #if DEBUG
        RecordingSamples.transcribeTalkSample(talk.recording, span: span)
        #endif
        for _ in 0..<60 {
            guard let entry = library.entry(talk.recording) else { return }
            if MeetingTalkWords.transcribed(entry.segments, span: span) { break }
            try? await Task.sleep(for: .milliseconds(200))
        }
        guard let entry = library.entry(talk.recording) else { return }
        guard let words = MeetingTalkWords.question(entry.segments, span: span) else {
            showNote(.nothingHeard)
            return
        }
        MeetingAsk.shared.ask(entry, text: words, spoken: true)
    }

    private func showNote(_ next: Note?) {
        noteTask?.cancel()
        noteTask = nil
        if note != next { note = next }
        guard next != nil else { return }
        noteTask = Task { [weak self] in
            guard (try? await Task.sleep(for: Self.noteShown)) != nil, let self else { return }
            self.note = nil
        }
    }

    /// The recording is stopping (`RecordingCapture.onStopping`): the span closes unasked, the voice's stretch is kept, the
    /// call ends and the tap goes.
    func ended(_ recording: UUID) {
        if talk?.recording == recording { endTalk(ask: false) }
        played(false, recording: recording)
        if callRecording == recording { endCall() }
        if retry?.recording == recording { retry = nil; retryTask?.cancel(); retryTask = nil }
        if asked?.recording == recording { asked = nil; probeTask?.cancel(); probeTask = nil }
        if keptAnswer?.recording == recording { keptAnswer = nil }
        if quietedPause == recording { quietedPause = nil }
        if listening {
            capture.listen(nil)
            listening = false
        }
    }

    // MARK: Following the meeting

    /// Brings the call and the tap in step with the recording, its role and room, and the hub's voice extension.
    private func sync() {
        let state = capture.state
        let recording = state == .recording || state == .paused
        let id = recording ? capture.recordingID : nil
        let entry = id.flatMap { library.entry($0) }
        // The tap listens while a room meeting records (the call's spans, the window's silence).
        let listens = entry?.roomMeeting == true && entry?.active == true
        if listens != listening {
            listening = listens
            let tap = tap
            // A new recording's clock starts again: the room heard in another one says nothing about this one.
            if listens { tap.forgetRoom() }
            capture.listen(listens ? { @Sendable buffer, at in tap.feed(buffer, at: at) } : nil)
        }
        if let talk, talk.recording != id || state != .recording || entry.map(canTalk) != true {
            // Paused, stopped, the role off: what was said up to here is still asked (never after a stop: `ended`).
            endTalk(ask: talk.recording == id)
        }
        // The call dropped (or ended) under a span it carried: the rest of it is asked without the call.
        if let talk, talk.lost(callTalking: callTalking(talk.recording)) != nil { fellOff() }
        // Paused (by the owner or a call): what Persona says stops, so nothing stale plays after Continue.
        if state == .paused, let id {
            if quietedPause != id {
                quietedPause = id
                quiet(id)
            }
        } else if state == .recording {
            quietedPause = nil
        }
        guard let id, let entry else { return endCall() }
        let roomMissing = RecordingSync.shared.roomMissing.contains(id)
        let room = MeetingConversation.shared.meeting(id).room
        let key = MeetingConversation.shared.roomKey(id)
        let probe = key.flatMap { VoiceSupport.shared.probes[$0.group] }
        let voice = MeetingCallRule.extensionID(extensions: probe?.extensions, info: probe?.info)
        let candidate = MeetingCallRule.wanted(entry, roomMissing: roomMissing, room: room) && key != nil
        if candidate, voice == nil, call == nil, let key { self.probe(key.group, recording: id) }
        let wanted = candidate && voice != nil
        if retry.map({ $0.recording != id }) == true { retry = nil }
        if let call {
            // A running call stays through an ask that failed or found the extension restarting (its own `ready` and
            // close decide then); only the hub's definite no ends it.
            let declined = MeetingCallRule.declines(extensions: probe?.extensions, info: probe?.info)
            if !candidate || declined || call.key != key || callRecording != id {
                endCall()
            } else if case .ended(let why) = call.phase {
                log.notice("meeting call ended: \(String(describing: why), privacy: .public)")
                // Its voice's stretch is kept first, with what it said: the captions go with the call.
                played(false, recording: id)
                keep(call, recording: id)
                self.call = nil
                callAudio = nil
                callRecording = nil
                retry = (id, MeetingCallRule.retries(after: why) ? Date.now.addingTimeInterval(Self.seconds(VoiceMeeting.retryEvery)) : nil)
                // It took meeting calls: the next one may need the hub asked again.
                asked = nil
            } else {
                return
            }
        }
        guard wanted, let key, let voice, call == nil else { return }
        if let retry, retry.recording == id {
            guard let at = retry.at else { return }
            guard at <= .now else { return wake(at: at) }
        }
        begin(key: key, extensionID: voice, recording: id)
    }

    /// The recording paused: the call's voice stops playing (what it had queued is dropped, its stretch closes, and the
    /// voice service stops saying it), and so does an answer played on the recorder's engine, which the pause froze.
    private func quiet(_ recording: UUID) {
        if callRecording == recording {
            callAudio?.clear()
            if isLive(recording) { call?.interruptSpeech() }
        }
        MeetingSpeaker.shared.recordingPaused()
    }

    /// The latest answer of a call that goes, for the Live Activity (`answer(for:)`).
    private func keep(_ call: VoiceConversation, recording: UUID) {
        if let said = call.lastAnswer { keptAnswer = (recording, said) }
    }

    private func begin(key: SessionKey, extensionID: String, recording: UUID) {
        retry = nil
        retryTask?.cancel()
        retryTask = nil
        let audio = MeetingCallAudio(capture: capture, tap: tap) { [weak self] on in self?.played(on, recording: recording) }
        callAudio = audio
        var deps = VoiceDeps.live(group: key.group)
        deps.audio = { audio }
        let call = VoiceConversation(key: key, extensionID: extensionID, surface: .phone, meeting: VoiceMeeting(recordingID: recording),
                                     deps: deps)
        self.call = call
        callRecording = recording
        keptAnswers = []
        log.info("meeting call: starting (echo cancellation \(self.capture.cancelsEcho, privacy: .public))")
        Task { await call.begin() }
    }

    private func endCall() {
        guard let call else { return }
        // A span on the call goes on without it (asked through `…/ask` when it ends).
        if talk?.viaCall == true { fellOff() }
        sendTalkOff()
        if let id = callRecording {
            played(false, recording: id)
            keep(call, recording: id)
        }
        self.call = nil
        callAudio = nil
        callRecording = nil
        call.end(.left)
    }

    /// Looks again once a call may start after the wait.
    private func wake(at date: Date) {
        guard retryTask == nil else { return }
        retryTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(max(0, date.timeIntervalSinceNow)))
            guard !Task.isCancelled, let self else { return }
            self.retryTask = nil
            self.sync()
        }
    }

    /// Asks the group's hub whether its voice extension takes meeting calls (P9): as the meeting wants a call, then a few
    /// slow retries while the answer says nothing either way (`MeetingCallRule.probeAgain`), and never again for this
    /// recording once the hub said no; not once a minute for the length of the meeting.
    private func probe(_ group: String, recording: UUID) {
        if asked?.recording != recording {
            asked = Asked(recording: recording)
            probeTask?.cancel()
            probeTask = nil
        }
        guard var ask = asked, let next = ask.next, next <= .now, let api = AppModel.shared.api(group) else { return }
        ask.tries += 1
        ask.next = nil
        asked = ask
        let tries = ask.tries
        Task { [weak self] in
            await VoiceSupport.shared.refresh(group: group, api: api)
            guard let self, self.asked?.recording == recording else { return }
            let probe = VoiceSupport.shared.probes[group]
            guard let again = MeetingCallRule.probeAgain(extensions: probe?.extensions, info: probe?.info, tries: tries) else { return }
            self.asked?.next = Date.now.addingTimeInterval(again)
            self.probeTask = Task { [weak self] in
                guard (try? await Task.sleep(for: .seconds(again))) != nil, let self else { return }
                self.probeTask = nil
                self.sync()
            }
        }
    }

    // MARK: Echo

    /// The call's voice began (`true`) or stopped playing: the stretch it took is Persona's on the recording, kept with
    /// what the voice said in it (`openSpan`); the answers said by then are not carried into the next stretch.
    private func played(_ on: Bool, recording: UUID) {
        if on {
            if voiceStart == nil {
                voiceStart = (recording, capture.timeline)
                // The audio socket says so in hub mode (#3862 phase 5), without the words: at playback's start the
                // captions hold only the answer's beginning (`MeetingTranscription.spokenText`); the whole answer goes
                // with the span once it ended.
                MeetingTranscription.shared.speaking(recording, voice: .call, on: true, at: capture.timeline,
                                                     text: openSpan(for: recording)?.text)
            }
            return
        }
        guard let span = openSpan(for: recording) else { return }
        voiceStart = nil
        if callRecording == recording { keptAnswers = MeetingCallEcho.kept(call?.captions ?? [], before: keptAnswers) }
        library.change(recording) { $0.attendance?.spoken.append(span) }
        MeetingTranscription.shared.speaking(recording, voice: .call, on: false, at: span.end, text: nil)
    }

    private static func seconds(_ duration: Duration) -> TimeInterval {
        Double(duration.components.seconds) + Double(duration.components.attoseconds) / 1e18
    }
}

/// Whether the owner has been told what Talk sends (#3862 phase 4, the contract's「Wording」): the first-use note under
/// Talk shows until the first span that went to the voice service while it showed. Written once.
nonisolated enum MeetingTalkPrivacy {
    static let seenKey = "recording.meeting.talkPrivacySeen"

    /// How long the Wording must have been on the page before a span counts as opened under it: drawn in the same moment
    /// as the finger went down (the call came up just then) is not shown.
    static let shownFor: TimeInterval = 1

    /// The Wording, on screen `since` (nil: not on screen), counts as shown at `now`.
    static func shown(since: Date?, at now: Date) -> Bool {
        guard let since else { return false }
        return now.timeIntervalSince(since) >= shownFor
    }

    static var isSeen: Bool { UserDefaults.standard.bool(forKey: seenKey) }

    static func seen() {
        guard !isSeen else { return }
        UserDefaults.standard.set(true, forKey: seenKey)
    }
}
#endif
