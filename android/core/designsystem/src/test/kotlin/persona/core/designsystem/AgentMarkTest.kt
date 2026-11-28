package persona.core.designsystem

import androidx.compose.foundation.layout.Column
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session

/** Which mark an agent's row draws and whether it carries the command line's </> badge (#6390), as iOS and the web. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class AgentMarkTest {
    @get:Rule val rule = createComposeRule()

    private fun session(agent: String? = null, surface: String? = null, source: String? = null, assistant: Boolean = false, label: String? = null) =
        Session(sessionId = "s", agent = agent, agentSurface = surface, sourceKind = source, assistant = assistant, agentLabelHint = label)

    @Test fun claudeAndAnOlderHubAreClaude() {
        for (agent in listOf(null, "", "claude")) assertEquals(AgentMarkKind.Claude, session(agent).agentMark?.kind)
    }

    @Test fun codexFromATerminalOrItsAppIsCodex() {
        assertEquals(AgentMarkKind.Codex, session("codex").agentMark?.kind)
        assertEquals(AgentMarkKind.Codex, session("claude", source = "codex_history").agentMark?.kind)
        assertEquals(AgentMarkKind.Codex, session(source = "codex_app_server").agentMark?.kind)
    }

    @Test fun traeHasItsMarkAndOthersTheirLetter() {
        assertEquals(AgentMarkKind.Trae, session("trae").agentMark?.kind)
        assertEquals(AgentMarkSpec(AgentMarkKind.Trae, "TRAE CLI", null), session("trae-cli").agentMark)
        val traex = session("traex").agentMark!!
        assertEquals(AgentMarkKind.Trae, traex.kind)
        assertEquals("TRAE CLI", traex.name)
        assertEquals(AgentMarkSpec(AgentMarkKind.Trae, "TRAE CLI", null), session("traecli").agentMark)
        assertEquals(AgentMarkKind.Dsh, session("dsh", label = "DSH").agentMark?.kind)
    }

    /**
     * #8143: Paseo calls every provider but Claude Code and Codex "paseo"; one whose launcher or label names a mark the
     * design system has draws it, any other its own name's letter, never the engine's.
     */
    @Test fun aPaseoProviderTakesTheMarkItsLauncherOrLabelNames() {
        fun paseo(label: String?, launcher: String?) =
            Session(sessionId = "paseo-1", agent = "paseo", agentLabelHint = label, launcher = launcher, sourceKind = "runtime").agentMark!!
        assertEquals(AgentMarkSpec(AgentMarkKind.Trae, "TRAE CLI", null), paseo("TRAE CLI", "traecli"))
        assertEquals(AgentMarkKind.Trae, paseo(null, "traecli").kind)
        assertEquals(AgentMarkKind.Trae, paseo("Trae Agent", "my-agent").kind)
        assertEquals(AgentMarkKind.Claude, paseo("Claude Code (ACP)", "claude-acp").kind)
        val grok = paseo("My Grok", "grok")
        assertEquals(AgentMarkKind.Grok, grok.kind)
        assertEquals("My Grok", grok.name)
        assertEquals("grok", paseo("Paseo", "grok").name)
        // Its Claude Code and Codex providers are those agents'.
        assertEquals(AgentMarkKind.Claude, Session(sessionId = "s", agent = "claude", launcher = "my-claude", sourceKind = "runtime").agentMark?.kind)
        assertEquals(AgentMarkKind.Codex, Session(sessionId = "s", agent = "codex", launcher = "my-codex", sourceKind = "runtime").agentMark?.kind)
    }

    @Test fun knownMarksGoByTheFirstWordOfAnyName() {
        assertEquals(listOf(AgentMarkKind.Claude, AgentMarkKind.Claude, AgentMarkKind.Codex, AgentMarkKind.Trae, AgentMarkKind.Trae, AgentMarkKind.Trae, AgentMarkKind.Trae),
            listOf("claude", "Claude-Code", "codex", "traex", "TRAE CLI", "trae_cli", " traecli ").map(::knownAgentMark))
        assertEquals(listOf(null, null, null, null),
            listOf(null, "", "unknown harness", "-trae").map(::knownAgentMark))
        assertEquals(listOf(AgentMarkKind.Grok, AgentMarkKind.Grok, AgentMarkKind.Antigravity, AgentMarkKind.Kimi, AgentMarkKind.Copilot),
            listOf("Grok · Official", "Grok Build", "Google Antigravity", "Kimi CLI", "GitHub Copilot CLI").map(::knownAgentMark))
        assertEquals(AgentMarkKind.OpenCode, session("opencode").agentMark?.kind)
        assertEquals(AgentMarkKind.Pi, session("pi", label = "My pi profile").agentMark?.kind)
        assertEquals(AgentMarkKind.Antigravity, Session(sessionId = "s", agent = "agy", launcher = "my-remote-launcher").agentMark?.kind)
        assertEquals(AgentMarkKind.Grok, Session(sessionId = "s", agent = "grok", launcher = "claude").agentMark?.kind)
    }

    @Test fun decorativeInitialFitsTheSameTileAtMaximumTextSize() {
        val fontScale = mutableFloatStateOf(1f)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale.floatValue)) {
                PersonaTheme { JAgentMark(AgentMarkSpec(null, "studio-runtime", null)) }
            }
        }
        fun initialWidth(): Float {
            val result = mutableListOf<TextLayoutResult>()
            val node = rule.onNodeWithText("S", useUnmergedTree = true).fetchSemanticsNode()
            val _ = node.config[SemanticsActions.GetTextLayoutResult].action!!.invoke(result)
            return result.single().getBoundingBox(0).width
        }
        val normal = initialWidth()
        rule.runOnIdle { fontScale.floatValue = 2f }
        assertEquals(normal, initialWidth(), 1f)
    }

    @Test fun theClientComesFromAgentSurface() {
        assertEquals(AgentSurface.Cli, session("claude", "cli").agentMark?.surface)
        assertEquals(AgentSurface.Desktop, session("codex", "desktop").agentMark?.surface)
        assertNull(session("codex", "sdk").agentMark?.surface)
        assertNull(session("codex").agentMark?.surface)
    }

    @Test fun personasOwnConversationsHaveNone() {
        assertNull(session(assistant = true).agentMark)
    }

    @Test fun theMarkSaysTheAgentAndItsClient() {
        rule.setContent {
            Column {
                for (style in AppStyle.entries) PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                    JAgentMark(AgentMarkSpec(AgentMarkKind.Claude, "Claude Code", AgentSurface.Cli), Modifier.testTag("cli-$style"))
                    JAgentMark(AgentMarkSpec(AgentMarkKind.Codex, "Codex", AgentSurface.Desktop), Modifier.testTag("desktop-$style"))
                    JAgentMark(AgentMarkSpec(null, "amp", null), Modifier.testTag("letter-$style"))
                }
            }
        }
        // Each style draws one of each: the three styles say the same.
        for ((said, n) in listOf("Claude Code（命令行版）" to 3, "Codex（桌面版）" to 3, "amp" to 3)) {
            rule.onAllNodesWithContentDescription(said, useUnmergedTree = true).assertCountEquals(n)
        }
    }
}
