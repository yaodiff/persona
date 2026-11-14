import BeautifulUI
import SwiftUI

@MainActor final class InboxAccountState: ObservableObject {
    @Published var accounts = InboxMailAccounts.cached(AppModel.shared.assistantManagerID)
}

/// The mail accounts the inbox's filters and New Email offer: the current assistant's (each manager keeps its own
/// sources, docs/assistant/managers.md "Pages"), kept for the next launch apart for each assistant; the default's
/// under the name they always had.
@MainActor enum InboxMailAccounts {
    static func cacheName(_ manager: String?) -> String { manager.map { "mail-accounts." + $0 } ?? "mail-accounts" }

    static func cached(_ manager: String?) -> [MailAccount] {
        StateCache.load(cacheName(manager), as: [MailAccount].self) ?? []
    }

    /// The accounts of `manager` (nil: the default's) as the hub has them now, kept; nil when they can't be read, or
    /// when another assistant became current meanwhile (they are not the page's any more).
    static func read(_ model: AppModel) async -> [MailAccount]? {
        let manager = model.assistantManagerID
        guard let api = model.api?.manager(manager), let accounts = try? await api.mailAccounts(),
              manager == model.assistantManagerID else { return nil }
        StateCache.save(cacheName(manager), accounts)
        return accounts
    }
}

// MARK: - Inbox

/// The unified inbox (docs/assistant/inbox.md): Feishu messages and mail in one list, one row per thing (the new messages in a
/// chat, a mail thread), newest first. No per-chat browsing: what is here is what came in. Filters narrow it by source
/// (Lark, one mail account) and kind (DMs, @Me, Group chat); a row swipes away as Done, and the done ones stay at the bottom
/// for a few days in case of a slip.
struct InboxView: View {
    @State private var path = NavigationPath()
    @StateObject private var accountState = InboxAccountState()

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("InboxView")
        #endif
        SplitNavigation(path: $path, destinations: InboxDestinations(accounts: accountState.accounts)) {
            InboxPage(path: $path, accountState: accountState)
        } empty: {
            SplitPlaceholder(symbol: Theme.Icon.inbox, title: String(localized: "Pick an Item"), message: String(localized: "Tap an item in the list on the left to see its details here."))
        }
        .environment(\.feishuOpen) { path.append($0) }
        .feishuHandlesLinks { path.append($0) }
    }
}

extension InboxView {
    /// A mail account's short name in the filters (the Mac's inbox uses it too).
    static func short(_ email: String) -> String { InboxPage.short(email) }
}

/// A waiting session pushes only on its own hub's Assistant stack. Another group's card keeps the old open, which
/// selects that group's Persona for an assistant conversation instead of leaving a foreign route on this hub's stack.
@MainActor enum InboxWaitingOpen {
    static func session(_ key: SessionKey, currentHub: String?, push: ((InboxChatRoute) -> Void)?, open: () -> Void) {
        if let push, key.group == currentHub {
            push(InboxChatRoute(key: key, manager: nil))
        } else {
            open()
        }
    }
}

