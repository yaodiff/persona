package persona.feature.chat

import android.app.DownloadManager
import android.content.Context
import android.content.res.Resources
import android.net.Uri
import android.os.Environment
import android.os.SystemClock
import android.webkit.CookieManager
import android.webkit.URLUtil
import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import persona.core.designsystem.FeedbackStyle
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.InetAddress
import java.net.URI
import java.net.URL
import java.nio.charset.Charset
import java.nio.charset.StandardCharsets

/** A file download emitted by an active, visible in-app WebView. */
internal data class ChatWebDownloadRequest(
    val url: String,
    val userAgent: String,
    val contentDisposition: String,
    val mimeType: String,
    val contentLength: Long,
    val referer: String? = null,
)

/** Outcome of handling an in-app WebView file download. */
internal sealed interface ChatWebDownloadResult {
    data class Started(val fileName: String, val downloadId: Long) : ChatWebDownloadResult
    data class OpenedInBrowser(val url: String) : ChatWebDownloadResult
    data object LoopbackUnsupported : ChatWebDownloadResult
    data class TooLarge(val maxBytes: Long = MAX_WEB_DOWNLOAD_BYTES) : ChatWebDownloadResult
    data object Refused : ChatWebDownloadResult
    data class Failed(val fileName: String?) : ChatWebDownloadResult
}

/** Strategy that starts a WebView file download; production runs preflight and enqueue on [Dispatchers.IO]. */
internal fun interface ChatWebDownloader {
    suspend fun download(context: Context, request: ChatWebDownloadRequest, isForwarded: Boolean): ChatWebDownloadResult

    companion object {
        val Platform = ChatWebDownloader { context, request, isForwarded ->
            withContext(Dispatchers.IO) {
                startWebDownload(
                    context = context,
                    request = request,
                    isForwarded = isForwarded,
                    isCancelled = { !isActive },
                )
            }
        }
    }
}

/** Which downloader [ChatLinkHost] invokes for in-app WebView downloads. */
internal val LocalChatWebDownloader = staticCompositionLocalOf { ChatWebDownloader.Platform }

/** DNS resolver used during download preflight so tests can verify loopback resolution without real network traffic. */
internal fun interface WebDownloadHostResolver {
    fun resolve(host: String): List<InetAddress>

    companion object {
        val Platform = WebDownloadHostResolver { host -> InetAddress.getAllByName(host).toList() }
    }
}

/** Single non-redirect-following HTTP probe issued before handing a terminal URL to [DownloadManager]. */
internal data class WebDownloadProbeRequest(
    val url: String,
    val method: String,
    val cookie: String?,
    val userAgent: String?,
    val referer: String?,
)

/** Response headers returned by a single preflight probe hop. */
internal data class WebDownloadProbeResponse(
    val statusCode: Int,
    val location: String? = null,
    val contentLength: Long? = null,
    val contentDisposition: String? = null,
    val contentType: String? = null,
)

/** Strategy that performs a single HTTP probe hop (`instanceFollowRedirects = false`). */
internal fun interface WebDownloadProber {
    fun probe(request: WebDownloadProbeRequest): WebDownloadProbeResponse

    companion object {
        val Platform = WebDownloadProber(::platformProbeWebDownload)
    }
}

/** Static classification of a URL host before DNS resolution. */
internal enum class WebDownloadHostSafety {
    /** Standard domain name, canonical non-loopback dotted-quad IPv4, or valid non-loopback IPv6 literal. */
    Allowed,
    /** Any loopback or unspecified (`0.0.0.0/8`, `::`) spelling, including shorthand/octal/hex/integer/IPv4-mapped forms. */
    Loopback,
    /** Non-canonical numeric IPv4/IPv6-like or malformed host spelling; fail closed. */
    DisallowedNumeric,
}

/**
 * Validated request data prepared for Android's [DownloadManager]. Separated from system service invocation so
 * URL validation, redirect preflight, filename resolution, and header sanitization can be tested deterministically.
 */
internal data class PreparedWebDownload(
    val url: String,
    val fileName: String,
    val mimeType: String?,
    val cookie: String?,
    val userAgent: String?,
    val referer: String?,
)

/**
 * Per-WebView in-flight and short-cooldown deduplication guard. Prevents double-taps or abusive page scripts
 * from repeatedly enqueueing duplicate downloads for the same canonical URL or flooding concurrent preflights.
 */
internal class ChatWebDownloadGuard(
    private val cooldownMs: Long = DEFAULT_DOWNLOAD_COOLDOWN_MS,
    private val maxInFlight: Int = MAX_CONCURRENT_DOWNLOADS_PER_VIEW,
    private val nowMs: () -> Long = SystemClock::elapsedRealtime,
) {
    private val inFlight = mutableSetOf<String>()
    private val recent = LinkedHashMap<String, Long>()

    @Synchronized
    fun tryStart(url: String): String? {
        val now = nowMs()
        pruneExpired(now)
        val key = canonicalDownloadKey(url)
        if (key in inFlight) return null
        val lastCompleted = recent[key]
        if (lastCompleted != null && now - lastCompleted < cooldownMs) return null
        if (inFlight.size >= maxInFlight) return null
        inFlight += key
        return key
    }

    @Synchronized
    fun finish(key: String, applyCooldown: Boolean = true) {
        inFlight.remove(key)
        if (applyCooldown) {
            val now = nowMs()
            pruneExpired(now)
            recent[key] = now
            while (recent.size > MAX_RECENT_DOWNLOAD_ENTRIES) {
                val oldest = recent.entries.firstOrNull()?.key ?: break
                recent.remove(oldest)
            }
        }
    }

    @Synchronized
    fun clear() {
        inFlight.clear()
        recent.clear()
    }

    private fun pruneExpired(now: Long) {
        val iter = recent.entries.iterator()
        while (iter.hasNext()) {
            if (now - iter.next().value >= cooldownMs) iter.remove()
        }
    }
}

