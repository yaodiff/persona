package persona.android.ui

import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import persona.core.domain.sort.TerminalResume
import persona.core.domain.sort.canTerminal
import persona.core.domain.sort.hasSource
import persona.core.domain.sort.isStoppedAgent
import persona.core.domain.sort.terminalResume
import persona.feature.terminal.TerminalResumeFailed
import persona.feature.terminal.TerminalResumer
import java.util.UUID

/**
 * Resume session on the terminal of a session that isn't running (#4345 / #5565), as on iOS (#4328): `POST /api/v1/sessions`
 * with `resume_session_id` and what the session ran with; a node from #4328 on fills in what is left out.
 */
internal class SessionTerminalResumer(
    private val sessions: () -> List<Session>,
    private val creation: () -> AgentCreationService?,
    private val machines: () -> List<Machine> = { emptyList() },
) : TerminalResumer {
    override fun isStopped(key: SessionKey): Boolean = sessions().firstOrNull { it.key == key }?.isStoppedAgent == true

    override fun canResume(key: SessionKey): Boolean {
        val session = sessions().firstOrNull { it.key == key } ?: return false
        if (creation() == null) return false
        val machine = machines().firstOrNull { it.id == session.machineId }
        // Nothing to resume it in (#5565): the same rule as ⋯'s Resume in Terminal.
        if (terminalResume(session, machine) == TerminalResume.NoEngine) return false
        return resumableInTerminal(session)
    }

    override suspend fun resume(key: SessionKey) {
        val session = checkNotNull(sessions().firstOrNull { it.key == key })
        val request = AgentStartRequest(key.machineId, session.cwd, session.launcher.orEmpty(),
            permissionMode = session.permissionMode, effort = session.effort, resumeSessionId = key.sessionId)
        when (val result = checkNotNull(creation()).start(request, UUID.randomUUID().toString())) {
            is AgentStartResult.Created -> Unit
            is AgentStartResult.Rejected -> throw TerminalResumeFailed(result.message.shown)
            AgentStartResult.Pending, AgentStartResult.Unknown -> error("No answer from the hub")
        }
    }
}

/**
 * What the terminal page's Resume can bring back (#4345 / #5565): a stopped Agent, a Codex thread with a terminal, or a
 * Claude Code session that isn't running (as iOS `TerminalPanel.canResume`).
 */
internal fun resumableInTerminal(session: Session): Boolean {
    if (session.assistant || session.starting) return false
    return session.isStoppedAgent ||
        (session.sourceKind == "codex_history" && session.canTerminal) ||
        (!session.hasSource && session.isClaude && session.cwd.isNotBlank())
}
