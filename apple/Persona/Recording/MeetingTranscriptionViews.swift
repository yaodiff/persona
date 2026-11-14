#if os(iOS)
import SwiftUI

/// What the phone says about hub transcription (#3862 phase 5; docs/assistant/meeting-minutes.md「Privacy per mode」,
/// docs/design/DESIGN_SYSTEM.md「Meeting page (#3862)」): the modes' names, the privacy texts per mode (the contract's
/// Wording), the fallback notice and the recording page's lines. Pure, for the tests and the views alike.
nonisolated enum MeetingTranscriptionWords {
    /// A mode's name where it is picked (the start's row, Settings › Meetings).
    static func title(_ mode: MeetingTranscriptionMode) -> String {
        switch mode {
        case .device: String(localized: "On This iPhone [Meeting Transcription]")
        case .hub: String(localized: "On the Hub [Meeting Transcription]")
        }
    }

    /// The line under Record (`RecordingsSection.startPrivacy`): what leaves the phone in the meeting about to start.
    /// `mode`: the mode it starts in (nil: the hub names none, as before phase 5); `service`: the extension the owner chose
    /// to transcribe, by name (nil: the built-in one, which the text doesn't name); `talk`: Talk would send its spans to
    /// the voice service (phase 4); `days`: how long the text is kept.
    static func startPrivacy(_ mode: MeetingTranscriptionMode?, service: String?, talk: Bool, days: Int) -> String {
        if mode == .hub {
            if let service {
                return String(localized: "This meeting's audio, Talk included, goes through your hub to \(service). The hub deletes the audio once the minutes are written; the text is kept for \(days) days.")
            }
            return String(localized: "This meeting's audio, Talk included, goes through your hub to the speech service it is set up with. The hub deletes the audio once the minutes are written; the text is kept for \(days) days.")
        }
        if talk {
            return String(localized: "Audio stays on this iPhone, except while you hold Talk: what is said then goes to the voice service. Its text syncs to Knowledge and is kept for \(days) days.")
        }
        return String(localized: "Audio stays on this device. Its text syncs to Knowledge and is kept for \(days) days.")
    }

    /// The meeting page's privacy line beside Talk (`MeetingPageBar`): privacy mode's Wording, or hub mode's in a meeting the
    /// hub transcribes (this meeting's audio, Talk included, goes through the hub).
    static func pagePrivacy(hub: Bool, service: String?) -> String {
        guard hub else {
            return String(localized: "Audio stays on this iPhone, except while you hold Talk: what is said then goes to the voice service.")
        }
        if let service {
            return String(localized: "This meeting's audio, Talk included, goes through your hub to \(service), and the hub deletes it once the minutes are written.")
        }
        return String(localized: "This meeting's audio, Talk included, goes through your hub to the speech service it is set up with, and the hub deletes it once the minutes are written.")
    }

    /// What a mode means for privacy, under its choice in Settings › Meetings (the contract's Wording).
    static func modePrivacy(_ mode: MeetingTranscriptionMode, service: String?) -> String {
        switch mode {
        case .device:
            return String(localized: "Audio stays on this iPhone, except while you hold Talk: what is said then goes to the voice service.")
        case .hub:
            if let service {
                return String(localized: "A meeting's audio, Talk included, goes through your hub to \(service), and the hub deletes it once the meeting's minutes are written. The transcript and the minutes are kept as long as your retention settings say.")
            }
            return String(localized: "A meeting's audio, Talk included, goes through your hub to the speech service it is set up with, and the hub deletes it once the meeting's minutes are written. The transcript and the minutes are kept as long as your retention settings say.")
        }
    }

    /// Why the Live box shows the phone's own lines in a hub-mode meeting, once it has fallen back
    /// (`MeetingTranscriptionRule.Shown.fellBack`): waiting for Wi-Fi, the hub stopped for this meeting, or anything else.
    static func fallback(_ meeting: MeetingTranscription.Meeting) -> String {
        if meeting.socket == .waitingForWiFi {
            return String(localized: "Waiting for Wi-Fi to send the audio to the hub. Showing this iPhone's transcript.")
        }
        if meeting.stopped != nil {
            return String(localized: "The hub stopped transcribing this meeting. Showing this iPhone's transcript.")
        }
        return String(localized: "The hub isn't transcribing right now. Showing this iPhone's transcript.")
    }

    /// The recording page's lines for its transcription (`RecordingTranscriptionFacts`): nil when there is nothing to say
    /// (no mode decided, or a meeting on the phone that never asked for the hub).
    struct Facts: Equatable, Sendable {
        var title: String
        /// What the hub is at: finishing (with a spinner), final, failed; or why the meeting stayed on the phone.
        var state: String?
        var busy: Bool
        /// The stretches the hub has no audio for, which the phone's text fills.
        var gaps: String?
        /// The gap uploads after the meeting, while they wait or run.
        var upload: String?
    }

    static func facts(_ view: MeetingTranscriptionView?, asked: MeetingTranscriptionMode?,
                      gapUpload: MeetingTranscription.GapUpload) -> Facts? {
        guard let view else { return nil }
        guard view.mode == .hub else {
            // Asked for the hub, kept on the phone (the hub wasn't ready, or an older manager).
            guard asked == .hub else { return nil }
            return Facts(title: String(localized: "Transcribed on this iPhone"),
                         state: String(localized: "The hub's transcription wasn't ready when this meeting started."), busy: false)
        }
        var facts = Facts(title: String(localized: "Transcribed on the hub"), busy: false)
        switch view.known {
        case .final: facts.state = String(localized: "The hub's transcript is final.")
        case .failed: facts.state = String(localized: "The hub couldn't transcribe it, so this iPhone's transcript is used.")
        default:
            facts.state = String(localized: "The hub is finishing the transcript…")
            facts.busy = true
        }
        if !view.gaps.isEmpty {
            facts.gaps = String(localized: "No audio on the hub for \(spans(view.gaps)). This iPhone's text fills in there.")
        }
        if !view.settled {
            switch gapUpload {
            case .uploading: facts.upload = String(localized: "Sending the missing audio to the hub…")
            case .waitingForWiFi: facts.upload = String(localized: "The missing audio goes to the hub once you're on Wi-Fi.")
            default: break
            }
        }
        return facts
    }

    /// "10:00–10:42, 12:00–12:30": the first three stretches, then how many more.
    static func spans(_ gaps: [MeetingGap]) -> String {
        let shown = gaps.prefix(3).map { "\(RecordingFormat.clock($0.from))–\(RecordingFormat.clock($0.to))" }
        let list = ListFormatter.localizedString(byJoining: shown)
        let more = gaps.count - shown.count
        return more > 0 ? String(localized: "\(list) and \(more) more [Meeting Gaps]") : list
    }

    /// The recording page's footer for a meeting whose audio went to the hub: it no longer says the audio stayed here.
    static func detailFooter(days: Int) -> String {
        String(localized: "This meeting's audio also went through your hub to its speech service; the hub deletes it once the minutes are written. After the retention period (\(days) days) the recording is cleared from this device and Knowledge.")
    }

    /// Whether the recording page's footer says the audio went through the hub (pure, for the tests): `detail.meta`'s
    /// mode once read; before that what this phone knows, so the page never says the audio stayed here while it may not
    /// have: the mode the upload's `200` decided (`hub`; `device` unless an older manager rolled a hub-mode meeting back),
    /// else the meeting asked for the hub and had hub-mode time (`RecordingAttendance.hubTime`).
    static func audioWentToHub(meta: MeetingTranscriptionView?, decided: MeetingTranscriptionRule.Decided?,
                               attendance: RecordingAttendance?) -> Bool {
        if let meta { return meta.mode == .hub }
        if let decided, !decided.olderManager { return decided.mode == .hub }
        guard let attendance, attendance.transcription == .hub else { return false }
        return !attendance.hubTime.isEmpty
    }
}

