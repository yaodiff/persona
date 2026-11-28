import Foundation
import Testing
@testable import Persona
import PersonaKit

/// Claude Code's live status can report a model request retry while the turn is still working (#887). A watched
/// Agent chat shows that error and its attempt count immediately, and shows nothing extra for ordinary live work.
@MainActor
struct SessionLiveRetryTests {
    private static let retryError = "429 Synthetic rate limit for retry capture"
    private static let retryObject = #"{"verb":"Frosting","elapsed_s":16,"tokens_down":22800,"thinking":true,"thought_s":3,"at":"2026-11-18T07:30:00Z","retry_error":"429 Synthetic rate limit for retry capture","retry_attempt":5,"retry_max":10}"#
    private static let ordinaryObject = #"{"verb":"Frosting","elapsed_s":17,"tokens_down":23000}"#
    private static let compactionObject = #"{"verb":"Compacting conversation","elapsed_s":7,"at":"2026-11-18T07:30:00Z"}"#
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent()

    private func live(_ object: String = Self.retryObject) throws -> SessionLive {
        try JSONDecoder().decode(SessionLive.self, from: Data(object.utf8))
    }

    private func presentation(_ live: SessionLive?, working: Bool = true, subscribed: Bool = true,
                              lang: String = "en") -> SessionLiveRetryPresentation? {
        SessionLiveRetryPresentation.of(live, working: working, subscribed: subscribed, lang: lang)
    }

    @Test func watchedChatsRequestTheSessionLiveFeature() throws {
        let query = LiveConnection.queryItems(EventCursor(), sessionLive: true)
        let value = try #require(query.first { $0.name == "features" }?.value)
        #expect(value.split(separator: ",").contains("session_live"))
    }

