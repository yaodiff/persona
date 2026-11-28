package persona.feature.inbox

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.MailAccount
import persona.core.contracts.api.MailAddress
import persona.core.contracts.api.MailAttachment
import persona.core.contracts.api.MailInboxItem
import persona.core.contracts.api.MailMessage
import persona.core.contracts.api.MailReference
import persona.core.contracts.api.MailTransport
import persona.core.contracts.api.SendMailRequest
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalMailAccess
import persona.core.designsystem.MailAccess
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.inbox.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.io.OutputStream

private const val ItemId = "mail:a1:8"
private const val Stored = "Hi,\n\nThe Q4 plan is attached. Can you review it by Friday?\n\nAlex"

private val Message = MailMessage(
    accountId = "a1", uid = 8, subject = "Q4 planning", from = MailAddress("Alex Example", "alex@example.com"),
    to = listOf(MailAddress("Me", "me@example.com")), date = "2026-11-04T10:00:00Z", seen = true,
    text = "$Stored\n\n-- \nThe live message goes on past what the hub stored.",
    attachments = listOf(MailAttachment("q4-plan.pdf", "application/pdf", 182_400, "1")), messageId = "<m8@example.com>",
)

private fun mailRecord(preview: String = "Hi, The Q4 plan is attached.", agent: InboxAgent? = null) = InboxRecord(
    InboxItem(ItemId, kind = "mail", title = Message.subject, sender = Message.from.display(), preview = preview,
        updatedAt = "2026-11-04T10:00:00Z", summary = "Alex asks for a review of the Q4 plan by Friday.", tier = "action"),
    source = "mail", mail = InboxMailRef("a1", uids = listOf(7, 8)), agent = agent,
)

/** Every route under `/api/v1/mail` (and the item read the live page starts with), by name as it is asked. */
private class RecordingMail : MailTransport {
    override val scope = "test"
    val asked = mutableListOf<String>()
    override suspend fun item(itemId: String): MailInboxItem { asked += "item"; return MailInboxItem(itemId, MailReference("a1", uids = listOf(7, 8))) }
    override suspend fun message(ref: MailReference, uid: Long): MailMessage { asked += "message"; return Message.copy(uid = uid) }
    override suspend fun setSeen(ref: MailReference, uid: Long, seen: Boolean) { asked += "flags" }
    override suspend fun setFlags(ref: MailReference, uid: Long, seen: Boolean?, flagged: Boolean?) { asked += "flags" }
    override suspend fun accounts(): List<MailAccount> { asked += "accounts"; return listOf(MailAccount(id = "a1", email = "me@example.com")) }
    override suspend fun sendMail(request: SendMailRequest) { asked += "send" }
    override suspend fun refreshInbox() { asked += "refresh" }
    override suspend fun downloadAttachment(ref: MailReference, uid: Long, attachmentId: String, output: OutputStream, offset: Long,
        progress: (Long, Long?) -> Unit) { asked += "attachment" }
}

/** An Inbox whose stored text for the item is [context] (the hub's `context` of `GET /api/v1/inbox/{id}`). */
private class StoredInbox(
    private val base: FakeInboxRepository,
    private val context: String?,
    mail: MailTransport? = null,
    var clipping: StoredMailClipping = StoredMailClipping.Unknown,
) : InboxRepository by base {
    val contexts = mutableListOf<String>()
    val done = mutableListOf<Pair<String, Boolean>>()
    override val mailTransport = MutableStateFlow(mail)
    override suspend fun personaContext(itemId: String): PersonaContext? {
        contexts += itemId
        return context?.let { PersonaContext(it, clipping) }
    }
    override suspend fun setDone(itemId: String, done: Boolean) { this.done += itemId to done }
    override suspend fun item(itemId: String): InboxRecord? = null
}

private val navigator = object : Navigator {
    override fun navigate(key: NavKey) = Unit
    override fun back() = Unit
    override fun selectTab(tab: AppTab) = Unit
}

