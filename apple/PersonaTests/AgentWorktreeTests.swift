import Foundation
import Testing
@testable import Persona

/// A new agent's own worktree (#6115): the branch it gets, where it checks out, and what closing or deleting the agent
/// asks before removing it.
struct AgentWorktreeTests {
    @Test func theBranchIsNamedAsTheNodeNamesIt() {
        #expect(AgentWorktree.branch(nameHint: "Fix the login crash!") == "persona/fix-the-login-crash")
        #expect(AgentWorktree.branch(nameHint: "修一下登录") == "persona/agent")
        let long = AgentWorktree.branch(nameHint: String(repeating: "abcd ", count: 20))
        #expect(long.count <= AgentWorktree.prefix.count + AgentWorktree.maxSlug && !long.hasSuffix("-"))
    }

    @Test func aPromptWithoutLettersNamesItAfterTheFolder() {
        #expect(AgentWorktree.nameHint(prompt: "add a widget", cwd: "/Users/me/code/weather") == "add a widget")
        #expect(AgentWorktree.nameHint(prompt: "加一个小组件", cwd: "/Users/me/code/weather/") == "weather")
        #expect(AgentWorktree.branch(nameHint: AgentWorktree.nameHint(prompt: "", cwd: "/Users/me/code/photo-sync")) == "persona/photo-sync")
    }

    @Test func itChecksOutUnderTheComputersDirectory() {
        let repo = WorktreeStatus(path: "/Users/me/code/weather", checkouts: "/Users/me/code/weather/.worktrees")
        #expect(AgentWorktree.checkout(repo, branch: "persona/weather") == "/Users/me/code/weather/.worktrees/persona-weather")
        #expect(AgentWorktree.checkout(WorktreeStatus(path: "/r"), branch: "persona/x") == "") // an older node: not said
    }

    @Test func statusDecodesWhatTheNodeSends() throws {
        let json = #"{"worktree":true,"ours":true,"path":"/r/.worktrees/w","branch":"persona/w","main_branch":"main","dirty":2,"unmerged":3}"#
        let st = try JSONDecoder().decode(WorktreeStatus.self, from: Data(json.utf8))
        #expect(st.ours && st.dirty == 2 && st.unmerged == 3 && st.mainBranch == "main" && st.checkouts.isEmpty)
        // Not in a repository: no path, so no switch.
        let none = try JSONDecoder().decode(WorktreeStatus.self, from: Data(#"{"worktree":false,"ours":false,"dirty":0,"unmerged":0}"#.utf8))
        #expect(none.path.isEmpty)
    }

    /// The engine an agent ran in made its worktree, and is the one asked about it and to remove it (a Paseo agent's is
    /// Paseo's); a session that names none leaves it to the computer's default.
    @Test func theEngineTheAgentRanInIsAsked() throws {
        func session(_ fields: String) throws -> Session {
            try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"mac","session_id":"s1","status":"idle"\#(fields)}"#.utf8))
        }
        #expect(AgentWorktree.engine(of: try session(#","pane_id":"w1:p1""#)) == "herdr")
        #expect(AgentWorktree.engine(of: try session(#","pane_id":"orca:t1""#)) == "orca")
        #expect(AgentWorktree.engine(of: try session(#","source_kind":"runtime","space_kind":"paseo""#)) == "paseo")
        #expect(AgentWorktree.engine(of: try session(#","pane_id":"botmux:x1""#)) == nil) // a Feishu topic, no engine
        #expect(AgentWorktree.engine(of: try session(#","source_kind":"runtime","space_kind":"dsh""#)) == nil)
        #expect(AgentWorktree.engine(of: try session("")) == nil)
    }

    @Test func onlyOneOfOursIsOfferedForCleanup() {
        #expect(AgentWorktree.cleanupOffered(WorktreeStatus(worktree: true, ours: false, path: "/r/w")) == nil)
        #expect(AgentWorktree.cleanupOffered(WorktreeStatus(worktree: true, ours: true, path: "")) == nil)
        #expect(AgentWorktree.cleanupOffered(WorktreeStatus(worktree: true, ours: true, path: "/r/w")) != nil)
        #expect(AgentWorktree.cleanupOffered(nil) == nil)
    }

    @Test func removingWhatLosesWorkWaitsForTheConfirmation() {
        let st = WorktreeStatus(worktree: true, ours: true, path: "/r/w", branch: "persona/w", dirty: 0, unmerged: 3)
        var c = AgentWorktree.Cleanup()
        #expect(AgentWorktree.ready(st, c)) // keeping is the default and loses nothing
        c.remove = true
        #expect(AgentWorktree.ready(st, c) && !AgentWorktree.force(st, c)) // the branch stays: its commits are kept
        c.deleteBranch = true
        #expect(AgentWorktree.losing(st, c) && !AgentWorktree.ready(st, c))
        c.confirmed = true
        #expect(AgentWorktree.ready(st, c) && AgentWorktree.force(st, c))
        #expect(AgentWorktree.ready(nil, c)) // nothing to clean up
        let dirty = WorktreeStatus(worktree: true, ours: true, path: "/r/w", dirty: 2)
        #expect(AgentWorktree.loss(dirty, deleteBranch: false).dirty == 2)
        #expect(!AgentWorktree.ready(dirty, .init(remove: true)))
    }
}
