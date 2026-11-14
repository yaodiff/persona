import PhotosUI
import SwiftUI
import UniformTypeIdentifiers

// Files in and out of a folder the file browser shows (docs/protocol/protocol.md "Changing files", #4858): uploads keep their names in
// that folder (only folders in the machine's home take them), one after another with progress; a taken name asks
// Replace / Keep Both before anything is sent. A download lands in a temporary folder of its own and goes on to Save to Files.

@MainActor @Observable
final class FolderTransfers {
    static let limit: Int64 = 2 << 30

    struct Item: Identifiable, Equatable {
        enum Kind { case upload, download, copy, compress }
        let id = UUID()
        var kind: Kind
        var name: String
        var size: Int64
        var done: Int64 = 0
        var waiting = true // behind another transfer
        var asking = false // the name is taken: waiting for Replace / Keep Both
        var zipping = false // several items (or a folder) zipped on the computer before the download (#5828)
        var filesDone = 0
        var filesTotal = 0
        var session: SessionKey? // machine page: nil; Chat: the session that owns this item
        var showsProgress = true // remote copy / zip: one second plus a successful operation read
        var status = ""
    }

    /// Rows and their display text are derived only when an item changes, not in the view's body.
    private(set) var visibleItems: [Item] = []
    private func syncVisible() {
        visibleItems = items.filter { $0.showsProgress && $0.session == session }.map { item in
            var item = item
            item.status = Self.status(item)
            return item
        }
    }

    private static func status(_ item: Item) -> String {
        if item.asking { return String(localized: "Already exists, waiting for you") }
        if item.waiting { return String(localized: "Waiting [File Attach 26c8cf]") }
        if item.zipping { return String(localized: "Zipping on the computer…") }
        switch item.kind {
        case .copy, .compress:
            let files = item.filesTotal > 0 ? String(localized: "\(item.filesDone) / \(item.filesTotal) items · ") : ""
            let bytes = item.size > 0 ? "\(item.done.storageText) / \(item.size.storageText)" : ""
            if !files.isEmpty || !bytes.isEmpty { return files + bytes }
            return item.kind == .copy ? String(localized: "Copying on the computer…") : String(localized: "Compressing on the computer…")
        case .upload, .download:
            return "\(item.done.storageText) / \(item.size.storageText)"
        }
    }

    /// A name already in the folder, waiting for the user's answer. Each one has its own continuation: a remote copy
    /// and a serial upload can ask at once, and neither question replaces the other (#6113).
    struct Conflict: Identifiable {
        let id = UUID()
        var name: String
        /// The transfer that asked.
        var item: Item.ID
    }

    enum Choice { case replace, keep, cancel }

    private(set) var items: [Item] = []
    /// Chat can retain one model across sessions; only the current session's rows and polling are active. POSTs for a
    /// hidden session continue. The machine page has no session, so all its items use the same nil scope.
    var session: SessionKey? {
        didSet {
            guard oldValue != session else { return }
            let stale = pollTasks.keys.filter { id in
                guard let item = items.first(where: { $0.id == id }) else { return false }
                return item.session != session
            }
            for id in stale { pollTasks.removeValue(forKey: id)?.cancel() }
            syncVisible()
        }
    }
    /// Questions in the order they were asked; the alert shows the first, then the next.
    private var conflicts: [Conflict] = []
    /// The question the alert shows.
    var conflict: Conflict? { conflicts.first }
    @ObservationIgnored private var answers: [Conflict.ID: CheckedContinuation<Choice, Never>] = [:]
    /// A finished download, for Save to Files; set back to nil when the picker is done.
    var saving: URL?
    private struct RemotePoll {
        let machine: String
        let api: APIClient
        let operationID: String
        let started: Date
    }
    @ObservationIgnored private var tasks: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var last: Task<Void, Never>?
    @ObservationIgnored private var pollRequests: [UUID: RemotePoll] = [:]
    @ObservationIgnored private var pollTasks: [UUID: Task<Void, Never>] = [:]

