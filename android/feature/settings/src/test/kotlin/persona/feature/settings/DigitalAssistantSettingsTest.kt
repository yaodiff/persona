package persona.feature.settings

import android.content.ActivityNotFoundException
import android.content.Intent
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import persona.core.contracts.ModelPick
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.contracts.api.DigitalAssistantModelGroup
import persona.core.contracts.api.DigitalAssistantModelOption
import persona.core.contracts.api.DigitalAssistantModelSettings
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.SettingsPage

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h2200dp-xhdpi")
class DigitalAssistantSettingsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private class FakeDigitalAssistantModelSettings(
        initialChoice: DigitalAssistantModelChoice = DigitalAssistantModelChoice(),
        private val catalog: DigitalAssistantModelCatalog = DigitalAssistantModelCatalog(
            groups = listOf(
                DigitalAssistantModelGroup(
                    launcher = "gemini",
                    title = "Gemini",
                    options = listOf(
                        DigitalAssistantModelOption(
                            pick = ModelPick("gemini", "gemini-2.5-flash"),
                            modelLabel = "Gemini 2.5 Flash",
                            launcherLabel = "Gemini",
                            description = "Fast responses",
                        ),
                    ),
                ),
            ),
        ),
    ) : DigitalAssistantModelSettings {
        private val mutableChoice = MutableStateFlow(initialChoice)
        override val choice: StateFlow<DigitalAssistantModelChoice> = mutableChoice.asStateFlow()
        override fun selectDefault() {
            mutableChoice.value = DigitalAssistantModelChoice()
        }
        override fun selectModel(choice: DigitalAssistantModelChoice) {
            mutableChoice.value = choice
        }
        override suspend fun loadCatalog(refresh: Boolean): DigitalAssistantModelCatalog = catalog
    }

    @Test
    fun candidateIntentsPrioritizeVoiceInputSettingsAndFallbackCleanly() {
        val candidates = DigitalAssistantSettings.candidateIntents("com.example.persona")
        assertEquals(Settings.ACTION_VOICE_INPUT_SETTINGS, candidates[0].action)
        assertEquals(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS, candidates[1].action)
        assertEquals(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, candidates[2].action)

        val attempted = mutableListOf<String?>()
        val opened = DigitalAssistantSettings.openSystemSettings("com.example.persona") { intent ->
            attempted += intent.action
            if (intent.action == Settings.ACTION_VOICE_INPUT_SETTINGS) {
                throw ActivityNotFoundException("missing voice settings")
            }
        }
        assertTrue(opened)
        assertEquals(
            listOf(Settings.ACTION_VOICE_INPUT_SETTINGS, Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS),
            attempted,
        )
    }

    @Test
    fun settingsHomePageShowsDefaultDigitalAssistantRowModelSummaryAndOpensSettingsPage() {
        val modelSettings = FakeDigitalAssistantModelSettings(
            initialChoice = DigitalAssistantModelChoice(
                pick = ModelPick("gemini", "gemini-2.5-flash"),
                modelLabel = "Gemini 2.5 Flash",
                launcherLabel = "Gemini",
            ),
        )
        val services = demoSettingsServices(rule.activity.resources).copy(
            demo = false,
            digitalAssistantModel = modelSettings,
        )
        var query by mutableStateOf("")
        val openedPages = mutableListOf<SettingsPage>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                SettingsHomePage(
                    services = services,
                    query = query,
                    onQuery = { query = it },
                    extensionsAvailable = false,
                    inboxSources = false,
                    extensionSummary = null,
                    channelsAvailable = false,
                    channelList = null,
                    knowledgeSelectionAvailable = false,
                    connection = ConnectionState.Online,
                    translationValue = null,
                    hubUpdateStatus = null,
                    updateValue = null,
                    selected = null,
                    onOpen = { openedPages += it },
                    onNavigate = null,
                    onClose = null,
                    onDemo = null,
                    onExitDemo = null,
                    footer = {},
                )
            }
        }

        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Default digital assistant").fetchSemanticsNodes().isNotEmpty()
        }
        rule.onNodeWithText("Not set").assertExists()
        rule.onNodeWithText("Quick window model: Gemini · Gemini 2.5 Flash").assertExists()

        query = "screenshot"
        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Models").fetchSemanticsNodes().isEmpty()
        }
        rule.onNodeWithText("Default digital assistant").assertExists()

        rule.onNodeWithText("Default digital assistant").performClick()
        assertEquals(listOf(SettingsPage.DigitalAssistant), openedPages)
    }

    @Test
    fun digitalAssistantSettingsPageOpensSystemSettingsAndUpdatesQuickWindowModel() {
        val modelSettings = FakeDigitalAssistantModelSettings()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                DigitalAssistantSettingsPage(
                    modelSettings = modelSettings,
                    connection = ConnectionState.Online,
                    demo = false,
                    onBack = {},
                )
            }
        }

        rule.waitUntil(5_000) {
            rule.onAllNodesWithText("Gemini 2.5 Flash").fetchSemanticsNodes().isNotEmpty()
        }

        // Selecting a dedicated model updates the shared settings flow immediately.
        rule.onNodeWithText("Gemini 2.5 Flash").performClick()
        assertEquals(ModelPick("gemini", "gemini-2.5-flash"), modelSettings.choice.value.pick)

        // Switching back to Default (Main chat) clears the dedicated model preference.
        rule.onNodeWithTag("digital-assistant-model-default").performClick()
        assertTrue(modelSettings.choice.value.isDefault)

        // System assistant row still launches Android's voice input / default digital assistant picker.
        rule.onNodeWithTag("digital-assistant-system-row").performClick()
        val started: Intent? = shadowOf(rule.activity).nextStartedActivity
        assertEquals(Settings.ACTION_VOICE_INPUT_SETTINGS, started?.action)
    }
}
