import PersonaKit
import SwiftUI

/// The Git tab of a session: branch and sync state, working-tree changes with their diffs, and the history.
struct GitPane: View {
    @Environment(AppModel.self) private var model
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    @Binding var status: GitStatus?
    /// Opens the review of the session's changes at a scope (#151); nil where there is none (no `git_review`, the Mac).
    var review: ((GitReviewScope?) -> Void)? = nil
    /// Where a pull request's review thread goes as a comment for the agent (#7014); nil when the chat takes no messages.
    var commentKey: SessionKey? = nil
    @State private var error: String?
    /// A refresh the user asked for failed over a status already shown (#1889): said above it, which stays.
    @State private var refreshError: String?
    @State private var older: [GitCommit] = []
    @State private var noMore = false
    @State private var loadingMore = false
    /// The last Load Earlier Commits failed: its button says so and tries again.
    @State private var moreFailed = false
    @State private var diff: GitDiffTarget?
    @State private var commit: GitCommit?
    @State private var pullModel = GitPullModel()
    @State private var revision = 0
    /// The tab's writes (#7013): staging, the commit, the push.
    @State private var writer = GitWriteModel()
    /// The sheet over the tab: the commit, Branches or Stashes (#7015).
    @State private var sheet: WriteSheet?
    /// The commit message, kept while the sheet is closed or the commit refused.
    @State private var commitMessage = ""
    /// A push waiting for its yes.
    @State private var askingPush: GitPushKind?
    /// The branch's pull request (#7014).
    @State private var prModel = GitPRModel()

    /// The hub writes for this tab (`git_write`, #7013): iPhone and iPad only, the Mac app is frozen.
    private var canWrite: Bool {
        #if os(iOS)
        model.features(group).contains("git_write")
        #else
        false
        #endif
    }

    private var writeDeps: GitWriteModel.Deps? { model.api(group).map { GitWriteModel.Deps(api: $0) } }

    var body: some View {
        writeSheets(page)
    }

