import XCTest
@testable import Persona
import PersonaWire

/// 「设置」 › 「模型供应商」 (#2854): the hub's per-machine sync state, the built-in claude and the rc file a launcher is from.
final class LauncherSyncTests: XCTestCase {
    func testDecodesTheListWithSyncAndBuiltin() throws {
        let json = """
        {"launchers": [
          {"name": "kcc", "source": "zshrc", "machines": ["box"], "rc_files": ["~/.bashrc"], "stale": ["box"]},
          {"name": "old", "source": "zshrc", "machines": ["mac"]},
          {"name": "claude", "source": "builtin", "model": "默认", "machines": ["box"], "versions": {"box": "2.1.0"}}],
         "sync": [{"machine": "box", "name": "Box", "state": "timeout", "rc_file": "~/.bashrc",
                   "synced_at": "2026-11-05T08:00:00.123456789Z"}]}
        """
        struct R: Decodable { var launchers: [LauncherView]; var sync: [LauncherSync]? }
        let r = try JSON.decoder.decode(R.self, from: Data(json.utf8))
        XCTAssertEqual(r.launchers[0].rcFileTitle, "~/.bashrc")
        XCTAssertEqual(r.launchers[0].stale, ["box"])
        XCTAssertEqual(r.launchers[1].rcFileTitle, "~/.zshrc", "an older hub's launcher is from ~/.zshrc")
        XCTAssertTrue(r.launchers[2].builtin)
        XCTAssertFalse(r.launchers[2].managed)
        XCTAssertEqual(r.launchers[2].versions?["box"], "2.1.0")
        XCTAssertEqual(r.sync?.first?.state, "timeout")
        XCTAssertNotNil(r.sync?.first?.syncedAt)
    }

    func testSummaries() {
        let at = Date(timeIntervalSince1970: 1_000_000)
        XCTAssertEqual(LauncherSync(machine: "a", state: "ok", rcFile: "~/.bashrc").summary(), "已同步 · ~/.bashrc")
        XCTAssertEqual(LauncherSync(machine: "a", state: "offline").summary(), "离线，还没同步过")
        XCTAssertTrue(LauncherSync(machine: "a", state: "offline", syncedAt: at).summary().hasPrefix("离线 · 显示上次同步的结果（"))
        XCTAssertTrue(LauncherSync(machine: "a", state: "timeout", syncedAt: at).summary().hasPrefix("超时 · "))
        XCTAssertEqual(LauncherSync(machine: "a", state: "error", error: "list launchers: exit status 1").summary(),
                       "失败：list launchers: exit status 1")
    }
}
