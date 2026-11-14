import SwiftUI
import PersonaWire

/// Whether `text` needs more than `lines` lines at the width it is drawn at: two copies of it, one held to `lines` and one
/// not, measured out of sight (they are a background, never part of the layout). Drives the Show All a clamp needs.
/// Several texts on one card each report under their own `id` into `cuts`, so a short one cannot hide that a long one is cut.
struct ClampMeasure: View {
    var text: AttributedString
    var lines: Int
    var font: Font
    var id: Int = 0
    @Binding var cuts: Set<Int>
    @State private var held: CGFloat = 0
    @State private var natural: CGFloat = 0

    var body: some View {
        ZStack {
            Text(text).lineLimit(lines).fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { held = $0; update() }
            Text(text).fixedSize(horizontal: false, vertical: true)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { natural = $0; update() }
        }
        .font(font)
        .hidden()
        .accessibilityHidden(true)
    }

    private func update() {
        let now = natural > held + 1
        if now != cuts.contains(id) { if now { cuts.insert(id) } else { cuts.remove(id) } }
    }
}

/// A plan task's brief on the plan card, before Start confirms it (docs/assistant/unified-entry.md "Briefs"; the web's
/// `PlanBrief`). Folded it is who asked (two lines) and one line that counts the rest, so the card's own buttons stay in
/// reach; Show All, its one control, opens the whole request and the goal, the criteria, the expected time and what is out
/// of bounds in place.
struct PlanBriefView: View {
    var brief: TaskBrief
    var title: String
    @State private var open = false
    @State private var cuts: Set<Int> = []

    var body: some View {
        let summary = brief.summary
        let more = !cuts.isEmpty || open || brief.hasMore(title: title)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            asked
            if open { fields }
            if more {
                HStack(spacing: Theme.Space.s) {
                    if !open, !summary.isEmpty {
                        Text(summary).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(2)
                            .accessibilityIdentifier("plan.brief.summary")
                    }
                    Spacer(minLength: 0)
                    Button {
                        withAnimation(Theme.Motion.state) { open.toggle() }
                    } label: {
                        Text(open ? String(localized: "Show Less") : String(localized: "Show All")).lineLimit(1).fixedSize()
                    }
                    .font(Theme.Font.captionStrong)
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
                    .tapTargetHeight()
                    .accessibilityIdentifier("plan.brief.toggle")
                }
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityIdentifier("plan.brief")
    }

    @ViewBuilder private var asked: some View {
        switch brief.asked {
        case .owner(let text, let truncated, let at):
            let prefix = String(localized: "You asked on \(Self.date(at)):") + " "
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                row(icon: Theme.Icon.quote, prefix: prefix, text: text, tone: .primary)
                if truncated {
                    Text("Cut short here. The whole message is in its conversation.").font(Theme.Font.caption).foregroundStyle(.tertiary)
                }
            }
        case .reconstructed:
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                row(icon: Theme.Icon.info, prefix: "", text: String(localized: "Reconstructed: the original request was not recorded"), tone: .secondary)
                if let handover = brief.handover?.trimmingCharacters(in: .whitespacesAndNewlines), !handover.isEmpty {
                    Text("Hand-over Prompt").font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
                    clamped(AttributedString(handover), tone: .secondary, id: 1)
                }
            }
        case .started(let by, let why, let at):
            let date = Self.date(at)
            let line = switch by {
            case "leader": String(localized: "Started by a leader conversation on \(date)")
            case "routine": String(localized: "Started by a routine on \(date)")
            case "hub": String(localized: "Started by Persona itself on \(date)")
            default: String(localized: "Started by the assistant on \(date)")
            }
            row(icon: Theme.Icon.ai, prefix: "", text: why.isEmpty ? line : "\(line): \(why)", tone: .secondary)
        case .unknown(let at):
            row(icon: Theme.Icon.info, prefix: "", text: String(localized: "Started on \(Self.date(at)). Who asked for it was not recorded."), tone: .secondary)
        }
    }

    /// An icon, then words held to two lines until Show All.
    private func row(icon: String, prefix: String, text: String, tone: HierarchicalShapeStyle) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.s) {
            ThemedIcon(icon).font(Theme.Font.label).foregroundStyle(.tertiary)
            clamped(Self.joined(prefix, text), tone: tone)
        }
    }

    /// `prefix` in the secondary tone, then `text`.
    private static func joined(_ prefix: String, _ text: String) -> AttributedString {
        var lead = AttributedString(prefix)
        lead.foregroundColor = .secondary
        return lead + AttributedString(text)
    }

    private func clamped(_ text: AttributedString, tone: HierarchicalShapeStyle, id: Int = 0) -> some View {
        Text(text).font(Theme.Font.caption).foregroundStyle(tone).lineLimit(open ? nil : 2)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background { ClampMeasure(text: text, lines: 2, font: Theme.Font.caption, id: id, cuts: $cuts) }
            .textSelection(.enabled)
    }

    @ViewBuilder private var fields: some View {
        let goal = brief.goalText
        let criteria = brief.criteriaNow
        let minutes = brief.expect
        let bounds = brief.bounds
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if !goal.isEmpty, goal != title.trimmingCharacters(in: .whitespacesAndNewlines) { field(String(localized: "Goal"), [goal]) }
            if !criteria.isEmpty { field(String(localized: "Done When"), criteria) }
            if minutes > 0 {
                field(String(localized: "Expected Time"), [minutes < 120 ? String(localized: "About \(minutes) min") : String(localized: "About \(TaskBrief.hoursText(minutes)) h")])
            }
            if !bounds.isEmpty { field(String(localized: "Out of Bounds"), bounds) }
        }
    }

    private func field(_ label: String, _ items: [String]) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(label).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
            ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                Text(items.count > 1 ? "• " + item : item).font(Theme.Font.caption).foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .textSelection(.enabled)
    }

    private static func date(_ at: Date?) -> String {
        guard let at, at.isMeaningful else { return "" }
        return at.formatted(date: .abbreviated, time: .omitted)
    }
}
