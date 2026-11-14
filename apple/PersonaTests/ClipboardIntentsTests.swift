import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The clipboard shortcuts' network part (#3064): what they send, and every failure as one sentence.
struct ClipboardIntentsTests {
    private let mac = MachineEntity(id: "mac", name: "MacBook")

    @Test func sendsTextAndSaysWhere() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/machines/mac/clipboard") { req in
            let body = try JSON.decoder.decode([String: String].self, from: #require(req.body))
            #expect(body == ["text": "hello"])
            return Data()
        }
        let said = try await ClipboardIntentFlow.send(hub.client, .text("hello"), to: mac)
        #expect(said == ClipboardOutcome.sent(.text).message(machine: "MacBook"))
    }

    @Test func nothingToSendSendsNothing() async {
        let hub = FakeHub()
        await #expect(throws: IntentFailureSentence.self) { try await ClipboardIntentFlow.send(hub.client, nil, to: mac) }
        #expect(hub.requests.isEmpty)
    }

    @Test func getsTextAndRefusesEmptyOrHidden() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"text","text":"from the Mac"}"#)
        #expect(try await ClipboardIntentFlow.fetch(hub.client, from: mac) == .text("from the Mac"))
        hub.on("GET", "/api/v1/machines/mac/clipboard", json: #"{"kind":"concealed"}"#)
        do {
            _ = try await ClipboardIntentFlow.fetch(hub.client, from: mac)
            Issue.record("hidden content is not handed out")
        } catch let e as IntentFailureSentence {
            #expect(e.message == ClipboardOutcome.remoteConcealed.message(machine: "MacBook"))
        }
    }

    @Test func hubRefusalsInTheirOwnWords() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/clipboard") { _ in
            throw APIError(status: 403, message: "off", fields: ["code": "clipboard_disabled"])
        }
        do {
            _ = try await ClipboardIntentFlow.fetch(hub.client, from: mac)
            Issue.record("refused")
        } catch let e as IntentFailureSentence {
            #expect(e.message == ClipboardFailure.disabled.sentence(machine: "MacBook"))
        } catch {
            Issue.record("unexpected \(error)")
        }
        #expect(ClipboardIntentFlow.failure(APIError(status: 401, message: "x"), machine: "MacBook")
                == IntentFailure.sentence(for: APIError(status: 401, message: "x"), host: IntentSupport.host()))
    }
}
