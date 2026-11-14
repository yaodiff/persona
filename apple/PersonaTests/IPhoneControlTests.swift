import Foundation
import PersonaKit
import Testing
@testable import Persona
import PersonaWire

/// Phone control in the app (docs/extensions/iphone.md): what the hub sends decodes, this iPhone finds itself among the
/// iPhones the computers list only by an exact model and version (several alike: the user picks; none: no switch),
/// the consent goes out only for the iPhone matched, and the tools read as steps. Made-up values.
@MainActor
struct IPhoneControlTests {
    private static let list = #"""
    {"on":true,"devices":[
     {"id":"dev-1","name":"我的 iPhone","product_type":"iPhone18,1","ios":"26.1","paired":true,"connected":true,"transport":"network",
      "locked":false,"ready":true,"allowed":false,"machines":[{"id":"macbook","name":"MacBook","enabled":true}],"consented":false},
     {"id":"dev-2","name":"Other iPhone","product_type":"iPhone14,5","ios":"18.6","paired":true,"connected":false,"note":"没有连上",
      "machines":[{"id":"studio","name":"Mac Studio","enabled":false}],"consented":true},
     {"id":"dev-3","name":"Work iPhone","product_type":"iPhone18,1","ios":"26.1.0","usb":true,"machines":[{"id":"macbook","name":"MacBook"}]}]}
    """#

    private func decoded() throws -> IPhoneList {
        try JSON.decoder.decode(IPhoneList.self, from: Data(Self.list.utf8))
    }

    @Test func decodes() throws {
        let l = try decoded()
        #expect(l.on && l.devices.count == 3)
        let me = try #require(l.devices.first)
        #expect(me.device.productType == "iPhone18,1" && me.device.connected && me.device.transport == "network")
        #expect(me.machines.first?.name == "MacBook" && me.macOn && !me.consented)
        #expect(l.devices[2].device.usb && !l.devices[2].device.paired && l.devices[2].machines.first?.enabled == false)
        // A computer's state as the machine list carries it; a job of a kind this app does not know still decodes.
        let p = try JSON.decoder.decode(MachineIPhone.self, from: Data(#"{"enabled":true,"installed":true,"devices":[],"job":{"kind":"pair","state":"running"}}"#.utf8))
        #expect(p.enabled && p.installed && p.job?.running == true)
        #expect(try JSON.decoder.decode(MachineIPhone.self, from: Data("{}".utf8)).devices.isEmpty)
    }

    @Test func matchesOnlyTheSameModelAndVersion() throws {
        let all = try decoded().devices
        // Two alike (dev-1 and dev-3, "26.1" and "26.1.0" being the same): the user picks.
        #expect(IPhoneIdentity.match(all, model: "iPhone18,1", ios: "26.1.0", picked: nil) == .choose([all[0], all[2]]))
        if case .this(let me, let alike) = IPhoneIdentity.match(all, model: "iPhone18,1", ios: "26.1", picked: "dev-3") {
            #expect(me.id == "dev-3" && alike == 2)
        } else {
            Issue.record("picked")
        }
        if case .this(let me, let alike) = IPhoneIdentity.match(all, model: "iPhone14,5", ios: "18.6", picked: nil) {
            #expect(me.id == "dev-2" && alike == 1)
        } else {
            Issue.record("one alike")
        }
        // Fail-closed: another version, another model, no model, or a pick that does not look like this iPhone.
        #expect(IPhoneIdentity.match(all, model: "iPhone18,1", ios: "26.2", picked: nil) == .none)
        #expect(IPhoneIdentity.match(all, model: "iPhone17,1", ios: "26.1", picked: nil) == .none)
        #expect(IPhoneIdentity.match(all, model: "", ios: "26.1", picked: nil) == .none)
        #expect(IPhoneIdentity.match(all, model: "iPhone17,1", ios: "26.1", picked: "dev-1") == .none)
        #expect(IPhoneIdentity.same("26", "26.0.0") && !IPhoneIdentity.same("26.1", "26.10") && !IPhoneIdentity.same("", ""))
    }

    @Test func consentGoesOutOnlyForTheIPhoneMatched() async throws {
        let l = try decoded()
        var sent: [(String, Bool)] = []
        var stored: String?
        func model(_ ios: String) -> IPhoneConsentModel {
            IPhoneConsentModel(deps: .init(list: { l }, consent: { device, allowed in
                sent.append((device, allowed))
                var seen = l.devices.first { $0.id == device }!
                seen.consented = allowed
                return seen
            }, model: "iPhone18,1", ios: ios, picked: { stored }, pick: { stored = $0 }))
        }
        let none = model("25.0")
        await none.load()
        #expect(none.shown && none.match == .none)
        await none.setAllowed(true)
        #expect(sent.isEmpty)

        let several = model("26.1")
        await several.load()
        guard case .choose = several.match else { Issue.record("several alike"); return }
        await several.setAllowed(true)
        #expect(sent.isEmpty, "nothing before the user picks")
        several.pick("dev-1")
        #expect(stored == "dev-1")
        await several.setAllowed(true)
        #expect(sent.count == 1 && sent[0] == ("dev-1", true))
        if case .this(let me, _) = several.match { #expect(me.consented) } else { Issue.record("picked") }
    }

    /// The consent carries this app's own bundle id and name, read from the bundle, so the hub keeps the assistant
    /// off Persona itself on the phone.
    @Test func consentSaysWhichAppItIs() async throws {
        let hub = FakeHub()
        let path = "/api/v1/iphone/consent"
        hub.on("PUT", path, json: #"{"id":"dev-1","name":"A","machines":[],"consented":true}"#)
        _ = try await hub.client.setIPhoneConsent(device: "dev-1", allowed: true)
        let body = try #require(hub.sent("PUT", path).first?.body)
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: Any]
        let app = sent?["app"] as? [String: Any]
        #expect(sent?["device"] as? String == "dev-1" && sent?["allowed"] as? Bool == true)
        #expect(app?["bundle_id"] as? String == Bundle.main.bundleIdentifier)
        #expect((app?["name"] as? String)?.isEmpty == false)
    }

    @Test func hiddenWithoutTheExtension() async {
        let off = IPhoneConsentModel(deps: .init(list: { IPhoneList(on: false) }, consent: { _, _ in throw APIError(status: 404, message: "") },
                                                 model: "iPhone18,1", ios: "26.1", picked: { nil }, pick: { _ in }))
        await off.load()
        #expect(!off.shown)
        let old = IPhoneConsentModel(deps: .init(list: { nil }, consent: { _, _ in throw APIError(status: 404, message: "") },
                                                 model: "iPhone18,1", ios: "26.1", picked: { nil }, pick: { _ in }))
        await old.load()
        #expect(!old.shown)
    }

    @Test func toolsReadAsSteps() {
        let steps: JSONValue = .object(["steps": .array([
            .object(["action": .string("tap"), "target": .object(["n": .number(3)])]),
            .object(["action": .string("type"), "text": .string("hello")]),
            .object(["action": .string("swipe"), "direction": .string("up")]),
            .object(["action": .string("button"), "button": .string("home")]),
            .object(["action": .string("tap"), "target": .object(["x": .number(119.6), "y": .number(640)])]),
            .object(["action": .string("long_press"), "target": .object(["label": .string("Pay")])]),
        ])])
        let line = IPhoneSteps.describe(steps) ?? ""
        let parts = line.components(separatedBy: " → ")
        #expect(parts.count == 6)
        #expect(!line.contains("hello"), "typed text is never shown, it may be a password")
        #expect(parts[0].contains("3") && parts[1].contains("5") && parts[3] == IPhoneSteps.step(.object(["action": .string("button"), "button": .string("home")])))
        #expect(parts[4].contains("(120, 640)") && parts[5].contains("“Pay”"))
        #expect(IPhoneSteps.describe(.object([:])) == nil)
        #expect(ToolPresentation.symbol("mcp__persona__iphone_do") == Theme.Icon.phoneControl)
        #expect(ToolPresentation.symbol("phone_contacts_search") != Theme.Icon.phoneControl, "the phone's own data tools stay as they are")
    }

    /// The consent names the iPhone it matched, in the section and in the dialog, so a twin is noticed.
    @Test func theConsentNamesTheIPhone() throws {
        let all = try decoded().devices
        let me = IPhoneIdentity.match(all, model: "iPhone14,5", ios: "18.6", picked: nil)
        #expect(IPhoneConsentText.confirmTitle(me).contains("Other iPhone"))
        #expect(IPhoneConsentText.detail(all[1]).contains("Other iPhone"))
        #expect(!IPhoneConsentText.confirmTitle(.none).contains("Other iPhone"))
    }

    @Test func aDeviceSaysWhatIsLeft() {
        var d = IPhoneDevice(id: "a", name: "A", ios: "26.1", paired: true, connected: true, ready: true, allowed: true)
        #expect(IPhoneControlText.state(d).ok)
        d.allowed = false
        #expect(!IPhoneControlText.state(d).ok)
        d.locked = true
        #expect(!IPhoneControlText.state(d).ok)
        d.paired = false
        #expect(!IPhoneControlText.state(IPhoneDevice(id: "b", name: "B", paired: false, usb: true)).ok)
    }
}
