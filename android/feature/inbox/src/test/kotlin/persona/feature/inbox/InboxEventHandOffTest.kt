package persona.feature.inbox

import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.isToggleable
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxEventHandOffTest {
    @get:Rule val rule = createComposeRule()

    private fun show(repository: HandOffBarTestRepository, itemId: String = repository.record.item.id, navigator: HandOffBarTestNavigator = HandOffBarTestNavigator()) {
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxEventScreen(repository, InboxEventRoute(itemId), navigator) } }
    }

    @Test fun undoRestoresTypedTextWithoutCallingTheHub() {
        val repository = HandOffBarTestRepository()
        show(repository)
        rule.onNodeWithTag("inbox-handoff-field").performTextReplacement("Please investigate")
        rule.onNodeWithTag("inbox-handoff-send").performClick()
        rule.onNodeWithText("Undo").performClick()
        rule.onNodeWithTag("inbox-handoff-field").assertTextEquals("Please investigate")
        assertTrue(repository.handOffRequests.isEmpty())
    }

    @Test fun queuedViewSendsOnceAndSentViewOnlyOpens() {
        val repository = HandOffBarTestRepository()
        val navigator = HandOffBarTestNavigator()
        show(repository, navigator = navigator)
        rule.onNodeWithTag("inbox-handoff-chip-summary").performClick()
        rule.onNodeWithText("View").performClick()
        rule.waitForIdle()
        assertEquals(1, repository.handOffRequests.size)
        assertEquals(ChatRoute("hub", "main", ConversationKind.Assistant), navigator.routes.single())
        rule.onNodeWithText("View").performClick()
        assertEquals(2, navigator.routes.size)
        assertEquals(1, repository.handOffRequests.size)
    }

    @Test fun targetMenuListsThreeAgentsThenMore() {
        val repository = HandOffBarTestRepository(agentCount = 5)
        show(repository)
        rule.onNodeWithTag("inbox-handoff-target").performClick()
        rule.onNodeWithText("Agent 1").assertExists()
        rule.onNodeWithText("Agent 3").assertExists()
        rule.onNodeWithText("Agent 4").assertDoesNotExist()
        rule.onNodeWithText("More…").performClick()
        rule.onNodeWithText("Agent 4").performClick()
        rule.onNodeWithTag("inbox-handoff-target").performClick()
        rule.onNode(isToggleable()).assertIsOff()
    }

    @Test fun draftReplyUsesTheExistingAgentDraft() {
        val repository = HandOffBarTestRepository()
        show(repository)
        rule.onNodeWithTag("inbox-handoff-chip-draft").performClick()
        rule.onNodeWithText("Existing AI reply").performTextReplacement("Edited reply")
        rule.onNodeWithText("Edited reply").assertExists()
        assertTrue(repository.handOffRequests.isEmpty())
    }

    @Test fun aReplySuggestionDoesNotDuplicateDraftReply() {
        val repository = HandOffBarTestRepository()
        repository.snapshot.value = repository.snapshot.value.copy(records = listOf(
            repository.record.copy(event = repository.record.event.copy(suggestion = InboxSuggestion(1, "reply", "Suggested reply"))),
        ))
        show(repository)
        rule.onNodeWithTag("inbox-handoff-chip-draft").assertDoesNotExist()
    }

    @Test fun doneItemsHaveNoInteractiveBar() {
        val done = HandOffBarTestRepository(record = handOffBarRecord(done = true))
        show(done)
        rule.onNodeWithTag("inbox-handoff-target").assertDoesNotExist()
        rule.onNodeWithTag("inbox-handoff-chip-summary").assertDoesNotExist()
    }

    @Test fun aFinishedItemOpenedFromHistoryCanBeHandedOff() {
        val repository = HandOffBarTestRepository(record = handOffBarRecord(done = true))
        repository.historyRecord = repository.record
        repository.snapshot.value = repository.snapshot.value.copy(records = emptyList())
        show(repository)
        rule.onNodeWithTag("inbox-handoff-chip-summary").performClick()
        rule.onNodeWithText("View").performClick()
        rule.waitForIdle()
        assertEquals(1, repository.handOffRequests.size)
    }

    @Test fun missingItemsHaveNoInteractiveBar() {
        val repository = HandOffBarTestRepository()
        show(repository, itemId = "missing")
        rule.onNodeWithText("Can't find this item. It may have been handled or removed.").assertExists()
        rule.onNodeWithTag("inbox-handoff-target").assertDoesNotExist()
    }
}

