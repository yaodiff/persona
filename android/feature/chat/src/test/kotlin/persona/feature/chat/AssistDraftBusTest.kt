package persona.feature.chat

import androidx.compose.ui.text.input.TextFieldValue
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.ImageLimits
import persona.core.contracts.SessionKey

class AssistDraftBusTest {
    private val mainSession = SessionKey("machine-1", "main-session")
    private val screenshot = ComposerAttachment(
        id = "assist-shot-1",
        name = "assist-1.jpg",
        uri = "content://com.example.persona.chat.files/assist/assist-1.jpg",
        image = true,
        size = 2048L,
    )
    private val pageContext = ChatLinkContext(
        kind = "context",
        title = "Docs",
        url = "https://example.com/docs",
        text = "Docs\nhttps://example.com/docs",
        id = "assist-ctx-1",
    )

    @Test
    fun outOfOrderContextAndLateScreenshotPreserveClaimedTarget() {
        var now = 1_000L
        val bus = AssistDraftQueue(clock = { now })

        bus.attachContext("h1", pageContext)
        val claimed = bus.claim(AssistDraftTarget.Session(mainSession))
        assertNotNull(claimed)
        assertEquals(AssistDraftTarget.Session(mainSession), claimed!!.target)
        assertNull(claimed.screenshot)
        assertEquals(pageContext, claimed.context)

        bus.markContextApplied("h1")
        assertFalse(bus.current()!!.hasUnappliedContent)

        now += 200L
        bus.attachScreenshot("h1", screenshot)
        val updated = bus.current()!!
        assertEquals(AssistDraftTarget.Session(mainSession), updated.target)
        assertEquals(screenshot, updated.screenshot)
        assertFalse(updated.screenshotApplied)
        assertTrue(updated.contextApplied)
        assertTrue(updated.hasUnappliedContent)
    }

    @Test
    fun prefillDraftPreservesExistingTextDeduplicatesAndEnforcesMediaCap() {
        val composer = ChatComposerState(ChatComposerDraft(text = "Existing draft question"))
        val contexts = mutableListOf<ChatLinkContext>()
        val draft = PendingAssistDraft(
            id = "h1",
            createdAtMillis = 100L,
            screenshot = screenshot,
            context = pageContext,
            target = AssistDraftTarget.Session(mainSession),
        )

        val first = prefillAssistDraft(composer, draft, existingContextIds = emptySet()) { contexts += it }
        assertTrue(first.screenshotAdded)
        assertFalse(first.screenshotRejectedForLimit)
        assertTrue(first.contextAdded)
        assertEquals("Existing draft question", composer.value.text)
        assertEquals(listOf(screenshot), composer.attachments)
        assertEquals(listOf(pageContext), contexts)

        // Second application with same ids must not duplicate attachment or context.
        val duplicate = prefillAssistDraft(composer, draft, existingContextIds = contexts.map { it.id }.toSet()) { contexts += it }
        assertFalse(duplicate.screenshotAdded)
        assertFalse(duplicate.contextAdded)
        assertEquals(1, composer.attachments.size)
        assertEquals(1, contexts.size)

        // When inline photo slots are full, reject cleanly instead of exceeding ImageLimits.DEFAULT_PER_MESSAGE.
        val fullComposer = ChatComposerState(
            ChatComposerDraft(
                attachments = (1..ImageLimits.DEFAULT_PER_MESSAGE).map { i ->
                    ComposerAttachment("img-$i", "img-$i.jpg", "content://example/img-$i", image = true)
                },
            ),
        )
        fullComposer.value = TextFieldValue("keep")
        val capped = prefillAssistDraft(fullComposer, draft)
        assertFalse(capped.screenshotAdded)
        assertTrue(capped.screenshotRejectedForLimit)
        assertEquals(ImageLimits.DEFAULT_PER_MESSAGE, fullComposer.attachments.size)
        assertEquals("keep", fullComposer.value.text)

        // Custom per-agent image limits are respected, and videos do not consume inline photo slots.
        val tightLimits = ImageLimits(perMessage = 2)
        val withVideoAndPhoto = ChatComposerState(
            ChatComposerDraft(
                attachments = listOf(
                    ComposerAttachment("vid-1", "clip.mp4", "content://example/clip.mp4", image = false, video = true),
                    ComposerAttachment("img-1", "img-1.jpg", "content://example/img-1", image = true),
                ),
            ),
        )
        val admittedBesideVideo = prefillAssistDraft(withVideoAndPhoto, draft, limits = tightLimits)
        assertTrue(admittedBesideVideo.screenshotAdded)
        assertFalse(admittedBesideVideo.screenshotRejectedForLimit)
        assertEquals(3, withVideoAndPhoto.attachments.size)

        val secondScreenshotDraft = draft.copy(
            screenshot = screenshot.copy(id = "assist-shot-2", uri = "content://com.example.persona.chat.files/assist/assist-2.jpg"),
        )
        val cappedByTightLimit = prefillAssistDraft(withVideoAndPhoto, secondScreenshotDraft, limits = tightLimits)
        assertFalse(cappedByTightLimit.screenshotAdded)
        assertTrue(cappedByTightLimit.screenshotRejectedForLimit)
        assertEquals(3, withVideoAndPhoto.attachments.size)
    }

