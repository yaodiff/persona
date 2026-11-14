import Foundation
import Testing
@testable import Persona
import PersonaWire

/// #6272: the watch's Milk Frog follows the agents, and never reads as calm without knowing.
struct WatchPetTests {
    private func session(_ id: String, _ state: String, group: String = "") throws -> Session {
        let o: [String: Any] = ["machine_id": "m", "session_id": id, "title": id, "cwd": "/code", "status": "idle", "state": state,
                                "started_by_persona": false, "assistant": false, "pending_interactions": 0]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func card(_ id: String, session: String) -> Interaction {
        Interaction(id: id, machineID: "m", sessionID: session, kind: .question)
    }

    private var calendar: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(secondsFromGMT: 0)!
        return c
    }

    private func at(hour: Int) -> Date { Date(timeIntervalSince1970: Double(hour) * 3600) }

    private func status(_ sessions: [Session] = [], cards: [Interaction] = [], unreachable: Bool = false,
                        partly: Bool = false, hour: Int = 12) -> WatchPetStatus {
        WatchPet.status(sessions: sessions, interactions: cards, unreachable: unreachable, partlyUnreachable: partly,
                        now: at(hour: hour), calendar: calendar)
    }

    @Test func aWaitingAgentComesFirstAndCountsOncePerAgent() throws {
        let all = [try session("a", "working"), try session("b", "needs_you")]
        #expect(status(all, cards: [card("1", session: "a"), card("2", session: "a"), card("3", session: "b")])
                == WatchPetStatus(state: .needsYou, count: 2))
    }

    @Test func workingComesBeforeFinished() throws {
        let all = [try session("a", "unread"), try session("b", "working"), try session("c", "working")]
        #expect(status(all) == WatchPetStatus(state: .working, count: 2))
        #expect(status([try session("a", "unread")]) == WatchPetStatus(state: .done, count: 1))
    }

    @Test func nothingGoingOnSleepsAtNight() throws {
        let quiet = [try session("a", "idle")]
        #expect(status(quiet, hour: 12).state == .idle)
        #expect(status(quiet, hour: 6).state == .sleep)
        #expect(status(quiet, hour: 7).state == .idle)
        #expect(status(quiet, hour: 22).state == .idle)
        #expect(status(quiet, hour: 23).state == .sleep)
    }

    @Test func noHubAnsweringIsOfflineWhateverTheOldDataSays() throws {
        #expect(status([try session("a", "working")], unreachable: true).state == .offline)
    }

    @Test func aHubOutOfReachIsNeverCalmButWhatTheOthersSayStands() throws {
        #expect(status([try session("a", "idle")], partly: true).state == .offline)
        #expect(status([try session("a", "unread")], partly: true).state == .offline)
        #expect(status([try session("a", "working")], partly: true).state == .working)
        #expect(status(cards: [card("1", session: "a")], partly: true).state == .needsYou)
    }

    /// Counted states use the one set of state names (#3960), not words of the frog's own.
    @Test func countsUseTheSharedStateNames() {
        #expect(WatchPetStatus(state: .needsYou, count: 2).title == StateName.count(.needsYou, 2))
        #expect(WatchPetStatus(state: .working, count: 3).title == StateName.count(.working, 3))
        #expect(WatchPetStatus(state: .done, count: 1).title == StateName.count(.unread, 1))
    }

    private func manifest() -> WatchPetManifest {
        let hash = String(repeating: "a", count: 64)
        let clip = WatchPetManifest.Clip(file: "pet/x.png", sha256: hash, fps: 10, frames: 4, still: 1)
        let face = WatchPetManifest.Face(file: "pet/f.png", sha256: hash)
        var clips = [WatchPet.tapClip: clip]
        var faces: [String: WatchPetManifest.Face] = [:]
        for s in WatchPetState.allCases { clips[s.clip] = clip; faces[s.clip] = face }
        return WatchPetManifest(format: WatchPetManifest.currentFormat, clips: clips, faces: faces)
    }

    @Test func aManifestMustCoverEveryState() {
        #expect(manifest().isUsable)
        var m = manifest()
        m.clips["sleep"] = nil
        #expect(!m.isUsable)
        m = manifest()
        m.faces["offline"] = nil
        #expect(!m.isUsable)
        m = manifest()
        m.clips[WatchPet.tapClip] = nil
        #expect(!m.isUsable)
    }

    @Test func aManifestFromAnotherFormatOrWithABadHashIsNotUsed() {
        var m = manifest()
        m.format = 2
        #expect(!m.isUsable)
        m = manifest()
        m.clips["idle"]?.sha256 = "../../evil"
        #expect(!m.isUsable)
        m = manifest()
        m.clips["idle"]?.still = 4
        #expect(!m.isUsable)
    }

    /// The package the hub really serves: what the watch would fetch.
    @Test func theShippedManifestIsUsable() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let package = root.appending(path: "server/internal/hub/personas").appending(path: PersonaSelection.packageID)
        let m = try JSONDecoder().decode(WatchPetManifest.self, from: Data(contentsOf: package.appending(path: "pet/pet.json")))
        #expect(m.isUsable)
        for file in m.clips.values.map(\.file) + m.faces.values.map(\.file) {
            #expect(FileManager.default.fileExists(atPath: package.appending(path: file).path))
        }
    }
}
