import Foundation

// Git / Files models and calls for a session's project (hub → node `git.*` / `fs.list`).

nonisolated struct GitCommit: Codable, Identifiable, Hashable, Sendable {
    var hash: String
    var short: String
    var subject: String
    var body: String?
    var author: String
    var date: Date?
    var refs: String?
    var id: String { hash }

    /// Branch / tag names pointing here ("HEAD -> master, origin/master" → ["master", "origin/master"]).
    var refNames: [String] {
        (refs ?? "").split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
            .map { $0.hasPrefix("HEAD -> ") ? String($0.dropFirst(8)) : $0 }
            .filter { !$0.isEmpty && $0 != "HEAD" }
    }
}

nonisolated struct GitFile: Codable, Identifiable, Hashable, Sendable {
    var path: String
    var origPath: String?
    var index: String
    var worktree: String
    var added: Int
    var deleted: Int
    var binary: Bool?
    var id: String { path }

    var isUntracked: Bool { index == "?" }
    var isConflicted: Bool { index == "U" || worktree == "U" }
    var isStaged: Bool { !isUntracked && index != "." }
    var hasUnstaged: Bool { !isUntracked && worktree != "." }
    /// The one letter shown in the change list: staged change wins, then the working-tree change.
    var letter: String {
        if isUntracked { return "U" }
        if isConflicted { return "!" }
        return index != "." ? index : worktree
    }
    var name: String { (path as NSString).lastPathComponent }
    var folder: String { (path as NSString).deletingLastPathComponent }

    enum CodingKeys: String, CodingKey {
        case path, index, worktree, added, deleted, binary
        case origPath = "orig_path"
    }
}

nonisolated struct GitStatus: Codable, Hashable, Sendable {
    var isRepo: Bool
    var root: String?
    var branch: String?
    var head: GitCommit?
    var upstream: String?
    var ahead: Int
    var behind: Int
    var files: [GitFile]
    var stashes: Int
    var commits: [GitCommit]
    /// The upstream was deleted on the remote (a merged pull request's branch): ahead and behind are 0 and mean
    /// nothing. Older nodes leave it out.
    var gone: Bool?

    enum CodingKeys: String, CodingKey {
        case root, branch, head, upstream, ahead, behind, files, stashes, commits, gone
        case isRepo = "is_repo"
    }

    /// The upstream branch, nil when there is none (a node leaves it out; the mock hub sends it empty).
    var upstreamName: String? { upstream?.isEmpty == false ? upstream : nil }
}

/// What a write of the Git tab answers (#7013, docs/protocol/protocol.md `git/stage` · `git/commit` · `git/push`):
/// the repository as it is after it, and the commit a commit made.
nonisolated struct GitWriteResult: Decodable, Sendable {
    var status: GitStatus
    var commit: GitCommit?
    /// What git said (a push's summary).
    var output: String?
    /// The stash list after a stash write (#7015).
    var stashes: [GitStashEntry]?
}

/// A branch to switch to (#7015, `git/branches`): local ones, then remote ones without a local branch of the same name.
nonisolated struct GitBranch: Decodable, Identifiable, Hashable, Sendable {
    var name: String
    var remote: Bool?
    var current: Bool?
    var upstream: String?
    var hash: String
    var subject: String
    var date: Date?
    var id: String { (remote == true ? "r:" : "l:") + name }

    /// What a switch asks for: a remote branch's name without its remote ("origin/release" → "release"); the node
    /// makes a local branch tracking it.
    var switchName: String {
        guard remote == true, let slash = name.firstIndex(of: "/") else { return name }
        return String(name[name.index(after: slash)...])
    }
}

/// A check on the branch's pull request (#7014): pass, fail, pending or skipped, and its run's page.
nonisolated struct GitCheck: Decodable, Hashable, Identifiable, Sendable {
    var name: String
    var state: String
    var url: String?
    var id: String { name + "\u{0}" + (url ?? "") }
}

