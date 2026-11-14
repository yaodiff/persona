import SwiftUI
import PersonaWire

/// An extension theme's role colors, in StyleSpec only while that theme is chosen; roles it leaves out fall back to the original tokens.
nonisolated struct ExtensionThemePalette: Hashable, Sendable {
    var id: String
    var name: String
    var accent: PersonaPalette.Pair
    var positive: PersonaPalette.Pair?
    var caution: PersonaPalette.Pair?
    var cautionText: PersonaPalette.Pair?
    var danger: PersonaPalette.Pair?
    var ai: PersonaPalette.Pair?

    nonisolated(unsafe) static var current: ExtensionThemePalette? {
        didSet { StyleSpec.recompose() }
    }

    init?(id: String, name: String, light: ExtensionThemeColors, dark: ExtensionThemeColors) {
        guard !id.isEmpty, !name.isEmpty, let l = Self.hex(light.accent), let d = Self.hex(dark.accent) else { return nil }
        self.id = id
        self.name = name
        accent = PersonaPalette.Pair(light: l, dark: d)
        var invalid = false
        func role(_ key: KeyPath<ExtensionThemeColors, String?>, fallback: PersonaPalette.Pair) -> PersonaPalette.Pair? {
            let l = light[keyPath: key], d = dark[keyPath: key]
            guard l != nil || d != nil else { return nil }
            let lh = l.flatMap(Self.hex), dh = d.flatMap(Self.hex)
            if l != nil && lh == nil || d != nil && dh == nil { invalid = true; return nil }
            return PersonaPalette.Pair(light: lh ?? fallback.light, dark: dh ?? fallback.dark)
        }
        positive = role(\.positive, fallback: Theme.Role.positive)
        caution = role(\.caution, fallback: Theme.Role.caution)
        cautionText = role(\.caution, fallback: Theme.Role.cautionText)
        danger = role(\.danger, fallback: Theme.Role.danger)
        ai = role(\.ai, fallback: Theme.Role.ai)
        if invalid { return nil }
    }

    /// One set of dynamic colors per style composition; Theme's getters reuse it so each Color keeps a stable identity.
    struct Colors: Sendable {
        var positive: Color?
        var caution: Color?
        var cautionText: Color?
        var danger: Color?
        var ai: Color?
        init(_ palette: ExtensionThemePalette) {
            positive = palette.positive?.color
            caution = palette.caution?.color
            cautionText = palette.cautionText?.color
            danger = palette.danger?.color
            ai = palette.ai?.color
        }
    }

    private static func hex(_ text: String) -> UInt32? {
        guard text.count == 7, text.first == "#", text.dropFirst().allSatisfy(\.isHexDigit) else { return nil }
        return UInt32(text.dropFirst(), radix: 16)
    }
}
