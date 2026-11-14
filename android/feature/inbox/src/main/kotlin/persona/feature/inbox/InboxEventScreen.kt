package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxTier
import persona.core.navigation.Navigator

/**
 * An inbox item's detail: source-specific meta and "View original", the current state, the suggestion, ✦ Summary, Recap,
 * then the Handling chain (its three newest entries, all once expanded) and Related. Items with event fields, and legacy
 * chat/mail items whose originals must stay unmounted until an explicit source action, open here ([inboxRowPage]). Record
 * lookup and its loading / missing / failed states belong to the history package.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun InboxEventScreen(repository: InboxRepository, route: InboxEventRoute, navigator: Navigator) {
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val listedRecord = inbox.records.inboxRecord(route.itemId)
    var lookup by remember(route.itemId) { mutableStateOf<InboxItemLookupResult?>(null) }
    var lookupAttempt by remember(route.itemId) { mutableIntStateOf(0) }
    LaunchedEffect(repository, route.itemId, listedRecord, lookupAttempt) {
        if (listedRecord == null) {
            lookup = null
            lookup = readInboxItem(repository, route.itemId)
        } else lookup = null
    }
    val record = listedRecord ?: (lookup as? InboxItemLookupResult.Found)?.record
    val pages = LocalInboxItemPages.current
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    var markingDone by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var stale by rememberSaveable(route.itemId) { mutableStateOf(false) }
    var lineageRevision by remember(route.itemId) { mutableIntStateOf(0) }
    val open = LocalLarkLinkOpener.current
    val clock = LocalInboxClock.current
    val zone = LocalInboxZone.current
    val nowText = record?.let { inboxNowForRow(it, res, currentLocale, now = clock(), zone = zone) }
    val oldMark = record?.let { eventMark(it) }
    val processingAction: (@Composable () -> Unit)? = record?.let { current ->
        inboxProcessingSession(current)?.let { session ->
            {
                JTextButton(stringResource(R.string.inbox_event_processing_chat), onClick = {
                    navigator.navigate(inboxConversationRoute(session.machineId, session.sessionId, current.item.id))
                })
            }
        }
    }
    val sayAction: (@Composable () -> Unit)? = record?.takeIf { !it.item.isDone && it.event.suggestion == null }?.let { current ->
        { InboxSayAction(repository, current, oldMark, onStale = { stale = it }) }
    }
    val stateActions: (@Composable () -> Unit)? = if (sayAction == null) processingAction else ({
        FlowRow {
            processingAction?.invoke()
            sayAction.invoke()
        }
    })
    fun go(target: ChainTarget) {
        val shown = record ?: return
        when (target) {
            is ChainTarget.Page -> navigator.navigate(target.key)
            ChainTarget.Source -> navigator.navigate(pages.open(shown))
            is ChainTarget.Url -> open?.invoke(target.url, inbox.contentSource)
            is ChainTarget.Item -> navigator.navigate(inbox.records.firstOrNull { it.item.id == target.id }
                ?.let { inboxRowPage(it, pages) } ?: InboxEventRoute(target.id))
            ChainTarget.Inline, ChainTarget.None -> Unit
        }
    }
    JListPage(
        title = record?.item?.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.inbox_item),
        onBack = navigator::back, largeTitle = false, refreshing = refreshing,
        onRefresh = {
            scope.launch {
                refreshing = true
                try {
                    repository.refreshNow()
                    if (record == null) lookupAttempt++
                } catch (e: CancellationException) { throw e } catch (_: Exception) { } finally { refreshing = false }
            }
        },
        actions = {
            if (record != null) JIconButton(PhIcons.CheckCircle, stringResource(if (record.item.isDone) R.string.inbox_move_back else R.string.inbox_done),
                enabled = !markingDone, onClick = {
                    markingDone = true
                    scope.launch {
                        try { repository.setDone(record.item.id, !record.item.isDone); error = null; navigator.back() }
                        catch (e: CancellationException) { throw e }
                        catch (e: Exception) { if (feedback?.failure(e, operation = true) != true) error = res.getString(R.string.inbox_failed_retry) }
                        finally { markingDone = false }
                    }
                })
            if (record != null) InboxSameMatterMenu(repository, record, onLinked = { lineageRevision++ })
        },
    ) {
        if (record == null) {
            when (val result = lookup) {
                null -> item("event-loading") { JLoading(text = stringResource(R.string.inbox_loading)) }
                is InboxItemLookupResult.Failed -> item("event-failed") {
                    JNotice(stringResource(R.string.inbox_refresh_failed), actionLabel = stringResource(R.string.inbox_retry),
                        onAction = { lookupAttempt++ })
                }
                InboxItemLookupResult.Missing -> item("event-missing") { StatusText(stringResource(R.string.inbox_item_missing)) }
                is InboxItemLookupResult.Found -> Unit
            }
            return@JListPage
        }
        item("event-source") {
            val translated = rememberItemTranslation(record)
            val meta = inboxRowThirdLine(record, res, translated?.preview?.ifBlank { null })
            Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    if (meta.isNotBlank()) Text(meta, Modifier.weight(1f), style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
                    else Spacer(Modifier.weight(1f))
                    JTextButton(stringResource(R.string.inbox_event_view_source), { navigator.navigate(pages.open(record)) }, icon = PhIcons.ArrowSquareOut)
                }
            }
        }
        val suggestion = record.event.suggestion
        if (nowText != null) item("event-state") {
            EventStateLine(EventMark.Now(nowText, inboxNowStatus(record.event.now), problem = record.event.now == "failed",
                key = record.event.now), Modifier.padding(bottom = Tokens.Space.s), stateActions)
        } else if (oldMark != null && oldMark !is EventMark.Suggests) item("event-state") {
            EventStateLine(oldMark, Modifier.padding(bottom = Tokens.Space.s), stateActions)
        } else item("event-state") {
            EventFallbackState(record, Modifier.padding(bottom = Tokens.Space.s), stateActions)
        }
        record.item.summary?.takeIf { it.isNotBlank() }?.let { summary ->
            item("event-summary") {
                Row(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    JIcon(PersonaIcons.ai, null, size = Tokens.Space.m, tint = PersonaTheme.palette.accent, decorative = true)
                    Text(summary, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                }
            }
        }
        // "Suggestion updated" stays over the new suggestion until the user answers again (the card is drawn anew for it).
        if (stale && !record.item.isDone) item("event-stale") {
            JNotice(stringResource(R.string.inbox_suggestion_stale), Modifier.padding(bottom = Tokens.Space.s), icon = PhIcons.ArrowsClockwise)
        }
        if (suggestion != null && !record.item.isDone) item("event-suggestion:${suggestion.version}") {
            SuggestionCard(repository, record.item.id, suggestion, Modifier.padding(bottom = Tokens.Space.s),
                needsYou = record.item.needsYou, onStale = { stale = it })
        }
        error?.let { message -> item("event-error") { StatusText(message, isError = true) } }
        record.event.recap?.let { recap ->
            item("event-recap") {
                JCard(Modifier.padding(bottom = Tokens.Space.s)) {
                    Text(stringResource(R.string.inbox_event_recap), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                    Spacer(Modifier.padding(top = Tokens.Space.xs))
                    Text(recap, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                }
            }
        }
        eventChain(record, inbox.sessions, inbox.mutes, ::go)
        item("event-line") { InboxLineSection(repository, record, navigator, reload = lineageRevision) }
        if (record.event.related.isNotEmpty()) {
            item("event-related-header") { JSectionHeader(stringResource(R.string.inbox_event_related), count = record.event.related.size) }
            itemsIndexed(record.event.related, key = { _, it -> "event-related:${it.id}" }) { index, related ->
                JGroupItem(GroupPosition.of(index, record.event.related.size)) {
                    JRow(related.title.ifBlank { related.id },
                        leadingIcon = if (related.source == "mail") PhIcons.EnvelopeSimple else PhIcons.ChatCircleDots,
                        onClick = { go(ChainTarget.Item(related.id)) })
                }
            }
        }
        item("event-end") { Spacer(Modifier.padding(top = Tokens.Space.xxl)) }
    }
}

/** The page's first tag: the section the event is in (In progress says who is on it), none for a done one. */
@Composable
private fun eventTag(record: InboxRecord): Pair<String, Status?>? {
    val item = record.item
    // The manager filed it (looked and found nothing for the user): it says so rather than a bare Done.
    if (item.isDone) return stringResource(if (record.event.doneBy == "manager") R.string.inbox_done_by_manager else R.string.inbox_done) to null
    return when (item.section) {
        "working" -> stringResource(R.string.inbox_section_working) to Status.Working
        InboxTier.Action.wire -> stringResource(R.string.inbox_bay_needs_you) to Status.NeedsYou
        InboxTier.Fyi.wire -> inboxTierTitle(InboxTier.Fyi) to null
        InboxTier.Noise.wire -> inboxTierTitle(InboxTier.Noise) to null
        else -> null
    }
}

