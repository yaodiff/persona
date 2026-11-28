import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A card its task's supervisor holds (`Interaction.supervisorHeld`: `delegating` on a permission, a plan or a terminal
/// dialog, D11 of docs/plans/plan-derived-counts.md, confirmed by the owner on 2026-11-23): it is the supervisor's until
/// `ask_escalate` hands it to the user, so it is no row of the Agents tab's Needs You, counts nowhere, and nothing on the
/// phone answers it (its card, Siri's Approve First, the watch, a notification's buttons). A `delegating` question (the
/// assistant deciding whether it may answer for the user) stays listed and answerable. Made-up values.
@MainActor
struct SupervisorHeldCardTests {
    private func session(_ id: String, state: String = "needs_you", manager: String? = nil) throws -> Session {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        return try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"studio","session_id":"\#(id)","title":"\#(id)","cwd":"/home/user/project","status":"idle","state":"\#(state)","updated_at":"2026-11-10T08:00:00Z","state_since":"2026-11-10T08:00:00Z"\#(stamp)}"#.utf8))
    }

    /// A card of `kind` on `session` ("" for none), created `minute` minutes past eight, `delegating` when held.
    private func card(_ id: String, session: String, kind: String, minute: Int = 0, delegating: Bool? = nil,
                      muted: Bool? = nil, manager: String? = nil, foldedUnder: String? = nil) throws -> Interaction {
        var body: String
        switch kind {
        case "question": body = #","questions":[{"question":"Which port?","options":[{"label":"8080"},{"label":"9090"}]}]"#
        case "screen":
            body = #","screen":"Do you want to proceed?\n\n❯ 1. Yes\n  2. No\n","dialog":{"question":"Do you want to proceed?","options":[{"key":"1","label":"Yes","keys":["1"]},{"key":"2","label":"No","keys":["2"]}],"rev":"5f0c2a9d41b7"}"#
        case "plan": body = ##","plan":"# Plan\n\nRemove the build folder.""##
        default: body = #","tool_name":"Bash","summary":"rmdir build""#
        }
        if let delegating { body += #","delegating":\#(delegating)"# }
        if let muted { body += #","muted":\#(muted)"# }
        if let manager { body += #","manager":"\#(manager)""# }
        if let foldedUnder { body += #","folded_under":"\#(foldedUnder)""# }
        let created = String(format: "2026-11-10T08:%02d:00Z", minute)
        return try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"studio","session_id":"\#(session)","kind":"\#(kind)","created_at":"\#(created)"\#(body)}"#.utf8))
    }

    // MARK: The rule

    @Test func onlyADelegatingCardThatIsNoQuestionIsHeld() throws {
        for kind in ["permission", "plan", "screen"] {
            let held = try card("ix", session: "s", kind: kind, delegating: true)
            #expect(held.supervisorHeld, "\(kind)")
            #expect(!held.answerableHere, "\(kind)")
            #expect(!held.isDelegating, "\(kind): not the assistant deciding about a question")
            let handed = try card("ix", session: "s", kind: kind, delegating: false)
            #expect(!handed.supervisorHeld && handed.answerableHere, "\(kind) handed to the user")
            let never = try card("ix", session: "s", kind: kind)
            #expect(!never.supervisorHeld, "\(kind) never delegated")
        }
        // A delegating question is the assistant's to decide about: listed and answerable, as before.
        let looking = try card("ix", session: "s", kind: "question", delegating: true)
        #expect(looking.isDelegating && !looking.supervisorHeld && looking.answerableHere)
        // The snapshot cache writes cards back: the hold survives it.
        let held = try card("ix", session: "s", kind: "screen", delegating: true)
        #expect(try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(held)).supervisorHeld)
    }

    // MARK: Needs You

    @Test func aSessionWhoseCardsAreAllHeldIsNoRow() throws {
        let waiting = AgentsNeedsYou.waiting([try session("t")])
        #expect(waiting.map(\.sessionID) == ["t"], "its state still says needs_you")
        let rows = AgentsNeedsYou.rows(cards: [try card("ix-terminal", session: "t", kind: "screen", delegating: true)], waiting: waiting)
        #expect(rows.isEmpty)
        #expect(AgentsNeedsYou.badge(rows) { _ in .agents } == 0)
        // Two held cards, a plan and a permission: still no row.
        let two = [try card("ix-plan", session: "t", kind: "plan", delegating: true),
                   try card("ix-perm", session: "t", kind: "permission", minute: 1, delegating: true)]
        #expect(AgentsNeedsYou.rows(cards: two, waiting: waiting).isEmpty)
        // Handed to the user (`ask_escalate` clears delegating), it is a row again.
        let handed = AgentsNeedsYou.rows(cards: [try card("ix-terminal", session: "t", kind: "screen", delegating: false)], waiting: waiting)
        #expect(handed.map(\.id) == ["agent:studio/t"])
        #expect(handed.first?.cards.map(\.id) == ["ix-terminal"])
    }

    @Test func aRowHoldsOnlyTheCardsThatAreNotHeld() throws {
        let waiting = AgentsNeedsYou.waiting([try session("p")])
        let cards = [try card("ix-held-permission", session: "p", kind: "permission", delegating: true),
                     try card("ix-asked", session: "p", kind: "question", minute: 1)]
        let rows = AgentsNeedsYou.rows(cards: cards, waiting: waiting)
        #expect(rows.map(\.id) == ["agent:studio/p"])
        #expect(rows.first?.cards.map(\.id) == ["ix-asked"])
        #expect(AgentsNeedsYou.badge(rows) { _ in .agents } == 1)
        // A delegating question beside a held permission is listed, as held questions are (D11).
        let looking = [try card("ix-held-permission", session: "p", kind: "permission", delegating: true),
                       try card("ix-looking", session: "p", kind: "question", minute: 1, delegating: true)]
        #expect(AgentsNeedsYou.rows(cards: looking, waiting: waiting).first?.cards.map(\.id) == ["ix-looking"])
    }

    @Test func aHeldCardOfASessionNotWaitingOrOnNoSessionIsNoRow() throws {
        let working = AgentsNeedsYou.waiting([try session("w", state: "working")])
        #expect(AgentsNeedsYou.rows(cards: [try card("ix", session: "w", kind: "permission", delegating: true)], waiting: working).isEmpty)
        #expect(AgentsNeedsYou.rows(cards: [try card("ix", session: "", kind: "permission", delegating: true)], waiting: []).isEmpty)
        // Another session waiting in its terminal, no card read, stays a row.
        let other = AgentsNeedsYou.waiting([try session("t"), try session("o")])
        let rows = AgentsNeedsYou.rows(cards: [try card("ix", session: "t", kind: "screen", delegating: true)], waiting: other)
        #expect(rows.map(\.id) == ["agent:studio/o"])
    }

    @Test func theSwitcherLeavesOutASessionWhoseCardsAreAllHeld() throws {
        let roster = ManagerRoster(managers: [HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true)],
                                   defaultID: "assistant")
        func waiting(_ sessions: [Session], _ cards: [Interaction]) -> [String: Int] {
            AssistantBadges.waiting(roster: roster, group: "", sessions: sessions, interactions: cards) { _, _ in true }
        }
        let t = try session("t", manager: "assistant")
        #expect(waiting([t], [try card("ix", session: "t", kind: "screen", delegating: true, manager: "assistant")]).isEmpty)
        #expect(waiting([], [try card("ix", session: "", kind: "permission", delegating: true, manager: "assistant")]).isEmpty)
        #expect(waiting([t], [try card("ix", session: "t", kind: "screen", delegating: true, manager: "assistant"),
                              try card("ix-asked", session: "t", kind: "question", minute: 1, manager: "assistant")]) == ["assistant": 1])
        #expect(waiting([t], []) == ["assistant": 1], "in Needs You with no card read: still its own prompt")
    }

