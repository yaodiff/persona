package persona.feature.chat

import persona.core.domain.ports.PortLinks
import android.content.ComponentCallbacks2
import android.content.Intent
import android.content.res.Configuration
import android.net.Uri
import android.view.ViewGroup
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.*
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.*
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.DialogWindowProvider
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.currentStateAsState
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.feature.inbox.LarkDocumentLink
import persona.feature.inbox.LarkDocumentPrefetch
import persona.feature.inbox.LarkLinkPicks
import persona.feature.inbox.LarkMomentsLink
import persona.core.inbox.LarkNativeLink
import persona.feature.inbox.LarkTextPick
import persona.feature.inbox.ManagerUriHandler
import persona.core.inbox.larkNativeLink
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

internal val LocalChatLinkOpening = staticCompositionLocalOf { 0 }

private enum class LinkDetent { Full, Peek, Hidden }

/**
 * Shared URI boundary for Agent, assistant, and other conversation surfaces. http(s) opens in the sheet (the hub's own
 * pages the app has a screen for navigate instead, [LocalChatHubLinks]), `persona://` links navigate ([LocalChatAppLinks]), mailto: / tel: / other apps' links go out after asking, the rest is refused.
 *
 * In a conversation ([onContext], [onEdit]) text picked on a page becomes "Use as context" or "AI edit"; with [onHandToPersona]
 * (pages with no conversation behind them: the app shell, the knowledge base) it is "Hand to Persona" instead.
 */
