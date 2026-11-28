import SwiftUI
import os

/// The opt-in iPhone Agent console (#17129): Main chat, Agents and Settings. External conversations are a segment in
/// Agents, never a fourth tab. The existing native pages keep their navigation, search and chat routes; only the root
/// and their presentation choices change after a template is applied.
@MainActor @Observable final class AgentConsoleRouter: TabRouter {
    private weak var model: AppModel?
    private var front = AppTab.assistant

    func attach(to model: AppModel) {
        self.model = model
        model.tabRouter = self
        front = model.selectedTabNow == .chats || model.selectedTabNow == .settings ? model.selectedTabNow : .assistant
        model.selectedTab = front
    }

    func detach(from model: AppModel) {
        guard model.tabRouter === self else { return }
        model.tabRouter = nil
    }

    func show(_ page: ShellPage) {
        if page == .messages { model?.selectedTab = .chats }
        else { model?.selectedTab = .assistant }
    }

    func route(_ tab: AppTab, in model: AppModel) {
        switch tab {
        case .assistant, .chats, .settings:
            front = tab
        case .feishu:
            front = .chats
            model.selectedTab = .chats
        case .mail:
            front = .assistant
            model.selectedTab = .assistant
        }
    }
}

struct AgentConsoleRoot: View {
    @Environment(AppModel.self) private var model
    @State private var router = AgentConsoleRouter()
    @State private var agentsMode: AgentConsoleAgentsMode = .running

    typealias Body = AnyView

    var body: AnyView {
        AnyView(content)
    }

    // Keep the TabView out of View.Body's associated-type witness (the iOS 27 TestFlight abort is in BodyAccessor).
    @ViewBuilder private var content: some View {
        // A relaunch can restore .feishu or .mail from before the template. The console has neither tab; map it before
        // TabView reads the selection rather than letting UIKit see a value its current tab set does not contain.
        let selected: AppTab = switch model.selectedTab {
        case .chats, .feishu: .chats
        case .settings: .settings
        default: .assistant
        }
        TabView(selection: Binding(get: { selected }, set: { model.selectHomeTab($0) })) {
            AgentConsoleMainTab()
            AgentConsoleAgentsTab(mode: $agentsMode)
            AgentConsoleSettingsTab()
        }
        .environment(\.agentConsole, true)
        .onAppear {
            // Non-content breadcrumbs for a device-only launch failure: tab id and whether the console was restored.
            Logger(subsystem: "com.persona.console", category: "launch").log("console appeared tab=\(model.selectedTab.rawValue, privacy: .public)")
            router.attach(to: model)
        }
        .onDisappear { router.detach(from: model) }
        .onChange(of: NativeTemplateStore.shared.enabled) { _, enabled in
            if !enabled, model.selectedTab == .settings {
                model.settingsPath = NavigationPath()
                model.selectedTab = .assistant
            }
        }
    }
}

/// Each tab is a separate `TabContent`, as the existing RootTabs are. Keeping the page types out of the root's
/// conditional body avoids the iOS 27 SwiftUI associated-type crash seen in the TestFlight restart reports (#17129).
@MainActor private struct AgentConsoleMainTab: TabContent {
    @Environment(AppModel.self) private var model

    var body: some TabContent<AppTab> {
        Tab(String(localized: "Main Chat"), systemImage: Theme.Icon.ai, value: AppTab.assistant) {
            AssistantView().modifier(TabActivityScope(tab: .assistant))
        }
        .badge(model.badges.assistant)
    }
}

@MainActor private struct AgentConsoleAgentsTab: TabContent {
    @Environment(AppModel.self) private var model
    @Binding var mode: AgentConsoleAgentsMode

    var body: some TabContent<AppTab> {
        Tab("Agents", systemImage: Theme.Icon.agents, value: AppTab.chats) {
            VStack(spacing: 0) {
                Picker("Agent conversations", selection: $mode) {
                    Text("Running").tag(AgentConsoleAgentsMode.running)
                    Text("External").tag(AgentConsoleAgentsMode.external)
                }
                .pickerStyle(.segmented)
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
                .accessibilityIdentifier("agent-console-running-external")

                if mode == .running {
                    ChatsView().environment(\.agentConsole, true)
                } else {
                    AgentConsoleExternalAgents()
                }
            }
            .modifier(TabActivityScope(tab: .chats))
        }
        .badge(model.badges.agents)
    }
}

@MainActor private struct AgentConsoleExternalAgents: View {
    var body: some View {
        NavigationStack {
            PersonalAgentListView()
                .accessibilityIdentifier("agent-console-external-agents")
                .navigationDestination(for: PersonalAgentSession.self) { session in
                    PersonalAgentSessionView(session: session).id(session.id)
                }
        }
    }
}

@MainActor private struct AgentConsoleSettingsTab: TabContent {
    var body: some TabContent<AppTab> {
        Tab(String(localized: "Settings"), systemImage: Theme.Icon.settings, value: AppTab.settings) {
            SettingsView().modifier(TabActivityScope(tab: .settings))
        }
    }
}

enum AgentConsoleAgentsMode: Hashable { case running, external }

extension EnvironmentValues {
    @Entry var agentConsole = false
}
