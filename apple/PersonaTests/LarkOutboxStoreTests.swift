import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct LarkOutboxStoreTests {
    private func folder() throws -> URL {
        let url = URL.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func identity(_ account: String = "fixture-account", group: String = "") -> LarkOutboxIdentity {
        LarkOutboxIdentity(group: group, account: account)
    }

    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/lark/raw/accounts", json: #"{"activeId":"1","accounts":[{"id":"1","userId":"fixture-account","signedIn":true}]}"#)
        return hub
    }

    private func attachment(in root: URL) async throws -> LarkAttachment {
        let file = root.appending(path: "notes.txt")
        try Data("draft file".utf8).write(to: file)
        return try await .stageFile(file)
    }

    @Test func restartKeepsDraftBytesAndQuoteWithoutSourceAccess() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        var file: LarkAttachment? = try await attachment(in: root)
        let staged = file!.file
        let quote = LarkReplyTo(id: "source", senderName: "Sender", text: "Question")
        try await queue.saveDraft(LarkDraft(text: "Answer", replyTo: quote), attachments: [file!])
        file = nil
        #expect(!FileManager.default.fileExists(atPath: staged.path))
        try FileManager.default.removeItem(at: root.appending(path: "notes.txt"))
        let restarted = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await restarted.prepare()
        #expect(restarted.snapshot.draft == LarkDraft(text: "Answer", replyTo: quote))
        let restored = try #require(restarted.snapshot.attachments.first)
        #expect(try String(contentsOf: restored.file, encoding: .utf8) == "draft file")
        #expect(restored.file.pathExtension == "txt")
        #expect(restarted.items.isEmpty)
        let bytes = try String(contentsOf: await store.directory.appending(path: "state.json"), encoding: .utf8)
        #expect(!bytes.contains("fixture-credential") && !bytes.contains(root.path))
    }

    @Test func restartPreservesMentionRecipientsInDraftAndOutgoing() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let recipient = LarkMention(userID: "user-example", name: "同事")
        let draft = LarkDraft(text: "@同事 请确认", mentions: [recipient])
        let outgoing = LarkOutgoing(text: "@同事 已更新", mentions: [recipient])
        _ = try await store.save(.init(draft: draft, items: [outgoing]))
        let loaded = try #require(try await store.load())
        #expect(loaded.draft.mentions == [recipient])
        #expect(loaded.items.first?.mentions == [recipient])
        let record = await store.directory.appending(path: "state.json")
        var json = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: record)) as? [String: Any])
        var rows = try #require(json["items"] as? [[String: Any]])
        rows[0].removeValue(forKey: "mentions")
        json["items"] = rows
        try JSONSerialization.data(withJSONObject: json).write(to: record)
        #expect(try await store.load()?.items.first?.mentions == [])
    }

    /// Where a retried message stands among the chat's messages (LarkChatRows, #921) survives a restart; files saved
    /// before it was kept read as never retried.
    @Test func restartKeepsWhenARetriedMessageWasWritten() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let written = Date(timeIntervalSince1970: 1_800_000_000)
        var outgoing = LarkOutgoing(text: "重发的", at: written)
        outgoing.retry(at: written.addingTimeInterval(60))
        _ = try await store.save(.init(items: [outgoing]))
        #expect(try await store.load()?.items.first?.written == written)
        let record = await store.directory.appending(path: "state.json")
        var json = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: record)) as? [String: Any])
        var rows = try #require(json["items"] as? [[String: Any]])
        rows[0].removeValue(forKey: "firstAt")
        json["items"] = rows
        try JSONSerialization.data(withJSONObject: json).write(to: record)
        #expect(try await store.load()?.items.first?.written == written.addingTimeInterval(60))
    }

    @Test func interruptedSendRestoresOnceWithSameIDAndNeverResendsByItself() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let queue = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await queue.prepare()
        let file = try await attachment(in: root)
        try await queue.saveDraft(LarkDraft(text: "Caption"), attachments: [file])
        let batch = [LarkOutgoing(text: "Caption", attempted: true), LarkOutgoing(text: "", attachment: file, attempted: true)]
        try await queue.enqueue(batch)
        let restarted = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await restarted.prepare()
        #expect(restarted.snapshot.draft.isEmpty && restarted.snapshot.attachments.isEmpty)
        #expect(restarted.items.map(\.id) == batch.map(\.id))
        #expect(restarted.items.allSatisfy { $0.state == .unconfirmed })
        let hub = hub()
        restarted.deliver(batch[0].id, using: hub.client, feed: "123") {}
        await Task.yield()
        #expect(hub.sent("POST", "/api/v1/lark/conversations/123/messages").isEmpty)
        var copy = LarkMessage(id: "local:" + batch[1].clientID, position: "3", senderName: "我", isMine: true,
                               type: "FILE", text: "", recalled: false, replyCount: 0,
                               clientID: batch[1].clientID, localState: "pending")
        try await restarted.settle([copy])
        #expect(restarted.items.count == 2)
        copy.id = "confirmed-copy"; copy.localState = "synced"
        let media = try #require(restarted.items.last?.attachment?.file)
        try await restarted.settle([copy])
        #expect(restarted.items.map(\.id) == [batch[0].id])
        #expect(!FileManager.default.fileExists(atPath: media.path))
        let saved = try await LarkOutboxStore(identity: identity(), feed: "123", base: root).load()
        #expect(saved?.items.map(\.id) == [batch[0].id])
    }

    @Test func queuedFilesStayRetryableButAttemptedAndLegacyFilesStayUnconfirmed() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let file = try await attachment(in: root)
        let attempted = LarkOutgoing(text: "", attachment: file, attempted: true)
        let queued = LarkOutgoing(text: "", attachment: file)
        _ = try await store.save(.init(items: [attempted, queued]))
        let loaded = try #require(try await store.load())
        #expect(loaded.items[0].state == .unconfirmed && !loaded.items[0].canRetry)
        #expect(loaded.items[1].state == .failed("尚未发送，点按重试") && loaded.items[1].canRetry)
        #expect(loaded.items[1].id == queued.id)
        let url = await store.directory.appending(path: "state.json")
        var record = try #require(JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        var rows = try #require(record["items"] as? [[String: Any]])
        rows[1].removeValue(forKey: "attempted")
        record["items"] = rows
        try JSONSerialization.data(withJSONObject: record).write(to: url)
        #expect(try await store.load()?.items[1].state == .unconfirmed)
    }

    @Test func attemptedMarkerIsDurableBeforeNetworkSubmission() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        let item = LarkOutgoing(text: "check ordering")
        try await queue.enqueue([item])
        var api = hub().client
        api.transport = { request in
            if request.path == "/api/v1/lark/raw/accounts" {
                return Data(#"{"activeId":"1","accounts":[{"id":"1","userId":"fixture-account","signedIn":true}]}"#.utf8)
            }
            let saved = try await store.load()
            #expect(saved?.items.first?.attempted == true)
            #expect(saved?.items.first?.state == .unconfirmed)
            return Data(#"{"message_id":"receipt"}"#.utf8)
        }
        queue.deliver(item.id, using: api, feed: "123") {}
        for _ in 0..<200 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .sent)
    }

    @Test func attemptMarkerWriteFailureNeverStartsNetworkRequest() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        let item = LarkOutgoing(text: "must stay local")
        try await queue.enqueue([item])
        let directory = await store.directory
        try FileManager.default.moveItem(at: directory, to: directory.appendingPathExtension("backup"))
        try Data().write(to: directory)
        let hub = hub(), path = "/api/v1/lark/conversations/123/messages"
        hub.on("POST", path, json: #"{"message_id":"unexpected"}"#)
        queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<200 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        guard case .failed = queue.items.first?.state else { Issue.record("Expected a recoverable local failure"); return }
        #expect(hub.sent("POST", path).isEmpty && queue.items.first?.attempted == false)
    }

    @Test func explicitRecoveryDiscardsCorruptRecordMediaAndOldBackups() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        _ = try await store.save(.init(attachments: [try await attachment(in: root)]))
        let directory = await store.directory
        let record = directory.appending(path: "state.json"), bytes = Data("broken".utf8)
        try bytes.write(to: record)
        let backup = directory.deletingLastPathComponent().appending(path: directory.lastPathComponent + ".recovery-old")
        try FileManager.default.copyItem(at: directory, to: backup)
        let queue = LarkChatOutbox(store: store)
        await #expect(throws: (any Error).self) { try await queue.prepare() }
        #expect(!queue.ready && queue.canRecover)
        try await queue.recover()
        #expect(queue.ready && !queue.canRecover && queue.preparationProblem == nil)
        #expect(!FileManager.default.fileExists(atPath: backup.path))
        #expect(try FileManager.default.contentsOfDirectory(at: directory.appending(path: "files"), includingPropertiesForKeys: nil).isEmpty)
        try await queue.saveDraft(.init(text: "new draft"), attachments: [])
        #expect(try await store.load()?.draft.text == "new draft")
    }

    @Test func newerVersionOffersRecoveryWithoutMisreportingStorageFailure() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        _ = try await store.save(.init())
        try Data(#"{"version":99}"#.utf8).write(to: await store.directory.appending(path: "state.json"))
        let queue = LarkChatOutbox(store: store)
        await #expect(throws: LarkOutboxStore.Failure.version) { try await queue.prepare() }
        #expect(queue.canRecover && queue.preparationProblem == LarkOutboxStore.Failure.version.localizedDescription)
    }

    @Test func failureSurvivesRestartAndExplicitRetryUsesOriginalClientID() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let queue = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await queue.prepare()
        let file = try await attachment(in: root)
        let item = LarkOutgoing(text: "", state: .failed("离线"), attachment: file)
        try await queue.enqueue([item])
        let restarted = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await restarted.prepare()
        #expect(restarted.items.first?.state == .failed("离线"))
        let hub = hub(), path = "/api/v1/lark/conversations/123/files"
        hub.on("POST", path, json: #"{"message_id":"copy"}"#)
        try await restarted.retry(item.id)
        restarted.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<200 where restarted.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(restarted.items.first?.state == .sent)
        #expect(hub.sent("POST", path).map { $0.query["client_id"] } == [item.clientID])
        let saved = try await LarkOutboxStore(identity: identity(), feed: "123", base: root).load()
        #expect(saved?.items.first?.messageID == "copy")
        #expect(saved?.items.first?.state == .sent)
    }

    @Test func restoreMovesFileBackToDraftAtomicallyAndKeepsOtherDraftText() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        let item = LarkOutgoing(text: "", state: .failed("离线"), attachment: try await attachment(in: root))
        try await queue.enqueue([item])
        let file = try #require(queue.items.first?.attachment)
        try await queue.restore(item.id, draft: LarkDraft(text: "Keep this"), attachments: [file])
        let saved = try #require(try await store.load())
        #expect(saved.items.isEmpty && saved.draft.text == "Keep this")
        #expect(saved.attachments.map(\.id) == [file.id])
        #expect(FileManager.default.fileExists(atPath: file.file.path))
        try await queue.saveDraft(LarkDraft(), attachments: [])
        #expect(!FileManager.default.fileExists(atPath: file.file.path))
    }

    @Test func isolatesAccountsGroupsAndChat() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        _ = try await store.save(LarkOutboxSnapshot(draft: LarkDraft(text: "private")))
        #expect(try await LarkOutboxStore(identity: identity("different"), feed: "123", base: root).load() == nil)
        #expect(try await LarkOutboxStore(identity: identity(), feed: "456", base: root).load() == nil)
        #expect(try await LarkOutboxStore(identity: identity(group: "another-group"), feed: "123", base: root).load() == nil)
    }

    @Test func anAcknowledgedFileOutsideTheTailWindowRequiresItsExactOwnedCopy() async throws {
        let queue = LarkChatOutbox(), hub = hub()
        let item = LarkOutgoing(text: "", state: .sent, messageID: "999")
        try await queue.enqueue([item])
        let path = "/api/v1/lark/raw/messages/999"
        hub.on("GET", path, json: #"{"item":{"id":"999","feedId":"other","isMine":true}}"#)
        await queue.verifyAcknowledged(using: hub.client, feed: "123")
        #expect(queue.items.count == 1)
        hub.on("GET", path, json: #"{"item":{"id":"999","feedId":"123","isMine":false}}"#)
        await queue.verifyAcknowledged(using: hub.client, feed: "123")
        #expect(queue.items.count == 1)
        hub.on("GET", path, json: #"{"item":{"id":"999","feedId":"123","isMine":true,"localState":"pending"}}"#)
        await queue.verifyAcknowledged(using: hub.client, feed: "123")
        #expect(queue.items.count == 1)
        hub.on("GET", path, json: #"{"item":{"id":"999","feedId":"123","isMine":true,"localState":"synced"}}"#)
        await queue.verifyAcknowledged(using: hub.client, feed: "123")
        #expect(queue.items.isEmpty)
    }

    @Test func diskFailurePreservesPreviousDraftAndNeverEnqueues() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        let file = try await attachment(in: root)
        try await queue.saveDraft(LarkDraft(text: "keep"), attachments: [file])
        let dir = await store.directory
        // Rename the original directory aside, then put a file in its place to force writes to fail.
        let backup = dir.deletingLastPathComponent().appending(path: "backup")
        try FileManager.default.moveItem(at: dir, to: backup)
        try Data().write(to: dir)
        await #expect(throws: (any Error).self) { try await queue.enqueue([LarkOutgoing(text: "keep", attachment: file)]) }
        #expect(queue.items.isEmpty && queue.snapshot.draft.text == "keep")
        try FileManager.default.removeItem(at: dir)
        try FileManager.default.moveItem(at: backup, to: dir)
        #expect(try await store.load()?.draft.text == "keep")
    }

    @Test func corruptRecordIsNotOverwrittenAndMissingMediaRemainsRemovable() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let file = try await attachment(in: root)
        let saved = try await store.save(LarkOutboxSnapshot(attachments: [file]))
        try FileManager.default.removeItem(at: saved.attachments[0].file)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        #expect(queue.snapshot.attachments.count == 1)
        let missing = try #require(queue.snapshot.attachments.first)
        await #expect(throws: (any Error).self) { try await queue.enqueue([LarkOutgoing(text: "", attachment: missing)]) }
        #expect(queue.items.isEmpty && queue.snapshot.attachments.count == 1)
        try await queue.saveDraft(LarkDraft(), attachments: [])
        let url = await store.directory.appending(path: "state.json")
        let invalid = Data("invalid record".utf8)
        try invalid.write(to: url)
        let broken = LarkChatOutbox(store: store)
        await #expect(throws: (any Error).self) { try await broken.prepare() }
        await #expect(throws: (any Error).self) { try await broken.enqueue([LarkOutgoing(text: "never send")]) }
        #expect(!broken.ready && broken.items.isEmpty)
        #expect(try Data(contentsOf: url) == invalid)
    }

    @Test func selectedAccountWinsOverPrimaryAndLoggedOutIsNeverGuessed() async throws {
        let hub = FakeHub(), path = "/api/v1/lark/raw/accounts"
        hub.on("GET", path, json: #"{"activeId":"2","accounts":[{"id":"1","userId":"primary","signedIn":true,"active":false},{"id":"2","userId":"selected","signedIn":true,"active":true}]}"#)
        #expect(try await hub.client.larkAccountID() == "selected")
        hub.on("GET", path, json: #"{"activeId":"2","accounts":[{"id":"1","userId":"primary","signedIn":true,"active":true},{"id":"2","userId":"selected","signedIn":false}]}"#)
        await #expect(throws: LarkOutboxIdentity.Failure.unavailable) { try await hub.client.larkAccountID() }
    }

    @Test func changingAccountBeforeDeliveryDoesNotAttemptWrite() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store), hub = FakeHub()
        hub.on("GET", "/api/v1/lark/raw/accounts", json: #"{"activeId":"2","accounts":[{"id":"2","userId":"other-account","signedIn":true}]}"#)
        let item = LarkOutgoing(text: "stay with original account")
        try await queue.prepare()
        try await queue.enqueue([item])
        queue.deliver(item.id, using: hub.client, feed: "123") {}
        for _ in 0..<200 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.attempted == false)
        #expect(queue.items.first?.state == .failed(LarkOutboxIdentity.Failure.changed.localizedDescription))
        #expect(hub.sent("POST", "/api/v1/lark/conversations/123/messages").isEmpty)
        #expect(try await store.load()?.items.first?.attempted == false)
    }

    @Test func typingCoalescesAndSendingCannotRestoreStaleDraft() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        for value in ["a", "ab", "abc"] { queue.scheduleDraft(.init(text: value), attachments: []) }
        #expect(try await store.load()?.draft.isEmpty == true)
        await queue.flushDraft()
        #expect(try await store.load()?.draft.text == "abc")
        queue.scheduleDraft(.init(text: "sent text"), attachments: [])
        try await queue.enqueue([LarkOutgoing(text: "sent text")])
        await queue.flushDraft()
        #expect(try await store.load()?.draft.isEmpty == true)
        queue.scheduleDraft(.init(text: "next draft"), attachments: [])
        await queue.flushDraft()
        #expect(try await store.load()?.draft.text == "next draft")
        #expect(try await store.load()?.items.count == 1)
    }

    @Test func forgottenGroupCancelsLateWritesAndPreservesOtherGroups() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let scope = identity()
        let queue = LarkChatOutbox.shared(identity: scope, feed: "123", base: root)
        let other = LarkChatOutbox.shared(identity: identity(group: "other"), feed: "123", base: root)
        try await queue.prepare(); try await other.prepare()
        try await other.saveDraft(.init(text: "keep other group"), attachments: [])
        try await LarkOutboxAccounts.shared.remember(scope.account, group: scope.group, base: root)
        let item = LarkOutgoing(text: "do not recreate removed data")
        try await queue.enqueue([item])
        let transport = hub(), path = "/api/v1/lark/conversations/123/messages"
        transport.on("POST", path, json: #"{"message_id":"late"}"#)
        var client = transport.client
        let answer = try #require(client.transport)
        client.transport = { request in
            let data = try await answer(request)
            // Deliberately ignore cancellation to model a late HTTP completion.
            if request.method == "POST" { try? await Task.sleep(for: .milliseconds(100)) }
            return data
        }
        queue.deliver(item.id, using: client, feed: "123") {}
        queue.scheduleDraft(.init(text: "pending keystroke"), attachments: [])
        for _ in 0..<200 where transport.sent("POST", path).isEmpty { try await Task.sleep(for: .milliseconds(1)) }
        #expect(transport.sent("POST", path).count == 1)
        await LarkChatOutbox.forget(groups: [scope.group], base: root).value
        try await Task.sleep(for: .milliseconds(150))
        await #expect(throws: LarkOutboxIdentity.Failure.removed) { try await queue.saveDraft(.init(text: "late"), attachments: []) }
        #expect(queue.items.isEmpty && queue.invalidated)
        #expect(!FileManager.default.fileExists(atPath: LarkOutboxIdentity.groupDirectory(scope.group, base: root).path))
        #expect(other.snapshot.draft.text == "keep other group")
        let pairedAgain = LarkChatOutbox.shared(identity: scope, feed: "123", base: root)
        try await pairedAgain.prepare()
        #expect(pairedAgain !== queue && pairedAgain.snapshot.draft.isEmpty && pairedAgain.items.isEmpty)
        await LarkChatOutbox.forget(base: root).value
        #expect(!FileManager.default.fileExists(atPath: root.appending(path: "lark-outbox-v2").path))
    }

    @Test func acceptedIdlessFileCanBeRemovedWithoutRetryOrNetworkWrite() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let store = LarkOutboxStore(identity: identity(), feed: "123", base: root)
        let queue = LarkChatOutbox(store: store)
        try await queue.prepare()
        let item = LarkOutgoing(text: "", state: .sent, attachment: try await attachment(in: root))
        try await queue.enqueue([item])
        #expect(queue.items.first?.canRetry == false)
        let file = try #require(queue.items.first?.attachment?.file)
        try await queue.discard(item.id)
        #expect(try await store.load()?.items.isEmpty == true)
        #expect(!FileManager.default.fileExists(atPath: file.path))
    }

    @Test func legacyImportRequiresExplicitActionAndRetainsBytesAfterSourceRemoval() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let config = ServerConfig(url: URL(string: "https://example.invalid")!, token: "old-credential")
        let legacy = LarkOutboxStore(identity: identity(), feed: "123", base: root, legacyConfig: config)
        _ = try await legacy.save(.init(draft: .init(text: "old draft"), attachments: [try await attachment(in: root)]))
        let queue = LarkChatOutbox(store: LarkOutboxStore(identity: identity(), feed: "123", base: root))
        try await queue.prepare()
        #expect(queue.snapshot.isEmpty)
        let saved = try #require(try await legacy.load())
        try await queue.importLegacy(saved)
        try await legacy.removeImportedLegacy()
        let restored = try #require(queue.snapshot.attachments.first)
        #expect(try String(contentsOf: restored.file, encoding: .utf8) == "draft file")
        #expect(queue.snapshot.draft.text == "old draft" && queue.items.isEmpty)
        await #expect(throws: LarkOutboxStore.Failure.notReady) { try await queue.importLegacy(saved) }
    }

    @Test func lateIdentityResponseCannotRecreateForgottenAccountBinding() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let accounts = LarkOutboxAccounts()
        let revision = await accounts.revision
        try await accounts.remove(groups: [""], base: root)
        await #expect(throws: LarkOutboxIdentity.Failure.removed) {
            try await accounts.remember("old", group: "", base: root, expectedRevision: revision)
        }
        #expect(await accounts.cached(group: "", base: root) == nil)
    }

    /// A named manager's chat may be signed in as another Lark user: the user it last verified never becomes the one the
    /// default's chat opens its draft under. The default's account stays in the file it was always kept in.
    @Test func aNamedManagersAccountIsKeptApartFromTheDefaults() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let accounts = LarkOutboxAccounts()
        let group = LarkOutboxIdentity.groupDirectory("", base: root)
        try FileManager.default.createDirectory(at: group, withIntermediateDirectories: true)
        try JSON.encoder.encode("home-user").write(to: group.appending(path: "account.json")) // kept before managers
        #expect(await accounts.cached(group: "", base: root) == "home-user", "the default's is read where it always was")

        try await accounts.remember("work-user", group: "", manager: "m-2", base: root)
        #expect(await accounts.cached(group: "", base: root) == "home-user", "a named manager's chat doesn't replace it")
        #expect(await accounts.cached(group: "", manager: "m-2", base: root) == "work-user")
        #expect(await accounts.cached(group: "", manager: "m-3", base: root) == nil)

        try await accounts.remember("home-user-2", group: "", base: root)
        let kept = try JSON.decoder.decode(String.self, from: Data(contentsOf: group.appending(path: "account.json")))
        #expect(kept == "home-user-2", "the default's still writes its own file")
        try await accounts.remove(groups: [""], base: root)
        #expect(await accounts.cached(group: "", manager: "m-2", base: root) == nil, "forgotten with its group")
    }

    @Test func queuedSendUsesCurrentRouteAndCredentialWithinOriginalGroup() async throws {
        let root = try folder()
        defer { try? FileManager.default.removeItem(at: root) }
        let old = hub(), next = hub(), path = "/api/v1/lark/conversations/123/messages"
        next.on("POST", path, json: #"{"message_id":"new-route-receipt"}"#)
        let queue = LarkChatOutbox.shared(identity: identity(), feed: "123", base: root)
        try await queue.prepare()
        let item = LarkOutgoing(text: "route changed")
        try await queue.enqueue([item])
        var current = old.client
        queue.deliver(item.id, client: { current }, feed: "123") {}
        current = next.client
        current.config = ServerConfig(url: URL(string: "https://new-route.invalid")!, token: "rotated-device-credential")
        for _ in 0..<200 where queue.items.first?.state == .sending { try await Task.sleep(for: .milliseconds(10)) }
        #expect(queue.items.first?.state == .sent && queue.items.first?.messageID == "new-route-receipt")
        #expect(old.sent("POST", path).isEmpty && next.sent("POST", path).count == 1)
        await LarkChatOutbox.forget(base: root).value
    }
}
