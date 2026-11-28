package persona.core.hub

import kotlinx.serialization.SerializationException
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.FeishuGroupAccess
import persona.core.contracts.api.FeishuGroupAccessPolicy
import persona.core.contracts.api.FeishuGroupAccessSettings
import persona.core.contracts.api.FeishuGroupChat
import persona.core.contracts.api.FeishuGroupUser

/**
 * Settings › Lark bot › Group access over the pairing's connection (web `lib/feishu-group-access.ts`, hub
 * `extmgr/bots_group_access.go`). A hub from before the route answers `GET` with 404: null, and the section hides. A 409
 * is a stale revision (`group_access_conflict`); the page reloads and tells the user it changed elsewhere.
 */
class HttpFeishuGroupAccess internal constructor(
    private val request: suspend (path: String, method: String, body: RequestBody?) -> Response,
) : FeishuGroupAccessSettings {
    constructor(connection: OkHttpHubConnection) : this({ path, method, body -> connection.request(path, method, body) })

    override suspend fun read(): FeishuGroupAccess? = try { decode(call("GET", null)) } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun update(revision: Long, policy: FeishuGroupAccessPolicy): FeishuGroupAccess =
        decode(call("PUT", buildJsonObject {
            put("revision", revision)
            putJsonObject("policy") {
                put("mode", policy.mode)
                put("allowed_users", JsonCodec.encodeToJsonElement(ListSerializer(FeishuGroupUser.serializer()), policy.allowedUsers))
                put("allowed_chats", JsonCodec.encodeToJsonElement(ListSerializer(FeishuGroupChat.serializer()), policy.allowedChats))
                put("blocked_users", JsonCodec.encodeToJsonElement(ListSerializer(FeishuGroupUser.serializer()), policy.blockedUsers))
                put("blocked_chats", JsonCodec.encodeToJsonElement(ListSerializer(FeishuGroupChat.serializer()), policy.blockedChats))
                put("guest_tools", JsonCodec.encodeToJsonElement(ListSerializer(String.serializer()), policy.guestTools))
                put("per_chat_hourly", policy.perChatHourly)
            }
        }))

    private fun decode(body: String): FeishuGroupAccess = try { JsonCodec.decodeFromString(FeishuGroupAccess.serializer(), body) }
    catch (error: SerializationException) { throw HubFailure(HubError.Protocol, cause = error) }
    catch (error: IllegalArgumentException) { throw HubFailure(HubError.Protocol, cause = error) }

    // The pairing's connection throws a HubFailure for a status that is not 2xx; a bare request returns it.
    private suspend fun call(method: String, body: JsonObject?): String =
        request(Path, method, body?.toString()?.toRequestBody(json)).use { response ->
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }

    private companion object {
        const val Path = "/api/v1/bots/feishu/group-access"
        val json = "application/json; charset=utf-8".toMediaType()
    }
}
