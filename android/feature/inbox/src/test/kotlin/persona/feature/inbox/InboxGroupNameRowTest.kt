package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxFollow
import persona.core.contracts.InboxHandling
import persona.core.designsystem.*
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.*
import java.io.File
import java.lang.reflect.Proxy
import java.util.Locale

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w600dp-h900dp-xhdpi")
class InboxGroupNameRowTest {
    @get:Rule val rule = createComposeRule()
    private val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val repository = Proxy.newProxyInstance(InboxRepository::class.java.classLoader, arrayOf(InboxRepository::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args[0]
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "RowTestRepository"
            else -> error("Unexpected repository access: ${method.name}")
        }
    } as InboxRepository
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun record(): InboxRecord {
        val path = System.getenv("PERSONA_INBOX_LIVE_FIXTURE")
        if (path == null) return InboxRecord(InboxItem("event", kind = "group",
            title = "Approve the spring launch checklist and confirm every owner before Friday's release", sender = "Sender",
            preview = "Latest line", summary = "Summary", updatedAt = "2026-01-01T11:21:00Z"),
            feedId = "feed", source = "lark", isGroup = true, groupName = "Source group", count = 1)
        val fixture = Json.parseToJsonElement(File(path).readText()).jsonObject
        val item = fixture.getValue("item").jsonObject
        fun text(key: String) = item[key]?.jsonPrimitive?.contentOrNull
        return InboxRecord(InboxItem(text("id")!!, kind = text("kind")!!, title = text("title")!!, sender = text("sender"),
            preview = text("preview").orEmpty(), summary = text("summary"), updatedAt = text("updated_at")),
            feedId = item.getValue("lark").jsonObject.getValue("feed_id").jsonPrimitive.content,
            source = "lark", isGroup = true, groupName = fixture.getValue("group_name").jsonPrimitive.content, count = 1)
    }