/// One comment of an open review thread.
nonisolated struct GitPRComment: Decodable, Hashable, Identifiable, Sendable {
    var author: String
    var body: String
    var url: String?
    var at: Date?
    var id: String { url ?? "\(author)\u{0}\(at?.timeIntervalSince1970 ?? 0)\u{0}\(body.prefix(40))" }
}

/// An open (unresolved) review thread: where it is and its comments.
nonisolated struct GitPRThread: Decodable, Hashable, Identifiable, Sendable {
    var path: String
    var line: Int?
    var outdated: Bool?
    var comments: [GitPRComment]
    /// Where it is and its first comment: stable across reads while the thread is open.
    var id: String { "\(path):\(line ?? 0)\u{0}" + (comments.first?.id ?? "") }

    enum CodingKeys: String, CodingKey { case path, line, outdated, comments }

    init(path: String, line: Int? = nil, outdated: Bool? = nil, comments: [GitPRComment]) {
        self.path = path
        self.line = line
        self.outdated = outdated
        self.comments = comments
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        line = try c.decodeIfPresent(Int.self, forKey: .line)
        outdated = try c.decodeIfPresent(Bool.self, forKey: .outdated)
        comments = try c.decodeIfPresent([GitPRComment].self, forKey: .comments) ?? []
    }
}

/// The current branch's pull request (#7014, `git/pr`, through gh on that computer): OPEN, CLOSED or MERGED, whether it
/// can merge (gh's mergeable and merge state), the review decision, merge-when-ready, checks and open review threads.
nonisolated struct GitPR: Decodable, Hashable, Sendable {
    var number: Int
    var url: String
    var title: String
    var state: String
    var draft: Bool?
    var base: String
    var head: String
    var mergeable: String?
    var mergeState: String?
    var review: String?
    var autoMerge: Bool?
    var checks: [GitCheck]
    var checksState: String?
    var threads: [GitPRThread]

    enum CodingKeys: String, CodingKey {
        case number, url, title, state, draft, base, head, mergeable, review, checks, threads
        case mergeState = "merge_state", autoMerge = "auto_merge", checksState = "checks_state"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        number = try c.decode(Int.self, forKey: .number)
        url = try c.decodeIfPresent(String.self, forKey: .url) ?? ""
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        state = try c.decodeIfPresent(String.self, forKey: .state) ?? "OPEN"
        draft = try c.decodeIfPresent(Bool.self, forKey: .draft)
        base = try c.decodeIfPresent(String.self, forKey: .base) ?? ""
        head = try c.decodeIfPresent(String.self, forKey: .head) ?? ""
        mergeable = try c.decodeIfPresent(String.self, forKey: .mergeable)
        mergeState = try c.decodeIfPresent(String.self, forKey: .mergeState)
        review = try c.decodeIfPresent(String.self, forKey: .review)
        autoMerge = try c.decodeIfPresent(Bool.self, forKey: .autoMerge)
        checks = try c.decodeIfPresent([GitCheck].self, forKey: .checks) ?? []
        checksState = try c.decodeIfPresent(String.self, forKey: .checksState)
        threads = try c.decodeIfPresent([GitPRThread].self, forKey: .threads) ?? []
    }
}

/// What every pull request call answers: the branch and its pull request (nil: it has none).
nonisolated struct GitPRAnswer: Decodable, Sendable {
    var branch: String
    var pr: GitPR?
}

/// What `git/branches` answers: the branches, and the remote's default branch by its name (`origin/HEAD`), nil when git
/// doesn't know it or the node is older (#7014).
nonisolated struct GitBranchList: Decodable, Sendable {
    var branches: [GitBranch]
    var defaultBranch: String?

    enum CodingKeys: String, CodingKey { case branches, defaultBranch = "default" }
}

/// One stash (#7015). `oid` names it whatever the stack does since: a node without it gets no writes.
nonisolated struct GitStashEntry: Decodable, Identifiable, Hashable, Sendable {
    var index: Int
    var oid: String?
    var message: String
    var date: Date?
    var id: String { (oid ?? "legacy") + ":\(index)" }

    /// The node said which stash this is (40 or 64 hex digits), so apply, pop and drop can't reach another one.
    var hasIdentity: Bool {
        guard let oid, oid.count == 40 || oid.count == 64 else { return false }
        return oid.allSatisfy { $0.isHexDigit && !$0.isUppercase }
    }
}

