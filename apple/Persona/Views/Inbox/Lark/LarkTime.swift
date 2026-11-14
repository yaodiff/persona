import SwiftUI

/// A small tag after a chat's name: External, Bot, Public, Meeting…
struct LarkNameTag: View {
    var text: String

    var body: some View {
        let hue: Color = switch text {
        case String(localized: "Bot"), "AI": LarkColor.hue("orange")
        case String(localized: "Public"): LarkColor.hue("green")
        default: LarkColor.blue
        }
        Text(text)
            .font(Theme.Font.labelMedium)
            .foregroundStyle(hue)
            .lineLimit(1)
            .padding(.horizontal, Theme.Space.xs)
            .padding(.vertical, 1.5)
            .background(hue.opacity(0.13), in: .rect(cornerRadius: 4, style: .continuous))
            .fixedSize()
    }
}

/// Feishu's time tiers and list times, used by the inbox rows, the doc reader and the chat's time separators.
nonisolated enum LarkTime {
    /// How far back a time is, as Feishu tiers it: the list and the chat's separators word each tier their own way.
    enum Tier: Equatable { case today, yesterday, thisWeek, thisYear, older }

    /// Today, yesterday, two to six days back (a weekday), earlier this year, or before that. Days are calendar
    /// days in `calendar`'s time zone; a time after today counts as this year or older.
    static func tier(of d: Date, now: Date = AppClock.now, calendar cal: Calendar = .current) -> Tier {
        let days = cal.dateComponents([.day], from: cal.startOfDay(for: d), to: cal.startOfDay(for: now)).day ?? 0
        switch days {
        case 0: return .today
        case 1: return .yesterday
        case 2...6: return .thisWeek
        default: return cal.component(.year, from: d) == cal.component(.year, from: now) ? .thisYear : .older
        }
    }

    /// Feishu's feed times: 05:59, Yesterday, Wednesday, Oct 21, 2025/10/21.
    static func short(_ d: Date?, now: Date = AppClock.now, calendar: Calendar = .current) -> String {
        guard let d, d.isMeaningful else { return "" }
        return switch tier(of: d, now: now, calendar: calendar) {
        case .today: d.formatted(.dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits))
        case .yesterday: String(localized: "Yesterday")
        case .thisWeek: d.formatted(.dateTime.weekday(.wide))
        case .thisYear: d.formatted(.dateTime.month().day())
        case .older: d.formatted(.dateTime.year().month(.twoDigits).day(.twoDigits))
        }
    }
}
