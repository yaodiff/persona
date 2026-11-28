import Foundation

// Token usage by project (docs/server/machines.md "Token usage by project", #16530): GET /api/v1/usage/projects and
// GET /api/v1/usage/projects/detail. The hub sums every computer's usage by the project each session's start directory
// resolved to (a git remote, a repository's first commit, a folder's name) and says each project's kind, group, name,
// share and computers; the apps only show them. Today, the last 3 and the last 7 days come in one answer, so a range
// switches without asking again. The web reads the same (web/src/lib/usage-projects.ts).

/// What a project is. An unknown kind from a newer hub reads `.unknown`.
public nonisolated enum UsageProjectKind: String, Decodable, Hashable, Sendable {
    /// A git working tree with a remote: the name is "owner/repo", the host its host.
    case repo
    /// A git working tree with no usable remote: the name is its folder's.
    case local
    /// A folder in no repository: the name is its name.
    case folder
    /// The home folder itself; temporary folders; the Persona assistant's conversations; a folder no computer resolved.
    case home, temp, assistant, unknown

    public init(from decoder: Decoder) throws {
        self = Self(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

/// The group a project shows in: development (git repositories), everything else a session started in, and what no
/// computer resolved.
public nonisolated enum UsageProjectGroup: String, Decodable, Hashable, Sendable {
    case development, other, unknown

    public init(from decoder: Decoder) throws {
        self = Self(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

/// One project's usage over a range.
public nonisolated struct UsageProject: Decodable, Hashable, Sendable, Identifiable {
    /// Opaque: what the breakdown takes back.
    public var key: String
    public var kind: UsageProjectKind
    public var group: UsageProjectGroup
    /// "owner/repo", a folder's name; empty for home, temp, assistant and unknown (the app names them).
    public var name: String
    /// A remote's host; empty otherwise.
    public var host: String
    /// How many computers it ran on in the range.
    public var machines: Int
    /// Of the range's total, 0…1.
    public var share: Double
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case key, kind, group, name, host, machines, share, tokens }

    public init(key: String, kind: UsageProjectKind, group: UsageProjectGroup, name: String = "", host: String = "",
                machines: Int = 1, share: Double = 0, tokens: UsageTokens = UsageTokens()) {
        self.key = key
        self.kind = kind
        self.group = group
        self.name = name
        self.host = host
        self.machines = machines
        self.share = share
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decode(String.self, forKey: .key)
        kind = try c.decodeIfPresent(UsageProjectKind.self, forKey: .kind) ?? .unknown
        group = try c.decodeIfPresent(UsageProjectGroup.self, forKey: .group) ?? .unknown
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
        machines = try c.decodeIfPresent(Int.self, forKey: .machines) ?? 0
        share = try c.decodeIfPresent(Double.self, forKey: .share) ?? 0
        tokens = try c.decodeIfPresent(UsageTokens.self, forKey: .tokens) ?? UsageTokens()
    }

    public var id: String { key }
}

/// One range of GET /api/v1/usage/projects: its total (the calendar's for the same days), each project's part,
/// largest first, and what is not split (stored before the hub kept folders, or from an older computer).
public nonisolated struct UsageProjectRange: Decodable, Hashable, Sendable {
    public var days: Int
    public var total: UsageTokens
    public var projects: [UsageProject]
    public var notSplit: UsageTokens

    public enum CodingKeys: String, CodingKey { case days, total, projects, notSplit = "not_split" }

    public init(days: Int, total: UsageTokens, projects: [UsageProject], notSplit: UsageTokens = UsageTokens()) {
        self.days = days
        self.total = total
        self.projects = projects
        self.notSplit = notSplit
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        days = try c.decode(Int.self, forKey: .days)
        total = try c.decodeIfPresent(UsageTokens.self, forKey: .total) ?? UsageTokens()
        projects = try c.decodeIfPresent([UsageProject].self, forKey: .projects) ?? []
        notSplit = try c.decodeIfPresent(UsageTokens.self, forKey: .notSplit) ?? UsageTokens()
    }

    /// The projects of a group, in the hub's order (largest first).
    public func projects(in group: UsageProjectGroup) -> [UsageProject] { projects.filter { $0.group == group } }

    /// A group's tokens.
    public func total(of group: UsageProjectGroup) -> Int64 { projects(in: group).reduce(0) { $0 + $1.tokens.total } }
}

/// GET /api/v1/usage/projects: today's, the last 3 days' and the last 7 days' projects, in that order.
public nonisolated struct UsageProjects: Decodable, Hashable, Sendable {
    public var zone: String
    public var ranges: [UsageProjectRange]

    public init(zone: String, ranges: [UsageProjectRange]) {
        self.zone = zone
        self.ranges = ranges
    }

    /// The days the ranges cover, in order.
    public static let rangeDays = [1, 3, 7]
}

/// One computer's, Agent's, model's or launcher's part of a project's usage, largest first.
public nonisolated struct UsagePart: Decodable, Hashable, Sendable, Identifiable {
    /// The computer's id, the agent (Session.agent's names), the model, or the launcher ("": typed in a terminal).
    public var key: String
    /// A computer's name ("" for one no longer here); empty for the others.
    public var name: String
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case key, name, tokens }

    public init(key: String, name: String = "", tokens: UsageTokens) {
        self.key = key
        self.name = name
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        key = try c.decodeIfPresent(String.self, forKey: .key) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        tokens = try c.decodeIfPresent(UsageTokens.self, forKey: .tokens) ?? UsageTokens()
    }

    public var id: String { key }
}

/// One working tree or folder of a project on one computer, written from the home folder.
public nonisolated struct UsagePlace: Decodable, Hashable, Sendable, Identifiable {
    public var machine: String
    public var machineName: String
    public var place: String
    /// A linked worktree, not the main checkout.
    public var worktree: Bool
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case machine, machineName = "machine_name", place, worktree, tokens }

    public init(machine: String, machineName: String = "", place: String, worktree: Bool = false, tokens: UsageTokens) {
        self.machine = machine
        self.machineName = machineName
        self.place = place
        self.worktree = worktree
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        machine = try c.decodeIfPresent(String.self, forKey: .machine) ?? ""
        machineName = try c.decodeIfPresent(String.self, forKey: .machineName) ?? ""
        place = try c.decodeIfPresent(String.self, forKey: .place) ?? ""
        worktree = try c.decodeIfPresent(Bool.self, forKey: .worktree) ?? false
        tokens = try c.decodeIfPresent(UsageTokens.self, forKey: .tokens) ?? UsageTokens()
    }

    public var id: String { machine + "\u{0}" + place }
}

/// One range of a project's breakdown.
public nonisolated struct UsageProjectBreakdown: Decodable, Hashable, Sendable {
    public var days: Int
    public var total: UsageTokens
    /// Of every project's tokens in the range, 0…1.
    public var share: Double
    /// Cache reads over all input, 0…1.
    public var cacheHit: Double
    public var machines: [UsagePart]
    public var agents: [UsagePart]
    public var models: [UsagePart]
    public var launchers: [UsagePart]
    public var places: [UsagePlace]

    public enum CodingKeys: String, CodingKey {
        case days, total, share, cacheHit = "cache_hit", machines, agents, models, launchers, places
    }

    public init(days: Int, total: UsageTokens, share: Double = 0, cacheHit: Double = 0, machines: [UsagePart] = [],
                agents: [UsagePart] = [], models: [UsagePart] = [], launchers: [UsagePart] = [], places: [UsagePlace] = []) {
        self.days = days
        self.total = total
        self.share = share
        self.cacheHit = cacheHit
        self.machines = machines
        self.agents = agents
        self.models = models
        self.launchers = launchers
        self.places = places
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        days = try c.decode(Int.self, forKey: .days)
        total = try c.decodeIfPresent(UsageTokens.self, forKey: .total) ?? UsageTokens()
        share = try c.decodeIfPresent(Double.self, forKey: .share) ?? 0
        cacheHit = try c.decodeIfPresent(Double.self, forKey: .cacheHit) ?? 0
        machines = try c.decodeIfPresent([UsagePart].self, forKey: .machines) ?? []
        agents = try c.decodeIfPresent([UsagePart].self, forKey: .agents) ?? []
        models = try c.decodeIfPresent([UsagePart].self, forKey: .models) ?? []
        launchers = try c.decodeIfPresent([UsagePart].self, forKey: .launchers) ?? []
        places = try c.decodeIfPresent([UsagePlace].self, forKey: .places) ?? []
    }
}

/// One calendar day of a project's usage.
public nonisolated struct UsageProjectDay: Decodable, Hashable, Sendable, Identifiable {
    /// 2006-01-02, the day in the hub's user zone.
    public var date: String
    public var tokens: UsageTokens

    public init(date: String, tokens: UsageTokens) {
        self.date = date
        self.tokens = tokens
    }

    public var id: String { date }
}

/// One hour on the clock of today.
public nonisolated struct UsageProjectHour: Decodable, Hashable, Sendable, Identifiable {
    public var t: Int64
    public var hour: Int
    public var tokens: UsageTokens

    public init(t: Int64, hour: Int, tokens: UsageTokens) {
        self.t = t
        self.hour = hour
        self.tokens = tokens
    }

    public var id: Int64 { t }
}

/// GET /api/v1/usage/projects/detail: the project (its tokens the last 7 days'), its breakdown for each range, its
/// last 7 days, oldest first, and today's hours.
public nonisolated struct UsageProjectDetail: Decodable, Hashable, Sendable {
    public var zone: String
    public var project: UsageProject
    public var ranges: [UsageProjectBreakdown]
    public var days: [UsageProjectDay]
    public var hours: [UsageProjectHour]

    public init(zone: String, project: UsageProject, ranges: [UsageProjectBreakdown], days: [UsageProjectDay], hours: [UsageProjectHour]) {
        self.zone = zone
        self.project = project
        self.ranges = ranges
        self.days = days
        self.hours = hours
    }
}
