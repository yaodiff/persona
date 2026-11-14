import Foundation
import Testing
@testable import Persona
import PersonaWire

/// When a conversation offers Voice Chat (docs/assistant/voice.md 3.1, 6.1, 6.10「VoiceAvailability」), the model a call starts with
/// (「未验证」 is usable, a model no longer offered is not), and the extension's /info, /models and /voices.
struct VoiceAvailabilityTests {
    private static func session(_ json: String) -> Session {
        try! JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private static let agent = session(#"{"machine_id":"m1","session_id":"s1","status":"idle"}"#)
    private static let voice = try! JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"voice","name":"实时语音","kind":"voice","status":"running"}"#.utf8))
    private static let protocol2 = VoiceInfoState.info(VoiceInfo(protocolVersion: 2, features: ["target", "runs"]))

    private func extensionList(_ json: String) -> [HubExtension] {
        try! JSON.decoder.decode([HubExtension].self, from: Data(json.utf8))
    }

    @Test func notOnTheMac() {
        #expect(VoiceAvailability.evaluate(platform: .other, session: Self.agent, extensions: [Self.voice], info: Self.protocol2) == .unsupported)
        #if os(iOS) || os(visionOS)
        #expect(VoicePlatform.current == .phone)
        #else
        #expect(VoicePlatform.current == .other)
        #endif
    }

    /// The composer's rule (`Session.canSend`): a conversation with no source takes messages without capabilities; a
    /// source must say it does, so the entry never shows where the composer says 「仅查看」.
    @Test func whetherTheConversationTakesMessages() {
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: Self.protocol2).isAvailable,
                "no source, no capabilities")
        let sourcedWithoutCapabilities = Self.session(#"{"machine_id":"m1","session_id":"s1","status":"idle","source_kind":"runtime"}"#)
        #expect(!sourcedWithoutCapabilities.canSend)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: sourcedWithoutCapabilities, extensions: [Self.voice], info: Self.protocol2)
            == .readOnly, "a source that did not say it takes messages")
        let readOnly = Self.session(#"{"machine_id":"m1","session_id":"s1","status":"idle","source_kind":"codex_history","capabilities":{"read":true,"send":false},"read_only_reason":"历史记录"}"#)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: readOnly, extensions: [Self.voice], info: Self.protocol2) == .readOnly)
        let sendable = Self.session(#"{"machine_id":"m1","session_id":"s1","status":"idle","source_kind":"codex_app_server","capabilities":{"send":true}}"#)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: sendable, extensions: [Self.voice], info: Self.protocol2).isAvailable)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: nil, extensions: [Self.voice], info: Self.protocol2) == .noConversation)
    }

    @Test func anOlderExtensionHasNoEntry() {
        let old = VoiceInfoState.info(VoiceInfo(protocolVersion: 1))
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: old) == .outdated)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: .missing) == .outdated)
    }

    @Test func noExtensionNeedsSetupLimitedDeviceAndNotYetKnown() {
        let others = extensionList(#"[{"id":"notes","name":"n","kind":"connector","status":"running"},{"id":"voice-off","name":"v","kind":"voice","status":"disabled"}]"#)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: others, info: nil) == .noExtension)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [], info: nil) == .noExtension)
        let waiting = extensionList(#"[{"id":"voice","name":"v","kind":"voice","status":"needs_setup","needs_setup":["实时语音服务的地址和 Key"]}]"#)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: waiting, info: nil) == .needsSetup(["实时语音服务的地址和 Key"]))
        let unset = VoiceInfoState.info(VoiceInfo(protocolVersion: 2, setup: ["还缺地址"]))
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: unset) == .needsSetup(["还缺地址"]))
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: .forbidden) == .limitedDevice)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: nil, info: nil) == .unknown)
        #expect(VoiceAvailability.evaluate(platform: .phone, session: Self.agent, extensions: [Self.voice], info: .failed("offline")) == .unknown)
    }

    @Test func picksTheFirstRunningVoiceExtensionWithASafeID() {
        let list = extensionList(#"[{"id":"notes","name":"n","kind":"notes","status":"running"},{"id":"voice-off","name":"v","kind":"voice","status":"error"},{"id":"../x","name":"bad","kind":"voice","status":"running"},{"id":"voice","name":"v","kind":"voice","status":"running"},{"id":"voice2","name":"v2","kind":"voice","status":"running"}]"#)
        #expect(VoiceAvailability.pick(list)?.id == "voice")
        #expect(VoiceAvailability.validID("gpt_live-2"))
        #expect(!VoiceAvailability.validID("Voice") && !VoiceAvailability.validID("") && !VoiceAvailability.validID("a/b"))
    }

    // MARK: Model

    @Test func modelResolution() {
        let models = VoiceModels(models: [.init(id: "custom/gpt-live-1"), .init(id: "custom/mini")], default: "custom/mini")
        #expect(VoiceModelPick.resolve(choice: nil, models: models) == .ready(nil), "an older hub: the extension's default")
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: nil, status: nil), models: models) == .ready(nil))
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: "custom/gpt-live-1", status: "未验证"), models: models)
            == .ready("custom/gpt-live-1"), "未验证 is usable")
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: "custom/gpt-live-1", status: "读不到扩展模型列表"), models: models)
            == .unavailable("读不到扩展模型列表"))
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: "custom/gone", status: nil), models: models) == .unavailable("custom/gone"),
                "a model the extension no longer offers")
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: "custom/gone", status: nil), models: VoiceModels()) == .ready("custom/gone"),
                "an empty list cannot tell")
        #expect(VoiceModelPick.resolve(choice: VoiceModelChoice(model: "custom/gone", status: nil), models: nil) == .ready("custom/gone"))
        #expect(VoiceModelChoice(model: "  ", status: " 未验证 ") == VoiceModelChoice(model: nil, status: nil))
    }

    // MARK: Client

    @Test func infoDecodesOneOrManySetupReasons() throws {
        let one = try JSON.decoder.decode(VoiceInfo.self, from: Data(#"{"protocol":2,"features":["target"],"setup":"还缺 Key"}"#.utf8))
        #expect(one == VoiceInfo(protocolVersion: 2, features: ["target"], setup: ["还缺 Key"]))
        let many = try JSON.decoder.decode(VoiceInfo.self, from: Data(#"{"protocol":2,"setup":["a","b"],"warnings":["w"]}"#.utf8))
        #expect(many.setup == ["a", "b"] && many.warnings == ["w"])
        #expect(try JSON.decoder.decode(VoiceInfo.self, from: Data("{}".utf8)).protocolVersion == 1)
    }

    @Test func infoModelsVoicesAndTheUseOverTheExtensionRoutes() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/voice/info", json: #"{"protocol":2,"features":["target","runs","asks","resume","suspend"]}"#)
        hub.on("GET", "/api/v1/extensions/voice/models", json: #"{"models":[{"id":"custom/gpt-live-1","name":"GPT-Live"}],"default":"custom/gpt-live-1"}"#)
        hub.on("GET", "/api/v1/extensions/voice/voices", json: #"{"voices":[{"id":"marin","gender":"female","languages":["zh","en"]}],"default":"marin"}"#)
        hub.on("GET", "/api/v1/model-config/purposes/plugin.voice", json: #"{"purpose":{"id":"plugin.voice","name":"实时语音","group":"语音","kind":"plugin","override":{},"effective":{"model":"custom/gpt-live-1"},"revision":0,"status":"未验证"},"providers":[]}"#)
        var api = hub.client
        api.extensionsAPI = true
        #expect(await api.voiceInfo("voice") == .info(VoiceInfo(protocolVersion: 2, features: ["target", "runs", "asks", "resume", "suspend"])))
        let models = try await api.voiceModels("voice")
        #expect(models.models.map(\.id) == ["custom/gpt-live-1"] && models.default == "custom/gpt-live-1")
        let voices = try await api.voiceVoices("voice", model: "custom/gpt-live-1")
        #expect(voices.voices.first?.id == "marin" && voices.voices.first?.languages == ["zh", "en"] && voices.default == "marin")
        #expect(hub.sent("GET", "/api/v1/extensions/voice/voices").first?.query == ["model": "custom/gpt-live-1"])
        #expect(try await api.voiceModelChoice("voice") == VoiceModelChoice(model: "custom/gpt-live-1", status: nil))
    }

    @Test func aHubThatLacksThemSaysSo() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions/limited/info") { _ in throw APIError(status: 403, message: "forbidden") }
        hub.on("GET", "/api/v1/extensions/down/info") { _ in throw APIError(status: 502, message: "bad gateway") }
        var api = hub.client
        api.extensionsAPI = true
        #expect(await api.voiceInfo("voice") == .missing)
        #expect(await api.voiceInfo("limited") == .forbidden)
        #expect(await api.voiceInfo("down") == .failed("bad gateway"))
        #expect(try await api.voiceModels("voice") == VoiceModels())
        #expect(try await api.voiceModelChoice("voice") == nil)
    }

    // MARK: What each hub said, kept a minute

    @MainActor @Test func supportAsksOncePerMinutePerGroup() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: #"{"extensions":[{"id":"voice","name":"实时语音","kind":"voice","status":"running"}]}"#)
        hub.on("GET", "/api/v1/extensions/voice/info", json: #"{"protocol":2,"features":["target"]}"#)
        var api = hub.client
        api.extensionsAPI = true
        let support = VoiceSupport()
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .unknown)
        await support.refresh(group: "", api: api, now: now, platform: .phone)
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .available(extensionID: "voice"))
        #expect(support.availability(for: Self.agent, group: "other", platform: .phone) == .unknown, "per group")
        await support.refresh(group: "", api: api, now: now.addingTimeInterval(59), platform: .phone)
        #expect(hub.sent("GET", "/api/v1/extensions/voice/info").count == 1, "younger than a minute: not asked again")
        await support.refresh(group: "", api: api, now: now.addingTimeInterval(61), platform: .phone)
        #expect(hub.sent("GET", "/api/v1/extensions/voice/info").count == 2)
        support.forget()
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .unknown)
        await support.refresh(group: "", api: api, now: now, platform: .other)
        #expect(hub.sent("GET", "/api/v1/extensions").count == 2, "nothing asked where there are no calls")
    }

    /// Back in the foreground the hub is asked again, but the entry keeps its last answer meanwhile: it does not blink
    /// out and reflow the composer row.
    @MainActor @Test func backInTheForegroundTheLastAnswerStaysUntilTheNext() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/extensions", json: #"{"extensions":[{"id":"voice","name":"实时语音","kind":"voice","status":"running"}]}"#)
        hub.on("GET", "/api/v1/extensions/voice/info", json: #"{"protocol":2}"#)
        var api = hub.client
        api.extensionsAPI = true
        let support = VoiceSupport()
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        await support.refresh(group: "", api: api, now: now, platform: .phone)
        support.expire()
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .available(extensionID: "voice"))
        hub.on("GET", "/api/v1/extensions/voice/info", json: #"{"protocol":2,"setup":"还缺 Key"}"#)
        await support.refresh(group: "", api: api, now: now.addingTimeInterval(1), platform: .phone)
        #expect(hub.sent("GET", "/api/v1/extensions/voice/info").count == 2, "asked again within the minute")
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .needsSetup(["还缺 Key"]))
    }

    @MainActor @Test func supportOnAHubWithoutExtensions() async {
        let hub = FakeHub()
        let support = VoiceSupport()
        await support.refresh(group: "", api: hub.client, platform: .phone)
        #expect(support.availability(for: Self.agent, group: "", platform: .phone) == .noExtension)
    }
}
