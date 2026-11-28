import AppIntents
import Foundation

/// "Ask Claude": sends a message to the newest live session on a machine, or starts a new one.
struct AskClaudeIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Ask Claude [Ask Claude Intent 946979]"
    nonisolated static let description = IntentDescription(
        "Sends what you say to the latest session on a machine, or starts a new one in herdr.",
        categoryName: "Chat",
        searchKeywords: ["Claude", "Session", "Send"]
    )
    /// The hub is plain HTTP over an SSH tunnel; the app does not need to come to the foreground. The result still
    /// carries `OpenSessionIntent`, so tapping the dialog's button opens the chat.
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Content", requestValueDialog: "What do you want to say to Claude?")
    var text: String

    @Parameter(title: "Machines")
    var machine: MachineEntity?

    @Parameter(title: "New Session [Ask Claude Intent c57c30]", default: false)
    var newSession: Bool

    nonisolated init() {}

    nonisolated init(text: String, machine: MachineEntity? = nil, newSession: Bool = false) {
        self.text = text
        self.machine = machine
        self.newSession = newSession
    }

    nonisolated static var parameterSummary: some ParameterSummary {
        When(\.$newSession, .equalTo, true) {
            Summary("Start a new chat on \(\.$machine) and ask \(\.$text)")
        } otherwise: {
            Summary("Ask \(\.$text) on \(\.$machine)")
        }
    }

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & OpensIntent {
        let body = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { throw IntentFailureSentence(String(localized: "What do you want to say to Claude?")) }

        do {
            let target = try await resolveMachine()
            // To the hub of the machine's group.
            let api = try IntentSupport.client(group: target.ref.group)
            let session = try await deliver(api, on: target, text: body)
            return .result(
                opensIntent: OpenSessionIntent(target: SessionEntity(session)),
                dialog: IntentSupport.dialog(IntentText.sent(to: session.title))
            )
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }

    /// An explicit machine wins; otherwise the one used last in the new-chat sheet, else the first online one.
    private func resolveMachine() async throws -> MachineEntity {
        if let machine { return machine }
        let machines = try await IntentSupport.machines()
        guard let picked = IntentPicker.machine(machines, preferred: UserDefaults.standard.string(forKey: IntentText.machineKey))
        else { throw IntentFailure.noMachine }
        return MachineEntity(picked)
    }

    private func deliver(_ api: APIClient, on machine: MachineEntity, text: String) async throws -> Session {
        guard machine.online else { throw IntentFailure.offline(machine.name) }
        if newSession { return try await startSession(api, on: machine, text: text) }
        return try await AskClaudeFlow.sendToLatest(api, machineID: machine.ref.id, group: machine.ref.group,
                                                    machineName: machine.name, text: text)
    }

    private func startSession(_ api: APIClient, on machine: MachineEntity, text: String) async throws -> Session {
        let defaults = UserDefaults.standard
        guard let launcher = defaults.string(forKey: IntentText.launcherKey(machine.id)), !launcher.isEmpty,
              let cwd = defaults.string(forKey: IntentText.cwdKey(machine.id)), !cwd.isEmpty
        else { throw IntentFailure.noLauncher(machine.name) }
        // The prompt rides along with the start request; sending it again would duplicate it.
        return try await api.start(.init(machineID: machine.ref.id, cwd: cwd, launcher: launcher, prompt: text, async: true))
    }
}

/// The network part of AskClaudeIntent for an existing session (tested in SiriIntentsTests).
nonisolated enum AskClaudeFlow {
    static func sendToLatest(_ api: APIClient, machineID: String, group: String, machineName: String,
                             text: String, clientMsgID: UUID = UUID()) async throws -> Session {
        let sessions = IntentSupport.folded(try await api.sessions())
        guard let session = IntentPicker.session(sessions, machine: machineID, group: group) else {
            throw IntentFailure.noSession(machineName)
        }
        guard session.canSend else {
            throw IntentFailureSentence(session.sourceSubtitle ?? String(localized: "This session is view-only"))
        }
        try await api.send(session.key, text: text, clientMsgID: clientMsgID.uuidString, via: .intent, opID: clientMsgID) // Siri's or a Shortcut's, not typed in the app
        return session
    }
}
