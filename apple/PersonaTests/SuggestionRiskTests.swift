import Foundation
import Testing
@testable import Persona

/// `suggestion_risky` is advisory wire data: a bad value must not drop the session, and native snapshots keep it.
@MainActor struct SuggestionRiskTests {
    private func session(risk: String? = nil) throws -> Session {
        let field = risk.map { #","suggestion_risky":"# + $0 } ?? ""
        return try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"box","session_id":"chat-1","title":"Chat","status":"idle","state":"idle","suggestion":"Review before sending"\#(field)}"#.utf8))
    }

    @Test func riskDecodesTrueFalseAndMissing() throws {
        #expect(try session(risk: "true").suggestionRisky)
        #expect(try session(risk: "false").suggestionRisky == false)
        let old = try session()
        #expect(old.suggestionRisky == false && old.suggestion == "Review before sending" && old.sessionID == "chat-1")
    }

    @Test(arguments: ["null", #""true""#, "1", "0", "[]", "{}"])
    func oddRiskValuesAreNotRiskyAndKeepTheSession(raw: String) throws {
        let decoded = try session(risk: raw)
        #expect(decoded.suggestionRisky == false)
        #expect(decoded.suggestion == "Review before sending" && decoded.sessionID == "chat-1")
    }

    @Test func codableAndNativeSnapshotKeepRisk() throws {
        let original = try session(risk: "true")
        let encoded = try JSON.encoder.encode(original)
        let object = try #require(try JSONSerialization.jsonObject(with: encoded) as? [String: Any])
        #expect(object["suggestion_risky"] as? Bool == true)
        #expect(try JSON.decoder.decode(Session.self, from: encoded).suggestionRisky)

        let snapshot = AppModel.DevSnapshot(machines: [], sessions: [original], interactions: [], assistant: nil,
                                            managers: nil, defaultManager: nil, picked: nil, pickedManager: nil)
        let restored = try JSON.decoder.decode(AppModel.DevSnapshot.self, from: JSON.encoder.encode(snapshot))
        #expect(restored.sessions.first?.suggestionRisky == true)
        #expect(restored.sessions.first?.suggestion == original.suggestion)
    }

    @Test func riskyPillCanOnlyFillAndSafePillKeepsDirectSend() {
        var fills = 0
        var sends = 0
        let risky = SuggestionPill(text: "Review", risky: true, fill: { fills += 1 }, send: { sends += 1 })
        #expect(risky.directSend == nil)
        risky.fill()
        #expect(fills == 1 && sends == 0)

        let safe = SuggestionPill(text: "Review", risky: false, fill: { fills += 1 }, send: { sends += 1 })
        #expect(safe.directSend != nil)
        safe.directSend?()
        #expect(fills == 1 && sends == 1)
    }

    @Test func aRiskOnlyChangeStillReachesTheOpenPage() throws {
        let old = try session(risk: "false")
        var changed = old
        changed.suggestionRisky = true
        #expect(!Session.sameForItsPage(old, changed))
        #expect(AgentsFeed.shape([old], searching: false) == AgentsFeed.shape([changed], searching: false))
        #expect(AgentsFeed.shape([old], searching: true) == AgentsFeed.shape([changed], searching: true))
    }
}
