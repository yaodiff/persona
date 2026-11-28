package persona.core.designsystem

import androidx.compose.animation.core.AnimationSpec
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsPropertyKey
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import persona.core.designsystem.icons.PhIcons

/** The composer trays are bounded independently of the transcript and the keyboard. */
object AssistantTrayTokens {
    val maximumHeight = 330.dp
    const val maximumHeightFraction = 0.45f
    val markSize = JChipTokens.dotSize
    /** Compact visual height of a Brief or Board tray entry; touch targets still expand to Android's 48dp floor. */
    val entryMinimumHeight = Tokens.Size.wellXS
    const val expandedRotation = 180f
}

/** Drawn leading/trailing edge-fade strengths (0 = none, 1 = full) for [AssistantTrayBar], exposed to UI tests. */
internal val AssistantTrayFade = SemanticsPropertyKey<AgentFilterFades>("AssistantTrayFade")

/** An entry's mark: [Pending] waits on the user, [Progress] is work under way, [Finished] the tasks that finished in the
 *  last 24 hours (#10922), in the done green of a finished row's check; [Agents] is the old Agents entry's. */
enum class AssistantTrayTone { Pending, Progress, Agents, Finished }

@Composable
fun assistantTrayColor(tone: AssistantTrayTone): Color = when (tone) {
    AssistantTrayTone.Pending -> PersonaTheme.palette.needsYou.dot
    AssistantTrayTone.Progress -> PersonaTheme.palette.working.dot
    AssistantTrayTone.Agents, AssistantTrayTone.Finished -> PersonaTheme.palette.positive
}

/**
 * Horizontally scrolling strip for [AssistantTrayEntry] controls above the composer.
 *
 * When entries fit on screen, no edge fade is drawn; when entries overflow (e.g., four trays or large system font
 * scales), the overflowing leading/trailing edges fade softly into the page background instead of hard-clipping.
 */
@Composable
fun AssistantTrayBar(
    modifier: Modifier = Modifier,
    content: @Composable RowScope.() -> Unit,
) {
    val palette = PersonaTheme.palette
    val scroll = rememberScrollState()
    val textScale = LocalDensity.current.fontScale.coerceAtLeast(1f)
    val fadeWidth = AgentFilterTokens.fade * textScale
    val fadeSpec: AnimationSpec<Float> = if (PersonaTheme.reduceMotion) SharedMotion.instant() else SharedMotion.reveal()
    val measured = scroll.viewportSize > 0
    val leading = animateFloatAsState(if (measured && scroll.canScrollBackward) 1f else 0f, fadeSpec, label = "tray-fade-leading")
    val trailing = animateFloatAsState(if (measured && scroll.canScrollForward) 1f else 0f, fadeSpec, label = "tray-fade-trailing")
    val fading by remember { derivedStateOf { leading.value > 0f || trailing.value > 0f } }
    val fadeModifier = if (fading) {
        Modifier.agentFilterEdgeFade(palette.background, fadeWidth, { leading.value }, { trailing.value })
    } else {
        Modifier
    }
    val gap = when (PersonaTheme.style) {
        AppStyle.Glass -> Tokens.Space.s
        AppStyle.Brief -> Tokens.Space.xs
        AppStyle.Board -> Tokens.Space.sm
    }
    Row(
        modifier
            .fillMaxWidth()
            .semantics { this[AssistantTrayFade] = AgentFilterFades(leading.value, trailing.value) }
            .then(fadeModifier)
            .horizontalScroll(scroll),
        horizontalArrangement = Arrangement.spacedBy(gap),
        verticalAlignment = Alignment.CenterVertically,
        content = content,
    )
}

/**
 * One entry above the composer:
 * - On Glass, iOS's compact `DockCapsule` (capsule over the page background, no leading dot or trailing caret,
 *   active [AssistantTrayTone.Pending] tinted in Needs You's tone, neutral chip fill otherwise, accent fill while
 *   [expanded], tabular digits, and a 48dp touch target).
 * - On Brief, a compact frameless editorial control at rest with a small status dot, tabular count, and quiet
 *   disclosure caret; active [AssistantTrayTone.Pending] uses Brief's yellow highlighter capsule and [expanded]
 *   uses the solid action capsule.
 * - On Board, a compact borderless recessed tag ([PersonaPalette.sunken]) at rest so it sits visually subordinate
 *   to the framed composer input box below; active [AssistantTrayTone.Pending] adopts Needs You's tinted chip and
 *   [expanded] highlights with the accent border and soft fill.
 *
 * An empty Pending stays quiet and can still open its empty state.
 */
