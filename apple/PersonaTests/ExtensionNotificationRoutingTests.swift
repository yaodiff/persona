import Foundation
import Testing
import UserNotifications
@testable import Persona
import PersonaWire

@MainActor struct ExtensionNotificationRoutingTests {
    private func event(_ action: String = "reply", hub: String? = "hub-b", text: String? = "收到") throws -> ExtensionNotificationEvent {
        var info: [AnyHashable: Any] = ["extension_notification": ["extension": "sample", "ui": "home", "state": "from-notice", "actions": [
            ["id": "reply", "title": "回复", "input": ["placeholder": "输入"]],
            ["id": "open", "title": "打开", "foreground": true]]]]
        if let hub { info["hub_id"] = hub }
        return try #require(ExtensionNotificationEvent(id: UUID().uuidString, info: info, category: "ext.sample.hash", actionID: action, text: text))
    }

    @Test func anExplicitUnknownOrConflictingSourceNeverFallsBack() throws {
        let e = try event()
        #expect(e.group(hubGroups: ["hub-b": "b"], groups: ["a", "b"]) == "b")
        #expect(e.group(hubGroups: [:], groups: ["a"]) == nil)
        var conflict = e
        conflict.sealedGroup = "a"
        #expect(conflict.group(hubGroups: ["hub-b": "b"], groups: ["a", "b"]) == nil)
        let old = try event(hub: nil)
        #expect(old.group(hubGroups: [:], groups: ["a", "b"]) == nil)
        #expect(old.group(hubGroups: [:], groups: [""]) == "")
    }

    @Test func backgroundUsesOriginalStateAndOnlyReportsToast() async throws {
        let hub = FakeHub()
        var api = hub.client
        api.extensionsAPI = true
        hub.on("POST", "/api/v1/extensions/sample/-/ui/home", json: #"{"toast":"已回复","open_url":"https://example.com","push":{"ui":"other"},"pop":true}"#)
        var reports: [String] = []
        var opened = false
        let deps = ExtensionNotificationHandler.Deps(group: { _ in "b" }, api: { _ in api },
            open: { _, _, _ in opened = true }, report: { _, _, body in reports.append(body) })
        let e = try event()
        await ExtensionNotificationHandler.handle(e, deps: deps)
        await ExtensionNotificationHandler.handle(e, deps: deps)
        #expect(!opened && reports == ["已回复"])
        let sent = hub.sent("POST", "/api/v1/extensions/sample/-/ui/home")
        #expect(sent.count == 1)
        let body = try JSON.decoder.decode(ExtensionUIRequest.self, from: try #require(sent.first?.body))
        #expect(body.action == "reply" && body.state == "from-notice" && body.values == ["text": "收到"])
    }

    @Test func unknownButtonsDismissalsAndMissingSourceMakeNoRequest() async throws {
        let hub = FakeHub()
        var reports = 0
        let deps = ExtensionNotificationHandler.Deps(group: { _ in nil }, api: { _ in hub.client },
            open: { _, _, _ in Issue.record("未知来源不应打开页面") }, report: { _, _, _ in reports += 1 })
        await ExtensionNotificationHandler.handle(try event("unknown"), deps: deps)
        await ExtensionNotificationHandler.handle(try event(UNNotificationDismissActionIdentifier), deps: deps)
        #expect(reports == 0 && hub.requests.isEmpty)
        await ExtensionNotificationHandler.handle(try event(), deps: deps)
        #expect(reports == 1 && hub.requests.isEmpty)
    }

    @Test func foregroundIsClaimedOnceBeforeDrawingOrRebuildingThePage() async throws {
        let hub = FakeHub()
        var api = hub.client
        api.extensionsAPI = true
        hub.on("POST", "/api/v1/extensions/sample/-/ui/home") { request in
            let body = try JSON.decoder.decode(ExtensionUIRequest.self, from: request.body!)
            if body.action != nil {
                #expect(body.state == "from-notice")
                return Data(#"{"view":{"state":"after-action","sections":[{"items":[{"type":"text","text":"已打开"}]}]},"toast":"完成"}"#.utf8)
            }
            return Data(#"{"view":{"state":"rendered","sections":[{"items":[{"type":"text","text":"页面"}]}]}}"#.utf8)
        }
        var route: ExtensionSystemOpen?
        let deps = ExtensionNotificationHandler.Deps(group: { _ in "b" }, api: { _ in api }, open: { link, action, values in
            route = ExtensionSystemOpen(link: link, action: action, values: values)
        }, report: { _, _, _ in Issue.record("前台不应发本地反馈通知") })
        await ExtensionNotificationHandler.handle(try event("open"), deps: deps)
        let open = try #require(route)
        #expect(hub.requests.isEmpty)
        let page = ExtensionViewModel(plugin: "sample", ui: "home", state: open.link.state, deps: .init(api: { api }))
        let outcome = await open.start(page)
        #expect(open.consumed && outcome.toast == "完成" && page.view?.state == "after-action")
        _ = await open.start(page)
        let rebuilt = ExtensionViewModel(plugin: "sample", ui: "home", state: open.link.state, deps: .init(api: { api }))
        _ = await open.start(rebuilt)
        let bodies = try hub.requests.compactMap(\.body).map { try JSON.decoder.decode(ExtensionUIRequest.self, from: $0) }
        #expect(bodies.filter { $0.action != nil }.count == 1)
        #expect(bodies.count == 2 && rebuilt.view?.state == "rendered")
    }

    @Test func aCancelledDeliveryDoesNotStartAnActionOrReportASuccess() async throws {
        let hub = FakeHub()
        var reported = false
        let deps = ExtensionNotificationHandler.Deps(group: { _ in "b" }, api: { _ in hub.client },
            open: { _, _, _ in Issue.record("取消后不应打开页面") }, report: { _, _, _ in reported = true })
        let e = try event()
        let work = Task { await ExtensionNotificationHandler.handle(e, deps: deps) }
        work.cancel()
        await work.value
        #expect(hub.requests.isEmpty && !reported)
    }

    @Test func aDefaultTapDoesNotConsumeTheOpenButtonOrAnotherGroupsNotification() async throws {
        var opened: [String] = []
        let hub = FakeHub()
        let deps = ExtensionNotificationHandler.Deps(group: { $0.hubID == "hub-c" ? "c" : "b" }, api: { _ in hub.client },
            open: { link, action, _ in opened.append(link.group + "/" + (action?.id ?? "default")) }, report: { _, _, _ in })
        let tap = try event(UNNotificationDefaultActionIdentifier)
        var button = try event("open")
        button.notificationID = tap.notificationID
        await ExtensionNotificationHandler.handle(tap, deps: deps)
        await ExtensionNotificationHandler.handle(button, deps: deps)
        await ExtensionNotificationHandler.handle(button, deps: deps)
        button.hubID = "hub-c"
        await ExtensionNotificationHandler.handle(button, deps: deps)
        #expect(opened == ["b/default", "b/open", "c/open"])
    }

    @Test func deepLinksRoundTripTheGroupAndOpaqueStateButRejectAmbiguity() throws {
        let link = ExtensionSystemLink(owner: "sample", ui: "home", group: "b", state: "day=1&range=周末")
        let url = try #require(link.url)
        #expect(ExtensionSystemLink(url: url) == link)
        #expect(ExtensionSystemLink(url: URL(string: "persona://extension/sample/home?group=a&group=b")!) == nil)
        #expect(ExtensionSystemLink(owner: "../admin", ui: "home", group: "b").url == nil)
        #expect(ExtensionSystemLink(owner: "sample", ui: "../home", group: "b").url == nil)
    }
}
