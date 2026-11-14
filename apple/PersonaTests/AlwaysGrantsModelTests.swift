import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor @Suite struct AlwaysGrantsModelTests {
    private let list = "/api/v1/assistant/conversations/c1/always"
    private let two = #"""
        {"grants":[
         {"key_hash":"aa","tool_name":"Bash","created_at":"2026-11-06T09:30:00.123456789+08:00"},
         {"key_hash":"bb","tool_name":"","created_at":"2026-11-06T09:31:00Z"}]}
        """#

    private func loaded(_ hub: FakeHub, conversation: String = "c1") async -> AlwaysGrantsModel {
        let page = AlwaysGrantsModel()
        await page.load(conversation: conversation, api: hub.client)
        return page
    }

    @Test func listsAndRevokesOneGrant() async throws {
        let hub = FakeHub()
        hub.on("GET", list, json: two)
        hub.on("DELETE", list + "/aa", json: #"{"ok":true,"restart_required":false}"#)
        let page = await loaded(hub)
        #expect(page.grants?.map(\.toolName) == ["Bash", ""])
        #expect(page.grants?[0].createdAt == JSON.parseDate("2026-11-06T09:30:00.123+08:00"))

        await page.revoke(try #require(page.grants?[0]), conversation: "c1", api: hub.client)
        #expect(page.grants?.map(\.keyHash) == ["bb"])
        #expect(!page.restartNeeded)
        #expect(page.revoking == nil)
    }

    @Test func theRestartWarningOutlivesTheSheet() async throws {
        let hub = FakeHub()
        let path = "/api/v1/assistant/conversations/restart-1/always"
        hub.on("GET", path, json: two)
        hub.on("DELETE", path + "/aa", json: #"{"ok":true,"restart_required":true}"#)
        let page = await loaded(hub, conversation: "restart-1")
        await page.revoke(try #require(page.grants?[0]), conversation: "restart-1", api: hub.client)
        #expect(page.restartNeeded)

        #expect(await loaded(hub, conversation: "restart-1").restartNeeded)
        hub.on("GET", list, json: two)
        #expect(await !loaded(hub).restartNeeded)
    }

    @Test func aFailedRevokeKeepsTheRowAndSaysWhy() async throws {
        let hub = FakeHub()
        hub.on("GET", list, json: two)
        hub.on("DELETE", list + "/aa") { _ in throw APIError(status: 500, message: "数据库不可用") }
        let page = await loaded(hub)
        await page.revoke(try #require(page.grants?[0]), conversation: "c1", api: hub.client)
        #expect(page.grants?.count == 2)
        #expect(page.error == "数据库不可用")
    }

    @Test(arguments: [403, 404]) func aHubOrDeviceWithoutTheRoutesHidesTheSection(status: Int) async {
        let hub = FakeHub()
        hub.on("GET", list) { _ in throw APIError(status: status, message: "no") }
        let page = await loaded(hub)
        #expect(page.unavailable)
        #expect(page.error == nil)
    }

    @Test func anotherFailureCanBeRetried() async {
        let hub = FakeHub()
        hub.on("GET", list) { _ in throw APIError(status: 502, message: "电脑离线") }
        let page = await loaded(hub)
        #expect(!page.unavailable)
        #expect(page.grants == nil)
        #expect(page.error == "电脑离线")

        hub.on("GET", list, json: #"{"grants":[]}"#)
        await page.load(conversation: "c1", api: hub.client)
        #expect(page.grants == [])
        #expect(page.error == nil)
    }
}
