import SwiftUI

/// The branch's pull request in the Git tab (#7014): one row with its number and title, its state, checks and open
/// review threads, opening its page; without one, a way to open one (once the branch is pushed); gh missing or not
/// signed in, a line and Retry. Nothing for no GitHub remote or a detached HEAD.
struct GitPRSection: View {
    let status: GitStatus
    let pr: GitPRModel
    let open: () -> Void
    let create: () -> Void
    let retry: () -> Void

    var body: some View {
        if let failed = pr.failed {
            if !failed.quiet {
                Section { failure(failed) } header: { Text("Pull Request") }
            }
        } else if let a = pr.answer {
            Section {
                if let p = a.pr { row(p) } else { none }
            } header: {
                Text("Pull Request")
            }
        }
    }

    private func failure(_ f: GitPRFailure) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.pullRequest).foregroundStyle(.secondary)
            Text(f.code == "no_gh" ? String(localized: "gh isn't installed on this computer: install it and sign in to see the pull request here")
                 : f.code == "not_signed_in" ? String(localized: "gh isn't signed in on this computer (gh auth login)")
                 : String(localized: "Couldn't read the pull request"))
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
            Spacer(minLength: Theme.Space.s)
            Button(action: retry) { ThemedIcon(Theme.Icon.retry) }
                .buttonStyle(.borderless)
                .disabled(pr.loading)
                .accessibilityLabel(Text("Retry [Components b8784c]"))
                .accessibilityIdentifier("git.pr.retry")
        }
    }

    private var none: some View {
        let can = GitPRModel.pushedAll(status)
        return HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.pullRequest).foregroundStyle(.secondary)
            Text(can ? String(localized: "This branch has no pull request yet") : String(localized: "Push the branch first, then open a pull request"))
                .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            Spacer(minLength: Theme.Space.s)
            Button(action: create) { Text("Open Pull Request").lineLimit(1).minimumScaleFactor(0.8) }
                .themedButton()
                .disabled(!can || pr.busy != nil)
                .accessibilityIdentifier("git.pr.create")
        }
    }

    private func row(_ p: GitPR) -> some View {
        Button(action: open) {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(p.state == "MERGED" ? Theme.Icon.merged : Theme.Icon.pullRequest).foregroundStyle(GitPRLook.tone(p))
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    (Text(verbatim: "#\(p.number) ").foregroundStyle(.secondary) + Text(p.title))
                        .font(Theme.Font.secondaryMedium).foregroundStyle(Color.primary).lineLimit(2)
                    GitPRSummary(pr: p)
                }
                Spacer(minLength: Theme.Space.s)
                if pr.loading { ProgressView() } else { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary) }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("git.pr.open")
    }
}

/// How a pull request and its checks look: GitHub's colors, as the Inbox's GitHub thread page draws them
/// (`GitHubThreadView`, `GitHubChecksCard`).
enum GitPRLook {
    /// Open green, merged purple, closed red, a draft gray.
    static func tone(_ p: GitPR) -> Color {
        switch p.state {
        case "MERGED": Theme.Hue.purple
        case "CLOSED": Theme.danger
        default: p.draft == true ? Theme.Hue.gray : Theme.positive
        }
    }

    /// A check's state as the GitHub thread page names its marks.
    static func card(_ state: String) -> String {
        switch state {
        case "pass": "success"
        case "fail": "failure"
        case "pending": "pending"
        default: "skipped"
        }
    }
}

/// The state chip, the checks in a few words and the open review comments, on one line that wraps.
struct GitPRSummary: View {
    let pr: GitPR

    var body: some View {
        let n = GitPRText.counts(pr.checks)
        FlowLayoutRow(spacing: Theme.Space.s) {
            Chip(text: GitPRText.state(pr), look: .tint(GitPRLook.tone(pr)), compact: true)
            if n.fail > 0 { count(Theme.Icon.errorFilled, Theme.danger, String(localized: "\(n.fail) failed")) }
            if n.pending > 0 { count(Theme.Icon.time, Theme.caution, String(localized: "\(n.pending) running")) }
            if n.pass > 0 { count(Theme.Icon.successFilled, Theme.positive, String(localized: "\(n.pass) passed")) }
            if !pr.threads.isEmpty {
                Text("\(pr.threads.count) open comments").font(Theme.Font.label).foregroundStyle(Theme.cautionText).lineLimit(1)
            }
        }
    }

