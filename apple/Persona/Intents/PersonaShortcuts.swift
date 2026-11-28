import AppIntents

/// Siri / Spotlight shortcuts. Every phrase has to include `\(.applicationName)`; only AppEntity/AppEnum
/// parameters may be interpolated, which is why the message text is asked for after the phrase matches.
// One list per platform, with no `#if` inside the builder: the App Intents metadata extractor drops every App Shortcut
// in an `#if` there (#17789; the iPhone listed none of Call Persona, Start Meeting or Message Persona). Apple allows
// ten; `CallPersonaIntentTests.atMostTenAppShortcuts` holds the count and `MessagePersonaIntentTests` the built list.
#if os(iOS)
struct PersonaShortcuts: AppShortcutsProvider {
    nonisolated static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: TalkToPersonaIntent(),
            phrases: [
                "Have a chat with \(.applicationName)",
                "Start a chat with \(.applicationName)",
                "Talk to \(.applicationName)",
                "Chat with \(.applicationName)",
            ],
            shortTitle: "Talk to Persona",
            systemImageName: "waveform.badge.mic" // icon: App Shortcuts take only a literal
        )
        // A call to Persona (#9290), no entitlement needed: "Hey Siri, call Persona", AirPods included. Siri may take a
        // "call <name>" phrase as a phone call of its own (the Simulator's did), so one phrase leads with the name.
        AppShortcut(
            intent: CallPersonaIntent(),
            phrases: [
                "Call \(.applicationName)",
                "Start a call with \(.applicationName)",
                "Give \(.applicationName) a call",
                "Make a call with \(.applicationName)",
                "Phone \(.applicationName)",
                "\(.applicationName) call",
            ],
            shortTitle: "Call Persona",
            systemImageName: "phone.arrow.up.right" // icon: App Shortcuts take only a literal (Theme.Icon.callPersona)
        )
        AppShortcut(
            intent: AskPersonaIntent(),
            phrases: [
                "Ask \(.applicationName) something",
                "Quick question for \(.applicationName)",
                "Ask \(.applicationName)",
                "Ask \(.applicationName) a question",
            ],
            shortTitle: "Ask Persona",
            systemImageName: "sparkles" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentTaskIntent(),
            phrases: [
                "Give an agent a task in \(.applicationName)",
                "Ask \(\.$agent) in \(.applicationName) to do something",
                "Hand off to an agent in \(.applicationName)",
                "Have \(.applicationName) agent do something",
                "Send a task to \(\.$agent) with \(.applicationName)",
            ],
            shortTitle: "Give an Agent a Task",
            systemImageName: "person.crop.circle.badge.checkmark" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentsStatusIntent(),
            phrases: [
                "Unread from \(.applicationName) personal agents",
                "What's new from \(.applicationName) agents",
                "Check \(.applicationName) agents",
            ],
            shortTitle: "Agent Unread",
            systemImageName: "tray.full" // icon: App Shortcuts take only a literal
        )
        // Message Persona (#17789): the main chat with the keyboard up, so the Action button lists it with nothing to set
        // up. It takes Ask Claude's place on the phone (ten at most): Ask Claude, an Agents-side action, stays an action
        // in the Shortcuts app there.
        AppShortcut(
            intent: OpenPersonaTypingIntent(),
            phrases: [
                "Type to \(.applicationName)",
                "Write to \(.applicationName)",
                "Open my \(.applicationName) chat",
                "Open the keyboard in \(.applicationName)",
            ],
            shortTitle: "Message Persona",
            systemImageName: "keyboard" // icon: App Shortcuts take only a literal (Theme.Icon.keyboard)
        )
        AppShortcut(
            intent: PersonaStatusIntent(),
            phrases: [
                "Status of \(.applicationName)",
                "\(.applicationName) status",
                "How's \(.applicationName) doing",
                "Check progress in \(.applicationName)",
            ],
            shortTitle: "Status",
            systemImageName: "gauge.with.dots.needle.33percent" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: ApproveFirstIntent(),
            phrases: [
                "Approve the first one in \(.applicationName)",
                "Approve pending in \(.applicationName)",
            ],
            shortTitle: "Approve First",
            systemImageName: "checkmark.shield" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AddTodoIntent(),
            phrases: [
                "Create a to-do in \(.applicationName)",
                "Note a to-do in \(.applicationName)",
                "Add to-do to \(.applicationName)",
                "Add a to-do in \(.applicationName)",
                "New \(.applicationName) to-do",
            ],
            shortTitle: "New To-Do",
            systemImageName: "checklist" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: StartMeetingRecordingIntent(),
            // Start Meeting since #8731; the Record phrases keep working.
            phrases: [
                "Start a meeting with \(.applicationName)",
                "\(.applicationName), start a meeting",
                "Record with \(.applicationName)",
                "Record a meeting with \(.applicationName)",
                "Record this meeting with \(.applicationName)",
                "Start recording in \(.applicationName)",
                "\(.applicationName), record the meeting",
            ],
            shortTitle: "Start Meeting",
            systemImageName: "waveform.circle" // icon: App Shortcuts take only a literal (Theme.Icon.recording)
        )
    }

    nonisolated static var shortcutTileColor: ShortcutTileColor { .tangerine }
}
#else
struct PersonaShortcuts: AppShortcutsProvider {
    nonisolated static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: TalkToPersonaIntent(),
            phrases: [
                "Have a chat with \(.applicationName)",
                "Start a chat with \(.applicationName)",
                "Talk to \(.applicationName)",
                "Chat with \(.applicationName)",
            ],
            shortTitle: "Talk to Persona",
            systemImageName: "waveform.badge.mic" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AskPersonaIntent(),
            phrases: [
                "Ask \(.applicationName) something",
                "Quick question for \(.applicationName)",
                "Ask \(.applicationName)",
                "Ask \(.applicationName) a question",
            ],
            shortTitle: "Ask Persona",
            systemImageName: "sparkles" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentTaskIntent(),
            phrases: [
                "Give an agent a task in \(.applicationName)",
                "Ask \(\.$agent) in \(.applicationName) to do something",
                "Hand off to an agent in \(.applicationName)",
                "Have \(.applicationName) agent do something",
                "Send a task to \(\.$agent) with \(.applicationName)",
            ],
            shortTitle: "Give an Agent a Task",
            systemImageName: "person.crop.circle.badge.checkmark" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentsStatusIntent(),
            phrases: [
                "Unread from \(.applicationName) personal agents",
                "What's new from \(.applicationName) agents",
                "Check \(.applicationName) agents",
            ],
            shortTitle: "Agent Unread",
            systemImageName: "tray.full" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AskClaudeIntent(),
            phrases: [
                "Ask Claude in \(.applicationName)",
                "Ask Claude with \(.applicationName)",
                "\(.applicationName), ask Claude",
                "Quick question for Claude in \(.applicationName)",
            ],
            shortTitle: "Ask Claude [Ask Claude Intent 946979]",
            systemImageName: "bubble.left.and.text.bubble.right" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: PersonaStatusIntent(),
            phrases: [
                "Status of \(.applicationName)",
                "\(.applicationName) status",
                "How's \(.applicationName) doing",
                "Check progress in \(.applicationName)",
            ],
            shortTitle: "Status",
            systemImageName: "gauge.with.dots.needle.33percent" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: ApproveFirstIntent(),
            phrases: [
                "Approve the first one in \(.applicationName)",
                "Approve pending in \(.applicationName)",
            ],
            shortTitle: "Approve First",
            systemImageName: "checkmark.shield" // icon: App Shortcuts take only a literal
        )
        // Apple allows ten App Shortcuts. On the phone Call Persona (#9290) takes this one's place: Run Extension Action
        // asks which action and can't take its values by voice, so it stays an action in Shortcuts there.
        AppShortcut(
            intent: RunExtensionActionIntent(),
            phrases: ["Run an extension action in \(.applicationName)"],
            shortTitle: "Run Extension Action",
            // AppShortcuts metadata requires a literal; mirrored from ExtensionSurfaceTokens.actionIcon.
            systemImageName: "puzzlepiece.extension" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AddTodoIntent(),
            phrases: [
                "Create a to-do in \(.applicationName)",
                "Note a to-do in \(.applicationName)",
                "Add to-do to \(.applicationName)",
                "Add a to-do in \(.applicationName)",
                "New \(.applicationName) to-do",
            ],
            shortTitle: "New To-Do",
            systemImageName: "checklist" // icon: App Shortcuts take only a literal
        )
    }

    nonisolated static var shortcutTileColor: ShortcutTileColor { .tangerine }
}
#endif
