package persona.core.designsystem

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalAccessibilityManager
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import kotlinx.coroutines.delay
import persona.core.designsystem.icons.PersonaIcons

/**
 * What a toast says about the thing just done (iOS `Toast.Style`): it chooses the icon, its colour, how long the toast stays
 * and the haptic it plays when none is named.
 */
enum class FeedbackStyle {
    Info, Success, Error;

    /** iOS plays success and error toasts' haptics; an info toast is quiet. */
    val haptic: Haptic? get() = when (this) { Info -> null; Success -> Haptic.Success; Error -> Haptic.Error }
}

/**
 * The one toast slot of a window (iOS `AppModel.toast`): the newest notice replaces the one showing, with no queue. The
 * host draws it ([JToastHost]); [show] plays its haptic.
 */
@Stable
class JToastState {
    var current: FeedbackNotice? by mutableStateOf(null)
        private set
    /** Bumps for every notice, so the same words shown again restart their time. */
    var serial by mutableStateOf(0)
        private set

    fun show(notice: FeedbackNotice) {
        current = notice
        serial++
    }

    fun dismiss(notice: FeedbackNotice) {
        if (current === notice) current = null
    }
}

@Composable
fun rememberToastState(): JToastState = remember { JToastState() }

/** How long a toast stays before the accessibility timeout stretches it (iOS `ToastCapsule.shown`): 5 s with a button, 4 s for an error, else 2.5 s. */
internal fun toastMillis(notice: FeedbackNotice): Long = when {
    notice.action != null -> 5_000
    notice.style == FeedbackStyle.Error -> 4_000
    else -> 2_500
}

/**
 * Top clearance below the status bar so window- and page-level toasts sit cleanly beneath
 * floating top bars ([ChatTopBar], [HomeNavigation], [PageBar]), matching iOS `ShellNavigationClearance`.
 */
val ToastTopBarClearance: Dp get() = Tokens.Space.xxl + Tokens.Space.xxl + Tokens.Space.m

/**
 * The window's toast at the top of what it covers (iOS `ToastOverlay`): a capsule with the style's icon, the words and up
 * to two buttons, sliding in from the top over the content (it moves nothing). Only its buttons take touches; the rest of
 * the window keeps them. TalkBack announces it, and the system's accessibility timeout lengthens its stay.
 */
@Composable
fun JToastHost(state: JToastState, modifier: Modifier = Modifier) {
    val notice = state.current
    val haptics = LocalHapticFeedback.current
    val accessibility = LocalAccessibilityManager.current
    LaunchedEffect(notice, state.serial) {
        notice ?: return@LaunchedEffect
        (notice.haptic ?: notice.style.haptic)?.let { haptics.play(it) }
        val base = toastMillis(notice)
        delay(accessibility?.calculateRecommendedTimeoutMillis(base, containsIcons = true, containsText = true,
            containsControls = notice.action != null) ?: base)
        state.dismiss(notice)
    }
    val motion = PersonaTheme.motion
    // With reduced motion it is there, or gone, at once.
    val still = PersonaTheme.reduceMotion
    Box(modifier.fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
        AnimatedVisibility(
            visible = notice != null,
            enter = if (still) EnterTransition.None else slideInVertically(motion.enter()) { -it } + fadeIn(motion.enter()),
            exit = if (still) ExitTransition.None else slideOutVertically(motion.enter()) { -it } + fadeOut(motion.enter()),
        ) {
            // The last notice stays drawn while it slides away.
            val shown = remember(notice) { notice } ?: return@AnimatedVisibility
            JToast(shown, dismiss = { state.dismiss(shown) })
        }
    }
}

/** One toast: the style's icon and the words on a floating capsule, then its buttons. */
@Composable
fun JToast(notice: FeedbackNotice, dismiss: () -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val (icon, tint) = when (notice.style) {
        FeedbackStyle.Error -> PersonaIcons.errorFilled to palette.danger.dot
        FeedbackStyle.Success -> PersonaIcons.successFilled to palette.positive
        FeedbackStyle.Info -> PersonaIcons.infoFilled to palette.accent
    }
    Row(
        modifier.padding(horizontal = Tokens.Space.xl).testTag("toast")
            .floatingSurface(RoundedCornerShape(percent = 50))
            .semantics(mergeDescendants = notice.action == null) { liveRegion = LiveRegionMode.Polite }
            .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        JIcon(icon, null, size = Tokens.Size.iconXS, tint = tint, selected = true, decorative = true)
        Text(notice.message, Modifier.weight(1f, fill = false), style = PersonaTheme.type.labelSmall, color = palette.ink,
            maxLines = 3, overflow = TextOverflow.Ellipsis)
        // Its buttons as iOS draws them: the accent's words, semibold. They keep the capsule's height; Compose stretches
        // their touch area to 48dp (TouchTargetTest).
        for (action in listOfNotNull(notice.secondary, notice.action)) {
            Text(action.label, Modifier.clip(RoundedCornerShape(percent = 50)).clickable(role = Role.Button) { dismiss(); action.run() }
                .padding(horizontal = Tokens.Space.xs), style = PersonaTheme.type.labelSmall.copy(fontWeight = FontWeight.SemiBold),
                color = palette.accent, maxLines = 1)
        }
    }
}
