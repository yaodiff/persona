import Foundation
import Observation
import PersonaWire

// The personal-agents list prefs shared with the web (docs/protocol/protocol.md 「Personal Agent list preferences」): pins, the view
// (Group By / Sort / Display / Source), and custom sections. Kept on the hub (GET/PUT /api/v1/agents/prefs), cached in
// UserDefaults; an older hub without the endpoint leaves them local.

nonisolated enum AgentGroupBy: String, Codable, CaseIterable, Sendable {
    case date, provider, none
    var label: String { switch self { case .date: String(localized: "Date"); case .provider: String(localized: "Source"); case .none: String(localized: "None [Personal Agent Prefs fcd40e]") } }
}

nonisolated enum AgentSortBy: String, Codable, CaseIterable, Sendable {
    case updated, unread, name
    var label: String { switch self { case .updated: String(localized: "Recently Updated"); case .unread: String(localized: "Unread Count"); case .name: String(localized: "Name [Personal Agent Prefs d44e9b]") } }
}

nonisolated enum AgentShow: String, Codable, CaseIterable, Sendable {
    case all, unread, sendable
    var label: String { switch self { case .all: String(localized: "All"); case .unread: String(localized: "Unread Only"); case .sendable: String(localized: "Sendable Only") } }
}

nonisolated struct PersonalAgentView: Codable, Hashable, Sendable {
    var group: String?
    var sort: String?
    var show: String?
    var providers: [String]?

    var groupBy: AgentGroupBy { group.flatMap(AgentGroupBy.init(rawValue:)) ?? .date }
    var sortBy: AgentSortBy { sort.flatMap(AgentSortBy.init(rawValue:)) ?? .updated }
    var showMode: AgentShow { show.flatMap(AgentShow.init(rawValue:)) ?? .all }
    var providerFilter: [String] { providers ?? [] }
    var isDefault: Bool { groupBy == .date && sortBy == .updated && showMode == .all && providerFilter.isEmpty }
}

nonisolated struct PersonalAgentSection: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
}

nonisolated struct PersonalAgentPrefs: Codable, Hashable, Sendable {
    var pins: [String] = []
    var view = PersonalAgentView()
    var sections: [PersonalAgentSection] = []
    var assign: [String: String] = [:]
    /// Removed from the list: session id → when (ms). Changed only through hide / unhide; the hub keeps its own on a save.
    var hidden: [String: Int64]?

    init() {}
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        pins = try c.decodeIfPresent([String].self, forKey: .pins) ?? []
        view = try c.decodeIfPresent(PersonalAgentView.self, forKey: .view) ?? PersonalAgentView()
        sections = try c.decodeIfPresent([PersonalAgentSection].self, forKey: .sections) ?? []
        assign = try c.decodeIfPresent([String: String].self, forKey: .assign) ?? [:]
        hidden = try c.decodeIfPresent([String: Int64].self, forKey: .hidden)
    }
    enum CodingKeys: String, CodingKey { case pins, view, sections, assign, hidden }

    func isPinned(_ id: String) -> Bool { pins.contains(id) }
    mutating func togglePin(_ id: String) {
        if let i = pins.firstIndex(of: id) { pins.remove(at: i) } else { pins.insert(id, at: 0) }
    }
    mutating func assign(_ id: String, to section: String?) {
        if let section { assign[id] = section } else { assign[id] = nil }
    }
    mutating func addSection(_ name: String, id: String = "s" + String(Int(Date().timeIntervalSince1970 * 1000), radix: 36)) {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if !n.isEmpty { sections.append(PersonalAgentSection(id: id, name: n)) }
    }
    mutating func removeSection(_ id: String) {
        sections.removeAll { $0.id == id }
        assign = assign.filter { $0.value != id }
    }
    /// Reset to Default: the view only; pins and sections stay.
    mutating func resetView() { view = PersonalAgentView() }
    mutating func toggleProvider(_ p: String, all: [String]) {
        var cur = view.providerFilter.isEmpty ? all : view.providerFilter
        if let i = cur.firstIndex(of: p) { cur.remove(at: i) } else { cur.append(p) }
        view.providers = Set(cur) == Set(all) ? [] : cur
    }
}

