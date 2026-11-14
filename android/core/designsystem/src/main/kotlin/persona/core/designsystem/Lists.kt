package persona.core.designsystem

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.*
import androidx.compose.ui.draw.rotate
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.DisableSelection
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.layout.SubcomposeLayout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.dp
import kotlin.math.roundToInt
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/** Paints a board holder down the left edge without asking the content for its intrinsic height. */
internal fun Modifier.holder(color: androidx.compose.ui.graphics.Color, width: Dp): Modifier = drawBehind {
    drawRect(color, size = androidx.compose.ui.geometry.Size(width.toPx(), size.height))
}

/** Horizontal page padding of the active style; list items assume they sit inside it. */
/** Side padding of page content, plus the room that keeps it to [ReadingWidth] in wide panes. */
val pagePadding: Dp @Composable get() = when (PersonaTheme.style) { AppStyle.Glass -> 16.dp; AppStyle.Brief -> 20.dp; AppStyle.Board -> 12.dp } * LayoutDensity.pageScale + LocalReadingInset.current

/** Where an item sits in a group, so lazy lists can draw one continuous container out of separate items. */
@Immutable
data class GroupPosition(val first: Boolean, val last: Boolean) {
    companion object {
        val Single = GroupPosition(first = true, last = true)
        fun of(index: Int, count: Int) = GroupPosition(first = index == 0, last = index == count - 1)
    }
}

