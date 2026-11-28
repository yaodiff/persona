package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.models.ModelPanel
import persona.core.domain.models.PanelConfig
import persona.core.domain.models.PanelModel
import persona.core.domain.models.PanelNote
import persona.core.domain.models.PanelOverride
import persona.core.domain.models.PanelPrefs
import persona.core.domain.models.PanelProvider
import persona.core.domain.models.PanelReason
import persona.core.domain.models.PanelRow
import persona.core.domain.models.PanelSection
import persona.core.domain.models.PanelState
import persona.core.domain.models.PanelView
import java.util.UUID

/** The words a chat gives its model panel for what only it knows: why rows are off, the efforts' and engines' names, the note. */
class ModelPanelWords(
    val reason: (PanelReason) -> String,
    val effort: (String) -> String,
    val engine: (String) -> String,
    val note: (PanelNote) -> String,
    val mark: (String) -> AgentMarkSpec?,
)

/**
 * The unified model panel (#5055, the web's `ui/model-panel/ModelPanel.tsx` from #5032, after Cindy's model selector;
 * DESIGN_SYSTEM.md 3.8 "Model pill and unified model panel"), as a sheet on the phone: [header] (a Persona chat's strip),
 * the search, the waiting switch's warning line, then a rail (Recent, Favorites, All, one tile per provider) beside the
 * two-line model rows, and "Configure models" at the foot. The model that runs has the selected fill, no check. A row's
 * star saves its setup as a favorite (always shown: there is no hover on a phone); its Customize opens the model's setup
 * page in the same sheet (the web's flyout): the thinking effort and, for the model that runs, [currentExtra]
 * (ultracode). A pick goes to [onPick], which applies it and closes the sheet once it did; while [picking] everything
 * waits. Favorites, Recent and the per-model setups are [prefs], kept by the caller ([onPrefs]).
 * [catalog] is the optional load state of the provider list, separate from each provider's models and [onReload].
 */
@Composable
fun JModelPanel(
    providers: List<PanelProvider>,
    state: PanelState,
    prefs: PanelPrefs,
    onPrefs: (PanelPrefs) -> Unit,
    words: ModelPanelWords,
    onPick: (PanelConfig) -> Unit,
    onDismiss: () -> Unit,
    picking: Boolean = false,
    header: (@Composable () -> Unit)? = null,
    /** An effort set on the model that runs, applied at once (the page stays open); null: its effort goes with a pick. */
    onApplyCurrent: ((String) -> Unit)? = null,
    currentExtra: (@Composable () -> Unit)? = null,
    onReload: ((String) -> Unit)? = null,
    onConfigure: (() -> Unit)? = null,
    catalog: JLoadable<*>? = null,
    onRetryCatalog: (() -> Unit)? = null,
) {
    var view by remember { mutableStateOf<PanelView>(PanelView.All) }
    var query by remember { mutableStateOf("") }
    var customizing by remember { mutableStateOf<String?>(null) }
    val sections = remember(providers, state, view, query, prefs) { ModelPanel.sections(providers, state, view, query, prefs) }
    val rows = sections.flatMap { it.rows }
    val cached = catalog as? JLoadable.Loaded<*>
    // Keep the retry visible when the lazy list holds its scroll position over the cached rows.
    val refreshFailure = cached?.failure?.takeIf { sections.isNotEmpty() }
    JSheet(onDismiss = onDismiss, showDragHandle = true, contentPadding = Tokens.Space.flush) {
        val open = customizing?.let { key -> rows.firstOrNull { it.key == key } }
        if (open != null) {
            SetupPage(open, prefs, onPrefs, words, live = onApplyCurrent != null && open.current, onApplyCurrent = onApplyCurrent,
                extra = if (open.current) currentExtra else null, onBack = { customizing = null })
            return@JSheet
        }
        header?.invoke()
        Column(Modifier.padding(horizontal = pagePadding), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JSearchField(query, { query = it }, stringResource(R.string.designsystem_modelpanel_search))
            state.note?.let { note ->
                Row(verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
                    JIcon(PhIcons.Warning, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.cautionText)
                    Text(words.note(note), style = PersonaTheme.type.micro, color = PersonaTheme.palette.cautionText)
                }
            }
            state.busy?.let { Text(words.reason(it), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2) }
            if (refreshFailure != null) {
                JNotice(refreshFailure, status = Status.NeedsYou, inlineAction = true, onAction = onRetryCatalog,
                    actionLabel = onRetryCatalog?.let { stringResource(R.string.designsystem_retry) })
            }
        }
        Spacer(Modifier.height(Tokens.Space.s))
        Row(Modifier.fillMaxWidth().height(Tokens.Size.box150 * 3)) {
            Rail(providers, view, query.isNotBlank(), onView = { view = it; query = "" })
            Box(Modifier.width(Tokens.Size.line).fillMaxHeight().background(PersonaTheme.palette.line))
            ModelList(sections, view, query, state, prefs, onPrefs, words, picking, onPick,
                onCustomize = { customizing = it.key }, live = onApplyCurrent != null, onReload = onReload,
                modifier = Modifier.weight(1f), catalog = if (refreshFailure != null) cached?.copy(failure = null) else catalog,
                onRetryCatalog = onRetryCatalog)
        }
        if (onConfigure != null) {
            JDivider()
            JTextButton(stringResource(R.string.designsystem_modelpanel_configure), { onDismiss(); onConfigure() },
                Modifier.padding(horizontal = pagePadding), icon = PhIcons.GearSix)
        }
    }
}

