package persona.core.designsystem

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AgentModel

class ModelSheetTest {
    @Test fun formattingDoesNotInventADetailLine() {
        assertNull(AgentModel("gpt-6.1-sol").detailLine())
        assertNull(AgentModel("gpt-6.1-sol", "GPT-6.1-sol").detailLine())
        assertEquals("gpt-6.1-sol", AgentModel("gpt-6.1-sol", "主力模型").detailLine())
        assertEquals("gpt-6.1-sol → gpt-6.1", AgentModel("gpt-6.1-sol", "主力模型", resolved = "gpt-6.1").detailLine())
        assertEquals("gpt-6.1-sol", AgentModel("sol", "Sol", resolved = "gpt-6.1-sol").detailLine())
    }

    @Test fun searchesFriendlyLabelsAndOriginalFields() {
        val model = AgentModel("gpt-6.1-sol")
        assertTrue(model.matches("GPT-6.1 Sol"))
        assertTrue(model.matches(" gpt-6.1-sol "))
        assertTrue(model.matches("6.1 SOL"))
        assertTrue(model.matches(""))
        assertFalse(model.matches("gpt-6.1-mini"))
        val alias = model.copy(name = "主力模型", resolved = "gpt-6.1", description = "日常任务")
        listOf("主力", "gpt-6.1-sol", "gpt-6.1", "日常").forEach { assertTrue(alias.matches(it)) }
    }

    @Test fun customSelectionsRemainLiteralIds() {
        val models = listOf(AgentModel("gpt-6.1-sol"))
        assertNull(customModelId(models, "gpt-6.1-sol"))
        assertNull(customModelId(models, "GPT-6.1 Sol"))
        assertNull(customModelId(models, " "))
        assertEquals("unlisted-model", customModelId(models, " unlisted-model "))
    }
}
