import BeautifulUI
import PhotosUI
import SwiftUI

/// The Assistant stack's conversation-selection rules, pure so every launch and reconnect case can be tested.
nonisolated enum AssistantCurrentNavigation {
    /// An automatic nil-to-main selection leaves every page already pushed over the conversation alone.
    static func keepsPushedPages(old: SessionKey?, new: SessionKey?, main: SessionKey?) -> Bool {
        old == nil && new != nil && new == main
    }

    /// A real hub-group change clears pages belonging to the old hub; initial pairing and reconnects do not.
    static func clearsPushedPages(oldHub: String?, newHub: String?) -> Bool {
        oldHub != nil && newHub != nil && oldHub != newHub
    }
}

/// The Persona tab: one personal assistant (a Claude session in ~/.persona/assistant on the hub's machine) that can reach the
/// user's computers, iPhone data, mail, company data and the knowledge base. One conversation is the main one (where the
/// user usually talks to it, and where other pages hand things to it); the others are side conversations.
struct AssistantView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.horizontalSizeClass) private var widthClass
    /// The ChatGPT-style shell (docs/apple/ios.md「New shell」): it owns the drawer, and pushes its pages here.
    @Environment(\.shell) private var shell
    #if os(iOS)
    /// The iPad's tabs omit Inbox and Settings: the sidebar column lists them as the shell's does.
    @Environment(\.padRouter) private var padRouter
    #endif
    @State private var pending: PendingStart?
    /// Pages pushed by value: the shell's Inbox and Machines (and what they push themselves).
    @State private var path = NavigationPath()
    #if os(iOS)
    /// The Sidebar layout (#6332): this page is the root of the page beside the sidebar, without a stack of its own; its
    /// pages go on that page's one path (`unifiedPath`), every place a value there, so a pick in the sidebar replaces
    /// what was over the conversation (an Agent, a page) instead of landing under it.
    @Environment(\.unifiedSidebar) private var unified
    @Environment(\.unifiedPath) private var hostPath
    @Environment(\.splitDetail) private var split
    @Environment(\.splitShowList) private var showList
    /// The inbox's accounts the split's destinations share, for the Inbox drawn as the root (#8359).
    @Environment(\.shellInboxAccounts) private var inboxAccounts
    private var embedded: Bool { unified != nil && shell == nil && hostPath != nil }
    /// The Sidebar layout on a phone: the page under the drawer (`SplitDetail.drawer`).
    private var drawerPage: Bool { embedded && split?.drawer == true }
    /// The path this page's pages are pushed on: the page beside the sidebar's in the Sidebar layout, its own otherwise.
    private var navPath: Binding<NavigationPath> { hostPath ?? $path }
    #else
    private var unified: UnifiedSidebar? { nil } // no Sidebar layout here (#9310)
    private var embedded: Bool { false }
    private var drawerPage: Bool { false }
    private var navPath: Binding<NavigationPath> { $path }
    #endif
    /// The last shell request shown (`ShellState.request`'s serial).
    @State private var handledRequest = 0
    /// The leader conversation a direction's page was just opened over from the sidebar (#11516): its becoming current
    /// is not the user leaving the page, which is already over it.
    @State private var directionLanding: SessionKey?
    /// The sidebar's place in front and the paths of the places left (docs/design/sidebar-navigation.md).
    @State private var places = ShellPlaces()
    /// A first message whose start failed (#1368): it stays on the page until it is sent again or deleted.
    @State private var failedStart = FailedStart.shared
    @State private var starterLauncher: String?
    /// The model picked under `starterLauncher` (#11472); nil: its default entry, or nothing picked. Set together with the
    /// launcher by the starter's model menu (`StarterModesMenu`); the Mac's launcher-only pickers never set it.
    @State private var starterModel: String?
    /// Health's own pick with several managers: its conversations are the default's, whichever manager the page shows.
    @State private var healthLauncher: String?
    @State private var healthModel: String?
    @State private var showProfile = false
    @State private var showKnowledge = false
    @State private var showRoutines = false
    @State private var showTodos = false
    /// An extension's main-navigation page opened from the sidebar (#2961).
    @State private var openedExtension: ExtensionNavRoute?
    /// Health: the health space's page (docs/assistant/health.md).
    @State private var showHealth = false
    /// A domain space's home or the new space form (#13500), presented as Health's page is.
    @State private var openedSpace: DomainSpacePage?
    /// What a space conversation's category chips take under the top bar (#13500): the conversation leaves it free.
    @State private var categoriesHeight: CGFloat = 0
    @State private var showRecordings = false
    /// The sidebar (Chat History, or a drag to the right anywhere on the page): docs/apple/ios.md「Persona top bar and sidebar」.
    @State private var drawer = AssistantDrawer()
    #if os(iOS)
    /// Regular width (iPad, iPhone Duo open; the Separate layout): the sidebar is a column beside the conversation; Chat
    /// History shows or hides it. As it was last left (`SplitListMemory`, #2624, #4206).
    @State private var columns = SplitListMemory.startsClosed(Self.listKey) ? .detailOnly : SplitColumns.shown
    /// Where the history column's shown state is kept.
    private static let listKey = "assistant.list"
    /// The history column as UIKit has it (`SplitColumnControl`).
    @State private var columnState = SplitColumnState()
    @State private var replySpeechContext = ReplySpeechContext()
    #else
    /// Regular width: the sidebar is a column beside the conversation; Chat History shows or hides it.
    @State private var columns = SplitColumns.shown
    #endif
    /// iPhone Duo's bars down the side: the top bar is the side rail, and the page's top is free.
    @Environment(\.shellRailEdge) private var railEdge
    /// The conversation's lifted message, drawn over the top bar (`MessageLiftHost`, #3188).
    @State private var liftHost = MessageLiftHost()
    #if DEBUG
    @State private var showAvatars = false
    #endif
    /// Mode / effort for new conversations ("" = the launcher's own), remembered across launches.
    @StoredValue("assistant.newMode") private var starterMode = ""
    @StoredValue("assistant.newEffort") private var starterEffort = ""

    private var current: SessionKey? { model.assistantCurrent }

    /// The sidebar column has the shell's places (Inbox, To-dos, Machines) and Settings (the iPad, `padRouter`).
    private var padPlaces: Bool {
        #if os(iOS)
        padRouter != nil && widthClass == .regular
        #else
        false
        #endif
    }

    /// What the shell or the iPad's tabs asked this page to show.
    private var request: (page: ShellPage, serial: Int)? {
        #if os(iOS)
        if shell == nil, let padRouter { return padRouter.request }
        #endif
        return shell?.request
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AssistantView")
        #endif
        if shell != nil {
            stack // the shell holds the drawer around both of its pages
        } else if embedded {
            // The Sidebar layout (#6332): the page beside the sidebar, whose stack and sidebar are the split's.
            routed.environment(\.pushInboxChat, inboxChatPush)
        } else if widthClass == .regular {
            // The split keeps both columns clear of the fold when the device is partly folded.
            NavigationSplitView(columnVisibility: $columns) {
                // One width, the other splits' (`SidebarWidth`, #4206).
                sidebar(column: true)
                    .toolbarVisibility(.hidden, for: .navigationBar)
                    .modifier(SidebarWidth())
            } detail: {
                stack
                    #if os(iOS)
                    // Slid by a finger, as the other splits' lists (`SplitColumnControl`, #4206).
                    .background {
                        SplitColumnControl(drags: true, state: columnState) { shown in
                            var t = Transaction()
                            t.disablesAnimations = true
                            withTransaction(t) { columns = shown ? SplitColumns.shown : .detailOnly }
                            SplitListMemory.keep(Self.listKey, shown: shown, state: columnState)
                        }
                    }
                    .modifier(SplitPageMin())
                    #endif
            }
            .modifier(SidebarTopFill(shown: columns != .detailOnly))
            .navigationSplitViewStyle(.balanced)
            #if os(iOS)
            // Shown or not, remembered; beside the page or over it by the width (#2624, #4206).
            .modifier(SplitListMemory(columns: $columns, key: Self.listKey, state: columnState))
            #endif
        } else {
            // The sidebar takes the whole height, like ChatGPT's: the tab bar fades away as it comes out
            // (`AssistantDrawer.tabBar`).
            AssistantDrawerLayout(drawer: drawer, enabled: atRoot, sidebar: { sidebar(column: false) }, page: stack)
        }
    }

    /// The sidebar, as the drawer (compact) or as a column of the split (regular, the tab bar beside it: no Settings; on
    /// the iPad, whose tabs have neither, the shell's places and Settings as on the iPhone, `padPlaces`).
    private func sidebar(column: Bool) -> some View {
        let places = column && padPlaces
        return AssistantSidebar(current: pending == nil ? current : nil,
                                open: { key in
                                    model.assistantCurrent = key
                                    pending = nil
                                    drawer.close()
                                    // The conversation, not the page pushed over it (the same one open: no change to see).
                                    if places { leavePlace() }
                                },
                                newConversation: { newConversation(); drawer.close(); if places { leavePlace() } },
                                // The iPad's column: places, each keeping its stack (`show`, docs/design/sidebar-navigation.md).
                                openProfile: places ? { show(.profile) } : { drawer.close(); showProfile = true },
                                openRoutines: places ? { show(.routines) } : { drawer.close(); showRoutines = true },
                                openKnowledge: places ? { show(.knowledge) } : { drawer.close(); showKnowledge = true },
                                openSettings: places ? { model.shellSettings = true }
                                    : column ? nil : { drawer.close(); model.selectedTab = .settings },
                                drawer: column ? nil : drawer,
                                openInbox: places ? { show(.inbox) } : nil,
                                openTodos: places ? { show(.todos) } : {
                                    // A place picked in the sidebar is taken here too (its list changes in place).
                                    _ = TodoWorkspace.shared.opening(inFront: showTodos)
                                    drawer.close()
                                    showTodos = true
                                },
                                onMachines: places ? { show(.machines) } : nil,
                                onOpenExtension: places ? { show(.extensionPage($0)) } : { r in drawer.close(); openedExtension = r },
                                openHealth: places ? { show(.health) } : { drawer.close(); enterHealth() },
                                openSpace: places ? { show(.space($0)) } : { slug in drawer.close(); enterSpace(slug) },
                                openNewSpace: places ? { show(.newSpace) } : { drawer.close(); backToRoot(); present(.newSpace) },
                                openRecordingsPage: Self.recordingsPage(places ? { show(.recordings) } : { drawer.close(); showRecordings = true }),
                                openStarred: sidebarStars)
    }

    private var sidebarStars: (() -> Void)? {
        #if os(iOS)
        { show(.starred) }
        #else
        nil
        #endif
    }

    /// The sidebar's Recordings: `open`s the page, which shows the recording running, else Record to start one.
    /// nil off the iPhone and iPad (the Mac records nothing).
    private static func recordingsPage(_ open: @escaping () -> Void) -> (() -> Void)? {
        #if os(iOS)
        return open
        #else
        return nil
        #endif
    }

    /// Whether the sidebar column is out beside the conversation: the split's in the Sidebar layout, this page's own else.
    private var sidebarColumnShown: Bool {
        #if os(iOS)
        if embedded { return split?.listHidden != true }
        #endif
        return columns != .detailOnly
    }

    /// Chat History: the drawer on a phone, the sidebar column beside the conversation on a wide display.
    private func toggleSidebar() {
        #if os(iOS)
        if embedded {
            // The Sidebar layout's sidebar is the split's column (#6332): it folds away and comes back from here.
            if split?.listHidden == true { showList?.show() } else { showList?.hide?() }
            return
        }
        #endif
        if let shell {
            shell.drawer.open()
        } else if widthClass == .regular {
            withAnimation(Theme.Motion.standard) { columns = columns == .detailOnly ? SplitColumns.shown : .detailOnly }
        } else {
            drawer.open()
        }
    }

    /// A phone's page (the iPhone, the open iPhone Duo): the iPhone's new conversation (`ShellHome`), not the iPad's.
    private var phonePage: Bool {
        #if os(iOS)
        UIDevice.current.userInterfaceIdiom == .phone
        #else
        false
        #endif
    }

    /// Nothing pushed on the page: the sidebar's drag only works here (pushed pages have their own back swipe).
    private var atRoot: Bool {
        var pushed = showProfile || showKnowledge || showRoutines || showTodos || openedExtension != nil || showHealth || showRecordings
            || openedSpace != nil
            || !navPath.wrappedValue.isEmpty
        #if DEBUG
        pushed = pushed || showAvatars
        #endif
        return !pushed
    }

    /// The drawer the page slides with: the shell's, or its own.
    private var activeDrawer: AssistantDrawer {
        #if os(iOS)
        if drawerPage, let unified { return unified.drawer } // the Sidebar layout's on a phone (#6332)
        #endif
        return shell?.drawer ?? drawer
    }

    /// The stack's root page. Its own property so the stack's modifier chain stays within what Xcode 26.0 can
    /// type-check in time (#5333).
    private var root: some View {
        Group {
            #if DEBUG
            let _ = ShellTrace.shared.note("assistant page \(NewChatCheck.name(current, main: model.assistantMain)) pending=\(pending != nil)")
            #endif
            if let key = current, pending == nil, !showsClearedMain {
                // Another assistant's conversation not listed yet: read through the manager it was opened for.
                ChatView(key: key, style: .assistant, itemManager: model.assistantItemManagers[key])
                    .id(key)
                    #if os(iOS)
                    .environment(\.messageLiftHost, liftHost)
                    .background {
                        if phonePage, atRoot, key == model.assistantMain, !activeDrawer.isOut, liftHost.menu?.lift == nil {
                            Color.clear
                                .background { ReplySpeechObserver(key: key) }
                                .modifier(ReplySpeechInputObservation(context: replySpeechContext))
                        }
                    }
                    #endif
                    // The iPad draws the shell's composer too (`ChatComposerKind.padCard`): the assistant's, as on the iPhone.
                    // The Sidebar layout's phone page too: the iPhone's conversation, as in the shell (#6332).
                    .environment(\.shellChat, shell != nil || ChatComposerKind.padCard || drawerPage)
            } else if shell != nil || drawerPage || phonePage {
                // A phone's page at regular width too (iPhone Duo open, either layout; #3680, #3641): the iPhone's new
                // conversation, with what needs the user over the composer. The iPad keeps `AssistantHome`.
                ShellHome(pending: shownStart, error: shownFailure?.reason, cleared: showsClearedMain, launcher: $starterLauncher,
                          launcherModel: $starterModel, mode: $starterMode,
                          effort: $starterEffort, openInbox: openFromOutside, resend: resend, delete: deleteFailed) { text, images in
                    Task { await start(text, images) }
                }
                .onAppear {
                    NewChatCheck.shared.homeAppeared()
                    #if DEBUG
                    ShellTrace.shared.note("home appeared")
                    #endif
                }
            } else {
                AssistantHome(pending: shownStart, error: shownFailure?.reason, cleared: showsClearedMain, launcher: $starterLauncher,
                              launcherModel: $starterModel, mode: $starterMode,
                              effort: $starterEffort, resend: resend, delete: deleteFailed) { text, images in
                    Task { await start(text, images) }
                }
                .onAppear { NewChatCheck.shared.homeAppeared() }
            }
        }
    }

    /// The root with its top bar, the lift layer and the pages it pushes: split from `stack` for Xcode 26.0 (#5333).
    private var dressedRoot: some View {
        AnyView(rootPage)
        .navigationDestination(isPresented: $showProfile) { AssistantProfileView() }
        .navigationDestination(isPresented: $showKnowledge) { KnowledgeView() }
        .navigationDestination(isPresented: $showRecordings) {
            RecordingsPage()
        }
        .navigationDestination(isPresented: $showRoutines) { AssistantRoutinesView() }
        .navigationDestination(isPresented: $showTodos) {
            #if os(iOS)
            TodosPlacePage(openDetail: { navPath.wrappedValue.append($0) }) // V1 where the hub sends the view (#10465)
            #else
            TodosView(openDetail: { navPath.wrappedValue.append($0) })
            #endif
        }
        .navigationDestination(item: $openedExtension) { ExtensionNavPage(route: $0) }
        // A destination does not get what the stack's root was given in a split (iPad): the page's chats are pushed
        // over it only with the push passed here, as the Inbox's pages get it, never opened as a sidebar pick (3.5).
        .navigationDestination(for: DirectionPageRoute.self) {
            DirectionPage(id: $0.id)
                .modifier(InboxDestinationEnvironment(chat: inboxChatPush, openFeishu: { navPath.wrappedValue.append($0) }))
        }
        .navigationDestination(isPresented: $showHealth) { healthPage }
        .navigationDestination(item: $openedSpace) { spacePage($0) }
        .modifier(ShellDestinations(path: $path, enabled: placesByValue && !embedded, inboxChat: inboxChatPush))
        .modifier(ClassicDestinations(path: $path, enabled: shell == nil && !padPlaces && !embedded))
        #if os(iOS)
        .modifier(SidebarRouteDestinations(enabled: embedded, page: sidebarPage))
        #endif
    }

    /// The page at the stack's root: the conversation, or in the Sidebar layout the root its sidebar picked (#8359).
    @ViewBuilder private var rootPage: some View {
        #if os(iOS)
        if embedded, let value = unified?.root.value {
            sidebarRootPage(value)
        } else {
            conversationPage
        }
        #else
        conversationPage
        #endif
    }

    /// The conversation with its top bar and the lift layer.
    private var conversationPage: some View {
        AnyView(root)
        // No system bar: the top bar floats over the conversation with a fog under it (it scrolls beneath).
        .toolbarVisibility(.hidden, for: .navigationBar)
        .background { RootBarHidden(atRoot: atRoot, roots: embedded ? unified : nil) }
        .overlay(alignment: .top) {
            AssistantTopBar(key: pending == nil ? current : nil,
                            sidebarIsColumn: widthClass == .regular,
                            sidebarShown: sidebarColumnShown,
                            sidebarCounts: embedded,
                            drawerTitle: drawerPage,
                            newChatInSidebar: embedded && widthClass == .regular || padPlaces,
                            openSidebar: toggleSidebar,
                            // In a health conversation New opens Health's new conversation page, whose list has the
                            // space's past ones (#4329); elsewhere the assistant's.
                            newConversation: newFromTopBar,
                            openKnowledge: { if placesByValue { show(.knowledge) } else { showKnowledge = true } },
                            openProfile: { if placesByValue { show(.profile) } else { showProfile = true } })
            #if os(iOS)
            .liftBlur(host: liftHost)
            #endif
        }
        // A space conversation's categories under the bar (#13500); the conversation leaves their height free.
        .overlay(alignment: .top) { spaceCategories }
        .environment(\.assistantTopExtra, currentSpace == nil ? 0 : categoriesHeight)
        #if os(iOS)
        // A message pressed in the conversation, over the bar too: the whole page dims (#3188).
        .overlay { MessageLiftHostLayer(host: liftHost) }
        .environment(\.replySpeechContext, phonePage ? replySpeechContext : nil)
        #endif
    }

    private var stack: some View {
        NavigationStack(path: $path) {
            routed
        }
        .environment(\.pushInboxChat, inboxChatPush)
    }

    /// The root as the stack's (or, in the Sidebar layout, the split's page's) first page: what it does there (its routes,
    /// the requests from the shell or the sidebar, the debug pages).
    private var routed: some View {
            // Erased here and in `dressedRoot` (#6735): unoptimized, this getter reserved a stack slot for every
            // modifier below, each holding the whole page (375 KB of an iPhone's 1 MB main-thread stack).
            AnyView(dressedRoot)
            // A task's card in the conversation, or Full Screen in an agent's sheet (TasksStore.expand): its page pushed
            // here, a drill-down from the conversation in every layout (#9971). In the Sidebar layout too it is pushed, not
            // made a root (a root is what the sidebar picks, #8359): it has Back to the conversation, and the sidebar's
            // selection stays on the conversation.
            .onChange(of: TasksStore.shared.fullAgent, initial: true) { _, key in
                guard let key else { return }
                TasksStore.shared.fullAgent = nil
                navPath.wrappedValue.append(key)
            }
            // A task's detail asked for (a "Sent to" line, a card's task, a dispatch link): in the Sidebar layout's page a
            // value on its path, so it belongs to the root it was opened on (#13156). Pushed by view from the conversation
            // (`detailPresentation`), a pick in the sidebar replaced the root under it and the page stayed in front.
            .onChange(of: taskDetailByValue && (TasksStore.shared.detailTask != nil || TasksStore.shared.detailAgent != nil),
                      initial: true) { _, asked in
                guard asked, let route = TasksStore.shared.takeDetail() else { return }
                navPath.wrappedValue.append(route)
            }
            .environment(\.taskDetailByValue, taskDetailByValue)
            // On appear too (a cold launch onto Inbox asks before this page exists), but each request once: the root
            // appears again after every pop, and replaying the last request pushed the inbox right back.
            .onChange(of: request?.serial, initial: true) { _, serial in
                guard let serial, serial > handledRequest else { return }
                handledRequest = serial
                show(request?.page)
            }
            .onChange(of: atRoot, initial: true) { _, root in
                shell?.chatAtRoot = root
                // Back out of a place by the user: it starts at its root next time.
                if root { places.backedOut() }
            }
            // Opening a conversation from the profile (a run, a follow-up) shows it: back to the root. So does Ask Persona
            // from the knowledge base, even when its conversation is the one already open.
            // A start error belongs to the home page it failed on: another conversation (from any menu, the Mac's
            // ⇧⌘N, a quick action) leaves it behind.
            .onChange(of: model.assistantCurrent) { old, new in
                // The app picking the main conversation by itself once the hub answers (none was open) is not the user
                // opening one: any page already pushed at launch stays. User-driven changes leave the conversation but
                // keep the place's stack for a later return (`ShellPlaces`).
                if let landing = directionLanding, landing == new {
                    directionLanding = nil
                    return // a direction's page was put over it already (`openDirectionPage`)
                }
                if AssistantCurrentNavigation.keepsPushedPages(old: old, new: new, main: model.assistantMain) { return }
                leavePlace()
            }
            // A direction's page, from its sidebar row's All › or its leader conversation's title menu (#11516).
            .onChange(of: DirectionPages.shared.ask?.serial, initial: true) { _, serial in
                guard let serial, let ask = DirectionPages.shared.take(serial) else { return }
                openDirectionPage(ask)
            }
            .onChange(of: model.hub?.group) { old, new in
                if old != new { model.forgetMainClear() }
                // A pushed inbox and its items belong to the old hub. Initial pairing (nil → a group) and reconnects stay.
                if AssistantCurrentNavigation.clearsPushedPages(oldHub: old, newHub: new) { forgetPlaces() }
            }
            #if os(iOS)
            .onChange(of: model.hiddenNavigationPages) { _, hidden in
                let current = embedded ? unified?.shownPlace : places.current
                if let page = current?.navigationPage, hidden.contains(page) { leavePlace() }
                if hidden.contains(.todos) { showTodos = false }
            }
            #endif
            .onChange(of: model.assistantManagerID) {
                starterLauncher = nil // a launcher of the assistant shown before
                starterModel = nil
                model.forgetMainClear() // the empty page belongs to the manager that was cleared
                // A first message still spinning up is the other assistant's: its conversation is listed there when it
                // starts (`start`), and this page shows the one switched to.
                pending = nil
            }
            // A conversation from the sidebar: the place in front is kept, the page switches under the drawer, then it closes.
            .onChange(of: model.assistantReveal) { leavePlace(); activeDrawer.close() }
            // Another assistant is current: a pushed page (the profile, scheduled tasks, memory…) read the one before and
            // would write to this one, so it closes.
            .onChange(of: model.assistantSwitches) { forgetPlaces() }
            #if DEBUG
            // `-openAssistantPage profile|avatars|routines|runs|…` (with `-tab assistant`): the profile and its pages;
            // `-openAssistantSidebar YES` (or a fraction): the sidebar is out; `-showDrawerAutoDrag <rounds>`: scripted drags, measured
            // (`DrawerProbe`, with `-showDrawerProbe YES`).
            .navigationDestination(isPresented: $showAvatars) { PersonaGallery() }
            .task {
                let d = UserDefaults.standard
                guard d.string(forKey: "openAssistantPage") != nil || d.string(forKey: "openShellPage") != nil else { return }
                // With `-openAssistantManager <id>` the page first switches to that assistant, which closes pushed pages
                // (`assistantSwitches`), and a launch that never chose a conversation picks the main one once the hub
                // answers, which goes back to the root too: open them after those, and only wait for what will happen.
                var mainComing = d.object(forKey: "assistant.current") == nil
                if let id = d.string(forKey: "openAssistantManager") {
                    let switches = model.assistantSwitches
                    for _ in 0..<40 where model.currentAssistant?.id != id { try? await Task.sleep(for: .milliseconds(250)) }
                    if model.assistantSwitches != switches { mainComing = true }
                }
                if mainComing {
                    // Until a conversation is on screen, or the settings are read and there is no main conversation to pick.
                    for _ in 0..<20 where model.assistantCurrent == nil && !(model.assistantConfig != nil && model.assistantMain == nil) {
                        try? await Task.sleep(for: .milliseconds(250))
                    }
                }
                try? await Task.sleep(for: .milliseconds(300)) // after their own going back
                switch UserDefaults.standard.string(forKey: "openAssistantPage") {
                case "avatars": showAvatars = true
                case "knowledge": showKnowledge = true
                case "todos": showTodos = true
                case "health": showHealth = healthShellPage != nil
                // `-openAssistantPage space:<slug>`: that space's home; `space-new`: the new space form (screenshots).
                case .some(let p) where p.hasPrefix("space:"): present(.space(String(p.dropFirst("space:".count))))
                case "space-new": present(.newSpace)
                case .some(let p) where p != "assistant": showProfile = true
                default: break
                }
                // `-openShellPage inbox|machines|routines|knowledge|profile` (the new shell): that page pushed.
                let atRoot = (navPath.wrappedValue.count, showRecordings)
                switch UserDefaults.standard.string(forKey: "openShellPage") {
                case "inbox": show(.inbox)
                case "todos": show(.todos)
                case "machines": show(.machines)
                case "routines": show(.routines)
                case "knowledge": show(.knowledge)
                case "profile": show(.profile)
                case "health": backToRoot(); showHealth = healthShellPage != nil // the page itself, for its screenshots
                case "recordings": show(.recordings)
                default: break
                }
                // A conversation picked after the push (a launch with `-openChatsFresh` reads it from the hub, late on a
                // busy Mac) goes back to the root and takes Recordings with it: pushed again while that can happen. Only
                // where the push shows in this stack's state (the sidebar layout picks a root instead).
                if UserDefaults.standard.string(forKey: "openShellPage") == "recordings",
                   (navPath.wrappedValue.count, showRecordings) != atRoot {
                    for _ in 0..<10 {
                        try? await Task.sleep(for: .milliseconds(500))
                        if (navPath.wrappedValue.count, showRecordings) == atRoot { show(.recordings) }
                    }
                }
            }
            .onAppear {
                // `-openAssistantSidebar 0.5`: held part way out, as a drag leaves it (screenshots of its corners).
                let sidebarAt = UserDefaults.standard.double(forKey: "openAssistantSidebar")
                if sidebarAt > 0 { activeDrawer.progress = min(sidebarAt, 1) }
                else if UserDefaults.standard.bool(forKey: "openAssistantSidebar") { activeDrawer.progress = 1 }
                #if os(iOS)
                let rounds = UserDefaults.standard.integer(forKey: "showDrawerAutoDrag")
                if rounds > 0 { Task { await DrawerAutoDrag(drawer: activeDrawer).run(rounds: rounds) } }
                #endif
            }
            #endif
            .task { await model.refreshAssistant() }
            // New Main Chat… from the top bar's menu or the sidebar's (#1762).
            .sheet(isPresented: Binding(get: { model.newMainAsked }, set: { model.newMainAsked = $0 })) { NewMainSheet() }
            #if DEBUG
            .task { // `-showNewMain YES`: the question up, for screenshots
                if UserDefaults.standard.bool(forKey: "showNewMain") { try? await Task.sleep(for: .seconds(3)); model.newMainAsked = true }
            }
            .task { // `-showClearedMain YES`: clear the main conversation once the hub offers it, for screenshots
                guard UserDefaults.standard.bool(forKey: "showClearedMain") else { return }
                let group = { model.hub?.group ?? "" }
                for _ in 0..<40 where model.assistantMain == nil
                    || !model.features(group()).contains(MainConversationActions.clearFeature) {
                    try? await Task.sleep(for: .milliseconds(250))
                }
                guard model.assistantMain != nil else { return }
                _ = await model.clearMainContext()
            }
            #endif
    }

    /// The Sidebar layout's page beside its column takes a task's detail on its path (#13156); elsewhere it is a sheet, or
    /// in regular width pushed by view from the conversation as before (`detailPresentation`).
    private var taskDetailByValue: Bool {
        #if os(iOS)
        embedded && widthClass == .regular
        #else
        false
        #endif
    }

    /// The sidebar's places push by value (`ShellRoute`, `ShellDestinations`): the iPhone shell and the iPad's column.
    private var placesByValue: Bool { shell != nil || padPlaces || embedded }

    /// Offered at the stack and registered with its destinations, so a wide split does not depend on the outer stack
    /// forwarding this environment value to its destination pages.
    private var inboxChatPush: ((InboxChatRoute) -> Void)? {
        guard placesByValue else { return nil }
        return { route in
            #if DEBUG
            ShellTrace.shared.note("inbox chat push place=\(String(describing: places.current))")
            #endif
            navPath.wrappedValue.append(route)
        }
    }

    /// A page the shell asked for (the drawer, the home page): from the conversation's root, so back comes to it. A place
    /// comes back as it was left, the place in front is kept for coming back to it, and the place already in front goes
    /// to its root (`ShellPlaces`, docs/design/sidebar-navigation.md). Under the open drawer the page switches without
    /// animation, then the drawer closes: one motion, not a push and a slide while the drawer closes (#6599).
    private func show(_ page: ShellPage?) {
        guard let page else { return }
        #if os(iOS)
        if model.returnToStartIfHidden(page.navigationPage) { return }
        #endif
        // A To-dos place the sidebar picked (#10465): To-dos opens on it at its root, what it kept being the place
        // before's. Its own row is All To-dos where To-dos is the place in front.
        let fresh = page == .todos && TodoWorkspace.shared.opening(inFront: todosInFront)
        #if os(iOS)
        if embedded, let unified { return showRoot(page, in: unified, fresh: fresh) }
        #endif
        let kept = placesByValue ? places.enter(page, from: navPath.wrappedValue, fresh: fresh) : nil
        #if DEBUG
        ShellTrace.shared.note("place \(page) \(kept == nil ? "root" : "kept") drawer=\(activeDrawer.isOut) path=\(navPath.wrappedValue.count) kept=\(kept?.count ?? 0)")
        #endif
        // The Inbox at its root: chosen again while in front, its page stays up and is told (#9053). A kept path puts
        // a new page beneath its item, which lands on that item's choice.
        if page == .inbox {
            if kept == nil { model.inboxEntries += 1 } else { InboxBarLanding.shared.broughtBack() }
        }
        underDrawer {
            backToRoot()
            if let kept {
                navPath.wrappedValue = kept
                return
            }
            switch page {
            case .newConversation: newConversation()
            case .profile: if placesByValue { navPath.wrappedValue.append(ShellRoute.profile) } else { showProfile = true }
            case .routines: if placesByValue { navPath.wrappedValue.append(ShellRoute.routines) } else { showRoutines = true }
            case .knowledge: if placesByValue { navPath.wrappedValue.append(ShellRoute.knowledge) } else { showKnowledge = true }
            #if os(iOS)
            case .starred: navPath.wrappedValue.append(ShellRoute.starred)
            #endif
            case .inbox: navPath.wrappedValue.append(ShellRoute.inbox)
            case .todos: navPath.wrappedValue.append(ShellRoute.todos)
            case .machines: navPath.wrappedValue.append(ShellRoute.machines)
            case .extensionPage(let r): navPath.wrappedValue.append(ShellRoute.extensionPage(r))
            case .health: enterHealth()
            case .space(let slug): enterSpace(slug)
            case .newSpace: present(.newSpace)
            case .recordings: if placesByValue { navPath.wrappedValue.append(ShellRoute.recordings) } else { showRecordings = true }
            case .conversation: break // back at the conversation (`backToRoot`), the place's path kept (`ShellPlaces`)
            case .messages: navPath.wrappedValue.append(SidebarRoute.messages)
            }
        }
        if let shell, shell.drawer.isOut { shell.drawer.close() }
    }

    /// An Inbox item opened from outside the Inbox (a Needs-you line over the composer): pushed over the Inbox, the list
    /// landed on the item's choice, so closing the item shows the list where the item lives (#9053, docs/assistant/inbox.md
    /// "Section bar"). What the Inbox kept open gives way to it. A page without places pushes the item alone, as before.
    private func openFromOutside(_ item: InboxItem) {
        #if os(iOS)
        if model.returnToStartIfHidden(.inbox) { return }
        #endif
        guard placesByValue else { return navPath.wrappedValue.append(item) }
        // the item in front before the list beneath first appears, whichever of the two appears first
        InboxBarLanding.shared.appeared(item)
        #if os(iOS)
        if embedded, let unified {
            traceRoot(.inbox, unified.select(.page(ShellRoute.inbox), path: navPath))
            navPath.wrappedValue = NavigationPath([item])
            return
        }
        #endif
        _ = places.enter(.inbox, from: navPath.wrappedValue)
        var path = NavigationPath()
        path.append(ShellRoute.inbox)
        path.append(item)
        navPath.wrappedValue = path
    }

    /// Whether To-dos is the place in front, whatever is pushed on it: the Sidebar layout's root, else the place this
    /// page shows over the conversation (the shell's and the iPad's), else the classic page. `show` asks it on every
    /// platform, visionOS included.
    private var todosInFront: Bool {
        #if os(iOS)
        if embedded, let unified { return unified.shownPlace == .todos }
        #endif
        return placesByValue ? places.current == .todos : showTodos
    }

    #if os(iOS)
    /// The Sidebar layout (#8359): each page the sidebar opens is a root of the page beside it, the conversation one of
    /// them. Picking one replaces the root at once, the one left keeping what was opened on it; picking the one in front
    /// goes back to it (`SidebarPageRoots`). The shaded row follows the root (`UnifiedSidebar.shownPlace`, `atRoot`).
    private func showRoot(_ page: ShellPage, in unified: UnifiedSidebar, fresh: Bool = false) {
        switch page {
        case .newConversation:
            traceRoot(page, unified.select(.conversation, path: navPath, contentChanged: true))
            newConversation()
        case .health:
            traceRoot(page, nil)
            enterHealth()
        case .space(let slug):
            traceRoot(page, nil)
            enterSpace(slug)
        default:
            let step = SidebarPageRoot.of(page).map { unified.select($0, path: navPath, fresh: fresh) }
            if page == .inbox, let step { inboxPicked(step) }
            traceRoot(page, step)
        }
    }

    /// The Inbox picked in the Sidebar layout (#9053): picked again while it is the root, its page stays up and is told,
    /// as every entry opens on Needs You; brought back with what it kept open, its new page lands on that item's choice.
    private func inboxPicked(_ step: SidebarPageRoots.Step) {
        switch step.move {
        case .none, .popToRoot: model.inboxEntries += 1
        case .switchRoot: if !step.path.isEmpty { InboxBarLanding.shared.broughtBack() }
        case .drillDown: break
        }
    }

    /// What a pick did to the page (`ShellTrace`, DEBUG): the move the UI tests read.
    private func traceRoot(_ page: ShellPage, _ step: SidebarPageRoots.Step?) {
        #if DEBUG
        ShellTrace.shared.note("root \(page) move=\(step.map { "\($0.move)" } ?? "health") path=\(navPath.wrappedValue.count)")
        #endif
    }

    /// A root of the Sidebar layout's page other than the conversation (#8359): a place, Health or Messages, or what the
    /// Agents list opened. Drawn as the page that value is pushed as elsewhere, the first page whatever is pushed on it.
    @ViewBuilder private func sidebarRootPage(_ value: AnyHashable) -> some View {
        Group {
            if let route = value.base as? ShellRoute {
                if let inboxAccounts {
                    ShellDestinations.place(route, path: navPath, accounts: inboxAccounts)
                        .modifier(InboxDestinationEnvironment(chat: inboxChatPush, openFeishu: { navPath.wrappedValue.append($0) }))
                }
            } else if let route = value.base as? SidebarRoute {
                sidebarPage(route)
            } else {
                SidebarAgentsRootPage(value: value)
            }
        }
        .id(value)
        .sidebarRootPage(ownsBack: value.base is SessionKey)
    }
    #endif

    /// Another assistant or hub: the pages pushed, and every place's or root's kept path, were the one before's.
    private func forgetPlaces() {
        places.forget()
        #if os(iOS)
        if embedded, let unified { unified.forget(path: navPath, keepingAsked: true) }
        #endif
        backToRoot()
    }

    /// Leaves the place in front for the conversation, keeping its path (`ShellPlaces`). The Sidebar layout: back at the
    /// conversation root, another conversation's starting at its root (#8359).
    private func leavePlace() {
        #if os(iOS)
        if embedded, let unified {
            unified.select(.conversation, path: navPath, contentChanged: unified.atRoot)
            return
        }
        #endif
        places.leave(with: navPath.wrappedValue)
        underDrawer { backToRoot() }
    }

    /// A direction's page (#11516, `DirectionPage`), pushed one level over its leader conversation: ‹ and the edge swipe
    /// come back to the conversation. From the title menu it is pushed as any page. From the sidebar the conversation
    /// becomes the current place with the page already over it, without animation, and then the drawer closes: an
    /// exception to a sidebar pick being a root (docs/design/DESIGN_SYSTEM.md 3.5), since the page is the conversation's
    /// own drill-down and its ‹ has somewhere to go. On the iPad the same happens in the detail column.
    private func openDirectionPage(_ ask: DirectionPages.Ask) {
        let route = DirectionPageRoute(id: ask.id)
        guard ask.fromSidebar else { return navPath.wrappedValue.append(route) }
        model.shellSettings = false
        shell?.select(0, animated: false)
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) {
            // The place or root in front is left as any pick leaves it, and the conversation starts afresh: the page is
            // the one page over it, never on a path the conversation was left with (an earlier page, an Agent's chat).
            #if os(iOS)
            if embedded, let unified {
                unified.select(.conversation, path: navPath, contentChanged: true)
            } else {
                places.leave(with: navPath.wrappedValue)
            }
            #else
            places.leave(with: navPath.wrappedValue)
            #endif
            backToRoot()
            if current != ask.lead {
                directionLanding = ask.lead
                model.assistantCurrent = ask.lead
            }
            navPath.wrappedValue = NavigationPath([route])
        }
        activeDrawer.close()
    }

    /// While the drawer is out the page is under it: it changes at once, and the drawer's closing is the only motion.
    private func underDrawer(_ change: () -> Void) {
        guard activeDrawer.isOut else { return change() }
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t, change)
    }

    /// The open conversation is the health space's.
    private var currentIsHealth: Bool { current.flatMap { model.session($0) }?.isHealth == true }

    /// Health's page, from the `health` module (`ModuleSlot.shellPlaces`, #14278); nil while it is off.
    private var healthShellPage: ModuleShellPlace? { Modules.registry.shellPage(id: SidebarRoute.healthPage) }

    /// Health's home with what it shares with Persona's new conversation: the launcher, model, mode and effort picked in
    /// the shell composer, and a start in the health space.
    @ViewBuilder private var healthPage: some View {
        if let page = healthShellPage {
            page.page(PersonaHostApp.ShellContext(disabled: shownStart != nil, launcher: healthLauncherPick, launcherModel: healthModelPick,
                                                  mode: $starterMode, effort: $starterEffort) { text, images in
                Task { await start(text, images, space: Session.healthSpace) }
            })
        }
    }

    /// Health from the sidebar or the shell opens its home, with the space's conversations listed (#13273); nothing
    /// while the health module is off.
    private func enterHealth() {
        backToRoot()
        guard healthShellPage != nil else { return }
        present(.health)
    }

    /// The open conversation's domain space (#13500), when it is one other than health's.
    private var currentSpace: String? {
        guard let s = current.flatMap({ model.session($0) }), s.isDomainSpace else { return nil }
        return s.assistantSpace
    }

    /// The top bar's New: in a health conversation Health's new conversation page, whose list has the space's past ones
    /// (#4329); in a domain space's, that space's home (#13500); elsewhere the assistant's.
    private var newFromTopBar: () -> Void {
        if let slug = currentSpace { return { backToRoot(); present(.space(slug)) } }
        if currentIsHealth, healthShellPage != nil { return { backToRoot(); present(.health) } }
        return newConversation
    }

    /// A domain space from the sidebar (#13500), as Health: back in its latest conversation, else its home.
    private func enterSpace(_ slug: String) {
        Task {
            switch await SpaceHomeModel.entry(slug: slug, deps: .live, live: model.spaceConversations) {
            case .open(let key):
                #if os(iOS)
                if embedded, let unified { unified.select(.conversation, path: navPath, contentChanged: true) }
                #endif
                backToRoot()
                model.openAssistantConversation(key)
            case .new:
                backToRoot()
                present(.space(slug))
            }
        }
    }

    /// The chips of the open space conversation's categories, under the top bar, measured for the room they take.
    @ViewBuilder private var spaceCategories: some View {
        if let key = current, pending == nil, currentSpace != nil {
            SpaceConversationCategories(sessionID: key.sessionID)
                .id(key)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { categoriesHeight = $0 }
                .padding(.top, AssistantTopBar.room(railEdge))
        }
    }

    /// A domain space's page: its home, or the new space form, which opens the space it makes.
    @ViewBuilder private func spacePage(_ page: DomainSpacePage) -> some View {
        switch page {
        case .home(let slug):
            SpaceHomeView(slug: slug, disabled: shownStart != nil, launcher: healthLauncherPick, launcherModel: healthModelPick,
                          mode: $starterMode, effort: $starterEffort,
                          send: { text, images in Task { await start(text, images, space: slug) } },
                          leave: { leavePlace() })
        case .new:
            NewSpaceView { track in
                backToRoot()
                present(.space(track.slug))
            }
        }
    }

    /// A page over the conversation: by value on the Sidebar layout's one path (#6332), so what the sidebar opens next
    /// replaces it; by its flag otherwise, as before.
    private func present(_ route: SidebarRoute) {
        #if os(iOS)
        if embedded, let unified { unified.select(.page(AnyHashable(route)), path: navPath); return }
        #endif
        if embedded { navPath.wrappedValue.append(route); return }
        switch route {
        case .health: showHealth = true
        case .space(let slug): openedSpace = .home(slug)
        case .newSpace: openedSpace = .new
        case .messages: break // the Sidebar layout's only
        }
    }

    #if os(iOS)
    /// The Sidebar layout's pages by value (`SidebarRoute`), drawn as the flags draw them in the other layouts.
    @ViewBuilder private func sidebarPage(_ route: SidebarRoute) -> some View {
        switch route {
        case .messages:
            LarkConversationsView().modifier(InboxDestinationEnvironment(chat: inboxChatPush, openFeishu: { navPath.wrappedValue.append($0) }))
        case .health:
            healthPage
            .modifier(InboxDestinationEnvironment(chat: inboxChatPush, openFeishu: { navPath.wrappedValue.append($0) }))
        case .space(let slug):
            spacePage(.home(slug))
        case .newSpace:
            spacePage(.new)
        }
    }
    #endif

    private func backToRoot() {
        if !navPath.wrappedValue.isEmpty { navPath.wrappedValue = NavigationPath() }
        showProfile = false
        showRoutines = false
        showKnowledge = false
        showTodos = false
        openedExtension = nil
        showHealth = false
        openedSpace = nil
        showRecordings = false
        #if os(iOS)
        // A task pushed beside the sidebar (regular width, `detailPresentation`) is a page of this stack too: going
        // back to the root, or to another conversation, takes it away (else the next chat would push it again). A
        // phone's task sheet stays as it was.
        if widthClass == .regular {
            TasksStore.shared.detailTask = nil
            TasksStore.shared.detailAgent = nil
        }
        #endif
    }

    private func newConversation() {
        let from = NewChatCheck.name(current, main: model.assistantMain)
        #if DEBUG
        ShellTrace.shared.note("new conversation from \(from)")
        #endif
        if from != "none" {
            // The page must come to the new-conversation page; a miss is kept and reported, labels only (#2189).
            NewChatCheck.shared.watch(from: from, current: { [model] in NewChatCheck.name(model.assistantCurrent, main: model.assistantMain) },
                                      miss: { [model] line in Self.newChatMissed(line, model: model) })
        }
        // From an open conversation, New chat is the welcome page. The empty page after Clear context stays only
        // when New chat is tapped from that page (nothing is open) (#5100).
        model.forgetMainClearForNewChat(from: current)
        withAnimation(Theme.Motion.standard) {
            model.assistantCurrent = nil
            pending = nil
        }
    }

    /// New Chat did not bring the new-conversation page: the line is kept on the device, and sent where the hub takes
    /// error reports.
    private static func newChatMissed(_ line: String, model: AppModel) {
        UserDefaults.standard.set("\(Date.now.formatted(.iso8601)) \(line)", forKey: NewChatCheck.defaultsKey)
        #if os(iOS)
        if ErrorReportQueue.shared.write(ErrorReports.report(kind: "log", level: "warning", message: line)), let group = model.hub?.group {
            model.flushErrorReports(group)
        }
        #endif
    }

    /// Clear context left this manager with no main conversation (#5100): an empty middle, the same top bar and composer.
    /// A different main, another manager, or a conversation that is open shows the ordinary page.
    private var showsClearedMain: Bool {
        model.mainClear.showsPage(group: model.hub?.group ?? "", manager: model.assistantManagerID ?? "",
                                  currentEmpty: current == nil, mainSessionID: model.assistantMain?.sessionID)
    }

    /// The first message the page shows: the one spinning up, else one whose start failed (#1368).
    private var shownStart: PendingStart? {
        pending ?? shownFailure.map { PendingStart(text: $0.text, imageCount: $0.images.count, failed: $0) }
    }

    /// The failed first message this page shows: the latest sent from the manager it shows, or from Health.
    private var shownFailure: FailedStart.Message? {
        failedStart.messages.last { $0.space != nil || model.assistantShows($0.manager) }
    }

    /// Health's launcher pick: home's with one manager. A space's home shares it on purpose (#13500): every space runs
    /// on the default manager as Health does, with Health's launcher setting as the fallback, so one pick serves them.
    private var healthLauncherPick: Binding<String?> { model.managerRoster?.isMulti == true ? $healthLauncher : $starterLauncher }
    private var healthModelPick: Binding<String?> { model.managerRoster?.isMulti == true ? $healthModel : $starterModel }

    /// Resend on a failed first message: the op id a chat's Resend would choose (`FailedStart.resendOp`), then the start.
    private func resend() {
        guard let m = shownFailure, let api = model.api else { return }
        if m.space == nil, let offline = model.assistantManagerOffline {
            model.toast = Toast(ManagerOfflineNote.text(offline), style: .error)
            return
        }
        let receipts = model.hubFeatures.contains(AssistantStart.feature)
        Task {
            let op = await FailedStart.resendOp(m, api: api, receipts: receipts)
            guard m.space != nil || model.showsPage(of: api) else { return } // another manager's page by now
            await start(m.text, m.images, space: m.space, op: op)
        }
    }

    private func deleteFailed() {
        model.readiness.failure = nil
        guard let m = shownFailure else { return }
        withAnimation(Theme.Motion.standard) { failedStart.clear { $0 == m } }
        // As a chat's Delete (#4626): Undo puts it back on its page.
        model.toast = .messageDeleted { [failedStart] in
            withAnimation(Theme.Motion.standard) { failedStart.fail(m) }
        }
    }

    /// `space`: the space the conversation opens in (Health passes "health"; it never becomes the main conversation).
    /// `op`: its op id (`AssistantStart`); Resend passes the one it chose.
    private func start(_ text: String, _ images: [ImagePayload], space: String? = nil, op: UUID = UUID()) async {
        guard let api = model.api else { return }
        model.readiness.failure = nil
        // From Health: back to the root, which shows the message coming as for any new conversation.
        if space != nil {
            backToRoot()
            model.assistantCurrent = nil
        }
        withAnimation(Theme.Motion.standard) { pending = PendingStart(text: text, imageCount: images.count, op: op) }
        let newChat = model.assistantNewChat
        do {
            // The first one becomes the main conversation; with the settings not read yet, whether there is one is unknown.
            let makesMain = space == nil && model.assistantConfig != nil && model.assistantMain == nil
            // Nothing picked names neither, so the hub keeps no pick over Main chat's default (#11472).
            let pick = AssistantStarterChoice.request(launcher: space == nil ? starterLauncher : healthLauncherPick.wrappedValue,
                                                      model: space == nil ? starterModel : healthModelPick.wrappedValue)
            // Suggestions and Resend share the cached check. Discovery never delays sending or clears an unknown choice.
            if !starterEffort.isEmpty {
                // Every space is the default manager's, Health's included (#13500).
                let machine = model.hubRef(space != nil ? model.healthMachineID : model.assistantConfig?.machineID ?? model.hubMachineID)
                let fallback = space != nil ? model.healthLauncherSetting : model.starterLauncherSetting
                let manager = space != nil ? nil : api.currentAssistantSettings().managerID
                let source = AssistantStarterModels.source(machine, launcher: pick.launcher, model: pick.model, fallback: fallback,
                                                           manager: manager, revision: model.assistantLauncherRequestRevision)
                starterEffort = model.assistantStarterCache.retained(starterEffort, source: source)
            }
            let s = try await AssistantStart.run(receipts: model.hubFeatures.contains(AssistantStart.feature), op: op) { op in
                try await api.startConversation(prompt: text, images: images, launcher: pick.launcher, model: pick.model,
                                                permissionMode: starterMode.isEmpty ? nil : starterMode,
                                                effort: starterEffort.isEmpty ? nil : starterEffort,
                                                main: makesMain, space: space, opID: op)
            }
            if makesMain { Task { await model.refreshAssistant() } }
            let decision = AssistantStartDecision(startGeneration: newChat, currentGeneration: model.assistantNewChat,
                                                  pendingOp: pending?.op, op: op, succeeded: true,
                                                  space: space, showsPage: model.showsPage(of: api))
            if decision.haptic == .success { Haptics.success() }
            if decision.began { model.began(s, text: text, images: images) } // keep it until claude has written it
            withAnimation(Theme.Motion.standard) {
                // Another manager's page by now: the conversation is listed on its own page, not opened on this one.
                if decision.open { model.assistantCurrent = s.key }
                if decision.clearPending { pending = nil }
                if decision.failure == .clear {
                    failedStart.clear { $0.space == space && (space != nil || model.sameManager($0.manager, api.shownManager)) }
                }
            }
        } catch {
            // A coded refusal (no computer, Claude Code missing…) is a card with what to do, not a red line (5.2).
            // The message stays where it was sent, as the chat's failed bubble, with Resend and Delete (#1368).
            let coded = CodedFailure(error)
            let decision = AssistantStartDecision(startGeneration: newChat, currentGeneration: model.assistantNewChat,
                                                  pendingOp: pending?.op, op: op, succeeded: false,
                                                  space: space, showsPage: model.showsPage(of: api))
            withAnimation(Theme.Motion.standard) {
                if decision.clearPending { pending = nil }
                if decision.failure == .show { model.readiness.failure = coded }
                if decision.failure == .show || decision.failure == .record {
                    failedStart.fail(FailedStart.Message(text: text, images: images, space: space, op: op,
                                                         mayHaveStarted: FailedStart.mayHaveStarted(error),
                                                         reason: coded == nil || decision.failure != .show ? error.shown : nil, at: .now,
                                                         manager: space == nil ? api.shownManager : nil))
                }
            }
            if decision.failure == .show, coded != nil { model.readinessChanged() }
            if decision.haptic == .error { Haptics.error() }
        }
    }
}

