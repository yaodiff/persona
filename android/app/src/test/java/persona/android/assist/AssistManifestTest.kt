package persona.android.assist

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.w3c.dom.Element

class AssistManifestTest {
    private val androidNs = "http://schemas.android.com/apk/res/android"

    private fun parse(path: String) = DocumentBuilderFactory.newInstance()
        .apply { isNamespaceAware = true }
        .newDocumentBuilder()
        .parse(File(path))

    private fun elements(parent: Element, tag: String): List<Element> {
        val nodes = parent.getElementsByTagName(tag)
        return (0 until nodes.length).map { nodes.item(it) as Element }
    }

    @Test
    fun manifestDeclaresVoiceInteractionServicesAndAssistActivity() {
        val manifest = parse("src/main/AndroidManifest.xml").documentElement
        val permissions = elements(manifest, "uses-permission")
            .map { it.getAttributeNS(androidNs, "name") }
        assertTrue(
            "Android 17+ (targetSdk=37) requires READ_ASSIST_STRUCTURE_SCREEN_CONTENT for assist context & screenshots",
            permissions.contains("android.permission.READ_ASSIST_STRUCTURE_SCREEN_CONTENT"),
        )
        val app = elements(manifest, "application").single()
        val services = elements(app, "service").associateBy { it.getAttributeNS(androidNs, "name") }

        val interaction = services[".assist.PersonaVoiceInteractionService"]
        assertNotNull("Missing PersonaVoiceInteractionService", interaction)
        assertEquals("android.permission.BIND_VOICE_INTERACTION", interaction!!.getAttributeNS(androidNs, "permission"))
        assertEquals("true", interaction.getAttributeNS(androidNs, "exported"))
        val interactionActions = elements(interaction, "action").map { it.getAttributeNS(androidNs, "name") }
        assertTrue(interactionActions.contains("android.service.voice.VoiceInteractionService"))
        val metadata = elements(interaction, "meta-data")
            .firstOrNull { it.getAttributeNS(androidNs, "name") == "android.voice_interaction" }
        assertNotNull("Missing android.voice_interaction metadata", metadata)
        assertEquals("@xml/voice_interaction_service", metadata!!.getAttributeNS(androidNs, "resource"))

        val session = services[".assist.PersonaVoiceInteractionSessionService"]
        assertNotNull("Missing PersonaVoiceInteractionSessionService", session)
        assertEquals("android.permission.BIND_VOICE_INTERACTION", session!!.getAttributeNS(androidNs, "permission"))
        assertEquals("true", session.getAttributeNS(androidNs, "exported"))

        val recognition = services[".assist.PersonaRecognitionService"]
        assertNotNull("Missing PersonaRecognitionService", recognition)
        assertEquals("false", recognition!!.getAttributeNS(androidNs, "exported"))
        assertTrue(elements(recognition, "intent-filter").isEmpty())

        val activities = elements(app, "activity").associateBy { it.getAttributeNS(androidNs, "name") }
        val assistActivity = activities[".assist.AssistActivity"]
        assertNotNull("Missing AssistActivity", assistActivity)
        assertEquals("true", assistActivity!!.getAttributeNS(androidNs, "exported"))
        assertEquals("true", assistActivity.getAttributeNS(androidNs, "excludeFromRecents"))
        assertEquals("@android:style/Theme.Translucent.NoTitleBar", assistActivity.getAttributeNS(androidNs, "theme"))
        assertEquals("adjustNothing|stateHidden", assistActivity.getAttributeNS(androidNs, "windowSoftInputMode"))
        val assistActions = elements(assistActivity, "action").map { it.getAttributeNS(androidNs, "name") }
        val assistCategories = elements(assistActivity, "category").map { it.getAttributeNS(androidNs, "name") }
        assertTrue(assistActions.contains("android.intent.action.ASSIST"))
        assertTrue(assistCategories.contains("android.intent.category.DEFAULT"))
    }

    @Test
    fun voiceInteractionServiceXmlMatchesServicesAndFlags() {
        val root = parse("src/main/res/xml/voice_interaction_service.xml").documentElement
        assertEquals("voice-interaction-service", root.tagName)
        assertEquals(
            "persona.android.assist.PersonaVoiceInteractionSessionService",
            root.getAttributeNS(androidNs, "sessionService"),
        )
        assertEquals(
            "persona.android.assist.PersonaRecognitionService",
            root.getAttributeNS(androidNs, "recognitionService"),
        )
        assertEquals("true", root.getAttributeNS(androidNs, "supportsAssist"))
        assertEquals("true", root.getAttributeNS(androidNs, "supportsLaunchVoiceAssistFromKeyguard"))
        assertEquals(
            "Android 17 (API 37) requires usesAssistData=true so VoiceInteractionManagerService delivers assist data",
            "true",
            root.getAttributeNS(androidNs, "usesAssistData"),
        )
        assertEquals(
            "Android 17 (API 37) requires usesAssistScreenshots=true so VoiceInteractionManagerService sets SHOW_WITH_SCREENSHOT",
            "true",
            root.getAttributeNS(androidNs, "usesAssistScreenshots"),
        )
        assertEquals(
            "Android 17 (API 37) requires usesAssistStructureScreenContent=true for assist screen content",
            "true",
            root.getAttributeNS(androidNs, "usesAssistStructureScreenContent"),
        )
        assertFalse(root.getAttributeNS(androidNs, "sessionService").startsWith("."))
    }

    @Test
    fun chatFileProviderExposesPrivateAssistCachePath() {
        val root = parse("../feature/chat/src/main/res/xml/chat_file_paths.xml").documentElement
        val cachePaths = elements(root, "cache-path")
        assertTrue(
            cachePaths.any { it.getAttribute("name") == "assist" && it.getAttribute("path") == "assist/" },
        )
    }
}