// MARK: Settings › Meetings

/// Hub transcription's part of Settings › Meetings (#3862 phase 5): the mode meetings start with, the speech service
/// when the hub has more than the built-in one, the glossary, and this iPhone's **Hub Transcription on Wi-Fi Only**.
/// Shown only by a hub that has the settings (`MeetingTranscriptionSettings.offered`).
struct MeetingTranscriptionSettingsSections: View {
    var settings: MeetingTranscriptionSettings
    var values: MeetingTranscriptionSettings.Values

    var body: some View {
        Section {
            Picker(String(localized: "Transcribe Meetings"), selection: Binding(
                get: { values.defaultMode },
                set: { mode in Task { await settings.setDefault(mode) } })) {
                ForEach(MeetingTranscriptionMode.allCases, id: \.self) { mode in
                    Text(MeetingTranscriptionWords.title(mode)).tag(mode)
                }
            }
            .pickerStyle(.inline)
            .labelsHidden()
            .accessibilityIdentifier("settings-meeting-transcription")
            .disabled(settings.saving)
            if MeetingTranscriptionSettings.showsChoice(settings.choices) {
                Picker(selection: Binding(get: { values.extensionRef }, set: { ref in Task { await settings.setExtension(ref) } })) {
                    ForEach(settings.choices) { choice in
                        Text(MeetingTranscriptionSettings.label(choice, in: settings.choices)).tag(choice.ref)
                    }
                } label: {
                    Label { Text("Speech Service") } icon: { ThemedIcon(Theme.Icon.meetingTranscription) }
                }
                .pickerStyle(.menu)
                .disabled(settings.saving)
                .accessibilityIdentifier("settings-meeting-transcription-service")
            }
            NavigationLink { MeetingGlossaryPage(settings: settings) } label: {
                LabeledContent {
                    Text(values.glossary.isEmpty ? String(localized: "None [Meeting Glossary]") : String(localized: "\(values.glossary.count) terms"))
                } label: {
                    Label { Text("Glossary [Meeting Transcription]") } icon: { ThemedIcon(Theme.Icon.glossary) }
                }
            }
            .accessibilityIdentifier("settings-meeting-glossary")
        } header: {
            Text("Transcription [Meeting Transcription]")
        } footer: {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text(MeetingTranscriptionWords.modePrivacy(values.defaultMode, service: settings.service))
                if values.defaultMode == .hub, !values.ready {
                    Text("The hub's speech service isn't ready, so meetings are transcribed on this iPhone until it is.")
                        .accessibilityIdentifier("settings-meeting-transcription-not-ready")
                }
                if let failure = settings.failure, case .hub(let words) = failure {
                    Text(words).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("settings-meeting-transcription-problem")
                }
            }
        }
        Section {
            Toggle(isOn: Binding(get: { settings.wifiOnly }, set: { settings.setWiFiOnly($0) })) {
                Label { Text("Hub Transcription on Wi-Fi Only") } icon: { ThemedIcon(Theme.Icon.wifiOnly) }
            }
            .accessibilityIdentifier("settings-meeting-wifi-only")
        } footer: {
            Text("For this iPhone only. With it on, a meeting transcribed on the hub sends no audio over cellular data: this iPhone transcribes it meanwhile, and the audio goes to the hub once you're on Wi-Fi. An hour of meeting is about 115 MB.")
        }
    }
}

