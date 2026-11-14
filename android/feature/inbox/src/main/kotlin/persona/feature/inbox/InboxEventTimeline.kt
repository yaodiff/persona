package persona.feature.inbox

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.GroupKeys
import persona.core.contracts.InboxChainEntry
import persona.core.designsystem.Brand
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSourceMark
import persona.core.designsystem.JTag
import persona.core.designsystem.MarkSize
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.channelBrand
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rowPadding
import persona.core.designsystem.text
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.stateWord

/** The shared icon meaning checked by spec/fixtures/inbox/timeline.json, independent of the platform glyph. */
internal fun chainIconMeaning(entry: InboxChainEntry): String {
    if (entry.kind == "reported") return "reported"
    when (entry.via?.takeIf { it.isNotBlank() }) {
        "assistant" -> return "persona"
        "agent" -> return "agents"
        "user" -> return "person"
        null, "" -> Unit
        else -> return "source"
    }
    return when (entry.kind) {
        "received" -> "received"
        "triaged" -> "triaged"
        "handling" -> "persona"
        "sent" -> "sendMail"
        "agent" -> "agents"
        "confirmed" -> "success"
        "rejected" -> "rejected"
        "instructed" -> "instructed"
        "self_replied" -> "reply"
        "failed" -> "error"
        "concluded" -> "concluded"
        "done" -> "done"
        else -> "info"
    }
}

/** The live Agent behind an agent step, never inferred from the historical entry's kind. */
internal fun chainAgentSession(entry: InboxChainEntry, sessions: List<AgentSession>): AgentSession? {
    if (entry.kind !in setOf("agent", "agent_stopped")) return null
    val link = entry.link ?: return null
    if (link.type != "session") return null
    val machine = link.machineId?.takeIf { it.isNotBlank() } ?: return null
    val session = link.sessionId?.takeIf { it.isNotBlank() } ?: return null
    return sessions.firstOrNull { it.session.machineId == machine && it.session.sessionId == session }
        ?: sessions.firstOrNull { GroupKeys.raw(it.session.machineId) == machine && it.session.sessionId == session }
}

/** Whether this is the last agent step for its session in hub order, and its live state can be shown. */
internal fun showsAgentState(chain: List<InboxChainEntry>, entry: InboxChainEntry, sessions: List<AgentSession>): Boolean {
    val session = chainAgentSession(entry, sessions) ?: return false
    return chain.indexOfLast { chainAgentSession(it, sessions) === session } == chain.indexOf(entry)
}

/** A reported row shows the original words, not the old app's "Assistant says:" prefix a second time. */
internal fun chainEntryText(entry: InboxChainEntry): String =
    if (entry.kind == "reported") entry.detail?.takeIf { it.isNotBlank() } ?: entry.text else entry.text

/** A reported detail is already on the row; it never opens a second copy inline. Other targets keep main's behavior. */
internal fun chainRowTarget(entry: InboxChainEntry, record: InboxRecord): ChainTarget {
    val target = chainTarget(entry, record)
    return if (entry.kind == "reported" && target == ChainTarget.Inline) ChainTarget.None else target
}

/** The timeline's latest three steps, with the older of those three first; all of it once expanded. */
internal fun LazyListScope.eventChain(
    record: InboxRecord,
    sessions: List<AgentSession>,
    mutes: AgentPreferences,
    go: (ChainTarget) -> Unit,
) {
    val chain = record.event.chain
    if (chain.isEmpty()) return
    item("event-chain") {
        var expanded by rememberSaveable(record.item.id) { mutableStateOf(false) }
        val shown = shownChain(chain, expanded)
        Column {
            JSectionHeader(stringResource(R.string.inbox_event_chain), count = chain.size,
                trailing = if (chain.size > 3) stringResource(if (expanded) R.string.inbox_collapse else R.string.inbox_event_chain_all, chain.size) else null,
                onClick = if (chain.size > 3) ({ expanded = !expanded }) else null,
                open = if (chain.size > 3) expanded else null, trailingIsFoldAction = true)
            shown.forEachIndexed { index, entry ->
                JGroupItem(GroupPosition.of(index, shown.size)) { ChainRow(entry, record, sessions, mutes, go) }
            }
        }
    }
}

