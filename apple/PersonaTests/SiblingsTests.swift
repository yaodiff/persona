import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// A space's tabs (`AppModel.siblings(of:)`): one per pane, the open one winning its pane, slot order; worked out once
/// per update of the lists, and a view reading them is drawn again only for its own space.
@MainActor
struct SiblingsTests {
    private func session(_ id: String, space: String? = "w1", pane: String? = nil, slot: Int? = nil, state: String = "idle",
                         updated: String? = nil, title: String? = nil, machine: String = "box") throws -> Session {
        var o: [String: Any] = ["machine_id": machine, "session_id": id, "title": title ?? "agent \(id)", "cwd": "/code/\(id)",
                                "status": "idle", "state": state, "started_by_persona": false, "assistant": false,
                                "pending_interactions": 0]
        if let space { o["space_id"] = space }
        if let pane { o["pane_id"] = pane }
        if let slot { o["slot_index"] = slot }
        if let updated { o["updated_at"] = updated }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private func key(_ id: String) -> SessionKey { SessionKey(machineID: "box", sessionID: id) }

    @Test func onePerPaneTheLatestInSlotOrder() throws {
        let spaces = AppModel.liveBySpace([
            try session("b", pane: "p2", slot: 2),
            try session("old", pane: "p1", slot: 1, updated: "2026-11-04T08:00:00Z"),
            try session("new", pane: "p1", slot: 1, updated: "2026-11-04T09:00:00Z"),
            try session("loose", slot: 0),
            try session("gone", pane: "p3", slot: 3, state: "offline"),
            try session("elsewhere", space: "w2", pane: "p9"),
            try session("nowhere", space: nil, pane: "p8"),
        ])
        #expect(spaces["box/w1"]?.map(\.sessionID) == ["loose", "new", "b"])
        #expect(spaces["box/w2"]?.map(\.sessionID) == ["elsewhere"])
        #expect(spaces.count == 2, "offline agents and ones outside a space are not tabs")
    }

    @Test func theOpenOneWinsItsPane() throws {
        let model = AppModel()
        model.adopt(try session("old", pane: "p1", slot: 1, updated: "2026-11-04T08:00:00Z"))
        model.adopt(try session("new", pane: "p1", slot: 1, updated: "2026-11-04T09:00:00Z"))
        model.adopt(try session("b", pane: "p2", slot: 2))
        #expect(model.siblings(of: key("b")).map(\.sessionID) == ["new", "b"])
        #expect(model.siblings(of: key("old")).map(\.sessionID) == ["old", "b"], "open, the older one of the pane is its tab")
        #expect(model.siblings(of: SessionKey(machineID: "box", sessionID: "unknown")).isEmpty)
    }

    @Test func aViewOfOneSpaceIsNotDrawnForAnother() throws {
        let model = AppModel()
        model.adopt(try session("a", pane: "p1", slot: 1))
        model.adopt(try session("x", space: "w2", pane: "p9"))
        nonisolated(unsafe) var changed = 0 // the change handler fires once, synchronously, on the write
        withObservationTracking { _ = model.siblings(of: key("a")) } onChange: { changed += 1 }
        model.adopt(try session("y", space: "w2", pane: "p8"))
        #expect(changed == 0, "another space's new tab")
        model.adopt(try session("c", pane: "p3", slot: 3))
        #expect(changed == 1, "a new tab in its own space")
        #expect(model.siblings(of: key("a")).map(\.sessionID) == ["a", "c"])
    }

    /// Only the spaces a view asked about are worked out at each update (#343): one first asked about after its tabs
    /// changed is current all the same, and follows the updates from then on.
    @Test func aSpaceAskedAboutLaterIsCurrent() throws {
        let model = AppModel()
        model.adopt(try session("a", pane: "p1", slot: 1))
        _ = model.siblings(of: key("a"))
        model.adopt(try session("x", space: "w2", pane: "p9", slot: 2))
        model.adopt(try session("y", space: "w2", pane: "p8", slot: 1))
        #expect(model.siblings(of: key("x")).map(\.sessionID) == ["y", "x"])
        model.adopt(try session("z", space: "w2", pane: "p7", slot: 3))
        #expect(model.siblings(of: key("x")).map(\.sessionID) == ["y", "x", "z"])
        #expect(model.siblings(of: key("a")).map(\.sessionID) == ["a"])
    }
}

/// What only names a machine (`AppModel.namedMachine(_:)`, the open chat's subtitle and its computer activity) is not
/// drawn again by a machine's heartbeat (#1337): it read `machine(_:)`, and every `last_seen` drew the chat again.
@MainActor
struct NamedMachineTests {
    private func machine(_ name: String, seen: String) throws -> Machine {
        try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"box","name":"\#(name)","kind":"local","online":true,"last_seen":"\#(seen)"}"#.utf8))
    }

    @Test func aHeartbeatDoesNotRedrawItARenameDoes() throws {
        let model = AppModel()
        let box = MachineRef(id: "box")
        model.replaceMachines("", [try machine("Box", seen: "2026-11-04T10:00:00Z")])
        nonisolated(unsafe) var changed = 0 // the change handler fires once, synchronously, on the write
        withObservationTracking { _ = model.namedMachine(box)?.title; _ = model.namedMachines } onChange: { changed += 1 }
        model.replaceMachines("", [try machine("Box", seen: "2026-11-04T10:00:05Z")])
        #expect(changed == 0, "a heartbeat")
        #expect(model.namedMachine(box)?.lastSeen == model.machine(box)?.lastSeen, "the data is the latest all the same")
        model.replaceMachines("", [try machine("Studio", seen: "2026-11-04T10:00:10Z")])
        #expect(changed == 1, "a rename")
        #expect(model.namedMachine(box)?.name == "Studio")
    }
}
