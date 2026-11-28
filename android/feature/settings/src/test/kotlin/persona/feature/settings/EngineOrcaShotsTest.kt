package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
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
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.DetectedEngine
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.MachineManagement
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.lang.reflect.Proxy

/**
 * The engine picker's Orca row (#17037): installed and not running, with its version, and an incompatible one with its
 * version and the hub's note, in every style, light and dark. Written as PNGs to `build/engine-orca-shots/` (or
 * `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
class EngineOrcaShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/engine-orca-shots").also { it.mkdirs() }

    private val herdr = DetectedEngine("herdr", "herdr", available = true, version = "0.9.5")
    private val stopped = Machine("m1", "Studio", online = true, engine = "orca", engineHosts = listOf("orca"), engineDefault = "orca",
        engines = listOf(herdr, DetectedEngine("orca", "Orca", available = false, version = "1.4.277", state = "stopped")))
    private val incompatible = stopped.copy(engines = listOf(herdr, DetectedEngine("orca", "Orca", available = false, version = "1.4.100",
        note = "This version of Orca doesn’t work with Persona. Update both to the latest version", state = "incompatible")))
    private val machines = MutableStateFlow(listOf(stopped))
    private val sessions = object : SessionsRepository {
        override val machines = this@EngineOrcaShotsTest.machines
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }

    // Only drawn, never called: the page holds on to it (the pairing step reads nothing else from it).
    private val management = Proxy.newProxyInstance(MachineManagement::class.java.classLoader, arrayOf(MachineManagement::class.java)) { proxy, method, args ->
        when (method.name) { // Compose compares it between frames
            "equals" -> proxy === args?.firstOrNull()
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "MachineManagement(shots)"
            else -> throw UnsupportedOperationException(method.name)
        }
    } as MachineManagement

    @Test fun everyStyleLightAndDark() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var which by mutableStateOf("stopped")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    key(which, style, dark) { EngineSelectionScreen("m1", sessions, management, pairing = true, onDone = {}) }
                }
            }
        }
        for ((name, machine, line) in listOf(Triple("stopped", stopped, "Installed, not running, version 1.4.277"),
            Triple("incompatible", incompatible, "Not compatible, version 1.4.100"))) {
            machines.value = listOf(machine)
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { which = name; style = s; dark = d }
                rule.waitForIdle()
                rule.onNodeWithText(line).assertExists()
                val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
                var other = 0
                val background = bitmap.getPixel(1, 1)
                for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
                assertTrue("$name $s drew nothing", other > 100)
                File(out(), "engine-orca-$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                    bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
                }
            }
        }
    }
}
