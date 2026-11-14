import SwiftUI
import Testing
import UIKit
@testable import Persona

/// Every style in every color (Interface Style × Theme Color, a persona's included) against the readability floors in docs/apple/ios.md
/// (「设计语言」): white words on the accent (solid buttons, selected chips) ≥ 3.5:1, the words on the user's own bubble
/// ≥ 4.5:1, the accent as a link on the page ≥ 3:1. In light and in dark.
struct StyleSpecTests {
    private static func rgb(_ c: Color, _ style: UIUserInterfaceStyle) -> (Double, Double, Double) {
        let u = UIColor(c).resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        u.getRed(&r, green: &g, blue: &b, alpha: &a)
        return (Double(r), Double(g), Double(b))
    }

    private static func luminance(_ c: (Double, Double, Double)) -> Double {
        func lin(_ v: Double) -> Double { v <= 0.03928 ? v / 12.92 : pow((v + 0.055) / 1.055, 2.4) }
        return 0.2126 * lin(c.0) + 0.7152 * lin(c.1) + 0.0722 * lin(c.2)
    }

    static func contrast(_ a: Color, _ b: Color, _ style: UIUserInterfaceStyle) -> Double {
        contrast(rgb(a, style), rgb(b, style))
    }

    private static func contrast(_ a: (Double, Double, Double), _ b: (Double, Double, Double)) -> Double {
        let la = luminance(a), lb = luminance(b)
        return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
    }

    /// `top` (with its opacity) laid over `under`, as drawn.
    private static func over(_ top: Color, _ under: (Double, Double, Double), _ style: UIUserInterfaceStyle) -> (Double, Double, Double) {
        let u = UIColor(top).resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
        var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        u.getRed(&r, green: &g, blue: &b, alpha: &a)
        func mix(_ x: CGFloat, _ y: Double) -> Double { Double(x * a) + y * Double(1 - a) }
        return (mix(r, under.0), mix(g, under.1), mix(b, under.2))
    }

    static func hex(_ c: Color, _ style: UIUserInterfaceStyle = .light) -> UInt32 {
        let (r, g, b) = rgb(c, style)
        return UInt32((r * 255).rounded()) << 16 | UInt32((g * 255).rounded()) << 8 | UInt32((b * 255).rounded())
    }

    private static let appearances: [(String, UIUserInterfaceStyle)] = [("light", .light), ("dark", .dark)]

    /// A persona's colors as a package brings them, made to meet the floors (PersonaPalette): yellow, the hardest one
    /// (white words do not read on it, so it is darkened to honey).
    static let yellow = PersonaPalette(light: ["primary": "#F2C230", "accent": "#F2C230", "bubble": "#F2C230"],
                                       dark: ["primary": "#F2C230", "accent": "#F7D154", "bubble": "#E0B020"])!

    /// Every color on offer: the fixed ones and a persona's.
    private static var colors: [(String, ColorSpec)] {
        ThemeColor.presets.compactMap { c in c.preset.map { (c.rawValue, $0) } } + [("persona", ColorSpec(persona: yellow, over: .blue))]
    }

    private static func below(_ floor: Double, _ pair: (StyleSpec) -> (Color, Color)) -> [String] {
        AppStyle.allCases.flatMap { style in
            colors.flatMap { name, colors in
                appearances.compactMap { look, ui in
                    let (a, b) = pair(StyleSpec(style.component, colors))
                    let c = contrast(a, b, ui)
                    return c < floor ? "\(style.rawValue)+\(name) \(look): \(String(format: "%.2f", c))" : nil
                }
            }
        }
    }

    /// Below a floor on purpose: Lark's dark blue is Lark's own (#4C88FF), 3.35:1 under white.
    private static let known = "+lark dark"

    @Test func whiteOnTheAccent() {
        let found = Self.below(3.5) { (.white, $0.accent) }.filter { !$0.contains(Self.known) }
        #expect(found.isEmpty, "white on the accent under 3.5:1: \(found)")
    }

    @Test func wordsOnTheUsersBubble() {
        let found = Self.below(4.5) { ($0.userBubbleText, $0.userBubble) }
        #expect(found.isEmpty, "bubble text under 4.5:1: \(found)")
    }

