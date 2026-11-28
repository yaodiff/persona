import SwiftUI

// Word lookup (#10977, docs/design/DESIGN_SYSTEM.md "Word lookup"): the lookup card's limits and the optional underline
// of saved words. A file of their own, away from Theme.swift; Shared/, so the watch and the widgets compile them, though
// they draw no lookups.

nonisolated extension Theme {
    enum TextLookup {
        /// A touch held longer than a hold's start is no lookup tap: it is a long press or a selection (`Theme.Hold`).
        static let tapLimit: TimeInterval = 0.3
        /// The senses a card lists; the extension may send more.
        static let senseLimit = 6
        /// A long word shrinks this far on its one line before it is cut.
        static let minimumScale: CGFloat = 0.6
        /// The saved-word underline: a hairline this thick, this far above the line's bottom (under the descenders' box,
        /// over the next line's room).
        static let underline: CGFloat = 1
        static let underlineInset: CGFloat = 1
        /// Its ink: the accent, quiet enough not to read as a link.
        @MainActor static var underlineInk: Color { Theme.accent.opacity(0.55) }
    }
}
