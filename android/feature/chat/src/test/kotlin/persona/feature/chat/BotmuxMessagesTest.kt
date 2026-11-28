package persona.feature.chat

import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import java.io.File
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Botmux
import persona.core.contracts.Fold as WireFold
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.chatLines

/**
 * A BotMux session's chat (docs/protocol/protocol.md "BotMux messages", web `BotmuxMessages.tsx`): decoded, a Lark message
 * with its tags and its original on request, the replies sent to Lark as Markdown, a card reply without words said as
 * such, BotMux's "nothing to send" left out; raw, the transcript as the agent got it. Synthetic data only.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
class BotmuxMessagesTest {
    @get:Rule val rule = createComposeRule()
    private val context: Context = ApplicationProvider.getApplicationContext()
    private val store get() = BotmuxModeStore.of(context)
    private val key = SessionKey("computer", "topic")
    private val botmuxSession = Session(machineId = "computer", sessionId = "topic", paneId = "botmux:topic")
    private val wrapped = "<botmux_reminder>Reply with botmux send at least once.</botmux_reminder>\n\n<user_message>\n" +
        "[User quoted a message — run `botmux quoted om_example_050` to view it]\nWhat do these two screenshots show?\n" +
        "</user_message>\n\n<sender type=\"user\" name=\"Sam Lee\" />"
    private val markdown = "## Findings\n**Two** screens differ:\nline one\nline two\n\n| Screen | State |\n| --- | --- |\n" +
        "| Home | ok |\n| Cart | broken |\n\n```kotlin\nval fixed = true\n```\n\n[Details](https://example.com/report)"

    private fun item(seq: Int, kind: String, role: String = "assistant", text: String? = null, id: String? = null,
        botmux: Botmux? = null, error: Boolean? = null, fold: WireFold? = null) =
        LineItem(Item("$seq#0", seq, role = role, kind = kind, text = text, toolUseId = id, toolName = "Bash".takeIf { kind == "tool_use" },
            isError = error, botmux = botmux, fold = fold))

    private val transcript = listOf(
        item(1, "text", "user", wrapped, botmux = Botmux("message", "What do these two screenshots show?", "Sam Lee", "user",
            quoted = true, attachments = 2, mentions = listOf("Review Bot"))),
        item(2, "tool_use", id = "a", text = "botmux send --content-file /tmp/r.md", botmux = Botmux("reply", markdown)),
        item(3, "tool_result", "user", "sent", id = "a"),
        item(4, "fold", fold = WireFold(items = 2, tools = 1, throughSeq = 5, throughId = "5#0",
            botmux = listOf(Botmux("reply", "", final = true)))),
        item(6, "text", text = "BOTMUX_NOTHING_TO_SEND"),
    )

    @Before fun decode() = store.set(BotmuxMode.Decode)
    @After fun reset() = store.set(BotmuxMode.Decode)

    private fun show(session: Session = botmuxSession, lines: List<ChatLine> = chatLines(transcript).lines, style: () -> AppStyle = { AppStyle.Glass },
        dark: () -> Boolean = { false }) {
        rule.setContent {
            PersonaTheme(darkTheme = dark(), reduceMotion = true, style = style()) {
                val loader = remember { ImageLoader.Builder(context).build() }
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    lines.forEach { line ->
                        ChatLineContent(line, key, loader, recordedDuration = null, onToggle = {}, onFold = {}, folding = emptySet(),
                            foldErrors = emptyMap(), onFullText = {}, agentSession = session)
                    }
                }
            }
        }
    }

    private fun shown(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    private fun waitFor(text: String) = rule.waitUntil(10_000) { shown(text) }

    @Test fun tagsInReadingOrder() {
        val res = context.resources
        assertEquals(listOf("Sam Lee", "Quoted a message", "Topic had earlier messages", "2 attachments", "@Review Bot", "Voice", "Cut short"),
            botmuxTags(res, Botmux("message", "x", "Sam Lee", "user", quoted = true, topicContext = true, attachments = 2,
                mentions = listOf("Review Bot", " "), voice = true, truncated = true)))
        assertEquals(listOf("Final reply in Lark", "Docs Bot (bot)", "1 attachment"),
            botmuxTags(res, Botmux("reply", "x", "Docs Bot", "bot", final = true, attachments = 1)))
        assertEquals(listOf("Sent from Persona"), botmuxTags(res, Botmux("trigger", "x", source = "Persona")))
        assertEquals(listOf("Triggered"), botmuxTags(res, Botmux("trigger", "x")))
        assertEquals(listOf("Sent to Lark"), botmuxTags(res, Botmux("reply", "x")))
    }

    @Test fun modeIsStoredAndDecodesByDefault() {
        assertEquals(BotmuxMode.Decode, BotmuxModeStore.modeOf(null))
        assertEquals(BotmuxMode.Decode, BotmuxModeStore.modeOf("unexpected"))
        assertEquals(BotmuxMode.Raw, BotmuxModeStore.modeOf("all"))
        store.set(BotmuxMode.Raw)
        assertEquals(BotmuxMode.Raw, store.mode.value)
        assertEquals("all", context.getSharedPreferences("persona.botmux", Context.MODE_PRIVATE).getString("mode", null))
    }

    /** Decoded: the Lark words in the bubble with their tags, the original on request, the replies as Markdown, no marker. */
    @Test fun decodedChat() {
        show()
        waitFor("broken")
        listOf("What do these two screenshots show?", "Sam Lee", "@Review Bot", "Quoted a message", "2 attachments",
            "Sent to Lark", "Final reply in Lark", "Findings", "Cart", "val fixed = true", "Details",
            "Replied with a Lark card. Its words weren't recorded.").forEach { assertTrue("missing 「$it」", shown(it)) }
        // a single line break in Lark is a break here too (the reply is drawn with keepLineBreaks)
        assertEquals("a  \nb\n\nc", keepLineBreaks("a\nb\n\nc"))
        assertTrue("the wrapper shows while decoding", !shown("botmux_reminder"))
        assertTrue("the marker shows while decoding", !shown("BOTMUX_NOTHING_TO_SEND"))
        rule.onNodeWithText("Original").performClick()
        rule.waitUntil(5_000) { shown("botmux_reminder") }
        rule.onNodeWithTag("botmux-original-text-1#0").assertExists()
        rule.onNodeWithText("Hide original").performClick()
        rule.waitUntil(5_000) { !shown("botmux_reminder") }
    }

    /** Raw: the transcript's words in the bubble, no tags, no reply line (the calls stay in the work), the marker kept. */
    @Test fun rawChatFollowsTheSwitch() {
        show()
        waitFor("Sent to Lark")
        store.set(BotmuxMode.Raw)
        rule.waitUntil(5_000) { shown("BOTMUX_NOTHING_TO_SEND") }
        assertTrue("raw shows the transcript's words", shown("botmux_reminder"))
        listOf("Sent to Lark", "@Review Bot", "Original", "Findings").forEach { assertTrue("「$it」 shows while raw", !shown(it)) }
        store.set(BotmuxMode.Decode)
        rule.waitUntil(5_000) { shown("Sent to Lark") && !shown("BOTMUX_NOTHING_TO_SEND") }
    }

    /** Only a BotMux session leaves the marker out: another agent's answer is its words. */
    @Test fun otherSessionsKeepTheMarker() {
        show(session = Session(machineId = "computer", sessionId = "plain"),
            lines = chatLines(listOf(item(1, "text", "user", "hi"), item(2, "text", text = "BOTMUX_NOTHING_TO_SEND"))).lines)
        waitFor("BOTMUX_NOTHING_TO_SEND")
    }

    /** An attachment-only reply (`text=""`, `attachments > 0`) keeps its tags alone without the Lark-card note. */
    @Test fun attachmentOnlyReplyOmitsCardNote() {
        val mediaOnly = listOf(
            item(1, "text", "user", wrapped, botmux = Botmux("message", "Send the screenshot", "Sam Lee", "user")),
            item(2, "tool_use", id = "a", text = "botmux send --image shot.png", botmux = Botmux("reply", "", attachments = 1)),
            item(3, "tool_result", "user", "sent", id = "a"),
        )
        show(lines = chatLines(mediaOnly).lines)
        waitFor("1 attachment")
        assertTrue("missing reply header tag", shown("Sent to Lark"))
        assertTrue("attachment-only reply should not show card note", !shown("Replied with a Lark card"))
    }

    /** A blank reply with no attachments (`text=""`, `attachments == 0`) shows the Lark-card placeholder note. */
    @Test fun blankReplyWithoutAttachmentsShowsCardNote() {
        val cardOnly = listOf(
            item(1, "text", "user", wrapped, botmux = Botmux("message", "Send the card", "Sam Lee", "user")),
            item(2, "tool_use", id = "a", text = "botmux send --response-kind final", botmux = Botmux("reply", "", final = true, attachments = 0)),
            item(3, "tool_result", "user", "sent", id = "a"),
        )
        show(lines = chatLines(cardOnly).lines)
        waitFor("Replied with a Lark card. Its words weren't recorded.")
        assertTrue("missing final reply header tag", shown("Final reply in Lark"))
    }

    @Test fun sessionInfoSwitch() {
        rule.setContent { PersonaTheme(reduceMotion = true) { BotmuxModeRow() } }
        rule.onNodeWithText("Show as in Lark").performClick()
        rule.waitUntil(5_000) { store.mode.value == BotmuxMode.Raw }
        rule.onNodeWithText("Show as in Lark").performClick()
        rule.waitUntil(5_000) { store.mode.value == BotmuxMode.Decode }
    }

    /** Review captures: decoded in every style, Glass dark, and raw (`build/i18n-shots/chat-botmux-*.png`). */
    @Test fun shots() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        show(style = { style }, dark = { dark })
        val out = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }
        fun capture(name: String) {
            rule.waitForIdle()
            rule.waitUntil(10_000) { shown("broken") || store.mode.value == BotmuxMode.Raw }
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "chat-botmux-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass; dark = false
        store.set(BotmuxMode.Raw)
        capture("raw-glass-light")
    }
}
