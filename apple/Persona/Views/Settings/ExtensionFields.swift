import SwiftUI
import PersonaWire

// UI version 6's new fields (docs/extensions/extensions.md "UI version 6", #827): a date, a time or both with the
// system's picker, held to `earliest`…`latest`; several choices in a menu of toggles that stays open while picking.
// Values as the wire writes them (`ExtensionFieldValues`).

/// A date, a time, or a date and a time. Empty, it says so and a tap sets it (to now, held to the range) rather than
/// showing a date nobody chose; set, it can be cleared.
struct ExtensionDateField: View {
    var field: ExtensionFieldComponent
    var label: String
    @Binding var value: String

    private var kind: String { field.kind }
    private var range: (from: Date?, to: Date?) {
        ExtensionFieldValues.range(earliest: field.earliest, latest: field.latest, kind: kind)
    }
    private var components: DatePickerComponents {
        switch kind {
        case "date": .date
        case "time": .hourAndMinute
        default: [.date, .hourAndMinute]
        }
    }

    var body: some View {
        if let d = ExtensionFieldValues.date(value, kind: kind) {
            HStack(spacing: Theme.Space.s) {
                picker(Binding(get: { d }, set: { value = ExtensionFieldValues.text($0, kind: kind) }))
                Button { value = "" } label: {
                    ThemedIcon(Theme.Icon.clear).foregroundStyle(.secondary).scaledFrame(width: Theme.Size.wellS, height: Theme.Size.wellS)
                        .contentShape(.rect)
                }
                .buttonStyle(.borderless)
                .accessibilityLabel(String(localized: "Clear \(label)"))
            }
        } else {
            LabeledContent {
                Button {
                    value = ExtensionFieldValues.text(ExtensionFieldValues.clamp(.now, range), kind: kind)
                } label: {
                    Text(field.placeholder?.isEmpty == false ? field.placeholder! : String(localized: "Not Set")).lineLimit(1)
                }
                .buttonStyle(.borderless)
            } label: {
                Text(label).lineLimit(2)
            }
        }
    }

    @ViewBuilder private func picker(_ selection: Binding<Date>) -> some View {
        let r = range
        switch (r.from, r.to) {
        case let (from?, to?): DatePicker(selection: selection, in: from...to, displayedComponents: components) { Text(label).lineLimit(2) }
        case let (from?, nil): DatePicker(selection: selection, in: from..., displayedComponents: components) { Text(label).lineLimit(2) }
        case let (nil, to?): DatePicker(selection: selection, in: ...to, displayedComponents: components) { Text(label).lineLimit(2) }
        case (nil, nil): DatePicker(selection: selection, displayedComponents: components) { Text(label).lineLimit(2) }
        }
    }
}

/// Several of a field's choices: a menu of toggles that stays open while picking, the chosen ones named beside the
/// label (in the choices' order).
struct ExtensionMultiChoiceField: View {
    var field: ExtensionFieldComponent
    var label: String
    @Binding var value: String

    private var choices: [ExtensionChoice] { field.choices ?? [] }
    private var picked: Set<String> { Set(ExtensionFieldValues.chosen(value)) }

    var body: some View {
        LabeledContent {
            Menu {
                ForEach(choices) { c in
                    Toggle(isOn: Binding(get: { picked.contains(c.value) }, set: { on in
                        var p = picked
                        if on { p.insert(c.value) } else { p.remove(c.value) }
                        value = ExtensionFieldValues.multichoice(p, order: choices.map(\.value))
                    })) { Text(c.label) }
                }
            } label: {
                Text(summary).lineLimit(1).truncationMode(.tail)
            }
            #if !os(macOS) // the frozen Mac app's menu closes after each pick: macOS has no `.disabled` here
            .menuActionDismissBehavior(.disabled)
            #endif
        } label: {
            Text(label).lineLimit(2)
        }
        .accessibilityValue(summary)
    }

    private var summary: String {
        let names = choices.filter { picked.contains($0.value) }.map(\.label)
        return names.isEmpty ? (field.placeholder?.isEmpty == false ? field.placeholder! : String(localized: "None [Extension Components 3005ea]"))
            : names.formatted(.list(type: .and, width: .narrow))
    }
}
