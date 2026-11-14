package persona.core.designsystem

import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.animateDpAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.animateScrollBy
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collectLatest
import persona.core.designsystem.icons.PersonaIcons

val AgentFilterExpanded = SemanticsPropertyKey<Boolean>("AgentFilterExpanded")

/**
 * Final E: only status choices scroll; grouping has one fixed, anchored menu at the trailing edge.
 * Labels reveal with animated width and a crossfade; equal-width CJK ellipses still abbreviate exactly.
 * Counts never take part in truncation.
 * Focus is resolved after scrolling settles, so changing a label's width cannot feed back into focus.
 */
@Composable
fun JAgentFilterCapsule(
    bays: List<Bay>, selected: Int, onSelect: (Int) -> Unit,
    groupLabel: String, groupActions: List<JAction>, modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val surface = if (PersonaTheme.style == AppStyle.Glass && !palette.dark) Tokens.Semantic.Glass.Light.pageBackground else palette.surfaceOpaque
    val visualHeight = AgentFilterTokens.height * LocalDensity.current.fontScale.coerceAtLeast(1f)
    val state = rememberLazyListState()
    var focused by remember { mutableIntStateOf(-1) }
    var menu by remember { mutableStateOf(false) }
    val reduceMotion = PersonaTheme.reduceMotion
    val duration = if (reduceMotion) Tokens.Motion.reduced else Tokens.Motion.State.duration
    // Only scroll start/end is observed, never animated item geometry on every frame.
    LaunchedEffect(state) {
        snapshotFlow { state.isScrollInProgress }.collectLatest { moving ->
            if (!moving) {
                delay(AgentFilterTokens.focusDelayMillis)
                focused = if (!state.canScrollBackward) -1 else {
                    val layout = state.layoutInfo
                    val center = layout.viewportStartOffset +
                        (layout.viewportEndOffset - layout.viewportStartOffset) * AgentFilterTokens.focusFraction
                    layout.visibleItemsInfo.minByOrNull { kotlin.math.abs(it.offset + it.size / 2f - center) }?.index ?: -1
                }
            }
        }
    }
    var previousSelection by remember { mutableIntStateOf(selected) }
    LaunchedEffect(selected) {
        if (previousSelection == selected) return@LaunchedEffect
        previousSelection = selected
        // Let the selected label expand before centering its final geometry.
        delay(duration.toLong())
        withFrameNanos { }
        val item = state.layoutInfo.visibleItemsInfo.firstOrNull { it.index == selected }
        if (item == null) state.scrollToItem(selected)
        val visible = state.layoutInfo.visibleItemsInfo.firstOrNull { it.index == selected }
        if (visible != null) {
            val layout = state.layoutInfo
            val delta = visible.offset + visible.size / 2f -
                (layout.viewportStartOffset + layout.viewportEndOffset) / 2f
            if (reduceMotion) state.scrollBy(delta) else state.animateScrollBy(delta, tween(duration))
        }
    }
    Box(modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)) {
        Box(Modifier.align(Alignment.Center).fillMaxWidth().height(visualHeight)
            .clip(CircleShape).background(surface)
            .border(AgentFilterTokens.stroke, palette.line, CircleShape))
        Row(Modifier.align(Alignment.Center).fillMaxWidth().heightIn(min = Tokens.MinTouch.android).clip(CircleShape),
            verticalAlignment = Alignment.CenterVertically) {
            LazyRow(
                state = state,
                modifier = Modifier.weight(1f).testTag("agents-status-scroll")
                    .graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
                    .drawWithContent {
                        drawContent()
                        val start = (AgentFilterTokens.chipPadding.toPx() / size.width).coerceIn(0f, 0.5f)
                        val end = (1f - AgentFilterTokens.fadeEnd.toPx() / size.width).coerceIn(start, 1f)
                        drawRect(Brush.horizontalGradient(0f to surface.copy(alpha = 0f), start to surface.copy(alpha = 1f),
                            end to surface.copy(alpha = 1f), 1f to surface.copy(alpha = 0f)), blendMode = BlendMode.DstIn)
                    },
                contentPadding = PaddingValues(horizontal = AgentFilterTokens.inset),
                horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.gap),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                items(bays.size, key = { bays[it].label }) { index ->
                    val bay = if (index == 0 && PersonaTheme.style == AppStyle.Glass) bays[index].copy(count = null) else bays[index]
                    CapsuleChoice(bay, selected == index, selected == index || focused == index,
                        duration, Modifier.testTag("agents-status-$index")) { onSelect(index) }
                }
            }
            Box(Modifier.width(AgentFilterTokens.stroke).height(AgentFilterTokens.separatorHeight).background(palette.line))
            Box(Modifier.width(AgentFilterTokens.groupWidth).heightIn(min = Tokens.MinTouch.android)
                .testTag("agents-grouping").clickable(role = Role.Button) { menu = true }
                .semantics { contentDescription = groupLabel }, contentAlignment = Alignment.Center) {
                JIcon(PersonaIcons.filter, null, size = AgentFilterTokens.icon, tint = palette.ink, decorative = true)
                JMenu(menu, { menu = false }, groupActions)
            }
        }
    }
}