nonisolated struct GitPullResult: Codable, Hashable, Sendable {
    var status: GitStatus
    var updated: Bool
}

nonisolated struct GitDiff: Codable, Hashable, Sendable {
    var path: String
    var diff: String
    var truncated: Bool
}

nonisolated struct GitShow: Codable, Hashable, Sendable {
    var commit: GitCommit
    var files: [GitFile]
    var diff: String
    var truncated: Bool
}

nonisolated struct FileEntry: Codable, Identifiable, Hashable, Sendable {
    var name: String
    var dir: Bool
    var symlink: Bool?
    var size: Int64
    var mtime: Date?
    /// A symlink's text and whether it leads nowhere (hub feature `file_info`, docs/protocol/protocol.md "File details and links").
    var linkTarget: String?
    var linkBroken: Bool?
    var id: String { name }

    enum CodingKeys: String, CodingKey {
        case name, dir, symlink, size, mtime
        case linkTarget = "link_target", linkBroken = "link_broken"
    }
}

/// One path's details for the file page's Info sheet (`GET …/stat?detail=1`, hub feature `file_info`). An older node
/// answers only `path`, `kind` and `size`.
nonisolated struct FileInfo: Codable, Hashable, Sendable {
    var path: String
    /// file, dir, link (a broken symlink), denied or missing.
    var kind: String
    var size: Int64?
    /// Permission bits as chmod takes them.
    var mode: Int?
    var owner: String?
    var group: String?
    var mtime: Date?
    var ctime: Date?
    var birthtime: Date?
    var items: Int?
    var itemsMore: Bool?
    var symlink: Bool?
    var linkTarget: String?
    var linkReal: String?
    var linkBroken: Bool?

    enum CodingKeys: String, CodingKey {
        case path, kind, size, mode, owner, group, mtime, ctime, birthtime, items, symlink
        case itemsMore = "items_more", linkTarget = "link_target", linkReal = "link_real", linkBroken = "link_broken"
    }

    /// The node answered with details (an older one sends only kind and size).
    var detailed: Bool { mode != nil || mtime != nil }
}

/// Every file under a folder (`GET …/fs/find`): paths relative to it, with `/`.
nonisolated struct FileFindResult: Codable, Hashable, Sendable {
    var cwd: String?
    var files: [String]?
    var truncated: Bool?
}

nonisolated struct FileListing: Codable, Hashable, Sendable {
    var path: String
    var entries: [FileEntry]
    var truncated: Bool
}

extension APIClient {
    private func mp(_ machine: String) -> String { "/api/v1/machines/\(machine)" }

