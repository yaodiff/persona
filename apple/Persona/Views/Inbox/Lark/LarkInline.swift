import SwiftUI
#if canImport(UIKit)
import UIKit
#endif
import PersonaWire

// MARK: - Colors

/// Feishu's named palette (card text colors, tags, header templates), light / dark.
nonisolated enum LarkColor {
    /// Chrome color (links, selection, the "new messages" line): Feishu blue in the Lark style, else the style's accent.
    /// Card content keeps Feishu's own palette (`text(_:)` / `hue(_:)`).
    static var blue: Color { StyleSpec.current.lark.blue }
    /// The Lark gateway's --message-outgoing / --message-incoming, tinted with the style's accent elsewhere.
    static var myBubble: Color { StyleSpec.current.lark.myBubble }
    static var otherBubble: Color { StyleSpec.current.lark.otherBubble }
    static var cardBackground: Color { StyleSpec.current.lark.cardBackground }
    static var cardStroke: Color { StyleSpec.current.lark.cardStroke }

    private static let named: [String: (UInt32, UInt32)] = [
        "blue": (0x3370FF, 0x4C88FF), "wathet": (0x049FD7, 0x26B5E8), "turquoise": (0x04B49C, 0x23C4AE),
        "green": (0x2EA121, 0x51BA43), "lime": (0x8FAC02, 0xA3C00E), "yellow": (0xDC9B04, 0xF5BF2D),
        "orange": (0xF77234, 0xFF8F4D), "red": (0xF54A45, 0xF76964), "carmine": (0xE0457E, 0xF06A97),
        "violet": (0xC34ACF, 0xD772E0), "purple": (0x7F3BF5, 0x9F6FF8), "indigo": (0x4954E6, 0x6B74F0),
        "grey": (0x8F959E, 0x8C8F96), "gray": (0x8F959E, 0x8C8F96), "neutral": (0x8F959E, 0x8C8F96),
    ]

    /// A text / foreground color: `red`, `grey-950`, `blue-500`, `default`.
    static func text(_ name: String?) -> Color? {
        guard let name, !name.isEmpty else { return nil }
        if name == "default" { return nil }
        if name == "white" { return .white }
        if name == "black" { return .primary }
        let parts = name.split(separator: "-")
        let base = String(parts.first ?? "")
        let shade = parts.count > 1 ? Int(parts[1]) ?? 500 : 500
        if base == "grey" || base == "gray" || base == "neutral" {
            if shade >= 800 { return .primary }
            if shade >= 600 { return .secondary }
        }
        guard let c = named[base] else { return nil }
        return Color(light: c.0, dark: c.1)
    }

    /// The template's base hue (card header / tag / summary accent), default blue.
    static func hue(_ name: String?) -> Color {
        guard let name, let c = named[String(name.split(separator: "-").first ?? "")] else { return blue }
        return Color(light: c.0, dark: c.1)
    }

    /// A surface tint: `grey`, `blue-50`, `red-100`, `bg-white`, `default`.
    static func background(_ name: String?) -> Color? {
        guard let name, !name.isEmpty, name != "default", name != "transparent" else { return nil }
        if name == "bg-white" { return cardBackground }
        let base = String(name.split(separator: "-").first ?? "")
        guard named[base] != nil else { return nil }
        let shade = name.split(separator: "-").dropFirst().first.flatMap { Int($0) } ?? 100
        return hue(base).opacity(base == "grey" || base == "gray" ? 0.12 : (shade <= 50 ? 0.08 : 0.13))
    }

    /// `#rrggbb[aa]` or `{lightMode, darkMode}`.
    static func custom(_ v: JSONValue?) -> Color? {
        guard let v else { return nil }
        if let s = v.string { return hex(s).map { Color(uiColor: $0) } }
        guard let light = v["lightMode"]?.string.flatMap(hex) else { return nil }
        let dark = v["darkMode"]?.string.flatMap(hex) ?? light
        return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? dark : light })
    }

    static func hex(_ s: String) -> UIColor? {
        var h = s.trimmingCharacters(in: .whitespaces)
        guard h.hasPrefix("#") else { return nil }
        h.removeFirst()
        if h.count == 3 || h.count == 4 { h = h.map { "\($0)\($0)" }.joined() }
        guard h.count == 6 || h.count == 8, let v = UInt64(h, radix: 16) else { return nil }
        let rgba = h.count == 6 ? (v << 8 | 0xff) : v
        return UIColor(red: CGFloat((rgba >> 24) & 0xff) / 255, green: CGFloat((rgba >> 16) & 0xff) / 255,
                       blue: CGFloat((rgba >> 8) & 0xff) / 255, alpha: CGFloat(rgba & 0xff) / 255)
    }
}

