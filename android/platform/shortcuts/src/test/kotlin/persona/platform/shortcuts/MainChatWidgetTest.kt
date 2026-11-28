package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProviderInfo
import android.content.ComponentName
import android.content.Context
import android.content.res.Configuration
import android.util.TypedValue
import android.view.View
import android.widget.TextView
import androidx.glance.color.DayNightColorProvider
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.xmlpull.v1.XmlPullParser
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.Session
import persona.core.contracts.SessionCompaction
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.PairingRequest
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.FleetState
import persona.platform.notify.FleetFeed
import persona.platform.notify.FleetSource
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonObject
import org.robolectric.util.ReflectionHelpers
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ColorSchemeChoice
import persona.core.designsystem.PersonaState
import persona.core.designsystem.SystemSurfaces
import persona.core.designsystem.ThemeColor
import persona.core.navigation.AppTab
import persona.core.navigation.AskPersonaLink
import persona.core.navigation.NewTodoLink
import persona.core.navigation.NotificationRoute
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class MainChatWidgetTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val mainKey = SessionKey("hub", "main-chat")

    @After
    fun tearDown() {
        MainChatWidgetStore.clear(context)
    }

    private fun localizedContext(tag: String): Context {
        val locale = Locale.forLanguageTag(tag)
        val config = Configuration(context.resources.configuration).apply { setLocale(locale) }
        return context.createConfigurationContext(config)
    }

    @Test
    fun unpairedStateIsCleanAndRoutesToApp() {
        val snapshot = MainChatWidgetState.build(
            paired = false,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(Session("hub", "main-chat", assistant = true, status = "working", lastText = "Secret")),
            interactions = listOf(Interaction("ix1", "hub", "main-chat", kind = "question", questions = listOf(Question("Secret?")))),
            resources = context.resources,
        )
        assertFalse(snapshot.paired)
        assertEquals(MainChatPhase.Unpaired, snapshot.phase)
        assertEquals(PersonaState.Idle, snapshot.phase.personaState)
        assertEquals("", snapshot.summary)
        assertEquals(0, snapshot.pendingCount)
        assertNull(snapshot.pending)
        assertTrue(snapshot.todos.isEmpty())

        val plan = MainChatWidgetLayout.compute(
            widthDp = 290f,
            heightDp = 150f,
            fontScale = 1f,
            paired = false,
            hasPending = false,
            hasStatusBadge = false,
            availableTodos = 0,
        )
        assertFalse(plan.showHeaderStatus)
        assertFalse(plan.showHeaderTime)
        assertFalse(plan.showActionBar)
        assertFalse(plan.showPendingBanner)
        assertTrue(plan.summaryMaxLines in 1..2)
        assertNull(SurfaceLinks.app(context).data)
    }

    @Test
    fun idleStateShowsSanitizedSummaryOrEmptyWithoutFabricatedData() {
        val emptySnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(Session("hub", "main-chat", assistant = true, status = "idle", state = "idle")),
            interactions = emptyList(),
            resources = context.resources,
        )
        assertTrue(emptySnapshot.paired)
        assertEquals(MainChatPhase.Idle, emptySnapshot.phase)
        assertEquals(PersonaState.Idle, emptySnapshot.phase.personaState)
        assertEquals("", emptySnapshot.summary)
        assertEquals(0, emptySnapshot.pendingCount)
        assertNull(emptySnapshot.pending)

        val repliedSnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(
                Session(
                    machineId = "hub",
                    sessionId = "main-chat",
                    assistant = true,
                    status = "idle",
                    state = "idle",
                    lastText = "**Trip plan** is ready for Friday morning.",
                    updatedAt = "2026-11-23T09:15:00Z",
                ),
            ),
            interactions = emptyList(),
            resources = context.resources,
        )
        assertEquals(MainChatPhase.Idle, repliedSnapshot.phase)
        assertEquals("Trip plan is ready for Friday morning.", repliedSnapshot.summary)
        assertEquals("2026-11-23T09:15:00Z", repliedSnapshot.updatedAt)

        val enContext = localizedContext("en")
        val sameDay = MainChatText.formatHeaderTime(
            updatedAt = "2026-11-23T09:15:00Z",
            context = enContext,
            now = Instant.parse("2026-11-23T12:00:00Z"),
            zone = ZoneOffset.UTC,
        )
        assertNotNull(sameDay)
        assertTrue(sameDay!!, sameDay.contains("9:15") || sameDay.contains("09:15"))

        val earlierDay = MainChatText.formatHeaderTime(
            updatedAt = "2026-11-21T09:15:00Z",
            context = enContext,
            now = Instant.parse("2026-11-23T12:00:00Z"),
            zone = ZoneOffset.UTC,
        )
        assertNotNull(earlierDay)
        assertTrue(earlierDay!!, earlierDay.contains("Nov") && earlierDay.contains("21"))
    }

    @Test
    fun workingStartingAndCompactingMapToThinkingExpression() {
        val working = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(Session("hub", "main-chat", assistant = true, status = "working", state = "working")),
            interactions = emptyList(),
            resources = context.resources,
        )
        assertEquals(MainChatPhase.Working, working.phase)
        assertEquals(PersonaState.Thinking, working.phase.personaState)

        val starting = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(Session("hub", "main-chat", assistant = true, status = "idle", state = "idle", starting = true)),
            interactions = emptyList(),
            resources = context.resources,
        )
        assertEquals(MainChatPhase.Working, starting.phase)

        val compacting = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey),
            sessions = listOf(
                Session(
                    machineId = "hub",
                    sessionId = "main-chat",
                    assistant = true,
                    status = "idle",
                    state = "idle",
                    compaction = SessionCompaction(state = "running"),
                ),
            ),
            interactions = emptyList(),
            resources = context.resources,
        )
        assertEquals(MainChatPhase.Working, compacting.phase)
    }

    @Test
    fun pendingInteractionsPrioritizeMainChatAndSupervisedTasksAndRouteDirectly() {
        val enRes = localizedContext("en").resources
        val taskAgentKey = SessionKey("mac", "task-agent-1")
        val unrelatedAgentKey = SessionKey("mac", "manual-agent")
        val leaderAgentKey = SessionKey("mac", "leader-agent")

        val tree = DirectionTree(
            directions = listOf(
                Direction(
                    id = "leader-1",
                    name = "Release",
                    secretary = false,
                    tasks = listOf(
                        DirectionTask(
                            id = "t-leader",
                            title = "Cut release branch",
                            state = DirectionTaskState.Working,
                            agents = listOf(DirectionAgent("mac", "leader-agent")),
                        ),
                    ),
                ),
                Direction(
                    id = mainKey.sessionId,
                    secretary = true,
                    tasks = listOf(
                        DirectionTask(
                            id = "t-main-1",
                            title = "Book Kyoto train tickets",
                            state = DirectionTaskState.NeedsYou,
                            agents = listOf(DirectionAgent("mac", "task-agent-1")),
                        ),
                        DirectionTask(
                            id = "t-main-2",
                            title = "Prepare travel checklist",
                            state = DirectionTaskState.Planned,
                        ),
                    ),
                ),
            ),
        )

        val mainQuestion = Interaction(
            id = "ix-main-q",
            machineId = mainKey.machineId,
            sessionId = mainKey.sessionId,
            kind = "question",
            createdAt = "2026-11-23T10:00:00Z",
            questions = listOf(Question("Should I confirm the Friday evening flight?")),
        )
        val supervisedPlan = Interaction(
            id = "ix-task-plan",
            machineId = taskAgentKey.machineId,
            sessionId = taskAgentKey.sessionId,
            kind = "plan",
            createdAt = "2026-11-23T10:05:00Z",
            summary = "Reserve hotel and train passes",
        )
        val delegatingQuestion = Interaction(
            id = "ix-delegating",
            machineId = mainKey.machineId,
            sessionId = mainKey.sessionId,
            kind = "question",
            createdAt = "2026-11-23T10:10:00Z",
            delegating = true,
            questions = listOf(Question("Internal delegated check")),
        )
        val unrelatedPermission = Interaction(
            id = "ix-unrelated",
            machineId = unrelatedAgentKey.machineId,
            sessionId = unrelatedAgentKey.sessionId,
            kind = "permission",
            createdAt = "2026-11-23T10:12:00Z",
            summary = "Run cargo test",
        )
        val leaderPermission = Interaction(
            id = "ix-leader",
            machineId = leaderAgentKey.machineId,
            sessionId = leaderAgentKey.sessionId,
            kind = "permission",
            createdAt = "2026-11-23T10:12:00Z",
            summary = "Publish tag",
        )

        val sessions = listOf(
            Session(mainKey.machineId, mainKey.sessionId, assistant = true, status = "blocked", state = "needs_you", lastText = "Waiting on your choice."),
            Session(taskAgentKey.machineId, taskAgentKey.sessionId, assistant = false, startedByPersona = true, status = "blocked", state = "needs_you"),
            Session(unrelatedAgentKey.machineId, unrelatedAgentKey.sessionId, assistant = false, startedByPersona = false, status = "blocked", state = "needs_you"),
            Session(leaderAgentKey.machineId, leaderAgentKey.sessionId, assistant = false, startedByPersona = true, status = "blocked", state = "needs_you"),
        )
        val assistantSnapshot = AssistantSnapshot(mainSession = mainKey, conversations = listOf(sessions.first()), directionTree = tree)

        val snapshot = MainChatWidgetState.build(
            paired = true,
            assistant = assistantSnapshot,
            sessions = sessions,
            interactions = listOf(supervisedPlan, delegatingQuestion, unrelatedPermission, leaderPermission, mainQuestion),
            resources = enRes,
        )

        assertEquals(MainChatPhase.NeedsYou, snapshot.phase)
        assertEquals(PersonaState.NeedsYou, snapshot.phase.personaState)
        assertEquals(2, snapshot.pendingCount)
        val leadPending = requireNotNull(snapshot.pending)
        assertEquals("ix-main-q", leadPending.id)
        assertEquals("Should I confirm the Friday evening flight?", leadPending.title)
        assertEquals(true, leadPending.assistant)

        // Direct click routes through SurfaceLinks.request to the exact conversation & interaction card.
        val pendingIntent = SurfaceLinks.request(context, leadPending.toInteraction()) { leadPending.assistant }
        val parsed = NotificationRoute.parse(pendingIntent.data!!)
        assertEquals(AppTab.Persona, parsed?.tab)
        assertEquals(mainKey.machineId, parsed?.chat?.machineId)
        assertEquals(mainKey.sessionId, parsed?.chat?.sessionId)
        assertEquals("ix-main-q", parsed?.request)

        // When only the supervised task interaction remains, it is surfaced and routed to Agents tab.
        val taskOnlySnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = assistantSnapshot,
            sessions = sessions,
            interactions = listOf(supervisedPlan, unrelatedPermission, leaderPermission),
            resources = enRes,
        )
        assertEquals(MainChatPhase.NeedsYou, taskOnlySnapshot.phase)
        assertEquals(1, taskOnlySnapshot.pendingCount)
        val taskPending = requireNotNull(taskOnlySnapshot.pending)
        assertEquals("ix-task-plan", taskPending.id)
        assertEquals(false, taskPending.assistant)
        val taskIntent = SurfaceLinks.request(context, taskPending.toInteraction()) { taskPending.assistant }
        val taskParsed = NotificationRoute.parse(taskIntent.data!!)
        assertEquals(AppTab.Agents, taskParsed?.tab)
        assertEquals(taskAgentKey.sessionId, taskParsed?.chat?.sessionId)
        assertEquals("ix-task-plan", taskParsed?.request)

        // Urgent open to-dos are ordered by urgency and capped at 2.
        assertEquals(2, snapshot.todos.size)
        assertEquals("Book Kyoto train tickets", snapshot.todos[0].title)
        assertEquals(DirectionTaskState.NeedsYou, snapshot.todos[0].taskState)
    }

    @Test
    fun pendingInteractionsExcludeOtherHubGroupsAndOtherManagers() {
        val enRes = localizedContext("en").resources
        val otherGroup = "g1a2b3c4d"
        val thirdGroup = "g9f8e7d6c"

        // 1. Primary hub selected: Persona-started sessions and pending cards from another hub group must be excluded.
        val primaryMain = Session(mainKey.machineId, mainKey.sessionId, assistant = true, status = "idle", state = "idle", lastText = "Primary hub idle.")
        val primaryOwnAgent = Session("mac", "agent-primary", assistant = false, startedByPersona = true, status = "idle", state = "idle")
        val otherHubAgent = Session("mac~$otherGroup", "agent-other-hub", assistant = false, startedByPersona = true, status = "blocked", state = "needs_you")
        val otherManagerAgent = Session("mac", "agent-other-mgr", assistant = false, startedByPersona = true, manager = "ops", status = "blocked", state = "needs_you")

        val otherHubInteraction = Interaction(
            id = "ix-other-hub",
            machineId = "mac~$otherGroup",
            sessionId = "agent-other-hub",
            kind = "permission",
            summary = "Secret action on secondary hub",
        )
        val otherManagerInteraction = Interaction(
            id = "ix-other-mgr",
            machineId = "mac",
            sessionId = "agent-other-mgr",
            kind = "question",
            manager = "ops",
            questions = listOf(Question("Secret ops manager question?")),
        )

        val primarySnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = mainKey, conversations = listOf(primaryMain)),
            sessions = listOf(primaryMain, primaryOwnAgent, otherHubAgent, otherManagerAgent),
            interactions = listOf(otherHubInteraction, otherManagerInteraction),
            resources = enRes,
        )
        assertEquals(MainChatPhase.Idle, primarySnapshot.phase)
        assertEquals(0, primarySnapshot.pendingCount)
        assertNull(primarySnapshot.pending)
        assertEquals("Primary hub idle.", primarySnapshot.summary)

        // 2. Secondary hub selected: primary and third hubs' Persona-started sessions are excluded, while secondary hub's own stays included.
        val secondaryMainKey = SessionKey("hub~$otherGroup", "main-chat")
        val secondaryMain = Session(secondaryMainKey.machineId, secondaryMainKey.sessionId, assistant = true, status = "idle", state = "idle")
        val primaryHubBlockedAgent = Session("mac", "agent-primary-blocked", assistant = false, startedByPersona = true, status = "blocked", state = "needs_you")
        val thirdHubBlockedAgent = Session("mac~$thirdGroup", "agent-third-blocked", assistant = false, startedByPersona = true, status = "blocked", state = "needs_you")
        val primaryHubInteraction = Interaction(
            id = "ix-primary-hub",
            machineId = "mac",
            sessionId = "agent-primary-blocked",
            kind = "question",
            questions = listOf(Question("Primary hub private question?")),
        )
        val thirdHubInteraction = Interaction(
            id = "ix-third-hub",
            machineId = "mac~$thirdGroup",
            sessionId = "agent-third-blocked",
            kind = "permission",
            summary = "Third hub private approval",
        )
        val secondarySnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = secondaryMainKey, conversations = listOf(secondaryMain)),
            sessions = listOf(secondaryMain, otherHubAgent, primaryHubBlockedAgent, thirdHubBlockedAgent),
            interactions = listOf(primaryHubInteraction, thirdHubInteraction, otherHubInteraction),
            resources = enRes,
        )
        assertEquals(MainChatPhase.NeedsYou, secondarySnapshot.phase)
        assertEquals(1, secondarySnapshot.pendingCount)
        assertEquals("ix-other-hub", secondarySnapshot.pending?.id)

        // 3. Non-default manager selected: unstamped default-manager sessions/cards are excluded, while own manager's stay included.
        val opsMainKey = SessionKey("hub", "ops-main")
        val opsMain = Session("hub", "ops-main", assistant = true, manager = "ops", status = "idle", state = "idle")
        val defaultManagerAgent = Session("mac", "agent-default-mgr", assistant = false, startedByPersona = true, manager = null, status = "blocked", state = "needs_you")
        val defaultManagerInteraction = Interaction(
            id = "ix-default-mgr",
            machineId = "mac",
            sessionId = "agent-default-mgr",
            kind = "question",
            questions = listOf(Question("Default manager question?")),
        )
        val opsSnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(mainSession = opsMainKey, conversations = listOf(opsMain), manager = "ops", managerId = "ops"),
            sessions = listOf(opsMain, defaultManagerAgent, otherManagerAgent),
            interactions = listOf(defaultManagerInteraction, otherManagerInteraction),
            resources = enRes,
        )
        assertEquals(MainChatPhase.NeedsYou, opsSnapshot.phase)
        assertEquals(1, opsSnapshot.pendingCount)
        assertEquals("ix-other-mgr", opsSnapshot.pending?.id)

        // 4. Empty selected assistant snapshot must never absorb Persona-started sessions from other hubs.
        val emptyAnchorSnapshot = MainChatWidgetState.build(
            paired = true,
            assistant = AssistantSnapshot(),
            sessions = listOf(otherHubAgent, primaryHubBlockedAgent),
            interactions = listOf(otherHubInteraction, primaryHubInteraction),
            resources = enRes,
        )
        assertEquals(MainChatPhase.Idle, emptyAnchorSnapshot.phase)
        assertEquals(0, emptyAnchorSnapshot.pendingCount)
        assertNull(emptyAnchorSnapshot.pending)
    }

    @Test
    fun textSanitizerStripsContextTagsMarkdownPathsAndInternalIds() {
        val raw = """
            <persona-context kind="context" title="Memory" url="">Secret internal context block</persona-context>
            <jarvis-context kind="edit" title="Legacy">Old context text</jarvis-context>
            <system-reminder>Do not leak system prompt</system-reminder>
            <persona-file path="/Users/alice/secret.txt" />
            ## **Weekend summary**
            - Booked [Kyoto Ryokan](https://example.com/hotel) for `Friday`
            - Checked /Users/alice/projects/persona/MainChatWidget.kt and ~/private/notes.md
            - Ref sess_9f8e7d6c5b4a and 550e8400-e29b-41d4-a716-446655440000 via claude-opus-4-6
            [Image #1]
            <attached_images>/tmp/photo.png</attached_images>
        """.trimIndent()

        val cleaned = MainChatText.sanitize(raw)
        assertFalse(cleaned, cleaned.contains("persona-context"))
        assertFalse(cleaned, cleaned.contains("jarvis-context"))
        assertFalse(cleaned, cleaned.contains("Secret internal"))
        assertFalse(cleaned, cleaned.contains("Old context"))
        assertFalse(cleaned, cleaned.contains("system-reminder"))
        assertFalse(cleaned, cleaned.contains("/Users/alice"))
        assertFalse(cleaned, cleaned.contains("~/private"))
        assertFalse(cleaned, cleaned.contains("sess_9f8e7d6c5b4a"))
        assertFalse(cleaned, cleaned.contains("550e8400-e29b-41d4-a716-446655440000"))
        assertFalse(cleaned, cleaned.contains("claude-opus-4-6"))
        assertFalse(cleaned, cleaned.contains("**"))
        assertFalse(cleaned, cleaned.contains("##"))
        assertFalse(cleaned, cleaned.contains("[Image #1]"))
        assertTrue(cleaned, cleaned.contains("Weekend summary"))
        assertTrue(cleaned, cleaned.contains("Booked Kyoto Ryokan for Friday"))

        // Unclosed context tag must also never leak raw XML.
        val unclosed = "Ready to go <persona-context kind=\"context\">unclosed raw payload"
        assertEquals("Ready to go", MainChatText.sanitize(unclosed))

        // Emoji clipping never splits a surrogate pair.
        val longEmoji = "a".repeat(19) + "🎉🎊✨ done"
        val clipped = MainChatText.sanitize(longEmoji, maxChars = 20)
        assertFalse(clipped, clipped.dropLast(1).last().isHighSurrogate())

        // Stripping an internal ID right after an opening parenthesis must not leave a leading comma or semicolon.
        assertEquals(
            "Deployment ready (zero extra cost)",
            MainChatText.sanitize("Deployment ready (todo_1234abcd, zero extra cost)"),
        )
        assertEquals(
            "已启动部署（零额外费用）",
            MainChatText.sanitize("已启动部署（todo_1234abcd，零额外费用）"),
        )
        assertEquals(
            "Task queued (ready)",
            MainChatText.sanitize("Task queued (sess_1234abcd; ready)"),
        )

        // Long summaries up to 400 characters stay intact for multi-line cards, while longer inputs clip cleanly.
        val mediumSummary = "Sentence ready for review. ".repeat(12).trim()
        assertTrue(mediumSummary.length in 300..399)
        assertEquals(mediumSummary, MainChatText.sanitize(mediumSummary))
        val overLimit = "Long update line. ".repeat(30).trim()
        val clippedSummary = MainChatText.sanitize(overLimit)
        assertTrue(clippedSummary.endsWith("…"))
        assertTrue(clippedSummary.length <= 401)

        // Pending title falls back to localized section 5.6 copy if raw input is purely an internal ID/path.
        val enRes = localizedContext("en").resources
        val scrubOnly = Interaction(
            id = "ix-raw",
            machineId = "mac",
            sessionId = "s1",
            kind = "plan",
            summary = "/Users/dev/project/file.kt sess_12345678",
        )
        assertEquals("Plan awaiting approval", MainChatText.pendingTitle(scrubOnly, enRes))
    }

    @Test
    fun adaptiveLayoutNeverExceedsHeightBudgetAcrossSizesAnd2xFontScale() {
        val sizes = listOf(
            110f to 100f, // Minimum resize 2x2
            140f to 140f, // Standard 2x2
            170f to 150f, // Wide 2x2
            250f to 140f, // Compact 4x2
            290f to 150f, // Default 4x2
            320f to 170f, // Tall 4x2
            290f to 180f, // Tall 4x2 (high-density launcher lower bound)
            290f to 190f, // Tall 4x2 (modern flagship launcher)
            320f to 195f, // Tall 4x2 (wide flagship launcher)
            320f to 205f, // Tall 4x2 (just below 4x3 threshold)
            280f to 240f, // Standard 4x3
            290f to 250f, // Standard tall 4x3
            320f to 270f, // Spacious 4x3
        )
        val scales = listOf(1.0f, 1.35f, 1.5f, 2.0f)

        for ((w, h) in sizes) {
            for (scale in scales) {
                for (paired in listOf(false, true)) {
                    for (hasPending in listOf(false, true)) {
                        for (todos in listOf(0, 2)) {
                            val plan = MainChatWidgetLayout.compute(
                                widthDp = w,
                                heightDp = h,
                                fontScale = scale,
                                paired = paired,
                                hasPending = paired && hasPending,
                                hasStatusBadge = paired,
                                availableTodos = if (paired) todos else 0,
                            )
                            assertTrue(
                                "Overflow for ${w}x$h @ ${scale}x (paired=$paired pending=$hasPending todos=$todos): " +
                                    "estimated=${plan.estimatedHeightDp} > height=$h ($plan)",
                                plan.estimatedHeightDp <= h + 0.01f,
                            )
                        }
                    }
                }
            }
        }
    }

    @Test
    fun adaptiveLayoutExpectationsFor2x2And4x2And4x3() {
        // 2x2 omits bottom quick action bar & header time and uses remaining height for summary lines.
        val small = MainChatWidgetLayout.compute(140f, 140f, fontScale = 1f, paired = true, hasPending = false, hasStatusBadge = true, availableTodos = 2)
        assertFalse(small.showActionBar)
        assertFalse(small.showHeaderTime)
        assertEquals(0, small.todoRows)
        assertTrue("2x2 should fill vertical space (>= 3 lines), got ${small.summaryMaxLines}", small.summaryMaxLines >= 3)

        // 2x2 at 2.0x font scale still fits cleanly without clipping.
        val small2x = MainChatWidgetLayout.compute(110f, 100f, fontScale = 2.0f, paired = true, hasPending = true, hasStatusBadge = true, availableTodos = 2)
        assertFalse(small2x.showActionBar)
        assertTrue(small2x.showPendingBanner)
        assertTrue(small2x.estimatedHeightDp <= 100f)

        // 4x2 compact/default shows 2 summary lines, quick action bar, and header timestamp.
        val medium = MainChatWidgetLayout.compute(290f, 150f, fontScale = 1f, paired = true, hasPending = false, hasStatusBadge = true, availableTodos = 2)
        assertTrue(medium.showActionBar)
        assertFalse(medium.expandedActionBar)
        assertTrue(medium.showHeaderTime)
        assertEquals(2, medium.summaryMaxLines)
        assertEquals(0, medium.todoRows)

        // Tall 4x2 cards (190-205dp) dynamically expand summary to >= 4 lines instead of capping at 2.
        for ((w, h) in listOf(290f to 190f, 320f to 195f, 320f to 205f)) {
            val tallMedium = MainChatWidgetLayout.compute(w, h, fontScale = 1f, paired = true, hasPending = false, hasStatusBadge = true, availableTodos = 2)
            assertTrue(tallMedium.showActionBar)
            assertEquals(0, tallMedium.todoRows)
            assertTrue("Tall 4x2 ${w}x$h should fit >= 4 summary lines, got ${tallMedium.summaryMaxLines}", tallMedium.summaryMaxLines >= 4)
            assertTrue(tallMedium.estimatedHeightDp <= h + 0.01f)
        }

        // 4x2 at 2.0x drops action bar automatically to keep header + content unclipped.
        val medium2x = MainChatWidgetLayout.compute(290f, 150f, fontScale = 2.0f, paired = true, hasPending = false, hasStatusBadge = true, availableTodos = 2)
        assertFalse(medium2x.showActionBar)
        assertTrue(medium2x.summaryMaxLines >= 1)
        assertTrue(medium2x.estimatedHeightDp <= 150f)

        // 4x3 without todos allocates all remaining vertical space to summary (>= 5 lines) + quick action bar.
        val largeNoTodos = MainChatWidgetLayout.compute(290f, 250f, fontScale = 1f, paired = true, hasPending = false, hasStatusBadge = false, availableTodos = 0)
        assertTrue(largeNoTodos.showActionBar)
        assertTrue("4x3 without todos should fit >= 5 summary lines, got ${largeNoTodos.summaryMaxLines}", largeNoTodos.summaryMaxLines >= 5)
        assertEquals(0, largeNoTodos.todoRows)

        // 4x3 with urgent open todos keeps 2 todo rows and gives leftover height to summary (>= 3 lines).
        val largeWithTodos = MainChatWidgetLayout.compute(290f, 250f, fontScale = 1f, paired = true, hasPending = false, hasStatusBadge = false, availableTodos = 2)
        assertTrue(largeWithTodos.showActionBar)
        assertTrue("4x3 with 2 todos should fit >= 3 summary lines, got ${largeWithTodos.summaryMaxLines}", largeWithTodos.summaryMaxLines >= 3)
        assertEquals(2, largeWithTodos.todoRows)

        // 4x3 with both pending banner and open todos preserves >= 2 summary lines alongside urgent todos.
        val largePendingTodos = MainChatWidgetLayout.compute(280f, 240f, fontScale = 1f, paired = true, hasPending = true, hasStatusBadge = true, availableTodos = 2)
        assertTrue(largePendingTodos.showPendingBanner)
        assertTrue(largePendingTodos.showActionBar)
        assertTrue(largePendingTodos.summaryMaxLines >= 2)
        assertTrue(largePendingTodos.todoRows >= 1)
        assertTrue(largePendingTodos.estimatedHeightDp <= 240f)

        // 4x3 at 2.0x expands action bar height and trims todos to prevent clipping.
        val large2x = MainChatWidgetLayout.compute(290f, 250f, fontScale = 2.0f, paired = true, hasPending = true, hasStatusBadge = true, availableTodos = 2)
        assertTrue(large2x.showPendingBanner)
        assertTrue(large2x.expandedActionBar)
        assertEquals(0, large2x.todoRows)
        assertFalse(large2x.showHeaderStatus)
        assertTrue(large2x.estimatedHeightDp <= 250f)
    }

    @Test
    fun doubleFontScaleFitsRealTextViewsAcrossAllStyles() {
        val scaledConfig = Configuration(context.resources.configuration).apply { fontScale = 2.0f }
        val scaledContext = context.createConfigurationContext(scaledConfig)
        val density = scaledContext.resources.displayMetrics.density.coerceAtLeast(1f)

        fun measureTextDp(sp: Float, maxLines: Int, text: String, maxWidthDp: Float): Float {
            val view = TextView(scaledContext).apply {
                setTextSize(TypedValue.COMPLEX_UNIT_SP, sp)
                this.maxLines = maxLines
                this.text = text
            }
            val widthPx = (maxWidthDp * density).toInt().coerceAtLeast(1)
            view.measure(
                View.MeasureSpec.makeMeasureSpec(widthPx, View.MeasureSpec.AT_MOST),
                View.MeasureSpec.makeMeasureSpec(0, View.MeasureSpec.UNSPECIFIED),
            )
            return view.measuredHeight / density
        }

        val sampleSizes = listOf(
            110f to 100f,
            140f to 140f,
            290f to 150f,
            290f to 190f,
            320f to 195f,
            320f to 205f,
            280f to 240f,
            290f to 250f,
            320f to 270f,
        )
        val longSummarySample =
            "Your trip itinerary is ready for Friday morning with confirmed flights, hotel check-in details, " +
                "rail passes, airport transfers, dining reservations, and daily schedule reminders prepared across every stop."
        for (style in AppStyle.entries) {
            val type = SystemSurfaces.type(style)
            for ((w, h) in sampleSizes) {
                for (paired in listOf(false, true)) {
                    for (hasPending in listOf(false, true)) {
                        for (todos in if (paired) listOf(0, 2) else listOf(0)) {
                            val plan = MainChatWidgetLayout.compute(
                                widthDp = w,
                                heightDp = h,
                                fontScale = 2.0f,
                                paired = paired,
                                hasPending = paired && hasPending,
                                hasStatusBadge = paired,
                                availableTodos = todos,
                            )
                            val pad = when {
                                plan.tightPadding -> 4f
                                plan.compactPadding -> 8f
                                else -> 12f
                            }
                            val sectionGap = when {
                                plan.tightPadding -> 2f
                                plan.compactPadding -> 4f
                                else -> 6f
                            }
                            val innerWidth = (w - pad * 2f).coerceAtLeast(1f)
                            val faceDp = if (plan.compactHeader) 24f else 28f
                            val titleSp = if (plan.compactHeader) type.caption.fontSize.value else type.callout.fontSize.value
                            val headerTextDp = measureTextDp(titleSp, 1, "Persona", innerWidth)
                            var totalDp = pad * 2f + maxOf(faceDp, headerTextDp)

                            if (!paired) {
                                if (plan.showUnpairedHeadline) {
                                    totalDp += sectionGap
                                    totalDp += measureTextDp(titleSp, 1, "Not paired", innerWidth)
                                    if (plan.summaryMaxLines > 0) {
                                        totalDp += 2f
                                        totalDp += measureTextDp(
                                            type.caption.fontSize.value,
                                            plan.summaryMaxLines,
                                            "Open Persona to connect your hub right away on this device",
                                            innerWidth,
                                        )
                                    }
                                }
                            } else {
                                if (plan.showPendingBanner) {
                                    val vPad = if (plan.compactPendingBanner) 4f else 12f
                                    val pendingDp = measureTextDp(type.caption.fontSize.value, 1, "Plan awaiting approval", innerWidth)
                                    totalDp += sectionGap + vPad + pendingDp
                                }
                                if (plan.summaryMaxLines > 0) {
                                    totalDp += sectionGap
                                    totalDp += measureTextDp(
                                        type.caption.fontSize.value,
                                        plan.summaryMaxLines,
                                        longSummarySample,
                                        innerWidth,
                                    )
                                }
                                if (plan.todoRows > 0) {
                                    totalDp += 6f
                                    repeat(plan.todoRows) {
                                        totalDp += 4f + measureTextDp(type.micro.fontSize.value, 1, "Book Kyoto train tickets", innerWidth)
                                    }
                                }
                                if (plan.showActionBar) {
                                    val barMin = if (plan.expandedActionBar) 48f else 40f
                                    val askDp = measureTextDp(type.caption.fontSize.value, 1, "Ask Persona…", innerWidth) + 16f
                                    totalDp += 8f + maxOf(barMin, askDp)
                                }
                            }

                            assertTrue(
                                "Real TextView overflow for $style ${w}x$h @ 2.0x (paired=$paired pending=$hasPending todos=$todos): " +
                                    "measured=$totalDp > height=$h ($plan)",
                                totalDp <= h + 0.5f,
                            )
                        }
                    }
                }
            }
        }
    }

    @Test
    fun widgetLookFollowsAllStylesThemeColorsAndColorScheme() {
        for (style in AppStyle.entries) {
            for (color in listOf(ThemeColor.Style) + ThemeColor.presets) {
                val look = MainChatWidgetLook.of(style, color, ColorSchemeChoice.System)
                val expectedLight = SystemSurfaces.palette(style, dark = false, color = color)
                val expectedDark = SystemSurfaces.palette(style, dark = true, color = color)
                assertEquals(expectedLight.surfaceOpaque, look.light.surfaceOpaque)
                assertEquals(expectedDark.surfaceOpaque, look.dark.surfaceOpaque)
                assertEquals(expectedLight.accent, look.light.accent)
                assertEquals(expectedDark.accent, look.dark.accent)
                val cardProvider = look.card as DayNightColorProvider
                assertEquals(expectedLight.surfaceOpaque, cardProvider.day)
                assertEquals(expectedDark.surfaceOpaque, cardProvider.night)
            }
        }

        val pinnedDark = MainChatWidgetLook.of(AppStyle.Glass, ThemeColor.Coral, ColorSchemeChoice.Dark)
        assertTrue(pinnedDark.isDark(context))
        val darkCard = pinnedDark.card as DayNightColorProvider
        assertEquals(darkCard.day, darkCard.night)
        assertEquals(pinnedDark.dark.surfaceOpaque, darkCard.day)

        val pinnedLight = MainChatWidgetLook.of(AppStyle.Brief, ThemeColor.Blue, ColorSchemeChoice.Light)
        assertFalse(pinnedLight.isDark(context))
        val lightCard = pinnedLight.card as DayNightColorProvider
        assertEquals(lightCard.day, lightCard.night)
        assertEquals(pinnedLight.light.surfaceOpaque, lightCard.day)
    }

    @Test
    fun snapshotStoreRoundTripsAndEnforcesPairingScope() {
        val original = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.NeedsYou,
            mainSession = mainKey,
            summary = "Flight itinerary is ready for confirmation.",
            updatedAt = "2026-11-23T11:30:00Z",
            pendingCount = 3,
            pending = MainChatPendingItem(
                id = "ix-1",
                machineId = "hub",
                sessionId = "main-chat",
                assistant = true,
                kind = "question",
                title = "Confirm Friday 18:00 departure?",
            ),
            todos = listOf(
                MainChatTodoItem("t1", "Book hotel", DirectionTaskState.NeedsYou.wire),
                MainChatTodoItem("t2", "Pack charger", DirectionTaskState.Planned.wire),
            ),
        )
        val hubA = "hub-a/scope"
        val hubB = "hub-b/scope"
        val prefs = context.getSharedPreferences(MainChatWidgetStore.PREFS_NAME, Context.MODE_PRIVATE)

        MainChatWidgetStore.save(context, hubA, original)
        assertEquals(original, MainChatWidgetStore.read(context, hubA))
        assertEquals(hubA, prefs.getString("pairing", null))

        // Reading with a different active pairing returns null and wipes the previous hub's snapshot.
        assertNull(MainChatWidgetStore.read(context, hubB))
        assertTrue(prefs.all.isEmpty())

        // Legacy unscoped snapshot (missing pairing key and JSON pairing field) is treated as mismatch and cleared.
        prefs.edit().putString("snapshot", """{"paired":true,"phase":"needs_you","summary":"Legacy leak"}""").commit()
        assertNull(MainChatWidgetStore.read(context, hubA))
        assertTrue(prefs.all.isEmpty())

        // Null/blank pairing refuses to persist and clears on read.
        MainChatWidgetStore.save(context, null, original)
        assertTrue(prefs.all.isEmpty())
        MainChatWidgetStore.save(context, hubA, original)
        assertNull(MainChatWidgetStore.read(context, null))
        assertTrue(prefs.all.isEmpty())
    }

    @Test
    fun surfacesRuntimeSavesSnapshotWhenPairedAndClearsOnUnpair() = runBlocking {
        val scope = "hub/main-chat-test"
        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) scope else null
            override suspend fun fetch() = FleetState()
        })
        val sessionsFlow = MutableStateFlow(
            listOf(
                Session(
                    machineId = mainKey.machineId,
                    sessionId = mainKey.sessionId,
                    assistant = true,
                    status = "idle",
                    state = "idle",
                    lastText = "Dinner reservation is confirmed for 7 PM.",
                ),
            ),
        )
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = sessionsFlow
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = mainKey, conversations = sessionsFlow.value))
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions, onlineWarmupGraceMillis = 120L)
        try {
            runtime.start()
            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope)?.summary != "Dinner reservation is confirmed for 7 PM.") {
                    delay(25)
                }
            }
            val saved = MainChatWidgetStore.read(context, scope)
            assertNotNull(saved)
            assertTrue(saved!!.paired)
            assertEquals(MainChatPhase.Idle, saved.phase)

            // Unpairing immediately clears persisted main-chat snapshot.
            pairedFlow.value = false
            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope) != null) {
                    delay(25)
                }
            }
            assertNull(MainChatWidgetStore.read(context, scope))
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun surfacesRuntimePreservesPopulatedPersistedSnapshotDuringColdStart() = runBlocking {
        val scope = "hub/cold-start"
        val persisted = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.Idle,
            mainSession = mainKey,
            summary = "Cached summary from before process restart.",
        )
        MainChatWidgetStore.save(context, scope, persisted)

        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) scope else null
            override suspend fun fetch() = FleetState()
        })
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions)
        try {
            runtime.start()
            // Wait past the 300ms debounce window while offline and repositories remain unpopulated.
            delay(450)
            assertNull(runtime.mainChatLivePairing.value)
            assertEquals(persisted, MainChatWidgetStore.read(context, scope))
            val liveEmpty = MainChatWidgetSnapshot(paired = true, phase = MainChatPhase.Idle)
            assertEquals(persisted, MainChatWidgetState.selectSnapshot(liveEmpty, persisted, liveSettled = false))
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun surfacesRuntimeOverwritesStaleNeedsYouWhenLiveStateBecomesEmptyIdle() = runBlocking {
        val scope = "hub/live-clear"
        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) scope else null
            override suspend fun fetch() = FleetState()
        })
        val initialSession = Session(
            machineId = mainKey.machineId,
            sessionId = mainKey.sessionId,
            assistant = true,
            status = "blocked",
            state = "needs_you",
            lastText = "Approve Friday travel plan?",
        )
        val initialTree = DirectionTree(
            directions = listOf(
                Direction(
                    id = mainKey.sessionId,
                    name = "Persona",
                    secretary = true,
                    tasks = listOf(DirectionTask("t1", "Confirm itinerary", DirectionTaskState.NeedsYou)),
                ),
            ),
        )
        val sessionsFlow = MutableStateFlow(listOf(initialSession))
        val assistantFlow = MutableStateFlow(
            AssistantSnapshot(mainSession = mainKey, conversations = listOf(initialSession), directionTree = initialTree),
        )
        val interactionsFlow = MutableStateFlow(
            listOf(
                Interaction(
                    id = "ix-pending-1",
                    machineId = mainKey.machineId,
                    sessionId = mainKey.sessionId,
                    kind = "question",
                    questions = listOf(Question("Approve Friday travel plan?")),
                ),
            ),
        )
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = sessionsFlow
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = assistantFlow
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = interactionsFlow
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions)
        try {
            runtime.start()
            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope)?.phase != MainChatPhase.NeedsYou) {
                    delay(25)
                }
            }
            val staleNeedsYou = requireNotNull(MainChatWidgetStore.read(context, scope))
            assertEquals(1, staleNeedsYou.pendingCount)
            assertEquals("ix-pending-1", staleNeedsYou.pending?.id)
            assertEquals(1, staleNeedsYou.todos.size)
            assertEquals(scope, runtime.mainChatLivePairing.value)

            // User resolves the last pending item and clears/archives the main conversation.
            interactionsFlow.value = emptyList()
            sessionsFlow.value = emptyList()
            assistantFlow.value = AssistantSnapshot()

            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope)?.phase != MainChatPhase.Idle) {
                    delay(25)
                }
            }
            val cleared = requireNotNull(MainChatWidgetStore.read(context, scope))
            assertTrue(cleared.paired)
            assertEquals(MainChatPhase.Idle, cleared.phase)
            assertEquals(0, cleared.pendingCount)
            assertNull(cleared.pending)
            assertEquals("", cleared.summary)
            assertTrue(cleared.todos.isEmpty())

            // Display selection must render live idle instead of stale persisted NeedsYou once settled.
            val settled = MainChatWidgetState.isLiveSettled(
                paired = true,
                activePairing = scope,
                settledPairing = runtime.mainChatLivePairing.value,
                assistant = AssistantSnapshot(),
                sessions = emptyList(),
                interactions = emptyList(),
            )
            assertTrue(settled)
            assertEquals(cleared, MainChatWidgetState.selectSnapshot(cleared, staleNeedsYou, liveSettled = settled))
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun surfacesRuntimeSettlesGenuinelyEmptyOnlineHubAndOverwritesStaleSnapshot() = runBlocking {
        val scope = "hub/online-empty"
        val staleNeedsYou = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.NeedsYou,
            mainSession = mainKey,
            summary = "Stale summary from previous process.",
            pendingCount = 1,
            pending = MainChatPendingItem("ix-stale", mainKey.machineId, mainKey.sessionId, true, "question", "Stale question?"),
            todos = listOf(MainChatTodoItem("t-stale", "Stale to-do", DirectionTaskState.NeedsYou.wire)),
        )
        MainChatWidgetStore.save(context, scope, staleNeedsYou)

        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) scope else null
            override suspend fun fetch() = FleetState()
        })
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions, onlineWarmupGraceMillis = 120L)
        try {
            runtime.start()
            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope)?.phase != MainChatPhase.Idle) {
                    delay(25)
                }
            }
            assertEquals(scope, runtime.mainChatLivePairing.value)
            val replaced = requireNotNull(MainChatWidgetStore.read(context, scope))
            assertEquals(MainChatPhase.Idle, replaced.phase)
            assertEquals(0, replaced.pendingCount)
            assertNull(replaced.pending)
            assertEquals("", replaced.summary)
            assertTrue(replaced.todos.isEmpty())
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun surfacesRuntimeEvictsCrossPairingPersistedSnapshotOnColdStart() = runBlocking {
        val previousScope = "hub-a/private-scope"
        val activeScope = "hub-b/new-scope"
        val previousHubSnapshot = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.NeedsYou,
            mainSession = mainKey,
            summary = "Confidential summary from previous hub.",
            pendingCount = 1,
            pending = MainChatPendingItem("ix-priv", mainKey.machineId, mainKey.sessionId, true, "permission", "Private approval"),
        )
        MainChatWidgetStore.save(context, previousScope, previousHubSnapshot)

        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) activeScope else null
            override suspend fun fetch() = FleetState()
        })
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions)
        try {
            runtime.start()
            val prefs = context.getSharedPreferences(MainChatWidgetStore.PREFS_NAME, Context.MODE_PRIVATE)
            withTimeout(5_000) {
                while (prefs.all.isNotEmpty()) {
                    delay(25)
                }
            }
            assertNull(MainChatWidgetStore.read(context, activeScope))
            assertNull(MainChatWidgetStore.read(context, previousScope))
            assertTrue(prefs.all.isEmpty())
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun surfacesRuntimeWaitsForAllThreeRepositoriesBeforeOverwritingPersistedSnapshot() = runBlocking {
        val scope = "hub/partial-restore"
        val persisted = MainChatWidgetSnapshot(
            paired = true,
            phase = MainChatPhase.NeedsYou,
            mainSession = mainKey,
            summary = "Complete persisted summary from previous run.",
            pendingCount = 1,
            pending = MainChatPendingItem("ix-persisted", mainKey.machineId, mainKey.sessionId, true, "question", "Persisted prompt?"),
            todos = listOf(MainChatTodoItem("t1", "Persisted to-do", DirectionTaskState.NeedsYou.wire)),
        )
        MainChatWidgetStore.save(context, scope, persisted)

        val pairedFlow = MutableStateFlow(true)
        val pairing = object : PairingService {
            override val isPaired = pairedFlow
            override suspend fun pair(request: PairingRequest) = Unit
            override suspend fun unpair() { pairedFlow.value = false }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val feed = FleetFeed(context, connection, pairing, object : FleetSource {
            override fun pairing(): String? = if (pairedFlow.value) scope else null
            override suspend fun fetch() = FleetState()
        })
        val sessionsFlow = MutableStateFlow<List<Session>>(emptyList())
        val assistantFlow = MutableStateFlow(AssistantSnapshot())
        val interactionsFlow = MutableStateFlow<List<Interaction>>(emptyList())
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = sessionsFlow
            override suspend fun refresh() = Unit
        }
        val assistant = object : AssistantRepository {
            override val assistant = assistantFlow
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = interactionsFlow
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }

        val runtime = SurfacesRuntime(context, feed, sessions, assistant, interactions)
        try {
            runtime.start()

            // 1. Only interactions restores first: assistant and sessions are still placeholders.
            val restoredInteraction = Interaction(
                id = "ix-live-1",
                machineId = mainKey.machineId,
                sessionId = mainKey.sessionId,
                kind = "question",
                questions = listOf(Question("Live restored question?")),
            )
            interactionsFlow.value = listOf(restoredInteraction)
            delay(450)
            assertNull("Must not settle when only interactions restored", runtime.mainChatLivePairing.value)
            assertEquals(persisted, MainChatWidgetStore.read(context, scope))
            assertFalse(
                MainChatWidgetState.isLiveSettled(
                    paired = true,
                    activePairing = scope,
                    settledPairing = runtime.mainChatLivePairing.value,
                    assistant = assistantFlow.value,
                    sessions = sessionsFlow.value,
                    interactions = interactionsFlow.value,
                ),
            )

            // 2. Sessions restores next, while assistant is still a placeholder: still must not overwrite disk.
            val liveSession = Session(
                machineId = mainKey.machineId,
                sessionId = mainKey.sessionId,
                assistant = true,
                status = "blocked",
                state = "needs_you",
                lastText = "Updated live summary after full restore.",
            )
            sessionsFlow.value = listOf(liveSession)
            delay(450)
            assertNull("Must not settle while assistant is still uninitialized", runtime.mainChatLivePairing.value)
            assertEquals(persisted, MainChatWidgetStore.read(context, scope))

            // 3. Once assistant also restores (all 3 repos ready), live settles and persists the complete rebuilt snapshot.
            assistantFlow.value = AssistantSnapshot(mainSession = mainKey, conversations = listOf(liveSession))
            withTimeout(5_000) {
                while (MainChatWidgetStore.read(context, scope)?.summary != "Updated live summary after full restore.") {
                    delay(25)
                }
            }
            assertEquals(scope, runtime.mainChatLivePairing.value)
            val saved = requireNotNull(MainChatWidgetStore.read(context, scope))
            assertEquals(MainChatPhase.NeedsYou, saved.phase)
            assertEquals("ix-live-1", saved.pending?.id)
            assertEquals("Live restored question?", saved.pending?.title)
        } finally {
            ReflectionHelpers.getField<CoroutineScope>(runtime, "scope").cancel()
            ReflectionHelpers.getField<CoroutineScope>(feed, "scope").cancel()
            context.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().apply()
        }
    }

    @Test
    fun manifestRegistersMainChatWidgetAlongsideExistingPersonaWidget() {
        val pm = context.packageManager
        val mainReceiver = pm.getReceiverInfo(ComponentName(context, MainChatWidgetReceiver::class.java), 0)
        assertNotNull(mainReceiver)
        assertTrue(mainReceiver.exported)

        val existingReceiver = pm.getReceiverInfo(ComponentName(context, PersonaWidgetReceiver::class.java), 0)
        assertNotNull(existingReceiver)
        assertTrue(existingReceiver.exported)

        // MainChatWidget carries private conversation/approval/to-do text and must be restricted to home_screen (never keyguard).
        var widgetCategory = 0
        context.resources.getXml(R.xml.main_chat_widget_info).use { parser ->
            while (parser.eventType != XmlPullParser.END_DOCUMENT) {
                if (parser.eventType == XmlPullParser.START_TAG && parser.name == "appwidget-provider") {
                    widgetCategory = parser.getAttributeIntValue(
                        "http://schemas.android.com/apk/res/android",
                        "widgetCategory",
                        0,
                    )
                    break
                }
                parser.next()
            }
        }
        assertEquals(AppWidgetProviderInfo.WIDGET_CATEGORY_HOME_SCREEN, widgetCategory)
        assertEquals(0, widgetCategory and AppWidgetProviderInfo.WIDGET_CATEGORY_KEYGUARD)

        // Verify quick-action routes remain canonical.
        assertEquals(AskPersonaLink, SurfaceLinks.ask(context).dataString)
        assertEquals(NewTodoLink, SurfaceLinks.newTodo(context).dataString)

        val manager = AppWidgetManager.getInstance(context)
        assertNotNull(manager)
    }

    @Test
    fun allThreeLocalesProvideCompleteMainChatWidgetStrings() {
        val en = localizedContext("en")
        val zh = localizedContext("zh-Hans")
        val ja = localizedContext("ja")

        assertEquals("Persona", en.getString(R.string.shortcuts_main_chat_header_title))
        assertEquals("Persona Chat", en.getString(R.string.shortcuts_main_chat_widget_label))
        assertEquals("Persona 主对话", zh.getString(R.string.shortcuts_main_chat_widget_label))
        assertEquals("Persona チャット", ja.getString(R.string.shortcuts_main_chat_widget_label))

        assertEquals("Needs you · 2", en.getString(R.string.shortcuts_main_chat_status_needs_you, 2))
        assertEquals("等你处理 · 2", zh.getString(R.string.shortcuts_main_chat_status_needs_you, 2))
        assertEquals("要対応 · 2", ja.getString(R.string.shortcuts_main_chat_status_needs_you, 2))

        assertEquals("Thinking", en.getString(R.string.shortcuts_main_chat_status_thinking))
        assertEquals("思考中", zh.getString(R.string.shortcuts_main_chat_status_thinking))
        assertEquals("考え中", ja.getString(R.string.shortcuts_main_chat_status_thinking))

        assertEquals("Ask Persona…", en.getString(R.string.shortcuts_main_chat_ask_hint))
        assertEquals("问 Persona…", zh.getString(R.string.shortcuts_main_chat_ask_hint))
        assertEquals("Personaに質問…", ja.getString(R.string.shortcuts_main_chat_ask_hint))

        assertEquals("+2", en.getString(R.string.shortcuts_main_chat_pending_more, 2))
        assertNotEquals(
            en.getString(R.string.shortcuts_main_chat_idle_greeting),
            zh.getString(R.string.shortcuts_main_chat_idle_greeting),
        )
        assertNotEquals(
            zh.getString(R.string.shortcuts_main_chat_idle_greeting),
            ja.getString(R.string.shortcuts_main_chat_idle_greeting),
        )
    }
}
