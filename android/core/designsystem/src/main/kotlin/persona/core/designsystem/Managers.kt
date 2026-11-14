package persona.core.designsystem

import android.content.res.Resources
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.size
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.ui.Alignment
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.parseTime
import java.time.Duration
import java.time.Instant
import java.time.ZoneId

// The hub's managers, "assistants" to the user (docs/assistant/managers.md "Clients"): the sidebar's switcher picks the
// current one, and every page shows what is its.

/**
 * The selected hub's roster of managers, as the app last read it; null until read (or from a hub without managers).
 * Pages read the current assistant from it ([currentAssistant]) and [ManagerRoster.isMulti]: with one manager they draw as before.
 */
val LocalManagerRoster = compositionLocalOf<ManagerRoster?> { null }

/**
 * What waits for the user at each assistant ([persona.core.contracts.api.waiting], the shell counts it): the switcher's
 * badges wherever its roster opens (the sidebar's head, Settings' first group).
 */
val LocalAssistantWaiting = compositionLocalOf<Map<String, Int>> { emptyMap() }

/** The group ([persona.core.contracts.GroupKeys] namespace; null: the primary) whose hub [LocalManagerRoster] is. */
val LocalManagerGroup = compositionLocalOf<String?> { null }

/**
 * The current assistant, for a page to list only what is its ([persona.core.contracts.api.CurrentAssistant.owns]):
 * the switcher's choice against the roster, read again when either changes.
 */
@Composable
fun currentAssistant(): persona.core.contracts.api.CurrentAssistant {
    val choice by persona.core.contracts.api.AssistantManagerChoice.current.collectAsState()
    return persona.core.contracts.api.CurrentAssistant(LocalManagerRoster.current, choice, LocalManagerGroup.current)
}

/** How long ago, as a manager's 最后同步于 says it: 刚刚, n 分钟前, n 小时前, then the day. */
fun managerAgo(res: Resources, at: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val gap = Duration.between(at, now)
    return when {
        gap.isNegative || gap < Duration.ofMinutes(1) -> res.getString(R.string.designsystem_manager_just_now)
        gap < Duration.ofHours(1) -> gap.toMinutes().toInt().let { res.getQuantityString(R.plurals.designsystem_manager_minutes_ago, it, it) }
        gap < Duration.ofDays(1) -> gap.toHours().toInt().let { res.getQuantityString(R.plurals.designsystem_manager_hours_ago, it, it) }
        else -> LocalizedFormats.format(at, LocalizedFormats.MONTH_DAY, res.configuration.locales[0], zone)
    }
}

