import Foundation

/// The Inbox display clock. DEBUG screenshot launches can pin it to one reproducible moment.
nonisolated enum AppClock {
    static var now: Date {
        #if DEBUG
        pinnedAtLaunch ?? Date()
        #else
        Date()
        #endif
    }

    #if DEBUG
    private struct Cache {
        var loaded = false
        var pinned: Date? = nil
    }

    private static let lock = NSLock()
    nonisolated(unsafe) private static var cache = Cache()

    /// `-showClockAt 2026-01-01T12:00:00Z` pins the clock for the whole launch. Read and parse the argument once,
    /// rather than consulting UserDefaults or an ISO 8601 parser from every row (performance P2 / P4).
    private static var pinnedAtLaunch: Date? {
        lock.withLock {
            if !cache.loaded {
                let value = UserDefaults.standard.string(forKey: "showClockAt")
                cache = Cache(loaded: true, pinned: value.flatMap(JSON.parseDate))
            }
            return cache.pinned
        }
    }

    /// Lets unit tests model separate launches. App code never resets the cached launch argument.
    static func resetForTests() {
        lock.withLock { cache = Cache() }
    }
    #endif
}
