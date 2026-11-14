import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Re-read session rows land where they were (#4683): the same rows again leave the lists equal, so nothing that reads
/// the sessions redraws, and the fan-out behind `sessionsByMachine` does not run.
struct SessionRowsInPlaceTests {
    private func session(_ id: String, title: String? = nil, machine: String = "m") -> Session {
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(title ?? id)","cwd":"/tmp/p","status":"idle","state":"idle"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func ids(_ rows: [Session]) -> [String] { rows.map(\.sessionID) }

    @Test func aReReadRowKeepsItsPlace() {
        let list = [session("a"), session("b"), session("c")]
        #expect(AppModel.replacing(session("b"), in: list) == list)
        let renamed = AppModel.replacing(session("b", title: "new"), in: list)
        #expect(ids(renamed) == ["a", "b", "c"])
        #expect(renamed[1].title == "new")
    }

    @Test func aRowTheListLacksGoesAtTheEnd() {
        #expect(ids(AppModel.replacing(session("d"), in: [session("a"), session("b")])) == ["a", "b", "d"])
        #expect(ids(AppModel.replacing(session("a"), in: [])) == ["a"])
    }

    @Test func otherCopiesOfTheRowAreDropped() {
        let list = [session("a"), session("b"), session("a"), session("c")]
        #expect(ids(AppModel.replacing(session("a", title: "new"), in: list)) == ["a", "b", "c"])
    }

    @Test func theSameSummariesAgainLeaveTheListEqual() {
        let old = [session("a"), session("b"), session("c")]
        #expect(AppModel.merging([session("c"), session("a")], into: old) == old)
    }

    @Test func summariesReplaceInPlaceAndNewOnesFollow() {
        let old = [session("a"), session("b"), session("c")]
        let merged = AppModel.merging([session("e"), session("b", title: "new"), session("d")], into: old)
        #expect(ids(merged) == ["a", "b", "c", "e", "d"])
        #expect(merged[1].title == "new")
    }

    @Test func eachSessionLandsOnce() {
        let merged = AppModel.merging([session("b", title: "first"), session("b", title: "second"), session("d"), session("d")],
                                      into: [session("a"), session("b")])
        #expect(ids(merged) == ["a", "b", "d"])
        #expect(merged[1].title == "first")
    }

    @Test func machinesStayApart() {
        let old = [session("a", machine: "m1"), session("a", machine: "m2")]
        let merged = AppModel.merging([session("a", title: "new", machine: "m2")], into: old)
        #expect(merged.map(\.machineID) == ["m1", "m2"])
        #expect(merged[0].title == "a")
        #expect(merged[1].title == "new")
    }
}
