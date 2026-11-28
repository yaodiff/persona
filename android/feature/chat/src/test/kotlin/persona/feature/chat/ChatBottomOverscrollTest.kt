package persona.feature.chat

import androidx.compose.foundation.MutatePriority
import androidx.compose.foundation.gestures.ScrollScope
import androidx.compose.foundation.interaction.DragInteraction
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.click
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipe
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.chatlist.ChatList
import persona.core.chatlist.ChatListEffects
import persona.core.chatlist.ChatListState
import persona.core.chatlist.holdPosition
import persona.core.chatlist.rememberChatListState
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JumpToLatest
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.sort.SessionRenamer

/**
 * Regression coverage for bottom overscroll and floating reader navigation (#13074):
 * - Overscrolling past the newest edge dismisses the keyboard but never exits `followsEnd`, never shows `JumpToLatest`,
 *   and never shifts messages upward.
 * - A lone `JumpToLatest` floats above the composer without shrinking the transcript height in Glass, Brief, and Board.
 *   A visible `PlanPill` reserves its row so the controls leave the newest message readable.
 * - Returning to the bottom (including small pixel settle offsets and drag stop/cancel races) reliably restores
 *   `followsEnd = true` and hides `JumpToLatest`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class ChatBottomOverscrollTest {
    @get:Rule val rule = createComposeRule()
    private val probe = KeyboardProbe()

    private val agentKey = SessionKey("computer-a", "importer")
    private val assistantKey = SessionKey("computer-a", "main")
    private val renamer = object : SessionRenamer {
        override fun project(source: Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = false
    }

    private fun history() = (1..30).map {
        Item(
            id = "m$it",
            seq = it,
            role = if (it % 2 == 1) "user" else "assistant",
            kind = "text",
            text = "Message number $it with enough words to take up vertical space in the transcript.",
            ts = "2026-11-12T01:%02d:00Z".format(it),
        )
    }

    private fun plannedHistory(): List<Item> = buildList {
        fun todo(text: String, status: String) = buildJsonObject {
            put("content", text)
            put("status", status)
            put("activeForm", text)
        }
        add(Item("u1", 1, role = "user", kind = "text", text = "Fix the retry loop", ts = "2026-11-12T01:01:00Z"))
        add(Item("t2", 2, role = "assistant", kind = "tool_use", toolUseId = "t2", toolName = "TodoWrite",
            ts = "2026-11-12T01:02:00Z", toolInput = buildJsonObject {
                put("todos", buildJsonArray {
                    add(todo("Inspect logs", "completed"))
                    add(todo("Fix retry", "in_progress"))
                    add(todo("Run tests", "pending"))
                })
            }))
        add(Item("t2-r", 2, role = "user", kind = "tool_result", toolUseId = "t2", text = "ok", ts = "2026-11-12T01:02:01Z"))
        for (seq in 3..28) {
            add(Item("t$seq", seq, role = "assistant", kind = "tool_use", toolUseId = "t$seq", toolName = "Bash",
                ts = "2026-11-12T01:%02d:00Z".format(seq),
                toolInput = buildJsonObject { put("command", "./gradlew test --tests Step$seq") }))
            add(Item("t$seq-r", seq, role = "user", kind = "tool_result", toolUseId = "t$seq", text = "ok",
                ts = "2026-11-12T01:%02d:05Z".format(seq)))
        }
        add(Item("r29", 29, role = "assistant", kind = "text", text = "Done with the retry fix.", ts = "2026-11-12T01:29:00Z"))
        for (seq in 30..45) {
            add(Item("f$seq", seq, role = if (seq % 2 == 0) "user" else "assistant", kind = "text",
                text = "Follow-up message $seq to push the plan card far above the viewport.",
                ts = "2026-11-12T01:%02d:00Z".format(seq)))
        }
    }

    private fun mountChat(
        key: SessionKey,
        styleState: () -> AppStyle,
        items: List<Item> = history(),
        composer: ChatComposerState = ChatComposerState(),
    ) {
        val transcript = MutableStateFlow(ChatSnapshot(loading = false, items = items))
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(agentKey.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(
                Session(agentKey.machineId, agentKey.sessionId, title = "Fix the importer retry", agent = "claude", status = "idle", state = "idle"),
                Session(assistantKey.machineId, assistantKey.sessionId, title = "Persona", assistant = true, status = "idle", state = "idle"),
            ))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = transcript
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val renderStyle = if (key == assistantKey) ChatRenderStyle.Assistant else ChatRenderStyle.Agent
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = styleState()) {
                probe.Around {
                    Box(Modifier.fillMaxSize()) {
                        ChatScreen(
                            sessionKey = key,
                            repository = chats,
                            sender = sender,
                            interactions = interactions,
                            sessions = sessions,
                            onBack = {},
                            chrome = ChatChromeBindings(renamer = renamer),
                            renderStyle = renderStyle,
                            composerState = composer,
                            showTopBar = key != assistantKey,
                        )
                    }
                }
            }
        }
        val anchorText = items.last().text.orEmpty().take(18)
        rule.waitUntil(15_000) { rule.onAllNodesWithText(anchorText, substring = true).fetchSemanticsNodes().isNotEmpty() }
        settle()
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()
    }

    private fun chatListNode() = rule.onNode(
        hasScrollToIndexAction() and SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange),
    )

    private fun jumpNode() = rule.onNode(hasContentDescription("Jump to latest", substring = true, ignoreCase = true))

    @Test fun bottomOverscrollKeepsFollowingAndDismissesKeyboardInAssistantChat() =
        assertBottomOverscrollAcrossStyles(assistantKey)

    @Test fun bottomOverscrollKeepsFollowingAndDismissesKeyboardInAgentChat() =
        assertBottomOverscrollAcrossStyles(agentKey)

    private fun assertBottomOverscrollAcrossStyles(key: SessionKey) {
        var style by mutableStateOf(AppStyle.Glass)
        val composer = ChatComposerState()
        mountChat(key, { style }, composer = composer)

        for (each in AppStyle.entries) {
            style = each
            settle()
            // 1. Overscroll with focused composer still dismisses keyboard/focus and never shows JumpToLatest.
            rule.onNode(hasSetTextAction()).performTouchInput { click() }
            settle()
            assertTrue("composer takes focus in $each", composer.focused)
            probe.reset()
            chatListNode().performTouchInput {
                swipeUp(startY = bottom - height * 0.25f, endY = top + height * 0.25f, durationMillis = 300)
            }
            settle()
            assertTrue("overscroll drag still clears focus in $each", probe.focusClears.isNotEmpty())
            assertTrue("overscroll drag still hides keyboard in $each", probe.hides > 0)
            jumpNode().assertDoesNotExist()

            // 2. Overscroll with idle composer leaves transcript height and newest message position unchanged.
            val transcriptBefore = chatListNode().fetchSemanticsNode().boundsInRoot
            val newestBefore = rule.onNodeWithText("Message number 30", substring = true).fetchSemanticsNode().boundsInRoot
            chatListNode().performTouchInput {
                swipeUp(startY = bottom - height * 0.25f, endY = top + height * 0.25f, durationMillis = 300)
            }
            settle()
            jumpNode().assertDoesNotExist()
            assertEquals("transcript bounds must not change on overscroll in $each",
                transcriptBefore, chatListNode().fetchSemanticsNode().boundsInRoot)
            assertEquals("newest message must not shift upward on overscroll in $each",
                newestBefore.bottom,
                rule.onNodeWithText("Message number 30", substring = true).fetchSemanticsNode().boundsInRoot.bottom,
                1f)
        }
    }

    @Test fun jumpToLatestFloatsAboveComposerWithoutShrinkingTranscriptAcrossStyles() {
        var style by mutableStateOf(AppStyle.Glass)
        mountChat(agentKey, { style })
        val minTouchPx = with(rule.density) { Tokens.MinTouch.android.toPx() }

        for (each in AppStyle.entries) {
            style = each
            settle()
            val transcriptBefore = chatListNode().fetchSemanticsNode().boundsInRoot
            val composer = rule.onNode(hasSetTextAction()).fetchSemanticsNode().boundsInRoot

            repeat(2) {
                chatListNode().performTouchInput {
                    swipeDown(startY = top + height * 0.25f, endY = bottom - height * 0.15f, durationMillis = 300)
                }
                settle()
            }

            jumpNode().assertIsDisplayed()
            val transcriptAfter = chatListNode().fetchSemanticsNode().boundsInRoot
            val button = jumpNode().fetchSemanticsNode().boundsInRoot

            assertEquals("Jump to latest must not shrink transcript in $each", transcriptBefore, transcriptAfter)
            assertTrue("Jump to latest $button overlaps composer $composer in $each", button.bottom <= composer.top)
            assertTrue("Jump to latest touch target too small in $each: $button",
                button.width >= minTouchPx - 1f && button.height >= minTouchPx - 1f)

            // Dragging the empty space to the left of the floating arrow still scrolls the transcript back toward bottom.
            val dragX = transcriptAfter.left + (button.left - transcriptAfter.left) * 0.4f
            val dragY = (button.top + button.bottom) * 0.5f
            assertTrue("clear space exists left of Jump to latest in $each", dragX < button.left)
            repeat(6) {
                chatListNode().performTouchInput {
                    swipe(
                        start = Offset(dragX - transcriptAfter.left, dragY - transcriptAfter.top),
                        end = Offset(dragX - transcriptAfter.left, transcriptAfter.height * 0.15f),
                        durationMillis = 200,
                    )
                }
                settle()
            }
            jumpNode().assertDoesNotExist()
            assertEquals("transcript bounds stay constant after returning to latest in $each",
                transcriptBefore, chatListNode().fetchSemanticsNode().boundsInRoot)
        }
    }

    @Test fun planPillLeavesTheNewestMessageReadable() {
        mountChat(agentKey, { AppStyle.Glass }, items = plannedHistory())
        val newest = rule.onNodeWithText("Follow-up message 45", substring = true)
            .assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        val pill = rule.onNode(hasContentDescription("Step 2 of 3", substring = true))
            .assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        assertFalse("Plan pill $pill covers newest message $newest", pill.overlaps(newest))
    }

    @Test fun planPillAndJumpToLatestShareTheFooterWithoutCollidingAcrossStyles() {
        var style by mutableStateOf(AppStyle.Glass)
        mountChat(agentKey, { style }, items = plannedHistory())

        for (each in AppStyle.entries) {
            style = each
            settle()
            val transcriptBefore = chatListNode().fetchSemanticsNode().boundsInRoot
            val composer = rule.onNode(hasSetTextAction()).fetchSemanticsNode().boundsInRoot

            repeat(2) {
                chatListNode().performTouchInput {
                    swipeDown(startY = top + height * 0.25f, endY = bottom - height * 0.15f, durationMillis = 300)
                }
                settle()
            }

            val transcriptAfter = chatListNode().fetchSemanticsNode().boundsInRoot
            assertEquals("showing the jump control keeps the existing transcript bounds in $each", transcriptBefore, transcriptAfter)
            val jump = jumpNode().assertIsDisplayed().fetchSemanticsNode().boundsInRoot
            assertTrue("Jump to latest $jump overlaps composer $composer in $each", jump.bottom <= composer.top)

            // In Glass the collapsed work line surfaces PlanPill alongside JumpToLatest.
            if (each == AppStyle.Glass) {
                val pill = rule.onNode(hasContentDescription("Step 2 of 3", substring = true))
                    .assertIsDisplayed().fetchSemanticsNode().boundsInRoot
                assertTrue("PlanPill $pill overlaps composer $composer", pill.bottom <= composer.top)
                assertTrue("PlanPill $pill overlaps Jump to latest $jump", pill.right <= jump.left)
            }

            // Return to latest via touch scroll before switching style.
            repeat(6) {
                chatListNode().performTouchInput {
                    swipeUp(startY = bottom - height * 0.2f, endY = top + height * 0.2f, durationMillis = 200)
                }
                settle()
            }
            jumpNode().assertDoesNotExist()
        }
    }

    @Test fun chatListEffectsRejectsOverscrollAndRecoversAtBottomWithToleranceAndDragStopCancel() {
        lateinit var state: ChatListState
        lateinit var scope: CoroutineScope
        var freezes = 0
        var follows = 0
        val rows = (0 until 30).map { "row-$it" }

        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                scope = rememberCoroutineScope()
                state = rememberChatListState()
                ChatListEffects(
                    state = state,
                    rowIds = rows,
                    ready = true,
                    onFreeze = { freezes++ },
                    onFollow = { follows++ },
                    loadOlder = {},
                )
                ChatList(state, Modifier.width(360.dp).height(600.dp).testTag("list")) {
                    items(rows.size, key = { rows[it] }) { index ->
                        Text("Row $index", Modifier.fillMaxWidth().height(56.dp))
                    }
                }
            }
        }
        settle()
        assertTrue("starts following end", state.followsEnd)
        assertEquals(0, freezes)

        // 1. Bottom overscroll (swipeUp at index 0, offset 0) never exits followsEnd or freezes.
        rule.onNodeWithTag("list").performTouchInput { swipeUp() }
        settle()
        assertTrue("bottom overscroll keeps followsEnd", state.followsEnd)
        assertEquals("bottom overscroll never freezes", 0, freezes)

        // 2. Genuine scroll upward into history exits followsEnd and freezes once.
        rule.onNodeWithTag("list").performTouchInput {
            swipeDown(startY = top + height * 0.2f, endY = bottom - height * 0.1f, durationMillis = 300)
        }
        settle()
        assertFalse("scrolling away from bottom exits followsEnd", state.followsEnd)
        assertEquals(1, freezes)
        assertTrue("list moved away from bottom", state.list.firstVisibleItemIndex > 0 || state.list.firstVisibleItemScrollOffset > 0)

        // 3. Settling within small bottom pixel tolerance (2 px) and stopping a drag reliably restores followsEnd.
        val followsBeforeTolerance = follows
        val interactions = state.list.interactionSource as MutableInteractionSource
        rule.runOnIdle {
            scope.launch {
                state.list.scrollToItem(0, 2)
                val start = DragInteraction.Start()
                interactions.emit(start)
                interactions.emit(DragInteraction.Stop(start))
            }
        }
        settle()
        assertTrue("small bottom offset recovers followsEnd on DragInteraction.Stop", state.followsEnd)
        assertEquals(followsBeforeTolerance + 1, follows)
        assertEquals("returnToLatest snaps residual offset to 0", 0, state.list.firstVisibleItemScrollOffset)

        // 4. DragInteraction.Cancel at (0, 0) when followsEnd was false also recovers (no stuck state on distinctUntilChanged).
        val followsBeforeCancel = follows
        rule.runOnIdle {
            state.holdPosition()
            assertFalse(state.followsEnd)
            scope.launch {
                val start = DragInteraction.Start()
                interactions.emit(start)
                interactions.emit(DragInteraction.Cancel(start))
            }
        }
        settle()
        assertTrue("DragInteraction.Cancel at bottom recovers followsEnd", state.followsEnd)
        assertEquals(followsBeforeCancel + 1, follows)
    }

    @Test fun shortNonScrollableConversationNeverExitsFollowOnDrag() {
        lateinit var state: ChatListState
        var freezes = 0
        val rows = listOf("only-1", "only-2")

        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                state = rememberChatListState()
                ChatListEffects(
                    state = state,
                    rowIds = rows,
                    ready = true,
                    onFreeze = { freezes++ },
                    onFollow = {},
                    loadOlder = {},
                )
                ChatList(state, Modifier.width(360.dp).height(600.dp).testTag("short-list")) {
                    items(rows.size, key = { rows[it] }) { index ->
                        Text("Short row $index", Modifier.fillMaxWidth().height(48.dp))
                    }
                }
            }
        }
        settle()

        rule.onNodeWithTag("short-list").performTouchInput { swipeDown() }
        settle()
        rule.onNodeWithTag("short-list").performTouchInput { swipeUp() }
        settle()
        assertTrue("short non-scrollable list stays following", state.followsEnd)
        assertEquals("short non-scrollable list never freezes", 0, freezes)
    }

    @Test fun dragLeavingBottomWhileNewestRowArrivesFreezesAtTheReadingPosition() =
        assertFollowingWhenNewestRowArrives(moveDuringDrag = true)

    @Test fun newestRowArrivingDuringDragWithoutDisplacementKeepsFollowing() =
        assertFollowingWhenNewestRowArrives(moveDuringDrag = false)

    private fun assertFollowingWhenNewestRowArrives(moveDuringDrag: Boolean) {
        lateinit var state: ChatListState
        lateinit var scope: CoroutineScope
        var freezes = 0
        var rows by mutableStateOf((0 until 30).map { "row-$it" })
        var drag: ScrollScope? = null
        var pendingDelta = 0f
        var consumedDelta = 0f
        val release = CompletableDeferred<Unit>()

        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                state = rememberChatListState()
                scope = rememberCoroutineScope()
                val committedRows = rows
                ChatListEffects(state, rowIds = committedRows, ready = true, onFreeze = { freezes++ },
                    onFollow = {}, loadOlder = {})
                Box {
                    ChatList(state, Modifier.width(360.dp).height(600.dp)) {
                        items(committedRows.size, key = { committedRows[it] }) { index ->
                            Text(committedRows[index], Modifier.fillMaxWidth().height(56.dp))
                        }
                    }
                    if (!state.followsEnd) JumpToLatest(count = 0, onClick = state::returnToLatest)
                }
                // Deliver the first threshold-crossing delta as the prepend is committed, before the
                // list's next idle reconciliation. The held user-input scroll excludes auto-follow.
                SideEffect {
                    if (committedRows.first() == "newest" && pendingDelta > 0f) {
                        consumedDelta = checkNotNull(drag).scrollBy(pendingDelta)
                        pendingDelta = 0f
                    }
                }
            }
        }
        settle()
        val newestBefore = rule.runOnIdle { readingAnchor(state) }
        assertEquals("row-0" to 0, newestBefore)
        val interactions = state.list.interactionSource as MutableInteractionSource
        rule.runOnIdle {
            scope.launch {
                val start = DragInteraction.Start()
                state.list.scroll(MutatePriority.UserInput) {
                    drag = this
                    interactions.emit(start)
                    release.await()
                    interactions.emit(DragInteraction.Stop(start))
                }
            }
        }
        settle()
        val crossingDelta = with(rule.density) { 80.dp.toPx() }
        rule.runOnIdle {
            assertTrue("user input is in progress", state.list.isScrollInProgress)
            assertEquals("starts with no user displacement", newestBefore, readingAnchor(state))
            if (moveDuringDrag) pendingDelta = crossingDelta
            rows = listOf("newest") + rows
        }
        settle()
        val anchor = rule.runOnIdle {
            assertEquals("the newly inserted row has been laid out", rows.size, state.list.layoutInfo.totalItemsCount)
            assertEquals("the held drag consumed the intended displacement", if (moveDuringDrag) crossingDelta else 0f, consumedDelta, 1f)
            if (moveDuringDrag) assertTrue("the list moved toward older rows", state.list.canScrollBackward)
            readingAnchor(state)
        }
        rule.runOnIdle { release.complete(Unit) }
        settle()

        assertEquals(!moveDuringDrag, state.followsEnd)
        assertEquals(if (moveDuringDrag) 1 else 0, freezes)
        if (moveDuringDrag) {
            jumpNode().assertIsDisplayed()
            assertEquals("releasing the drag preserves its position", anchor, rule.runOnIdle { readingAnchor(state) })
            rule.runOnIdle { rows = listOf("newer") + rows }
            settle()
            assertEquals("later messages preserve the reading position", anchor, rule.runOnIdle { readingAnchor(state) })
            jumpNode().assertIsDisplayed()
        } else {
            jumpNode().assertDoesNotExist()
            assertEquals("the latest row is followed after the release", "newest" to 0, rule.runOnIdle { readingAnchor(state) })
        }
    }

    private fun readingAnchor(state: ChatListState): Pair<Any, Int> {
        val list = state.list
        val row = list.layoutInfo.visibleItemsInfo.first { it.index == list.firstVisibleItemIndex }
        return row.key to list.firstVisibleItemScrollOffset
    }
}