    @Test func theAccentAsALink() {
        let found = Self.below(3) { ($0.accent, $0.page) }
        #expect(found.isEmpty, "the accent on the page under 3:1: \(found)")
    }

    /// A secondary soft button (Clay, Island) draws its label in the tint on `card` (`SoftButtonStyle`, #6157): the accent
    /// and the danger color ≥ 3:1 on it. The glass and bordered buttons' fills are the system's, not tokens:
    /// `ButtonContrastUITests` measures those on screen.
    @Test func secondarySoftButtonsRead() {
        var found: [String] = []
        for style in AppStyle.allCases where style.component.buttons == .soft {
            for (name, colors) in Self.colors {
                let spec = StyleSpec(style.component, colors)
                for (look, ui) in Self.appearances {
                    for (what, ink) in [("accent", spec.accent), ("danger", spec.extensionColors?.danger ?? Theme.Role.dangerColor)] {
                        let c = Self.contrast(ink, spec.card, ui)
                        if c < 3 { found.append("\(style.rawValue)+\(name) \(look) \(what): \(String(format: "%.2f", c))") }
                    }
                }
            }
        }
        #expect(found.isEmpty, "a soft secondary button's label under 3:1 on the card: \(found)")
    }

    /// What sits in the user's own message (#5204): its words, and the blocks it opens with (quotes, forwards, text to
    /// change, comments), whose rule, heading and words are the words' color, ≥ 4.5:1 on the block's fill laid over the
    /// bubble; a path linked in the words ≥ 3:1. On Persona's main conversation's box and on the style's bubble, at its
    /// top and its deepened bottom. The style's white words of a colored bubble once drew blocks at 1.35:1 on the box.
    @Test func whatSitsInTheUsersBubble() {
        var found: [String] = []
        for boxed in [true, false] {
            for style in AppStyle.allCases {
                for (name, colors) in Self.colors {
                    let ink = Theme.UserBubbleInk.of(StyleSpec(style.component, colors), boxed: boxed)
                    for (look, ui) in Self.appearances {
                        let top = Self.rgb(ink.bubble, ui)
                        // `Theme.deepening`: 12% toward black at the bottom.
                        let ends = ink.deepens ? [top, (top.0 * 0.88, top.1 * 0.88, top.2 * 0.88)] : [top]
                        for (end, under) in zip(["top", "bottom"], ends) {
                            let block = Self.over(ink.quoteFill, under, ui)
                            let at = "\(boxed ? "box" : "bubble") \(style.rawValue)+\(name) \(look) \(end)"
                            for (what, a, b, floor) in [("words", ink.words, under, 4.5), ("block", ink.words, block, 4.5),
                                                        ("link", ink.link, under, 3.0)] {
                                let c = Self.contrast(Self.rgb(a, ui), b)
                                if c < floor { found.append("\(at) \(what): \(String(format: "%.2f", c))") }
                            }
                        }
                    }
                }
            }
        }
        #expect(found.isEmpty, "under the floor in the user's bubble: \(found)")
    }

    /// The quieter Markdown headings (H4–H6, `Theme.Reply.secondaryInk`, #3863) are text: ≥ 4.5:1 on every page and
    /// card, and on Persona's reply box, in light and dark.
    @Test func quieterHeadingsReadAsText() {
        let ink = Theme.Reply.secondaryInk, hex = Theme.Reply.secondaryInkHex
        let found = Self.below(4.5) { (ink, $0.page) } + Self.below(4.5) { (ink, $0.card) }
        #expect(found.isEmpty, "the quieter heading ink under 4.5:1: \(found)")
        #expect(Self.hex(ink) == hex.light && Self.hex(ink, .dark) == hex.dark)
        #expect(PersonaPalette.contrast(hex.light, Theme.ChatBox.personaHex.light) >= 4.5)
        #expect(PersonaPalette.contrast(hex.dark, Theme.ChatBox.personaHex.dark) >= 4.5)
    }