@Composable
private fun CapsuleChoice(
    bay: Bay, selected: Boolean, expanded: Boolean, duration: Int, modifier: Modifier,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type.callout.copy(fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium)
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    val compact = remember(bay.label) { collapsedFilterLabel(bay.label) }
    val fullWidth = remember(bay.label, type, density) { measurer.measure(bay.label, type).size.width }
    val compactWidth = remember(compact, type, density) { measurer.measure(compact, type).size.width }
    val width by animateDpAsState(with(density) { (if (expanded) fullWidth else compactWidth).toDp() }, tween(duration), label = "filter-label-width")
    Row(modifier.heightIn(min = Tokens.MinTouch.android).widthIn(min = Tokens.MinTouch.android).clip(CircleShape)
        .clickable(role = Role.Tab, onClick = onClick)
        .semantics(mergeDescendants = true) {
            this.selected = selected
            this[AgentFilterExpanded] = expanded
            contentDescription = listOfNotNull(bay.label, bay.count?.toString()).joinToString(" ")
        }
        .padding(vertical = Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        Row(Modifier.heightIn(min = AgentFilterTokens.chipHeight).clip(CircleShape)
            .background(if (selected) palette.accent.copy(alpha = AgentFilterTokens.selectedFillAlpha) else palette.surfaceOpaque.copy(alpha = 0f))
            .padding(horizontal = AgentFilterTokens.chipPadding), verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(AgentFilterTokens.contentGap)) {
            bay.icon?.let { JIcon(it, null, size = AgentFilterTokens.dot, tint = palette.status(bay.status ?: Status.Working, bay.waiting).dot, decorative = true) }
                ?: bay.status?.let {
                    Box(Modifier.size(AgentFilterTokens.dot).clip(CircleShape).background(palette.status(it, bay.waiting).dot))
                }
            Crossfade(expanded, modifier = Modifier.width(width).clearAndSetSemantics { }, animationSpec = tween(duration), label = "filter-label") { reveal ->
                Text(if (reveal) bay.label else compact, style = type,
                    color = if (selected) palette.accent else palette.ink, maxLines = 1, softWrap = false, overflow = TextOverflow.Ellipsis)
            }
            bay.count?.let { Text("$it", style = type, color = if (selected) palette.accent else palette.ink2,
                maxLines = 1, softWrap = false, modifier = Modifier.clearAndSetSemantics { }) }
        }
    }
}

/** Count Unicode code points, not UTF-16 halves, when making the compact two-character label. */
internal fun collapsedFilterLabel(label: String): String {
    val count = label.codePointCount(0, label.length)
    return if (count <= AgentFilterTokens.collapsedCharacters) label else
        label.substring(0, label.offsetByCodePoints(0, AgentFilterTokens.collapsedCharacters)) + "…"
}
