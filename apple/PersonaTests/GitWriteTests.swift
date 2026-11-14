import Foundation
import Testing
@testable import Persona

/// The Git tab's writes (#7013): the requests the web's Git panel sends, what a tick box shows, when Push shows and
/// what it asks for, one write at a time, and a refusal said in plain words with the node's own.
@MainActor
struct GitWriteTests {
    private static func status(files: String = "[]", branch: String? = "main", upstream: String? = "origin/main", ahead: Int = 0, gone: Bool? = nil) -> String {
        let b = branch.map { #""branch":"\#($0)","# } ?? ""
        let u = upstream.map { #""upstream":"\#($0)","# } ?? ""
        let g = gone.map { #""gone":\#($0),"# } ?? ""
        return #"{"is_repo":true,"root":"/work/sample",\#(b)\#(u)\#(g)"ahead":\#(ahead),"behind":0,"files":\#(files),"stashes":0,"commits":[]}"#
    }

    private static func decode(_ json: String) throws -> GitStatus { try JSON.decoder.decode(GitStatus.self, from: Data(json.utf8)) }

    private static func file(_ path: String, _ index: String, _ worktree: String) -> GitFile {
        GitFile(path: path, index: index, worktree: worktree, added: 1, deleted: 0)
    }

    // MARK: What a tick box shows (the web's stageState)

    @Test func stageMarkFollowsTheIndexAndTheTree() {
        #expect(GitStageMark(Self.file("a", "M", ".")) == .all)
        #expect(GitStageMark(Self.file("a", "A", ".")) == .all)
        #expect(GitStageMark(Self.file("a", "M", "M")) == .some)
        #expect(GitStageMark(Self.file("a", ".", "M")) == .none)
        #expect(GitStageMark(Self.file("a", "?", "?")) == .none) // new, not tracked yet
        #expect(GitStageMark(Self.file("a", "U", "U")) == .some) // unmerged
    }

    @Test func everyChangeMarkIsAllNoneOrMixed() {
        let staged = Self.file("a", "M", "."), partly = Self.file("b", "M", "M"), not = Self.file("c", ".", "M")
        #expect(GitStageMark(all: [staged, staged]) == .all)
        #expect(GitStageMark(all: [not, Self.file("d", "?", "?")]) == .none)
        #expect(GitStageMark(all: [staged, not]) == .some)
        #expect(GitStageMark(all: [partly]) == .some)
        #expect(GitStageMark(all: []) == .none)
    }

    // MARK: When Push shows (the web's PushButton)

    @Test func pushShowsForCommitsAMissingOrDeletedUpstreamOnly() throws {
        let synced = try Self.decode(Self.status(ahead: 0)), ahead = try Self.decode(Self.status(ahead: 2))
        let none = try Self.decode(Self.status(upstream: nil)), empty = try Self.decode(Self.status(upstream: ""))
        let gone = try Self.decode(Self.status(gone: true)), detached = try Self.decode(Self.status(branch: nil, ahead: 3))
        #expect(GitPushKind(synced) == nil)
        #expect(GitPushKind(ahead) == .commits(2, upstream: "origin/main"))
        #expect(GitPushKind(none) == .fresh)
        #expect(GitPushKind(empty) == .fresh) // the mock hub used to send it empty
        #expect(GitPushKind(gone) == .gone(upstream: "origin/main"))
        #expect(GitPushKind(detached) == nil)
        #expect(GitPushKind.fresh.setsUpstream && !GitPushKind.commits(1, upstream: "origin/main").setsUpstream)
    }

    // MARK: Requests and answers

    @Test func stagesAFileAndUnstagesOneAllStaged() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/stage") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status(files: #"[{"path":"a.swift","index":"M","worktree":".","added":1,"deleted":0}]"#))}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let r = await model.toggle(Self.file("a.swift", ".", "M"), machine: "mac", cwd: "/work/sample", deps: deps)
        #expect(r?.status.files.first.map { GitStageMark($0) } == .all)
        _ = await model.toggle(Self.file("a.swift", "M", "."), machine: "mac", cwd: "/work/sample", deps: deps)
        _ = await model.toggle(Self.file("b.swift", "M", "M"), machine: "mac", cwd: "/work/sample", deps: deps)
        #expect(bodies.count == 3)
        #expect(bodies[0]["cwd"] as? String == "/work/sample" && bodies[0]["paths"] as? [String] == ["a.swift"] && bodies[0]["unstage"] as? Bool == false)
        #expect(bodies[1]["unstage"] as? Bool == true)
        #expect(bodies[2]["unstage"] as? Bool == false) // a partly staged file is staged whole
        #expect(model.busy == nil && model.refusal == nil)
    }

    @Test func stageAllSendsEveryPathAndUnstagesWhenAllAreStaged() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/stage") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status())}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let mixed = try Self.decode(Self.status(files: #"[{"path":"a","index":"M","worktree":".","added":1,"deleted":0},{"path":"b","index":"?","worktree":"?","added":0,"deleted":0}]"#))
        _ = await model.toggleAll(mixed, machine: "mac", cwd: "/w", deps: deps)
        let all = try Self.decode(Self.status(files: #"[{"path":"a","index":"M","worktree":".","added":1,"deleted":0}]"#))
        _ = await model.toggleAll(all, machine: "mac", cwd: "/w", deps: deps)
        #expect(bodies.map { $0["paths"] as? [String] } == [["a", "b"], ["a"]])
        #expect(bodies.map { $0["unstage"] as? Bool } == [false, true])
    }

    @Test func commitsTheMessageAndAnswersTheCommit() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/commit") { req in
            let body = try JSONSerialization.jsonObject(with: req.body!) as! [String: String]
            #expect(body == ["cwd": "/w", "message": "Show the hourly forecast\n\nWith a test."])
            return Data(#"{"status":\#(Self.status(ahead: 1)),"commit":{"hash":"e1e1e1e1e1","short":"e1e1e1e","subject":"Show the hourly forecast","author":"Me"}}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let r = await model.commit(message: "Show the hourly forecast\n\nWith a test.", machine: "mac", cwd: "/w", deps: deps)
        #expect(r?.commit?.short == "e1e1e1e" && r?.status.ahead == 1)
        // an empty message sends nothing
        let empty = await model.commit(message: "  \n", machine: "mac", cwd: "/w", deps: deps)
        #expect(empty == nil)
        #expect(hub.sent("POST", "/api/v1/machines/mac/git/commit").count == 1)
    }

    @Test func pushSetsTheUpstreamOnlyForABranchWithoutOne() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/push") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status()),"output":"To example.invalid:weather.git"}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        _ = await model.push(.fresh, machine: "mac", cwd: "/w", deps: deps)
        _ = await model.push(.commits(2, upstream: "origin/main"), machine: "mac", cwd: "/w", deps: deps)
        #expect(bodies.map { $0["set_upstream"] as? Bool } == [true, false])
    }

    // MARK: Refusals

    @Test func aRefusalIsSaidInPlainWordsWithGitsOwnAndItsFiles() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/commit") { _ in
            throw APIError(status: 409, message: "git commit: swiftlint: 2 violations", fields: ["code": "git_hook_failed"],
                           details: .object(["code": .string("git_hook_failed"), "files": .array([.string("Provider.swift")])]))
        }
        let model = GitWriteModel()
        let r = await model.commit(message: "x", machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        #expect(r == nil && model.busy == nil)
        let refusal = try #require(model.refusal)
        #expect(refusal.title == "提交被拒绝了（通常是 pre-commit 之类的 hook）")
        #expect(refusal.detail == "git commit: swiftlint: 2 violations")
        #expect(refusal.files == ["Provider.swift"])
    }

    @Test func anOldNodeAndAnUnknownCodeHaveTheirOwnWords() {
        let old = GitRefusal(APIError(status: 502, message: "node: unknown method git.stage"))
        #expect(old.title == "那台电脑上的 Persona 版本太旧，不支持这个操作，请更新")
        let other = GitRefusal(APIError(status: 409, message: "fatal: something else", fields: ["code": "git_failed"]))
        #expect(other.title == "没有完成" && other.detail == "fatal: something else")
        #expect(GitRefusal.title("git_push_rejected") == "远端有更新的提交：先拉取，再推送")
    }

    @Test func oneWriteAtATime() async throws {
        let model = GitWriteModel()
        var release: CheckedContinuation<GitWriteResult, Never>?
        var calls = 0
        let fresh = GitWriteResult(status: try Self.decode(Self.status()), commit: nil, output: nil)
        let deps = GitWriteModel.Deps(stage: { _, _, _, _ in
            calls += 1
            return await withCheckedContinuation { release = $0 }
        }, commit: { _, _, _ in fresh }, push: { _, _, _ in fresh })
        let task = Task { await model.toggle(Self.file("a", ".", "M"), machine: "mac", cwd: "/w", deps: deps) }
        for _ in 0..<100 where release == nil { await Task.yield() }
        let continuation = try #require(release)
        #expect(model.busy == .stage("a"))
        let commit = await model.commit(message: "x", machine: "mac", cwd: "/w", deps: deps)
        let second = await model.toggle(Self.file("b", ".", "M"), machine: "mac", cwd: "/w", deps: deps)
        #expect(commit == nil && second == nil)
        continuation.resume(returning: fresh)
        _ = await task.value
        #expect(model.busy == nil && calls == 1)
    }

    // MARK: Branches and stashes (#7015)

    @Test func branchesDecodeAndARemoteOneSwitchesByItsOwnName() throws {
        let json = #"{"branches":[{"name":"widget-hourly","current":true,"hash":"a1","subject":"x","date":"2026-11-13T10:00:00Z"},{"name":"main","hash":"a2","subject":"y","date":"2026-11-13T09:00:00Z"},{"name":"origin/release/2.4","remote":true,"hash":"a3","subject":"z","date":"2026-11-12T09:00:00Z"}]}"#
        let list = try JSON.decoder.decode(GitBranchList.self, from: Data(json.utf8))
        let b = list.branches
        #expect(list.defaultBranch == nil, "an older node, or git doesn't know it")
        let named = try JSON.decoder.decode(GitBranchList.self, from: Data(#"{"branches":[],"default":"main"}"#.utf8))
        #expect(named.defaultBranch == "main")
        #expect(b.map(\.switchName) == ["widget-hourly", "main", "release/2.4"])
        #expect(b[0].current == true && b[1].current == nil && b[2].remote == true)
        #expect(Set(b.map(\.id)).count == 3)
    }

    @Test func aStashIsNamedByItsOidOnly() {
        #expect(GitStashEntry(index: 0, oid: String(repeating: "a", count: 40), message: "m").hasIdentity)
        #expect(GitStashEntry(index: 0, oid: String(repeating: "0f", count: 32), message: "m").hasIdentity)
        #expect(!GitStashEntry(index: 0, oid: nil, message: "m").hasIdentity)
        #expect(!GitStashEntry(index: 0, oid: "abc", message: "m").hasIdentity)
        #expect(!GitStashEntry(index: 0, oid: String(repeating: "A", count: 40), message: "m").hasIdentity)
    }

    @Test func switchesOrCreatesABranch() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        hub.on("POST", "/api/v1/machines/mac/git/switch") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status())}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let r = await model.switchTo("main", create: false, machine: "mac", cwd: "/w", deps: deps)
        _ = await model.switchTo("fix/rain", create: true, machine: "mac", cwd: "/w", deps: deps)
        #expect(r?.status.branch == "main")
        #expect(bodies.map { $0["branch"] as? String } == ["main", "fix/rain"])
        #expect(bodies.map { $0["create"] as? Bool } == [false, true])
        #expect(bodies.allSatisfy { $0["cwd"] as? String == "/w" })
    }

