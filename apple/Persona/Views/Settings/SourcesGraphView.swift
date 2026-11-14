import SwiftUI

// Inbox Sources' relationship view on the iPhone (#5741, docs/extensions/subscriptions.md "Pages"): what brings each
// source (an extension, or Persona itself), how it is read, the source and where its things go, from
// `GET /api/v1/subscriptions/-/graph` in the pages' own words. A tree per extension with filters (columns don't fit a
// phone); tapping a source opens a sheet with its path, Open Settings (its page) and, for a subscription, Set Up with
// the assistant. Reached from the "Source Relationships" row the pages draw for an app that says it has this view.

/// `GET /api/v1/subscriptions/-/graph`.
nonisolated struct SourcesGraph: Decodable, Equatable, Sendable {
    struct Extension: Decodable, Equatable, Sendable, Identifiable {
        var id: String
        var name: String
        var line: String
    }

    struct Read: Decodable, Equatable, Sendable, Identifiable {
        var id: String
        var extensionID: String
        var title: String
        var direct: Bool
        var push: Bool

        private enum CodingKeys: String, CodingKey { case id, extensionID = "extension", title, direct, push }

        init(id: String, extensionID: String, title: String, direct: Bool = false, push: Bool = false) {
            (self.id, self.extensionID, self.title, self.direct, self.push) = (id, extensionID, title, direct, push)
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            extensionID = try c.decodeIfPresent(String.self, forKey: .extensionID) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            direct = try c.decodeIfPresent(Bool.self, forKey: .direct) ?? false
            push = try c.decodeIfPresent(Bool.self, forKey: .push) ?? false
        }
    }

    struct Badge: Decodable, Equatable, Sendable {
        var text: String
        var tone: String?
    }

    struct Source: Decodable, Equatable, Sendable, Identifiable {
        var id: String
        var read: String
        var name: String
        var icon: String
        var line: String
        var badge: Badge?
        var reading: Bool
        var to: [String]
        var duplicates: [String]
        /// The -subscriptions page that shows it.
        var state: String
        /// Set Up with the assistant's open_url (a subscription's).
        var ask: String?

        private enum CodingKeys: String, CodingKey { case id, read, name, icon, line, badge, reading, to, duplicates, state, ask }

        init(id: String, read: String, name: String, icon: String = "", line: String = "", badge: Badge? = nil, reading: Bool = true,
             to: [String] = ["inbox"], duplicates: [String] = [], state: String = "", ask: String? = nil) {
            (self.id, self.read, self.name, self.icon, self.line, self.badge) = (id, read, name, icon, line, badge)
            (self.reading, self.to, self.duplicates, self.state, self.ask) = (reading, to, duplicates, state, ask)
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            read = try c.decode(String.self, forKey: .read)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            icon = try c.decodeIfPresent(String.self, forKey: .icon) ?? ""
            line = try c.decodeIfPresent(String.self, forKey: .line) ?? ""
            badge = try c.decodeIfPresent(Badge.self, forKey: .badge)
            reading = try c.decodeIfPresent(Bool.self, forKey: .reading) ?? false
            to = try c.decodeIfPresent([String].self, forKey: .to) ?? []
            duplicates = try c.decodeIfPresent([String].self, forKey: .duplicates) ?? []
            state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
            ask = try c.decodeIfPresent(String.self, forKey: .ask)
        }
    }

    struct Destination: Decodable, Equatable, Sendable, Identifiable {
        var id: String
        var name: String
        var line: String
    }

    var extensions: [Extension]
    var reads: [Read]
    var sources: [Source]
    var destinations: [Destination]

    private enum CodingKeys: String, CodingKey { case extensions, reads, sources, destinations }

    init(extensions: [Extension], reads: [Read], sources: [Source], destinations: [Destination]) {
        (self.extensions, self.reads, self.sources, self.destinations) = (extensions, reads, sources, destinations)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        extensions = try c.decodeIfPresent([Extension].self, forKey: .extensions) ?? []
        reads = try c.decodeIfPresent([Read].self, forKey: .reads) ?? []
        sources = try c.decodeIfPresent([Source].self, forKey: .sources) ?? []
        destinations = try c.decodeIfPresent([Destination].self, forKey: .destinations) ?? []
    }
}

extension APIClient {
    /// `GET /api/v1/subscriptions/-/graph`: Inbox Sources as relations, in the request's language, with the icons the
    /// pages' UI version has (the pages' rows show the same ones).
    func sourcesGraph() async throws -> SourcesGraph {
        try await get("/api/v1/subscriptions/-/graph", query: ["ui_version": String(ExtensionUIContext.pageVersion)])
    }
}

