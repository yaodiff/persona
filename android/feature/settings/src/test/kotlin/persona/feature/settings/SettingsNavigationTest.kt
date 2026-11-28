package persona.feature.settings

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AssistantRoute
import persona.core.navigation.AssistantSettingsRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConnectorRoute
import persona.core.navigation.ManagerRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsRoute
import org.junit.Assert.*
import org.junit.Test

class SettingsNavigationTest {
    @Test fun everyDestinationKeepsItsExistingPageNameAndRoundTrips() {
        val pages = mapOf(
            SettingsPage.Diagnostics to "diag",
            SettingsPage.Readiness to "ready",
            SettingsPage.Notifications to "notify",
            SettingsPage.PhoneData to "phone",
            SettingsPage.Appearance to "style",
            SettingsPage.Devices to "devices",
            SettingsPage.About to "about",
            SettingsPage.Pairing to "pair",
            SettingsPage.ClaudeCode to "claude",
            SettingsPage.ModelConfig to "models",
            SettingsPage.Hubs to "hubs",
            SettingsPage.Persona to "persona",
            SettingsPage.Extensions to "plugins",
            SettingsPage.Mail to "mail",
            SettingsPage.Feishu to "feishu",
            SettingsPage.Channels to "channels",
            SettingsPage.HubUpdate to "hub-update",
            SettingsPage.Translation to "translate",
            SettingsPage.Triage to "triage",
            SettingsPage.Capsule to "capsule",
            SettingsPage.Language to "language",
            SettingsPage.Codex to "codex",
            SettingsPage.Agy to "agy",
            SettingsPage.Providers to "providers",
            SettingsPage.Storage to "storage",
            SettingsPage.Desktops to "desktops",
            SettingsPage.Managers to "managers",
            SettingsPage.FeishuBot to "feishu-bot",
            SettingsPage.Sent to "connectors/sent",
            SettingsPage.Connectors to "connectors",
            SettingsPage.AgentSettings to "agent-settings",
            SettingsPage.NotificationBackground to "notification-background",
            SettingsPage.KnowledgeSelection to "knowledge-selection",
            SettingsPage.StandingAuthorizations to "standing-authorizations",
            SettingsPage.DigitalAssistant to "digital-assistant",
        )
        assertEquals(SettingsPage.entries.toSet(), pages.keys)
        assertEquals(pages.size, pages.values.toSet().size)
        pages.forEach { (page, name) ->
            assertEquals(name, page.toInitialPage())
            assertEquals(page, settingsPageFromInitialPage(name))
            assertEquals(page, settingsPageFromInitialPage(page.toInitialPage()))
        }
    }

    @Test fun legacySentPageNamesResolveToTheNestedLedger() {
        assertEquals(SettingsPage.Sent, settingsPageFromInitialPage("sent"))
        assertEquals("connectors/sent", settingsPageFromInitialPage("sent")!!.toInitialPage())
    }

    @Test fun rootAndUnknownNamesHaveNoDetailDestination() {
        listOf("settings", "assistant", "", "unknown", "DIAG").forEach { assertNull(settingsPageFromInitialPage(it)) }
    }

    /** What a switch of assistant closes: the first group's pages and what they open, never this device's or the hub's. */
    @Test fun aSwitchClosesOnlyTheCurrentAssistantsPages() {
        val assistants = listOf(SettingsPage.Providers, SettingsPage.ModelConfig, SettingsPage.Triage, SettingsPage.Connectors,
            SettingsPage.Mail, SettingsPage.Feishu, SettingsPage.FeishuBot, SettingsPage.Sent, SettingsPage.Extensions,
            SettingsPage.Channels, SettingsPage.ClaudeCode, SettingsPage.Codex, SettingsPage.Agy, SettingsPage.AgentSettings, SettingsPage.KnowledgeSelection,
            SettingsPage.StandingAuthorizations)
        SettingsPage.entries.forEach { page ->
            assertEquals(page.name, page in assistants, isAssistantSettingsDestination(SettingsPageRoute(page)))
            assertEquals(page.name, page in assistants, isAssistantSettingsPage(page.toInitialPage()))
        }
        listOf(AssistantSettingsRoute, ModelPurposeRoute("agent.triage", "m-2"), ConnectorRoute("github", "m-2"))
            .forEach { assertTrue("$it", isAssistantSettingsDestination(it)) }
        listOf(SettingsRoute, ManagerRoute("m-2")).forEach { assertFalse("$it", isAssistantSettingsDestination(it)) }
        assertTrue(isAssistantSettingsPage("connector/github"))
        assertTrue(isAssistantSettingsPage("mail-add"))
        assertFalse(isAssistantSettingsPage("settings"))
    }

    /** A switch closes the first group's pages on every stack the shell has, and what they opened; the root stays. */
    @Test fun aSwitchClosesThemOnEveryStack() {
        val sheet = mutableListOf<NavKey>(SettingsRoute, SettingsPageRoute(SettingsPage.Connectors), ConnectorRoute("github", "m-2"))
        assertTrue(closeAssistantSettings(sheet))
        assertEquals(listOf<NavKey>(SettingsRoute), sheet)
        // A tab a link pushed Settings › Mail onto (its add form in place): back to the chat it came from.
        val tab = mutableListOf<NavKey>(AssistantRoute, ChatRoute("server", "s1"), SettingsPageRoute(SettingsPage.Mail))
        assertTrue(closeAssistantSettings(tab))
        assertEquals(listOf<NavKey>(AssistantRoute, ChatRoute("server", "s1")), tab)
        // This device's and the hub's pages stay, and so does a stack's root.
        val device = mutableListOf<NavKey>(SettingsRoute, SettingsPageRoute(SettingsPage.Storage), SettingsPageRoute(SettingsPage.Persona))
        assertFalse(closeAssistantSettings(device))
        assertEquals(3, device.size)
        val root = mutableListOf<NavKey>(SettingsPageRoute(SettingsPage.Providers))
        assertFalse(closeAssistantSettings(root))
        assertEquals(1, root.size)
    }
}
