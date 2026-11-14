import Foundation

/// What a finished exchange used and what it cost (#5470, the web's `lib/turn-usage.ts` and `lib/turn-cost.ts` from #5426).
/// A price is the hub's built-in catalog, USD per million tokens. An exchange has a cost only when every model that
/// used tokens has a price for every kind it used.

/// One model's tokens in an exchange. `input` excludes the cache. `fast` is Claude Code's fast speed, which has no price.
nonisolated struct ModelTokens: Equatable, Sendable {
    var model: String
    var fast: Bool = false
    var input: Int64 = 0
    var output: Int64 = 0
    var cacheRead: Int64 = 0
    var cacheWrite: Int64 = 0
    var cacheWrite1h: Int64 = 0
}

/// An exchange's tokens by model, in the order each model first used tokens, and whether it is the page's first round.
nonisolated struct UsageSummary: Equatable, Sendable {
    var models: [ModelTokens]
    var first: Bool = false
}

/// The tokens an exchange's items carry, in order, then each subagent it launched. `first`: no earlier exchange used any.
nonisolated struct TurnUsage: Equatable, Sendable {
    var entries: [TokenUsage]
    var first: Bool = false
}

/// One exchange after reports sit in the exchange that launched them. `answerID` is its last reply.
nonisolated struct ExchangeUsage: Equatable, Sendable {
    var usage: TurnUsage?
    var answerID: String?
    var cut: Bool = false
}

private struct Counts {
    var model: String
    var fast: Bool
    var input: Int64 = 0
    var output: Int64 = 0
    var cacheRead: Int64 = 0
    var cacheWrite: Int64 = 0
    var cacheWrite1h: Int64 = 0

    mutating func add(_ u: TokenUsage, max: Bool) {
        input = merge(input, u.input, max: max)
        output = merge(output, u.output, max: max)
        cacheRead = merge(cacheRead, u.cacheRead, max: max)
        cacheWrite = merge(cacheWrite, u.cacheWrite, max: max)
        cacheWrite1h = merge(cacheWrite1h, u.cacheWrite1h, max: max)
    }

    mutating func add(_ other: Counts) {
        input += other.input
        output += other.output
        cacheRead += other.cacheRead
        cacheWrite += other.cacheWrite
        cacheWrite1h += other.cacheWrite1h
    }

    func tokens() -> ModelTokens {
        ModelTokens(model: model, fast: fast, input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite,
                    cacheWrite1h: cacheWrite1h)
    }

    var used: Bool { input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0 || cacheWrite1h > 0 }

    private func merge(_ current: Int64, _ value: Int64, max: Bool) -> Int64 {
        let n: Int64 = value > 0 ? value : 0
        return max ? Swift.max(current, n) : current + n
    }
}