/// What the view shows: the graph, a filter over it, and the source picked.
@MainActor @Observable final class SourcesGraphModel {
    enum Filter: String, CaseIterable, Sendable { case all, inbox, kb, idle }

    struct Deps {
        var load: () async throws -> SourcesGraph
    }

    /// One extension and its sources the filter keeps.
    struct Group: Identifiable, Equatable {
        var ext: SourcesGraph.Extension
        var sources: [SourcesGraph.Source]
        var id: String { ext.id }
    }

    private(set) var graph: SourcesGraph?
    private(set) var failed: String?
    var filter: Filter = .all
    var picked: String?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps) { self.deps = deps }

    func load() async {
        if graph == nil { failed = nil }  // Retry shows the spinner again
        do {
            graph = try await deps.load()
            failed = nil
            if let p = picked, graph?.sources.contains(where: { $0.id == p }) != true { picked = nil }
        } catch {
            // a load a newer one replaced (the view's task restarts) is no failure
            if graph == nil, !Task.isCancelled { failed = error.localizedDescription }
        }
    }

    func keeps(_ s: SourcesGraph.Source, _ f: Filter) -> Bool {
        switch f {
        case .all: true
        case .inbox: s.to.contains("inbox")
        case .kb: s.to.contains("kb")
        case .idle: !s.reading
        }
    }

    func count(_ f: Filter) -> Int { graph?.sources.filter { keeps($0, f) }.count ?? 0 }

    /// The extensions in the server's order, each with the sources the filter keeps; one with none is left out.
    var groups: [Group] {
        guard let g = graph else { return [] }
        let extOf = Dictionary(g.reads.map { ($0.id, $0.extensionID) }, uniquingKeysWith: { a, _ in a })
        return g.extensions.compactMap { e in
            let mine = g.sources.filter { extOf[$0.read] == e.id && keeps($0, filter) }
            return mine.isEmpty ? nil : Group(ext: e, sources: mine)
        }
    }

    func read(of s: SourcesGraph.Source) -> SourcesGraph.Read? { graph?.reads.first { $0.id == s.read } }

    func ext(of s: SourcesGraph.Source) -> SourcesGraph.Extension? {
        guard let r = read(of: s) else { return nil }
        return graph?.extensions.first { $0.id == r.extensionID }
    }

    var pickedSource: SourcesGraph.Source? { picked.flatMap { p in graph?.sources.first { $0.id == p } } }

    /// Where a source's things go, by the destinations' names ("Inbox, Knowledge base").
    func destinations(of s: SourcesGraph.Source) -> String {
        let names = s.to.map { d in graph?.destinations.first { $0.id == d }?.name ?? d }
        return names.formatted(.list(type: .and, width: .narrow))
    }

    /// The first source that may bring the same things, by name.
    func duplicate(of s: SourcesGraph.Source) -> String? {
        s.duplicates.lazy.compactMap { id in self.graph?.sources.first { $0.id == id }?.name }.first
    }
}

struct SourcesGraphView: View {
    var manager: String?
    @Environment(AppModel.self) private var app
    @State private var model: SourcesGraphModel?

