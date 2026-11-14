import BeautifulUI
import SwiftUI

/// A new conversation in the shell uses the shared `AssistantWelcome`:
/// greeting, what Persona can reach, things to ask), and over the composer what needs the user right now — agents
/// waiting on them and the inbox's Needs You — one quiet line each, as many as fit under the welcome's first two rows of things
/// to ask (`NeedsRoom`). While the conversation spins up, the first message and a typing line so the send feels instant.
struct ShellHome: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize
    /// iPhone Duo's bars down the side: the top bar is the side rail, and the page's top is free.
    @Environment(\.shellRailEdge) private var railEdge
    var pending: PendingStart?
    var error: String?
    @Binding var launcher: String?
    @Binding var mode: String
    @Binding var effort: String
    var openInbox: (InboxItem) -> Void
    var resend: () -> Void = {}
    var delete: () -> Void = {}
    var send: (String, [ImagePayload]) -> Void
    @State private var room = NeedsRoom()
    @State private var route: ReadinessRoute?
    /// The composer's @ list is open (#2319): Needs You steps aside for it.
    @State private var mentionOpen = false

    /// How far the welcome fades out over Needs You and the composer, rather than being cut off at their edge.
    fileprivate static let fade = Theme.Space.xl

    /// What the page shows: the start error, or under `-showHomeState error` a sample one (screenshots).
    private var shownError: String? {
        #if DEBUG
        if UserDefaults.standard.string(forKey: "showHomeState") == "error" { return "没能开始对话：hub 没有回应（请求超时）" } // i18n-ignore
        #endif
        return error
    }

    var body: some View {
        ScrollView {
            if let pending {
                FirstMessagePending(pending: pending, name: model.assistantShownName, shell: true, resend: resend, delete: delete)
                    // The page's height inside its margins, the message at its bottom (a long one runs up out of it, as the
                    // chat shows its newest end).
                    .containerRelativeFrame(.vertical, alignment: .bottom)
            } else {
                VStack(alignment: .leading, spacing: Theme.Space.xl) {
                    // Its errors stay over the composer, where the send was.
                    AssistantWelcome(send: send, openHelp: { route = .help($0, machine: nil) }, cardRows: 2)
                }
                .padding(.horizontal, Theme.Space.l)
                .frame(maxWidth: ChatView.readableWidth)
                .frame(maxWidth: .infinity)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .dismissesKeyboardOnTap()
        .contentMargins(.top, AssistantTopBar.room(railEdge), for: .scrollContent)
        // Room to scroll the last things to ask clear of the fade.
        .contentMargins(.bottom, pending == nil ? Self.fade : ChatView.newestGap, for: .scrollContent)
        .onPreferenceChange(WelcomeFold.self) { room.fold = $0 }
        .background(Theme.pageBackground.ignoresSafeArea())
        .safeAreaInset(edge: .bottom, spacing: 0) {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                // On the welcome, and under a failed first message (#1368): why it failed, what to do about it.
                if pending == nil || pending?.failed != nil {
                    if let error = shownError {
                        Group {
                            if AppStyle.current.usesBeautifulUI {
                                ShellErrorNote(text: error).padding(.horizontal, Theme.Space.s)
                            } else {
                                Label(error, systemImage: Theme.Icon.error)
                                    .font(Theme.Font.caption)
                                    .foregroundStyle(Theme.danger)
                                    .padding(.horizontal, Theme.Space.l)
                            }
                        }
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { room.error = $0 }
                    }
                    ReadinessHomeCard(route: $route)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { room.card = $0 }
                        .onDisappear { room.card = 0 }
                }
                if pending == nil, !mentionOpen {
                    ShellNeedsYou(maxHeight: room.needs(withError: shownError != nil, top: AssistantTopBar.room(railEdge)), openInbox: openInbox).shellScaled()
                }
                if let m = model.assistantManagerOffline { ManagerOfflineNote(manager: m) }
                StarterComposer(disabled: pending != nil || model.assistantManagerOffline != nil, shell: StarterComposer.ShellModes(launcher: $launcher, mode: $mode, effort: $effort),
                                gate: model.readinessGate(route: $route), send: send)
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { room.composerAtRest($0, typeSize: typeSize) }
                    .onPreferenceChange(MentionMenuOpen.self) { open in withAnimation(Theme.Motion.standard) { mentionOpen = open } }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.bottom, Theme.Space.s)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
            // The welcome reaches down here: back what needs the user and the field with the page, or the cards read
            // through them; above them the welcome fades out (a hard edge cut the cards in two).
            .background(alignment: .top) {
                LinearGradient(colors: [Theme.pageBackground.opacity(0), Theme.pageBackground], startPoint: .top, endPoint: .bottom)
                    .frame(height: Self.fade)
                    .offset(y: -Self.fade)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
            .background(Theme.pageBackground.ignoresSafeArea(edges: .bottom))
        }
        .onGeometryChange(for: CGSize.self) { $0.size } action: { room.pageAtRest($0) }
        .navigationTitle(model.assistantShownName)
        .task(id: launcher) { if pending == nil { await model.warmAssistant(launcher: launcher) } }
        .modifier(ReadinessRefresh())
        .readinessSheets($route)
    }
}

/// How tall Needs You may be on the new conversation page: what is left between the end of the welcome's first two rows of
/// things to ask and the composer (2026-11-02, #283: on iPhone Duo's outer screen three rows and the composer took half of the
/// screen and cut the suggestions in two). It shows as many rows as fit there, at least one. No limit until it is all
/// measured.
private struct NeedsRoom {
    /// The page's height with the keyboard down: the most it has been at this width. The keyboard makes the whole page
    /// shorter (not its safe area: `ignoresSafeArea(.keyboard)` does not get the height back), and typing must not take
    /// rows away; turning the phone or the other screen of an iPhone Duo changes the width.
    private(set) var page: CGFloat = 0
    private var pageWidth: CGFloat = 0
    /// Where the welcome's things to ask that stay in view end, from the top of the scroll content (`WelcomeFold`).
    var fold: CGFloat = 0
    /// The composer at rest, one line: the least it has been at this text size (typing grows it, which must not take
    /// rows away either).
    private(set) var composer: CGFloat = 0
    private var composerSize: DynamicTypeSize?
    /// The start error over Needs You, while it shows.
    var error: CGFloat = 0
    /// The readiness card over Needs You (`ReadinessHomeCard`), while it shows.
    var card: CGFloat = 0

    mutating func pageAtRest(_ size: CGSize) {
        guard size.height > 0 else { return }
        page = size.width == pageWidth ? max(page, size.height) : size.height
        pageWidth = size.width
    }

    mutating func composerAtRest(_ height: CGFloat, typeSize: DynamicTypeSize) {
        guard height > 0 else { return }
        composer = composerSize == typeSize && composer > 0 ? min(composer, height) : height
        composerSize = typeSize
    }

    /// `top`: what the page leaves free at its top for the bar (`AssistantTopBar.room`).
    func needs(withError: Bool, top: CGFloat = AssistantTopBar.height) -> CGFloat {
        guard page > 0, fold > 0, composer > 0 else { return .infinity }
        // Under it: the composer, the stack's spacing and bottom padding (ShellHome), the error and its spacing.
        let below = composer + Theme.Space.m + Theme.Space.s + (withError ? error + Theme.Space.m : 0) + (card > 0 ? card + Theme.Space.m : 0)
        return page - top - fold - ShellHome.fade - below
    }
}

/// Offers its one subview exactly `height` (its own ideal height when that is unlimited) whatever the parent offers, and
/// takes the subview's own size, so a `ViewThatFits` in it picks what fits in `height`. A `.frame(maxHeight:)` would itself
/// grow to `height`; going by the parent's offer, the stack in `safeAreaInset` offers less while the keyboard is up and
/// rows dropped as soon as the composer was focused.
private struct AtMostHeight: Layout {
    var height: CGFloat

    private func offer(width: CGFloat?) -> ProposedViewSize {
        ProposedViewSize(width: width, height: height.isFinite ? max(0, height) : nil)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        subviews.first?.sizeThatFits(offer(width: proposal.width)) ?? .zero
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, proposal: offer(width: bounds.width))
    }
}