/**
 * The Inbox follows the hub's mail module (#15995, the rule of #13430). With mail stored-only an opened mail asks no
 * mail route and shows what the hub stored with one line saying why there is no more; nothing that sets mail up, sends,
 * flags or downloads is offered; the Inbox's own actions stay. With mail live the page and its requests are today's.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h1500dp-xhdpi")
class MailModuleOffTest {
    @get:Rule val rule = createComposeRule()

    private fun text(id: Int): String = ApplicationProvider.getApplicationContext<Context>().getString(id)
    private val offLine get() = text(R.string.inbox_mail_module_off)
    private val clippedLine get() = text(R.string.inbox_mail_module_off_clipped)
    private fun shown(text: String, substring: Boolean = false) = rule.onAllNodesWithText(text, substring = substring).fetchSemanticsNodes().isNotEmpty()
    private fun waitFor(text: String, substring: Boolean = false) = rule.waitUntil(5_000) { shown(text, substring) }

    private fun show(access: MailAccess, content: @Composable () -> Unit) = rule.setContent {
        PersonaTheme(reduceMotion = true) {
            CompositionLocalProvider(LocalMailAccess provides access) { Box(Modifier.width(400.dp).height(1500.dp)) { content() } }
        }
    }

    private fun inbox(
        context: String?,
        record: InboxRecord? = mailRecord(),
        mail: MailTransport? = null,
        clipping: StoredMailClipping = StoredMailClipping.Unknown,
    ): StoredInbox {
        val base = FakeInboxRepository()
        base.snapshot.value = InboxSnapshot(records = listOfNotNull(record))
        return StoredInbox(base, context, mail, clipping)
    }

    /** What only a live mail offers: sending, flags, attachments. */
    private val mailOnly get() = listOf(R.string.inbox_mail_reply, R.string.inbox_mail_reply_all, R.string.inbox_mail_forward, R.string.inbox_mail_flag,
        R.string.inbox_mail_mark_read, R.string.inbox_mail_mark_unread, R.string.inbox_mail_attachments, R.string.inbox_ai_draft)

    @Test fun aReplyIsOfferedEverywhereExceptToAMailWhileMailIsStoredOnly() {
        val mail = mailRecord()
        val chat = InboxRecord(InboxItem("lark:1", kind = "dm", title = "Chat"), source = "lark", feedId = "c1")
        assertTrue(MailAccess.Live.offersReply(mail))
        assertTrue(MailAccess.Live.offersReply(chat))
        assertFalse(MailAccess.StoredOnly.offersReply(mail))
        assertTrue(MailAccess.StoredOnly.offersReply(chat))
        // The reply sheet of the event page and its hand-off bar.
        assertTrue(inboxSupportsDraftReply(mail))
        assertTrue(inboxSupportsDraftReply(mail, MailAccess.Live))
        assertFalse(inboxSupportsDraftReply(mail, MailAccess.StoredOnly))
        assertTrue(inboxSupportsDraftReply(chat, MailAccess.StoredOnly))
    }

    @Test fun storedOnlyAnOpenedMailAsksNoMailRouteAndShowsTheStoredTextWithTheLine() {
        val mail = RecordingMail()
        val repository = inbox(Stored, mail = mail)
        show(MailAccess.StoredOnly) { MailScreen(repository, mail, MailRoute(ItemId), navigator) }
        waitFor("Can you review it by Friday?", substring = true)
        waitFor(offLine)
        // Headers and the hub's summary stay.
        assertTrue(shown("Q4 planning"))
        assertTrue(shown("Alex Example <alex@example.com>"))
        assertTrue(shown(text(R.string.inbox_mail_from)))
        assertTrue(shown("Alex asks for a review of the Q4 plan by Friday."))
        // Only what was stored: the rest of the live message is not there, and nobody claims to have loaded it.
        assertFalse(shown("The live message goes on", substring = true))
        // No raw error, no Retry, no control that needs the mail module.
        assertFalse(shown(text(R.string.inbox_mail_load_failed)))
        assertFalse(shown(text(R.string.inbox_retry)))
        assertFalse(shown(text(R.string.inbox_mail_go_back)))
        for (gone in mailOnly) rule.onAllNodesWithText(text(gone)).assertCountEquals(0)
        rule.onAllNodesWithContentDescription(text(R.string.inbox_mail_flag)).assertCountEquals(0)
        // The Inbox's own actions stay.
        assertTrue(shown(text(R.string.inbox_hand_to_ai)))
        assertTrue(shown(text(R.string.inbox_mail_mark_done)))
        rule.waitForIdle()
        assertEquals(emptyList<String>(), mail.asked)
        assertEquals(listOf(ItemId), repository.contexts.distinct())
    }

    @Test fun storedOnlyWithNoStoredTextThePreviewIsShown() {
        val repository = inbox(context = null, record = mailRecord(preview = "Only the preview was kept."))
        show(MailAccess.StoredOnly) { MailScreen(repository, RecordingMail(), MailRoute(ItemId), navigator) }
        waitFor("Only the preview was kept.")
        waitFor(offLine)
    }

    @Test fun storedOnlyWithNothingKeptSaysSoCalmly() {
        val repository = inbox(context = "", record = mailRecord(preview = ""))
        show(MailAccess.StoredOnly) { MailScreen(repository, RecordingMail(), MailRoute(ItemId), navigator) }
        waitFor(text(R.string.inbox_mail_stored_empty))
        waitFor(offLine)
        assertFalse(shown(text(R.string.inbox_mail_load_failed)))
    }

    @Test fun storedOnlyClippedTextAddsThatTheTextIsCutShort() {
        val mail = RecordingMail()
        val repository = inbox(Stored, clipping = StoredMailClipping.Clipped)
        show(MailAccess.StoredOnly) { MailScreen(repository, mail, MailRoute(ItemId), navigator) }
        waitFor(clippedLine)
        // The stored text itself still renders; only the notice changes.
        assertTrue(shown("Can you review it by Friday?", substring = true))
        // The plain sentence (which ends at "kept.") is not the clipped notice.
        assertFalse(shown(offLine))
        // Knowing the text was clipped asks no mail route.
        rule.waitForIdle()
        assertEquals(emptyList<String>(), mail.asked)
        assertEquals(listOf(ItemId), repository.contexts.distinct())
    }

    @Test fun storedOnlyWholeTextKeepsThePlainLineWithoutAClippingClaim() {
        val repository = inbox(Stored, clipping = StoredMailClipping.Whole)
        show(MailAccess.StoredOnly) { MailScreen(repository, RecordingMail(), MailRoute(ItemId), navigator) }
        waitFor(offLine)
        assertFalse(shown(clippedLine))
    }

    @Test fun storedOnlyUnknownClippingKeepsTodaysPlainLine() {
        val repository = inbox(Stored, clipping = StoredMailClipping.Unknown)
        show(MailAccess.StoredOnly) { MailScreen(repository, RecordingMail(), MailRoute(ItemId), navigator) }
        waitFor(offLine)
        assertFalse(shown(clippedLine))
    }

    @Test fun storedOnlyAnAiDraftForTheMailIsNotOfferedForSending() {
        val repository = inbox(Stored, record = mailRecord(agent = InboxAgent("drafted", draft = "Thanks Alex, Thursday works.", basis = "Alex asked.")))
        show(MailAccess.StoredOnly) { MailScreen(repository, RecordingMail(), MailRoute(ItemId), navigator) }
        waitFor(offLine)
        assertFalse(shown("Thanks Alex, Thursday works.", substring = true))
    }

    @Test fun storedOnlyALinkToAMailThatIsGoneSaysItIsMissing() {
        val mail = RecordingMail()
        val repository = inbox(Stored, record = null, mail = mail)
        show(MailAccess.StoredOnly) { MailScreen(repository, mail, MailRoute(ItemId), navigator) }
        waitFor(text(R.string.inbox_item_missing))
        assertFalse(shown(text(R.string.inbox_mail_load_failed)))
        assertEquals(emptyList<String>(), mail.asked)
    }

    @Test fun liveTheMailPageAndItsRequestsAreTodays() {
        val mail = RecordingMail()
        val repository = inbox(Stored, mail = mail)
        show(MailAccess.Live) { MailScreen(repository, mail, MailRoute(ItemId), navigator) }
        waitFor("The live message goes on", substring = true)
        for (kept in listOf(R.string.inbox_mail_reply, R.string.inbox_mail_reply_all, R.string.inbox_mail_forward, R.string.inbox_mail_attachments))
            assertTrue(text(kept), shown(text(kept)))
        assertFalse(shown(offLine))
        rule.waitForIdle()
        // The item, its newest message and the accounts, as before; the stored text is not asked for.
        assertEquals(setOf("item", "message", "accounts"), mail.asked.toSet())
        assertEquals(emptyList<String>(), repository.contexts)
    }

    @Test fun composingANewMailIsOfferedOnlyWhileMailIsLive() {
        var access by mutableStateOf(MailAccess.Live)
        val repository = inbox(Stored, mail = RecordingMail())
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalMailAccess provides access) { Box(Modifier.width(400.dp).height(1500.dp)) { InboxScreen(repository, navigator) } }
            }
        }
        val compose = text(R.string.inbox_compose_mail)
        rule.waitUntil(5_000) { rule.onAllNodesWithContentDescription(compose).fetchSemanticsNodes().isNotEmpty() }
        access = MailAccess.StoredOnly
        rule.waitUntil(5_000) { rule.onAllNodesWithContentDescription(compose).fetchSemanticsNodes().isEmpty() }
        // The mail itself stays listed.
        assertTrue(shown("Q4 planning"))
        access = MailAccess.Live
        rule.waitUntil(5_000) { rule.onAllNodesWithContentDescription(compose).fetchSemanticsNodes().isNotEmpty() }
    }
}