/// The inbox's page itself, in whatever stack shows it: Inbox's own (InboxView), or the shell's Assistant page (pushed from
/// the drawer, docs/apple/ios.md「New shell」), which passes `embedded` so the page brings its destinations and link handling.
struct InboxPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.pushInboxChat) private var pushChat
    @Binding var path: NavigationPath
    @ObservedObject var accountState: InboxAccountState
    var embedded = false
    @StoredValue("inbox.source") private var source = "" // "" all, "feishu", or a mail account id
    @StoredValue("inbox.kind") private var kind = ""     // "" all, agent, dm, mention, group, mail
    @State private var query = ""
    @State private var itemMemo = InboxProjectionMemo()
    private var accounts: [MailAccount] {
        get { accountState.accounts }
        nonmutating set { accountState.accounts = newValue }
    }
    /// Sections shown open: tiers (action, working, fyi, noise) and "done". Collapsed and Done start closed.
    @State private var folds = InboxFolds()
    /// Needs You, FYI and Collapsed by source or by time (#2059), kept on this device; Done is always by time.
    @StoredValue("inbox.grouping") private var grouping = "" // "" by source, "time"
    /// The groups opened or closed by hand, kept on this device: JSON of "|<section>|<group>" → open (the leading bar is
    /// where a manager's id was while the list had one group per manager).
    @StoredValue("inbox.folds") private var groupFoldsJSON = ""
    /// A title to scroll to (Done's, for the DEBUG `-showInboxDone`).
    @State private var scrollTarget: String?
    /// Done groups that loaded their first page by themselves on opening (#2969): once while the page is up.
    @State private var autoLoaded: Set<String> = []
    @State private var composing: ComposeDraft?
    @State private var editingDraft: InboxItem? // Edit on the AI Twin's draft
    @State private var answering: Interaction? // a card on no session, open by itself (#4059)
    @State private var showingMutes = false     // Muted
    @State private var showingRules = false     // Rules
    @State private var showingSubscriptions = false  // source subscriptions (#2041)
    @State private var ruling: InboxPolicyTarget? // a row's Rule for This Chat / Sender / Repository…
    @State private var todoRequest: InboxTodoRequest? // rows waiting for their to-do's due time (#6480)
    @State private var confirming = false // Clear All over many items asks first
    /// Selecting (#2059): the picked items' keys (`InboxItem.key`); nil when not selecting. Marking stays in Persona (done, with Undo).
    @State private var picked: Set<String>?
    private var store: InboxStore { .shared }

    /// The kind filters, by key.
    static let kinds = [("mention", String(localized: "@Me")), ("dm", String(localized: "DMs")), ("group", String(localized: "Group chat")), ("mail", String(localized: "Mail [Assistant View 3d708e]")), ("agent", String(localized: "Agent"))]

    private var itemProjection: InboxProjection {
        itemMemo(store.items, filter: .init(source: source, kind: kind, query: query, scope: model.assistantScope), revision: store.itemsRevision)
    }

    private var openItems: [InboxItem] { itemProjection.openItems }

    /// The hub's connectors with items in the list (GitHub…), one filter each; none, no chip.
    private var connectorSources: [InboxProjection.Source] { itemProjection.connectorSources }

    /// The current assistant's agents (and the assistant) waiting for an answer, an approval or a plan review: they need
    /// the user too, so they head Needs You. Opening one goes to its chat; the Agents tab stays as it is.
    private var waitingAgents: [Interaction] {
        guard source.isEmpty, kind.isEmpty || kind == "agent" else { return [] }
        return model.scopedInteractions.filter { ix in
            !model.isMuted(ix.sessionKey) && (query.isEmpty || [ix.sessionTitle ?? "", ix.summary ?? "", ix.title].contains { $0.localizedCaseInsensitiveContains(query) })
        }
    }

    var body: some View {
        if embedded { // its destinations are the shell's (ShellDestinations)
            page
                .environment(\.feishuOpen) { path.append($0) }
                .feishuHandlesLinks { path.append($0) }
        } else {
            page
        }
    }

    private var page: some View {
            list
                .navigationTitle("Inbox")
                .navigationBarTitleDisplayMode(.inline)
                .pageSearchable(text: $query, prompt: String(localized: "Search Titles, Senders, and Content"))
                .pageToolbar {
                    if let picked {
                        selectingToolbar(picked)
                    } else {
                    ToolbarItem(placement: .principal) { ConnectionTitle(title: String(localized: "Inbox")) }
                    if model.isMultiHub {
                        // Each hub has its own inbox (docs/protocol/multi-hub.md「Terminology」): whose this is, and the others.
                        ToolbarItem(placement: .topBarLeading) { HubSwitcher(compact: true) }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Button("Select") { withAnimation(Theme.Motion.standard) { picked = [] } }
                            .disabled(pickable.isEmpty)
                            .accessibilityIdentifier("inbox.select")
                    }
                    ToolbarItem(placement: .topBarTrailing) {

                        // Mail accounts are managed in Settings › Connectors › Mail (2026-11-01, with this device's data and Lark).
                        Menu {
                            cleanButton
                            Divider()
                            Button { composing = ComposeDraft(accountID: accounts.first?.id ?? "", manager: model.assistantManagerID, accounts: accounts) } label: { Label("New Email", systemImage: Theme.Icon.compose) }
                                .disabled(accounts.isEmpty)
                            Button { showingMutes = true } label: { Label("Muted [Inbox Mutes View e5e468]", systemImage: Theme.Icon.mute) }
                            Button { showingRules = true } label: { Label("Rules [Inbox Policy]", systemImage: Theme.Icon.inboxRule) }
                            // Every source subscription, with the Inbox's other settings, once the hub shows them (#2041).
                            if model.hubFeatures.contains(SubscriptionsUI.feature) {
                                Button { showingSubscriptions = true } label: {
                                    if model.hubFeatures.contains(SubscriptionsUI.sourcesFeature) { // every source of the Inbox (#5278)
                                        Label("Inbox Sources", systemImage: Theme.Icon.subscriptions)
                                    } else {
                                        Label("Subscriptions [Source Subscriptions]", systemImage: Theme.Icon.subscriptions)
                                    }
                                }
                                .accessibilityIdentifier("inbox.subscriptions")
                            }
                        } label: { ThemedIcon(Theme.Icon.moreMenu) }
                        .accessibilityLabel("More")
                        .accessibilityIdentifier("inbox.more")
                    }
                    }
                }
                // selecting: the bar at the bottom is the selection's (as in Mail), not the tabs'
                .toolbar(picked == nil ? .automatic : .hidden, for: .tabBar)
                .sheet(item: $composing) { d in ComposeView(draft: d, accounts: d.accounts ?? accounts) }
                .sheet(item: $editingDraft) { InboxDraftSheet(item: $0, accounts: accounts) }
                .sheet(item: $answering) { SessionlessCardSheet(interaction: $0) }
                .sheet(isPresented: $showingMutes) { InboxMutesView() }
                .sheet(isPresented: $showingRules) { InboxRulesView() }
                .sheet(isPresented: $showingSubscriptions) { SubscriptionsSheet() }
                .sheet(item: $ruling) { InboxPolicySheet(target: $0) }
                .sheet(item: $todoRequest) { InboxDueSheet(request: $0) }
                .confirmationDialog(InboxBulk.confirmTitle, isPresented: $confirming, titleVisibility: .visible) {
                    Button(InboxBulk.confirmButton(count: cleanScope.count)) { clean(confirmed: true) }
                        .accessibilityIdentifier("inbox.cleanConfirm")
                    Button("Cancel", role: .cancel) {}
                } message: {
                    Text(InboxBulk.confirmMessage(count: cleanScope.count, filters: filterNames, query: query))
                }
                // Another assistant is current: the list is its inbox now. What was picked, opened or asked of the one
                // before goes (an item's page, the Muted and Rules lists, a rule's sheet: they read it and would write to
                // it), a source filter too (a mailbox or a connector of the one before would leave the list empty), and
                // Done's groups load their first pages again, from its history. A mail being written stays: it keeps
                // the assistant and the accounts it was started with (`ComposeDraft.manager`), and goes out through it.
                .onChange(of: model.assistantSwitches) {
                    source = ""
                    // This assistant's accounts as last kept (it may be offline), then as the hub has them.
                    accounts = InboxMailAccounts.cached(model.assistantManagerID)
                    Task { await loadAccounts() }
                    picked = nil
                    autoLoaded = []
                    showingMutes = false
                    showingRules = false
                    ruling = nil
                    editingDraft = nil
                    if !embedded, !path.isEmpty { path = NavigationPath() }
                }
                // A plugin enabled or set up since: its sources' items open as it draws them.
                .task { await InboxItemSlots.shared.reload() }
                .task {
                    store.reload()
                    await loadAccounts()
                    // A source kept from another time that no chip stands for now (a connector of another assistant's
                    // items): the list would be empty with nothing to turn off.
                    if !source.isEmpty, source != "feishu", store.listed, !accounts.contains(where: { $0.id == source }),
                       !connectorSources.contains(where: { $0.id == source }) { source = "" }
                    #if DEBUG
                    // `-showInboxMutes YES`: the Muted list (screenshots).
                    if UserDefaults.standard.bool(forKey: "showInboxMutes") { showingMutes = true }
                    // `-showInboxRules YES`: the Rules list. `-openInboxRule chat|sender|repo`: the rule sheet of the first open
                    // item with one, as its menu opens it; `edit`: Rules, then Edit on the newest rule (screenshots).
                    if UserDefaults.standard.bool(forKey: "showInboxRules") || UserDefaults.standard.string(forKey: "openInboxRule") == "edit" {
                        showingRules = true
                    } else if let tag = UserDefaults.standard.string(forKey: "openInboxRule") {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        for it in openItems {
                            if let m = it.mutes?.first(where: { $0.tag == tag }) { ruling = InboxPolicyTarget(item: it, option: m); break }
                        }
                    }
                    // `-openInboxFirst YES`: open the first open item (detail screenshots).
                    if UserDefaults.standard.bool(forKey: "openInboxFirst") {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        if let first = openItems.first { path = NavigationPath([first]) }
                    }
                    // `-openInboxItem <id>`: open that item (a kind's detail, e.g. a Moments one).
                    if let id = UserDefaults.standard.string(forKey: "openInboxItem") {
                        for _ in 0..<20 where !store.items.contains(where: { $0.id == id }) { try? await Task.sleep(for: .milliseconds(500)) }
                        if let it = store.items.first(where: { $0.id == id }) { path = NavigationPath([it]) }
                    }
                    // `-showInboxPicked <n>`: Select with the first n items in sight picked (#2059 screenshots; 0: none);
                    // `-showInboxPickedDone YES` then marks them done (the toast with Undo).
                    if UserDefaults.standard.object(forKey: "showInboxPicked") != nil {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        picked = Set(pickable.prefix(UserDefaults.standard.integer(forKey: "showInboxPicked")).map(\.key))
                        if UserDefaults.standard.bool(forKey: "showInboxPickedDone") {
                            try? await Task.sleep(for: .milliseconds(800))
                            store.setDone(chosen(picked ?? []), true)
                            picked = nil
                        }
                    }
                    // `-showInboxDue YES` / `-showInboxMerge YES` / `-showInboxTodo YES`: Select with the first two rows picked and the
                    // due sheet open / those two merged / those two added to to-dos for today (#6480 screenshots).
                    let actionKeys = ["showInboxDue", "showInboxMerge", "showInboxTodo"].filter { UserDefaults.standard.bool(forKey: $0) }
                    if let action = actionKeys.first {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        picked = Set(pickable.prefix(2).map(\.key))
                        try? await Task.sleep(for: .milliseconds(800))
                        let two = chosen(picked ?? [])
                        switch action {
                        case "showInboxDue": todoRequest = InboxTodoRequest(items: two) { picked = nil }
                        case "showInboxMerge": store.merge(two) { picked = nil }
                        default: store.addTodos(two, due: .today) { picked = nil }
                        }
                    }
                    // `-showInboxDone YES`: the first group's Done open and scrolled to, its groups' titles sticking (#2059 screenshots).
                    if UserDefaults.standard.bool(forKey: "showInboxDone") {
                        for _ in 0..<20 where store.items.isEmpty || store.history.isEmpty && store.doneDays == nil { try? await Task.sleep(for: .milliseconds(500)) }
                        let share = share
                        if !folds.isOpen("done") { folds.toggle("done") }
                        let done = doneAll(itemProjection.sortedDoneItems)
                        // its newest group open (only Just Now and Today start open), then scrolled to its first row:
                        // Done's title and the group's stick above it
                        if UserDefaults.standard.bool(forKey: "showInboxDoneFolded"),
                           let last = InboxGroups.done(done, now: AppClock.now).filter({ groupOpen("done", $0.key) }).last?.items.last {
                            // `-showInboxDoneFolded YES`: the groups as they start (Just Now and Today open), the last
                            // open row at the top so the folded ones with their counts follow it (#2969 screenshots)
                            try? await Task.sleep(for: .milliseconds(300))
                            scrollTarget = last.key
                        } else if let g = InboxGroups.done(done, now: AppClock.now).first {
                            setGroupOpen("done", g.key, true)
                            try? await Task.sleep(for: .milliseconds(300))
                            scrollTarget = g.items.first?.key
                        } else {
                            // the hub's day counts and nothing loaded yet (#2969): Done's groups are folded, so the
                            // last open row above it goes to the top (a List scrolls to rows, not headers)
                            try? await Task.sleep(for: .milliseconds(300))
                            scrollTarget = [InboxItem.Tier.action, .fyi].flatMap { share.byTier[$0] ?? [] }.last?.key
                        }
                    }
                    // `-showInboxJump <group>` ("week", "month", "d:2026-11-06"…): Done open and jumped to that group, its first
                    // page loaded from the hub's history (#2969 screenshots: exact counts on groups not loaded before).
                    if let key = UserDefaults.standard.string(forKey: "showInboxJump"), !key.isEmpty {
                        for _ in 0..<20 where store.items.isEmpty || store.doneDays == nil { try? await Task.sleep(for: .milliseconds(500)) }
                        if !folds.isOpen("done") { folds.toggle("done") }
                        setGroupOpen("done", key, true)
                        autoLoaded.insert(key)
                        store.loadDoneGroup(key, range: InboxGroups.timeRange(key, now: AppClock.now))
                        for _ in 0..<50 where store.groupLoading.contains(key) || store.history.isEmpty { try? await Task.sleep(for: .milliseconds(100)) }
                        try? await Task.sleep(for: .milliseconds(300))
                        scrollTarget = store.history.filter { InboxGroups.timeKey($0.doneAt, now: AppClock.now, recent: true) == key }
                            .max { ($0.doneAt ?? .distantPast) < ($1.doneAt ?? .distantPast) }?.key
                    }
                    // `-showInboxPickGroups YES`: Select with the mail group folded and picked whole and the Lark group picked.
                    if UserDefaults.standard.bool(forKey: "showInboxPickGroups") {
                        for _ in 0..<20 where store.open.isEmpty { try? await Task.sleep(for: .milliseconds(500)) }
                        let groups = groupsOf(share.byTier[.action] ?? [])
                        if let mail = groups.first(where: { $0.key.hasPrefix("mail:") }) { setGroupOpen("action", mail.key, false) }
                        picked = Set(groups.filter { $0.key == "lark" || $0.key.hasPrefix("mail:") }.flatMap(\.items).map(\.key))
                        if UserDefaults.standard.bool(forKey: "showInboxPickedDone") {
                            try? await Task.sleep(for: .milliseconds(800))
                            store.setDone(chosen(picked ?? []), true)
                            picked = nil
                        }
                    }
                    // `-openInboxAsk <id>`: open that waiting agent's row as a tap does (a sessionless card in its sheet, #4059).
                    if let id = UserDefaults.standard.string(forKey: "openInboxAsk") {
                        for _ in 0..<20 where !model.pendingInteractions.contains(where: { $0.id == id }) { try? await Task.sleep(for: .milliseconds(500)) }
                        if let ix = model.pendingInteractions.first(where: { $0.id == id }) { open(ix) }
                    }
                    #endif
                }
    }

    // MARK: Groups (#2059)

    /// The sections' parts, as the list draws them.
    private var share: InboxShare {
        InboxShare(byTier: kind == "agent" ? [:] : itemProjection.byTier, agents: waitingAgents, done: itemProjection.doneItems)
    }

    private var groupFolds: [String: Bool] { (try? JSONDecoder().decode([String: Bool].self, from: Data(groupFoldsJSON.utf8))) ?? [:] }

    private static func groupID(_ section: String, _ key: String) -> String { "|\(section)|\(key)" }

    private func groupOpen(_ section: String, _ key: String) -> Bool {
        groupFolds[Self.groupID(section, key)] ?? InboxGroups.openAtFirst(section: section, key: key)
    }

    private func setGroupOpen(_ section: String, _ key: String, _ open: Bool) {
        var f = groupFolds
        f[Self.groupID(section, key)] = open
        groupFoldsJSON = (try? JSONEncoder().encode(f)).flatMap { String(data: $0, encoding: .utf8) } ?? ""
    }

    /// A section's items by source (the chips' order) or by time.
    private func groupsOf(_ items: [InboxItem]) -> [InboxGroups.Group] {
        if grouping == InboxGroups.Grouping.time.rawValue {
            return InboxGroups.byTime(items, now: AppClock.now, at: \.updatedAt)
        }
        let order = ["lark"] + accounts.map { "mail:\($0.id)" } + connectorSources.map { "c:\($0.id)" }
        return InboxGroups.bySource(items, order: order) { id in accounts.first { $0.id == id }?.email ?? id }
    }

    /// Whether a section shows its groups' titles: two groups or more, or the agents waiting above its items.
    private func titled(_ tier: InboxItem.Tier, groups: [InboxGroups.Group], agents: Int) -> Bool {
        tier != .working && (groups.count > 1 || (agents > 0 && !groups.isEmpty))
    }

    /// Whether a Done group is complete: a page of its own reached its start, or (unfiltered) as many are loaded as the
    /// hub counts. The list's done rows are not a group being complete: after #5549 it carries only the newest 200 of them.
    static func doneGroupComplete(_ key: String, _ n: Int, groupComplete: Set<String>, counts: [String: Int], filtered: Bool) -> Bool {
        if groupComplete.contains(key) { return true }
        return !filtered && n >= counts[key, default: 0]
    }

    /// Done's groups (#2969). With the hub's day counts (#2967; the current assistant's, as the history is) every group they
    /// name is listed with its count, loaded or not, and loads its own items when it opens; filtered or searched, the
    /// counts are of what is loaded ("n+" until the group is complete), and `next` is the group "Show Older" loads.
    /// Without them, as before: what the history has loaded, "n+" on the last group while there is more.
    private func doneGroups(_ done: [InboxItem], days: [String: Int]?) -> (groups: [DoneGroup], next: DoneGroup?) {
        let now = AppClock.now
        let loaded = InboxGroups.done(done, now: now)
        guard let days else {
            let more = store.historyMore && !store.history.isEmpty
            return (loaded.enumerated().map { i, g in
                DoneGroup(group: g, count: g.items.count, plus: i == loaded.count - 1 && more, more: false, range: InboxGroups.timeRange(g.key, now: now))
            }, nil)
        }
        let filtered = !source.isEmpty || !kind.isEmpty || !query.isEmpty
        let counts = InboxGroups.dayCounts(days, now: now, just: filtered ? 0 : loaded.first { $0.key == "just" }?.items.count ?? 0)
        let byKey = Dictionary(loaded.map { ($0.key, $0.items) }, uniquingKeysWith: +)
        // complete: a page of its own reached its start, or (unfiltered) as many are loaded as the hub counts. The
        // list's done rows are not a group being complete: after #5549 it carries only the newest 200 of them.
        func complete(_ key: String, _ n: Int) -> Bool {
            Self.doneGroupComplete(key, n, groupComplete: store.groupComplete, counts: counts, filtered: filtered)
        }
        func group(_ key: String) -> DoneGroup {
            let items = byKey[key] ?? []
            let done = complete(key, items.count)
            return DoneGroup(group: InboxGroups.Group(key: key, title: InboxGroups.timeTitle(key), items: items),
                             count: filtered ? items.count : max(items.count, counts[key, default: 0]), plus: filtered && !done, more: !done,
                             range: InboxGroups.timeRange(key, now: now))
        }
        let keys = InboxGroups.sortedKeys(filtered ? Array(byKey.keys) : Array(byKey.keys) + Array(counts.keys), now: now)
        let next = filtered ? InboxGroups.sortedKeys(counts.keys, now: now).first { !complete($0, 0) } : nil
        return (keys.map(group), next.map(group))
    }

    /// "Show Older" under Done or in one of its groups: one more page. In a group it loads the first page by itself when
    /// it shows (the group opened), once: the next ones are a tap each, so opening 1+ Month doesn't stream in all of it.
    private func olderRow(loading: Bool, auto: String? = nil, run: @escaping () -> Void) -> some View {
        Button(loading ? String(localized: "Loading…") : String(localized: "Show Older"), action: run)
            .disabled(loading)
            .lineLimit(1)
            .listRowSeparator(.hidden)
            .themedRow()
            .onAppear {
                guard let auto, !autoLoaded.contains(auto) else { return }
                autoLoaded.insert(auto)
                run()
            }
    }

    /// Done as shown: the list's done items, then the history's it doesn't have (the current assistant's, as the
    /// store reads it), newest done first.
    private func doneAll(_ listed: [InboxItem]) -> [InboxItem] {
        let have = Set(listed.map(\.key))
        let filter = InboxProjection.Filter(source: source, kind: kind, query: query)
        let older = store.history.filter { $0.isDone && !have.contains($0.key) && filter.matches($0) }
        return older.isEmpty ? listed : (listed + older).sorted { ($0.doneAt ?? .distantPast) > ($1.doneAt ?? .distantPast) }
    }

    /// A group's title: it opens / closes the group (sticking at the top while its rows scroll by); selecting, its tick
    /// picks or unpicks all of it, folded or not, and "n selected" says what is picked in it.
    private func groupHeader(_ section: String, _ g: InboxGroups.Group, count: Int, more: Bool = false, pickable: Bool) -> some View {
        let open = groupOpen(section, g.key)
        let n = picked.map { p in g.items.filter { p.contains($0.key) }.count } ?? 0
        return HStack(spacing: Theme.Space.sm) {
            if picked != nil && pickable {
                Button { pickGroup(g.items) } label: {
                    ThemedIcon(n == 0 ? Theme.Icon.unchecked : n == g.items.count ? Theme.Icon.successFilled : Theme.Icon.partlyChecked)
                        .font(Theme.Font.heading)
                        .foregroundStyle(n == 0 ? Color.secondary : Theme.accent)
                        .tapTargetHeight()
                        // A finger wide too, the title where it was: hit over the header's inset and the gap before the
                        // title, whose own button, drawn after it, keeps its taps (#6658).
                        .tapTargetOutset(.horizontal)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(String(localized: "Select All in \(g.title)"))
                .accessibilityIdentifier("inbox.pickGroup")
            }
            Button {
                withAnimation(Theme.Motion.standard) { setGroupOpen(section, g.key, !open) }
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(open ? 90 : 0))
                    // the section's title size, not its weight: a level under it ("Done · 2088"), not above it (#2969)
                    Text(g.title).font(Theme.Font.caption).foregroundStyle(.secondary)
                    Text(verbatim: more ? "\(count)+" : "\(count)").font(Theme.Font.caption).foregroundStyle(.tertiary)
                    Spacer(minLength: 0)
                }
                .lineLimit(1)
                .tapTargetHeight()
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("inbox.groupTitle")
            if n > 0 { Text("\(n) Picked").font(Theme.Font.caption).foregroundStyle(Theme.accent).lineLimit(1) }
        }
        .textCase(nil)
        .listRowInsets(.vertical, Theme.Space.flush) // its buttons are the room above and below (#6576)
        .id(Self.groupID(section, g.key))
    }

    private func pickGroup(_ items: [InboxItem]) {
        guard var p = picked else { return }
        let keys = items.map(\.key)
        if keys.allSatisfy(p.contains) { p.subtract(keys) } else { p.formUnion(keys) }
        picked = p
        Haptics.tap()
    }

    // MARK: Select (#2059)

    /// What Select can pick: the open items in sight (in the sections and groups shown open; In Progress is always
    /// open). The agents waiting and the done items stay as they are, dimmed.
    private var pickable: [InboxItem] {
        let share = share
        return InboxItem.Tier.allCases.filter { folds.isOpen($0.rawValue) }.flatMap { tier -> [InboxItem] in
            let items = share.byTier[tier] ?? []
            let groups = tier == .working ? [] : groupsOf(items)
            guard titled(tier, groups: groups, agents: tier == .action ? share.agents.count : 0) else { return items }
            return groups.filter { groupOpen(tier.rawValue, $0.key) }.flatMap(\.items)
        }
    }

    /// The picked items the list still has open (one filed meanwhile, elsewhere, drops out).
    private func chosen(_ picked: Set<String>) -> [InboxItem] { openItems.filter { picked.contains($0.key) } }

    @ToolbarContentBuilder private func selectingToolbar(_ picked: Set<String>) -> some ToolbarContent {
        let pickable = pickable
        let all = !pickable.isEmpty && pickable.allSatisfy { picked.contains($0.key) }
        let chosen = chosen(picked)
        ToolbarItem(placement: .principal) {
            Text("\(chosen.count) Selected").font(Theme.Font.heading).lineLimit(1)
        }
        ToolbarItem(placement: .topBarLeading) {
            // Select All adds what is in sight (a folded group picked whole stays picked); Deselect All clears all
            Button(all ? String(localized: "Deselect All") : String(localized: "Select All")) {
                self.picked = all ? [] : picked.union(pickable.map(\.key))
            }
            .disabled(pickable.isEmpty)
            .accessibilityIdentifier("inbox.selectAll")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button("Done") { withAnimation(Theme.Motion.standard) { self.picked = nil } }
                .accessibilityIdentifier("inbox.selectDone")
        }
        // One item across the bar, so the three actions are measured together: the words where they fit, and where they do not
        // (large text, a long language) Add to To-Dos is an icon, still named for VoiceOver. Separate toolbar items would fold
        // Mark as Done into an overflow menu instead.
        ToolbarItem(placement: .bottomBar) {
            ViewThatFits(in: .horizontal) {
                pickedBar(words: true)
                pickedBar(words: false)
            }
            .frame(maxWidth: .infinity)
        }
    }

    /// The bar's three actions. Merge: the picked rows become one row (undoable); Add to To-Dos asks the due time; Mark as Done is
    /// one request with Undo on the toast (`InboxStore.setDone`). Each ends selecting when it went through.
    private func pickedBar(words: Bool) -> some View {
        let chosen = chosen(picked ?? [])
        return HStack(spacing: Theme.Space.l) {
            Button("Merge Rows") {
                store.merge(chosen) { withAnimation(Theme.Motion.standard) { self.picked = nil } }
            }
            .disabled(!InboxStore.canMerge(chosen))
            .accessibilityIdentifier("inbox.mergePicked")
            Menu {
                InboxDueButtons(add: { due in addTodos(chosen, due) }, pick: { todoRequest = InboxTodoRequest(items: chosen) { self.picked = nil } })
            } label: {
                if words {
                    Text("Add to To-Dos")
                } else {
                    Label("Add to To-Dos", systemImage: Theme.Icon.todo).labelStyle(.iconOnly)
                }
            }
            .disabled(chosen.isEmpty)
            .accessibilityLabel("Add to To-Dos")
            .accessibilityIdentifier("inbox.todoPicked")
            Spacer(minLength: 0)
            Button("Mark as Done") {
                withAnimation(Theme.Motion.standard) {
                    store.setDone(chosen, true)
                    self.picked = nil
                }
            }
            .disabled(chosen.isEmpty)
            .accessibilityIdentifier("inbox.markPickedDone")
        }
        .lineLimit(1)
        .buttonStyle(.borderless)
    }

    /// Add to To-Dos with a due: one to-do per row; selecting ends once they were added.
    private func addTodos(_ items: [InboxItem], _ due: InboxDue) {
        store.addTodos(items, due: due) { withAnimation(Theme.Motion.standard) { self.picked = nil } }
    }

    private func pick(_ it: InboxItem) {
        guard var p = picked else { return }
        if p.contains(it.key) { p.remove(it.key) } else { p.insert(it.key) }
        picked = p
        Haptics.tap()
    }

    // MARK: Clear All

    /// The names of the filters on now (Lark, a mailbox, a connector; @Me, DMs…), for what Clear All covers.
    private var filterNames: [String] {
        var names: [String] = []
        if source == "feishu" {
            names.append(String(localized: "Lark [Inbox Event Views 7e4419]"))
        } else if let a = accounts.first(where: { $0.id == source }) {
            names.append(Self.short(a.email))
        } else if let c = connectorSources.first(where: { $0.id == source }) {
            names.append(c.name)
        }
        if let k = Self.kinds.first(where: { $0.0 == kind }) { names.append(k.1) }
        return names
    }

    /// What Clear All covers: the open items the list shows now (its filters and search). The Agents
    /// waiting at the top are not inbox items: they wait for an answer, not to be filed.
    private var cleanScope: [InboxItem] { kind == "agent" ? [] : openItems }

    /// Clear All, with what it covers and how many under the name: files every open item in view as dismissed; from
    /// `InboxBulk.confirmFrom` items on it asks first. One request, Undo after (docs/assistant/inbox.md「Clean up all」).
    private var cleanButton: some View {
        let scope = cleanScope
        return Button { clean(confirmed: false) } label: {
            Label("Clean up all", systemImage: Theme.Icon.cleanUpAll)
            Text(InboxBulk.scopeLine(filters: filterNames, query: query, count: scope.count))
        }
        .disabled(scope.isEmpty)
        // A menu item's subtitle is drawn but not read out: the spoken name says what it covers and how many too.
        .accessibilityLabel(Text("Clean up all") + Text(verbatim: String(localized: ", ")) + Text(InboxBulk.scopeLine(filters: filterNames, query: query, count: scope.count)))
        .accessibilityIdentifier("inbox.cleanUpAll")
    }

    private func clean(confirmed: Bool) {
        let scope = cleanScope
        if !confirmed && InboxBulk.needsConfirm(scope.count) {
            confirming = true
            return
        }
        withAnimation(Theme.Motion.standard) { store.clean(scope) }
    }

    private var list: some View {
        let projection = itemProjection
        let openItems = projection.openItems
        let waitingAgents = waitingAgents
        // The current assistant's alone where the hub has several: no manager headings (docs/assistant/managers.md "Pages").
        let share = InboxShare(byTier: kind == "agent" ? [:] : projection.byTier, agents: waitingAgents, done: projection.doneItems)
        // The hub's merged list has nothing of an assistant that is offline: said so in place of the empty inbox.
        let offline = model.assistantManagerOffline
        return ScrollViewReader { proxy in
        List {
            filters
                .listRowInsets(EdgeInsets(top: 4, leading: 0, bottom: 8, trailing: 0))
                .listRowSeparator(.hidden)
                .themedRow()
            Picker("Group By", selection: $grouping) {
                Text("By Source").tag("")
                Text("By Time").tag(InboxGroups.Grouping.time.rawValue)
            }
            .pickerStyle(.segmented)
            .accessibilityIdentifier("inbox.grouping")
            .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: Theme.Space.sm, trailing: Theme.Space.page))
            .listRowSeparator(.hidden)
            .themedRow()
            // New items the hub holds back until the model sorts them (#1773): above the first item.
            if let line = InboxTriageText.line(store.triage) {
                InboxTriageBanner(line: line, configurable: InboxTriageText.configurable(store.triage, mayChangeSettings: !model.isDemo))
                    .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: Theme.Space.s, trailing: Theme.Space.page))
                    .listRowSeparator(.hidden)
                    .listRowBackground(Color.clear)
            }
            // Sources not read this round keep their earlier items: say so even when triage is otherwise ok (#1849).
            if let unread = InboxTriageText.unread(store.triage) {
                InboxUnreadNotice(text: unread)
                    .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: Theme.Space.s, trailing: Theme.Space.page))
                    .listRowSeparator(.hidden)
                    .listRowBackground(Color.clear)
            }
            if let error = store.error, store.items.isEmpty {
                Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    .themedRow()
            }
            if openItems.isEmpty && waitingAgents.isEmpty && !store.loading, let offline {
                AssistantOfflineNotice(manager: offline)
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            } else if openItems.isEmpty && waitingAgents.isEmpty && !store.loading {
                EmptyStateView(symbol: "checkmark.seal", title: query.isEmpty ? String(localized: "All Caught Up") : String(localized: "No Matches"),
                               message: query.isEmpty ? String(localized: "New Lark messages and emails show up here.") : String(localized: "Try a different search."))
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            // The AI's triage: Needs You (and what it has not looked at yet), FYI, Collapsed (noise); each by source or
            // by time (#2059): a group is its own section, so its title sticks while its rows scroll by.
            ForEach(InboxItem.Tier.allCases, id: \.self) { tier in
                let items = share.byTier[tier] ?? []
                let agents = tier == .action ? share.agents : []
                if !items.isEmpty || !agents.isEmpty {
                    let groups = tier == .working ? [] : groupsOf(items)
                    let header = sectionHeader(tier.title, count: items.count + agents.count, key: tier.rawValue,
                                               clear: picked == nil ? InboxPage.clearTitle(tier).map { ($0, { store.setDone(items, true) }) } : nil)
                    if titled(tier, groups: groups, agents: agents.count) {
                        Section { EmptyView() } header: { header }
                        if folds.isOpen(tier.rawValue) {
                            if !agents.isEmpty {
                                let g = InboxGroups.Group(key: "agents", title: String(localized: "Agents Waiting on You"), items: [])
                                Section { if groupOpen(tier.rawValue, g.key) { ForEach(agents) { agentRow($0) } } } header: {
                                    groupHeader(tier.rawValue, g, count: agents.count, pickable: false)
                                }
                            }
                            ForEach(groups) { g in
                                Section { if groupOpen(tier.rawValue, g.key) { ForEach(g.items, id: \.key) { row($0) } } } header: {
                                    groupHeader(tier.rawValue, g, count: g.items.count, pickable: true)
                                }
                            }
                        }
                    } else {
                        Section {
                            if folds.isOpen(tier.rawValue) {
                                ForEach(agents) { agentRow($0) }
                                ForEach(items, id: \.key) { row($0) }
                            }
                        } header: { header }
                    }
                }
            }
            // Done, by when it was done, with the hub's history after the list's few days.
            let done = doneAll(projection.sortedDoneItems)
            let days = store.doneDays
            let (dg, nextGroup) = doneGroups(done, days: days)
            let doneCount = days == nil ? done.count : dg.reduce(0) { $0 + $1.count }
            if doneCount > 0 {
                let more = days == nil ? store.historyMore && !store.history.isEmpty : nextGroup != nil || dg.contains { $0.plus }
                let count = more ? "\(doneCount)+" : "\(doneCount)"
                let filtered = !source.isEmpty || !kind.isEmpty || !query.isEmpty
                if dg.count > 1 {
                    // Its title as a row, not as the header of a section with nothing in it: as that header (with its
                    // Jump To menu, #2979) its text came out washed like a stuck title's wherever it was, and on an
                    // iPhone the scroll edge reached from the top down to it, over every row between (#4218).
                    Section {
                        doneHeader(count: count, groups: dg, proxy: proxy)
                            .listRowSeparator(.hidden)
                            .listRowBackground(Color.clear)
                            .accessibilityAddTraits(.isHeader)
                    }
                    if folds.isOpen("done") {
                        ForEach(dg) { g in
                            Section {
                                if groupOpen("done", g.id) {
                                    ForEach(g.group.items, id: \.key) { row($0) }
                                    if days != nil && g.more && !filtered {
                                        olderRow(loading: store.groupLoading.contains(g.id), auto: g.id) { store.loadDoneGroup(g.id, range: g.range) }
                                    }
                                }
                            } header: {
                                groupHeader("done", g.group, count: g.count, more: g.plus, pickable: false)
                            }
                        }
                        if days == nil && store.historyMore && !store.history.isEmpty {
                            olderRow(loading: store.historyLoading) { store.loadOlderDone() }
                        }
                        if let nextGroup {
                            olderRow(loading: store.groupLoading.contains(nextGroup.id)) { store.loadDoneGroup(nextGroup.id, range: nextGroup.range) }
                        }
                    }
                } else {
                    Section {
                        if folds.isOpen("done") { ForEach(done, id: \.key) { row($0) } }
                    } header: {
                        doneHeader(count: count, groups: [], proxy: proxy)
                    }
                }
            }
        }
        .listStyle(.plain)
        .themedPage()
        .refreshable { await store.refresh() }
        .overlay { if store.loading && store.items.isEmpty { ProgressView() } }
        // Done's day counts once the list is in, so Done lists its groups and counts before it opens (#2969); a hub
        // without them gets the history's first page instead
        .task(id: store.listed) { if store.listed { store.loadDoneDays() } }
        .onChange(of: scrollTarget) { _, id in
            guard let id else { return }
            withAnimation(Theme.Motion.standard) { proxy.scrollTo(id, anchor: .top) }
            scrollTarget = nil
        }
        }
    }

    /// Done's title: opens / closes it; "Jump To" lists its groups with their counts, and collapses or expands them all.
    private func doneHeader(count: String, groups: [DoneGroup], proxy: ScrollViewProxy) -> some View {
        let open = folds.isOpen("done")
        return HStack(spacing: Theme.Space.sm) {
            Button {
                withAnimation(Theme.Motion.standard) { folds.toggle("done") }
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    Text(verbatim: "\(String(localized: "Done [Inbox View 59f6c8]")) · \(count)").font(Theme.Font.sectionHeader).foregroundStyle(.secondary)
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                        .rotationEffect(.degrees(open ? 90 : 0))
                    Spacer(minLength: 0)
                }
                .lineLimit(1)
                .tapTargetHeight()
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("inbox.doneTitle")
            if groups.count > 1 {
                Menu {
                    ForEach(groups) { g in
                        Button { jump(to: g, proxy: proxy) } label: { Text(g.group.title); Text(verbatim: g.plus ? "\(g.count)+" : "\(g.count)") }
                    }
                    Divider()
                    Button("Collapse All") { for g in groups { setGroupOpen("done", g.id, false) } }
                    Button("Expand All") { for g in groups { setGroupOpen("done", g.id, true) } }
                } label: {
                    // a finger wide too: the two-character zh-Hans label is about 30 pt
                    Text("Jump To").font(Theme.Font.secondary).lineLimit(1)
                        .tapTargetHeight()
                        .scaledFrame(minWidth: Theme.Size.tapTarget, alignment: .trailing)
                        .frame(minWidth: Theme.Size.tapTarget, alignment: .trailing)
                        .contentShape(.rect)
                }
                .accessibilityIdentifier("inbox.jumpTo")
            }
        }
        .textCase(nil)
        .listRowInsets(.vertical, Theme.Space.flush)
        .id("|done")
    }

    /// Jump To one of Done's groups: Done and the group open, then scrolled to its first row (its title sticks above it;
    /// a List scrolls to rows, which are identified by `InboxItem.key`, not to section headers). A group none of whose
    /// items has come yet loads its first page (#2969; opening it does), then scrolls once one is there.
    private func jump(to g: DoneGroup, proxy: ScrollViewProxy) {
        if !folds.isOpen("done") { folds.toggle("done") }
        setGroupOpen("done", g.id, true)
        let key = g.id
        if g.more {
            autoLoaded.insert(key)
            store.loadDoneGroup(key, range: g.range)
        }
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(120))
            var first = g.group.items.first?.key
            // up to 5 s for its first page: then the group's first item, newest first, as Done lists it
            for _ in 0..<50 where first == nil {
                try? await Task.sleep(for: .milliseconds(100))
                if store.groupLoading.contains(key) { continue }
                first = store.history.filter { InboxGroups.timeKey($0.doneAt, now: AppClock.now, recent: true) == key }
                    .max { ($0.doneAt ?? .distantPast) < ($1.doneAt ?? .distantPast) }?.key
            }
            guard let first else { return }
            withAnimation(Theme.Motion.standard) { proxy.scrollTo(first, anchor: .top) }
        }
    }

    /// A section title that opens / closes the section, with an optional action on the right (List headers take no
    /// context menu, so it is a visible button).
    private func sectionHeader(_ title: String, count: Int, key: String, clear: (String, () -> Void)?) -> some View {
        HStack(spacing: Theme.Space.sm) {
            if InboxItem.Tier(rawValue: key)?.folds == false {
                // In Progress: always open, a plain title (inbox phase 1).
                // as tall as the other sections' titles, which are buttons
                Text("\(title) · \(count)").font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
                    .tapTargetHeight()
                Spacer(minLength: 0)
            } else {
            Button {
                withAnimation(Theme.Motion.standard) { folds.toggle(key) }
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    Text("\(title) · \(count)").font(Theme.Font.sectionHeader).foregroundStyle(key == "action" ? Color.primary : .secondary)
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                        .rotationEffect(.degrees(folds.isOpen(key) ? 90 : 0))
                    Spacer(minLength: 0)
                }
                .lineLimit(1)
                .tapTargetHeight()
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("inbox.sectionTitle")
            }
            if let clear {
                Button { withAnimation(Theme.Motion.standard) { clear.1() } } label: {
                    Text(clear.0).tapTargetHeight().contentShape(.rect)
                }
                .font(Theme.Font.secondary)
                .lineLimit(1)
                .buttonStyle(.borderless)
                .accessibilityIdentifier("inbox.clear")
            }
        }
        .textCase(nil)
        .listRowInsets(.vertical, Theme.Space.flush)
    }

    private func agentRow(_ ix: Interaction) -> some View {
        Button { open(ix) } label: {
            InboxAgentRow(interaction: ix, machine: model.machine(ix.machineRef), machineName: model.machine(ix.machineRef)?.title ?? ix.machineID)
        }
        .buttonStyle(.plain)
        .disabled(picked != nil)
        .opacity(picked != nil ? Theme.Fade.unavailable : 1)
        .modifier(InboxRowLook())
        .accessibilityIdentifier("inbox.agentRow")
    }

    /// The agent's chat (the assistant's in Chat), where the question / plan / approval waits.
    private func open(_ ix: Interaction) {
        // A card on no session has no chat: it opens by itself (#4059).
        if ix.isSessionless { answering = ix; return }
        InboxWaitingOpen.session(ix.sessionKey, currentHub: model.hub?.group, push: pushChat, open: { model.open(ix.sessionKey) })
    }

    @ViewBuilder private func row(_ it: InboxItem) -> some View {
        let content = InboxRow(item: it, showAccount: accounts.count > 1, sending: store.sending.contains(it.key),
                               send: { text in Task { await store.send(it, text: text, changes: it.agent?.changes ?? []) } }, edit: { editingDraft = it })
        if let picked {
            // Selecting (#2059): a tap anywhere on the row picks it (its draft's buttons too); a done item can't be picked.
            let on = picked.contains(it.key)
            HStack(alignment: .top, spacing: Theme.Space.m) {
                if !it.isDone {
                    ThemedIcon(on ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                        .font(Theme.Font.heading)
                        .foregroundStyle(on ? Theme.accent : Color.secondary)
                        .scaledFrame(height: Theme.Size.avatar)
                }
                content.allowsHitTesting(false)
            }
            .contentShape(.rect)
            .onTapGesture { if !it.isDone { pick(it) } }
            .opacity(it.isDone ? Theme.Fade.unavailable : 1)
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(it.isDone ? [] : on ? [.isButton, .isSelected] : .isButton)
            .accessibilityIdentifier("inbox.pickRow")
            .modifier(InboxRowLook())
        } else {
        content
            .background { NavigationLink(value: it) { EmptyView() }.opacity(0) }
            .swipeActions(edge: .trailing, allowsFullSwipe: true) {
                if it.isDone {
                    Button { store.setDone(it, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }.tint(Theme.accent)
                } else {
                    Button { store.setDone(it, true) } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }.tint(Theme.positive)
                }
            }
            .contextMenu { rowMenu(it) }
            .modifier(InboxRowLook())
        }
    }

    /// A row's long-press menu: Done (or Move Back), then what the item can be muted by — its repository, chat or sender,
    /// the name under each (docs/assistant/inbox.md「Muting」) — then a rule for each of those ("Policies").
    @ViewBuilder private func rowMenu(_ it: InboxItem) -> some View {
        if it.isDone {
            Button { store.setDone(it, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }
        } else {
            Button { store.setDone(it, true) } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }
            Menu {
                InboxDueButtons(add: { store.addTodos([it], due: $0) }, pick: { todoRequest = InboxTodoRequest(items: [it]) })
            } label: { Label("Add to To-Dos", systemImage: Theme.Icon.todo) }
        }
        if let mutes = it.mutes, !mutes.isEmpty {
            Section {
                ForEach(mutes, id: \.self) { m in
                    Button { store.mute(it, m) } label: {
                        Label(m.title, systemImage: Theme.Icon.mute)
                        Text(m.shortName)
                    }
                }
            }
            Section { InboxRuleButtons(item: it) { ruling = $0 } }
        }
    }

    // MARK: Filters

    /// Sources (all, Lark, each mail account) then kinds, as one row of chips; in the BeautifulUI style its filter chips,
    /// wrapping to a second row (no sideways scrolling at the top of a page, like the Agents list).
    @ViewBuilder private var filters: some View {
        if AppStyle.current.usesBeautifulUI {
            LarkFlowLayout(spacing: Theme.Space.xs, lineSpacing: Theme.Space.sm) {
                filterChip(String(localized: "All"), on: source.isEmpty && kind.isEmpty) { source = ""; kind = "" }
                filterChip(String(localized: "Lark [Inbox Event Views 7e4419]"), on: source == "feishu") { source = source == "feishu" ? "" : "feishu" }
                ForEach(accounts) { a in
                    filterChip(Self.short(a.email), on: source == a.id) { source = source == a.id ? "" : a.id }
                }
                ForEach(connectorSources, id: \.id) { c in
                    filterChip(c.name, on: source == c.id) { source = source == c.id ? "" : c.id }
                }
                ForEach(Self.kinds, id: \.0) { k, title in
                    filterChip(title, on: kind == k) { kind = kind == k ? "" : k }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.l)
        } else {
            plainFilters
        }
    }

    private func filterChip(_ title: String, on: Bool, action: @escaping () -> Void) -> some View {
        BUIFilterChip(title, isActive: on) {
            withAnimation(Theme.Motion.standard) { action() }
            Haptics.tap()
        }
    }

    private var plainFilters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.s) {
                chip(String(localized: "All"), on: source.isEmpty && kind.isEmpty) { source = ""; kind = "" }
                chip(String(localized: "Lark [Inbox Event Views 7e4419]"), symbol: Theme.Icon.feishuChats, on: source == "feishu") { source = source == "feishu" ? "" : "feishu" }
                ForEach(accounts) { a in
                    chip(Self.short(a.email), symbol: Theme.Icon.mail, on: source == a.id) { source = source == a.id ? "" : a.id }
                }
                ForEach(connectorSources, id: \.id) { c in
                    chip(c.name, symbol: Theme.Icon.connectors, on: source == c.id) { source = source == c.id ? "" : c.id }
                }
                Divider().scaledFrame(height: Theme.Size.badge)
                ForEach(Self.kinds, id: \.0) { k, title in
                    chip(title, on: kind == k) { kind = kind == k ? "" : k }
                }
            }
            .padding(.horizontal, Theme.Space.l)
        }
    }

    private func chip(_ title: String, symbol: String? = nil, on: Bool, action: @escaping () -> Void) -> some View {
        Button {
            withAnimation(Theme.Motion.standard) { action() }
            Haptics.tap()
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if let symbol { ThemedIcon(symbol).font(Theme.Font.label) }
                Text(title)
            }
            .font(on ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            .lineLimit(1)
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.sm)
            .foregroundStyle(on ? Color.white : Color.primary)
            .background(on ? AnyShapeStyle(Theme.accent) : AnyShapeStyle(Color.secondary.opacity(0.12)), in: .capsule)
        }
        .buttonStyle(.plain)
    }

    /// A section's clear-all on the right: mark all seen (FYI), clear all (Collapsed); none for Needs You and In Progress (the AI is
    /// on those).
    nonisolated static func clearTitle(_ tier: InboxItem.Tier) -> String? {
        switch tier {
        case .action, .working: nil
        case .fyi: String(localized: "Mark All Seen")
        case .noise: String(localized: "Clear All")
        }
    }

    /// The part of an address before the @ (chips stay short), unless that alone says nothing ("c@…").
    static func short(_ email: String) -> String {
        let name = String(email.split(separator: "@").first ?? Substring(email))
        return name.count >= 3 ? name : email
    }

    private func loadAccounts() async {
        guard let a = await InboxMailAccounts.read(model) else { return }
        accounts = a
    }
}

