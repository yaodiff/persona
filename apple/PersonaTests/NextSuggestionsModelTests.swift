import Foundation
import Testing
@testable import Persona

private actor AssistantPUTGate {
    enum Failure: Error { case requestDidNotStart, noPendingRequest }
    private var count = 0
    private var pending: [CheckedContinuation<Void, Never>] = []

    func hold() async {
        count += 1
        await withCheckedContinuation { pending.append($0) }
    }

    func waitForRequest() async throws {
        for _ in 0..<200 {
            if count > 0 && !pending.isEmpty { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        throw Failure.requestDidNotStart
    }

    func release() throws {
        guard !pending.isEmpty else { throw Failure.noPendingRequest }
        pending.removeFirst().resume()
    }
}

/// Settings › Assistant › Suggest the Next Message: optional wire support and the dedicated model's optimistic,
/// assistant-scoped writes. All hubs and identifiers are synthetic.
@MainActor struct NextSuggestionsModelTests {
    private static func state(next: Bool? = nil) -> String {
        let field = next.map { #","next_suggestions":"# + ($0 ? "true" : "false") } ?? ""
        return #"{"machine_id":"box","launcher":"claude","cwd":"/assistant","conversations":[]\#(field)}"#
    }

    private func model(api: @escaping () -> APIClient?, report: @escaping (Error) -> Void = { _ in }) -> NextSuggestionsModel {
        NextSuggestionsModel(deps: AssistantDeps(api: api, report: report, toast: { _ in }, open: { _ in }))
    }

    @Test func wireDistinguishesOnOffAndAnOlderHub() throws {
        #expect(try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(Self.state(next: true).utf8)).nextSuggestions == true)
        #expect(try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(Self.state(next: false).utf8)).nextSuggestions == false)
        #expect(try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(Self.state().utf8)).nextSuggestions == nil)

        for on in [true, false] {
            let body = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(nextSuggestions: on))) as? [String: Any])
            #expect(body["next_suggestions"] as? Bool == on && body.count == 1)
        }
        let unrelated = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(APIClient.AssistantConfigBody(mainTasksQuiet: true))) as? [String: Any])
        #expect(unrelated["next_suggestions"] == nil)
    }

    @Test func loadShowsOffAndAnOlderHubKeepsTheRowHidden() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        let page = model(api: { hub.client })
        await page.load()
        #expect(page.value == false && page.loaded && !page.saving)
        #expect(hub.sent("GET", "/api/v1/assistant").count == 1)

        let old = FakeHub(url: "https://old.fake.invalid")
        old.on("GET", "/api/v1/assistant", json: Self.state())
        let oldPage = model(api: { old.client })
        await oldPage.load()
        #expect(oldPage.value == nil && oldPage.loaded)
        await oldPage.set(true)
        #expect(old.sent("PUT", "/api/v1/assistant").isEmpty, "an unsupported setting cannot be switched blindly")
    }

    @Test func aFailedLoadIsNotMarkedAsLoaded() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { _ in throw APIError(status: 500, message: "synthetic offline") }
        let page = model(api: { hub.client }, report: { _ in })
        await page.load()
        #expect(page.value == nil && !page.loaded && !page.saving)
    }

    @Test func saveIsOptimisticSparseAndConfirmed() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        var during: Bool?
        var busy = false
        let page = model(api: { hub.client })
        hub.on("PUT", "/api/v1/assistant") { request in
            during = page.value
            busy = page.saving
            let body = try #require(request.body)
            let object = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
            #expect(object["next_suggestions"] as? Bool == true && object.count == 1)
            return Data(Self.state(next: true).utf8)
        }
        await page.load()
        await page.set(true)
        #expect(during == true && busy, "the switch moved before the PUT answered")
        #expect(page.value == true && !page.saving)
    }

    @Test func theResponseValueWinsOverTheRequest() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        hub.on("PUT", "/api/v1/assistant", json: Self.state(next: false))
        let page = model(api: { hub.client })
        await page.load()
        await page.set(true)
        #expect(page.value == false && !page.saving)
    }

    @Test func aFailedSaveRollsBackAndReportsOnce() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        hub.on("PUT", "/api/v1/assistant") { _ in throw APIError(status: 500, message: "synthetic refusal") }
        var reports: [String] = []
        let page = model(api: { hub.client }, report: { reports.append(String(describing: $0)) })
        await page.load()
        await page.set(true)
        #expect(page.value == false && !page.saving)
        #expect(reports.count == 1)
    }

    @Test func aSecondChangeWhileSavingSendsOnlyOneWrite() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        hub.on("PUT", "/api/v1/assistant", json: Self.state(next: true))
        let gate = AssistantPUTGate()
        var client = hub.client
        let inner = try #require(client.transport)
        client.transport = { request in
            if request.method == "PUT" { await gate.hold() }
            return try await inner(request)
        }
        let page = model(api: { client })
        await page.load()
        let first = Task { await page.set(true) }
        try await gate.waitForRequest()
        await page.set(false)
        #expect(page.value == true && page.saving)
        try await gate.release()
        await first.value
        #expect(page.value == true && !page.saving)
        #expect(hub.sent("PUT", "/api/v1/assistant").count == 1)
    }

    @Test func restartUsesTheNewAssistantAndFencesTheOldWrite() async throws {
        let old = FakeHub(url: "https://old.fake.invalid")
        old.on("GET", "/api/v1/assistant", json: Self.state(next: false))
        old.on("PUT", "/api/v1/assistant") { _ in throw APIError(status: 500, message: "old assistant failed") }
        let new = FakeHub(url: "https://new.fake.invalid")
        new.on("GET", "/api/v1/assistant", json: Self.state(next: true))
        new.on("PUT", "/api/v1/assistant", json: Self.state(next: false))

        let gate = AssistantPUTGate()
        var oldClient = old.client
        let oldTransport = try #require(oldClient.transport)
        oldClient.transport = { request in
            if request.method == "PUT" { await gate.hold() }
            return try await oldTransport(request)
        }
        var current: APIClient? = oldClient
        var reports: [String] = []
        let page = model(api: { current }, report: { reports.append(String(describing: $0)) })
        await page.load()
        #expect(page.value == false)

        let oldSave = Task { await page.set(true) }
        try await gate.waitForRequest()
        #expect(page.value == true && page.saving)
        current = new.client
        await page.restart()
        #expect(page.value == true && !page.saving)

        try await gate.release()
        await oldSave.value
        #expect(page.value == true, "the old assistant's failure must not roll back the new assistant")
        #expect(reports.isEmpty, "an old assistant's late error is not shown on the new page")

        await page.set(false)
        #expect(page.value == false && !page.saving)
        #expect(old.sent("PUT", "/api/v1/assistant").count == 1)
        #expect(new.sent("PUT", "/api/v1/assistant").count == 1)
        #expect(new.sent("GET", "/api/v1/assistant").count == 1)
    }
}
