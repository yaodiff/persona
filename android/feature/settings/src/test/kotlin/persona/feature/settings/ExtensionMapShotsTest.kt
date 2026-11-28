package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasTestTag
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.generated.ExtensionAction
import persona.core.contracts.generated.ExtensionMapCoordinate
import persona.core.contracts.generated.ExtensionMapLink
import persona.core.contracts.generated.ExtensionMapMarker
import persona.core.contracts.generated.ExtensionMapRoute
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h1000dp-mdpi")
class ExtensionMapShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun mapAspectUsesTheImageAspectContract() {
        assertEquals(1f, mapAspect("square"), 0f)
        assertEquals(4f / 3f, mapAspect("landscape"), 0f)
        assertEquals(16f / 9f, mapAspect("wide"), 0f)
        assertEquals(3f / 4f, mapAspect("portrait"), 0f)
        assertEquals(4f / 3f, mapAspect("auto"), 0f)
        assertEquals(4f / 3f, mapAspect(null), 0f)
    }

    @Test fun fitsRoutesAcrossTheDateLineAndAcceptsOnlyHttpsLinks() {
        val west = ExtensionMapCoordinate(10.0, 179.8)
        val east = ExtensionMapCoordinate(11.0, -179.7)
        val frame = checkNotNull(mapFrame(listOf(west, east), null))
        assertTrue(frame.spanLon < 1.0)
        assertNull(mapFrame(listOf(ExtensionMapCoordinate(110.0, 45.0)), null))
        assertEquals("https://example.com/route", mapExternalLink("https://example.com/route"))
        assertNull(mapExternalLink("http://example.com/route"))
        assertNull(mapExternalLink("https:///route"))
    }

    @Test fun routesRenderInEveryStyleAndOpenAnInteractiveFullScreen() {
        val first = ExtensionMapCoordinate(24.45, 118.06)
        val second = ExtensionMapCoordinate(24.46, 118.08)
        val item = ExtensionViewItem(type = "map", title = "Harbour route", alt = "Harbour and park map", showList = true,
            routes = listOf(ExtensionMapRoute("a", "By the harbour", distanceMeters = 1400, durationSeconds = 1200, points = listOf(first, second)),
                ExtensionMapRoute("b", "Through the park", summary = "Scenic route", points = listOf(first, ExtensionMapCoordinate(24.44, 118.079), second))),
            markers = listOf(ExtensionMapMarker("start", first.lat, first.lon, label = "Harbour"),
                ExtensionMapMarker("end", second.lat, second.lon, label = "Park")),
            links = listOf(ExtensionMapLink("Open route", "https://example.com/route")))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf(item)
        val opened = mutableListOf<String>()
        val handle = ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, { opened += it })
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.width(360.dp).background(PersonaTheme.palette.background)) {
                    ExtensionMap(shown, ExtensionViewState(), handle)
                }
            }
        }
        val output = File(System.getProperty("persona.shots") ?: "build/extension-map-shots").also { it.mkdirs() }
        for (appearance in AppStyle.entries) for (night in listOf(false, true)) {
            rule.runOnIdle { style = appearance; dark = night }
            rule.waitForIdle()
            val image = rule.onNodeWithTag("extension-map").captureToImage().asAndroidBitmap()
            val name = "${appearance.name.lowercase()}-${if (night) "dark" else "light"}.png"
            File(output, name).outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNodeWithTag("extension-map-full-screen").assertExists()
        rule.onNodeWithText("Zoom in").performClick()
        rule.onNodeWithText("Reset map").performClick()
        val scenic = rule.onNode(hasTestTag("extension-map-route-b") and hasAnyAncestor(hasTestTag("extension-map-full-screen")))
        scenic.performScrollTo().performClick().assertIsSelected()
        rule.runOnIdle {
            shown = item.copy(showList = false, routes = item.routes.orEmpty() +
                ExtensionMapRoute("c", "Along the bank", points = listOf(first, second)))
        }
        scenic.assertIsSelected()
        assertTrue(opened.isEmpty())
    }

    @Test fun fullScreenClosesBeforeDispatchingConfirmationAndLinks() {
        val item = ExtensionViewItem(type = "map", title = "Harbour route", alt = "Harbour map", showList = true,
            markers = listOf(
                ExtensionMapMarker("origin", 24.45, 118.06, label = "Harbour"),
                ExtensionMapMarker("stop", 24.46, 118.08, label = "Park",
                    action = ExtensionAction(id = "visit", confirm = "Visit the park?")),
            ),
            links = listOf(ExtensionMapLink("Open route", "https://example.com/route")),
        )
        var pressed: ExtensionViewAction? = null
        var opened: String? = null
        val handle = ExtensionViewHandle({ _, action -> pressed = action }, { _, _ -> }, {}, { opened = it })
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ExtensionMap(item, ExtensionViewState(), handle)
            }
        }
        val cover = hasTestTag("extension-map-full-screen")
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNode(hasText("Park") and hasAnyAncestor(cover)).performScrollTo().performClick()
        rule.onNode(cover).assertDoesNotExist()
        rule.runOnIdle {
            assertEquals("visit", pressed?.id)
            assertEquals("Visit the park?", pressed?.confirm)
        }
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNode(hasText("Open route") and hasAnyAncestor(cover)).performScrollTo().performClick()
        rule.onNode(cover).assertDoesNotExist()
        rule.runOnIdle { assertEquals("https://example.com/route", opened) }
    }

    @Test fun aReplacedKeylessMapStartsFromItsOwnRouteAndCamera() {
        val first = ExtensionMapCoordinate(24.45, 118.06)
        val second = ExtensionMapCoordinate(24.46, 118.08)
        fun route(id: String) = ExtensionMapRoute(id, id, points = listOf(first, second))
        val one = ExtensionViewItem(type = "map", title = "One", alt = "One", routes = listOf(route("a"), route("b")))
        val two = ExtensionViewItem(type = "map", title = "Two", alt = "Two", routes = listOf(route("x"), route("y")))
        var shown by mutableStateOf(one)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ExtensionMap(shown, ExtensionViewState(), ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {}))
            }
        }
        rule.onNodeWithTag("extension-map-canvas").performClick()
        val cover = hasTestTag("extension-map-full-screen")
        rule.onNode(hasTestTag("extension-map-route-b") and hasAnyAncestor(cover))
            .performScrollTo().performClick().assertIsSelected()
        rule.onNodeWithText("Zoom in").performClick()
        rule.runOnIdle { shown = two }
        rule.onNode(cover).assertDoesNotExist()
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNode(hasTestTag("extension-map-route-x") and hasAnyAncestor(cover)).assertIsSelected()
        rule.onNode(hasTestTag("extension-map-route-b") and hasAnyAncestor(cover)).assertDoesNotExist()
    }

    @Test fun aRouteMapReplacedByMarkersStartsFresh() {
        val first = ExtensionMapCoordinate(24.45, 118.06)
        val second = ExtensionMapCoordinate(24.46, 118.08)
        fun route(id: String) = ExtensionMapRoute(id, id, points = listOf(first, second))
        val markers = listOf(
            ExtensionMapMarker("start", first.lat, first.lon, label = "Harbour"),
            ExtensionMapMarker("park", second.lat, second.lon, label = "Park"),
        )
        val one = ExtensionViewItem(type = "map", title = "Routes", alt = "Routes", showList = true,
            routes = listOf(route("a"), route("b")), markers = markers)
        val onlyMarkers = one.copy(title = "Markers", alt = "Markers", routes = emptyList())
        var shown by mutableStateOf(one)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ExtensionMap(shown, ExtensionViewState(), ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {}))
            }
        }
        rule.onNodeWithTag("extension-map-canvas").performClick()
        val cover = hasTestTag("extension-map-full-screen")
        rule.onNode(hasTestTag("extension-map-route-b") and hasAnyAncestor(cover))
            .performScrollTo().performClick().assertIsSelected()
        rule.onNode(hasText("Park") and hasAnyAncestor(cover))
            .performScrollTo().performClick().assertIsSelected()
        rule.onNodeWithText("Zoom in").performClick()
        rule.runOnIdle { shown = onlyMarkers }
        rule.onNode(cover).assertDoesNotExist()
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNodeWithText("Zoom out").assertIsNotEnabled()
        rule.onNode(hasText("Park") and hasAnyAncestor(cover))
            .performScrollTo().assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }

    @Test fun aLiveMarkerAppendKeepsInteractionButReplacingMarkersStartsFresh() {
        fun marker(id: String, label: String, lon: Double) =
            ExtensionMapMarker(id, 24.45, lon, label = label)
        val one = ExtensionViewItem(type = "map", title = "One", alt = "One",
            markers = listOf(marker("origin", "Harbour", 118.06), marker("park", "Park", 118.08)))
        val appended = one.copy(markers = one.markers.orEmpty() + marker("shop", "Shop", 118.09))
        val replaced = one.copy(title = "Two", alt = "Two",
            markers = listOf(marker("office", "Office", 118.10), marker("beach", "Beach", 118.11)))
        var shown by mutableStateOf(one)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                ExtensionMap(shown, ExtensionViewState(), ExtensionViewHandle({ _, _ -> }, { _, _ -> }, {}, {}))
            }
        }
        rule.onNodeWithTag("extension-map-canvas").performClick()
        val cover = hasTestTag("extension-map-full-screen")
        rule.onNode(hasText("Park") and hasAnyAncestor(cover))
            .performScrollTo().performClick().assertIsSelected()
        rule.onNodeWithText("Zoom in").performClick()
        rule.runOnIdle { shown = appended }
        rule.onNode(cover).assertExists()
        rule.onNode(hasText("Park") and hasAnyAncestor(cover))
            .performScrollTo().assertIsSelected()
        rule.runOnIdle { shown = replaced }
        rule.onNode(cover).assertDoesNotExist()
        rule.onNodeWithTag("extension-map-canvas").performClick()
        rule.onNodeWithText("Zoom out").assertIsNotEnabled()
        rule.onNode(hasText("Office") and hasAnyAncestor(cover))
            .performScrollTo().assert(SemanticsMatcher.keyNotDefined(SemanticsProperties.Selected))
    }
}
