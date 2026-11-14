import SwiftUI
import PersonaWire

struct ExtensionReviewBar: View {
    var review: ExtensionReview
    @Bindable var model: ExtensionViewModel
    @Binding var link: LinkTarget?
    #if os(macOS)
    @State private var expanded = true
    #else
    @State private var expanded = false
    #endif

    private var text: Binding<String> {
        Binding(get: { model.values[review.name] ?? "" }, set: { model.values[review.name] = $0 })
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.s) {
                Button { withAnimation(Theme.Motion.layout) { expanded.toggle() } } label: {
                    Text("Review drafts · \(review.count)").lineLimit(1)
                }
                .themedButton()
                .accessibilityIdentifier("extension-review-toggle")
                Spacer(minLength: Theme.Space.flush)
                if let discard = review.discard {
                    ExtensionActionButton(action: discard, title: String(localized: "Discard Drafts"),
                                          destructive: true, model: model, link: $link) {
                        Text("Discard Drafts").lineLimit(1)
                    }
                    .themedButton()
                }
            }
            if expanded {
                CodeComposerView(placeholder: review.placeholder ?? String(localized: "Write a comment…"),
                                 text: text, buttons: [], running: model.running, identifier: "extension-review-summary")
                    .disabled(!model.fieldsEnabled)
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: Theme.Space.s) { buttons }
                        .fixedSize(horizontal: true, vertical: false)
                    Menu { buttons } label: { Text("More").lineLimit(1) }
                        .themedButton()
                }
                .frame(maxWidth: .infinity, alignment: .trailing)
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .background(Theme.pageBackground)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("extension-review-bar")
        .modifier(ExtensionStreamProbe(stream: model.stream, lifecycle: "review"))
    }

    @ViewBuilder private var buttons: some View {
        ForEach(Array(review.buttons.enumerated()), id: \.element.action.id) { index, button in
            ExtensionActionButton(action: button.action, title: button.title, model: model,
                                  values: [review.name: model.values[review.name] ?? ""], link: $link) {
                HStack(spacing: Theme.Space.xs) {
                    if model.running == button.action.id { ProgressView().controlSize(.small) }
                    if index == 0 {
                        Text(button.title).lineLimit(1)
                    } else {
                        Text(button.title).lineLimit(1).foregroundStyle(ExtensionUIStyle.ink(button.tone))
                    }
                }
            }
            .themedButton(prominent: index == 0)
        }
    }
}