struct PendingStart: Equatable {
    var text: String
    var imageCount: Int
    /// A start that failed (#1368): drawn as the chat's failed bubble, with Resend and Delete, and no typing line.
    var failed: FailedStart.Message? = nil
    /// Which invocation owns the sending bubble; a late completion never clears a newer send's.
    var op: UUID? = nil
}

/// The first message of a new conversation while it spins up, laid out as the chat will lay it out the moment it takes
/// over: the day, the message in the chat's own bubble, then a typing line, at the bottom over the composer, in the
/// chat's margins. It used to sit at the top of the new-conversation page and jump to the bottom when the chat came in
/// (#1911). `bottom`: the space the chat leaves between its newest row and the composer.
struct FirstMessagePending: View {
    var pending: PendingStart
    var name: String
    var shell: Bool
    /// A failed one's Resend and Delete (#1368).
    var resend: () -> Void = {}
    var delete: () -> Void = {}

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) { // the chat's rows' spacing
            if let failed = pending.failed {
                // As the chat keeps a message that did not go: the red mark's menu sends it again or deletes it.
                DayLine(date: failed.at)
                PendingBubble(msg: PendingMessage(id: failed.op, text: failed.text, images: failed.images, state: .failed, createdAt: failed.at),
                              retry: resend, discard: delete)
                    .chatMark("chat.outbox.failed.\(failed.op.uuidString)")
            } else {
                DayLine(date: .now)
                VStack(alignment: .trailing, spacing: Theme.Space.sm) {
                    if pending.imageCount > 0 {
                        Label("\(pending.imageCount) images", systemImage: Theme.Icon.photo).font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                    HStack {
                        Spacer(minLength: 48)
                        UserBubbleBody(quotes: [], text: pending.text)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .trailing)
                .chatMark("chat.outbox.sending.start")
                TypingIndicator(text: String(localized: "Sending…"))
            }
        }
        .padding(.horizontal, Theme.Space.l) // the chat's list margins
        .frame(maxWidth: ChatView.readableWidth)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
    }
}

