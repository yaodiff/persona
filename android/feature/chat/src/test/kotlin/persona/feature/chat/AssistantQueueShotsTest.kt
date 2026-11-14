package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Interaction
import persona.core.contracts.ImagePayload
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.QueueEdit
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TasksStore
import java.io.File

/**
 * Persona's conversation with a queue (#4339 C6, iOS QueueStrip in a Persona chat), on the real page: the user's two
 * messages, then one relayed from another conversation (`queued_relayed`), under the In progress entry and right above
 * the composer, folded, opened and with Send Now at one press on Glass; Brief and Board draw it as
 * their Agent chat's strip (always open, no Send now). Then the same page with nothing queued, which every style draws
 * as before. Every style, light and dark (`build/assistant-queue-shots/`, or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h760dp-xhdpi")
class AssistantQueueShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val main = SessionKey("computer-a", "main")
    private val web = Session("computer-a", "web", title = "Update the web interface", cwd = "/work/web", state = "working", status = "working",
        activity = "Checking the routes", updatedAt = "2026-11-12T09:30:00Z")
    private val todos = listOf(Todo("task-web", web.title, origin = "main", createdAt = "2026-11-12T09:00:00Z",
        agents = listOf(TodoAgent(web.machineId, web.sessionId, cwd = web.cwd))))
    // Read from the wire, as the hub lists it: the last of the queue came from another conversation.
    private val persona = JsonCodec.decodeFromString<Session>("""{"machine_id":"computer-a","session_id":"main","title":"Persona",
        "assistant":true,"status":"working","state":"working","queued_relayed":1,"queued":["Also move next Wednesday's meeting to the afternoon",
        "Check the flight times for Friday too","From “Morning brief”: the meeting notes are ready. Send them to everyone who attended?"]}""")

    private val api = object : TasksApi {
        override suspend fun todos() = todos
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
        override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
        override suspend fun retryTask(todoId: String): Todo = error("not used")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
        override suspend fun today(conversation: String): FeedItem = error("not used")
        override suspend fun mainConversation() = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }
    private val connection = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private val sessions = object : SessionsRepository {
        override val sessions = MutableStateFlow(listOf(persona, web))
        override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true)))
        override suspend fun refresh() = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    // No times on the turn: its work line would tick with the clock, and main's pictures are taken minutes apart from the branch's.
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = flowOf(ChatSnapshot(loading = false, items = listOf(
            Item("user", 1, role = "user", kind = "text", text = "Plan my week around the release."),
            Item("answer", 2, role = "assistant", kind = "text", text = "Looking at your calendar and the release dates now."),
        )))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val actions = object : ChatActions {
        /** The photos the hub returns beside the queue when a message is taken back, by message. */
        var photos: List<List<ImagePayload>?> = emptyList()
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun editQueue(session: Session, keep: List<Int>): QueueEdit = QueueEdit(session.queued.orEmpty(), photos)
        override suspend fun sendQueuedNow(session: Session) = Unit
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(5) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }

    private fun shot(name: String, image: Bitmap = rule.onNodeWithTag("assistant-queue-shot").captureToImage().asAndroidBitmap()) {
        val output = File(System.getProperty("persona.shots") ?: "build/assistant-queue-shots").also { it.mkdirs() }
        File(output, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun page(style: () -> AppStyle, dark: () -> Boolean, fontScale: Float = 1f) {
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark()) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration,
                LocalDensity provides Density(LocalDensity.current.density, fontScale)) {
                PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                    Box(Modifier.width(400.dp).height(760.dp).background(PersonaTheme.palette.background).testTag("assistant-queue-shot")) {
                        // A new chat when the style or theme changes, so a notice or Glass's fold does not carry over.
                        key(style(), dark()) {
                            ChatScreen(main, chats, sender, interactions, sessions, {},
                                chrome = ChatChromeBindings(actions = actions, connection = connection, tasks = tasks, onOpenSession = {}),
                                renderStyle = ChatRenderStyle.Assistant, embedded = true, showTopBar = false)
                        }
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun personasQueueFoldedOpenAndSendNowInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        page({ style }, { dark })
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            settle()
            shot("queue-folded-$suffix")
            // The line opens it (Glass; nothing to open where the conversation draws no queue).
            rule.onAllNodesWithContentDescription("queued · Persona", substring = true).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
            }
            shot("queue-open-$suffix")
            // Glass sends at one press and draws no dialog. Brief and Board have no Send now.
            val sendsNow = s == AppStyle.Glass
            rule.onAllNodesWithText("Send now").assertCountEquals(if (sendsNow) 1 else 0)
            if (sendsNow) {
                rule.onAllNodesWithText("Send now")[0].performClick(); settle()
                rule.onAllNodes(isDialog()).assertCountEquals(0)
                shot("queue-send-now-$suffix")
            }
        }
        // Nothing queued: the page as it was before the queue came to it.
        sessions.sessions.value = listOf(persona.copy(queued = null), web)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            settle()
            shot("noqueue-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    // Android's largest text (twice the size), Glass opened: the disc grows with the words, as iOS's scaledFrame.
    @Test fun personasQueueAtTheLargestText() {
        var dark by mutableStateOf(false)
        page({ AppStyle.Glass }, { dark }, fontScale = 2f)
        for (d in listOf(false, true)) {
            dark = d; settle()
            rule.onAllNodesWithContentDescription("queued · Persona", substring = true).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
            }
            shot("queue-open-largest-glass-${if (d) "dark" else "light"}")
            rule.onAllNodesWithContentDescription("queued · Persona", substring = true).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
            }
        }
    }

    // Persona messages with photos (the paths this phone sent in the words, as its composer writes them): on Glass a row says how
    // many, "N images" under its words, iOS's count; Brief and Board keep the mark among the words.
    private val withPhotos = JsonCodec.decodeFromString<Session>("""{"machine_id":"computer-a","session_id":"main","title":"Persona",
        "assistant":true,"status":"working","state":"working","queued":["Look at these two screens for the rollout\n\n<attached_images>\n/tmp/shot-a.jpg\n/tmp/shot-b.jpg\n</attached_images>",
        "Please look at the image\n\n<attached_images>\n/tmp/shot-c.jpg\n</attached_images>","Check the flight times for Friday too"]}""")

    @Test fun personasQueueRowsSayHowManyPhotosTheyCarry() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        sessions.sessions.value = listOf(withPhotos, web)
        page({ style }, { dark })
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            settle()
            rule.onAllNodesWithContentDescription("queued · Persona", substring = true).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
            }
            shot("queue-photos-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            rule.onAllNodesWithText("2 images").assertCountEquals(if (s == AppStyle.Glass) 1 else 0)
            rule.onAllNodesWithText("1 image").assertCountEquals(if (s == AppStyle.Glass) 1 else 0)
            rule.onAllNodesWithContentDescription("queued · Persona", substring = true).fetchSemanticsNodes().firstOrNull()?.let {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
            }
        }
    }

    private fun photo(color: Int): ImagePayload {
        val bitmap = Bitmap.createBitmap(160, 120, Bitmap.Config.ARGB_8888).apply { eraseColor(color) }
        val bytes = java.io.ByteArrayOutputStream().also { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()
        return ImagePayload("image/png", java.util.Base64.getEncoder().encodeToString(bytes))
    }

    // Taken back, a message brings its photos with it (the hub returns them beside the queue, base64): they are in the draft, over the words.
    private fun takeBack(dark: Boolean) {
        useChatFileProvider(androidx.test.core.app.ApplicationProvider.getApplicationContext())
        // the third message carries two photos as payloads (sent from another device: its words hold no paths for them)
        actions.photos = listOf(null, null, listOf(photo(0xFF4F8DF7.toInt()), photo(0xFFE9A23B.toInt())))
        sessions.sessions.value = listOf(withPhotos, web)
        page({ AppStyle.Glass }, { dark })
        settle()
        rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick(); settle()
        rule.onAllNodesWithContentDescription("Item 3", substring = true)[0].performClick(); settle()
        rule.onAllNodesWithText("Take back to edit")[0].performClick()
        // the photos are thumbnails of the draft (the page shows no notice of its own: the app's feedback host does); they are
        // written off the main thread, so the page's frames go on by hand while the clock stands still
        repeat(400) {
            if (rule.onAllNodesWithContentDescription("Photo 2", substring = true).fetchSemanticsNodes().isNotEmpty()) return@repeat
            settle(); Thread.sleep(25)
        }
        rule.onAllNodesWithContentDescription("Photo 2", substring = true).assertCountEquals(1)
        settle()
        shot("queue-take-back-${if (dark) "dark" else "light"}")
    }

    @Test fun takingBackAPersonaMessageBringsItsPhotosBackLight() = takeBack(dark = false)

    @Test fun takingBackAPersonaMessageBringsItsPhotosBackDark() = takeBack(dark = true)
}
