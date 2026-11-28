package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.longClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.LarkPart
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.io.IOException
import persona.core.inbox.*

/** The assistant's own connector rows keep their conversation presentation rather than looking like external mail. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class AssistantInboxRowsTest {
    @get:Rule val rule = createComposeRule()

    private val summary = "Three checks passed; the release can proceed."
    private val chatUrl = "https://hub.example.com/chat/server/session-1"
    private val record = InboxRecord(
        InboxItem("assistant:1", kind = "assistant_post", title = "Morning briefing",
            preview = summary, summary = summary, tier = "fyi"),
        sourceLabel = "Assistant", source = "assistant",
        connector = InboxConnectorRef("server/session-1", label = "Assistant", url = chatUrl),
    )

    private class OneInbox(snapshot: InboxSnapshot, var context: String?, var lookedUp: InboxRecord? = null) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        val itemCalls = mutableListOf<String>()
        var lookupGate: CompletableDeferred<Unit>? = null
        var lookupFailure: IOException? = null
        override suspend fun item(itemId: String): InboxRecord? {
            itemCalls += itemId
            lookupGate?.await()
            lookupFailure?.let { throw it }
            return lookedUp
        }
        override suspend fun refresh() = Unit
        override suspend fun personaContext(itemId: String) = context?.let { PersonaContext(it) }
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Test fun connectorUrlSurvivesWireDecoding() {
        val url = "https://hub.example.com/chat/server/session-1"
        val decoded = JsonCodec.decodeFromString(InboxConnectorRef.serializer(),
            """{"thread":"server/session-1","label":"Assistant","url":"$url"}""")
        val encoded = JsonCodec.encodeToJsonElement(InboxConnectorRef.serializer(), decoded).jsonObject
        assertEquals(url, encoded["url"]?.jsonPrimitive?.content)
    }

    @Test fun rowKeepsPreviewThatDoesNotRepeatSummary() {
        val extra = "Open the checklist for the remaining release notes."
        val shown = record.copy(item = record.item.copy(preview = extra))
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), "${shown.item.title}\n$summary\n$extra")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxItemRow(shown, inbox, navigator, ReplyComposers(inbox, rememberCoroutineScope()), GroupPosition.Single,
                    done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
            }
        }
        rule.onAllNodesWithText(summary, useUnmergedTree = true).assertCountEquals(1)
        rule.onAllNodesWithText(extra, useUnmergedTree = true).assertCountEquals(1)
    }

    @Test fun detailOpensTheConversationFromOpenTheChat() {
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)), "${record.item.title}\n$summary")
        val opened = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalLarkLinkOpener provides { url, _ -> opened += url }) {
                    Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                    }
                }
            }
        }
        rule.onNodeWithText("Open the chat").performClick()
        assertEquals(listOf(chatUrl), opened)
    }

    @Test fun detailLoadsPagedBackAssistantRow() {
        val inbox = OneInbox(InboxSnapshot(), "${record.item.title}\n$summary", lookedUp = record).apply {
            lookupGate = CompletableDeferred()
        }
        val opened = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalLarkLinkOpener provides { url, _ -> opened += url }) {
                    Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                    }
                }
            }
        }
        rule.onNodeWithText("Loading the inbox").assertExists()
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertDoesNotExist()

        inbox.lookupGate?.complete(Unit)
        rule.waitForIdle()
        rule.onNodeWithText(record.item.title).assertExists()
        rule.onNodeWithText("Open the chat").performClick()
        assertEquals(listOf(chatUrl), opened)
        assertEquals(listOf(record.item.id), inbox.itemCalls)
    }

    @Test fun detailRetriesFailedPagedBackLookup() {
        val inbox = OneInbox(InboxSnapshot(), "${record.item.title}\n$summary", lookedUp = record).apply {
            lookupFailure = IOException("offline")
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText("Couldn't refresh the inbox. Try again.").assertExists()
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertDoesNotExist()

        inbox.lookupFailure = null
        rule.onNodeWithText("Retry").performClick()
        rule.waitForIdle()
        rule.onNodeWithText(record.item.title).assertExists()
        assertEquals(listOf(record.item.id, record.item.id), inbox.itemCalls)
    }

    @Test fun detailShowsMissingOnlyWhenPagedBackLookupReturnsNull() {
        val inbox = OneInbox(InboxSnapshot(), null)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertExists()
        assertEquals(listOf(record.item.id), inbox.itemCalls)
    }

    @Test fun rowShowsSummaryOnceWithoutSenderPrefixOrHandOff() {
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)), "${record.item.title}\n$summary")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxItemRow(record, inbox, navigator, ReplyComposers(inbox, rememberCoroutineScope()), GroupPosition.Single,
                    done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
            }
        }
        rule.onAllNodesWithText(summary, useUnmergedTree = true).assertCountEquals(1)
        rule.onAllNodesWithText(record.item.title, useUnmergedTree = true).assertCountEquals(1)
        rule.onNodeWithText("Assistant: $summary", substring = true).assertDoesNotExist()
        rule.onNode(hasText(record.item.title) and hasClickAction()).performTouchInput { longClick() }
        rule.onNodeWithText("Done").assertExists()
        rule.onNodeWithText("Hand to AI").assertDoesNotExist()
    }

    @Test fun detailOmitsContentThatOnlyRepeatsTitleAndSummaryAndHandOff() {
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)), "${record.item.title}\n$summary")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText(summary).assertExists()
        rule.onNodeWithText("Content").assertDoesNotExist()
        rule.onNodeWithText("Hand to AI").assertDoesNotExist()
    }

    @Test fun detailStripsSummaryBeforeAdditionalPreviewContent() {
        val extra = "Additional details from the assistant response."
        val shown = record.copy(item = record.item.copy(preview = "$summary\n\n$extra"))
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), null)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("assistant-detail-extra").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("Content").assertExists()
            rule.onNodeWithText(extra).assertExists()
            rule.onNodeWithText("$summary\n\n$extra").assertDoesNotExist()
            val bitmap = rule.onNodeWithTag("assistant-detail-extra").captureToImage().asAndroidBitmap()
            val name = "assistant-inbox-en-detail-extra-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Test fun detailStripsTitleAndSummaryWhenTitleStartsWithSummary() {
        val summaryText = "Morning"
        val shown = record.copy(item = record.item.copy(title = "Morning briefing", summary = summaryText, preview = summaryText))
        val extra = "Extra details that belong only in the body."
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), "${shown.item.title}\n$summaryText\n\n$extra")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText(extra).assertExists()
        rule.onNodeWithText("briefing\n$summaryText\n\n$extra").assertDoesNotExist()
    }

    @Test fun detailStripsTitleAndSummaryWhenTheyAreEqual() {
        val summaryText = "Morning"
        val shown = record.copy(item = record.item.copy(title = summaryText, summary = summaryText, preview = summaryText))
        val extra = "Extra details after the repeated heading."
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), "$summaryText\n$summaryText\n\n$extra")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText(extra).assertExists()
        rule.onNodeWithText("$summaryText\n\n$extra").assertDoesNotExist()
    }

    @Test fun detailStripsOnlyOneSummaryFromPreviewWhenTitleEqualsSummary() {
        val summaryText = "Morning"
        val extra = "Morning notes are ready."
        val shown = record.copy(item = record.item.copy(title = summaryText, summary = summaryText,
            preview = "$summaryText\n$extra"))
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), null)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText(extra).assertExists()
        rule.onNodeWithText("notes are ready.").assertDoesNotExist()
    }

    @Test fun detailDoesNotStripSummaryInsideAnotherWord() {
        val shown = record.copy(item = record.item.copy(title = "Daily briefing", summary = "Morning", preview = "Mornington"))
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), null)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText("Mornington").assertExists()
        rule.onNodeWithText("ton").assertDoesNotExist()
    }

    @Test fun detailReloadsContextWhenTheItemRefreshes() {
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)), "${record.item.title}\n$summary")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText("Content").assertDoesNotExist()
        val extra = "The refreshed conversation contains the complete release checklist."
        inbox.context = "${record.item.title}\n$summary\n\n$extra"
        inbox.snapshot.value = InboxSnapshot(records = listOf(record.copy(
            item = record.item.copy(updatedAt = "2026-11-14T01:00:00Z"))))
        rule.onNodeWithText("Content").assertExists()
        rule.onNodeWithText(extra, substring = true).assertExists()
    }

    @Test fun detailKeepsContentThatAddsToTitleAndSummary() {
        val extra = "The full release checklist is attached in the conversation."
        val inbox = OneInbox(InboxSnapshot(records = listOf(record)), "${record.item.title}\n$summary\n\n$extra")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    InboxItemDetailScreen(inbox, InboxItemDetailRoute(record.item.id), navigator)
                }
            }
        }
        rule.onNodeWithText("Content").assertExists()
        rule.onNodeWithText(extra).assertExists()
        rule.onNodeWithText("${record.item.title}\n$summary\n\n$extra").assertDoesNotExist()
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
    @Test fun englishShots() = shots("en", "Morning briefing", summary, "Assistant", "Open the chat", "Content")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi", fontScale = 2f)
    @Test fun englishLargestTextShots() = shots("en", "Morning briefing", summary, "Assistant", "Open the chat", "Content", large = true)

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1000dp-xhdpi")
    @Test fun simplifiedChineseShots() = shots("zh", "早间简报", "三项检查均通过，可以发布。", "助手", "打开对话", "正文")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1000dp-xhdpi")
    @Test fun japaneseShots() = shots("ja", "朝のまとめ", "3件のチェックが完了し、リリースできます。", "アシスタント", "チャットを開く", "本文")

    private fun shots(language: String, title: String, summaryText: String, source: String, openChat: String, content: String, large: Boolean = false) {
        val shown = record.copy(
            item = record.item.copy(title = title, sender = source, preview = summaryText, summary = summaryText),
            sourceLabel = source, connector = record.connector?.copy(label = source),
        )
        val inbox = OneInbox(InboxSnapshot(records = listOf(shown)), "$title\n$summaryText")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("row")
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalLarkLinkOpener provides { _, _ -> }) {
                    Box(Modifier.testTag("assistant-shot").width(400.dp).height(
                        if (page == "row") if (large) 420.dp else 280.dp else if (large) 1400.dp else 1000.dp)
                        .background(PersonaTheme.palette.background)) {
                        if (page == "row") {
                            InboxItemRow(shown, inbox, navigator, ReplyComposers(inbox, rememberCoroutineScope()), GroupPosition.Single,
                                done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
                        } else {
                            InboxItemDetailScreen(inbox, InboxItemDetailRoute(shown.item.id), navigator)
                        }
                    }
                }
            }
        }
        for (target in listOf("row", "detail")) {
            page = target
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                if (target == "row") rule.onAllNodesWithText(summaryText, useUnmergedTree = true).assertCountEquals(1)
                else {
                    rule.onNodeWithText(openChat).assertExists()
                    rule.onNodeWithText(content).assertDoesNotExist()
                }
                val bitmap = rule.onNodeWithTag("assistant-shot").captureToImage().asAndroidBitmap()
                val background = bitmap.getPixel(1, 1)
                var other = 0
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
                    if (bitmap.getPixel(x, y) != background) other++
                }
                assertTrue("$language $target drew nothing", other > 100)
                val name = "assistant-inbox-$language-$target-${s.name.lowercase()}-${if (d) "dark" else "light"}${if (large) "-large" else ""}.png"
                File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/assistant-inbox-shots").also { it.mkdirs() }
}
