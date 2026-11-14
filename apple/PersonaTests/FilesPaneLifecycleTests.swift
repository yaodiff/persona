#if os(iOS)
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// FilesPane on a real window (#6113 review): Control Center, a call or the app switcher only pause progress polling;
/// a download, a file waiting in Save to Files and an "already exists" question stay for the user. Leaving the page
/// still ends them. Questions from two transfers are shown one after the other.
/// Serialized: each test puts a key window over the scene and presents from it (lessons/testing.md, #2025).
@Suite(.serialized) @MainActor
struct FilesPaneLifecycleTests {
    @Observable @MainActor final class Host {
        var active = true
        var phase: ScenePhase = .active
        var mounted = true
    }

    private struct Root: View {
        let host: Host
        let model: AppModel
        let transfers: FolderTransfers

        var body: some View {
            ZStack {
                if host.mounted {
                    FilesPane(machineID: "m1", cwd: "~", transfers: transfers)
                        .environment(\.tabIsActive, host.active)
                        .environment(\.scenePhase, host.phase)
                }
            }
            .environment(model)
        }
    }

    /// A window of its own in the test host's scene, so the page can present the alert and the system's Save to Files.
    @MainActor private final class FilesPaneTestWindow {
        let window: UIWindow

        init(_ root: some View) {
            let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
            window = scene.map { UIWindow(windowScene: $0) } ?? UIWindow(frame: CGRect(x: 0, y: 0, width: 402, height: 874))
            if let scene { window.frame = scene.coordinateSpace.bounds }
            window.windowLevel = .alert + 1
            window.rootViewController = UIHostingController(rootView: root)
            window.makeKeyAndVisible()
        }

        var presented: UIViewController? {
            var top = window.rootViewController?.presentedViewController
            while let next = top?.presentedViewController { top = next }
            return top
        }

        var alertTitle: String? { (presented as? UIAlertController)?.title }
        /// What is presented, for a failure message.
        var chain: String {
            var out: [String] = [], top = window.rootViewController?.presentedViewController
            while let vc = top {
                out.append("\(type(of: vc))(\((vc as? UIAlertController)?.title ?? "-"), beingDismissed: \(vc.isBeingDismissed))")
                top = vc.presentedViewController
            }
            return out.joined(separator: " > ")
        }
        var savePickerOpen: Bool { presented is UIDocumentPickerViewController }

        func close() {
            window.rootViewController?.dismiss(animated: false)
            window.isHidden = true
            window.rootViewController = nil
        }
    }

    @MainActor private final class Flags {
        var downloadCancelled = false
    }

    private let machine = MachineRef(group: "", id: "m1")