@Composable
private fun ChainSourceIcon(via: String, record: InboxRecord) {
    when {
        via == "chat" -> JSourceMark(record.sourceLabel.ifBlank { via }, brand = channelBrand(via), size = MarkSize.Inline)
        via == "mail" || via == record.source && record.isMail ->
            JIcon(PersonaIcons.mail, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        via in setOf("lark", "feishu") || via == record.source && record.isLark ->
            JSourceMark(record.sourceLabel.ifBlank { via }, brand = Brand.Lark, size = MarkSize.Inline)
        else -> JSourceMark(if (via == record.source) record.sourceLabel.ifBlank { via } else via,
            brand = channelBrand(via), size = MarkSize.Inline)
    }
}

@Composable
private fun ChainIcon(entry: InboxChainEntry, record: InboxRecord) {
    val via = entry.via?.takeIf { it.isNotBlank() }
    if (via == null && entry.kind != "reported") {
        JIcon(chainIcon(entry.kind, record), null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        return
    }
    when (chainIconMeaning(entry)) {
        "source" -> ChainSourceIcon(via ?: record.source, record)
        "persona" -> JIcon(PersonaIcons.persona, null, size = Tokens.Space.l, tint = PersonaTheme.palette.accent, decorative = true)
        "agents" -> JIcon(PersonaIcons.agents, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        "person" -> JIcon(PersonaIcons.person, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        "reported" -> JIcon(PersonaIcons.reported, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
        else -> JIcon(chainIcon(entry.kind, record), null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink2, decorative = true)
    }
}

@Composable
private fun ChainRow(
    entry: InboxChainEntry,
    record: InboxRecord,
    sessions: List<AgentSession>,
    mutes: AgentPreferences,
    go: (ChainTarget) -> Unit,
) {
    val palette = PersonaTheme.palette
    val reported = entry.kind == "reported"
    val words = chainEntryText(entry).ifBlank { entry.kind }
    val baseTarget = chainRowTarget(entry, record)
    var open by rememberSaveable(
        record.item.id,
        entry.id,
        if (reported) words else null,
        if (reported) entry.messages else null,
    ) { mutableStateOf(false) }
    var clipped by remember(record.item.id, entry.id, words) { mutableStateOf(false) }
    val target = when {
        reported && baseTarget == ChainTarget.None ->
            if (open || clipped || entry.messages.isNotEmpty()) ChainTarget.Inline else ChainTarget.None
        else -> baseTarget
    }
    val clock = LocalInboxClock.current
    val zone = LocalInboxZone.current
    val time = chainTime(entry.at, currentLocale, clock(), zone)
    val agent = chainAgentSession(entry, sessions)
    val agentState = if (showsAgentState(record.event.chain, entry, sessions)) agent?.let { it.stateWord(mutes.mutes(it)).text() } else null
    JRow(onClick = when (target) {
        ChainTarget.None -> null
        ChainTarget.Inline -> ({ open = !open })
        else -> ({ go(target) })
    }, chevron = target !is ChainTarget.None && target !is ChainTarget.Inline) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (time.isNotEmpty()) Text(time, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
            ChainIcon(entry, record)
            if (reported) JTag(stringResource(R.string.inbox_event_assistant_says))
            Text(words, Modifier.weight(1f, fill = false), style = PersonaTheme.type.callout,
                color = if (reported) palette.ink2 else palette.ink, maxLines = if (open) Int.MAX_VALUE else 2, overflow = TextOverflow.Ellipsis,
                onTextLayout = { layout ->
                    if (reported && !open) {
                        val nowClipped = layout.lineCount > 0 &&
                            layout.getLineEnd(layout.lineCount - 1, visibleEnd = true) < words.trimEnd().length
                        if (clipped != nowClipped) clipped = nowClipped
                    }
                })
            agentState?.let { Text(it, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            if (target == ChainTarget.Inline) JIcon(if (open) PersonaIcons.collapse else PersonaIcons.expand, null,
                size = Tokens.Space.l, tint = palette.ink3, decorative = true)
        }
    }
    if (open && target == ChainTarget.Inline && (!reported || entry.messages.isNotEmpty())) Column(Modifier.fillMaxWidth().padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.m),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (!reported) entry.detail?.takeIf { it.isNotBlank() }?.let { Text(it, style = PersonaTheme.type.callout, color = palette.ink2) }
        entry.messages.forEach { message ->
            Text(stringResource(R.string.inbox_suggestion_row, message.sender, message.text), style = PersonaTheme.type.callout, color = palette.ink2)
        }
    }
}
