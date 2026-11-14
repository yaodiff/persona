import AppIntents
import SwiftUI
import WidgetKit

/// Persona's Home Screen and Lock Screen widgets (docs/apple/home-widgets.md): each draws only the snapshot the app left
/// in the App Group (`HomeSnapshotStore`) and the persona's pictures the Live Activity's faces already hold
/// (`FleetLookStore`). The app reloads a kind's timeline when its part of the snapshot changes; a timeline carries one
/// more entry at the moment the snapshot turns stale, so old numbers step back without a reload.
enum HomeTimeline {
    static let placeholder = HomeEntry(date: .now, snapshot: HomeSnapshot(paired: true))

    /// `edit` applies the widget's Edit Widget choices before the Smart Stack's score, which counts only what it shows.
    /// The Calendar widget also takes what its switch and a tapped day left for its size and View (`calendar`, read off
    /// the main actor by its provider: `calendarState`).
    static func entry(_ kind: HomeKind, family: WidgetFamily, at date: Date, snapshot: HomeSnapshot?, calendar: HomeCalendarState? = nil,
                      edit: (inout HomeEntry) -> Void) -> HomeEntry {
        var e = HomeEntry(date: date, snapshot: snapshot ?? HomeSnapshot(), pictures: HomeFaces.load())
        edit(&e)
        if kind == .today {
            e.calendarKey = HomeCalendarState.slot(family, e.calendarView)
            e.calendarSlot = calendar?.slots[e.calendarKey]
        }
        e.relevance = HomeRelevance.of(kind, e.context)
        return e
    }

    /// Now; when the snapshot turns stale, so old numbers step back without a reload; for the Calendar widget also
    /// midnight (the date and the grids move on) and when a picked day goes back to today. At most four entries.
    static func timeline(_ kind: HomeKind, family: WidgetFamily, calendar: HomeCalendarState? = nil,
                         edit: (inout HomeEntry) -> Void) -> Timeline<HomeEntry> {
        let snapshot = HomeSnapshotStore.shared.read()
        let now = Date.now
        let first = entry(kind, family: family, at: now, snapshot: snapshot, calendar: calendar, edit: edit)
        var dates: Set<Date> = []
        if let snapshot, snapshot.at > 0 {
            let stale = Date(timeIntervalSince1970: snapshot.at + HomeSnapshot.staleAfter)
            if stale > now { dates.insert(stale) }
        }
        if kind == .today {
            dates.insert(HomeDays.nextMidnight(after: now))
            if let back = HomeCalendarState.pickEnds(first.calendarSlot, after: now) { dates.insert(back) }
        }
        let later = dates.sorted().map { entry(kind, family: family, at: $0, snapshot: snapshot, calendar: calendar, edit: edit) }
        return Timeline(entries: [first] + later, policy: .never)
    }

    /// The Calendar widget's saved switch and picked days (`Widgets/calendar.json` in the App Group), read off the main
    /// actor (#7252, `docs/apple/performance.md` P1); nil for the other kinds.
    @concurrent nonisolated static func calendarState(_ kind: HomeKind) async -> HomeCalendarState? {
        kind == .today ? HomeCalendarStore.shared.savedState() : nil
    }
}

/// A widget with nothing to edit.
struct HomeProvider: TimelineProvider {
    var kind: HomeKind

    func placeholder(in context: Context) -> HomeEntry { HomeTimeline.placeholder }

    func getSnapshot(in context: Context, completion: @escaping (HomeEntry) -> Void) {
        completion(HomeTimeline.entry(kind, family: context.family, at: .now, snapshot: HomeSnapshotStore.shared.read()) { _ in })
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<HomeEntry>) -> Void) {
        completion(HomeTimeline.timeline(kind, family: context.family) { _ in })
    }
}

/// A widget with Edit Widget choices (Agents, Calendar, Computers, Usage): the same timeline, filtered by them.
struct HomeEditProvider<Intent: HomeWidgetEdit>: AppIntentTimelineProvider {
    var kind: HomeKind

