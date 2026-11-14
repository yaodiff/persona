#if os(iOS) || os(visionOS)
import SwiftUI
#if os(iOS)
import AVFoundation
#endif

/// Knowledge › Record (docs/apple/recording.md): start a recording, see this device's recordings (the watch's arrive here) with
/// where each one is (recording, transcribing, synced), open one for its transcript. The text goes to the knowledge base
/// by itself (`RecordingSync`); nothing is added to a composer.
struct RecordingsSection: View {
    #if os(iOS)
    @Environment(AppModel.self) private var model
    /// The next recording's choices, shared with the page's bottom bar when there is one (`RecordingsPage`).
    var form: RecordingStartForm?
    /// Record (or the running recording) is in the page's bottom bar, under the thumb (#5522): the section keeps the options.
    var startAtBottom = false
    #endif
    @State private var library = RecordingLibrary.shared
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared
    @State private var capture = RecordingCapture.shared
    /// Which day is today's and whether a paired watch has Persona, worked out outside `body` (P4).
    @State private var days = RecordingDayBoard.shared
    #if os(iOS)
    /// The start's choices when the page has none to share (Knowledge › Recordings): the line under Record reads them too.
    @State private var ownForm = RecordingStartForm()
    /// The speech service a hub-mode meeting's audio goes to, when the owner chose another extension's (#3862 phase 5):
    /// the line under Record names it. nil: the built-in one.
    @State private var transcriptionService: String?
    #endif
    /// The recording a swipe asked to delete (its row shows the confirmation), and why a delete failed.
    @State private var pendingDelete: UUID?
    @State private var failure: String?

    var body: some View {
        // The recording being made is the now-recording card, not a row of the list below it.
        let capturing = capture.state == .recording || capture.state == .paused
        // Today's all-day recording is the Today section's row, not one of the list below it (C30).
        #if os(iOS)
        let today = days.todayID
        #else
        let today: UUID? = nil
        #endif
        let recents = library.entries.filter { !(capturing && $0.id == capture.recordingID) && $0.id != today }
        Section {
            #if os(iOS)
            RecordingCaptureControls(part: startAtBottom ? .options : .inline, sharedForm: form ?? ownForm)
            #else
            RecordingCaptureControls()
            #endif
        } footer: {
            // Read once, before a recording: not under the controls of one that runs.
            if capture.state == .idle {
                #if os(iOS)
                Text(startPrivacy)
                    .accessibilityIdentifier("recording-start-privacy")
                #else
                Text("Audio stays on this device. Its text syncs to Knowledge and is kept for \(sync.store.days) days.")
                #endif
            }
        }
        #if os(iOS)
        // Whether the hub's voice extension takes meeting calls, for the line under Record (#3862 phase 4).
        .task(id: MeetingAttendees.conversation(model)) {
            guard MeetingAttendees.conversation(model), let hub = model.hub else { return }
            await VoiceSupport.shared.refresh(group: hub.group, api: hub.api)
        }
        // Hub transcription (#3862 phase 5): whether the hub can transcribe now (the start offers On the Hub only then),
        // read again as the page opens, and the speech service the line under Record names when it isn't the built-in one.
        .task(id: MeetingAttendees.transcription(model)) {
            guard MeetingAttendees.transcription(model), let api = model.api else { return }
            await model.refreshAssistant()
            let ref = model.assistantConfig?.meetingTranscriptionExtension ?? ""
            var name: String?
            if !ref.isEmpty {
                let listed = (try? await api.currentAssistantSettings().plugins()) ?? []
                name = MeetingTranscriptionSettings.service(ref, choices: MeetingTranscriptionSettings.choices(listed))
            }
            transcriptionService = name
            // The meeting page's Talk line names it too.
            MeetingTranscription.shared.noteService(name)
        }
        #endif
        #if os(iOS)
        // All-day recording: with a paired watch that has Persona (it offers the mode whatever the hub supports: the
        // phone keeps the text until the hub takes it, docs/assistant/day-recap.md「Compatibility」), and once a day
        // exists. Without either, the page is as it always was.
        let dayOffered = RecordingDayDefaults.offered(model.hubFeatures)
        if today != nil || days.watchAppInstalled {
            RecordingDayTodaySection(dayID: today, offered: dayOffered)
        }
        #endif
        Section {
            if recents.isEmpty, !capturing, today == nil {
                EmptyStateView(symbol: Theme.Icon.recording, title: String(localized: "No Recordings Yet"),
                               message: String(localized: "Record meetings, dictation and on-site notes with your phone or Apple Watch; the transcribed text goes into Knowledge."))
                    .listRowBackground(Color.clear)
            }
            ForEach(recents) { entry in
                if entry.isDay {
                    // A day is deleted on its page: it stops the watch and deletes every part on the hub too.
                    NavigationLink { RecordingDayView(id: entry.id) } label: { RecordingDayRow(entry: entry) }
                        .accessibilityIdentifier("recording-day-row")
                } else {
                    NavigationLink { RecordingDetailView(id: entry.id) } label: { RecordingRow(entry: entry) }
                        .accessibilityIdentifier("recording-row")
                        // Only the swiped row's dialog shows, so its popover points at that row.
                        .confirmationDialog("Delete This Recording?", isPresented: Binding(get: { pendingDelete == entry.id }, set: { if !$0, pendingDelete == entry.id { pendingDelete = nil } }),
                                            titleVisibility: .visible) {
                            Button("Delete", role: .destructive) { failure = RecordingDeletion.run(entry.id) }
                        } message: { Text("The audio on this device will be deleted now. Its text in Knowledge is removed after sync succeeds; if Knowledge is off or read-only, that deletion stays pending.") }
                        .swipeActions(edge: .trailing) {
                            // A recording the watch is still making has no Delete, as on its page.
                            if !entry.active {
                                // No `.destructive` role: it takes the row off the screen at once, before the confirmation (red comes from the tint).
                                Button { pendingDelete = entry.id } label: {
                                    Label("Delete", systemImage: Theme.Icon.delete)
                                }
                                .tint(Theme.danger)
                                .accessibilityIdentifier("recording-row-delete")
                            }
                        }
                }
            }
        } header: {
            if !recents.isEmpty {
                HStack(spacing: Theme.Space.sm) {
                    Text("Recent Recordings")
                    CountBadge(count: recents.count)
                }
                .alert("Couldn't Finish", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
                    Button("OK [Recordings Section de32e2]") { failure = nil }.lineLimit(1)
                } message: { Text(failure ?? "") }
            }
        } footer: {
            if let problem = sync.problem ?? library.error {
                Label(problem, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger)
                    .accessibilityIdentifier("recording-sync-problem")
            }
        }
        .task {
            PhoneRecordingReceiver.shared.start()
            transcription.resumePending()
            sync.kick(reconcile: true)
            RecordingRollingKeeper.shared.schedule()
            #if DEBUG && os(iOS)
            // `-showDaySample today|paused|recap|pending|failed|consent`: made-up days (screenshots, UI tests); without it
            // an earlier run's go. Once per launch.
            // The launch starts it too (PersonaApp, beside the meeting samples), so it is usually done by now.
            await RecordingSamples.installDaySampleIfAsked()
            #endif
        }
        #if os(iOS)
        // Yesterday leaves the Today section at midnight, and the section comes once a paired watch has Persona.
        .tabActiveTask { await days.followMidnight() }
        .tabActiveTask { await days.followWatch() }
        #endif
    }
}

