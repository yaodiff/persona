import SwiftUI

/// The branches to switch to (#7015, the web's `BranchesView`): a search field that also names a new branch, the local
/// branches, then remote ones without a local one, newest first. A tap switches: at once in a clean folder, after a
/// question when uncommitted changes would go along. Nothing is forced: files the other branch has differently are
/// refused and listed, with a way to Stashes.
struct GitBranchesSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model
    let machineID: String
    let cwd: String
    let status: GitStatus
    @Bindable var writer: GitWriteModel
    let deps: GitWriteModel.Deps
    /// Hands a write's answer to the tab (nil: refused); true when it went through.
    let applied: (GitWriteResult?) -> Bool
    /// Opens Stashes in this sheet's place.
    let openStashes: () -> Void

    @State private var branches: [GitBranch]?
    @State private var failure: String?
    @State private var query = ""
    @State private var asking: Ask?

    struct Ask: Identifiable, Equatable {
        var branch: String
        var create: Bool
        var id: String { (create ? "+" : "") + branch }
    }

    private var name: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }
    /// Under 300 names (the node's cap): filtered as typed.
    private var shown: [GitBranch] {
        name.isEmpty ? branches ?? [] : (branches ?? []).filter { $0.name.localizedCaseInsensitiveContains(name) }
    }
    /// A name git takes that isn't a local branch already.
    private var canCreate: Bool {
        guard let branches, !name.isEmpty, WorktreeForm.branchProblem(name) == nil else { return false }
        return !branches.contains { $0.remote != true && $0.name == name }
    }
    private var switching: Bool { if case .switchTo = writer.busy { true } else { false } }

    var body: some View {
        NavigationStack {
            List {
                if canCreate {
                    Section {
                        Button { pick(name, create: true) } label: {
                            HStack(spacing: Theme.Space.m) {
                                ThemedIcon(Theme.Icon.add).foregroundStyle(Theme.accent)
                                Text("Create Branch “\(name)” from Here").font(Theme.Font.bodyStrong).foregroundStyle(Theme.accent).lineLimit(2)
                                Spacer(minLength: Theme.Space.s)
                                if writer.busy == .switchTo(name) { ProgressView() }
                            }
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .disabled(writer.busy != nil)
                        .accessibilityIdentifier("git.branch.create")
                    }
                }
                list
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always), prompt: Text("Find or name a branch"))
            .autocorrectionDisabled()
            .textInputAutocapitalization(.never)
            .navigationTitle(Text("Branches"))
            .navigationSubtitle(status.branch ?? String(localized: "Detached HEAD"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) }
                        .disabled(switching)
                }
            }
            .confirmationDialog(Text(asking.map { $0.create ? String(localized: "Create \($0.branch)?") : String(localized: "Switch to \($0.branch)?") } ?? ""),
                                isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }),
                                titleVisibility: .visible, presenting: asking) { a in
                Button { Task { await go(a.branch, create: a.create) } } label: { Text(a.create ? "Create" : "Switch").lineLimit(1) }
                    .accessibilityIdentifier("git.branch.confirm")
            } message: { _ in
                Text("\(status.files.count) uncommitted changes go along. If the other branch has any of those files differently, nothing is switched and they are listed.")
            }
            .gitRefusalAlert($writer.refusal, stashes: openStashes)
            .task { await load() }
        }
        .interactiveDismissDisabled(switching)
    }

    @ViewBuilder private var list: some View {
        if let branches {
            Section {
                ForEach(shown) { row($0) }
                if shown.isEmpty && !canCreate {
                    Text("No branch matches").foregroundStyle(.secondary)
                }
            } footer: {
                if shown.contains(where: { $0.remote == true }) {
                    Text("Tapping a remote branch makes a local branch here that tracks it.")
                }
            }
        } else if let failure {
            ContentUnavailableView {
                Label("Couldn't Read the Branches", systemImage: Theme.Icon.error)
            } description: {
                Text(failure)
            } actions: {
                Button { Task { await load() } } label: { Text("Retry [Components b8784c]").lineLimit(1) }.themedButton()
            }
            .listRowBackground(Color.clear)
        } else {
            HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
        }
    }

    private func row(_ b: GitBranch) -> some View {
        let current = b.current == true
        return Button { pick(b.switchName, create: false) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(Theme.Icon.gitBranch).foregroundStyle(current ? Theme.accent : Color.secondary)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(b.name).font(Theme.Font.secondaryMedium).foregroundStyle(current ? Theme.accent : Color.primary)
                        .lineLimit(1).truncationMode(.middle)
                    Text(b.date.map { "\(b.subject) · \(Fmt.relative($0))" } ?? b.subject)
                        .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                if writer.busy == .switchTo(b.switchName) {
                    ProgressView()
                } else if current {
                    Label("Current", systemImage: Theme.Icon.done).font(Theme.Font.label).foregroundStyle(Theme.accent).lineLimit(1)
                } else if b.remote == true {
                    Chip(text: String(localized: "Remote"))
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(current || writer.busy != nil)
        .accessibilityIdentifier("git.branch")
    }

    /// With uncommitted changes a switch asks first (they go along); in a clean folder it switches at once.
    private func pick(_ branch: String, create: Bool) {
        if status.files.isEmpty { Task { await go(branch, create: create) } } else { asking = Ask(branch: branch, create: create) }
    }

    private func go(_ branch: String, create: Bool) async {
        asking = nil
        let r = await writer.switchTo(branch, create: create, machine: machineID, cwd: cwd, deps: deps)
        guard applied(r) else { return }
        Haptics.success()
        model.toast = Toast(create ? String(localized: "Created branch \(branch)") : String(localized: "On \(branch)"), style: .success)
        dismiss()
    }

    private func load() async {
        failure = nil
        do { branches = try await deps.branches(machineID, cwd).branches } catch {
            if !error.isCancellation { failure = GitRefusal(error).detail }
        }
    }
}

/// Stashes (#7015, the web's `StashesView`): put the uncommitted changes away (new files too, by default), and bring one
/// back (Apply keeps it, Pop removes it) or drop it, which asks first. Each names its stash by index and oid; a stack
/// that moved is refused and the list read again.
struct GitStashesSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model
    let machineID: String
    let cwd: String
    let status: GitStatus
    @Bindable var writer: GitWriteModel
    let deps: GitWriteModel.Deps
    let applied: (GitWriteResult?) -> Bool

    @State private var list: [GitStashEntry]?
    @State private var message = ""
    @State private var untracked = true
    @State private var dropping: GitStashEntry?
    @FocusState private var typing: Bool

    private var stashing: Bool { if case .stash = writer.busy { true } else { false } }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField("What the changes are (optional)", text: $message)
                        .focused($typing)
                        .submitLabel(.done)
                        .accessibilityIdentifier("git.stash.message")
                    Toggle("Include New Files", isOn: $untracked)
                    Button { Task { await act(.push(message: message, untracked: untracked)) } } label: {
                        HStack(spacing: Theme.Space.s) {
                            if writer.busy == .stash(.push(message: message, untracked: untracked)) { ProgressView() }
                            Text("Stash Changes").lineLimit(1)
                        }
                        .frame(maxWidth: .infinity)
                    }
                    .themedButton(prominent: true)
                    .disabled(status.files.isEmpty || writer.busy != nil)
                    .accessibilityIdentifier("git.stash.push")
                } footer: {
                    Text("Puts your uncommitted changes away so the folder is clean; bring them back from the list below.")
                }
                Section {
                    stashes
                } header: {
                    Text("Stashed")
                } footer: {
                    if list?.contains(where: { !$0.hasIdentity }) == true {
                        Text("Update Persona on this computer to apply, pop or drop stashes safely.")
                    }
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .refreshable { await refresh() }
            .navigationTitle(Text("Stashes"))
            .navigationSubtitle(status.branch ?? String(localized: "Detached HEAD"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) }
                        .disabled(stashing)
                }
            }
            .confirmationDialog(Text("Drop This Stash?"), isPresented: Binding(get: { dropping != nil }, set: { if !$0 { dropping = nil } }),
                                titleVisibility: .visible, presenting: dropping) { e in
                Button(role: .destructive) { Task { await act(.drop(e)) } } label: { Text("Drop").lineLimit(1) }
                    .accessibilityIdentifier("git.stash.dropConfirm")
            } message: { e in
                Text("“\(e.message)” is deleted and can't be brought back from here.")
            }
            .gitRefusalAlert($writer.refusal)
            .task { await refresh() }
        }
        .interactiveDismissDisabled(stashing)
    }

    @ViewBuilder private var stashes: some View {
        if let list {
            if list.isEmpty {
                Text("Nothing stashed").foregroundStyle(.secondary)
            }
            ForEach(list) { row($0) }
        } else {
            HStack { Spacer(); ProgressView(); Spacer() }
        }
    }

    /// One stash: a tap opens what can be done with it.
    private func row(_ e: GitStashEntry) -> some View {
        let busy = writer.busy.map { if case .stash(let op) = $0 { op.entry == e } else { false } } ?? false
        return Menu {
            Button { Task { await act(.apply(e)) } } label: {
                Text("Apply")
                Text("Bring the changes back and keep this stash")
            }
            Button { Task { await act(.pop(e)) } } label: {
                Text("Pop")
                Text("Bring the changes back and remove this stash")
            }
            Divider()
            Button(role: .destructive) { dropping = e } label: { Label("Drop…", systemImage: Theme.Icon.delete) }
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(e.message).font(Theme.Font.secondaryMedium).foregroundStyle(Color.primary).lineLimit(2)
                    if let d = e.date { Text(Fmt.relative(d)).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1) }
                }
                Spacer(minLength: Theme.Space.s)
                if busy { ProgressView() } else { ThemedIcon(Theme.Icon.more).foregroundStyle(Theme.accent) }
            }
            .contentShape(.rect)
        }
        .disabled(!e.hasIdentity || writer.busy != nil)
        .accessibilityIdentifier("git.stash")
    }

    private func act(_ op: GitStashOp) async {
        dropping = nil
        let r = await writer.stash(op, machine: machineID, cwd: cwd, deps: deps)
        if applied(r), let r {
            list = r.stashes ?? []
            Haptics.success()
            switch op {
            case .push:
                message = ""
                typing = false
                model.toast = Toast(String(localized: "Changes stashed"), style: .success)
            case .apply: model.toast = Toast(String(localized: "Stash applied (it is kept)"), style: .success)
            case .pop: model.toast = Toast(String(localized: "Stash popped"), style: .success)
            case .drop: model.toast = Toast(String(localized: "Stash dropped"), style: .success)
            case .list: break
            }
        } else if op.entry != nil {
            // the stack may have moved: read it again, and the user picks again
            await refresh()
        }
    }

    private func refresh() async {
        guard let r = await writer.stash(.list, machine: machineID, cwd: cwd, deps: deps) else { return }
        list = r.stashes ?? []
    }
}
