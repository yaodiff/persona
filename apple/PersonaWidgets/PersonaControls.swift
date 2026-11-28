import AppIntents
import SwiftUI
import WidgetKit

/// Control Center / Lock Screen / Action Button: open the assistant to ask something.
struct AskPersonaControl: ControlWidget {
    // New controls keep the previous stable kind so they remain usable after reinstalling the previous app.
    var body: some ControlWidgetConfiguration { Self.configuration(kind: LegacyNames.controlAskKind) }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaAssistantIntent()) {
                Label("Ask Persona", systemImage: "sparkles")
            }
        }
        .displayName("Ask Persona")
        .description("Opens the Persona assistant.")
    }
}

/// Control: open the Agents page.
struct PersonaAgentsControl: ControlWidget {
    var body: some ControlWidgetConfiguration { Self.configuration(kind: LegacyNames.controlAgentsKind) }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaAgentsIntent()) {
                Label("Agents", systemImage: "person.crop.circle.badge.checkmark")
            }
        }
        .displayName("Persona Agents")
        .description("Opens the Agents tab in Persona.")
    }
}

/// Control: open the main conversation with the keyboard up (#17789), as the Message Persona App Shortcut does.
struct MessagePersonaControl: ControlWidget {
    var body: some ControlWidgetConfiguration { Self.configuration(kind: "persona.control.message") }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaTypingIntent()) {
                // Theme.Icon.keyboard; the extension keeps its own symbol names, like the neighbouring controls.
                Label("Message Persona", systemImage: "keyboard")
            }
        }
        .displayName("Message Persona")
        .description("Opens your main chat with the keyboard up.")
    }
}

/// Control: open a new assistant conversation with dictation already running.
struct DictatePersonaControl: ControlWidget {
    var body: some ControlWidgetConfiguration { Self.configuration(kind: "persona.control.dictate") }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaDictationIntent()) {
                // Theme.Icon.dictate; the extension keeps its own symbol names, like the neighbouring controls.
                Label("Dictate to Persona", systemImage: "mic")
            }
        }
        .displayName("Dictate to Persona")
        .description("Opens a new conversation and starts dictation.")
    }
}

/// Control: call Persona on the main conversation, as the Call Persona App Shortcut does (#13241).
struct CallPersonaControl: ControlWidget {
    var body: some ControlWidgetConfiguration { Self.configuration(kind: "persona.control.call") }

    static func configuration(kind: String) -> some ControlWidgetConfiguration {
        StaticControlConfiguration(kind: kind) {
            ControlWidgetButton(action: OpenPersonaCallIntent()) {
                // Theme.Icon.callPersona; the extension keeps its own symbol names, like the neighbouring controls.
                Label("Call Persona", systemImage: "phone.arrow.up.right")
            }
        }
        .displayName("Call Persona")
        .description("Opens Persona and calls it on your main chat.")
    }
}

/// Also retain kinds saved by interim canonical-identity builds, with the same drawing and action.
struct CompatibleAssistantControl: ControlWidget {
    var body: some ControlWidgetConfiguration { AskPersonaControl.configuration(kind: "persona.control.ask") }
}

struct CompatibleAgentsControl: ControlWidget {
    var body: some ControlWidgetConfiguration { PersonaAgentsControl.configuration(kind: "persona.control.agents") }
}
