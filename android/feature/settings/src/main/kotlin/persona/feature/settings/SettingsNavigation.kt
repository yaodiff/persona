package persona.feature.settings

import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import persona.core.navigation.SettingsPage

/** Stable page names shared by standalone settings and the navigation shell. */
fun SettingsPage.toInitialPage(): String = when (this) {
    SettingsPage.Diagnostics -> "diag"
    SettingsPage.Readiness -> "ready"
    SettingsPage.Notifications -> "notify"
    SettingsPage.PhoneData -> "phone"
    SettingsPage.Appearance -> "style"
    SettingsPage.Devices -> "devices"
    SettingsPage.About -> "about"
    SettingsPage.Pairing -> "pair"
    SettingsPage.ClaudeCode -> "claude"
    SettingsPage.ModelConfig -> "models"
    SettingsPage.Hubs -> "hubs"
    SettingsPage.Persona -> "persona"
    SettingsPage.Extensions -> "plugins"
    SettingsPage.Mail -> "mail"
    SettingsPage.Feishu -> "feishu"
    SettingsPage.Channels -> "channels"
    SettingsPage.HubUpdate -> "hub-update"
    SettingsPage.Translation -> "translate"
    SettingsPage.Triage -> "triage"
    SettingsPage.Capsule -> "capsule"
    SettingsPage.Language -> "language"
    SettingsPage.Codex -> "codex"
    SettingsPage.Agy -> "agy"
    SettingsPage.Providers -> "providers"
    SettingsPage.Storage -> "storage"
    SettingsPage.Desktops -> "desktops"
    SettingsPage.Managers -> "managers"
    SettingsPage.FeishuBot -> "feishu-bot"
    SettingsPage.Sent -> "connectors/sent"
    SettingsPage.Connectors -> "connectors"
    SettingsPage.AgentSettings -> "agent-settings"
    SettingsPage.NotificationBackground -> "notification-background"
    SettingsPage.KnowledgeSelection -> "knowledge-selection"
    SettingsPage.StandingAuthorizations -> "standing-authorizations"
    SettingsPage.DigitalAssistant -> "digital-assistant"
}

/** The legacy ledger page opens at its new location; the root and unknown names have no destination. */
fun settingsPageFromInitialPage(initialPage: String): SettingsPage? =
    if (initialPage == "sent") SettingsPage.Sent
    else SettingsPage.entries.firstOrNull { it.toInitialPage() == initialPage }

/**
 * The pages of Settings' first group, the current assistant's settings (docs/assistant/managers.md "Clients"), and the
 * pages they open: what a switch of assistant closes, so no page shows one assistant's settings once another is current.
 */
private val assistantPages = setOf(SettingsPage.Providers, SettingsPage.ModelConfig, SettingsPage.Triage, SettingsPage.Connectors,
    SettingsPage.Mail, SettingsPage.Feishu, SettingsPage.FeishuBot, SettingsPage.Sent, SettingsPage.Extensions, SettingsPage.Channels,
    SettingsPage.ClaudeCode, SettingsPage.Codex, SettingsPage.Agy, SettingsPage.AgentSettings, SettingsPage.KnowledgeSelection, SettingsPage.StandingAuthorizations)

/** Whether [page] (an in-place page key, [SettingsPage.toInitialPage]) is one of the current assistant's settings pages. */
internal fun isAssistantSettingsPage(page: String): Boolean =
    page.startsWith("connector/") || page == "mail-add" || settingsPageFromInitialPage(page)?.let { it in assistantPages } == true

/** Whether the settings destination [key] is one of the current assistant's (its pages and what they open). */
fun isAssistantSettingsDestination(key: androidx.navigation3.runtime.NavKey): Boolean = when (key) {
    is persona.core.navigation.SettingsPageRoute -> key.page in assistantPages
    persona.core.navigation.AssistantSettingsRoute, is persona.core.navigation.ModelPurposeRoute, is persona.core.navigation.ModelProviderRoute,
    is persona.core.navigation.ExtensionRoute, is persona.core.navigation.ExtensionUiRoute, persona.core.navigation.ExtensionMarketRoute,
    is persona.core.navigation.ExtensionCatalogRoute, persona.core.navigation.ExtensionMarketsRoute, is persona.core.navigation.ConnectorRoute,
    is persona.core.navigation.ChannelRoute -> true
    else -> false
}

/**
 * What a switch of assistant does to a back stack (the Settings sheet's, and each tab's, which a link or a readiness card
 * may have pushed settings onto): everything from its first page of the current assistant's settings on is closed, so no
 * page shows one assistant's settings, or sends what it holds, once another is current. Its root stays.
 * Whether anything was closed.
 */
fun closeAssistantSettings(stack: MutableList<androidx.navigation3.runtime.NavKey>): Boolean {
    val first = stack.indexOfFirst(::isAssistantSettingsDestination)
    if (first <= 0) return false
    while (stack.size > first) stack.removeAt(stack.lastIndex)
    return true
}

/**
 * [closeAssistantSettings] on each of [stacks] whenever [assistantId] (the current assistant) changes; not when first
 * shown. The shell's: the Settings sheet's stack and each tab's.
 */
@androidx.compose.runtime.Composable
fun CloseAssistantSettingsOnSwitch(assistantId: String, stacks: () -> List<MutableList<androidx.navigation3.runtime.NavKey>>) {
    var shown by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf(assistantId) }
    androidx.compose.runtime.LaunchedEffect(assistantId) {
        if (shown == assistantId) return@LaunchedEffect
        shown = assistantId
        stacks().forEach { closeAssistantSettings(it) }
    }
}