/**
 * The mail page drawn on the JVM with Robolectric's native graphics (no emulator), in every style, light and dark, in
 * each language: live (as today) and stored-only. PNGs go to `build/mail-module-shots/` to look at; none is committed.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MailModuleOffShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/mail-module-shots").also { it.mkdirs() }

    private fun shots(language: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var access by mutableStateOf(MailAccess.Live)
        var fontScale by mutableStateOf(1f)
        // Unknown keeps the matrix's existing plain notice; the clipping loop below drives the three states.
        var clipping by mutableStateOf(StoredMailClipping.Unknown)
        val mail = RecordingMail()
        val base = FakeInboxRepository()
        base.snapshot.value = InboxSnapshot(records = listOf(mailRecord()))
        val repository = StoredInbox(base, Stored, mail)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val density = androidx.compose.ui.platform.LocalDensity.current
                CompositionLocalProvider(LocalMailAccess provides access,
                    androidx.compose.ui.platform.LocalDensity provides androidx.compose.ui.unit.Density(density.density, fontScale)) {
                    key(style, dark, access, fontScale, clipping) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1100.dp).background(PersonaTheme.palette.background)) {
                            MailScreen(repository, mail, MailRoute(ItemId), navigator)
                        }
                    }
                }
            }
        }
        val resources = ApplicationProvider.getApplicationContext<Context>()
        val offLine = resources.getString(R.string.inbox_mail_module_off)
        val clippedLine = resources.getString(R.string.inbox_mail_module_off_clipped)
        fun readyLine(): String = when {
            access == MailAccess.Live -> "The live message goes on"
            clipping == StoredMailClipping.Clipped -> clippedLine
            else -> offLine
        }
        fun capture(name: String) {
            rule.waitUntil(5_000) {
                rule.onAllNodesWithText(readyLine(), substring = true).fetchSemanticsNodes().isNotEmpty()
            }
            rule.mainClock.advanceTimeBy(1_000)
            rule.waitForIdle()
            File(out(), name).outputStream().use { rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (a in MailAccess.entries) {
            style = s; dark = d; access = a
            capture("mail-${if (a == MailAccess.Live) "on" else "off"}-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png")
        }
        // The stored page at the largest text size: the line wraps and nothing is cut off.
        style = AppStyle.Glass; dark = false; access = MailAccess.StoredOnly; fontScale = 2f
        capture("mail-off-$language-glass-light-font2.png")
        // The three clipping notices (#15995), Glass in light and dark for this language: clipped says the text is
        // cut short, whole and unknown keep today's plain sentence.
        fontScale = 1f
        for (c in StoredMailClipping.entries) for (d in listOf(false, true)) {
            repository.clipping = c
            clipping = c
            dark = d
            capture("mail-off-${c.name.lowercase()}-$language-glass-${if (d) "dark" else "light"}.png")
        }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1100dp-xhdpi") @Test fun english() = shots("en")
    @Config(sdk = [35], qualifiers = "zh-w400dp-h1100dp-xhdpi") @Test fun chinese() = shots("zh")
    @Config(sdk = [35], qualifiers = "ja-w400dp-h1100dp-xhdpi") @Test fun japanese() = shots("ja")
}
