package persona.feature.assistant

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.Composable
import androidx.compose.runtime.key
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.orderHubInbox
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.NeedsYouRow

/** How many things the Persona home lists before it says how many more there are. */
internal const val NeedsYouShown = 3

/**
 * What needs the user now, over the Persona home's suggestions (iOS's ShellNeedsYou): the agents waiting on an answer,
 * an approval or a plan, then the inbox's "Needs you", newest first. [lines] are the first few; [more] says how many
 * are left (the agents beyond the first ones count too, like on iOS).
 */
data class NeedsYou(val lines: List<NeedsYouLine> = emptyList(), val more: Int = 0) {
    val isEmpty: Boolean get() = lines.isEmpty()
    val total: Int get() = lines.size + more
}

/** One line: who it is (a conversation, a message's title) and what it wants, on one line each. */
sealed interface NeedsYouLine {
    val key: String
    val who: String
    val what: String

    /**
     * An agent's pending request ([kind] is the interaction's: question, plan, permission, screen…). On Glass a line is a
     * session that waits for the user ([needsYouBySession]): [row] is its Needs you row's id, [requestId] its oldest card,
     * blank when [terminal] says it waits only in its terminal.
     */
    data class Agent(
        val session: SessionKey,
        val requestId: String,
        val kind: String,
        val toolName: String?,
        override val who: String,
        override val what: String,
        val row: String? = null,
        val terminal: Boolean = false,
    ) : NeedsYouLine {
        override val key: String get() = row ?: "agent:$requestId"
    }

    /** An inbox item the hub sorted into "Needs you"; [lark] tells a Lark message from a mail. [who] is blank when the item
     *  has neither a title nor a sender: the row then reads "Lark" or "Mail" ([needsYouWho]). */
    data class Inbox(
        val itemId: String,
        val lark: Boolean,
        val itemKind: String,
        override val who: String,
        override val what: String,
    ) : NeedsYouLine {
        override val key: String get() = "inbox:$itemId"
    }
}

/**
 * An inbox record as the home needs it: its item, and whether it came from Lark (it has a feed) or is a mail.
 * [working] says the AI twin has it in hand (`InboxRecord.agentWorking`): it is Persona's, so not a line here (#4848).
 */
data class NeedsYouMessage(val item: InboxItem, val lark: Boolean, val working: Boolean = false)

/**
 * The lines of [NeedsYou]: requests of agents that are not Persona's own conversations ([isAssistant], or a session
 * marked assistant) and not muted (by session, or by space unless the session is let through), in the order the hub
 * gives them; then the inbox items still to do in "Needs you", newest first. The first [shown] are lines, the rest a count.
 */
fun needsYou(
    requests: List<Interaction>,
    sessions: List<Session>,
    mutes: AgentPreferences,
    messages: List<NeedsYouMessage>,
    isAssistant: (SessionKey) -> Boolean = { false },
    shown: Int = NeedsYouShown,
): NeedsYou {
    val bySession = sessions.associateBy { it.key }
    val foldedHomes = requests.filter { !it.foldedUnder.isNullOrBlank() }.mapTo(HashSet()) { it.homeKey }
    val agents = requests.distinctBy { if (it.homeKey in foldedHomes) it.homeKey else it.id }.filter { request ->
        val session = bySession[request.homeKey]
        val space = session?.spaceName?.takeIf { it.isNotBlank() }?.let { "${request.machineId}/$it" }
        !isAssistant(request.homeKey) && session?.assistant != true && !mutes.mutes(request.homeKey.path, space)
    }.map { request ->
        NeedsYouLine.Agent(request.homeKey, request.id, request.kind, request.toolName,
            who = if (request.homeKey in foldedHomes) agentName(null, null, bySession[request.homeKey])
                else agentName(request, bySession[request.homeKey]), what = oneLine(request.headline.shown))
    }
    val all: List<NeedsYouLine> = agents + inboxLines(messages)
    return NeedsYou(all.take(shown), (all.size - shown).coerceAtLeast(0))
}

/**
 * The lines of the blank page's Needs you on Glass (#4339 C17): iOS's `ShellNeedsYou`, counting sessions as
 * `DESIGN_SYSTEM.md` section 4.8 does. One line per row the Agents tab's badge counts ([rows], `ShellCounts.agentRows`:
 * quiet and muted sessions left out), so the agents among them are the badge's number; a session's line reads its oldest
 * card, and a session that waits only in its terminal reads "Terminal needs you". Then the inbox's "Needs you", newest
 * first, as in [needsYou]. All of them: the page shows as many as fit ([needsYouShown]).
 */
