import Foundation

/// The live screen's picture on mobile data (#6396): cellular or a personal hotspot, what the system calls an expensive
/// network. Clearer keeps the tiers up to the 4 Mbit/s cellular cap; Data Saver keeps it at 1.5 Mbit/s and 1× pixels
/// (`ScreenVideoBudget`). Kept on this device for every computer. Until the user chooses, the picture starts on Data
/// Saver and the page asks once (`ScreenMobileDataCard`); unanswered, it asks once more next time, then no more. Low Data
/// Mode is always Data Saver and never asks. Settings › Live Screen › Quality changes it.
nonisolated enum ScreenMobileData: String, CaseIterable, Sendable {
    case clearer, saver

    static let key = "screen.mobileData"
    static let askedKey = "screen.mobileData.asked"
    /// Times the question goes unanswered before it stops being asked.
    static let asks = 2

    /// What the user chose; nil until they do.
    static var chosen: ScreenMobileData? {
        UserDefaults.standard.string(forKey: key).flatMap(ScreenMobileData.init(rawValue:))
    }

    /// What the picture uses on mobile data: the choice, else Data Saver.
    static var current: ScreenMobileData { chosen ?? .saver }

    static func choose(_ m: ScreenMobileData) { UserDefaults.standard.set(m.rawValue, forKey: key) }

    /// Ask on this stream: nothing chosen yet, and asked fewer than `asks` times.
    static var shouldAsk: Bool { chosen == nil && UserDefaults.standard.integer(forKey: askedKey) < asks }

    /// The question was shown (an answer counts as one too; `choose` makes it moot).
    static func markAsked() {
        UserDefaults.standard.set(UserDefaults.standard.integer(forKey: askedKey) + 1, forKey: askedKey)
    }

    /// The choice in plain words, with about how much data an hour of typical use takes (screenbench, #6396: 40% still,
    /// 25% typing, 20% scrolling, 15% video).
    var title: String {
        switch self {
        case .clearer: String(localized: "Clearer")
        case .saver: String(localized: "Data Saver")
        }
    }

    var usage: String {
        switch self {
        case .clearer: String(localized: "About 0.6 GB an hour")
        case .saver: String(localized: "About 0.2–0.3 GB an hour")
        }
    }
}