// MARK: - Row

struct InboxRow: View {
    var item: InboxItem
    var showAccount = false
    var sending = false
    /// Send / Edit on the AI twin's draft (the list only).
    var send: ((String) -> Void)?
    var edit: (() -> Void)?

    @StoredValue(TranslationPrefs.targetKey) private var translateTarget = TranslationPrefs.defaultTarget
    @StoredValue(TranslationPrefs.enabledKey) private var translating = false

    /// Settings › Translation: the hub's translation of foreign mail, done as it came in (subject and preview).
    private var translated: InboxItem.Translated? { translating && TranslationPrefs.mail ? item.translated(translateTarget) : nil }

    /// Where the text column starts inside the row (40 avatar + 12): the separator lines up with it.
    static let textColumn: CGFloat = 52

    var body: some View {
        let now = self.now
        HStack(alignment: .top, spacing: Theme.Space.m) {
            avatar
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(translated.map(\.subject).flatMap { $0.isEmpty ? nil : $0 } ?? item.title).font(item.isDone ? Theme.Font.body : Theme.Font.heading).foregroundStyle(item.isDone ? .secondary : .primary).lineLimit(1)
                    if translated != nil {
                        ThemedIcon(Theme.Icon.translate).font(Theme.Font.label).foregroundStyle(.tertiary)
                            .accessibilityLabel("Translated")
                    }
                    if let label = item.kindLabel {
                        Chip(text: label, look: item.kind == "mention" || item.kind == "doc" && label == String(localized: "@Me") ? .tone(StatusPalette.needsYou) : .neutral)
                    }
                    Spacer(minLength: 4)
                    Text(LarkTime.short(item.updatedAt)).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize()
                }
                HStack(alignment: .top, spacing: Theme.Space.sm) {
                    if let summary = item.summary, !summary.isEmpty {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + summary))
                                // The hierarchical styles, not `Color.primary`: a Mac list's selected row turns that
                                // white, where the row sets the words' color for these (MacHoverRow).
                                .font(Theme.Font.secondary)
                                .foregroundStyle(item.isDone ? AnyShapeStyle(.secondary) : AnyShapeStyle(.primary.opacity(0.9))).lineLimit(2)
                            if !item.repeatsSummary { Text(from).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1) }
                        }
                    } else {
                        Text(from).font(Theme.Font.secondary).lineHeight(.secondary).foregroundStyle(.secondary).lineLimit(2)
                    }
                    Spacer(minLength: 0)
                    if item.count > 1 && !item.isDone {
                        // @Me is the accent (it is about you); any other count is neutral.
                        CountBadge(count: item.count, look: item.kind == "mention" ? .accent : .neutral)
                    }
                }
                if item.doc != nil && !item.isDone {
                    InboxDocRowsPreview(item: item).padding(.top, Theme.Space.xxs)
                }
                if showAccount && !item.isLark {
                    Text(item.accountName).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                if let merged = item.mergedFrom?.count, merged > 0 {
                    // Rows merged into this one (#6480): plain grey small text
                    Text("Merged from \(merged + 1) rows").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                        .accessibilityIdentifier("inbox.mergedFrom")
                }
                if let now, !item.isDone {
                    nowLine(now)
                } else if let mark = InboxEventText.mark(item) {
                    // Inbox phase 1: the suggestion (it replaces the AI agent's draft card, which a hub mirrors it
                    // into), In Progress's state, the 'preparing a suggestion' line; its buttons are in the detail.
                    InboxEventMarkLine(mark: mark).padding(.top, Theme.Space.xxs)
                } else if !item.isDone && item.suggestion == nil && item.handling == nil {
                    if let draft = item.agentDraft, let send {
                        InboxDraftCard(draft: draft, changes: (item.agent?.changes ?? []).map { DocChangeText.line($0, in: item.doc) },
                                       sending: sending, send: { send(draft) }, edit: edit)
                            .padding(.top, Theme.Space.sm)
                    } else if item.agentWorking {
                        HStack(spacing: Theme.Space.sm) {
                            InboxSpinner()
                            Text(item.agent?.state == "queued" ? String(localized: "AI twin is queued") : item.agent?.task.map { String(localized: "AI twin is working: \($0)") } ?? String(localized: "AI twin is working"))
                                .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                        }
                        .padding(.top, Theme.Space.xxs)
                    }
                }
            }
        }
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
    }

    /// The shared state key wins over the old mark. The frozen Mac list keeps its existing row and mark.
    private var now: String? {
        #if os(macOS)
        nil
        #else
        InboxNow.text(for: item)
        #endif
    }

    /// The same state line as the other clients: waiting has a still clock, only AI work spins, and anything that
    /// needs the owner carries the existing Needs you mark. FYI and folded states do not spin.
    private func nowLine(_ words: String) -> some View {
        let symbol = InboxNow.symbol(for: item)
        return HStack(spacing: Theme.Space.sm) {
            InboxNowSymbol(symbol: symbol)
            Text(words).font(Theme.Font.label)
                .foregroundStyle(symbol == .failed ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.secondary))
                .lineLimit(1)
        }
        .padding(.top, Theme.Space.xxs)
    }

    /// Who wrote what (under the AI's summary once it has one).
    private var from: String {
        if item.isAssistantRow { return item.preview } // the tag says who: no "Assistant:" in front
        #if !os(macOS)
        if let meta = InboxNow.meta(item, lang: interfaceTextLocale.identifier) { return meta }
        #endif
        if let doc = item.doc { return doc.typeName + " · " + item.preview } // the preview names who: Li Lei mentioned you
        if item.moment != nil { return String(localized: "\(item.sender ?? ""): \(item.preview)", comment: "Who wrote it: what they wrote") } // who commented, on the post in the title
        if let c = item.connector { // example-org/example-repo#12 · bob: please take a look
            let who = [item.sender ?? "", item.preview].filter { !$0.isEmpty }.joined(separator: String(localized: ": ", comment: "Separator between a label and its value"))
            return [c.reference ?? "", who].filter { !$0.isEmpty }.joined(separator: " · ")
        }
        if let t = translated?.preview, !t.isEmpty, let sender = item.sender, !sender.isEmpty { return String(localized: "\(sender): \(t)", comment: "Who wrote it: what they wrote") }
        guard let sender = item.sender, !sender.isEmpty, item.lark?.isGroup != false || !item.isLark else { return item.preview }
        return String(localized: "\(sender): \(item.preview)", comment: "Who wrote it: what they wrote")
    }

    private var avatar: some View {
        Group {
            if item.isAssistantRow {
                // The assistant's own row wears the assistant's avatar, with no source badge.
                PersonaAvatar(size: Theme.Size.avatar).opacity(item.isDone ? 0.6 : 1)
            } else {
                sourceAvatar
            }
        }
    }

    private var sourceAvatar: some View {
        ZStack(alignment: .bottomTrailing) {
            Group {
            if let doc = item.doc {
                InboxDocIcon(doc: doc)
            } else if item.moment != nil {
                RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
                    .fill((LarkColor.text("orange") ?? Theme.accent).opacity(0.15))
                    .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
                    .overlay(ThemedIcon(Theme.Icon.moments).font(Theme.Font.bodyStrong).foregroundStyle(LarkColor.text("orange") ?? Theme.accent))
            } else if item.isLark {
                LarkAvatarImage(name: item.title, url: item.avatar, isGroup: item.lark?.isGroup ?? false, size: Theme.Size.avatar)
            } else {
                let disc = Theme.monogram(item.sender ?? "")
                Monogram(name: item.sender ?? "", fill: disc.fill, ink: disc.ink)
            }
            }
            .opacity(item.isDone ? 0.6 : 1)
            // The source: Lark's (or a connector's: GitHub's, a template image, in black) own mark on white; mail, or a
            // connector without a mark, a neutral gray, not the accent.
            Group {
                if let brand = item.isLark ? Theme.Brand.lark : item.connector != nil ? ConnectorText.brand(item.source) : nil {
                    BrandBadge(brand)
                } else {
                    ThemedIcon(item.connector != nil ? Theme.Icon.connectors : Theme.Icon.mailFilled)
                        .font(Theme.Font.labelHeavy).imageScale(.small).dynamicTypeSize(...DynamicTypeSize.large)
                        .foregroundStyle(.white)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                        .background(Color(uiColor: .systemGray), in: .circle)
                }
            }
            .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
            .overlay(Circle().stroke(Theme.pageBackground, lineWidth: 1.5))
            .offset(x: 4, y: 4)
        }
        .scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
    }
}

