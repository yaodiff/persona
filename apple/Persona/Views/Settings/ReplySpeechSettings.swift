#if os(iOS)
import AVFAudio
import SwiftUI

/// Main Chat read-aloud belongs to the iPhone listening to the conversation.
nonisolated enum ReplySpeechPreferences {
    static let enabled = "mainChat.readAloud"
    static let full = "mainChat.readAloud.full"
    static let engine = "mainChat.readAloud.engine"
    static let rate = "mainChat.readAloud.rate"
    static let voice = "mainChat.readAloud.voice"
}

struct ReplySpeechMenuToggle: View {
    @StoredValue(ReplySpeechPreferences.enabled) private var enabled = false

    var body: some View {
        Toggle(isOn: $enabled) {
            Label("Read Replies Aloud", systemImage: Theme.Icon.answersAloud)
        }
        .accessibilityIdentifier("reply-speech-menu")
    }
}

struct ReplySpeechSettings: View {
    @StoredValue(ReplySpeechPreferences.enabled) private var enabled = false
    @StoredValue(ReplySpeechPreferences.full) private var full = false
    @StoredValue(ReplySpeechPreferences.engine) private var engine = "auto"
    @StoredValue(ReplySpeechPreferences.rate) private var rate = 1.0
    @StoredValue(ReplySpeechPreferences.voice) private var voice = ""
    @State private var extensionChoices: [(id: String, name: String)] = []
    @State private var voices: [(id: String, name: String)] = []

    var body: some View {
        SettingsForm {
            Section {
                SettingsSwitchRow(title: String(localized: "Read Replies Aloud"), symbol: Theme.Icon.answersAloud,
                                  accessibilityID: "reply-speech-enabled", isOn: $enabled)
            } footer: {
                Text("Reads new Main Chat replies while the chat is open on this iPhone.")
            }
            Section {
                Picker("Reading Length", selection: $full) {
                    Text("Opening Only").tag(false)
                    Text("Full Reply").tag(true)
                }
                .accessibilityIdentifier("reply-speech-range")
                Picker("Voice Source", selection: $engine) {
                    Text("Automatic").tag("auto")
                    Text("On This iPhone").tag("device")
                    ForEach(extensionChoices, id: \.id) { ext in Text(ext.name).tag(ext.id) }
                    if engine != "auto", engine != "device", !extensionChoices.contains(where: { $0.id == engine }) {
                        Text("Unavailable Extension").tag(engine).disabled(true)
                    }
                }
                .accessibilityIdentifier("reply-speech-engine")
                Picker("iPhone Voice", selection: $voice) {
                    Text("Automatic").tag("")
                    ForEach(voices, id: \.id) { choice in Text(choice.name).tag(choice.id) }
                    if !voice.isEmpty, !voices.contains(where: { $0.id == voice }) {
                        Text("Unavailable Voice").tag(voice).disabled(true)
                    }
                }
                .pickerStyle(.navigationLink)
                .accessibilityIdentifier("reply-speech-voice")
                Stepper(value: $rate, in: 0.5...2.0, step: 0.25) {
                    LabeledContent("Speaking Rate", value: "\(rate.formatted())×")
                }
                .accessibilityIdentifier("reply-speech-rate")
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Read Replies Aloud"))
        .task {
            extensionChoices = await ReplySpeechPlayback.extensions().map { ("extension:" + $0.id, $0.name) }
        }
        .task {
            voices = await Task.detached(priority: .userInitiated) {
                AVSpeechSynthesisVoice.speechVoices().map { voice in
                    let language = Locale.current.localizedString(forIdentifier: voice.language) ?? voice.language
                    return (id: voice.identifier, name: "\(language) · \(voice.name)")
                }.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }
            }.value
        }
    }
}
#endif