    // MARK: No way to answer it

    @Test func siriNeitherReadsOutNorApprovesAHeldCard() throws {
        let held = try card("held", session: "a", kind: "permission", delegating: true)
        let open = try card("open", session: "b", kind: "permission", minute: 1)
        let sessions = [try session("a"), try session("b")]
        #expect(IntentPicker.visible([held, open], sessions: sessions, mutes: [:]).map(\.id) == ["open"])
        #expect(ApproveFirstIntent.target([held, open], sessions: sessions, mutes: [:])?.id == "open")
        #expect(ApproveFirstIntent.target([held], sessions: sessions, mutes: [:]) == nil)
    }

    @Test func theWatchLeavesOutAHeldCard() throws {
        let cards = [try card("held", session: "a", kind: "screen", delegating: true), try card("open", session: "a", kind: "permission")]
        #expect(WatchInteractions.leavingMuted(cards).map(\.id) == ["open"])
    }

    @Test func aNotificationsButtonDoesNotSettleAHeldCard() throws {
        let cards = [try card("held", session: "a", kind: "permission", delegating: true), try card("open", session: "a", kind: "plan")]
        #expect(!NotificationActions.answerable("held", among: cards))
        #expect(NotificationActions.answerable("open", among: cards))
        #expect(NotificationActions.answerable("unknown", among: cards), "a card the app has not loaded is left to the hub")
    }