@Composable
fun ChatLinkHost(
    modifier: Modifier = Modifier,
    preloadUrls: List<String> = emptyList(),
    isScrolling: Boolean = false,
    onContext: ((ChatLinkContext) -> Unit)? = null,
    onEdit: (suspend (ChatLinkContext, String) -> Unit)? = null,
    onVisibilityChanged: (Boolean) -> Unit = {},
    /** Sends a whole message to Persona's main conversation (and shows it); the sheet closes once it went. */
    onHandToPersona: (suspend (String) -> Unit)? = null,
    /**
     * The conversation's manager (docs/assistant/managers.md "Clients"): the documents and posts its links open natively, and
     * those read ahead, are read through it, as are links tapped in them; null: none named.
     */
    manager: String? = null,
    /** Deferred scroll-state reader so callers avoid recomposing the whole page when scrolling starts or stops. */
    scrolling: (() -> Boolean)? = null,
    content: @Composable (bottomInset: Dp) -> Unit,
) {
    val context = LocalContext.current
    val resources = LocalResources.current
    val webFeatures = LocalChatWebFeatures.current
    val webDownloader = LocalChatWebDownloader.current
    val pool = remember(context, webFeatures) { ChatWebViewPool(context, webFeatures) }
    val scope = rememberCoroutineScope()
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val lifecycleState by lifecycle.currentStateAsState()
    val feedback = LocalAppFeedback.current
    val appLinks by rememberUpdatedState(LocalChatAppLinks.current)
    val hubLinks by rememberUpdatedState(LocalChatHubLinks.current)
    val lark = LocalLarkFeatures.current
    var url by remember { mutableStateOf<String?>(null) }
    /** The manager the page that opened [url] named (ManagerUriHandler); null: [manager]. */
    var linkManager by remember { mutableStateOf<String?>(null) }
    /** "machine name :port" while the sheet shows one of the machine's ports (docs/operations/ports.md "Forwarding"). */
    var forwarded by remember { mutableStateOf<String?>(null) }
    val portsState = rememberUpdatedState(LocalChatPorts.current)
    val ports by portsState
    var unsupported by remember { mutableStateOf(false) }
    var external by remember { mutableStateOf<String?>(null) }
    var opening by remember { mutableIntStateOf(0) }
    var closeRequest by remember { mutableIntStateOf(0) }
    val linkMenu = remember { ChatLinkMenuState() }
    val uriHandler = remember { object : ManagerUriHandler {
        override fun openUri(uri: String) = openUri(uri, null)
        override fun openUri(uri: String, manager: String?) {
            when (ChatLinks.kind(uri)) {
                ChatLinkKind.Web -> {
                    val target = ChatLinks.webUrl(uri)
                    // A page of the hub's web app that the app shows itself opens there, not in the sheet.
                    if (target != null && hubLinks?.invoke(target) == true) { if (url != null) closeRequest++; return }
                    // A link to this machine's localhost is one of its ports, opened through the hub (docs/operations/ports.md).
                    val machine = ports
                    val port = machine?.let { PortLinks.link(uri, it.hostnames) }
                    when {
                        target == null -> unsupported = true
                        machine != null && port != null -> scope.launch {
                            runCatching { machine.opener.open(machine.machineId, port.port, port.path, port.secure, port.port in machine.listed) }
                                .onSuccess { local -> pool.forget(local); url = local; linkManager = null; forwarded = "${machine.name} :${port.port}"; opening++ }
                                .onFailure { error ->
                                    if (error is persona.core.contracts.api.PortGone) machine.presence?.noteGone(port.port)
                                    if (error !is kotlinx.coroutines.CancellationException) feedback?.notice(error.message ?: resources.getString(R.string.chat_link_port_failed, port.port))
                                }
                        }
                        else -> { url = target; linkManager = manager; forwarded = null; opening++ }
                    }
                }
                ChatLinkKind.App -> {
                    // Route-table links keep their original URL so selecting another hub can be completed by the shell.
                    if (hubLinks?.invoke(uri) == true) { if (url != null) closeRequest++; return }
                    val link = ChatLinks.appLink(uri)
                    val navigate = appLinks
                    if (link == null || navigate == null) unsupported = true
                    else { if (url != null) closeRequest++; navigate(link) }
                }
                ChatLinkKind.External -> { external = uri.trim() }
                ChatLinkKind.Refused -> { unsupported = true }
            }
        }
    } }
    val visibility by rememberUpdatedState(onVisibilityChanged)
    LaunchedEffect(url) { visibility(url != null) }
    DisposableEffect(pool, lifecycle) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_STOP) pool.pause()
            if (event == Lifecycle.Event.ON_START) pool.resume()
        }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer) }
    }
    // Memory pressure (the app in the background, or the system asks): pages not on screen go; each is a renderer.
    DisposableEffect(pool, context) {
        val callbacks = object : ComponentCallbacks2 {
            override fun onTrimMemory(level: Int) { if (level >= ComponentCallbacks2.TRIM_MEMORY_UI_HIDDEN) pool.trimMemory() }
            override fun onConfigurationChanged(newConfig: Configuration) = Unit
            @Deprecated("Superseded by onTrimMemory")
            override fun onLowMemory() = pool.trimMemory()
        }
        context.applicationContext.registerComponentCallbacks(callbacks)
        onDispose { context.applicationContext.unregisterComponentCallbacks(callbacks) }
    }
    ChatLinkPrefetch(preloadUrls, isScrolling, scrolling, lifecycleState, pool, portsState, lark, manager)
    val picks = if (onHandToPersona != null) listOf(ChatLinkPick.HandToPersona)
        else listOfNotNull(ChatLinkPick.Edit.takeIf { onEdit != null }, ChatLinkPick.Context.takeIf { onContext != null })
    // A web page is over the page: the page's glass goes opaque and the dock makes way (LocalWebSheets).
    WebSheetShown(url != null)
    // A conversation embedded under its host's bar (Persona's page) has the card start below that bar, not under it.
    // Standalone hosts use the safe top instead; the larger value avoids counting an inset already in the host's bar twice.
    val hostBar = LocalChromeInsets.current.top
    var hostTop by remember { mutableFloatStateOf(0f) }
    // The host's height, in pixels: it shrinks on every frame the keyboard slides up (the page's imePadding), so it is read
    // in layout and effects, and in composition only while a card is open; a BoxWithConstraints here composed the whole
    // conversation again on each of those frames.
    var hostHeight by remember { mutableIntStateOf(-1) }
    CompositionLocalProvider(
        LocalUriHandler provides uriHandler,
        LocalChatLinkOpening provides opening,
        LocalChatLinkMenu provides linkMenu,
    ) {
        Box(modifier.fillMaxSize().onGloballyPositioned { hostTop = it.positionInWindow().y }.onSizeChanged { hostHeight = it.height }) {
            // safeDrawing includes the keyboard: read it in a derived state, whose value (the top) does not move with it.
            val safeDrawing = WindowInsets.safeDrawing
            val density = LocalDensity.current
            // Until the host is measured (its first frame) the window's height stands in.
            val windowHeight = LocalWindowInfo.current.containerSize.height
            val geometryState = remember(safeDrawing, density, hostBar, windowHeight) {
                derivedStateOf {
                    val pixels = (if (hostHeight >= 0) hostHeight else windowHeight).toFloat()
                    // Window insets use window coordinates; this host may already start below its page bar or a sheet's safe top.
                    val remainingSafeTop = (safeDrawing.getTop(density) - hostTop).coerceAtLeast(0f)
                    LinkGeometry(pixels, maxOf(with(density) { hostBar.toPx() }, remainingSafeTop).coerceIn(0f, pixels))
                }
            }
            val geometry by geometryState
            val drag = remember { AnchoredDraggableState(LinkDetent.Hidden) }
            // Keyed on the derived state itself: one made anew for a new bar, inset or window (now and then, never per
            // keyboard frame) restarts the collector, which would otherwise keep the old one's anchors.
            LaunchedEffect(drag, geometryState) {
                snapshotFlow { geometryState.value }.collect { (pixels, above) ->
                    drag.updateAnchors(DraggableAnchors {
                        LinkDetent.Full at above
                        LinkDetent.Peek at above + (pixels - above) * 0.4f
                        LinkDetent.Hidden at pixels
                    })
                }
            }
            LaunchedEffect(opening) { if (url != null) drag.animateTo(LinkDetent.Full) }
            LaunchedEffect(closeRequest) { if (closeRequest > 0 && url != null) drag.animateTo(LinkDetent.Hidden) }
            LaunchedEffect(drag.settledValue) { if (drag.settledValue == LinkDetent.Hidden && !drag.isAnimationRunning) { url = null; forwarded = null } }
            content(if (url != null && drag.currentValue == LinkDetent.Peek) with(density) { (geometry.pixels - geometry.above).toDp() } * 0.6f else 0.dp)
            url?.let { target ->
                // A page whose renderer went away is loaded anew (a new view) when the user asks.
                var generation by remember(target) { mutableIntStateOf(0) }
                // A Feishu link the hub shows natively (the Lark extension, #1530): a document's reader or a Moments post.
                val native = remember(target, lark) { larkNativeLink(target, lark) }
                // Whether that native view switched to the web page ("Switch to web page", or a read that failed): then the card's
                // "Refresh", progress and crash row are the page's again.
                var nativeWebpage by remember(target) { mutableStateOf(false) }
                val nativeShown = native != null && !nativeWebpage
                // The WebView is only taken (and loaded) once a page shows it: a native view reads without it.
                val view = remember(pool, target, generation, nativeShown) { if (nativeShown) null else pool.acquire(target) }
                var editing by remember(target) { mutableStateOf<ChatLinkContext?>(null) }
                var handing by remember(target) { mutableStateOf<ChatLinkContext?>(null) }
                var menu by remember { mutableStateOf(false) }
                val pick by rememberUpdatedState(onContext)
                val picked: (ChatLinkPick, ChatLinkContext) -> Unit = { kind, selected ->
                    when (kind) {
                        ChatLinkPick.Edit -> { editing = selected }
                        ChatLinkPick.Context -> { pick?.invoke(selected); scope.launch { drag.animateTo(LinkDetent.Peek) } }
                        ChatLinkPick.HandToPersona -> { handing = selected }
                    }
                }
                // What the native view read: its title and the text "Use as context" sends for the whole of it (G184).
                var nativeTitle by remember(target) { mutableStateOf<String?>(null) }
                var nativeWhole by remember(target) { mutableStateOf<String?>(null) }
                var nativeRefresh by remember(target) { mutableIntStateOf(0) }
                val nativeFallback = when (native) {
                    is LarkNativeLink.Document -> stringResource(R.string.chat_link_lark_doc)
                    is LarkNativeLink.MomentsPost -> stringResource(R.string.chat_link_moments)
                    null -> ""
                }
                val pickLabels = picks.map { stringResource(it.label) }
                val nativePicks = remember(picks, pickLabels, target) {
                    LarkLinkPicks(picks.zip(pickLabels) { kind, label ->
                        LarkTextPick(label) { text ->
                            picked(kind, ChatLinkContext(if (kind == ChatLinkPick.Edit) "edit" else "context",
                                nativeTitle ?: nativeFallback, target, text))
                        }
                    }, onWebpage = { nativeWebpage = it }) { title, whole -> nativeTitle = title; nativeWhole = whole }
                }
                val currentDownloader by rememberUpdatedState(webDownloader)
                val currentFeedback by rememberUpdatedState(feedback)
                if (view != null) {
                    // Only a committed composition activates the view; acquires from abandoned compositions stay evictable.
                    SideEffect { view.picks = picks; pool.activate(view) }
                    // Only the view going away releases it: a change of picks must not destroy the page being shown.
                    DisposableEffect(view) {
                        val downloadJob = SupervisorJob(scope.coroutineContext[Job])
                        val downloadScope = CoroutineScope(scope.coroutineContext + downloadJob)
                        view.onPick = picked
                        view.onExternal = { external = it }
                        view.onDownload = { request ->
                            val key = view.downloadGuard.tryStart(request.url)
                            if (key != null) {
                                val isForwardedPage = forwarded != null
                                downloadScope.launch {
                                    var completed = false
                                    try {
                                        val _ = handleWebDownload(context, resources, currentDownloader, request, isForwardedPage) { notice, style ->
                                            val _ = currentFeedback?.notice(notice, style)
                                        }
                                        completed = true
                                    } finally {
                                        view.downloadGuard.finish(key, applyCooldown = completed)
                                    }
                                }
                            }
                        }
                        onDispose {
                            downloadJob.cancel()
                            view.downloadGuard.clear()
                            pool.release(view)
                        }
                    }
                }
                fun close() {
                    view?.exitCustomView()
                    scope.launch { drag.animateTo(LinkDetent.Hidden) }
                }
                fun back() {
                    when {
                        view != null && view.exitCustomView() -> Unit
                        view != null && !view.gone && view.canGoBack() -> view.goBack()
                        else -> close()
                    }
                }
                BackHandler { back() }
                val pixels = geometry.pixels
                val offset = drag.offset.takeIf { it.isFinite() } ?: pixels
                val panelHeight = with(density) { (pixels - offset).coerceIn(0f, pixels).toDp() }
                val palette = PersonaTheme.palette
                val shape = PersonaTheme.shapes.sheetShape
                // No glass inside the card either: its page would put the WebView in a glass source (LocalWebSheets).
                // The card sits below the host's bar and system top, and over the dock, so its page keeps no room for them.
                // Its background reaches the side cutouts; only the controls and page content move inside them.
                CompositionLocalProvider(LocalGlassLevel provides GlassLevel.Solid, LocalChromeInsets provides ChromeInsets()) {
                Column(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(panelHeight).testTag("chat_link_card")
                    .shadow(Tokens.Space.s, shape).clip(shape).background(palette.surfaceOpaque)
                    .windowInsetsPadding(safeDrawing.only(WindowInsetsSides.Horizontal))
                    .consumeWindowInsets(safeDrawing.only(WindowInsetsSides.Top))) {
                    Column(Modifier.fillMaxWidth().anchoredDraggable(drag, Orientation.Vertical)) {
                        Box(Modifier.align(Alignment.CenterHorizontally).padding(top = Tokens.Space.s).size(Tokens.Space.Standard.xxl, Tokens.Space.Standard.xs)
                            .clip(PersonaTheme.shapes.controlShape).background(palette.ink3))
                        Row(Modifier.fillMaxWidth().padding(end = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
                            JIconButton(PhIcons.CaretLeft, stringResource(R.string.chat_link_back), { back() })
                            Column(Modifier.weight(1f)) {
                                Text((if (native != null && (nativeShown || view?.pageTitle.isNullOrBlank())) nativeTitle ?: nativeFallback else view?.pageTitle.orEmpty()).ifBlank { stringResource(R.string.chat_link_page) }, style = PersonaTheme.type.bodyStrong, color = palette.ink,
                                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(forwarded ?: view?.currentUrl.orEmpty().ifBlank { target }, style = PersonaTheme.type.micro, color = palette.ink3,
                                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                            // The whole of a native page as context (iOS LinkSheet, web ContextButton): a document goes
                            // by its link and the whole-document marker (the agent reads it), a post with its text once it is read.
                            val whole = ChatLinkContext.whole(native, nativeWhole)
                            if (whole != null && onContext != null) JIconButton(PhIcons.Quotes,
                                stringResource(if (native is LarkNativeLink.Document) R.string.chat_link_context_doc else R.string.chat_link_context_post), {
                                    picked(ChatLinkPick.Context, ChatLinkContext("context", nativeTitle ?: nativeFallback, target, whole))
                                })
                            JIconButton(if (drag.currentValue == LinkDetent.Full) PhIcons.CaretDown else PhIcons.CaretUp,
                                stringResource(if (drag.currentValue == LinkDetent.Full) R.string.chat_link_peek else R.string.chat_link_full),
                                { scope.launch { drag.animateTo(if (drag.currentValue == LinkDetent.Full) LinkDetent.Peek else LinkDetent.Full) } })
                            Box {
                                val copy = rememberCopy()
                                val page = { view?.currentUrl?.let { ChatLinks.webUrl(it) } ?: target }
                                JIconButton(PhIcons.DotsThree, stringResource(R.string.chat_link_actions), { menu = true })
                                val copied = stringResource(R.string.chat_link_copied)
                                val noBrowser = stringResource(R.string.chat_link_no_browser)
                                // A forwarded port's address is the phone's own loopback: no other app could open it.
                                JMenu(menu, { menu = false }, listOfNotNull(
                                    JAction(stringResource(R.string.chat_link_copy), PhIcons.LinkSimple) {
                                        copy(page(), copied)
                                    }.takeIf { forwarded == null },
                                    JAction(stringResource(R.string.chat_link_reload), PhIcons.ArrowClockwise, enabled = nativeShown || view?.gone == false) {
                                        if (nativeShown) nativeRefresh++ else view?.reload()
                                    },
                                    if (forwarded != null) null else JAction(stringResource(R.string.chat_link_open_browser), PhIcons.ArrowSquareOut) {
                                        val opened = runCatching {
                                            context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(page())).addCategory(Intent.CATEGORY_BROWSABLE))
                                        }.isSuccess
                                        if (!opened) feedback?.notice(noBrowser)
                                    },
                                    JAction(stringResource(R.string.chat_link_share), PhIcons.ShareNetwork) {
                                        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, page())
                                        val _ = runCatching { context.startActivity(Intent.createChooser(send, null)) }
                                    }.takeIf { forwarded == null },
                                ))
                            }
                            JIconButton(PhIcons.X, stringResource(R.string.chat_link_close), ::close)
                        }
                    }
                    if (view != null && !view.gone && view.loadProgress < 100 && view.failure == null)
                        JLinearProgress(view.loadProgress / 100f, Modifier.padding(horizontal = pagePadding))
                    if (view != null) (if (view.gone) stringResource(R.string.chat_link_crashed) else view.failure)?.let { error ->
                        Row(Modifier.padding(horizontal = pagePadding), verticalAlignment = Alignment.CenterVertically) {
                            Text(error, Modifier.weight(1f).padding(vertical = Tokens.Space.s), style = PersonaTheme.type.callout,
                                color = palette.danger.text)
                            JTextButton(stringResource(if (view.gone) R.string.chat_link_reopen else R.string.chat_link_retry), { if (view.gone) generation++ else view.reload() })
                        }
                    }
                    if (native != null) {
                        // Feishu documents and Moments posts in their native views while the hub has them (#1530); each can
                        // still switch to the page, which takes the WebView then (a crashed one is gone: "Reopen" above).
                        val page: @Composable () -> Unit = {
                            if (view != null && !view.gone) key(view) { ChatLinkWebSlot(view, Modifier.fillMaxSize()) }
                        }
                        val read = linkManager ?: manager
                        // A link inside it (another document, a post) opens as the manager it is read as.
                        val nested = remember(uriHandler, read) { object : ManagerUriHandler {
                            override fun openUri(uri: String) = uriHandler.openUri(uri, read)
                            override fun openUri(uri: String, manager: String?) = uriHandler.openUri(uri, manager ?: read)
                        } }
                        CompositionLocalProvider(LocalUriHandler provides nested) {
                            if (native is LarkNativeLink.Document)
                                LarkDocumentLink(target, Modifier.fillMaxWidth().weight(1f), nativePicks, nativeRefresh, read, page)
                            else if (native is LarkNativeLink.MomentsPost)
                                LarkMomentsLink(native.postId, target, Modifier.fillMaxWidth().weight(1f), nativePicks, nativeRefresh, read, page)
                        }
                    } else if (view != null && !view.gone) key(view) {
                        JPage(title = stringResource(R.string.chat_link_page), modifier = Modifier.fillMaxWidth().weight(1f), topBar = {}) { padding ->
                            ChatLinkWebSlot(view, Modifier.fillMaxSize().padding(padding).padding(horizontal = pagePadding))
                        }
                    } else Spacer(Modifier.fillMaxWidth().weight(1f))
                }
                }
                editing?.let { selection ->
                    LinkPickDialog(stringResource(R.string.chat_link_edit_title), selection, stringResource(R.string.chat_link_edit_hint), onDismiss = { editing = null }) { instruction ->
                        onEdit?.invoke(selection, instruction)
                        editing = null
                        scope.launch { drag.animateTo(LinkDetent.Peek) }
                    }
                }
                handing?.let { selection ->
                    HandToPersonaDialog(selection, onDismiss = { handing = null }) { message ->
                        onHandToPersona?.invoke(message)
                        handing = null
                        close()
                    }
                }
                val customView = view?.takeUnless { it.gone }?.customView
                if (view != null && customView != null) {
                    JFullScreen(onDismiss = { view.exitCustomView() }) {
                        val dialogView = LocalView.current
                        SideEffect {
                            (dialogView.parent as? DialogWindowProvider)?.window?.let { window ->
                                WindowCompat.getInsetsController(window, dialogView).run {
                                    isAppearanceLightStatusBars = false
                                    isAppearanceLightNavigationBars = false
                                }
                            }
                        }
                        DisposableEffect(dialogView) {
                            val controller = (dialogView.parent as? DialogWindowProvider)?.window?.let { window ->
                                WindowCompat.getInsetsController(window, dialogView)
                            }
                            controller?.run {
                                systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
                                hide(WindowInsetsCompat.Type.systemBars())
                            }
                            onDispose {
                                controller?.show(WindowInsetsCompat.Type.systemBars())
                            }
                        }
                        Box(Modifier.fillMaxSize().background(palette.mediaBackdrop).testTag("chat_link_custom_view")) {
                            key(customView) {
                                AndroidView(
                                    factory = { (customView.parent as? ViewGroup)?.removeView(customView); customView },
                                    modifier = Modifier.fillMaxSize(),
                                    onRelease = { (it.parent as? ViewGroup)?.removeView(it) },
                                )
                            }
                        }
                    }
                }
            }
        }
        linkMenu.target?.let { menuTarget ->
            val copy = rememberCopy()
            val copied = stringResource(R.string.chat_link_copied)
            val noBrowser = stringResource(R.string.chat_link_no_browser)
            ChatLinkMenuOverlay(
                target = menuTarget,
                resources = resources,
                ports = ports,
                onDismiss = linkMenu::close,
                onOpenInApp = { link -> uriHandler.openUri(link, manager) },
                onOpenInBrowser = { link -> if (!openInSystemBrowser(context, link)) feedback?.notice(noBrowser) },
                onCopy = { link -> copy(link, copied) },
                onShare = { link -> shareLinkText(context, link) },
            )
        }
        if (unsupported) JDialog(onDismiss = { unsupported = false }, title = stringResource(R.string.chat_link_unsupported_title),
            text = stringResource(R.string.chat_link_unsupported_text), confirm = stringResource(R.string.chat_link_ok) to { unsupported = false }, dismiss = null)
        external?.let { link ->
            val prompt = remember(link, resources) { ChatLinks.externalPrompt(link, resources) }
            val noApp = stringResource(R.string.chat_link_no_app)
            JDialog(onDismiss = { external = null }, title = prompt.title, text = prompt.text, confirm = stringResource(R.string.chat_link_open) to {
                external = null
                if (!openExternal(context, link)) feedback?.notice(noApp)
            })
        }
        // Declared after the card's own effects, which Compose forgets in reverse order: when the host goes, the pool closes first,
        // so the page's release only destroys it instead of building a replacement for a pool that is about to be destroyed.
        DisposableEffect(pool) { onDispose { pool.destroy() } }
    }
}

