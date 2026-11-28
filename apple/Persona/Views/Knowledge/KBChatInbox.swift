import PersonaWire
import SwiftUI

// A chat's Inbox items on its knowledge page (#15356, docs/assistant/knowledge.md "A chat's Inbox items"), as the web
// draws them (web/src/views/knowledge/KBInbox.tsx): "In Inbox", the open items with their section and the done ones
// folded under "N done", each opening its item; and a row of the chat's last 7 days that an item holds, opening that
// item. An item opens as a link to it does (`/inbox/<id>`, docs/protocol/deep-links.md; `AppModel.openKBInboxItem`, on
// the Inbox's side in InboxKBChat.swift). A chat without items, and a hub from before them, draw the page as before.

/// The page's part of the chat's Inbox items, worked out once per read (pure, for tests).
nonisolated enum KBChatInbox {
    /// What a chat's page draws of its Inbox items: the open ones, the done ones, and the recent text cut into pieces
    /// (nil when no item holds a row of it).
    struct Page: Equatable, Sendable {
        var open: [KBInboxItem]
        var done: [KBInboxItem]
        var pieces: [Piece]?

        /// nil for an entry that is no chat, and for a chat without items (or from a hub before them).
        init?(_ response: KBDetailResponse) {
            guard response.detail.kind == KBKind.chats.rawValue, let items = response.inbox, !items.isEmpty else { return nil }
            open = items.filter { $0.doneAt == nil }
            done = items.filter { $0.doneAt != nil }
            pieces = KBChatInbox.pieces(response.detail.recent ?? "", items: items)
        }
    }

    /// A run of a chat's recent text as the page draws it: plain lines, or one message row that Inbox item `item` holds.
    struct Piece: Equatable, Hashable, Identifiable, Sendable {
        /// Its first line's index in the text.
        var id: Int
        var text: String
        var item: KBInboxItem?
    }

    /// The section an item's row names, as the Inbox sorts it: an open item's `status`, else its `tier`; nil for an
    /// item not sorted yet. A word this app doesn't know reads as Needs You, as the web's `inboxTier`.
    static func tier(_ item: KBInboxItem) -> InboxItem.Tier? {
        let status = item.doneAt == nil ? item.status ?? "" : ""
        let key = status.isEmpty ? item.tier ?? "" : status
        guard !key.isEmpty else { return nil }
        return InboxItem.Tier(rawValue: key) ?? .action
    }

    /// The line under an item's title: its summary, else its preview.
    static func line(_ item: KBInboxItem) -> String? {
        [item.summary, item.preview].lazy.compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }.first { !$0.isEmpty }
    }

    /// What a row of the recent text goes by (the web's `messageRowKey`): its day, and when, who and what as the
    /// message line reads them, the indentation of its further lines left out.
    static func rowKey(day: String, line: String) -> String {
        let m = KBMessageLine(line)
        let text = m.text.replacingOccurrences(of: #"\n[ \t]+"#, with: "\n", options: .regularExpression)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return [day, m.time, m.sender, text].joined(separator: "\u{0}")
    }

    /// The item each row an item names belongs to, by `rowKey`: the first item naming it (the hub lists the newest first
    /// and gives a row to one item).
    static func rowItems(_ items: [KBInboxItem]) -> [String: KBInboxItem] {
        var out: [String: KBInboxItem] = [:]
        for item in items {
            for m in item.messages ?? [] where out[rowKey(day: m.day, line: m.line)] == nil {
                out[rowKey(day: m.day, line: m.line)] = item
            }
        }
        return out
    }

    /// The recent text ("## 2026-10-30" days, "- 10:15 name: text" rows with their further lines indented) cut the way
    /// the web's `parseRecent` and the hub's `kbRecentRows` read it: a row is a "- " line and the indented lines after it,
    /// or a line of its own; headings, blank lines and "…" belong to no row. A row an item holds is a piece of its own,
    /// from its first line to its last; the lines between are plain pieces. nil when no item holds a row: the page draws
    /// the text whole, as before.
    static func pieces(_ recent: String, items: [KBInboxItem]) -> [Piece]? {
        let byRow = rowItems(items)
        guard !byRow.isEmpty, !recent.isEmpty else { return nil }
        let lines = recent.components(separatedBy: "\n")
        var linked: [(first: Int, last: Int, item: KBInboxItem)] = []
        var day = ""
        var row: (first: Int, last: Int, lines: [String], day: String)?
        func end() {
            if let r = row, !r.day.isEmpty, let item = byRow[rowKey(day: r.day, line: r.lines.joined(separator: "\n"))] {
                linked.append((r.first, r.last, item))
            }
            row = nil
        }
        for (i, l) in lines.enumerated() {
            if let d = heading(l) {
                end()
                day = d
            } else if l.hasPrefix("- ") {
                end()
                row = (i, i, [l], day)
            } else if var r = row, l.first?.isWhitespace == true {
                r.last = i
                r.lines.append(l)
                row = r
            } else if case let t = l.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty, t != "…" {
                end()
                row = (i, i, [l], day)
            }
        }
        end()
        guard !linked.isEmpty else { return nil }
        var out: [Piece] = []
        var next = 0
        func plain(upTo stop: Int) {
            if next < stop, case let text = lines[next..<stop].joined(separator: "\n"), !text.isEmpty {
                out.append(Piece(id: next, text: text))
            }
        }
        for r in linked {
            plain(upTo: r.first)
            out.append(Piece(id: r.first, text: lines[r.first...r.last].joined(separator: "\n"), item: r.item))
            next = r.last + 1
        }
        plain(upTo: lines.count)
        return out
    }

    /// The day a "## 2026-10-30" heading names.
    private static func heading(_ l: String) -> String? {
        guard l.hasPrefix("## ") else { return nil }
        let d = l.dropFirst(3).trimmingCharacters(in: .whitespaces)
        let shape = d.enumerated().allSatisfy { i, c in i == 4 || i == 7 ? c == "-" : c.isASCII && c.isNumber }
        return d.count == 10 && shape ? d : nil
    }
}