/** A section heading inside a page: plain text in every style, with an optional count and trailing action. */
@Composable
fun JSectionHeader(
    title: String,
    modifier: Modifier = Modifier,
    count: Int? = null,
    status: Status? = null,
    trailing: String? = null,
    onClick: (() -> Unit)? = null,
    /** Greyed: what it heads is not reachable now (an offline manager's group). */
    muted: Boolean = false,
    /** Before the title: a group's tick while selecting (#2576). */
    leading: (@Composable () -> Unit)? = null,
    /** A group inside a section (an inbox section's source or day, #2576): closer to its rows, the title a level below the
     *  section's (caption, regular, in every style; as iOS and the web), and a caret for whether it is open (null: none).
     *  With [onClick], [open] also tells TalkBack whether the section is open and what a tap does (#4100), in every style. */
    sub: Boolean = false,
    open: Boolean? = null,
    /** The [count] in a capsule of this state's tone (the To-dos list's sections, #3712, as iOS `CountBadge`); none: quiet digits. */
    countTone: Status? = null,
    /** [trailing] is the fold's own action word ("Expand" / "Collapse"): while the header folds ([onClick] and [open]),
     *  TalkBack hears it from the state and click label, so the visible word is not read a second time (#4120). */
    trailingIsFoldAction: Boolean = false,
    /** Actions to move into an overflow menu only when the full title, count and ordinary actions do not fit. */
    overflowActions: List<JAction> = emptyList(),
    // Last, so a caller can pass it as a trailing lambda: `JSectionHeader(title) { … }`.
    action: (@Composable RowScope.() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val fold = if (onClick != null && open != null) foldLabels(open) else null
    val titleStyle = if (sub) PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal)
    else if (style == AppStyle.Glass) PersonaTheme.type.caption.copy(fontWeight = FontWeight.SemiBold, fontSize = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp))
    else PersonaTheme.type.headline.copy(fontSize = androidx.compose.ui.unit.TextUnit(15f, androidx.compose.ui.unit.TextUnitType.Sp))
    fun headerModifier(): Modifier = modifier.fillMaxWidth()
        .then(if (onClick != null) Modifier.combinedClickable(role = Role.Button, onClickLabel = fold?.action, onClick = onClick) else Modifier)
        .padding(top = if (sub) Tokens.Space.s else if (style == AppStyle.Board) 18.dp.dense else 26.dp.dense, bottom = if (sub) Tokens.Space.xs else 10.dp.dense,
            start = if (style == AppStyle.Brief) 0.dp else 4.dp.dense, end = if (style == AppStyle.Brief) 0.dp else 4.dp.dense)
        .heightIn(min = if (leading != null) 48.dp else 0.dp)
        .semantics { heading(); fold?.let { stateDescription = it.state } }
    @Composable
    fun leadingContent() {
        leading?.let { it(); Spacer(Modifier.width(Tokens.Space.xs)) }
        if (sub && open != null) {
            JIcon(PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3, modifier = Modifier.rotate(if (open) 90f else 0f))
            Spacer(Modifier.width(Tokens.Space.xs))
        }
        if (status != null && style == AppStyle.Board) {
            Box(Modifier.size(10.dp).clip(RoundedCornerShape(2.dp)).background(palette.status(status).dot))
            Spacer(Modifier.width(8.dp.dense))
        }
    }
    @Composable
    fun titleGroup(wrap: Boolean) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                title, style = titleStyle,
                color = if (muted) palette.ink3 else if (sub || style == AppStyle.Glass) palette.ink2 else palette.ink,
                maxLines = if (wrap) Int.MAX_VALUE else 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false),
            )
            count?.let {
                Spacer(Modifier.width(Tokens.Space.sm))
                if (countTone == null) Text("$it", style = PersonaTheme.type.micro, color = palette.ink3)
                else {
                    val tone = palette.status(countTone)
                    Text("$it", style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.SemiBold), color = tone.text, maxLines = 1,
                        modifier = Modifier.clip(CircleShape).background(tone.tint)
                            .padding(horizontal = SectionHeaderTokens.countPadding, vertical = SectionHeaderTokens.countPaddingVertical))
                }
            }
        }
    }
    @Composable
    fun ordinaryActions() {
        if (trailing != null || action != null) Spacer(Modifier.width(Tokens.Space.s))
        trailing?.let {
            Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1,
                modifier = if (trailingIsFoldAction && fold != null) Modifier.clearAndSetSemantics {} else Modifier)
        }
        if (action != null) Row { action.invoke(this) }
    }
    if (overflowActions.isEmpty()) {
        Row(
            headerModifier(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            leadingContent()
            // The title and its count take the free width, so the trailing text and the action sit at the end of the row.
            Row(Modifier.weight(1f)) { titleGroup(false) }
            ordinaryActions()
        }
        return
    }
    var menu by remember { mutableStateOf(false) }
    val textMeasurer = rememberTextMeasurer()
    val countStyle = PersonaTheme.type.micro
    val trailingStyle = PersonaTheme.type.caption
    val actionStyle = PersonaTheme.type.labelSmall
    SubcomposeLayout(
        headerModifier(),
    ) { constraints ->
        val leading = subcompose("leading") {
            Row(verticalAlignment = Alignment.CenterVertically) { leadingContent() }
        }.map { it.measure(Constraints()) }
        val leadingWidth = leading.maxOfOrNull { it.width } ?: 0
        val density = this
        fun textWidth(text: String, style: TextStyle): Int = textMeasurer.measure(
            AnnotatedString(text), style = style, maxLines = 1, softWrap = false,
        ).size.width
        fun Dp.toPxInt() = with(density) { this@toPxInt.toPx() }.roundToInt()
        val fullTitleWidth = textWidth(title, titleStyle) + (count?.let {
            Tokens.Space.sm.toPxInt() + textWidth("$it", countStyle) +
                if (countTone != null) SectionHeaderTokens.countPadding.toPxInt() * 2 else 0
        } ?: 0)
        val ordinaryWidth = (if (trailing != null || action != null) Tokens.Space.s.toPxInt() else 0) +
            (trailing?.let { textWidth(it, trailingStyle) } ?: 0) +
            if (action != null) maxOf(Tokens.MinTouch.android.toPxInt(),
                textWidth(overflowActions.last().label, actionStyle) + Tokens.Space.s.toPxInt() * 2) else 0
        val overflow = overflowActions.isNotEmpty() && leadingWidth + fullTitleWidth + ordinaryWidth > constraints.maxWidth
        val right = if (overflow) subcompose("overflow") {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Spacer(Modifier.width(Tokens.Space.s))
                JIconButton(PhIcons.DotsThree, stringResource(R.string.designsystem_more), { menu = true }, raised = false,
                    modifier = Modifier.testTag("inbox.section.overflow"))
                JMenu(menu, { menu = false }, buildList {
                    trailing?.let { add(JAction(it, enabled = onClick != null, onClick = { onClick?.invoke() })) }
                    addAll(overflowActions)
                })
            }
        }.map { it.measure(Constraints()) } else subcompose("ordinary-visible") {
            Row(verticalAlignment = Alignment.CenterVertically) { ordinaryActions() }
        }.map { it.measure(Constraints()) }
        val rightWidth = right.maxOfOrNull { it.width } ?: 0
        val bounded = constraints.maxWidth != Constraints.Infinity
        val titleWidth = if (bounded) (constraints.maxWidth - leadingWidth - rightWidth).coerceAtLeast(0) else fullTitleWidth
        val title = subcompose("title") { titleGroup(overflow) }.map { it.measure(Constraints(maxWidth = titleWidth)) }
        val height = maxOf(leading.maxOfOrNull { it.height } ?: 0, title.maxOfOrNull { it.height } ?: 0, right.maxOfOrNull { it.height } ?: 0)
        val width = if (bounded) constraints.maxWidth else leadingWidth + titleWidth + rightWidth
        layout(width, height) {
            leading.forEach { it.place(0, (height - it.height) / 2) }
            title.forEach { it.place(leadingWidth, (height - it.height) / 2) }
            right.forEach { it.place(width - rightWidth, (height - it.height) / 2) }
        }
    }
}

