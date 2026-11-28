import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Exact owner replies stay separate from ordinary Allow and model delivery, and retry under their original event id.
@MainActor
struct OwnerApprovalReplyTests {
    private let path = "/api/v1/sessions/m/s/messages"

    private func proposal() throws -> Interaction {
        let json = #"{"id":"proposal-1","machine_id":"m","session_id":"s","kind":"permission","tool_name":"owner_action_prepare","approval_batch_id":"batch-1","owner_proposal":true,"no_expiry":true,"owner_approval_phrases":["发","同意"],"created_at":"2026-11-21T12:00:00.123456789Z"}"#
        return try JSON.decoder.decode(Interaction.self, from: Data(json.utf8))
    }

    private func model(_ hub: FakeHub, _ ix: Interaction) throws -> AppModel {
        let model = AppModel()
        model.apiForTests = hub.client
        let session = #"{"machine_id":"m","session_id":"s","title":"Main","cwd":"/tmp","status":"idle","state":"needs_you"}"#
        let s = try JSON.decoder.decode(Session.self, from: Data(session.utf8))
        model.applySessions([MachineRef(group: "", id: "m"): [s]])
        model.replaceInteractions("", [ix])
        return model
    }

    @Test func proposalMetadataAndSettingsDecodeWhileTheMarkerNeverSerializes() throws {
        let ix = try proposal()
        #expect(ix.ownerApprovalPhrase == "发" && ix.noExpiry == true && ix.approvalBatchID == "batch-1")
        let ordinary = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ordinary","machine_id":"m","session_id":"s","kind":"permission","owner_approval_phrases":["发"]}"#.utf8))
        #expect(ordinary.ownerApprovalPhrase == nil)

        let resolution = Resolution(allow: true, ownerApprovalPhrase: "发")
        let object = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(resolution)) as? [String: Any])
        #expect(object["allow"] as? Bool == true)
        #expect(object["ownerApprovalPhrase"] == nil && object["owner_approval_phrase"] == nil)

        var body = APIClient.AssistantConfigBody()
        body.ownerReplyApproval = true
        body.persistentOwnerActionCards = false
        body.ownerReplyApprovalPhrases = ["发", "Agree"]
        let settings = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(body)) as? [String: Any])
        #expect(Set(settings.keys) == ["owner_reply_approval", "persistent_owner_action_cards", "owner_reply_approval_phrases"])
        #expect(settings["owner_reply_approval"] as? Bool == true && settings["persistent_owner_action_cards"] as? Bool == false)
        #expect(settings["owner_reply_approval_phrases"] as? [String] == ["发", "Agree"])
    }

    @Test func exactReplyUsesOnlyTheProofEnvelopeAndItsRetryKeepsTheEventID() async throws {
        let hub = FakeHub()
        var fail = true
        hub.on("POST", path) { _ in
            if fail {
                fail = false
                throw APIError(status: 400, message: "try again")
            }
            return Data(#"{"ok":true,"approval_reply":true,"interaction_id":"proposal-1"}"#.utf8)
        }
        let ix = try proposal()
        let model = try model(hub, ix)
        let answer = Resolution(allow: true, ownerApprovalPhrase: "发")
        await model.resolve(ix, answer)
        #expect(model.pendingInteractions.count == 1 && model.answerFailure(ix.uid) != nil)
        await model.retryAnswer(ix)
        #expect(model.pendingInteractions.isEmpty)

        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        #expect(hub.sent("POST", "/api/v1/interactions/proposal-1/resolve").isEmpty)
        let bodies = try requests.map { request -> [String: Any] in
            let body = try #require(request.body)
            let object = try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])
            #expect(Set(object.keys) == ["text", "client_msg_id", "approval_reply_to", "approval_reply_created_at"])
            #expect(object["text"] as? String == "发" && object["approval_reply_to"] as? String == "proposal-1")
            #expect(object["approval_reply_created_at"] as? String == ix.createdAtWire)
            #expect((object["client_msg_id"] as? String)?.caseInsensitiveCompare(request.headers[OpRetry.header] ?? "") == .orderedSame)
            return object
        }
        #expect((bodies[0]["client_msg_id"] as? String) == (bodies[1]["client_msg_id"] as? String))
    }

    @Test func aSameIDReplacementStartsANewOwnerEventWithItsOwnOccurrence() async throws {
        let hub = FakeHub()
        var fail = true
        hub.on("POST", path) { _ in
            if fail {
                fail = false
                throw APIError(status: 400, message: "try again")
            }
            return Data(#"{"ok":true,"approval_reply":true,"interaction_id":"proposal-1"}"#.utf8)
        }
        let first = try proposal()
        let replacement = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"proposal-1","machine_id":"m","session_id":"s","kind":"permission","tool_name":"owner_action_prepare","approval_batch_id":"batch-1","owner_proposal":true,"no_expiry":true,"owner_approval_phrases":["发","同意"],"created_at":"2026-11-21T12:00:01.123456789Z"}"#.utf8))
        let model = try model(hub, first)
        let answer = Resolution(allow: true, ownerApprovalPhrase: "发")
        await model.resolve(first, answer)
        model.replaceInteractions("", [replacement])
        await model.resolve(replacement, answer)
        let requests = hub.sent("POST", path)
        #expect(requests.count == 2)
        let bodies = try requests.map { request -> [String: Any] in
            let body = try #require(request.body)
            return try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])
        }
        #expect((bodies[0]["client_msg_id"] as? String) != (bodies[1]["client_msg_id"] as? String))
        #expect(bodies[0]["approval_reply_created_at"] as? String == first.createdAtWire)
        #expect(bodies[1]["approval_reply_created_at"] as? String == replacement.createdAtWire)
    }

    @Test func anUnadvertisedReplyCannotFallBackToAllowOrAModelMessage() async throws {
        let hub = FakeHub()
        let ix = try proposal()
        let model = try model(hub, ix)
        await model.resolve(ix, Resolution(allow: true, ownerApprovalPhrase: "owner said yes"))
        #expect(hub.sent("POST", path).isEmpty)
        #expect(hub.sent("POST", "/api/v1/interactions/proposal-1/resolve").isEmpty)
        #expect(model.pendingInteractions.count == 1)
    }
}