/**
 * The page's slot in the card. Leaving composition only detaches the view; destroying it is [ChatWebViewPool.release], which the
 * host's `DisposableEffect(view)` calls once, when the page stops showing. A slot that comes back with the same view gets it alive.
 */
@Composable
internal fun ChatLinkWebSlot(view: ChatSelectionWebView, modifier: Modifier = Modifier) {
    AndroidView(factory = { (view.parent as? ViewGroup)?.removeView(view); view }, modifier = modifier)
}

/** Which WebView features the host's pool may use; the platform's answers unless a test fixes them. */
internal val LocalChatWebFeatures = staticCompositionLocalOf { ChatWebFeatures.Platform }

/**
 * The machine a conversation runs on, for its port links (docs/operations/ports.md "Recognizing links in replies"): its id and name, the names
 * it goes by in links, the ports its agents list (the hub allows those without asking), and how to open one.
 */
internal data class ChatPorts(
    val machineId: String,
    val name: String,
    val hostnames: List<String>,
    /** The ports its agents list, with their pages' titles. */
    val listed: Map<Int, String?>,
    val opener: persona.core.contracts.api.PortOpener,
    /** What the other ports' badges know (null where nothing asks, e.g. a preview). */
    val presence: PortPresence? = null,
)

internal val LocalChatPorts = androidx.compose.runtime.compositionLocalOf<ChatPorts?> { null }