/** A state line even for an older hub's suggestion, tier-only or done item: reuse the page's existing words. */
@Composable
private fun EventFallbackState(record: InboxRecord, modifier: Modifier = Modifier, action: (@Composable () -> Unit)? = null) {
    val (word, status) = eventTag(record) ?: return
    JInlineStatus(word, modifier, tone = when (status) {
        Status.Working -> InlineTone.Busy
        Status.NeedsYou -> InlineTone.Caution
        else -> InlineTone.Done
    }, action = action)
}

/** Where the processing stands, as one line: who is on it, "AI is preparing a suggestion", or that it failed. */
@Composable
internal fun EventStateLine(mark: EventMark, modifier: Modifier = Modifier, action: (@Composable () -> Unit)? = null) {
    when (mark) {
        is EventMark.Working -> AdaptiveEventStateLine(stringResource(mark.word), modifier, InlineTone.Busy, action = action)
        EventMark.Preparing -> AdaptiveEventStateLine(stringResource(R.string.inbox_handling_preparing), modifier,
            InlineTone.Busy, action = action)
        is EventMark.Failed -> AdaptiveEventStateLine(mark.reason?.let { stringResource(R.string.inbox_handling_failed_reason, it) }
            ?: stringResource(R.string.inbox_handling_failed), modifier, InlineTone.Failed, action = action)
        is EventMark.Suggests -> Unit
        is EventMark.Now -> {
            val icon = inboxNowIcon(mark.key)
            AdaptiveEventStateLine(mark.text, modifier, icon.tone, icon.icon, action)
        }
    }
}