/** An offline manager's 「最后同步于 …」, or 「离线」 without a time; null while it is online. */
fun managerOfflineText(res: Resources, manager: HubManager, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String? {
    if (manager.online) return null
    return parseTime(manager.lastSeen)?.let { res.getString(R.string.designsystem_manager_last_synced, managerAgo(res, it, now, zone)) }
        ?: res.getString(R.string.designsystem_manager_offline)
}

/**
 * What a conversation of manager [id] on machine [machineId] says where its composer was while that manager is offline
 * (several managers, the selected group's hub: [group]): 「工作 · 最后同步于 3 分钟前」; null when it can take messages, or
 * with one manager. Wherever the conversation was opened (the inbox, a notification), nothing can be sent to it then.
 */
fun managerOfflineNote(res: Resources, roster: persona.core.contracts.api.ManagerRoster?, group: String?, machineId: String, id: String?): String? {
    if (roster == null || !roster.isMulti || persona.core.contracts.GroupKeys.namespaceOf(machineId) != group) return null
    val manager = roster.manager(roster.ownerOf(id))?.takeIf { !it.isInstalling } ?: return null
    return managerOfflineText(res, manager)?.let { res.getString(R.string.designsystem_manager_offline_note, manager.shownName, it) }
}

/** The name a manager goes by on the pages: its name, else its id. */
val HubManager.shownName: String get() = name.ifBlank { id }

/**
 * The one name every assistant surface reads — the switcher, the sidebar's main-conversation row, the top bar and the
 * composer placeholder (#6401). With several assistants ([ManagerRoster.isMulti]) it is the current manager's
 * [HubManager.shownName], so those surfaces and the switcher can never disagree. With a single installed assistant the
 * product keeps its own title ([fallback], the persona pack's name): a lone assistant shows no personal identity; the
 * same applies before the roster is read or on a hub without the managers API.
 */
fun persona.core.contracts.api.ManagerRoster?.assistantDisplayName(shownId: String, fallback: String): String {
    if (this == null || !isMulti) return fallback
    return manager(shownId)?.shownName?.takeIf { it.isNotBlank() } ?: fallback
}

/**
 * The colour of an assistant's monogram, the same on every client: the default assistant's is the AI purple (null
 * here), another's goes by the sum of its id's UTF-8 bytes, mod 5: blue, green, orange, pink, indigo.
 */
fun managerHue(id: String, isDefault: Boolean): JHue? {
    if (isDefault) return null
    val sum = id.toByteArray(Charsets.UTF_8).sumOf { it.toInt() and 0xFF }
    return ManagerHues[sum % ManagerHues.size]
}

private val ManagerHues = listOf(JHue.Blue, JHue.Green, JHue.Orange, JHue.Pink, JHue.Indigo)

/** The first letter an assistant's monogram shows: its name's first character (a whole code point), in capitals. */
fun managerInitial(name: String): String {
    val trimmed = name.trim()
    if (trimmed.isEmpty()) return ""
    return trimmed.substring(0, trimmed.offsetByCodePoints(0, 1)).uppercase()
}

/** An assistant's presence, the dot at its monogram's corner: green online, grey offline, orange while it is installed. */
@Composable
fun managerPresenceColor(manager: HubManager): androidx.compose.ui.graphics.Color {
    val palette = PersonaTheme.palette
    return when {
        manager.isInstalling -> palette.hue(JHue.Orange).dot
        manager.online -> palette.status(Status.Unread).dot
        // The offline tone's dot is a faint fill meant to sit inside its outline; alone on a monogram its text grey reads.
        else -> palette.status(Status.Offline).text
    }
}

/**
 * An assistant in the switcher: its initial on its colour ([managerHue]) in a circle, with its presence
 * ([managerPresenceColor]) as a dot at the bottom corner when [presence] is set. The dot's ring is cut out of the
 * monogram, so it is whatever lies behind (the sidebar, the menu, a selected row). Not [JAvatar]: that one is a
 * person's or a source's picture on the quiet surface, and this one's colour is what tells the assistants apart.
 */
@Composable
fun JManagerAvatar(
    manager: HubManager, isDefault: Boolean, modifier: Modifier = Modifier,
    size: androidx.compose.ui.unit.Dp = HomeTokens.assistantAvatar, presence: Boolean = false,
    /** Something waits for the user at another assistant: the needs-you dot at the top corner. */
    alert: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val hue = managerHue(manager.id, isDefault)
    val fill = hue?.let { palette.hue(it).dot } ?: palette.ai
    val ink = hue?.let { palette.hue(it).onDot } ?: ExtensionThemeColors.on(fill)
    val dot = if (presence) managerPresenceColor(manager) else null
    androidx.compose.foundation.layout.Box(modifier.size(size).clearAndSetSemantics {}) {
        androidx.compose.foundation.layout.Box(
            Modifier.fillMaxSize()
                // Its own layer, so clearing the ring leaves what is behind the avatar, not a hole through the window.
                .graphicsLayer { compositingStrategy = androidx.compose.ui.graphics.CompositingStrategy.Offscreen }
                .drawWithContent {
                    drawContent()
                    if (dot != null) {
                        val radius = HomeTokens.assistantPresenceDot.toPx() / 2
                        val ring = HomeTokens.assistantPresenceRing.toPx()
                        val center = androidx.compose.ui.geometry.Offset(this.size.width - radius - ring, this.size.height - radius - ring)
                        drawCircle(androidx.compose.ui.graphics.Color.Transparent, radius + ring, center,
                            blendMode = androidx.compose.ui.graphics.BlendMode.Clear)
                        drawCircle(dot, radius, center)
                    }
                },
        ) {
            androidx.compose.foundation.layout.Box(
                Modifier.fillMaxSize().clip(androidx.compose.foundation.shape.CircleShape).background(fill),
                contentAlignment = Alignment.Center,
            ) {
                // Sized by the circle, not by the system's text size: the letter is part of the mark (at 200 % text it
                // would outgrow the circle and cover the presence dot).
                val initial = with(androidx.compose.ui.platform.LocalDensity.current) { (size * HomeTokens.assistantInitialShare).toSp() }
                Text(managerInitial(manager.shownName), style = PersonaTheme.type.label.copy(fontSize = initial,
                    lineHeight = androidx.compose.ui.unit.TextUnit.Unspecified, fontWeight = androidx.compose.ui.text.font.FontWeight.SemiBold),
                    color = ink, maxLines = 1)
            }
        }
        if (alert) JStatusDot(Status.NeedsYou, Modifier.align(Alignment.TopEnd), breathes = false) // still, as iOS's `AssistantWaitingDot`
    }
}

/** BlendMode.Clear uses only the source alpha; RGB must not become an opaque cutout color. */
private val AvatarPresenceCutout = Tokens.System.Light.primary.copy(alpha = 0f)

/**
 * The default assistant's head: the chosen look's face (PersonaFace), not the first-letter monogram, so changing the
 * look changes this face too — one identity with the main conversation (#6401). The presence and needs-you corner
 * marks are the same [JManagerAvatar] draws, with the ring cut out of the face's own layer.
 */
@Composable
fun JDefaultManagerFace(
    manager: HubManager, modifier: Modifier = Modifier,
    size: androidx.compose.ui.unit.Dp = HomeTokens.assistantAvatar, presence: Boolean = false, alert: Boolean = false,
) {
    val dot = if (presence) managerPresenceColor(manager) else null
    androidx.compose.foundation.layout.Box(modifier.size(size).clearAndSetSemantics {}) {
        androidx.compose.foundation.layout.Box(
            Modifier.fillMaxSize()
                .graphicsLayer { compositingStrategy = androidx.compose.ui.graphics.CompositingStrategy.Offscreen }
                .drawWithContent {
                    drawContent()
                    if (dot != null) {
                        val radius = HomeTokens.assistantPresenceDot.toPx() / 2
                        val ring = HomeTokens.assistantPresenceRing.toPx()
                        val center = androidx.compose.ui.geometry.Offset(this.size.width - radius - ring, this.size.height - radius - ring)
                        drawCircle(AvatarPresenceCutout, radius + ring, center,
                            blendMode = androidx.compose.ui.graphics.BlendMode.Clear)
                        drawCircle(dot, radius, center)
                    }
                },
        ) {
            PersonaFace(PersonaState.Idle, Modifier.fillMaxSize(), size = size)
        }
        if (alert) JStatusDot(Status.NeedsYou, Modifier.align(Alignment.TopEnd))
    }
}
@Composable
private fun AssistantLine(text: String) =
    Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)