fun needsYouBySession(rows: List<NeedsYouRow>, sessions: List<Session>, messages: List<NeedsYouMessage>): List<NeedsYouLine> {
    val bySession = sessions.associateBy { it.key }
    val agents = rows.map { row ->
        val card = row.cards.firstOrNull()
        val session = bySession[row.key]
        NeedsYouLine.Agent(row.key, card?.id.orEmpty(), card?.kind ?: "screen", card?.toolName,
            who = agentName(card?.sessionTitle?.takeIf { card.foldedUnder.isNullOrBlank() }, card?.cwd, session),
            what = card?.let { oneLine(it.headline.shown) }.orEmpty(),
            row = row.id, terminal = card == null)
    }
    return agents + inboxLines(messages)
}

/** The inbox items still to do in "Needs you" (not those the AI twin has in hand), newest first. */
private fun inboxLines(messages: List<NeedsYouMessage>): List<NeedsYouLine> {
    val lark = messages.filter { it.lark }.map { it.item.id }.toSet()
    // "Needs you" by the hub's section (status, else tier), as the inbox page groups it.
    return orderHubInbox(messages.filterNot { it.working }.map { it.item }.distinctBy { it.id }.filter { it.needsYou })
        .map { item ->
            NeedsYouLine.Inbox(item.id, item.id in lark, item.kind,
                who = item.title.ifBlank { item.sender.orEmpty() },
                what = oneLine(item.summary?.takeIf { it.isNotBlank() } ?: item.preview))
        }
}

/** The conversation's name as the lists show it: its title, else its folder, else blank (the row then reads "Agent", [needsYouWho]). */
private fun agentName(request: Interaction, session: Session?): String = agentName(request.sessionTitle, request.cwd, session)

private fun agentName(title: String?, cwd: String?, session: Session?): String =
    title?.trim()?.takeIf { it.isNotEmpty() && it != "Claude Code" }
        ?: session?.title?.trim()?.takeIf { it.isNotEmpty() && it != "Claude Code" }
        ?: (cwd ?: session?.cwd)?.trimEnd('/')?.substringAfterLast('/')?.takeIf { it.isNotBlank() }
        .orEmpty()

private fun oneLine(text: String): String = text.replace(Regex("\\s+"), " ").trim()

/** The interaction's mark: what kind of decision is waiting. */
internal fun needsYouIcon(line: NeedsYouLine): PhIcon = when (line) {
    is NeedsYouLine.Agent -> when (line.kind) {
        "question" -> PhIcons.SealQuestion
        "plan" -> PhIcons.ListChecks
        "permission" -> if (line.toolName == "Bash") PhIcons.Terminal else PhIcons.ShieldWarning
        "screen" -> PhIcons.TerminalWindow
        "elicitation" -> PhIcons.NotePencil
        else -> PhIcons.Hand
    }
    is NeedsYouLine.Inbox -> when {
        line.itemKind == "doc" -> PhIcons.FileText
        line.lark -> PhIcons.ChatsCircle
        else -> PhIcons.EnvelopeSimple
    }
}

/**
 * "Needs you now" on the Persona home: one row per thing (its mark, who, what), a tap opens it where it lives; then
 * "N more in the inbox" when there are more. Nothing at all when nothing needs the user.
 */
@Composable
internal fun NeedsYouSection(needs: NeedsYou, onOpen: (NeedsYouLine) -> Unit, onMore: () -> Unit, modifier: Modifier = Modifier) {
    if (needs.isEmpty) return
    val palette = PersonaTheme.palette
    val count = needs.lines.size + if (needs.more > 0) 1 else 0
    Column(modifier) {
        JSectionHeader(stringResource(R.string.assistant_needs_you_header), count = needs.total, status = Status.NeedsYou)
        needs.lines.forEachIndexed { index, line ->
            key(line.key) {
                JGroupItem(GroupPosition.of(index, count)) {
                    JRow(needsYouWho(line), subtitle = line.what.ifBlank { null }, subtitleLines = 1, onClick = { onOpen(line) },
                        leading = {
                            // Regular weight: some fill glyphs (a terminal window, a checklist) read as solid blocks.
                            JIcon(needsYouIcon(line), null, tint = if (line is NeedsYouLine.Agent) palette.needsYou.dot else palette.ink2)
                        })
                }
            }
        }
        if (needs.more > 0) JGroupItem(GroupPosition.of(count - 1, count)) {
            JRow(pluralStringResource(R.plurals.assistant_needs_you_more, needs.more, needs.more), onClick = onMore,
                leading = { JIcon(PhIcons.Tray, null, tint = palette.ink2) })
        }
    }
}

/** Who a line is from as the row reads it: an agent without a title or folder is "Agent", an inbox item without a title or sender is "Lark" or "Mail". */
@Composable
internal fun needsYouWho(line: NeedsYouLine): String = when {
    line.who.isNotBlank() -> line.who
    line is NeedsYouLine.Agent -> stringResource(R.string.assistant_needs_you_agent)
    line is NeedsYouLine.Inbox && line.lark -> stringResource(R.string.assistant_source_lark)
    else -> stringResource(R.string.assistant_source_mail)
}
