#if DEBUG && os(iOS)
import SwiftUI
import WidgetKit

/// Every widget at its size, in each way the system draws it, for screenshots and design review (`-showHomeWidgets`,
/// docs/apple/home-widgets.md「Screenshots」). A view outside a widget gets no container background, family, margins
/// or tint from the system, so the gallery supplies each: the stage, the size, the 16 pt margins a module widget has,
/// and a stand-in for the tint (`homeSimTint`). Tinted and clear are close to, not exactly, what iOS draws; the shots
/// on the Home Screen itself are the proof.
enum HomeGallery {
    /// How the system draws a widget: on the Home Screen in light, dark, tinted or clear; on the Lock Screen.
    enum Look: String, CaseIterable, Identifiable {
        case light, dark, tinted, clear, lock
        var id: String { rawValue }
        var title: String {
            switch self {
            case .light: "Light"
            case .dark: "Dark"
            case .tinted: "Tinted"
            case .clear: "Clear"
            case .lock: "Lock Screen"
            }
        }
        var scheme: ColorScheme { self == .light ? .light : .dark }
        var mode: WidgetRenderingMode {
            switch self {
            case .light, .dark: .fullColor
            case .tinted, .clear: .accented
            case .lock: .vibrant
            }
        }
    }

    /// The tint the gallery paints tinted widgets with (one of the system's choices: a warm amber).
    static let tint = HomeWidgetTokens.Gallery.tint
    /// iPhone 17 Pro Max's widget sizes; iPad Pro 13-inch's extra large; the Lock Screen's.
    static func size(_ family: WidgetFamily) -> CGSize {
        switch family {
        case .systemSmall: CGSize(width: 170, height: 170)
        case .systemMedium: CGSize(width: 364, height: 170)
        case .systemLarge: CGSize(width: 364, height: 382)
        case .systemExtraLarge: CGSize(width: 795, height: 379)
        case .accessoryCircular: CGSize(width: 76, height: 76)
        case .accessoryRectangular: CGSize(width: 172, height: 76)
        default: CGSize(width: 260, height: 22)
        }
    }
    static let corner: CGFloat = 24
    static let margin: CGFloat = 16
    static let pad: CGFloat = 18
    static let tileCorner: CGFloat = 14
}

/// What sits behind a widget in a shot: a made-up wallpaper.
struct HomeGalleryWallpaper: View {
    var look: HomeGallery.Look
    var body: some View {
        switch look {
        case .light:
            LinearGradient(colors: HomeWidgetTokens.Gallery.lightWall, startPoint: .topLeading, endPoint: .bottomTrailing)
        case .dark:
            LinearGradient(colors: HomeWidgetTokens.Gallery.darkWall, startPoint: .topLeading, endPoint: .bottomTrailing)
        case .tinted:
            LinearGradient(colors: HomeWidgetTokens.Gallery.tintedWall, startPoint: .topLeading, endPoint: .bottomTrailing)
        case .clear:
            LinearGradient(colors: HomeWidgetTokens.Gallery.clearWall, startPoint: .topLeading, endPoint: .bottomTrailing)
        case .lock:
            LinearGradient(colors: HomeWidgetTokens.Gallery.lockWall, startPoint: .top, endPoint: .bottom)
        }
    }
}

/// One widget as the system would frame it.
struct HomeGalleryCell: View {
    var kind: HomeKind
    var family: WidgetFamily
    var look: HomeGallery.Look
    var entry: HomeEntry
    /// Redacted as on a locked phone.
    var locked = false

    var body: some View {
        let size = HomeGallery.size(family)
        let accessory = family == .accessoryCircular || family == .accessoryRectangular || family == .accessoryInline
        HomeWidgetView(kind: kind, entry: entry, size: family)
            .padding(accessory ? 0 : HomeGallery.margin)
            .frame(width: size.width, height: size.height)
            .background { if !accessory { background } }
            .clipShape(RoundedRectangle(cornerRadius: accessory ? 0 : HomeGallery.corner, style: .continuous))
            .environment(\.colorScheme, look.scheme)
            .environment(\.widgetRenderingMode, look.mode)
            .environment(\.homeSimTint, simTint)
            .redacted(reason: locked ? .privacy : [])
    }

    private var simTint: Color? {
        switch look {
        case .light, .dark: nil
        case .tinted: HomeGallery.tint
        case .clear, .lock: .white
        }
    }

    @ViewBuilder private var background: some View {
        switch look {
        case .light, .dark: HomeWidgetTokens.surface
        case .tinted: HomeWidgetTokens.Gallery.tintedSurface
        case .clear, .lock: Color.white.opacity(0.12)
        }
    }
}

/// A titled row of cells for one widget at one size, every look side by side.
struct HomeGalleryRow: View {
    var title: String
    var kind: HomeKind
    var family: WidgetFamily
    var looks: [HomeGallery.Look]
    var entry: HomeEntry

    var body: some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) {
            Text(verbatim: title).font(.headline).foregroundStyle(.black.opacity(0.75))
            HStack(alignment: .top, spacing: HomeGallery.pad) {
                ForEach(looks) { look in
                    VStack(spacing: HomeWidgetTokens.tight) {
                        HomeGalleryCell(kind: kind, family: family, look: look, entry: entry)
                            .padding(HomeGallery.pad)
                            .background(HomeGalleryWallpaper(look: look))
                            .clipShape(RoundedRectangle(cornerRadius: HomeGallery.tileCorner, style: .continuous))
                        Text(verbatim: look.title).font(.caption).foregroundStyle(.black.opacity(0.55))
                    }
                }
            }
        }
    }
}

