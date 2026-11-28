package persona.android.ui

import persona.android.R
import androidx.compose.ui.res.stringResource
import androidx.compose.runtime.*
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.GroupKeys
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.HubGroupInfo
import persona.core.contracts.api.KnowledgeKind
import persona.core.contracts.api.MainConversationSender
import persona.core.designsystem.AppFeedback
import persona.core.navigation.AppTab
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.HubLinks
import persona.core.navigation.HubPage
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MessageJumps
import persona.core.navigation.Navigator
import persona.core.navigation.ResolvedRoute
import persona.core.navigation.TerminalRoute
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.feature.chat.*
import persona.feature.assistant.personaConversationTitle
import persona.feature.inbox.InboxWebSelection
import persona.core.navigation.KnowledgeDetailRoute
import persona.core.navigation.KnowledgeKindRoute
import persona.core.navigation.KnowledgeRoute
import persona.feature.inbox.LocalInboxWebSelection
import java.util.UUID

/**
 * Links for the whole shell: the link sheet over pages outside a conversation (the knowledge base, the Persona home's
 * own links), `persona://` links tapped in any conversation (they open a conversation or a tab, nothing else), links to
 * the hub's own web pages (the assistant's `/s/…`, `/persona/…`, `/todos?id=…`: the same screens in the app), and
 * text picked on a page with no conversation behind it (those pages, and the inbox's): "Hand to Persona" puts it in Persona's
 * main conversation and shows the Persona tab, as the inbox's own "Hand to Persona" does.
 */
@Composable
internal fun ShellLinkHost(session: ShellSession, navigator: Navigator, feedback: AppFeedback, content: @Composable () -> Unit) {
    // The paired sender is the connection's forwarding wrapper, not the outbox itself, so it is no
    // MainConversationSender: the main conversation comes from the pairing's chat wiring, as in a chat (ShellChat).
    // Before, "Hand to Persona" never showed on these pages (only demo mode's sender is one).
    val activeWiring by chatWiringSource().current.collectAsStateWithLifecycle()
    val main = session.sender as? MainConversationSender
        ?: activeWiring?.takeIf { it.scope.path == session.identity }?.mainSender
    val handToPersona: (suspend (String) -> Unit)? = remember(main, navigator) {
        main?.let { sender ->
            val send: suspend (String) -> Unit = { message ->
                sender.sendToMain(message, UUID.randomUUID().toString())
                navigator.selectTab(AppTab.Persona)
            }
            send
        }
    }
    val appLinks: (ChatAppLink) -> Unit = remember(session, navigator) {
        fun open(page: HubPage, hub: String?, source: String) {
            val groups = session.groups.groups.value
            val selected = groups.selectedGroup()
            val route = resolveHubPage(page, selected?.namespace, session.sessions.sessions.value.map { it.key },
                session.assistant.assistant.value, groups, hub) ?: return
            if (route.hubGroupId != null && route.hubGroupId != selected?.id) {
                if (source.isNotEmpty()) persona.android.MainActivity.pendingHubLink.value = source
            } else openResolvedHubPage(route, navigator)
        }
        { link: ChatAppLink ->
            when (link) {
                is ChatAppLink.Session -> open(HubPage.Agent(link.machineId, link.sessionId, link.itemId), null, "")
                is ChatAppLink.Page -> open(link.page, link.hub, link.source)
                is ChatAppLink.Tab -> when (link.tab) {
                    "inbox" -> navigator.navigate(persona.core.navigation.InboxRoute)
                    "settings" -> navigator.navigate(persona.core.navigation.SettingsRoute)
                    "agents" -> navigator.selectTab(AppTab.Agents)
                    else -> navigator.selectTab(AppTab.Persona)
                }
            }
        }
    }
    val hubLinks: (String) -> Boolean = remember(session, navigator) {
        { link: String ->
            val groups = session.groups.groups.value
            val selected = groups.selectedGroup()
            val route = resolveHubRoute(link, session.hubAddresses(), selected?.namespace,
                session.sessions.sessions.value.map { it.key }, session.assistant.assistant.value, groups)
            when {
                route == null -> false
                route.hubGroupId != null && route.hubGroupId != selected?.id -> {
                    persona.android.MainActivity.pendingHubLink.value = link
                    true
                }
                else -> openResolvedHubPage(route, navigator)
            }
        }
    }
    val forwarding = rememberChatForwarding(session, navigator)
    var picked by remember { mutableStateOf<ChatLinkContext?>(null) }
    val inboxPick: (InboxWebSelection) -> Unit = { selection -> picked = ChatLinkContext("context", selection.title, selection.url, selection.text) }
    // Without a main conversation to send to, a pick can only say where it works.
    val contextNotice = stringResource(R.string.app_link_context_elsewhere)
    val editNotice = stringResource(R.string.app_link_edit_elsewhere)
    val contextElsewhere: (ChatLinkContext) -> Unit = { feedback.notice(contextNotice) }
    val editElsewhere: suspend (ChatLinkContext, String) -> Unit = { _, _ -> feedback.notice(editNotice) }
    CompositionLocalProvider(LocalChatAppLinks provides appLinks, LocalChatHubLinks provides hubLinks,
        LocalInboxWebSelection provides inboxPick.takeIf { handToPersona != null }, LocalChatForwarding provides forwarding) {
        ChatLinkHost(
            onContext = contextElsewhere.takeIf { handToPersona == null },
            onEdit = editElsewhere.takeIf { handToPersona == null },
            onHandToPersona = handToPersona,
        ) { _ ->
            val uri = androidx.compose.ui.platform.LocalUriHandler.current
            val pending = persona.android.MainActivity.pendingCapsuleUrl.value
            LaunchedEffect(pending) {
                pending?.let { url ->
                    uri.openUri(url)
                    if (persona.android.MainActivity.pendingCapsuleUrl.value == url) persona.android.MainActivity.pendingCapsuleUrl.value = null
                }
            }
            content()
        }
    }
    picked?.let { selection ->
        HandToPersonaDialog(selection, onDismiss = { picked = null }) { message ->
            handToPersona?.invoke(message)
            picked = null
        }
    }
}

