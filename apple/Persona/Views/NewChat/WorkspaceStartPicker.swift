import SwiftUI

extension WorkspaceStart {
    /// The groups with only the workspaces whose name, project, branch or folder holds `query`.
    nonisolated static func filter(_ groups: [Group], query: String) -> [Group] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return groups }
        return groups.compactMap { g in
            let rows = g.workspaces.filter { w in
                [title(w), g.title, w.branch, w.cwd].contains { $0.localizedStandardContains(q) }
            }
            return rows.isEmpty ? nil : Group(id: g.id, title: g.title, workspaces: rows)
        }
    }
}

/// A workspace's kind in words, the same three on every client (#7259).
extension WorkspaceStart.Kind {
    var label: String {
        switch self {
        case .main: String(localized: "workspace.kind.main", defaultValue: "Main checkout")
        case .worktree: String(localized: "workspace.kind.worktree", defaultValue: "Worktree")
        case .folder: String(localized: "workspace.kind.folder", defaultValue: "Folder")
        }
    }
}

/// One workspace in New Agent's list: its name, its kind and branch, its folder.
struct WorkspaceStartRow: View {
    var workspace: HostWorkspace
    var home: String?

    /// "Worktree · fix-login": the kind word, with the branch when the name does not already say it.
    static func kindLine(_ w: HostWorkspace) -> String {
        let kind = WorkspaceStart.kind(w).label
        return WorkspaceStart.branch(w).map { kind + " · " + $0 } ?? kind
    }

    /// Under New Agent's Workspace row once one is chosen: what it is and where (the folder rows are hidden then).
    static func detail(_ w: HostWorkspace, home: String?) -> String {
        kindLine(w) + "\n" + Fmt.path(w.cwd, home: home)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(WorkspaceStart.title(workspace)).foregroundStyle(Color.primary).lineLimit(1)
            Text(Self.kindLine(workspace))
                .font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
            Text(Fmt.path(workspace.cwd, home: home))
                .font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1).truncationMode(.head)
        }
    }
}

/// New Agent's workspaces (#7259), by project, with Choose by Folder on top to go back to typing or browsing one.
struct WorkspaceStartPicker: View {
    var groups: [WorkspaceStart.Group]
    var selectedKey: String?
    var home: String?
    var onPick: (HostWorkspace?) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var shown: [WorkspaceStart.Group]

    init(groups: [WorkspaceStart.Group], selectedKey: String?, home: String?, onPick: @escaping (HostWorkspace?) -> Void) {
        self.groups = groups
        self.selectedKey = selectedKey
        self.home = home
        self.onPick = onPick
        _shown = State(initialValue: groups)
    }

    var body: some View {
        List {
            Section {
                Button { onPick(nil); dismiss() } label: {
                    HStack(spacing: Theme.Space.m) {
                        Label(String(localized: "workspace.useDirectory", defaultValue: "Choose by Folder"), systemImage: Theme.Icon.folder)
                        Spacer(minLength: Theme.Space.xs)
                        if selectedKey == nil { ThemedIcon(Theme.Icon.success).foregroundStyle(Theme.accent) }
                    }
                }
                .accessibilityIdentifier("workspace.useDirectory")
            }
            ForEach(shown) { group in
                Section {
                    ForEach(group.workspaces) { workspace in
                        Button { onPick(workspace); dismiss() } label: {
                            HStack(spacing: Theme.Space.m) {
                                WorkspaceStartRow(workspace: workspace, home: home)
                                Spacer(minLength: Theme.Space.xs)
                                if selectedKey == workspace.key { ThemedIcon(Theme.Icon.success).foregroundStyle(Theme.accent) }
                            }
                        }
                        .accessibilityIdentifier("workspace.choice")
                    }
                } header: {
                    Text(group.title.isEmpty ? String(localized: "workspace.otherGroup", defaultValue: "Other Workspaces") : group.title)
                }
            }
        }
        .themedGroupedPage()
        .navigationTitle(String(localized: "workspace.chooseStart", defaultValue: "Choose Workspace"))
        .pageSearchable(text: $query, prompt: String(localized: "Search"))
        .onChange(of: query) { _, q in shown = WorkspaceStart.filter(groups, query: q) }
        .onChange(of: groups) { _, now in shown = WorkspaceStart.filter(now, query: query) }
    }
}
