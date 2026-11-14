import Foundation
import Observation
import PersonaWire

/// Reads the phone's steps, sleep and weight for the health trends (#3272). The values come from the same HealthKit
/// queries `phone_health_summary` answers with (`HealthTool.daily`); a metric that fails or has no data is left out,
/// so one missing permission does not hide the others.
@MainActor @Observable final class HealthTrendsModel {
    struct Deps {
        /// Whether Health is on this device and has been asked for (the system never tells an app whether reading
        /// was allowed).
        var access: () -> PhoneAuthStatus
        var requestAccess: () async -> Bool
        var daily: (HealthTrendMetric, Int) async throws -> [String: Double]
        var now: () -> Date

        static var live: Deps {
            Deps(access: { HealthAccess.cachedStatus() },
                 requestAccess: { await HealthAccess.request() != .notDetermined },
                 daily: { metric, days in try await HealthTool.daily(metric.rawValue, days: days) },
                 now: { Date() })
        }
    }

    enum State: Equatable {
        case loading
        /// Health was never asked for on this phone.
        case needsAccess
        /// This device has no Health data (iPad without Health, Mac, Vision).
        case unavailable
        /// The cards in metric order; empty when no metric has data (or reading was not allowed).
        case loaded([HealthTrendMetric: ExtensionPersonaCard])
    }

    private(set) var state: State = .loading
    var range: HealthTrendRange = .week
    /// The range the cards on screen are for.
    @ObservationIgnored private var loadedRange: HealthTrendRange?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    func load() async {
        switch deps.access() {
        case .notNeeded: state = .unavailable; return
        case .notDetermined: state = .needsAccess; return
        default: break
        }
        let range = self.range
        // A reload of the same range keeps its cards; another range does not show the old one's under the new title.
        if case .loaded = state, loadedRange == range {} else { state = .loading }
        let days = HealthTrends.dayKeys(range.rawValue, now: deps.now())
        var cards: [HealthTrendMetric: ExtensionPersonaCard] = [:]
        for metric in HealthTrendMetric.allCases {
            guard let values = try? await deps.daily(metric, range.rawValue) else { continue }
            if let card = HealthTrends.card(metric, values: values, days: days, range: range) { cards[metric] = card }
        }
        guard range == self.range else { return } // the range changed while reading; that load draws
        state = .loaded(cards)
        loadedRange = range
    }

    func requestAccess() async {
        _ = await deps.requestAccess()
        await load()
    }
}
