package persona.core.designsystem

import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons

/** How many rows an opened queue shows before it scrolls (iOS `QueueStrip.visibleRows`). */
const val QUEUE_VISIBLE_ROWS = 3

/**
 * How tall an opened queue's list may be, in pixels, from the height its rows take together (spacing included): about
 * [QUEUE_VISIBLE_ROWS] of them, all of them when there are fewer (iOS measures one row and multiplies). Null until it
 * was measured.
 */
fun queueListCap(rowsHeight: Int, count: Int): Int? =
    if (rowsHeight <= 0 || count <= 0) null else rowsHeight * minOf(count, QUEUE_VISIBLE_ROWS) / count

/**
 * Messages typed while the agent works and not taken yet, right above the composer (iOS QueueStrip): [title] says how
 * many and that they go in order ("2 queued · Claude will handle them in order"), then each one numbered, at most two
 * lines. Tapping one hands its index to [onItem] (its actions). While an operation runs ([busy]) a spinner shows and
 * nothing can be tapped; [enabled] false shows the queue without actions. A long queue scrolls under the title.
 *
 * Glass is iOS's strip: folded to the title line by default, which opens and folds it as iOS's disclosure does
 * ([initiallyExpanded] opens it from the start: catalog, screenshots); opened, each message sits in a well and about
 * [QUEUE_VISIBLE_ROWS] show before it scrolls. Its line carries [sendNow], a text button folded or open, for
 * [onSendNow] (iOS Send Now). [sendNowDescription], when set, is spoken with that button and is not drawn. Brief (a paper
 * card) and the board (a bordered ticket) stay always open, without Send now (the alignment plan's ground rules: no new
 * alignment work outside Glass). In every style a row with a note in [notes]
 * (by index) says it in the caption size under its words (iOS's "Arrives when this chat is free" under a message
 * relayed from another conversation); without notes Brief and Board draw as before. Glass also says how many photos a row's
 * message carries ([photos] by index, iOS's "N images" under the words); a row whose [items] words are empty draws just that.
 *
 * Messages relayed from other conversations are not mixed into [items]: since #6394 they arrive as [relayedItems] and
 * collapse into one summary line ("N messages from other conversations are waiting") in every style. Tapping the line
 * expands them, read-only; tapping one hands its index in the whole queue (the owner's count plus its position) to
 * [onRelayedItem], whose sheet offers copy only.
 */
@Composable
fun QueueStrip(
    title: String,
    items: List<String>,
    onItem: (Int) -> Unit,
    modifier: Modifier = Modifier,
    busy: Boolean = false,
    enabled: Boolean = true,
    sendNow: String? = null,
    sendNowDescription: String? = null,
    onSendNow: () -> Unit = {},
    notes: List<String?> = emptyList(),
    initiallyExpanded: Boolean = false,
    photos: List<Int> = emptyList(),
    relayedItems: List<String> = emptyList(),
    relayedSummary: String? = null,
    onRelayedItem: (Int) -> Unit = {},
) {
    if (items.isEmpty() && relayedItems.isEmpty()) return
    if (PersonaTheme.style == AppStyle.Glass) GlassQueueStrip(title, items, onItem, modifier, busy, enabled, sendNow, sendNowDescription, onSendNow, notes, initiallyExpanded, photos, relayedItems, relayedSummary, onRelayedItem)
    else PlainQueueStrip(title, items, onItem, modifier, busy, enabled, notes, relayedItems, relayedSummary, onRelayedItem)
}

