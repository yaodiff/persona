#if os(iOS)
import SwiftUI

/// Settings › Meetings (#2981, docs/assistant/meeting-minutes.md「Attendance fields」): the role every meeting recording
/// starts with, kept on the hub (`meeting_attendance_default`, Off until changed), and what Persona may say in front of
/// others. Each recording can still pick its own role and session (the Recordings page). Shown only by a hub that offers
/// attending. A hub with hub transcription (#3862 phase 5) adds how meetings are transcribed, its speech service, the
/// glossary and this iPhone's Wi-Fi rule (`MeetingTranscriptionSettingsSections`).
struct MeetingSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var capturedAPI: APIClient?
    @State private var role: MeetingAttendance?
    @State private var saving = false
    /// Hub transcription's settings, made once the page shows (its model reads this phone's Wi-Fi rule: P3).
    @State private var transcription: MeetingTranscriptionSettings?

    var body: some View {
        SettingsForm {
            Section {
                Picker(String(localized: "Persona in Meetings"), selection: Binding(
                    get: { role ?? .off },
                    set: { role in Task { await save(role) } })) {
                    ForEach(MeetingAttendance.allCases, id: \.self) { Text($0.title).tag($0) }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                .accessibilityIdentifier("settings-meeting-attendance")
                .disabled(role == nil || saving)
            } header: {
                Text("Persona in Meetings")
            } footer: {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let role { Text(role.detail) }
                    Text("Each meeting recording starts with this. To change it for one meeting, pick another on the Recordings page before you start.")
                }
            }
            if let transcription, transcription.offered, let values = transcription.values {
                MeetingTranscriptionSettingsSections(settings: transcription, values: values)
            }
            Section {
                Label("Answers only from meeting material: earlier minutes, project progress and who owns a to-do.", systemImage: Theme.Icon.meeting)
                Label("Never brings up private chats, mail, health or other personal data.", systemImage: Theme.Icon.privacy)
                Label("Speaks only when you tap Ask Persona, and only when it takes part.", systemImage: Theme.Icon.question)
            } header: {
                Text("In Front of Others")
            } footer: {
                Text("The hub holds Persona to these limits in a meeting, whatever it is asked.")
            }
            .font(Theme.Font.secondary)
        }
        .themedPage()
        .navigationTitle(String(localized: "Meetings"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await load()
            if transcription == nil { transcription = MeetingTranscriptionSettings(deps: .live) }
            await transcription?.load()
        }
    }

    private func load() async {
        if capturedAPI == nil { capturedAPI = model.api?.currentAssistantSettings() }
        guard let api = capturedAPI else { return }
        do {
            let settings = try await api.assistant()
            role = settings.meetingAttendanceDefault.flatMap(MeetingAttendance.init(rawValue:)) ?? .off
        } catch { model.show(error) }
    }

    private func save(_ next: MeetingAttendance) async {
        guard !saving, let api = capturedAPI else { return }
        saving = true
        defer { saving = false }
        do {
            _ = try await api.setAssistant(meetingAttendanceDefault: next.rawValue)
            role = next
            if api.sameSettingsScope(as: model.api) { await model.refreshAssistant() }
        } catch { model.show(error) }
    }
}
#endif