/**
 * Validates and enqueues an `http(s)` WebView download into Android's system [DownloadManager] targeting public
 * [Environment.DIRECTORY_DOWNLOADS] with completion notifications.
 *
 * Before enqueueing, validates the host statically (canonicalizing IPv6/IPv4-mapped and rejecting non-canonical
 * numeric IPv4 forms fail-closed), resolves DNS to block loopback/unspecified destinations, manually follows up to
 * [MAX_DOWNLOAD_REDIRECTS] hops (`instanceFollowRedirects = false`) re-checking every hop, enforces the
 * [maxDownloadBytes] size cap on known lengths, and scopes cookies to same-origin hops only.
 *
 * Falls back to the system browser only when [DownloadManager] itself is unavailable or rejects an already verified
 * terminal URL.
 */
internal fun startWebDownload(
    context: Context,
    request: ChatWebDownloadRequest,
    isForwarded: Boolean = false,
    cookieForUrl: (String) -> String? = ::platformCookieForUrl,
    enqueue: (Context, PreparedWebDownload) -> Long? = ::platformEnqueueDownload,
    openInBrowser: (Context, String) -> Boolean = ::openInSystemBrowser,
    hostResolver: WebDownloadHostResolver = WebDownloadHostResolver.Platform,
    prober: WebDownloadProber = WebDownloadProber.Platform,
    maxDownloadBytes: Long = MAX_WEB_DOWNLOAD_BYTES,
    isCancelled: () -> Boolean = { false },
): ChatWebDownloadResult {
    val initialUrl = when (val initial = validateDownloadCandidate(request.url)) {
        is UrlValidation.Valid -> initial.url
        UrlValidation.Loopback -> return ChatWebDownloadResult.LoopbackUnsupported
        UrlValidation.Refused -> return ChatWebDownloadResult.Refused
    }
    if (request.contentLength > maxDownloadBytes) {
        return ChatWebDownloadResult.TooLarge(maxDownloadBytes)
    }

    val userAgent = sanitizeHeaderValue(request.userAgent)
    val initialCookie = sanitizeHeaderValue(runCatching { cookieForUrl(initialUrl) }.getOrNull())
    val preflight = runDownloadPreflight(
        initialUrl = initialUrl,
        initialCookie = initialCookie,
        cookieForUrl = cookieForUrl,
        userAgent = userAgent,
        referer = request.referer,
        isForwarded = isForwarded,
        hostResolver = hostResolver,
        prober = prober,
        isCancelled = isCancelled,
    )
    val verified = when (preflight) {
        is PreflightOutcome.Ready -> preflight
        PreflightOutcome.Loopback -> return ChatWebDownloadResult.LoopbackUnsupported
        PreflightOutcome.Refused -> return ChatWebDownloadResult.Refused
        PreflightOutcome.Failed -> return ChatWebDownloadResult.Failed(null)
    }

    val probedLength = verified.contentLength
    if (probedLength != null && probedLength > maxDownloadBytes) {
        return ChatWebDownloadResult.TooLarge(maxDownloadBytes)
    }

    val finalUrl = verified.finalUrl
    val baseMimeType = sanitizeHeaderValue(request.mimeType)?.substringBefore(';')?.trim()?.takeIf { it.isNotEmpty() }
        ?: sanitizeHeaderValue(verified.contentType)?.substringBefore(';')?.trim()?.takeIf { it.isNotEmpty() }
    val effectiveDisposition = request.contentDisposition.takeIf { it.isNotBlank() }
        ?: verified.contentDisposition.orEmpty()
    val fileName = resolveDownloadFileName(finalUrl, effectiveDisposition, baseMimeType.orEmpty())
    val finalCookie = when {
        finalUrl == initialUrl -> initialCookie
        sameWebOrigin(finalUrl, initialUrl) -> sanitizeHeaderValue(runCatching { cookieForUrl(finalUrl) }.getOrNull())
        else -> null
    }
    val prepared = PreparedWebDownload(
        url = finalUrl,
        fileName = fileName,
        mimeType = baseMimeType,
        cookie = finalCookie,
        userAgent = userAgent,
        referer = if (isForwarded) null else safeRefererHeader(request.referer, finalUrl),
    )

    if (isCancelled()) throw CancellationException("Web download cancelled before enqueue")
    val downloadId = runCatching { enqueue(context, prepared) }.getOrNull()
    if (downloadId != null && downloadId >= 0L) {
        return ChatWebDownloadResult.Started(fileName, downloadId)
    }
    if (isCancelled()) throw CancellationException("Web download cancelled before browser fallback")
    return if (openInBrowser(context, finalUrl)) {
        ChatWebDownloadResult.OpenedInBrowser(finalUrl)
    } else {
        ChatWebDownloadResult.Failed(fileName)
    }
}

/** Runs [downloader] and emits a localized user-facing notice describing the outcome. */
internal suspend fun handleWebDownload(
    context: Context,
    resources: Resources,
    downloader: ChatWebDownloader,
    request: ChatWebDownloadRequest,
    isForwarded: Boolean,
    onNotice: (String, FeedbackStyle) -> Unit,
): ChatWebDownloadResult {
    val result = downloader.download(context, request, isForwarded)
    val (notice, style) = when (result) {
        is ChatWebDownloadResult.Started ->
            resources.getString(R.string.chat_web_download_started, result.fileName) to FeedbackStyle.Success
        is ChatWebDownloadResult.OpenedInBrowser ->
            resources.getString(R.string.chat_web_download_opened_browser) to FeedbackStyle.Info
        ChatWebDownloadResult.LoopbackUnsupported ->
            resources.getString(R.string.chat_web_download_loopback_unsupported) to FeedbackStyle.Error
        is ChatWebDownloadResult.TooLarge -> {
            val maxMb = (result.maxBytes / BYTES_PER_MIB).coerceAtLeast(1L).toInt()
            resources.getString(R.string.chat_web_download_too_large, maxMb) to FeedbackStyle.Error
        }
        ChatWebDownloadResult.Refused ->
            resources.getString(R.string.chat_web_cannot_open) to FeedbackStyle.Error
        is ChatWebDownloadResult.Failed ->
            resources.getString(R.string.chat_web_download_failed) to FeedbackStyle.Error
    }
    onNotice(notice, style)
    return result
}

