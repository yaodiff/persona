package persona.core.hub

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.*
import persona.core.contracts.api.*
import java.io.OutputStream
import java.security.MessageDigest
import java.time.Instant
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.*
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

data class PairedHub(
    val hubId: String, val token: String, val deviceId: String?, val address: String,
    val fingerprint: String?, val candidates: List<String>,
    /** The link's proxied origins, checked with system trust ([HubHttpClient.systemOrigins], #2622). */
    val systemOrigins: Set<String> = emptySet(),
) {
    override fun toString() = "PairedHub(<redacted>)"
}

/** Networking for pairing and settings. Credentials are persisted by the application. */
class HubPairingClient private constructor(
    private val expectedHubId: String?,
    val fingerprint: String?,
    addresses: List<String>,
    version: String,
    private val connection: OkHttpHubConnection?,
    /** The link's proxied origins ([systemTrustedOrigin], #2622). */
    private val systemOrigins: Set<String> = emptySet(),
    /** Tests: where system trust comes from. */
    systemTrust: () -> javax.net.ssl.X509TrustManager = ::systemTrustManager,
) : AutoCloseable {
    /**
     * Pairing uses temporary credentials before a live connection exists. [access] is the Cloudflare Access service token
     * entered before pairing: sent to the pairing's HTTPS addresses (and those the hub lists) from the first probe on.
     * [systemOrigins]: the link's proxied `url`, checked with system trust ([systemTrustedOrigin]).
     */
    constructor(expectedHubId: String?, fingerprint: String?, addresses: List<String>, version: String, access: HubAccess? = null,
        systemOrigins: Set<String> = emptySet(), systemTrust: () -> javax.net.ssl.X509TrustManager = ::systemTrustManager) :
        this(expectedHubId, fingerprint, addresses, version, connection = null, systemOrigins, systemTrust) {
        if (access != null) http.access = { url -> access.takeIf { endpoints.any { it.url.toHttpUrl().sameOrigin(url) } } }
    }

    /** Settings and downloads share the running connection's route and credentials. */
    constructor(connection: OkHttpHubConnection) : this(connection.http.hubId, connection.http.fingerprint,
        connection.candidates().map { it.url }, "", connection)

    val http = connection?.http ?: HubHttpClient(expectedHubId, fingerprint, version, systemTrust = systemTrust, systemOrigins = systemOrigins)
    private val selector = HubRouteSelector(http)
    private var endpoints = addresses.distinct().map(::endpoint)
    private var pairedAddress: String = endpoints.first().url
    val address: String get() = connection?.activeRoute()?.endpoint?.url ?: pairedAddress
    private var candidates = endpoints.map { ConnectionCandidate(it.url, it.kind, CoreText.of(Key.ProbeNotYet)) }
    private var lastFailure: String? = null

    suspend fun probe(progress: (PairingProgress) -> Unit = {}): HubRoute {
        if (connection != null) return connection.activeRoute()
        candidates = endpoints.map { ConnectionCandidate(it.url, it.kind, Probing) }
        progress(PairingProgress(PairingStage.Probe, candidates = candidates))
        try {
            val route = selector.select(orderEndpoints(endpoints)) { target, result ->
                candidates = candidates.map {
                    if (it.url != target.url) it else it.copy(
                        result = endpointResultLabel(result.exceptionOrNull()?.let { error -> HubFailure.from(error).kind }),
                        checkedAt = Instant.now().toString(),
                    )
                }
                progress(PairingProgress(PairingStage.Probe, candidates = candidates))
            }
            pairedAddress = route.endpoint.url
            candidates = candidates.map { if (it.result == Probing) it.copy(result = CoreText.of(Key.ProbeStopped)) else it }
            lastFailure = null
            return route
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            lastFailure = endpointResultLabel(HubFailure.from(error).kind).english()
            throw error
        }
    }

    suspend fun pair(
        token: String,
        deviceName: String,
        deviceId: String? = null,
        replaces: suspend (String) -> String? = { null },
        progress: (PairingProgress) -> Unit,
    ): PairedHub {
        check(connection == null) { "Pairing requires a new client" }
        val route = probe(progress)
        progress(PairingProgress(PairingStage.VerifyIdentity, candidates = candidates))
        val hubId = route.health.hubId ?: expectedHubId ?: legacyHubId(address)
        require(hubId.matches(Regex("[A-Za-z0-9_-]{1,128}")))
        progress(PairingProgress(PairingStage.ExchangeCredentials))
        val previous = if (deviceId == null) replaces(hubId)?.takeIf { it.isNotBlank() } else null
        val exchanged = if (deviceId != null) buildJsonObject { put("token", token); put("id", deviceId) } else try {
            json("/api/v1/auth/tokens", token, buildJsonObject {
                put("name", deviceName)
                put("kind", "phone")
                previous?.let { put("replaces", it) }
            })
        } catch (error: HubFailure) {
            if (error.status == 404) null else throw error
        }
        val deviceToken = exchanged?.get("token")?.jsonPrimitive?.contentOrNull
            ?.takeIf { it.isNotBlank() } ?: if (exchanged == null) token else throw HubFailure(HubError.Protocol)
        progress(PairingProgress(PairingStage.LoadMachines))
        val machines = json("/api/v1/machines", deviceToken)
        if (machines["machines"] !is JsonArray) throw HubFailure(HubError.Protocol)
        refreshEndpoints(deviceToken)
        return PairedHub(hubId, deviceToken, exchanged?.get("id")?.jsonPrimitive?.contentOrNull,
            address, fingerprint, endpoints.map { it.url }, systemOrigins)
    }

    suspend fun refreshEndpoints(token: String) {
        val response = try { json("/api/v1/endpoints", token) }
        catch (error: HubFailure) { if (error.status == 404) return else throw error }
        val metadata = JsonCodec.decodeFromJsonElement<EndpointsResponse>(response)
        if (expectedHubId != null && metadata.hubId != expectedHubId) throw HubFailure(HubError.HubMismatch)
        // The discovery response may add routes, but cannot replace the trusted pairing pin.
        if (fingerprint != null && !metadata.fp.isNullOrBlank() && metadata.fp != fingerprint) {
            throw HubFailure(HubError.CertificateMismatch)
        }
        // Without a pinned fingerprint, HTTPS candidates are checked against the system CAs (HubHttpClient.systemTrusted).
        val discovered = metadata.endpoints.mapNotNull { runCatching { endpoint(it.url) }.getOrNull() }
        endpoints = (endpoints + discovered).distinctBy { endpointKey(it.url) }
        candidates = endpoints.map { item -> candidates.firstOrNull { it.url == item.url }
            ?: ConnectionCandidate(item.url, item.kind, CoreText.of(Key.ProbeNotYet)) }
    }

    suspend fun diagnostics(token: String, deviceId: String?): ConnectionDiagnostics {
        val snapshot = ConnectionDiagnostics(
            currentPath = "${endpoint(address).kind} · $address", candidates = candidates,
            fingerprint = fingerprint, lastDisconnectReason = lastFailure, legacyHub = fingerprint == null && !address.startsWith("https://"),
        )
        if (deviceId == null) return snapshot
        val path = "/api/v1/devices/${deviceId.encodeSegment()}/sessions"
        val rows = try { json(path, token)["sessions"] as? JsonArray }
        catch (error: HubFailure) { if (error.status == 404) return snapshot else throw error }
        return snapshot.copy(sessions = rows.orEmpty().map { row ->
            val obj = row.jsonObject
            DeviceConnectionSession(obj.text("connected_at").orEmpty(), obj.text("disconnected_at"), obj.text("reason"))
        })
    }

    suspend fun checkUpdate(token: String): AppUpdate? = (lookupUpdate(token) as? AppUpdateLookup.Published)?.update

    suspend fun lookupUpdate(token: String): AppUpdateLookup {
        val metadata = try { json("/api/v1/app/android/latest", token) }
        catch (error: HubFailure) { if (error.status == 404 || error.status == 204) return AppUpdateLookup.NotPublished else throw error }
        val checksum = metadata.text("sha256")?.takeIf { it.matches(Regex("[A-Fa-f0-9]{64}")) }
            ?: throw HubFailure(HubError.Protocol)
        return AppUpdateLookup.Published(AppUpdate(
            metadata.getValue("version_code").jsonPrimitive.long,
            metadata.getValue("version_name").jsonPrimitive.content,
            metadata.text("download_url")?.takeIf { it.isNotBlank() } ?: "/api/v1/app/android/apk",
            checksum, metadata.text("notes") ?: metadata.text("release_notes").orEmpty(),
            metadata.text("package_name"), metadata.text("certificate_sha256"),
        ))
    }

    suspend fun download(token: String, update: AppUpdate, destination: OutputStream) = withContext(Dispatchers.IO) {
        val route = probe()
        val base = route.endpoint.baseUrl()
        // Same origin and path prefix as the hub (hubResolve), never another host.
        val target = requireNotNull(base.hubResolve(update.downloadUrl)) { "Invalid APK origin" }
        val path = requireNotNull(base.hubPath(target)) { "Invalid APK origin" }
        val checksum = update.sha256?.takeIf { it.matches(Regex("[A-Fa-f0-9]{64}")) }
            ?: throw HubFailure(HubError.Protocol)
        val digest = MessageDigest.getInstance("SHA-256")
        val response = if (connection != null) connection.request(
            path + (target.encodedQuery?.let { "?$it" } ?: ""), buffered = false, expectedRoute = route,
        ) else http.client.newCall(authorized(target, token).build()).awaitResponse(buffered = false)
        response.use { response ->
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body!!.byteStream().use { input ->
                val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
                while (true) {
                    currentCoroutineContext().ensureActive()
                    val count = input.read(buffer)
                    if (count < 0) break
                    digest.update(buffer, 0, count)
                    destination.write(buffer, 0, count)
                }
            }
        }
        val actual = digest.digest().joinToString("") { "%02x".format(it) }
        if (!actual.equals(checksum, ignoreCase = true)) throw HubFailure(HubError.Protocol,
            message = "APK checksum mismatch", code = "apk_checksum_mismatch", retryable = false)
    }

    suspend fun json(path: String, token: String, body: JsonObject? = null, headers: Map<String, String> = emptyMap()): JsonObject {
        if (connection != null) {
            return connection.request(path, if (body == null) "GET" else "POST",
                body?.toString()?.toRequestBody("application/json".toMediaType()), headers).use { response ->
                if (response.code == 204) throw HubFailure.from(response)
                JsonCodec.parseToJsonElement(response.body!!.string()).jsonObject
            }
        }
        require(path.startsWith("/api/"))
        val base = address.toHttpUrl()
        val target = base.hubUrl(path)
        require(base.hubPath(target)?.startsWith("/api/") == true)
        val builder = authorized(target, token)
        headers.forEach { (name, value) -> builder.header(name, value) }
        if (body != null) builder.post(body.toString().toRequestBody("application/json".toMediaType()))
        http.client.newCall(builder.build()).awaitResponse().use { response ->
            if (!response.isSuccessful || response.code == 204) throw HubFailure.from(response)
            return JsonCodec.parseToJsonElement(response.body!!.string()).jsonObject
        }
    }

    private fun authorized(url: HttpUrl, token: String) = Request.Builder().url(url).header("Authorization", "Bearer $token")
    override fun close() { if (connection == null) http.close() }

    companion object {
        fun addresses(request: PairingRequest): List<String> {
            require(request.token.isNotBlank())
            require(request.hub?.matches(Regex("[A-Za-z0-9_-]{1,128}")) != false)
            require(request.tls == null || request.tls in 1..65535)
            SpkiTrustManager(request.fp)
            val primary = endpoint(request.url).url.toHttpUrl()
            val urls = buildList {
                request.tls?.let { port ->
                    if (request.fp != null) add(primary.newBuilder().scheme("https").port(port).build().toString())
                }
                add(primary.toString())
                addAll(request.alt.map { endpoint(it).url })
            }.distinct()
            // HTTPS without `fp` (a hub behind a reverse proxy with a public CA's certificate, `https://host/prefix`) is
            // checked against the system CAs and the host name instead of a pinned key; see SpkiTrustManager.
            return urls
        }

        /**
         * The origin ("host:port") of a link with `fp` whose `url` is served by a reverse proxy or tunnel with a public
         * CA's certificate (#2622, docs/operations/network.md "Reverse-proxy subpaths (Android)"): an HTTPS `url` on a
         * host name, not on the hub's own TLS port (`tls`), checked like any site (system CAs, host name) as the iOS app
         * and the web client do. Null for every other link: an IP literal, the `tls` port, no `fp` (all of its HTTPS is
         * system-trusted anyway) or no `tls` (then `url` is the hub's own listener). Decided from the link alone and kept
         * with the pairing: the hub's endpoint list never adds one.
         */
        fun systemTrustedOrigin(request: PairingRequest): String? {
            if (request.fp == null || request.tls == null) return null
            val url = runCatching { endpoint(request.url).url.toHttpUrl() }.getOrNull() ?: return null
            if (!url.isHttps || url.port == request.tls || isIpLiteral(url.host)) return null
            return tlsKey(url.host, url.port)
        }

        private fun isIpLiteral(host: String) = host.contains(':') || host.matches(Regex("""\d{1,3}(\.\d{1,3}){3}"""))

        fun legacyHubId(address: String): String = "legacy-" + MessageDigest.getInstance("SHA-256")
            .digest(endpoint(address).url.toByteArray()).take(16).joinToString("") { "%02x".format(it) }

        private fun endpoint(address: String): Endpoint {
            val normalized = address.trim().trimEnd('/').toHttpUrl()
            Endpoint(normalized.toString()).baseUrl()
            return Endpoint(normalized.toString(), endpointKind(normalized.toString()), normalized.port, normalized.scheme)
        }

        private fun String.encodeSegment() = "https://example.com/".toHttpUrl().newBuilder().addPathSegment(this).build().encodedPath.removePrefix("/")
        private fun JsonObject.text(key: String) = this[key]?.jsonPrimitive?.contentOrNull
    }
}

private val Probing = CoreText.of(Key.Probing)
