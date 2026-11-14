import Foundation
import Testing
@testable import Persona
import PersonaWire

/// quota-relay's sidebar page (nav_page `sidebar`, #4576) as the extension draws it: the fixture is the extension's
/// own export (extensions repo `testdata/relay-accounts-view.json`, made-up
/// accounts), copied here as is. The page reads, stays on the list page (no tabs), its filter, segmented and search
/// send what the extension reads back, a row pushes the account's page without asking the extension, and nothing on it
/// names a launcher or a key.
@MainActor
struct RelayQuotaAccountsPageTests {
    private static let fixture = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        .appending(path: "Fixtures/relay-accounts-view.json")
    private static let path = "/api/v1/plugins/quota-relay/-/ui/sidebar"

    /// One of the fixture's views (`default`, `kiro`, `detail`) as the JSON the hub would send.
    private static func raw(_ name: String) throws -> String {
        let all = try #require(try JSONSerialization.jsonObject(with: Data(contentsOf: fixture)) as? [String: Any])
        let v = try #require(all[name])
        return String(decoding: try JSONSerialization.data(withJSONObject: v), as: UTF8.self)
    }

    private static func view(_ name: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(try raw(name).utf8))
    }

    private func page(_ hub: FakeHub, state: String? = nil, depth: Int = 1) -> ExtensionViewModel {
        ExtensionViewModel(plugin: "quota-relay", ui: "sidebar", context: ExtensionUIContext.here().forPage(), state: state,
                           depth: depth, deps: .init(api: { hub.client }))
    }

    private func bodies(_ hub: FakeHub) throws -> [JSONValue] {
        try hub.sent("POST", Self.path).map { try JSON.decoder.decode(JSONValue.self, from: $0.body ?? Data()) }
    }

    /// Every string in the fixture's JSON (titles, subtitles, values, captions, choices…).
    private static func strings(_ any: Any) -> [String] {
        switch any {
        case let s as String: [s]
        case let a as [Any]: a.flatMap(strings)
        case let o as [String: Any]: o.values.flatMap(strings)
        default: []
        }
    }

    @Test(arguments: ["default", "kiro", "detail"])
    func everyComponentIsKnown(_ name: String) throws {
        let v = try Self.view(name)
        let items = v.sections.flatMap(\.items)
        #expect(!items.isEmpty)
        #expect(!items.contains { if case .unknown = $0 { true } else { false } }, "every component of \(name) is drawn")
        #expect(items.count <= 300, "within the apps' item limit")
        let keys = items.compactMap(\.key)
        #expect(keys.count == items.count && Set(keys).count == keys.count, "every item has its own key")
        // The page's parts (no dividers): one per section, each item keeping its key as its identity.
        let parts = ExtensionParts.of(v)
        #expect(parts.count == v.sections.count)
        #expect(parts.flatMap(\.items).map(\.id) == keys.map { "k:" + $0 })
    }

    @Test func theListStaysOnTheListPage() throws {
        // No tabs, no split, no header: the list page (`ExtensionPageView.form`), with its search and filter chips.
        for name in ["default", "kiro"] {
            let v = try Self.view(name)
            #expect(v.tabs == nil && !v.wantsCodePage, "\(name) is not the code-hosting page")
            #expect(v.title == "中转账号" && v.refreshAfter == 60)
            #expect(v.search?.action.id == "search" && v.search?.placeholder == "搜索账号")
            let f = try #require(v.filter)
            #expect(f.name == "status" && f.action.id == "status" && f.value == "all")
            #expect(f.choices.count <= 20, "every choice is a chip")
            #expect(Set(f.choices.map(\.value)).count == f.choices.count)
            #expect(f.choices.first?.value == "all")
        }
        // An account's page has a header: drawn by the code page (`wantsCodePage`), its other sections as plain blocks.
        let d = try Self.view("detail")
        #expect(d.wantsCodePage && d.tabs == nil && d.layout == nil && d.search == nil && d.filter == nil)
        guard case .header(let h)? = d.sections.first?.items.first else {
            Issue.record("the account's page starts with its header")
            return
        }
        #expect(!h.title.isEmpty && h.badges?.isEmpty == false && h.facts?.isEmpty == false)
        #expect(d.sections.dropFirst().flatMap(\.items).allSatisfy { !$0.isCodeHosting })
    }

    @Test func theDefaultViewIsEveryAccount() throws {
        let v = try Self.view("default")
        let head = v.sections[0].items
        let type = try #require(head.first?.segmented)
        #expect(type.name == "type" && type.value == "all" && type.action.id == "type")
        #expect(type.choices.map(\.value) == ["all", "claude", "kiro", "other"], "four choices: all of them drawn (5 at most)")
        #expect(type.choices.allSatisfy { $0.label.count <= 12 }, "short enough for a segment")
        #expect(head.contains { $0.statGrid?.stats.count == 4 })
        // The filter's All counts what the segmented's All does: the default hides nothing.
        #expect(v.filter?.choices.first?.label == type.choices.first?.label)

        // One row per account, each with its badge and its push; a section per group, the refresh button at the end.
        let rows = v.sections.dropFirst().flatMap(\.items).compactMap(\.row)
        let total = try #require(type.choices.first.flatMap { Int($0.label.split(separator: " ").last ?? "") })
        #expect(rows.count == total, "全部 \(total): as many rows")
        for r in rows {
            #expect(!r.title.isEmpty && r.badge?.text.isEmpty == false)
            let push = try #require(r.action?.push, "\(r.title) pushes its page")
            #expect(push.ui == "sidebar" && push.title?.isEmpty == false)
            let state = try JSON.decoder.decode(JSONValue.self, from: Data((push.state ?? "").utf8))
            #expect(state["a"] != nil && r.key == "a:" + (state["a"]?.string ?? ""))
        }
        #expect(v.sections.last?.items.last?.button?.action.id == "refresh")
    }

    @Test(arguments: ["default", "kiro", "detail"])
    func nothingNamesALauncherOrAKey(_ name: String) throws {
        let all = try #require(try JSONSerialization.jsonObject(with: Data(contentsOf: Self.fixture)) as? [String: Any])
        let text = Self.strings(try #require(all[name]))
        #expect(!text.contains { $0.contains("启动器") || $0.contains("Key") }, "no launcher or key on \(name)")
    }

    @Test func pickingATypeAndAStatusAndSearchingSendWhatTheExtensionReads() async throws {
        let hub = FakeHub()
        let main = try Self.raw("default"), kiro = try Self.raw("kiro")
        hub.on("POST", Self.path) { req in
            let b = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data(#"{"view":\#(b["action"] == .string("type") ? kiro : main)}"#.utf8)
        }
        let m = page(hub)
        await m.load()
        #expect(m.values["type"] == "all" && m.values["status"] == "all" && m.query == "")
        let state = try #require(m.view?.state)

        // The segmented: {type}, with the page's state (the extension keeps the status and the search there).
        let type = try #require(m.view?.sections[0].items.first?.segmented)
        _ = await m.choose(type.name, "kiro", action: type.action)
        var last = try #require(try bodies(hub).last)
        #expect(last["action"] == .string("type") && last["values"] == .object(["type": .string("kiro")]))
        #expect(last["state"] == .string(state))
        #expect(m.values["type"] == "kiro" && m.view?.sections.dropFirst().first?.title?.hasPrefix("Kiro") == true)

        // The filter: {status, q}.
        _ = await m.filter("cooldown")
        last = try #require(try bodies(hub).last)
        #expect(last["action"] == .string("status"))
        #expect(last["values"] == .object(["status": .string("cooldown"), "q": .string("")]))

        // The search: {q, status} (the View it answers says status all again: the chips follow it).
        m.query = "kiro-4*"
        _ = await m.search()
        last = try #require(try bodies(hub).last)
        #expect(last["action"] == .string("search"))
        #expect(last["values"] == .object(["q": .string("kiro-4*"), "status": .string("all")]))
        #expect(m.query == "kiro-4*", "the answer does not clear what was searched")
    }

    @Test func aRowPushesTheAccountsPageWithoutAskingTheExtension() async throws {
        let hub = FakeHub()
        hub.on("POST", Self.path, json: #"{"view":\#(try Self.raw("default"))}"#)
        let m = page(hub)
        await m.load()
        let row = try #require(m.view?.sections.dropFirst().flatMap(\.items).compactMap(\.row).first)
        let action = try #require(row.action)
        let before = hub.requests.count
        #expect(await m.perform(action) == .init())
        #expect(hub.requests.count == before, "a push is not sent")
        let pushed = try #require(m.pushed)
        #expect(pushed.ui == "sidebar" && pushed.title == action.push?.title)

        // The page it pushes: the same interface, one level deeper, starting with the account's state.
        let t = ExtensionPushTarget(model: m, push: pushed)
        #expect(t.plugin == "quota-relay" && t.ui == "sidebar" && t.depth == 2 && t.state == pushed.state)

        // That page asks for its view with that state and draws the account's page.
        let detail = FakeHub()
        detail.on("POST", Self.path, json: #"{"view":\#(try Self.raw("detail"))}"#)
        let d = page(detail, state: t.state, depth: t.depth)
        await d.load()
        #expect(try bodies(detail).first?["state"] == .string(t.state ?? ""))
        #expect(d.view?.wantsCodePage == true && d.view?.sections.flatMap(\.items).contains { $0.progress != nil } == true)
    }
}