/// The settings view model reads the same hub state as the other clients and accepts a write only after the PUT and a
/// fresh GET both confirm it. A stale or partial answer rolls the optimistic value back.
@MainActor
struct OwnerApprovalSettingsModelTests {
    private let path = "/api/v1/assistant"
    private let off = #"{"machine_id":"m","launcher":"","cwd":"/tmp","conversations":[],"manager":"mgr","owner_reply_approval":false,"persistent_owner_action_cards":false,"owner_reply_approval_phrases":["发","同意"]}"#
    private let on = #"{"machine_id":"m","launcher":"","cwd":"/tmp","conversations":[],"manager":"mgr","owner_reply_approval":true,"persistent_owner_action_cards":false,"owner_reply_approval_phrases":["发","同意"]}"#
    private let custom = #"{"machine_id":"m","launcher":"","cwd":"/tmp","conversations":[],"manager":"mgr","owner_reply_approval":false,"persistent_owner_action_cards":false,"owner_reply_approval_phrases":["发","Agree"]}"#

    private final class Saved {
        var states: [APIClient.AssistantState] = []
    }

    private func make(_ hub: FakeHub, _ saved: Saved) -> OwnerApprovalSettingsModel {
        OwnerApprovalSettingsModel(deps: .init(api: { hub.client }, saved: { saved.states.append($0) }))
    }

    @Test func loadShowsCurrentValuesAndAnOlderHubHidesTheForm() async {
        let hub = FakeHub()
        hub.on("GET", path, json: off)
        let saved = Saved()
        let page = make(hub, saved)
        await page.load()
        #expect(page.loading == false && page.failure == nil)
        #expect(page.config?.ownerReplyApproval == false && page.config?.persistentOwnerActionCards == false)
        #expect(page.phraseText == "发\n同意" && saved.states.count == 1)

        hub.on("GET", path, json: #"{"machine_id":"m","launcher":"","cwd":"/tmp","conversations":[]}"#)
        await page.load()
        #expect(page.config == nil && saved.states.count == 1)
    }

    @Test func switchSaveChecksTheAnswerAndAFreshReadBack() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: off)
        let saved = Saved()
        let page = make(hub, saved)
        await page.load()
        hub.on("GET", path, json: on)
        hub.on("PUT", path, json: on)
        await page.saveSwitch(.reply, on: true)

        #expect(page.config?.ownerReplyApproval == true && page.failure == nil && page.saving == false)
        #expect(saved.states.count == 2)
        #expect(hub.sent("GET", path).count == 2)
        let requests = hub.sent("PUT", path)
        #expect(requests.count == 1)
        let request = try #require(requests.first)
        let body = try #require(JSONSerialization.jsonObject(with: request.body ?? Data()) as? [String: Any])
        #expect(Set(body.keys) == ["owner_reply_approval"])
        #expect(body["owner_reply_approval"] as? Bool == true)
    }

    @Test func aStaleSwitchAnswerRollsBackAndIsNotPublishedAsSaved() async {
        let hub = FakeHub()
        hub.on("GET", path, json: off)
        hub.on("PUT", path, json: off)
        let saved = Saved()
        let page = make(hub, saved)
        await page.load()
        await page.saveSwitch(.reply, on: true)

        #expect(page.config?.ownerReplyApproval == false && page.failure != nil && page.saving == false)
        #expect(saved.states.count == 1)
        #expect(hub.sent("GET", path).count == 1, "a stale PUT never becomes a confirmed local opt-in")
    }

    @Test func phraseSaveTrimsWholeLinesAndChecksTheReadBack() async throws {
        let hub = FakeHub()
        hub.on("GET", path, json: off)
        let saved = Saved()
        let page = make(hub, saved)
        await page.load()
        page.phraseText = " 发 \n\n Agree \n"
        hub.on("GET", path, json: custom)
        hub.on("PUT", path, json: custom)
        await page.savePhrases()

        #expect(page.phraseText == "发\nAgree" && page.phrases == ["发", "Agree"])
        #expect(page.config?.ownerReplyApprovalPhrases == ["发", "Agree"] && page.failure == nil)
        #expect(saved.states.count == 2 && hub.sent("GET", path).count == 2)
        let requests = hub.sent("PUT", path)
        #expect(requests.count == 1)
        let request = try #require(requests.first)
        let body = try #require(JSONSerialization.jsonObject(with: request.body ?? Data()) as? [String: Any])
        #expect(Set(body.keys) == ["owner_reply_approval_phrases"])
        #expect(body["owner_reply_approval_phrases"] as? [String] == ["发", "Agree"])
    }
}