    /// ~/slow.txt: a remote copy that runs until cancelled, its operation read for progress. ~/c.txt into ~/dest and an
    /// upload of u.txt: names already taken until the answer comes. ~/big.bin: a download that runs until cancelled.
    private func makeHub(_ flags: Flags) -> FakeHub {
        let hub = FakeHub()
        hub.onAsync("POST", "/api/v1/machines/m1/files/copy") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            if (body?["paths"] as? [String]) == ["~/c.txt"] {
                if body?["conflict"] == nil { throw APIError(status: 409, message: "taken", fields: ["code": "exists", "name": "c.txt"]) }
                return Data(#"{"copied":[{"from":"~/c.txt","to":"~/dest/c 2.txt"}]}"#.utf8)
            }
            let id = body?["operation_id"] as? String ?? "missing"
            hub.on("GET", "/api/v1/machines/m1/files/operations/\(id)",
                   json: #"{"id":"\#(id)","kind":"copy","state":"running","files_done":1,"files_total":2,"bytes_done":32,"bytes_total":100}"#)
            while !Task.isCancelled { try? await Task.sleep(for: .milliseconds(20)) }
            throw CancellationError()
        }
        hub.on("POST", "/api/v1/machines/m1/files") { req in
            if req.query["conflict"] == nil { throw APIError(status: 409, message: "taken", fields: ["code": "exists", "name": "u.txt"]) }
            return Data(#"{"path": "/home/me/dest/u 2.txt", "name": "u 2.txt", "size": 5}"#.utf8)
        }
        hub.onAsync("GET", "/api/v1/machines/m1/file/download") { _ in
            while !Task.isCancelled { try? await Task.sleep(for: .milliseconds(20)) }
            flags.downloadCancelled = true
            throw CancellationError()
        }
        return hub
    }

    private func reads(_ hub: FakeHub) -> Int { hub.requests.filter { $0.path.contains("/files/operations/") }.count }

    private func waitUntil(_ condition: @escaping @MainActor () -> Bool) async {
        for _ in 0..<300 where !condition() { try? await Task.sleep(for: .milliseconds(20)) }
    }

    private func settle() async { try? await Task.sleep(for: .milliseconds(300)) }

    /// A finished download waiting in Save to Files: a folder of its own, as `FolderTransfers.fetch` makes it.
    private func pendingSave() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appending(path: "save-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appending(path: "notes.txt")
        try Data("hello".utf8).write(to: url)
        return url
    }

    private func localFile(_ name: String) throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appending(path: "folder-tests/" + UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appending(path: name)
        try Data("hello".utf8).write(to: url)
        return url
    }

    @Test func sceneChangesOnlyPausePolling() async throws {
        let flags = Flags(), hub = makeHub(flags), model = AppModel(), transfers = FolderTransfers(), host = Host()
        model.apiForTests = hub.client
        let shown = FilesPaneTestWindow(Root(host: host, model: model, transfers: transfers))
        defer {
            for item in transfers.items { transfers.cancel(item.id) }
            shown.close()
        }
        await settle()
        transfers.duplicate(["~/slow.txt"], cwd: "~", machine: machine, model: model) { _ in }
        transfers.download(FileRef(group: "", machineID: "m1", path: "~/big.bin", cwd: "~"), name: "big.bin", size: 1 << 20, model: model)
        await waitUntil { hub.sent("GET", "/api/v1/machines/m1/file/download").count == 1 }
        let save = try pendingSave()
        transfers.saving = save
        await waitUntil { shown.savePickerOpen }
        #expect(shown.savePickerOpen, "Save to Files (.fileMover) did not open")
        await waitUntil { reads(hub) > 0 }
        #expect(reads(hub) > 0, "the remote copy was not polled")

        for phase in [ScenePhase.inactive, .background] {
            host.phase = phase
            await settle()
            let paused = reads(hub)
            try await Task.sleep(for: .milliseconds(600))
            #expect(reads(hub) == paused, "progress polling went on while the scene was \(phase)")
            #expect(transfers.items.contains { $0.kind == .download } && !flags.downloadCancelled,
                    "the \(phase) scene cancelled the download")
            #expect(transfers.saving == save && FileManager.default.fileExists(atPath: save.path),
                    "the \(phase) scene removed the file waiting for Save to Files")
            #expect(shown.savePickerOpen, "the \(phase) scene closed Save to Files")
        }

        let before = reads(hub)
        host.phase = .active
        await waitUntil { reads(hub) > before }
        #expect(reads(hub) > before, "polling did not resume with the active scene")
        #expect(transfers.saving == save && !flags.downloadCancelled && shown.savePickerOpen)

        // Leaving the page still ends what nobody will see through; the remote copy's POST goes on.
        host.mounted = false
        await waitUntil { flags.downloadCancelled && transfers.saving == nil }
        #expect(flags.downloadCancelled && transfers.saving == nil, "leaving the page kept the download")
        #expect(!FileManager.default.fileExists(atPath: save.path), "leaving the page kept the file for Save to Files")
        #expect(transfers.items.map(\.kind) == [.copy], "leaving the page stopped the remote copy")
    }