@Composable
fun AssistantTrayEntry(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    CompactTouchLayout {
        when (PersonaTheme.style) {
            AppStyle.Glass -> GlassDockCapsule(label, count, tone, expanded, description, onClick, modifier, enabled)
            AppStyle.Brief -> BriefTrayEntry(label, count, tone, expanded, description, onClick, modifier, enabled)
            AppStyle.Board -> BoardTrayEntry(label, count, tone, expanded, description, onClick, modifier, enabled)
        }
    }
}

@Composable
private fun GlassDockCapsule(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier, enabled: Boolean) {
    val palette = PersonaTheme.palette
    val waiting = tone == AssistantTrayTone.Pending && count > 0
    val fill = when {
        expanded -> palette.accent
        waiting -> palette.needsYou.tint
        palette.dark -> Tokens.Common.Dark.Fill.chip
        else -> Tokens.Common.Light.Fill.chip
    }
    val words = when {
        expanded -> palette.onAccent
        waiting -> palette.needsYou.text
        else -> palette.ink2
    }
    val type = PersonaTheme.type.caption.copy(fontWeight = if (expanded || waiting) FontWeight.SemiBold else FontWeight.Medium)
    val countText = count.takeIf { it > 0 }?.let(::countBadgeText)
    Row(
        modifier
            .then(if (enabled) Modifier.minimumInteractiveComponentSize() else Modifier)
            .clip(CircleShape)
            // Page background under the translucent fill keeps scrolled chat text from bleeding through (#3696).
            .background(palette.background)
            .background(fill)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .semantics { stateDescription = description }
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.sm),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(label, style = type, color = words, maxLines = 1, softWrap = false)
        countText?.let {
            Text(it, style = type.copy(fontFeatureSettings = TabularFigures), color = words, maxLines = 1, softWrap = false)
        }
    }
}

@Composable
private fun BriefTrayEntry(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier, enabled: Boolean) {
    val palette = PersonaTheme.palette
    val waiting = tone == AssistantTrayTone.Pending && count > 0
    val quietEmpty = tone == AssistantTrayTone.Pending && count == 0
    val fill = when {
        expanded -> palette.action
        waiting -> palette.needsYou.tint
        else -> null
    }
    val ink = when {
        expanded -> palette.onAction
        waiting -> palette.needsYou.text
        quietEmpty -> palette.ink3
        else -> palette.ink2
    }
    val dotColor = when {
        expanded -> palette.onAction
        waiting -> if (palette.dark) palette.needsYou.dot else palette.needsYou.text
        quietEmpty -> palette.idle.dot
        else -> assistantTrayColor(tone)
    }
    val caretTint = if (expanded) palette.onAction else ink.copy(alpha = 0.72f)
    val labelStyle = PersonaTheme.type.caption.copy(fontWeight = if (expanded || waiting) FontWeight.SemiBold else FontWeight.Medium)
    val countStyle = PersonaTheme.type.caption.copy(
        fontWeight = if (expanded || waiting) FontWeight.SemiBold else FontWeight.Normal,
        fontFeatureSettings = TabularFigures,
    )
    val countText = count.takeIf { it > 0 }?.let(::countBadgeText)
    Row(
        modifier
            .heightIn(min = AssistantTrayTokens.entryMinimumHeight)
            .clipDrawing(CircleShape)
            .then(if (fill != null) Modifier.background(fill) else Modifier)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .semantics { stateDescription = description }
            .padding(
                horizontal = if (fill != null) Tokens.Space.s else Tokens.Space.sm,
                vertical = Tokens.Space.xs,
            ),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(Tokens.Size.dotS).clip(CircleShape).background(dotColor))
        Text(label, style = labelStyle, color = ink, maxLines = 1, softWrap = false)
        countText?.let {
            Text(it, style = countStyle, color = ink, maxLines = 1, softWrap = false)
        }
        JIcon(if (expanded) PhIcons.CaretDown else PhIcons.CaretUp, null, size = Tokens.Size.markXS, tint = caretTint)
    }
}

