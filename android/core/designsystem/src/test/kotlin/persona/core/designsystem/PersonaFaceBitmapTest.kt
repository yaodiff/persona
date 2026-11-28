package persona.core.designsystem

import android.graphics.Bitmap
import android.graphics.Color as AndroidColor
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import kotlin.math.roundToInt

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class PersonaFaceBitmapTest {
    private fun pixelHash(bitmap: Bitmap): Int {
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        return pixels.contentHashCode()
    }

    @Test
    fun rendersAllStatesInLightAndDarkWithDistinctOutput() {
        val ringLight = SystemSurfaces.palette(AppStyle.Glass, dark = false).needsYou.dot
        val ringDark = SystemSurfaces.palette(AppStyle.Glass, dark = true).needsYou.dot

        for (dark in listOf(false, true)) {
            val ring = if (dark) ringDark else ringLight
            val hashes = PersonaState.entries.associateWith { state ->
                val bmp = renderPersonaFaceBitmap(state = state, sizePx = 84, dark = dark, needsYouRing = ring, density = 3f, compact = true)
                assertEquals(84, bmp.width)
                assertEquals(84, bmp.height)
                assertEquals(Bitmap.Config.ARGB_8888, bmp.config)
                assertFalse(bmp.isRecycled)
                pixelHash(bmp)
            }
            // Idle, Thinking (thought bubble), NeedsYou (outer ring), and Offline (muted disc) should be visually distinct.
            assertNotEquals("Idle vs Thinking (dark=$dark)", hashes[PersonaState.Idle], hashes[PersonaState.Thinking])
            assertNotEquals("Idle vs NeedsYou (dark=$dark)", hashes[PersonaState.Idle], hashes[PersonaState.NeedsYou])
            assertNotEquals("Idle vs Working (dark=$dark)", hashes[PersonaState.Idle], hashes[PersonaState.Working])
            assertNotEquals("Idle vs Offline (dark=$dark)", hashes[PersonaState.Idle], hashes[PersonaState.Offline])
        }

        // Cached helper returns a valid bitmap of the requested size.
        val cached = personaFaceBitmap(PersonaState.Thinking, sizePx = 72, dark = false, needsYouRing = ringLight)
        assertEquals(72, cached.width)
        assertTrue(cached === personaFaceBitmap(PersonaState.Thinking, sizePx = 72, dark = false, needsYouRing = ringLight))
    }

    @Test
    fun idleBodyFillIsOpaqueBrightWhiteInLightAndDark() {
        val sizePx = 96
        val ringLight = SystemSurfaces.palette(AppStyle.Glass, dark = false).needsYou.dot
        val ringDark = SystemSurfaces.palette(AppStyle.Glass, dark = true).needsYou.dot

        // Sample two interior body points well clear of eyes, cheek, gloss, and outline:
        // belly center (480, 620) and upper forehead (480, 380) in Mochi's 1024-unit space.
        val belly = Mochi.p(480f, 620f) * sizePx.toFloat()
        val forehead = Mochi.p(480f, 380f) * sizePx.toFloat()
        val bx = belly.x.roundToInt()
        val by = belly.y.roundToInt()
        val fx = forehead.x.roundToInt()
        val fy = forehead.y.roundToInt()

        for (compact in listOf(true, false)) {
            val lightBmp = renderPersonaFaceBitmap(PersonaState.Idle, sizePx, dark = false, needsYouRing = ringLight, compact = compact)
            val darkBmp = renderPersonaFaceBitmap(PersonaState.Idle, sizePx, dark = true, needsYouRing = ringDark, compact = compact)

            for ((label, bmp) in listOf("light" to lightBmp, "dark" to darkBmp)) {
                val bellyPx = bmp.getPixel(bx, by)
                assertEquals("$label belly alpha (compact=$compact)", 255, AndroidColor.alpha(bellyPx))
                assertTrue(
                    "$label belly RGB should stay near white (#FFFFFF->#E9E3DE), was rgb(${AndroidColor.red(bellyPx)},${AndroidColor.green(bellyPx)},${AndroidColor.blue(bellyPx)})",
                    AndroidColor.red(bellyPx) >= 230 && AndroidColor.green(bellyPx) >= 225 && AndroidColor.blue(bellyPx) >= 220,
                )

                val headPx = bmp.getPixel(fx, fy)
                assertEquals("$label forehead alpha (compact=$compact)", 255, AndroidColor.alpha(headPx))
                assertTrue(
                    "$label forehead RGB should stay near pure white, was rgb(${AndroidColor.red(headPx)},${AndroidColor.green(headPx)},${AndroidColor.blue(headPx)})",
                    AndroidColor.red(headPx) >= 248 && AndroidColor.green(headPx) >= 248 && AndroidColor.blue(headPx) >= 245,
                )
            }

            // Opaque body fill must not let the dark or light background disc bleed through.
            assertEquals("belly pixel must match across light and dark (compact=$compact)", lightBmp.getPixel(bx, by), darkBmp.getPixel(bx, by))
            assertEquals("forehead pixel must match across light and dark (compact=$compact)", lightBmp.getPixel(fx, fy), darkBmp.getPixel(fx, fy))
        }
    }
}
