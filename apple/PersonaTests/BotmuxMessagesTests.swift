import Foundation
import PersonaKit
import PersonaWire
import Testing
@testable import Persona

@MainActor
struct BotmuxMessagesTests {
    private func item(_ id: String, role: String = "assistant", kind: ItemKind = .text,
                      text: String = "", botmux: BotmuxMessage? = nil) -> Item {
        Item(id: id, seq: 1, role: role, kind: kind, text: text, botmux: botmux)
    }

    @Test func itemAndFoldDecodeAllSafeMetadataWithoutChangingOriginal() throws {
        let json = #"""
        {"id":"example","seq":1,"role":"user","kind":"text","text":"original wrapper",
         "botmux":{"kind":"message","text":"Decoded body","sender":"Example sender","sender_type":"bot",
         "quoted":true,"topic_context":true,"thread":true,"attachments":2,
         "attachment_meta":[{"kind":"file","name":"example.txt","mime_type":"text/plain"}],
         "mentions":["Example bot"],"source":"Example source","voice":true,"truncated":true}}
        """#
        let value = try JSONDecoder().decode(Item.self, from: Data(json.utf8))
        #expect(value.text == "original wrapper")
        let message = try #require(value.botmux)
        #expect(message.text == "Decoded body")
        #expect(message.sender == "Example sender")
        #expect(message.senderType == "bot")
        #expect(message.quoted == true && message.topicContext == true && message.thread == true)
        #expect(message.attachments == 2 && message.attachmentMeta?.first?.mimeType == "text/plain")
        #expect(message.mentions == ["Example bot"] && message.voice == true && message.truncated == true)
        let roundTrip = try JSONDecoder().decode(Item.self, from: JSONEncoder().encode(value))
        #expect(roundTrip == value)
        let fold = try JSONDecoder().decode(Fold.self, from: Data(#"""
        {"items":2,"tools":1,"thoughts":0,"through_seq":3,"through_id":"example",
         "botmux":[{"kind":"reply","text":"","final":true}]}
        """#.utf8))
        #expect(fold.botmux?.first?.final == true)
        #expect(fold.botmux?.first?.text == "")
    }

    @Test func olderNodesAndFutureKindsStillShowOriginal() throws {
        let value = try JSONDecoder().decode(Item.self, from: Data(#"""
        {"id":"old","seq":1,"role":"user","kind":"text","text":"ordinary message"}
        """#.utf8))
        #expect(value.botmux == nil)
        #expect(BotmuxPresentation.user(value) == value)
        var future = value
        future.botmux = BotmuxMessage(kind: "future", text: "future body")
        #expect(BotmuxPresentation.user(future) == future)
    }

    @Test func decodedUserHasSeparateMemoIdentityAndNeverOverwritesRawInput() {
        let original = item("user", role: "user", text: "original wrapper",
                            botmux: BotmuxMessage(kind: "message", text: "Decoded body"))
        let decoded = BotmuxPresentation.user(original)
        #expect(decoded.text == "Decoded body")
        #expect(original.text == "original wrapper")
        #expect(decoded.id != original.id)
        var updated = original
        updated.botmux?.text = "Updated structured body"
        #expect(BotmuxPresentation.user(updated).id != decoded.id)
    }

    @Test func repliesRequireSuccessfulCompletionAndSurviveFoldedHistory() {
        let reply = BotmuxMessage(kind: "reply", text: "## Result\n\n| Check | Result |\n|---|---|\n| Sample | OK |\n\n```swift\nlet x = 1\n```\nFirst\nSecond")
        let use = item("send", kind: .toolUse, botmux: reply)
        let success = item("ok", role: "user", kind: .toolResult, text: "ok")
        var failed = success
        failed.isError = true
        let running = ChatRow.tools([.call(ToolCall(use: use))])
        #expect(BotmuxPresentation.replies([running]).isEmpty)
        #expect(BotmuxPresentation.replies([.tools([.call(ToolCall(use: use, result: failed))])]).isEmpty)
        let complete = ChatRow.tools([.call(ToolCall(use: use, result: success))])
        #expect(BotmuxPresentation.replies([complete]).first?.text == reply.text)
        var folded = item("fold", kind: .fold)
        folded.fold = Fold(items: 2, tools: 1, thoughts: 0, throughSeq: 3, throughID: "example",
                           botmux: [reply, BotmuxMessage(kind: "reply", text: "")])
        let replies = BotmuxPresentation.replies([.tools([.folded(folded)])])
        #expect(replies.count == 2)
        #expect(Set(replies.map(\.id)).count == 2)
        #expect(BotmuxDisplay.needsReplyPlaceholder(kind: replies[1].botmux!.kind, text: replies[1].text!))
    }

    @Test func modeSwitchReprojectsSentinelAndRepliesWithoutChangingWireItems() {
        let user = item("user", role: "user", text: "wrapper", botmux: BotmuxMessage(kind: "message", text: "Hello"))
        var use = item("send", kind: .toolUse, botmux: BotmuxMessage(kind: "reply", text: ""))
        use.toolUseID = "call"
        use.toolName = "Bash"
        var result = item("result", role: "user", kind: .toolResult, text: "ok")
        result.toolUseID = "call"
        let sentinel = item("sentinel", text: "BOTMUX_NOTHING_TO_SEND")
        let wireItems = [user, use, result, sentinel]
        let blocks = ChatStore.rebuild(wireItems, settled: nil).blocks
        let decoded = ChatLines(openWork: [], botmuxMode: .decoded, botmuxSession: true).build(blocks, live: nil)
        let raw = ChatLines(openWork: [], botmuxMode: .original, botmuxSession: true).build(blocks, live: nil)
        let decodedAnswers = decoded.compactMap { line -> Item? in
            if case .row(.assistant(let i)) = line.content { return i }
            return nil
        }
        #expect(decodedAnswers.count == 1)
        #expect(decodedAnswers.first?.botmux?.kind == "reply")
        #expect(raw.contains { $0.id == sentinel.id })
        #expect(!decoded.contains { $0.id == sentinel.id })
        #expect(wireItems.last?.text == "BOTMUX_NOTHING_TO_SEND")
        // Non-BotMux chats must never lose text that happens to be the same word.
        let ordinary = ChatStore.rebuild([item("ordinary-user", role: "user", text: "Hello"), sentinel], settled: nil).blocks
        #expect(ChatLines(openWork: [], botmuxMode: .decoded).build(ordinary, live: nil).contains { $0.id == sentinel.id })
    }

    @Test func localPreferencePersistsAndIsReversible() throws {
        let suite = "example.botmux." + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let preference = StoredValue(wrappedValue: BotmuxDisplayMode.decoded, BotmuxDisplayMode.storageKey, store: defaults)
        #expect(preference.wrappedValue == .decoded)
        preference.wrappedValue = .original
        #expect(defaults.string(forKey: BotmuxDisplayMode.storageKey) == "all")
        let reloaded = StoredValue(wrappedValue: BotmuxDisplayMode.decoded, BotmuxDisplayMode.storageKey, store: defaults)
        #expect(reloaded.wrappedValue == .original)
        preference.wrappedValue = .decoded
        #expect(reloaded.wrappedValue == .decoded)
    }

    @Test func mediaOnlyRepliesKeepAttachmentsWithoutCardPlaceholder() throws {
        // Both intermediate images and final file-only sends have no text body.
        for (count, final) in [(1, false), (2, true)] {
            let json = """
            {"kind":"reply","text":"","attachments":\(count),"final":\(final),
             "attachment_meta":[{"kind":"file","name":"example.txt","mime_type":"text/plain"}]}
            """
            let reply = try JSONDecoder().decode(BotmuxMessage.self, from: Data(json.utf8))
            let shown = BotmuxPresentation.reply(reply, from: item("media", kind: .toolUse))
            #expect(shown.botmux?.attachments == count)
            #expect(shown.botmux?.final == final)
            #expect(!BotmuxDisplay.needsReplyPlaceholder(kind: reply.kind, text: reply.text,
                                                        attachments: reply.attachments ?? 0))
        }
    }

    @Test func deliveredRepliesFollowFinalAssistantTextIncludingFoldedHistory() {
        let reply = BotmuxMessage(kind: "reply", text: "Sent reply")
        let user = item("user", role: "user", text: "wrapper",
                        botmux: BotmuxMessage(kind: "message", text: "Hello"))
        var use = item("send", kind: .toolUse, botmux: reply)
        use.toolUseID = "call"
        use.toolName = "Bash"
        var result = item("result", role: "user", kind: .toolResult, text: "ok")
        result.toolUseID = "call"
        let final = item("final", text: "Additional final explanation")
        var fold = item("fold", kind: .fold)
        fold.fold = Fold(items: 2, tools: 1, thoughts: 0, throughSeq: 3,
                         throughID: "result", botmux: [reply])
        for history in [[user, use, result, final], [user, fold, final]] {
            let blocks = ChatStore.rebuild(history, settled: nil).blocks
            let lines = ChatLines(openWork: [], botmuxMode: .decoded, botmuxSession: true).build(blocks, live: nil)
            let answers = lines.compactMap { line -> Item? in
                if case .row(.assistant(let value)) = line.content { return value }
                return nil
            }
            #expect(answers.map(\.text) == ["Additional final explanation", "Sent reply"])
        }
    }

    @Test func decodedRepliesUseExistingGFMPreparation() {
        let text = "# Heading\n\n**Strong** and [link](https://example.com)\n\n- First\n- Second\n\n| Name | State |\n| --- | --- |\n| Example | Ready |\n\n```swift\nlet ready = true\n```\nFirst line\nSecond line"
        let html = AssistantRowText(text).markdown.renderHTML()
        for expected in ["<h1>", "<strong>Strong</strong>", "<ul>", "<table>", "<pre><code", "First line\nSecond line"] {
            #expect(html.contains(expected))
        }
    }
}