@Composable
private fun BoardTrayEntry(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier, enabled: Boolean) {
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(PersonaTheme.shapes.chip)
    val waiting = tone == AssistantTrayTone.Pending && count > 0
    val quietEmpty = tone == AssistantTrayTone.Pending && count == 0
    val fill = when {
        expanded -> palette.accentSoft
        waiting -> palette.needsYou.tint
        quietEmpty -> palette.sunken.copy(alpha = 0.6f)
        else -> palette.sunken
    }
    val borderSpec = when {
        expanded -> Tokens.Size.line to palette.accent
        waiting -> Tokens.Size.hairline to palette.needsYou.dot.copy(alpha = 0.45f)
        else -> null
    }
    val ink = when {
        expanded -> palette.ink
        waiting -> palette.needsYou.text
        quietEmpty -> palette.ink3
        else -> palette.ink2
    }
    val countInk = when {
        expanded -> palette.accent
        waiting -> palette.needsYou.text
        else -> palette.ink3
    }
    val dotColor = if (quietEmpty) palette.idle.dot else assistantTrayColor(tone)
    val caretTint = if (expanded) palette.accent else ink.copy(alpha = 0.72f)
    val labelStyle = PersonaTheme.type.caption.copy(fontWeight = if (expanded || waiting) FontWeight.SemiBold else FontWeight.Medium)
    val countStyle = PersonaTheme.type.caption.copy(
        fontWeight = if (expanded || waiting) FontWeight.SemiBold else FontWeight.Normal,
        fontFeatureSettings = TabularFigures,
    )
    val countText = count.takeIf { it > 0 }?.let(::countBadgeText)
    Row(
        modifier
            .heightIn(min = AssistantTrayTokens.entryMinimumHeight)
            .clipDrawing(shape)
            .background(fill)
            .then(if (borderSpec != null) Modifier.border(borderSpec.first, borderSpec.second, shape) else Modifier)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .semantics { stateDescription = description }
            .padding(horizontal = Tokens.Space.sm, vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(Tokens.Size.dotS).clip(CircleShape).background(dotColor))
        Text(label, style = labelStyle, color = ink, maxLines = 1, softWrap = false)
        countText?.let {
            Text(it, style = countStyle, color = countInk, maxLines = 1, softWrap = false)
        }
        JIcon(if (expanded) PhIcons.CaretDown else PhIcons.CaretUp, null, size = Tokens.Size.markXS, tint = caretTint)
    }
}

/** Row status marks share the same deliberately named palette as the tray controls. */
@Composable
fun AssistantTrayMark(tone: AssistantTrayTone?, modifier: Modifier = Modifier) {
    Box(modifier.size(AssistantTrayTokens.markSize).clip(CircleShape)
        .background(tone?.let { assistantTrayColor(it) } ?: PersonaTheme.palette.ink3))
}

/**
 * Smooth horizontal overflow fade for the tray entry bar, using the same edge mask as [JAgentFilterCapsule].
 * Draws nothing when all entries fit inside the available width.
 */
@Composable
fun Modifier.assistantTrayEdgeFade(scrollState: ScrollState): Modifier {
    val palette = PersonaTheme.palette
    val reduceMotion = PersonaTheme.reduceMotion
    val spec: AnimationSpec<Float> = if (reduceMotion) SharedMotion.instant() else SharedMotion.reveal()
    val leading = animateFloatAsState(if (scrollState.canScrollBackward) 1f else 0f, spec, label = "tray-bar-fade-leading")
    val trailing = animateFloatAsState(if (scrollState.canScrollForward) 1f else 0f, spec, label = "tray-bar-fade-trailing")
    val fade = AgentFilterTokens.fade * LocalDensity.current.fontScale.coerceAtLeast(1f)
    return agentFilterEdgeFade(palette.background, fade, { leading.value }, { trailing.value })
}
