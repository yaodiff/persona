package persona.feature.settings

import android.app.Activity
import android.app.KeyguardManager
import android.content.Intent
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.core.app.RemoteInput
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import persona.core.contracts.GroupKeys
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.EXTENSION_NOTIFICATION_REMOTE_INPUT
import persona.core.contracts.api.ExtensionNotificationActionRecord
import persona.core.contracts.api.ExtensionNotificationActionStore
import persona.core.contracts.api.ExtensionNotificationClaim
import persona.core.contracts.api.ExtensionNotificationInspectionStatus
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.contracts.api.ExtensionViewPush
import persona.core.navigation.ExtensionUiRoute
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import persona.core.contracts.api.SUBSCRIPTIONS_UI
import persona.core.contracts.generated.ExtensionNotifyAction

// The places an extension's View is drawn (docs/extensions/extensions.md "Declarative UI"): a page of its own (extension_page, from the
// extension's page in Settings › Extensions; nav_page, from the "Extensions" section of Settings › Extensions) and a section on a launcher's
// page (launcher_section). The UI slots of Settings › Extensions and a machine's page are in ExtensionSlots.kt.

val LocalExtensionPageVisible = compositionLocalOf { true }

/**
 * Settings › Extensions › one › a page it draws, or a nav page opened from the settings home; or (UI version 3) a page the
 * extension pushed on another, first drawn with [initialState]. A push opens the next page through [onPush]; a pop goes
 * back, and the page underneath is drawn again when it shows.
 */
internal fun nativePageBinding(owner: persona.core.contracts.api.ExtensionStreamBinding?, manager: String?): persona.core.contracts.api.ExtensionStreamBinding? =
    owner?.let { binding -> manager?.takeIf { it.isNotBlank() }?.let { binding.copy(manager = it) } ?: binding }

internal data class ExtensionNotificationPageExecution(val action: ExtensionNotifyAction, val values: Map<String, String>)
internal enum class ExtensionNotificationPageError { Expired, WrongScope, Invalid, Locked }
internal data class ExtensionNotificationPageResolution(
    val record: ExtensionNotificationActionRecord? = null,
    val execution: ExtensionNotificationPageExecution? = null,
    val error: ExtensionNotificationPageError? = null,
) {
    val requiresHttp: Boolean get() = record?.action != null
}

/** A route is only navigation; the process-local record is what proves which declared action, if any, may run. */
internal suspend fun resolveExtensionNotificationPage(
    token: String,
    extensionId: String,
    uiId: String,
    manager: String?,
    inputText: String?,
    store: ExtensionNotificationActionStore,
    locked: suspend () -> Boolean = { false },
): ExtensionNotificationPageResolution {
    val inspection = store.inspect(token) ?: return ExtensionNotificationPageResolution()
    val record = inspection.record
    if (record.extension != extensionId || record.ui != uiId || manager != null && manager != record.scope.manager) {
        return ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.Invalid)
    }
    when (inspection.status) {
        ExtensionNotificationInspectionStatus.Expired -> return ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.Expired)
        ExtensionNotificationInspectionStatus.WrongScope -> return ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.WrongScope)
        ExtensionNotificationInspectionStatus.Current -> Unit
    }
    val action = record.action ?: return ExtensionNotificationPageResolution(record)
    if (action.foreground == true && locked()) {
        return ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.Locked)
    }
    val values = if (action.input == null) emptyMap() else mapOf("text" to inputText.orEmpty())
    return when (val claim = store.claim(token, action.id, values)) {
        is ExtensionNotificationClaim.Claimed -> ExtensionNotificationPageResolution(record,
            ExtensionNotificationPageExecution(action, claim.values))
        is ExtensionNotificationClaim.AlreadyHandled -> ExtensionNotificationPageResolution(record)
        is ExtensionNotificationClaim.Expired -> ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.Expired)
        is ExtensionNotificationClaim.WrongScope -> ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.WrongScope)
        is ExtensionNotificationClaim.Invalid -> ExtensionNotificationPageResolution(record, error = ExtensionNotificationPageError.Invalid)
        ExtensionNotificationClaim.Missing -> ExtensionNotificationPageResolution()
    }
}