// MARK: - Runs

/// How a run of text looks; nil fields inherit.
nonisolated struct LarkRunStyle: Hashable {
    var bold = false
    var italic = false
    var underline = false
    var dotted = false
    var strike = false
    var code = false
    var color: Color?
    var background: Color?
    var size: CGFloat?
    var link: URL?
}

/// One piece of inline content: styled text, an emoji drawn from the Feishu catalogue, or an SF Symbol glyph.
nonisolated enum LarkInline: Hashable {
    case text(String, LarkRunStyle)
    case emoji(key: String, fallback: String)
    case symbol(String, Color?)
    case image(url: String)
}

/// Base text appearance for a block of inline runs. Sizes are Feishu's, set on Persona's levels (`Theme.Level`: body,
/// secondary, caption, label; 17 / 15 / 13 / 12 on the iPhone, 13 / 12 / 11 / 11 on the Mac); `scale` (`Theme.textScale`, set by `LarkInlineText`) makes them follow the
/// system's text size and Small / Standard / Large like the rest of the app.
nonisolated struct LarkTextBase: Hashable {
    var size: CGFloat = Theme.Level.body.size
    var bold = false
    var color: Color?
    var scale: CGFloat = 1

    /// The size to draw at.
    var points: CGFloat { size * scale }

    func scaled(_ scale: CGFloat) -> LarkTextBase {
        var b = self
        b.scale = scale
        return b
    }
}

extension EnvironmentValues {
    /// Text appearance cards hand down to their nested elements (note = small grey, heading = large bold…).
    @Entry var larkTextBase = LarkTextBase()
}

