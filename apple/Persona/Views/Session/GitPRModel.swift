import Foundation
import Observation

/// Whether a pull request can merge now (as gh says), only once its checks and reviews allow, not before a conflict is
/// resolved, or not at all (closed, merged, a draft). The web's `mergeReady` (`web/src/lib/git-pr.ts`).
nonisolated enum GitMergeReady: Equatable, Sendable {
    case now, later, conflict, no

    init(_ pr: GitPR) {
        if pr.state != "OPEN" || pr.draft == true { self = .no } else if pr.mergeable == "CONFLICTING" || pr.mergeState == "DIRTY" {
            self = .conflict
        } else if ["CLEAN", "UNSTABLE", "HAS_HOOKS"].contains(pr.mergeState ?? "") { self = .now } else { self = .later }
    }
}

/// How it merges (Squash by default, as on the web).
nonisolated enum GitMergeMethod: String, CaseIterable, Identifiable, Sendable {
    case squash, merge, rebase
    var id: String { rawValue }

    var title: String {
        switch self {
        case .squash: String(localized: "Squash")
        case .merge: String(localized: "Merge Commit")
        case .rebase: String(localized: "Rebase")
        }
    }
}

/// The pull request's words for the Git tab (#7014), as the web's `git-pr.ts` says them.
nonisolated enum GitPRText {
    /// How many checks are in each state.
    struct Counts: Equatable {
        var pass = 0, fail = 0, pending = 0, skipped = 0
    }

    static func counts(_ checks: [GitCheck]) -> Counts {
        var n = Counts()
        for c in checks {
            switch c.state {
            case "pass": n.pass += 1
            case "fail": n.fail += 1
            case "pending": n.pending += 1
            default: n.skipped += 1
            }
        }
        return n
    }

    /// `src/a.ts:42`, or the path alone for a thread on the whole file.
    static func place(_ th: GitPRThread) -> String {
        th.line.map { "\(th.path):\($0)" } ?? th.path
    }

    /// A review thread as a comment for the agent, the block the web writes: the reviewers' words quoted (each
    /// `@author: …`), and a note in the user's words saying which pull request and where, editable before sending.
    static func comment(_ pr: GitPR, _ th: GitPRThread) -> MessageComment {
        let words = th.comments.map { "@\($0.author): \($0.body.trimmingCharacters(in: .whitespacesAndNewlines))" }.joined(separator: "\n\n")
        let place = place(th)
        let note = th.outdated == true
            ? String(localized: "A review comment on pull request #\(pr.number) at \(place) (that code has changed since): please check whether it still applies and address it.")
            : String(localized: "A review comment on pull request #\(pr.number) at \(place): please address it.")
        return MessageComment(quoted: MessageComments.clamp(words), note: note)
    }

    /// The state in a word: Open, Draft, Merged, Closed.
    static func state(_ pr: GitPR) -> String {
        switch pr.state {
        case "MERGED": String(localized: "Merged")
        case "CLOSED": String(localized: "Closed")
        default: pr.draft == true ? String(localized: "Draft") : String(localized: "git.pr.state.open", defaultValue: "Open")
        }
    }

    /// gh's review decision in words; one it adds later as it is.
    static func review(_ review: String) -> String {
        switch review {
        case "APPROVED": String(localized: "Approved")
        case "CHANGES_REQUESTED": String(localized: "Changes requested")
        case "REVIEW_REQUIRED": String(localized: "Review required")
        default: review
        }
    }
}

/// Why the pull request couldn't be read: nothing to show for no GitHub remote or a detached HEAD, a line for gh missing
/// or not signed in, another for anything else.
nonisolated struct GitPRFailure: Equatable, Sendable {
    var code: String
    var detail: String

    /// No section at all: not worth a card (as the web's `QUIET`).
    var quiet: Bool { ["git_not_github", "git_detached", "git_not_repo"].contains(code) }
}

/// The current branch's pull request in the Git tab (#7014, the web's `useGitPR`): read when the tab opens and again only
/// when what was pushed moves (the branch, its upstream, what is left to push, HEAD), since gh asks GitHub each time;
/// opening one and merging it (now, when ready, or not by itself any more). One write at a time.
@MainActor @Observable
final class GitPRModel {
    struct Deps {
        var read: (_ machine: String, _ cwd: String) async throws -> GitPRAnswer
        var create: (_ machine: String, _ cwd: String, _ title: String, _ body: String, _ base: String?, _ draft: Bool) async throws -> GitPRAnswer
        var merge: (_ machine: String, _ cwd: String, _ number: Int, _ how: How) async throws -> GitPRAnswer

