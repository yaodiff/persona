import SwiftUI

/// Every hit region of a worktree shares these commands and the exact host-owned creation target.
struct WorktreeSidebarMenu: View, Equatable {
    enum Command: CaseIterable {
        case open, newSession, newChild, rename, copyPath, copyName, openParent

        var title: LocalizedStringResource {
            switch self {
            case .open: "Open Tree [Agent Menu]"
            case .newSession: "New Session [Agent Menu]"
            case .newChild: "New Child Worktree"
            case .rename: "Rename Worktree"
            case .copyPath: "Copy Path"
            case .copyName: "Copy Name [Agent Menu]"
            case .openParent: "Open Parent [Agent Menu]"
            }
        }

        func label(_ symbol: String) -> some View {
            Label { Text(title) } icon: { Image(systemName: symbol) }
                .lineLimit(1)
        }
    }

    nonisolated let workspace: HostWorkspace
    nonisolated let parent: HostWorkspace?
    nonisolated let sessions: [Session]
    nonisolated let agent: Session?
    #if os(iOS)
    /// Each Agent's menu reads its own facts (`AgentActionMenuReader`): built here, they made the list observe the
    /// machines and links of every Agent in it (#10231).
    nonisolated let handlers: AgentMenuHandlers
    #else
    nonisolated let menus: [AgentRowMenu]
    #endif
    var open: (HostWorkspace) -> Void
    var newSession: (HostWorkspace) -> Void
    var newChild: (HostWorkspace) -> Void
    var rename: (HostWorkspace) -> Void = { _ in }

    /// Transcript pushes and workspace polling must not replace an open native menu.
    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.workspace.key == b.workspace.key && a.workspace.title == b.workspace.title && a.workspace.cwd == b.workspace.cwd
            && a.workspace.online == b.workspace.online && a.workspace.available == b.workspace.available
            && a.parent?.key == b.parent?.key && a.agent?.key == b.agent?.key && sameMenus(a, b)
            && a.sessions.map { [$0.key.path, $0.title] } == b.sessions.map { [$0.key.path, $0.title] }
    }

    #if os(iOS)
    nonisolated private static func sameMenus(_ a: Self, _ b: Self) -> Bool { a.handlers === b.handlers }
    #else
    nonisolated private static func sameMenus(_ a: Self, _ b: Self) -> Bool { a.menus == b.menus }
    #endif

    var body: some View {
        Section {
            Button { open(workspace) } label: {
                Command.open.label(Theme.Icon.openSpace)
            }
            .accessibilityIdentifier("worktree.open")
            Button { newSession(workspace) } label: {
                Command.newSession.label(Theme.Icon.compose)
            }
            .disabled(!workspace.online || !workspace.available || workspace.cwd.isEmpty)
            .accessibilityIdentifier("workspace.newSession")
            Button { newChild(workspace) } label: {
                Command.newChild.label(Theme.Icon.newWorktree)
            }
            .disabled(!workspace.online || !workspace.available)
            .accessibilityIdentifier("workspace.newChild")
            Button { rename(workspace) } label: {
                Command.rename.label(Theme.Icon.edit)
            }
            .disabled(!workspace.online || !workspace.available)
            .accessibilityIdentifier("worktree.rename")
        }
        Section {
            Button { UIPasteboard.general.string = workspace.cwd } label: {
                Command.copyPath.label(Theme.Icon.copy)
            }
            .disabled(workspace.cwd.isEmpty)
            .accessibilityIdentifier("worktree.copyPath")
            Button { UIPasteboard.general.string = workspace.title } label: {
                Command.copyName.label(Theme.Icon.copy)
            }
            .accessibilityIdentifier("worktree.copyName")
            if let parent {
                Button { open(parent) } label: {
                    Command.openParent.label(Theme.Icon.parentFolder)
                }
                .accessibilityIdentifier("worktree.openParent")
            }
        }
        if let agent {
            Section { agentMenu(agent) }
        } else if !sessions.isEmpty {
            Section {
                ForEach(sessions) { session in
                    Menu { agentMenu(session) } label: {
                        Label { Text(session.title).lineLimit(1) } icon: {
                            AgentMark(session)
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder private func agentMenu(_ session: Session) -> some View {
        #if os(iOS)
        AgentActionMenuReader(session: session, fromWorktreeSubmenu: agent == nil, handlers: handlers)
        #else
        if let menu = menus.first(where: { $0.key == session.key }) { menu.equatable() }
        #endif
    }

}
