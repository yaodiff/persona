package persona.feature.agents

import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentWorkspace
import persona.core.contracts.api.DefaultOpens
import persona.core.contracts.api.ModelBinding
import persona.core.contracts.api.ModelPurpose
import persona.core.contracts.api.ModelPurposeDetail

/**
 * New Agent says where a start goes when the computer starts new Agents as Orca chats (#17650): the hub's `default_opens`
 * while the start keeps the default; nothing once a launcher, a preset or a model is picked, in plan mode or a mode the chat
 * does not take, with ultracode, in another engine's workspace, or from a hub that says nothing.
 */
class NewAgentOrcaChatTest {
    private val opens = DefaultOpens("orca-claude", "Orca chat · Claude Code", listOf("default", "acceptEdits", "auto", "bypassPermissions"))
    private val keeps = NewAgentState(initialized = true, machineId = "computer", selection = AgentDefaults("/code", ""),
        launchers = listOf(AgentLauncher("claude"), AgentLauncher("orca-claude")), defaultOpens = opens,
        modelConfig = NewAgentModelConfig.Follow(ModelPurposeDetail(ModelPurpose("agent.new", effective = ModelBinding("launcher.claude", "")))))

    @Test fun keepingTheDefaultOpensAsTheHubsChat() {
        assertEquals(opens, keeps.opensAsOrcaChat)
        assertEquals(opens, keeps.copy(selection = keeps.selection.copy(mode = "acceptEdits")).opensAsOrcaChat)
        assertEquals(opens, keeps.copy(workspace = AgentWorkspace(source = "orca", sourceId = "wt", cwd = "/code")).opensAsOrcaChat)
    }

    @Test fun anythingElseOpensWhereItAlwaysDid() {
        assertNull(keeps.copy(defaultOpens = null).opensAsOrcaChat)
        assertNull(keeps.copy(selection = keeps.selection.copy(launcher = "claude")).opensAsOrcaChat)
        assertNull(keeps.copy(selection = keeps.selection.copy(mode = "plan")).opensAsOrcaChat)
        assertNull(keeps.copy(ultracode = true).opensAsOrcaChat)
        assertNull(keeps.copy(workspace = AgentWorkspace(source = "paseo", sourceId = "w", cwd = "/code")).opensAsOrcaChat)
        assertNull(keeps.copy(modelConfig = NewAgentModelConfig.Unsupported).opensAsOrcaChat)
        val codex = keeps.copy(defaultOpens = DefaultOpens("orca-codex", permissionModes = listOf("default", "auto", "bypassPermissions")))
        assertNull(codex.copy(selection = keeps.selection.copy(mode = "acceptEdits")).opensAsOrcaChat)
    }

    @Test fun theHubsAnswerReads() {
        val read = Json { ignoreUnknownKeys = true }.decodeFromString<DefaultOpens>(
            """{"launcher":"orca-codex","display_name":"Orca chat · Codex","permission_modes":["default","auto"],"dropped":true,"later":1}""")
        assertEquals("Orca chat · Codex", read.title)
        assertEquals(listOf("default", "auto"), read.permissionModes)
        assertTrue(read.dropped)
        assertEquals("orca-claude", DefaultOpens("orca-claude").title)
    }
}
