import SwiftUI
import PersonaWire

/// The listed row for a saved pick: exact IDs before the hub's folded aliases, without adding an "Other Model" row.
nonisolated func assistantModelOption(_ id: String?, in models: [AgentModelOption]) -> AgentModelOption? {
    guard let id else { return nil }
    return models.first { $0.id == id } ?? models.first { ($0.aliases ?? []).contains(id) }
}

/// The Persona tab's top bar (docs/apple/ios.md「Persona top bar and sidebar」), floating over the conversation with a fog under it
/// instead of a solid bar: Chat History (the sidebar) on the left, Persona's face and name in the middle, New Chat on the right.
/// The face opens a menu: Knowledge, Details (the profile), the copy items (the working directory, a block of facts to
/// paste to another agent, the session id), the launcher, the session info card, Interrupt. While the hub is out of reach
/// the name pill shows the spinner and Waiting for Network / Connecting… / Updating….
struct AssistantTopBar: View {
    @Environment(AppModel.self) private var model
    #if os(iOS)
    @Environment(\.replySpeechContext) private var replySpeechContext
    #endif
    /// In the shell (docs/apple/ios.md「New shell」) the middle is the Assistant | Agents switch; its Assistant opens this bar's menu.
    @Environment(\.shell) private var shell
    /// The system's bars down the side (iPhone Duo): the shell's bar is the side rail there.
    @Environment(\.shellRailEdge) private var railEdge
    /// The conversation on screen; nil on the new-conversation page.
    var key: SessionKey?
    /// The sidebar is a column beside the conversation (a wide display): the button shows or hides it.
    var sidebarIsColumn = false
    /// The column is out. On a phone's tabs (iPhone Duo open) its own button folds it, at its top, so this one is only
    /// there to bring it back (#2623).
    var sidebarShown = false
    /// The Sidebar layout (#6332): with its column folded away, the button carries how many Agents need the user (the
    /// sidebar is where they are); off elsewhere.
    var sidebarCounts = false
    /// The Sidebar layout on a phone (#6332): the face opens the drawer, the conversation's name is the middle
    /// (`drawerRow`); off elsewhere.
    var drawerTitle = false
    /// The sidebar beside the page has New Chat at its foot (the Sidebar layout's column on the iPad and the open iPhone
    /// Duo, the iPad's Persona column): its one entry (#4206, the owner), so the page has none, the column out or away.
    /// A phone keeps this one.
    var newChatInSidebar = false
    var openSidebar: () -> Void
    var newConversation: () -> Void
    var openKnowledge: () -> Void
    var openProfile: () -> Void
    @State private var showInfo = false
    @State private var showDefaultPermissions = false
    @State private var showBackgroundActivity = false
    @State private var showLink = false
    @State private var launchers: [Launcher] = []
    /// Each launcher's /model list, fetched when the bar appears (the menu's model submenus).
    @State private var modelOptions: [String: [AgentModelOption]] = [:]
    /// A copy's confirmation, in the name pill for a moment (a toast would sit right on the face).
    @State private var hint: String?
    /// The launcher whose "Other Model…" is open (#1644): a model ID typed by hand for the next message.
    @State private var typingFor: String?

    /// The bar's height below the status bar: what the conversation leaves free at its top so nothing hides under it.
    static let height: CGFloat = 70
    /// How far below the bar the fog fades out.
    static let fade: CGFloat = 44
    /// How far below the safe area's top the fog stays solid: just past the round buttons and the switch, so a line of
    /// text is hidden under the controls and starts fading right below them.
    static let fogSolid: CGFloat = height - Theme.Space.l
    /// What the page leaves free at its top for the bar: little while it is the side rail (iPhone Duo, `ShellSideRail`),
    /// where the column beside the content holds it.
    static func room(_ railEdge: HorizontalEdge?) -> CGFloat {
        #if os(iOS) || os(visionOS)
        return railEdge == nil ? height : Theme.Space.s
        #else
        return height
        #endif
    }
    /// How far below the status bar the round buttons start. The sidebar's header beside the bar in the split starts
    /// there too, with the same `circle` (AssistantSidebar `besideBar`), so the two rows line up.
    static let topInset = Theme.Space.xxs

