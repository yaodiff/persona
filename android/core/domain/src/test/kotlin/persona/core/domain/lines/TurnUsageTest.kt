package persona.core.domain.lines

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Fold as WireFold
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.ModelPrice
import persona.core.contracts.TaskNote as WireTask
import persona.core.contracts.TokenUsage
import java.time.Instant

/**
 * The same cases as `web/src/lib/turn-usage.test.ts` (#5426): what an exchange used, what it cost, and the card's
 * English lines. Reports count once in the exchange that launched them.
 */
class TurnUsageTest {
    private fun user(seq: Int, text: String) = Item("u$seq#0", seq, role = "user", kind = "text", text = text)
    private fun answer(seq: Int, text: String, usage: List<TokenUsage> = emptyList()) =
        Item("a$seq#0", seq, role = "assistant", kind = "text", text = text, usage = usage)
    private fun think(seq: Int, usage: List<TokenUsage>) =
        Item("k$seq#0", seq, role = "assistant", kind = "thinking", text = "", usage = usage)
    private fun call(seq: Int, id: String, name: String = "Bash", usage: List<TokenUsage> = emptyList()) =
        Item("t$seq#0", seq, role = "assistant", kind = "tool_use", toolUseId = id, toolName = name, usage = usage)
    private fun result(seq: Int, id: String, usage: List<TokenUsage> = emptyList()) =
        Item("r$seq#0", seq, role = "user", kind = "tool_result", toolUseId = id, text = "ok", usage = usage)
    private fun opus(input: Long = 0, output: Long = 0, cacheRead: Long = 0, cacheWrite: Long = 0, cacheWrite1h: Long = 0) =
        TokenUsage(model = OPUS, input = input, output = output, cacheRead = cacheRead, cacheWrite = cacheWrite, cacheWrite1h = cacheWrite1h)
    private fun tokens(input: Long = 0, output: Long = 0, cacheRead: Long = 0, cacheWrite: Long = 0, cacheWrite1h: Long = 0, model: String = OPUS, fast: Boolean = false) =
        ModelTokens(model, fast, input, output, cacheRead, cacheWrite, cacheWrite1h)
    private fun models(exchange: ExchangeUsage) = summarize(exchange.usage)?.models

    @Test fun everyItemOfTheExchangeAddsItsTokens() {
        val fold = Item("fold:f5#0", 5, role = "assistant", kind = "fold",
            fold = WireFold(items = 4, tools = 2, throughSeq = 8, usage = listOf(opus(input = 10, output = 20, cacheRead = 300))))
        val duration = Item("d12#0", 12, role = "system", kind = "turn_duration",
            usage = listOf(TokenUsage(model = "gpt-5.5", input = 7, output = 8, cacheRead = 9)))
        val items = listOf(
            user(1, "fix it"),
            think(2, listOf(opus(input = 2, output = 30, cacheRead = 1000, cacheWrite = 500, cacheWrite1h = 100))),
            call(3, "c3", "Agent", listOf(opus(input = 1, output = 5, cacheRead = 1500))),
            result(4, "c3", listOf(TokenUsage(id = "agent:x", model = SONNET, input = 50, output = 60, cacheRead = 70, cacheWrite = 80))),
            fold,
            answer(11, "done", listOf(opus(output = 40, cacheRead = 1600))),
            duration,
            user(13, "next"),
            answer(14, "sure"),
        )
        val exchanges = chatUsage(items)
        assertEquals(listOf(
            tokens(input = 13, output = 95, cacheRead = 4400, cacheWrite = 500, cacheWrite1h = 100),
            tokens(model = "gpt-5.5", input = 7, output = 8, cacheRead = 9),
            tokens(model = SONNET, input = 50, output = 60, cacheRead = 70, cacheWrite = 80),
        ), models(exchanges[0]))
        assertEquals(true, exchanges[0].usage?.first)
        assertNull(exchanges[1].usage)
        assertEquals(5, segmentUsage(items)[0]?.entries?.size)
    }