    private func count(_ symbol: String, _ color: Color, _ words: String) -> some View {
        Label { Text(words).lineLimit(1) } icon: { ThemedIcon(symbol) }
            .font(Theme.Font.label)
            .foregroundStyle(color)
            .labelStyle(.titleAndIcon)
    }
}

/// The pull request's page (#7014, the web's `PRView`): what it is, its checks (each opens its run in the in-app link
/// card), its open review threads (each handed to the agent as a comment for the next message) and merging. Merging,
/// setting it to merge when ready and taking that back each ask first; the branch is never deleted.
struct GitPRSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model
    let machineID: String
    let cwd: String
    let status: GitStatus
    @Bindable var pr: GitPRModel
    let deps: GitPRModel.Deps
    /// Where a review thread goes as a comment for the agent; nil when the session takes no messages.
    let commentKey: SessionKey?

    @State private var link: LinkTarget?
    @State private var method: GitMergeMethod = .squash
    @State private var asking: GitPRModel.How?
    /// The threads handed to the agent and the ones unfolded, by id.
    @State private var handed: Set<String> = []
    @State private var unfolded: Set<String> = []

    var body: some View {
        NavigationStack {
            List {
                if let p = pr.answer?.pr {
                    Section { header(p) }
                    if !p.checks.isEmpty { checks(p) }
                    threads(p)
                    if p.state == "OPEN" { merge(p) }
                } else if pr.loading {
                    HStack { Spacer(); ProgressView(); Spacer() }.listRowBackground(Color.clear)
                } else {
                    ContentUnavailableView("This Branch Has No Pull Request", systemImage: Theme.Icon.pullRequest)
                        .listRowBackground(Color.clear)
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .refreshable { await pr.read(status, machine: machineID, cwd: cwd, deps: deps, force: true) }
            .navigationTitle(pr.answer?.pr.map { Text(verbatim: "#\($0.number)") } ?? Text("Pull Request"))
            .navigationSubtitle(pr.answer?.pr.map { "\($0.head) → \($0.base)" } ?? (status.branch ?? ""))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if let p = pr.answer?.pr, let u = URL(string: p.url) {
                    ToolbarItem(placement: .topBarLeading) {
                        Button { link = LinkTarget(url: u) } label: { ThemedIcon(Theme.Icon.openExternal) }
                            .accessibilityLabel(Text("Open on GitHub"))
                            .accessibilityIdentifier("git.pr.github")
                    }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button { dismiss() } label: { Text("Done [Extension System Presentation c0b3fb]").lineLimit(1) }
                        .disabled(pr.busy != nil)
                }
            }
            .confirmationDialog(Text(asking.map(askTitle) ?? ""), isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }),
                                titleVisibility: .visible, presenting: asking) { how in
                Button { Task { await go(how) } } label: { Text(askButton(how)).lineLimit(1) }
                    .accessibilityIdentifier("git.pr.confirm")
            } message: { how in
                Text(askMessage(how))
            }
            .gitRefusalAlert($pr.refusal)
            .sheet(item: $link) { LinkSheet(target: $0, key: commentKey) }
        }
        .interactiveDismissDisabled(pr.busy != nil)
    }

    private func header(_ p: GitPR) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text(p.title).font(Theme.Font.heading).textSelection(.enabled)
            FlowLayoutRow(spacing: Theme.Space.s) {
                Chip(text: GitPRText.state(p), look: .tint(GitPRLook.tone(p)), compact: true)
                if let r = p.review, !r.isEmpty { Text(GitPRText.review(r)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1) }
                if p.autoMerge == true && p.state == "OPEN" {
                    Text("Merges when ready").font(Theme.Font.label).foregroundStyle(Theme.accent).lineLimit(1)
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityIdentifier("git.pr.header")
    }

    private func checks(_ p: GitPR) -> some View {
        Section {
            ForEach(p.checks) { c in
                Button { if let u = c.url.flatMap(URL.init(string:)) { link = LinkTarget(url: u) } } label: {
                    HStack(spacing: Theme.Space.m) {
                        ThemedIcon(GitHubChecksCard.symbol(GitPRLook.card(c.state))).foregroundStyle(GitHubChecksCard.color(GitPRLook.card(c.state)))
                        Text(c.name).foregroundStyle(Color.primary).lineLimit(2)
                        Spacer(minLength: Theme.Space.s)
                        Text(checkWord(c.state)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        if c.url != nil { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary) }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(c.url == nil)
                .accessibilityIdentifier("git.pr.check")
            }
        } header: {
            Text("Checks")
        }
    }

    private func checkWord(_ state: String) -> String {
        switch state {
        case "pass": String(localized: "Passed")
        case "fail": String(localized: "Failed")
        case "pending": String(localized: "Running")
        default: String(localized: "Skipped")
        }
    }

    private func threads(_ p: GitPR) -> some View {
        Section {
            if p.threads.isEmpty {
                Text("No open review comments").foregroundStyle(.secondary)
            }
            ForEach(p.threads) { thread(p, $0) }
        } header: {
            HStack {
                Text("Open review comments · \(p.threads.count)")
                Spacer(minLength: Theme.Space.s)
                if p.threads.count > 1, commentKey != nil {
                    Button { hand(p, p.threads) } label: { Text("Hand All to Agent").lineLimit(1) }
                        .textCase(nil)
                        .accessibilityIdentifier("git.pr.handAll")
                }
            }
        }
    }

    private func thread(_ p: GitPR, _ th: GitPRThread) -> some View {
        let i = th.id
        return VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                ThemedIcon(Theme.Icon.document).font(Theme.Font.label).foregroundStyle(.secondary)
                Text(GitPRText.place(th)).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head)
                if th.outdated == true { Chip(text: String(localized: "Outdated"), compact: true) }
            }
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                ForEach(th.comments) { c in
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        HStack(spacing: Theme.Space.xs) {
                            Text(verbatim: "@\(c.author)").font(Theme.Font.labelStrong)
                            if let at = c.at { Text("· " + Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.secondary) }
                        }
                        .lineLimit(1)
                        Text(Self.markdown(c.body)).font(Theme.Font.secondary).textSelection(.enabled).readingContent()
                    }
                }
            }
            .lineLimit(unfolded.contains(i) ? nil : 6)
            HStack(spacing: Theme.Space.s) {
                if th.comments.count > 1 || th.comments.contains(where: { $0.body.count > 240 }) {
                    Button { if unfolded.contains(i) { unfolded.remove(i) } else { unfolded.insert(i) } } label: {
                        Text(unfolded.contains(i) ? "Show Less" : "Show All").lineLimit(1)
                    }
                    .buttonStyle(.borderless)
                    .foregroundStyle(.secondary)
                }
                Spacer(minLength: Theme.Space.s)
                if commentKey != nil {
                    Button { hand(p, [th]) } label: {
                        Label(handed.contains(i) ? String(localized: "In the Message") : String(localized: "Hand to Agent"),
                              systemImage: handed.contains(i) ? Theme.Icon.done : Theme.Icon.routeToTask)
                            .lineLimit(1)
                    }
                    .themedButton()
                    .accessibilityIdentifier("git.pr.hand")
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
    }

    private func merge(_ p: GitPR) -> some View {
        let ready = GitMergeReady(p)
        return Section {
            if ready == .no {
                Text("A draft: mark it ready for review on GitHub before merging").foregroundStyle(.secondary)
            } else {
                Picker(selection: $method) {
                    ForEach(GitMergeMethod.allCases) { Text($0.title).tag($0) }
                } label: { Text("How") }
                    .accessibilityIdentifier("git.pr.method")
                Text(ready == .conflict ? String(localized: "It conflicts with \(p.base): resolve that first")
                     : ready == .later ? (p.autoMerge == true ? String(localized: "Set to merge by itself once its checks and reviews pass")
                                          : String(localized: "Not mergeable yet: its checks or reviews are still needed. It can merge by itself once they pass."))
                     : String(localized: "Ready to merge into \(p.base)"))
                    .font(Theme.Font.secondary).foregroundStyle(.secondary)
                if p.autoMerge == true {
                    mergeButton(String(localized: "Don't Merge by Itself"), .notByItself, prominent: false)
                } else if ready != .now {
                    mergeButton(String(localized: "Merge When Ready"), .whenReady(method), prominent: false)
                }
                if ready == .now {
                    mergeButton(String(localized: "git.pr.merge", defaultValue: "Merge"), .now(method), prominent: true)
                }
            }
        } header: {
            Text(String(localized: "git.pr.merge", defaultValue: "Merge"))
        }
    }

    private func mergeButton(_ title: String, _ how: GitPRModel.How, prominent: Bool) -> some View {
        Button { asking = how } label: {
            HStack(spacing: Theme.Space.s) {
                if pr.busy == .merge(how) { ProgressView() }
                Text(title).lineLimit(1)
            }
            .frame(maxWidth: .infinity)
        }
        .themedButton(prominent: prominent)
        .disabled(pr.busy != nil)
        .accessibilityIdentifier("git.pr.merge")
    }

    private func askTitle(_ how: GitPRModel.How) -> String {
        switch how {
        case .now: String(localized: "Merge #\(pr.answer?.pr?.number ?? 0)?")
        case .whenReady: String(localized: "Merge When Ready?")
        case .notByItself: String(localized: "Stop Merging by Itself?")
        }
    }

    private func askButton(_ how: GitPRModel.How) -> String {
        switch how {
        case .now: String(localized: "git.pr.merge", defaultValue: "Merge")
        case .whenReady: String(localized: "Merge When Ready")
        case .notByItself: String(localized: "Don't Merge by Itself")
        }
    }

    private func askMessage(_ how: GitPRModel.How) -> String {
        guard let p = pr.answer?.pr else { return "" }
        switch how {
        case .now(let m): return String(localized: "#\(p.number) goes into \(p.base) on GitHub now (\(m.title)). \(p.head) is kept, here and on GitHub.")
        case .whenReady(let m): return String(localized: "Once its checks and reviews pass, GitHub merges it into \(p.base) (\(m.title)) without asking again. \(p.head) is kept, here and on GitHub.")
        case .notByItself: return String(localized: "It stays open; nothing merges until you merge it.")
        }
    }

    private func go(_ how: GitPRModel.How) async {
        asking = nil
        guard let p = pr.answer?.pr else { return }
        guard await pr.merge(p, how, machine: machineID, cwd: cwd, deps: deps) != nil else {
            if pr.refusal != nil { Haptics.error() }
            return
        }
        Haptics.success()
        switch how {
        case .now: model.toast = Toast(String(localized: "Merged #\(p.number)"), style: .success)
        case .whenReady: model.toast = Toast(String(localized: "It merges once its checks and reviews pass"), style: .success)
        case .notByItself: model.toast = Toast(String(localized: "It no longer merges by itself"), style: .success)
        }
    }

    /// Review threads go over the message box as comments (`MessageComment`, the web's block), sent with the next message.
    private func hand(_ p: GitPR, _ list: [GitPRThread]) {
        guard let key = commentKey else { return }
        for th in list {
            model.composerComments[key, default: []].append(GitPRText.comment(p, th))
            handed.insert(th.id)
        }
        Haptics.success()
        model.toast = Toast(list.count == 1 ? String(localized: "Added to the message: it goes with your next message")
                            : String(localized: "\(list.count) review comments added to the message"), style: .success)
    }

    private static func markdown(_ s: String) -> AttributedString {
        (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
    }
}

/// Opening a pull request for the branch (#7014, the web's `PRCreateView`): title (its last commit's subject to start),
/// description, Into (the default branch or another), Open as Draft. It is public, so it asks first; once open, the
/// page takes this sheet's place.
struct GitPRCreateSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model
    let machineID: String
    let cwd: String
    let status: GitStatus
    @Bindable var pr: GitPRModel
    let deps: GitPRModel.Deps
    /// The branches it could go into, and the default one's name when git knows it.
    let branches: () async throws -> GitBranchList
    /// Shows the new pull request's page.
    let opened: () -> Void

    @State private var title: String
    @State private var text = ""
    @State private var base = ""
    @State private var draft = false
    @State private var bases: [String] = []
    /// The remote's default branch by name (origin/HEAD); nil: unknown, said as "the default branch".
    @State private var defaultBranch: String?
    @State private var asking = false

    init(machineID: String, cwd: String, status: GitStatus, pr: GitPRModel, deps: GitPRModel.Deps,
         branches: @escaping () async throws -> GitBranchList, opened: @escaping () -> Void) {
        self.machineID = machineID
        self.cwd = cwd
        self.status = status
        self.pr = pr
        self.deps = deps
        self.branches = branches
        self.opened = opened
        _title = State(initialValue: status.head?.subject ?? "")
    }

    private var canOpen: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && GitPRModel.pushedAll(status) && pr.busy == nil
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    TextField("Title", text: $title).accessibilityIdentifier("git.pr.title")
                    TextField("Description (optional)", text: $text, axis: .vertical).lineLimit(4...12)
                }
                Section {
                    Picker(selection: $base) {
                        Text(defaultBranch.map { String(localized: "\($0) (Default)") } ?? String(localized: "The Default Branch")).tag("")
                        ForEach(bases, id: \.self) { Text($0).tag($0) }
                    } label: { Text("Into") }
                        .accessibilityIdentifier("git.pr.into")
                    Toggle("Open as Draft", isOn: $draft)
                } footer: {
                    Text(GitPRModel.pushedAll(status) ? String(localized: "The title starts as the last commit's subject.")
                         : String(localized: "Push the branch first, then open a pull request"))
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle(Text("New Pull Request"))
            .navigationSubtitle(status.branch ?? "")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button { dismiss() } label: { Text("Cancel").lineLimit(1) }.disabled(pr.busy != nil)
                }
                ToolbarItem(placement: .confirmationAction) {
                    if pr.busy == .create {
                        ProgressView()
                    } else {
                        Button { asking = true } label: { Text("Create").lineLimit(1) }
                            .disabled(!canOpen)
                            .accessibilityIdentifier("git.pr.open.go")
                    }
                }
            }
            .confirmationDialog(Text("Open a Pull Request?"), isPresented: $asking, titleVisibility: .visible) {
                Button { Task { await go() } } label: { Text("Open Pull Request").lineLimit(1) }
                    .accessibilityIdentifier("git.pr.create.confirm")
            } message: {
                Text(confirmation)
            }
            .gitRefusalAlert($pr.refusal)
            .task { await loadBases() }
        }
        .interactiveDismissDisabled(pr.busy != nil)
    }

    /// Where it goes, by name when it is known; "the default branch" has its own sentence, so it reads as words.
    private var into: String? { base.isEmpty ? defaultBranch : base }

    private var confirmation: String {
        let from = status.branch ?? ""
        switch (into, draft) {
        case (let name?, true): return String(localized: "A draft pull request from \(from) into \(name) is opened on GitHub; everyone who can see the repository sees it.")
        case (let name?, false): return String(localized: "A pull request from \(from) into \(name) is opened on GitHub; everyone who can see the repository sees it.")
        case (nil, true): return String(localized: "A draft pull request from \(from) into the default branch is opened on GitHub; everyone who can see the repository sees it.")
        case (nil, false): return String(localized: "A pull request from \(from) into the default branch is opened on GitHub; everyone who can see the repository sees it.")
        }
    }

    private func go() async {
        asking = false
        // the branch shown is the one asked for: a default known by name goes by that name
        guard await pr.create(title: title, body: text, base: into, draft: draft, machine: machineID, cwd: cwd, deps: deps)?.pr != nil else {
            if pr.refusal != nil { Haptics.error() }
            return
        }
        Haptics.success()
        model.toast = Toast(String(localized: "Pull request opened"), style: .success)
        opened()
    }

    /// The other branches by the name GitHub knows them by, without the current one.
    private func loadBases() async {
        guard let list = try? await branches() else { return }
        defaultBranch = list.defaultBranch
        var seen = Set<String>(), out: [String] = []
        for b in list.branches where b.current != true {
            let name = b.switchName
            if name != status.branch, name != list.defaultBranch, seen.insert(name).inserted { out.append(name) }
        }
        bases = out
    }
}
