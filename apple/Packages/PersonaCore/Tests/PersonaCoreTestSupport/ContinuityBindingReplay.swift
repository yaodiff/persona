import Foundation
import PersonaCore
import Synchronization

public struct ContinuityBindingReplay: Sendable {
    public let cases: Int
    public let steps: Int
    public let patches: Int

    public init(cases: Int, steps: Int, patches: Int) {
        self.cases = cases
        self.steps = steps
        self.patches = patches
    }

    public static func replay(_ case: ContinuityCase) throws -> Int {
        let name = "\(`case`.file)/\(`case`.name)"
        let chat = "fixture/continuity"
        let recorder = EffectRecorder()
        let client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(run: { effect in
            recorder.effects.withLock { $0.append(effect) }
        }))
        defer { client.close() }
        let initial = `case`.initial
        let snapshot = ChatSnapshot(
            itemsJson: try initial.itemsJSON(), history: initial.text("history"),
            covered: Int64(initial.number("covered") ?? 0), hasMore: initial.boolean("has_more"),
            loadedOnce: initial.flag("loaded_once")
        )
        try client.dispatch(.openChat(chat: chat, snapshot: snapshot))
        var list: [String] = []
        var patches = 0
        _ = try step(client, chat: chat, list: &list, label: "\(name) initial")
        patches += 1
        recorder.effects.withLock { $0.removeAll() }
        for (index, event) in `case`.events.enumerated() {
            let label = "\(name) step \(index)"
            let action: Action
            if let event = try event.chatEvent() {
                action = .chat(chat: chat, event: event)
            } else {
                action = .continueFill(chat: chat)
            }
            try client.dispatch(action)
            let patch = try step(client, chat: chat, list: &list, label: label)
            patches += 1
            let effects = recorder.effects.withLock { recorded in
                defer { recorded.removeAll() }
                return recorded
            }
            var requests: [ContinuityJSON] = []
            for effect in effects {
                switch effect {
                case let .fetch(chat: effectChat, request: _):
                    guard effectChat == chat else { throw ContinuityFixtureError.invalidFixture("\(label): wrong chat") }
                    requests.append(try effect.requestJSON ?? .null)
                case let .rejected(reason: reason): throw ContinuityFixtureError.rejected("\(label): \(reason)")
                }
            }
            let expected = `case`.expected[index]
            let actualItems = ContinuityJSON.array(try list.map { try ContinuityJSON.from(try JSONSerialization.jsonObject(with: Data($0.utf8))) })
            try equal(actualItems, expected["items"], "\(label) items")
            try equal(.array(list.map { json in
                let item = try ContinuityJSON.from(try JSONSerialization.jsonObject(with: Data(json.utf8)))
                return item["id"] ?? .null
            }), expected["item_ids"], "\(label) item_ids")
            try equal(.number(Double(patch.covered)), expected["covered"], "\(label) covered")
            try equal(patch.history.map(ContinuityJSON.string) ?? .null, expected["history"], "\(label) history")
            try equal(patch.hasMore.map(ContinuityJSON.bool) ?? .null, expected["has_more"], "\(label) has_more")
            try equal(.bool(patch.loadedOnce), expected["loaded_once"], "\(label) loaded_once")
            try equal(.array(requests), expected["actions"], "\(label) actions")
        }
        return patches
    }

    public static func replayAll(in directory: URL = ContinuityCase.repositoryDirectory) throws -> ContinuityBindingReplay {
        let cases = try ContinuityCase.all(in: directory)
        var steps = 0
        var patches = 0
        for `case` in cases {
            patches += try replay(`case`)
            steps += `case`.events.count
        }
        return ContinuityBindingReplay(cases: cases.count, steps: steps, patches: patches)
    }

    private static func step(_ client: PersonaCoreClient, chat: String, list: inout [String], label: String) throws -> ChatPatch {
        try client.flush()
        let patch = try client.drain(chat: chat)
        for operation in patch.ops {
            switch operation {
            case let .remove(index: index):
                guard index < list.count else { throw ContinuityFixtureError.invalidFixture("\(label): remove index") }
                list.remove(at: Int(index))
            case let .insert(index: index, id: _, json: json):
                guard index <= list.count else { throw ContinuityFixtureError.invalidFixture("\(label): insert index") }
                list.insert(json, at: Int(index))
            case let .update(index: index, id: _, json: json):
                guard index < list.count else { throw ContinuityFixtureError.invalidFixture("\(label): update index") }
                list[Int(index)] = json
            }
        }
        return patch
    }

    private static func equal(_ actual: ContinuityJSON?, _ expected: ContinuityJSON?, _ message: String) throws {
        guard let actual, let expected, actual == expected else {
            throw ContinuityFixtureError.invalidFixture("\(message)\nactual: \(String(describing: actual))\nexpected: \(String(describing: expected))")
        }
    }
}

final class EffectRecorder: @unchecked Sendable {
    let effects = Mutex<[Effect]>([])
}