    func gitStatus(machine: String, cwd: String) async throws -> GitStatus {
        try await get(mp(machine) + "/git/status", query: ["cwd": cwd])
    }
    private struct GitPullRequest: Encodable { var cwd: String }
    func gitPull(machine: String, cwd: String) async throws -> GitPullResult {
        try await post(mp(machine) + "/git/pull", body: GitPullRequest(cwd: cwd), timeout: 100)
    }
    // The Git tab's writes (#7013, hub feature `git_write`): each a tap the app confirmed when it leaves the computer;
    // the node never forces. A refusal is a 409 with `code` (and `files`), as the web's Git panel reads it.
    private struct GitStageRequest: Encodable { var cwd: String; var paths: [String]; var unstage: Bool }
    /// `git add` (or `git restore --staged` with unstage) of paths.
    func gitStage(machine: String, cwd: String, paths: [String], unstage: Bool) async throws -> GitWriteResult {
        try await post(mp(machine) + "/git/stage", body: GitStageRequest(cwd: cwd, paths: paths, unstage: unstage), timeout: 100)
    }
    private struct GitCommitRequest: Encodable { var cwd, message: String }
    /// Commits what is staged; hooks run, and the hub waits up to five minutes for them.
    func gitCommit(machine: String, cwd: String, message: String) async throws -> GitWriteResult {
        try await post(mp(machine) + "/git/commit", body: GitCommitRequest(cwd: cwd, message: message), timeout: 330)
    }
    private struct GitPushRequest: Encodable {
        var cwd: String
        var setUpstream: Bool
        enum CodingKeys: String, CodingKey { case cwd, setUpstream = "set_upstream" }
    }
    /// Pushes the branch to its upstream, never forced; setUpstream makes one on origin for a branch without one.
    func gitPush(machine: String, cwd: String, setUpstream: Bool) async throws -> GitWriteResult {
        try await post(mp(machine) + "/git/push", body: GitPushRequest(cwd: cwd, setUpstream: setUpstream), timeout: 100)
    }
    /// The branches to switch to (#7015): local ones, then remote ones without a local one, newest first; and the remote's
    /// default branch when git knows it (#7014).
    func gitBranches(machine: String, cwd: String) async throws -> GitBranchList {
        try await get(mp(machine) + "/git/branches", query: ["cwd": cwd], as: GitBranchList.self)
    }
    private struct GitSwitchRequest: Encodable { var cwd, branch: String; var create: Bool }
    /// Switches to branch, or makes it here with create. Uncommitted files the branch has differently are refused
    /// (`git_conflict` with the files), never overwritten; the other uncommitted changes go along.
    func gitSwitch(machine: String, cwd: String, branch: String, create: Bool) async throws -> GitWriteResult {
        try await post(mp(machine) + "/git/switch", body: GitSwitchRequest(cwd: cwd, branch: branch, create: create), timeout: 100)
    }
    private struct GitStashRequest: Encodable {
        var cwd, op: String
        var index: Int?
        var oid, message: String?
        var untracked: Bool?
    }
    /// A stash write (op push, list, apply, pop or drop): apply, pop and drop name the stash by index and oid, so a
    /// stack that moved is refused (`git_stash_stale`).
    private struct GitPRCreateRequest: Encodable {
        var cwd, title, body: String
        var base: String?
        var draft: Bool
    }
    /// The current branch's pull request, read through gh on that computer as it is signed in (#7014).
    func gitPR(machine: String, cwd: String) async throws -> GitPRAnswer {
        try await get(mp(machine) + "/git/pr", query: ["cwd": cwd], as: GitPRAnswer.self)
    }
    /// Opens a pull request for the branch (pushed, nothing left to push); base nil: the default branch.
    func gitPRCreate(machine: String, cwd: String, title: String, body: String, base: String?, draft: Bool) async throws -> GitPRAnswer {
        try await post(mp(machine) + "/git/pr", body: GitPRCreateRequest(cwd: cwd, title: title, body: body, base: base, draft: draft),
                       timeout: 100, as: GitPRAnswer.self)
    }
    private struct GitPRMergeRequest: Encodable {
        var cwd: String
        var number: Int
        var method: String?
        var auto: Bool?
        var autoOff: Bool?
        enum CodingKeys: String, CodingKey { case cwd, number, method, auto, autoOff = "auto_off" }
    }
    /// Merges it now (method), sets it to merge once checks and reviews allow (auto), or takes that back (autoOff). The
    /// branch is never deleted.
    func gitPRMerge(machine: String, cwd: String, number: Int, method: String?, auto: Bool, autoOff: Bool) async throws -> GitPRAnswer {
        try await post(mp(machine) + "/git/pr/merge",
                       body: GitPRMergeRequest(cwd: cwd, number: number, method: method, auto: auto ? true : nil, autoOff: autoOff ? true : nil),
                       timeout: 100, as: GitPRAnswer.self)
    }
    func gitStash(machine: String, cwd: String, op: String, entry: GitStashEntry? = nil, message: String? = nil,
                  untracked: Bool? = nil) async throws -> GitWriteResult {
        try await post(mp(machine) + "/git/stash", body: GitStashRequest(cwd: cwd, op: op, index: entry?.index, oid: entry?.oid,
                                                                       message: message, untracked: untracked), timeout: 100)
    }
    func gitDiff(machine: String, cwd: String, path: String, staged: Bool) async throws -> GitDiff {
        try await get(mp(machine) + "/git/diff", query: ["cwd": cwd, "path": path, "staged": staged ? "1" : "0"])
    }
    func gitShow(machine: String, cwd: String, hash: String) async throws -> GitShow {
        try await get(mp(machine) + "/git/show", query: ["cwd": cwd, "hash": hash])
    }
    private struct CommitsResponse: Decodable { var commits: [GitCommit] }
    func gitLog(machine: String, cwd: String, skip: Int, limit: Int = 50) async throws -> [GitCommit] {
        try await get(mp(machine) + "/git/log", query: ["cwd": cwd, "skip": String(skip), "limit": String(limit)],
                      as: CommitsResponse.self).commits
    }
    func listDir(machine: String, path: String, cwd: String) async throws -> FileListing {
        try await get(mp(machine) + "/fs", query: ["path": path, "cwd": cwd])
    }
    private struct InfoResponse: Decodable { var stats: [FileInfo] }
    /// One path's details for the Info sheet (`stat?detail=1`).
    func fileInfo(machine: String, path: String, cwd: String) async throws -> FileInfo? {
        try await get(mp(machine) + "/stat", query: ["path": path, "cwd": cwd, "detail": "1"], as: InfoResponse.self).stats.first
    }
    /// Every file under dir (`fs/find`; Git's files in a repository), for finding by name.
    func findFiles(machine: String, dir: String) async throws -> FileFindResult {
        try await getDecodedOffMain(mp(machine) + "/fs/find", query: ["cwd": dir], as: FileFindResult.self)
    }