/// The BeautifulUI style's start error: the message in its red on a soft red card, whole (a pill would cut it).
private struct ShellErrorNote: View {
    var text: String
    @Environment(\.buiTheme) private var theme

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
            ThemedIcon(Theme.Icon.error)
            Text(text).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .buiFont(.detail)
        .foregroundStyle(theme.colors.red)
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.sm)
        .background(Theme.Fill.well, in: .rect(cornerRadius: theme.radii.card, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}

/// What needs the user, over the empty composer: the agents waiting on them (an answer, an approval, a plan), then the
/// inbox's Needs You, newest first — three at most, fewer when `maxHeight` has no room for them (one at least), then where
/// the rest are. A tap opens it where it lives.
struct ShellNeedsYou: View {
    @Environment(AppModel.self) private var model
    @Environment(\.pushInboxChat) private var pushChat
    /// How tall it may be (`NeedsRoom`).
    var maxHeight: CGFloat = .infinity
    var openInbox: (InboxItem) -> Void

    private enum Line: Identifiable {
        case agent(Interaction)
        case inbox(InboxItem)

        var id: String {
            switch self {
            case .agent(let ix): "ix-" + ix.id
            case .inbox(let it): "in-" + it.key
            }
        }
    }

    private var lines: [Line] {
        #if DEBUG
        // `-showHomeState empty`: nothing needs the user (screenshots).
        if UserDefaults.standard.string(forKey: "showHomeState") == "empty" { return [] }
        #endif
        // The current assistant's, as its Agents and Inbox pages list them.
        let agents = model.scopedInteractions.filter { !model.isMuted($0.sessionKey) }.map(Line.agent)
        let inbox = InboxStore.shared.scopedItems
            .filter { !$0.isDone && $0.triage == .action }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
            .map(Line.inbox)
        return agents + inbox
    }

    var body: some View {
        let all = lines
        if !all.isEmpty {
            AtMostHeight(height: maxHeight) {
                ViewThatFits(in: .vertical) {
                    list(all, shown: 3)
                    list(all, shown: 2)
                    list(all, shown: 1)
                }
            }
        }
    }

    /// Its own height whatever it is offered: with no room even for one row, the last one is drawn whole, not squeezed.
    private func list(_ all: [Line], shown: Int) -> some View {
        Group {
            if AppStyle.current.usesBeautifulUI { beautiful(all, shown: shown) } else { plain(all, shown: shown) }
        }
        .fixedSize(horizontal: false, vertical: true)
    }

    /// The line for what is not shown: how many are left and where they are. The inbox holds only its own: once agents
    /// are among them it says Needs You, and with only agents left it opens Agents.
    private func more(_ hidden: ArraySlice<Line>) -> (text: String, open: () -> Void) {
        let agents = hidden.filter { if case .agent = $0 { true } else { false } }.count
        let text = agents > 0 ? String(localized: "\(hidden.count) more need you") : String(localized: "\(hidden.count) more in Inbox")
        return (text, { [model] in model.selectedTab = agents == hidden.count ? .chats : .mail })
    }

    /// BeautifulUI's list card: a heading with how many wait, then one row per thing (its mark, who, what, a chevron).
    private func beautiful(_ all: [Line], shown: Int) -> some View {
        BUIListSection(StateName.needsYou.text, count: all.count) {
            ForEach(all.prefix(shown)) { line in beautifulRow(line) }
            if all.count > shown {
                let rest = more(all.dropFirst(shown))
                Button(action: rest.open) {
                    BUIListRow(rest.text, accessory: .disclosure) {
                        IconTile(Theme.Icon.inbox, tint: Color.secondary)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("shell-needs-more")
            }
        }
        .transition(.opacity)
        .accessibilityElement(children: .contain) // its own id; the rows keep theirs
        .accessibilityIdentifier("shell-needs-you")
    }

    @ViewBuilder private func beautifulRow(_ line: Line) -> some View {
        switch line {
        case .agent(let ix):
            Button { InboxWaitingOpen.session(ix.sessionKey, currentHub: model.hub?.group, push: pushChat, open: { model.open(ix.sessionKey) }) } label: {
                BUIListRow(agentName(ix), detail: oneLine(ix.summary ?? ix.questions?.first?.question ?? ix.title),
                           accessory: .disclosure, detailLines: 1) {
                    IconTile(ix.symbol, tint: Theme.needsYou)
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("shell-needs-agent")
        case .inbox(let it):
            Button { openInbox(it) } label: {
                BUIListRow(it.title, detail: oneLine(it.summary ?? it.preview), accessory: .disclosure, detailLines: 1) {
                    beautifulMark(it)
                }
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("shell-needs-inbox")
        }
    }

    /// An inbox item's mark: Feishu's logo on a white tile, or its kind's symbol on the accent.
    @ViewBuilder private func beautifulMark(_ it: InboxItem) -> some View {
        if let brand = Self.brand(it) {
            BrandTile(brand)
        } else {
            IconTile(Self.symbol(it), tint: Theme.accent)
        }
    }

    private func oneLine(_ s: String) -> String { s.replacingOccurrences(of: "\n", with: " ") }

    private func plain(_ all: [Line], shown: Int) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(all.prefix(shown)) { line in row(line) }
            if all.count > shown {
                let rest = more(all.dropFirst(shown))
                Button(action: rest.open) {
                    HStack(spacing: Theme.Space.m) {
                        ThemedIcon(Theme.Icon.inbox)
                            .font(Theme.Font.heading)
                            .foregroundStyle(.secondary)
                            .scaledFrame(width: Theme.Size.icon)
                        Text(rest.text).font(Theme.Font.body).foregroundStyle(.secondary).lineLimit(1)
                        Spacer(minLength: 0)
                    }
                    .modifier(LineFrame())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("shell-needs-more")
            }
        }
        .transition(.opacity)
        .accessibilityElement(children: .contain) // its own id; the rows keep theirs
        .accessibilityIdentifier("shell-needs-you")
    }

    @ViewBuilder private func row(_ line: Line) -> some View {
        switch line {
        case .agent(let ix):
            Button { InboxWaitingOpen.session(ix.sessionKey, currentHub: model.hub?.group, push: pushChat, open: { model.open(ix.sessionKey) }) } label: {
                HStack(spacing: Theme.Space.m) {
                    ThemedIcon(ix.symbol)
                        .font(Theme.Font.heading)
                        .foregroundStyle(Theme.needsYou)
                        .scaledFrame(width: Theme.Size.icon)
                    text(who: agentName(ix), what: ix.summary ?? ix.questions?.first?.question ?? ix.title)
                }
                .modifier(LineFrame())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("shell-needs-agent")
        case .inbox(let it):
            Button { openInbox(it) } label: {
                HStack(spacing: Theme.Space.m) {
                    mark(it)
                    text(who: it.title, what: it.summary ?? it.preview)
                }
                .modifier(LineFrame())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("shell-needs-inbox")
        }
    }

    /// ChatGPT's rows: one line of large gray text.
    private func text(who: String, what: String) -> some View {
        // All in the secondary gray like ChatGPT's rows; the name a step darker to find it at a glance.
        (Text(who).foregroundStyle(Color.primary.opacity(0.75)) + Text("  " + what.replacingOccurrences(of: "\n", with: " ")).foregroundStyle(.secondary))
            .font(Theme.Font.body)
            .lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private func mark(_ it: InboxItem) -> some View {
        if let brand = Self.brand(it) {
            BrandMark(brand, side: Theme.Size.iconS)
                .scaledFrame(width: Theme.Size.icon)
        } else {
            ThemedIcon(Self.symbol(it))
                .font(Theme.Font.heading)
                .foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.icon)
        }
    }

    /// A source's own mark: Lark's for its chats, a connector's (GitHub…); nil draws the kind's symbol.
    private static func brand(_ it: InboxItem) -> String? {
        if it.connector != nil { return ConnectorText.brand(it.source) }
        return it.source == "lark" && it.kind != "doc" && it.kind != "moment" ? Theme.Brand.lark : nil
    }

    private static func symbol(_ it: InboxItem) -> String {
        if it.connector != nil { return Theme.Icon.connectors }
        return switch it.kind {
        case "doc": Theme.Icon.document
        case "moment": Theme.Icon.moments
        default: Theme.Icon.mail
        }
    }

    private func agentName(_ ix: Interaction) -> String {
        if let t = ix.sessionTitle, !t.isEmpty { return t }
        return ix.cwd.map { URL(fileURLWithPath: $0).lastPathComponent } ?? "Agent"
    }

    private struct LineFrame: ViewModifier {
        func body(content: Content) -> some View {
            content
                .padding(.horizontal, Theme.Space.l)
                .scaledFrame(minHeight: Theme.Size.box56)
                .contentShape(.rect)
        }
    }
}

/// The pages the shell pushes onto the Assistant page by value (docs/apple/ios.md「New shell」), and what those push in turn: the
/// inbox (its items and Feishu pages), the machines (a machine, its agents). One stack, values only: a value pushed on
/// top of a view-based push lands under it (docs/lessons/ios.md).
struct ShellDestinations: ViewModifier {
    @Binding var path: NavigationPath
    var enabled: Bool
    /// Injected here as well as on Assistant's stack so destination pages in a wide split receive it.
    var inboxChat: ((InboxChatRoute) -> Void)? = nil
    /// The pages the Agents list registers too (a chat, a to-do, a machine, its terminals): off in the Sidebar layout
    /// (#6332), whose one path has the Agents' (`AgentsDestinations`), the split's first pages without a back.
    var agentRoutes = true
    @StateObject private var accounts = InboxAccountState()

    /// The explicit closure from Assistant, or this destination's own path in the unified Sidebar layout.
    private var routeChat: ((InboxChatRoute) -> Void)? {
        if let inboxChat { return inboxChat }
        guard enabled else { return nil }
        return { path.append($0) }
    }

    func body(content: Content) -> some View {
        if enabled {
            content
                // ☰ beside Back on a place's pages: the sidebar opens at any depth, so a place is changed without
                // popping it (docs/design/sidebar-navigation.md). In the Sidebar layout a place is the split's first
                // page, with no back to the conversation under it (#6332).
                .navigationDestination(for: ShellRoute.self) { route in
                    routed(place(route).modifier(ShellPlaceSidebarButton()).modifier(FirstPage(on: !agentRoutes)))
                }
                .modifier(InboxDestinations(accounts: accounts.accounts, inboxChat: routeChat, openFeishu: { path.append($0) }))
                .modifier(AgentRoutes(on: agentRoutes, chat: routeChat, openFeishu: { path.append($0) }))
                .navigationDestination(for: InboxChatRoute.self) { r in routed(ChatView(key: r.key, itemManager: r.manager).id(r.key)) }
                .environment(\.feishuOpen) { path.append($0) }
                .environment(\.pushInboxChat, routeChat)
                #if DEBUG
                .onAppear { ShellTrace.shared.note("inbox chat closure available=\(routeChat != nil)") }
                #endif
        } else {
            content
        }
    }

    /// Injects on each returned destination, not only the stack's root content (the iPad split drops the latter).
    private func routed<Content: View>(_ content: Content) -> some View {
        content.modifier(InboxDestinationEnvironment(chat: routeChat, openFeishu: { path.append($0) }))
    }

    /// What the Agents list registers as well: here only where it does not (the shell, the iPad's Persona tab).
    private struct AgentRoutes: ViewModifier {
        var on: Bool
        var chat: ((InboxChatRoute) -> Void)?
        var openFeishu: ((FeishuRoute) -> Void)?

        func body(content: Content) -> some View {
            if on {
                content
                    .navigationDestination(for: TodoDetailRoute.self) { r in
                        TodoDetailView(todoID: r.id).modifier(ShellPlaceSidebarButton())
                            .modifier(InboxDestinationEnvironment(chat: chat, openFeishu: openFeishu))
                    }
                    .navigationDestination(for: MachineDetailRoute.self) { r in
                        MachineDetailView(ref: r.ref).modifier(ShellPlaceSidebarButton())
                            .modifier(InboxDestinationEnvironment(chat: chat, openFeishu: openFeishu))
                    }
                    .navigationDestination(for: TerminalsRoute.self) { r in
                        TerminalsView(only: r.machine).modifier(ShellPlaceSidebarButton())
                            .modifier(InboxDestinationEnvironment(chat: chat, openFeishu: openFeishu))
                    }
                    .navigationDestination(for: SessionKey.self) { key in
                        ChatView(key: key).id(key).modifier(InboxDestinationEnvironment(chat: chat, openFeishu: openFeishu))
                    }
            } else {
                content
            }
        }
    }

    /// The Sidebar layout's places are the split's first pages: no back to the conversation under them (#6332).
    private struct FirstPage: ViewModifier {
        var on: Bool

        func body(content: Content) -> some View {
            if on { content.splitDetailPage() } else { content }
        }
    }

    @ViewBuilder private func place(_ route: ShellRoute) -> some View {
        switch route {
        case .inbox: InboxPage(path: $path, accountState: accounts, embedded: true)
        case .todos: TodosView()
        case .machines: MachinesView()
        case .extensionPage(let r): ExtensionNavPage(route: r)
        case .profile: AssistantProfileView()
        case .routines: AssistantRoutinesView()
        case .knowledge: KnowledgeView()
        case .recordings:
            #if os(iOS) || os(visionOS)
            RecordingsPage()
            #else
            KnowledgeView() // the Mac records nothing: its Knowledge lists the hub's recordings
            #endif
        }
    }
}

/// ☰ beside Back on the pages of a place the shell's sidebar opened (docs/design/sidebar-navigation.md): it opens the drawer
/// over the page as it is, so the user can go to another place and come back to this one where they left it. Only in the
/// iPhone shell; the iPad's sidebar is a column of its own.
struct ShellPlaceSidebarButton: ViewModifier {
    @Environment(\.shell) private var shell

    func body(content: Content) -> some View {
        #if os(iOS)
        if let shell {
            content.toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button {
                        Haptics.tap()
                        shell.drawer.open()
                    } label: {
                        Label(String(localized: "Chat History [Shell Components 60636b]"), systemImage: Theme.Icon.sidebarMenu)
                            .labelStyle(.iconOnly)
                    }
                    .tint(Color.primary) // as Back beside it, not the accent
                    .accessibilityIdentifier("shell-place-sidebar")
                }
            }
        } else {
            content
        }
        #else
        content
        #endif
    }
}

extension EnvironmentValues {
    /// The Persona conversation is in the shell (docs/apple/ios.md「New shell」): neutral bubbles, the shell's composer, modes in it.
    @Entry var shellChat = false
}
