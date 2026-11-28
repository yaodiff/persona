package persona.core.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.json.Json
import org.junit.Assert.*
import org.junit.Test

class RouterTest {
    private val router = Router { _, session ->
        when (session) {
            "assistant" -> ConversationKind.Assistant
            "agent" -> ConversationKind.Agent
            else -> null
        }
    }
    private val assistant = ChatRoute("machine", "assistant", ConversationKind.Assistant)
    private val agent = ChatRoute("machine", "agent", ConversationKind.Agent)

    @Test fun allEntrancesResolveToTheSameTabAndPage() {
        val cases = listOf(
            Triple(RouteInput.DeepLink("persona://open?machine=machine&session=assistant"), AppTab.Persona, assistant),
            Triple(RouteInput.DeepLink("persona://open?machine=machine&session=agent"), AppTab.Agents, agent),
            Triple(RouteInput.Notification("machine/assistant"), AppTab.Persona, assistant),
            Triple(RouteInput.Notification("machine/agent"), AppTab.Agents, agent),
            Triple(RouteInput.Share(assistant.copy(kind = null)), AppTab.Persona, assistant),
            Triple(RouteInput.Share(agent), AppTab.Agents, agent),
            Triple(RouteInput.Internal(assistant), AppTab.Persona, assistant),
            Triple(RouteInput.Internal(agent), AppTab.Agents, agent),
            Triple(RouteInput.Internal(InboxItemRoute("item")), AppTab.Persona, InboxItemRoute("item")),
            Triple(RouteInput.DeepLink("persona://open?target=inbox&item=item"), AppTab.Persona, InboxItemRoute("item")),
            Triple(RouteInput.DeepLink("persona://open?target=lark&chat=chat"), AppTab.Persona, LarkChatRoute("chat")),
            Triple(RouteInput.Internal(LarkChatRoute("chat")), AppTab.Persona, LarkChatRoute("chat")),
            Triple(RouteInput.Notification("machine/assistant", requestId = "request"), AppTab.Persona, InteractionRoute(assistant, "request")),
            Triple(RouteInput.DeepLink("persona://open?target=request&machine=machine&session=agent&request=request"), AppTab.Agents, InteractionRoute(agent, "request")),
            Triple(RouteInput.Internal(ConnectionSettingsRoute), AppTab.Persona, SettingsPageRoute(SettingsPage.Diagnostics)),
            Triple(RouteInput.DeepLink("persona://open?target=settings&page=notifications"), AppTab.Persona, SettingsPageRoute(SettingsPage.Notifications)),
            Triple(RouteInput.DeepLink("persona://open?machine=machine&session=a%2Fb%2Bc&kind=assistant"), AppTab.Persona,
                ChatRoute("machine", "a/b+c", ConversationKind.Assistant)),
        )
        cases.forEach { (input, tab, key) -> assertRoute(router.parse(input), tab, key) }
        AppTab.entries.forEach { tab ->
            assertRoute(router.parse(RouteInput.Internal(tabRoot(tab))), tab, tabRoot(tab))
            assertRoute(router.parseDebug(mapOf("debug.openTab" to tab.name)), tab, tabRoot(tab))
        }
        SettingsPage.entries.forEach { page ->
            assertRoute(router.parse(RouteInput.Internal(SettingsPageRoute(page))), AppTab.Persona, SettingsPageRoute(page))
        }
        val pairLink = "persona://pair?url=https%3A%2F%2Fexample.com&token=example"
        val pairing = router.parse(RouteInput.DeepLink(pairLink))
        assertRoute(pairing, AppTab.Persona, PairingRoute)
        assertEquals(pairLink, pairing?.pairingLink)
        assertRoute(router.parseDebug(mapOf("debug.openPairing" to pairLink)), AppTab.Persona, PairingRoute)
        assertRoute(router.parseDebug(mapOf("debug.openPairing" to "true")), AppTab.Persona, PairingRoute)
        assertRoute(router.parseDebug(mapOf("debug.openDiagnostics" to "true")), AppTab.Persona, SettingsPageRoute(SettingsPage.Diagnostics))
        assertRoute(router.parseDebug(mapOf("debug.openSession" to "machine/assistant")), AppTab.Persona, assistant)
        assertRoute(router.parseDebug(mapOf("debug.openSession" to "machine/agent")), AppTab.Agents, agent)
        assertRoute(router.parseDebug(mapOf("debug.openRequest" to "request", "debug.session" to "machine/agent")),
            AppTab.Agents, InteractionRoute(agent, "request"))
        assertRoute(router.parseDebug(mapOf("debug.openSession" to "machine/unknown", "debug.conversationKind" to "assistant")),
            AppTab.Persona, ChatRoute("machine", "unknown", ConversationKind.Assistant))
        listOf(
            "https://open?machine=machine&session=agent", "persona://open/path?machine=machine&session=agent",
            "persona://open?machine=machine&session=", "persona://open?machine=machine&session=agent&session=assistant",
            "persona://open?machine=machine&session=%ZZ", "persona://open?machine=machine&session=agent&kind=unknown",
            "persona://open?target=settings&page=unknown", "persona://pair.example.com", "persona://open#fragment",
        ).forEach { assertNull(it, router.parse(RouteInput.DeepLink(it))) }
        assertNull(router.parse(RouteInput.Notification("machine/")))
        assertNull(router.parse(RouteInput.Notification("machine/unknown")))
        assertNull(router.parseDebug(mapOf("debug.openTab" to "inbox", "debug.openDiagnostics" to "true")))
        assertNull(router.parseDebug(mapOf("debug.openRequest" to "request")))
    }

