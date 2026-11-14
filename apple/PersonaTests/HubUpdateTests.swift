import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Settings › hub and the line over the Agents list (docs/operations/hub-update.md「In the App」), against a FakeHub.
@MainActor
struct HubUpdateTests {
    static let available = #"{"version":"v1-202611021500","build":202611021500,"channel":"release","can_update":true,"#
        + #""latest":{"version":"v2-202611021700","build":202611021700,"released":"2026-11-02T09:00:00Z","notes":"更快的推送","newer":true,"bad":false},"#
        + #""state":"available","busy":[],"auto":false,"auto_notice":false,"last_check":"2026-11-02T09:05:00.123456789Z","confirmed":true}"#
    static let waiting = #"{"version":"v1","build":202611021500,"channel":"release","can_update":true,"#
        + #""latest":{"version":"v2","build":202611021700,"newer":true,"bad":false},"state":"waiting_idle","#
        + #""busy":[{"machine_id":"box","machine":"Box","session_id":"s1","title":"migrate the db"}],"auto":true,"auto_notice":false,"confirmed":true}"#

    private func model(_ hub: FakeHub, published: @escaping (HubUpdateStatus) -> Void = { _ in }) -> HubUpdateModel {
        HubUpdateModel(deps: HubUpdateDeps(api: { hub.client }, report: { _ in }, publish: published))
    }