    @Test
    fun expiredHandoffIsDroppedOnClaim() {
        var now = 1_000L
        val bus = AssistDraftQueue(clock = { now }, maxAgeMillis = 5_000L)
        bus.publish("stale", screenshot = screenshot, context = pageContext)
        now += 6_000L
        assertNull(bus.claim(AssistDraftTarget.Starter))
        assertNull(bus.current())
    }

    @Test
    fun assistCacheUrisAreCleanedUpWhenLeavingComposerDraft() {
        val pkg = "com.example.persona"
        val assistUri = "content://$pkg.chat.files/assist/assist-123.jpg"
        assertTrue(isCaptureUri(assistUri, pkg))

        val before = listOf(screenshot.copy(uri = assistUri))
        assertEquals(listOf(assistUri), leftCaptures(before, emptyList(), pkg))
    }

    @Test
    fun unsentOverlayTextHandoffAppendsCleanlyWithoutOverwritingExistingDraft() {
        val bus = AssistDraftQueue(clock = { 1_000L })
        bus.publish(id = "h2", screenshot = screenshot, text = "Summarize this chart")
        val claimed = bus.claim(AssistDraftTarget.Session(mainSession))!!
        assertTrue(claimed.hasUnappliedContent)
        assertEquals("Summarize this chart", claimed.text)

        val composer = ChatComposerState(ChatComposerDraft(text = "Earlier note"))
        val applied = prefillAssistDraft(composer, claimed)
        assertTrue(applied.screenshotAdded)
        assertTrue(applied.textAdded)
        assertEquals("Earlier note\nSummarize this chart", composer.value.text)

        bus.markScreenshotApplied("h2")
        bus.markTextApplied("h2")
        assertFalse(bus.current()!!.hasUnappliedContent)
    }

    @Test
    fun composeReplacementKeepsAttachmentsAndUsesTheExactPromptText() {
        val bus = AssistDraftQueue(clock = { 1_000L })
        val prompt = "  What happened today?  "
        bus.publish(id = "compose", text = prompt, textMode = AssistDraftTextMode.Replace)
        val claimed = bus.claim(AssistDraftTarget.Session(mainSession))!!
        assertEquals(prompt, claimed.text)

        val attachment = ComposerAttachment("file-1", "notes.txt", "content://example/notes.txt", image = false)
        val composer = ChatComposerState(ChatComposerDraft(text = "Earlier note", attachments = listOf(attachment)))
        val applied = prefillAssistDraft(composer, claimed)
        assertTrue(applied.textAdded)
        assertEquals(prompt, composer.value.text)
        assertEquals(listOf(attachment), composer.attachments)
    }
}