    private var page: some View {
        List {
            if let refreshError, status != nil {
                Section {
                    NoticeBanner(text: String(localized: "Couldn't refresh, so this is the status from the last read: \(refreshError)"), tone: .danger) {
                        Button("Retry") { Task { await load() } }
                    }
                    .listRowInsets(EdgeInsets())
                    .listRowBackground(Color.clear)
                    .accessibilityIdentifier("git-refresh-failed")
                }
            }
            if let s = status, s.isRepo {
                Section {
                    header(s)
                    if canWrite { sheetRows(s) }
                }
                if canWrite {
                    GitPRSection(status: s, pr: prModel, open: { sheet = .pr }, create: { sheet = .prCreate },
                                 retry: { Task { await readPR(force: true) } })
                }
                changes(s)
                history(s)
            } else if let error {
                ContentUnavailableView("Can't Read Git Status", systemImage: Theme.Icon.error, description: Text(error))
                    .listRowBackground(Color.clear)
            } else if status?.isRepo == false {
                ContentUnavailableView("Not a Git Repository", systemImage: Theme.Icon.gitBranch, description: Text(cwd))
                    .listRowBackground(Color.clear)
            } else {
                HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
        .safeAreaInset(edge: .bottom) { commitBar }
        .refreshable {
            await load()
            await readPR(force: true)
        }
        #if os(macOS)
        .pageToolbar { MacRefreshItem(id: [machineID, cwd]) { await load() } } // no pull to refresh on the Mac
        #endif
        .task(id: cwd) { await load() }
        // The pull request again only when what was pushed moved (gh asks GitHub each time), as on the web.
        .task(id: canWrite ? GitPRModel.key(status) : "") { await readPR() }
        #if DEBUG
        // `-showGitReload YES`: a refresh and a Load Earlier Commits two seconds after the first read, as the user's
        // would be, to see what their failures say (#1889, with scripts/mock/mock_hub.py MOCK_GIT_READ_FAIL=1).
        .task(id: cwd) {
            guard UserDefaults.standard.bool(forKey: "showGitReload") else { return }
            try? await Task.sleep(for: .seconds(2))
            await load()
            if let s = status, s.commits.count >= 30 { await loadMore(after: s.commits.count + older.count) }
        }
        #endif
        // Live refresh (#3089): the status again when the hub says it moved, in place (the commits loaded below stay).
        .fsLive(group: group, machineID: machineID, cwd: cwd, paths: [], git: true) { change in
            if change.git { Task { await load(live: true) } }
        }
        .sheet(item: $diff) { t in GitDiffSheet(machineID: machineID, group: group, cwd: cwd, root: status?.root, target: t) }
        .sheet(item: $commit) { c in GitCommitSheet(machineID: machineID, group: group, cwd: cwd, commit: c) }
        #if os(macOS) && DEBUG
        .onAppear {
            // `-macGitSheet diff|commit`: a made-up change / commit's sheet, to see its size on the Mac (demo mode has
            // no repositories, so it shows its error state). The change and the commit are the ones scripts/mock/mock_hub.py
            // (MOCK_FILES=1) answers for, so paired with it the sheets show a real diff.
            switch UserDefaults.standard.string(forKey: "macGitSheet") {
            case "diff":
                diff = GitDiffTarget(file: GitFile(path: "Sources/App/main.swift", index: "M", worktree: "M", added: 2, deleted: 1), staged: false)
            case "commit":
                commit = GitCommit(hash: String(repeating: "0", count: 32) + "a1b2c3d0", short: "a1b2c3d", subject: "Show the city name",
                                   author: "Sample Dev")
            default: break
            }
        }
        #endif
    }

    private func header(_ s: GitStatus) -> some View {
        let push = canWrite ? GitPushKind(s) : nil
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.gitBranch).foregroundStyle(Theme.accent)
                Text(s.branch ?? String(localized: "Detached HEAD")).font(Theme.Font.heading).lineLimit(1)
                Spacer(minLength: Theme.Space.xs)
                // With Push (#7013) the commits to send are its count.
                if s.ahead > 0 && push == nil { sync(Theme.Icon.send, s.ahead, .secondary) }
                if s.upstreamName != nil && s.gone != true { pullButton(s) } // nothing to pull from a deleted upstream
                if let push { pushButton(push) }
            }
            HStack(spacing: Theme.Space.sm) {
                if let up = s.upstreamName {
                    Label(up, systemImage: Theme.Icon.gitUpstream).lineLimit(1)
                    if s.gone == true {
                        Text("· Deleted on the remote")
                    } else if s.ahead == 0 && s.behind == 0 {
                        Text("· Up to date")
                    }
                } else {
                    Label("No upstream branch", systemImage: Theme.Icon.gitNoUpstream).lineLimit(1)
                }
                if s.stashes > 0 { Text("· \(s.stashes) stashes") }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            if let root = s.root {
                Text(Fmt.path(root, home: nil)).font(Theme.Font.label.monospaced()).foregroundStyle(.tertiary).lineLimit(1).truncationMode(.head)
            }
            if let message = pullModel.message {
                Label(message, systemImage: pullModel.failed ? Theme.Icon.error : Theme.Icon.success)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(pullModel.failed ? Theme.danger : Theme.positive)
                    .accessibilityIdentifier("git-pull-result")
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func pullButton(_ s: GitStatus) -> some View {
        Button {
            Task { await pull() }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if pullModel.isPulling { ProgressView().controlSize(.mini) }
                Chip(text: pullModel.isPulling ? String(localized: "Pulling") : s.behind > 0 ? String(localized: "Pull \(s.behind)") : String(localized: "git.pull", defaultValue: "Pull"),
                     symbol: pullModel.isPulling ? nil : Theme.Icon.down,
                     look: .tint(s.behind > 0 ? Theme.cautionText : Theme.accent))
                    .monospacedDigit()
                    .lineLimit(1)
            }
            .padding(.vertical, Theme.Space.s)
            .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .disabled(pullModel.isPulling || loadingMore || s.branch == nil || writer.busy != nil)
        .accessibilityIdentifier("git-pull")
        .accessibilityLabel("Pull Latest Code")
        .accessibilityValue(pullModel.isPulling ? String(localized: "Pulling") : String(localized: "\(s.behind) commits behind"))
        .help("Safely pulls the latest upstream code without merging or overwriting local changes")
    }

    /// Push beside Pull (#7013): the commits to send, or a branch the remote doesn't have (any more). It asks first.
    private func pushButton(_ kind: GitPushKind) -> some View {
        let pushing = writer.busy == .push
        let count = if case .commits(let n, _) = kind { n } else { 0 }
        return Button {
            askingPush = kind
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if pushing { ProgressView().controlSize(.mini) }
                Chip(text: pushing ? String(localized: "Pushing") : count > 0 ? String(localized: "Push \(count)") : String(localized: "Push"),
                     symbol: pushing ? nil : Theme.Icon.gitPush, look: .tint(Theme.accent))
                    .monospacedDigit()
                    .lineLimit(1)
            }
            .padding(.vertical, Theme.Space.s)
            .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .disabled(writer.busy != nil || pullModel.isPulling)
        .accessibilityIdentifier("git-push")
        .accessibilityLabel(Text("Push"))
        .accessibilityValue(pushing ? String(localized: "Pushing") : count > 0 ? String(localized: "\(count) commits to push") : "")
    }

    private func sync(_ symbol: String, _ n: Int, _ color: Color) -> some View {
        Chip(text: "\(n)", symbol: symbol, look: color == .secondary ? .neutral : .tint(color)).monospacedDigit()
    }

    @ViewBuilder private func changes(_ s: GitStatus) -> some View {
        Section {
            if s.files.isEmpty {
                Label("Working tree is clean", systemImage: Theme.Icon.success).foregroundStyle(.secondary)
            }
            ForEach(s.files) { f in
                changeRow(f)
            }
        } header: {
            if !s.files.isEmpty {
                let added = s.files.reduce(0) { $0 + $1.added }, deleted = s.files.reduce(0) { $0 + $1.deleted }
                HStack {
                    Text("Changes \(s.files.count)")
                    Spacer()
                    Text("+\(added)").foregroundStyle(Theme.Diff.add)
                    Text("−\(deleted)").foregroundStyle(Theme.Diff.del)
                    if let review {
                        // Review from here starts at the uncommitted changes, the ones this list shows.
                        Button { review(.uncommitted) } label: { Label("Review", systemImage: Theme.Icon.reviewChanges).lineLimit(1) }
                            .themedButton()
                            .textCase(nil)
                            .accessibilityIdentifier("git.review")
                    }
                }
                .monospacedDigit()
            }
        }
    }

    @ViewBuilder private func history(_ s: GitStatus) -> some View {
        Section(String(localized: "git.commits", defaultValue: "Commits")) {
            ForEach(s.commits + older) { c in
                Button { commit = c } label: { GitCommitRow(commit: c, isHead: c.hash == s.head?.hash) }
                    .buttonStyle(.plain)
            }
            if !noMore && s.commits.count >= 30 {
                Button {
                    Task { await loadMore(after: s.commits.count + older.count) }
                } label: {
                    HStack(spacing: Theme.Space.sm) {
                        if loadingMore { ProgressView().controlSize(.mini) }
                        Text(moreFailed && !loadingMore ? String(localized: "Couldn't load. Try again.") : String(localized: "Load Earlier Commits"))
                            .lineLimit(1)
                            .minimumScaleFactor(0.8)
                            .foregroundStyle(moreFailed && !loadingMore ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.tint))
                    }
                    .frame(maxWidth: .infinity)
                }
                .disabled(loadingMore)
                .accessibilityIdentifier("git-load-more")
            }
        }
    }

    /// A changed file: its diff on a tap; with the hub's writes (#7013), its tick box in front (stage / unstage at once),
    /// and the same as a swipe from the leading edge and in the long-press menu.
    @ViewBuilder private func changeRow(_ f: GitFile) -> some View {
        let open = Button { diff = GitDiffTarget(file: f, staged: f.isStaged && !f.hasUnstaged) } label: { GitFileRow(file: f) }
            .buttonStyle(.plain)
        if canWrite {
            let mark = GitStageMark(f)
            HStack(spacing: Theme.Space.xs) {
                TickBox(mark: mark == .all ? .all : mark == .some ? .some : .none,
                        label: mark == .all ? String(localized: "Unstage \(f.name)") : String(localized: "Stage \(f.name)"),
                        value: mark == .all ? String(localized: "Staged") : mark == .some ? String(localized: "Partly staged") : String(localized: "Not staged"),
                        busy: writer.busy == .stage(f.path)) { Task { await toggle(f) } }
                    .disabled(writer.busy != nil)
                    .accessibilityIdentifier("git.stage")
                open
            }
            .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.xs, bottom: Theme.Space.xs, trailing: Theme.Space.l))
            .swipeActions(edge: .leading) {
                Button { Task { await toggle(f) } } label: { stageLabel(mark) }
                    .tint(Theme.accent)
                    .disabled(writer.busy != nil)
            }
            .contextMenu {
                Button { Task { await toggle(f) } } label: { stageLabel(mark) }.disabled(writer.busy != nil)
                Button { UIPasteboard.general.string = f.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
            }
        } else {
            open.contextMenu {
                Button { UIPasteboard.general.string = f.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
            }
        }
    }

    private func stageLabel(_ mark: GitStageMark) -> some View {
        mark == .all ? Label("Unstage", systemImage: Theme.Icon.uncheckedBox) : Label("Stage", systemImage: Theme.Icon.checkedBox)
    }

    /// The bar over the bottom while there are changes (#7013): every change's tick box, how many are staged, and
    /// Commit…, which opens the commit sheet.
    @ViewBuilder private var commitBar: some View {
        if canWrite, let s = status, s.isRepo, !s.files.isEmpty {
            let all = GitStageMark(all: s.files)
            let staged = s.files.count(where: { GitStageMark($0) != .none })
            HStack(spacing: Theme.Space.s) {
                TickBox(mark: all == .all ? .all : all == .some ? .some : .none,
                        label: all == .all ? String(localized: "Unstage All") : String(localized: "Stage All"),
                        value: String(localized: "\(staged) staged"),
                        busy: writer.busy == .stageAll) { Task { await toggleAll(s) } }
                    .disabled(writer.busy != nil)
                    .accessibilityIdentifier("git.stageAll")
                VStack(alignment: .leading, spacing: Theme.Space.flush) {
                    Text(s.branch == nil ? String(localized: "Detached HEAD: switch to a branch to commit")
                         : staged == 0 ? String(localized: "Tick the files to commit") : String(localized: "\(staged) staged"))
                        .font(Theme.Font.secondaryMedium)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                    Text("\(s.files.count) changes").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                .accessibilityElement(children: .combine)
                Spacer(minLength: 0)
                Button { sheet = .commit } label: { Text("Commit…").lineLimit(1) }
                    .themedButton(prominent: true)
                    .disabled(staged == 0 || s.branch == nil || writer.busy != nil)
                    .accessibilityIdentifier("git.commit")
            }
            .padding(.vertical, Theme.Space.xs)
            .padding(.trailing, Theme.Space.m)
            .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
            .padding(.horizontal, Theme.Space.page).padding(.bottom, Theme.Space.s)
            // The list runs on under the bar: back it with the page and fade the list out above it, or the rows and
            // commits scrolled under it read through its words.
            .background(alignment: .top) {
                LinearGradient(colors: [Theme.groupedPage.opacity(0), Theme.groupedPage], startPoint: .top, endPoint: .bottom)
                    .frame(height: Theme.Space.xl)
                    .offset(y: -Theme.Space.xl)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
            .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
        }
    }

    enum WriteSheet: String, Identifiable {
        case commit, branches, stashes, pr, prCreate
        var id: String { rawValue }
    }

    /// Branches and Stashes under the branch card (#7015): each opens its sheet; Stashes says how many there are.
    @ViewBuilder private func sheetRows(_ s: GitStatus) -> some View {
        sheetRow(String(localized: "Branches"), symbol: Theme.Icon.gitBranch, value: nil, open: .branches)
        sheetRow(String(localized: "Stashes"), symbol: Theme.Icon.gitStash, value: s.stashes > 0 ? "\(s.stashes)" : nil, open: .stashes)
    }

    private func sheetRow(_ title: String, symbol: String, value: String?, open: WriteSheet) -> some View {
        Button { sheet = open } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(symbol).foregroundStyle(Theme.accent)
                Text(title).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if let value { Text(value).foregroundStyle(.secondary).monospacedDigit() }
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(writer.busy != nil)
        .accessibilityIdentifier("git.open." + open.rawValue)
    }

    /// The commit, Branches and Stashes sheets, Push's question and a refusal's alert (#7013, #7015), on the tab.
    private func writeSheets(_ content: some View) -> some View {
        content
            .sheet(item: $sheet) { which in
                if let s = status, let deps = writeDeps {
                    switch which {
                    case .commit:
                        GitNewCommitSheet(status: s, message: $commitMessage, writer: writer) { await commit() }
                    case .branches:
                        GitBranchesSheet(machineID: machineID, cwd: cwd, status: s, writer: writer, deps: deps,
                                         applied: { applied($0) }, openStashes: { sheet = .stashes })
                    case .stashes:
                        GitStashesSheet(machineID: machineID, cwd: cwd, status: s, writer: writer, deps: deps, applied: { applied($0) })
                    case .pr:
                        if let api = model.api(group) {
                            GitPRSheet(machineID: machineID, cwd: cwd, status: s, pr: prModel, deps: GitPRModel.Deps(api: api), commentKey: commentKey)
                        }
                    case .prCreate:
                        if let api = model.api(group) {
                            GitPRCreateSheet(machineID: machineID, cwd: cwd, status: s, pr: prModel, deps: GitPRModel.Deps(api: api),
                                             branches: { try await deps.branches(machineID, cwd) }, opened: { sheet = .pr })
                        }
                    }
                }
            }
            .confirmationDialog(Text("Push to the Remote?"), isPresented: Binding(get: { askingPush != nil }, set: { if !$0 { askingPush = nil } }),
                                titleVisibility: .visible, presenting: askingPush) { kind in
                Button { Task { await push(kind) } } label: { Text("Push").lineLimit(1) }
                    .accessibilityIdentifier("git.push.confirm")
            } message: { kind in
                Text(pushQuestion(kind))
            }
            .gitRefusalAlert(Binding(get: { sheet != nil ? nil : writer.refusal }, set: { writer.refusal = $0 }))
    }

    /// What Push says it does, as the web's question: never forced.
    private func pushQuestion(_ kind: GitPushKind) -> String {
        let branch = status?.branch ?? ""
        return switch kind {
        case .commits(let n, let up): String(localized: "\(n) commits go to \(up). Never forced: if the remote has newer commits, the push stops and says so.")
        case .fresh: String(localized: "\(branch) is not on the remote yet: it is created on origin under the same name and tracked from now on. Never forced: if the remote has newer commits, the push stops and says so.")
        case .gone(let up): String(localized: "\(up) was deleted on the remote: pushing creates it again from \(branch). Never forced: if the remote has newer commits, the push stops and says so.")
        }
    }

    private func toggle(_ f: GitFile) async {
        guard let deps = writeDeps else { return }
        Haptics.selection()
        revision += 1
        applied(await writer.toggle(f, machine: machineID, cwd: cwd, deps: deps))
    }

    private func toggleAll(_ s: GitStatus) async {
        guard let deps = writeDeps else { return }
        Haptics.selection()
        revision += 1
        applied(await writer.toggleAll(s, machine: machineID, cwd: cwd, deps: deps))
    }

    private func commit() async {
        guard let deps = writeDeps else { return }
        revision += 1
        let r = await writer.commit(message: commitMessage, machine: machineID, cwd: cwd, deps: deps)
        applied(r)
        if let c = r?.commit {
            commitMessage = ""
            sheet = nil
            Haptics.success()
            model.toast = Toast(String(localized: "Committed \(c.short)"), style: .success)
        }
    }

    private func push(_ kind: GitPushKind) async {
        guard let deps = writeDeps else { return }
        askingPush = nil
        revision += 1
        let branch = status?.branch ?? ""
        if applied(await writer.push(kind, machine: machineID, cwd: cwd, deps: deps)) {
            Haptics.success()
            model.toast = Toast(String(localized: "Pushed \(branch)"), style: .success)
        }
    }

    /// A write's answer is the repository after it: shown as is (the history again from the top when HEAD moved). A
    /// refusal buzzes and reads the status again, since it may have changed the tree.
    @discardableResult private func applied(_ r: GitWriteResult?) -> Bool {
        guard let r else {
            if writer.refusal != nil {
                Haptics.error()
                Task { await load() }
            }
            return false
        }
        if r.status.head?.hash != status?.head?.hash {
            older = []
            noMore = false
        }
        status = r.status
        error = nil
        return true
    }

    /// Reads the branch's pull request when what was pushed moved, or always with force.
    private func readPR(force: Bool = false) async {
        guard canWrite, let api = model.api(group) else { return }
        await prModel.read(status, machine: machineID, cwd: cwd, deps: GitPRModel.Deps(api: api), force: force)
    }

    private func pull() async {
        guard let api = model.api(group), !cwd.isEmpty, !pullModel.isPulling, writer.busy == nil else { return }
        revision += 1
        if let fresh = await pullModel.pull(machine: machineID, cwd: cwd, deps: .init(api: api)) {
            status = fresh
            older = []
            noMore = false
            error = nil
            Haptics.success()
        } else {
            await load()
        }
    }

    /// live: an automatic refresh (#3089): the older commits already loaded stay while the newest one is the same,
    /// and a failure leaves what is shown without a word. One the user asked for (pull to refresh, Refresh / ⌘R, Retry)
    /// says it failed (GitReadFailure).
    private func load(live: Bool = false) async {
        // A write running answers the status itself (#7013); a read taken in its middle would land after it.
        guard let api = model.api(group), !cwd.isEmpty, !pullModel.isPulling, writer.busy == nil else { return }
        let started = revision
        do {
            let fresh = try await api.gitStatus(machine: machineID, cwd: cwd)
            guard started == revision else { return }
            let sameHistory = live && fresh.commits.first?.hash == status?.commits.first?.hash
            if fresh != status { status = fresh }
            if !sameHistory {
                older = []
                noMore = false
                moreFailed = false
            }
            error = nil
            refreshError = nil
        } catch {
            switch GitReadFailure(error.shown, shown: status != nil, live: live) {
            case .page(let message): self.error = message
            case .refresh(let message): refreshError = message
            case .quiet: break
            }
        }
    }

    private func loadMore(after skip: Int) async {
        guard let api = model.api(group), !pullModel.isPulling else { return }
        loadingMore = true
        defer { loadingMore = false }
        do {
            let more = try await api.gitLog(machine: machineID, cwd: cwd, skip: skip)
            older += more
            noMore = more.count < 50
            moreFailed = false
        } catch {
            moreFailed = !error.isCancellation
        }
    }
}

/// What a failed read of the Git status says (#1889). Before anything is shown, the page's error; over a status already
/// shown, a read the user asked for says it failed above it and the status stays; the automatic live refresh stays
/// quiet, as do a cancellation, a timeout and no network (`shown` nil), which the app says elsewhere.
nonisolated enum GitReadFailure: Equatable {
    case page(String)
    case refresh(String)
    case quiet

    init(_ message: String?, shown: Bool, live: Bool) {
        guard let message, !live else { self = .quiet; return }
        self = shown ? .refresh(message) : .page(message)
    }
}

struct GitDiffTarget: Identifiable, Hashable {
    var file: GitFile
    var staged: Bool
    var id: String { file.path + (staged ? "#staged" : "") }
}

/// Status letter badge colored like the editors do.
struct GitBadge: View {
    var letter: String

    var body: some View {
        Text(letter)
            .font(Theme.Font.labelHeavy.monospaced()).dynamicTypeSize(...DynamicTypeSize.xLarge)
            .foregroundStyle(color)
            .scaledFrame(width: Theme.Size.iconXS, height: Theme.Size.iconXS)
            .background(color.opacity(0.14), in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
    }

    var color: Color {
        switch letter {
        case "A", "U": Theme.Diff.add
        case "D", "!": Theme.Diff.del
        case "R", "C": .secondary
        default: Theme.Diff.changed
        }
    }
}

struct GitFileRow: View {
    var file: GitFile
    /// The working tree's changes say staged / partly staged; a commit's files have no stage (their `index` is only
    /// the change kind, for the badge).
    var showsStage = true

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            GitBadge(letter: file.letter)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(file.name).font(Theme.Font.secondaryMedium).lineLimit(1)
                HStack(spacing: Theme.Space.xs) {
                    if showsStage, file.isStaged { Text(file.hasUnstaged ? String(localized: "Partly staged") : String(localized: "Staged")).foregroundStyle(.primary) }
                    if let orig = file.origPath { Text("← " + (orig as NSString).lastPathComponent) }
                    if !file.folder.isEmpty { Text(file.folder).truncationMode(.head) }
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
            Spacer(minLength: 6)
            if file.binary == true {
                Text("Binary").font(Theme.Font.label).foregroundStyle(.secondary)
            } else if file.added + file.deleted > 0 {
                HStack(spacing: Theme.Space.xs) {
                    Text("+\(file.added)").foregroundStyle(Theme.Diff.add)
                    Text("−\(file.deleted)").foregroundStyle(Theme.Diff.del)
                }
                .font(Theme.Font.label.monospacedDigit())
            }
        }
        .contentShape(.rect)
    }
}

struct GitCommitRow: View {
    var commit: GitCommit
    var isHead = false

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(commit.subject).font(Theme.Font.secondaryMedium).lineLimit(2)
            HStack(spacing: Theme.Space.sm) {
                Text(commit.short).font(Theme.Font.code).foregroundStyle(Theme.accent)
                Text(commit.author).lineLimit(1)
                if let d = commit.date { Text("· " + Fmt.relative(d)).lineLimit(1) }
            }
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            let refs = commit.refNames
            if !refs.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: Theme.Space.xs) {
                        // A local branch outlined, a remote one (origin/…) plain: refs are facts, not buttons.
                        ForEach(refs, id: \.self) { r in Chip(text: r, look: r.contains("/") ? .neutral : .outline) }
                    }
                }
            }
        }
        .contentShape(.rect)
    }
}

