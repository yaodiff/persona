import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// The number on the app's icon (#5824, `AppIconBadge`): the tab badges added up, the same rule on the iPhone and the
/// Mac's Dock (this file runs in both test targets); nothing while signed out; the system told only when the number
/// moved, once for a burst of changes. Made-up values.
@MainActor
struct AppIconBadgeTests {
    @Test func theIconAddsUpTheTabs() {
        let tabs = TabBadges(assistant: 1, agents: 2)
        #expect(AppIconBadge.count(tabs, inboxUnread: 3, paired: true) == 6)
        #expect(AppIconBadge.count(TabBadges(), inboxUnread: 0, paired: true) == 0)
        #expect(AppIconBadge.count(tabs, inboxUnread: 3, paired: false) == 0, "signed out: no number from the hub it left")
    }

    @Test func theDockSaysNothingAtZero() {
        #expect(AppIconBadge.label(0) == nil)
        #expect(AppIconBadge.label(12) == "12")
    }

    /// What the model's tabs count is what the icon counts: an agent asking twice is one in Needs You, one waiting in
    /// its terminal is one too, a Persona conversation's question counts on the Persona tab, and an agent that finished
    /// unread on none.
    @Test func itCountsWhatTheTabsCount() throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "icon-badge-" + UUID().uuidString))
        let sessions = try [session("b", state: "needs_you"), session("c", state: "needs_you"), session("d", state: "unread"),
                            session("main", state: "needs_you", assistant: true)]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        model.replaceInteractions("", try [card("x1", session: "b"), card("x2", session: "b"), card("x3", session: "main")])
        #expect(model.badges == TabBadges(assistant: 1, agents: 2))
        #expect(AppIconBadge.count(model.badges, inboxUnread: 4, paired: true) == 7)
    }

    /// A burst of changes is one call with where it ended; a burst that ends where it started is none.
    @Test func theSystemHearsOnlyANumberThatMoved() async throws {
        let source = Source()
        let told = Told()
        let watcher = AppIconBadgeWatcher(wait: 0.05) { told.values.append($0) }
        watcher.start { source.n }
        #expect(told.values == [0], "the number at launch, so a stale one from the last run goes")

        source.n = 1
        source.n = 2
        source.n = 3
        try await told.wait(for: 2)
        #expect(told.values == [0, 3])
        #expect(watcher.shown == 3)

        source.n = 5
        source.n = 3
        try await Task.sleep(for: .milliseconds(200))
        #expect(told.values == [0, 3], "back where it was: the system is not told again")

        source.n = 0
        try await told.wait(for: 3)
        #expect(told.values == [0, 3, 0], "everything handled clears the icon")
    }

    /// Back in front (after the permission question, or with badges turned on in Settings): the same number again.
    @Test func resendTellsTheSameNumberAgain() {
        let told = Told()
        let watcher = AppIconBadgeWatcher(wait: 0.05) { told.values.append($0) }
        watcher.resend()
        #expect(told.values.isEmpty, "nothing to resend before the first read")
        watcher.start { 4 }
        watcher.resend()
        #expect(told.values == [4, 4])
    }

    @Test func aSecondStartIsIgnored() {
        let told = Told()
        let watcher = AppIconBadgeWatcher(wait: 0.05) { told.values.append($0) }
        watcher.start { 2 }
        watcher.start { 9 }
        #expect(told.values == [2])
    }

    private func session(_ id: String, state: String, assistant: Bool = false) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"server","session_id":"\#(id)","state":"\#(state)","assistant":\#(assistant)}"#.utf8))
    }

    private func card(_ id: String, session: String) throws -> Interaction {
        try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"server","session_id":"\#(session)","kind":"permission"}"#.utf8))
    }
}

@MainActor @Observable private final class Source {
    var n = 0
}

@MainActor private final class Told {
    var values: [Int] = []

    /// Until `count` numbers were told, at most two seconds.
    func wait(for count: Int) async throws {
        for _ in 0..<80 where values.count < count { try await Task.sleep(for: .milliseconds(25)) }
    }
}
