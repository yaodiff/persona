#if os(iOS)
import SwiftUI

struct WorkoutReportSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var reports = WorkoutReports.shared

    var body: some View {
        SettingsForm {
            Section {
                SettingsSwitchRow(title: String(localized: "Automatic workout reports"),
                                  symbol: Theme.Icon.health, isOn: Binding(
                                    get: { reports.enabled },
                                    set: { value in Task { await reports.setEnabled(value, model: model) } }))
                .disabled(reports.busy || model.hub == nil || model.isDemo)
            } footer: {
                // Which mode this build is in (#2526): right after a workout with HealthKit background delivery, else on opening.
                if reports.immediate {
                    Text("Off by default. After you enable this, a workout of at least 10 minutes is reported right after it is saved to Health, even while Persona is closed: its measurements go to your hub to create a report. Older workouts are not imported. Reports already submitted may finish after you turn this off.")
                } else {
                    Text("Off by default. After you enable this, opening Persona checks for new workouts of at least 10 minutes and sends their measurements to your hub to create a report. Older workouts are not imported. Reports already submitted may finish after you turn this off.")
                }
            }
            Section {
                Text("Reports contain measurement cards, heart-rate and pace charts when data is available, a comparison with your recent workouts of the same kind, and a brief summary. Health read permissions are managed in iOS Settings; an empty result can also mean access was not granted.")
            }
            if let error = reports.error {
                Section { Text(error).foregroundStyle(Theme.caution) }
            }
        }
        .themedPage()
        .detailTitle(String(localized: "Health"))
    }
}
#endif
