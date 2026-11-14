import SwiftUI
import PersonaWire

/// A message's card from a messaging source (#2138): the extension turned the vendor's card into a declarative View
/// (`docs/extensions/extension-messaging.md`「Conversations and messages」, `view.schema.json`), drawn here in the chat's card frame: text, rows, numbers,
/// links, and buttons that go back to the extension (`POST messages/{id}/actions/{action}`). What a card cannot do in a
/// chat (fields to fill, charts) is left out; the card's `text` is the fallback when nothing is left.
struct MessagingViewCard: View {
    /// The card part's `metadata.variant` (LarkMessagingBridge).
    nonisolated static let variant = "persona-view"

    var meta: JSONValue
    var fallback: String
    @Environment(\.larkContext) private var context
    @Environment(\.larkAccount) private var larkAccount
    @Environment(\.larkManager) private var larkManager
    @Environment(\.larkGroup) private var larkGroup
    @Environment(\.larkRefresh) private var refresh
    @Environment(\.openURL) private var openURL
    @Environment(AppModel.self) private var model
    @State private var running: String?

    /// The generated View types carry their own keys: read with a plain decoder, as the assistant's cards are.
    private var view: ExtensionView? {
        guard let raw = try? JSONEncoder().encode(meta["view"] ?? .null) else { return nil }
        return try? JSONDecoder().decode(ExtensionView.self, from: raw)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            if let view, view.sections.contains(where: { !$0.items.isEmpty }) {
                ForEach(Array(view.sections.enumerated()), id: \.offset) { _, section in
                    if let t = section.title, !t.isEmpty { Text(t).font(Theme.Font.bodyStrong) }
                    ForEach(Array(section.items.enumerated()), id: \.offset) { _, item in itemView(item) }
                    if let f = section.footer, !f.isEmpty { Text(f).font(Theme.Font.caption).foregroundStyle(.secondary) }
                }
            } else {
                Text(fallback.isEmpty ? String(localized: "[Card]", comment: "Stands in for a card in plain text") : fallback).font(Theme.Font.body)
            }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(LarkColor.cardBackground)
        .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke))
    }

    @ViewBuilder private func itemView(_ item: ExtensionComponent) -> some View {
        switch item {
        case .text(let t):
            Text(t.text).font(t.style == "caption" ? Theme.Font.caption : t.style == "code" ? Theme.Font.code : Theme.Font.body)
                .foregroundStyle(t.style == "secondary" || t.style == "caption" ? .secondary : .primary)
        case .markdown(let m):
            Text(m.text).font(Theme.Font.body)
        case .metric(let m):
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(m.label).font(Theme.Font.caption).foregroundStyle(.secondary)
                Text(m.value).font(Theme.Font.bodyStrong).foregroundStyle(ExtensionUIStyle.tone(m.tone))
                if let d = m.detail, !d.isEmpty { Text(d).font(Theme.Font.caption).foregroundStyle(.secondary) }
            }
        case .row(let r):
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(r.title).font(Theme.Font.body)
                    if let s = r.subtitle, !s.isEmpty { Text(s).font(Theme.Font.caption).foregroundStyle(.secondary) }
                }
                Spacer(minLength: 0)
                if let v = r.value, !v.isEmpty { Text(v).font(Theme.Font.secondary).foregroundStyle(.secondary) }
            }
            .contentShape(.rect)
            .onTapGesture { if let a = r.action { press(a.id) } }
        case .button(let b):
            Button { press(b.action.id) } label: {
                HStack(spacing: Theme.Space.xs) {
                    if running == b.action.id { ProgressView().controlSize(.small) }
                    Text(b.title).lineLimit(1)
                }
            }
            .buttonStyle(.bordered)
            .tint(b.role == "destructive" ? Theme.danger : nil)
            .disabled(running != nil || context.messageID == nil)
        case .link(let l):
            if let url = URL(string: l.url), url.scheme == "https" {
                Button(l.title) { openURL(url) }.lineLimit(1)
            }
        case .divider:
            Divider()
        default:
            EmptyView()
        }
    }

    /// A button: the extension does what it does; a word comes back as a toast, then the chat reads again.
    private func press(_ action: String) {
        // The chat's account: a button of another account's card goes to that account.
        guard running == nil, let message = context.messageID, let api = model.larkAPI(group: larkGroup, manager: larkManager, account: larkAccount), let source = api.messagingSource else { return }
        running = action
        Task {
            defer { running = nil }
            do {
                // Ids are the source's own (any characters): each one path segment.
                let segment = { (s: String) in s.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(["/"])) ?? s }
                let raw = try await api.request("POST", api.messagingPath(source, "messages/\(segment(message))/actions/\(segment(action))"),
                                                query: api.messagingQuery(), body: [String: String]())
                if let o = try? JSONSerialization.jsonObject(with: raw) as? [String: Any] {
                    if let toast = o["toast"] as? String, !toast.isEmpty { model.toast = Toast(toast) }
                    if let s = o["open_url"] as? String, let url = URL(string: s), url.scheme == "https" { openURL(url) }
                }
                refresh?()
            } catch {
                model.toast = Toast(error.shown ?? String(localized: "The action didn't complete"), style: .error)
            }
        }
    }
}
