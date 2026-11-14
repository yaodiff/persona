package persona.feature.chat

import android.content.res.Resources
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import persona.core.contracts.Item
import persona.core.contracts.api.ChatActions
import persona.core.designsystem.JSheet
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ModelChanges
import persona.core.domain.lines.PriceBook
import persona.core.domain.lines.ReplyLabelKind
import persona.core.domain.lines.TurnCost
import persona.core.domain.lines.UsageCard
import persona.core.domain.lines.UsageDurationText
import persona.core.domain.lines.catalogIds
import persona.core.domain.lines.chatUsage
import persona.core.domain.lines.formatCost
import persona.core.domain.lines.replyLabel
import persona.core.domain.lines.replyUsageMillis
import persona.core.domain.lines.summarize
import persona.core.domain.lines.turnCost
import persona.core.domain.lines.usageCard

/** What one finished reply says under itself, and the lines a tap opens (#5470). */
internal data class ReplyUsageView(val label: String, val lines: List<String>, val actionLabel: String)

/** Empty unless the page has priced the transcript. Call sites other than the page draw no label. */
internal val LocalReplyUsage = staticCompositionLocalOf<Map<String, ReplyUsageView>> { emptyMap() }

/**
 * One price book for the process, the web's single map. A chat screen used to keep its own, so opening a chat or
 * rotating it asked the catalog again and the label arrived late. [catalog] is read when a batch is sent, so a later
 * load does not keep the client the first screen captured. Request and drain share [gate]: the book's map is not safe
 * for two screens to fill at once.
 */
internal object ReplyPrices {
    private val gate = Mutex()
    private var catalog: ChatActions? = null
    val book: PriceBook = PriceBook(ask = { ids ->
        catalog?.catalogPrices(ids) ?: error("Catalog unavailable")
    })

    suspend fun load(actions: ChatActions, ids: List<String>) {
        gate.withLock {
            catalog = actions
            book.request(ids)
            book.drain()
        }
    }
}

/**
 * The label under each finished reply, keyed by that reply's item id. Nothing for an exchange until the catalog has
 * answered every model it might price, so the label never flips from tokens to a cost. [hideAnswerId] is the live
 * turn's reply while it is still at work.
 */
@Composable
internal fun rememberReplyUsage(
    items: List<Item>,
    lines: List<ChatLine>,
    persona: Boolean,
    hideAnswerId: String?,
    catalog: ChatActions?,
): Map<String, ReplyUsageView> {
    val exchanges = remember(items) { chatUsage(items) }
    val ids = remember(exchanges) {
        exchanges.flatMap { ex -> summarize(ex.usage)?.let(::catalogIds).orEmpty() }.distinct()
    }
    var tick by remember { mutableIntStateOf(0) }
    LaunchedEffect(ids, catalog) {
        val actions = catalog ?: return@LaunchedEffect
        ReplyPrices.load(actions, ids)
        tick++
    }
    val durations = remember(lines) { replyUsageMillis(lines) }
    val res = LocalResources.current
    return remember(exchanges, durations, persona, hideAnswerId, tick, res) {
        replyUsageViews(res, exchanges, durations, persona, hideAnswerId, durationText(res), ReplyPrices.book)
    }
}