/** The rail (the web's `Rail`): Recent, Favorites, All, one letter tile per provider; a search shows All. */
@Composable
private fun Rail(providers: List<PanelProvider>, view: PanelView, searching: Boolean, onView: (PanelView) -> Unit) {
    val palette = PersonaTheme.palette
    @Composable
    fun Cell(label: String, on: Boolean, onClick: () -> Unit, content: @Composable () -> Unit) {
        Box(Modifier.size(Tokens.MinTouch.android).padding(Tokens.Space.xs).clip(RoundedCornerShape(Tokens.Radius.control))
            .then(if (on) Modifier.background(palette.surfaceStrong) else Modifier)
            .clickable(role = Role.Tab, onClick = onClick)
            .semantics { contentDescription = label; selected = on },
            contentAlignment = Alignment.Center) { content() }
    }
    val shown = if (searching) PanelView.All else view
    Column(Modifier.width(Tokens.MinTouch.android).fillMaxHeight().verticalScroll(rememberScrollState())) {
        Cell(stringResource(R.string.designsystem_modelpanel_recent), shown == PanelView.Recent, { onView(PanelView.Recent) }) {
            JIcon(PhIcons.Clock, null, size = Tokens.Size.iconXS, tint = palette.ink2)
        }
        Cell(stringResource(R.string.designsystem_modelpanel_favorites), shown == PanelView.Favorites, { onView(PanelView.Favorites) }) {
            JIcon(PhIcons.Star, null, size = Tokens.Size.iconXS, tint = palette.ink2)
        }
        Cell(stringResource(R.string.designsystem_modelpanel_all), shown == PanelView.All, { onView(PanelView.All) }) {
            JIcon(PhIcons.SquaresFour, null, size = Tokens.Size.iconXS, tint = palette.ink2)
        }
        providers.distinctBy { it.id }.forEach { p ->
            Cell(p.name, shown == PanelView.Provider(p.id), { onView(PanelView.Provider(p.id)) }) { JProviderTile(p.name, size = Tokens.Size.iconXS) }
        }
    }
}

