package persona.core.domain.lines

import persona.core.contracts.Botmux
import persona.core.contracts.Item

/** One reply an exchange sent to Lark: [id] is its call's id, or `<fold id>:<n>` for one its folded work holds. */
data class BotmuxReply(val id: String, val reply: Botmux)

/**
 * The replies an exchange sent to Lark, in order (web `botmuxReplies`, docs/protocol/protocol.md "BotMux messages"): its
 * completed, successful `botmux send` calls, and those its folded work lists in `fold.botmux`.
 */
fun botmuxReplies(steps: List<WorkItem>): List<BotmuxReply> = buildList {
    for (step in steps) when (step) {
        is WorkItem.Tool -> {
            val reply = step.call.use.item.botmux?.takeIf { it.isReply } ?: continue
            val result = step.call.result ?: continue
            if (result.item.isError != true) add(BotmuxReply(step.call.id, reply))
        }
        is WorkItem.Folded -> step.item.item.fold?.botmux?.forEachIndexed { i, reply ->
            if (reply.isReply) add(BotmuxReply("${step.item.id}:$i", reply))
        }
        else -> Unit
    }
}

/** What BotMux asks the agent to answer with when a message was not meant for it: nothing reaches Lark. */
const val BOTMUX_NOTHING_TO_SEND = "BOTMUX_NOTHING_TO_SEND"

/** An assistant text that is only [BOTMUX_NOTHING_TO_SEND]: a decoded BotMux chat leaves it out. */
fun isBotmuxNothingToSend(item: Item): Boolean =
    item.role == "assistant" && item.kind == "text" && item.text?.trim() == BOTMUX_NOTHING_TO_SEND
