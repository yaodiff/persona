import Foundation
import PersonaWire

/// The "Fleet" Live Activity's content (hub/liveactivity.go FleetState, field for field): every agent that needs you,
/// is working, or finished unseen, summed up for the lock screen and the Dynamic Island. The hub computes it and
/// pushes it over APNs; the open app gets the same thing as the `fleet` WebSocket event.
///
/// Times are Unix seconds, not `Date`: ActivityKit decodes pushed JSON with a plain JSONDecoder, which reads a Date
/// as seconds since 2001. Every field decodes leniently so an older or newer hub never kills the activity.
nonisolated struct FleetState: Codable, Hashable, Sendable {
    var phase: Phase = .none
    var needsYou = 0
    var working = 0
    var unread = 0
    var items: [Item] = []
    var workingSince: Double?
    /// The lock screen may show agent names (Settings › Notifications › Show Details on Lock Screen); off, only counts.
    var details = false
    /// The assistant (Chat tab) is answering; pushed only to devices whose island shows it.
    var assistant = false
    /// The compact Dynamic Island's sides (Settings › Notifications › Dynamic Island, IslandPrefs).
    var left: IslandSlot = .status
    var right: IslandSlot = .status
    /// The numbers the island shows, rounded to two significant digits by the hub.
    var stats: Stats?
    /// The persona this device's activity draws (Settings › Avatar). The widget extension cannot read the app's settings, and
    /// on a phone it did not get them from the shared keychain either (build 98): the app stamps its own updates with
    /// it, and the hub echoes what the app told it (prefs `look`) in this device's pushes (docs/apple/live-activity.md「Avatar」).
    /// Nil from an older hub: the extension falls back to the keychain.
    var look: FleetLook?

    nonisolated struct Stats: Codable, Hashable, Sendable {
        var tpm = 0
        var tokens = 0
        var requests = 0
        var loads: [Load] = []

        nonisolated struct Load: Codable, Hashable, Sendable {
            var id: String
            var name = ""
            /// The 1-minute load average over the cores × 100, rounded to 10, the tenth of a core the apps print
            /// (hub fleetStats; an older hub rounds to 5); shown per core (`FleetText.perCore`), never as a percentage.
            var percent = 0
        }

        init(tpm: Int = 0, tokens: Int = 0, requests: Int = 0, loads: [Load] = []) {
            self.tpm = tpm
            self.tokens = tokens
            self.requests = requests
            self.loads = loads
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            tpm = try c.decodeIfPresent(Int.self, forKey: .tpm) ?? 0
            tokens = try c.decodeIfPresent(Int.self, forKey: .tokens) ?? 0
            requests = try c.decodeIfPresent(Int.self, forKey: .requests) ?? 0
            loads = (try? c.decodeIfPresent([Load].self, forKey: .loads)) ?? []
        }
    }

    nonisolated enum Phase: String, Codable, Hashable, Sendable {
        case needsYou, working, done
        case none = ""

        init(from decoder: Decoder) throws {
            self = Phase(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .none
        }
    }

    nonisolated struct Item: Codable, Hashable, Sendable, Identifiable {
        var id: String // "machine/session"
        var title = ""
        var place = ""
        var kind: Kind = .working
        var detail = ""
        /// The interaction Allow / Deny / Approve resolve.
        var interaction: String?
        /// The exact interaction occurrence those buttons drew; absent from an older hub.
        var interactionCreatedAt: String?
        var since: Double?
        /// A working item whose turn ended while its background work runs (#2935): `detail` says what runs
        /// ("parity-audit · Verify · 169/185"), and `since` is when its workflow started (#3433). Absent from older hubs.
        var background = false
        /// The bar under a background workflow (#3437): the counts `detail` prints. Nil otherwise, or from older hubs.
        var progress: Progress?

        /// A workflow's agents done of those it started so far (the total is unknown before it ends).
        nonisolated struct Progress: Codable, Hashable, Sendable {
            var done = 0
            var started = 0

            init(done: Int, started: Int) {
                self.done = done
                self.started = started
            }

            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                done = (try? c.decodeIfPresent(Int.self, forKey: .done)) ?? 0
                started = (try? c.decodeIfPresent(Int.self, forKey: .started)) ?? 0
            }

            /// How far the bar is filled, 0...1; nil while nothing has started (no bar to draw).
            var fraction: Double? {
                guard started > 0 else { return nil }
                return Double(min(max(done, 0), started)) / Double(started)
            }
        }

        nonisolated enum Kind: String, Codable, Hashable, Sendable {
            case permission, plan, question, screen, working, unread

            init(from decoder: Decoder) throws {
                self = Kind(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .screen
            }
        }

        init(id: String, title: String, place: String = "", kind: Kind, detail: String = "", interaction: String? = nil,
             interactionCreatedAt: String? = nil, since: Double? = nil, background: Bool = false, progress: Progress? = nil) {
            self.id = id
            self.title = title
            self.place = place
            self.kind = kind
            self.detail = detail
            self.interaction = interaction
            self.interactionCreatedAt = interactionCreatedAt
            self.since = since
            self.background = background
            self.progress = progress
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            place = try c.decodeIfPresent(String.self, forKey: .place) ?? ""
            kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .screen
            detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
            interaction = try c.decodeIfPresent(String.self, forKey: .interaction)
            interactionCreatedAt = try c.decodeIfPresent(String.self, forKey: .interactionCreatedAt)
            since = try c.decodeIfPresent(Double.self, forKey: .since)
            background = (try? c.decodeIfPresent(Bool.self, forKey: .background)) ?? false
            // A bar this app cannot read (a newer hub's shape) drops only the bar.
            progress = try? c.decodeIfPresent(Progress.self, forKey: .progress)
        }

        var needsYou: Bool { kind != .working && kind != .unread }
        /// Background work with a line to say what runs (#3437): drawn with 「后台」 and that line. An older hub's
        /// background item has no line, and is a working row as before.
        var runsInBackground: Bool { kind == .working && background && !detail.isEmpty }
        /// Its row has a second line (a request's ask, background work's line), when names are shown.
        var twoLines: Bool { needsYou || runsInBackground }
        var sinceDate: Date? { since.map { Date(timeIntervalSince1970: $0) } }

        /// machine id and session id, for the deep link.
        var key: (machine: String, session: String)? {
            guard let slash = id.firstIndex(of: "/") else { return nil }
            return (String(id[..<slash]), String(id[id.index(after: slash)...]))
        }
    }

    init(phase: Phase = .none, needsYou: Int = 0, working: Int = 0, unread: Int = 0, items: [Item] = [], workingSince: Double? = nil,
         details: Bool = false, assistant: Bool = false, left: IslandSlot = .status, right: IslandSlot = .status, stats: Stats? = nil,
         look: FleetLook? = nil) {
        self.phase = phase
        self.needsYou = needsYou
        self.working = working
        self.unread = unread
        self.items = items
        self.workingSince = workingSince
        self.details = details
        self.assistant = assistant
        self.left = left
        self.right = right
        self.stats = stats
        self.look = look
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        phase = try c.decodeIfPresent(Phase.self, forKey: .phase) ?? .none
        needsYou = try c.decodeIfPresent(Int.self, forKey: .needsYou) ?? 0
        working = try c.decodeIfPresent(Int.self, forKey: .working) ?? 0
        unread = try c.decodeIfPresent(Int.self, forKey: .unread) ?? 0
        items = try c.decodeIfPresent([Item].self, forKey: .items) ?? []
        workingSince = try c.decodeIfPresent(Double.self, forKey: .workingSince)
        details = try c.decodeIfPresent(Bool.self, forKey: .details) ?? false
        assistant = try c.decodeIfPresent(Bool.self, forKey: .assistant) ?? false
        left = (try? c.decodeIfPresent(IslandSlot.self, forKey: .left)) ?? .status
        right = (try? c.decodeIfPresent(IslandSlot.self, forKey: .right)) ?? .status
        stats = try? c.decodeIfPresent(Stats.self, forKey: .stats)
        look = try? c.decodeIfPresent(FleetLook.self, forKey: .look)
    }

    /// The app's own copy for this device, like the hub's push (hub forDevice): its settings, the chosen load, and the
    /// persona it draws when given.
    func tailored(island: IslandPrefs, details: Bool, look: FleetLook? = nil) -> FleetState {
        var s = self
        s.details = details
        if !details {
            // Without names a request reads "A permission request is waiting": no Allow / Deny for something nobody can
            // tell apart, only a tap into the app (#12982, as the hub's forDevice).
            for i in s.items.indices where s.items[i].kind == .permission || s.items[i].kind == .plan {
                s.items[i].interaction = nil
                s.items[i].interactionCreatedAt = nil
            }
        }
        if let look { s.look = look }
        (s.left, s.right) = island.sides
        if var stats = s.stats, !stats.loads.isEmpty {
            let pick = stats.loads.first { $0.id == island.machine } ?? stats.loads.max { $0.percent < $1.percent }
            stats.loads = pick.map { [$0] } ?? []
            s.stats = stats
        }
        return s
    }

    /// Without the items `quiet` names (agents of quiet tasks, #1731), their counts taken off with them and the phase
    /// worked out again. A hub that knows quiet tasks leaves them out itself; this only catches what it still sent (a
    /// fleet from before the task was marked). Counts of agents not among the items stay: they cannot be told apart.
    func leavingOut(_ quiet: (Item) -> Bool) -> FleetState {
        let gone = items.filter(quiet)
        guard !gone.isEmpty else { return self }
        var s = self
        s.items.removeAll(where: quiet)
        for item in gone {
            switch item.kind {
            case .working: s.working = max(0, s.working - 1)
            case .unread: s.unread = max(0, s.unread - 1)
            default: s.needsYou = max(0, s.needsYou - 1)
            }
        }
        if s.working == 0 { s.workingSince = nil }
        s.phase = s.needsYou > 0 ? .needsYou : s.working > 0 ? .working : s.unread > 0 ? .done : .none
        return s
    }

    var isEmpty: Bool { phase == .none }
    /// A device's version of the fleet (`tailored`) has nothing to draw: no agent counts, and the assistant is not
    /// answering on a side of its island. Only the assistant answering is something to show where the island shows it
    /// (Thinking, #4398 / #4628), as the hub's `showsNothing` (liveactivity.go).
    var showsNothing: Bool { phase == .none && !(assistant && (left == .assistant || right == .assistant)) }
    var workingSinceDate: Date? { workingSince.map { Date(timeIntervalSince1970: $0) } }
    /// Agents left out when only the first `showing` items are drawn ("+2").
    func hidden(showing: Int) -> Int { max(0, needsYou + working + unread - min(showing, items.count)) }
    /// Whether the expanded island packs its first `showing` rows closer (`IslandMetrics.packedRowGap`): two rows of two
    /// lines (background work, requests) and the "+2" under them pass its about 160 pt at the usual gap, and the "+2" was
    /// cut in half (#3437). Packed, the "+2" stays where it always is, as on the lock screen.
    func islandPacked(showing: Int) -> Bool {
        hidden(showing: showing) > 0 && items.prefix(showing).filter(\.twoLines).count >= 2
    }
    /// The first request that can be settled with a button (permission or plan).
    var firstActionable: Item? { items.first { ($0.kind == .permission || $0.kind == .plan) && $0.interaction != nil } }
}

/// The activity's wording, in one place so tests pin it and the widget and the app say the same thing.
nonisolated enum FleetText {
    static func headline(_ s: FleetState) -> String {
        switch s.phase {
        case .needsYou: StateName.count(.needsYou, s.needsYou)
        case .working: StateName.count(.working, s.working)
        case .done: StateName.count(.unread, s.unread)
        case .none: interfaceText("All Caught Up")
        }
    }

    /// "Permission: Bash: go test" / "Plan needs approval" / "Claude has a question" … for one item.
    static func line(_ item: FleetState.Item) -> String {
        switch item.kind {
        case .permission: item.detail.isEmpty ? interfaceText("Needs permission") : interfaceText("Permission: \(item.detail)")
        case .plan: item.detail.isEmpty ? interfaceText("Plan needs approval") : interfaceText("Plan: \(item.detail)")
        case .question: item.detail.isEmpty ? interfaceText("Has a question") : item.detail
        case .screen: item.detail.isEmpty ? interfaceText("Terminal Needs You") : item.detail
        case .working: item.runsInBackground ? item.detail : StateName.working.text
        case .unread: item.detail.isEmpty ? StateName.unread.text : item.detail
        }
    }

    /// Where a background item's place goes (#3437): its turn is over, the work runs on in the background.
    static var background: String { interfaceText("Background [Live Activity]") }

    /// What a hidden (lock screen without details) request says instead of its title.
    static func anonymous(_ item: FleetState.Item) -> String {
        switch item.kind {
        case .permission: interfaceText("A permission request is waiting")
        case .plan: interfaceText("A plan needs your approval")
        case .question: interfaceText("A question needs your answer")
        case .screen: interfaceText("A terminal needs you")
        case .working: StateName.working.text
        case .unread: StateName.unread.text
        }
    }

    static func more(_ n: Int) -> String { interfaceText("+\(n) [Fleet State 9ec1cc]") }

    /// 980 / 1.2k / 12k / 4.2M / 42M: numbers that fit beside the camera.
    static func short(_ n: Int) -> String {
        func scaled(_ v: Double, _ unit: String) -> String {
            v < 10 ? String(format: "%.1f", v).replacingOccurrences(of: ".0", with: "") + unit : "\(Int(v.rounded()))" + unit
        }
        // A unit ends where its number would round to 1000: 999_600 is "1M", not "1000k".
        switch n {
        case ..<1000: return "\(n)"
        case ..<999_500: return scaled(Double(n) / 1000, "k")
        case ..<999_500_000: return scaled(Double(n) / 1_000_000, "M")
        default: return scaled(Double(n) / 1_000_000_000, "B")
        }
    }

    /// A count past 99 where the island has room for one short number (IslandGlyph).
    static let many = "99+"

    /// The words for a number on the expanded island / lock screen.
    static func stat(_ cell: IslandCell) -> String? {
        switch cell {
        case let .tpm(n): interfaceText(LocalizedStringResource("\(short(n))/min", comment: "Tokens per minute"))
        case let .tokens(n): interfaceText(LocalizedStringResource("\(short(n)) today", comment: "Tokens today"))
        case let .requests(n): interfaceText(LocalizedStringResource("\(short(n)) reqs today", comment: "Requests today"))
        case let .assistant(on): on ? interfaceText("Assistant replying") : interfaceText("Assistant idle")
        // Load per core, as Machines writes it ("Load 1.4 per core"); a chosen machine's name stands for "Load".
        case let .load(name, p): interfaceText(name.isEmpty ? "Load \(perCore(percent: p)) per core" : "\(name) \(perCore(percent: p)) per core")
        default: nil
        }
    }

    /// A machine's load per core from the island's percent (load ÷ cores × 100, the hub's fleetStats), one decimal as
    /// on Machines and the web (`MetricFormat.perCore`): 110 is "1.1". In whole numbers, so an older hub's steps of 5
    /// round one way (145 is "1.5"), not by how a Double stores 1.45. Not a CPU percentage, and not capped: past 1,
    /// threads wait for a core.
    static func perCore(percent: Int) -> String {
        let tenths = (max(percent, 0) + 5) / 10
        return "\(tenths / 10).\(tenths % 10)"
    }
}

/// What one side of the compact Dynamic Island can show (hub notify.go, the same names).
nonisolated enum IslandSlot: String, Codable, Hashable, Sendable, CaseIterable {
    case status, tpm, tokens, requests, assistant, load, timer, none

    init(from decoder: Decoder) throws {
        self = IslandSlot(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .status
    }

    var title: String {
        switch self {
        case .status: interfaceText("Status Counts")
        case .tpm: interfaceText("Tokens per Minute")
        case .tokens: interfaceText("Tokens Today")
        case .requests: interfaceText("Requests Today")
        case .assistant: interfaceText("Persona Assistant")
        case .load: interfaceText("Machine Load")
        case .timer: interfaceText("Longest Turn Timer")
        case .none: interfaceText("None [Agent Setting Options fc3ffd]")
        }
    }
}

/// Settings › Notifications › Dynamic Island, stored per device on the hub (prefs.island).
nonisolated struct IslandPrefs: Codable, Hashable, Sendable {
    enum Preset: String, Codable, CaseIterable, Sendable {
        case a, b, c, custom

        init(from decoder: Decoder) throws {
            self = Preset(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .a
        }

        var title: String {
            switch self {
            case .a: interfaceText("Status")
            case .b: interfaceText("Status + Speed")
            case .c: interfaceText("Status + Tokens Today")
            case .custom: interfaceText("Custom")
            }
        }
    }

    var preset: Preset = .a
    var left: IslandSlot?
    var right: IslandSlot?
    /// Whose load `load` shows; nil: the busiest machine.
    var machine: String?

    enum CodingKeys: String, CodingKey { case preset, left, right, machine }

    init(preset: Preset = .a, left: IslandSlot? = nil, right: IslandSlot? = nil, machine: String? = nil) {
        self.preset = preset
        self.left = left
        self.right = right
        self.machine = machine
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        preset = (try? c.decodeIfPresent(Preset.self, forKey: .preset)) ?? .a
        left = try? c.decodeIfPresent(IslandSlot.self, forKey: .left)
        right = try? c.decodeIfPresent(IslandSlot.self, forKey: .right)
        machine = (try? c.decodeIfPresent(String.self, forKey: .machine)).flatMap { $0?.isEmpty == false ? $0 : nil }
    }

    /// Every field, empty when unset: the hub merges what is sent, so leaving one out would keep the old value.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(preset, forKey: .preset)
        try c.encode(left?.rawValue ?? "", forKey: .left)
        try c.encode(right?.rawValue ?? "", forKey: .right)
        try c.encode(machine ?? "", forKey: .machine)
    }

    var sides: (left: IslandSlot, right: IslandSlot) {
        switch preset {
        case .a: (.status, .status)
        case .b: (.status, .tpm)
        case .c: (.status, .tokens)
        case .custom: (left ?? .status, right ?? .status)
        }
    }
}

/// One side of the compact island, worked out from the state: "needs you" always takes the left side; a `status` side
/// shows the most urgent count (left) or the next one (right).
nonisolated enum IslandCell: Hashable, Sendable {
    case needsYou(Int), working(Int), unread(Int)
    case tpm(Int), tokens(Int), requests(Int)
    case assistant(Bool)
    case load(name: String, percent: Int)
    case timer(Date)
    case empty

    var isStatus: Bool { count != nil }

    /// How many agents a status cell counts.
    var count: Int? {
        switch self {
        case let .needsYou(n), let .working(n), let .unread(n): n
        default: nil
        }
    }

    static func sides(_ s: FleetState) -> (left: IslandCell, right: IslandCell) {
        var counts: [IslandCell] = []
        if s.needsYou > 0 { counts.append(.needsYou(s.needsYou)) }
        if s.working > 0 { counts.append(.working(s.working)) }
        if s.unread > 0 { counts.append(.unread(s.unread)) }
        let left: IslandCell = s.needsYou > 0 ? counts[0] : cell(s.left, s, status: counts.first)
        var right: IslandCell
        if s.right == .status {
            // The count after the one on the left; with a number on the left, the most urgent count.
            let next = left.isStatus ? counts.firstIndex(of: left).map { $0 + 1 } ?? 0 : 0
            right = next < counts.count ? counts[next] : .empty
        } else {
            right = cell(s.right, s, status: nil)
        }
        if right == left { right = .empty }
        return (left, right)
    }

    /// The single circle when another app's activity shares the island: the most urgent count.
    static func minimal(_ s: FleetState) -> IslandCell {
        if s.needsYou > 0 { return .needsYou(s.needsYou) }
        if s.working > 0 { return .working(s.working) }
        if s.unread > 0 { return .unread(s.unread) }
        return sides(s).left
    }

    static func cell(_ slot: IslandSlot, _ s: FleetState, status: IslandCell?) -> IslandCell {
        switch slot {
        case .status: status ?? .empty
        case .tpm: .tpm(s.stats?.tpm ?? 0)
        case .tokens: .tokens(s.stats?.tokens ?? 0)
        case .requests: .requests(s.stats?.requests ?? 0)
        case .assistant: .assistant(s.assistant)
        case .load: s.stats?.loads.first.map { .load(name: $0.name, percent: $0.percent) } ?? .empty
        case .timer: s.workingSinceDate.map { .timer($0) } ?? .empty
        case .none: .empty
        }
    }
}

/// Which persona the Live Activity draws and in what color (docs/apple/live-activity.md「Avatar」): the app's choice in
/// Settings › Appearance › Avatar and the accent it draws that persona in. It rides in the activity's content (`FleetState.look`),
/// and the app also leaves it with a package's pictures in a keychain group it shares with the widget extension
/// (LiveActivity/FleetPersona.swift: drawing, saving, reading).
nonisolated struct FleetLook: Codable, Hashable, Sendable {
    /// PersonaFace.Design's raw value: a built-in's drawing; for a package, what is drawn while its faces cannot be.
    var design: String
    /// The accent as "0xRRGGBB" values, light and dark.
    var light: UInt32
    var dark: UInt32
    /// A hub package ("<id>/<rev>"): drawn from its faces (`FleetFaces`). Nil: a built-in.
    var package: String?

    init(design: String, light: UInt32, dark: UInt32, package: String? = nil) {
        self.design = design
        self.light = light
        self.dark = dark
        self.package = package
    }
}
