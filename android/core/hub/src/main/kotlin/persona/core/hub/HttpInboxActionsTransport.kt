package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.InboxActionsTransport
import persona.core.contracts.api.InboxItemContext
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.put
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

/** Resolve the active route per call; credentials and scope must belong to that route. */
class HttpInboxActionsTransport(
    client: OkHttpClient,
    private val endpoint: () -> HttpUrl,
    private val token: String,
    private val isCurrent: () -> Boolean,
) : InboxActionsTransport {
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()
    // The hub answers a draft only once its model has written it. The wait is a configured ceiling, not a measurement.
    private val draftHttp = http.newBuilder().readTimeout(DRAFT_READ_TIMEOUT_SECONDS, TimeUnit.SECONDS).build()

    private fun url(vararg segments: String): HttpUrl {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        return endpoint().hubUrl("/api/v1/inbox").newBuilder()
            .apply { segments.forEach(::addPathSegment) }.build()
    }

    private suspend fun request(itemId: String? = null): String =
        call(http, url(itemId ?: "refresh"), if (itemId == null) "{}" else null)

    private suspend fun call(client: OkHttpClient, url: HttpUrl, body: String?): String {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val request = Request.Builder().url(url.withRequestManager()).header("Authorization", "Bearer $token").apply {
            if (body != null) post(body.toRequestBody("application/json".toMediaType()))
        }.build()
        return client.newCall(request).awaitResponse().use { response ->
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }
    }

    override suspend fun collectNow() { request() }
    override suspend fun context(itemId: String): InboxItemContext {
        val result = JsonCodec.parseToJsonElement(request(itemId)).jsonObject
        val item = result["item"]?.jsonObject ?: result
        val text = item["context"]?.jsonPrimitive?.contentOrNull.orEmpty()
        // `mail_body_truncated` stands beside `context` (#15995). Only a real JSON boolean decides: absent, a string
        // or a number stays null ("not known"), never false/"whole". A manager envelope nests the item under `item`,
        // so an envelope-level flag is accepted as a fallback.
        val truncated = item.wireBoolean("mail_body_truncated") ?: result.wireBoolean("mail_body_truncated")
        return InboxItemContext(text, truncated)
    }

    override suspend fun draft(itemId: String, instruction: String): String {
        require(itemId.isNotBlank())
        val url = url(itemId, "draft")
        val body = JsonCodec.encodeToString(kotlinx.serialization.json.JsonObject.serializer(),
            buildJsonObject { put("instruction", instruction) })
        val result = JsonCodec.parseToJsonElement(call(draftHttp, url, body)).jsonObject
        return result["text"]?.jsonPrimitive?.contentOrNull?.trim()?.takeIf { it.isNotEmpty() }
            ?: throw HubFailure(HubError.Protocol, "draft is empty")
    }

    companion object {
        /** Policy for one model completion on the hub; unmeasured. */
        const val DRAFT_READ_TIMEOUT_SECONDS = 120L
    }
}

/** A real JSON boolean (`true`/`false` token) only; null when absent, a string, a number or another type. */
private fun JsonObject.wireBoolean(key: String): Boolean? {
    val primitive = this[key] as? JsonPrimitive ?: return null
    if (primitive.isString) return null
    return when (primitive.content) {
        "true" -> true
        "false" -> false
        else -> null
    }
}
