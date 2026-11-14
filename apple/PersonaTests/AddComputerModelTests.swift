import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Machines › Add Computer (#4196, docs/protocol/protocol.md「Computer pairing codes」「电脑的信任」): the add request, a new code for one that waits,
/// every refusal's words, the computer joining and being confirmed or rejected, machines decoding trust / error_code,
/// and a pending computer kept out of New Agent. Made-up values.
@MainActor
struct AddComputerModelTests {
    private static let start = Date(timeIntervalSince1970: 1_800_000_000)

    private static func code(machine: String = "macbook", name: String = "小林的 MacBook", expires: Date, install: Bool = true) -> String {
        let at = ISO8601DateFormatter().string(from: expires)
        let pair = "persona://link?url=https%3A%2F%2Fhub.example.com&token=jp_example&machine=\(machine)"
        var json = #"{"token":"jp_example","expires_at":"\#(at)","url":"https://hub.example.com","machine":"\#(machine)","#
            + #""name":"\#(name)","trust":"pending","added":true,"pair_url":"\#(pair)","command":"persona-link pair '\#(pair)'""#
        if install { json += #","install":{"linux":"curl -fsSL https://example.com/install.sh | sh -s -- --node --pair '\#(pair)'"}"# }
        return json + "}"
    }

    private static func machines(_ items: String...) -> String { #"{"machines":["# + items.joined(separator: ",") + "]}" }
    private static let waiting = #"{"id":"macbook","name":"小林的 MacBook","kind":"link","online":false,"trust":"pending","error_code":"never_connected","error":"等你在新版 App 或网页里确认这是你的电脑"}"#
    private static let joined = #"{"id":"macbook","name":"小林的 MacBook","kind":"link","online":true,"os":"darwin","trust":"pending","last_seen":"2026-11-06T00:00:00Z"}"#
    private static let confirmed = #"{"id":"macbook","name":"小林的 MacBook","kind":"link","online":true,"os":"darwin"}"#

    @MainActor final class Clock { var now = Date(timeIntervalSince1970: 1_800_000_000) }

    private func model(_ hub: FakeHub, _ clock: Clock = Clock(), _ request: AddComputerRequest = .add,
                       canConfirm: Bool = true) -> AddComputerModel {
        AddComputerModel(request, deps: .init(api: { _ in hub.client }, now: { clock.now }, canConfirm: { _ in canConfirm }))
    }

    private func refusal(_ status: Int, _ code: String, _ message: String = "hub 的原话") -> APIError {
        APIError(status: status, message: message, fields: ["code": code, "error": message])
    }

    @Test func addSendsTheChineseNameTheSystemAndItsIcon() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: Self.start.addingTimeInterval(600)))
        let page = model(hub)
        page.os = .linux
        page.name = "  小林的 MacBook "
        await page.submit()
        let body = try #require(hub.sent("POST", "/api/v1/auth/pair-codes").first?.body)
        let sent = try #require(try JSONSerialization.jsonObject(with: body) as? [String: [String: String]])
        #expect(sent == ["add": ["name": "小林的 MacBook", "os": "linux", "icon": "server"]])
        #expect(page.stage == .waiting && page.machineID == "macbook" && page.secondsLeft == 600)
        let c = try #require(page.code)
        #expect(AddComputerText.linuxCommand(c).installs && AddComputerText.linuxCommand(c).line.hasPrefix("curl -fsSL "))
        #expect(c.pairLink.hasPrefix("persona://link?") && c.trust == "pending" && c.added == true)
    }

    @Test func macAndWindowsAskForTheirIcons() async throws {
        for (os, icon) in [(ComputerOS.mac, "laptop"), (.windows, "desktop")] {
            let hub = FakeHub()
            hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: Self.start.addingTimeInterval(600)))
            let page = model(hub)
            page.os = os
            page.name = "电脑"
            await page.submit()
            let body = try #require(hub.sent("POST", "/api/v1/auth/pair-codes").first?.body)
            let sent = try JSONSerialization.jsonObject(with: body) as? [String: [String: String]]
            #expect(sent?["add"]?["icon"] == icon && sent?["add"]?["os"] == os.rawValue)
        }
    }

    @Test func aNameIsNeededAndAtMostFortyCharacters() async {
        let hub = FakeHub()
        let page = model(hub)
        page.name = "   "
        #expect(page.validName == nil)
        await page.submit()
        #expect(page.failure?.code == "invalid_name" && hub.requests.isEmpty)
        page.name = String(repeating: "电", count: 40)
        #expect(page.validName != nil)
        page.name = String(repeating: "电", count: 41)
        #expect(page.validName == nil)
    }

    @Test func withoutTheInstallLineLinuxPairsAnInstalledLink() throws {
        let data = Data(Self.code(expires: Self.start, install: false).utf8)
        let c = try JSON.decoder.decode(APIClient.ComputerCode.self, from: data)
        let line = AddComputerText.linuxCommand(c)
        #expect(!line.installs && line.line == "persona-link pair '\(c.pairLink)'")
        #expect(AddComputerText.windowsCommand(c) == #".\persona-link.exe install --personad .\personad.exe --pair '"# + c.pairLink + "'")
    }

    @Test func anExpiredCodeIsMadeAgainForTheSameComputer() async throws {
        let hub = FakeHub(), clock = Clock()
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: Self.start.addingTimeInterval(90)))
        let page = model(hub, clock)
        page.name = "小林的 MacBook"
        await page.submit()
        clock.now = Self.start.addingTimeInterval(30.4)
        page.tick()
        #expect(page.secondsLeft == 60 && !page.expired)
        clock.now = Self.start.addingTimeInterval(91)
        page.tick()
        #expect(page.expired && page.secondsLeft == 0)
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: clock.now.addingTimeInterval(600)))
        await page.reissue()
        let again = try #require(hub.sent("POST", "/api/v1/auth/pair-codes").last?.body)
        #expect(try JSONSerialization.jsonObject(with: again) as? [String: String] == ["machine": "macbook"])
        #expect(page.stage == .waiting && !page.expired && page.secondsLeft == 600) // the countdown starts again
    }

    @Test func aClockThatIsOffCountsTenMinutesFromNow() throws {
        let past = try JSON.decoder.decode(APIClient.ComputerCode.self, from: Data(Self.code(expires: Self.start.addingTimeInterval(-5)).utf8))
        #expect(AddComputerText.expiry(past, now: Self.start) == Self.start.addingTimeInterval(600))
        let far = try JSON.decoder.decode(APIClient.ComputerCode.self, from: Data(Self.code(expires: Self.start.addingTimeInterval(7200)).utf8))
        #expect(AddComputerText.expiry(far, now: Self.start) == Self.start.addingTimeInterval(600))
    }

    @Test func everyRefusalHasItsWords() {
        let words: [(Int, String)] = [(403, "forbidden"), (409, "mesh_member"), (429, "too_many_pending"), (409, "not_pending"),
                                      (404, "not_found"), (400, "invalid_name"), (400, "invalid_os")]
        var seen = Set<String>()
        for (status, code) in words {
            let f = AddComputerText.failure(refusal(status, code))
            #expect(f.code == code)
            #expect(!f.message.isEmpty && f.message != "hub 的原话", "\(code) has no words of its own")
            seen.insert(f.message)
        }
        #expect(seen.count == words.count, "two codes say the same")
        #expect(AddComputerText.failure(refusal(429, "too_many_pending")).message.contains("3 台"))
        // tls_required: the hub's plain words (in the user's language); ours only when it sent none.
        #expect(AddComputerText.failure(refusal(409, "tls_required", "hub 还没打开安全连接")).message == "hub 还没打开安全连接")
        #expect(AddComputerText.failure(refusal(409, "tls_required", "")).message.contains("安全连接"))
        // A code this app does not know: the hub's own words.
        #expect(AddComputerText.failure(refusal(409, "something_new", "新的原因")).message == "新的原因")
    }

    @Test func aRefusedAddStaysOnTheFormWithTheWords() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes") { _ in throw self.refusal(429, "too_many_pending") }
        let page = model(hub)
        page.name = "第四台"
        await page.submit()
        #expect(page.stage == .form && page.failure?.code == "too_many_pending" && page.code == nil)
    }

    @Test func itJoinsThenIsConfirmed() async throws {
        let hub = FakeHub(), clock = Clock()
        var refreshed = 0
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: Self.start.addingTimeInterval(600)))
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.waiting))
        let page = AddComputerModel(.add, deps: .init(api: { _ in hub.client }, now: { clock.now }, refreshed: { refreshed += 1 }))
        page.name = "小林的 MacBook"
        await page.submit()
        await page.poll()
        #expect(page.stage == .waiting && page.machine?.neverConnected == true)
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.joined))
        await page.poll()
        #expect(page.stage == .joined && page.machine.flatMap(AddComputerText.systemName) == "macOS")
        hub.on("POST", "/api/v1/machines/macbook/confirm", json: #"{"ok":true}"#)
        await page.confirm()
        #expect(page.stage == .confirmed && refreshed == 1)
        #expect(hub.sent("POST", "/api/v1/machines/macbook/confirm").count == 1)
    }

    @Test func rejectedItIsGone() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.joined))
        hub.on("POST", "/api/v1/machines/macbook/reject", json: #"{"ok":true}"#)
        let page = model(hub, Clock(), .confirm(MachineRef(group: "", id: "macbook")))
        await page.poll()
        #expect(page.stage == .joined)
        await page.reject()
        #expect(page.stage == .rejected && hub.sent("POST", "/api/v1/machines/macbook/reject").count == 1)
    }

    @Test func confirmAndRejectRefusals() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.joined))
        hub.on("POST", "/api/v1/machines/macbook/reject") { _ in throw self.refusal(409, "not_pending") }
        let page = model(hub, Clock(), .confirm(MachineRef(group: "", id: "macbook")))
        await page.poll()
        await page.reject()
        #expect(page.stage == .confirmed) // confirmed elsewhere meanwhile
        let other = model(hub, Clock(), .confirm(MachineRef(group: "", id: "macbook")))
        await other.poll()
        hub.on("POST", "/api/v1/machines/macbook/confirm") { _ in throw self.refusal(404, "not_found") }
        await other.confirm()
        #expect(other.stage == .gone)
    }

    @Test func confirmingOneThatIsNotOnlineOffersANewLinkOnlyIfItNeverConnected() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.waiting))
        let page = model(hub, Clock(), .confirm(MachineRef(group: "", id: "macbook")))
        await page.poll()
        #expect(page.stage == .offline && page.canReissue)
        let asleep = #"{"id":"macbook","name":"小林的 MacBook","kind":"link","online":false,"trust":"pending","error_code":"link_offline","last_seen":"2026-11-06T00:00:00Z"}"#
        hub.on("GET", "/api/v1/machines", json: Self.machines(asleep))
        await page.poll()
        #expect(page.stage == .offline && !page.canReissue)
        hub.on("GET", "/api/v1/machines", json: Self.machines())
        await page.poll()
        #expect(page.stage == .gone) // rejected elsewhere, or it waited too long
    }

    @Test func aHubThatCannotConfirmGoesStraightToAdded() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.code(expires: Self.start.addingTimeInterval(600)))
        hub.on("GET", "/api/v1/machines", json: Self.machines(Self.joined))
        let page = model(hub, Clock(), canConfirm: false)
        page.name = "小林的 MacBook"
        await page.submit()
        await page.poll()
        #expect(page.stage == .confirmed)
    }

    @Test func machinesDecodeTrustAndErrorCode() throws {
        let list = try JSON.decoder.decode(APIClient.MachinesResponse.self,
                                           from: Data(Self.machines(Self.waiting, Self.confirmed, #"{"id":"x","name":"X","kind":"ssh","online":false,"error_code":"new_code","error":"原话"}"#).utf8)).machines
        #expect(list[0].trust == "pending" && list[0].isPending && list[0].errorCode == "never_connected" && list[0].neverConnected)
        #expect(list[1].trust == nil && !list[1].isPending && list[1].errorCode == nil)
        // An unknown code shows the hub's error.
        #expect(list[2].errorCode == "new_code" && AddComputerText.machineProblem(list[2].errorCode) == nil && AddComputerText.machineLines(list[2])?.line == "原话")
        for code in ["node_service_down", "link_offline", "never_connected", "node_outdated"] {
            #expect(AddComputerText.machineProblem(code)?.isEmpty == false, "\(code)")
        }
        #expect(AddComputerText.machineProblem("link_offline")?.contains("Persona Link") == true)
        #expect(MachineConcern.of(list[0], alert: .normal, waiting: 0) == .pending)
    }

    @Test func aPendingComputerIsNotOfferedForANewAgent() throws {
        let list = try JSON.decoder.decode(APIClient.MachinesResponse.self, from: Data(Self.machines(Self.joined,
            #"{"id":"studio","name":"Mac Studio","kind":"ssh","online":true}"#).utf8)).machines
        #expect(NewChatView.offered(list, onlyGroup: nil).map(\.id) == ["studio"])
        #expect(NewChatView.offered(list, onlyGroup: "other").isEmpty)
    }

    @Test func theComputerToConfirm() throws {
        let list = try JSON.decoder.decode(APIClient.MachinesResponse.self, from: Data(Self.machines(
            #"{"id":"a","name":"A","kind":"link","online":false,"trust":"pending"}"#, Self.joined, Self.confirmed.replacingOccurrences(of: "macbook", with: "c")).utf8)).machines
        #expect(list.filter(\.isPending).map(\.id) == ["a", "macbook"])
        #expect(AddComputerText.pendingToConfirm(list)?.id == "macbook") // the one online can be asked about now
        #expect(AddComputerText.pendingToConfirm(list, preferred: "a")?.id == "a")
        #expect(AddComputerText.pendingToConfirm(list, preferred: "c")?.id == "macbook") // c is confirmed already
        #expect(ComputerOS(reported: "darwin") == .mac && ComputerOS(reported: "linux") == .linux && ComputerOS(reported: nil) == nil)
    }
}
