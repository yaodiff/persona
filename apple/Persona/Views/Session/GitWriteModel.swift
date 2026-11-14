import Foundation
import Observation
import os

/// Whether all of a changed file is staged (its tick box is ticked), part of it, or none: as the web's `stageState`
/// (`web/src/lib/git-write.ts`). A new file Git doesn't track yet has nothing staged.
nonisolated enum GitStageMark: Equatable, Sendable {
    case none, some, all

    init(_ f: GitFile) {
        let inIndex = f.index != "." && f.index != "?"
        let inTree = f.worktree != "." && f.worktree != "?" ? true : f.index == "?"
        self = inIndex && !inTree ? .all : inIndex ? .some : .none
    }

    /// The tick box of every change at once (the commit bar's): all staged, none, or a mix.
    init(all files: [GitFile]) {
        let marks = files.map { GitStageMark($0) }
        self = !marks.isEmpty && marks.allSatisfy { $0 == .all } ? .all : marks.allSatisfy { $0 == .none } ? .none : .some
    }
}

/// A refused write said for the user (#7013): what happened in plain words (the web's wording, by the node's code),
/// the files it is about, and git's own words.
nonisolated struct GitRefusal: Identifiable, Equatable, Sendable {
    var id = UUID()
    var title: String
    var detail: String
    var files: [String] = []
    /// The node's code (`git_conflict` …), nil for one it didn't give.
    var code: String?

    init(title: String, detail: String, files: [String] = [], code: String? = nil) {
        self.title = title
        self.detail = detail
        self.files = files
        self.code = code
    }

    init(_ error: Error) {
        guard let e = error as? APIError else {
            self.init(title: String(localized: "It didn't go through"), detail: error.shown ?? error.localizedDescription)
            return
        }
        let files = e.details?["files"]?.array?.compactMap(\.string) ?? []
        if let title = Self.title(e.code) {
            self.init(title: title, detail: e.message, files: files, code: e.code)
        } else if e.message.range(of: "unknown method git.") != nil {
            // A node from before the web's Git writes does not know the method: Persona there needs updating.
            self.init(title: String(localized: "Persona on that computer is too old for this: update it"), detail: e.message)
        } else {
            self.init(title: String(localized: "It didn't go through"), detail: e.localizedDescription, files: files)
        }
    }

    /// The refusals the Git tab's writes answer with (docs/protocol/protocol.md, `git/stage` … `git/stash`).
    static func title(_ code: String?) -> String? {
        switch code {
        case "git_not_repo": String(localized: "Not a Git Repository")
        case "git_detached": String(localized: "HEAD is detached: switch to a branch first")
        case "git_nothing_staged": String(localized: "Nothing is staged: tick the files to commit")
        case "git_hook_failed": String(localized: "The commit was refused (a hook, usually pre-commit)")
        case "git_identity": String(localized: "Git on that computer has no name and email to commit with")
        case "git_no_upstream": String(localized: "This branch has no upstream yet")
        case "git_no_remote": String(localized: "No remote named origin to push to")
        case "git_push_rejected": String(localized: "The remote has newer commits: pull first, then push")
        case "git_auth": String(localized: "The remote refused the credentials on that computer")
        case "git_conflict": String(localized: "Uncommitted changes would be overwritten: commit or stash these first")
        case "git_busy": String(localized: "Another Git command is running there (the Agent's?): try again in a moment")
        case "git_no_branch": String(localized: "No such branch, or that name can't be used")
        case "git_no_stash": String(localized: "Nothing to stash, or that stash is gone")
        case "git_stash_stale": String(localized: "The stash list changed. Refresh and select the stash again.")
        case "git_stash_unsupported": String(localized: "Safe stash removal is unavailable for this repository.")
        case "git_stash_conflicted": String(localized: "Applied with conflicts in these files: resolve them, then commit. The stash is kept.")
        case "git_not_pushed": String(localized: "Push the branch first: a pull request needs every commit on GitHub")
        case "git_no_pr": String(localized: "This branch has no pull request")
        case "git_not_github": String(localized: "This repository has no GitHub remote")
        case "no_gh": String(localized: "GitHub CLI (gh) is not installed on that computer")
        case "not_signed_in": String(localized: "GitHub CLI (gh) is not signed in on that computer: run gh auth login there")
        default: nil
        }
    }

    /// git's words as the alert shows them: none when the files say it all (uncommitted files in a switch's way, #7015),
    /// git's words otherwise (a hook's output, a remote's refusal). They always go to the log.
    var shownDetail: String? {
        code == "git_conflict" && !files.isEmpty ? nil : detail
    }

    static func == (a: Self, b: Self) -> Bool { a.title == b.title && a.detail == b.detail && a.files == b.files && a.code == b.code }
}

/// What Push would do now (#7013): nothing (no button), push the commits to the upstream, make the upstream on origin
/// for a branch without one, or make it again after it was deleted on the remote. As the web's `PushButton`.
nonisolated enum GitPushKind: Equatable, Sendable {
    case commits(Int, upstream: String)
    case fresh
    case gone(upstream: String)

    init?(_ s: GitStatus) {
        guard s.branch != nil else { return nil }
        guard let up = s.upstreamName else { self = .fresh; return }
        if s.gone == true { self = .gone(upstream: up) } else if s.ahead > 0 { self = .commits(s.ahead, upstream: up) } else { return nil }
    }

    /// The push makes the upstream on origin (`set_upstream`).
    var setsUpstream: Bool { self == .fresh }
}