    func placeholder(in context: Context) -> HomeEntry { HomeTimeline.placeholder }

    func snapshot(for configuration: Intent, in context: Context) async -> HomeEntry {
        let calendar = await HomeTimeline.calendarState(kind)
        return HomeTimeline.entry(kind, family: context.family, at: .now, snapshot: HomeSnapshotStore.shared.read(), calendar: calendar) {
            configuration.apply(to: &$0)
        }
    }

    func timeline(for configuration: Intent, in context: Context) async -> Timeline<HomeEntry> {
        let calendar = await HomeTimeline.calendarState(kind)
        return HomeTimeline.timeline(kind, family: context.family, calendar: calendar) { configuration.apply(to: &$0) }
    }
}

/// The persona's pictures for the widgets: a hub package's, one a state, from where the app keeps the Live Activity's
/// (`FleetLookStore`, at the empty states' size); nil for Mochi, which is drawn in code.
enum HomeFaces {
    static func load() -> HomeFacePictures? {
        let store = FleetLookStore.shared
        guard let package = FleetLook.load(store).supported.package else { return nil }
        var pictures = HomeFacePictures()
        let points = HomeWidgetTokens.faceEmpty
        for state in PersonaState.allCases {
            guard let face = store.face(package, state, points: points), face.own,
                  let image = FleetFaces.image(face.png, points: CGFloat(FleetFaces.size(points))) else { continue }
            pictures.states[state.rawValue] = image
        }
        if let avatar = store.read(FleetLookStore.facesDir + "/" + FleetLookStore.folder(package) + "/"
                                   + FleetLookStore.file(FleetLookStore.avatarName, FleetFaces.size(points))) {
            pictures.avatar = FleetFaces.image(avatar, points: CGFloat(FleetFaces.size(points)))
        }
        return pictures.states.isEmpty && pictures.avatar == nil ? nil : pictures
    }
}

/// One kind's widget: its gallery name and line, its sizes (the Lock Screen's too), its view.
enum HomeWidgetConfiguration {
    static func make(_ kind: HomeKind) -> some WidgetConfiguration {
        StaticConfiguration(kind: kind.widgetKind, provider: HomeProvider(kind: kind)) { entry in
            HomeWidgetView(kind: kind, entry: entry)
        }
        .home(kind)
    }

    /// The same with Edit Widget's choices. A widget placed before it had any keeps its kind and takes the defaults.
    static func make<Intent: HomeWidgetEdit>(_ kind: HomeKind, editing: Intent.Type) -> some WidgetConfiguration {
        AppIntentConfiguration(kind: kind.widgetKind, intent: Intent.self, provider: HomeEditProvider<Intent>(kind: kind)) { entry in
            HomeWidgetView(kind: kind, entry: entry)
        }
        .home(kind)
    }
}

private extension WidgetConfiguration {
    func home(_ kind: HomeKind) -> some WidgetConfiguration {
        configurationDisplayName(Text(kind.title))
            .description(Text(kind.summary))
            .supportedFamilies(kind.families)
    }
}

/// The widgets (#5664). Each kind is a type of its own: a `Widget` has no arguments.
struct AgentsHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.agents, editing: AgentsWidgetEdit.self) }
}

struct InboxHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.inbox) }
}

struct RecordHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.record) }
}

/// The Calendar widget (`today` from before it had a week and a month: a kind never changes).
struct TodayHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.today, editing: CalendarWidgetEdit.self) }
}

struct TodosHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.todos) }
}

struct UsageHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.usage, editing: UsageWidgetEdit.self) }
}

struct ComputersHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.computers, editing: ComputersWidgetEdit.self) }
}

struct HealthHomeWidget: Widget {
    var body: some WidgetConfiguration { HomeWidgetConfiguration.make(.health) }
}
