package persona.core.domain.answer

import persona.core.contracts.AgentState
import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.parseTime
import persona.core.domain.plainMarkdown
import persona.core.domain.sort.replyLine
import persona.core.domain.sort.workLine
import persona.core.contracts.toolDisplayName
import java.time.Instant

// A row shows one line of it; the rest is cut by the row anyway.
private const val AskMax = 160

/**
 * What a card asks, in one line for a row that waits on it (#3964; the web's `taskAsk` / `rowLine`): "Has a question: which framework?",
 * "Needs your permission: swift test", "Plan awaiting approval", "Terminal needs you: …", "MCP request: …"; the kind alone when the card carries no
 * words. Null for a card of a kind this app does not know with nothing to say.
 */
fun Interaction.askLine(): CoreText? {
    val kicker = when (kind) {
        "question" -> Key.AskQuestion
        "permission" -> Key.AskPermission
        "plan" -> Key.AskPlan
        "screen" -> Key.AskScreen
        "elicitation" -> Key.AskElicitation
        else -> null
    }
    val ask = askWords()
    return when {
        kicker == null -> ask
        ask == null -> CoreText.of(kicker)
        else -> CoreText.of(Key.AskLine, CoreText.of(kicker), ask)
    }
}

/**
 * What a card asks in its own words, without the kind [askLine] puts first: the question, the command, the dialog's
 * question, the form's message, a plan's summary. A folded card shows it after its title (iOS `InteractionPanel.previewText`).
 */
fun Interaction.askWords(): CoreText? {
    fun words(text: String?) = text?.let(::plainMarkdown)?.trim()?.takeIf(String::isNotEmpty)?.take(AskMax)?.let(CoreText::plain)
    return when (kind) {
        "question" -> words(questions?.firstOrNull()?.question?.takeIf(String::isNotBlank) ?: summary)
        // A command keeps its backticks and stars: only its lines are folded into one. With nothing to go on, the tool's name.
        "permission" -> if (summary.isNullOrBlank() && toolInput == null) toolDisplayName(toolName)?.let(CoreText::plain)
        else permissionSummary().text.let { text ->
            if (text.key != Key.Verbatim) text
            else text.args.first().toString().replace(Whitespace, " ").trim().takeIf(String::isNotEmpty)?.take(AskMax)?.let(CoreText::plain)
        }
        // as the web's: the kind alone, the plan's own first line is often "## Context" or a table row
        "plan" -> words(summary)
        "screen" -> words(dialog?.question?.takeIf(String::isNotBlank) ?: summary)
        "elicitation" -> words(elicitation?.message?.takeIf(String::isNotBlank) ?: summary)
        else -> words(summary)
    }
}

private val Whitespace = Regex("\\s+")

/** Each session's card that came first (then by id), the one its row speaks of; the chat lists the rest after it. */
fun List<Interaction>.firstBySession(): Map<SessionKey, Interaction> = groupBy { it.sessionKey }.mapValues { (_, cards) ->
    cards.minWith(EarliestFirst)
} + groupBy { it.homeKey }.mapValues { (home, cards) ->
    cards.filter { it.sessionKey == home }.minWithOrNull(EarliestFirst) ?: cards.minWith(EarliestFirst)
}

private val EarliestFirst = compareBy<Interaction> { parseTime(it.createdAt) ?: Instant.MAX }.thenBy { it.id }

/**
 * The card a task waits on (the web's `taskInteraction`): its [lead] agent's first, else the one that came first among
 * its other active agents' (then by id).
 */
fun Todo.waitingCard(lead: Session?, cards: Map<SessionKey, Interaction>): Interaction? =
    lead?.let { cards[it.key] } ?: activeAgents.mapNotNull { cards[it.key] }.minWithOrNull(EarliestFirst)

/** What a session's row says it waits for (#3964): its own [card]'s [askLine] while it needs you; null otherwise. */
fun Session.askingLine(card: Interaction?): CoreText? =
    card?.takeIf { foldedState == AgentState.NeedsYou && it.homeKey == key }?.askLine()

/**
 * The line under an agent row's title: what its card asks while it waits for you ([askingLine]), else why its host says
 * it waits or stopped (iOS listPreview), what it asks the user once seen and still unanswered ([replyLine], #5096; an
 * unread or muted one keeps its last words, as on iOS and the web), where
 * its work stands once the hub triaged its turn (docs/assistant/agent-work.md), else its last words.
 */
fun Session.rowLine(card: Interaction?, now: Instant, muted: Boolean = false): CoreText? = askingLine(card)
    ?: CoreText.of(Key.CodexWaiting).takeIf { awaitingFirstMessage && agentState == AgentState.Idle }
    ?: reasonPreview?.let(CoreText::plain)
    ?: replyLine?.takeIf { awaitsReply && agentState == AgentState.Idle && !muted }?.let(CoreText::plain)
    ?: workLine(now) ?: lastText?.takeIf(String::isNotBlank)?.let(CoreText::plain)
