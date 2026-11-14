package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.inboxEventFields
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import persona.core.navigation.RouteSource

/** The detail status keeps its complete sentence and usable actions when width or text size makes one row too small. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi")
class InboxDetailStatusLayoutTest {
    @get:Rule val rule = createComposeRule()
    private val repository = FakeInboxRepository()
    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private val clock = Instant.parse("2026-01-01T12:00:00Z")
    private val status = "Waiting for 张三确认新报价 · look again tomorrow 09:00: 问更正后的报价行不行"

    private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull

    private fun showcase(): InboxRecord {
        val root = System.getProperty("persona.repo.root")?.let(::File) ?: generateSequence(File(".").canonicalFile) { it.parentFile }
            .first { File(it, ".git").exists() && File(it, "android").isDirectory }
        val fixture = Json.parseToJsonElement(File(root, "spec/fixtures/inbox/showcase.json").readText()).jsonObject
        val item = fixture.getValue("items").jsonArray.first { it.jsonObject.getValue("id").jsonPrimitive.content == "sc-1" }.jsonObject
        val event = inboxEventFields(item)
        return InboxRecord(
            InboxItem(
                id = "sc-1", account = item.text("account").orEmpty(), kind = item.text("kind").orEmpty(),
                title = item.text("title").orEmpty(), sender = item.text("sender"), preview = item.text("preview").orEmpty(),
                summary = item.text("summary"), tier = item.text("tier"), updatedAt = item.text("updated_at"),
                doneAt = item.text("done_at"), status = event.status, urgent = event.urgent, where = event.where,
            ),
            sourceLabel = item.text("account_name").orEmpty(), count = item.text("count")?.toIntOrNull() ?: 0,
            source = item.text("source").orEmpty(), event = event,
        )
    }

    private fun show(record: InboxRecord, fontScale: Float = 1f) {
        repository.snapshot.value = InboxSnapshot(records = listOf(record))
        rule.setContent {
            CompositionLocalProvider(
                LocalDensity provides Density(LocalDensity.current.density, fontScale),
                LocalInboxClock provides { clock },
                LocalInboxZone provides ZoneOffset.UTC,
            ) {
                PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                    Box { InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator) }
                }
            }
        }
    }

    private fun bounds(text: String): Rect =
        rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot

    private fun textLayout(text: String): TextLayoutResult {
        val node = rule.onNodeWithText(text, useUnmergedTree = true).fetchSemanticsNode()
        val layouts = mutableListOf<TextLayoutResult>()
        node.config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    private fun assertFullyVisible(text: String, context: String) {
        val layout = textLayout(text)
        val lastGlyph = layout.getBoundingBox(text.lastIndex)
        assertFalse("$context: text must not be ellipsized or clipped: node=${bounds(text)} layout=${layout.size} " +
            "lines=${layout.lineCount} input=${layout.layoutInput.text.text}", layout.hasVisualOverflow)
        assertTrue("$context: the last glyph must be inside the text layout: ${layout.size}",
            lastGlyph.width > 0f && lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width)
    }

    private fun assertSingleLineFullyVisible(text: String, context: String) {
        val layout = textLayout(text)
        val lastGlyph = layout.getBoundingBox(text.lastIndex)
        assertTrue("$context: the complete single-line label must be drawn: ${layout.size}",
            lastGlyph.width > 0f && lastGlyph.left >= 0f && lastGlyph.right <= layout.size.width)
    }

    private fun overlaps(a: Rect, b: Rect): Boolean =
        a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom

    private fun assertStatusAndActionsAreSeparate(context: String, actionsMustWrap: Boolean) {
        val state = bounds(status)
        val conversation = bounds("Conversation ›")
        val say = bounds("Say something")
        assertFalse("$context: the sentence and Conversation overlap: $state / $conversation", overlaps(state, conversation))
        assertFalse("$context: the sentence and Say something overlap: $state / $say", overlaps(state, say))
        assertFalse("$context: the actions overlap: $conversation / $say", overlaps(conversation, say))
        if (actionsMustWrap) {
            assertTrue("$context: actions need their own line below the sentence: $state / $conversation / $say",
                state.bottom <= minOf(conversation.top, say.top) + 1f)
        }
        val actionsShareALine = conversation.top < say.bottom && say.top < conversation.bottom
        if (actionsShareALine) {
            assertTrue("$context: Conversation stays before Say something", conversation.left < say.left)
        } else {
            assertTrue("$context: Conversation stays before Say something", conversation.top <= say.top)
        }
        val root = rule.onRoot().fetchSemanticsNode().boundsInRoot
        for ((label, action) in listOf("Conversation ›" to conversation, "Say something" to say)) {
            assertTrue("$context: $label stays on screen: $root / $action",
                action.left >= root.left && action.right <= root.right + 1f)
            assertSingleLineFullyVisible(label, context)
        }
    }

    @Test fun englishExpandedShowcaseKeepsItsWholeStatusAndBothActionsUsable() {
        show(showcase())
        rule.onNodeWithText("Timeline").performClick()

        assertFullyVisible(status, "English expanded sc-1")
        assertStatusAndActionsAreSeparate("English expanded sc-1", actionsMustWrap = true)
        val summary = bounds("张三发现税率错了，正在等他确认更正后的报价")
        val recap = rule.onNodeWithText("张三要报价单。Agent 对着价目表核了一遍，发现第 3 行用的是去年的税率；更正后的报价已经用邮件发出。",
            useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val timeline = bounds("Timeline")
        assertTrue("summary → recap → timeline stays unchanged", summary.top < recap.top && recap.top < timeline.top)

        rule.onNodeWithText("Conversation ›").performClick()
        assertEquals(ChatRoute("m1", "s-handling", source = RouteSource.InboxItem("sc-1")), opened.last())
        rule.onNodeWithText("Say something").performClick()
        rule.onNode(hasSetTextAction()).assertExists()
    }

    @Config(qualifiers = "en-w320dp-h1800dp-xhdpi")
    @Test fun narrowShowcaseGivesTheWholeStatusAndActionsSeparateLines() {
        show(showcase())
        assertFullyVisible(status, "320dp sc-1")
        assertStatusAndActionsAreSeparate("320dp sc-1", actionsMustWrap = true)
    }

    @Test fun largeTextKeepsActionsBelowTheStatusWithoutOverlapOrClipping() {
        show(showcase(), fontScale = 2f)
        assertStatusAndActionsAreSeparate("200% text sc-1", actionsMustWrap = true)
        rule.onNodeWithText("Conversation ›").performClick()
        assertEquals(ChatRoute("m1", "s-handling", source = RouteSource.InboxItem("sc-1")), opened.last())
    }

    @Test fun aPendingSuggestionKeepsStatusConversationAndContentOrderWithoutSay() {
        val suggested = showcase().let {
            it.copy(event = it.event.copy(suggestion = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")))
        }
        show(suggested)
        assertFullyVisible(status, "suggested sc-1")
        rule.onNodeWithText("Conversation ›").assertExists()
        rule.onNodeWithText("Say something").assertDoesNotExist()
        val summary = bounds("张三发现税率错了，正在等他确认更正后的报价")
        val suggestion = bounds("Friday works")
        val recap = rule.onNodeWithText("张三要报价单。Agent 对着价目表核了一遍，发现第 3 行用的是去年的税率；更正后的报价已经用邮件发出。",
            useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val timeline = bounds("Timeline")
        assertTrue("summary → suggestion → recap → timeline stays unchanged",
            summary.top < suggestion.top && suggestion.top < recap.top && recap.top < timeline.top)
        rule.onNodeWithText("Conversation ›").performClick()
        assertEquals(ChatRoute("m1", "s-handling", source = RouteSource.InboxItem("sc-1")), opened.last())
    }

    @Test fun oldHubUnknownNowAndDoneKeepTheirExistingFallbacks() {
        val base = showcase().let {
            it.copy(item = it.item.copy(status = "fyi", tier = "fyi"),
                event = it.event.copy(now = null, follow = null, status = "fyi", suggestion = null))
        }
        show(base)
        rule.onNodeWithText("FYI", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Conversation ›").assertExists()

        repository.snapshot.value = InboxSnapshot(records = listOf(base.copy(event = base.event.copy(now = "future_unknown"))))
        rule.onNodeWithText("FYI", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Conversation ›").assertExists()

        val done = base.copy(item = base.item.copy(doneAt = "2026-01-01T12:00:00Z", status = null),
            event = base.event.copy(status = null, doneBy = "user"))
        repository.snapshot.value = InboxSnapshot(records = listOf(done))
        rule.onNodeWithText("Done", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Conversation ›").assertExists()
        rule.onNodeWithText("Say something").assertDoesNotExist()
    }
}
