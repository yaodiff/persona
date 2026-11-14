import ActivityKit
import SwiftUI
import WidgetKit

/// Draws the recording activity (docs/apple/live-activity.md「Recording」): on the Lock Screen its name, what it is (a meeting,
/// bookmarks, paused by a call; with the live view the current topic), the time, the third line (Persona's answer or the
/// latest line) and Bookmark / Stop; in the Dynamic Island a red mark and the time, opened up the same. The time is the
/// system's (`Text(timerInterval:)`), so a recording that runs on needs no updates.
/// Colors are fixed (`Palette`): the extension builds neither the app's Theme nor its style.
struct RecordingLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: RecordingActivityAttributes.self) { context in
            RecordingLockScreen(state: context.state, id: context.attributes.recordingID)
                // The lock screen is cut at about 160 pt: larger text would push the buttons off it.
                .dynamicTypeSize(...DynamicTypeSize.large)
                .activitySystemActionForegroundColor(Palette.recordingRed)
                .environment(\.locale, LanguageStore.drawingLocale)
                .widgetURL(RecordingLink.page)
        } dynamicIsland: { context in
            let state = context.state
            let id = context.attributes.recordingID
            return DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    RecordingMark(phase: state.phase, size: IslandMetrics.expandedFace)
                        .padding(.leading, IslandMetrics.edge)
                }
                // The time in the corner, wide enough for "1:23:45": beside a title in the center region it was cut to "…".
                DynamicIslandExpandedRegion(.trailing) {
                    RecordingTime(state: state)
                        .font(.title3.weight(.semibold))
                        .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .white)
                        .frame(minWidth: IslandMetrics.recordingExpandedTimerWidth, alignment: .trailing)
                        .padding(.trailing, IslandMetrics.edge)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    VStack(alignment: .leading, spacing: IslandMetrics.rowGap) {
                        VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                            Text(state.title).font(.headline).lineLimit(1)
                            RecordingSubtitle(state: state).font(.caption.weight(.medium)).foregroundStyle(.white.opacity(0.8)).lineLimit(1)
                        }
                        .padding(.horizontal, IslandMetrics.gap)
                        RecordingAnswer(state: state).foregroundStyle(.white.opacity(0.85)).padding(.horizontal, IslandMetrics.gap)
                        RecordingButtons(id: id, onDark: true, attends: state.persona != nil, recording: state.phase == .recording,
                                         talking: state.talking)
                            .padding(.horizontal, IslandMetrics.recordingIslandButtonsInset)
                    }
                    .environment(\.locale, LanguageStore.drawingLocale)
                }
            } compactLeading: {
                IslandWidth { _ in
                    RecordingMark(phase: state.phase, size: IslandMetrics.face, talking: state.talking == true)
                        .padding(.leading, IslandMetrics.edge)
                }
            } compactTrailing: {
                IslandWidth { limited in
                    if !limited {
                        RecordingTime(state: state)
                            .font(.system(size: IslandMetrics.timerNumber, weight: .semibold))
                            .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .white)
                            // `Text(timerInterval:)` takes all the width it is offered.
                            .frame(width: IslandMetrics.recordingTimerWidth, alignment: .trailing)
                            .padding(.trailing, IslandMetrics.edge)
                    }
                }
            } minimal: {
                RecordingMark(phase: state.phase, size: IslandMetrics.limitedContent, talking: state.talking == true)
            }
            .widgetURL(RecordingLink.page)
            .keylineTint(Palette.recordingRed)
        }
        .supplementalActivityFamilies([.small])
    }
}

/// The recording's mark: a red disc with a waveform while it records, a pause sign while it doesn't, and the talk mark
/// while a talk window is open (#3862 phase 4; still, no repeating animation: they don't run in Live Activities).
struct RecordingMark: View {
    var phase: RecordingActivityAttributes.ContentState.Phase
    var size: CGFloat
    var talking = false

    private var symbol: String {
        guard phase == .recording else { return RecordingSymbol.paused }
        return talking ? RecordingSymbol.talking : RecordingSymbol.recording
    }

