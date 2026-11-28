package persona.feature.chat

import android.app.DownloadManager
import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.webkit.CookieManager
import android.widget.FrameLayout
import androidx.compose.foundation.clickable
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.test.runTest
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import java.io.IOException
import java.net.InetAddress
import java.net.UnknownHostException
import java.nio.charset.StandardCharsets

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatWebDownloadsTest {
    @get:Rule val rule = createComposeRule()

    private val pageUrl = "https://example.invalid/releases/latest"
    private val apkUrl = "https://cdn.example.invalid/downloads/persona-release.apk"
    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val created = mutableListOf<RecordingWebView>()
    private lateinit var pool: ChatWebViewPool

    private val publicIpv4: InetAddress = InetAddress.getByAddress(byteArrayOf(198.toByte(), 51, 100, 10))
    private val loopbackIpv4: InetAddress = InetAddress.getByAddress(byteArrayOf(127, 0, 0, 1))
    private val unspecifiedIpv4: InetAddress = InetAddress.getByAddress(byteArrayOf(0, 0, 0, 0))
    private val loopbackIpv6: InetAddress = InetAddress.getByAddress(ByteArray(16).apply { this[15] = 1 })
    private val unspecifiedIpv6: InetAddress = InetAddress.getByAddress(ByteArray(16))

    private val publicResolver = WebDownloadHostResolver { listOf(publicIpv4) }
    private val okProber = WebDownloadProber { WebDownloadProbeResponse(statusCode = 200) }

    @Before fun setUp() {
        CookieManager.getInstance().removeAllCookies(null)
        pool = ChatWebViewPool(context, NoWebFeatures) { viewContext ->
            RecordingWebView(viewContext).also(created::add)
        }
    }

    @After fun tearDown() {
        pool.destroy()
    }

    @Test fun foregroundMountedWebViewForwardsValidDownloadWithMetadataAndReferer() {
        val view = pool.acquire(pageUrl) as RecordingWebView
        pool.activate(view)
        val container = FrameLayout(context)
        container.addView(view)

        val received = mutableListOf<ChatWebDownloadRequest>()
        view.onDownload = { received += it }

        simulateDownloadStart(
            view = view,
            url = apkUrl,
            userAgent = "Mozilla/5.0 PersonaAndroid",
            contentDisposition = "attachment; filename=\"persona-21.apk\"",
            mimeType = "application/vnd.android.package-archive",
            contentLength = 42_000L,
        )

        assertEquals(1, received.size)
        val req = received.single()
        assertEquals(apkUrl, req.url)
        assertEquals("Mozilla/5.0 PersonaAndroid", req.userAgent)
        assertEquals("attachment; filename=\"persona-21.apk\"", req.contentDisposition)
        assertEquals("application/vnd.android.package-archive", req.mimeType)
        assertEquals(42_000L, req.contentLength)
        assertEquals(pageUrl, req.referer)
    }

    @Test fun backgroundPreloadedDetachedPausedAndReleasedViewsNeverTriggerDownloads() {
        val extensionlessDownload = "https://example.invalid/export?id=99"
        pool.preload(extensionlessDownload)
        val preloaded = pool.existing(extensionlessDownload) as RecordingWebView
        val preloadedEvents = mutableListOf<ChatWebDownloadRequest>()
        preloaded.onDownload = { preloadedEvents += it }

        // Background preloaded view is paused, detached, and inactive: must suppress drive-by downloads.
        simulateDownloadStart(preloaded, extensionlessDownload)
        assertTrue(preloadedEvents.isEmpty())
        assertTrue(preloaded.suppressedBackgroundDownload)

        // Acquiring the preloaded download URL in the foreground reloads it cleanly.
        val acquired = pool.acquire(extensionlessDownload) as RecordingWebView
        assertEquals(listOf(extensionlessDownload, extensionlessDownload), acquired.loads)
        assertFalse(acquired.suppressedBackgroundDownload)

        // Active but detached (parent == null) still cannot start a download.
        pool.activate(acquired)
        acquired.onDownload = { preloadedEvents += it }
        simulateDownloadStart(acquired, apkUrl)
        assertTrue(preloadedEvents.isEmpty())

        // Mounted, then paused via pool.pause() cannot start a download.
        val container = FrameLayout(context)
        container.addView(acquired)
        pool.pause()
        simulateDownloadStart(acquired, apkUrl)
        assertTrue(preloadedEvents.isEmpty())

        // Resumed and mounted succeeds.
        pool.resume()
        simulateDownloadStart(acquired, apkUrl)
        assertEquals(1, preloadedEvents.size)

        // After release, callback is cleared, guard is cleared, and downloads are suppressed.
        assertNotNull(acquired.downloadGuard.tryStart(apkUrl))
        pool.release(acquired)
        assertNull(acquired.onDownload)
        assertNotNull("Expected release to clear downloadGuard state", acquired.downloadGuard.tryStart(apkUrl))
        simulateDownloadStart(acquired, apkUrl)
        assertEquals(1, preloadedEvents.size)
    }

    @Test fun directDownloadExtensionsAndNonCanonicalLoopbackSkipBackgroundPreload() {
        pool.preload("https://example.invalid/files/persona.apk")
        pool.preload("https://example.invalid/files/archive.zip")
        pool.preload("https://example.invalid/files/report.pdf")
        pool.preload("http://127.1/secret")
        pool.preload("http://2130706433/secret")
        pool.preload("http://[::ffff:7f00:1]/secret")
        pool.preload("http://0x08080808/secret")
        assertTrue(created.isEmpty())

        // Explicit foreground acquire still creates and loads normally.
        val view = pool.acquire("https://example.invalid/files/persona.apk") as RecordingWebView
        assertEquals(listOf("https://example.invalid/files/persona.apk"), view.loads)
    }

    @Test fun nonHttpSchemesAndCredentialUrlsAreRefusedInPoolAndDownloader() {
        val view = pool.acquire(pageUrl) as RecordingWebView
        pool.activate(view)
        FrameLayout(context).addView(view)

        val received = mutableListOf<ChatWebDownloadRequest>()
        view.onDownload = { received += it }

        val rejectedUrls = listOf(
            "file:///etc/passwd",
            "content://com.example.provider/secret",
            "javascript:alert(1)",
            "data:text/plain;base64,SGVsbG8=",
            "blob:https://example.invalid/1234",
            "persona://session/honor/abc",
            "https://user:pass@example.invalid/file.apk",
            "",
        )

        var enqueueCalls = 0
        var browserCalls = 0
        for (badUrl in rejectedUrls) {
            simulateDownloadStart(view, badUrl)
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest(badUrl, "UA", "", "application/octet-stream", -1L, pageUrl),
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = publicResolver,
                prober = okProber,
            )
            assertEquals("Expected Refused for $badUrl", ChatWebDownloadResult.Refused, result)
        }
        assertTrue(received.isEmpty())
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)
    }

    @Test fun canonicalAndNonCanonicalLoopbackAndUnspecifiedUrlsAreRejectedWithoutNetworkOrFallback() {
        val loopbackUrls = listOf(
            // Canonical localhost & IPv4/IPv6 loopback
            "http://localhost:4173/build.apk",
            "https://localhost:8443/report.pdf",
            "http://localhost./build.apk",
            "http://dev.localhost:5173/app.apk",
            "http://dev.localhost./app.apk",
            "http://localhost.localdomain/app.apk",
            "http://ip6-localhost/app.apk",
            "http://ip6-loopback/app.apk",
            "http://127.0.0.1:8080/bundle.zip",
            "http://127.0.0.42:9000/file.bin",
            "http://127.0.0.1./bundle.zip",
            "http://0.0.0.0:8080/file.bin",
            "http://${ipv4(0, 1, 2, 3)}/file.bin",
            // Non-canonical numeric IPv4 (shorthand, integer, hex, octal, leading zeros)
            "http://127.1/",
            "http://127.1:8080/secret.apk",
            "http://127.0.1/secret.apk",
            "http://2130706433/",
            "http://2130706433:8080/secret.apk",
            "http://0x7f000001/",
            "http://0x000000007f000001/",
            "http://0x7f.1/",
            "http://0x7f.0.0.1/",
            "http://127.0x00.0x00.0x01/",
            "http://0177.0.0.1/",
            "http://0177.1/",
            "http://017700000001/",
            "http://127.00.0.001/",
            "http://0/",
            "http://0x0/",
            "http://0.1/",
            "http://0000.0.0.0/",
            // IPv6 compressed, expanded, unspecified, IPv4-mapped, compatible, translated, NAT64
            "http://[::1]:3000/output.tar.gz",
            "http://[0:0:0:0:0:0:0:1]/output.tar.gz",
            "http://[0000:0000:0000:0000:0000:0000:0000:0001]/output.tar.gz",
            "http://[::]/output.tar.gz",
            "http://[0:0:0:0:0:0:0:0]/output.tar.gz",
            "http://[::ffff:7f00:1]/",
            "http://[::ffff:7f00:0001]/",
            "http://[::FFFF:7F00:1]/",
            "http://[0:0:0:0:0:ffff:7f00:1]/",
            "http://[::ffff:127.0.0.1]/",
            "http://[::ffff:127.0.0.42]:8080/app.apk",
            "http://[::ffff:0:0]/",
            "http://[::ffff:0.0.0.0]/",
            "http://[::127.0.0.1]/",
            "http://[::7f00:1]/",
            "http://[::ffff:0:127.0.0.1]/",
            "http://[64:ff9b::127.0.0.1]/",
            "http://[64:ff9b::7f00:1]/",
            "http://[::1%25lo]/",
        )

        var resolveCalls = 0
        var probeCalls = 0
        var enqueueCalls = 0
        var browserCalls = 0
        for (loopback in loopbackUrls) {
            assertTrue("Expected isLoopbackWebUrl=true for $loopback", isLoopbackWebUrl(loopback))
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest(loopback, "UA", "", "application/octet-stream", -1L, loopback),
                isForwarded = true,
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = WebDownloadHostResolver { resolveCalls++; listOf(publicIpv4) },
                prober = WebDownloadProber { probeCalls++; WebDownloadProbeResponse(200) },
            )
            assertEquals("Expected LoopbackUnsupported for $loopback", ChatWebDownloadResult.LoopbackUnsupported, result)
        }
        assertEquals(0, resolveCalls)
        assertEquals(0, probeCalls)
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)
    }

    @Test fun nonCanonicalNonLoopbackNumericHostsFailClosedWhileLegitimateHostsRemainAllowed() {
        val disallowedNumericUrls = listOf(
            "http://134744072/file.apk",         // integer 8.8.8.8
            "http://0x08080808/file.apk",        // hex 8.8.8.8
            "http://010.010.010.010/file.apk",   // octal 8.8.8.8
            "http://198.1/file.apk",             // shorthand 2-part IPv4
            "http://0177.0.0.09/file.apk",       // malformed octal digit
            "http://127.256.0.1/file.apk",       // out-of-range octet
            "http://4294967296/file.apk",        // > uint32
            "http://198.51.100.10./file.apk",    // trailing dot on IPv4
        )
        var enqueueCalls = 0
        var browserCalls = 0
        for (bad in disallowedNumericUrls) {
            assertEquals("Expected DisallowedNumeric for $bad", WebDownloadHostSafety.DisallowedNumeric, classifyWebDownloadHost(bad))
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest(bad, "UA", "", "application/octet-stream", -1L, pageUrl),
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = publicResolver,
                prober = okProber,
            )
            assertEquals("Expected Refused for $bad", ChatWebDownloadResult.Refused, result)
        }
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)

        // Normal public domains and canonical non-loopback LAN / Tailscale IPv4 & IPv6 must remain allowed.
        val allowedUrls = listOf(
            "https://cdn.example.invalid/app.apk",
            "https://127.example.invalid/app.apk",
            "https://0x7f.example.invalid/app.apk",
            "http://${ipv4(192, 168, 1, 10)}:8080/build.apk",
            "http://${ipv4(10, 0, 0, 5)}/file.zip",
            "http://100.64.0.1:9000/pkg.apk",
            "http://198.51.100.10/file.apk",
            "https://[2001:db8::1]/file.apk",
            "http://[fd7a:115c:a1e0::1]:8080/file.apk",
        )
        for (good in allowedUrls) {
            assertEquals("Expected Allowed for $good", WebDownloadHostSafety.Allowed, classifyWebDownloadHost(good))
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest(good, "UA", "", "application/octet-stream", -1L, pageUrl),
                cookieForUrl = { null },
                enqueue = { _, _ -> 10L },
                openInBrowser = { _, _ -> false },
                hostResolver = publicResolver,
                prober = okProber,
            )
            assertTrue("Expected Started for $good, got $result", result is ChatWebDownloadResult.Started)
        }
    }

    @Test fun dnsResolutionToLoopbackOrUnspecifiedOrFailureFailsClosed() {
        val dnsCases = listOf(
            "single IPv4 loopback" to listOf(loopbackIpv4),
            "single IPv6 loopback" to listOf(loopbackIpv6),
            "single IPv4 unspecified" to listOf(unspecifiedIpv4),
            "single IPv6 unspecified" to listOf(unspecifiedIpv6),
            "dual-record public plus loopback" to listOf(publicIpv4, loopbackIpv4),
        )
        var probeCalls = 0
        var enqueueCalls = 0
        var browserCalls = 0
        for ((label, resolved) in dnsCases) {
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest("https://localtest.example.invalid/app.apk", "UA", "", "application/octet-stream", -1L, pageUrl),
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = WebDownloadHostResolver { resolved },
                prober = WebDownloadProber { probeCalls++; WebDownloadProbeResponse(200) },
            )
            assertEquals("Expected LoopbackUnsupported for $label", ChatWebDownloadResult.LoopbackUnsupported, result)
        }

        // DNS failure (UnknownHostException or empty answer) fails closed without enqueueing or browser fallback.
        val dnsError = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/octet-stream", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = WebDownloadHostResolver { throw UnknownHostException("nxdomain") },
            prober = WebDownloadProber { probeCalls++; WebDownloadProbeResponse(200) },
        )
        assertTrue("Expected Failed on DNS error, got $dnsError", dnsError is ChatWebDownloadResult.Failed)

        val dnsEmpty = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/octet-stream", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = WebDownloadHostResolver { emptyList() },
            prober = WebDownloadProber { probeCalls++; WebDownloadProbeResponse(200) },
        )
        assertTrue("Expected Failed on empty DNS answer, got $dnsEmpty", dnsEmpty is ChatWebDownloadResult.Failed)

        assertEquals(0, probeCalls)
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)
    }

    @Test fun redirectsToLoopbackIllegalSchemeOrTooManyHopsAreBlocked() {
        val loopbackTargets = listOf(
            "http://127.0.0.1:8080/secret",
            "http://127.1/secret",
            "http://2130706433/secret",
            "http://[::ffff:7f00:1]/secret",
            "//127.0.0.1/secret",
            "https://localtest.example.invalid/secret",
        )
        val resolver = WebDownloadHostResolver { host ->
            if (host == "localtest.example.invalid") listOf(loopbackIpv4) else listOf(publicIpv4)
        }
        var enqueueCalls = 0
        var browserCalls = 0

        for (target in loopbackTargets) {
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest("https://redirect.example.invalid/dl", "UA", "", "application/octet-stream", -1L, pageUrl),
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = resolver,
                prober = WebDownloadProber { probe ->
                    if (probe.url == "https://redirect.example.invalid/dl") {
                        WebDownloadProbeResponse(statusCode = 302, location = target)
                    } else {
                        WebDownloadProbeResponse(statusCode = 200)
                    }
                },
            )
            assertEquals("Expected LoopbackUnsupported for redirect to $target", ChatWebDownloadResult.LoopbackUnsupported, result)
        }

        val refusedTargets = listOf(
            "file:///etc/passwd",
            "javascript:alert(1)",
            "https://user:pass@cdn.example.invalid/file.apk",
            "http://0x08080808/file.apk",
            "",
        )
        for (target in refusedTargets) {
            val result = startWebDownload(
                context = context,
                request = ChatWebDownloadRequest("https://redirect.example.invalid/dl", "UA", "", "application/octet-stream", -1L, pageUrl),
                enqueue = { _, _ -> enqueueCalls++; 1L },
                openInBrowser = { _, _ -> browserCalls++; true },
                hostResolver = publicResolver,
                prober = WebDownloadProber { WebDownloadProbeResponse(statusCode = 302, location = target) },
            )
            assertEquals("Expected Refused for redirect to '$target'", ChatWebDownloadResult.Refused, result)
        }

        // Redirect chain exceeding MAX_DOWNLOAD_REDIRECTS (5) is refused.
        val tooMany = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest("https://redirect.example.invalid/hop/0", "UA", "", "application/octet-stream", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = publicResolver,
            prober = WebDownloadProber { probe ->
                val hopNum = probe.url.substringAfterLast('/').toInt()
                WebDownloadProbeResponse(statusCode = 302, location = "/hop/${hopNum + 1}")
            },
        )
        assertEquals(ChatWebDownloadResult.Refused, tooMany)

        // Preflight IO failure fails closed without enqueueing or opening browser.
        val ioFailure = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/octet-stream", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = publicResolver,
            prober = WebDownloadProber { throw IOException("timeout") },
        )
        assertTrue("Expected Failed on IO error, got $ioFailure", ioFailure is ChatWebDownloadResult.Failed)

        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)
    }

    @Test fun redirectPreflightEnqueuesTerminalUrlAndScopesCookiesToSameOrigin() {
        val initial = "https://origin.example.invalid/releases/latest"
        val sameOriginNext = "https://origin.example.invalid/releases/v2/redirect"
        val crossOriginFinal = "https://cdn.example.invalid/artifacts/persona-v2.apk"
        val probeLog = mutableListOf<WebDownloadProbeRequest>()
        var captured: PreparedWebDownload? = null

        val result = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(initial, "PersonaUA", "", "", -1L, pageUrl),
            cookieForUrl = { url ->
                when (url) {
                    initial -> "sid=origin-secret"
                    sameOriginNext -> "sid=origin-next"
                    crossOriginFinal -> "cdn=should-not-cross-origin"
                    else -> null
                }
            },
            enqueue = { _, prepared -> captured = prepared; 55L },
            openInBrowser = { _, _ -> error("should not fall back") },
            hostResolver = publicResolver,
            prober = WebDownloadProber { req ->
                probeLog += req
                when (req.url) {
                    initial -> WebDownloadProbeResponse(statusCode = 302, location = "/releases/v2/redirect")
                    sameOriginNext -> WebDownloadProbeResponse(statusCode = 302, location = crossOriginFinal)
                    crossOriginFinal -> WebDownloadProbeResponse(
                        statusCode = 200,
                        contentLength = 1024L,
                        contentType = "application/vnd.android.package-archive",
                    )
                    else -> error("unexpected probe ${req.url}")
                }
            },
        )

        assertEquals(ChatWebDownloadResult.Started("persona-v2.apk", 55L), result)
        assertEquals(3, probeLog.size)
        assertEquals("sid=origin-secret", probeLog[0].cookie)
        assertEquals("sid=origin-next", probeLog[1].cookie)
        assertNull("Cross-origin redirect probe must not receive cookies", probeLog[2].cookie)
        val prepared = checkNotNull(captured)
        assertEquals(crossOriginFinal, prepared.url)
        assertNull("Cross-origin terminal download must not leak origin cookies", prepared.cookie)
        assertEquals("persona-v2.apk", prepared.fileName)

        // Same-origin redirect keeps same-origin cookies scoped to the terminal URL.
        val sameFinal = "https://origin.example.invalid/files/same.apk"
        var sameCaptured: PreparedWebDownload? = null
        val sameResult = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(initial, "PersonaUA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            cookieForUrl = { url -> if (url == sameFinal) "sid=same-final" else "sid=origin-secret" },
            enqueue = { _, preparedReq -> sameCaptured = preparedReq; 56L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = WebDownloadProber { req ->
                if (req.url == initial) WebDownloadProbeResponse(302, location = sameFinal)
                else WebDownloadProbeResponse(200)
            },
        )
        assertEquals(ChatWebDownloadResult.Started("same.apk", 56L), sameResult)
        assertEquals(sameFinal, sameCaptured?.url)
        assertEquals("sid=same-final", sameCaptured?.cookie)
    }

    @Test fun head405And501FallbackToGetRangeDetectsHiddenRedirectAndOversizedRange() {
        // HEAD 405 -> GET reveals redirect to loopback -> blocked!
        val methods = mutableListOf<String>()
        var enqueueCalls = 0
        val hiddenLoopback = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = WebDownloadProber { req ->
                methods += req.method
                if (req.method == "HEAD") WebDownloadProbeResponse(statusCode = 405)
                else WebDownloadProbeResponse(statusCode = 302, location = "http://127.1/internal")
            },
        )
        assertEquals(listOf("HEAD", "GET"), methods)
        assertEquals(ChatWebDownloadResult.LoopbackUnsupported, hiddenLoopback)
        assertEquals(0, enqueueCalls)

        // HEAD 501 -> GET 206 with oversized Content-Range -> TooLarge!
        val oversized = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = WebDownloadProber { req ->
                if (req.method == "HEAD") WebDownloadProbeResponse(statusCode = 501)
                else WebDownloadProbeResponse(statusCode = 206, contentLength = MAX_WEB_DOWNLOAD_BYTES + 1L)
            },
        )
        assertEquals(ChatWebDownloadResult.TooLarge(MAX_WEB_DOWNLOAD_BYTES), oversized)
        assertEquals(0, enqueueCalls)

        // Real HTTP probe verification via MockWebServer (HEAD 405 -> GET Range: bytes=0-0 -> 206 Content-Range).
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(405))
            server.enqueue(
                MockResponse()
                    .setResponseCode(206)
                    .setHeader("Content-Range", "bytes 0-0/1048576")
                    .setHeader("Content-Type", "application/vnd.android.package-archive")
                    .setBody("x"),
            )
            server.start()
            val url = server.url("/file.apk").toString()
            val headResp = WebDownloadProber.Platform.probe(WebDownloadProbeRequest(url, "HEAD", null, "UA", null))
            assertEquals(405, headResp.statusCode)
            val getResp = WebDownloadProber.Platform.probe(WebDownloadProbeRequest(url, "GET", null, "UA", null))
            assertEquals(206, getResp.statusCode)
            assertEquals(1_048_576L, getResp.contentLength)
            val firstReq = server.takeRequest()
            val secondReq = server.takeRequest()
            assertEquals("HEAD", firstReq.method)
            assertEquals("GET", secondReq.method)
            assertEquals("bytes=0-0", secondReq.getHeader("Range"))
        }
    }

    @Test fun maxFileSizePolicyEnforcesLimitFromRequestAndPreflightAndAllowsUnknownSize() {
        var probeCalls = 0
        var enqueueCalls = 0
        var browserCalls = 0

        // Request contentLength > limit fails immediately before preflight.
        val tooLargeImmediate = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", MAX_WEB_DOWNLOAD_BYTES + 1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = publicResolver,
            prober = WebDownloadProber { probeCalls++; WebDownloadProbeResponse(200) },
        )
        assertEquals(ChatWebDownloadResult.TooLarge(MAX_WEB_DOWNLOAD_BYTES), tooLargeImmediate)
        assertEquals(0, probeCalls)
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)

        // Request contentLength == limit is allowed.
        val exactLimit = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", MAX_WEB_DOWNLOAD_BYTES, pageUrl),
            cookieForUrl = { null },
            enqueue = { _, _ -> 101L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertTrue("Expected Started at exact limit, got $exactLimit", exactLimit is ChatWebDownloadResult.Started)

        // Unknown request contentLength (-1) with preflight Content-Length > limit is rejected.
        val tooLargePreflight = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            enqueue = { _, _ -> enqueueCalls++; 1L },
            openInBrowser = { _, _ -> browserCalls++; true },
            hostResolver = publicResolver,
            prober = WebDownloadProber { WebDownloadProbeResponse(200, contentLength = MAX_WEB_DOWNLOAD_BYTES + 1024L) },
        )
        assertEquals(ChatWebDownloadResult.TooLarge(MAX_WEB_DOWNLOAD_BYTES), tooLargePreflight)
        assertEquals(0, enqueueCalls)
        assertEquals(0, browserCalls)

        // Unknown request length (-1) and unknown preflight length (null) are allowed.
        val unknownAllowed = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            cookieForUrl = { null },
            enqueue = { _, _ -> 102L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertTrue("Expected Started for unknown length, got $unknownAllowed", unknownAllowed is ChatWebDownloadResult.Started)
    }

    @Test fun startWebDownloadPassesSanitizedCookiesUserAgentRefererAndFilename() {
        var captured: PreparedWebDownload? = null
        val result = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(
                url = apkUrl,
                userAgent = "Mozilla/5.0\r\nInjected: bad",
                contentDisposition = "attachment; filename=\"fallback.apk\"; filename*=UTF-8''Persona%20Build+21.apk",
                mimeType = "application/vnd.android.package-archive; charset=binary",
                contentLength = 1024L,
                referer = "https://example.invalid/releases/latest#secret-fragment",
            ),
            cookieForUrl = { "sid=abc123; theme=dark\r\nX-Bad: 1" },
            enqueue = { _, prepared -> captured = prepared; 77L },
            openInBrowser = { _, _ -> error("should not fall back") },
            hostResolver = publicResolver,
            prober = okProber,
        )

        assertEquals(ChatWebDownloadResult.Started("Persona Build+21.apk", 77L), result)
        val prepared = checkNotNull(captured)
        assertEquals(apkUrl, prepared.url)
        assertEquals("Persona Build+21.apk", prepared.fileName)
        assertEquals("application/vnd.android.package-archive", prepared.mimeType)
        assertEquals("sid=abc123; theme=darkX-Bad: 1", prepared.cookie)
        assertEquals("Mozilla/5.0Injected: bad", prepared.userAgent)
        assertEquals("https://example.invalid/releases/latest", prepared.referer)

        // HTTPS referer is stripped on HTTP download downgrade; loopback referer is stripped on forwarded pages.
        var downgraded: PreparedWebDownload? = null
        val httpResult = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(
                url = "http://cdn.example.invalid/file.zip",
                userAgent = "UA",
                contentDisposition = "",
                mimeType = "application/zip",
                contentLength = 10L,
                referer = "https://example.invalid/private",
            ),
            cookieForUrl = { null },
            enqueue = { _, preparedReq -> downgraded = preparedReq; 78L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertTrue(httpResult is ChatWebDownloadResult.Started)
        assertNull(downgraded?.referer)

        var fromForwarded: PreparedWebDownload? = null
        val fwdResult = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(
                url = apkUrl,
                userAgent = "UA",
                contentDisposition = "",
                mimeType = "application/vnd.android.package-archive",
                contentLength = 10L,
                referer = "http://127.1:4173/index.html",
            ),
            isForwarded = true,
            cookieForUrl = { null },
            enqueue = { _, preparedReq -> fromForwarded = preparedReq; 79L },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertTrue(fwdResult is ChatWebDownloadResult.Started)
        assertNull(fromForwarded?.referer)
    }

    @Test fun resolveDownloadFileNameSanitizesTraversalHandlesRfc5987AndCapsUtf8BytesSafely() {
        assertEquals(
            "evil.apk",
            resolveDownloadFileName(apkUrl, "attachment; filename=\"../../evil.apk\"", "application/vnd.android.package-archive"),
        )
        assertEquals(
            "a_b_c_.txt",
            resolveDownloadFileName("https://example.invalid/dl", "attachment; filename=\"sub\\dir/a:b*c?.txt\"", "text/plain"),
        )
        assertEquals(
            "测试+v1.apk",
            resolveDownloadFileName(
                "https://example.invalid/dl",
                "attachment; filename*=UTF-8''%E6%B5%8B%E8%AF%95+v1.apk",
                "application/vnd.android.package-archive",
            ),
        )
        assertEquals(
            "dl.apk",
            resolveDownloadFileName("https://example.invalid/dl?id=42", "", "application/vnd.android.package-archive"),
        )
        assertEquals(
            "downloadfile.apk",
            resolveDownloadFileName("https://example.invalid/?id=42", "", "application/vnd.android.package-archive"),
        )

        // Long ASCII filename is capped by UTF-8 bytes with extension preserved.
        val longAscii = "a".repeat(240) + ".apk"
        val truncatedAscii = resolveDownloadFileName("https://example.invalid/dl", "attachment; filename=\"$longAscii\"", "application/vnd.android.package-archive")
        assertTrue(truncatedAscii.toByteArray(StandardCharsets.UTF_8).size <= MAX_FILENAME_UTF8_BYTES)
        assertTrue(truncatedAscii.endsWith(".apk"))
        assertValidUtf16(truncatedAscii)

        // 120 CJK characters (360+ UTF-8 bytes, well over 255-byte filesystem component limit).
        val longCjk = "测".repeat(120) + ".apk"
        val truncatedCjk = resolveDownloadFileName("https://example.invalid/dl", "attachment; filename=\"$longCjk\"", "application/vnd.android.package-archive")
        val cjkBytes = truncatedCjk.toByteArray(StandardCharsets.UTF_8).size
        assertTrue("Expected <= $MAX_FILENAME_UTF8_BYTES UTF-8 bytes, got $cjkBytes", cjkBytes <= MAX_FILENAME_UTF8_BYTES)
        assertTrue("Expected strictly below 255 filesystem bytes, got $cjkBytes", cjkBytes < 255)
        assertTrue("Expected .apk extension preserved, got $truncatedCjk", truncatedCjk.endsWith(".apk"))
        assertTrue(truncatedCjk.startsWith("测"))
        assertValidUtf16(truncatedCjk)

        // 100 4-byte emoji (400+ UTF-8 bytes, surrogate pairs in UTF-16) with extension and extensionless APK.
        val longEmoji = "\uD83D\uDE80".repeat(100) + ".zip"
        val truncatedEmoji = resolveDownloadFileName("https://example.invalid/dl", "attachment; filename=\"$longEmoji\"", "application/zip")
        val emojiBytes = truncatedEmoji.toByteArray(StandardCharsets.UTF_8).size
        assertTrue("Expected <= $MAX_FILENAME_UTF8_BYTES UTF-8 bytes, got $emojiBytes", emojiBytes <= MAX_FILENAME_UTF8_BYTES)
        assertTrue("Expected .zip extension preserved, got $truncatedEmoji", truncatedEmoji.endsWith(".zip"))
        assertValidUtf16(truncatedEmoji)

        val extensionlessEmoji = "\uD83C\uDF89".repeat(100)
        val truncatedEmojiApk = resolveDownloadFileName(
            "https://example.invalid/dl",
            "attachment; filename=\"$extensionlessEmoji\"",
            "application/vnd.android.package-archive",
        )
        val emojiApkBytes = truncatedEmojiApk.toByteArray(StandardCharsets.UTF_8).size
        assertTrue("Expected <= $MAX_FILENAME_UTF8_BYTES UTF-8 bytes after .apk append, got $emojiApkBytes", emojiApkBytes <= MAX_FILENAME_UTF8_BYTES)
        assertTrue(truncatedEmojiApk.endsWith(".apk"))
        assertValidUtf16(truncatedEmojiApk)
    }

    @Test fun platformDownloadManagerEnqueuesPublicDownloadRequest() {
        CookieManager.getInstance().setCookie(apkUrl, "token=live-cookie")
        val result = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(
                url = apkUrl,
                userAgent = "PersonaAndroid/1.0",
                contentDisposition = "attachment; filename=\"persona-test.apk\"",
                mimeType = "application/vnd.android.package-archive",
                contentLength = 512L,
                referer = pageUrl,
            ),
            openInBrowser = { _, _ -> error("DownloadManager should succeed in Robolectric") },
            hostResolver = publicResolver,
            prober = okProber,
        )

        assertTrue("Expected Started, got $result", result is ChatWebDownloadResult.Started)
        val started = result as ChatWebDownloadResult.Started
        assertEquals("persona-test.apk", started.fileName)
        val manager = context.getSystemService(DownloadManager::class.java)
        assertNotNull(manager)
        val request = shadowOf(manager).getRequest(started.downloadId)
        assertNotNull(request)
        val shadowRequest = shadowOf(request)
        assertEquals(apkUrl, shadowRequest.uri.toString())
        assertEquals("persona-test.apk", shadowRequest.title.toString())
        assertEquals(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED, shadowRequest.notificationVisibility)
        assertTrue(shadowRequest.destination.toString().endsWith("/Download/persona-test.apk"))
    }

    @Test fun downloadManagerFailureFallsBackToExternalBrowserOrReportsFailure() {
        val openedUrls = mutableListOf<String>()
        val fallbackResult = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            enqueue = { _, _ -> throw SecurityException("DownloadManager disabled") },
            openInBrowser = { _, url -> openedUrls += url; true },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertEquals(ChatWebDownloadResult.OpenedInBrowser(apkUrl), fallbackResult)
        assertEquals(listOf(apkUrl), openedUrls)

        val failedResult = startWebDownload(
            context = context,
            request = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl),
            enqueue = { _, _ -> null },
            openInBrowser = { _, _ -> false },
            hostResolver = publicResolver,
            prober = okProber,
        )
        assertEquals(ChatWebDownloadResult.Failed("persona-release.apk"), failedResult)
    }

    @Test fun handleWebDownloadEmitsLocalizedNoticesForEveryOutcome() = runTest {
        val resources = context.resources
        val req = ChatWebDownloadRequest(apkUrl, "UA", "", "application/vnd.android.package-archive", -1L, pageUrl)
        val notices = mutableListOf<Pair<String, FeedbackStyle>>()

        val outcomes = listOf(
            ChatWebDownloadResult.Started("app.apk", 1L),
            ChatWebDownloadResult.OpenedInBrowser(apkUrl),
            ChatWebDownloadResult.LoopbackUnsupported,
            ChatWebDownloadResult.TooLarge(MAX_WEB_DOWNLOAD_BYTES),
            ChatWebDownloadResult.Refused,
            ChatWebDownloadResult.Failed("app.apk"),
        )
        for (outcome in outcomes) {
            val _ = handleWebDownload(context, resources, ChatWebDownloader { _, _, _ -> outcome }, req, false) { msg, style ->
                notices += msg to style
            }
        }

        assertEquals(
            listOf(
                resources.getString(R.string.chat_web_download_started, "app.apk") to FeedbackStyle.Success,
                resources.getString(R.string.chat_web_download_opened_browser) to FeedbackStyle.Info,
                resources.getString(R.string.chat_web_download_loopback_unsupported) to FeedbackStyle.Error,
                resources.getString(R.string.chat_web_download_too_large, 512) to FeedbackStyle.Error,
                resources.getString(R.string.chat_web_cannot_open) to FeedbackStyle.Error,
                resources.getString(R.string.chat_web_download_failed) to FeedbackStyle.Error,
            ),
            notices,
        )
    }

    @Test fun downloadGuardDeduplicatesInFlightCooldownCanonicalUrlsAndEnforcesConcurrencyCap() {
        var now = 1_000L
        val guard = ChatWebDownloadGuard(cooldownMs = 3_000L, maxInFlight = 2, nowMs = { now })

        val key1 = guard.tryStart("https://cdn.example.invalid/downloads/app.apk")
        assertNotNull(key1)
        // Same canonical URL (uppercase host, default port, fragment) is blocked while in-flight.
        assertNull(guard.tryStart("HTTPS://CDN.EXAMPLE.INVALID:443/downloads/app.apk#top"))

        // Second distinct URL allowed, third hits maxInFlight = 2.
        val key2 = guard.tryStart("https://cdn.example.invalid/downloads/other.apk")
        assertNotNull(key2)
        assertNull(guard.tryStart("https://cdn.example.invalid/downloads/third.apk"))

        // Finishing key1 starts cooldown window: immediate re-trigger is still blocked.
        guard.finish(key1!!, applyCooldown = true)
        assertNull(guard.tryStart("https://cdn.example.invalid/downloads/app.apk"))

        // Still inside cooldown at +2999ms.
        now += 2_999L
        assertNull(guard.tryStart("https://cdn.example.invalid/downloads/app.apk"))

        // After cooldown expires at +3000ms, re-trigger succeeds.
        now += 1L
        val key1Again = guard.tryStart("https://cdn.example.invalid/downloads/app.apk")
        assertNotNull(key1Again)

        // clear() resets all in-flight and cooldown entries.
        guard.finish(key1Again!!, applyCooldown = true)
        guard.clear()
        assertNotNull(guard.tryStart("https://cdn.example.invalid/downloads/app.apk"))
    }

    @Test fun chatLinkHostRoutesForegroundDownloadAndDeduplicatesRepeatedTriggers() {
        lateinit var composeView: View
        val notices = mutableListOf<Pair<String, FeedbackStyle>>()
        val captured = mutableListOf<Pair<ChatWebDownloadRequest, Boolean>>()
        val gate = CompletableDeferred<ChatWebDownloadResult>()
        val feedback = object : AppFeedback {
            override fun failure(error: Throwable, operation: Boolean) = true
            override fun notice(message: String): Boolean { notices += message to FeedbackStyle.Info; return true }
            override fun notice(message: String, style: FeedbackStyle): Boolean { notices += message to style; return true }
        }
        val downloader = ChatWebDownloader { _, req, forwarded ->
            captured += req to forwarded
            if (req.url == apkUrl) gate.await() else ChatWebDownloadResult.Started("other.zip", 43L)
        }

        rule.setContent {
            composeView = LocalView.current
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalChatWebFeatures provides NoWebFeatures,
                    LocalChatWebDownloader provides downloader,
                    LocalAppFeedback provides feedback,
                ) {
                    ChatLinkHost(onContext = {}) { _ ->
                        val uri = LocalUriHandler.current
                        Text("Open download page", Modifier.clickable { uri.openUri(pageUrl) })
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Open download page").performClick()
        rule.waitForIdle()

        val webView = rule.runOnIdle { findWebViews(composeView).single() }
        rule.runOnIdle {
            // Double-tap / page loop while first preflight is still in-flight: must only start once.
            simulateDownloadStart(
                view = webView,
                url = apkUrl,
                contentDisposition = "attachment; filename=\"persona-build.apk\"",
                mimeType = "application/vnd.android.package-archive",
            )
            simulateDownloadStart(
                view = webView,
                url = "$apkUrl#duplicate",
                contentDisposition = "attachment; filename=\"persona-build.apk\"",
                mimeType = "application/vnd.android.package-archive",
            )
        }
        rule.waitForIdle()
        assertEquals(1, captured.size)
        assertTrue(notices.isEmpty())

        // Complete in-flight download, then immediately trigger same URL again: cooldown deduplicates it.
        gate.complete(ChatWebDownloadResult.Started("persona-build.apk", 42L))
        rule.waitForIdle()
        rule.runOnIdle {
            simulateDownloadStart(
                view = webView,
                url = apkUrl,
                contentDisposition = "attachment; filename=\"persona-build.apk\"",
                mimeType = "application/vnd.android.package-archive",
            )
        }
        rule.waitForIdle()

        assertEquals(1, captured.size)
        assertEquals(apkUrl, captured.single().first.url)
        assertFalse(captured.single().second)
        assertEquals(
            listOf(context.resources.getString(R.string.chat_web_download_started, "persona-build.apk") to FeedbackStyle.Success),
            notices,
        )

        // A different URL on the same view still downloads normally.
        val otherUrl = "https://cdn.example.invalid/downloads/other.zip"
        rule.runOnIdle {
            simulateDownloadStart(view = webView, url = otherUrl, mimeType = "application/zip")
        }
        rule.waitForIdle()
        assertEquals(2, captured.size)
        assertEquals(otherUrl, captured[1].first.url)
    }

    @Test fun chatLinkHostDisposalCancelsInFlightDownloadAndClearsGuard() {
        lateinit var composeView: View
        val notices = mutableListOf<Pair<String, FeedbackStyle>>()
        val started = CompletableDeferred<Unit>()
        val neverFinishes = CompletableDeferred<ChatWebDownloadResult>()
        var cancelled = false
        val showHost = androidx.compose.runtime.mutableStateOf(true)
        val feedback = object : AppFeedback {
            override fun failure(error: Throwable, operation: Boolean) = true
            override fun notice(message: String): Boolean { notices += message to FeedbackStyle.Info; return true }
            override fun notice(message: String, style: FeedbackStyle): Boolean { notices += message to style; return true }
        }
        val downloader = ChatWebDownloader { _, _, _ ->
            started.complete(Unit)
            try {
                neverFinishes.await()
            } catch (e: kotlinx.coroutines.CancellationException) {
                cancelled = true
                throw e
            }
        }

        rule.setContent {
            composeView = LocalView.current
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(
                    LocalChatWebFeatures provides NoWebFeatures,
                    LocalChatWebDownloader provides downloader,
                    LocalAppFeedback provides feedback,
                ) {
                    if (showHost.value) {
                        ChatLinkHost(onContext = {}) { _ ->
                            val uri = LocalUriHandler.current
                            Text("Open page", Modifier.clickable { uri.openUri(pageUrl) })
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Open page").performClick()
        rule.waitForIdle()

        val webView = rule.runOnIdle { findWebViews(composeView).single() }
        rule.runOnIdle { simulateDownloadStart(view = webView, url = apkUrl) }
        rule.waitForIdle()
        assertTrue(started.isCompleted)

        // Disposing the host cancels the in-flight coroutine, clears downloadGuard, and emits no late notice.
        rule.runOnIdle { showHost.value = false }
        rule.waitForIdle()
        assertTrue("Expected in-flight download coroutine to be cancelled on dispose", cancelled)
        assertTrue(notices.isEmpty())
        assertNotNull("Expected downloadGuard to be cleared on dispose", webView.downloadGuard.tryStart(apkUrl))
    }

    private fun ipv4(vararg octets: Int): String = octets.joinToString(".")

    private fun assertValidUtf16(value: String) {
        var i = 0
        while (i < value.length) {
            val ch = value[i]
            if (Character.isHighSurrogate(ch)) {
                assertTrue("Unpaired high surrogate at index $i in '$value'", i + 1 < value.length && Character.isLowSurrogate(value[i + 1]))
                i += 2
            } else {
                assertFalse("Unpaired low surrogate at index $i in '$value'", Character.isLowSurrogate(ch))
                i += 1
            }
        }
    }

    private fun findWebViews(root: View): List<ChatSelectionWebView> {
        val found = mutableListOf<ChatSelectionWebView>()
        fun visit(v: View) {
            if (v is ChatSelectionWebView) found += v
            if (v is ViewGroup) repeat(v.childCount) { visit(v.getChildAt(it)) }
        }
        visit(root)
        return found
    }
}
