import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The sidebar's assistant switcher (docs/assistant/managers.md "Clients"): what each row says, the list's order, the
/// avatar colors every client shares, what a roster kept across launches holds, and the install code a row of one still
/// installing shows. Made-up values.
@MainActor
struct AssistantSwitcherTests {
    nonisolated private static let start = Date(timeIntervalSince1970: 1_800_000_000)

    nonisolated private static func at(_ seconds: TimeInterval) -> String {
        ISO8601DateFormatter().string(from: start.addingTimeInterval(seconds))
    }

    nonisolated private static let roster = #"""
    {"default":"m-1","managers":[
      {"id":"m-3","name":"书房","online":false,"installing":"\#(at(8 * 60))"},
      {"id":"assistant","name":"助手","online":true,"builtin":true,"host":"server","os":"linux","deploy":"child"},
      {"id":"m-1","name":"工作","online":true,"default":true,"host":"MacBook Pro","os":"darwin","deploy":"service"},
      {"id":"m-2","name":"家里","online":false,"host":"NAS","last_seen":"\#(at(-2 * 3600))"},
      {"id":"m-4","name":"旧的","online":false,"installing":"\#(at(-60))"}]}
    """#

    private static func list() throws -> ManagerRoster {
        ManagerRoster(try JSON.decoder.decode(APIClient.ManagerList.self, from: Data(roster.utf8)))
    }

    @Test func theListIsTheInstalledOnesThenThoseInstalling() throws {
        let roster = try Self.list()
        #expect(AssistantSwitcherText.rows(roster).map(\.id) == ["m-1", "assistant", "m-2", "m-3", "m-4"],
                "the default first, the roster's order, then the ones still installing")
        let one = ManagerRoster(managers: [HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true)])
        #expect(AssistantSwitcherText.rows(one).map(\.id) == ["assistant"], "one assistant is listed too")
    }

    @Test func aRowSaysWhetherItIsOnlineAndWhere() throws {
        let roster = try Self.list()
        func line(_ id: String) throws -> String {
            AssistantSwitcherText.line(try #require(roster.managers.first { $0.id == id }), now: Self.start)
        }
        #expect(try line("m-1") == "在线 · MacBook Pro")
        #expect(try line("m-2").hasPrefix("离线 · 最后同步于"), "an offline one: when it last synced")
        #expect(try line("m-3") == "安装中 · 码 8 分钟后过期")
        #expect(try line("m-4") == "安装中 · 码已过期")
        let nowhere = HubManager(id: "x", name: "x", online: true)
        #expect(AssistantSwitcherText.line(nowhere, now: Self.start) == "在线", "no host reported: just online")
    }

    /// VoiceOver hears what a row and the header show: how much waits beside another assistant, nothing beside the
    /// current one, and on the sidebar's header that another assistant has something waiting. Settings speaks its full
    /// title and state instead, without a waiting message for a dot it does not draw.
    @Test func whatWaitsIsSpoken() throws {
        let roster = try Self.list()
        let work = try #require(roster.managers.first { $0.id == "m-1" })
        #expect(AssistantSwitcherText.spoken(work, now: Self.start, current: false, isDefault: true, waiting: 3)
                == "工作, 在线 · MacBook Pro, 默认, 3 件等你处理")
        #expect(AssistantSwitcherText.spoken(work, now: Self.start, current: true, isDefault: true, waiting: 3)
                == "工作, 在线 · MacBook Pro, 默认, 当前", "the current one's row shows no count")
        #expect(AssistantSwitcherText.spoken(work, now: Self.start, current: false, isDefault: false) == "工作, 在线 · MacBook Pro")
        #expect(AssistantSwitcherText.spokenHeader(work, now: Self.start, othersWaiting: false) == "工作, 在线 · MacBook Pro")
        #expect(AssistantSwitcherText.spokenHeader(work, now: Self.start, othersWaiting: true) == "工作, 在线 · MacBook Pro, 别的助手有事等你处理")
        #expect(AssistantSwitcherText.spokenSettingsHeader(title: "工作 的设置", manager: work, now: Self.start)
                == "工作 的设置, 在线 · MacBook Pro")
    }

    /// In place of an empty inbox or to-do list while the current assistant is offline: the hub's merged lists have
    /// nothing of it.
    @Test func anOfflineAssistantsListsSayItIsOffline() throws {
        let roster = try Self.list()
        let home = try #require(roster.managers.first { $0.id == "m-2" })
        #expect(AssistantOfflineNotice.title(home) == "家里 离线")
        #expect(AssistantOfflineNotice.message(home).hasPrefix("最后同步于") && AssistantOfflineNotice.message(home).hasSuffix("它上线后这里会更新。"))
        #expect(AssistantOfflineNotice.message(HubManager(id: "x", name: "x", online: false)) == "它上线后这里会更新。")
    }

    @Test func avatarColorsAreTheSameOnEveryClient() throws {
        func hue(_ id: String, default d: String = "assistant") -> Int? {
            AssistantSwitcherText.hue(HubManager(id: id, name: id, online: true), defaultID: d)
        }
        #expect(hue("assistant") == nil, "the default's is the assistant's own color")
        // The sum of the id's UTF-8 bytes modulo five: m (109) + - (45) + 1 (49) = 203.
        #expect(hue("m-1") == 3 && hue("m-2") == 4 && hue("m-3") == 0)
        #expect(hue("assistant", default: "m-1") == 1 && hue("m-1", default: "m-1") == nil, "the default moved: so do the colors (986 % 5)")
        #expect(hue("工作") == "工作".utf8.reduce(0) { $0 + Int($1) } % 5, "bytes, not characters")
    }

    @Test func theRosterIsKeptAsItCame() throws {
        let roster = try Self.list()
        let kept = try JSON.decoder.decode([HubManager].self, from: JSON.encoder.encode(roster.managers))
        #expect(ManagerRoster(managers: kept, defaultID: roster.defaultID) == roster, "a launch shows the roster the last one read")
    }

    @Test func aRowStillInstallingShowsItsCodeAgainOrANewOne() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        hub.on("POST", "/api/v1/managers/m-3/code", json: #"{"code":"mi_1","expires_at":"\#(Self.at(600))"}"#)
        func asked() -> Int { hub.sent("POST", "/api/v1/managers/m-3/code").count }
        let clock = ManagersModelTests.Clock()
        let page = ManagersModel(deps: .init(api: { hub.client }, now: { clock.now }))
        await page.load()
        let installing = try #require(page.manager("m-3"))

        await page.showCode(installing)
        #expect(page.shown?.code.code == "mi_1" && page.shown?.neverInstalled == true, "none on hand: a new one")
        page.shown = nil
        await page.showCode(installing)
        #expect(page.shown?.code.code == "mi_1" && asked() == 1, "still good: the same one, which a new one would void")
        page.shown = nil
        clock.now = Self.start.addingTimeInterval(601)
        hub.on("POST", "/api/v1/managers/m-3/code", json: #"{"code":"mi_2","expires_at":"\#(Self.at(1200))"}"#)
        await page.showCode(installing)
        #expect(page.shown?.code.code == "mi_2" && asked() == 2, "run out: a new one")
    }
}