/** Keep the existing inline status while its whole sentence and actions fit; otherwise give the actions the next line. */
@Composable
private fun AdaptiveEventStateLine(
    text: String,
    modifier: Modifier = Modifier,
    tone: InlineTone,
    icon: PhIcon? = null,
    action: (@Composable () -> Unit)? = null,
) {
    if (action == null) {
        JInlineStatus(text, modifier, tone = tone, icon = icon)
        return
    }
    val textMeasurer = rememberTextMeasurer()
    val textStyle = PersonaTheme.type.caption
    var actionWidth by remember(text, LocalDensity.current) { mutableIntStateOf(0) }
    // Keep the widest real measurement: actions that wrap must not make the inline / stacked choice oscillate.
    val measuredAction: @Composable () -> Unit = {
        Box(Modifier.onSizeChanged { actionWidth = maxOf(actionWidth, it.width) }) { action() }
    }
    SubcomposeLayout(modifier) { constraints ->
        val textWidth = textMeasurer.measure(AnnotatedString(text), style = textStyle, maxLines = 1, softWrap = false).size.width
        // Probe only the icon, spacing and padding; the real sentence and actions enter semantics exactly once.
        val chromeWidth = subcompose("event-state-chrome-width") {
            JInlineStatus("", Modifier.width(IntrinsicSize.Max), tone = tone, icon = icon, action = { Spacer(Modifier) })
        }.single().measure(constraints.copy(minWidth = 0, maxWidth = Constraints.Infinity)).width
        val stacked = chromeWidth + textWidth + actionWidth > constraints.maxWidth
        val content = subcompose("event-state-content") {
            if (!stacked) {
                JInlineStatus(text, Modifier.fillMaxWidth(), tone = tone, icon = icon, action = measuredAction)
            } else {
                val palette = PersonaTheme.palette
                val ink = when (tone) {
                    InlineTone.Busy -> palette.ink2
                    InlineTone.Caution -> palette.cautionText
                    InlineTone.Failed -> palette.danger.text
                    InlineTone.Done -> palette.done.text
                }
                Column(Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape)
                    .background(PersonaTheme.palette.statusSurface)) {
                    Row(Modifier.fillMaxWidth().semantics(mergeDescendants = true) {}
                        .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        when {
                            icon != null -> JIcon(icon, null, size = Tokens.Space.l, tint = ink)
                            tone == InlineTone.Busy -> JSpinner(Modifier.size(Tokens.Size.markS), color = ink,
                                stroke = SpinnerTokens.compactStroke)
                            tone == InlineTone.Caution -> JIcon(PhIcons.Warning, null, size = Tokens.Space.Standard.l,
                                tint = palette.caution)
                            tone == InlineTone.Failed -> JIcon(PhIcons.WarningCircle, null, size = Tokens.Space.Standard.l,
                                tint = palette.danger.dot)
                            tone == InlineTone.Done -> JIcon(PhIcons.CheckCircle, null, size = Tokens.Space.Standard.l,
                                tint = palette.positive)
                        }
                        Text(text, Modifier.weight(1f), style = textStyle, color = ink)
                    }
                    Row(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m).padding(bottom = Tokens.Space.xs),
                        horizontalArrangement = Arrangement.End) { measuredAction() }
                }
            }
        }.single().measure(constraints)
        layout(content.width, content.height) { content.place(0, 0) }
    }
}

