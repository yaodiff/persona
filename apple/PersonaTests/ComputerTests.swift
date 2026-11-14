import Foundation
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// Operating a Mac through Cua Driver (the hub's plugin cua, docs/extensions/computer-use.md): what the machine list says of it,
/// and how the assistant's two tools read in the chat.
@MainActor
struct ComputerTests {
    private func machine(_ extra: String = "") throws -> Machine {
        try JSON.decoder.decode(Machine.self, from: Data("{\"id\":\"macbook\",\"name\":\"MacBook\",\"kind\":\"ssh\",\"online\":true\(extra)}".utf8))
    }

    @Test func theMachineListSaysItOnlyWhileTheExtensionIsOn() throws {
        #expect(try machine().computer == nil, "plugin off, an older hub, not a Mac")
        let off = try machine(",\"computer\":{\"enabled\":false}")
        #expect(off.computer == MachineComputer(enabled: false))
        let c = try #require(try machine(",\"computer\":{\"enabled\":true,\"installed\":true,\"version\":\"0.38.0\",\"daemon\":true,"
            + "\"accessibility\":\"granted\",\"screen_recording\":\"missing\",\"desktop\":\"active\",\"ready\":false,\"note\":\"还没有屏幕录制权限\"}").computer)
        #expect(c.enabled && !c.ready && c.screenRecording == "missing" && c.note == "还没有屏幕录制权限")
        let m = try machine(",\"computer\":{\"enabled\":true,\"ready\":true}")
        #expect(try JSON.decoder.decode(Machine.self, from: JSON.encoder.encode(m)) == m)
    }

    private func call(_ name: String, _ input: [String: JSONValue]) -> ToolCall {
        let use = Item(id: "1#0", seq: 1, ts: Date(timeIntervalSince1970: 1), role: "assistant", kind: .toolUse, text: nil,
                       toolUseID: "t1", toolName: "mcp__persona__" + name, toolInput: .object(input), isError: nil, image: nil)
        return ToolCall(use: use, result: nil)
    }

    @Test func aToolThatOperatesAMacShowsItsScreen() throws {
        let mac = try machine()
        var work = try machine()
        work.group = "work"
        let machines = [mac, work]
        let named: JSONValue = .object(["machine": .string("MacBook")])
        #expect(ScreenEntry.machine("mcp__persona__computer_run", input: named, chat: MachineRef(id: "server"), machines: machines) == mac.ref,
                "the assistant's: the Mac it names, by name too")
        #expect(ScreenEntry.machine("mcp__persona__computer_state", input: .object(["machine": .string("macbook")]),
                                    chat: MachineRef(group: "work", id: "server"), machines: machines) == MachineRef(group: "work", id: "macbook"),
                "in the chat's group of hubs")
        #expect(ScreenEntry.machine("mcp__cua-driver__click", input: .object([:]), chat: mac.ref, machines: machines) == mac.ref,
                "an agent's own Cua Driver: its machine")
        #expect(ScreenEntry.machine("Bash", input: named, chat: mac.ref, machines: machines) == nil)
        #expect(ScreenEntry.machine("mcp__persona__computer_run", input: .object([:]), chat: mac.ref, machines: machines) == nil)
    }

    @Test func theMachineListSaysWhetherItsScreenCanBeShown() throws {
        #expect(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true}").computer?.screenSharing == true)
        #expect(try machine(",\"computer\":{\"enabled\":true}").computer?.screenSharing == false, "an older hub")
    }

    /// #3954: a Mac tells whether it can carry the live screen as hardware video (the helper's version and codecs); absent
    /// means RFB only, and it survives a round-trip.
    @Test func theMachineListSaysWhetherItCarriesVideo() throws {
        #expect(try machine(",\"computer\":{\"enabled\":true}").computer?.screenVideo == nil, "no helper, or an older node: RFB only")
        let v = try #require(try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true,"
            + "\"screen_video\":{\"version\":1,\"codecs\":[\"hevc\",\"h264\"],\"permission\":\"granted\"}}").computer?.screenVideo)
        #expect(v.advertised && v.version == 1 && v.codecs == ["hevc", "h264"] && v.permission == "granted")
        let none = try #require(try machine(",\"computer\":{\"enabled\":true,\"screen_video\":{\"version\":0}}").computer?.screenVideo)
        #expect(!none.advertised, "version 0 is no video")
        let m = try machine(",\"computer\":{\"enabled\":true,\"screen_video\":{\"version\":2,\"codecs\":[\"hevc\"]}}")
        #expect(try JSON.decoder.decode(Machine.self, from: JSON.encoder.encode(m)) == m, "round-trips")
    }

    /// #1280: one step per missing permission in System Settings' own words, the allowed ones checked first.
    @Test func theGrantGuideSaysWhatIsLeft() {
        let both = GrantGuide.steps(MachineComputer(enabled: true, installed: true, daemon: true, accessibility: "missing", screenRecording: "missing"))
        #expect(both.map(\.id) == ["accessibility", "screen", "prompt", "recheck"])
        #expect(both[0].text.contains("系统设置 › 隐私与安全性 › 辅助功能") && both[0].text.contains("「Cua Driver」"))
        #expect(both[1].text.hasPrefix("同一处的「录屏与系统录音」"))
        #expect(GrantGuide.number(of: both[3], in: both) == 4)
        #expect(GrantGuide.header(MachineComputer(accessibility: "missing", screenRecording: "missing"), asking: false) == "Cua Driver 还需要两项权限")

        let screen = GrantGuide.steps(MachineComputer(enabled: true, installed: true, daemon: true, accessibility: "granted", screenRecording: "missing"))
        #expect(screen.map(\.id) == ["accessibility", "screen", "prompt", "recheck"] && screen[0].done && !screen[1].done)
        #expect(screen[0].text == "辅助功能：已允许 Cua Driver")
        #expect(screen[1].text.contains("系统设置 › 隐私与安全性 › 录屏与系统录音") && screen[1].text.contains("屏幕录制"))
        #expect(GrantGuide.number(of: screen[1], in: screen) == 1, "the allowed one has no number")
        #expect(GrantGuide.header(MachineComputer(accessibility: "granted", screenRecording: "missing"), asking: false) == "Cua Driver 还需要一项权限")

        let accessibility = GrantGuide.steps(MachineComputer(accessibility: "missing", screenRecording: "granted"))
        #expect(accessibility.map(\.id) == ["screen", "accessibility", "recheck"], "no prompt step without screen recording")
    }

    private func click(_ name: String) -> JSONValue {
        .object(["click": .object(["app": .string("Calculator"), "role": .string("button"), "name": .string(name)])])
    }

    /// #1323: steps in words, not JSON.
    @Test func stepsReadAsWords() {
        let mixed: JSONValue = .object(["steps": .array([
            click("Clear"), .object(["type_text": .object(["text": .string("你好")])]),
            .object(["tool": .string("press_key"), "args": .object(["key": .string("return")])]),
            .object(["hotkey": .object(["keys": .array([.string("cmd"), .string("s")])])]),
            .object(["launch_app": .object(["name": .string("Safari")])]), .object(["expect": .object([:])]),
            click("Equals"), click("Done"),
        ])])
        #expect(ComputerSteps.describe("mcp__persona__computer_run", input: mixed) == "点 C → 输入「你好」 → 按 ⏎ → 按 ⌘S → 打开 Safari → 检查 → 点 = → Done")
        #expect(ComputerSteps.describe("mcp__persona__computer_state", input: .object(["app": .string("Calculator")])) == "看 Calculator")
        #expect(ComputerSteps.describe("mcp__persona__computer_state", input: .object([:])) == "看整个屏幕")
        #expect(ComputerSteps.describe("mcp__cua-driver__click", input: .object(["element_token": .string("s1:5")])) == "点 一处")
        #expect(ComputerSteps.describe("mcp__cua-driver__type_text", input: .object(["text": .string(String(repeating: "长", count: 40))]))
            == "输入「" + String(repeating: "长", count: 23) + "…」")
        #expect(ComputerSteps.isComputerTool("mcp__cua-driver__run_steps") && !ComputerSteps.isComputerTool("Bash"))
    }

    /// #1343: the chat's latest computer tool call, running until its result, then lingering a few seconds.
    @Test func theLatestOperationIsRunningUntilItsResult() throws {
        let mac = try machine()
        let t0 = Date(timeIntervalSince1970: 1000)
        func item(_ id: String, _ kind: ItemKind, _ ts: Date, name: String? = nil, input: JSONValue? = nil, use: String) -> Item {
            Item(id: id, seq: 1, ts: ts, role: kind == .toolUse ? "assistant" : "user", kind: kind, text: nil, toolUseID: use,
                 toolName: name, toolInput: input, isError: nil, image: nil)
        }
        let use = item("1", .toolUse, t0, name: "mcp__persona__computer_run", input: .object(["machine": .string("MacBook"), "steps": .array([click("All Clear")])]), use: "t1")
        let bash = item("2", .toolUse, t0, name: "Bash", input: .object([:]), use: "t2")
        let running = try #require(ComputerActivity.latest(in: [use, bash], chat: MachineRef(id: "server"), machines: [mac]))
        #expect(running.machine == mac.ref && running.machineName == "MacBook" && running.step == "点 AC" && running.running)
        #expect(running.shown(at: t0.addingTimeInterval(600)), "running: up however long")
        let done = try #require(ComputerActivity.latest(in: [use, item("3", .toolResult, t0.addingTimeInterval(2), use: "t1")],
                                                       chat: MachineRef(id: "server"), machines: [mac]))
        #expect(!done.running && done.at == t0.addingTimeInterval(2))
        #expect(done.shown(at: t0.addingTimeInterval(5)) && !done.shown(at: t0.addingTimeInterval(9)), "then fades")
        #expect(ComputerActivity.latest(in: [bash], chat: mac.ref, machines: [mac]) == nil)
    }

    @Test func theToolsReadInChinese() throws {
        let macs = [try machine()]
        let look = call("computer_state", ["machine": .string("macbook"), "app": .string("Calculator")])
        #expect(ToolPresentation.verb(look.name) == "看电脑")
        #expect(ToolPresentation.subject(look, machines: macs) == "MacBook · Calculator", "the Mac's name, not its id")
        #expect(ToolPresentation.subject(look, machines: []) == "macbook · Calculator", "no machine goes by it: as the tool said")
        #expect(ToolPresentation.subject(call("computer_state", ["machine": .string("macbook")]), machines: macs) == "MacBook · 整个屏幕")
        let steps = call("computer_run", ["machine": .string("macbook"), "steps": .array(["All Clear", "6", "Multiply", "7", "Equals"].map(click))])
        #expect(ToolPresentation.verb(steps.name) == "操控电脑")
        #expect(ToolPresentation.subject(steps, machines: macs) == "MacBook · 点 AC → 6 → × → 7 → =")
        let script = call("computer_run", ["machine": .string("macbook"), "script": .string("const app = await cua.getApp(\"Calculator\");\nreturn 1")])
        #expect(ToolPresentation.subject(script, machines: macs) == "MacBook · 运行脚本：const app = await cua.getApp(\"Calculator\");")
        #expect(ToolPresentation.symbol(steps.name) == Theme.Icon.machine)
        #expect(ToolPresentation.truncation(steps.name) == .tail && ToolPresentation.truncation("Read") == .middle,
                "steps read from their start; a path keeps its file name")
    }

    /// The screenshot comes back as a real computer_state's does (its result is text, a JPEG, text; the hub's transcript
    /// makes that a result and an image item of the same tool use): the call carries it, and it decodes.
    @Test func theScreenshotComesWithItsCall() throws {
        let size = CGSize(width: 23, height: 41)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1 // pixels as the Mac's JPEG has them
        let jpeg = try #require(UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            UIColor.orange.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
        }.jpegData(compressionQuality: 0.8))
        let use = Item(id: "a1#0", seq: 1, ts: nil, role: "assistant", kind: .toolUse, text: nil, toolUseID: "t1",
                       toolName: "mcp__persona__computer_state", toolInput: .object(["machine": .string("macbook"), "app": .string("Calculator")]),
                       isError: nil, image: nil)
        let result = Item(id: "u2#0", seq: 2, ts: nil, role: "user", kind: .toolResult, text: "{\"snapshot_id\":\"s1\"}\nwindow 1",
                          toolUseID: "t1", toolName: nil, toolInput: nil, isError: nil, image: nil)
        let shot = Item(id: "u2#1", seq: 3, ts: nil, role: "user", kind: .image, text: nil, toolUseID: "t1", toolName: nil, toolInput: nil,
                        isError: nil, image: ImagePayload(mime: "image/jpeg", data: jpeg.base64EncodedString()))
        let rows = ChatStore.buildRows([use, result, shot])
        guard case .tools(let steps)? = rows.first, case .call(let c)? = steps.first else {
            Issue.record("no tool row: \(rows)")
            return
        }
        #expect(c.images.count == 1 && rows.count == 1, "the picture goes with its call, not as a message of its own")
        let data = try #require(Data(base64Encoded: c.images[0].data))
        let image = try #require(UIImage(data: data))
        #expect(image.size == size)
    }
}
