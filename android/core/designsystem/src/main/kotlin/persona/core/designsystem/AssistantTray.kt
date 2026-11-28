package persona.core.designsystem

import androidx.compose.animation.core.AnimationSpec
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.material3.minimumInteractiveComponentSize
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.Role
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
    /** An entry is a tappable filter chip on Brief and Board, so it is as tall as one. */
    val entryMinimumHeight = JChipTokens.height
    const val expandedRotation = 180f
}

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
 * One entry above the composer: on Glass, iOS's compact `DockCapsule` (capsule over the page background, no leading dot
 * or trailing caret, active [AssistantTrayTone.Pending] tinted in Needs You's tone, neutral chip fill otherwise, accent
 * fill while [expanded], tabular digits, and a 48dp touch target); on Brief and Board, the style's filter chip ([JChip])
 * with its tone mark and caret. An empty Pending stays quiet and can still open its empty state.
 */
@Composable
fun AssistantTrayEntry(label: String, count: Int, tone: AssistantTrayTone, expanded: Boolean,
    description: String, onClick: () -> Unit, modifier: Modifier = Modifier, enabled: Boolean = true) {
    CompactTouchLayout {
        if (PersonaTheme.style == AppStyle.Glass) {
            GlassDockCapsule(label, count, tone, expanded, description, onClick, modifier, enabled)
            return@CompactTouchLayout
        }
        val active = tone != AssistantTrayTone.Pending || count > 0
        JChip(label, modifier.semantics { stateDescription = description }, selected = expanded,
            onClick = if (enabled) onClick else null, count = count.takeIf { it > 0 }, muted = !active,
            dot = if (active) assistantTrayColor(tone) else PersonaTheme.palette.ink3, role = Role.Button,
            trailingIcon = if (expanded) PhIcons.CaretDown else PhIcons.CaretUp)
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
    val countText = when {
        count > 99 -> "99+"
        count > 0 -> "$count"
        else -> null
    }
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
