import Foundation
import Testing
@testable import Persona

/// The lineage actions use the existing parent endpoint's bare JSON body, never a `parent` wrapper.
@MainActor
struct InboxParentTests {
    private static let path = "/api/v1/inbox/lark:f2:200/parent"
    private static let answer = """
    {"id":"lark:f2:200","source":"lark","kind":"group","account":"lark","account_name":"Chat","title":"Design review",
     "preview":"","count":1}
    """

    @Test func detachingSendsJSONNull() async throws {
        let hub = FakeHub()
        hub.on("PUT", Self.path, json: Self.answer)

        let item = try await hub.client.setInboxParent("lark:f2:200", nil)

        #expect(item.id == "lark:f2:200")
        let request = try #require(hub.requests.last)
        #expect(request.method == "PUT")
        #expect(request.path == Self.path)
        let body = try #require(request.body)
        #expect(String(data: body, encoding: .utf8) == "null")
        #expect(try JSONSerialization.jsonObject(with: body, options: [.fragmentsAllowed]) is NSNull)
    }

    @Test func linkingSendsTheBareParentObject() async throws {
        let hub = FakeHub()
        hub.on("PUT", Self.path, json: Self.answer)

        _ = try await hub.client.manager("assistant-2").setInboxParent(
            "lark:f2:200", InboxParent(kind: "inbox", id: "lark:f1:100"))

        let request = try #require(hub.requests.last)
        #expect(request.headers[APIClient.managerHeader] == "assistant-2")
        let body = try #require(request.body)
        let object = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(object.count == 2)
        #expect(object["kind"] as? String == "inbox")
        #expect(object["id"] as? String == "lark:f1:100")
        #expect(object["parent"] == nil)
    }
}