    /// The Mac window's chrome a style brings (`StyleSpec.MacChrome`, Lark's), in the style's own colors: a selected
    /// row's icon in the accent ≥ 3:1 on the sections column's raised block and on the selection's wash over the side
    /// column and over the page, the row's words ≥ 4.5:1 on that wash, and the white letter ≥ 3:1 on every monogram
    /// hue (a heading-sized letter, so the large-text floor).
    @Test func theMacChromeReads() {
        var found: [String] = []
        for style in AppStyle.allCases {
            let chrome = style.component.mac
            let spec = StyleSpec(style.component, style.component.colors)
            for (look, ui) in Self.appearances {
                func check(_ what: String, _ a: (Double, Double, Double), _ b: (Double, Double, Double), _ floor: Double) {
                    let c = Self.contrast(a, b)
                    if c < floor { found.append("\(style.rawValue) \(look) \(what): \(String(format: "%.2f", c))") }
                }
                let accent = Self.rgb(spec.accent, ui)
                if let raised = chrome.sidebarSelected { check("icon on the raised block", accent, Self.rgb(raised, ui), 3) }
                if let tint = chrome.selectionTint {
                    let unders: [(String, Color)] = [("side column", chrome.sideColumn ?? spec.page), ("page", spec.page)]
                    for (name, under) in unders {
                        let wash = Self.over(spec.accent.opacity(tint), Self.rgb(under, ui), ui)
                        check("icon on the selection over the \(name)", accent, wash, 3)
                        check("words on the selection over the \(name)", Self.rgb(Theme.primaryText, ui), wash, 4.5)
                    }
                }
                for (i, hue) in chrome.monogramHues.enumerated() {
                    check("white on monogram hue \(i)", Self.rgb(Theme.onSolid, ui), Self.rgb(hue, ui), 3)
                }
            }
        }
        #expect(found.isEmpty, "the Mac chrome under its floors: \(found)")
    }

    /// The minutes' summary card (#6563), on every style's card and the system's grouped cell, in light and dark: a topic
    /// tint as a mark (a group's dot, a bullet's mark) ≥ 3:1 on the card and as a glyph on its soft tile ≥ 3:1; on the
    /// Core Conclusions box (the AI tint over the card) the AI color as its lightbulb ≥ 3:1, and every word ≥ 4.5:1, a
    /// conclusion's time on its chip (the AI's fill over the box) included: the AI color as words falls to 4.1:1 there,
    /// so the box's title and the time's words are primary (`MinutesConclusions.chip`). The tints are never a state's
    /// colors or the AI's.
    @Test func theSummaryCardsTintsRead() {
        let cell = Color(light: 0xFFFFFF, dark: 0x1C1C1E)
        var found: [String] = []
        for style in AppStyle.allCases {
            for (name, colors) in Self.colors {
                let spec = StyleSpec(style.component, colors)
                for (look, ui) in Self.appearances {
                    for (under, card) in [("card", spec.card), ("cell", cell)] {
                        let at = "\(style.rawValue)+\(name) \(look) \(under)"
                        let ground = Self.rgb(card, ui)
                        func check(_ what: String, _ a: (Double, Double, Double), _ b: (Double, Double, Double), _ floor: Double) {
                            let c = Self.contrast(a, b)
                            if c < floor { found.append("\(at) \(what): \(String(format: "%.2f", c))") }
                        }
                        for (i, tint) in Theme.Hue.minutesGroups.enumerated() {
                            let ink = Self.rgb(tint, ui)
                            check("tint \(i) on the card", ink, ground, 3)
                            check("tint \(i) on its tile", ink, Self.over(Theme.softTint(tint), ground, ui), 3)
                        }
                        let well = Self.over(Theme.aiTint, ground, ui)
                        let ai = Self.rgb(Theme.ai, ui)
                        check("AI mark on the well", ai, well, 3)
                        check("words on the well", Self.rgb(Theme.primaryText, ui), well, 4.5)
                        check("time on its chip on the well", Self.rgb(Theme.primaryText, ui), Self.over(Theme.aiTint, well, ui), 4.5)
                    }
                }
            }
        }
        #expect(found.isEmpty, "the summary card under its floors: \(found)")
        let kept: [Color] = [Theme.Role.danger.color, Theme.Role.positive.color, Theme.Role.caution.color, Theme.Hue.orange,
                             Theme.Hue.green, Theme.Hue.red, Theme.Hue.amber, Theme.Hue.yellow, Theme.Role.aiColor]
        for tint in Theme.Hue.minutesGroups {
            #expect(!kept.contains { Self.hex($0) == Self.hex(tint) && Self.hex($0, .dark) == Self.hex(tint, .dark) })
        }
        #expect(Set(Theme.Hue.minutesGroups.map { Self.hex($0) }).count == Theme.Hue.minutesGroups.count)
    }

