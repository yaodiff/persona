package persona.core.designsystem

import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.layout.Placeable
import androidx.compose.ui.layout.layoutId
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.semantics.text
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import persona.core.contracts.CoreText
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentRowLeading
import persona.core.domain.sort.AgentRowStyle
import persona.core.domain.sort.rowParts
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

// The Agents list (docs/android/android-design.md 2.4): computer chips over a tree of computer → space → session. The tree is told
// apart by indentation, type and hairlines, not by plates: the page background stays whole and only business cards
// (requests, notices; the board's agent strips) sit on it. Lazy lists give every heading and row its own item.

/** A small mark on a row or a space: pinned, muted, a draft, messages waiting to send; [ai] in the AI color (ultracode on, #582). */
@Immutable
data class RowFlag(val label: String, val icon: PhIcon, val caution: Boolean = false, val ai: Boolean = false)

/** A short fact under a session row: where it runs, a loop, background work, a draft, messages waiting to go out. */
@Immutable
data class RowFact(val text: String, val caution: Boolean = false)

/**
 * An Agents row's third line on Glass (iOS `SlotRow.placeLine` in the Avatar style, #4338 A9), after the row's source tag:
 * where it runs ([words]: the tab, space › tab, the computer, joined with " · "), the [model] it runs on, its [todo]'s
 * title and, when [muted], the bell. The words come from core/domain's `rowContent`.
 */
@Immutable
data class RowPlace(val words: String = "", val model: String? = null, val todo: String? = null, val muted: Boolean = false)

/**
 * Background Workflow fields under a session row, after an AI-coloured [icon]. The row gives its critical facts their
 * space first, then ellipsizes these details in the remaining part of the same metadata line.
 */
@Immutable
data class RowNote(val fields: List<CoreText>, val icon: PhIcon)

// ------------------------------------------------------------------ measures

private val Step = 22.dp
private val Caret = 14.dp
private val Hairline = 1.dp
private val Action = 40.dp
private val Dot = 7.dp
private val Holder = 26.dp
private val None = 0.dp

/** Glass's leading well (iOS `Theme.Size.wellL`), the agent's mark in it (`markL`) and the rim around its corner (`rule`). */
private val AvatarWell = Tokens.Space.Standard.xxl + Tokens.Space.Standard.xs
private val AvatarMark = Tokens.Space.Standard.l + Tokens.Space.Standard.xxs / 2
private val AvatarRim = Tokens.Space.Standard.xxs
/** Between the well and the row's words (iOS's Avatar row: `Theme.Space.m`). */
private val AvatarGap get() = Tokens.Space.m
/** Between Two Lines' dot and its words (iOS style B: `Theme.Space.s`). */
private val TwoLineGap get() = Tokens.Space.s
/** The background-waiting clock against the dot it stands in for (as on the space page's cards). */
internal const val WaitClockScale = 1.7f
/** A State Card that needs you, tinted in its blue (iOS `StatusPalette.needsYouWash`: the system blue at this opacity). */
private const val CardWashAlpha = 0.1f
/** iOS's outlined chip: secondary words in a hairline of the secondary color at this opacity. */
private const val OutlineAlpha = 0.4f

/** Width of [JStatusMark] in the active style, so a row's mark can sit centred under its space's caret. */
private val markSize: Dp @Composable get() = when (PersonaTheme.style) { AppStyle.Glass -> 28.dp; AppStyle.Brief -> 36.dp; AppStyle.Board -> 22.dp }

// ------------------------------------------------------------------ computer chips

/**
 * A computer in the strip above the Agents tree, as a filter chip of the active style: [icon] is the device (server,
 * desktop, laptop) and its corner dot says whether the computer is [online]. "All machines" passes neither. [count] is how
 * many sessions the list shows while the chip is selected.
 */
