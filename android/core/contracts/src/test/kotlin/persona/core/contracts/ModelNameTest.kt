package persona.core.contracts

import kotlinx.serialization.encodeToString
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.modelName

class ModelNameTest {
    @Test fun formatsGptVersionsAndEveryVariantWithoutACatalog() {
        val cases = mapOf(
            "gpt-6.1-sol" to "GPT-6.1 Sol",
            "gpt-6.1" to "GPT-6.1",
            "gpt-4o-mini" to "GPT-4o Mini",
            "gpt-6.1-codex-max" to "GPT-6.1 Codex Max",
            "GPT-6.1-SOL" to "GPT-6.1 SOL",
            "gpt-4.1-mini-2025-04-14" to "GPT-4.1 Mini 2025 04 14",
        )
        cases.forEach { (id, expected) ->
            assertEquals(id, expected, modelName(id))
            assertEquals(expected, AgentModel(id).label)
            assertEquals(expected, AgentModel(id, id).label)
        }
        assertEquals("GPT-6.1 Sol", (AgentModel("GPT-6.1-SOL", "gpt-6.1-sol").label).zhAny())
    }

    @Test fun explicitAliasesTakePrecedenceEvenWhenTheyLookLikeGptIds() {
        assertEquals("我的主力模型", (AgentModel("gpt-6.1-sol", "我的主力模型").label).zhAny())
        assertEquals("GPT-6.1 Pro", (AgentModel("gpt-6.1-sol", "GPT-6.1 Pro").label).zhAny())
        assertEquals("gpt-6.1", (AgentModel("gpt-6.1-sol", "gpt-6.1").label).zhAny())
        assertEquals(" GPT-6.1-sol ", (AgentModel("gpt-6.1-sol", " GPT-6.1-sol ").label).zhAny())
        assertEquals("Sol", (AgentModel("gpt-6.1-sol", "Sol", resolved = "gpt-6.1-sol").label).zhAny())
    }

    @Test fun nonGptUnknownAndEmptyNamesKeepTheirFallbacks() {
        listOf("claude-opus-5-5", "deepseek/v4.1", "o3-mini", "gateway/gpt-6.1", "unknown", "gpt", "gpt-", "gpt-6.1--sol", " gpt-6.1", "gpt-6.1[1m]").forEach { id ->
            assertEquals(id, modelName(id))
            assertEquals(id, AgentModel(id).label)
        }
        assertEquals("Claude Opus", (AgentModel("claude-opus-5-5", "Claude Opus").label).zhAny())
        assertEquals("", (modelName(null)).zhAny())
        assertEquals("", (modelName("")).zhAny())
        assertEquals("", (AgentModel("").label).zhAny())
        assertEquals("GPT-6.1 Sol", (AgentModel("gpt-6.1-sol", " \t").label).zhAny())
        assertEquals(" \t", (modelName(" \t")).zhAny())
    }

    @Test fun labelsNeverChangeModelIdsOrSerializedRequests() {
        val model = JsonCodec.decodeFromString<AgentModel>("""{"id":"gpt-6.1-sol","name":"GPT-6.1-sol"}""")
        val encoded = JsonCodec.encodeToString(model)
        assertEquals("GPT-6.1 Sol", (model.label).zhAny())
        assertEquals("gpt-6.1-sol", (model.id).zhAny())
        assertEquals("GPT-6.1-sol", (model.name).zhAny())
        assertEquals(model, JsonCodec.decodeFromString<AgentModel>(encoded))
        assertEquals(encoded, JsonCodec.encodeToString(model))
        val request = AgentStartRequest("box", "/srv/app", "test-launcher", model = model.id)
        assertEquals("gpt-6.1-sol", (JsonCodec.decodeFromString<AgentStartRequest>(JsonCodec.encodeToString(request)).model).zhAny())
    }
}