/// A new conversation outside the compact shell and a phone's split (the iPad, the Mac, Apple Vision Pro):
/// `AssistantWelcome` (greeting, capabilities, one-tap suggestions); while a conversation spins up it shows the first
/// message and a typing indicator so the send feels instant.
struct AssistantHome: View {
    @Environment(AppModel.self) private var model
    var pending: PendingStart?
    var error: String?
    /// Clear context (#5100): the same composer, an empty middle. Not the centered new-chat page.
    var cleared = false
    @Binding var launcher: String?
    /// The model picked under `launcher` (#11472): the iPad's model menu sets it; the Mac's launcher chip leaves it nil.
    @Binding var launcherModel: String?
    @Binding var mode: String
    @Binding var effort: String
    var resend: () -> Void = {}
    var delete: () -> Void = {}
    var send: (String, [ImagePayload]) -> Void
    @State private var route: ReadinessRoute?
    @State private var starterModels = AssistantStarterModels()

    private var starterMachine: MachineRef { model.hubRef(model.assistantConfig?.machineID ?? model.hubMachineID) }
    private var starterManager: String? { model.api(starterMachine.group)?.currentAssistantSettings().managerID }
    private var effortSource: String {
        AssistantStarterModels.source(starterMachine, launcher: launcher, model: launcherModel, fallback: model.starterLauncherSetting,
                                      manager: starterManager, revision: model.assistantLauncherRequestRevision)
    }

