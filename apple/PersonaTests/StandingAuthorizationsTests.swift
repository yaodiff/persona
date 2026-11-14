import Foundation
import Testing
@testable import Persona

/// Assistant Profile › Standing Authorizations (#7002): the hub's list in plain words, read and revoked through the page's
/// assistant, a revoke that leaves at once and comes back when the hub refuses. Made-up grants.
@MainActor
struct StandingAuthorizationsTests {
    private static let list = #"""
    {"revision":4,"authorizations":[
      {"id":"sa_one","machine_id":"laptop","directory":"/code/example","actions":["merge","deploy-test"],
       "conditions":["ci_green","Only the reviewed branch"],"quote":"Merge it when the checks pass.","source":"Main conversation",
       "granted_at":"2026-11-11T07:36:00.123456Z","reversible":true},
      {"id":"sa_two","machine_id":"laptop","directory":"/code/another","actions":["push","launch"],"conditions":[],
       "quote":"Push reviewed changes.","source":"Main conversation","granted_at":"2026-11-12T08:00:00Z","reversible":false},
      {"machine_id":"laptop","directory":"/code/no-id","actions":["merge"]}
    ]}
    """#

    private final class Toasts { var shown: [Toast] = [] }

    private func page(_ hub: FakeHub, shown: String? = nil, toasts: Toasts = Toasts()) -> StandingAuthorizationsModel {
        StandingAuthorizationsModel(deps: AssistantDeps(api: { hub.client.showing(shown) }, report: { _ in },
                                                        toast: { toasts.shown.append($0) }, open: { _ in }))
    }

    @Test func theListDecodesLenientlyAndLeavesOutAGrantWithoutAnID() throws {
        let decoded = try JSON.decoder.decode(StandingAuthorizationList.self, from: Data(Self.list.utf8))
        #expect(decoded.revision == 4)
        #expect(decoded.authorizations.map(\.id) == ["sa_one", "sa_two"], "one without an id can't be revoked")
        let one = decoded.authorizations[0]
        #expect(one.actions == ["merge", "deploy-test"] && one.reversible && one.folderName == "example")
        let granted = try #require(one.grantedAt), plain = try #require(ISO8601DateFormatter().date(from: "2026-11-11T07:36:00Z"))
        #expect(abs(granted.timeIntervalSince(plain)) < 1, "the hub's fractional seconds")
        let odd = try JSON.decoder.decode(StandingAuthorizationList.self, from: Data(#"{"authorizations":[{"id":"x","actions":"merge"}]}"#.utf8))
        #expect(odd.revision == 0 && odd.authorizations.first?.actions == [] && odd.authorizations.first?.grantedAt == nil)
    }

    @Test func aGrantReadsInPlainWords() throws {
        let grants = try JSON.decoder.decode(StandingAuthorizationList.self, from: Data(Self.list.utf8)).authorizations
        let merge = String(localized: "merge changes [Standing Authorization phrase]")
        let test = String(localized: "deploy to a test environment [Standing Authorization phrase]")
        let phrase = [merge, test].formatted(.list(type: .and))
        #expect(StandingAuthorizationText.actionsPhrase(grants[0]) == phrase)
        #expect(StandingAuthorizationText.actions(grants[0]) == phrase.prefix(1).uppercased() + phrase.dropFirst(), "a line starts with a capital")
        // An action this build doesn't know stays the hub's word; so does the owner's own condition.
        #expect(StandingAuthorizationText.actionsPhrase(grants[1]).contains("“launch”"))
        #expect(StandingAuthorizationText.action("launch") == "launch")
        #expect(StandingAuthorizationText.condition("ci_green") == String(localized: "All checks pass [Standing Authorization]"))
        #expect(StandingAuthorizationText.condition("Only the reviewed branch") == "Only the reviewed branch")
        #expect(StandingAuthorizationText.conditions(grants[1]) == nil, "no conditions, no line")
        #expect(StandingAuthorizationText.place(grants[0], machine: "Laptop") == String(localized: "In \("example") on \("Laptop") [Standing Authorization]"))
        let message = StandingAuthorizationText.revokeMessage(grants[0], machine: "Laptop")
        #expect(message.contains("example") && message.contains("Laptop") && message.contains(merge))
    }

    /// The hub keeps a grant's source as words: the ones it writes itself and the usual ones are worded in the app's
    /// language, whatever language they were written in; anything else stays as written.
    @Test func aKnownSourceIsWordedInTheAppsLanguage() {
        let suggested = String(localized: "Suggested rule: the same answer \(3) times in 14 days, confirmed with Always. [Standing Authorization source]")
        for raw in ["Suggested rule: the same answer 3 times in 14 days, confirmed with Always.",
                    "建议的规则：14 天内 3 次相同的回答，点「总是」确认。", // i18n-ignore: the hub's sentence
                    "提案されたルール：14 日間に同じ回答が 3 回あり、「常に」で確認されました。"] { // i18n-ignore: the hub's sentence
            #expect(StandingAuthorizationText.source(raw) == suggested, "\(raw)")
        }
        let main = String(localized: "Main chat [Standing Authorization source]")
        for raw in ["Main conversation", " main chat. ", "主对话", "メインチャット"] { // i18n-ignore: sources as written
            #expect(StandingAuthorizationText.source(raw) == main, "\(raw)")
        }
        #expect(StandingAuthorizationText.source("Settings") == String(localized: "Settings [Standing Authorization source]"))
        #expect(StandingAuthorizationText.source("conversation") == String(localized: "A chat [Standing Authorization source]"))
        #expect(StandingAuthorizationText.source("Weekly review with Sam") == "Weekly review with Sam", "unknown words stay")
    }

    @Test func theListIsReadForThePagesAssistant() async {
        for (shown, named) in [("work", "work"), (nil, nil)] as [(String?, String?)] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/assistant/standing-authorizations", json: Self.list)
            let page = page(hub, shown: shown)
            await page.load()
            #expect(page.grants?.map(\.id) == ["sa_one", "sa_two"] && page.failure == nil)
            #expect(hub.requests.map { $0.headers[APIClient.managerHeader] } == [named], "\(shown ?? "default")")
        }
    }

    @Test func withoutFullAccessThePageSaysSo() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/standing-authorizations") { _ in throw APIError(status: 403, message: "Full access is required.") }
        let page = page(hub)
        await page.load()
        #expect(page.grants == nil && page.failure == .forbidden)
    }

    @Test func aFailedFirstReadShowsRetryAndAFailedRefreshKeepsTheList() async {
        let hub = FakeHub()
        var fail = true
        hub.on("GET", "/api/v1/assistant/standing-authorizations") { _ in
            if fail { throw APIError(status: 503, message: "Unavailable") }
            return Data(Self.list.utf8)
        }
        let toasts = Toasts()
        let page = page(hub, toasts: toasts)
        await page.load()
        guard case .unavailable? = page.failure else {
            Issue.record("no failure: \(String(describing: page.failure))")
            return
        }
        #expect(page.grants == nil)
        fail = false
        await page.load()
        #expect(page.grants?.count == 2 && page.failure == nil)
        fail = true
        await page.load()
        #expect(page.grants?.count == 2, "the list stays; the failure is a toast")
        #expect(toasts.shown.map(\.style) == [.error])
    }

    @Test func aConfirmedRevokeSendsOneDeleteForItsGrantAndTheRowLeaves() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/standing-authorizations", json: Self.list)
        var listedWhileSending: [String]?
        let toasts = Toasts()
        let page = page(hub, shown: "work", toasts: toasts)
        hub.on("DELETE", "/api/v1/assistant/standing-authorizations/sa_one") { _ in
            listedWhileSending = page.grants?.map(\.id)
            return Data(#"{"ok":true,"revision":5}"#.utf8)
        }
        await page.load()
        let grant = try #require(page.grants?.first)
        await page.revoke(grant)
        #expect(listedWhileSending == ["sa_two"], "the row left before the hub answered")
        #expect(page.grants?.map(\.id) == ["sa_two"])
        let sent = hub.sent("DELETE", "/api/v1/assistant/standing-authorizations/sa_one")
        #expect(sent.count == 1 && sent.first?.headers[APIClient.managerHeader] == "work")
        #expect(toasts.shown.map(\.message) == [String(localized: "Authorization revoked")])
        #expect(page.revoking.isEmpty)
    }

    @Test func aRefusedRevokePutsTheRowBackWhereItWas() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/standing-authorizations", json: Self.list)
        hub.on("DELETE", "/api/v1/assistant/standing-authorizations/sa_one") { _ in throw APIError(status: 503, message: "Unavailable") }
        let toasts = Toasts()
        let page = page(hub, toasts: toasts)
        await page.load()
        let grant = try #require(page.grants?.first)
        await page.revoke(grant)
        #expect(page.grants?.map(\.id) == ["sa_one", "sa_two"], "back in its place")
        #expect(toasts.shown.map(\.style) == [.error])
        #expect(toasts.shown.first?.message == String(localized: "Couldn't revoke this authorization. It's still in effect. Check the connection and try again."))
    }

    /// Back on the list, the page reads again; a read the hub answered before the revoke landed still lists the grant.
    @Test func aReadFromBeforeTheRevokeDoesNotBringItBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/standing-authorizations", json: Self.list)
        hub.on("DELETE", "/api/v1/assistant/standing-authorizations/sa_one", json: #"{"ok":true,"revision":5}"#)
        let page = page(hub)
        await page.load()
        await page.revoke(try #require(page.grants?.first))
        await page.load() // the stale list, with sa_one
        #expect(page.grants?.map(\.id) == ["sa_two"])
    }

    @Test func anIDIsOnePathSegment() async {
        let hub = FakeHub()
        hub.on("DELETE", "/api/v1/assistant/standing-authorizations/sa_example%2Fone", json: #"{"ok":true,"revision":2}"#)
        _ = try? await hub.client.revokeStandingAuthorization("sa_example/one")
        #expect(hub.sent("DELETE", "/api/v1/assistant/standing-authorizations/sa_example%2Fone").count == 1)
    }
}
