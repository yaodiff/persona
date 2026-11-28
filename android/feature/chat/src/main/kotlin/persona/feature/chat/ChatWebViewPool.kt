package persona.feature.chat

import androidx.annotation.StringRes
import androidx.annotation.VisibleForTesting
import android.content.Context
import android.graphics.Bitmap
import android.graphics.Rect
import android.os.Build
import android.os.Message
import android.view.ActionMode
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.view.ViewGroup
import android.webkit.*
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.webkit.*
import java.net.URI
import java.util.concurrent.Executors
import org.json.JSONTokener
import kotlinx.coroutines.CompletableDeferred

/** What the pool asks the WebView implementation it runs on. Production asks [WebViewFeature]; a test fixes the answers.
 * Call sites keep the platform predicate after this one so lint sees the guard and a false test answer short-circuits it. */
internal fun interface ChatWebFeatures {
    fun supported(feature: String): Boolean

    companion object {
        val Platform = ChatWebFeatures { WebViewFeature.isFeatureSupported(it) }
    }
}

/** Owned by one chat host; never retains a departed activity or a different account. */
internal class ChatWebViewPool(
    private val context: Context,
    private val features: ChatWebFeatures = ChatWebFeatures.Platform,
    private val setAlgorithmicDarkening: (WebSettings, Boolean) -> Unit = ::applyPlatformAlgorithmicDarkening,
    private val viewFactory: (Context) -> ChatSelectionWebView = { ChatSelectionWebView(it) },
) {
    /** Steady state: the page on screen and up to three preloaded links. A pending acquisition can add one while it commits. */
    private val limit = 4
    private val pages = LinkedHashMap<String, ChatSelectionWebView>(limit, 0.75f, true)
    private var active: ChatSelectionWebView? = null
    /** Views whose host composition committed and has not released them: still on its screen, so never evicted. */
    private val held = mutableSetOf<ChatSelectionWebView>()
    /** The latest acquisition not committed by [activate]. Earlier uncommitted acquisitions become evictable again. */
    private var pending: ChatSelectionWebView? = null
    /** After [destroy] nothing is created or brought back: late calls from the host's own teardown do nothing. */
    private var closed = false
    private val speculative = mutableSetOf<String>()
    private val popups = mutableSetOf<WebView>()
    private val startupExecutor = Executors.newSingleThreadExecutor()
    private var started = false
    private val startup = CompletableDeferred<Unit>()

    suspend fun warmUp() {
        if (closed) return
        if (!started) {
            started = true
            WebViewCompat.startUpWebView(context.applicationContext, WebViewStartUpConfig.Builder(startupExecutor).build(),
                object : WebViewOutcomeReceiver<WebViewStartUpResult, WebViewStartupException> {
                    override fun onResult(result: WebViewStartUpResult) { startup.complete(Unit) }
                    override fun onError(error: WebViewStartupException) { startup.complete(Unit) }
                })
        }
        startup.await()
    }

    /** The view for [url], or null once the pool is [destroy]ed. [activate] commits it to the host after composition succeeds. */
    fun acquire(url: String): ChatSelectionWebView? {
        if (closed) return null
        val view = pages[url] ?: create().also { pages[url] = it; if (url in noCache) it.settings.cacheMode = WebSettings.LOAD_NO_CACHE; it.loadUrl(url) }
        // Only the newest uncommitted view needs protection. A composition abandoned before activate leaves no permanent held entry.
        pending = view
        val wasSpeculative = speculative.remove(url)
        val hadSuppressedDownload = view.suppressedBackgroundDownload
        view.suppressedBackgroundDownload = false
        if (wasSpeculative || hadSuppressedDownload) {
            if (view.paused) {
                view.paused = false
                view.onResume()
            }
            view.loadUrl(url)
        }
        trim()
        return view
    }

    /** A successful host composition owns [view] until [release]. An abandoned composition never calls this. */
    fun activate(view: ChatSelectionWebView) {
        if (closed || view.gone || pages.values.none { it === view }) return
        if (pending === view) pending = null
        held += view
        active?.takeIf { it !== view }?.let { previous ->
            previous.exitCustomView()
            pauseView(previous)
        }
        val changed = active !== view
        active = view
        if (changed || view.paused) resumeView(view)
        maybeRecoverMedia(view)
        trim()
    }

    /**
     * Drops the page kept for [url]: a forwarded port's address is the same every time it opens, and the dev server
     * behind it has moved on (docs/operations/ports.md, iOS #1934), so it loads anew, without the disk cache.
     */
    fun forget(url: String) {
        noCache += url
        speculative.remove(url)
        val view = pages[url] ?: return
        // The one on screen reloads in place; one kept aside goes, and the next acquire loads it anew.
        if (view === active) {
            view.exitCustomView()
            view.settings.cacheMode = WebSettings.LOAD_NO_CACHE
            if (view.loadProgress >= 100) view.reload()
        } else {
            pages.remove(url)
            held -= view
            if (pending === view) pending = null
            destroyView(view)
        }
    }

    private val noCache = mutableSetOf<String>()

    @androidx.annotation.OptIn(markerClass = [Profile.ExperimentalUrlPrefetch::class])
    fun preload(url: String) {
        // Loopback addresses (the phone's own), direct media, and direct file downloads are never preloaded in a paused background WebView.
        if (closed || classifyWebDownloadHost(url) != WebDownloadHostSafety.Allowed || isDirectMedia(url) || isDirectDownload(url) || pages.containsKey(url)) return
        val view = create()
        pages[url] = view
        view.needsMediaRecovery = true
        if (features.supported(WebViewFeature.PRERENDER_WITH_URL) &&
            WebViewFeature.isFeatureSupported(WebViewFeature.PRERENDER_WITH_URL)) {
            speculative.add(url)
            WebViewCompat.prerenderUrlAsync(view, url, null, context.mainExecutor, object : PrerenderOperationCallback {
                override fun onPrerenderActivated() = Unit
                override fun onError(error: PrerenderException) {
                    if (pages[url] === view && speculative.remove(url)) view.loadUrl(url)
                }
            })
        } else view.loadUrl(url)
        pauseView(view)
        trim()
    }

    @VisibleForTesting
    fun existing(url: String): ChatSelectionWebView? = pages[url]

    fun release(view: ChatSelectionWebView) {
        held -= view
        if (pending === view) pending = null
        if (closed) return
        val entry = pages.entries.firstOrNull { it.value === view } ?: return
        val url = entry.key
        view.exitCustomView()
        view.onPick = null; view.onExternal = null; view.onDownload = null; view.downloadGuard.clear()
        (view.parent as? ViewGroup)?.removeView(view)
        pauseView(view)
        if (active === view) active = null
        if (url in noCache) return

        // An ordinary view serves one opening: its browsed page and history go, and a fresh view waits on the original URL.
        pages.remove(url)
        speculative.remove(url)
        destroyView(view)
        preload(url)
    }

    /**
     * Over the cap the least recently used page goes, but never the committed page nor the newest acquisition whose composition
     * may still commit. Replacing [pending] makes an abandoned acquisition eligible here again, so repeated abandoned compositions
     * cannot accumulate protected views.
     */
    private fun trim() {
        while (pages.size > limit) {
            val entry = pages.entries.firstOrNull { it.value !== active && it.value !in held && it.value !== pending } ?: return
            pages.remove(entry.key)
            speculative.remove(entry.key)
            destroyView(entry.value)
        }
    }

    /** Memory pressure drops pages no host owns. Committed and newest pending views stay valid even while detached from their slot. */
    fun trimMemory() {
        val drop = pages.entries.filter { it.value !== active && it.value !in held && it.value !== pending && it.value.parent == null }
        drop.forEach { (url, view) -> pages.remove(url); speculative.remove(url); destroyView(view) }
    }

    /** A renderer that crashed or was killed takes only its page with it: forgotten here, shown as gone, made anew on reopening. */
    private fun dropGone(view: ChatSelectionWebView) {
        pages.entries.firstOrNull { it.value === view }?.let { pages.remove(it.key); speculative.remove(it.key) }
        held -= view
        if (pending === view) pending = null
        if (active === view) active = null
        view.gone = true
        destroyView(view)
    }

    private fun destroyView(view: ChatSelectionWebView) {
        view.exitCustomView()
        view.onPick = null
        view.onExternal = null
        view.onDownload = null
        view.downloadGuard.clear()
        view.onWindowAttached = null
        (view.parent as? ViewGroup)?.removeView(view)
        view.destroy()
    }

    private fun pauseView(view: ChatSelectionWebView) {
        mute(view, true)
        if (!view.paused) {
            view.paused = true
            view.onPause()
        }
    }

    private fun resumeView(view: ChatSelectionWebView) {
        mute(view, false)
        if (view.paused) {
            view.paused = false
            view.onResume()
        }
    }

    /**
     * Nudges `<video>` elements whose initial metadata/first-frame load stalled while the WebView was paused and
     * window-detached during background preloading. Runs at most once after the view is active, resumed, attached, and loaded.
     */
    private fun maybeRecoverMedia(view: ChatSelectionWebView) {
        if (!view.needsMediaRecovery || !view.pageLoaded || !view.isWindowAttached) return
        view.needsMediaRecovery = false
        view.recoverStalledMedia()
    }

    @Suppress("SetJavaScriptEnabled")
    private fun create() = viewFactory(context).apply {
        settings.javaScriptEnabled = true
        settings.domStorageEnabled = true
        settings.allowFileAccess = false
        settings.allowContentAccess = false
        // Match Chrome for arbitrary web links: allow passive mixed content (images, QR codes, media) on HTTPS pages
        // while still blocking active mixed content (HTTP scripts, stylesheets, iframes).
        settings.mixedContentMode = WebSettings.MIXED_CONTENT_COMPATIBILITY_MODE
        // Honor standard HTTP Cache-Control / ETag semantics so cacheable assets stay fast while no-store / no-cache
        // pages and HTML5 <video> byte-range requests are never served stale from disk.
        settings.cacheMode = WebSettings.LOAD_DEFAULT
        // Match Chrome viewport and pinch-zoom behavior so responsive and desktop-width pages fit phone width cleanly.
        settings.useWideViewPort = true
        settings.loadWithOverviewMode = true
        settings.setSupportZoom(true)
        settings.builtInZoomControls = true
        settings.displayZoomControls = false
        settings.setSupportMultipleWindows(true)
        settings.javaScriptCanOpenWindowsAutomatically = false
        // Do not heuristically invert pages (or their images and QR codes) in dark mode; let each page's own
        // prefers-color-scheme / color-scheme decide, matching Chrome.
        isForceDarkAllowed = false
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            settings.isAlgorithmicDarkeningAllowed = false
        }
        if (features.supported(WebViewFeature.ALGORITHMIC_DARKENING)) {
            setAlgorithmicDarkening(settings, false)
        }
        // Logins embedded in a page (a sign-in frame from another site) need its cookies; every page shares the store.
        CookieManager.getInstance().setAcceptThirdPartyCookies(this, true)
        val page = this
        onWindowAttached = {
            if (!closed && active === page && !page.gone && !page.paused) maybeRecoverMedia(page)
        }
        webViewClient = object : WebViewClient() {
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val target = request.url.toString()
                if (ChatLinks.webUrl(target) != null) return false
                // Another app's link (mailto:, tel:, intent:): only for a tap on a page that is on screen, and only after
                // asking. Preloaded pages and scripts cannot start apps; this app's own persona: links never come from a page.
                if (ChatLinks.kind(target) == ChatLinkKind.External && request.hasGesture() && view.parent != null) page.onExternal?.invoke(target)
                else if (request.isForMainFrame) failure = view.context.getString(R.string.chat_web_cannot_open)
                return true
            }
            override fun onPageStarted(view: WebView, url: String?, favicon: android.graphics.Bitmap?) {
                page.exitCustomView()
                pageLoaded = false
                failure = null; currentUrl = url.orEmpty(); loadProgress = 0
            }
            override fun onPageFinished(view: WebView, url: String?) {
                currentUrl = url.orEmpty(); pageTitle = view.title.orEmpty(); loadProgress = 100
                pageLoaded = true
                if (!closed && active === page && !page.gone && !page.paused) maybeRecoverMedia(page)
            }
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                this@ChatWebViewPool.dropGone(page)
                return true
            }
            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (request.isForMainFrame) failure = view.context.getString(R.string.chat_web_load_failed)
            }
            override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, response: WebResourceResponse) {
                if (request.isForMainFrame) failure = view.context.getString(R.string.chat_web_unreachable)
            }
        }
        webChromeClient = object : WebChromeClient() {
            override fun onProgressChanged(view: WebView, value: Int) { loadProgress = value }
            override fun onReceivedTitle(view: WebView, title: String?) { pageTitle = title.orEmpty() }
            override fun getDefaultVideoPoster(): Bitmap = Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888)
            override fun onShowCustomView(view: View?, callback: CustomViewCallback?) {
                if (view == null || closed || page.gone || active !== page) {
                    val _ = runCatching { callback?.onCustomViewHidden() }
                    return
                }
                page.showCustomView(view, callback)
            }
            override fun onHideCustomView() {
                page.onHideCustomViewFromWeb()
            }
            override fun onCreateWindow(view: WebView, isDialog: Boolean, isUserGesture: Boolean, resultMsg: Message): Boolean {
                if (!isUserGesture) return false
                // Route target=_blank back to this view without handing off to another app.
                val popup = WebView(context)
                popups.add(popup)
                popup.webViewClient = object : WebViewClient() {
                    override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                        ChatLinks.webUrl(request.url.toString())?.let { this@apply.loadUrl(it) }
                        popup.post { if (popups.remove(popup)) popup.destroy() }
                        return true
                    }
                }
                (resultMsg.obj as WebView.WebViewTransport).webView = popup
                resultMsg.sendToTarget()
                return true
            }
        }
        setDownloadListener { downloadUrl, userAgent, contentDisposition, mimetype, contentLength ->
            if (!canAcceptDownload(page)) {
                if (!closed && !page.gone && active !== page && page !in held && page !== pending) {
                    page.suppressedBackgroundDownload = true
                }
                return@setDownloadListener
            }
            val safeUrl = ChatLinks.webUrl(downloadUrl.orEmpty()) ?: return@setDownloadListener
            val referer = page.currentUrl.ifBlank { page.url.orEmpty() }.takeIf { it.isNotBlank() }
            page.onDownload?.invoke(
                ChatWebDownloadRequest(
                    url = safeUrl,
                    userAgent = userAgent.orEmpty(),
                    contentDisposition = contentDisposition.orEmpty(),
                    mimeType = mimetype.orEmpty(),
                    contentLength = contentLength,
                    referer = referer,
                ),
            )
        }
    }

    private fun canAcceptDownload(view: ChatSelectionWebView): Boolean {
        if (closed || view.gone || active !== view || view.paused || view.parent == null) return false
        return speculative.none { pages[it] === view }
    }

    private fun isDirectMedia(url: String): Boolean = urlExtension(url) in directMediaExtensions

    private fun isDirectDownload(url: String): Boolean = urlExtension(url) in directDownloadExtensions

    private fun urlExtension(url: String): String {
        val path = runCatching { URI(url.trim()).path }.getOrNull()?.lowercase() ?: return ""
        val segment = path.substringAfterLast('/', "")
        val ext = segment.substringAfterLast('.', "")
        return if (ext.isNotEmpty() && ext != segment) ext else ""
    }

    private fun mute(view: WebView, muted: Boolean) {
        if (features.supported(WebViewFeature.MUTE_AUDIO) && WebViewFeature.isFeatureSupported(WebViewFeature.MUTE_AUDIO)) {
            WebViewCompat.setAudioMuted(view, muted)
        }
    }

    fun pause() {
        pages.values.forEach { view ->
            if (!view.pageLoaded) view.needsMediaRecovery = true
            pauseView(view)
        }
    }

    fun resume() {
        active?.let { view ->
            resumeView(view)
            maybeRecoverMedia(view)
        }
    }

    fun destroy() {
        closed = true
        held.clear()
        pending = null
        startup.complete(Unit)
        popups.forEach { it.destroy() }
        popups.clear()
        pages.values.forEach { destroyView(it) }
        pages.clear()
        speculative.clear()
        active = null
        startupExecutor.shutdown()
    }
}

