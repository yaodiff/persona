package persona.core.designsystem

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.SemanticsPropertyReceiver
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons

// By Repository's lines on Glass (iOS `RepoTreeRows.swift`, #4338 A7, A8; docs/android/android-design.md 2.4): each
// repository a card on the page, its header line and its working trees' lines in it, their agents in the work-item rows
// (`AgentRow`) under their line, the idle ones behind one quiet line; a computer's idle repositories one row each in its
// Idle Repositories. Brief and Board keep `AgentSpaceHeader` and `AgentIdleRow` (decision 8).

/** A level in (iOS `Theme.Tree.indent`), the column a line's mark sits in (`Theme.Tree.column`), above and below its words. */
private val TreeIndent = Tokens.Space.Standard.xl - Tokens.Space.Standard.xxs
private val TreeColumn = Tokens.Space.Standard.l + Tokens.Space.Standard.xxs
private val TreeRowPadding get() = Tokens.Space.sm
private val TreeChevron = Tokens.Space.Standard.m
private val TreeWorktreeMark = Tokens.Space.Standard.m + Tokens.Space.Standard.xxs

/**
 * The fill of By Repository's cards (iOS's grouped card), from the page that holds them ([JAgentHomeSurface]); null where
 * the page has none, and the lines sit on the page.
 */
val LocalRepoCard = staticCompositionLocalOf<Color?> { null }

/**
 * A line's slice of its repository's card (iOS's inset grouped section): the card's fill, its top corners and the room
 * between cards above it on the [first] line ([spaced]; none right under a section's header, Idle Repositories), its
 * bottom corners on the [last], the card's sides inside it. The rows of the card each draw their slice, as the list
 * draws one line an item.
 */
@Composable
fun Modifier.repoCard(first: Boolean, last: Boolean, spaced: Boolean = true): Modifier {
    val fill = LocalRepoCard.current ?: return this
    val radius = PersonaTheme.shapes.panel
    val shape = RoundedCornerShape(
        topStart = if (first) radius else Tokens.Space.flush, topEnd = if (first) radius else Tokens.Space.flush,
        bottomStart = if (last) radius else Tokens.Space.flush, bottomEnd = if (last) radius else Tokens.Space.flush,
    )
    return padding(top = if (first && spaced) Tokens.Space.m else Tokens.Space.flush).clip(shape).background(fill)
        .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.flush)
        .padding(top = if (first) Tokens.Space.xs else Tokens.Space.flush, bottom = if (last) Tokens.Space.xs else Tokens.Space.flush)
}

/** The chevron at a line's end that turns as it folds (iOS `FoldChevron`): down while open, right while folded. */
@Composable
private fun FoldChevron(folded: Boolean) {
    val turn by animateFloatAsState(if (folded) -90f else 0f,
        if (PersonaTheme.reduceMotion) snap() else tween(PersonaTheme.motion.standard), label = "fold chevron")
    JIcon(PersonaIcons.expand, null, size = TreeChevron, tint = PersonaTheme.palette.ink3, modifier = Modifier.rotate(turn))
}

/**
 * A line of the tree (iOS `RepoTreeLine`): [level] levels in, its [mark] in the tree's column so marks line up level by
 * level, at least a finger's height, the whole line the button, read as one: [spoken] (after the click, which keeps
 * its actions).
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun RepoTreeLine(
    level: Int, onClick: () -> Unit, modifier: Modifier, spoken: SemanticsPropertyReceiver.() -> Unit,
    onLongClick: (() -> Unit)? = null, onLongClickLabel: String? = null,
    mark: @Composable () -> Unit, content: @Composable RowScope.() -> Unit,
) {
    Row(
        modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)
            .onSecondaryClick(onLongClick)
            .combinedClickable(role = Role.Button, onClick = onClick, onLongClick = onLongClick, onLongClickLabel = onLongClickLabel)
            .clearAndSetSemantics(spoken)
            .padding(start = TreeIndent * level, top = TreeRowPadding, bottom = TreeRowPadding),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        Box(Modifier.width(TreeColumn), contentAlignment = Alignment.Center) { mark() }
        content()
    }
}

/**
 * A repository's header (iOS `RepoCardHeader`): its folder, its [title] in bold and the chevron that turns as it folds;
 * no counts. Its actions (the space's open, pin, mute, rename and to-do; Mark all as read) are its long press
 * ([onLongClick], read as [actionsLabel]), with no ⋯ of its own. A repository with nothing going on is a row of its
 * computer's Idle Repositories instead ([AgentIdleRepoRow]).
 */
