import Foundation
import Testing
@testable import Persona

/// New Agent's workspaces (#7259): which catalog rows are offered, how they are grouped and ordered, and that a chosen
/// Paseo workspace starts there.
@MainActor struct WorkspaceStartTests {
    private func row(_ id: String, source: String = "paseo", machine: String = "box", group: String = "", title: String = "",
                     cwd: String? = nil, branch: String = "", project: String? = "app", projectID: String? = "p1",
                     main: Bool? = nil, kind: String? = "git", available: Bool = true, online: Bool = true) -> HostWorkspace {
        HostWorkspace(machineID: machine, source: source, sourceID: id, title: title, cwd: cwd ?? "/src/\(id)", branch: branch,
                      parentSourceID: nil, index: nil, projectID: projectID, projectName: project, isMain: main,
                      workspaceKind: kind, group: group, online: online, available: available)
    }

    @Test func offersOnlyTheSelectedComputersCheckoutEnginesThatCanStartNow() {
        let catalog = [
            row("paseo-main", main: true),
            row("orca-tree", source: "orca"),
            row("herdr-space", source: "herdr"),
            row("botmux", source: "botmux"),
            row("other-computer", machine: "laptop"),
            row("other-hub", group: "second"),
            row("gone", available: false),
            row("offline", online: false),
        ]
        let ids = WorkspaceStart.choices(catalog, machine: MachineRef(id: "box")).map(\.sourceID)
        #expect(ids == ["paseo-main", "orca-tree"])
        #expect(WorkspaceStart.choices(catalog, machine: MachineRef(group: "second", id: "box")).map(\.sourceID) == ["other-hub"])
    }

    @Test func groupsByProjectMainCheckoutFirstThenWorktreesThenFoldersAndNoProjectLast() {
        let rows = [
            row("notes", title: "notes", project: nil, projectID: nil, kind: "folder"),
            row("b-fix", title: "fix-login", branch: "fix-login"),
            row("a-main", title: "app", branch: "main", main: true),
            row("a-docs", title: "Docs", branch: "docs"),
            row("api-main", title: "api", branch: "main", project: "api", projectID: "p2", main: true),
            row("scratch", title: "", cwd: "/tmp/scratch", project: "app", kind: "folder"),
        ]
        let groups = WorkspaceStart.groups(rows)
        #expect(groups.map(\.title) == ["api", "app", ""])
        #expect(groups[1].workspaces.map(\.sourceID) == ["a-main", "a-docs", "b-fix", "scratch"])
        #expect(groups[2].workspaces.map(\.sourceID) == ["notes"])
        #expect(groups[1].workspaces.map(WorkspaceStart.kind) == [.main, .worktree, .worktree, .folder])
    }

    @Test func projectsWithTheSameNameOnTwoEnginesStayApart() {
        let groups = WorkspaceStart.groups([row("p", projectID: "x"), row("o", source: "orca", projectID: "x")])
        #expect(groups.count == 2)
        #expect(Set(groups.flatMap { $0.workspaces.map(\.sourceID) }) == ["p", "o"])
    }

    @Test func rowsAreNamedByTitleThenBranchThenFolderAndSayTheBranchOnce() {
        let titled = row("t", title: "Login work", branch: "fix-login")
        #expect(WorkspaceStart.title(titled) == "Login work")
        #expect(WorkspaceStart.branch(titled) == "fix-login")
        let byBranch = row("b", title: " ", branch: "fix-login")
        #expect(WorkspaceStart.title(byBranch) == "fix-login")
        #expect(WorkspaceStart.branch(byBranch) == nil, "the branch is not said twice")
        let folder = row("f", title: "", cwd: "/src/notes/", branch: "", kind: "folder")
        #expect(WorkspaceStart.title(folder) == "notes")
        #expect(WorkspaceStart.branch(folder) == nil)
        #expect(WorkspaceStart.project(row("x", project: nil, projectID: nil)) == "")
        var fromPath = row("y", project: " ", projectID: nil)
        fromPath.projectPath = "/src/app"
        #expect(WorkspaceStart.project(fromPath) == "app")
    }

    @Test func searchKeepsMatchingRowsAndDropsEmptyGroups() {
        let groups = WorkspaceStart.groups([
            row("a", title: "app", main: true),
            row("f", title: "fix-login", branch: "fix-login"),
            row("api", title: "api", project: "api", projectID: "p2", main: true),
        ])
        #expect(WorkspaceStart.filter(groups, query: "").map(\.title) == ["api", "app"])
        let login = WorkspaceStart.filter(groups, query: "login")
        #expect(login.map(\.title) == ["app"] && login[0].workspaces.map(\.sourceID) == ["f"])
        #expect(WorkspaceStart.filter(groups, query: "api").flatMap { $0.workspaces.map(\.sourceID) } == ["api"])
    }

    @Test func aChosenPaseoWorkspaceStartsInItOnItsOwnHost() async throws {
        let picked = row("ws-7", title: "fix-login", cwd: "/src/app-fix", branch: "fix-login")
        let destination = try #require(SpacePreset.destination(preset: nil, workspace: picked))
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions", json: #"{"session":{"machine_id":"box","session_id":"new","title":"Started","cwd":"/src/app-fix","status":"idle","state":"idle"}}"#)
        _ = try await hub.client.start(.init(machineID: picked.machineID, cwd: destination.cwd, launcher: "claude", prompt: nil,
                                             workspaceID: destination.workspaceID, host: destination.host, async: true))
        let data = try #require(hub.sent("POST", "/api/v1/sessions").last?.body)
        let sent = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(sent["host"] as? String == "paseo")
        #expect(sent["workspace_id"] as? String == "ws-7")
        #expect(sent["cwd"] as? String == "/src/app-fix")
    }
}