    @Test fun aSubagentCountsOnceAtItsLargestCounts() {
        fun report(input: Long = 0, output: Long = 0) = listOf(TokenUsage(id = "agent:x", model = SONNET, input = input, output = output))
        val items = listOf(user(1, "go"), call(2, "c2", "Agent"), result(3, "c2", report(input = 10, output = 5)),
            result(4, "c2", report(input = 8, output = 9)).copy(id = "r4b#0"), answer(5, "ok"))
        assertEquals(listOf(tokens(model = SONNET, input = 10, output = 9)), models(chatUsage(items).single()))
        val two = listOf(user(1, "go"), result(3, "c2", report(input = 10)),
            result(4, "c4", listOf(TokenUsage(id = "agent:y", model = SONNET, input = 3))), answer(5, "ok"))
        assertEquals(13L, models(chatUsage(two).single())!!.single().input)
    }

    @Test fun aSubagentCountsInTheExchangeThatLaunchedIt() {
        val items = listOf(
            user(1, "check the export"), call(2, "c2", "Agent", listOf(opus(output = 10))),
            result(3, "c2", listOf(TokenUsage(id = "agent:x", model = SONNET, input = 5, output = 100))), answer(4, "it failed"),
            user(5, "try again"), call(6, "c6", "SendMessage", listOf(opus(output = 11))), result(7, "c6"), answer(8, "resumed"),
            user(9, "and?"), result(10, "c2", listOf(TokenUsage(id = "agent:x", model = SONNET, input = 7, output = 300))).copy(id = "r10#0"),
            answer(11, "done", listOf(opus(output = 12))),
        )
        assertEquals(listOf(
            listOf(listOf(OPUS, 0L, 10L), listOf(SONNET, 7L, 300L)),
            listOf(listOf(OPUS, 0L, 11L)),
            listOf(listOf(OPUS, 0L, 12L)),
        ), chatUsage(items).map { models(it)?.map { m -> listOf(m.model, m.input, m.output) } })
    }

    @Test fun aBackgroundResultAfterTheNextMessageStaysWithItsLaunch() {
        val done = result(6, "notify-bg1", listOf(TokenUsage(id = "agent:bg1", model = SONNET, output = 900)))
            .copy(task = WireTask(kind = "agent", status = "completed", taskId = "bg1", toolUseId = "c2"))
        val items = listOf(user(1, "look in the background"), call(2, "c2", "Agent", listOf(opus(output = 10))),
            result(3, "c2"), answer(4, "started"), user(5, "and meanwhile?"), done, answer(7, "here", listOf(opus(output = 20))))
        val exchanges = chatUsage(items)
        assertEquals(listOf(listOf(OPUS, 10L), listOf(SONNET, 900L)), models(exchanges[0])?.map { listOf(it.model, it.output) })
        assertEquals(listOf(listOf(OPUS, 20L)), models(exchanges[1])?.map { listOf(it.model, it.output) })
    }

    @Test fun aNestedSubagentCountsOnceAtTheLargerReport() {
        val parent = result(3, "c2", listOf(
            TokenUsage(id = "agent:p", model = SONNET, output = 100),
            TokenUsage(id = "agent:child", model = "claude-haiku-5-5", output = 40)))
        val child = Item("n5#0", 5, role = "system", kind = "notice", text = "Agent done",
            task = WireTask(kind = "agent", status = "completed", taskId = "child"),
            usage = listOf(TokenUsage(id = "agent:child", model = "claude-haiku-5-5", output = 60)))
        val items = listOf(user(1, "split it up"), call(2, "c2", "Agent"), parent, answer(4, "on it"), child,
            answer(6, "all done", listOf(opus(output = 1))))
        assertEquals(listOf(listOf(OPUS, 1L), listOf(SONNET, 100L), listOf("claude-haiku-5-5", 60L)),
            models(chatUsage(items).single())?.map { listOf(it.model, it.output) })
    }

