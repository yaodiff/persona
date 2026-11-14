import Foundation
import Testing
@testable import Persona

/// The same cases as `web/src/lib/turn-usage.test.ts` and the Android `TurnUsageTest` (#5426, #5470).
@MainActor
struct TurnUsageTests {
    private let opusID = "claude-opus-5-5"
    private let sonnetID = "claude-sonnet-5-5"

    private func user(_ seq: Int, _ text: String) -> Item {
        Item(id: "u\(seq)#0", seq: seq, ts: nil, role: "user", kind: .text, text: text)
    }

    private func answer(_ seq: Int, _ text: String, usage: [TokenUsage] = []) -> Item {
        var it = Item(id: "a\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: .text, text: text)
        if !usage.isEmpty { it.usage = usage }
        return it
    }

    private func think(_ seq: Int, _ usage: [TokenUsage]) -> Item {
        var it = Item(id: "k\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: .thinking, text: "")
        it.usage = usage
        return it
    }

    private func call(_ seq: Int, _ id: String, _ name: String = "Bash", usage: [TokenUsage] = []) -> Item {
        var it = Item(id: "t\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: .toolUse, toolUseID: id, toolName: name)
        if !usage.isEmpty { it.usage = usage }
        return it
    }

    private func result(_ seq: Int, _ id: String, usage: [TokenUsage] = [], itemID: String? = nil) -> Item {
        var it = Item(id: itemID ?? "r\(seq)#0", seq: seq, ts: nil, role: "user", kind: .toolResult, text: "ok", toolUseID: id)
        if !usage.isEmpty { it.usage = usage }
        return it
    }

    private func opus(input: Int64 = 0, output: Int64 = 0, cacheRead: Int64 = 0, cacheWrite: Int64 = 0, cacheWrite1h: Int64 = 0) -> TokenUsage {
        TokenUsage(model: opusID, input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite, cacheWrite1h: cacheWrite1h)
    }

    private func tokens(input: Int64 = 0, output: Int64 = 0, cacheRead: Int64 = 0, cacheWrite: Int64 = 0, cacheWrite1h: Int64 = 0,
                        model: String? = nil, fast: Bool = false) -> ModelTokens {
        ModelTokens(model: model ?? opusID, fast: fast, input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite,
                    cacheWrite1h: cacheWrite1h)
    }

    private func models(_ exchange: ExchangeUsage) -> [ModelTokens]? { summarize(exchange.usage)?.models }

    private func task(_ id: String, call: String? = nil, status: String) -> TaskNote {
        TaskNote(kind: "agent", status: status, title: "", taskID: id, toolUseID: call)
    }

    @Test func everyItemOfTheExchangeAddsItsTokens() {
        var foldItem = Item(id: "fold:f5#0", seq: 5, ts: nil, role: "assistant", kind: .fold)
        foldItem.fold = Fold(items: 4, tools: 2, thoughts: 0, until: nil, throughSeq: 8, throughID: "f",
                             usage: [opus(input: 10, output: 20, cacheRead: 300)])
        var duration = Item(id: "d12#0", seq: 12, ts: nil, role: "system", kind: .turnDuration)
        duration.usage = [TokenUsage(model: "gpt-5.5", input: 7, output: 8, cacheRead: 9)]
        let items = [
            user(1, "fix it"),
            think(2, [opus(input: 2, output: 30, cacheRead: 1000, cacheWrite: 500, cacheWrite1h: 100)]),
            call(3, "c3", "Agent", usage: [opus(input: 1, output: 5, cacheRead: 1500)]),
            result(4, "c3", usage: [TokenUsage(id: "agent:x", model: sonnetID, input: 50, output: 60, cacheRead: 70, cacheWrite: 80)]),
            foldItem, answer(11, "done", usage: [opus(output: 40, cacheRead: 1600)]), duration,
            user(13, "next"), answer(14, "sure"),
        ]
        let exchanges = chatUsage(items)
        #expect(models(exchanges[0]) == [
            tokens(input: 13, output: 95, cacheRead: 4400, cacheWrite: 500, cacheWrite1h: 100),
            tokens(input: 7, output: 8, cacheRead: 9, model: "gpt-5.5"),
            tokens(input: 50, output: 60, cacheRead: 70, cacheWrite: 80, model: sonnetID),
        ])
        #expect(exchanges[0].usage?.first == true)
        #expect(exchanges[1].usage == nil)
        #expect(segmentUsage(items)[0]?.entries.count == 5)
    }

    @Test func aSubagentCountsOnceAtItsLargestCounts() {
        func report(input: Int64 = 0, output: Int64 = 0) -> [TokenUsage] {
            [TokenUsage(id: "agent:x", model: sonnetID, input: input, output: output)]
        }
        let items = [user(1, "go"), call(2, "c2", "Agent"), result(3, "c2", usage: report(input: 10, output: 5)),
                     result(4, "c2", usage: report(input: 8, output: 9), itemID: "r4b#0"), answer(5, "ok")]
        #expect(models(chatUsage(items)[0]) == [tokens(input: 10, output: 9, model: sonnetID)])
        let two = [user(1, "go"), result(3, "c2", usage: report(input: 10)),
                   result(4, "c4", usage: [TokenUsage(id: "agent:y", model: sonnetID, input: 3)]), answer(5, "ok")]
        #expect(models(chatUsage(two)[0])?.first?.input == 13)
    }

    @Test func aSubagentCountsInTheExchangeThatLaunchedIt() {
        let items = [
            user(1, "check the export"), call(2, "c2", "Agent", usage: [opus(output: 10)]),
            result(3, "c2", usage: [TokenUsage(id: "agent:x", model: sonnetID, input: 5, output: 100)]), answer(4, "it failed"),
            user(5, "try again"), call(6, "c6", "SendMessage", usage: [opus(output: 11)]), result(7, "c6"), answer(8, "resumed"),
            user(9, "and?"), result(10, "c2", usage: [TokenUsage(id: "agent:x", model: sonnetID, input: 7, output: 300)], itemID: "r10#0"),
            answer(11, "done", usage: [opus(output: 12)]),
        ]
        let got = chatUsage(items).map { models($0)?.map { [$0.model, "\($0.input)", "\($0.output)"] } }
        #expect(got == [
            [[opusID, "0", "10"], [sonnetID, "7", "300"]],
            [[opusID, "0", "11"]],
            [[opusID, "0", "12"]],
        ])
    }

    @Test func aBackgroundResultAfterTheNextMessageStaysWithItsLaunch() {
        var done = result(6, "notify-bg1", usage: [TokenUsage(id: "agent:bg1", model: sonnetID, output: 900)])
        done.task = task("bg1", call: "c2", status: "completed")
        let items = [user(1, "look in the background"), call(2, "c2", "Agent", usage: [opus(output: 10)]),
                     result(3, "c2"), answer(4, "started"), user(5, "and meanwhile?"), done,
                     answer(7, "here", usage: [opus(output: 20)])]
        let exchanges = chatUsage(items)
        #expect(models(exchanges[0])?.map { [$0.model, "\($0.output)"] } == [[opusID, "10"], [sonnetID, "900"]])
        #expect(models(exchanges[1])?.map { [$0.model, "\($0.output)"] } == [[opusID, "20"]])
    }

    @Test func aNestedSubagentCountsOnceAtTheLargerReport() {
        let parent = result(3, "c2", usage: [
            TokenUsage(id: "agent:p", model: sonnetID, output: 100),
            TokenUsage(id: "agent:child", model: "claude-haiku-5-5", output: 40),
        ])
        var child = Item(id: "n5#0", seq: 5, ts: nil, role: "system", kind: .notice, text: "Agent done")
        child.task = task("child", status: "completed")
        child.usage = [TokenUsage(id: "agent:child", model: "claude-haiku-5-5", output: 60)]
        let items = [user(1, "split it up"), call(2, "c2", "Agent"), parent, answer(4, "on it"), child,
                     answer(6, "all done", usage: [opus(output: 1)])]
        #expect(models(chatUsage(items)[0])?.map { [$0.model, "\($0.output)"] } ==
            [[opusID, "1"], [sonnetID, "100"], ["claude-haiku-5-5", "60"]])
    }

    @Test func aResumedSubagentAndItsChildCountOnce() {
        func report(_ seq: Int, _ callID: String, _ id: String, _ status: String, _ usage: [TokenUsage]) -> Item {
            var it = result(seq, callID, usage: usage, itemID: "r\(seq)#0")
            it.task = task(id, call: callID, status: status)
            return it
        }
        func parent(_ n: Int64) -> TokenUsage { TokenUsage(id: "agent:P", model: opusID, input: 10, output: 100 + n) }
        let child = TokenUsage(id: "agent:C", model: opusID, input: 1000, output: 5000)
        var running = result(3, "toolu_P")
        running.task = task("P", call: "toolu_P", status: "running")
        let items = [
            user(1, "review it"), call(2, "toolu_P", "Agent"), running,
            report(4, "toolu_inside_P", "C", "failed", [child]),
            report(5, "toolu_P", "P", "failed", [parent(0), child]),
            answer(6, "it failed"), user(7, "resume it"),
            report(8, "toolu_P", "P", "completed", [parent(20), child]),
            answer(9, "done"),
        ]
        let got = chatUsage(items).map { summarize($0.usage)?.models.map { ["\($0.input)", "\($0.output)"] } }
        #expect(got[0] == [["1010", "5120"]])
        #expect(got[1] == nil)
    }

    @Test func aStoppedTurnKeepsItsNoticeInTheLastExchange() {
        var stopped = Item(id: "n5#0", seq: 5, ts: nil, role: "system", kind: .notice, text: "已中断")
        stopped.usage = [TokenUsage(model: "gpt-5.5", input: 70, output: 8)]
        let exchanges = chatUsage([user(1, "rename it"), call(2, "c2"), result(3, "c2"), answer(4, "renaming"), stopped])
        #expect(exchanges[0].cut == true)
        #expect(models(exchanges[0]) == [tokens(input: 70, output: 8, model: "gpt-5.5")])
    }

    @Test func wrappersSumAndTheFastSpeedStaysApart() {
        #expect(modelTokens([
            TokenUsage(model: "\(opusID)-20261101", input: 1),
            TokenUsage(model: opusID, output: 3, fast: true),
            TokenUsage(model: "us.anthropic.\(opusID)-v1:0", input: 2),
            TokenUsage(input: 4),
        ]) == [tokens(input: 3), tokens(output: 3, fast: true), tokens(input: 4, model: "")])
        #expect(modelTokens([TokenUsage(model: opusID)]).isEmpty)
        let merged = modelTokens([
            TokenUsage(id: "agent:x", model: sonnetID, output: 5),
            TokenUsage(model: opusID, output: 1),
            TokenUsage(id: "agent:x", model: sonnetID, output: 4),
            TokenUsage(model: sonnetID, output: 2),
        ]).map { "\($0.model) \($0.output)" }
        #expect(merged == ["\(sonnetID) 7", "\(opusID) 1"])
    }

    @Test func aBackgroundNoticeOnItsCardCountsInTheLaunchingExchange() {
        var notice = Item(id: "n6#0", seq: 6, ts: nil, role: "system", kind: .notice, text: "Agent done")
        notice.task = task("bg1", call: "c2", status: "completed")
        notice.usage = [TokenUsage(id: "agent:bg1", model: sonnetID, output: 900)]
        let items = [user(1, "look in the background"), call(2, "c2", "Agent", usage: [opus(output: 10)]),
                     result(3, "c2"), answer(4, "started"), user(5, "and meanwhile?"), notice,
                     answer(7, "here", usage: [opus(output: 20)])]
        let exchanges = chatUsage(items)
        #expect(models(exchanges[0])?.map { [$0.model, "\($0.output)"] } == [[opusID, "10"], [sonnetID, "900"]])
        #expect(models(exchanges[1])?.map { [$0.model, "\($0.output)"] } == [[opusID, "20"]])
    }

    @Test func aWakeCountsInTheWorkItWokeAndOnlyTheFirstRoundIsMarked() {
        var wake = Item(id: "w1#0", seq: 1, ts: nil, role: "system", kind: .notice, text: "Background command finished")
        wake.wake = true
        wake.task = TaskNote(kind: "shell", status: "completed", title: "", taskID: "b1")
        wake.usage = [opus(input: 5)]
        let exchanges = chatUsage([wake, answer(2, "built", usage: [opus(output: 2)]), user(3, "hi"), answer(4, "hello"),
                                   user(5, "again"), answer(6, "sure", usage: [opus(output: 1)])])
        #expect(exchanges.map { models($0)?.first } == [tokens(input: 5, output: 2), nil, tokens(output: 1)])
        #expect(exchanges.map { $0.usage?.first } == [true, nil, false])
    }

    @Test func theUsageKeyIsStableAndParsesBack() {
        let one = segmentUsage([user(1, "a"), answer(2, "b", usage: [opus(input: 1)])])[0]
        let two = segmentUsage([user(1, "a"), answer(2, "b", usage: [opus(input: 1)])])[0]
        #expect(usageKey(one) == usageKey(two))
        #expect(parseUsage(usageKey(one)) == UsageSummary(models: [tokens(input: 1)]))
        #expect(parseUsage(nil) == nil)
        #expect(parseUsage("{") == nil)
    }

    @Test func usageOnTheItemAndItsFoldDecodes() throws {
        let item = try JSONDecoder().decode(Item.self, from: Data("""
        {"id":"a#0","seq":1,"role":"assistant","kind":"text","usage":[{"model":"claude-opus-5-5","in":2,"out":3,"cache_read":4,"cache_write":5,"cache_write_1h":1,"fast":true}]}
        """.utf8))
        let usage = try #require(item.usage?.first)
        #expect(usage.input == 2)
        #expect(usage.cacheWrite1h == 1)
        #expect(usage.fast)
        #expect(!usage.report)
        let folded = try JSONDecoder().decode(Item.self, from: Data("""
        {"id":"f#0","seq":2,"role":"assistant","kind":"fold","fold":{"items":1,"tools":0,"thoughts":0,"through_seq":1,"through_id":"f","usage":[{"id":"agent:x","model":"m","out":9}]}}
        """.utf8))
        #expect(folded.fold?.usage?.first?.id == "agent:x")
        #expect(folded.fold?.usage?.first?.report == true)
        let older = try JSONDecoder().decode(Item.self, from: Data(#"{"id":"o#0","seq":3,"role":"assistant","kind":"text"}"#.utf8))
        #expect(older.usage == nil)
    }

    @Test func totalsAddEveryKindAndTheHitRateIsTheCacheShare() {
        let sums = usageTotals([tokens(input: 100, output: 50, cacheRead: 300, cacheWrite: 100), tokens(output: 10, model: sonnetID)])
        #expect([sums.total, sums.input, sums.output, sums.cacheRead, sums.cacheWrite] == [560, 100, 60, 300, 100])
        #expect(sums.hit == 0.6)
        #expect(usageTotals([tokens(output: 5)]).hit == nil)
    }

    @Test func moneyAndPercentsReadAsTheWebWritesThem() {
        #expect([formatCost(0.123), formatCost(0.004), formatCost(0), formatCost(12.5)] == ["$0.12", "<$0.01", "$0.00", "$12.50"])
        // The exact binary value, the web's toFixed: 1.005 is "$1.00", 0.285 is "$0.28".
        #expect([formatCost(1.005), formatCost(0.285), formatCost(0.35)] == ["$1.00", "$0.28", "$0.35"])
        #expect([formatPercent(0.6), formatPercent(0.1234), formatPercent(0.9999), formatPercent(nil)] == ["60%", "12.3%", "100%", nil])
        #expect(usageDuration(65_000) == "1m 5s")
        #expect(usageDuration(7_200_000) == "2h 0m")
        #expect(usageDuration(7_200_000, text: UsageDurationText(hoursMinutes: { _, _ in "H" }, daysHoursMinutes: { _, _, _ in "D" })) == "H")
    }

    @Test func whitespaceOnlyIsNotTheAnswer() {
        let exchanges = chatUsage([user(1, "hi"), answer(2, "done", usage: [opus(output: 2)]), answer(3, "  \n\t")])
        #expect(exchanges.first?.answerID == "a2#0")
    }

    @Test func aModelIsPricedPerMillionAndOnlyWhenEveryKindHasAPrice() {
        let price = ModelPrice(input: 4, output: 20, cacheRead: 0.2, cacheWrite: 5, cacheWrite1h: 8)
        let row = tokens(input: 1_000_000, output: 1_000_000, cacheRead: 1_000_000, cacheWrite: 3_000_000, cacheWrite1h: 1_000_000)
        let full = 4.0 + 20 + 0.2 + 2 * 5 + 8
        #expect(modelCost(row, price) == full)
        var without = price
        without.cacheWrite1h = nil
        let fallback = 4.0 + 20 + 0.2 + 3 * 5
        #expect(modelCost(row, without) == fallback)
        #expect(modelCost(tokens(input: 10, cacheWrite: 5), ModelPrice(input: 4, output: 20, cacheRead: 0.2)) == nil)
        #expect(modelCost(tokens(input: 10), ModelPrice(input: 4, output: 20)) == 4 * 10 / 1e6)
        #expect(modelCost(tokens(input: 10, fast: true), price) == nil)
        #expect(modelCost(tokens(input: 10), nil) == nil)
    }

    @Test func theExchangeHasACostOnlyWhenEveryModelDoes() {
        let price = ModelPrice(input: 4, output: 20, cacheRead: 0.2, cacheWrite: 5, cacheWrite1h: 8)
        let sonnet = ModelPrice(input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5)
        let prices: (String) -> ModelPrice? = { id in id == opusID ? price : id == sonnetID ? sonnet : nil }
        let both = turnCost([tokens(output: 5000), tokens(output: 2000, model: sonnetID)], price: prices)
        #expect(both?.models == [ModelShare(model: opusID, cost: 0.1), ModelShare(model: sonnetID, cost: 0.02)])
        #expect(abs((both?.total ?? 0) - 0.12) < 1e-9)
        #expect(turnCost([tokens(output: 5000), tokens(output: 1, model: "claude-haiku-5-5")], price: prices) == nil)
        #expect(turnCost([tokens(output: 5000), tokens(output: 1, model: "")], price: prices) == nil)
        #expect(turnCost([tokens(output: 5000), tokens(output: 1, fast: true)], price: prices) == nil)
    }

    @Test func theCardLinesFollowTheWeb() {
        let one = UsageSummary(models: [tokens(input: 2, output: 3400, cacheRead: 1_200_000, cacheWrite: 9000)])
        #expect(usageLinesEn(one, TurnCost(total: 0.1234, models: [ModelShare(model: opusID, cost: 0.1234)]), ms: 65_000) == [
            "Request details", "Token value this turn: $0.12",
            "Tokens: 1.2M total · input 2 · output 3.4k",
            "Cache breakdown: read 1.2M · write 9.0k · hit rate 99.3%",
            "Time 1m 5s", "Model: Opus 5.5",
        ])
        let two = UsageSummary(models: [tokens(output: 5000), tokens(output: 2000, model: sonnetID)])
        #expect(usageLinesEn(two, TurnCost(total: 0.12, models: [ModelShare(model: opusID, cost: 0.1), ModelShare(model: sonnetID, cost: 0.02)])) == [
            "Request details", "Token value this turn: $0.12", "By model:",
            "· Opus 5.5 $0.10", "· Sonnet 5.5 $0.02",
            "Tokens: 7.0k total · input 0 · output 7.0k",
            "Cache breakdown: read 0 · write 0",
        ])
        let unpriced = UsageSummary(models: [tokens(input: 100, output: 23_300), tokens(output: 10, model: "claude-haiku-5-5")])
        #expect(usageLinesEn(unpriced, nil) == [
            "Tokens: 23.4k total · input 100 · output 23.3k",
            "Cache breakdown: read 0 · write 0 · hit rate 0%",
            "Model: 2 models",
            "Cost unavailable for this turn; showing usage only",
        ])
    }

    @Test func aLowCacheIsSuggestedExceptOnTheFirstRound() {
        let missed = [tokens(input: 45_000, output: 10, cacheRead: 5000)]
        #expect(lowCache(UsageSummary(models: missed)))
        #expect(!lowCache(UsageSummary(models: missed, first: true)))
        #expect(!lowCache(UsageSummary(models: [tokens(input: 40_000, cacheRead: 5000)])))
        #expect(!lowCache(UsageSummary(models: [tokens(input: 30_000, cacheRead: 30_000)])))
        let lines = usageLinesEn(UsageSummary(models: missed), nil)
        #expect(lines[lines.count - 2] == "Suggestion: Cache hit rate is low; more context was billed again this turn")
    }

    @Test func aPersonaConversationSaysTheCostInPlainWords() {
        #expect(personaLinesEn(TurnCost(total: 0.12, models: [ModelShare(model: opusID, cost: 0.12)])) ==
            ["About $0.12 for this reply, estimated at list prices"])
        #expect(personaLinesEn(TurnCost(total: 0.12, models: [ModelShare(model: opusID, cost: 0.1), ModelShare(model: sonnetID, cost: 0.02)])) == [
            "About $0.12 for this reply, estimated at list prices", "By model:",
            "· Opus 5.5 $0.10", "· Sonnet 5.5 $0.02",
        ])
    }

    @Test func pricesAreAskedOnceAndAFailureIsNotRetriedAtOnce() async throws {
        let asked = Asked()
        let book = PriceBook(ask: { ids in
            await asked.add(ids)
            return [opusID: ModelPrice(input: 4, output: 20)]
        })
        book.request([opusID, "claude-haiku-5-5"])
        book.request([opusID])
        try await book.drain()
        book.request([opusID, "claude-haiku-5-5"])
        try await book.drain()
        let calls = await asked.calls
        #expect(calls.count == 1)
        #expect(calls.first == [opusID, "claude-haiku-5-5"])
        #expect(book.priceOf(opusID)?.input == 4)
        #expect(book.answered("claude-haiku-5-5"))
        #expect(book.priceOf("claude-haiku-5-5") == nil)

        let failed = Failed()
        let offline = PriceBook(ask: { _ in await failed.bump(); throw PriceOffline() }, clock: { 1_000 })
        offline.request([opusID])
        try await offline.drain()
        #expect(!offline.answered(opusID))
        offline.request([opusID], now: 1_000)
        try await offline.drain()
        #expect(await failed.count == 1)
        offline.request([opusID], now: 61_000)
        try await offline.drain()
        #expect(await failed.count == 2)
    }

    @Test func cancellingAPriceAskDoesNotCountAsAnAnswer() async {
        let calls = Failed()
        let book = PriceBook(ask: { _ in
            let n = await calls.bump()
            if n == 1 { throw PriceCancelled() }
            return [:]
        })
        book.request([opusID])
        var cancelled = false
        do { try await book.drain() } catch is PriceCancelled { cancelled = true } catch { Issue.record(error) }
        #expect(cancelled)
        #expect(!book.answered(opusID))
        book.request([opusID])
        try? await book.drain()
        #expect(await calls.count == 2)
        #expect(book.answered(opusID))
    }

    /// A second request while the first ask is still out must not post the same id again.
    @Test func anInFlightModelIsNotAskedTwice() async throws {
        let gate = Gate()
        let asked = Asked()
        let book = PriceBook(ask: { ids in
            await asked.add(ids)
            if ids == [self.opusID] { await gate.wait() }
            var out: [String: ModelPrice?] = [:]
            for id in ids { out[id] = ModelPrice(input: 4, output: 20) }
            return out
        })
        book.request([opusID])
        async let first: Void = book.drain()
        while await asked.calls.isEmpty { await Task.yield() }
        book.request([opusID, sonnetID])
        await gate.open()
        try await first
        let calls = await asked.calls
        #expect(calls == [[opusID], [sonnetID]])
        #expect(book.priceOf(opusID)?.input == 4)
        #expect(book.priceOf(sonnetID)?.input == 4)
    }

    /// URLSession reports a replaced task as URLError.cancelled. That is not a failure, so a retry inside a minute asks again.
    @Test func aCancelledURLSessionAskIsAskedAgain() async throws {
        let calls = Failed()
        let book = PriceBook(ask: { _ in
            let n = await calls.bump()
            if n == 1 { throw URLError(.cancelled) }
            return [self.opusID: ModelPrice(input: 4, output: 20)]
        }, clock: { 1_000 })
        book.request([opusID])
        do {
            try await book.drain()
            Issue.record("the cancelled ask returned")
        } catch {
            #expect(error.isCancellation)
        }
        #expect(!book.answered(opusID))
        book.request([opusID], now: 1_000)
        try await book.drain()
        #expect(await calls.count == 2)
        #expect(book.priceOf(opusID)?.input == 4)
    }

    @Test func oneFinishedWorkLineIsTheReplysTime() {
        let start = Date(timeIntervalSince1970: 1_763_078_400)
        let work = ReplyTiming(workStart: start, workEnd: start.addingTimeInterval(65))
        let reply = ReplyTiming(replyID: "a#0")
        #expect(replyUsageMillis([ReplyTiming(isUser: true), work, reply])["a#0"] == 65_000)
        let again = ReplyTiming(workStart: start, workEnd: start.addingTimeInterval(1))
        #expect(replyUsageMillis([ReplyTiming(isUser: true), work, again, reply])["a#0"] == nil)
        var live = work
        live.live = true
        #expect(replyUsageMillis([ReplyTiming(isUser: true), live, reply])["a#0"] == nil)
    }
}

private struct PriceOffline: Error {}

private actor Asked {
    var calls: [[String]] = []
    func add(_ ids: [String]) { calls.append(ids) }
}

private actor Failed {
    var count = 0
    func bump() -> Int { count += 1; return count }
}

private actor Gate {
    private var opened = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    func wait() async {
        if opened { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func open() {
        opened = true
        let pending = waiters
        waiters.removeAll()
        pending.forEach { $0.resume() }
    }
}
