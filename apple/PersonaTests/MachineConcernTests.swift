import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The order of Machines (docs/server/machines.md「App」): what needs a look first, otherwise the hub's order.
@MainActor struct MachineConcernTests {
    private func machine(_ id: String, online: Bool = true, mismatch: Bool? = nil) -> Machine {
        Machine(id: id, name: id, kind: "ssh", online: online, nodeMismatch: mismatch)
    }

    @Test func concerns() {
        #expect(MachineConcern.of(machine("a", online: false), alert: .critical, waiting: 3) == .offline)
        #expect(MachineConcern.of(machine("a"), alert: .critical, waiting: 3) == .critical)
        #expect(MachineConcern.of(machine("a"), alert: .warning, waiting: 3) == .warning)
        #expect(MachineConcern.of(machine("a", mismatch: true), alert: .normal, waiting: 1) == .needsYou)
        #expect(MachineConcern.of(machine("a", mismatch: true), alert: .normal, waiting: 0) == .mismatch)
        #expect(MachineConcern.of(machine("a", mismatch: false), alert: .normal, waiting: 0) == nil)
    }

    @Test func mostPressingFirstThenTheHubsOrder() {
        let concern: [String: MachineConcern] = ["calm2": .mismatch, "wait1": .needsYou, "off": .offline, "wait2": .needsYou, "hot": .warning]
        let hub = ["calm1", "wait1", "calm2", "hot", "wait2", "off", "calm3"].map { machine($0) }
        let order = MachineConcern.ordered(hub) { concern[$0.id] }.map(\.id)
        #expect(order == ["off", "hot", "wait1", "wait2", "calm2", "calm1", "calm3"])
    }

    @Test func tally() throws {
        func s(_ state: String) throws -> Session {
            let json = """
            {"machine_id":"m","session_id":"\(state)","title":"t","cwd":"/tmp/p","status":"idle","state":"\(state)"}
            """
            return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        }
        let t = AgentTally(try ["needs_you", "working", "working", "unread", "idle", "unknown"].map(s))
        #expect(t.needsYou == 1 && t.working == 2 && t.idle == 3)
    }

    /// A Reconnect that did not take says why and what to do in plain words, never the raw ssh error (#3837).
    @Test func stillOfflineSaysWhatToDo() {
        func off(_ error: String?, kind: String = "ssh", mismatch: Bool? = nil) -> Machine {
            var m = Machine(id: "a", name: "a", kind: kind, online: false, nodeMismatch: mismatch)
            m.error = error
            return m
        }
        let timeout = MachineDetailView.stillOffline(off("ssh: connect to host x port 22: Operation timed out"))
        #expect(timeout.contains("连接超时") && !timeout.contains("ssh:"))
        #expect(MachineDetailView.stillOffline(off("Permission denied (publickey)")).contains("SSH 密钥"))
        #expect(MachineDetailView.stillOffline(off("Permission denied", kind: "link")).contains("Persona Link"))
        #expect(MachineDetailView.stillOffline(off("connection refused")).contains("node 在运行"))
        #expect(MachineDetailView.stillOffline(off(nil, mismatch: true)).contains("重新部署"))
        #expect(MachineDetailView.stillOffline(off("version mismatch", kind: "local")).contains("本地升级"))
        #expect(MachineDetailView.stillOffline(off("something odd")) == "还没连上，原因见上方卡片")
        #expect(MachineDetailView.stillOffline(off(nil)) == "还没连上，稍后再试一次")
        #expect(MachineDetailView.stillOffline(nil) == "还没连上，稍后再试一次")
    }

    /// An offline card leads with why and what to do, the raw error under it; what it cannot read stays as it was.
    @Test func offlineCardSaysWhyAndWhatToDo() {
        func off(_ error: String?, kind: String = "ssh", code: String? = nil, deploying: Bool? = nil, mismatch: Bool? = nil) -> Machine {
            var m = Machine(id: "a", name: "a", kind: kind, online: false, nodeMismatch: mismatch)
            m.error = error
            m.errorCode = code
            m.deploying = deploying
            return m
        }
        let raw = "ssh: connect to host x port 22: Operation timed out"
        let timeout = AddComputerText.machineLines(off(raw))
        #expect(timeout?.line == "连接超时：网络不稳或电脑在休眠。等它醒来或换个网络，然后重新连接")
        #expect(timeout?.detail == raw)
        #expect(AddComputerText.machineLines(off("Permission denied", kind: "link"))?.line == "认证没通过：在这台电脑上重新配对 Persona Link，然后重新连接")
        #expect(AddComputerText.machineLines(off("connection refused"))?.line.hasPrefix("连不上这台电脑：") == true)
        #expect(AddComputerText.machineLines(off("version mismatch", kind: "local"))?.line.contains("本地升级") == true)
        // A known error code keeps its own words; a deploying one, an unknown error and an online one are as before.
        let coded = AddComputerText.machineLines(off(raw, code: "link_offline"))
        #expect(coded?.line == AddComputerText.machineProblem("link_offline") && coded?.detail == nil)
        #expect(AddComputerText.machineLines(off(raw, deploying: true))?.line == raw)
        #expect(AddComputerText.machineLines(off("something odd"))?.line == "something odd")
        #expect(AddComputerText.machineLines(off("something odd"))?.detail == nil)
        #expect(NodeHealth.offlineReason(off(nil, mismatch: true)) == nil) // its deploy row says it
        var online = off(raw)
        online.online = true
        #expect(NodeHealth.offlineReason(online) == nil)
    }
}
