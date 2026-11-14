import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The checklist after pairing and Settings' steps-left row (#4195, docs/assistant/onboarding.md 4.5), the expired-code message, and the
/// first screen's getting-started link.
@MainActor
struct OnboardingChecklistTests {
    private func readiness(_ json: String) throws -> AssistantReadiness {
        try JSON.decoder.decode(AssistantReadiness.self, from: Data(json.utf8))
    }

    @Test func rowsFollowTheReadiness() throws {
        let r = try readiness(#"""
        {"mode":"setup","next":"model","steps":[
          {"id":"computer","state":"ok"},{"id":"claude","state":"checking"},
          {"id":"model","state":"problem","code":"not_logged_in","title":"Claude Code 还没登录","detail":"登录一次","action":{"id":"connect_model"},"alt_actions":[{"id":"login_claude"}]},
          {"id":"first_message","state":"todo","code":"never_tried","title":"试一句","action":{"id":"test_assistant"}}]}
        """#)
        let rows = OnboardingChecklistModel.rows(r)
        #expect(rows.map(\.id) == ["computer", "claude", "model", "first_message"])
        #expect(rows.map(\.look) == [.done, .checking, .problem, .todo])
        #expect(rows[2].action == "connect_model" && rows[2].alt == "login_claude" && rows[2].title == "Claude Code 还没登录")
        #expect(rows[0].action == nil && rows[3].action == "test_assistant")
        // checking never blocks: only the model is left, and the row says what it is
        #expect(OnboardingChecklistModel.left(r) == 1 && !OnboardingChecklistModel.allDone(r))
        #expect(OnboardingChecklistModel.leftSummary(r) == "Claude Code 还没登录")
    }

    /// Settings counts only what blocks the main conversation (#1925): an unverified model or a running self-check in
    /// `full` leaves nothing to do, a rate limit neither, and a sleeping computer is one step that names itself.
    @Test func onlyBlockingStepsAreLeft() throws {
        let unverified = try readiness(#"""
        {"mode":"full","steps":[
          {"id":"computer","state":"ok"},{"id":"claude","state":"checking"},
          {"id":"model","state":"unknown","code":"model_unverified"},{"id":"first_message","state":"todo"}]}
        """#)
        #expect(OnboardingChecklistModel.left(unverified) == 0)
        let limited = try readiness(#"""
        {"mode":"full","steps":[
          {"id":"computer","state":"ok"},{"id":"claude","state":"ok"},{"id":"model","state":"problem","code":"rate_limited"}]}
        """#)
        #expect(OnboardingChecklistModel.left(limited) == 0)
        let asleep = try readiness(#"""
        {"mode":"setup","next":"computer","steps":[
          {"id":"computer","state":"problem","code":"node_unreachable","title":"laptop is offline","action":{"id":"wake_computer"}},
          {"id":"claude","state":"unknown"},{"id":"model","state":"unknown"},{"id":"first_message","state":"unknown"}]}
        """#)
        #expect(OnboardingChecklistModel.left(asleep) == 1)
        #expect(OnboardingChecklistModel.leftSummary(asleep) == "laptop is offline")
    }

    @Test func zeroComputersAndUnknownValues() throws {
        let zero = try readiness(#"""
        {"mode":"setup","next":"computer","steps":[
          {"id":"computer","state":"todo","code":"no_machine","title":"添加你的电脑","action":{"id":"add_computer"}},
          {"id":"claude","state":"unknown"},{"id":"model","state":"unknown"},{"id":"first_message","state":"unknown"}]}
        """#)
        // unknown steps behind a missing computer are not counted: one step, adding the computer
        #expect(OnboardingChecklistModel.left(zero) == 1)
        #expect(OnboardingChecklistModel.leftSummary(zero) == "添加你的电脑")
        #expect(OnboardingChecklistModel.rows(zero).first?.action == "add_computer")
        // a state this app does not know: one more step (todo); an action it does not know: no button
        let odd = try readiness(#"""
        {"mode":"setup","steps":[{"id":"computer","state":"sleepy","action":{"id":"wiggle"}}]}
        """#)
        let row = try #require(OnboardingChecklistModel.rows(odd).first)
        #expect(row.look == .todo && row.action == nil)
        // a step the hub did not send at all is unknown, not done
        #expect(OnboardingChecklistModel.rows(odd)[1].look == .unknown)
        #expect(OnboardingChecklistModel.left(nil) == 0)
    }

    @Test func allDoneClosesWithStart() throws {
        let full = try readiness(#"""
        {"mode":"full","steps":[{"id":"computer","state":"ok"},{"id":"claude","state":"ok"},{"id":"model","state":"ok"},{"id":"first_message","state":"ok"}]}
        """#)
        #expect(OnboardingChecklistModel.allDone(full) && OnboardingChecklistModel.left(full) == 0)
    }

    @Test func laterDismissesAtTheHub() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/assistant/readiness/dismiss", json: #"{"ok":true}"#)
        let m = AssistantReadinessModel()
        m.reset(try readiness(#"{"mode":"setup","steps":[],"dismissed":false}"#))
        await m.dismiss(api: hub.client)
        let body = try #require(try JSON.decoder.decode(JSONValue.self, from: hub.sent("POST", "/api/v1/assistant/readiness/dismiss").first?.body ?? Data()).object)
        #expect(body["dismissed"] == .bool(true) && m.current?.dismissed == true)
    }

    @Test func theChecklistShowsOnlyAfterPairingOnAReadinessHub() throws {
        let model = AppModel.shared
        let was = (model.checklistPending, model.readiness.current)
        defer { model.checklistPending = was.0; model.readiness.reset(was.1) }
        model.readiness.reset(try readiness(#"{"mode":"setup","steps":[],"dismissed":false}"#))
        model.checklistPending = false
        #expect(!model.showsChecklist)
        model.checklistPending = true
        // shown only where the hub says it has readiness (features); a put-off one never comes back by itself
        #expect(model.showsChecklist == model.hubFeatures.contains(AssistantReadiness.feature))
        model.readiness.reset(try readiness(#"{"mode":"setup","steps":[],"dismissed":true}"#))
        #expect(!model.showsChecklist)
    }

    @Test func anExpiredCodeIsSaidInWords() {
        #expect(PairError.isExpiredCode(APIError(status: 401, message: "invalid token", fields: ["code": "invalid_token"])))
        #expect(PairError.isExpiredCode(APIError(status: 401, message: "invalid token"))) // a hub before codes
        #expect(!PairError.isExpiredCode(APIError(status: 403, message: "forbidden", fields: ["code": "forbidden"])))
        #expect(!PairError.isExpiredCode(URLError(.timedOut)))
    }
}