    @Test func decodesTheStatus() throws {
        let s = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.available.utf8))
        #expect(s.installable && s.latest?.version == "v2-202611021700" && s.lastCheck != nil && s.channelTitle == "发布版")
        let w = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.waiting.utf8))
        #expect(w.working && w.busy.first?.title == "migrate the db" && w.stateTitle == "等 1 个在忙的结束")
    }

    @Test func loadsChecksAndUpdates() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/hub/update", json: Self.available)
        hub.on("GET", "/api/v1/hub/update/history", json: #"{"history":[{"from_version":"v0","to_version":"v1","to_build":202611021500,"result":"updated","auto":true}]}"#)
        hub.on("POST", "/api/v1/hub/update/check", json: Self.available)
        hub.on("POST", "/api/v1/hub/update", json: Self.waiting)
        var published: [HubUpdateStatus] = []
        let m = model(hub) { published.append($0) }
        await m.load()
        #expect(m.status?.installable == true && m.history.first?.resultTitle == "已更新" && published.count == 1)
        await m.check()
        #expect(hub.sent("POST", "/api/v1/hub/update/check").count == 1)
        await m.update(now: false)
        #expect(m.status?.state == "waiting_idle")
        await m.update(now: true) // the second confirmation's Update Now
        let bodies = hub.sent("POST", "/api/v1/hub/update").compactMap { $0.body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Bool] } }
        #expect(bodies.map { $0["now"] } == [false, true])
    }

    @Test func autoUpdateAndTheNotice() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/hub/update/settings") { req in
            let body = (try? JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Bool]) ?? [:]
            let auto = body["auto"] ?? true
            return Data((#"{"version":"v1","build":1,"channel":"release","can_update":true,"state":"idle","busy":[],"auto":"# + (auto ? "true" : "false")
                + #","auto_notice":false,"confirmed":true}"#).utf8)
        }
        let m = model(hub)
        await m.setAuto(false)
        #expect(m.status?.auto == false)
        await m.noticeSeen()
        let sent = hub.sent("PUT", "/api/v1/hub/update/settings").compactMap { $0.body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Bool] } }
        #expect(sent == [["auto": false], ["auto_notice_seen": true]])
    }

    /// A hub from before updates in the app answers 404: the page says so instead of failing.
    @Test func anOlderHub() async {
        let hub = FakeHub()
        let m = model(hub)
        await m.load()
        #expect(m.unsupported && m.loaded)
    }

    /// A timeout or no network says so instead of spinning for good; a second load reads it (#3484).
    @Test func anUnreachableHubSaysSo() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/hub/update") { _ in throw URLError(.timedOut) }
        let m = model(hub)
        await m.load()
        #expect(m.status == nil && m.loaded && !m.unsupported)
        #expect(m.error == HubUpdateModel.unreachable)
        hub.on("GET", "/api/v1/hub/update", json: Self.available)
        await m.load()
        #expect(m.status?.installable == true && m.error == nil)
    }

    /// Not connected to a hub says so; a cancelled load (the page went away) says nothing.
    @Test func noHubAndACancelledLoad() async {
        let none = HubUpdateModel(deps: HubUpdateDeps(api: { nil }, report: { _ in }, publish: { _ in }))
        await none.load()
        #expect(none.error == HubUpdateModel.noHub && none.loaded)
        let hub = FakeHub()
        hub.on("GET", "/api/v1/hub/update") { _ in throw CancellationError() }
        let m = model(hub)
        await m.load()
        #expect(m.status == nil && m.error == nil)
    }

    @Test func compatibility() {
        #expect(HubCompat.of(nil, appBuild: 100) == .ok)
        #expect(HubCompat.of(HubHello(protocolVersion: 1, minAppBuild: 0), appBuild: 100) == .ok)
        #expect(HubCompat.of(HubHello(), appBuild: 100) == .hubTooOld) // a hub from before protocols
        #expect(HubCompat.of(HubHello(protocolVersion: 1, minAppBuild: 120), appBuild: 100) == .appTooOld)
    }

    @Test func theLineOverTheAgentsList() throws {
        let avail = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.available.utf8))
        let offered = HubNotice.available(version: "v2-202611021700", build: 202611021700)
        #expect(HubNotice.of(avail, compat: .ok, dismissed: []) == offered)
        #expect(HubNotice.of(avail, compat: .ok, dismissed: [offered.dismissKey!]) == nil) // closed for this build
        var auto = avail
        auto.auto = true
        #expect(HubNotice.of(auto, compat: .ok, dismissed: []) == nil) // it will install by itself
        var held = auto
        held.latest?.held = "新版本不包含这个 hub 正在运行的提交"
        #expect(HubNotice.of(held, compat: .ok, dismissed: []) == offered) // held back: it will not, so it is offered
        auto.autoNotice = true
        #expect(HubNotice.of(auto, compat: .ok, dismissed: []) == .autoOn) // told once
        let waiting = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.waiting.utf8))
        #expect(HubNotice.of(waiting, compat: .ok, dismissed: []) == .updating("等 1 个在忙的结束"))
        var back = avail
        (back.state, back.error) = ("rolled_back", "更新到 v2 没成功，已回到 v1：box 没连回来")
        let wentBack = HubNotice.rolledBack("更新到 v2 没成功，已回到 v1：box 没连回来", build: 202611021700)
        #expect(HubNotice.of(back, compat: .ok, dismissed: []) == wentBack)
        // Closing one line lets the next one through, and closing that does not bring the first back.
        #expect(HubNotice.of(back, compat: .ok, dismissed: [wentBack.dismissKey!]) == offered)
        #expect(HubNotice.of(back, compat: .ok, dismissed: [wentBack.dismissKey!, offered.dismissKey!]) == nil)
        #expect(HubNotice.of(nil, compat: .appTooOld, dismissed: []) == .appTooOld)
        #expect(HubNotice.appTooOld.dismissKey == nil) // cannot be closed
        #expect(HubNotice.of(nil, compat: .hubTooOld, dismissed: []) == .hubTooOld)
        #expect(HubNotice.of(nil, compat: .hubTooOld, dismissed: ["hubTooOld"]) == nil)
        var bad = avail
        bad.latest?.bad = true
        #expect(HubNotice.of(bad, compat: .ok, dismissed: []) == nil) // went back before: not offered again
    }

    /// A release auto-update holds back says why (its code may not contain the hub's commit), and so does the history.
    @Test func aHeldRelease() throws {
        let json = Self.available.replacingOccurrences(of: #""bad":false}"#, with: #""bad":false,"held":"新版本不包含它"}"#)
        let s = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(json.utf8))
        #expect(s.latest?.held == "新版本不包含它")
        #expect(try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.available.utf8)).latest?.held == nil)
        let r = try JSON.decoder.decode(HubUpdateRecord.self, from: Data(#"{"from_version":"v1","to_version":"v2","to_build":202611021700,"result":"held","reason":"新版本不包含它","auto":true}"#.utf8))
        #expect(r.resultTitle == "没有自动装")
    }

    /// A Docker hub: a newer image gets a line (no Update, the page says what to run), closed per version.
    @Test func aDockerHub() throws {
        let docker = try JSON.decoder.decode(HubUpdateStatus.self, from: Data(Self.docker.utf8))
        #expect(docker.image == .init(name: "ghcr.io/owner/persona", tag: "0.3.0", pinned: true))
        #expect(HubNotice.of(docker, compat: .ok, dismissed: []) == .newImage(version: "0.4.0"))
        #expect(HubNotice.of(docker, compat: .ok, dismissed: ["image:0.4.0"]) == nil)
        #expect(HubNotice.of(docker, compat: .ok, dismissed: ["image:0.3.9"]) == .newImage(version: "0.4.0"))
        var current = docker
        current.latest?.newer = false
        #expect(HubNotice.of(current, compat: .ok, dismissed: []) == nil)
    }

    static let docker = #"{"version":"0.3.0","build":0,"channel":"docker","can_update":false,"why_not":"Docker 里的 hub 由新镜像更新","latest":{"version":"0.4.0","build":0,"newer":true,"bad":false},"state":"available","busy":[],"auto":false,"auto_notice":false,"confirmed":true,"image":{"name":"ghcr.io/owner/persona","tag":"0.3.0","pinned":true}}"#

    @Test func eventsCarryItLive() {
        guard case .hello(let info)? = ServerEvent.decode(Data(#"{"type":"hello","features":["x"],"channel":"release","build":202611021500,"protocol":1,"min_app_build":0}"#.utf8)) else {
            Issue.record("hello"); return
        }
        #expect(info == HubHello(features: ["x"], channel: "release", build: 202611021500, protocolVersion: 1, minAppBuild: 0))
        guard case .hubUpdate(let s)? = ServerEvent.decode(Data((#"{"type":"hub_update","update":"# + Self.waiting + "}").utf8)) else {
            Issue.record("hub_update"); return
        }
        #expect(s.state == "waiting_idle")
    }

    @Test func skills() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/skills", json: #"{"machine":"服务器","skills":[{"name":"lark-gateway","description":"飞书","source":"user"}]}"#)
        let m = SkillsModel(api: { hub.client })
        await m.load()
        #expect(m.skills.map(\.name) == ["lark-gateway"] && m.machine == "服务器")
    }

    /// Skills that cannot be read say why: not connected, the hub out of reach, the assistant's machine too slow, or
    /// the hub's own reason; a cancelled load says nothing (#4274).
    @Test func skillsSayWhyTheyCannotBeRead() async {
        let none = SkillsModel(api: { nil })
        await none.load()
        #expect(none.loaded && none.error == SkillsModel.noHub)
        let hub = FakeHub()
        let m = SkillsModel(api: { hub.client })
        hub.on("GET", "/api/v1/skills") { _ in throw URLError(.timedOut) }
        await m.load()
        #expect(m.loaded && m.error == SkillsModel.unreachable)
        hub.on("GET", "/api/v1/skills") { _ in throw APIError(status: 502, message: "context deadline exceeded") }
        await m.load()
        #expect(m.error == SkillsModel.machineSlow)
        hub.on("GET", "/api/v1/skills") { _ in throw APIError(status: 409, message: "还没设置助手跑在哪台电脑上") }
        await m.load()
        #expect(m.error == "还没设置助手跑在哪台电脑上")
        hub.on("GET", "/api/v1/skills", json: #"{"machine":"服务器","skills":[]}"#)
        await m.load()
        #expect(m.error == nil && m.skills.isEmpty)
        let cancelled = SkillsModel(api: { hub.client })
        hub.on("GET", "/api/v1/skills") { _ in throw CancellationError() }
        await cancelled.load()
        #expect(!cancelled.loaded && cancelled.error == nil)
    }
}
