package persona.core.contracts

import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * What a BotMux-wrapped item says without the wrapper (server `proto.Botmux`, docs/protocol/protocol.md "BotMux messages"):
 * a Lark message BotMux handed the agent (`message`), a turn sent through BotMux's trigger (`trigger`, [source] names who),
 * or the agent's `botmux send` reply (`reply`, on its `tool_use` item). [text] is the words without BotMux's blocks; the
 * item's own `text` keeps the transcript's, for the original. Only display names leave the node, never a Lark id or email.
 * Set by the node; absent for anything else and from older nodes.
 */
@KeepGeneratedSerializer
@Serializable(with = BotmuxSerializer::class)
data class Botmux(
    @WireEnum("message", "trigger", "reply", "unknown") val kind: String = "unknown",
    val text: String = "",
    val sender: String? = null,
    @SerialName("sender_type") val senderType: String? = null,
    /** The message quoted another one; [topicContext]: the topic had messages before the agent joined it. */
    val quoted: Boolean = false,
    @SerialName("topic_context") val topicContext: Boolean = false,
    /** Posted in a topic, not to the chat itself. */
    val thread: Boolean = false,
    val attachments: Int = 0,
    @SerialName("attachment_meta") val attachmentMeta: List<BotmuxAttachment> = emptyList(),
    val mentions: List<String> = emptyList(),
    val source: String? = null,
    /** A reply sent with `--response-kind final`; [voice]: sent as a voice message. */
    val final: Boolean = false,
    val voice: Boolean = false,
    /** The words were clipped inside the wrapper: [text] is their start. */
    val truncated: Boolean = false,
) {
    val isReply get() = kind == "reply"
}

/** Safe attachment metadata from BotMux's structured turn record (never a local path or Lark id). */
@KeepGeneratedSerializer
@Serializable(with = BotmuxAttachmentSerializer::class)
data class BotmuxAttachment(
    val kind: String = "",
    val name: String? = null,
    @SerialName("mime_type") val mimeType: String? = null,
)

object BotmuxSerializer : WireSerializer<Botmux>(Botmux.generatedSerializer())
object BotmuxAttachmentSerializer : WireSerializer<BotmuxAttachment>(BotmuxAttachment.generatedSerializer())
