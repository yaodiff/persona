package persona.feature.settings

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.platform.LocalContext
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.api.*
import persona.core.navigation.Navigator
import persona.core.navigation.SettingsRoute

@EntryPoint
@InstallIn(SingletonComponent::class)
interface SettingsDependencies {
    fun settingsServices(): SettingsServices
    fun pairing(): PairingService
    fun connection(): HubConnection
    fun sessions(): SessionsRepository
    fun updates(): AppUpdates
}

data class SettingsBinding(val identity: String, val deviceSettings: DeviceSettings?, val hubAddress: String?)

data class SettingsServices(
    val pairing: PairingService,
    val connection: HubConnection,
    val sessions: SessionsRepository,
    val updates: AppUpdates,
    val diagnostics: SettingsDiagnostics? = null,
    val appUpdateState: AppUpdateState? = null,
    val appUpdateStore: AppUpdateStore? = null,
    val demo: Boolean = false,
    val deviceSettings: DeviceSettings? = null,
    val hubAddress: String? = null,
    // Must stop background work and clear all data for the old hub before returning.
    val disconnect: suspend () -> Unit = { pairing.unpair() },
    val liveDiagnostics: ConnectionDiagnosticsSource? = connection as? ConnectionDiagnosticsSource,
    val binding: kotlinx.coroutines.flow.StateFlow<SettingsBinding?>? = null,
    /** Manual candidates and route choices; null where the connection cannot change them (demo mode). */
    val endpoints: ConnectionEndpoints? = connection as? ConnectionEndpoints,
    /** Engine choice and Claude Code settings on the paired hub's machines; null in demo mode. */
    val machines: MachineManagement? = null,
    /** Settings › Model configuration on the paired hub; null in demo mode. */
    val models: ModelConfiguration? = null,
    /** "New model provider" (from "Model configuration") on the paired hub; null in demo mode. */
    val providers: ProviderSetup? = null,
    /** Settings › Extensions on the paired hub; null in demo mode. */
    val extensions: ExtensionManagement? = null,
    /** Where the selected hub's extensions draw (nav pages, cards, machine sections), kept by the shell; null without. */
    val extensionSlots: ExtensionSlots? = null,
    /** Settings › Channels on the paired hub; null in demo mode. */
    val channels: ChannelManagement? = null,
    /** Settings › Lark bot (its credentials), opened from its channel's page; null in demo mode. */
    val feishuBot: persona.core.contracts.api.FeishuBotSettings? = null,
    /** The phone's hub groups (settings › hub); null where there is no real pairing (demo mode). */
    val hubs: HubGroups? = null,
    /** Settings › hub › SSH Tunnel and Local Proxy of each paired hub; null in demo mode. */
    val tunnels: HubTunnelSettings? = null,
    /** Pairs one more hub instead of replacing the pairing (settings › hub › "Add hub"). */
    val addHub: PairingService? = null,
    /** Settings › Connectors on the paired hub; null in demo mode. */
    val connectors: ConnectorsManagement? = null,
    /** Settings › Hub version (the hub's version and its updating itself) on the paired hub; null in demo mode. */
    val hubUpdates: HubUpdates? = null,
    /** Settings › Managers (the hub's roster of managers and their install codes) on the paired hub; null in demo mode. */
    val managers: HubManagers? = null,
    /** Settings › Connectors › Records sent as you on the paired hub; null in demo mode. */
    val sent: HubSent? = null,
    /** Settings › Translation, kept on this phone; null in demo mode. */
    val translation: TranslationSettings? = null,
    /** Settings › Session triage on the paired hub (docs/assistant/agent-work.md "Switches"); null in demo mode. */
    val triage: AgentTriage? = null,
    val capsule: FloatingCapsuleSettings? = null,
    /** Local model choice for Android's Default Digital Assistant floating overlay; null where unavailable. */
    val digitalAssistantModel: DigitalAssistantModelSettings? = null,
    /** The last pairing attempt (recorded by the pairing page, shown in "Connection diagnostics"); null in demo mode. */
    val pairingAttempts: PairingAttempts? = null,
    /** The main conversation's steps on "Checklist" (docs/assistant/onboarding.md 4.5); null or unavailable: the phone's items only. */
    val readiness: AssistantReadinessSource? = null,
    /** The desktop apps connected to the selected hub now (its `desktops` event); null in demo mode. */
    val desktops: kotlinx.coroutines.flow.StateFlow<List<persona.core.contracts.HubDesktop>>? = null,
    /** Settings › Storage's deleting of cache files, keeping the ones the data layer has open; null deletes them directly. */
    val clearCacheFiles: ((List<java.io.File>) -> Unit)? = null,
    /** Settings › Knowledge selection on the paired hub; null in demo mode. */
    val knowledgeSelection: KnowledgeSelection? = null,
    /** How the Agents list draws its agents (Settings › Interface style, Glass), kept with the list's other settings; null without a list. */
    val agentsDisplay: persona.core.domain.sort.AgentListDisplaySettings? = null,
    /** Settings › Standing Authorizations on the paired hub; null in demo mode. */
    val standing: StandingAuthorizationsSource? = null,
    /** What each extension may read of personal information (#13500), the default manager's; null in demo mode. */
    val extensionTrust: persona.core.contracts.api.ExtensionTrustSource? = null,
    /** Device-local Developing Persona records; never scoped to or synchronized with a hub. */
    val developing: DevelopingDiagnostics? = null,
)