@Composable
fun MachineChip(
    name: String,
    count: Int,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    icon: PhIcon? = null,
    online: Boolean? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = RoundedCornerShape(PersonaTheme.shapes.chip)
    val fill = when {
        selected && style == AppStyle.Board -> palette.statusSurface
        selected -> palette.action
        style == AppStyle.Glass -> palette.surfaceStrong
        else -> palette.surface
    }
    val ink = if (selected && style != AppStyle.Board) palette.onAction else palette.ink
    val border = when (style) {
        AppStyle.Glass -> palette.strokeBottom.takeIf { !selected }
        AppStyle.Brief -> null
        AppStyle.Board -> if (selected) palette.accent else palette.line
    }
    val spoken = listOfNotNull(online?.let { stringResource(if (it) R.string.designsystem_online else R.string.designsystem_offline) },
        pluralStringResource(R.plurals.designsystem_sessions, count, count)).joinToString(stringResource(R.string.designsystem_separator))
    Row(
        modifier.heightIn(min = Action).clip(shape).background(fill).then(if (border != null) Modifier.border(Hairline, border, shape) else Modifier)
            .clickable(role = Role.Tab, onClick = onClick)
            .clearAndSetSemantics { contentDescription = name; stateDescription = spoken; this.selected = selected }
            .padding(horizontal = Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (icon != null) {
            Box {
                JIcon(icon, null, size = Tokens.Space.l, tint = if (online == false && !selected) palette.ink3 else ink)
                if (online != null) Box(
                    Modifier.align(Alignment.BottomEnd).offset(Tokens.Space.xxs, Tokens.Space.xxs).size(Dot).clip(CircleShape).background(fill)
                        .padding(Hairline).clip(CircleShape).background(if (online) palette.positive else palette.offline.dot),
                )
            }
            Spacer(Modifier.width(Tokens.Space.s))
        }
        Text(name, style = PersonaTheme.type.labelSmall.copy(fontWeight = FontWeight.Medium), color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Spacer(Modifier.width(Tokens.Space.xs + Hairline))
        Text("$count", style = PersonaTheme.type.micro, color = if (selected && style != AppStyle.Board) ink else palette.ink3, maxLines = 1)
    }
}

// ------------------------------------------------------------------ headings

/** The rule above a computer (or a top-level section): a hairline, a heavier ink rule on the board. */
@Composable
private fun SectionRule() {
    val palette = PersonaTheme.palette
    if (PersonaTheme.style == AppStyle.Board) Box(Modifier.fillMaxWidth().height(Tokens.Space.xxs).background(palette.ink))
    else Box(Modifier.fillMaxWidth().height(Hairline).background(palette.line))
}

/**
 * Status counts after a heading: a dot and a number for each state that has any; [waiting] (the agents that only wait in
 * the background, #6045) as a clock in that state's lighter tone and a number, before an idle count, as iOS's
 * `StateCounts`. Each is spoken as one. [lighter]: the counted agents only wait in the background, so their dots take that tone.
 */
@Composable
internal fun StateCounts(counts: List<Pair<Status, Int>>, waiting: Int = 0, lighter: Boolean = false) {
    val palette = PersonaTheme.palette
    @Composable fun count(n: Int, spoken: String, mark: @Composable () -> Unit) {
        Spacer(Modifier.width(Tokens.Space.s + Tokens.Space.xxs))
        Row(Modifier.clearAndSetSemantics { contentDescription = spoken }, verticalAlignment = Alignment.CenterVertically) {
            mark()
            Spacer(Modifier.width(Tokens.Space.xs))
            Text("$n", style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1)
        }
    }
    val waitingWords = stringResource(R.string.designsystem_count_waiting_background, waiting)
    var clockDrawn = false
    counts.filter { it.second > 0 }.forEach { (status, n) ->
        if (status == Status.Idle && waiting > 0 && !clockDrawn) {
            clockDrawn = true
            count(waiting, waitingWords) { JIcon(persona.core.designsystem.icons.PersonaIcons.backgroundWaiting, null, size = Caret, tint = palette.backgroundWaiting.dot, decorative = true) }
        }
        // Idle is spoken as iOS says it ("3 idle"), not by the state's own name (Seen).
        count(n, if (status == Status.Idle) pluralStringResource(R.plurals.designsystem_idle_count, n, n) else "$n ${stringResource(status.labelRes)}") {
            JStatusDot(status, size = Dot, waiting = lighter, breathes = false) // still, as iOS's `StateCounts`
        }
    }
    if (waiting > 0 && !clockDrawn) count(waiting, waitingWords) {
        JIcon(persona.core.designsystem.icons.PersonaIcons.backgroundWaiting, null, size = Caret, tint = palette.backgroundWaiting.dot, decorative = true)
    }
}

/** A compact icon button for list headings (a computer's gauge, a space's ⋯); bars keep the larger [JIconButton]. */
@Composable
fun HeadingAction(icon: PhIcon, label: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    Box(
        modifier.size(Action).clip(CircleShape).clickable(role = Role.Button, onClick = onClick).semantics { contentDescription = label },
        contentAlignment = Alignment.Center,
    ) { JIcon(icon, null, size = Tokens.Size.iconXS, tint = PersonaTheme.palette.ink2) }
}

/**
 * The first level of the Agents tree: a computer, or a top-level section ("Pinned", a state, a history project). The
 * largest type of the tree with a rule above it ([first] has none); no plate. [icon] is the device or the section's
 * mark, [online] adds the connection dot ("Offline" is also written), [detail] is the line under the name, [counts] the
 * sessions that need the user / work / are unread, [onMonitor] the gauge button to the computer's page. [tag] names
 * the hub group the computer belongs to, only when the phone has more than one (docs/android/android.md "Multiple hubs").
 */
@Composable
fun AgentTreeHeader(
    title: String,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    first: Boolean = false,
    icon: PhIcon? = null,
    online: Boolean? = null,
    detail: String? = null,
    counts: List<Pair<Status, Int>> = emptyList(),
    /** The agents that only wait in the background, drawn apart from [counts] ([StateCounts]). */
    waiting: Int = 0,
    /**
     * A section of agents that only wait in the background (By Status, #4338 A5): on Glass its [icon] and its count's dot
     * take that state's lighter tone ([PersonaPalette.backgroundWaiting]), as iOS's `SectionHeader` takes its section's.
     */
    waitsInBackground: Boolean = false,
    onMonitor: (() -> Unit)? = null,
    tag: String? = null,
    onAction: (() -> Unit)? = null,
    actionIcon: PhIcon? = null,
    actionLabel: String? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val lighter = waitsInBackground && PersonaTheme.style == AppStyle.Glass
    Column(modifier.fillMaxWidth().padding(top = if (first) Tokens.Space.xs else Tokens.Space.xl)) {
        if (!first) SectionRule()
        Row(
            Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onToggle).semantics { heading() }
                .padding(top = Tokens.Space.m, bottom = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            JIcon(if (folded) PhIcons.CaretRight else PhIcons.CaretDown, stringResource(if (folded) R.string.designsystem_group_expand else R.string.designsystem_group_collapse), size = Tokens.Space.l, tint = palette.ink3)
            Spacer(Modifier.width(Tokens.Space.s - Tokens.Space.xxs))
            icon?.let { JIcon(it, null, size = Tokens.Size.iconXS, tint = if (lighter) palette.backgroundWaiting.dot else palette.ink); Spacer(Modifier.width(Tokens.Space.s)) }
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(title, style = type.title.copy(fontWeight = FontWeight.Bold), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f, fill = false))
                    tag?.let { Spacer(Modifier.width(Tokens.Space.s)); JTag(it) }
                    online?.let {
                        Spacer(Modifier.width(Tokens.Space.s))
                        val onlineText = stringResource(if (it) R.string.designsystem_online else R.string.designsystem_offline)
                        Box(Modifier.size(Dot).clip(CircleShape).background(if (it) palette.positive else palette.offline.dot)
                            .semantics { contentDescription = onlineText })
                        if (!it) { Spacer(Modifier.width(Tokens.Space.xs)); Text(onlineText, style = type.micro, color = palette.ink3, maxLines = 1) }
                    }
                }
                detail?.let { Text(it, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            }
            StateCounts(counts, waiting, lighter)
            if (onMonitor != null) { Spacer(Modifier.width(Tokens.Space.xs)); HeadingAction(PhIcons.Gauge, stringResource(R.string.designsystem_machine_monitor), onMonitor) }
            if (onAction != null) { Spacer(Modifier.width(Tokens.Space.xs)); HeadingAction(actionIcon ?: PhIcons.CaretRight, actionLabel ?: stringResource(R.string.designsystem_details), onAction) }
        }
    }
}

/**
 * The second level: a herdr workspace under its computer, indented [depth] steps (0 when the list shows one
 * computer and drops its heading) and smaller than the computer. A [quiet] space (all finished and seen) is lighter
 * and says how many [sessions] it holds instead of [counts]. [counts] take a clock for the agents that only [waiting] in the
 * background, as iOS's `StateCounts` does. [menu] is the space's ⋯ (a [HeadingAction] and its sheet).
 * Under "By repository" the same heading is a repository or one of its working trees, told apart by [icon] (folder for a
 * repository, ⌂ for the main checkout, no icon for another tree) and one more [depth] step for a tree. [kind] names the
 * level for TalkBack (space, repository, main worktree, worktree): the caret says "Expand" / "Collapse" it, the icon
 * (when present) says what it is, the heading whether it is open.
 */
@Composable
fun AgentSpaceHeader(
    title: String,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    depth: Int = 1,
    first: Boolean = false,
    quiet: Boolean = false,
    sessions: Int = 0,
    counts: List<Pair<Status, Int>> = emptyList(),
    /** The agents that only wait in the background, drawn apart from [counts] with a clock ([StateCounts]). */
    waiting: Int = 0,
    flags: List<RowFlag> = emptyList(),
    menu: (@Composable () -> Unit)? = null,
    icon: PhIcon? = null,
    kind: String = stringResource(R.string.designsystem_space),
) {
    val palette = PersonaTheme.palette
    val foldState = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    val type = PersonaTheme.type
    Row(
        modifier.fillMaxWidth().padding(top = if (first) Tokens.Space.xxs else Tokens.Space.s).heightIn(min = Action)
            .clickable(role = Role.Button, onClick = onToggle)
            .semantics { heading(); stateDescription = foldState }.padding(start = Step * depth),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        JIcon(if (folded) PhIcons.CaretRight else PhIcons.CaretDown, stringResource(if (folded) R.string.designsystem_expand_named else R.string.designsystem_collapse_named, kind), size = Caret, tint = palette.ink3)
        Spacer(Modifier.width(Tokens.Space.s - Tokens.Space.xxs))
        icon?.let { JIcon(it, kind, size = Caret, tint = palette.ink2); Spacer(Modifier.width(Tokens.Space.xs)) }
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
            Text(
                title, style = if (quiet) type.callout else type.label, color = if (quiet) palette.ink2 else palette.ink,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false),
            )
            flags.forEach { Spacer(Modifier.width(Tokens.Space.xs)); JIcon(it.icon, it.label, size = Caret, tint = palette.ink3) }
        }
        if (quiet) Text(pluralStringResource(R.plurals.designsystem_sessions, sessions, sessions), style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3, maxLines = 1)
        else StateCounts(counts, waiting)
        menu?.invoke()
    }
}