/** A row's event line under its preview: the suggestion's kind and first line, or where the processing stands. */
@Composable
internal fun EventRowMark(mark: EventMark) {
    val palette = PersonaTheme.palette
    when (mark) {
        is EventMark.Suggests -> Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PhIcons.Sparkle, null, size = Tokens.Space.m, tint = palette.accent, decorative = true)
            Text(stringResource(R.string.inbox_suggestion_row, stringResource(mark.kind), mark.line), style = PersonaTheme.type.caption,
                color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        is EventMark.Working -> JStateLabel(stringResource(mark.word), Status.Working)
        EventMark.Preparing -> JStateLabel(stringResource(R.string.inbox_handling_preparing), Status.Working)
        is EventMark.Failed -> JStateLabel(stringResource(R.string.inbox_handling_failed), Status.NeedsYou, problem = true)
        is EventMark.Now -> Unit
    }
}

/**
 * The suggestion and its buttons. Edit opens [SuggestionEditor]; a stale suggestion (the hub has a newer one) says
 * "Suggestion updated" and the page redraws from the item the hub answers with. A retry of the same answer keeps its client id.
 */
@Composable
internal fun SuggestionCard(repository: InboxRepository, itemId: String, suggestion: InboxSuggestion, modifier: Modifier = Modifier,
    /** In Needs you the card is marked as the user's to decide; a conclusion in FYI is just read. */
    needsYou: Boolean = true,
    /** True when the answer found a newer suggestion (the page says "Suggestion updated"), false when the user answers again. */
    onStale: (Boolean) -> Unit = {},
) {
    val palette = PersonaTheme.palette
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val buttons = suggestionButtons(suggestion)
    var busy by remember(suggestion.version) { mutableStateOf<SuggestionAction?>(null) }
    var editing by rememberSaveable(suggestion.version) { mutableStateOf(false) }
    var notice by remember(suggestion.version) { mutableStateOf<String?>(null) }
    var last by remember(suggestion.version) { mutableStateOf<SuggestionRequest?>(null) }
    fun act(action: SuggestionAction, text: String?) {
        if (busy != null) return
        val request = last?.takeIf { it.action == action && it.text == text } ?: SuggestionRequest(itemId, suggestion.version, action, text)
        last = request
        busy = action
        onStale(false)
        scope.launch {
            try {
                val outcome = repository.actOnSuggestion(request)
                editing = false
                notice = null
                onStale(outcome == SuggestionOutcome.Stale)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e, operation = true) != true) notice = res.getString(R.string.inbox_failed_retry) }
            finally { busy = null }
        }
    }
    JCard(modifier, status = if (needsYou) Status.NeedsYou else null) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PhIcons.Sparkle, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
            Text(stringResource(suggestionKindLabel(suggestion)), style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
        }
        Spacer(Modifier.padding(top = Tokens.Space.xs))
        Text(suggestion.text, style = PersonaTheme.type.body, color = palette.ink)
        suggestion.note?.takeIf { it.isNotBlank() }?.let {
            Spacer(Modifier.padding(top = Tokens.Space.xs))
            Text(stringResource(R.string.inbox_suggestion_note, it), style = PersonaTheme.type.caption, color = palette.ink3)
        }
        notice?.let { JNotice(it, Modifier.padding(top = Tokens.Space.s), status = Status.NeedsYou) }
        Spacer(Modifier.padding(top = Tokens.Space.s))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JButton(stringResource(buttons.confirm), { act(SuggestionAction.Confirm, null) }, Modifier.weight(1f), size = JButtonSize.Small,
                enabled = busy == null, loading = busy == SuggestionAction.Confirm)
            if (buttons.editable) JButton(stringResource(R.string.inbox_suggestion_edit), { editing = true }, Modifier.weight(1f),
                kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = busy == null)
            JButton(stringResource(R.string.inbox_suggestion_reject), { act(SuggestionAction.Reject, null) }, Modifier.weight(1f),
                kind = JButtonKind.Quiet, size = JButtonSize.Small, enabled = busy == null, loading = busy == SuggestionAction.Reject)
        }
    }
    if (editing) SuggestionEditor(suggestion, buttons, busy,
        onConfirm = { text -> act(SuggestionAction.Confirm, confirmText(suggestion, text)) },
        onInstruct = { words -> act(SuggestionAction.Instruct, words.trim()) },
        onDismiss = { if (busy == null) editing = false })
}

