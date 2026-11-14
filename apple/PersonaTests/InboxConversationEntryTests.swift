import Foundation
import Testing
@testable import Persona

/// The processing-conversation entry exists only when its handling session has both usable identifiers.
@MainActor
struct InboxConversationEntryTests {
    private func item(handling: InboxItem.Handling? = nil) -> InboxItem {
        var item = InboxItem(id: "matter", source: "chat", kind: "group", account: "chat", accountName: "Team chat",
                             title: "A matter", preview: "Latest line", count: 1)
        item.handling = handling
        return item
    }

    @Test func completeHandlingReturnsTheProcessingConversation() {
        let handling = InboxItem.Handling(owner: "manager", state: "waiting", machineID: "m1", sessionID: "s-handling")
        #expect(InboxEventBlocks.conversationKey(for: item(handling: handling))
                == SessionKey(machineID: "m1", sessionID: "s-handling"))
    }

    @Test func missingOrIncompleteHandlingHasNoConversation() {
        #expect(InboxEventBlocks.conversationKey(for: item()) == nil, "no handling must not offer a conversation")

        let incomplete = [
            InboxItem.Handling(state: "waiting", sessionID: "s-handling"),
            InboxItem.Handling(state: "waiting", machineID: "m1"),
            InboxItem.Handling(state: "waiting", machineID: "", sessionID: "s-handling"),
            InboxItem.Handling(state: "waiting", machineID: "m1", sessionID: ""),
            InboxItem.Handling(state: "waiting", machineID: "", sessionID: ""),
        ]
        for handling in incomplete {
            #expect(InboxEventBlocks.conversationKey(for: item(handling: handling)) == nil,
                    "machineID=\(String(describing: handling.machineID)), sessionID=\(String(describing: handling.sessionID))")
        }
    }
}