#if os(iOS)
extension RecordingsSection {
    /// The line under Record: what leaves the phone, the contract's Wording per mode (docs/assistant/meeting-minutes.md
    /// 「Privacy per mode」), before the meeting starts. A meeting about to start in hub mode (#3862 phase 5: picked, or
    /// the hub's default while it is ready) sends its audio through the hub to its speech service, named when the owner
    /// chose another extension's. Otherwise, where the next meeting's Talk would send its spans to the voice service (a
    /// room meeting taking part, on a hub whose voice extension takes meeting calls; phase 4), privacy mode's words;
    /// elsewhere the audio stays on this device, as before.
    private var startPrivacy: String {
        let form = form ?? ownForm
        let role = form.pickedRole ?? MeetingAttendees.defaultRole(model)
        let voice = model.hub.flatMap { MeetingCall.shared.voiceExtension(group: $0.group) }
        let meeting = form.pickedKind == .meeting
        let mode = meeting && MeetingAttendees.conversation(model)
            ? MeetingAttendees.startTranscription(model, picked: MeetingAttendees.offersHub(model) ? form.pickedTranscription : nil) : nil
        let talk = meeting && MeetingCallRule.talkSendsAudio(conversation: MeetingAttendees.conversation(model), role: role, voice: voice)
        return MeetingTranscriptionWords.startPrivacy(mode, service: transcriptionService, talk: talk, days: sync.store.days)
    }
}

/// An option row's title with what it means under it, both starting at the title's edge (not the icon's).
private struct RowTitle: View {
    var title: LocalizedStringKey
    var detail: String?
    var detailID: String?

    init(_ title: LocalizedStringKey, detail: String? = nil, detailID: String? = nil) {
        self.title = title
        self.detail = detail
        self.detailID = detailID
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title)
            if let detail {
                Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier(detailID ?? "")
            }
        }
    }
}
#endif

/// Deleting a recording, the one way for the list's swipe and the recording's page: stop its transcription, then delete it here
/// and (once sync allows) in Knowledge.
enum RecordingDeletion {
    /// nil: deleted; otherwise the words for the alert.
    @MainActor static func run(_ id: UUID) -> String? {
        RecordingTranscription.shared.cancel(id)
        do { try RecordingSync.shared.delete(id); return nil } catch { return String(localized: "The recording wasn't deleted. Try again later.") }
    }
}

/// One recording: where it came from, how long, and where it is on its way to the knowledge base.
struct RecordingRow: View {
    var entry: RecordingEntry

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(entry.isMeeting ? Theme.Icon.meeting : Theme.Icon.recording).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                .scaledFrame(width: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(entry.displayTitle).font(Theme.Font.body).lineLimit(1)
                HStack(spacing: Theme.Space.xs) {
                    Text("\(entry.sourceTitle) · \(RecordingFormat.clock(entry.duration)) ·")
                    RecordingStatusText(entry: entry)
                }
                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
    }
}

/// Recording / Transcribing / Synced…: the one word for where a recording is.
struct RecordingStatusText: View {
    var entry: RecordingEntry
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared

    var body: some View {
        let (text, tone) = Self.status(entry, transcription: transcription, sync: sync)
        Text(text).foregroundStyle(tone).accessibilityIdentifier("recording-status")
    }

    static func status(_ entry: RecordingEntry, transcription: RecordingTranscription, sync: RecordingSync) -> (String, Color) {
        if entry.active {
            if entry.isRolling { return (String(localized: "Rolling recording"), Theme.danger) }
            return (String(localized: "Recording [Recordings Section e35a14]"), Theme.danger)
        }
        if let error = transcription.errors[entry.id] { return (error, Theme.danger) }
        if transcription.current == entry.id { return (String(localized: "Transcribing"), Theme.accent) }
        if !entry.receivedAll { return (entry.expectedSegments == 0 ? String(localized: "No audio recorded") : String(localized: "Waiting for Apple Watch"), .secondary) }
        switch sync.status(entry) {
        case .synced: return (entry.transcribedAll ? String(localized: "Synced to Knowledge") : String(localized: "Partly synced"), Theme.positive)
        case .uploading: return (String(localized: "Syncing"), Theme.accent)
        case .rejected: return (String(localized: "Not accepted by Knowledge"), Theme.danger)
        case .expired: return (String(localized: "Past retention"), .secondary)
        case .pending: return (entry.transcribedAll ? String(localized: "Waiting to sync") : String(localized: "Waiting to transcribe"), .secondary)
        case .waiting: return (entry.transcribedAll ? String(localized: "No speech detected") : String(localized: "Waiting to transcribe"), .secondary)
        }
    }
}

#if os(iOS)
/// The next recording's choices on the iPhone: what the option rows set and Record starts with. One object, so the
/// options in the list and Record in the page's bottom bar (#5522) read and change the same picks.
@Observable final class RecordingStartForm {
    /// Persona's part in the next meeting (#2981): the hub's default until one is picked here for it.
    var pickedRole: MeetingAttendance?
    var pickedAttendee: MeetingAttendees.Candidate?
    /// The next meeting's private mark (#3862): off unless turned on here, for that meeting only.
    var pickedPrivate = false
    /// Meeting unless Note is picked here, for the next start only (#5243): never remembered.
    var pickedKind = RecordingKindChoice.meeting
    /// How the next meeting is transcribed (#3862 phase 5): the hub's default until picked here, for that meeting only;
    /// offered only while the hub lists `meeting_transcription` and can transcribe.
    var pickedTranscription: MeetingTranscriptionMode?
    /// A start was tapped and recording has not begun (the calendar read, then the microphone): Record stays, busy.
    var startRequested = false
    /// The calendar's meeting on now (read again each minute while nothing records).
    var meetingNow: RecordingMeeting?
    var calendarAskable = false
    /// A Bluetooth microphone is on the route (the Microphone picker shows only then).
    var hasBluetooth = false
}
#endif

/// Start a recording on this phone (the watch has its own). Record is the one prominent button (#5243): a meeting, named
/// after the calendar event happening now (`MeetingRecorder`, docs/apple/recording.md「Meeting recordings」). Under it the
/// options are one row each, first Meeting / Note (Note, for this one start, records a plain note and hides the meeting's
/// options). While one runs, the controls are one row: the now-recording card, which opens the live sheet
/// (`RecordingLiveSheet`).
struct RecordingCaptureControls: View {
    #if os(iOS)
    /// Which part of the controls this view draws: all of them in place (Knowledge › Recordings), or, on the Recordings
    /// page, the options in the list and Record (or the running recording's card) in the bottom bar (#5522).
    enum Part { case inline, options, bar }
    var part: Part = .inline
    /// The choices shared with the other part; nil: this view's own.
    var sharedForm: RecordingStartForm?
    @State private var ownForm = RecordingStartForm()
    private var form: RecordingStartForm { sharedForm ?? ownForm }
    #endif
    @State private var capture = RecordingCapture.shared
    @State private var library = RecordingLibrary.shared
    @StoredValue(RecordingDefaults.localeKey) private var locale = RecordingDefaults.locale
    #if os(iOS)
    @StoredValue(RecordingInput.storageKey) private var inputRaw = RecordingInput.phone.rawValue
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    #endif

