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
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.flow.update
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import java.io.File

/**
 * Inbox phase 1 on the JVM with Robolectric's native graphics, in every style, light and dark, from the shared fixture
 * with every state: the list (Needs you · In progress · FYI · Collapsed · Done and the rows' marks) and the event pages (the
 * suggestion of each kind, In progress, Urgent, failed, the chain folded and open), plus the Edit sheet. PNGs go to
 * `build/inbox-event-shots/` (or `-Dpersona.shots=<dir>`), docs/android/android.md"Inbox: events, suggestions, and handling chains".
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class InboxEventShotsTest {
    @get:Rule val rule = createComposeRule()

    private class EventInbox(records: List<InboxRecord>) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = records))
        override val connectionState = flowOf(ConnectionState.Online)
        val answers = mutableListOf<SuggestionRequest>()
        var answer: (SuggestionRequest) -> SuggestionOutcome = { SuggestionOutcome.Done }
        override suspend fun actOnSuggestion(request: SuggestionRequest): SuggestionOutcome { answers += request; return answer(request) }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-event-shots").also { it.mkdirs() }

    private fun save(name: String, bitmap: Bitmap) {
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun capture(name: String) {
        rule.waitForIdle()
        save(name, rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap())
    }

    private fun mode(dark: Boolean) = if (dark) "dark" else "light"

    @Config(qualifiers = "zh-w400dp-h2600dp-xhdpi")
    @Test fun drawsTheListWithEverySectionAndMark() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var run by mutableIntStateOf(0)
        val inbox = EventInbox(phase1Records())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(2600.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(inbox, navigator)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; run++
            rule.waitForIdle()
            capture("inbox-events-list-${s.name.lowercase()}-${mode(d)}")
            for (words in listOf("进行中", "等对方回复", "AI 正在准备建议", "建议回复：可以，我周四下班前把报价发你。")) {
                assertTrue(words, rule.onAllNodes(hasText(words, substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
            }
        }
        // A row with event fields opens its event page; the old AI twin draft card is not drawn under its suggestion.
        rule.onNodeWithText("张三问周五前能否给客户报价").performClick()
        assertEquals(InboxEventRoute("lark:oc_demo1:100"), opened.last())
        assertEquals(0, rule.onAllNodes(hasText("改一改")).fetchSemanticsNodes().size)
    }

    @Test fun drawsTheEventPagesInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf("lark:oc_demo1:100")
        var run by mutableIntStateOf(0)
        val inbox = EventInbox(phase1Records())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                        InboxEventScreen(inbox, InboxEventRoute(shown), navigator)
                    }
                }
            }
        }
        val pages = listOf("lark:oc_demo1:100" to "reply", "lark:oc_demo3:300" to "waiting", "lark:oc_demo2:200" to "urgent",
            "mail:a1:contract:88" to "agent", "lark:oc_demo7:700" to "conclusion", "lark:oc_demo6:600" to "failed")
        for (s in AppStyle.entries) for (d in listOf(false, true)) for ((id, name) in pages) {
            style = s; dark = d; shown = id; run++
            rule.waitForIdle()
            rule.onNodeWithText("看原文").assertExists()
            if (name == "waiting") rule.onNodeWithText("处理链条").performClick()
            capture("inbox-event-$name-${s.name.lowercase()}-${mode(d)}")
        }
    }

    @Test fun theButtonsAnswerTheSuggestionAndAStaleOneSaysSo() {
        val records = phase1Records()
        val inbox = EventInbox(records)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    InboxEventScreen(inbox, InboxEventRoute("lark:oc_demo1:100"), navigator)
                }
            }
        }
        // Send runs it exactly as shown: the version, no text.
        rule.onNode(hasText("发送") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(SuggestionRequest("lark:oc_demo1:100", 2, SuggestionAction.Confirm, null, inbox.answers.last().clientId), inbox.answers.last())

        // Stale: the hub had a newer suggestion; the page redraws with it and says "Suggestion updated".
        inbox.answer = { request ->
            inbox.snapshot.update { snap -> snap.copy(records = snap.records.map { r ->
                if (r.item.id != request.itemId) r else r.copy(event = r.event.copy(suggestion = InboxSuggestion(3, "reply", "可以，周四前发你。")))
            }) }
            SuggestionOutcome.Stale
        }
        rule.onNode(hasText("驳回") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(SuggestionAction.Reject, inbox.answers.last().action)
        rule.onNodeWithText("建议已更新").assertExists()
        rule.onNodeWithText("可以，周四前发你。").assertExists()
        for (d in listOf(false, true)) { dark = d; capture("inbox-event-stale-glass-${mode(d)}") }

        // Edit: the edited text goes with confirm; "Say something" goes as instruct.
        inbox.answer = { SuggestionOutcome.Done }
        rule.onNode(hasText("修改") and hasClickAction()).performClick()
        rule.waitForIdle()
        for (d in listOf(false, true)) {
            dark = d
            rule.waitForIdle()
            save("inbox-event-editor-glass-${mode(d)}", rule.onNode(isDialog()).captureToImage().asAndroidBitmap())
        }
        rule.onNode(hasSetTextAction() and hasText("可以，周四前发你。")).performTextReplacement("可以，周三前发你。")
        rule.onNode(hasText("发送") and hasClickAction() and androidx.compose.ui.test.hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals(SuggestionAction.Confirm to "可以，周三前发你。", inbox.answers.last().action to inbox.answers.last().text)
        assertEquals(3L, inbox.answers.last().version)

        rule.onNode(hasText("修改") and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onAllNodes(hasSetTextAction())[1].performTextReplacement("别这么回，先问时间")
        rule.onNode(hasText("告诉 AI") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(SuggestionAction.Instruct to "别这么回，先问时间", inbox.answers.last().action to inbox.answers.last().text)

        // The chain: the newest entry, its link opens the processing conversation.
        rule.onNodeWithText("管理者处理").performClick()
        assertEquals(ChatRoute("m1", "s-demo1", source = persona.core.navigation.RouteSource.InboxItem("lark:oc_demo1:100")), opened.last())
        rule.onNodeWithText("看原文").performClick()
        assertEquals(LarkRoute("oc_demo1", "lark:oc_demo1:100", 100), opened.last())
    }
}