/**
 * True when [url] statically targets this device's loopback or unspecified interface (`localhost`, `*.localhost`,
 * `127.0.0.0/8`, `0.0.0.0/8`, `::1`, `::`, IPv4-mapped/embedded IPv6 loopback, and shorthand/integer/hex/octal
 * IPv4 encodings).
 */
internal fun isLoopbackWebUrl(url: String): Boolean =
    classifyWebDownloadHost(url) == WebDownloadHostSafety.Loopback

/**
 * Classifies [url]'s host statically without performing DNS:
 * - Parses full/compressed IPv6 literals (including zone stripping and IPv4-mapped/compatible/translated/NAT64 forms),
 *   failing closed on any malformed bracketed/colon host.
 * - Detects WHATWG/POSIX `inet_aton` numeric IPv4 spellings (shorthand, integer, hex, octal, leading zeros),
 *   classifying `127.0.0.0/8` and `0.0.0.0/8` as [WebDownloadHostSafety.Loopback] and any other non-canonical or
 *   malformed numeric host as [WebDownloadHostSafety.DisallowedNumeric].
 * - Preserves standard domain names, canonical dotted-quad non-loopback IPv4 (including LAN/Tailscale), and valid
 *   non-loopback IPv6 literals as [WebDownloadHostSafety.Allowed].
 */
internal fun classifyWebDownloadHost(url: String): WebDownloadHostSafety {
    val rawHost = extractRawHost(url) ?: return WebDownloadHostSafety.DisallowedNumeric
    if (rawHost.isEmpty()) return WebDownloadHostSafety.DisallowedNumeric

    if (rawHost.startsWith('[') || rawHost.endsWith(']') || ':' in rawHost) {
        if (rawHost.startsWith('[') != rawHost.endsWith(']')) return WebDownloadHostSafety.DisallowedNumeric
        val inner = rawHost.trim('[', ']').substringBefore('%')
        return classifyIpv6Host(inner)
    }

    val hadTrailingDot = rawHost.endsWith('.')
    val host = if (hadTrailingDot) rawHost.dropLast(1) else rawHost
    if (host.isEmpty() || host.endsWith('.')) return WebDownloadHostSafety.DisallowedNumeric

    if (host in loopbackHostNames || host.endsWith(".localhost")) {
        return WebDownloadHostSafety.Loopback
    }

    val parts = host.split('.')
    if (parts.any { it.isEmpty() }) return WebDownloadHostSafety.DisallowedNumeric

    if (endsInIpv4Number(parts)) {
        if (!hadTrailingDot) {
            val canonical = parseCanonicalIpv4(parts)
            if (canonical != null) {
                return if (isLoopbackOrUnspecifiedIpv4(canonical)) {
                    WebDownloadHostSafety.Loopback
                } else {
                    WebDownloadHostSafety.Allowed
                }
            }
        }
        val numericIpv4 = parseNonCanonicalIpv4(parts)
        return when {
            numericIpv4 != null && isLoopbackOrUnspecifiedIpv4(numericIpv4) -> WebDownloadHostSafety.Loopback
            else -> WebDownloadHostSafety.DisallowedNumeric
        }
    }

    return WebDownloadHostSafety.Allowed
}

/** True when [first] and [second] share the same `(scheme, host, effectivePort)` origin. */
internal fun sameWebOrigin(first: String, second: String): Boolean {
    val a = runCatching { URI(first.trim()) }.getOrNull() ?: return false
    val b = runCatching { URI(second.trim()) }.getOrNull() ?: return false
    val schemeA = a.scheme?.lowercase() ?: return false
    val schemeB = b.scheme?.lowercase() ?: return false
    if (schemeA != schemeB) return false
    val hostA = a.host?.lowercase()?.trimEnd('.') ?: return false
    val hostB = b.host?.lowercase()?.trimEnd('.') ?: return false
    if (hostA != hostB) return false
    return effectivePort(schemeA, a.port) == effectivePort(schemeB, b.port)
}

/** Canonicalizes [url] for deduplication: lowercases scheme/host, drops default port and `#fragment`. */
internal fun canonicalDownloadKey(url: String): String {
    val trimmed = url.trim()
    val uri = runCatching { URI(trimmed) }.getOrNull() ?: return trimmed.substringBefore('#')
    val scheme = uri.scheme?.lowercase() ?: return trimmed.substringBefore('#')
    val host = uri.host?.lowercase()?.trimEnd('.') ?: return trimmed.substringBefore('#')
    val port = uri.port
    val portPart = when {
        port <= 0 -> ""
        scheme == "http" && port == 80 -> ""
        scheme == "https" && port == 443 -> ""
        else -> ":$port"
    }
    val path = uri.rawPath?.takeIf { it.isNotEmpty() } ?: "/"
    val query = uri.rawQuery?.let { "?$it" }.orEmpty()
    return "$scheme://$host$portPart$path$query"
}

/**
 * Resolves a safe public download filename, preferring RFC 5987 `filename*=` when present, then falling back
 * to [URLUtil.guessFileName], stripping path traversal and illegal filesystem characters, and capping the result
 * at [MAX_FILENAME_UTF8_BYTES] UTF-8 bytes on Unicode code-point boundaries.
 */
internal fun resolveDownloadFileName(url: String, contentDisposition: String, mimeType: String): String {
    val baseMime = mimeType.substringBefore(';').trim()
    val rfc5987 = parseRfc5987FileName(contentDisposition)?.let(::sanitizeDownloadFileName)
    if (rfc5987 != null) return ensureApkExtension(rfc5987, baseMime)
    val guessed = runCatching {
        URLUtil.guessFileName(url, contentDisposition.takeIf { it.isNotBlank() }, baseMime.takeIf { it.isNotEmpty() })
    }.getOrNull()?.let(::sanitizeDownloadFileName)
    if (guessed != null) {
        val adjusted = if (
            baseMime.equals(APK_MIME_TYPE, ignoreCase = true) &&
            guessed.endsWith(".bin", ignoreCase = true) &&
            !url.substringBefore("?").substringBefore("#").endsWith(".bin", ignoreCase = true) &&
            !contentDisposition.contains(".bin", ignoreCase = true)
        ) {
            guessed.dropLast(4) + ".apk"
        } else {
            guessed
        }
        return ensureApkExtension(adjusted, baseMime)
    }
    return if (baseMime.equals(APK_MIME_TYPE, ignoreCase = true)) "download.apk" else "download"
}