/** Leaf scope for link prefetching so scroll start/stop never recomposes [ChatLinkHost] or its conversation content. */
@Composable
private fun ChatLinkPrefetch(
    preloadUrls: List<String>,
    isScrolling: Boolean,
    scrolling: (() -> Boolean)?,
    lifecycleState: Lifecycle.State,
    pool: ChatWebViewPool,
    ports: State<ChatPorts?>,
    lark: persona.core.contracts.LarkFeatures,
    manager: String?,
) {
    val activeScroll = scrolling?.invoke() ?: isScrolling
    LaunchedEffect(preloadUrls, activeScroll, lifecycleState) {
        if (!activeScroll && lifecycleState.isAtLeast(Lifecycle.State.STARTED)) {
            delay(600)
            pool.warmUp()
            // A machine's port is never preloaded: it would load the phone's own localhost (docs/operations/ports.md). Nor is a
            // Feishu link the card shows natively: its page loads only on "Switch to web page" (its document is read ahead below).
            preloadUrls.mapNotNull { ChatLinks.webUrl(it) }
                .filter { PortLinks.link(it, ports.value?.hostnames.orEmpty()) == null && larkNativeLink(it, lark) == null }
                .take(3).forEach { pool.preload(it) }
        }
    }
    // Feishu documents among the links are read ahead into the reader's cache, as on the web (iOS skips them too).
    val documentsAhead = remember(preloadUrls, lark) {
        preloadUrls.mapNotNull { ChatLinks.webUrl(it) }.filter { larkNativeLink(it, lark) is LarkNativeLink.Document }.take(3)
    }
    if (documentsAhead.isNotEmpty() && !activeScroll && lifecycleState.isAtLeast(Lifecycle.State.STARTED)) {
        LarkDocumentPrefetch(documentsAhead, manager)
    }
}

/** The link card's room in the host, in pixels: its height and where the card's top stops. */
private data class LinkGeometry(val pixels: Float, val above: Float)