    @Test fun aResumedSubagentAndItsChildCountOnce() {
        fun task(id: String, call: String, status: String) = WireTask(kind = "agent", taskId = id, toolUseId = call, status = status)
        fun report(seq: Int, call: String, id: String, status: String, usage: List<TokenUsage>) =
            result(seq, call, usage).copy(id = "r$seq#0", task = task(id, call, status))
        fun parent(n: Int) = TokenUsage(id = "agent:P", model = OPUS, input = 10, output = 100L + n)
        val child = TokenUsage(id = "agent:C", model = OPUS, input = 1000, output = 5000)
        val items = listOf(
            user(1, "review it"), call(2, "toolu_P", "Agent"), result(3, "toolu_P").copy(task = task("P", "toolu_P", "running")),
            report(4, "toolu_inside_P", "C", "failed", listOf(child)),
            report(5, "toolu_P", "P", "failed", listOf(parent(0), child)),
            answer(6, "it failed"),
            user(7, "resume it"),
            report(8, "toolu_P", "P", "completed", listOf(parent(20), child)),
            answer(9, "done"),
        )
        assertEquals(listOf(listOf(listOf(1010L, 5120L)), null),
            chatUsage(items).map { summarize(it.usage)?.models?.map { m -> listOf(m.input, m.output) } })
    }

    @Test fun aStoppedTurnKeepsItsNoticeInTheLastExchange() {
        val stopped = Item("n5#0", 5, role = "system", kind = "notice", text = "已中断",
            usage = listOf(TokenUsage(model = "gpt-5.5", input = 70, output = 8)))
        val exchanges = chatUsage(listOf(user(1, "rename it"), call(2, "c2"), result(3, "c2"), answer(4, "renaming"), stopped))
        assertEquals(true, exchanges.single().cut)
        assertEquals(listOf(tokens(model = "gpt-5.5", input = 70, output = 8)), models(exchanges.single()))
    }

    @Test fun wrappersSumAndTheFastSpeedStaysApart() {
        assertEquals(listOf(
            tokens(input = 3),
            tokens(fast = true, output = 3),
            tokens(model = "", input = 4),
        ), modelTokens(listOf(
            TokenUsage(model = "$OPUS-20261101", input = 1),
            TokenUsage(model = OPUS, output = 3, fast = true),
            TokenUsage(model = "us.anthropic.$OPUS-v1:0", input = 2),
            TokenUsage(input = 4),
        )))
        assertEquals(emptyList<ModelTokens>(), modelTokens(listOf(TokenUsage(model = OPUS))))
        assertEquals(listOf(SONNET to 7L, OPUS to 1L), modelTokens(listOf(
            TokenUsage(id = "agent:x", model = SONNET, output = 5),
            TokenUsage(model = OPUS, output = 1),
            TokenUsage(id = "agent:x", model = SONNET, output = 4),
            TokenUsage(model = SONNET, output = 2),
        )).map { it.model to it.output })
    }

    @Test fun aBackgroundNoticeOnItsCardCountsInTheLaunchingExchange() {
        val notice = Item("n6#0", 6, role = "system", kind = "notice", text = "Agent done",
            task = WireTask(kind = "agent", status = "completed", taskId = "bg1", toolUseId = "c2"),
            usage = listOf(TokenUsage(id = "agent:bg1", model = SONNET, output = 900)))
        val items = listOf(user(1, "look in the background"), call(2, "c2", "Agent", listOf(opus(output = 10))),
            result(3, "c2"), answer(4, "started"), user(5, "and meanwhile?"), notice, answer(7, "here", listOf(opus(output = 20))))
        val exchanges = chatUsage(items)
        assertEquals(listOf(listOf(OPUS, 10L), listOf(SONNET, 900L)), models(exchanges[0])?.map { listOf(it.model, it.output) })
        assertEquals(listOf(listOf(OPUS, 20L)), models(exchanges[1])?.map { listOf(it.model, it.output) })
    }

    @Test fun aWakeCountsInTheWorkItWokeAndOnlyTheFirstRoundIsMarked() {
        val wake = Item("w1#0", 1, role = "system", kind = "notice", wake = true, text = "Background command finished",
            task = WireTask(kind = "shell", status = "completed", taskId = "b1"), usage = listOf(opus(input = 5)))
        val exchanges = chatUsage(listOf(wake, answer(2, "built", listOf(opus(output = 2))), user(3, "hi"), answer(4, "hello"),
            user(5, "again"), answer(6, "sure", listOf(opus(output = 1)))))
        assertEquals(listOf(tokens(input = 5, output = 2), null, tokens(output = 1)), exchanges.map { models(it)?.single() })
        assertEquals(listOf(true, null, false), exchanges.map { it.usage?.first })
    }

