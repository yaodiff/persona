import Observation
import SwiftUI

/// The text the hub stored for a mail's Inbox item, as a reader shows it (#15995).
///
/// The hub writes the item's `context` for the models that triage it (`server/internal/assistant/inbox_mail.go`): once
/// it has read the newest mail, a block of header lines, an empty line, then up to 3,000 characters of that mail's
/// plain text. The headers are drawn by the page from the item itself, so only the text after them is shown. A
/// context in any other shape (the list of previews the hub writes before it has read a body, or a newer hub's) is
/// shown as stored. Whether the hub clipped the text is `MailStoredClipping`, from the same page of the item.
nonisolated enum MailStoredText: Hashable, Sendable {
    case text(String)
    /// Nothing was stored: no context and no preview.
    case none

    init(context: String?, preview: String) {
        let stored = Self.body(of: context ?? "")
        let shown = stored.isEmpty ? preview.trimmingCharacters(in: .whitespacesAndNewlines) : stored
        self = shown.isEmpty ? .none : .text(shown)
    }

    /// The mail's text in a context the hub wrote after reading the body; else the context as it is.
    private static func body(of context: String) -> String {
        let whole = context.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let gap = whole.range(of: "\n\n") else { return whole }
        let head = whole[..<gap.lowerBound]
        // The header block the hub writes: it opens with the mailbox line and ends with the subject line.
        guard head.hasPrefix("邮箱 "), // i18n-ignore: matches the hub's stored text
              let last = head.split(separator: "\n").last, last.hasPrefix("主题：") // i18n-ignore: matches the hub's stored text
        else { return whole }
        return whole[gap.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// What the stored reader shows for one mail item: it reads the item's stored text from Core (`GET /api/v1/inbox/{id}`)
/// and never asks a route under `/api/v1/mail`.
@Observable final class MailStoredModel {
    struct Deps {
        /// The item in full, from its own manager.
        var item: (InboxItem) async throws -> InboxItem

        static var live: Deps {
            Deps(item: { item in
                let model = AppModel.shared
                guard let api = model.api?.manager(model.itemManager(item.manager)) else { throw CancellationError() }
                return try await api.inboxItem(item.id)
            })
        }
    }

    enum State: Hashable {
        case loading
        /// The stored text, and whether the hub says it is all of the mail.
        case shown(MailStoredText, MailStoredClipping)
    }

    private(set) var state = State.loading
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    /// Reads the stored text. When the read fails the preview the list already has is shown: there is nothing the user
    /// could retry that the pull to refresh does not do, so no error is drawn. Whether the text is whole is known only
    /// for the context the item's own page sent.
    func load(_ item: InboxItem) async {
        let full = try? await deps.item(item)
        guard !Task.isCancelled else { return }
        let stored = full?.context?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
        state = .shown(MailStoredText(context: full?.context, preview: item.preview), stored ? (full?.storedClipping ?? .unknown) : .unknown)
    }
}

/// A mail already in the Inbox, opened while the hub's mail module is off (`MailAccess.storedOnly`): its headers, the
/// text the hub stored, and one line saying why there is no more. No reply, no flags, no attachments, no translation of
/// a body that cannot be read, and opening it marks nothing as read on the mail server.
struct MailStoredView: View {
    var item: InboxItem
    var summary: MailSummary
    @State private var model = MailStoredModel()

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                Text(summary.subject?.isEmpty == false ? summary.subject! : String(localized: "(No subject)"))
                    .font(Theme.Font.title)
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    Monogram(name: summary.from?.display ?? "", fill: Theme.accent.opacity(0.15), ink: Theme.accent)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(summary.from?.display ?? "").font(Theme.Font.secondaryStrong)
                        Text(summary.from?.email ?? "").font(Theme.Font.label).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Text(summary.date.map { $0.formatted(date: .abbreviated, time: .shortened) } ?? "").font(Theme.Font.label).foregroundStyle(.secondary)
                }
                Divider()
                switch model.state {
                case .loading:
                    ProgressView().frame(maxWidth: .infinity).padding(Theme.Space.xl)
                case .shown(.text(let text), let clipping):
                    Text(text).font(Theme.Font.body).lineHeight(.body).textSelection(.enabled).readingContent()
                        .accessibilityIdentifier("mail.stored.text")
                    notice(Self.line(clipping))
                case .shown(.none, _):
                    notice(String(localized: "Mail is turned off on the hub machine, so the full message and its attachments can't be loaded. No text was saved for this mail."))
                }
            }
            .padding(Theme.Space.xl)
        }
        .task(id: item.key) { await model.load(item) }
    }

    private func notice(_ line: String) -> some View {
        Text(line).font(Theme.Font.caption).foregroundStyle(.secondary)
            .accessibilityIdentifier("mail.stored.notice")
    }

    /// The one line under the stored text: why there is no more, and what the hub says about the text's length.
    static func line(_ clipping: MailStoredClipping) -> String {
        switch clipping {
        case .clipped: String(localized: "Mail is turned off on the hub machine, so the full message and its attachments can't be loaded. This is the text saved earlier, and it is cut short.")
        case .whole: String(localized: "Mail is turned off on the hub machine, so the full message and its attachments can't be loaded. This is the text saved earlier.")
        case .unknown: String(localized: "Mail is turned off on the hub machine, so the full message and its attachments can't be loaded. This is the text saved earlier; a long message may be cut short.")
        }
    }
}

/// A mail of an Inbox item, opened: the live reader while mail works, the stored text while the hub's mail module is
/// off. The one place that decides between them.
struct MailReaderView: View {
    var item: InboxItem
    var summary: MailSummary
    var accounts: [MailAccount]

    var body: some View {
        switch MailModule.access {
        case .live: MailDetailView(summary: summary, accounts: accounts)
        case .storedOnly: MailStoredView(item: item, summary: summary)
        }
    }
}
