import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Settings › Claude Code › Blocked Config Folders (`ClaudeDirsLogic`): the same cases as web `agent-settings.test.ts`.
struct ClaudeDirsLogicTests {
    private func state() throws -> APIClient.ClaudeDirsState {
        let json = """
        {"home":"/home/me","live":true,"blocked":["~/.automation/*","/opt/x"],"dirs":[
         {"path":"/home/me/.claude","default":true,"source":"default","running":0,"sessions":40},
         {"path":"/home/me/.automation/a","blocked":true,"blocked_by":"~/.automation/*","source":"found","running":1,"sessions":120},
         {"path":"/home/me/.config/cc/work","source":"config","running":0}]}
        """
        return try JSON.decoder.decode(APIClient.ClaudeDirsState.self, from: Data(json.utf8))
    }

    @Test func rows() throws {
        let s = try state()
        #expect(ClaudeDirsLogic.shortPath("/home/me/.automation/a", home: "/home/me") == "~/.automation/a")
        #expect(ClaudeDirsLogic.shortPath("/home/user/x", home: "/home/me") == "/home/user/x")
        #expect(ClaudeDirsLogic.meta(s.dirs[0], home: s.home) == "默认目录，不能屏蔽 · 40 个会话")
        #expect(ClaudeDirsLogic.meta(s.dirs[1], home: s.home) == "自动发现 · 120 个会话 · 1 个正在运行 · 由规则 ~/.automation/* 屏蔽")
        #expect(ClaudeDirsLogic.meta(s.dirs[2], home: s.home) == "hub.json 里配的")
    }

    @Test func blockAndUnblock() throws {
        let s = try state()
        #expect(ClaudeDirsLogic.toggled(s, s.dirs[2]) == ["~/.automation/*", "/opt/x", "~/.config/cc/work"])
        #expect(ClaudeDirsLogic.toggled(s, s.dirs[1]) == ["/opt/x"])
        #expect(ClaudeDirsLogic.looseRules(s) == ["/opt/x"])
    }

    @Test func typedRules() {
        #expect(ClaudeDirsLogic.problem("", existing: []) != nil)
        #expect(ClaudeDirsLogic.problem("relative", existing: [])?.contains("开头") == true)
        #expect(ClaudeDirsLogic.problem("/opt/x/", existing: ["/opt/x"]) == "已经屏蔽了")
        #expect(ClaudeDirsLogic.problem(" ~/.bots/* ", existing: []) == nil)
        #expect(ClaudeDirsLogic.cleaned(" ~/.bots/ ") == "~/.bots")
    }

    /// A load that failed says so, with Retry under it: a timeout or no network too, which left the section spinning
    /// for good (#744); a cancelled one (the page went away) says nothing.
    @Test func loadFailures() {
        let timeout = URLError(.timedOut), offline = URLError(.notConnectedToInternet), cancelled = CancellationError()
        #expect(ClaudeDirsLogic.loadFailure(timeout) == "连不上 hub，读不到这台电脑的配置目录")
        #expect(ClaudeDirsLogic.loadFailure(offline) == "连不上 hub，读不到这台电脑的配置目录")
        #expect(ClaudeDirsLogic.loadFailure(cancelled) == nil)
        let other = NSError(domain: "test", code: 1, userInfo: [NSLocalizedDescriptionKey: "node 太旧"])
        #expect(ClaudeDirsLogic.loadFailure(other) == "读不到这台电脑的配置目录：node 太旧")
    }
}