/// The diff of one changed file; switches between the unstaged and staged halves when it has both.
struct GitDiffSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    /// The repository's root: the working tree's version of a file is read there (comparing, `CompareView`).
    var root: String? = nil
    @State var target: GitDiffTarget
    @State private var diff: GitDiff?
    @State private var error: String?

    /// A picture or a document the hub can give the versions of: compared instead of a text diff (docs/apple/ios.md「Compare」).
    private var compares: DocumentText.Kind? {
        guard model.features(group).contains("git_blob"), root != nil else { return nil }
        return DocumentText.kind(target.file.name)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if target.file.isStaged && target.file.hasUnstaged {
                    SegmentedChoice("", [(false, String(localized: "Unstaged")), (true, String(localized: "Staged"))], selection: $target.staged)
                    .padding(.horizontal, Theme.Space.l)
                    .padding(.vertical, Theme.Space.s)
                }
                if let kind = compares, let root {
                    CompareFileView(machine: MachineRef(group: group, id: machineID), cwd: cwd, root: root, file: target.file.path,
                                    versions: .change(staged: target.staged), kind: kind)
                        .id(target)
                } else if let diff {
                    DiffText(diff: diff.diff, truncated: diff.truncated)
                } else if let error {
                    ContentUnavailableView {
                        Label("Can't Read Changes", systemImage: Theme.Icon.error)
                    } description: {
                        Text(error)
                    } actions: {
                        Button("Retry [Components b8784c]") { Task { await load() } }.lineLimit(1).themedButton()
                    }
                } else {
                    ProgressView().frame(maxHeight: .infinity)
                }
            }
            .navigationTitle(target.file.name)
            .navigationSubtitle(target.file.folder.isEmpty ? String(localized: "Repository root") : target.file.folder)
            .navigationBarTitleDisplayMode(.inline)
            .pageToolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } }
                ToolbarItem(placement: .primaryAction) {
                    Menu {
                        Button { UIPasteboard.general.string = diff?.diff } label: { Label("Copy Diff", systemImage: Theme.Icon.copy) }
                        Button { UIPasteboard.general.string = target.file.path } label: { Label("Copy Path", systemImage: Theme.Icon.copy) }
                    } label: { ThemedIcon(Theme.Icon.more) }
                }
            }
            .task(id: target) { await load() }
            .onHubReconnect(group) { if error != nil { Task { await load() } } }
            #if os(macOS)
            .macLargeSheet(bars: true)
            #endif
        }
    }

    /// Reads the diff of the half shown. A sheet has nothing else that loads it again: a timeout, no network or no hub
    /// is said with Retry too, not left as a spinner (#4170); a cancelled load (the other half picked) says nothing.
    private func load() async {
        let t = target
        diff = nil
        error = nil
        guard let api = model.api(group) else { error = String(localized: "Not connected to the hub [App Model 93d955]"); return }
        do {
            let d = try await api.gitDiff(machine: machineID, cwd: cwd, path: t.file.path, staged: t.staged)
            if t == target { diff = d }
        } catch {
            if t == target, !error.isCancellation { self.error = error.shown ?? error.localizedDescription }
        }
    }
}