/// The glossary (#3862 phase 5, `meeting_glossary`): names and words the hub's transcription should spell right. A field
/// to add one and the list, a swipe deletes; each change is saved at once, cleaned as the hub cleans it, and one the hub
/// would refuse (over 200 terms, a term over 40 characters) is said here and never sent.
struct MeetingGlossaryPage: View {
    var settings: MeetingTranscriptionSettings
    @State private var draft = ""
    @FocusState private var typing: Bool

    private var terms: [String] { settings.values?.glossary ?? [] }
    private var blank: Bool { draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        SettingsForm {
            Section {
                HStack(spacing: Theme.Space.s) {
                    TextField(String(localized: "Add a Name or Word"), text: $draft)
                        .focused($typing)
                        .submitLabel(.done)
                        .onSubmit { add() }
                        .autocorrectionDisabled()
                        .accessibilityIdentifier("glossary-field")
                    Button { add() } label: { Text("Add [Meeting Glossary]").lineLimit(1) }
                        .buttonStyle(.borderless)
                        .disabled(blank || settings.saving)
                        .accessibilityIdentifier("glossary-add")
                }
            } footer: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let problem {
                        Text(problem).foregroundStyle(Theme.danger)
                            .accessibilityIdentifier("glossary-problem")
                    }
                    Text("Names and words the hub's transcription should spell right, such as people, products and projects: up to 200, each at most 40 characters. A meeting's title and its attendees are added by themselves.")
                }
            }
            if !terms.isEmpty {
                Section {
                    ForEach(terms, id: \.self) { term in
                        Text(term).font(Theme.Font.body)
                            .accessibilityIdentifier("glossary-term")
                            // One change at a time: a delete while another saves would not be sent.
                            .deleteDisabled(settings.saving)
                    }
                    .onDelete { offsets in
                        var next = terms
                        next.remove(atOffsets: offsets)
                        Task { await settings.setGlossary(next) }
                    }
                } header: {
                    Text("\(terms.count) terms")
                }
            }
        }
        .themedPage()
        .navigationTitle(String(localized: "Glossary [Meeting Transcription]"))
        .navigationBarTitleDisplayMode(.inline)
    }

    /// The term typed goes first, so the newest is at the top; the field empties only once the hub kept it. Return while
    /// another change is still being saved does nothing and keeps the text, as Add is off then.
    private func add() {
        guard !blank, !settings.saving else { return }
        let term = draft
        Task {
            if await settings.setGlossary([term] + terms), draft == term { draft = "" }
        }
    }

    /// Why the last change didn't go, in words.
    private var problem: String? {
        switch settings.failure {
        case .tooManyTerms?: String(localized: "The glossary holds at most 200 terms.")
        case .termTooLong(let term)?: String(localized: "“\(term)” is longer than 40 characters.")
        case .hub(let words)?: words
        case nil: nil
        }
    }
}