    private func sendChecked(_ text: String, _ images: [ImagePayload]) {
        if !ChatComposerKind.padCard { effort = starterModels.retained(effort, source: effortSource) }
        send(text, images)
    }

    /// How far the page fades out above the chips and the field.
    private static let fade = Theme.Space.xl

    /// Nothing can be sent: a first message is spinning up, or the manager the page shows is offline.
    private var blocked: Bool { pending != nil || model.assistantManagerOffline != nil }

    var body: some View {
        ScrollView {
            if let pending {
                FirstMessagePending(pending: pending, name: model.assistantShownName, shell: false, resend: resend, delete: delete)
                    // The page's height inside its margins, the message at its bottom (a long one runs up out of it, as the
                    // chat shows its newest end).
                    .containerRelativeFrame(.vertical, alignment: .bottom)
            } else if cleared {
                Color.clear
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .accessibilityIdentifier("main-cleared")
            } else {
                VStack(alignment: .leading, spacing: Theme.Space.xl) {
                    AssistantWelcome(send: sendChecked, openHelp: { route = .help($0, machine: nil) })
                }
                .padding(.horizontal, Theme.Space.xl)
                .padding(.bottom, Theme.Space.xl)
                // The conversation's measure on a wide display (the iPad).
                .frame(maxWidth: ChatView.readableWidth)
                .frame(maxWidth: .infinity)
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .dismissesKeyboardOnTap()
        // The floating top bar (AssistantTopBar) is over the page: leave its room, let the page scroll under it. Before
        // the composer is added: margins reach every scroll view below them, and the chips' row above the field stood
        // the top bar's height tall, over the suggestion cards.
        .contentMargins(.top, AssistantTopBar.height, for: .scrollContent)
        // Room to scroll the last suggestions clear of the fade over the chips (as ShellHome).
        .contentMargins(.bottom, pending == nil ? Self.fade : ChatView.newestGap, for: .scrollContent)
        .themedGroupedPage()
        .safeAreaInset(edge: .bottom, spacing: 0) {
            // The chips' row a step off the field (2026-11-04: with Theme.Space.s it sat right on it, and the
            // suggestion cards were cut off hard just above it).
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                // A failed first message: why, and what to do when it is a refusal (#1368).
                if pending?.failed != nil, let error {
                    Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        .padding(.horizontal, Theme.Space.s)
                }
                if pending == nil || pending?.failed != nil {
                    VStack(spacing: Theme.Space.m) {
                        PushHealthNotice(group: model.hub?.group ?? "")
                        ReadinessHomeCard(route: $route)
                    }
                }
                if let m = model.assistantManagerOffline { ManagerOfflineNote(manager: m) }
                if ChatComposerKind.padCard {
                    // The iPad: the iPhone's composer, the launcher, mode and effort in its model menu (#2779).
                    StarterComposer(disabled: blocked, shell: StarterComposer.ShellModes(launcher: $launcher, launcherModel: $launcherModel, mode: $mode, effort: $effort),
                                    gate: model.readinessGate(route: $route), offersClear: cleared && pending == nil, send: send)
                } else {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: Theme.Space.s) {
                            // The BeautifulUI style picks the launcher in the prompt bar's model menu.
                            if !AppStyle.current.usesBeautifulUI { StarterModelChip(launcher: $launcher) }
                            NewSessionModePicker(mode: $mode, effort: $effort, modes: SessionModes.assistantModes,
                                                 efforts: starterModels.options(for: effortSource))
                        }
                        .padding(.horizontal, Theme.Space.xs)
                    }
                    if AppStyle.current.usesBeautifulUI {
                        BeautifulStarterComposer(disabled: blocked, launcher: $launcher, gate: model.readinessGate(route: $route),
                                                  offersClear: cleared && pending == nil, send: sendChecked)
                    } else {
                        StarterComposer(disabled: blocked, gate: model.readinessGate(route: $route), offersClear: cleared && pending == nil, send: sendChecked)
                    }
                }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.bottom, Theme.Space.s)
            .frame(maxWidth: ChatView.readableWidth)
            .frame(maxWidth: .infinity)
            // Above the chips the page fades out instead of ending in a hard edge across the cards.
            .background(alignment: .top) {
                LinearGradient(colors: [Theme.groupedPage.opacity(0), Theme.groupedPage], startPoint: .top, endPoint: .bottom)
                    .frame(height: Self.fade)
                    .offset(y: -Self.fade)
                    .allowsHitTesting(false)
                    .accessibilityHidden(true)
            }
            // At large text sizes the page reaches down here: back the chips and the field with the page, or the
            // machine chips scrolled under them read through.
            .background(Theme.groupedPage.ignoresSafeArea(edges: .bottom))
        }
        .keepsHeightUnderUndockedKeyboard() // the composer stays at the bottom under a floating keyboard (#2565)
        .navigationTitle("Persona")
        .task(id: launcher) { if pending == nil { await model.warmAssistant(launcher: launcher) } }
        .task(id: effortSource) {
            guard !ChatComposerKind.padCard else { return }
            if await starterModels.load(starterMachine, launcher: launcher, model: launcherModel, fallback: model.starterLauncherSetting,
                                        manager: starterManager, revision: model.assistantLauncherRequestRevision) {
                effort = starterModels.retained(effort, source: effortSource)
            }
        }
        .onChange(of: launcher) { old, new in if old != new { effort = "" } }
        .modifier(ReadinessRefresh())
        .readinessSheets($route)
    }
}

