package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.PersonaTheme
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentListDisplaySettings
import persona.core.domain.sort.AgentSeenRows

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class AgentHerdrTabNameSettingTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    @Test fun hiddenWhenUnsupportedAndTogglesWithRetryOnFailure() {
        val enabled = MutableStateFlow<Boolean?>(null)
        var failNext = true
        var refreshes = 0
        val writes = mutableListOf<Boolean>()
        val settings = object : AgentListDisplaySettings {
            override val display = MutableStateFlow(AgentListDisplay.Avatar)
            override suspend fun setDisplay(value: AgentListDisplay) = Unit
            override val seenRows = MutableStateFlow(AgentSeenRows.Stay)
            override suspend fun setSeenRows(value: AgentSeenRows) = Unit
            override val useHerdrTabName = enabled
            override suspend fun refreshHerdrTabName() { refreshes++ }
            override suspend fun setUseHerdrTabName(value: Boolean) {
                writes += value
                if (failNext) {
                    failNext = false
                    error("Hub unreachable")
                }
                enabled.value = value
            }
        }

        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                AgentHerdrTabNameSetting(settings)
            }
        }
        rule.waitForIdle()
        assertEquals(1, refreshes)
        rule.onNodeWithTag("settings-use-herdr-tab-name").assertDoesNotExist()

        enabled.value = false
        rule.waitForIdle()
        rule.onNodeWithText("Use the herdr tab name as the session name").assertExists()

        // First toggle fails: value stays false and Retry appears.
        rule.onNodeWithTag("settings-use-herdr-tab-name").performClick()
        rule.waitForIdle()
        assertEquals(listOf(true), writes)
        assertEquals(false, enabled.value)
        rule.onNodeWithText("Retry").assertExists().performClick()
        rule.waitForIdle()
        assertEquals(listOf(true, true), writes)
        assertEquals(true, enabled.value)
        rule.onNodeWithText("Retry").assertDoesNotExist()
    }
}
