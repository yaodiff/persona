import SwiftUI

// An extension's View drawn into a picture (`extensionPicture`, #7261: Share as Image of a reply with a card). A picture
// has nothing to use, and `ImageRenderer` draws neither scroll views nor UIKit controls (a progress bar, a switch, a
// segmented control, a text field come out as a placeholder), so these draw what the controls look like in the chat
// with shapes, words and the style's icons. Only `ExtensionItemRow` uses them, and only in a picture.

/// A progress bar as `ProgressView(value:)` draws it: a thin track with its filled part in the bar's color.
struct ExtensionPictureBar: View {
    var ratio: Double
    var tint: Color

    var body: some View {
        Capsule()
            .fill(Theme.Fill.chip)
            .frame(height: Theme.Size.barWide)
            .overlay(alignment: .leading) {
                GeometryReader { g in
                    Capsule().fill(tint).frame(width: g.size.width * min(max(ratio, 0), 1))
                }
            }
            .accessibilityHidden(true)
    }
}

/// A button as a card draws it (a plain button: its title in the accent, red when it deletes), on one line.
struct ExtensionPictureButton: View {
    var title: String
    var destructive: Bool

    var body: some View {
        Text(title)
            .font(Theme.Font.body)
            .foregroundStyle(destructive ? Theme.danger : Theme.accent)
            .lineLimit(1)
            .minimumScaleFactor(0.8)
    }
}

/// A switch's state in a picture: a ticked or an empty box in the style's icons.
struct ExtensionPictureSwitch: View {
    var on: Bool

    var body: some View {
        ThemedIcon(on ? Theme.Icon.checkedBox : Theme.Icon.uncheckedBox)
            .font(Theme.Font.heading)
            .foregroundStyle(on ? Theme.accent : Color.secondary)
    }
}

/// A row's control in a picture (`ExtensionControlRow` in the chat): a switch's state, a slider's or a stepper's value.
struct ExtensionPictureControl: View {
    var control: ExtensionRowControl

    var body: some View {
        if control.kind == ExtensionRowControlKind.switch {
            ExtensionPictureSwitch(on: control.on == true)
        } else {
            Text(control.written(control.value ?? control.min ?? 0))
                .font(Theme.Font.body.monospacedDigit())
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }
}

/// A field in a picture: its label, then what it holds (a choice's name, a switch's state), else its placeholder;
/// its help under it. A secret never shows its value.
struct ExtensionPictureField: View {
    var field: ExtensionFieldComponent

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.m) {
                Text(field.label.isEmpty ? field.name : field.label).font(Theme.Font.body).lineLimit(2)
                Spacer(minLength: Theme.Space.s)
                if field.kind == "toggle" {
                    ExtensionPictureSwitch(on: field.value == "true")
                } else if let shown = Self.shown(field) {
                    Text(shown.text)
                        .font(Theme.Font.body)
                        .foregroundStyle(shown.filled ? .secondary : .tertiary)
                        .lineLimit(2)
                        .multilineTextAlignment(.trailing)
                }
            }
            if let d = field.detail, !d.isEmpty {
                Text(d).font(Theme.Font.label).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    /// What the field shows: its value (the names of the choices it holds), else its placeholder; nil for neither.
    static func shown(_ field: ExtensionFieldComponent) -> (text: String, filled: Bool)? {
        let value = field.kind == "secret" ? "" : (field.value ?? "")
        if !value.isEmpty {
            guard let choices = field.choices, !choices.isEmpty else { return (value, true) }
            // A multichoice holds a JSON array of the chosen values (docs/extensions/extensions.md, `FieldKind`).
            let picked = field.kind == "multichoice"
                ? ((try? JSONDecoder().decode([String].self, from: Data(value.utf8))) ?? [value]) : [value]
            let names = picked.map { v in choices.first { $0.value == v }?.label ?? v }
            return (names.joined(separator: ", "), true)
        }
        if let p = field.placeholder, !p.isEmpty { return (p, false) }
        return nil
    }
}

/// A segmented choice in a picture: its choices side by side in a capsule, the picked one raised.
struct ExtensionPictureSegmented: View {
    var item: ExtensionSegmentedComponent

    var body: some View {
        let choices = Array(item.choices.prefix(5))
        let picked = item.value.isEmpty ? choices.first?.value : item.value
        if choices.count >= 2 {
            HStack(spacing: Theme.Space.xxs) {
                ForEach(choices) { c in
                    Text(c.label)
                        .font(Theme.Font.label)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, Theme.Space.xs)
                        .background(c.value == picked ? Theme.Fill.pressed : Color.clear, in: Capsule())
                }
            }
            .padding(Theme.Space.xxs)
            .background(Theme.Fill.chip, in: Capsule())
        }
    }
}
