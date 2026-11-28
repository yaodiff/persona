package persona.android.navigation

import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateSetOf
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import persona.core.contracts.SessionKey
import persona.core.hub.errors.ErrorBreadcrumbs
import persona.feature.inbox.ConnectorThreadRoute
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemDetailRoute
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.MailRoute

internal class AppNavigator(
    private val stacks: Map<AppTab, MutableList<NavKey>>,
    private val selectedTab: () -> AppTab,
    private val onSelectTab: (AppTab) -> Unit,
    private val chatTab: (ChatRoute) -> AppTab? = { null },
    /** Returns true when the Persona home shows this conversation itself; the flag says whether to switch to it. */
    private val openAssistant: (ChatRoute, Boolean) -> Boolean = { _, _ -> false },
    inboxReturns: List<ChatRoute>? = null,
    /**
     * Regular width: a route to a sidebar place (or a child of one) switches that root instead of pushing
     * onto whatever root is in front. Returns true when it did, so the caller must not also push.
     * iOS selects the place, then sets the path (`AssistantView` open-from-outside, AssistantView.swift:601–605).
     */
    private val openPlace: (NavKey) -> Boolean = { false },
    /**
     * Regular width, the stack is only a place root: Back switches to the conversation instead of leaving
     * the app. iOS `leavePlace` selects the conversation (`AssistantView.swift:682–686`) after
     * `assistantReveal` (`AppModel.swift:1381`, `:1393`). Returns true when it switched.
     */
    private val leavePlaceRoot: () -> Boolean = { false },
    /** The Inbox was navigated to (a link to it in a chat or from the hub) on this tab: its list opens as on entry (#9053). */
    private val onInboxEntered: (AppTab) -> Unit = {},
    /**
     * Merged sidebar layout (#13165): an Agent opened from inside a Persona page stays on that root's stack
     * instead of moving to the Agents tab, so picking another sidebar item and returning restores it.
     */
    private val keepPersonaAgents: () -> Boolean = { false },
) : Navigator {
    /**
     * Pages [navigate] pushed onto another tab's stack, and the tab each was opened from: back from such a page returns
     * to that tab (a task card in the Persona home opens its agent's chat under Agents; back goes to the home, not to
     * the Agents list). The user picking a tab themselves drops these.
     */
    private val openedFrom = mutableMapOf<NavKey, AppTab>()
    /** Null is legacy construction; an explicit empty restore must not revive a retained NavKey's source. */
    private val hasExplicitInboxReturns = inboxReturns != null
    private val inboxSources = mutableStateMapOf<SessionKey, RouteSource.InboxItem>().apply {
        // A legacy position has no separate ledger. Seed it before Root's first automatic save, then let an explicitly
        // persisted latest source win over the source retained by the visible NavKey.
        if (inboxReturns == null) {
            stacks.values.forEach { stack ->
                stack.filterIsInstance<ChatRoute>().forEach { route ->
                    (route.source as? RouteSource.InboxItem)?.let {
                        this[SessionKey(route.machineId, route.sessionId)] = it
                    }
                }
            }
        }
        inboxReturns.orEmpty().forEach { route ->
            (route.source as? RouteSource.InboxItem)?.let { this[SessionKey(route.machineId, route.sessionId)] = it }
        }
    }
    /** An explicit non-inbox open supersedes provenance retained by an existing NavKey. */
    private val inboxSourceSuppressions = mutableStateSetOf<SessionKey>()

    init { stacks.values.forEach(::restoreSecondaryParents) }

    /** Serializable provenance for ShellPosition; the visible Agent key and its saved UI state stay unchanged. */
    fun inboxReturns(): List<ChatRoute> = inboxSources.map { (session, source) ->
        ChatRoute(session.machineId, session.sessionId, source = source)
    }.sortedWith(compareBy(ChatRoute::machineId, ChatRoute::sessionId))

    /** The tab whose inside page pushed [page], or null for a root / direct open. */
    fun sourceTab(page: NavKey?): AppTab? = if (page == null) null else openedFrom[page]

    /** Root applies the same latest-ledger guard before embedding a direct assistant destination in Persona home. */
    fun hasInboxOrigin(key: NavKey): Boolean = inboxSource(key) != null

    override fun selectTab(tab: AppTab) {
        openedFrom.clear()
        inboxSources.clear()
        inboxSourceSuppressions.clear()
        // Explicit Agents selection opens its list; targeted session navigation uses onSelectTab directly.
        if (tab == AppTab.Agents) {
            val stack = stacks.getValue(tab)
            while (stack.size > 1) stack.removeAt(stack.lastIndex)
        }
        onSelectTab(tab)
        ErrorBreadcrumbs.shared.navigation("tab:${tab.name}")
    }

    override fun navigate(key: NavKey) = navigate(key, replacingDetailIn = null)

    /** A list pick replaces its detail only after routing confirms it belongs to that tab. */
    fun navigate(key: NavKey, replacingDetailIn: AppTab?) {
        val from = selectedTab()
        // The route's type only, never its arguments (machine, session, item ids).
        ErrorBreadcrumbs.shared.navigation(key::class.simpleName ?: "route")
        if (key is ChatRoute && key.decisionSource != null) {
            // To Decide lives in the Assistant (Persona) host. Stack the source over that host only when it is
            // actually present; a late callback while the user sits on inbox/settings must not append to whatever
            // tab is selected, otherwise Back returns to an unrelated page (review P1).
            val personaStack = stacks.getValue(AppTab.Persona)
            val personaTop = personaStack.lastOrNull()
            // Already inside an Assistant conversation: stack the source directly above it for one-step Back.
            // At the Persona root: build root Assistant -> source. Any other top means Persona is on an unrelated
            // page, so fall through to normal routing rather than mutating an unexpected stack.
            if (personaTop is ChatRoute || personaTop is AssistantRoute) {
                // A side session is opened by the source chat after it lands. Put it over the Assistant host, not over
                // the source task chat, so Back returns to the pager that listed the decision.
                val host = if (key.side) {
                    personaStack.indexOfLast {
                        it is AssistantRoute || it is ChatRoute && it.kind == ConversationKind.Assistant
                    }
                } else -1
                val existing = personaStack.indexOfFirst {
                    it is ChatRoute && it.machineId == key.machineId && it.sessionId == key.sessionId && it.side == key.side
                }
                if (existing >= 0) {
                    while (personaStack.lastIndex > existing) personaStack.removeAt(personaStack.lastIndex)
                    val page = personaStack[existing] as ChatRoute
                    personaStack[existing] = page.copy(decisionSource = key.decisionSource)
                } else {
                    if (host >= 0) while (personaStack.lastIndex > host) personaStack.removeAt(personaStack.lastIndex)
                    if (personaStack.lastOrNull() != key) personaStack.add(key)
                }
                onSelectTab(AppTab.Persona)
                return
            }
        }
        val shown = stacks.mapValues { it.value.lastOrNull() }
        // Reopening the unchanged visible route is idempotent, not a new direct open that invalidates its origin.
        val visibleOrigin = shown[from]?.takeIf { it == key }?.let(openedFrom::get)
        val opened = open(key, replacingDetailIn)
        val to = selectedTab()
        if (key == InboxRoute) onInboxEntered(to)
        val page = stacks.getValue(to).lastOrNull() ?: return
        if (to == from && page == key && visibleOrigin != null) openedFrom[page] = visibleOrigin
        // Not recorded: the tab's root, and a page that already topped the target tab's stack but is not the one asked
        // for (this call did not open it).
        if (to != from && page != tabRoot(to) && (page != shown[to] || page == opened)) openedFrom[page] = from
    }

    /** A sidebar pick changes the displayed Agent root while retaining the logical Agents anchor. */
    fun pickSidebarAgent(key: NavKey) {
        val stack = stacks.getValue(AppTab.Agents)
        // Use the existing key for the same session, even when the list omits kind or the key has saved provenance.
        val target = (key as? ChatRoute)?.let { chat -> stack.firstOrNull { sameKindlessTarget(it, chat) } } ?: key
        noteDirectOpen(key)
        openedFrom.remove(target)
        replaceBackStack(stack, listOf(AgentsRoute, target))
        if (target is ChatRoute) {
            stacks.getValue(AppTab.Persona).removeAll { page -> page.chat()?.let { sameConversation(it, target) } == true }
        }
        onSelectTab(AppTab.Agents)
    }

    /** Record a direct ResolvedRoute open without changing its route, card, focus, or saved UI state. */
    fun noteOpened(key: NavKey) {
        val chat = key.chat() ?: return
        val session = SessionKey(chat.machineId, chat.sessionId)
        // Reopening a conversation invalidates its previous cross-tab origin, even when this open stays on the same tab.
        openedFrom.keys.removeAll { page -> page.chat()?.let { sameConversation(it, chat) } == true }
        val source = chat.source as? RouteSource.InboxItem
        if (source != null) {
            inboxSources[session] = source
            inboxSourceSuppressions.remove(session)
        } else {
            inboxSources.remove(session)
            inboxSourceSuppressions.add(session)
        }
    }

    /**
     * Record a notification, deep-link or sidebar open. Unlike an in-app chat-to-terminal navigation, a direct terminal
     * open starts a new return chain for the whole session, including the synthetic conversation below the terminal.
     */
    fun noteDirectOpen(key: NavKey) {
        val terminal = key as? TerminalRoute ?: return noteOpened(key)
        val session = SessionKey(terminal.machineId, terminal.sessionId)
        openedFrom.keys.removeAll { page -> page.session() == session }
        inboxSources.remove(session)
        inboxSourceSuppressions.add(session)
    }

    /** Returns the actual key opened, which may have inherited the visible inbox conversation's source. */
    private fun open(key: NavKey, replacingDetailIn: AppTab? = null): NavKey {
        val route = inheritVisibleInboxSource(key)
        if (route == ConnectionSettingsRoute) return open(SettingsPageRoute(SettingsPage.Diagnostics), replacingDetailIn)
        val chat = when (route) { is ChatRoute -> route; is InteractionRoute -> route.chat; else -> null }
        val existingTab = chat?.let { target ->
            stacks.entries.firstOrNull { (_, stack) ->
                stack.any { page -> page.chat()?.let { sameConversation(it, target) } == true }
            }?.key
        }
        noteOpened(route)
        if (chat != null && inboxSource(route) == null && openAssistant(chat, true)) return route
        // A sourced assistant conversation belongs above the item that opened it, even when its kind is already known.
        // A kindless conversation reopened from the other tab instead restores its existing page and saved UI state.
        val fromInbox = chat != null && chat.source is RouteSource.InboxItem &&
            (chat.kind == ConversationKind.Assistant || chatTab(chat) == AppTab.Persona)
        val keepAgentOnPersona = chat != null && keepPersonaAgents() && selectedTab() == AppTab.Persona &&
            chat.machineId.isNotBlank() && chat.sessionId.isNotBlank() &&
            (route !is InteractionRoute || route.requestId.isNotBlank())
        val resolved = when {
            chat == null -> null
            fromInbox -> null
            keepAgentOnPersona -> ResolvedRoute(AppTab.Persona, route)
            chat.kind == null && existingTab != null && existingTab != selectedTab() -> ResolvedRoute(existingTab, route)
            chat.kind != null -> Router().parse(RouteInput.Internal(route))
            // Keep the kindless key itself: normalizing it would replace the existing page and lose its saved UI state.
            else -> {
                val target = existingTab
                    ?: chatTab(chat)
                    ?: selectedTab().takeIf { it == AppTab.Persona }
                    ?: AppTab.Agents
                ResolvedRoute(target, route).takeIf { chat.source !is RouteSource.InboxItem || target == AppTab.Agents }
            }
        }
        if (resolved != null) {
            // A kindless reopen must not let Router.restore replace a known-kind page (and its Navigation3 UI state)
            // merely because the incoming key now names less metadata. Interaction requests still replace normally
            // unless they name the very same request.
            val retained = if (chat?.kind == null) {
                stacks.getValue(resolved.tab).firstOrNull { page ->
                    page.chat()?.kind != null && sameKindlessTarget(page, route)
                }
            } else null
            replaceDetail(resolved.tab, replacingDetailIn, route)
            val tab = resolved.restore(stacks)
            if (retained != null) {
                val stack = stacks.getValue(tab)
                val restoredAt = stack.indexOfFirst { it == route }
                if (restoredAt >= 0) {
                    stack[restoredAt] = if (retained is ChatRoute && route is ChatRoute && route.decisionSource != null)
                        retained.copy(decisionSource = route.decisionSource) else retained
                }
            }
            onSelectTab(tab)
            return stacks.getValue(tab).lastOrNull() ?: route
        }
        val rootTab = when (route) {
            AssistantRoute -> AppTab.Persona
            AgentsRoute -> AppTab.Agents
            else -> null
        }
        if (rootTab != null) {
            onSelectTab(rootTab)
            return route
        }
        val target = if (route is ChatRoute || fromInbox) existingTab?.takeIf { chat?.kind == null }
            ?: chatTab(requireNotNull(chat))
            ?: stacks.entries.firstOrNull { route in it.value }?.key
            ?: selectedTab().takeIf { it == AppTab.Persona }
            ?: AppTab.Agents else selectedTab()
        replaceDetail(target, replacingDetailIn, route)
        onSelectTab(target)
        if (chat != null && (route is ChatRoute || fromInbox)) stacks.filterKeys { it != target }.values.forEach { stack ->
            stack.removeAll { page -> page is ChatRoute && sameConversation(page, chat) }
        }
        val stack = stacks.getValue(target)
        val visibleMail = stack.lastOrNull() as? MailRoute
        // A UID chooses a position in one mail page. Replace its visible target instead of stacking another copy.
        if (route is MailRoute && visibleMail?.itemId == route.itemId) {
            if (visibleMail != route) stack[stack.lastIndex] = route
            return route
        }
        // The wide Inbox can reopen the event beside its mail; reveal that event rather than burying the mail below it.
        if (route is InboxEventRoute && visibleMail?.itemId == route.itemId &&
            stack.getOrNull(stack.lastIndex - 1) == route) {
            stack.removeAt(stack.lastIndex)
            return route
        }
        // The sourced-Persona path does not use Router.restore, but an already-visible kindless reopen needs the same
        // exact-key retention. A page buried below a newer detail keeps the existing push/pop chain behavior.
        val retained = if (chat?.kind == null) stack.lastOrNull()?.takeIf { sameKindlessTarget(it, route) } else null
        // A place route at regular width replaces the root. Pushing it here would insert Inbox under To-dos
        // once restoreSecondaryParents runs (Router.kt:203–227). A drill on the place already in front returns false.
        if (target == AppTab.Persona && openPlace(route)) return retained ?: route
        if (retained == null && stack.last() != route) stack.add(route)
        restoreSecondaryParents(stack)
        return retained ?: route
    }

    private fun replaceDetail(tab: AppTab, replacingDetailIn: AppTab?, key: NavKey) {
        val stack = stacks.getValue(tab)
        if (tab == replacingDetailIn && stack.lastOrNull() != key) {
            while (stack.size > 1) stack.removeAt(stack.lastIndex)
        }
    }

    /** A child conversation opened above an inbox conversation belongs to the same return chain. */
    private fun inheritVisibleInboxSource(key: NavKey): NavKey {
        if (selectedTab() != AppTab.Persona) return key
        val visible = stacks.getValue(AppTab.Persona).lastOrNull() ?: return key
        val source = inboxSource(visible) ?: return key
        return when (key) {
            is ChatRoute -> if (key.source == null) key.copy(source = source) else key
            is InteractionRoute -> if (key.chat.source == null) key.copy(chat = key.chat.copy(source = source)) else key
            else -> key
        }
    }

    /** Repair restored entries once session metadata becomes available. */
    fun restoreChatTabs() {
        val visible = stacks.getValue(selectedTab()).lastOrNull()
        stacks.forEach { (tab, stack) ->
            stack.filterIsInstance<ChatRoute>().forEach chatEntry@ { chat ->
                val owner = chatTab(chat) ?: return@chatEntry
                if (owner == AppTab.Persona && inboxSource(chat) == null && openAssistant(chat, visible == chat)) {
                    stack.removeAll { it == chat }
                    return@chatEntry
                }
                if (owner != tab) {
                    if (tab == AppTab.Persona && owner == AppTab.Agents && keepPersonaAgents()) return@chatEntry
                    stack.removeAll { it == chat }
                    val source = inboxSource(chat)
                    // At regular width the item is a root switch, then the chat sits on that host.
                    // Pushing onto the conversation would make [Assistant, Inbox, InboxItem] (a drill, composed twice).
                    val placed = owner == AppTab.Persona && source != null && openPlace(InboxItemRoute(source.itemId))
                    val destination = stacks.getValue(owner)
                    if (chat !in destination) {
                        if (placed || owner != selectedTab() || visible == chat) destination.add(chat)
                        else destination.add(1, chat)
                    }
                    // The restored Agent used to live under Agents. Remember that cross-tab return even after late
                    // metadata moves its unchanged key to Persona; Back must rebuild the source detail, not the list.
                    if (tab == AppTab.Agents && owner == AppTab.Persona && source != null) {
                        openedFrom[chat] = tab
                    }
                    if (visible == chat) onSelectTab(owner)
                }
            }
        }
    }

    override fun back() {
        val from = selectedTab()
        val stack = stacks.getValue(from)
        if (stack.size <= 1) {
            // A pushed page pops. A place that is the whole stack switches to the conversation (item 4, item 5).
            if (from == AppTab.Persona) { val _ = leavePlaceRoot() }
            return
        }
        val page = stack.removeAt(stack.lastIndex)
        val origin = openedFrom.remove(page)
        val source = inboxSource(page)
        if (source != null && (from == AppTab.Agents || origin == AppTab.Agents)) {
            val persona = stacks.getValue(AppTab.Persona)
            // A retained Persona stack may belong to an older item, or contain only the list. Return to the latest
            // item's actual detail unless its detail, native source page or processing conversation is already on top.
            val visibleItem = persona.lastOrNull()?.let(::displayedInboxItemId)
            if (visibleItem != source.itemId) {
                val item = InboxItemRoute(source.itemId)
                // Regular width: the inbox host shows the item. Compact still pushes, and the parent insert
                // stays in Router.restoreSecondaryParents (Router.kt:203–227).
                if (!openPlace(item)) {
                    persona.add(item)
                    restoreSecondaryParents(persona)
                }
            }
            // A real inbox source takes precedence over a stale openedFrom=Agents origin.
            onSelectTab(AppTab.Persona)
        } else if (origin != null) {
            onSelectTab(origin)
        }
    }

    private fun inboxSource(page: NavKey): RouteSource.InboxItem? {
        val chat = page.chat() ?: return null
        val session = SessionKey(chat.machineId, chat.sessionId)
        inboxSources[session]?.let { return it }
        if (session in inboxSourceSuppressions || hasExplicitInboxReturns) return null
        return chat.source as? RouteSource.InboxItem
    }

    /** Latest provenance names the visible processing page's item; an unrelated sourced page can still name its own. */
    private fun displayedInboxItemId(page: NavKey): String? {
        val chat = page.chat() ?: return page.inboxItemId()
        val session = SessionKey(chat.machineId, chat.sessionId)
        inboxSources[session]?.let { return it.itemId }
        if (session in inboxSourceSuppressions) return null
        return page.inboxItemId()
    }

    /** Remove every destination owned by the confirmed closed session, including hidden tabs. */
    fun sessionClosed(session: SessionKey) {
        stacks.values.forEach { stack ->
            stack.removeAll { key ->
                when (key) {
                    is ChatRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    is InteractionRoute -> key.chat.machineId == session.machineId && key.chat.sessionId == session.sessionId
                    is TerminalRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    else -> false
                }
            }
        }
        inboxSources.remove(session)
        inboxSourceSuppressions.remove(session)
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }

    /**
     * The chat's hub group went away: its pages become [moved] (the same session under its new id) where they stand,
     * else they go. Visited tabs stay composed, so this can come from a hidden tab: the tab on screen stays as it is.
     */
    fun groupGone(chat: ChatRoute, moved: ChatRoute?) {
        val previous = SessionKey(chat.machineId, chat.sessionId)
        val source = inboxSources.remove(previous)
        val suppressed = inboxSourceSuppressions.remove(previous)
        if (moved != null) {
            val destination = SessionKey(moved.machineId, moved.sessionId)
            if (source != null) {
                inboxSources[destination] = source
                inboxSourceSuppressions.remove(destination)
            } else if (suppressed) {
                inboxSources.remove(destination)
                inboxSourceSuppressions.add(destination)
            }
        }
        fun shows(key: NavKey) = key.chat()?.let { sameConversation(it, chat) } == true
        // Callers rebuilding the replacement may only know its new ids and kind; provenance belongs to the existing page.
        val destination = moved?.let {
            if (it.source == null && chat.source != null) it.copy(source = chat.source) else it
        }
        stacks.values.forEach { stack ->
            val at = stack.indexOfFirst { shows(it) }.takeIf { it >= 0 } ?: return@forEach
            val previous = stack[at]
            val from = stack.firstNotNullOfOrNull { page -> openedFrom[page]?.takeIf { shows(page) } }
            stack.removeAll { shows(it) }
            if (destination != null && stack.none { it is ChatRoute && sameConversation(it, destination) }) {
                // A known-kind request remains that exact request through migration; a kindless reopen can then retain
                // its InteractionRoute instead of receiving a plain ChatRoute. Legacy kindless pages keep old behavior.
                val replacement = if (previous is InteractionRoute && previous.chat.kind != null) {
                    previous.copy(chat = destination)
                } else {
                    destination
                }
                stack.add(at, replacement)
                from?.let { openedFrom[replacement] = it }
            }
        }
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }
}

