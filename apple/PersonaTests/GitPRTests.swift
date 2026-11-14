import Foundation
import Testing
@testable import Persona

/// The branch's pull request in the Git tab (#7014): what the web's `git-pr.ts` decides (can it merge, the checks in
/// a few words, a review thread as a comment for the agent), when it is read again, and the requests it sends.
@MainActor
struct GitPRTests {
    private static let readyPR = #"{"number":128,"url":"https://github.com/example/weather/pull/128","title":"Hourly forecast","state":"OPEN","draft":false,"base":"main","head":"widget-hourly","mergeable":"MERGEABLE","merge_state":"CLEAN","review":"APPROVED","checks":[{"name":"build","state":"pass","url":"https://example.invalid/1"},{"name":"tests","state":"fail"},{"name":"lint","state":"pending"},{"name":"deploy","state":"skipped"}],"checks_state":"failing","threads":[{"path":"Provider.swift","line":42,"comments":[{"author":"reviewer-a","body":"  Fetch only the next 12 hours?  ","url":"https://example.invalid/r1","at":"2026-11-13T10:00:00Z"},{"author":"me","body":"Will do.","at":"2026-11-13T11:00:00Z"}]},{"path":"WidgetView.swift","outdated":true,"comments":[{"author":"reviewer-b","body":"Use a token for the color."}]}]}"#

    private static func pr(_ json: String = readyPR) throws -> GitPR { try JSON.decoder.decode(GitPR.self, from: Data(json.utf8)) }