/** Validates and normalizes a `Referer` header, stripping fragments and blocking loopback or HTTPS->HTTP leaks. */
internal fun safeRefererHeader(referer: String?, downloadUrl: String): String? {
    val raw = sanitizeHeaderValue(referer) ?: return null
    val safeReferer = validatedDownloadUrl(raw) ?: return null
    if (classifyWebDownloadHost(safeReferer) != WebDownloadHostSafety.Allowed) return null
    val refererScheme = runCatching { URI(safeReferer).scheme?.lowercase() }.getOrNull() ?: return null
    val downloadScheme = runCatching { URI(downloadUrl).scheme?.lowercase() }.getOrNull() ?: return null
    if (refererScheme == "https" && downloadScheme == "http") return null
    return safeReferer.substringBefore('#').trim().takeIf { it.isNotEmpty() }
}

private sealed interface PreflightOutcome {
    data class Ready(
        val finalUrl: String,
        val contentLength: Long?,
        val contentDisposition: String?,
        val contentType: String?,
    ) : PreflightOutcome

    data object Loopback : PreflightOutcome
    data object Refused : PreflightOutcome
    data object Failed : PreflightOutcome
}

private fun runDownloadPreflight(
    initialUrl: String,
    initialCookie: String?,
    cookieForUrl: (String) -> String?,
    userAgent: String?,
    referer: String?,
    isForwarded: Boolean,
    hostResolver: WebDownloadHostResolver,
    prober: WebDownloadProber,
    isCancelled: () -> Boolean,
): PreflightOutcome {
    var currentUrl = initialUrl
    val visited = mutableSetOf<String>()

    for (hop in 0..MAX_DOWNLOAD_REDIRECTS) {
        if (isCancelled()) throw CancellationException("Web download preflight cancelled")
        if (!visited.add(canonicalDownloadKey(currentUrl))) return PreflightOutcome.Refused

        val dnsCheck = verifyResolvedHostAddresses(currentUrl, hostResolver)
        when (dnsCheck) {
            HostResolutionOutcome.Safe -> Unit
            HostResolutionOutcome.Loopback -> return PreflightOutcome.Loopback
            HostResolutionOutcome.Failed -> return PreflightOutcome.Failed
        }

        val hopCookie = when {
            hop == 0 -> initialCookie
            sameWebOrigin(currentUrl, initialUrl) -> sanitizeHeaderValue(runCatching { cookieForUrl(currentUrl) }.getOrNull())
            else -> null
        }
        val hopReferer = if (isForwarded) null else safeRefererHeader(referer, currentUrl)
        val probe = probeSingleHop(
            url = currentUrl,
            cookie = hopCookie,
            userAgent = userAgent,
            referer = hopReferer,
            prober = prober,
            isCancelled = isCancelled,
        ) ?: return PreflightOutcome.Failed

        when {
            probe.statusCode in REDIRECT_STATUS_CODES -> {
                if (hop >= MAX_DOWNLOAD_REDIRECTS) return PreflightOutcome.Refused
                val rawLocation = probe.location?.trim()?.takeIf { it.isNotEmpty() } ?: return PreflightOutcome.Refused
                val resolved = runCatching { URI(currentUrl).resolve(rawLocation).toString() }.getOrNull()
                    ?: return PreflightOutcome.Refused
                when (val next = validateDownloadCandidate(resolved)) {
                    is UrlValidation.Valid -> currentUrl = next.url
                    UrlValidation.Loopback -> return PreflightOutcome.Loopback
                    UrlValidation.Refused -> return PreflightOutcome.Refused
                }
            }
            probe.statusCode in 200..299 -> {
                return PreflightOutcome.Ready(
                    finalUrl = currentUrl,
                    contentLength = probe.contentLength,
                    contentDisposition = probe.contentDisposition,
                    contentType = probe.contentType,
                )
            }
            else -> return PreflightOutcome.Failed
        }
    }
    return PreflightOutcome.Refused
}

private fun probeSingleHop(
    url: String,
    cookie: String?,
    userAgent: String?,
    referer: String?,
    prober: WebDownloadProber,
    isCancelled: () -> Boolean,
): WebDownloadProbeResponse? {
    if (isCancelled()) throw CancellationException("Web download probe cancelled")
    val head = runCatching {
        prober.probe(
            WebDownloadProbeRequest(
                url = url,
                method = "HEAD",
                cookie = cookie,
                userAgent = userAgent,
                referer = referer,
            ),
        )
    }.getOrElse { error ->
        if (error is CancellationException) throw error
        return null
    }
    if (head.statusCode !in HEAD_FALLBACK_STATUS_CODES) return head
    if (isCancelled()) throw CancellationException("Web download fallback probe cancelled")
    return runCatching {
        prober.probe(
            WebDownloadProbeRequest(
                url = url,
                method = "GET",
                cookie = cookie,
                userAgent = userAgent,
                referer = referer,
            ),
        )
    }.getOrElse { error ->
        if (error is CancellationException) throw error
        null
    }
}

private enum class HostResolutionOutcome { Safe, Loopback, Failed }