/// The AI twin's reply under an inbox row: one tap sends it as it is, Edit opens it in the draft sheet.
struct InboxDraftCard: View {
    var draft: String
    /// Status moves made with it (document items), one line each.
    var changes: [String] = []
    var sending: Bool
    var send: () -> Void
    var edit: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Label("AI Twin's Draft Reply", systemImage: Theme.Icon.ai)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(Theme.ai)
                .lineLimit(1)
            Text(draft)
                .font(Theme.Font.secondary)
                .foregroundStyle(.primary)
                .lineLimit(4)
                .frame(maxWidth: .infinity, alignment: .leading)
            ForEach(changes, id: \.self) { c in
                Label(c, systemImage: Theme.Icon.inProgress)
                    .font(Theme.Font.captionMedium).foregroundStyle(.secondary).lineLimit(1)
            }
            if AppStyle.current.usesBeautifulUI { beautifulActions } else { plainActions }
        }
        .padding(Theme.Space.m)
        .modifier(DraftSurface())
    }

    /// BeautifulUI's RecommendationCard actions: Edit secondary, Send the accent.
    private var beautifulActions: some View {
        HStack(spacing: Theme.Space.s) {
            Spacer(minLength: 0)
            if let edit {
                Button("Edit [Inbox Event Views 37090f]", action: edit).buttonStyle(.bui(.secondary)).lineLimit(1)
            }
            Button(action: send) {
                HStack(spacing: Theme.Space.xs) {
                    if sending { BUISpinner(size: Theme.Size.dotXL) }
                    Text("Send").lineLimit(1)
                }
            }
            .buttonStyle(.bui(.accent))
            .disabled(sending)
            .accessibilityLabel("Send the AI twin's draft reply")
        }
    }

    private var plainActions: some View {
        HStack(spacing: Theme.Space.s) {
            Spacer(minLength: 0)
            if let edit {
                Button(action: edit) {
                    Text("Edit [Inbox Event Views 37090f]").font(Theme.Font.secondaryMedium).lineLimit(1)
                        .padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.sm)
                        .foregroundStyle(Theme.accent)
                        .background(Theme.accent.opacity(0.12), in: .capsule)
                }
                .buttonStyle(.borderless)
            }
            Button(action: send) {
                HStack(spacing: Theme.Space.xs) {
                    if sending { ProgressView().controlSize(.mini).tint(.white) } else { ThemedIcon(Theme.Icon.sendMail).font(Theme.Font.label) }
                    Text("Send")
                }
                .font(Theme.Font.secondaryStrong).lineLimit(1)
                .padding(.horizontal, Theme.Space.l).padding(.vertical, Theme.Space.sm)
                .foregroundStyle(.white)
                .background(Theme.accent, in: .capsule)
            }
            .buttonStyle(.borderless)
            .disabled(sending)
            .accessibilityLabel("Send the AI twin's draft reply")
        }
    }
}