/** The clock, then the cost or the tokens. A tap opens the same lines the web's card shows. */
@Composable
internal fun ReplyUsageFooter(item: Item, view: ReplyUsageView) {
    var open by remember(item.id) { mutableStateOf(false) }
    val time = item.ts?.let { userBubbleTime(it, currentLocale) }
    Row(Modifier.fillMaxWidth().padding(top = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.sm),
        verticalAlignment = Alignment.CenterVertically) {
        if (time != null) Text(time, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
        Text(view.label,
            Modifier.clickable(role = Role.Button, onClickLabel = view.actionLabel) { open = true },
            style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
    if (open) JSheet(onDismiss = { open = false }, scrollable = true) { ReplyUsageDetails(view.lines) }
}

/** The card's lines, one under the other. They may wrap; the label above them does not. */
@Composable
internal fun ReplyUsageDetails(lines: List<String>) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        for (line in lines) Text(line, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
    }
}

private fun durationText(res: Resources) = UsageDurationText(
    hoursMinutes = { hours, minutes -> res.getString(R.string.chat_reply_dur_hours, hours.toInt(), minutes.toInt()) },
    daysHoursMinutes = { days, hours, minutes ->
        res.getString(R.string.chat_reply_dur_days, days.toInt(), hours.toInt(), minutes.toInt())
    },
)

private fun replyUsageViews(
    res: Resources,
    exchanges: List<persona.core.domain.lines.ExchangeUsage>,
    durations: Map<String, Long>,
    persona: Boolean,
    hideAnswerId: String?,
    text: UsageDurationText,
    book: PriceBook,
): Map<String, ReplyUsageView> {
    val out = LinkedHashMap<String, ReplyUsageView>()
    for (ex in exchanges) {
        val summary = summarize(ex.usage) ?: continue
        val answer = ex.answerId ?: continue
        if (answer == hideAnswerId) continue
        val modelIds = catalogIds(summary)
        // Still asking, or not asked yet: say nothing. A failure counts as answered, and then the tokens show.
        if (modelIds.isNotEmpty() && !book.ready(modelIds)) continue
        val cost = if (modelIds.isEmpty()) null else turnCost(summary.models) { book.priceOf(it) }
        val label = replyLabel(persona, summary, cost) ?: continue
        val priced = cost?.takeIf { it.total > 0 }
        val lines = if (persona && priced != null) personaUsageLines(res, priced)
            else replyUsageLines(res, usageCard(summary, cost, durations[answer], text))
        val action = res.getString(if (persona) R.string.chat_reply_persona_title else R.string.chat_reply_details)
        val words = when (label.kind) {
            ReplyLabelKind.Worth -> res.getString(R.string.chat_reply_worth, label.amount)
            ReplyLabelKind.Tokens -> res.getString(R.string.chat_reply_tokens, label.amount)
            ReplyLabelKind.Approx -> res.getString(R.string.chat_reply_approx, label.amount)
        }
        out[answer] = ReplyUsageView(words, lines, action)
    }
    return out
}

/** The Agent card, in the web's order, from this app's strings. */
internal fun replyUsageLines(res: Resources, card: UsageCard): List<String> = buildList {
    if (card.cost != null) {
        add(res.getString(R.string.chat_reply_details))
        add(res.getString(R.string.chat_reply_value, card.cost))
    }
    if (card.modelCosts.isNotEmpty()) {
        add(res.getString(R.string.chat_reply_by_model))
        for ((name, share) in card.modelCosts) add(res.getString(R.string.chat_reply_model_cost, name, share))
    }
    add(res.getString(R.string.chat_reply_token_line, card.total, card.input, card.output))
    add(if (card.rate != null) res.getString(R.string.chat_reply_cache, card.cacheRead, card.cacheWrite, card.rate)
        else res.getString(R.string.chat_reply_cache_no_rate, card.cacheRead, card.cacheWrite))
    if (card.duration != null) add(res.getString(R.string.chat_reply_time, card.duration))
    when {
        card.singleModel != null -> add(res.getString(R.string.chat_reply_model, card.singleModel))
        card.modelCount > 1 -> add(res.getString(R.string.chat_reply_model,
            res.getQuantityString(R.plurals.chat_reply_models, card.modelCount, card.modelCount)))
    }
    if (card.lowCache) add(res.getString(R.string.chat_reply_suggestion, res.getString(R.string.chat_reply_low_cache)))
    if (card.cost == null) add(res.getString(R.string.chat_reply_no_cost))
}

/** A Persona conversation: the cost in plain words, and each model's share when there were several. No tokens. */
internal fun personaUsageLines(res: Resources, cost: TurnCost): List<String> = buildList {
    add(res.getString(R.string.chat_reply_persona_cost, formatCost(cost.total)))
    if (cost.models.size >= 2) {
        add(res.getString(R.string.chat_reply_by_model))
        for (m in cost.models) add(res.getString(R.string.chat_reply_model_cost, ModelChanges.replyModelName(m.model), formatCost(m.cost)))
    }
}
