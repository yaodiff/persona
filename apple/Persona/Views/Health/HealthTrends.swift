import Foundation
import PersonaWire

/// The health trends the app draws from the phone's own Health data (#3272): steps, sleep and weight per day, each as a
/// ```persona-card (#886) so they look like the cards in a health answer. Pure: the days and their values in, the card
/// out; HealthTrendsModel reads the values.
nonisolated enum HealthTrendMetric: String, CaseIterable, Identifiable, Sendable {
    case steps, sleep, weight

    var id: String { rawValue }

    var title: String {
        switch self {
        case .steps: String(localized: "Steps")
        case .sleep: String(localized: "Sleep [Health Tool 01324e]")
        case .weight: String(localized: "Sleep [Health Tool 38f7c7]")
        }
    }

    var unit: String {
        switch self {
        case .steps: String(localized: "steps")
        case .sleep: String(localized: "hr")
        case .weight: String(localized: "kg")
        }
    }

    /// Steps are counted per day (bars); sleep and weight are levels that move (a line).
    var kind: String { self == .steps ? ExtensionPersonaCardKind.bar : ExtensionPersonaCardKind.line }
}

/// How far back the trends look.
nonisolated enum HealthTrendRange: Int, CaseIterable, Identifiable, Sendable {
    case week = 7
    case month = 30

    var id: Int { rawValue }
    var title: String { self == .week ? String(localized: "Last 7 Days") : String(localized: "Last 30 Days") }
}

nonisolated enum HealthTrends {
    /// The last `days` days ending today, oldest first, keyed as the health summary keys them ("yyyy-MM-dd").
    /// Only `calendar`'s time zone is used: the summary's keys are Gregorian whatever calendar the phone's region uses
    /// (`ToolDate.day`), so a Buddhist or Japanese calendar would give other years and match no day.
    static func dayKeys(_ days: Int, now: Date, calendar: Calendar = .current) -> [String] {
        let g = gregorian(calendar.timeZone)
        let today = g.startOfDay(for: now)
        return (0..<max(days, 1)).reversed().compactMap { back in
            g.date(byAdding: .day, value: -back, to: today).map { key($0, calendar: g) }
        }
    }

    static func key(_ day: Date, calendar: Calendar = .current) -> String {
        let c = gregorian(calendar.timeZone).dateComponents([.year, .month, .day], from: day)
        return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
    }

    private static func gregorian(_ zone: TimeZone) -> Calendar {
        var g = Calendar(identifier: .gregorian)
        g.timeZone = zone
        return g
    }

    /// A day's label on the chart: the weekday over a week ("Wed"), the date over a month ("11/5").
    static func label(_ key: String, range: HealthTrendRange) -> String {
        let parts = key.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3 else { return key }
        if range == .month { return "\(parts[1])/\(parts[2])" }
        var c = DateComponents()
        c.year = parts[0]; c.month = parts[1]; c.day = parts[2]
        var g = Calendar(identifier: .gregorian)
        g.timeZone = TimeZone(secondsFromGMT: 0) ?? .current
        guard let date = g.date(from: c) else { return key }
        let names = [String(localized: "Sun [Health Trends ee239f]"), String(localized: "Mon [Health Trends c430f4]"), String(localized: "Tue [Health Trends d1378a]"), String(localized: "Wed [Health Trends 296116]"), String(localized: "Fri [Health Trends e9d3b0]"), String(localized: "Sat [Health Trends 572388]"), String(localized: "Sat [Health Trends f9aa11]")]
        return names[(g.component(.weekday, from: date) - 1) % 7]
    }

    /// The card for one metric over `days` (oldest first), nil when there is no day with data.
    /// - steps: a bar per day, a day without steps is 0 (today's are partial); the note is the daily average.
    /// - sleep, weight: a line through the days that have data; one day only is a single number.
    static func card(_ metric: HealthTrendMetric, values: [String: Double], days: [String], range: HealthTrendRange) -> ExtensionPersonaCard? {
        let known = days.compactMap { d in values[d].map { (day: d, value: $0) } }
        guard let last = known.last else { return nil }
        let subtitle = range.title
        if known.count == 1 && metric != .steps {
            return ExtensionPersonaCard(type: ExtensionPersonaCardKind.stat, title: metric.title, value: last.value, unit: metric.unit,
                                        note: String(localized: "Entry from \(label(last.day, range: range))"))
        }
        let points: [ExtensionCardPart]
        if metric == .steps {
            points = days.map { ExtensionCardPart(label: label($0, range: range), value: values[$0] ?? 0) }
        } else {
            points = known.map { ExtensionCardPart(label: label($0.day, range: range), value: $0.value) }
        }
        return ExtensionPersonaCard(type: metric.kind, title: metric.title, subtitle: subtitle, value: last.value, unit: metric.unit,
                                    note: note(metric, known.map(\.value)), points: points)
    }

    /// What one line under the chart says: the average of the days with data, or for weight how much it moved.
    static func note(_ metric: HealthTrendMetric, _ values: [Double]) -> String? {
        guard let first = values.first, let last = values.last else { return nil }
        switch metric {
        case .weight:
            let change = last - first
            let sign = change > 0.05 ? "+" : ""
            return String(localized: "Change over the period \(sign)\(ExtensionPersonaCard.amount(change, metric.unit))")
        case .steps, .sleep:
            let average = values.reduce(0, +) / Double(values.count)
            return String(localized: "Daily average \(ExtensionPersonaCard.amount(average, metric.unit)) (\(values.count) days with data)")
        }
    }
}