private fun verifyResolvedHostAddresses(
    url: String,
    hostResolver: WebDownloadHostResolver,
): HostResolutionOutcome {
    val rawHost = runCatching { URI(url).host }.getOrNull()?.lowercase()?.trim('[', ']')?.substringBefore('%')
        ?.trimEnd('.')
        ?: return HostResolutionOutcome.Failed
    if (rawHost.isEmpty()) return HostResolutionOutcome.Failed

    val literalBytes = parseCanonicalLiteralBytes(rawHost)
    val addresses = if (literalBytes != null) {
        val addr = runCatching { InetAddress.getByAddress(literalBytes) }.getOrNull() ?: return HostResolutionOutcome.Failed
        listOf(addr)
    } else {
        runCatching { hostResolver.resolve(rawHost) }.getOrNull()?.takeIf { it.isNotEmpty() }
            ?: return HostResolutionOutcome.Failed
    }

    return if (addresses.any(::isLoopbackOrUnspecifiedAddress)) {
        HostResolutionOutcome.Loopback
    } else {
        HostResolutionOutcome.Safe
    }
}

private fun parseCanonicalLiteralBytes(host: String): ByteArray? {
    if (':' in host) {
        val ipv6 = parseIpv6Literal(host) ?: return null
        return if (ipv6.canonical) ipv6.bytes else null
    }
    val parts = host.split('.')
    val ipv4 = parseCanonicalIpv4(parts) ?: return null
    return byteArrayOf(
        ((ipv4 ushr 24) and 0xFFL).toByte(),
        ((ipv4 ushr 16) and 0xFFL).toByte(),
        ((ipv4 ushr 8) and 0xFFL).toByte(),
        (ipv4 and 0xFFL).toByte(),
    )
}

private fun isLoopbackOrUnspecifiedAddress(address: InetAddress): Boolean {
    if (address.isLoopbackAddress || address.isAnyLocalAddress) return true
    val bytes = address.address ?: return true
    return when (bytes.size) {
        4 -> isLoopbackOrUnspecifiedIpv4Bytes(bytes[0], bytes[1], bytes[2], bytes[3])
        16 -> isLoopbackOrUnspecifiedIpv6Bytes(bytes)
        else -> true
    }
}

private sealed interface UrlValidation {
    data class Valid(val url: String) : UrlValidation
    data object Loopback : UrlValidation
    data object Refused : UrlValidation
}

private fun validateDownloadCandidate(rawUrl: String): UrlValidation {
    val trimmed = rawUrl.trim()
    val uri = runCatching { URI(trimmed) }.getOrNull() ?: return UrlValidation.Refused
    val scheme = uri.scheme?.lowercase()
    if (scheme != "http" && scheme != "https") return UrlValidation.Refused
    if (uri.rawUserInfo != null || uri.rawAuthority?.contains('@') == true) return UrlValidation.Refused

    when (classifyWebDownloadHost(trimmed)) {
        WebDownloadHostSafety.Loopback -> return UrlValidation.Loopback
        WebDownloadHostSafety.DisallowedNumeric -> return UrlValidation.Refused
        WebDownloadHostSafety.Allowed -> Unit
    }

    val safeUrl = validatedDownloadUrl(trimmed) ?: return UrlValidation.Refused
    return UrlValidation.Valid(safeUrl)
}

private fun extractRawHost(url: String): String? {
    val uri = runCatching { URI(url.trim()) }.getOrNull() ?: return null
    val scheme = uri.scheme?.lowercase()
    if (scheme != "http" && scheme != "https") return null
    uri.host?.lowercase()?.takeIf { it.isNotEmpty() }?.let { return it }
    val authority = uri.rawAuthority?.takeIf { it.isNotEmpty() } ?: return null
    if ('@' in authority) return null
    if (authority.startsWith('[')) {
        val close = authority.indexOf(']')
        if (close <= 1) return null
        val rest = authority.substring(close + 1)
        if (rest.isNotEmpty() && (!rest.startsWith(':') || !rest.substring(1).all { it in '0'..'9' })) return null
        return authority.substring(0, close + 1).lowercase()
    }
    val colon = authority.indexOf(':')
    if (colon >= 0) {
        val portStr = authority.substring(colon + 1)
        if (portStr.isNotEmpty() && !portStr.all { it in '0'..'9' }) return null
        return authority.substring(0, colon).lowercase().takeIf { it.isNotEmpty() }
    }
    return authority.lowercase()
}

private fun validatedDownloadUrl(rawUrl: String): String? {
    val safeUrl = ChatLinks.webUrl(rawUrl) ?: return null
    val javaUri = runCatching { URI(safeUrl) }.getOrNull() ?: return null
    val javaScheme = javaUri.scheme?.lowercase() ?: return null
    val javaHost = javaUri.host?.lowercase()?.takeIf { it.isNotEmpty() } ?: return null
    val androidUri = runCatching { Uri.parse(safeUrl) }.getOrNull() ?: return null
    if (androidUri.scheme?.lowercase() != javaScheme) return null
    if (androidUri.userInfo != null) return null
    val androidHost = androidUri.host?.lowercase()?.takeIf { it.isNotEmpty() } ?: return null
    if (androidHost.trim('[', ']') != javaHost.trim('[', ']')) return null
    return safeUrl
}

private fun classifyIpv6Host(inner: String): WebDownloadHostSafety {
    val parsed = parseIpv6Literal(inner) ?: return WebDownloadHostSafety.DisallowedNumeric
    if (isLoopbackOrUnspecifiedIpv6Bytes(parsed.bytes)) return WebDownloadHostSafety.Loopback
    return if (parsed.canonical) WebDownloadHostSafety.Allowed else WebDownloadHostSafety.DisallowedNumeric
}

private data class ParsedIpv6(val bytes: ByteArray, val canonical: Boolean)

