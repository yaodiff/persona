package persona.core.domain.lines

import persona.core.contracts.ModelPrice
import persona.core.contracts.TokenUsage
import java.math.BigDecimal
import java.math.RoundingMode
import kotlin.math.abs
import kotlin.math.round

/**
 * What an exchange's tokens are worth and what its reply says about them (#5470, the web's `lib/turn-cost.ts` from #5426).
 * A price is the hub's built-in catalog's reference price per million tokens. An exchange has a cost only when every
 * model that used tokens has a price for every kind it used.
 */

/** One model's tokens in an exchange. [input] excludes the cache. [fast] is Claude Code's fast speed, which has no price. */
data class ModelTokens(
    val model: String,
    val fast: Boolean = false,
    val input: Long = 0,
    val output: Long = 0,
    val cacheRead: Long = 0,
    val cacheWrite: Long = 0,
    val cacheWrite1h: Long = 0,
)

/** An exchange's tokens by model, in the order each model first used tokens, and whether it is the page's first round. */
data class UsageSummary(val models: List<ModelTokens>, val first: Boolean = false)

private class Counts(val model: String, val fast: Boolean) {
    var input = 0L
    var output = 0L
    var cacheRead = 0L
    var cacheWrite = 0L
    var cacheWrite1h = 0L

    fun add(u: TokenUsage, max: Boolean) {
        input = merge(input, u.input, max)
        output = merge(output, u.output, max)
        cacheRead = merge(cacheRead, u.cacheRead, max)
        cacheWrite = merge(cacheWrite, u.cacheWrite, max)
        cacheWrite1h = merge(cacheWrite1h, u.cacheWrite1h, max)
    }

    fun add(other: Counts) {
        input += other.input
        output += other.output
        cacheRead += other.cacheRead
        cacheWrite += other.cacheWrite
        cacheWrite1h += other.cacheWrite1h
    }

    fun toTokens() = ModelTokens(model, fast, input, output, cacheRead, cacheWrite, cacheWrite1h)
    fun used() = input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0 || cacheWrite1h > 0

    private fun merge(current: Long, value: Long, max: Boolean): Long {
        val n = if (value > 0) value else 0L
        return if (max) maxOf(current, n) else current + n
    }
}

/** What an exchange's entries add up to by model and speed. A report (an id) counts once per model and speed, at the
 *  largest count of each kind; the rest are summed. A report seen before the model's own requests keeps its place.
 *  Models with no tokens are left out. */
fun modelTokens(entries: List<TokenUsage>): List<ModelTokens> {
    val reports = LinkedHashMap<String, Counts>()
    val models = LinkedHashMap<String, Counts>()
    for (u in entries) {
        val model = if (u.model.isNullOrBlank()) "" else ModelChanges.bareModel(u.model!!)
        val key = "$model\n${if (u.fast) 1 else 0}"
        val row = models.getOrPut(key) { Counts(model, u.fast) }
        if (u.report) reports.getOrPut("${u.id}\n$key") { Counts(model, u.fast) }.add(u, max = true)
        else row.add(u, max = false)
    }
    for ((key, report) in reports) {
        models.getValue(key.substring(key.indexOf('\n') + 1)).add(report)
    }
    return models.values.filter { it.used() }.map { it.toTokens() }
}

/** The usage a reply carries, summed by model. Null when it used nothing. */
fun summarize(usage: TurnUsage?): UsageSummary? {
    val models = if (usage == null) emptyList() else modelTokens(usage.entries)
    return if (models.isEmpty()) null else UsageSummary(models, usage?.first == true)
}

/** All of an exchange's tokens. [total] is input, output, cache reads and writes. [hit] is the cache reads' share of
 *  all input, null without any input. */
data class UsageTotals(
    val input: Long, val output: Long, val cacheRead: Long, val cacheWrite: Long, val cacheWrite1h: Long,
    val total: Long, val hit: Double?,
)

