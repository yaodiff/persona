import AppIntents
import Foundation
import PersonaWire

/// "Ask Persona": sends the sentence into the assistant's main conversation (the Persona tab) and waits a little for the
/// answer, so Siri can speak it. Runs in the background straight against the hub (Keychain.serverConfig).
struct AskPersonaIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Ask Persona"
    nonisolated static let description = IntentDescription(
        "Sends what you say to the Persona assistant's main chat, waits for the answer and reads it out. If it takes a while, you'll hear that it was sent and can read the answer in the app later.",
        categoryName: "Chat",
        searchKeywords: ["Persona", "Assistant", "ask [Ask Persona Intent 4628c0]", "assistant", "ask"]
    )
    @available(iOS 26.0, *)
    nonisolated static var supportedModes: IntentModes { .background }

    @Parameter(title: "Question [Ask Persona Intent 33a861]", requestValueDialog: "What do you want to ask Persona?")
    var question: String

    nonisolated init() {}
    nonisolated init(question: String) { self.question = question }

    nonisolated static var parameterSummary: some ParameterSummary { Summary("Ask Persona \(\.$question)") }

    // The snippet view's `ShowsSnippetView` / `view:` result does not build with the iOS 26 SDKs (Xcode 26.x, Swift 6.2–6.3),
    // so it is only compiled with Swift 6.4+ (Xcode 27); before that the answer comes back as the dialog, as on macOS.
    #if (os(iOS) || os(visionOS)) && compiler(>=6.4)
    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> & ShowsSnippetView {
        let (body, answer, spoken) = try await ask()
        return .result(value: answer ?? "", dialog: IntentSupport.dialog(spoken),
                       view: IntentAnswerSnippet(title: body, answer: answer ?? AskPersonaText.pending))
    }
    #else
    // macOS (and iOS before Swift 6.4) has no snippet views for intents: the answer comes back as the dialog only.
    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        let (_, answer, spoken) = try await ask()
        return .result(value: answer ?? "", dialog: IntentSupport.dialog(spoken))
    }
    #endif

    /// The question as sent, the answer if it came in time, and the sentence to speak.
    private nonisolated func ask() async throws -> (String, String?, String) {
        let body = question.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { throw IntentFailureSentence(String(localized: "What do you want to ask Persona?")) }
        do {
            let api = try IntentSupport.client()
            let answer = try await AskPersonaFlow.ask(api, text: body)
            return (body, answer, AskPersonaText.spoken(answer))
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }
}

/// The network part of AskPersonaIntent: send, then poll the conversation's new items until the turn ends.
nonisolated enum AskPersonaFlow {
    /// Siri gives an intent roughly half a minute; leave room for the request itself.
    static let budget: Duration = .seconds(24)
    static let pollEvery: Duration = .milliseconds(1500)

    static func ask(_ api: APIClient, text: String) async throws -> String? {
        let state = try await api.assistant()
        guard let key = state.mainKey() else {
            // No main conversation yet: the first one becomes it. The prompt rides with the start request.
            // Marked: what Siri or a Shortcut sends (a locked phone's too) never counts as typed in the app (`via`).
            let s = try await api.startConversation(prompt: text, main: true, via: .intent)
            return try await waitForAnswer(api, key: SessionKey(machineID: s.machineID, sessionID: s.sessionID), after: 0)
        }
        let last = (try? await api.items(key, beforeSeq: nil, turns: 1).toSeq) ?? 0
        let id = UUID()
        try await api.send(key, text: text, clientMsgID: id.uuidString, via: .intent, opID: id)
        return try await waitForAnswer(api, key: key, after: last ?? 0)
    }

    private static func waitForAnswer(_ api: APIClient, key: SessionKey, after: Int) async throws -> String? {
        let clock = ContinuousClock()
        let deadline = clock.now + budget
        var seen: [Item] = []
        var cursor = after
        while clock.now < deadline {
            try await Task.sleep(for: pollEvery)
            if let page = try? await api.items(key, afterSeq: cursor) {
                seen += page.items
                cursor = page.toSeq ?? page.items.last?.seq ?? cursor
            }
            let status = (try? await api.sessions())?.first { $0.machineID == key.machineID && $0.sessionID == key.sessionID }?.status
            if AskPersonaText.turnOver(status: status), let answer = AskPersonaText.answer(in: seen) { return answer }
        }
        return AskPersonaText.answer(in: seen)
    }
}

/// Pure wording / extraction for AskPersonaIntent (tested in SiriIntentsTests).
nonisolated enum AskPersonaText {
    static let pending = String(localized: "Sent to Persona. It's still thinking; the answer will show up in Persona's main chat.")

    /// The assistant's visible text after the user's message, in order.
    static func answer(in items: [Item]) -> String? {
        let text = items.filter { $0.role == "assistant" && $0.kind == .text }
            .compactMap { $0.text?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .joined(separator: "\n\n")
        return text.isEmpty ? nil : text
    }

    static func turnOver(status: SessionStatus?) -> Bool {
        guard let status else { return false }
        return !status.isActive
    }

    /// What Siri reads: a long answer is clipped (the snippet shows the whole thing).
    static func spoken(_ answer: String?) -> String {
        guard let answer else { return pending }
        return IntentText.excerpt(AssistantCardText.replacingCards(AssistantActionSource.stripping(answer)), limit: 300)
    }
}
