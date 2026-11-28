package persona.feature.machines

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.DetectedEngine
import persona.core.contracts.Machine
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/**
 * A machine page's Engine row for a computer that uses herdr, Orca and Paseo together (#17052): the row names the whole set
 * and the note covers every engine it uses. Every style, light and dark; PNGs go to `build/engine-set-shots/` (or
 * `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MachineEngineSetShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/engine-set-shots").also { it.mkdirs() }

    private val machine = Machine("m1", "Workstation", kind = "ssh", sshHost = "workstation.example.com", online = true, engine = "herdr",
        engineHosts = listOf("herdr", "orca", "paseo"), engineDefault = "herdr",
        engines = listOf(DetectedEngine("herdr", "herdr", true), DetectedEngine("orca", "Orca", true), DetectedEngine("paseo", "Paseo", true)))

    @Config(sdk = [35], qualifiers = "en-w400dp-h700dp-xhdpi")
    @Test fun theRowNamesTheWholeSet() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(700.dp).background(PersonaTheme.palette.background)) {
                    LazyColumn { machineControls(machine, reconnecting = false, onEngine = {}, onReconnect = {}) }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("herdr + Orca + Paseo").assertExists()
            rule.onNodeWithText(rule.activity.getString(R.string.machines_engine_note)).assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            var other = 0
            val background = bitmap.getPixel(1, 1)
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
            assertTrue("drew nothing", other > 100)
            val name = "machine-engine-set-en-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
