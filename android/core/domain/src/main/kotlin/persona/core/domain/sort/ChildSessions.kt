package persona.core.domain.sort

import persona.core.contracts.Session
import persona.core.contracts.GroupKeys
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime

/** A child is hidden only while this list holds its parent on the same machine (web foldRows). */
fun foldRows(rows: List<AgentSession>, hierarchy: Boolean = false): List<AgentSession> {
    val listed = rows.mapTo(HashSet()) { it.session.key }
    val knownGroups = rows.filter { it.hierarchyKnown || !it.session.foldedUnder.isNullOrBlank() || it.session.children != null }
        .mapTo(HashSet()) { GroupKeys.namespaceOf(it.session.machineId) }
    return rows.filterNot { row -> row.session.foldedUnder?.takeIf(String::isNotBlank)?.let {
        SessionKey(row.session.machineId, it) in listed
    } == true }.map { row ->
        val known = hierarchy || GroupKeys.namespaceOf(row.session.machineId) in knownGroups
        val session = row.session.let { if (it.agentState == row.state) it else it.copy(state = row.state.wire) }
        if (session === row.session && (!known || row.hierarchyKnown)) row else row.copy(session = session, hierarchyKnown = known || row.hierarchyKnown)
    }
}

/** List and dock copies carry the folded state without changing the repository's own session rows. */
fun foldedSessions(rows: List<Session>, hierarchy: Boolean = false): List<Session> = foldRows(rows.map(::AgentSession), hierarchy).map { row ->
    row.session.let { if (it.agentState == row.state) it else it.copy(state = row.state.wire) }
}

/** Every depth is already assigned to its topmost ancestor by the hub. */
fun childSessions(rows: List<Session>, parent: SessionKey): List<Session> = rows
    .filter { it.machineId == parent.machineId && it.foldedUnder == parent.sessionId && it.key != parent }
    .sortedWith(compareBy<Session> { parseTime(it.startedAt ?: it.updatedAt) }.thenBy { it.sessionId })
