import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct CodeSnippetModelTests {
    @Test func presentationAndEquivalentLanguagesHaveTheSamePreparationKey() {
        var code = ExtensionCodeComponent(path: "example.go", text: "package main\n")
        let original = CodeSnippetInput(code)
        code.language = "Go"
        code.path = "other.go"
        code.startLine = 42
        code.highlight = .init(start: 42, end: 43)
        code.truncated = true
        #expect(CodeSnippetInput(code) == original)
        code.text += "var value = 1\n"
        #expect(CodeSnippetInput(code) != original)
        code.text = original.text
        code.language = "Python"
        #expect(CodeSnippetInput(code) != original)
    }

    @Test func unchangedAndPresentationOnlyUpdatesReuseTheIndexedDocument() async throws {
        let gate = PreparationGate(paused: false)
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        var code = ExtensionCodeComponent(path: "example.go", text: "first\nsecond\n")
        model.update(code)
        await model.load(code)
        let document = try #require(model.source?.document)
        for index in 0..<100 {
            code.startLine = index + 1
            code.highlight = .init(start: index + 1, end: index + 1)
            model.update(code)
            await model.load(code)
            #expect(model.source?.document === document)
            #expect(model.source?.gutter(0).string == String(index + 1))
            #expect(model.source?.line(1).string == "second")
        }
        let count = await gate.count
        #expect(count == 1)
    }

    @Test func presentationChangesDuringPreparationUseTheLatestPresentation() async throws {
        let gate = PreparationGate()
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        var code = ExtensionCodeComponent(text: "first\nsecond\n", startLine: 1)
        model.update(code)
        let task = Task { await model.load(code) }
        await gate.started(code.text)
        code.startLine = 50
        code.highlight = .init(start: 50, end: 51)
        model.update(code)
        await gate.finish(code.text)
        await task.value
        #expect(model.source?.gutter(0).string == "50")
        #expect(model.source?.code.highlight?.start == 50)
    }

    @Test func replacedInputHidesOldLinesUntilTheNewDocumentIsReady() async throws {
        let gate = PreparationGate(paused: false)
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        let old = ExtensionCodeComponent(text: "old")
        model.update(old)
        await model.load(old)
        let fresh = ExtensionCodeComponent(text: "new\nsecond")
        model.update(fresh)
        #expect(model.current(fresh) == nil)
        await model.load(fresh)
        #expect(model.current(fresh)?.lineCount == 2)
        #expect(model.current(fresh)?.line(0).string == "new")
        let count = await gate.count
        #expect(count == 2)
    }

    @Test func cancelledPreparationsDoNotPublish() async {
        let gate = PreparationGate()
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        let code = ExtensionCodeComponent(text: "cancelled")
        model.update(code)
        let task = Task { await model.load(code) }
        await gate.started(code.text)
        task.cancel()
        await gate.finish(code.text)
        await task.value
        #expect(model.source == nil)
        await gate.unpause()
        await model.load(code)
        #expect(model.source?.line(0).string == "cancelled")
        let count = await gate.count
        #expect(count == 1)
    }

    @Test func recentCompletedInputsAreReusedAndColdInputsAreEvicted() async {
        let gate = PreparationGate(paused: false)
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        let a = ExtensionCodeComponent(text: "a"), b = ExtensionCodeComponent(text: "b"), c = ExtensionCodeComponent(text: "c")
        for code in [a, b, a, c, a] {
            model.update(code)
            await model.load(code)
        }
        let reused = await gate.count
        #expect(reused == 3)
        model.update(b)
        await model.load(b)
        let evicted = await gate.count
        #expect(evicted == 4)
    }

    @Test func olderResponsesDoNotReplaceANewerDocument() async {
        let gate = PreparationGate()
        let model = CodeSnippetModel(prepare: { input in await gate.prepare(input) })
        let old = ExtensionCodeComponent(text: "old")
        let fresh = ExtensionCodeComponent(text: "fresh")
        model.update(old)
        let a = Task { await model.load(old) }
        await gate.started(old.text)
        model.update(fresh)
        let b = Task { await model.load(fresh) }
        await gate.started(fresh.text)
        await gate.finish(fresh.text)
        await b.value
        await gate.finish(old.text)
        await a.value
        #expect(model.current(fresh)?.line(0).string == "fresh")
        #expect(model.current(old) == nil)
    }

    nonisolated private static func probe(_ input: CodeSnippetInput) -> (isMain: Bool, document: CodeDocument) {
        (Thread.isMainThread, input.document())
    }

    @Test func lineIndexPreparationRunsOutsideTheMainActor() async {
        let input = CodeSnippetInput(ExtensionCodeComponent(text: "你好 🙂\nsecond\n"))
        let result = await Task.detached { Self.probe(input) }.value
        #expect(!result.isMain)
        #expect(result.document.lineCount == 2)
    }

    @Test func defaultFactoryPreparesAFullUnicodeDocument() async {
        let model = CodeSnippetModel()
        let code = ExtensionCodeComponent(text: "你好 🙂\nsecond\n")
        model.update(code)
        await model.load(code)
        #expect(model.source?.lineCount == 2)
        #expect(model.source?.document.string == "你好 🙂\nsecond")
    }

    private actor PreparationGate {
        private var paused: Bool
        private(set) var count = 0
        private var pending: [String: (CodeSnippetInput, CheckedContinuation<CodeDocument, Never>)] = [:]
        private var waiting: [String: [CheckedContinuation<Void, Never>]] = [:]

        init(paused: Bool = true) { self.paused = paused }

        func unpause() { paused = false }

        func prepare(_ input: CodeSnippetInput) async -> CodeDocument {
            count += 1
            guard paused else { return input.document() }
            return await withCheckedContinuation { continuation in
                pending[input.text] = (input, continuation)
                waiting.removeValue(forKey: input.text)?.forEach { $0.resume() }
            }
        }

        func started(_ text: String) async {
            if pending[text] != nil { return }
            await withCheckedContinuation { waiting[text, default: []].append($0) }
        }

        func finish(_ text: String) {
            guard let (input, continuation) = pending.removeValue(forKey: text) else { return }
            continuation.resume(returning: input.document())
        }
    }
}
