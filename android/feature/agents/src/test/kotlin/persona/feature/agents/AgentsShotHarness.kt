package persona.feature.agents

import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isSelected
import androidx.compose.ui.test.junit4.ComposeContentTestRule
import androidx.compose.ui.test.performClick
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.navigation3.runtime.NavKey
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.NavItem
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** The compact home navigation when the Agents tab is already open. */
internal val agentsHomeChrome = HomeChrome(
    items = listOf(
        NavItem(AppTab.Persona.name, "Assistant", PhIcons.Sparkle),
        NavItem(AppTab.Agents.name, "Agents", PhIcons.SquaresFour),
    ),
    selected = AppTab.Agents.name,
    onSelect = {},
    leading = {},
)

internal fun ComposeContentTestRule.reselectAgentsTab() {
    onNode(hasText("Agents") and isSelected()).performClick()
}

/**
 * The Agents screens' dependencies over fixed [sessions] and [machines], with some of them [pins]ned or [mutes]d (session ids
 * as `machine/id`, spaces as `machine/name` under [pinnedSpaces] and [mutedSpaces]): no hub, no network, preferences in memory.
 */
internal fun agentsDependencies(
    sessions: List<Session>, machines: List<Machine>, pins: List<String> = emptyList(), mutes: List<String> = emptyList(),
    pinnedSpaces: List<String> = emptyList(), mutedSpaces: List<String> = emptyList(),
): AgentsDependencies = object : AgentsDependencies {
    private val repository = object : SessionsRepository {
        override val sessions = MutableStateFlow(sessions)
        override val machines = MutableStateFlow(machines)
        override suspend fun refresh() = Unit
    }
    private val center = object : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val hub = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private val outgoing = object : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val stored = AgentsPreferences(object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences) = transform(data.value).also { data.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot(
            pins = HubPreferenceSet(spaces = pinnedSpaces, sessions = pins), mutes = HubPreferenceSet(spaces = mutedSpaces, sessions = mutes)))
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) {
            preferences.update { it.copy(seen = it.seen + (session.path to version)) }
        }
    })
    override fun sessions() = repository
    override fun interactions() = center
    override fun connection() = hub
    override fun sender() = outgoing
    override fun preferences() = stored
    override fun machines(): MachineManagement? = null
}

/** A view model over the same fixed data ([agentsDependencies]) for the screenshots that draw the whole Agents screen. */
internal fun agentsModel(
    sessions: List<Session>, machines: List<Machine>, pins: List<String> = emptyList(), mutes: List<String> = emptyList(),
): AgentsViewModel = agentsDependencies(sessions, machines, pins, mutes).let {
    AgentsViewModel(it.sessions(), it.interactions(), it.connection(), it.sender(), it.preferences())
}

/** A navigator that goes nowhere: the screens under test open nothing. */
internal object NoNavigation : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}
