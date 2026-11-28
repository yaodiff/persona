// i18n-ignore-file: DEBUG-only synthetic conversation for simulator UI verification; no hub or personal data.
#if DEBUG
import PersonaKit
import SwiftUI

extension View {
    func botmuxSample() -> some View { modifier(BotmuxSampleSheet()) }
}

private enum BotmuxSamples {
    static let enabled = UserDefaults.standard.bool(forKey: "showBotmuxSample")
    static let user = Item(id: "example-user", seq: 1, role: "user", kind: .text,
                           text: "<user_message>Can you review this example?</user_message>\n<sender name=\"Example sender\" />",
                           botmux: BotmuxMessage(kind: "message", text: "Can you review this example?",
                                                 sender: "Example sender", quoted: true, attachments: 2,
                                                 mentions: ["Example bot"], voice: true))
    static let richText = "## Review\n**Ready** to check.\nA second line.\n\n| Check | Result |\n| --- | --- |\n| Sample | Passed |\n\n```swift\nlet ready = true\n```"
    static let reply = Item(id: "example-reply", seq: 2, role: "assistant", kind: .text, text: richText,
                            botmux: BotmuxMessage(kind: "reply", text: richText))
    static let card = Item(id: "example-card", seq: 3, role: "assistant", kind: .text,
                           text: "", botmux: BotmuxMessage(kind: "reply", text: "", final: true))
    static let sentinel = Item(id: "example-sentinel", seq: 4, role: "assistant", kind: .text,
                               text: "BOTMUX_NOTHING_TO_SEND")
}

private struct BotmuxSampleSheet: ViewModifier {
    func body(content: Content) -> some View {
        content.sheet(isPresented: .constant(BotmuxSamples.enabled)) { BotmuxSamplePage() }
    }
}

private struct BotmuxSamplePage: View {
    @StoredValue(BotmuxDisplayMode.storageKey) private var mode: BotmuxDisplayMode = .decoded

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    BotmuxModePicker(mode: $mode).pickerStyle(.segmented)
                    if mode == .decoded {
                        BotmuxMessagesView(item: BotmuxSamples.user)
                        BotmuxMessagesView(item: BotmuxSamples.reply)
                        BotmuxMessagesView(item: BotmuxSamples.card)
                    } else {
                        UserBubble(item: BotmuxSamples.user)
                        AssistantText(item: BotmuxSamples.reply)
                        AssistantText(item: BotmuxSamples.sentinel)
                    }
                }
                .padding(Theme.Space.page)
            }
            .navigationTitle("BotMux")
            .background(Theme.Fill.well)
        }
    }
}
#endif