    // Rename, move, trash and put back (docs/protocol/protocol.md "Changing files"); paths as in listDir.
    private struct RenameRequest: Encodable { var path, cwd, name: String }
    private struct RenameResponse: Decodable { var path: String }
    /// Renames path in its folder; returns the new path.
    func renameFile(machine: String, path: String, cwd: String, name: String) async throws -> String {
        try await post(mp(machine) + "/files/rename", body: RenameRequest(path: path, cwd: cwd, name: name), as: RenameResponse.self).path
    }
    private struct MoveRequest: Encodable {
        var paths: [String]
        var cwd, dir: String
        var conflict: String?
        var operationID: String? = nil
        enum CodingKeys: String, CodingKey { case paths, cwd, dir, conflict, operationID = "operation_id" }
    }
    struct MovedFile: Decodable, Sendable { var from, to: String }
    private struct MoveResponse: Decodable { var moved: [MovedFile]? }
    /// Moves paths into folder dir; conflict "replace" / "keep" answers a 409 `exists`.
    func moveFiles(machine: String, paths: [String], cwd: String, dir: String, conflict: String? = nil) async throws -> [MovedFile] {
        try await post(mp(machine) + "/files/move", body: MoveRequest(paths: paths, cwd: cwd, dir: dir, conflict: conflict),
                       timeout: 130, as: MoveResponse.self).moved ?? []
    }
    private struct TrashRequest: Encodable { var paths: [String]; var cwd: String }
    struct TrashedFile: Decodable, Sendable {
        var path: String
        var trashID: String
        enum CodingKeys: String, CodingKey { case path, trashID = "trash_id" }
    }
    private struct TrashResponse: Decodable { var trashed: [TrashedFile]? }
    /// Moves paths to the machine's trash; restoreFiles with the ids puts them back.
    func trashFiles(machine: String, paths: [String], cwd: String) async throws -> [TrashedFile] {
        try await post(mp(machine) + "/files/trash", body: TrashRequest(paths: paths, cwd: cwd), timeout: 130, as: TrashResponse.self).trashed ?? []
    }
    // New folders, text files and zips (#5828, #5829; docs/protocol/protocol.md "Changing files").
    private struct MkdirRequest: Encodable { var dir, cwd, name: String }
    /// Makes folder name in dir; returns its path (absolute).
    func makeDir(machine: String, dir: String, cwd: String, name: String) async throws -> String {
        try await post(mp(machine) + "/files/mkdir", body: MkdirRequest(dir: dir, cwd: cwd, name: name), as: RenameResponse.self).path
    }
    private struct WriteRequest: Encodable {
        var path, cwd, text: String
        var create: Bool?
        var revision: String?
        var sha256: String?
        var force: Bool?
    }
    struct WrittenFile: Decodable, Sendable {
        var path: String
        var size: Int64
        var revision: String
        var mtime: Date?
    }
    /// Writes a text file: create makes a new one (409 `exists` when taken); otherwise it replaces the file while it is
    /// still at revision (or hashes to sha256), else 409 `file_changed`; force: Overwrite Anyway.
    func writeFile(machine: String, path: String, cwd: String, text: String, create: Bool = false, revision: String? = nil,
                   sha256: String? = nil, force: Bool = false) async throws -> WrittenFile {
        try await post(mp(machine) + "/files/write",
                       body: WriteRequest(path: path, cwd: cwd, text: text, create: create ? true : nil, revision: revision,
                                          sha256: sha256, force: force ? true : nil),
                       timeout: 60, as: WrittenFile.self)
    }
    private struct ZipRequest: Encodable {
        var paths: [String]
        var cwd: String
        var dir: String?
        var name: String
        var operationID: String? = nil
        enum CodingKeys: String, CodingKey { case paths, cwd, dir, name, operationID = "operation_id" }
    }
    struct ZippedFile: Decodable, Sendable {
        var path: String
        var name: String
        var size: Int64
        var files: Int
    }
    /// Zips paths into dir as name, or with no dir into a temporary file on the computer for a download.
    func zipFiles(machine: String, paths: [String], cwd: String, dir: String?, name: String, operationID: String? = nil) async throws -> ZippedFile {
        try await post(mp(machine) + "/files/zip", body: ZipRequest(paths: paths, cwd: cwd, dir: dir, name: name, operationID: operationID), timeout: 610, as: ZippedFile.self)
    }
    /// Copies paths into folder dir (#5830); conflict "replace" / "keep" answers a 409 `exists` ("keep" into their own
    /// folder: Duplicate).
    func copyFiles(machine: String, paths: [String], cwd: String, dir: String, conflict: String? = nil, operationID: String? = nil) async throws -> [MovedFile] {
        struct CopyResponse: Decodable { var copied: [MovedFile]? }
        return try await post(mp(machine) + "/files/copy", body: MoveRequest(paths: paths, cwd: cwd, dir: dir, conflict: conflict, operationID: operationID),
                              timeout: 610, as: CopyResponse.self).copied ?? []
    }