    var body: some View {
        ZStack {
            Circle().fill(Palette.recordingRed.opacity(phase == .recording ? 1 : 0.6))
            Image(systemName: symbol)
                .font(.system(size: size * 0.48, weight: .bold))
                .foregroundStyle(.white)
        }
        .frame(width: size, height: size)
        .accessibilityHidden(true)
    }
}

/// The recorded time: counted by the system while recording, standing still while paused.
struct RecordingTime: View {
    var state: RecordingActivityAttributes.ContentState

    var body: some View {
        Group {
            if state.phase == .recording, let since = state.runningSinceDate {
                Text(timerInterval: since...Date.distantFuture, countsDown: false, showsHours: true)
            } else {
                Text(Duration.seconds(Int(state.elapsed)),
                     format: .time(pattern: state.elapsed >= 3600 ? .hourMinuteSecond : .minuteSecond))
            }
        }
        .monospacedDigit()
        .multilineTextAlignment(.trailing)
        .lineLimit(1)
    }
}

/// Under the name: a meeting, its bookmarks; or why it isn't recording.
struct RecordingSubtitle: View {
    var state: RecordingActivityAttributes.ContentState

    /// The role and the current topic (#3862 phase 2): while it records, Persona attends and the room has named one. On
    /// the Lock Screen they take up to two lines, so the third line takes one (`RecordingAnswer.lockLines`).
    static func showsTopic(_ state: RecordingActivityAttributes.ContentState) -> Bool {
        state.phase == .recording && state.persona != nil && state.topic?.isEmpty == false
    }

    var body: some View {
        switch state.phase {
        case .interrupted: Text("Paused by a call · Open Persona to continue")
        case .paused: Text("Paused")
        case .recording:
            if let persona = state.persona {
                // Persona attends (#2981): said in place of what it is, so whoever sees the phone knows (the bookmarks
                // stay on the recording's page). With the live view (#3862 phase 2) the current topic follows it, so a long
                // one is what gets cut (on the Lock Screen after a second line, in the island after the first), never the
                // role.
                if let topic = state.topic, !topic.isEmpty {
                    Text("\(RecordingRole.text(persona)) · \(Text("Now: \(topic) [Meeting Topic]"))")
                } else {
                    RecordingRole(persona: persona)
                }
            } else if state.marks > 0 {
                Text(state.meeting ? "Meeting · \(state.marks) bookmarks" : "Recording · \(state.marks) bookmarks")
            } else {
                Text(state.meeting ? "Meeting recording" : "Recording on iPhone")
            }
        }
    }
}

/// Bookmark and Stop: neutral and red, white words on the red (as the Fleet activity's Allow on blue). A meeting Persona
/// attends (#2981) leads with Ask Persona (a tap asks about what was just said), and Bookmark, Turn Off (Persona stops
/// attending) and Stop give up their words for their marks so the four fit; each keeps its name for VoiceOver. A room
/// meeting with Talk (#3862 phase 4, `talking` not nil) leads with Talk instead: a tap opens a talk window, and while it
/// is open the button reads Done and ends it (each tap says which it was, `TalkInMeetingIntent.open`).
struct RecordingButtons: View {
    var id: String
    var onDark = false
    /// Persona attends: Turn Off joins the row, and while it records, Ask Persona (or Talk).
    var attends = false
    var recording = true
    /// The meeting's Talk: nil without it, true while a talk window is open.
    var talking: Bool? = nil