    /// The colored canvases stay darker than the system's grouped cells in dark (#1C1C1E): Clay's at #1D1A23 was one
    /// color with them (1.01:1); Island's own #0A1110 is 1.13:1.
    @Test func darkWashesSitUnderTheCells() {
        let cells = Color(light: 0xF2F2F7, dark: 0x1C1C1E)
        let found = Self.colors.compactMap { name, c in
            Self.contrast(c.wash.page.color, cells, .dark) < 1.1 ? name : nil
        }
        #expect(found.isEmpty, "a dark wash as light as the cells: \(found)")
    }

    /// Names tell the choices apart in the pickers; two alike would be one twice.
    @Test func everyChoiceIsItsOwn() {
        #expect(Set(AppStyle.allCases.map(\.title)).count == AppStyle.allCases.count)
        #expect(Set(ThemeColor.allCases.map(\.title)).count == ThemeColor.allCases.count)
    }

    /// Match Interface Style draws each style exactly as it was when a style carried its colors, and Liquid Glass + Coral is the old Claude.
    @Test func eachStyleInItsOwnColorsIsAsBefore() {
        func spec(_ s: AppStyle, _ c: ThemeColor = .style) -> StyleSpec { .compose(style: s, color: c, persona: nil) }
        func pair(_ c: Color) -> [UInt32] { [Self.hex(c), Self.hex(c, .dark)] }
        let glass = spec(.glass)
        #expect(pair(glass.accent) == [0x007AFF, 0x0A84FF])
        #expect(pair(glass.userBubble) == [0xDCEAFF, 0x1D3557])
        #expect(pair(glass.page) == pair(Theme.systemPage))
        #expect(pair(glass.lark.myBubble) == pair(Color(light: 0x007AFF, dark: 0x0A84FF).opacity(0.16)))
        #expect(pair(glass.lark.otherBubble) == pair(StyleSpec.larkGrayBubble))
        #expect(!glass.deepensBubble)

        let claude = spec(.glass, .coral)
        #expect(pair(claude.accent) == [0xD4683F, 0xCD6E45])
        #expect(pair(claude.userBubble) == [0xB24C25, 0xAF4C28])
        #expect(pair(claude.lark.blue) == [0xD4683F, 0xCD6E45])
        #expect(claude.deepensBubble && claude.surface == .glass)

        let clay = spec(.clay)
        #expect(pair(clay.accent) == [0x7A5AF8, 0x8979EB])
        #expect(pair(clay.userBubble) == [0x6759D3, 0x6756D9])
        #expect(pair(clay.page) == [0xEFEAF7, 0x0A0510])
        #expect(pair(clay.card) == [0xF8F5FD, 0x2A2533])
        #expect(pair(clay.puffyShadow) == [0x6B54D6, 0x000000])
        #expect(pair(clay.puffyHighlight) == [0xFFFFFF, 0x3A3445])
        #expect(pair(clay.lark.otherBubble) == pair(clay.card))

        let island = spec(.island)
        #expect(pair(island.accent) == [0x27747F, 0x2A9798])
        #expect(pair(island.userBubble) == [0x14201E, 0xE8F3F1])
        #expect(pair(island.userBubbleText) == [0xFFFFFF, 0x0B1211])
        #expect(pair(island.page) == [0xE6F1EF, 0x0A1110])
        #expect(pair(island.card) == [0xFFFFFF, 0x16201E])
        #expect(pair(island.softShadow) == pair(StyleSpec.defaultSoftShadow))

        let lark = spec(.lark)
        #expect(pair(lark.accent) == [0x3370FF, 0x4C88FF])
        #expect(pair(lark.userBubble) == [0xD1E3FF, 0x133063])
        #expect(pair(lark.page) == [0xF5F6F7, 0x141414])
        #expect(pair(lark.card) == [0xFFFFFF, 0x1F1F1F])
        #expect(pair(lark.lark.myBubble) == [0xD1E3FF, 0x133063])
        #expect(pair(lark.lark.cardBackground) == [0xFFFFFF, 0x1E1F23])

        let beautiful = spec(.beautiful)
        #expect(pair(beautiful.accent) == pair(glass.accent) && beautiful.components == .beautiful)
    }

