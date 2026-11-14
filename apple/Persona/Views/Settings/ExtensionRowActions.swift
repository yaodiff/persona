import SwiftUI
import PersonaWire

// UI version 6's row actions (docs/extensions/extensions.md "UI version 6", #827): a row's or a tile's `actions`, the
// platform's way. A long press (a right click on the Mac) opens their menu; the `swipe` ones are also under a swipe
// from the trailing edge in a list; VoiceOver has them as the row's actions. Destructive ones are red and last; a full
// swipe never runs one (a tap does); one with `confirm` asks first, with an explicit Cancel (an alert: a
// confirmation dialog shown from a row loses its Cancel, docs/lessons/ios.md). Under a swipe a destructive one is red
// by its tint, not its role: the list takes that role as the row's deletion and closes the row (and the page with its
// question). They are shortcuts: the page the row opens can do the same.

extension View {
    /// The extension's extra actions on this row or tile; nothing when it has none or the page cannot use them (a card).
    func extensionRowActions(_ actions: [ExtensionRowAction]?, model: (any ExtensionViewControlling)?,
                             link: Binding<LinkTarget?>) -> some View {
        modifier(ExtensionRowActions(actions: actions ?? [], model: model, link: link))
    }
}

private struct ExtensionRowActions: ViewModifier {
    var actions: [ExtensionRowAction]
    var model: (any ExtensionViewControlling)?
    @Binding var link: LinkTarget?
    @Environment(\.extensionDisplayedRevision) private var displayed
    @Environment(AppModel.self) private var app
    /// The action asking first: kept apart from the alert's flag, which the alert clears before its button runs.
    @State private var asking: ExtensionRowAction?
    @State private var confirming = false

    /// The usual ones first, destructive ones last.
    private var ordered: [ExtensionRowAction] {
        actions.filter { !Self.destructive($0) } + actions.filter(Self.destructive)
    }

    private static func destructive(_ a: ExtensionRowAction) -> Bool { a.role == "destructive" }

    func body(content: Content) -> some View {
        if actions.isEmpty || model == nil {
            content
        } else {
            content
                .contextMenu { ForEach(Array(ordered.enumerated()), id: \.offset) { _, a in button(a) } }
                .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                    // The first sits at the edge: the destructive one there, as Mail puts Delete.
                    ForEach(Array(ordered.filter { $0.swipe == true }.reversed().enumerated()), id: \.offset) { _, a in
                        button(a, role: false).tint(Self.destructive(a) ? Theme.danger : Theme.Hue.gray)
                    }
                }
                .accessibilityActions {
                    ForEach(Array(ordered.enumerated()), id: \.offset) { _, a in Button(a.title) { start(a) } }
                }
                .alert(asking?.action.confirm ?? "", isPresented: $confirming) {
                    if let a = asking {
                        Button(role: Self.destructive(a) ? .destructive : nil) { run(a) } label: { Text(a.title).lineLimit(1) }
                            .accessibilityIdentifier("plugin-ui-confirm")
                    }
                    Button(role: .cancel) {} label: { Text("Cancel").lineLimit(1) }
                        .accessibilityIdentifier("plugin-ui-confirm-cancel")
                }
        }
    }

    /// `role: false` under a swipe (see the top of this file).
    private func button(_ a: ExtensionRowAction, role: Bool = true) -> some View {
        Button(role: role && Self.destructive(a) ? .destructive : nil) { start(a) } label: {
            if let icon = a.icon, !icon.isEmpty {
                Label(a.title, systemImage: Theme.Icon.styled(ExtensionUIStyle.icon(icon)))
            } else {
                Text(a.title)
            }
        }
        .disabled(model?.isEnabled(a.action) != true)
        .accessibilityIdentifier("plugin-ui-row-action-\(a.action.id)")
    }

    private func start(_ a: ExtensionRowAction) {
        if a.action.confirm?.isEmpty == false, model?.managesConfirmation != true {
            asking = a
            confirming = true
        } else {
            run(a)
        }
    }

    private func run(_ a: ExtensionRowAction) {
        guard let model else { return }
        let execute = model.prepared(a.action, with: [:], displayed: displayed)
        Task { ExtensionOutcome.show(await execute(), app: app, link: $link) }
    }
}
