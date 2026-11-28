package persona.feature.chat

import android.content.Context
import android.content.SharedPreferences
import android.content.res.Resources
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.Botmux
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.designsystem.AssistantAnswerFrame
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JSwitchRow
import persona.core.designsystem.JTag
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.isBotmuxNothingToSend
import persona.core.domain.sort.isBotmux

// BotMux sessions as in Lark (docs/protocol/protocol.md "BotMux messages", web `views/chat/BotmuxMessages.tsx`): a message
// as written there with its facts as tags and its original on request, and what the agent sent to Lark as its message.
// Items without `botmux` (other sessions, older nodes, text the node could not read) are drawn as before.

/** How a BotMux chat reads (web `BotmuxMode`): as in Lark, or the transcript as the agent got it. */
internal enum class BotmuxMode(val stored: String) { Decode("decode"), Raw("all") }

/**
 * The BotMux mode, kept on this phone for every chat (the web keeps `persona.agentBotmuxMode` in the browser): decoded
 * until it is switched. One per process, so an open chat follows the switch at once.
 */
internal class BotmuxModeStore private constructor(private val prefs: SharedPreferences) {
    private val state = MutableStateFlow(modeOf(prefs.getString(KEY, null)))
    val mode: StateFlow<BotmuxMode> = state.asStateFlow()

    fun set(mode: BotmuxMode) {
        if (state.value == mode) return
        prefs.edit().putString(KEY, mode.stored).apply()
        state.value = mode
    }

    companion object {
        private const val NAME = "persona.botmux"
        private const val KEY = "mode"
        @Volatile private var sharedApp: Context? = null
        @Volatile private var shared: BotmuxModeStore? = null

        fun of(context: Context): BotmuxModeStore {
            val app = context.applicationContext
            shared?.takeIf { sharedApp === app }?.let { return it }
            return synchronized(this) {
                shared?.takeIf { sharedApp === app }
                    ?: BotmuxModeStore(app.getSharedPreferences(NAME, Context.MODE_PRIVATE)).also {
                        sharedApp = app
                        shared = it
                    }
            }
        }

        /** What was stored, read back: anything but the raw mode (nothing yet, an unknown value) decodes. */
        fun modeOf(stored: String?): BotmuxMode = if (stored == BotmuxMode.Raw.stored) BotmuxMode.Raw else BotmuxMode.Decode
    }
}

@Composable
internal fun rememberBotmuxMode(): BotmuxMode {
    val context = LocalContext.current
    val store = remember(context) { BotmuxModeStore.of(context) }
    val mode by store.mode.collectAsStateWithLifecycle()
    return mode
}

/** The tags under a message or a reply, in reading order (web `botmuxTags`). */
internal fun botmuxTags(res: Resources, b: Botmux): List<String> = buildList {
    if (b.kind == "trigger") add(b.source?.takeIf(String::isNotBlank)?.let { res.getString(R.string.chat_botmux_via, it) }
        ?: res.getString(R.string.chat_botmux_triggered))
    if (b.isReply) add(res.getString(if (b.final) R.string.chat_botmux_sent_final else R.string.chat_botmux_sent))
    b.sender?.takeIf(String::isNotBlank)?.let { add(if (b.senderType == "bot") res.getString(R.string.chat_botmux_bot, it) else it) }
    if (b.quoted) add(res.getString(R.string.chat_botmux_quoted))
    if (b.topicContext) add(res.getString(R.string.chat_botmux_topic))
    if (b.attachments > 0) add(res.getQuantityString(R.plurals.chat_botmux_attachments, b.attachments, b.attachments))
    b.mentions.filter(String::isNotBlank).forEach { add("@$it") }
    if (b.voice) add(res.getString(R.string.chat_botmux_voice))
    if (b.truncated) add(res.getString(R.string.chat_botmux_truncated))
}

/** The message as written in Lark (web `decodedUser`): its words in place of the transcript's. */
internal fun decodedBotmuxItem(item: Item): Item? = item.botmux?.let { item.copy(text = it.text, fullSize = null) }

/** A decoded BotMux chat leaves out an answer that is only BotMux's "nothing to send"; the raw one keeps it. */
@Composable
internal fun botmuxSentinelHidden(item: Item, session: Session?): Boolean =
    session?.isBotmux == true && isBotmuxNothingToSend(item) && rememberBotmuxMode() == BotmuxMode.Decode

