import Observation
import PersonaWire

/// Only source text and its resolved language require a new byte/line index.
nonisolated struct CodeSnippetInput: Hashable, Sendable {
    let text: String
    let language: SyntaxLanguage?

    init(_ code: ExtensionCodeComponent) {
        text = code.text
        language = code.language.flatMap(SyntaxLanguage.named) ?? code.path.flatMap { SyntaxLanguage.detect(name: $0) }
    }

    static func == (lhs: Self, rhs: Self) -> Bool {
        lhs.text == rhs.text && lhs.language?.id == rhs.language?.id
    }

    func hash(into hasher: inout Hasher) {
        hasher.combine(text)
        hasher.combine(language?.id)
    }

    func document() -> CodeDocument { CodeDocument(text: text, language: language) }
}

/// Prepare inputs off main; keep the two most recent jobs/results when a display task is cancelled.
@MainActor @Observable
final class CodeSnippetModel {
    private struct Preparation {
        let id: UInt64
        let task: Task<CodeDocument, Error>
        var used: UInt64
    }

    private(set) var source: CodeSnippetSource?
    @ObservationIgnored private var latest: ExtensionCodeComponent?
    @ObservationIgnored private var request = 0
    @ObservationIgnored private var clock: UInt64 = 0
    @ObservationIgnored private var preparations: [CodeSnippetInput: Preparation] = [:]
    @ObservationIgnored private let prepare: @Sendable (CodeSnippetInput) async throws -> CodeDocument

    init(prepare: @escaping @Sendable (CodeSnippetInput) async throws -> CodeDocument = { input in
        await Task.detached(priority: .userInitiated) { input.document() }.value
    }) {
        self.prepare = prepare
    }

    func update(_ code: ExtensionCodeComponent) {
        latest = code
        if let source, source.input == CodeSnippetInput(code), source.code != code {
            self.source = source.updatingPresentation(code)
        }
    }

    func load(_ code: ExtensionCodeComponent) async {
        guard !Task.isCancelled else { return }
        let input = CodeSnippetInput(code)
        // Do not replace a newer highlight/start-line update with the task's captured older presentation.
        if latest.map(CodeSnippetInput.init) != input { update(code) }
        guard source?.input != input else { return }
        request &+= 1
        let generation = request
        let preparation = preparation(for: input)
        let document: CodeDocument
        do {
            document = try await preparation.task.value
        } catch {
            if preparations[input]?.id == preparation.id { preparations[input] = nil }
            return
        }
        // Cancelling this display does not discard a successfully indexed recent input.
        guard !Task.isCancelled, generation == request, let latest, CodeSnippetInput(latest) == input else { return }
        source = CodeSnippetSource(latest, document: document)
    }

    private func preparation(for input: CodeSnippetInput) -> Preparation {
        clock &+= 1
        if var ready = preparations[input] {
            ready.used = clock
            preparations[input] = ready
            return ready
        }
        if preparations.count >= 2, let oldest = preparations.min(by: { $0.value.used < $1.value.used })?.key {
            preparations[oldest] = nil
        }
        let factory = prepare
        let task = Task { try await factory(input) }
        let fresh = Preparation(id: clock, task: task, used: clock)
        preparations[input] = fresh
        return fresh
    }

    func current(_ code: ExtensionCodeComponent) -> CodeSnippetSource? {
        guard let source, source.input == CodeSnippetInput(code) else { return nil }
        return source
    }
}
