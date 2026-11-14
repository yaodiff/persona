package persona.feature.inbox

import androidx.compose.foundation.layout.Box
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxHandling
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/** Regression coverage for compatibility and locale boundaries not exercised by the changed tests. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w500dp-h1200dp-xhdpi")
class InboxFourwayFinalAdversarialTest {
    @get:Rule val rule = createComposeRule()

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Test fun anOldHubSuggestionStillShowsItsCurrentStateAndConversation() {
        val record = InboxRecord(
            InboxItem(
                "old-hub-suggestion", kind = "group", title = "Event title", sender = "Alex", preview = "Can we?",
                summary = "Summary", tier = "action", status = "action",
            ),
            source = "chat",
            sourceLabel = "Team chat",
            event = InboxEventFields(
                status = "action",
                handling = InboxHandling(state = "done", machineId = "m1", sessionId = "s1"),
                suggestion = InboxSuggestion(version = 1, kind = "reply", text = "Friday works"),
            ),
        )
        val repository = FakeInboxRepository()
        repository.snapshot.value = InboxSnapshot(records = listOf(record))
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Box { InboxEventScreen(repository, InboxEventRoute(record.item.id), navigator) }
            }
        }

        // No shared `now` key does not remove the open item's current-state sentence and icon.
        rule.onNodeWithText("Needs you", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Conversation ›", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Friday works", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Say something", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("Try again", useUnmergedTree = true).assertDoesNotExist()

        repository.snapshot.value = InboxSnapshot(records = listOf(record.copy(event = record.event.copy(handling = null))))
        rule.waitForIdle()
        rule.onNodeWithText("Needs you", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Conversation ›", useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun chainTimesZeroPadEarlyHoursInEveryRequiredLocale() {
        val now = Instant.parse("2026-11-05T12:00:00Z")
        val today = "2026-11-05T01:05:00Z"
        for (locale in listOf(Locale.US, Locale.SIMPLIFIED_CHINESE, Locale.JAPAN)) {
            assertEquals(locale.toString(), "01:05", chainTime(today, locale, now, ZoneOffset.UTC))
        }
    }

    @Test fun chainTimesZeroPadEarlyHoursOnEarlierDatesInEveryRequiredLocale() {
        val now = Instant.parse("2026-11-05T12:00:00Z")
        val earlier = "2026-11-04T01:05:00Z"
        assertEquals("Nov 4, 01:05", chainTime(earlier, Locale.US, now, ZoneOffset.UTC))
        assertEquals("11月4日 01:05", chainTime(earlier, Locale.SIMPLIFIED_CHINESE, now, ZoneOffset.UTC))
        assertEquals("11月4日 01:05", chainTime(earlier, Locale.JAPAN, now, ZoneOffset.UTC))
    }
}