fun usageTotals(models: List<ModelTokens>): UsageTotals {
    var input = 0L
    var output = 0L
    var cacheRead = 0L
    var cacheWrite = 0L
    var cacheWrite1h = 0L
    for (m in models) {
        input += m.input
        output += m.output
        cacheRead += m.cacheRead
        cacheWrite += m.cacheWrite
        cacheWrite1h += m.cacheWrite1h
    }
    val prompt = input + cacheRead + cacheWrite
    return UsageTotals(input, output, cacheRead, cacheWrite, cacheWrite1h, prompt + output,
        if (prompt > 0) cacheRead.toDouble() / prompt else null)
}

/** The usage a reply's props carry, summed by model. Null for anything else. */
fun parseUsage(key: String?): UsageSummary? {
    if (key.isNullOrBlank()) return null
    return try {
        summarize(decodeUsage(key))
    } catch (_: Exception) {
        null
    }
}

/** A model's cost in USD at [price] (per million tokens). A 1-hour cache write uses its own price, else the cache
 *  write's. Null when a kind it used has no price, or for the fast speed. */
fun modelCost(m: ModelTokens, price: ModelPrice?): Double? {
    if (price == null || m.fast) return null
    if ((m.input > 0 && price.input == null) || (m.output > 0 && price.output == null) ||
        (m.cacheRead > 0 && price.cacheRead == null) || (m.cacheWrite > 0 && price.cacheWrite == null)) return null
    val write1h = minOf(m.cacheWrite1h, m.cacheWrite)
    val write1hPrice = price.cacheWrite1h ?: price.cacheWrite ?: 0.0
    return (m.input * (price.input ?: 0.0) + m.output * (price.output ?: 0.0) + m.cacheRead * (price.cacheRead ?: 0.0) +
        (m.cacheWrite - write1h) * (price.cacheWrite ?: 0.0) + write1h * write1hPrice) / 1e6
}

data class ModelShare(val model: String, var cost: Double)

data class TurnCost(val total: Double, val models: List<ModelShare>)

/** The exchange's cost, only when every model that used tokens has one. Models without a cost of their own are left
 *  out of [TurnCost.models]; a partial sum is never returned as the whole. */
fun turnCost(models: List<ModelTokens>, price: (String) -> ModelPrice?): TurnCost? {
    val out = mutableListOf<ModelShare>()
    for (m in models) {
        val cost = if (m.model.isEmpty()) null else modelCost(m, price(m.model))
        if (cost == null) return null
        val same = out.find { it.model == m.model }
        if (same != null) same.cost += cost else out += ModelShare(m.model, cost)
    }
    return TurnCost(out.sumOf { it.cost }, out.filter { it.cost > 0 })
}

/** Two decimals, "<$0.01" under a cent, "$0.00" for zero. Rounds the exact binary value, as the web's `toFixed` does. */
fun formatCost(usd: Double): String {
    if (usd == 0.0) return "$0.00"
    if (usd < 0.01) return "<$0.01"
    return "$" + BigDecimal(usd).setScale(2, RoundingMode.HALF_UP).toPlainString()
}

/** A whole percent when it is close to whole, else one decimal. The decimal rounds the exact binary value. */
fun formatPercent(v: Double?): String? {
    if (v == null || !v.isFinite()) return null
    val pct = (v * 100).coerceIn(0.0, 100.0)
    val whole = round(pct)
    if (abs(pct - whole) < 0.05) return "${whole.toLong()}%"
    return BigDecimal(pct).setScale(1, RoundingMode.HALF_UP).toPlainString().removeSuffix(".0") + "%"
}

private const val lowCacheMinInput = 50_000L
private const val lowCacheMaxHit = 0.2

/** A lot of input that mostly missed the cache. Never for a first round, which has nothing cached. */
fun lowCache(usage: UsageSummary): Boolean {
    if (usage.first) return false
    val s = usageTotals(usage.models)
    return s.input + s.cacheRead + s.cacheWrite >= lowCacheMinInput && s.hit != null && s.hit < lowCacheMaxHit
}