    /// Queues files picked on this device for the folder `dir` (as the browser knows it, relative to cwd or ~/);
    /// uploaded gets each file's path on the machine. owned: the files are our own temporary copies (from the photo
    /// library), removed with their folder once sent.
    func upload(_ urls: [URL], into dir: String, cwd: String, machine: MachineRef, model: AppModel, owned: Bool = false,
                uploaded: @escaping (String) -> Void) {
        let features = model.features(machine.group)
        if !features.isEmpty, !features.contains("upload_dir") {
            model.toast = Toast(String(localized: "The hub is too old to upload into folders. Update the hub and try again."), style: .error)
            return
        }
        for url in urls {
            let scoped = url.startAccessingSecurityScopedResource()
            let size = Int64((try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            if size > Self.limit {
                if scoped { url.stopAccessingSecurityScopedResource() }
                if owned { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
                model.toast = Toast(String(localized: "“\(url.lastPathComponent)” is \(size.storageText), over 2 GB, and can’t be uploaded"), style: .error)
                continue
            }
            let item = Item(kind: .upload, name: url.lastPathComponent, size: size)
            queue(item) { [weak self] in
                defer {
                    if scoped { url.stopAccessingSecurityScopedResource() }
                    if owned { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
                }
                await self?.send(item.id, url: url, dir: dir, cwd: cwd, machine: machine, model: model, uploaded: uploaded)
            }
        }
    }

    /// Fetches a machine's file for Save to Files: `saving` is set once it is all here.
    func download(_ ref: FileRef, name: String, size: Int64, model: AppModel) {
        let item = Item(kind: .download, name: name, size: size)
        queue(item) { [weak self] in await self?.fetch(item.id, ref: ref, name: name, model: model) }
    }

    /// Several items, or a folder, as one zip the computer makes in a temporary folder (`files/zip`), then downloaded
    /// for Save to Files like one file (#5828).
    func downloadZip(_ paths: [String], name: String, cwd: String, machine: MachineRef, model: AppModel) {
        var item = Item(kind: .download, name: name, size: 0)
        item.zipping = true
        queue(item) { [weak self] in await self?.zipThenFetch(item.id, paths: paths, name: name, cwd: cwd, machine: machine, model: model) }
    }

    private func remoteItem(_ kind: Item.Kind, _ paths: [String]) -> Item {
        var item = Item(kind: kind, name: paths.map { ($0 as NSString).lastPathComponent }.joined(separator: ", "), size: 0)
        item.showsProgress = false
        return item
    }

    /// Copy To…: a taken name uses the same Replace / Keep Both question as an upload; each retry is a new operation.
    func copy(_ paths: [String], into dir: String, cwd: String, machine: MachineRef, model: AppModel, conflict policy: String? = nil,
              done: @escaping () -> Void) {
        guard !paths.isEmpty else { return }
        let item = remoteItem(.copy, paths)
        queue(item, serial: false) { [weak self] in
            guard let self else { return }
            update(item.id) { $0.waiting = false }
            do {
                _ = try await copyOperation(item.id, paths: paths, into: dir, cwd: cwd, machine: machine, model: model, conflict: policy)
                guard !Task.isCancelled else { return }
                Haptics.tap()
                model.toast = Toast(paths.count == 1
                    ? String(localized: "Copied “\((paths[0] as NSString).lastPathComponent)” to \(FolderEdits.folderName(dir))")
                    : String(localized: "Copied \(paths.count) items to \(FolderEdits.folderName(dir))"), style: .success)
                done()
            } catch {
                if !error.isCancellation { model.toast = Toast(FolderEdits.message(error), style: .error) }
            }
        }
    }

    func duplicate(_ paths: [String], cwd: String, machine: MachineRef, model: AppModel, made: @escaping ([String]) -> Void) {
        guard let first = paths.first else { return }
        let dir = (first as NSString).deletingLastPathComponent
        let item = remoteItem(.copy, paths)
        queue(item, serial: false) { [weak self] in
            guard let self else { return }
            update(item.id) { $0.waiting = false }
            do {
                let copied = try await copyOperation(item.id, paths: paths, into: dir, cwd: cwd, machine: machine, model: model, conflict: "keep")
                guard !Task.isCancelled else { return }
                Haptics.success()
                made(copied.map { ($0.to as NSString).lastPathComponent })
            } catch {
                if !error.isCancellation { model.toast = Toast(FolderEdits.message(error), style: .error) }
            }
        }
    }

    private func copyOperation(_ id: UUID, paths: [String], into dir: String, cwd: String, machine: MachineRef, model: AppModel,
                               conflict policy: String?) async throws -> [APIClient.MovedFile] {
        guard let api = model.api(machine.group) else { throw CancellationError() }
        var policy = policy
        while true {
            do {
                return try await remote(id, machine: machine.id, api: api) { operationID in
                    try await api.copyFiles(machine: machine.id, paths: paths, cwd: cwd, dir: dir, conflict: policy, operationID: operationID)
                }
            } catch let e as APIError where e.code == "exists" && policy == nil {
                let name = e.fields["name"] ?? (paths[0] as NSString).lastPathComponent
                update(id) { $0.asking = true }
                let choice = await ask(name, for: id)
                update(id) {
                    $0.asking = false
                    $0.done = 0
                    $0.size = 0
                    $0.filesDone = 0
                    $0.filesTotal = 0
                }
                switch choice {
                case .replace: policy = "replace"
                case .keep: policy = "keep"
                case .cancel: throw CancellationError()
                }
            }
        }
    }

    func compress(_ paths: [String], cwd: String, machine: MachineRef, model: AppModel, made: @escaping (String) -> Void) {
        guard let first = paths.first, let api = model.api(machine.group) else { return }
        let dir = (first as NSString).deletingLastPathComponent
        let base = (first as NSString).lastPathComponent
        let name = paths.count == 1 ? ((base as NSString).deletingPathExtension.isEmpty ? base : (base as NSString).deletingPathExtension) + ".zip"
            : String(localized: "Archive.zip")
        let item = remoteItem(.compress, paths)
        queue(item, serial: false) { [weak self] in
            guard let self else { return }
            update(item.id) { $0.waiting = false }
            do {
                let zip = try await remote(item.id, machine: machine.id, api: api) { operationID in
                    try await api.zipFiles(machine: machine.id, paths: paths, cwd: cwd, dir: dir, name: name, operationID: operationID)
                }
                guard !Task.isCancelled else { return }
                Haptics.success()
                model.toast = Toast(String(localized: "Compressed into “\(zip.name)”"), style: .success)
                made(zip.name)
            } catch {
                if !error.isCancellation { model.toast = Toast(FolderEdits.message(error), style: .error) }
            }
        }
    }

    /// The original long POST remains authoritative. Polling only paints progress; cancellation of this task cancels that POST.
    private func remote<T>(_ id: UUID, machine: String, api: APIClient,
                           request: (String) async throws -> T) async throws -> T {
        let operationID = UUID().uuidString
        pollRequests[id] = RemotePoll(machine: machine, api: api, operationID: operationID, started: Date())
        startPolling(id)
        defer {
            pollTasks.removeValue(forKey: id)?.cancel()
            pollRequests[id] = nil
        }
        return try await request(operationID)
    }

    /// Restarts only polling when the file page returns to the front; the original POST never stopped.
    func resumePolling() {
        for id in pollRequests.keys { startPolling(id) }
    }

    private func startPolling(_ id: UUID) {
        guard pollTasks[id] == nil, let poll = pollRequests[id],
              let item = items.first(where: { $0.id == id }), item.session == session else { return }
        pollTasks[id] = Task { [weak self] in
            let delay = max(0, 1 - Date().timeIntervalSince(poll.started))
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            // A successful read proves this chain can report and cancel the operation; old chains keep the old no-row UI.
            while !Task.isCancelled {
                if let operation = try? await poll.api.fileOperation(machine: poll.machine, id: poll.operationID) {
                    self?.update(id) {
                        $0.showsProgress = true
                        $0.done = operation.bytesDone
                        $0.size = operation.bytesTotal
                        $0.filesDone = operation.filesDone
                        $0.filesTotal = operation.filesTotal
                    }
                }
                try? await Task.sleep(for: .milliseconds(250))
            }
        }
    }

    func cancel(_ id: UUID) {
        pollTasks.removeValue(forKey: id)?.cancel()
        pollRequests[id] = nil
        tasks.removeValue(forKey: id)?.cancel()
        for question in conflicts where question.item == id { answer(.cancel, to: question.id) } // only its own
        items.removeAll { $0.id == id }
        syncVisible()
    }

    /// When the browser goes away (the page, not the scene: see `pausePolling`): a question nobody will answer is a
    /// no, downloads nobody will save stop. Uploads and remote copy / zip POSTs go on; only their progress polling
    /// pauses until the file page is in front again.
    func stop() {
        for question in conflicts { answer(.cancel, to: question.id) }
        pausePolling()
        for item in items where item.kind == .download { cancel(item.id) }
        if let saving { MachineFileDownload.remove(saving) }
        saving = nil
    }

    /// Control Center, a call, the app switcher: the scene, not the page, went away. Only progress polling pauses;
    /// downloads, a file waiting in Save to Files and questions stay for the user (`resumePolling` on return).
    func pausePolling() {
        for task in pollTasks.values { task.cancel() }
        pollTasks.removeAll()
    }

    /// After Save to Files (moved, or not): the temporary folder goes.
    func saved() {
        MachineFileDownload.remove(saving)
        saving = nil
    }

    /// Uploads and downloads stay serial. A remote copy / zip starts independently so a long transfer cannot hide it.
    private func queue(_ item: Item, serial: Bool = true, _ work: @escaping () async -> Void) {
        var item = item
        item.session = session
        items.append(item)
        syncVisible()
        let prev = serial ? last : nil
        let task = Task { [weak self] in
            await prev?.value
            guard !Task.isCancelled else { return }
            await work()
            self?.tasks[item.id] = nil
            self?.items.removeAll { $0.id == item.id }
            self?.syncVisible()
        }
        tasks[item.id] = task
        if serial { last = task }
    }

    private func send(_ id: UUID, url: URL, dir: String, cwd: String, machine: MachineRef, model: AppModel,
                      uploaded: (String) -> Void) async {
        update(id) { $0.waiting = false }
        guard let api = model.api(machine.group) else { return }
        let name = url.lastPathComponent
        do {
            let staged = try await FileUploads.stage(url)
            defer { try? FileManager.default.removeItem(at: staged.deletingLastPathComponent()) }
            update(id) { $0.size = (try? staged.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? $0.size }
            let out = try await Self.send(staged, name: name, dir: dir, cwd: cwd, machine: machine.id, api: api,
                                          ask: { [weak self] name in
                                              self?.update(id) { $0.asking = true }
                                              return await self?.ask(name, for: id) ?? .cancel
                                          },
                                          restart: { [weak self] in self?.update(id) { $0.done = 0; $0.asking = false } }) { sent in
                Task { @MainActor [weak self] in self?.update(id) { $0.done = max($0.done, sent) } }
            }
            guard let out, !Task.isCancelled else { return }
            Haptics.tap()
            uploaded(out.path)
        } catch {
            if let msg = Self.message(error, name: name) { model.toast = Toast(msg, style: .error) }
        }
    }

    /// One file into the folder: sent with no conflict policy first; a taken name asks, and the answer sends it again
    /// with it (nil: the user said Cancel).
    static func send(_ file: URL, name: String, dir: String, cwd: String, machine: String, api: APIClient,
                     ask: (String) async -> Choice, restart: () -> Void = {},
                     progress: @escaping @Sendable (Int64) -> Void) async throws -> APIClient.UploadedFile? {
        var policy: String?
        while true {
            do {
                var query = ["dir": dir, "cwd": cwd]
                if let policy { query["conflict"] = policy }
                return try await api.upload(file: file, name: name, machine: machine, dest: "dir", query: query, progress: progress)
            } catch let e as APIError where e.code == "exists" && policy == nil {
                switch await ask(name) {
                case .replace: policy = "replace"
                case .keep: policy = "keep"
                case .cancel: return nil
                }
                restart()
            }
        }
    }

    private func zipThenFetch(_ id: UUID, paths: [String], name: String, cwd: String, machine: MachineRef, model: AppModel) async {
        update(id) { $0.waiting = false }
        guard let api = model.api(machine.group) else { return }
        do {
            let zip = try await api.zipFiles(machine: machine.id, paths: paths, cwd: cwd, dir: nil, name: name)
            try Task.checkCancellation()
            update(id) {
                $0.zipping = false
                $0.size = zip.size
            }
            await fetch(id, ref: FileRef(group: machine.group, machineID: machine.id, path: zip.path, cwd: cwd), name: name, model: model)
        } catch {
            if !error.isCancellation {
                model.toast = Toast(String(localized: "Couldn’t download “\(name)”: \(FolderEdits.message(error))"), style: .error)
            }
        }
    }

    private func fetch(_ id: UUID, ref: FileRef, name: String, model: AppModel) async {
        update(id) { $0.waiting = false }
        guard let api = model.api(ref.group) else { return }
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("save-" + UUID().uuidString, isDirectory: true)
        let url = directory.appendingPathComponent((name as NSString).lastPathComponent)
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try await api.download("/api/v1/machines/\(ref.machineID)/file/download",
                                   query: ["path": ref.path, "cwd": ref.cwd ?? ""], to: url) { received, total in
                Task { @MainActor [weak self] in
                    self?.update(id) { $0.done = received; if total > 0 { $0.size = total } }
                }
            }
            try Task.checkCancellation()
            Haptics.tap()
            if let saving { MachineFileDownload.remove(saving) } // one at a time: a newer one wins
            saving = url
        } catch {
            try? FileManager.default.removeItem(at: directory)
            if !error.isCancellation {
                model.toast = Toast(String(localized: "Couldn’t download “\(name)”: \(error.localizedDescription)"), style: .error)
            }
        }
    }

    /// Waits for the answer to transfer `item`'s question, queued behind any question already asked.
    private func ask(_ name: String, for item: Item.ID) async -> Choice {
        guard !Task.isCancelled else { return .cancel } // its row is gone: nobody will be asked
        return await withCheckedContinuation { (c: CheckedContinuation<Choice, Never>) in
            let question = Conflict(name: name, item: item)
            answers[question.id] = c
            conflicts.append(question)
        }
    }

    /// The answer to the question in front (tests; the alert answers the one it shows by id).
    func answer(_ choice: Choice) {
        if let conflict { answer(choice, to: conflict.id) }
    }

    /// The answer to one question; nothing once it has been answered, so a late call never answers the next one.
    func answer(_ choice: Choice, to id: Conflict.ID) {
        conflicts.removeAll { $0.id == id }
        answers.removeValue(forKey: id)?.resume(returning: choice)
    }

    private func update(_ id: UUID, _ change: (inout Item) -> Void) {
        guard let i = items.firstIndex(where: { $0.id == id }) else { return }
        var next = items[i]
        change(&next)
        guard next != items[i] else { return }
        items[i] = next
        syncVisible()
    }

    /// What to tell the user when a file did not make it into the folder; nil when they cancelled it.
    nonisolated static func message(_ error: Error, name: String) -> String? {
        if error.isCancellation { return nil }
        if let e = error as? APIError {
            switch e.code {
            case "too_large": return String(localized: "“\(name)” is over 2 GB and can’t be uploaded")
            case "read_only": return String(localized: "This folder can’t be changed from your phone. Only folders in the home folder can (except ~/.persona and ~/Library).")
            case "node_outdated": return e.message
            default: break
            }
            if e.status == 404 || (e.status == 400 && e.message.contains("dest")) {
                return String(localized: "The hub is too old to upload into folders. Update the hub and try again.")
            }
        }
        return String(localized: "“\(name)” wasn’t uploaded: \(error.localizedDescription)")
    }
}

/// The transfers of a folder under its list: name, progress, cancel.
struct FolderTransfersBar: View {
    var transfers: FolderTransfers

    var body: some View {
        VStack(spacing: Theme.Space.sm) {
            ForEach(transfers.visibleItems) { item in
                AttachmentRow(symbol: symbol(item), name: item.name, status: item.status,
                              progress: item.asking || item.zipping ? nil : item.size > 0 ? Double(min(item.done, item.size)) / Double(item.size) : 0,
                              removeLabel: removeLabel(item)) { transfers.cancel(item.id) }
            }
        }
    }

    private func symbol(_ item: FolderTransfers.Item) -> String {
        switch item.kind {
        case .upload: Theme.Icon.upload
        case .download: Theme.Icon.save
        case .copy: Theme.Icon.copy
        case .compress: Theme.Icon.archive
        }
    }

    private func removeLabel(_ item: FolderTransfers.Item) -> String {
        switch item.kind {
        case .upload: String(localized: "Cancel upload of \(item.name)")
        case .download: String(localized: "Cancel download of \(item.name)")
        case .copy: String(localized: "Cancel copy of \(item.name)")
        case .compress: String(localized: "Cancel compression of \(item.name)")
        }
    }
}

/// A picture or video from the photo library as a file of its own (its original format and name where it has one), or
/// any file dragged in from another app.
nonisolated struct PickedLibraryFile: Transferable {
    let url: URL

    static var transferRepresentation: some TransferRepresentation {
        FileRepresentation(importedContentType: .movie) { try copy($0.file) }
        FileRepresentation(importedContentType: .image) { try copy($0.file) }
        FileRepresentation(importedContentType: .data) { try copy($0.file) }
    }

    private static func copy(_ file: URL) throws -> PickedLibraryFile {
        let dir = FileManager.default.temporaryDirectory.appending(path: "uploads/" + UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let dest = dir.appending(path: file.lastPathComponent)
        try FileManager.default.copyItem(at: file, to: dest)
        return PickedLibraryFile(url: dest)
    }

    /// The picked items as files, in the order picked; the ones that could not be read are counted.
    static func load(_ items: [PhotosPickerItem]) async -> (urls: [URL], failed: Int) {
        var urls: [URL] = [], failed = 0
        for item in items {
            if let f = try? await item.loadTransferable(type: PickedLibraryFile.self) { urls.append(f.url) } else { failed += 1 }
        }
        return (urls, failed)
    }
}
