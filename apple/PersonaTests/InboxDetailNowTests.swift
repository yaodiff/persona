import Foundation
import Testing
@testable import Persona

/// The second detail batch: the shared header line, now-only event pages and the one status-symbol mapping used by row and detail.
@MainActor
struct InboxDetailNowTests {
    private func item(now: String? = nil) -> InboxItem {
        var item = InboxItem(id: "matter", source: "chat", kind: "group", account: "chat", accountName: "Team chat",
                             title: "A matter", preview: "Latest line", count: 1)
        item.now = now
        return item
    }

    @Test func statusSymbolsFollowTheSharedRule() {
        #expect(InboxNow.symbol(for: item(now: "waiting")) == .time)
        for key in ["queued", "looking", "agent_working"] {
            #expect(InboxNow.symbol(for: item(now: key)) == .spinner, "\(key)")
        }
        for key in ["confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "needs_you"] {
            #expect(InboxNow.symbol(for: item(now: key)) == .needsYou, "\(key)")
        }
        #expect(InboxNow.symbol(for: item(now: "failed")) == .failed)
        for key in ["fyi", "folded", "done_replied", "done_manager", "done_user", "done_read", "done_dismissed",
                    "done_muted", "done_superseded", "a_future_state"] {
            #expect(InboxNow.symbol(for: item(now: key)) == nil, "\(key)")
        }
        #expect(InboxNow.symbol(for: item()) == nil)
    }

    @Test func aSharedStateKeyAloneMakesAnEvent() {
        var it = item(now: "done_replied")
        #expect(it.isEvent)
        #expect(InboxNow.text(for: it) == "你回过了")
        it.now = nil
        #expect(!it.isEvent)
    }

    @Test func detailHeaderUsesSharedMetaAndKeepsTheOldFallback() {
        var it = item()
        it.where = "Launch group"
        it.sender = "Alex"
        let expected = InboxNow.meta(it, lang: interfaceTextLocale.identifier)
        #expect(expected == "Launch group · Alex：Latest line")
        #expect(InboxEventPage.from(it) == expected)

        it.where = nil
        #expect(InboxNow.meta(it, lang: interfaceTextLocale.identifier) == nil)
        #expect(InboxEventPage.from(it) == "Team chat · A matter")

        it.where = ""
        #expect(InboxEventPage.from(it) == "Team chat · A matter")
    }
}
