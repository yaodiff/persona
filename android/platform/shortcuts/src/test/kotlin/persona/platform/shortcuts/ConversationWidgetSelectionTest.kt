package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.xmlpull.v1.XmlPullParser
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.RouteInput
import persona.core.navigation.Router

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ConversationWidgetSelectionTest {
    private val context: Context = ApplicationProvider.getApplicationContext()

    @After
    fun tearDown() {
        ConversationWidgetPrefs.clearAll(context)
        MainChatWidgetStore.clear(context)
    }

    @Test
    fun prefsSaveReadPairingIsolationDeleteAndRestore() {
        // Unbound instance defaults to null (Main Chat backwards compatibility).
        assertNull(ConversationWidgetPrefs.read(context, 101, "hub-a"))

        val leaderBinding = ConversationWidgetBinding(
            pairing = "hub-a",
            machineId = "m1",
            sessionId = "dir-1",
            titleSnapshot = "Release Ops",
            kind = ConversationWidgetKind.Leader,
        )
        val chatBinding = ConversationWidgetBinding(
            pairing = "hub-a",
            machineId = "m1",
            sessionId = "chat-2",
            titleSnapshot = "Design Review",
            kind = ConversationWidgetKind.Assistant,
        )
        ConversationWidgetPrefs.save(context, 101, leaderBinding)
        ConversationWidgetPrefs.save(context, 102, chatBinding)

        assertEquals(leaderBinding, ConversationWidgetPrefs.read(context, 101, "hub-a"))
        assertEquals(chatBinding, ConversationWidgetPrefs.read(context, 102, "hub-a"))
        // Different pairing cannot read hub-a bindings.
        assertNull(ConversationWidgetPrefs.read(context, 101, "hub-b"))

        // Overlapping restore IDs (101 -> 102, 102 -> 103) migrate cleanly without clobbering.
        ConversationWidgetPrefs.restore(context, intArrayOf(101, 102), intArrayOf(102, 103))
        assertNull(ConversationWidgetPrefs.read(context, 101, "hub-a"))
        assertEquals(leaderBinding, ConversationWidgetPrefs.read(context, 102, "hub-a"))
        assertEquals(chatBinding, ConversationWidgetPrefs.read(context, 103, "hub-a"))

        // Delete removes specified widget IDs only.
        ConversationWidgetPrefs.delete(context, intArrayOf(102))
        assertNull(ConversationWidgetPrefs.read(context, 102, "hub-a"))
        assertEquals(chatBinding, ConversationWidgetPrefs.read(context, 103, "hub-a"))
    }

    @Test
    fun receiversCleanUpOnDeletedAndMigrateOnRestored() {
        val bindingA = ConversationWidgetBinding("hub-a", "m1", "s1", "Alpha", ConversationWidgetKind.Assistant)
        val bindingB = ConversationWidgetBinding("hub-a", "m1", "s2", "Beta", ConversationWidgetKind.Agent)
        ConversationWidgetPrefs.save(context, 201, bindingA)
        ConversationWidgetPrefs.save(context, 202, bindingB)

        val mainReceiver = MainChatWidgetReceiver()
        mainReceiver.onRestored(context, intArrayOf(201), intArrayOf(301))
        assertNull(ConversationWidgetPrefs.read(context, 201, "hub-a"))
        assertEquals(bindingA, ConversationWidgetPrefs.read(context, 301, "hub-a"))
        mainReceiver.onDeleted(context, intArrayOf(301))
        assertNull(ConversationWidgetPrefs.read(context, 301, "hub-a"))

        val askReceiver = AskPersonaWidgetReceiver()
        askReceiver.onRestored(context, intArrayOf(202), intArrayOf(302))
        assertNull(ConversationWidgetPrefs.read(context, 202, "hub-a"))
        assertEquals(bindingB, ConversationWidgetPrefs.read(context, 302, "hub-a"))
        askReceiver.onDeleted(context, intArrayOf(302))
        assertNull(ConversationWidgetPrefs.read(context, 302, "hub-a"))
    }

    @Test
    fun storeIsolatesSnapshotsByPairingAndConversationKey() {
        val mainSnap = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.Idle,
            mainSession = SessionKey("m1", "main"),
            summary = "Main summary",
            isMainChat = true,
        )
        val leaderKey = "leader:m1:dir-1"
        val leaderSnap = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.Working,
            mainSession = SessionKey("m1", "dir-1"),
            summary = "Leader summary",
            title = "Release Ops",
            isMainChat = false,
            targetAssistant = true,
        )

        MainChatWidgetStore.save(context, "hub-a", mainSnap)
        MainChatWidgetStore.save(context, "hub-a", leaderSnap, leaderKey)

        assertEquals("Main summary", MainChatWidgetStore.read(context, "hub-a")?.summary)
        val restoredLeader = MainChatWidgetStore.read(context, "hub-a", leaderKey)
        assertNotNull(restoredLeader)
        assertEquals("Leader summary", restoredLeader?.summary)
        assertEquals("Release Ops", restoredLeader?.title)
        assertFalse(restoredLeader?.isMainChat ?: true)

        // Switching pairing evicts previous pairing snapshots.
        assertNull(MainChatWidgetStore.read(context, "hub-b"))
        assertNull(MainChatWidgetStore.read(context, "hub-b", leaderKey))
    }

    @Test
    fun leaderProjectionAggregatesDirectionTasksAndPendingOnlyForThatDirection() {
        val leaderKey = SessionKey("m1", "dir-1")
        val childAgentKey = SessionKey("m1", "agent-sub-1")
        val otherAgentKey = SessionKey("m1", "agent-other")

        val tree = DirectionTree(
            directions = listOf(
                Direction(
                    id = "dir-1",
                    name = "Release Ops",
                    counts = DirectionCounts(needsYou = 1, working = 1),
                    running = true,
                    tasks = listOf(
                        DirectionTask(
                            id = "t1",
                            title = "Ship build 28",
                            state = DirectionTaskState.NeedsYou,
                            agents = listOf(DirectionAgent("m1", "agent-sub-1")),
                        ),
                    ),
                ),
                Direction(
                    id = "dir-2",
                    name = "Other Direction",
                    tasks = listOf(
                        DirectionTask(
                            id = "t2",
                            title = "Unrelated task",
                            state = DirectionTaskState.Stuck,
                            agents = listOf(DirectionAgent("m1", "agent-other")),
                        ),
                    ),
                ),
            ),
        )
        val leaderSession = Session(
            machineId = "m1",
            sessionId = "dir-1",
            title = "Raw Leader Title",
            assistant = true,
            lastText = "Coordinating release checks",
        )
        val assistant = AssistantSnapshot(
            mainSession = SessionKey("m1", "main"),
            conversations = listOf(leaderSession),
            leaders = listOf("dir-1", "dir-2"),
            directionTree = tree,
        )
        val interactions = listOf(
            Interaction(id = "ix-1", machineId = "m1", sessionId = "agent-sub-1", kind = "permission", summary = "Approve deploy"),
            Interaction(id = "ix-2", machineId = "m1", sessionId = "agent-other", kind = "question", summary = "Other question"),
        )
        val binding = ConversationWidgetBinding("hub-a", "m1", "dir-1", "Release Ops", ConversationWidgetKind.Leader)

        val snap = MainChatWidgetState.build(
            paired = true,
            assistant = assistant,
            sessions = listOf(leaderSession, Session(machineId = "m1", sessionId = "agent-sub-1"), Session(machineId = "m1", sessionId = "agent-other")),
            interactions = interactions,
            resources = context.resources,
            targetBinding = binding,
        )

        assertFalse(snap.isMainChat)
        assertFalse(snap.bindingMissing)
        assertEquals("Release Ops", snap.title)
        assertEquals(leaderKey, snap.mainSession)
        assertEquals(MainChatPhase.NeedsYou, snap.phase)
        assertEquals(1, snap.pendingCount)
        assertEquals("ix-1", snap.pending?.id)
        assertEquals(listOf("t1"), snap.todos.map { it.id })
        assertEquals("Coordinating release checks", snap.summary)
    }

    @Test
    fun regularConversationProjectionIsolatesOwnStateAndPending() {
        val targetKey = SessionKey("m1", "chat-2")
        val targetSession = Session(
            machineId = "m1",
            sessionId = "chat-2",
            title = "Design Review",
            assistant = true,
            status = "working",
            lastText = "Reviewing widget spacing",
        )
        val otherSession = Session(machineId = "m1", sessionId = "chat-3", title = "Other")
        val assistant = AssistantSnapshot(
            mainSession = SessionKey("m1", "main"),
            conversations = listOf(targetSession, otherSession),
            directionTree = DirectionTree(
                directions = listOf(
                    Direction(
                        id = "main",
                        secretary = true,
                        tasks = listOf(DirectionTask("t-sec", "Main task", DirectionTaskState.Working)),
                    ),
                ),
            ),
        )
        val interactions = listOf(
            Interaction(id = "ix-other", machineId = "m1", sessionId = "chat-3", kind = "question", summary = "Ignore me"),
        )
        val binding = ConversationWidgetBinding("hub-a", "m1", "chat-2", "Design Review", ConversationWidgetKind.Assistant)

        val snap = MainChatWidgetState.build(
            paired = true,
            assistant = assistant,
            sessions = listOf(targetSession, otherSession),
            interactions = interactions,
            resources = context.resources,
            targetBinding = binding,
        )

        assertFalse(snap.isMainChat)
        assertFalse(snap.bindingMissing)
        assertEquals("Design Review", snap.title)
        assertEquals(targetKey, snap.mainSession)
        assertEquals(MainChatPhase.Working, snap.phase)
        assertEquals(0, snap.pendingCount)
        assertNull(snap.pending)
        assertTrue(snap.todos.isEmpty())
    }

    @Test
    fun archivedOrDeletedBindingMarksBindingMissing() {
        val archived = Session(machineId = "m1", sessionId = "gone", title = "Old Thread", archived = true)
        val binding = ConversationWidgetBinding("hub-a", "m1", "gone", "Old Thread", ConversationWidgetKind.Assistant)

        val archivedSnap = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(conversations = listOf(archived)),
            sessions = listOf(archived),
            interactions = emptyList(),
            resources = context.resources,
            targetBinding = binding,
        )
        assertTrue(archivedSnap.bindingMissing)
        assertEquals("Old Thread", archivedSnap.title)

        val deletedSnap = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = SessionKey("m1", "main")),
            sessions = listOf(Session(machineId = "m1", sessionId = "main", assistant = true)),
            interactions = emptyList(),
            resources = context.resources,
            targetBinding = binding,
        )
        assertTrue(deletedSnap.bindingMissing)
        assertEquals("Old Thread", deletedSnap.title)

        val askMissing = AskWidgetTarget.resolve(
            paired = true,
            binding = binding,
            assistant = AssistantSnapshot(conversations = listOf(archived)),
            sessions = listOf(archived),
        )
        assertTrue(askMissing.bindingMissing)
        assertFalse(askMissing.isMainChat)
    }

    @Test
    fun pickerModelGroupsSearchesAndFallsBackOffline() {
        val mainKey = SessionKey("m1", "main")
        val leaderSession = Session(machineId = "m1", sessionId = "dir-1", title = "Leader Raw", assistant = true, lastText = "Tracking milestones")
        val otherAssistant = Session(machineId = "m1", sessionId = "chat-2", title = "Travel Plan", assistant = true, lastText = "Flight options")
        val otherAgent = Session(machineId = "m1", sessionId = "agent-1", title = "Repo Refactor", assistant = false)
        val archivedSession = Session(machineId = "m1", sessionId = "old-1", title = "Archived", archived = true)
        val assistant = AssistantSnapshot(
            mainSession = mainKey,
            conversations = listOf(leaderSession, otherAssistant, archivedSession),
            leaders = listOf("dir-1"),
            directionTree = DirectionTree(listOf(Direction(id = "dir-1", name = "Android Quality"))),
        )

        val sections = ConversationWidgetPickerModel.build(
            assistant = assistant,
            sessions = listOf(leaderSession, otherAssistant, otherAgent, archivedSession),
            currentBinding = null,
            mainChatTitle = "Main chat",
            mainChatSubtitle = "Primary conversation",
            directionFallbackTitle = "Untitled",
        )
        assertNotNull(sections.main)
        assertTrue(sections.main!!.selected)
        assertEquals(listOf("Android Quality"), sections.leaders.map { it.title })
        assertEquals(listOf("Travel Plan", "Repo Refactor"), sections.others.map { it.title })

        val filtered = ConversationWidgetPickerModel.build(
            assistant = assistant,
            sessions = listOf(leaderSession, otherAssistant, otherAgent, archivedSession),
            currentBinding = null,
            mainChatTitle = "Main chat",
            mainChatSubtitle = "Primary conversation",
            directionFallbackTitle = "Untitled",
            query = "travel",
        )
        assertNull(filtered.main)
        assertTrue(filtered.leaders.isEmpty())
        assertEquals(listOf("Travel Plan"), filtered.others.map { it.title })

        // Offline / empty fallback still exposes pinned Main chat.
        val offline = ConversationWidgetPickerModel.build(
            assistant = AssistantSnapshot(),
            sessions = emptyList(),
            currentBinding = null,
            mainChatTitle = "Main chat",
            mainChatSubtitle = "Primary conversation",
            directionFallbackTitle = "Untitled",
        )
        assertNotNull(offline.main)
        assertTrue(offline.leaders.isEmpty())
        assertTrue(offline.others.isEmpty())
    }

    @Test
    fun askConversationDeepLinkRoutesToChatWithFocusComposer() {
        val key = SessionKey("m1", "dir-1")
        val assistantIntent = SurfaceLinks.askConversation(context, key, assistant = true)
        val agentIntent = SurfaceLinks.askConversation(context, key, assistant = false)

        val router = Router()
        val resolvedAssistant = router.parse(RouteInput.DeepLink(assistantIntent.dataString!!))
        assertNotNull(resolvedAssistant)
        assertEquals(AppTab.Persona, resolvedAssistant!!.tab)
        assertEquals(ChatRoute("m1", "dir-1", ConversationKind.Assistant), resolvedAssistant.key)
        assertTrue(resolvedAssistant.focusComposer)

        val resolvedAgent = router.parse(RouteInput.DeepLink(agentIntent.dataString!!))
        assertNotNull(resolvedAgent)
        assertEquals(AppTab.Agents, resolvedAgent!!.tab)
        assertEquals(ChatRoute("m1", "dir-1", ConversationKind.Agent), resolvedAgent.key)
        assertTrue(resolvedAgent.focusComposer)
    }

    @Test
    fun widgetProvidersDeclareConfigureActivityAndReconfigurableFeature() {
        for (resId in listOf(R.xml.main_chat_widget_info, R.xml.ask_persona_widget_info)) {
            val parser = context.resources.getXml(resId)
            var configure: String? = null
            var features: String? = null
            while (parser.eventType != XmlPullParser.END_DOCUMENT) {
                if (parser.eventType == XmlPullParser.START_TAG && parser.name == "appwidget-provider") {
                    for (i in 0 until parser.attributeCount) {
                        when (parser.getAttributeName(i)) {
                            "configure" -> configure = parser.getAttributeValue(i)
                            "widgetFeatures" -> features = parser.getAttributeValue(i)
                        }
                    }
                }
                parser.next()
            }
            assertEquals("persona.platform.shortcuts.ConversationWidgetConfigureActivity", configure)
            assertNotNull("Expected widgetFeatures on xml resource", features)
            assertTrue("Expected reconfigurable feature flag, got $features", features == "0x1" || features == "1" || features?.contains("reconfigurable") == true)
        }
    }
}