/** Edit: the suggestion's text to change and confirm, or "Say something" to tell the processing conversation instead. */
@Composable
private fun SuggestionEditor(
    suggestion: InboxSuggestion,
    buttons: SuggestionButtons,
    busy: SuggestionAction?,
    onConfirm: (String) -> Unit,
    onInstruct: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    var text by rememberSaveable(suggestion.version) { mutableStateOf(suggestion.text) }
    var words by rememberSaveable(suggestion.version) { mutableStateOf("") }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_suggestion_editor_title), dismissible = busy == null, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(text, { text = it }, Modifier.fillMaxWidth(), label = stringResource(suggestionKindLabel(suggestion)), minLines = 3, maxLines = 10)
            JButton(stringResource(buttons.confirm), { onConfirm(text) }, Modifier.fillMaxWidth(), size = JButtonSize.Medium,
                enabled = busy == null && text.isNotBlank(), loading = busy == SuggestionAction.Confirm)
            JDivider()
            JTextField(words, { words = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.inbox_suggestion_say),
                placeholder = stringResource(R.string.inbox_suggestion_say_hint), minLines = 2)
            JButton(stringResource(R.string.inbox_suggestion_say_send), { onInstruct(words) }, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                size = JButtonSize.Medium, enabled = busy == null && words.isNotBlank(), loading = busy == SuggestionAction.Instruct)
        }
    }
}