/**
 * Hour-and-above duration words. Under an hour every language uses "6s", "1m 5s" or "2m".
 * The translated forms come from the app's strings; English is the default the tests use.
 */
class UsageDurationText(
    val hoursMinutes: (Long, Long) -> String = { hours, minutes -> "${hours}h ${minutes}m" },
    val daysHoursMinutes: (Long, Long, Long) -> String = { days, hours, minutes -> "${days}d ${hours}h ${minutes}m" },
) {
    companion object {
        val English = UsageDurationText()
    }
}

/** How a duration reads on the card, the web's `workDuration`: under an hour the same in every language. */
fun usageDuration(ms: Long, text: UsageDurationText = UsageDurationText.English): String {
    val total = maxOf(1L, round((if (ms.isFinite()) ms else 0L).toDouble() / 1000.0).toLong())
    if (total < 60) return "${total}s"
    if (total >= 3600) {
        val days = total / 86_400
        val hours = (total % 86_400) / 3600
        val minutes = (total % 3600) / 60
        return if (days > 0) text.daysHoursMinutes(days, hours, minutes) else text.hoursMinutes(hours, minutes)
    }
    val minutes = total / 60
    val seconds = total % 60
    return if (seconds != 0L) "${minutes}m ${seconds}s" else "${minutes}m"
}

private fun Long.isFinite() = true

private fun modelNames(models: List<ModelTokens>): List<String> {
    val names = LinkedHashSet<String>()
    for (m in models) if (m.model.isNotEmpty()) names += ModelChanges.replyModelName(m.model)
    return names.toList()
}

/**
 * The numbers the card's lines are filled with. [cost] is set only when the exchange has a price and it is above zero.
 * [modelCosts] is each priced model's share when there are two or more. [singleModel] is the one name to show when
 * there is no breakdown; [modelCount] is how many names there are then.
 */
data class UsageCard(
    val cost: String?,
    val modelCosts: List<Pair<String, String>>,
    val total: String,
    val input: String,
    val output: String,
    val cacheRead: String,
    val cacheWrite: String,
    val rate: String?,
    val duration: String?,
    val singleModel: String?,
    val modelCount: Int,
    val lowCache: Boolean,
)

fun usageCard(usage: UsageSummary, cost: TurnCost?, ms: Long? = null, text: UsageDurationText = UsageDurationText.English): UsageCard {
    val totals = usageTotals(usage.models)
    val priced = cost?.takeIf { it.total > 0 }
    val breakdown = priced != null && priced.models.size >= 2
    val names = modelNames(usage.models)
    return UsageCard(
        cost = priced?.let { formatCost(it.total) },
        modelCosts = if (breakdown && priced != null) priced.models.map { ModelChanges.replyModelName(it.model) to formatCost(it.cost) } else emptyList(),
        total = Subagents.tokens(totals.total),
        input = Subagents.tokens(totals.input),
        output = Subagents.tokens(totals.output),
        cacheRead = Subagents.tokens(totals.cacheRead),
        cacheWrite = Subagents.tokens(totals.cacheWrite),
        rate = formatPercent(totals.hit),
        duration = if (ms != null && ms > 0) usageDuration(ms, text) else null,
        singleModel = if (!breakdown && names.size == 1) names[0] else null,
        modelCount = if (!breakdown) names.size else 0,
        lowCache = lowCache(usage),
    )
}

/** The card's lines in English, in the web's order. The apps draw the same lines from their own strings. */
fun usageLinesEn(usage: UsageSummary, cost: TurnCost?, ms: Long? = null): List<String> {
    val card = usageCard(usage, cost, ms)
    return buildList {
        if (card.cost != null) {
            add("Request details")
            add("Token value this turn: ${card.cost}")
        }
        if (card.modelCosts.isNotEmpty()) {
            add("By model:")
            for ((name, share) in card.modelCosts) add("· $name $share")
        }
        add("Tokens: ${card.total} total · input ${card.input} · output ${card.output}")
        add(if (card.rate != null) "Cache breakdown: read ${card.cacheRead} · write ${card.cacheWrite} · hit rate ${card.rate}"
            else "Cache breakdown: read ${card.cacheRead} · write ${card.cacheWrite}")
        if (card.duration != null) add("Time ${card.duration}")
        when {
            card.singleModel != null -> add("Model: ${card.singleModel}")
            card.modelCount > 1 -> add("Model: ${card.modelCount} models")
        }
        if (card.lowCache) add("Suggestion: Cache hit rate is low; more context was billed again this turn")
        if (card.cost == null) add("Cost unavailable for this turn; showing usage only")
    }
}

