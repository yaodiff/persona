import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A Lark chat's paging and read reporting (`LarkPager`), without the chat.
@MainActor struct LarkPagerTests {
    private func msg(_ p: Int, mine: Bool = false, read: Bool? = nil, text: String = "") -> LarkMessage {
        let json = """
        {"id":"m\(p)","position":"\(p)","sender_name":"\(mine ? "我" : "韩梅梅")","is_mine":\(mine),"type":"TEXT","text":"\(text)",
         "recalled":false,"reply_count":0\(read.map { ",\"is_read\":\($0)" } ?? "")}
        """
        return try! JSON.decoder.decode(LarkMessage.self, from: Data(json.utf8))
    }

    private func page(_ ps: [Int], older: Bool = false, newer: Bool? = nil, olderCursor: String? = nil, newerCursor: String? = nil) -> APIClient.LarkMessages {
        APIClient.LarkMessages(messages: ps.map { msg($0) }, hasOlder: older, hasNewer: newer, olderCursor: olderCursor, newerCursor: newerCursor)
    }

    @Test func aPageSetsBothCursors() {
        var p = LarkPager()
        p.replace(with: page([5, 6, 7], older: true, newer: true))
        #expect(p.olderRequest == "5" && p.newerRequest == "7")
        #expect(p.hasOlder && p.hasNewer)
        p.replace(with: page([5, 6, 7], olderCursor: "4x", newerCursor: "8x"))
        #expect(p.olderRequest == "4x" && p.newerRequest == "8x", "the hub's cursors win over positions")
    }

    @Test func olderAndNewerPagesJoinWithoutRepeats() {
        var p = LarkPager()
        p.replace(with: page([5, 6], older: true, newer: true))
        p.prepend(page([3, 4, 5], older: false))
        #expect(p.messages.map(\.position) == ["3", "4", "5", "6"])
        #expect(!p.hasOlder)
        p.append(page([6, 7], newer: false), from: "6")
        #expect(p.messages.map(\.position) == ["3", "4", "5", "6", "7"])
        #expect(!p.hasNewer && p.newerRequest == "7")
        p.append(page([], newer: false), from: "7")
        #expect(p.newerRequest == "7", "an empty page keeps where it asked from")
    }

    @Test func aPollUpdatesInPlaceAndReturnsOnlyWhatIsNewer() {
        var p = LarkPager()
        p.replace(with: page([1, 2]))
        var latest = page([2, 3])
        latest.messages[0].text = "edited"
        let fresh = p.refresh(with: latest)
        #expect(p.messages.map(\.text) == ["", "edited"])
        #expect(fresh.map(\.position) == ["3"])
        p.add(fresh)
        #expect(p.newerRequest == "3")
    }

    @Test func twoPollsAtOnceAddANewMessageOnce() {
        var p = LarkPager()
        p.replace(with: page([1, 2]))
        let a = p.refresh(with: page([2, 3])), b = p.refresh(with: page([2, 3])) // both before either adds
        p.add(a)
        p.add(b)
        #expect(p.messages.map(\.position) == ["1", "2", "3"])
    }

    @Test func readIsReportedUpToTheNewestSeenWithOthersUnreadIDs() {
        var p = LarkPager()
        p.messages = [msg(1), msg(2, mine: true), msg(3), msg(4, read: true), msg(5)]
        p.readCursor = "1"
        let none = p.readReport()
        #expect(none == nil, "nothing seen yet")
        let first = p.see(p.messages[3]), again = p.see(p.messages[3])
        #expect(first && !again, "seen once")
        let r = p.readReport()
        #expect(r?.position == "4")
        #expect(r?.messageIDs == ["m3"], "not mine, not already read, past the cursor, up to the newest seen")
        #expect(p.readCursor == "4")
        let after = p.readReport()
        #expect(after == nil, "nothing new since")
    }

    @Test func openedAtAMessageTheCursorSitsBeforeIt() {
        var p = LarkPager()
        p.replace(with: page([10, 11, 12, 13]))
        p.openRead(at: "11", readPosition: nil) // an older hub: no read position
        #expect(p.readCursor == "10")
        _ = p.see(p.messages[1])
        _ = p.see(p.messages[2])
        let r = p.readReport()
        #expect(r?.position == "12")
        #expect(r?.messageIDs == ["m11", "m12"], "the message it opened at is reported too")
    }

    @Test func openedAtAMessageTheReadPositionWins() {
        var p = LarkPager()
        p.replace(with: page([8, 9, 10, 11, 12]))
        p.openRead(at: "11", readPosition: "8")
        #expect(p.readCursor == "8")
        _ = p.see(p.messages[1])
        #expect(p.readReport()?.messageIDs == ["m9"])
    }

    @Test func aReadPositionPastTheMessageDoesNotSkipIt() {
        var p = LarkPager()
        p.replace(with: page([10, 11, 12]))
        p.openRead(at: "11", readPosition: "12")
        #expect(p.readCursor == "10")
        p.openRead(at: "11", readPosition: "11")
        #expect(p.readCursor == "10", "the message itself is not read yet")
    }