    var body: some View {
        #if os(iOS) // Bluetooth input is iPhone-only (`RecordingInput`); visionOS records with its own microphone
        if part == .bar {
            bar // the list's part reads the route and the calendar for both
        } else {
            listControls
        }
        #else
        controls
        #endif
    }

    #if os(iOS)
    private var listControls: some View {
        controls
            .task { // the Microphone picker shows only while a Bluetooth device (glasses, headset) is on the route
                refreshBluetooth()
                for await _ in NotificationCenter.default.notifications(named: AVAudioSession.routeChangeNotification) { refreshBluetooth() }
            }
            .task(id: capture.state == .idle) {
                while capture.state == .idle, !Task.isCancelled {
                    form.meetingNow = await MeetingCalendar.meetingOffMain()
                    form.calendarAskable = MeetingCalendar.canAsk
                    try? await Task.sleep(for: .seconds(60))
                }
            }
    }

    /// The page's bottom bar: Record and what the meeting will be called, or the running recording's card.
    @ViewBuilder private var bar: some View {
        if capture.state == .recording || capture.state == .paused {
            // A card as in the list it came from: in the bar it has no row behind it.
            nowRecording.card(padding: Theme.Space.m)
        } else {
            hero(busy: preparing != .idle)
        }
    }
    #endif

    /// The microphone a recording starts with: the Bluetooth one only when chosen and still connected.
    private var input: RecordingInput {
        #if os(iOS)
        form.hasBluetooth ? (RecordingInput(rawValue: inputRaw) ?? .phone) : .phone
        #else
        .phone
        #endif
    }

    @ViewBuilder private var controls: some View {
        #if os(iOS)
        if capture.state == .recording || capture.state == .paused {
            if part == .inline { nowRecording }
        } else {
            idleControls
        }
        banners
        #else
        legacyControls
        #endif
    }

    #if os(iOS)
    // MARK: Idle

    /// Where a start is: tapped and not yet recording, or in the capture's own `.starting`.
    private var preparing: RecordingPhase {
        RecordingPhase.derive(.init(startRequested: form.startRequested, capture: capture.state == .starting ? .starting : .idle))
    }

