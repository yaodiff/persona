package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.MessageItemTokens
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcon
import persona.core.domain.inbox.InboxInteraction
import persona.core.inbox.*

internal data class InboxSourceOption(
    val id: String,
    val label: String,
    val sourceType: String = "",
)

/**
 * Horizontal source and kind filter pills under the Glass Inbox section capsule (matching iOS `InboxView.plainFilters`).
 * Uses [Role.Button] rather than selection semantics so section-bar tab queries remain unambiguous.
 */
@Composable
internal fun GlassInboxFilterStrip(
    sources: List<InboxSourceOption>,
    kinds: List<Pair<String, String>>,
    activeSource: String,
    activeKind: String,
    onSelectSource: (String) -> Unit,
    onSelectKind: (String) -> Unit,
    onClearAll: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val allActive = activeSource.isEmpty() && activeKind.isEmpty()
    Row(
        modifier
            .fillMaxWidth()
            .horizontalScroll(rememberScrollState())
            .padding(vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        GlassInboxFilterPill(
            label = stringResource(R.string.inbox_bay_all),
            active = allActive,
            icon = null,
            onClick = onClearAll,
        )
        sources.forEach { option ->
            val icon = when (option.sourceType) {
                "lark" -> PersonaIcons.feishuChats
                "mail" -> PersonaIcons.mail
                else -> null
            }
            GlassInboxFilterPill(
                label = option.label,
                active = activeSource == option.id,
                icon = icon,
                onClick = { onSelectSource(if (activeSource == option.id) "" else option.id) },
            )
        }
        if (sources.isNotEmpty()) {
            Box(
                Modifier
                    .width(Tokens.Size.line)
                    .height(Tokens.Size.badge)
                    .background(palette.line),
            )
        }
        kinds.forEach { (kindId, label) ->
            GlassInboxFilterPill(
                label = label,
                active = activeKind == kindId,
                icon = null,
                onClick = { onSelectKind(if (activeKind == kindId) "" else kindId) },
            )
        }
    }
}

@Composable
private fun GlassInboxFilterPill(
    label: String,
    active: Boolean,
    icon: PhIcon?,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val fill = if (active) palette.accent else palette.sunken
    val ink = if (active) palette.needsYou.onDot else palette.ink
    Row(
        Modifier
            .clip(CircleShape)
            .background(fill)
            .clickable(role = Role.Button, onClick = onClick)
            .padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.sm),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
    ) {
        icon?.let {
            JIcon(it, null, size = Tokens.Size.markS, tint = ink, decorative = true)
        }
        Text(
            label,
            style = PersonaTheme.type.caption.copy(fontWeight = if (active) FontWeight.SemiBold else FontWeight.Normal),
            color = ink,
            maxLines = 1,
            softWrap = false,
        )
    }
}

internal fun inboxInteractionIcon(kind: String): PhIcon = when (kind) {
    "plan" -> PersonaIcons.taskPlan
    "question" -> PersonaIcons.help
    "screen" -> PersonaIcons.terminal
    else -> PersonaIcons.waitsForYou
}

/**
 * Glass waiting-agent row in the Inbox (matching iOS `InboxAgentRow`): rounded-square tinted icon well with semantic
 * blue request-kind icon, session title + short timestamp, ask line, and blue request headline + machine name.
 */
@Composable
internal fun GlassInboxAgentRow(
    interaction: InboxInteraction,
    machineName: String?,
    position: GroupPosition,
    dimmed: Boolean,
    onClick: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val res = LocalResources.current
    val clock = LocalInboxClock.current
    val zone = LocalInboxZone.current
    val timeText = interaction.createdAt?.let { displayTime(it, currentLocale, res, zone, clock()) }
    val heading = interaction.sessionTitle.ifBlank { interaction.title }
    val ask = (interaction.askLine ?: interaction.summary)?.trim()?.trimStart('#')?.trim()
        ?.takeIf { it.isNotEmpty() && it != heading }
    val fallbackKindLabel = when (interaction.kind) {
        "plan" -> stringResource(persona.core.designsystem.R.string.designsystem_decision_plan)
        "question" -> stringResource(persona.core.designsystem.R.string.designsystem_decision_question)
        "permission" -> stringResource(persona.core.designsystem.R.string.designsystem_decision_command)
        "screen" -> stringResource(persona.core.designsystem.R.string.designsystem_decision_screen)
        else -> ""
    }
    val rawHeadline = interaction.title.trim()
    val subtitleHeadline = if (rawHeadline.isEmpty() || rawHeadline == ask) fallbackKindLabel else rawHeadline
    val machine = machineName?.trim()?.takeIf { it.isNotEmpty() }

    JGroupItem(position) {
        Row(
            Modifier
                .fillMaxWidth()
                .alpha(if (dimmed) 0.45f else 1f)
                .clickable(enabled = !dimmed, role = Role.Button, onClick = onClick)
                .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
            verticalAlignment = Alignment.Top,
        ) {
            Box(
                Modifier
                    .size(MessageItemTokens.avatarSize(prominent = false))
                    .clip(RoundedCornerShape(Tokens.Radius.control))
                    .background(palette.needsYou.tint),
                contentAlignment = Alignment.Center,
            ) {
                JIcon(
                    inboxInteractionIcon(interaction.kind),
                    null,
                    size = Tokens.Size.iconXS,
                    tint = palette.needsYou.dot,
                    selected = true,
                    decorative = true,
                )
            }
            Column(
                Modifier.weight(1f),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
            ) {
                Row(
                    Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        heading,
                        style = type.headline,
                        color = palette.ink,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f),
                    )
                    timeText?.let {
                        Text(it, style = type.caption, color = palette.ink3, maxLines = 1)
                    }
                }
                if (ask != null) {
                    Text(
                        ask,
                        style = type.callout,
                        color = palette.ink,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                if (subtitleHeadline.isNotEmpty() || machine != null) {
                    Text(
                        buildAnnotatedString {
                            if (subtitleHeadline.isNotEmpty()) {
                                withStyle(SpanStyle(color = palette.needsYou.text, fontWeight = FontWeight.SemiBold)) {
                                    append(subtitleHeadline)
                                }
                            }
                            if (machine != null) {
                                if (subtitleHeadline.isNotEmpty()) {
                                    withStyle(SpanStyle(color = palette.ink2)) { append(" · ") }
                                }
                                withStyle(SpanStyle(color = palette.ink2)) { append(machine) }
                            }
                        },
                        style = type.caption,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
        }
    }
}