internal fun handOffBarRecord(id: String = "item", done: Boolean = false) = InboxRecord(
    InboxItem(id, title = "Example event", preview = "Example preview", doneAt = if (done) "2026-11-01T00:00:00Z" else null),
    source = "mail",
    mail = InboxMailRef("account", uids = listOf(7)),
    agent = InboxAgent("drafted", draft = "Existing AI reply"),
    mutes = listOf(InboxMuteOption("sender", "sender@example.com", "Example sender")),
)

internal class HandOffBarTestRepository(
    val record: InboxRecord = handOffBarRecord(),
    agentCount: Int = 1,
) : InboxRepository {
    override val entries = MutableStateFlow(emptyList<InboxEntry>())
    override val snapshot = MutableStateFlow(InboxSnapshot(
        records = listOf(record),
        sessions = (1..agentCount).map { index ->
            AgentSession(Session(machineId = "machine", sessionId = "agent-$index", title = "Agent $index", status = "idle"))
        },
        machines = mapOf("machine" to "Example computer"),
        targetsReady = true,
    ))
    override val connectionState = flowOf(ConnectionState.Online)
    override val handOffMemory = MutableStateFlow(emptyList<HandOffMemoryEntry>())
    var historyRecord: InboxRecord? = null
    override suspend fun item(itemId: String): InboxRecord? = snapshot.value.records.inboxRecord(itemId) ?: historyRecord?.takeIf { it.item.id == itemId }
    val handOffRequests = mutableListOf<InboxHandOffRequest>()
    val drafts = mutableMapOf<ReplyTarget, ReplyDraft>()
    var result: SessionKey? = SessionKey("hub", "main")
    var failure: Exception? = null
    var doneFailure: Exception? = null
    var handOffGate: CompletableDeferred<Unit>? = null
    var memoryGate: CompletableDeferred<Unit>? = null
    var doneGate: CompletableDeferred<Unit>? = null
    var doneCalls = 0

    override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
        handOffRequests += request
        handOffGate?.await()
        failure?.let { throw it }
        return if (request.machineId != null && request.sessionId != null) SessionKey(request.machineId!!, request.sessionId!!) else result
    }

    override suspend fun saveHandOffMemory(value: List<HandOffMemoryEntry>) {
        memoryGate?.await()
        handOffMemory.value = value
    }
    override suspend fun personaContext(itemId: String): PersonaContext = PersonaContext("Full example context")
    override suspend fun draftReply(itemId: String, instruction: String): String = "AI reply"
    override suspend fun setDone(itemId: String, done: Boolean) {
        doneCalls++
        doneGate?.await()
        doneFailure?.let { throw it }
        snapshot.value = snapshot.value.copy(records = snapshot.value.records.map {
            if (it.item.id == itemId) it.copy(item = it.item.copy(doneAt = if (done) "2026-11-01T00:00:00Z" else null)) else it
        })
    }

    override suspend fun refresh() = Unit
    override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
    override suspend fun loadOlder(feedId: String) = Unit
    override suspend fun refreshConversation(feedId: String) = Unit
    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = drafts[target]
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) { drafts[target] = draft }
    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
    override suspend fun downloadFile(file: LarkPart.File): String = error("Not used")
}

internal class HandOffBarTestNavigator : Navigator {
    val routes = mutableListOf<NavKey>()
    val tabs = mutableListOf<AppTab>()
    override fun navigate(key: NavKey) { routes += key }
    override fun back() = Unit
    override fun selectTab(tab: AppTab) { tabs += tab }
}