    @ViewBuilder private var idleControls: some View {
        let busy = preparing != .idle
        if part == .inline { hero(busy: busy) }
        if capture.state == .starting { startingRow }
        // Meeting or Note is a choice row under the one Record button, never a second button (#5243); a note has none
        // of the meeting's options (progressive disclosure).
        kindRow.disabled(busy)
        if form.pickedKind == .meeting, MeetingAttendees.offered(model) {
            personaRow.disabled(busy)
            if MeetingAttendees.conversation(model) {
                // The meeting's own room conversation attends (#3862): no session to pick, and the private mark instead.
                privateRow.disabled(busy)
                // On This iPhone or On the Hub (#3862 phase 5), only while the hub can transcribe: never a choice that
                // can't be had. The line under Record says what each sends.
                if MeetingAttendees.offersHub(model) { transcriptionRow.disabled(busy) }
            } else if role.attends {
                // Never the main chat by default (#3862): Choose until one is picked here or picked before (not the main
                // chat), and the meeting starts with Persona off without one.
                let attendee = self.attendee
                NavigationLink { MeetingAttendeePicker(selected: attendee?.key) { form.pickedAttendee = $0 } } label: {
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        MeetingAttendeeLabel(title: attendee?.title, detail: attendee?.detail)
                        if attendee == nil {
                            Text("Choose who attends. Until then Persona stays out of the meeting.")
                                .font(Theme.Font.caption).foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                                .accessibilityIdentifier("meeting-attendee-needed")
                        }
                    }
                }
                .disabled(busy)
                .accessibilityIdentifier("meeting-attendee")
            }
        }
        // With an icon like the rows above, so every title starts at one edge (#5522).
        Picker(selection: $locale) {
            Text("Chinese").tag("zh-CN")
            Text("English").tag("en-US")
            Text("Japanese").tag("ja-JP")
        } label: {
            Label { Text("Recording Language") } icon: { ThemedIcon(Theme.Icon.language) }
        }.font(Theme.Font.body).disabled(busy)
        if form.hasBluetooth {
            Picker(selection: $inputRaw) {
                ForEach(RecordingInput.allCases, id: \.rawValue) { Text($0.title).tag($0.rawValue) }
            } label: {
                Label { Text("Microphone") } icon: { ThemedIcon(Theme.Icon.dictate) }
            }.font(Theme.Font.body).disabled(busy)
        }
    }

    /// Record, and what a meeting will be called: the calendar's meeting on now, or a way to let it read the calendar (a
    /// note has no name from it). While the start is in flight the button keeps its label, shows a spinner beside it and
    /// is disabled.
    @ViewBuilder private func hero(busy: Bool) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Button { record() } label: {
                HStack(spacing: Theme.Space.s) {
                    // White like the title: the symbol took the tint, drew in the button's own blue and pushed the title off-center.
                    Label("Record [Recordings Section ef2821]", systemImage: Theme.Icon.recording).foregroundStyle(Theme.onSolid)
                    if busy { ProgressView() }
                }
                .font(Theme.Font.bodyStrong)
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .frame(maxWidth: .infinity)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
            }
            .themedButton(prominent: true)
            .disabled(busy)
            .accessibilityIdentifier("recording-meeting-start")
            if form.pickedKind == .meeting {
                calendarLine.frame(maxWidth: .infinity, alignment: part == .bar ? .center : .leading)
            }
        }
        .padding(.vertical, Theme.Space.xs)
    }

    /// What the meeting will be called: the calendar's meeting on now, or a way to let it read the calendar.
    @ViewBuilder private var calendarLine: some View {
        if let meeting = form.meetingNow, let title = meeting.title {
            Label(String(localized: "Calendar: \(title) · \(Self.span(meeting))"), systemImage: Theme.Icon.calendarEvent)
                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                .accessibilityIdentifier("recording-meeting-now")
        } else if form.calendarAskable {
            Button {
                Task {
                    _ = await MeetingCalendar.requestAccess()
                    form.calendarAskable = MeetingCalendar.canAsk
                    form.meetingNow = await MeetingCalendar.meetingOffMain()
                }
            } label: {
                Label("Name Recordings After Calendar Meetings", systemImage: Theme.Icon.calendarEvent).font(Theme.Font.caption)
                    .lineLimit(1).multilineTextAlignment(.leading)
            }
            .buttonStyle(.borderless)
        } else {
            Text("Named after the calendar event happening now, if there is one.").font(Theme.Font.caption).foregroundStyle(.secondary)
        }
    }

    /// The capture is `.starting` (the microphone): Cancel is possible only now. "Waiting for microphone access" is the
    /// detail line, not the whole block.
    private var startingRow: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.m) {
                Text("Starting…").font(Theme.Font.body)
                Spacer(minLength: Theme.Space.s)
                Button { capture.cancelStarting() } label: { Text("Cancel").lineLimit(1) }
                    .buttonStyle(.borderless)
                    .accessibilityIdentifier("recording-start-cancel")
            }
            Text("Waiting for microphone access").font(Theme.Font.caption).foregroundStyle(.secondary)
        }
        .transition(.opacity)
    }

    /// Meeting or Note for the next start: a menu row like Persona's, with what a note is under it.
    private var kindRow: some View {
        Picker(selection: Bindable(form).pickedKind) {
            ForEach(RecordingKindChoice.allCases, id: \.self) { Text($0.title).tag($0) }
        } label: {
            // What a note is, under the title rather than the icon: every row starts its words at one edge (#5522).
            Label {
                RowTitle("Type", detail: form.pickedKind == .note ? String(localized: "No minutes, and Persona stays out.") : nil,
                         detailID: "recording-kind-detail")
            } icon: { ThemedIcon(form.pickedKind.symbol) }
        }
        .pickerStyle(.menu)
        .font(Theme.Font.body)
        .accessibilityIdentifier("recording-kind")
    }

    /// Persona's part: one menu, the same control and label as while recording, with what it does under it.
    private var personaRow: some View {
        Picker(selection: Binding(get: { role }, set: { form.pickedRole = $0 })) {
            ForEach(MeetingAttendance.allCases, id: \.self) { Text($0.title).tag($0) }
        } label: {
            Label {
                RowTitle("Persona", detail: role.attends ? role.detail : nil, detailID: "meeting-attendance-detail")
            } icon: { ThemedIcon(Theme.Icon.persona) }
        }
        .pickerStyle(.menu)
        .font(Theme.Font.body)
        .accessibilityIdentifier("meeting-attendance-role")
    }

    /// Private Meeting (#3862): whether this meeting's minutes may be used when Persona answers in later ones.
    private var privateRow: some View {
        // The system switch, its words under the title as in the rows above (#5522).
        Toggle(isOn: Bindable(form).pickedPrivate) {
            Label {
                RowTitle("Private Meeting", detail: String(localized: "Its minutes are never used when Persona answers in later meetings."))
            } icon: { ThemedIcon(Theme.Icon.privateMeeting) }
        }
        .font(Theme.Font.body)
        .accessibilityIdentifier("meeting-private-start")
    }

    /// How the meeting is transcribed (#3862 phase 5): a menu row like Persona's, the hub's default until picked here.
    private var transcriptionRow: some View {
        Picker(selection: Binding(get: { MeetingAttendees.startTranscription(model, picked: form.pickedTranscription) ?? .device },
                                  set: { form.pickedTranscription = $0 })) {
            ForEach(MeetingTranscriptionMode.allCases, id: \.self) { Text(MeetingTranscriptionWords.title($0)).tag($0) }
        } label: {
            Label { RowTitle("Transcription [Meeting Transcription]") } icon: { ThemedIcon(Theme.Icon.meetingTranscription) }
        }
        .pickerStyle(.menu)
        .font(Theme.Font.body)
        .accessibilityIdentifier("meeting-transcription-start")
    }

    /// The role shown and started with: the one picked here, else the hub's default; on a hub without the meeting
    /// conversation, Off while nobody is chosen to attend (the main chat never attends by default, #3862).
    private var role: MeetingAttendance {
        if let picked = form.pickedRole { return picked }
        guard MeetingAttendees.conversation(model) || attendee != nil else { return .off }
        return MeetingAttendees.defaultRole(model)
    }

    // MARK: Recording

    /// The now-recording card: one tappable row with the recording's mark, title, time and state; it opens the live sheet.
    @ViewBuilder private var nowRecording: some View {
        let entry = capture.recordingID.flatMap(library.entry)
        let paused = capture.state == .paused
        Button { RecordingLivePresenter.shared.open() } label: {
            HStack(spacing: Theme.Space.m) {
                RecordingStateDot(paused: paused).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(entry?.displayTitle ?? String(localized: "Recording")).font(Theme.Font.bodyStrong)
                        .lineLimit(1)
                        .accessibilityIdentifier("recording-now-title")
                    Text(capture.interrupted ? String(localized: "Paused by a call") : paused ? String(localized: "Paused") : String(localized: "Recording [Recordings Section 5254d3]"))
                        .font(Theme.Font.secondary)
                        .foregroundStyle(paused ? Color.secondary : Palette.recordingRed)
                        .accessibilityIdentifier("recording-now-state")
                }
                Spacer(minLength: Theme.Space.s)
                clock(paused: paused)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityHint(Text("Opens the recording"))
        .accessibilityIdentifier("recording-now-card")
        .recordingMotion(reduced: reduceMotion, value: paused)
    }

    private func clock(paused: Bool) -> some View {
        RecordingClock(running: !paused, font: Theme.Font.bodyStrong)
            .foregroundStyle(paused ? Color.secondary : Color.primary)
    }

    // MARK: Problems

    /// What went wrong, or is worth knowing, as banners: no microphone permission has a way to Settings, not a dead button.
    @ViewBuilder private var banners: some View {
        if let error = capture.error {
            let denied = error == String(localized: "Allow Persona to use the microphone in Settings")
            Group {
                if denied {
                    NoticeBanner(text: error, tone: .danger) {
                        Button { openSettings() } label: { Text("Open Settings").lineLimit(1) }
                    }
                } else {
                    NoticeBanner(text: error, tone: .danger)
                }
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
        }
        if let notice = capture.notice {
            NoticeBanner(text: notice, tone: .neutral)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets())
        }
    }

    private func openSettings() {
        if let url = URL(string: UIApplication.openSettingsURLString) { UIApplication.shared.open(url) }
    }

    // MARK: Starting

    /// Record: a meeting with the options shown, or a note when Note is picked. The picks are for this start only.
    private func record() {
        // The transcription mode (#3862 phase 5): the one picked here while the hub can transcribe, else the hub's default
        // (`device` while it can't).
        let picked = MeetingAttendees.offersHub(model) ? form.pickedTranscription : nil
        let start = RecordingStart.make(form.pickedKind, offered: MeetingAttendees.offered(model), conversation: MeetingAttendees.conversation(model),
                                        photos: MeetingAttendees.photos(model), liveView: MeetingAttendees.liveView(model),
                                        highlights: MeetingAttendees.highlights(model),
                                        transcription: MeetingAttendees.startTranscription(model, picked: picked),
                                        role: role, privateMark: form.pickedPrivate,
                                        attendee: chosenAttendee)
        form.pickedRole = nil; form.pickedAttendee = nil; form.pickedPrivate = false; form.pickedKind = .meeting
        form.pickedTranscription = nil
        switch start {
        case .meeting(let attendance): startMeeting(attendance)
        case .note: startNote()
        }
    }

    private func startMeeting(_ attendance: RecordingAttendance?) {
        form.startRequested = true
        Task {
            await MeetingRecorder.shared.startMeeting(inForeground: true, openPage: false, attendance: attendance)
            form.startRequested = false
        }
    }

    private func startNote() {
        let chosen = input
        form.startRequested = true
        Task {
            await capture.start(locale: locale, input: chosen)
            form.startRequested = false
        }
    }

    /// Who attends the next meeting: the one picked here, else the one picked last unless that was the main chat; nil
    /// until one is chosen (#3862).
    private var attendee: MeetingAttendees.Candidate? { form.pickedAttendee ?? MeetingAttendees.preferred(model) }

    /// Who attends the meeting Record starts (`RecordingStart.make`): a session picked here that can no longer attend (its
    /// computer went offline meanwhile) gives way to the preferred one.
    private var chosenAttendee: MeetingAttendees.Candidate? {
        let all = MeetingAttendees.candidates(model)
        return form.pickedAttendee.flatMap { p in all.first { $0.key == p.key } } ?? MeetingAttendees.preferred(model)
    }

    /// 14:00–15:00.
    static func span(_ meeting: RecordingMeeting) -> String {
        guard let start = meeting.start, let end = meeting.end else { return "" }
        return (start..<max(end, start)).formatted(.interval.hour().minute())
    }

    private func refreshBluetooth() {
        form.hasBluetooth = RecordingInput.bluetoothConnected
    }
    #endif

    #if os(visionOS)
    // MARK: visionOS keeps its controls: the live sheet is the phone's.

    @ViewBuilder private var legacyControls: some View {
        if capture.state == .idle {
            Picker("Recording Language", selection: $locale) {
                Text("Chinese").tag("zh-CN")
                Text("English").tag("en-US")
                Text("Japanese").tag("ja-JP")
            }.font(Theme.Font.body)
            Button { Task { await capture.start(locale: locale, input: .phone) } } label: {
                Label("Start Recording", systemImage: Theme.Icon.recording).lineLimit(1)
            }.accessibilityIdentifier("recording-start")
        } else if capture.state == .starting {
            ProgressView(String(localized: "Waiting for microphone access")).font(Theme.Font.secondary)
            Button("Cancel") { capture.cancelStarting() }.lineLimit(1)
        } else {
            legacyRecordingNow
        }
        if let error = capture.error { Text(error).font(Theme.Font.secondary).foregroundStyle(Theme.danger) }
        if let notice = capture.notice { Text(notice).font(Theme.Font.secondary).foregroundStyle(.secondary) }
    }

    /// A recording in progress: what it is, its time and state, and pause / bookmark / stop.
    @ViewBuilder private var legacyRecordingNow: some View {
        let entry = capture.recordingID.flatMap(library.entry)
        if let entry {
            Text(entry.displayTitle).font(Theme.Font.heading).lineLimit(1)
                .accessibilityIdentifier("recording-now-title")
        }
        HStack(spacing: Theme.Space.s) {
            Text(RecordingFormat.clock(capture.elapsed)).font(Theme.Font.title.monospacedDigit())
            Spacer()
            Text(capture.state == .paused ? String(localized: "Paused") : String(localized: "Recording [Recordings Section 5254d3]"))
                .font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                .accessibilityIdentifier("recording-now-state")
        }
        HStack(spacing: Theme.Space.m) {
            Button { capture.togglePause() } label: {
                Label(capture.state == .paused ? String(localized: "Continue") : String(localized: "Pause"), systemImage: capture.state == .paused ? Theme.Icon.run : Theme.Icon.pause).lineLimit(1)
            }.themedButton()
            Button { capture.addMark() } label: { Label("Bookmark", systemImage: Theme.Icon.pin).lineLimit(1) }.themedButton()
                .accessibilityIdentifier("recording-bookmark")
            Button { _ = capture.stop() } label: { Label("Stop [Car Play Voice Screen c7b24e]", systemImage: Theme.Icon.interrupt).lineLimit(1) }
                .themedButton().tint(Theme.danger).accessibilityIdentifier("recording-stop")
        }
    }
    #endif
}