/// One commit: message, author, the files it touched, and each file's diff.
struct GitCommitSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let machineID: String
    /// The group of hubs the machine is in (docs/protocol/multi-hub.md).
    var group: String = ""
    let cwd: String
    let commit: GitCommit
    @State private var show: GitShow?
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        Text(commit.subject).font(Theme.Font.heading)
                        if let body = (show?.commit.body ?? commit.body), !body.isEmpty {
                            Text(body).font(Theme.Font.body).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                        HStack(spacing: Theme.Space.sm) {
                            Text(commit.author)
                            if let d = commit.date { Text("· " + d.formatted(date: .abbreviated, time: .shortened)) }
                        }
                        .font(Theme.Font.label)
                        .foregroundStyle(.secondary)
                        Button {
                            UIPasteboard.general.string = commit.hash
                            Haptics.success()
                        } label: {
                            Label(commit.hash, systemImage: Theme.Icon.id).font(Theme.Font.label.monospaced()).lineLimit(1).truncationMode(.middle)
                        }
                        .buttonStyle(.borderless)
                    }
                    .padding(.vertical, Theme.Space.xxs)
                }
                if let show {
                    let chunks = DiffText.split(show.diff)
                    Section("\(show.files.count) files") {
                        ForEach(show.files) { f in
                            NavigationLink {
                                Group {
                                    if model.features(group).contains("git_blob"), let kind = DocumentText.kind(f.name) {
                                        CompareFileView(machine: MachineRef(group: group, id: machineID), cwd: cwd, root: "", file: f.path,
                                                        versions: .commit(commit.hash), kind: kind)
                                    } else {
                                        DiffText(diff: chunks[f.path] ?? "", truncated: show.truncated && chunks[f.path] == nil)
                                    }
                                }
                                .navigationTitle(f.name)
                                .navigationBarTitleDisplayMode(.inline)
                            } label: {
                                GitFileRow(file: f, showsStage: false)
                            }
                        }
                    }
                } else if let error {
                    Text(error).foregroundStyle(Theme.danger)
                    Button("Retry [Components b8784c]") { Task { await load() } }.lineLimit(1)
                } else {
                    HStack { Spacer(); ProgressView(); Spacer() }
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle(commit.short)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } }
            .task { await load() }
            .onHubReconnect(group) { if error != nil { Task { await load() } } }
            #if os(macOS)
            .macLargeSheet(bars: true)
            #endif
        }
    }

    /// Reads the commit's files and diff; a failure of any kind but a cancellation is said, with Retry (#4170).
    private func load() async {
        error = nil
        guard let api = model.api(group) else { error = String(localized: "Not connected to the hub [App Model 93d955]"); return }
        do { show = try await api.gitShow(machine: machineID, cwd: cwd, hash: commit.hash) }
        catch { if !error.isCancellation { self.error = error.shown ?? error.localizedDescription } }
    }
}