    nonisolated struct RemoteFileOperation: Decodable, Sendable {
        var id: String
        var kind: String
        var state: String
        var filesDone: Int
        var filesTotal: Int
        var bytesDone: Int64
        var bytesTotal: Int64
        enum CodingKeys: String, CodingKey {
            case id, kind, state
            case filesDone = "files_done"
            case filesTotal = "files_total"
            case bytesDone = "bytes_done"
            case bytesTotal = "bytes_total"
        }
    }

    /// A copy / zip still running on the computer. Its terminal state never replaces the original POST's answer.
    func fileOperation(machine: String, id: String) async throws -> RemoteFileOperation {
        try await getDecodedOffMain(mp(machine) + "/files/operations/\(id)", as: RemoteFileOperation.self)
    }
    private struct RestoreRequest: Encodable {
        var trashIDs: [String]
        enum CodingKeys: String, CodingKey { case trashIDs = "trash_ids" }
    }
    private struct RestoreResponse: Decodable { var restored: [RenameResponse]? }
    func restoreFiles(machine: String, trashIDs: [String]) async throws -> [String] {
        try await post(mp(machine) + "/files/restore", body: RestoreRequest(trashIDs: trashIDs), timeout: 130, as: RestoreResponse.self)
            .restored?.map(\.path) ?? []
    }
}
