import SwiftUI

// A topic's knowledge points as the iPhone and iPad show them (#7251): the topic page's sections by subtopic, a point's
// row, and a point's detail (a sheet on the iPhone, the map's detail column on the iPad). The web's `TopicPoints.tsx`.

/// A point's kind as a pill: Pitfall and Open Question in the warning tone.
struct KBPointKindPill: View {
    var kind: String

    var body: some View {
        KBPill(text: KBPointKind.label(kind), tint: KBPointKind.warns(kind) ? Theme.caution : nil, bold: true)
    }
}

/// A point in a list: its kind and text, the reason or cause, then where it came from and the day it was confirmed.
struct KBPointRow: View {
    var point: KBTopicPoint
    var links: [KBEntry]

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                KBPointKindPill(kind: point.kind)
                Text(point.text).font(Theme.Font.body).foregroundStyle(.primary).fixedSize(horizontal: false, vertical: true)
            }
            if let why = point.why, !why.isEmpty {
                Text(String(localized: "\(KBPointKind.whyLabel(point.kind)): \(why)"))
                    .font(Theme.Font.secondary).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            if let line = metaLine {
                Text(line).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .readingContent()
        .padding(.vertical, Theme.Space.xxs)
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    private var metaLine: String? {
        let names = KBTopics.sources(point, links: links).map(\.name)
        var parts: [String] = []
        if !names.isEmpty { parts.append(names.joined(separator: String(localized: ", [List Separator]"))) }
        if let when = point.when, !when.isEmpty { parts.append(String(localized: "Confirmed \(KBTopics.day(when))")) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// A topic page's points: the loose ones first, then a section per subtopic (its own subtopics as headings inside it,
/// anything deeper folded into those). A point opens its detail.
struct KBTopicPointsSections: View {
    var detail: KBDetail
    var open: (KBTopicPoint) -> Void

    var body: some View {
        let root = KBTopics.group(detail.points)
        let links = detail.links ?? []
        if root.size == 0 {
            Section {
                Text("No knowledge points under this topic yet.").font(Theme.Font.secondary).foregroundStyle(.secondary)
            } header: { Text("Knowledge Points") }
        }
        if !root.points.isEmpty {
            Section {
                ForEach(root.points) { p in row(p, links) }
            } header: { header(String(localized: "Knowledge Points"), root.points.count) }
        }
        ForEach(root.groups, id: \.path) { g in
            Section {
                ForEach(g.points) { p in row(p, links) }
                ForEach(g.groups, id: \.path) { sub in
                    Text(sub.name).font(Theme.Font.captionStrong).foregroundStyle(.secondary).padding(.top, Theme.Space.xs)
                        .accessibilityAddTraits(.isHeader)
                    ForEach(flattened(sub)) { p in row(p, links) }
                }
            } header: { header(g.name, g.size) }
        }
    }

    private func row(_ p: KBTopicPoint, _ links: [KBEntry]) -> some View {
        Button { open(p) } label: { KBPointRow(point: p, links: links) }
            .buttonStyle(.plain)
            .accessibilityIdentifier("kb.point")
            .contextMenu {
                Button { UIPasteboard.general.string = p.text } label: { Label("Copy", systemImage: Theme.Icon.copy) }
            }
    }

    private func flattened(_ g: KBPointGroup) -> [KBTopicPoint] { g.points + g.groups.flatMap(flattened) }

    private func header(_ title: String, _ count: Int) -> some View {
        HStack(spacing: Theme.Space.sm) {
            Text(title)
            Text("\(count)").foregroundStyle(Color.secondary.opacity(0.7)).monospacedDigit()
        }
    }
}

/// A point on its own: kind and day, the text, the topic path above it, the reason or cause, and the entries it came from,
/// which open. The iPhone's sheet and the iPad map's detail column.
struct KBPointDetail: View {
    var point: KBTopicPoint
    var links: [KBEntry]
    var crumbs: [String]
    var openEntry: (KBEntry) -> Void

    var body: some View {
        Section {
            VStack(alignment: .leading, spacing: Theme.Space.sm) {
                HStack(spacing: Theme.Space.sm) {
                    KBPointKindPill(kind: point.kind)
                    if let when = point.when, !when.isEmpty { KBPill(text: String(localized: "Confirmed \(KBTopics.day(when))")) }
                }
                Text(point.text).font(Theme.Font.title).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                if !crumbs.isEmpty {
                    Text(crumbs.joined(separator: " › ")).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            .readingContent()
            .padding(.vertical, Theme.Space.xs)
        }
        .listRowBackground(Color.clear)
        if let why = point.why, !why.isEmpty {
            Section(KBPointKind.whyLabel(point.kind)) {
                Text(why).font(Theme.Font.body).readingContent().textSelection(.enabled)
            }
        }
        let from = KBTopics.sources(point, links: links)
        if !from.isEmpty {
            Section("From [Knowledge Point]") {
                ForEach(from, id: \.self) { e in
                    Button { openEntry(e) } label: {
                        HStack {
                            KBLinkRow(entry: e)
                            Spacer(minLength: Theme.Space.s)
                            ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
                        }
                        .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("kb.point.source")
                }
            }
        }
        Section {
            Button { UIPasteboard.general.string = point.text } label: { Label("Copy", systemImage: Theme.Icon.copy).lineLimit(1) }
        }
    }
}

/// A point in a sheet over the topic page (the iPhone); a source opens over the page under it.
struct KBPointSheet: View {
    @Environment(\.dismiss) private var dismiss
    var point: KBTopicPoint
    var links: [KBEntry]
    var crumbs: [String]
    var openEntry: (KBEntry) -> Void

    var body: some View {
        NavigationStack {
            List {
                KBPointDetail(point: point, links: links, crumbs: crumbs) { e in
                    dismiss()
                    openEntry(e)
                }
            }
            .listStyle(.insetGrouped)
            .themedPage()
            .navigationTitle("Knowledge Point")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button { dismiss() } label: { Text("Done").lineLimit(1) } }
            }
        }
        .presentationDetents([.medium, .large])
    }
}

/// A point the page has open, with what its sheet needs.
struct KBOpenPoint: Identifiable, Hashable {
    var point: KBTopicPoint
    var links: [KBEntry]
    var crumbs: [String]
    var id: String { point.id }
}
