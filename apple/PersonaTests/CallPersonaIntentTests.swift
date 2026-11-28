import AppIntents
import Foundation
import Testing
@testable import Persona

/// Siri's and Shortcuts' ways into a call to Persona (#9290, docs/apple/siri.md「Calling Persona」): the App Shortcut Call
/// Persona, Talk to Persona with no car, and the opt-in SiriKit extension's switch as the app sees it.
struct CallPersonaIntentTests {
    /// The call's Start needs the app in front, so Call Persona opens it before it runs.
    @Test func callPersonaOpensTheApp() {
        if #available(iOS 26.0, *) {
            #expect(CallPersonaIntent.supportedModes == .foreground(.immediate))
        } else {
            #expect(CallPersonaIntent.openAppWhenRun)
        }
    }

    /// The Call Persona control (#13241) opens the app as Call Persona does and places Call Persona's own call, with
    /// Call Persona's glyph; its intent stays out of Shortcuts, where the App Shortcut already is.
    @Test func theCallControlPlacesCallPersonasCall() throws {
        if #available(iOS 26.0, *) {
            #expect(OpenPersonaCallIntent.supportedModes == .foreground(.immediate))
        } else {
            #expect(OpenPersonaCallIntent.openAppWhenRun)
        }
        #expect(!OpenPersonaCallIntent.isDiscoverable)
        let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        let intents = try String(contentsOf: apple.appending(path: "LiveActivity/PersonaControlIntents.swift"), encoding: .utf8)
        let start = try #require(intents.range(of: "struct OpenPersonaCallIntent: AppIntent {"))
        let end = try #require(intents.range(of: "\n}\n", range: start.upperBound..<intents.endIndex))
        let intent = String(intents[start.upperBound..<end.lowerBound])
        #expect(intent.contains("await MainActor.run { CallPersonaIntent.place() }"))
        let controls = try String(contentsOf: apple.appending(path: "PersonaWidgets/PersonaControls.swift"), encoding: .utf8)
        let control = try #require(controls.range(of: "struct CallPersonaControl: ControlWidget {"))
        let drawingEnd = try #require(controls.range(of: "\n}\n", range: control.upperBound..<controls.endIndex))
        let drawing = String(controls[control.upperBound..<drawingEnd.lowerBound])
        #expect(drawing.contains("ControlWidgetButton(action: OpenPersonaCallIntent())"))
        #expect(drawing.contains("systemImage: \"\(Theme.Icon.callPersona)\""))
    }

    /// The car's conversation still starts in the background (the phone may stay locked in the car); only the phone's
    /// call comes to the front.
    @Test func talkToPersonaStaysInTheBackgroundUntilItCalls() {
        if #available(iOS 26.0, *) {
            #expect(TalkToPersonaIntent.supportedModes.contains(.background))
            #expect(TalkToPersonaIntent.supportedModes.contains(.foreground(.dynamic)))
            #expect(!TalkToPersonaIntent.supportedModes.contains(.foreground(.immediate)))
        } else {
            #expect(!TalkToPersonaIntent.openAppWhenRun)
        }
    }

    /// Apple allows ten App Shortcuts; more and the build's App Intents metadata is refused.
    @Test func atMostTenAppShortcuts() {
        #expect(PersonaShortcuts.appShortcuts.count == 10)
    }

    /// Talk to Persona promises a call only where it places one: the phone's description and keywords; elsewhere
    /// (Apple Vision Pro, where `PersonaCalls` does not exist and it only says to connect CarPlay) CarPlay's alone.
    @Test func talkToPersonaPromisesACallOnlyWhereItPlacesOne() throws {
        let path = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "Persona/Intents/TalkToPersonaIntent.swift")
        // The code alone: a comment may say what it does not promise.
        let source = try String(contentsOf: path, encoding: .utf8).components(separatedBy: "\n")
            .filter { !$0.trimmingCharacters(in: .whitespaces).hasPrefix("//") }.joined(separator: "\n")
        let phone = try #require(source.range(of: "#if os(iOS)"))
        let other = try #require(source.range(of: "#else", range: phone.upperBound..<source.endIndex))
        let end = try #require(source.range(of: "#endif", range: other.upperBound..<source.endIndex))
        let elsewhere = String(source[other.upperBound..<end.lowerBound])
        #expect(String(source[phone.upperBound..<other.lowerBound]).contains("a call to Persona on this device"))
        #expect(elsewhere.contains("IntentDescription(") && !elsewhere.contains("call to Persona") && !elsewhere.contains(#""call""#))
    }

    @Test func siriSaysItIsCalling() {
        #expect(CallPersonaIntent.calling == String(localized: "Calling Persona"))
        #expect(CallPersonaIntent.calling != CarPlayTalk.notConnected.sentence)
    }

    /// `NSSiriUsageDescription` stays in the raw Info.plist in every build (ITMS-90683: `SiriCalls.swift` and
    /// `OwnerCall.swift` unconditionally link `Intents`), while a build carries the SiriKit extension and runtime
    /// `SiriCalls.builtIn` switch (`PersonaSiriCalls`) only when `apple/siri-calls.yml` was merged in.
    @MainActor @Test func siriCallsFollowTheBuildsSwitch() {
        let purpose = Bundle.main.infoDictionary?["NSSiriUsageDescription"] as? String
        #expect(purpose?.isEmpty == false)
        #expect(purpose != "NSSiriUsageDescription")
        let plugins = Bundle.main.builtInPlugInsURL?.appending(path: "PersonaCallIntents.appex").path ?? ""
        #expect(SiriCalls.builtIn == FileManager.default.fileExists(atPath: plugins))
    }
}