/** A Persona conversation's lines: the cost in plain words, and each model's share when there were several. */
fun personaLinesEn(cost: TurnCost): List<String> = buildList {
    add("About ${formatCost(cost.total)} for this reply, estimated at list prices")
    if (cost.models.size >= 2) {
        add("By model:")
        for (m in cost.models) add("· ${ModelChanges.replyModelName(m.model)} ${formatCost(m.cost)}")
    }
}

enum class ReplyLabelKind { Worth, Tokens, Approx }

data class ReplyLabel(val kind: ReplyLabelKind, val amount: String)

/** The text after the reply's time. A Persona conversation says nothing without a price. */
fun replyLabel(persona: Boolean, usage: UsageSummary, cost: TurnCost?): ReplyLabel? {
    val priced = cost?.takeIf { it.total > 0 }
    if (persona && priced == null) return null
    val total = usageTotals(usage.models).total
    if (priced == null && total <= 0) return null
    return if (priced != null) ReplyLabel(if (persona) ReplyLabelKind.Approx else ReplyLabelKind.Worth, formatCost(priced.total))
        else ReplyLabel(ReplyLabelKind.Tokens, Subagents.tokens(total))
}

/**
 * Prices the catalog has already answered. A missing id is asked once, remembered as null when the catalog has none,
 * and asked again a minute after a failure. [ask] throws when the hub cannot be asked; it maps each answered id to a
 * price or null.
 */
class PriceBook(
    private val ask: suspend (List<String>) -> Map<String, ModelPrice?>,
    private val clock: () -> Long = System::currentTimeMillis,
) {
    private val prices = HashMap<String, ModelPrice?>()
    private val asking = HashSet<String>()
    private val failed = HashMap<String, Long>()
    private val waiting = LinkedHashSet<String>()

    fun request(ids: Iterable<String>, now: Long = clock()) {
        for (id in ids) {
            if (id.isEmpty() || prices.containsKey(id) || asking.contains(id)) continue
            val at = failed[id]
            if (at != null && now - at < RETRY_MS) continue
            asking += id
            waiting += id
        }
    }

    suspend fun drain() {
        if (waiting.isEmpty()) return
        val all = waiting.toList()
        waiting.clear()
        try {
            for (chunk in all.chunked(BATCH)) {
                try {
                    val found = ask(chunk)
                    for (id in chunk) {
                        prices[id] = if (found.containsKey(id)) found[id] else null
                        failed.remove(id)
                    }
                } catch (cancelled: kotlinx.coroutines.CancellationException) {
                    throw cancelled
                } catch (_: Exception) {
                    val now = clock()
                    for (id in chunk) failed[id] = now
                } finally {
                    chunk.forEach { asking.remove(it) }
                }
            }
        } catch (cancelled: kotlinx.coroutines.CancellationException) {
            // A chunk not asked yet must not stay "in flight", or the next request would skip it.
            all.forEach { asking.remove(it) }
            throw cancelled
        }
    }

    fun priceOf(id: String): ModelPrice? = prices[id]
    fun answered(id: String) = prices.containsKey(id)
    fun ready(ids: List<String>) = ids.all { answered(it) || failed.containsKey(it) } && ids.none { asking.contains(it) }
    fun forget() {
        prices.clear()
        asking.clear()
        failed.clear()
        waiting.clear()
    }

    private companion object {
        const val RETRY_MS = 60_000L
        const val BATCH = 100
    }
}
