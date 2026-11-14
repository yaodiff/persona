package persona.android.ui

import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.api.CurrentAssistant
import persona.core.contracts.api.ManagerRoster
import persona.core.contracts.api.waiting
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.NeedsYouRow
import persona.core.domain.sort.needsYouBadgeRows
import persona.core.domain.sort.needsYouCards
import persona.core.domain.sort.needsYouRows
import persona.core.domain.sort.needsYouWaiting
import persona.feature.inbox.InboxRecord

/**
 * What the shell counts beside the pages (docs/assistant/managers.md "Clients"): the current assistant's alone (the tab
 * badges, the sidebar's to-dos), and what waits at each assistant for the switcher's badges and the dot on the
 * sidebar's button. [sessions] are every manager's, [mutes] the phone's muted sessions and spaces.
 */
internal class ShellCounts(
    private val current: CurrentAssistant,
    private val sessions: List<Session>,
    private val mutes: AgentPreferences,
) {
    private val byKey = sessions.associateBy { it.key }

    /** A card is its own manager's, else its session's (an older hub stamps neither: the default's). */
    fun owned(request: Interaction): Boolean =
        current.ownsOn(request.machineId, request.manager ?: byKey[request.sessionKey]?.manager)

    /** A card a count counts: not a quiet task's (#1731, told in the main conversation), not a muted session's. */
    fun countsCard(request: Interaction): Boolean {
        val session = byKey[request.homeKey]
        return session?.quiet != true && !mutes.mutes(request.homeKey.path, session?.spaceName?.let { "${request.machineId}/$it" })
    }

    /** A session the switcher's badges count: not quiet, not muted. */
    fun countsSession(session: Session): Boolean =
        !session.quiet && !mutes.mutes(session.key.path, session.spaceName?.let { "${session.machineId}/$it" })

    /**
     * The Agents tab's badge (#3580): the current assistant's agents waiting for the user, each session once however many
     * cards it has, and each card on no session, less quiet and muted ones (spec/fixtures/needs-you), the number iOS and
     * the web read. [requests] are its cards outside its own conversations.
     */
    fun agentsWaiting(requests: List<Interaction>): Int = agentRows(requests).size

    /** The rows [agentsWaiting] counts, oldest first: what the blank page's Needs you lists on Glass (#4339 C17). */
    fun agentRows(requests: List<Interaction>): List<NeedsYouRow> {
        val waiting = needsYouWaiting(sessions.filter { current.ownsOn(it.machineId, it.manager) })
        return needsYouBadgeRows(needsYouRows(needsYouCards(requests, byKey), waiting), byKey, mutes)
    }

    /** The current assistant's open to-dos, quiet ones left out: the sidebar's count beside To-dos. */
    fun todos(todos: List<Todo>): Int = todos.count { !it.isDone && !it.quiet && current.owns(it.manager) }

    /** What waits at each assistant ([waiting]): the switcher's badges. */
    fun waiting(roster: ManagerRoster?, pending: List<Interaction>): Map<String, Int> {
        // A paged window may not hold the parent yet: its children's cards still count as one session there.
        val missing = pending.filter { !it.foldedUnder.isNullOrBlank() && it.homeKey !in byKey }.distinctBy { it.homeKey }
            .map { Session(it.machineId, it.homeKey.sessionId, state = "needs_you", manager = it.manager ?: byKey[it.sessionKey]?.manager) }
        return roster?.waiting(pending.map { it.copy(sessionId = it.homeKey.sessionId) },
            persona.core.domain.sort.foldedSessions(sessions + missing), current.group, ::countsCard, ::countsSession).orEmpty()
    }

    /** The dot on the sidebar's button: something waits at another assistant than the current one. */
    fun othersWaiting(roster: ManagerRoster?, pending: List<Interaction>): Boolean = current.othersWaiting(waiting(roster, pending))

    companion object {
        /** The current assistant's inbox items (no stamp: the default's), what the sidebar's Inbox count and "Needs you now" read. */
        fun records(current: CurrentAssistant, records: List<InboxRecord>): List<InboxRecord> = records.filter { current.owns(it.manager) }

        /** The current assistant's agents' cards as the inbox lists them. */
        fun interactions(current: CurrentAssistant, interactions: List<InboxInteraction>): List<InboxInteraction> =
            interactions.filter { current.ownsOn(it.machineId, it.manager) }
    }
}
