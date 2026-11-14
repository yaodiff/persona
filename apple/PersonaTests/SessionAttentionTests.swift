import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A botmux session's reason for waiting and its Feishu topic (attention_reason, external_url; docs/integrations/engines.md「botmux」).
@MainActor
struct SessionAttentionTests {
    private func session(_ json: String) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    @Test func theReasonTakesTheListLineAndTheTopicOpensOnTheWeb() throws {
        let s = try session(#"{"session_id":"s","status":"blocked","last_text":"正在跑全量","attention_reason":"本轮执行失败，再发一条消息继续","external_url":"https://applink.example.com/thread?x=1"}"#)
        #expect(s.listPreview(muted: false) == "本轮执行失败，再发一条消息继续")
        #expect(s.externalLink?.absoluteString == "https://applink.example.com/thread?x=1")
    }

    @Test func withoutAReasonTheLastWordsStay() throws {
        let s = try session(#"{"session_id":"s","status":"working","last_text":"在补测试","attention_reason":""}"#)
        #expect(s.listPreview(muted: false) == "在补测试")
        #expect(s.externalLink == nil)
        #expect(try session(#"{"session_id":"s","status":"idle"}"#).listPreview(muted: false) == nil)
    }

    /// A host's failure notice (failure_reason) is no wait: the session stays in its turn's state, the notice takes the
    /// list line while it does not work, and its last words come back once it does.
    @Test func aFailureNoticeTakesTheListLineButNotTheState() throws {
        let s = try session(#"{"session_id":"s","status":"done","last_text":"已改好标题","failure_reason":"⚠️ 本轮执行失败，再发一条消息继续"}"#)
        #expect(s.failureReason == "⚠️ 本轮执行失败，再发一条消息继续")
        #expect(s.state != .needsYou)
        #expect(s.listPreview(muted: false) == "本轮执行失败，再发一条消息继续")
        let working = try session(#"{"session_id":"s","status":"working","last_text":"在补测试","failure_reason":"⚠️ 失败"}"#)
        #expect(working.listPreview(muted: false) == "在补测试")
    }

    /// The app draws its own warning sign: botmux's at the start of the notice goes (the note and the list line).
    @Test func theFailureNoteDropsTheNoticesWarningSign() {
        #expect(Session.failureText("⚠️ Claude 本轮执行失败") == "Claude 本轮执行失败")
        #expect(Session.failureText("⚠ 失败") == "失败")
        #expect(Session.failureText("本轮执行失败 ⚠️") == "本轮执行失败 ⚠️")
    }

    @Test func onlyAWebLinkOpens() throws {
        for url in ["javascript:alert(1)", "lark://applink/x", "http://example.com", "not a url"] {
            #expect(try session(#"{"session_id":"s","status":"idle","external_url":"\#(url)"}"#).externalLink == nil, "\(url)")
        }
    }
}