    private var session: Session? { key.flatMap { model.session($0) } }
    private var machineID: String { model.assistantMachineID(key) }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AssistantTopBar")
        #endif
        bar
            #if os(iOS)
            .onChange(of: showInfo || showDefaultPermissions || showBackgroundActivity || showLink || typingFor != nil,
                      initial: true) { _, open in replySpeechContext?.titleSheetOpen = open }
            #endif
            // Session Info is a sheet, like the Agents chat's (SessionInfoSheet).
            .sheet(isPresented: Binding(get: { showInfo && key != nil }, set: { showInfo = $0 })) {
                if let key {
                    SessionInfoSheet(key: key, session: session, assistant: true)
                        .presentationDetents([.medium, .large])
                        .presentationDragIndicator(.visible)
                }
            }
            .onChange(of: key) { showInfo = false; showDefaultPermissions = false; showBackgroundActivity = false }
            // A direction's permission sheet lives on the bar, not on a menu row that disappears as the menu closes.
            .sheet(isPresented: $showDefaultPermissions) {
                NavigationStack {
                    DefaultPermissionsView()
                        .pageToolbar { SheetClose { showDefaultPermissions = false } }
                }
            }
            .sheet(isPresented: Binding(get: { showBackgroundActivity && key == model.assistantMain },
                                        set: { showBackgroundActivity = $0 })) {
                if let key { BackgroundActivitySheet(key: key) }
            }
            // "Other Model…" (#1644): an ID typed by hand for the next message; the IDs typed before can be forgotten
            // there, so the submenus are read again when it closes.
            .sheet(isPresented: Binding(get: { typingFor != nil && key != nil }, set: { if !$0 { typingFor = nil } }),
                   onDismiss: { Task { await loadModelOptions() } }) {
                if let l = typingFor {
                    TypedModelSheet(machine: model.hubRef(machineID), launcher: l, purpose: "assistant", initial: typedPick(l),
                                    note: String(localized: "This chat uses it from the next message.")) { id in pick(l, model: id) }
                }
            }
            #if DEBUG
            .task { // `-showInfo YES`: Session Info open, for screenshots (the Agents chat reads the same flag)
                if UserDefaults.standard.bool(forKey: "showInfo") { try? await Task.sleep(for: .seconds(2)); showInfo = true }
            }
            #endif
        .task(id: "\(model.hubRef(machineID).path)|\(model.assistantLauncherRequestRevision)") {
            if let answer = try? await model.assistantLaunchers(model.hubRef(machineID)), !Task.isCancelled {
                launchers = answer.launchers.filter(\.runsAssistant)
            }
            await loadModelOptions()
        }
    }

    @ViewBuilder private var bar: some View {
        #if os(iOS)
        if shell != nil { shellBar } else if drawerTitle { drawerRow } else { classicBar }
        #else
        if shell != nil { shellBar } else { classicBar }
        #endif
    }

    #if os(iOS)
    /// The Sidebar layout's bar on a phone (#6332), the shell's without the switch: the persona's face opens the drawer
    /// (the inbox's mark on it, how many Agents need the user at its corner), the conversation's name in the middle with
    /// the face's menu (Knowledge, details, copy…), New Chat.
    private var drawerRow: some View {
        let link = model.connection.shown
        return HStack(alignment: .top) {
            ShellPersonaButton(id: "assistant-sidebar-button", action: openSidebar)
                .overlay(alignment: .topTrailing) { SidebarWaitingBadge() }
            Spacer(minLength: Theme.Space.s)
            Menu { menuItems(link) } label: {
                Self.pill {
                    if link != .online {
                        ConnectionLabel(phase: link, compact: true)
                    } else {
                        Text(name).font(Theme.Font.bodyStrong).lineLimit(1)
                        ThemedIcon(Theme.Icon.expand).font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
            }
            .menuStyle(.button)
            .buttonStyle(.plain)
            .accessibilityLabel(link != .online ? String(localized: "\(name), \(ConnectionLabel.text(link, hubRestarting: model.hubUpdate?.restarting == true))") : name)
            .accessibilityHint("Knowledge, details, copy directory and chat info")
            .accessibilityIdentifier("persona-avatar")
            .connectionDetails($showLink)
            Spacer(minLength: Theme.Space.s)
            ShellCircleButton(symbol: Theme.Icon.compose, label: String(localized: "New Chat [Quick Actions ea4b29]"), id: "assistant-new", action: newConversation)
                .disabled(key == nil)
                .opacity(key == nil ? 0.4 : 1)
        }
        .shellTopBarScaled()
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.xxs)
        .frame(height: Self.height, alignment: .top)
        .barScreenshotBanner(name: ownName)
        .background(alignment: .top) { BarFog(solid: Self.fogSolid, fade: Self.fade) }
    }
    #endif

    /// ChatGPT's top bar: the persona's face (the drawer; a mark while the inbox has something for the user), the
    /// switch, New Chat.
    @ViewBuilder private var shellBar: some View {
        #if os(iOS) || os(visionOS)
        if let railEdge { shellRail(railEdge) } else { shellRow }
        #else
        shellRow
        #endif
    }

    #if os(iOS) || os(visionOS)
    /// The shell's bar as the side rail (iPhone Duo): the face and New Chat, then the switch at the foot of the column.
    private func shellRail(_ edge: HorizontalEdge) -> some View {
        @Bindable var model = model
        let link = model.connection.shown
        return ShellSideRail(edge: edge) {
            ShellPersonaButton(id: "assistant-sidebar-button", action: openSidebar)
            ShellCircleButton(symbol: Theme.Icon.compose, label: String(localized: "New Chat [Quick Actions ea4b29]"), id: "assistant-new", action: newConversation)
                .disabled(key == nil)
                .opacity(key == nil ? 0.4 : 1)
        } switcher: {
            ShellSwitch(tab: $model.selectedTab, axis: .vertical) { menuItems(link) }
        }
        .shellTopBarScaled()
        .connectionDetails($showLink)
        .modifier(ShellBarAtRest(page: 0)) // ShellMovingBar stands in while the strip slides
    }
    #endif

    private var shellRow: some View {
        @Bindable var model = model
        let link = model.connection.shown
        return HStack(alignment: .top) {
            ShellPersonaButton(id: "assistant-sidebar-button", action: openSidebar)
            Spacer(minLength: Theme.Space.s)
            ShellSwitch(tab: $model.selectedTab) { menuItems(link) }
            Spacer(minLength: Theme.Space.s)
            ShellCircleButton(symbol: Theme.Icon.compose, label: String(localized: "New Chat [Quick Actions ea4b29]"), id: "assistant-new", action: newConversation)
                .disabled(key == nil)
                .opacity(key == nil ? 0.4 : 1)
        }
        .shellTopBarScaled()
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.xxs)
        .frame(height: Self.height, alignment: .top)
        .barScreenshotBanner(name: ownName)
        .background(alignment: .top) { BarFog(solid: Self.fogSolid, fade: Self.fade) }
        .connectionDetails($showLink)
        .modifier(ShellBarAtRest(page: 0)) // ShellMovingBar stands in while the strip slides
    }

    private var classicBar: some View {
        // Beside the one sidebar's column (#4206) the column's own button folds it, at its top: this one only brings it
        // back, and is not there while the column is out. Left out, not faded: a glass button made see-through (or slid
        // away) while the split moved its columns left the page taking no touches at all on iPhone Duo open
        // (2026-11-10).
        let sidebarButtonShown = !(sidebarCounts && sidebarIsColumn && sidebarShown)
        return HStack(alignment: .top) {
            if !sidebarButtonShown && newChatInSidebar {
                // Its room kept, empty, as New Chat's on the other side: the face stays in the middle.
                Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget).accessibilityHidden(true)
            }
            if sidebarButtonShown {
                GlassGroup {
                    Self.circle(sidebarIsColumn ? Theme.Icon.sidebar : Theme.Icon.sidebarMenu, label: String(localized: "Chat History [Shell Components 60636b]"), id: "assistant-sidebar-button") {
                        Haptics.tap()
                        openSidebar()
                    }
                }
                .overlay(alignment: .topTrailing) { if sidebarCounts && !sidebarShown { SidebarWaitingBadge() } }
                .transition(.identity)
            }
            Spacer(minLength: 8)
            if faceInColumn { namePill } else { face }
            Spacer(minLength: 8)
            if newChatInSidebar {
                // Its room kept, empty, so the face stays in the middle.
                Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget).accessibilityHidden(true)
            } else {
                GlassGroup {
                    Self.circle(Theme.Icon.compose, label: String(localized: "New Chat [Quick Actions ea4b29]"), id: "assistant-new") {
                        Haptics.tap()
                        newConversation()
                    }
                    .disabled(key == nil)
                }
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Self.topInset)
        .frame(height: Self.height, alignment: .top)
        .barScreenshotBanner(name: ownName)
        .background(alignment: .top) {
            // Solid down to the bar's bottom edge, fading out below it (fading under the pill let the messages show
            // through the name).
            BarFog(solid: Self.fogSolid, fade: Self.fade)
        }
    }

    /// The bar's round icon button; also the sidebar header's search button when the sidebar is the split's column.
    static func circle(_ symbol: String, label: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            ThemedIcon(symbol)
                .font(Theme.Font.heading)
                .dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .surface(interactive: true, in: .circle)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    /// The sidebar column is out beside the page and its head draws the persona's face (the assistant switcher): the
    /// bar's middle is then the name pill alone, so the screen shows the face once (#9973). With the column folded the
    /// face comes back. Only where the column stands beside the page (the iPad, the open iPhone Duo).
    private var faceInColumn: Bool {
        #if os(iOS)
        sidebarIsColumn && sidebarShown && model.showsAssistantSwitcher
        #else
        false
        #endif
    }

    /// The name pill without the face (`faceInColumn`), as the phone's Sidebar layout bar draws it (`drawerRow`), with
    /// the face's menu: the hub picker with several hubs, Knowledge, Details, the copies, the launcher, Interrupt. A
    /// copy's confirmation shows in it for a moment, as on the Mac's bar.
    private var namePill: some View {
        let link = model.connection.shown
        return HStack(spacing: Theme.Space.flush) {
            Menu { menuItems(link) } label: {
                Self.pill {
                    if let hint {
                        ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.positive)
                        Text(hint).font(Theme.Font.bodyStrong).lineLimit(1)
                    } else if link != .online {
                        ConnectionLabel(phase: link, compact: true)
                    } else {
                        Text(name).font(Theme.Font.bodyStrong).lineLimit(1)
                        ThemedIcon(Theme.Icon.expand).font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                }
            }
            .menuStyle(.button)
            .buttonStyle(.plain)
            .accessibilityLabel(link != .online ? String(localized: "\(name), \(ConnectionLabel.text(link, hubRestarting: model.hubUpdate?.restarting == true))") : name)
            .accessibilityHint("Knowledge, details, copy directory and chat info")
            .accessibilityIdentifier("persona-avatar")
            .connectionDetails($showLink)
        }
        .animation(Theme.Motion.standard, value: hint)
        .task(id: hint) { await clearHint() }
    }

    /// The capsule of a name pill: one line, at most 240 pt, a finger's height.
    static func pill<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        HStack(spacing: Theme.Space.xs) { content() }
            .padding(.horizontal, Theme.Space.l)
            .scaledFrame(height: Theme.Size.tapTarget)
            .scaledFrame(maxWidth: Theme.Size.box240)
            .fixedSize()
            // Not the glass `surface`: inside a Menu's label it spreads over the whole label (the face's menu).
            .background(.regularMaterial, in: .capsule)
            .contentShape(.capsule)
    }

    /// The face over the name pill; both are the menu's button. A copy's confirmation covers the pill for a moment
    /// (beside the menu, not on it: anything attached to a Menu becomes part of its button and its label).
    private var face: some View {
        ZStack(alignment: .bottom) {
            menu
            if let hint { hintPill(hint) }
        }
        .animation(Theme.Motion.standard, value: hint)
        .task(id: hint) { await clearHint() }
    }

    /// A copy's confirmation, in a pill of its own.
    private func hintPill(_ hint: String) -> some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.successFilled).foregroundStyle(Theme.positive)
            Text(hint).lineLimit(1)
        }
        .font(Theme.Font.labelStrong)
        .padding(.horizontal, Theme.Space.m)
        .scaledFrame(height: Theme.Size.icon)
        .fixedSize()
        .background(.regularMaterial, in: .capsule)
        .overlay(Capsule().strokeBorder(Color.primary.opacity(0.08), lineWidth: 0.5))
        .allowsHitTesting(false)
        .transition(.opacity)
    }

    /// A multi-line closure in the modifier chain slows the release build's type-check (docs/lessons/ios.md): a method instead.
    private func clearHint() async {
        guard hint != nil else { return }
        try? await Task.sleep(for: .seconds(3))
        hint = nil
    }

    private var menu: some View {
        let link = model.connection.shown
        return Menu { menuItems(link) } label: {
            VStack(spacing: -7) {
                TopBarDot(key: key, machineID: machineID)
                    .shadow(color: Theme.persona.opacity(0.3), radius: 8, y: 3)
                ZStack {
                    if link != .online {
                        ConnectionLabel(phase: link, compact: true).transition(.opacity)
                    } else {
                        Text(name).font(Theme.Font.labelStrong).lineLimit(1).transition(.opacity)
                    }
                }
                .padding(.horizontal, Theme.Space.m)
                .scaledFrame(height: Theme.Size.icon)
                .scaledFrame(maxWidth: Theme.Size.box200)
                .fixedSize() // as wide as the name, at most 200
                // Not the glass `surface`: inside a Menu's label the glass spreads over the whole label (face included).
                .background(.regularMaterial, in: .capsule)
                .overlay(Capsule().strokeBorder(Color.primary.opacity(0.08), lineWidth: 0.5))
                .animation(Theme.Motion.standard, value: link)
                .opacity(hint == nil ? 1 : 0)
            }
            .contentShape(.rect)
        }
        .menuStyle(.button)
        .buttonStyle(.plain)
        .accessibilityLabel(link != .online ? String(localized: "\(name), \(ConnectionLabel.text(link, hubRestarting: model.hubUpdate?.restarting == true))") : name)
        .accessibilityHint("Knowledge, details, copy directory and chat info")
        .accessibilityIdentifier("persona-avatar")
        .connectionDetails($showLink)
    }

    /// The pill's name: the assistant's on the main conversation and the new-conversation page, a side conversation's
    /// title.
    private var name: String {
        let own = ownName
        guard let key, key != model.assistantMain else { return own }
        let t = session?.title ?? ""
        return t.isEmpty || t == "Claude Code" ? own : t
    }

    /// Whose page it is: the current assistant's name, as the sidebar's head says it (with one assistant too, #9973;
    /// the face stays the persona's, picked in Appearance); another assistant's conversation opened from elsewhere
    /// names its own.
    private var ownName: String { model.assistantTitleName(in: key) }

    /// The conversation's launcher (the next message uses it) or, on the new-conversation page, the default one.
    /// The model picked for the next message here, nil: the launcher's default.
    private var pickedModel: String? { key.flatMap { model.assistantModels[$0] } }

    private func modelID(_ m: AgentModelOption) -> String? { m.isDefault == true ? nil : m.id }

    /// One model of a launcher's submenu, checked when it is the next message's.
    private func modelButton(_ launcherName: String, _ m: AgentModelOption) -> some View {
        let listed = assistantModelOption(pickedModel, in: modelOptions[launcherName] ?? [])
        let chosen = launcherName == launcher && (pickedModel == nil ? modelID(m) == nil : listed?.id == m.id)
        return Button { pick(launcherName, model: m.isDefault == true ? nil : m.id) } label: {
            if chosen { Label(m.title, systemImage: Theme.Icon.done) } else { Text(m.title) }
        }
    }

    /// What the launcher menu is built from (see `.id(launcherMenuID)`): the pick, whether the hub keeps a typed ID, and
    /// every launcher's models.
    private var launcherMenuID: [String] {
        [launcher, pickedModel ?? "", model.hubFeatures.contains(ModelID.feature) ? ModelID.feature : ""]
            + launchers.flatMap { l in [l.name] + (modelOptions[l.name] ?? []).flatMap { [$0.id] + ($0.aliases ?? []) } }
    }

    /// The next message's model when it is one typed for `launcherName` that its list doesn't have: the field starts with it.
    private func typedPick(_ launcherName: String) -> String {
        guard launcherName == launcher, let p = pickedModel, assistantModelOption(p, in: modelOptions[launcherName] ?? []) == nil else { return "" }
        return p
    }

    /// The launcher's agent takes a model ID typed by hand (#1644): Claude Code or Codex.
    static func takesTypedModel(_ l: Launcher) -> Bool {
        guard l.baseURL != "(traex)" else { return false }
        return LauncherModels.takesTypedModel(agent: l.agent.flatMap { $0.isEmpty ? nil : $0 } ?? "claude")
    }

    /// The launcher's submenu ends with "Other Model…": its agent takes a typed ID and the hub keeps one for the next
    /// message (`typed_models`); an older hub dropped an unlisted pick without a word.
    static func offersTypedModel(_ l: Launcher, features: Set<String>) -> Bool {
        features.contains(ModelID.feature) && takesTypedModel(l)
    }

    private func pick(_ launcher: String, model picked: String?) {
        guard let key else { return }
        model.assistantLaunchers[key] = launcher
        model.assistantModels[key] = picked
        Haptics.tap()
        let what = picked == nil ? launcher : "\(launcher) · \(picked ?? "")"
        model.toast = Toast(String(localized: "Using \(what) from the next message"), style: .success)
    }

    /// What the conversation runs with no pick of its own: Main Chat the launcher in Settings › Assistant, another its own.
    private var defaultLauncher: String {
        if let key, key != model.assistantMain, let l = session?.launcher, !l.isEmpty { return l }
        return model.assistantConfig?.launcher ?? "claude"
    }

    private func pickDefault() {
        guard let key else { return }
        model.assistantLaunchers[key] = nil
        model.assistantModels[key] = nil
        Haptics.tap()
        model.toast = Toast(String(localized: "Using the default, \(defaultLauncher), from the next message"), style: .success)
    }

    /// Each launcher's /model list; a failure just leaves the submenu without entries.
    private func loadModelOptions() async {
        let machine = model.hubRef(machineID)
        guard let api = model.api(machine.group), !launchers.isEmpty, !Task.isCancelled else { return }
        await withTaskGroup(of: (String, [AgentModelOption]?).self) { group in
            for l in launchers {
                group.addTask { (l.name, try? await api.launcherModels(machine: machine.id, launcher: l.name, purpose: "assistant")) }
            }
            // An empty answer counts too (the last typed ID forgotten); a failed one keeps what was there.
            for await (name, options) in group {
                if let options, !Task.isCancelled { modelOptions[name] = options }
            }
        }
    }

    private var launcher: String { AssistantCopyText.launcher(key, in: model) }

    private var cwd: String { AssistantCopyText.cwd(key, in: model) }

    @ViewBuilder private func menuItems(_ link: ConnectionStatus.Phase) -> some View {
        // Only this conversation's things: the hub is picked in the sidebar's assistant switcher, with its assistants (#12594).
        Section(menuTitle) {
            // A leader conversation's first item: the page of everything under its direction (#11516).
            DirectionPageMenuItem(key: key, showsPermissions: $showDefaultPermissions)
            // The Agent SDK could not run here (#2455): it says so, and why.
            if session?.runtimeFallback?.isEmpty == false,
               let r = AssistantRuntime.status(runtime: session?.runtime, fallback: session?.runtimeFallback) {
                Button { showInfo = true } label: { Label(r, systemImage: Theme.Icon.error) }
                    .accessibilityIdentifier("assistant-runtime-fallback")
            }
            if link != .online {
                Button { showLink = true } label: { Label("Connection Details", systemImage: Theme.Icon.offline) }
            }
            Button(action: openKnowledge) { Label("Knowledge", systemImage: Theme.Icon.knowledge) }
            Button(action: openProfile) { Label("Details [Assistant Top Bar 1932da]", systemImage: Theme.Icon.person) }
            if key != nil, key == model.assistantMain {
                #if os(iOS)
                if UIDevice.current.userInterfaceIdiom == .phone { ReplySpeechMenuToggle() }
                #endif
                Button { showBackgroundActivity = true } label: {
                    Label("Background activity", systemImage: Theme.Icon.recent)
                }
                .accessibilityIdentifier("background-activity-menu")
            }
            ModuleViews(place: .chatMenu) // a Mac's live screen, whether or not the model is operating it (#1343)
            if let main = model.assistantMain, key != main {
                Button {
                    withAnimation(Theme.Motion.standard) { model.assistantCurrent = main }
                    Haptics.tap()
                } label: { Label("Back to Main Chat", systemImage: Theme.Icon.home) }
            }
        }
        Section {
            Button { copy(cwd, String(localized: "Working Directory Copied")) } label: { Label("Copy Directory", systemImage: Theme.Icon.folder) }
            Button { copy(infoText, String(localized: "Chat Info Copied")) } label: { Label("Copy Info", systemImage: Theme.Icon.copy) }
            if let key {
                Button { copy(key.sessionID, String(localized: "Session ID Copied")) } label: { Label(String(localized: "Copy Session ID [Assistant Sidebar f24fb3]"), systemImage: Theme.Icon.id) }
                // What the chat last went through, structure only (ChatTrace): to paste into a report.
                Button { copy(ChatTrace.shared.text(key), String(localized: "Diagnostics copied")) } label: { Label(String(localized: "Copy Diagnostics"), systemImage: Theme.Icon.diagnostics) }
            }
        }
        if model.canCompact(key) || model.canClearMain(key) || model.canStartNewMain(key) {
            Section { MainConversationMenu(key: key) } // Compact context, Clear context, New Main Chat… (#1762, #5100)
        }
        if let key {
            Section {
                Menu {
                    Section("From the Next Message, Use") {
                        // The conversation's own launcher (for Main Chat the one in Settings › Assistant): no pick of this chat's.
                        Button { pickDefault() } label: {
                            if model.assistantLaunchers[key] == nil, pickedModel == nil { Label("Default: \(defaultLauncher)", systemImage: Theme.Icon.done) }
                            else { Text("Default: \(defaultLauncher)") }
                        }
                        ForEach(launchers) { l in
                            Menu {
                                Button { pick(l.name, model: nil) } label: {
                                    if l.name == launcher, pickedModel == nil { Label("Default Model", systemImage: Theme.Icon.done) } else { Text("Default Model") }
                                }
                                ForEach((modelOptions[l.name] ?? []).filter { $0.custom != true }) { m in
                                    modelButton(l.name, m)
                                }
                                let typed = (modelOptions[l.name] ?? []).filter { $0.custom == true }
                                if !typed.isEmpty {
                                    Section("Typed Before") { ForEach(typed) { m in modelButton(l.name, m) } }
                                }
                                if Self.offersTypedModel(l, features: model.hubFeatures) {
                                    let picked = l.name == launcher ? pickedModel : nil
                                    let other = picked.flatMap { p in assistantModelOption(p, in: modelOptions[l.name] ?? []) == nil ? p : nil }
                                    Section {
                                        Button { typingFor = l.name } label: {
                                            // A typed pick that isn't listed (yet) is checked here, by its ID.
                                            if let other { Label { Text("Other Model…"); Text(other) } icon: { ThemedIcon(Theme.Icon.done) } }
                                            else { Label("Other Model…", systemImage: Theme.Icon.keyboard) }
                                        }
                                        .accessibilityIdentifier("assistant-model-other-\(l.name)")
                                    }
                                }
                            } label: {
                                let title = [l.name, Fmt.model(l.model)].compactMap { $0 }.joined(separator: " · ")
                                // No "Just Once": the pick stays with this chat, from the next message on (#7513).
                                if l.name == launcher { Label(title, systemImage: Theme.Icon.done) } else { Text(title) }
                            }
                            // A nested menu keeps the items it was first built with: the list read after the bar
                            // came up, an ID typed or forgotten since, or another pick builds it again.
                            .id([launcher, pickedModel ?? ""] + (modelOptions[l.name] ?? []).map(\.id))
                        }
                    }
                } label: { Label("Launcher: \(launcher)", systemImage: Theme.Icon.model) }
                // This one too keeps the items it was first built with (its launchers' submenus among them): built
                // again when a list comes in or changes, or a pick does.
                .id(launcherMenuID)
                Button { showInfo = true } label: {
                    Label("Session Info", systemImage: Theme.Icon.info)
                }
                if session?.status.isActive == true {
                    Button { Task { await model.interrupt(key) } } label: { Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt) }
                }
            }
        }
    }

    /// The menu's heading: which conversation, and what it runs on.
    private var menuTitle: String {
        let title = baseMenuTitle
        guard model.isMultiHub, let g = model.hub?.group else { return title }
        return title + " · " + model.groupName(g)
    }

    private var baseMenuTitle: String {
        guard let key else { return String(localized: "New chat · \(launcher)") }

        let title = name == ownName && key == model.assistantMain ? String(localized: "Main Chat") : name
        return [title, launcher, Fmt.model(pickedModel ?? session?.model)].compactMap { $0 }.joined(separator: " · ")
    }

    private var infoText: String { AssistantCopyText.info(key, in: model) }

    private func copy(_ text: String, _ done: String) {
        UIPasteboard.general.string = text
        Haptics.success()
        hint = done
        AccessibilityNotification.Announcement(done).post()
    }
}

