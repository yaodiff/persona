package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.Alignment
import androidx.compose.ui.test.getBoundsInRoot
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/**
 * The app's one toast (#4209, T11): its looks in every style, light and dark, for info, success, error and a notice with
 * Undo; its timing (iOS `ToastCapsule.shown`); one slot; TalkBack hears it. PNGs in `build/toast-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ToastShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/toast-shots").also { it.mkdirs() }

    private val notices = listOf(
        FeedbackNotice("Copied", style = FeedbackStyle.Success),
        FeedbackNotice("Saved to Photos"),
        FeedbackNotice("Couldn't send: the hub is out of reach", style = FeedbackStyle.Error),
        FeedbackNotice("Archived 3 items", FeedbackAction("Undo") {}),
    )

    @Test fun glassLight() = shoot(AppStyle.Glass, dark = false)
    @Test fun glassDark() = shoot(AppStyle.Glass, dark = true)
    @Test fun briefLight() = shoot(AppStyle.Brief, dark = false)
    @Test fun briefDark() = shoot(AppStyle.Brief, dark = true)
    @Test fun boardLight() = shoot(AppStyle.Board, dark = false)
    @Test fun boardDark() = shoot(AppStyle.Board, dark = true)

    private fun shoot(style: AppStyle, dark: Boolean) {
        val name = "toast-${style.name.lowercase()}-${if (dark) "dark" else "light"}"
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag(name).width(400.dp).background(PersonaTheme.palette.background).padding(vertical = 16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    for (notice in notices) JToast(notice, dismiss = {})
                }
            }
        }
        val bitmap = rule.onNodeWithTag(name).captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun oneSlotTimedAsIOSAndAnnounced() {
        val state = JToastState()
        var undone = 0
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(300.dp)) { JToastHost(state) }
            }
        }
        // Info stays 2.5 s.
        rule.runOnIdle { state.show(FeedbackNotice("Copied")); Snapshot.sendApplyNotifications() }
        rule.mainClock.advanceTimeBy(2_400)
        rule.onNodeWithText("Copied").assertIsDisplayed()
        rule.onNode(SemanticsMatcher.expectValue(SemanticsProperties.LiveRegion, LiveRegionMode.Polite)).assertIsDisplayed()
        rule.mainClock.advanceTimeBy(400)
        rule.runOnIdle { assertNull(state.current) }
        // The newest replaces the one showing; an error stays 4 s, a button 5 s.
        rule.runOnIdle { state.show(FeedbackNotice("first")); state.show(FeedbackNotice("Send failed", style = FeedbackStyle.Error)); Snapshot.sendApplyNotifications() }
        rule.mainClock.advanceTimeBy(3_900)
        rule.onNodeWithText("Send failed").assertIsDisplayed()
        rule.mainClock.advanceTimeBy(300)
        rule.runOnIdle { assertNull(state.current) }
        rule.runOnIdle { state.show(FeedbackNotice("Archived", FeedbackAction("Undo") { undone++ })); Snapshot.sendApplyNotifications() }
        rule.mainClock.advanceTimeBy(4_900)
        rule.onNodeWithText("Undo").performClick()
        rule.mainClock.advanceTimeBy(100)
        rule.runOnIdle {
            assertEquals(1, undone)
            assertNull(state.current)
        }
    }

    @Test fun toastWithTopBarClearanceClearsChatTopBarAcrossStyles() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val state = JToastState().apply { show(FeedbackNotice("Branched to a new chat")) }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("toast-scene").width(400.dp).height(240.dp).background(PersonaTheme.palette.background)) {
                    ChatTopBar(
                        title = "Migrate blog to Astro",
                        source = "Mac Studio",
                        onBack = {},
                        modifier = Modifier.testTag("top-bar"),
                    )
                    JToastHost(state, Modifier.align(Alignment.TopCenter).padding(top = ToastTopBarClearance))
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val name = "toast-clearance-${s.name.lowercase()}-${if (d) "dark" else "light"}"
            val bar = rule.onNodeWithTag("top-bar").getBoundsInRoot()
            val toast = rule.onNodeWithTag("toast").getBoundsInRoot()
            assertTrue("$name: toast top (${toast.top}) must clear top bar bottom (${bar.bottom})", toast.top >= bar.bottom)
            val bitmap = rule.onNodeWithTag("toast-scene").captureToImage().asAndroidBitmap()
            File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
