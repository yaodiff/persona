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
import androidx.compose.ui.test.assertIsOff
import androidx.compose.ui.test.assertIsOn
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
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
 * "Start new Agents as Orca chats" on Engine (#17650): off on a computer with Orca in its set, on after a tap (saved at
 * once), and no switch on a computer without Orca. Every style, light and dark, in English, plus Chinese and Japanese;
 * PNGs go to `build/orca-chat-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class OrcaChatStartsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/orca-chat-shots").also { it.mkdirs() }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi")
    @Test fun english() = shots("en", every = true)

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", every = false)

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1100dp-xhdpi")
    @Test fun japanese() = shots("ja", every = false)

    private enum class Shot { Off, On, WithoutOrca }

    private val engines = listOf(DetectedEngine("herdr", "herdr", true, version = "0.9.3"), DetectedEngine("orca", "Orca", true, version = "1.4.277"))
    private val withOrca = Machine("m1", "Workstation", online = true, engine = "herdr", engineHosts = listOf("herdr", "orca"),
        engineDefault = "herdr", engines = engines, orcaChatStarts = "off")
    private val withoutOrca = withOrca.copy(engineHosts = listOf("herdr"), orcaChatStarts = null)
    private val machines = MutableStateFlow(listOf(withOrca))
    private val sessions = object : SessionsRepository {
        override val machines = this@OrcaChatStartsShotsTest.machines
        override val sessions = MutableStateFlow(emptyList<Session>())
        override suspend fun refresh() = Unit
    }
    private val saved = mutableListOf<Boolean>()
    private val management: MachineManagement = Proxy.newProxyInstance(MachineManagement::class.java.classLoader, arrayOf(MachineManagement::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args?.firstOrNull()
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "MachineManagement"
            // a suspend function's last argument is its continuation: the answer goes back as the result
            "setOrcaChatStarts" -> (args!![1] as Boolean).also { saved += it }
            else -> throw UnsupportedOperationException(method.name)
        }
    } as MachineManagement

    private fun shots(language: String, every: Boolean) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                    key(round) { EngineSelectionScreen("m1", sessions, management, pairing = false, onDone = {}, onBack = {}) }
                }
            }
        }
        val variants = if (every) AppStyle.entries.flatMap { listOf(it to false, it to true) } else listOf(AppStyle.Glass to false)
        for ((s, d) in variants) for (shot in Shot.entries) {
            style = s; dark = d
            when (shot) {
                Shot.Off -> { machines.value = listOf(withOrca); round++; rule.waitForIdle(); rule.onNodeWithTag("orca-chat-starts").assertIsOff() }
                Shot.On -> {
                    rule.onNodeWithTag("orca-chat-starts").performClick()
                    rule.waitForIdle()
                    rule.onNodeWithTag("orca-chat-starts").assertIsOn()
                    assertEquals(true, saved.last())
                }
                Shot.WithoutOrca -> {
                    machines.value = listOf(withoutOrca)
                    round++
                    rule.waitForIdle()
                    assertTrue(rule.onAllNodesWithTagCount("orca-chat-starts") == 0)
                }
            }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val name = "orca-chat-starts-$language-${shot.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png"
            File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun androidx.compose.ui.test.junit4.AndroidComposeTestRule<*, *>.onAllNodesWithTagCount(tag: String): Int =
        onAllNodes(androidx.compose.ui.test.hasTestTag(tag)).fetchSemanticsNodes().size
}
