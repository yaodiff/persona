import SwiftUI

/// A stopped agent's closeout, in the chat's header above its transcript (#3612, docs/assistant/agent-work.md "Closeout";
/// the web's views/chat/CloseoutCard.tsx): whether it says the work is done, what changed, what it produced, whether it
/// is merged and deployed, what it verified and what it still needs. It is the agent's own claim and the card says
/// so. Beside "merged" and "deployed" stands what the hub found when it looked itself: confirmed (the check, in the
/// positive color), contradicted (the warning mark on the caution chip: color is never the only signal) or not
/// checked (a dash, in secondary words). It is not part of the transcript: the reply that carried the raw block shows
/// without it while this card is there (`ChatLines.hidingCloseout`).
struct CloseoutCard: View {
    var closeout: Closeout
    var artifacts: [WorkArtifact] = []
    /// The most its details may take; nil: as tall as they are. Past it they scroll inside, so a long closeout never
    /// pushes the conversation off the screen (as `WorkflowSection`).
    var maxHeight: CGFloat?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.openURL) private var openURL
    @State private var open: Bool
    @State private var detailsHeight: CGFloat = 0

    /// `startsOpen` false: the details start folded, as when the workflow section is open above the card and the two
    /// would take most of the page.
    init(closeout: Closeout, artifacts: [WorkArtifact] = [], maxHeight: CGFloat? = nil, startsOpen: Bool = true) {
        self.closeout = closeout
        self.artifacts = artifacts
        self.maxHeight = maxHeight
        _open = State(initialValue: startsOpen)
    }

    private var did: [String] { (closeout.did ?? []).filter { !$0.isEmpty } }
    private var merged: Closeout.Claim? { closeout.claim(.merged) }
    private var deployed: Closeout.Claim? { closeout.claim(.deployed) }
    private var verified: String? { closeout.verified.flatMap { $0.isEmpty ? nil : $0 } }
    private var missing: String? { closeout.missing.flatMap { $0.isEmpty ? nil : $0 } }
    private var hasDetails: Bool {
        !did.isEmpty || !artifacts.isEmpty || merged != nil || deployed != nil || verified != nil || missing != nil
    }

    var body: some View {
        if let status = closeout.statusLine {
            VStack(alignment: .leading, spacing: Theme.Space.flush) {
                if hasDetails {
                    // The card's top padding is inside the button: the whole band of the head, at least a finger tall
                    // at every text size, folds the details (DESIGN_SYSTEM.md 6.3).
                    Button {
                        Haptics.tap()
                        withAnimation(reduceMotion ? nil : Theme.Motion.reveal) { open.toggle() }
                    } label: {
                        // Its own height, whatever the row around it offers: a frame with only a minimum takes a smaller
                        // offer (a Form row's first guess in Settings' sample) and the words ran over the card's edge.
                        head(status)
                            .scaledFrame(minHeight: Theme.Size.tapTarget)
                            .frame(minHeight: Theme.Size.tapTarget)
                            .fixedSize(horizontal: false, vertical: true)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(spoken(status))
                    .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed"))
                    .accessibilityIdentifier("closeout.head")
                } else {
                    head(status)
                        .padding(.vertical, Theme.Space.s)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(spoken(status))
                        .accessibilityIdentifier("closeout.head")
                }
                if hasDetails, open {
                    Divider()
                    Group {
                        if let maxHeight, maxHeight > 0 {
                            ScrollView(.vertical) {
                                details.onGeometryChange(for: CGFloat.self) { $0.size.height } action: { detailsHeight = $0 }
                            }
                            .scrollBounceBehavior(.basedOnSize)
                            .frame(height: shownHeight(maxHeight))
                        } else {
                            // No cap (Settings' sample): as tall as they are, laid out at once. A height measured after
                            // layout never reached the Form row around it, and the card ran over its neighbours.
                            details.fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(.vertical, Theme.Space.s)
                }
            }
            .padding(.horizontal, Theme.Space.m)
            // A plain fill as GoalBar's and the workflow card's: the folded header fades the open one out, and glass
            // does not fade with it.
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier(closeout.contradicted ? "closeout.card.contradicted" : "closeout.card")
        }
    }

    /// The details' height up to the cap; nil until measured.
    private func shownHeight(_ maxHeight: CGFloat) -> CGFloat? {
        detailsHeight > 0 ? min(detailsHeight, maxHeight) : nil
    }

    private func spoken(_ status: String) -> String {
        [String(localized: "Closeout"), status, String(localized: "The agent's own report")]
            .joined(separator: String(localized: ", ", comment: "List separator"))
    }

    // MARK: Head

    private var mark: (symbol: String, color: Color) {
        if closeout.contradicted { return (Theme.Icon.error, Theme.cautionText) }
        switch closeout.known {
        case .done?: return (Theme.Icon.done, Theme.positive)
        case .blocked?: return (Theme.Icon.time, .secondary)
        case .notDone?, nil: return (Theme.Icon.info, .secondary)
        }
    }

    private func head(_ status: String) -> some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(mark.symbol)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(mark.color)
                .scaledFrame(width: Theme.Size.markM)
                .accessibilityHidden(true)
            // The status, "The agent's own report" under it: one layout at every width and text size. A `ViewThatFits`
            // choosing between beside and under measured a Form row (Settings' sample) with one and drew the other, so
            // the head ran over what is around it.
            VStack(alignment: .leading, spacing: Theme.Space.flush) {
                Text(status).font(Theme.Font.secondaryStrong).foregroundStyle(.primary).lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
                ownReport.lineLimit(2).fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if hasDetails {
                ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand)
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
            }
        }
    }

    private var ownReport: Text {
        Text("The agent's own report").font(Theme.Font.caption).foregroundStyle(.secondary)
    }

    // MARK: Details

    @ViewBuilder private var details: some View {
        // On the value's first line: a link artifact is a tap target taller than its words, and a label at its
        // top stood above them.
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: Theme.Space.m, verticalSpacing: Theme.Space.sm) {
            ForEach(fields) { field in
                GridRow {
                    label(field.title).gridColumnAlignment(.leading)
                    value(field).frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private enum FieldKind {
        case did, artifacts, merged, deployed, verified, missing
    }

    private struct Field: Identifiable {
        var kind: FieldKind
        var title: String
        var id: String { title }
    }

    private var fields: [Field] {
        var out: [Field] = []
        if !did.isEmpty { out.append(Field(kind: .did, title: String(localized: "What changed"))) }
        if !artifacts.isEmpty { out.append(Field(kind: .artifacts, title: String(localized: "Artifacts"))) }
        if merged != nil { out.append(Field(kind: .merged, title: String(localized: "Merged [Closeout]"))) }
        if deployed != nil { out.append(Field(kind: .deployed, title: String(localized: "Deployed [Closeout]"))) }
        if verified != nil { out.append(Field(kind: .verified, title: String(localized: "Verified [Closeout]"))) }
        if missing != nil { out.append(Field(kind: .missing, title: String(localized: "Still needs"))) }
        return out
    }

    private func label(_ title: String) -> some View {
        Text(title)
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .fixedSize()
    }

    @ViewBuilder private func value(_ field: Field) -> some View {
        switch field.kind {
        case .did:
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                ForEach(Array(did.enumerated()), id: \.offset) { _, line in words(line) }
            }
            .accessibilityIdentifier("closeout.did")
        case .artifacts:
            VStack(alignment: .leading, spacing: Theme.Space.flush) {
                ForEach(Array(artifacts.enumerated()), id: \.offset) { _, artifact in artifactRow(artifact) }
            }
            .accessibilityIdentifier("closeout.artifacts")
        case .merged:
            if let merged { ClaimLine(subject: .merged, claim: merged) }
        case .deployed:
            if let deployed { ClaimLine(subject: .deployed, claim: deployed) }
        case .verified:
            words(verified ?? "").accessibilityIdentifier("closeout.verified")
        case .missing:
            words(missing ?? "").accessibilityIdentifier("closeout.missing")
        }
    }

    private func words(_ text: String) -> some View {
        Text(text)
            .font(Theme.Font.caption)
            .foregroundStyle(.primary)
            .fixedSize(horizontal: false, vertical: true)
            .textSelection(.enabled)
    }

    private static func symbol(_ kind: String) -> String {
        switch kind {
        case "pr": Theme.Icon.pullRequest
        case "commit": Theme.Icon.commit
        case "branch": Theme.Icon.gitBranch
        case "doc": Theme.Icon.document
        case "build": Theme.Icon.build
        default: Theme.Icon.link
        }
    }

    /// A link opens in the chat's in-app link sheet (the page's `openURL`); anything else is a reference to read.
    @ViewBuilder private func artifactRow(_ artifact: WorkArtifact) -> some View {
        if let url = artifact.link {
            Button {
                openURL(url)
            } label: {
                Label {
                    Text(artifact.label).lineLimit(1).truncationMode(.middle)
                } icon: {
                    ThemedIcon(Self.symbol(artifact.kind)).font(Theme.Font.labelStrong)
                }
                .labelStyle(.titleAndIcon)
                .font(Theme.Font.captionMedium)
                .foregroundStyle(Theme.accent)
                .scaledFrame(minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(String(localized: "Open \(artifact.label)"))
        } else {
            Label {
                Text(artifact.label).font(Theme.Font.code).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            } icon: {
                ThemedIcon(Self.symbol(artifact.kind)).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            }
            .labelStyle(.titleAndIcon)
            .foregroundStyle(.primary)
            .padding(.vertical, Theme.Space.xs)
        }
    }
}

/// What the agent said, then what the hub found: three states that differ in words, mark and color.
private struct ClaimLine: View {
    var subject: Closeout.Subject
    var claim: Closeout.Claim

    private var said: String {
        switch claim.said {
        case true?: String(localized: "Says yes")
        case false?: String(localized: "Says no")
        case nil: String(localized: "Not said")
        }
    }

    /// The hub's finding in words; nil when it did not look.
    private var found: String? {
        guard let found = claim.found else { return nil }
        switch (subject, found) {
        case (.merged, true): return String(localized: "Hub found it merged")
        case (.merged, false): return String(localized: "Hub found it isn't merged")
        case (.deployed, true): return String(localized: "Hub found it deployed")
        case (.deployed, false): return String(localized: "Hub found it isn't deployed")
        }
    }

    private var state: String {
        switch claim.state {
        case .confirmed: "confirmed"
        case .contradicted: "contradicted"
        case .unchecked: "unchecked"
        case .found: "found"
        }
    }

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) { parts }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) { parts }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier(subject == .merged ? "closeout.merged.\(state)" : "closeout.deployed.\(state)")
    }

    @ViewBuilder private var parts: some View {
        Text(said).font(Theme.Font.caption).foregroundStyle(.primary).lineLimit(1)
        switch claim.state {
        case .confirmed:
            finding(String(localized: "Hub confirmed"), symbol: Theme.Icon.done, color: Theme.positive)
        case .contradicted:
            Chip(text: found ?? "", symbol: Theme.Icon.error, look: .review(.caution), shrinks: true)
        case .found:
            Text(found ?? "").font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
        case .unchecked:
            finding(String(localized: "Not checked by the hub"), symbol: Theme.Icon.notChecked, color: .secondary)
        }
    }

    private func finding(_ text: String, symbol: String, color: Color) -> some View {
        Label {
            Text(text).lineLimit(1)
        } icon: {
            ThemedIcon(symbol).font(Theme.Font.labelStrong)
        }
        .labelStyle(.titleAndIcon)
        .font(Theme.Font.caption)
        .foregroundStyle(color)
    }
}
