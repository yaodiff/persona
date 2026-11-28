package persona.core.domain.sort

import persona.core.contracts.Machine
import persona.core.contracts.Session
import java.util.Locale

/** Space detail always includes pinned members and preserves the terminal's tab order, and each tab's pane order. */
data class AgentSpace(val id: String, val name: String, val members: List<AgentSession>) {
    val machineId: String get() = members.first().session.machineId
    val preferenceKey: String get() = "$machineId/$name"
    val cwd: String get() = members.first().session.cwd
    val projectFolder: String? get() {
        if (members.first().session.projectGroupId() == null) return null
        return members.mapNotNull { row ->
            row.session.workspaceCwd?.takeIf(String::isNotBlank) ?: row.session.cwd.takeIf(String::isNotBlank)
        }.groupingBy { it }.eachCount().entries
            .minWithOrNull(compareByDescending<Map.Entry<String, Int>> { it.value }.thenBy { it.key })?.key
    }
    val priority: Int get() = members.minOf { it.state.priority }
    val updatedAt get() = members.mapNotNull { it.updatedAt }.maxOrNull()
    val quiet: Boolean get() = quiet(AgentPreferences())
    /**
     * Nothing in it goes on: every agent is seen, none waits for the user's reply, and no held row belongs here
     * (iOS `AgentListOrder.isQuiet`). Idle-row visibility is separately [AgentSession.stays].
     * Ordinary pinned sessions already keep their row visible; they do not by themselves promote a quiet space.
     */
    fun quiet(mutes: AgentPreferences): Boolean = members.none {
        !it.isQuiet || it.isWaiting || it.awaitsReply(mutes) || it.justFinished(mutes) || it.heldInList
    }
}

private fun Session.projectGroupId(): String? = spaceId?.takeIf(String::isNotEmpty) ?: workspaceId?.takeIf(String::isNotEmpty)

fun agentSpaces(sessions: List<AgentSession>, byProject: Boolean = false): List<AgentSpace> = sessions
    .filter { it.isLive && !it.session.assistant }
    .groupBy { row ->
        val session = row.session
        val group = if (byProject) session.projectGroupId() else session.spaceId
        "${session.machineId}/${group ?: "-"}"
    }
    .map { (id, members) -> AgentSpace(id, members.first().session.let { it.spaceName ?: it.folderName },
        // the tabs in order, a tab's agents by their place in it (#2320: the initiator first)
        members.sortedWith(compareBy<AgentSession> { it.session.slotIndex ?: 0 }.thenBy { it.session.slotId.orEmpty() }.thenBy(PaneOrder) { it.session })) }
    .sortedWith(compareBy<AgentSpace> { it.priority }.thenByDescending { it.updatedAt }.thenBy { it.id })

/** A project's folder on its own computer, and the parent displayed when the project name is shared. */
data class AgentProjectFolder(val machineId: String, val path: String, val qualifier: String? = null)

internal fun projectFolders(spaces: List<AgentSpace>, machines: List<Machine>): Map<String, AgentProjectFolder> {
    val names = spaces.groupingBy { it.machineId to it.name.lowercase(Locale.ROOT) }.eachCount()
    val homes = machines.associate { it.id to it.home }
    return spaces.mapNotNull { space ->
        val folder = space.projectFolder ?: return@mapNotNull null
        val qualifier = if (names.getValue(space.machineId to space.name.lowercase(Locale.ROOT)) > 1)
            projectParent(folder, homes[space.machineId].orEmpty()) else null
        space.id to AgentProjectFolder(space.machineId, folder, qualifier)
    }.toMap()
}

private fun projectParent(folder: String, home: String): String? {
    val path = folder.trimEnd('/', '\\')
    val separator = path.indexOfLast { it == '/' || it == '\\' }
    if (separator < 0) return null
    val windows = '\\' in path || path.getOrNull(1) == ':'
    val root = separator == 0 || windows && separator == 2 && path[1] == ':'
    val parent = path.take(separator + if (root) 1 else 0)
    val prefix = home.trimEnd('/', '\\')
    if (prefix.isEmpty()) return parent
    if (parent.equals(prefix, ignoreCase = windows)) return "~"
    return if (parent.startsWith(prefix, ignoreCase = windows) && parent.getOrNull(prefix.length) in listOf('/', '\\'))
        "~" + parent.drop(prefix.length) else parent
}

fun AgentPreferences.togglePin(id: String, space: Boolean = false): AgentPreferences {
    fun List<String>.toggle() = if (id in this) filterNot { it == id } else this + id
    return if (space) copy(spaces = spaces.toggle()) else copy(sessions = sessions.toggle())
}

/** Same rule as iOS: muting inside a muted space is the agent's own mute, so it survives unmuting the space. */
fun AgentPreferences.toggleMute(id: String, spaceKey: String?): AgentPreferences {
    val rest = copy(sessions = sessions.filterNot { it == id }, allowed = allowed - id)
    return when {
        !mutes(id, spaceKey) -> rest.copy(sessions = rest.sessions + id)
        spaceKey != null && spaceKey in spaces -> rest.copy(allowed = rest.allowed + id)
        else -> rest
    }
}
