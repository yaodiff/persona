package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxHandling
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator

/** The failed handling line and instruction sheet in every style, light / dark and at 1x / 2x. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-mdpi")
class InboxSaySomethingShotsTest {
    @get:Rule val rule = createComposeRule()

    @Config(qualifiers = "zh-w400dp-h1100dp-mdpi")
    @Test fun retryAtOneX() = drawEvent("mdpi")

    @Config(qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun retryAtTwoX() = drawEvent("xhdpi")

    @Config(qualifiers = "zh-w400dp-h1100dp-mdpi")
    @Test fun saySheetAtOneX() = drawSheet("mdpi")

    @Config(qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun saySheetAtTwoX() = drawSheet("xhdpi")

    @Test fun processingAndSayShareTheEventStateLine() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val repository = Repository(record("current", "Current", InboxHandling(state = "looking", machineId = "m1", sessionId = "s1")))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        InboxEventScreen(repository, InboxEventRoute("current"), navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("处理对话 ›").assertExists()
            rule.onNodeWithText("说一句").assertExists()
            assertTrue("processing comes before Say something", rule.onNodeWithText("处理对话 ›").fetchSemanticsNode().boundsInRoot.left <
                rule.onNodeWithText("说一句").fetchSemanticsNode().boundsInRoot.left)
            save("inbox-processing-say-${s.name.lowercase()}-${mode(d)}", rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())
        }
    }

    private fun drawEvent(density: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val repository = Repository(record("current", "Current", InboxHandling(state = "failed", error = "timeout")))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        InboxEventScreen(repository, InboxEventRoute("current"), navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("再试一次").assertExists()
            save("inbox-retry-${s.name.lowercase()}-${mode(d)}-$density", rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())
        }
    }

    private fun drawSheet(density: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val record = record("current", "Current", InboxHandling(state = "looking"))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                InboxSaySomething(Repository(record), record, mark = null, onStale = {})
            }
        }
        rule.onNodeWithText("说一句").performClick()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("告诉 AI").assertExists()
            save("inbox-say-sheet-${s.name.lowercase()}-${mode(d)}-$density", rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
        }
    }

    private fun mode(dark: Boolean) = if (dark) "dark" else "light"
    private fun save(name: String, bitmap: Bitmap) {
        assertTrue("$name drew nothing", bitmap.width > 0 && bitmap.height > 0)
        val directory = File(System.getProperty("persona.shots") ?: "build/inbox-say-shots").also { it.mkdirs() }
        File(directory, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: persona.core.navigation.AppTab) = Unit
    }

    private class Repository(record: InboxRecord) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record)))
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("unused")
    }

    companion object {
        private fun record(id: String, title: String, handling: InboxHandling) = InboxRecord(
            InboxItem(id, title = title, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action", handling = handling))
    }
}