/// "In Inbox" on a chat's page: its open Inbox items, then "N Done" folding the done ones open. Each row opens its item.
struct KBChatInboxSection: View {
    var page: KBChatInbox.Page
    var open: (KBInboxItem) -> Void
    @State private var doneShown = false

    var body: some View {
        Section {
            ForEach(page.open) { row($0) }
            if doneShown { ForEach(page.done) { row($0) } }
            if !page.done.isEmpty {
                Button { withAnimation(Theme.Motion.standard) { doneShown.toggle() } } label: {
                    Text(doneShown ? String(localized: "Collapse") : String(localized: "\(page.done.count) done [Knowledge Inbox]"))
                        .font(Theme.Font.secondaryMedium).lineLimit(1)
                }
                .accessibilityIdentifier("kb.inbox.done")
            }
        } header: {
            HStack(spacing: Theme.Space.sm) {
                Text("In Inbox")
                if !page.open.isEmpty { Text("\(page.open.count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit() }
            }
            .accessibilityElement(children: .combine)
        }
    }

    private func row(_ item: KBInboxItem) -> some View {
        Button { open(item) } label: { KBInboxRow(item: item) }
            .buttonStyle(.plain)
            .accessibilityIdentifier("kb.inbox.item")
    }
}

/// An Inbox item on a chat's page, laid out as a knowledge row (`KBRow`): done or open, its title and when it last
/// changed (was done), then its section and its summary, so the title keeps the row's width at every text size.
struct KBInboxRow: View {
    var item: KBInboxItem

    var body: some View {
        let done = item.doneAt != nil
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) {
            ThemedIcon(done ? Theme.Icon.done : Theme.Icon.inbox).font(Theme.Font.secondary).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    Text(item.title).font(Theme.Font.secondaryMedium).foregroundStyle(done ? .secondary : .primary).lineLimit(2)
                    Spacer(minLength: Theme.Space.xs)
                    if let at = item.doneAt ?? item.updatedAt, at.isMeaningful {
                        Text(Fmt.relative(at)).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1).fixedSize()
                    }
                }
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    if let tier = KBChatInbox.tier(item) {
                        KBPill(text: tier.title, tint: tier == .action && !done ? Theme.needsYou : nil)
                    }
                    if let line = KBChatInbox.line(item) {
                        Text(line).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2)
                    }
                }
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }
}

/// A chat's last 7 days: the text as before, and when Inbox items hold some of its rows, those rows open their item,
/// drawn as links (the accent color, an Inbox mark at their end).
struct KBRecentText: View {
    var text: String
    var pieces: [KBChatInbox.Piece]?
    var open: (KBInboxItem) -> Void

    var body: some View {
        if let pieces {
            VStack(alignment: .leading, spacing: Theme.Space.flush) {
                ForEach(pieces) { p in
                    if let item = p.item {
                        Button { open(item) } label: {
                            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                                // In the accent color, as a link, with the Inbox mark.
                                Text(p.text).font(Theme.Font.secondary).frame(maxWidth: .infinity, alignment: .leading)
                                ThemedIcon(Theme.Icon.inbox).font(Theme.Font.label)
                            }
                            .tapTargetOutset()
                        }
                        .buttonStyle(.borderless)
                        .accessibilityHint(String(localized: "Open in Inbox: \(item.title)"))
                        .accessibilityIdentifier("kb.recent.inbox")
                    } else {
                        Text(p.text).font(Theme.Font.secondary).textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        } else {
            Text(text).font(Theme.Font.secondary).textSelection(.enabled)
        }
    }
}