/// The draft's card: the accent's tint with a hairline; in the BeautifulUI style the field gray (RecommendationCard).
struct DraftSurface: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content.background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        } else {
            content
                .background(Theme.accent.opacity(0.07), in: .rect(cornerRadius: Theme.Radius.card))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card).stroke(Theme.accent.opacity(0.18), lineWidth: 0.5))
        }
    }
}

/// An agent waiting on the user, among the inbox rows: which agent, what it asks, then what it waits for (red, as
/// Needs You is everywhere) and on which machine. The label sits under the title so the title keeps the width.
struct InboxAgentRow: View {
    var interaction: Interaction
    var machine: Machine?
    var machineName: String

    var body: some View {
        let ix = interaction
        // What it asks, by card kind (the plan's title, the terminal dialog's question); older hubs that filled
        // neither keep the old summary / first-question fallback.
        let asks = ix.askLine(includeToolName: false) ?? ix.summary ?? ix.questions?.first?.question
        HStack(alignment: .top, spacing: Theme.Space.m) {
            // The rounded square of the Agents rows (SessionAvatar), with what kind of request it is.
            SessionAvatar(machine: machine, state: .needsYou, size: 40, symbol: ix.symbol, dot: false)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(ix.sessionTitle?.isEmpty == false ? ix.sessionTitle! : (ix.cwd.map { URL(fileURLWithPath: $0).lastPathComponent } ?? "Agent"))
                        .font(Theme.Font.heading).lineLimit(1)
                    Spacer(minLength: 4)
                    Text(LarkTime.short(ix.createdAt)).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize()
                }
                if let asks, !asks.isEmpty {
                    Text(asks).font(Theme.Font.secondary).foregroundStyle(.primary.opacity(0.9)).lineLimit(2) // see InboxRow
                    (Text(ix.title).fontWeight(.semibold).foregroundStyle(StatusPalette.needsYou.text)
                        + Text(" · " + machineName).foregroundStyle(.secondary))
                        .font(Theme.Font.caption).lineLimit(1)
                } else {
                    Text(ix.title).font(Theme.Font.secondaryStrong).foregroundStyle(StatusPalette.needsYou.text).lineLimit(1)
                    Text(machineName).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
        .padding(.vertical, Theme.Space.m)
        .contentShape(.rect)
    }
}

// MARK: - Detail

/// One inbox item opened: on iOS a native chat or mail stays behind View original until asked for, so the detail alone
/// reports nothing read; the other sources keep their existing body (placement inbox_item, `InboxItemSlotContent`). The
/// AI's summary with AI Draft Reply / Hand to AI is on top, Done in the toolbar, whoever draws the body.
struct InboxDetailView: View {
    var item: InboxItem
    var accounts: [MailAccount]
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var drafting = false
    @State private var handingOff = false
    @State private var sameMatter = false
    @State private var ruling: InboxPolicyTarget?
    @State private var todoRequest: InboxTodoRequest?
    @State private var slot: InboxItemSlotModel
    #if os(iOS)
    /// The expanded item's key; nil keeps every newly opened or reused detail collapsed.
    @State private var expandedSourceKey: String?
    #endif

