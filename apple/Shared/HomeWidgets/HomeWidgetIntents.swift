#if os(iOS)
import AppIntents
import Foundation
import WidgetKit

/// Ticks a to-do off from the To-dos widget (medium and large). It runs in the app (the widget extension never
/// reaches the hub): the app marks it done in the widget's snapshot at once, tells the hub, and on a refusal marks it
/// failed, so the widget says it did not go through and a tap tries again
/// (docs/apple/home-widgets.md「Ticking off a to-do」). The extension builds only the type.
struct CompleteTodoIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Complete To-Do [Home Widgets]"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }

    @Parameter(title: "To-Do [Home Widgets]") var todoID: String

    nonisolated init() {}
    nonisolated init(todoID: String) { self.todoID = todoID }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await HomeWidgetActions.complete(todoID: todoID)
        #endif
        return .result()
    }
}

/// The Calendar widget's switch (Today · Week · Month): changes the view of the widgets kept under `slot`
/// (`HomeCalendarState`) and redraws them, without the app. It needs nothing but the App Group, so it runs in
/// whichever process the system picks (docs/apple/home-widgets.md「Calendar: Today, Week and Month」).
struct ShowCalendarViewIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Show Calendar View [Home Widgets]"
    nonisolated static let isDiscoverable = false

    @Parameter(title: "Widget [Home Widgets]") var slot: String
    @Parameter(title: "View [Home Widgets]") var view: String

    nonisolated init() {}
    nonisolated init(slot: String, view: HomeCalendarView) {
        self.slot = slot
        self.view = view.rawValue
    }

    /// Off the main actor: it writes the widget's state file (#7252).
    @concurrent nonisolated func perform() async throws -> some IntentResult {
        let slot = slot
        if let view = HomeCalendarView(rawValue: view) {
            HomeCalendarStore.shared.update { $0.show(view, in: slot) }
        }
        await HomeCalendarStore.reload()
        return .result()
    }
}

/// A day tapped in the Calendar widget's week or month: shows that day's events in the widget, without the app;
/// today, or the day already picked, goes back to today. A picked day lasts `HomeCalendarState.pickLasts`.
struct PickCalendarDayIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Pick a Day [Home Widgets]"
    nonisolated static let isDiscoverable = false

    @Parameter(title: "Widget [Home Widgets]") var slot: String
    @Parameter(title: "Day [Home Widgets]") var day: String

    nonisolated init() {}
    nonisolated init(slot: String, day: String) {
        self.slot = slot
        self.day = day
    }

    /// Off the main actor: it writes the widget's state file (#7252).
    @concurrent nonisolated func perform() async throws -> some IntentResult {
        let slot = slot, day = day, now = Date.now
        HomeCalendarStore.shared.update { $0.pick(day, in: slot, at: now) }
        await HomeCalendarStore.reload()
        return .result()
    }
}
#endif
