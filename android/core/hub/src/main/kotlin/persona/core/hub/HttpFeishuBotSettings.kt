package persona.core.hub

import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.FeishuBotStatus
import persona.core.contracts.api.FeishuBotUpdate
import persona.core.contracts.api.FeishuGroupAccessSettings
import persona.core.contracts.api.LarkSyncSettings

/**
 * Settings › Lark bot over the pairing's connection (docs/extensions/bots.md"Configuration and endpoints"). Saving credentials waits on the hub
 * checking them with Feishu's open platform: longer than the client's usual read timeout.
 */
class HttpFeishuBotSettings internal constructor(
    /** The bot's daily sync, over the same connection. */
    override val sync: LarkSyncSettings? = null,
    /** Who else may @ the bot in a group, over the same connection. */
    override val groupAccess: FeishuGroupAccessSettings? = null,
    private val request: suspend (path: String, method: String, body: RequestBody?, readTimeoutSeconds: Long?) -> Response,
) : FeishuBotSettings {
    constructor(connection: OkHttpHubConnection) : this(HttpLarkSync(connection), HttpFeishuGroupAccess(connection), { path, method, body, timeout ->
        connection.request(path, method, body, readTimeoutSeconds = timeout)
    })

    override suspend fun status() = decode(call("GET", null, null))

    override suspend fun update(update: FeishuBotUpdate) = decode(call("PUT", buildJsonObject {
        update.enabled?.let { put("enabled", it) }
        update.appId?.let { put("app_id", it) }
        update.appSecret?.takeIf { it.isNotEmpty() }?.let { put("app_secret", it) }
        update.domain?.let { put("domain", it) }
    }, if (update.appId != null) VerifySeconds else null))

    private fun decode(body: String): FeishuBotStatus = try { JsonCodec.decodeFromString(FeishuBotStatus.serializer(), body) }
    catch (error: SerializationException) { throw HubFailure(HubError.Protocol, cause = error) }
    catch (error: IllegalArgumentException) { throw HubFailure(HubError.Protocol, cause = error) }

    // The pairing's connection throws a HubFailure for a status that is not 2xx; a bare request returns it.
    private suspend fun call(method: String, body: JsonObject?, timeout: Long?): String =
        request(Path, method, body?.toString()?.toRequestBody(json), timeout).use { response ->
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }

    private companion object {
        const val Path = "/api/v1/bots/feishu"
        val json = "application/json; charset=utf-8".toMediaType()
        /** The hub verifies new credentials with the open platform before it saves them (iOS waits 40 s too). */
        const val VerifySeconds = 40L
    }
}
