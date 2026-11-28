import SwiftUI
import PersonaWire

/// The token usage charts' colors (docs/server/machines.md "Token usage", #4973): one per kind, light and dark steps of the
/// charts' categorical palette, in the stacking order (input, output, cache write, cache read) validated as a set
/// with the dataviz validator: adjacent pairs at least ΔE 24.7 (light) / 17.3 (dark) for colour-blind readers.
/// Light teal is under 3:1 on a light card, so the legend always writes the numbers beside it.
nonisolated enum UsageColor {
    static let input = Color(light: 0x2A78D6, dark: 0x3987E5)
    static let output = Color(light: 0xEB6834, dark: 0xD95926)
    static let cacheWrite = Color(light: 0x6250D6, dark: 0x9085E9)
    static let cacheRead = Color(light: 0x1BAF7A, dark: 0x199E70)
    /// The cache hit rate's one line.
    static let hitRate = cacheRead
    /// Token usage by project (#16530): non-development projects (folders, the home folder, the assistant), apart from
    /// the kinds' teal; development ones take the accent. At least 3:1 on a card in both schemes; the web's
    /// `--project-other`.
    static let projectOther = Color(light: 0x0F7F8F, dark: 0x4BBFCC)
    /// A card's sparkline of all tokens.
    static let total = input

    static func of(_ kind: UsageKind) -> Color {
        switch kind {
        case .input: input
        case .output: output
        case .cacheWrite: cacheWrite
        case .cacheRead: cacheRead
        }
    }
}