/// What Copy Info puts on the clipboard: enough for another agent (or the user at a terminal) to find this conversation.
/// The face's menu copies it, and on iPhone Duo open the main conversation's row (#2623).
nonisolated enum AssistantCopyText {
    /// The conversation's launcher (the next message uses it) or, with none (the new-conversation page), the default one.
    @MainActor static func launcher(_ key: SessionKey?, in model: AppModel) -> String {
        // The main conversation follows Settings › Assistant (the hub sends it there), side ones keep their own.
        if let key, key == model.assistantMain, let l = model.assistantLaunchers[key] ?? model.assistantConfig?.launcher { return l }
        if let key { return model.assistantLaunchers[key] ?? model.session(key)?.launcher ?? model.assistantConfig?.launcher ?? "claude" }
        return model.assistantConfig?.launcher ?? "claude"
    }

    /// Where the conversation runs.
    @MainActor static func cwd(_ key: SessionKey?, in model: AppModel) -> String {
        if let c = key.flatMap({ model.session($0) })?.cwd, !c.isEmpty { return c }
        if let c = model.assistantConfig?.cwd, !c.isEmpty { return c }
        return "~/.persona/assistant"
    }

    /// Copy Info's text for the conversation.
    @MainActor static func info(_ key: SessionKey?, in model: AppModel) -> String {
        let session = key.flatMap { model.session($0) }
        let machineID = model.assistantMachineID(key)
        return info(title: session.map { $0.title.isEmpty || $0.title == "Claude Code" ? nil : $0.title } ?? nil,
                    isMain: key != nil && key == model.assistantMain, sessionID: key?.sessionID,
                    machine: model.machine(model.hubRef(machineID))?.name, machineID: machineID, cwd: cwd(key, in: model),
                    launcher: launcher(key, in: model), model: Fmt.model(session?.model))
    }

    static func info(title: String?, isMain: Bool, sessionID: String?, machine: String?, machineID: String, cwd: String,
                     launcher: String, model: String?) -> String {
        var lines: [String] = []
        let name = [isMain ? String(localized: "Main Chat") : nil, title.map { String(localized: "「\($0)」", comment: "A chat title in quotes") }]
            .compactMap { $0 }.joined(separator: " ")
        lines.append(sessionID == nil ? String(localized: "Persona assistant (Claude on the hub)")
                     : name.isEmpty ? String(localized: "Persona assistant chat") : String(localized: "Persona assistant chat \(name)"))
        if let sessionID { lines.append(String(localized: "Session ID: \(sessionID)")) }
        let place = machine.map { $0 == machineID ? $0 : String(localized: "\($0) (\(machineID))") } ?? machineID
        lines.append(String(localized: "Machine: \(place)"))
        lines.append(String(localized: "Working directory: \(cwd)"))
        lines.append(model.map { String(localized: "Launcher: \(launcher) · Model: \($0)") } ?? String(localized: "Launcher: \(launcher)"))
        if let sessionID { lines.append(String(localized: "To continue on that machine: cd \(cwd) && claude --resume \(sessionID)")) }
        return lines.joined(separator: "\n")
    }
}

