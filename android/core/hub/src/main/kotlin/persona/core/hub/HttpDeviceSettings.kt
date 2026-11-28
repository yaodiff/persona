package persona.core.hub

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

/** Supply the paired TLS/local-network-policy client and recreate this adapter when pairing changes. */
class HttpDeviceSettings(
    baseUrl: String,
    private val token: String,
    private val preferenceDeviceId: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean = { true },
) : DeviceSettings {
    private val base = baseUrl.toHttpUrl()
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()

    private suspend fun request(vararg path: String, method: String = "GET", body: JsonObject? = null): JsonObject {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.hubUrl("/api/v1").newBuilder().apply {
            path.forEach(::addPathSegment)
        }.build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .method(method, body?.toString()?.toRequestBody("application/json".toMediaType())).build()
        return http.newCall(request).awaitResponse().use { response ->
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!response.isSuccessful) throw HubFailure.from(response)
            val text = response.body?.string().orEmpty()
            if (text.isBlank()) JsonObject(emptyMap()) else JsonCodec.parseToJsonElement(text).jsonObject
        }
    }

    override suspend fun devices(): PairedDevices {
        val data = try { request("auth", "tokens") } catch (failure: HubFailure) {
            throw when (failure.status) {
                404 -> DeviceCredentialsUnavailable(CoreText.of(Key.DeviceCredentialsUnsupported))
                503 -> DeviceCredentialsUnavailable(CoreText.of(Key.DeviceCredentialsNoDatabase))
                else -> failure
            }
        }
        val rows = when (val value = data["tokens"]) {
            JsonNull -> JsonArray(emptyList())
            is JsonArray -> value
            else -> throw HubFailure(HubError.Protocol)
        }
        val all = rows.map { it.jsonObject }.map { item ->
            PairedDevice(item.getValue("id").jsonPrimitive.content, item.text("name").orEmpty(),
                item.text("kind").orEmpty(), item.text("role").orEmpty(), item.text("last_seen"), item.text("platform"),
                item.text("created_at"), item.text("revoked_at")?.takeIf(String::isNotBlank))
        }
        val onlineIds = (data["online"] as? JsonArray)
            ?.mapNotNull { (it as? JsonPrimitive)?.contentOrNull?.takeIf(String::isNotBlank) }
            ?.toSet()
            .orEmpty()
        return PairedDevices(all.filter { it.revokedAt == null }, data.text("current")?.takeIf(String::isNotBlank),
            all.filter { it.revokedAt != null }, onlineIds)
    }

    override suspend fun revoke(credentialId: String) {
        require(credentialId.isNotBlank())
        request("auth", "tokens", credentialId, method = "DELETE")
    }

    override suspend fun preferences(): DeviceNotificationPreferences {
        require(preferenceDeviceId.isNotBlank())
        return decode(request("devices", preferenceDeviceId, "prefs"))
    }

    override suspend fun lowDistraction(): Boolean? = try {
        request("push", "ln-mode")["enabled"]?.jsonPrimitive?.booleanOrNull == true
    } catch (failure: HubFailure) {
        if (failure.status == 404 || failure.status == 405) null else throw failure // an older hub has no switch
    }

    override suspend fun saveLowDistraction(on: Boolean): Boolean =
        request("push", "ln-mode", method = "PUT", body = buildJsonObject { put("enabled", on) })["enabled"]?.jsonPrimitive?.booleanOrNull == true

    override suspend fun savePreferences(preferences: DeviceNotificationPreferences): DeviceNotificationPreferences {
        require(preferenceDeviceId.isNotBlank())
        // Leave island and future server fields untouched.
        return decode(request("devices", preferenceDeviceId, "prefs", method = "PUT", body = buildJsonObject {
            put("input", preferences.input)
            put("done", preferences.done.wire)
            put("lock_details", preferences.lockDetails)
            put("live_activity", preferences.liveActivity)
            preferences.muteWhileDesktop?.let { put("mute_while_desktop", it) }
        }))
    }

    override suspend fun browserLogin(id: String, hub: String, allow: Boolean?): BrowserLoginDetails {
        val data = request("auth", "browser-login", "approve", method = "POST", body = buildJsonObject {
            put("id", id); put("hub", hub); allow?.let { put("allow", it) }
            if (allow == true) put("show_number", true)
        })
        return BrowserLoginDetails(
            name = data.getValue("name").jsonPrimitive.content,
            source = data.getValue("source").jsonPrimitive.content,
            ageSeconds = data.getValue("age_seconds").jsonPrimitive.int,
            state = data.text("state"),
            network = data.text("network"),
            number = data.text("number"),
        )
    }

    override suspend fun pairCode(): AppPairCode {
        val data = request("auth", "pair-codes", method = "POST", body = buildJsonObject { put("web", webBase(base)) })
        val token = data.text("token")?.takeIf(String::isNotBlank) ?: throw HubFailure(HubError.Protocol)
        val pairUrl = data.text("pair_url")?.takeIf(String::isNotBlank) ?: throw HubFailure(HubError.Protocol)
        return AppPairCode(token, wireMillis(data.text("expires_at")), pairUrl, qr(data["qr"]),
            data.text("web_url")?.takeIf(String::isNotBlank), qr(data["web_qr"]), loopbackOnly(base.host))
    }

    override suspend fun saveMutedManagers(ids: List<String>): List<String> {
        require(preferenceDeviceId.isNotBlank())
        val answer = request("devices", preferenceDeviceId, "prefs", method = "PUT", body = buildJsonObject {
            put("muted_managers", JsonArray(ids.distinct().map(::JsonPrimitive)))
        })
        return mutedManagers(answer) ?: throw HubFailure(HubError.Protocol)
    }

    override suspend fun saveQuietHours(quiet: QuietHours): QuietHours {
        require(preferenceDeviceId.isNotBlank())
        val answer = request("devices", preferenceDeviceId, "prefs", method = "PUT", body = buildJsonObject {
            put("quiet", buildJsonObject {
                put("enabled", quiet.enabled)
                put("start", quiet.start)
                put("end", quiet.end)
                quiet.tz?.takeIf(String::isNotBlank)?.let { put("tz", it) }
            })
        })
        return quietHours(answer) ?: throw HubFailure(HubError.Protocol)
    }

    /** Tells the hub this device's Settings › Translation (only that field: the rest of its prefs stay as they are). */
    suspend fun saveTranslatePrefs(prefs: HubTranslatePrefs): HubTranslatePrefs? {
        require(preferenceDeviceId.isNotBlank())
        return decode(request("devices", preferenceDeviceId, "prefs", method = "PUT", body = buildJsonObject {
            // Every field, false ones too: the hub merges into what it has, and the codec drops defaults.
            put("translate", buildJsonObject {
                put("enabled", prefs.enabled)
                put("target", prefs.target)
                put("mail", prefs.mail)
            })
        })).translate
    }

    private fun decode(data: JsonObject) = DeviceNotificationPreferences(
        input = data["input"]?.jsonPrimitive?.boolean ?: throw HubFailure(HubError.Protocol),
        done = DoneNotification.entries.firstOrNull { it.wire == data.text("done") } ?: throw HubFailure(HubError.Protocol),
        lockDetails = data["lock_details"]?.jsonPrimitive?.booleanOrNull ?: false,
        longTurnSeconds = data["long_turn_seconds"]?.jsonPrimitive?.intOrNull,
        doneDelaySeconds = data["done_delay_seconds"]?.jsonPrimitive?.intOrNull,
        liveActivity = data["live_activity"]?.jsonPrimitive?.booleanOrNull ?: true,
        muteWhileDesktop = data["mute_while_desktop"]?.jsonPrimitive?.booleanOrNull,
        mutedManagers = mutedManagers(data),
        quiet = quietHours(data),
        translate = (data["translate"] as? JsonObject)?.let {
            runCatching { JsonCodec.decodeFromJsonElement(HubTranslatePrefs.serializer(), it) }.getOrNull()
        },
    )

    /** `muted_managers` (always a list from a hub that has it); null from an older hub. */
    private fun mutedManagers(data: JsonObject): List<String>? = when (val value = data["muted_managers"]) {
        is JsonArray -> value.mapNotNull { (it as? JsonPrimitive)?.contentOrNull?.takeIf(String::isNotBlank) }
        JsonNull -> emptyList()
        else -> null
    }

    /** `quiet` (an object from a hub that has it, empty times while never set); null from an older hub. */
    private fun quietHours(data: JsonObject): QuietHours? {
        val value = data["quiet"] as? JsonObject ?: return null
        return QuietHours(
            enabled = (value["enabled"] as? JsonPrimitive)?.booleanOrNull ?: false,
            start = value.text("start")?.takeIf { QuietHours.minutes(it) != null } ?: QuietHours.DEFAULT_START,
            end = value.text("end")?.takeIf { QuietHours.minutes(it) != null } ?: QuietHours.DEFAULT_END,
            tz = value.text("tz")?.takeIf(String::isNotBlank),
        )
    }

    private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull

    companion object {
        /**
         * The page the browser sign-in link opens: the hub serves the web at its own address (docs/web/web.md), under the
         * sub-path a reverse proxy puts it at; no query, fragment or account.
         */
        fun webBase(hub: okhttp3.HttpUrl): String {
            val path = hub.encodedPath.let { if (it.endsWith("/")) it else "$it/" }
            return hub.newBuilder().query(null).fragment(null).username("").password("").encodedPath(path).build().toString()
        }

        /** Whether only this device can open [host]: a link to it does nothing on another one. */
        fun loopbackOnly(host: String): Boolean {
            val h = host.lowercase().removePrefix("[").removeSuffix("]")
            return h == "localhost" || h.endsWith(".localhost") || h == "::1" || h.startsWith("127.")
        }

        internal fun qr(value: JsonElement?): QrImage? {
            val o = value as? JsonObject ?: return null
            val size = (o["size"] as? JsonPrimitive)?.intOrNull ?: return null
            val rows = (o["rows"] as? JsonArray)?.mapNotNull { (it as? JsonPrimitive)?.contentOrNull } ?: return null
            return QrImage(size, rows).takeIf { it.valid }
        }

        /** RFC3339 (Z or an offset) to epoch milliseconds; 0 when unreadable. */
        fun wireMillis(text: String?): Long =
            text?.let { runCatching { java.time.OffsetDateTime.parse(it).toInstant().toEpochMilli() }.getOrNull() } ?: 0L
    }
}
