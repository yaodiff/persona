import Foundation
import Testing
@testable import Persona

/// The Terminals page (#4754): every pane of a computer, Agents' and plain ones, and a stopped Agent's left behind; and
/// a stopped Agent's row is read-only (its terminal opens, nothing is sent).
@MainActor struct TerminalsListTests {
    private func panes(_ json: String) -> TerminalPanes {
        do { return try JSON.decoder.decode(TerminalPanes.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func session(_ extra: String, status: String = "offline") -> Session {
        let json = """
        {"machine_id":"m","session_id":"s1","title":"Fix the index","cwd":"/tmp/p","status":"\(status)","state":"offline"\(extra)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func everyPaneIsARowOfItsKind() {
        let list = panes("""
        {"hosts":[{"kind":"herdr","new_tab":true,"workspaces":[{"id":"w1","label":"app"},{"id":"w2","label":""}],
          "tabs":[{"id":"w1:t1","workspace_id":"w1","label":"agent-1"}],
          "panes":[{"pane":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","agent":"claude","session_id":"a1"},
                   {"pane":"w1:p2","tab_id":"w1:t1","workspace_id":"w1","foreground":"vim","cwd":"/home/me/app"},
                   {"pane":"w2:p1","tab_id":"w2:t1","workspace_id":"w2","agent_session":"s1"}]}]}
        """)
        let rows = TerminalsList.rows(list)
        #expect(rows.map(\.kind) == [.agent, .plain, .finished])
        #expect(rows[1].pane.terminal == "pane:w1:p2", "a pane without its address gets the pane: one")
        #expect(rows[1].pane.foreground == "vim")
        #expect(TerminalsList.hasFinished(list))
        #expect(TerminalsList.spaces(list, newSpace: true).map(\.label) == ["app", "w2"], "a workspace without a label is named by its id")
        #expect(TerminalsList.folder("/home/me/app") == "app")
        #expect(TerminalsList.folder(nil).isEmpty)
    }

    @Test func rowsSayWhatRunsOnce() {
        #expect(TerminalsList.agentName("codex") == "Codex", "the app's name, not the runtime id")
        #expect(TerminalsList.agentName("claude") == "Claude")
        #expect(TerminalsList.agentName(nil) == "Claude")
        #expect(TerminalsList.agentName("pi") == "pi")
        #expect(TerminalsList.plainDetails(title: "shell · zsh", foreground: "zsh", folder: "persona") == ["persona"],
                "the title names the program already")
        #expect(TerminalsList.plainDetails(title: "shell", foreground: "vim", folder: "app") == ["vim", "app"])
        #expect(TerminalsList.plainDetails(title: "shell", foreground: nil, folder: "").isEmpty)
    }

    @Test func noNewTerminalWhereTheHostCannotOpenOne() {
        let list = panes(#"{"hosts":[{"kind":"orca","new_tab":false,"workspaces":[{"id":"w1","label":"app"}],"panes":[]}]}"#)
        #expect(TerminalsList.spaces(list, newSpace: true).isEmpty)
        #expect(!TerminalsList.hasFinished(list))
    }

    @Test func anEngineWithNoWorkspaceOffersTheHomeFolder() {
        let list = panes(#"{"hosts":[{"kind":"herdr","new_tab":true,"workspaces":[],"panes":[]},{"kind":"orca","new_tab":false,"workspaces":[],"panes":[]}]}"#)
        let spaces = TerminalsList.spaces(list, newSpace: true)
        #expect(spaces.map(\.id) == [""], "one home-folder entry, for the host that opens tabs only")
        #expect(spaces.first?.kind == "herdr")
        #expect(TerminalsList.spaces(list, newSpace: false).isEmpty, "a hub without pane_new_space: the Agent line instead")
        #expect(TerminalsList.rows(list).isEmpty)
    }

    @Test func aStoppedAgentIsReadOnly() {
        let stopped = session(#","capabilities":{"read":true,"terminal":true},"read_only_reason":"stopped""#)
        #expect(stopped.isStopped)
        #expect(!stopped.canSend)
        #expect(stopped.canTerminal)
        #expect(!stopped.canInterrupt)

        let old = session("") // a node before #4754: no capabilities, as before
        #expect(!old.isStopped)
        #expect(old.canSend)

        let live = session("", status: "idle")
        #expect(!live.isStopped)
        #expect(live.canSend && live.canTerminal)
    }

    /// #6153: a computer's section ends up listed, failed or late with its request's id, never on a bare spinner; an
    /// answer to a request a newer one replaced is ignored, and a new request clears the old failure and lateness.
    @Test func aComputerNeverSitsOnABareSpinner() {
        let list = panes(#"{"hosts":[]}"#)
        var load = TerminalsLoad()
        #expect(load.shown == .waiting)
        load.expire(nil) // the spinner showed with no request of this page's
        #expect(load.shown == .late(nil))

        load.start("aaaa1111")
        #expect(load.shown == .waiting, "a new request clears lateness")
        load.expire("old")
        #expect(load.shown == .waiting, "another request's patience is not this one's")
        load.expire("aaaa1111")
        #expect(load.shown == .late("aaaa1111"))
        load.answer("aaaa1111", list)
        #expect(load.shown == .rows(list), "a late answer still lists")

        load.start("bbbb2222")
        #expect(load.shown == .rows(list), "the list stays while a refresh is on its way")
        load.expire("bbbb2222")
        #expect(load.shown == .rows(list))
        load.fail("aaaa1111", "stale")
        #expect(load.shown == .rows(list), "a replaced request's failure is ignored")
        load.fail("bbbb2222", "The request timed out.")
        #expect(load.shown == .failed("The request timed out."))
        load.start("cccc3333")
        #expect(load.shown == .rows(list), "a new request clears the failure")

        #expect(TerminalsLoad(failure: "Not connected").shown == .failed("Not connected"))
        let named = TerminalsLoad.withRequest("No answer", "cccc3333") // the tests run in zh-Hans
        #expect(named.hasPrefix("No answer") && named.contains("cccc3333"))
        #expect(TerminalsLoad.withRequest("Bad (request cccc3333)", "cccc3333") == "Bad (request cccc3333)", "said once")
        #expect(TerminalsLoad.withRequest("No answer", nil) == "No answer")
        #expect(TerminalsLoad.newRequest().count == 8)
    }

    /// #6996: a plain terminal's row renames its tab on herdr and Tern, as the web's node menu does; an Agent's row (its
    /// own Rename Agent…), a stopped Agent's leftover and another engine's terminal don't offer it.
    @Test func plainTerminalsRenameTheirTab() {
        let list = panes("""
        {"hosts":[{"kind":"herdr","tabs":[{"id":"w1:t1","workspace_id":"w1","label":"agent-1"},{"id":"w1:t2","workspace_id":"w1","label":"shell"}],
          "panes":[{"pane":"w1:p1","tab_id":"w1:t1","workspace_id":"w1","agent":"claude","session_id":"a1"},
                   {"pane":"w1:p2","tab_id":"w1:t2","workspace_id":"w1"},
                   {"pane":"w1:p3","tab_id":"w1:t2","workspace_id":"w1","agent_session":"s1"},
                   {"pane":"w1:p4","workspace_id":"w1"}]},
         {"kind":"tern","tabs":[{"id":"t9","workspace_id":"x","label":"logs"}],"panes":[{"pane":"t9p","tab_id":"t9","workspace_id":"x"}]},
         {"kind":"orca","tabs":[{"id":"o1","workspace_id":"y","label":"build"}],"panes":[{"pane":"o1p","tab_id":"o1","workspace_id":"y"}]}]}
        """)
        let mac = MachineRef(group: "g", id: "mac")
        let rows = TerminalsList.rows(list)
        #expect(rows.map(\.host) == ["herdr", "herdr", "herdr", "herdr", "tern", "orca"])
        let targets = rows.map { TabRename.target($0, in: list, on: mac) }
        #expect(targets[0] == nil, "an Agent's row renames the Agent")
        #expect(targets[1] == TabRename.Target(machine: mac, tabID: "w1:t2", host: "herdr", name: "shell"), "the prompt starts from the tab's name")
        #expect(targets[2] == nil, "a stopped Agent's leftover is titled by that Agent")
        #expect(targets[3] == nil, "a pane that names no tab")
        #expect(targets[4]?.name == "logs" && targets[4]?.host == "tern")
        #expect(targets[5] == nil, "Orca is not asked")
        #expect(TabRename.canRename(host: ""), "a node that does not say runs herdr")

        let t = targets[1]!
        #expect(TabRename.body(t, name: "  dev   server ") == ["tab_id": "w1:t2", "label": "dev server", "kind": "herdr"])
        #expect(TabRename.body(TabRename.Target(machine: mac, tabID: "2", host: "", name: ""), name: "x") == ["tab_id": "2", "label": "x"],
                "no kind for a node that does not say")
    }

    /// Save is on only for a name the computer takes (1–80 characters once white space is folded) that isn't the one it has.
    @Test func saveWantsANewNameTheComputerTakes() {
        #expect(TabRename.cleaned("  a \t b\n ") == "a b")
        #expect(!TabRename.canSave("", current: "shell"))
        #expect(!TabRename.canSave("   ", current: "shell"))
        #expect(!TabRename.canSave(" shell ", current: "shell"), "unchanged")
        #expect(TabRename.canSave("dev", current: "shell"))
        #expect(TabRename.canSave("dev", current: ""), "a tab with no name yet")
        #expect(TabRename.canSave(String(repeating: "名", count: 80), current: "shell"))
        #expect(!TabRename.canSave(String(repeating: "名", count: 81), current: "shell"))
    }

    /// The new name shows at once and the old one comes back the same way; another host's tab of the same id is left alone.
    @Test func theNewNameShowsAtOnceAndGoesBackOnARefusal() {
        let list = panes("""
        {"hosts":[{"kind":"herdr","tabs":[{"id":"w1:t2","workspace_id":"w1","label":"shell"}],"panes":[{"pane":"w1:p2","tab_id":"w1:t2","workspace_id":"w1","title":"zsh"}]},
                  {"kind":"tern","tabs":[{"id":"w1:t2","workspace_id":"w1","label":"other"}],"panes":[]}]}
        """)
        let renamed = TabRename.renamed(list, host: "herdr", tab: "w1:t2", to: "dev")
        #expect(renamed.hosts[0].tabs[0].label == "dev")
        #expect(renamed.hosts[1].tabs[0].label == "other")
        #expect(NodeMenu.shellTitle(renamed.hosts[0].panes[0], in: renamed) == "dev · zsh", "the row's title follows")
        #expect(TabRename.renamed(renamed, host: "herdr", tab: "w1:t2", to: "shell") == list)
    }

    /// A refusal is one plain sentence: the computer away, an engine that can't, a terminal already closed; else the
    /// hub's words after "Couldn't rename".
    @Test func aRefusalIsOnePlainSentence() {
        func failure(_ status: Int, _ message: String, code: String) -> String {
            var e = APIError(status: status, message: message, fields: ["code": code, "error": message])
            if status == 502 && code == "internal" { e.showsNodeReason = true } // as APIClient.nodeAction marks it
            return TabRename.failure(e, computer: "Studio")
        }
        let offline = failure(503, "machine studio is offline", code: "node_unreachable")
        let unsupported = failure(502, "unsupported: orca cannot do this", code: "internal")
        let gone = failure(502, "tab w1:t2 is not running", code: "internal")
        let other = failure(409, "Something else", code: "conflict")
        #expect(offline.contains("Studio") && !offline.contains("machine studio"), "the computer's name, not the hub's words")
        #expect(unsupported.contains("Studio") && !unsupported.contains("orca"), "no engine name or raw error")
        #expect(!gone.contains("w1:t2"), "no tab id")
        #expect(other.contains("Something else"))
        #expect(Set([offline, unsupported, gone, other]).count == 4)
        #expect(failure(502, "no tab", code: "internal") == gone)
    }

    /// The every-few-seconds read takes a changed list only: an unchanged answer, one overtaken by a newer request, or
    /// one for a failed or not yet listed computer changes nothing.
    @Test func theQuietRefreshTakesOnlyAChangedList() {
        let a = panes(#"{"hosts":[{"kind":"herdr","tabs":[{"id":"t","workspace_id":"w","label":"shell"}],"panes":[]}]}"#)
        let b = TabRename.renamed(a, host: "herdr", tab: "t", to: "dev")
        var load = TerminalsLoad()
        load.start("r1")
        let early = load.refreshed(b, during: "r1")
        #expect(!early, "nothing listed yet: the request's own answer comes")
        load.answer("r1", a)
        let unchanged = load.refreshed(a, during: "r1")
        #expect(!unchanged)
        let overtaken = load.refreshed(b, during: "r0")
        #expect(!overtaken, "a newer request started meanwhile")
        let taken = load.refreshed(b, during: "r1")
        #expect(taken)
        #expect(load.shown == .rows(b))
        load.start("r2")
        load.fail("r2", "offline")
        let failed = load.refreshed(a, during: "r2")
        #expect(!failed, "a failed computer waits for Retry")
        #expect(TerminalsLoad.refreshEvery == .seconds(8), "as often as the web's Terminal page")
    }

    /// #7115: every computer's answer reaches its row, failures included; the page never ends with answers dropped.
    @Test func everyComputersAnswerIsApplied() async {
        struct Down: Error {}
        let one = panes(#"{"hosts":[{"kind":"herdr","panes":[{"pane":"w1:p1"}]}]}"#)
        let jobs = (1...8).map { n in
            TerminalsFetch.Job(path: "m\(n)", request: "r\(n)") {
                try await Task.sleep(for: .milliseconds(10 * (9 - n))) // the last computer answers first
                if n == 5 { throw Down() }
                return one
            }
        }
        var applied: [String: String] = [:]
        await TerminalsFetch.run(jobs) { job, outcome in
            switch outcome {
            case .answered(let list): applied[job.path] = "answered \(list.hosts.count)"
            case .failed: applied[job.path] = "failed"
            case .left: applied[job.path] = "left"
            }
        }
        #expect(applied.count == 8, "every computer is answered when run returns: \(applied)")
        #expect(applied["m5"] == "failed")
        #expect(applied.filter { $0.value == "answered 1" }.count == 7)
    }

    /// A page that goes away cancels its requests: the computers run late with Retry, nothing is shown as failed.
    @Test func aCancelledPageLeavesItsComputers() async {
        let jobs = (1...3).map { n in
            TerminalsFetch.Job(path: "m\(n)", request: "r\(n)") {
                try await Task.sleep(for: .seconds(60))
                return TerminalPanes(hosts: [])
            }
        }
        var applied: [String: String] = [:]
        let page = Task { @MainActor in
            await TerminalsFetch.run(jobs) { job, outcome in
                if case .left = outcome { applied[job.path] = "left" } else { applied[job.path] = "other" }
            }
        }
        try? await Task.sleep(for: .milliseconds(100))
        page.cancel()
        await page.value
        #expect(applied == ["m1": "left", "m2": "left", "m3": "left"])
    }
}