/** SystemUI can deliver the intent just before KeyguardManager reports the accepted unlock; never bypass a real lock. */
internal suspend fun extensionNotificationLockedAfterUnlockSettles(
    locked: () -> Boolean,
    settleMillis: Long = 3_000,
    pollMillis: Long = 100,
): Boolean {
    if (!locked()) return false
    return withTimeoutOrNull(settleMillis) {
        while (locked()) delay(pollMillis)
        false
    } ?: true
}

@Composable
fun ExtensionUiPage(extensions: ExtensionManagement?, extensionId: String, uiId: String, title: String, onBack: () -> Unit,
                    initialState: String? = null, depth: Int = 0, shown: Boolean = true, availableAdapter: NativeExtensionStreamAdapter? = null,
                    onPush: (ExtensionUiRoute) -> Unit = {}, manager: String? = null, onAsk: ((String) -> Unit)? = null,
                    onCompose: ((String, String?) -> Unit)? = null,
                    onSettings: ((String, String?) -> Unit)? = null,
                    onConversation: ((String, String) -> Unit)? = null,
                    notificationActionToken: String? = null,
                    notificationIntent: Intent? = null,
                    notificationStore: ExtensionNotificationActionStore = ExtensionNotificationActionStore.shared,
                    notificationLocked: (suspend () -> Boolean)? = null) {
    // Depth 0 is the sidebar root: the shell's sidebar button replaces Back (iOS sidebarRootPage). Deeper pages keep Back.
    // onPop and "Set Up with…" still call [onBack]. On a lone root that switches to the conversation (AppNavigator.back).
    val barBack = if (depth == 0 && LocalIsSidebarRoot.current) null else onBack
    val notification = if (notificationActionToken == null) null else {
        val context = LocalContext.current
        val activity = context as? Activity
        val intent = notificationIntent ?: activity?.intent
        val keyguard = remember(context) { context.getSystemService(KeyguardManager::class.java) }
        val lifecycle = LocalLifecycleOwner.current.lifecycle
        var resume by remember(notificationActionToken) { mutableIntStateOf(0) }
        DisposableEffect(lifecycle, notificationActionToken) {
            val observer = LifecycleEventObserver { _, event ->
                if (event == Lifecycle.Event.ON_RESUME) resume++
            }
            lifecycle.addObserver(observer)
            onDispose { lifecycle.removeObserver(observer) }
        }
        produceState<ExtensionNotificationPageResolution?>(initialValue = null, notificationActionToken, notificationStore,
            intent, resume) {
            val text = intent?.let { RemoteInput.getResultsFromIntent(it) }
                ?.getCharSequence(EXTENSION_NOTIFICATION_REMOTE_INPUT)?.toString()
            val locked = notificationLocked ?: {
                try {
                    extensionNotificationLockedAfterUnlockSettles({ keyguard?.isDeviceLocked != false })
                } catch (cancelled: CancellationException) {
                    throw cancelled
                } catch (_: Exception) {
                    true
                }
            }
            value = resolveExtensionNotificationPage(notificationActionToken, extensionId, uiId, manager, text,
                notificationStore, locked)
        }.value
    }
    if (notificationActionToken != null && notification == null) {
        JListPage(title = title, onBack = barBack) { item { JLoading() } }
        return
    }
    notification?.error?.let { error ->
        val message = when (error) {
            ExtensionNotificationPageError.WrongScope -> R.string.settings_ext_notification_wrong_scope
            ExtensionNotificationPageError.Locked -> R.string.settings_ext_notification_unlock
            else -> R.string.settings_ext_notification_expired
        }
        JListPage(title = title, onBack = barBack) { item { JNotice(stringResource(message)) } }
        return
    }
    val pageManager = notification?.record?.scope?.manager ?: manager
    val pageState = notification?.record?.state ?: initialState
    val requiresHttp = notification?.requiresHttp == true
    // The source subscription pages (#2041), foreground notification actions and the hub's own ids ("-pages", #15017) are
    // drawn over HTTP only.
    val defaultBinding = extensions?.nativeStream?.collectAsState(initial = null)?.value
        ?.takeUnless { extensionId == SUBSCRIPTIONS_UI || requiresHttp || DashboardPages.httpOnly(extensionId) }
    val binding = remember(defaultBinding, pageManager) { nativePageBinding(defaultBinding, pageManager) }
    val owned = if (availableAdapter == null && binding != null && !requiresHttp) androidx.lifecycle.viewmodel.compose.viewModel(
        key = "native-extension:${binding.group}:${binding.owner}:${binding.manager}:${System.identityHashCode(binding.connection)}:$extensionId:$uiId:$depth",
        initializer = { ExtensionUIStreamController(binding, extensionId, uiId, pageState.orEmpty()) }) else null
    val adapter = if (requiresHttp || DashboardPages.httpOnly(extensionId)) null else availableAdapter ?: owned?.adapter
    if (owned != null) SideEffect { owned.adapter.shown = shown }
    val nativeState = adapter?.session?.state?.collectAsState()?.value
    val nativeNotice = nativeState?.unsupported?.isNotEmpty() == true
    val nativeUnsupported = nativeState?.let { it.unsupported.isNotEmpty() || it.phase == persona.core.contracts.stream.NativeStreamPhase.Closed } == true
    if (adapter != null && nativeUnsupported) LaunchedEffect(adapter) {
        // Compatibility fallback owns cleanup after the native renderer's scope has been removed.
        kotlinx.coroutines.withContext(kotlinx.coroutines.NonCancellable) {
            kotlinx.coroutines.withTimeoutOrNull(5000) { adapter.close() }
        }
    }
    if (adapter != null && !nativeUnsupported) {
        require(adapter.session.identity.extension == extensionId && adapter.session.identity.contribution == uiId)
        // The bar hides Back at a regular-width root; a stream pop still leaves the page (onBack).
        NativeExtensionStreamPage(adapter, title, onBack, showBack = barBack != null, onPush = { push ->
            ExtensionViewText.pushed(extensionId, uiId, title, depth, push, binding?.manager ?: pageManager)?.let(onPush)
        }, presentations = owned?.presentations)
        return
    }
    if (extensions == null) {
        JListPage(title = title, onBack = barBack) { item { JNotice(stringResource(if (nativeUnsupported) R.string.settings_ext_native_unsupported else R.string.settings_ext_demo_none)) } }
        return
    }
    val api = rememberPinned(extensions, extensionPrefixes(extensionId), pageManager)
    // Inbox Sources (#5278, #5661): these pages alone are told the composer's assistant name, even when it is blank.
    val assistantName = LocalPersonaName.current
    val pageContext = if (extensionId == SUBSCRIPTIONS_UI) ExtensionUiContext(assistant = assistantName.ifBlank { "" }) else null
    val model = remember(api, extensionId, uiId, pageState, pageContext) {
        ExtensionViewModel(api, extensionId, uiId, context = pageContext, initialState = pageState)
    }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val ask = onAsk
    val handle = rememberExtensionView(model, appLink = { url ->
        val settings = ExtensionViewText.settings(extensionId, url)
        if (settings != null && onSettings != null) {
            onSettings(settings, manager)
            true
        } else {
            val conversation = if (extensionId == "space-conversations") ExtensionViewText.spaceConversation(url) else null
            if (conversation != null && onConversation != null) {
                onConversation(conversation.first, conversation.second)
                true
            } else {
                val compose = ExtensionViewText.composeFrom(extensionId, url)
                if (compose != null && onCompose != null) {
                    // A page's own quick prompt goes to the assistant's composer, unsent. Close the page first.
                    onBack()
                    onCompose(compose, pageManager)
                    return@rememberExtensionView true
                }
                // Set Up with … is only answered here, and only from these pages. Close the page, then hand the text on.
                val text = ExtensionViewText.askFrom(extensionId, url) ?: return@rememberExtensionView false
                val send = ask ?: return@rememberExtensionView false
                onBack()
                send(text)
                true
            }
        }
    }, onPush = { push ->
        ExtensionViewText.pushed(extensionId, uiId, title, depth, push, pageManager)?.let { model.covered(); onPush(it) }
    }, onPop = onBack)
    var notificationActionStarted by remember(model) { mutableStateOf(false) }
    LaunchedEffect(model, notification) {
        val execution = notification?.execution
        if (execution != null) {
            notificationActionStarted = true
            handle.act(persona.core.contracts.api.ExtensionViewAction(execution.action.id), execution.values)
        } else if (!notificationActionStarted) {
            model.load()
        }
    }
    // A view with refresh_after is drawn again while this page is on screen (docs/extensions/extensions.md "UI slots" "Refresh"),
    // one the extension invalidated (ui.invalidate) at once or when it shows again, and one back from a page pushed on it.
    KeepFresh(model, extensions.uiEvents)
    val view = state.view
    val placeName = stringResource(R.string.settings_dashboards_title)
    val header = DashboardPages.headerTitle(extensionId, uiId, view?.title, title) { placeName }
    // UI version 8: a columns view takes the pane's whole width, as many columns of cards as it has room for
    ExtensionPageWidth(view) { columns, fullWidth ->
        JListPage(title = header, onBack = barBack, largeTitle = false,
            refreshing = state.loading && view != null, onRefresh = { scope.launch { model.load() } }, fullWidth = fullWidth) {
            if (DashboardPages.showsOwner(extensionId, uiId)) item("page-owner") { DashboardOwnerLine() }
            if (nativeNotice) item("native-unsupported") { JNotice(stringResource(R.string.settings_ext_native_unsupported)) }
            viewStatus("page", state, onRetry = { scope.launch { model.load() } })
            if (view != null && view.sections.none { it.items.any(ExtensionViewText::drawable) }) item("page-empty") {
                JEmptyState(stringResource(R.string.settings_ext_page_empty_title), stringResource(R.string.settings_ext_page_empty_text), icon = PhIcons.PuzzlePiece)
            }
            extensionViewItems("page", state, handle, columns = columns)
            item("page-end") { Spacer(Tokens.Space.xl) }
        }
    }
}