private fun applyPlatformAlgorithmicDarkening(settings: WebSettings, allowed: Boolean) {
    if (WebViewFeature.isFeatureSupported(WebViewFeature.ALGORITHMIC_DARKENING)) {
        // Optional appearance only: a framework / WebView version mismatch must not make the link card unusable.
        try { WebSettingsCompat.setAlgorithmicDarkeningAllowed(settings, allowed) } catch (_: UnsupportedOperationException) {}
    }
}

/** Direct audio/video file extensions skipped by background preloading so they load only in an active, visible WebView. */
private val directMediaExtensions = setOf("mp4", "m4v", "mov", "webm", "mkv", "ogv", "m3u8", "mpd", "mp3", "m4a", "ogg", "wav", "flac")

/** Direct file download extensions skipped by background preloading so downloads only begin on an explicit foreground open. */
private val directDownloadExtensions = setOf("apk", "aab", "zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "pdf", "dmg", "exe", "msi", "deb", "rpm", "pkg", "ipa", "iso", "bin")

/**
 * Re-triggers `load()` on `<video>` elements that stalled at `readyState < 2` while a preloaded WebView was paused and
 * detached. Leaves actively playing, seeked, `preload="none"`, `srcObject`, and MSE (`blob:`) videos untouched.
 */
internal const val RESUME_STALLED_MEDIA_JS =
    "(function(){var vs=document.querySelectorAll('video');for(var i=0;i<vs.length;i++){var v=vs[i];var src=v.currentSrc||v.getAttribute('src')||(v.querySelector('source[src]')&&v.querySelector('source[src]').getAttribute('src'))||'';if(!v.srcObject&&src&&!/^(blob|mediastream):/i.test(src)&&v.paused&&v.currentTime===0&&v.readyState<2&&v.preload!=='none'){try{v.load();}catch(e){}}}})();"

/** What text picked on a page can become: in a conversation "AI edit" and "Use as context", elsewhere "Hand to Persona". */
internal enum class ChatLinkPick(@StringRes val label: Int) {
    Edit(R.string.chat_web_pick_edit), Context(R.string.chat_web_pick_context), HandToPersona(R.string.chat_web_pick_hand),
}

internal open class ChatSelectionWebView(context: Context) : WebView(context) {
    /** The items put first in the text selection menu, in this order. */
    var picks: List<ChatLinkPick> = listOf(ChatLinkPick.Edit, ChatLinkPick.Context)
    var onPick: ((ChatLinkPick, ChatLinkContext) -> Unit)? = null
    /** A tapped link for another app (mailto:, tel:, intent:): the host asks before it leaves. */
    var onExternal: ((String) -> Unit)? = null
    /** A file download triggered by a foreground, mounted page; the host routes it through [ChatWebDownloader]. */
    var onDownload: ((ChatWebDownloadRequest) -> Unit)? = null
    /** Per-view in-flight and short-cooldown deduplication guard; cleared on release and destroy. */
    internal val downloadGuard: ChatWebDownloadGuard = ChatWebDownloadGuard()
    /** True when a background preloaded view suppressed a download so the next foreground acquire reloads cleanly. */
    internal var suppressedBackgroundDownload: Boolean = false
    /** Active fullscreen custom view requested via [WebChromeClient.onShowCustomView], if any. */
    var customView by mutableStateOf<View?>(null)
        internal set
    private var customViewCallback: WebChromeClient.CustomViewCallback? = null
    /** True while [WebView.onPause] is active on this view. */
    internal var paused: Boolean = false
    /** True once [WebViewClient.onPageFinished] has fired for the current main-frame navigation. */
    internal var pageLoaded: Boolean = false
    /** True when the page started loading while paused/detached and should nudge stalled `<video>` elements once visible. */
    internal var needsMediaRecovery: Boolean = false
    /** Notifies the pool when this view attaches to a window so deferred media recovery can run on a live surface. */
    internal var onWindowAttached: (() -> Unit)? = null
    var currentUrl by mutableStateOf("")
    var pageTitle by mutableStateOf("")
    var failure by mutableStateOf<String?>(null)
    var loadProgress by mutableIntStateOf(0)
    /** Its renderer crashed or was killed: the view is destroyed, the host offers to load the page again. */
    var gone by mutableStateOf(false)

    internal open val isWindowAttached: Boolean get() = isAttachedToWindow

    override fun onAttachedToWindow() {
        super.onAttachedToWindow()
        onWindowAttached?.invoke()
    }

    internal fun showCustomView(view: View, callback: WebChromeClient.CustomViewCallback?) {
        if (customView === view) {
            if (callback != null && callback !== customViewCallback) customViewCallback = callback
            return
        }
        clearCustomView()
        customView = view
        customViewCallback = callback
    }

    /** Dismisses any active custom view, detaching it and invoking its Chromium callback once. */
    internal fun exitCustomView(): Boolean = clearCustomView()

    internal fun onHideCustomViewFromWeb() {
        clearCustomView()
    }

    private fun clearCustomView(): Boolean {
        val current = customView
        val callback = customViewCallback
        if (current == null && callback == null) return false
        customView = null
        customViewCallback = null
        (current?.parent as? ViewGroup)?.removeView(current)
        val _ = runCatching { callback?.onCustomViewHidden() }
        return true
    }

    internal fun recoverStalledMedia() {
        evaluateJavascript(RESUME_STALLED_MEDIA_JS, null)
    }

    override fun startActionMode(callback: ActionMode.Callback): ActionMode? = super.startActionMode(wrap(callback))
    override fun startActionMode(callback: ActionMode.Callback, type: Int): ActionMode? = super.startActionMode(wrap(callback), type)

    private fun wrap(delegate: ActionMode.Callback) = object : ActionMode.Callback2() {
        override fun onCreateActionMode(mode: ActionMode, menu: Menu): Boolean = delegate.onCreateActionMode(mode, menu)
        // Chromium fills the menu here, each time the selection changes, with items that ask to be action buttons (the
        // floating toolbar puts those first). Ours ask the same in Chromium's group with the lowest order, so they come
        // before "Copy", as on iOS; added in onCreateActionMode they ended up after it or in the overflow.
        override fun onPrepareActionMode(mode: ActionMode, menu: Menu): Boolean {
            val changed = delegate.onPrepareActionMode(mode, menu)
            ChatLinkPick.entries.forEach { menu.removeItem(it.id) }
            if (onPick == null) return changed
            val group = (0 until menu.size()).map { menu.getItem(it) }.minByOrNull { it.order }?.groupId ?: Menu.NONE
            picks.forEach { menu.add(group, it.id, 0, context.getString(it.label)).setShowAsAction(MenuItem.SHOW_AS_ACTION_ALWAYS) }
            return true
        }
        override fun onActionItemClicked(mode: ActionMode, item: MenuItem): Boolean {
            val pick = ChatLinkPick.entries.firstOrNull { it.id == item.itemId } ?: return delegate.onActionItemClicked(mode, item)
            val sourceUrl = url.orEmpty()
            val sourceTitle = title.orEmpty()
            // Read the selection before closing the menu: finishing first clears it, and the script runs later.
            evaluateJavascript("(function(){var e=document.activeElement;return e&&(e.tagName==='TEXTAREA'||e.tagName==='INPUT')&&e.type!=='password'&&typeof e.selectionStart==='number'?e.value.substring(e.selectionStart,e.selectionEnd):window.getSelection().toString();})()") { result ->
                val selected = runCatching { JSONTokener(result).nextValue() as? String }.getOrNull().orEmpty()
                if (selected.isNotBlank() && url == sourceUrl) onPick?.invoke(pick, ChatLinkContext(
                    if (pick == ChatLinkPick.Edit) "edit" else "context", sourceTitle, sourceUrl, selected))
                mode.finish()
            }
            return true
        }
        override fun onDestroyActionMode(mode: ActionMode) = delegate.onDestroyActionMode(mode)
        override fun onGetContentRect(mode: ActionMode, view: View, outRect: Rect) {
            if (delegate is ActionMode.Callback2) delegate.onGetContentRect(mode, view, outRect)
            else super.onGetContentRect(mode, view, outRect)
        }
    }

    private val ChatLinkPick.id: Int get() = 0x6a0101 + ordinal
}