private fun parseIpv6Literal(input: String): ParsedIpv6? {
    if (input.isEmpty() || ":::" in input) return null
    val doubleColon = input.indexOf("::")
    if (doubleColon >= 0 && input.indexOf("::", doubleColon + 2) >= 0) return null

    val leftParts: List<String>
    val rightParts: List<String>
    val hasCompression = doubleColon >= 0
    if (hasCompression) {
        val left = input.substring(0, doubleColon)
        val right = input.substring(doubleColon + 2)
        leftParts = if (left.isEmpty()) emptyList() else left.split(':')
        rightParts = if (right.isEmpty()) emptyList() else right.split(':')
        if (leftParts.any { it.isEmpty() } || rightParts.any { it.isEmpty() }) return null
    } else {
        leftParts = input.split(':')
        rightParts = emptyList()
        if (leftParts.any { it.isEmpty() }) return null
    }

    var canonical = true
    var tailIpv4: Long? = null
    val effectiveLeft: List<String>
    val effectiveRight: List<String>

    val lastSegment = if (hasCompression) rightParts.lastOrNull() else leftParts.lastOrNull()
    if (lastSegment != null && '.' in lastSegment) {
        val v4Parts = lastSegment.split('.')
        val canonicalV4 = parseCanonicalIpv4(v4Parts)
        if (canonicalV4 != null) {
            tailIpv4 = canonicalV4
        } else {
            val nonCanonicalV4 = parseNonCanonicalIpv4(v4Parts) ?: return null
            tailIpv4 = nonCanonicalV4
            canonical = false
        }
        if (hasCompression) {
            effectiveLeft = leftParts
            effectiveRight = rightParts.dropLast(1)
        } else {
            effectiveLeft = leftParts.dropLast(1)
            effectiveRight = emptyList()
        }
    } else {
        effectiveLeft = leftParts
        effectiveRight = rightParts
    }

    val leftGroups = parseIpv6HexGroups(effectiveLeft) ?: return null
    val rightGroups = parseIpv6HexGroups(effectiveRight) ?: return null
    val ipv4GroupCount = if (tailIpv4 != null) 2 else 0
    val explicitCount = leftGroups.size + rightGroups.size + ipv4GroupCount
    val zeroGroups = if (hasCompression) {
        if (explicitCount >= 8) return null
        8 - explicitCount
    } else {
        if (explicitCount != 8) return null
        0
    }

    val bytes = ByteArray(16)
    var pos = 0
    fun writeGroup(value: Int) {
        bytes[pos++] = ((value ushr 8) and 0xFF).toByte()
        bytes[pos++] = (value and 0xFF).toByte()
    }
    for (group in leftGroups) writeGroup(group)
    repeat(zeroGroups) { writeGroup(0) }
    for (group in rightGroups) writeGroup(group)
    if (tailIpv4 != null) {
        writeGroup(((tailIpv4 ushr 16) and 0xFFFFL).toInt())
        writeGroup((tailIpv4 and 0xFFFFL).toInt())
    }
    return ParsedIpv6(bytes, canonical)
}

private fun parseIpv6HexGroups(groups: List<String>): IntArray? {
    val result = IntArray(groups.size)
    for (i in groups.indices) {
        val part = groups[i]
        if (part.isEmpty() || part.length > 4) return null
        var value = 0
        for (ch in part) {
            val digit = Character.digit(ch, 16)
            if (digit < 0) return null
            value = (value shl 4) or digit
        }
        result[i] = value
    }
    return result
}

private fun isLoopbackOrUnspecifiedIpv6Bytes(bytes: ByteArray): Boolean {
    if (bytes.size != 16) return true
    val allFirst12Zero = (0 until 12).all { bytes[it] == 0.toByte() }
    if (allFirst12Zero) {
        // :: (unspecified), ::1 (loopback), and deprecated IPv4-compatible ::w.x.y.z
        val allZero = bytes[12] == 0.toByte() && bytes[13] == 0.toByte() && bytes[14] == 0.toByte() && bytes[15] == 0.toByte()
        val isLoopback1 = bytes[12] == 0.toByte() && bytes[13] == 0.toByte() && bytes[14] == 0.toByte() && bytes[15] == 1.toByte()
        if (allZero || isLoopback1) return true
        return isLoopbackOrUnspecifiedIpv4Bytes(bytes[12], bytes[13], bytes[14], bytes[15])
    }
    // IPv4-mapped ::ffff:0:0/96
    val isIpv4Mapped = (0 until 10).all { bytes[it] == 0.toByte() } &&
        bytes[10] == 0xFF.toByte() && bytes[11] == 0xFF.toByte()
    if (isIpv4Mapped) {
        return isLoopbackOrUnspecifiedIpv4Bytes(bytes[12], bytes[13], bytes[14], bytes[15])
    }
    // IPv4-translated ::ffff:0:0:0/96 (RFC 2765)
    val isIpv4Translated = (0 until 8).all { bytes[it] == 0.toByte() } &&
        bytes[8] == 0xFF.toByte() && bytes[9] == 0xFF.toByte() &&
        bytes[10] == 0.toByte() && bytes[11] == 0.toByte()
    if (isIpv4Translated) {
        return isLoopbackOrUnspecifiedIpv4Bytes(bytes[12], bytes[13], bytes[14], bytes[15])
    }
    // NAT64 well-known prefix 64:ff9b::/96 (RFC 6052)
    val isNat64WellKnown = bytes[0] == 0x00.toByte() && bytes[1] == 0x64.toByte() &&
        bytes[2] == 0xFF.toByte() && bytes[3] == 0x9B.toByte() &&
        (4 until 12).all { bytes[it] == 0.toByte() }
    if (isNat64WellKnown) {
        return isLoopbackOrUnspecifiedIpv4Bytes(bytes[12], bytes[13], bytes[14], bytes[15])
    }
    return false
}

private fun endsInIpv4Number(parts: List<String>): Boolean {
    val last = parts.lastOrNull() ?: return false
    if (last.isEmpty()) return false
    if (last.all { it in '0'..'9' }) return true
    if (last.startsWith("0x")) {
        val hex = last.substring(2)
        return hex.all { it in '0'..'9' || it in 'a'..'f' }
    }
    return false
}

private fun parseCanonicalIpv4(parts: List<String>): Long? {
    if (parts.size != 4) return null
    var value = 0L
    for (part in parts) {
        if (part.isEmpty() || part.length > 3) return null
        if (part.length > 1 && part[0] == '0') return null
        if (!part.all { it in '0'..'9' }) return null
        val octet = part.toIntOrNull() ?: return null
        if (octet !in 0..255) return null
        value = (value shl 8) or octet.toLong()
    }
    return value
}

