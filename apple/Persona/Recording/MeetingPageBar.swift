#if os(iOS)
import BeautifulUI
import PhotosUI
import SwiftUI

/// The meeting page's bottom bar (#3862, docs/assistant/meeting-minutes.md「The meeting card」), pinned under the thumb:
/// the Just Me switch, the composer, and Pause, Bookmark and Stop while the meeting records; the composer alone after it.
/// The composer is #4486's typed question (`MeetingQuestionField`: the shell composer's field and round send button) with
/// a [+] menu before it, not `ShellComposerBar`: that one's dictation would take the microphone the recording holds. Its
/// words go to the room (`…/ask`), to the main chat with Just Me (`…/just-me`), or after the meeting to the follow-up
/// (`…/followup`, then the conversation's messages). While the field has the keyboard, Pause, Bookmark and Stop step
/// aside so the thread stays in view.
///
/// [+] also takes photos (phase 3) while a meeting that started with them records and Just Me is off: they are the
/// meeting's material, never private. Take Photo opens the camera, Choose Photos the library; both are presented from
/// the bar (a picker inside a menu never presents), and each picture goes to `MeetingPhotos` with the meeting's time
/// when it was taken or picked; a picked one's row shows at once, before Photos hands its bytes over. Adding one plays
/// no haptic: its row is the feedback, and the microphone is open.
///
/// **Talk** (#3862 phase 4) takes send's place while the field is empty and Persona can be asked (any role that attends):
/// held while the owner speaks, to the meeting call when it is live, else asked without it. Not with Just Me on (Talk is
/// the room's: turning it on ends a hold), and not after the meeting. While it is held the caption over the controls says
/// Persona hears; where the call sends the span to the voice service, the line under it says so (the contract's
/// Wording) until a span went while it showed, whatever word the caption has meanwhile. Until the owner has seen it
/// there, a span stays on the phone (`MeetingCallRule.toCall`).
/// A typed question goes as the call's `text` frame while the call is live, else through `…/ask`.
///
/// Stop asks first, as the live sheet's bar does: a stopped recording cannot be continued.
struct MeetingPageBar: View {
    var entry: RecordingEntry
    var mode: MeetingPage.Mode
    var paused = false
    @Binding var justMe: Bool
    /// The field has the keyboard: the page steps aside as the bar does (`MeetingPage`).
    var focus: FocusState<Bool>.Binding
    /// After the meeting, a voice call bound to the follow-up (#3862 phase 6): set where one can start now; nil, no button.
    var voice: (() -> Void)?

