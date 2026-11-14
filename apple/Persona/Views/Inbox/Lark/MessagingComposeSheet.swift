import SwiftUI
import PersonaWire

/// What a messaging source asks for before a mode's send (#2138, docs/extensions/extension-messaging.md「Composer modes」): its view's text and
/// fields (a word, a secret, a choice, a switch), and the submit button that takes their values back to `compose`.
/// Nothing else of the view goes back: buttons' ids and rows' actions are not the source's to read here.
struct MessagingComposeSheet: View {
    var view: ExtensionView
    var working: Bool
    var submit: ([String: String]) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var values: [String: String] = [:]

    var body: some View {
        NavigationStack {
            Form {
                ForEach(Array(view.sections.enumerated()), id: \.offset) { _, section in
                    Section {
                        ForEach(Array(section.items.enumerated()), id: \.offset) { _, item in row(item) }
                    } header: {
                        if let t = section.title, !t.isEmpty { Text(t) }
                    } footer: {
                        if let f = section.footer, !f.isEmpty { Text(f) }
                    }
                }
            }
            .themedGroupedPage()
            .navigationTitle(view.title ?? String(localized: "Continue Sending"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() }.lineLimit(1) } }
            .onAppear(perform: seed)
            .onChange(of: view) { _, _ in seed() }
        }
    }

    /// The fields' starting values: what the source put in them (never a secret's). Only this view's fields: what
    /// an earlier one asked (a password) does not go back with the next.
    private func seed() {
        values = [:]
        for s in view.sections {
            for case .field(let f) in s.items {
                values[f.name] = f.kind == "secret" ? "" : (f.value ?? f.choices?.first?.value ?? "")
            }
        }
    }

    @ViewBuilder private func row(_ item: ExtensionComponent) -> some View {
        switch item {
        case .text(let t):
            Text(t.text).font(t.style == "secondary" || t.style == "caption" ? Theme.Font.secondary : Theme.Font.body)
                .foregroundStyle(t.style == "secondary" || t.style == "caption" ? .secondary : .primary)
        case .markdown(let m):
            Text(m.text).font(Theme.Font.body)
        case .field(let f):
            let binding = Binding(get: { values[f.name] ?? "" }, set: { values[f.name] = $0 })
            switch f.kind {
            case "secret":
                SecretField(f.label, text: binding, identifier: "messaging.compose." + f.name)
            case "choice":
                Picker(f.label, selection: binding) {
                    ForEach(f.choices ?? [], id: \.value) { c in Text(c.title ?? c.value).tag(c.value) }
                }
                .accessibilityIdentifier("messaging.compose." + f.name)
            case "toggle":
                Toggle(f.label, isOn: Binding(get: { values[f.name] == "true" }, set: { values[f.name] = $0 ? "true" : "false" }))
            default:
                TextField(f.label, text: binding, prompt: f.placeholder.map { Text($0) })
                    .accessibilityIdentifier("messaging.compose." + f.name)
            }
        case .button(let b):
            if b.action.submit == true {
                Button {
                    submit(values)
                    // A secret goes once: a wrong one is typed again, a right one is not kept here.
                    for s in view.sections { for case .field(let f) in s.items where f.kind == "secret" { values[f.name] = "" } }
                } label: {
                    HStack {
                        Text(b.title).lineLimit(1)
                        if working { Spacer(); ProgressView().controlSize(.small) }
                    }
                }
                .disabled(working)
                .accessibilityIdentifier("messaging.compose.submit")
            }
        default:
            EmptyView()
        }
    }
}