@Composable
private fun ModelList(
    sections: List<PanelSection>, view: PanelView, query: String, state: PanelState, prefs: PanelPrefs, onPrefs: (PanelPrefs) -> Unit,
    words: ModelPanelWords, picking: Boolean, onPick: (PanelConfig) -> Unit, onCustomize: (PanelRow) -> Unit, live: Boolean,
    onReload: ((String) -> Unit)?, modifier: Modifier, catalog: JLoadable<*>?, onRetryCatalog: (() -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val list = rememberLazyListState()
    // opening on All: the model that runs in view (its group's header with it)
    val current = remember(sections) {
        var at = 0
        sections.firstNotNullOfOrNull { s ->
            val header = at
            at += 1 + s.rows.size
            header.takeIf { s.rows.any { it.current } }
        } ?: -1
    }
    LaunchedEffect(Unit) { if (current > 0) list.scrollToItem(current) }
    val content: LazyListScope.() -> Unit = {
        if (sections.isEmpty()) item("empty") {
            val words = when {
                query.isNotBlank() -> R.string.designsystem_modelpanel_no_match
                view == PanelView.Recent -> R.string.designsystem_modelpanel_recent_empty
                view == PanelView.Favorites -> R.string.designsystem_modelpanel_favorites_empty
                catalog != null -> R.string.designsystem_modelpanel_empty
                else -> R.string.designsystem_modelpanel_loading
            }
            Text(stringResource(words), Modifier.padding(Tokens.Space.l), style = PersonaTheme.type.caption, color = palette.ink2)
        }
        sections.forEach { section ->
            item("h:" + section.key) { GroupHeader(section, words, onReload) }
            items(section.rows, key = { it.key }) { row ->
                val block = ModelPanel.rowBlock(row.provider, state, row.current)
                val customizable = ModelPanel.rowCustomizable(row, state, live)
                val starred = row.favorite != null || row.starredUid != null ||
                    prefs.favorites.any { it.setup.same(row.setup) }
                ModelRow(row, second(row, section, view), block, customizable, starred, picking, words,
                    onPick = { if (block == null && !picking) onPick(row.setup) },
                    onStar = {
                        val uid = row.favorite?.uid ?: row.starredUid ?: prefs.favorites.firstOrNull { it.setup.same(row.setup) }?.uid
                        onPrefs(if (uid != null) prefs.removeFavorite(uid) else prefs.addFavorite(row.setup, UUID.randomUUID().toString()))
                    },
                    onCustomize = { onCustomize(row) })
            }
        }
    }
    val loadingText = stringResource(R.string.designsystem_modelpanel_loading)
    LazyColumn(modifier.fillMaxHeight().testTag("model-panel-list"), list) {
        if (catalog == null) content()
        else jLoadableContent(catalog, onRetryCatalog, loadingText, isEmpty = { sections.isEmpty() }, empty = content) { content() }
    }
}

/** A group's header: its name (or Favorites, Recent, Recommended), a spinner while its list is read, and why it is off. */
@Composable
private fun GroupHeader(section: PanelSection, words: ModelPanelWords, onReload: ((String) -> Unit)?) {
    val palette = PersonaTheme.palette
    val title = when (section.kind) {
        PanelSection.Kind.Favorites -> stringResource(R.string.designsystem_modelpanel_favorites)
        PanelSection.Kind.Recent -> stringResource(R.string.designsystem_modelpanel_recent)
        PanelSection.Kind.Recommended -> stringResource(R.string.designsystem_modelpanel_recommended)
        PanelSection.Kind.Provider -> section.provider?.name.orEmpty()
    }
    val provider = section.provider
    Column(Modifier.fillMaxWidth().padding(start = Tokens.Space.m, end = Tokens.Space.m, top = Tokens.Space.m, bottom = Tokens.Space.xs)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(title, Modifier.weight(1f, fill = false), style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold), color = palette.ink2,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (section.loading) JSpinner(Modifier.size(Tokens.Size.markS))
        }
        if (provider != null && section.kind == PanelSection.Kind.Provider) {
            val why = provider.unavailable ?: PanelReason.Locked.takeIf { provider.locked }
            if (why != null) Text(words.reason(why), style = PersonaTheme.type.micro, color = palette.ink3)
            else if (provider.models == null) provider.error?.let { error -> Row(verticalAlignment = Alignment.CenterVertically) {
                Text(error, Modifier.weight(1f), style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
                if (onReload != null) JTextButton(stringResource(R.string.designsystem_modelpanel_retry), { onReload(provider.id) })
            } }
        }
    }
}

/** A row's second line: where it comes from in All, Recent and Favorites, what the model is in a provider's own view. */
@Composable
private fun second(row: PanelRow, section: PanelSection, view: PanelView): String? = when {
    row.kind == PanelRow.Kind.Typed -> listOf(row.provider.name, stringResource(R.string.designsystem_modelpanel_use_typed_note)).joinToString(" · ")
    view is PanelView.Provider -> row.model.description
    else -> {
        val detail = if (section.kind == PanelSection.Kind.Provider)
            row.model.description?.takeIf { it.isNotBlank() } ?: row.model.resolved?.takeIf { it != row.model.id } else null
        listOfNotNull(row.provider.name, detail).joinToString(" · ")
    }
}

/** One two-line row (the web's `Row`): the provider's tile, the name, its star and Customize, then the engine and the effort. */
@Composable
private fun ModelRow(
    row: PanelRow, second: String?, block: PanelReason?, customizable: Boolean, starred: Boolean, picking: Boolean,
    words: ModelPanelWords, onPick: () -> Unit, onStar: () -> Unit, onCustomize: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val off = block != null || picking
    val name = if (row.kind == PanelRow.Kind.Typed) stringResource(R.string.designsystem_modelpanel_use_typed, row.model.id) else row.model.name
    val effort = when {
        row.reportsSessionEffort && row.shownEffort.isEmpty() -> stringResource(R.string.designsystem_status_unknown)
        row.reportsSessionEffort || row.model.efforts.isNotEmpty() -> row.shownEffort.takeIf(String::isNotEmpty)?.let(words.effort)
        else -> null
    }
    val setupWords = listOfNotNull(words.engine(row.provider.engine), effort).joinToString(" · ")
    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs).clip(RoundedCornerShape(Tokens.Radius.control))
        .then(if (row.current) Modifier.background(palette.surfaceStrong) else Modifier)
        .clickable(enabled = !off, role = Role.Button, onClick = onPick)
        .semantics { selected = row.current }
        .stableAlpha(if (block != null && !(row.current && customizable)) DIMMED else 1f)
        .padding(start = Tokens.Space.s, top = Tokens.Space.xs, bottom = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically) {
        JProviderTile(row.provider.name)
        Spacer(Modifier.width(Tokens.Space.s))
        Column(Modifier.weight(1f)) {
            Text(name, style = PersonaTheme.type.callout.copy(fontWeight = FontWeight.Medium), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            second?.takeIf(String::isNotBlank)?.let { Text(it, style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            modifier = Modifier.semantics { contentDescription = setupWords }) {
            words.mark(row.provider.engine)?.let { JAgentMark(it, size = Tokens.Size.markXS) }
            effort?.let { Text(it, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, softWrap = false) }
        }
        if (row.kind != PanelRow.Kind.Typed) JIconButton(PhIcons.Star,
            stringResource(if (starred) R.string.designsystem_modelpanel_remove_favorite else R.string.designsystem_modelpanel_save_favorite),
            onStar, enabled = !picking && (block == null || starred), tint = if (starred) palette.cautionText else palette.ink3, selected = starred)
        if (customizable) JIconButton(PhIcons.SlidersHorizontal, stringResource(R.string.designsystem_modelpanel_customize), onCustomize,
            enabled = !picking, tint = palette.ink3)
        else Spacer(Modifier.width(Tokens.Space.s))
    }
}

private const val DIMMED = 0.5f

/**
 * A model's setup (the web's flyout, a page of the sheet on the phone): the thinking effort, the chat's [extra] for the
 * model that runs, and whether the setup is the recommended one, customized or saved. The model that runs applies its
 * effort at once when the chat can ([live]); any other keeps it as that model's own setup, which picking its row sends.
 */
@Composable
private fun SetupPage(
    row: PanelRow, prefs: PanelPrefs, onPrefs: (PanelPrefs) -> Unit, words: ModelPanelWords, live: Boolean,
    onApplyCurrent: ((String) -> Unit)?, extra: (@Composable () -> Unit)?, onBack: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val model: PanelModel = row.model
    val shown = row.shownEffort
    val edit = row.effortEdit(live)
    Column(Modifier.padding(horizontal = pagePadding).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            JIconButton(PhIcons.CaretLeft, stringResource(R.string.designsystem_back), onBack)
            Text(stringResource(R.string.designsystem_modelpanel_setup_of, model.name), Modifier.weight(1f), style = PersonaTheme.type.title,
                color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JProviderTile(row.provider.name)
            Text(listOf(row.provider.name, words.engine(row.provider.engine)).joinToString(" · "), style = PersonaTheme.type.caption, color = palette.ink2)
        }
        if (edit == PanelRow.EffortEdit.ReadOnly) {
            val label = shown.takeIf(String::isNotEmpty)?.let(words.effort) ?: stringResource(R.string.designsystem_status_unknown)
            Text(stringResource(R.string.designsystem_modelpanel_effort) + " · " + label,
                style = PersonaTheme.type.callout, color = palette.ink)
            extra?.invoke()
            return@Column
        }
        if (model.efforts.isNotEmpty()) {
            val index = model.efforts.indexOf(shown).coerceAtLeast(0)
            Text(stringResource(R.string.designsystem_modelpanel_effort) + " · " + words.effort(model.efforts[index]) +
                (if (model.efforts[index] == model.defaultEffort) " · " + stringResource(R.string.designsystem_modelpanel_recommended) else ""),
                style = PersonaTheme.type.callout, color = palette.ink)
            if (model.efforts.size > 1) {
                JSlider(index.toFloat(), { value ->
                    val effort = model.efforts[value.toInt().coerceIn(0, model.efforts.lastIndex)]
                    if (effort == shown) return@JSlider
                    if (edit == PanelRow.EffortEdit.Live) onApplyCurrent?.invoke(effort)
                    else onPrefs(prefs.setOverride(row.provider.id, model.id, PanelOverride(effort = effort)))
                }, valueRange = 0f..model.efforts.lastIndex.toFloat(), step = 1f,
                    contentDescription = stringResource(R.string.designsystem_modelpanel_effort))
                Row {
                    Text(stringResource(R.string.designsystem_modelpanel_faster), Modifier.weight(1f), style = PersonaTheme.type.micro, color = palette.ink3)
                    Text(stringResource(R.string.designsystem_modelpanel_smarter), style = PersonaTheme.type.micro, color = palette.ink3)
                }
            }
        }
        extra?.invoke()
        JDivider()
        val override = prefs.overrides[PanelPrefs.overrideKey(row.provider.id, model.id)]
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(when {
                row.kind == PanelRow.Kind.Favorite -> R.string.designsystem_modelpanel_setup_saved
                override != null -> R.string.designsystem_modelpanel_setup_customized
                else -> R.string.designsystem_modelpanel_setup_recommended
            }), Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink2)
            if (override != null && edit == PanelRow.EffortEdit.Preference) JTextButton(stringResource(R.string.designsystem_modelpanel_reset),
                { onPrefs(prefs.resetOverride(row.provider.id, model.id)) })
        }
        Spacer(Modifier.height(Tokens.Space.s))
    }
}