    private static func status(branch: String? = "widget-hourly", upstream: String? = "origin/widget-hourly", ahead: Int = 0, gone: Bool = false, head: String = "h1") throws -> GitStatus {
        let b = branch.map { #""branch":"\#($0)","# } ?? ""
        let u = upstream.map { #""upstream":"\#($0)","# } ?? ""
        let json = #"{"is_repo":true,\#(b)\#(u)"gone":\#(gone),"ahead":\#(ahead),"behind":0,"files":[],"stashes":0,"commits":[],"head":{"hash":"\#(head)","short":"h","subject":"s","author":"a"}}"#
        return try JSON.decoder.decode(GitStatus.self, from: Data(json.utf8))
    }

    @Test func decodesAPullRequestAndNone() throws {
        let p = try Self.pr()
        #expect(p.number == 128 && p.mergeState == "CLEAN" && p.review == "APPROVED" && p.autoMerge == nil)
        #expect(p.checks.count == 4 && p.threads.count == 2 && p.threads[1].line == nil && p.threads[1].outdated == true)
        // a node that leaves the lists out
        let bare = try Self.pr(#"{"number":3,"state":"OPEN","base":"main","head":"x"}"#)
        #expect(bare.checks.isEmpty && bare.threads.isEmpty && bare.title.isEmpty)
        let none = try JSON.decoder.decode(GitPRAnswer.self, from: Data(#"{"branch":"x","pr":null}"#.utf8))
        #expect(none.pr == nil && none.branch == "x")
        #expect(Set(p.checks.map(\.id)).count == 4 && Set(p.threads.map(\.id)).count == 2)
    }

    @Test func mergeReadyFollowsGh() throws {
        var p = try Self.pr()
        #expect(GitMergeReady(p) == .now)
        p.mergeState = "UNSTABLE"; #expect(GitMergeReady(p) == .now)
        p.mergeState = "BLOCKED"; #expect(GitMergeReady(p) == .later)
        p.mergeState = "DIRTY"; #expect(GitMergeReady(p) == .conflict)
        p.mergeState = "CLEAN"; p.mergeable = "CONFLICTING"; #expect(GitMergeReady(p) == .conflict)
        p.mergeable = "MERGEABLE"; p.draft = true; #expect(GitMergeReady(p) == .no)
        p.draft = false; p.state = "MERGED"; #expect(GitMergeReady(p) == .no)
    }

    @Test func checksInAFewWordsAndThreadsAsComments() throws {
        let p = try Self.pr()
        #expect(GitPRText.counts(p.checks) == GitPRText.Counts(pass: 1, fail: 1, pending: 1, skipped: 1))
        #expect(GitPRText.place(p.threads[0]) == "Provider.swift:42" && GitPRText.place(p.threads[1]) == "WidgetView.swift")
        let c = GitPRText.comment(p, p.threads[0])
        #expect(c.quoted == "@reviewer-a: Fetch only the next 12 hours?\n\n@me: Will do.")
        #expect(c.note == "拉取请求 #128 在 Provider.swift:42 的一条评审意见：请处理。")
        #expect(GitPRText.comment(p, p.threads[1]).note.contains("那段代码之后改过"))
        #expect(GitPRText.state(p) == "开启" && GitPRText.review("CHANGES_REQUESTED") == "要求修改" && GitPRText.review("NEW") == "NEW")
    }

    @Test func readsOnceWhatWasPushedMovesAndAlwaysWithForce() async throws {
        var reads = 0
        let deps = GitPRModel.Deps(read: { _, _ in
            reads += 1
            return try JSON.decoder.decode(GitPRAnswer.self, from: Data(#"{"branch":"widget-hourly","pr":\#(Self.readyPR)}"#.utf8))
        }, create: { _, _, _, _, _, _ in throw CancellationError() }, merge: { _, _, _, _ in throw CancellationError() })
        let model = GitPRModel()
        let s = try Self.status()
        await model.read(s, machine: "mac", cwd: "/w", deps: deps)
        await model.read(s, machine: "mac", cwd: "/w", deps: deps)
        #expect(reads == 1 && model.answer?.pr?.number == 128)
        await model.read(try Self.status(head: "h2"), machine: "mac", cwd: "/w", deps: deps)
        #expect(reads == 2)
        await model.read(try Self.status(head: "h2"), machine: "mac", cwd: "/w", deps: deps, force: true)
        #expect(reads == 3)
        // a detached HEAD reads nothing and shows nothing
        await model.read(try Self.status(branch: nil), machine: "mac", cwd: "/w", deps: deps)
        #expect(reads == 3 && model.answer == nil)
        let pushed = try Self.status(), ahead = try Self.status(ahead: 1), bare = try Self.status(upstream: nil), gone = try Self.status(gone: true)
        #expect(GitPRModel.pushedAll(pushed))
        #expect(!GitPRModel.pushedAll(ahead) && !GitPRModel.pushedAll(bare) && !GitPRModel.pushedAll(gone))
    }

    @Test func aReadRefusalKeepsItsCodeAndTheQuietOnesShowNothing() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines/mac/git/pr") { _ in throw APIError(status: 409, message: "gh is not installed", fields: ["code": "no_gh"]) }
        let model = GitPRModel()
        await model.read(try Self.status(), machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        #expect(model.failed == GitPRFailure(code: "no_gh", detail: "gh is not installed") && model.failed?.quiet == false)
        #expect(GitPRFailure(code: "git_not_github", detail: "").quiet && GitPRFailure(code: "git_detached", detail: "").quiet)
        #expect(hub.sent("GET", "/api/v1/machines/mac/git/pr").first?.query["cwd"] == "/w")
    }

    @Test func opensAndMergesWithTheWebsBodies() async throws {
        let hub = FakeHub()
        var bodies: [[String: Any]] = []
        let answer = Data(#"{"branch":"widget-hourly","pr":\#(Self.readyPR)}"#.utf8)
        hub.on("POST", "/api/v1/machines/mac/git/pr") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return answer
        }
        hub.on("POST", "/api/v1/machines/mac/git/pr/merge") { req in
            bodies.append(try JSONSerialization.jsonObject(with: req.body!) as! [String: Any])
            return answer
        }
        let model = GitPRModel(), deps = GitPRModel.Deps(api: hub.client)
        let opened = await model.create(title: "  Hourly forecast ", body: "Why", base: " ", draft: true, machine: "mac", cwd: "/w", deps: deps)
        _ = await model.create(title: "x", body: "", base: "release", draft: false, machine: "mac", cwd: "/w", deps: deps)
        let blank = await model.create(title: "  ", body: "", base: nil, draft: false, machine: "mac", cwd: "/w", deps: deps)
        let p = try Self.pr()
        _ = await model.merge(p, .now(.squash), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.merge(p, .whenReady(.rebase), machine: "mac", cwd: "/w", deps: deps)
        _ = await model.merge(p, .notByItself, machine: "mac", cwd: "/w", deps: deps)
        #expect(opened?.pr?.number == 128 && blank == nil)
        #expect(bodies.count == 5)
        #expect(bodies[0]["title"] as? String == "Hourly forecast" && bodies[0]["body"] as? String == "Why" && bodies[0]["draft"] as? Bool == true)
        #expect(bodies[0]["base"] == nil, "a blank base: the default branch")
        #expect(bodies[1]["base"] as? String == "release")
        #expect(bodies[2]["number"] as? Int == 128 && bodies[2]["method"] as? String == "squash" && bodies[2]["auto"] == nil && bodies[2]["auto_off"] == nil)
        #expect(bodies[3]["method"] as? String == "rebase" && bodies[3]["auto"] as? Bool == true)
        #expect(bodies[4]["auto_off"] as? Bool == true && bodies[4]["method"] == nil)
    }

    @Test func aRefusedMergeIsSaid() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/machines/mac/git/pr/merge") { _ in
            throw APIError(status: 409, message: "gh pr merge: not mergeable", fields: ["code": "git_failed"])
        }
        let model = GitPRModel()
        let r = await model.merge(try Self.pr(), .now(.merge), machine: "mac", cwd: "/w", deps: .init(api: hub.client))
        #expect(r == nil && model.busy == nil)
        #expect(model.refusal?.detail == "gh pr merge: not mergeable")
        #expect(GitRefusal.title("not_signed_in")?.contains("gh auth login") == true)
    }
}
