import Testing
@testable import Persona

/// The uncapped contract itself: no finite line count can satisfy plain reported detail, including one capped at four.
/// This final-only suite references the production helper added with the fix; keep it out of the baseline test overlay.
nonisolated struct InboxChainLineLimitTests {
    @Test(arguments: [false, true])
    func plainReportedDetailHasNoLineLimit(open: Bool) {
        for messages in [nil, []] as [[InboxItem.ChainEntry.Message]?] {
            let entry = InboxItem.ChainEntry(id: "reported", kind: "reported", text: "Reported",
                                             detail: "First line\nSecond line\nThird line\nFourth line\nFifth line",
                                             messages: messages)
            #expect(InboxEventText.chainLineLimit(entry, open: open) == nil,
                    "plain reported detail was capped when open was \(open)")
        }
    }

    @Test
    func excludedRowsAreCappedOnlyWhenClosed() {
        let detail = "First line\nSecond line\nThird line\nFourth line\nFifth line"
        let messages = [InboxItem.ChainEntry.Message(sender: "Agent", text: "Context", at: nil)]
        let cases: [(name: String, entry: InboxItem.ChainEntry)] = [
            ("non-reported", InboxItem.ChainEntry(id: "sent", kind: "sent", text: "Sent", detail: detail)),
            ("nil detail", InboxItem.ChainEntry(id: "nil-detail", kind: "reported", text: "Reported")),
            ("empty detail", InboxItem.ChainEntry(id: "empty-detail", kind: "reported", text: "Reported", detail: "")),
            ("source link", InboxItem.ChainEntry(id: "source-link", kind: "reported", text: "Reported", detail: detail,
                                                 link: InboxItem.Link(type: "source", url: "https://example.com/report"))),
            ("malformed session link", InboxItem.ChainEntry(id: "malformed-link", kind: "reported", text: "Reported",
                                                             detail: detail, link: InboxItem.Link(type: "session"))),
            ("messages", InboxItem.ChainEntry(id: "messages", kind: "reported", text: "Reported", detail: detail,
                                              messages: messages)),
        ]

        for c in cases {
            #expect(InboxEventText.chainLineLimit(c.entry, open: false) == 2,
                    "\(c.name) lost its closed two-line cap")
            #expect(InboxEventText.chainLineLimit(c.entry, open: true) == nil,
                    "\(c.name) stayed capped when open")
        }
    }
}
