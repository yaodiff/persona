import Foundation
import Testing
@testable import Persona

/// Edges of the shared inbox state words and row line that the fixture cases do not reach: empty values, the row's
/// convenience path (which supplies the clock), the old mark yielding to `now`, and the showcase's raw wire JSON.
struct InboxNowEdgeTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private static func item(_ id: String = "matter", now: String? = nil, follow: InboxItem.Follow? = nil) -> InboxItem {
        var it = InboxItem(id: id, source: "chat", kind: "group", account: "chat", accountName: "Team chat",
                           title: "Confirm the launch date", preview: "Can we move it to Friday?", count: 1)
        it.now = now
        it.follow = follow
        return it
    }

    /// An empty value is the same as none: it never leaves an empty slot in the sentence ("Waiting for", "AI couldn't
    /// finish: ", a leading " · ").
    @Test func emptyValuesCountAsAbsent() {
        var it = Self.item(now: "waiting", follow: .init(who: "", next: "", checkAt: nil))
        #expect(InboxNow.text(it, lang: "en", time: nil) == "Waiting for a reply")
        it.follow?.checkAt = Date(timeIntervalSince1970: 1_767_355_200)
        #expect(InboxNow.text(it, lang: "en", time: "09:00") == "Waiting for a reply · look again 09:00")

        var failed = Self.item(now: "failed")
        failed.handling = InboxItem.Handling(state: "failed", error: "")
        #expect(InboxNow.text(failed, lang: "en", time: nil) == "AI couldn't handle it")

        var due = Self.item(now: "check_due", follow: .init(who: nil, next: "", checkAt: nil))
        #expect(InboxNow.text(due, lang: "en", time: nil) == "Time to look again")
        due.follow?.next = "ask whether Friday works"
        #expect(InboxNow.text(due, lang: "en", time: nil) == "Time to look again: ask whether Friday works")

        var noPlace = InboxItem(id: "no-place", source: "chat", kind: "dm", account: "chat", accountName: "c", title: "t", preview: "p", count: 1)
        noPlace.where = ""
        noPlace.sender = "Alex"
        #expect(InboxNow.meta(noPlace, lang: "en") == nil)

        var emptyKey = Self.item(now: "")
        emptyKey.handling = InboxItem.Handling(state: "waiting")
        #expect(InboxNow.text(emptyKey, lang: "en", time: nil) == nil)
    }

    /// `check_due` never grows a time clause: its time already came (the compose rule joins only the next step).
    @Test func checkDueCarriesNoTime() {
        let it = Self.item(now: "check_due", follow: .init(who: "Alex", next: "ask", checkAt: Date(timeIntervalSince1970: 1_767_355_200)))
        #expect(InboxNow.text(it, lang: "en", time: "09:00") == "Time to look again: ask")
    }

    /// The row's convenience supplies the clock itself: a waiting item with a `check_at` shows the formatted time, in
    /// whichever language the app runs in (the tests' scheme is zh-Hans).
    @Test func theRowsConvenienceFillsInTheTime() throws {
        let checkAt = try #require(JSON.parseDate("2026-01-02T09:00:00Z"))
        let clock = try #require(JSON.parseDate("2026-01-02T08:00:00Z"))
        let it = Self.item(now: "waiting", follow: .init(who: nil, next: nil, checkAt: checkAt))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let lang = interfaceTextLocale.identifier
        let expected = InboxNow.text(it, lang: lang, time: "09:00")
        #expect(InboxNow.text(for: it, now: clock, calendar: utc) == expected)
        #expect(expected?.contains("09:00") == true, "the convenience must pass the formatted check_at: \(expected ?? "nil")")
        // Without a check_at there is no time clause at all.
        let plain = Self.item(now: "waiting", follow: .init(who: nil, next: nil, checkAt: nil))
        #expect(InboxNow.text(for: plain, now: clock, calendar: utc) == InboxNow.text(plain, lang: lang, time: nil))
        // A key other than waiting reads no clock.
        let looking = Self.item(now: "looking")
        #expect(InboxNow.text(for: looking, now: clock, calendar: utc) == InboxNow.text(looking, lang: lang, time: nil))
    }

    /// A look-again moment keeps the space between the time and 「再看」 that the shared table carries, and the other
    /// languages keep their own separators.
    @Test func chineseLookAgainKeepsItsSpace() {
        let it = Self.item(now: "waiting", follow: .init(who: nil, next: nil, checkAt: Date(timeIntervalSince1970: 1_767_355_200)))
        #expect(InboxNow.text(it, lang: "zh-Hans", time: "09:00") == "等对方回复 · 09:00 再看")
        // Other languages keep their separator as the table has it.
        #expect(InboxNow.text(it, lang: "en", time: "09:00") == "Waiting for a reply · look again 09:00")
        #expect(InboxNow.text(it, lang: "ja", time: "09:00") == "相手の返信待ち · 09:00に再確認")
    }

    /// With `now`, the row no longer draws the old mark line: an urgent item the AI is looking at says "AI is looking",
    /// where the old mark said it was preparing a suggestion.
    @Test func nowWinsOverTheOldMark() {
        var it = Self.item(now: "looking")
        it.urgent = true
        it.handling = InboxItem.Handling(state: "looking")
        #expect(InboxEventText.mark(it) == .preparing)
        #expect(InboxNow.text(it, lang: "en", time: nil) == "AI is looking")

        var queued = Self.item()
        queued.agent = InboxItem.Agent(state: "queued")
        queued.handling = InboxItem.Handling(state: "looking")
        #expect(InboxNow.text(queued, lang: "en", time: nil) == nil) // no key: today's row
        queued.now = "queued"
        #expect(InboxNow.text(queued, lang: "en", time: nil) == "Queued for the AI")
    }

    /// The showcase's raw wire JSON (the scenario every client draws) decodes, extra manager-only fields included
    /// (`checks`, `due`, `via`), and gives the zh-Hans waiting sentence with its named person.
    @Test func showcaseItemDecodes() throws {
        struct Showcase: Decodable {
            var items: [InboxItem]
            var detail: String
            var now_clock: String
        }
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/inbox/showcase.json"))
        let showcase = try JSON.decoder.decode(Showcase.self, from: data)
        let detail = try #require(showcase.items.first { $0.id == showcase.detail })
        #expect(detail.where == "订单群")
        #expect(detail.now == "waiting")
        #expect(detail.follow?.who == "张三确认新报价")
        #expect(detail.follow?.next == "问更正后的报价行不行")
        let checkAt = try #require(detail.follow?.checkAt)
        let clock = try #require(JSON.parseDate(showcase.now_clock))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let time = InboxNow.time(checkAt, lang: "zh-Hans", now: clock, calendar: utc)
        #expect(time == "明天 09:00")
        #expect(InboxNow.text(detail, lang: "zh-Hans", time: time) == "等张三确认新报价 · 明天 09:00 再看：问更正后的报价行不行")
        #expect(InboxNow.meta(detail, lang: "zh-Hans") == "订单群 · 张三：好的，我看一下新的")
        // Its follow_up_ask sibling reads its counters without a word for them.
        let ask = try #require(showcase.items.first { $0.now == "follow_up_ask" })
        #expect(InboxNow.text(ask, lang: "zh-Hans", time: nil) == "看过 3 次还没动静，还要跟吗？")
    }
}