/// What a new conversation opens on, in both layouts (2026-11-02 the user wanted it back in the shell, which had shown
/// only the composer): a greeting, what Persona can reach, one-tap things to ask.
struct AssistantWelcome: View {
    @Environment(AppModel.self) private var model
    var send: (String, [ImagePayload]) -> Void
    /// Opens local help for a readiness example question that is `local` (never sent to the model).
    var openHelp: ((String) -> Void)? = nil
    /// The cards' rows at most (the shell: two, the page ends at their bottom, with no third row peeking out under the
    /// fade over Needs You, #5648); every suggestion when nil.
    var cardRows: Int? = nil
    /// The token calendar's days (#9506) and its page.
    @State private var usage = TokenUsageModel()
    @State private var showUsage = false
    @Environment(\.horizontalSizeClass) private var sizeClass

    /// A thing to ask: sent as `prompt`, or (`local`) opening help on this device; `needs`: the phone data it reads.
    struct Suggestion {
        var symbol: String
        var title: String
        var prompt: String
        var local: String? = nil
        var needs: [String] = []
    }

    /// The hub's example questions when it has readiness (in `setup` only the two that open help here), else this
    /// app's own (an older hub).
    private var suggestions: [Suggestion] {
        guard let list = model.readiness.current?.suggestions, !list.isEmpty else {
            return Self.builtIn.map { Suggestion(symbol: $0.symbol, title: $0.title, prompt: $0.prompt) }
        }
        return list.map { s in
            Suggestion(symbol: Self.symbol(s.id), title: s.title, prompt: s.prompt ?? s.title,
                       local: s.local == true ? s.id : nil, needs: s.needs ?? [])
        }
    }

    private static func symbol(_ id: String) -> String {
        switch id {
        case "what_can_you_do": Theme.Icon.help
        case "how_to_connect_model": Theme.Icon.model
        case "largest_folders": Theme.Icon.folder
        case "today": Theme.Icon.pluginCalendar
        case "connect_mail": Theme.Icon.mailAccounts
        default: Theme.Icon.ai
        }
    }

    /// Before a question that reads phone data goes out: which permission the system is about to ask for.
    private func ask(_ s: Suggestion) {
        Haptics.tap()
        if let local = s.local {
            openHelp?(ReadinessRoute.helpTopics.contains(local) ? local : "what_can_you_do")
            return
        }
        if s.needs.contains(where: { $0.hasPrefix("phone_calendar") }) {
            model.toast = Toast(String(localized: "Next, your iPhone will ask you to let Persona read your calendar"))
        } else if !s.needs.isEmpty {
            model.toast = Toast(String(localized: "Next, your iPhone will ask whether Persona may read this data"))
        }
        send(s.prompt, [])
    }

    private static let builtIn: [(symbol: String, title: String, prompt: String)] = [
        ("calendar", String(localized: "Today's Schedule"), String(localized: "Check my schedule for today: look at both my iPhone calendar and Lark calendar, merge them, and list everything by time.")),
        (Theme.Icon.mailAccounts, String(localized: "Emails to Handle"), String(localized: "Which recent emails in my inbox need me to act or reply? Just the important ones.")),
        (Theme.Icon.machines, String(localized: "What My Computers Are Doing"), String(localized: "What are the agents on my computers doing right now? Which are waiting on me, and which finished but I haven't looked at yet?")),
        (Theme.Icon.feishuChats, String(localized: "Lark Messages"), String(localized: "Summarize the important messages I got on Lark today and which ones need a reply from me.")),
        (Theme.Icon.meeting, String(localized: "Meeting Prep"), String(localized: "Look at my remaining meetings today, pick the most important one, and help me prepare: agenda, related docs, last meeting's notes, and open to-dos.")),
        ("heart.text.square", String(localized: "This Week's Health"), String(localized: "Using the health data on my iPhone, summarize my steps, sleep, and workouts this week.")),
    ]

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xl) {
            header
            // An offline manager takes nothing: no things to ask, which would send.
            if model.assistantManagerOffline == nil { things }
        }
        .coordinateSpace(.named(WelcomeFold.space))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("assistant-welcome")
        // Read when the page shows and when the app comes back to the front: no timer (the home stays still).
        .tabActiveTask(id: sizeClass == .regular) {
            await usage.loadDays(sizeClass == .regular ? TokenUsageModel.yearDays : TokenUsageModel.phoneDays)
        }
        .navigationDestination(isPresented: $showUsage) { TokenUsagePage() }
        #if DEBUG
        .onAppear { if UserDefaults.standard.bool(forKey: "openTokenUsage") { showUsage = true } } // screenshots
        #endif
    }

    @ViewBuilder private var things: some View {
        if AppStyle.current.usesBeautifulUI {
            // BeautifulUI's list card: one row per thing to ask, its mark, what it is and how it is put.
            BUIListSection(String(localized: "Things to Ask")) {
                ForEach(suggestions, id: \.title) { s in
                    Button {
                        ask(s)
                    } label: {
                        BUIListRow(s.title, detail: s.local == nil ? s.prompt : nil, accessory: .disclosure, detailLines: 1) {
                            IconTile(s.symbol, tint: Theme.accent)
                        }
                    }
                    .buttonStyle(.plain)
                    .modifier(WelcomeFold.Mark(first: s.title == suggestions.first?.title))
                }
            }
        } else if let cardRows {
            // The shell's few cards are all made, wherever they are (#4073): a lazy grid never makes the ones below the
            // page, and at the largest text sizes the greeting already fills it, so the fold was never measured and
            // Needs You took every row it had over the composer.
            Grid(horizontalSpacing: 12, verticalSpacing: 12) {
                let shown = Array(suggestions.prefix(cardRows * 2))
                let rows = stride(from: 0, to: shown.count, by: 2).map { Array(shown[$0..<min($0 + 2, shown.count)]) }
                ForEach(rows, id: \.[0].title) { row in
                    GridRow {
                        ForEach(row, id: \.title) { card($0) }
                    }
                }
            }
        } else {
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)], spacing: 12) {
                ForEach(suggestions, id: \.title) { card($0) }
            }
        }
    }

    private func card(_ s: Suggestion) -> some View {
        Button {
            ask(s)
        } label: {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                ThemedIcon(s.symbol)
                    .font(Theme.Font.title)
                    .foregroundStyle(Theme.accent)
                Text(s.title).font(Theme.Font.heading).foregroundStyle(Color.primary)
            }
            .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.box72, alignment: .topLeading)
            .card(padding: 14)
        }
        .buttonStyle(.plain)
        // Two cards a row, and the first two rows kept clear of Needs You: the fold is where the tallest of the first
        // four ends (#5648: English and Japanese titles run to two lines, and the second row was half under the fade
        // when the fold was the first row's end).
        .modifier(WelcomeFold.Mark(first: suggestions.prefix(WelcomeFold.cards).contains { $0.title == s.title }))
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            Text(greeting).font(Theme.Font.display).padding(.top, Theme.Space.xl)
            Text(model.assistantMain == nil
                 ? String(localized: "This is your main chat: I'll remember what we talk about here. I can see your computers, iPhone, email, Lark, and organized projects, and I can keep an eye on things and remind you when it's time.")
                 : String(localized: "Start a new side chat. I can see your computers, iPhone, email, Lark, and organized projects, and I can keep an eye on things and remind you when it's time."))
                .font(Theme.Font.secondary).lineHeight(.secondary)
                .foregroundStyle(.secondary)
            // Tokens a day (#9506), where the computers' chips were: labels that did nothing (the connection is in the
            // title). Only once the hub has history with usage in it: an older hub, or one with no computers, shows none.
            if usage.hasUsage, let days = usage.days {
                TokenCalendarCard(days: days) { showUsage = true }
            }
        }
    }

    private var greeting: String {
        switch Calendar.current.component(.hour, from: .now) {
        case 5..<11: String(localized: "Good morning")
        case 11..<13: String(localized: "Good afternoon [Assistant View fceb19]")
        case 13..<18: String(localized: "Good afternoon [Assistant View 552594]")
        default: String(localized: "Good evening")
        }
    }
}

/// Where the welcome's things to ask that must stay in view end (the cards' first two rows, the list's first row), from the
/// welcome's top (so scrolling does not move it): the shell keeps what needs the user over the composer below it (`ShellHome`).
struct WelcomeFold: PreferenceKey {
    static let space = "assistant-welcome"
    /// The cards kept in view: two rows of two.
    static let cards = 4
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }

    /// On every thing to ask; the ones kept in view say where they end.
    struct Mark: ViewModifier {
        var first: Bool

        func body(content: Content) -> some View {
            content.background {
                GeometryReader { g in
                    Color.clear.preference(key: WelcomeFold.self, value: first ? g.frame(in: .named(WelcomeFold.space)).maxY : 0)
                }
            }
        }
    }
}

struct TypingIndicator: View {
    var text: String
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var phase = 0.0

    var body: some View {
        if AppStyle.current.usesBeautifulUI { shimmer } else { dots }
    }

    /// The BeautifulUI style: its thinking header while it works, the sparkle and the words with the shimmer across them.
    private var shimmer: some View {
        HStack(spacing: Theme.Space.sm) {
            BUIIcon(.sparkle, size: 13)
                .foregroundStyle(Theme.ai)
            Text(text)
                .font(Theme.Font.secondary)
                .buiShimmer()
        }
        .accessibilityElement(children: .combine)
    }

    private var dots: some View {
        HStack(spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.xs) {
                ForEach(0..<3) { i in
                    Circle()
                        .fill(Theme.accent)
                        .scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                        .opacity(0.35 + 0.65 * max(0, sin(phase + Double(i) * 0.9)))
                }
            }
            Text(text).font(Theme.Font.secondary).foregroundStyle(.secondary).contentTransition(.opacity)
        }
        .onAppear {
            guard !reduceMotion else { return }
            withAnimation(.linear(duration: 1.2).repeatForever(autoreverses: false)) { phase = .pi * 2 }
        }
    }
}

