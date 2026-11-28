import SwiftUI
import WidgetKit

@main
struct PersonaWidgetsBundle: WidgetBundle {
    init() {
        // The app's interface language (App Group), for this extension's next launch (docs/apple/ios.md「Interface language」).
        LanguageStore.shared.follow(LanguageStore.shared.groupResolved)
    }

    var body: some Widget {
        HomeWidgetBundle().body
        ExtensionSystemWidget()
        LiveActivityBundle().body
        MeetingRecordingControl()
        AskPersonaControl()
        PersonaAgentsControl()
        MessagePersonaControl()
        DictatePersonaControl()
        CallPersonaControl()
        CompatibleAssistantControl()
        CompatibleAgentsControl()
    }
}

/// Persona's Live Activities, a bundle of their own so the main one stays within what a widget bundle's builder takes.
struct LiveActivityBundle: WidgetBundle {
    var body: some Widget {
        FleetLiveActivity()
        CompatibleFleetLiveActivity()
        RecordingLiveActivity()
        #if canImport(AlarmKit)
        if #available(iOS 26.0, *) {
            AlarmLiveActivity()
        }
        #endif
    }
}

/// Persona's Home Screen widgets (docs/apple/home-widgets.md), a bundle of their own so the main one stays within what
/// a widget bundle's builder takes.
struct HomeWidgetBundle: WidgetBundle {
    var body: some Widget {
        AgentsHomeWidget()
        TodayHomeWidget()
        TodosHomeWidget()
        InboxHomeWidget()
        UsageHomeWidget()
        ComputersHomeWidget()
        RecordHomeWidget()
        HealthWidgetBundle().body
    }
}

/// Keep every bundle builder below ten children, and preserve the existing Health kind.
struct HealthWidgetBundle: WidgetBundle {
    var body: some Widget {
        HealthHomeWidget()
        WaterHomeWidget()
        ProteinHomeWidget()
        HealthTodayHomeWidget()
    }
}
