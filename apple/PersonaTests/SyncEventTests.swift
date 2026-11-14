import Foundation
import Testing
@testable import Persona
import PersonaWire

struct SyncEventTests {
    @Test func decodesRevs() throws {
        let data = Data(#"{"type":"revs","machine_id":"m1","revs":{"s1":120,"s2":7}}"#.utf8)
        guard case .revs(let machine, let revs) = try #require(ServerEvent.decode(data)) else { Issue.record("not revs"); return }
        #expect(machine == "m1")
        #expect(revs == ["s1": 120, "s2": 7])
    }

    @Test func decodesLark() throws {
        let data = Data(#"{"type":"lark","feed_ids":["a","b"]}"#.utf8)
        guard case .lark(let ids, let settingsChanged, _) = try #require(ServerEvent.decode(data)) else { Issue.record("not lark"); return }
        #expect(ids == ["a", "b"])
        #expect(!settingsChanged)
    }

    @Test func decodesSettingsBatchedWithMessages() throws {
        let data = Data(#"{"type":"lark","feed_ids":["a"],"settings_changed":true}"#.utf8)
        guard case .lark(let ids, let settingsChanged, _) = try #require(ServerEvent.decode(data)) else { Issue.record("not lark"); return }
        #expect(ids == ["a"] && settingsChanged)
    }

    @Test func decodesAgents() throws {
        let data = Data(#"{"type":"agents","session_ids":["lark-bridge:agent:grok_bot:a:b/"]}"#.utf8)
        guard case .agents(let ids) = try #require(ServerEvent.decode(data)) else { Issue.record("not agents"); return }
        #expect(ids == ["lark-bridge:agent:grok_bot:a:b/"])
    }

    /// A tiered look moved to another tier (docs/apple/persona.md): the app lists the personas again.
    @Test func decodesPersonas() throws {
        guard case .personas = try #require(ServerEvent.decode(Data(#"{"type":"personas"}"#.utf8))) else {
            Issue.record("not personas"); return
        }
    }

    @Test func personalAgentPathEncodesSlashAndColon() {
        #expect(APIClient.agentPath("lark-bridge:agent:x:y/") == "/api/v1/agents/sessions/lark-bridge%3Aagent%3Ax%3Ay%2F")
    }

    @Test func personalAgentMessageKeepsUnknownBlocks() throws {
        let json = #"{"externalId":"t1","role":"assistant","blocks":[{"type":"text","text":"hi"},{"type":"tool-call","name":"web"},{"type":"image","url":"u"}],"createTimeMs":1,"position":3}"#
        let m = try JSON.decoder.decode(PersonalAgentMessage.self, from: Data(json.utf8))
        #expect(m.blocks.map(\.type) == ["text", "tool-call", "image"])
        #expect(m.blocks[1].name == "web")
        #expect(m.local == nil)
    }

    @Test func sessionCarriesRev() throws {
        let s = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s","status":"idle","rev":42}"#.utf8))
        #expect(s.rev == 42)
    }

    @Test func sessionCarriesModes() throws {
        let s = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s","status":"idle","permission_mode":"plan","effort":"xhigh"}"#.utf8))
        #expect(s.permissionMode == "plan")
        #expect(s.effort == "xhigh")
        #expect(SessionModes.modeLabel(s.permissionMode) == "只做计划")
        #expect(SessionModes.effortLabel(s.effort) == "很高")
        #expect(SessionModes.modeLabel("on-request") == "on-request") // unknown values (Codex) show as they are
        let auto = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s","status":"idle","effort":"auto"}"#.utf8))
        #expect(auto.effort == "auto")
        #expect(SessionModes.effortLabel(auto.effort) == "自动")
        #expect(SessionModes.effortLevels.first?.id == "auto") // first in the running session's menu
        #expect(!SessionModes.startEffortLevels.contains { $0.id == "auto" }) // same as Use Launcher's when starting
        let old = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"s","status":"idle"}"#.utf8))
        #expect(old.permissionMode == nil && old.effort == nil)
    }

    @Test func modeCapabilitiesDriveModeControls() throws {
        func decode(_ body: String) throws -> Session { try JSON.decoder.decode(Session.self, from: Data(body.utf8)) }
        let desktop = try decode(#"{"session_id":"desktop","status":"working","agent":"claude","source_kind":"claude_desktop","capabilities":{"read":true,"send":true,"modes":false}}"#)
        #expect(desktop.canSend && !desktop.canChangeModes)
        let orca = try decode(#"{"session_id":"orca","status":"working","agent":"claude","source_kind":"runtime","capabilities":{"read":true,"send":true,"modes":true}}"#)
        #expect(orca.canChangeModes)
        let agy = try decode(#"{"session_id":"agy","status":"working","agent":"agy","source_kind":"runtime","capabilities":{"read":true,"send":true,"modes":true}}"#)
        #expect(agy.canChangeModes)
        #expect(agy.capabilities?.modes == true)

        // An older hub omits modes: preserve the old agent-based behavior rather than treating absence as a refusal.
        let oldDesktop = try decode(#"{"session_id":"old-desktop","status":"working","agent":"claude","source_kind":"claude_desktop","capabilities":{"read":true,"send":true}}"#)
        #expect(oldDesktop.canChangeModes)
        let oldAgy = try decode(#"{"session_id":"old-agy","status":"working","agent":"agy","source_kind":"runtime","capabilities":{"read":true,"send":true}}"#)
        #expect(!oldAgy.canChangeModes)
        let noSend = try decode(#"{"session_id":"read-only","status":"working","agent":"agy","source_kind":"runtime","capabilities":{"read":true,"modes":true}}"#)
        #expect(!noSend.canChangeModes)
        let offlineAgy = try decode(#"{"session_id":"offline-agy","status":"offline","agent":"agy","source_kind":"runtime","capabilities":{"read":true,"send":true,"modes":true}}"#)
        #expect(!offlineAgy.canChangeModes)
        // As on the web, an assistant can change its modes even while offline: the hub applies them to the next run.
        let offlineAssistant = try decode(#"{"session_id":"offline-assistant","status":"offline","assistant":true}"#)
        #expect(offlineAssistant.canChangeModes)
        let assistantNoModes = try decode(#"{"session_id":"assistant-no-modes","status":"working","assistant":true,"capabilities":{"send":true,"modes":false}}"#)
        #expect(!assistantNoModes.canChangeModes)
    }

    /// A Codex thread the hub drives through its app-server lists its levels (`efforts`): only its effort changes, among them.
    @Test func codexThreadOnItsAppServerSetsItsEffortOnly() throws {
        func decode(_ body: String) throws -> Session { try JSON.decoder.decode(Session.self, from: Data(body.utf8)) }
        let live = try decode(#"{"session_id":"s","status":"idle","agent":"codex","source_kind":"codex_app_server","capabilities":{"read":true,"send":true},"effort":"high","efforts":["low","high","ultra"]}"#)
        #expect(live.setsEffortOnly && !live.isClaude)
        #expect(SessionModes.effortOptions(live).map(\.id) == ["low", "high", "ultra"])
        #expect(SessionModes.effortOptions(live).map(\.title) == ["低", "高", "极高"])
        #expect(SessionModes.effortLabel("minimal") == "极低")
        let pane = try decode(#"{"session_id":"s","status":"idle","agent":"codex","efforts":["medium"]}"#)
        #expect(pane.setsEffortOnly) // a herdr pane joined on its app-server: no source, its levels say so
        let terminal = try decode(#"{"session_id":"s","status":"idle","agent":"codex"}"#)
        #expect(!terminal.setsEffortOnly)
        #expect(SessionModes.effortOptions(terminal).map(\.id) == SessionModes.effortLevels.map(\.id))
        let readOnly = try decode(#"{"session_id":"s","status":"idle","source_kind":"codex_app_server","capabilities":{"read":true},"efforts":["low"]}"#)
        #expect(!readOnly.setsEffortOnly)
        let claude = try decode(#"{"session_id":"s","status":"idle","efforts":["low"]}"#)
        #expect(!claude.setsEffortOnly) // Claude Code changes both
    }

    @Test func sessionSourcesDecodeConservatively() throws {
        func decode(_ body: String) throws -> Session { try JSON.decoder.decode(Session.self, from: Data(body.utf8)) }
        let old = try decode(#"{"session_id":"aabbccdd","status":"working"}"#)
        #expect(old.canRead && old.canSend && old.canInterrupt && old.canApprove && old.canTerminal)
        #expect(!old.canAttach && old.sourceSubtitle == nil)
        let missing = try decode(#"{"session_id":"opaque","source_kind":"codex_app_server"}"#)
        #expect(!missing.canRead && !missing.canSend && !missing.canInterrupt && !missing.canApprove && !missing.canTerminal && !missing.canAttach)
        let history = try decode(#"{"session_id":"opaque","source_kind":"codex_history","source_thread_id":"thread","source_instance_id":"instance","parent_thread_id":"parent","capabilities":{"read":true},"read_only_reason":"外部进程正在使用此会话"}"#)
        #expect(history.canRead && !history.canSend && !history.canInterrupt && !history.canApprove && !history.canTerminal && !history.canAttach)
        #expect(history.sourceSubtitle == "Codex 历史 · 仅查看：外部进程正在使用此会话")
        let bg = try decode(#"{"session_id":"s","source_kind":"claude_bg","capabilities":{"read":true},"read_only_reason":"回复请在电脑上打开"}"#)
        #expect(bg.sourceSubtitle == "Claude 后台 · 仅查看：回复请在电脑上打开")
        let desktop = try decode(#"{"session_id":"s","source_kind":"claude_desktop","capabilities":{"read":true,"send":true}}"#)
        #expect(desktop.canSend && !desktop.canInterrupt && !desktop.canApprove && !desktop.canTerminal)
        #expect(desktop.sourceSubtitle == "Claude 桌面版")
        let cli = try decode(#"{"session_id":"s","source_kind":"claude_cli","capabilities":{"read":true},"read_only_reason":"回复请在电脑上"}"#)
        #expect(cli.sourceSubtitle == "终端里的 Claude · 仅查看：回复请在电脑上")
        #expect(history.resumeCommand == "codex resume")
        #expect(history.sourceThreadID == "thread" && history.sourceInstanceID == "instance" && history.parentThreadID == "parent")
        let roundTrip = try JSON.decoder.decode(Session.self, from: JSON.encoder.encode(history))
        #expect(roundTrip == history)
    }

    @Test func liveSourceMenusFollowCapabilitiesNotStatus() throws {
        var live = try JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"opaque","source_kind":"codex_app_server","status":"working","capabilities":{"read":true,"send":true,"attach":true},"attached":false}"#.utf8))
        #expect(live.canSend && live.canAttach && !live.canTerminal && !live.canInterrupt && !live.canApprove)
        live.attached = true
        #expect(!live.canAttach)
        live.capabilities?.interrupt = true
        #expect(live.canInterrupt)
        #expect(live.agentLabel == "Codex" && live.resumeCommand == "codex resume")
    }

    @Test func resumeLineQuotesTheFolderAndSkipsAMissingOne() {
        #expect(Session.resumeLine(cwd: "/w/My Projects", command: "codex resume", id: "abc")
                == "cd '/w/My Projects' && codex resume abc")
        #expect(Session.resumeLine(cwd: "/w/it's; rm -rf ~", command: "claude --resume", id: "abc")
                == #"cd '/w/it'\''s; rm -rf ~' && claude --resume abc"#)
        #expect(Session.resumeLine(cwd: nil, command: "claude --resume", id: "abc") == "claude --resume abc")
        #expect(Session.resumeLine(cwd: "", command: "claude --resume", id: "abc") == "claude --resume abc")
    }

    @MainActor @Test func rowMenuEqualityIncludesPermissions() {
        let actions = AgentRowMenu.Actions()
        let key = SessionKey(machineID: "m", sessionID: "s")
        let old = AgentRowMenu(key: key, pinned: false, muted: false, looping: false, live: true, actions: actions)
        let external = AgentRowMenu(key: key, pinned: false, muted: false, looping: false, live: true, terminal: false, attachable: true, actions: actions)
        #expect(old != external)
        var attached = external
        attached.attachable = false
        #expect(attached != external)
        #expect(external == external)
        // A runtime's session renames and closes where its node says so (#6294): the menu redraws when that changes.
        var runtime = external
        runtime.renamable = false
        #expect(runtime != external)
        runtime = external
        runtime.closable = false
        #expect(runtime != external)
    }

    @Test func lazyImagesLearnTheirRef() {
        var it = Item(id: "u#1", seq: 3, ts: nil, role: "user", kind: .image, text: nil, toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
        var img = ImagePayload(mime: "image/png", data: "")
        img.lazy = true
        it.image = img
        let out = ChatStore.withImageRefs([it], SessionKey(machineID: "m", sessionID: "s"))
        #expect(out[0].image?.ref == "m/s/u#1")
    }
}