    var body: some View {
        Group {
            if let model {
                SourcesGraphContent(model: model, assistant: app.persona.name, manager: manager)
            } else {
                Color.clear
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Source Relationships"))
        // Loads when shown and again after subscriptions change: a burst of their broadcasts reloads once, as the entry
        // rows do (SubscriptionsEntry.swift).
        .task(id: SubscriptionsRefresh.shared.revision) {
            if let model {
                if model.graph != nil {
                    try? await Task.sleep(for: .seconds(1))
                    guard !Task.isCancelled else { return }
                }
                await model.load()
                return
            }
            let api = app.api?.currentAssistantSettings()
            let made = SourcesGraphModel(deps: .init(load: {
                guard let api else { throw URLError(.notConnectedToInternet) }
                return try await api.sourcesGraph()
            }))
            model = made
            await made.load()
        }
    }
}

private struct SourcesGraphContent: View {
    @Bindable var model: SourcesGraphModel
    var assistant: String?
    var manager: String?
    @Environment(AppModel.self) private var app

    var body: some View {
        LoadableContent(state: state, retry: { Task { await model.load() } }) { _ in
            if model.count(.all) == 0 {
                EmptyStateView(symbol: Theme.Icon.inbox, title: String(localized: "No Sources Yet [Source Relationships]"),
                               message: String(localized: "Nothing brings things into the Inbox or the knowledge base yet."))
                    .frame(maxHeight: .infinity)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: Theme.Space.m) {
                        Text("Where each source comes from, how it's read and where it goes. Tap one to see its whole path.")
                            .font(Theme.Font.secondary)
                            .foregroundStyle(.secondary)
                        filters
                        ForEach(model.groups) { group in
                            SourcesGraphGroup(model: model, group: group)
                        }
                    }
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.vertical, Theme.Space.m)
                }
            }
        }
        .sheet(item: Binding(get: { model.pickedSource }, set: { model.picked = $0?.id })) { s in
            SourcesGraphDetails(model: model, source: s, assistant: assistant, manager: manager)
                .presentationDetents([.medium, .large])
                // A solid panel, as the web's details card: on the half-height glass sheet the page's badges showed
                // through behind its words.
                .presentationBackground(Theme.card)
        }
    }

    /// The design system's one way to show loading and a failure (with Retry).
    private var state: Loadable<SourcesGraph> {
        if let g = model.graph { return .loaded(g) }
        if let f = model.failed { return .failed(String(localized: "Couldn't read the relationships: \(f)")) }
        return .loading
    }

    private var filters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.s) {
                ForEach(SourcesGraphModel.Filter.allCases, id: \.self) { f in
                    FilterChip(title: title(f), count: model.count(f), on: model.filter == f) { model.filter = f }
                        .accessibilityIdentifier("graph.filter.\(f.rawValue)")
                }
            }
        }
        .scrollClipDisabled()  // the chips scroll out to the screen's edge, not the page's margin
    }

    private func title(_ f: SourcesGraphModel.Filter) -> String {
        switch f {
        case .all: String(localized: "All [Source Relationships]")
        case .inbox: String(localized: "To Inbox [Source Relationships]")
        case .kb: String(localized: "To Knowledge [Source Relationships]")
        case .idle: String(localized: "Can't Read [Source Relationships]")
        }
    }
}

/// One extension: its tile and count, then a row per source hanging off a line from it.
private struct SourcesGraphGroup: View {
    var model: SourcesGraphModel
    var group: SourcesGraphModel.Group
    @ScaledMetric(relativeTo: .body) private var rail: CGFloat = 15

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header.padding(.bottom, Theme.Space.xs)
            ForEach(Array(group.sources.enumerated()), id: \.element.id) { i, s in
                Button { model.picked = s.id } label: {
                    SourcesGraphRow(model: model, source: s)
                        .padding(.leading, rail + Theme.Space.m)
                        .background(alignment: .leading) {
                            SourcesGraphRail(reading: s.reading, more: group.sources[(i + 1)...].contains { $0.reading },
                                             last: i == group.sources.count - 1)
                                .frame(width: rail)
                                .padding(.leading, rail / 2)
                        }
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("graph.source.\(s.id)")
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)  // every card the page's width, stacked rows or not
        .padding(Theme.Space.m)
        .surface(in: .rect(cornerRadius: Theme.Radius.card))
    }

    /// The extension's tile, name and count.
    @ViewBuilder private var header: some View {
        let tile = IconTile(group.ext.id == "persona" ? Theme.Icon.persona : Theme.Icon.plugins, tint: Theme.accent)
        let count = Text(group.ext.line).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
        HStack(spacing: Theme.Space.sm) {
            tile
            Text(group.ext.name).font(Theme.Font.bodyStrong).lineLimit(2)
            Spacer(minLength: Theme.Space.s)
            count
        }
    }
}

/// A row's piece of the tree: the elbow into it, and the trunk on to the rows below. Faint where nothing reads.
private struct SourcesGraphRail: View {
    var reading: Bool
    var more: Bool
    var last: Bool

    var body: some View {
        Canvas { ctx, size in
            let x: CGFloat = 0.75, mid = size.height / 2, r = min(Theme.Radius.badge, size.width / 2)
            var elbow = Path()
            elbow.move(to: CGPoint(x: x, y: 0))
            elbow.addLine(to: CGPoint(x: x, y: mid - r))
            elbow.addQuadCurve(to: CGPoint(x: x + r, y: mid), control: CGPoint(x: x, y: mid))
            elbow.addLine(to: CGPoint(x: size.width, y: mid))
            ctx.stroke(elbow, with: .color(Self.line(reading)), lineWidth: Theme.Size.lineMedium)
            if !last {
                var trunk = Path()
                trunk.move(to: CGPoint(x: x, y: mid - r))
                trunk.addLine(to: CGPoint(x: x, y: size.height))
                ctx.stroke(trunk, with: .color(Self.line(more)), lineWidth: Theme.Size.lineMedium)
            }
        }
        .accessibilityHidden(true)
    }

    /// A line that leads to a source reading now, and a fainter one to sources that can't read (signed out, failing).
    static func line(_ reading: Bool) -> Color { Color.secondary.opacity(reading ? 0.45 : 0.2) }
}

