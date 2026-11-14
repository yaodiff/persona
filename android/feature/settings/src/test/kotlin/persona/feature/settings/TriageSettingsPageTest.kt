package persona.feature.settings

import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.AgentTriage
import persona.core.contracts.api.TriageSwitches
import persona.core.designsystem.PersonaTheme

/** The status-afterwards row exists only on hubs that offer it and follows the same save and enabled rules as closeout. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h2600dp")
class TriageSettingsPageTest {
    @get:Rule val rule = createComposeRule()

    private class FakeTriage(
        private var current: TriageSwitches,
        private val putGate: CompletableDeferred<Unit>? = null,
    ) : AgentTriage {
        val puts = mutableListOf<TriageSwitches>()

        override suspend fun switches() = current

        override suspend fun put(value: TriageSwitches): TriageSwitches {
            puts += value
            putGate?.await()
            current = value
            return value
        }
    }

    private fun page(api: AgentTriage) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                TriageSettingsPage(triage = api, models = null, onBack = {}, onModel = {})
            }
        }
        rule.waitForIdle()
    }

    private val closeoutAsk get() = rule.onNodeWithTag("triage-closeout-ask")

    @Test fun anOlderHubKeepsTheCloseoutRowButHasNoStatusAfterwardsRow() {
        page(FakeTriage(TriageSwitches(enabled = true, closeout = true)))

        rule.onNodeWithTag("triage-closeout").assertExists()
        closeoutAsk.assertDoesNotExist()
    }

    @Test fun togglingSavesCloseoutAskAndLeavesEveryOtherSwitchAlone() {
        val before = TriageSwitches(enabled = true, autoContinue = true, wake = true, decide = true,
            replyReminder = false, closeout = true, closeoutAsk = false)
        val api = FakeTriage(before)
        page(api)

        closeoutAsk.assertIsOff().assertIsEnabled().performClick()
        rule.waitForIdle()

        assertEquals(listOf(before.copy(closeoutAsk = true)), api.puts)
        closeoutAsk.assertIsOn().assertIsEnabled()
    }

    @Test fun theRowIsDisabledWhileTriageIsOff() {
        val api = FakeTriage(TriageSwitches(enabled = false, closeout = true, closeoutAsk = true))
        page(api)

        closeoutAsk.assertIsOn().assertIsNotEnabled().performClick()
        rule.waitForIdle()
        assertTrue("a disabled row saves nothing", api.puts.isEmpty())
    }

    @Test fun theRowIsDisabledWhileItsSaveIsInFlight() {
        val before = TriageSwitches(enabled = true, closeout = true, closeoutAsk = false)
        val gate = CompletableDeferred<Unit>()
        val api = FakeTriage(before, putGate = gate)
        page(api)

        try {
            closeoutAsk.assertIsOff().assertIsEnabled().performClick()
            rule.waitUntil(timeoutMillis = 5_000) { api.puts.size == 1 }
            closeoutAsk.assertIsOn().assertIsNotEnabled()
            assertEquals(listOf(before.copy(closeoutAsk = true)), api.puts)
        } finally {
            gate.complete(Unit)
        }
        rule.waitForIdle()
        closeoutAsk.assertIsOn().assertIsEnabled()
    }
}
