package persona.core.domain.answer

import persona.core.domain.zhAny

import persona.core.contracts.Interaction
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** "Always allow" is Claude's; another agent's card offers it only with always_label (iOS AgentPermissionForm). */
class AlwaysOfferTest {
    private val card = Interaction("ix", machineId = "m", sessionId = "s", kind = "permission", toolName = "Bash")

    @Test fun claudesCardsOfferItUnderTheirOwnName() {
        assertTrue(card.offersAlways)
        assertTrue(card.copy(agent = "claude").offersAlways)
        assertEquals("本会话总是允许", (card.alwaysActionLabel).zhAny())
    }

    @Test fun scopeComesFromTheRememberedRuleRatherThanTheDisplayedCommand() {
        assertEquals("这段对话里的所有命令", card.copy(alwaysKeys = listOf("tool:Bash")).alwaysScopeDetail.zhAny())
        assertEquals("这段对话里的所有命令", card.alwaysScopeDetail.zhAny())
        val read = card.copy(toolName = "Read", alwaysKeys = listOf("tool:Read(//workspace/docs/**)"))
        assertEquals("这段对话中，这个目录里的所有读取操作", read.alwaysScopeDetail.zhAny())
        for (tool in listOf("Edit", "Write", "MultiEdit", "NotebookEdit")) {
            val edit = card.copy(toolName = tool, alwaysKeys = listOf("tool:Edit(//workspace/docs/**)"))
            assertEquals("这段对话中，这个目录里的所有文件编辑", edit.alwaysScopeDetail.zhAny())
        }
        assertEquals("这段对话里的所有「Read」调用", read.copy(alwaysKeys = null).alwaysScopeDetail.zhAny())
        assertEquals("批准的范围在这段对话中持续有效", read.copy(alwaysKeys = listOf("lark:POST /conversations/*/messages")).alwaysScopeDetail.zhAny())
        assertEquals("Only this command", card.copy(agent = "codex", alwaysLabel = "Only this command").alwaysScopeDetail.zhAny())
        assertEquals(null, card.copy(agent = "codex").alwaysScopeDetail)
    }

    @Test fun genericToolScopeChoosesTheNameForEveryCall() {
        val report = card.copy(
            toolName = "mcp__persona__report_to_supervisor",
            toolTitle = "Report to supervisor · Done", toolNameTitle = "Report to supervisor",
        )
        assertEquals("这段对话里的所有「Report to supervisor」调用", report.alwaysScopeDetail.zhAny())
        assertEquals("这段对话里的所有「Report to supervisor · Done」调用",
            report.copy(toolNameTitle = " ").alwaysScopeDetail.zhAny())
        assertEquals("这段对话里的所有「Persona · report to supervisor」调用",
            report.copy(toolTitle = " ", toolNameTitle = " ").alwaysScopeDetail.zhAny())
        assertEquals("这段对话里的所有「工具」调用",
            card.copy(toolName = null, toolTitle = " ", toolNameTitle = " ").alwaysScopeDetail.zhAny())
        assertEquals("All Report to supervisor calls in this conversation", report.alwaysScopeDetail?.english())
    }

    @Test fun otherAgentsNeedTheirTuiToNameWhatItAllows() {
        assertFalse(card.copy(agent = "codex").offersAlways)
        assertFalse(card.copy(agent = "traex", alwaysLabel = " ").offersAlways)
        assertFalse(card.copy(agent = "dsh", agentLabelHint = "DSH").offersAlways)
        val labelled = card.copy(agent = "codex", alwaysLabel = "以后不再问以 curl -s 开头的命令")
        assertTrue(labelled.offersAlways)
        assertEquals("总是同意", (labelled.alwaysActionLabel).zhAny())
        val always = ApprovalPolicy.actions(AnswerEntry.Conversation, "permission", false).single { it.id == ApprovalAction.Always }
        assertTrue(AnswerRequirement.AlwaysOffered in always.requirements)
        assertFalse(AnswerBuilder.build(card.copy(agent = "codex"), AnswerAction.Always).allowed)
        assertTrue(AnswerBuilder.build(labelled, AnswerAction.Always).allowed)
    }

    /**
     * #8143: a Paseo card naming only the engine (an older node's) can't name its agent, yet it stays another agent's card:
     * no "Always allow in this session", Claude's alone, and "Always allow" only with what its TUI says it allows.
     */
    @Test fun aPaseoCardThatCantNameItsAgentIsStillAnotherAgentsCard() {
        for (paseo in listOf(card.copy(agent = "paseo", agentLabelHint = "Paseo"), card.copy(agent = "paseo", agentLabelHint = "Grok"))) {
            assertFalse(paseo.offersAlways)
            assertFalse(AnswerBuilder.build(paseo, AnswerAction.Always).allowed)
            assertEquals(null, paseo.alwaysScopeDetail)
            val labelled = paseo.copy(alwaysLabel = "Only this command")
            assertTrue(labelled.offersAlways)
            assertEquals("总是同意", (labelled.alwaysActionLabel).zhAny())
        }
    }

    /** #3590 / #3941: where the manager says whether it keeps an "always allow", that decides, over the card's own rule. */
    @Test fun theManagersAlwaysHonoredDecides() {
        val own = card.copy(toolName = "iphone_do", alwaysHonored = false)
        assertFalse(own.offersAlways)
        assertFalse(AnswerBuilder.build(own, AnswerAction.Always).allowed)
        assertTrue(AnswerBuilder.build(own, AnswerAction.Allow).allowed)
        val codex = card.copy(agent = "codex", alwaysHonored = true)
        assertTrue(codex.offersAlways)
        assertTrue(AnswerBuilder.build(codex, AnswerAction.Always).allowed)
        assertFalse(card.copy(agent = "codex", alwaysLabel = "Only this command", alwaysHonored = false).offersAlways)
        assertTrue(card.copy(alwaysHonored = null).offersAlways)
        assertFalse(card.copy(agent = "codex", alwaysHonored = null).offersAlways)
    }

    @Test fun runtimeAlwaysKeysExplainScopeWhenManagerHonorsAlways() {
        val agyWrite = card.copy(
            agent = "antigravity", agentLabelHint = "Antigravity", toolName = "Write",
            alwaysHonored = true, alwaysKeys = listOf("tool:Edit(//workspace/docs/**)"),
        )
        assertTrue(agyWrite.offersAlways)
        assertEquals("这段对话中，这个目录里的所有文件编辑", agyWrite.alwaysScopeDetail.zhAny())
        val agyRead = agyWrite.copy(toolName = "Read", alwaysKeys = listOf("tool:Read(//workspace/docs/**)"))
        assertEquals("这段对话中，这个目录里的所有读取操作", agyRead.alwaysScopeDetail.zhAny())
        val agyBash = agyWrite.copy(toolName = "Bash", alwaysKeys = listOf("tool:Bash"))
        assertEquals("这段对话里的所有命令", agyBash.alwaysScopeDetail.zhAny())
        // Without always_keys or always_label, non-Claude cards do not invent a whole-tool scope.
        assertEquals(null, card.copy(agent = "antigravity", alwaysHonored = true).alwaysScopeDetail)
    }
}
