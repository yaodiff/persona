import PersonaKit
import SwiftUI

/// BotMux's decoded conversation uses the same bubble and rich Markdown as every other conversation.
struct BotmuxMessagesView: View, Equatable {
    var item: Item
    private var shown: Item
    private var placeholder: Bool

    init(item: Item) {
        self.item = item
        shown = item.role == "user" ? BotmuxPresentation.user(item) : item
        if let message = item.botmux, message.kind == "reply", item.text != message.text {
            shown.id = BotmuxPresentation.contentID(item.id, message)
            shown.text = message.text
        }
        placeholder = item.botmux.map {
            BotmuxDisplay.needsReplyPlaceholder(kind: $0.kind, text: $0.text, attachments: $0.attachments ?? 0)
        } ?? false
    }

    nonisolated static func == (lhs: Self, rhs: Self) -> Bool { lhs.item == rhs.item }

    var body: some View {
        if let message = item.botmux {
            VStack(alignment: item.role == "user" ? .trailing : .leading, spacing: Theme.Space.xs) {
                BotmuxTags(message: message)
                    .frame(maxWidth: .infinity, alignment: item.role == "user" ? .trailing : .leading)
                if item.role == "user" {
                    UserBubble(item: shown).equatable()
                    BotmuxOriginal(item: item)
                } else if placeholder {
                    Text("Card sent to Lark [BotMux]")
                        .font(Theme.Font.body).foregroundStyle(.secondary)
                        .accessibilityIdentifier("botmux.card-sent")
                } else if !message.text.isEmpty {
                    AssistantText(item: shown).equatable()
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("botmux.message." + item.id)
        }
    }
}

/// Facts wrap at large Dynamic Type sizes; they never compete with the message for a fixed-width row.
private struct BotmuxTags: View {
    var message: BotmuxMessage

    var body: some View {
        FlowLayoutRow(spacing: Theme.Space.xs) {
            if message.kind == "trigger" {
                if let source = message.source, !source.isEmpty {
                    tag(Text("Via \(source) [BotMux]"))
                } else {
                    tag(Text("Triggered message [BotMux]"))
                }
            }
            if message.kind == "reply" {
                if message.final == true { tag(Text("Final reply sent to Lark [BotMux]")) }
                else { tag(Text("Sent to Lark [BotMux]")) }
            }
            if let sender = message.sender, !sender.isEmpty {
                if message.senderType == "bot" { tag(Text("Bot: \(sender) [BotMux]")) }
                else { tag(Text(verbatim: sender)) }
            }
            if message.quoted == true { tag(Text("Quoted message [BotMux]")) }
            if message.topicContext == true { tag(Text("Earlier topic messages [BotMux]")) }
            if message.thread == true { tag(Text("In a topic [BotMux]")) }
            if let count = message.attachments, count > 0 { tag(Text("Attachments: \(count) [BotMux]")) }
            ForEach((message.mentions ?? []).indices, id: \.self) { index in
                tag(Text(verbatim: "@" + (message.mentions ?? [])[index]))
            }
            if message.voice == true { tag(Text("Voice message [BotMux]")) }
            if message.truncated == true { tag(Text("Message truncated [BotMux]")) }
        }
    }

    private func tag(_ text: Text) -> some View {
        text.font(Theme.Font.caption).foregroundStyle(.secondary)
            .padding(.horizontal, Theme.Space.s).padding(.vertical, Theme.Space.xxs)
            .background(Theme.Fill.chip, in: .capsule)
    }
}

/// The original is loaded only on disclosure, through the chat's existing full-text loader.
private struct BotmuxOriginal: View {
    var item: Item
    @State private var open = false
    @State private var fullText: String?
    @State private var error: String?
    @State private var retry = 0
    @Environment(\.loadFullText) private var loadFullText

    var body: some View {
        DisclosureGroup(isExpanded: $open) {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(verbatim: fullText ?? item.cutText)
                    .font(Theme.Font.caption.monospaced()).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if item.fullSize != nil, fullText == nil {
                    if let error {
                        Text(verbatim: error).foregroundStyle(Theme.danger)
                        Button("Retry") { retry += 1 }.lineLimit(1)
                            .frame(minHeight: Theme.Size.tapTarget)
                    } else {
                        ProgressView().accessibilityLabel(Text("Loading original transcript [BotMux]"))
                    }
                }
            }
            .padding(Theme.Space.s)
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
        } label: {
            Text("Original transcript [BotMux]").font(Theme.Font.caption)
                .frame(minHeight: Theme.Size.tapTarget)
        }
        .accessibilityIdentifier("botmux.original." + item.id)
        .task(id: "\(open)-\(retry)") {
            guard open, item.fullSize != nil, fullText == nil, let loadFullText else { return }
            error = nil
            do {
                let text = try await loadFullText(item.id)
                guard !Task.isCancelled else { return }
                fullText = text
            } catch {
                guard !Task.isCancelled else { return }
                self.error = error.shown
            }
        }
    }
}

struct BotmuxModePicker: View {
    @Binding var mode: BotmuxDisplayMode

    var body: some View {
        Picker("BotMux messages", selection: $mode) {
            Text("Decoded [BotMux]").tag(BotmuxDisplayMode.decoded)
            Text("Original [BotMux]").tag(BotmuxDisplayMode.original)
        }
        .lineLimit(1)
        .accessibilityIdentifier("botmux.mode")
    }
}