/** iOS's strip: the title line in the label size, then the rows as wells, about three of them before it scrolls. */
@Composable
private fun GlassQueueStrip(
    title: String, items: List<String>, onItem: (Int) -> Unit, modifier: Modifier, busy: Boolean, enabled: Boolean,
    sendNow: String?, sendNowDescription: String?, onSendNow: () -> Unit, notes: List<String?>, initiallyExpanded: Boolean, photos: List<Int>,
    relayedItems: List<String>, relayedSummary: String?, onRelayedItem: (Int) -> Unit,
) {
    var expanded by rememberSaveable { mutableStateOf(initiallyExpanded) }
    var relayedExpanded by remember { mutableStateOf(false) }
    val spoken = stringResource(R.string.designsystem_with_state, title,
        stringResource(if (expanded) R.string.designsystem_expanded else R.string.designsystem_collapsed))
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val active = enabled && !busy
    val line = type.micro.copy(fontWeight = FontWeight.SemiBold)
    Column(
        modifier.fillMaxWidth().padding(horizontal = 10.dp.dense, vertical = Tokens.Space.xs)
            .floatingSurface(RoundedCornerShape(Tokens.Radius.panel)).animateContentSize(PersonaTheme.motion.size()),
    ) {
        Row(
            Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)
                .clickable(role = Role.Button) { expanded = !expanded }.semantics { contentDescription = spoken }
                .padding(horizontal = Tokens.Space.m),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
        ) {
            JIcon(PhIcons.Tray, null, size = Tokens.Space.l, tint = palette.ink2)
            Text(title, style = line, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            if (busy) JSpinner(Modifier.size(Tokens.Space.Standard.m), stroke = Tokens.Space.Standard.xxs)
            else sendNow?.let { SendNowButton(it, sendNowDescription, line, onSendNow) }
            JIcon(if (expanded) PersonaIcons.expand else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3)
        }
        if (expanded) {
            var rowsHeight by remember { mutableIntStateOf(0) }
            // The cap bounds the owner's own rows; the relayed block stays outside the scroll so its rows are always
            // reachable, and its own summary line folds them (#6394).
            val cap = queueListCap(rowsHeight, items.size.coerceAtLeast(1))?.let { with(LocalDensity.current) { it.toDp() } }
            Box(
                Modifier.fillMaxWidth().padding(bottom = Tokens.Space.m).then(if (cap != null) Modifier.heightIn(max = cap) else Modifier)
                    .verticalScroll(rememberScrollState()),
            ) {
                Column(
                    Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m).onSizeChanged { rowsHeight = it.height },
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
                ) {
                    val itemActions = stringResource(R.string.designsystem_queue_actions)
                    items.forEachIndexed { index, text ->
                        val note = notes.getOrNull(index)
                        val count = photos.getOrNull(index) ?: 0
                        val photoWords = if (count > 0) pluralStringResource(R.plurals.designsystem_queue_photos, count, count) else null
                        val said = listOfNotNull(photoWords, note).joinToString(". ").ifEmpty { null }
                        val spoken = if (said == null) stringResource(R.string.designsystem_queue_item, index + 1, text)
                            else stringResource(R.string.designsystem_queue_item_note, index + 1, text, said)
                        Row(
                            Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.card)).background(palette.sunken)
                                .clickable(enabled = active, role = Role.Button, onClickLabel = itemActions) { onItem(index) }
                                .semantics(mergeDescendants = true) { contentDescription = spoken }
                                .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                            verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                        ) {
                            QueueNumber(index + 1)
                            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                                if (text.isNotEmpty()) Text(text, style = type.callout, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                // iOS: the photos a message carries, as a caption with the photo mark under its words
                                photoWords?.let {
                                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
                                        JIcon(PersonaIcons.photo, null, size = Tokens.Space.m, tint = palette.ink2, decorative = true)
                                        Text(it, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                    }
                                }
                                note?.let { Text(it, style = type.caption, color = palette.ink2) }
                            }
                            if (enabled) JIcon(PersonaIcons.more, null, size = Tokens.Space.l, tint = palette.ink3,
                                modifier = Modifier.padding(top = Tokens.Space.xxs))
                        }
                    }
                }
            }
            RelayedBlock(relayedItems, relayedSummary, items.size, relayedExpanded,
                onToggle = { relayedExpanded = it }, onRelayedItem)
        }
    }
}

/** Brief and Board: their card and ticket as before, always open; a row's note under its words. */
@Composable
private fun PlainQueueStrip(
    title: String, items: List<String>, onItem: (Int) -> Unit, modifier: Modifier, busy: Boolean, enabled: Boolean, notes: List<String?>,
    relayedItems: List<String>, relayedSummary: String?, onRelayedItem: (Int) -> Unit,
) {
    var relayedExpanded by remember { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = RoundedCornerShape(if (style == AppStyle.Brief) 20.dp.corner else 8.dp.corner)
    val outer = when (style) {
        AppStyle.Brief -> Modifier.padding(horizontal = 12.dp.dense, vertical = 4.dp.dense).clip(shape).background(palette.surface)
        else -> Modifier.padding(horizontal = 10.dp.dense, vertical = 4.dp.dense).clip(shape).background(palette.surface).border(1.dp, palette.line, shape)
    }
    val active = enabled && !busy
    Column(modifier.fillMaxWidth().then(outer).padding(top = 10.dp.dense, bottom = 4.dp.dense)) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 14.dp.dense), verticalAlignment = Alignment.CenterVertically) {
            JIcon(PhIcons.Tray, null, size = 16.dp, tint = palette.ink2)
            Spacer(Modifier.width(8.dp.dense))
            Text(title, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            if (busy) JSpinner(Modifier.size(14.dp), stroke = 1.8.dp)
        }
        Column(Modifier.fillMaxWidth().heightIn(max = 184.dp).verticalScroll(rememberScrollState()).padding(top = 4.dp.dense)) {
            val itemActions = stringResource(R.string.designsystem_queue_actions)
            items.forEachIndexed { index, text ->
                val note = notes.getOrNull(index)
                val spoken = if (note == null) stringResource(R.string.designsystem_queue_item, index + 1, text)
                    else stringResource(R.string.designsystem_queue_item_note, index + 1, text, note)
                if (index > 0) JDivider(Modifier.padding(start = 50.dp.dense, end = 14.dp.dense))
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 44.dp.row)
                        .clickable(enabled = active, role = Role.Button, onClickLabel = itemActions) { onItem(index) }
                        .semantics(mergeDescendants = true) { contentDescription = spoken }
                        .padding(horizontal = 14.dp.dense, vertical = 8.dp.dense),
                    verticalAlignment = Alignment.Top,
                ) {
                    QueueNumber(index + 1)
                    Spacer(Modifier.width(12.dp.dense))
                    val words = Modifier.weight(1f).padding(top = 1.dp.dense)
                    if (note == null) Text(text, style = PersonaTheme.type.callout, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis,
                        modifier = words)
                    else Column(words, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                        Text(text, style = PersonaTheme.type.callout, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                        Text(note, style = PersonaTheme.type.caption, color = palette.ink2)
                    }
                    if (enabled) {
                        Spacer(Modifier.width(8.dp.dense))
                        JIcon(PhIcons.DotsThree, null, size = 18.dp, tint = palette.ink3, modifier = Modifier.padding(top = 2.dp.dense))
                    }
                }
            }
            RelayedBlock(relayedItems, relayedSummary, items.size, relayedExpanded, onToggle = { relayedExpanded = it }, onRelayedItem)
        }
    }
}