/// Composer for a conversation that does not exist yet, sent through `send` (it starts the conversation). In the shell
/// it is a conversation's composer (#3805): `ShellComposerBar`, the iPhone's attach panel on a tap of + and its menu on
/// touch and hold (#3225), the iPad's + menu, and the same rows (`AttachMoreSection.common`). What differs is what a
/// conversation that is not there yet cannot have: it keeps its text, quotes and attachments in `StarterDraft`, picks the
/// launcher, mode and effort to start with (`StarterModesMenu`), and offers no voice call. Elsewhere (Health, the Mac,
/// visionOS) the chat's input line (`ComposerRow`).
struct StarterComposer: View {
    @Environment(AppModel.self) private var model
    var disabled: Bool
    /// The shell's composer (docs/apple/ios.md「New shell」): the launcher, mode and effort are in its gauge menu.
    var shell: ShellModes? = nil
    /// The field's hint; nil: Ask <Persona's name>… (Health has its own).
    var placeholder: String? = nil
    /// Where picked files land on the assistant's machine: "health" for Health's reports/ (docs/assistant/health.md「Checkup reports」).
    var uploadDest: String? = nil
    /// A domain space's new conversation (#13500): its own draft, and the default manager's machine and launcher, as
    /// Health's. nil: the assistant's own.
    var space: String? = nil
    /// The main conversation cannot answer yet (docs/assistant/onboarding.md 4.4): send goes to what is missing, the draft stays.
    var gate: SendGate? = nil
    /// The cleared main conversation (#5100): `/clear` is listed and does nothing. It never starts the new chat.
    var offersClear = false
    var send: (String, [ImagePayload]) -> Void

    struct ShellModes {
        @Binding var launcher: String?
        /// The model picked under `launcher` (#11472); nil: its default entry, or nothing picked (Main chat's default).
        @Binding var launcherModel: String?
        @Binding var mode: String
        @Binding var effort: String
        /// Health's new conversation (#3825): with none picked, the launcher is Health's (the default manager's).
        var health = false
    }

    @State private var starterModels = AssistantStarterModels()
    @State private var picks: [PhotosPickerItem] = []
    @State private var showPhotos = false
    @State private var showFiles = false
    @State private var showCamera = false
    @State private var moduleSheet: ModuleComposerAction? // a module's composer action whose sheet is up (Glasses Photo)
    @State private var showScan = false // Scan Document (#3720), only where the device can scan
    #if os(iOS)
    /// Add as Context › (#3721): the kind whose picker is open.
    @State private var contextPick: ContextPickKind?
    #endif
    @State private var declarations = ExtensionTextContributions()
    #if os(iOS)
    /// The iPhone's 「+」 panel (`AttachSheet`), as in a conversation.
    @State private var attachPanel = AttachPanel()
    #endif
    @State private var selection: TextSelection?
    @FocusState private var focused: Bool
    @Environment(\.scenePhase) private var phase
    /// What it holds for the next message, kept by the app for the new conversation as a chat's draft is (#2626): the
    /// composer is built again with the whole tree when iOS snapshots the app in another width on its way to the
    /// background (iPhone Duo open), and its text and photos were gone on the way back.
    private var draft: StarterDraft { model.starterDraft(space.map { "space:" + $0 } ?? uploadDest ?? "") }
    /// Health's and every space's new conversation are the default manager's, whichever the Persona page shows.
    private var defaultManager: Bool { uploadDest == Session.healthSpace || space != nil }
    private var text: String {
        get { draft.text }
        nonmutating set { draft.text = newValue }
    }
    private var images: [(UIImage, ImagePayload)] {
        get { draft.held.images }
        nonmutating set { draft.held.images = newValue }
    }
    /// Sessions picked in the @ list (#2319), quoted at the top of the shell's composer.
    private var quotes: [ContextRef] {
        get { draft.quotes }
        nonmutating set { draft.quotes = newValue }
    }
    private var textBinding: Binding<String> { Binding(get: { text }, set: { text = $0 }) }
    private var imagesBinding: Binding<[(UIImage, ImagePayload)]> { Binding(get: { images }, set: { images = $0 }) }
    private var quotesBinding: Binding<[ContextRef]> { Binding(get: { quotes }, set: { quotes = $0 }) }
    private var files: FileAttachments { draft.held.files }
    private var videos: VideoAttachments { draft.held.videos }

    /// Files to the assistant's machine, as cards above the box until the first message carries them (#8361).
    private func upload(_ urls: [URL]) {
        files.start(urls, machine: machine, model: model, dest: uploadDest)
    }

    private var canSend: Bool {
        !disabled && !draft.held.busy
            && (!text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !images.isEmpty || !draft.held.ready.isEmpty || !quotes.isEmpty)
    }

    /// Photos, files, videos and quotes riding along: `/clear` with any of them is a message.
    private var attachedCount: Int {
        images.count + draft.held.ready.count + videos.items.count + quotes.count
    }

    private func submit() {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if offersClear, MainClearCommand.isCommand(t, attachments: attachedCount) {
            text = ""
            return
        }
        if let gate { gate.action(); return } // nothing goes and nothing is queued: the draft stays
        let ready = draft.held.ready
        // Nothing to send: a second ⏎ on the Mac before the field redraws (#785) would start an empty conversation.
        guard !draft.held.busy, !t.isEmpty || !images.isEmpty || !ready.isEmpty || !quotes.isEmpty else { return }
        // The keyboard goes down with the send: the conversation that takes over has a field of its own, and the keyboard
        // coming back up and going down again on the way moved the first message up and down with it (#1911).
        focused = false
        if let shell { shell.effort = starterModels.retained(shell.effort, source: effortSource) }
        send(draft.message(t, files: ready), images.map(\.1))
        draft.sent()
    }

    /// The pictures a message here may carry (the hub's, #12973).
    private var limits: ImageLimits { model.imageLimits(machine.group) }

    private func add(_ img: UIImage) {
        MessagePhotos.add(img, to: &draft.held.images, model: model, limits: limits,
                          spill: MessagePhotos.spill(into: files, machine: machine, model: model, dest: uploadDest))
    }

    /// A scan's pages: the next message's photos while they fit, else one PDF uploaded as a picked file is (#3720).
    private func addScan(_ pages: [UIImage]) {
        #if os(iOS)
        DocumentScanner.deliver(pages, room: limits.roomLeft(holding: images.count), model: model, add: { add($0) }, upload: { upload($0) })
        #endif
    }

    #if os(iOS)
    /// What + offers besides photos and files, as in a conversation (`AttachMoreSection.common`): an agent's quick
    /// commands are not Persona's.
    private var moreSections: [AttachMoreSection] {
        var sections = AttachMoreSection.common(camera: CameraPicker.isAvailable ? { showCamera = true } : nil,
                                                scan: DocumentScanner.isAvailable ? { showScan = true } : nil,
                                                modules: Modules.registry.composerActions, open: { moduleSheet = $0 }, mention: { typeMention() },
                                                context: { contextPick = $0 })
        if let prompts = AttachMoreSection.prompts(declarations.prompts, fill: { prompt in
            text = ExtensionTextContributions.filling(prompt)
            selection = TextSelection(insertionPoint: text.endIndex)
            focused = true
        }) {
            sections.append(prompts)
        }
        if offersClear { sections.append(mainClearSection) }
        return sections
    }

    /// The main conversation's only command, on the cleared page: picking it does nothing.
    private var mainClearSection: AttachMoreSection {
        AttachMoreSection(id: "main-clear", title: String(localized: "Quick Commands [Chat View c813e6]"), rows: [
            AttachMoreRow(id: "attach-main-clear", title: String(localized: "Clear Context /clear"), symbol: Theme.Icon.clearContext, word: "/clear") {
                text = ""
            },
        ], symbol: Theme.Icon.quickCommands)
    }
    #endif

    /// The iPhone's tap on + in the shell's composer; nil on the iPad (its + is the menu) and for the input line.
    private var attachOpener: ((CGRect) -> Void)? {
        #if os(iOS)
        shell == nil ? nil : attachPanel.opener(focused: $focused)
        #else
        nil
        #endif
    }

    #if os(iOS)
    private var attachHost: AttachSheetHost<AttachMoreRows> {
        // past the hub's pictures a pick goes as a file (`add`), so the panel takes as many as the files have room for too
        AttachSheetHost(panel: attachPanel, caption: textBinding,
                        photoRoom: limits.roomLeft(holding: images.count) + max(0, FileAttachments.most - files.items.count),
                        videoRoom: max(0, VideoAttachments.most - videos.items.count),
                        camera: CameraPicker.isAvailable ? { showCamera = true } : nil,
                        files: { showFiles = true }, allPhotos: { showPhotos = true }, showsMore: true,
                        more: { AttachMoreRows(panel: attachPanel, sections: moreSections) }) { outcome in
            switch outcome {
            case .then(let picks, let action):
                Task { await draft.held.addPicked(picks, machine: machine, model: model, limits: limits, dest: uploadDest) }
                action()
            case .closed(let picks):
                Task { await draft.held.addPicked(picks, machine: machine, model: model, limits: limits, dest: uploadDest) }
            case .send(let picks):
                Task {
                    await draft.held.addPicked(picks, machine: machine, model: model, limits: limits, dest: uploadDest)
                    submit() // the gate first, as the box's Send
                }
            }
        }
    }
    #endif

    /// The `@word` before the caret, for the @ list (#2319): the shell's composer only, while it has the keyboard.
    private var mentionToken: MentionToken? {
        guard shell != nil, focused, !disabled else { return nil }
        if TypedText.composingFocused { return nil } // Pinyin still marked: the list waits for the confirmed words
        return MentionToken.at(text, selection: selection)
    }

    /// A new conversation has no files or folder yet: Persona's workspaces, the other sessions, and what a typed
    /// absolute or ~ path lists on the assistant's machine. Health and the frozen Mac/visionOS starters keep theirs.
    private var mentionSource: MentionSource { MentionSource(machine: machine, workspaces: offersWorkspaceMentions) }

    private var offersWorkspaceMentions: Bool {
        #if os(iOS)
        return uploadDest == nil && space == nil
        #else
        return false
        #endif
    }

    private func typeMention() {
        let caret = focused ? PathInsert.range(selection, in: text)?.lowerBound : nil // an unfocused field's is stale
        let r = MentionToken.typing(in: text, caret: caret ?? text.utf16.count)
        text = r.text
        selection = .cursor(utf16: r.caret)
        focused = true
    }

    /// The assistant's machine (the hub's unless set otherwise): files and videos go to its temp directory. Health's is
    /// the default manager's.
    private var machine: MachineRef {
        model.hubRef(defaultManager ? model.healthMachineID : model.assistantConfig?.machineID ?? model.hubMachineID)
    }

    private var starterManager: String? {
        defaultManager ? nil : model.api(machine.group)?.currentAssistantSettings().managerID
    }
    private var effortSource: String {
        AssistantStarterModels.source(machine, launcher: shell?.launcher, model: shell?.launcherModel, fallback: shell.flatMap { model.launcherSetting($0) },
                                      manager: starterManager, revision: model.assistantLauncherRequestRevision)
    }