/**
 * "N more idle": opens the finished sessions an active space keeps tucked away. [depth] is the space's. Once they are
 * [open] (Glass's By project, iOS `collapseRow`, #4338 A8) the row follows them as "Hide N Idle" in the accent and folds them
 * again. Closed, on Glass, it names the first of them: [tabs] (their tab names, at most six) as chips in a row of their own
 * that does not take touches and fades out toward the end of the line (iOS `IdleFoldRow`), in [compact] lists as one line of
 * words after the count (iOS `CompactFoldRow`). Brief and Board keep the count alone.
 */
@Composable
fun AgentIdleRow(
    hidden: Int, onClick: () -> Unit, modifier: Modifier = Modifier, depth: Int = 1, open: Boolean = false,
    tabs: List<String> = emptyList(), compact: Boolean = false,
) {
    if (open) {
        AgentFoldRow(persona.core.designsystem.icons.PersonaIcons.collapse, pluralStringResource(R.plurals.designsystem_idle_hide, hidden, hidden), onClick, modifier, depth)
        return
    }
    val names = if (PersonaTheme.style == AppStyle.Glass) tabs.take(IdleTabNames) else emptyList()
    val start = Step * depth + (Caret - markSize) / 2
    Row(
        modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick)
            .padding(start = start.coerceAtLeast(None) + markSize + Tokens.Space.s - Tokens.Space.xxs, top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(pluralStringResource(R.plurals.designsystem_idle_more, hidden, hidden), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
        Spacer(Modifier.width(Tokens.Space.xs))
        JIcon(PhIcons.CaretDown, null, size = Caret, tint = PersonaTheme.palette.ink3)
        if (names.isNotEmpty()) {
            Spacer(Modifier.width(Tokens.Space.s))
            if (compact) Text(names.joinToString(" · "), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1,
                overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            else Row(Modifier.weight(1f).fadeOutAtEnd().horizontalScroll(rememberScrollState(), enabled = false), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                names.forEach { IdleTabChip(it) }
            }
        }
    }
}

/** How many of the hidden agents "N more idle" names (iOS `IdleFoldRow`: `sessions.prefix(6)`). */
private const val IdleTabNames = 6

/** iOS's neutral `Chip`: a tab's name in a capsule of the chip fill, in the secondary words. */
@Composable
private fun IdleTabChip(text: String) {
    val palette = PersonaTheme.palette
    Text(text, Modifier.clip(CircleShape).background(if (palette.dark) Tokens.Common.Dark.Fill.chip else Tokens.Common.Light.Fill.chip)
        .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xxs),
        style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Medium), color = palette.ink2, maxLines = 1, softWrap = false)
}

/**
 * A line's last half fading out (iOS `.mask(LinearGradient(.black, .black, .clear))`): the first half whole, the rest to nothing
 * at the end. Only the gradient's alpha counts (the mask keeps what it covers), so any palette ink will do.
 */
@Composable
private fun Modifier.fadeOutAtEnd(): Modifier {
    val solid = PersonaTheme.palette.ink
    return graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }.drawWithContent {
        drawContent()
        val rtl = layoutDirection == LayoutDirection.Rtl
        drawRect(Brush.horizontalGradient(0f to solid, 0.5f to solid, 1f to solid.copy(alpha = 0f),
            startX = if (rtl) size.width else 0f, endX = if (rtl) 0f else size.width), blendMode = BlendMode.DstIn)
    }
}

/**
 * A long run of a section's rows (iOS's "Show All N"): [count] of them, the first ones above; once [expanded], "Collapse"
 * at their end. In the accent, as a button; [depth] as the rows'.
 */
@Composable
fun AgentShowAllRow(count: Int, expanded: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier, depth: Int = 1) {
    AgentFoldRow(
        if (expanded) persona.core.designsystem.icons.PersonaIcons.collapse else persona.core.designsystem.icons.PersonaIcons.expand,
        if (expanded) stringResource(R.string.designsystem_show_fewer) else stringResource(R.string.designsystem_show_all, count),
        onClick, modifier, depth,
    )
}

/** A line that opens or folds a run of rows, in the accent as a button: its [icon] in the rows' mark column, then [words]; [depth] as the rows'. */
@Composable
private fun AgentFoldRow(icon: PhIcon, words: String, onClick: () -> Unit, modifier: Modifier, depth: Int) {
    val start = Step * depth + (Caret - markSize) / 2
    val accent = PersonaTheme.palette.accent
    Row(
        modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick)
            .padding(start = start.coerceAtLeast(None), top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Box(Modifier.width(markSize), contentAlignment = Alignment.Center) {
            JIcon(icon, null, size = Caret, tint = accent, decorative = true)
        }
        Text(words, style = PersonaTheme.type.caption, color = accent, maxLines = 1)
    }
}

// ------------------------------------------------------------------ rows