    @Test func sceneChangesLeaveAQuestionForTheUser() async throws {
        let flags = Flags(), hub = makeHub(flags), model = AppModel(), transfers = FolderTransfers(), host = Host()
        model.apiForTests = hub.client
        let shown = FilesPaneTestWindow(Root(host: host, model: model, transfers: transfers))
        defer {
            for item in transfers.items { transfers.cancel(item.id) }
            shown.close()
        }
        await settle()
        var copied = false
        transfers.copy(["~/c.txt"], into: "~/dest", cwd: "~", machine: machine, model: model) { copied = true }
        await waitUntil { shown.alertTitle?.contains("c.txt") == true }
        #expect(shown.alertTitle?.contains("c.txt") == true, "the taken name did not ask")

        for phase in [ScenePhase.inactive, .background, .active] {
            host.phase = phase
            await settle()
            #expect(transfers.conflict?.name == "c.txt", "the \(phase) scene answered the question")
            #expect(shown.alertTitle?.contains("c.txt") == true, "the \(phase) scene closed the question")
            #expect(!copied)
        }
        transfers.answer(.keep)
        await waitUntil { copied }
        #expect(copied)
        #expect(hub.sent("POST", "/api/v1/machines/m1/files/copy").count == 2)
    }

    /// Presses a button of the alert as UIKit does: the alert closes, then the action's handler runs (SwiftUI's button
    /// action, then its isPresented binding). The alert is modal, so its buttons are how a shown question gets answered.
    private func press(_ title: String, in alert: UIAlertController) async {
        guard let action = alert.actions.first(where: { $0.title == title }), let block = action.value(forKey: "handler") else {
            Issue.record("no \(title) in the alert: \(alert.actions.map { $0.title ?? "" })")
            return
        }
        typealias Handler = @convention(block) (UIAlertAction) -> Void
        let handler = unsafeBitCast(block as AnyObject, to: Handler.self)
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            alert.dismiss(animated: true) {
                handler(action)
                c.resume()
            }
        }
    }

    /// A remote copy and an upload ask at once: the copy's question first; its Keep Both answers only the copy, then the
    /// upload's question opens an alert of its own, whose Cancel answers only the upload.
    @Test func questionsFromTwoTransfersAreShownInTurn() async throws {
        let flags = Flags(), hub = makeHub(flags), model = AppModel(), transfers = FolderTransfers(), host = Host()
        model.apiForTests = hub.client
        let shown = FilesPaneTestWindow(Root(host: host, model: model, transfers: transfers))
        defer {
            for item in transfers.items { transfers.cancel(item.id) }
            shown.close()
        }
        await settle()
        var copied = false, uploaded = ""
        transfers.copy(["~/c.txt"], into: "~/dest", cwd: "~", machine: machine, model: model) { copied = true }
        await waitUntil { transfers.items.first?.asking == true }
        transfers.upload([try localFile("u.txt")], into: "~/dest", cwd: "~", machine: machine, model: model) { uploaded = $0 }
        await waitUntil { transfers.items.filter(\.asking).count == 2 && shown.alertTitle != nil }
        let first = try #require(shown.presented as? UIAlertController, "no alert for the questions: \(shown.chain)")
        #expect(first.title?.contains("c.txt") == true, "the first question was replaced: \(shown.chain)")

        await press(String(localized: "Keep Both"), in: first)
        await waitUntil { copied && shown.alertTitle?.contains("u.txt") == true }
        #expect(copied, "Keep Both did not answer the copy's question")
        let second = try #require(shown.presented as? UIAlertController, "the upload's question did not open its own alert: \(shown.chain)")
        #expect(second !== first && second.title?.contains("u.txt") == true, "the second question was not shown after the first: \(shown.chain)")
        #expect(hub.sent("POST", "/api/v1/machines/m1/files").count == 1, "closing the first alert answered the second")

        await press(String(localized: "Cancel"), in: second)
        await waitUntil { transfers.items.isEmpty && shown.presented == nil }
        #expect(uploaded.isEmpty && hub.sent("POST", "/api/v1/machines/m1/files").count == 1, "Cancel sent the upload again")
        #expect(transfers.items.isEmpty && transfers.conflict == nil && shown.presented == nil, "still presented: \(shown.chain)")
        #expect(hub.sent("POST", "/api/v1/machines/m1/files/copy").count == 2)
    }
}
#endif