nonisolated struct PersonalAgentGroup: Identifiable, Hashable, Sendable {
    var id: String // pinned, sec:<id>, p:<provider>, d:<bucket>, all
    var label: String
    var sessions: [PersonalAgentSession]
    var unread: Int { sessions.reduce(0) { $0 + $1.unreadCount } }
}

nonisolated enum PersonalAgentArrange {
    static func providerLabel(_ p: String) -> String {
        ["grok_bot": "Grok Bot", "muse": "Muse", "cue": "Cue", "dots": "Dots", "claude_duet": "Claude Duet"][p] ?? (p.isEmpty ? String(localized: "Other") : p)
    }

    static func bucket(_ ms: Int64, now: Date, calendar: Calendar = .current) -> (key: String, label: String, order: Int) {
        let t = Date(timeIntervalSince1970: Double(ms) / 1000)
        let today = calendar.startOfDay(for: now)
        let day: TimeInterval = 86_400
        if t >= today { return ("today", String(localized: "Today"), 0) }
        if t >= today.addingTimeInterval(-day) { return ("yesterday", String(localized: "Yesterday"), 1) }
        if t >= today.addingTimeInterval(-7 * day) { return ("week", String(localized: "Last 7 Days"), 2) }
        if t >= today.addingTimeInterval(-30 * day) { return ("month", String(localized: "Last 30 Days"), 3) }
        return ("older", String(localized: "Earlier"), 4)
    }

    /// Same order as the web's arrangeSessions: filter, Pinned (pin order), custom sections, then the grouping.
    static func arrange(_ list: [PersonalAgentSession], prefs: PersonalAgentPrefs, query: String = "", now: Date = Date()) -> [PersonalAgentGroup] {
        let v = prefs.view
        let q = query.trimmingCharacters(in: .whitespaces)
        let keep = list.filter { s in
            (q.isEmpty || s.title.localizedCaseInsensitiveContains(q) || s.provider.localizedCaseInsensitiveContains(q)
                || providerLabel(s.provider).localizedCaseInsensitiveContains(q))
                && (v.showMode != .unread || s.unreadCount > 0) && (v.showMode != .sendable || s.canSend)
                && (v.providerFilter.isEmpty || v.providerFilter.contains(s.provider))
        }
        let byID = Dictionary(keep.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        var out: [PersonalAgentGroup] = []
        let pinned = prefs.pins.compactMap { byID[$0] }
        var used = Set(pinned.map(\.id))
        if !pinned.isEmpty { out.append(PersonalAgentGroup(id: "pinned", label: String(localized: "Pinned [Personal Agent List View fb47db]"), sessions: pinned)) }
        let cmp: (PersonalAgentSession, PersonalAgentSession) -> Bool = { a, b in
            switch v.sortBy {
            case .unread: a.unreadCount != b.unreadCount ? a.unreadCount > b.unreadCount : a.rankTimeMs > b.rankTimeMs
            case .name: a.title != b.title ? a.title.localizedStandardCompare(b.title) == .orderedAscending : a.rankTimeMs > b.rankTimeMs
            case .updated: a.rankTimeMs > b.rankTimeMs
            }
        }
        for sec in prefs.sections {
            let ss = keep.filter { !used.contains($0.id) && prefs.assign[$0.id] == sec.id }.sorted(by: cmp)
            used.formUnion(ss.map(\.id))
            if !ss.isEmpty { out.append(PersonalAgentGroup(id: "sec:\(sec.id)", label: sec.name, sessions: ss)) }
        }
        let rest = keep.filter { !used.contains($0.id) }.sorted(by: cmp)
        switch v.groupBy {
        case .none:
            if !rest.isEmpty { out.append(PersonalAgentGroup(id: "all", label: out.isEmpty ? "" : String(localized: "Other"), sessions: rest)) }
        case .provider:
            var order: [String] = []
            var by: [String: [PersonalAgentSession]] = [:]
            for s in rest {
                if by[s.provider] == nil { order.append(s.provider) }
                by[s.provider, default: []].append(s)
            }
            var gs = order.map { PersonalAgentGroup(id: "p:\($0)", label: providerLabel($0), sessions: by[$0]!) }
            if v.sortBy == .name { gs.sort { $0.label.localizedStandardCompare($1.label) == .orderedAscending } }
            else { gs.sort { ($0.sessions.map(\.rankTimeMs).max() ?? 0) > ($1.sessions.map(\.rankTimeMs).max() ?? 0) } }
            out += gs
        case .date:
            var by: [String: (label: String, order: Int, ss: [PersonalAgentSession])] = [:]
            for s in rest {
                let b = bucket(s.rankTimeMs, now: now)
                by[b.key, default: (b.label, b.order, [])].ss.append(s)
            }
            out += by.sorted { $0.value.order < $1.value.order }.map { PersonalAgentGroup(id: "d:\($0.key)", label: $0.value.label, sessions: $0.value.ss) }
        }
        return out
    }
}

@MainActor @Observable
final class PersonalAgentPrefsStore {
    static let shared = PersonalAgentPrefsStore()

    private(set) var prefs: PersonalAgentPrefs
    private let defaults: UserDefaults
    private var lastConfirmed: PersonalAgentPrefs
    private var hubBacked = true
    private var seq = 0
    private var pendingSaves = 0
    private var saveTask: Task<Void, Never>?

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        _ = NativeStateMigration.defaults(defaults)
        let cached = NativeStateMigration.readPrefs(PersonalAgentPrefs.self, defaults: defaults) ?? PersonalAgentPrefs()
        prefs = cached
        lastConfirmed = cached
    }

    private func cache(_ p: PersonalAgentPrefs) {
        prefs = p
        if let d = try? JSONEncoder().encode(p) { NativeStateMigration.writePrefs(d, defaults: defaults) }
    }

    func load(_ api: APIClient?) async {
        guard let api else { return }
        let mine = seq
        if pendingSaves > 0 {
            await saveTask?.value // a read started during a save must not fetch the hub's older snapshot
            guard mine == seq else { return }
        }
        do {
            let p = try await api.personalAgentPrefs()
            guard mine == seq, pendingSaves == 0 else { return } // a read that began before an edit must not erase it
            lastConfirmed = p
            hubBacked = true
            cache(p)
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            guard mine == seq, pendingSaves == 0 else { return }
            hubBacked = false
        } catch {}
    }

    func update(_ api: APIClient?, _ change: (inout PersonalAgentPrefs) -> Void, onFailure: @escaping (String) -> Void = { _ in }) {
        var p = prefs
        change(&p)
        cache(p)
        guard hubBacked, let api else { return }
        seq += 1
        let mine = seq
        pendingSaves += 1
        let previous = saveTask
        saveTask = Task {
            await previous?.value // full snapshots must reach the hub in edit order, not merely update the UI in that order
            var reload = false
            do {
                let saved = try await api.putPersonalAgentPrefs(p)
                lastConfirmed = saved
                if mine == seq { cache(saved) }
            } catch let e as APIError where e.status == 404 || e.status == 405 {
                if mine == seq { hubBacked = false }
            } catch {
                if mine == seq {
                    cache(lastConfirmed) // also clears an earlier unsaved edit; it does not depend on the recovery GET
                    if !error.isCancellation { onFailure(error.localizedDescription) }
                    reload = true
                }
            }
            pendingSaves -= 1
            if reload {
                await load(api) // no pending save here, so the read does not wait for this task itself
            }
            if mine == seq { saveTask = nil }
        }
    }
}

extension APIClient {
    func personalAgentPrefs() async throws -> PersonalAgentPrefs {
        try await get("/api/v1/agents/prefs", as: PersonalAgentPrefs.self)
    }

    func putPersonalAgentPrefs(_ p: PersonalAgentPrefs) async throws -> PersonalAgentPrefs {
        try JSON.decoder.decode(PersonalAgentPrefs.self, from: try await request("PUT", "/api/v1/agents/prefs", body: p))
    }
}
