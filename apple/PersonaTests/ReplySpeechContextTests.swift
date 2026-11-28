#if os(iOS)
import Testing
import UIKit
@testable import Persona

@MainActor struct ReplySpeechContextTests {
    @Test func settingsAndSheetsFromTheTitleAndChatEachCoverTheChat() {
        let context = ReplySpeechContext()
        #expect(context.allows(settingsOpen: false))
        #expect(!context.allows(settingsOpen: true))
        context.titleSheetOpen = true
        #expect(!context.allows(settingsOpen: false))
        #expect(!context.allows(settingsOpen: true))
        context.chatSheetOpen = true
        context.titleSheetOpen = false
        #expect(!context.allows(settingsOpen: false), "the webpage still covers the conversation")
        context.chatSheetOpen = false
        #expect(context.allows(settingsOpen: false))
    }

    @Test func markedInputStopsReadingBeforeTheDraftBindingChanges() throws {
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        let field = UITextView(frame: CGRect(x: 0, y: 0, width: 300, height: 44))
        window.addSubview(field)
        window.makeKeyAndVisible()
        defer { field.resignFirstResponder(); window.isHidden = true }
        try #require(field.becomeFirstResponder())
        let context = ReplySpeechContext()
        context.observeInput()
        defer { context.endInputObservation() }
        #expect(context.allows(settingsOpen: false))
        field.setMarkedText("ni", selectedRange: NSRange(location: 2, length: 0))
        #expect(!context.allows(settingsOpen: false))
        // Sending confirms the composition and clears the field, keeping the keyboard up.
        field.unmarkText()
        field.text = ""
        context.sentDraft()
        #expect(field.isFirstResponder)
        #expect(context.allows(settingsOpen: false))
        field.insertText("hello")
        #expect(!context.allows(settingsOpen: false), "starting the next draft stops playback again")
        field.resignFirstResponder()
        #expect(context.allows(settingsOpen: false))
    }
}
#endif
