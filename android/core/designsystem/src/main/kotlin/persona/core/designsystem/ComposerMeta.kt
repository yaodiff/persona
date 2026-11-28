package persona.core.designsystem

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.FlowRowScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.selected
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/**
 * Compact wrapping strip for secondary composer metadata and overrides (default launcher/model pick,
 * permission and thinking-effort hint) above the input field. Keeps visual weight clearly below the
 * primary composer input frame while preserving wrap behavior at narrow widths and large font scales.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ComposerMetaStrip(
    modifier: Modifier = Modifier,
    content: @Composable FlowRowScope.() -> Unit,
) {
    FlowRow(
        modifier = modifier,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        itemVerticalAlignment = Alignment.CenterVertically,
        content = content,
    )
}

/**
 * A lightweight inline metadata item or picker trigger inside [ComposerMetaStrip].
 *
 * At rest ([selected] = false), draws frameless text + icon directly on the composer surface so it never
 * competes with the primary input field or forms a stacked brick row. When [selected] is true (an active
 * per-conversation override such as a picked launcher/model), adopts a soft accent pill so the override
 * stays immediately scannable across Glass, Brief, and Board in both light and dark themes.
 *
 * Interactive instances ([onClick] != null) clip only drawing ([clipDrawing]) so Compose preserves Android's
 * 48dp minimum touch target without inflating vertical chrome height.
 */
@Composable
fun ComposerMetaChip(
    text: String,
    icon: PhIcon,
    modifier: Modifier = Modifier,
    selected: Boolean = false,
    dropdown: Boolean = false,
    onClick: (() -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.style
    val shape = if (style == AppStyle.Board) RoundedCornerShape(PersonaTheme.shapes.chip) else CircleShape
    val fill = if (selected) palette.accentSoft else null
    val borderColor = if (selected && style == AppStyle.Board) palette.accent.copy(alpha = 0.5f) else null
    val textColor = when {
        selected && style == AppStyle.Brief -> palette.ink
        selected -> palette.accent
        else -> palette.ink2
    }
    val iconTint = when {
        selected && style == AppStyle.Brief -> palette.ink
        selected -> palette.accent
        else -> palette.ink3
    }
    val textStyle = PersonaTheme.type.caption.copy(
        fontWeight = if (selected) FontWeight.SemiBold else FontWeight.Medium,
    )
    Row(
        modifier
            .heightIn(min = Tokens.Size.wellXS)
            .clipDrawing(shape)
            .then(if (fill != null) Modifier.background(fill) else Modifier)
            .then(if (borderColor != null) Modifier.border(Tokens.Size.hairline, borderColor, shape) else Modifier)
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClick = onClick) else Modifier)
            .semantics { this.selected = selected }
            .padding(
                horizontal = if (selected) Tokens.Space.s else Tokens.Space.sm,
                vertical = Tokens.Space.xxs,
            ),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        JIcon(icon, null, size = Tokens.Size.markS, tint = iconTint)
        Text(
            text = text,
            modifier = Modifier.weight(1f, fill = false),
            style = textStyle,
            color = textColor,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        if (dropdown) {
            JIcon(PhIcons.CaretDown, null, size = Tokens.Size.markXS, tint = iconTint)
        }
    }
}
