import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The chat draws an interrupted receipt only from what the hub worked out (#3834): a handled one is a grey line, a
/// batch one card on its first receipt, the batch's other receipts nothing of their own.
struct InterruptLookTests {
    private func receipt(_ id: String, _ extra: [String: JSONValue] = [:]) -> FeedItem {
        var body: [String: JSONValue] = ["title": .string("Example task"), "state": .string("interrupted")]
        body.merge(extra) { _, new in new }
        return FeedItem(id: id, conversation: "c1", at: .distantPast, kind: .receipt, ref: "todo_\(id)", body: .object(body))
    }

    @Test func aReceiptFromAnOlderHubIsDrawnAsEver() {
        #expect(receipt("a").interruptLook == nil)
    }

    @Test func cancelledReroutedAndResumedAreHandledLines() {
        for r in [InterruptResolution.cancelled, .rerouted, .resumed, .accepted, .done] {
            #expect(receipt("a", ["resolution": .string(r.rawValue)]).interruptLook == .resolved(r))
        }
        #expect(receipt("a", ["resolution": .string("pending")]).interruptLook == .pending)
    }

    @Test func aBatchIsDrawnOnceOnItsHead() {
        func entry(_ id: String, _ r: String) -> JSONValue {
            .object(["receipt": .string(id), "todo_id": .string("todo_\(id)"), "title": .string(id), "resolution": .string(r)])
        }
        let batch: JSONValue = .object(["cause": .string("model_error"), "machine": .string("mlx"), "total": .number(2),
                                        "pending": .number(1), "resolved": .number(1),
                                        "tasks": .array([entry("h", "cancelled"), entry("m", "pending")])])
        guard case .batch(let b) = receipt("h", ["resolution": .string("cancelled"), "batch": batch]).interruptLook else {
            Issue.record("the head is not a batch")
            return
        }
        #expect(b.total == 2 && b.pending == 1 && b.machine == "mlx")
        #expect(b.tasks.map(\.resolved) == [.cancelled, .pending])
        #expect(receipt("m", ["resolution": .string("pending"), "group": .string("h")]).interruptLook == .hidden)
    }
}
