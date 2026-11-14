package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxHandling
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * The detail page's state line: shared `now` words stay above a suggestion, whose card still says the suggestion once;
 * an older hub without `now` uses its tier state beside the processing conversation. Every other state has
 * words — a hub without `now`, without a mark and without a suggestion still shows the page's own tier or done words.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w500dp-h1200dp-xhdpi")
class InboxDetailStateReviewTest {
    @get:Rule val rule = createComposeRule()
    private val repository = FakeInboxRepository()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun record(
        now: String? = null,
        suggestion: InboxSuggestion? = null,
        tier: String = "action",
        status: String? = null,
        done: Boolean = false,
        doneBy: String? = null,
    ): InboxRecord {
        val event = InboxEventFields(status = status, now = now, suggestion = suggestion, doneBy = doneBy)
        return InboxRecord(
            InboxItem("event", kind = "group", title = "Event title", sender = "Alex", preview = "Can we?",
                tier = tier, status = status, doneAt = if (done) "2026-01-01T12:00:00Z" else null),
            source = "chat", sourceLabel = "Team chat", event = event,
        )
    }

    private fun show(record: InboxRecord) {
        repository.snapshot.value = InboxSnapshot(records = listOf(record))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Box { InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator) }
            }
        }
    }

    @Test fun aSuggestionKeepsItsNowStateAndConversationBeforeTheCard() {
        val reply = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")
        val suggested = record(now = "confirm_reply", suggestion = reply).let {
            it.copy(item = it.item.copy(summary = "Summary"),
                event = it.event.copy(handling = InboxHandling(state = "done", machineId = "m1", sessionId = "s1")))
        }
        show(suggested)
        // The card: its kind and its body, once each, not the state line's own "Suggested reply: Friday works".
        assertEquals(1, rule.onAllNodesWithText("Suggested reply", useUnmergedTree = true).fetchSemanticsNodes().size)
        assertEquals(1, rule.onAllNodesWithText("Friday works", useUnmergedTree = true).fetchSemanticsNodes().size)
        assertEquals(0, rule.onAllNodesWithText("Suggested reply: Friday works", useUnmergedTree = true).fetchSemanticsNodes().size)

        val state = rule.onNodeWithText("Waiting for you to confirm a reply", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val summary = rule.onNodeWithText("Summary", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val suggestion = rule.onNodeWithText("Friday works", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("state < summary < suggestion: $state / $summary / $suggestion",
            state.top < summary.top && summary.top < suggestion.top)
        rule.onNodeWithText("Conversation ›").assertExists()
        rule.onNodeWithText("Say something").assertDoesNotExist()
        rule.onNodeWithText("Try again").assertDoesNotExist()
    }

    @Test fun everyStateHasWords() {
        // An older hub's suggestion, without now: tier state and conversation remain; the card says the suggestion once.
        val reply = InboxSuggestion(version = 1, kind = "reply", text = "Friday works")
        val oldHub = record(suggestion = reply).let {
            it.copy(event = it.event.copy(handling = InboxHandling(state = "done", machineId = "m1", sessionId = "s1")))
        }
        show(oldHub)
        rule.onNodeWithText("Needs you", useUnmergedTree = true).assertExists()
        assertTrue("suggestion card", rule.onAllNodesWithText("Friday works", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
        rule.onNodeWithText("Suggested reply: Friday works", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("Conversation ›").assertExists()

        // A tier-only item without now and without a mark: the page's own tier words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(tier = "fyi", status = "fyi")))
        assertTrue("tier words", rule.onAllNodesWithText("FYI", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

        // A done item: its done words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(tier = "action", done = true, doneBy = "user")))
        assertTrue("done words", rule.onAllNodesWithText("Done", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())

        // A now key the table knows: its words.
        repository.snapshot.value = InboxSnapshot(records = listOf(record(now = "check_due")))
        assertTrue("now words", rule.onAllNodesWithText("Time to look again", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
    }

    @Test fun theTimelineShowsTheThreeNewestAndAllWhenExpanded() {
        val chain = (1..5).map { i ->
            InboxChainEntry("c$i", at = "2026-01-01T11:0$i:00Z", kind = if (i == 5) "reported" else "received",
                text = "Step $i", via = if (i == 5) "agent" else "chat")
        }
        val withChain = record().let { it.copy(event = it.event.copy(chain = chain)) }
        show(withChain)
        // Folded: the three newest, the oldest of them first.
        val shown = listOf("Step 3", "Step 4", "Step 5").map {
            rule.onAllNodesWithText(it, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot.top
        }
        assertTrue("three newest, oldest first: $shown", shown[0] < shown[1] && shown[1] < shown[2])
        for (older in listOf("Step 1", "Step 2")) {
            assertEquals(older, 0, rule.onAllNodesWithText(older, useUnmergedTree = true).fetchSemanticsNodes().size)
        }
        // Only a reported step says so.
        rule.onNodeWithText("Assistant says", useUnmergedTree = true).assertExists()
    }

    @Test fun aReportedStepIsDrawnApartFromTheProgramsOwnSteps() {
        val chain = listOf(
            InboxChainEntry("c1", at = "2026-01-01T11:01:00Z", kind = "received", text = "Received", via = "chat"),
            InboxChainEntry("c2", at = "2026-01-01T11:02:00Z", kind = "reported", text = "Sent the email", via = "agent"),
        )
        val withChain = record().let { it.copy(event = it.event.copy(chain = chain)) }
        show(withChain)
        assertEquals("reported", chainIconMeaning(chain[1]))
        assertEquals("the reported step's tag", 1,
            rule.onAllNodesWithText("Assistant says", useUnmergedTree = true).fetchSemanticsNodes().size)
    }
}
