import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The readiness pages ask the hub again on their own while a step can finish on another computer (#1893): installing
/// Claude Code, signing in to it, waking a computer, a self-check still running.
struct ReadinessPollTests {
    private func readiness(_ steps: String) throws -> AssistantReadiness {
        try JSON.decoder.decode(AssistantReadiness.self, from: Data(#"{"mode":"setup","steps":[\#(steps)]}"#.utf8))
    }

    @Test func pollsWhileAStepCanFinishElsewhere() throws {
        // not signed in to Claude Code yet: done in a terminal on the computer
        let signIn = try readiness(#"""
        {"id":"computer","state":"ok"},{"id":"claude","state":"problem","code":"not_logged_in"},
        {"id":"model","state":"unknown"},{"id":"first_message","state":"todo","code":"never_tried"}
        """#)
        #expect(ReadinessPoll.key(signIn) == "claude:problem:not_logged_in")
        // the node's self-check outlasted the hub's wait: the spinner must not stay
        let checking = try readiness(#"{"id":"computer","state":"ok"},{"id":"claude","state":"checking"},{"id":"model","state":"ok"}"#)
        #expect(ReadinessPoll.key(checking) == "claude:checking:")
        // no computer yet, and a state this app does not know: both still to do
        let todo = try readiness(#"{"id":"computer","state":"todo","code":"no_machine"},{"id":"model","state":"sleepy"}"#)
        #expect(ReadinessPoll.key(todo) == "computer:todo:no_machine,model:sleepy:")
    }

    @Test func stopsWhenNothingCanFinishByItself() throws {
        #expect(ReadinessPoll.key(nil) == nil)
        let done = try readiness(#"""
        {"id":"computer","state":"ok"},{"id":"claude","state":"skipped"},{"id":"model","state":"ok"},{"id":"first_message","state":"ok"}
        """#)
        #expect(ReadinessPoll.key(done) == nil)
        // only the user's own try moves these: Try a message asks again by itself
        let untried = try readiness(#"""
        {"id":"computer","state":"ok"},{"id":"claude","state":"ok"},{"id":"model","state":"unknown","code":"model_unverified"},
        {"id":"first_message","state":"todo","code":"never_tried"}
        """#)
        #expect(ReadinessPoll.key(untried) == nil)
    }

    @Test func pollsOnlyInFrontAndWhileTheAppIsActive() {
        let key: String? = "claude:problem:claude_missing"
        #expect(ReadinessPoll.runs(key: key, tabActive: true, sceneActive: true, appActive: true))
        #expect(!ReadinessPoll.runs(key: nil, tabActive: true, sceneActive: true, appActive: true))
        #expect(!ReadinessPoll.runs(key: key, tabActive: false, sceneActive: true, appActive: true))
        #expect(!ReadinessPoll.runs(key: key, tabActive: true, sceneActive: false, appActive: true))
        #expect(!ReadinessPoll.runs(key: key, tabActive: true, sceneActive: true, appActive: false))
    }

    @Test func waitsLongerEachTimeUpToHalfAMinute() {
        #expect((0...6).map { ReadinessPoll.delay(after: $0) } == [5, 10, 20, 30, 30, 30, 30].map { Duration.seconds($0) })
        #expect(ReadinessPoll.delay(after: -1) == ReadinessPoll.first)
        #expect(ReadinessPoll.delay(after: .max) == ReadinessPoll.longest)
    }

    /// A poll is an ordinary ask, which a fresh answer short-circuits: the shortest wait must outlast `fresh`, or the
    /// first poll would never reach the hub.
    @Test @MainActor func theFirstPollIsNotSwallowedByAFreshAnswer() {
        #expect(ReadinessPoll.first >= .seconds(AssistantReadinessModel.fresh))
        #expect(ReadinessPoll.delay(after: 0) == ReadinessPoll.first)
    }
}