    @Test fun theUsageKeyIsStableAndParsesBack() {
        val one = segmentUsage(listOf(user(1, "a"), answer(2, "b", listOf(opus(input = 1)))))[0]
        val two = segmentUsage(listOf(user(1, "a"), answer(2, "b", listOf(opus(input = 1)))))[0]
        assertEquals(usageKey(one), usageKey(two))
        assertEquals(UsageSummary(listOf(tokens(input = 1))), parseUsage(usageKey(one)))
        assertNull(parseUsage(null))
        assertNull(parseUsage("{"))
    }

    @Test fun usageOnTheItemAndItsFoldDecodes() {
        val item = JsonCodec.decodeFromString<Item>("""{"id":"a#0","kind":"text","usage":[{"model":"claude-opus-5-5","in":2,"out":3,"cache_read":4,"cache_write":5,"cache_write_1h":1,"fast":true}]}""")
        val usage = item.usage.single()
        assertEquals(2L, usage.input)
        assertEquals(1L, usage.cacheWrite1h)
        assertTrue(usage.fast)
        assertFalse(usage.report)
        val folded = JsonCodec.decodeFromString<Item>("""{"id":"f#0","kind":"fold","fold":{"items":1,"usage":[{"id":"agent:x","model":"m","out":9}]}}""")
        assertEquals("agent:x", folded.fold!!.usage.single().id)
        assertTrue(folded.fold!!.usage.single().report)
        assertEquals(emptyList<TokenUsage>(), JsonCodec.decodeFromString<Item>("""{"id":"o#0","kind":"text"}""").usage)
    }

    @Test fun totalsAddEveryKindAndTheHitRateIsTheCacheShare() {
        val sums = usageTotals(listOf(tokens(input = 100, output = 50, cacheRead = 300, cacheWrite = 100), tokens(model = SONNET, output = 10)))
        assertEquals(listOf(560L, 100L, 60L, 300L, 100L), listOf(sums.total, sums.input, sums.output, sums.cacheRead, sums.cacheWrite))
        assertEquals(0.6, sums.hit)
        assertNull(usageTotals(listOf(tokens(output = 5))).hit)
    }

    @Test fun moneyAndPercentsReadAsTheWebWritesThem() {
        assertEquals(listOf("$0.12", "<$0.01", "$0.00", "$12.50"), listOf(formatCost(0.123), formatCost(0.004), formatCost(0.0), formatCost(12.5)))
        // The exact binary value, the web's toFixed: 1.005 is "$1.00", 0.285 is "$0.28".
        assertEquals(listOf("$1.00", "$0.28", "$0.35"), listOf(formatCost(1.005), formatCost(0.285), formatCost(0.35)))
        assertEquals(listOf("60%", "12.3%", "100%", null), listOf(formatPercent(0.6), formatPercent(0.1234), formatPercent(0.9999), formatPercent(null)))
        assertEquals("1m 5s", usageDuration(65_000))
        assertEquals("2h 0m", usageDuration(7_200_000))
        assertEquals("H", usageDuration(7_200_000, UsageDurationText(hoursMinutes = { _, _ -> "H" })))
    }

    @Test fun whitespaceOnlyIsNotTheAnswer() {
        val exchanges = chatUsage(listOf(user(1, "hi"), answer(2, "done", listOf(opus(output = 2))), answer(3, "  \n\t")))
        assertEquals("a2#0", exchanges.single().answerId)
    }

