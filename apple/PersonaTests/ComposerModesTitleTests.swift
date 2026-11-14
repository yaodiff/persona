import Testing
@testable import Persona

/// The shell composer's model button (#1091): the short model names and "model · effort".
@MainActor
struct ComposerModesTitleTests {
    @Test func shortModelNames() {
        #expect(SessionModes.modelName("claude-opus-5-5") == "Opus 5.5")
        #expect(SessionModes.modelName("claude-sonnet-4-5-20250929") == "Sonnet 4.5")
        #expect(SessionModes.modelName("claude-haiku-5-5") == "Haiku 5.5")
        #expect(SessionModes.modelName("claude-fable-5-1") == "Fable 5.1")
        #expect(SessionModes.modelName("opus") == "Opus")
        #expect(SessionModes.modelName("opus[1m]") == "Opus")
        #expect(SessionModes.modelName("claude-opus-5-5[1m]") == "Opus 5.5")
        #expect(SessionModes.modelName("claude-fable-5-2[1m]") == "Fable 5.2")
        #expect(SessionModes.modelName("claude-sonnet-5-5[1m]") == "Sonnet 5.5")
        #expect(SessionModes.modelName("claude-opus-5-5-20260101") == "Opus 5.5")
        #expect(SessionModes.modelName("claude-3-5-sonnet-20241022") == "Sonnet")
        #expect(SessionModes.modelName("openai/gpt-5.5") == "5.5")
        #expect(SessionModes.modelName("") == nil)
        #expect(SessionModes.modelName(nil) == nil)
    }

    @Test(arguments: [
        ("gpt-6.1-sol", "6.1 Sol"),
        ("gpt-6.1", "6.1"),
        ("openai/gpt-6.1-sol", "6.1 Sol"),
        ("GPT-6.1-SOL", "6.1 Sol"),
        ("gpt-5.5-codex", "5.5 Codex"),
        ("gpt-5.5-mini", "5.5 Mini"),
        ("gpt-4o-mini", "4o Mini"),
        ("gpt-6.1-sol-codex-mini-preview-2026-11-05", "6.1 Sol Codex Mini Preview 2026 11 05"),
        ("gpt-6.1-sol[1m]", "6.1 Sol[1m]"),
        ("gpt-", "gpt-"),
        ("gpt--6.1", "gpt--6.1"),
        ("my-gpt-6.1-sol", "my-gpt-6.1-sol"),
        ("glm-5.3", "glm-5.3"),
        ("deepseek-v4", "deepseek-v4")
    ])
    func gptDisplayNames(raw: String, expected: String) {
        #expect(SessionModes.modelName(raw) == expected)
        #expect(SessionModes.composerTitle(model: SessionModes.modelName(raw), effort: "高")?.spoken == "\(expected) 高")
    }

    @Test func emptyAndUnknownNames() {
        #expect(SessionModes.modelName("   ") == nil)
        #expect(SessionModes.modelName("unknown-model") == "unknown-model")
        #expect(SessionModes.composerTitle(model: SessionModes.modelName(""), effort: "高")?.spoken == "思考 高")
        #expect(SessionModes.composerTitle(model: SessionModes.modelName(nil), effort: nil) == nil)
    }

    @Test func titles() {
        #expect(SessionModes.composerTitle(model: "Opus 5.5", effort: "高") == .init(model: "Opus 5.5", effort: "高"))
        #expect(SessionModes.composerTitle(model: "Opus 5.5", effort: "高")?.spoken == "Opus 5.5 高") // no 「·」 (#1117)
        #expect(SessionModes.composerTitle(model: "gpt-5.5", effort: nil) == .init(model: "gpt-5.5"))
        #expect(SessionModes.composerTitle(model: "gpt-5.5", effort: nil)?.spoken == "gpt-5.5")
        #expect(SessionModes.composerTitle(model: nil, effort: "很高") == .init(model: "思考", effort: "很高"))
        #expect(SessionModes.composerTitle(model: nil, effort: nil) == nil)
    }
}