        init(api: APIClient) {
            read = { try await api.gitPR(machine: $0, cwd: $1) }
            create = { try await api.gitPRCreate(machine: $0, cwd: $1, title: $2, body: $3, base: $4, draft: $5) }
            merge = { machine, cwd, number, how in
                switch how {
                case .now(let m): try await api.gitPRMerge(machine: machine, cwd: cwd, number: number, method: m.rawValue, auto: false, autoOff: false)
                case .whenReady(let m): try await api.gitPRMerge(machine: machine, cwd: cwd, number: number, method: m.rawValue, auto: true, autoOff: false)
                case .notByItself: try await api.gitPRMerge(machine: machine, cwd: cwd, number: number, method: nil, auto: false, autoOff: true)
                }
            }
        }

        init(read: @escaping (String, String) async throws -> GitPRAnswer,
             create: @escaping (String, String, String, String, String?, Bool) async throws -> GitPRAnswer,
             merge: @escaping (String, String, Int, How) async throws -> GitPRAnswer) {
            self.read = read
            self.create = create
            self.merge = merge
        }
    }

    /// Merge now, set it to merge when ready, or take that back.
    enum How: Hashable, Sendable {
        case now(GitMergeMethod), whenReady(GitMergeMethod), notByItself
    }

    enum Work: Hashable { case create, merge(How) }

    private(set) var answer: GitPRAnswer?
    private(set) var failed: GitPRFailure?
    private(set) var loading = false
    private(set) var busy: Work?
    var refusal: GitRefusal?
    private var readKey = ""

    /// What it is read again on, as the web's: the branch, its upstream, gone or not, what is left to push, HEAD; empty
    /// (nothing to read) outside a repository or on a detached HEAD.
    static func key(_ s: GitStatus?) -> String {
        guard let s, s.isRepo, let branch = s.branch else { return "" }
        return "\(branch)|\(s.upstreamName ?? "")|\(s.gone == true ? "gone" : "")|\(s.ahead)|\(s.head?.hash ?? "")"
    }

    /// The branch can have a pull request opened: on a branch, pushed (and still there), nothing left to push.
    static func pushedAll(_ s: GitStatus) -> Bool {
        s.branch != nil && s.upstreamName != nil && s.gone != true && s.ahead == 0
    }

    /// Reads when the key moved since the last read, or always with force (pull to refresh, Retry).
    func read(_ s: GitStatus?, machine: String, cwd: String, deps: Deps, force: Bool = false) async {
        let k = Self.key(s)
        guard !k.isEmpty else {
            answer = nil
            failed = nil
            readKey = ""
            return
        }
        guard force || k != readKey else { return }
        readKey = k
        loading = true
        defer { loading = false }
        do {
            let a = try await deps.read(machine, cwd)
            guard readKey == k else { return }
            answer = a
            failed = nil
        } catch {
            guard !error.isCancellation else {
                readKey = ""
                return
            }
            guard readKey == k else { return }
            failed = GitPRFailure(code: (error as? APIError)?.code ?? "", detail: GitRefusal(error).detail)
        }
    }

    /// Opens a pull request (the user said yes: it is public).
    func create(title: String, body: String, base: String?, draft: Bool, machine: String, cwd: String, deps: Deps) async -> GitPRAnswer? {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return nil }
        let b = base?.trimmingCharacters(in: .whitespacesAndNewlines)
        return await run(.create) { try await deps.create(machine, cwd, t, body, b?.isEmpty == false ? b : nil, draft) }
    }

    /// Merges, sets merge-when-ready or takes it back (the user said yes).
    func merge(_ pr: GitPR, _ how: How, machine: String, cwd: String, deps: Deps) async -> GitPRAnswer? {
        await run(.merge(how)) { try await deps.merge(machine, cwd, pr.number, how) }
    }

    private func run(_ work: Work, _ call: () async throws -> GitPRAnswer) async -> GitPRAnswer? {
        guard busy == nil else { return nil }
        busy = work
        defer { busy = nil }
        do {
            let a = try await call()
            answer = a
            failed = nil
            return a
        } catch {
            if !error.isCancellation { refusal = GitRefusal(error) }
            return nil
        }
    }
}
