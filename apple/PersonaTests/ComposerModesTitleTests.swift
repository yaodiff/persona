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
        #expect(SessionModes.modelName("openai/gpt-5.5") == "GPT-5.5")
        #expect(SessionModes.modelName("") == nil)
        #expect(SessionModes.modelName(nil) == nil)
    }

    @Test(arguments: [
        ("gpt-6.1-sol", "GPT-6.1 Sol"),
        ("gpt-6.1", "GPT-6.1"),
        ("openai/gpt-6.1-sol", "GPT-6.1 Sol"),
        ("GPT-6.1-SOL", "GPT-6.1 Sol"),
        ("gpt-5.5-codex", "GPT-5.5 Codex"),
        ("gpt-5.5-mini", "GPT-5.5 Mini"),
        ("gpt-4o-mini", "GPT-4o Mini"),
        ("gpt-6.1-sol-codex-mini-preview-2026-11-05", "GPT-6.1 Sol Codex Mini Preview 2026 11 05"),
        ("gpt-6.1-sol[1m]", "GPT-6.1 Sol[1m]"),
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

    @Test(arguments: [
        ("gemini-4-carbon-high", "Gemini 4 Carbon"),
        ("gemini-4-neon-xhigh", "Gemini 4 Neon"),
        ("gemini-3.8-flash-medium", "Gemini 3.8 Flash"),
        ("gemini-4-carbon-low", "Gemini 4 Carbon"),
        ("gemini-4-neon-max", "Gemini 4 Neon"),
        ("google/GEMINI-4-CARBON", "Gemini 4 Carbon"),
        ("gemini-pro-orchestrator", "Gemini Pro Orchestrator"),
        ("gemini-pro-orchestrator-neon", "Gemini Pro Orchestrator Neon"),
        ("gemini-pro-orchestrator-neon-high", "Gemini Pro Orchestrator Neon"),
        ("gemini-4-flash-high-preview", "Gemini 4 Flash High Preview"),
        ("gemini--4", "gemini--4"),
        ("Gemini 4 Carbon", "Gemini 4 Carbon")
    ])
    func geminiDisplayNames(raw: String, expected: String) {
        let model = SessionModes.modelName(raw)
        #expect(model == expected)
        #expect(SessionModes.composerTitle(model: model, effort: "High")?.spoken == "\(expected) High")
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

    @Test func starterLauncherLegacyDefaultMatchesAnEmptyModel() {
        let modes = StarterComposer.ShellModes(launcher: .constant("old"), launcherModel: .constant(nil), mode: .constant(""), effort: .constant(""))
        let app = AppModel()
        let legacy = StarterModesMenu.title(modes: modes, launchers: [Launcher(name: "old", model: "默认")], model: app)
        #expect(legacy == StarterModesMenu.title(modes: modes, launchers: [Launcher(name: "old")], model: app))
        #expect(legacy?.model != "默认")
        let explicit = StarterModesMenu.title(modes: modes, launchers: [Launcher(name: "old", model: "claude-opus-5-5")], model: app)
        #expect(explicit?.model == "Opus 5.5")
    }
}
