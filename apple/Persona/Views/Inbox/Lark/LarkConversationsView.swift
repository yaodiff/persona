import SwiftUI

#if os(iOS) || os(visionOS)
/// One extra entry, not a second messaging implementation: destinations are the inbox's native chat.
struct LarkConversationsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let config = model.config {
            LarkAccountsPage(config: config).id(config)
        }
    }
}

/// The Feishu accounts on the hub's Lark gateway (docs/extensions/feishu.md「Multiple accounts」): with more than one signed in, chips over the
/// list pick whose chats it shows; each account keeps its own list and cache.
private struct LarkAccountsPage: View {
    @State private var choice: LarkAccountChoice

    init(config: ServerConfig) {
        _choice = State(initialValue: LarkAccountChoice(deps: .live(config)))
    }

    var body: some View {
        LarkConversationsPage(config: choice.config, choice: choice).id(choice.requestAccount)
    }
}

private struct LarkConversationsPage: View {
    @Environment(AppModel.self) private var model
    @Environment(\.shell) private var shell
    /// iPhone Duo's bars down the side: the shell's bar is the side rail there (`ShellSideRail`).
    @Environment(\.shellRailEdge) private var railEdge
    @Environment(\.pageTabSwitch) private var pageTabSwitch
    @Environment(\.scenePhase) private var phase
    @State private var store: LarkConversationsModel
    @State private var path: [LarkConversation] = []
    @State private var query = ""
    @State private var pins = LarkPinnedInteraction()
    @State private var canExpandPins = false
    @State private var scroll = LarkConversationScroll()
    /// The end of the list (the spinner) is on screen: pages keep coming while it stays there.
    @State private var endVisible = false
    /// State stays live across a scan's awaits, including when the shell retains a hidden tab.
    @State private var pageVisible = false
    @State private var sceneActive = false
    /// Pages a search has asked for since the query last changed (`searchPageLimit`).
    @State private var searchPages = 0
    var choice: LarkAccountChoice

    /// A search reads on through this many pages by itself, then asks before going further back (#2823).
    private static let searchPageLimit = 8
    /// At least this long between two refreshes of the list: a busy account sends an event for every message (#2823),
    /// and one that page 1 cannot answer reads every loaded page again.
    private static let refreshGap: Duration = .seconds(1)
    /// The next page is asked for when the row this far from the end comes on screen.
    private static let prefetchRows = 10

    init(config: ServerConfig, choice: LarkAccountChoice) {
        self.choice = choice
        _store = State(initialValue: LarkConversationsModel(deps: .live(config, account: choice.requestAccount,
                                                                          accountName: choice.selectedName)))
    }
    private var display: LarkMessageDisplay { model.hub?.larkDisplay.policy ?? .hidden }

