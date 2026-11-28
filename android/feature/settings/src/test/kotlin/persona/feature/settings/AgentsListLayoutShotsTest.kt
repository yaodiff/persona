package persona.feature.settings

import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.designsystem.AppStyle
import persona.core.designsystem.AppStyleStore
import persona.core.designsystem.LocalAppStyleStore
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.pagePadding
import persona.core.domain.sort.AgentListDisplay
import persona.core.domain.sort.AgentListDisplaySettings
import persona.core.domain.sort.AgentSeenRows
import java.io.File

/**
 * Settings › Interface style's "Agents list layout" (#4338 A9, iOS `LayoutSettingsPage`): on Glass the four displays, each
 * with two agents drawn that way and what it shows, the chosen one checked; a tap sets the list's choice. Brief and Board
 * have no such section (decision 8). PNGs in `build/agents-layout-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h3200dp-xhdpi")
@org.robolectric.annotation.GraphicsMode(org.robolectric.annotation.GraphicsMode.Mode.NATIVE)
class AgentsListLayoutShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private val choice = object : AgentListDisplaySettings {
        override val display = MutableStateFlow(AgentListDisplay.Avatar)
        override suspend fun setDisplay(value: AgentListDisplay) { display.value = value }
        override val seenRows = MutableStateFlow(AgentSeenRows.Stay)
        override suspend fun setSeenRows(value: AgentSeenRows) { seenRows.value = value }
    }

    @Test fun glass() {
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        lateinit var store: AppStyleStore
        rule.setContent {
            val context = LocalContext.current
            store = remember { AppStyleStore(context) }
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalAppStyleStore provides store) {
                    Box(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background)) {
                        AppearanceSettings(Modifier.fillMaxWidth().padding(horizontal = pagePadding), agentsDisplay = choice)
                    }
                }
            }
        }
        rule.runOnIdle { store.setStyle(AppStyle.Glass) }
        val out = File(System.getProperty("persona.shots") ?: "build/agents-layout-shots").also { it.mkdirs() }
        for (d in listOf(false, true)) {
            rule.runOnIdle { dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            out.resolve("interface-style-glass-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        assertEquals(1, rule.onAllNodesWithText("Agents list layout").fetchSemanticsNodes().size)
        rule.onNodeWithTag("layout-agents-Avatar").assertIsSelected()
        rule.onNodeWithTag("layout-agents-Cards").performClick()
        rule.waitForIdle()
        assertEquals(AgentListDisplay.Cards, choice.display.value)
        rule.onNodeWithTag("layout-agents-Cards").assertIsSelected()
        // Brief and Board keep Compact and Detailed in the list's ⋯; their settings page has no such section.
        rule.runOnIdle { store.setStyle(AppStyle.Brief); style = AppStyle.Brief }
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodesWithText("Agents list layout").fetchSemanticsNodes().size)
    }
}