/// Recordings as a page of its own (the drawer's Recordings, the Control, Siri, the activity's tap): what Knowledge ›
/// Recordings shows, one step closer.
struct RecordingsPage: View {
    #if DEBUG
    /// A made-up recording's page, opened once per launch for screenshots and UI tests.
    private nonisolated enum DebugPage: Hashable {
        case recording(UUID)
        case day(UUID)
    }

    @State private var debugPage: DebugPage?
    private static var sampleOpened = false
    #endif
    #if os(iOS)
    /// A meeting's thread asked for from outside (the minutes push, #3862 phase 6), pushed over this page.
    @State private var opener = MeetingPageOpener.shared
    @State private var openedMeeting: UUID?
    /// The next recording's choices, shared by the options in the list and Record in the bottom bar.
    @State private var form = RecordingStartForm()
    #endif

    /// The list, with the bottom bar on a phone.
    @ViewBuilder private var page: some View {
        #if os(iOS)
        // On a phone the page's one main action sits under the thumb (#5522): Record, or the running recording's card,
        // in a bar at the bottom, as the live sheet's Pause and Stop (`RecordingLiveBar`); the options for the next
        // recording stay at the top of the list.
        List { RecordingsSection(form: form, startAtBottom: true) }
            .listStyle(.insetGrouped)
            .safeAreaInset(edge: .bottom, spacing: Theme.Space.flush) {
                RecordingCaptureControls(part: .bar, sharedForm: form)
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.vertical, Theme.Space.s)
                    .background { Theme.groupedPage.ignoresSafeArea(edges: .bottom) }
            }
        #else
        List { RecordingsSection() }
            .listStyle(.insetGrouped)
        #endif
    }

    var body: some View {
        page
            .themedPage()
            .navigationTitle(String(localized: "Recordings"))
            .navigationBarTitleDisplayMode(.inline)
            #if os(iOS)
            .recordingLiveSheet()
            // Pushed over this list, not over the recording's page: its Open Minutes / Open Recording is the way there.
            .navigationDestination(item: $openedMeeting) { MeetingPage(id: $0, mode: .after, opensRecording: true) }
            // On appear too: a cold launch from the push asks before this page exists.
            .onChange(of: opener.serial, initial: true) {
                if let id = opener.take() { openedMeeting = id }
            }
            #endif
            #if DEBUG
            .navigationDestination(item: $debugPage) { page in
                switch page {
                case .recording(let id): RecordingDetailView(id: id)
                case .day(let id): RecordingDayView(id: id)
                }
            }
            .onAppear {
                // Once per launch: back from it, the page appears again.
                guard !Self.sampleOpened else { return }
                let defaults = UserDefaults.standard
                // `-showMeetingSample minutes|pending`: the made-up finished meeting's page; `overview`: the longer one's,
                // for the summary card (#6563).
                if ["minutes", "pending"].contains(defaults.string(forKey: "showMeetingSample")) {
                    Self.sampleOpened = true
                    debugPage = .recording(RecordingSamples.pastMeeting)
                } else if defaults.string(forKey: "showMeetingSample") == "overview" {
                    Self.sampleOpened = true
                    debugPage = .recording(RecordingSamples.overviewMeeting)
                }
                #if os(iOS)
                // `-openDayRecap YES` with `-showDaySample …`: the made-up past day's page, once it is in place.
                if defaults.bool(forKey: "openDayRecap") {
                    Self.sampleOpened = true
                    Task {
                        await RecordingSamples.installDaySampleIfAsked()
                        debugPage = .day(RecordingSamples.pastDay)
                    }
                }
                #endif
            }
            #endif
    }
}

