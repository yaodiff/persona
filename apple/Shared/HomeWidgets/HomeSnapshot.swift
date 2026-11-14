import Foundation

/// What the Home Screen and Lock Screen widgets draw (docs/apple/home-widgets.md): one small file the app writes into
/// the App Group, `Widgets/snapshot.json`, whenever what it says changes (in the foreground, after a background refresh,
/// when a push arrives). The widget extension never reaches the hub and never reads the keychain: on a phone it cannot
/// (docs/apple/live-activity.md「Why the avatar pack was still wrong in build 103」), and a widget drawn from a file needs
/// neither a token nor the network.
///
/// Counts are the app's own, worked out where the app shows them (the Agents list's Needs you, the Inbox's Needs you,
/// the Machines page's online count), so a widget never says another number than the app (principle 4). Times are Unix
/// seconds.
nonisolated struct HomeSnapshot: Codable, Hashable, Sendable {
    /// The file's shape. The app and its widgets ship together; a file of another shape (written by the version before
    /// an update, until the app runs again) reads as none. Bump it when a field changes its meaning or a new one is
    /// required; a new optional field reads as absent from an older file and needs no bump (`Today.days`), so the
    /// widgets keep their content across an update.
    static let version = 1
    var version = HomeSnapshot.version
    /// When the app wrote it; the widgets say how old it is once it is `staleAfter` old.
    var at: Double = 0
    /// A hub is paired. Without one every widget asks to open Persona to connect.
    var paired = false
    var look = Look()
    var agents = Agents()
    var today: Today?
    var todos: Todos?
    var usage: Usage?
    var machines: [Machine] = []
    var recording: Recording?
    var health: Health?
    var inbox: Inbox?

    /// After this long without a write the widgets show the time of the data and the persona offline (gray): numbers
    /// that may be old are never passed off as current. The app writes at least every `HomeWidgetWriter.heartbeat` while
    /// it runs, and the hub wakes it when the counts change (#5721).
    static let staleAfter: TimeInterval = 60 * 60

    func isStale(at date: Date) -> Bool { at > 0 && date.timeIntervalSince1970 - at > Self.staleAfter }

    // MARK: Persona

    /// The persona chosen in Settings › Appearance › Avatar.
    nonisolated struct Look: Codable, Hashable, Sendable {
        /// Its name, as the app's top bar says it ("Mochi", a package's own name).
        var name = ""
        /// A hub package ("<id>/<rev>") drawn from its pictures in `Widgets/faces/`; nil: Mochi, drawn in code.
        var package: String?
        /// The package's `primary` color (`Theme.persona`), light and dark, 0xRRGGBB; nil: Mochi's own paper.
        var primaryLight: UInt32?
        var primaryDark: UInt32?
    }

    // MARK: Agents

    /// The agents that need the user, are working or just finished, the most urgent first (the Agents list's order).
    nonisolated struct Agents: Codable, Hashable, Sendable {
        var rows: [AgentRow] = []
        /// Rows past the cap are left out of `rows` but still counted here, so a full list never undercounts.
        var more = Counts()
        /// Persona itself is answering in the main chat (the face thinks).
        var assistantAnswering = false

        static let cap = 40
    }

    nonisolated struct Counts: Codable, Hashable, Sendable {
        var needsYou = 0
        var working = 0
        var done = 0
        var total: Int { needsYou + working + done }
    }

    nonisolated struct AgentRow: Codable, Hashable, Sendable, Identifiable {
        /// "<machine>/<session>": what a tap opens (`persona://open?machine=…&session=…`).
        var id: String
        /// The session's title (DESIGN_SYSTEM.md 5.6): what it is doing, never a path or an engine name.
        var title: String
        var state: AgentState
        var kind: AgentKind = .coding
        /// The machine's id, for the computer a widget is set to, and its name as the user calls it.
        var machine = ""
        var place: String?
        /// Since when it is in this state.
        var since: Double?
        /// What it waits for, in the request's title words (Plan awaiting approval, Has a question…); nil unless needs you.
        var ask: String?
        /// What it is doing now, one line ("Running tests", a workflow's step).
        var detail: String?
    }

    nonisolated enum AgentState: String, Codable, Hashable, Sendable {
        case needsYou = "needs_you", working, done

        init(from decoder: Decoder) throws {
            self = AgentState(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .working
        }
    }

    /// Which agents a widget shows (Edit Widget › Agents): the ones that write code on your computers, or the tasks
    /// Persona runs for you.
    nonisolated enum AgentKind: String, Codable, Hashable, Sendable {
        case coding, assistant

        init(from decoder: Decoder) throws {
            self = AgentKind(rawValue: (try? decoder.singleValueContainer().decode(String.self)) ?? "") ?? .coding
        }
    }

    // MARK: Today

    nonisolated struct Today: Codable, Hashable, Sendable {
        /// The rest of today's calendar events, in order (the phone's calendar when Persona already has full access,
        /// else the hub's meetings).
        var events: [Event] = []
        /// Where they came from: nil when neither can be read (the widget says how to allow the calendar).
        var source: EventSource?
        /// The days around today for the Calendar widget's week and month (`HomeDays`), from the phone's calendar; nil
        /// when only today is known (the hub's meetings) or nothing is.
        var days: Days?

        static let cap = 8
    }

    /// Every day from `from` to `to` (`HomeDays.key`s): this month's grid and the two weeks after today. A day of the
    /// range missing from `byDay` has no events; a day outside the range is not known.
    nonisolated struct Days: Codable, Hashable, Sendable {
        var from: String
        var to: String
        var byDay: [String: Day] = [:]

        func covers(_ key: String) -> Bool { key >= from && key <= to }
    }

    nonisolated struct Day: Codable, Hashable, Sendable {
        /// How many events the day has; `events` holds the first `HomeDays.perDay`.
        var count: Int
        var events: [Event]
    }

    nonisolated enum EventSource: String, Codable, Hashable, Sendable {
        case calendar, hub
    }

    nonisolated struct Event: Codable, Hashable, Sendable {
        var title: String
        var start: Double
        var end: Double
        var allDay = false
    }

    // MARK: To-dos

    /// The To-dos page's Today filter: due today, and open ones overdue; the most urgent first.
    nonisolated struct Todos: Codable, Hashable, Sendable {
        var items: [Todo] = []
        /// How many the filter holds (the list above is capped), of which overdue.
        var count = 0
        var overdue = 0
    }

    nonisolated struct Todo: Codable, Hashable, Sendable, Identifiable {
        var id: String
        var title: String
        var due: Double?
        var overdue = false
        /// Ticked on the widget: shown done at once while the app tells the hub (`ticking`), or the hub refused it
        /// (`failed`: the widget says so and a tap tries again).
        var tick: Tick?
    }

    nonisolated enum Tick: String, Codable, Hashable, Sendable {
        case ticking, failed
    }

    // MARK: Usage

    nonisolated struct Usage: Codable, Hashable, Sendable {
        /// Subscription accounts, the fullest window first (docs/server/usage-windows.md, the Machines page's order).
        var accounts: [Account] = []
        /// Tokens today on every computer, cache included, in the phone's time zone (the island's Tokens Today).
        var tokensToday = 0
        /// Output tokens a minute over the last minutes.
        var tpm = 0
        /// Today's tokens by the hour, midnight first (up to 24); empty when not known.
        var hourly: [Int] = []
        /// Money spent today where a gateway reports it (`LauncherQuota`); nil when none does.
        var costToday: Double?
        var currency = "USD"
    }

    nonisolated struct Account: Codable, Hashable, Sendable, Identifiable {
        var id: String
        /// "Codex Pro", "Claude Max": provider and plan as the Machines page names them.
        var name: String
        var windows: [Window] = []
        var stale = false
    }

    nonisolated struct Window: Codable, Hashable, Sendable {
        /// "5h", "7d".
        var label: String
        var minutes = 0
        var percent: Double
        var resetsAt: Double?
    }

    // MARK: Computers

    nonisolated struct Machine: Codable, Hashable, Sendable, Identifiable {
        var id: String
        /// The user's name for it.
        var name: String
        var online: Bool
        var working = 0
        var needsYou = 0
        /// Load average ÷ cores, 0…100; nil when not reported.
        var load: Int?
        /// The mark the user gave it (`Machine.icon`: server, desktop, else a laptop).
        var icon: String?
    }

    // MARK: Recording

    nonisolated struct Recording: Codable, Hashable, Sendable {
        var id: String
        var title: String
        var started: Double
    }

    // MARK: Health

    /// Today's summary, read from Health with the user's permission (the Health page's metrics).
    nonisolated struct Health: Codable, Hashable, Sendable {
        var steps: Int?
        /// The average of the last 7 days, for the bar's mark.
        var stepsAverage: Int?
        /// Hours asleep last night.
        var sleepHours: Double?
        var restingHeartRate: Int?
        var activeEnergy: Int?
        /// Steps a day for the last 7 days, today last.
        var week: [Int] = []
    }

    // MARK: Inbox

    nonisolated struct Inbox: Codable, Hashable, Sendable {
        /// Open items that need the user (the Inbox's Needs you).
        var needsYou = 0
        var unread = 0
        /// The newest first, at most three.
        var latest: [Message] = []
    }

    nonisolated struct Message: Codable, Hashable, Sendable, Identifiable {
        var id: String
        /// The sender or group.
        var sender: String
        /// The summary the Inbox shows.
        var summary: String
        var at: Double
        var urgent = false
        var needsYou = false
    }
}