    @Test func theNewMessagesCountTakesOthersPastTheAnchor() {
        #expect(LarkPager.countsAsNew(msg(8), past: "7"))
        #expect(!LarkPager.countsAsNew(msg(7), past: "7"), "at the anchor: read already")
        #expect(!LarkPager.countsAsNew(msg(6), past: "7"))
        #expect(!LarkPager.countsAsNew(msg(9, mine: true), past: "7"), "mine are not new to me")
    }

    @Test func openingAgainDoesNotMoveTheCursorBack() {
        var p = LarkPager()
        p.replace(with: page([10, 11, 12]))
        p.readCursor = "12"
        p.openRead(at: "11", readPosition: "9")
        #expect(p.readCursor == "12")
    }

    @Test func aThreadsOlderPageGoesOnTopWithoutRepeatsOrItsRoot() {
        var p = LarkPager()
        p.replace(with: page([60, 61, 62], older: true, olderCursor: "60").replies(of: "m1"))
        #expect(p.hasOlder && p.olderRequest == "60")
        // The oldest page starts with the root and overlaps what is loaded.
        p.prepend(page([1, 58, 59, 60], older: false, olderCursor: "1").replies(of: "m1"))
        #expect(p.messages.map(\.position) == ["58", "59", "60", "61", "62"])
        #expect(!p.hasOlder, "no more pages once the hub says so")
    }

    @Test func aThreadRefreshAddsNewRepliesOnce() {
        var p = LarkPager()
        p.replace(with: page([1, 10, 11]).replies(of: "m1"))
        let fresh = p.refresh(with: page([1, 10, 11, 12]).replies(of: "m1"))
        #expect(fresh.map(\.position) == ["12"], "the root is not a new reply")
        p.add(fresh)
        p.add(p.refresh(with: page([11, 12]).replies(of: "m1")))
        #expect(p.messages.map(\.position) == ["10", "11", "12"])
    }

    // MARK: Jump to Latest (#1034)

    @Test func movingAwayKeepsTheNewestForGoingBackWithoutTheNetwork() {
        var p = LarkPager()
        p.replace(with: page([40, 41, 42], older: true))
        p.replace(with: page([10, 11], older: true, newer: true)) // a jump to an older message
        let back = p.toTail(cached: [])
        #expect(back)
        #expect(p.messages.map(\.position) == ["40", "41", "42"])
        #expect(!p.hasNewer && p.hasOlder && p.olderRequest == "40" && p.newerRequest == "42")
        let again = p.toTail(cached: [])
        #expect(!again, "already at the newest")
    }

    @Test func theCacheServesWhenItReachesFurtherOrNothingWasKept() {
        var p = LarkPager()
        p.replace(with: page([10, 11], older: true, newer: true)) // opened at a message: nothing kept
        let cached = page([30, 31]).messages
        let fromCache = p.toTail(cached: cached)
        #expect(fromCache && p.messages.map(\.position) == ["30", "31"])

        var q = LarkPager()
        q.replace(with: page([40, 41], older: true))
        q.replace(with: page([10, 11], older: true, newer: true))
        let newer = q.toTail(cached: page([50, 51]).messages)
        #expect(newer && q.messages.last?.position == "51", "the cache went on while away")

        var r = LarkPager()
        r.replace(with: page([10, 11], older: true, newer: true))
        let none = r.toTail(cached: [])
        #expect(!none && r.messages.map(\.position) == ["10", "11"], "nothing here: wait for the hub")
    }

    @Test func theLatestPageJoinsUpOrLeavesAGap() {
        var p = LarkPager()
        p.replace(with: page([40, 41], older: true))
        // Joined up: starts right after the last loaded.
        let next = p.latest(page([42, 43], older: true))
        #expect(next == .fresh(page([42, 43]).messages))
        // Overlapping is joined up too.
        let overlap = p.latest(page([41, 42], older: true))
        #expect(overlap == .fresh(page([42]).messages))
        // The whole chat in one page: nothing can be missing.
        let whole = p.latest(page([60, 61], older: false))
        #expect(whole == .fresh(page([60, 61]).messages))
        // Messages missing in between.
        let gap = p.latest(page([60, 61], older: true))
        #expect(gap == .gap)
        #expect(p.messages.map(\.position) == ["40", "41"], "a gap changes nothing by itself")
    }

    @Test func aGapLoadsTheNewerPagesInOrderFromTheBottom() {
        var p = LarkPager()
        p.replace(with: page([40, 41], older: true))
        p.markGap()
        #expect(p.hasNewer && p.catchingUp && p.newerRequest == "41")
        let back = p.toTail(cached: [])
        #expect(!back, "catching up is the newest window already")
        p.append(page([42, 43], newer: true), from: "41")
        #expect(p.catchingUp)
        p.append(page([44], newer: false), from: "43")
        #expect(!p.hasNewer && !p.catchingUp && p.messages.map(\.position) == ["40", "41", "42", "43", "44"])
    }
}
