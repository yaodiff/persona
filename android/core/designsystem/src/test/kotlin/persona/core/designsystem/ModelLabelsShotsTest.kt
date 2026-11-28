package persona.core.designsystem

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AgentModel
import java.io.File

/** Only supplied fake models are rendered; no hub or model catalog is consulted. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1000dp-xhdpi")
class ModelLabelsShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun labelsAndSelectionsInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var selected by mutableStateOf("gpt-6.1-sol")
        var picked = ""
        val models = listOf(
            AgentModel("gpt-6.1-sol"),
            AgentModel("gpt-6.1", "GPT-6.1"),
            AgentModel("gpt-4o-mini"),
            AgentModel("gpt-6.1-codex-max"),
            AgentModel("alias", "自定义名称", resolved = "gpt-6.1-sol"),
            AgentModel("claude-opus-5-5", "Claude Opus"),
        )
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    JModelSheet("选择此对话的模型", models, selected, null, false, null,
                        onPick = { picked = it; selected = it }, onRefresh = {}, onDismiss = {}, custom = true)
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            listOf("GPT-6.1 Sol", "GPT-6.1", "GPT-4o Mini", "GPT-6.1 Codex Max", "自定义名称", "Claude Opus").forEach {
                rule.onNodeWithText(it).assertExists()
            }
            // A formatted title must not create a raw-id subtitle in an otherwise unnamed row.
            assertEquals(0, rule.onAllNodesWithText("gpt-6.1-sol").fetchSemanticsNodes().size)
            rule.onNodeWithText("alias → gpt-6.1-sol").assertExists()
            rule.onNodeWithText("GPT-6.1 Sol").performClick()
            assertEquals("gpt-6.1-sol", picked)
            rule.onNodeWithText("自定义名称").performClick()
            assertEquals("alias", picked)
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var other = 0
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
                if (bitmap.getPixel(x, y) != background) other++
            }
            assertTrue("$s/$d drew nothing", other > 100)
            val out = File(System.getProperty("persona.shots") ?: "build/model-label-shots").apply { mkdirs() }
            File(out, "models-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        selected = "gpt-4.1"
        rule.onNodeWithText("GPT-4.1").assertExists()
        rule.onNodeWithText("gpt-4.1").assertDoesNotExist()
        rule.onNodeWithText("当前配置，列表里没有").assertExists()
    }
}