    init(item: InboxItem, accounts: [MailAccount]) {
        self.item = item
        self.accounts = accounts
        _slot = State(initialValue: InboxItemSlotModel(item: item))
    }

    private var current: InboxItem { InboxStore.shared.items.first { $0.key == item.key } ?? item }

    var body: some View {
        page
            .sheet(isPresented: $drafting) {
                InboxDraftSheet(item: current, accounts: accounts, onSent: { dismiss() })
            }
            .sheet(isPresented: $handingOff) { InboxHandOffSheet(item: current) }
            .sheet(isPresented: $sameMatter) { InboxSameMatterPicker(item: current, tree: nil) }
            .sheet(item: $ruling) { InboxPolicySheet(target: $0) }
            .sheet(item: $todoRequest) { InboxDueSheet(request: $0) }
            #if os(iOS)
            .onChange(of: item.key) { _, _ in
                expandedSourceKey = nil
            }
            #endif
            #if DEBUG
            // `-showInboxAction draft|handoff`: open that sheet (screenshots).
            .task {
                switch UserDefaults.standard.string(forKey: "showInboxAction") {
                case "draft": drafting = true
                case "handoff": handingOff = true
                default: break
                }
            }
            #endif
            .pageToolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        if !current.isDone {
                            // Add to To-Dos (#6480): the row counts as handled, so the page closes like Done
                            Menu {
                                InboxDueButtons(add: { InboxStore.shared.addTodos([current], due: $0) { dismiss() } },
                                                pick: { todoRequest = InboxTodoRequest(items: [current]) { dismiss() } })
                            } label: { Label("Add to To-Dos", systemImage: Theme.Icon.todo) }
                        }
                        if let mutes = current.mutes, !mutes.isEmpty {
                            InboxRuleButtons(item: current) { ruling = $0 }
                        }
                        Button { sameMatter = true } label: { Label("Same matter as…", systemImage: Theme.Icon.link) }
                            .accessibilityIdentifier("inbox.detail.sameMatter")
                    } label: { ThemedIcon(Theme.Icon.moreMenu) }
                        .accessibilityLabel("More")
                        .accessibilityIdentifier("inbox.detail.more")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    if current.isDone {
                        Button { InboxStore.shared.setDone(current, false) } label: { Label("Move Back", systemImage: Theme.Icon.undo) }
                    } else {
                        Button {
                            InboxStore.shared.setDone(current, true)
                            dismiss()
                        } label: { Label("Done [Inbox View 59f6c8]", systemImage: Theme.Icon.done) }
                            .accessibilityIdentifier("inbox.detail.done")
                    }
                }
            }
    }

    /// An event (inbox phase 1: recap, a suggestion, the chain) opens as its own page, the source behind 'view original';
    /// anything else keeps the actions over its body, with a native chat or mail collapsed on iOS until its row is tapped.
    @ViewBuilder private var page: some View {
        if current.isEvent {
            InboxEventPage(item: current, accounts: accounts, draft: { drafting = true })
                .navigationTitle(current.title)
                .navigationBarTitleDisplayMode(.inline)
        } else {
            InboxItemSlotContent(item: current, slot: slot) { content }
                .safeAreaInset(edge: .top, spacing: 0) {
                    InboxActionBar(item: current, draft: { drafting = true }, handOff: { handingOff = true })
                }
        }
    }

    @ViewBuilder private var content: some View {
        if current.doc != nil {
            InboxDocDetailView(item: current)
        } else if current.connector != nil {
            InboxConnectorDetailView(item: current)
        } else if let m = current.moment {
            LarkMomentsPostView(postID: m.postID, pushed: true, manager: model.linkManager(current.manager)) // the post and its comments, as a shared Moments card opens
        } else if item.larkConversation != nil || item.mailSummary != nil {
            #if os(iOS)
            collapsibleSource
            #else
            originalSource
            #endif
        } else {
            EmptyStateView(symbol: "questionmark", title: item.title, message: item.preview)
        }
    }

    /// The unchanged source, shared by the platforms that open it directly and by iOS once it is explicitly expanded.
    @ViewBuilder private var originalSource: some View {
        if let c = item.larkConversation {
            LarkChatView(conversation: c, manager: model.linkManager(current.manager))
        } else if let m = item.mailSummary {
            MailDetailView(summary: m, accounts: accounts)
        }
    }

    #if os(iOS)
    private var showsOriginalSource: Bool {
        expandedSourceKey == item.key
    }

    /// Only the expanded branch mentions the source views: `InboxItemSlotContent` can evaluate its plain content eagerly,
    /// so neither a chat nor a mail reader may be constructed while this row is collapsed.
    @ViewBuilder private var collapsibleSource: some View {
        Group {
            if showsOriginalSource {
                originalSource
            } else {
                // The chat owns its title only while expanded; the collapsed detail must keep naming the item.
                Spacer()
                    .navigationTitle(current.title)
                    .navigationBarTitleDisplayMode(.inline)
            }
        }
        .safeAreaInset(edge: .top, spacing: Theme.Space.flush) { sourceDisclosure }
    }

    /// The one entry stays above the source in both states; its preview follows the inbox row's metadata and sender line.
    private var sourceDisclosure: some View {
        Button(action: toggleOriginalSource) {
            HStack(spacing: Theme.Space.s) {
                Text(sourcePreview)
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                Label(showsOriginalSource ? String(localized: "Collapse") : String(localized: "View original"), systemImage: Theme.Icon.openDocument)
                    .font(Theme.Font.captionMedium)
                    .foregroundStyle(Theme.accent)
                    .lineLimit(1)
                    .fixedSize()
                    .layoutPriority(1)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, Theme.Space.page)
            .tapTargetHeight()
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("inbox.detail.source")
        .background(.bar)
        .overlay(alignment: .bottom) { Divider() }
    }

    private var sourcePreview: String {
        if let meta = InboxNow.meta(item, lang: interfaceTextLocale.identifier) { return meta }
        guard let sender = item.sender, !sender.isEmpty, item.lark?.isGroup != false || !item.isLark else { return item.preview }
        return String(localized: "\(sender): \(item.preview)", comment: "Who wrote it: what they wrote")
    }

    private func toggleOriginalSource() {
        withAnimation(Theme.Motion.standard) {
            expandedSourceKey = showsOriginalSource ? nil : item.key
        }
    }
    #endif
}