/** Parses WHATWG / POSIX `inet_aton` 1-to-4-part decimal, octal, or hex IPv4 spellings into an unsigned 32-bit value. */
private fun parseNonCanonicalIpv4(parts: List<String>): Long? {
    if (parts.isEmpty() || parts.size > 4) return null
    val numbers = LongArray(parts.size)
    for (i in parts.indices) {
        numbers[i] = parseIpv4NumberPart(parts[i]) ?: return null
    }
    for (i in 0 until numbers.size - 1) {
        if (numbers[i] > 255L) return null
    }
    val maxLast = when (numbers.size) {
        1 -> 0xFFFFFFFFL
        2 -> 0xFFFFFFL
        3 -> 0xFFFFL
        4 -> 0xFFL
        else -> return null
    }
    val last = numbers.last()
    if (last > maxLast) return null

    var result = last
    for (i in 0 until numbers.size - 1) {
        result += numbers[i] shl (8 * (3 - i))
    }
    return result and 0xFFFFFFFFL
}

private fun parseIpv4NumberPart(part: String): Long? {
    if (part.isEmpty()) return null
    return when {
        part.startsWith("0x") -> {
            val digits = part.substring(2)
            if (digits.isEmpty()) return 0L
            if (!digits.all { it in '0'..'9' || it in 'a'..'f' }) return null
            val trimmed = digits.trimStart('0').ifEmpty { "0" }
            if (trimmed.length > 8) return null
            trimmed.toLongOrNull(16)
        }
        part.length > 1 && part[0] == '0' -> {
            val digits = part.substring(1)
            if (!digits.all { it in '0'..'7' }) return null
            val trimmed = digits.trimStart('0').ifEmpty { "0" }
            if (trimmed.length > 11) return null
            trimmed.toLongOrNull(8)?.takeIf { it <= 0xFFFFFFFFL }
        }
        else -> {
            if (!part.all { it in '0'..'9' }) return null
            if (part.length > 10) return null
            part.toLongOrNull(10)?.takeIf { it <= 0xFFFFFFFFL }
        }
    }
}

private fun isLoopbackOrUnspecifiedIpv4(ipv4: Long): Boolean {
    val firstOctet = ((ipv4 ushr 24) and 0xFFL).toInt()
    return firstOctet == 127 || firstOctet == 0
}

private fun isLoopbackOrUnspecifiedIpv4Bytes(
    b0: Byte,
    @Suppress("UNUSED_PARAMETER") b1: Byte,
    @Suppress("UNUSED_PARAMETER") b2: Byte,
    @Suppress("UNUSED_PARAMETER") b3: Byte,
): Boolean {
    val first = b0.toInt() and 0xFF
    return first == 127 || first == 0
}

private fun effectivePort(scheme: String, port: Int): Int = when {
    port > 0 -> port
    scheme == "https" -> 443
    scheme == "http" -> 80
    else -> -1
}

private fun sanitizeHeaderValue(value: String?): String? {
    val cleaned = value?.replace(HEADER_CONTROL_CHARS, "")?.trim()
    return cleaned?.takeIf { it.isNotEmpty() }
}

private fun sanitizeDownloadFileName(raw: String): String? {
    val leaf = raw.replace('\\', '/').substringAfterLast('/')
    val cleaned = buildString(leaf.length) {
        var i = 0
        while (i < leaf.length) {
            val cp = leaf.codePointAt(i)
            val charCount = Character.charCount(cp)
            when {
                cp < 0x20 || cp == 0x7F || cp in 0xD800..0xDFFF -> Unit
                cp < 128 && cp.toChar() in ILLEGAL_FILENAME_CHARS -> append('_')
                else -> appendCodePoint(cp)
            }
            i += charCount
        }
    }.trim().trimStart('.').trim()
    if (cleaned.isEmpty() || cleaned == "." || cleaned == "..") return null
    return truncateFileNameUtf8(cleaned, MAX_FILENAME_UTF8_BYTES)
}

private fun truncateFileNameUtf8(name: String, maxBytes: Int): String? {
    if (name.toByteArray(StandardCharsets.UTF_8).size <= maxBytes) return name
    val dot = name.lastIndexOf('.')
    if (dot > 0) {
        val ext = name.substring(dot)
        val extBytes = ext.toByteArray(StandardCharsets.UTF_8).size
        if (ext.length > 1 && extBytes <= MAX_EXTENSION_UTF8_BYTES && extBytes < maxBytes) {
            val stem = truncateToUtf8Bytes(name.substring(0, dot), maxBytes - extBytes).trimEnd()
            if (stem.isNotEmpty() && stem != "." && stem != "..") {
                return stem + ext
            }
        }
    }
    return truncateToUtf8Bytes(name, maxBytes).trim().trimStart('.').trim().takeIf {
        it.isNotEmpty() && it != "." && it != ".."
    }
}

private fun truncateToUtf8Bytes(text: String, maxBytes: Int): String {
    if (maxBytes <= 0) return ""
    val out = StringBuilder(text.length.coerceAtMost(maxBytes))
    var usedBytes = 0
    var i = 0
    while (i < text.length) {
        val cp = text.codePointAt(i)
        val charCount = Character.charCount(cp)
        if (cp in 0xD800..0xDFFF) {
            i += charCount
            continue
        }
        val cpBytes = utf8ByteCount(cp)
        if (usedBytes + cpBytes > maxBytes) break
        out.appendCodePoint(cp)
        usedBytes += cpBytes
        i += charCount
    }
    return out.toString()
}

private fun utf8ByteCount(codePoint: Int): Int = when {
    codePoint <= 0x7F -> 1
    codePoint <= 0x7FF -> 2
    codePoint <= 0xFFFF -> 3
    else -> 4
}

private fun ensureApkExtension(fileName: String, baseMime: String): String {
    if (!baseMime.equals(APK_MIME_TYPE, ignoreCase = true)) return fileName
    if (fileName.contains('.')) return fileName
    return truncateFileNameUtf8("$fileName.apk", MAX_FILENAME_UTF8_BYTES) ?: "download.apk"
}

