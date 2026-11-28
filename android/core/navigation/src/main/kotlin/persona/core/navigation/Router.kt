package persona.core.navigation

import persona.core.contracts.ProtocolCompatibility

import androidx.navigation3.runtime.NavKey
import java.net.URI
import java.net.URLDecoder

sealed interface RouteInput {
    data class DeepLink(val url: String) : RouteInput
    data class Notification(
        val sessionPath: String,
        val kind: ConversationKind? = null,
        val requestId: String? = null,
    ) : RouteInput
    data class Share(val chat: ChatRoute) : RouteInput
    data class Internal(val key: NavKey) : RouteInput
}

/**
 * Pairing credentials are transient input, never a serialized back-stack key. [focusComposer]: the Persona tab opens
 * with its composer focused and the keyboard up ([AskPersonaLink]). [card]: Agents opens at its list with this request
 * card open (a card outside any session, #4808).
 */
class ResolvedRoute(
    val tab: AppTab,
    val key: NavKey,
    val pairingLink: String? = null,
    val focusComposer: Boolean = false,
    val card: RequestRef? = null,
    /** An explicit secondary parent when a management entry has no stack to infer it from. */
    val parent: NavKey? = null,
    /** The hub group this page belongs to; the shell selects it before opening when needed. */
    val hubGroupId: String? = null,
)

/** The launcher's "Ask Persona": the Persona tab with the cursor in its composer, cold start or not. */
const val AskPersonaLink = "persona://open?target=tab&tab=persona&focus=composer"

/** Agents › To-dos with a new to-do being written ([NewTodoRoute]). */
const val NewTodoLink = "persona://open?target=new-todo"