    /// A color goes with any style: its accent, bubble and Lark's links on Clay, its wash under Clay and Island, while
    /// Liquid Glass and Lark keep their neutral pages.
    @Test func colorsAndStylesCombine() {
        let clayCoral = StyleSpec.compose(style: .clay, color: .coral, persona: nil)
        #expect(Self.hex(clayCoral.accent) == 0xD4683F)
        #expect(Self.hex(clayCoral.lark.blue) == 0xD4683F)
        #expect(Self.hex(clayCoral.page) == Self.hex(ColorSpec.coral.wash.page.color))
        #expect(clayCoral.surface == .puffy && clayCoral.fontDesign == .rounded)
        let islandCoral = StyleSpec.compose(style: .island, color: .coral, persona: nil)
        #expect(Self.hex(islandCoral.card) == 0xFFFFFF)
        let larkLavender = StyleSpec.compose(style: .lark, color: .lavender, persona: nil)
        #expect(Self.hex(larkLavender.page) == 0xF5F6F7)
        #expect(Self.hex(larkLavender.lark.blue) == 0x7A5AF8)
        #expect(larkLavender.surface == .hairline)
    }

    /// A persona colors the app only under Match Avatar; its own primary (the face's glow) is there whatever the colors.
    @Test func aPersonaColorsOnlyWhenFollowed() {
        let p = Self.yellow
        let followed = StyleSpec.compose(style: .clay, color: .persona, persona: p)
        #expect(Self.hex(followed.accent) == p.accent?.light)
        #expect(Self.hex(followed.userBubble) == p.bubble?.light)
        #expect(Self.hex(followed.page) == Self.hex(ColorSpec.Wash(accent: p.accent!).page.color))
        let ownColors = StyleSpec.compose(style: .clay, color: .style, persona: p)
        #expect(Self.hex(ownColors.accent) == 0x7A5AF8)
        #expect(ownColors.personaPrimary != nil && followed.personaPrimary != nil)
        // The built-in brings no colors: Match Avatar is the style's own.
        #expect(Self.hex(StyleSpec.compose(style: .island, color: .persona, persona: nil).accent) == 0x27747F)
    }

    /// Settings from before Theme Color was a choice become the choices that look the same.
    @Test func oldSettingsBecomeTheSameLook() {
        typealias C = AppearanceChoice
        #expect(C.resolve(style: nil, color: nil, persona: nil) == C(style: .glass, color: .style))
        #expect(C.resolve(style: "clay", color: nil, persona: nil) == C(style: .clay, color: .style))
        #expect(C.resolve(style: "claude", color: nil, persona: nil) == C(style: .glass, color: .coral))
        #expect(C.resolve(style: "island", color: nil, persona: "persona") == C(style: .island, color: .style))
        // A persona chosen colored the app: it still does.
        #expect(C.resolve(style: "clay", color: nil, persona: "nwverse") == C(style: .clay, color: .persona))
        #expect(C.resolve(style: "claude", color: nil, persona: "nwverse") == C(style: .glass, color: .persona))
        // Chosen since: kept as chosen.
        #expect(C.resolve(style: "lark", color: "coral", persona: "nwverse") == C(style: .lark, color: .coral))
        #expect(C.resolve(style: "island", color: "style", persona: "nwverse") == C(style: .island, color: .style))
        // `-ui.style claude` as a launch argument over a saved choice still means coral.
        #expect(C.resolve(style: "claude", color: "style", persona: nil) == C(style: .glass, color: .coral))
        #expect(C.resolve(style: "neon", color: "plaid", persona: nil) == C(style: .glass, color: .style))
    }
}