/// Inline runs drawn as one `Text`, so they wrap together; emoji pictures sit in the line like glyphs.
struct LarkInlineText: View {
    var runs: [LarkInline]
    var base: LarkTextBase? = nil
    var lineLimit: Int? = nil
    var alignment: TextAlignment = .leading
    @Environment(\.larkTextBase) private var inherited
    @Environment(\.larkManager) private var manager
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let b = (base ?? inherited).scaled(Theme.textScale(typeSize))
        let _ = LarkMedia.shared.emojiVersion
        Self.compose(runs, base: b, manager: manager)
            .lineSpacing(b.points * (Theme.Level.body.lineHeight - 1.19)) // the body's line height, at Feishu's size
            .lineLimit(lineLimit)
            .multilineTextAlignment(alignment)
            .fixedSize(horizontal: false, vertical: true)
    }

    /// `manager`: the chat's, whose copies of the pictures in the runs were loaded (`LarkCardRuns.preloadIcons`).
    @MainActor static func compose(_ runs: [LarkInline], base: LarkTextBase, manager: String? = nil) -> Text {
        var out: Text?
        var pending = AttributedString()
        func append(_ t: Text) { out = out.map { Text("\($0)\(t)") } ?? t }
        func flush() {
            guard !pending.characters.isEmpty else { return }
            append(Text(pending))
            pending = AttributedString()
        }
        let emojiPoints = (base.points * 1.25).rounded()
        for run in runs {
            switch run {
            case .text(let s, let style):
                pending += attributed(s, style: style, base: base)
            case .emoji(let key, let fallback):
                if let img = LarkMedia.shared.emojiImage(key, points: emojiPoints, manager: manager) {
                    flush()
                    append(Text(Image(uiImage: img)).baselineOffset(-emojiPoints * 0.2))
                } else {
                    pending += attributed(fallback, style: LarkRunStyle(), base: base)
                }
            case .symbol(let name, let color):
                flush()
                var t = Text(Image(systemName: name)).font(.system(size: base.points * 0.95))
                if let color { t = t.foregroundStyle(color) }
                append(t)
            case .image(let url):
                if let img = LarkMedia.shared.cached(LarkMedia.url(url, manager: manager), size: larkThumbEdge(points: emojiPoints)) {
                    flush()
                    append(Text(Image(uiImage: LarkMedia.fitted(img, points: emojiPoints))).baselineOffset(-emojiPoints * 0.2))
                }
            }
        }
        flush()
        return out ?? Text("")
    }

    @MainActor static func attributed(_ s: String, style: LarkRunStyle, base: LarkTextBase) -> AttributedString {
        var a = AttributedString(style.code ? s.replacingOccurrences(of: " ", with: "\u{00A0}") : s)
        let size = (style.size ?? base.size) * base.scale
        var font: Font = style.code ? .system(size: size * 0.88, design: .monospaced) : .system(size: size)
        if style.bold || base.bold { font = font.weight(.semibold) }
        if style.italic { font = font.italic() }
        a.font = font
        if let link = style.link {
            a.link = link
            a.foregroundColor = style.color ?? LarkColor.blue
        } else if let c = style.color ?? base.color {
            a.foregroundColor = c
        }
        if style.code {
            a.backgroundColor = Color.primary.opacity(0.08)
            if style.color == nil { a.foregroundColor = Color(light: 0xC7254E, dark: 0xF07C9A) }
        }
        if let bg = style.background { a.backgroundColor = bg }
        if style.strike { a.strikethroughStyle = .single }
        if style.underline { a.underlineStyle = .single }
        if style.dotted { a.underlineStyle = Text.LineStyle(pattern: .dot, color: .secondary) }
        return a
    }
}

// MARK: - Layout helpers

/// Wraps children onto as many rows as needed (buttons in an action row, tags, chips).
struct LarkFlowLayout: Layout {
    var spacing: CGFloat = 8
    var lineSpacing: CGFloat = 8
    /// Lines end at the right edge (under my messages, which sit on the right).
    var trailing = false

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, row: CGFloat = 0, widest: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(ProposedViewSize(width: width, height: nil))
            if x > 0 && x + size.width > width { y += row + lineSpacing; x = 0; row = 0 }
            x += size.width + spacing
            row = max(row, size.height)
            widest = max(widest, x - spacing)
        }
        return CGSize(width: proposal.width ?? widest, height: y + row)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        // Lines first (which subviews, how wide), then each line placed from the left or ending at the right.
        var lines: [(items: [(LayoutSubview, CGSize)], width: CGFloat, height: CGFloat)] = []
        var x: CGFloat = 0
        for s in subviews {
            let size = s.sizeThatFits(ProposedViewSize(width: bounds.width, height: nil))
            if lines.isEmpty || (x > 0 && x + size.width > bounds.width) {
                lines.append(([], 0, 0))
                x = 0
            }
            lines[lines.count - 1].items.append((s, size))
            lines[lines.count - 1].width = x + size.width
            lines[lines.count - 1].height = max(lines[lines.count - 1].height, size.height)
            x += size.width + spacing
        }
        var y = bounds.minY
        for line in lines {
            var x = trailing ? bounds.maxX - min(line.width, bounds.width) : bounds.minX
            for (s, size) in line.items {
                s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: min(size.width, bounds.width), height: size.height))
                x += size.width + spacing
            }
            y += line.height + lineSpacing
        }
    }
}

