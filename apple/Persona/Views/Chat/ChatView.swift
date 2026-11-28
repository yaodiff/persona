import BeautifulUI
import MarkdownUI
import OSLog
import PersonaKit
import PhotosUI
import SwiftUI
#if os(iOS)
import UIKit
#endif
#if DEBUG
import notify
#endif
import PersonaWire

enum ChatStyle {
    /// Develop: every step visible, tab bar hidden.
    case dev
    /// Chat (assistant): thoughts hidden, tool runs folded into one line, lives at the root of its tab.
    case assistant
}

struct ChatView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dynamicTypeSize) private var typeSize
    /// The session on screen; the sibling strip can switch it to another agent in the same space.
    @State private var key: SessionKey
    var style: ChatStyle = .dev
    @StoredValue(BotmuxDisplayMode.storageKey) private var botmuxMode: BotmuxDisplayMode = .decoded

    private var isBotmuxSession: Bool { session?.host == "botmux" || session?.hostKind == "botmux" }
    @State private var position = ScrollPosition(edge: .top) // .top is the newest end of the flipped list
    /// Where the chat stands with its newest end: ↓, following and holding back all come from it (#1046). Changed
    /// through `setFollow` only.
    @State private var follow = ChatFollow()
    /// Hide the arrow for the first moment on entry (it would flash while the list settles at the end).
    @State private var settled = false
    /// Bumped when a message's wait for the hub to say working runs out (`ChatAwaitingTurn.window`): drawn again then.
    @State private var awaitTick = 0
    /// The chat's height: the workflow section's cap is a share of it (#4532).
    @State private var pageHeight: CGFloat = 0
    /// The pin is bringing a header up to show what opened under it (`ChatPin.onRising`): no ↓ for that moment.
    @State private var pinRising = false
    @State private var showTerminal = false
    /// The terminal comes up to resume a stopped Agent at once (Resume in Terminal, #5521): it shows the resume's progress
    /// and connects as soon as the Agent is up.
    @State private var resumeOnOpen = false
    @State private var file: FileRef?
    /// A knowledge note's block from Add as Context (#3721), opened from the bubble.
    @State private var openedKnowledge: KBEntry?
    /// Unified Session Overview sheet (iPhone) / inspector (iPad) and full Subagent Detail navigation (#27, #14322).
    /// Held as one reference so `ChatView` stays small on the stack (`MainPagesStackTests`).
    @State private var overviewUI = SessionOverviewUI()
    /// Opens a folded child's full detail from an inline `ChildSessionCard` (P5, reference object).
    @State private var childOpener = ChildSessionOpener()
    /// Its side sessions' sheet and the side shown over it (#7944, `SideSessionsPresentation`): one reference, so the
    /// page's value stays as small as it was (`MainPagesStackTests`).
    @State private var sideUI: SideSessionsUI
    #if os(iOS)
    /// The review page of this session's changes (#151).
    @State private var reviewRequest: ReviewRequest?
    #endif
    /// A hosted header's goal mark opened (`GoalSheet`); the open header's `GoalBar` keeps its own.
    @State private var showGoal = false
    /// A permission-mode pick waiting for its confirmation (`SessionModes.confirmation`, #3719).
    @State private var modeConfirm: ModeConfirm?
    /// A mode change on its way to the node: the header's mode draws a spinner in place of its symbol.
    @State private var modeChanging = false
    @State private var debugImages: [ImagePayload]?
    @State private var showTodoDetail = false
    /// A task a hub link in this chat opened (`/todos?id=…`, #2696): its detail rises like a link.
    @State private var linkedTodo: TodoDetailRoute?
    /// A knowledge entry or computer a link in this chat opened (#9926); a to-do keeps `linkedTodo`.
    @State private var hubSheet: HubLinkSheet?
    @State private var showLink = false
    @State private var pane: SessionPane = .chat
    /// A file the Files pane shows in its folder (the preview's Show in Files Page, #5827).
    @State private var filesReveal: String?
    /// Kept when the Files pane swaps back to Chat, so an unfinished remote operation can be seen again.
    @State private var folderTransfers = FolderTransfers()
    private var git: GitStatus? {
        session.flatMap { AgentMenuRouting.shared.gitStatus($0) }
    }
    @State private var menu = MessageMenu()
    /// What Forward To picked (#3807): done once its sheet has gone, as a navigation made while it goes is dropped.
    @State private var forwardPicked: ForwardOutcome?
    /// A forward on its way to a new Agent: New Chat with it over the first message.
    @State private var forwardNewAgent: ForwardNewAgent?
    #if os(iOS)
    /// Select Messages (#3838): the messages picked to forward together, while selecting.
    @State private var pick = MessagePick()
    /// Export Image (#9468): the picked messages on their way into a picture (`ChatExportSheet`).
    @State private var exporting: ExportRequest?
    #endif
    #if os(iOS)
    /// Full Page in this chat's system screenshot (#1609).
    @State private var fullPage = ChatFullPage()
    #endif
    /// Work lines the user tapped open (by line id; kept here so lazy rows coming back keep their state).
    @State private var openWork: Set<String> = []
    @State private var openSupervision: Set<String> = []
    /// Runs of tool calls (`ChatLine.toolRun`) opened into a row per call.
    @State private var openRuns: Set<String> = []
    /// While you read history, the chat is drawn from the items it had when you left the newest end (inserting at the
    /// newest end of the flipped list would push what you read); history loaded at the far end, and holes filled
    /// behind it, still show. Cleared when you are back at the newest. It used to keep the lines up to the newest
    /// line's id, but those ids change as a turn goes on (the answer folds into the work line, a finished turn's work
    /// lines merge), and losing the id let everything in at once.
    @State private var frozen: Frozen?
    /// Bumped to build the chat's scroll view again (after a fold that left it drawing nothing).
    @State private var listBuild = 0

    struct Frozen: Equatable {
        var seq: Int
        var part: Int
        /// The turn that was running: its lines stay as they were drawn (live work line, answer not yet picked).
        var live: String?
        /// The streamed words and the unsent messages on screen then: they stay as they were. Taken away, they went from
        /// under the finger the moment it drew the list away from a streaming reply, the list jumped by their height,
        /// and came back on release (the Mac, 2026-11-04: 80 pt into a drag the list was at the newest again, 380 pt
        /// away as it glided, then back).
        var text = ""
        /// By `ChatLines.frozenKey`, which a resend keeps (its id changes).
        var pending: Set<String> = []
        /// Where the failed ones among them were drawn: the line each sat before (`ChatLines.placeFrozen`).
        var failedAt: [String: String] = [:]
    }
    /// The user dragged the list at least once: only then does reaching the top load older pages (a lazy stack may
    /// lay out the top row while settling at the bottom, which used to chain-load history on entry).
    @State private var userScrolled = false
    @State private var entered = false
    #if DEBUG
    /// How many times each conversation has appeared since launch (`probe.chat`'s `appeared`). Opened at launch it
    /// appeared three times within 0.2 s while the shell's navigation went back and forth (2026-11-02): once it is.
    static var appearances: [String: Int] = [:]
    @State private var appearCount = 0
    #endif
    /// Where to land on entry, once the lines are there: the start of the latest answer (finished, unread) or where
    /// you left off (finished, read), or the message a hub link named (`?at=`, #2696).
    @State private var entry: Entry?
    /// Lines on screen (for remembering where you were).
    /// Bookkeeping written while scrolling (visible rows, finger down) and the cached lines: kept out of SwiftUI's
    /// state so writing it never re-renders the page.
    @State private var scratch = ChatScratch()
    /// Keeps a tapped header in place while what it opens settles (`ChatPin`).
    @State private var pin = ChatPin()
    /// ↓'s jump to the newest end, run on the list itself (#3027).
    @State private var jumpRun = ChatJumpRun()
    /// Work lines just opened, until when their steps fade in as they appear (they arrive from the hub after the tap,
    /// `Unfolding`). Closing has no such phase: what closes goes at once (#3005).
    @State private var revealUntil: [String: Date] = [:]

    private enum Entry: Equatable {
        case latestAnswer, line(String), message(String)
        /// The message a task's card is about (#12625), found once the items are in.
        case card(Interaction)
        #if os(iOS)
        case starred(MessageJump)
        #endif
    }
    #if os(iOS)
    @State private var checkedStar: UUID?
    #endif
    /// The line being landed on, measured while landing (only that one row watches its frame).
    @State private var landing: String?

    private var readKey: String { "chat.read.\(shownKey.machineID)/\(shownKey.sessionID)" }
    @State private var renaming: Session?
    @State private var closing: Session?
    @State private var deleting: Session?
    /// The plan sheet (`PlanSheet`), and whether the newest plan's line is on screen (if not, its pill shows).
    @State private var planOpen = Self.debugPlanSheet
    @State private var planLineShown = false
    /// The page's dismiss action, read by `DismissReader` and kept unobserved: `@Environment(\.dismiss)` on this view
    /// changed each time the shell's page went behind or came back, and drew the whole chat again (#5438).
    @State private var dismissChat = DismissHolder()
    /// Hosted by a page about something else (a task's sheet, `HostedChat`): its header is the page's only one.
    @Environment(\.hostedChat) private var host
    /// In the detail column of a split (iPhone Duo open): the list is beside the chat, the tab bar stays.
    @Environment(\.splitDetail) private var split
    /// A task's detail goes on the stack's path, not pushed by view from here (#13156).
    @Environment(\.taskDetailByValue) private var taskDetailByValue
    @Environment(\.splitShowList) private var showList
    /// The Persona conversation in the shell (docs/apple/ios.md「New shell」).
    @Environment(\.shellChat) private var shellChat
    @Environment(\.shell) private var shell
    /// iPhone Duo's bars down the side: the shell's bar over the Persona conversation is the side rail (`ShellSideRail`).
    @Environment(\.shellRailEdge) private var railEdge
    /// Room under the assistant's top bar for what the page draws there (a space conversation's categories, #13500).
    @Environment(\.assistantTopExtra) private var topExtra
    /// iPhone Duo's side column under the tab bar: how much of it this chat's buttons have (`sideButtons`, #9837).
    @Environment(\.sideColumn) private var sideColumn
    /// The page draws this chat's lifted message (`MessageLiftHost`).
    @Environment(\.messageLiftHost) private var liftHost
    #if os(iOS)
    @Environment(\.replySpeechContext) private var replySpeechContext
    #endif
    /// The shell's composer (`ShellComposerBar`): the assistant's conversation and, in the shell, an agent's too, in every
    /// style (#1091). Other platforms retain `ComposerRow` / `PromptComposer` and their mode chips. On the Mac
    /// the card in every style but BeautifulUI's (its prompt bar is already one): the field, and under it Add, the model
    /// and its modes, send / stop, as Codex's and ChatGPT's desktop apps (#2113).
    private var usesShellComposer: Bool { shellChat || shell != nil || ChatComposerKind.card }
    /// The system presents bars down the side of the screen (iPhone Duo closed, or open in landscape).
    @State private var barsVertical = false
    /// The slim header's height while the actions are in the side column (`sideHeader`).
    @State private var sideHeaderHeight: CGFloat = 0
    /// The keyboard is up on the composer: the slim header gives its rows to the chat (`sideHeader`).
    @State private var composerTyping = false
    /// Safari-style chrome (title row, agent strip, tabs): it follows the finger as the list scrolls and settles
    /// open or folded when the finger lifts. Its own object so the per-frame updates only redraw the header.
    @State private var chrome = ChatChrome()
    /// The page's height, read by the pending cards only (#926).
    @State private var footerRoom = FooterRoom()
    /// Where each pending card's message is in the chat, for its View in Chat (#9392).
    @State private var cardSources = CardSources()
    /// The assistant's pager over the composer and its To Decide entry (#12831).
    @State private var dock = QuestionDockState()
    @Environment(\.openSpace) private var openSpace
    @State private var link: LinkTarget?
    /// ⋯ › Preview's list of the session's dev servers, and the one picked there, opened once the list is gone (#150).
    @State private var previewing = false
    @State private var previewPicked: PreviewOption?
    /// Where a Model Not Connected card's Fix It goes (`ReadinessSheets`).
    @State private var readinessRoute: ReadinessRoute?
    /// A Persona conversation's own model picker, shared by its composer (Main chat) and model-route remedy (#10365).
    @State private var showAssistantModel = false
    @State private var assistantPanel: AssistantModelPanelModel?
    /// The image viewer, shown by the page rather than the row it was opened from (`viewImages`).
    @State private var viewingImages: ImageViewing?
    @Namespace private var imageZoom
    /// Tool calls the user opened or closed (`openTools`).
    @State private var openTools = OpenTools()
    /// What the environment's actions call (`ChatActions`).
    @State private var actions = ChatActions()
    /// The line a quote just led back to, lit up for a moment (`QuoteFlash`).
    @State private var quoteFlash: QuoteFlashMark?
    /// The right-edge navigator while a finger is on it (new conversation layout, `ChatEdgeScrub`).
    @State private var scrub = EdgeScrubState()
    @State private var userInputIndex: (key: SessionKey, index: ChatUserInputIndex)?
    /// The permission mode changes the hub recorded for an Agent's chat (#10297, `loadModeChanges`), by session, and
    /// the chat and mode they were last read for.
    @State private var modeChanges: (key: SessionKey, changes: [APIClient.ModeChange])?
    @State private var modeChangesRead: String?
    @State private var navigationJump: EdgeMark?
    /// Offered once this chat's pending card was answered and another conversation needs you (#4348); nil until then.
    @State private var offeredNext: SessionKey?

    /// `sidePage`: the page is a side session's or subagent's own (`SideSessionPage`, `SubagentDetailPage`), which shows
    /// that session directly without redirecting to its parent. `initialSubagent`: an embedded `parent@agent` subagent to
    /// show immediately when hosted by `SubagentDetailPage`.
    init(key: SessionKey, style: ChatStyle = .dev, itemManager: String? = nil, sidePage: Bool = false,
         backgroundActivity: Bool = false, initialSubagent: Subagent? = nil) {
        // Built again for the same path (a tree swap): the agent its header switched to, not the one it opened with.
        #if os(iOS)
        var start = style == .dev && !sidePage ? AppModel.shared.chatSwitched[key] ?? key : key
        #else
        var start = key
        #endif
        var landing: SessionKey?
        if style == .dev, !sidePage, let parent = AppModel.shared.sideParent(of: start) {
            landing = start
            start = parent
        }
        _sideUI = State(initialValue: SideSessionsUI(shown: landing))
        _key = State(initialValue: start)
        _viewing = State(initialValue: initialSubagent)
        opened = key
        self.style = style
        self.itemManager = itemManager
        self.backgroundActivity = backgroundActivity
    }

    /// Read-only Background activity of the main conversation. It shares the transcript store and every line renderer,
    /// but never becomes the visible chat and never acknowledges anything as seen.
    private let backgroundActivity: Bool

    /// The session this page was opened with (the path's), which `switchTo` records its switches under.
    private let opened: SessionKey
    /// The manager of the inbox item this conversation was opened from (`InboxChatRoute`).
    private let itemManager: String?

    /// A subagent of this session being looked at instead of the main agent (read-only, same message UI).
    @State private var viewing: Subagent?
    @State private var takeoverCoordinator = CodexTakeoverCoordinator()
    /// This view's claim on its subscriptions and on `visibleSession` (another view of the same chat has its own).
    @State private var owner = UUID()

    /// What the chat shows: the main agent, or the subagent picked in the agent strip.
    private var shownKey: SessionKey { Self.shownKey(key, viewing: viewing) }

    /// The session whose items are on screen: the subagent's own transcript while one is open (its items, images and
    /// full texts are fetched from it, not from the main session).
    static func shownKey(_ key: SessionKey, viewing: Subagent?) -> SessionKey {
        viewing.map { SessionKey(group: key.group, machineID: key.machineID, sessionID: $0.sessionID) } ?? key
    }
    private var store: ChatStore { model.chat(shownKey) }
    #if DEBUG || PERF_SIGNPOST
    private static let printsChanges = UserDefaults.standard.bool(forKey: "showPerfChanges")
    #endif
    /// The latest time this chat's model operated a Mac (#1343): shown with its running work, kept for the live screen.
    private var computerActivity: ComputerActivity? {
        ComputerActivity.latest(in: store.items, chat: shownKey.machineRef, machines: model.namedMachines)
    }
    private var session: Session? { model.session(key) }
    /// This chat's side sessions (#7944, `AppModel.sides(of:)`): an Agents chat of its own only, never a hosted one (a
    /// task's sheet, a side's own page).
    private var sides: SideSessions { style == .dev && host == nil ? model.sides(of: key) : SideSessions() }
    /// The pane of a Codex opened with no first message (Session.awaitingFirstMessage): once its first message goes in,
    /// from here or its terminal, its row goes and the thread's comes in that pane, and the page follows it there.
    @State private var codexWaitingPane: String?
    /// The waiting row the pane was noted for: the page follows only while it still shows that row (not after moving to
    /// another session, whose page a thread in that pane must not take over).
    @State private var codexWaitingID: String?
    private var codexThread: SessionKey? {
        guard let pane = codexWaitingPane, codexWaitingID == key.sessionID else { return nil }
        return model.sessionsByMachine[key.machineRef]?.first {
            $0.paneID == pane && $0.sessionID != key.sessionID && $0.isCodex && $0.awaitingFirstMessage != true && $0.state.isLive
        }?.key
    }
    /// The manager the links tapped here read through: the session's, else (a conversation not listed yet) the inbox
    /// item's it was opened from or the one the Persona page shows it for; nil for the default's.
    private var linkManager: String? {
        model.linkManager(session?.manager ?? itemManager ?? (style == .assistant ? model.assistantManagerID : nil), group: key.group)
    }
    /// The manager this conversation's feed is read from and its cards answer to (`AppModel.feedManager`): its own,
    /// whichever assistant the page shows. nil for the default's and with one manager: that client names none (and
    /// drops the page's current selection, `APIClient.manager`).
    private var feedManager: String? { model.feedManager(listed: session, opened: itemManager, group: key.group) }
    nonisolated private struct FeedScope: Hashable {
        var key: SessionKey
        var manager: String?
    }
    /// The latest state of the subagent being viewed (the list refreshes with the session).
    private var viewedAgent: Subagent? {
        viewing.flatMap { v in (session?.subagents?.first { $0.id == v.id } ?? v).shown(in: model.chat(key).tasks, sessionWorks: sessionWorks) }
    }
    /// The session's subagents, each running or not as its own chat's lines tell it (`Subagent.running(in:)`, #5602):
    /// the session's, also while a subagent's is shown.
    private var subagents: [Subagent] {
        let tasks = model.chat(key).tasks, works = sessionWorks
        return (session?.subagents ?? []).map { $0.shown(in: tasks, sessionWorks: works) }
    }
    /// At work, or waiting on the user (a subagent's question stops its session there).
    /// The session's subagents by their call, for their cards (`SubagentCard`).
    private var subagentsByCall: [String: Subagent] {
        Dictionary(subagents.compactMap { a in a.toolUseID.map { ($0, a) } }, uniquingKeysWith: { a, _ in a })
    }
    private var sessionWorks: Bool { session?.state == .working || session?.state == .needsYou }
    /// The chat's newest plan (`ChatPlan.latest`), worked out once per change of its items.
    private var latestPlan: ChatPlan? {
        if let memo = scratch.plan, memo.version == store.version, memo.key == shownKey { return memo.plan }
        let plan = ChatPlan.latest(store.items)
        scratch.plan = (shownKey, store.version, plan)
        return plan
    }
    /// The subagents the running exchange waits on (its work line says so).
    private var agentsWorking: AgentsWorking? { AgentsWorking.of(subagents, tasks: model.chat(key).tasks) }
    private var status: SessionStatus {
        if let a = viewedAgent { return a.running ? .working : .idle }
        return session?.status ?? .unknown
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ChatView")
        // `-showPerfChanges YES`: what made the page draw again, in the log (docs/apple/mac.md「Measuring hitches on Mac」).
        let _ = Self.printsChanges ? Self._printChanges() : ()
        #endif
        // Build the page sequentially across `@inline(never)` type-erased stages so an unoptimized Debug build never
        // keeps `chatLifecycle` + `chatActivity` + `chatPresentation` + `chatSurface` + `paneContent` + `chatPane`
        // live on the main thread stack simultaneously (#6735, `MainPagesStackTests`). Every stage wraps the same
        // static SwiftUI view tree in `AnyView`, preserving view identity and state.
        let _ = syncChatHandlers()
        let stage0 = buildSwipeablePanes()
        let stage1 = applyChatSurface(stage0)
        let stage2 = applyChatPresentation(stage1)
        let stage3 = applyChatActivityTop(stage2)
        let stage4 = applyChatActivityMiddle(stage3)
        let stage5 = applyChatActivityBottom(stage4)
        let stage6 = applyChatLifecycleTop(stage5)
        let stage7 = applyChatLifecycleBottom(stage6)
        return applyChatPageShell(stage7)
    }

    @inline(never) private func syncChatHandlers() {
        actions.set(handlers)
        overviewUI.setHandlers(
            openOutput: { t in
                if let out = t.outputFile { file = FileRef(group: key.group, machineID: key.machineID, path: out, cwd: session?.cwd) }
            },
            showInChat: { target in
                showSubagentInChat(target)
            },
            canShowInChat: { target in
                canShowSubagentInChat(target)
            }
        )
        _ = childOpener.set { childKey in
            overviewUI.openDetail(.session(childKey), fromOverview: false)
        }
    }

    @inline(never) private func buildSwipeablePanes() -> AnyView {
        AnyView(swipeablePanes)
    }

    @inline(never) private func applyChatPageShell(_ base: AnyView) -> AnyView {
        // Every row's memo below (RowMemo) is this chat's: item ids repeat across chats (#372). Outermost, so the
        // sheets this page presents (a tool's detail) are inside it too.
        base
            .background { DismissReader(holder: dismissChat) }
            .environment(\.rowMemoScope, RowMemo.scope(shownKey))
            // A tap on a word of a message looks it up while a text_lookup extension answers (#10977).
            .textLookupSurface(style == .assistant ? "chat" : "agent")
            .environment(\.assistantCardConversation, style == .assistant ? shownKey : nil)
            // Only this chat hands its lifted message to the page (a chat shown in a sheet from it draws its own).
            .environment(\.messageLiftHost, nil)
            // A pressed message, lifted over the whole page with its bar (#603); over the page around it where that
            // floats a bar over the chat (Persona's conversation, `MessageLiftHost`, #3188).
            .liftBlur(menu)
            .stackSegment()
            .overlay { if liftHost == nil { MessageLiftLayer(menu: menu) } }
            .devScreen(style == .assistant ? "assistant-chat" : "chat")
            .onAppear { liftHost?.menu = menu }
            .onDisappear { if liftHost?.menu === menu { liftHost?.menu = nil } }
            .codexTakeoverDialogs(coordinator: takeoverCoordinator) { adopted in
                model.adopt(adopted)
                switchTo(adopted.key)
            }
            .stackSegment()
    }

    @inline(never) private func applyChatSurface(_ base: AnyView) -> AnyView {
        base
        // A readable measure on a wide page (iPhone Duo open, nothing beside the chat); a no-op on other iPhones.
        .frame(maxWidth: Self.readableWidth)
        // Where a message's action bar may go (MessageMenu): this chat, not the whole screen.
        .coordinateSpace(.named(MessageMenu.area))
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { menu.areaWidth = $0 }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { pageHeight = $0 }
        .frame(maxWidth: .infinity)
        .background(Theme.pageBackground)
        .background {
            if backgroundActivity {
                Color.clear.accessibilityElement().accessibilityIdentifier("background-activity-page")
            }
        }
        .stackSegment()
        .navigationTitle(title)
        .navigationBarTitleDisplayMode(.inline)
        // Agents chats draw their own header (devChrome) so it can follow the scroll; the assistant's floats over the
        // chat (AssistantTopBar, drawn by AssistantView over this page and the new-conversation page). Where the system
        // puts bars down the side (iPhone Duo), an Agents chat hands its actions to it instead: they go in that column.
        .toolbarVisibility(backgroundActivity || sideBars ? .visible : .hidden, for: .navigationBar)
        .navigationBarBackButtonHidden(backgroundActivity || (sideBars && !systemBack))
        .pageToolbar { if sideBars { sideBarItems } }
        // The system draws that bar's title over the content, not in the column: a second title right above the
        // header's own. Removed while the header shows its title; while typing the header is only fog, and the
        // system's title names the session.
        .toolbar(removing: sideBars && !composerTyping ? .title : nil)
        .readsVerticalBars($barsVertical)
        // Here, not on the header: a pick from the composer's model menu asks too, and the folded header leaves the tree.
        .alert(modeConfirm.map(modeConfirmTitle) ?? "", isPresented: Binding(get: { modeConfirm != nil }, set: { if !$0 { modeConfirm = nil } }),
               presenting: modeConfirm) { c in
            Button(modeConfirmAction(c), role: .destructive) { applyMode(c.mode) }
            Button("Cancel", role: .cancel) {}
        } message: { c in
            Text(modeConfirmMessage(c))
        }
        // The header's overlay and the side-bars top under it start a segment of their own (#9709): the chain had grown
        // two longer at its end, where each slot holds all before it.
        .stackSegment()
        .overlay(alignment: .top) {
            if style == .dev { if sideBars { sideHeader } else { devChrome } }
        }
        #if os(iOS)
        // The page's top under side bars in a split is the chat's own, not its navigation bar's (`pinsSideTop`).
        .safeAreaPadding(.top, pinsSideTop ? Theme.Space.xl : 0)
        .ignoresSafeArea(.container, edges: pinsSideTop ? .top : [])
        #endif
        .stackSegment()
    }

    /// The closeout the header's card shows (#3612, `CloseoutCard`): a stopped agent's own account of its work, on its
    /// own chat (not a subagent's, not an assistant conversation).
    private var shownCloseout: Closeout? {
        guard style == .dev, pane == .chat, viewing == nil, let session, !session.assistant else { return nil }
        return session.shownCloseout
    }

    /// The card, at most a share of the page tall (as the workflow section): the conversation stays under it.
    @ViewBuilder private var closeoutCard: some View {
        if let closeout = shownCloseout {
            // Under an open workflow section it starts folded: the two would take most of the page.
            CloseoutCard(closeout: closeout, artifacts: session?.work?.artifacts ?? [],
                         maxHeight: pageHeight > 0 ? pageHeight * Theme.Size.workflowShare : nil, startsOpen: !hasWorkflows)
                // A list row carries only where the closeout stands; what changed and what was verified come with
                // the session's own read. One that arrived without them (a list push after the chat opened) is read again.
                .task(id: CloseoutRead(key: key, closeout: closeout)) {
                    if closeout.did == nil, closeout.verified == nil { await model.refreshSessionDetail(key) }
                }
        }
    }

    /// A workflow runs in the background (#2935), or one just ended: its section shows (only in the chat pane).
    private var hasWorkflows: Bool { pane == .chat && !(session?.shownWorkflows().isEmpty ?? true) }

    /// 「后台 workflow」 above the subagents: a card per running workflow, its phases, progress and agents; an agent opens
    /// here like a subagent (its `session_id` reads as one). Beside the side bars the header never folds: the cards are
    /// their heads until tapped.
    @ViewBuilder private var workflowBar: some View {
        if hasWorkflows, let session {
            WorkflowSection(runs: session.shownWorkflows(), maxHeight: pageHeight > 0 ? pageHeight * Theme.Size.workflowShare : nil,
                            compact: sideBars) { a in
                openWorkflowAgent(a)
            }
        }
    }

    /// The folds holding the Workflow call of a run the hub lists (`WorkflowRun.foldsHoldingCalls`), on the main
    /// agent's chat. Nothing to look through for a chat without runs.
    private var workflowFolds: [Item] {
        guard viewing == nil, let runs = session?.workflows, !runs.isEmpty else { return [] }
        return WorkflowRun.foldsHoldingCalls(store.items, runs: runs)
    }

    /// Fetches them; one that did not come (the hub restarting, the phone offline, not connected yet) is asked for again
    /// after a wait that doubles (`WorkflowCallCard.fetchFolds`). A fold that came is gone from the items; one that comes
    /// back (the chat loaded afresh, another chat and back) is fetched again.
    private func unfoldWorkflowCalls(_ folds: [Item]) async {
        let key = shownKey
        await WorkflowCallCard.fetchFolds(folds) { [model] f in
            try? await model.unfold(key, f)
            // Came: it gave its place to its work. Still there: it failed, or there was no hub to ask.
            return !model.chat(key).items.contains { $0.id == f.id }
        }
    }

    /// One of a workflow's agents, opened here like a subagent (its `session_id` reads as one).
    private func openWorkflowAgent(_ a: WorkflowAgent) {
        guard let sid = a.sessionID else { return }
        view(Subagent(id: a.id, sessionID: sid, type: "workflow", description: a.label, running: a.running,
                      background: true, startedAt: a.startedAt, updatedAt: a.updatedAt, lastText: a.lastText))
    }


    @inline(never) private func applyChatPresentation(_ base: AnyView) -> AnyView {
        base
        // Unified Session Overview sheet (iPhone .medium/.large) / inspector (iPad) + full Subagent Detail navigation (#27, #14322).
        .modifier(SessionOverviewPresentation(
            parent: key,
            assistant: style == .assistant,
            on: !backgroundActivity,
            ui: overviewUI
        ))
        .stackSegment()
        .sheet(isPresented: Binding(get: { style == .assistant && TasksStore.shared.showBoard },
                                    set: { TasksStore.shared.showBoard = $0 }),
               onDismiss: {
                   let tasks = TasksStore.shared
                   if let id = tasks.openAfterBoard { tasks.openAfterBoard = nil; tasks.detailTask = id }
                   if let key = tasks.openAgentAfterBoard { tasks.openAgentAfterBoard = nil; tasks.detailAgent = key }
               }) {
            TaskBoardSheet(conversation: key.sessionID)
        }
        // A task's own detail: assigning detailTask must not be a dead link. iPhone dispatch links push a live Agent
        // directly; this is the starting/no-Agent fallback and the normal task detail.
        // Not where the stack takes it by value (the Sidebar layout's page, `taskDetailByValue`, #13156).
        .detailPresentation(isPresented: Binding(get: { style == .assistant && !taskDetailByValue
                                                        && (TasksStore.shared.detailTask != nil || TasksStore.shared.detailAgent != nil) },
                                                 set: { if !$0 { TasksStore.shared.detailTask = nil; TasksStore.shared.detailAgent = nil } })) {
            Group {
                if let id = TasksStore.shared.detailTask {
                    TaskDetailSheet(todoID: id, initialAgent: TasksStore.shared.detailTaskAgent).id(id)
                }
                else if let key = TasksStore.shared.detailAgent { TaskDetailSheet(agent: key) }
            }
        }
        .stackSegment()
    }

    @inline(never) private func applyChatActivityTop(_ base: AnyView) -> AnyView {
        // AnyView between stages: the page's modifiers as one type were ~60 levels deep, and the runtime spent
        // about 100 ms building that type's metadata the first time the page drew (cold start, #1337). Each boundary
        // is the same type every time, so nothing below loses its identity or state. Split into sequential
        // `@inline(never)` stages so an unoptimized Debug build never stacks their frames together (#6735, `MainPagesStackTests`).
        base
        .task(id: "\(key.machineID)/\(key.sessionID)|\(session?.cwd ?? "")") { await loadGit() }
        #if os(iOS)
        .task(id: "\(key.path)|\(session != nil)") { takeAgentMenuDestination() }
        #endif
        // A Workflow call the hub folded into a turn's work (#3436): that fold is fetched, so the run's card comes out.
        .task(id: workflowFolds.map(\.id)) { await unfoldWorkflowCalls(workflowFolds) }
        .task(id: FeedScope(key: key, manager: feedManager)) {
            guard style == .assistant else { return }
            TasksStore.shared.load(key.sessionID, api: model.api(key.group)?.manager(feedManager))
            if key == model.assistantMain { TasksStore.shared.loadToday(key.sessionID) }
            #if DEBUG
            // `-showTaskDetail <todo id>` / `-showTaskBoard YES`: a task's detail or the board, for screenshots.
            if let id = UserDefaults.standard.string(forKey: "showTaskDetail") {
                // On the Sidebar layout's path (#13156) after the launch's first assistant switch, which empties that path.
                if taskDetailByValue { try? await Task.sleep(for: .seconds(2)) }
                TasksStore.shared.detailTask = id
            }
            if UserDefaults.standard.bool(forKey: "showTaskBoard") { TasksStore.shared.showBoard = true }
            #endif
        }
        // Left alone for a few seconds, the open chrome folds away by itself (the page goes back to being all chat).
        .stackSegment()
        .task(id: style == .dev && pane == .chat) {
            guard style == .dev, pane == .chat, ChatChrome.folds else { return }
            scratch.lastActivity = .now
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(500))
                if chrome.collapse == 0, !overviewUI.showOverview, !scratch.userDragging, !Self.somethingPresented,
                   Date.now.timeIntervalSince(scratch.lastActivity) >= Self.chromeIdleSeconds {
                    hideChrome()
                }
            }
        }
        .onChange(of: computerActivity, initial: true) { _, a in if let a { model.computerActivities[a.machine] = a } }
        .onChange(of: pane) { _, p in
            showChrome()
            // The chat's scroll view is rebuilt when you come back to it: start at the latest message.
            if p == .chat {
                position = ScrollPosition(edge: .top)
                settled = false
                setFollow { $0.follow() }
                settleAtEnd()
            }
        }
        // A different chat shown in place: its own cards decide whether another conversation needs you (#4348).
        .onChange(of: key) { _, _ in
            offeredNext = nil
            dock = QuestionDockState()
        }
        // Leaving the conversation (another agent shown in place, the page gone, or hidden in the shell: `onTabVisibility`)
        // leaves the phone's voice chat bound to it with the bar back to it at the top, and coming back takes the bar away
        // (docs/assistant/voice.md 6.5; #9887 one button: it no longer ends there).
        .onChange(of: key) { old, new in
            model.voice?.leave(old)
            model.voice?.returned(new)
        }
        .onTabVisibility(appear: { [key, model] in model.voice?.returned(key) }, disappear: { [key, model] in model.voice?.leave(key) })
        // The bar back to this conversation's voice chat tapped over a cover of this page (#9887: a full-screen picture,
        // the terminal): the cover goes, and the page showing again takes the bar away (`VoiceCalls.returnTo`).
        .onChange(of: model.voice?.isReturning(to: key) ?? false) { _, back in if back { closeCovers() } }
        .onChange(of: status) { _, st in if !st.isActive { Task { await loadGit() } } }
        .stackSegment()
    }

    @inline(never) private func applyChatActivityMiddle(_ base: AnyView) -> AnyView {
        // One stack: the chat takes the whole screen. Beside the list (split) the tab bar stays, or there is no way out.
        base
        .hidesTabBarWhenShown(style == .dev && split == nil)
        .fullScreenCover(isPresented: Binding(get: { showTerminal && session?.canTerminal == true },
                                              set: { showTerminal = $0; if !$0 { resumeOnOpen = false } })) {
            TerminalCover(key: key, switchTo: switchTo, resumeOnOpen: resumeOnOpen).presentationBackground(.clear)
        }
        .stackSegment()
        .renameSessionAlert($renaming)
        // The presenters that each hold a binding to a session (rename, link to a todo, close, delete) got one more
        // with the link sheet: cut in two (#9709, `MainPagesStackTests`).
        .stackSegment()
        #if os(iOS)
        .sheet(item: Binding(get: { AgentMenuRouting.shared.linkingSession },
                              set: { AgentMenuRouting.shared.linkingSession = $0 })) {
            TodoLinkAgentSheet(session: $0)
        }
        #endif
        .confirmClose($closing) { closedAgent($0, order: []) }
        .confirmDeleteSession($deleting)
        // Deleted here or from the list beside it: the chat goes (AppModel.deletedSession).
        .onChange(of: model.deletedSession) { _, gone in if let gone { closedAgent(gone, order: []) } }
        .stackSegment()
        .sheet(item: $file) { ref in
            FileViewer(ref: ref, key: key)
                .environment(\.showInFiles, (session?.cwd ?? "").isEmpty ? nil : { target in
                    file = nil
                    filesReveal = target
                    withAnimation(Theme.Motion.standard) { pane = .files }
                })
        }
        .stackSegment()
        // Presented here, pushed in Knowledge: the ✕ belongs to the sheet (#9653).
        .sheet(item: $openedKnowledge) { entry in
            NavigationStack { KnowledgeDetailView(entry: entry).toolbar { SheetClose { openedKnowledge = nil } } }
        }
        .sheet(isPresented: $planOpen) { PlanSheet(plan: latestPlan) }
        .stackSegment()
    }

    @inline(never) private func applyChatActivityBottom(_ base: AnyView) -> AnyView {
        base
        #if DEBUG
        // Simulator automation: `-showComposerPhoto YES` puts a made-up picture in the composer, as if picked from
        // Photos (the UI tests of a sent picture showing in the chat); `-showComposerPhoto 22` that many, past the hub's
        // pictures too (#12973's screenshots: the rest go as files).
        .task {
            let d = UserDefaults.standard
            let count = max(d.bool(forKey: "showComposerPhoto") ? 1 : 0, d.integer(forKey: "showComposerPhoto"))
            if count > 0 { model.composerInbox[key, default: []].append(contentsOf: Array(repeating: Self.debugPhoto, count: count)) }
        }
        // Simulator automation: `-openFirstImage YES` opens the viewer on the first image once it loads.
        .onChange(of: store.rows.count) { _, _ in
            guard UserDefaults.standard.bool(forKey: "openFirstImage"), debugImages == nil else { return }
            let imgs = store.items.compactMap(\.image)
            if !imgs.isEmpty { debugImages = imgs }
        }
        .fullScreenCover(isPresented: Binding(get: { debugImages != nil }, set: { if !$0 { debugImages = [] } })) {
            if let imgs = debugImages, !imgs.isEmpty {
                ImageViewer(images: imgs, index: imgs.count - 1, conversation: imgs) { img in
                    model.composerInbox[key, default: []].append(img)
                }
            }
        }
        #endif
        // Pushed into the detail column in regular width (iPad, #2816), a sheet otherwise.
        .stackSegment()
        .hubLinkSheet($hubSheet)
        .detailPresentation(item: $linkedTodo) { r in
            DetailStack {
                TodoDetailView(todoID: r.id).sheetDone { linkedTodo = nil }
            }
        }
        .detailPresentation(isPresented: $showTodoDetail) {
            if let s = session, let todoID = s.todo?.id,
                TodosLogic.isOnTodosHub(sessionGroup: s.key.group, hubGroup: model.hub?.group) {
                DetailStack {
                    TodoDetailView(todoID: todoID).sheetDone { showTodoDetail = false }
                }
            }
        }
        .stackSegment()
    }

    #if os(iOS)
    /// A page opened from a message covers the main chat even while its underlying view stays mounted.
    private var coveredMainChat: Bool? {
        guard replySpeechContext != nil, style == .assistant, !backgroundActivity, key == model.assistantMain else { return nil }
        return link != nil || viewingImages != nil || file != nil || openedKnowledge != nil || linkedTodo != nil
            || hubSheet != nil || previewing || planOpen || showAssistantModel || readinessRoute != nil || showTodoDetail
    }
    #endif

    @inline(never) private func applyChatLifecycleTop(_ base: AnyView) -> AnyView {
        base
        .environment(\.openFile, actions.openFile)
        .environment(\.openTurnReview, actions.openTurnReview)
        .environment(\.fileScope, FileScope(machine: key.machineRef, cwd: session?.cwd ?? ""))
        .environment(\.chatImages, store.images)
        .environment(\.viewImages, actions.viewImages)
        .environment(\.imageZoom, imageZoom)
        .environment(\.openTools, openTools)
        .environment(\.plainRefusals, session?.assistant == true) // a Persona conversation: plain words (#5202)
        .fullScreenCover(item: $viewingImages) { v in
            Group {
                if let viewed = v.viewed {
                    ImageViewer(items: viewed, index: v.index) { [model, key] img in model.composerInbox[key, default: []].append(img) }
                } else {
                    ImageViewer(images: v.images, index: v.index, conversation: v.conversation) { [model, key] img in
                        model.composerInbox[key, default: []].append(img)
                    }
                }
            }
            .navigationTransition(.zoom(sourceID: v.source, in: imageZoom))
        }
        .stackSegment()
        .environment(\.openLink, actions.openLink)
        .environment(\.openPort, actions.openPort)
        // Web links open in the in-app sheet (Feishu docs in the native reader); local paths open the machine's file viewer.
        .environment(\.openURL, actions.openURL)
        #if os(iOS)
        .onChange(of: coveredMainChat, initial: true) { _, covered in
            if let covered { replySpeechContext?.chatSheetOpen = covered }
        }
        #endif
        .sheet(item: $link) { t in LinkSheet(target: t, key: key, choosePreview: { openPort($0.link, name: $0.name) }) }
        .readinessSheets($readinessRoute)
        .onChange(of: showAssistantModel) { _, on in
            if on, assistantPanel == nil {
                assistantPanel = AssistantModelPanelModel(key: key, machine: key.machineRef, defaultRule: conversationDefaultRule)
            }
        }
        .sheet(isPresented: $showAssistantModel, onDismiss: { assistantPanel = nil }) {
            if let assistantPanel {
                AssistantModelSheet(session: model.session(key), panel: assistantPanel)
            }
        }
        #if os(iOS)
        .sheet(isPresented: $previewing, onDismiss: {
            if let o = previewPicked { previewPicked = nil; openPort(o.link, name: o.name) }
        }) {
            if let cwd = session?.cwd { PreviewChooser(key: key, cwd: cwd) { o in previewPicked = o; previewing = false } }
        }
        #endif
        .stackSegment()
    }

    @inline(never) private func applyChatLifecycleBottom(_ base: AnyView) -> AnyView {
        base
        .environment(\.attachImage, actions.attachImage)
        .environment(\.loadImage, actions.loadImage)
        .environment(\.loadFullText, actions.loadFullText)
        #if os(iOS)
        .environment(\.starredChat, starredChatContext)
        #endif
        .onAppear(perform: appeared)
        // The same chat was already open (the app left on it) when its "Done" notification was tapped: no new entry.
        .onChange(of: model.latestAnswerFor) { _, k in
            guard style == .dev, k == key, entered, takeLatestAnswer() else { return }
            entry = .latestAnswer
            landEntry(currentLines())
        }
        // A hub link to a message of this chat, while it is open (#2696).
        .stackSegment()
        .onChange(of: model.messageJump) { _, _ in
            guard entered, takeMessageJump() else { return }
            landEntry(currentLines())
        }
        // The launcher's list behind this session's model menus (#3754): read once it can switch, kept for a while.
        .task(id: "\(SessionModes.switchesModel(session))|\(SessionModes.launcher(session))") {
            await RunningModelLists.shared.load(key, session)
        }
        // A refused send's Open Terminal: a dialog holds this session's terminal (#3752).
        .onChange(of: model.terminalAsk) { _, ask in
            guard let ask, ask == key else { return }
            model.terminalAsk = nil
            if session?.canTerminal == true { openTerminal() }
        }
        // Going to the background is the last chance before iOS may end the app: a cold relaunch (the dev chat is
        // restored, AppModel.rememberChat) then opens at the same line, not only after leaving the chat.
        .onChange(of: scenePhase) { _, phase in if !backgroundActivity, phase == .background { rememberReadPosition() } }
        .onDisappear {
            ChatTrace.shared.note(shownKey, "close")
            #if DEBUG
            ShellTrace.shared.note("chat disappeared \(style == .assistant ? "assistant" : "agents")")
            #endif
        }
        // Kept shell pages do not disappear. Only the visible chat owns its
        // transcript stream; the Agent keeps working on the hub while hidden.
        // Returning subscribes from the retained store's covered sequence.
        .stackSegment()
        .onTabVisibility(appear: {
            model.subscribe(key, by: owner)
            if viewing != nil { model.subscribe(shownKey, by: owner) }
            if !backgroundActivity {
                model.show(key, by: owner)
                model.markSeen(key)
            }
            #if os(iOS)
            if !backgroundActivity {
                fullPage.document = { fullPageDocument() }
                fullPage.setShown(true)
            }
            #endif
        }, disappear: {
            if !backgroundActivity { rememberReadPosition() }
            if viewing != nil { model.unsubscribe(shownKey, by: owner) }
            model.unsubscribe(key, by: owner)
            if !backgroundActivity { model.hide(by: owner) }
            #if os(iOS)
            fullPage.setShown(false)
            fullPage.document = nil // it holds this view, whose state holds the page
            #endif
        })
        #if DEBUG && os(iOS)
        // `-fullPagePDF <path>`: once the chat has loaded, writes its Full Page PDF there and the report beside it
        // (<path>.json); `-fullPageMaxHeight <pt>` lowers the height limit (#1609).
        .task(id: "full-page-debug") {
            guard let path = UserDefaults.standard.string(forKey: "fullPagePDF"), !path.isEmpty else { return }
            for _ in 0..<120 where !store.loadedOnce { try? await Task.sleep(for: .milliseconds(250)) }
            try? await Task.sleep(for: .seconds(3))
            var limits = fullPage.limits
            let max = UserDefaults.standard.double(forKey: "fullPageMaxHeight")
            if max > 0 { limits.maxHeight = max }
            if case .success(let pdf) = await fullPage.export(limits: limits) {
                try? pdf.data.write(to: URL(fileURLWithPath: path))
            }
            if let report = fullPage.lastReport, let data = try? JSONEncoder().encode(report) {
                try? data.write(to: URL(fileURLWithPath: path + ".json"))
            }
        }
        #endif
        .onChange(of: session?.needsSeen()) { _, unseen in
            if !backgroundActivity, unseen == true && model.visibleSession == key { model.markSeen(key) }
        }
        .stackSegment()
    }

    #if os(iOS)
    /// What Full Page draws (#1609): the loaded lines, oldest first, as the list draws them; where the screen was.
    /// Unsent messages, the live text and the composer are not part of it.
    private func fullPageDocument() -> ChatFullPage.Document? {
        let lines = currentLines()
        guard !lines.isEmpty, let environment = fullPage.environment, scratch.listFrame.width > 0 else { return nil }
        let visible = Set(scratch.visibleLines)
        let anchor = scratch.atNewest ? nil : lines.first { visible.contains($0.id) }?.id
        return ChatFullPage.Document(lines: lines.map { line in .init(id: line.id, view: AnyView(lineView(line))) },
                                     width: scratch.listFrame.width, background: UIColor(Theme.pageBackground),
                                     environment: environment, anchor: anchor, viewport: scratch.listFrame.height)
    }
    #endif

    /// The chat as a flat list of lines (see `ChatLine`): every message of a turn opens its own exchange — the
    /// message, one work line for what the agent did after it, and the agent's last words before the next message.
    /// The chat's lines, rebuilt only when what they come from changed: the store's blocks, the running turn and
    /// which work lines are open. (Every re-render used to walk the whole history again.)
    private func currentLines() -> [ChatLine] {
        let items = store.items
        // Frozen: the items up to the snapshot (they are in transcript order); what comes after waits.
        let cut = frozen.map { f in Self.prefixCount(items) { ($0.seq, $0.part) <= (f.seq, f.part) } } ?? items.count
        let allFeed = style == .assistant ? ChatLines.drawnFeed(TasksStore.shared.feed(shownKey.sessionID)) : []
        let kids = model.childRows(shownKey)
        scratch.childRows = kids
        let modes = shownModes(until: frozen == nil ? nil : items[..<cut].last(where: { $0.ts != nil })?.ts ?? .distantPast)
        let childStamp = kids.map { "\($0.key.path):\($0.rev ?? 0):\($0.state.rawValue):\($0.updatedAt?.timeIntervalSince1970 ?? 0)" }.joined(separator: "|")
        let key = ChatScratch.LinesKey(store: "\(shownKey.machineID)/\(shownKey.sessionID)", version: frozen == nil ? store.version : cut,
                                       first: items.first?.id, live: frozen == nil ? liveTurnID : frozen?.live, open: openWork,
                                       runs: openRuns, frozen: frozen, feed: ChatLines.feedKey(allFeed),
                                       streaming: drawsLive && (frozen.map { !$0.text.isEmpty } ?? liveShown),
                                       quietAgents: quietSubagents, workflowCalls: workflowCalls,
                                       day: style == .assistant ? Calendar.current.startOfDay(for: .now) : nil,
                                       supervision: openSupervision, active: shownRoundActive, background: foldsBackground,
                                       foldRoutineRelayReplies: foldRoutineRelayReplies, latestReplyNeedsAttention: latestReplyNeedsAttention,
                                       closeout: shownCloseout != nil, children: childStamp, modes: modes.map { "\($0.kind)@\($0.at.timeIntervalSince1970)" },
                                       turnChanges: showsTurnChanges, cwd: showsTurnChanges ? (session?.cwd ?? "") : "",
                                       botmuxMode: botmuxMode, botmuxSession: isBotmuxSession)
        if scratch.linesKey == key { return scratch.lines }
        var blocks = store.blocks
        if frozen != nil {
            let built = ChatStore.rebuild(Array(items[..<cut]), settled: scratch.frozenSettled)
            scratch.frozenSettled = built.settled
            blocks = built.blocks
        }
        var builder = lineBuilder
        var seen = model.childIndex.seen
        builder.childPlaces = ChildSessions.places(kids, seen: &seen)
        model.childIndex.seen = seen
        builder.childSessions = Dictionary(kids.map { ($0.key, $0) }, uniquingKeysWith: { _, newer in newer })
        builder.historyComplete = store.loadedOnce && !store.hasMore
        let presentation: ChatLines.Presentation = backgroundActivity ? .backgroundActivity : .timeline
        let projection = builder.projection(Self.debugTrim(blocks), live: key.live, streaming: key.streaming,
                                            presentation: presentation)
        let feed = projection.feed(allFeed, for: presentation)
        var lines = backgroundActivity ? projection.activity : projection.timeline
        if NativeTemplateStore.shared.plainMainChat, style == .assistant, !backgroundActivity {
            // Questions, replies and work needing the owner. Finished patrols and briefings stay in Background activity;
            // a live supervision round, plans, tasks, live cards and errors remain in the main timeline (#17129).
            lines = lines.filter { line in
                switch line.content {
                case .supervision(_, _, let live, _): live
                case .feed(let item): ![FeedItem.Kind.brief, .today, .inboxSuggestion].contains(item.kind)
                default: true
                }
            }
        }
        lines = ModelChanges.mark(lines)
        #if os(iOS)
        // An Agent's long finished answers as several rows: one row many screens tall stuttered whenever it was on
        // screen (#4970). Persona's own conversation keeps its boxed answers whole.
        if style == .dev { lines = ChatLines.splitAnswers(lines) }
        #endif
        // The permission mode changes the hub recorded (#10297), each at the end of the exchange it happened in; while
        // older history is still to load, one from before the first loaded item waits for it.
        if !key.modes.isEmpty {
            lines = ChatLines.mergeModeChanges(lines, modes, earliest: store.hasMore ? items.lazy.compactMap(\.ts).first ?? .distantFuture : nil)
        }
        if style == .assistant { // docs/assistant/unified-entry.md: the hub's items, quick results, a line for each day
            if backgroundActivity {
                lines = ChatLines.quickResults(ChatLines.mergeActivityFeed(lines, feed))
            } else {
                // Dispatch links have no instruction list. Keep Persona's hand-offs as their own folded lines instead
                // of hiding them as though a full task card still rendered them (#3711).
                lines = ChatLines.daySections(ChatLines.quickResults(ChatLines.mergeFeed(lines, feed)))
            }
        }
        lines = ChatLines.sourceRuns(lines)
        // The card in the header says the closeout (#3612): the reply that carried the raw block shows without it.
        if key.closeout { lines = ChatLines.hidingCloseout(lines) }
        scratch.lines = lines
        scratch.linesKey = key
        ChatTrace.shared.note(shownKey, ChatTrace.screen(lines, live: store.liveText.count, streaming: key.streaming, running: key.live != nil,
                                                         frozen: frozen != nil, withheld: items.count - cut))
        return lines
    }

    /// This chat's mode changes (`loadModeChanges`); frozen (the user reading back), only those up to `until`, the time
    /// of the last item shown: what came after waits with the rest.
    private func shownModes(until: Date?) -> [APIClient.ModeChange] {
        guard style == .dev, let m = modeChanges, m.key == shownKey else { return [] }
        guard let until else { return m.changes }
        return m.changes.filter { $0.at <= until }
    }

    /// The main conversation and the leader conversations fold their background rounds while the hub's switch is on
    /// (docs/apple/ios.md "Background rounds", #6595).
    private var foldsBackground: Bool {
        ChatLines.foldsBackground(assistant: style == .assistant, conversation: shownKey.sessionID,
                                  main: model.assistantMain?.sessionID, on: model.mainBackgroundFolded,
                                  leaders: Set(model.assistantLeaders))
    }

    private var latestReplyNeedsAttention: Bool {
        ChatLines.needsReplyAttention(session)
    }

    private var foldRoutineRelayReplies: Bool {
        style == .assistant && shownKey == model.assistantMain && model.assistantConfig?.foldRoutineRelayReplies == true
    }

    /// A later turn must not make a frozen historical question look like a live one.
    private var shownRoundActive: Bool { frozen.map { $0.live != nil } ?? status.isActive }

    /// Completed Agent exchanges show the Changed Files card (never Assistant conversations or subagent views).
    private var showsTurnChanges: Bool { style == .dev && session?.assistant == false && viewing == nil }

    /// Whether the Changed Files card can open branch review for this session.
    private var canReviewTurnChanges: Bool {
        #if os(iOS)
        showsTurnChanges && model.features(key.group).contains("git_review") && !(session?.cwd ?? "").isEmpty
        #else
        false
        #endif
    }

    /// What builds the lines from the blocks, with what is open now.
    private var lineBuilder: ChatLines {
        ChatLines(openWork: openWork, openAll: Self.debugOpenAll, openRuns: openRuns,
                  openAllRuns: Self.debugOpenRuns, botmuxMode: botmuxMode, botmuxSession: isBotmuxSession,
                  chipRuns: AppStyle.current.usesBeautifulUI,
                  foldsIntoRun: foldsIntoRun, includeTurnChanges: showsTurnChanges,
                  turnChangesCwd: showsTurnChanges ? session?.cwd : nil,
                  foldSupervision: style == .assistant, foldBackground: foldsBackground,
                  main: shownKey.sessionID == model.assistantMain?.sessionID,
                  foldRoutineRelayReplies: foldRoutineRelayReplies, latestReplyNeedsAttention: latestReplyNeedsAttention,
                  openSupervision: openSupervision, openAllSupervision: Self.debugOpenSupervision, active: shownRoundActive)
    }
    private func unfoldWork(_ lines: [ChatLine]) -> [ChatLine] { lineBuilder.unfold(lines) }
    private func isOpen(_ work: String) -> Bool { lineBuilder.isOpen(work, live: false) }
    private func isOpen(_ work: String, live: Bool) -> Bool { lineBuilder.isOpen(work, live: live) }
    /// Opens or folds a work line from wherever it is (including a jump to a step).
    private func flipWork(_ id: String) {
        if openWork.contains(id) { openWork.remove(id) } else { openWork.insert(id) }
    }

    private func isSupervisionOpen(_ id: String) -> Bool {
        openSupervision.contains(id) != Self.debugOpenSupervision
    }

    private func flipSupervision(_ id: String) {
        if openSupervision.contains(id) { openSupervision.remove(id) } else { openSupervision.insert(id) }
    }

    private func isRunOpen(_ id: String) -> Bool { openRuns.contains(id) != Self.debugOpenRuns }
    private func runID(holding call: String, group: String, items: [WorkItem]) -> String? {
        lineBuilder.runID(holding: call, group: group, items: items)
    }
    private func foldsIntoRun(_ call: ToolCall) -> Bool {
        if call.id.hasPrefix(ChildSessions.callPrefix) { return false } // a child session's card stays its own step
        if SubagentCard.isAgent(call) { return false } // its card is a line of its own
        guard let id = call.use.toolUseID else { return true }
        if WorkflowCallCard.staysOut(call, listed: workflowCalls) { return false } // its run's card (#3436)
        return !store.tasks.started.contains(id) && store.tasks.notes[id] == nil
    }

    /// The background subagents the hub says stopped: a card's state that is not in the chat's items (`LinesKey`).
    private var quietSubagents: Set<String> { Set(subagentsByCall.filter { !$0.value.running }.keys) }
    /// Listed Workflow calls keep their own cards inside opened work summaries, outside chip runs (`LinesKey`).
    private var workflowCalls: Set<String> { Set((session?.workflows ?? []).compactMap(\.toolUseID)) }

    static func foldAPIErrors(_ lines: [ChatLine]) -> [ChatLine] { ChatLines.foldAPIErrors(lines) }

    /// How many leading elements satisfy `pred` (true for a prefix, then false): a binary search.
    static func prefixCount(_ items: [Item], _ pred: (Item) -> Bool) -> Int {
        var lo = 0, hi = items.count
        while lo < hi {
            let mid = (lo + hi) / 2
            if pred(items[mid]) { lo = mid + 1 } else { hi = mid }
        }
        return lo
    }

    #if DEBUG
    /// Opens and folds a work line from its header, then from Collapse under its steps, logging how far the header and the
    /// reply under the steps moved on screen (both should be 0).
    private func foldProbe() async {
        func pause() async { try? await Task.sleep(for: .seconds(1.5)) }
        // Puts a line's top at a fraction of the list on screen: jump near, then correct by what is measured.
        func bring(_ id: String, to f: CGFloat) async {
            position.scrollTo(id: id, anchor: UnitPoint(x: 0.5, y: 1 - f))
            for _ in 0..<6 {
                try? await Task.sleep(for: .milliseconds(250))
                guard let now = scratch.lineY[id], Set(scratch.visibleLines).contains(id) else { continue }
                let target = scratch.listFrame.minY + scratch.listFrame.height * f
                if abs(target - now) < 1 { break }
                scrollList(to: chrome.offset + (target - now))
            }
            await pause()
        }
        try? await Task.sleep(for: .seconds(6))
        var log: [String] = []
        for kind in ["w-"] {
            // The newest work line of this kind with steps, brought into view.
            let newest = currentLines().last { if case .work(let id, let items, _, _, _) = $0.content { id.hasPrefix(kind) && !items.isEmpty } else { false } }
            guard let w = newest?.id else { log.append("no \(kind) work line"); continue }
            await bring(w, to: 0.3)
            guard let y0 = scratch.workHeadY[w], Set(scratch.visibleLines).contains(w) else { log.append("\(w) not on screen"); continue }
            toggleWork(w, fromHeader: true); await pause()
            let y1 = scratch.workHeadY[w] ?? .nan
            toggleWork(w, fromHeader: true); await pause()
            let y2 = scratch.workHeadY[w] ?? .nan
            log.append("header \(w) at \(Int(y0)): opening moved it \(Int(y1 - y0)), folding \(Int(y2 - y0))")
            toggleWork(w, fromHeader: true); await pause()
            let lines = currentLines()
            guard let e = lines.firstIndex(where: { $0.id == w + "-end" }), e + 1 < lines.count else { log.append("no line after the steps"); continue }
            let reply = lines[e + 1].id
            await bring(w + "-end", to: 0.4)
            let r0 = scratch.lineY[reply] ?? .nan, end0 = scratch.lineY[w + "-end"] ?? .nan
            toggleWork(w, fromHeader: false); await pause()
            let r1 = scratch.lineY[reply] ?? .nan
            log.append("收起 at \(Int(end0)), reply at \(Int(r0)): folding moved the reply \(Int(r1 - r0)); header now \(Int(scratch.workHeadY[w] ?? .nan))") // i18n-ignore
        }
        if let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first {
            try? log.joined(separator: "\n").write(to: dir.appending(path: "fold.txt"), atomically: true, encoding: .utf8)
        }
        // Left open with Collapse in the middle of the screen, for screenshots.
        if let w = currentLines().last(where: { if case .work(let id, let items, _, _, _) = $0.content { id.hasPrefix("w-") && !items.isEmpty } else { false } })?.id {
            toggleWork(w, fromHeader: false); await pause()
            await bring(w + "-end", to: 0.5)
        }
    }
    #endif

    #if DEBUG
    /// `-showUnfoldLab YES` with `-unfoldTrace YES`: the newest work line opened and folded from its header twice at the
    /// newest end, then twice with the list 40 pt into the history (reading), 2.5 s apart, each measured by
    /// `UnfoldTrace`: what `UnfoldTests` taps on iOS, for the Mac, which has no UI tests.
    private func unfoldLab() async {
        func pause() async { try? await Task.sleep(for: .seconds(2.5)) }
        try? await Task.sleep(for: .seconds(8))
        let newest = currentLines().last { if case .work(let id, let items, _, _, _) = $0.content { id.hasPrefix("w-") && !items.isEmpty } else { false } }
        guard let w = newest?.id else { return print("UNFOLD lab: no work line") }
        for _ in 0..<4 { toggleWork(w, fromHeader: true); await pause() }
        userScrolled = true
        setFollow { $0.read() }
        // 40 pt into the history: reading, its header still on a Mac window's screen.
        scrollList(to: chrome.offset + 40)
        await pause()
        print("UNFOLD lab reading: header at \(Int(scratch.workHeadY[w] ?? -1)), newest end \(scratch.atNewest ? "here" : "away")")
        for _ in 0..<4 { toggleWork(w, fromHeader: true); await pause() }
        print("UNFOLD lab done")
    }

    /// Scrolls into the history while the agent keeps working (`MOCK_STREAM=1`), then logs, every 2 s, where a line on
    /// screen is and how many items have arrived (the line must not move); back at the newest, the new lines show.
    private func freezeProbe() async {
        try? await Task.sleep(for: .seconds(8)) // a big chat takes a moment to load and lay out
        userScrolled = true
        setFollow { $0.read() }
        scrollList(to: chrome.offset + 260)
        try? await Task.sleep(for: .seconds(1.5))
        var log: [String] = []
        defer {
            if let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first {
                try? log.joined(separator: "\n").write(to: dir.appending(path: "freeze.txt"), atomically: true, encoding: .utf8)
            }
        }
        let mid = scratch.listFrame.midY
        // The measured line nearest the middle of the list (rows report their position while on screen).
        let onScreen = scratch.lineY.filter { scratch.listFrame.minY < $0.value && $0.value < scratch.listFrame.maxY }.map(\.key)
        guard let id = onScreen.min(by: { abs((scratch.lineY[$0] ?? .infinity) - mid) < abs((scratch.lineY[$1] ?? .infinity) - mid) }),
              let y0 = scratch.lineY[id] else {
            log.append("no line on screen (visible \(scratch.visibleLines.count), measured \(scratch.lineY.count), list \(scratch.listFrame), lines \(currentLines().count))")
            return
        }
        let n0 = store.items.count
        log.append("watching \(id) at \(Int(y0)) with \(n0) items")
        var moved = 0
        for _ in 0..<8 {
            try? await Task.sleep(for: .seconds(2))
            let y = scratch.lineY[id] ?? .nan
            moved = max(moved, Int(abs(y - y0)))
            log.append("items \(store.items.count), line at \(Int(y)) (moved \(Int(y - y0)))")
        }
        setFollow { $0.follow() }
        scrollToEnd()
        try? await Task.sleep(for: .seconds(2))
        let newest = currentLines().last?.id ?? ""
        let newestY = scratch.lineY[newest] ?? .nan
        log.append("RESULT: \(store.items.count - n0) items arrived while reading, line moved at most \(moved)pt; back at the newest: last line \(newest) on screen \(scratch.listFrame.minY < newestY && newestY < scratch.listFrame.maxY)")
    }
    #endif

    /// Opens a port of this conversation's machine: the phone listens for it and the sheet loads it from there; a
    /// port nothing listens on any more says so (its badge turns gray).
    /// A link to a page of a paired hub the app has itself (HubPageLink, #2696: the assistant's links to an agent, a
    /// conversation, a task, maybe one of its messages) opens that page here, not the web page. False: not one, or
    /// one the app cannot place (a conversation of a group whose assistant it does not know, another hub's task);
    /// the link opens as before.
    private func openHubPage(_ url: URL) -> Bool {
        guard let (group, page) = model.hubPage(url, preferring: key.group) else { return false }
        let target: SessionKey
        let item: String?
        switch page {
        case .todo(let id):
            guard TodosLogic.isOnTodosHub(sessionGroup: group, hubGroup: model.hub?.group) else { return false }
            #if os(iOS)
            if model.returnToStartIfHidden(.todos) { return true }
            #endif
            Haptics.tap()
            linkedTodo = TodoDetailRoute(id: id)
            return true
        case .agent(let machine, let session, let at):
            target = model.linkedSession(group, machine: machine, session: session)
            item = at
        case .conversation(let id, let at):
            guard let k = model.linkedConversation(group, id: id) else { return false }
            target = k
            item = at
        default:
            // Any other page of the route table (#9926): where the app has it, a detail over this chat.
            guard let opened = model.openHubPage(page, group: group) else { return false }
            Haptics.tap()
            if case .sheet(let s) = opened { hubSheet = s }
            return true
        }
        Haptics.tap()
        if target == key || target == shownKey { // this chat: only to the message
            if let item {
                entry = .message(item)
                scratch.entryPages = 0
                landEntry(currentLines())
            }
            return true
        }
        model.openLinked(target, item: item)
        return true
    }

    /// The session's dev servers to preview (#150): an Agent chat on a computer whose hub has previews (the iPhone; the
    /// iPad keeps the card too, the Mac app is frozen).
    private var canPreview: Bool {
        #if os(iOS)
        guard style == .dev, let s = session, s.assistant != true, !s.cwd.isEmpty,
              model.api(key.group)?.config.isDemo == false else { return false }
        return model.features(key.group).contains(PreviewLogic.feature)
        #else
        false
        #endif
    }

    /// Opens a port of the session's computer in the link card; `name`: the dev server it is (the card's subtitle).
    private func openPort(_ l: PortLink, name server: String? = nil) {
        let machine = key.machineRef
        Task {
            do {
                let url = try await model.forwardedURL(l, machine: machine)
                let name = model.machine(machine)?.title ?? machine.id
                let named = server.flatMap { $0 == ":\(l.port)" ? nil : $0 }
                link = LinkTarget(url: url, forwarded: "\(name) :\(l.port)" + (named.map { " · \($0)" } ?? ""))
            } catch {
                model.toast = Toast(error.localizedDescription, style: .error)
            }
        }
    }

    /// What this render's actions do (`ChatActions`).
    private var handlers: ChatActions.Handlers {
        ChatActions.Handlers(
            openFile: { [key, session] path in file = FileRef(group: key.group, machineID: key.machineID, path: path, cwd: session?.cwd) },
            openFileAt: { [key, session] path, line in
                file = FileRef(group: key.group, machineID: key.machineID, path: path, cwd: session?.cwd, line: line)
            },
            // A link to this machine's localhost is one of its ports, opened through the hub (docs/operations/ports.md).
            // The hub's own pages first: a hub on the agent's machine (or the phone's localhost, a mock) would read as a port.
            openLink: { [model, key] url in
                if openHubPage(url) { return }
                if let l = PortLinks.link(url, hostnames: model.hostnames(of: key.machineRef)) { openPort(l) } else { link = LinkTarget(url: url, manager: linkManager) }
            },
            openPort: { openPort($0) },
            viewImages: { viewingImages = $0 },
            attachImage: { [model, key] img in model.composerInbox[key, default: []].append(img) },
            loadImage: { [model, key] path in
                guard let f = try? await model.api(key.group)?.file(machine: key.machineID, path: path, cwd: nil), let data = f.data else { return nil }
                return ImagePayload(mime: f.mime ?? "image/jpeg", data: data)
            },
            // The session on screen: a subagent's items live in its own transcript.
            loadFullText: { [model, shownKey] id in
                guard let api = model.api(shownKey.group) else { throw CancellationError() }
                return try await api.fullItem(shownKey, id: id).text ?? ""
            },
            openSubagent: { toolUseID in
                if let a = session?.subagents?.first(where: { $0.toolUseID == toolUseID }) {
                    if host == nil {
                        overviewUI.openDetail(.embedded(parent: key, agent: a), fromOverview: false)
                    } else {
                        view(a)
                    }
                }
            },
            openWorkflowAgent: { openWorkflowAgent($0) },
            openPlan: { planOpen = true },
            openFold: openFold,
            jumpToCall: { jumpToCall($0) },
            openQuote: { jumpToQuoted($0) },
            openAppLink: { openHubPage($0) },
            openTurnBranchReview: {
                #if os(iOS)
                if TurnReviewRouting.route(targetFile: nil, canReview: canReviewTurnChanges, cwd: session?.cwd) == .review(targetFile: nil) {
                    actions.pendingReviewFallback = nil
                    reviewRequest = ReviewRequest(scope: .branch)
                }
                #endif
            },
            openTurnChangedFile: { [key, session] changed in
                switch TurnReviewRouting.route(targetFile: changed.path, canReview: canReviewTurnChanges, cwd: session?.cwd) {
                case .review(let target):
                    #if os(iOS)
                    actions.pendingReviewFallback = nil
                    reviewRequest = ReviewRequest(scope: .branch, targetFile: target)
                    #else
                    file = FileRef(group: key.group, machineID: key.machineID, path: target ?? changed.path, cwd: session?.cwd)
                    #endif
                case .file(let path):
                    file = FileRef(group: key.group, machineID: key.machineID, path: path, cwd: session?.cwd)
                case .none:
                    break
                }
            })
    }

    /// Folds or opens a work line without moving what you were reading. In the upside-down list the newest end holds
    /// still on its own: folding from Collapse under the last step leaves the reply below it where it was. From the header,
    /// the header stays under the finger instead (what is above it holds still; the reply comes up to meet it).
    /// Scrolls to a vertical offset, keeping the horizontal one (see `ChatScratch.offsetX`).
    private func scrollList(to y: CGFloat) {
        position.scrollTo(point: CGPoint(x: scratch.offsetX, y: y))
    }

    #if DEBUG
    /// `-showAutoScroll`'s jumps to `y` pt from the newest end: on the list as the chat jumps (`ChatJumpRun`), or with
    /// `-showSwiftUIJumps YES` as SwiftUI's animated scroll (eased over `seconds`, else the chat's standard motion).
    private func debugJump(to y: CGFloat, seconds: Double?) {
        if !Self.swiftUIJumps, let list = pin.list {
            position = ScrollPosition(idType: String.self)
            jumpRun.start(list, to: .offset(ChatJumpRun.newest(list) + y)) { _ in }
            return
        }
        withAnimation(seconds.map { .easeOut(duration: $0) } ?? Theme.Motion.standard) { scrollList(to: y) }
    }
    #endif

    /// Folds or opens a work line. From its header, the header stays under the finger (`ChatPin`), through the steps
    /// arriving from the hub after the tap; from Collapse under the last step, the flipped list keeps the reply below where
    /// it was on its own. No animation: hundreds of rows animating out left blank frames behind. Folding is at once too:
    /// the steps used to fade away first, their space held, and for a fifth of a second the screen showed a fading copy
    /// of them over a blank (#3005).
    private func toggleWork(_ id: String, fromHeader: Bool, run: Bool = false) {
        Haptics.tap()
        let wasOpen = run ? isRunOpen(id) : isOpen(id)
        #if DEBUG
        UnfoldTrace.shared.begin("\(run ? "run" : "work") \(wasOpen ? "fold" : "open")", height: { [pin] in pin.contentHeight },
                                 drawn: { Self.drawnTop(id) }) { [pin] in
            pin.screenY(id)
        }
        #endif
        // Its steps fade in as they appear, unless the list is at its newest end: the steps come in there first and the
        // pin brings the header back a frame later, and for that frame the screen held nothing but steps still fading in:
        // blank (#754). They show at once instead.
        if !run { revealUntil[id] = wasOpen || scratch.atNewest ? nil : .now.addingTimeInterval(Self.stepsArrive) }
        changeWork(id, fromHeader: fromHeader, run: run, wasOpen: wasOpen)
    }

    /// How long after opening a work line its steps may still arrive (and reveal themselves): the pin's hold.
    private static let stepsArrive: TimeInterval = 1.5

    /// Opens or closes a run of chips like a work line: its calls fade in under it, or go at once. They are already
    /// loaded, so only the ones appearing in this pass fade in (`revealUntil` of the work line, briefly).
    private func toggleRun(_ id: String) {
        let line = id.components(separatedBy: "/run-")[0] // a run's id is "<work line>/run-<first call>"
        let wasOpen = isRunOpen(id)
        #if DEBUG
        UnfoldTrace.shared.begin("run \(wasOpen ? "fold" : "open")", height: { [pin] in pin.contentHeight },
                                 drawn: { Self.drawnTop(id) }) { [pin] in pin.screenY(id) }
        #endif
        Haptics.tap()
        if !wasOpen { revealUntil[line] = max(revealUntil[line] ?? .distantPast, .now.addingTimeInterval(Theme.Motion.revealTime)) }
        changeWork(id, fromHeader: true, run: true, wasOpen: wasOpen)
    }

    private func changeWork(_ id: String, fromHeader: Bool, run: Bool, wasOpen: Bool) {
        // A work line or a run of chips is a row like any other (`ChatFollow.toggledRow`, #3757). In the middle it stays
        // where it is, folded too, until the user takes the list, taps ↓ or sends: handed to the usual hold, following
        // came back 1.5 s later and took the list to the newest end (the tapped chips up by what opened under them,
        // BeautifulUI, 119–381 pt, #1123; a work line folded 61 pt short of it, #3005). Opened at the newest end it rises
        // to the top bar with what opens and what comes in after (#1173), the newest end on screen, and stays there while
        // a running turn's steps keep coming in under it (following on would scroll it away, holding back would freeze
        // the turn, #1046). Closed there while following, the list follows on. A fold the list could not keep the header
        // for glides to the newest end and follows from there (`ChatPin.landed`).
        let following = follow.follows
        var anchored = false, rise: CGFloat = 0
        if fromHeader {
            var next = follow
            let how = next.toggledRow(closing: wasOpen, atNewest: scratch.atNewest)
            anchored = how != .moment
            rise = how == .rising ? workRise(id) : 0
            pin.hold(id, from: id, slow: true, rise: rise, keep: anchored)
        }
        if wasOpen { scratch.foldedAt = .now }
        // Not taking the list: while the header settles nothing pulls it away (the pin has it) and nothing is held
        // back (the running turn opened at the newest keeps growing: it froze, #1046); then the chat goes on as before.
        holdFollow(anchored: anchored)
        if anchored, rise > 0, following, !run, !wasOpen { keepHeader(id, rise: rise) }
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) {
            if run {
                if openRuns.contains(id) { openRuns.remove(id) } else { openRuns.insert(id) }
            } else if openWork.contains(id) { openWork.remove(id) } else { openWork.insert(id) }
        }
        #if PERF_SIGNPOST
        PerfTrace.event("Work toggle", "open=\(!wasOpen) held=\(fromHeader)")
        #endif
    }

    /// How far up a work line opened at the newest end may go (`ChatPin.hold`'s `rise`): to the top of the list, under
    /// the header. Held where it was, its steps came in under the composer, out of sight: the first tap seemed to do
    /// nothing, and what arrived meanwhile was held back until the next tap folded it, when it all came in at once.
    /// Elsewhere it stays where it was tapped.
    private func workRise(_ id: String) -> CGFloat {
        guard scratch.atNewest, let y = scratch.workHeadY[id], scratch.listFrame.height > 0 else { return 0 }
        return y - riseTop
    }

    /// How high on screen a row opened at the newest end goes (window y): just under the top bar (#1173, #3757).
    private var riseTop: CGFloat { scratch.listFrame.minY + headerRoom + Theme.Space.l }

    #if DEBUG
    /// Where the work line header `id` is drawn (`DrawnProbe`); the Mac has no probe.
    private static func drawnTop(_ id: String) -> CGFloat? {
        #if os(iOS)
        return DrawnProbe.top(id)
        #else
        return nil
        #endif
    }
    #endif

    /// A step of the work line `group` as it opens: steps appearing just after it opened fade in.
    private func stepUnfolding(_ group: String) -> Unfolding {
        Unfolding(revealing: (revealUntil[group] ?? .distantPast) > .now)
    }

    /// A work line's header moved (its top on screen): kept for the fold probe, and told to the pin. The wake
    /// drawer draws its own lines ("dw-…") with this too; the chat's list has none of those, so the pin never moves it.
    private func workHeadMoved(_ id: String, _ spot: ChatPin.Spot) {
        scratch.workHeadY[id] = spot.y
        pin.moved(id, to: spot)
    }

    /// Where a work line's header is, by its top: the open header is taller than the folded one, and held by its
    /// middle it pushed what is above it 10 pt up. The content is upside down: the top on screen is its far edge there.
    nonisolated fileprivate static func headSpot(_ g: GeometryProxy) -> ChatPin.Spot {
        ChatPin.Spot(y: g.frame(in: .global).minY, content: g.frame(in: .named(ChatPin.space)).maxY)
    }

    /// Every Persona conversation can keep its own model pick; its remedy and composer open this same sheet.
    private var assistantModelPickAvailable: Bool {
        AssistantModelChoice.pickerAvailable(assistant: style == .assistant, keepsPick: model.keepsConversationModelPick(key.group))
    }
    private var openAssistantModel: (() -> Void)? {
        guard assistantModelPickAvailable else { return nil }
        return { showAssistantModel = true }
    }

    /// Every Persona conversation's model-route remedy opens its own picker. The new turn-error code gates this remedy;
    /// the older saved-pick feature gates only the way back, not the choice of a working model.
    private var openConversationModel: (() -> Void)? {
        guard style == .assistant else { return nil }
        return { showAssistantModel = true }
    }

    private var conversationDefaultRule: AssistantModelChoice.DefaultRule {
        AssistantModelChoice.defaultRule(isMain: key == model.assistantMain,
                                         supervisor: model.isBackgroundSupervisor(key.sessionID),
                                         leader: model.assistantLeaders.contains(key.sessionID), origin: session?.origin)
    }

    /// Persona's last turn ended on a model set-up error the hub named (`turn_error_code`, docs/assistant/onboarding.md 4.4):
    /// the fix to show instead of Claude Code's 「Not logged in」 as an answer. Persona's conversations only, as the web.
    private func turnFix(raw: String? = nil, status: Int? = nil) -> TurnErrorFix? {
        guard style == .assistant else { return nil }
        if let fix = TurnErrorFix(code: session?.turnErrorCode, launcher: session?.launcher) { return fix }
        // A hub that did not code the turn still gets the web's narrow tool-search fallback; any coded kind wins.
        guard session?.turnErrorCode == nil, let raw else { return nil }
        return TurnErrorFix.toolSearch(raw: raw, status: status)
    }

    /// The Model Not Connected card for the line, when it is the turn's error: the last line, an answer or an API error.
    private func turnErrorCard(_ line: ChatLine, raw: String?, status: Int? = nil) -> ReadinessCard? {
        guard let fix = turnFix(raw: raw, status: status), ChatLines.lastOfChat(currentLines())?.id == line.id else { return nil }
        let retry = fix.action == "retry" && !self.status.isActive && session?.canSend == true
        let canDrop = fix.action == "pick_model" && model.keepsConversationModelPick(key.group) && session?.modelPick != nil
        let followsDefault = AssistantModelChoice.followsDefault(isMain: key == model.assistantMain,
                                                                  supervisor: model.isBackgroundSupervisor(key.sessionID),
                                                                  leader: model.assistantLeaders.contains(key.sessionID), origin: session?.origin)
        return ReadinessCard(title: fix.title, detail: fix.detail, tone: .danger,
                             actionTitle: fix.action == "retry" ? (retry ? String(localized: "Retry") : nil) : String(localized: "Fix it"),
                             action: fix.action == "retry" && !retry ? nil : {
                                 switch fix.action {
                                 case "retry":
                                     guard let current = model.latest(key), !current.status.isActive, current.canSend else { return }
                                     model.sendNow(key, text: "继续") // i18n-ignore
                                 case "edit_provider": readinessRoute = .provider(session?.launcher)
                                 case "pick_model": openConversationModel?()
                                 case "connect_model":
                                     let launcher = session?.launcher?.trimmingCharacters(in: .whitespacesAndNewlines)
                                     if let launcher, !launcher.isEmpty {
                                         readinessRoute = .provider(launcher)
                                     } else {
                                         readinessRoute = .providerWizard
                                     }
                                 default: readinessRoute = .providerWizard
                                 }
                             },
                             altTitle: canDrop ? String(localized: followsDefault ? "Follow default" : "Undo pick") : nil,
                             alt: canDrop ? { Task { await model.dropConversationModel(key) } } : nil,
                             details: [raw].compactMap { $0 })
    }

    @ViewBuilder private func lineView(_ line: ChatLine) -> some View {
        switch line.content {
        case .supervision(let id, _, let live, let source):
            SupervisionRecordLine(live: live, open: openSupervision.contains(id) != Self.debugOpenSupervision, source: source) {
                Haptics.tap()
                toggleInPlace(pin, id, animation: Theme.Motion.state, closing: openSupervision.contains(id) != Self.debugOpenSupervision) {
                    if openSupervision.contains(id) { openSupervision.remove(id) } else { openSupervision.insert(id) }
                }
            }
            .chatPinned(id)
        case .supervisionInput(let item):
            SupervisionInputLine(item: item)
        case .row(let r):
            if r.errorCandidate != nil, let card = turnErrorCard(line, raw: r.errorCandidate?.text) { card } else { rowView(r, isLatest: false, live: true, showsSource: line.showsSource, hidesPrompt: line.hidesPrompt) }
        case .answerPart(let item, let part, let index, let count):
            VStack(alignment: .leading, spacing: 0) {
                AssistantTextPart(item: item, part: part, index: index, count: count).equatable()
                    .chatMark(index == 0 ? "chat.answer." + item.id : "chat.answer.\(item.id).\(index)")
                if index == count - 1 { ReplyUsageFooter(item: item) }
            }
        case .feed(let item): FeedLine(item: item, group: shownKey.group)
        case .quick(let result): QuickResultCard(result: result)
        case .placeMap(let result): PlaceMapCard(result: result)
        case .day(let date): DayLine(date: date)
        case .modeChange(_, let change): ModeChangeLine(change: change, session: session)
        case .modelChange(_, let from, let to):
            if let names = line.modelNames { ModelChangeDivider(names: names, rawFrom: from, rawTo: to) }
        case .apiError(let item, let info, let count):
            if let card = turnErrorCard(line, raw: info.raw, status: info.status) {
                card
            } else {
            // Retry only when the error is the last thing, the agent is not working (someone may have retried already) and
            // trying again can help (not a login or a bad request). An exhausted model recovery chain that can send
            // qualifies too (#7824): Retry runs the request again, on the configured model once it is back, even after a
            // refusal the error text alone calls final.
            // Not when the fallback model is missing (#4489): Continue would go to the same model.
            // The exhausted chain's words go on the last error only; an earlier error in the chat is not what they explain.
            APIErrorCard(info: info, count: count, at: item.ts,
                         recovery: ModelFallbackText.exhaustedRecovery(session).flatMap { r in ChatLines.lastOfChat(currentLines())?.id == line.id ? r : nil },
                         retry: ModelFallbackText.offersRetry(transient: info.transient, session: session)
                             && !status.isActive && ChatLines.lastOfChat(currentLines())?.id == line.id
                             && model.session(key)?.turnErrorCode != ModelFallbackText.unavailableCode
                             ? { model.sendNow(key, text: "继续") } : nil) // i18n-ignore
            }
        case .userImages(_, let items):
            // Close to the bubble below it, like the photos on a message being sent (the list's spacing is 14).
            SentPhotos(images: items.compactMap(\.image)).padding(.bottom, -8)
        case .work(let id, let items, let live, let start, let end):
            WorkLine(items: items, live: live, start: start, end: end, open: isOpen(id, live: live), agents: live ? agentsWorking : nil,
                     accepting: live && items.isEmpty && sendPhase == .processing && !status.isActive,
                     openTerminal: live && style == .dev && session?.canTerminal == true ? { openTerminal() } : nil) {
                toggleWork(id, fromHeader: true)
            }
            .equatable()
            .modifier(WorkHeadReport(id: id, moved: workHeadMoved))
        case .workStep(let group, let item, let first):
            WorkStepView(item: item, first: first).equatable()
                .unfolding(stepUnfolding(group))
        case .agentOut(_, let call):
            // A card of its own (no rail on the timeline: the line above is shut).
            Group {
                if WorkflowCallCard.isWorkflow(call) { WorkflowCallCard(call: call) } else { SubagentCard(call: call) }
            }
            .background { if !WorkCardSlice.timeline { WorkCardSlice(top: true, bottom: true) } }
        case .agents(let group, let calls, let first, let alone):
            SubagentGrid(calls: calls, first: first, alone: alone)
                .unfolding(alone ? Unfolding() : stepUnfolding(group))
        case .toolRun(let id, let calls, let open, let first):
            ToolRunRow(calls: calls, open: open, first: first) { toggleRun(id) }
                .equatable()
                .unfolding(stepUnfolding(id.components(separatedBy: "/run-")[0])) // a run's id is "<work line>/run-<first call>"
                .modifier(WorkHeadReport(id: id, moved: workHeadMoved))
        case .workEnd(let group, let count, let files):
            if AppStyle.current.usesBeautifulUI {
                // The foot of the open work line's card: the files it changed, then Collapse.
                WorkEndView(count: count, files: files, pinKey: "work-end-" + group) { toggleWork(group, fromHeader: false) }
                    .equatable()
                    .unfolding(stepUnfolding(group))
            } else {
                // The foot of the open work line's card.
                Button { toggleWork(group, fromHeader: false) } label: {
                    HStack(spacing: Theme.Space.sm) {
                        ThemedIcon(Theme.Icon.collapse).font(Theme.Font.labelStrong)
                        Text("Collapse \(count) steps [Chat View 608bfc]").lineLimit(1).fixedSize()
                    }
                    .font(Theme.Font.caption)
                    .foregroundStyle(.secondary)
                    .padding(.leading, WorkCardSlice.contentLeading(typeSize))
                    .padding(.trailing, WorkCardSlice.inset)
                    .padding(.vertical, Theme.Space.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background { WorkCardSlice(top: false, bottom: true) }
                    .overlay(alignment: .top) { WorkCardSlice.separator }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Collapse steps")
                .accessibilityIdentifier("work-fold-bottom")
                .unfolding(stepUnfolding(group))
            }
        case .recaps(_, let items):
            RecapCard(items: items).chatPinned("recap-" + line.id) // where the open recap folds to (`dismissRecap`)
        case .viewed(_, let images):
            ViewedImagesRow(images: images).equatable()
        case .turnChanges(let id, let changes):
            TurnChangesCard(pinKey: id, changes: changes, canReview: canReviewTurnChanges).equatable()
        case .toolViews(_, let views):
            ToolViewAttachmentsRow(views: views, link: $link).equatable()
        }
    }

    static let bottomID = "chat-bottom"

    /// Scrolls to the sentinel after the last row by id: SwiftUI lays that row out and positions around it, unlike
    /// `scrollTo(edge: .bottom)`, which trusts the lazy stack's estimated height and can land in unrendered space.
    /// Lands on the entry target once its line exists (the cache or the first page may not hold it yet).
    private func landEntry(_ lines: [ChatLine]) {
        guard let target = entry, !lines.isEmpty else { return }
        var id: String?
        switch target {
        case .latestAnswer:
            // The line right after the last input: the start of what the agent did and said for it.
            if let i = lines.lastIndex(where: \.isInput), i + 1 < lines.count { id = lines[i + 1].id }
        case .message(let item):
            // A hub link's or a search result's message (#6284): brought to the middle and lit up as a quote's is, a
            // little longer, as the chat has just opened (`landingHold`); further up than what is loaded, older pages
            // the same way as for a saved place; not found, the chat stays at the newest.
            if jumpToQuoted(ContextRef(kind: "quote", title: "", url: "", text: "", msg: item), hold: Theme.Quote.landingHold) {
                entry = nil
            } else if store.backlogBuilt {
                if store.hasMore, scratch.entryPages < Self.entryPageLimit { searchOlderForEntry() } else { entry = nil }
            }
            return
        case .card(let card):
            // A task's card opened from the assistant's pager (#12625): the message it is about, then as a link's message.
            // Not in what is loaded: older pages as for a link; none at all, the chat stays at its newest.
            guard store.backlogBuilt else { return }
            if let item = CardSource.source(for: card, in: store.items) {
                entry = .message(item)
                landEntry(lines)
            } else if !(card.toolUseID ?? "").isEmpty, store.hasMore, scratch.entryPages < Self.entryPageLimit {
                searchOlderForEntry() // a card without an id takes the newest question loaded, never an older page's
            } else {
                entry = nil
            }
            return
        #if os(iOS)
        case .starred(let jump):
            guard checkedStar == jump.id, shownKey == jump.key, let source = jump.starred else { return }
            if let item = store.items.first(where: { $0.id == source.item }) {
                guard source.matches(item) else {
                    entry = nil
                    model.toast = Toast(String(localized: "The original message is unavailable."))
                    return
                }
                if jumpToMessage(source.item, hold: Theme.Quote.landingHold, stillMatches: {
                    shownKey == jump.key && store.items.contains(where: source.matches)
                }) { entry = nil; return }
            }
            if store.backlogBuilt {
                if store.hasMore, scratch.entryPages < Self.entryPageLimit {
                    searchOlderForEntry()
                } else {
                    entry = nil
                    model.toast = Toast(String(localized: "Couldn't find the original message in the loaded chat."))
                }
            }
            return
        #endif
        case .line(let saved):
            if lines.contains(where: { $0.id == saved }) {
                id = saved
            } else if store.backlogBuilt {
                // Further up than what is loaded: a cold start has the last two turns (the cache, the hub's first
                // page). Older pages until it shows, a few at most (#597); judged only on built lines, the page merges
                // before they are. Not found: stay at the newest. Left waiting, it used to yank the list there (and
                // stop following) whenever paging up later happened to load that line.
                if store.hasMore, scratch.entryPages < Self.entryPageLimit {
                    searchOlderForEntry()
                } else {
                    entry = nil
                }
                return
            }
        }
        guard let id else { return }
        entry = nil
        setFollow { $0.land() }
        if target == .latestAnswer, let i = lines.lastIndex(where: \.isInput) {
            landUnderInput(lines[i].id)
            return
        }
        Task { @MainActor in
            await Task.yield()
            // Flipped: anchoring at the scroll view's bottom is the top of the screen — the line starts the page.
            position.scrollTo(id: id, anchor: .bottomLeading)
            try? await Task.sleep(for: .milliseconds(300))
            position.scrollTo(id: id, anchor: .bottomLeading)
            try? await Task.sleep(for: .milliseconds(100))
            landed()
            await mendBlankLanding(on: id)
        }
    }

    /// The saved place's search (`landEntry`): turns per older page, and pages at most.
    static let entryPageTurns = 10
    static let entryPageLimit = 10

    /// One older page for the saved place, then the look again once its lines are built. A page that fails or brings
    /// nothing ends the search (stay at the newest), as does leaving for another chat; each wait gives up after 10 s.
    private func searchOlderForEntry() {
        guard !scratch.entrySearching else { return }
        scratch.entrySearching = true
        scratch.entryPages += 1
        let store = store, key = shownKey
        Task { @MainActor in
            defer { scratch.entrySearching = false }
            let deadline = ContinuousClock.now + .seconds(10)
            while store.isLoadingOlder, ContinuousClock.now < deadline { try? await Task.sleep(for: .milliseconds(50)) }
            let before = store.items.count
            let loaded = await model.loadOlder(key, turns: Self.entryPageTurns)
            guard shownKey == key, entry != nil else { return } // another chat, or the user took the list
            guard loaded, store.items.count > before else { entry = nil; return }
            while store.isRebuilding, ContinuousClock.now < deadline + .seconds(10) { try? await Task.sleep(for: .milliseconds(20)) }
            scratch.entrySearching = false
            landEntry(currentLines())
        }
    }

    /// A landing is over: near the newest end it follows (a line in the last screenful lands there: what arrived after
    /// it was held back with no ↓, TestFlight 50/51), further up it reads (#597), by the geometry.
    private func landed() {
        setFollow { $0.landed() }
        mendMisplacedStartSoon()
    }

    /// The end sentinel (`bottomID`), the lazy stack's first row, was laid out `y` from the stack's top: 0 when the stack
    /// placed its rows from its start.
    private func startPlaced(at y: CGFloat) {
        guard abs(scratch.startY - y) > 0.5 else { return }
        scratch.startY = y
        mendMisplacedStartSoon()
    }

    /// The newest line was laid out `y` from the stack's top: past the sentinel by the rows' spacing at least.
    private func newestPlaced(_ id: String, at y: CGFloat) {
        if let last = scratch.newestLineY, last.id == id, abs(last.y - y) <= 0.5 { return }
        scratch.newestLineY = (id, y)
        mendMisplacedStartSoon()
    }

    /// The stack placed its newest end by estimate (#7382): the sentinel off the stack's top, the newest line over it, or,
    /// with nothing drawn between them, the newest line further than the rows' spacing past it.
    private var startMisplaced: Bool {
        if abs(scratch.startY) > Self.startSlack { return true }
        guard let newest = scratch.newestLineY, newest.id == scratch.lines.last?.id else { return false } // the lines last built
        let after = newest.y - scratch.startY - Theme.Space.l
        return after < -Self.startSlack || (after > Self.startSlack && newestAlone)
    }

    /// Nothing is drawn between the end sentinel and the newest line (`chatRows`): no streamed words, typing line,
    /// unsent message, error or call captions. Read loosely: anything that might be there counts.
    private var newestAlone: Bool {
        shownPending.isEmpty && sendPhase == nil && store.error == nil && (frozen?.text ?? store.liveText).isEmpty
            && !(viewing == nil && (model.voice?.isBound(to: key) ?? false))
    }

    /// How far off the newest end may be placed before the list is built again (rounding, a pixel).
    static let startSlack: CGFloat = 1

    /// `mendMisplacedStart` a moment later, if the stack still has it so then: rows coming in or going out at the newest
    /// end are placed over a few frames.
    private func mendMisplacedStartSoon() {
        guard startMisplaced else { return }
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(300))
            mendMisplacedStart()
        }
    }

    /// The lazy stack placed its newest rows away from where its content starts (#7382, #1942). After a landing far from
    /// the newest end (a saved place, a search hit, a quote), SwiftUI places what is between there and the newest end by
    /// estimated heights, and a finger that then scrolls back to the newest end gets those rows where the estimates put
    /// them, never corrected: rows taller than estimated (a long Markdown answer) put the newest line over the sentinel,
    /// before the scroll view's end, so the answer's last lines stayed under the capsules and the composer and a drag
    /// there sprang back (590 pt of it, measured); shorter ones left thousands of points of blank room before it, and
    /// rows out of order. ↓ was gone (the offset said the list was at its end), and only building the list again places
    /// the rows from its start. Done once the newest line is on screen (the list was taken to the newest end, so it goes
    /// there) and nothing moves the list: not under a finger, in a glide, a jump, a landing or a held row; otherwise the
    /// next time the list comes to rest. Logged (category `chat-landing`) with numbers only.
    private func mendMisplacedStart() {
        guard startMisplaced, !scratch.userDragging, entry == nil, landing == nil, !pin.holding,
              let newest = currentLines().last, scratch.drawnLines.contains(newest.id) else { return }
        if let list = pin.list, list.isTracking || list.isDecelerating || jumpRun.running { return }
        // A list that keeps reporting a misplaced start is left alone after one try a second: never a loop of builds.
        guard Date.now.timeIntervalSince(scratch.startMendedAt) > 1 else { return }
        scratch.startMendedAt = .now
        let start = Int(scratch.startY), placed = Int(scratch.newestLineY?.y ?? 0)
        scratch.startY = 0
        scratch.newestLineY = nil
        #if DEBUG
        scratch.startMends += 1
        #endif
        Self.landingLog.notice("misplaced newest end: sentinel at \(start, privacy: .public), newest line at \(placed, privacy: .public) pt; built again")
        listBuild += 1
        setFollow { $0.follow() }
        position.scrollTo(edge: .top)
    }

    /// Opened again at the place it was left, a chat on a phone showed only its header, ↓ and the composer, no line
    /// drawn anywhere, until it was closed (#1386; never on a simulator). A landing that leaves no line in the viewport
    /// builds the list again and lands once more; still nothing, it goes to the newest. Each step writes one line to the
    /// system log (category `chat-landing`) with counts and geometry only, never ids or text.
    private func mendBlankLanding(on id: String) async {
        let key = shownKey
        var pretend = false
        #if DEBUG
        pretend = Self.blankLanding
        if pretend, let list = pin.list { list.contentOffset.y = list.contentSize.height + 2 * list.bounds.height }
        #endif
        for attempt in 1...2 {
            try? await Task.sleep(for: .milliseconds(150))
            let lines = currentLines()
            let blank = scratch.drawnLines.isEmpty || pretend
            pretend = false
            guard shownKey == key, !lines.isEmpty, blank, !scratch.userDragging, entry == nil else {
                if attempt > 1 { logLanding("mended", attempt: attempt - 1, on: id, lines: lines) }
                return
            }
            logLanding("blank", attempt: attempt, on: id, lines: lines)
            #if DEBUG
            scratch.blankLandings += 1
            #endif
            listBuild += 1
            try? await Task.sleep(for: .milliseconds(50))
            if attempt == 1, lines.contains(where: { $0.id == id }) {
                setFollow { $0.land() }
                position.scrollTo(id: id, anchor: .bottomLeading)
                try? await Task.sleep(for: .milliseconds(300))
                position.scrollTo(id: id, anchor: .bottomLeading)
                landed()
            } else {
                setFollow { $0.follow() }
                position.scrollTo(edge: .top)
            }
        }
        try? await Task.sleep(for: .milliseconds(150))
        guard shownKey == key else { return }
        logLanding(scratch.drawnLines.isEmpty ? "still blank" : "mended", attempt: 2, on: id, lines: currentLines())
    }

    private static let landingLog = Logger(subsystem: "persona", category: "chat-landing")

    /// One line about a landing: where the target is among the lines, what is drawn, where the list is.
    private func logLanding(_ what: String, attempt: Int, on id: String, lines: [ChatLine]) {
        let at = lines.firstIndex(where: { $0.id == id }) ?? -1
        let list = pin.list
        let offset = list.map { Int($0.contentOffset.y) } ?? -1
        let content = list.map { Int($0.contentSize.height) } ?? -1
        let height = list.map { Int($0.bounds.height) } ?? -1
        Self.landingLog.notice("""
            \(what, privacy: .public) after landing (try \(attempt, privacy: .public)): line \(at, privacy: .public) of \(lines.count, privacy: .public), \
            drawn \(scratch.drawnLines.count, privacy: .public), visible \(scratch.visibleLines.count, privacy: .public), \
            offset \(offset, privacy: .public), content \(content, privacy: .public), height \(height, privacy: .public), \
            mode \(String(describing: follow.mode), privacy: .public), build \(listBuild, privacy: .public)
            """)
    }

    /// New content arrived: following goes to the newest, unless a tapped header is held in place (`ChatPin`). Nothing
    /// here turns following on; that is `ChatFollow`'s (it used to, at the newest end untouched, to make up for the
    /// sentinel and following disagreeing: #754).
    private func contentArrived(foldChrome: Bool) {
        // A finger on the list: not even at the newest end. The scroll phase says `tracking` only once the finger has
        // moved; touched down near the newest, the chat jumped there under it (`ScrollUnderPushTests`, 263 pt).
        if pin.list?.isTracking == true { return }
        guard follow.follows, !pin.holding else { return }
        // ↓ is on its way to the newest end: it gets there whatever arrives meanwhile (the flipped list's newest end is
        // offset 0 however much comes in), and a jump of this one's would cut its glide short. It looks again once there.
        if jumpRun.running { return }
        // More than a screen away while following: a work line opened at the newest end of a running turn, its steps
        // between the header and the newest. Its next step glides there rather than jumping a screen or more.
        scrollToEnd(animated: scratch.listFrame.height > 0 && scratch.newestGap > scratch.listFrame.height)
        if foldChrome, userScrolled { hideChrome() }
    }

    /// How much of your last message stays on screen above the answer: about its last line (and the bubble's edge).
    static let inputTail: CGFloat = 34

    /// A finished turn you have not seen: your message's last line at the top of the page, the agent's answer under it.
    /// Jumps to the message, then moves by what is measured (rows not drawn yet only have estimated heights). The
    /// header folds to its slim bar at once (you came to read; it would fold a moment later anyway and uncover more of
    /// your message), so the page is laid out against the slim bar.
    private func landUnderInput(_ input: String) {
        landing = input
        scratch.landingMaxY = nil
        Task { @MainActor in
            await Task.yield()
            position.scrollTo(id: input, anchor: .bottomLeading)
            for n in 0..<8 {
                try? await Task.sleep(for: .milliseconds(60))
                if n == 1 { hideChrome() } // once the header has measured itself
                guard let bottom = scratch.landingMaxY, scratch.listFrame.height > 0 else { continue }
                let want = scratch.listFrame.minY + (style == .dev ? (sideBars ? sideHeaderHeight : chrome.settledHeight) : AssistantTopBar.room(railEdge) + topExtra) + Self.inputTail
                let off = want - bottom // > 0: too high; the flipped list scrolls toward older to lower it
                if abs(off) < 1 { break }
                scrollList(to: chrome.offset + off)
            }
            landing = nil
            landed()
            await mendBlankLanding(on: input)
        }
    }

    /// Remembers the top line on screen when leaving (nothing when at the newest: open at the newest again).
    private func rememberReadPosition() {
        guard viewing == nil else { return }
        // Still on the way to the saved place (an older page loading): what is on screen is not where you read.
        if case .line = entry { return }
        if follow.near || scratch.visibleLines.isEmpty {
            UserDefaults.standard.removeObject(forKey: readKey)
            return
        }
        // Persona's conversation: a message, not the hub's lines (a task card, a day), which may come in after the
        // transcript on the next launch and not be there yet to land on.
        let order = currentLines().filter { line in
            guard style == .assistant else { return true }
            switch line.content {
            case .feed, .day, .quick: return false
            default: return true
            }
        }.map(\.id)
        // Lines only: a failed message's bubble among them (#6023) is no place to come back to.
        guard let top = scratch.visibleLines.filter({ order.contains($0) })
            .min(by: { (order.firstIndex(of: $0) ?? .max) < (order.firstIndex(of: $1) ?? .max) }) else { return }
        UserDefaults.standard.set(top, forKey: readKey)
    }

    private func prefetchOlder() {
        let store = store
        guard userScrolled, store.hasMore, !store.isLoadingOlder else { return }
        Task { await model.loadOlder(shownKey) }
    }

    /// The newest message is at offset 0 of the flipped list: an exact jump, no estimate involved.
    private func scrollToEnd(animated: Bool = false) {
        #if DEBUG
        scratch.endJumps += 1
        #endif
        // Already there, and the stored position says so: setting it again only re-evaluated the whole page (a
        // streaming reply asks every 40 characters; the flipped list stays at its newest end by itself).
        if !animated, scratch.atNewest, position.edge == .top { return }
        if animated {
            // On the list itself, as ↓ (`ChatJumpRun`): SwiftUI's animated scroll drew every text it passed again on
            // every frame (#5076). Its stored position is forgotten first, as ↓ does (#4788).
            if let list = pin.list, !Self.swiftUIJumps {
                if pin.holding { pin.release() }
                position = ScrollPosition(idType: String.self)
                jumpRun.start(list) { arrived in landedAtNewest(list, arrived: arrived, again: true) }
                return
            }
            withAnimation(Theme.Motion.standard, completionCriteria: .removed) {
                position.scrollTo(edge: .top)
            } completion: {
                reconcileNewestGeometry()
            }
        } else {
            position.scrollTo(edge: .top)
        }
    }

    /// Revealing held-back rows can move UIKit to the end without another SwiftUI geometry callback (#1211).
    /// Read the real final position, including an interrupted jump, rather than forcing following or near=true.
    private func reconcileNewestGeometry() {
        guard model.isSubscribed(shownKey, by: owner), let gap = pin.measuredNewestGap else { return }
        scratch.newestGap = gap
        scratch.atNewest = gap <= 0.5
        setFollow { $0.scrolled(gap: gap) }
    }

    /// On entry / returning to the chat tab: at the newest (already the natural start of the flipped list).
    private func settleAtEnd() {
        Task { @MainActor in
            await Task.yield()
            if entry == nil && follow.follows { scrollToEnd() }
            try? await Task.sleep(for: .milliseconds(400))
            settled = true
        }
    }

    /// ↓: to the newest end, following, from anywhere and at any moment, a glide included (#3027). On the phone the jump
    /// runs on the list itself (`ChatJumpRun`): it stops the glide where it is, comes from a screen away at most and
    /// says when it is there; then what came in meanwhile is caught up with, and the geometry read from the list.
    private func jumpToEnd() {
        #if DEBUG && (os(iOS) || os(visionOS))
        if pin.list?.isDecelerating == true { scratch.glideTaps += 1 }
        #endif
        Haptics.tap()
        setFollow { $0.follow() }
        if let list = pin.list {
            if pin.holding { pin.release() } // a tapped header held in place would put the list back each frame
            // SwiftUI's stored position still says where the user's own scrolling left the list. The jump moves the
            // scroll view, not that position: on the next change of content (the rows held back while reading, let in
            // by this tap; a streamed line; a row laid out taller than guessed) SwiftUI put the list back there, during
            // the jump or after it, and as it was following by then, ↓ was gone and nothing brought it back (#4788).
            // Forgotten here, as `ChatPin` forgets it before it moves the list; the newest end is stored once there.
            position = ScrollPosition(idType: String.self)
            #if DEBUG
            scratch.endJumps += 1
            let tapped = CACurrentMediaTime()
            #endif
            jumpRun.start(list) { arrived in
                #if DEBUG
                scratch.jumpMs = Int((CACurrentMediaTime() - tapped) * 1000)
                #endif
                landedAtNewest(list, arrived: arrived, again: true)
            }
            return
        }
        scrollToEnd(animated: true)
    }

    private static let jumpLog = Logger(subsystem: "persona", category: "chat-jump")

    /// ↓'s jump is over: the geometry is read from the list (#1211), and following, SwiftUI's position is the newest end
    /// again, as every other way there leaves it, so what streams in next keeps the list there (it is there already:
    /// nothing moves). A finger that took the list meanwhile has decided already. `again`: a moment later the list is
    /// looked at once more, and if anything took it away from the newest end while it follows, it goes there again.
    private func landedAtNewest(_ list: UIScrollView, arrived: Bool, again: Bool) {
        reconcileNewestGeometry()
        if follow.follows { scrollToEnd() }
        guard arrived, again else { return }
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(300))
            guard pin.list === list, follow.follows, !jumpRun.running, !list.isTracking, !list.isDecelerating,
                  let gap = pin.measuredNewestGap, gap > ChatJump.there else { return }
            Self.jumpLog.notice("A jump to the newest landed, then the list moved \(Int(gap), privacy: .public) pt from it: again")
            #if DEBUG
            scratch.jumpSlips += 1
            #endif
            position = ScrollPosition(idType: String.self)
            jumpRun.start(list) { arrived in landedAtNewest(list, arrived: arrived, again: false) }
        }
    }

    /// ↓ shows: the follow state says so, once the chat has settled on entry.
    private var arrowShows: Bool { follow.arrow && settled && !pinRising }

    /// Changes the follow state; written back only if it changed (the geometry reports every frame of a scroll, and
    /// each write would evaluate the page again).
    private func setFollow(_ change: (inout ChatFollow) -> Void) {
        var next = follow
        change(&next)
        guard next != follow else { return }
        // Out of a kept hold (↓, sending, the user's scrolling, a jump to a saved place): the pin lets go of the header
        // too; into another hold (folded, another line opened), what was tapped settles first.
        if follow.anchored, !next.anchored || next.mode == .landing {
            if next.mode == .holding { pin.unkeep() } else { pin.release() }
        }
        follow = next
    }

    /// An anchored header (`changeWork`) lost: the steps under it laid out all at once and the lazy list then measured
    /// them taller than it had guessed, so the header went off the top and left the list, and the pin had nothing to
    /// put back (BeautifulUI, #1173's test failed on master too). By id the list lays that row out exactly:
    /// it goes to the top, under the bar as far as `rise` let it, and the pin holds it from there.
    private func keepHeader(_ key: String, rise: CGFloat) {
        let target = riseTop
        Task { @MainActor in
            for _ in 0..<3 {
                try? await Task.sleep(for: .milliseconds(300))
                guard follow.anchored, !scratch.userDragging else { return }
                let y = scratch.workHeadY[key] ?? -.infinity
                if y >= target - 1, y < scratch.listFrame.maxY { return }
                pin.release()
                // Flipped: the scroll view's bottom edge is the top of the screen.
                position.scrollTo(id: key, anchor: .bottomLeading)
                try? await Task.sleep(for: .milliseconds(100))
                if let y = scratch.workHeadY[key], scratch.listFrame.height > 0 { scrollList(to: chrome.offset + (target - y)) } // > 0: too high, toward older lowers it
                try? await Task.sleep(for: .milliseconds(100))
                pin.hold(key, slow: true, keep: true)
            }
        }
    }

    /// A header opened or folded: held while it settles (`ChatPin`'s hold), then as before; anchored, until something
    /// ends it (`ChatFollow.hold(anchored:)`).
    private func holdFollow(anchored: Bool = false) {
        setFollow { $0.hold(anchored: anchored) }
        scratch.holds += 1
        let n = scratch.holds
        if anchored { return }
        Task { @MainActor in
            try? await Task.sleep(for: .seconds(ChatPin.holdTime))
            guard scratch.holds == n else { return }
            setFollow { $0.endHold() }
            // Following again short of the newest (folded from an anchored open): back there.
            if follow.follows, !scratch.atNewest { scrollToEnd(animated: true) }
        }
    }

    /// Streamed words are drawn after the lines (`ChatLiveText`'s rule: not once the message is in the rows).
    private var liveShown: Bool { ChatLiveText.shows(store) }
    /// The chat draws live text: the assistant's in the main chat, and an agent's as Claude shows it in any chat of it
    /// (docs/integrations/claude-integration.md "Live replies (MessageDisplay)"), the same way.
    private var drawsLive: Bool { !backgroundActivity && (style == .assistant || store.liveIsAgents) }

    /// Local feedback is per conversation, never a speculative change to the runtime's session state.
    private var sendPhase: ChatSendFeedback.Phase? {
        _ = awaitTick
        guard !backgroundActivity, viewedAgent == nil, let feedback = store.sendFeedback else { return nil }
        // The queue strip already owns queued messages; do not add another progress home for them.
        if store.pending.contains(where: { $0.id == feedback.id }), !shownPending.contains(where: { $0.id == feedback.id }) { return nil }
        return feedback.visible(now: .now)
    }

    /// A message of yours is the newest thing and the agent has not begun on it: no turn running yet, the session at
    /// work or starting up, or the message just sent and the hub not saying working yet (#3853).
    private func startingTurn(_ pending: [PendingMessage]) -> Bool {
        // A failed one is going nowhere (and may sit up in the chat, #6023).
        guard pending.contains(where: { $0.state != .failed }), liveTurnID == nil else { return false }
        return status.isActive || session?.starting == true || awaiting == .sending
    }

    private var liveTurnID: String? {
        guard case .turn(let t) = store.blocks.last else { return nil }
        // The work is for a message of yours still on its way, not for this turn: that message's getting-ready line
        // stands for it until its copy comes in (`startingTurn`).
        if viewedAgent == nil,
           ChatAwaitingTurn.workIsForPending(shownPending, status: status, stateSince: session?.stateSince,
                                             newestLine: store.newestLineAt) { return nil }
        // Your message is in and nothing answered it yet: running already, before the hub's working comes (#3853).
        if let feedback = store.sendFeedback, feedback.belongs(to: t) {
            if sendPhase == .sending { return nil }
            if sendPhase == .processing { return t.id }
            // Finished local attempts must not be revived by transcript-only speculation.
            return status.isActive ? t.id : nil
        }
        return status.isActive || awaiting.turnID == t.id ? t.id : nil
    }

    /// The live turn's reply, while it is still at work: no cost under it until the turn stops. Waiting on you, the
    /// reply is in, so the cost shows (#5470).
    private var hiddenUsageAnswer: String? {
        guard let id = liveTurnID, session?.state != .needsYou,
              case .turn(let turn) = store.blocks.last, turn.id == id,
              case .assistant(let item) = turn.answer else { return nil }
        return item.id
    }

    /// Messages sent from here that the transcript does not have yet, minus those Claude holds in its queue (the queue
    /// strip shows them).
    private var shownPending: [PendingMessage] { backgroundActivity ? [] : pendingPlacement.chat }

    /// The row's kept messages (#10194) the transcript does not have yet (the row may lag behind the line).
    private var keptUndelivered: [UndeliveredMessage] {
        guard !backgroundActivity, let list = session?.undelivered, !list.isEmpty else { return [] }
        return UndeliveredMessage.shown(list, landed: store.items.compactMap(\.clientMsgID),
                                        drawn: (store.pending + store.arriving).map(\.id.uuidString))
    }

    /// What was sent from here and is not in the transcript yet, split between the chat and the queue strip (#1461).
    private var pendingPlacement: (strip: [PendingMessage], chat: [PendingMessage]) {
        QueuePlacement.split(store.arriving + store.pending, working: session?.status.isActive == true, queued: session?.queued ?? [],
                             stripShown: (style == .dev || session?.assistant == true))
    }

    /// The wait between a send and the hub's working (`ChatAwaitingTurn`); not for a subagent being viewed.
    private var awaiting: ChatAwaitingTurn {
        _ = awaitTick
        guard viewedAgent == nil else { return .none }
        if let feedback = store.sendFeedback {
            if store.pending.contains(where: { $0.id == feedback.id }) || store.arriving.contains(where: { $0.id == feedback.id }) { return .none }
            if case .turn(let turn) = store.blocks.last, feedback.belongs(to: turn) { return .none }
        }
        let failed = session?.failureReason == nil ? nil : (session?.stateSince ?? .distantFuture)
        return ChatAwaitingTurn.of(pending: store.arriving + store.pending, last: store.blocks.last, status: status,
                                   failedSince: failed, now: .now)
    }

    /// When that wait runs out by itself, so the chat stops showing it as started then.
    private var awaitDeadline: Date? {
        if let feedback = store.sendFeedback, feedback.phase != .finished { return feedback.deadline }
        return ChatAwaitingTurn.deadline(pending: store.arriving + store.pending, last: store.blocks.last)
    }

    private var lastPromptID: String? { store.items.last { $0.role == "user" && $0.kind == .text }?.id }

    /// A swipe to the right goes from files or Git back to the chat. To the left there is none: on the chat a drag to
    /// the left quotes a message (#1058); files are behind the header's folder button. On the chat it is off: there it
    /// would do nothing, and the system's swipe back from anywhere on a pushed page is the page's (#9971).
    private var swipeablePanes: some View {
        #if os(visionOS)
        // A pinch-and-drag scrolls on visionOS; a sideways drag here would take it. The panes switch with their buttons.
        paneContent
        #else
        paneContent
            .gesture(SwipeTo(direction: .right, enabled: style == .dev && pane != .chat) { swipePane(forward: false) })
        #endif
    }

    @ViewBuilder private var paneContent: some View {
        switch pane {
        case .chat: chatPane.stackSegment().transition(.move(edge: .leading))
        case .terminal:
            if session?.canTerminal == true {
                TerminalPanel(key: key, switchTo: switchTo, onKeyboard: terminalKeyboard)
                    .padding(.horizontal, Theme.Space.l).padding(.bottom, Theme.Space.s)
                    .padding(.top, style == .dev ? chrome.height - min(chrome.collapse, chrome.maxCollapse) : 0)
            } else { chatPane.stackSegment() }
        // Files and Git come in from the right (a swipe to the left), the chat back from the left.
        case .git: GitPane(machineID: key.machineID, group: key.group, cwd: session?.cwd ?? "", status: sharedGit, review: openReview,
                        commentKey: session?.canSend == false ? nil : key).padding(.top, headerRoom)
            .transition(.move(edge: .trailing))
        case .files: FilesPane(machineID: key.machineID, group: key.group, cwd: session?.cwd ?? "", git: git, key: key,
                                     reloadGit: { await loadGit() }, reveal: $filesReveal, transfers: folderTransfers).padding(.top, headerRoom)
            .transition(.move(edge: .trailing))
        }
    }

    /// Typing on the terminal folds the header to its slim bar, so the rows over the keyboard go to the terminal; the
    /// keyboard going down opens it again.
    private func terminalKeyboard(_ up: Bool) {
        guard style == .dev else { return }
        withAnimation(Theme.Motion.layout) { chrome.collapse = up ? chrome.maxCollapse : 0 }
    }

    /// The custom header's full height (Agents; the assistant's floating bar): the other panes sit below it, the chat
    /// list scrolls under it.
    private var headerRoom: CGFloat {
        if backgroundActivity { return 0 }
        return style == .dev ? (sideBars ? sideHeaderHeight : chrome.height) : AssistantTopBar.room(railEdge) + topExtra
    }

    /// Git only for repositories, files only when we know the directory; the assistant has neither.
    private var panes: [SessionPane] {
        guard let session else { return [.chat] }
        return AgentMenuFacts.panes(session, git: AgentMenuRouting.shared.gitStatus(session))
    }

    private var paneBadges: [SessionPane: String] {
        AgentMenuFacts.paneBadges(session.flatMap { AgentMenuRouting.shared.gitStatus($0) })
    }

    private var sharedGit: Binding<GitStatus?> {
        Binding(get: { git }, set: { new in
            if let session { AgentMenuRouting.shared.store.set(new, for: .init(session: session.key, cwd: session.cwd)) }
        })
    }

    private func loadGit() async {
        guard style == .dev, let session, !session.cwd.isEmpty, let api = model.api(session.group) else { return }
        await AgentMenuRouting.shared.store.load(.init(session: session.key, cwd: session.cwd), api: api)
    }

    /// The chat is an upside-down list (newest first, flipped back on screen), the way chat apps do it: the newest
    /// message sits at scroll offset 0, so opening the page, following new messages and ↓ are exact jumps to 0 — a
    /// lazy stack never has to estimate its way to the bottom (that is what left pages blank). Older pages load at the
    /// far end and never push what you are reading.
    @ViewBuilder private var chatPane: some View {
        // The list above, the composer below — stacked, not overlaid: with the keyboard up the whole page gets
        // shorter and the newest message (offset 0 of the flipped list) stays right above the composer.
        let under = underFooter
        VStack(spacing: 0) {
            // A hairline above the list: a scroll view touching the top safe area extends under it and adds its
            // inset — at the wrong end once flipped. Not touching it, the list gets no safe area at all (the
            // composer below keeps it off the bottom and the keyboard).
            Color.clear.frame(height: Theme.Size.line)
            ChatRegion(name: "ChatTranscript.body") {
                chatScroll()
                    // What reaches the footer fades out behind it, under everything drawn over the list (#3696).
                    .overlay(alignment: .bottom) { footerFog(under: under) }
                    // The navigator and the arrow keep to the part of the list over the footer.
                    .overlay { if layout.on { EdgeScrubOverlay(state: scrub).padding(.bottom, under) } }
                    .modifier(EdgeScrubbing(on: layout.on, band: scrubBand, changed: scrubbed))
                    .tabActiveTask(id: "\(shownKey)|\(store.resets)|\(lastPromptID ?? "")") {
                        await loadUserInputIndex()
                    }
                    // not `tabActiveTask`: the app coming back to the front asks nothing (#10297)
                    .task(id: "\(shownKey)|\(session?.permissionMode ?? "")|\(session?.bypassPending == true)") {
                        await loadModeChanges()
                    }
                    .tabActiveTask(id: navigationJump) {
                        guard let mark = navigationJump else { return }
                        await jumpToInput(mark)
                    }
                    .overlay(alignment: .bottomTrailing) { arrowLayer.padding(.bottom, under) }
                    #if DEBUG
                    .overlay(alignment: .topLeading) { if Self.scrollProbe { scrollState } }
                    .screenProbe("chat", page: style == .assistant ? "assistant" : "agents",
                                 ChatShown(key: shownKey.machineID + "/" + shownKey.sessionID, appeared: appearCount, highlighted: quoteFlash?.line))
                    .overlay(alignment: .topLeading) {
                        #if DEBUG
                        if UnfoldTrace.enabled { unfoldProbe } else if ScreenProbe.enabled { rowsProbe }
                        #else
                        if ScreenProbe.enabled { rowsProbe }
                        #endif
                    }
                    #endif
            }
            // It may get as short as the cards make it while the keyboard is up (its margins set no minimum of their own).
            .frame(minHeight: .zero)
            // On behind the top of the footer, which is drawn after it, over it (`underFooter`).
            .padding(.bottom, -under)
            // A stack of its own, laid out before the list (which may get as short as nothing): with an attachment over the
            // shell's composer (a photo, a quote) the footer kept its height while the draft grew, and the field held a line
            // and a half of it, cut at its top (#3703, the open iPhone Duo on its side with the keyboard up). Priority on
            // the region alone did not do it. Not the classic composer's island, which takes what it is offered until it
            // has measured itself.
            ChatRegion(name: "ChatFooter.body") { VStack(spacing: 0) { pickOrFooter } }
                .layoutPriority(usesShellComposer ? 1 : 0)
                // What the footer takes of the page: the pending cards grow into the rest while the keyboard is up.
                // Exact, like the cards' own height inside it (`PendingCards.shown`): the cards take one from the other for
                // what the footer holds besides them, which must not move with their own height (#1580).
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { footerRoom.footer = $0 }
        }
        // The height the page offers, even when what is in it is taller: the pending cards size themselves by it. A
        // frame with both bounds takes the offered size whatever the content (not a design value: no token for it).
        .frame(minHeight: .zero, maxHeight: .infinity)
        // A floating keyboard floats over the page: the composer stays at the bottom, the list keeps its height (#2565).
        .keepsHeightUnderUndockedKeyboard()
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if abs(footerRoom.height - h) >= 1 { footerRoom.height = h }
        }
        // As short as the list may get while the keyboard is up: the header over it (folded or not) and the hairline above
        // it. The cards take the rest (#1912).
        .onChange(of: headerRoom, initial: true) { _, h in
            footerRoom.listFloor = Theme.Size.line + Theme.Space.s // and a gap over 「N 个待处理」
            footerRoom.fixedHeader = h
            footerRoom.chrome = style == .dev && !sideBars ? chrome : nil
            pin.riseTop = scratch.listFrame.minY + h + Theme.Space.l // `riseTop`, with the header's new height
        }
        .onChange(of: session?.awaitingFirstMessage == true ? session?.paneID : nil, initial: true) { _, pane in
            if let pane { codexWaitingPane = pane; codexWaitingID = key.sessionID }
        }
        .onChange(of: codexThread) { _, next in
            guard let next else { return }
            codexWaitingPane = nil
            switchTo(next)
        }
        .environment(\.chatLayout, layout)
        .modifier(ReplyUsageLoader(
            items: store.items,
            lines: currentLines(),
            persona: style == .assistant,
            hideAnswerID: hiddenUsageAnswer,
            canAsk: model.api(shownKey.group) != nil,
            catalog: { ids in
                guard let api = model.api(shownKey.group) else { throw CancellationError() }
                return try await api.catalogPrices(ids)
            }))
        .stackSegment()
        .environment(\.openSubagent, actions.openSubagent)
        .environment(\.openChildSession, childOpener)
        .environment(\.openPlan, actions.openPlan)
        .environment(\.subagentsByCall, subagentsByCall)
        .environment(\.childSessionRows, Dictionary(scratch.childRows.map { ($0.key, $0) }, uniquingKeysWith: { _, newer in newer }))
        .environment(\.showsChildHarness, style != .assistant)
        .task(id: model.revealChild) {
            guard let reveal = model.revealChild,
                  let target = ChildSessionLines.target(matching: reveal, in: currentLines()) else { return }
            if let sid = target.supervisionID, !isSupervisionOpen(sid) { flipSupervision(sid) }
            if !isOpen(target.groupID) { flipWork(target.groupID) }
        }
        .stackSegment()
        .environment(\.workflowChat, key)
        .environment(\.chatActions, actions)
        .environment(\.subagentParent, key)
        .environment(\.openFold, actions.openFold)
        .environment(\.taskIndex, store.tasks)
        .environment(\.jumpToCall, actions.jumpToCall)
        .environment(\.openQuote, actions.openQuote)
    }

    /// How the chat draws (#1666): the new conversation layout in Persona's conversations on iPhone and iPad.
    private var layout: ChatLayout { ChatLayoutSettings.shared.layout(assistant: style == .assistant) }

    /// Where a finger held still opens the right-edge navigator (window coordinates): the band along the list's right
    /// edge, below the top bar floating over it.
    private var scrubBand: () -> CGRect {
        let room = headerRoom
        return { [scratch] in
            let f = scratch.listFrame
            return CGRect(x: f.maxX - Theme.Size.edgeBand, y: f.minY + room, width: Theme.Size.edgeBand, height: max(0, f.height - room))
        }
    }

    #if os(iOS)
    /// The right-edge navigator's finger (`EdgeScrubGesture`): opens on the messages the user typed, picks as it moves,
    /// goes to the picked one as it lets go, if it moved to one (#2615).
    private func scrubbed(_ p: CGPoint, _ phase: UIGestureRecognizer.State) {
        let y = p.y - scratch.listFrame.minY
        switch phase {
        case .began:
            let opened = withAnimation(Theme.Motion.state) {
                scrub.open(navigationMarks(), height: scratch.listFrame.height, top: headerRoom, at: y)
            }
            if opened { EdgeScrubHaptics.open() }
        case .changed:
            if scrub.move(to: y) { EdgeScrubHaptics.tick() }
        case .ended:
            // The tick it opened on is only where the finger happened to be: letting go there is no choice.
            let chosen = scrub.moved
            if let mark = withAnimation(Theme.Motion.state, { scrub.close() }), chosen { navigationJump = mark }
        default:
            withAnimation(Theme.Motion.state) { _ = scrub.close() }
        }
    }
    #else
    private func scrubbed(_ p: CGPoint, _ phase: Int) {}
    #endif

    /// Derive only when opening the navigator, never on scroll frames. Loaded history expands the initial window.
    private func navigationMarks() -> [EdgeMark] {
        ChatUserNavigation.marks(index: userInputIndex?.key == shownKey ? userInputIndex?.index : nil,
                                 lines: currentLines(), history: store.history, revealingOlder: userScrolled)
    }

    private func loadUserInputIndex() async {
        guard layout.on, let api = model.api(shownKey.group) else { return }
        let key = shownKey, store = store
        for attempt in 0..<4 {
            do {
                let index = try await api.userInputIndex(key)
                guard !Task.isCancelled, shownKey == key, model.api(key.group)?.config == api.config else { return }
                guard store.history == nil || index.history == store.history else {
                    await model.reload(key, shouldApply: { !Task.isCancelled && shownKey == key })
                    continue
                }
                userInputIndex = (key, index)
                return
            } catch {
                // Older/offline hubs retain the loaded-only navigator; retry only while this page is visible.
                guard !Task.isCancelled, shownKey == key, attempt < 3 else { return }
                try? await Task.sleep(for: .seconds(3))
            }
        }
    }

    /// An Agent's chat reads its permission mode changes when it opens and when its mode changes, nothing else: not on a
    /// timer, a refresh, the page shown again or the app back in front (one request; the energy lane counts them). A
    /// restart into Full access queued or called off (`bypassPending`) counts as a change of mode: the mode stays the
    /// same, and its line would only show on reopening (#11410). After a change of mode it looks once more 2 s later: the
    /// manager records the change as the node answers, which can be a moment after the row says the new mode. Off the
    /// main actor; a failure (an older hub, offline) keeps what there was and asks again next time. Persona's own
    /// conversations have none.
    private func loadModeChanges() async {
        guard style == .dev, let api = model.api(shownKey.group) else { return }
        let key = shownKey, read = "\(key)|\(session?.permissionMode ?? "")|\(session?.bypassPending == true)"
        guard modeChangesRead != read else { return }
        let modeChanged = modeChangesRead?.hasPrefix("\(key)|") == true
        for wait in modeChanged ? [0, 2] : [0] {
            if wait > 0 { try? await Task.sleep(for: .seconds(wait)) }
            guard !Task.isCancelled, let changes = try? await api.modeChanges(key), !Task.isCancelled, shownKey == key else { continue }
            modeChangesRead = read
            // none (most chats), or the same again: nothing to draw again
            if changes.isEmpty && modeChanges?.key != key { continue }
            if modeChanges?.key == key && modeChanges?.changes == changes { continue }
            modeChanges = (key, changes)
        }
    }

    /// A released choice may not be in the two-turn tail yet. Load contiguous, folded pages only on that choice.
    /// Both the request and every wait are bounded and fenced against leaving the page or rewriting its history.
    private func jumpToInput(_ mark: EdgeMark) async {
        let key = shownKey, store = store, history = store.history
        guard let api = model.api(key.group) else { return }
        defer { if navigationJump == mark { navigationJump = nil } }
        let deadline = ContinuousClock.now + .seconds(60)
        for _ in 0...Self.entryPageLimit {
            guard !Task.isCancelled, shownKey == key, store.history == history,
                  model.api(key.group)?.config == api.config, ContinuousClock.now < deadline else { return }
            while (store.isRebuilding || store.isLoadingOlder), !Task.isCancelled, ContinuousClock.now < deadline {
                try? await Task.sleep(for: .milliseconds(20))
            }
            guard !Task.isCancelled, shownKey == key, store.history == history, ContinuousClock.now < deadline else { return }
            if let line = ChatUserNavigation.lineID(for: mark, lines: currentLines()) {
                jumpToLine(line)
                return
            }
            guard store.hasMore, let first = store.items.first else { break }
            store.isLoadingOlder = true
            do {
                let page = try await api.items(key, beforeSeq: first.seq, turns: ChatUserNavigation.initialCount)
                store.isLoadingOlder = false
                guard !Task.isCancelled, shownKey == key, store.history == history,
                      model.api(key.group)?.config == api.config,
                      page.history == nil || page.history == history else { return }
                guard let earlier = page.items.first, earlier.seq < first.seq else { break }
                store.prepend(page.items, hasMore: page.hasMore)
            } catch {
                store.isLoadingOlder = false
                if error.isHistoryNotFound, !Task.isCancelled, shownKey == key, store.history == history,
                   model.api(key.group)?.config == api.config { store.prepend([], hasMore: false) }
                if !Task.isCancelled { model.show(error) }
                return
            }
        }
        if !Task.isCancelled { model.show(APIError(status: 0, message: String(localized: "Couldn't load this message. Try again."))) }
    }

    /// Brings a line of the loaded chat to the middle of the list and lights it up, as a quote's does; the list stops
    /// following the newest.
    private func jumpToLine(_ id: String) {
        userScrolled = true
        entry = nil
        setFollow { $0.read() }
        Task { @MainActor in
            withAnimation(Theme.Motion.standard) { position.scrollTo(id: id, anchor: .center) }
            try? await Task.sleep(for: .milliseconds(250))
            quoteFlash = QuoteFlashMark(line: id)
        }
    }

    /// A note's or a to-do's block from Add as Context (#3721) leads back to it, as a card's link does
    /// (`persona://kb/<kind>/<id>`, `persona://task/<id>`); false for any other block.
    private func openContextLink(_ ref: ContextRef) -> Bool {
        guard ref.kind == "context", let c = URLComponents(string: ref.url), c.scheme == "persona" else { return false }
        let parts = c.percentEncodedPath.split(separator: "/").compactMap { String($0).removingPercentEncoding }
        switch c.host {
        case "task" where parts.count == 1:
            TasksStore.shared.detailTask = parts[0]
            return true
        case "kb" where parts.count >= 2 && KBKind(rawValue: parts[0]) != nil:
            let id = parts.dropFirst().joined(separator: "/")
            openedKnowledge = KBEntry(kind: parts[0], id: id, name: id, relevance: 0)
            return true
        default:
            return false
        }
    }

    /// From a quote in a sent bubble to the message it quotes (#986): brought to the middle of the list and lit up
    /// for a moment. False when it is not in the loaded chat (an older page, another conversation): the block shows
    /// its whole text instead.
    /// `hold`: how long the message stays lit (`Theme.Quote.landingHold` where the chat opened at it).
    private func jumpToQuoted(_ ref: ContextRef, hold: Duration = Theme.Quote.flashHold) -> Bool {
        if ref.isForward { return openForwarded(ref) }
        if openContextLink(ref) { return true }
        guard ref.kind == "quote", !ref.msg.isEmpty else { return false }
        return jumpToMessage(ref.msg, hold: hold)
    }

    /// Finds the pending cards' messages in the loaded chat (`CardSource`) and how View in Chat goes to one (#9392).
    private func findCardSources() {
        let items = store.items
        var found: [String: String] = [:]
        for card in pendingCards { found[card.id] = CardSource.item(for: card.interaction, in: items) }
        if cardSources.items != found { cardSources.items = found }
        // After the card has folded (`InteractionPanel`): the list's new height first, then the jump, which lands where
        // the list measured it.
        cardSources.open = { id in
            Task { @MainActor in
                try? await Task.sleep(for: .milliseconds(400))
                _ = jumpToMessage(id)
            }
        }
    }

    /// Brings message `msg` (an item's id) of the loaded chat to the middle of the list and lights it up for `hold`, opening
    /// the work line it is folded in; the list stops following the newest. False when it is not in the loaded chat.
    private func jumpToMessage(_ msg: String, hold: Duration = Theme.Quote.flashHold, stillMatches: (() -> Bool)? = nil) -> Bool {
        var target: String?
        for line in currentLines() {
            switch line.content {
            case .row, .recaps, .answerPart:
                if line.holds(msg) { target = line.id }
            case .work(let wid, let items, let live, _, _):
                // The agent's words before its last ones are in the work line: open it, go to that step.
                if let item = items.first(where: { if case .text(let i) = $0 { i.id == msg } else { false } }) {
                    if !isOpen(wid, live: live) {
                        var t = Transaction()
                        t.disablesAnimations = true
                        withTransaction(t) { flipWork(wid) }
                    }
                    target = wid + "/" + item.id
                }
            case .workStep(let group, let item, _):
                if case .text(let i) = item, i.id == msg { target = group + "/" + item.id }
            default: break
            }
            if target != nil { break }
        }
        guard let target else { return false }
        userScrolled = true
        setFollow { $0.read() }
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(120)) // an opened work line's steps are laid out first
            guard stillMatches?() != false else { return }
            withAnimation(Theme.Motion.standard) { position.scrollTo(id: target, anchor: .center) }
            try? await Task.sleep(for: .milliseconds(250))
            guard stillMatches?() != false else { return }
            quoteFlash = QuoteFlashMark(line: target, hold: hold)
        }
        return true
    }

    /// A forward's card (#3807): its original chat, at the message, when this app has that chat; else the card opens to
    /// the whole text.
    private func openForwarded(_ ref: ContextRef) -> Bool {
        guard let o = ForwardMessage.origin(ref) else { return false }
        let origin = SessionKey(group: key.group, machineID: o.machine, sessionID: o.session)
        guard model.session(origin) != nil else { return false }
        model.openLinked(origin, item: o.item)
        return true
    }

    /// Whether `id` (a tool_use_id) appears anywhere in `lines`, including inside folded work or supervision rounds.
    private func hasCallTarget(_ id: String, in lines: [ChatLine]) -> Bool {
        for line in lines {
            switch line.content {
            case .work(_, let items, _, _, _):
                for case .step(.call(let c)) in items where c.use.toolUseID == id { return true }
            case .row(.tools(let steps)):
                for case .call(let c) in steps where c.use.toolUseID == id { return true }
            case .agentOut(_, let c):
                if c.use.toolUseID == id { return true }
            case .agents(_, let calls, _, _):
                if calls.contains(where: { $0.use.toolUseID == id }) { return true }
            case .supervision(_, let inner, _, _):
                if hasCallTarget(id, in: inner) { return true }
            default:
                break
            }
        }
        return false
    }

    /// Finds the rendered line id for `toolUseID` after its enclosing supervision/work/run lines have been opened.
    private func renderedLineID(forCall id: String, in lines: [ChatLine]) -> String? {
        for line in lines {
            switch line.content {
            case .workStep(_, .step(.call(let c)), _) where c.use.toolUseID == id:
                return line.id
            case .agentOut(_, let c) where c.use.toolUseID == id:
                return line.id
            case .agents(_, let calls, _, _) where calls.contains(where: { $0.use.toolUseID == id }):
                return line.id
            case .row(.tools(let steps)) where steps.contains(where: { if case .call(let c) = $0 { c.use.toolUseID == id } else { false } }):
                return line.id
            default:
                break
            }
        }
        return nil
    }

    /// From a background task's notice to the call that started it: opens the work line it is folded in and brings
    /// the step into view (rows of the newest end hold still; the list stops following it).
    private func jumpToCall(_ id: String) {
        func holds(_ steps: [ToolStep]) -> String? {
            for case .call(let c) in steps where c.use.toolUseID == id { return c.id }
            return nil
        }
        func locate(in lines: [ChatLine], supervisionID: String? = nil) -> (supervisionID: String?, target: String)? {
            for line in lines {
                switch line.content {
                case .work(let wid, let items, let live, _, _):
                    let steps = items.compactMap { if case .step(let s) = $0 { s } else { nil } }
                    if let step = holds(steps) {
                        var t = Transaction()
                        t.disablesAnimations = true
                        if let sid = supervisionID, !isSupervisionOpen(sid) {
                            withTransaction(t) { flipSupervision(sid) }
                        }
                        if !isOpen(wid, live: live) {
                            withTransaction(t) { flipWork(wid) }
                        }
                        if AppStyle.current.usesBeautifulUI, let run = runID(holding: step, group: wid, items: items), !isRunOpen(run) {
                            withTransaction(t) { _ = openRuns.insert(run) }
                        }
                        let resolved = renderedLineID(forCall: id, in: currentLines()) ?? (wid + "/" + step)
                        return (supervisionID, resolved)
                    }
                case .row(.tools(let steps)):
                    if holds(steps) != nil {
                        if let sid = supervisionID, !isSupervisionOpen(sid) {
                            var t = Transaction()
                            t.disablesAnimations = true
                            withTransaction(t) { flipSupervision(sid) }
                        }
                        return (supervisionID, line.id)
                    }
                case .supervision(let sid, let inner, _, _):
                    if let found = locate(in: inner, supervisionID: supervisionID ?? sid) { return found }
                default: break
                }
            }
            return nil
        }
        guard let found = locate(in: currentLines()) else {
            Haptics.attention()
            return
        }
        let target = found.target
        userScrolled = true
        setFollow { $0.read() }
        Haptics.tap()
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(160)) // the opened work line's steps are laid out first
            withAnimation(Theme.Motion.standard) { position.scrollTo(id: target, anchor: .center) }
        }
    }

    /// Opening a work line the page folded fetches its steps (docs/protocol/protocol.md「Folded work」).
    /// The pin holds the work line's header until they are in (`ChatPin.awaiting`, #3757).
    private var openFold: (Item) async throws -> Void {
        { [model, shownKey, pin] fold in try await pin.awaiting { try await model.unfold(shownKey, fold) } }
    }

    @ViewBuilder private var footer: some View {
        if backgroundActivity {
            EmptyView()
        } else if let a = viewedAgent {
            SubagentFooter(agent: a) {
                if let host { host.leave() } else { view(nil) }
            }
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
        } else {
            // No room of its own above: the list's margin at its newest end is the room over its first row (`newestGap`).
            mainFooter
        }
    }

    #if DEBUG
    /// The made-up picture `-showComposerPhoto` attaches: a blue square with a white bar, easy to tell in a screenshot.
    static let debugPhoto: UIImage = UIGraphicsImageRenderer(size: CGSize(width: 240, height: 240)).image { c in
        UIColor.systemBlue.setFill()
        c.fill(CGRect(x: 0, y: 0, width: 240, height: 240))
        UIColor.white.setFill()
        c.fill(CGRect(x: 40, y: 104, width: 160, height: 32))
    }

    /// The list's distance from its newest end by its scroll view, for `probe.chat-rows` (-1: none).
    private var listGap: Int {
        return pin.measuredNewestGap.map { Int($0.rounded()) } ?? -1
    }

    /// `probe.chat-rows` (`-probes YES`): what the list has and shows, sampled ten times a second (which lines are on
    /// screen is the list's, written as it scrolls and not observed): the chat must not go blank or move by itself.
    private var rowsProbe: some View {
        TimelineView(.periodic(from: .now, by: 0.1)) { _ in
            let order = currentLines().map(\.id)
            let index = Dictionary(order.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
            let visible = scratch.visibleLines.filter { index[$0] != nil }.sorted { index[$0]! < index[$1]! }
            let lastShown = order.last.map { scratch.drawnLines.contains($0) } ?? false
            // A work line's header by its own report, the one the pin holds it by: the pin moves the list from inside
            // that report, and the line's own geometry callback missed the move. A header kept in place while a reply
            // streamed under it read as moving 56 pt and back, on screenshots identical to the pixel (#1046).
            Color.clear.frame(width: Theme.Size.line, height: Theme.Size.line)
                .screenProbe("chat-rows", page: style == .assistant ? "assistant" : "agents",
                             ChatRowsShown(items: store.items.count, lines: order.count, visible: visible.count, drawn: scratch.drawnLines.count,
                                           top: visible.first, topY: visible.first.flatMap { scratch.workHeadY[$0] ?? scratch.lineY[$0] }.map { Int($0.rounded()) },
                                           newest: scratch.atNewest, follows: follow.follows,
                                           offset: Int(scratch.scrollY.rounded()), content: Int(scratch.contentHeight.rounded()),
                                           frozen: frozen != nil, ids: order.suffix(4).map { String($0.suffix(16)) },
                                           x: Int(scratch.offsetX.rounded()),
                                           near: follow.near, mode: "\(follow.mode)", arrow: arrowShows, gap: Int(scratch.newestGap.rounded()),
                                           dragging: scratch.userDragging, held: pin.holding, blank: scratch.blankLandings,
                                           glideTaps: scratch.glideTaps, jumpMs: scratch.jumpMs, slips: scratch.jumpSlips,
                                           listGap: listGap, lastShown: lastShown, storedRow: position.viewID(type: String.self) != nil,
                                           newestY: scratch.newestLineY.flatMap { $0.id == order.last ? Int($0.y.rounded()) : nil },
                                           startMends: scratch.startMends))
        }
    }

    #if DEBUG
    /// `probe.unfold` (`-unfoldTrace YES`): what each open and fold cost (`UnfoldTrace`). Drawn instead of
    /// `probe.chat-rows`, whose ten updates a second were frames of their own in what it measures.
    private var unfoldProbe: some View {
        Color.clear.frame(width: Theme.Size.line, height: Theme.Size.line)
            .screenProbe("unfold", page: style == .assistant ? "assistant" : "agents", UnfoldTrace.shared.results)
            .onAppear {
                UnfoldTrace.shared.idle(after: 6)
                UnfoldTrace.shared.newestGap = { [pin] in pin.measuredNewestGap }
                UnfoldTrace.shared.riseTop = { [pin] in pin.riseTop }
            }
    }
    #endif

    /// `-showScrollState YES`: where the list is, for UI tests (`ChatFollowTests`). XCUITest's frames of rows in the
    /// flipped list are wrong; the list itself knows which lines are on screen.
    static let scrollProbe = UserDefaults.standard.bool(forKey: "showScrollState") || scrollProbeVisible
    /// `-showScrollState visible`: the same, readable in a screenshot.
    static let scrollProbeVisible = UserDefaults.standard.string(forKey: "showScrollState") == "visible"

    private var scrollState: some View {
        TimelineView(.periodic(from: .now, by: 0.5)) { _ in
            let lines = currentLines()
            let newest = lines.last?.id ?? ""
            let shown = Set(scratch.visibleLines)
            let lastShown = lines.lastIndex { shown.contains($0.id) }.map { lines.count - 1 - $0 } ?? -1
            Text("bottom=\(follow.near ? 1 : 0) follows=\(follow.follows ? 1 : 0) mode=\(follow.mode) frozen=\(frozen == nil ? 0 : 1) arrow=\(arrowShows ? 1 : 0) newestShown=\(shown.contains(newest) ? 1 : 0) behind=\(lastShown) items=\(store.items.count) withheld=\(store.items.count - (frozen.map { f in Self.prefixCount(store.items) { ($0.seq, $0.part) <= (f.seq, f.part) } } ?? store.items.count)) key=\(key.machineID)/\(key.sessionID)")
                .font(Theme.Font.label).opacity(Self.scrollProbeVisible ? 1 : 0.01).allowsHitTesting(false)
                .accessibilityIdentifier("chat.scrollState")
        }
    }
    #endif

    /// ↓ over the list. It comes and goes with a short scale and fade, not at once (#3027).
    @ViewBuilder private var arrowLayer: some View {
        ZStack { jumpArrow }.animation(Theme.Motion.state, value: arrowShows)
    }

    @ViewBuilder private var jumpArrow: some View {
            if arrowShows {
                Button(action: jumpToEnd) {
                    ThemedIcon(Theme.Icon.down).font(Theme.Font.bodyStrong).scaledFrame(width: Theme.Size.avatar, height: Theme.Size.avatar)
                }
                .accessibilityLabel("Jump to Latest")
                .themedButton()
                .clipShape(.circle)
                #if os(iOS)
                // The finger is taken by a UIKit view over the button, on touch-down: on a phone, a tap on the button
                // while the list still glided did nothing (`JumpArrowTouch`, #3027).
                .overlay { JumpArrowTouch(action: jumpToEnd) }
                #endif
                .padding(Theme.Space.l)
                .transition(.scale.combined(with: .opacity))
            }
    }

    /// Above the assistant's composer (docs/assistant/unified-entry.md): what the conversation's tasks ask, the @ list while
    /// a mention is typed, the action bar (#3393), and the route chip.
    @ViewBuilder private var taskFooter: some View {
        let conversation = key.sessionID
        let tasks = TasksStore.shared
        // What waits for the owner, one card at a time behind To Decide (#12831): the conversation's own cards and its tasks'.
        let own = dockCards
        QuestionDock(own: own, conversation: conversation, room: footerRoom, state: dock, inCall: model.voice?.isLive(on: key) ?? false)
            .environment(\.cardSources, cardSources)
            .task(id: CardSourceInputs(cards: own.map(\.id), count: store.items.count, last: store.items.last?.id)) {
                if !own.isEmpty { findCardSources() }
            }
        DraftReader(key: key) { typed in
            if let range = TaskMentionList.mention(in: typed) {
                TaskMentionList(conversation: conversation) { todo in
                    withAnimation(Theme.Motion.state) {
                        var draft = model.drafts[key] ?? ""
                        draft.removeSubrange(range)
                        model.drafts[key] = draft
                        tasks.routes[conversation] = todo.id
                    }
                }
            }
        }
        // Out of the way of a voice call (docs/assistant/voice.md 6.2「通话期间」), back when it ends.
        HiddenDuringCall(key: key) {
            // Needs You (always visible), In Progress and neutral Wrap Up in one row (#3393, #16165); Finished opens
            // from the tray header instead of staying persistent on the collapsed bar.
            if !NativeTemplateStore.shared.plainMainChat {
                AssistantActionBar(conversation: conversation, key: key, computer: computerActivity, dock: dock, dockOwn: own)
                // When using `ShellComposerBar`, task routing lives in `ComposerTopSlot` fused to the composer's top edge.
                if !usesShellComposer { RouteChip(conversation: conversation) }
            }
        }
    }

    /// The conversation's own cards as the assistant's pager takes them (#12831).
    private var dockCards: [DockCard] {
        pendingCards.map { DockCard(interaction: $0.interaction, enabled: $0.enabled, localOnly: $0.localOnly, blocked: $0.blocked, from: $0.from) }
    }

    /// The assistant's pager holds the conversation's own cards (#12831): the stack under the entries is only an Agent's.
    private var cardsDocked: Bool { style == .assistant && viewing == nil }

    /// The agent's pending cards, and in DEBUG the sample ones the screenshot switches add (`-showSampleQuestion YES`…).
    private var pendingCards: [PendingCards.Card] {
        let parentPage = style == .dev && host == nil
        var cards = model.interactions(for: key).compactMap { ix -> PendingCards.Card? in
            // A stopped Agent's work card stays open for putting it off or dismissing it, which send the Agent nothing (#6839).
            let answerable = model.canAnswer(ix)
            let localOnly = !answerable && ix.isManagerWork
            let blocked = answerable ? nil : model.answerBlock(ix)
            let enabled = answerable || localOnly || blocked == nil
            // A side session's card (#7944), here by its `folded_under` as every folded child's: "From Side Session ·
            // <title>", and not while that side's own page is the one shown, which has it.
            if parentPage, ix.sessionKey != key, let side = model.sideChat(of: ix) {
                guard side != sideUI.shown else { return nil }
                return PendingCards.Card(
                    interaction: ix, enabled: enabled, localOnly: localOnly, blocked: blocked,
                    from: Self.sideCardSource(side, ix, model))
            }
            return PendingCards.Card(interaction: ix, enabled: enabled, localOnly: localOnly, blocked: blocked)
        }
        // A card answered in a terminal dialog of several questions stays, greyed, until the next one comes (#2970).
        if let move = model.screenMoves[key], !cards.contains(where: { $0.interaction.kind == .screen }) {
            cards.append(PendingCards.Card(interaction: move.card, enabled: false))
        }
        #if DEBUG
        let samples: [(String, (SessionKey) -> Interaction?)] = [
            ("showSampleQuestion", Interaction.sampleQuestion), ("showSampleScreen", Interaction.sampleScreen),
            ("showSamplePermission", Interaction.samplePermission), ("showSampleCodexApproval", Interaction.sampleCodexApproval),
            ("showSampleCodexQuestion", Interaction.sampleCodexQuestion),
        ]
        for (flag, sample) in samples where UserDefaults.standard.bool(forKey: flag) {
            if let ix = sample(key) { cards.append(PendingCards.Card(interaction: ix, enabled: true)) }
        }
        // `-showSampleStoppedWork YES`: a manager work card on an Agent that has stopped (#6839).
        if UserDefaults.standard.bool(forKey: "showSampleStoppedWork"), let ix = Interaction.sampleStoppedWork(key) {
            cards.append(PendingCards.Card(interaction: ix, enabled: true, localOnly: true))
        }
        // `-showVoiceSample asking|approval|plan_feedback`: the card the voice asks about or points to.
        cards += (model.voice?.sampleCards(key) ?? []).map { PendingCards.Card(interaction: $0, enabled: true) }
        // `-showSideSessions …`: the sample side's question, in its own chat and in its parent's.
        if let parent = model.sideParent(of: key), let ix = SideSessionSamples.card(for: key, parent: parent) {
            cards.append(PendingCards.Card(interaction: ix, enabled: true))
        }
        for row in sides.rows where row.key != sideUI.shown {
            if let ix = SideSessionSamples.card(for: row.key, parent: key) {
                cards.append(PendingCards.Card(interaction: ix, enabled: true, from: Self.sideCardSource(row.key, ix, model)))
            }
        }
        #endif
        return cards
    }

    /// "From Side Session · <its title>": the line a side session's card carries in its parent's chat (#7944).
    static func sideCardSource(_ side: SessionKey, _ ix: Interaction, _ model: AppModel) -> String {
        let title = model.session(side)?.displayTitle ?? ix.sessionTitle ?? ""
        return String(localized: "From Side Session · \(title)")
    }

    /// The conversation the post-resolution entry points at: another one, still needing you, not muted or quiet (#4348).
    /// `pendingCards.isEmpty` is the display-side half of "a new card takes the entry back": it is evaluated on every
    /// render, so a card that arrives while mainFooter is removed (viewing a subagent transcript) still hides the strip
    /// when the footer comes back — the onChange observer is not attached then to clear offeredNext.
    private var nextNeedsYouSession: Session? {
        guard pendingCards.isEmpty, let k = offeredNext, k != key, let s = model.session(k), s.state == .needsYou,
              !model.isMuted(s), !model.isQuiet(s) else { return nil }
        return s
    }

    /// Opens it: another agent in place (like the sibling menu), anything else through the model.
    private func openNext(_ s: Session) {
        offeredNext = nil
        if style == .dev, !s.assistant { switchTo(s.key) } else { Haptics.tap(); model.open(s.key) }
    }

    private var mainFooter: some View {
            let column = VStack(spacing: Theme.Space.m) {
                if style == .assistant, viewing == nil { taskFooter }
                // An agent operating a Mac (its own Cua Driver): with its running work, over the box (#1343).
                if style == .dev, viewing == nil, ModuleViews.any(.chatFooter, .init(from: key, activity: computerActivity)) {
                    ModuleViews(place: .chatFooter, context: .init(from: key, activity: computerActivity))
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if !cardsDocked, !pendingCards.isEmpty {
                    PendingCards(cards: pendingCards, room: footerRoom)
                        .stackSegment() // keep the deepest card subtree out of the page's opaque view chain (#15274)
                        .environment(\.cardSources, cardSources)
                        .task(id: CardSourceInputs(cards: pendingCards.map(\.id), count: store.items.count, last: store.items.last?.id)) {
                            findCardSources()
                        }
                }
                // The latest question the assistant answered here (#544), until put away; Session Info lists them all.
                if let answered = model.delegated(for: key).last {
                    DelegatedCard(answer: answered)
                }
                // The last card was answered: one low-key entry to the next conversation that needs you (#4348).
                if let next = nextNeedsYouSession {
                    NextNeedsYouStrip(title: next.assistant ? (next.title.isEmpty ? model.assistantShownName : next.title) : next.displayTitle(preferTab: model.prefersHerdrTabName(for: next))) {
                        openNext(next)
                    }
                }
                let assistantTopSlot = style == .assistant && viewing == nil && usesShellComposer && session?.isStopped != true
                if !assistantTopSlot {
                    let queued = session?.queued ?? []
                    let sending = pendingPlacement.strip
                    if !queued.isEmpty || !sending.isEmpty, style == .dev || session?.assistant == true {
                        QueueStrip(key: key, queued: queued, own: session?.ownQueuedCount ?? queued.count, sending: sending,
                                   photoMarks: model.chat(key).queuePhotoMarks,
                                   steers: session?.steersQueue == true || (session?.assistant == true && session?.canSend == true),
                                   persona: session?.assistant == true ? model.assistantShownName : nil,
                                   agentName: session?.agentLabel ?? "Claude")
                    }
                }
                // The newest plan while its line is out of view (#5602): which step of how many; tap, the whole plan.
                // Folded while a card asks something (#2970): it sat over the card's buttons.
                if viewing == nil, let plan = latestPlan, !planLineShown, pendingCards.isEmpty || !ScreenStepsLogic.enabled {
                    PlanPill(plan: plan) { planOpen = true }
                }
                // The assistant takes modes on Codex too (the node maps them to Codex's sandbox / approvals); a Codex thread
                // on its app-server takes its effort alone (setsEffortOnly).
                // In the shell the modes are in the composer's model menu (ShellComposerBar, #1091).
                let modes = viewing == nil && !usesShellComposer && session?.canSend == true && (session.map({ $0.canChangeModes || $0.setsEffortOnly }) ?? true)
                if modes {
                    SessionModeBar(key: key, session: session, pick: requestMode)
                        .padding(.leading, Theme.Space.xs)
                }
                if let s = session, s.isStopped {
                    #if os(iOS)
                    StoppedAgentBar(session: s, coordinator: takeoverCoordinator, openTerminal: openTerminal, resume: resumeHere) { adopted in
                        model.adopt(adopted)
                        switchTo(adopted.key)
                    } // Resume here opens the terminal page and resumes there (#5521)
                    #else
                    StoppedAgentBar(openTerminal: openTerminal) // read-only: its terminal page has the shell and Resume (#4754)
                    #endif
                } else if session?.isTerminalOnly == true {
                    // No reader for its conversation here: the terminal is the view, one tap instead of title › ⋯ › Terminal (#6170).
                    TerminalOnlyBar(openTerminal: openTerminal)
                } else if let s = session, !s.canSend, let reason = s.sourceSubtitle {
                    #if os(iOS)
                    ViewThatFits(in: .horizontal) {
                        HStack(alignment: .center, spacing: Theme.Space.s) {
                            Text(reason).foregroundStyle(.secondary).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                            CodexTakeoverControl(session: s, coordinator: takeoverCoordinator) { adopted in
                                model.adopt(adopted)
                                switchTo(adopted.key)
                            }
                        }
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text(reason).foregroundStyle(.secondary)
                            CodexTakeoverControl(session: s, coordinator: takeoverCoordinator) { adopted in
                                model.adopt(adopted)
                                switchTo(adopted.key)
                            }
                        }
                    }
                    .font(Theme.Font.caption).frame(maxWidth: .infinity, alignment: .leading)
                    #else
                    Text(reason).font(Theme.Font.caption).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                    #endif
                } else if session == nil, let reason = model.missingSessionReason(key) {
                    // Opened outside the current manager's filtered list and the hub could not supply the row (#8748).
                    Text(reason).font(Theme.Font.caption).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading)
                }
                if session?.isStopped != true { // a stopped Agent takes no messages: its line above says so (#4754)
                    AnyView(Composer(key: key, status: status, placeholder: style == .assistant ? String(localized: "Ask \(model.assistantShownName)…") : String(localized: "Message \(session?.agentLabel ?? "Claude") [Chat View c6f021]"),
                             covered: link != nil, assistantChrome: style == .assistant && viewing == nil,
                             onCleared: { switchTo($0) }, onFocus: { composerTyping = $0 }, pickMode: { requestMode($0) },
                             openAssistantModel: openAssistantModel)
                        // The shell's expanding field stays in the page's layout graph; so does BeautifulUI's prompt bar,
                        // whose menus hang above it: an island's hosting view takes no touches outside its bounds.
                        .layoutIsland(enabled: !usesShellComposer && !AppStyle.current.usesBeautifulUI))
                }
            }
            return AnyView(column)
            .padding(.horizontal, Theme.Space.m)
            .padding(.bottom, Theme.Space.s)
            .animation(Theme.Motion.standard, value: status)
            .animation(Theme.Motion.standard, value: model.interactions(for: key).map(\.id))
            // The last card answered: offer the next conversation that needs you; a new card takes the entry back (#4348).
            // Key and ids are one observed value: on an in-place chat switch the ids flip for a different conversation,
            // and SwiftUI does not order this against onChange(of: key), so a cross-key change is ignored here on purpose.
            .onChange(of: NextNeedsYouTrigger(key: key, ids: model.interactions(for: key).map(\.id))) { old, new in
                guard new.key == old.key else { return } // a different chat shown in place: onChange(of: key) clears it
                if new.ids.isEmpty, !old.ids.isEmpty {
                    offeredNext = NextNeedsYou.next(after: key, in: model.sessionsNow,
                                                    muted: { model.isMuted($0) || model.isQuiet($0) })
                } else if !new.ids.isEmpty {
                    offeredNext = nil
                }
            }
            .background {
                DraftReader(key: key) { typed in
                    Color.clear.onChange(of: typed) { _, draft in
                        if !draft.isEmpty, let recap = pendingRecap { dismissRecap(recap) } // typing closes it
                    }
                }
            }
    }

    /// The agent's "while you were away" recap shown open, while it is the newest thing in the chat and has not been
    /// closed (a tap, or starting to type). It is the chat's last line, in the flow with the agent's words (#1360: above
    /// the composer it covered the end of the reply): `recapInFlow`; closed, it is the one-line marker again.
    private var pendingRecap: Item? {
        guard style == .dev, case .row(.recap(let i))? = model.chat(key).blocks.last else { return nil }
        return closedRecap == i.id ? nil : i
    }

    @StoredValue("closedRecaps") private var closedRecapsData = Data()
    private var closedRecap: String? {
        (try? JSONDecoder().decode([String: String].self, from: closedRecapsData))?[key.machineID + "/" + key.sessionID]
    }

    /// The open recap's line: the earlier recaps folded with it as their chip, then the newest in full.
    private func recapInFlow(_ open: ChatLines.OpenRecap) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if !open.earlier.isEmpty { RecapCard(items: open.earlier) }
            RecapStrip(item: open.newest) { dismissRecap(open.newest, line: open.line) }
        }
        .transition(.opacity)
        .chatPinned("recap-" + open.line)
    }

    /// Closes the open recap; tapped (`line`: its line), the line stays where it is in the chat, the marker it folds to
    /// in its place (#3757).
    private func dismissRecap(_ recap: Item, line: String? = nil) {
        var all = (try? JSONDecoder().decode([String: String].self, from: closedRecapsData)) ?? [:]
        all[key.machineID + "/" + key.sessionID] = recap.id
        if all.count > 300 { all = Dictionary(uniqueKeysWithValues: all.suffix(200).map { ($0.key, $0.value) }) }
        let close = { closedRecapsData = (try? JSONEncoder().encode(all)) ?? Data() }
        if let line { toggleInPlace(pin, "recap-" + line, animation: Theme.Motion.standard, closing: true, close) } else { withAnimation(Theme.Motion.standard, close) }
    }

    /// Switches the chat to a subagent (nil: back to the main agent); its transcript streams like a session's.
    private func view(_ agent: Subagent?) {
        guard agent?.id != viewing?.id else { return }
        Haptics.tap()
        if viewing != nil { model.unsubscribe(shownKey, by: owner) }
        withAnimation(Theme.Motion.standard) { viewing = agent }
        if agent != nil { model.subscribe(shownKey, by: owner) }
        setFollow { $0.follow() }
        frozen = nil
        scrollToEnd()
    }

    /// The list's rows, newest first: apart from `chatScroll`, which as one expression Swift 6.2 could not type-check in time.
    private func chatRows(store: ChatStore, lines: [ChatLine], pending: [PendingMessage]) -> some View {
        let rows = store.rows
        let newestID = lines.last?.id
        return LazyVStack(alignment: .leading, spacing: Theme.Space.l) {
            // Newest first (the list is drawn upside down): the end sentinel, live text, unsent messages, lines (a failed
            // message among them, before the first line written after it).
            // It takes no room: past it come only the rows' spacing and the list's room at its end (`endRoom`).
            Color.clear
                .frame(height: Theme.Size.line)
                .padding(.bottom, -Theme.Size.line)
                .onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .named(ChatPin.space)).minY }) { y in startPlaced(at: y) }
                .id(Self.bottomID)
            // A voice call's captions, the thread's last rows while it is bound here (docs/assistant/voice.md 6.2): they read the
            // call themselves; the page only asks whether one is bound.
            if viewing == nil, let voice = model.voice, voice.isBound(to: key) {
                voice.threadTail(key: key, held: frozen != nil)
                    .flippedBack()
                    .id("voice-tail")
            }
            if let err = store.error {
                Label(err, systemImage: Theme.Icon.error).font(Theme.Font.label).foregroundStyle(Theme.danger).flippedBack()
            }
            // Streamed text, the typing line and unsent messages sit at the newest end too: while you read
            // history they wait with the rest, as they were drawn then (`Frozen`).
            if drawsLive, !ChatLines.hidesLiveSupervision(lines, open: openSupervision, openAll: Self.debugOpenSupervision) {
                ChatLiveText(store: store, held: frozen?.text, hidesBotmuxSentinel: isBotmuxSession && botmuxMode == .decoded)
            }
            // Sent, and the agent not on it yet: the line the new-conversation page showed under the first message
            // (`FirstMessagePending`), where the turn's work line comes in. Without it the chat that took over showed
            // the message a line lower, then pushed it up when the turn began (#1911).
            if frozen == nil, let phase = sendPhase, phase == .sending || liveTurnID == nil {
                TypingIndicator(text: phase.label)
                    .flippedBack()
                    .id("send-feedback")
                    #if DEBUG
                    .onAppear { store.traceSendFeedback(phase) }
                    .onChange(of: phase) { _, phase in store.traceSendFeedback(phase) }
                    #endif
            } else if frozen == nil, startingTurn(pending) {
                TypingIndicator(text: String(localized: "\(style == .assistant ? model.assistantShownName : (session?.agentLabel ?? "Claude")) is getting ready…"))
                    .flippedBack()
                    .id("starting-turn")
            }
            // Sent from here, not in the transcript yet (ones Claude queued show in the queue strip instead). One that
            // failed with messages sent after it sits before the first of them, among the lines below (#6023). While
            // history is read (`Frozen`) each stays where it was drawn when the freeze began: nothing moves under the
            // reader then (#1243).
            let placed = frozen.map { f in
                ChatLines.placeFrozen(lines, pending.filter { f.pending.contains(ChatLines.frozenKey($0)) }, at: f.failedAt)
            }
                ?? ChatLines.placeFailed(lines, pending, writtenAt: store.writtenAt)
            ForEach(placed.rest.reversed()) { msg in
                pendingBubble(msg)
                    #if os(iOS)
                    .allowsHitTesting(!pick.on)
                    #endif
            }
            // The row carries kept messages to every device, even when the transcript has no earlier turns.
            let kept = keptUndelivered
            ForEach(kept.reversed()) { m in
                let target = m.heldForComputer ? model.heldTarget(key, m.id, row: session) : nil
                let offer = HeldRetryOffer(target, m)
                UndeliveredBubble(message: m, canManage: !m.heldForComputer || target != nil, retryDisabled: model.heldRetryDisabled(offer),
                                  retry: { Task { await model.retryUndelivered(key, m.id, shown: offer) } },
                                  discard: { Task { await model.deleteUndelivered(key, m.id, target: target) } })
                    .chatMark("chat.undelivered.\(m.state).\(m.id)")
                    #if os(iOS)
                    .allowsHitTesting(!pick.on) // not sent yet: never picked, and quiet while selecting
                    #endif
                    .flippedBack()
                    .id("undelivered-" + m.id)
            }
            // The first message of a conversation, not in the transcript yet: its day over it, as the new-conversation
            // page drew it (`FirstMessagePending`) and the transcript's lines will (#1911).
            if style == .assistant, lines.isEmpty, !pending.isEmpty { DayLine(date: .now).flippedBack() }
            // Every message, timer line, tool count and answer is its own lazy row (a running turn can hold
            // hundreds of steps; one stack of them froze the page).
            let openRecap = ChatLines.openRecap(lines, pending: viewing == nil ? pendingRecap : nil)
            ForEach(lines.reversed()) { line in
                Group {
                    if let openRecap, openRecap.line == line.id { recapInFlow(openRecap) } else { lineView(line) }
                }
                    #if os(iOS)
                    .pickable(Self.pickBar(line), mark: Self.pickMark(line), pick: pick, selecting: pick.on, refused: pickRefused)
                    #endif
                    .background { if let flash = quoteFlash, flash.line == line.id { QuoteFlash(hold: flash.hold).id(flash.id) } }
                    .flippedBack()
                    .debugLineY(line.id, scratch)
                    // In the list's content, which scrolling does not change: reported only when the stack places it.
                    .onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .named(ChatPin.space)).minY }) { y in
                        if line.id == newestID { newestPlaced(line.id, at: y) }
                    }
                    .landingFrame(line.id == landing, scratch)
                    .id(line.id)
                    .zIndex(menu.selected != nil && line.holds(menu.selected) ? 1 : 0)
                // Upside down: drawn after the line is drawn above it, where it was written.
                ForEach((placed.before[line.id] ?? []).reversed()) { msg in
                    pendingBubble(msg)
                        #if os(iOS)
                        .allowsHitTesting(!pick.on) // not sent yet: never picked, and quiet while selecting
                        #endif
                }
            }
            switch ChatLines.placeholder(lines: lines, hasTranscript: !backgroundActivity && !rows.isEmpty,
                                         hasPending: !kept.isEmpty || !pending.isEmpty || !store.pending.isEmpty || !store.arriving.isEmpty,
                                         hasLiveText: drawsLive && !(frozen?.text ?? store.liveText).isEmpty,
                                         loadedOnce: store.loadedOnce, historyNotFound: store.historyNotFound) {
            case .loading:
                ChatSkeleton().flippedBack().accessibilityIdentifier("chat-loading-placeholder")
            case .empty:
                if backgroundActivity {
                    EmptyStateView(symbol: Theme.Icon.recent, title: String(localized: "No background activity"),
                                   message: String(localized: "Completed quiet rounds stay here for you to inspect."))
                        .flippedBack().accessibilityIdentifier("background-activity-empty")
                } else if model.session(key)?.isStopped == true { // read-only (#4754): nothing to start from here
                    EmptyStateView(symbol: Theme.Icon.conversation, title: String(localized: "No messages"),
                                   message: String(localized: "This Agent stopped before it left any messages."))
                        .flippedBack().accessibilityIdentifier("chat-empty-placeholder")
                } else if model.session(key)?.awaitingFirstMessage == true {
                    // A Codex opened with no first message (docs/integrations/codex.md「Standalone TUI takeover」):
                    // it waits for one, sent from here or typed in its terminal. Said once, here, as the web's note.
                    let computer = model.machine(key.machineRef)?.title ?? key.machineID
                    EmptyStateView(symbol: Theme.Icon.conversation, title: String(localized: "No messages yet"),
                                   message: String(localized: "Codex is open on \(computer). Send its first message below, or type it in its terminal."))
                        .flippedBack().accessibilityIdentifier("chat.codexWaiting")
                } else {
                    EmptyStateView(symbol: Theme.Icon.conversation, title: String(localized: "No messages yet"), message: String(localized: "Send a message to get started."))
                        .flippedBack().accessibilityIdentifier("chat-empty-placeholder")
                }
            case .none: EmptyView()
            }
            if store.hasMore {
                // Reaching the far (oldest) end loads the page before it — only once the user has dragged the list.
                ProgressView()
                    .controlSize(.small)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, Theme.Space.sm)
                    .task(id: "\(rows.first?.id ?? "")|\(userScrolled)") { if userScrolled { await model.loadOlder(shownKey) } }
            }
        }
        .scrollTargetLayout()
        .coordinateSpace(.named(ChatPin.space))
        .background { ChatPin.Finder(pin: pin) }
    }

    /// A message sent from here, not in the transcript yet: its bubble with Retry / Discard when it failed.
    private func pendingBubble(_ msg: PendingMessage) -> some View {
        PendingBubble(msg: msg, retry: { model.retry(key, msg) }, discard: { model.discard(key, msg) })
            .chatMark("chat.outbox.\(msg.state).\(msg.id.uuidString)")
            .flippedBack()
            .id(msg.id.uuidString)
    }

    private func chatScroll() -> some View {
        let store = store
        let lines = currentLines()
        let pending = shownPending
        let under = underFooter
        // Two statements: as one expression Swift 6.2 could not type-check it in time.
        let list = ScrollView {
            chatRows(store: store, lines: lines, pending: pending)
        }
        .id(listBuild) // built again after a fold that left it drawing nothing (`listBuild`)
        // Side margins as scroll content margins, not padding: jumping to a row aligns its leading edge with the
        // margin instead of the screen edge (padding shifted the page 16pt sideways after landing on a row).
        .contentMargins(.horizontal, 16, for: .scrollContent)
        .onScrollTargetVisibilityChange(idType: String.self) { ids in
            scratch.visibleLines = ids
            let shown = latestPlan.map { p in ids.contains { $0.hasSuffix("/" + p.callID) } } ?? false
            if planLineShown != shown { planLineShown = shown }
        }
        // Any part of a row on screen: `visibleLines` counts a row once half of it is, and a line taller than two
        // screens never is. None at all after a landing is a blank page (`mendBlankLanding`).
        .onScrollTargetVisibilityChange(idType: String.self, threshold: 0.01) { ids in
            scratch.drawnLines = ids
            // The oldest dozen lines start loading the page before them: by the time you reach the top it is usually
            // there (pages with screenshots run to megabytes and take seconds). Here, not an `onAppear` on every row:
            // each row the lazy list made while scrolling then ran an action and an update of the page for nothing.
            if lines.prefix(12).contains(where: { ids.contains($0.id) }) { prefetchOlder() }
        }
        .flippedBack() // the scroll view itself upside down; every row above is flipped back
        // Safe areas would land on the wrong ends of a flipped scroll view: ignore them and add the room ourselves.
        // (Content top = the composer side on screen, content bottom = the navigation side.) The newest end has the
        // room that says the conversation ends there (`endRoom`); it scrolls with the rows, so a little way up they still
        // go right down to the footer.
        // The header floats over the list (Agents): the oldest end leaves its full height free, so nothing hides
        // under it; the list's own frame never changes as it folds, so folding never moves the messages.
        // Where the list runs on behind the footer (`underFooter`) its newest end has that much more room: every message
        // rests where it would on a list ending at the footer, and the offset past the inset (`newestGap`) is the same.
        .contentMargins(.top, Self.endRoom + under, for: .scrollContent)
        .contentMargins(.bottom, headerRoom + 10, for: .scrollContent)
        .scrollIndicators(.hidden)
        // iOS 26's soft edge blur works out its region on the unflipped view and fogged the whole list.
        .scrollEdgeEffectHiddenWhenSupported(true, for: .all)
        .scrollPosition($position)
        // The part over the footer: what the page shows of the list, as the follow, the landings and the navigator take it.
        .onGeometryChange(for: CGRect.self, of: { g in
            var f = g.frame(in: .global)
            f.size.height = max(0, f.height - under)
            return f
        }) { rect in
            let old = scratch.listFrame
            scratch.listFrame = rect
            pin.riseTop = riseTop
            // Drawn upside down from its bottom edge, the list moves everything on it when that edge moves: what sits
            // under it came or went (the queue strip taken as the next turn started moved the rows the user had open
            // 60 pt down, #3005). The pin puts a held row back. Only that edge: its top edge moving moves nothing on
            // it, and going by the list's height the pin moved a work line's header 40 pt by itself (`UnfoldTests`).
            // Not for the keyboard: the newest stays above it, as it always has.
            if old.height > 0, !KeyboardState.shared.isUp { pin.resized(by: rect.maxY - old.maxY) }
        }
        .scrollDismissesKeyboard(.interactively)
        .simultaneousGesture(TapGesture().onEnded { menu.dismiss(); Keyboard.dismiss() })
        // Only the user's own scrolling closes the bar; content streaming in also moves the offset (.animating).
        .onScrollGeometryChange(for: ListScroll.self, of: { ListScroll(offset: $0.contentOffset.y, height: $0.containerSize.height - under, x: $0.contentOffset.x, top: $0.contentInsets.top, content: $0.contentSize.height) }) { old, new in
            scratch.offsetX = new.x
            #if DEBUG
            scratch.scrollY = new.offset
            scratch.contentHeight = new.content
            #endif
            // A work line folded with hundreds of points of it open, and what is left fits on one screen: the scroll view
            // put its offset back inside the shorter content (from far up to the newest end) and the upside-down lazy
            // list drew nothing there, ever: the content area blank, nothing to drag, no new line arriving in a held-back
            // chat to lay it out again, until the app was killed (#686). It is built again; all of it fits, so nothing
            // of where it was is lost. Nothing above to read any more either: it follows again.
            if new.content < old.content, new.content <= new.height, old.content > old.height,
               Date.now.timeIntervalSince(scratch.foldedAt) < 1.5, !scratch.userDragging {
                listBuild += 1
                setFollow { $0.follow() }
            }
            scratch.atNewest = new.offset <= -new.top + 0.5
            scratch.newestGap = new.offset + new.top
            setFollow { $0.scrolled(gap: scratch.newestGap) }
            chrome.offset = new.offset
            // A height change (keyboard) moves the offset too: that is not the user scrolling.
            if old.height != new.height { return }
            scrolled(by: new.offset - old.offset)
        }
        .onScrollPhaseChange { old, phase in
            // The user's own scrolling: a drag, and the glide after a flick. Deceleration alone is not proof — landing
            // on the entry line produced it with no finger down and folded the chrome on open.
            if phase != .idle { scratch.lastActivity = .now }
            #if DEBUG
            scratch.phase = "\(old)>\(phase)"
            #endif
            let flick = phase == .decelerating && (old == .tracking || old == .interacting || scratch.userDragging)
            #if DEBUG
            // The user's flick glides on: UI tests that tap during the glide wait for this (`TouchScript`, #3027).
            if flick, old != .decelerating, ScreenProbe.enabled { notify_post(Self.glideNote) }
            #endif
            scratch.userDragging = phase == .tracking || phase == .interacting || flick
            if phase == .idle, old != .idle, old != .animating {
                settleChrome() // the finger lifted, the glide ended
                // Where the user's scrolling stopped decides (nothing if it was the app's). A tap (down and up, the list
                // never scrolled) is no scrolling: deciding there ended the hold on a row just opened (#3005).
                setFollow { if old == .tracking { $0.tapped() } else { $0.released() } }
                mendMisplacedStartSoon()
            }
            // On this upside-down list (`flippedBack`) `.scrollDismissesKeyboard(.interactively)` never put the keyboard
            // away while scrolling (#1823). A real drag does it; a touch that may still be a tap (.tracking) does not, so
            // a field inside the list ("Other answer…") keeps the keyboard it opens.
            if phase == .interacting { Keyboard.dismiss() }
            if phase == .tracking || phase == .interacting {
                scratch.lastUserScroll = .now
                menu.dismiss()
                entry = nil // the user scrolls on their own: no landing on the saved place after this
                userScrolled = true
                setFollow { $0.touched() } // nothing changes until the finger lets go
            }
        }
        // In parts: as one expression Xcode 26.0 could not type-check it in time (#2571), and Xcode 26.3 not even in two (#2963).
        let base = list
        .environment(menu)
        .environment(\.chatPin, pin)
        #if os(iOS)
        .background {
            FullPageEnvironmentReader(page: fullPage)
            FullPageAnchor { [fullPage] in fullPage.setAnchor($0) }
        }
        #endif
        let hooked = base
        .onAppear {
            pin.letGo = { [position = $position] in position.wrappedValue = ScrollPosition(idType: String.self) }
            pin.onRising = { [rising = $pinRising] in rising.wrappedValue = $0 }
            pin.toggled = { [follow = $follow, scratch] closing in
                var next = follow.wrappedValue
                let how = next.toggledRow(closing: closing, atNewest: scratch.atNewest)
                if next != follow.wrappedValue { follow.wrappedValue = next }
                return (keep: how != .moment, rise: how == .rising)
            }
            pin.landed = { [follow = $follow, pin] in
                var next = follow.wrappedValue
                guard next.anchored else { return }
                next.follow()
                follow.wrappedValue = next
                pin.release()
            }
            #if DEBUG && (os(iOS) || os(visionOS))
            // The page measurements move the list's scroll view themselves (`PagePerf`): tell the chat a finger is on
            // it, as a drag does (`onScrollPhaseChange`), or it keeps following the newest end and puts its stored
            // position back whenever rows come in, and the list never gets far (#4970).
            PagePerf.touchers[ObjectIdentifier(pin)] = { [pin] list, down in
                guard pin.list === list else { return }
                if down {
                    scratch.userDragging = true
                    scratch.lastUserScroll = .now
                    entry = nil
                    userScrolled = true
                    setFollow { $0.touched() }
                    pin.letGo()
                    pin.release() // as a finger lets go of a kept header: it would put the list back (`ChatPin.keepHeld`)
                } else {
                    scratch.userDragging = false
                    setFollow { $0.released() }
                }
            }
            #endif
        }
        #if DEBUG
        .onAppear {
            guard ScrollTrace.enabled else { return }
            ScrollTrace.shared.start(list: { [pin] in pin.list }) { [store, scratch, pin, follow = $follow, frozen = $frozen, position = $position] in
                Self.traceLine(follow: follow.wrappedValue, frozen: frozen.wrappedValue, scratch: scratch, store: store,
                               position: position.wrappedValue) + " sv \(pin.traceFrame) pin \(pin.traceHeld)"
            }
        }
        #endif
        // The sheets and the rest of the chain start from an erased list (#9709, `MainPagesStackTests`): as one type,
        // the list's chain with its scroll view and rows was about a hundred levels deep, and the chat pane's first
        // render built its runtime metadata on top of the page's getters, 515 KB deep in a Debug build.
        let presented = hooked.stackSegment()
        .sheet(item: $menu.sharing) { m in MessageShareSheet(message: m) }
        #if os(iOS)
        .sheet(item: $menu.forwarding, onDismiss: forwardDone) { d in ForwardSheet(draft: d) { forwardPicked = $0 } }
        .sheet(item: $exporting) { r in ChatExportSheet(request: r, pick: pick).presentationDetents([.large]) }
        .sheet(item: $forwardNewAgent) { f in
            NewChatView(contextBlock: f.refs.map(\.xml).joined(separator: "\n"), forwarded: f.refs, forwardedImages: f.images,
                        onlyGroup: key.group) { s in
                forwardNewAgent = nil
                model.open(s.key)
            }
        }
        #endif
        .accessibleConfirmationDialog("This Reply", isPresented: Binding(get: { menu.more != nil }, set: { if !$0 { menu.more = nil } })) {
            if let id = menu.more {
                // A new conversation with what was said up to here (#2137). Opened once the dialog is gone: a navigation
                // made while it is still going away is dropped, and the chat stayed on the original.
                Button("Branch to New Chat") {
                    Task {
                        try? await Task.sleep(for: .milliseconds(400))
                        menu.fork?(id)
                    }
                }
            }
        }
        .rewindDialogs(menu: menu)
        return presented
        // New content while following: back to offset 0 (exact; nothing to estimate).
        .onChange(of: store.resets) { _, _ in frozen = nil } // the snapshot's items are gone
        .onChange(of: follow.holdsBack) { _, holds in
            ChatTrace.shared.note(shownKey, "\(holds ? "hold back" : "let go") mode=\(follow.mode) near=\(follow.near ? 1 : 0) touching=\(follow.touching ? 1 : 0)")
            if !holds {
                frozen = nil
            } else if let last = store.items.last {
                let failed = ChatLines.placeFailed(currentLines(), store.pending, writtenAt: store.writtenAt).before
                frozen = Frozen(seq: last.seq, part: last.part, live: liveTurnID,
                                text: drawsLive && liveShown ? store.liveText : "",
                                pending: Set(store.pending.map(ChatLines.frozenKey)), failedAt: ChatLines.failedLines(failed))
            }
        }
        .onChange(of: store.rows.last?.id) { _, _ in contentArrived(foldChrome: false) }
        .onChange(of: lines.count, initial: true) { _, _ in landEntry(lines) }
        .onChange(of: store.backlogBuilt) { _, _ in landEntry(lines) }
        .onChange(of: liveTurnID) { _, _ in contentArrived(foldChrome: false) }
        .task(id: awaitDeadline) {
            guard let end = awaitDeadline, end > .now else { return }
            try? await Task.sleep(for: .seconds(end.timeIntervalSinceNow + 0.1))
            if !Task.isCancelled { awaitTick &+= 1 }
        }
        // Following the newest as it streams in folds the chrome too (once the user has taken the list).
        // Not while a tapped header is held in place (`ChatPin`): opening a work line loads its steps.
        .onChange(of: store.items.count) { _, _ in contentArrived(foldChrome: true) }
        .modifier(ChatStreamFollow(store: store) { contentArrived(foldChrome: true) })
        .background { model.voice?.tailFollower(key: key) { contentArrived(foldChrome: false) } } // a voice call's captions, likewise
        // Your own message brings you to the newest — only yours (one typed on the computer, or sent by another session,
        // used to pull you out of the history you were reading too), and only if you have not scrolled since sending it:
        // Claude can take it in seconds later, while you went back up to read (2026-10-30).
        .onChange(of: lastPromptID) { old, new in
            guard old != nil, let new, let item = store.items.last(where: { $0.id == new }), let text = item.text,
                  let i = scratch.sentHere.firstIndex(where: { PendingMessage.tied(item, to: $0.id) ?? PendingMessage.same(text, $0.text) })
            else { return }
            scratch.sentHere.remove(at: i)
            guard scratch.lastUserScroll < scratch.sentAt else { return }
            setFollow { $0.follow() }
            scrollToEnd()
        }
        .onChange(of: store.pending.count) { old, new in
            guard new > old else { return }
            scratch.sentHere = Array((scratch.sentHere + store.pending.map { (id: $0.id, text: $0.text) }).suffix(20))
            scratch.sentAt = .now
            setFollow { $0.follow() }
            scrollToEnd(animated: true)
        }
        // Parse what rows will show (Markdown, paths) in the background as messages load: scrolling then only lays out.
        .task(id: prewarmKey) { await prewarmRows() }
    }

    #if DEBUG
    /// Posted (Darwin notification) when a flick of the user's starts to glide, with `-probes YES`.
    static let glideNote = "dev.persona.test.chat-glide"

    /// One `ScrollTrace` state line. Out of the modifier chain: inline, its interpolation helped the chain time out (#2963).
    private static func traceLine(follow: ChatFollow, frozen: Frozen?, scratch: ChatScratch, store: ChatStore,
                                  position: ScrollPosition) -> String {
        let mode = "\(follow.mode)\(follow.anchored ? "+anchored" : "") near \(follow.near ? 1 : 0) touch \(follow.touching ? 1 : 0) drag \(scratch.userDragging ? 1 : 0)"
        let held = "frozen \(frozen == nil ? 0 : 1) ftext \(frozen?.text.count ?? -1)"
        let list = " list \(Int(scratch.listFrame.minY))-\(Int(scratch.listFrame.maxY)) ph \(scratch.phase)"
        return "\(mode) \(held) live \(store.liveText.count) items \(store.items.count) gap \(Int(scratch.newestGap)) jumps \(scratch.endJumps)"
            + list + " pos \(traced(position)) slips \(scratch.jumpSlips)"
    }

    /// What SwiftUI's stored position holds, for the trace: an edge, a point, a row's id, or nothing; `user` when the
    /// user's own scrolling put it there (#4788).
    private static func traced(_ p: ScrollPosition) -> String {
        let at = if let e = p.edge { "edge:\(e)" } else if let pt = p.point { "pt:\(Int(pt.y))" }
            else if let id = p.viewID(type: String.self) { "id:\(id.suffix(12))" } else { "none" }
        return at + (p.isPositionedByUser ? "/user" : "")
    }
    #endif

    // Out of the modifier chain: inline, Swift 6.3 timed out type-checking the Release build.
    private var prewarmSource: String { "\(shownKey.machineID)/\(shownKey.sessionID)|\(store.resets)" }
    /// In the background it changes too, so leaving cancels a prewarm between rows and coming back runs it again.
    private var prewarmKey: String { "\(prewarmSource)|\(store.version)|\(RowMemo.prewarms(in: scenePhase))" }

    private func prewarmRows() async {
        // Not while the app is in the background (#9108): a recording may be keeping it running, under iOS's CPU limit.
        guard RowMemo.prewarms(in: scenePhase) else { return }
        let source = prewarmSource
        if scratch.prewarmSource != source {
            scratch.prewarmSource = source
            scratch.prewarmedIDs = []
        }
        let items = store.items.filter { !scratch.prewarmedIDs.contains($0.id) }
        guard !items.isEmpty else { return }
        let scope = RowMemo.scope(shownKey)
        let warm = Task.detached(priority: .utility) { RowMemo.prewarm(items, scope: scope) }
        await withTaskCancellationHandler {
            await warm.value
        } onCancel: {
            warm.cancel()
        }
        if !Task.isCancelled { scratch.prewarmedIDs.formUnion(items.map(\.id)) }
    }

    /// The flipped list's offset grows toward older messages. Like Safari's toolbar, the chrome follows the finger:
    /// reading on (toward the newest) folds it as far as the finger goes, going back into the history unfolds it.
    /// Idle time before the open chrome folds itself.
    #if DEBUG
    /// `-openSendMessage` went out this launch.
    static var debugSent = false
    #endif

    static var chromeIdleSeconds: TimeInterval {
        #if DEBUG
        // `-holdChromeOpen YES`: the header never folds by itself (UI tests that tap in it).
        if UserDefaults.standard.bool(forKey: "holdChromeOpen") { return .infinity }
        #endif
        return 3
    }

    /// Something is up over the page (a sheet from the header — the space's tabs —, its ⋯ menu): the header waits. Folded,
    /// it leaves the view tree (`ChromeHeader`), and what it presented would go with it.
    static var somethingPresented: Bool {
        UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.flatMap(\.windows)
            .contains { $0.isKeyWindow && $0.rootViewController?.presentedViewController != nil }
    }

    private func scrolled(by delta: CGFloat) {
        guard style == .dev, pane == .chat, scratch.userDragging, delta != 0 else { return }
        scratch.lastActivity = .now
        let folded = min(max(chrome.collapse - delta, 0), chrome.maxCollapse)
        guard folded != chrome.collapse else { return }
        chrome.collapse = folded
        if folded > 0, overviewUI.showOverview { overviewUI.showOverview = false } // the sheet goes with the chrome
    }

    /// The finger lifted: never stay half folded. At the newest the chrome folds (the page is all chat); elsewhere
    /// it goes to whichever end is nearer.
    private func settleChrome() {
        guard style == .dev, pane == .chat else { return }
        let full = chrome.maxCollapse
        if chrome.offset < 40 {
            hideChrome()
        } else if chrome.collapse > 0, chrome.collapse < full {
            withAnimation(Theme.Motion.layout) { chrome.collapse = chrome.collapse > full / 2 ? full : 0 }
        }
    }

    private func hideChrome() {
        guard style == .dev, pane == .chat, chrome.collapse < chrome.maxCollapse else { return }
        withAnimation(.smooth(duration: 0.35)) { chrome.collapse = chrome.maxCollapse }
        overviewUI.showOverview = false
    }

    private func showChrome() {
        scratch.lastActivity = .now // shown on purpose: give it the full idle time before folding again
        guard chrome.collapse > 0 else { return }
        withAnimation(.smooth(duration: 0.35)) { chrome.collapse = 0 }
    }

    private var compactTitleControl: some View {
        Button { Haptics.tap(); showChrome() } label: {
            HStack(spacing: Theme.Space.sm) {
                // Out of reach: the slim bar says so too (the open header's subtitle explains on a tap).
                if model.connection.shown != .online {
                    ProgressView().controlSize(.mini)
                    Text(ConnectionLabel.text(model.connection.shown, hubRestarting: model.hubUpdate?.restarting == true)).lineLimit(1)
                } else {
                    if let s = session {
                        if !s.assistant { AgentMark(s, side: Theme.Size.markXS) } // its agent before its state (#6390)
                        StatusDot(state: s.state, size: 7, waiting: s.waitsInBackground)
                    }
                    Text(compactTitle).lineLimit(1).truncationMode(.tail)
                }
            }
            .animation(Theme.Motion.standard, value: model.connection.shown)
            .font(Theme.Font.captionStrong)
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(height: Theme.Size.wellXS)
            .background(Theme.Fill.chip, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(model.connection.shown != .online ? String(localized: "\(ConnectionLabel.text(model.connection.shown, hubRestarting: model.hubUpdate?.restarting == true)), show title and tags")
                            : String(localized: "\(compactTitle), show title and tags"))
        .accessibilityIdentifier("chat.compactTitle")
        .titleSaysLink() // the slim bar says Connecting… too: the shell's floating pill does not say it again
    }

    /// The chrome folded away: back, the session's name as the open header says it — tap it for everything again,
    /// where it runs included — and the open header's ⋯.
    private var compactChrome: some View {
        HStack(spacing: 0) {
            Button { back() } label: {
                ThemedIcon(backSymbol).font(Theme.Font.secondaryStrong).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .contentShape(.rect) // hit like the ⋯ (#9699)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(backLabel)
            .modifier(SidebarButtonID(active: backShowsList))
            .noBackWhen(hidesBack)
            Spacer(minLength: 0)
            titleMenu(compactTitleControl)
            Spacer(minLength: 0)
            // The open header's ⋯ and its menu (#7962, the user's call; #3719 had left this spot empty). As wide as ‹,
            // so the capsule stays in the middle and no narrower; the open ⋯'s square to hit. Drawn bare like ‹: the
            // bar is 40 pt, no room for the open header's 44 pt glass. The goal, the loop and background work and the
            // mode are in the open header, a tap away.
            moreMenu {
                ThemedIcon(Theme.Icon.more).font(Theme.Font.secondaryStrong).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                    .contentShape(.rect)
            }
        }
        .padding(.horizontal, Theme.Space.xs)
        .frame(height: ChatChrome.compactHeight)
    }

    /// A goal, a loop or background work: a small mark each in a hosted header's one row (#3255).
    private var hostedMarks: some View {
            HStack(spacing: 0) {
                if let goal = session?.goal {
                    Button { Haptics.tap(); showGoal = true } label: {
                        ThemedIcon(Theme.Icon.goal)
                            .font(Theme.Font.labelHeavy)
                            .foregroundStyle(goal.tint)
                            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellM)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Goal, \(goal.statusLabel): \(goal.headline). Tap to view.")
                    .sheet(isPresented: $showGoal) { GoalSheet(goal: goal) }
                }
                if session?.loop != nil || !(session?.background ?? []).isEmpty {
                    Button { Haptics.tap(); overviewUI.openOverview(focus: .background) } label: { activityMark }
                        .buttonStyle(.plain)
                        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellM)
                        .accessibilityLabel("Loop and background tasks, tap to view")
                }
            }
    }

    /// A hosted header's sign of a loop / background tasks: the count by a neutral gear, or the loop arrows. As the open
    /// header's pill (`ActivityChips`), it says how many, not the state: the title's line says that once (#3719).
    private var activityMark: some View {
        let bg = session?.background?.count ?? 0
        return HStack(spacing: Theme.Space.xxs) {
            ThemedIcon(bg > 0 ? ActivityChips.backgroundSymbol : ActivityChips.loopSymbol)
                .font(Theme.Font.labelHeavy)
            if bg > 0 { Text("\(bg)").font(Theme.Font.labelHeavy.monospacedDigit()) }
        }
        .foregroundStyle(bg > 0 ? Color.secondary : Theme.loop)
        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellM)
        .contentShape(.rect)
    }

    /// The "…" menu, in the custom header (Agents) or the system bar (assistant).
    /// Opens the review of this session's changes, when the hub reviews (`git_review`) and the session has a folder; nil
    /// on the Mac (frozen) and elsewhere. `nil` scope: the one the session last used.
    private var openReview: ((GitReviewScope?) -> Void)? {
        #if os(iOS)
        guard style == .dev, model.features(key.group).contains("git_review"), let cwd = session?.cwd, !cwd.isEmpty else { return nil }
        return { scope in
            actions.pendingReviewFallback = nil
            reviewRequest = ReviewRequest(scope: scope)
        }
        #else
        return nil
        #endif
    }

    #if os(iOS)
    private func reviewPage(_ r: ReviewRequest) -> some View {
        let sessionKey = key
        let cwd = session?.cwd ?? ""
        return ReviewPage(key: sessionKey, cwd: cwd, scope: r.scope, targetFile: r.targetFile,
                          onFallbackFile: { path in
                              actions.pendingReviewFallback = (
                                  sessionKey: sessionKey,
                                  ref: FileRef(group: sessionKey.group, machineID: sessionKey.machineID, path: path, cwd: cwd)
                              )
                          },
                          api: { [model, sessionKey] in model.api(sessionKey.group) })
            .environment(model)
            .presentationSizing(.page) // the iPad's too: room for the diffs, not the small form
    }

    private func handleReviewDismiss() {
        guard let pending = actions.pendingReviewFallback else { return }
        actions.pendingReviewFallback = nil
        guard pending.sessionKey == key else { return }
        file = pending.ref
    }
    #endif

    /// ⋯ and its menu, in the open header and in the folded slim bar (#7962): one menu, so an item added to
    /// `moreMenuItems` shows in both. `label` is how each draws the button (the open one in glass, the slim bar's bare).
    private func moreMenu<Look: View>(@ViewBuilder label: () -> Look) -> some View {
        #if os(iOS)
        Menu(content: { moreMenuItems }, label: label)
            .labelOnlyMenu()
            .accessibilityLabel("More")
            .accessibilityIdentifier("chat.more")
        #else
        Menu(content: { moreMenuItems }, label: label)
            .labelOnlyMenu()
            .accessibilityLabel("More")
            .accessibilityIdentifier("chat.more")
        #endif
    }

    @ViewBuilder private var moreMenuItems: some View {
        if isBotmuxSession { BotmuxModePicker(mode: $botmuxMode) }
        #if os(iOS)
        if style == .dev, let host { hostedMenuItems(host) }
        if style == .dev, let session {
            // Not the header's to observe: the facts read the machines, links and pins (#10231).
            AnyView(AgentActionMenuReader(session: session, currentPane: pane, hosted: host != nil, showsSpace: openSpace != nil,
                                          sides: sides, handlers: agentMenuHandlers)).stackSegment()
        } else if session?.canAttach == true {
            Button { Task { await model.attach(key) } } label: { Label("Attach Live", systemImage: Theme.Icon.link).lineLimit(1) }
        }
        if style == .assistant {
            if status.isActive, session?.canInterrupt == true {
                Button { Task { await model.interrupt(key) } } label: { Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt) }
            }
            ModuleViews(place: .chatMenu, context: .init(recent: computerActivity?.machine, from: key))
            if let s = session {
                Button { UIPasteboard.general.string = s.cwd } label: { Label("Copy Directory", systemImage: Theme.Icon.folder) }
                if s.status.isLive && s.canClose {
                    Button(role: .destructive) { closing = s } label: { Label("Stop and Close Agent…", systemImage: Theme.Icon.end).lineLimit(1) }
                }
                if s.canDelete {
                    Button(role: .destructive) { deleting = s } label: { Label("Delete Chat…", systemImage: Theme.Icon.delete).lineLimit(1) }
                }
            }
        }
        #else
        legacyMoreMenuItems
        #endif
    }

    #if !os(iOS)
    @ViewBuilder private var legacyMoreMenuItems: some View {
            if style == .dev, let host { hostedMenuItems(host) }
            if let s = session, let todo = s.todo,
                TodosLogic.isOnTodosHub(sessionGroup: s.key.group, hubGroup: model.hub?.group) {
                Button {
                    showTodoDetail = true
                } label: {
                    Label(todo.title, systemImage: Theme.Icon.todo)
                }
            }
            if style == .dev {
                if session?.canTerminal == true {
                    Button { openTerminal() } label: { Label("Terminal", systemImage: Theme.Icon.terminal).lineLimit(1) }
                }
                #if os(iOS)
                // Its computer is read only for a session that isn't running: a live chat never watches the machines (P5).
                if let s = session, s.status == .offline || s.status == .unknown { resumeMenuItem(s) }
                #endif
                if pane != .chat {
                    Button { withAnimation(Theme.Motion.standard) { pane = .chat } } label: { Label("Back to Chat", systemImage: SessionPane.chat.symbol) }
                }
                ForEach(panes.filter { $0 == .files || $0 == .git }) { p in
                    Button { withAnimation(Theme.Motion.standard) { pane = p } } label: {
                        Label(paneBadges[p].map { String(localized: "\(p.title) (\($0))") } ?? p.title, systemImage: p.symbol)
                    }
                }
                #if os(iOS)
                if let open = openReview {
                    Button { open(nil) } label: { Label("Review Changes", systemImage: Theme.Icon.reviewChanges).lineLimit(1) }
                }
                #endif
                if canPreview {
                    Button { previewing = true } label: { Label("Preview", systemImage: Theme.Icon.servedPage).lineLimit(1) }
                        .accessibilityIdentifier("chat-preview")
                }
                // The header's status line shows only running subagents: finished, folded and cross-machine ones live in
                // Session Overview's Subagents section (a hosted sheet still keeps its own strip).
                let subagentTotal = session.map { ChildSessions.totalSubagentCount($0, heldChildren: model.childRows(key)) } ?? 0
                if host == nil, subagentTotal > 0 {
                    Button { overviewUI.openOverview(focus: .subagents) } label: {
                        Label(String(localized: "Subagents (\(subagentTotal)) [Session Overview]"), systemImage: Theme.Icon.subagent).lineLimit(1)
                    }
                }
                // Its side sessions (#7944), as the header's chip opens them.
                let sides = self.sides
                if !sides.isEmpty {
                    Button { sideUI.showList = true } label: {
                        Label(SideSessionsSheet.menuTitle(sides.count), systemImage: Theme.Icon.sideSessions).lineLimit(1)
                    }
                    .accessibilityIdentifier("side-sessions-menu")
                }
            }
            if session?.canAttach == true {
                Button { Task { await model.attach(key) } } label: { Label("Attach Live", systemImage: Theme.Icon.link).lineLimit(1) }
            }
            if status.isActive, session?.canInterrupt == true {
                Button { Task { await model.interrupt(key) } } label: { Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt) }
            }
            // By `capabilities.rename` where the node says (a runtime's session, #6294), else a terminal's.
            if style == .dev, let s = session, s.canRename {
                Button { renaming = s } label: { Label("Rename… [Assistant Sidebar ec4cd0]", systemImage: Theme.Icon.edit) }
            }
            if style == .dev {
                Button { model.toggleMute(key) } label: {
                    Label(model.isMuted(key) ? String(localized: "Unmute [Chat View 3834f9]") : String(localized: "Mute"), systemImage: model.isMuted(key) ? Theme.Icon.unmute : Theme.Icon.mute)
                }
            }
            ModuleViews(place: .chatMenu, context: .init(machine: session?.assistant == true ? nil : key.machineRef,
                                                         recent: computerActivity?.machine, from: key))
            Divider()
            if let s = session {
                if s.canTerminal {
                    Button { UIPasteboard.general.string = "\(s.resumeCommand) \(s.sourceThreadID ?? s.sessionID)" } label: {
                        Label("Copy Resume Command", systemImage: Theme.Icon.copy).lineLimit(1)
                    }
                }
                Button { UIPasteboard.general.string = s.cwd } label: { Label("Copy Directory", systemImage: Theme.Icon.folder) }
                if host == nil, let space = s.spaceKey, let openSpace {
                    Button { openSpace(space) } label: { Label("All Agents in Space", systemImage: Theme.Icon.space) }
                }
                if s.status.isLive && s.canClose { // `capabilities.close` where the node says (#6294), else a terminal's
                    Button(role: .destructive) { closing = s } label: {
                        Label("Stop and Close Agent…", systemImage: Theme.Icon.end).lineLimit(1)
                    }
                }
                if s.canDelete {
                    Button(role: .destructive) { deleting = s } label: {
                        Label("Delete Chat…", systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                }
            }
    }

    #endif

    #if os(iOS)
    private var agentMenuHandlers: AgentMenuHandlers {
        let handlers = AgentMenuHandlers()
        handlers.navigate = { _, destination in
            switch destination {
            case .terminal(let resume):
                if resume { resumeInTerminal() } else { openTerminal() }
            case .pane(let target):
                withAnimation(Theme.Motion.standard) { pane = target }
            case .review:
                openReview?(nil)
            case .preview:
                previewing = true
            case .subagents:
                overviewUI.openOverview(focus: .subagents)
            case .sideSessions:
                sideUI.showList = true
            case .space:
                if let space = session?.spaceKey { openSpace?(space) }
            }
        }
        handlers.rename = { renaming = $0 }
        handlers.close = { closing = $0 }
        handlers.delete = { deleting = $0 }
        handlers.linkTodo = { AgentMenuRouting.shared.linkingSession = $0 }
        handlers.openTodo = { _ in showTodoDetail = true }
        handlers.takeover = { session in
            Task {
                if let adopted = await takeoverCoordinator.takeOver(session, model: model) {
                    model.adopt(adopted)
                    switchTo(adopted.key)
                }
            }
        }
        handlers.continueInNewSession = { session in
            Task {
                if let adopted = await takeoverCoordinator.continueInNewSession(session, model: model) {
                    model.adopt(adopted)
                    switchTo(adopted.key)
                }
            }
        }
        return handlers
    }

    private func takeAgentMenuDestination() {
        guard let session, let destination = AgentMenuRouting.shared.take(key) else { return }
        agentMenuHandlers.navigate(session, destination)
    }
    #endif

    /// A custom title: tapping it toggles the developer info card (instead of the system scroll-to-top).
    /// While the hub is out of reach the subtitle turns into a spinner and Connecting… (Telegram keeps the name and
    /// swaps the "online" line the same way); a tap then explains the link instead of opening the session info.
    private var titleButton: some View { titleMenu(sessionTitleControl) }

    @ViewBuilder private func titleMenu<Content: View>(_ control: Content) -> some View {
        #if os(iOS)
        if style == .dev, let session {
            control.contextMenu {
                AgentActionMenuReader(session: session, currentPane: pane, hosted: host != nil, showsSpace: openSpace != nil,
                                      sides: sides, handlers: agentMenuHandlers)
            }
        } else { control }
        #else
        control
        #endif
    }

    @ViewBuilder private var sessionTitleControl: some View {
        // Hosted for several agents (a task's): the title's ⌄ switches between them, and has the session's info too.
        // The switch stays while the hub is out of reach (its line then says so, and the menu explains the link).
        if style == .dev, let host, host.agents.count > 1 {
            Menu {
                Section("Switch Agent") {
                    ForEach(host.agents) { s in
                        let agentTitle = s.displayTitle(preferTab: model.prefersHerdrTabName(for: s))
                        Button { if s.key != key { host.select(s.key) } } label: {
                            if s.key == key {
                                Label(agentTitle, systemImage: Theme.Icon.success)
                            } else {
                                Text("\(agentTitle) · \(s.state.label)")
                            }
                        }
                    }
                }
                if model.connection.shown != .online {
                    Button { showLink = true } label: { Label("Connection Details", systemImage: Theme.Icon.info) }
                } else {
                    Button { overviewUI.openOverview() } label: { Label("Session overview [Session Overview]", systemImage: Theme.Icon.info) }
                }
            } label: {
                titleLabel(chevron: true)
            }
            .labelOnlyMenu()
            .tint(Color.primary) // a title, not a button: the menu's accent tint would color it blue
            .accessibilityLabel("\(shownTitle), \(host.agents.count) agents, tap to switch")
            .accessibilityIdentifier("chat.title")
            #if DEBUG
            .modifier(UITestSessionProof(session: key.path))
            #endif
            .connectionDetails($showLink)
        } else {
            infoTitleButton
        }
    }

    /// The title over its second line; `chevron` adds the ⌄ of a title that switches agents.
    private func titleLabel(chevron: Bool) -> some View {
        let link = model.connection.shown
        return VStack(spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.xs) {
                Text(shownTitle).font(Theme.Font.heading).lineLimit(1).truncationMode(.tail)
                if chevron {
                    ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                }
            }
            ZStack {
                if link != .online {
                    ConnectionLabel(phase: link, compact: true).transition(.opacity)
                } else {
                    HStack(spacing: Theme.Space.xs) {
                        if style == .dev, let s = session { devSubtitle(s) } else { Text(subtitle).lineLimit(1) }
                    }
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .transition(.opacity)
                }
            }
            .animation(Theme.Motion.standard, value: link)
        }
        // All the room between the round buttons on an iPhone; a readable cap on a wide page.
        .scaledFrame(maxWidth: Theme.Size.headerTitle)
        .contentShape(.rect)
        .titleSaysLink() // the shell's floating pill does not say Connecting… again under this title
    }

    private var infoTitleButton: some View {
        let link = model.connection.shown
        return Button {
            Haptics.tap()
            if link != .online { showLink = true } else { overviewUI.openOverview() }
        } label: {
            titleLabel(chevron: false)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(link != .online ? String(localized: "\(shownTitle), \(ConnectionLabel.text(link, hubRestarting: model.hubUpdate?.restarting == true)), view connection details") : String(localized: "\(shownTitle), view session info"))
        .accessibilityIdentifier("chat.title")
        #if DEBUG
        .modifier(UITestSessionProof(session: key.path))
        #endif
        .connectionDetails($showLink)
    }

    /// A hosting page's part of ⋯ (#3255): what it is about (the session's own title when the page's differs, the
    /// page's note), its own actions, and the agent in the Agents page, where its space's other agents are.
    @ViewBuilder private func hostedMenuItems(_ host: HostedChat) -> some View {
        let own = session?.displayTitle(preferTab: preferHerdrTabName) ?? ""
        let header = [own != title ? own : nil, host.note].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
        if header.isEmpty {
            hostedMenuActions(host)
        } else {
            Section(header) { hostedMenuActions(host) }
        }
        Divider()
    }

    @ViewBuilder private func hostedMenuActions(_ host: HostedChat) -> some View {
        host.menu
        Button { host.openInAgents(key) } label: {
            Label("Open in Agents", systemImage: Theme.Icon.agents).lineLimit(1)
        }
    }

    /// Agents: the title's second line — the state (and for how long it has worked this turn), then where it runs.
    /// The state's words take its color; the place stays secondary. The tab's name is left out when the strip below
    /// shows the space's tabs (the selected one is this agent's; 2026-11-01 user request: it said it twice). Short of
    /// room the machine goes; the place always stays (cut short if it must), so the line keeps one shape whatever the state.
    private func devSubtitle(_ s: Session) -> some View {
        VisibleTimelineView(interval: 30) { now in
            let words = stateWords(s, now: now)
            // Hosted (one agent's page, #3255) the line is status · machine · project: the tab's name would crowd the machine out.
            let tabShown = host != nil || !model.siblings(of: key).isEmpty
            let machine = model.namedMachine(s.machineRef)?.title ?? s.machineID
            let rest = s.chatSubtitleParts(machine: machine, tabShown: tabShown, preferTab: preferHerdrTabName)
            HStack(spacing: Theme.Space.xs) {
                AgentMark(s, side: Theme.Size.markXS) // its agent before its state (#6390); this line is an agent's only
                StatusDot(state: s.state, size: 7, waiting: s.waitsInBackground)
                // Not ViewThatFits: that measured both lines again at every width of a window resize (#1337).
                FirstFitting {
                    subtitleLine(s, words, rest.full)
                    subtitleLine(s, words, rest.short) // truncates when it must
                }
                .firstFittingText(subtitleText(s, words, rest.full))
            }
        }
    }

    /// Quiet states (Seen, Not running) say nothing in words (2026-10-31 user request): the dot carries them, the line is where
    /// it runs. Needs You / Working N min / Unread still lead the line.
    private func subtitleLine(_ s: Session, _ words: String, _ rest: [String]) -> some View {
        let parts = subtitleParts(s, rest)
        if Self.quiet(s.state) {
            return Text(parts.isEmpty ? words : parts.joined(separator: " · ")).foregroundStyle(.secondary).lineLimit(1)
        }
        let tail = parts.map { " · " + $0 }.joined()
        return (Text(words).fontWeight(.semibold)
            .foregroundStyle(s.state == .idle || s.state == .offline || s.state == .unknown ? Color.secondary : s.stateTone.text)
            + Text(tail).foregroundStyle(.secondary))
            .lineLimit(1) // FirstFitting picks by the ideal (one-line) width; the last one truncates
    }

    private func subtitleParts(_ s: Session, _ rest: [String]) -> [String] {
        ([s.sourceSubtitle].compactMap { $0 } + rest).filter { !$0.isEmpty }
    }

    /// What `subtitleLine` says, as one string.
    private func subtitleText(_ s: Session, _ words: String, _ rest: [String]) -> String {
        let parts = subtitleParts(s, rest)
        if Self.quiet(s.state) { return parts.isEmpty ? words : parts.joined(separator: " · ") }
        return ([words] + parts).joined(separator: " · ")
    }

    static func quiet(_ state: AgentState) -> Bool { state == .idle || state == .offline || state == .unknown }

    /// "Working 22 min" while a turn runs (minutes: the work line in the chat has the seconds), else the state's name.
    private func stateWords(_ s: Session, now: Date) -> String {
        // Its turn ended and a workflow / background work keeps it going (#2935): 「工作中（后台）」, not the turn's time;
        // or all it runs only waits (#6045): 「后台等待中」.
        if s.isBackgroundWorking || s.waitsInBackground { return s.stateLabel }
        guard s.state == .working, status.isActive, case .turn(let t)? = store.blocks.last, let start = t.startedAt else { return s.state.label }
        let m = Int(now.timeIntervalSince(start)) / 60
        if m < 1 { return s.state.label }
        return s.state.label + " " + (m < 60 ? String(localized: "\(m) min [Chat View a38788]") : String(localized: "\(m / 60) hr \(m % 60) min"))
    }

    /// The Agents header: back, title (with the state), the terminal and ⋯; under them, when there are any, the other
    /// agents of the space (a menu) and a /loop or background work. It folds into a slim bar as the list scrolls (not on the Mac: `ChatChrome.folds`).
    private var devChrome: some View {
        let header = ChromeHeader(chrome: chrome, touched: { scratch.lastActivity = .now }) {
            VStack(spacing: 0) {
                // One container for the row's glass (back, the terminal, ⋯): drawn together instead of as separate glasses.
                GlassGroup(spacing: 8) {
                    HStack(spacing: Theme.Space.s) {
                        headerBack
                        if pane == .chat, let expand = host?.expand { hostedExpandButton(expand) }
                        Spacer(minLength: 0)
                        titleButton
                        Spacer(minLength: 0)
                        // Hosted, the goal and a /loop or background work are small marks here, as in the folded bar:
                        // the header stays one row (#3255).
                        if host != nil { hostedMarks }
                        // Files (a swipe to the left now quotes a message, #1058), the terminal (it comes up from the
                        // bottom, TerminalCover) and Git are in ···: one button there (#1205).
                        moreMenu {
                            ThemedIcon(Theme.Icon.more).font(Theme.Font.heading).dynamicTypeSize(...DynamicTypeSize.xxLarge).scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                                .contentShape(.circle)
                        }
                        .surface(interactive: true, in: .circle)
                    }
                }
                .padding(.horizontal, Theme.Space.l)
                // A hosting sheet's grabber is over the row (#3255): it starts below it.
                .padding(.top, host?.closes == true ? Theme.Space.m : Theme.Space.xxs)
                .padding(.bottom, Theme.Space.sm)
                // Row two, only when there is something for it. Hosted (a task's sheet, #3255) it is one agent's page:
                // no strip of the space's tabs, and its activity is a mark in row one.
                let hasActivity = host == nil && (session?.loop != nil || !(session?.background ?? []).isEmpty)
                let tabs = host == nil && session?.spaceKey != nil
                let mode = headerModeSession
                let sides = self.sides
                if tabs || hasActivity || mode != nil || !sides.isEmpty {
                    contextRow(tabs: tabs, activity: hasActivity, mode: mode, sides: sides)
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.bottom, Theme.Space.s)
                }
                // The goal it works toward (docs/integrations/agent-goal.md): a tap shows all of it.
                if host == nil, let goal = session?.goal {
                    GoalBar(goal: goal)
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.bottom, Theme.Space.s)
                }
                if hasWorkflows {
                    workflowBar
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.bottom, Theme.Space.s)
                }
                // What the agent said of its own work as it stopped (#3612): above the transcript, never inside it.
                if shownCloseout != nil {
                    closeoutCard
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.bottom, Theme.Space.s)
                }
                // The ports it listens on (its dev servers, docs/operations/ports.md): a tap opens the page.
                if let ports = session?.ports, !ports.isEmpty {
                    SessionPortChips(machine: key.machineRef, ports: ports)
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.bottom, Theme.Space.s)
                }
                if pane == .chat {
                    if host == nil {
                        statusLine
                            .padding(.horizontal, Theme.Space.page)
                            .padding(.bottom, Theme.Space.sm)
                    } else {
                        hostedAgentStrip
                    }
                }
            }
        } compact: {
            compactChrome
        }
        #if os(iOS)
        .sheet(item: $reviewRequest, onDismiss: handleReviewDismiss) { r in reviewPage(r) }
        #endif
        // Its side sessions' sheet, a side's page over it, and a landing that names a side (#7944). On the chain behind
        // AnyView: one more modifier on it would hold a second copy of the whole header on the page's stack, which an
        // unoptimized build lays out by value (`MainPagesStackTests`).
        return AnyView(header).modifier(SideSessionsPresentation(parent: key, on: host == nil, ui: sideUI))
    }

    /// Back: out of files / Git to the chat first, then off the page.
    /// The widest the conversation gets: lines longer than this are hard to read.
    static let readableWidth: CGFloat = 760
    /// The list's margin at its newest end (its top: the list is upside down): scrolled to the end, the newest message
    /// stands clearly off the footer, so the end reads as the end (#2016). It scrolls with the rows; a little way up
    /// they reach the footer as before.
    static let endRoom: CGFloat = Theme.Space.xxl
    /// From the newest row's bottom to the footer's first row at the end: the rows' spacing (#1108) and `endRoom`; the
    /// end marker takes no room and the footer adds none. The first message of a new conversation is laid out with it
    /// before the chat takes over (`FirstMessagePending`, #1911).
    static let newestGap: CGFloat = Theme.Space.l + endRoom
    /// From the top of Persona's footer to its composer at the default text size: the capsule row's 8 pt margin, a 28 pt
    /// capsule and the footer's 12 pt (`AssistantActionBar`). Larger text makes the row taller, and the list then ends a
    /// little above the composer, where `FooterFog` is already solid.
    static let underDock: CGFloat = Theme.Space.xxl + Theme.Space.l
    /// The last of it, between the capsules and the composer: solid page in the fog, so nothing shows faintly there.
    static let underDockSolid: CGFloat = Theme.Space.m

    /// How far the list runs on behind the top of the footer (#3696). In Persona's chats, behind the capsule row over the
    /// composer, so the chat fades out behind the capsules instead of ending in a band of page above them; the newest end
    /// has that much more room, so at rest nothing is behind them. None where the footer starts with the composer itself
    /// (Agents chats, a subagent's transcript, a voice call, which puts the capsules away): there the chat is gone by the
    /// composer's top edge.
    private var underFooter: CGFloat {
        style == .assistant && !backgroundActivity && viewing == nil && !(model.voice?.isLive(on: key) ?? false) ? Self.underDock : 0
    }

    /// The fog over the list's end: clear `newestGap` above the footer, where the newest message rests, and solid from
    /// the bottom of the capsules behind the footer down to the list's bottom edge. A fixed size at the list's bottom:
    /// scrolling and typing do not draw it again.
    @ViewBuilder private func footerFog(under: CGFloat) -> some View {
        FooterFog(height: Self.newestGap + under, solid: under > 0 ? Self.underDockSolid : 0)
    }

    /// An Agents chat whose actions are in the system's side column (no header of its own).
    private var sideBars: Bool { style == .dev && barsVertical }

    /// Under side bars in a split (the open Duo on its side) the page starts `Theme.Space.xl` from the top whatever its
    /// navigation bar holds. The bar holds nothing across the top (its actions are in the side column, its title is
    /// removed) but, with the column folded, the system's Show Sidebar, and that row pushed the header and the chat 58 pt
    /// down. The empty bar put the page `Theme.Space.xl` down, so with the column out nothing moves; folded, the sidebar's
    /// button is the side column's first item (`sideBarItems`, #11636). Not while typing: the bar then shows the system's
    /// title over the content, and the header is only fog. Not in a hosting sheet, which has a bar of its own.
    private var pinsSideTop: Bool {
        #if os(iOS)
        sideBars && split?.sideBars == true && host == nil && !composerTyping
        #else
        false
        #endif
    }

    /// Under side bars, a page pushed onto another, on its chat: Back is the system's own (#9971). It does what ours does
    /// there (one page back), the system puts it at the top of the side column, and only with it does iOS's swipe back
    /// from anywhere on the page work: UIKit gives that swipe no touch on a page that hides the system's Back. Ours stays
    /// where it does something else: files / Git back to the chat, a sheet's ✕.
    private var systemBack: Bool {
        guard sideBars, pane == .chat, !hidesBack, !backShowsList else { return false }
        if let host { return !host.closes }
        return split?.isFirstPage != true
    }

    /// The header's actions as system toolbar items, for the side column: back on top (a custom one, so it leaves files /
    /// Git for the chat first), then the panes, the terminal and ⋯. Each has a title as well as a symbol: the column
    /// shows the symbol, the overflow menu both. A root picked in the sidebar, with the sidebar folded, has the sidebar's
    /// button there instead (#8603, #11636): the system's Show Sidebar is removed on the roots' column, since iOS drew it
    /// in the bar's title row, which the chat removes, and did not draw it again on a root after a pop.
    @ToolbarContentBuilder private var sideBarItems: some ToolbarContent {
        if !hidesBack && !systemBack {
            ToolbarItem(placement: .topBarLeading) {
                // A hosting sheet's ✕ Close, its pushed page's ‹ Back, the panes' ‹ Back (#2625): as the header's would be;
                // a root's Show Sidebar (`backSymbol`, `backLabel`).
                Button { back() } label: { Label(backLabel, systemImage: backSymbol) }
                    .modifier(SidebarButtonID(active: backShowsList))
            }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            ForEach(sideButtons) { p in
                switch p {
                case .chat:
                    Button { withAnimation(Theme.Motion.standard) { pane = .chat } } label: { Label("Chat", systemImage: SessionPane.chat.symbol) }
                case .terminal:
                    Button { Haptics.tap(); openTerminal() } label: { Label("Terminal", systemImage: SessionPane.terminal.symbol).lineLimit(1) }
                default:
                    Button { Haptics.tap(); withAnimation(Theme.Motion.standard) { pane = p } } label: {
                        Label(paneBadges[p].map { String(localized: "\(p.title) (\($0))") } ?? p.title, systemImage: p.symbol)
                    }
                }
            }
            Menu { moreMenuItems } label: { Label("More", systemImage: Theme.Icon.more) }
        }
    }

    /// The side column's buttons between Back and ⋯, in order (back to the chat from a pane, the terminal, Git, Files),
    /// as many of them as stand clear of the Duo's tab bar under them (#9837, `TabSwitchLayout.pageButtons`): the leading
    /// ones; ⋯ has every one of them (`moreMenuItems`), so what has no room is still a tap away there.
    private var sideButtons: [SessionPane] {
        var all: [SessionPane] = pane != .chat ? [.chat] : []
        if session?.canTerminal == true { all.append(.terminal) }
        all += panes.filter { ($0 == .git || $0 == .files) && $0 != pane }
        let back = !hidesBack
        return Array(all.prefix(TabSwitchLayout.pageButtons(all.count, back: back, room: sideColumn?.room)))
    }

    /// The header's other half while its buttons are in the side column: the title (a tap shows the session's info),
    /// and when there are any the space's other agents, a /loop or background work and subagents. It stays put, but
    /// while the keyboard is up on the composer it is only the fog: iPhone Duo's inner display on its side has about
    /// 390pt over the keyboard, and the title, the tabs and the mode chips over the box left no row of the chat
    /// (2026-11-04). The system's title over the content says which session it is then (it is removed while the
    /// header shows its own).
    private var sideHeader: some View {
        let header = VStack(spacing: Theme.Space.sm) {
            if !composerTyping { sideHeaderRows }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.xs)
        .padding(.bottom, Theme.Space.s)
        .frame(maxWidth: .infinity)
        .background(alignment: .top) {
            // The page's fog from the very top, so messages scrolling up do not show through the title.
            GeometryReader { g in
                TopFog(solidHeight: g.safeAreaInsets.top + g.size.height, fadeHeight: 16)
                    .frame(height: g.safeAreaInsets.top + g.size.height + 16)
                    .offset(y: -g.safeAreaInsets.top)
            }
        }
        .animation(Theme.Motion.layout, value: composerTyping)
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { sideHeaderHeight = $0 }
        #if os(iOS)
        .sheet(item: $reviewRequest, onDismiss: handleReviewDismiss) { r in reviewPage(r) }
        #endif
        // Its side sessions' sheet, a side's page over it, and a landing that names a side (#7944). On the chain behind
        // AnyView: one more modifier on it would hold a second copy of the whole header on the page's stack, which an
        // unoptimized build lays out by value (`MainPagesStackTests`).
        return AnyView(header).modifier(SideSessionsPresentation(parent: key, on: host == nil, ui: sideUI))
    }

    @ViewBuilder private var sideHeaderRows: some View {
        titleButton
        let hasActivity = session?.loop != nil || !(session?.background ?? []).isEmpty
        let tabs = host == nil && session?.spaceKey != nil
        let mode = headerModeSession
        let sides = self.sides
        if tabs || hasActivity || mode != nil || !sides.isEmpty { contextRow(tabs: tabs, activity: hasActivity, mode: mode, sides: sides) }
        if let goal = session?.goal { GoalBar(goal: goal) }
        workflowBar
        closeoutCard
        if let ports = session?.ports, !ports.isEmpty { SessionPortChips(machine: key.machineRef, ports: ports) }
        if pane == .chat {
            if host == nil {
                statusLine
            } else {
                hostedAgentStrip
            }
        }
    }

    /// The header's second row: the space's other agents on the left, taking the width there is, and this agent's /loop
    /// and background work as one pill held at the trailing end (there too without tabs), so neither moves when the
    /// other comes or goes. Its side sessions' chip leads the trailing group while it has any (#7944).
    private func contextRow(tabs: Bool, activity: Bool, mode: Session?, sides: SideSessions = SideSessions()) -> some View {
        HStack(spacing: Theme.Space.m) {
            if tabs {
                SiblingTabs(current: key, select: switchTo, closed: closedAgent).frame(maxWidth: .infinity, alignment: .leading)
            } else {
                Spacer(minLength: 0)
            }
            // This agent's own: its /loop and background work, then its permission mode at the very end (under ⋯, #3719),
            // which stays put as the work comes and goes.
            if activity || mode != nil || !sides.isEmpty {
                HStack(spacing: Theme.Space.s) {
                    if !sides.isEmpty { SideSessionsChip(sides: sides) { sideUI.showList = true } }
                    if activity {
                        ActivityChips(loop: session?.loop, background: session?.background ?? []) { overviewUI.openOverview(focus: .background) }
                    }
                    if let mode { HeaderModeButton(session: mode, changing: modeChanging, pick: requestMode).equatable() }
                }
            }
        }
    }

    /// The agent whose permission mode ends row two (#3719): one with a mode to see or change
    /// (`SessionModes.showsInHeader`). Not on a hosted page, whose header stays one row (#3255), nor in the folded bar.
    private var headerModeSession: Session? {
        guard host == nil, let s = session, SessionModes.showsInHeader(s) else { return nil }
        return s
    }

    struct ModeConfirm: Equatable {
        var mode: String
        var kind: SessionModes.Confirmation
    }

    /// A permission mode picked in the header or the composer's model menu: asks first where `SessionModes.confirmation`
    /// says so (into Allow Everything; out of it when the agent cannot come back), else switches at once.
    private func requestMode(_ mode: String) {
        // Against the mode it runs in as the menus tick it: a session that lists its own modes (#8261) is in its own
        // approval when it reports none. While a restart into Allow Everything waits (`bypassPending`), picking the
        // checked mode again sends it to the node so the restart is called off without switching modes (#7996 review).
        guard SessionModes.appliesPick(session, to: mode), !modeChanging else { return }
        if let kind = SessionModes.confirmation(session, to: mode) {
            modeConfirm = ModeConfirm(mode: mode, kind: kind)
        } else {
            applyMode(mode)
        }
    }

    private func applyMode(_ mode: String) {
        modeChanging = true
        Task {
            _ = await model.setModes(key, permissionMode: mode) // a refusal is shown by the model, the mode stays as it was
            modeChanging = false
        }
    }

    private func modeConfirmTitle(_ c: ModeConfirm) -> String {
        switch c.kind {
        case .allowEverything: String(localized: "Allow Everything?")
        case .leaveAllowEverything: String(localized: "Leave Allow Everything?")
        }
    }

    private func modeConfirmAction(_ c: ModeConfirm) -> String {
        switch c.kind {
        case .allowEverything: String(localized: "Allow Everything")
        case .leaveAllowEverything: String(localized: "Switch to \(SessionModes.modeLabel(c.mode) ?? c.mode)")
        }
    }

    private func modeConfirmMessage(_ c: ModeConfirm) -> String {
        switch c.kind {
        // A restart into it (#6513) says so, as its row does.
        case .allowEverything: [String(localized: "Edits files and runs commands without asking"), session.flatMap(SessionModes.bypassNote)].compactMap { $0 }.joined(separator: "\n")
        case .leaveAllowEverything: String(localized: "This agent wasn't started with Allow Everything, so it can't be switched back to it from here.")
        }
    }

    /// The header's third row (`HeaderStatusLine`): what its subagents are doing, when that is worth a line.
    @ViewBuilder private var statusLine: some View {
        if let status = HeaderStatus.pick(subagents, viewing: viewing) {
            HeaderStatusLine(status: status, open: { a in
                overviewUI.openDetail(.embedded(parent: key, agent: a), fromOverview: false)
            }, openAll: {
                Haptics.tap()
                overviewUI.openOverview(focus: .subagents)
            })
        }
    }

    /// Hosted (a task's sheet, #3255), which this header's redesign leaves as it was: Main Agent and a chip per running
    /// subagent, with "All subagents" for the rest.
    @ViewBuilder private var hostedAgentStrip: some View {
        let all = subagents
        if !all.isEmpty {
            AgentStrip(agents: all.filter { $0.running || $0.id == viewing?.id }, all: all, viewing: viewing?.id) { a in view(a) }
        }
    }

    /// Whether `target` has a real launch call or folded child card anchor in the parent chat's loaded transcript.
    /// Drives whether "Show in chat" is offered on `SubagentDetailPage`.
    private func canShowSubagentInChat(_ target: SubagentOverview.Target) -> Bool {
        let lines = currentLines()
        switch target {
        case .embedded(_, let agent):
            guard let callID = agent.toolUseID, !callID.isEmpty else { return false }
            return hasCallTarget(callID, in: lines)
        case .session(let childKey):
            return ChildSessionLines.target(of: childKey, in: lines) != nil
        }
    }

    /// Scrolls the parent chat to the launch call or folded child card for `target` ("Show in chat").
    private func showSubagentInChat(_ target: SubagentOverview.Target) {
        switch target {
        case .embedded(_, let agent):
            if let callID = agent.toolUseID {
                jumpToCall(callID)
            }
        case .session(let childKey):
            jumpToChildSession(childKey)
        }
    }

    /// Expands the enclosing supervision/work group and `ChildSessionCard` for `childKey`, then scrolls the transcript
    /// directly to that real child card anchor.
    private func jumpToChildSession(_ childKey: SessionKey) {
        guard let target = ChildSessionLines.target(of: childKey, in: currentLines()) else {
            Haptics.attention()
            return
        }
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) {
            if let sid = target.supervisionID, !isSupervisionOpen(sid) {
                flipSupervision(sid)
            }
            if !isOpen(target.groupID) {
                flipWork(target.groupID)
            }
        }
        model.requestRevealChild(childKey)
        userScrolled = true
        setFollow { $0.read() }
        Haptics.tap()
        let lineID = target.lineID
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(160)) // allow popped detail transition + opened work steps to lay out
            withAnimation(Theme.Motion.standard) { position.scrollTo(id: lineID, anchor: .center) }
            try? await Task.sleep(for: .milliseconds(250))
            quoteFlash = QuoteFlashMark(line: lineID)
        }
    }

    /// The first page of a split's detail column, on the chat: nothing to go back to (the list is beside it).
    private var hidesBack: Bool {
        host == nil && split?.isFirstPage == true && split?.listHidden != true && pane == .chat
    }
    /// The split's first page with its list column closed: ‹ brings the list back. There is nothing to go back to, and
    /// with this page's own header no system button to open the list (the iPad; the open Duo upright).
    private var backShowsList: Bool {
        host == nil && split?.isFirstPage == true && split?.listHidden == true && pane == .chat
    }

    /// The header's leading button: back to the chat from files / Git; on the chat ‹, or a hosting page's ✕ / ‹.
    /// A root picked in the list, with the list closed (#8603): the sidebar's button, as on the main conversation, not a ‹
    /// that opened the sidebar. Only a page pushed from inside another has ‹.
    private var backSymbol: String {
        if backShowsList { return split?.drawer == true ? Theme.Icon.sidebarMenu : Theme.Icon.sidebar }
        return pane == .git || pane == .files ? Theme.Icon.back : host?.leaveSymbol ?? Theme.Icon.back
    }
    private var backLabel: String {
        if backShowsList { return String(localized: "Show Sidebar") }
        return pane == .git || pane == .files ? String(localized: "Back [Task Sheets 572cf4]") : host?.leaveLabel ?? String(localized: "Back [Task Sheets 572cf4]")
    }

    /// The open header's ‹. Inside the row's glass container `noBackWhen` does not hide it: the container draws the
    /// button's glass and symbol itself, so a disabled ‹ that did nothing showed on every chat that is a split's first
    /// page: on the Mac (#1337), and on iPad / iPhone Duo open beside the list. The hidden one is an empty spot of its
    /// size, which keeps the title centered.
    @ViewBuilder private var headerBack: some View {
        if hidesBack {
            Color.clear.dynamicTypeSize(...DynamicTypeSize.xxLarge).scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
        } else if backShowsList && split?.drawer == true {
            // The Sidebar layout on a phone (#6332): the chat is the drawer's pick, not a page to go back from. The
            // persona's face opens the drawer, as on the conversation, with how many Agents need the user.
            #if os(iOS)
            ShellPersonaButton(id: "sidebar-drawer-button") { back() }
                .dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .overlay(alignment: .topTrailing) { SidebarWaitingBadge() }
            #else
            headerBackButton
            #endif
        } else if backShowsList {
            // A root picked in the list, with the list closed (#8603): the sidebar's button (`backSymbol`) with how many
            // Agents need the user, as on the main conversation.
            headerBackButton
                .modifier(SidebarButtonID(active: true))
                .overlay(alignment: .topTrailing) { SidebarWaitingBadge() }
        } else {
            headerBackButton
        }
    }

    private var headerBackButton: some View {
        Button { back() } label: {
            ThemedIcon(backSymbol).font(Theme.Font.heading).dynamicTypeSize(...DynamicTypeSize.xxLarge).scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .surface(interactive: true, in: .circle)
        .accessibilityLabel(backLabel)
    }

    /// A hosting sheet's Full Screen beside its ✕ (#3743): the agent's full page instead of the sheet.
    private func hostedExpandButton(_ expand: @escaping (SessionKey) -> Void) -> some View {
        Button { expand(key) } label: {
            ThemedIcon(Theme.Icon.enlarge).font(Theme.Font.heading).dynamicTypeSize(...DynamicTypeSize.xxLarge)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .surface(interactive: true, in: .circle)
        .accessibilityLabel("Full Screen")
        .accessibilityIdentifier("task-detail.fullscreen")
    }

    private func back() {
        if pane != .chat { withAnimation(Theme.Motion.standard) { pane = .chat } } else if let host { host.leave() }
        else if backShowsList, let showList { showList.show() } else { dismissChat.action?() }
    }

    /// Resume in Terminal (#5521): the terminal comes up over the page and resumes the stopped Agent there (TerminalPanel's
    /// Resume Session, `session.start` with its id), so its progress, an error and then the Agent itself show in one place.
    private func resumeInTerminal() {
        resumeOnOpen = true
        openTerminal()
    }

    /// The stopped line's Resume: as Resume in Terminal.
    private var resumeHere: (() -> Void)? {
        resumeInTerminal
    }

    #if os(iOS)
    /// ⋯'s Resume in Terminal, as `Session.terminalResume(on:)` says: there for a stopped Agent, disabled with why where
    /// its computer has no terminal engine, gone otherwise (running, starting, an assistant's, a desktop app's).
    @ViewBuilder private func resumeMenuItem(_ s: Session) -> some View {
        let machine = model.machine(s.machineRef)
        switch s.terminalResume(on: machine) {
        case .available:
            Button { resumeInTerminal() } label: { Label("Resume in Terminal", systemImage: Theme.Icon.resume).lineLimit(1) }
                .accessibilityIdentifier("chat-resume-terminal")
        case .noEngine:
            // The reason under the name, written as the mode menu's disabled Allow Everything: two `Text`s and the image
            // (a menu drops a `Label`'s second `Text`, and keeps a `Label`'s icon tinted while the row dims).
            Button {} label: {
                Text("Resume in Terminal")
                Text("No terminal engine on \(machine?.name ?? s.machineID)", comment: "Why Resume in Terminal is off: the computer enables no terminal engine")
                Theme.Icon.image(Theme.Icon.resume)
            }
            .disabled(true)
            .accessibilityIdentifier("chat-resume-terminal")
        case .hidden:
            EmptyView()
        }
    }
    #endif

    /// Terminal: it comes up from the bottom over the page.
    private func openTerminal() {
        showTerminal = true
    }

    /// A swipe to the right goes back toward the chat (from Git to files, from files to the chat).
    private func swipePane(forward: Bool) {
        guard style == .dev else { return }
        let order = panes.filter { $0 != .terminal }
        guard let i = order.firstIndex(of: pane) else { return }
        let j = forward ? i + 1 : i - 1
        guard order.indices.contains(j) else { return }
        Haptics.tap()
        withAnimation(Theme.Motion.standard) { pane = order[j] }
    }

    /// The slim bar's words: what the session is, as the open header, the web and Android name it (#3631). Where it runs
    /// is a tap away, on the open header's second line and in Session Info; the pane's name only off the chat.
    private var shownTitle: String {
        style == .dev ? model.pendingAgentTitle(key) ?? title : title
    }

    private var compactTitle: String {
        pane == .chat ? shownTitle : shownTitle + " · " + pane.title
    }

    /// A hub link asked for one of this chat's messages (AppModel.messageJump): the entry target from now; taken once.
    private func takeMessageJump() -> Bool {
        guard let j = model.messageJump, j.key == key else { return false }
        model.messageJump = nil
        scratch.entryPages = 0
        #if os(iOS)
        if let source = j.starred {
            guard let api = model.api(key.group) else {
                model.toast = Toast(String(localized: "Couldn't check the original message. Try again."))
                return false
            }
            if viewing != nil { view(nil) }
            entry = .starred(j)
            checkedStar = nil
            Task {
                let result = await StarredStore.readOriginal(source, key: key, api: api)
                guard entry == .starred(j), shownKey == j.key, api.sameSettingsScope(as: model.api(key.group)) else { return }
                if result == .verified {
                    checkedStar = j.id
                    landEntry(currentLines())
                } else {
                    entry = nil
                    model.toast = Toast(result == .unavailable ? String(localized: "The original message is unavailable.") :
                        String(localized: "Couldn't check the original message. Try again."))
                }
            }
            return true
        }
        #endif
        if let card = j.card {
            entry = .card(card)
            return true
        }
        entry = .message(j.item)
        return true
    }

    /// A "Done" notification for this chat asked for its latest answer (AppModel.latestAnswerFor); taken once.
    private func takeLatestAnswer() -> Bool {
        guard model.latestAnswerFor == key else { return false }
        model.latestAnswerFor = nil
        return true
    }

    /// What covers this page full screen goes (the bar back to its voice chat was tapped, #9887): the picture viewer and
    /// the terminal. Their dismissal shows the page again, which takes the bar away (`VoiceCalls.returned`).
    private func closeCovers() {
        if viewingImages != nil { viewingImages = nil }
        if showTerminal {
            showTerminal = false
            resumeOnOpen = false // as its own dismissal does
        }
    }

    private func appeared() {
            ChatTrace.shared.note(shownKey, "open items=\(store.items.count) covered=\(store.covered)")
            #if DEBUG
            let id = shownKey.machineID + "/" + shownKey.sessionID
            Self.appearances[id, default: 0] += 1
            appearCount = Self.appearances[id, default: 0]
            ShellTrace.shared.note("chat appeared \(style == .assistant ? "assistant" : "agents") #\(appearCount)")
            #endif
            bindMenu()
            #if os(iOS)
            takeAgentMenuDestination()
            #endif
            if !entered { // only on entry: coming back from a full-screen cover must not move what you were reading
                entered = true
                if takeMessageJump() {
                    // opened from a hub link to one of its messages (#2696): that message, not the newest
                } else if style == .dev, session?.state == .unread || takeLatestAnswer() || Self.debugAsUnread {
                    entry = .latestAnswer // finished, not seen (or opened from its notification): the latest answer from its start
                } else if let saved = UserDefaults.standard.string(forKey: readKey) {
                    entry = .line(saved) // where you left off (Persona's conversations too, #1666)
                }
                settleAtEnd()
                #if DEBUG && os(iOS)
                if style == .assistant, Self.debugEdgeScrub > 0 { Task { await showEdgeScrub(Self.debugEdgeScrub) } }
                #endif
            }
            if style == .dev { AppModel.rememberChat(key) }
            #if DEBUG
            if UserDefaults.standard.bool(forKey: "openTerminalPane") { openTerminal() }
            if UserDefaults.standard.bool(forKey: "openSpaceFromChat") { // chat → its space page
                UserDefaults.standard.set(false, forKey: "openSpaceFromChat")
                Task {
                    try? await Task.sleep(for: .seconds(5)) // the session list may not be in yet
                    if let space = session?.spaceKey { openSpace?(space) }
                }
            }
            if UserDefaults.standard.bool(forKey: "showScrollToOldest") { // testing: flick toward the oldest, log progress
                Task {
                    try? await Task.sleep(for: .seconds(8))
                    userScrolled = true // as if dragged: older pages only load after the user took the list
                    setFollow { $0.read() }
                    var log: [String] = []
                    var y: CGFloat = chrome.offset
                    for step in 0..<20 {
                        y += 4000
                        withAnimation(.easeOut(duration: 1.5)) { scrollList(to: y) }
                        try? await Task.sleep(for: .seconds(2.5))
                        let s = model.chat(shownKey)
                        log.append("step \(step) offset=\(Int(chrome.offset)) target=\(Int(y)) items=\(s.items.count) rows=\(s.rows.count) first=\(s.items.first?.seq ?? 0) more=\(s.hasMore) loading=\(s.isLoadingOlder) follows=\(follow.follows) near=\(follow.near) ends=\(scratch.endJumps)")
                        y = max(y, chrome.offset)
                    }
                    if let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first {
                        try? log.joined(separator: "\n").write(to: dir.appending(path: "oldest.txt"), atomically: true, encoding: .utf8)
                    }
                }
            }
            if UserDefaults.standard.bool(forKey: "showFoldProbe") { // folding keeps what you read in place: Documents/fold.txt
                Task { await foldProbe() }
            }
            if UnfoldTrace.enabled, UserDefaults.standard.bool(forKey: "showUnfoldLab") { // UNFOLD lines on the console
                Task { await unfoldLab() }
            }
            if UserDefaults.standard.bool(forKey: "showFreezeProbe") { // reading history holds still: Documents/freeze.txt
                Task { await freezeProbe() }
            }
            if UserDefaults.standard.bool(forKey: "showChatDump") { // what the chat holds, every 2 s: Documents/chat-dump.json
                Task {
                    let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
                    while !Task.isCancelled {
                        let s = model.chat(shownKey)
                        let dump: [String: Any] = ["session": shownKey.sessionID, "covered": s.covered, "history": s.history ?? "",
                                                   "has_more": s.hasMore, "lines": currentLines().count, "visible": scratch.visibleLines.count,
                                                   "items": s.items.map { [$0.id, $0.seq, $0.kind.rawValue, $0.fullSize ?? 0] as [Any] }]
                        if let data = try? JSONSerialization.data(withJSONObject: dump) {
                            try? data.write(to: dir.appending(path: "chat-dump.json"), options: .atomic)
                        }
                        try? await Task.sleep(for: .seconds(2))
                    }
                }
            }
            #if os(iOS)
            if UserDefaults.standard.bool(forKey: "showJumpPerf") { Task { await jumpPerf() } } // the four jumps, measured (#5076)
            #endif
            if UserDefaults.standard.bool(forKey: "showAutoScroll") { // profiling: flicks through the history, frames counted
                Task {
                    try? await Task.sleep(for: .seconds(12)) // the history loads (-loadAll) and the page settles
                    // Sitting on the page first (a script may push hub updates meanwhile: /mock/churn), then the flicks.
                    FrameStats.shared.start()
                    try? await Task.sleep(for: .seconds(8))
                    FrameStats.shared.stop(label: "idle")
                    // Typing: a character every 120 ms into the draft, as the text field's binding writes it.
                    FrameStats.shared.start()
                    for i in 0..<40 {
                        model.drafts[key] = String(repeating: "字", count: i + 1) // i18n-ignore
                        try? await Task.sleep(for: .milliseconds(120))
                    }
                    model.drafts[key] = ""
                    FrameStats.shared.stop(label: "type")
                    // A reply streaming in (a script starts it as soon as frames-type.json is written: /mock/say).
                    FrameStats.shared.start()
                    try? await Task.sleep(for: .seconds(6))
                    FrameStats.shared.stop(label: "stream")
                    try? await Task.sleep(for: .seconds(2))
                    FrameStats.shared.start()
                    // Programmatic jumps: four of 3000 pt back into the history, then four back to the newest, as the chat
                    // jumps (`ChatJumpRun`), or with `-showSwiftUIJumps YES` as SwiftUI's animated scroll (before #5076).
                    var y: CGFloat = 0
                    for step in 0..<8 {
                        y = max(0, y + (step < 4 ? 3000 : -3000))
                        debugJump(to: y, seconds: 2.5)
                        try? await Task.sleep(for: .seconds(2.8))
                    }
                    FrameStats.shared.stop(label: "sweep")
                    // Short jumps, less than a screen: the whole way animates either way (frames-hop.json).
                    try? await Task.sleep(for: .seconds(1))
                    FrameStats.shared.start()
                    for step in 0..<8 {
                        y = max(0, y + (step < 4 ? 600 : -600))
                        debugJump(to: y, seconds: nil)
                        try? await Task.sleep(for: .seconds(1))
                    }
                    FrameStats.shared.stop(label: "hop")
                    // The same distance as a finger's glide: the list's own scroll view moved every display frame at
                    // 2000 pt/s (no SwiftUI state written), four seconds back into the history and four to the newest.
                    try? await Task.sleep(for: .seconds(2))
                    if let list = pin.list {
                        FrameStats.shared.start()
                        await ListGlide.run(list, seconds: 4, speed: 2000)
                        await ListGlide.run(list, seconds: 4, speed: -2000)
                        FrameStats.shared.stop(label: "glide")
                    }
                }
            }
            if UserDefaults.standard.bool(forKey: "showCompactChrome") { // folded, once the header has its height
                Task { try? await Task.sleep(for: .seconds(3)); withAnimation(.smooth) { chrome.collapse = chrome.maxCollapse } }
            }
            if UserDefaults.standard.bool(forKey: "showShareCard") { // the share card of the latest answer
                Task {
                    try? await Task.sleep(for: .seconds(5))
                    let answer = model.chat(key).blocks.reversed().lazy.compactMap { b -> Item? in
                        if case .turn(let t) = b, case .assistant(let i)? = t.answer { i } else { nil }
                    }.first
                    if let a = answer { menu.sharing = menu.shareMessage(text: a.text ?? "", isUser: false, date: a.ts) }
                }
            }
            #if os(iOS)
            // `-showExport replies|all` (#9468): selecting with every reply (or every message) picked, then Export Image.
            if let what = UserDefaults.standard.string(forKey: "showExport") {
                Task {
                    try? await Task.sleep(for: .seconds(5))
                    let bars = currentLines().compactMap { Self.pickBar($0) }.filter { what == "all" || !$0.isUser }
                    guard let first = bars.first else { return }
                    pick.start(first.id)
                    for b in bars.dropFirst() { _ = pick.toggle(b.id) }
                    exportPicks()
                }
            }
            #endif
            if UserDefaults.standard.bool(forKey: "showCloseAgent") { // the close confirmation, for screenshots
                Task { try? await Task.sleep(for: .seconds(3)); closing = session }
            }
            if UserDefaults.standard.bool(forKey: "showDeleteSession") { // Delete Chat's confirmation, for screenshots and UI tests
                UserDefaults.standard.set(false, forKey: "showDeleteSession") // once: the page appears again after Cancel
                Task { // once the session list is in (the chat can open before it)
                    for _ in 0..<30 {
                        try? await Task.sleep(for: .seconds(1))
                        if let s = session, s.canDelete { deleting = s; break }
                    }
                }
            }
            if UserDefaults.standard.bool(forKey: "openPreviewServers") { // `-openPreviewServers YES`: ⋯ › Preview's list (#150, screenshots)
                Task { try? await Task.sleep(for: .seconds(2)); if canPreview { previewing = true } }
            }
            if let u = UserDefaults.standard.string(forKey: "openLinkURL").flatMap(URL.init(string:)) { // `-openLinkURL <url>`
                Task { try? await Task.sleep(for: .seconds(3)); actions.openLink(u) } // as a tap would: localhost is the machine's
            }
            if UserDefaults.standard.bool(forKey: "openFirstImage") {
                Task {
                    for _ in 0..<30 { // the cached copy drops images; wait for the hub's
                        try? await Task.sleep(for: .seconds(1))
                        let imgs = model.chat(key).items.compactMap(\.image)
                        if !imgs.isEmpty, debugImages == nil { debugImages = imgs; break }
                    }
                }
            }
            if UserDefaults.standard.bool(forKey: "showInfo") { overviewUI.openOverview() }
            // `-showAgentInfo YES`: an Agent chat's own overview sheet (`-showInfo` also opens the assistant's, which is mounted too).
            if UserDefaults.standard.bool(forKey: "showAgentInfo"), session?.assistant != true { overviewUI.openOverview() }
            if UserDefaults.standard.bool(forKey: "openTodoDetail") { showTodoDetail = true }
            // `-openSendMessage <text>`: refusals (SendSafety). Once per launch: the chat appears, goes and appears again
            // while the app starts (three times in 0.2 s), and each one wrote the message anew, with its own id
            // (ConnectionTests saw it reach the hub three times; a message the user writes is written once).
            if let t = UserDefaults.standard.string(forKey: "openSendMessage"), !Self.debugSent {
                Self.debugSent = true
                // `-openSendDelay <s>`: later (ConnectionTests sends while the hub is down)
                let delay = UserDefaults.standard.object(forKey: "openSendDelay") != nil ? UserDefaults.standard.double(forKey: "openSendDelay") : 3
                Task { try? await Task.sleep(for: .seconds(delay)); model.sendNow(key, text: t) }
            }
            if let p = UserDefaults.standard.string(forKey: "openPane").flatMap(SessionPane.init(rawValue:)) {
                if p == .terminal { openTerminal() } else { pane = p } // the terminal comes up over the page (in its place on the Mac)
            }
            if let f = UserDefaults.standard.string(forKey: "openFilePath") {
                // Once the session is known (its cwd resolves a relative path) and the push is over: a sheet asked for
                // during the push is dropped (a fresh install, FileViewerUITests).
                Task {
                    for _ in 0..<40 where session == nil { try? await Task.sleep(for: .milliseconds(250)) }
                    try? await Task.sleep(for: .seconds(1))
                    // `-openFileLine <n>`: at that line, as a message's `path:n` opens it.
                    let line = UserDefaults.standard.object(forKey: "openFileLine") != nil ? UserDefaults.standard.integer(forKey: "openFileLine") : nil
                    file = FileRef(group: key.group, machineID: key.machineID, path: f, cwd: session?.cwd, line: line)
                }
            }
            if let id = UserDefaults.standard.string(forKey: "openAgent") { // `-openAgent <agent id>`: that subagent
                Task {
                    try? await Task.sleep(for: .seconds(4))
                    if let a = session?.subagents?.first(where: { $0.id == id }) { view(a) }
                }
            }
            if UserDefaults.standard.bool(forKey: "showOldest") { // the start of what is loaded (with -showFromSeq: that turn's top)
                Task {
                    try? await Task.sleep(for: .seconds(5))
                    userScrolled = true
                    setFollow { $0.read() }
                    position.scrollTo(edge: .bottom)
                }
            }
            if UserDefaults.standard.bool(forKey: "jumpEnd") { // what tapping the ↓ arrow does, from the top
                Task {
                    try? await Task.sleep(for: .seconds(6))
                    userScrolled = true
                    position.scrollTo(edge: .bottom) // the oldest end of the flipped list
                    try? await Task.sleep(for: .seconds(4))
                    jumpToEnd()
                }
            }
            if UserDefaults.standard.bool(forKey: "showMenu") { // the long-press bar on the last answer, for screenshots
                Task {
                    try? await Task.sleep(for: .seconds(UserDefaults.standard.bool(forKey: "loadAll") ? 40 : 6))
                    let from = UserDefaults.standard.integer(forKey: "showFromSeq")
                    let answers = model.chat(key).blocks.compactMap { b -> ChatRow? in if case .turn(let t) = b { t.answer } else { nil } }
                    let last = from > 0
                        ? answers.first { if case .assistant(let i) = $0 { i.seq > from } else { false } }
                        : answers.last
                    menu.anchor = CGPoint(x: 160, y: 30)
                    menu.selected = last?.id
                }
            }
            if let what = UserDefaults.standard.string(forKey: "showRewind") { // Rewind on the second-to-last message of yours: bar / ask / run
                Task {
                    try? await Task.sleep(for: .seconds(6))
                    let users = model.chat(key).blocks.flatMap { b -> [Item] in
                        guard case .turn(let t) = b else { return [] }
                        return t.users.compactMap { if case .user(let i) = $0 { i } else { nil } }
                    }
                    guard let pick = users.dropLast().last ?? users.last else { return }
                    if what == "ask" {
                        menu.rewinding = RewindAsk(id: pick.id, text: pick.text ?? "")
                    } else if what == "run" { // what Rewind Chat Only does, through the hub
                        menu.rewind?(pick.id, pick.text ?? "", .conversation)
                    } else {
                        menu.anchor = CGPoint(x: 260, y: 20)
                        menu.selected = pick.id
                    }
                }
            }
            if UserDefaults.standard.bool(forKey: "loadAll") {
                Task {
                    try? await Task.sleep(for: .seconds(3))
                    while model.chat(key).hasMore { await model.loadOlder(key) }
                    try? await Task.sleep(for: .seconds(1))
                    if UserDefaults.standard.integer(forKey: "showFromSeq") == 0 { position.scrollTo(edge: .bottom) } // oldest end
                }
            }
            #endif
    }

    /// Simulator automation: `-openAllWork YES` shows every work line unfolded (screenshots of the expanded state).
    #if DEBUG
    static let foldProbe = UserDefaults.standard.bool(forKey: "showFoldProbe") || UserDefaults.standard.bool(forKey: "showFreezeProbe")
    /// `-showBlankLanding YES`: every landing is thrown past the end of the list and its first look finds nothing drawn,
    /// as on the page of #1386, for `mendBlankLanding` to bring back (`ChatReenterTests`). (Thrown alone, the list comes
    /// back inside its content by itself, somewhere else, with rows drawn.)
    static let blankLanding = UserDefaults.standard.bool(forKey: "showBlankLanding")
    /// `-showAsUnread YES`: open the chat as if its answer had not been seen (where it lands).
    static let debugAsUnread = UserDefaults.standard.bool(forKey: "showAsUnread")
    /// `-showSwiftUIJumps YES`: the chat's jumps animate SwiftUI's scroll position, as before #5076, to measure the two
    /// ways in one build (`-showAutoScroll`'s sweep and hops).
    static let swiftUIJumps = UserDefaults.standard.bool(forKey: "showSwiftUIJumps")
    /// `-showEdgeScrub N`: Persona's conversation opens the right-edge navigator on the Nth newest message the user typed
    /// and keeps it open (screenshots of the new conversation layout, #1666).
    static let debugEdgeScrub = UserDefaults.standard.integer(forKey: "showEdgeScrub")
    #else
    static let debugAsUnread = false
    static let swiftUIJumps = false
    #endif

    #if DEBUG && os(iOS)
    /// `-showJumpPerf YES` (#5076): the chat's animated jumps through a long answer, three of each, every one measured
    /// (Documents/frames-jump-<kind>-<n>.json): ↓ from 8000 pt into the history (`end`), a message sent from there
    /// (`send`), from the newest end to a quote of the longest answer (its start, `quote`), to the oldest tool call
    /// (`call`) and to the oldest line on screen, as the navigator goes (`near`). With `-showSwiftUIJumps YES` as before.
    /// Documents/jump-perf.txt says where each one left the list.
    private func jumpPerf() async {
        try? await Task.sleep(for: .seconds(12)) // the history loads and the page settles
        let dir = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        var log: [String] = []
        defer { try? log.joined(separator: "\n").write(to: dir.appending(path: "jump-perf.txt"), atomically: true, encoding: .utf8) }
        let items = model.chat(shownKey).items
        guard let list = pin.list,
              let answer = items.filter({ $0.role == "assistant" && $0.kind == .text }).max(by: { ($0.text?.count ?? 0) < ($1.text?.count ?? 0) }),
              let call = items.first(where: { $0.kind == .toolUse && $0.toolUseID != nil })?.toolUseID else {
            return log.append("jump-perf: no list, answer or call")
        }
        func reading(at y: CGFloat) async { // as a finger leaves the list there
            PagePerf.touchers[ObjectIdentifier(pin)]?(list, true)
            list.setContentOffset(CGPoint(x: list.contentOffset.x, y: ChatJumpRun.newest(list) + y), animated: false)
            try? await Task.sleep(for: .milliseconds(300))
            PagePerf.touchers[ObjectIdentifier(pin)]?(list, false)
            try? await Task.sleep(for: .seconds(2))
        }
        func atNewest() async {
            setFollow { $0.follow() }
            scrollToEnd()
            try? await Task.sleep(for: .seconds(2))
        }
        func measure(_ kind: String, _ n: Int, _ act: () -> Void) async {
            let y0 = list.contentOffset.y
            FrameStats.shared.start()
            act()
            try? await Task.sleep(for: .seconds(1.5))
            FrameStats.shared.stop(label: "jump-\(kind)-\(n)")
            log.append("\(kind) \(n): from \(Int(y0 - ChatJumpRun.newest(list))) to \(Int(list.contentOffset.y - ChatJumpRun.newest(list))) pt from the newest end, at newest \(scratch.atNewest)")
        }
        for n in 1...3 {
            await reading(at: 8000)
            await measure("end", n) { jumpToEnd() }
            await reading(at: 8000)
            await measure("send", n) { log.append("sent \(model.sendNow(shownKey, text: "第 \(n) 次：再核对一遍"))") } // i18n-ignore
            await atNewest()
            await measure("quote", n) { _ = jumpToQuoted(ContextRef(kind: "quote", title: "", url: "", text: "", msg: answer.id)) }
            await atNewest()
            await measure("call", n) { jumpToCall(call) }
            await atNewest()
            let shown = Set(scratch.drawnLines)
            if let near = currentLines().first(where: { shown.contains($0.id) })?.id {
                await measure("near", n) { jumpToLine(near) }
            }
            await atNewest()
        }
        try? Data().write(to: dir.appending(path: "jump-perf-done"))
    }

    private func showEdgeScrub(_ n: Int) async {
        try? await Task.sleep(for: .seconds(4)) // the chat loaded and settled
        let marks = navigationMarks()
        guard scrub.open(marks, height: scratch.listFrame.height, top: headerRoom, at: 0) else { return }
        scrub.pick(max(0, marks.count - n))
    }
    #endif

    private static var debugOpenSupervision: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "showSupervisionOpen")
        #else
        false
        #endif
    }

    private static var debugOpenAll: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "openAllWork")
        #else
        false
        #endif
    }

    /// `-showPlanSheet YES`: the chat opens with its plan sheet up (screenshots).
    private static var debugPlanSheet: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "showPlanSheet")
        #else
        false
        #endif
    }

    /// `-showRunsOpen YES`: every run of tool chips starts open, for screenshots of the steps in it.
    private static var debugOpenRuns: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "showRunsOpen")
        #else
        false
        #endif
    }

    /// Simulator automation: `-showFromSeq N` renders from the turn holding the user message with that seq
    /// (scrolling a long lazy history to a turn drifts while rows above size themselves).
    private static func debugTrim(_ blocks: [ChatBlock]) -> [ChatBlock] {
        #if DEBUG
        let seq = UserDefaults.standard.integer(forKey: "showFromSeq")
        if seq > 0, let i = blocks.firstIndex(where: { b in
            if case .turn(let t) = b { return t.users.contains { if case .user(let u) = $0 { u.seq == seq } else { false } } }
            return false
        }) {
            var end = i + 1 // plus work background tasks woke the agent for right after it
            while end < blocks.count {
                if case .turn(let t) = blocks[end], !t.users.isEmpty { break } // up to the next message: recaps too
                end += 1
            }
            var out = Array(blocks[i..<end]) // near the newest end: no long scroll to reach it
            // `-dropUsers YES`: hide that turn's messages so its process row and answer fit one screenshot.
            if UserDefaults.standard.bool(forKey: "dropUsers"), case .turn(var t) = out[0] { t.users = []; out[0] = .turn(t) }
            return out
        }
        #endif
        return blocks
    }

    /// Quote puts the message over the box as a quote that goes with the next message (#986); Edit replaces the draft
    /// with your old message.
    private func bindMenu() {
        menu.quote = { [model, key] text, id, isUser, part in
            let from = isUser ? String(localized: "Me") : (model.session(key)?.agentLabel ?? "Claude")
            let quote = ContextRef.quote(text, from: from, msg: id, part: part)
            guard let quotes = ContextRef.adding(quote, to: model.composerContexts[key] ?? []) else { return }
            withAnimation(Theme.Motion.state) { model.composerContexts[key] = quotes }
            model.composerFocus = key // ready to type the reply, as in Telegram
        }
        if menu.swipeQuotes != (style == .dev) { menu.swipeQuotes = style == .dev }
        // A message that led with comments puts them back over the box as cards, the words in it (iOS; the web does the same).
        menu.edit = { [model, key] text in
            if let split = MessageComments.inBubble(text) {
                model.composerComments[key] = split.comments
                model.drafts[key] = [split.quote.map { $0.components(separatedBy: "\n").map { "> " + $0 }.joined(separator: "\n") }, split.rest]
                    .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: "\n\n")
            } else {
                model.drafts[key] = text
            }
        }
        menu.fork = { [model, key] itemID in
            Task {
                guard let new = await model.fork(key, at: itemID), model.session(key)?.assistant != true else { return }
                switchTo(new) // the Agents chat changes agents in place
            }
        }
        menu.canFork = { [model, key] in model.session(key)?.canFork == true }
        menu.rewind = { [model, key] itemID, text, restore in
            Task { await model.rewind(key, items: model.chat(key).items, at: itemID, text: text, restore: restore) }
        }
        menu.rewindBlock = { [model, key] in
            model.rewinding.contains(key) ? String(localized: "Rewinding") : Rewind.block(model.session(key))
        }
        menu.rewindOffer = { [model, key] in Rewind.offer(model.session(key)) }
        menu.session = { [model, key] in model.session(key) }
        menu.source = { [model, key] in
            let s = model.session(key)
            return [s?.spaceName, s?.slotLabel].compactMap { $0 }.joined(separator: " › ")
        }
        #if os(iOS)
        // Forward (#3807): the pressed message as a forward, to the picker.
        menu.forward = { [model, key, weak menu = menu] bar in
            guard let menu else { return }
            var draft = Self.forwardDraft(bar, key: key, model: model, menu: menu)
            draft.selects = true
            menu.forwarding = draft
        }
        // Select Messages (#9468): the long-press bar's row starts selecting with the pressed message picked; a reply's
        // questions are its turn's messages from the user's side (Include Questions).
        menu.select = { [pick] id in
            Keyboard.dismiss()
            withAnimation(Theme.Motion.standard) { pick.start(id) }
        }
        menu.selectable = { id in currentLines().contains { Self.pickBar($0)?.id == id } }
        pick.readQuestions = { [model, key] in PickQuestions.map(model.chat(key).blocks) }
        #endif
    }

    /// The pressed message as a forward: who wrote it and where, its text as this chat holds it (the whole of it read
    /// when it goes, if cut), your own message's pictures, and the same message as a share card.
    static func forwardDraft(_ bar: MessageBarInfo, key: SessionKey, model: AppModel, menu: MessageMenu) -> ForwardDraft {
        let s = model.session(key)
        let item = model.chat(key).items.first { $0.id == bar.id }
        let role: ForwardMessage.Role = bar.isUser ? .user : bar.from != nil ? .other : s?.assistant == true ? .persona : .agent
        let author = bar.from ?? (role == .persona ? "Persona" : s?.agentLabel ?? "Claude")
        let preferTab = s.map { model.prefersHerdrTabName(for: $0) } ?? false
        let primaryTab = s?.displayTabName(preferTab: preferTab)
        let project = s?.assistant == true ? nil : [s?.spaceName, primaryTab != nil ? nil : s?.slotLabel].compactMap { $0 }.joined(separator: " › ")
        let source = ForwardMessage.Source(key: key, item: bar.id, role: role, author: author, chat: s?.displayTitle(preferTab: preferTab) ?? "",
                                           project: project, machine: s?.assistant == true ? nil : model.machine(key.machineRef)?.name,
                                           time: bar.date ?? item?.ts)
        // The transcript's text, as the bubble or the source card reads it; what the bar has when the item is not loaded.
        let relayed = item.flatMap { MessageSource.of($0) } != nil
        let raw = item.map { relayed ? MessageSource.body($0) : ($0.text ?? bar.text) } ?? bar.text
        var full: (() async throws -> String)?
        if let item, item.fullSize != nil {
            full = { [model] in
                guard let api = model.api(key.group) else { throw CancellationError() }
                var whole = item
                whole.text = try await api.fullItem(key, id: item.id).text ?? ""
                whole.fullSize = nil
                whole.injected = nil
                return relayed ? MessageSource.body(whole) : whole.text ?? ""
            }
        }
        let share = menu.shareMessage(text: bar.text, isUser: bar.isUser, date: bar.date)
        // A forwarded photo is decoded off the main actor and no larger than a picked one, as the composer will send it
        // (`MessagePhotos.payload`; docs/apple/performance.md, P8).
        let side = MessagePhotos.maxSide
        return ForwardDraft(source: source, raw: raw, full: full, images: bar.isUser ? UserBubble.imagePaths(raw) : [],
                            loadImage: { [model, cwd = s?.cwd] path in
                                guard let data = try? await model.api(key.group)?.fileBytes(machine: key.machineID, path: path, cwd: cwd)
                                else { return nil }
                                return await Task.detached(priority: .userInitiated) { LarkMedia.decode(data, maxPixel: side) }.value
                            }, share: share)
    }

    /// Forward To has gone: share the message as an image, start a new Agent with it, or put it over the target's box.
    private func forwardDone() {
        guard let picked = forwardPicked else { return }
        forwardPicked = nil
        switch picked {
        case .share(let m): menu.sharing = m
        case .select(let id):
            #if os(iOS)
            Keyboard.dismiss()
            withAnimation(Theme.Motion.standard) { pick.start(id) }
            #else
            _ = id
            #endif
        case .newAgent(let refs, let images): forwardNewAgent = ForwardNewAgent(refs: refs, images: images)
        case .target(let refs, let images, let target): model.forward(refs, images: images, to: target, group: key.group)
        }
        #if os(iOS)
        // Forwarded: selecting is over. Forward To cancelled (nothing picked) comes back to the selection as it was.
        if case .select = picked {} else if pick.on { pick.end() }
        #endif
    }

    #if os(iOS)
    /// The message a line stands for while selecting (#3838); nil for lines that are not messages. A long answer's parts
    /// (#4970) are each a line of the one answer.
    private static func pickBar(_ line: ChatLine) -> MessageBarInfo? {
        switch line.content {
        case .row(let r): r.pickBar
        case .answerPart(let item, _, _, _): MessageBarInfo(id: item.id, text: item.text ?? "", isUser: false, date: item.ts, pending: false)
        default: nil
        }
    }

    /// Whether a line shows its message's mark: all but an answer's later parts, which pick with it.
    private static func pickMark(_ line: ChatLine) -> Bool {
        if case .answerPart(_, _, let index, _) = line.content { return index == 0 }
        return true
    }

    /// The footer, or while selecting the bar with Cancel, the count, Include Questions and Forward · Copy · Export Image in
    /// its place. The composer's words, quotes and pictures are the model's, so it comes back as it was.
    @ViewBuilder private var pickOrFooter: some View {
        if pick.on {
            PickBar(pick: pick, cancel: { withAnimation(Theme.Motion.standard) { pick.end() } }, forward: forwardPicks,
                    copy: copyPicks, export: exportPicks, refused: pickRefused)
        } else {
            footer
        }
    }

    private func pickRefused() {
        Haptics.error()
        model.toast = Toast(String(localized: "Up to \(ForwardMessage.most) messages at a time"), style: .error)
    }

    /// The picked messages as forwards, in the chat's order; with `questions`, with the questions they bring (#9468).
    private func pickedDrafts(questions: Bool) -> [ForwardDraft] {
        let ids = questions ? pick.ids.union(PickQuestions.brought(pick.ids, questions: pick.questions)) : pick.ids
        let shown = Dictionary(currentLines().compactMap { Self.pickBar($0) }.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        return model.chat(key).items.compactMap { ids.contains($0.id) ? shown[$0.id] : nil }
            .map { Self.forwardDraft($0, key: key, model: model, menu: menu) }
    }

    /// Forward: the picked messages (and their questions, with Include Questions) to Forward To. Selecting ends once a
    /// target is picked (`forwardDone`); Cancel there comes back to the selection.
    private func forwardPicks() {
        let drafts = pickedDrafts(questions: pick.include)
        guard !drafts.isEmpty else { return }
        menu.forwarding = ForwardDraft(pieces: drafts.flatMap(\.pieces), loadImage: drafts[0].loadImage,
                                       share: drafts.count == 1 ? drafts[0].share : nil)
    }

    /// Copy (#9468): the picked messages (and their questions) as text, each its whole, who wrote it over it; then
    /// selecting ends.
    private func copyPicks() {
        let pieces = pickedDrafts(questions: pick.include).flatMap(\.pieces)
        guard !pieces.isEmpty else { return }
        Task {
            do {
                let messages = try await ChatExportRenderer.messages(pieces)
                UIPasteboard.general.string = ExportText.copy(messages)
                Haptics.success()
                model.toast = Toast(String(localized: "Copied [Selected Messages]"), style: .success)
                withAnimation(Theme.Motion.standard) { pick.end() }
            } catch {
                Haptics.error()
                model.toast = Toast(error.isOffline ? String(localized: "Can't reach the hub") : error.shown ?? error.localizedDescription,
                                  style: .error)
            }
        }
    }

    /// Export Image (#9468): the preview of the picked messages as a picture; selecting stays, to export again or forward.
    private func exportPicks() {
        let without = pickedDrafts(questions: false), with = pickedDrafts(questions: true)
        guard let first = with.first else { return }
        let s = model.session(key)
        exporting = ExportRequest(title: s?.displayTitle(preferTab: s.map { model.prefersHerdrTabName(for: $0) } ?? false) ?? "",
                                  agent: s?.assistant == true ? model.assistantShownName(in: key) : (s?.agentLabel ?? ""),
                                  pieces: { questions in (questions ? with : without).flatMap(\.pieces) },
                                  loadImage: first.loadImage, width: menu.areaWidth > 0 ? menu.areaWidth : Self.phoneWidth,
                                  prefersLetterMark: AgentKind.prefersLetterMark(for: s))
    }

    /// A phone's width, for a picture drawn before the chat knows its own.
    private static let phoneWidth: CGFloat = 393
    #else
    private var pickOrFooter: some View { footer }
    #endif

    private func closedAgent(_ closed: SessionKey, order: [SessionKey]) {
        guard closed == key else { return }
        // Hosted, it was this one agent's page: it goes, rather than turning into a tab mate's.
        if host != nil { pane = .chat; return back() }
        if let next = AgentCloseRules.next(after: closed, in: order, available: model.siblings(of: key).map(\.key)) {
            switchTo(next)
        } else {
            pane = .chat
            back()
        }
    }

    private func switchTo(_ next: SessionKey) {
        guard next != key else { return }
        Haptics.tap()
        rememberReadPosition()
        if viewing != nil { model.unsubscribe(shownKey, by: owner); viewing = nil }
        model.unsubscribe(key, by: owner)
        actions.pendingReviewFallback = nil
        key = next
        entry = nil // a saved place not reached in the last chat is not looked for in this one
        scratch.entryPages = 0
        _ = takeMessageJump() // switched to by a hub link to one of its messages
        userScrolled = false
        settled = false
        setFollow { $0.follow() }
        settleAtEnd()
        bindMenu()
        menu.selected = nil
        if pane == .git || pane == .files { pane = .chat } // the next agent may live in another directory
        AgentMenuRouting.shared.store.clear(.init(session: key, cwd: session?.cwd ?? ""))
        position = ScrollPosition(edge: .top)
        model.subscribe(next, by: owner)
        model.show(next, by: owner)
        model.markSeen(next)
        if style == .dev {
            AppModel.rememberChat(next)
            #if os(iOS)
            model.chatSwitched[opened] = next == opened ? nil : next
            #endif
        }
    }

    private var preferHerdrTabName: Bool {
        guard style == .dev else { return false }
        return session.map { model.prefersHerdrTabName(for: $0) } ?? model.prefersHerdrTabName(for: key.group)
    }

    private var title: String {
        if backgroundActivity { return String(localized: "Background activity") }
        if style == .dev, let t = host?.title, !t.isEmpty { return t }
        if style == .dev, let s = session, let tab = s.displayTabName(preferTab: preferHerdrTabName) { return tab }
        let t = session?.title ?? ""
        if style == .assistant { return t.isEmpty || t == "Claude Code" ? "Persona" : t }
        return t.isEmpty ? String(localized: "Chat") : t
    }

    #if os(iOS)
    private var starredChatContext: StarredChatContext? {
        guard !ProcessInfo.processInfo.isiOSAppOnMac, shownKey.group == model.hub?.group,
              model.hubFeatures.contains(APIClient.starredMessagesFeature) else { return nil }
        return StarredChatContext(key: shownKey, conversation: style == .assistant ? .assistant : .agent,
                                  title: title, speaker: style == .assistant ? model.assistantShownName(in: shownKey) : (session?.agentLabel ?? ""),
                                  manager: session?.manager, api: model.api(shownKey.group))
    }
    #endif

    private var subtitle: String {
        guard let s = session else { return "" }
        if style == .assistant {
            if !model.interactions(for: key).isEmpty { return String(localized: "Waiting for you [Chat View 99e510]") }
            let launcher = model.assistantLaunchers[key] ?? s.launcher
            return s.status.isActive ? String(localized: "Working…") : ([launcher, Fmt.model(s.model)].compactMap { $0 }.joined(separator: " · "))
        }
        let preferTab = preferHerdrTabName
        let primaryTab = s.displayTabName(preferTab: preferTab)
        let titleDetail = s.displayTitleDetail(preferTab: preferTab)
        let machine = model.namedMachine(s.machineRef)?.title ?? s.machineID
        let place = [s.spaceName, primaryTab != nil ? nil : s.slotLabel].compactMap { $0 }.joined(separator: " › ")
        return [titleDetail, s.sourceSubtitle, machine, place.isEmpty ? nil : place, s.hasSource ? nil : s.agentLabel, s.stateLabel(muted: model.isMuted(s))].compactMap { $0 }.joined(separator: " · ")
    }

    /// The extension that opened this Persona conversation (#3758), when `item` is the message it opened it with: the
    /// conversation's first user message, once the page has loaded back to its start (with more to load, the first one
    /// here is not the first). nil for every other message and conversation. With it, which extension that was, whose
    /// own icon the line draws when it can (#10358).
    private func externalOpening(_ item: Item) -> (source: AutoConversationSource, opener: ConversationOpener?)? {
        guard style == .assistant, let source = AutoConversationSource.external(session?.origin), !store.hasMore,
              store.items.first(where: { $0.role == "user" && $0.kind == .text })?.id == item.id else { return nil }
        return (source, session.flatMap(ConversationOpener.of))
    }

    /// A user's line: a bubble, or what stands for it (a message from a source, one Persona injected, a loop's tick, a peer's).
    @ViewBuilder private func userRow(_ i: Item, showsSource: Bool, hidesPrompt: Bool) -> some View {
        if botmuxMode == .decoded, i.botmux?.isInbound == true {
            BotmuxMessagesView(item: i).equatable()
        } else if let source = MessageSource.of(i) {
            SourceMessage(item: i, source: source, showsSource: showsSource, hidesPrompt: hidesPrompt).equatable()
        } else if let n = Injected.of(i) {
            // Persona's message for the model: one line in its place, never its words (docs/protocol/protocol.md「Messages injected by Persona」).
            InjectedLine(item: i, injected: n, conversation: style == .assistant ? shownKey.sessionID : nil)
        } else if (i.text ?? "").hasPrefix("/loop") {
            LoopTickRow(item: i)
        } else if let from = i.from {
            PeerMessage(item: i, from: from)
        } else {
            let opening = externalOpening(i)
            UserBubble(item: i, external: opening?.source, opener: opening?.opener).equatable()
        }
    }

    @ViewBuilder private func rowView(_ row: ChatRow, isLatest: Bool, live: Bool, showsSource: Bool = true, hidesPrompt: Bool = false) -> some View {
        switch row {
        case .user(let i): userRow(i, showsSource: showsSource, hidesPrompt: hidesPrompt).chatMark("chat.user." + i.id)
        case .assistant(let i):
            VStack(alignment: .leading, spacing: 0) {
                if botmuxMode == .decoded, i.botmux?.kind == "reply" {
                    BotmuxMessagesView(item: i).equatable().chatMark("chat.answer." + i.id)
                } else {
                    AssistantText(item: i).equatable().chatMark("chat.answer." + i.id)
                }
                ReplyUsageFooter(item: i)
            }
        case .thinking(let i):
            if style == .dev && live { ThinkingRow(item: i) }
        case .tools(let steps):
            if style == .dev && live { ToolGroupView(steps: steps) } else { AssistantToolsRow(steps: steps) }
        case .notice(let i): NoticeRow(item: i)
        case .recap(let i): RecapCard(item: i)
        case .image(let i):
            if let p = i.image {
                HStack { if i.role == "user" { Spacer(minLength: 60) }; Base64Image(payload: p).scaledFrame(maxWidth: Theme.Size.box240) }
            }
        }
    }
}

struct ChatSkeleton: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var on = false
    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            ForEach(0..<5, id: \.self) { i in
                RoundedRectangle(cornerRadius: Theme.Radius.control)
                    .fill(on ? Theme.Fill.chip : Theme.Fill.well)
                    .scaledFrame(width: [220, 300, 180, 260, 140][i], height: Theme.Size.markS)
                    .frame(maxWidth: .infinity, alignment: i == 0 ? .trailing : .leading)
            }
        }
        .onAppear { if !reduceMotion { withAnimation(.easeInOut(duration: 0.9).repeatForever()) { on = true } } }
    }
}

/// The model request Claude is retrying now, above the composer. Its plain reason leads; the observed error stays as
/// secondary detail. Ordinary work, a null live event, turn end and subscription cleanup leave no note behind.
struct SessionLiveRetryNote: View {
    let key: SessionKey
    @Environment(AppModel.self) private var model

    var body: some View {
        if let retry = model.liveRetry(key) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.retry).font(Theme.Font.labelStrong).foregroundStyle(Theme.cautionText)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(retry.attemptText).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.cautionText)
                    Text(retry.error.reason).font(Theme.Font.secondary)
                    Text(retry.error.raw).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                        .textSelection(.enabled)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("chat.model-retry")
        }
    }
}

/// Claude's suggested next message over the composer: a tap puts it in the box to edit, Send Now (long press) sends it.
/// A session Persona is still starting (node/starting.go): what is sent now waits and goes in once it is ready. Or why
/// it did not start.
struct StartingNote: View {
    var error: String?
    var agent: String
    /// The node refused the start with a code (`Session.startErrorCode`, #1642): `error` is the hub's whole sentence in
    /// the user's language, its remedy last, shown in full under Didn't Start rather than cut after three lines.
    var coded = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            if let error, coded {
                ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.danger)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Didn't Start").font(Theme.Font.secondaryStrong).lineLimit(1)
                    Text(error).font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
            } else if let error {
                ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.danger)
                Text("Didn't start: \(error) [Chat View 38dec8]").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
            } else {
                ProgressView().controlSize(.mini)
                Text("Starting \(agent). Your message will be sent once it's ready.").font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(2)
            }
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("chat.starting")
    }
}

/// Why a session waits for the user, in its host's words (a botmux attention), with its Feishu topic one tap away.
/// Under it, the AskUserQuestion it waits on, read-only (#5889), when no card can answer it: a dialog only its computer
/// can answer still says here what it asks.
struct AttentionNote: View {
    var reason: String
    var link: URL?
    /// The chat whose open question it lists; read here, so only the note follows the chat's items (P5).
    var key: SessionKey? = nil
    @Environment(AppModel.self) private var model

    var body: some View {
        // A question a card can answer is listed there, not here too (#5937).
        let questions = key.flatMap {
            model.interactions(for: $0).isEmpty ? AskUserAnswers.pending(model.chat($0).items) : nil
        } ?? []
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.needsYou).font(Theme.Font.labelStrong).foregroundStyle(StatusPalette.needsYou.text)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text(reason).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
                ForEach(Array(questions.enumerated()), id: \.offset) { _, q in
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        Text(q.question).font(Theme.Font.captionMedium)
                        ForEach(Array(q.options.enumerated()), id: \.offset) { _, label in AskUserDetail.choice(label, picked: false) }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityIdentifier("chat.attention.question")
                }
            }
            Spacer(minLength: 0)
            if let link { FeishuLink(url: link).font(Theme.Font.secondaryStrong) }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.attention")
    }
}

/// What the post-resolution offer watches (#4348): the open chat and its pending-card ids together. An in-place chat
/// switch changes the key and ids in one value, so that transition can be told apart from the same chat's last card
/// disappearing without relying on the order of separate `onChange` modifiers in the same transaction.
private struct NextNeedsYouTrigger: Equatable {
    let key: SessionKey
    let ids: [String]
}

/// After this chat's card was answered, the next conversation that needs you: one tap goes there, nothing jumps by
/// itself (#4348). Same caution look as `AttentionNote`; it goes when this chat asks again or the other one stops.
struct NextNeedsYouStrip: View {
    var title: String
    var open: () -> Void

    var body: some View {
        Button(action: open) {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.needsYou).font(Theme.Font.labelStrong).foregroundStyle(StatusPalette.needsYou.text)
                Text("Next Needing You").font(Theme.Font.secondaryStrong).foregroundStyle(Color.primary).lineLimit(1)
                Text(title).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.forward).font(Theme.Font.label).foregroundStyle(.secondary)
            }
            .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
            .surface(interactive: true, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .contentShape(.rect(cornerRadius: Theme.Radius.card))
        }
        .buttonStyle(.plain).accessibilityElement(children: .combine).accessibilityIdentifier("chat.nextNeedsYou")
    }
}

/// Its host stopped the last turn on an error it will not retry by itself (a botmux notice): what went wrong, a retry
/// (a message to go on, delivered by the host) and its Feishu topic. Not a wait for the user: no 「需要你」.
struct FailureNote: View {
    var reason: String
    var link: URL?
    var retry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.labelStrong).foregroundStyle(Theme.danger)
                Text(Session.failureText(reason)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(3)
                Spacer(minLength: 0)
            }
            HStack(spacing: Theme.Space.m) {
                Button(action: retry) { Label("Retry [Components b8784c]", systemImage: Theme.Icon.retry).lineLimit(1) }
                    .font(Theme.Font.secondaryStrong)
                    .accessibilityIdentifier("chat.failure.retry")
                if let link { FeishuLink(url: link).font(Theme.Font.secondaryStrong) }
                Spacer(minLength: 0)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.failure")
    }

}

/// Open in Lark: the system opens it (Feishu takes its own links), not the chat's in-app web sheet.
struct FeishuLink: View {
    var url: URL

    var body: some View {
        Link(destination: url) { Label("Open in Lark [Chat View aa5c01]", systemImage: Theme.Icon.openExternal).lineLimit(1) }
            .environment(\.openURL, OpenURLAction { _ in .systemAction })
            .accessibilityIdentifier("chat.openFeishu")
    }
}

struct SuggestionPill: View {
    var text: String
    var risky: Bool = false
    var fill: () -> Void
    var send: () -> Void

    /// One policy for every direct-send affordance. The frozen Mac and unverified visionOS keep their existing behavior.
    var directSend: (() -> Void)? {
        #if os(iOS)
        return risky ? nil : send
        #else
        return send
        #endif
    }

    var body: some View {
        Button {
            Haptics.tap()
            fill()
        } label: {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.ai).font(Theme.Font.labelStrong).foregroundStyle(Theme.ai)
                Text(text).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
                Spacer(minLength: 4)
                Text("Use").font(Theme.Font.labelStrong).foregroundStyle(Theme.accent).lineLimit(1).fixedSize()
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .surface(tint: Theme.ai.opacity(0.06), interactive: true, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .contextMenu {
            if let directSend {
                Button { Haptics.success(); directSend() } label: { Label("Send Now", systemImage: "arrow.up.circle") }
            }
            Button(action: fill) { Label("Fill In", systemImage: Theme.Icon.insert) }
        } preview: {
            Text(text).font(Theme.Font.body).padding(Theme.Space.l).frame(maxWidth: 320, alignment: .leading)
        }
        .accessibilityLabel("Claude suggests: \(text)")
        .modifier(SuggestionAccessibility(send: directSend))
    }
}

/// Risky suggestions have no direct-send accessibility action and do not advertise one. Safe suggestions keep both.
private struct SuggestionAccessibility: ViewModifier {
    var send: (() -> Void)?

    @ViewBuilder func body(content: Content) -> some View {
        if let send {
            content
                .accessibilityHint("Fills the input box. Touch and hold to send right away.")
                .accessibilityAction(named: "Send Now", send)
        } else {
            content.accessibilityHint("Fills the input box.")
        }
    }
}


/// Which composer the chat draws where it is not the shell's choice (`usesShellComposer`).
enum ChatComposerKind {
    /// iPad and an open iPhone Duo keep their regular-width tabs and split, with the iPhone's
    /// expanding composer card in every style. Its width follows the page's readable width.
    static var padCard: Bool {
        #if os(iOS)
        true
        #else
        false
        #endif
    }

    /// The shell's composer where there is no shell: the iPad's.
    static var card: Bool { padCard }
}

struct Composer: View, Equatable {
    /// Where a refused send's card button goes (`ReadinessSheets`).
    @State private var failureRoute: ReadinessRoute?
    @Environment(\.scenePhase) private var phase
    /// What the page hands in; the rest it reads from the model itself. The page re-evaluates on every hub push for its
    /// conversation, and the composer (a text field, its buttons, the mode chips) used to go with it.
    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.key == b.key && a.status == b.status && a.placeholder == b.placeholder && (a.onCleared == nil) == (b.onCleared == nil)
            && a.covered == b.covered
            && a.assistantChrome == b.assistantChrome
            && (a.pickMode == nil) == (b.pickMode == nil)
            && (a.openAssistantModel == nil) == (b.openAssistantModel == nil)
    }

    @Environment(AppModel.self) private var model
    #if os(iOS)
    @Environment(\.replySpeechContext) private var replySpeechContext
    #endif
    @Environment(\.shellChat) private var shellChat
    @Environment(\.shell) private var shell
    /// The shell's composer (`ShellComposerBar`): the assistant's conversation and, in the shell, an agent's too, in every
    /// style (#1091). Other platforms retain `ComposerRow` / `PromptComposer` and their mode chips. On the Mac
    /// the card in every style but BeautifulUI's (its prompt bar is already one): the field, and under it Add, the model
    /// and its modes, send / stop, as Codex's and ChatGPT's desktop apps (#2113).
    private var usesShellComposer: Bool { shellChat || shell != nil || ChatComposerKind.card }
    /// The current assistant's saved prompts for the "/" lists (#9436); observed through the store, which changes only when
    /// the list does, never while typing.
    private var savedPrompts: SavedPromptStore { .shared }
    let key: SessionKey
    var status: SessionStatus
    var placeholder: String?
    /// The link card covers the chat (ChatView's `link`): a picture handed in (a preview's screenshot, #150) lands without
    /// raising the keyboard, which would close the card.
    var covered = false
    /// Unified Assistant conversation chrome (#16165): fuses queued messages and task routing into `ShellComposerBar`'s
    /// top edge and arbitrates secondary advisory banners through `ComposerBannerCoordinator` while keeping all critical
    /// prompts visible.
    var assistantChrome = false
    /// Glow While Replying (Settings › Appearance, off by default): the assistant's composer glows while its reply is
    /// being written (`working`), not while it waits on the user (`blocked`) and not in an Agent's chat.
    @StoredValue(ComposerGlowPrefs.enabledKey) private var glowEnabled = false
    private var glowsWhileReplying: Bool { glowEnabled && shellChat && status == .working }
    /// After /clear the pane runs a new session: the page follows it.
    var onCleared: ((SessionKey) -> Void)? = nil
    /// The box got or lost the keyboard.
    var onFocus: ((Bool) -> Void)? = nil
    /// A permission mode picked in the model menu goes through the page, which asks first where it must (#3719).
    var pickMode: ((String) -> Void)? = nil
    /// The page owns the conversation model sheet so both this row and a model-route card open the same picker.
    var openAssistantModel: (() -> Void)? = nil
    private var text: String {
        get { model.drafts[key] ?? "" }
        nonmutating set { model.drafts[key] = newValue }
    }
    private var textBinding: Binding<String> { Binding(get: { text }, set: { text = $0 }) }
    /// The failure notice Retry was tapped on: its note stays hidden (the host takes Continue, the list follows).
    @State private var retried: String?
    @State private var picks: [PhotosPickerItem] = []
    @State private var showPhotos = false
    @State private var showCamera = false
    @State private var moduleSheet: ModuleComposerAction? // a module's composer action whose sheet is up (Glasses Photo)
    @State private var showScan = false // Scan Document (#3720), only where the device can scan
    #if os(iOS)
    /// Add as Context › (#3721): the kind whose picker is open.
    @State private var contextPick: ContextPickKind?
    #endif
    /// The photos, videos and file uploads for the next message: the app keeps them per conversation, as it keeps the
    /// text (#2626), so a composer built again still has them.
    private var held: ComposerAttachments { model.composerAttachments(key) }
    private var images: [(UIImage, ImagePayload)] {
        get { held.images }
        nonmutating set { held.images = newValue }
    }
    private var videos: VideoAttachments { held.videos }
    private var files: FileAttachments { held.files }
    @State private var showFiles = false
    #if os(iOS)
    /// The iPhone's 「+」 panel (`AttachSheet`, Telegram's attachment menu).
    @State private var attachPanel = AttachPanel()
    #endif
    @State private var selection: TextSelection?
    @State private var declarations = ExtensionTextContributions()
    @State private var confirmClear = false
    @State private var clearing = false
    /// The draft a "/" row was picked into: the list stays closed for it until the draft changes.
    @State private var slashPicked: String?
    /// `/permissions` (#6513): the mode list it opens, and the mode picked there, applied through `pickMode` once the
    /// sheet is gone (the page's confirmation is an alert, which can't show over it).
    @State private var showPermissions = false
    @State private var permissionsPick: String?
    @FocusState private var focused: Bool
    /// The model menu's "Other Model…" (#1644): a model ID typed by hand, through POST …/modes.
    @State private var typingModel = false

    /// The composer's phone icon (docs/assistant/voice.md 6.1; #9887 one button: the one voice entry): only where a call
    /// can start. On the iPhone it calls this conversation through the system; elsewhere it is the voice chat in the app
    /// (the voice module's `ConversationVoice`). Why a call could not be placed shows over the app (`PersonaCalls.refusal`).
    private var voiceEntry: (() -> Void)? {
        guard model.voice?.offersCall(in: key) == true else { return nil }
        return { startVoice() }
    }

    /// `inApp`: the voice chat in the app whatever the device (DEBUG `-openVoice`).
    private func startVoice(inApp: Bool = false) {
        focused = false
        model.voice?.start(key, inApp: inApp)
    }

    /// Voice Chat in the 「+」 menu of the composers with no phone icon (Apple Vision Pro's: the Mac has no call audio yet,
    /// docs/assistant/voice.md 6.9). The iPhone's and the iPad's + have none: the phone icon is their one voice entry.
    @ViewBuilder private var voiceMenuItem: some View {
        if model.voice?.listsVoiceChat == true {
            Button { startVoice() } label: { Label("Voice Chat", systemImage: Theme.Icon.voiceChannel) }
                .accessibilityIdentifier("composer-menu-voice")
        }
    }

    /// The active modules' composer actions (Glasses Photo) in the + menus that list their rows themselves, after Take
    /// Photo; each opens its sheet (`ComposerCaptureSheets`). `Modules.registry` is built once at launch: a plain read.
    @ViewBuilder private var moduleMenuItems: some View {
        ForEach(Modules.registry.composerActions) { action in
            Button { moduleSheet = action } label: { Label(String(localized: action.title), systemImage: action.symbol).lineLimit(1) }
        }
    }

    /// The terminal /clear command belongs to Claude Code panes.
    private var canClear: Bool {
        guard let s = model.session(key) else { return false }
        return SessionModes.paneClaude(s)
    }

    private var allowsSend: Bool {
        guard let session = model.session(key), session.canSend, offlineManager == nil else { return false }
        return model.offlineAgent(session)?.canHold != false
    }
    /// The manager whose conversation this is, while a hub with several has it offline: nothing goes to it until it is
    /// back; its history reads.
    private var offlineManager: HubManager? {
        if let s = model.session(key) { return model.offlineManager(of: s) }
        // Not listed yet: the manager the Persona page opened it for (an inbox item's).
        return model.assistantItemManagers[key].flatMap { model.managerRosters[key.group]?.offlineManager(of: $0) }
    }
    /// A health conversation's files go to the health space's reports/ (docs/assistant/health.md「Checkup reports」), not the temp directory.
    private var uploadDest: String? { model.session(key)?.isHealth == true ? Session.healthSpace : nil }

    /// /loop for Claude agents (`LoopCommand.offered`, as on the web): in the quick commands, with a line saying what
    /// a /loop draft will do.
    private var canLoop: Bool {
        guard let s = model.session(key), s.canSend else { return false }
        return LoopCommand.offered(agent: s.agent, sourceKind: s.sourceKind, assistant: s.assistant)
    }

    /// Puts the command word before what is in the box, which becomes its task, and the caret right after the word for
    /// an interval (/loop needs a task before it goes). A draft that already is the command stays as it is.
    private func startDraft(_ word: String) {
        let rest = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !LoopCommand.isDraft(rest) { text = word + rest }
        selection = TextSelection(insertionPoint: text.index(text.startIndex, offsetBy: min(word.count, text.count)))
        focused = true
    }

    private var canSend: Bool {
        allowsSend && !held.busy && (!text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !images.isEmpty || !held.ready.isEmpty
            || !(model.composerContexts[key] ?? []).isEmpty || !(model.composerComments[key] ?? []).isEmpty)
    }

    /// Something rides along besides the text (photos, videos, files, quotes): the bar opens and sends without text.
    private var hasAttachments: Bool {
        !images.isEmpty || !videos.items.isEmpty || !files.items.isEmpty || !(model.composerContexts[key] ?? []).isEmpty || !(model.composerComments[key] ?? []).isEmpty
    }

    /// The launcher's list behind this session's model menus once read (#3754); nil: the fixed aliases.
    private var runningModels: [AgentModel]? { RunningModelLists.shared.models(key, model.session(key)) }

    /// An explicit model capability governs the picker (#6294). A node that takes picks offers its launcher's list once
    /// read. Without that capability, only Claude Code panes get the terminal picker. The "/" list uses `takesModelCommand`.
    private var canPickModel: Bool {
        guard let s = model.session(key) else { return false }
        if s.capabilities?.model != nil { return SessionModes.offersModels(s, list: runningModels) }
        return SessionModes.paneClaude(s)
    }

    /// The main Persona conversation is named by the hub's assistant snapshot; leaders and other Assistant chats
    /// carry the same assistant flag and can keep their own picks too (#9248).
    private var isMainAssistantChat: Bool { model.assistantMain == key }
    /// The hub keeps a conversation's own model pick (#5033); an older hub keeps nothing and the row stays read-only.
    private var assistantModelPickAvailable: Bool {
        AssistantModelChoice.pickerAvailable(assistant: isMainAssistantChat || model.session(key)?.assistant == true,
                                             keepsPick: model.keepsConversationModelPick(key.group))
    }
    private var assistantDefaultRule: AssistantModelChoice.DefaultRule {
        AssistantModelChoice.defaultRule(isMain: isMainAssistantChat,
                                         supervisor: model.isBackgroundSupervisor(key.sessionID),
                                         leader: model.assistantLeaders.contains(key.sessionID),
                                         origin: model.session(key)?.origin)
    }
    /// What this conversation's next message runs on: its own pick, its inheritance rule, then the running process.
    private var assistantEffective: AssistantModelChoice.Effective? {
        guard isMainAssistantChat || model.session(key)?.assistant == true else { return nil }
        // The response cache is unobserved; this small revision is the redraw signal after a fresh hub answer.
        _ = model.assistantLauncherRevision
        let rule = assistantDefaultRule
        return AssistantModelChoice.effective(
            session: model.session(key), response: model.cachedAssistantLaunchers(key.machineRef, main: true), rule: rule,
            superviseResponse: rule == .supervise
                ? model.cachedAssistantLaunchers(key.machineRef, purpose: "assistant.supervise") : nil)
    }
    /// The model name the composer title and the modes menu row both show, from the one effective setup.
    private var assistantEffectiveName: String? {
        assistantEffective.flatMap { AssistantModelChoice.displayName($0, response: model.cachedAssistantLaunchers(key.machineRef, main: true)) }
    }

    @ViewBuilder private var extensionPrompts: some View {
        if !declarations.prompts.isEmpty {
            Section("Extension Prompts") {
                ForEach(declarations.prompts) { prompt in
                    Button {
                        text = ExtensionTextContributions.filling(prompt)
                        selection = TextSelection(insertionPoint: text.endIndex)
                        focused = true
                    } label: {
                        Label { Text(prompt.title).lineLimit(1) } icon: { ThemedIcon(Theme.Icon.insert) }
                    }
                    .accessibilityIdentifier("extension-prompt-\(prompt.id)")
                }
            }
        }
    }

    private var promptCommands: [PromptCommand] {
        // The main conversation adds `/clear` (#5100). The quick messages, /loop, /permissions, saved prompts (#9436) and extension prompts stay.
        var commands = PromptItems.commands(clear: canClear && !offersMainClear && !status.isActive && !clearing, loop: canLoop,
                                            permissions: canPickPermissions)
            + PromptItems.saved(savedPrompts.prompts)
            + declarations.prompts.map {
            PromptCommand(String(localized: "/extension/\($0.title)", comment: "Menu name of a prompt an extension adds"), detail: $0.ownerName, insert: $0.text)
        }
        if offersMainClear, !commands.contains(where: { $0.id == MainClearCommand.word }) { // a saved "clear" leaves it (#11410)
            commands.append(PromptCommand(MainClearCommand.word, detail: String(localized: "Clear the chat and start over")))
        }
        return commands
    }

    /// Quick messages and /clear in the + menu (`ComposerRow`, and an agent's in the shell).
    /// The main conversation adds `/clear` (#5100), and it does not ask first.
    @ViewBuilder private var quickCommands: some View {
        Section("Quick Commands [Chat View c813e6]") {
            ForEach(SlashCommand.commands(loop: canLoop)) { cmd in
                Button {
                    if cmd.drafts { startDraft(cmd.text) } else { Task { await sendText(cmd.text) } }
                } label: { Label(cmd.title, systemImage: cmd.symbol) }
            }
            if offersMainClear {
                Button { Task { await runMainClear() } } label: {
                    Label("Clear Context /clear", systemImage: Theme.Icon.clearContext)
                }
                .disabled(model.mainClear.busy)
            } else if canClear {
                Button(role: .destructive) { confirmClear = true } label: {
                    Label("Clear Context /clear", systemImage: Theme.Icon.clearContext)
                }
                .disabled(status.isActive || clearing)
            }
        }
    }

    /// `/permissions` where the page can switch this session's mode (#6513): a picked mode goes through `pickMode`.
    private var canPickPermissions: Bool { pickMode != nil && SessionModes.offersPermissionsCommand(model.session(key)) }

    /// The "/" list for the draft (`SlashMenu`): an agent's chat in the composers without the prompt bar's own menu,
    /// and the main conversation's, which adds `/clear` to those rows (#5100).
    /// `/model` only where typing it switches the model (`Session.takesModelCommand`): a runtime's session that takes a
    /// model through its menus would get the words as a message (#8144). `/permissions` where the mode can be switched
    /// here (#6513); another Persona conversation lists it alone, never an Agent's quick messages (#7996 review).
    private var slashRows: [SlashMenuRow] {
        let plain = usesShellComposer || !AppStyle.current.usesBeautifulUI
        let assistant = model.session(key)?.assistant == true
        let agentList = !shellChat && !assistant && plain
        // The prompt bar has its own "/" menu; this list is the shell's and the plain input line's.
        let mainList = offersMainClear && plain
        let saved = plain ? savedPrompts.prompts : []
        if assistant, !mainList {
            var base = (!shellChat && plain && canPickPermissions) ? [SlashMenuRow.permissions] : []
            base += saved.map(SlashMenuRow.saved)
            guard !base.isEmpty, text.hasPrefix("/"), text != slashPicked else { return [] }
            return SlashMenuRow.matching(text, in: base)
        }
        return SlashMenuRow.slashRows(draft: text, picked: slashPicked, showsList: agentList || mainList,
                                       model: model.session(key)?.takesModelCommand == true, permissions: canPickPermissions,
                                       loop: canLoop, agentClear: canClear && !status.isActive && !clearing, mainClear: offersMainClear,
                                       saved: saved)
    }

    /// A "/" row picked: its text in the box to send or edit, as on the web; an agent's /clear asks first, as the + menu's.
    /// The main conversation's /clear runs at once; /permissions opens the mode list (#6513).
    private func pickSlash(_ row: SlashMenuRow) {
        if row == .permissions {
            text = ""
            showPermissions = true
            return
        }
        if row == .clear {
            text = ""
            if offersMainClear { Task { await runMainClear() } } else { confirmClear = true }
            return
        }
        text = row.text
        slashPicked = row.text
        selection = TextSelection(insertionPoint: row.text.endIndex)
        focused = true
    }

    /// The `@word` before the caret, for the @ list (`ComposerMentionMenu`, #2319): in the shell's composer while it has
    /// the keyboard, an agent's chat and Persona's alike, as on the web.
    private var mentionToken: MentionToken? {
        guard usesShellComposer, focused else { return nil }
        if TypedText.composingFocused { return nil } // Pinyin still marked: the list waits for the confirmed words
        return MentionToken.at(text, selection: selection)
    }

    /// What the @ list offers here: this chat's files and its session's folder, then Persona's workspaces and the other
    /// sessions. The frozen Mac and visionOS composers keep their existing resources.
    private var mentionSource: MentionSource {
        MentionSource(machine: key.machineRef, cwd: model.session(key)?.cwd,
                      files: { MentionRows.usedPaths(model.chat(key).items) }, except: key,
                      workspaces: offersWorkspaceMentions)
    }

    private var offersWorkspaceMentions: Bool {
        #if os(iOS)
        return uploadDest == nil && model.session(key)?.assistant == true
        #else
        return false
        #endif
    }

    /// The + menu's Mention: `@` at the caret, which opens the @ list (the web's @ button beside +).
    private var mentionMenuItem: some View {
        Button { typeMention() } label: { Label("Mention a File or Session", systemImage: Theme.Icon.mention).lineLimit(1) }
            .accessibilityIdentifier("composer-menu-mention")
    }

    private func typeMention() {
        // An unfocused field's selection is stale (a restored draft's): then at the end.
        let caret = focused ? PathInsert.range(selection, in: text)?.lowerBound : nil
        let r = MentionToken.typing(in: text, caret: caret ?? text.utf16.count)
        text = r.text
        selection = .cursor(utf16: r.caret)
        focused = true
    }

    /// A workspace or session picked in the @ list: its context sits over the box, once.
    private func quoteMention(_ ref: ContextRef) {
        guard let updated = ContextRef.addingMention(ref, to: contexts.wrappedValue) else { return }
        contexts.wrappedValue = updated
    }

    /// Effort and permission mode can change: Claude Code and the assistant (`SessionModeBar` shows them for the same).
    private var setsModes: Bool { model.session(key)?.canChangeModes == true }

    /// Only the effort can change: a Codex thread on its app-server (`Session.setsEffortOnly`).
    private var setsEffortOnly: Bool { model.session(key)?.setsEffortOnly == true }

    /// The model button's title; with nothing to show or pick, no button. The main conversation names its *effective*
    /// model (pick > Main chat default > running), so a hub-side change shows after the picker refreshes (#6542).
    private var modesTitle: SessionModes.ComposerTitle? {
        let effort = setsModes || setsEffortOnly
        if let name = assistantEffectiveName {
            return SessionModes.composerTitle(model: name, effort: effort ? SessionModes.effortLabel(model.session(key)?.effort) : nil)
        }
        return SessionModesMenu.title(model.session(key), setsModes: effort) ?? (effort || canPickModel ? .init(model: String(localized: "Model [Assistant View c98e11]")) : nil)
    }

    /// The @ menu: files this chat's tools touched, then other agents' sessions.
    private func mentions() -> [PromptSource] {
        PromptItems.files(model.chat(key).items, cwd: model.session(key)?.cwd ?? "")
            + PromptItems.sessions(model.sessionsNow, except: key)
    }

    /// Claude's suggested next message, while the box is empty and the agent waits.
    private var suggestion: String? {
        guard !status.isActive, text.isEmpty, images.isEmpty, videos.items.isEmpty, let s = model.session(key)?.suggestion, !s.isEmpty else { return nil }
        return s
    }

    private var contexts: Binding<[ContextRef]> {
        Binding(get: { model.composerContexts[key] ?? [] }, set: { model.composerContexts[key] = $0.isEmpty ? nil : $0 })
    }

    private var comments: Binding<[MessageComment]> {
        Binding(get: { model.composerComments[key] ?? [] }, set: { model.composerComments[key] = $0.isEmpty ? nil : $0 })
    }

    /// The notes over the box, the attachments and the box itself (`body` adds what happens around them). In a voice
    /// call the voice bar takes the box's place (docs/assistant/voice.md 6.2); what goes with a typed message waits, kept.
    private var composerStack: some View {
        composerNotes
            .readinessSheets($failureRoute)
            .modifier(MainReadinessRefresh(enabled: isMain))
            // The Claude quota beside ultracode's switch (#1307): kept no older than two minutes while the chat offers the
            // switch and is in front, so a menu opened after a long stay in the chat says where the quota is now.
            .tabActiveTask(id: ultracodeOffered && phase == .active) {
                guard ultracodeOffered, phase == .active else { return }
                await UsageWindowsStore.shared.keepFresh(model)
            }
    }

    /// This chat offers Claude Code's ultracode switch (#1307).
    private var ultracodeOffered: Bool { Ultracode.offered(model.session(key), features: model.features(key.group)) }

    /// Persona's main conversation: it carries the readiness card and the send gate (as the web, `useReadinessGate`).
    private var isMain: Bool { model.assistantMain == key && model.session(key)?.assistant == true }

    /// Clear context (#5100): the main conversation's only command, when the hub can. A turn does not hide it.
    private var offersMainClear: Bool { model.canClearMain(key) }

    /// Photos, files, videos, quotes and comments: `/clear` with any of them is a message.
    private var ridingAlong: Int {
        images.count + held.ready.count + videos.items.count + (model.composerContexts[key]?.count ?? 0) + (model.composerComments[key]?.count ?? 0)
    }

    /// Clears the main conversation. On a failure, `/clear` is back in the box.
    private func runMainClear() async {
        text = ""
        let ok = await model.clearMainContext()
        if !ok { text = MainClearCommand.word }
    }

    /// The main conversation cannot answer yet: send goes to what is missing, the draft stays.
    private var gate: SendGate? { isMain && !status.isActive ? model.readinessGate(route: $failureRoute, pickModel: openAssistantModel) : nil }

    private var composerNotes: some View {
        let voice = model.voice
        let inCall = voice?.isLive(on: key) ?? false
        let session = model.session(key)
        let showsUltracodeLost = session.map { Ultracode.lostShown($0, features: model.features(key.group)) } == true
        let showsModelFallback = session.flatMap { ModelFallbackText.card($0, headless: $0.assistant == true) } != nil
        let showsVoiceEnded = voice != nil && !inCall && voice?.showsEnded(on: key) == true
        let activeSuggestion = inCall ? nil : suggestion
        let advisory: ComposerAdvisoryBanner? = assistantChrome ? ComposerBannerCoordinator.advisory(for: .init(
            critical: .init(
                stoppedAgent: session?.isStopped == true,
                terminalOnly: session?.isTerminalOnly == true,
                readOnlySource: session.map { !$0.canSend && $0.sourceSubtitle != nil } == true,
                missingSession: session == nil && model.missingSessionReason(key) != nil,
                liveRetry: model.liveRetry(key) != nil,
                offlineAgent: offlineManager == nil && session.flatMap { model.offlineAgent($0) } != nil,
                managerOffline: offlineManager != nil,
                startingOrError: session.map { $0.starting == true || $0.startError != nil } == true,
                attention: session.map { !($0.attentionReason ?? "").isEmpty && $0.state == .needsYou } == true,
                failure: session.map { !($0.failureReason ?? "").isEmpty && !status.isActive && retried != $0.failureReason } == true,
                readinessOrFailure: isMain
                    ? (model.readiness.failure != nil || model.readiness.homeCardStep != nil
                       || model.readiness.probeError != nil || model.readiness.probe != nil)
                    : (session?.assistant == true && model.readiness.failure != nil),
                pendingConfirmation: !model.interactions(for: key).isEmpty
            ),
            advisories: .init(
                modelFallback: showsModelFallback,
                ultracodeLost: showsUltracodeLost,
                compaction: model.compactionNote(key) != nil,
                pushHealth: isMain && PushHealthStore.shared.notice(group: key.group) != nil,
                voiceEnded: showsVoiceEnded,
                suggestion: activeSuggestion != nil
            )
        )) : nil
        let allowAdvisory: (ComposerAdvisoryBanner) -> Bool = { banner in
            !assistantChrome || advisory == banner
        }
        return VStack(spacing: Theme.Space.s) {
            // Critical prompts below are never gated or suppressed by advisory arbitration.
            SessionLiveRetryNote(key: key)
            if offlineManager == nil, let session, let away = model.offlineAgent(session) {
                Text(verbatim: away.line)
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityIdentifier("chat-offline-agent")
            }
            if let m = offlineManager {
                ManagerOfflineNote(manager: m)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if let s = session, s.starting == true || s.startError != nil {
                StartingNote(error: s.startError, agent: s.agentLabel, coded: s.startErrorCode?.isEmpty == false)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
                SessionShellQuestion(session: s)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if let s = session, let reason = s.attentionReason, !reason.isEmpty, s.state == .needsYou {
                AttentionNote(reason: reason, link: s.externalLink, key: key)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if let s = session, let reason = s.failureReason, !reason.isEmpty, !status.isActive,
                      retried != reason {
                // Continue alone (not what is in the box); the note goes at once and stays gone for this notice, while the
                // host takes the message and the list catches up.
                FailureNote(reason: reason, link: s.externalLink) {
                    retried = reason
                    model.sendNow(key, text: "继续") // i18n-ignore
                }
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if isMain {
                // The main conversation: what it still needs, or a refused send (docs/assistant/onboarding.md 4.4, 5.2).
                ReadinessHomeCard(key: key, pickModel: openAssistantModel, route: $failureRoute)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            } else if session?.assistant == true, let f = model.readiness.failure {
                CodedFailureCard(failure: f, route: $failureRoute)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            // Secondary advisory banners share a single slot in Assistant conversations (`ComposerBannerCoordinator`).
            // A resume turned ultracode off (#1307): one line to turn it back on, or dismiss.
            if allowAdvisory(.ultracodeLost), let s = session, showsUltracodeLost {
                UltracodeLostNote(key: key, session: s)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            // After an upstream refusal the session runs on a fallback model (#4489): which, and the way back.
            if allowAdvisory(.modelFallback), let s = session, s.modelFallback != nil {
                ModelFallbackCard(key: key, session: s, working: status.isActive)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if isMain, allowAdvisory(.pushHealth) {
                PushHealthNotice(group: key.group)
            }
            if allowAdvisory(.compaction) {
                CompactionSlot(key: key) // an assistant conversation's Compact context (#1762)
            }
            if canLoop, let hint = LoopCommand.hint(text) {
                LoopDraftNote(hint: hint)
                    .transition(.opacity)
            }
            if allowAdvisory(.suggestion), let activeSuggestion {
                SuggestionPill(text: activeSuggestion, risky: session?.suggestionRisky == true) {
                    text = activeSuggestion
                    selection = TextSelection(insertionPoint: activeSuggestion.endIndex) // keep typing after it
                    focused = true
                } send: {
                    Task { await sendText(activeSuggestion) }
                }
                .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if !inCall {
                if !files.items.isEmpty { FileAttachmentsBar(files: files) }
                if !videos.items.isEmpty { VideoAttachmentsBar(videos: videos) }
                PasteImageChip(focused: focused) { add($0) }
                if !images.isEmpty {
                    ComposerImages.editable(held) // a tap marks it up before sending
                }
                #if os(iOS)
                if !(model.composerComments[key] ?? []).isEmpty {
                    CommentCardsRow(comments: comments)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
                #endif
                // In the stack, not over it: the composer sits in its own hosting controller, which takes no touches
                // outside its bounds (#518).
                let slash = slashRows
                if !slash.isEmpty {
                    SlashMenu(rows: slash, pick: pickSlash)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
                if let token = mentionToken {
                    ComposerMentionMenu(token: token, text: textBinding, selection: $selection, source: mentionSource, quote: quoteMention)
                        .transition(.move(edge: .bottom).combined(with: .opacity))
                }
            }
            // A call that ended by itself: 「语音已结束 · 继续」 over the box, until the page goes (docs/assistant/voice.md 1.2 L).
            if allowAdvisory(.voiceEnded), let voice, showsVoiceEnded {
                voice.endedLine(key: key)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if let voice, inCall {
                voice.bar(key: key, label: nil)
                    .transition(.opacity)
            } else if usesShellComposer {
                // The shell's composer (docs/apple/ios.md「New shell」), whatever the style: the assistant's and an agent's (#1091).
                ShellComposerBar(placeholder: status.isActive ? String(localized: "Add a note (queued)")
                                     : (suggestion ?? (shellChat ? String(localized: "Ask \(model.assistantShownName)") : placeholder ?? String(localized: "Message Claude"))),
                                 text: textBinding, selection: $selection, focused: $focused, canSend: canSend,
                                 hasAttachments: hasAttachments,
                                 stop: status.isActive && session?.canInterrupt == true ? { Task { await model.interrupt(key) } } : nil,
                                 voice: voiceEntry,
                                 voiceLabel: model.voice?.iconLabel(key),
                                 modesTitle: modesTitle,
                                 modesDisclosure: assistantModelPickAvailable,
                                 quotes: contexts,
                                 gate: gate,
                                 openAttach: attachOpener,
                                 glows: glowsWhileReplying,
                                 dictationHints: { [model, key] in DictationLanguage.ownerTexts(model.chat(key).items) },
                                 send: { deliverOrGate() }) {
                    #if os(iOS)
                    // The iPhone's tap opens the panel, its touch and hold this menu; the iPad's + is the menu, with
                    // Photos and Files first. The rest is the panel's More tab (#3225).
                    if attachOpener == nil {
                        Section {
                            Button { showPhotos = true } label: { Label("Photos", systemImage: Theme.Icon.photos) }
                            Button { showFiles = true } label: { Label("Files", systemImage: Theme.Icon.file) }
                        }
                    }
                    AttachMenuItems(sections: attachMoreSections)
                    #else
                    Button { showPhotos = true } label: { Label("Photos", systemImage: Theme.Icon.photos) }
                    if CameraPicker.isAvailable {
                        Button { showCamera = true } label: { Label("Take Photo [Chat View 6e3a10]", systemImage: Theme.Icon.camera) }
                    }
                    moduleMenuItems
                    Button { showFiles = true } label: { Label("Files", systemImage: Theme.Icon.file) }
                    mentionMenuItem
                    voiceMenuItem
                    extensionPrompts
                    if !shellChat || offersMainClear { quickCommands } // an agent's, as its composer offered them before the shell's; the main chat adds /clear (#5100)
                    #endif
                } modes: {
                    SessionModesMenu(key: key, session: session, picksModel: canPickModel, setsModes: setsModes,
                                     setsEffortOnly: setsEffortOnly, mainConversation: isMainAssistantChat,
                                     otherModel: canPickModel ? { typingModel = true } : nil,
                                     assistantPick: openAssistantModel,
                                     assistantModelName: assistantEffectiveName, pick: pickMode)
                } top: {
                    if assistantChrome { ComposerTopSlot(key: key) }
                }
            } else if AppStyle.current.usesBeautifulUI {
                // The BeautifulUI style's prompt bar; every other style keeps the original input line. Its direct
                // model action opens the main chat's Change Model sheet, including on macOS (#6542).
                PromptComposer(placeholder: status.isActive ? String(localized: "Add a note (queued)") : (suggestion ?? placeholder ?? String(localized: "Message Claude")),
                               text: textBinding, selection: $selection, focused: $focused,
                               hasAttachments: hasAttachments,
                               stop: status.isActive && model.session(key)?.canInterrupt == true ? { Task { await model.interrupt(key) } } : nil,
                               attach: PromptItems.attachments(camera: CameraPicker.isAvailable, modules: Modules.registry.composerActions)
                                   + (model.voice?.listsVoiceChat == true ? [PromptItems.voice] : []),
                               mentions: mentions,
                               commands: promptCommands,
                               models: canPickModel ? PromptItems.composerModels(running: model.session(key)?.model, list: runningModels) : [],
                               modelID: PromptItems.runningModelID(model.session(key)?.model, list: runningModels),
                               assistantModelName: assistantModelPickAvailable ? (assistantEffectiveName ?? String(localized: "Change Model…")) : nil,
                               assistantPick: openAssistantModel,
                               pickModel: { id in
                                   switch PromptItems.modelPick(id, running: model.session(key)?.model, list: runningModels) {
                                   case .typeID: typingModel = true // #1644: through …/modes
                                   case .keep: break
                                   // a session whose node takes the model (#6294): through …/modes, never as a message
                                   case .send where model.session(key)?.picksModelByModes == true: Task { await model.setModes(key, model: id) }
                                   case .send(let text): model.sendNow(key, text: text, images: []) // photos and quotes stay for the message
                                   }
                               },
                               onAttach: { source in
                                   // A PhotosPicker cannot present from inside the menu: these sheets hang off the composer.
                                   if let action = Modules.registry.composerAction(id: source.id) { moduleSheet = action; return }
                                   switch source.id {
                                   case "photo": showPhotos = true
                                   case "camera": showCamera = true
                                   case PromptItems.voice.id: startVoice()
                                   default: showFiles = true
                                   }
                               },
                               onMention: { source in
                                   guard let ref = PromptItems.quote(source, in: model.sessionsNow) else { return false }
                                   contexts.wrappedValue.append(ref)
                                   return true
                               },
                               onCommand: { command in
                                   // by id: a saved prompt of the same name types its text (#11410)
                                   switch PromptItems.ownCommand(command, permissions: canPickPermissions) {
                                   case .loop:
                                       // The bar puts its text in without moving the caret off the "/": what is typed
                                       // next went in the middle ("/10m check CIloop "). Ours, after the bar has
                                       // cleared the "/" it took.
                                       Task { @MainActor in startDraft(SlashCommand.loop.text) }
                                   case .permissions: showPermissions = true
                                   case .clear: if offersMainClear { Task { await runMainClear() } } else { confirmClear = true }
                                   case nil: return false
                                   }
                                   return true
                               },
                               quotes: contexts,
                               send: { // the bar clears the draft right after
                                   let t = text
                                   if offersMainClear, MainClearCommand.isCommand(t, attachments: ridingAlong) {
                                       Task {
                                           let ok = await model.clearMainContext()
                                           if !ok { text = t }
                                       }
                                       return
                                   }
                                   if let gate { gate.action(); Task { @MainActor in text = t }; return } // the draft stays
                                   if held.busy { return holdForUploads(restoring: t) }
                                   Task {
                                       let accepted = await sendText(t)
                                       if !accepted { text = t }
                                   }
                               })
            } else {
                ComposerRow(placeholder: status.isActive ? String(localized: "Add a note (queued)") : (suggestion ?? placeholder ?? String(localized: "Message Claude")),
                            text: textBinding, selection: $selection, focused: $focused, canSend: canSend,
                            stop: status.isActive && model.session(key)?.canInterrupt == true ? { Task { await model.interrupt(key) } } : nil,
                            send: { deliverOrGate() }, quotes: contexts,
                            gate: gate) {
                    // A PhotosPicker inside a Menu never presents (the menu dismisses first): present it from here.
                    Button { showPhotos = true } label: { Label("Photos", systemImage: Theme.Icon.photos) }
                    if CameraPicker.isAvailable {
                        Button { showCamera = true } label: { Label("Take Photo [Chat View 6e3a10]", systemImage: Theme.Icon.camera) }
                    }
                    moduleMenuItems
                    Button { showFiles = true } label: { Label("Files", systemImage: Theme.Icon.file) }
                    voiceMenuItem
                    extensionPrompts
                    quickCommands
                }
            }
        }
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("Composer")
        #endif
        // In parts: as one expression Swift 6.2 could not type-check it in time on the Mac (#2539).
        let top = AnyView(composerStack)
        .disabled(!allowsSend)
        .tabActiveTask(id: ExtensionTextSource(group: key.group, features: model.features(key.group),
                                               hub: model.api(key.group)?.config.url.absoluteString, active: phase == .active)) {
            guard phase == .active else { return }
            await declarations.refreshWhileActive(api: model.api(key.group))
        }
        // Voice (docs/assistant/voice.md 6.1): asks the hub about it, says why there is none, and the DEBUG switches (6.8).
        .background { model.voice?.composerSupport(key: key, active: phase == .active, offered: { voiceEntry != nil }, start: { startVoice(inApp: true) }) }
        #if DEBUG
        .task { // `-showSampleQuotes YES`: a page's text and another session quoted over the box (#986, screenshots)
            guard UserDefaults.standard.bool(forKey: "showSampleQuotes") else { return }
            try? await Task.sleep(for: .seconds(3))
            let have = contexts.wrappedValue
            contexts.wrappedValue += ContextRef.samples.filter { s in !have.contains { $0.url == s.url } } // the task may run again
        }
        #if os(iOS)
        .task { // `-showSampleComments YES`: a review's two code comments and a page's element over the box (#151, screenshots)
            guard UserDefaults.standard.bool(forKey: "showSampleComments") else { return }
            try? await Task.sleep(for: .seconds(3))
            if comments.wrappedValue.isEmpty { comments.wrappedValue = MessageComment.samples }
        }
        #endif
        .task { // `-openSlashMenu YES`: "/" in the box and its list open, for screenshots; `-openSlashMenu /re`: that draft
            guard let draft = UserDefaults.standard.string(forKey: "openSlashMenu") else { return }
            try? await Task.sleep(for: .seconds(4))
            text = draft.hasPrefix("/") ? draft : "/"
        }
        .task { // `-openPermissionModes YES`: the mode list `/permissions` opens (#6513), for screenshots
            guard UserDefaults.standard.bool(forKey: "openPermissionModes") else { return }
            try? await Task.sleep(for: .seconds(4))
            for _ in 0..<20 where !canPickPermissions { try? await Task.sleep(for: .seconds(1)) } // the session list loads
            if canPickPermissions { showPermissions = true }
        }
        .task { // `-openMentionMenu @Sources/`: that draft ("@" for any other value) with the keyboard up and its @ list open (#2319, screenshots)
            guard let draft = UserDefaults.standard.string(forKey: "openMentionMenu") else { return }
            try? await Task.sleep(for: .seconds(4))
            text = draft.hasPrefix("@") ? draft : "@"
            selection = .cursor(utf16: text.utf16.count)
            focused = true
        }
        .task { // `-showLoopDraft "/loop 10m check CI"`: that draft in the box, with its line (#2601, screenshots)
            guard let draft = UserDefaults.standard.string(forKey: "showLoopDraft") else { return }
            try? await Task.sleep(for: .seconds(4))
            for _ in 0..<20 where !canLoop { try? await Task.sleep(for: .seconds(1)) } // the session list loads
            if canLoop { text = draft }
        }
        .task { // `-focusComposer YES`: the keyboard up, for screenshots of the chat while typing
            if UserDefaults.standard.bool(forKey: "focusComposer") {
                try? await Task.sleep(for: .seconds(6))
                focused = true
            }
        }
        #endif
        .animation(Theme.Motion.standard, value: suggestion)
        .animation(Theme.Motion.standard, value: slashRows.isEmpty)
        .animation(Theme.Motion.standard, value: mentionToken == nil)
        .onChange(of: focused) { _, f in onFocus?(f) }
        .onDisappear { if focused { onFocus?(false) } } // gone with the keyboard (a subagent opened, another pane)
        .modifier(CompactionTracking(key: key))
        // A quote (#1058) or Message Persona (#17789) asks for the keyboard. Taken once this chat is in front and the app
        // is active: from a cold launch the field may be laid out before it can be first responder, so it tries briefly.
        .tabActiveTask(id: model.composerFocus == key) {
            guard model.composerFocus == key else { return }
            for _ in 0..<10 {
                focused = true
                try? await Task.sleep(for: .milliseconds(150))
                if focused || Task.isCancelled { break }
            }
            if model.composerFocus == key { model.composerFocus = nil }
        }
        #if DEBUG
        .task { // `-showClearConfirm YES`: the /clear confirmation, for screenshots
            guard UserDefaults.standard.bool(forKey: "showClearConfirm") else { return }
            try? await Task.sleep(for: .seconds(4))
            for _ in 0..<20 where !canClear { try? await Task.sleep(for: .seconds(1)) } // the session list loads
            confirmClear = canClear
        }
        #endif
        return AnyView(top)
        .accessibleConfirmationDialog("Clear Context?", isPresented: $confirmClear, titleVisibility: .visible) {
            Button("Clear [Chat View 1ef3de]", role: .destructive) {
                clearing = true
                Task {
                    let next = await model.clearContext(key)
                    clearing = false
                    if let next { onCleared?(next) }
                }
            }
        } message: {
            Text("\(model.session(key)?.agentLabel ?? "Claude") will start a new session. This chat stays in your history, and the page moves to the new session.")
        }
        // the hub's pictures without the session's agent: reading the session here would redraw the box on every row
        // update (P5); past them a pick goes as a file anyway
        .mediaPicker(isPresented: $showPhotos, selection: $picks, limit: model.imageLimits(key.group).perMessage + VideoAttachments.most)
        #if os(iOS)
        .modifier(attachHost)
        .task { if attachOpener != nil { AttachLibrary.prewarm() } }
        .sheet(item: $contextPick) { kind in
            ContextPickerSheet(kind: kind) { ref in
                if !contexts.wrappedValue.contains(where: { $0.title == ref.title && $0.text == ref.text }) { contexts.wrappedValue.append(ref) }
            }
        }
        #endif
        .fileUploadPicker(isPresented: $showFiles) { urls in
            files.start(urls, machine: key.machineRef, model: model, dest: uploadDest)
        }
        #if DEBUG
        .task { // `-openUploadDemo <name>[,<name>…]`: made-up files uploading (24 MB, then other sizes), for screenshots
            if let list = UserDefaults.standard.string(forKey: "openUploadDemo") {
                try? await Task.sleep(for: .seconds(3))
                let megabytes = [24, 2, 9, 1, 18, 5, 3, 40]
                let urls = list.split(separator: ",").enumerated().compactMap { pair -> URL? in
                    let (i, name) = pair
                    let dir = FileManager.default.temporaryDirectory.appending(path: "upload-demo/" + UUID().uuidString)
                    let url = dir.appending(path: String(name))
                    guard (try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)) != nil,
                          (try? Data(count: megabytes[i % megabytes.count] << 20).write(to: url)) != nil else { return nil }
                    return url
                }
                files.start(urls, machine: key.machineRef, model: model, dest: uploadDest, owned: true)
            }
        }
        #endif
        .modifier(ComposerCaptureSheets(camera: $showCamera, module: $moduleSheet, scan: $showScan, pages: { addScan($0) }) { add($0) })
        .sheet(isPresented: $typingModel) { SessionTypedModelSheet(key: key, session: model.session(key)) }
        // `/permissions` (#6513): the pick goes through the page once the sheet is gone, where its confirmation can show.
        .sheet(isPresented: $showPermissions, onDismiss: {
            guard let mode = permissionsPick else { return }
            permissionsPick = nil
            pickMode?(mode)
        }) {
            PermissionModeSheet(key: key) { permissionsPick = $0 }
        }
        // Keep the effective conversation model fresh so the composer title matches what the next message runs (#6542):
        // re-read when the conversation changes or Model/Assistant settings change. A transcript rev would cancel this
        // request every streaming tick, so the AppModel invalidates the cache from assistant snapshots instead.
        .task(id: "\(key.sessionID)-\(model.session(key)?.assistant == true)-\(model.assistantLauncherRequestRevision)") {
            guard assistantModelPickAvailable else { return }
            _ = try? await model.assistantLaunchers(key.machineRef, refresh: true, main: true)
            if assistantDefaultRule == .supervise {
                _ = try? await model.assistantLaunchers(key.machineRef, refresh: true, purpose: "assistant.supervise")
            }
        }
        .onChange(of: picks) { _, items in
            guard !items.isEmpty else { return }
            Task { await load(items) }
        }
        .onChange(of: model.composerInbox[key]?.count ?? 0, initial: true) { _, n in
            guard n > 0, let incoming = model.composerInbox[key] else { return }
            model.composerInbox[key] = nil
            for img in incoming { add(img) }
            if !covered { focused = true }
        }
    }

    /// The iPhone's 「+」 opens the panel (iPad and Vision Pro keep the menu); the keyboard goes down as the sheet comes up.
    private var attachOpener: ((CGRect) -> Void)? {
        #if os(iOS)
        attachPanel.opener(focused: $focused)
        #else
        nil
        #endif
    }

    #if os(iOS)
    private var attachHost: AttachSheetHost<AttachMoreRows> {
        // past the hub's pictures a pick goes as a file (`add`), so the panel takes as many as the files have room for too
        AttachSheetHost(panel: attachPanel, caption: textBinding,
                        photoRoom: model.imageLimits(key.group).roomLeft(holding: images.count) + max(0, FileAttachments.most - files.items.count),
                        videoRoom: max(0, VideoAttachments.most - videos.items.count),
                        camera: CameraPicker.isAvailable ? { showCamera = true } : nil,
                        files: { showFiles = true }, allPhotos: { showPhotos = true }, showsMore: true,
                        more: { AttachMoreRows(panel: attachPanel, sections: attachMoreSections) },
                        finish: attachFinished)
    }

    /// What + offers besides photos and files (#3225), one list for three places: the panel's More tab, the iPhone's touch
    /// and hold on +, and the iPad's + menu under Photos and Files. The most used first (the menu puts it nearest the
    /// finger): the camera and (Persona's main conversation) Meeting Recording, Mention, an Agent's Quick Commands, then
    /// extension prompts. No voice or call rows (#9887 one button): the composer's phone icon is the one voice entry. Rows a chat or device can never use are left out; /clear while the Agent works is dimmed.
    private var attachMoreSections: [AttachMoreSection] {
        var sections = AttachMoreSection.common(camera: CameraPicker.isAvailable ? { showCamera = true } : nil,
                                                scan: DocumentScanner.isAvailable ? { showScan = true } : nil,
                                                modules: Modules.registry.composerActions, open: { moduleSheet = $0 },
                                                // As the sidebar's (#3723): records the meeting with Recordings open, or opens the one running.
                                                // Not with the hub's recording module off (#15214).
                                                meeting: isMain && RecordingModule.allowance.offersWrites
                                                    ? { Task { await MeetingRecorder.shared.startMeeting(inForeground: true) } } : nil,
                                                mention: { typeMention() }, context: { contextPick = $0 })
        if !shellChat || offersMainClear {
            var rows = SlashCommand.commands(loop: canLoop).map { cmd in
                AttachMoreRow(id: "attach-command-\(cmd.title)", title: cmd.title, symbol: cmd.symbol, word: cmd.word) {
                    if cmd.drafts { startDraft(cmd.text) } else { Task { await sendText(cmd.text) } }
                }
            }
            if offersMainClear {
                rows.append(AttachMoreRow(id: "attach-main-clear", title: String(localized: "Clear Context /clear"), symbol: Theme.Icon.clearContext,
                                          word: "/clear", disabled: model.mainClear.busy) { Task { await runMainClear() } })
            } else if canClear {
                rows.append(AttachMoreRow(id: "attach-clear", title: String(localized: "Clear Context /clear"), symbol: Theme.Icon.clearContext,
                                          word: "/clear", destructive: true, disabled: status.isActive || clearing) { confirmClear = true })
            }
            sections.append(AttachMoreSection(id: "commands", title: String(localized: "Quick Commands [Chat View c813e6]"), rows: rows,
                                              symbol: Theme.Icon.quickCommands))
        }
        if let prompts = AttachMoreSection.prompts(declarations.prompts, fill: { prompt in
            text = ExtensionTextContributions.filling(prompt)
            selection = TextSelection(insertionPoint: text.endIndex)
            focused = true
        }) {
            sections.append(prompts)
        }
        return sections
    }

    /// What the panel handed back: picked photos and videos into the composer (and sent, from its Send), or a row's action.
    private func attachFinished(_ outcome: AttachOutcome) {
        switch outcome {
        case .then(let picks, let action):
            Task { await addPicked(picks) }
            action()
        case .closed(let picks):
            Task { await addPicked(picks) }
        case .send(let picks):
            Task {
                await addPicked(picks)
                // As the box's Send: /clear first, then the gate; videos still on their way hold the message and say so (sendText).
                if offersMainClear, MainClearCommand.isCommand(text, attachments: ridingAlong) {
                    await runMainClear()
                } else if let gate { gate.action() } else if allowsSend { await sendText(text) }
            }
        }
    }

    private func addPicked(_ picks: AttachPicks) async {
        await held.addPicked(picks, machine: key.machineRef, model: model, limits: limits, dest: uploadDest)
    }
    #endif

    /// Send, unless the draft is the main conversation's `/clear`, which clears even while a turn runs and past a send gate.
    private func deliverOrGate() {
        if offersMainClear, MainClearCommand.isCommand(text, attachments: ridingAlong) {
            Task { await runMainClear() }
            return
        }
        if let gate { gate.action() } else { Task { await sendText(text) } }
    }

    @discardableResult
    private func sendText(_ raw: String) async -> Bool {
        // `/permissions` typed and sent opens the mode list, as picking it does; nothing goes to the agent (#6513). With
        // anything attached it is a message (#7996 review).
        if SessionModes.opensPermissions(raw, offered: canPickPermissions, attached: hasAttachments) {
            if raw == text { text = "" }
            showPermissions = true
            return true
        }
        if offersMainClear, MainClearCommand.isCommand(raw, attachments: ridingAlong) {
            let restoring = raw
            text = ""
            let ok = await model.clearMainContext()
            if !ok { text = restoring }
            return ok
        }
        guard model.allowSend(key) else { return false }
        if held.busy { holdForUploads(); return false }
        let t = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        let ctx = model.composerContexts[key] ?? []
        let comments = model.composerComments[key] ?? []
        let files = held.ready
        guard !t.isEmpty || !images.isEmpty || !files.isEmpty || !ctx.isEmpty || !comments.isEmpty else { return false }
        // Picked page excerpts ride along as <persona-context> tags before what was typed, comments as `> ` blocks
        // after them (the web's order), videos and files as <persona-file> after the text, which stays as typed (#8361).
        // Comments alone need no words.
        let alone = t.isEmpty && images.isEmpty && files.isEmpty && !comments.isEmpty
        // A forward with no note (#3807) says so, rather than pointing at "the context".
        let forwards = ctx.filter(\.isForward).count
        let lone = forwards > 1 ? String(localized: "Please look at the forwarded messages above.")
            : forwards == 1 ? String(localized: "Please look at the forwarded message above.") : String(localized: "See the context above.")
        let typed = alone ? "" : t.isEmpty ? (images.isEmpty ? (files.isEmpty ? lone : AttachedFile.standIn(files)) : String(localized: "Please look at the image [Assistant View 5ad878]")) : t
        let body = MessageComments.message(comments, text: AttachedFile.message(typed, files: files))
        let message = ContextRef.message(ctx, body: body)
        // Routed to a task ("Tell “xx”"): straight to its agent, not to Persona (docs/assistant/unified-entry.md「指向谁」).
        // Not with videos: they are on this conversation's machine, the task's agent may be on another.
        if images.isEmpty, files.isEmpty, key.group == (model.hub?.group ?? ""), TasksStore.shared.sendRouted(key.sessionID, text: message) {
            if raw == text { text = "" }
            model.composerContexts[key] = nil
            model.composerComments[key] = nil
            return true
        }
        // On screen at once; the request runs in the background (a failure turns the bubble into "Resend").
        guard model.sendNow(key, text: message, images: images.map(\.1)) else { return false }
        if raw == text { text = "" }
        #if os(iOS)
        if text.isEmpty, key == model.assistantMain { replySpeechContext?.sentDraft() }
        #endif
        images = []
        held.sentFiles()
        model.composerContexts[key] = nil
        model.composerComments[key] = nil
        return true
    }

    /// A send while a video or a file is still on its way: nothing goes; what was typed goes back in the box (the
    /// prompt bar clears it right after asking to send).
    private func holdForUploads(restoring raw: String? = nil) {
        model.toast = Toast(held.waitNote)
        if let raw, text.isEmpty { Task { text = raw } }
    }


    private func load(_ items: [PhotosPickerItem]) async {
        let movies = items.filter(PickedMovie.isVideo) // sent as files (VideoAttach.swift)
        if !movies.isEmpty { videos.start(movies, machine: key.machineRef, model: model) }
        await MessagePhotos.load(items.filter { !PickedMovie.isVideo($0) }, model: model) { add($0) }
        picks = []
    }

    /// The pictures a message to this session may carry (the hub's, by its agent, #12973). Read in actions only: in `body`
    /// the session's row would redraw the box on every update (P5), so the panel and the picker take the hub's by group.
    private var limits: ImageLimits { model.imageLimits(key) }

    private func add(_ img: UIImage) {
        MessagePhotos.add(img, to: &images, model: model, limits: limits,
                          spill: MessagePhotos.spill(into: files, machine: key.machineRef, model: model, dest: uploadDest))
    }

    /// A scan's pages: the next message's photos while they fit, else one PDF uploaded as a picked file is (#3720).
    private func addScan(_ pages: [UIImage]) {
        #if os(iOS)
        // A PDF goes as the panel's Files do: a card above the box (the app's own copy, deleted once it is there).
        DocumentScanner.deliver(pages, room: limits.roomLeft(holding: images.count), model: model, add: { add($0) }) { urls in
            files.start(urls, machine: key.machineRef, model: model, dest: uploadDest, owned: true)
        }
        #endif
    }
}

/// The composer's "/" menu: Claude Code's own commands, and Persona' quick messages under a short word. Picking one
/// types `text` into the draft; send sends it.
struct SlashCommand: Identifiable {
    /// The + menu's entry (the original input line).
    var title: String
    var symbol: String
    /// The "/" word and its description (the BeautifulUI style's prompt bar).
    var word: String
    var detail: String
    var text: String
    /// Picking it only types `text` into the box, to be finished before sending (/loop); the others send at once from
    /// the + menu.
    var drafts = false
    var id: String { word }

    static let all: [SlashCommand] = [
        SlashCommand(title: String(localized: "Continue", comment: "Slash command title: let the agent keep going"), symbol: "forward", word: String(localized: "/continue", comment: "Slash command word the user can type"), detail: String(localized: "Let it keep going"), text: String(localized: "Continue")),
        SlashCommand(title: String(localized: "Compact Context /compact"), symbol: Theme.Icon.collapseAll, word: "/compact", detail: String(localized: "Compact context"), text: "/compact"),
        SlashCommand(title: String(localized: "View Context /context"), symbol: "chart.pie", word: "/context", detail: String(localized: "View context"), text: "/context"),
        SlashCommand(title: String(localized: "Commit and Push"), symbol: "arrow.up.doc", word: String(localized: "/commit", comment: "Slash command word the user can type"), detail: String(localized: "Commit by the conventions and push"), text: String(localized: "Commit the current changes following the conventions and push them", comment: "Message sent to the agent when the command is picked")),
        SlashCommand(title: String(localized: "Report Progress"), symbol: Theme.Icon.list, word: String(localized: "/report", comment: "Slash command word the user can type"), detail: String(localized: "Report progress and what's left"), text: String(localized: "Briefly report your progress so far and what's left", comment: "Message sent to the agent when the command is picked")),
    ]

    /// Claude Code's /loop (#2601): `/loop [interval] <what to do>` re-runs the prompt on a schedule.
    static let loop = SlashCommand(title: String(localized: "Repeat on a schedule /loop"), symbol: Theme.Icon.inProgress, word: "/loop", detail: String(localized: "Repeat a task on a schedule"),
                                   text: LoopCommand.text, drafts: true)

    /// What a session's composer offers: /loop only to Claude agents (`LoopCommand.offered`).
    static func commands(loop: Bool) -> [SlashCommand] { loop ? all + [Self.loop] : all }
}

/// Over the composer while the draft is a /loop command: what it will repeat and how often, or what it still lacks
/// (the web's line under its box, #1586).
struct LoopDraftNote: View {
    var hint: LoopHint

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.inProgress).font(Theme.Font.labelStrong)
                .foregroundStyle(hint.missing ? AnyShapeStyle(.tertiary) : AnyShapeStyle(Theme.loop))
            Text(hint.text).font(Theme.Font.secondary)
                .foregroundStyle(hint.missing ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.secondary))
                .lineLimit(2)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("chat.loopHint")
    }
}

/// A row of the list typing "/" opens over the composer, as the web's (`AGENT_COMMANDS` in web/src/views/chat/Composer.tsx):
/// the word, what it does and, for a quick message, the words it puts in the box. The BeautifulUI prompt bar has its own.
struct SlashMenuRow: Identifiable, Equatable {
    var word: String
    var detail: String
    var text: String
    /// A saved prompt's id (#9436): its row may share a word with a command of the chat's, and stays a row of its own.
    var savedID: String? = nil
    var id: String { savedID.map { "saved:\($0)" } ?? word }

    /// A saved prompt as a row (docs/apple/ios.md "Saved prompts"): its name after "/", **Saved** as where it comes from,
    /// and its text, which picking it types into the box.
    static func saved(_ p: SavedPrompt) -> SlashMenuRow {
        SlashMenuRow(word: "/" + p.name, detail: savedDetail, text: p.text, savedID: p.id)
    }

    /// A saved prompt's row says where it comes from (#9436), in both "/" lists.
    static let savedDetail = String(localized: "Saved [Slash Menu 9436a1]", comment: "Slash list: where a row comes from, the user's saved prompts")

    /// What a quick message sends, shown at the row's end; a command is its own text.
    var preview: String? { text.hasPrefix("/") ? nil : text }

    static let model = SlashMenuRow(word: "/model", detail: String(localized: "Switch model"), text: "/model ")
    /// Persona's own (#6513, the web's #6476): opens the permission mode list instead of going to the agent.
    static let permissions = SlashMenuRow(word: SessionModes.permissionsCommand, detail: String(localized: "Switch permission mode"), text: SessionModes.permissionsCommand)
    static let clear = SlashMenuRow(word: "/clear", detail: String(localized: "Clear the chat and start over"), text: "/clear")
    /// Picked from this list the draft is only the "/word" typed so far, so the row's text replaces it as any row's does.
    static let loop = SlashMenuRow(word: SlashCommand.loop.word, detail: SlashCommand.loop.detail, text: SlashCommand.loop.text)

    /// The web's order: its quick messages, then Claude Code's commands. A quick command added later goes after these.
    /// The words as `SlashCommand.all` has them (localized), so the order holds in every language.
    private static let order = [String(localized: "/continue", comment: "Slash command word the user can type"),
                                String(localized: "/commit", comment: "Slash command word the user can type"),
                                String(localized: "/report", comment: "Slash command word the user can type"), "/compact", "/context"]

    /// The rows an agent's chat offers: /model where the phone can pick one, /permissions where its mode can be switched
    /// (`SessionModes.offersPermissionsCommand`), /loop for a Claude agent (`LoopCommand.offered`), /clear where the pane
    /// takes it.
    static func rows(model: Bool, permissions: Bool = false, loop: Bool = false, clear: Bool) -> [SlashMenuRow] {
        let quick = SlashCommand.all.sorted { (order.firstIndex(of: $0.word) ?? order.count) < (order.firstIndex(of: $1.word) ?? order.count) }
        var out = quick.map { SlashMenuRow(word: $0.word, detail: $0.detail, text: $0.text) }
        if model { out.append(.model) }
        if permissions { out.append(.permissions) }
        if loop { out.append(.loop) }
        if clear { out.append(.clear) }
        return out
    }

    /// The composer's "/" list. The main conversation adds `/clear` to the rows it already had (#5100);
    /// an agent's own `/clear` stays where `rows` put it. The prompt bar does not use this list (`showsList` false).
    /// `saved`: the current assistant's saved prompts (#9436), after the chat's own commands, in their list's order; a chat
    /// with no commands of its own (`showsList` false) still lists them, as the web does.
    static func slashRows(draft: String, picked: String?, showsList: Bool, model: Bool, permissions: Bool = false, loop: Bool,
                          agentClear: Bool, mainClear: Bool, saved: [SavedPrompt] = []) -> [SlashMenuRow] {
        guard showsList || !saved.isEmpty, draft.hasPrefix("/"), draft != picked else { return [] }
        var listed = showsList ? rows(model: model, permissions: permissions, loop: loop, clear: agentClear && !mainClear) : []
        if showsList, mainClear, !listed.contains(where: { $0.word == Self.clear.word }) { listed.append(.clear) }
        listed += saved.map(Self.saved)
        return matching(draft, in: listed)
    }

    /// The rows for a draft: one word after a "/" at the start, matched at the start of the command or anywhere in its
    /// description; nothing for any other draft.
    static func matching(_ draft: String, in rows: [SlashMenuRow]) -> [SlashMenuRow] {
        guard draft.hasPrefix("/"), !draft.contains(where: \.isWhitespace) else { return [] }
        let query = draft.dropFirst().lowercased()
        guard !query.isEmpty else { return rows }
        return rows.filter { $0.word.dropFirst().lowercased().hasPrefix(query) || $0.detail.contains(query) }
    }
}

/// The "/" list over the composer: a tap puts the row's text in the box (it does not send). Past five and a half rows it
/// scrolls, the half row saying so: with the keyboard up a small phone has no room for all of them over the box.
struct SlashMenu: View {
    var rows: [SlashMenuRow]
    var pick: (SlashMenuRow) -> Void
    /// One row's height, as drawn (it follows the text size).
    @State private var rowHeight: CGFloat = 0
    private static let visibleRows: CGFloat = 5.5

    var body: some View {
        ScrollView {
            list
        }
        .scrollBounceBehavior(.basedOnSize)
        // Its rows' own height until one is measured, not what it is offered: the footer is laid out first (#3703).
        .fixedSize(horizontal: false, vertical: rowHeight == 0)
        .frame(height: rowHeight > 0 ? rowHeight * min(CGFloat(rows.count), Self.visibleRows) : nil)
        .padding(.vertical, Theme.Space.xs)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("slash-menu")
    }

    private var list: some View {
        VStack(spacing: 0) {
            ForEach(rows) { row in
                Button { pick(row) } label: {
                    HStack(spacing: Theme.Space.s) {
                        Text(row.word).font(Theme.Font.code).foregroundStyle(.primary).fixedSize()
                        Text(row.detail).font(Theme.Font.secondary).foregroundStyle(.secondary).truncationMode(.tail)
                        Spacer(minLength: Theme.Space.s)
                        if let preview = row.preview {
                            Text("「\(preview)」").font(Theme.Font.caption).foregroundStyle(.tertiary).truncationMode(.tail)
                                .layoutPriority(-1)
                        }
                    }
                    .lineLimit(1)
                    .padding(.horizontal, Theme.Space.m)
                    .scaledFrame(minHeight: Theme.Size.tapTarget)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(row.word)
                .accessibilityHint(row.detail)
                .accessibilityIdentifier("slash-menu.\(row.word)")
            }
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if !rows.isEmpty { rowHeight = h / CGFloat(rows.count) }
        }
    }
}

extension UIImage {
    /// Nonisolated: a scan's PDF is drawn off the main actor (`DocumentScanner.pdf`, #3720).
    nonisolated func scaledDown(maxSide: CGFloat) -> UIImage {
        let side = max(size.width, size.height)
        guard side > maxSide else { return self }
        let scale = maxSide / side
        let target = CGSize(width: size.width * scale, height: size.height * scale)
        // One pixel a point: at the screen's scale (the renderer's default) 1600 came out 4800 px on a phone, three
        // times the side meant, nine times the memory, in the composer until sent (#2626) and in the message.
        let format = UIGraphicsImageRendererFormat.default()
        format.scale = 1
        return UIGraphicsImageRenderer(size: target, format: format).image { _ in draw(in: CGRect(origin: .zero, size: target)) }
    }
}

/// The session's live terminal, come up from the bottom over the page (Terminal, or the terminal button of a reply): all
/// of the screen goes to it — no title, no tabs — under a slim bar that drags it back down (a tap on its handle or its
/// Close too), there in every state, a session that isn't running included (#4328).
/// Its size does not change with anything around it but the keyboard (a new size is a resize of the pane it drives).
/// A plain terminal opened from Terminals renames its tab from the bar's ⋯ (#6996), the same prompt as its row's.
struct TerminalCover: View {
    @Environment(\.dismiss) private var dismiss
    let key: SessionKey
    var switchTo: ((SessionKey) -> Void)?
    /// Resume in Terminal (#5521): resume the stopped Agent as the terminal comes up.
    var resumeOnOpen = false
    /// Rename… in the bar's ⋯: the tab, and what saves a new name (true once the computer took it). Nil: the bar keeps
    /// its settings button (an Agent's terminal: its chat's ⋯ renames it).
    var rename: Rename? = nil
    @State private var offset: CGFloat = 0
    @State private var renaming: TabRename.Target?
    /// The name saved here, which the next Rename… starts from.
    @State private var renamed: String?

    struct Rename {
        var target: TabRename.Target
        var save: (TabRename.Target, String) async -> Bool
    }

    /// The slim bar's drag, close and rename, apart from `body`: inline, the visionOS build gives up type-checking it.
    private var grab: TerminalGrab {
        TerminalGrab(
            dragged: { offset = max(0, $0) },
            released: { distance, predicted in
                if distance > 140 || predicted > 420 { close() } else { withAnimation(.spring(duration: 0.3)) { offset = 0 } }
            },
            close: close,
            rename: rename.map { r in { renaming = current(r) } })
    }

    /// How much the page under it is dimmed: less as the terminal goes down.
    private var dim: Double {
        let down: CGFloat = min(offset / 500, 1)
        return Double(0.4 * (1 - down))
    }

    var body: some View {
        TerminalPanel(key: key, switchTo: switchTo, resumeOnOpen: resumeOnOpen, grab: grab)
            .padding(.horizontal, Theme.Space.xs)
            .padding(.bottom, Theme.Space.xxs)
            .offset(y: offset)
            .background {
                // The page shows through, dimmed less as the terminal goes down.
                Color.black.opacity(dim).ignoresSafeArea()
            }
            .accessibilityAction(.escape) { close() } // VoiceOver's scrub, as on any sheet (#4328)
            .renameTerminalAlert($renaming) { t, name in
                guard let rename else { return }
                let before = renamed
                renamed = TabRename.cleaned(name)
                Task { if !(await rename.save(t, name)) { renamed = before } }
            }
    }

    private func current(_ r: Rename) -> TabRename.Target {
        var t = r.target
        if let renamed { t.name = renamed }
        return t
    }

    private func close() { dismiss() }
}

/// Above a stopped Agent's composer (#4754, docs/integrations/engines.md「Agents run in a terminal」): it takes no messages;
/// its terminal page shows the shell it left and offers Resume, the one way it runs again. Resume Session here opens that
/// page and resumes it there (#5521), as the web's line does; the words go above the two buttons, which stay on one line.
private struct StoppedAgentBar: View {
#if os(iOS)
    var session: Session
    var coordinator: CodexTakeoverCoordinator
#endif
    var openTerminal: () -> Void
#if os(iOS)
    var resume: (() -> Void)?
    var onSession: (Session) -> Void
#endif

    var body: some View {
        Group {
#if os(iOS)
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                HStack(spacing: Theme.Space.s) {
                    note
                    CodexTakeoverControl(session: session, coordinator: coordinator, onSession: onSession)
                }
                if let resume {
                    HStack(spacing: Theme.Space.s) {
                        terminal
                        Button(action: resume) {
                            Label("Resume Session", systemImage: Theme.Icon.resume).font(Theme.Font.labelStrong).lineLimit(1)
                        }
                        .themedButton(prominent: true)
                        .accessibilityIdentifier("chat.stopped.resume")
                    }
                } else {
                    HStack { Spacer(minLength: 0); terminal }
                }
            }
#else
            HStack(spacing: Theme.Space.s) {
                note
                terminal
            }
#endif
        }
        .accessibilityElement(children: .contain)
    }

    private var note: some View {
        Text("This Agent has stopped. Its conversation and terminal stay; it doesn't take messages.")
            .font(Theme.Font.caption).foregroundStyle(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var terminal: some View {
        Button(action: openTerminal) {
            Label("Terminal", systemImage: Theme.Icon.terminal).font(Theme.Font.labelStrong).lineLimit(1)
        }
        .themedButton()
        .accessibilityIdentifier("chat.stopped.terminal")
    }
}

/// The line over the composer of an Agent whose chat Persona cannot draw (`Session.isTerminalOnly`, #6170): what it is and
/// the terminal, which is its view.
private struct TerminalOnlyBar: View {
    var openTerminal: () -> Void

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            Text("Persona can't show it as a chat. Its terminal shows what it is doing.")
                .font(Theme.Font.caption).foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button(action: openTerminal) {
                Label("Terminal", systemImage: Theme.Icon.terminal).font(Theme.Font.labelStrong).lineLimit(1)
            }
            .themedButton(prominent: true)
            .accessibilityIdentifier("chat.terminalOnly.terminal")
        }
        .accessibilityElement(children: .contain)
    }
}

/// A queued message moved one place (#6088): the `keep` of `/queue` that re-queues them in the new order.
nonisolated enum QueueOrder {
    /// The original indexes in their new order with the one at `index` moved `step` places (-1 up, 1 down); nil when
    /// that is past either end.
    static func moved(count: Int, index: Int, by step: Int) -> [Int]? {
        let to = index + step
        guard step != 0, (0..<count).contains(index), (0..<count).contains(to) else { return nil }
        var order = Array(0..<count)
        order.remove(at: index)
        order.insert(index, at: to)
        return order
    }
}

/// Input typed while Claude works and not yet taken, shown above the composer instead of in the chat.
/// Folded to its one-line summary by default; opened, it shows about three rows and scrolls past them.
/// Editing pulls it back (like ↑ in the terminal); the other queued messages are re-queued in order.
/// A Claude in a terminal (`Session.steersQueue`, #6088) also takes them now (Send Now, at one tap since #7169; its
/// VoiceOver hint says what happens to what it is doing), and each moves up or down.
struct QueueStrip: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var queued: [String]
    /// The user's own messages come first; relayed ones are only shown and copied.
    var own: Int
    /// Messages sent from this phone while the agent worked that Claude has not reported queued yet (#1461): drawn
    /// after the queued ones as sending, waiting for the network or failed, with the actions each state has.
    var sending: [PendingMessage] = []
    private var total: Int { queued.count + sending.count }
    /// Local photo counts derived by `ChatStore` for the own rows; Agent attachment marks come from `QueuedDraft`.
    var photoMarks: [Int] = []
    var steers = false
    /// A Persona chat names the persona; an Agent chat names its agent (`agentName`).
    var persona: String? = nil
    /// The Agent chat's agent as its row names it (`Session.agentLabel`): Claude, Codex, a Paseo provider such as Grok
    /// (#8144).
    var agentName = "Claude"
    private var agent: String { persona ?? agentName }
    private let relays: [QueuedRelay?]
    @State private var busy = false
    @State private var expanded = false
    /// Messages from other conversations collapse into one line until the owner opens them (#6394).
    @State private var relayedExpanded = false
    /// One row's height with its spacing, as drawn (it follows the text size and the rows' line counts).
    @State private var rowHeight: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var typeSize
    private static let visibleRows: CGFloat = 3

    init(key: SessionKey, queued: [String], own: Int, sending: [PendingMessage] = [], photoMarks: [Int] = [],
         steers: Bool = false, persona: String? = nil, agentName: String = "Claude") {
        self.key = key
        self.queued = queued
        self.own = own
        self.sending = sending
        self.photoMarks = photoMarks
        self.steers = steers
        self.persona = persona
        self.agentName = agentName
        relays = queued.dropFirst(own).map { QueuedRelay($0) }
    }

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            ScrollView {
                rows
            }
            .scrollBounceBehavior(.basedOnSize)
            // Its rows' own height until one is measured, not what it is offered: the footer is laid out first (#3703).
            .fixedSize(horizontal: false, vertical: rowHeight == 0)
            .frame(height: rowHeight > 0 ? rowHeight * min(CGFloat(drawnCount), Self.visibleRows) : nil)
            .padding(.top, Theme.Space.sm)
        } label: {
            Group {
                if typeSize >= Theme.interfaceLargestTypeSize {
                    // At the interface's largest size, where the accessibility sizes land too, Send Now (which keeps its
                    // width) left the title a few characters and took the tap that opens the strip (#4846, #7730):
                    // unless both fit on one line, the title wraps and Send Now has the line under it. Smaller sizes
                    // keep the one line, the title cut short where it must be.
                    ViewThatFits(in: .horizontal) {
                        oneLine
                        VStack(alignment: .leading, spacing: Theme.Space.sm) {
                            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                                tray
                                title.multilineTextAlignment(.leading) // the label centers wrapped lines
                            }
                            sendNowButton
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                } else {
                    oneLine
                }
            }
            .font(Theme.Font.labelStrong)
            .foregroundStyle(.secondary)
            .contentShape(.rect)
        }
        .tint(.secondary)
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .transition(.move(edge: .bottom).combined(with: .opacity))
        .accessibilityIdentifier("queue-strip")
    }

    private var tray: some View { ThemedIcon("tray.and.arrow.down").accessibilityHidden(true) }
    private var title: Text {
        if relays.contains(where: { $0?.noWake == true }) { return Text("\(total) queued [Queue Strip]") }
        return Text("\(total) queued · \(agent) will handle them in order [Queue Strip]")
    }

    /// The tray, the title on one line and Send Now at the end.
    private var oneLine: some View {
        HStack(spacing: Theme.Space.sm) {
            tray
            title.lineLimit(1)
            Spacer(minLength: 0)
            sendNowButton
        }
    }

    /// Send Now when a Claude in a terminal has the user's own messages queued, or the spinner while the queue is being
    /// handed over. It sends at one tap, as on the web (#7169, #7154): nothing is lost, the messages only go now rather
    /// than at the next step, and Delete and Stop act at one tap too (`docs/design/DESIGN_SYSTEM.md` 4.4).
    @ViewBuilder private var sendNowButton: some View {
        if busy {
            ProgressView().controlSize(.mini)
        } else if steers, own > 0 {
            Button(action: sendNow) {
                Label("Send Now [Queue Strip]", systemImage: Theme.Icon.sendNow).lineLimit(1).fixedSize()
            }
            .buttonStyle(.plain)
            .foregroundStyle(Theme.accent)
            .accessibilityHint(sendNowHint)
            .accessibilityIdentifier("queue-send-now")
        }
    }

    /// What sending now does to the step under way, which a confirmation used to say before #7169.
    private var sendNowHint: Text {
        if let persona {
            return Text("\(persona) gets them right away instead of at its next step. What it is doing now stops, or goes on in the background when it can, and it starts again with your messages. [Queue Strip]")
        }
        return Text("Sends the queued messages to the agent now. A command it is running keeps running in the background; a reply it is writing starts again with your message. [Queue Strip]")
    }

    /// A message of this phone's not yet reported queued (#1461): its words, and what became of the send.
    private func sendingRow(_ msg: PendingMessage, number: Int) -> some View {
        let draft = QueuedDraft(msg.text, photoCount: msg.images.count)
        return Menu {
            switch msg.state {
            case .failed:
                Button { model.retry(key, msg) } label: { Label("Resend", systemImage: Theme.Icon.retry) }
            case .waiting:
                Button { model.retry(key, msg) } label: { Label("Retry Now [Message Views f40f4f]", systemImage: Theme.Icon.retry) }
            case .sending, .sent:
                EmptyView()
            }
            Button { UIPasteboard.general.string = draft.text.isEmpty ? msg.text : draft.text } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
            if msg.onlyHere {
                if msg.state == .waiting {
                    Button(role: .destructive) { model.discard(key, msg) } label: { Label("Don't Send", systemImage: Theme.Icon.delete) }
                } else {
                    Button(role: .destructive) { model.discard(key, msg) } label: { Label("Delete", systemImage: Theme.Icon.delete) }
                }
            }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(number)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    QueuedWords(draft: draft)
                    if !msg.images.isEmpty {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(Theme.Icon.photo)
                            Text("\(msg.images.count) images")
                        }
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                    switch msg.state {
                    case .sending:
                        Text("Sending… [Queue Strip]").font(Theme.Font.caption).foregroundStyle(.secondary)
                    case .waiting:
                        Text("Sends when connected").font(Theme.Font.caption).foregroundStyle(.secondary)
                    case .failed:
                        Text("Not sent. Tap to resend [Queue Strip]").font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    case .sent:
                        EmptyView()
                    }
                }
                Spacer(minLength: 0)
                ThemedIcon(msg.state == .failed ? Theme.Icon.errorFilled : Theme.Icon.more).font(Theme.Font.label)
                    .foregroundStyle(msg.state == .failed ? Theme.danger : .secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .opacity(msg.state == .failed || msg.state == .waiting ? 0.8 : 1)
        }
        .accessibilityLabel(Text("Item \(number): \(QueuedWords.spoken(draft))"))
        .accessibilityIdentifier("queue-sending-item")
    }

    /// Messages other conversations sent in, behind the owner's own in the queue (#4342).
    private var relayedCount: Int { max(queued.count - own, 0) }
    /// How many rows the opened strip actually draws, for measuring one row's height (a folded relayed block is one line).
    private var drawnCount: Int {
        own + sending.count + (relayedCount > 0 ? (relayedExpanded ? relayedCount : 1) : 0)
    }

    private var rows: some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            ForEach(0..<own, id: \.self) { i in
                ownRow(i)
            }
            if relayedCount > 0 {
                Button {
                    withAnimation(Theme.Motion.state) { relayedExpanded.toggle() }
                } label: {
                    HStack(spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.sendNow).font(Theme.Font.label).foregroundStyle(.secondary)
                        Text("\(relayedCount) messages from other conversations are waiting [Queue Strip]")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.expand)
                            .font(Theme.Font.captionStrong)
                            .foregroundStyle(.secondary)
                            .rotationEffect(.degrees(relayedExpanded ? 180 : 0))
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.s)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("queue-relayed-summary")
                .accessibilityLabel(Text("\(relayedCount) messages from other conversations are waiting [Queue Strip]"))
                if relayedExpanded {
                    ForEach(own..<queued.count, id: \.self) { i in
                        relayedRow(i)
                    }
                }
            }
            ForEach(Array(sending.enumerated()), id: \.element.id) { j, msg in
                sendingRow(msg, number: queued.count + j + 1)
            }
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if drawnCount > 0 { rowHeight = h / CGFloat(drawnCount) }
        }
    }

    /// One of the owner's own queued messages: edit, reorder, copy or delete.
    @ViewBuilder
    private func ownRow(_ i: Int) -> some View {
        let q = queued[i]
        let photos = photoMarks.indices.contains(i) ? photoMarks[i] : 0
        let draft = QueuedDraft(q, photoCount: photos)
        Menu {
            Button { act(i, delete: false) } label: { Label("Take Back to Edit", systemImage: Theme.Icon.edit) }
            if steers, own > 1 {
                Button { move(i, by: -1) } label: { Label("Move Up [Queue Strip]", systemImage: Theme.Icon.queueUp) }
                    .disabled(i == 0)
                    .accessibilityIdentifier("queue-move-up")
                Button { move(i, by: 1) } label: { Label("Move Down [Queue Strip]", systemImage: Theme.Icon.queueDown) }
                    .disabled(i == own - 1)
                    .accessibilityIdentifier("queue-move-down")
            }
            Button { UIPasteboard.general.string = draft.text.isEmpty ? q : draft.text } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
            Button(role: .destructive) { act(i, delete: true) } label: { Label("Delete", systemImage: Theme.Icon.delete) }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(i + 1)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    QueuedWords(draft: draft)
                    if photos > 0 {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(Theme.Icon.photo)
                            Text("\(photos) images")
                        }
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.more).font(Theme.Font.label).foregroundStyle(.secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .disabled(busy)
        .accessibilityLabel(Text("Item \(i + 1): \(QueuedWords.spoken(draft))"))
        .accessibilityIdentifier("queue-item")
        .accessibilityHint(Text("Edit, copy or delete"))
        .accessibilityValue(photos > 0 ? Text("\(photos) images") : Text(""))
    }

    /// One message relayed from another conversation: shown only, the only action is copy (#4342).
    @ViewBuilder
    private func relayedRow(_ i: Int) -> some View {
        let q = queued[i]
        let relay = relays[i - own]
        let draft = QueuedDraft(relay?.text ?? q)
        let source = relay.map { String(localized: "From \($0.source)") }
        let body = QueuedWords.spoken(draft)
        let words = [source, body].compactMap { $0 }.joined(separator: " · ")
        let noWake = relay?.noWake == true
        Menu {
            Button {
                let copy = QueuedDraft(q).text
                UIPasteboard.general.string = copy.isEmpty ? q : copy
            } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Text("\(i + 1)")
                    .font(Theme.Font.labelHeavy.monospacedDigit())
                    .foregroundStyle(Theme.accent)
                    .scaledFrame(width: Theme.Size.badge, height: Theme.Size.badge)
                    .background(Theme.accent.opacity(0.14), in: .circle)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    if let source { Text(source).font(Theme.Font.labelStrong).lineLimit(1) }
                    QueuedWords(draft: draft)
                    (noWake ? Text("Arrives with the next message [Queue Strip]") : Text("Arrives when this turn ends [Queue Strip]"))
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.more).font(Theme.Font.label).foregroundStyle(.secondary).padding(.top, Theme.Space.xs)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        }
        .disabled(busy)
        .accessibilityLabel(noWake
            ? Text("Item \(i + 1): \(words), arrives with the next message [Queue Strip]")
            : Text("Item \(i + 1): \(words), arrives when this turn ends [Queue Strip]"))
        .accessibilityIdentifier("queue-item")
        .accessibilityHint(Text("Copy [Readiness Views d37380]"))
    }

    private func act(_ i: Int, delete: Bool) {
        busy = true
        Task {
            await model.editQueued(key, index: i, delete: delete)
            busy = false
        }
    }

    private func move(_ i: Int, by step: Int) {
        busy = true
        Task {
            await model.moveQueued(key, index: i, by: step)
            busy = false
        }
    }

    private func sendNow() {
        busy = true
        Task {
            await model.sendQueuedNow(key)
            busy = false
        }
    }
}

/// A queued message in its row (#3022): a mark for the photos, videos and quotes it carries, then its words; never the
/// text Claude's queue keeps of them (`[Image #n]`, the photos' tag and paths).
struct QueuedWords: View {
    var draft: QueuedDraft

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            if !draft.imagePaths.isEmpty { mark(Theme.Icon.photo, draft.imagePaths.count) }
            if !draft.files.isEmpty { mark(Theme.Icon.video, draft.files.count) }
            if !draft.contexts.isEmpty { mark(Theme.Icon.quote, draft.contexts.count) }
            Text(draft.text.isEmpty ? Self.spoken(draft) : draft.text)
                .font(Theme.Font.secondary)
                .foregroundStyle(draft.text.isEmpty ? Color.secondary : Color.primary)
                .lineLimit(2)
                .multilineTextAlignment(.leading)
        }
        .accessibilityElement(children: .combine)
    }

    /// The count beside the mark only next to words: without them the words' place says it ("3 images").
    private func mark(_ icon: String, _ count: Int) -> some View {
        HStack(spacing: Theme.Space.xxs) {
            ThemedIcon(icon)
            if count > 1, !draft.text.isEmpty { Text(count, format: .number) }
        }
        .font(Theme.Font.label)
        .foregroundStyle(.secondary)
        .fixedSize()
    }

    /// What VoiceOver reads, and the row's words when it has none: what it carries, then its words.
    static func spoken(_ draft: QueuedDraft) -> String {
        var parts: [String] = []
        if !draft.imagePaths.isEmpty { parts.append(String(localized: "\(draft.imagePaths.count) images")) }
        if !draft.files.isEmpty { parts.append(String(localized: "\(draft.files.count) videos [Queue Strip]")) }
        if !draft.contexts.isEmpty { parts.append(ContextRef.summary(draft.contexts)) }
        if !draft.text.isEmpty { parts.append(draft.text) }
        return ListFormatter.localizedString(byJoining: parts)
    }
}

/// System camera for "Take Photo" in the composer.
struct CameraPicker: UIViewControllerRepresentable {
    /// Whether this device has a camera to take a picture with (Apple Vision Pro does not lend its cameras to apps).
    static var isAvailable: Bool {
        #if os(visionOS)
        false
        #else
        #if DEBUG
        // Simulator screenshots: `-showCameraRows YES` lists Take Photo as a phone does (the simulator has no camera to open).
        if UserDefaults.standard.bool(forKey: "showCameraRows") { return true }
        #endif
        return UIImagePickerController.isSourceTypeAvailable(.camera)
        #endif
    }

    var onImage: (UIImage) -> Void
    @Environment(\.dismiss) private var dismiss

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIViewController(context: Context) -> UIImagePickerController {
        let vc = UIImagePickerController()
        #if !os(visionOS) // never shown there: `isAvailable` is false
        vc.sourceType = .camera
        #endif
        vc.delegate = context.coordinator
        return vc
    }

    func updateUIViewController(_ vc: UIImagePickerController, context: Context) {}

    final class Coordinator: NSObject, UIImagePickerControllerDelegate, UINavigationControllerDelegate {
        let parent: CameraPicker
        init(_ parent: CameraPicker) { self.parent = parent }

        func imagePickerController(_ picker: UIImagePickerController, didFinishPickingMediaWithInfo info: [UIImagePickerController.InfoKey: Any]) {
            if let img = info[.originalImage] as? UIImage { parent.onImage(img) }
            parent.dismiss()
        }

        func imagePickerControllerDidCancel(_ picker: UIImagePickerController) { parent.dismiss() }
    }
}

/// Unified Session Overview wrapper retaining the `SessionInfoSheet` type name for existing call sites (e.g.
/// `AssistantTopBar`), ordered **Environment → Context → Subagents → Background tasks** (#27, #14322).
struct SessionInfoSheet: View {
    let key: SessionKey
    var session: Session?
    /// Opened from the Persona tab, where the conversation may not be in the fleet's list yet (`session` is nil then).
    var assistant = false
    @State private var endedExpanded = false

    var body: some View {
        NavigationStack {
            SessionOverviewSheet(
                key: key,
                session: session,
                assistant: assistant,
                endedExpanded: $endedExpanded
            )
        }
    }

    /// "1.25K tokens": the count as the Machines page and the web write it (TokenFormat), with a localized unit (#9205).
    static func tokens(_ n: Int) -> String { String(localized: "\(TokenFormat.count(n)) tokens") }
}

/// A finished exchange: messages, the folded process, the answer.
/// One line of the chat. A turn renders as: its messages (queued ones gathered), a thin "Working 39s / Worked 5 min"
/// line that folds the work, then — when open — the agent's words, narration in gray and each stretch of tool calls
/// as "Used N tools"; finished turns are folded and keep just the answer. Lines are separate lazy rows on purpose.
/// One line of the chat. A turn reads as exchanges: each message, one quiet work line for what the agent did after
/// it ("Used 3 tools, thought once · 1 min 20 s ›", tap to see every step), then the agent's last words before the
/// next message. Running and finished turns look the same; the running exchange's work line ticks. Lines are
/// separate lazy rows on purpose (a running turn can have hundreds of steps).
struct ChatLine: Identifiable {
    let id: String
    let content: Content
    /// Computed after the final row ordering, not in a lazy view's appearance callbacks.
    var showsSource = true
    /// A scheduled task's or a follow-up's input in the main conversation while background rounds fold: its source
    /// block shows the one-line notice, never the prompt (`ChatLines.hidesPrompt`).
    var hidesPrompt = false
    /// Friendly names computed by the model-change factory, never by a row's body.
    var modelNames: (from: String, to: String)?
    /// The first visible line of a photo-only or wake exchange (`ChatLines.flatLines`).
    var startsExchange = false
    /// A child revealed from a folded supervision/background round; still not a visible reply for model comparison.
    var inFoldedRound = false

    indirect enum Content {
        /// One compact header for the entire quiet round; children are flattened only when it is opened. `source`: the
        /// input's source for a background round (docs/apple/ios.md "Background rounds"), nil for a supervision round.
        case supervision(id: String, lines: [ChatLine], live: Bool, source: MessageSource?)
        case supervisionInput(Item)
        case row(ChatRow)
        case work(id: String, items: [WorkItem], live: Bool, start: Date?, end: Date?)
        case userImages(id: String, items: [Item])
        /// Two or more "while you were away" recaps in a row, as one line (they open in one drawer, newest first).
        case recaps(id: String, items: [Item])
        case workStep(group: String, item: WorkItem, first: Bool)
        /// A running subagent's card under its folded work line (#5602, as the web): it stays in view while the line is
        /// shut, by the id it has as a step of the open line, so opening the line does not draw it again.
        case agentOut(group: String, call: ToolCall)
        /// Subagents launched one after another, their cards in one grid (`SubagentGrid`); `alone`: the running ones
        /// out of their folded work line.
        case agents(group: String, calls: [ToolCall], first: Bool, alone: Bool)
        /// Two or more tool calls in a row of an open work line, as one line of chips.
        case toolRun(id: String, calls: [ToolCall], open: Bool, first: Bool)
        /// The foot of an open work card: Collapse, and the files the work changed (`ChatLines.changedFiles`).
        case workEnd(group: String, count: Int, files: [ChangedFile])
        /// A model API error (`APIErrorInfo`), repeated `count` times in a row.
        case apiError(item: Item, info: APIErrorInfo, count: Int)
        /// What the hub added to an assistant conversation: a plan, a task, a receipt… (docs/assistant/unified-entry.md).
        case feed(FeedItem)
        /// A reminder or an event the assistant added on the phone, under its turn's work line (QuickResult).
        case quick(QuickResult)
        /// Where the phone is, or the places a search found, on a map under its turn's work line (PlaceMapResult, #5543).
        case placeMap(PlaceMapResult)
        /// The date over the first line of a day (the assistant's conversation).
        case day(Date)
        /// The images an exchange's work looked at, between it and the answer (`ViewedImages`, docs/protocol/protocol.md「Images viewed this turn」).
        case viewed(id: String, images: [ViewedImage])
        /// The files a completed Agent exchange changed, shown after its final answer (`TurnChanges`, #12016).
        case turnChanges(id: String, changes: TurnChangeSet)
        /// Declarative extension views emitted out-of-band by a turn's tool calls (`ToolViews`).
        case toolViews(id: String, views: [ExtensionView])
        /// A part of a long finished answer, a row of its own (#4970, `AnswerParts`): the first has the answer's id.
        case answerPart(item: Item, part: AnswerParts.Part, index: Int, count: Int)
        /// A permission mode change the hub recorded, at the end of the exchange it happened in (#10297,
        /// `ChatLines.mergeModeChanges`); `id` from its time and kind.
        case modeChange(id: String, change: APIClient.ModeChange)
        /// The reply models on either side of a visible exchange boundary (#5055, `ModelChanges.mark`).
        case modelChange(id: String, from: String, to: String)

        var id: String {
            switch self {
            case .supervision(let id, _, _, _): id
            case .supervisionInput(let item): item.id
            case .row(let r): r.id
            case .work(let id, _, _, _, _): id
            case .workStep(let group, let item, _): group + "/" + item.id
            case .agentOut(let group, let call): group + "/" + WorkItem.step(.call(call)).id
            case .agents(let group, let calls, _, _): group + "/agents-" + (calls.first?.id ?? "")
            case .workEnd(let group, _, _): group + "-end"
            case .toolRun(let id, _, _, _): id
            case .userImages(let id, _): id
            case .recaps(let id, _): id
            case .apiError(let item, _, _): item.id
            case .feed(let f): "feed-" + f.id
            case .quick(let q): "quick-" + q.callID
            case .placeMap(let m): "map-" + m.callID
            case .day(let d): "day-\(Int(d.timeIntervalSince1970))"
            case .viewed(let id, _): id
            case .turnChanges(let id, _): id
            case .toolViews(let id, _): id
            case .answerPart(let item, _, let index, _): index == 0 ? item.id : item.id + "/part-\(index)"
            case .modeChange(let id, _): id
            case .modelChange(let id, _, _): id
            }
        }
    }

    private init(_ content: Content) {
        id = content.id
        self.content = content
    }

    private init(id: String, content: Content) {
        self.id = id
        self.content = content
    }

    /// `line` drawn as `row` under its own id: the list keeps its place (`ChatLines.hidingCloseout`).
    static func replacing(_ line: ChatLine, with row: ChatRow) -> Self { Self(id: line.id, content: .row(row)) }

    static func supervision(id: String, lines: [ChatLine], live: Bool, source: MessageSource? = nil) -> Self {
        Self(.supervision(id: id, lines: lines, live: live, source: source))
    }
    static func supervisionInput(_ item: Item) -> Self { Self(.supervisionInput(item)) }
    static func row(_ row: ChatRow) -> Self { Self(.row(row)) }
    static func work(id: String, items: [WorkItem], live: Bool, start: Date?, end: Date?) -> Self {
        Self(.work(id: id, items: items, live: live, start: start, end: end))
    }
    static func userImages(id: String, items: [Item]) -> Self { Self(.userImages(id: id, items: items)) }
    static func recaps(id: String, items: [Item]) -> Self { Self(.recaps(id: id, items: items)) }
    static func workStep(group: String, item: WorkItem, first: Bool) -> Self {
        Self(.workStep(group: group, item: item, first: first))
    }
    static func agentOut(group: String, call: ToolCall) -> Self { Self(.agentOut(group: group, call: call)) }
    static func agents(group: String, calls: [ToolCall], first: Bool, alone: Bool) -> Self {
        Self(.agents(group: group, calls: calls, first: first, alone: alone))
    }
    static func workEnd(group: String, count: Int, files: [ChangedFile] = []) -> Self {
        Self(.workEnd(group: group, count: count, files: files))
    }
    static func apiError(item: Item, info: APIErrorInfo, count: Int) -> Self { Self(.apiError(item: item, info: info, count: count)) }
    static func feed(_ item: FeedItem) -> Self { Self(.feed(item)) }
    static func quick(_ result: QuickResult) -> Self { Self(.quick(result)) }
    static func placeMap(_ result: PlaceMapResult) -> Self { Self(.placeMap(result)) }
    static func day(_ date: Date) -> Self { Self(.day(date)) }
    static func modeChange(id: String, _ change: APIClient.ModeChange) -> Self { Self(.modeChange(id: id, change: change)) }
    static func modelChange(id: String, from: String, to: String) -> Self {
        var line = Self(.modelChange(id: id, from: from, to: to))
        line.modelNames = (replyModelName(from), replyModelName(to))
        return line
    }
    static func viewed(id: String, images: [ViewedImage]) -> Self { Self(.viewed(id: id, images: images)) }
    static func turnChanges(id: String, changes: TurnChangeSet) -> Self { Self(.turnChanges(id: id, changes: changes)) }
    static func toolViews(id: String, views: [ExtensionView]) -> Self { Self(.toolViews(id: id, views: views)) }
    static func answerPart(item: Item, part: AnswerParts.Part, index: Int, count: Int) -> Self {
        Self(.answerPart(item: item, part: part, index: index, count: count))
    }
    static func toolRun(id: String, calls: [ToolCall], open: Bool, first: Bool) -> Self {
        Self(.toolRun(id: id, calls: calls, open: open, first: first))
    }

    func holds(_ id: String?) -> Bool {
        guard let id else { return false }
        switch content {
        case .supervision(_, let lines, _, _): return lines.contains { $0.holds(id) }
        case .supervisionInput(let item): return item.id == id
        case .row(let r): return r.id == id
        case .answerPart(let item, _, _, _): return item.id == id
        case .recaps(_, let items): return items.contains { $0.id == id }
        default: return false
        }
    }

    var isInput: Bool {
        switch content {
        case .row(.user): true
        default: false
        }
    }
}

/// What a work line folds: tool calls, thoughts, system notices and the agent's words before its last ones.
nonisolated indirect enum WorkItem: Identifiable, Equatable, Sendable {
    case step(ToolStep)
    case text(Item)

    var id: String {
        switch self {
        case .step(let s): s.id
        case .text(let i): "t-" + i.id
        }
    }
}

/// One step of an open work line, a line of the chat list itself. The steps read as one card under their header:
/// each draws its slice of it (`WorkCardSlice`), with a hairline between steps, instead of a tile per step.
struct WorkStepView: View, Equatable {
    nonisolated let item: WorkItem
    nonisolated let first: Bool
    @Environment(\.dynamicTypeSize) private var typeSize

    nonisolated static func == (lhs: Self, rhs: Self) -> Bool {
        lhs.item == rhs.item && lhs.first == rhs.first
    }

    @MainActor
    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("WorkStepView")
        #endif
        Group {
            switch item {
            case .step(.call(let c)) where c.id.hasPrefix(ChildSessions.callPrefix):
                if let childKey = ChildSessionLines.childKey(of: c) {
                    ChildSessionCard(key: childKey)
                }
            case .step(.call(let c)) where SubagentCard.isAgent(c):
                SubagentCard(call: c)
            case .step(.call(let c)) where WorkflowCallCard.isWorkflow(c):
                // Its run's card (#3436): the card reads the run itself, this view is not drawn again by pushes.
                WorkflowCallCard(call: c)
            case .step(.call(let c)):
                ToolCallRow(call: c, autoExpand: false, grouped: true)
            case .step(.thought(let t)) where t.task != nil && t.wake != true:
                TaskNoteRow(item: t, grouped: true) // a Monitor event between two commands: one line like theirs
            case .step(.thought(let t)) where t.kind != .notice && AppStyle.current.usesBeautifulUI:
                ThinkingRow(item: t, compact: true) // a row like the tools', with their insets
            case .step(.thought(let t)):
                Group { if t.kind == .notice { NoticeRow(item: t) } else { ThinkingRow(item: t, compact: !WorkCardSlice.timeline) } }
                    .padding(.leading, WorkCardSlice.contentLeading(typeSize))
                    .padding(.trailing, WorkCardSlice.inset)
                    .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
            case .step(.folded(let f)):
                FoldedWorkRow(fold: f)
                    .padding(.leading, WorkCardSlice.contentLeading(typeSize))
                    .padding(.trailing, WorkCardSlice.inset)
                    .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
            case .text(let i):
                // The agent's words between its steps: on the timeline in the quieter type, so they read as part of
                // the process and not as its answer.
                AssistantText(item: i, quiet: WorkCardSlice.timeline)
                    .padding(.leading, WorkCardSlice.contentLeading(typeSize))
                    .padding(.trailing, WorkCardSlice.inset)
                    .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background { WorkCardSlice(top: first, bottom: false) }
        .overlay(alignment: .top) { if !first { WorkCardSlice.separator } }
    }
}

/// The chat's actions handed down through the environment (open a file, a link, an image, a subagent…), as closures
/// made once per page that call whatever the page's last render set. A closure written in `body` is a new value on
/// every render and SwiftUI cannot tell it is the same one: every row that reads one (each visible work step reads
/// several) was evaluated again on every push from the hub (2026-10-31, `LiveUpdateTests`).
@MainActor final class ChatActions {
    struct Handlers {
        var openFile: (String) -> Void = { _ in }
        /// A path linked in a message (`PathLinkURL`), at its line.
        var openFileAt: (String, Int?) -> Void = { _, _ in }
        var openLink: (URL) -> Void = { _ in }
        var openPort: (PortLink) -> Void = { _ in }
        var viewImages: (ImageViewing) -> Void = { _ in }
        var attachImage: (UIImage) -> Void = { _ in }
        var loadImage: (String) async -> ImagePayload? = { _ in nil }
        var loadFullText: (String) async throws -> String = { _ in "" }
        var openSubagent: (String) -> Void = { _ in }
        var openWorkflowAgent: (WorkflowAgent) -> Void = { _ in }
        var openPlan: () -> Void = {}
        var openFold: (Item) async throws -> Void = { _ in }
        var jumpToCall: (String) -> Void = { _ in }
        var openQuote: (ContextRef) -> Bool = { _ in false }
        /// An app link to one of Persona's pages (`persona://open<path>`, an older `persona://` form, #9926): true when it
        /// opened.
        var openAppLink: (URL) -> Bool = { _ in false }
        var openTurnBranchReview: () -> Void = {}
        var openTurnChangedFile: (TurnChangedFile) -> Void = { _ in }
    }

    private var handlers = Handlers()

    func set(_ h: Handlers) { handlers = h }

    /// A changed file queued to open in `FileViewer` after `ReviewPage` dismisses (avoids simultaneous sheet drop).
    var pendingReviewFallback: (sessionKey: SessionKey, ref: FileRef)?
    private(set) lazy var openFile = FileOpener { [weak self] in self?.handlers.openFile($0) }
    private(set) lazy var openTurnReview = TurnReviewOpener(
        openBranchReview: { [weak self] in self?.handlers.openTurnBranchReview() },
        openChangedFile: { [weak self] in self?.handlers.openTurnChangedFile($0) }
    )
    private(set) lazy var openLink: (URL) -> Void = { [weak self] in self?.handlers.openLink($0) }
    private(set) lazy var openPort: (PortLink) -> Void = { [weak self] in self?.handlers.openPort($0) }
    private(set) lazy var viewImages: (ImageViewing) -> Void = { [weak self] in self?.handlers.viewImages($0) }
    private(set) lazy var attachImage: (UIImage) -> Void = { [weak self] in self?.handlers.attachImage($0) }
    // The async ones run on the main actor, where the handlers are.
    private(set) lazy var loadImage = ImageLoader { @MainActor [weak self] path in
        guard let load = self?.handlers.loadImage else { return nil }
        return await load(path)
    }
    private(set) lazy var loadFullText: (String) async throws -> String = { @MainActor [weak self] id in
        guard let load = self?.handlers.loadFullText else { throw CancellationError() }
        return try await load(id)
    }
    private(set) lazy var openSubagent: (String) -> Void = { [weak self] in self?.handlers.openSubagent($0) }
    private(set) lazy var openWorkflowAgent: (WorkflowAgent) -> Void = { [weak self] in self?.handlers.openWorkflowAgent($0) }
    private(set) lazy var openPlan: () -> Void = { [weak self] in self?.handlers.openPlan() }
    private(set) lazy var openFold: (Item) async throws -> Void = { @MainActor [weak self] item in
        guard let open = self?.handlers.openFold else { return }
        try await open(item)
    }
    private(set) lazy var jumpToCall: (String) -> Void = { [weak self] in self?.handlers.jumpToCall($0) }
    private(set) lazy var openQuote: (ContextRef) -> Bool = { [weak self] in self?.handlers.openQuote($0) ?? false }
    /// Web links open in the in-app sheet (Feishu docs in the native reader); anything else as before.
    private(set) lazy var openURL = OpenURLAction { [weak self] url in
        #if os(iOS)
        if let code = CodeCopyURL.parse(url) { // inline code in a reply: copied (#2984)
            CodeCopy.copy(code, said: String(localized: "Copied [Mac Hover 07d6bb]"))
            return .handled
        }
        #endif
        if let ref = PathLinkURL.parse(url) { // a path in a message: the file viewer, at its line
            self?.handlers.openFileAt(ref.path, ref.line)
            return .handled
        }
        if let path = PathLinkURL.localPath(url) { // an explicit Markdown link to a machine file, absolute or cwd-relative
            self?.handlers.openFileAt(path, nil)
            return .handled
        }
        guard ["http", "https"].contains(url.scheme?.lowercased() ?? ""), let self else {
            // A link to one of Persona's pages written into a message (#9926): handed to the system it came back to
            // `onOpenURL` and went nowhere.
            if self?.handlers.openAppLink(url) == true { return .handled }
            return .systemAction
        }
        handlers.openLink(url)
        return .handled
    }
}

/// A slice of an open work line's card (in the BeautifulUI style its card: the style's card color, a 1 pt ring). The steps are lines of their own in the lazy chat list (a turn can hold hundreds; one view holding them
/// all stalled the page), so each draws its part: rounded on top for the first, at the bottom for the Collapse foot,
/// reaching halfway into the list's gaps to meet its neighbours.
struct WorkCardSlice: View {
    var top: Bool
    var bottom: Bool
    @Environment(\.dynamicTypeSize) private var typeSize
    /// Half the list's spacing (`LazyVStack(spacing: Theme.Space.l)`): the slices meet in the middle of each gap. It was
    /// 7 from when the spacing was 14; at 16 every seam showed a 2 pt gap (white lines across the card, the rail broken).
    static let bridge: CGFloat = Theme.Space.l / 2
    static let radius = Theme.Radius.card
    /// Side padding inside a card. The timeline's icon column uses `iconLeading` instead.
    static let inset: CGFloat = Theme.Space.m
    /// Where the timeline's icons start. Flush with the assistant reply bubble, which begins at the list's content
    /// margin. A card keeps `inset` inside its ring, so that style does not move.
    static var iconLeading: CGFloat { timeline ? Theme.Space.flush : inset }
    /// The card's text column, unchanged: inset + icon + 10. The hairline between card steps starts there.
    /// The separator only draws for cards, so it reads this directly instead of a text size.
    static let cardTextColumn: CGFloat = 46
    /// The icon's width at this text size, the same scale `scaledFrame` gives the step icons.
    static func iconWidth(_ typeSize: DynamicTypeSize) -> CGFloat { Theme.Size.icon * Theme.sizeScale(typeSize) }
    /// Where a tool row's words start. On the timeline that is the scaled icon plus the header's gap, so the title,
    /// the opened detail and Collapse share one edge. A card keeps `cardTextColumn`.
    static func textColumn(_ typeSize: DynamicTypeSize) -> CGFloat {
        timeline ? iconLeading + iconWidth(typeSize) + Theme.Space.m : cardTextColumn
    }
    /// The rail's x, through the middle of the tools' icons.
    static func railX(_ typeSize: DynamicTypeSize) -> CGFloat { iconLeading + iconWidth(typeSize) / 2 }

    static var timeline: Bool { StyleSpec.current.workList == .timeline }
    /// Where a step's own content starts: the card's inset, or (timeline) right of the rail, where the tools' words are.
    static func contentLeading(_ typeSize: DynamicTypeSize) -> CGFloat { timeline ? textColumn(typeSize) : inset }

    var body: some View {
        if Self.timeline { rail } else { card }
    }

    /// Liquid Glass: no card, a thin line down the left through the steps, reaching into the gaps to meet its neighbours;
    /// it starts at the first step and stops in the middle of the Collapse foot.
    private var rail: some View {
        GeometryReader { g in
            Rectangle()
                .fill(Color(uiColor: .separator))
                .frame(width: Theme.Size.lineMedium, height: bottom ? g.size.height / 2 + (top ? 0 : Self.bridge) : g.size.height + (top ? 0 : Self.bridge) + Self.bridge)
                .offset(x: Self.railX(typeSize) - Theme.Size.lineMedium / 2, y: top ? Theme.Space.m : -Self.bridge)
        }
        .allowsHitTesting(false)
    }

    private var card: some View {
        let r = Self.radius
        let fill = UnevenRoundedRectangle(topLeadingRadius: top ? r : 0, bottomLeadingRadius: bottom ? r : 0,
                                          bottomTrailingRadius: bottom ? r : 0, topTrailingRadius: top ? r : 0,
                                          style: .continuous).fill(Self.beautiful ? Theme.card : Theme.Fill.well)
        return StrokeShapeView(shape: WorkCardEdges(top: top, bottom: bottom, radius: r),
                        style: Theme.cardStroke, strokeStyle: StrokeStyle(lineWidth: Self.beautiful ? Theme.Size.line : 0.5),
                        isAntialiased: true, background: fill)
            .padding(.top, top ? 0 : -Self.bridge)
            .padding(.bottom, bottom ? 0 : -Self.bridge)
    }

    /// BeautifulUI's card (the style's card color, a 1 pt ring); Persona' own is the gray well with a hairline.
    private static var beautiful: Bool { AppStyle.current.usesBeautifulUI }

    /// The hairline between two steps, in the gap above a row, from the words' column (none on the timeline: the rail
    /// joins the steps).
    @ViewBuilder static var separator: some View {
        if !timeline {
            Rectangle().fill(beautiful ? Theme.cardStroke : Color(uiColor: .separator)).frame(height: beautiful ? Theme.Size.line : Theme.Size.hairline)
                .padding(.leading, cardTextColumn)
                .offset(y: -bridge)
                .allowsHitTesting(false)
        }
    }
}

/// The foot of an open work card: in the BeautifulUI style the files this work changed as its diff chips (a tap opens
/// the file, like the diff's header), the first six and "+N files" for the rest; then Collapse N steps.
struct WorkEndView: View, Equatable {
    @Environment(\.openFile) private var openFile
    @Environment(\.chatPin) private var pin
    @Environment(\.dynamicTypeSize) private var typeSize
    nonisolated let count: Int
    nonisolated let files: [ChangedFile]
    /// What the chat's pin knows the chips by: "+N files" opens the rest under them, the chips staying where they are (#3757).
    nonisolated let pinKey: String
    var collapse: () -> Void
    @State private var all = false

    nonisolated static func == (lhs: Self, rhs: Self) -> Bool { lhs.count == rhs.count && lhs.files == rhs.files }

    private static let shown = 6

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if !files.isEmpty {
                let list = all ? files : Array(files.prefix(Self.shown))
                FileDiffChips(list.map { FileDiff(file: ($0.path as NSString).lastPathComponent, additions: $0.adds, deletions: $0.dels) },
                              moreLabel: files.count > list.count ? String(localized: "+\(files.count - list.count) files") : nil,
                              onMore: { toggleInPlace(pin, pinKey, animation: Theme.Motion.standard) { all = true } },
                              onSelect: openFile.map { open in { diff in
                                  if let f = files.first(where: { ($0.path as NSString).lastPathComponent == diff.file }) { open(f.path) }
                              } })
                    .padding(.horizontal, WorkCardSlice.inset)
                    .padding(.top, Theme.Space.m)
                    .accessibilityIdentifier("work-files")
                    .chatPinned(pinKey)
            }
            Button(action: collapse) {
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.collapse).font(Theme.Font.labelStrong)
                    Text("Collapse \(count) steps [Chat View 608bfc]").lineLimit(1).fixedSize()
                }
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .padding(.leading, WorkCardSlice.contentLeading(typeSize))
                .padding(.trailing, WorkCardSlice.inset)
                .padding(.vertical, Theme.Space.m)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Collapse steps")
            .accessibilityIdentifier("work-fold-bottom")
        }
        .background { WorkCardSlice(top: false, bottom: true) }
        .overlay(alignment: .top) { WorkCardSlice.separator }
    }
}

/// The card's outline for one slice: the sides, and the rounded top or bottom where the card starts or ends.
private nonisolated struct WorkCardEdges: Shape {
    var top: Bool
    var bottom: Bool
    var radius: CGFloat

    func path(in rect: CGRect) -> Path {
        var p = Path()
        let r = min(radius, rect.height / 2, rect.width / 2)
        // Left side, bottom to top, then across the top and down the right side.
        p.move(to: CGPoint(x: rect.minX, y: bottom ? rect.maxY - r : rect.maxY))
        if top {
            p.addLine(to: CGPoint(x: rect.minX, y: rect.minY + r))
            p.addArc(tangent1End: CGPoint(x: rect.minX, y: rect.minY), tangent2End: CGPoint(x: rect.minX + r, y: rect.minY), radius: r)
            p.addLine(to: CGPoint(x: rect.maxX - r, y: rect.minY))
            p.addArc(tangent1End: CGPoint(x: rect.maxX, y: rect.minY), tangent2End: CGPoint(x: rect.maxX, y: rect.minY + r), radius: r)
        } else {
            p.addLine(to: CGPoint(x: rect.minX, y: rect.minY))
            p.move(to: CGPoint(x: rect.maxX, y: rect.minY))
        }
        p.addLine(to: CGPoint(x: rect.maxX, y: bottom ? rect.maxY - r : rect.maxY))
        if bottom {
            p.addArc(tangent1End: CGPoint(x: rect.maxX, y: rect.maxY), tangent2End: CGPoint(x: rect.maxX - r, y: rect.maxY), radius: r)
            p.addLine(to: CGPoint(x: rect.minX + r, y: rect.maxY))
            p.addArc(tangent1End: CGPoint(x: rect.minX, y: rect.maxY), tangent2End: CGPoint(x: rect.minX, y: rect.maxY - r), radius: r)
        }
        return p
    }
}

/// The quiet line under a message: what the agent did for it, as BeautifulUI's tool run header. Finished:
/// "⌄ 3 tool calls, 2 messages · 1 min 20 s"; running: a spinner and "Working 39s · 2 tool calls" (ticking, shimmering)
/// with the terminal at the end. Tap to list every step right here (tools, thoughts, notices, earlier replies); tap
/// the same line again to fold.
nonisolated struct WorkSummary: Equatable {
    let full: [String]
    let short: [String]

    init(_ items: [WorkItem]) {
        var calls = 0, thoughts = 0, notices = 0, texts = 0
        for item in items {
            switch item {
            case .step(.call): calls += 1
            case .step(.folded(let f)): // work the page left out counts as what it stands for
                calls += f.fold?.tools ?? 0
                thoughts += f.fold?.thoughts ?? 0
            case .step(.thought(let thought)):
                if thought.kind == .notice {
                    if thought.wake == true { calls += 1 } else { notices += 1 }
                } else { thoughts += 1 }
            case .text: texts += 1
            }
        }
        var full: [String] = []
        var short: [String] = []
        if AppStyle.current.usesBeautifulUI {
            // BeautifulUI's header words: "5 tool calls, 2 messages".
            if calls > 0 { full.append(String(localized: "\(calls) tool calls")); short.append(String(localized: "\(calls) tool calls")) }
            if thoughts > 0 { full.append(String(localized: "\(thoughts) thoughts")) }
            if texts > 0 { full.append(String(localized: "\(texts) messages")) }
            if full.isEmpty && notices > 0 { full.append(String(localized: "\(notices) background notices")) }
            if short.isEmpty && notices > 0 { short.append(String(localized: "\(notices) background notices")) }
        } else {
            if calls > 0 { full.append(String(localized: "Tools \(calls)")); short.append(String(localized: "Tools \(calls)")) }
            if thoughts > 0 { full.append(String(localized: "Thinking \(thoughts)")) }
            if texts > 0 { full.append(String(localized: "Replies \(texts)")) }
            if full.isEmpty && notices > 0 { full.append(String(localized: "Background \(notices) [Chat View 999fb8]")) }
            if short.isEmpty && notices > 0 { short.append(String(localized: "Background \(notices) [Chat View 999fb8]")) }
        }
        self.full = full
        self.short = short
    }
}

struct WorkLine: View, Equatable {
    /// The closures are the line's own (keyed by its id): a hub push re-evaluating the page leaves unchanged lines alone.
    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.isEmpty == b.isEmpty && a.counts == b.counts && a.live == b.live && a.accepting == b.accepting && a.start == b.start && a.end == b.end
            && a.open == b.open && (a.openTerminal == nil) == (b.openTerminal == nil) && a.agents == b.agents && a.doing == b.doing
    }

    private let isEmpty: Bool
    private let counts: WorkSummary
    let live: Bool
    let accepting: Bool
    let start: Date?
    let end: Date?
    let open: Bool
    /// The subagents the running line waits on: what it says instead of its counts.
    let agents: AgentsWorking?
    /// Otherwise, while it runs, the step it names instead of its counts (#2987); drawn as `shown`, which follows it at
    /// most about twice a second.
    private let doing: WorkNow?
    @State private var shown: WorkNow?
    @State private var shownAt = Date.distantPast
    let openTerminal: (() -> Void)?
    let toggle: () -> Void

    init(items: [WorkItem], live: Bool, start: Date?, end: Date?, open: Bool, agents: AgentsWorking? = nil,
         accepting: Bool = false, openTerminal: (() -> Void)?, toggle: @escaping () -> Void) {
        isEmpty = items.isEmpty
        counts = WorkSummary(items)
        self.live = live
        self.accepting = accepting
        self.start = start
        self.end = end
        self.open = open
        self.agents = live ? agents : nil
        doing = live && self.agents == nil ? WorkNow.of(items) : nil
        _shown = State(initialValue: doing)
        self.openTerminal = openTerminal
        self.toggle = toggle
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("WorkLine")
        #endif
        Group { if AppStyle.current.usesBeautifulUI { beautiful } else { plain } }
            .task(id: doing) { await follow(doing) }
    }

    /// Lets `next` through once half a second has passed since the last step shown (`WorkNow.hold`); a newer step in
    /// the meantime replaces this wait (`task(id:)`), so a run of quick calls shows the newest, not each name in turn.
    private func follow(_ next: WorkNow?) async {
        guard next != shown else { return }
        let wait = WorkNow.hold(next, shown: shown, shownAt: shownAt, now: .now)
        if wait > 0 {
            try? await Task.sleep(for: .seconds(wait))
            guard !Task.isCancelled else { return }
        }
        shown = next
        shownAt = .now
    }

    /// The step the line names now, when it names one (`doing` is already nil on a finished line and under subagents).
    private var step: WorkNow? { WorkNow.named(doing: doing, shown: shown) }

    /// BeautifulUI's tool run header: ⌄, the counts and the time; working, a spinner and a shimmer.
    private var beautiful: some View {
        HStack(spacing: Theme.Space.m) {
            Button(action: toggle) {
                ToolRunTitle(isOpen: open, isRunning: live) {
                    // Whole words, never cut: the full summary, or a shorter one when the row is narrow.
                    if live && !accepting {
                        VisibleTimelineView(interval: 1) { now in summary(now: now) }
                    } else {
                        summary(now: end ?? .now)
                    }
                }
                .opacity(isEmpty && !live ? 0.6 : 1)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .disabled(isEmpty)
            .accessibilityLabel(open ? String(localized: "Collapse steps") : String(localized: "Expand steps"))
            .accessibilityValue(label(now: end ?? .now, short: false))
            .accessibilityIdentifier("work-head")
            Spacer(minLength: 0)
            if let openTerminal {
                Button(action: openTerminal) { ThemedIcon(Theme.Icon.terminal).font(Theme.Font.caption).foregroundStyle(.secondary) }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Open terminal")
            }
        }
    }

    /// Persona' own: the counts and the time in secondary words, the chevron after them.
    private var plain: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            HStack(spacing: Theme.Space.m) {
                Button(action: toggle) {
                    HStack(spacing: Theme.Space.sm) {
                        // Working: the state's amber spinner, the words stay secondary (the dot says the state).
                        if live { ProgressView().controlSize(.mini).tint(Theme.busy) }
                        // Whole words, never cut: the full summary, or a shorter one when the row is narrow.
                        if live && !accepting {
                            VisibleTimelineView(interval: 1) { now in summary(now: now) }
                        } else {
                            summary(now: end ?? .now)
                        }
                        if !isEmpty {
                            ThemedIcon(Theme.Icon.disclosure)
                                .font(Theme.Font.labelStrong)
                                .rotationEffect(.degrees(open ? 90 : 0))
                        }
                    }
                    .font(Theme.Font.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .disabled(isEmpty)
                .accessibilityLabel(open ? String(localized: "Collapse steps") : String(localized: "Expand steps"))
                .accessibilityValue(label(now: end ?? .now, short: false))
                .accessibilityIdentifier("work-head")
                Spacer(minLength: 0)
                if let openTerminal {
                    Button(action: openTerminal) { ThemedIcon(Theme.Icon.terminal).font(Theme.Font.caption).foregroundStyle(.secondary) }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Open terminal")
                }
            }
        }
    }

    @ViewBuilder private func summary(now: Date) -> some View {
        if let step { stepSummary(step, now: now) } else { counted(now: now) }
    }

    /// One line: the step's words give way (a path or a command in the middle, a sentence at the end), "+2" and the
    /// time stay whole.
    @ViewBuilder private func stepSummary(_ step: WorkNow, now: Date) -> some View {
        let took = Self.duration(from: start, to: now)
        HStack(spacing: Theme.Space.flush) {
            Text(step.text).truncationMode(step.tail ? .tail : .middle)
            Text(step.trailer(took: took)).fixedSize()
        }
        .lineLimit(1)
        .accessibilityElement(children: .combine)
    }

    private func counted(now: Date) -> some View {
        FirstFitting { // ViewThatFits measured both at every width of a window resize (#1337)
            Text(label(now: now, short: false)).fixedSize()
            Text(label(now: now, short: true)).fixedSize()
        }
        .firstFittingText(label(now: now, short: false))
    }

    private func label(now: Date, short: Bool) -> String {
        if accepting { return String(localized: "Processing…") }
        // The step in full: the line may have cut it.
        if let step { return step.text + step.trailer(took: Self.duration(from: start, to: now)) }
        let parts = agents.map { [SubagentGridText.working(count: $0.count, doing: $0.doing, short: short)] } ?? (short ? counts.short : counts.full)
        let took = Self.duration(from: start, to: now)
        guard AppStyle.current.usesBeautifulUI else {
            // Persona' own: "Tool 3 · Thinking 1 · 1 min 20 s", "Working 39s · Tool 2".
            var p = parts
            if live { p.insert(String(localized: "Working \(took)"), at: 0) } else if !took.isEmpty { p.append(took) }
            return p.isEmpty ? String(localized: "Done [Task Sheets f28461]") : p.joined(separator: " · ")
        }
        let what = parts.joined(separator: String(localized: ", ", comment: "List separator"))
        if live { return what.isEmpty ? String(localized: "Working \(took)") : String(localized: "Working \(took) · \(what)") }
        if what.isEmpty { return took.isEmpty ? String(localized: "Done [Task Sheets f28461]") : String(localized: "Done · \(took)") }
        return took.isEmpty ? what : what + " · " + took
    }

    static func duration(from start: Date?, to end: Date) -> String {
        guard let start else { return "" }
        let s = max(0, Int(end.timeIntervalSince(start)))
        if s < 60 { return String(localized: "\(s) s") }
        if s < 3600 { return s % 60 == 0 ? String(localized: "\(s / 60) min [Chat View a38788]") : String(localized: "\(s / 60)m \(s % 60)s") }
        return String(localized: "\(s / 3600) hr \(s % 3600 / 60) min")
    }
}


/// What the closeout card's detail read follows: the chat and the closeout as the list has it.
private struct CloseoutRead: Equatable {
    var key: SessionKey
    var closeout: Closeout
}

/// Where a work line's or tool run's header is, told to the chat's pin (`workHeadMoved`). Not from Full Page's copies of
/// the lines (#1609): they are off screen, and their positions moved the list the next time a header was tapped.
private struct WorkHeadReport: ViewModifier {
    let id: String
    let moved: (String, ChatPin.Spot) -> Void
    @Environment(\.fullPageExport) private var exporting

    func body(content: Content) -> some View {
        if exporting {
            content
        } else {
            content.onGeometryChange(for: ChatPin.Spot.self, of: ChatView.headSpot) { moved(id, $0) }
                #if DEBUG && os(iOS)
                .background { if UnfoldTrace.enabled { DrawnProbe(key: id) } }
                #endif
        }
    }
}


/// The chat list's offset and visible height, watched together (see `scrolled`).
private struct ListScroll: Equatable {
    var offset: CGFloat
    var height: CGFloat
    var x: CGFloat
    /// The content inset at the newest end (the offset there is minus this).
    var top: CGFloat = 0
    var content: CGFloat = 0
}

/// The composer's draft, read in a body of its own: the footer around it (cards, dock, the composer's own view) is not
/// drawn again for every keystroke. Observation tracks `AppModel.drafts` as one value, so any read of it in the footer's
/// body made each keystroke rebuild and compare the whole footer.
private struct DraftReader<Content: View>: View {
    let key: SessionKey
    @ViewBuilder var content: (String) -> Content
    @Environment(AppModel.self) private var model

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("DraftReader")
        #endif
        content(model.drafts[key] ?? "")
    }
}

private struct ChatRegion<Content: View>: View {
    let name: StaticString
    @ViewBuilder var content: () -> Content

    var body: some View {
        #if PERF_SIGNPOST
        let _ = PerfTrace.event(name)
        #endif
        #if DEBUG
        let _ = PerfCount.body("\(name)")
        #endif
        content()
    }
}

private struct ChatLiveText: View {
    let store: ChatStore
    /// While the chat holds back what arrives (`Frozen`): the words as they were drawn then. One view either way: drawn
    /// in a branch of its own beside this one (both rows `live`), the frozen words never showed, this view stayed in
    /// the lazy list and went on growing under the finger (2026-11-04, StreamingScrollTests: 9 to 30 pt every delta).
    var held: String?
    var hidesBotmuxSentinel = false
    /// The new conversation layout: the answer streams in the box it settles in (#1666).
    @Environment(\.chatLayout) private var layout

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ChatLiveText")
        #endif
        // Held: the store is not read, so the deltas do not draw this again.
        let text = held ?? (Self.shows(store) ? store.liveText : "")
        // Held, or the stream over: the words whole, not revealed further (#6651).
        let done = held != nil || store.liveDone
        if !text.isEmpty, !hidesBotmuxSentinel || !BotmuxDisplay.hidesSentinel(text, role: "assistant", mode: .decoded) {
            if layout.on {
                StreamingText(text: text, done: done)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .chatBoxed(Theme.ChatBox.persona)
                    .chatMark("chat.live")
                    .id("live")
                    .flippedBack()
            } else {
                StreamingText(text: text, done: done).chatMark("chat.live").id("live").flippedBack()
            }
        }
    }

    /// There is streamed text, and its message is not the last row yet. An agent's is already what its newest items
    /// lack (`ChatStore.agentStreamed`): its words may begin like the row before them.
    static func shows(_ store: ChatStore) -> Bool {
        let live = store.liveText
        guard !live.isEmpty else { return false }
        if store.liveIsAgents { return true }
        if case .assistant(let item) = store.rows.last, let text = item.text { return !(text.hasPrefix(live) || live == text) }
        return true
    }
}

private struct ChatStreamFollow: ViewModifier {
    let store: ChatStore
    let follow: () -> Void

    func body(content: Content) -> some View {
        content.onChange(of: store.liveText.count / 40) { _, _ in follow() }
    }
}

/// How far the Agents header is folded (0 = open, maxCollapse = just the slim bar). Written on every scroll frame,
/// read only by `ChromeHeader`, so following the finger redraws the header and nothing else.
@Observable
final class ChatChrome {
    static let compactHeight: CGFloat = Theme.Size.slimBar
    /// Whether the header folds at all (the retired Mac app's did not, #1337).
    static let folds = true
    var collapse: CGFloat = 0
    /// The open header's height, measured.
    var height: CGFloat = 0
    /// The list's offset (0 = newest), kept for deciding where to settle.
    @ObservationIgnored var offset: CGFloat = 0
    var maxCollapse: CGFloat { Self.folds ? max(0, height - Self.compactHeight) : 0 }
    /// The header's height once a landing has settled: the slim bar, or all of it where it does not fold.
    var settledHeight: CGFloat { Self.folds ? Self.compactHeight : height }
}

/// The open header slides up under a shrinking clip while the slim bar fades in over it: one continuous motion,
/// driven by `chrome.collapse` (the finger, or a short animation when it settles).
private struct ChromeHeader<Full: View, Compact: View>: View {
    var chrome: ChatChrome
    /// Any touch on the header (switching agents, scrolling the strip): it stays open while in use.
    var touched: () -> Void
    @ViewBuilder var full: () -> Full
    @ViewBuilder var compact: () -> Compact
    /// Settings › Appearance & Display › Persona Banner in Screenshots (#9508).
    @StoredValue(ScreenshotBannerPrefs.enabledKey) private var screenshotBanner = ScreenshotBannerPrefs.enabledByDefault

    /// The open header and its slim bar: its own view, so the fold is read where it is drawn (inside the bar's own
    /// hosting controller with the screenshot banner on, #9508), not by this one at every step of a fold.
    private var header: some View {
        ChromeHeaderStack(chrome: chrome, full: full, compact: compact)
    }

    var body: some View {
        #if os(iOS)
        if ScreenshotBanner.wraps(screenshotBanner) {
            // A screenshot shows the banner here instead (#9508): the header over the page's color and its material,
            // so the banner under it never shows through; the banner over the page's color alone (no second blur).
            header
                .contentShape(.rect)
                .simultaneousGesture(DragGesture(minimumDistance: 0).onChanged { _ in touched() })
                .screenshotBanner {
                    ZStack { Theme.pageBackground; Rectangle().fill(.bar) }
                } bannerBackdrop: {
                    Theme.pageBackground
                } banner: {
                    ScreenshotBannerContent()
                }
        } else {
            plain
        }
        #else
        plain
        #endif
    }

    /// The header as it always was.
    private var plain: some View {
        header
            .background(.bar, ignoresSafeAreaEdges: .top)
            .simultaneousGesture(DragGesture(minimumDistance: 0).onChanged { _ in touched() })
    }
}

/// The open header and its slim bar, folded as far as the chat has scrolled (`ChromeHeader`).
private struct ChromeHeaderStack<Full: View, Compact: View>: View {
    var chrome: ChatChrome
    @ViewBuilder var full: () -> Full
    @ViewBuilder var compact: () -> Compact

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ChromeHeader")
        #endif
        let maxC = chrome.maxCollapse
        let c = min(max(chrome.collapse, 0), maxC)
        let p = maxC > 0 ? c / maxC : 0 // 0 open … 1 folded
        ZStack(alignment: .top) {
            ChatRegion(name: "ChromeFull.body", content: full)
                .fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { chrome.height = $0 }
                .offset(y: -c)
                .opacity(Double(1 - min(1, p * 1.5)))
                .allowsHitTesting(p < 0.5)
                .modifier(GoneWhen(gone: p * 1.5 >= 1, height: chrome.height))
            // Not there at all until it starts to show (it is clear below half way).
            if p > 0.5 {
                ChatRegion(name: "ChromeCompact.body", content: compact)
                    .opacity(Double(max(0, (p - 0.5) * 2)))
            }
        }
        .frame(height: chrome.height > 0 ? max(chrome.height - c, ChatChrome.compactHeight) : nil, alignment: .top)
        .clipped()
    }
}

/// The open header once it has faded out: gone from the tree, so its buttons leave VoiceOver and XCUITest.
/// `accessibilityHidden` alone was not enough (its back and ⋯ are glass, UIKit): slid up under the clip, its Back sat in
/// the status bar's band, invisible, "hittable", and a tap there went nowhere.
/// On iOS a clear placeholder of its last measured height takes its place (folding reads that height). It was a
/// `.hidden()` copy of the header: another identity, so every fold built the whole header again, its glass and the
/// agents' tabs, and every unfold once more (#9103). Now folding only drops it and unfolding builds it once. While
/// folded its height is not measured; it is again as soon as it opens.
/// On the Mac it only leaves VoiceOver: it is already clear and takes no clicks there (`allowsHitTesting`), and
/// swapping it for a hidden copy cost 71–82 ms of hitches a second scrolling a chat up and down, 9–29 without (#1337).
private struct GoneWhen: ViewModifier {
    var gone: Bool
    /// The open header's measured height (`ChatChrome.height`).
    var height: CGFloat

    func body(content: Content) -> some View {
        if gone { Color.clear.frame(height: height) } else { content }
    }
}

/// Per-chat bookkeeping that must not redraw the page when it changes. A plain class held in @State: SwiftUI keeps
/// the same instance across renders but does not observe it.
final class ChatScratch {
    var prewarmSource = ""
    var prewarmedIDs = Set<String>()
    struct LinesKey: Equatable {
        var store: String
        var version: Int
        var first: String?
        var live: String?
        var open: Set<String>
        var runs: Set<String>
        var frozen: ChatView.Frozen?
        /// The assistant conversation's feed items (TasksStore): one more is a new line.
        var feed: [String] = []
        /// The running turn's next words are streaming (`ChatLines.lines`).
        var streaming = false
        /// Subagent status affects the running summary even when all cards are folded.
        var quietAgents: Set<String> = []
        /// Listed Workflow calls keep their own cards outside chip runs, inside the work summary.
        var workflowCalls: Set<String> = []
        /// Today, in an assistant conversation: on a new day yesterday's task cards are lines and give back the hand-offs
        /// they took in (`SentToAgents.takenIn`, #1668).
        var day: Date?
        var supervision: Set<String> = []
        var active = false
        /// Background rounds fold (`ChatLines.foldBackground`): the main conversation with the hub's switch on.
        var background = false
        var foldRoutineRelayReplies = false
        var latestReplyNeedsAttention = false
        /// The header shows the session's closeout card: its raw block is left out of the last reply.
        var closeout = false
        /// Folded children placed in this chat. A change of row rebuilds the lines.
        var children = ""
        /// The permission mode changes merged in (#10297): one more is a new line.
        var modes: [String] = []
        /// Whether completed Agent exchanges emit Changed Files cards (#12016), and the folder used for display paths.
        var turnChanges = false
        var cwd = ""
        var botmuxMode: BotmuxDisplayMode = .decoded
        var botmuxSession = false
    }
    /// What was sent from this chat lately: its transcript copy arriving brings you to the newest, others do not.
    var sentHere: [(id: UUID, text: String)] = []
    /// When the last message went out from here, and when the user last took the list (a drag): a message's
    /// transcript copy only brings you to the newest if you have not scrolled since sending it.
    var sentAt = Date.distantPast
    var lastUserScroll = Date.distantPast
    /// The frozen chat's rows up to its last exchange (`ChatStore.rebuild`).
    var frozenSettled: ChatStore.SettledRows?
    /// The chat's newest plan by the store's version (`ChatView.latestPlan`).
    var plan: (key: SessionKey, version: Int, plan: ChatPlan?)?
    /// Rows on screen (for remembering where you left off), written by the list as it scrolls.
    var visibleLines: [String] = []
    /// Rows with any part on screen (`ChatView.mendBlankLanding`).
    var drawnLines: [String] = []
    /// When a work line was last folded (the list may follow again if that leaves it at the newest end).
    var foldedAt = Date.distantPast
    /// Where the lazy stack last placed its first row, the end sentinel, from the stack's top (0 when laid out right;
    /// `ChatView.mendMisplacedStart`), and when the list was last built again for it.
    var startY: CGFloat = 0
    /// Where it placed the newest line, from the stack's top (`Theme.Space.l` past the sentinel, or more).
    var newestLineY: (id: String, y: CGFloat)?
    var startMendedAt = Date.distantPast
    #if DEBUG
    var startMends = 0 // lists built again for a misplaced newest end (#7382), for the scroll probe
    #endif
    #if DEBUG
    /// Landings that left nothing drawn (`ChatView.mendBlankLanding`, `probe.chat-rows`).
    var blankLandings = 0
    /// Where the list is and how tall its content is (`probe.chat-rows`).
    var scrollY: CGFloat = 0
    var contentHeight: CGFloat = 0
    /// The scroll phase's last change, from and to (`ScrollTrace`).
    var phase = ""
    #endif
    /// The finger is on the list (or it is gliding after a flick): only then does the chrome follow it.
    var userDragging = false
    /// The list sits exactly at its newest end (offset at the content inset).
    var atNewest = false
    /// How far the list is from its newest end (pt; the flipped list's offset past its inset).
    var newestGap: CGFloat = 0
    /// Headers opened or folded (`ChatView.holdFollow`): only the last one's hold ends the follow state's.
    var holds = 0
    /// The saved place's search for older pages (`ChatView.searchOlderForEntry`).
    var entryPages = 0
    var entrySearching = false
    /// Last time the user scrolled or touched the header: the chrome folds itself after a few idle seconds.
    var lastActivity = Date.now
    var linesKey: LinesKey?
    var lines: [ChatLine] = []
    /// Folded children of the open parent, read once while the lines are built and handed to the cards.
    var childRows: [Session] = []
    /// Where each open work line's header is on screen (global midY), and the list's frame: folding or opening from
    /// the header keeps the header under the finger.
    var workHeadY: [String: CGFloat] = [:]
    var listFrame: CGRect = .zero
    /// The bottom on screen of the line being landed on (`ChatView.landing`).
    var landingMaxY: CGFloat?
    /// The list's horizontal offset at rest (-16: the side margins are content margins). Vertical jumps keep it:
    /// `scrollTo(y:)` set it to 0 and the page lost its margins.
    var offsetX: CGFloat = 0
    #if DEBUG
    var endJumps = 0 // jumps to the newest (scrollToEnd), for the scroll probe
    var glideTaps = 0 // ↓ tapped while the list glided (#3027), for the scroll probe
    var jumpMs = -1 // how long the last ↓ took to the newest end (#3027), for the scroll probe
    var jumpSlips = 0 // ↓ landed and the list was taken away from the newest end while following (#4788), for the scroll probe
    var lineY: [String: CGFloat] = [:] // each line's top on screen, for the fold probe (-showFoldProbe)
    #endif
}

extension View {
    /// DEBUG, with -showFoldProbe or -probes: records the line's top on screen (fold probe, `probe.chat-rows`).
    /// Otherwise nothing.
    @ViewBuilder func debugLineY(_ id: String, _ scratch: ChatScratch) -> some View {
        #if DEBUG
        if ChatView.foldProbe || ScreenProbe.enabled {
            onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .global).minY }) { scratch.lineY[id] = $0 }
        } else {
            self
        }
        #else
        self
        #endif
    }
}

extension View {
    /// Reports this line's bottom on screen while the chat lands on it (`ChatView.landUnderInput`).
    @ViewBuilder func landingFrame(_ on: Bool, _ scratch: ChatScratch) -> some View {
        if on {
            onGeometryChange(for: CGFloat.self, of: { $0.frame(in: .global).maxY }) { scratch.landingMaxY = $0 }
        } else {
            self
        }
    }
}

private extension View {
    /// Hidden but keeping its room, so the title stays centered.
    func noBackWhen(_ hidden: Bool) -> some View {
        opacity(hidden ? 0 : 1).disabled(hidden).accessibilityHidden(hidden)
    }
}

extension View {
    /// Whether the system presents this page's bars down the side of the screen (iPhone Duo); always false before iOS 27.1.
    /// The Agents chat and the pages that host it before it is there (a task's page, a side session's) read the same, so
    /// their Back is in the same place (#9971). DEBUG `-sideBars YES` says yes on any simulator (screenshots, tests).
    func readsVerticalBars(_ vertical: Binding<Bool>) -> some View { modifier(VerticalBarsReader(vertical: vertical)) }
}

private struct VerticalBarsReader: ViewModifier {
    @Binding var vertical: Bool

    func body(content: Content) -> some View {
        Group {
            #if compiler(>=6.4) && os(iOS) // `toolbarVerticalEdge` is in the iOS 27.1 SDK; Xcode 26 (Swift ≤ 6.3) has no such key path
            if #available(iOS 27.1, *) {
                content.modifier(VerticalBarsEdgeReader(vertical: $vertical))
            } else {
                content
            }
            #else
            content
            #endif
        }
        #if DEBUG
        .onAppear { if UserDefaults.standard.bool(forKey: "sideBars") { vertical = true } }
        #endif
    }
}

/// Whether bars are down the side, known on the page's first drawing (`readsVerticalBars` learns it a moment later): a
/// page whose system Back depends on it has that Back from the start, which iOS's swipe back from anywhere needs (#9971).
struct WithVerticalBars<Content: View>: View {
    @ViewBuilder var content: (Bool) -> Content

    #if DEBUG
    /// `-sideBars YES` (screenshots, tests), read once.
    static var forced: Bool { VerticalBarsForced.value }
    #else
    static var forced: Bool { false }
    #endif

    var body: some View {
        #if compiler(>=6.4) && os(iOS) // `toolbarVerticalEdge` is in the iOS 27.1 SDK
        if #available(iOS 27.1, *) {
            VerticalBarsEnvironment(content: content)
        } else {
            content(Self.forced)
        }
        #else
        content(Self.forced)
        #endif
    }
}

#if DEBUG
private enum VerticalBarsForced {
    static let value = UserDefaults.standard.bool(forKey: "sideBars")
}
#endif

#if compiler(>=6.4) && os(iOS)
@available(iOS 27.1, *)
private struct VerticalBarsEnvironment<Content: View>: View {
    @Environment(\.toolbarVerticalEdge) private var edge
    var content: (Bool) -> Content

    var body: some View {
        #if DEBUG
        content(edge != nil || VerticalBarsForced.value)
        #else
        content(edge != nil)
        #endif
    }
}

@available(iOS 27.1, *)
private struct VerticalBarsEdgeReader: ViewModifier {
    @Environment(\.toolbarVerticalEdge) private var edge
    @Binding var vertical: Bool

    func body(content: Content) -> some View {
        content.onChange(of: edge != nil, initial: true) { _, v in vertical = v }
    }
}
#endif

#if DEBUG
/// The open conversation (`probe.chat`, docs/apple/ios-state.md): which one, for the tests to check against what was tapped.
nonisolated struct ChatShown: Encodable, Equatable {
    var key: String
    /// How many times this conversation has appeared since launch (`ChatView.appearances`).
    var appeared: Int
    /// The exact line the most recent link or quote highlighted.
    var highlighted: String? = nil
}

/// `probe.chat-rows`: the items and lines the chat has, how many lines are on screen, the top one and where it is.
nonisolated struct ChatRowsShown: Encodable, Equatable {
    var items: Int
    var lines: Int
    var visible: Int
    /// Rows with any part on screen (`visible` counts a row once half of it is).
    var drawn: Int
    var top: String?
    var topY: Int?
    var newest: Bool
    var follows: Bool
    var offset: Int
    var content: Int
    var frozen: Bool
    /// The newest few lines' ids (their ends).
    var ids: [String]
    /// The list's horizontal offset (-16 at rest: the side margins).
    var x: Int
    /// Within `ChatFollow.tolerance` of the newest end; the follow mode; ↓ Jump to Latest shows; how far the list is from its
    /// newest end (pt); a finger is on it; the pin holds a header.
    var near: Bool
    var mode: String
    var arrow: Bool
    var gap: Int
    var dragging: Bool
    var held: Bool
    /// Landings that left nothing drawn and were mended (`ChatView.mendBlankLanding`).
    var blank: Int
    /// ↓ taps that came while the list still glided after a flick (#3027), and how long the last ↓ took to the newest
    /// end (ms; -1: none yet).
    var glideTaps: Int
    var jumpMs: Int
    /// ↓ landed and the list was then taken away from the newest end while following (#4788): it went there again.
    var slips: Int
    /// The list's distance from its newest end as its scroll view has it now (pt; -1: not known), not SwiftUI's
    /// geometry, which can lag a move made on the scroll view (#1211).
    var listGap: Int
    /// The newest line has some part on screen.
    var lastShown: Bool
    /// SwiftUI's stored scroll position names a row: once a row was opened (`ChatPin.letGo`), the user's own scrolling
    /// stores the row it leaves on screen, and SwiftUI scrolls back to it when the content changes (#4788).
    var storedRow: Bool
    /// Where the newest line starts in the list's content (16, the rows' spacing past the end sentinel, when the lazy stack
    /// placed it from its start; #7382).
    var newestY: Int?
    /// Lists built again because the stack had placed its newest end elsewhere (`ChatView.mendMisplacedStart`).
    var startMends: Int
}
#endif

/// A page's dismiss action for code that runs later (a back button's handler). SwiftUI hands out a new `DismissAction`
/// whenever the presentation around the view changes; a big view that declares `@Environment(\.dismiss)` is drawn again
/// each time. `DismissReader` declares it instead and keeps the latest here, unobserved (docs/apple/performance.md P5).
@MainActor final class DismissHolder {
    var action: DismissAction?
}

struct DismissReader: View {
    let holder: DismissHolder
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        let _ = holder.action = dismiss
        Color.clear
            .allowsHitTesting(false)
            .accessibilityHidden(true)
    }
}

/// The identifier of a chat's leading button while it opens the sidebar (#8603): not the symbol's, so a test tells it
/// from a ‹.
private struct SidebarButtonID: ViewModifier {
    var active: Bool
    func body(content: Content) -> some View {
        if active { content.accessibilityIdentifier("chat-sidebar-button") } else { content }
    }
}