    /// A PhotosPicker inside a Menu never presents (the menu dismisses first): the buttons present it from here.
    @ViewBuilder private var attachments: some View {
        Button { showPhotos = true } label: { Label("Photos", systemImage: Theme.Icon.photos) }
        Button { showFiles = true } label: { Label("Files", systemImage: Theme.Icon.file) }
        if shell != nil { // the @ list is the shell composer's (#2319)
            Button { typeMention() } label: { Label("Mention a File or Session", systemImage: Theme.Icon.mention).lineLimit(1) }
                .accessibilityIdentifier("composer-menu-mention")
        }
    }

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            if !files.items.isEmpty { FileAttachmentsBar(files: files) }
            if !videos.items.isEmpty { VideoAttachmentsBar(videos: videos) }
            PasteImageChip(focused: focused) { add($0) }
            if !images.isEmpty { ComposerImages.editable(draft.held) } // a tap marks it up, as in a conversation
            if let token = mentionToken {
                ComposerMentionMenu(token: token, text: textBinding, selection: $selection, source: mentionSource) { ref in
                    if let updated = ContextRef.addingMention(ref, to: quotes) { quotes = updated }
                }
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if offersClear {
                let rows = SlashMenuRow.matching(text, in: [.clear])
                if !rows.isEmpty {
                    SlashMenu(rows: rows) { _ in text = "" }
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            if let shell {
                ShellComposerBar(placeholder: placeholder ?? String(localized: "Ask \(model.assistantShownName)"), text: textBinding, selection: $selection, focused: $focused,
                                 canSend: canSend, hasAttachments: !images.isEmpty || !videos.items.isEmpty || !files.items.isEmpty || !quotes.isEmpty,
                                 modesTitle: StarterModesMenu.title(modes: shell, launchers: starterModels.launchers, mainDefault: starterModels.mainDefault, model: model),
                                 quotes: quotesBinding, gate: gate, openAttach: attachOpener,
                                 dictationRequest: uploadDest == nil && space == nil ? model.dictationRequest : nil,
                                 takeDictation: { request in model.takeDictationRequest(request, uploadDest: uploadDest) }, send: submit) {
                    #if os(iOS)
                    // As a conversation's + (#3225): the iPhone's touch and hold, the iPad's tap with Photos and Files first.
                    if attachOpener == nil {
                        Section {
                            Button { showPhotos = true } label: { Label("Photos", systemImage: Theme.Icon.photos) }
                            Button { showFiles = true } label: { Label("Files", systemImage: Theme.Icon.file) }
                        }
                    }
                    AttachMenuItems(sections: moreSections)
                    #else
                    attachments
                    #endif
                } modes: {
                    StarterModesMenu(modes: shell, starter: starterModels, efforts: starterModels.options(for: effortSource)) {
                        Task { await starterModels.loadLists(machine, manager: starterManager, revision: model.assistantLauncherRequestRevision) }
                    }
                }
                .task(id: effortSource) {
                    if await starterModels.load(machine, launcher: shell.launcher, model: shell.launcherModel, fallback: model.launcherSetting(shell),
                                                manager: starterManager, revision: model.assistantLauncherRequestRevision) {
                        shell.effort = starterModels.retained(shell.effort, source: effortSource)
                    }
                    // The menu's model rows under each launcher (#11472); sending never waits for them.
                    await starterModels.loadLists(machine, manager: starterManager, revision: model.assistantLauncherRequestRevision)
                }
            } else {
                ComposerRow(placeholder: placeholder ?? String(localized: "Ask \(model.assistantShownName)…"), text: textBinding, selection: $selection, focused: $focused, canSend: canSend,
                            send: submit, gate: gate) {
                    attachments
                }
            }
        }
        .mediaPicker(isPresented: $showPhotos, selection: $picks, limit: limits.perMessage + VideoAttachments.most)
        .fileUploadPicker(isPresented: $showFiles) { urls in
            // The assistant runs on its machine (the hub's unless set otherwise): files go to that one's temp directory.
            upload(urls)
        }
        #if os(iOS)
        .modifier(attachHost)
        // The Photos library warmed off the main thread, as a conversation's composer does (#4250).
        .task { if attachOpener != nil { AttachLibrary.prewarm() } }
        .modifier(ComposerCaptureSheets(camera: $showCamera, module: $moduleSheet, scan: $showScan, pages: { addScan($0) }) { add($0) })
        .sheet(item: $contextPick) { kind in
            ContextPickerSheet(kind: kind) { ref in
                if !quotes.contains(where: { $0.title == ref.title && $0.text == ref.text }) { quotes.append(ref) }
            }
        }
        // The hub's extensions' prompts for the shell's + list, as a conversation's composer reads them.
        .tabActiveTask(id: ExtensionTextSource(group: model.hubGroup, features: model.features(model.hubGroup),
                                               hub: model.api(model.hubGroup)?.config.url.absoluteString, active: phase == .active && shell != nil)) {
            guard phase == .active, shell != nil else { return }
            await declarations.refreshWhileActive(api: model.api(model.hubGroup))
        }
        #endif
        .onChange(of: picks) { _, items in
            guard !items.isEmpty else { return }
            Task {
                let movies = items.filter(PickedMovie.isVideo) // sent as files (VideoAttach.swift)
                if !movies.isEmpty { videos.start(movies, machine: machine, model: model) }
                await MessagePhotos.load(items.filter { !PickedMovie.isVideo($0) }, model: model) { add($0) }
                picks = []
            }
        }
        .animation(Theme.Motion.standard, value: mentionToken == nil)
        .preference(key: MentionMenuOpen.self, value: mentionToken != nil)
        #if DEBUG
        .task { // `-openMentionMenu YES`: "@" with the keyboard up and its list open (#2319, screenshots)
            guard shell != nil, let draft = UserDefaults.standard.string(forKey: "openMentionMenu") else { return }
            try? await Task.sleep(for: .seconds(4))
            text = draft.hasPrefix("@") ? draft : "@"
            selection = .cursor(utf16: text.utf16.count)
            focused = true
        }
        #endif
    }
}

/// The BeautifulUI style's composer for a conversation that does not exist yet: the prompt bar (`PromptComposer`) with its own text,
/// sent through `send` (it starts the conversation). Its model menu is the launcher the conversation starts with.
struct BeautifulStarterComposer: View {
    @Environment(AppModel.self) private var model
    var disabled: Bool
    @Binding var launcher: String?
    /// The field's hint; nil: Ask <Persona's name>… (Health has its own).
    var placeholder: String? = nil
    /// Where picked files land on the assistant's machine: "health" for Health's reports/ (docs/assistant/health.md「Checkup reports」).
    var uploadDest: String? = nil
    /// A domain space's new conversation (#13500): its own draft, and the default manager's machine and launcher, as
    /// Health's. nil: the assistant's own.
    var space: String? = nil
    /// The main conversation cannot answer yet: send goes to what is missing, the draft stays (the card says what).
    var gate: SendGate? = nil
    /// The cleared main conversation (#5100): `/clear` is listed and does nothing.
    var offersClear = false
    var send: (String, [ImagePayload]) -> Void
    @State private var picks: [PhotosPickerItem] = []
    @State private var launchers: [Launcher] = []
    @State private var showPhotos = false
    @State private var showFiles = false
    @State private var selection: TextSelection?
    @FocusState private var focused: Bool
    /// What it holds for the next message, kept by the app for the new conversation as a chat's draft is (#2626): the
    /// composer is built again with the whole tree when iOS snapshots the app in another width on its way to the
    /// background (iPhone Duo open), and its text and photos were gone on the way back.
    private var draft: StarterDraft { model.starterDraft(space.map { "space:" + $0 } ?? uploadDest ?? "") }
    /// Health's and every space's new conversation are the default manager's, whichever the Persona page shows.
    private var defaultManager: Bool { uploadDest == Session.healthSpace || space != nil }
    private var text: String {
        get { draft.text }
        nonmutating set { draft.text = newValue }
    }
    private var images: [(UIImage, ImagePayload)] {
        get { draft.held.images }
        nonmutating set { draft.held.images = newValue }
    }
    private var quotes: [ContextRef] {
        get { draft.quotes }
        nonmutating set { draft.quotes = newValue }
    }
    private var textBinding: Binding<String> { Binding(get: { text }, set: { text = $0 }) }
    private var imagesBinding: Binding<[(UIImage, ImagePayload)]> { Binding(get: { images }, set: { images = $0 }) }
    private var quotesBinding: Binding<[ContextRef]> { Binding(get: { quotes }, set: { quotes = $0 }) }
    private var files: FileAttachments { draft.held.files }
    private var videos: VideoAttachments { draft.held.videos }

    /// Files to the assistant's machine, as cards above the box until the first message carries them (#8361).
    private func upload(_ urls: [URL]) {
        files.start(urls, machine: machine, model: model, dest: uploadDest)
    }

    /// The pictures a message here may carry (the hub's, #12973).
    private var limits: ImageLimits { model.imageLimits(machine.group) }

    private func add(_ img: UIImage) {
        MessagePhotos.add(img, to: &images, model: model, limits: limits,
                          spill: MessagePhotos.spill(into: files, machine: machine, model: model, dest: uploadDest))
    }

    private var launcherMachine: String { defaultManager ? model.healthMachineID : model.assistantConfig?.machineID ?? model.hubMachineID }
    /// The launcher a new conversation starts with (the user's pick, else the one in Persona settings), or nil while
    /// settings are not known (#4200): no name is made up, and requests omit the field so the hub picks its default.
    private var currentLauncher: String? { launcher ?? (defaultManager ? model.healthLauncherSetting : model.starterLauncherSetting) }
    /// Where files and videos go: the assistant's machine.
    private var machine: MachineRef { model.hubRef(launcherMachine) }

    /// The machine's launchers; until they load, the current one alone, so the button shows what a new
    /// conversation starts with. With no launcher known (#4200) the model button stays hidden.
    private var models: [PromptModel] {
        let list = launchers.map { PromptModel(id: $0.name, name: $0.name, tag: SessionModes.gptModelName($0.model) ?? Fmt.model($0.model) ?? "") }
        guard list.isEmpty, let currentLauncher else { return list }
        return [PromptModel(id: currentLauncher, name: currentLauncher, tag: "")]
    }

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            if currentLauncher == nil { StarterLauncherStatus() }
            if !files.items.isEmpty { FileAttachmentsBar(files: files) }
            if !videos.items.isEmpty { VideoAttachmentsBar(videos: videos) }
            PasteImageChip(focused: focused) { add($0) }
            if !images.isEmpty { ComposerImages.editable(draft.held) } // a tap marks it up, as in a conversation
            PromptComposer(placeholder: placeholder ?? String(localized: "Ask \(model.assistantShownName)…"), text: textBinding, selection: $selection, focused: $focused,
                           hasAttachments: !images.isEmpty || !videos.items.isEmpty || !files.items.isEmpty || !quotes.isEmpty, disabled: disabled,
                           attach: PromptItems.attachments(camera: false),
                           mentions: { PromptItems.sessions(model.sessionsNow, except: nil) },
                           commands: offersClear ? [PromptCommand("/clear", detail: String(localized: "Clear the chat and start over"))] : [],
                           models: models,
                           modelID: currentLauncher ?? "",
                           pickModel: { id in
                               launcher = id
                               Haptics.tap()
                           },
                           onAttach: { source in
                               switch source.id {
                               case "photo": showPhotos = true
                               default: showFiles = true
                               }
                           },
                           onMention: { source in
                               guard let ref = PromptItems.quote(source, in: model.sessionsNow) else { return false }
                               quotes.append(ref)
                               return true
                           },
                           onCommand: { command in
                               guard offersClear, command.name == MainClearCommand.word else { return false }
                               text = ""
                               return true
                           },
                           quotes: quotesBinding,
                           dictationRequest: uploadDest == nil && space == nil ? model.dictationRequest : nil,
                           takeDictation: { request in model.takeDictationRequest(request, uploadDest: uploadDest) },
                           send: {
                               let typed = text
                               let t = typed.trimmingCharacters(in: .whitespacesAndNewlines)
                               let attached = images.count + draft.held.ready.count + videos.items.count + quotes.count
                               if offersClear, MainClearCommand.isCommand(t, attachments: attached) {
                                   text = ""
                                   return
                               }
                               if let gate { // the bar clears the box right after this: put the draft back
                                   gate.action()
                                   Task { text = typed }
                                   return
                               }
                               if draft.held.busy { // nothing goes; the bar clears the box right after this, so it is put back
                                   model.toast = Toast(draft.held.waitNote)
                                   Task { text = typed }
                                   return
                               }
                               send(draft.message(t, files: draft.held.ready), images.map(\.1))
                               draft.sent()
                           })
        }
        // Again once the hub is paired: on a fresh launch the first try runs before there is one.
        .task(id: "\(machine.path)|\(model.assistantLauncherRequestRevision)") {
            let api = model.api(machine.group)
            let scoped = defaultManager ? api?.manager(nil) : api
            if let answer = try? await model.assistantLaunchers(machine, using: scoped), !Task.isCancelled {
                launchers = answer.launchers.filter(\.runsAssistant)
            }
        }
        .mediaPicker(isPresented: $showPhotos, selection: $picks, limit: limits.perMessage + VideoAttachments.most)
        .fileUploadPicker(isPresented: $showFiles) { urls in
            // The assistant runs on its machine (the hub's unless set otherwise): files go to that one's temp directory.
            upload(urls)
        }
        .onChange(of: picks) { _, items in
            Task {
                guard !items.isEmpty else { return }
                let movies = items.filter(PickedMovie.isVideo) // sent as files (VideoAttach.swift)
                if !movies.isEmpty { videos.start(movies, machine: machine, model: model) }
                for item in items where !PickedMovie.isVideo(item) {
                    guard let data = try? await item.loadTransferable(type: Data.self), let img = UIImage(data: data) else {
                        AppModel.shared.toast = Toast(String(localized: "Couldn't read some photos (they may still be downloading from iCloud)"), style: .error)
                        continue
                    }
                    add(img) // held to the hub's pictures like every other way in (#12973)
                }
                picks = []
            }
        }
    }
}

/// Tool activity in the assistant: one quiet line ("Tools 4 · Run 3 · Read 1"), expandable to the steps.
struct AssistantToolsRow: View {
    var steps: [ToolStep]
    @State private var open = false
    @State private var unfold = Unfolding()
    @Environment(\.chatPin) private var pin
    /// The line stays where it is in the chat while its steps open under it, folded ones fetched a moment later (#3757).
    private var pinKey: String { "tools-" + (steps.first?.id ?? "") }

    private func toggle() {
        toggleInPlace(pin, pinKey, animation: Theme.Motion.standard, unfold: $unfold, closing: open) { open.toggle() }
    }

    private var calls: [ToolCall] { steps.compactMap { if case .call(let c) = $0 { c } else { nil } } }

    var body: some View {
        let calls = calls
        let count = steps.reduce(0) { $0 + $1.tools } // a fold counts the calls it stands for
        let running = calls.last.map(\.isRunning) ?? false
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if AppStyle.current.usesBeautifulUI {
                // BeautifulUI's chip run, as in an agent's chat: one chip per kind of tool (a spinner while one runs).
                Button(action: toggle) {
                    ToolChipGroup(ToolRunSummary.chips(calls), isOpen: open, isRunning: running)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(count) tool calls: \(sources(calls))")
            } else {
                Button(action: toggle) {
                    HStack(spacing: Theme.Space.sm) {
                        if running {
                            ProgressView().controlSize(.mini)
                            if let last = calls.last {
                                Text("\(ToolPresentation.verb(last)) \(ToolPresentation.subject(last))").lineLimit(1)
                            }
                        } else {
                            ThemedIcon(Theme.Icon.unknownTool)
                            Text("Tools \(count)")
                            Text(sources(calls)).foregroundStyle(.tertiary).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).rotationEffect(.degrees(open ? 90 : 0))
                    }
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .background(Theme.Fill.well, in: .capsule)
                }
                .buttonStyle(.plain)
            }
            if open {
                ForEach(steps) { step in
                    switch step {
                    case .call(let c): ToolCallRow(call: c, autoExpand: false)
                    case .folded(let f): FoldedWorkRow(fold: f)
                    case .thought: EmptyView()
                    }
                }
                .unfolding(unfold)
            }
        }
        .chatPinned(pinKey)
    }

    /// Which kinds of data were touched, in plain words.
    private func sources(_ calls: [ToolCall]) -> String {
        var seen: [String] = []
        for c in calls {
            let n = ToolPresentation.baseName(c.name)
            let label: String
            if n.hasPrefix("phone_") { label = "iPhone" }
            else if n.hasPrefix("mail_") { label = String(localized: "Mail [Assistant View 3d708e]") }
            else if n == "Bash" { label = (c.input["command"]?.string ?? "").contains("ssh ") ? String(localized: "Computers") : String(localized: "Commands [Assistant View 928f87]") }
            else if n == "Skill" { label = c.input["skill"]?.string ?? String(localized: "Skill") }
            else if n.hasPrefix("Web") { label = String(localized: "Web Pages") }
            else { label = ToolPresentation.verb(c.name) }
            if !seen.contains(label) { seen.append(label) }
        }
        return seen.prefix(4).joined(separator: String(localized: "、", comment: "Separator between list items"))
    }
}

/// Model chip on the assistant home page: which launcher the next new conversation starts with. While the Persona
/// settings are not read it shows Reading, and when they cannot be read Couldn't Load Settings with a retry — never a name (#4200).
struct StarterModelChip: View {
    @Environment(AppModel.self) private var model
    @Binding var launcher: String?
    @State private var launchers: [Launcher] = []

    private var current: String? { launcher ?? model.starterLauncherSetting }

