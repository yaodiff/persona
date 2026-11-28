package persona.android.di

import persona.android.AppText
import persona.android.R
import android.app.Application
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import persona.android.PersonaApp
import persona.core.contracts.api.*
import persona.core.hub.HttpChannelManagement
import persona.core.hub.HttpFeishuBotSettings
import persona.core.hub.HttpFeishuGroupAccess
import persona.core.hub.HttpLarkSync
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Settings › Channels (docs/extensions/channels.md) and the channels' marks in conversations. The settings pages are hub-level:
 * the selected group's hub, in its own ids. The marks come from every open group's list.
 */
@Module
@InstallIn(SingletonComponent::class)
object ChannelsModule {
    @OptIn(ExperimentalCoroutinesApi::class)
    @Provides @Singleton fun channels(runtime: ConnectionRuntime, stacks: GroupStacks, application: Application): ChannelManagement =
        object : ChannelManagement {
            // The selected hub's features, from its hello: the settings row shows while it says channels.
            override val available: Flow<Boolean> = (application as PersonaApp).hub
                .flatMapLatest { hub -> hub?.connection?.serverFeatures ?: flowOf(emptySet()) }
                .map { "channels" in it }.distinctUntilChanged()
            // "Send messages to" picks one of the selected hub's assistant conversations, in that hub's own ids.
            override val conversations: Flow<List<persona.core.contracts.Session>> = stacks.selected
                .flatMapLatest { stack -> stack?.repositories?.sessions?.sessions ?: flowOf(emptyList()) }
                .map { list -> list.filter { it.assistant } }
            private fun api(): HttpChannelManagement {
                val data = runtime.scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)
                return HttpChannelManagement(runtime.connectionFor(data), available)
            }
            override suspend fun channels() = api().channels()
            override suspend fun channel(id: String) = api().channel(id)
            override suspend fun update(id: String, change: ChannelChange) = api().update(id, change)
            override suspend fun bind(id: String, step: String?, values: Map<String, String>?) = api().bind(id, step, values)
            override suspend fun unbind(id: String) = api().unbind(id)
            override suspend fun test(id: String) = api().test(id)
        }

    @Provides @Singleton fun directory(store: ChannelStore): ChannelDirectory = store

    /** Settings › Lark bot: the selected hub's bot, like the channels' pages. */
    @Provides @Singleton fun feishuBot(runtime: ConnectionRuntime): FeishuBotSettings = object : FeishuBotSettings {
        private fun api(): HttpFeishuBotSettings {
            val data = runtime.scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)
            return HttpFeishuBotSettings(runtime.connectionFor(data))
        }
        override suspend fun status() = api().status()
        override suspend fun update(update: FeishuBotUpdate) = api().update(update)
        override val sync: LarkSyncSettings = LarkSyncThrough { HttpLarkSync(runtime.connectionFor(
            runtime.scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false))) }
        override val groupAccess: FeishuGroupAccessSettings = object : FeishuGroupAccessSettings {
            private fun api() = HttpFeishuGroupAccess(runtime.connectionFor(
                runtime.scopedData.value ?: throw HubFailure(HubError.NoNetwork, AppText.get(R.string.app_error_pair_first), retryable = false)))
            override suspend fun read() = api().read()
            override suspend fun update(revision: Long, policy: FeishuGroupAccessPolicy) = api().update(revision, policy)
        }
    }
}

/**
 * Every open group's channels (GET /api/v1/channels), kept per hub group and read again each time its connection comes
 * online with `channels` in its features; kept while offline, so the marks stay. The selected group's list comes
 * first: a channel id both have takes its name.
 */
@Singleton
@OptIn(ExperimentalCoroutinesApi::class)
class ChannelStore @Inject constructor(application: Application, private val stacks: GroupStacks) : ChannelDirectory {
    private val app = application as PersonaApp
    private val lists = MutableStateFlow<Map<String, List<HubChannel>>>(emptyMap())

    override val channels: StateFlow<List<HubChannel>> = combine(lists, stacks.selected) { byGroup, selected ->
        val order = listOfNotNull(selected?.id) + byGroup.keys.filter { it != selected?.id }
        ChannelSources.merge(order.mapNotNull { byGroup[it] })
    }.distinctUntilChanged().stateIn(app.scope, SharingStarted.Eagerly, emptyList())

    init {
        app.scope.launch {
            stacks.stacks.collectLatest { open ->
                // A group that went (disconnected, merged) takes its list with it.
                lists.update { byGroup -> byGroup.filterKeys { id -> open.any { it.id == id } } }
                coroutineScope { open.forEach { stack -> launch { follow(stack) } } }
            }
        }
    }

    private suspend fun follow(stack: GroupStack) {
        val connection = stack.connection
        combine(connection.state, connection.serverFeatures) { state, features ->
            if (state == ConnectionState.Online) "channels" in features else null
        }.distinctUntilChanged().collectLatest { has ->
            when (has) {
                null -> Unit // offline: keep what was read
                false -> lists.update { it - stack.id } // a hub without channels (or one that dropped them)
                true -> try {
                    val list = HttpChannelManagement(connection, flowOf(true)).channels()
                    lists.update { it + (stack.id to list) }
                } catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { /* The next reconnect reads it again; the marks keep the last list. */ }
            }
        }
    }
}