/**
 * The sidebar's head: the current assistant ([avatar], its [name] with a caret, its [status] line under it); a tap
 * opens and closes the roster's menu under it ([expanded]). [trailing] is the bar's other button (the sidebar's search).
 * Spoken as one: "name, status", and that something waits at another assistant ([othersWaiting], the avatar's dot).
 */
@Composable
fun JAssistantSwitcherHeader(
    name: String, status: String, expanded: Boolean, onClick: () -> Unit, modifier: Modifier = Modifier,
    avatar: @Composable () -> Unit, trailing: (@Composable () -> Unit)? = null, othersWaiting: Boolean = false,
) {
    val palette = PersonaTheme.palette
    val label = stringResource(R.string.designsystem_manager_switcher)
    val stateText = stringResource(if (expanded) R.string.designsystem_expanded else R.string.designsystem_collapsed)
    val named = stringResource(R.string.designsystem_manager_spoken, name, status)
    val spoken = if (othersWaiting) stringResource(R.string.designsystem_manager_spoken, named, stringResource(R.string.designsystem_manager_others_waiting))
        else named
    Row(modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Row(Modifier.weight(1f).heightIn(min = Tokens.MinTouch.android).clip(PersonaTheme.shapes.controlShape)
            .clickable(role = androidx.compose.ui.semantics.Role.Button, onClickLabel = label, onClick = onClick)
            .semantics(mergeDescendants = true) { contentDescription = spoken; stateDescription = stateText }
            .padding(vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically) {
            avatar()
            Spacer(Modifier.width(Tokens.Space.m))
            androidx.compose.foundation.layout.Column(Modifier.weight(1f, fill = false)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(name, Modifier.weight(1f, fill = false), style = PersonaTheme.type.headline, color = palette.ink,
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Spacer(Modifier.width(Tokens.Space.xs))
                    JIcon(if (expanded) persona.core.designsystem.icons.PhIcons.CaretUp else persona.core.designsystem.icons.PhIcons.CaretDown,
                        null, size = Tokens.Space.m, tint = palette.ink3, decorative = true)
                }
                AssistantLine(status)
            }
        }
        trailing?.let { Spacer(Modifier.width(Tokens.Space.s)); it() }
    }
}

/**
 * One assistant of the switcher's roster: its monogram with its presence dot, its name with Default beside it, its
 * [status] line, and at the end what waits for the user there ([badge]) or the tick of the current one. An offline
 * one's row is faded as a whole and can still be picked.
 */
@Composable
fun JAssistantSwitcherRow(
    manager: HubManager, isDefault: Boolean, status: String, current: Boolean, badge: Int,
    onClick: () -> Unit, modifier: Modifier = Modifier, defaultTag: String? = null,
) {
    val palette = PersonaTheme.palette
    val faded = !manager.online && !manager.isInstalling
    // Spoken as one: "name, Default, status", then "Current" or how many wait for the user, not a bare number.
    val spoken = listOfNotNull(manager.shownName, defaultTag, status,
        if (current) stringResource(R.string.designsystem_manager_current)
        else badge.takeIf { it > 0 }?.let { androidx.compose.ui.res.pluralStringResource(R.plurals.designsystem_manager_waiting, it, it) },
    ).reduce { spokenSoFar, part -> stringResource(R.string.designsystem_manager_spoken, spokenSoFar, part) }
    JRow(modifier.stableAlpha(if (faded) HomeTokens.assistantOfflineAlpha else 1f).semantics { contentDescription = spoken },
        onClick = onClick, chevron = false,
        selected = current, trailing = if (current || badge > 0) ({
        if (current) JIcon(persona.core.designsystem.icons.PhIcons.Check, null, size = Tokens.Size.iconXS, tint = palette.accent, decorative = true)
        else JBadge(badge)
    }) else null) {
        JManagerAvatar(manager, isDefault, size = HomeTokens.assistantAvatarSmall, presence = true)
        Spacer(Modifier.width(Tokens.Space.m))
        androidx.compose.foundation.layout.Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(manager.shownName, Modifier.weight(1f, fill = false), style = PersonaTheme.type.body, color = palette.ink,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                defaultTag?.let { JTag(it) }
            }
            AssistantLine(status)
        }
    }
}

/**
 * The head of Settings' first group, the current assistant's settings (docs/assistant/managers.md "Clients"): its
 * monogram with the presence dot ([manager]; none: no monogram), "{name}'s settings" ([title]), and Switch
 * ([onSwitch]), which opens the same roster as the sidebar's head; without [onSwitch] (no roster to switch in) no Switch.
 */
@Composable
fun JAssistantSettingsHeader(
    title: String, manager: HubManager?, isDefault: Boolean, switchLabel: String, onSwitch: (() -> Unit)?, modifier: Modifier = Modifier,
) {
    JSectionHeader(title, modifier,
        leading = manager?.let { { JManagerAvatar(it, isDefault, size = HomeTokens.assistantAvatarHeader, presence = true) } },
        action = onSwitch?.let { switch -> { JTextButton(switchLabel, switch, Modifier.heightIn(min = Tokens.MinTouch.android)) } })
}
