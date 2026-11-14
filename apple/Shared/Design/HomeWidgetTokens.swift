#if os(iOS)
import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// The Home Screen and Lock Screen widgets (docs/apple/home-widgets.md). The widget extension builds this file but not
/// `Theme`, as it does `IslandMetrics` and `WatchFaceTokens`: a table of its own. Widget sizes are the system's, so the
/// hero numbers, rings and faces are fixed sizes (the documented exception in DESIGN_SYSTEM.md 2.2); every line of
/// words is a text style and follows the text size.
///
/// The look is the system's own widgets': a plain surface, one widget one job, one hero (a number, a ring, a chart, a
/// button), one color that says which widget it is. The persona is an accent: a small face in the corner, the empty
/// states, the hint that something needs you.
nonisolated enum HomeWidgetTokens {
    // MARK: Surface

    /// The widget's background: the system widgets' white, and their dark gray.
    static let surface = Color(light: 0xFFFFFF, dark: 0x1C1C1E)
    /// A ring's or a bar's track, and a monogram's disc.
    static let track = dynamic(light: (0x787880, 0.16), dark: (0x787880, 0.32))
    /// The same in the tinted, clear and Lock Screen modes, where the system paints every opaque pixel in one color.
    static let trackMono = Color.white.opacity(0.22)
    /// A line between rows (the large widgets' lists).
    static let rule = dynamic(light: (0x3C3C43, 0.12), dark: (0x545458, 0.6))

    // MARK: Each widget's color

    /// Agents: the agent states' own (`StatusPalette`), Needs You first.
    static let needsYou = StatusPalette.needsYou
    static let working = StatusPalette.working
    static let done = StatusPalette.unread
    /// Today: the app icon's coral, on the weekday and the events.
    static let today = Palette.coral
    /// To-dos: orange, its overdue ones red.
    static let todos = Color(light: 0xF08A00, dark: 0xFF9F0A)
    static let overdue = Color(light: 0xD93A3A, dark: 0xFF5F57)
    /// Usage: a window by how full it is: calm, getting there (≥ 80 %, `UsageWindows.isWarn`), full (≥ 95 %).
    static let usageCalm = Color(light: 0x248A3D, dark: 0x30D158)
    static let usageWarn = Color(light: 0xF08A00, dark: 0xFF9F0A)
    static let usageFull = Color(light: 0xD93A3A, dark: 0xFF5F57)
    static let warnAt = 80.0
    static let fullAt = 95.0
    /// Today's tokens by the hour; the current hour in full.
    static let tokens = Color(light: 0x2A78D6, dark: 0x3987E5)
    /// Computers: a computer's ring when it is calm (its load); working or waiting take the agent states'.
    static let computerCalm = usageCalm
    /// Record: the system's recording red, and the button's darker red under a white mark.
    static let recording = Palette.recordingRed
    static let recordButton = Palette.recordingButton
    /// Health: steps, sleep, heart.
    static let steps = Color(light: 0xE0336E, dark: 0xFF4F87)
    static let sleep = Color(light: 0x5B4FD6, dark: 0x9C94FF)
    static let heart = Color(light: 0xD93A3A, dark: 0xFF5F57)
    /// Inbox: Needs You's blue.
    static let inbox = StatusPalette.needsYou

    // MARK: Type

    /// The widget's name, in its color, at the top (as Calendar's weekday).
    static let title = Font.system(.footnote, weight: .semibold)
    /// The one number a widget is about.
    static let hero = Font.system(size: 52, weight: .semibold, design: .rounded)
    static let heroMedium = Font.system(size: 40, weight: .semibold, design: .rounded)
    static let heroSmall = Font.system(size: 28, weight: .semibold, design: .rounded)
    /// The day of the month on Today.
    static let day = Font.system(size: 44, weight: .regular)
    /// How far a hero may shrink to fit ("1.2M", "12,480").
    static let heroMinScale: CGFloat = 0.5
    /// How far a name in a row may shrink before it is cut short ("MacBook Air" beside a large ring).
    static let nameMinScale: CGFloat = 0.8
    /// The line under a hero, and the first line of a row.
    static let headline = Font.system(.subheadline, weight: .semibold)
    /// A row's or a hero's second line.
    static let body = Font.system(.footnote)
    /// Times, counts beside, chart labels.
    static let caption = Font.system(.caption)
    static let captionStrong = Font.system(.caption, weight: .semibold)
    /// A ring's number in its middle.
    static let ringNumber = Font.system(size: 30, weight: .semibold, design: .rounded)
    static let ringNumberSmall = Font.system(size: 15, weight: .semibold, design: .rounded)
    /// Lock Screen: the rectangular widget's first line and the rest.
    static let lockTitle = Font.system(.headline)
    static let lockLine = Font.system(.caption)

    // MARK: Spacing

    static let flush: CGFloat = 0
    static let hairline: CGFloat = 1
    static let gap: CGFloat = 8
    static let tight: CGFloat = 4
    static let hair: CGFloat = 2
    /// Between a large widget's rows.
    static let row: CGFloat = 10
    /// Between a medium widget's two halves.
    static let halves: CGFloat = 16

    // MARK: Shapes

    /// An event's color bar.
    static let bar: CGFloat = 4
    /// Rings: the hero ring of a small widget, the medium row's, the computers' four.
    static let ringHero: CGFloat = 96
    static let ringMedium: CGFloat = 64
    static let ringSmall: CGFloat = 50
    static let ringStroke: CGFloat = 10
    static let ringStrokeSmall: CGFloat = 7
    /// The shortest event block in Today's day view: one line of its title.
    static let timelineLine: CGFloat = 16
    /// The charts' height and their bars' corner.
    static let chartHeight: CGFloat = 92
    static let chartBar: CGFloat = 3
    /// The Record button.
    static let recordHero: CGFloat = 88
    static let recordMedium: CGFloat = 76
    static let recordMark = Font.system(size: 30, weight: .semibold)
    static let recordMarkLock = Font.system(size: 24, weight: .semibold)
    /// A state's dot.
    static let dot: CGFloat = 8

    // MARK: Calendar (the switch, the week and the month)

    /// The switch at the Calendar widget's top right (Today · Week · Month): its height, its corner and the chosen
    /// part's, the space around a part's word, the chosen part's fill. A fixed size, as the grids it switches between.
    static let segmentHeight: CGFloat = 22
    static let segmentCorner: CGFloat = 8
    static let segmentInset: CGFloat = 2
    static let segmentPad: CGFloat = 8
    static let segment = Font.system(size: 12, weight: .semibold)
    static let segmentOn = Color(light: 0xFFFFFF, dark: 0x636366)
    static let segmentOnMono = Color.white.opacity(0.35)
    /// A day in the week's strip: its number's disc and the number, the weekday's letter above.
    static let weekDisc: CGFloat = 25
    static let weekNumber = Font.system(size: 15, weight: .semibold)
    static let weekday = Font.system(size: 11, weight: .semibold)
    /// The row of weekday letters over a month: the large widget's, the medium's.
    static let weekdayRow: CGFloat = 16
    static let weekdayRowCompact: CGFloat = 12
    /// The large widget's month: a row, a day's disc and number. The medium widget's: the same, smaller, with the
    /// grid's width beside the day's list.
    static let monthRow: CGFloat = 29
    static let monthDisc: CGFloat = 24
    static let monthNumber = Font.system(size: 15, weight: .medium)
    static let monthRowCompact: CGFloat = 15
    static let monthDiscCompact: CGFloat = 15
    static let monthNumberCompact = Font.system(size: 11, weight: .medium)
    static let weekdayCompact = Font.system(size: 10, weight: .semibold)
    static let monthGridCompact: CGFloat = 150
    /// The dot under a day that has events: in the week, in the large month, in the medium month.
    static let dayDot: CGFloat = 5
    static let dayDotMonth: CGFloat = 4
    static let dayDotCompact: CGFloat = 2.5
    /// Days gone by step back: their dots.
    static let pastDot = 0.45
    /// An event on one line: the bar beside it, its title.
    static let lineBar: CGFloat = 3
    static let lineBarHeight: CGFloat = 13
    static let lineTitle = Font.system(.footnote, weight: .semibold)

    // MARK: The persona (an accent)

    /// In the corner beside the widget's name.
    static let faceCorner: CGFloat = 22
    /// Where a widget has nothing to show (and before pairing).
    static let faceEmpty: CGFloat = 46
    /// The Lock Screen's rectangular widget.
    static let faceLock: CGFloat = 40
    /// Done's check, × the face's side.
    static let checkShare: CGFloat = 0.42
    /// Content from a snapshot older than `HomeSnapshot.staleAfter`.
    static let staleOpacity = 0.5

    // MARK: Symbols (the extension doesn't build Theme.Icon; each mirrors its token)

    static let agentsSymbol = "chevron.left.forwardslash.chevron.right" // icon: mirrors Theme.Icon.agents
    static let todaySymbol = "calendar" // icon: mirrors Theme.Icon.calendarView
    static let todoSymbol = "checklist" // icon: mirrors Theme.Icon.todo
    static let todoOpen = "circle" // icon: an open to-do's box, as the To-dos page draws it
    static let todoDone = "checkmark.circle.fill" // icon: a to-do just ticked
    static let todoFailed = "exclamationmark.circle.fill" // icon: mirrors Theme.Icon.error, filled
    static let usageSymbol = "gauge.with.dots.needle.50percent" // icon: mirrors Theme.Icon.effort (how much is used)
    static let laptopSymbol = "laptopcomputer" // icon: mirrors Theme.Icon.laptop
    static let desktopSymbol = "desktopcomputer" // icon: mirrors Theme.Icon.machine
    static let serverSymbol = "server.rack" // icon: mirrors Theme.Icon.server
    static let recordSymbol = "waveform" // icon: mirrors RecordingSymbol.recording (the bare waveform on the red disc)
    static let stopSymbol = "stop.fill" // icon: mirrors RecordingSymbol.stop
    static let healthSymbol = "heart.fill" // icon: Theme.Icon.health, filled for the small mark
    static let stepsSymbol = "figure.walk" // icon: steps, as the Health app marks them
    static let sleepSymbol = "bed.double.fill" // icon: sleep, as the Health app marks it
    static let inboxSymbol = "tray.full" // icon: mirrors Theme.Icon.inbox
    static let checkSymbol = "checkmark" // icon: mirrors Theme.Icon.done

    /// A symbol as the widgets draw it: plain, at the size of the words around it (the extension builds no
    /// `ThemedIcon`; every name is one of the tokens above).
    static func symbol(_ name: String) -> Image { Image(systemName: name) }

    #if DEBUG
    /// The gallery's made-up wallpapers and the tint it paints tinted widgets with (`-showHomeWidgets`).
    enum Gallery {
        static let lightWall = [Color(hex: 0xDCE7F5), Color(hex: 0xF3E3EC)]
        static let darkWall = [Color(hex: 0x0E1B33), Color(hex: 0x2B1630)]
        static let tintedWall = [Color(hex: 0x1E140A), Color(hex: 0x3A2610)]
        static let clearWall = [Color(hex: 0x3E6FB0), Color(hex: 0xB0566F), Color(hex: 0xE59A4B)]
        static let lockWall = [Color(hex: 0x23305A), Color(hex: 0x5A3E6E)]
        static let tint = Color(hex: 0xF2B45A)
        static let tintedSurface = Color(hex: 0x2A1E12).opacity(0.92)
        static let sheet = Color(hex: 0xF4F4F6)
    }
    #endif

    #if canImport(UIKit)
    private static func dynamic(light: (UInt32, CGFloat), dark: (UInt32, CGFloat)) -> Color {
        Color(uiColor: UIColor { trait in
            let (hex, alpha) = trait.userInterfaceStyle == .dark ? dark : light
            return UIColor(hex: hex).withAlphaComponent(alpha)
        })
    }
    #endif
}
#endif