/// A page of rows with a title, as one image.
struct HomeGallerySheet<Content: View>: View {
    var title: String
    var note: String?
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: HomeGallery.pad * 1.2) {
            VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
                Text(verbatim: title).font(.largeTitle.bold()).foregroundStyle(.black.opacity(0.85))
                if let note { Text(verbatim: note).font(.callout).foregroundStyle(.black.opacity(0.55)) }
            }
            content
        }
        .padding(HomeGallery.pad * 2)
        .background(HomeWidgetTokens.Gallery.sheet)
    }
}
/// One widget's Lock Screen families on the Lock Screen's wallpaper: the circle and the rectangle side by side, the
/// inline line under them.
struct HomeGalleryLockRow: View {
    var kind: HomeKind
    var entry: HomeEntry

    var body: some View {
        let families = kind.families.filter { [.accessoryCircular, .accessoryRectangular].contains($0) }
        VStack(alignment: .leading, spacing: HomeWidgetTokens.gap) {
            HStack(spacing: HomeGallery.pad) {
                ForEach(families, id: \.self) { HomeGalleryCell(kind: kind, family: $0, look: .lock, entry: entry) }
            }
            if kind.families.contains(.accessoryInline) {
                HomeGalleryCell(kind: kind, family: .accessoryInline, look: .lock, entry: entry)
            }
        }
        .padding(HomeGallery.pad)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(HomeGalleryWallpaper(look: .lock))
        .clipShape(RoundedRectangle(cornerRadius: HomeGallery.tileCorner, style: .continuous))
    }
}

/// `-showHomeWidgets calendar:<page>`: the Calendar widget's views, a page a shot (an iPad's screen holds one):
/// `medium` (today, week, month, a picked day), `large` (today, week), `large2` (month, a picked day), `more` (tinted
/// and clear, no calendar access, the small one).
struct HomeCalendarGallery: View {
    var page: String
    var entry: HomeEntry

    var body: some View {
        switch page {
        case "large":
            row("today · large", .systemLarge, view: .today)
            row("week · large", .systemLarge, view: .week)
        case "large2":
            row("month · large", .systemLarge, view: .month)
            row("month · large · a day picked", .systemLarge, view: .month, picked: 8)
        case "more":
            row("week · medium", .systemMedium, view: .week, looks: [.tinted, .clear])
            row("month · medium", .systemMedium, view: .month, looks: [.tinted, .clear])
            row("week · medium · no calendar access", .systemMedium, view: .week, noAccess: true)
            row("small", .systemSmall, view: .week)
        default:
            row("today · medium", .systemMedium, view: .today)
            row("week · medium", .systemMedium, view: .week)
            row("month · medium", .systemMedium, view: .month)
            row("week · medium · tomorrow picked", .systemMedium, view: .week, picked: 1)
        }
    }

    private func row(_ title: String, _ family: WidgetFamily, view: HomeCalendarView, picked: Int? = nil,
                     looks: [HomeGallery.Look] = [.light, .dark], noAccess: Bool = false) -> some View {
        var e = entry
        e.calendarKey = "gallery"
        var slot = HomeCalendarState.Slot(view: view)
        if let picked, let day = Calendar.current.date(byAdding: .day, value: picked, to: e.date) {
            slot.day = HomeDays.key(day)
            slot.pickedAt = e.date.timeIntervalSince1970
        }
        e.calendarSlot = slot
        if noAccess { e.snapshot.today = .init(events: [], source: nil) }
        return HomeGalleryRow(title: "calendar · \(title)", kind: .today, family: family, looks: looks, entry: e)
    }
}

/// `-showHomeWidgets YES`: every widget at every Home Screen size, light and dark, with the sample data, over the app
/// (screenshots and design review; the real proof is the Home Screen itself). `-showHomeWidgets lock`: the Lock
/// Screen's families instead, and `lock:agents,today` only those kinds (one screenshot holds four).
struct HomeGalleryOverlay: View {
    @State private var shown = UserDefaults.standard.string(forKey: "showHomeWidgets").flatMap { ["", "NO"].contains($0) ? nil : $0 }

    var body: some View {
        if let shown {
            let entry = HomeEntry(date: .now, snapshot: HomeSamples.busy(zh: LanguageStore.shared.launched.hasPrefix("zh"), now: .now))
            ScrollView([.vertical, .horizontal]) {
                HomeGallerySheet(title: "Persona", note: nil) {
                    if shown.hasPrefix("calendar") {
                        HomeCalendarGallery(page: shown.split(separator: ":").dropFirst().first.map(String.init) ?? "medium", entry: entry)
                    } else if shown.hasPrefix("lock") {
                        let only = shown.split(separator: ":").dropFirst().first.map { $0.split(separator: ",").map(String.init) }
                        ForEach(HomeKind.allCases.filter { only?.contains($0.rawValue) ?? true }, id: \.self) { kind in
                            HomeGalleryLockRow(kind: kind, entry: entry)
                        }
                    } else {
                        ForEach(HomeKind.allCases, id: \.self) { kind in
                            ForEach(kind.families.filter { [.systemSmall, .systemMedium, .systemLarge].contains($0) }, id: \.self) { family in
                                HomeGalleryRow(title: "\(kind.rawValue) · \(family)", kind: kind, family: family, looks: [.light, .dark], entry: entry)
                            }
                        }
                    }
                }
            }
            .background(HomeWidgetTokens.Gallery.sheet)
        }
    }
}
#endif
