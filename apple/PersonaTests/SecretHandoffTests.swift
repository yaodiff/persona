import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The hand-off card (`secret_handoff`, #3962) and secret references on the iPhone: the card is answered on itself only
/// (every quick path is off), says what goes where without a reference, and only its own approval carries the card's
/// feature; an extension's secret may hold a provider's reference, and a provider's page lists who uses its references
/// and its Always rules.
@MainActor struct SecretHandoffTests {
    private let raw = Data(#"""
    {"id":"ix_h","machine_id":"server","session_id":"s1","kind":"permission","tool_name":"secret_handoff",
     "tool_input":{"variables":[{"name":"REGISTRY_TOKEN","provider":"example-vault","provider_name":"Example Vault","title":"Registry token › credential"},
                                {"name":"DEPLOY_KEY","provider":"example-vault"}],
                   "agent":"Fix the release CI","machine_name":"MacBook","cwd":"~/code/app","prompt":"Fix the release CI"},
     "summary":"Give … Approve on iPhone or the web.","always_label":"skips this card for these items with claude on MacBook, in conversations you start yourself"}
    """#.utf8)

    @Test func theCardIsAnsweredOnItselfOnly() throws {
        let ix = try JSONDecoder().decode(Interaction.self, from: raw)
        #expect(ix.isSecretHandoff)
        #expect(!ix.isSecretRun)
        // every quick Allow (notifications' actions, lists, Siri, the watch, the Live Activity's intents) is off
        #expect(ix.requiresInput)
        #expect(ApproveFirstIntent.resolution(for: ix) == nil)
    }

    @Test func itSaysWhatGoesWhereNeverAReference() throws {
        let ix = try JSONDecoder().decode(Interaction.self, from: raw)
        let r = SecretHandoffRequest(ix.toolInput)
        #expect(r.variables.map(\.name) == ["REGISTRY_TOKEN", "DEPLOY_KEY"])
        #expect(r.variables.map(\.item) == ["Example Vault › Registry token › credential", "example-vault"])
        #expect(r.agent == "Fix the release CI" && r.machine == "MacBook" && r.cwd == "~/code/app" && r.prompt == "Fix the release CI")
        #expect(SecretHandoffRequest(nil).variables.isEmpty)
    }

    @Test func onlyTheCardsApprovalCarriesTheFeature() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix_h/resolve", json: #"{"ok":true}"#)
        _ = try await hub.client.resolve("ix_h", Resolution(allow: true), handoffCard: true)
        _ = try await hub.client.resolve("ix_h", Resolution(allow: false))
        let sent = hub.sent("POST", "/api/v1/interactions/ix_h/resolve")
        #expect(sent.count == 2)
        #expect(sent.first?.headers["X-Persona-Features"] == SecretHandoff.feature)
        #expect(sent.last?.headers["X-Persona-Features"] == nil)
    }

    @Test func secretReferencesDecodeAndSayWhoseTheyAre() throws {
        let json = #"""
        {"id": "example-vault", "name": "Example Vault", "kind": "secret_providers", "status": "running", "permissions": [],
         "secrets": [{"name": "api_key", "title": "API key", "set": true,
                      "reference": {"provider": "example-vault", "provider_name": "Example Vault", "reference": "op://Example/Notes/credential", "title": "Notes › credential"}}],
         "secret_users": [{"kind": "extension", "extension": "example-notes", "extension_name": "Example Notes", "secret": "api_key", "secret_title": "API key", "reference": "op://Example/Notes/credential"},
                          {"kind": "launcher", "launcher": "gateway", "launcher_name": "Gateway", "machine": "macbook", "reference": "op://Example/Gateway/key"}],
         "handoff_rules": [{"id": "0123456789abcdef", "reference": "op://Example/Registry/credential", "launcher": "claude", "machine": "macbook", "by": "device:d1", "at": "2026-01-01T10:00:00Z"}]}
        """#
        let d = try JSONDecoder().decode(HubExtensionDetail.self, from: Data(json.utf8))
        let secret = try #require(d.secrets.first)
        #expect(secret.reference?.providerName == "Example Vault")
        #expect(ExtensionText.secretState(secret) == String(localized: "From \("Example Vault")", comment: "A secret that holds a secret provider's reference"))
        #expect(d.secretUsers.map(\.kind) == ["extension", "launcher"])
        #expect(Set(d.secretUsers.map(\.id)).count == 2)
        #expect(d.handoffRules.map(\.id) == ["0123456789abcdef"])
        // a hub before references: none of them
        let old = try JSONDecoder().decode(HubExtensionDetail.self, from: Data(#"{"id":"x","name":"X","kind":"k","status":"running","permissions":[]}"#.utf8))
        #expect(old.secretUsers.isEmpty && old.handoffRules.isEmpty)
    }

    /// What Siri, Shortcuts or the share sheet sends is marked `via`, so it never counts as the user's own words for an
    /// Always rule; what the app's own composer sends is not marked.
    @Test func siriAndTheShareSheetMarkWhatTheySend() async throws {
        let hub = FakeHub()
        let session = #"{"session":{"machine_id":"server","session_id":"s9","status":"working","cwd":"/a"}}"#
        hub.on("POST", "/api/v1/assistant/conversations", json: session)
        hub.on("POST", "/api/v1/sessions/server/main/messages", json: #"{"ok":true}"#)
        let id = UUID().uuidString
        _ = try await ShareTargets.send(hub.client, to: ShareTarget(kind: .newChat, key: nil, title: "New Chat"), text: "a page", images: [], clientMsgID: id)
        _ = try await ShareTargets.send(hub.client, to: ShareTarget(kind: .main, key: SessionKey(machineID: "server", sessionID: "main"), title: "Persona"),
                                        text: "a page", images: [], clientMsgID: id)
        _ = try await hub.client.startConversation(prompt: "what is on today", main: true, via: .intent)
        try await hub.client.send(SessionKey(machineID: "server", sessionID: "main"), text: "typed", clientMsgID: "c1")
        func via(_ body: Data?) throws -> String? {
            try (JSONSerialization.jsonObject(with: try #require(body)) as? [String: Any])?["via"] as? String
        }
        let starts = hub.sent("POST", "/api/v1/assistant/conversations"), sends = hub.sent("POST", "/api/v1/sessions/server/main/messages")
        #expect(starts.count == 2 && sends.count == 2)
        #expect(try via(starts[0].body) == "share")
        #expect(try via(starts[1].body) == "intent")
        #expect(try via(sends[0].body) == "share")
        #expect(try via(sends[1].body) == nil)
    }

    /// A message a card's button wrote (the Today card's Prep) keeps its mark in the outbox, across a relaunch, and is
    /// sent with it.
    @Test func aCardsMessageKeepsItsMarkInTheOutbox() async throws {
        var msg = PendingMessage(text: "Help me prepare for “Design review”", images: [])
        msg.via = .card
        let kept = try JSONDecoder().decode(PendingMessage.self, from: try JSONEncoder().encode(msg))
        #expect(kept.via == .card)
        let typed = try JSONDecoder().decode(PendingMessage.self, from: try JSONEncoder().encode(PendingMessage(text: "typed", images: [])))
        #expect(typed.via == nil)
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/server/main/messages", json: #"{"ok":true}"#)
        try await hub.client.send(SessionKey(machineID: "server", sessionID: "main"), text: kept.text, clientMsgID: kept.id.uuidString, via: kept.via)
        let body = try #require(hub.sent("POST", "/api/v1/sessions/server/main/messages").first?.body)
        #expect((try JSONSerialization.jsonObject(with: body) as? [String: Any])?["via"] as? String == "card")
    }

    @Test func aReferenceIsSavedInPlaceOfAValueAndARuleRemoved() async throws {
        let hub = FakeHub()
        let detail = #"{"id":"example-notes","name":"Example Notes","kind":"connectors","status":"running","permissions":[]}"#
        hub.on("PUT", "/api/v1/plugins/example-notes/-/secrets/api_key", json: detail)
        hub.on("DELETE", "/api/v1/plugins/example-vault/-/handoff-rules/0123456789abcdef", json: detail)
        _ = try await hub.client.setExtension("example-notes", secret: "api_key", reference: "op://Example/Notes/credential")
        _ = try await hub.client.removeHandoffRule("example-vault", rule: "0123456789abcdef")
        let put = try #require(hub.sent("PUT", "/api/v1/plugins/example-notes/-/secrets/api_key").first?.body)
        #expect(try JSON.decoder.decode([String: String].self, from: put) == ["reference": "op://Example/Notes/credential"])
        #expect(hub.sent("DELETE", "/api/v1/plugins/example-vault/-/handoff-rules/0123456789abcdef").count == 1)
    }
}
