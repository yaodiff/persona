import AppIntents
import Foundation
import Testing
@testable import Persona

/// Message Persona (#17789): the Action button's, the control's and Shortcuts' way into the main chat with the keyboard
/// up, with no network beyond `FakeHub`.
@MainActor @Suite(.serialized) struct MessagePersonaIntentTests {
    private static let assistant = #"{"machine_id":"m","launcher":"claude","cwd":"/","main":"main","conversations":[{"machine_id":"m","session_id":"main","status":"idle","assistant":true}]}"#

    /// It comes to the front: the keyboard needs the app there.
    @Test func opensTheApp() {
        if #available(iOS 26.0, *) {
            #expect(OpenPersonaTypingIntent.supportedModes == .foreground(.immediate))
        } else {
            #expect(OpenPersonaTypingIntent.openAppWhenRun)
        }
        #expect(OpenPersonaTypingIntent.isDiscoverable)
    }

    /// From another tab, a pushed Settings sheet and a side conversation: the main conversation, and its composer is
    /// asked for the keyboard.
    @Test func opensTheMainConversationAndAsksItsComposerForTheKeyboard() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.assistant)
        let model = try Self.model(hub)
        defer { Self.close(model) }
        await model.refreshAssistant()
        let main = try #require(model.assistantMain)
        model.selectedTab = .chats
        model.shellSettings = true
        model.assistantCurrent = SessionKey(machineID: "m", sessionID: "side")

        await model.openMainAssistantForTyping(waits: 0)

        #expect(model.selectedTab == .assistant)
        #expect(!model.shellSettings)
        #expect(model.assistantCurrent == main)
        #expect(model.composerFocus == main)
    }

    /// A fresh install has no main conversation yet: the new conversation page, and no composer is asked.
    @Test func withoutAMainConversationOpensTheNewConversationPage() async throws {
        let model = try Self.model()
        defer { Self.close(model) }
        model.selectedTab = .chats
        model.assistantCurrent = SessionKey(machineID: "m", sessionID: "previous")

        await model.openMainAssistantForTyping(waits: 0)

        #expect(model.selectedTab == .assistant)
        #expect(model.assistantCurrent == nil)
        #expect(model.composerFocus == nil)
    }

    /// Listed for the Action button on the phone (an App Shortcut) and drawn by its control, still ten App Shortcuts.
    @Test func isAnAppShortcutAndAControl() throws {
        #expect(PersonaShortcuts.appShortcuts.count == 10)
        let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        let shortcuts = try String(contentsOf: apple.appending(path: "Persona/Intents/PersonaShortcuts.swift"), encoding: .utf8)
        #expect(shortcuts.contains("intent: OpenPersonaTypingIntent(),"))
        let controls = try String(contentsOf: apple.appending(path: "PersonaWidgets/PersonaControls.swift"), encoding: .utf8)
        let control = try #require(controls.range(of: "struct MessagePersonaControl: ControlWidget {"))
        let end = try #require(controls.range(of: "\n}\n", range: control.upperBound..<controls.endIndex))
        let drawing = String(controls[control.upperBound..<end.lowerBound])
        #expect(drawing.contains("ControlWidgetButton(action: OpenPersonaTypingIntent())"))
        #expect(drawing.contains("systemImage: \"\(Theme.Icon.keyboard)\""))
        let bundle = try String(contentsOf: apple.appending(path: "PersonaWidgets/PersonaWidgetsBundle.swift"), encoding: .utf8)
        #expect(bundle.contains("MessagePersonaControl()"))
    }

    /// What the system lists is what the build's App Intents metadata holds, not the Swift list: the extractor dropped every
    /// App Shortcut inside an `#if` in the builder, so the phone listed seven, without Call Persona, Start Meeting or
    /// Message Persona (#17789). The app hosts these tests, so its own metadata is in the main bundle.
    @Test func theBuiltMetadataListsEveryAppShortcut() throws {
        let url = Bundle.main.bundleURL.appending(path: "Metadata.appintents/extract.actionsdata")
        let json = try #require(try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        let listed = (json["autoShortcuts"] as? [[String: Any]] ?? []).compactMap { $0["actionIdentifier"] as? String }
        #expect(listed.count == PersonaShortcuts.appShortcuts.count)
        for intent in ["OpenPersonaTypingIntent", "CallPersonaIntent", "StartMeetingRecordingIntent"] {
            #expect(listed.contains(intent), "\(intent) is not in the build's App Shortcuts: \(listed)")
        }
    }

    private static func model(_ hub: FakeHub = FakeHub()) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "message-persona-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(ServerConfig(url: hub.url, token: "t"))
        return model
    }

    private static func close(_ model: AppModel) {
        for link in model.links { link.live.disconnect() }
    }
}
