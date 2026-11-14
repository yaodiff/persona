#if os(iOS)
import ActivityKit
import Foundation
import Observation
import OSLog

/// The phone's half of the recording Live Activity (docs/apple/live-activity.md「Recording」): one activity while this phone
/// records, any recording (a meeting's or a note), started with it and ended with it. It follows `RecordingCapture` (state,
/// a call's interruption) and the recording's entry (its title once the calendar is read, bookmarks); the time is counted
/// by the system from `runningSince`, so a recording that runs on needs no updates. A meeting with the live view (#3862
/// phase 2) also carries its current topic and latest line: an answer, a phase or anything else goes at once, a new line
/// or topic at most every 5 seconds (`hold`).
@MainActor final class RecordingActivity {
    static let shared = RecordingActivity()
    typealias Content = RecordingActivityAttributes.ContentState

    private let capture = RecordingCapture.shared
    private let library = RecordingLibrary.shared
    private var started = false
    /// What the activity shows now, and for which recording.
    private var shown: (id: UUID, content: Content)?
    /// When `shown` went to the system: a new line or topic waits until `throttle` after it.
    private var sentAt: Date?
    /// Each source's latest answer (the room's asks, the meeting call's voice) and when this phone first had it, for the
    /// recording `answersOf`: the third line is the newer one for `answerShown` after it came.
    private var answersSeen: [AnswerSource: (key: String, at: Date)] = [:]
    private var answersOf: UUID?
    /// A sync due later (a held line or topic, an answer giving way to the line), and when.
    private var wake: (task: Task<Void, Never>, at: Date)?
    private let log = Logger(subsystem: "persona", category: "recording-activity")

    /// From `MeetingRecorder.start()` at launch.
    func start() {
        guard !started else { return }
        started = true
        observe()
    }

    /// Brings the activity in step now, rather than on the next run loop: an `AudioRecordingIntent` must have it showing
    /// by the time it returns.
    func refresh() { sync() }

    private func observe() {
        withObservationTracking { sync() } onChange: {
            Task { @MainActor in RecordingActivity.shared.observe() }
        }
    }

    private func sync() {
        let state = capture.state
        let interrupted = capture.interrupted
        guard let id = capture.recordingID, state == .recording || state == .paused, let entry = library.entry(id) else {
            endAll()
            return
        }
        let previous = shown?.id == id ? shown?.content : nil
        let now = Date.now
        // Read here, so the activity follows them: an older manager took the room meeting with Persona off (#3862), and
        // the room's topics (phase 2; in the background, without `meeting_live`, read after the lines went:
        // `MeetingConversation.liveAccepted`). The answer is the newer of the room's latest ask and what the meeting call
        // said last (#3862 phase 4: Talk and typed questions go to the call while it is live). Just me never reaches the
        // activity: its answers are neither.
        let roomMissing = RecordingSync.shared.roomMissing.contains(id)
        if answersOf != id {
            answersOf = id
            answersSeen = [:]
        }
        let asked = MeetingAsk.shared.exchanges(for: id).last(where: { $0.answer != nil })
        let said = MeetingCall.shared.answer(for: id)
        let answered = Self.newest([
            asked.flatMap { e in e.answer.map { Answer(text: $0, at: seen(.ask, e.id.uuidString, now: now)) } },
            said.map { Answer(text: $0.text, at: seen(.call, $0.id, now: now)) },
        ])
        let topic = entry.attendance?.liveView == true ? MeetingTopic.current(MeetingConversation.shared.meeting(id).topics)?.title : nil
        // Talk (#3862 phase 4): whether a span is open, read here so the activity follows it at once.
        let talking = MeetingCall.shared.talk?.recording == id
        // Hub transcription (#3862 phase 5): the hub's latest line while it transcribes, else the phone's own, said so.
        let transcription = MeetingTranscription.shared
        let hub = transcription.meeting(id)
        let transcribed = hub.hubMode
            ? Transcribed(hubLine: hub.lines.last?.text, shown: transcription.shown(id, role: entry.attendance?.role ?? .off)) : nil
        let content = Self.content(entry: entry, recording: state == .recording, interrupted: interrupted,
                                   elapsed: capture.timeline, previous: previous, now: now,
                                   answer: answered?.text, answeredAt: answered?.at,
                                   topic: topic, roomMissing: roomMissing, talking: talking, transcribed: transcribed)
        // The answer gives way to the latest line 60 s after it came: looked at again then.
        if content.answer != nil, entry.attendance?.liveView == true, let at = answered?.at,
           at.addingTimeInterval(Self.answerShown) > now {
            wake(at: at.addingTimeInterval(Self.answerShown))
        }
        if let previous {
            guard previous != content else { return }
            let hold = Self.hold(content, after: previous, sentAt: sentAt, now: now)
            guard hold <= 0 else {
                wake(at: now.addingTimeInterval(hold))
                return
            }
            shown = (id, content); sentAt = now
            if let activity = activity(id) { update(activity, content) } else { request(id, content) }
        } else {
            shown = (id, content); sentAt = now
            request(id, content)
        }
    }