/// A stash write (#7015, `git/stash`): put the uncommitted changes away (new files too with untracked), read the list,
/// or apply (keep it), pop (remove it) or drop one.
nonisolated enum GitStashOp: Hashable, Sendable {
    case push(message: String, untracked: Bool)
    case list
    case apply(GitStashEntry)
    case pop(GitStashEntry)
    case drop(GitStashEntry)

    var name: String {
        switch self {
        case .push: "push"
        case .list: "list"
        case .apply: "apply"
        case .pop: "pop"
        case .drop: "drop"
        }
    }

    var entry: GitStashEntry? {
        switch self {
        case .apply(let e), .pop(let e), .drop(let e): e
        case .push, .list: nil
        }
    }

    /// What the changes are: sent only when written.
    var message: String? {
        guard case .push(let m, _) = self else { return nil }
        let t = m.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }

    var untracked: Bool? {
        guard case .push(_, let u) = self else { return nil }
        return u
    }
}

/// The Git tab's writes (#7013, hub feature `git_write`): stage or unstage a file or all of them, commit what is
/// staged, push; switch or make a branch, stash (#7015). One at a time; each answers the repository as it is after it, and a refusal is kept until it is read.
@MainActor @Observable
final class GitWriteModel {
    struct Deps {
        var stage: (_ machine: String, _ cwd: String, _ paths: [String], _ unstage: Bool) async throws -> GitWriteResult
        var commit: (_ machine: String, _ cwd: String, _ message: String) async throws -> GitWriteResult
        var push: (_ machine: String, _ cwd: String, _ setUpstream: Bool) async throws -> GitWriteResult
        var branches: (_ machine: String, _ cwd: String) async throws -> GitBranchList
        var switchBranch: (_ machine: String, _ cwd: String, _ branch: String, _ create: Bool) async throws -> GitWriteResult
        var stash: (_ machine: String, _ cwd: String, _ op: GitStashOp) async throws -> GitWriteResult

        init(api: APIClient) {
            stage = { try await api.gitStage(machine: $0, cwd: $1, paths: $2, unstage: $3) }
            commit = { try await api.gitCommit(machine: $0, cwd: $1, message: $2) }
            push = { try await api.gitPush(machine: $0, cwd: $1, setUpstream: $2) }
            branches = { try await api.gitBranches(machine: $0, cwd: $1) }
            switchBranch = { try await api.gitSwitch(machine: $0, cwd: $1, branch: $2, create: $3) }
            stash = { machine, cwd, op in
                try await api.gitStash(machine: machine, cwd: cwd, op: op.name, entry: op.entry, message: op.message, untracked: op.untracked)
            }
        }

        init(stage: @escaping (String, String, [String], Bool) async throws -> GitWriteResult,
             commit: @escaping (String, String, String) async throws -> GitWriteResult,
             push: @escaping (String, String, Bool) async throws -> GitWriteResult,
             branches: @escaping (String, String) async throws -> GitBranchList = { _, _ in GitBranchList(branches: []) },
             switchBranch: @escaping (String, String, String, Bool) async throws -> GitWriteResult = { _, _, _, _ in throw CancellationError() },
             stash: @escaping (String, String, GitStashOp) async throws -> GitWriteResult = { _, _, _ in throw CancellationError() }) {
            self.stage = stage
            self.commit = commit
            self.push = push
            self.branches = branches
            self.switchBranch = switchBranch
            self.stash = stash
        }
    }

    /// What is running: a file's tick (its path), every change's, the commit, the push, a switch to a branch, a stash
    /// write.
    enum Work: Hashable {
        case stage(String), stageAll, commit, push, switchTo(String), stash(GitStashOp)
    }

    private static let log = Logger(subsystem: "persona", category: "git-write")

    private(set) var busy: Work?
    /// The last refusal, until the user reads it.
    var refusal: GitRefusal?

    /// Ticks or unticks one file: unticks one that is all staged, stages the rest of one that is partly.
    func toggle(_ f: GitFile, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        await run(.stage(f.path)) { try await deps.stage(machine, cwd, [f.path], GitStageMark(f) == .all) }
    }

    /// Stages every change, or unstages them all when every one is staged already.
    func toggleAll(_ s: GitStatus, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        guard !s.files.isEmpty else { return nil }
        return await run(.stageAll) { try await deps.stage(machine, cwd, s.files.map(\.path), GitStageMark(all: s.files) == .all) }
    }

    /// Commits what is staged with message (never `-a`; hooks run).
    func commit(message: String, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return nil }
        return await run(.commit) { try await deps.commit(machine, cwd, message) }
    }

    /// Pushes once the user said yes; never forced.
    func push(_ kind: GitPushKind, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        await run(.push) { try await deps.push(machine, cwd, kind.setsUpstream) }
    }

    /// Switches to branch (a remote one by its name without the remote), or makes it from here with create.
    func switchTo(_ branch: String, create: Bool, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        await run(.switchTo(branch)) { try await deps.switchBranch(machine, cwd, branch, create) }
    }

    /// A stash write; apply, pop and drop only for a stash the node named by its oid.
    func stash(_ op: GitStashOp, machine: String, cwd: String, deps: Deps) async -> GitWriteResult? {
        if let e = op.entry, !e.hasIdentity { return nil }
        return await run(.stash(op)) { try await deps.stash(machine, cwd, op) }
    }

    private func run(_ work: Work, _ call: () async throws -> GitWriteResult) async -> GitWriteResult? {
        guard busy == nil else { return nil }
        busy = work
        defer { busy = nil }
        do {
            return try await call()
        } catch {
            // A cancelled write (the page went away) says nothing; the status is read again when it comes back.
            if !error.isCancellation {
                let r = GitRefusal(error)
                Self.log.notice("git write refused: \(r.code ?? "-", privacy: .public) \(r.detail)")
                refusal = r
            }
            return nil
        }
    }
}