/** What TalkBack says for a section that folds: whether it is open ([state]) and what a tap does ([action]). */
@Immutable
private data class FoldLabels(val state: String, val action: String)

@Composable
private fun foldLabels(open: Boolean) = FoldLabels(
    state = stringResource(if (open) R.string.designsystem_expanded else R.string.designsystem_collapsed),
    action = stringResource(if (open) R.string.designsystem_collapse else R.string.designsystem_expand),
)

/**
 * A row of its own that folds a section open or shut on tap (a header drawn around a [JSectionHeader] with its own
 * caret): a button that tells TalkBack whether the section is [open] and what a tap does (#4100), as [JSectionHeader]
 * does with `onClick` and `open`. Draws nothing.
 */
@Composable
fun Modifier.foldToggle(open: Boolean, onToggle: () -> Unit): Modifier {
    val fold = foldLabels(open)
    return clickable(role = Role.Button, onClickLabel = fold.action, onClick = onToggle).semantics { stateDescription = fold.state }
}

/** A [JSectionHeader]'s count capsule ([JSectionHeader] `countTone`). */
object SectionHeaderTokens {
    val countPadding = 6.dp
    val countPaddingVertical = 1.dp
}

/**
 * One item of a group. List groups ([separate] false) read as one container (a glass panel, ruled rows, a
 * bordered table); separate items are cards of their own (glass cards, wash cards, board strips).
 * [holder] gives board strips their coloured holder; other styles ignore it. [grouped]: the group is a card of iOS's
 * grouped list on the black page (the computer's projects, #4338 A11): on Glass in dark it is the system's lighter gray
 * ([PersonaPalette.surfaceOpaque], as [JSettingsSheet]'s grouped surfaces), where the translucent glass fill would leave it
 * near-invisible beside its edge highlight. Light and the other styles draw as without it.
 */