/**
 * The messages other conversations sent in, collapsed to one summary line in every style (#6394). Tapping the line
 * expands the read-only rows; their numbers continue after the owner's own ([ownCount]), and a tap hands the row's
 * whole-queue index to [onRelayedItem] (its sheet offers copy only).
 */
@Composable
private fun RelayedBlock(
    relayedItems: List<String>,
    relayedSummary: String?,
    ownCount: Int,
    expanded: Boolean,
    onToggle: (Boolean) -> Unit,
    onRelayedItem: (Int) -> Unit,
) {
    if (relayedItems.isEmpty() || relayedSummary == null) return
    val palette = PersonaTheme.palette
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.sm)) {
        Row(
            Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.card)).clickable(role = Role.Button) { onToggle(!expanded) }
                .semantics { contentDescription = relayedSummary }
                .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            JIcon(PhIcons.Tray, null, size = Tokens.Space.l, tint = palette.ink2, decorative = true)
            Text(relayedSummary, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold),
                color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
            JIcon(if (expanded) PersonaIcons.expand else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3)
        }
        if (expanded) {
            relayedItems.forEachIndexed { pos, text ->
                val index = ownCount + pos
                val spoken = stringResource(R.string.designsystem_queue_item, index + 1, text)
                Row(
                    Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.card)).background(palette.sunken)
                        .clickable(role = Role.Button) { onRelayedItem(index) }
                        .semantics(mergeDescendants = true) { contentDescription = spoken }
                        .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                    verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                ) {
                    QueueNumber(index + 1)
                    Text(text, style = PersonaTheme.type.callout, color = palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f))
                }
            }
        }
    }
}

/**
 * Send Now on Glass's title line: the accent's words, as iOS's plain button, with the platform's touch target around
 * them. [description] is the button's spoken state (what a confirmation used to say) and is not drawn (#7170).
 */
@Composable
private fun SendNowButton(label: String, description: String?, style: TextStyle, onClick: () -> Unit) {
    val spoken = if (description.isNullOrEmpty()) Modifier else Modifier.semantics { stateDescription = description }
    Box(
        Modifier.minimumInteractiveComponentSize().clip(PersonaTheme.shapes.controlShape)
            .clickable(role = Role.Button, onClick = onClick).then(spoken)
            .padding(horizontal = Tokens.Space.xs),
        contentAlignment = Alignment.Center,
    ) { Text(label, style = style, color = PersonaTheme.palette.accent, maxLines = 1, softWrap = false) }
}

/**
 * The place of a queued message: an accent disc in glass (iOS's, `Theme.Size.badge`, growing with the text size as its
 * `scaledFrame` does, up to 1.6 times), ink on a grey disc in brief, a small square on the board.
 */
@Composable
private fun QueueNumber(number: Int) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val (fill, ink) = when (style) {
        AppStyle.Brief -> palette.surfaceStrong to palette.ink
        AppStyle.Glass, AppStyle.Board -> palette.accentSoft to palette.accent
    }
    Box(
        Modifier.size(if (style == AppStyle.Glass) Tokens.Size.badge * LocalDensity.current.fontScale.coerceIn(1f, HomeTokens.shellScaleMaximum) else 24.dp)
            .clip(if (style == AppStyle.Board) RoundedCornerShape(4.dp) else CircleShape).background(fill),
        contentAlignment = Alignment.Center,
    ) { Text("$number", style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Bold), color = ink, maxLines = 1) }
}