private fun NavKey.chat(): ChatRoute? = when (this) {
    is ChatRoute -> this
    is InteractionRoute -> chat
    else -> null
}

private fun NavKey.session(): SessionKey? = when (this) {
    is ChatRoute -> SessionKey(machineId, sessionId)
    is InteractionRoute -> SessionKey(chat.machineId, chat.sessionId)
    is TerminalRoute -> SessionKey(machineId, sessionId)
    else -> null
}

/** The item this page displays or processes, without treating an unrelated non-root page as its return target. */
internal fun NavKey.inboxItemId(): String? = when (this) {
    is InboxItemRoute -> itemId
    is InboxItemUiRoute -> itemId
    is InboxEventRoute -> itemId
    is InboxItemDetailRoute -> itemId
    is ConnectorThreadRoute -> itemId
    is LarkRoute -> itemId
    is LarkMomentRoute -> itemId
    is MailRoute -> itemId
    is ChatRoute -> (source as? RouteSource.InboxItem)?.itemId
    is InteractionRoute -> (chat.source as? RouteSource.InboxItem)?.itemId
    else -> null
}

private fun sameConversation(left: ChatRoute, right: ChatRoute): Boolean =
    left.machineId == right.machineId && left.sessionId == right.sessionId

/** Only an identical conversation/request keeps its old key; a new interaction request keeps replacement semantics. */
private fun sameKindlessTarget(existing: NavKey, reopened: NavKey): Boolean = when {
    existing is ChatRoute && reopened is ChatRoute -> sameConversation(existing, reopened)
    existing is InteractionRoute && reopened is InteractionRoute ->
        sameConversation(existing.chat, reopened.chat) && existing.requestId == reopened.requestId
    else -> false
}
