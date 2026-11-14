package persona.core.designsystem

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import persona.core.designsystem.icons.PhIcons

internal fun sidebarActionFill(palette: PersonaPalette): Color =
    if (palette.dark && palette.accent == Palettes.GlassDark.accent) Palettes.GlassLight.accent else palette.accent

@Composable
fun JSidebarNewChat(text: String, onClick: () -> Unit, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val compact = LocalHomeDrawer.current && PersonaTheme.style == AppStyle.Glass
    val fill = if (compact) sidebarActionFill(palette) else palette.accent
    CompositionLocalProvider(LocalPalette provides if (compact) palette.copy(action = fill, onAction = palette.onAccentFill(fill)) else palette) {
        JButton(text, onClick, modifier, icon = PhIcons.NotePencil)
    }
}

internal fun sidebarHistoryRowSpins(status: Status, working: Boolean): Boolean =
    status == Status.Working || (working && status != Status.NeedsYou && status != Status.Unread)

/** Compact iPhone history is one title and a state mark, not a second conversation preview. */
@Composable
fun JSidebarHistoryRow(
    title: String,
    status: Status,
    selected: Boolean,
    onClick: () -> Unit,
    onMenu: () -> Unit,
    menuLabel: String,
    /** The line under the title (a leader conversation's open tasks, #4331); null: one line only. */
    hint: String? = null,
    /** A marked line under the title (the sentence a search found). Drawn instead of [hint] when set. */
    hintMark: AnnotatedString? = null,
    /** The title's quiet start ([JRow]'s `titleLead`): a hub title's day and kind. */
    titleLead: String? = null,
    titleAccessory: (@Composable () -> Unit)? = null,
    /** Drawn before the ⋯ (a 常驻 lead's caret that folds its to-dos open, #4352); null: nothing. */
    trailingAccessory: (@Composable () -> Unit)? = null,
    /** A lead with anything at work under it while its own conversation is idle spins the working mark anyway (#6230, #6626). */
    working: Boolean = false,
) {
    JRow(title, onClick = onClick, onLongClick = onMenu, selected = selected, chevron = false,
        subtitle = if (hintMark == null) hint else null, subtitleMark = hintMark,
        titleAccessory = titleAccessory, titleLead = titleLead, trailing = {
            // A Needs You dot is still, as iOS's `RowStateMark` in a dense list; a lead with work under it spins (#6230), as there.
            if (status == Status.NeedsYou || status == Status.Working || status == Status.Unread || working) JStatusDot(status, spinning = sidebarHistoryRowSpins(status, working), breathes = false)
            trailingAccessory?.invoke()
            JIconButton(PhIcons.DotsThree, menuLabel, onMenu)
        })
}
