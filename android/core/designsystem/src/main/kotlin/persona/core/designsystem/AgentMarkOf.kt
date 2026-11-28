package persona.core.designsystem

import persona.core.contracts.Session

/**
 * The mark of a session's agent (#6390), the same reading as iOS `AgentKind` and the web's `sessionAgent`: a Codex
 * thread from an app-server or history is Codex whatever it reports; "" (an older hub) is Claude Code; an agent the app
 * does not know by its id (a runtime's; Paseo's other providers, all "paseo") takes the mark its launcher or its label
 * names ([knownAgentMark], #8143), else it is named by [Session.agentLabel] and drawn as its letter. Persona's own
 * conversations get none.
 */
val Session.agentMark: AgentMarkSpec?
    get() {
        if (assistant) return null
        val surface = when (agentSurface) {
            "desktop" -> AgentSurface.Desktop
            "cli" -> AgentSurface.Cli
            else -> null
        }
        return when {
            isCodex -> AgentMarkSpec(AgentMarkKind.Codex, "Codex", surface)
            agent == "traex" || agent == "traecli" || agent == "trae-cli" -> AgentMarkSpec(AgentMarkKind.Trae, agentLabel, surface)
            agent.equals("trae", ignoreCase = true) -> AgentMarkSpec(AgentMarkKind.Trae, "TRAE", surface)
            isClaude -> AgentMarkSpec(AgentMarkKind.Claude, "Claude Code", surface)
            else -> AgentMarkSpec(knownAgentMark(agent) ?: knownAgentMark(launcher) ?: knownAgentMark(agentLabelHint), agentLabel, surface)
        }
    }

/**
 * The mark the design system has for an agent named [name] (an agent's id, a launcher's name, a provider's label), by
 * its product name or an alias. Older labels such as "Claude Code (ACP)" keep their first-word lookup.
 */
fun knownAgentMark(name: String?): AgentMarkKind? {
    val product = name?.substringBefore('·')?.trim()?.lowercase() ?: return null
    if (product.firstOrNull()?.isLetterOrDigit() != true) return null
    val key = product.filterNot { it.isWhitespace() || it in ".-_" }
    return MARKS[key] ?: LEGACY_MARKS[product.takeWhile(Char::isLetterOrDigit)]
}

private val LEGACY_MARKS = mapOf(
    "claude" to AgentMarkKind.Claude, "claudecode" to AgentMarkKind.Claude,
    "codex" to AgentMarkKind.Codex,
    "trae" to AgentMarkKind.Trae, "traex" to AgentMarkKind.Trae, "traecli" to AgentMarkKind.Trae,
)

private val MARKS = LEGACY_MARKS + mapOf(
    "codexcli" to AgentMarkKind.Codex, "traeagent" to AgentMarkKind.Trae,
    "opencode" to AgentMarkKind.OpenCode,
    "pi" to AgentMarkKind.Pi, "piweb" to AgentMarkKind.Pi,
    "gemini" to AgentMarkKind.Gemini, "geminicli" to AgentMarkKind.Gemini,
    "cursor" to AgentMarkKind.Cursor, "cursoragent" to AgentMarkKind.Cursor,
    "grok" to AgentMarkKind.Grok, "grokbuild" to AgentMarkKind.Grok,
    "grokchat" to AgentMarkKind.Grok, "grokcli" to AgentMarkKind.Grok,
    "agy" to AgentMarkKind.Antigravity, "antigravity" to AgentMarkKind.Antigravity,
    "antigravitycli" to AgentMarkKind.Antigravity, "googleantigravity" to AgentMarkKind.Antigravity,
    "dsh" to AgentMarkKind.Dsh, "deepseekharness" to AgentMarkKind.Dsh,
    "kimi" to AgentMarkKind.Kimi, "kimicli" to AgentMarkKind.Kimi,
    "copilot" to AgentMarkKind.Copilot, "copilotcli" to AgentMarkKind.Copilot,
    "githubcopilot" to AgentMarkKind.Copilot, "githubcopilotcli" to AgentMarkKind.Copilot,
)
