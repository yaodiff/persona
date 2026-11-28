import SwiftUI
import PersonaWire

/// A personal Agent's allowance at its service as the hub passes it on (`quota`, docs/extensions/lark-migration.md
/// "Personal Agents in Messages"); every field optional, so a source that sends more or less still decodes.
nonisolated struct PersonalAgentQuota: Codable, Hashable, Sendable {
    var state: String?
    /// What is left as a fraction (0–1).
    var remaining: Double?
    /// What is left as a percentage (0–100).
    var percent: Double?
    var tone: String?
    var kind: String?
    var label: String?
    var resetsAt: JSONValue?
    var stale: Bool?
    /// The source's own "show it" flag. Not read: older accounts say false while they have a real figure.
    var show: Bool?

    private enum Keys: String, CodingKey { case state, remaining, percent, tone, kind, label, resetsAt, stale, show }

    init(state: String? = nil, remaining: Double? = nil, percent: Double? = nil, resetsAt: JSONValue? = nil, stale: Bool? = nil, show: Bool? = nil) {
        self.state = state; self.remaining = remaining; self.percent = percent; self.resetsAt = resetsAt; self.stale = stale; self.show = show
    }

    /// Field by field: one a source sends in another shape is left out, and never takes the session list down with it.
    init(from decoder: Decoder) throws {
        guard let c = try? decoder.container(keyedBy: Keys.self) else { return }
        state = try? c.decode(String.self, forKey: .state)
        remaining = try? c.decode(Double.self, forKey: .remaining)
        percent = try? c.decode(Double.self, forKey: .percent)
        tone = try? c.decode(String.self, forKey: .tone)
        kind = try? c.decode(String.self, forKey: .kind)
        label = try? c.decode(String.self, forKey: .label)
        resetsAt = try? c.decode(JSONValue.self, forKey: .resetsAt)
        stale = try? c.decode(Bool.self, forKey: .stale)
        show = try? c.decode(Bool.self, forKey: .show)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: Keys.self)
        try c.encodeIfPresent(state, forKey: .state)
        try c.encodeIfPresent(remaining, forKey: .remaining)
        try c.encodeIfPresent(percent, forKey: .percent)
        try c.encodeIfPresent(tone, forKey: .tone)
        try c.encodeIfPresent(kind, forKey: .kind)
        try c.encodeIfPresent(label, forKey: .label)
        try c.encodeIfPresent(resetsAt, forKey: .resetsAt)
        try c.encodeIfPresent(stale, forKey: .stale)
        try c.encodeIfPresent(show, forKey: .show)
    }
}

/// The charge to draw: 0–1, the whole percentage, and its tone.
nonisolated struct PersonalAgentCharge: Hashable, Sendable {
    enum Tone: String, Hashable, Sendable { case ok, low, exhausted, stale }
    var level: Double
    var percent: Int
    var tone: Tone
    var resetsAt: Date?

    /// Nil when there is no figure to draw (no quota; neither remaining nor percent, and not exhausted). `show == false`
    /// is not a reason to hide it.
    init?(_ q: PersonalAgentQuota?) {
        guard let q else { return nil }
        let exhausted = q.state == "exhausted"
        var level: Double
        if let p = q.percent, p.isFinite { level = p / 100 }
        else if let r = q.remaining, r.isFinite { level = r > 1 ? r / 100 : r }
        else if exhausted { level = 0 }
        else { return nil }
        level = exhausted ? 0 : min(1, max(0, level))
        self.level = level
        percent = Int((level * 100).rounded())
        tone = q.stale == true || q.state == "stale" ? .stale : level <= 0 ? .exhausted : (level <= 0.2 || q.state == "low") ? .low : .ok
        switch q.resetsAt {
        case .number(let ms) where ms > 0: resetsAt = Date(timeIntervalSince1970: ms / 1000)
        case .string(let s): resetsAt = ISO8601DateFormatter().date(from: s)
        default: resetsAt = nil
        }
    }

    var color: Color {
        switch tone {
        case .ok: Theme.Hue.green
        case .low: Theme.Hue.amber
        case .exhausted: Theme.Hue.red
        case .stale: Theme.Hue.gray
        }
    }

    /// "70% left", with when it resets when the source says.
    var description: String {
        var parts = [tone == .exhausted ? String(localized: "Allowance used up", comment: "A personal Agent's allowance at its service is used up")
                                        : String(localized: "\(percent)% left", comment: "What is left of a personal Agent's allowance at its service")]
        if tone == .stale { parts.append(String(localized: "Not current", comment: "A personal Agent's allowance figure is an old reading")) }
        if let resetsAt { parts.append(String(localized: "Resets \(Fmt.relative(resetsAt))", comment: "When a personal Agent's allowance resets")) }
        return parts.joined(separator: " · ")
    }
}

extension PersonalAgentSession {
    var charge: PersonalAgentCharge? { PersonalAgentCharge(quota) }
}

/// The charge as a ring around an avatar of `size`, filled clockwise from the top, drawn outside the avatar's frame so
/// the layout does not move. Nothing without a figure.
struct PersonalAgentChargeRing: ViewModifier {
    let charge: PersonalAgentCharge?
    let size: CGFloat

    func body(content: Content) -> some View {
        content.overlay {
            if let charge {
                let stroke = Theme.Space.xxs
                ZStack {
                    Circle().stroke(Theme.Fill.well, lineWidth: stroke)
                    Circle().trim(from: 0, to: charge.level)
                        .stroke(charge.color, style: StrokeStyle(lineWidth: stroke, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                }
                .frame(width: size + Theme.Space.sm, height: size + Theme.Space.sm)
                .opacity(charge.tone == .stale ? 0.6 : 1)
                .accessibilityHidden(true)
                .allowsHitTesting(false)
            }
        }
    }
}

/// A small battery filled to the charge and its percentage; read out as "70% left".
struct PersonalAgentBattery: View {
    let charge: PersonalAgentCharge
    @ScaledMetric private var width = Theme.Space.l
    @ScaledMetric private var height = Theme.Space.s

    var body: some View {
        HStack(spacing: Theme.Space.xxs) {
            ZStack(alignment: .leading) {
                RoundedRectangle(cornerRadius: Theme.Radius.thumbnail).stroke(Color.secondary.opacity(0.55), lineWidth: 1)
                RoundedRectangle(cornerRadius: Theme.Radius.thumbnail).fill(charge.color)
                    .frame(width: max(1, (width - Theme.Space.xs) * charge.level))
                    .padding(Theme.Space.xxs)
                    .opacity(charge.level > 0 ? 1 : 0)
            }
            .frame(width: width, height: height)
            Text("\(charge.percent)%").font(Theme.Font.caption).monospacedDigit()
                .foregroundStyle(charge.tone == .ok ? Color.secondary : charge.color).lineLimit(1)
        }
        .opacity(charge.tone == .stale ? 0.6 : 1)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(charge.description)
    }
}