class Router(
    /** Resolves feature-specific hub pages in the app shell; null uses the destinations core navigation owns. */
    private val hubLink: ((String) -> ResolvedRoute?)? = null,
    private val conversationKind: (machineId: String, sessionId: String) -> ConversationKind? = { _, _ -> null },
) {
    /** Null means invalid input or missing session metadata; retain pending legacy input until metadata arrives. */
    fun parse(input: RouteInput): ResolvedRoute? = when (input) {
        is RouteInput.DeepLink -> deepLink(input.url)
        is RouteInput.Notification -> sessionPath(input.sessionPath, input.kind)?.let { chat ->
            resolve(input.requestId?.let { InteractionRoute(chat, it) } ?: chat)
        }
        is RouteInput.Share -> resolve(input.chat)
        is RouteInput.Internal -> resolve(input.key)
    }

    private fun resolve(key: NavKey): ResolvedRoute? = when (key) {
        AssistantRoute -> ResolvedRoute(AppTab.Persona, key)
        AgentsRoute -> ResolvedRoute(AppTab.Agents, key)
        InboxRoute -> ResolvedRoute(AppTab.Persona, key)
        SettingsRoute -> ResolvedRoute(AppTab.Persona, key)
        NewTodoRoute -> ResolvedRoute(AppTab.Agents, key)
        ConnectionSettingsRoute -> resolve(SettingsPageRoute(SettingsPage.Diagnostics))
        PairingRoute, is SettingsPageRoute -> ResolvedRoute(AppTab.Persona, key)
        is ExtensionUiRoute -> key.takeIf { it.extensionId.isNotBlank() && it.uiId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is ExtensionRoute -> key.takeIf { it.extensionId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is InboxItemRoute -> key.takeIf { it.itemId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is LarkChatRoute -> key.takeIf { it.chatId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is ChatRoute -> normalizedChat(key)?.let { ResolvedRoute(it.kind.tab, it) }
        is InteractionRoute -> normalizedChat(key.chat)?.takeIf { key.requestId.isNotBlank() }?.let {
            ResolvedRoute(it.kind.tab, key.copy(chat = it))
        }
        else -> null
    }

    private fun normalizedChat(chat: ChatRoute): ChatRoute? {
        if (chat.machineId.isBlank() || chat.sessionId.isBlank()) return null
        val kind = chat.kind ?: conversationKind(chat.machineId, chat.sessionId) ?: return null
        val validSource = when (val source = chat.source) {
            null -> true
            is RouteSource.InboxItem -> source.itemId.isNotBlank()
            is RouteSource.LarkConversation -> source.chatId.isNotBlank()
            is RouteSource.Interaction -> source.machineId.isNotBlank() && source.requestId.isNotBlank()
        }
        return chat.copy(kind = kind).takeIf { validSource }
    }

    private fun deepLink(url: String): ResolvedRoute? {
        val uri = try { URI(url) } catch (_: IllegalArgumentException) { return null }
            catch (_: java.net.URISyntaxException) { return null }
        if (uri.scheme?.lowercase() in ProtocolCompatibility.appSchemes && uri.isPageLink()) {
            if (hubLink != null) return hubLink.invoke(url)
            val parsed = HubLinks.parse(url, emptyList()) as? HubLinks.Result.Page ?: return null
            return resolve(parsed.page)
        }
        if (uri.scheme !in ProtocolCompatibility.appSchemes || uri.rawAuthority !in setOf("open", "pair") ||
            !uri.rawPath.isNullOrEmpty() || uri.rawFragment != null) return null
        if (uri.host == "pair") {
            // PairingInput remains responsible for validating credentials and transport policy.
            return ResolvedRoute(AppTab.Persona, PairingRoute, url)
        }
        val parameters = linkedMapOf<String, String>()
        try {
            for (part in uri.rawQuery.orEmpty().split('&').filter { it.isNotEmpty() }) {
                val name = decode(part.substringBefore('='))
                val value = decode(part.substringAfter('=', ""))
                if (parameters.put(name, value) != null) return null
            }
        } catch (_: IllegalArgumentException) { return null }
        val kind = when (parameters["kind"]) {
            null -> null
            "assistant" -> ConversationKind.Assistant
            "agent" -> ConversationKind.Agent
            else -> return null
        }
        val key: NavKey = when (parameters["target"] ?: "chat") {
            "chat", "request" -> {
                val chat = ChatRoute(parameters["machine"] ?: return null, parameters["session"] ?: return null, kind)
                // A notification's link without a tab (session metadata not loaded yet) still names its request.
                val request = parameters["request"]
                if (parameters["target"] == "request" || parameters["target"] == null && !request.isNullOrBlank())
                    InteractionRoute(chat, request ?: return null)
                else chat
            }
            "extension" -> {
                val extension = parameters["extension"] ?: return null
                val ui = parameters["ui"]
                if (ui == null) {
                    if (parameters["trigger"] != "content") return null
                    ExtensionRoute(extension, manager = parameters["manager"])
                } else ExtensionUiRoute(extension, ui, parameters["title"].orEmpty(), parameters["state"],
                    manager = parameters["manager"], notificationActionToken = parameters["notification"])
            }
            "inbox" -> InboxItemRoute(parameters["item"] ?: return null)
            "new-todo" -> NewTodoRoute
            "lark" -> LarkChatRoute(parameters["chat"] ?: return null)
            "settings" -> SettingsPageRoute(if (parameters["page"] == "connectors/sent") SettingsPage.Sent
                else SettingsPage.entries.firstOrNull {
                    it.name.equals(parameters["page"], ignoreCase = true)
                } ?: return null)
            "tab" -> linkedPage(ProtocolCompatibility.canonicalTab(parameters["tab"]) ?: return null) ?: return null
            else -> return null
        }
        val resolved = resolve(key) ?: return null
        // Both the Persona tab (main chat) and an explicit ChatRoute have a composer to focus.
        return if (parameters["focus"] == "composer" && (resolved.key == AssistantRoute || resolved.key is ChatRoute)) {
            ResolvedRoute(resolved.tab, resolved.key, resolved.pairingLink, focusComposer = true)
        } else resolved
    }

    private fun resolve(page: HubPage): ResolvedRoute? = when (page) {
        is HubPage.Agent -> {
            page.itemId?.let { MessageJumps.request(page.machineId, page.sessionId, it) }
            if (page.view == "term") ResolvedRoute(AppTab.Agents, TerminalRoute(page.machineId, page.sessionId))
            else resolve(ChatRoute(page.machineId, page.sessionId, ConversationKind.Agent))
        }
        is HubPage.Conversation -> {
            if (page.sessionId == null) return resolve(AssistantRoute)
            val machine = page.machineId?.takeIf { it.isNotBlank() } ?: return null
            page.itemId?.let { MessageJumps.request(machine, page.sessionId, it) }
            resolve(ChatRoute(machine, page.sessionId, ConversationKind.Assistant))
        }
        HubPage.NewConversation -> ResolvedRoute(AppTab.Persona, AssistantRoute, focusComposer = true)
        is HubPage.Knowledge, is HubPage.KnowledgeEntry -> null // Feature routes are supplied by the app shell.
        is HubPage.Screen -> resolveLinkedScreen(page.key)
    }

    /** Additional destinations accepted from a route-table link without changing RouteInput.Internal's old contract. */
    private fun resolveLinkedScreen(key: NavKey): ResolvedRoute? = when (key) {
        TodosRoute -> ResolvedRoute(AppTab.Agents, key)
        is TodoDetailRoute -> key.takeIf { it.todoId.isNotBlank() }?.let { ResolvedRoute(AppTab.Agents, it) }
        MachinesRoute -> ResolvedRoute(AppTab.Persona, key)
        is MachineDetailRoute -> key.takeIf { it.machineId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is TerminalRoute -> key.takeIf { it.machineId.isNotBlank() && it.sessionId.isNotBlank() }?.let { ResolvedRoute(AppTab.Agents, it) }
        is ModelPurposeRoute -> key.takeIf { it.purposeId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is ChannelRoute -> key.takeIf { it.channelId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is ConnectorRoute -> key.takeIf { it.connectorId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        is ManagerRoute -> key.takeIf { it.managerId.isNotBlank() }?.let { ResolvedRoute(AppTab.Persona, it) }
        else -> resolve(key)
    }
}

private fun URI.isPageLink(): Boolean = when (host?.lowercase()) {
    "session", "task", "kb", "settings", "extension" -> true
    "open" -> !rawPath.isNullOrEmpty() || rawQuery.orEmpty().split('&').any { it.substringBefore('=') == "route" }
    else -> false
}

private fun decode(value: String): String = URLDecoder.decode(value.replace("+", "%2B"), "UTF-8")

internal fun sessionPath(path: String, kind: ConversationKind?): ChatRoute? {
    val separator = path.indexOf('/')
    if (separator <= 0 || separator == path.lastIndex) return null
    return ChatRoute(path.substring(0, separator), path.substring(separator + 1), kind)
}

private val ConversationKind?.tab: AppTab
    get() = if (this == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents

fun tabRoot(tab: AppTab): NavKey = when (tab) {
    AppTab.Persona -> AssistantRoute
    AppTab.Agents -> AgentsRoute
}

/** Retired tab links still open their content, now as secondary pages under Persona. */
internal fun linkedPage(name: String): NavKey? = when (name.lowercase()) {
    "persona" -> AssistantRoute
    "agents" -> AgentsRoute
    "inbox" -> InboxRoute
    "settings" -> SettingsRoute
    else -> null
}

/** Select the returned tab after applying this to the app-owned stacks; "New to-do" also asks [newTodo] for its sheet. */
fun ResolvedRoute.restore(stacks: Map<AppTab, MutableList<NavKey>>, newTodo: NewTodoRequests = NewTodoOpen): AppTab {
    val stack = stacks.getValue(tab)
    if (stack.isEmpty()) stack.add(tabRoot(tab))
    // The card is listed on the tab's root and the Persona tab's composer sits there: pages left open on top are closed first.
    if (card != null || (focusComposer && key == tabRoot(tab))) while (stack.size > 1) stack.removeAt(stack.lastIndex)
    if (key == tabRoot(tab)) return tab
    // Conversations have one owning tab; secondary pages can legitimately remain open in both stacks.
    if (key is ChatRoute || key is InteractionRoute) {
        stacks.filterKeys { it != tab }.values.forEach { other -> other.removeAll { sameTarget(it, key) } }
    }
    // Pop to an existing target without replacing its saved UI state.
    val existing = stack.indexOfFirst { sameTarget(it, key) }
    if (existing < 0) stack.add(key)
    else {
        while (stack.lastIndex > existing) stack.removeAt(stack.lastIndex)
        // Preserve the existing NavKey and its UI state when only provenance changed.
        val previous = stack[existing]
        val withDecisionSource = if (previous is ChatRoute && key is ChatRoute)
            previous.copy(decisionSource = key.decisionSource ?: previous.decisionSource) else previous
        if (previous == ConnectionSettingsRoute ||
            previous is ChatRoute && key is ChatRoute && previous.kind != key.kind ||
            // A notification about one request of a conversation already open: that page shows this request first.
            key is InteractionRoute && previous != key) {
            stack[existing] = key
        } else if (withDecisionSource != previous) stack[existing] = withDecisionSource
    }
    if (parent != null && !stack.contains(parent)) stack.add(stack.lastIndex, parent)
    // Keep the serialized Sent key compatible, but make its parent explicit for links and history restores.
    restoreSecondaryParents(stack)
    // The list kept from an earlier use keeps its state too: its sheet opens on this request, each time.
    if (key == NewTodoRoute) newTodo.request()
    return tab
}

/** Fill missing secondary-page parents without replacing restored entries or the page they were opened from. */
fun restoreSecondaryParents(stack: MutableList<NavKey>) {
    restoreConnectorParents(stack)
    var index = 0
    var hasSettings = false
    var hasInbox = false
    while (index < stack.size) {
        val key = stack[index]
        val parent = when (key) {
            PairingRoute, ConnectionSettingsRoute, is SettingsPageRoute, is ExtensionRoute,
            is ModelProviderRoute, is ChannelRoute, is ManagerRoute, is ConnectorRoute,
            ExtensionMarketRoute, ExtensionMarketsRoute, is ExtensionCatalogRoute -> SettingsRoute.takeUnless { hasSettings }
            // An extension page keeps the Assistant, Inbox or Settings stack it was opened from.
            is ExtensionUiRoute -> null
            is InboxItemRoute, is InboxItemUiRoute, is LarkChatRoute -> InboxRoute.takeUnless { hasInbox }
            else -> null
        }
        if (parent != null) {
            stack.add(index, parent)
            index++
        }
        hasSettings = hasSettings || key == SettingsRoute || parent == SettingsRoute
        hasInbox = hasInbox || key == InboxRoute || parent == InboxRoute
        index++
    }
}

/** Insert the ledger's connector parent without replacing its saved key or affecting other pages. */
fun restoreConnectorParents(stack: MutableList<NavKey>) {
    var index = 0
    val parent = SettingsPageRoute(SettingsPage.Connectors)
    while (index < stack.size) {
        val key = stack[index]
        val child = key == SettingsPageRoute(SettingsPage.Sent)
        if (child && (index == 0 || stack[index - 1] != parent)) {
            stack.add(index, parent)
            index++
        }
        index++
    }
}

private fun sameTarget(left: NavKey, right: NavKey): Boolean = when {
    // A side session's own chat (#7944) sits over the page a link to it opened, its parent's: two pages.
    left is ChatRoute && right is ChatRoute -> left.machineId == right.machineId && left.sessionId == right.sessionId && left.side == right.side
    // One conversation is one page, whichever of its requests it was opened at.
    left is InteractionRoute -> sameTarget(left.chat, right)
    right is InteractionRoute -> sameTarget(left, right.chat)
    left == ConnectionSettingsRoute -> right == SettingsPageRoute(SettingsPage.Diagnostics)
    else -> left == right
}
