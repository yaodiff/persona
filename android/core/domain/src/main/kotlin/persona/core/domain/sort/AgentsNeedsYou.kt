package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import java.time.Instant

/**
 * One row of the Agents tab's Needs you (spec/fixtures/needs-you, the iOS app's `AgentsNeedsYou`): a session that waits
 * for the user with its pending cards, oldest first, or a card on no session by itself. The tab's badge counts these
 * rows, so it reads the number iOS and the web read: sessions, never cards (docs/assistant/managers.md "Counts").
 */
data class NeedsYouRow(val key: SessionKey, val cards: List<Interaction>, val since: Instant?) {
    /** `agent:<machine>/<session>`, or `card:<id>` for a card on no session, as the shared fixture names rows. */
    val id: String
        get() = cards.singleOrNull()?.takeIf { it.sessionId.isEmpty() }?.let { "card:${it.id}" } ?: "agent:${key.path}"
}

/** The cards the Agents tab lists: none of the Persona tab's own conversations. */
fun needsYouCards(cards: List<Interaction>, sessions: Map<SessionKey, Session>): List<Interaction> =
    cards.filter { (sessions[it.homeKey] ?: sessions[it.sessionKey])?.assistant != true }

/** The sessions that wait for the user whether or not a card was read; never the Persona tab's own conversations. */
fun needsYouWaiting(sessions: List<Session>): List<Session> =
    foldedSessions(sessions).filter { !it.assistant && it.agentState == AgentState.NeedsYou }

/**
 * The rows for [cards] (pending) and [waiting] (sessions in Needs you): a session's cards under it, oldest first, the
 * session waiting while it has one whatever its state says; a session with no card read (a prompt only its terminal
 * shows) and each card on no session are rows of their own. Rows go oldest first, by their oldest card or, with none,
 * by when the session started waiting.
 */
fun needsYouRows(cards: List<Interaction>, waiting: List<Session>): List<NeedsYouRow> {
    val rows = ArrayList<NeedsYouRow>()
    val at = HashMap<SessionKey, Int>()
    cards.distinctBy { it.id }.sortedBy { parseTime(it.createdAt) ?: Instant.MIN }.forEach { card ->
        val i = at[card.homeKey]
        when {
            card.sessionId.isEmpty() -> rows += NeedsYouRow(card.sessionKey, listOf(card), parseTime(card.createdAt))
            i != null -> rows[i] = rows[i].copy(cards = rows[i].cards + card)
            else -> {
                at[card.homeKey] = rows.size
                rows += NeedsYouRow(card.homeKey, listOf(card), parseTime(card.createdAt))
            }
        }
    }
    waiting.forEach { session ->
        val home = session.foldedUnder?.takeIf(String::isNotBlank)?.let { SessionKey(session.machineId, it) } ?: session.key
        if (home !in at) {
            at[home] = rows.size
            rows += NeedsYouRow(home, emptyList(), parseTime(session.stateSince ?: session.updatedAt))
        }
    }
    // Stable: rows that started waiting at the same moment keep the order they came in.
    return rows.withIndex().sortedWith(compareBy({ it.value.since ?: Instant.MIN }, { it.index })).map { it.value }
}

/**
 * The rows the Agents tab's badge counts: [rows] less the ones that are not the tab's to announce, a quiet task's agents
 * (the main conversation tells about them) and the muted agents and spaces ([mutes]), as the assistant's count leaves
 * them out. The blank page's Needs you lists these too (#4339 C17), so it counts what the badge counts.
 */
fun needsYouBadgeRows(rows: List<NeedsYouRow>, sessions: Map<SessionKey, Session>, mutes: AgentPreferences): List<NeedsYouRow> =
    rows.filter { row ->
        val session = sessions[row.key]
        session?.quiet != true && !mutes.mutes(row.key.path, session?.spaceName?.let { "${row.key.machineId}/$it" })
    }

/** The Agents tab's badge: how many [needsYouBadgeRows] there are. */
fun needsYouBadge(rows: List<NeedsYouRow>, sessions: Map<SessionKey, Session>, mutes: AgentPreferences): Int =
    needsYouBadgeRows(rows, sessions, mutes).size