private fun parseRfc5987FileName(contentDisposition: String): String? {
    if (contentDisposition.isBlank()) return null
    for (part in contentDisposition.split(';')) {
        val trimmed = part.trim()
        val eq = trimmed.indexOf('=')
        if (eq <= 0) continue
        val key = trimmed.substring(0, eq).trim()
        if (!key.equals("filename*", ignoreCase = true)) continue
        var value = trimmed.substring(eq + 1).trim()
        if (value.length >= 2 && value.first() == '"' && value.last() == '"') {
            value = value.substring(1, value.length - 1)
        }
        val firstQuote = value.indexOf('\'')
        val secondQuote = if (firstQuote >= 0) value.indexOf('\'', firstQuote + 1) else -1
        if (firstQuote < 0 || secondQuote < 0) continue
        val charsetName = value.substring(0, firstQuote).trim()
        val charset = when (charsetName.lowercase()) {
            "", "utf-8", "utf8" -> StandardCharsets.UTF_8
            "iso-8859-1", "latin1" -> StandardCharsets.ISO_8859_1
            "us-ascii", "ascii" -> StandardCharsets.US_ASCII
            else -> runCatching { Charset.forName(charsetName) }.getOrDefault(StandardCharsets.UTF_8)
        }
        val decoded = decodePercentBytes(value.substring(secondQuote + 1), charset)
        if (decoded.isNotBlank()) return decoded
    }
    return null
}

private fun decodePercentBytes(value: String, charset: Charset): String {
    val out = ByteArrayOutputStream(value.length)
    var i = 0
    while (i < value.length) {
        val ch = value[i]
        if (ch == '%' && i + 2 < value.length) {
            val hi = Character.digit(value[i + 1], 16)
            val lo = Character.digit(value[i + 2], 16)
            if (hi >= 0 && lo >= 0) {
                out.write((hi shl 4) or lo)
                i += 3
                continue
            }
        }
        out.write(ch.toString().toByteArray(charset))
        i++
    }
    return String(out.toByteArray(), charset)
}

internal fun parseContentRangeTotalLength(contentRange: String?): Long? {
    val total = contentRange?.trim()?.substringAfter('/', "")?.trim() ?: return null
    if (total.isEmpty() || total == "*") return null
    return total.toLongOrNull()?.takeIf { it > 0L }
}

private fun platformProbeWebDownload(request: WebDownloadProbeRequest): WebDownloadProbeResponse {
    val connection = (URL(request.url).openConnection() as HttpURLConnection).apply {
        requestMethod = request.method
        instanceFollowRedirects = false
        useCaches = false
        connectTimeout = PREFLIGHT_TIMEOUT_MS
        readTimeout = PREFLIGHT_TIMEOUT_MS
        request.userAgent?.let { setRequestProperty("User-Agent", it) }
        request.cookie?.let { setRequestProperty("Cookie", it) }
        request.referer?.let { setRequestProperty("Referer", it) }
        if (request.method == "GET") {
            setRequestProperty("Range", "bytes=0-0")
        }
    }
    return try {
        val status = connection.responseCode
        val location = connection.getHeaderField("Location")?.trim()?.takeIf { it.isNotEmpty() }
        val contentLength = when (status) {
            HttpURLConnection.HTTP_PARTIAL -> parseContentRangeTotalLength(connection.getHeaderField("Content-Range"))
            else -> connection.getHeaderField("Content-Length")?.trim()?.toLongOrNull()?.takeIf { it > 0L }
        }
        WebDownloadProbeResponse(
            statusCode = status,
            location = location,
            contentLength = contentLength,
            contentDisposition = connection.getHeaderField("Content-Disposition"),
            contentType = connection.contentType,
        )
    } finally {
        connection.disconnect()
    }
}

private fun platformCookieForUrl(url: String): String? =
    runCatching { CookieManager.getInstance().getCookie(url) }.getOrNull()

private fun platformEnqueueDownload(context: Context, prepared: PreparedWebDownload): Long? {
    val manager = context.getSystemService(DownloadManager::class.java) ?: return null
    val uri = Uri.parse(prepared.url)
    val dmRequest = DownloadManager.Request(uri).apply {
        setTitle(prepared.fileName)
        setDescription(uri.host?.takeIf { it.isNotBlank() } ?: prepared.url)
        prepared.mimeType?.let { setMimeType(it) }
        prepared.cookie?.let { addRequestHeader("Cookie", it) }
        prepared.userAgent?.let { addRequestHeader("User-Agent", it) }
        prepared.referer?.let { addRequestHeader("Referer", it) }
        setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
        setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, prepared.fileName)
    }
    return manager.enqueue(dmRequest)
}

private val loopbackHostNames = setOf("localhost", "localhost.localdomain", "ip6-localhost", "ip6-loopback")
private val REDIRECT_STATUS_CODES = setOf(301, 302, 303, 307, 308)
private val HEAD_FALLBACK_STATUS_CODES = setOf(403, 405, 501)
private val HEADER_CONTROL_CHARS = Regex("[\r\n\u0000]")
private const val ILLEGAL_FILENAME_CHARS = ":*?\"<>|/\\"
/** Capped well below the 255-byte filesystem component limit to leave room for DownloadManager disambiguation suffixes. */
internal const val MAX_FILENAME_UTF8_BYTES = 180
private const val MAX_EXTENSION_UTF8_BYTES = 24
internal const val MAX_DOWNLOAD_REDIRECTS = 5
private const val PREFLIGHT_TIMEOUT_MS = 5_000
private const val BYTES_PER_MIB = 1024L * 1024L
internal const val MAX_WEB_DOWNLOAD_BYTES: Long = 512L * BYTES_PER_MIB
internal const val DEFAULT_DOWNLOAD_COOLDOWN_MS: Long = 3_000L
internal const val MAX_CONCURRENT_DOWNLOADS_PER_VIEW: Int = 3
private const val MAX_RECENT_DOWNLOAD_ENTRIES: Int = 64
private const val APK_MIME_TYPE = "application/vnd.android.package-archive"
