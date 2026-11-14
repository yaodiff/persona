import SwiftUI

/// The knowledge map (#7251, the web's Knowledge map): the topic tree (or the projects tree) as one outline. On the iPhone
/// the web's two-sided map is turned upright: topics, subtopics and points one under another, folding open, reading at
/// the system's text size. On a wide iPad a detail column beside it shows what is selected (the web map's side panel).
struct KnowledgeMapView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.knowledgeSettingsAPI) private var inheritedAPI
    @State private var capturedAPI: APIClient?

    enum Shown: String, Hashable { case topics, projects }

    @State private var shown: Shown = .topics
    @State private var trees: [Shown: KBMapNode] = [:]
    @State private var open: [Shown: Set<String>] = [:]
    @State private var failed: [Shown: Int] = [:]
    @State private var error: String?
    /// Organize knowledge points by topic, as the hub has it (nil until read): off explains an empty map.
    @State private var curating: Bool?
    @State private var selected: KBMapNode?
    @State private var sheet: KBOpenPoint?
    @State private var pushed: KBMapTarget?
    @State private var settings = false
    /// Two columns: room for both (`Theme.Size.knowledgeMapTwoColumns`).
    @State private var wide = false

    var body: some View {
        GeometryReader { geo in
            let twoColumns = geo.size.width >= Theme.Size.knowledgeMapTwoColumns
            Group {
                if twoColumns {
                    HStack(spacing: Theme.Space.flush) {
                        outline(wide: true).frame(maxWidth: .infinity)
                        Divider()
                        detailColumn.frame(maxWidth: .infinity)
                    }
                } else {
                    outline(wide: false)
                }
            }
            .onChange(of: twoColumns, initial: true) { _, now in wide = now }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "Knowledge Map"))
        .navigationBarTitleDisplayMode(.inline)
        .navigationDestination(item: $pushed) { t in KnowledgeDetailView(entry: t.entry) }
        .navigationDestination(isPresented: $settings) { KnowledgeSelectionView() }
        .sheet(item: $sheet) { p in
            KBPointSheet(point: p.point, links: p.links, crumbs: p.crumbs) { e in
                pushed = KBMapTarget(kind: e.kind, id: e.id, name: e.name)
            }
        }
        .task(id: shown) { await load(shown) }
        .refreshable { await load(shown, again: true) }
        .environment(\.knowledgeSettingsAPI, capturedAPI)
    }

    // MARK: Outline

    private func outline(wide: Bool) -> some View {
        List {
            // The Knowledge page's own choice of kind (`KBKindBar`) and the Inbox's filters are chips that grow with the
            // text size; the system's segmented control kept its small fixed size here (#7251 review).
            Section {
                HStack(spacing: Theme.Space.s) {
                    FilterChip(title: String(localized: "Topics [Knowledge Map]"), symbol: Theme.Icon.topic, on: shown == .topics) { shown = .topics }
                        .accessibilityIdentifier("kb.map.view.topics")
                    FilterChip(title: String(localized: "Projects [Knowledge Map]"), symbol: Theme.Icon.space, on: shown == .projects) { shown = .projects }
                        .accessibilityIdentifier("kb.map.view.projects")
                    Spacer(minLength: Theme.Space.flush)
                }
                .accessibilityElement(children: .contain)
                .accessibilityLabel(Text("Map"))
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
            if let tree = trees[shown] {
                if tree.kids.isEmpty {
                    empty
                } else {
                    Section { stats(tree) } header: { Text(tree.title + " · " + (tree.meta ?? "")) }
                    Section {
                        ForEach(KBMap.rows(tree, open: open[shown] ?? [])) { row in
                            outlineRow(row, wide: wide)
                        }
                    } footer: {
                        if let n = failed[shown], n > 0 { Text("\(n) topics couldn't be read.") }
                    }
                }
            } else if let error {
                Section { Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger).font(Theme.Font.caption) }
            } else {
                Section { ProgressView().frame(maxWidth: .infinity).padding(.vertical, Theme.Space.xl) }.listRowBackground(Color.clear)
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
    }

    private func outlineRow(_ row: KBOutlineRow, wide: Bool) -> some View {
        let n = row.node
        let isOpen = open[shown]?.contains(n.id) == true
        return HStack(alignment: n.point == nil ? .center : .firstTextBaseline, spacing: Theme.Space.s) {
            if row.depth == 0 {
                Circle().fill(color(n.tone)).scaledFrame(width: Theme.Size.dotL, height: Theme.Size.dotL)
            }
            if let p = n.point { KBPointKindPill(kind: p.kind) }
            Text(n.title)
                .font(row.depth == 0 ? Theme.Font.heading : n.point != nil ? Theme.Font.secondary : Theme.Font.body)
                .lineLimit(n.point != nil ? 4 : 2)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let meta = n.meta, n.point == nil {
                Text(meta).font(Theme.Font.caption).foregroundStyle(.secondary).monospacedDigit().lineLimit(1)
            }
            if row.folds {
                Button { toggle(n) } label: {
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                        .rotationEffect(.degrees(isOpen ? 90 : 0))
                        .tapTargetFrame()
                }
                .buttonStyle(.plain)
                .accessibilityLabel(n.title)
                .accessibilityValue(Text(isOpen ? String(localized: "Expanded") : String(localized: "Collapsed")))
                .accessibilityIdentifier("kb.map.fold")
            }
        }
        .padding(.vertical, Theme.Space.xs)
        .padding(.leading, Theme.Space.l * CGFloat(row.depth))
        // The rails down the left, one per level in the branch's tone: drawn behind the row, as tall as it is.
        .background(alignment: .leading) {
            HStack(spacing: Theme.Space.flush) {
                ForEach(0..<row.depth, id: \.self) { _ in
                    Rectangle().fill(color(n.tone).opacity(0.35)).frame(width: Theme.Size.rule).frame(width: Theme.Space.l)
                }
            }
        }
        .contentShape(.rect)
        .onTapGesture { tap(n, wide: wide) }
        .listRowBackground(wide && selected?.id == n.id ? color(n.tone).opacity(0.12) : nil)
        .listRowInsets(EdgeInsets(top: Theme.Space.flush, leading: Theme.Space.l, bottom: Theme.Space.flush, trailing: Theme.Space.s))
        .listRowSeparator(row.depth == 0 ? .visible : .hidden)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier(n.point != nil ? "kb.map.point" : "kb.map.node")
    }

    /// A point opens its detail; a topic or project its page (the iPad selects it); anything else folds.
    private func tap(_ n: KBMapNode, wide: Bool) {
        if wide { selected = n; return }
        if let p = n.point {
            sheet = KBOpenPoint(point: p, links: n.links, crumbs: n.crumbs)
        } else if let target = n.open {
            pushed = target
        } else if !n.kids.isEmpty {
            toggle(n)
        }
    }

    private func toggle(_ n: KBMapNode) {
        withAnimation(Theme.Motion.standard) {
            var set = open[shown] ?? []
            if set.contains(n.id) { set.remove(n.id) } else { set.insert(n.id) }
            open[shown] = set
        }
    }

    // MARK: Numbers

    /// The map's counts: topics, points and their sources; or active projects, with a summary, and the main chat's notes.
    private func stats(_ tree: KBMapNode) -> some View {
        let items: [(Int, String)]
        if shown == .topics {
            let points = tree.pointsUnder
            let sources = Set(points.flatMap { ($0.point?.sources ?? []).map { $0.kind + "/" + $0.id } })
            items = [(tree.kids.count, String(localized: "Topics [Knowledge Map Count]")), (points.count, String(localized: "Knowledge Points")),
                     (sources.count, String(localized: "Sources [Knowledge Map]"))]
        } else {
            let projects = tree.kids.flatMap { $0.kind == .branch && $0.id == "branch:active" ? $0.kids.flatMap(\.kids) : $0.kids }
            let active = tree.kids.first { $0.id == "branch:active" }?.kids.flatMap(\.kids).count ?? 0
            let summaries = projects.filter { $0.kids.contains { $0.meta == String(localized: "Summary [Knowledge Map]") } }.count
            let notes = projects.compactMap { $0.kids.first { $0.id.hasSuffix("#notes") }.flatMap { Int($0.meta ?? "") } }.reduce(0, +)
            items = [(active, String(localized: "Active [Knowledge Map]")), (summaries, String(localized: "With a Summary [Knowledge Map]")),
                     (notes, String(localized: "Main Chat Notes [Knowledge Map]"))]
        }
        return HStack(alignment: .top, spacing: Theme.Space.m) {
            ForEach(items.indices, id: \.self) { i in
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("\(items[i].0)").font(Theme.Font.title).monospacedDigit().lineLimit(1)
                    Text(items[i].1).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("kb.map.stats")
    }

    // MARK: Empty

    @ViewBuilder private var empty: some View {
        Section {
            if shown == .topics, curating == false {
                EmptyStateView(symbol: Theme.Icon.knowledgeMap, title: String(localized: "Knowledge Points Are Off"),
                               message: String(localized: "Turn on “Organize knowledge points by topic” in Settings › Knowledge Selection, and Persona starts curating in the background."),
                               action: (title: String(localized: "Open Knowledge Selection"), run: { settings = true }))
            } else if shown == .topics {
                EmptyStateView(symbol: Theme.Icon.knowledgeMap, title: String(localized: "No Topics Yet"),
                               message: String(localized: "In the background, Persona pulls durable knowledge out of your project timelines and chats and files it by topic. The map fills in as it does."))
            } else {
                EmptyStateView(symbol: Theme.Icon.knowledgeMap, title: String(localized: "No Projects Yet"),
                               message: String(localized: "Projects appear as Persona sorts them out of your chats and Agents' work."))
            }
        }
        .listRowBackground(Color.clear)
    }

    // MARK: Detail column (iPad)

    private var detailColumn: some View {
        List {
            if let n = selected, n.id != trees[shown]?.id {
                if let p = n.point {
                    KBPointDetail(point: p, links: n.links, crumbs: n.crumbs) { e in pushed = KBMapTarget(kind: e.kind, id: e.id, name: e.name) }
                } else {
                    nodeDetail(n)
                }
            } else if let tree = trees[shown], !tree.kids.isEmpty {
                Section {
                    Text(shown == .topics
                         ? String(localized: "Knowledge Persona pulled out of project timelines and chats, filed by topic rather than by project: concepts, decisions, how-tos, pitfalls and facts. Select a topic or a point to see it here.")
                         : String(localized: "Each project's goal, status and the main chat's notes, with its curated summary by section. Select a project to see it here."))
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                } header: { Text("What This Shows [Knowledge Map]") }
            }
        }
        .listStyle(.insetGrouped)
        .themedPage()
        .accessibilityIdentifier("kb.map.detail")
    }

    @ViewBuilder private func nodeDetail(_ n: KBMapNode) -> some View {
        Section {
            VStack(alignment: .leading, spacing: Theme.Space.sm) {
                if !n.crumbs.isEmpty {
                    Text(n.crumbs.joined(separator: " › ")).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                Text(n.title).font(Theme.Font.title).fixedSize(horizontal: false, vertical: true)
                if let meta = n.meta, !n.isTopic { KBPill(text: meta) }
                if let body = n.body, !body.isEmpty, body != n.title {
                    Text(body).font(Theme.Font.body).foregroundStyle(.secondary).readingContent()
                }
            }
            .padding(.vertical, Theme.Space.xs)
        }
        .listRowBackground(Color.clear)
        if n.isTopic {
            let points = n.pointsUnder
            let kinds = Dictionary(grouping: points, by: { $0.point?.kind ?? "" })
            if !kinds.isEmpty {
                Section(String(localized: "\(points.count) knowledge points")) {
                    LarkFlowLayout(spacing: Theme.Space.sm, lineSpacing: Theme.Space.sm) {
                        ForEach((KBPointKind.known + [""]).filter { kinds[$0] != nil }, id: \.self) { k in
                            KBPill(text: "\(KBPointKind.label(k)) \(kinds[k]?.count ?? 0)", tint: KBPointKind.warns(k) ? Theme.caution : nil)
                        }
                    }
                    .padding(.vertical, Theme.Space.xxs)
                }
            }
            let subs = n.kids.filter(\.isTopic)
            if !subs.isEmpty {
                Section("Subtopics [Knowledge Map]") {
                    ForEach(subs) { s in
                        Button { selected = s } label: {
                            LabeledContent(s.title) { Text(s.meta ?? "").monospacedDigit() }.contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            let direct = n.kids.filter { $0.point != nil }
            if !direct.isEmpty {
                Section("Knowledge Points") {
                    ForEach(direct) { k in
                        Button { selected = k } label: { KBPointRow(point: k.point!, links: k.links) }.buttonStyle(.plain)
                    }
                }
            }
        } else if !n.kids.isEmpty {
            Section {
                ForEach(n.kids) { k in
                    Button { selected = k } label: {
                        Text(k.title).font(Theme.Font.body).frame(maxWidth: .infinity, alignment: .leading).contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        if let target = n.open {
            Section {
                Button { pushed = target } label: {
                    Label("Open in Knowledge", systemImage: Theme.Icon.disclosure).lineLimit(1)
                }
                .accessibilityIdentifier("kb.map.openEntry")
            }
        }
    }

    // MARK: Loading

    private func color(_ tone: KBMapNode.Tone) -> Color {
        switch tone {
        case .accent: Theme.accent
        case .green: Theme.Hue.green
        case .orange: Theme.Hue.orange
        }
    }

    /// The list, then every entry's detail (at most six at a time); a detail that can't be read still gives its row and
    /// is counted under the outline.
    private func load(_ which: Shown, again: Bool = false) async {
        if capturedAPI == nil { capturedAPI = inheritedAPI ?? model.api?.currentAssistantSettings() }
        guard let api = capturedAPI, again || trees[which] == nil else { return }
        do {
            let kind: KBKind = which == .topics ? .topics : .projects
            let list = try await api.kbList(kind).items
            var details: [String: KBDetail] = [:]
            var missed = 0
            await withTaskGroup(of: (String, KBDetail?).self) { group in
                var next = list.makeIterator()
                func add() { if let e = next.next() { group.addTask { (e.id, try? await api.kbDetail(kind.rawValue, id: e.id).detail) } } }
                for _ in 0..<6 { add() }
                while let (id, d) = await group.next() {
                    if let d { details[id] = d } else { missed += 1 }
                    add()
                }
            }
            if which == .topics {
                curating = try? await api.assistant().kbTopics ?? true
                trees[which] = KBMap.topics(list.map { KBMap.Topic(entry: $0, detail: details[$0.id]) })
            } else {
                trees[which] = KBMap.projects(list, details: details)
            }
            if let tree = trees[which], open[which] == nil || again { open[which] = KBMap.initiallyOpen(tree) }
            failed[which] = missed
            error = nil
            #if DEBUG
            // `-showKnowledgeMapPoint YES`: the first point opened (screenshots), its sheet on the iPhone, selected on the iPad.
            if which == .topics, UserDefaults.standard.bool(forKey: "showKnowledgeMapPoint"), let first = trees[which]?.pointsUnder.first {
                if wide { selected = first } else if let p = first.point { sheet = KBOpenPoint(point: p, links: first.links, crumbs: first.crumbs) }
            }
            #endif
        } catch {
            if let shown = error.shown { self.error = shown }
        }
    }
}