/**
 * Who keeps a Dashboards page (#15017), one line under its header at every width: the app's own words, so a page the
 * assistant drew cannot pass for one of the app's own screens.
 */
@Composable
private fun DashboardOwnerLine() {
    Text(stringResource(R.string.settings_dashboards_owner), Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xs),
        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
}

/** Loading, an error with "Retry" when there is nothing to show yet, or the error above what is shown. */
private fun LazyListScope.viewStatus(key: String, state: ExtensionViewState, onRetry: () -> Unit) {
    when {
        state.view == null && state.error != null -> item("$key-error") {
            JNotice(state.error.text(), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou,
                actionLabel = stringResource(R.string.settings_ext_retry), onAction = onRetry)
        }
        state.view == null && !state.unsupported -> item("$key-loading") { JLoading() }
        state.error != null -> item("$key-error") {
            JInlineStatus(state.error.text(), Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed)
        }
    }
}

@Composable
private fun Spacer(height: androidx.compose.ui.unit.Dp) = androidx.compose.foundation.layout.Spacer(Modifier.padding(top = height))

/** The pages an extension draws, listed on its page: a row each into it. */
internal fun LazyListScope.extensionPages(pages: List<ExtensionUiPlace>, onOpen: (ExtensionUiPlace) -> Unit) {
    if (pages.isEmpty()) return
    item("pages-title") { JSectionHeader(stringResource(R.string.settings_ext_pages)) }
    itemsIndexed(pages, key = { _, it -> "page-${it.id}" }) { index, page ->
        JGroupItem(GroupPosition.of(index, pages.size)) {
            JRow(page.title.ifBlank { page.id }, leadingIcon = PhIcons.SquaresFour, onClick = { onOpen(page) })
        }
    }
}

