import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The app's side of the in-app tunnel (docs/operations/network.md「Connecting to hub through an in-app tunnel」): only a hub with an extension that
/// contributes a tunnel, switched on, has one; its name titles the tunnel and its settings say where it goes.
struct InAppTunnelRouteTests {
    private func extensions(_ json: String) throws -> [HubExtension] {
        try JSONDecoder().decode(HubExtensionList.self, from: Data(json.utf8)).plugins
    }

    private func detail(_ json: String) throws -> HubExtensionDetail {
        try JSON.decoder.decode(HubExtensionDetail.self, from: Data(json.utf8))
    }

    @Test func onlyAnEnabledTunnelExtensionCounts() throws {
        let on = try extensions(#"{"extensions":[{"id":"cis","name":"CIS","kind":"connectors","status":"running","enabled":true},{"id":"office","name":"办公网","kind":"skills","status":"running","enabled":true,"contributes":{"tunnels":["office"]}}]}"#)
        #expect(InAppTunnelRoute.extensionOn(on) == "office")
        let off = try extensions(#"{"extensions":[{"id":"office","name":"办公网","kind":"skills","status":"disabled","enabled":false,"contributes":{"tunnels":["office"]}}]}"#)
        #expect(InAppTunnelRoute.extensionOn(off) == nil)
        let other = try extensions(#"{"extensions":[{"id":"cis","name":"CIS","kind":"connectors","status":"running","enabled":true,"contributes":{"skills":["cis"]}}]}"#)
        #expect(InAppTunnelRoute.extensionOn(other) == nil)
        #expect(InAppTunnelRoute.extensionOn([]) == nil)
    }

    @Test func theDetailNamesTheTunnel() throws {
        let d = try detail(#"{"id":"office","name":"办公网","kind":"skills","status":"running","config":{"target_host":"hub.internal.test"},"contributions":[{"point":"tunnels","id":"office","title":" 办公网隧道 ","enabled":true,"can_disable":true}]}"#)
        let route = InAppTunnelRoute(extension: d, always: false)
        #expect(route?.name == "办公网隧道" && route?.title == "办公网隧道" && route?.targetHost == "hub.internal.test")
        let switchedOff = try detail(#"{"id":"office","name":"办公网","kind":"skills","status":"running","config":{"target_host":"hub.internal.test"},"contributions":[{"point":"tunnels","id":"office","title":"办公网隧道","enabled":false,"can_disable":true}]}"#)
        #expect(InAppTunnelRoute(extension: switchedOff, always: false) == nil)
        let untitled = try detail(#"{"id":"office","name":"办公网","kind":"skills","status":"running","config":{"target_host":"hub.internal.test"}}"#)
        #expect(InAppTunnelRoute(extension: untitled, always: true)?.title == String(localized: "内网隧道"))
    }

    @Test func theExtensionSettingsMakeTheRoute() {
        let full = InAppTunnelRoute(extensionConfig: ["target_host": .string(" hub.internal.test "), "target_port": .number(8080)], always: true)
        #expect(full == InAppTunnelRoute(targetHost: "hub.internal.test", targetPort: 8080, always: true))
        #expect(full?.target == "hub.internal.test:8080")
        let hostOnly = InAppTunnelRoute(extensionConfig: ["target_host": .string("192.0.2.5")], always: false)
        #expect(hostOnly?.targetPort == InAppTunnelRoute.defaultTargetPort)
        let oddPort = InAppTunnelRoute(extensionConfig: ["target_host": .string("192.0.2.5"), "target_port": .number(80.5)], always: false)
        #expect(oddPort?.targetPort == InAppTunnelRoute.defaultTargetPort)
        #expect(InAppTunnelRoute(extensionConfig: [:], always: false) == nil)
        #expect(InAppTunnelRoute(extensionConfig: ["target_host": .string("  ")], always: false) == nil)
        #expect(InAppTunnelRoute(extensionConfig: ["target_host": .number(1)], always: false) == nil)
    }

    @Test func aSavedRouteKeepsItsName() throws {
        let saved = InAppTunnelRoute(targetHost: "[::1]", targetPort: 18660, name: "办公网隧道", always: true)
        let back = try JSONDecoder().decode(InAppTunnelRoute.self, from: JSONEncoder().encode(saved))
        #expect(back == saved)
        let old = try JSONDecoder().decode(InAppTunnelRoute.self, from: Data(#"{"target_host":"192.0.2.5"}"#.utf8))
        #expect(old.name.isEmpty && !old.always && old.targetPort == 18660)
    }

    @Test func anSSHPortProbesOnlyTheSSHPath() throws {
        // No ssh_port: the route and its probe are what they were.
        let plain = try #require(InAppTunnelRoute(extensionConfig: ["target_host": .string("192.0.2.5")], always: false))
        #expect(plain.sshPort == nil && plain.sshProbe == plain)
        // With one: the direct tunnel still goes to the HTTP port; the SSH path probes the SSH port.
        let ssh = try #require(InAppTunnelRoute(extensionConfig: ["target_host": .string("192.0.2.5"), "ssh_port": .number(22)], always: false))
        #expect(ssh.target == "192.0.2.5:18660")
        #expect(ssh.sshProbe.target == "192.0.2.5:22")
        // Not a port: ignored.
        #expect(InAppTunnelRoute(extensionConfig: ["target_host": .string("h"), "ssh_port": .number(0)], always: false)?.sshPort == nil)
        // Kept with the pairing; a pairing saved before it has none.
        #expect(try JSONDecoder().decode(InAppTunnelRoute.self, from: JSONEncoder().encode(ssh)) == ssh)
        #expect(try JSONDecoder().decode(InAppTunnelRoute.self, from: Data(#"{"target_host":"h"}"#.utf8)).sshPort == nil)
    }
}