/// What an exchange's entries add up to by model and speed. A report counts once per model and speed, at the largest
/// count of each kind; the rest are summed. Models with no tokens are left out.
func modelTokens(_ entries: [TokenUsage]) -> [ModelTokens] {
    var reports: [String: Counts] = [:]
    var reportOrder: [String] = []
    var models: [String: Counts] = [:]
    var modelOrder: [String] = []
    for u in entries {
        let model = (u.model ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "" : bareModel(u.model ?? "")
        let key = model + "\n" + (u.fast ? "1" : "0")
        if models[key] == nil {
            models[key] = Counts(model: model, fast: u.fast)
            modelOrder.append(key)
        }
        if u.report {
            let reportKey = (u.id ?? "") + "\n" + key
            if reports[reportKey] == nil {
                reports[reportKey] = Counts(model: model, fast: u.fast)
                reportOrder.append(reportKey)
            }
            if var row = reports[reportKey] {
                row.add(u, max: true)
                reports[reportKey] = row
            }
        } else if var row = models[key] {
            row.add(u, max: false)
            models[key] = row
        }
    }
    for reportKey in reportOrder {
        guard let report = reports[reportKey], let nl = reportKey.firstIndex(of: "\n") else { continue }
        let modelKey = String(reportKey[reportKey.index(after: nl)...])
        if var row = models[modelKey] {
            row.add(report)
            models[modelKey] = row
        }
    }
    return modelOrder.compactMap { key in models[key].flatMap { $0.used ? $0.tokens() : nil } }
}

/// The usage a reply carries, summed by model. Nil when it used nothing.
func summarize(_ usage: TurnUsage?) -> UsageSummary? {
    let models = usage.map { modelTokens($0.entries) } ?? []
    return models.isEmpty ? nil : UsageSummary(models: models, first: usage?.first == true)
}

/// All of an exchange's tokens. `total` is input, output, cache reads and writes. `hit` is nil without any input.
nonisolated struct ExchangeTotals: Equatable, Sendable {
    var input: Int64
    var output: Int64
    var cacheRead: Int64
    var cacheWrite: Int64
    var cacheWrite1h: Int64
    var total: Int64
    var hit: Double?
}

func usageTotals(_ models: [ModelTokens]) -> ExchangeTotals {
    var input: Int64 = 0, output: Int64 = 0, cacheRead: Int64 = 0, cacheWrite: Int64 = 0, cacheWrite1h: Int64 = 0
    for m in models {
        input += m.input
        output += m.output
        cacheRead += m.cacheRead
        cacheWrite += m.cacheWrite
        cacheWrite1h += m.cacheWrite1h
    }
    let prompt = input + cacheRead + cacheWrite
    return ExchangeTotals(input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite, cacheWrite1h: cacheWrite1h,
                       total: prompt + output, hit: prompt > 0 ? Double(cacheRead) / Double(prompt) : nil)
}

/// A model's cost in USD at `price` (per million tokens). Null when a kind it used has no price, or for the fast speed.
func modelCost(_ m: ModelTokens, _ price: ModelPrice?) -> Double? {
    guard let price, !m.fast else { return nil }
    if (m.input > 0 && price.input == nil) || (m.output > 0 && price.output == nil) ||
        (m.cacheRead > 0 && price.cacheRead == nil) || (m.cacheWrite > 0 && price.cacheWrite == nil) { return nil }
    let write1h = min(m.cacheWrite1h, m.cacheWrite)
    let write1hPrice = price.cacheWrite1h ?? price.cacheWrite ?? 0
    return (Double(m.input) * (price.input ?? 0) + Double(m.output) * (price.output ?? 0) +
        Double(m.cacheRead) * (price.cacheRead ?? 0) + Double(m.cacheWrite - write1h) * (price.cacheWrite ?? 0) +
        Double(write1h) * write1hPrice) / 1e6
}

nonisolated struct ModelShare: Equatable, Sendable {
    var model: String
    var cost: Double
}

nonisolated struct TurnCost: Equatable, Sendable {
    var total: Double
    var models: [ModelShare]
}

/// The exchange's cost, only when every model that used tokens has one. A partial sum is never the whole.
func turnCost(_ models: [ModelTokens], price: (String) -> ModelPrice?) -> TurnCost? {
    var out: [ModelShare] = []
    for m in models {
        guard !m.model.isEmpty, let cost = modelCost(m, price(m.model)) else { return nil }
        if let i = out.firstIndex(where: { $0.model == m.model }) { out[i].cost += cost }
        else { out.append(ModelShare(model: m.model, cost: cost)) }
    }
    return TurnCost(total: out.reduce(0) { $0 + $1.cost }, models: out.filter { $0.cost > 0 })
}

/// Two decimals, "<$0.01" under a cent, "$0.00" for zero.
func formatCost(_ usd: Double) -> String {
    if usd == 0 { return "$0.00" }
    if usd >= 0.01 { return "$" + String(format: "%.2f", locale: posix, usd) }
    return "<$0.01"
}

/// A whole percent when it is close to whole, else one decimal.
func formatPercent(_ v: Double?) -> String? {
    guard let v, v.isFinite else { return nil }
    let pct = min(100, max(0, v * 100))
    let whole = pct.rounded()
    if abs(pct - whole) < 0.05 { return "\(Int(whole))%" }
    var text = String(format: "%.1f", locale: posix, pct)
    if text.hasSuffix(".0") { text.removeLast(2) }
    return text + "%"
}

private let lowCacheMinInput: Int64 = 50_000
private let lowCacheMaxHit = 0.2

/// A lot of input that mostly missed the cache. Never for a first round, which has nothing cached.
func lowCache(_ usage: UsageSummary) -> Bool {
    if usage.first { return false }
    let s = usageTotals(usage.models)
    return s.input + s.cacheRead + s.cacheWrite >= lowCacheMinInput && s.hit != nil && (s.hit ?? 1) < lowCacheMaxHit
}

/// Hour-and-above duration words. Under an hour every language uses "6s", "1m 5s" or "2m".
/// The translated forms come from the catalog; English is the default the tests use.
struct UsageDurationText {
    var hoursMinutes: (Int64, Int64) -> String
    var daysHoursMinutes: (Int64, Int64, Int64) -> String

    static let english = UsageDurationText(
        hoursMinutes: { hours, minutes in "\(hours)h \(minutes)m" },
        daysHoursMinutes: { days, hours, minutes in "\(days)d \(hours)h \(minutes)m" }
    )
}

/// How a duration reads on the card. Under an hour it is the same in every language ("1m 5s").
func usageDuration(_ ms: Int64, text: UsageDurationText = .english) -> String {
    let seconds = max(1, Int64((Double(ms) / 1000).rounded()))
    if seconds < 60 { return "\(seconds)s" }
    if seconds >= 3600 {
        let days = seconds / 86_400
        let hours = (seconds % 86_400) / 3600
        let minutes = (seconds % 3600) / 60
        if days > 0 { return text.daysHoursMinutes(days, hours, minutes) }
        return text.hoursMinutes(hours, minutes)
    }
    let minutes = seconds / 60
    let rest = seconds % 60
    return rest != 0 ? "\(minutes)m \(rest)s" : "\(minutes)m"
}

/// "23.4k", "950", "1.2M": the web's `compactTokens`, in every language.
func compactTokens(_ count: Int64) -> String {
    let v = max(Int64(0), count)
    if v < 1000 { return String(v) }
    let steps: [(Double, String)] = [(1e3, "k"), (1e6, "M"), (1e9, "B")]
    for (div, unit) in steps where Double(v) / div < 999.95 || unit == "B" {
        return String(format: "%.1f", locale: posix, Double(v) / div) + unit
    }
    return String(v)
}

/// The numbers the card's lines are filled with. `cost` is set only when the price is above zero.
nonisolated struct ReplyUsageCard: Equatable, Sendable {
    var cost: String?
    var modelCosts: [(String, String)]
    var total: String
    var input: String
    var output: String
    var cacheRead: String
    var cacheWrite: String
    var rate: String?
    var duration: String?
    var singleModel: String?
    var modelCount: Int
    var lowCache: Bool

    static func == (a: Self, b: Self) -> Bool {
        a.cost == b.cost && a.modelCosts.elementsEqual(b.modelCosts, by: ==) && a.total == b.total && a.input == b.input
            && a.output == b.output && a.cacheRead == b.cacheRead && a.cacheWrite == b.cacheWrite && a.rate == b.rate
            && a.duration == b.duration && a.singleModel == b.singleModel && a.modelCount == b.modelCount
            && a.lowCache == b.lowCache
    }
}

func usageCard(_ usage: UsageSummary, cost: TurnCost?, ms: Int64? = nil, text: UsageDurationText = .english) -> ReplyUsageCard {
    let totals = usageTotals(usage.models)
    let priced = cost.flatMap { $0.total > 0 ? $0 : nil }
    let breakdown = priced != nil && (priced?.models.count ?? 0) >= 2
    let names = modelNames(usage.models)
    return ReplyUsageCard(
        cost: priced.map { formatCost($0.total) },
        modelCosts: breakdown ? (priced?.models.map { (replyModelName($0.model), formatCost($0.cost)) } ?? []) : [],
        total: compactTokens(totals.total), input: compactTokens(totals.input), output: compactTokens(totals.output),
        cacheRead: compactTokens(totals.cacheRead), cacheWrite: compactTokens(totals.cacheWrite),
        rate: formatPercent(totals.hit),
        duration: (ms != nil && (ms ?? 0) > 0) ? usageDuration(ms ?? 0, text: text) : nil,
        singleModel: !breakdown && names.count == 1 ? names[0] : nil,
        modelCount: breakdown ? 0 : names.count,
        lowCache: lowCache(usage))
}

/// The card's lines in English, in the web's order. The apps draw the same lines from their own strings.
func usageLinesEn(_ usage: UsageSummary, _ cost: TurnCost?, ms: Int64? = nil) -> [String] {
    let card = usageCard(usage, cost: cost, ms: ms)
    var lines: [String] = []
    if let cost = card.cost {
        lines.append("Request details")
        lines.append("Token value this turn: \(cost)")
    }
    if !card.modelCosts.isEmpty {
        lines.append("By model:")
        for (name, share) in card.modelCosts { lines.append("· \(name) \(share)") }
    }
    lines.append("Tokens: \(card.total) total · input \(card.input) · output \(card.output)")
    if let rate = card.rate {
        lines.append("Cache breakdown: read \(card.cacheRead) · write \(card.cacheWrite) · hit rate \(rate)")
    } else {
        lines.append("Cache breakdown: read \(card.cacheRead) · write \(card.cacheWrite)")
    }
    if let duration = card.duration { lines.append("Time \(duration)") }
    if let name = card.singleModel { lines.append("Model: \(name)") }
    else if card.modelCount > 1 { lines.append("Model: \(card.modelCount) models") }
    if card.lowCache { lines.append("Suggestion: Cache hit rate is low; more context was billed again this turn") }
    if card.cost == nil { lines.append("Cost unavailable for this turn; showing usage only") }
    return lines
}

/// A Persona conversation's lines: the cost in plain words, and each model's share when there were several.
func personaLinesEn(_ cost: TurnCost) -> [String] {
    var lines = ["About \(formatCost(cost.total)) for this reply, estimated at list prices"]
    if cost.models.count >= 2 {
        lines.append("By model:")
        for m in cost.models { lines.append("· \(replyModelName(m.model)) \(formatCost(m.cost))") }
    }
    return lines
}

enum ReplyLabelKind { case worth, tokens, approx }

struct ReplyLabel: Equatable {
    var kind: ReplyLabelKind
    var amount: String
}

/// The text after the reply's time. A Persona conversation says nothing without a price.
func replyLabel(persona: Bool, usage: UsageSummary, cost: TurnCost?) -> ReplyLabel? {
    let priced = cost.flatMap { $0.total > 0 ? $0 : nil }
    if persona && priced == nil { return nil }
    let total = usageTotals(usage.models).total
    if priced == nil && total <= 0 { return nil }
    if let priced { return ReplyLabel(kind: persona ? .approx : .worth, amount: formatCost(priced.total)) }
    return ReplyLabel(kind: .tokens, amount: compactTokens(total))
}

/// Model ids worth asking the catalog about. Empty when a model has no name or ran at the fast speed.
func catalogIds(_ summary: UsageSummary) -> [String] {
    guard summary.models.allSatisfy({ !$0.model.isEmpty && !$0.fast }) else { return [] }
    var seen: [String] = []
    for m in summary.models where !seen.contains(m.model) { seen.append(m.model) }
    return seen
}

// MARK: - Placing reports

private struct Seg {
    var entry: String?
    var userIDs: [String] = []
    var items: [Item] = []
    var entries: [TokenUsage] = []
    var answerID: String?
    var cut = false
    var first = false
    func usage() -> TurnUsage? { entries.isEmpty ? nil : TurnUsage(entries: entries, first: first) }
}

/// What each exchange carries on its own items, before reports are placed and before the first round is marked.
func segmentUsage(_ items: [Item]) -> [TurnUsage?] {
    split(readingOrder(items)).map { $0.usage() }
}

/// What each exchange used once reports sit in the exchange that launched them, and which reply the label belongs to.
func chatUsage(_ items: [Item]) -> [ExchangeUsage] {
    let ordered = readingOrder(items)
    var segs = split(ordered)
    placeReports(&segs, ordered)
    if let i = segs.firstIndex(where: { !$0.entries.isEmpty }) { segs[i].first = true }
    return segs.map { ExchangeUsage(usage: $0.usage(), answerID: $0.answerID, cut: $0.cut) }
}

private func readingOrder(_ items: [Item]) -> [Item] {
    items.enumerated().sorted { a, b in
        if a.element.seq != b.element.seq { return a.element.seq < b.element.seq }
        let pa = blockPart(a.element.id), pb = blockPart(b.element.id)
        if pa != pb { return pa < pb }
        return a.offset < b.offset
    }.map(\.element)
}

private func blockPart(_ id: String) -> Int {
    guard let hash = id.lastIndex(of: "#") else { return 0 }
    return Int(id[id.index(after: hash)...]) ?? 0
}

private func split(_ items: [Item]) -> [Seg] {
    let attached = attachedAgentNotices(items)
    var out: [Seg] = []
    var current: Int?
    for it in items {
        if attached.contains(it.id) || viewFeedbackOnly(it) { continue }
        if isUserMessage(it) {
            let entry = String(it.id.split(separator: "#", maxSplits: 1).first ?? Substring(it.id))
            if let open = current, out[open].entry == entry, out[open].items.isEmpty {
                out[open].userIDs.append(it.id)
            } else {
                var seg = Seg()
                seg.entry = entry
                seg.userIDs.append(it.id)
                out.append(seg)
                current = out.count - 1
            }
            continue
        }
        if current == nil {
            out.append(Seg())
            current = out.count - 1
        }
        guard let k = current else { continue }
        out[k].items.append(it)
        let own = itemUsage(it, reports: false)
        if !own.isEmpty { out[k].entries.append(contentsOf: own) }
        // A whitespace-only reply is not the answer (the web's `text?.trim()`).
        if it.role == "assistant", it.kind == .text, let text = it.text,
           !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { out[k].answerID = it.id }
    }
    for i in out.indices {
        let at = out[i].items.filter { !isInterrupt($0) }.map(\.seq).max() ?? Int.min
        out[i].cut = out[i].items.contains { isInterrupt($0) && $0.seq > at }
    }
    return out
}

private struct ReportAcc {
    var id: String
    var model: String?
    var fast: Bool
    var input: Int64 = 0
    var output: Int64 = 0
    var cacheRead: Int64 = 0
    var cacheWrite: Int64 = 0
    var cacheWrite1h: Int64 = 0
    var k = 0
    var launched = false

    mutating func take(_ u: TokenUsage) {
        input = max(input, max(0, u.input))
        output = max(output, max(0, u.output))
        cacheRead = max(cacheRead, max(0, u.cacheRead))
        cacheWrite = max(cacheWrite, max(0, u.cacheWrite))
        cacheWrite1h = max(cacheWrite1h, max(0, u.cacheWrite1h))
    }

    func toUsage() -> TokenUsage {
        TokenUsage(id: id, model: model, input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite,
                   cacheWrite1h: cacheWrite1h, fast: fast)
    }
}

private func placeReports(_ segs: inout [Seg], _ items: [Item]) {
    guard items.contains(where: { !itemUsage($0, reports: true).isEmpty }) else { return }
    var at: [String: Int] = [:]
    func own(_ id: String?, _ k: Int) {
        guard let id, !id.isEmpty, at[id] == nil else { return }
        at[id] = k
    }
    var lastResult: [String: String] = [:]
    for it in items {
        if it.kind == .toolResult, let call = it.toolUseID, !call.isEmpty { lastResult[call] = it.id }
    }
    for (k, seg) in segs.enumerated() {
        for id in seg.userIDs { own(id, k) }
        let leadingWake = seg.userIDs.isEmpty && seg.items.first.map(isWake) == true
        for it in seg.items {
            if isInterrupt(it) || it.kind == .turnDuration || it.kind == .toolResult { continue }
            if leadingWake, it.id == seg.items.first?.id { continue }
            if it.kind == .text, (it.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { continue }
            own(it.id, k)
            if it.kind == .toolUse, let call = it.toolUseID, !call.isEmpty {
                own("call:" + call, k)
                own(lastResult[call], k)
            }
        }
        own(seg.answerID, k)
    }
    let calls = taskCalls(items)
    var reports: [String: ReportAcc] = [:]
    var reportOrder: [String] = []
    var here = 0
    for it in items {
        if let owned = at[it.id] { here = owned }
        let list = itemUsage(it, reports: true)
        if list.isEmpty { continue }
        let launch = launchIndex(it, calls, at)
        for u in list {
            guard let id = u.id, !id.isEmpty else { continue }
            let key = id + "\n" + (u.model ?? "") + "\n" + (u.fast ? "1" : "0")
            if reports[key] == nil {
                reports[key] = ReportAcc(id: id, model: (u.model ?? "").isEmpty ? nil : u.model, fast: u.fast, k: here)
                reportOrder.append(key)
            }
            if var acc = reports[key] {
                acc.take(u)
                // A launch replaces where the report was first seen, and a later earlier launch wins.
                if let launch, !acc.launched || launch < acc.k {
                    acc.k = launch
                    acc.launched = true
                }
                reports[key] = acc
            }
        }
    }
    for key in reportOrder {
        guard let acc = reports[key], segs.indices.contains(acc.k) else { continue }
        segs[acc.k].entries.append(acc.toUsage())
    }
}

private func launchIndex(_ it: Item, _ calls: [String: String], _ at: [String: Int]) -> Int? {
    for call in [it.toolUseID, it.task?.toolUseID, calls[it.id]].compactMap({ $0 }).filter({ !$0.isEmpty }) {
        if let k = at["call:" + call] { return k }
    }
    return nil
}

private func itemUsage(_ it: Item, reports: Bool) -> [TokenUsage] {
    ((it.usage ?? []) + (it.fold?.usage ?? [])).filter { $0.report == reports }
}

private func isUserMessage(_ it: Item) -> Bool {
    it.role == "user" && (it.kind == .text || (it.kind == .image && it.toolUseID == nil))
}

private func isWake(_ it: Item) -> Bool { it.kind == .notice && it.wake == true }

/// The transcript's "Interrupted" notice. Its tokens belong to the exchange it stopped.
private func isInterrupt(_ it: Item) -> Bool {
    it.kind == .notice && it.task == nil && (it.text ?? "").contains("中断") // i18n-ignore: the transcript's interrupt notice
}

private func viewFeedbackOnly(_ it: Item) -> Bool {
    guard it.role == "user", it.kind == .text, let text = it.text, text.contains("<persona-view-errors") else { return false }
    let rest = text.replacingOccurrences(of: #"<persona-view-errors\b[^>]*>[\s\S]*?</persona-view-errors>\s*"#, with: "",
                                         options: .regularExpression)
    return rest.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
}

private func launchedTaskID(_ text: String?) -> String? {
    guard let text else { return nil }
    return firstMatch(text, #"(?:running in background with ID:?|agentId:|Monitor started \(task:?|moved to the background \(ID:?)\s*([A-Za-z0-9_-]+)"#)?[1]
}

private func taskCalls(_ items: [Item]) -> [String: String] {
    var byTask: [String: String] = [:]
    for it in items {
        guard let call = it.toolUseID ?? it.task?.toolUseID, !call.isEmpty else { continue }
        let task = it.task?.taskID ?? (it.kind == .toolResult ? launchedTaskID(it.text) : nil)
        if let task { byTask[task] = call }
    }
    var calls: [String: String] = [:]
    for it in items {
        guard let task = it.task else { continue }
        let call = it.toolUseID ?? task.toolUseID ?? task.taskID.flatMap { byTask[$0] }
        if let call, !call.isEmpty { calls[it.id] = call }
    }
    return calls
}

/// Agent notices whose launching call is on the page: not a line of their own, and not counted where they sit.
private func attachedAgentNotices(_ items: [Item]) -> Set<String> {
    let launches = Set(items.compactMap { it -> String? in
        guard it.kind == .toolUse, it.toolName == "Agent" || it.toolName == "Task" else { return nil }
        return it.toolUseID
    })
    let calls = taskCalls(items)
    return Set(items.compactMap { it -> String? in
        guard it.kind == .notice, it.task?.kind == "agent", let call = calls[it.id], launches.contains(call) else { return nil }
        return it.id
    })
}

/// The work time under a reply, only when the exchange has exactly one finished work span. Keyed by the reply's id.
struct ReplyTiming: Sendable {
    var isUser = false
    var replyID: String?
    var live = false
    var workStart: Date?
    var workEnd: Date?
}

func replyUsageMillis(_ lines: [ReplyTiming]) -> [String: Int64] {
    var out: [String: Int64] = [:]
    var pending: [Int64] = []
    for line in lines {
        if line.isUser { pending.removeAll(); continue }
        if let id = line.replyID {
            if pending.count == 1 { out[id] = pending[0] }
            pending.removeAll()
            continue
        }
        if !line.live, let start = line.workStart, let end = line.workEnd {
            let ms = Int64(end.timeIntervalSince(start) * 1000)
            if ms > 0 { pending.append(ms) }
        }
    }
    return out
}

// MARK: - Usage key

/// The usage as one string. The same usage is the same string. `first` is omitted when it is false.
func usageKey(_ usage: TurnUsage?) -> String? {
    guard let usage else { return nil }
    var object: [String: Any] = ["entries": usage.entries.map(entryObject)]
    if usage.first { object["first"] = true }
    guard let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]) else { return nil }
    return String(data: data, encoding: .utf8)
}

func parseUsage(_ key: String?) -> UsageSummary? {
    guard let key, !key.isEmpty else { return nil }
    guard let usage = try? decodeUsage(key) else { return nil }
    return summarize(usage)
}

func decodeUsage(_ key: String) throws -> TurnUsage {
    guard let data = key.data(using: .utf8),
          let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
          let rows = object["entries"] as? [[String: Any]] else { throw UsageKeyError.entries }
    let entries = rows.map { row in
        TokenUsage(id: row["id"] as? String, model: row["model"] as? String, input: jsonInt(row["in"]), output: jsonInt(row["out"]),
                   cacheRead: jsonInt(row["cache_read"]), cacheWrite: jsonInt(row["cache_write"]),
                   cacheWrite1h: jsonInt(row["cache_write_1h"]), fast: (row["fast"] as? Bool) == true)
    }
    return TurnUsage(entries: entries, first: (object["first"] as? Bool) == true)
}

private enum UsageKeyError: Error { case entries }

private func entryObject(_ u: TokenUsage) -> [String: Any] {
    var row: [String: Any] = [:]
    if let id = u.id, !id.isEmpty { row["id"] = id }
    if let model = u.model, !model.isEmpty { row["model"] = model }
    if u.input > 0 { row["in"] = NSNumber(value: u.input) }
    if u.output > 0 { row["out"] = NSNumber(value: u.output) }
    if u.cacheRead > 0 { row["cache_read"] = NSNumber(value: u.cacheRead) }
    if u.cacheWrite > 0 { row["cache_write"] = NSNumber(value: u.cacheWrite) }
    if u.cacheWrite1h > 0 { row["cache_write_1h"] = NSNumber(value: u.cacheWrite1h) }
    if u.fast { row["fast"] = true }
    return row
}

private func jsonInt(_ value: Any?) -> Int64 {
    let n: Int64
    if let v = value as? Int64 { n = v }
    else if let v = value as? Int { n = Int64(v) }
    else if let v = value as? NSNumber { n = v.int64Value }
    else { return 0 }
    return n > 0 ? n : 0
}

// MARK: - Prices

/// The catalog ask was cancelled. It is not a failure, and the ids are asked again.
struct PriceCancelled: Error {}

/// Prices the catalog has already answered. A missing id is asked once, remembered as null when the catalog has none,
/// and asked again a minute after a failure.
final class PriceBook: @unchecked Sendable {
    private let ask: @MainActor ([String]) async throws -> [String: ModelPrice?]
    private let clock: () -> Int64
    private var prices: [String: ModelPrice?] = [:]
    private var asking: Set<String> = []
    private var failed: [String: Int64] = [:]
    private var waiting: [String] = []
    /// One fetch at a time. A chat's task is replaced when the transcript arrives; the next drain waits for this one
    /// instead of dropping the ids it still holds.
    private var flushing = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    /// Called on the main actor after a fetch stores answers, including when the task that started it was replaced.
    var onUpdate: (@MainActor () -> Void)?
    private let retryMS: Int64 = 60_000
    private let batch = 100

    init(ask: @escaping @MainActor ([String]) async throws -> [String: ModelPrice?], clock: @escaping () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) }) {
        self.ask = ask
        self.clock = clock
    }

    func request(_ ids: [String], now: Int64? = nil) {
        let now = now ?? clock()
        for id in ids {
            if id.isEmpty || prices.keys.contains(id) { continue }
            if let at = failed[id], now - at < retryMS { continue }
            // Already queued: keep it queued. Already in flight: do not queue a second POST.
            if waiting.contains(id) { asking.insert(id); continue }
            if asking.contains(id) { continue }
            asking.insert(id)
            waiting.append(id)
        }
    }

    func drain() async throws {
        if flushing {
            await withCheckedContinuation { waiters.append($0) }
            try Task.checkCancellation()
        }
        try await flushWaiting()
    }

    /// Asks for whatever is queued. A second caller waits (`flushing`) and then asks for anything queued during the wait.
    private func flushWaiting() async throws {
        if waiting.isEmpty || flushing { return }
        flushing = true
        defer {
            flushing = false
            let pending = waiters
            waiters.removeAll()
            pending.forEach { $0.resume() }
        }
        while !waiting.isEmpty {
            let all = waiting
            waiting.removeAll()
            try await fetch(all)
            onUpdate?()
        }
    }

    private func fetch(_ all: [String]) async throws {
        do {
            var start = 0
            while start < all.count {
                let chunk = Array(all[start..<min(start + batch, all.count)])
                start += chunk.count
                defer { for id in chunk { asking.remove(id) } }
                do {
                    let found = try await ask(chunk)
                    for id in chunk {
                        // A nil subscript assignment removes the key. A missing catalog price is an answer.
                        prices.updateValue(found[id] ?? nil, forKey: id)
                        failed.removeValue(forKey: id)
                    }
                } catch {
                    if isPriceCancellation(error) { throw error }
                    let now = clock()
                    for id in chunk { failed[id] = now }
                }
            }
        } catch {
            // A cancelled request, including URLError.cancelled, is not a failure. The unanswered ids go back on
            // the queue for the drain that is waiting, and they are not remembered as failed for a minute.
            if isPriceCancellation(error) {
                for id in all {
                    asking.remove(id)
                    if !prices.keys.contains(id), !waiting.contains(id) { waiting.append(id) }
                }
                throw error
            }
        }
    }

    private func isPriceCancellation(_ error: Error) -> Bool {
        error is PriceCancelled || error.isCancellation
    }

    func priceOf(_ id: String) -> ModelPrice? { prices[id] ?? nil }
    func answered(_ id: String) -> Bool { prices.keys.contains(id) }
    func ready(_ ids: [String]) -> Bool { ids.allSatisfy { answered($0) || failed[$0] != nil } && ids.allSatisfy { !asking.contains($0) } }
}