    var body: some View {
        if let current {
            Menu {
                ForEach(launchers) { l in
                    Button {
                        launcher = l.name
                        Haptics.tap()
                    } label: {
                        if l.name == current { Label(l.name, systemImage: Theme.Icon.done) } else { Text("\(l.name)  \(SessionModes.gptModelName(l.model) ?? Fmt.model(l.model) ?? "")") }
                    }
                }
            } label: {
                ModeChip(symbol: Theme.Icon.model, text: current, tint: .secondary) // neutral, like the mode and effort chips beside it
            }
            .labelOnlyMenu()
            .task(id: "\(model.assistantConfig?.machineID ?? model.hubMachineID)|\(model.assistantLauncherRequestRevision)") {
                if let answer = try? await model.assistantLaunchers(model.hubRef(model.assistantConfig?.machineID ?? model.hubMachineID)), !Task.isCancelled {
                    launchers = answer.launchers.filter(\.runsAssistant)
                }
            }
        } else {
            StarterLauncherStatus()
        }
    }
}

/// The new-conversation page's model capsule while Persona settings are not known yet (#4200): a spinner with
/// Reading, or Couldn't Load Settings as a button that retries. It never shows a launcher name, and uses the `ModeChip` metrics.
struct StarterLauncherStatus: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.assistantConfigLoadFailed {
            Button {
                Haptics.tap()
                model.retryAssistantLoad()
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.error)
                    Text(String(localized: "Couldn't Load Settings")).lineLimit(1).minimumScaleFactor(0.8)
                    Text(String(localized: "Retry [Components b8784c]")).lineLimit(1).foregroundStyle(Theme.accent)
                }
                .statusCapsule()
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Couldn't Load Settings"))
            .accessibilityHint(Text("Retry [Components b8784c]"))
        } else {
            HStack(spacing: Theme.Space.xs) {
                ProgressView().controlSize(.mini)
                Text(String(localized: "Reading")).lineLimit(1)
            }
            .statusCapsule()
            .accessibilityLabel(Text("Reading"))
        }
    }
}

private extension View {
    /// The same capsule metrics and ink as a neutral `ModeChip`, for the starter's loading / error states.
    func statusCapsule() -> some View {
        font(Theme.Font.labelMedium)
            .foregroundStyle(Color.primary)
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.sm)
            .background(Color.primary.opacity(0.06), in: .capsule)
            .contentShape(.capsule)
    }
}

/// The launcher a new conversation starts with per Persona settings; nil when settings are not known or the hub
/// names none (#4200).
private extension AppModel {
    var starterLauncherSetting: String? {
        guard let name = assistantConfig?.launcher, !name.isEmpty else { return nil }
        return name
    }

    /// Health's: the default manager's, whose space it is.
    var healthLauncherSetting: String? {
        guard let name = defaultAssistant?.launcher, !name.isEmpty else { return nil }
        return name
    }

    /// The launcher a new conversation starts with when none is picked: Health's on its page (#3825), else Persona's.
    func launcherSetting(_ modes: StarterComposer.ShellModes) -> String? {
        modes.health ? healthLauncherSetting : starterLauncherSetting
    }
}

/// The shell composer's model menu for a conversation not started yet (#1091): the launcher and, under it, the model it
/// starts with (#11472, the running chat's model sheet and the web's model panel: launchers first, each one's models a
/// level down), the effort and, a level down, the permission mode ("" keeps the launcher's own). With nothing picked the
/// conversation follows Main chat's default and the start names neither launcher nor model.
struct StarterModesMenu: View {
    @Environment(AppModel.self) private var model
    var modes: StarterComposer.ShellModes
    var starter: AssistantStarterModels
    var efforts: [SessionModes.Option]
    /// Reads the launchers' model lists that failed again.
    var retryLists: () -> Void = {}

    /// The button's title: the model the new conversation starts on (Main chat's default with nothing picked, #11472;
    /// the launcher's name when it names none) and the effort picked. While the launcher is not known (#4200) the title
    /// is the status text, not nil — the shell bar draws the button so the menu's Reading / Couldn't Load Settings section
    /// is reachable on the iPhone path.
    @MainActor static func title(modes: StarterComposer.ShellModes, launchers: [Launcher], mainDefault: LauncherDefault? = nil,
                                 model: AppModel) -> SessionModes.ComposerTitle? {
        let effort = SessionModes.effort(modes.effort)?.title
        guard let current = effective(modes: modes, mainDefault: mainDefault, model: model) else {
            return SessionModes.ComposerTitle(model: model.assistantConfigLoadFailed ? String(localized: "Couldn't Load Settings") : String(localized: "Reading"), effort: effort)
        }
        let response = APIClient.LaunchersResponse(launchers: launchers, default: mainDefault, unavailable: nil)
        return SessionModes.composerTitle(model: AssistantModelChoice.displayName(current, response: response), effort: effort)
    }

    /// What the new conversation starts on (`AssistantStarterChoice.effective`); nil while Persona settings are not read.
    @MainActor static func effective(modes: StarterComposer.ShellModes, mainDefault: LauncherDefault?,
                                     model: AppModel) -> AssistantModelChoice.Effective? {
        guard let fallback = modes.launcher ?? model.launcherSetting(modes) else { return nil }
        return AssistantStarterChoice.effective(launcher: modes.launcher, model: modes.launcherModel, fallback: fallback,
                                                mainDefault: mainDefault)
    }

    private var current: AssistantModelChoice.Effective? {
        Self.effective(modes: modes, mainDefault: starter.mainDefault, model: model)
    }

    var body: some View {
        if modes.mode == "bypassPermissions" { // said first, as in a running conversation's menu (SessionModesMenu)
            Section { BypassWarning() }
        }
        if let current {
            if !starter.launchers.isEmpty {
                Section("Model [Assistant View c98e11]") { // the launchers, each one's models a level down (#11472)
                    if current.picked {
                        Button {
                            Haptics.tap()
                            choose(launcher: nil, model: nil)
                        } label: {
                            Label(String(localized: "Follow Main Chat default"), systemImage: Theme.Icon.clearContext)
                        }
                        .accessibilityIdentifier("starterModel.followDefault")
                    }
                    ForEach(starter.launchers) { l in
                        launcherMenu(l, current: current)
                    }
                }
            }
        } else {
            // Settings are not read yet: the menu says so (and offers a retry) instead of a launcher name (#4200).
            Section("Model [Assistant View c98e11]") {
                if model.assistantConfigLoadFailed {
                    Button {} label: { Label(String(localized: "Couldn't Load Settings"), systemImage: Theme.Icon.error) }.disabled(true)
                    Button {
                        Haptics.tap()
                        model.retryAssistantLoad()
                    } label: {
                        Label(String(localized: "Retry [Components b8784c]"), systemImage: Theme.Icon.retry)
                    }
                } else {
                    Button {} label: {
                        HStack {
                            Text(String(localized: "Reading"))
                            Spacer()
                            ProgressView()
                        }
                    }
                    .disabled(true)
                }
            }
        }
        Section("Thinking Effort") { // switches, so the title is drawn (#4322, `MenuChoice`)
            MenuChoice(id: "", selected: modes.effort, pick: { modes.effort = $0 }) { Text("Use Launcher's") }
            ForEach(efforts) { o in
                MenuChoice(id: o.id, selected: modes.effort, pick: { modes.effort = $0 }) {
                    Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }
                }
            }
        }
        Picker(selection: modes.$mode) {
            Text("Use Launcher's").tag("")
            ForEach(SessionModes.assistantModes) { o in Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id) }
        } label: {
            Label("Permission Mode: \(SessionModes.mode(modes.mode)?.title ?? String(localized: "Use Launcher's"))", systemImage: SessionModes.mode(modes.mode)?.symbol ?? Theme.Icon.askFirst)
        }
        .pickerStyle(.menu)
    }

    /// One launcher: its name and, for the one the conversation starts on, its model; a level down its models as the
    /// running chat's model sheet lists them (`AssistantModelSheet.section`): the catalog's rows with its Default entry,
    /// else the configured model, a lock's reason, Reading models… or Retry.
    private func launcherMenu(_ l: Launcher, current: AssistantModelChoice.Effective) -> some View {
        let list = starter.lists[l.name]
        let models = starter.visibleModels[l.name] ?? []
        let configured = list == nil ? nil : AssistantModelChoice.configuredFallback(l, models: models, locked: list?.locked)
        let aliases = starter.aliases[l.name] ?? [:]
        let selected = AssistantModelChoice.selectedModel(current, launcher: l.name, aliases: aliases, configuredModel: configured?.id)
        let pick = { (row: String) in
            guard let p = AssistantStarterChoice.pick(launcher: l.name, model: row, current: current, aliases: aliases,
                                                      configuredModel: configured?.id,
                                                      configuredIsDefault: configured?.sendsDefault == true) else { return }
            choose(launcher: p.launcher, model: p.model)
        }
        let title = l.displayName.flatMap { $0.isEmpty ? nil : $0 } ?? l.name
        return Menu {
            Group {
                if let configured {
                    // No catalog (a direct API, an older node): its configured model is still a concrete choice; a locked
                    // launcher sends its Default (no model), never an overriding model ID.
                    MenuChoice(id: configured.sendsDefault ? "" : configured.id, selected: selected, pick: pick) {
                        Text(SessionModes.modelName(configured.id) ?? configured.id)
                    }
                } else {
                    if !models.contains(where: \.isDefault) {
                        // The launcher's own default, when its catalog has no Default row (or is not read yet).
                        MenuChoice(id: "", selected: selected, pick: pick) {
                            Text(String(localized: "Default [Prompt Composer 844b8c]"))
                            if let own = l.model.flatMap({ $0.isEmpty || $0 == "默认" ? nil : $0 }) { Text(own) } // i18n-ignore: legacy wire value, never displayed
                        }
                    }
                    ForEach(models) { m in
                        MenuChoice(id: m.isDefault ? "" : m.id, selected: selected, pick: pick) {
                            Text(m.displayName)
                            if let detail = m.detail { Text(detail) }
                        }
                        .accessibilityIdentifier("starterModel.\(l.name).\(m.isDefault ? "default" : m.id)")
                    }
                }
                if let locked = list?.locked {
                    Button {} label: { Label(locked, systemImage: Theme.Icon.locked) }.disabled(true)
                }
                if list == nil {
                    if starter.listFailures.contains(l.name) {
                        Button {
                            Haptics.tap()
                            retryLists()
                        } label: {
                            Label(String(localized: "Retry"), systemImage: Theme.Icon.retry)
                        }
                    } else {
                        Button {} label: { Text(String(localized: "Reading models…")) }.disabled(true)
                    }
                }
            }
        } label: {
            // A submenu's row draws no second line and tints an icon with the accent, unlike the switches' checks: the one
            // the conversation starts on says its model after its name instead.
            if current.launcher == l.name,
               let name = AssistantModelChoice.displayName(current, response: .init(launchers: starter.launchers, default: starter.mainDefault, unavailable: nil)),
               name != title {
                Text(verbatim: "\(title) · \(name)")
            } else {
                Text(title)
            }
        }
        .accessibilityIdentifier("starterModel.\(l.name)")
    }

    /// Keeps the pick for the new conversation (nil, nil: follow Main chat's default again). Another launcher clears the
    /// effort, as before (its levels differ); another model of the same one keeps it while its levels have it (`retained`).
    private func choose(launcher: String?, model picked: String?) {
        let next = launcher ?? starter.mainDefault?.launcher ?? model.launcherSetting(modes)
        if next != current?.launcher { modes.effort = "" }
        modes.launcher = launcher
        modes.launcherModel = picked
    }
}

/// Hides the stack's navigation bar again once the page is back at its root. Swiped back from a pushed page with its
/// search still open (Knowledge), UIKit left the bar showing over the root, with the root's title and ~106 pt tall: the top
/// bar was pushed down under it, and a tap where the switch belongs hit the empty bar (2026-11-01). SwiftUI still has
/// the bar hidden, so it never hides it again.
///
/// Only while the conversation is the root (#8359): in the Sidebar layout another root (a place, an Agent's chat) takes
/// its place in the same stack, and a hide still waiting from the conversation's last update hid that root's bar, its
/// title and, on the open Duo upright, the button that brings the sidebar back.
private struct RootBarHidden: UIViewRepresentable {
    var atRoot: Bool
    /// The Sidebar layout's roots (#8596): a hide runs only while the conversation is still the root in front, read when
    /// it runs. The view taken away (`dismantleUIView`) was not enough: SwiftUI kept it in the window a moment after
    /// Recordings had replaced it, and a hide waiting 0.4 s hid Recordings' bar (the open Duo, one run in two).
    var roots: UnifiedSidebar?

    /// The conversation is still on screen: cleared when it is taken away (`dismantleUIView`).
    final class Coordinator {
        var live = true
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.isUserInteractionEnabled = false
        return v
    }

    static func dismantleUIView(_ v: UIView, coordinator: Coordinator) {
        coordinator.live = false
    }

    func updateUIView(_ v: UIView, context: Context) {
        guard atRoot else { return }
        let coordinator = context.coordinator
        let roots = roots
        // Now, and once the pop and the search's own dismissal have finished.
        for delay in [0.0, 0.4, 0.9] {
            DispatchQueue.main.asyncAfter(deadline: .now() + delay) { [weak v, weak roots] in
                guard coordinator.live, v?.window != nil else { return }
                if let roots, roots.root.value != nil { return } // another root is in front now
                var r: UIResponder? = v
                while let next = r?.next, !(next is UIViewController) { r = next }
                guard let nav = (r?.next as? UIViewController)?.navigationController,
                      nav.viewControllers.count == 1, !nav.isNavigationBarHidden else { return }
                nav.setNavigationBarHidden(true, animated: false)
            }
        }
    }
}

/// The classic layout has no ShellDestinations, so nothing registers the todo detail route in this stack: a todo row
/// pushed from the todo list (TodosView) would open nothing. Nor the agent page Full Screen pushes (`SessionKey`). The
/// shell registers both itself (ShellHome.swift).
private struct ClassicDestinations: ViewModifier {
    @Binding var path: NavigationPath
    var enabled: Bool

    func body(content: Content) -> some View {
        if enabled {
            content
                .navigationDestination(for: TodoDetailRoute.self) { r in TodoDetailView(todoID: r.id, openDetail: { (route: TodoDetailRoute) in path.append(route) }) }
                .navigationDestination(for: SessionKey.self) { key in ChatView(key: key).id(key) }
        } else {
            content
        }
    }
}