    @Test fun aModelIsPricedPerMillionAndOnlyWhenEveryKindHasAPrice() {
        val price = ModelPrice(input = 4.0, output = 20.0, cacheRead = 0.2, cacheWrite = 5.0, cacheWrite1h = 8.0)
        val row = tokens(input = 1_000_000, output = 1_000_000, cacheRead = 1_000_000, cacheWrite = 3_000_000, cacheWrite1h = 1_000_000)
        assertEquals(4 + 20 + 0.2 + 2 * 5 + 8, modelCost(row, price)!!, 1e-9)
        assertEquals(4 + 20 + 0.2 + 3 * 5, modelCost(row, price.copy(cacheWrite1h = null))!!, 1e-9)
        assertNull(modelCost(tokens(input = 10, cacheWrite = 5), ModelPrice(input = 4.0, output = 20.0, cacheRead = 0.2)))
        assertEquals(4 * 10 / 1e6, modelCost(tokens(input = 10), ModelPrice(input = 4.0, output = 20.0))!!, 1e-12)
        assertNull(modelCost(tokens(input = 10, fast = true), price))
        assertNull(modelCost(tokens(input = 10), null))
    }

    @Test fun theExchangeHasACostOnlyWhenEveryModelDoes() {
        val price = ModelPrice(input = 4.0, output = 20.0, cacheRead = 0.2, cacheWrite = 5.0, cacheWrite1h = 8.0)
        val sonnet = ModelPrice(input = 2.0, output = 10.0, cacheRead = 0.1, cacheWrite = 2.5)
        val prices = { id: String -> if (id == OPUS) price else if (id == SONNET) sonnet else null }
        val both = turnCost(listOf(tokens(output = 5000), tokens(model = SONNET, output = 2000)), prices)
        assertEquals(listOf(ModelShare(OPUS, 0.1), ModelShare(SONNET, 0.02)), both?.models)
        assertEquals(0.12, both!!.total, 1e-9)
        assertNull(turnCost(listOf(tokens(output = 5000), tokens(model = "claude-haiku-5-5", output = 1)), prices))
        assertNull(turnCost(listOf(tokens(output = 5000), tokens(model = "", output = 1)), prices))
        assertNull(turnCost(listOf(tokens(output = 5000), tokens(output = 1, fast = true)), prices))
    }

    @Test fun theCardLinesFollowTheWeb() {
        val one = UsageSummary(listOf(tokens(input = 2, output = 3400, cacheRead = 1_200_000, cacheWrite = 9000)))
        assertEquals(listOf(
            "Request details",
            "Token value this turn: $0.12",
            "Tokens: 1.2M total · input 2 · output 3.4k",
            "Cache breakdown: read 1.2M · write 9.0k · hit rate 99.3%",
            "Time 1m 5s",
            "Model: Opus 5.5",
        ), usageLinesEn(one, TurnCost(0.1234, listOf(ModelShare(OPUS, 0.1234))), 65_000))
        val two = UsageSummary(listOf(tokens(output = 5000), tokens(model = SONNET, output = 2000)))
        assertEquals(listOf(
            "Request details",
            "Token value this turn: $0.12",
            "By model:",
            "· Opus 5.5 $0.10",
            "· Sonnet 5.5 $0.02",
            "Tokens: 7.0k total · input 0 · output 7.0k",
            "Cache breakdown: read 0 · write 0",
        ), usageLinesEn(two, TurnCost(0.12, listOf(ModelShare(OPUS, 0.1), ModelShare(SONNET, 0.02)))))
        val unpriced = UsageSummary(listOf(tokens(input = 100, output = 23_300), tokens(model = "claude-haiku-5-5", output = 10)))
        assertEquals(listOf(
            "Tokens: 23.4k total · input 100 · output 23.3k",
            "Cache breakdown: read 0 · write 0 · hit rate 0%",
            "Model: 2 models",
            "Cost unavailable for this turn; showing usage only",
        ), usageLinesEn(unpriced, null))
    }

