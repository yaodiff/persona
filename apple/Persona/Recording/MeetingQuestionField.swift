#if os(iOS)
import SwiftUI

/// The typed question's field and send button (#4486, DESIGN_SYSTEM.md 4.4), one view for the live sheet's bar
/// (`RecordingLiveBar`) and the meeting page's (`MeetingPageBar`, #3862), so the two never drift apart: the composer's field
/// (`ComposerSurface`, a vertical `TextField` that grows to `Theme.Recording.askLines` lines) and its round send button (`ComposerRoundButton`), which stays in place dimmed
/// (`Theme.Recording.askSendIdle`) until there is something it can send. Return typed at the end of the field sends too.
///
/// `beside` (the meeting page's [+]), the field and send sit on one row.
/// `instead` (the meeting page's Talk, #3862 phase 4) takes send's place while `showsInstead` (the field is empty), as
/// the chat's voice button does.
struct MeetingQuestionField<Beside: View, Instead: View>: View {
    @Binding var text: String
    var focus: FocusState<Bool>.Binding
    var placeholder: String
    /// Send's spoken name.
    var sendLabel: String
    var canSend: Bool
    /// The field takes no words now (the bar's caption says why): a tap doesn't bring the keyboard up.
    var disabled = false
    /// The keyboard comes up as the field appears (the live bar opens its row for typing).
    var focusOnAppear = false
    /// `instead` stands where send is.
    var showsInstead = false
    var send: () -> Void
    @ViewBuilder var beside: () -> Beside
    @ViewBuilder var instead: () -> Instead

    var body: some View {
        HStack(alignment: .bottom, spacing: Theme.Space.xs) {
            beside()
            field
            trailing
        }
    }

    @ViewBuilder private var trailing: some View {
        if showsInstead { instead() } else { sendButton }
    }

    private var field: some View {
        TextField(placeholder, text: $text, axis: .vertical)
            .font(Theme.Font.body)
            .lineLimit(1...Theme.Recording.askLines)
            .focused(focus)
            .submitLabel(.send)
            .onSubmit { send() }
            // A multi-line field takes Return as a new line rather than a submit: one typed at the end sends instead
            // (what can't go is dropped by `send`). A pasted question keeps its lines.
            .onChange(of: text) { old, new in
                guard new == old + "\n" else { return }
                text = old
                send()
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.s)
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .modifier(ComposerSurface())
            .contentShape(.rect(cornerRadius: Theme.Shell.composerRadius))
            .onTapGesture { if !disabled { focus.wrappedValue = true } }
            .disabled(disabled)
            .accessibilityIdentifier("meeting-ask-field")
            .onAppear { if focusOnAppear { focus.wrappedValue = true } }
    }

    private var sendButton: some View {
        ComposerRoundButton(symbol: Theme.Icon.send, label: sendLabel, id: "meeting-ask-send", pressScales: true) { send() }
            .disabled(!canSend)
            .opacity(canSend ? 1 : Theme.Recording.askSendIdle)
    }
}

extension MeetingQuestionField where Beside == EmptyView, Instead == EmptyView {
    /// Nothing beside the field (the live bar's row), and send always.
    init(text: Binding<String>, focus: FocusState<Bool>.Binding, placeholder: String, sendLabel: String, canSend: Bool,
         disabled: Bool = false, focusOnAppear: Bool = false, send: @escaping () -> Void) {
        self.init(text: text, focus: focus, placeholder: placeholder, sendLabel: sendLabel, canSend: canSend, disabled: disabled,
                  focusOnAppear: focusOnAppear, send: send, beside: { EmptyView() }, instead: { EmptyView() })
    }
}
#endif