@Composable
fun JGroupItem(
    position: GroupPosition,
    modifier: Modifier = Modifier,
    separate: Boolean = false,
    holder: Status? = null,
    grouped: Boolean = false,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    if (LocalHomeDrawer.current && style == AppStyle.Glass) {
        Column(modifier.fillMaxWidth(), content = content)
        return
    }
    val radius = if (separate) PersonaTheme.shapes.card else PersonaTheme.shapes.panel.coerceAtMost(26.dp).let { if (style == AppStyle.Board) 10.dp else it }
    val shape = if (separate) RoundedCornerShape(radius) else RoundedCornerShape(
        topStart = if (position.first) radius else 0.dp, topEnd = if (position.first) radius else 0.dp,
        bottomStart = if (position.last) radius else 0.dp, bottomEnd = if (position.last) radius else 0.dp,
    )
    val gap = if (separate && !position.last) (if (style == AppStyle.Board) 8.dp else 12.dp) else 0.dp
    Column(modifier.fillMaxWidth().padding(bottom = gap)) {
        when (style) {
            AppStyle.Glass -> Column(
                Modifier.fillMaxWidth().clip(shape).background(if (grouped && palette.dark) palette.surfaceOpaque else palette.surface)
                    .then(if (separate) Modifier.border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape) else Modifier)
                    .drawBehind {
                        if (!separate && position.first) drawLine(palette.strokeTop.copy(alpha = 0.6f), Offset(radius.toPx(), 0f), Offset(size.width - radius.toPx(), 0f), 1.dp.toPx())
                        if (!separate && !position.first) drawLine(palette.line, Offset(16.dp.toPx(), 0f), Offset(size.width - 16.dp.toPx(), 0f), 1.dp.toPx())
                    },
                content = content,
            )
            AppStyle.Brief -> Column(
                Modifier.fillMaxWidth()
                    .then(if (separate) Modifier.clip(shape).background(palette.surface) else Modifier)
                    .drawBehind { if (!separate && !position.first) drawLine(palette.line, Offset(0f, 0f), Offset(size.width, 0f), 1.dp.toPx()) },
                content = content,
            )
            AppStyle.Board -> if (separate) Column(
                Modifier.fillMaxWidth().clip(shape).background(palette.surface)
                    .then(if (holder != null) Modifier.holder(palette.status(holder).dot, Tokens.Space.xxs).padding(start = 6.dp.dense) else Modifier)
                    .border(1.dp, palette.line, shape),
                content = content,
            ) else Column(
                Modifier.fillMaxWidth().clip(shape).background(palette.surface)
                    .drawBehind {
                        val w = 1.dp.toPx()
                        drawLine(palette.line, Offset(0f, 0f), Offset(0f, size.height), w)
                        drawLine(palette.line, Offset(size.width - w, 0f), Offset(size.width - w, size.height), w)
                        if (position.first) drawLine(palette.line, Offset(0f, w / 2), Offset(size.width, w / 2), w)
                        else drawLine(palette.line, Offset(14.dp.toPx(), 0f), Offset(size.width, 0f), w)
                        if (position.last) drawLine(palette.line, Offset(0f, size.height - w / 2), Offset(size.width, size.height - w / 2), w)
                    },
                content = content,
            )
        }
    }
}

/** A small, non-lazy group (settings sections): items in one container. */
@Composable
fun JGroup(modifier: Modifier = Modifier, rows: List<@Composable () -> Unit>) {
    Column(modifier) {
        rows.forEachIndexed { index, row -> JGroupItem(GroupPosition.of(index, rows.size)) { row() } }
    }
}

/**
 * Side padding of a [JRow] in the active style: glass and board inset the row inside its panel, brief rows sit on
 * the paper's text column (none). Content that belongs to a row but is not one (a long value set under its label, a
 * note) pads with this to line up with the rows' text.
 */
val rowPadding: Dp @Composable get() = when (PersonaTheme.style) { AppStyle.Glass -> 16.dp; AppStyle.Brief -> 0.dp; AppStyle.Board -> 14.dp }

/** The space between a [JRow]'s leading slot and its title. */
private val RowLeadingGap = 14.dp

/**
 * Where a [JRow]'s title starts behind a leading icon of the default size. A line hung under such a row (a state and
 * its time) starts here, so it sits under the title rather than under the icon in every style.
 */
val rowTitleStart: Dp @Composable get() = rowPadding + JIconDefaultSize + RowLeadingGap