    @Test fun aLowCacheIsSuggestedExceptOnTheFirstRound() {
        val missed = listOf(tokens(input = 45_000, cacheRead = 5000, output = 10))
        assertTrue(lowCache(UsageSummary(missed)))
        assertFalse(lowCache(UsageSummary(missed, first = true)))
        assertFalse(lowCache(UsageSummary(listOf(tokens(input = 40_000, cacheRead = 5000)))))
        assertFalse(lowCache(UsageSummary(listOf(tokens(input = 30_000, cacheRead = 30_000)))))
        val lines = usageLinesEn(UsageSummary(missed), null)
        assertEquals("Suggestion: Cache hit rate is low; more context was billed again this turn", lines[lines.lastIndex - 1])
    }

    @Test fun aPersonaConversationSaysTheCostInPlainWords() {
        assertEquals(listOf("About $0.12 for this reply, estimated at list prices"),
            personaLinesEn(TurnCost(0.12, listOf(ModelShare(OPUS, 0.12)))))
        assertEquals(listOf(
            "About $0.12 for this reply, estimated at list prices",
            "By model:",
            "· Opus 5.5 $0.10",
            "· Sonnet 5.5 $0.02",
        ), personaLinesEn(TurnCost(0.12, listOf(ModelShare(OPUS, 0.1), ModelShare(SONNET, 0.02)))))
    }

    @Test fun pricesAreAskedOnceAndAFailureIsNotRetriedAtOnce() = runBlocking {
        var calls = 0
        val asked = mutableListOf<List<String>>()
        val book = PriceBook({ ids ->
            calls++
            asked += ids
            mapOf(OPUS to ModelPrice(input = 4.0, output = 20.0))
        })
        book.request(listOf(OPUS, "claude-haiku-5-5"))
        book.request(listOf(OPUS))
        book.drain()
        book.request(listOf(OPUS, "claude-haiku-5-5"))
        book.drain()
        assertEquals(1, calls)
        assertEquals(listOf(OPUS, "claude-haiku-5-5"), asked.single())
        assertEquals(4.0, book.priceOf(OPUS)!!.input)
        assertTrue(book.answered("claude-haiku-5-5"))
        assertNull(book.priceOf("claude-haiku-5-5"))

        var failed = 0
        val offline = PriceBook({ failed++; throw IllegalStateException("offline") }, clock = { 1_000 })
        offline.request(listOf(OPUS))
        offline.drain()
        assertFalse(offline.answered(OPUS))
        offline.request(listOf(OPUS), now = 1_000)
        offline.drain()
        assertEquals(1, failed)
        offline.request(listOf(OPUS), now = 61_000)
        offline.drain()
        assertEquals(2, failed)
    }

    @Test fun cancellingAPriceAskDoesNotCountAsAnAnswer() = runBlocking {
        var calls = 0
        val book = PriceBook({
            calls++
            if (calls == 1) throw CancellationException("stop")
            emptyMap()
        })
        book.request(listOf(OPUS))
        var cancelled = false
        try {
            book.drain()
        } catch (_: CancellationException) {
            cancelled = true
        }
        assertTrue(cancelled)
        assertFalse(book.answered(OPUS))
        book.request(listOf(OPUS))
        book.drain()
        assertEquals(2, calls)
        assertTrue(book.answered(OPUS))
    }

    @Test fun oneFinishedWorkLineIsTheReplysTime() {
        val start = Instant.parse("2026-11-14T00:00:00Z")
        val user = ChatLine.Row(ChatRow("user", LineItem(Item("u#0", 1, role = "user", kind = "text", text = "hi"))))
        val work = ChatLine.Work("w", emptyList(), live = false, start = start, end = start.plusMillis(65_000), open = false)
        val reply = ChatLine.Row(ChatRow("reply", LineItem(Item("a#0", 2, role = "assistant", kind = "text", text = "ok"))))
        assertEquals(65_000L, replyUsageMillis(listOf(user, work, reply))["a#0"])
        val again = ChatLine.Work("w2", emptyList(), live = false, start = start, end = start.plusMillis(1_000), open = false)
        assertNull(replyUsageMillis(listOf(user, work, again, reply))["a#0"])
        val live = work.copy(live = true)
        assertNull(replyUsageMillis(listOf(user, live, reply))["a#0"])
    }

    private companion object {
        const val OPUS = "claude-opus-5-5"
        const val SONNET = "claude-sonnet-5-5"
    }
}