    var body: some View {
        HStack(spacing: IslandMetrics.buttonGap) {
            if attends && recording {
                Group {
                    if let talking {
                        // What the button shows goes with the tap: a stale Done can't open a window.
                        Button(intent: TalkInMeetingIntent(recordingID: id, open: !talking)) {
                            if talking { label("Done [Talk Window]", RecordingSymbol.talking) } else { label("Talk [Meeting Talk]", RecordingSymbol.talk) }
                        }
                    } else {
                        Button(intent: AskPersonaInMeetingIntent(recordingID: id)) { label("Ask Persona", RecordingSymbol.ask) }
                    }
                }
                .buttonStyle(.bordered)
                .tint(onDark ? .white : .primary)
                .layoutPriority(1)
            }
            Button(intent: AddRecordingBookmarkIntent(recordingID: id)) { label("Bookmark", RecordingSymbol.bookmark, iconOnly: attends) }
                .buttonStyle(.bordered)
                .tint(onDark ? .white : .primary)
            if attends {
                Button(intent: TurnOffMeetingAttendanceIntent(recordingID: id)) {
                    label("Turn Off Persona", RecordingSymbol.attendanceOff, iconOnly: true)
                }
                .buttonStyle(.bordered)
                .tint(onDark ? .white : .primary)
            }
            Button(intent: StopRecordingIntent(recordingID: id)) { label("Stop", RecordingSymbol.stop, iconOnly: attends) }
                .buttonStyle(.borderedProminent)
                .tint(Palette.recordingButton)
                .foregroundStyle(.white)
        }
        .buttonBorderShape(.capsule)
    }

    @ViewBuilder private func label(_ text: LocalizedStringKey, _ symbol: String, iconOnly: Bool = false) -> some View {
        if iconOnly {
            Label(text, systemImage: symbol)
                .labelStyle(.iconOnly)
                .font(.subheadline.weight(.semibold))
                .frame(minWidth: IslandMetrics.recordingIconButton)
        } else {
            Label(text, systemImage: symbol)
                .font(.subheadline.weight(.semibold))
                .lineLimit(1)
                .minimumScaleFactor(0.8)
                .frame(maxWidth: .infinity)
        }
    }
}

/// "Persona taking part" / "Persona listening": the role, said wherever Persona attends.
struct RecordingRole: View {
    var persona: String

    var body: some View { Self.text(persona) }

    static func text(_ persona: String) -> Text { Text(persona == "participant" ? "Persona taking part" : "Persona listening") }
}

/// The third line, in a meeting Persona attends: its latest answer (the hub posts no notification for answers; with the
/// live view only for 60 s after it came, `RecordingActivity.content`), else the meeting's latest line (#3862 phase 2),
/// or, while the recording is paused and the subtitle says why, that Persona still attends. Two lines at most, and one
/// on the Lock Screen beside a topic (`lockLines`), so the Lock Screen keeps within its height (docs/apple/live-activity.md
/// 「Pitfalls」). Never Just me: it isn't in the state.
struct RecordingAnswer: View {
    var state: RecordingActivityAttributes.ContentState
    /// The most lines the answer or the line takes.
    var lines = 2

    /// Its lines on the Lock Screen: one while the subtitle carries the current topic (#3862 phase 2), which may take two
    /// lines: two lines each passed the ~160 pt. Not the subtitle cut to one line instead: beside the role (never cut)
    /// it left a character or two of a Chinese topic. Else two, as before the live view (#2981).
    static func lockLines(_ state: RecordingActivityAttributes.ContentState) -> Int {
        RecordingSubtitle.showsTopic(state) ? 1 : 2
    }

    var body: some View {
        if let persona = state.persona {
            if state.talking == true, state.phase == .recording {
                // A talk window is open (#3862 phase 4): what Persona hears now, in place of the answer or the line.
                Label { Text("Listening to you…").lineLimit(1) } icon: { Image(systemName: RecordingSymbol.talking) }
                    .font(.caption.weight(.medium))
            } else if let answer = state.answer, !answer.isEmpty {
                Label { Text(answer).lineLimit(lines) } icon: { Image(systemName: RecordingSymbol.persona) }
                    .font(.caption.weight(.medium))
                    .accessibilityLabel(Text("Persona: \(answer)"))
            } else if state.phase != .recording {
                Label { RecordingRole(persona: persona).lineLimit(1) } icon: { Image(systemName: RecordingSymbol.persona) }
                    .font(.caption.weight(.medium))
            } else if let line = state.line, !line.isEmpty {
                if state.fellBack == true {
                    // Hub transcription fell back (#3862 phase 5): the line is this iPhone's own, and says so in words.
                    Label { Text("On iPhone: \(line) [Recording Line]").lineLimit(lines) } icon: { Image(systemName: RecordingSymbol.phone) }
                        .font(.caption.weight(.medium))
                } else {
                    Label { Text(line).lineLimit(lines) } icon: { Image(systemName: RecordingSymbol.line) }
                        .font(.caption.weight(.medium))
                }
            }
        }
    }
}