// MARK: The meeting page

/// The notice that hub transcription fell back to this iPhone (#3862 phase 5): why, in words
/// (`MeetingTranscriptionWords.fallback`), with the phone's mark in the caution color. Under the Live box's title, under
/// the Highlights card's (phase 2b) and atop the meeting's transcript.
struct MeetingTranscriptionFallbackNotice: View {
    var words: String

    var body: some View {
        Label {
            Text(words).fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(Theme.Icon.phone).foregroundStyle(Theme.caution)
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("meeting-transcription-fallback")
    }
}

/// The fallback notice for the meeting `id` while its hub transcription has fallen back (`MeetingTranscription.shown`),
/// nothing otherwise: for what shows no line of the store itself (the Highlights card, the transcript page). A view of
/// its own, so the store's changes (a caption a second in hub mode) redraw only it (P5).
struct MeetingTranscriptionFallbackRow: View {
    var id: UUID
    @State private var transcription = MeetingTranscription.shared

    var body: some View {
        // Whether it fell back doesn't hang on the role (only which lines show does).
        if transcription.shown(id, role: .off).fellBack {
            MeetingTranscriptionFallbackNotice(words: MeetingTranscriptionWords.fallback(transcription.meeting(id)))
        }
    }
}

/// The hub's caption (#3862 phase 5): what is being said now, its time and its words in the secondary color, replaced by
/// the next one; read as "Being said: …". In the Live box (cut at `lineLimit`) and under the meeting's transcript.
///
/// The interface stops at xxxLarge (docs/design/DESIGN_SYSTEM.md "Dynamic Type"), so it has one layout: no branch of its
/// own for the accessibility sizes.
struct MeetingCaptionRow: View {
    var caption: MeetingInterim
    var lineLimit: Int?

    var body: some View {
        let stamp = Text(RecordingFormat.clock(caption.start)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
        let words = Text(caption.text).font(Theme.Font.body).foregroundStyle(.secondary).lineLimit(lineLimit)
            .fixedSize(horizontal: false, vertical: lineLimit == nil)
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) { stamp; words }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Text("Being said: \(caption.text)"))
    }
}