@Composable
fun AgentRepoHeader(
    title: String,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    onLongClick: (() -> Unit)? = null,
    actionsLabel: String? = null,
    main: Boolean = false,
    offline: Boolean = false,
    pinned: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val kind = stringResource(R.string.designsystem_repository)
    val primary = stringResource(R.string.designsystem_worktree_primary)
    val offlineText = stringResource(R.string.designsystem_worktree_offline)
    val pinnedText = stringResource(R.string.designsystem_project_pinned)
    val foldState = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    val separator = stringResource(R.string.designsystem_separator)
    RepoTreeLine(
        level = 0, onClick = onToggle, onLongClick = onLongClick, onLongClickLabel = actionsLabel, modifier = modifier,
        spoken = {
            heading()
            contentDescription = listOfNotNull(title, kind, primary.takeIf { main }, offlineText.takeIf { offline },
                pinnedText.takeIf { pinned }).joinToString(separator)
            stateDescription = foldState
        },
        mark = { JIcon(if (main) PhIcons.House else PersonaIcons.folder, null, size = Tokens.Space.l, tint = palette.ink2) },
    ) {
        Text(title, style = PersonaTheme.type.bodyStrong, color = if (offline) palette.ink2 else palette.ink, maxLines = 1,
            overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        if (pinned) JIcon(PersonaIcons.pinned, null, size = TreeWorktreeMark, tint = palette.ink2)
        FoldChevron(folded)
    }
}

/**
 * A working tree's line in its repository's card (iOS `RepoWorktreeLine`): ⌂ for the main checkout with its Primary chip,
 * and no mark for the others so the name and indentation carry the meaning like the web sidebar, its [name], small after
 * it what the name does not say ([branch], or [detached] HEAD in the caution color), at the end [tally] ("N agents",
 * Idle), the source's [pinned] mark and the chevron. [quiet] or [offline] put the name in the secondary color.
 * A tap folds it; a long press is its actions ([onLongClick], read as [actionsLabel]), as the repository header's.
 */
@Composable
fun AgentWorktreeLine(
    name: String,
    main: Boolean,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    level: Int = 1,
    branch: String? = null,
    detached: Boolean = false,
    tally: String? = null,
    quiet: Boolean = false,
    offline: Boolean = false,
    pinned: Boolean = false,
    onLongClick: (() -> Unit)? = null,
    actionsLabel: String? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val kind = stringResource(if (main) R.string.designsystem_main_worktree else R.string.designsystem_worktree)
    val primary = stringResource(R.string.designsystem_worktree_primary)
    val detachedText = stringResource(R.string.designsystem_worktree_detached)
    val offlineText = stringResource(R.string.designsystem_worktree_offline)
    val pinnedText = stringResource(R.string.designsystem_project_pinned)
    val foldState = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    val separator = stringResource(R.string.designsystem_separator)
    RepoTreeLine(
        level = level, onClick = onToggle, onLongClick = onLongClick, onLongClickLabel = actionsLabel, modifier = modifier,
        spoken = {
            contentDescription = listOfNotNull(kind, name, primary.takeIf { main }, branch ?: detachedText.takeIf { detached },
                offlineText.takeIf { offline }, pinnedText.takeIf { pinned }, tally).joinToString(separator)
            stateDescription = foldState
        },
        // Only the main checkout wears a mark; other worktrees stay text-only, matching the web sidebar. RepoTreeLine
        // reserves the mark column either way, so the names stay aligned.
        mark = { if (main) JIcon(PhIcons.House, null, size = TreeWorktreeMark, tint = palette.ink2) },
    ) {
        val detail = branch ?: detachedText.takeIf { detached }
        NameFirst(Modifier.weight(1f), chip = main, detail = detail != null) {
            Text(name, style = type.labelSmall, color = if (quiet || offline) palette.ink2 else palette.ink, maxLines = 1,
                overflow = TextOverflow.Ellipsis)
            if (main) JTag(primary)
            detail?.let {
                Text(it, style = type.caption, color = if (branch == null) palette.cautionText else palette.ink2, maxLines = 1,
                    overflow = TextOverflow.Ellipsis)
            }
        }
        tally?.let { Text(it, style = type.caption, color = palette.ink2, maxLines = 1) }
        if (pinned) JIcon(PersonaIcons.pinned, null, size = TreeWorktreeMark, tint = palette.ink2)
        FoldChevron(folded)
    }
}

/**
 * An idle repository's row in its computer's Idle Repositories (iOS `QuietSpaceRow` in By Repository's `quietSection`,
 * #4338 A8): its folder, its [title], under it how many [agents] and the first [tabs] they are in, when it last moved
 * ([time]) and the chevron that turns as it opens. A tap opens its agents under it ([onToggle]); a long press is the
 * actions its card's header had ([onLongClick], read as [actionsLabel]).
 */
@Composable
fun AgentIdleRepoRow(
    title: String,
    agents: Int,
    tabs: List<String>,
    time: String?,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    onLongClick: (() -> Unit)? = null,
    actionsLabel: String? = null,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val kind = stringResource(R.string.designsystem_repository)
    val foldState = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    val separator = stringResource(R.string.designsystem_separator)
    // "2 agents · review · research", as iOS joins them.
    val detail = (listOf(pluralStringResource(R.plurals.designsystem_tab_agents, agents, agents)) + tabs).joinToString(" · ")
    RepoTreeLine(
        level = 0, onClick = onToggle, onLongClick = onLongClick, onLongClickLabel = actionsLabel, modifier = modifier,
        spoken = {
            contentDescription = listOfNotNull(title, kind, detail, time?.takeIf { it.isNotEmpty() }).joinToString(separator)
            stateDescription = foldState
        },
        mark = { JIcon(PersonaIcons.folder, null, size = Tokens.Space.l, tint = palette.ink2) },
    ) {
        // A list row's room above and below its two lines, as iOS's.
        Column(Modifier.weight(1f).padding(vertical = Tokens.Space.xs)) {
            Text(title, style = type.headline, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(detail, style = type.micro, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        time?.takeIf { it.isNotEmpty() }?.let { Text(it, style = type.micro, color = palette.ink2, maxLines = 1) }
        FoldChevron(folded)
    }
}

/**
 * A project's fold row inside a By App section (#9199, iOS `AppProjectRow`): folder (or the agents mark for Other), its
 * [title] in the secondary weight and color, optional [machine] in caption/tertiary when the section mixes computers, an
 * idle-toned [count] capsule, and the fold chevron. Not a section heading: it sits under the app's header with no rule
 * above it; its sessions are one level in. [compact] uses the denser type (iOS `CompactList`).
 *
 * Leading matches iOS `listRowInsets(… rowInsets(level: 0) …)`: the folder at the list's level-0 leading (same inset as
 * the app section's FoldButton), so it sits left of the heading's app mark — deliberate on iOS, not a bug. Trailing
 * mirrors iOS (`title; machine; Spacer; count; chevron`): one weighted lead for title+machine, then count and chevron
 * with no extra Spacer (several `weight(1f, fill = false)` siblings would split the remaining width and leave the badge mid-row).
 */
@Composable
fun AgentAppProjectRow(
    title: String,
    count: Int,
    folded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
    other: Boolean = false,
    machine: String? = null,
    compact: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val foldState = stringResource(if (folded) R.string.designsystem_folded else R.string.designsystem_expanded)
    val separator = stringResource(R.string.designsystem_separator)
    val agentsLabel = pluralStringResource(R.plurals.designsystem_tab_agents, count, count)
    val markSize = if (compact) Tokens.Space.Standard.m else Tokens.Space.l
    val titleStyle = if (compact) type.caption.copy(fontWeight = FontWeight.SemiBold) else type.labelSmall
    val metaStyle = if (compact) type.micro else type.caption
    val tone = palette.status(Status.Idle)
    RepoTreeLine(
        level = 0, onClick = onToggle, modifier = modifier,
        spoken = {
            contentDescription = listOfNotNull(title, machine?.takeIf { it.isNotEmpty() }, foldState, agentsLabel)
                .joinToString(separator)
            stateDescription = foldState
        },
        mark = { JIcon(if (other) PersonaIcons.agents else PersonaIcons.folder, null, size = markSize, tint = palette.ink2) },
    ) {
        // One weight for the lead (iOS Spacer after title/machine); title shrinks first, machine capped.
        Row(
            Modifier.weight(1f),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            Text(
                title, style = titleStyle, color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f, fill = false),
            )
            machine?.takeIf { it.isNotEmpty() }?.let {
                Text(
                    it, style = metaStyle, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.widthIn(max = Tokens.Size.box120),
                )
            }
        }
        Text(
            "$count", style = type.micro.copy(fontWeight = FontWeight.SemiBold),
            color = tone.text, maxLines = 1,
            modifier = Modifier.clip(CircleShape).background(tone.tint)
                .padding(horizontal = SectionHeaderTokens.countPadding, vertical = SectionHeaderTokens.countPaddingVertical),
        )
        FoldChevron(folded)
    }
}

/**
 * A working tree's idle agents behind one quiet line (iOS `RepoIdleLine`, #4338 A8), at their rows' [level]: folded,
 * "2 idle · review · research" ([count] and the first [names]) and a chevron, and a tap shows them ([onClick]); once
 * [open], "Hide 2 Idle" after them, and a tap folds them again.
 */
@Composable
fun AgentRepoIdleLine(
    count: Int,
    names: List<String>,
    open: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    level: Int = 1,
) {
    val palette = PersonaTheme.palette
    val words = if (open) pluralStringResource(R.plurals.designsystem_idle_hide, count, count)
        else (listOf(pluralStringResource(R.plurals.designsystem_idle_count, count, count)) + names).joinToString(" · ")
    val spoken = if (open) words else pluralStringResource(R.plurals.designsystem_idle_show, count, count)
    RepoTreeLine(
        level = level, onClick = onClick, modifier = modifier, spoken = { contentDescription = spoken },
        mark = { JIcon(if (open) PersonaIcons.collapse else PersonaIcons.idleFold, null, size = TreeWorktreeMark, tint = palette.ink3) },
    ) {
        Text(words, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f))
        if (!open) JIcon(PersonaIcons.disclosure, null, size = TreeChevron, tint = palette.ink3)
    }
}

/**
 * A line's name, then its [chip] and [detail] after it (the children in that order): the name has the room first, as
 * iOS gives it the layout priority, the chip keeps its width and the detail what is left, at least a few letters of it.
 */
@Composable
private fun NameFirst(modifier: Modifier, chip: Boolean, detail: Boolean, content: @Composable () -> Unit) {
    val gap = Tokens.Space.s
    val least = Tokens.Space.xxl + Tokens.Space.l
    Layout(content, modifier) { measurables, constraints ->
        val space = gap.roundToPx()
        val free = constraints.copy(minWidth = 0)
        val chipPlaced = if (chip) measurables[1].measure(free) else null
        val detailMeasurable = if (detail) measurables.last() else null
        val chipWidth = chipPlaced?.let { it.width + space } ?: 0
        val kept = detailMeasurable?.let { minOf(it.maxIntrinsicWidth(constraints.maxHeight), least.roundToPx()) + space } ?: 0
        val name = measurables[0].measure(free.copy(maxWidth = (constraints.maxWidth - chipWidth - kept).coerceAtLeast(0)))
        val detailPlaced = detailMeasurable?.measure(free.copy(maxWidth = (constraints.maxWidth - name.width - chipWidth - space).coerceAtLeast(0)))
        val height = maxOf(name.height, chipPlaced?.height ?: 0, detailPlaced?.height ?: 0)
        layout(constraints.maxWidth, height) {
            var x = 0
            for (placeable in listOfNotNull(name, chipPlaced, detailPlaced)) {
                placeable.placeRelative(x, (height - placeable.height) / 2)
                x += placeable.width + space
            }
        }
    }
}
