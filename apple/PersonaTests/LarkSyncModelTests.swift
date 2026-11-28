import Foundation
import PersonaKit
import PersonaWire
import Testing
import SwiftUI
import UIKit
@testable import Persona

/// Settings › Lark Bot › Daily Sync (#15392): the same hub routes and words as the web's section.
@MainActor struct LarkSyncModelTests {
    private static let path = "/api/v1/bots/feishu/sync"

    /// The chat bot with one account that is up to date and one that must authorize again, and a sync bot waiting for
    /// its admin with one account whose authorization ends soon. Names are made up.
    private static let view = """
    {"available":true,"schedule_hour":3,
     "accounts":[{"id":"acc-1","label":"Work"},{"id":"acc-2","name":"Example User"}],
     "bots":[{"bot_id":"sync-1","label":"Team bot","role":"sync_only","tier":"full_with_im","approval":"awaiting_approval","primary":false,"has_secret":true},
             {"bot_id":"primary","role":"chat_and_sync","tier":"docs_standard","primary":true,"has_secret":true}],
     "bindings":[{"key":"primary:acc-1","bot_id":"primary","account_id":"acc-1","tier":"docs_standard","enabled":true,"health":"ok",
                  "last_run_at":"2026-11-25T03:00:12.5Z","next_run_at":"2026-11-26T03:00:00Z","last_messages":12,"last_docs":4,"running":false},
                 {"key":"primary:acc-2","bot_id":"primary","account_id":"acc-2","enabled":true,"health":"needs_reauth","last_messages":0,"last_docs":0,"running":false},
                 {"key":"sync-1:acc-3","bot_id":"sync-1","account_id":"acc-3","enabled":false,"health":"ok","reauth_due_days":6,"last_messages":0,"last_docs":0,"running":false}]}
    """

    private func model(_ hub: FakeHub, toasts: BotRecorder<[String]> = BotRecorder([])) -> LarkSyncModel {
        LarkSyncModel(deps: .init(api: { hub.client }, toast: { toasts.value.append($0.message) }))
    }

    /// The request's JSON object. Run and the removals send no body (the hub's routes read none): that is an empty one.
    private func body(_ req: LocalRequest) throws -> [String: Any] {
        guard let data = req.body, !data.isEmpty else { return [:] }
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    @Test func loadsTheViewWithTheChatBotFirst() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        let m = model(hub)
        await m.load()
        let v = try #require(m.view)
        #expect(v.orderedBots.map(\.botID) == ["primary", "sync-1"])
        #expect(v.bindings(of: "primary").count == 2)
        #expect(v.title(v.bindings[0]) == "Work")
        #expect(v.title(v.bindings[1]) == "Example User")
        #expect(v.title(v.bindings[2]) == String(localized: "Account \(3)"), "an account the hub doesn't list is numbered")
        #expect(m.primary?.botID == "primary")
        #expect(!m.running)
    }