/// The hub's caption under the meeting's transcript (`MeetingTranscriptPage`) while the page shows it
/// (`MeetingTranscription.shown`), nothing otherwise. A view of its own, so a caption redraws only it (P5).
struct MeetingTranscriptCaption: View {
    var id: UUID
    var role: MeetingAttendance
    @State private var transcription = MeetingTranscription.shared

    var body: some View {
        if transcription.shown(id, role: role).hubInterim, let caption = transcription.meeting(id).interim, !caption.text.isEmpty {
            MeetingCaptionRow(caption: caption, lineLimit: nil)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityIdentifier("meeting-transcript-interim")
        }
    }
}

/// Hands the meeting's transcript page the hub's lines while it shows them (`MeetingTranscription.shown`), nil while it
/// shows the phone's own. It draws nothing and is a view of its own, so the store's captions redraw only it, and the
/// page's rows are built again only when the lines change (P4, P5).
struct MeetingTranscriptHubFeed: View {
    var id: UUID
    var role: MeetingAttendance
    @Binding var lines: [MeetingHubLine]?
    @State private var transcription = MeetingTranscription.shared

    var body: some View {
        let shown = transcription.shown(id, role: role).hubLines ? transcription.meeting(id).lines : nil
        Color.clear
            .onChange(of: shown, initial: true) { _, new in
                if lines != new { lines = new }
            }
    }
}

// MARK: The recording's page

/// The recording page's last footer (#3862 phase 5): what left the phone and how long it is kept. A meeting whose audio
/// went through the hub says so (`MeetingTranscriptionWords.audioWentToHub`), from `detail.meta` once read and from what the
/// phone knows before (the store, the attendance), so it never says the audio stayed here while the page waits for the
/// minutes or is offline; else `device`. A view of its own, so the store's changes redraw only it (P5).
struct RecordingRetentionFooter: View {
    var id: UUID
    var attendance: RecordingAttendance?
    var meta: MeetingTranscriptionView?
    var days: Int
    /// The text when the audio stayed here.
    var device: String
    @State private var store = MeetingTranscription.shared

    var body: some View {
        let hub = MeetingTranscriptionWords.audioWentToHub(meta: meta, decided: store.meeting(id).decided, attendance: attendance)
        Text(hub ? MeetingTranscriptionWords.detailFooter(days: days) : device)
    }
}

/// The recording page's transcription row: what `detail.meta.transcription` says (`view`, read with the minutes), else
/// what the transcription store last read, with the gap uploads the store runs. A view of its own, so the store's changes
/// (a meeting recording elsewhere moves it every second) redraw only it (P5).
struct RecordingTranscriptionRow: View {
    var id: UUID
    var view: MeetingTranscriptionView?
    /// The mode the meeting asked for as it started (`RecordingAttendance.transcription`).
    var asked: MeetingTranscriptionMode?
    @State private var store = MeetingTranscription.shared

    var body: some View {
        let meeting = store.meeting(id)
        if let facts = MeetingTranscriptionWords.facts(view ?? meeting.hub, asked: asked, gapUpload: meeting.gapUpload) {
            RecordingTranscriptionFacts(facts: facts)
        }
    }
}

/// How the recording was transcribed, on its page (#3862 phase 5): on the hub, how far the hub is (finishing until its
/// final pass is done, final or failed), the stretches it has no audio for, and the gap uploads; or that a meeting that
/// asked for the hub stayed on the phone.
struct RecordingTranscriptionFacts: View {
    var facts: MeetingTranscriptionWords.Facts

    var body: some View {
        Label {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(facts.title).font(Theme.Font.secondaryMedium).foregroundStyle(.primary)
                if let state = facts.state {
                    HStack(spacing: Theme.Space.xs) {
                        if facts.busy { ProgressView().controlSize(.small) }
                        Text(state)
                    }
                }
                if let gaps = facts.gaps { Text(gaps) }
                if let upload = facts.upload { Text(upload) }
            }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
        } icon: {
            ThemedIcon(Theme.Icon.meetingTranscription).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("recording-transcription")
    }
}
#endif
