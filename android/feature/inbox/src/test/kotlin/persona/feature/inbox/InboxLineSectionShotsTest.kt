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
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.InboxParentRef
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.Navigator

/** Thread in every style, both color schemes and at 1x / 2x; PNGs go to build/inbox-line-shots (or -Dpersona.shots). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class InboxLineSectionShotsTest {
    @get:Rule val rule = createComposeRule()

    @Config(qualifiers = "zh-w400dp-h900dp-mdpi")
    @Test fun drawsAtOneX() = draw("mdpi")

    @Config(qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun drawsAtTwoX() = draw("xhdpi")

    @Test fun drawsTheEventPageWithLineageInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val current = record("current", "Current")
        val repository = Repository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), record("parent", "报价要等客户确认")),
                InboxLineAncestor(InboxParentRef("todo", "todo-1"))),
            listOf(record("child", "客户确认了报价"))))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                        InboxEventScreen(repository, InboxEventRoute("current"), navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("线索").assertExists()
            val name = "inbox-event-line-${s.name.lowercase()}-${if (d) "dark" else "light"}"
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun drawsTheSameMatterPickerInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val current = record("current", "Current")
        val tree = InboxLineage(InboxParentRef("inbox", "current"), current)
        val repository = Repository(tree, listOf(current,
            record("alpha", "发布准备", "Alex", "等客户确认"), record("beta", "周四报价", "Sam", "再核对税率")))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) { InboxSameMatterSheet(repository, current, tree, onDismiss = {}, onLinked = {}) }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            rule.onNodeWithText("是同一件事…").assertExists()
            val name = "inbox-same-matter-${s.name.lowercase()}-${if (d) "dark" else "light"}"
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun draw(density: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val current = record("current", "Current")
        val repository = Repository(InboxLineage(InboxParentRef("inbox", "current"), current,
            listOf(InboxLineAncestor(InboxParentRef("inbox", "parent"), record("parent", "报价要等客户确认")),
                InboxLineAncestor(InboxParentRef("todo", "todo-1"))),
            listOf(record("child", "客户确认了报价"))))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        InboxLineSection(repository, current, navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            val name = "inbox-line-${s.name.lowercase()}-${if (d) "dark" else "light"}-$density"
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertTrue("$name drew nothing", bitmap.width > 0 && bitmap.height > 0)
            File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-line-shots").also { it.mkdirs() }
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: persona.core.navigation.AppTab) = Unit
    }

    private class Repository(private val line: InboxLineage, records: List<InboxRecord> = listOfNotNull(line.item)) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override suspend fun refresh() = Unit
        override suspend fun inboxLine(item: InboxRecord): InboxLineage = line
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
        private fun record(id: String, title: String, sender: String? = null, preview: String = "") = InboxRecord(
            InboxItem(id, title = title, sender = sender, preview = preview, status = "action", updatedAt = "2026-11-13T00:00:00Z"),
            event = InboxEventFields(status = "action"))
    }
}
