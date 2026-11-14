import SwiftUI

/// UI version 4–5's code-hosting components where a whole page does not draw them (a card, a section; a page draws
/// them as the code page, `ExtensionCodePage`): what they say, read-only, small; a composer writes and sends as on the
/// page. Cards report UI version 6 (`ExtensionUIContext.cardVersion`), so the hub sends these as they are (#827).
struct ExtensionCodeSummary: View {
    var item: ExtensionComponent
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(AppModel.self) private var app

    /// Files listed at most; the rest are counted.
    private static let files = 12

    var body: some View {
        switch item {
        case .header(let h): CodeHeaderView(header: h)
        case .breadcrumb(let b):
            Text(b.crumbs.map(\.title).joined(separator: " / ")).font(Theme.Font.code).foregroundStyle(.secondary).lineLimit(1)
                .truncationMode(.head)
        case .fileTree(let t):
            let files = t.entries.filter { $0.kind != "dir" }
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                ForEach(Array(files.prefix(Self.files).enumerated()), id: \.offset) { _, e in
                    fileLine(e.path, status: e.status, additions: e.additions, deletions: e.deletions)
                }
                if files.count > Self.files {
                    Text("\(files.count - Self.files) more files").font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
        case .code(let c):
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                if let p = c.path, !p.isEmpty { Text(p).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.head) }
                Text(verbatim: c.text.split(separator: "\n", omittingEmptySubsequences: false).prefix(20).joined(separator: "\n"))
                    .font(Theme.Font.code).lineLimit(20).textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(Theme.Space.s)
                    .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control))
            }
        case .diff(let d):
            fileLine(d.path, status: d.status, additions: d.additions, deletions: d.deletions)
        case .commentThread(let t):
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                if let title = t.title, !title.isEmpty { Text(title).font(Theme.Font.labelStrong).lineLimit(1) }
                ForEach(Array(t.comments.prefix(5).enumerated()), id: \.offset) { _, c in
                    CodeCommentView(author: c.author, avatar: c.avatar?.url, at: c.at, markdown: c.body ?? "",
                                    badge: c.badge.map { ($0.text, $0.tone) }, event: c.event == true, link: $link)
                }
            }
        case .composer(let c):
            // Only the buttons this card can use (a read-only assistant card can use none): with none, no box to type in.
            if let model, case let buttons = c.buttons.filter({ model.isEnabled($0.action) }), !buttons.isEmpty {
                CodeComposerView(placeholder: c.placeholder ?? "", text: Binding(get: { model.values[c.name] ?? "" }, set: { model.values[c.name] = $0 }),
                                 buttons: buttons.map { b in (b.title, b.tone, b.action.id, { send(b, name: c.name, model) }) },
                                 running: model.running, identifier: "code-composer-\(c.name)")
            }
        default: EmptyView()
        }
    }

    private func send(_ b: ExtensionComposerButton, name: String, _ model: any ExtensionViewControlling) {
        let text = model.values[name] ?? ""
        Task { ExtensionOutcome.show(await model.perform(b.action, with: [name: text]), app: app, link: $link) }
    }

    private func fileLine(_ path: String, status: String?, additions: Int?, deletions: Int?) -> some View {
        HStack(spacing: Theme.Space.s) {
            if let l = CodeFileStatus.letter(status) { Text(l).font(Theme.Font.code).foregroundStyle(CodeFileStatus.color(status)) }
            Text(path).font(Theme.Font.code).lineLimit(1).truncationMode(.head)
            Spacer(minLength: Theme.Space.s)
            CodeChangeCounts(additions: additions, deletions: deletions)
        }
        .accessibilityElement(children: .combine)
    }
}
