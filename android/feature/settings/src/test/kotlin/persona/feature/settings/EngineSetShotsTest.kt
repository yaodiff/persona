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
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
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
 * Choose Engine on a computer that uses herdr, Orca and Paseo together, set up on the web (#17052): the set and its default
 * above the options, Save off until the choice changes, and the note for a pick inside the set and outside it. Every style,
 * light and dark, in English, plus Chinese and Japanese; PNGs go to `build/engine-set-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class EngineSetShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/engine-set-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
    @Test fun english() = shots("en", every = true)

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", every = false)

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1200dp-xhdpi")
    @Test fun japanese() = shots("ja", every = false)

    private enum class Shot { Opened, InsideTheSet, OutsideTheSet }

    private val machine = Machine("m1", "Workstation", online = true, engine = "herdr",
        engineHosts = listOf("herdr", "orca", "paseo"), engineDefault = "herdr",
        engines = listOf(DetectedEngine("herdr", "herdr", true, version = "0.9.3"), DetectedEngine("herdr-core", "Herdr App", false),
            DetectedEngine("orca", "Orca", true, version = "1.4.277"), DetectedEngine("paseo", "Paseo", true)))
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(machine))
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
    // Only drawn, never called: Save stays off or is not tapped.
    private val management: MachineManagement = Proxy.newProxyInstance(MachineManagement::class.java.classLoader, arrayOf(MachineManagement::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args?.firstOrNull()
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "MachineManagement"
            else -> throw UnsupportedOperationException(method.name)
        }
    } as MachineManagement

    private fun shots(language: String, every: Boolean) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1200.dp).background(PersonaTheme.palette.background)) {
                    key(round) { EngineSelectionScreen("m1", sessions, management, pairing = false, onDone = {}, onBack = {}) }
                }
            }
        }
        val save = rule.activity.getString(R.string.settings_engine_save)
        val variants = if (every) AppStyle.entries.flatMap { listOf(it to false, it to true) } else listOf(AppStyle.Glass to false)
        for ((s, d) in variants) for (shot in Shot.entries) {
            style = s; dark = d
            if (shot == Shot.Opened) round++
            rule.waitForIdle()
            when (shot) {
                Shot.Opened -> rule.onNode(hasText(save) and hasClickAction()).assertIsNotEnabled()
                Shot.InsideTheSet -> {
                    rule.onNodeWithText("Orca").performClick()
                    rule.waitForIdle()
                    rule.onNode(hasText(save) and hasClickAction()).assertIsEnabled()
                }
                Shot.OutsideTheSet -> {
                    rule.onNodeWithText("Herdr App").performClick()
                    rule.waitForIdle()
                    rule.onNode(hasText(save) and hasClickAction()).assertIsEnabled()
                }
            }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            var other = 0
            val background = bitmap.getPixel(1, 1)
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
            assertTrue("$shot drew nothing", other > 100)
            val name = "engine-set-$language-${shot.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
