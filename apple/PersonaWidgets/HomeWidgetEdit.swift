import AppIntents
import WidgetKit

/// Edit Widget (docs/apple/home-widgets.md「Edit Widget」): what a widget shows, chosen on the widget itself and never in
/// the app's Settings. Every choice starts at a default that works on a fresh install, and the lists come from the
/// snapshot the app last wrote (`HomeChoices`), so opening Edit Widget never calls the hub.
protocol HomeWidgetEdit: WidgetConfigurationIntent {
    func apply(to entry: inout HomeEntry)
}

/// Agents: which agents, and on which computer.
struct AgentsWidgetEdit: HomeWidgetEdit {
    nonisolated static let title: LocalizedStringResource = "Agents [Home Widgets]"
    nonisolated static let description = IntentDescription("How many agents need you, and the one waiting longest. [Home Widgets]")

    @Parameter(title: "Show [Home Widgets]", default: .all) var show: HomeAgentsShow
    @Parameter(title: "Computer [Home Widgets]") var computer: HomeComputerEntity?

    nonisolated init() {}

    func apply(to entry: inout HomeEntry) {
        entry.agentKind = show.kind
        entry.machine = HomeChoices.filter(computer?.id)
    }
}

/// Calendar: the view it starts on; the switch at its top right changes it from there.
struct CalendarWidgetEdit: HomeWidgetEdit {
    nonisolated static let title: LocalizedStringResource = "Calendar [Home Widgets]"
    nonisolated static let description = IntentDescription("Today, this week or this month on your calendar. [Home Widgets]")

    @Parameter(title: "View [Home Widgets]", default: .today) var view: HomeCalendarView

    nonisolated init() {}

    func apply(to entry: inout HomeEntry) { entry.calendarView = view }
}

/// Computers: every computer, or one.
struct ComputersWidgetEdit: HomeWidgetEdit {
    nonisolated static let title: LocalizedStringResource = "Computers [Home Widgets]"
    nonisolated static let description = IntentDescription("Your computers: how busy they are, and which are offline. [Home Widgets]")

    @Parameter(title: "Computer [Home Widgets]") var computer: HomeComputerEntity?

    nonisolated init() {}

    func apply(to entry: inout HomeEntry) { entry.machine = HomeChoices.filter(computer?.id) }
}

/// Usage: the fullest account, or one.
struct UsageWidgetEdit: HomeWidgetEdit {
    nonisolated static let title: LocalizedStringResource = "Usage [Home Widgets]"
    nonisolated static let description = IntentDescription("How full your subscription windows are, and today's tokens. [Home Widgets]")

    @Parameter(title: "Account [Home Widgets]") var account: HomeAccountEntity?

    nonisolated init() {}

    func apply(to entry: inout HomeEntry) { entry.account = HomeChoices.filter(account?.id) }
}

nonisolated extension HomeAgentsShow: AppEnum {
    nonisolated static let typeDisplayRepresentation: TypeDisplayRepresentation = "Show [Home Widgets]"
    nonisolated static let caseDisplayRepresentations: [HomeAgentsShow: DisplayRepresentation] = [
        .all: "All Agents [Home Widgets]",
        .computers: "Agents on Your Computers [Home Widgets]",
        .persona: "Persona's Tasks [Home Widgets]",
    ]
}

nonisolated extension HomeCalendarView: AppEnum {
    nonisolated static let typeDisplayRepresentation: TypeDisplayRepresentation = "View [Home Widgets]"
    nonisolated static let caseDisplayRepresentations: [HomeCalendarView: DisplayRepresentation] = [
        .today: "Today [Home Widgets]",
        .week: "Week [Home Widgets]",
        .month: "Month [Home Widgets]",
    ]
}

/// A computer in Edit Widget; the empty id is every computer, the default.
nonisolated struct HomeComputerEntity: AppEntity, Hashable, Sendable {
    static let typeDisplayRepresentation: TypeDisplayRepresentation = "Computer [Home Widgets]"
    static let defaultQuery = HomeComputerQuery()
    static let every = HomeComputerEntity(id: "", name: "")

    var id: String
    var name: String

    var displayRepresentation: DisplayRepresentation {
        id.isEmpty ? DisplayRepresentation(title: "Any Computer [Home Widgets]") : DisplayRepresentation(title: "\(name)")
    }
}

nonisolated struct HomeComputerQuery: EntityQuery {
    func entities(for identifiers: [String]) async throws -> [HomeComputerEntity] { Self.all().filter { identifiers.contains($0.id) } }
    func suggestedEntities() async throws -> [HomeComputerEntity] { Self.all() }
    func defaultResult() async -> HomeComputerEntity? { .every }

    static func all() -> [HomeComputerEntity] {
        [.every] + HomeChoices.computers(HomeSnapshotStore.shared.read()).map { HomeComputerEntity(id: $0.id, name: $0.name) }
    }
}

/// A subscription account in Edit Widget; the empty id is the fullest, the default.
nonisolated struct HomeAccountEntity: AppEntity, Hashable, Sendable {
    static let typeDisplayRepresentation: TypeDisplayRepresentation = "Account [Home Widgets]"
    static let defaultQuery = HomeAccountQuery()
    static let fullest = HomeAccountEntity(id: "", name: "")

    var id: String
    var name: String

    var displayRepresentation: DisplayRepresentation {
        id.isEmpty ? DisplayRepresentation(title: "The Fullest [Home Widgets]") : DisplayRepresentation(title: "\(name)")
    }
}

nonisolated struct HomeAccountQuery: EntityQuery {
    func entities(for identifiers: [String]) async throws -> [HomeAccountEntity] { Self.all().filter { identifiers.contains($0.id) } }
    func suggestedEntities() async throws -> [HomeAccountEntity] { Self.all() }
    func defaultResult() async -> HomeAccountEntity? { .fullest }

    static func all() -> [HomeAccountEntity] {
        [.fullest] + HomeChoices.accounts(HomeSnapshotStore.shared.read()).map { HomeAccountEntity(id: $0.id, name: $0.name) }
    }
}