/// The face in the bar, in its own view: what it shows reads the machines, the session and its last item, all of which
/// every hub push replaces; read in the bar itself, each push rebuilt the bar and its menu (an open menu with it).
private struct TopBarDot: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    let key: SessionKey?
    let machineID: String

    var body: some View {
        PersonaDot(size: Theme.Size.avatarL, state: model.personaState(key, machineID: machineID, inFront: inFront))
    }
}

extension AppModel {
    /// The machine Persona's conversation `key` runs on; on the new-conversation page (nil) the assistant's.
    func assistantMachineID(_ key: SessionKey?) -> String {
        key?.machineID ?? assistantConfig?.machineID ?? hubMachineID
    }

    /// What the persona's face shows for Persona's conversation `key` (docs/apple/persona.md「States」): Offline when the hub or
    /// Persona's machine is out of reach, Waiting for you when the conversation waits on the user, Thinking / Working while a turn
    /// runs (thinking until it writes or calls a tool), else Idle. Read only by the face's own view (`TopBarDot`,
    /// `ShellPersonaButton`): every hub push changes what it reads. `inFront`: the page's `tabIsActive`; behind another
    /// section (the Mac keeps the page, `MacPageStack`) the machine is read as it is now, not followed push by push.
    func personaState(_ key: SessionKey?, machineID: String, inFront: Bool = true) -> PersonaState {
        #if DEBUG
        // `-showPersonaState idle|thinking|working|needs_you|offline`: hold the face in one state (screenshots).
        if let s = UserDefaults.standard.string(forKey: "showPersonaState").flatMap(PersonaState.init(rawValue:)) { return s }
        #endif
        // Updating… is the hub answering again: not Offline.
        let link = connection.shown
        if link == .waitingForNetwork || link == .connecting || link == .rejected || machine(hubRef(machineID), inFront: inFront)?.online == false { return .offline }
        guard let key, let session = session(key) else { return .idle }
        if session.state == .needsYou || !interactions(for: key).isEmpty { return .needsYou }
        guard session.state == .working else { return .idle }
        let last = chats[key]?.items.last
        return last == nil || last?.role == "user" || last?.kind == .thinking ? .thinking : .working
    }
}

