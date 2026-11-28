import Testing
@testable import PersonaKit

struct BotmuxDisplayTests {
    @Test func modeDefaultsAndRoundTrips() {
        #expect(BotmuxDisplayMode(storedValue: nil) == .decoded)
        #expect(BotmuxDisplayMode(storedValue: "future") == .decoded)
        for mode in BotmuxDisplayMode.allCases {
            #expect(BotmuxDisplayMode(storedValue: mode.rawValue) == mode)
            #expect(mode.next != mode)
            #expect(mode.next.next == mode)
        }
    }

    @Test func onlyWholeAssistantSentinelsAreHidden() {
        for text in ["BOTMUX_NOTHING_TO_SEND", " \nBOTMUX_NOTHING_TO_SEND\r\n"] {
            #expect(BotmuxDisplay.hidesSentinel(text, role: "assistant", mode: .decoded))
            #expect(!BotmuxDisplay.hidesSentinel(text, role: "assistant", mode: .original))
            #expect(!BotmuxDisplay.hidesSentinel(text, role: "user", mode: .decoded))
        }
        for text in [nil, "", "Here is BOTMUX_NOTHING_TO_SEND", "`BOTMUX_NOTHING_TO_SEND`",
                     "BOTMUX_NOTHING_TO_SEND\nA real answer", "botmux_nothing_to_send"] as [String?] {
            #expect(!BotmuxDisplay.hidesSentinel(text, role: "assistant", mode: .decoded))
        }
    }

    @Test func cardRepliesKeepAVisiblePlaceholder() {
        #expect(BotmuxDisplay.needsReplyPlaceholder(kind: "reply", text: ""))
        #expect(BotmuxDisplay.needsReplyPlaceholder(kind: "reply", text: "\n \t"))
        #expect(!BotmuxDisplay.needsReplyPlaceholder(kind: "message", text: ""))
        #expect(!BotmuxDisplay.needsReplyPlaceholder(kind: "reply", text: "## Card summary"))
        #expect(!BotmuxDisplay.needsReplyPlaceholder(kind: "reply", text: "", attachments: 1))
        #expect(!BotmuxDisplay.needsReplyPlaceholder(kind: "reply", text: "", attachments: 3))
    }
}