    /// An older hub (404) or one without the Lark module (`available` false): nothing is drawn, and nothing is wrong.
    @Test func anOlderHubHidesTheSection() async throws {
        let older = FakeHub()
        let m = model(older)
        await m.load()
        #expect(m.view == nil && m.loadError == nil)

        let without = FakeHub()
        without.on("GET", Self.path, json: #"{"available":false,"bots":[],"bindings":[]}"#)
        let n = model(without)
        await n.load()
        #expect(n.view == nil && n.loadError == nil)

        let failing = FakeHub()
        failing.on("GET", Self.path) { _ in throw APIError(status: 500, message: "boom") }
        let f = model(failing)
        await f.load()
        #expect(f.view == nil && f.loadError != nil)
    }

    @Test func healthWordsAndTonesFollowTheWeb() {
        func b(_ health: String, enabled: Bool = true, running: Bool = false, due: Int? = nil) -> LarkSyncView.Binding {
            .init(key: "k", botID: "b", accountID: "a", enabled: enabled, health: health, reauthDueDays: due, running: running)
        }
        #expect(LarkSyncText.health(b("ok")).tone == .positive)
        #expect(LarkSyncText.health(b("ok", enabled: false)).tone == .neutral)
        #expect(LarkSyncText.health(b("failing", running: true)).tone == .accent, "a running sync wins over the last outcome")
        #expect(LarkSyncText.health(b("needs_reauth")).tone == .danger)
        #expect(LarkSyncText.health(b("failing")).tone == .danger)
        for h in ["rate_limited", "scope_missing", "awaiting_approval"] { #expect(LarkSyncText.health(b(h)).tone == .caution) }
        #expect(LarkSyncText.health(b("something_new")).tone == .neutral)
        #expect(LarkSyncText.needsAuthorize(b("unauthorized")) && LarkSyncText.needsAuthorize(b("needs_reauth")))
        #expect(LarkSyncText.needsAuthorize(b("scope_missing")))
        #expect(LarkSyncText.health(b("scope_missing")).text == String(localized: "Reauthorize to Add Missing Permissions [Lark Sync]"))
        #expect(LarkSyncText.reauthSoon(b("scope_missing", due: 30)) == nil)
        #expect(LarkSyncText.reauthSoon(b("ok", due: 6)) == 6)
        #expect(LarkSyncText.reauthSoon(b("ok", due: 15)) == nil)
        #expect(LarkSyncText.reauthSoon(b("ok", due: -2)) == 0)
        #expect(LarkSyncText.reauthSoon(b("needs_reauth", due: 3)) == nil, "already asked to authorize again")
        #expect(LarkSyncText.approval(.init(botID: "x", approval: "awaiting_approval"))?.tone == .caution)
        #expect(LarkSyncText.approval(.init(botID: "x", approval: "ready")) == nil)
    }

    @Test func statusLineSaysOnlyWhatTheHubKnows() throws {
        let v = try JSON.decoder.decode(LarkSyncView.self, from: Data(Self.view.utf8))
        let now = try #require(LarkSyncText.date("2026-11-25T06:00:00Z"))
        let synced = LarkSyncText.statusLine(v.bindings[0], now: now)
        #expect(synced.components(separatedBy: " · ").count == 4) // synced, messages, documents, next
        #expect(synced.contains("12") && synced.contains("4"))
        let never = LarkSyncText.statusLine(v.bindings[1], now: now)
        #expect(never.components(separatedBy: " · ").count == 1, "no next run while it must authorize again")
        #expect(LarkSyncText.date("2026-11-25T03:00:00Z") != nil && LarkSyncText.date("2026-11-25T03:00:00.123456789Z") != nil)
        #expect(LarkSyncText.date("") == nil)
    }

    @Test func changesGoToTheirRoutesAndShowTheAnswer() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        var sent: [String: [String: Any]] = [:]
        for (method, route) in [("PUT", Self.path), ("PUT", "\(Self.path)/bindings/primary%3Aacc-1"), ("POST", "\(Self.path)/bindings/primary%3Aacc-1/run"),
                                ("DELETE", "\(Self.path)/bindings/primary%3Aacc-2"), ("DELETE", "\(Self.path)/bots/sync-1")] {
            hub.on(method, route) { req in
                sent["\(method) \(route)"] = try self.body(req)
                return Data(Self.view.utf8)
            }
        }
        let toasts = BotRecorder<[String]>([])
        let m = model(hub, toasts: toasts)
        await m.load()
        let v = try #require(m.view)
        await m.setSchedule(5)
        #expect(sent["PUT \(Self.path)"]?["schedule_hour"] as? Int == 5)
        await m.setEnabled(v.bindings[0], false)
        #expect(sent["PUT \(Self.path)/bindings/primary%3Aacc-1"]?["enabled"] as? Bool == false)
        await m.run(v.bindings[0])
        #expect(sent["POST \(Self.path)/bindings/primary%3Aacc-1/run"] != nil)
        #expect(toasts.value.count == 1)
        await m.remove(v.bindings[1])
        #expect(sent["DELETE \(Self.path)/bindings/primary%3Aacc-2"] != nil)
        await m.removeBot(v.bots[0])
        #expect(sent["DELETE \(Self.path)/bots/sync-1"] != nil)
        #expect(m.error == nil && m.busy.isEmpty)
        for (method, route) in [("POST", "\(Self.path)/bindings/primary%3Aacc-1/run"), ("DELETE", "\(Self.path)/bindings/primary%3Aacc-2"),
                                ("DELETE", "\(Self.path)/bots/sync-1")] {
            #expect(hub.sent(method, route).count == 1 && hub.sent(method, route).first?.body == nil, "\(method) \(route) sends no body")
        }
    }

    @Test func aFailedChangeKeepsTheViewAndSaysWhy() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        hub.on("POST", "\(Self.path)/bindings/primary%3Aacc-1/run") { _ in throw APIError(status: 409, message: "already running") }
        let m = model(hub)
        await m.load()
        let before = m.view
        await m.run(try #require(m.view?.bindings.first))
        #expect(m.view == before)
        #expect(m.error?.contains("already running") == true)
    }

    /// Adding: a new app by scanning (step 1, its QR step asked again until done), then step 2 prepares the authorization
    /// with the hub's own /lark-oauth as the return address.
    @Test func addingRegistersThenAuthorizes() async throws {
        let hub = FakeHub(url: "https://hub.example.com/persona/")
        hub.on("GET", Self.path, json: Self.view)
        hub.on("POST", "\(Self.path)/bots") { req in
            let b = try self.body(req)
            #expect(b["tier"] as? String == "full_with_im" && b["label"] as? String == "Team" && b["region"] as? String == "lark")
            return Data(#"{"bot_id":"new-1","prompt":{"step":"register","kind":"qr","qr":{"size":1,"rows":["1"]},"interval":3}}"#.utf8)
        }
        var polls = 0
        hub.on("POST", "\(Self.path)/bots/new-1/register") { _ in
            polls += 1
            return Data((polls < 2 ? #"{"step":"register","kind":"qr","qr":{"size":1,"rows":["1"]}}"# : #"{"done":true}"#).utf8)
        }
        var redirect = ""
        hub.on("POST", "\(Self.path)/bots/new-1/authorize") { req in
            let b = try self.body(req)
            redirect = b["redirect_uri"] as? String ?? ""
            #expect(b["tier"] as? String == "full_with_im")
            return Data(#"{"mode":"browser","url":"https://open.example.com/authorize?x=1"}"#.utf8)
        }
        let m = model(hub)
        await m.load()
        m.flow = .choose
        await m.startAdd(new: true, label: "  Team ", tier: LarkSyncView.tierFull, region: "lark")
        guard case .register(bot: "new-1", tier: _, prompt: _, scopes: false) = m.flow else { Issue.record("not at step 1: \(String(describing: m.flow))"); return }
        #expect(await m.pollRegister() == false)
        #expect(await m.pollRegister() == true)
        guard case let .authorize(bot, _, again, answer, _, error) = m.flow else { Issue.record("not at step 2"); return }
        #expect(bot == "new-1" && !again && answer?.mode == "browser" && error == nil)
        #expect(redirect == "https://hub.example.com/persona/lark-oauth")
        m.openBrowser()
        #expect(m.browser?.url.absoluteString == "https://open.example.com/authorize?x=1")
        #expect(m.browser?.redirect == redirect)
    }

    /// The chat bot's app given the permission: the scopes route, and a registration that is already done goes straight on.
    @Test func usingTheChatBotAddsItsScopes() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        hub.on("POST", "\(Self.path)/bots/primary/scopes", json: #"{"done":true}"#)
        hub.on("POST", "\(Self.path)/bots/primary/authorize", json: #"{"mode":"manual","redirect_uri":"https://fake.invalid/lark-oauth","url":"https://open.example.com/a"}"#)
        let m = model(hub)
        await m.load()
        await m.startAdd(new: false, label: "", tier: LarkSyncView.tierDocs, region: "feishu")
        guard case let .authorize(bot, _, _, answer, _, _) = m.flow else { Issue.record("not at step 2"); return }
        #expect(bot == "primary" && answer?.mode == "manual")
    }

    /// The browser return: the code and state go to the hub, which answers the new view; declining says so.
    @Test func theBrowserReturnFinishesTheAuthorization() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        hub.on("POST", "\(Self.path)/bots/primary/authorize", json: #"{"mode":"browser","url":"https://open.example.com/a"}"#)
        var finished: [String: Any] = [:]
        hub.on("POST", "\(Self.path)/oauth") { req in
            finished = try self.body(req)
            return Data(Self.view.utf8)
        }
        let toasts = BotRecorder<[String]>([])
        let m = model(hub, toasts: toasts)
        await m.load()
        await m.startAuthorize(bot: "primary", tier: LarkSyncView.tierDocs, again: true)
        await m.finishBrowser(.declined("access_denied"))
        guard case let .authorize(_, _, _, _, _, error) = m.flow else { Issue.record("left step 2"); return }
        #expect(error != nil)
        await m.finishBrowser(.code(state: "st", code: "cd"))
        #expect(finished["state"] as? String == "st" && finished["code"] as? String == "cd")
        #expect(m.flow == nil && toasts.value.count == 1)
    }

    /// The device flow is asked again until done; a failure stays on screen with Start Over.
    @Test func theDeviceFlowPollsUntilDone() async throws {
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        hub.on("POST", "\(Self.path)/bots/primary/authorize",
               json: #"{"mode":"device","prompt":{"step":"device","kind":"device_code","url":"https://open.example.com/device","user_code":"ABCD-EFGH","interval":5}}"#)
        let answer = BotRecorder(#"{"step":"device"}"#)
        hub.on("POST", "\(Self.path)/bots/primary/authorize/poll") { _ in Data(answer.value.utf8) }
        let m = model(hub)
        await m.load()
        await m.startAuthorize(bot: "primary", tier: LarkSyncView.tierDocs)
        #expect(await m.pollDevice() == false)
        answer.value = #"{"error":"expired"}"#
        #expect(await m.pollDevice() == true)
        guard case let .authorize(_, _, _, _, prompt, _) = m.flow else { Issue.record("left step 2"); return }
        #expect(prompt?.error == "expired" && prompt?.userCode == "ABCD-EFGH")
        answer.value = #"{"done":true}"#
        #expect(await m.pollDevice() == true)
        #expect(m.flow == nil)
    }

    @Test func theReturnAddressIsTheHubsOwnLarkOAuthPage() throws {
        #expect(LarkOAuthReturn.redirectURI(hub: URL(string: "https://hub.example.com")!) == "https://hub.example.com/lark-oauth")
        #expect(LarkOAuthReturn.redirectURI(hub: URL(string: "https://hub.example.com:8443/persona/")!) == "https://hub.example.com:8443/persona/lark-oauth")
        let redirect = "https://hub.example.com/persona/lark-oauth"
        #expect(LarkOAuthReturn.parse(URL(string: "https://hub.example.com/persona/lark-oauth?code=c1&state=s1")!, redirect: redirect) == .code(state: "s1", code: "c1"))
        #expect(LarkOAuthReturn.parse(URL(string: "https://HUB.example.com/persona/lark-oauth?error=access_denied")!, redirect: redirect) == .declined("access_denied"))
        #expect(LarkOAuthReturn.parse(URL(string: "https://hub.example.com/persona/lark-oauth")!, redirect: redirect) == .declined(""))
        #expect(LarkOAuthReturn.parse(URL(string: "https://open.example.com/authorize?code=c1&state=s1")!, redirect: redirect) == nil)
        #expect(LarkOAuthReturn.parse(URL(string: "http://hub.example.com/persona/lark-oauth?code=c&state=s")!, redirect: redirect) == nil, "another scheme")
        #expect(LarkOAuthReturn.parse(URL(string: "https://hub.example.com/lark-oauth?code=c&state=s")!, redirect: redirect) == nil, "another path")
    }

    @Test func aLenientViewFillsWhatIsMissing() throws {
        let v = try JSON.decoder.decode(LarkSyncView.self, from: Data(#"{"available":true,"schedule_hour":30,"bots":[],"bindings":[]}"#.utf8))
        #expect(v.scheduleHour == 3 && v.accounts.isEmpty)
    }

    /// The section as the Lark Bot page draws it, light and dark, with made-up accounts: pictures for a person to look at
    /// (`PERSONA_SHOTS`, as the other hosted tests'); nothing is written without it.
    @Test func shotsOfTheSection() async throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let hub = FakeHub()
        hub.on("GET", Self.path, json: Self.view)
        let m = model(hub)
        await m.load()
        #expect(m.view != nil)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        for (name, style) in [("light", UIUserInterfaceStyle.light), ("dark", .dark)] {
            let window = UIWindow(windowScene: scene)
            window.frame = CGRect(x: 0, y: 0, width: 393, height: 1500)
            window.overrideUserInterfaceStyle = style
            let page = NavigationStack {
                SettingsForm { LarkSyncSections(model: m) }
                    .themedPage()
                    .navigationTitle(String(localized: "Lark Bot"))
                    .navigationBarTitleDisplayMode(.inline)
            }
            window.rootViewController = UIHostingController(rootView: page.environment(AppModel.shared))
            window.isHidden = false
            window.layoutIfNeeded()
            try? await Task.sleep(for: .milliseconds(800))
            let image = UIGraphicsImageRenderer(bounds: window.bounds).image { _ in
                window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
            }
            try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try image.pngData()?.write(to: URL(fileURLWithPath: dir + "/lark-sync-\(name).png"))
            window.isHidden = true
        }
    }
}
