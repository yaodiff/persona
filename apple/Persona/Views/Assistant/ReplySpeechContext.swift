#if os(iOS)
import Combine
import SwiftUI

/// The main conversation can stay mounted behind Settings or a title-menu sheet. A composing keyboard
/// can also show text before SwiftUI publishes a draft. Either makes a new reply ineligible for speech.
@MainActor @Observable final class ReplySpeechContext {
    var titleSheetOpen = false
    var chatSheetOpen = false
    private var inputHasText = false
    @ObservationIgnored private var inputEvents: AnyCancellable?

    func allows(settingsOpen: Bool) -> Bool { !settingsOpen && !titleSheetOpen && !chatSheetOpen && !inputHasText }

    func observeInput() {
        inputHasText = Self.hasText(FirstResponder.current as? UITextInput)
        let center = NotificationCenter.default
        let changes = Publishers.Merge(center.publisher(for: UITextView.textDidChangeNotification),
                                      center.publisher(for: UITextField.textDidChangeNotification))
        let ends = Publishers.Merge(center.publisher(for: UITextView.textDidEndEditingNotification),
                                   center.publisher(for: UITextField.textDidEndEditingNotification))
        inputEvents = changes.map { Self.hasText($0.object as? UITextInput) }
            .merge(with: ends.map { _ in false })
            .sink { [weak self] in self?.inputHasText = $0 }
    }

    private static func hasText(_ input: UITextInput?) -> Bool {
        guard let input else { return false }
        return input.hasText || TypedText.composing(input)
    }

    /// A successful send can clear the binding without a UIKit edit or the field losing focus.
    func sentDraft() { inputHasText = false }

    func endInputObservation() {
        inputEvents = nil
        inputHasText = false
    }
}

extension EnvironmentValues {
    @Entry var replySpeechContext: ReplySpeechContext?
}

struct ReplySpeechInputObservation: ViewModifier {
    let context: ReplySpeechContext
    @StoredValue(ReplySpeechPreferences.enabled) private var enabled = false

    func body(content: Content) -> some View {
        content
            .onChange(of: enabled, initial: true) {
                if enabled { context.observeInput() } else { context.endInputObservation() }
            }
            .onDisappear { context.endInputObservation() }
    }
}
#endif
