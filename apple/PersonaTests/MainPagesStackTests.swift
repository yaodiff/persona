import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Every main page fits an iPhone's main-thread stack in a Debug build (#6735). A Debug build on a phone crashed on every
/// launch into Agents: unoptimized, a function reserves a stack slot on entry for every opaque intermediate of a modifier
/// chain, each holding everything before it, and the Agents page's first render reached 941 KB of the device's 1008 KB.
/// The simulator's main thread has 8 MB, so nothing showed it there; `StackPeak` measures the depth instead.
///
/// Each page renders once, with an empty model (no hub), in a phone-sized window inside a navigation stack. The slots that
/// overflowed are sized by the views' types, not their data, so an empty page measures them; rows and sheets a page
/// only builds with data are not covered. The pages share one process: the first builds the Swift runtime's metadata for
/// types the others use too, so a page measured later can read up to ~100 KB less than its cold first render on a
/// phone. The budget (512 KB, half the device's stack) leaves room for that.
///
/// A page over the budget: cut its long modifier chains (`stackSegment()`, `AnyView` at stage boundaries) and measure
/// again (`docs/lessons/ios.md`, "A Debug build on an iPhone has a 1 MB main-thread stack"). The pages still over it have
/// a ceiling below, which may only go down.
@MainActor struct MainPagesStackTests {
    /// Pages over the budget on the day this test came in (simulator, Debug, Xcode 26.0): lower a ceiling when its page
    /// improves, delete it once the page is under the budget; never raise one.
    static let ceilings: [Page: Int] = [.assistant: 600 * 1024, .shell: 688 * 1024, .sidebar: 672 * 1024, .profile: 648 * 1024]

    enum Page: String, CaseIterable, CustomTestStringConvertible {
        case assistant, shell, agents, session, inbox, lark, settings, sidebar
        case todos, machines, terminals, profile, routines, knowledge, recordings

        var testDescription: String { rawValue }

        @MainActor @ViewBuilder var view: some View {
            switch self {
            // The Assistant page as the phone's shell draws it; `shell` is the whole of it, with the drawer and the bar.
            case .assistant: AssistantView().environment(\.shell, ShellState())
            case .shell: ShellView()
            case .agents: ChatsView()
            case .session: ChatView(key: SessionKey(machineID: "box", sessionID: "stack-test"))
            case .inbox: InboxPage(path: .constant(NavigationPath()), accountState: InboxAccountState(), embedded: true)
            case .lark: LarkConversationsView()
            case .settings: SettingsView().environment(\.settingsSheet, true)
            case .sidebar:
                AssistantSidebar(current: nil, open: { _ in }, newConversation: {}, openProfile: {}, openRoutines: {},
                                 openKnowledge: {}, openSettings: {}, drawer: nil)
            case .todos: TodosView()
            case .machines: MachinesView()
            case .terminals: TerminalsView(only: nil)
            case .profile: AssistantProfileView()
            case .routines: AssistantRoutinesView()
            case .knowledge: KnowledgeView()
            case .recordings: RecordingsPage()
            }
        }

        /// Pages that bring their own navigation are not wrapped in another stack.
        var ownsNavigation: Bool { self == .assistant || self == .shell || self == .agents }
    }

    @Test(arguments: Page.allCases) func thePageFitsAPhonesMainThreadStack(_ page: Page) throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "stack-\(page)-" + UUID().uuidString))
        let root = page.ownsNavigation ? AnyView(page.view) : AnyView(NavigationStack { page.view })
        let (bytes, window) = StackPeak.render(root.environment(model), in: scene)
        defer { window.isHidden = true }
        let limit = Self.ceilings[page] ?? StackPeak.pageBudget
        print("MainPagesStackTests: \(page) \(bytes / 1024) KB (limit \(limit / 1024) KB)")
        #expect(bytes < limit,
                "the \(page) page's first render reached \(bytes / 1024) KB of stack (limit \(limit / 1024) KB; an iPhone's main thread has about 1 MB)")
    }

    /// The measure itself: a page known to be shallow stays far under the budget, so a failure above is the page's.
    @Test func aPlainListIsFarUnderTheBudget() throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let (bytes, window) = StackPeak.render(NavigationStack { List { Text("row") } }, in: scene)
        defer { window.isHidden = true }
        #expect(bytes > 0 && bytes < 200 * 1024, "a one-row List reached \(bytes / 1024) KB")
    }
}
