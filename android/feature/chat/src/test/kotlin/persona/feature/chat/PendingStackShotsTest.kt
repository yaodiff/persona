package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.HomeNavigation
import persona.core.designsystem.JAppMark
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.AnswerDraft
import persona.core.domain.sort.SessionRenamer
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/**
 * Pending requests in the real chat page (#4210 T6, #4339 C10), every style, light and dark: an Agent's chat with one
 * request ("one"), with three ("three"), with two and the first folded ("folded"), and Persona's conversation with its own
 * card and its tasks' oldest question ("assistant"; "assistant-many" with two
 * cards of its own, which outgrow their place and scroll under "N pending"); "three" and "assistant-many" again at the
 * largest text size (Glass, light). It uses only what `main` has too, so the same file draws the pictures before the change. PNGs in `build/pending-stack-shots/` (or
 * `-Dpersona.shots=<dir>`). Native JVM rendering: no system bars, no glass refraction, no device.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class PendingStackShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val key = SessionKey("computer-a", "importer")
    // A fixed hour, so two runs (main and the branch) draw the same times.
    private val start = Instant.parse("2026-11-12T01:00:00Z")
    private fun at(seq: Int) = start.plusSeconds(seq * 60L).toString()
    private val exchange = listOf(
        Item("u1", 1, role = "user", kind = "text", text = "Make the importer retry once when the hub restarts", ts = at(1)),
        Item("a2", 2, role = "assistant", kind = "text", ts = at(2),
            text = "The retry is in. Before I push, I need the test run approved and the branch to push to."),
    )
    private val permission = Interaction("perm", machineId = key.machineId, sessionId = key.sessionId, kind = "permission",
        toolName = "Bash", createdAt = at(3), toolInput = buildJsonObject {
            put("command", JsonPrimitive("npm test -- --watch=false"))
            put("description", JsonPrimitive("Run the test suite"))
        })
    private val question = Interaction("ask", machineId = key.machineId, sessionId = key.sessionId, kind = "question", createdAt = at(4),
        questions = listOf(Question("Which branch should I push to?", options = listOf(QuestionOption("main"), QuestionOption("release/1.4")))))
    private val plan = Interaction("plan", machineId = key.machineId, sessionId = key.sessionId, kind = "plan", createdAt = at(5),
        summary = "Retry the import once after a restart", plan = "## Retry the import\n1. Catch the restart error\n2. Retry once\n3. Add a test")
    private val recordedPlan = plan.copy(id = "plan-then", planThen = "default")
    private val recordedPlanLarge = recordedPlan.copy(plan = "1. Catch the restart error")
    private val prepared = permission.copy(id = "prepared", canClarify = true,
        expiresAt = Instant.now().plusSeconds(3_600).toString())
    private val renamer = object : SessionRenamer {
        override fun project(source: Flow<List<Session>>) = source
        override suspend fun rename(session: Session, title: String?, tabName: String?) = false
    }

    private fun texts(): List<String> = rule.onAllNodes(SemanticsMatcher("any node") { true }, useUnmergedTree = true).fetchSemanticsNodes().flatMap { node ->
        node.config.getOrNull(SemanticsProperties.Text).orEmpty().map { it.text } +
            node.config.getOrNull(SemanticsProperties.ContentDescription).orEmpty()
    }

    private fun frames(until: (List<String>) -> Boolean, what: String) {
        val deadline = System.currentTimeMillis() + 30_000
        while (true) {
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            if (until(texts())) break
            check(System.currentTimeMillis() < deadline) { "$what never showed; shown: ${texts()}" }
            Thread.sleep(20)
        }
        repeat(20) { Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeByFrame() }
    }

    private fun save(name: String) {
        val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val output = File(System.getProperty("persona.shots") ?: "build/pending-stack-shots").also { it.mkdirs() }
        File(output, "$name.png").outputStream().use { assertTrue(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }

    private fun agentScene(name: String, requests: List<Interaction>, style: AppStyle, dark: Boolean, large: Boolean = false, fold: Boolean = false, showAnswers: Boolean = false) {
        val agent = Session(key.machineId, key.sessionId, title = "Fix the importer retry", agent = "claude", status = "blocked",
            state = "needs_you", pendingInteractions = requests.size, cwd = "/home/me/code/app")
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer A", online = true)))
            override val sessions = MutableStateFlow(listOf(agent))
            override suspend fun refresh() = Unit
        }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = exchange, loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(requests)
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val density = Density(LocalDensity.current.density, if (large) 2f else 1f)
            CompositionLocalProvider(LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, chrome = ChatChromeBindings(renamer = renamer))
                    }
                }
            }
        }
        frames({ shown -> shown.any { it.startsWith("The retry is in") } && shown.any { it == "Collapse" } }, name)
        if (fold) {
            rule.onAllNodesWithContentDescription("Collapse").onFirst().performClick()
            frames({ shown -> shown.any { it == "Expand" } }, "$name folded")
        }
        if (showAnswers) {
            rule.mainClock.autoAdvance = true
            rule.onNodeWithText("Allow").performScrollTo()
            rule.mainClock.autoAdvance = false
            frames({ shown -> "Allow" in shown }, "$name answers")
        }
        save("$name-${style.name.lowercase()}-${if (dark) "dark" else "light"}${if (large) "-large" else ""}")
    }

    private fun assistantScene(style: AppStyle, dark: Boolean, many: Boolean = false, large: Boolean = false) {
        val main = SessionKey("computer-a", "main")
        val docs = Session("computer-a", "docs", title = "Update the docs", state = "needs_you", status = "blocked", pendingInteractions = 1)
        val release = Session("computer-b", "release", title = "Prepare the release", state = "needs_you", status = "blocked", pendingInteractions = 1)
        val todos = listOf(
            Todo("task-docs", docs.title, origin = "main", agents = listOf(TodoAgent(docs.machineId, docs.sessionId)), createdAt = at(1), updatedAt = at(1)),
            Todo("task-release", release.title, origin = "main", agents = listOf(TodoAgent(release.machineId, release.sessionId)), createdAt = at(2), updatedAt = at(2)),
        )
        val own = Interaction("own", machineId = main.machineId, sessionId = main.sessionId, kind = "permission", toolName = "Bash", createdAt = at(6),
            toolInput = buildJsonObject {
                put("command", JsonPrimitive("ls ~/Downloads"))
                put("description", JsonPrimitive("List the downloads folder"))
            })
        val docsAsk = Interaction("docs-ask", machineId = docs.machineId, sessionId = docs.sessionId, kind = "question", createdAt = at(4),
            questions = listOf(Question("Should the guide cover Windows too?", options = listOf(QuestionOption("Yes"), QuestionOption("Not yet")))))
        val releaseAsk = Interaction("release-ask", machineId = release.machineId, sessionId = release.sessionId, kind = "permission",
            toolName = "Bash", summary = "Tag the release", createdAt = at(5))
        // "many": the conversation itself asks two things, so its own cards outgrow their place and scroll
        val ownAsk = Interaction("own-ask", machineId = main.machineId, sessionId = main.sessionId, kind = "question", createdAt = at(7),
            questions = listOf(Question("Which folder should I tidy first?", options = listOf(QuestionOption("Downloads"), QuestionOption("Desktop")))))
        val requests = MutableStateFlow(if (many) listOf(own, ownAsk, docsAsk, releaseAsk) else listOf(own, docsAsk, releaseAsk))
        val sessionRows = MutableStateFlow(listOf(Session(main.machineId, main.sessionId, title = "Persona", assistant = true, status = "blocked",
            state = "needs_you", pendingInteractions = 1), docs, release))
        val sessions = object : SessionsRepository {
            override val sessions = sessionRows
            override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true), Machine("computer-b", "Computer B", online = true)))
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = requests
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val api = object : TasksApi {
            override suspend fun todos() = todos
            override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo = error("not used")
            override suspend fun acceptTask(todoId: String, askedAt: String) = Unit
            override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
            override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
            override suspend fun retryTask(todoId: String): Todo = error("not used")
            override suspend fun removeTask(todoId: String) = Unit
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
            override suspend fun today(conversation: String): FeedItem = error("not used")
            override suspend fun mainConversation() = "main"
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = error("not used")
        }
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("user", 1, role = "user", kind = "text", text = "Tidy up my downloads and keep the docs and the release moving.", ts = at(1)),
                Item("answer", 2, role = "assistant", kind = "text", text = "I'll look at the downloads folder first. The docs and the release each have a question for you.", ts = at(2)),
            )))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val density = Density(LocalDensity.current.density, if (large) 2f else 1f)
            CompositionLocalProvider(LocalDensity provides density) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        HomeNavigation(listOf(NavItem("assistant", "Assistant", PhIcons.Sparkle), NavItem("agents", "Agents", PhIcons.Robot)), "assistant", {},
                            leading = { JAppMark(Tokens.Space.xl) })
                        Box(Modifier.weight(1f)) {
                            ChatScreen(main, chats, sender, interactions, sessions, {},
                                chrome = ChatChromeBindings(tasks = tasks, renamer = renamer),
                                renderStyle = ChatRenderStyle.Assistant, embedded = true, showTopBar = false)
                        }
                    }
                }
            }
        }
        frames({ shown -> shown.any { it.startsWith("I'll look at the downloads") } && shown.any { it == "Needs you" } }, "assistant")
        save("assistant${if (many) "-many" else ""}-${style.name.lowercase()}-${if (dark) "dark" else "light"}${if (large) "-large" else ""}")
    }

    @Test fun oneGlass() = agentScene("one", listOf(permission), AppStyle.Glass, false)
    @Test fun oneGlassDark() = agentScene("one", listOf(permission), AppStyle.Glass, true)
    @Test fun preparedGlass() = agentScene("prepared", listOf(prepared), AppStyle.Glass, false, showAnswers = true)
    @Test fun preparedGlassDark() = agentScene("prepared", listOf(prepared), AppStyle.Glass, true, showAnswers = true)
    @Test fun preparedGlassLarge() = agentScene("prepared", listOf(prepared), AppStyle.Glass, false, large = true, showAnswers = true)
    @Test fun preparedGlassLargeDark() = agentScene("prepared", listOf(prepared), AppStyle.Glass, true, large = true, showAnswers = true)
    @Test fun oneBrief() = agentScene("one", listOf(permission), AppStyle.Brief, false)
    @Test fun oneBriefDark() = agentScene("one", listOf(permission), AppStyle.Brief, true)
    @Test fun oneBoard() = agentScene("one", listOf(permission), AppStyle.Board, false)
    @Test fun oneBoardDark() = agentScene("one", listOf(permission), AppStyle.Board, true)
    @Test fun threeGlass() = agentScene("three", listOf(question, plan, permission), AppStyle.Glass, false)
    @Test fun threeGlassDark() = agentScene("three", listOf(question, plan, permission), AppStyle.Glass, true)
    @Test fun threeBrief() = agentScene("three", listOf(question, plan, permission), AppStyle.Brief, false)
    @Test fun threeBriefDark() = agentScene("three", listOf(question, plan, permission), AppStyle.Brief, true)
    @Test fun threeBoard() = agentScene("three", listOf(question, plan, permission), AppStyle.Board, false)
    @Test fun threeBoardDark() = agentScene("three", listOf(question, plan, permission), AppStyle.Board, true)
    @Test fun threeGlassLarge() = agentScene("three", listOf(question, plan, permission), AppStyle.Glass, false, large = true)
    @Test fun recordedPlanGlass() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Glass, false)
    @Test fun recordedPlanGlassDark() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Glass, true)
    @Test fun recordedPlanBrief() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Brief, false)
    @Test fun recordedPlanBriefDark() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Brief, true)
    @Test fun recordedPlanBoard() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Board, false)
    @Test fun recordedPlanBoardDark() = agentScene("recorded-plan", listOf(recordedPlan), AppStyle.Board, true)
    @Test fun recordedPlanGlassLarge() = agentScene("recorded-plan", listOf(recordedPlanLarge), AppStyle.Glass, false, large = true)
    @Test fun recordedPlanComponentGlassLarge() {
        rule.mainClock.autoAdvance = false
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 2f)) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        InteractionForm(recordedPlanLarge, null, AnswerDraft(), {}, busy = false, uncertain = false, full = false,
                            onAnswer = { _, _, _ -> }, onTerminal = null, onExpand = {}, showTerminalHelp = false)
                    }
                }
            }
        }
        repeat(20) { Snapshot.sendApplyNotifications(); rule.mainClock.advanceTimeByFrame() }
        save("recorded-plan-component-glass-light-large")
    }
    @Test fun foldedGlass() = agentScene("folded", listOf(permission, question), AppStyle.Glass, false, fold = true)
    @Test fun foldedGlassDark() = agentScene("folded", listOf(permission, question), AppStyle.Glass, true, fold = true)
    @Test fun foldedBrief() = agentScene("folded", listOf(permission, question), AppStyle.Brief, false, fold = true)
    @Test fun foldedBriefDark() = agentScene("folded", listOf(permission, question), AppStyle.Brief, true, fold = true)
    @Test fun foldedBoard() = agentScene("folded", listOf(permission, question), AppStyle.Board, false, fold = true)
    @Test fun foldedBoardDark() = agentScene("folded", listOf(permission, question), AppStyle.Board, true, fold = true)
    @Test fun assistantGlass() = assistantScene(AppStyle.Glass, false)
    @Test fun assistantGlassDark() = assistantScene(AppStyle.Glass, true)
    @Test fun assistantBrief() = assistantScene(AppStyle.Brief, false)
    @Test fun assistantBriefDark() = assistantScene(AppStyle.Brief, true)
    @Test fun assistantBoard() = assistantScene(AppStyle.Board, false)
    @Test fun assistantBoardDark() = assistantScene(AppStyle.Board, true)
    @Test fun assistantManyGlass() = assistantScene(AppStyle.Glass, false, many = true)
    @Test fun assistantManyGlassDark() = assistantScene(AppStyle.Glass, true, many = true)
    @Test fun assistantManyBrief() = assistantScene(AppStyle.Brief, false, many = true)
    @Test fun assistantManyBriefDark() = assistantScene(AppStyle.Brief, true, many = true)
    @Test fun assistantManyBoard() = assistantScene(AppStyle.Board, false, many = true)
    @Test fun assistantManyBoardDark() = assistantScene(AppStyle.Board, true, many = true)
    @Test fun assistantManyGlassLarge() = assistantScene(AppStyle.Glass, false, many = true, large = true)
}