/**
 * A session: its mark, title and time on the first line, the state and latest message on the second, and one metadata
 * line for [note] and [facts]. Board puts metadata on the second line instead, so its strip never exceeds two lines.
 * [compact] keeps the first line alone. [depth] is that of the heading it sits under, so the
 * mark lines up with the heading's caret. Glass and brief draw a plain row with a hairline above it unless it is
 * first in its group ([position]); the board keeps its agent strip, compacted, because each strip is the agent itself.
 * Long-pressing (or a secondary click) opens the row's actions ([onLongClick]). [muted] fades the row with
 * [stableAlpha], like every dimmed control: a toggled `Modifier.alpha` after clip + background can lose them.
 * Glass draws iOS's Avatar row (#4338 A9): with an [agentMark], the mark in a round well with the state on its corner
 * ([AgentAvatar]); with a [place], a third line that starts with the [source] tag ([PlaceLine]). Compact rows, Brief
 * and Board keep the mark beside the state mark and the tag beside the latest words. Glass draws the list's other row
 * styles ([style], iOS `AgentRowStyle`) as their parts say (`rowParts` in core/domain): Two Lines ([TwoLineRow]) and
 * State Cards ([StateCardRow]); Brief and Board ignore it.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun AgentRow(
    title: String,
    status: Status,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    onLongClick: (() -> Unit)? = null,
    depth: Int = 0,
    position: GroupPosition = GroupPosition.Single,
    preview: String? = null,
    time: String? = null,
    agent: String? = null,
    statusText: String? = null,
    facts: List<RowFact> = emptyList(),
    flags: List<RowFlag> = emptyList(),
    compact: Boolean = false,
    muted: Boolean = false,
    selected: Boolean = false,
    label: String? = null,
    note: RowNote? = null,
    launcher: Boolean = false,
    branch: Boolean = false,
    /** Where it was opened (Lark group / Terminal, #3132): under the time, right-aligned, so the title keeps the whole line (#3043). */
    source: RowSource? = null,
    /** Which agent runs there, and in which client (#6390): its mark before the state mark (Glass: in its well). */
    agentMark: AgentMarkSpec? = null,
    /** Glass's third line ([RowPlace]); null: none, the facts say where it runs. */
    place: RowPlace? = null,
    /** It only waits in the background (#6045): Glass's well shows the clock on its corner instead of the dot. */
    waiting: Boolean = false,
    /** Glass's row style while not [compact] (iOS `AgentRowStyle`); Brief and Board keep their row. */
    style: AgentRowStyle = AgentRowStyle.Avatar,
    /** A folded child's request must stay visible even when Board's metadata normally takes the preview's line. */
    showPreviewWithMetadata: Boolean = false,
    end: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val board = PersonaTheme.style == AppStyle.Board
    val shown = if (muted) Status.Muted else status
    val tone = palette.status(shown, waiting)
    val strong = !muted && (status == Status.NeedsYou || status == Status.Unread)
    val glass = PersonaTheme.style == AppStyle.Glass
    val parts = AgentListDisplay.of(if (glass) style else AgentRowStyle.Avatar, compact).rowParts
    val avatar = glass && parts.leading == AgentRowLeading.Well && agentMark != null
    val placeLine = glass && parts.placeLine && place != null
    val mark = if (avatar) AvatarWell else markSize
    // Avatar's words take two lines, as iOS's; every other row keeps one.
    val wordLines = if (avatar) parts.wordLines else 1
    val start = if (board) Step * depth else (Step * depth + (Caret - mark) / 2).coerceAtLeast(None)
    val gap = if (avatar) AvatarGap else Tokens.Space.s - Tokens.Space.xxs
    val hasMetadata = note != null || facts.isNotEmpty()
    val boardMetadata = board && hasMetadata && !compact
    val statusLabel = stringResource(shown.labelRes)
    val press = Modifier.onSecondaryClick(onLongClick).combinedClickable(role = Role.Button, onClick = onClick, onLongClick = onLongClick)
        // Compact rows and Board's two-line metadata rows have no visible state line: TalkBack still says it (#3902).
        // Board also keeps the latest message in its merged text without replacing the title or metadata. A row whose
        // state is its dot's alone ([statusText] null, #4338 A9) is told it by name too.
        .then(if (compact || boardMetadata) Modifier.semantics {
            stateDescription = statusText ?: statusLabel
            if (boardMetadata && !showPreviewWithMetadata && !preview.isNullOrBlank()) text = AnnotatedString(preview.replace('\n', ' '))
        } else if (statusText == null) Modifier.semantics { stateDescription = statusLabel } else Modifier)
    val click = Modifier.selectedRow(selected).then(press)
    if (glass && parts.leading == AgentRowLeading.Dot) {
        TwoLineRow(title, shown, strong, muted, waiting, depth, position, preview, time, statusText, label, launcher, flags, note, facts,
            source, agentMark, place, branch, modifier.then(click), end)
        return
    }
    if (glass && parts.card) {
        StateCardRow(title, shown, strong, muted, waiting, depth, preview, time, statusText, label, launcher, flags, note, facts, source,
            agentMark, place, selected, modifier, press, end)
        return
    }
    val text: @Composable ColumnScope.() -> Unit = {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                LabeledTitle(label, Modifier.weight(1f, fill = false)) {
                    Text(
                        title, style = type.label.copy(fontWeight = if (strong) FontWeight.SemiBold else FontWeight.Medium),
                        color = if (muted) palette.ink2 else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    )
                }
                if (launcher) { Spacer(Modifier.width(Tokens.Space.s - Tokens.Space.xxs)); JTag(stringResource(R.string.designsystem_tab_launcher)) }
                flags.forEach { flag ->
                    Spacer(Modifier.width(Tokens.Space.xs))
                    JIcon(flag.icon, flag.label, size = Caret, tint = when { flag.caution -> palette.cautionText; flag.ai -> palette.ai; else -> palette.ink3 })
                }
            }
            // The well's mark names the agent (iOS's Avatar row writes no kind beside the title).
            agent?.takeIf { !avatar }?.let { Spacer(Modifier.width(Tokens.Space.s)); Text(it, style = type.micro, color = palette.ink2, maxLines = 1) }
            // one line only: the tag goes before the time
            if (compact || boardMetadata) source?.let { Spacer(Modifier.width(Tokens.Space.s)); SourceTag(it) }
            time?.takeIf { it.isNotEmpty() }?.let { Spacer(Modifier.width(Tokens.Space.s)); Text(it, style = type.micro, color = palette.ink3, maxLines = 1) }
        }
        if (!compact) {
            val line = buildAnnotatedString {
                statusText?.let { withStyle(SpanStyle(color = if (PersonaTheme.style == AppStyle.Brief && status == Status.Working) palette.ink else tone.text, fontWeight = FontWeight.Medium)) { append(it) } }
                if (statusText != null && !preview.isNullOrBlank()) append("  ")
                preview?.takeIf { it.isNotBlank() }?.let { append(it.replace('\n', ' ')) }
            }
            val lineText: @Composable () -> Unit = {
                Text(line, style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = wordLines, overflow = TextOverflow.Ellipsis)
            }
            if (!boardMetadata || showPreviewWithMetadata) {
                if (source != null && !placeLine) Row(verticalAlignment = Alignment.CenterVertically) {
                    Box(Modifier.weight(1f)) { if (line.isNotEmpty()) lineText() }
                    Spacer(Modifier.width(Tokens.Space.s))
                    SourceTag(source)
                } else if (line.isNotEmpty()) lineText()
            }
        }
    }
    // a branch hangs off its initiator one step up: a hairline down from under that row's mark
    val guide = if (!branch) Modifier else Modifier.drawBehind {
        val x = (if (board) Step * (depth - 1) + Holder / 2 else (Step * (depth - 1) + (Caret - mark) / 2).coerceAtLeast(None) + mark / 2).toPx()
        drawLine(palette.line, Offset(x, 0f), Offset(x, size.height), Hairline.toPx())
    }
    if (board) {
        val shape = RoundedCornerShape(PersonaTheme.shapes.card)
        Column(
            modifier.fillMaxWidth().then(guide).padding(start = start, bottom = if (position.last) None else Tokens.Space.s - Tokens.Space.xxs).clip(shape)
                .background(palette.surface).drawBehind { drawRect(tone.dot, size = size.copy(width = Tokens.Space.xxs.toPx())) }
                .border(Hairline, palette.line, shape).then(click).stableAlpha(if (muted) 0.75f else 1f),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                // The agent's mark before its state (#6390): what runs there, then how it is doing.
                agentMark?.let { Spacer(Modifier.width(Tokens.Space.s)); JAgentMark(it) }
                Box(Modifier.width(Holder), contentAlignment = Alignment.Center) { HolderGlyph(shown) }
                Column(
                    Modifier.weight(1f).padding(horizontal = Tokens.Space.m - Tokens.Space.xxs)
                        .padding(top = Tokens.Space.s - Tokens.Space.xxs, bottom = if (boardMetadata) None else Tokens.Space.s - Tokens.Space.xxs),
                    content = text,
                )
                end?.invoke()
            }
            if (boardMetadata) {
                val indent = (if (agentMark != null) Tokens.Space.s + Caret else None) + Holder + Tokens.Space.m
                RowMetadata(note, facts, indent, Modifier.padding(bottom = Tokens.Space.s - Tokens.Space.xxs))
            }
        }
        return
    }
    Column(
        modifier.fillMaxWidth().then(guide).then(click)
            .drawBehind {
                if (!position.first) drawLine(palette.line, Offset((start + mark + gap).toPx(), 0f), Offset(size.width, 0f), Hairline.toPx())
            }
            .stableAlpha(if (muted) 0.72f else 1f),
    ) {
        Row(
            Modifier.padding(start = start, top = Tokens.Space.s, bottom = if ((hasMetadata || placeLine) && !compact) None else Tokens.Space.s),
            verticalAlignment = if (compact) Alignment.CenterVertically else Alignment.Top,
        ) {
            if (avatar) AgentAvatar(requireNotNull(agentMark), shown, waiting) else {
                // The agent's mark before its state (#6390): what runs there, then how it is doing.
                agentMark?.let { Box(Modifier.height(mark), contentAlignment = Alignment.Center) { JAgentMark(it) }; Spacer(Modifier.width(Tokens.Space.xs)) }
                Box(Modifier.size(mark), contentAlignment = Alignment.Center) { JStatusMark(shown) }
            }
            Spacer(Modifier.width(gap))
            Column(Modifier.weight(1f).padding(top = if (compact) None else Tokens.Space.xxs), content = text)
            end?.let { Box(Modifier.align(Alignment.CenterVertically)) { it() } }
        }
        if (hasMetadata && !compact) {
            val indent = (if (agentMark != null && !avatar) Caret + Tokens.Space.xs else None) + mark + gap
            RowMetadata(note, facts, indent, Modifier.padding(start = start, bottom = if (placeLine) None else Tokens.Space.s))
        }
        if (placeLine) PlaceLine(requireNotNull(place), source, Modifier.padding(start = start + mark + gap, top = Tokens.Space.xs, bottom = Tokens.Space.s))
    }
}

