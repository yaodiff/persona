import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The browser on a Mac (the hub's plugin ego, docs/plans/plan-ego-browser.md): what the machine list says, the setup's
/// steps, the plugin page's words.
@MainActor
struct BrowserTests {
    private func machine(_ extra: String) throws -> Machine {
        try JSON.decoder.decode(Machine.self, from: Data("{\"id\":\"macbook\",\"name\":\"MacBook\",\"kind\":\"ssh\",\"online\":true\(extra)}".utf8))
    }

    @Test func theMachineListSaysIt() throws {
        #expect(try machine("").browser == nil, "plugin off, an older hub, not a Mac")
        let b = try #require(try machine(",\"browser\":{\"enabled\":true,\"installed\":true,\"version\":\"0.6.2\",\"set_up\":true,"
            + "\"imported\":true,\"analytics\":\"on\",\"ready\":true,\"screen_sharing\":true}").browser)
        #expect(b.enabled && b.setUp && b.imported && b.ready && b.screenSharing && b.analytics == "on" && b.checked)
        #expect(try machine(",\"browser\":{\"enabled\":false}").browser?.checked == false, "not asked yet")
    }

    /// The plugin ego on for a Mac with Screen Sharing shows its live screen, without the plugin cua (#1507).
    @Test func egoShowsTheScreen() throws {
        #expect(AppModel.showsScreen(try machine(",\"browser\":{\"enabled\":true,\"screen_sharing\":true}")))
        #expect(!AppModel.showsScreen(try machine(",\"browser\":{\"enabled\":false,\"screen_sharing\":true}")))
        #expect(!AppModel.showsScreen(try machine(",\"browser\":{\"enabled\":true,\"screen_sharing\":false}")))
        #expect(AppModel.showsScreen(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true}")))
        #expect(!AppModel.showsScreen(try machine("")))
    }

    /// #3954: the Picture / Quality switcher appears only when the showing extension advertises the video channel — the
    /// extension on, Screen Sharing answering, and the helper present. A shared screen without the helper stays RFB only.
    @Test func theVideoSwitcherNeedsAnAdvertisedChannel() throws {
        #expect(AppModel.advertisesVideo(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true,\"screen_video\":{\"version\":1}}")))
        #expect(AppModel.advertisesVideo(try machine(",\"browser\":{\"enabled\":true,\"screen_sharing\":true,\"screen_video\":{\"version\":1}}")))
        #expect(!AppModel.advertisesVideo(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true}")), "shows the screen, no helper: RFB only")
        #expect(!AppModel.advertisesVideo(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":false,\"screen_video\":{\"version\":1}}")), "not sharing")
        #expect(!AppModel.advertisesVideo(try machine(",\"computer\":{\"enabled\":false,\"screen_sharing\":true,\"screen_video\":{\"version\":1}}")), "extension off")
        #expect(!AppModel.advertisesVideo(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true,\"screen_video\":{\"version\":0}}")), "version 0 is no video")
    }

    @Test func theStepsSayWhatIsLeft() {
        let setUp = BrowserGuide.setUpSteps(MachineBrowser(installed: true))
        #expect(setUp.map(\.id) == ["open", "import", "finish", "recheck"])
        #expect(setUp[1].text.contains("从 Chrome") && setUp[1].text.contains("钥匙串密码") && setUp[2].text.contains("ego-browser"))
        let on = BrowserGuide.readySteps(MachineBrowser(installed: true, setUp: true, imported: false, analytics: "on", ready: true))
        #expect(on.map(\.id) == ["setup", "import", "analytics"] && !on[2].done && on[2].text.contains("设置 › 同步和服务"))
        #expect(on[1].text.contains("先在 ego lite 里登录"))
        let off = BrowserGuide.readySteps(MachineBrowser(installed: true, setUp: true, imported: true, analytics: "off", ready: true))
        #expect(off.allSatisfy { $0.done })
    }

    @Test func toolsReadInChinese() throws {
        let use = Item(id: "1#0", seq: 1, ts: Date(timeIntervalSince1970: 1), role: "assistant", kind: .toolUse, text: nil, toolUseID: "t1",
                       toolName: "mcp__persona__browser_run",
                       toolInput: .object(["machine": .string("macbook"), "script": .string("\nawait page.goto(\"https://example.com\");\nreturn 1")]),
                       isError: nil, image: nil)
        let run = ToolCall(use: use, result: nil)
        #expect(ToolPresentation.verb(run.name) == "用浏览器")
        #expect(ToolPresentation.subject(run, machines: [try machine("")]) == "MacBook · await page.goto(\"https://example.com\");",
                "the Mac's name, not its id")
        #expect(ToolPresentation.truncation(run.name) == .tail)
        #expect(ToolPresentation.verb("mcp__persona__browser_look") == "看浏览器")
    }
}