/// `TopFog` behind a floating bar, as its background: from the top of the screen (under the status bar) down to `solid`
/// below the safe area's top, then fading out over `fade`, and across the whole width. On iPhone Duo's outer screen
/// the status bar stands at the side: a fog only as wide as the page stopped beside it, a hard edge (#283).
private extension View {
    /// What a screenshot shows of the bar (#9508): the assistant's avatar and `name` (the bar's own for whose page it
    /// is, #12243), and Persona's repository at the right (#10172), in its row of buttons, down to where the fog starts to fade (the fog under both carries on
    /// below). Under the bar the page's color with the fog's own over it, so at rest the bar looks as it did; under the
    /// banner the page's color alone.
    func barScreenshotBanner(name: String) -> some View {
        screenshotBanner(solid: AssistantTopBar.fogSolid, fade: Theme.Space.m) {
            ZStack {
                Theme.pageBackground
                Rectangle().fill(.regularMaterial)
                Theme.pageBackground.opacity(0.82)
            }
        } bannerBackdrop: {
            Theme.pageBackground
        } banner: {
            ScreenshotBannerContent(row: (AssistantTopBar.topInset, Theme.Size.tapTarget), name: name)
        }
    }
}

struct BarFog: View {
    var solid: CGFloat
    var fade: CGFloat

