package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.SUBSCRIPTIONS_UI
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalPersonaName
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.ExtensionUiRoute
import java.io.File

/**
 * Settings › Inbox Sources (#5308): the row next to Connectors, only once the hub reports `inbox_sources`, opening the
 * subscription pages with no state. Shots in Glass, Brief and Board, light and dark, under `build/inbox-sources-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2200dp-xhdpi")
class InboxSourcesSettingsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    /** The activity rule allows one setContent, so each name is its own test. */
    private fun rendered(name: String?, extension: String, ui: String, title: String): ExtensionUiContext? {
        val seen = mutableListOf<ExtensionUiContext?>()
        val api = object : ExtensionApiStub() {
            override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) =
                ExtensionView(title = "Sources").also { seen += context }
        }
        rule.setContent {
            val page = @Composable {
                PersonaTheme(reduceMotion = true) { ExtensionUiPage(api, extension, ui, title, onBack = {}) }
            }
            if (name == null) page() else CompositionLocalProvider(LocalPersonaName provides name, content = page)
        }
        rule.waitUntil(5_000) { seen.isNotEmpty() }
        return seen.last()
    }

    @Test fun thePagesSendTheAssistantsName() {
        assertEquals("Ada", rendered("Ada", SUBSCRIPTIONS_UI, "pages", "Inbox Sources")?.assistant)
    }

    @Test fun aBlankNameIsStillSent() {
        assertEquals("", rendered("   ", SUBSCRIPTIONS_UI, "pages", "Inbox Sources")?.assistant)
    }

    @Test fun otherPagesOmitTheAssistant() {
        assertNull(rendered(null, "example-gateway", "home", "Example"))
    }

    @Test fun theSettingsRow() {
        val services = demoSettingsServices(rule.activity.resources).copy(demo = false)
        var sources by mutableStateOf(false)
        var query by mutableStateOf("")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var opened: NavKey? = null
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(2200.dp).background(PersonaTheme.palette.background)) {
                    SettingsHomePage(services, query, { query = it }, false, sources, null, false, null,
                        ConnectionState.Online, null, null, null, null, {}, { opened = it }, null, null, null, {}, {})
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Connectors").fetchSemanticsNodes().isNotEmpty() }
        assertEquals(0, rule.onAllNodesWithText("Inbox Sources").fetchSemanticsNodes().size)
        sources = true
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Inbox Sources").fetchSemanticsNodes().isNotEmpty() }
        assertEquals(1, rule.onAllNodesWithText("What brings new things into your Inbox").fetchSemanticsNodes().size)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s
            dark = d
            rule.waitForIdle()
            val image = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val out = File("build/inbox-sources-shots").also { it.mkdirs() }
            File(out, "${s.key}-${if (d) "dark" else "light"}.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        query = "subscriptions"
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Models").fetchSemanticsNodes().isEmpty() }
        assertEquals(1, rule.onAllNodesWithText("Inbox Sources").fetchSemanticsNodes().size)
        query = ""
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Models").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Inbox Sources").performClick()
        val route = opened as ExtensionUiRoute
        assertEquals(SUBSCRIPTIONS_UI, route.extensionId)
        assertEquals("pages", route.uiId)
        assertNull(route.state)
    }
}
