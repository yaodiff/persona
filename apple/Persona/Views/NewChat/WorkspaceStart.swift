import Foundation

/// New Agent's workspaces (#7259): the selected computer's catalog workspaces an Agent can start in, Paseo's main
/// checkouts, worktrees and folders included. The same rule as the web's `lib/workspace-start.ts` and Android's
/// `NewAgentWorkspaces`: only engines that keep checkouts (Orca, Paseo), only rows the computer can use now, grouped by
/// project, main checkout first, then worktrees, then folders.
nonisolated enum WorkspaceStart {
    /// The engines whose workspaces are checkouts and folders a start can name (`workspace_id` + `host`). A terminal
    /// host's space (herdr) is joined from its own menu, not chosen here.
    static let sources: Set<String> = ["orca", "paseo"]

    nonisolated enum Kind: Int, Comparable, Sendable {
        case main, worktree, folder
        static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }
    }

    /// One project's workspaces; `title` "" is the last group, the workspaces in no project.
    nonisolated struct Group: Identifiable, Hashable, Sendable {
        var id: String
        var title: String
        var workspaces: [HostWorkspace]
    }

    static func kind(_ w: HostWorkspace) -> Kind {
        if w.workspaceKind == "folder" { return .folder }
        return w.isMain == true ? .main : .worktree
    }

    /// What the row is called: the workspace's title, else its branch, else its folder's name.
    static func title(_ w: HostWorkspace) -> String {
        for name in [w.title, w.branch] where !name.trimmingCharacters(in: .whitespaces).isEmpty { return name }
        return lastElement(w.cwd)
    }

    /// The branch said after the kind word: a Git workspace's, when the title does not already say it.
    static func branch(_ w: HostWorkspace) -> String? {
        guard kind(w) != .folder, !w.branch.isEmpty, w.branch != title(w) else { return nil }
        return w.branch
    }

    /// The workspaces offered for `machine`: its own, of the engines above, that it can start in now.
    static func choices(_ catalog: [HostWorkspace], machine: MachineRef) -> [HostWorkspace] {
        catalog.filter { $0.machineRef == machine && sources.contains($0.source) && $0.available && $0.online }
    }

    static func groups(_ choices: [HostWorkspace]) -> [Group] {
        var byKey: [String: Group] = [:]
        for w in choices {
            let heading = project(w)
            let key = heading.isEmpty ? "" : w.source + "\u{1F}" + (w.projectID.flatMap { $0.isEmpty ? nil : $0 } ?? heading)
            byKey[key, default: Group(id: key, title: heading, workspaces: [])].workspaces.append(w)
        }
        return byKey.values.map { g in
            var g = g
            g.workspaces.sort { a, b in
                let ka = kind(a), kb = kind(b)
                if ka != kb { return ka < kb }
                let order = title(a).localizedStandardCompare(title(b))
                return order == .orderedSame ? a.key < b.key : order == .orderedAscending
            }
            return g
        }
        .sorted { a, b in
            if a.title.isEmpty != b.title.isEmpty { return b.title.isEmpty }
            let order = a.title.localizedStandardCompare(b.title)
            return order == .orderedSame ? a.id < b.id : order == .orderedAscending
        }
    }

    /// The project's name, else its main checkout's folder name; "" in none.
    static func project(_ w: HostWorkspace) -> String {
        if let name = w.projectName?.trimmingCharacters(in: .whitespaces), !name.isEmpty { return name }
        if let path = w.projectPath, !path.isEmpty { return lastElement(path) }
        return ""
    }

    private static func lastElement(_ path: String) -> String {
        var p = path
        while p.count > 1 && (p.hasSuffix("/") || p.hasSuffix("\\")) { p.removeLast() }
        return p.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? p
    }
}
