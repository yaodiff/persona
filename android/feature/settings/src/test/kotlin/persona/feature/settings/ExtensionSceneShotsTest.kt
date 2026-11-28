package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assertContentDescriptionEquals
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.pinch
import androidx.compose.ui.test.swipe
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.generated.ExtensionAction
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h1000dp-mdpi")
class ExtensionSceneShotsTest {
    @get:Rule val rule = createComposeRule()

    private val view by lazy {
        val file = File(System.getProperty("persona.repo.root"), "schema/extension/examples/View.v11-scene-flat.json")
        JsonCodec.decodeFromString(ExtensionView.serializer(), file.readText())
    }
    private val scene get() = view.sections.flatMap { it.items }.single { it.type == "scene" }

    @Test fun sceneIsDrawableInExtensionPagesAndCards() {
        var compact by mutableStateOf(false)
        val state = ExtensionViewState(view = view)
        val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {})
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                if (compact) ExtensionCardContent(state, handle) else ExtensionViewColumn(state, handle)
            }
        }
        rule.onNodeWithTag("extension-scene").assertExists()
        rule.runOnIdle { compact = true }
        rule.onNode(hasTestTag("extension-scene") and hasClickAction()).assertExists()
    }

    @Test fun compactAndPageScenesUseEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var compact by mutableStateOf(false)
        val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {})
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.width(400.dp).background(PersonaTheme.palette.background).testTag("scene-shot")) {
                    ExtensionScene(scene, ExtensionViewState(), handle, compact = compact)
                }
            }
        }
        for (small in listOf(false, true)) for (appearance in listOf(false, true)) for (nextStyle in AppStyle.entries) {
            rule.runOnIdle {
                compact = small
                dark = appearance
                style = nextStyle
            }
            val name = "${if (small) "compact" else "page"}-${nextStyle.name.lowercase()}-${if (appearance) "dark" else "light"}"
            capture(name)
        }
    }

    @Test fun cameraAndPinActionsStayOnTheExistingScene() {
        assertEquals(9, scene.shapes?.size)
        var item by mutableStateOf(scene)
        var pressed: Pair<ExtensionViewItem, ExtensionViewAction>? = null
        val handle = ExtensionViewHandle({ row, action -> pressed = row to action }, { _, _ -> }, {}, {})
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ExtensionScene(item, ExtensionViewState(), handle)
            }
        }
        rule.onNodeWithTag("scene-reset").assertIsNotEnabled()
        rule.onNodeWithTag("extension-scene").performTouchInput {
            swipe(start = Offset(width * 0.25f, height * 0.58f), end = Offset(width * 0.7f, height * 0.58f))
        }
        rule.onNodeWithTag("scene-reset").assertIsEnabled()
        rule.runOnIdle { item = item.copy(caption = "The lights changed") }
        rule.onNodeWithTag("scene-reset").assertIsEnabled().performClick().assertIsNotEnabled()
        rule.onNodeWithTag("extension-scene").performTouchInput {
            val y = height * 0.3f
            pinch(Offset(width * 0.4f, y), Offset(width * 0.2f, y), Offset(width * 0.6f, y), Offset(width * 0.8f, y))
        }
        rule.onNodeWithTag("scene-reset").assertIsEnabled().performClick().assertIsNotEnabled()
        rule.onNodeWithTag("scene-zoom-in").performClick()
        rule.onNodeWithTag("scene-reset").assertIsEnabled()
        val pin = rule.onNodeWithTag("scene-pin-light.living")
        pin.assertContentDescriptionEquals("Living room light, On").assertIsEnabled().performClick()
        rule.runOnIdle {
            assertEquals("home/light.living", pressed?.first?.key)
            assertEquals("open", pressed?.second?.id)
            assertEquals(mapOf("entity" to "light.living"), pressed?.second?.values)
        }
        rule.onNodeWithTag("scene-open-full").performClick()
        rule.onNodeWithTag("scene-full-screen").assertExists()
        rule.onNode(hasTestTag("scene-pin-climate.living") and hasAnyAncestor(hasTestTag("scene-full-screen")))
            .assertContentDescriptionEquals("Living room AC, Off")
    }

    @Test fun compactPreviewOpensTheSameInteractiveScene() {
        val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {})
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true) { ExtensionScene(scene, ExtensionViewState(), handle, compact = true) }
        }
        val living = rule.onNodeWithTag("scene-pin-light.living", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        val climate = rule.onNodeWithTag("scene-pin-climate.living", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
        assertTrue("Compact pin readouts should have their own space", !living.overlaps(climate))
        rule.onNode(hasTestTag("extension-scene") and hasClickAction()).performClick()
        rule.onNodeWithTag("scene-full-screen").assertExists()
        rule.onNode(hasTestTag("scene-pin-light.living") and hasClickAction() and
            SemanticsMatcher.expectValue(SemanticsProperties.ContentDescription, listOf("Living room light, On"))).assertExists()
    }

    @Test fun compactCoverDismissesBeforeAConfirmationAndReopensAtTheInitialCamera() {
        val pins = scene.pins.orEmpty().map {
            if (it.id == "light.living") it.copy(action = ExtensionAction(id = "toggle", confirm = "Turn the light on?")) else it
        }
        var pressed: ExtensionViewAction? = null
        val handle = ExtensionViewHandle({ _, action -> pressed = action }, { _, _ -> }, {}, {})
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true) {
                ExtensionScene(scene.copy(pins = pins), ExtensionViewState(), handle, compact = true)
            }
        }
        val preview = rule.onNode(hasTestTag("extension-scene") and hasClickAction())
        preview.performClick()
        rule.onNodeWithTag("scene-zoom-in").performClick()
        rule.onNodeWithTag("scene-reset").assertIsEnabled()
        rule.onNode(hasTestTag("scene-pin-light.living") and hasAnyAncestor(hasTestTag("scene-full-screen")))
            .assertIsEnabled().performClick()
        rule.onNodeWithTag("scene-full-screen").assertDoesNotExist()
        rule.runOnIdle {
            assertEquals("toggle", pressed?.id)
            assertEquals("Turn the light on?", pressed?.confirm)
        }
        preview.performClick()
        rule.onNodeWithTag("scene-reset").assertIsNotEnabled()
    }

    private fun capture(name: String) {
        val bitmap = rule.onNodeWithTag("scene-shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var drawn = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
            if (bitmap.getPixel(x, y) != background) drawn++
        }
        assertTrue("No geometry was drawn for $name", drawn > 100)
        val output = File("build/extension-scene-shots").also { it.mkdirs() }
        File(output, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
