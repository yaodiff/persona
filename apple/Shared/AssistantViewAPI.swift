import Foundation
import PersonaWire

nonisolated extension APIClient {
    private struct AssistantActionResponse: Decodable { var action: ExtensionViewAction }

    /// Uses the schema's input. A declared reply's text is deliberately absent from this request type.
    func assistantViewAction(conversation: String, key: String, input: ExtensionViewActionInput) async throws -> ExtensionViewAction {
        let data = try await request("POST", Self.assistantViewPath(conversation: conversation, key: key) + "/actions", body: input)
        return try JSON.decoder.decode(AssistantActionResponse.self, from: data).action
    }
    func assistantHTMLReply(conversation: String, key: String, input: ExtensionAssistantHTMLActionInput) async throws -> ExtensionViewAction {
        let data = try await request("POST", Self.assistantViewPath(conversation: conversation, key: key) + "/html-actions", body: input)
        return try JSON.decoder.decode(AssistantActionResponse.self, from: data).action
    }
    func assistantHTMLOpen(conversation: String, key: String, input: ExtensionAssistantHTMLActionInput) async throws -> ExtensionAssistantHTMLOpenResult {
        let data = try await request("POST", Self.assistantViewPath(conversation: conversation, key: key) + "/html-actions", body: input)
        return try JSON.decoder.decode(ExtensionAssistantHTMLOpenResult.self, from: data)
    }
    /// Escape each opaque id exactly once, including a literal percent or slash; never interpret it as a path.
    static func assistantViewPath(conversation: String, key: String) -> String {
        let allowed = CharacterSet.urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#%"))
        let conversation = conversation.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
        let key = key.addingPercentEncoding(withAllowedCharacters: allowed) ?? ""
        return "/api/v1/assistant/conversations/\(conversation)/views/\(key)"
    }
}
