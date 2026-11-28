import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Word lookup (#10977, docs/apple/ios.md "Word lookup (text_lookup)"): the model against a FakeHub (inert without the
/// feature or an extension, the lookup, Save / Saved and their rollback, reading again on changes and in front) and the
/// rule for which tapped characters are a word to look up.
@MainActor struct TextLookupModelTests {
    private static let state = #"{"available":true,"extension":"relingo","contribution":"relingo","name":"Relingo","underline_saved":false,"words":["Context"]}"#
    private static let entry = #"{"found":true,"word":"deployed","lemma":"deploy","phonetic":"/dɪˈplɔɪ/","senses":[{"pos":"v.","text":"to put into use"}],"saved":false}"#

    private final class Clock { var now = Date(timeIntervalSince1970: 1_800_000_000) }

    private func model(_ hub: FakeHub, features: Set<String> = [TextLookupModel.feature], clock: Clock = Clock()) -> TextLookupModel {
        let client = hub.client
        return TextLookupModel(deps: .init(api: { client }, features: { features }, now: { clock.now }))
    }

    @Test func inertWithoutTheFeature() async {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: Self.state)
        let m = model(hub, features: [])
        m.refresh()
        await m.settle()
        #expect(!m.active)
        #expect(hub.sent("GET", TextLookupModel.path).isEmpty, "read without the feature")
        await #expect(throws: TextLookupModel.Failure.self) { try await m.lookup("deploy", context: nil) }
        #expect(hub.sent("POST", TextLookupModel.path + "/lookup").isEmpty)
    }

    @Test func inertWhenNoExtensionAnswers() async {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: #"{"available":false,"underline_saved":false,"words":[]}"#)
        let m = model(hub)
        m.refresh()
        await m.settle()
        #expect(!m.active && !m.underlines && m.saved.isEmpty)
    }

    @Test func anOlderManagerWithoutTheRouteIsInert() async {
        let hub = FakeHub() // no route: 404
        let m = model(hub)
        m.refresh()
        await m.settle()
        #expect(!m.active)
    }

    @Test func readsTheStateAndTheSavedWords() async {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: Self.state)
        let m = model(hub)
        m.refresh()
        await m.settle()
        #expect(m.active)
        #expect(m.providerName == "Relingo")
        #expect(m.saved == ["context"])
        #expect(!m.underlines, "underline is off unless the extension asks")
    }

    @Test func looksUpWithContextAndCaches() async throws {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: Self.state)
        hub.on("POST", TextLookupModel.path + "/lookup", json: Self.entry)
        let m = model(hub)
        m.refresh()
        await m.settle()
        let e = try await m.lookup("deployed", context: "We deployed it.")
        #expect(e.found && e.lemma == "deploy" && e.phonetic == "/dɪˈplɔɪ/" && e.senses.count == 1)
        #expect(e.key == "deploy")
        let body = try #require(hub.sent("POST", TextLookupModel.path + "/lookup").first?.body)
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["word": "deployed", "context": "We deployed it."])
        _ = try await m.lookup("Deployed", context: nil)
        #expect(hub.sent("POST", TextLookupModel.path + "/lookup").count == 1, "a second lookup of the same word is cached")
    }

    @Test func saveAndRemoveShowAtOnceAndReachTheHub() async throws {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: Self.state)
        hub.on("POST", TextLookupModel.path + "/lookup", json: Self.entry)
        hub.on("POST", TextLookupModel.path + "/words", json: #"{"saved":true}"#)
        hub.on("DELETE", TextLookupModel.path + "/words/deploy", json: #"{"saved":false}"#)
        let m = model(hub)
        m.refresh()
        await m.settle()
        let e = try await m.lookup("deployed", context: nil)
        #expect(!m.isSaved(e))
        try await m.save(e, context: "We deployed it.", source: "chat")
        #expect(m.isSaved(e))
        let body = try #require(hub.sent("POST", TextLookupModel.path + "/words").first?.body)
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["word": "deploy", "context": "We deployed it.", "source": "chat"])
        try await m.remove(e)
        #expect(!m.isSaved(e))
        #expect(hub.sent("DELETE", TextLookupModel.path + "/words/deploy").count == 1)
    }

    @Test func aFailedSaveGoesBack() async throws {
        let hub = FakeHub()
        hub.on("GET", TextLookupModel.path, json: Self.state)
        hub.on("POST", TextLookupModel.path + "/words") { _ in throw APIError(status: 502, message: "no answer") }
        let m = model(hub)
        m.refresh()
        await m.settle()
        let e = TextLookupEntry(found: true, word: "deploy")
        await #expect(throws: APIError.self) { try await m.save(e, context: nil, source: nil) }
        #expect(!m.isSaved(e))
    }

    @Test func unavailableOnLookupReadsTheStateAgain() async {
        let hub = FakeHub()
        var available = true
        hub.on("GET", TextLookupModel.path) { _ in
            Data((available ? Self.state : #"{"available":false,"underline_saved":false,"words":[]}"#).utf8)
        }
        hub.on("POST", TextLookupModel.path + "/lookup") { _ in
            throw APIError(status: 404, message: "off", fields: ["code": "text_lookup_unavailable"])
        }
        let m = model(hub)
        m.refresh()
        await m.settle()
        #expect(m.active)
        available = false
        await #expect(throws: TextLookupModel.Failure.self) { try await m.lookup("deploy", context: nil) }
        await m.settle()
        #expect(!m.active, "turned off elsewhere: the words are only words again")
    }

    @Test func refreshFollowsChangesAndForegroundIsThrottled() async {
        let hub = FakeHub()
        var underline = false
        hub.on("GET", TextLookupModel.path) { _ in
            Data(#"{"available":true,"name":"Relingo","underline_saved":\#(underline),"words":["deploy"]}"#.utf8)
        }
        let clock = Clock()
        let m = model(hub, clock: clock)
        m.refresh()
        await m.settle()
        #expect(!m.underlines)
        underline = true
        m.refresh() // text_lookup_changed
        await m.settle()
        #expect(m.underlines)
        let reads = hub.sent("GET", TextLookupModel.path).count
        clock.now += 10
        m.foreground()
        await m.settle()
        #expect(hub.sent("GET", TextLookupModel.path).count == reads, "back in front within a minute: no read")
        clock.now += TextLookupModel.foregroundInterval
        m.foreground()
        await m.settle()
        #expect(hub.sent("GET", TextLookupModel.path).count == reads + 1)
    }

    @Test func changesWhileReadingMakeOneMoreRead() async {
        let hub = FakeHub()
        let gate = AsyncStream<Void>.makeStream()
        var first = true
        hub.onAsync("GET", TextLookupModel.path) { _ in
            if first { first = false; for await _ in gate.stream { break } }
            return Data(Self.state.utf8)
        }
        let m = model(hub)
        m.refresh()
        // The first read is on its way (the hub has it) before the changes come.
        for _ in 0..<1000 where hub.sent("GET", TextLookupModel.path).isEmpty { await Task.yield() }
        m.refresh()
        m.refresh()
        gate.continuation.yield()
        await m.settle()
        #expect(hub.sent("GET", TextLookupModel.path).count == 2, "two changes while one read was out: one more read")
        #expect(m.active)
    }

    @Test func anotherProviderDropsTheCachedLookups() async throws {
        let hub = FakeHub()
        var provider = "relingo"
        hub.on("GET", TextLookupModel.path) { _ in
            Data(#"{"available":true,"extension":"\#(provider)","contribution":"\#(provider)","underline_saved":false,"words":[]}"#.utf8)
        }
        hub.on("POST", TextLookupModel.path + "/lookup", json: Self.entry)
        let m = model(hub)
        m.refresh()
        await m.settle()
        _ = try await m.lookup("deployed", context: nil)
        provider = "other"
        m.refresh() // text_lookup_changed: another extension answers now
        await m.settle()
        _ = try await m.lookup("deployed", context: nil)
        #expect(hub.sent("POST", TextLookupModel.path + "/lookup").count == 2, "the other provider is asked, not the old cache")
    }

    @Test func anotherHubStartsOverAndDropsTheOldHubsRead() async throws {
        let old = FakeHub(), new = FakeHub()
        let gate = AsyncStream<Void>.makeStream()
        var first = true
        old.onAsync("GET", TextLookupModel.path) { _ in
            if first { first = false; for await _ in gate.stream { break } }
            return Data(Self.state.utf8)
        }
        old.on("POST", TextLookupModel.path + "/lookup", json: Self.entry)
        new.on("GET", TextLookupModel.path, json: #"{"available":true,"extension":"relingo","contribution":"relingo","underline_saved":false,"words":["deploy"]}"#)
        new.on("POST", TextLookupModel.path + "/lookup", json: Self.entry)
        var hub = old
        let m = TextLookupModel(deps: .init(api: { hub.client }, features: { [TextLookupModel.feature] }))
        m.refresh()
        for _ in 0..<1000 where old.sent("GET", TextLookupModel.path).isEmpty { await Task.yield() }
        hub = new
        m.reset() // the old hub's read is still out
        await m.settle()
        gate.continuation.yield()
        for _ in 0..<100 { await Task.yield() }
        #expect(m.saved == ["deploy"], "the old hub's late answer does not replace the new hub's words")
        _ = try await m.lookup("deployed", context: nil)
        #expect(new.sent("POST", TextLookupModel.path + "/lookup").count == 1)
        #expect(old.sent("POST", TextLookupModel.path + "/lookup").isEmpty)
    }

    @Test func anotherHubWithSameProviderDropsCachedEntriesAndLateLookups() async throws {
        let old = FakeHub(), new = FakeHub()
        let oldEntry = #"{"found":true,"word":"deployed","lemma":"deploy","senses":[{"pos":"v.","text":"old hub meaning"}],"saved":false}"#
        let newEntry = #"{"found":true,"word":"deployed","lemma":"deploy","senses":[{"pos":"v.","text":"new hub meaning"}],"saved":false}"#
        let slowGate = AsyncStream<Void>.makeStream()
        old.on("GET", TextLookupModel.path, json: Self.state)
        old.onAsync("POST", TextLookupModel.path + "/lookup") { req in
            let text = req.body.flatMap { String(data: $0, encoding: .utf8) } ?? ""
            if text.contains("pending") {
                for await _ in slowGate.stream { break }
                return Data(#"{"found":true,"word":"pending","senses":[{"text":"old pending"}],"saved":true}"#.utf8)
            }
            return Data(oldEntry.utf8)
        }
        new.on("GET", TextLookupModel.path, json: Self.state)
        new.on("POST", TextLookupModel.path + "/lookup") { req in
            let text = req.body.flatMap { String(data: $0, encoding: .utf8) } ?? ""
            if text.contains("pending") {
                return Data(#"{"found":true,"word":"pending","senses":[{"text":"new pending"}],"saved":false}"#.utf8)
            }
            return Data(newEntry.utf8)
        }
        var hub = old
        let m = TextLookupModel(deps: .init(api: { hub.client }, features: { [TextLookupModel.feature] }))
        m.refresh()
        await m.settle()
        let fromOld = try await m.lookup("deployed", context: nil)
        #expect(fromOld.senses.first?.text == "old hub meaning")

        // Start a slow lookup on the old hub, then switch to another hub that also has the same provider available.
        let inFlight = Task { try await m.lookup("pending", context: nil) }
        for _ in 0..<1000 where old.sent("POST", TextLookupModel.path + "/lookup").count < 2 { await Task.yield() }
        hub = new
        m.reset()
        await m.settle()
        slowGate.continuation.yield()
        _ = try await inFlight.value

        // Neither the previously cached entry nor the late in-flight lookup from the old hub leaks into the new hub.
        let fromNew = try await m.lookup("deployed", context: nil)
        #expect(fromNew.senses.first?.text == "new hub meaning")
        let pendingNew = try await m.lookup("pending", context: nil)
        #expect(pendingNew.senses.first?.text == "new pending")
        #expect(!m.saved.contains("pending"))
        #expect(new.sent("POST", TextLookupModel.path + "/lookup").count == 2)
    }
}

struct TextLookupWordsTests {
    private func word(_ text: String, at needle: String, offset: Int = 0) -> String? {
        let r = (text as NSString).range(of: needle)
        return TextLookupWords.word(in: text, at: r.location + offset)?.word
    }

    @Test func plainWordsAndTheirPunctuation() {
        #expect(word("Deploy the widget.", at: "Deploy") == "Deploy")
        #expect(word("Deploy the widget.", at: "widget", offset: 5) == "widget")
        #expect(word("(context)", at: "context") == "context")
        #expect(word("It's a well-known fix", at: "It's") == "It's")
        #expect(word("a well-known fix", at: "known") == "well-known")
        #expect(word("don’t stop", at: "don") == "don't", "the typographic apostrophe is looked up as the plain one")
        #expect(word("café crème", at: "café") == "café")
        #expect(word("'quoted'", at: "quoted") == "quoted")
    }

    @Test func codeAddressesAndNoiseAreNoWords() {
        #expect(word("see example.com now", at: "com") == nil)
        #expect(word("run snake_case here", at: "snake") == nil)
        #expect(word("open src/main.swift", at: "main") == nil)
        #expect(word("mail me@example.org", at: "me") == nil)
        #expect(word("ChatView redraws", at: "Chat") == nil, "camelCase is an identifier")
        #expect(word("v2 shipped", at: "v") == nil)
        #expect(word("a b", at: "a") == nil, "one letter")
        #expect(word("今天 Deploy 了", at: "今") == nil, "not a Latin letter")
        #expect(word("a -- b", at: "--") == nil)
        #expect(word(String(repeating: "a", count: 41), at: "a") == nil, "longer than 40")
        #expect(word(String(repeating: "a", count: 40), at: "a") != nil)
        #expect(word("x", at: "x", offset: 5) == nil, "past the end")
    }

    @Test func contextIsTheSentenceAroundTheWord() {
        let text = "First sentence here. We deployed the widget today! Last one?"
        let r = (text as NSString).range(of: "deployed")
        #expect(TextLookupWords.context(in: text, around: r) == "We deployed the widget today!")
        let long = String(repeating: "word ", count: 200) + "target " + String(repeating: "more ", count: 200)
        let lr = (long as NSString).range(of: "target")
        let c = TextLookupWords.context(in: long, around: lr)
        #expect(c.contains("target") && c.count <= TextLookupWords.contextLimit + 10)
    }

    @Test func savedWordsAreFoundAsWholeWords() {
        let text = "Context matters; the contexts and pre-context differ. context.swift is code."
        let found = TextLookupWords.occurrences(of: ["context"], in: text).map { (text as NSString).substring(with: $0) }
        #expect(found == ["Context"], "only the whole word, never inside another or in a file name: \(found)")
    }
}
