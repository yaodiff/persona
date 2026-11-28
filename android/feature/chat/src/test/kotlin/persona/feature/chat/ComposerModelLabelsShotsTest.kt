package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.designsystem.*
import persona.core.domain.sort.canChangeEffortOnly
import persona.core.domain.sort.canChangeModes
import java.io.File

/** The actual composer and its production hint helper, rendered with fake session data and no service. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class ComposerModelLabelsShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun currentGptLabelInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var session by mutableStateOf(Session(machineId = "box", sessionId = "main", assistant = true,
            model = "gpt-6.1-sol", permissionMode = "default", effort = "high"))
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).height(600.dp).background(PersonaTheme.palette.background)) {
                        JSectionHeader("Persona 主对话", modifier = Modifier.padding(Tokens.Space.l))
                        Spacer(Modifier.weight(1f))
                        ChatComposer(session.key, rememberChatComposerState(), working = false, sending = false,
                            canClear = false, service = null, onSend = {}, onCommand = {}, onNotice = {},
                            modeHint = chatModeHint(session, includeModes = session.canChangeModes || session.canChangeEffortOnly))
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("GPT-6.1 Sol").assertExists()
            rule.onNodeWithText("gpt-6.1-sol").assertDoesNotExist()
            rule.onNodeWithText("输入消息").assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var other = 0
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
                if (bitmap.getPixel(x, y) != background) other++
            }
            assertTrue("$s/$d drew nothing", other > 100)
            val out = File(System.getProperty("persona.shots") ?: "build/composer-model-label-shots").apply { mkdirs() }
            File(out, "composer-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        session = session.copy(model = "gpt-6.1")
        rule.onNodeWithText("GPT-6.1").assertExists()
        rule.onNodeWithText("GPT-6.1 Sol").assertDoesNotExist()
        session = session.copy(model = "claude-opus-5-5")
        rule.onNodeWithText("GPT-6.1").assertDoesNotExist()
        rule.onNodeWithText("claude-opus-5-5").assertDoesNotExist()
        session = session.copy(model = null)
        rule.onNodeWithText("输入消息").assertExists()
    }

    @Test fun declaredModeCapabilityControlsTheRealModeBarInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var session by mutableStateOf(Session(machineId = "box", sessionId = "desktop", agent = "claude_desktop",
            sourceKind = "claude_desktop", status = "idle", permissionMode = "default", effort = "high",
            capabilities = SessionCapabilities(read = true, send = true, modes = false)))
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("mode-shot").width(400.dp).height(180.dp).background(PersonaTheme.palette.background)) {
                        val scope = rememberCoroutineScope()
                        val resources = LocalResources.current
                        val chrome = remember { ChatChromeState(resources, null, scope, {}, {}) }
                        if (session.canChangeModes) ChatModeBar(session, chrome, online = true)
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/mode-capability-shots").apply { mkdirs() }
        for (name in listOf("desktop", "agy")) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            session = session.copy(sessionId = name, agent = if (name == "agy") "agy" else "claude",
                agentLabelHint = "Antigravity".takeIf { name == "agy" },
                sourceKind = if (name == "agy") "runtime" else "claude_desktop",
                capabilities = SessionCapabilities(read = true, send = true, modes = name == "agy"))
            style = s; dark = d
            rule.waitForIdle()
            if (name == "agy") rule.onNodeWithContentDescription("模式").assertExists()
            else rule.onNodeWithContentDescription("模式").assertDoesNotExist()
            val bitmap = rule.onNodeWithTag("mode-shot").captureToImage().asAndroidBitmap()
            File(out, "mode-capability-$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}
