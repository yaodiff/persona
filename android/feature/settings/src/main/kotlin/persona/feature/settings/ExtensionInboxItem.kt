package persona.feature.settings

import androidx.compose.ui.platform.LocalResources
import android.content.res.Resources
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.runtime.*
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiForbidden
import persona.core.contracts.api.ManagerPins
import persona.core.contracts.api.pinned
import persona.core.designsystem.*
import persona.core.navigation.InboxItemUiRoute
import kotlinx.coroutines.launch

// The body of an inbox item's page drawn by the extension of the connector it comes from (docs/extensions/extensions.md "UI slots"
// `inbox_item`, #1683): the extension's view of the item, drawn with the same renderer as its other pages, the item's id
// in the context. The inbox keeps its own header above it (feature:inbox's InboxItemExtensionScreen); the shell puts the
// two together and opens the item's own page when the extension cannot draw it.

/** Why an inbox item shows its own page instead of the extension's body. */
sealed interface InboxItemFallback {
    /** The extension does not draw this item here: a device that may only read (403), 404, `unsupported`, nothing drawn. */
    data object Quiet : InboxItemFallback

    /** Drawing it broke (502 `extension_failed`, the hub out of reach): the item's own page says so in a line, with "Retry". */
    data class Notice(val message: String) : InboxItemFallback
}

internal object InboxItemPageText {
    /**
     * Whether the item's own page takes over, and how: before the first view, a refusal is quiet and a breakdown is
     * told; a view with nothing this app draws is quiet. Once a view has been drawn, a failed refresh or button keeps
     * it and says so, as on the extension's other pages; only an extension that then says the item is not its own gives way.
     */
    fun fallback(res: Resources, state: ExtensionViewState): InboxItemFallback? {
        if (state.unsupported) return InboxItemFallback.Quiet
        val view = state.view
        if (view == null) {
            val failure = state.failure ?: return null
            if (state.loading) return null
            return if (failure is ExtensionUiForbidden) InboxItemFallback.Quiet
            else InboxItemFallback.Notice(res.getString(R.string.settings_ext_item_page_failed, failureReasonText(failure).resolve(res)))
        }
        return InboxItemFallback.Quiet.takeIf { view.sections.none { it.items.any(ExtensionViewText::drawable) } }
    }

    /** What a button may open on an item's page: https, and an inbox item's source. */
    fun opens(url: String): Boolean = ExtensionViewText.isHttps(url) || ExtensionViewText.inboxSource(url) != null
}

/**
 * The extension's body of an inbox item's page as it is now: the view's [title] (none until it has one), whether it is
 * being read for the first time ([loading]) or again ([refreshing]), the [fallback] when the extension cannot draw it,
 * and its [items] for the page's list.
 */
class InboxItemBody internal constructor(
    val title: String?,
    val loading: Boolean,
    val refreshing: Boolean,
    val fallback: InboxItemFallback?,
    val refresh: () -> Unit,
    val items: LazyListScope.() -> Unit,
)

/**
 * Item [route]'s body as its connector's extension draws it, refreshed while on screen when the view asks
 * (`refresh_after`) and drawn again whenever the item changes ([revision]: new messages, done). A button that answers
 * `persona://inbox/<id>/source` calls [onSource] with that item's id; other links are https and open in the app. No
 * extensions here (demo mode): the item's own page, quietly.
 *
 * The extension of the item's [manager] (its stamp in the merged list; null: none) draws it, buttons and controls
 * included: the same extension may be installed under several managers. It knows the item by its id on the hub,
 * [hubItemId], not the app's.
 */
@Composable
fun rememberInboxItemBody(slots: ExtensionSlots?, route: InboxItemUiRoute, revision: Any? = null, manager: String? = null,
    hubItemId: String = route.itemId, onSource: (String) -> Unit): InboxItemBody {
    val shared = slots?.api ?: return InboxItemBody(null, false, false, InboxItemFallback.Quiet, {}, {})
    val pins = remember(route.extensionId, manager) { ManagerPins.of(extensionPrefixes(route.extensionId), manager) }
    val api = remember(shared, pins) { shared.pinned(pins) }
    val model = remember(api, route, hubItemId) {
        ExtensionViewModel(api, route.extensionId, route.uiId, ExtensionUiContext(item = hubItemId, size = "full"), opens = InboxItemPageText::opens)
    }
    val state by model.state.collectAsState()
    val res = androidx.compose.ui.platform.LocalResources.current
    val scope = rememberCoroutineScope()
    val source by rememberUpdatedState(onSource)
    val handle = rememberExtensionView(model, appLink = { url -> ExtensionViewText.inboxSource(url)?.let { source(it); true } ?: false })
    // The item changed (a new message): a reply being typed in its page stays. The first draw has nothing typed yet.
    LaunchedEffect(model, revision) { model.load(keepTyped = true) }
    KeepFresh(model)
    val reload = remember(model) { { scope.launch { model.load() }; Unit } }
    return InboxItemBody(
        title = state.view?.title?.takeIf { it.isNotBlank() },
        loading = state.view == null && state.failure == null && !state.unsupported,
        refreshing = state.loading && state.view != null,
        fallback = InboxItemPageText.fallback(res, state),
        refresh = reload,
    ) {
        // A refresh or a button that failed after the view was drawn: one line above it.
        state.error?.takeIf { state.view != null }?.let { error ->
            item("item-body-error") { JInlineStatus(error.text(), tone = InlineTone.Failed) }

        }
        extensionViewItems("item-body", state, handle, compactPreview = true)
    }
}