    @Test func decodesRetryAndAllExistingLiveFields() throws {
        let data = Data(#"{"type":"live","machine_id":"mac","session_id":"agent-1","live":{"verb":"Frosting","elapsed_s":16,"tokens_down":22800,"thinking":true,"thought_s":3,"at":"2026-11-18T07:30:00Z","retry_error":"429 Synthetic rate limit for retry capture","retry_attempt":5,"retry_max":10}}"#.utf8)
        guard case .live(let key, let decoded)? = ServerEvent.decode(data), let decoded else {
            Issue.record("live retry event not decoded")
            return
        }
        #expect(key == SessionKey(machineID: "mac", sessionID: "agent-1"))
        #expect(decoded.verb == "Frosting")
        #expect(decoded.elapsedS == 16)
        #expect(decoded.tokensDown == 22_800)
        #expect(decoded.thinking == true)
        #expect(decoded.thoughtS == 3)
        #expect(decoded.at == "2026-11-18T07:30:00Z")
        #expect(decoded.retryError == Self.retryError)
        #expect(decoded.retryAttempt == 5)
        #expect(decoded.retryMax == 10)
    }

    @Test func decodesLiveNullAsAClearEvent() throws {
        let data = Data(#"{"type":"live","machine_id":"mac","session_id":"agent-1","live":null}"#.utf8)
        guard case .live(let key, let decoded)? = ServerEvent.decode(data) else {
            Issue.record("live null event not decoded")
            return
        }
        #expect(key == SessionKey(machineID: "mac", sessionID: "agent-1"))
        #expect(decoded == nil)
    }

    @Test func compactionUsesTheSameLivePushAndClearsWithIt() throws {
        let data = Data(#"{"type":"live","machine_id":"mac","session_id":"agent-1","live":{"verb":"Compacting conversation","elapsed_s":7,"at":"2026-11-18T07:30:00Z"}}"#.utf8)
        guard case .live(let key, let live)? = ServerEvent.decode(data), let live else {
            Issue.record("compaction live event not decoded")
            return
        }
        #expect(key == SessionKey(machineID: "mac", sessionID: "agent-1"))
        let shown = try #require(SessionLiveCompactionPresentation.of(live))
        #expect(shown.elapsedS == 7)
        #expect(SessionLiveCompactionPresentation.of(try self.live(Self.compactionObject)) == shown)
        #expect(SessionLiveCompactionPresentation.of(nil) == nil, "live: null clears the work line")
        #expect(SessionLiveCompactionPresentation.of(try self.live(Self.ordinaryObject)) == nil)
        #expect(SessionLiveCompactionPresentation.of(live, subscribed: false) == nil)
    }

    @Test func laterLivePushesClearAStaleCompactionPresentation() async throws {
        let hub = FakeHub()
        let model = AppModel()
        model.apiForTests = hub.client
        let key = SessionKey(machineID: "box", sessionID: "s1")
        let object = #"{"machine_id":"box","session_id":"s1","status":"working","state":"working","pane_id":"w1:p1"}"#
        let session = try JSONDecoder().decode(Session.self, from: Data(object.utf8))
        model.applySessions([MachineRef(group: "", id: "box"): [session]])
        model.subscribe(key, by: UUID())

        model.handle(.live(key, try live(Self.compactionObject)), group: "")
        #expect(model.liveCompaction(key)?.elapsedS == 7)
        model.handle(.live(key, try live(Self.ordinaryObject)), group: "")
        #expect(model.liveCompaction(key) == nil, "a later ordinary live line replaces stale compaction")
        model.handle(.live(key, try live(Self.compactionObject)), group: "")
        model.handle(.live(key, nil), group: "")
        #expect(model.liveCompaction(key) == nil, "live: null clears compaction")
    }

    @Test func compactionWordsMatchTheWebExactly() throws {
        let catalogData = try Data(contentsOf: Self.repo.appending(path: "apple/Shared/Localization/Localizable.xcstrings"))
        let catalog = try #require((try JSONSerialization.jsonObject(with: catalogData) as? [String: Any])?["strings"] as? [String: Any])
        let entry = try #require(catalog["Compacting context…"] as? [String: Any])
        let localizations = try #require(entry["localizations"] as? [String: Any])
        for language in ["en", "ja", "zh-Hans"] {
            let apple = (((localizations[language] as? [String: Any])?["stringUnit"] as? [String: Any])?["value"] as? String)
            let webData = try Data(contentsOf: Self.repo.appending(path: "web/src/i18n/locales/\(language)/chat-runningstatusbar.json"))
            let web = (try JSONSerialization.jsonObject(with: webData) as? [String: Any])?["compacting"] as? String
            #expect(apple == web, "\(language) Apple and web compaction words differ")
        }
    }

    @Test(arguments: [
        ("en", "Retrying model request (5/10)"),
        ("zh-Hans", "正在重试模型请求（5/10）"),
        ("ja", "モデルリクエストを再試行中（5/10）"),
    ])
    func showsTheObservedErrorAndLocalizedAttemptBeforeTurnEnd(lang: String, attemptText: String) throws {
        let shown = try #require(presentation(live(), lang: lang))
        #expect(shown.error.raw == Self.retryError)
        #expect(shown.attempt == 5)
        #expect(shown.maxAttempts == 10)
        #expect(shown.attemptText == attemptText)
    }

    @Test func knownRateLimitUsesAPIErrorInfosKnownClassification() throws {
        let shown = try #require(presentation(live()))
        let known = try #require(APIErrorInfo.parse("API Error: \(Self.retryError)"))
        #expect(shown.error.status == 429)
        #expect(shown.error.reason == known.reason)
        #expect(shown.error.transient == true)
        #expect(shown.error.raw == Self.retryError)
    }

    @Test func genericRetryErrorUsesAPIErrorInfosGenericClassification() throws {
        let raw = "Synthetic request failure"
        let object = #"{"retry_error":"Synthetic request failure","retry_attempt":2,"retry_max":4}"#
        let shown = try #require(presentation(live(object)))
        let generic = try #require(APIErrorInfo.parse("API Error: \(raw)"))
        #expect(shown.error.status == nil)
        #expect(shown.error.signature == generic.signature)
        #expect(shown.error.transient == nil)
        #expect(shown.error.raw == raw)
    }

    @Test func ordinaryWorkNullTurnEndAndSubscriptionCleanupShowNoRetry() throws {
        let ordinary = try live(Self.ordinaryObject)
        let retry = try live()
        #expect(presentation(ordinary) == nil, "ordinary live work preserves the existing status UI")
        #expect(presentation(nil) == nil, "live: null clears a previous retry")
        #expect(presentation(retry, working: false) == nil, "turn end clears a previous retry")
        #expect(presentation(retry, subscribed: false) == nil, "subscription cleanup clears a previous retry")
    }

    @Test func directCloseClearsRetryWithoutAnotherSessionListUpdate() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/box/s1/close", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let key = SessionKey(machineID: "box", sessionID: "s1")
        let object = #"{"machine_id":"box","session_id":"s1","status":"working","state":"working","pane_id":"w1:p1"}"#
        let session = try JSONDecoder().decode(Session.self, from: Data(object.utf8))
        model.applySessions([MachineRef(group: "", id: "box"): [session]])
        model.subscribe(key, by: UUID())
        model.handle(.live(key, try live()), group: "")
        #expect(model.liveRetry(key) != nil)
        #expect(await model.close(key))
        #expect(model.liveRetry(key) == nil)
    }
}