/// One recording's transcript, with re-transcribing in another language and delete (here and in the knowledge base). A
/// meeting also has its private mark, and a room meeting the way to its conversation (#3862).
struct RecordingDetailView: View {
    var id: UUID
    #if os(iOS)
    @Environment(AppModel.self) private var model
    @State private var conversation = MeetingConversation.shared
    /// This page's hold on the meeting's state (`MeetingConversation.watch`), while it shows a meeting.
    @State private var owner = UUID()
    @State private var watching = false
    /// The private mark being changed: shown at once, the hub's answer follows.
    @State private var pendingPrivate: Bool?
    /// The hub said the recording isn't a meeting (`409 not_meeting`): it takes no private mark.
    @State private var notMeeting = false
    /// Its transcript has words (`MeetingTalk.hasText`), worked out when it changes rather than on every pass (P4).
    @State private var hasText = false
    #endif
    @Environment(\.dismiss) private var dismiss
    @State private var library = RecordingLibrary.shared
    @State private var transcription = RecordingTranscription.shared
    @State private var sync = RecordingSync.shared
    @State private var keeper = RecordingRollingKeeper.shared
    @State private var deleting = false
    @State private var erasing = false
    @State private var failure: String?
    /// The transcript line a timestamp jumped to, lit for a moment.
    @State private var lit: Int?
    @State private var minutes = RecordingMinutesModel()
    #if os(iOS)
    /// Summary or Transcript (#5026), as picked on this page; nil until the user picks, which is the Summary once the
    /// minutes are ready. It lasts while the page is open: no setting.
    @State private var picked: RecordingMinutesCards.Pane?
    /// The Summary's cards, worked out when the minutes or the transcript change rather than on every pass (P4).
    @State private var summary: RecordingMinutesCards.Summary?
    /// A time tapped in the Summary: shown, lit, once the transcript has taken the Summary's place.
    @State private var pendingJump: Double?
    #endif
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        Group {
            if let entry = library.entry(id) { ScrollViewReader { proxy in content(entry, proxy: proxy) } } else {
                EmptyStateView(symbol: Theme.Icon.recording, title: String(localized: "Recording Deleted"), message: String(localized: "Deleted from this device. Removal from Knowledge may still be pending."))
            }
        }
        .themedPage()
        // Each new revision (more text, the end) may bring or change the minutes; while they are on their way, ask again.
        .task(id: "\(library.entry(id)?.revision ?? 0)|\(minutes.round)") { await followMinutes() }
        .navigationTitle("Recording")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if library.entries.contains(where: \.isRolling) {
                #if os(iOS)
                await RecordingHealthSignals.requestAccess() // HealthKit bookmarks are iOS only
                #endif
                await keeper.run()
            }
        }
        #if os(iOS)
        .onTabVisibility(appear: { watchMeeting() }, disappear: { unwatchMeeting() })
        .onChange(of: library.entry(id)?.revision, initial: true) {
            let next = library.entry(id).map(MeetingTalk.hasText) ?? false
            if hasText != next { hasText = next }
        }
        #endif
        .confirmationDialog("Delete This Recording?", isPresented: $deleting, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                if let message = RecordingDeletion.run(id) { failure = message } else { dismiss() }
            }
        } message: { Text("The audio on this device will be deleted now. Its text in Knowledge is removed after sync succeeds; if Knowledge is off or read-only, that deletion stays pending.") }
        .confirmationDialog("Erase the last 10 minutes?", isPresented: $erasing, titleVisibility: .visible) {
            Button("Erase", role: .destructive) {
                let now = Date.now
                library.erase(id, from: now.addingTimeInterval(-RecordingRolling.eraseMinutes * 60), to: now)
            }
        }
        .alert("Couldn't Finish", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button("OK [Recordings Section de32e2]") { failure = nil }.lineLimit(1)
        } message: { Text(failure ?? "") }
    }

    private func content(_ entry: RecordingEntry, proxy: ScrollViewProxy) -> some View {
        List {
            Section {
                Text(entry.displayTitle).font(Theme.Font.heading)
                if let meeting = entry.meeting { RecordingMeetingFacts(meeting: meeting) }
                HStack(spacing: Theme.Space.xs) {
                    if entry.isRolling {
                        Text("\(entry.sourceTitle) · keeps \(Int(RecordingRolling.keepHours))h · \(retainedMinutes(entry)) min now ·")
                    } else {
                        Text("\(entry.sourceTitle) · \(RecordingFormat.clock(entry.duration)) ·")
                    }
                    RecordingStatusText(entry: entry)
                }
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                #if os(iOS)
                // How it was transcribed (#3862 phase 5): on the hub (being finished until final, its gaps), or kept on
                // this iPhone although it asked for the hub.
                if entry.attendance?.transcription != nil {
                    RecordingTranscriptionRow(id: id, view: minutes.transcription, asked: entry.attendance?.transcription)
                }
                #endif
                if !entry.marks.isEmpty {
                    Label(String(localized: "Bookmarks \(ListFormatter.localizedString(byJoining: entry.marks.map(RecordingFormat.clock)))"), systemImage: Theme.Icon.pin)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                if transcription.current == id { ProgressView().tint(Theme.accent) }
            }
            #if os(iOS)
            meetingSection(entry)
            #endif
            if entry.isRolling {
                rollingSections(entry)
            } else {
                let synced = sync.status(entry) == .synced
                #if os(iOS)
                // Once the minutes are ready the switch takes their sections' place (#5026): the Summary's cards, or the
                // transcript as before. Until then the page is as it was, with no switch.
                let pane = RecordingMinutesCards.pane(picked: picked, ready: minutes.ready != nil)
                if let ready = minutes.ready, let pane {
                    minutesSwitch(entry, ready, pane)
                    if pane == .summary {
                        if let summary {
                            RecordingMinutesSummary(entry: entry, model: minutes, synced: synced, summary: summary) { showInTranscript($0) }
                        }
                    } else {
                        transcriptSection(entry)
                    }
                } else {
                    RecordingMinutesSections(entry: entry, model: minutes, synced: synced) { jump(to: $0, in: entry, proxy: proxy) }
                    transcriptSection(entry)
                }
                #else
                RecordingMinutesSections(entry: entry, model: minutes, synced: synced) { jump(to: $0, in: entry, proxy: proxy) }
                transcriptSection(entry)
                #endif
            }
            if !entry.active {
                Section {
                    if !entry.isRolling {
                        Picker("Transcription Language", selection: Binding(get: { entry.locale }, set: { transcription.setLocale(id, $0) })) {
                            Text("Chinese").tag("zh-CN"); Text("English").tag("en-US"); Text("Japanese").tag("ja-JP")
                        }.disabled(transcription.current == id)
                        if transcription.current == id {
                            Button("Stop Transcribing") { transcription.cancel(id) }.lineLimit(1)
                        } else if !entry.transcribedAll {
                            Button("Resume Transcribing") { transcription.enqueue(id) }.lineLimit(1)
                        }
                    } else {
                        Button(role: .destructive) { erasing = true } label: {
                            Label("Erase last 10 minutes", systemImage: Theme.Icon.delete).lineLimit(1)
                        }
                    }
                    Button("Delete Recording", role: .destructive) { deleting = true }.lineLimit(1)
                        .accessibilityIdentifier("recording-delete")
                }
            }
            // The retention text is read once, at the end: not between the header and the minutes.
            Section {
                EmptyView()
            } footer: {
                #if os(iOS)
                // A meeting whose audio went through the hub (#3862 phase 5) says so, before its `detail.meta` too.
                if entry.attendance?.transcription != nil {
                    RecordingRetentionFooter(id: id, attendance: entry.attendance, meta: minutes.transcription,
                                             days: sync.store.days, device: retentionFooter(entry))
                } else {
                    Text(retentionFooter(entry))
                }
                #else
                Text(retentionFooter(entry))
                #endif
            }
        }
        // The minutes unfold when they arrive (Reduce Motion: at once).
        .animation(reduceMotion ? nil : Theme.Motion.reveal, value: minutes.state)
        #if os(iOS)
        .onChange(of: minutes.state, initial: true) { refreshSummary() }
        .onChange(of: entry.revision) { refreshSummary() }
        // A time tapped in the Summary: the transcript's rows reach the list in this update, so the scroll to its line
        // waits for the main actor's next turn.
        .onChange(of: picked) { _, now in
            guard now == .transcript, let at = pendingJump else { return }
            pendingJump = nil
            Task { jump(to: at, in: entry, proxy: proxy) }
        }
        #endif
    }

    /// What the page says, at its end, about what left the device and how long it is kept, when the audio stayed here. A
    /// meeting that asked for a transcription mode (#3862 phase 5) shows it through `RecordingRetentionFooter`, which says
    /// the audio went through the hub instead when it may have.
    private func retentionFooter(_ entry: RecordingEntry) -> String {
        if entry.isRolling {
            return String(localized: "Rolling keeps the last \(Int(RecordingRolling.keepHours)) hours. Pull a range to transcribe it; text syncs into Knowledge. Audio stays on this device.")
        }
        return String(localized: "Only the text and timestamps go into Knowledge; the original audio isn't uploaded. After the retention period (\(sync.store.days) days) it's cleared from this device and Knowledge.")
    }

    /// The transcript, a line a row with its time; a line a time in the minutes jumped to is lit.
    private func transcriptSection(_ entry: RecordingEntry) -> some View {
        Section("Transcript") {
            if entry.sentences.isEmpty {
                Text("No transcript yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
            let ids = Self.stableIDs(entry.sentences)
            ForEach(Array(entry.sentences.enumerated()), id: \.offset) { index, sentence in
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(RecordingFormat.clock(sentence.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
                    Text(sentence.text).readingContent().font(Theme.Font.body).textSelection(.enabled)
                }
                .id(ids[index])
                .listRowBackground(lit == index ? Theme.accentTint : nil)
                .accessibilityIdentifier(lit == index ? "recording-sentence-lit" : "recording-sentence")
            }
        }
    }

    private func followMinutes() async {
        while !Task.isCancelled, let entry = library.entry(id) {
            await minutes.load(entry, synced: sync.status(entry) == .synced)
            guard minutes.waiting else { return }
            try? await Task.sleep(for: .seconds(15))
        }
    }

    #if os(iOS)
    // MARK: Summary | Transcript (#5026)

    /// The switch, right under the page's header, with the minutes' Copy and Share under it: they send the minutes
    /// whichever view shows, so they stay in one place. Both rows sit on the page's canvas, as the cards below do.
    private func minutesSwitch(_ entry: RecordingEntry, _ ready: RecordingMinutes, _ pane: RecordingMinutesCards.Pane) -> some View {
        Section {
            Picker("Minutes", selection: Binding(get: { pane }, set: { picked = $0 })) {
                Text("Summary").tag(RecordingMinutesCards.Pane.summary)
                    .accessibilityIdentifier("recording-view-summary")
                Text("Transcript").tag(RecordingMinutesCards.Pane.transcript)
                    .accessibilityIdentifier("recording-view-transcript")
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .accessibilityIdentifier("recording-view-switch")
            .minutesCanvasRow()
            RecordingMinutesActions(entry: entry, minutes: ready, summaryCard: summary?.overview != nil)
                .minutesCanvasRow()
        }
    }

    /// A time in the Summary: the Transcript takes its place, then shows that line, lit (`jump`, from `.onChange(of: picked)`).
    private func showInTranscript(_ seconds: Double) {
        pendingJump = seconds
        picked = .transcript
    }

    /// The Summary's cards from the minutes and the transcript as they are now; read in an action, not in `body`.
    private func refreshSummary() {
        guard let ready = minutes.ready, let entry = library.entry(id) else {
            if summary != nil { summary = nil }
            return
        }
        // The summary card (#6563) leaves out what the page's header already says of the meeting.
        let next = RecordingMinutesCards.summary(ready, sentences: entry.sentences, shown: .init(entry.meeting))
        guard next != summary else { return }
        // The first cards come a moment after the minutes (this runs after the update that brought them): they unfold
        // as the minutes do (Reduce Motion: at once); later changes of the transcript just redraw them.
        withAnimation(reduceMotion || summary != nil ? nil : Theme.Motion.reveal) { summary = next }
    }

    // MARK: The meeting conversation (#3862)

    /// Private Meeting for any meeting (whatever recorded it: a meeting with no mark counts as private until it is turned
    /// off), and Talk about this meeting (#3862 phase 6) for any recording that ended with text or minutes, or one the hub
    /// holds a conversation for (`MeetingTalk.offered`; past retention its transcript here doesn't count): it pushes the
    /// meeting's one thread. Only with a hub that lists the meeting conversation.
    @ViewBuilder private func meetingSection(_ entry: RecordingEntry) -> some View {
        let meeting = conversation.meeting(id)
        let offered = MeetingAttendees.conversation(model)
        let showsPrivate = entry.isMeeting && !notMeeting
        let showsThread = MeetingTalk.offered(entry, conversation: offered, minutes: minutes.ready != nil, meeting: meeting,
                                              text: hasText, expired: sync.isExpired(entry))
        if offered, showsPrivate || showsThread {
            Section {
                if showsPrivate {
                    Toggle(isOn: Binding(get: { pendingPrivate ?? MeetingPage.isPrivate(entry, meeting) }, set: { setPrivate($0) })) {
                        Label {
                            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                                Text("Private Meeting").font(Theme.Font.body)
                                Text("Its minutes are never used when Persona answers in later meetings.")
                                    .font(Theme.Font.caption).foregroundStyle(.secondary)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        } icon: {
                            ThemedIcon(Theme.Icon.privateMeeting)
                        }
                    }
                    .disabled(pendingPrivate != nil)
                    .accessibilityIdentifier("recording-private")
                }
                if showsThread {
                    NavigationLink { MeetingPage(id: id, mode: .after) } label: {
                        // Drawn as the Private Meeting row beside it: in the style's icon language.
                        Label { Text("Talk about this meeting").lineLimit(1) } icon: {
                            ThemedIcon(Theme.Icon.conversation)
                        }
                    }
                    .accessibilityIdentifier("recording-talk")
                }
            }
        }
    }

    /// Reads the meeting's private mark and room while the page shows a meeting (`GET …/live`, then `meeting_live`).
    private func watchMeeting() {
        guard !watching, library.entry(id)?.isMeeting == true, MeetingAttendees.conversation(model) else { return }
        watching = true
        conversation.watch(id, by: owner)
    }

    private func unwatchMeeting() {
        guard watching else { return }
        watching = false
        conversation.unwatch(id, by: owner)
    }

    /// `PUT …/private`; a recording the hub doesn't count as a meeting loses the row instead of showing an error.
    private func setPrivate(_ value: Bool) {
        pendingPrivate = value
        Task {
            do {
                try await conversation.setPrivate(id, value)
            } catch let error as APIError where error.status == 409 && error.code == "not_meeting" {
                notMeeting = true
            } catch {
                failure = MeetingConversation.privateMessage(error)
            }
            pendingPrivate = nil
        }
    }
    #endif

    /// A transcript line's identity: where it starts, in milliseconds, so a row keeps its identity when text is added before
    /// or after it (a line at the same start gets a number after it).
    static func stableIDs(_ sentences: [RecordingSentence]) -> [String] {
        var seen: [Int: Int] = [:]
        return sentences.map { sentence in
            let millis = Int((sentence.start * 1000).rounded())
            let count = seen[millis, default: 0]
            seen[millis] = count + 1
            return count == 0 ? "sentence-\(millis)" : "sentence-\(millis)-\(count)"
        }
    }

    /// To the transcript line said at `seconds` (a timestamp in the minutes), lit for two seconds.
    private func jump(to seconds: Double, in entry: RecordingEntry, proxy: ScrollViewProxy) {
        guard let index = RecordingMinutes.sentenceIndex(at: seconds, in: entry.sentences) else { return }
        let ids = Self.stableIDs(entry.sentences)
        withAnimation(reduceMotion ? nil : Theme.Motion.layout) { proxy.scrollTo(ids[index], anchor: .center) }
        lit = index
        Task {
            try? await Task.sleep(for: .seconds(2))
            if lit == index { withAnimation(Theme.Motion.standard) { lit = nil } }
        }
    }

    @ViewBuilder private func rollingSections(_ entry: RecordingEntry) -> some View {
        Section {
            HStack(spacing: Theme.Space.s) {
                ForEach(RecordingRolling.pullMinutes, id: \.self) { minutes in
                    Button("\(minutes) min") { transcription.pull(id, minutes: minutes) }
                        .themedButton().lineLimit(1)
                }
            }
        } header: {
            Text("Pull")
        } footer: {
            Text("Rolling recordings aren't transcribed until you pull a range.")
        }
        if let range = transcription.pulls[id] {
            let picked = entry.segments(from: range.lowerBound, to: range.upperBound)
            let lines = entry.pulledSentences(from: range.lowerBound, to: range.upperBound)
            let state = entry.pullState(from: range.lowerBound, to: range.upperBound,
                                        transcribing: transcription.current == id, error: transcription.errors[id])
            Section {
                if case .failed = state, let message = state.message {
                    Text(message).font(Theme.Font.secondary).foregroundStyle(Theme.danger)
                    Button { transcription.pull(id, range: range) } label: {
                        Label("Retry", systemImage: Theme.Icon.retry).lineLimit(1)
                    }.accessibilityIdentifier("recording-pull-retry")
                } else if lines.isEmpty, let message = state.message {
                    Text(message).font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
                ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        Text(line.0.formatted(date: .omitted, time: .standard)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
                        Text(line.1).font(Theme.Font.body).textSelection(.enabled)
                    }
                }
                if !picked.isEmpty {
                    Button { saveAsClip(picked.map(\.index)) } label: {
                        Label("Save as recording", systemImage: Theme.Icon.save).lineLimit(1)
                    }
                }
            } header: {
                Text("Pulled range")
            }
        }
        let runs = keptRuns(entry)
        if !runs.isEmpty {
            Section {
                ForEach(runs) { run in
                    Button { transcription.pull(id, range: run.from...run.to) } label: {
                        HStack(spacing: Theme.Space.m) {
                            Text("\(run.from.formatted(date: .omitted, time: .shortened))–\(run.to.formatted(date: .omitted, time: .shortened))")
                                .font(Theme.Font.body.monospacedDigit()).lineLimit(1)
                            Spacer(minLength: Theme.Space.s)
                            Text(run.keep.title).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    .buttonStyle(.plain)
                }
            } header: {
                Text("Kept segments")
            } footer: {
                Text("Bookmarks and health spikes keep a few minutes around them for 7 days.")
            }
        }
    }

    private func retainedMinutes(_ entry: RecordingEntry) -> Int {
        Int((entry.segments.map(\.duration).reduce(0, +) / 60).rounded())
    }

    private func keptRuns(_ entry: RecordingEntry) -> [RecordingKeptRun] {
        var runs: [RecordingKeptRun] = []
        for segment in entry.segments.sorted(by: { $0.index < $1.index }) {
            guard let keep = segment.kept, let from = segment.startedAt, let to = segment.endedAt else { continue }
            if let last = runs.last, last.keep.reason == keep.reason, last.endIndex + 1 == segment.index {
                runs[runs.count - 1].endIndex = segment.index
                runs[runs.count - 1].to = to
            } else {
                runs.append(RecordingKeptRun(id: segment.index, endIndex: segment.index, from: from, to: to, keep: keep))
            }
        }
        return runs
    }

    private func saveAsClip(_ indexes: [Int]) {
        do {
            _ = try library.saveClip(from: id, indexes: indexes)
        } catch {
            failure = String(localized: "Couldn't save a clip from those segments.")
        }
    }
}

/// A meeting recording's meeting: when it was booked, where, and who was invited (from the calendar event it is named after).
struct RecordingMeetingFacts: View {
    var meeting: RecordingMeeting

    var body: some View {
        // When and where on one line.
        let when: String? = {
            guard let start = meeting.start, let end = meeting.end else { return nil }
            return RecordingFormat.span(start, end)
        }()
        let line = [when, meeting.location].compactMap { $0 }.joined(separator: " · ")
        if !line.isEmpty {
            Label(line, systemImage: when == nil ? Theme.Icon.place : Theme.Icon.calendarEvent)
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
        }
        if !meeting.attendees.isEmpty {
            Label(ListFormatter.localizedString(byJoining: meeting.attendees), systemImage: Theme.Icon.people)
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
        }
    }
}

private struct RecordingKeptRun: Identifiable {
    var id: Int
    var endIndex: Int
    var from: Date
    var to: Date
    var keep: RecordingKeep
}
#endif
