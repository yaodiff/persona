import SwiftUI

/// Settings › Appearance › Navigation (#6332): how the iPhone and iPad lay out Persona and the Agents.
///
/// - `separate` (the default): Assistant and Agents as two pages, the Assistant | Agents switch at the top on an
///   iPhone and the Persona | Agents tabs on an iPad. Unchanged from before the choice existed.
/// - `sidebar`: everything in one sidebar, as the web app has it: the main conversation, the pages, Persona's chats and
///   the Agents list. What is picked in it is the page beside it (docs/apple/ios.md「Sidebar layout」).
///
/// This device's own choice (not the hub's), read where the app's root is chosen (`RootView`); a change takes effect at
/// once. The Mac (frozen) and Apple Vision Pro keep their own navigation and never show the choice.
enum NavigationLayout: String, CaseIterable, Identifiable {
    case separate, sidebar

    /// The UserDefaults key; `-app.navigation sidebar` at launch picks the sidebar (UI tests, screenshots).
    static let key = "app.navigation"

    var id: String { rawValue }

    /// The choice's name, as the Settings row's value shows it.
    var title: String {
        switch self {
        case .separate: String(localized: "Separate [Navigation Layout]")
        case .sidebar: String(localized: "Sidebar [Navigation Layout]")
        }
    }

    /// One sentence under the choice on the Navigation page.
    var summary: String {
        switch self {
        case .separate:
            String(localized: "Assistant and Agents each have a page, switched at the top. On iPad they are the Persona and Agents tabs.")
        case .sidebar:
            String(localized: "The main chat, pages, Persona chats and Agents are all in one sidebar, as in the web app. On iPhone it slides in from the left; on iPad it stays on the left.")
        }
    }

    /// Where the Sidebar layout is built (#6332): the iPhone (the drawer) and the iPad (the column). The Navigation row
    /// shows only where it does something.
    static var offered: Bool {
        #if os(iOS)
        true
        #else
        false
        #endif
    }
}
