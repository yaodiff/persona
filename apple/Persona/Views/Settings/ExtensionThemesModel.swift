import Foundation
import Observation
import SwiftUI
#if os(macOS)
import AppKit
#else
import UIKit
#endif
import PersonaWire

/// The API's theme entries reuse the generated manifest type, adding only the owning extension and the server's validation result.
nonisolated struct ExtensionThemeOffer: Codable, Hashable, Sendable, Identifiable {
    var owner: String
    var ownerName: String
    var theme: ExtensionThemeItem
    var problems: [String]
    var id: String { owner + "/" + theme.id }

    enum CodingKeys: String, CodingKey { case owner = "extension", ownerName = "extension_name", problems }
    init(from decoder: Decoder) throws {
        theme = try ExtensionThemeItem(from: decoder)
        let c = try decoder.container(keyedBy: CodingKeys.self)
        owner = try c.decode(String.self, forKey: .owner)
        ownerName = try c.decode(String.self, forKey: .ownerName)
        problems = try c.decodeIfPresent([String].self, forKey: .problems) ?? []
    }
    func encode(to encoder: Encoder) throws {
        try theme.encode(to: encoder)
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(owner, forKey: .owner)
        try c.encode(ownerName, forKey: .ownerName)
        try c.encode(problems, forKey: .problems)
    }
}

extension APIClient {
    func extensionThemes() async throws -> [ExtensionThemeOffer] {
        struct Answer: Decodable { var themes: [ExtensionThemeOffer] }
        let answer: Answer = try await get(extensionPath("/-/themes"))
        return answer.themes
    }
}

/// Checked on the real style backgrounds, with StyleSpecTests' floor; a failing contribution is not recolored to hide it.
@MainActor enum ExtensionThemeContrast {
    static func palette(_ offer: ExtensionThemeOffer) -> ExtensionThemePalette? {
        guard offer.problems.isEmpty,
              let palette = ExtensionThemePalette(id: offer.id, name: offer.theme.name,
                                                  light: offer.theme.light, dark: offer.theme.dark) else { return nil }
        for style in AppStyle.allCases {
            let spec = StyleSpec(style.component, ColorSpec(extensionTheme: palette, over: style.component.colors))
            for dark in [false, true] {
                guard contrast(.white, spec.accent, dark: dark) >= PersonaPalette.onAccent,
                      contrast(spec.accent, spec.page, dark: dark) >= PersonaPalette.link,
                      contrast(spec.userBubbleText, spec.userBubble, dark: dark) >= PersonaPalette.bubbleWords else { return nil }
                let given = dark ? offer.theme.dark : offer.theme.light
                let roles: [(String?, PersonaPalette.Pair?)] = [(given.positive, palette.positive), (given.caution, palette.caution),
                                                                (given.danger, palette.danger), (given.ai, palette.ai)]
                for (provided, pair) in roles where provided != nil {
                    guard let color = pair?.color,
                          contrast(color, spec.page, dark: dark) >= PersonaPalette.link,
                          contrast(color, spec.card, dark: dark) >= PersonaPalette.link else { return nil }
                }
            }
        }
        return palette
    }

    private static func contrast(_ a: Color, _ b: Color, dark: Bool) -> Double {
        let x = luminance(a, dark: dark), y = luminance(b, dark: dark)
        return (max(x, y) + 0.05) / (min(x, y) + 0.05)
    }

    private static func luminance(_ color: Color, dark: Bool) -> Double {
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        #if os(macOS)
        let appearance = NSAppearance(named: dark ? .darkAqua : .aqua)!
        appearance.performAsCurrentDrawingAppearance {
            NSColor(color).usingColorSpace(.sRGB)?.getRed(&r, green: &g, blue: &b, alpha: &a)
        }
        #else
        UIColor(color).resolvedColor(with: UITraitCollection(userInterfaceStyle: dark ? .dark : .light))
            .getRed(&r, green: &g, blue: &b, alpha: &a)
        #endif
        func linear(_ value: CGFloat) -> Double {
            let value = Double(value)
            return value <= 0.03928 ? value / 12.92 : pow((value + 0.055) / 1.055, 2.4)
        }
        return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
    }
}

/// Saves only the chosen theme, for offline launches; the list is in-memory state and does not write UserDefaults often.
@MainActor @Observable final class ExtensionThemesModel {
    struct Deps {
        var color: () -> ThemeColor
        var setColor: (ThemeColor) -> Void
        static var live: Deps { .init(color: { AppModel.shared.themeColor }, setColor: { AppModel.shared.themeColor = $0 }) }
    }

    static let shared = ExtensionThemesModel()
    private(set) var offers: [ExtensionThemeOffer] = []
    private(set) var palette: ExtensionThemePalette?
    @ObservationIgnored private var source: String?
    @ObservationIgnored private var selected: ExtensionThemeOffer?
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private let cache: URL?
    @ObservationIgnored private let deps: Deps

    private struct Saved: Codable { var source: String; var offer: ExtensionThemeOffer }

    init(cache: URL? = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first?
        .appending(path: "extension-theme-selection.json"), deps: Deps = .live) {
        self.cache = cache
        self.deps = deps
        if let cache, let data = try? Data(contentsOf: cache), let saved = try? JSON.decoder.decode(Saved.self, from: data),
           let palette = ExtensionThemeContrast.palette(saved.offer) {
            source = saved.source
            selected = saved.offer
            offers = [saved.offer]
            self.palette = palette
            ExtensionThemePalette.current = palette
        }
    }

    func refresh(api: APIClient?) async {
        guard let api else { return }
        generation += 1
        let mine = generation
        let nextSource = api.group.isEmpty ? api.config.url.absoluteString : api.group
        if source != nextSource {
            source = nextSource
            offers = []
            clearSelection()
        }
        guard api.extensionsAPI else { offers = []; clearSelection(); return }
        let fresh: [ExtensionThemeOffer]
        do { fresh = try await api.extensionThemes() }
        catch let error as APIError where error.status == 404 { fresh = [] }
        catch { return }
        guard generation == mine, !Task.isCancelled else { return }
        let valid = fresh.filter { ExtensionThemeContrast.palette($0) != nil }
        if valid != offers { offers = valid }
        if let selected {
            if let updated = valid.first(where: { $0.id == selected.id }) { apply(updated) }
            else { clearSelection() }
        }
    }

    func select(_ offer: ExtensionThemeOffer) {
        guard offers.contains(offer) else { return }
        apply(offer)
        deps.setColor(.extensionTheme)
    }

    private func apply(_ offer: ExtensionThemeOffer) {
        guard let next = ExtensionThemeContrast.palette(offer) else { return }
        let changed = selected != offer || palette != next
        selected = offer
        if palette != next { palette = next; ExtensionThemePalette.current = next }
        if changed, let source, let cache, let data = try? JSON.encoder.encode(Saved(source: source, offer: offer)) {
            try? FileManager.default.createDirectory(at: cache.deletingLastPathComponent(), withIntermediateDirectories: true)
            try? data.write(to: cache, options: .atomic)
            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: cache.path)
        }
    }

    private func clearSelection() {
        selected = nil
        if palette != nil { palette = nil; ExtensionThemePalette.current = nil }
        if let cache { try? FileManager.default.removeItem(at: cache) }
        if deps.color() == .extensionTheme { deps.setColor(.style) }
    }
}