/// A source: how it is read, its name and badge, and where its things go.
private struct SourcesGraphRow: View {
    var model: SourcesGraphModel
    var source: SourcesGraph.Source

    var body: some View {
        let read = model.read(of: source)
        let tool = read.map { $0.direct ? String(localized: "Direct [Source Relationships]") : $0.title } ?? ""
        HStack(spacing: Theme.Space.sm) {
            Chip(text: tool, look: .outline, compact: true)
            Text(source.name).font(Theme.Font.bodyStrong).foregroundStyle(.primary).lineLimit(1)
            if let b = source.badge {
                Chip(text: b.text, look: b.tone == nil || b.tone == "neutral" ? .neutral : .tint(ExtensionUIStyle.tone(b.tone)))
            }
            Spacer(minLength: Theme.Space.s)
            goes()
        }
        .padding(.vertical, Theme.Space.s)
        .frame(minHeight: Theme.Size.tapTarget)
        .accessibilityElement(children: .combine)
        .accessibilityValue(model.destinations(of: source))
    }

    /// The Inbox and knowledge base icons, blue while the source reads; both always hold their place (the icons line up
    /// down the card).
    private func goes() -> some View {
        HStack(spacing: Theme.Space.xs) {
            ForEach(["inbox", "kb"], id: \.self) { d in
                ThemedIcon(d == "kb" ? Theme.Icon.knowledge : Theme.Icon.inbox)
                    .font(Theme.Font.label)
                    .foregroundStyle(source.reading ? Theme.accent : Color.secondary)
                    .opacity(source.to.contains(d) ? 1 : 0)
            }
        }
        .accessibilityHidden(true)
    }
}

/// A picked source: its path, its line, a possible duplicate, and what to do with it.
private struct SourcesGraphDetails: View {
    var model: SourcesGraphModel
    var source: SourcesGraph.Source
    var assistant: String?
    var manager: String?
    @Environment(AppModel.self) private var app
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        let read = model.read(of: source)
        let how = read.map { r in
            r.direct ? String(localized: "Persona reads it directly") : r.push ? r.title : String(localized: "With “\(r.title)”")
        } ?? ""
        let steps: [(String, String)] = [
            (String(localized: "Extension [Source Relationships]"), model.ext(of: source)?.name ?? ""),
            (String(localized: "How it's read"), how),
            (String(localized: "Goes to"), model.destinations(of: source)),
        ]
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                HStack(spacing: Theme.Space.sm) {
                    HStack(spacing: Theme.Space.sm) {
                        ThemedIcon(ExtensionUIStyle.icon(source.icon)).font(Theme.Font.heading).foregroundStyle(Theme.accent)
                        Text(source.name).font(Theme.Font.heading).lineLimit(2)
                    }
                    if let b = source.badge {
                        Chip(text: b.text, look: b.tone == nil || b.tone == "neutral" ? .neutral : .tint(ExtensionUIStyle.tone(b.tone)))
                    }
                }
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    ForEach(steps, id: \.0) { k, v in
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text(k).font(Theme.Font.label).foregroundStyle(.secondary)
                            Text(v).font(Theme.Font.bodyStrong)
                        }
                    }
                }
                Text(source.line).font(Theme.Font.secondary).foregroundStyle(.secondary)
                if let dup = model.duplicate(of: source) {
                    Text("May bring the same things as “\(dup)”. If they do, turn one of them off.")
                        .font(Theme.Font.secondary)
                        .foregroundStyle(Theme.cautionText)
                        .padding(Theme.Space.sm)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.caution.opacity(0.14), in: .rect(cornerRadius: Theme.Radius.badge))
                }
                VStack(spacing: Theme.Space.s) {
                    if let ask = source.ask.flatMap(URL.init(string:)), SubscriptionsUI.askText(ask) != nil {
                        Button {
                            dismiss()
                            _ = app.openIfAsk(ask)
                        } label: {
                            Text(assistant.map { $0.isEmpty ? String(localized: "Set Up with the Assistant") : String(localized: "Set Up with \($0)") } ?? String(localized: "Set Up with the Assistant"))
                                .lineLimit(1)
                                .minimumScaleFactor(0.8)
                                .frame(maxWidth: .infinity)
                        }
                        .themedButton(prominent: true)
                        .accessibilityIdentifier("graph.ask")
                    }
                    Button {
                        dismiss()
                        app.settingsPath.append(SettingsPage.subscriptions(state: source.state))
                    } label: {
                        Text("Open Settings [Source Relationships]").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                    }
                    .themedButton()
                    .accessibilityIdentifier("graph.open")
                }
            }
            .padding(Theme.Space.page)
        }
    }
}