/// Values a destination needs directly: an outer NavigationStack / NavigationSplitView does not reliably forward them
/// to pushed pages on iPad. Both are optional on Mac, Vision and the classic layouts, preserving their fallbacks.
struct InboxDestinationEnvironment: ViewModifier {
    var chat: ((InboxChatRoute) -> Void)? = nil
    var openFeishu: ((FeishuRoute) -> Void)? = nil

    func body(content: Content) -> some View {
        content
            .environment(\.pushInboxChat, chat)
            .environment(\.feishuOpen, openFeishu)
    }
}

/// The pages the inbox opens, in either layout (`SplitNavigation`).
struct InboxDestinations: ViewModifier {
    var accounts: [MailAccount]
    var inboxChat: ((InboxChatRoute) -> Void)? = nil
    var openFeishu: ((FeishuRoute) -> Void)? = nil
    @Environment(AppModel.self) private var model
    @Environment(\.feishuOpen) private var inheritedFeishuOpen
    private var routeOpen: ((FeishuRoute) -> Void)? { openFeishu ?? inheritedFeishuOpen }

    // Links on these pages open in the app (feishuHandlesLinks) wherever the stack is: the shell pushes them onto its
    // own stack, where the inbox page's handler does not reach. Those on an item's pages, and on a document or post
    // opened from one, read through the item's manager.
    func body(content: Content) -> some View {
        content
            // Keyed by the item, as on the Mac: in the split the list's links replace the detail column's page, and an
            // unkeyed one is updated in place with the last item's state — a Feishu chat kept the chat before's messages
            // and draft under the new chat's title (#885).
            .navigationDestination(for: InboxItem.self) {
                InboxDetailView(item: $0, accounts: accounts).id($0.id).splitDetailPage()
                    .feishuHandlesLinks(routeOpen, manager: model.linkManager($0.manager))
                    .modifier(InboxDestinationEnvironment(chat: inboxChat, openFeishu: routeOpen))
            }
            .navigationDestination(for: FeishuRoute.self) {
                FeishuRouteView(route: $0).splitDetailPage().feishuHandlesLinks(routeOpen, manager: $0.manager)
                    .modifier(InboxDestinationEnvironment(chat: inboxChat, openFeishu: routeOpen))
            }
            // view original and the chain's links of an event (inbox phase 1).
            .navigationDestination(for: InboxSourceRoute.self) {
                InboxSourcePage(route: $0, accounts: accounts).splitDetailPage()
                    .feishuHandlesLinks(routeOpen, manager: model.linkManager($0.item.manager))
                    .modifier(InboxDestinationEnvironment(chat: inboxChat, openFeishu: routeOpen))
            }
            .extensionPushDestination()
    }
}

/// An inbox row on the list: in the BeautifulUI style a card of its own on the page (its surface), like the Agents
/// list's capsules, no list background or line; otherwise the plain row with its line from the text's column. The
/// swipe actions stay the list row's either way.
private struct InboxRowLook: ViewModifier {
    func body(content: Content) -> some View {
        if AppStyle.current.usesBeautifulUI {
            content
                .padding(.horizontal, Theme.Space.m)
                .card(padding: 0)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.l, bottom: Theme.Space.xs, trailing: Theme.Space.l))
        } else {
            content
                .listRowInsets(EdgeInsets(top: 0, leading: 16, bottom: 0, trailing: 12))
                .themedRow()
                .alignmentGuide(.listRowSeparatorLeading) { _ in InboxRow.textColumn }
        }
    }
}

/// A group of Done (#2969): its items loaded so far, its count (the hub's when it has day counts), whether the count reads
/// "n+" (`plus`), whether more of its items can load (`more`) and its time range (where its pages start).
private struct DoneGroup: Identifiable {
    var group: InboxGroups.Group
    var count: Int
    var plus: Bool
    var more: Bool
    var range: (start: Date?, end: Date?)
    var id: String { group.key }
}
