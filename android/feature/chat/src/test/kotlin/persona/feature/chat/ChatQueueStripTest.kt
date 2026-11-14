package persona.feature.chat

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.ImagePayload
import persona.core.contracts.QueueEdit
import persona.core.contracts.Session
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.DraftUploadState
import persona.core.contracts.api.HubCodedFailure
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.R as DesignSystemR

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatQueueStripTest {
    @get:Rule val rule = createComposeRule()
    private val res = ApplicationProvider.getApplicationContext<Context>().resources

    /** Takes every queue operation; records the `keep` lists and the sends now. */
    private class Queue : ChatActions {
        val kept = mutableListOf<List<Int>>()
        var sentNow = 0
        var refusal: Exception? = null
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        /** The photos the hub returns beside the queue (a Persona conversation's), by message. */
        var photos: List<List<ImagePayload>?> = emptyList()
        override suspend fun editQueue(session: Session, keep: List<Int>): QueueEdit { kept += keep; return QueueEdit(session.queued.orEmpty(), photos) }
        override suspend fun sendQueuedNow(session: Session) { refusal?.let { throw it }; sentNow++ }
    }

    private class Refusal(override val status: Int?, override val code: String?, message: String) : Exception(message), HubCodedFailure

    private val claude = Session(machineId = "box", sessionId = "s1", title = "Refactor", agent = "claude", status = "working",
        paneId = "p1", queued = listOf("先跑测试", "再改 README", "最后提交"))

    @Test fun movingOneReQueuesThemAllInTheNewOrder() {
        assertEquals(listOf(1, 0, 2), queueMoved(3, 1, -1))
        assertEquals(listOf(0, 2, 1), queueMoved(3, 1, 1))
        assertEquals(listOf(1, 2, 0), queueMoved(3, 0, 2))
        // Past either end, or no move: nothing to send.
        assertNull(queueMoved(3, 0, -1))
        assertNull(queueMoved(3, 2, 1))
        assertNull(queueMoved(3, 1, 0))
        assertNull(queueMoved(3, 3, -1))
    }

    @Test fun moveUpAndDownOnlyWhereTheQueueCanBeReorderedGreyedAtTheEnds() {
        val all = listOf(QueueAction.Edit, QueueAction.MoveUp, QueueAction.MoveDown, QueueAction.Copy, QueueAction.Delete)
        assertEquals(all.zip(listOf(true, false, true, true, true)), queueActions(0, 3, steers = true))
        assertEquals(all.zip(listOf(true, true, true, true, true)), queueActions(1, 3, steers = true))
        assertEquals(all.zip(listOf(true, true, false, true, true)), queueActions(2, 3, steers = true))
        val plain = listOf(QueueAction.Edit to true, QueueAction.Copy to true, QueueAction.Delete to true)
        assertEquals(plain, queueActions(1, 3, steers = false))
        // One message has nowhere to move.
        assertEquals(plain, queueActions(0, 1, steers = true))
    }

    @Test fun aRefusedSendNowSaysTheHubsWords() {
        assertEquals("排队里已经没有消息了", sendNowFailure(Refusal(409, "nothing_queued", "排队里已经没有消息了"), res))
        assertEquals("屏幕上没有立即发送的提示", sendNowFailure(Refusal(409, "send_now_unrecognized", "屏幕上没有立即发送的提示"), res))
        // Another code's text is raw detail, never shown.
        assertTrue(sendNowFailure(Refusal(502, "node_unreachable", "rpc: connection closed"), res) != "rpc: connection closed")
    }

    /** The button keeps its name, speaks [words], and draws neither those words nor a dialog. */
    private fun assertSendNowSpeaks(words: String) {
        rule.onNodeWithText("立即发送").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, words))
        rule.onNodeWithText(words).assertDoesNotExist()
        rule.onAllNodes(isDialog()).assertCountEquals(0)
    }

    @Test fun sendNowSendsAtOnePressWithNoDialog() {
        val queue = Queue()
        val notices = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(claude, queue, ChatComposerState(), { notices += it }, {})
            }
        }
        // Folded: the line and its Send Now, no rows. One press sends; TalkBack still hears what the dialog said.
        rule.onNodeWithContentDescription("第 1 条：先跑测试").assertDoesNotExist()
        assertSendNowSpeaks(res.getString(R.string.chat_queue_send_now_message))
        rule.onNodeWithText("立即发送").performClick()
        rule.waitForIdle()
        assertEquals(1, queue.sentNow)
        assertEquals(listOf("已立即发给 Agent"), notices)
        rule.onAllNodes(isDialog()).assertCountEquals(0)
        queue.refusal = Refusal(409, "nothing_queued", "排队里已经没有消息了")
        rule.onNodeWithText("立即发送").performClick()
        rule.waitForIdle()
        assertEquals("排队里已经没有消息了", notices.last())
        rule.onAllNodes(isDialog()).assertCountEquals(0)
    }

    @Test fun aMessageMovesUpOrDownFromItsSheet() {
        val queue = Queue()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(claude, queue, ChatComposerState(), {}, {})
            }
        }
        rule.onNodeWithContentDescription("排队中 3 条 · Claude 忙完会依次处理，已收起").performClick()
        rule.onNodeWithContentDescription("第 1 条：先跑测试").performClick()
        rule.onNodeWithText("上移").assertIsNotEnabled()
        rule.onNodeWithText("下移").assertIsEnabled().performClick()
        rule.waitForIdle()
        rule.onNodeWithContentDescription("第 2 条：再改 README").performClick()
        rule.onNodeWithText("上移").performClick()
        rule.waitForIdle()
        assertEquals(listOf(listOf(1, 0, 2), listOf(1, 0, 2)), queue.kept)
    }

    @Test fun anotherAgentsQueueHasNeitherSendNowNorMoves() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(claude.copy(agent = "codex"), Queue(), ChatComposerState(), {}, {}, initiallyExpanded = true)
            }
        }
        rule.onNodeWithText("立即发送").assertDoesNotExist()
        rule.onNodeWithContentDescription("第 2 条：再改 README").performClick()
        rule.onNodeWithText("撤回编辑").assertIsEnabled()
        rule.onNodeWithText("上移").assertDoesNotExist()
        rule.onNodeWithText("删除").assertIsEnabled()
    }
    @Test fun titleSaysHowManyAndForWhom() {
        assertEquals("排队中 2 条 · Claude 忙完会依次处理", queueTitle(2, "Claude", res))
        assertEquals("排队中 1 条 · Codex 忙完会依次处理", queueTitle(1, "Codex", res))
    }

    @Test fun pullingOneOutKeepsTheRestInOrder() {
        assertEquals(listOf(1, 2), queueKeep(3, 0))
        assertEquals(listOf(0, 2), queueKeep(3, 1))
        // The only queued message: nothing is put back, and the empty list is what the hub gets.
        assertEquals(emptyList<Int>(), queueKeep(1, 0))
    }

    // Persona's conversation (#4339 C6, iOS QueueStrip with `own`): the user's two messages, then one relayed from another conversation.
    private val persona = Session(machineId = "box", sessionId = "main", title = "Persona", assistant = true, status = "working",
        queued = listOf("顺便把下周三的会也挪到下午", "再查一下航班", "来自「早报」：会议纪要整理好了，要不要发给参会的人？"), queuedRelayed = 1)

    @Test fun aRelayedMessageIsOnlyCopiedAndTheMovesStopAtTheUsersOwn() {
        assertEquals(listOf(QueueAction.Copy to true), queueActions(2, 2, steers = true))
        assertEquals(listOf(QueueAction.Copy to true), queueActions(0, 0, steers = false))
        val all = listOf(QueueAction.Edit, QueueAction.MoveUp, QueueAction.MoveDown, QueueAction.Copy, QueueAction.Delete)
        assertEquals(all.zip(listOf(true, false, true, true, true)), queueActions(0, 2, steers = true))
        assertEquals(all.zip(listOf(true, true, false, true, true)), queueActions(1, 2, steers = true))
        // Persona's conversation reorders and sends now where the phone may send; an Agent only in a terminal pane.
        assertTrue(persona.reordersQueue)
        assertFalse(persona.copy(sourceKind = "codex_history").reordersQueue)
        assertFalse(claude.copy(paneId = null).reordersQueue)
    }

    @Test fun personasQueueListsTheUsersOwnFirstAndFoldsTheRelayedOneIntoOneLine() {
        val queue = Queue()
        val notices = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(persona, queue, ChatComposerState(), { notices += it }, {}, initiallyExpanded = true)
            }
        }
        // The title counts all three; inside, the owner's two are rows and the relayed one is a collapsed summary (#6394).
        rule.onNodeWithContentDescription("排队中 3 条 · Persona 忙完会依次处理，已展开").assertExists()
        rule.onNodeWithContentDescription("第 1 条：顺便把下周三的会也挪到下午").assertExists()
        rule.onNodeWithContentDescription("第 2 条：再查一下航班").assertExists()
        val summary = "还有 1 条别的对话在排队"
        rule.onNodeWithContentDescription(summary).assertExists()
        rule.onNodeWithContentDescription("第 3 条：来自「早报」：会议纪要整理好了，要不要发给参会的人？").assertDoesNotExist()
        // Open the summary: the relayed row appears and is only copied.
        rule.onNodeWithContentDescription(summary).performClick()
        rule.waitForIdle()
        rule.onNodeWithContentDescription("第 3 条：来自「早报」：会议纪要整理好了，要不要发给参会的人？").assertExists()
        rule.onNodeWithContentDescription("第 3 条", substring = true).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("拷贝").assertIsEnabled()
        rule.onNodeWithText("撤回编辑").assertDoesNotExist()
        rule.onNodeWithText("删除").assertDoesNotExist()
        rule.onNodeWithText("上移").assertDoesNotExist()
        rule.onNodeWithText("拷贝").performClick()
        rule.waitForIdle()
        // The user's second is the last that moves: down stops there, up re-queues only the user's own.
        rule.onNodeWithContentDescription("第 2 条：再查一下航班").performClick()
        rule.onNodeWithText("下移").assertIsNotEnabled()
        rule.onNodeWithText("上移").assertIsEnabled().performClick()
        rule.waitForIdle()
        rule.onNodeWithContentDescription("第 1 条：顺便把下周三的会也挪到下午").performClick()
        rule.onNodeWithText("删除").performClick()
        rule.waitForIdle()
        assertEquals(listOf(listOf(1, 0), listOf(1)), queue.kept)
        assertEquals("已从队列删除", notices.last())
        // Send Now hands over the queue at one press, speaking Persona's words and not the agent's (#7170).
        assertSendNowSpeaks(res.getString(R.string.chat_queue_send_now_message_persona, "Persona"))
        rule.onNodeWithText(res.getString(R.string.chat_queue_send_now_message)).assertDoesNotExist()
        rule.onNodeWithText("立即发送").performClick()
        rule.waitForIdle()
        assertEquals(1, queue.sentNow)
        rule.onAllNodes(isDialog()).assertCountEquals(0)
    }

    // Seeing one's own queue is behaviour: Brief and Board show Persona's too, as their always-open strip without Send now.
    @Test fun briefAndBoardFoldTheRelayedOneBehindTheSummaryLine() {
        var style by mutableStateOf(AppStyle.Brief)
        val queue = Queue()
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                ChatQueueStrip(persona, queue, ChatComposerState(), {}, {})
            }
        }
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            style = s
            rule.waitForIdle()
            rule.onNodeWithText("排队中 3 条 · Persona 忙完会依次处理").assertExists()
            rule.onNodeWithContentDescription("第 1 条：顺便把下周三的会也挪到下午").assertExists()
            val summary = "还有 1 条别的对话在排队"
            rule.onNodeWithContentDescription(summary).assertExists()
            // The backlog stays behind the summary line until it is opened (the two styles share the strip's state,
            // so a second style may start where the first left off: fold it first, then assert it is collapsed).
            if (rule.onAllNodes(hasContentDescription("第 3 条：来自「早报」：会议纪要整理好了，要不要发给参会的人？")).fetchSemanticsNodes().isNotEmpty()) {
                rule.onNodeWithContentDescription(summary).performClick()
                rule.waitForIdle()
            }
            rule.onNodeWithContentDescription("第 3 条：来自「早报」：会议纪要整理好了，要不要发给参会的人？").assertDoesNotExist()
            rule.onNodeWithText("立即发送").assertDoesNotExist()
            rule.onNodeWithContentDescription(summary).performClick()
            rule.waitForIdle()
            rule.onNodeWithContentDescription("第 3 条：来自「早报」：会议纪要整理好了，要不要发给参会的人？").assertExists()
            rule.onNodeWithContentDescription("第 3 条", substring = true).performClick()
            rule.waitForIdle()
            rule.onNodeWithText("撤回编辑").assertDoesNotExist()
            rule.onNodeWithText("拷贝").performClick()
            rule.waitForIdle()
            rule.onNodeWithContentDescription("第 2 条：再查一下航班").performClick()
            rule.onNodeWithText("下移").assertIsNotEnabled()
            rule.onNodeWithText("上移").performClick()
            rule.waitForIdle()
        }
        assertEquals(listOf(listOf(1, 0), listOf(1, 0)), queue.kept)
    }

    @Test fun onlyRelayedMessagesOfferNoSendNow() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(persona.copy(status = "idle", queued = persona.queued!!.takeLast(1)), Queue(), ChatComposerState(), {}, {})
            }
        }
        // Folded by default: the title counts the backlog and there is no Send Now; opening the strip reveals the
        // collapsed summary line, not individual rows (#6394).
        rule.onNodeWithContentDescription("排队中 1 条 · Persona 忙完会依次处理，已收起").assertExists()
        rule.onNodeWithText("立即发送").assertDoesNotExist()
        rule.onNodeWithContentDescription("排队中 1 条 · Persona 忙完会依次处理，已收起").performClick()
        rule.waitForIdle()
        rule.onNodeWithContentDescription("还有 1 条别的对话在排队").assertExists()
        rule.onNodeWithContentDescription("第 1 条：来自「早报」", substring = true).assertDoesNotExist()
    }

    @Test fun anAssistantRefusalOfSendNowSaysTheHubsWords() {
        assertEquals("Claude Code 没有接受这次中断", sendNowFailure(Refusal(409, "assistant_control_failed", "Claude Code 没有接受这次中断"), res))
    }

    @Test fun aPulledBackMessageGoesAfterTheDraftOnANewLine() {
        val empty = ChatComposerState()
        empty.restoreQueued("顺便把 README 也更新一下")
        assertEquals("顺便把 README 也更新一下", empty.value.text)
        assertEquals(empty.value.text.length, empty.value.selection.start)

        val typed = ChatComposerState(ChatComposerDraft("先跑测试"))
        typed.restoreQueued("改完以后跑一遍 UI 测试")
        assertEquals("先跑测试\n改完以后跑一遍 UI 测试", typed.value.text)

        // Photos it carried come back as attachments, not as their tag.
        val photos = ChatComposerState(ChatComposerDraft("看下"))
        photos.restoreQueued("截图在这\n\n<attached_images>\n/tmp/shot.png\n</attached_images>")
        assertEquals("看下\n截图在这", photos.value.text)
        assertEquals(listOf("/tmp/shot.png"), photos.attachments.map { it.path })
    }

    @Test fun claudeQueuedPhotosRestoreAfterAnExistingDraftWithoutLosingOtherAttachments() {
        val existing = ComposerAttachment("existing", "original.jpg", "", true, "/tmp/original.jpg",
            uploadState = DraftUploadState.Uploaded)
        val state = ChatComposerState(ChatComposerDraft("Keep this draft", listOf(existing)))
        val video = "<persona-file kind=\"video\" path=\"/tmp/take 1.mov\"/>"
        val context = "<persona-context kind=\"context\" title=\"Notes\">Keep [Image #9] literally</persona-context>"
        val words = "Check these screens $video\n$context"
        state.restoreQueued("[Image #1] [Image #2]\n\n\n$words\n<attached_images>\npath: /tmp/first.jpg\n/tmp/first.jpg\npath: /tmp/second screen.jpg\nnot a path\n</attached_images>")
        // Its video comes back as a video already on the machine (iOS `VideoAttachments.restore`), not as its tag.
        assertEquals("Keep this draft\nCheck these screens\n$context", state.value.text)
        assertEquals(state.value.text.length, state.value.selection.end)
        assertEquals(existing, state.attachments.first())
        assertEquals(listOf("/tmp/original.jpg", "/tmp/first.jpg", "/tmp/second screen.jpg", "/tmp/take 1.mov"), state.attachments.map { it.path })
        assertTrue(state.attachments.last().video)
        assertTrue(state.attachments.all { it.uploadState == DraftUploadState.Uploaded })
        assertTrue(state.ready)
        assertEquals("Keep this draft\nCheck these screens\n$context\n\n<persona-file kind=\"video\" path=\"/tmp/take 1.mov\" name=\"take 1.mov\" size=\"0\"/>" +
            "\n\n<attached_images>\n/tmp/original.jpg\n/tmp/first.jpg\n/tmp/second screen.jpg\n</attached_images>", state.message())
    }

    @Test fun photoOnlyQueueStandInsAreNotRestoredAsUserWords() {
        for (words in listOf("Please look at the image", "Please look at the images", "Please look at the photos",
            "See the images", "请看图片", "画像を見てください")) {
            val state = ChatComposerState().apply { photosOnlyText = "Please look at the photos" }
            state.restoreQueued("[Image #3] $words\n<attached_images>\npath: /tmp/photo.jpg\n</attached_images>")
            assertEquals("", state.value.text)
            assertEquals(listOf("/tmp/photo.jpg"), state.attachments.map { it.path })
            assertEquals("Please look at the photos\n\n<attached_images>\n/tmp/photo.jpg\n</attached_images>", state.message())
            assertEquals("[Photo]", composerMessagePreview("$words\n<attached_images>\npath: /tmp/photo.jpg\n</attached_images>", "[Photo]"))
            val plain = ChatComposerState().apply { restoreQueued(words) }
            assertEquals(words, plain.value.text)
        }
    }

    @Test fun queuePreviewsRemoveOnlyLeadingClaudeImageMarkers() {
        assertEquals("Check these screens\n[Photo]", composerMessagePreview(
            "[Image #1] [Image #2]\n\n\nCheck these screens\n<attached_images>\npath: /tmp/a.jpg\npath: /tmp/b.jpg\n</attached_images>", "[Photo]"))
        val raw = "Keep [Image #7] as text <persona-file kind=\"video\" path=\"/tmp/clip.mov\"/>"
        assertEquals(raw, composerMessagePreview(raw, "[Photo]"))
        val empty = ChatComposerState().apply { restoreQueued("[Image #6]Check the screen\n<attached_images>\n</attached_images>") }
        assertEquals("Check the screen", empty.value.text)
        assertTrue(empty.attachments.isEmpty())
    }

    // A Persona message taken back brings its photos (#4339 C6): the hub returns them beside the queue as base64, which the
    // words do not carry; they go back into the draft as photos, written where a camera photo waits and ready to go up.
    private val tinyPng = ImagePayload("image/png", "iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAFklEQVR4nAAJAPb/AgAAAAAAAAAAAwAAGwADK+C1DwAAAABJRU5ErkJggg==")

    @Test fun takingBackAPersonaMessageBringsItsPhotosBackIntoTheDraft() {
        useChatFileProvider(ApplicationProvider.getApplicationContext())
        val queue = Queue().apply { photos = listOf(listOf(tinyPng, tinyPng), null) }
        val notices = mutableListOf<String>()
        val composer = ChatComposerState()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(persona, queue, composer, { notices += it }, {}, initiallyExpanded = true)
            }
        }
        rule.onNodeWithContentDescription("第 1 条", substring = true).performClick()
        rule.onNodeWithText("撤回编辑").performClick()
        rule.waitUntil(10_000) { notices.isNotEmpty() }
        // The words back in the box, and both photos as photos of the draft, each a file the draft keeps and cleans up like a capture.
        assertEquals("顺便把下周三的会也挪到下午", composer.value.text)
        assertEquals(2, composer.attachments.size)
        assertTrue(composer.attachments.all { it.image && !it.video && it.uri.contains("chat-captures") })
        assertEquals(listOf(res.getString(R.string.chat_queue_restored)), notices)
        assertEquals(listOf(listOf(1)), queue.kept)
        // One with no photos brings none.
        notices.clear()
        rule.onNodeWithContentDescription("第 2 条", substring = true).performClick()
        rule.onNodeWithText("撤回编辑").performClick()
        rule.waitUntil(10_000) { notices.isNotEmpty() }
        assertEquals("再查一下航班", composer.value.text.lines().last())
        assertEquals(2, composer.attachments.size)
    }

    @Test fun aPhotoThatCannotBePutBackIsSaidNotSilentlyDropped() {
        useChatFileProvider(ApplicationProvider.getApplicationContext())
        val queue = Queue().apply { photos = listOf(listOf(tinyPng, ImagePayload("image/png", "%%%%"), ImagePayload("image/png", "")), null) }
        val notices = mutableListOf<String>()
        val composer = ChatComposerState()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(persona, queue, composer, { notices += it }, {}, initiallyExpanded = true)
            }
        }
        rule.onNodeWithContentDescription("第 1 条", substring = true).performClick()
        rule.onNodeWithText("撤回编辑").performClick()
        rule.waitUntil(10_000) { notices.isNotEmpty() }
        assertEquals(1, composer.attachments.size)
        assertEquals(listOf(res.getQuantityString(R.plurals.chat_queue_photos_lost, 2, 2)), notices)
    }

    @Test fun deletingAMessageBringsNothingBack() {
        val queue = Queue().apply { photos = listOf(listOf(tinyPng), null) }
        val composer = ChatComposerState()
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(persona, queue, composer, {}, {}, initiallyExpanded = true)
            }
        }
        rule.onNodeWithContentDescription("第 1 条", substring = true).performClick()
        rule.onNodeWithText("删除").performClick()
        rule.waitForIdle()
        assertEquals(listOf(listOf(1)), queue.kept)
        assertTrue(composer.attachments.isEmpty())
        assertEquals("", composer.value.text)
    }

    // iOS's "N images" under a Persona message's words, from what this phone sent (its photos ride in the words as paths).
    private val twoPhotos = "看看这两张\n\n<attached_images>\n/tmp/a.jpg\n/tmp/b.jpg\n</attached_images>"
    private val onePhotoAlone = "Please look at the image\n\n<attached_images>\n/tmp/c.jpg\n</attached_images>"
    private val photoRows = persona.copy(queued = listOf(twoPhotos, onePhotoAlone, "只有文字"), queuedRelayed = 0)

    @Test fun aQueuedPersonaRowSaysHowManyPhotosItCarriesOnGlass() {
        assertEquals(2, composerPhotoCount(twoPhotos))
        assertEquals(1, composerPhotoCount(onePhotoAlone))
        assertEquals(0, composerPhotoCount("只有文字"))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                ChatQueueStrip(photoRows, Queue(), ChatComposerState(), {}, {}, initiallyExpanded = true)
            }
        }
        val two = res.getQuantityString(DesignSystemR.plurals.designsystem_queue_photos, 2, 2)
        val one = res.getQuantityString(DesignSystemR.plurals.designsystem_queue_photos, 1, 1)
        // The count is a caption under the words, which no longer carry the mark ...
        rule.onNodeWithText(two).assertExists()
        rule.onNodeWithText(one).assertExists()
        rule.onNodeWithText("看看这两张").assertExists()
        rule.onAllNodesWithText("[图片]", substring = true).assertCountEquals(0)
        // ... a photo alone leaves its row to say just that, and the row's spoken form carries it too.
        rule.onNodeWithContentDescription("第 1 条：看看这两张。$two").assertExists()
        rule.onNodeWithContentDescription("第 3 条：只有文字").assertExists()
        assertEquals(1, rule.onAllNodesWithText(two).fetchSemanticsNodes().size)
    }

    @Test fun anAgentsQueueAndBriefAndBoardKeepThePhotoMarkAmongTheWords() {
        var style by mutableStateOf(AppStyle.Glass)
        var session by mutableStateOf(claude.copy(queued = photoRows.queued))
        rule.setContent {
            PersonaTheme(style = style, reduceMotion = true) {
                ChatQueueStrip(session, Queue(), ChatComposerState(), {}, {}, initiallyExpanded = true)
            }
        }
        val two = res.getQuantityString(DesignSystemR.plurals.designsystem_queue_photos, 2, 2)
        // An Agent's chat on Glass: iOS puts its mark before the words; Android keeps its [Photo] among them.
        rule.onNodeWithText("看看这两张\n\n[图片]").assertExists()
        rule.onAllNodesWithText(two).assertCountEquals(0)
        for (s in listOf(AppStyle.Brief, AppStyle.Board)) {
            session = photoRows; style = s
            rule.waitForIdle()
            rule.onNodeWithText("看看这两张\n\n[图片]").assertExists()
            rule.onAllNodesWithText(two).assertCountEquals(0)
        }
    }
}
