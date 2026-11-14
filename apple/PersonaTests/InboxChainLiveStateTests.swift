import Foundation
import Testing
@testable import Persona

/// Which timeline rows carry an Agent's live state (`InboxEventText.liveStateEntries`): one per linked session, the
/// newest step that links it, and only for `agent` / `agent_stopped` steps with a session link
/// (spec/fixtures/inbox/timeline.json, "live_state").
@MainActor
struct InboxChainLiveStateTests {
    private static func session(_ machine: String, _ session: String) -> InboxItem.Link {
        InboxItem.Link(type: "session", machineID: machine, sessionID: session)
    }

    private static func entry(_ id: String, _ kind: String, link: InboxItem.Link? = nil) -> InboxItem.ChainEntry {
        InboxItem.ChainEntry(id: id, at: nil, kind: kind, text: id, link: link)
    }

    @Test func theNewestStepOfEachSessionCarriesTheState() {
        // The chain reads oldest first: the last step naming a session is its newest.
        let chain = [
            Self.entry("a1", "agent", link: Self.session("m1", "s1")),
            Self.entry("a2", "agent", link: Self.session("m1", "s2")),
            Self.entry("a3", "agent_stopped", link: Self.session("m1", "s1")),
        ]
        #expect(InboxEventText.liveStateEntries(chain) == ["a2", "a3"])
    }

    @Test func onlyAgentStepsWithASessionLinkCount() {
        let chain = [
            // The processing conversation is not an Agent step, whatever session it links.
            Self.entry("h1", "handling", link: Self.session("m1", "s1")),
            // An Agent step without a link, or one that goes to the source or another item.
            Self.entry("a1", "agent"),
            Self.entry("a2", "agent", link: InboxItem.Link(type: "source")),
            Self.entry("a3", "agent_stopped", link: InboxItem.Link(type: "item", id: "other")),
            // A session link missing either half.
            Self.entry("a4", "agent", link: InboxItem.Link(type: "session", machineID: "m1")),
            Self.entry("a5", "agent", link: InboxItem.Link(type: "session", sessionID: "s1")),
            Self.entry("a6", "agent", link: InboxItem.Link(type: "session", machineID: "", sessionID: "")),
            // One real Agent step, so the set is not empty for the wrong reason.
            Self.entry("a7", "agent", link: Self.session("m1", "s1")),
        ]
        #expect(InboxEventText.liveStateEntries(chain) == ["a7"])
    }

    @Test func aSessionOnlyStopsBeingNewestWhenAnotherStepNamesIt() {
        let chain = [
            Self.entry("a1", "agent", link: Self.session("m1", "s1")),
            Self.entry("r1", "received", link: InboxItem.Link(type: "source")),
            Self.entry("w1", "waiting"),
        ]
        // Later steps of other kinds leave the Agent step the newest one for its session.
        #expect(InboxEventText.liveStateEntries(chain) == ["a1"])
        // A chain with no Agent step at all shows no live state.
        #expect(InboxEventText.liveStateEntries([Self.entry("r1", "received")]).isEmpty)
    }

    @Test func theFoldedTimelineShowsNoStateWhoseNewestStepIsFoldedAway() {
        // Five steps, so the folded timeline keeps the newest three; s1's newest step is the second, out of sight.
        let chain = [
            Self.entry("r1", "received"),
            Self.entry("a1", "agent", link: Self.session("m1", "s1")),
            Self.entry("r2", "received"),
            Self.entry("w1", "waiting"),
            Self.entry("r3", "received"),
        ]
        let newest = InboxEventText.liveStateEntries(chain)
        #expect(newest == ["a1"])
        let folded = InboxEventText.chainRows(chain, expanded: false)
        #expect(folded.count == 3)
        #expect(!folded.contains { newest.contains($0.id) })
        #expect(InboxEventText.chainRows(chain, expanded: true).contains { newest.contains($0.id) })
    }
}