// MARK: - Names

/// A model id without the gateway prefix or the snapshot date, as the web compares it.
func bareModel(_ id: String) -> String {
    let trimmed = id.trimmingCharacters(in: .whitespacesAndNewlines)
    var s = trimmed.split(separator: "/").last.map(String.init) ?? trimmed
    s = replace(s, #"^(?:[a-z]+\.)?anthropic\."#, caseInsensitive: true)
    s = replace(s, #"@[^@]*$"#)
    s = replace(s, #"-v\d+:\d+(?=(?:\[[^\]]*])?$)"#, caseInsensitive: true)
    s = replace(s, #"-\d{8}(?=(?:\[[^\]]*])?$)"#)
    return s.isEmpty ? trimmed : s
}

/// How a reply's model is named ("claude-opus-5-5" is "Opus 5.5").
func replyModelName(_ id: String) -> String {
    let bare = bareModel(id)
    let short = claudeShort(bare)
    if !short.isEmpty, short != bare { return short.prefix(1).uppercased() + short.dropFirst() }
    return gptName(bare)
}

private func modelNames(_ models: [ModelTokens]) -> [String] {
    var names: [String] = []
    for m in models where !m.model.isEmpty {
        let name = replyModelName(m.model)
        if !names.contains(name) { names.append(name) }
    }
    return names
}

private func claudeShort(_ id: String) -> String {
    let raw = id.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let m = firstMatch(raw, #"^claude-([a-z]+)((?:-\d{1,2})*)(?:-\d{8})?(\[1m])?$"#, caseInsensitive: true) else { return raw }
    let version = m[2].split(separator: "-").filter { !$0.isEmpty }.joined(separator: ".")
    let window = m[3].isEmpty ? "" : "1M"
    return [m[1].lowercased(), version, window].filter { !$0.isEmpty }.joined(separator: " ")
}

private func gptName(_ label: String) -> String {
    guard let m = firstMatch(label, #"^gpt-([a-z0-9]+(?:\.[a-z0-9]+)*(?:-[a-z0-9]+(?:\.[a-z0-9]+)*)*)$"#, caseInsensitive: true) else {
        return label
    }
    return m[1].split(separator: "-").enumerated().map { index, part in
        let text = String(part)
        guard index > 0, let first = text.first, first.isLetter else { return text }
        return String(first).uppercased() + text.dropFirst().lowercased()
    }.joined(separator: " ")
}

private let posix = Locale(identifier: "en_US_POSIX")

private func replace(_ text: String, _ pattern: String, caseInsensitive: Bool = false) -> String {
    text.replacingOccurrences(of: pattern, with: "", options: caseInsensitive ? [.regularExpression, .caseInsensitive] : .regularExpression)
}

private func firstMatch(_ text: String, _ pattern: String, caseInsensitive: Bool = false) -> [String]? {
    guard let re = try? NSRegularExpression(pattern: pattern, options: caseInsensitive ? .caseInsensitive : []) else { return nil }
    let range = NSRange(text.startIndex..., in: text)
    guard let match = re.firstMatch(in: text, range: range) else { return nil }
    return (0..<match.numberOfRanges).map { i in
        guard let r = Range(match.range(at: i), in: text) else { return "" }
        return String(text[r])
    }
}