    @Test fun canonicalAppLinksResolveColdAndRefuseUnsafeRoutes() {
        assertRoute(router.parse(RouteInput.DeepLink("persona://open/todos/t-19")), AppTab.Agents, TodoDetailRoute("t-19"))
        for (link in listOf(
            "persona://open//evil.example/x",
            "persona://open:8080/s/machine/agent",
            "persona://open?route=%2F%2Fevil.example%2Fx",
            "persona://open/%5Cevil",
        )) assertNull(link, router.parse(RouteInput.DeepLink(link)))
    }

    @Test fun personaDeepLinksKeepCredentialsTransient() {
        val pair = "persona://pair?url=https%3A%2F%2Fhub.example.com&token=fixture"
        val route = router.parse(RouteInput.DeepLink(pair))
        assertRoute(route, AppTab.Persona, PairingRoute)
        assertEquals(pair, route?.pairingLink)
        assertRoute(router.parseDebug(mapOf("debug.openPairing" to pair)), AppTab.Persona, PairingRoute)
        for (suffix in listOf("open/path?machine=machine&session=agent", "open?machine=machine&session=",
            "open?machine=machine&session=agent&session=assistant", "open?machine=machine&session=%ZZ", "open#fragment")) {
            assertNull(router.parse(RouteInput.DeepLink("persona://$suffix")))
        }
    }

    @Test fun askPersonaFocusesTheComposerOfThePersonaTabOnly() {
        val ask = router.parse(RouteInput.DeepLink(AskPersonaLink))
        assertRoute(ask, AppTab.Persona, AssistantRoute)
        assertTrue(ask!!.focusComposer)
        assertFalse(router.parse(RouteInput.DeepLink("persona://open?target=tab&tab=persona"))!!.focusComposer)
        // Elsewhere, or with another value, the parameter is ignored rather than refusing the link.
        val agents = router.parse(RouteInput.DeepLink("persona://open?target=tab&tab=agents&focus=composer"))
        assertRoute(agents, AppTab.Agents, AgentsRoute)
        assertFalse(agents!!.focusComposer)
        assertFalse(router.parse(RouteInput.DeepLink("persona://open?target=tab&tab=persona&focus=search"))!!.focusComposer)
        assertNull(router.parse(RouteInput.DeepLink("$AskPersonaLink&focus=composer")))
        val chatAsk = router.parse(RouteInput.DeepLink("persona://open?target=chat&machine=m1&session=s1&kind=assistant&focus=composer"))
        assertEquals(AppTab.Persona, chatAsk?.tab)
        assertEquals(ChatRoute("m1", "s1", ConversationKind.Assistant), chatAsk?.key)
        assertTrue(chatAsk!!.focusComposer)
        val stacks = mapOf(AppTab.Persona to mutableListOf<androidx.navigation3.runtime.NavKey>(AssistantRoute), AppTab.Agents to mutableListOf<androidx.navigation3.runtime.NavKey>(AgentsRoute))
        assertEquals(AppTab.Persona, chatAsk.restore(stacks))
        assertEquals(listOf(AssistantRoute, ChatRoute("m1", "s1", ConversationKind.Assistant)), stacks[AppTab.Persona])
        val newTodo = router.parse(RouteInput.DeepLink(NewTodoLink))
        assertEquals(AppTab.Agents, newTodo!!.tab)
        assertEquals(NewTodoRoute, newTodo.key)
    }