    private var filtered: [LarkConversation] {
        LarkConversationsModel.listed(store.conversations, pinned: store.filter == .all ? store.pinned : [],
                                     query: query, more: store.nextCursor != nil)
    }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollViewReader { proxy in
            List {
                if query.isEmpty, choice.accounts.count > 1 {
                    LarkAccountChips(choice: choice)
                        .listRowInsets(EdgeInsets(top: Theme.Space.s, leading: 0, bottom: Theme.Space.s, trailing: 0))
                        .listRowSeparator(.hidden)
                        .themedRow()
                }
                if store.filter == .all, query.isEmpty, !store.pinned.isEmpty {
                    LarkPinnedShelf(conversations: store.pinned, interaction: $pins, expandable: $canExpandPins) { path.append($0) }
                        .id("feishu-pins")
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { scroll.shelfChanged($0) }
                        .background(LarkConversationScroll.Finder(scroll: scroll))
                        .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: 0, trailing: Theme.Space.page))
                        .listRowSeparator(.hidden)
                        .themedRow()
                }
                Section {
                    if store.conversations.isEmpty, store.nextCursor == nil {
                        empty
                            .listRowSeparator(.hidden)
                    } else {
                        // Once per update: with a query it filters every loaded chat (`listed`), and three places read it.
                        let rows = filtered
                        if store.failed {
                            Button { Task { await store.refresh() } } label: {
                                Label(store.needsUpgrade ? String(localized: "Update the hub to sync pinned chats · Check Again") : String(localized: "Not updated · Retry"), systemImage: Theme.Icon.retry)
                                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                            }
                            .listRowSeparator(.hidden)
                        }
                        let prefetch = query.isEmpty && store.filter == .all ? rows.dropLast(Self.prefetchRows).last?.id : nil
                        ForEach(rows) { conversation in
                            NavigationLink(value: conversation) { LarkConversationRow(conversation: conversation, display: display) }
                                .accessibilityIdentifier("feishu-chat-" + conversation.id)
                                .id(conversation.id)
                                .onGeometryChange(for: CGFloat.self) { $0.frame(in: .global).minY } action: { scroll.reader.row(conversation.id, top: $0) }
                                .onDisappear { scroll.reader.gone(conversation.id) }
                                .onScrollVisibilityChange(threshold: 0.01) { visible in
                                    if visible, conversation.id == prefetch { Task { await store.more() } }
                                }
                                .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.page, bottom: 0, trailing: Theme.Space.page))
                                .themedRow()
                        }
                        if rows.isEmpty, !query.isEmpty, !searchingOn {
                            EmptyStateView(symbol: Theme.Icon.search, title: String(localized: "No Results for “\(query)”"),
                                           message: store.nextCursor == nil ? String(localized: "You can search chat names and recent messages. Try another word.")
                                                                            : String(localized: "Searched the latest \(store.conversations.count) chats."))
                                .listRowSeparator(.hidden)
                                .listRowBackground(Color.clear)
                        }
                        if store.nextCursor != nil {
                            Group {
                                if store.pageFailed {
                                    Button("Retry [Lark Conversations View 8924c7]") { Task { await store.more() } }.lineLimit(1)
                                } else if scansPages, searchPages >= Self.searchPageLimit {
                                    Button(store.filter == .unread ? String(localized: "lark.filter.continue") : String(localized: "Search Older Chats")) {
                                        searchPages = 0; endVisible = true; resumePaging()
                                    }.lineLimit(1)
                                    .accessibilityIdentifier("feishu-filter-continue")
                                } else {
                                    VStack(spacing: Theme.Space.xs) {
                                        ProgressView()
                                        if !query.isEmpty {
                                            Text("Searched \(store.conversations.count) chats, looking further back")
                                                .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                                        }
                                    }
                                    .frame(maxWidth: .infinity)
                                    .padding(.vertical, Theme.Space.s)
                                    // Loads while it is on screen and goes on while it stays there (a short search's
                                    // results leave it in view); a task keyed on the cursor ran off screen too (#1034).
                                    .onScrollVisibilityChange(threshold: 0.01) { visible in
                                        endVisible = visible
                                        if visible { resumePaging() }
                                    }
                                    .onDisappear { endVisible = false }
                                }
                            }
                            .frame(maxWidth: .infinity)
                            .listRowSeparator(.hidden)
                        }
                    }
                } header: {
                    filters(proxy: proxy)
                        .id("feishu-filters")
                        .textCase(nil)
                        .listRowInsets(EdgeInsets())
                }
            }
            .listStyle(.plain)
            .listSectionSpacing(0)
            .accessibilityIdentifier("feishu-conversations")
            .onAppear {
                store.willChange = { [weak scroll] in scroll?.reader.willChange() }
                scroll.reader.reveal = { id, at in proxy.scrollTo(id, anchor: UnitPoint(x: 0.5, y: at)) }
            }
            .task {
                // Read once; a change comes as a settings event (AppModel).
                if let link = model.hub, link.larkDisplay.snapshot == nil { await link.larkDisplay.refresh(using: link.api) }
            }
            .onScrollGeometryChange(for: Double.self) { Double($0.contentOffset.y + $0.contentInsets.top) } action: { _, offset in
                updatePins(offset)
            }
            .onScrollPhaseChange { old, phase, context in
                let offset = Double(context.geometry.contentOffset.y + context.geometry.contentInsets.top)
                if phase != .idle { scroll.reader.cancel() }
                store.holdScrolling(phase != .idle)
                if (phase == .tracking || phase == .interacting) && old != .tracking && old != .interacting {
                    pins.beginDrag(scrollOffset: offset, shelfPassed: shelfPassed(offset), canExpand: store.filter == .all && canExpandPins && query.isEmpty)
                }
                if old == .interacting && phase != .interacting && phase != .tracking {
                    updatePins(offset)
                    withAnimation(Theme.Motion.expand) { _ = pins.endDrag(canExpand: store.filter == .all && canExpandPins && query.isEmpty) }
                } else if old == .tracking && phase != .interacting { pins.cancelDrag() }
            }
            .themedPage()
            .pageSearchable(text: $query, prompt: String(localized: "lark.searchChats", defaultValue: "Search chats", comment: "Search field over the Lark chats list"))
            .navigationTitle("Messages [Quote Views 4da199]")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar(shell == nil ? .visible : .hidden, for: .navigationBar)
            .safeAreaInset(edge: .top, spacing: 0) {
                if let shell, railEdge == nil { topBar(shell) }
            }
            .overlay {
                if let shell, let railEdge { sideRail(shell, edge: railEdge) }
            }
            .pageToolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { openConnectionSettings() } label: { Label("Lark Settings", systemImage: Theme.Icon.settings) }
                }
            }
            }
            .navigationDestination(for: LarkConversation.self) { LarkChatView(conversation: $0, messageComposer: true) }
        }
        #if os(iOS)
        // A phone's tabs (iPhone Duo open): the switch at the very top, over the list and a chat opened from it, as on
        // Agents (#2623).
        .safeAreaInset(edge: .top, spacing: 0) { if shell == nil, pageTabSwitch { PageTabSwitchRow() } }
        #endif
        .onChange(of: store.accountID) { _, _ in path = []; pins = LarkPinnedInteraction() }
        .onChange(of: query) { _, _ in
            pins = LarkPinnedInteraction(); scroll.resetShelf()
            searchPages = 0
            resumePaging()
        }
        // A page held for a moving list lands when it rests: the end may still be on screen and want the next one.
        .onChange(of: store.nextCursor) { _, cursor in if cursor != nil { resumePaging() } }
        .onTabVisibility(appear: { pageVisible = true; resumePaging() }, disappear: { pageVisible = false })
        .onChange(of: phase, initial: true) { _, value in
            sceneActive = VisibleScenePolicy.permits(value)
            resumePaging()
        }
        .onChange(of: store.pinned.isEmpty) { _, empty in
            if empty { pins = LarkPinnedInteraction(); scroll.resetShelf() }
        }
        .onDisappear { pins.cancelDrag(); store.holdScrolling(false); store.willChange = {} }
        .onChange(of: path.isEmpty, initial: true) { _, root in
            shell?.feishuAtRoot = root
            if root { resumePaging() }
        }
        .tabActiveTask(id: model.connection.shown == .online) {
            let changes = LarkSync.shared.listChanges()
            async let accounts: Void = choice.load()
            await store.refresh()
            await accounts
            for await change in changes {
                guard !Task.isCancelled else { return }
                if change.everything {
                    async let accounts: Void = choice.load()
                    await store.refresh()
                    await accounts
                } else {
                    // Most events are new messages: page 1 alone, of this account's chats only.
                    await store.refreshHead(change.feeds(of: choice.requestAccount))
                }
                // Events that come meanwhile fold into one (the stream keeps the newest), and the next page gets its turn.
                try? await Task.sleep(for: Self.refreshGap)
            }
        }

    }

    /// A search with too little to show is still reading on through older pages.
    private var scansPages: Bool { !query.isEmpty || store.filter == .unread }

    private var searchingOn: Bool {
        store.nextCursor != nil && !store.pageFailed && searchPages < Self.searchPageLimit
    }

    /// Pages while the end of the list is on screen; a search counts its pages and stops at `searchPageLimit`.
    private func resumePaging() {
        guard pageVisible, sceneActive, path.isEmpty, endVisible else { return }
        Task { await loadAtEnd() }
    }

    private func loadAtEnd() async {
        let selected = store.filter
        while pageVisible, sceneActive, path.isEmpty, selected == store.filter, endVisible, let cursor = store.nextCursor, !store.pageFailed, !Task.isCancelled {
            let searching = scansPages
            if searching, searchPages >= Self.searchPageLimit { return }
            await store.more()
            guard selected == store.filter else { return }
            // Nothing new on screen yet (another request had it, or the page waits for the list to rest): the cursor's
            // change asks again.
            if store.nextCursor == cursor { return }
            if searching { searchPages += 1 }
        }
    }

    private func select(_ filter: LarkConversationsModel.Filter, proxy: ScrollViewProxy) {
        guard filter != store.filter else { return }
        searchPages = 0
        endVisible = false
        pins = LarkPinnedInteraction()
        scroll.resetShelf()
        Task {
            await store.select(filter)
            let top = filter == .all && query.isEmpty && !store.pinned.isEmpty ? "feishu-pins" : "feishu-filters"
            proxy.scrollTo(top, anchor: .top)
        }
    }

    /// The section header pins below the page bar; the pinned shelf remains ordinary scrolling content above it.
    private func filters(proxy: ScrollViewProxy) -> some View {
        HStack(spacing: Theme.Space.flush) {
            filter(.all, title: String(localized: "lark.filter.all"), proxy: proxy)
            filter(.unread, title: String(localized: "lark.filter.unread"), proxy: proxy)
        }
        .padding(.horizontal, Theme.Space.xxs)
        .background {
            RoundedRectangle(cornerRadius: Theme.Radius.control)
                .fill(Theme.Fill.well)
                .padding(.vertical, Theme.Space.sm)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.page)
        .background(Theme.pageBackground)
    }

    private func filter(_ value: LarkConversationsModel.Filter, title: String, proxy: ScrollViewProxy) -> some View {
        let selected = store.filter == value
        return Button {
            select(value, proxy: proxy)
            Haptics.tap()
        } label: {
            Text(title)
                .font(selected ? Theme.Font.captionStrong : Theme.Font.caption)
                // Section headers supply a secondary foreground; explicit semantic colors keep these controls legible.
                .foregroundStyle(selected ? Color.primary : Color.secondary)
                .lineLimit(1)
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.xs)
                .background(selected ? Theme.Fill.pressed : .clear, in: .rect(cornerRadius: Theme.Radius.control))
                .padding(.vertical, Theme.Space.s)
                .tapTargetHeight()
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("feishu-filter-" + value.rawValue)
    }

    private func shelfPassed(_ offset: Double) -> Bool {
        scroll.shelfHeight > 0 && offset >= Double(scroll.shelfHeight) - LarkPinnedInteraction.topTolerance
    }

    private func updatePins(_ offset: Double) {
        let expanded = pins.isExpanded
        pins.update(scrollOffset: offset, shelfPassed: shelfPassed(offset))
        if expanded && !pins.isExpanded { scroll.compensateCollapse = true }
    }

    @ViewBuilder private var empty: some View {
        if store.loading {
            ProgressView(String(localized: "Loading… [Personal Agent Session View 4927a5]")).frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xxl)
        } else if store.status?.configured == false {
            notice(String(localized: "Lark isn't connected yet"), detail: String(localized: "Connect a Lark account on the hub and your chats will show up here."), action: String(localized: "Connection Settings")) { openConnectionSettings() }
        } else if store.needsUpgrade {
            notice(String(localized: "Hub update needed"), detail: String(localized: "After updating, pinned chats show up and your current Lark account syncs."), action: String(localized: "Check Again [Lark Chat View c25fb8]")) { Task { await store.refresh() } }
        } else if store.failed || store.pageFailed {
            notice(String(localized: "Couldn't load chats"), detail: String(localized: "Check the Lark connection and try again later."), action: String(localized: "Retry [Components b8784c]")) { Task { await store.refresh() } }
        } else {
            Text(store.filter == .unread ? String(localized: "lark.filter.empty") : String(localized: "No chats yet"))
                .font(Theme.Font.body).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xxl)
        }
    }

    private func notice(_ title: String, detail: String, action: String, perform: @escaping () -> Void) -> some View {
        VStack(spacing: Theme.Space.m) {
            Text(title).font(Theme.Font.heading)
            Text(detail).font(Theme.Font.secondary).foregroundStyle(.secondary).multilineTextAlignment(.center)
            Button(action, action: perform).lineLimit(1).themedButton()
        }
        .frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xxl)
    }

    /// The chat connector's page of the default assistant, whose account and switches these chats follow
    /// (`SettingsPage.feishu`), whichever assistant is current.
    private func openConnectionSettings() {
        model.settingsPath = NavigationPath([SettingsPage.feishu(manager: model.settingsRoute(nil))])
        if shell != nil { model.shellSettings = true }
        else { model.selectedTab = .settings }
    }

    private func topBar(_ shell: ShellState) -> some View {
        @Bindable var model = model
        return HStack(alignment: .top, spacing: Theme.Space.s) {
            ShellPersonaButton(id: "feishu-sidebar") { shell.drawer.open() }
            Spacer(minLength: 0)
            ViewThatFits(in: .horizontal) {
                ShellSwitch(tab: $model.selectedTab) { EmptyView() } workMenu: { EmptyView() }
                ShellSwitch(tab: $model.selectedTab) { EmptyView() } workMenu: { EmptyView() }
                    .dynamicTypeSize(...Theme.Shell.compactSwitchLargestTypeSize)
            }
            Spacer(minLength: 0)
            ShellCircleButton(symbol: Theme.Icon.settings, label: String(localized: "Lark Settings"), id: "feishu-settings") { openConnectionSettings() }
        }
        .padding(.horizontal, Theme.Space.l).padding(.top, Theme.Space.xxs)
        .shellTopBarScaled().modifier(ShellBarAtRest(page: 2))
    }

    /// The bar as the side rail (iPhone Duo): the face and Lark Settings, then the switch at the foot of the column.
    private func sideRail(_ shell: ShellState, edge: HorizontalEdge) -> some View {
        @Bindable var model = model
        return ShellSideRail(edge: edge) {
            ShellPersonaButton(id: "feishu-sidebar") { shell.drawer.open() }
            ShellCircleButton(symbol: Theme.Icon.settings, label: String(localized: "Lark Settings"), id: "feishu-settings") { openConnectionSettings() }
        } switcher: {
            ShellSwitch(tab: $model.selectedTab, axis: .vertical) { EmptyView() } workMenu: { EmptyView() }
        }
        .shellTopBarScaled()
        .modifier(ShellBarAtRest(page: 2)) // ShellMovingBar stands in while the strip slides
    }
}
#endif
