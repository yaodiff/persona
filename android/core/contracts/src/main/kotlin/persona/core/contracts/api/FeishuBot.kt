package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Settings › Lark bot (docs/extensions/bots.md"Configuration and endpoints", iOS `FeishuBotView`): the Feishu bot the user talks to Persona through,
 * its app credentials and how to make one. Pairing, its features and a test message are on its channel's page
 * ([ChannelManagement]). Bound to the active pairing's selected hub.
 */
interface FeishuBotSettings {
    /** `GET /api/v1/bots/feishu`. */
    suspend fun status(): FeishuBotStatus

    /** `PUT /api/v1/bots/feishu` with only what [update] holds; credentials the hub can't verify are its words (400). */
    suspend fun update(update: FeishuBotUpdate): FeishuBotStatus

    /** The bot's daily sync into the knowledge base ([LarkSyncSettings]); null where this client has none. */
    val sync: LarkSyncSettings? get() = null

    /** Who else may @ the bot in a group ([FeishuGroupAccessSettings]); null where this client has none. */
    val groupAccess: FeishuGroupAccessSettings? get() = null
}

@Serializable data class FeishuBotStatus(
    val enabled: Boolean = false,
    @SerialName("app_id") val appId: String = "",
    val domain: String = "feishu",
    @SerialName("has_secret") val hasSecret: Boolean = false,
    /** The credentials come from hub.json; saving here replaces them with these. */
    @SerialName("from_config") val fromConfig: Boolean = false,
    val connected: Boolean = false,
    val error: String? = null,
    /** Unix seconds; 0 or absent: nothing yet. */
    @SerialName("last_event_at") val lastEventAt: Long? = null,
    @SerialName("bot_name") val botName: String? = null,
    val owner: FeishuBotOwner? = null,
    val pairing: FeishuBotPairing? = null,
    /** Asked of the assistant when the page loads: absent while it isn't connected. */
    val target: String? = null,
    val agents: List<FeishuBotAgent>? = null,
)

@Serializable data class FeishuBotOwner(val name: String = "", @SerialName("paired_at") val pairedAt: Long = 0)

@Serializable data class FeishuBotPairing(val code: String = "", @SerialName("expires_at") val expiresAt: Long = 0)

@Serializable data class FeishuBotAgent(
    val title: String = "",
    @SerialName("machine_id") val machineId: String = "",
    @SerialName("session_id") val sessionId: String = "",
)

/** What `PUT` changes: only what is set. [appId] with [appSecret] and [domain] are checked by the hub first. */
data class FeishuBotUpdate(
    val enabled: Boolean? = null,
    val appId: String? = null,
    val appSecret: String? = null,
    val domain: String? = null,
)
