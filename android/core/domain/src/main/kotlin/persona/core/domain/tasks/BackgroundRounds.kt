package persona.core.domain.tasks

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.MessageSource
import persona.core.domain.injectedOf
import persona.core.domain.isPersonaTool
import persona.core.domain.lines.ChatLine

/**
 * Folding the main conversation's background rounds (#4314, docs/assistant/assistant.md "Background rounds"): a round
 * that another conversation, a scheduled task or a follow-up started is drawn as one line the user can open. Display
 * only: every line stays in the transcript, under that line. [open] holds the fold lines the user opened (the chat's
 * open-line set); [reveal] is an item a search or a link lands on, whose round is drawn open. [rounds]: background rounds
 * fold (the main conversation with the hub's switch on); without it only supervision rounds fold, on Glass
 * ([AssistantConversation.project]'s `inline`, iOS `SupervisionRecordLine`).
 */
data class BackgroundFolding(val open: Set<String> = emptySet(), val reveal: String? = null, val rounds: Boolean = true) {
    companion object {
        /** The source a folded supervision round stands under: drawn "Handled in the background" (iOS `SupervisionRecordLine`). */
        val supervision = MessageSource("supervision")

        /** The message sources whose rounds fold (the web's `supervisionRound`, widened by #4314). */
        val kinds = setOf("conversation", "followup", "patrol")

        /**
         * The source of a round's first input when that round folds, else null. A follow-up folds only as a scheduled
         * one: never a usage-limit continuation or a wake-up (they carry on the user's own exchange), and never one
         * without a title (nothing to say which task ran).
         */
        fun sourceOf(item: Item): MessageSource? {
            val source = persona.core.domain.sourceOf(item)?.takeIf { it.kind in kinds } ?: return null
            if (source.kind != "followup") return source
            val injected = persona.core.domain.injectedOf(item)
            if (injected?.kind == "limit_continue" || injected?.state in setOf("limit_continue", "wake")) return null
            return source.takeIf { !it.title.isNullOrBlank() }
        }

        /** The id of the line that stands for the round [roundId] (its first input's id). */
        fun lineId(roundId: String): String = "bg-$roundId"

        /**
         * Whether conversation [sessionId] folds its background rounds (#4314, #6595): the main conversation or one of
         * the hub's leader (direction) conversations, while the switch is on. A hub that omits the switch means on;
         * an ordinary side conversation never folds.
         */
        fun foldsConversation(sessionId: String, main: String?, on: Boolean, leaders: Set<String>): Boolean {
            if (!on || sessionId.isBlank()) return false
            if (!main.isNullOrBlank() && sessionId == main) return true
            return sessionId in leaders
        }
    }
}

/**
 * One folded background round, drawn as one muted line, `@<source> · Reported from the background` (`· Working in the background…`
 * while [live]); while [open] its [lines] follow it unchanged.
 */
data class BackgroundRound(
    val id: String, val source: MessageSource, val live: Boolean, val open: Boolean, val lines: List<ChatLine>,
) {
    /** A quiet supervision round (Glass), drawn "Handled in the background" rather than with a source mention. */
    val supervision: Boolean get() = source.kind == BackgroundFolding.supervision.kind
}

/** Only Persona's visibility tools, not similarly named tools of another MCP server (web `supervisionTool`). */
fun supervisionTool(name: String?): String? {
    val bare = if (name != null && name.startsWith("mcp__") && isPersonaTool(name)) name.substringAfterLast("__") else name
    return bare?.takeIf { it in setOf("notify_user", "ask_user", "supervision_finish", "task_review") }
}

/**
 * Whether a round must stay shown (web `supervisionVisible`, #1988): it told or asked the user something (a successful
 * `ask_user` / `notify_user`, or an `ask_user` still waiting while the round runs), ended in a task review verdict card
 * (`accepted` / `needs_user`), finished its supervision with user visibility, the hub's fold says so, or the round's
 * injected supervision is marked `level="user"` (the same two marks the web and iOS read). Arguments alone never prove
 * success: a call counts by its result. [results] maps a tool call's id to its result.
 */
fun supervisionVisible(ordered: List<Item>, results: Map<String, Item>, live: Boolean): Boolean {
    if (ordered.any {
            it.fold?.supervisionUser == true ||
                injectedOf(it)?.let { injected -> injected.state == "supervise" && injected.level == "user" } == true
        }) return true
    for (use in ordered) {
        if (use.kind != "tool_use") continue
        val name = supervisionTool(use.toolName) ?: continue
        val result = use.toolUseId?.let(results::get)
        if (result == null) {
            // ask_user waits for a person before it has a result; never bury its pending answer card.
            if (name == "ask_user" && live) return true
            continue
        }
        if (result.isError == true) continue
        when (name) {
            "task_review" -> if (stringField(use.toolInput as? JsonObject, "verdict") in setOf("accepted", "needs_user")) return true
            "supervision_finish" -> {
                // A failed or malformed receipt cannot promote the round from its requested visibility.
                val receipt = runCatching { JsonCodec.parseToJsonElement(result.text.orEmpty()) as? JsonObject }.getOrNull()
                if (stringField(receipt, "visibility") == "user") return true
            }
            else -> return true
        }
    }
    return false
}

private fun stringField(obj: JsonObject?, key: String): String? =
    (obj?.get(key) as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull

/** A group of Needs you rows under the conversation that supervises them ([conversation]: its session id). */
data class NeedsYouGroup(val conversation: String, val entries: List<NeedsYouEntry>)

/**
 * Needs you grouped by supervising conversation (#4314): while folding is on and the task rows come from more than one
 * conversation (a to-do's `origin`), one group per conversation, [conversation]'s own first and the others in the order
 * their first row comes. A row that is not a task, and a task without an origin, is [conversation]'s. Rows keep their
 * order within a group, and the rows are the same ones: the count does not change. Otherwise one group, as before.
 */
fun needsYouGroups(needsYou: List<NeedsYouEntry>, conversation: String, folded: Boolean): List<NeedsYouGroup> {
    fun owner(entry: NeedsYouEntry): String =
        (entry as? NeedsYouEntry.Task)?.todo?.origin?.takeIf(String::isNotBlank) ?: conversation
    val origins = needsYou.filterIsInstance<NeedsYouEntry.Task>().map(::owner).distinct()
    if (!folded || origins.size < 2) return listOf(NeedsYouGroup(conversation, needsYou))
    val byOwner = needsYou.groupBy(::owner)
    val order = listOf(conversation) + byOwner.keys.filter { it != conversation }
    return order.map { NeedsYouGroup(it, byOwner[it].orEmpty()) }
}