    @Test fun askPersonaLandsOnTheComposerOverPagesPushedOntoThePersonaTab() {
        val stacks = AppTab.entries.associateWith { mutableListOf(tabRoot(it)) }
        // Knowledge and To-dos (feature modules) are pushed onto whichever tab is selected, Persona included.
        stacks.getValue(AppTab.Persona).add(PushedPage)
        assertEquals(AppTab.Persona, router.parse(RouteInput.DeepLink(AskPersonaLink))!!.restore(stacks))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun restoredIdentityReusesExistingEntriesAndKeepsSourcesSerializable() {
        val stacks = AppTab.entries.associateWith { mutableListOf(tabRoot(it)) }
        val sources = listOf(RouteSource.InboxItem("item"), RouteSource.LarkConversation("chat"),
            RouteSource.Interaction("machine", "request"))
        val fromInbox = assistant.copy(source = sources.first())
        assertEquals(fromInbox, Json.decodeFromString<ChatRoute>(Json.encodeToString(fromInbox)))
        val old = Json.decodeFromString<ChatRoute>("""{"machineId":"machine","sessionId":"assistant"}""")
        assertNull(old.kind)
        stacks.getValue(AppTab.Agents).add(old)
        router.parse(RouteInput.Internal(fromInbox))!!.restore(stacks)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        stacks.getValue(AppTab.Persona).add(ChatRoute("machine", "other", ConversationKind.Assistant))
        sources.forEach { source ->
            val route = router.parse(RouteInput.Internal(assistant.copy(source = source)))!!
            assertEquals(AppTab.Persona, route.restore(stacks))
            route.restore(stacks)
            assertEquals(listOf(AssistantRoute, fromInbox), stacks.getValue(AppTab.Persona))
        }
        listOf<NavKey>(InboxItemRoute("item"), LarkChatRoute("chat"), InteractionRoute(agent, "request"),
            SettingsPageRoute(SettingsPage.Diagnostics), PairingRoute).forEach { key ->
            val route = router.parse(RouteInput.Internal(key))!!
            route.restore(stacks)
            route.restore(stacks)
            assertEquals(1, stacks.getValue(route.tab).count { it == key })
            assertEquals(key, stacks.getValue(route.tab).last())
        }
        val interaction = InteractionRoute(fromInbox, "request")
        assertEquals(interaction, Json.decodeFromString<InteractionRoute>(Json.encodeToString(interaction)))
    }

    @Test fun onlyPersonaAndAgentsArePrimaryTabs() {
        assertEquals(listOf(AppTab.Persona, AppTab.Agents), AppTab.entries.toList())
        assertEquals(listOf(AssistantRoute, AgentsRoute), AppTab.entries.map(::tabRoot))
    }

    @Test fun retiredTabLinksAndDebugLaunchesOpenSecondaryPagesUnderPersona() {
        for ((name, key) in listOf("inbox" to InboxRoute, "settings" to SettingsRoute)) {
            val external = router.parse(RouteInput.DeepLink("persona://open?target=tab&tab=$name"))!!
            val debug = router.parseDebug(mapOf("debug.openTab" to name))!!
            for (route in listOf(external, debug)) {
                assertRoute(route, AppTab.Persona, key)
                val stacks = AppTab.entries.associateWith { mutableListOf(tabRoot(it)) }
                route.restore(stacks)
                assertEquals(listOf(AssistantRoute, key), stacks.getValue(AppTab.Persona))
                assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
            }
        }
    }

    @Test fun inboxAndSettingsLinksRestoreParentsWithoutChangingExistingEntries() {
        val cases = listOf(
            "persona://open?target=inbox&item=item" to listOf(InboxRoute, InboxItemRoute("item")),
            "persona://open?target=lark&chat=chat" to listOf(InboxRoute, LarkChatRoute("chat")),
            "persona://open?target=settings&page=about" to listOf(SettingsRoute, SettingsPageRoute(SettingsPage.About)),
            "persona://pair?url=https%3A%2F%2Fexample.com&token=fixture" to listOf(SettingsRoute, PairingRoute),
        )
        for ((link, pages) in cases) {
            val origin = PushedPage
            val stacks = AppTab.entries.associateWith { mutableListOf(tabRoot(it)) }
            stacks.getValue(AppTab.Persona).add(origin)
            val route = router.parse(RouteInput.DeepLink(link))!!
            assertEquals(AppTab.Persona, route.restore(stacks))
            route.restore(stacks)
            assertEquals(listOf(AssistantRoute, origin) + pages, stacks.getValue(AppTab.Persona))
            stacks.getValue(AppTab.Persona).removeAt(stacks.getValue(AppTab.Persona).lastIndex)
            assertEquals(pages.first(), stacks.getValue(AppTab.Persona).last())
        }
    }

    @Test fun secondaryLinksDoNotRemoveTheSamePagesFromTheOtherTab() {
        for (page in listOf(SettingsRoute, SettingsPageRoute(SettingsPage.About), InboxRoute, InboxItemRoute("item"))) {
            val stacks = AppTab.entries.associateWith { mutableListOf(tabRoot(it)) }
            stacks.getValue(AppTab.Agents).add(page)
            restoreSecondaryParents(stacks.getValue(AppTab.Agents))
            val previous = stacks.getValue(AppTab.Agents).toList()
            router.parse(RouteInput.Internal(page))!!.restore(stacks)
            assertEquals(previous, stacks.getValue(AppTab.Agents))
            assertEquals(page, stacks.getValue(AppTab.Persona).last())
        }
    }

    private fun assertRoute(actual: ResolvedRoute?, tab: AppTab, key: NavKey) {
        assertNotNull(actual)
        assertEquals(tab, actual!!.tab)
        assertEquals(key, actual.key)
    }
}

private data object PushedPage : NavKey