    /// Where an answer on the third line came from.
    enum AnswerSource: Hashable { case ask, call }

    /// A hub-mode meeting's line (#3862 phase 5): the hub's latest, and what the page shows of the hub
    /// (`MeetingTranscriptionRule.Shown`): its lines while it is up and healthy, else the phone's own with the notice.
    nonisolated struct Transcribed: Equatable {
        var hubLine: String?
        var shown: MeetingTranscriptionRule.Shown
    }

    /// An answer and when this phone first had it.
    nonisolated struct Answer: Equatable {
        var text: String
        var at: Date
    }

    /// The newest of the sources' latest answers (pure, for the tests); the later one wins a tie.
    nonisolated static func newest(_ answers: [Answer?]) -> Answer? {
        answers.compactMap { $0 }.reduce(nil) { best, next in best.map { next.at >= $0.at ? next : $0 } ?? next }
    }

    /// When this phone first had `key` as `source`'s latest answer.
    private func seen(_ source: AnswerSource, _ key: String, now: Date) -> Date {
        if let known = answersSeen[source], known.key == key { return known.at }
        answersSeen[source] = (key, now)
        return now
    }

    /// Syncs again at `date` (or earlier, when a sync is already due before it: that one sets the next).
    private func wake(at date: Date) {
        if let wake, wake.at <= date { return }
        wake?.task.cancel()
        let task = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .seconds(max(0, date.timeIntervalSinceNow)))
            guard !Task.isCancelled, let self else { return }
            self.wake = nil
            self.sync()
        }
        wake = (task, date)
    }

    /// What the activity shows for `entry`. Recording on, the time keeps counting from the start it had (`previous`), so
    /// the many saves of a running recording (each segment, each transcript) change nothing on screen. `roomMissing`: a
    /// room meeting an older manager took with Persona off (`RecordingSync.roomMissing`, #3862) shows as not attended,
    /// as the meeting page says, with no Ask or Turn Off.
    ///
    /// With the live view (#3862 phase 2, `RecordingAttendance.liveView`) and Persona attending, `topic` (the room's
    /// current one) and the live log's latest line come too, and the third line is the answer (`answeredAt`: when it came)
    /// for 60 s, then the latest line; without a line yet the answer stays, as it always does without the live view.
    ///
    /// A room meeting Persona can be asked in has Talk (#3862 phase 4, `MeetingCallRule.talkOffered`): `talking` false,
    /// or true while a span is open (`talking`), which the Lock Screen says ("Listening to you…") in place of the third
    /// line; every other recording has nil, and Ask Persona as before.
    ///
    /// A meeting the hub transcribes (#3862 phase 5, `transcribed`): its line is the hub's latest while the page shows the
    /// hub's lines, else the phone's own, with `fellBack` once transcription has fallen back to this iPhone (the Lock
    /// Screen says "On iPhone: …").
    nonisolated static func content(entry: RecordingEntry, recording: Bool, interrupted: Bool, elapsed: Double,
                                    previous: Content?, now: Date, answer: String? = nil, answeredAt: Date? = nil,
                                    topic: String? = nil, roomMissing: Bool = false, talking: Bool = false,
                                    transcribed: Transcribed? = nil) -> Content {
        let phase: Content.Phase = recording ? .recording : interrupted ? .interrupted : .paused
        let keep = recording && previous?.phase == .recording
        let since = recording ? (keep ? previous?.runningSince : nil) ?? now.timeIntervalSince1970 - elapsed : nil
        let attended = MeetingAsk.canAsk(entry, roomMissing: roomMissing)
        let liveView = attended && entry.attendance?.liveView == true
        let answer = attended ? Self.cut(answer, Self.answerLength) : nil
        let hubLines = transcribed?.shown.hubLines == true
        let line = liveView ? Self.cut(hubLines ? transcribed?.hubLine : entry.attendance?.live.last?.text, Self.lineLength) : nil
        let fresh = answeredAt.map { now.timeIntervalSince($0) < Self.answerShown } ?? true
        let showsAnswer = answer != nil && (line == nil || fresh)
        let fellBack = !showsAnswer && line != nil && !hubLines && transcribed?.shown.fellBack == true
        return Content(phase: phase, runningSince: since, elapsed: keep ? previous?.elapsed ?? elapsed : elapsed,
                       title: String(entry.displayTitle.prefix(Self.titleLength)), meeting: entry.isMeeting, marks: entry.marks.count,
                       persona: attended ? entry.attendance?.role.rawValue : nil,
                       answer: showsAnswer ? answer : nil,
                       topic: liveView ? Self.cut(topic, Self.topicLength) : nil,
                       line: showsAnswer ? nil : line,
                       talking: MeetingCallRule.talkOffered(entry, roomMissing: roomMissing) ? recording && talking : nil,
                       fellBack: fellBack ? true : nil)
    }

    /// How long to hold `next` back when `shown` went out at `sentAt` (pure, for the tests): an answer, a phase, the
    /// title, the bookmarks, Persona's part go at once; a change of only the topic or the line waits until `throttle`
    /// after the last update, so a meeting's lines never send more than one update every 5 seconds.
    nonisolated static func hold(_ next: Content, after shown: Content, sentAt: Date?, now: Date) -> TimeInterval {
        var rest = next
        rest.topic = shown.topic
        rest.line = shown.line
        guard rest == shown, let sentAt else { return 0 }
        return max(0, Self.throttle - now.timeIntervalSince(sentAt))
    }

    /// `text` trimmed and cut to `length` characters; nil when nothing is left.
    private nonisolated static func cut(_ text: String?, _ length: Int) -> String? {
        guard let text = text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return nil }
        return String(text.prefix(length))
    }

    /// The most of an answer the activity carries (it shows two or three lines of it).
    nonisolated static let answerLength = 160
    /// The most of the current topic and of the latest line (the contract's 60 and 120 characters), and of the title,
    /// which shows on one line: together they keep the state far under ActivityKit's 4 KB.
    nonisolated static let topicLength = 60
    nonisolated static let lineLength = 120
    nonisolated static let titleLength = 100
    /// The third line is the latest answer this long after it came, then the latest line.
    nonisolated static let answerShown: TimeInterval = 60
    /// A new line or topic updates the activity at most this often.
    nonisolated static let throttle: TimeInterval = 5

    /// While recording, a higher relevance than the Fleet activity's (1 at most): the island shows the recording.
    nonisolated static let relevance: Double = 2

    private func activity(_ id: UUID) -> Activity<RecordingActivityAttributes>? {
        Activity<RecordingActivityAttributes>.activities.first {
            $0.attributes.recordingID == id.uuidString && ($0.activityState == .active || $0.activityState == .stale)
        }
    }

    private func request(_ id: UUID, _ content: Content) {
        // One at a time: another left over (an app killed while recording) goes.
        for other in Activity<RecordingActivityAttributes>.activities
        where other.attributes.recordingID != id.uuidString && (other.activityState == .active || other.activityState == .stale) {
            end(other)
        }
        guard activity(id) == nil else { return }
        do {
            _ = try Activity.request(attributes: RecordingActivityAttributes(recordingID: id.uuidString),
                                     content: ActivityContent(state: content, staleDate: nil, relevanceScore: Self.relevance),
                                     pushType: nil)
        } catch {
            // Live Activities off for Persona in Settings: the recording goes on without one.
            log.notice("recording activity not started: \(error.localizedDescription, privacy: .public)")
        }
    }

    private func update(_ activity: Activity<RecordingActivityAttributes>, _ content: Content) {
        nonisolated(unsafe) let a = activity
        let state = ActivityContent(state: content, staleDate: nil, relevanceScore: Self.relevance)
        Task { await a.update(state) }
    }

    private func endAll() {
        shown = nil; sentAt = nil; answersSeen = [:]; answersOf = nil
        wake?.task.cancel(); wake = nil
        for activity in Activity<RecordingActivityAttributes>.activities
        where activity.activityState == .active || activity.activityState == .stale {
            end(activity)
        }
    }

    private func end(_ activity: Activity<RecordingActivityAttributes>) {
        nonisolated(unsafe) let a = activity
        Task { await a.end(nil, dismissalPolicy: .immediate) }
    }
}
#endif