    @Test func stashWritesSendWhatTheWebSends() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        let oid = String(repeating: "a", count: 40)
        hub.on("POST", "/api/v1/machines/mac/git/stash") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return Data(#"{"status":\#(Self.status()),"stashes":[{"index":0,"oid":"\#(oid)","message":"On main: m","date":"2026-11-13T10:00:00Z"}]}"#.utf8)
        }
        let model = GitWriteModel(), deps = GitWriteModel.Deps(api: hub.client)
        let e = GitStashEntry(index: 1, oid: oid, message: "On main: m")
        let pushed = await model.stash(.push(message: "  try the palette ", untracked: true), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.stash(.push(message: " ", untracked: false), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.stash(.list, machine: "mac", cwd: "/w", deps: deps)
        _ = await model.stash(.apply(e), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.stash(.pop(e), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.stash(.drop(e), machine: "mac", cwd: "/w", deps: deps)
        #expect(pushed?.stashes?.first?.hasIdentity == true)
        #expect(bodies.map { $0["op"] as? String } == ["push", "push", "list", "apply", "pop", "drop"])
        #expect(bodies[0]["message"] as? String == "try the palette" && bodies[0]["untracked"] as? Bool == true)
        #expect(bodies[1]["message"] == nil && bodies[1]["untracked"] as? Bool == false) // no message: git's own
        #expect(bodies[2]["index"] == nil && bodies[2]["oid"] == nil)
        for b in bodies[3...] { #expect(b["index"] as? Int == 1 && b["oid"] as? String == oid) }
    }

    @Test func aStashWithoutItsOidIsNeverWritten() async {
        let hub = FakeHub()
        let model = GitWriteModel()
        let r = await model.stash(.drop(GitStashEntry(index: 0, oid: nil, message: "old")), machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        #expect(r == nil && model.refusal == nil)
        #expect(hub.sent("POST", "/api/v1/machines/mac/git/stash").isEmpty)
    }

    @Test func aSwitchRefusedForFilesInTheWayKeepsItsCodeAndFiles() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/switch") { _ in
            throw APIError(status: 409, message: "error: Your local changes to the following files would be overwritten by checkout",
                           fields: ["code": "git_conflict"],
                           details: .object(["code": .string("git_conflict"), "files": .array([.string("a.swift"), .string("b.swift")])]))
        }
        let model = GitWriteModel()
        _ = await model.switchTo("main", create: false, machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        let refusal = try #require(model.refusal)
        #expect(refusal.code == "git_conflict" && refusal.files == ["a.swift", "b.swift"])
        #expect(refusal.shownDetail == nil, "the files say it; git's words go to the log")
        #expect(GitRefusal(title: "t", detail: "hook output", code: "git_hook_failed").shownDetail == "hook output")
        #expect(GitRefusal(title: "t", detail: "git's words", code: "git_conflict").shownDetail == "git's words", "no files: git's words stay")
        #expect(refusal.title == "会覆盖未提交的改动：先提交或收起这些文件")
        #expect(GitRefusal.title("git_stash_stale") == "贮藏列表变了。刷新后重新选择贮藏。")
        #expect(GitRefusal.title("git_stash_conflicted") == "已应用，但这些文件有冲突：解决后再提交。贮藏仍保留。")
    }
}
