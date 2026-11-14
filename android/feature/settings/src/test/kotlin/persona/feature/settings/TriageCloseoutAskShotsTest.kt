package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AgentTriage
import persona.core.contracts.api.TriageSwitches
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** Native JVM renders of the real Session triage page; these are not emulator or device captures. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class TriageCloseoutAskShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class FakeTriage(private var current: TriageSwitches) : AgentTriage {
        override suspend fun switches() = current
        override suspend fun put(value: TriageSwitches): TriageSwitches {
            current = value
            return value
        }
    }

    private fun show(
        api: AgentTriage,
        style: () -> AppStyle,
        dark: () -> Boolean,
        scale: () -> Float = { 1f },
        width: Dp = 400.dp,
        height: Dp = 1800.dp,
    ) {
        rule.setContent {
            val density = LocalDensity.current.density
            CompositionLocalProvider(LocalDensity provides Density(density, scale())) {
                PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                    Box(Modifier.testTag("shot").width(width).height(height).background(PersonaTheme.palette.background)) {
                        TriageSettingsPage(triage = api, models = null, onBack = {}, onModel = {})
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun capture(name: String): Bitmap {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val out = File("build/triage-closeout-ask-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use {
            assertTrue("could not write $name", bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
        return bitmap
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1900dp-xhdpi")
    @Test fun englishMatrix() = matrix("en", "Ask for the status afterwards")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1900dp-xhdpi")
    @Test fun simplifiedChineseMatrix() = matrix("zh", "事后问一次进展")

    private fun matrix(language: String, title: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        show(FakeTriage(TriageSwitches(enabled = true, closeout = true, closeoutAsk = true)),
            style = { style }, dark = { dark })
        val row = rule.onNodeWithTag("triage-closeout-ask")
        rule.onNodeWithText(title).assertExists()

        for (look in AppStyle.entries) {
            var lightOn: Bitmap? = null
            for (night in listOf(false, true)) {
                style = look; dark = night
                Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeBy(1_000); rule.waitForIdle()
                val variant = "$language-${look.name.lowercase()}-${if (night) "dark" else "light"}"
                row.assertIsOn()
                val on = capture("triage-closeout-ask-$variant-on")
                if (night) assertFalse("light and dark must differ for $look", on.sameAs(lightOn!!)) else lightOn = on

                row.performClick(); rule.mainClock.advanceTimeBy(1_000); rule.waitForIdle()
                row.assertIsOff()
                val off = capture("triage-closeout-ask-$variant-off")
                assertFalse("on and off must differ for $variant", on.sameAs(off))

                row.performClick(); rule.mainClock.advanceTimeBy(1_000); rule.waitForIdle()
                row.assertIsOn()
            }
        }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1900dp-xhdpi")
    @Test fun anOldHubHasNoRow() {
        show(FakeTriage(TriageSwitches(enabled = true, closeout = true)),
            style = { AppStyle.Glass }, dark = { false })
        rule.onNodeWithTag("triage-closeout").assertExists()
        rule.onNodeWithTag("triage-closeout-ask").assertDoesNotExist()
        capture("triage-closeout-ask-en-old-hub-no-row")
    }

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1900dp-xhdpi")
    @Test fun japaneseUsesTheSharedWording() {
        show(FakeTriage(TriageSwitches(enabled = true, closeout = true, closeoutAsk = true)),
            style = { AppStyle.Glass }, dark = { false })
        rule.onNodeWithText("あとから状況を聞く").assertExists()
        rule.onNodeWithTag("triage-closeout-ask").assertIsOn()
        capture("triage-closeout-ask-ja-glass-light-on")
    }

    @Config(sdk = [35], qualifiers = "en-w640dp-h4000dp-xhdpi", fontScale = 2f)
    @Test fun largeEnglishTextKeepsTheWholeTitle() {
        var scale by mutableStateOf(1f)
        show(FakeTriage(TriageSwitches(enabled = true, closeout = true, closeoutAsk = true)),
            style = { AppStyle.Glass }, dark = { false }, scale = { scale }, width = 600.dp, height = 3900.dp)
        val default = capture("triage-closeout-ask-en-glass-light-on-default-text")

        scale = 2f
        Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeBy(1_000); rule.waitForIdle()
        val large = capture("triage-closeout-ask-en-glass-light-on-large-text")
        assertFalse("default and large text must render differently", default.sameAs(large))

        val layouts = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText("Ask for the status afterwards", useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        assertCompleteSingleLineTitle(layouts.single())
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h4000dp-xhdpi", fontScale = 2f)
    @Test fun largeChineseTextKeepsTheWholeTitle() {
        var scale by mutableStateOf(1f)
        show(FakeTriage(TriageSwitches(enabled = true, closeout = true, closeoutAsk = true)),
            style = { AppStyle.Glass }, dark = { false }, scale = { scale }, height = 3900.dp)
        val default = capture("triage-closeout-ask-zh-glass-light-on-default-text")

        scale = 2f
        Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeBy(1_000); rule.waitForIdle()
        val large = capture("triage-closeout-ask-zh-glass-light-on-large-text")
        assertFalse("default and large text must render differently", default.sameAs(large))

        val layouts = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText("事后问一次进展", useUnmergedTree = true).fetchSemanticsNode()
            .config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        val title = layouts.single()
        assertCompleteSingleLineTitle(title)
    }

    private fun assertCompleteSingleLineTitle(title: TextLayoutResult) {
        // Check actual truncation: the broad overflow flag can be true although the one rendered line is complete.
        assertEquals("the large title stays on one line", 1, title.lineCount)
        assertFalse("the large title must not ellipsize", title.isLineEllipsized(0))
        assertTrue("the last glyph stays inside the title bounds", title.getLineRight(0) <= title.size.width)
    }
}