extension View {
    /// `action` once each time the group's socket answers again after being down or stalled (a stall keeps the state
    /// connected while the title says Connecting…). The Git sheets read once when they open, so one that failed meanwhile
    /// would wait for Retry with the hub already back (#4385). No polling.
    func onHubReconnect(_ group: String, _ action: @escaping () -> Void) -> some View {
        modifier(HubReconnectAction(group: group, action: action))
    }
}

private struct HubReconnectAction: ViewModifier {
    @Environment(AppModel.self) private var model
    let group: String
    let action: () -> Void

    func body(content: Content) -> some View {
        content.onChange(of: model.isLive(group) && model.socket(group)?.stalled != true) { was, now in if !was && now { action() } }
    }
}

/// A unified diff with old / new line numbers, colored lines, no wrapping (scroll sideways), rendered lazily.
@MainActor private enum GitDiffMemo {
    struct Content {
        let lines: [DiffText.Line]
        let text: NSAttributedString
    }

    private static var values: [String: Content] = [:]

    static func content(_ diff: String) -> Content {
        if let value = values[diff] { return value }
        let lines = DiffText.parse(diff)
        let value = Content(lines: lines, text: DiffText.attributed(lines))
        if value.text.length <= 1_000_000 {
            if values.count >= 8 { values.removeAll(keepingCapacity: true) }
            values[diff] = value
        }
        return value
    }
}