private fun List<HubGroupInfo>.selectedGroup(): HubGroupInfo? = firstOrNull { it.selected } ?: firstOrNull()

private fun List<HubGroupInfo>.groupForHub(hub: String?): HubGroupInfo? =
    if (hub == null) selectedGroup() else firstOrNull { group -> group.hubs.any { it.hubId == hub } }

/**
 * Opens the app's screen for [link]. Web links use the selected group's [hubs] addresses; an app link's explicit hub
 * selects any paired group in [groups]. The returned route carries that group so the shell can select it before opening.
 */
internal fun openHubPage(link: String, hubs: List<String>, namespace: String?, sessions: List<SessionKey>,
    assistant: AssistantSnapshot, navigator: Navigator, groups: List<HubGroupInfo> = emptyList()): Boolean {
    val route = resolveHubRoute(link, hubs, namespace, sessions, assistant, groups) ?: return false
    return openResolvedHubPage(route, navigator)
}

/** The native route of a hub link; cold-start Router and in-app links share this mapping. */
internal fun resolveHubRoute(link: String, hubs: List<String>, namespace: String?, sessions: List<SessionKey>,
    assistant: AssistantSnapshot, groups: List<HubGroupInfo> = emptyList()): ResolvedRoute? {
    val selectedId = groups.selectedGroup()?.id
    val profiles = if (groups.isEmpty()) listOf(HubLinks.Hub("", hubs)) else groups.flatMap { group ->
        group.hubs.map { member -> HubLinks.Hub(member.hubId, if (group.id == selectedId) hubs else emptyList()) }
    }
    val result = HubLinks.parse(link, profiles) as? HubLinks.Result.Page ?: return null
    return resolveHubPage(result.page, namespace, sessions, assistant, groups, result.hub?.takeIf { it.isNotBlank() })
}