    private fun textLayout(text: String): TextLayoutResult {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun assertTitleStartsVisibly(title: String, context: String) {
        val firstGlyph = textLayout(title).getBoundingBox(0)
        assertTrue("$context: the title's first glyph must remain visible, not be replaced by an ellipsis", firstGlyph.width > 0f)
    }

    private fun assertFullyVisible(text: String, context: String) {
        val layout = textLayout(text)
        val lastGlyph = layout.getBoundingBox(text.lastIndex)
        assertTrue("$context: the complete state must fit without clipping or ellipsis: ${layout.size}",
            lastGlyph.width > 0f && lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width)
    }

    @Test fun legacyEventUsesTheSharedThreeContentLinesInEveryStyle() {
        val record = record()
        val sender = record.item.sender!!
        val group = record.groupName!!
        val meta = "$sender：${record.item.preview}"
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var shown by mutableStateOf(record)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.testTag("row").width(580.dp).background(PersonaTheme.palette.background)) {
                        InboxItemRow(shown, repository, navigator, ReplyComposers(repository, rememberCoroutineScope()), GroupPosition.Single,
                            done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) for (fontScale in listOf(1f, 2f)) {
            rule.runOnIdle { style = appearance; dark = night; scale = fontScale }
            rule.waitForIdle()
            val context = "$appearance dark=$night scale=$fontScale"
            assertTitleStartsVisibly(record.item.title, context)
            assertFullyVisible(meta, context)
            val title = rule.onNodeWithText(record.item.title, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val summary = rule.onNodeWithText(record.item.summary!!, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val latest = rule.onNodeWithText(meta, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("$context: $title / $summary / $latest", title.top < summary.top && summary.top < latest.top)
            // Neither the sender nor the source conversation is a second title on an old-hub row.
            rule.onNodeWithText(sender, useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText(group, useUnmergedTree = true).assertDoesNotExist()
            record.item.updatedAt?.let { rule.onNodeWithText(displayTime(it, Locale.SIMPLIFIED_CHINESE, res), useUnmergedTree = true).assertExists() }
            val out = File(System.getenv("PERSONA_INBOX_SHOTS") ?: "build/inbox-row-boundary-shots/legacy").apply { mkdirs() }
            File(out, "${appearance.name.lowercase()}-${if (night) "dark" else "light"}-${fontScale}x.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("row").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }

        // Without a summary, the source-specific line takes that position rather than leaving a blank second line.
        rule.runOnIdle { shown = record.copy(item = record.item.copy(summary = null)) }
        rule.onNodeWithText(meta, useUnmergedTree = true).assertExists()
        rule.onNodeWithText(record.item.summary!!, useUnmergedTree = true).assertDoesNotExist()
        for (kind in listOf("dm", "mail")) {
            rule.runOnIdle {
                shown = record.copy(item = record.item.copy(kind = kind), isGroup = false, groupName = null,
                    source = if (kind == "mail") "mail" else "lark", feedId = if (kind == "mail") null else record.feedId)
            }
            val expected = if (kind == "dm") record.item.preview else meta
            rule.onNodeWithText(expected, useUnmergedTree = true).assertExists()
            rule.onNodeWithText(group, useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText(sender, useUnmergedTree = true).assertDoesNotExist()
            rule.onAllNodesWithText(record.item.title, useUnmergedTree = true).assertCountEquals(1)
            if (kind == "dm") rule.onNodeWithText("私聊", useUnmergedTree = true).assertExists()
            else rule.onNodeWithText("私聊", useUnmergedTree = true).assertDoesNotExist()
        }
        rule.runOnIdle { shown = record.copy(isGroup = false) }
        rule.onNodeWithText("私聊", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun eventWithWhereKeepsTheSummaryAndUsesTheSharedThreeContentLines() {
        val base = record()
        val sender = base.item.sender!!
        val group = base.groupName!!
        val followed = base.copy(
            item = base.item.copy(summary = "AI summary"),
            event = InboxEventFields(where = group, now = "confirm_reply"),
        )
        val metaText = if (group == sender) "$sender：${base.item.preview}" else "$group · $sender：${base.item.preview}"
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableStateOf(1f)
        var rowWidth by mutableStateOf(580.dp)
        var shown by mutableStateOf(followed)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.testTag("follow-row").width(rowWidth).background(PersonaTheme.palette.background)) {
                        InboxItemRow(shown, repository, navigator, ReplyComposers(repository, rememberCoroutineScope()), GroupPosition.Single,
                            done = shown.item.isDone, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
                    }
                }
            }
        }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) for (fontScale in listOf(1f, 2f)) {
            rule.runOnIdle { style = appearance; dark = night; scale = fontScale }
            rule.waitForIdle()
            val context = "$appearance dark=$night scale=$fontScale"
            assertTitleStartsVisibly(followed.item.title, context)
            assertFullyVisible("等你确认回复", context)
            assertFullyVisible(metaText, context)
            val title = rule.onNodeWithText(followed.item.title, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val summary = rule.onNodeWithText("AI summary", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            val meta = rule.onNodeWithText(metaText, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
            assertTrue("$context: $title / $summary / $meta", title.top < summary.top && summary.top < meta.top)
            rule.onNodeWithText(group, useUnmergedTree = true).assertDoesNotExist()
            rule.onNodeWithText(sender, useUnmergedTree = true).assertDoesNotExist()
            followed.item.updatedAt?.let { rule.onNodeWithText(displayTime(it, Locale.SIMPLIFIED_CHINESE, res), useUnmergedTree = true).assertExists() }
            val out = File(System.getenv("PERSONA_INBOX_FOLLOW_SHOTS") ?: "build/inbox-row-boundary-shots/follow").apply { mkdirs() }
            File(out, "${appearance.name.lowercase()}-${if (night) "dark" else "light"}-${fontScale}x.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("follow-row").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }

        // A direct chat says the person once, in the same title / summary / meta order.
        rule.runOnIdle {
            shown = followed.copy(item = followed.item.copy(kind = "dm"), isGroup = false, groupName = null,
                event = followed.event.copy(where = sender, now = "waiting"))
        }
        rule.onNodeWithText("$sender：${base.item.preview}", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("等对方回复", useUnmergedTree = true).assertExists()
        rule.onNodeWithText(sender, useUnmergedTree = true).assertDoesNotExist()

        val working = followed.copy(
            item = followed.item.copy(status = InboxItem.WORKING),
            event = followed.event.copy(status = InboxItem.WORKING, handling = InboxHandling(state = "looking"), now = "waiting",
                follow = InboxFollow(who = "A very long wait for the whole review group to answer",
                    next = "Check every remaining owner and then ask for a decision again")),
        )
        for (unknownOrMissing in listOf("something_new", null)) {
            rule.runOnIdle { shown = working.copy(event = working.event.copy(now = unknownOrMissing)) }
            rule.onNodeWithText("AI 正在看", useUnmergedTree = true).assertExists()
        }

        // At a narrow width the state keeps every glyph and the right padding; the meta on its left is the part that ellipsizes.
        rule.runOnIdle { shown = followed; rowWidth = 360.dp; scale = 1f }
        assertFullyVisible("等你确认回复", "narrow 360dp")
        val row = rule.onNodeWithTag("follow-row").fetchSemanticsNode().boundsInRoot
        val state = rule.onNodeWithText("等你确认回复", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("The state must end inside the row's content bounds: $state / $row", state.right <= row.right)

        // Without where the third line is sender: preview, not a bold source-conversation line.
        rule.runOnIdle { shown = base }
        rule.onNodeWithText("AI summary", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText(base.item.summary!!, useUnmergedTree = true).assertExists()
        rule.onNodeWithText("$sender：${base.item.preview}", useUnmergedTree = true).assertExists()
        rule.onNodeWithText(group, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText(sender, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText(metaText, useUnmergedTree = true).assertDoesNotExist()

        // A repeated summary leaves no empty third row: its state takes the next line, as on every other row.
        rule.runOnIdle { shown = followed.copy(item = followed.item.copy(preview = "AI summary"), source = "assistant") }
        val summaryRow = rule.onNodeWithText("AI summary", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val separateState = rule.onNodeWithText("等你确认回复", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("The state must sit below the summary, not beside it: $separateState / $summaryRow",
            separateState.top >= summaryRow.bottom - 1f)
    }
}
