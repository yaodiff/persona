package persona.feature.settings

import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import persona.core.designsystem.currentAssistant
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.ChannelRoute
import persona.core.navigation.ConnectorRoute
import persona.core.navigation.EngineRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.Navigator
import persona.core.navigation.LauncherRoute
import persona.core.navigation.ExtensionCatalogRoute
import persona.core.navigation.ExtensionMarketRoute
import persona.core.navigation.ExtensionMarketsRoute
import persona.core.navigation.ExtensionRoute
import persona.core.navigation.ExtensionUiRoute

/**
 * Settings pages opened by key rather than from the settings list: a machine's engine (from its page in Agents) and
 * one AI use of "Model configuration" (from that list, or from the voice page for its extension's model), one extension of "Extensions" and the
 * pages it draws, "Extension market" with its extensions and markets, a launcher of a machine (from the machine's page), and one channel
 * of "Channels".
 */
fun EntryProviderScope<NavKey>.settingsDetailEntries(navigator: Navigator, services: SettingsServices,
    askPersona: ((Navigator, String) -> Unit)? = null) {
    val demo = services.demo
    entry<EngineRoute> { route ->
        EngineSelectionScreen(route.machineId, services.sessions, services.machines.takeUnless { demo }, pairing = false,
            onDone = navigator::back, onBack = navigator::back)
    }
    entry<ModelPurposeRoute> { route ->
        val machines by services.sessions.machines.collectAsState()
        ModelPurposePage(services.models.takeUnless { demo }, route.purposeId, onBack = navigator::back,
            providers = services.providers.takeUnless { demo }, manager = route.manager, machines = machines)
    }
    entry<ExtensionRoute> { route ->
        val manager = remember(route) { ExtensionOwners.of(route.manager) }
        val subscriptionsTitle = stringResource(R.string.settings_subscriptions_title)
        ExtensionPage(services.extensions.takeUnless { demo }, route.extensionId, onBack = navigator::back, startSignIn = route.signIn,
            manager = manager,
            onPage = { page -> navigator.navigate(ExtensionUiRoute(page.extension.ifBlank { route.extensionId }, page.id, page.title.ifBlank { page.id }, manager = manager)) },
            onChannel = { navigator.navigate(ChannelRoute(it)) }, machines = services.sessions.machines,
            launchers = services.providers.takeUnless { demo }?.let { p -> { p.launchers() } },
            onUpgrade = { market, id -> navigator.navigate(ExtensionCatalogRoute(market, id, manager = manager)) },
            connectors = services.connectors.takeUnless { demo }, onConnector = { navigator.navigate(ConnectorRoute(it, manager)) },
            onSubscriptions = { navigator.navigate(subscriptionsRoute(subscriptionsTitle, extension = route.extensionId)) })
    }
    entry<ConnectorRoute> { route ->
        val subscriptionsTitle = stringResource(R.string.settings_subscriptions_title)
        HubConnectorPage(services.connectors.takeUnless { demo }, route.connectorId, onBack = navigator::back, manager = route.manager,
            extensions = services.extensions.takeUnless { demo },
            onSubscriptions = { navigator.navigate(subscriptionsRoute(subscriptionsTitle, connector = route.connectorId)) })
    }
    entry<ExtensionUiRoute> { route ->
        ExtensionUiPage(services.extensions.takeUnless { demo }, route.extensionId, route.uiId, route.title, onBack = navigator::back,
            initialState = route.state, depth = route.depth,
            shown = LocalExtensionPageVisible.current && (persona.core.navigation.LocalDetailKey.current == null || persona.core.navigation.LocalDetailKey.current == route),
            onPush = { navigator.navigate(it.copy(manager = it.manager ?: route.manager)) }, manager = route.manager,
            onAsk = if (route.extensionId == persona.core.contracts.api.SUBSCRIPTIONS_UI) askPersona?.let { ask -> { text -> ask(navigator, text) } } else null)
    }
    // The market of the current assistant's Extensions (docs/assistant/managers.md "Clients"): what it installs is that one's.
    entry<ExtensionMarketRoute> {
        val mine = services.forCurrentAssistant()
        val owner = currentAssistant().id
        ExtensionMarketPage(mine.extensions.takeUnless { demo }, onBack = navigator::back,
            onExtension = { navigator.navigate(ExtensionCatalogRoute(it.market, it.id, manager = owner)) }, onMarkets = { navigator.navigate(ExtensionMarketsRoute) })
    }
    entry<ExtensionCatalogRoute> { route ->
        val manager = remember(route) { ExtensionOwners.of(route.manager) }
        ExtensionCatalogPage(services.extensions.takeUnless { demo }, route.marketId, route.extensionId, onBack = navigator::back,
            manager = manager,
            onInstalled = { navigator.navigate(ExtensionRoute(it, manager = manager)) },
            onSignIn = { navigator.navigate(ExtensionRoute(it, signIn = true, manager = manager)) })
    }
    entry<ExtensionMarketsRoute> { ExtensionMarketsPage(services.forCurrentAssistant().extensions.takeUnless { demo }, onBack = navigator::back) }
    entry<persona.core.navigation.ManagerRoute> { route ->
        val machines by services.sessions.machines.collectAsState()
        ManagerPage(services.managers.takeUnless { demo }, route.managerId, onBack = navigator::back,
            devices = services.bound().deviceSettings.takeUnless { demo }, machines = machines)
    }
    // One of the current assistant's channels and model providers (its Settings, docs/assistant/managers.md "Clients").
    entry<ChannelRoute> { route ->
        ChannelPage(services.forCurrentAssistant().channels.takeUnless { demo }, route.channelId, onBack = navigator::back, onOpen = navigator::navigate)
    }
    entry<persona.core.navigation.ModelProviderRoute> { route ->
        val machines by services.sessions.machines.collectAsState()
        ProviderDetailPage(services.forCurrentAssistant().providers.takeUnless { demo }, route.name, machines, onBack = navigator::back)
    }
    entry<LauncherRoute> { route ->
        // Only the machines' names (for "Quota"'s footer): their live metrics change all the time and would redraw the page.
        val names by remember(services.sessions) {
            services.sessions.machines.map { list -> list.associate { it.id to it.name } }.distinctUntilChanged()
        }.collectAsState(emptyMap())
        val machineName = remember(names) { { id: String -> names[id]?.takeIf { it.isNotBlank() } ?: id } }
        LauncherPage(services.machines.takeUnless { demo }, services.extensions.takeUnless { demo }, route.machineId, route.name, onBack = navigator::back,
            machineName = machineName, onOpen = navigator::navigate)
    }
}