/** A settings-style row: optional icon, title and subtitle, a value or trailing control, and a chevron when it navigates. */
@Composable
fun JRow(
    title: String,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    leadingIcon: PhIcon? = null,
    leading: (@Composable () -> Unit)? = null,
    value: String? = null,
    trailing: (@Composable () -> Unit)? = null,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    chevron: Boolean = onClick != null && trailing == null,
    enabled: Boolean = true,
    destructive: Boolean = false,
    subtitleLines: Int = 2,
    selected: Boolean = false,
    /** Drawn right after the title on its line (a small source mark); the title gives way to it when space is short. */
    titleAccessory: (@Composable () -> Unit)? = null,
    /** Drawn again with a new [value], its numbers count to the new ones and other text fades in (#2811). */
    changingValue: Boolean = false,
    /** What accessibility services call the row's click: a switch row says [Role.Switch] so its on / off is read. */
    role: Role = Role.Button,
    /** [leadingIcon]'s colour in place of the quiet ink (an extension's named `icon_color`). */
    leadingIconTint: Color? = null,
    /** The start of the title in the quiet ink, drawn before [title]: a hub conversation title's day and kind ("1101 | Fix | "), so its topic is what the eye finds (iOS `ConversationTitle`). */
    titleLead: String? = null,
    /** A subtitle with part of it marked (a search match). Drawn instead of [subtitle] when set, on one line. */
    subtitleMark: AnnotatedString? = null,
) {
    val palette = PersonaTheme.palette
    RowFrame(modifier, onClick, onLongClick, null, enabled, selected, value, trailing, chevron, changingValue, role) {
        if (leading != null) { leading(); Spacer(Modifier.width(RowLeadingGap)) }
        else if (leadingIcon != null) {
            JIcon(leadingIcon, null, size = 22.dp, tint = if (destructive) palette.danger.dot else leadingIconTint ?: palette.ink2, decorative = true)
            Spacer(Modifier.width(16.dp.dense))
        }
        Column(Modifier.weight(1f)) {
            val titleText: @Composable (Modifier) -> Unit = { m ->
                val ink = if (destructive) palette.danger.text else palette.ink
                if (titleLead == null) Text(title, m, style = PersonaTheme.type.body, color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                else Text(buildAnnotatedString { withStyle(SpanStyle(color = palette.ink3)) { append(titleLead) }; append(title) }, m,
                    style = PersonaTheme.type.body, color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (titleAccessory == null) titleText(Modifier)
            else Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                titleText(Modifier.weight(1f, fill = false))
                titleAccessory()
            }
            val caption = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal)
            when {
                subtitleMark != null -> Text(subtitleMark, style = caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                subtitle != null -> Text(subtitle, style = caption, color = palette.ink2, maxLines = subtitleLines, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/**
 * A [JRow] whose middle is [content] instead of a title and subtitle: lines that wrap, tags, a status mark in front.
 * Everything else is the row's own (the style's side padding, height, click and long press, selection, the dimmed
 * disabled state, [trailing] and the chevron), so it lines up with the rows around it in every style. [content] gets
 * the width left of [trailing], centred vertically like a title; give a mark `Modifier.align(Alignment.Top)` to keep
 * it on the first of several lines. [onClickLabel] names the click for accessibility ("More actions").
 */
@Composable
fun JRow(
    modifier: Modifier = Modifier,
    trailing: (@Composable () -> Unit)? = null,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    onClickLabel: String? = null,
    chevron: Boolean = onClick != null && trailing == null,
    enabled: Boolean = true,
    selected: Boolean = false,
    /** What accessibility services call the row's click; a row that toggles a [JSwitch] passes [Role.Switch]. */
    role: Role = Role.Button,
    content: @Composable RowScope.() -> Unit,
) = RowFrame(modifier, onClick, onLongClick, onClickLabel, enabled, selected, value = null, trailing, chevron, role = role) {
    Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, content = content)
}

/** The parts every [JRow] shares; [content] is laid out first, then the value, the trailing control and the chevron. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RowFrame(
    modifier: Modifier,
    onClick: (() -> Unit)?,
    onLongClick: (() -> Unit)?,
    onClickLabel: String?,
    enabled: Boolean,
    selected: Boolean,
    value: String?,
    trailing: (@Composable () -> Unit)?,
    chevron: Boolean,
    changingValue: Boolean = false,
    role: Role = Role.Button,
    content: @Composable RowScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val drawer = LocalHomeDrawer.current && style == AppStyle.Glass
    Row(
        modifier.fillMaxWidth().heightIn(min = if (drawer) HomeTokens.drawerRowHeight else 56.dp)
            .then(if (drawer) Modifier.clip(RoundedCornerShape(Tokens.Radius.control)).background(if (selected) palette.sunken else Color.Transparent) else Modifier.selectedRow(selected))
            .onSecondaryClick(onLongClick)
            .then(if (onClick != null || onLongClick != null) Modifier.combinedClickable(enabled = enabled, role = role, onClickLabel = onClickLabel, onClick = onClick ?: {}, onLongClick = onLongClick) else Modifier)
            .stableAlpha(if (enabled) 1f else 0.45f).padding(horizontal = if (drawer) Tokens.Space.m else rowPadding, vertical = if (drawer) Tokens.Space.xs else 10.dp.dense),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        content()
        value?.let {
            Spacer(Modifier.width(10.dp.dense))
            val valueStyle = if (style == AppStyle.Board) PersonaTheme.type.micro.copy(fontSize = androidx.compose.ui.unit.TextUnit(14f, androidx.compose.ui.unit.TextUnitType.Sp)) else PersonaTheme.type.callout
            if (changingValue) JChangingText(it, style = valueStyle, color = palette.ink3, modifier = Modifier.widthIn(max = 180.dp))
            else Text(it, style = valueStyle, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 180.dp))
        }
        trailing?.let { Spacer(Modifier.width(10.dp.dense)); it() }
        if (chevron) { Spacer(Modifier.width(6.dp.dense)); JIcon(PhIcons.CaretRight, null, size = 16.dp, tint = palette.ink3) }
    }
}

@Composable
fun JSwitchRow(
    title: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    modifier: Modifier = Modifier,
    subtitle: String? = null,
    leadingIcon: PhIcon? = null,
    enabled: Boolean = true,
    /** Lines the subtitle may take before it ends in an ellipsis; a line that must be read in full passes [Int.MAX_VALUE]. */
    subtitleLines: Int = 2,
) = JRow(title, modifier, subtitle, leadingIcon, value = null, onClick = { onCheckedChange(!checked) }, chevron = false, enabled = enabled,
    subtitleLines = subtitleLines, trailing = { JSwitch(checked, null, enabled = enabled) }, role = Role.Switch)

/**
 * A card: a frosted panel, a wash card, or a bordered strip. [status] tints the glass edge and gives board
 * strips a holder.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun JCard(
    modifier: Modifier = Modifier,
    onClick: (() -> Unit)? = null,
    onLongClick: (() -> Unit)? = null,
    status: Status? = null,
    padding: Dp = if (PersonaTheme.style == AppStyle.Board) 14.dp else 20.dp,
    content: @Composable ColumnScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = PersonaTheme.shapes.cardShape
    val click = if (onClick != null || onLongClick != null) Modifier.combinedClickable(role = Role.Button, onClick = onClick ?: {}, onLongClick = onLongClick) else Modifier
    when (style) {
        AppStyle.Glass -> Column(
            modifier.fillMaxWidth().clip(shape).background(palette.surface)
                .border(1.dp, Brush.verticalGradient(listOf(status?.let { palette.status(it).dot.copy(alpha = 0.8f) } ?: palette.strokeTop, palette.strokeBottom)), shape)
                .then(click).padding(padding),
            content = content,
        )
        AppStyle.Brief -> Column(modifier.fillMaxWidth().clip(shape).background(palette.surface).then(click).padding(padding), content = content)
        AppStyle.Board -> Column(
            modifier.fillMaxWidth().clip(shape).background(palette.surface)
                .then(if (status != null) Modifier.holder(palette.status(status).dot, Tokens.Space.xxs) else Modifier)
                .border(1.dp, palette.line, shape).then(click)
                .padding(start = if (status != null) 6.dp.dense + padding else padding, end = padding, top = padding, bottom = padding),
            content = content,
        )
    }
}

/**
 * A notice inside a page (connection lost, permission needed). [status] picks the tone. [inlineAction] puts the action
 * at the end of the text's line as a text button (a one-line notice over a list: "1 more waiting on you · Show all",
 * #3740) instead of a button under the text.
 */
@Composable
fun JNotice(
    text: String,
    modifier: Modifier = Modifier,
    title: String? = null,
    status: Status = Status.Unknown,
    icon: PhIcon = PhIcons.Info,
    actionLabel: String? = null,
    onAction: (() -> Unit)? = null,
    inlineAction: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val tone = palette.status(status)
    val action = if (actionLabel != null && onAction != null) actionLabel to onAction else null
    JCard(modifier, status = status, padding = if (inlineAction) NoticeTokens.inlinePadding else 16.dp) {
        Row(verticalAlignment = if (inlineAction) Alignment.CenterVertically else Alignment.Top) {
            JIcon(icon, null, size = 20.dp, tint = if (PersonaTheme.style == AppStyle.Brief) palette.ink else tone.dot,
                modifier = if (inlineAction) Modifier else Modifier.padding(top = 2.dp.dense))
            Spacer(Modifier.width(12.dp.dense))
            Column(Modifier.weight(1f)) {
                title?.let { Text(it, style = PersonaTheme.type.headline, color = palette.ink) }
                Text(text, style = PersonaTheme.type.callout, color = palette.ink2)
                if (action != null && !inlineAction) {
                    Spacer(Modifier.height(10.dp.dense))
                    JButton(action.first, action.second, kind = JButtonKind.Secondary, size = JButtonSize.Medium)
                }
            }
            if (action != null && inlineAction) JTextButton(action.first, action.second)
        }
    }
}

/** A [JNotice] whose action sits on its line ([JNotice] `inlineAction`): less padding above and below, the text button's own height filling it. */
object NoticeTokens {
    val inlinePadding = 12.dp
}

@Composable
fun JEmptyState(
    title: String,
    description: String,
    modifier: Modifier = Modifier,
    icon: PhIcon? = null,
    action: (@Composable () -> Unit)? = null,
    /** A named colour for the icon and its well (an extension's `icon_color`); none: the quiet ink. */
    iconHue: JHue? = null,
    /** Drawn where the icon goes, in its place (an assistant's monogram, [JAssistantOffline]). */
    leading: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    Column(modifier.fillMaxWidth().padding(horizontal = 32.dp.dense, vertical = 40.dp.dense), horizontalAlignment = Alignment.CenterHorizontally) {
        if (leading != null) { leading(); Spacer(Modifier.height(16.dp.dense)) }
        else icon?.let {
            val hue = iconHue?.let { h -> palette.hue(h) }
            Box(
                Modifier.size(64.dp).clip(if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(12.dp.corner) else CircleShape).background(hue?.tint ?: palette.surfaceStrong),
                contentAlignment = Alignment.Center,
            ) { JIcon(it, null, size = 28.dp, tint = hue?.dot ?: palette.ink2, decorative = true) }
            Spacer(Modifier.height(16.dp.dense))
        }
        Text(title, style = PersonaTheme.type.title, color = palette.ink, textAlign = TextAlign.Center)
        Spacer(Modifier.height(6.dp.dense))
        Text(description, style = PersonaTheme.type.callout, color = palette.ink2, textAlign = TextAlign.Center)
        action?.let { Spacer(Modifier.height(18.dp.dense)); it() }
    }
}

@Composable
fun JLoading(modifier: Modifier = Modifier, text: String? = null) {
    Column(modifier.fillMaxWidth().padding(32.dp.dense), horizontalAlignment = Alignment.CenterHorizontally) {
        JSpinner(Modifier.size(28.dp))
        text?.let { Spacer(Modifier.height(12.dp.dense)); Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
    }
}

@Composable
fun JDivider(modifier: Modifier = Modifier, inset: Dp = 0.dp) {
    Box(modifier.padding(start = inset).fillMaxWidth().height(1.dp).background(PersonaTheme.palette.line))
}

/**
 * A person, group or source. [badge] is a small corner mark (Lark / mail). [image] is their picture, filling the same
 * shape; without one (none, still loading, failed) the initial or [icon] stands in.
 */
@Composable
fun JAvatar(
    text: String,
    modifier: Modifier = Modifier,
    size: Dp = 40.dp,
    icon: PhIcon? = null,
    badge: PhIcon? = null,
    badgeColor: Color? = null,
    image: Painter? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = if (style == AppStyle.Board) RoundedCornerShape(size * 0.22f) else CircleShape
    Box(modifier.size(size)) {
        Box(
            Modifier.fillMaxSize().clip(shape).background(palette.surfaceStrong)
                .then(if (style == AppStyle.Glass) Modifier.border(1.dp, Brush.verticalGradient(listOf(palette.strokeTop, palette.strokeBottom)), shape) else Modifier),
            contentAlignment = Alignment.Center,
        ) {
            if (image != null) Image(image, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
            else if (icon != null) JIcon(icon, null, size = size * 0.5f, tint = palette.ink2)
            else Text(text.trim().take(1), style = PersonaTheme.type.label.copy(fontSize = androidx.compose.ui.unit.TextUnit(size.value * 0.4f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.ink2)
        }
        badge?.let {
            val color = badgeColor ?: if (style == AppStyle.Brief) palette.ink else palette.accent
            Box(
                Modifier.align(Alignment.BottomEnd).offset(3.dp, 3.dp).size(size * 0.45f).clip(if (style == AppStyle.Board) RoundedCornerShape(4.dp) else CircleShape)
                    .background(palette.background).padding(2.dp.dense).clip(if (style == AppStyle.Board) RoundedCornerShape(3.dp) else CircleShape).background(color),
                contentAlignment = Alignment.Center,
            ) { JIcon(it, null, size = size * 0.26f, tint = if (style == AppStyle.Brief) palette.background else Color.White, selected = true) }
        }
    }
}

/**
 * True inside a container that selects its whole content as one: the words of a message "Select" has selected in place
 * (`MessageSelection.kt` in feature/chat). A block that would open a [SelectionContainer] of its own, a code block
 * ([SelectableOnItsOwn]), joins that one, so a single selection runs across the message; and what a tap on text does
 * (copying inline code) stands down.
 */
val LocalSelectionAbove = staticCompositionLocalOf { false }

/** [content] selectable on its own, in a [SelectionContainer] of its own, unless a container above it covers it ([LocalSelectionAbove]). */
@Composable
fun SelectableOnItsOwn(content: @Composable () -> Unit) {
    if (LocalSelectionAbove.current) content() else SelectionContainer { content() }
}

/**
 * Text the reader can long-press to select and copy (a link, an address shown to hand on), with no surface of its own;
 * [style] and [color] come from the theme like any [Text].
 */
@Composable
fun JSelectableText(
    text: String,
    style: androidx.compose.ui.text.TextStyle,
    color: Color,
    modifier: Modifier = Modifier,
    maxLines: Int = Int.MAX_VALUE,
) {
    SelectionContainer(modifier) {
        Text(text, style = style, color = color, maxLines = maxLines, overflow = TextOverflow.Ellipsis)
    }
}

/** Code and command text in the mono face on a sunken surface; selectable. */
@Composable
fun JCodeBlock(code: String, modifier: Modifier = Modifier, maxLines: Int = Int.MAX_VALUE, small: Boolean = false) =
    JCodeBlock(androidx.compose.ui.text.AnnotatedString(code), modifier, maxLines, small)

/** Rich code text (links, search highlights); [onTextLayout] lets readers locate matches. [header]: a title bar drawn
 *  inside the block, above the code (a reply's code block: its language and Copy, #2984). */
@Composable
fun JCodeBlock(
    code: androidx.compose.ui.text.AnnotatedString,
    modifier: Modifier = Modifier,
    maxLines: Int = Int.MAX_VALUE,
    small: Boolean = false,
    onTextLayout: (androidx.compose.ui.text.TextLayoutResult) -> Unit = {},
    header: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) 6.dp.corner else 14.dp.corner)
    val box = Modifier.fillMaxWidth().clip(shape).background(if (PersonaTheme.style == AppStyle.Brief) palette.background else palette.sunken)
        .then(if (PersonaTheme.style == AppStyle.Brief) Modifier.border(1.dp, palette.line, shape) else Modifier)
    val inset = 14.dp.dense
    val text: @Composable (Modifier) -> Unit = { m ->
        SelectableOnItsOwn {
            Text(
                code, style = if (small) PersonaTheme.type.codeSmall else PersonaTheme.type.code, color = palette.ink, maxLines = maxLines, overflow = TextOverflow.Ellipsis,
                onTextLayout = onTextLayout, modifier = m.padding(inset),
            )
        }
    }
    if (header == null) text(modifier.then(box))
    else Column(modifier.then(box)) {
        // the title bar is not code: a selection of the message leaves its language and Copy out
        DisableSelection { header() }
        text(Modifier.fillMaxWidth())
    }
}
