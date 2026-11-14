package persona.core.domain

import org.junit.Assert.assertEquals
import org.junit.Test

/** The model's short name reads as the web's `modelChipLabel` (`web/src/lib/tasks.ts`). */
class ModelNamesTest {
    @Test fun shortNames() {
        assertEquals("Haiku 4.5", modelShortName("claude-haiku-4-5-20251001"))
        assertEquals("Opus 4.8", modelShortName("claude-opus-4-8[1m]"))
        assertEquals("Opus 5.5", modelShortName("claude-opus-5-5[1m]"))
        assertEquals("Fable 5.2", modelShortName("claude-fable-5-2[1m]"))
        assertEquals("Sonnet 5.5", modelShortName("claude-sonnet-5-5[1m]"))
        assertEquals("Opus 5.5", modelShortName("claude-opus-5-5-20260101"))
        assertEquals("Haiku 5.5", modelShortName("claude-haiku-5-5"))
        assertEquals("Sonnet 4", modelShortName("us.anthropic.claude-sonnet-4"))
        assertEquals("Opus 4.1", modelShortName("gateway/claude-opus-4-1"))
        assertEquals("GPT-5.4 Mini", modelShortName("gpt-5.4-mini"))
        assertEquals("GPT-5 Codex", modelShortName("gpt-5-codex"))
        assertEquals("GPT-5.1 Codex Max", modelShortName("gpt-5.1-codex-max"))
        assertEquals("GPT-5", modelShortName("gpt-5"))
        assertEquals("Kimi K2", modelShortName("kimi-k2"))
        assertEquals("", modelShortName(null))
        assertEquals("", modelShortName("  "))
    }
}
