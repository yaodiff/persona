package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.navigation.*

private data class HomeEntry(
    val key: NavKey?,
    val title: Int,
    val icon: PhIcon,
    val subtitle: Int? = null,
    val aliases: Int? = null,
    val value: String? = null,
    val itemKey: String = key.toString(),
    val onClick: (() -> Unit)? = null,
    val subtitleText: String? = null,
)

/** The active modules' Settings pages (#13847), at the end of the assistant's group; each opens its module's page. */
private fun moduleEntries(pages: List<persona.core.moduleapi.ModuleSettingsPage>): List<HomeEntry> =
    pages.map { HomeEntry(it.route, it.title, it.icon) }

/** Navigation only. The first scope uses the same current-assistant roster and bound services as before. */
@Composable
internal fun SettingsHomePage(
    services: SettingsServices, query: String, onQuery: (String) -> Unit,
    extensionsAvailable: Boolean, inboxSources: Boolean, extensionSummary: String?, channelsAvailable: Boolean, channelList: List<HubChannel>?,
    knowledgeSelectionAvailable: Boolean,
    connection: ConnectionState, translationValue: String?, hubUpdateStatus: HubUpdateStatus?, updateValue: String?,
    selected: NavKey?, onOpen: (SettingsPage) -> Unit, onNavigate: ((NavKey) -> Unit)?, onClose: (() -> Unit)?,
    onDemo: (() -> Unit)?, onExitDemo: (() -> Unit)?,
    footer: @Composable () -> Unit,
) {
    val context = LocalContext.current
    val resources = context.resources
    val assistant = currentAssistant()
    val styleStore = LocalAppStyleStore.current
    var defaultDigitalAssistant by remember(context) { mutableStateOf(DigitalAssistantSettings.isDefaultAssistant(context)) }
    val digitalAssistantChoice = services.digitalAssistantModel?.choice?.collectAsState()?.value
    val digitalAssistantSubtitle = digitalAssistantChoice?.takeUnless { it.isDefault }?.fullLabel
        ?.takeIf(String::isNotBlank)
        ?.let { stringResource(R.string.settings_digital_assistant_model_summary, it) }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        defaultDigitalAssistant = DigitalAssistantSettings.isDefaultAssistant(context)
    }
    fun entry(page: SettingsPage, title: Int, icon: PhIcon, subtitle: Int? = null, aliases: Int? = null, value: String? = null) =
        HomeEntry(SettingsPageRoute(page), title, icon, subtitle, aliases, value)
    val assistantEntries = listOfNotNull(
        entry(SettingsPage.ModelConfig, R.string.settings_home_models, PersonaIcons.memory, R.string.settings_home_models_detail, R.string.settings_search_models_aliases),
        onNavigate?.let { HomeEntry(AssistantSettingsRoute, R.string.settings_row_assistant, PersonaIcons.ai, R.string.settings_row_assistant_subtitle) },
        entry(SettingsPage.StandingAuthorizations, R.string.settings_standing_title, PersonaIcons.privacy, aliases = R.string.settings_standing_search_aliases),
        entry(SettingsPage.Connectors, R.string.settings_section_connectors, PersonaIcons.link, R.string.settings_home_connectors_detail, R.string.settings_search_connectors_aliases),
        // Inbox Sources (#5308): the same pages as the Inbox menu, every source, once the hub reports inbox_sources.
        if (!inboxSources) null else HomeEntry(
            ExtensionUiRoute(SUBSCRIPTIONS_UI, "pages", stringResource(R.string.settings_inbox_sources)),
            R.string.settings_inbox_sources, PersonaIcons.inProgress, R.string.settings_inbox_sources_detail,
            R.string.settings_search_inbox_sources_aliases),
        if (!extensionsAvailable) null else entry(SettingsPage.Extensions, R.string.settings_section_extensions, PersonaIcons.skills, R.string.settings_home_extensions_detail, R.string.settings_search_extensions_aliases, extensionSummary),
        if (!knowledgeSelectionAvailable) null else entry(SettingsPage.KnowledgeSelection, R.string.settings_knowledge_selection_title, PersonaIcons.knowledge,
            R.string.settings_knowledge_selection_subtitle, R.string.settings_knowledge_selection_search_aliases),
        if (!channelsAvailable) null else entry(SettingsPage.Channels, R.string.settings_src_channels_title, PersonaIcons.channels, R.string.settings_home_channels_detail),
        entry(SettingsPage.AgentSettings, R.string.settings_home_agents, PersonaIcons.agents, R.string.settings_home_agents_detail, R.string.settings_search_agents_aliases),
    ) + moduleEntries(persona.core.moduleapi.LocalModuleSettingsPages.current.takeIf { onNavigate != null }.orEmpty())
    val deviceEntries = listOfNotNull(
        entry(SettingsPage.Appearance, R.string.settings_style_title, PersonaIcons.options, aliases = R.string.settings_search_appearance_aliases, value = PersonaTheme.style.label),
        entry(SettingsPage.Language, R.string.settings_language_title, PersonaIcons.servedPage),
        entry(SettingsPage.NotificationBackground, R.string.settings_notification_background_title, PersonaIcons.notifications,
            R.string.settings_home_notifications_detail, R.string.settings_search_notifications_aliases),
        HomeEntry(
            key = SettingsPageRoute(SettingsPage.DigitalAssistant),
            title = R.string.settings_digital_assistant_title,
            icon = PersonaIcons.ai,
            subtitle = R.string.settings_digital_assistant_detail,
            aliases = R.string.settings_search_digital_assistant_aliases,
            value = stringResource(
                if (defaultDigitalAssistant) R.string.settings_digital_assistant_active
                else R.string.settings_digital_assistant_inactive,
            ),
            itemKey = "device-digital-assistant",
            subtitleText = digitalAssistantSubtitle,
        ),
        entry(SettingsPage.Translation, R.string.settings_translation_title, PersonaIcons.translate, R.string.settings_row_translation_subtitle, value = translationValue),
        entry(SettingsPage.Storage, R.string.settings_storage_title, PersonaIcons.archive),
        entry(SettingsPage.About, R.string.settings_home_about, PersonaIcons.info, R.string.settings_home_about_detail, R.string.settings_search_about_aliases, updateValue),
    )
    val hubEntries = listOfNotNull(
        entry(SettingsPage.Diagnostics, R.string.settings_home_connection, PersonaIcons.link, R.string.settings_home_connection_detail, R.string.settings_search_connection_aliases,
            if (services.demo) stringResource(R.string.settings_demo_mode) else connection.label()),
        services.hubs?.let { entry(SettingsPage.Hubs, R.string.settings_row_hubs, PersonaIcons.server, R.string.settings_row_hubs_subtitle) },
        services.hubUpdates?.takeUnless { services.demo }?.let { entry(SettingsPage.HubUpdate, R.string.settings_row_hub_update, PersonaIcons.retry,
            R.string.settings_row_hub_update_subtitle, value = HubUpdateText.summary(hubUpdateStatus)) },
        services.managers?.takeUnless { services.demo }?.let { entry(SettingsPage.Managers, R.string.settings_managers_title, PersonaIcons.person, R.string.settings_managers_hint) },
        entry(SettingsPage.Persona, R.string.settings_avatar_title, PersonaIcons.persona,
            value = if (LocalPersonaPack.current != null) LocalPersonaName.current else LocalPersonaDesign.current.title),
        entry(SettingsPage.Devices, R.string.settings_devices_title, PersonaIcons.thisDevice, R.string.settings_row_devices_subtitle),
    )
    fun visible(rows: List<HomeEntry>): List<HomeEntry> {
        if (query.isBlank()) return rows
        return rows.filter {
            val former = (it.key as? SettingsPageRoute)?.page?.let { page -> settingsFormerEntryAliases(resources, page) }.orEmpty()
            settingsSearchMatches(query, resources.getString(it.title), it.subtitle?.let(resources::getString),
                listOf(it.aliases?.let(resources::getString).orEmpty(), former).joinToString(" "))
        }
    }
    val a = visible(assistantEntries); val d = visible(deviceEntries); val h = visible(hubEntries)
    val nativeVisible = styleStore != null && settingsSearchMatches(query, stringResource(R.string.settings_update_v7_native_title),
        stringResource(R.string.settings_update_v7_native_subtitle))
    JListPage(title = stringResource(R.string.settings_title), actions = {
        onClose?.let { JIconButton(PersonaIcons.close, stringResource(persona.core.designsystem.R.string.designsystem_close), it, raised = true) }
    }) {
        item("settings-search") { JSearchField(query, onQuery, stringResource(R.string.settings_search_placeholder), Modifier.fillMaxWidth().testTag("settings-search")) }
        if (services.demo && query.isBlank()) {
            item { JNotice(stringResource(R.string.settings_demo_notice), title = stringResource(R.string.settings_demo_mode)) }
            onExitDemo?.let { exit -> item { JButton(stringResource(R.string.settings_demo_connect), exit, Modifier.fillMaxWidth().padding(top = Tokens.Space.m)) } }
        }
        fun group(rows: List<HomeEntry>) = rows.forEachIndexed { index, row ->
            item(row.itemKey) {
                if (row.key == SettingsPageRoute(SettingsPage.Channels)) ChannelsSettingsRow(channelList, GroupPosition.of(index, rows.size),
                    selected == row.key, onClick = { onOpen(SettingsPage.Channels) })
                else JGroupItem(GroupPosition.of(index, rows.size)) {
                JRow(title = stringResource(row.title), leadingIcon = row.icon, subtitle = row.subtitleText ?: row.subtitle?.let { stringResource(it) }, value = row.value,
                    selected = row.key != null && selected == row.key, onClick = {
                        when {
                            row.onClick != null -> row.onClick.invoke()
                            row.key is SettingsPageRoute -> onOpen(row.key.page)
                            row.key != null -> onNavigate?.invoke(row.key)
                        }
                    })
            } }
        }
        if (a.isNotEmpty()) {
            item("assistant-head") {
                val switchLabel = stringResource(R.string.settings_group_assistant_switch)
                AssistantSwitcher(services.managers.takeUnless { services.demo }, LocalAssistantWaiting.current,
                    hubOnline = connection in setOf(ConnectionState.Online, ConnectionState.Updating), onManage = { onOpen(SettingsPage.Managers) },
                    fallback = { JAssistantSettingsHeader(stringResource(persona.core.designsystem.R.string.designsystem_brand_persona), null, false,
                        switchLabel, onSwitch = null, modifier = Modifier.testTag("settings-assistant-head")) },
                    head = { current, isDefault, _, toggle -> JAssistantSettingsHeader(stringResource(R.string.settings_group_assistant, current.shownName), current, isDefault,
                        switchLabel, onSwitch = toggle) })
            }
            group(a)
            val named = assistant.manager?.takeIf { services.managers != null && !services.demo && assistant.roster?.installed?.isNotEmpty() == true }
            if (named != null && query.isBlank()) item("assistant-footer") {
                SettingsCaption(stringResource(R.string.settings_group_assistant_footer, named.shownName), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s))
            }
        }
        if (d.isNotEmpty() || nativeVisible) {
            item("device-head") { JSectionHeader(stringResource(R.string.settings_group_device)) }
            group(d)
            if (nativeVisible) item("native-inbox-chats") { JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                val native by styleStore!!.nativeInboxChats.collectAsState()
                JSwitchRow(stringResource(R.string.settings_update_v7_native_title), native, styleStore::setNativeInboxChats,
                    subtitle = stringResource(R.string.settings_update_v7_native_subtitle))
            } }
        }
        if (h.isNotEmpty()) {
            item("hub-head") { JSectionHeader(stringResource(R.string.settings_group_hub)) }
            group(h)
            if (query.isBlank()) item("hub-footer") { SettingsCaption(stringResource(R.string.settings_group_hub_footer), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
        }
        if (a.isEmpty() && d.isEmpty() && h.isEmpty() && !nativeVisible) item("settings-search-empty") {
            JEmptyState(stringResource(R.string.settings_search_empty), stringResource(R.string.settings_search_empty_detail), icon = PersonaIcons.search)
        }
        if (query.isBlank()) item { footer() }
    }
}
