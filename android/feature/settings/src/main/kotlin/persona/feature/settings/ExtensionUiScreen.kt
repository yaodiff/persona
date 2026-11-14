package persona.feature.settings

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import persona.core.contracts.GroupKeys
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.ExtensionManagement
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.contracts.api.ExtensionViewPush
import persona.core.navigation.ExtensionUiRoute
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.api.SUBSCRIPTIONS_UI

// The places an extension's View is drawn (docs/extensions/extensions.md "Declarative UI"): a page of its own (extension_page, from the
// extension's page in Settings › Extensions; nav_page, from the "Extensions" section of the settings home) and a section on a launcher's
// page (launcher_section). The UI slots of the settings home and a machine's page are in ExtensionSlots.kt.

val LocalExtensionPageVisible = compositionLocalOf { true }

/**
 * Settings › Extensions › one › a page it draws, or a nav page opened from the settings home; or (UI version 3) a page the
 * extension pushed on another, first drawn with [initialState]. A push opens the next page through [onPush]; a pop goes
 * back, and the page underneath is drawn again when it shows.
 */
internal fun nativePageBinding(owner: persona.core.contracts.api.ExtensionStreamBinding?, manager: String?): persona.core.contracts.api.ExtensionStreamBinding? =
    owner?.let { binding -> manager?.takeIf { it.isNotBlank() }?.let { binding.copy(manager = it) } ?: binding }

@Composable
fun ExtensionUiPage(extensions: ExtensionManagement?, extensionId: String, uiId: String, title: String, onBack: () -> Unit,
                    initialState: String? = null, depth: Int = 0, shown: Boolean = true, availableAdapter: NativeExtensionStreamAdapter? = null,
                    onPush: (ExtensionUiRoute) -> Unit = {}, manager: String? = null, onAsk: ((String) -> Unit)? = null) {
    // The source subscription pages (#2041) are the manager's, drawn over HTTP only.
    val defaultBinding = extensions?.nativeStream?.collectAsState(initial = null)?.value?.takeUnless { extensionId == SUBSCRIPTIONS_UI }
    val binding = remember(defaultBinding, manager) { nativePageBinding(defaultBinding, manager) }
    val owned = if (availableAdapter == null && binding != null) androidx.lifecycle.viewmodel.compose.viewModel(
        key = "native-extension:${binding.group}:${binding.owner}:${binding.manager}:${System.identityHashCode(binding.connection)}:$extensionId:$uiId:$depth",
        initializer = { ExtensionUIStreamController(binding, extensionId, uiId, initialState.orEmpty()) }) else null
    val adapter = availableAdapter ?: owned?.adapter
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
        NativeExtensionStreamPage(adapter, title, onBack, onPush = { push ->
            ExtensionViewText.pushed(extensionId, uiId, title, depth, push, binding?.manager ?: manager)?.let(onPush)
        }, presentations = owned?.presentations)
        return
    }
    if (extensions == null) {
        JListPage(title = title, onBack = onBack) { item { JNotice(stringResource(if (nativeUnsupported) R.string.settings_ext_native_unsupported else R.string.settings_ext_demo_none)) } }
        return
    }
    val api = rememberPinned(extensions, extensionPrefixes(extensionId), manager)
    // Inbox Sources (#5278, #5661): these pages alone are told the composer's assistant name, even when it is blank.
    val assistantName = LocalPersonaName.current
    val pageContext = if (extensionId == SUBSCRIPTIONS_UI) ExtensionUiContext(assistant = assistantName.ifBlank { "" }) else null
    val model = remember(api, extensionId, uiId, initialState, pageContext) {
        ExtensionViewModel(api, extensionId, uiId, context = pageContext, initialState = initialState)
    }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val ask = onAsk
    val handle = rememberExtensionView(model, appLink = { url ->
        // Set Up with … is only answered here, and only from these pages. Close the page, then hand the text on.
        val text = ExtensionViewText.askFrom(extensionId, url) ?: return@rememberExtensionView false
        val send = ask ?: return@rememberExtensionView false
        onBack()
        send(text)
        true
    }, onPush = { push ->
        ExtensionViewText.pushed(extensionId, uiId, title, depth, push, manager)?.let { model.covered(); onPush(it) }
    }, onPop = onBack)
    LaunchedEffect(model) { model.load() }
    // A view with refresh_after is drawn again while this page is on screen (docs/extensions/extensions.md "UI slots" "Refresh"),
    // one the extension invalidated (ui.invalidate) at once or when it shows again, and one back from a page pushed on it.
    KeepFresh(model, extensions.uiEvents)
    val view = state.view
    JListPage(title = view?.title?.takeIf { it.isNotBlank() } ?: title, onBack = onBack, largeTitle = false,
        refreshing = state.loading && view != null, onRefresh = { scope.launch { model.load() } }) {
        if (nativeNotice) item("native-unsupported") { JNotice(stringResource(R.string.settings_ext_native_unsupported)) }
        viewStatus("page", state, onRetry = { scope.launch { model.load() } })
        if (view != null && view.sections.none { it.items.any(ExtensionViewText::drawable) }) item("page-empty") {
            JEmptyState(stringResource(R.string.settings_ext_page_empty_title), stringResource(R.string.settings_ext_page_empty_text), icon = PhIcons.PuzzlePiece)
        }
        extensionViewItems("page", state, handle)
        item("page-end") { Spacer(Tokens.Space.xl) }
    }
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
                extensionViewItems(key, state, handle)
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