fun EntryProviderScope<NavKey>.settingsEntries(navigator: Navigator, services: SettingsServices? = null) {
    entry<SettingsRoute> { SettingsScreen(services ?: injectedSettingsServices()) }
}

@Composable
private fun injectedSettingsServices(): SettingsServices {
    val context = LocalContext.current.applicationContext
    return remember(context) {
        val entry = EntryPointAccessors.fromApplication(context, SettingsDependencies::class.java)
        entry.settingsServices()
    }
}

@Composable
internal fun SettingsServices.bound(): SettingsServices {
    val source = binding ?: return this
    val current = source.collectAsState().value
    return remember(this, current) {
        copy(deviceSettings = current?.deviceSettings, hubAddress = current?.hubAddress, binding = null)
    }
}

/**
 * These services with the current assistant's settings scoped to it (docs/assistant/managers.md "Clients", the first group
 * of Settings): model providers, model configuration, triage, connectors with mail and Lark, channels, the Lark bot
 * and the sent-as-you ledger name [assistant] on every request, so their pages read and write that assistant's alone;
 * extensions are pinned to it under `extensions/` (their list is filtered by each one's stamp). A hub without a roster
 * (older, one assistant) gets the requests it always got.
 */
internal fun SettingsServices.forAssistant(assistant: persona.core.contracts.api.CurrentAssistant): SettingsServices {
    // The default assistant's (one assistant too) and an older hub's (no roster) name none: the requests they always sent.
    val roster = assistant.roster ?: return this
    val id = assistant.id
    if (id.isBlank() || id == roster.defaultManagerId) return this
    return copy(
        providers = providers?.forManager(id),
        models = models?.forManager(id),
        triage = triage?.forManager(id),
        knowledgeSelection = knowledgeSelection?.forManager(id),
        connectors = connectors?.forManager(id),
        channels = channels?.forManager(id),
        feishuBot = feishuBot?.forManager(id),
        sent = sent?.forManager(id),
        standing = standing?.forManager(id),
        // Its extensions and their source subscriptions (`/api/v1/subscriptions…`, answered by its own extension manager).
        extensions = extensions?.pinned(ManagerPins.of(listOf(listOf("extensions"), listOf("plugins"), listOf("subscriptions")), id)),
    )
}

/** [SettingsServices.forAssistant] for the current assistant, made again when it changes. */
@Composable
internal fun SettingsServices.forCurrentAssistant(): SettingsServices {
    val assistant = persona.core.designsystem.currentAssistant()
    return remember(this, assistant.id, assistant.roster?.defaultManagerId, assistant.roster != null) { forAssistant(assistant) }
}