    @Test func aNotificationCannotAnswerItsSameIDReplacement() throws {
        let first = try card("same", session: "a", kind: "permission")
        let replacement = try card("same", session: "a", kind: "permission", minute: 1)
        #expect(!NotificationActions.answerable("same", occurrence: first.createdAtWire, among: [replacement]))
        #expect(NotificationActions.answerable("same", occurrence: replacement.createdAtWire, among: [replacement]))
        #expect(NotificationActions.answerable("same", among: [replacement]), "legacy ID-only notifications retain their behavior")
        #expect(NotificationActions.answerable("same", occurrence: first.createdAtWire, among: []),
                "an unloaded card is left to the hub, which rejects a stale occurrence")
    }

    @Test func voiceBarLeavesOutHeldCardsAndPlanFeedback() throws {
        let heldPlan = try card("held-plan", session: "a", kind: "plan", delegating: true)
        let openPlan = try card("open-plan", session: "a", kind: "plan", minute: 1)
        let heldPerm = try card("held-perm", session: "a", kind: "permission", delegating: true)
        let askingQuestion = try card("asking-q", session: "a", kind: "question", minute: 2, delegating: true)
        let idle = try session("a", state: "idle")
        let refused = VoiceRun(id: "voice-1", task: "skip step three", state: .refused, msgIDs: ["voice-1"], baseSeq: 1,
                               startedAt: nil, code: "dialog_open", message: nil, draft: nil, clearable: nil)

        // Held cards are neither announced as pending nor offered as Send Back as Feedback.
        #expect(VoiceBar.answerableCards([heldPlan, openPlan, heldPerm, askingQuestion]).map(\.id) == ["open-plan", "asking-q"])
        #expect(VoiceBar.returnablePlan(id: "held-plan", in: [heldPlan, openPlan]) == nil)
        #expect(VoiceBar.returnablePlan(id: "open-plan", in: [heldPlan, openPlan])?.id == "open-plan")

        let refusedHeldOnly = VoiceBarLine.make(phase: .live(.listening), link: .live, runs: [refused], session: idle,
                                                cards: [heldPlan], muted: false, label: "Claude")
        #expect(refusedHeldOnly.kind == .notSent)
        #expect(refusedHeldOnly.actions == [.dismiss(run: "voice-1")], "held plan is never offered for feedback")

        let refusedMixed = VoiceBarLine.make(phase: .live(.listening), link: .live, runs: [refused], session: idle,
                                             cards: [openPlan, heldPlan], muted: false, label: "Claude")
        #expect(refusedMixed.actions == [.returnPlan(card: "open-plan", feedback: "skip step three", run: "voice-1"), .dismiss(run: "voice-1")])

        let steadyHeldOnly = VoiceBarLine.make(phase: .live(.listening), link: .live, runs: [], session: idle,
                                               cards: [heldPlan, heldPerm], muted: false, asking: "held-plan", label: "Claude")
        #expect(steadyHeldOnly.kind == .ready, "held cards do not raise needsTap or asking")
        let steadyHeldNoAsk = VoiceBarLine.make(phase: .live(.listening), link: .live, runs: [], session: try session("a", state: "working"),
                                                cards: [heldPlan, heldPerm], muted: false, label: "Claude")
        #expect(steadyHeldNoAsk.kind == .working, "held cards do not override a working conversation with needsTap")

        // A delegating question stays listed and answerable (D11).
        let steadyQuestion = VoiceBarLine.make(phase: .live(.listening), link: .live, runs: [], session: idle,
                                               cards: [heldPerm, askingQuestion], muted: false, asking: "asking-q", label: "Claude")
        #expect(steadyQuestion.kind == .asking)
    }

    #if os(iOS)
    private final class ScriptedUrgentOwner: UrgentCallSpeech {
        var lines: [String]
        var said: [String] = []
        init(_ lines: [String]) { self.lines = lines }
        func say(_ text: String) async { said.append(text) }
        func listen() async -> String? { lines.isEmpty ? nil : lines.removeFirst() }
        func stop() {}
        func stopListening() {}
        var canHear: Bool { true }
    }

    @Test func urgentCallLeavesOutAndNeverResolvesAHeldCard() async throws {
        let heldPerm = try card("held-perm", session: "a", kind: "permission", delegating: true)
        let heldPlan = try card("held-plan", session: "a", kind: "plan", minute: 1, delegating: true)
        let heldScreen = try card("held-screen", session: "a", kind: "screen", minute: 2, delegating: true)
        let openPlan = try card("open-plan", session: "a", kind: "plan", minute: 3)
        let delegatingQuestion = try card("q-del", session: "a", kind: "question", minute: 4, delegating: true)
        let all = [heldPerm, heldPlan, heldScreen, openPlan, delegatingQuestion]

        #expect(UrgentCallSession.answerableCard(id: "held-perm", group: "", in: all) == nil)
        #expect(UrgentCallSession.answerableCard(id: "held-plan", group: "", in: all) == nil)
        #expect(UrgentCallSession.answerableCard(id: "held-screen", group: "", in: all) == nil)
        #expect(UrgentCallSession.answerableCard(id: "open-plan", group: "", in: all)?.id == "open-plan")
        #expect(UrgentCallSession.answerableCard(id: "q-del", group: "", in: all)?.id == "q-del")

        let ring = UrgentCallRing(id: "call_held", title: "Supervisor task", manager: "", conversationID: "a", attempt: 1, group: "")

        // Even if initialized with a held plan card, the session drops it, never prompts approval, and never resolves it.
        let plainIntro = UrgentCallSession(ring: ring, voice: ScriptedUrgentOwner([]), card: nil,
                                           resolve: { _, _ in }, ended: { _ in }).introduction()
        let tapSession = UrgentCallSession(ring: ring, voice: ScriptedUrgentOwner([]), card: heldPlan,
                                           resolve: { _, _ in }, ended: { _ in })
        #expect(tapSession.card == nil)
        #expect(tapSession.introduction() == plainIntro, "held plan card never adds an approval prompt to the call introduction")
        tapSession.decide(allow: true)
        #expect(tapSession.pending == nil)
        tapSession.hangUp()

        var heldResolved: [String] = []
        let heldVoice = ScriptedUrgentOwner(["approve", "confirm", "hang up"])
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            let session = UrgentCallSession(ring: ring, voice: heldVoice, card: heldPlan,
                                            resolve: { ix, _ in heldResolved.append(ix.id) },
                                            ended: { _ in done.resume() })
            #expect(session.card == nil)
            session.start()
        }
        #expect(heldResolved.isEmpty)

        // Contrast control: an open plan card is kept and resolved on confirmed approval.
        var openResolved: [(String, Resolution)] = []
        let openVoice = ScriptedUrgentOwner(["approve", "confirm", "hang up"])
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            let session = UrgentCallSession(ring: ring, voice: openVoice, card: openPlan,
                                            resolve: { ix, r in openResolved.append((ix.id, r)) },
                                            ended: { _ in done.resume() })
            #expect(session.card?.id == "open-plan")
            #expect(session.introduction() != plainIntro)
            session.start()
        }
        #expect(openResolved.map(\.0) == ["open-plan"])
        #expect(openResolved.map(\.1) == [Resolution(approve: true, mode: "acceptEdits")])
    }
    #endif

    @Test func watchPetStatusLeavesOutHeldCardsAndHeldOnlySessions() throws {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(secondsFromGMT: 0)!
        let noon = Date(timeIntervalSince1970: 12 * 3600)
        func pet(_ sessions: [Session], _ cards: [Interaction]) -> WatchPetStatus {
            WatchPet.status(sessions: sessions, interactions: cards, unreachable: false, partlyUnreachable: false,
                            now: noon, calendar: utc)
        }

        let t = try session("t")
        let heldScreen = try card("ix-screen", session: "t", kind: "screen", delegating: true)
        let heldPerm = try card("ix-perm", session: "t", kind: "permission", minute: 1, delegating: true)
        let heldPlan = try card("ix-plan", session: "t", kind: "plan", minute: 2, delegating: true)
        // Only supervisor-held cards: not needs_you, treated as working under the supervisor (also through WatchInteractions.forPet).
        #expect(pet([t], [heldScreen]) == WatchPetStatus(state: .working, count: 1))
        #expect(pet([t], [heldPlan, heldPerm]) == WatchPetStatus(state: .working, count: 1))
        #expect(pet([t], WatchInteractions.forPet([heldScreen])) == WatchPetStatus(state: .working, count: 1))

        // A folded child's or side's held card keeps its waiting parent out of needs_you too.
        let parent = try session("lead")
        let childHeld = try card("ix-child-held", session: "worker", kind: "permission", delegating: true, foldedUnder: "lead")
        #expect(pet([parent], [childHeld]) == WatchPetStatus(state: .working, count: 1))

        // Sessionless held card or idle session with a held card: stays idle, never needs_you.
        #expect(pet([], [try card("ix-none", session: "", kind: "permission", delegating: true)]) == WatchPetStatus(state: .idle))
        #expect(pet([try session("i", state: "idle")], [try card("ix-idle", session: "i", kind: "plan", delegating: true)])
                == WatchPetStatus(state: .idle))

        // Handed to the user (`delegating: false`), it is needs_you again.
        #expect(pet([t], [try card("ix-screen", session: "t", kind: "screen", delegating: false)])
                == WatchPetStatus(state: .needsYou, count: 1))

        // Mixed held + open user card (or non-muted delegating question) still counts the session as needs_you once,
        // both directly and through `WatchInteractions.forPet` / `WatchGroupRefresh.apply` (while keeping the
        // delegating question out of `leavingMuted` and the visible watch list).
        let openQ = try card("ix-open", session: "t", kind: "question", minute: 3)
        #expect(pet([t], [heldPerm, openQ]) == WatchPetStatus(state: .needsYou, count: 1))
        let delegatingQ = try card("ix-del-q", session: "t", kind: "question", minute: 4, delegating: true)
        #expect(pet([t], [heldPerm, delegatingQ]) == WatchPetStatus(state: .needsYou, count: 1))
        #expect(WatchInteractions.leavingMuted([heldPerm, delegatingQ]).isEmpty)
        #expect(WatchInteractions.forPet([heldPerm, delegatingQ]).map(\.id) == ["ix-perm", "ix-del-q"])
        #expect(pet([t], WatchInteractions.forPet([heldPerm, delegatingQ])) == WatchPetStatus(state: .needsYou, count: 1))

        // Production watch pipeline (`WatchGroupRefresh.apply` -> visible wrist list + `WatchPet.status`):
        // a non-muted delegating question stays off the wrist list and never buzzes, yet keeps the session needs_you.
        let appliedMixed = WatchGroupRefresh.apply(sessions: [t], interactions: [heldPerm, delegatingQ])
        #expect(appliedMixed.interactions.isEmpty)
        #expect(WatchGroupRefresh.newHapticUids(seen: [], current: appliedMixed.interactions).isEmpty)
        #expect(appliedMixed.petInteractions.map(\.id) == ["ix-perm", "ix-del-q"])
        #expect(pet(appliedMixed.sessions, appliedMixed.petInteractions) == WatchPetStatus(state: .needsYou, count: 1))

        // Contrast: a muted delegating question is excluded from both `leavingMuted` and `forPet`, so a session with
        // a supervisor-held card plus only a muted delegating question stays held-only (`.working`); without a held
        // card the `needs_you` session itself is not misclassified as held-only.
        let mutedDelegatingQ = try card("ix-muted-del-q", session: "t", kind: "question", minute: 5, delegating: true, muted: true)
        #expect(WatchInteractions.leavingMuted([heldPerm, mutedDelegatingQ]).isEmpty)
        #expect(WatchInteractions.forPet([heldPerm, mutedDelegatingQ]).map(\.id) == ["ix-perm"])
        #expect(pet([t], WatchInteractions.forPet([heldPerm, mutedDelegatingQ])) == WatchPetStatus(state: .working, count: 1))
        let appliedMutedDelegating = WatchGroupRefresh.apply(sessions: [t], interactions: [heldPerm, mutedDelegatingQ])
        #expect(appliedMutedDelegating.interactions.isEmpty)
        #expect(pet(appliedMutedDelegating.sessions, appliedMutedDelegating.petInteractions) == WatchPetStatus(state: .working, count: 1))
        #expect(WatchInteractions.forPet([mutedDelegatingQ]).isEmpty)
        #expect(pet([t], WatchInteractions.forPet([mutedDelegatingQ])) == WatchPetStatus(state: .needsYou, count: 1))

        // Contrast through `WatchGroupRefresh.apply`: open user card is visible on the wrist and keeps pet needs_you,
        // while a held-only session leaves the wrist list empty and reads as working under its supervisor.
        let appliedOpen = WatchGroupRefresh.apply(sessions: [t], interactions: [heldPerm, openQ])
        #expect(appliedOpen.interactions.map(\.id) == ["ix-open"])
        #expect(pet(appliedOpen.sessions, appliedOpen.petInteractions) == WatchPetStatus(state: .needsYou, count: 1))
        let appliedHeldOnly = WatchGroupRefresh.apply(sessions: [t], interactions: [heldPerm])
        #expect(appliedHeldOnly.interactions.isEmpty)
        #expect(pet(appliedHeldOnly.sessions, appliedHeldOnly.petInteractions) == WatchPetStatus(state: .working, count: 1))

        // Another needs_you session with no card read stays counted, while the held-only session is excluded.
        let other = try session("o")
        #expect(pet([t, other], [heldScreen]) == WatchPetStatus(state: .needsYou, count: 1))
    }
}