    init(entry: RecordingEntry, mode: MeetingPage.Mode, paused: Bool = false, justMe: Binding<Bool>, focus: FocusState<Bool>.Binding,
         voice: (() -> Void)? = nil) {
        self.entry = entry
        self.mode = mode
        self.paused = paused
        _justMe = justMe
        self.focus = focus
        self.voice = voice
    }

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.buiTheme) private var theme
    @State private var capture = RecordingCapture.shared
    @State private var ask = MeetingAsk.shared
    @State private var conversation = MeetingConversation.shared
    @State private var sync = RecordingSync.shared
    /// The meeting call and Talk (#3862 phase 4).
    @State private var call = MeetingCall.shared
    /// Hub transcription (#3862 phase 5): the Talk line says hub mode's words in a meeting the hub transcribes.
    @State private var transcription = MeetingTranscription.shared
    /// Read for one thing only: how many of the last pick couldn't be read.
    @State private var photos = MeetingPhotos.shared
    @State private var draft = ""
    @State private var confirmingStop = false
    /// The camera and the library, presented from the bar for the [+] menu's items.
    @State private var takingPhoto = false
    @State private var choosingPhotos = false
    @State private var picks: [PhotosPickerItem] = []

    private enum Target { case room, justMe, followup }

    private var target: Target { mode == .after ? .followup : justMe ? .justMe : .room }
    private var role: MeetingAttendance { entry.attendance?.role ?? .off }
    /// The last upload's `200` had no `room`: an older manager took the meeting with Persona off (`RecordingSync`).
    private var roomMissing: Bool { sync.roomMissing.contains(entry.id) }
    /// Persona is off and Just Me is off: there is nobody to ask.
    private var silenced: Bool { target == .room && !role.attends }
    /// Persona's role attends but it can't be asked now (`MeetingAsk.canAsk`): the hub took the meeting without its room.
    /// Just Me still goes (it needs only a meeting that records).
    private var unreachable: Bool { target == .room && role.attends && !MeetingAsk.canAsk(entry, roomMissing: roomMissing) }
    /// Why the follow-up can't be written now (after the meeting).
    private var hold: MeetingConversation.FollowupHold? { target == .followup ? conversation.followupHold(entry) : nil }
    /// More words than the route takes (`MeetingConversation.maxText`): Send stays off and the caption says so, rather
    /// than the end of the owner's words being dropped.
    private var overLong: Bool {
        switch target {
        case .room: false
        case .justMe: MeetingConversation.tooLong(draft)
        case .followup: conversation.followupLimited(entry.id) && MeetingConversation.tooLong(draft)
        }
    }
    /// The keyboard is up: the controls under the composer step aside (#4486's rule).
    private var writing: Bool { focus.wrappedValue }

    private var canSend: Bool {
        guard MeetingAsk.hasWords(draft), !overLong else { return false }
        switch target {
        // One question at a time, deliberately stricter than the hub's two: an answer sits next to its question. The
        // meeting call takes a question whenever it is live (the fast layer hands it to the room).
        case .room: return MeetingAsk.canAsk(entry, roomMissing: roomMissing) && (call.isLive(entry.id) || !ask.busy)
        // Just Me needs only a meeting that records; it never waits on the room's question.
        case .justMe: return entry.active
        case .followup: return hold == nil
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if mode == .live { justMeRow }
            MeetingQuestionField(text: $draft, focus: focus, placeholder: placeholder, sendLabel: String(localized: "Send"),
                                 canSend: canSend, disabled: fieldDisabled, showsInstead: showsTalk,
                                 send: send) {
                plus
                voiceButton
            } instead: {
                talkButton
            }
            if let caption {
                Text(caption).font(Theme.Font.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("meeting-ask-caption")
            }
            if showsPrivacy { privacyLine }
            if let photoProblem {
                Text(photoProblem).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityIdentifier("meeting-photo-problem")
            }
            if mode == .live, !writing { controls }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .frame(maxWidth: .infinity)
        .background { Theme.groupedPage.ignoresSafeArea(edges: .bottom) }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: writing)
        .onChange(of: fieldDisabled) { _, disabled in
            if disabled { focus.wrappedValue = false }
        }
        .onChange(of: photoProblem) { _, now in
            if let now { AccessibilityNotification.Announcement(now).post() }
        }
        // Talk is the room's: Just Me ends a hold (its button goes too, which ends it as well).
        .onChange(of: justMe) { _, on in
            if on { call.endHold(entry.id) }
        }
        .confirmationDialog("Stop Recording?", isPresented: $confirmingStop, titleVisibility: .visible) {
            Button("Stop Recording", role: .destructive) { MeetingRecorder.shared.stop(recordingID: entry.id.uuidString) }
        } message: {
            Text("The audio is saved on this device and its text goes to Knowledge. A stopped recording can't be continued.")
        }
        .photosPicker(isPresented: $choosingPhotos, selection: $picks, maxSelectionCount: Theme.Recording.photoPickLimit,
                      matching: .images)
        .onChange(of: picks) { _, items in addPicked(items) }
        .fullScreenCover(isPresented: $takingPhoto) {
            CameraPicker { image in addTaken(image) }.ignoresSafeArea()
        }
    }

    // MARK: Just Me

    private var justMeRow: some View {
        Toggle(isOn: $justMe) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text("Just Me").font(Theme.Font.bodyStrong)
                Text("Only you see it, in the main chat").font(Theme.Font.caption).foregroundStyle(.secondary)
            }
        }
        .disabled(!entry.active)
        .accessibilityIdentifier("meeting-just-me")
    }

    // MARK: The composer

    private var placeholder: String {
        switch target {
        case .room:
            silenced ? String(localized: "Persona is off")
                : unreachable ? String(localized: "Persona can't take part right now") : String(localized: "Ask Persona…")
        case .justMe: String(localized: "Ask privately…")
        case .followup: String(localized: "Ask about this meeting…")
        }
    }

    private var fieldDisabled: Bool { silenced || unreachable || hold != nil }

    /// Why the field can't take words now, and the way out when there is one here (an older manager has none: the role
    /// is on, and nothing on the phone changes it).
    private var blocked: String? {
        if silenced { return String(localized: "Turn Persona on in the menu to ask, or use Just Me.") }
        if unreachable { return String(localized: "Persona can't take part in this meeting right now.") }
        switch hold {
        case .finishing?: return String(localized: "Persona is finishing the meeting. You can ask about it in a moment.")
        // Not "in a moment": the hub has no end of the meeting until a later upload lands, or 12 hours after the last.
        case .endRefused?:
            return sync.problem ?? String(localized: "Recording sync couldn't finish. Try again after checking the hub's diagnostics.")
        case .noFollowup?: return String(localized: "This hub can't take questions about this meeting right now.")
        case nil: return nil
        }
    }

    /// The caption under the field: why it takes no words, that the words
    /// are too many to send, or Talk's (#3862 phase 4): Persona hears while it is held, or a word after a span. Where a
    /// span goes to the voice service, the Wording is a line of its own under it (`privacyLine`).
    private var caption: String? {
        if let blocked { return blocked }
        if overLong { return String(localized: "Too long to send. Shorten it to \(MeetingConversation.maxText.formatted()) characters.") }
        return talkCaption
    }

    // MARK: Talk

    /// Talk stands in send's place: live, asking the room, Persona can be asked now, nothing typed.
    private var showsTalk: Bool { mode == .live && target == .room && call.canTalk(entry) && !MeetingAsk.hasWords(draft) }

    /// A span from this page is open.
    private var talking: Bool { call.talk?.recording == entry.id }

    /// The span goes to the voice service (the meeting call is live) rather than staying on the phone.
    private var talkSendsAudio: Bool { call.isLive(entry.id) }

    /// The Wording shows under Talk: a span would go to the voice service and the owner hasn't seen one go with it yet.
    /// Beside a note after a span (hold longer, heard nothing), never replaced by it: the next span goes to the call only
    /// if this was on screen as it opened. Not while a span is held (the caption says Persona hears).
    private var showsPrivacy: Bool { showsTalk && talkSendsAudio && !call.privacySeen && !talking }

    /// The contract's Wording as its own line, which tells `MeetingCall` when it is actually on screen: a span opened
    /// under it may go to the call and marks it seen (`MeetingCall.showingPrivacy`, `MeetingTalkPrivacy.shown`).
    private var privacyLine: some View {
        let recording = entry.id
        return Text(talkPrivacy).font(Theme.Font.caption).foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("meeting-talk-privacy")
            .onAppear { call.showingPrivacy(true, recording: recording) }
            .onDisappear { call.showingPrivacy(false, recording: recording) }
    }

    private var talkButton: some View {
        MeetingTalkButton(held: talking, hint: talkSendsAudio ? talkPrivacy : String(localized: "Hold while you speak. Persona answers when you let go."),
                          press: { startTalk() }, release: { call.endTalk() }, window: { openTalkWindow() },
                          gone: { call.endHold(entry.id) })
    }

    /// What the caption says about Talk now, if anything.
    private var talkCaption: String? {
        guard mode == .live, target == .room else { return nil }
        if let talk = call.talk, talk.recording == entry.id {
            // A window (the Lock Screen's tap, or VoiceOver) ends on a tap; a hold when the finger lifts.
            return talk.window ? String(localized: "Listening to you… Tap Talk when you’re done.")
                : String(localized: "Listening to you… Let go when you’re done.")
        }
        switch call.note {
        case .nothingHeard?: return String(localized: "Persona didn’t hear anything. Hold Talk while you speak.")
        case .holdLonger?: return String(localized: "Hold Talk while you speak, then let go.")
        case nil: return nil
        }
    }

    /// The contract's Wording where Talk sends its spans to the voice service: privacy mode's (#3862 phase 4), or in a
    /// meeting the hub transcribes hub mode's (phase 5: all of its audio goes through the hub, Talk included).
    private var talkPrivacy: String {
        MeetingTranscriptionWords.pagePrivacy(hub: transcription.hubTranscribes(entry.id), service: transcription.service)
    }

    private func startTalk() {
        focus.wrappedValue = false
        // Whether the Wording was on screen as the finger went down is `MeetingCall`'s to know (`privacyLine`).
        guard call.beginTalk(entry.id, from: .hold) else { return }
        Haptics.tap()
        AccessibilityNotification.Announcement(String(localized: "Listening to you…")).post()
    }

    /// VoiceOver's double tap (it can't hold): a talk window. Its hint says where the words go, but not every way in speaks
    /// it (hints off, Voice Control, Switch Control, a keyboard): it goes to the call under the Wording on screen, as a
    /// hold does.
    private func openTalkWindow() {
        guard call.beginTalk(entry.id, from: .pageWindow) else { return }
        AccessibilityNotification.Announcement(String(localized: "Listening to you…")).post()
    }

    /// [+]: what else the composer can send: photos for the meeting's material (a meeting that takes them), and the
    /// question about what was just said, asked of the room; with nothing to offer (Just Me, after the meeting, Persona
    /// off or not reachable, no photos) the button is not there.
    @ViewBuilder private var plus: some View {
        if canAskHeard || canAddPhotos {
            Menu {
                if canAddPhotos { photoButtons }
                if canAskHeard { heardButton }
            } label: {
                plusFace
            }
            .menuStyle(.button)
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Add [Meeting Composer]"))
            .accessibilityIdentifier("meeting-plus")
            .shellScaled()
        }
    }

    /// Voice Chat after the meeting (#3862 phase 6): the composer's voice button, before the field where the live page has
    /// [+] (the bar has no [+] after the meeting). The call is bound to the follow-up; the page shows its bar in place of
    /// this one while it runs.
    @ViewBuilder private var voiceButton: some View {
        if mode == .after, let voice {
            ComposerVoiceButton(round: false, id: "meeting-voice") {
                focus.wrappedValue = false
                voice()
            }
        }
    }

    private var canAskHeard: Bool { mode == .live && target == .room && MeetingAsk.canAsk(entry, roomMissing: roomMissing) }

    /// Some photos of the last pick couldn't be read (still in iCloud): said until the next pick, while photos can be
    /// added.
    private var photoProblem: String? {
        guard canAddPhotos, photos.unreadPicks(entry.id) > 0 else { return nil }
        return String(localized: "Couldn't read some photos (they may still be downloading from iCloud)")
    }

    /// Photos go to a room meeting that started with them while it records, whatever Persona's role (with Persona off the
    /// hub stores them without showing the room); never with Just Me, which is private, and not after the meeting.
    private var canAddPhotos: Bool {
        mode == .live && target == .room && entry.active && entry.roomMeeting && entry.attendance?.photos == true
    }

    /// Take Photo (where there is a camera) and Choose Photos; each opens from the bar, after the menu has closed.
    @ViewBuilder private var photoButtons: some View {
        if CameraPicker.isAvailable {
            Button { takingPhoto = true } label: { Label("Take Photo", systemImage: Theme.Icon.camera) }
                .accessibilityIdentifier("meeting-photo-camera")
        }
        Button { choosingPhotos = true } label: { Label("Choose Photos", systemImage: Theme.Icon.photos) }
            .accessibilityIdentifier("meeting-photo-library")
    }

    private var heardButton: some View {
        Button { askHeard() } label: { Label("What Was Just Said", systemImage: Theme.Icon.question) }
            .disabled(ask.busy)
            .accessibilityIdentifier("meeting-heard")
    }

    /// The shell composer's [+] (`ShellComposerBar.plusFace`): the add symbol in the composer's ink, no circle, so send
    /// stays the row's one solid button.
    private var plusFace: some View {
        ThemedIcon(Theme.Icon.add)
            .font(Theme.Font.title)
            .fontWeight(.regular)
            .foregroundStyle(ComposerInk.icon(theme))
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            .contentShape(.circle)
    }

    // MARK: Pause, Bookmark, Stop

    private var controls: some View {
        HStack(spacing: Theme.Space.m) {
            barButton(pauseTitle, paused ? Theme.Icon.run : Theme.Icon.pause) { togglePause() }
                .accessibilityIdentifier("recording-pause")
            barButton(String(localized: "Bookmark"), Theme.Icon.pin) { addBookmark() }
                .accessibilityIdentifier("recording-bookmark")
            barButton(String(localized: "Stop [Car Play Voice Screen c7b24e]"), Theme.Icon.interrupt) { confirmingStop = true }
                .accessibilityIdentifier("recording-stop")
        }
    }

    private var pauseTitle: String { paused ? String(localized: "Continue") : String(localized: "Pause") }

    /// A standard button, its icon over its label, at least a finger tall.
    private func barButton(_ title: String, _ symbol: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(spacing: Theme.Space.xxs) {
                ThemedIcon(symbol).font(Theme.Font.heading).scaledFrame(height: Theme.Size.icon)
                Text(title).font(Theme.Font.captionStrong).lineLimit(1).minimumScaleFactor(0.8)
            }
            .frame(maxWidth: .infinity)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
        }
        .themedButton()
    }

    // MARK: Actions

    /// Sends the draft where the bar points; the keyboard goes down after, so the new question is in view.
    private func send() {
        guard canSend else { return }
        let text = draft
        let sent: Bool
        switch target {
        // While the meeting call is live the question is its `text` frame (#3862 phase 4); else an ask.
        case .room: sent = call.say(text, recording: entry.id) || ask.ask(entry, text: text) != nil
        case .justMe: sent = conversation.sendJustMe(entry, text: text) != nil
        case .followup: sent = conversation.sendFollowup(entry.id, text: text) != nil
        }
        guard sent else { return }
        draft = ""
        focus.wrappedValue = false
        Haptics.tap()
    }

    /// The question about what was just said (`…/ask` with the time of the tap).
    private func askHeard() {
        guard ask.ask(entry) != nil else { return }
        Haptics.tap()
    }

    /// Pause: the haptic after the microphone is paused (a pulse on touch-down would overlap live capture). Continue:
    /// before it opens again. The state is announced.
    private func togglePause() {
        if paused {
            Haptics.tap()
            capture.togglePause()
            AccessibilityNotification.Announcement(String(localized: "Recording continued")).post()
        } else {
            capture.togglePause()
            Haptics.tap()
            AccessibilityNotification.Announcement(String(localized: "Recording paused")).post()
        }
    }

    private func addBookmark() {
        MeetingRecorder.shared.addMark(recordingID: entry.id.uuidString)
        AccessibilityNotification.Announcement(String(localized: "Bookmark added")).post()
    }

    /// The library's picks: each goes as the meeting's material at the time they were picked, its row in the thread at
    /// once ("Preparing…"). Their bytes come from Photos when each one's turn to be prepared comes (a HEIC as it is: the
    /// pipeline reads it off the main actor); one Photos can't hand over (still in iCloud) is said under the field.
    private func addPicked(_ items: [PhotosPickerItem]) {
        guard !items.isEmpty else { return }
        picks = []
        let loads = items.map { item in { @Sendable () async -> Data? in try? await item.loadTransferable(type: Data.self) } }
        MeetingPhotos.shared.pick(loads, to: entry, at: capture.timeline)
    }

    /// The camera's picture: encoded as JPEG off the main actor (a full-size frame takes a while), then prepared and sent
    /// as any photo is. Its time is when it was taken, not when the encoding is done.
    private func addTaken(_ image: UIImage) {
        let at = capture.timeline
        let recording = entry.id
        let quality = Self.cameraQuality
        Task {
            let data = await Task.detached(priority: .userInitiated) { image.jpegData(compressionQuality: quality) }.value
            guard let data, let current = RecordingLibrary.shared.entry(recording) else { return }
            MeetingPhotos.shared.add(.camera(data), to: current, at: at)
        }
    }

    /// The camera's frame as it is handed over (the pipeline downscales it to 1600 px and encodes it again): close to
    /// what the camera saw, so the text read in it is the text on the board.
    private static let cameraQuality: CGFloat = 0.9
}
#endif
