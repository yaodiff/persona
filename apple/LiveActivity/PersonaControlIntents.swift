import AppIntents
import Foundation

// Intents behind the Control Center / Lock Screen / Action Button controls (PersonaWidgets/PersonaControls.swift).
// Compiled into the app and the widget extension; they open the app, so the bodies only run in the app.

/// Opens Persona on the assistant tab, ready to type or dictate.
struct OpenPersonaAssistantIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Open Persona Assistant"
    nonisolated static let description = IntentDescription("Opens Persona's assistant page so you can talk or type.")
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MainActor.run { AppModel.shared.selectedTab = .assistant }
        #endif
        return .result()
    }
}

/// Message Persona (#17789): opens the main conversation with the keyboard up, for the Action button (an App Shortcut,
/// so it is listed there with nothing to set up), its control and Shortcuts.
struct OpenPersonaTypingIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Message Persona"
    // App Intent metadata says "this device", never "iPhone": App Store Connect rejects it (90626, #6471).
    nonisolated static let description = IntentDescription(
        "Opens your main chat with Persona with the keyboard up, ready to type.",
        categoryName: "Chat",
        searchKeywords: ["Persona", "message", "type", "chat", "keyboard"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        // Not awaited: a cold launch may wait for the hub's main conversation longer than the system waits.
        await MainActor.run { Task { await AppModel.shared.openMainAssistantForTyping() } }
        #endif
        return .result()
    }
}

/// Opens a new assistant conversation and asks its composer to start dictating once it is visible.
struct OpenPersonaDictationIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Dictate to Persona"
    nonisolated static let description = IntentDescription("Opens a new conversation and starts dictation.")
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    /// The same entry for the intent and its DEBUG stand-in; tests can supply a model other than the app's singleton.
    #if !WIDGET_EXTENSION
    @MainActor static func open(_ model: AppModel) {
        model.shellSettings = false
        model.openNewAssistantForDictation()
    }
    #endif

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MainActor.run { Self.open(AppModel.shared) }
        #endif
        return .result()
    }
}

/// Calls Persona on the main conversation (#13241): the Call Persona control's intent. The call is Call Persona's own
/// (`CallPersonaIntent.place()`, #9290), waiting for the app to be in front before its Start. A control needs an intent
/// the widget extension also compiles, and `CallPersonaIntent` is the app's alone. Hidden from Shortcuts, where the
/// Call Persona App Shortcut already is.
struct OpenPersonaCallIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Call Persona"
    nonisolated static let description = IntentDescription("Opens Persona and calls it on your main chat.")
    nonisolated static let isDiscoverable = false
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MainActor.run { CallPersonaIntent.place() }
        #endif
        return .result()
    }
}

/// Opens Persona on the Agents tab (personal agents are one tap from there).
struct OpenPersonaAgentsIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Open Persona Agents"
    nonisolated static let description = IntentDescription("Opens the Agents tab in Persona.")
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .foreground(.immediate) }
    // iOS 18 has no supportedModes: it opens the app this way.
    nonisolated static var openAppWhenRun: Bool { true }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MainActor.run { AppModel.shared.selectedTab = .chats }
        #endif
        return .result()
    }
}