struct DiffText: View {
    var diff: String
    var truncated = false

    nonisolated struct Line: Identifiable, Sendable {
        enum Kind { case add, del, context, hunk, info }
        var id: Int
        var kind: Kind
        var text: String
        var old: Int?
        var new: Int?
        /// The words that changed, as UTF-16 offsets into `text` (`LineDiff.Line.changed`): an edit's in the chat's
        /// `DiffView`; none from a unified diff.
        var changed: [Range<Int>] = []
    }

    var body: some View {
        let content = GitDiffMemo.content(diff)
        if content.lines.isEmpty {
            ContentUnavailableView("No Text Changes", systemImage: Theme.Icon.document, description: Text("It may be a binary file, a permissions-only change, or a change that was undone."))
        } else {
            VStack(spacing: 0) {
                MonoTextView(text: content.text, inset: UIEdgeInsets(top: 6, left: 0, bottom: 6, right: 12))
                if truncated {
                    Text("Change too large; showing the first 512 KB").font(Theme.Font.label).foregroundStyle(.secondary).padding(Theme.Space.s)
                }
            }
            .background(Theme.codeBackground)
        }
    }

    /// Gutter (old / new line numbers, +/−) and the line, colored; lines are padded to the longest so the
    /// added / removed backgrounds run the full width.
    static func attributed(_ lines: [Line]) -> NSAttributedString {
        let font = MonoText.font(11)
        let width = (lines.map(\.text.count).max() ?? 0) + 2
        let out = NSMutableAttributedString()
        func num(_ n: Int?) -> String { let s = n.map(String.init) ?? ""; return String(repeating: " ", count: max(0, 5 - s.count)) + s }
        for (i, l) in lines.enumerated() {
            let (mark, fg, bg): (String, UIColor, UIColor) = switch l.kind {
            case .add: ("+", UIColor(Theme.Diff.add), UIColor(Theme.Diff.add).withAlphaComponent(0.13))
            case .del: ("−", UIColor(Theme.Diff.del), UIColor(Theme.Diff.del).withAlphaComponent(0.12))
            case .hunk: (" ", .secondaryLabel, .quaternarySystemFill)
            case .info: (" ", .secondaryLabel, .clear)
            case .context: (" ", .label, .clear)
            }
            let gutter = num(l.old) + num(l.new) + " " + mark + " "
            out.append(NSAttributedString(string: gutter, attributes: [.font: font, .foregroundColor: UIColor.tertiaryLabel, .backgroundColor: bg]))
            let body = l.text.padding(toLength: max(width, l.text.count), withPad: " ", startingAt: 0)
            let color: UIColor = l.kind == .add || l.kind == .del || l.kind == .context ? .label : fg
            #if os(macOS)
            // An NSTextView fills a background to the view's edge from the line break: the last line needs one too.
            let end = "\n"
            #else
            let end = i < lines.count - 1 ? "\n" : ""
            #endif
            out.append(NSAttributedString(string: body + end, attributes: [.font: font, .foregroundColor: color, .backgroundColor: bg]))
        }
        return out
    }

