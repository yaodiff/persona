package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.runtime.Composition
import androidx.compose.runtime.ExperimentalComposeRuntimeApi
import androidx.compose.runtime.RecomposeScope
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.currentComposer
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.runtime.tooling.CompositionObserver
import androidx.compose.runtime.tooling.ObservableComposition
import androidx.compose.runtime.tooling.setObserver
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.ModeChange
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
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.chatLines

@OptIn(ExperimentalComposeRuntimeApi::class)
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatScrollRecompositionTest {
    @get:Rule val rule = createComposeRule()

    private class ScopeCounter : CompositionObserver {
        val callers = HashMap<String, Int>()
        override fun onScopeEnter(scope: RecomposeScope) {
            val caller = Throwable().stackTrace.firstOrNull {
                !it.className.startsWith("androidx.compose.runtime") &&
                    !it.className.startsWith(ChatScrollRecompositionTest::class.java.name)
            }
            val where = "${caller?.fileName}:${caller?.methodName}"
            callers[where] = (callers[where] ?: 0) + 1
        }
        override fun onBeginComposition(composition: ObservableComposition) = Unit
        override fun onReadInScope(scope: RecomposeScope, value: Any) = Unit
        override fun onScopeExit(scope: RecomposeScope) = Unit
        override fun onEndComposition(composition: ObservableComposition) = Unit
        override fun onScopeInvalidated(scope: RecomposeScope, value: Any?) = Unit
        override fun onScopeDisposed(scope: RecomposeScope) = Unit
    }

    @Test fun chatLinkHostScrollingLambdaAvoidsRecomposingContentSlot() {
        val scrolling = mutableStateOf(false)
        var contentRecompositions = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                ChatLinkHost(scrolling = { scrolling.value }) {
                    contentRecompositions++
                    Text("Transcript")
                }
            }
        }
        rule.waitForIdle()
        assertEquals(1, contentRecompositions)

        repeat(3) {
            rule.runOnIdle { scrolling.value = true }
            rule.waitForIdle()
            rule.runOnIdle { scrolling.value = false }
            rule.waitForIdle()
        }
        assertEquals("toggling scrolling must not recompose ChatLinkHost content", 1, contentRecompositions)
    }

    @Test fun chatJumpToLatestIsolatesTimelineVersionAndVisibleItemReadsFromParent() {
        val timeline = ChatTimeline()
        val m1 = Item("m1#0", 1, role = "assistant", kind = "text", text = "First")
        val m2 = Item("m2#0", 2, role = "assistant", kind = "text", text = "Second")
        val m3 = Item("m3#0", 3, role = "assistant", kind = "text", text = "Third")
        val m4 = Item("m4#0", 4, role = "assistant", kind = "text", text = "Fourth")
        val live = listOf(m1, m2, m3, m4)
        timeline.freeze(listOf(m1, m2))
        val timelineVersion = mutableIntStateOf(0)
        val newestVisible = mutableStateOf<Item?>(m2)
        var parentRecompositions = 0

        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                parentRecompositions++
                ChatJumpToLatest(
                    timeline = timeline,
                    timelineVersion = timelineVersion,
                    items = live,
                    newestVisibleItem = newestVisible,
                    readThrough = null,
                    deferCount = false,
                    agentChat = false,
                    onClick = {},
                )
            }
        }
        rule.waitForIdle()
        assertEquals(1, parentRecompositions)
        rule.onNodeWithText("2").assertIsDisplayed()

        rule.runOnIdle { newestVisible.value = m1 }
        rule.waitForIdle()
        assertEquals("visible item changes must not recompose the parent scope", 1, parentRecompositions)
        rule.onNodeWithText("3").assertIsDisplayed()

        rule.runOnIdle {
            timeline.follow()
            timelineVersion.intValue++
        }
        rule.waitForIdle()
        assertEquals("timelineVersion bumps must not recompose the parent scope", 1, parentRecompositions)
        rule.onNode(hasContentDescription("Jump to latest")).assertIsDisplayed()
    }

    @Test fun rememberChatPageLinesSkipsRecomputationWhenModeChangesUntilDoesNotAffectLines() {
        val key = SessionKey("mac", "main")
        val start = Instant.parse("2026-11-23T10:00:00Z")
        val items = listOf(
            Item("u1#0", 1, role = "user", kind = "text", text = "Hi", ts = start.toString()),
            Item("a1#0", 2, role = "assistant", kind = "text", text = "Hello", ts = start.plusSeconds(10).toString()),
        )
        val projection = chatLines(items.map(Item::asLineItem))
        val snapshot = ChatSnapshot(items = items, loading = false)
        val search = ChatSearchState()
        val earlyChange = ModeChange(kind = "set", from = "default", to = "acceptEdits", at = start.plusSeconds(5).toString())
        val lateChange = ModeChange(kind = "set", from = "acceptEdits", to = "plan", at = start.plusSeconds(30).toString())

        var renderStyle by mutableStateOf(ChatRenderStyle.Assistant)
        var modeChanges by mutableStateOf(listOf(earlyChange, lateChange))
        var modeChangesUntil by mutableStateOf<Instant?>(null)
        var capturedLines: List<ChatLine>? = null

        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                val pageLines = rememberChatPageLines(
                    key = key,
                    renderStyle = renderStyle,
                    snapshot = snapshot,
                    session = null,
                    foldsBackground = false,
                    supervisingConversation = false,
                    tasks = null,
                    taskTodos = emptyList(),
                    search = search,
                    openWork = emptySet(),
                    projection = projection,
                    feed = emptyList(),
                    taskPlans = emptyMap(),
                    taskCardProjection = emptyMap(),
                    modeChanges = modeChanges,
                    modeChangesUntil = modeChangesUntil,
                )
                SideEffect { capturedLines = pageLines.lines }
            }
        }
        rule.waitForIdle()

        // Assistant chats never merge mode changes: flipping modeChangesUntil keeps the exact same lines instance.
        val assistantInitial = checkNotNull(capturedLines)
        rule.runOnIdle { modeChangesUntil = start.plusSeconds(10) }
        rule.waitForIdle()
        assertSame(assistantInitial, capturedLines)
        rule.runOnIdle { modeChangesUntil = null }
        rule.waitForIdle()
        assertSame(assistantInitial, capturedLines)

        // Agent chats with only pre-cutoff mode changes also keep the exact same lines instance across cutoff flips.
        rule.runOnIdle {
            renderStyle = ChatRenderStyle.Agent
            modeChanges = listOf(earlyChange)
            modeChangesUntil = null
        }
        rule.waitForIdle()
        val agentBeforeCutoff = checkNotNull(capturedLines)
        assertEquals(1, agentBeforeCutoff.filterIsInstance<ChatLine.ModeChange>().size)
        rule.runOnIdle { modeChangesUntil = start.plusSeconds(10) }
        rule.waitForIdle()
        assertSame(agentBeforeCutoff, capturedLines)

        // Agent chats with a post-cutoff mode change hide it while frozen and restore it on follow.
        rule.runOnIdle {
            modeChanges = listOf(earlyChange, lateChange)
            modeChangesUntil = null
        }
        rule.waitForIdle()
        assertEquals(2, checkNotNull(capturedLines).filterIsInstance<ChatLine.ModeChange>().size)
        rule.runOnIdle { modeChangesUntil = start.plusSeconds(10) }
        rule.waitForIdle()
        assertEquals(1, checkNotNull(capturedLines).filterIsInstance<ChatLine.ModeChange>().size)
        rule.runOnIdle { modeChangesUntil = null }
        rule.waitForIdle()
        assertEquals(2, checkNotNull(capturedLines).filterIsInstance<ChatLine.ModeChange>().size)
    }

    @Test fun chatSearchProjectionTracksTimelineVersionWhileFrozen() {
        val base = listOf(Item("m1#0", 1, role = "assistant", kind = "text", text = "Hello"))
        val live = ChatLiveItems(base)
        val projection = ChatSearchProjection()
        var projectCalls = 0
        val first = projection.update(live.revisions.value, frozen = true, timelineVersion = 1) {
            projectCalls++
            base
        }
        assertEquals(base, first)
        assertEquals(1, projectCalls)

        // Same structure generation and timeline version skips recomputation while frozen.
        live.updateText("m1#0", ChatTextUpdate("Hello world", streaming = true))
        assertNull(projection.update(live.revisions.value, frozen = true, timelineVersion = 1) {
            projectCalls++
            base
        })
        assertEquals(1, projectCalls)

        // A timelineVersion change (such as fold expansion) refreshes search items even while frozen.
        val refreshed = projection.update(live.revisions.value, frozen = true, timelineVersion = 2) {
            projectCalls++
            base
        }
        assertEquals(base, refreshed)
        assertEquals(2, projectCalls)
    }

    @Test fun draggingUpFromBottomOfLongMainChatDoesNotRecomposeChatPageOrReaderContent() {
        val key = SessionKey("macbook", "main")
        val start = Instant.parse("2026-11-23T09:00:00Z")
        val initialItems = (1..60).map { n ->
            Item(
                id = "m$n#0",
                seq = n,
                role = if (n % 2 == 1) "user" else "assistant",
                kind = "text",
                text = "Message $n: " + "enough body text to span multiple lines in the main chat ".repeat(2),
                ts = start.plusSeconds(n * 20L).toString(),
            )
        }
        val transcript = MutableStateFlow(
            ChatSnapshot(items = initialItems, loading = false, verified = true, lastSeenMessageId = "m60#0"),
        )
        val session = Session(
            machineId = key.machineId,
            sessionId = key.sessionId,
            title = "Persona",
            assistant = true,
            status = "idle",
            state = "idle",
        )
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "MacBook", online = true)))
            override val sessions = MutableStateFlow(listOf(session))
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

        lateinit var composition: Composition
        rule.setContent {
            composition = currentComposer.composition
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize()) {
                    ChatScreen(
                        sessionKey = key,
                        repository = chats,
                        sender = sender,
                        interactions = interactions,
                        sessions = sessions,
                        onBack = {},
                        renderStyle = ChatRenderStyle.Assistant,
                        showTopBar = false,
                    )
                }
            }
        }
        rule.waitUntil(15_000) {
            rule.onAllNodesWithText("Message 60:", substring = true).fetchSemanticsNodes().isNotEmpty()
        }
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()

        val list = rule.onNode(
            hasScrollToIndexAction() and
                SemanticsMatcher.keyIsDefined(SemanticsProperties.VerticalScrollAxisRange),
        )
        val counter = ScopeCounter()
        val handle = checkNotNull(rule.runOnUiThread { composition.setObserver(counter) }) {
            "composition cannot be observed"
        }
        try {
            // In reverseLayout, swiping down drags upward away from the latest message at the bottom.
            list.performTouchInput { swipeDown(startY = top + height * 0.25f, endY = bottom - height * 0.15f) }
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
        } finally {
            handle.dispose()
        }

        val jump = rule.onNode(hasContentDescription("Jump to latest", substring = true, ignoreCase = true))
        jump.assertIsDisplayed()

        val chatPageRuns = counter.callers["ChatScreen.kt:ChatPage"] ?: 0
        val readerContentRuns = counter.callers["ChatPageReaderContent.kt:ChatPageReaderContent"] ?: 0
        val report = counter.callers.entries.sortedByDescending { it.value }.take(12).joinToString { "${it.key}=${it.value}" }
        assertEquals("ChatPage should not recompose when leaving the bottom ($report)", 0, chatPageRuns)
        assertEquals("ChatPageReaderContent should not recompose when leaving the bottom ($report)", 0, readerContentRuns)

        // New assistant replies arriving while frozen stay deferred and show in Jump to latest until returning.
        val incoming = Item(
            id = "m61#0",
            seq = 61,
            role = "assistant",
            kind = "text",
            text = "Arrived while frozen",
            ts = start.plusSeconds(61 * 20L).toString(),
        )
        transcript.value = ChatSnapshot(
            items = initialItems + incoming,
            loading = false,
            verified = true,
            lastSeenMessageId = "m60#0",
        )
        rule.waitUntil(10_000) {
            rule.onAllNodes(hasContentDescription("Jump to latest", substring = true, ignoreCase = true) and
                hasContentDescription("1", substring = true))
                .fetchSemanticsNodes().isNotEmpty()
        }
        assertFalse(
            "frozen window must not pull the viewport to the new reply",
            rule.onAllNodesWithText("Arrived while frozen").fetchSemanticsNodes().isNotEmpty(),
        )

        jump.performClick()
        rule.waitUntil(10_000) {
            rule.onAllNodesWithText("Arrived while frozen").fetchSemanticsNodes().isNotEmpty()
        }
        jump.assertDoesNotExist()
    }
}