@Composable
private fun BotmuxTags(b: Botmux, end: Boolean, modifier: Modifier = Modifier) {
    val tags = botmuxTags(LocalResources.current, b)
    if (tags.isEmpty()) return
    FlowRow(modifier.fillMaxWidth().testTag("botmux-tags"),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs, if (end) Alignment.End else Alignment.Start),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        tags.forEach { JTag(it) }
    }
}

/**
 * A message BotMux handed the agent: decoded, its tags, its words in [bubble] (the chat's user bubble, with its pictures:
 * [hasImages]) and the original on request; raw, the bubble with the transcript's words, as any other chat.
 */
@Composable
internal fun BotmuxUserMessage(item: Item, hasImages: Boolean, bubble: @Composable (Item) -> Unit) {
    val decoded = decodedBotmuxItem(item)
    if (decoded == null || rememberBotmuxMode() == BotmuxMode.Raw) return bubble(item)
    Column(Modifier.fillMaxWidth().testTag("botmux-message-${item.id}"), horizontalAlignment = Alignment.End,
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        BotmuxTags(item.botmux!!, end = true)
        if (decoded.text.orEmpty().isNotBlank() || hasImages) bubble(decoded)
        BotmuxOriginal(item)
    }
}

/** "Original": the transcript's words, as the agent read them, folded until asked for. */
@Composable
private fun BotmuxOriginal(item: Item) {
    if (item.text.isNullOrBlank()) return
    var open by rememberSaveable(item.id) { mutableStateOf(false) }
    var whole by rememberSaveable(item.id) { mutableStateOf<String?>(null) }
    JTextButton(stringResource(if (open) R.string.chat_botmux_hide_original else R.string.chat_botmux_original),
        { open = !open }, modifier = Modifier.testTag("botmux-original-${item.id}"))
    if (!open) return
    val palette = PersonaTheme.palette
    Column(Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.control)).background(palette.sunken)
        .padding(Tokens.Space.s).testTag("botmux-original-text-${item.id}")) {
        Text(whole ?: item.text.orEmpty().let { if (item.fullSize != null) cutText(it) else it },
            style = PersonaTheme.type.code, color = palette.ink2)
        if (item.fullSize != null && whole == null) FullTextButton(item) { whole = it }
    }
}

/**
 * What an exchange sent to Lark, each reply as the agent's message under its tags: Markdown as Lark shows it (a single
 * line break stays one). A reply with no words and no attachments (an older card reply) says so rather than vanishing;
 * an attachment-only reply keeps its tags alone. Nothing while the chat is raw: the calls are in the work above.
 */
@Composable
internal fun BotmuxRepliesLine(line: ChatLine.BotmuxReplies) {
    if (rememberBotmuxMode() == BotmuxMode.Raw) return
    Column(Modifier.fillMaxWidth().testTag("botmux-replies"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        line.replies.forEach { (id, reply) ->
            Column(Modifier.fillMaxWidth().testTag("botmux-reply-$id"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                BotmuxTags(reply, end = false)
                when {
                    reply.text.isNotBlank() -> AssistantAnswerFrame(boxed = true) {
                        ReplyMarkdown(keepLineBreaks(reply.text), "botmux/$id", ownReply = false, selectable = true)
                    }
                    reply.attachments == 0 -> ChatSystemLine(stringResource(R.string.chat_botmux_card_reply))
                }
            }
        }
    }
}

/** Session info's switch for a BotMux session: decoded as in Lark, or the raw transcript. Kept for every chat. */
@Composable
internal fun BotmuxModeRow(modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val store = remember(context) { BotmuxModeStore.of(context) }
    val mode by store.mode.collectAsStateWithLifecycle()
    JGroupItem(GroupPosition.of(0, 1), modifier) {
        JSwitchRow(stringResource(R.string.chat_botmux_decode), mode == BotmuxMode.Decode,
            { store.set(if (it) BotmuxMode.Decode else BotmuxMode.Raw) },
            Modifier.testTag("botmux-mode"), subtitle = stringResource(R.string.chat_botmux_decode_note))
    }
}