/** The native route of an already parsed page; message-link and web-link navigation share this mapping. */
internal fun resolveHubPage(page: HubPage, namespace: String?, sessions: List<SessionKey>,
    assistant: AssistantSnapshot, groups: List<HubGroupInfo> = emptyList(), hub: String? = null): ResolvedRoute? {
    if (hub != null && groups.isEmpty()) return null
    val target = groups.groupForHub(hub)
    if (groups.isNotEmpty() && target == null) return null
    // The hub speaks its own machine ids; merged lists carry the target group's suffix (GroupKeys).
    fun machine(id: String) = GroupKeys.machine(id, target?.namespace ?: namespace)
    fun route(tab: AppTab, key: androidx.navigation3.runtime.NavKey, focus: Boolean = false) =
        ResolvedRoute(tab, key, focusComposer = focus, hubGroupId = target?.id)
    return when (page) {
        is HubPage.Agent -> {
            val machineId = machine(page.machineId)
            // The web page also takes the first characters of a session id (#2464): the one session they name.
            val sessionId = sessions.filter { it.machineId == machineId && it.sessionId.startsWith(page.sessionId) }
                .let { keys -> keys.firstOrNull { it.sessionId == page.sessionId } ?: keys.singleOrNull() }?.sessionId ?: page.sessionId
            page.itemId?.let { MessageJumps.request(machineId, sessionId, it) }
            route(AppTab.Agents, if (page.view == "term") TerminalRoute(machineId, sessionId) else ChatRoute(machineId, sessionId))
        }
        is HubPage.Conversation -> {
            val main = assistant.mainSession
            val id = page.sessionId
            val linkedMachine = page.machineId
            val key = when {
                id == null -> main
                // An explicit machine resolves another manager's conversation even when it is not in the current list.
                linkedMachine != null -> SessionKey(machine(linkedMachine), id)
                // Otherwise retain the current assistant's resolution and its not-listed-yet fallback.
                else -> assistant.conversations.map { it.key }.firstOrNull { it.sessionId == id }
                    ?: main?.let { SessionKey(it.machineId, id) } ?: return null
            }
            if (id != null && page.managerId != null) {
                AssistantManagerChoice.noteItem(listOf("assistant", "conversations", id), page.managerId)
            }
            if (key == null) route(AppTab.Persona, AssistantRoute)
            else {
                page.itemId?.let { MessageJumps.request(key.machineId, key.sessionId, it) }
                route(AppTab.Persona, ChatRoute(key.machineId, key.sessionId, ConversationKind.Assistant))
            }
        }
        HubPage.NewConversation -> route(AppTab.Persona, AssistantRoute, focus = true)
        is HubPage.Knowledge -> {
            val kind = page.kind?.let { KnowledgeKind.fromWire(it) ?: return null }
            route(AppTab.Persona, kind?.let { KnowledgeKindRoute(it) } ?: KnowledgeRoute)
        }
        is HubPage.KnowledgeEntry -> {
            val kind = KnowledgeKind.fromWire(page.kind) ?: return null
            route(AppTab.Persona, KnowledgeDetailRoute(kind, page.id))
        }
        is HubPage.Screen -> {
            val key = page.key.let {
                // Named copying keeps the parsed range and all route defaults; manager-bearing screens navigate unchanged.
                if (it is MachineDetailRoute) it.copy(machineId = machine(it.machineId)) else it
            }
            val tab = when (key) {
                AgentsRoute, TodosRoute, is TodoDetailRoute -> AppTab.Agents
                else -> AppTab.Persona
            }
            route(tab, key)
        }
    }
}

/** Message and web links keep their existing direct-navigation behavior; cold starts apply the same route to the stacks. */
private fun openResolvedHubPage(route: ResolvedRoute, navigator: Navigator): Boolean {
    if (route.focusComposer) {
        val chat = route.key as? ChatRoute
        if (chat != null) persona.feature.chat.ChatComposerFocus.request(SessionKey(chat.machineId, chat.sessionId))
        else persona.feature.assistant.AssistantComposerFocus.request()
    }
    if (route.key == AssistantRoute) navigator.selectTab(route.tab) else navigator.navigate(route.key)
    return true
}

internal fun openHubPage(page: HubPage, namespace: String?, sessions: List<SessionKey>,
    assistant: AssistantSnapshot, navigator: Navigator, groups: List<HubGroupInfo> = emptyList(), hub: String? = null): Boolean =
    resolveHubPage(page, namespace, sessions, assistant, groups, hub)?.let { openResolvedHubPage(it, navigator) } == true

/**
 * What chats need for Forward (#3901): the sessions and Persona's conversations the app already holds, each group's Recent,
 * and how a target opens: an Agent's chat in Agents, a Persona conversation in Persona's page, Persona's new conversation
 * with its composer focused, or the new-agent page. Forwards stay on their hub group (Persona's page shows the selected one).
 */
@Composable
private fun rememberChatForwarding(session: ShellSession, navigator: Navigator): ChatForwarding {
    val resources = androidx.compose.ui.platform.LocalResources.current
    val wiring = chatWiringSource()
    return remember(session, navigator, resources, wiring) {
        ChatForwarding(
            agents = session.sessions.sessions, machines = session.sessions.machines, assistant = session.assistant.assistant,
            personaGroupOf = { key ->
                GroupKeys.namespaceOf(key.machineId) == session.groups.groups.value.firstOrNull { it.selected }?.namespace
            },
            conversationTitle = { it.personaConversationTitle(resources) },
            memory = { key -> wiring.bindingsFor(key).value?.forwardMemory },
            openChat = { key, assistant ->
                if (assistant) {
                    navigator.selectTab(AppTab.Persona)
                    navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Assistant))
                } else navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Agent))
            },
            openPersonaHome = {
                persona.feature.assistant.AssistantComposerFocus.request()
                navigator.selectTab(AppTab.Persona)
            },
            openNewAgent = { context -> navigator.navigate(persona.core.navigation.NewAgentRoute(context = context)) },
        )
    }
}