    nonisolated static func parse(_ diff: String) -> [Line] {
        var out: [Line] = []
        var old = 0, new = 0
        for raw in diff.components(separatedBy: "\n") {
            let id = out.count
            if raw.hasPrefix("diff --git") || raw.hasPrefix("index ") || raw.hasPrefix("--- ") || raw.hasPrefix("+++ ") {
                continue
            }
            if raw.hasPrefix("@@") {
                // @@ -12,7 +12,9 @@ context
                let parts = raw.split(separator: " ")
                if parts.count > 2 {
                    old = Int(parts[1].dropFirst().split(separator: ",").first ?? "") ?? 0
                    new = Int(parts[2].dropFirst().split(separator: ",").first ?? "") ?? 0
                }
                out.append(Line(id: id, kind: .hunk, text: raw))
            } else if raw.hasPrefix("+") {
                out.append(Line(id: id, kind: .add, text: String(raw.dropFirst()), new: new)); new += 1
            } else if raw.hasPrefix("-") {
                out.append(Line(id: id, kind: .del, text: String(raw.dropFirst()), old: old)); old += 1
            } else if raw.hasPrefix(" ") {
                out.append(Line(id: id, kind: .context, text: String(raw.dropFirst()), old: old, new: new)); old += 1; new += 1
            } else if raw.hasPrefix("\\") || raw.hasPrefix("Binary") || raw.hasPrefix("new file") || raw.hasPrefix("deleted file")
                        || raw.hasPrefix("similarity") || raw.hasPrefix("rename ") || raw.hasPrefix("old mode") || raw.hasPrefix("new mode") {
                out.append(Line(id: id, kind: .info, text: raw))
            }
        }
        return out
    }

    /// Splits a multi-file diff (git show) into per-file diffs keyed by the new path.
    static func split(_ diff: String) -> [String: String] {
        var res: [String: String] = [:]
        var path: String?
        var buf: [String] = []
        func flush() { if let path { res[path] = buf.joined(separator: "\n") } }
        for line in diff.components(separatedBy: "\n") {
            if line.hasPrefix("diff --git ") {
                flush()
                buf = []
                // diff --git a/x b/x → x (paths with spaces keep working since b/ is taken from the end)
                if let r = line.range(of: " b/", options: .backwards) { path = String(line[r.upperBound...]) } else { path = nil }
            }
            buf.append(line)
        }
        flush()
        return res
    }
}