    var body: some View {
        GeometryReader { g in
            let inset = g.safeAreaInsets
            let solidHeight = inset.top + solid
            TopFog(solidHeight: solidHeight, fadeHeight: fade)
                .frame(width: g.size.width + inset.leading + inset.trailing, height: solidHeight + fade)
                .offset(x: -inset.leading, y: -inset.top)
        }
    }
}

/// The fog under a floating bar: the page's own color, blurred, solid for `solidHeight` and then fading out over
/// `fadeHeight` (the content scrolls under it and shows through only below the bar). Both layers fade along an eased
/// curve, so the end of the solid part leaves no line across the text; the blur fades out over the first half only, so
/// a line half under the fog dims rather than shows a smeared copy of itself (#2909).
struct TopFog: View {
    var solidHeight: CGFloat
    var fadeHeight: CGFloat

    var body: some View {
        let total = max(solidHeight + fadeHeight, 1)
        let solid = solidHeight / total
        let fade = fadeHeight / total
        ZStack {
            Rectangle().fill(.regularMaterial)
                .mask(Self.easedMask(from: solid, over: fade / 2))
            Theme.pageBackground.opacity(0.82)
                .mask(Self.easedMask(from: solid, over: fade))
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    /// Opaque down to `start`, then out to clear over `length` (unit points) along the eased curve the chat's footer fog
    /// shares (`EasedFade`).
    private static func easedMask(from start: CGFloat, over length: CGFloat) -> LinearGradient {
        LinearGradient(stops: EasedFade.stops(.black, from: start, over: length), startPoint: .top, endPoint: .bottom)
    }
}