/**
 * A launcher of a machine (opened from its row on the machine's page): what it runs, its "Quota" at its gateway, and the
 * sections extensions draw for the gateway. A section whose extension says the gateway is not its own is left out without a word.
 * [machineName] names the machine that read the quota.
 */
@Composable
fun LauncherPage(machines: MachineManagement?, extensions: ExtensionManagement?, machineId: String, name: String, onBack: () -> Unit,
                 machineName: (String) -> String = { it }, onOpen: (ExtensionUiRoute) -> Unit = {}) {
    val scope = rememberCoroutineScope()
    val res = LocalResources.current
    var launcher by remember(machineId, name) { mutableStateOf<AgentLauncher?>(null) }
    var launcherError by remember(machineId, name) { mutableStateOf<String?>(null) }
    LaunchedEffect(machines, machineId, name) {
        machines ?: return@LaunchedEffect
        try {
            launcher = machines.launchers(machineId).firstOrNull { it.name == name }
            launcherError = if (launcher == null) res.getString(R.string.settings_ext_launcher_gone) else null
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) { launcherError = res.getString(R.string.settings_ext_launcher_failed, failureReasonText(error).resolve(res)) }
    }
    val quota = remember(machines, machineId, name) { machines?.let { LauncherQuotaModel(it, machineId, name) } }
    val quotaState = quota?.state?.collectAsState()?.value
    LaunchedEffect(quota) { quota?.load() }
    // The extension calls go to the hub, which knows its machines by their own ids (no group prefix).
    val context = remember(machineId, name) { ExtensionUiContext(launcher = name, machine = GroupKeys.raw(machineId)) }
    val places = remember(extensions, context) { extensions?.let { ExtensionPlacesModel(it, "launcher_section", context) } }
    val placesState = places?.state?.collectAsState()?.value ?: ExtensionPlacesState()
    val models = remember(extensions, placesState.places) {
        extensions?.let { api -> placesState.places.orEmpty().map { place -> place to ExtensionViewModel(api, place, context) } }.orEmpty()
    }
    LaunchedEffect(places) { places?.load() }
    val sections = models.map { (place, model) ->
        key(place.extension, place.id) {
            LaunchedEffect(model) { model.load() }
            KeepFresh(model, extensions?.uiEvents)
            Triple(place, model.state.collectAsState().value, rememberExtensionView(model, onPush = { onOpen(ExtensionViewText.opened(place, it)) }))
        }
    }
    fun reload() = scope.launch {
        quota?.let { launch { it.load(refresh = true) } }
        places?.load()
        models.forEach { (_, model) -> launch { model.load() } }
    }
    JListPage(title = name, onBack = onBack, largeTitle = false,
        refreshing = placesState.loading || sections.any { it.second.loading && it.second.view != null } ||
            quotaState?.let { it.loading && it.answer != null } == true, onRefresh = { reload() }) {
        item("launcher") {
            JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                JRow(name, leadingIcon = PhIcons.RocketLaunch, subtitle = stringResource(R.string.settings_ext_launcher))
            }
        }
        launcherError?.let { item("launcher-error") { JInlineStatus(it, Modifier.padding(top = Tokens.Space.s), tone = InlineTone.Failed) } }
        launcher?.let { l ->
            val rows = listOfNotNull(
                launcherModel(l)?.let { R.string.settings_ext_launcher_model to it },
                ExtensionViewText.host(l.baseUrl)?.let { R.string.settings_ext_launcher_gateway to it },
                R.string.settings_ext_launcher_type to if (l.agentType == "codex") "Codex" else "Claude Code",
            )
            item("about-title") { JSectionHeader(stringResource(R.string.settings_ext_launcher)) }
            itemsIndexed(rows, key = { _, it -> "about-${it.first}" }) { index, (label, value) ->
                JGroupItem(GroupPosition.of(index, rows.size)) { JRow(stringResource(label), value = value) }
            }
        }
        // How much is left comes before the extensions' sections, as on iOS and the web.
        if (quota != null && quotaState != null) launcherQuotaItems(quotaState, machineName, onRefresh = { scope.launch { quota.load(refresh = true) } })
        if (extensions != null) {
            placesState.error?.let { error ->
                item("places-error") {
                    Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JInlineStatus(error.text(), tone = InlineTone.Failed)
                        JButton(stringResource(R.string.settings_ext_retry),
 { scope.launch { places?.load() } }, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                            enabled = !placesState.loading)
                    }
                }
            }
            sections.forEach { (place, state, handle) ->
                if (state.unsupported) return@forEach
                val key = "ui-${place.extension}-${place.id}"
                // The heading waits for the view: an extension that turns out not to know this gateway never shows one.
                if (state.view == null && state.error == null) return@forEach
                item("$key-title") { JSectionHeader(state.view?.title?.takeIf { it.isNotBlank() } ?: place.title.ifBlank { place.extension }) }
                viewStatus(key, state, onRetry = { scope.launch { models.firstOrNull { it.first == place }?.second?.load() } })
                extensionViewItems(key, state, handle, compactPreview = true)
            }
            if (placesState.places == null && placesState.loading || sections.any { it.second.view == null && it.second.loading }) {
                item("places-loading") { JLoading() }
            }
        }
        item("end") { Spacer(Tokens.Space.xl) }
    }
}

/** "claude-opus-4" and "anthropic/claude-opus-4" both read as the last part of the path. */
private fun launcherModel(launcher: AgentLauncher): String? = launcher.model?.takeIf { it.isNotBlank() }?.substringAfterLast('/')