/// The Lock Screen: mark, name and what it is, the time; under them the buttons. The watch's Smart Stack (`.small`) gets
/// a glance: mark, name, time, no buttons (they wouldn't fit).
struct RecordingLockScreen: View {
    var state: RecordingActivityAttributes.ContentState
    var id: String
    @Environment(\.activityFamily) private var family

    var body: some View {
        if family == .small { glance } else { full }
    }

    private var glance: some View {
        HStack(spacing: IslandMetrics.rowGap) {
            RecordingMark(phase: state.phase, size: IslandMetrics.face)
            VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                Text(state.title).font(.footnote.weight(.semibold)).lineLimit(1)
                RecordingTime(state: state).font(.footnote).foregroundStyle(state.phase == .recording ? Palette.recordingRed : .secondary)
            }
            Spacer(minLength: 0)
        }
        .padding(IslandMetrics.rowGap)
    }

    private var full: some View {
        VStack(alignment: .leading, spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
            HStack(spacing: IslandMetrics.rowGap + IslandMetrics.gap) {
                RecordingMark(phase: state.phase, size: IslandMetrics.recordingMark)
                VStack(alignment: .leading, spacing: IslandMetrics.smallGap) {
                    Text(state.title).font(.headline).lineLimit(1)
                    RecordingSubtitle(state: state).font(.caption.weight(.medium)).foregroundStyle(.secondary).lineLimit(2).fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: IslandMetrics.gap)
                RecordingTime(state: state)
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(state.phase == .recording ? Palette.recordingRed : .secondary)
                    // It takes all the width it is offered: fixed, the name and the subtitle keep the rest of the row.
                    .frame(width: IslandMetrics.recordingLockTimerWidth, alignment: .trailing)
            }
            RecordingAnswer(state: state, lines: RecordingAnswer.lockLines(state)).foregroundStyle(.secondary)
            RecordingButtons(id: id, attends: state.persona != nil, recording: state.phase == .recording, talking: state.talking)
        }
        .padding(IslandMetrics.lockPadding)
    }
}

/// The recording activity's symbols (the extension doesn't build `Theme.Icon`): the app's own for the same meanings.
enum RecordingSymbol {
    /// The app's recording symbol (`Theme.Icon.recording`): the Control's.
    static let recordingAction = "waveform.circle"
    /// On the red mark the disc is drawn already: the bare waveform.
    static let recording = "waveform"
    static let paused = "pause.fill"
    /// The app's bookmark (`Theme.Icon.pin`).
    static let bookmark = "pin"
    static let stop = "stop.fill"
    /// The app's question (`Theme.Icon.question`): Ask Persona in a meeting it attends.
    static let ask = "questionmark.bubble"
    /// The app's Persona stops attending (`Theme.Icon.attendanceOff`).
    static let attendanceOff = "waveform.slash"
    /// The app's Persona (`Theme.Icon.persona`): its answer.
    static let persona = "sparkle"
    /// The app's quote (`Theme.Icon.quote`): the meeting's latest line (#3862 phase 2).
    static let line = "text.quote"
    /// The app's iPhone (`Theme.Icon.phone`): the line is this iPhone's own, hub transcription having fallen back (#3862
    /// phase 5).
    static let phone = "iphone"
    /// The app's Talk (`Theme.Icon.talk`, #3862 phase 4): the button that opens a talk window.
    static let talk = "mic.circle"
    /// The talk mark: on the red disc while a talk window is open, and its Done (the bare microphone, as the disc is drawn).
    static let talking = "mic.fill"
}