/** A display's name in the Agents page's ⋯ › Display and in Settings (iOS `AgentListDisplay.title`). */
val AgentListDisplay.label: String @Composable get() = stringResource(when (this) {
    AgentListDisplay.Avatar -> R.string.designsystem_row_style_avatar
    AgentListDisplay.TwoLines -> R.string.designsystem_row_style_two_lines
    AgentListDisplay.Cards -> R.string.designsystem_row_style_cards
    AgentListDisplay.CompactList -> R.string.designsystem_row_style_list
})

/** What a display looks like, under its preview in Settings (iOS `AgentListDisplay.summary`). */
val AgentListDisplay.summary: String @Composable get() = stringResource(when (this) {
    AgentListDisplay.Avatar -> R.string.designsystem_row_style_avatar_summary
    AgentListDisplay.TwoLines -> R.string.designsystem_row_style_two_lines_summary
    AgentListDisplay.Cards -> R.string.designsystem_row_style_cards_summary
    AgentListDisplay.CompactList -> R.string.designsystem_row_style_list_summary
})

/**
 * The first line of Glass's Two Lines and State Cards rows (iOS `twoLineRow`, `cardRow`): [leading] (Two Lines' dot), the
 * agent's mark, the title with the tab's [label], the starter's tag and the [flags], then the time.
 */
@Composable
private fun StyledTitleLine(
    title: String, strong: Boolean, muted: Boolean, label: String?, launcher: Boolean, flags: List<RowFlag>, agentMark: AgentMarkSpec?,
    time: String?, leading: (@Composable () -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Row(verticalAlignment = Alignment.CenterVertically) {
        leading?.invoke()
        agentMark?.let { JAgentMark(it, size = Tokens.Size.markS); Spacer(Modifier.width(Tokens.Space.xs)) }
        Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
            LabeledTitle(label, Modifier.weight(1f, fill = false)) {
                Text(
                    title, style = type.label.copy(fontWeight = if (strong) FontWeight.SemiBold else FontWeight.Medium),
                    color = if (muted) palette.ink2 else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                )
            }
            if (launcher) { Spacer(Modifier.width(Tokens.Space.xs)); JTag(stringResource(R.string.designsystem_tab_launcher)) }
            flags.forEach { flag ->
                Spacer(Modifier.width(Tokens.Space.xs))
                JIcon(flag.icon, flag.label, size = Caret, tint = when { flag.caution -> palette.cautionText; flag.ai -> palette.ai; else -> palette.ink3 })
            }
        }
        time?.takeIf { it.isNotEmpty() }?.let { Spacer(Modifier.width(Tokens.Space.s)); Text(it, style = type.micro, color = palette.ink3, maxLines = 1) }
    }
}

/** A row's state as a dot of its color, or the clock of one that only waits in the background ([waiting]). */
@Composable
private fun RowStateDot(status: Status, waiting: Boolean) {
    if (waiting) JIcon(persona.core.designsystem.icons.PersonaIcons.backgroundWaiting, null, size = Tokens.Size.dotL * WaitClockScale,
        tint = PersonaTheme.palette.backgroundWaiting.dot, decorative = true)
    else JStatusDot(status)
}

/**
 * Glass's Two Lines row (iOS style B, `twoLineRow`, #4338 A9): the state's dot under its heading's caret, the agent's mark
 * and the title with the time; then its state word ([statusText]), one line of its latest words (where it runs while it
 * has said nothing), the muted mark and the [source]'s tag at the end. No model or to-do: B has no line for them. The
 * facts line follows, as on the Avatar row; a hairline above it from the title's start, unless first in its group.
 */