nonisolated enum LarkFormat {
    static func fileSize(_ raw: String?) -> String {
        guard let raw, let n = Int64(raw) else { return raw ?? "" }
        return n.storageText
    }

    static func duration(ms: Double?) -> String {
        guard let ms, ms > 0 else { return "" }
        let s = Int((ms / 1000).rounded())
        return s >= 3600 ? String(format: "%d:%02d:%02d", s / 3600, s / 60 % 60, s % 60) : String(format: "%d:%02d", s / 60, s % 60)
    }

    /// ISO timestamps in summary-card fields read as local times; anything else is shown as is.
    static func fieldValue(_ s: String) -> String {
        guard s.count >= 20, s.contains("T"), let d = JSON.parseDate(s), d.isMeaningful else { return s }
        return d.formatted(.dateTime.month().day().hour().minute())
    }

    /// Decodes the HTML character references Feishu leaves in card markdown text.
    static func entities(_ s: String) -> String {
        guard s.contains("&") else { return s }
        var out = s
        let table = ["&nbsp;": "\u{00A0}", "&ensp;": "\u{2002}", "&emsp;": "\u{2003}", "&lt;": "<", "&gt;": ">", "&quot;": "\"",
                     "&#39;": "'", "&apos;": "'", "&sim;": "~", "&amp;": "&"]
        for (k, v) in table { out = out.replacingOccurrences(of: k, with: v, options: .caseInsensitive) }
        guard out.contains("&#"), let re = try? NSRegularExpression(pattern: "&#(x?)([0-9a-fA-F]{1,7});") else { return out }
        let ns = out as NSString
        var result = ""
        var last = 0
        for m in re.matches(in: out, range: NSRange(location: 0, length: ns.length)) {
            result += ns.substring(with: NSRange(location: last, length: m.range.location - last))
            let hex = ns.substring(with: m.range(at: 1)) == "x"
            let num = UInt32(ns.substring(with: m.range(at: 2)), radix: hex ? 16 : 10)
            result += num.flatMap(Unicode.Scalar.init).map { String(Character($0)) } ?? ns.substring(with: m.range)
            last = m.range.location + m.range.length
        }
        return result + ns.substring(from: last)
    }

    /// Splits card markdown text into runs: `<br>`, `<a href>`, `<font color>` (the markup Feishu leaves in place).
    static func cardMarkdown(_ text: String, style: LarkRunStyle) -> [LarkInline] {
        let pattern = #"<br\s*/?>|<a\s([^<>]*)>([^<]*)</a\s*>|<font\s([^<>]*)>([^<]*)</font\s*>"#
        guard text.contains("<"), let re = try? NSRegularExpression(pattern: pattern, options: .caseInsensitive) else {
            return [.text(entities(text), style)]
        }
        let ns = text as NSString
        var out: [LarkInline] = []
        var last = 0
        func attr(_ attrs: String, _ name: String) -> String? {
            guard let r = try? NSRegularExpression(pattern: "(?:^|\\s)\(name)\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)'|([^\\s\"'>]+))", options: .caseInsensitive),
                  let m = r.firstMatch(in: attrs, range: NSRange(location: 0, length: (attrs as NSString).length)) else { return nil }
            for i in 1...3 where m.range(at: i).location != NSNotFound { return (attrs as NSString).substring(with: m.range(at: i)) }
            return nil
        }
        for m in re.matches(in: text, range: NSRange(location: 0, length: ns.length)) {
            if m.range.location > last { out.append(.text(entities(ns.substring(with: NSRange(location: last, length: m.range.location - last))), style)) }
            if m.range(at: 1).location != NSNotFound {
                var s = style
                let href = attr(ns.substring(with: m.range(at: 1)), "href") ?? ""
                if href.hasPrefix("http://") || href.hasPrefix("https://") { s.link = URL(string: href) }
                out.append(.text(entities(ns.substring(with: m.range(at: 2))), s))
            } else if m.range(at: 3).location != NSNotFound {
                var s = style
                let color = attr(ns.substring(with: m.range(at: 3)), "color") ?? ""
                s.color = LarkColor.text(color) ?? LarkColor.hex(color).map { Color(uiColor: $0) } ?? s.color
                out.append(.text(entities(ns.substring(with: m.range(at: 4))), s))
            } else {
                out.append(.text("\n", style))
            }
            last = m.range.location + m.range.length
        }
        if last < ns.length { out.append(.text(entities(ns.substring(from: last)), style)) }
        return out
    }
}