@Composable
private fun TwoLineRow(
    title: String, shown: Status, strong: Boolean, muted: Boolean, waiting: Boolean, depth: Int, position: GroupPosition,
    preview: String?, time: String?, statusText: String?, label: String?, launcher: Boolean, flags: List<RowFlag>,
    note: RowNote?, facts: List<RowFact>, source: RowSource?, agentMark: AgentMarkSpec?, place: RowPlace?, branch: Boolean,
    modifier: Modifier, end: (@Composable () -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val tone = palette.status(shown, waiting)
    val start = Step * depth
    val words = start + Caret + TwoLineGap
    val hasMetadata = note != null || facts.isNotEmpty()
    // a branch hangs off its initiator one step up: a hairline down from under that row's dot
    val guide = if (!branch) Modifier else Modifier.drawBehind {
        val x = (Step * (depth - 1) + Caret / 2).toPx()
        drawLine(palette.line, Offset(x, 0f), Offset(x, size.height), Hairline.toPx())
    }
    Column(
        modifier.fillMaxWidth().then(guide)
            .drawBehind { if (!position.first) drawLine(palette.line, Offset(words.toPx(), 0f), Offset(size.width, 0f), Hairline.toPx()) }
            .stableAlpha(if (muted) 0.72f else 1f),
    ) {
        Row(Modifier.padding(start = start, top = Tokens.Space.s, bottom = if (hasMetadata) None else Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                StyledTitleLine(title, strong, muted, label, launcher, flags, agentMark, time, leading = {
                    Box(Modifier.width(Caret), contentAlignment = Alignment.Center) { RowStateDot(shown, waiting) }
                    Spacer(Modifier.width(TwoLineGap))
                })
                val said = preview?.takeIf { it.isNotBlank() }?.replace('\n', ' ') ?: place?.words?.takeIf(String::isNotEmpty)
                val line = buildAnnotatedString {
                    statusText?.let { withStyle(SpanStyle(color = tone.text, fontWeight = FontWeight.Medium)) { append(it) } }
                    if (statusText != null && said != null) append("  ")
                    said?.let { append(it) }
                }
                if (line.isNotEmpty() || place?.muted == true || source != null) Row(Modifier.padding(start = Caret + TwoLineGap), verticalAlignment = Alignment.CenterVertically) {
                    Text(line, Modifier.weight(1f), style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (place?.muted == true) {
                        Spacer(Modifier.width(Tokens.Space.xs))
                        JIcon(PhIcons.Prohibit, stringResource(R.string.designsystem_row_muted), size = Caret, tint = palette.ink3)
                    }
                    source?.let { Spacer(Modifier.width(Tokens.Space.s)); SourceTag(it) }
                }
            }
            end?.let { Box { it() } }
        }
        if (hasMetadata) RowMetadata(note, facts, Caret + TwoLineGap, Modifier.padding(start = start, bottom = Tokens.Space.s))
    }
}

/**
 * Glass's State Cards row (iOS style C, `cardRow`, #4338 A9): a card of its own [depth] steps in, its state's color in a
 * bar down the leading edge; the agent's mark and the title with the time; two lines of its latest words; the facts
 * line; at the foot its state in words in its color ([statusText], every state but Seen: `cardStateWord`) and where it
 * runs ([PlaceLine]). One that needs you is tinted ([CardWashAlpha]); one open beside the list is ringed in the accent.
 * One that only waits in the background ([waiting]) takes that state's lighter tone, as iOS's `stateTone`.
 */
@Composable
private fun StateCardRow(
    title: String, shown: Status, strong: Boolean, muted: Boolean, waiting: Boolean, depth: Int, preview: String?, time: String?,
    statusText: String?, label: String?, launcher: Boolean, flags: List<RowFlag>, note: RowNote?, facts: List<RowFact>, source: RowSource?,
    agentMark: AgentMarkSpec?, place: RowPlace?, selected: Boolean, modifier: Modifier, press: Modifier, end: (@Composable () -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val tone = palette.status(shown, waiting)
    val shape = PersonaTheme.shapes.cardShape
    Column(
        modifier.fillMaxWidth().padding(start = Step * depth, top = Tokens.Space.xs, bottom = Tokens.Space.xs)
            .clip(shape).background(if (shown == Status.NeedsYou) palette.needsYou.dot.copy(alpha = CardWashAlpha) else palette.surfaceOpaque)
            .border(Tokens.Size.hairline, palette.line, shape)
            .then(if (selected) Modifier.border(Tokens.Size.lineMedium, palette.accent, shape).semantics { this.selected = true } else Modifier)
            .then(press)
            .drawBehind {
                val bar = Tokens.Space.xs.toPx()
                val inset = Tokens.Space.s.toPx()
                drawRoundRect(tone.dot, Offset(Tokens.Space.xs.toPx(), inset), Size(bar, (size.height - inset * 2).coerceAtLeast(0f)), CornerRadius(bar / 2))
            }
            .stableAlpha(if (muted) 0.72f else 1f)
            .padding(start = Tokens.Space.l, end = Tokens.Space.m, top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Box(Modifier.weight(1f)) { StyledTitleLine(title, strong, muted, label, launcher, flags, agentMark, time) }
            end?.invoke()
        }
        preview?.takeIf { it.isNotBlank() }?.let {
            Text(it.replace('\n', ' '), style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        if (note != null || facts.isNotEmpty()) RowMetadata(note, facts, None, Modifier.padding(top = None))
        if (statusText != null || place != null || source != null) Row(verticalAlignment = Alignment.CenterVertically) {
            statusText?.let {
                Text(it, style = type.caption.copy(fontWeight = FontWeight.Medium), color = tone.text, maxLines = 1)
                Spacer(Modifier.width(Tokens.Space.s))
            }
            PlaceLine(place ?: RowPlace(), source, Modifier.weight(1f))
        }
    }
}

@Composable
private fun rowFactsText(facts: List<RowFact>): AnnotatedString {
    val palette = PersonaTheme.palette
    return buildAnnotatedString {
        facts.forEachIndexed { index, fact ->
            if (index > 0) append("   ")
            withStyle(SpanStyle(color = if (fact.caution) palette.cautionText else palette.ink3)) { append(fact.text) }
        }
    }
}

/**
 * One metadata line, normally aligned with the title. When long facts would consume the Workflow's width there, the line
 * uses the space below the row's marks too. Facts have priority and ellipsize only when they cannot fit; a bounded Workflow
 * slot keeps its full text in semantics while its visible text ellipsizes in the remaining width.
 */
@Composable
private fun RowMetadata(note: RowNote?, facts: List<RowFact>, indent: Dp, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val textStyle = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal)
    val factsLine = rowFactsText(facts)
    val value = note?.let { rollingText(it.fields.map { field -> field.text() }.filter(String::isNotEmpty).joinToString("   ")) }
    Layout(
        content = {
            if (note != null) Row(verticalAlignment = Alignment.CenterVertically) {
                JIcon(note.icon, null, size = Caret, tint = palette.ai)
                Spacer(Modifier.width(Tokens.Space.xxs))
                Text(value.orEmpty(), style = textStyle, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (factsLine.isNotEmpty()) Text(factsLine, style = textStyle, maxLines = 1, overflow = TextOverflow.Ellipsis)
        },
        modifier = modifier.padding(top = Tokens.Space.xxs),
    ) { measurables, constraints ->
        val noteMeasurable = if (note != null) measurables.first() else null
        val factsMeasurable = if (factsLine.isNotEmpty()) measurables.last() else null
        val gap = if (note != null && factsMeasurable != null) Tokens.Space.xs.roundToPx() else 0
        val minimumNote = if (note != null) (Caret + Tokens.Space.xxs + Tokens.Space.l).roundToPx() else 0
        // Intrinsics choose the allocation; Compose permits only one measure() call for each child.
        val desiredFactsWidth = factsMeasurable?.maxIntrinsicWidth(constraints.maxHeight) ?: 0
        val indented = indent.roundToPx().coerceAtLeast(0)
        fun metadataStart(factsWidth: Int) = if (indented + minimumNote + gap + factsWidth <= constraints.maxWidth) indented else 0
        val fullStart = metadataStart(desiredFactsWidth)
        val needsNoteSlot = noteMeasurable != null && factsMeasurable != null &&
            constraints.maxWidth - fullStart - gap - desiredFactsWidth < minimumNote
        val reservedNote = if (needsNoteSlot) minimumNote + gap else 0
        val factsMaxWidth = (constraints.maxWidth - reservedNote).coerceAtLeast(0)
        val factsPlaceable = factsMeasurable?.measure(constraints.copy(minWidth = 0, maxWidth = factsMaxWidth))
        val factsWidth = factsPlaceable?.width ?: 0
        val start = metadataStart(factsWidth)
        val noteWidth = (constraints.maxWidth - start - gap - factsWidth).coerceAtLeast(0)
        val notePlaceable = if (noteWidth > 0) noteMeasurable?.measure(constraints.copy(minWidth = 0, maxWidth = noteWidth)) else null
        val width = constraints.maxWidth
        val height = maxOf(notePlaceable?.height ?: 0, factsPlaceable?.height ?: 0)
        layout(width, height) {
            var x = start
            notePlaceable?.let {
                it.placeRelative(x, (height - it.height) / 2)
                x += it.width + gap
            }
            factsPlaceable?.placeRelative(x, (height - factsPlaceable.height) / 2)
        }
    }
}

/**
 * A herdr tab holding several agents (#2320): drawn as the row of the most urgent of them ([title], [status], its
 * latest words) after the tab's [label] (`#2` or its name), with "N Agents" at the end that folds and opens the
 * tab's agents ([onToggle]; they are listed under it as [AgentRow]s, the initiator marked as the [launcher] and the panes it
 * split off one step further in as [branch]es). Tapping the row opens that most urgent agent ([onClick]).
 */
@Composable
fun AgentTabRow(
    label: String,
    title: String,
    status: Status,
    agents: Int,
    folded: Boolean,
    onToggle: () -> Unit,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    depth: Int = 0,
    position: GroupPosition = GroupPosition.Single,
    preview: String? = null,
    time: String? = null,
    agent: String? = null,
    statusText: String? = null,
    compact: Boolean = false,
    selected: Boolean = false,
) = AgentRow(
    title, status, onClick, modifier, depth = depth, position = position, preview = preview, time = time, agent = agent,
    statusText = statusText, compact = compact, selected = selected, label = label.takeIf { it.isNotBlank() },
    end = { TabToggle(agents, folded, onToggle) },
)

/** "N Agents" and a caret: folds a tab's agents to its row and opens them again. */
@Composable
private fun TabToggle(agents: Int, folded: Boolean, onToggle: () -> Unit) {
    val palette = PersonaTheme.palette
    val count = pluralStringResource(R.plurals.designsystem_tab_agents, agents, agents)
    val state = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    Row(
        Modifier.heightIn(min = Action).minimumInteractiveComponentSize().clip(RoundedCornerShape(PersonaTheme.shapes.chip)).clickable(role = Role.Button, onClick = onToggle)
            .clearAndSetSemantics { contentDescription = count; stateDescription = state }
            .padding(horizontal = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(count, style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1)
        Spacer(Modifier.width(Tokens.Space.xs))
        JIcon(if (folded) PhIcons.CaretRight else PhIcons.CaretDown, null, size = Caret, tint = palette.ink3)
    }
}

/**
 * A row's title after an optional [label] (a tab's `#2` or name): the label takes at most half the width, so a long tab
 * name truncates instead of hiding the title, and the title gets the rest.
 */
@Composable
private fun LabeledTitle(label: String?, modifier: Modifier, title: @Composable () -> Unit) {
    if (label == null) { Box(modifier) { title() }; return }
    val palette = PersonaTheme.palette
    val gap = Tokens.Space.s - Tokens.Space.xxs
    Layout({
        Text(label, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Medium), color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        title()
    }, modifier) { measurables, constraints ->
        val space = gap.roundToPx()
        val first = measurables[0].measure(constraints.copy(minWidth = 0, maxWidth = constraints.maxWidth / 2))
        val second = measurables[1].measure(constraints.copy(minWidth = 0, maxWidth = (constraints.maxWidth - first.width - space).coerceAtLeast(0)))
        val height = maxOf(first.height, second.height)
        layout(first.width + space + second.width, height) {
            first.placeRelative(0, (height - first.height) / 2)
            second.placeRelative(first.width + space, (height - second.height) / 2)
        }
    }
}

/** The glyph in a board strip's holder, in the status's ink. */
@Composable
private fun HolderGlyph(status: Status) {
    val ink = PersonaTheme.palette.status(status).text
    when (status) {
        Status.Working, Status.Waiting -> if (PersonaTheme.reduceMotion) JIcon(PhIcons.Play, null, size = Caret, tint = ink, selected = true)
            else JSpinner(Modifier.size(Caret), color = ink, stroke = Tokens.Space.Standard.xxs)
        Status.NeedsYou -> JIcon(PhIcons.Hand, null, size = Caret, tint = ink, selected = true)
        Status.Unread -> JIcon(PhIcons.Check, null, size = Caret, tint = ink)
        Status.Offline -> JIcon(PhIcons.Power, null, size = Caret, tint = ink)
        else -> JIcon(PhIcons.Checks, null, size = Caret, tint = ink)
    }
}

/**
 * Glass's leading mark on an Agents row (iOS `AgentAvatar`, #4338 A9): the agent's mark in a round well, its state on the
 * well's corner, the clock for one that only waits in the background ([waiting]). The corner is rimmed by a ring cut out of
 * the well, so it reads apart from the well on whatever the row sits on (the page, a repository's card, a selected row's
 * plate), as iOS rims it in the row's own background. TalkBack hears the mark's agent; the row says the state.
 */
@Composable
private fun AgentAvatar(mark: AgentMarkSpec, status: Status, waiting: Boolean) {
    val palette = PersonaTheme.palette
    val corner = Tokens.Size.dotXL + AvatarRim * 2
    Box(Modifier.size(AvatarWell)) {
        Box(
            Modifier.matchParentSize().graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
                .drawWithContent {
                    drawContent()
                    val radius = corner.toPx() / 2
                    val center = Offset(size.width - radius + AvatarRim.toPx(), size.height - radius + AvatarRim.toPx())
                    drawCircle(palette.ink, radius, center, blendMode = BlendMode.Clear)
                }
                .clip(CircleShape).background(palette.sunken),
            contentAlignment = Alignment.Center,
        ) { JAgentMark(mark, size = AvatarMark) }
        Box(Modifier.align(Alignment.BottomEnd).offset(AvatarRim, AvatarRim).size(corner), contentAlignment = Alignment.Center) {
            if (waiting) JIcon(persona.core.designsystem.icons.PersonaIcons.backgroundWaiting, null, size = corner,
                tint = palette.backgroundWaiting.dot, decorative = true)
            else JStatusDot(status, size = Tokens.Size.dotXL)
        }
    }
}

/**
 * Glass's third line of an Agents row ([RowPlace], iOS `SlotRow.placeLine`, #4338 A9): the [source] tag, where it runs,
 * the model, the to-do as an outlined chip and the bell of a muted one, a step apart. The words give way first; the
 * to-do keeps its chip while it fits beside them (or in half the room), else it is its mark alone (iOS `ViewThatFits`).
 */
@Composable
private fun PlaceLine(place: RowPlace, source: RowSource?, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val mutedLabel = stringResource(R.string.designsystem_row_muted)
    Layout(
        content = {
            source?.let { Box(Modifier.layoutId(PlaceSource)) { SourceTag(it) } }
            place.words.takeIf(String::isNotEmpty)?.let {
                Text(it, Modifier.layoutId(PlaceWords), style = type.caption.copy(fontWeight = FontWeight.Normal), color = palette.ink3,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            place.model?.let {
                Text(it, Modifier.layoutId(PlaceModel).padding(horizontal = Tokens.Space.sm), style = PersonaTheme.type.micro,
                    color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            place.todo?.let { TodoChip(it, Modifier.layoutId(PlaceTodo)) }
            if (place.muted) JIcon(PhIcons.Prohibit, mutedLabel, Modifier.layoutId(PlaceBell), size = Caret, tint = palette.ink3)
        },
        modifier = modifier.fillMaxWidth(),
    ) { measurables, constraints ->
        val gap = Tokens.Space.xs.roundToPx()
        val byId = measurables.associateBy { it.layoutId }
        var left = constraints.maxWidth
        fun fit(id: Any, width: Int = left): Placeable? =
            byId[id]?.measure(Constraints(maxWidth = width.coerceAtLeast(0)))?.also { left -= it.width + gap }
        val sourceP = fit(PlaceSource)
        val bellP = fit(PlaceBell)
        val modelP = fit(PlaceModel)
        val words = byId[PlaceWords]
        val todoP = byId[PlaceTodo]?.let { todo ->
            val chip = todo.maxIntrinsicWidth(constraints.maxHeight)
            val room = left - if (words != null) gap else 0
            val full = if (words == null) chip <= room else chip + words.maxIntrinsicWidth(constraints.maxHeight) <= room || chip <= room / 2
            // Too narrow for the chip, the chip draws its mark alone.
            fit(PlaceTodo, if (full) left else Caret.roundToPx())
        }
        val wordsP = words?.measure(Constraints(maxWidth = left.coerceAtLeast(0)))
        val parts = listOfNotNull(sourceP, wordsP, modelP, todoP, bellP)
        val height = parts.maxOfOrNull { it.height } ?: 0
        layout(constraints.maxWidth, height) {
            var x = 0
            parts.forEach { it.placeRelative(x, (height - it.height) / 2); x += it.width + gap }
        }
    }
}

private const val PlaceSource = "source"
private const val PlaceWords = "words"
private const val PlaceModel = "model"
private const val PlaceTodo = "todo"
private const val PlaceBell = "bell"

/**
 * A row's to-do on its third line (iOS `Chip(look: .outline)` with the to-do's mark): the mark and the title in a hairline
 * capsule; offered less than its width, the mark alone. TalkBack hears "To-do: <title>" either way.
 */
@Composable
private fun TodoChip(title: String, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val spoken = stringResource(R.string.designsystem_row_todo, title)
    val pad = Tokens.Space.s
    Layout(
        content = {
            Box(Modifier.border(Hairline, palette.ink3.copy(alpha = OutlineAlpha), CircleShape))
            JIcon(persona.core.designsystem.icons.PersonaIcons.todo, null, size = Caret, tint = palette.ink3)
            Text(title, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Medium), color = palette.ink3, maxLines = 1)
        },
        modifier = modifier.clearAndSetSemantics { contentDescription = spoken },
    ) { measurables, constraints ->
        val (outline, icon, text) = measurables
        val inset = pad.roundToPx()
        val space = Tokens.Space.xs.roundToPx()
        val iconP = icon.measure(Constraints())
        val textP = text.measure(Constraints())
        val vertical = Tokens.Space.xxs.roundToPx()
        val chipWidth = inset + iconP.width + space + textP.width + inset
        val chipHeight = maxOf(iconP.height, textP.height) + vertical * 2
        if (chipWidth <= constraints.maxWidth) {
            val outlineP = outline.measure(Constraints.fixed(chipWidth, chipHeight))
            layout(chipWidth, chipHeight) {
                outlineP.place(0, 0)
                iconP.placeRelative(inset, (chipHeight - iconP.height) / 2)
                textP.placeRelative(inset + iconP.width + space, (chipHeight - textP.height) / 2)
            }
        } else layout(iconP.width, iconP.height) { iconP.placeRelative(0, 0) }
    }
}

/** An agent row's source tag ([AgentRow]'s `source`): its word, its tint and, for a Lark group with a topic, what a tap opens. */
@Immutable
data class RowSource(val label: String, val kind: SourceKind, val onClick: (() -> Unit)? = null, val clickLabel: String? = null)

/** The source tag: the time's size, a tight slip in the source's tint; a button when it opens something. */
@Composable
private fun SourceTag(source: RowSource) {
    val colors = PersonaTheme.palette.source(source.kind)
    val shape = RoundedCornerShape(if (PersonaTheme.style == AppStyle.Board) Tokens.Space.xs else Tokens.Space.s - Tokens.Space.xxs)
    val tap = source.onClick?.let { Modifier.clickable(role = Role.Button, onClickLabel = source.clickLabel, onClick = it) } ?: Modifier
    Text(source.label, style = PersonaTheme.type.micro.copy(fontWeight = FontWeight.Medium), color = colors.text, maxLines = 1,
        modifier = Modifier.then(if (source.onClick != null) Modifier.minimumInteractiveComponentSize() else Modifier)
            .clip(shape).then(tap).background(PersonaTheme.palette.statusSurface).padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs / 2))
}

// ------------------------------------------------------------------ quick filters

/** The narrowest a board bay gets before the board scrolls instead: room for a short label like "Terminal" or "Lark group". */
private val BoardBayMinWidth = 92.dp
/** How much of the next bay a scrolled board shows at its edge. */
private const val BoardBayPeek = 0.4f

/** One quick filter: a label, its count, and the status it filters by. */
@Immutable
/**
 * A quick filter: its words, count and state ([status]'s dot, or [icon] in its color: Waiting in the background's clock).
 * [waiting]: it lists the agents that only wait in the background, so the Glass home draws it in that state's lighter tone.
 */
data class Bay(
    val label: String, val count: Int? = null, val status: Status? = null, val icon: persona.core.designsystem.icons.PhIcon? = null,
    val waiting: Boolean = false,
)

/**
 * The quick filters of a list. Glass shows glass chips; brief underlined tabs; the board a counter board with
 * one big number per bay — tapping a cell filters to it. A Needs You dot is still, as on iOS's `QuickFilterBar`.
 */
@Composable
fun QuickFilterBar(bays: List<Bay>, selected: Int, onSelect: (Int) -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    when (PersonaTheme.style) {
        AppStyle.Glass -> Row(modifier.fillMaxWidth().horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(8.dp.dense)) {
            bays.forEachIndexed { index, bay -> JChip(bay.label, selected = index == selected, onClick = { onSelect(index) }, status = bay.status, count = bay.count, breathes = false) }
        }
        // Tabs that do not fit scroll sideways rather than being cut off at the edge.
        AppStyle.Brief -> JSegmented(bays.map { JSegment(it.label, it.count) }, selected, onSelect, modifier.horizontalScroll(rememberScrollState()))
        AppStyle.Board -> BoxWithConstraints(modifier.fillMaxWidth()) {
            val shape = RoundedCornerShape(10.dp.corner)
            // Each bay is a share of the width while its label fits; more bays than that (the Agents list's six) keep
            // their width and the board scrolls sideways instead of cutting every label short.
            val fits = maxWidth / bays.size.coerceAtLeast(1) >= BoardBayMinWidth
            // Scrolled, as many whole bays as fit and part of the next, so the board reads as one that scrolls.
            val whole = (maxWidth / BoardBayMinWidth).toInt().coerceAtLeast(1)
            val bayWidth = maxWidth / (whole + BoardBayPeek)
            val frame = Modifier.fillMaxWidth().clip(shape).background(palette.surface).border(1.dp, palette.line, shape)
            val row = if (fits) frame.height(IntrinsicSize.Min) else frame.horizontalScroll(rememberScrollState()).height(IntrinsicSize.Min)
            Row(row) {
                bays.forEachIndexed { index, bay ->
                    if (index > 0) Box(Modifier.width(1.dp).fillMaxHeight().background(palette.line))
                    val on = index == selected
                    val color = bay.status?.let { palette.status(it).dot } ?: palette.ink
                    val cell = if (fits) Modifier.weight(1f) else Modifier.widthIn(min = bayWidth)
                    Box(cell.clickable(role = Role.Tab) { onSelect(index) }.semantics { this.selected = on }) {
                        Column(Modifier.padding(start = 12.dp.dense, top = 10.dp.dense, bottom = 12.dp.dense, end = 6.dp.dense)) {
                            Text("${bay.count ?: "—"}", style = type.number, color = if ((bay.count ?: 0) == 0) palette.ink3 else palette.ink, maxLines = 1)
                            Spacer(Modifier.height(2.dp.dense))
                            Row(verticalAlignment = Alignment.CenterVertically) {
                                if (bay.status != null) { Box(Modifier.size(8.dp).clip(RoundedCornerShape(2.dp)).background(color)); Spacer(Modifier.width(5.dp.dense)) }
                                Text(bay.label, style = type.caption.copy(fontSize = androidx.compose.ui.unit.TextUnit(12f, androidx.compose.ui.unit.TextUnitType.Sp)), color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                        }
                        if (on) Box(Modifier.align(Alignment.BottomStart).fillMaxWidth().height(3.dp).background(color))
                    }
                }
            }
        }
    }
}
