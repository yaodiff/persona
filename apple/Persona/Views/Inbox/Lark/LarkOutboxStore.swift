import Foundation
import PersonaWire

/// The composer and its outgoing rows share one atomic record: a crash cannot restore both copies of a send.
nonisolated struct LarkOutboxSnapshot: Sendable {
    var draft = LarkDraft()
    var attachments: [LarkAttachment] = []
    var items: [LarkOutgoing] = []
    var isEmpty: Bool { draft.isEmpty && attachments.isEmpty && items.isEmpty }
}

/// User-authored data belongs in Application Support, not an evictable cache or the picker's temporary directory.
/// All file work is serialized off the main actor. Tokens and absolute source paths never enter the record.
actor LarkOutboxStore {
    nonisolated let identity: LarkOutboxIdentity
    let directory: URL
    private let files = FileManager.default
    private var invalidated = false
    private let sealer: LarkOutboxSealer

    init(identity: LarkOutboxIdentity, feed: String, base: URL = URL.applicationSupportDirectory, legacyConfig: ServerConfig? = nil, sealer: LarkOutboxSealer = LarkOutboxSealer()) {
        self.sealer = sealer
        self.identity = identity
        if let legacyConfig {
            let key = legacyConfig.url.absoluteString + "\n" + legacyConfig.token + "\n" + feed
            directory = base.appending(path: "lark-outbox-v1/" + LarkOutboxIdentity.digest(key))
        } else { directory = identity.directory(feed: feed, base: base) }
    }

    func invalidate() {
        invalidated = true
    }

    func removeImportedLegacy() throws {
        guard !invalidated, directory.deletingLastPathComponent().lastPathComponent == "lark-outbox-v1" else { return }
        if files.fileExists(atPath: directory.path) { try files.removeItem(at: directory) }
    }

    enum Failure: LocalizedError {
        case version, corrupt, notReady, unconfirmedFile
        var errorDescription: String? {
            switch self {
            case .version: String(localized: "Can't read drafts on this device. Update the app and try again.")
            case .corrupt: String(localized: "Drafts on this device can't be read")
            case .notReady: String(localized: "Drafts on this device haven't been restored yet. Reload them first.")
            case .unconfirmedFile: String(localized: "Not yet confirmed whether the file was sent. Check again later.")
            }
        }
    }

    func load() throws -> LarkOutboxSnapshot? {
        guard !invalidated else { throw LarkOutboxIdentity.Failure.removed }
        let record = directory.appending(path: "state.json")
        guard files.fileExists(atPath: record.path) else { return nil }
        let bytes: Data
        do { bytes = try sealer.open(Data(contentsOf: record)) }
        catch let error as LarkOutboxSealer.Failure { throw error }
        catch { throw Failure.corrupt }
        struct Header: Decodable { var version: Int }
        let header: Header
        do { header = try JSON.decoder.decode(Header.self, from: bytes) }
        catch { throw Failure.corrupt }
        guard header.version == 1 || header.version == 2 else { throw Failure.version }
        let saved: Record
        do { saved = try JSON.decoder.decode(Record.self, from: bytes) }
        catch { throw Failure.corrupt }
        return LarkOutboxSnapshot(draft: saved.draft,
            attachments: saved.attachments.map(attachment),
            items: saved.items.map { item in
                var o = LarkOutgoing(id: item.id, text: item.text, encrypted: item.encrypted, at: item.at,
                             state: item.state == .sending
                                ? (item.attempted == false ? .failed(String(localized: "Not sent yet. Tap to retry")) : .unconfirmed) : item.state,
                             replyTo: item.replyTo, thread: item.thread, threadRoot: item.threadRoot,
                             attachment: item.attachment.map(attachment), messageID: item.messageID, attempted: item.attempted ?? true, mentions: item.mentions ?? [])
                o.firstAt = item.firstAt
                o.mode = item.mode
                o.prepared = item.prepared
                return o
            })
    }

    /// The user explicitly discards an unreadable composer. No inaccessible media backups accumulate.
    func recover() throws -> LarkOutboxSnapshot {
        guard !invalidated else { throw LarkOutboxIdentity.Failure.removed }
        if files.fileExists(atPath: directory.path) {
            try files.removeItem(at: directory)
        }
        let prefix = directory.lastPathComponent + ".recovery-"
        for backup in (try? files.contentsOfDirectory(at: directory.deletingLastPathComponent(), includingPropertiesForKeys: nil)) ?? [] {
            if backup.lastPathComponent.hasPrefix(prefix) { try files.removeItem(at: backup) }
        }
        return try save(LarkOutboxSnapshot())
    }

    /// Copy bytes before committing references; delete unreferenced bytes only after the record is committed.
    func save(_ snapshot: LarkOutboxSnapshot) throws -> LarkOutboxSnapshot {
        guard !invalidated else { throw LarkOutboxIdentity.Failure.removed }
        try prepare(directory)
        let media = directory.appending(path: "files")
        try prepare(media)
        var candidate = snapshot
        candidate.attachments = try snapshot.attachments.map { try retain($0, in: media) }
        candidate.items = try snapshot.items.map { item in
            var copy = item
            if let file = item.attachment { copy.attachment = try retain(file, in: media) }
            return copy
        }
        for item in candidate.items where item.state == .sending {
            if let file = item.attachment {
                let values = try file.file.resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey])
                guard values.isRegularFile == true, values.fileSize == file.size else { throw LarkAttachment.Failure.invalid }
            }
        }
        let record = Record(snapshot: candidate)
        var data = try JSON.encoder.encode(record)
        // Sealed when anything in it is secret: an encrypted draft or row, or a source's sealed row (its words and token).
        if candidate.draft.encryption != nil || candidate.items.contains(where: { $0.encrypted != nil || $0.mode == "sealed" }) {
            data = try sealer.seal(data)
        }
        let url = directory.appending(path: "state.json")
        #if os(iOS) || os(visionOS)
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
        #else
        try data.write(to: url, options: .atomic)
        #endif
        let referenced = Set((candidate.attachments + candidate.items.compactMap(\.attachment)).map { $0.id.uuidString })
        // Best effort: interrupted imports are harmless and cleaned on the next successful transaction.
        for child in (try? files.contentsOfDirectory(at: media, includingPropertiesForKeys: nil)) ?? [] {
            if UUID(uuidString: child.lastPathComponent) != nil, !referenced.contains(child.lastPathComponent) {
                try? files.removeItem(at: child)
            }
        }
        return candidate
    }

    private func prepare(_ url: URL) throws {
        try files.createDirectory(at: url, withIntermediateDirectories: true,
                                  attributes: [.posixPermissions: 0o700])
        var url = url
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try url.setResourceValues(values)
        #if os(iOS) || os(visionOS)
        try files.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: url.path)
        #endif
    }

    private func retain(_ source: LarkAttachment, in media: URL) throws -> LarkAttachment {
        let folder = media.appending(path: source.id.uuidString)
        let target = payload(source.id, name: source.name)
        // A restored record with missing media remains removable and editable. Sending validates its bytes.
        if source.file != target {
            try prepare(folder)
            if !files.fileExists(atPath: target.path) {
                let incoming = folder.appending(path: "incoming")
                try? files.removeItem(at: incoming)
                try files.copyItem(at: source.file, to: incoming)
                try files.moveItem(at: incoming, to: target)
                try files.setAttributes([.posixPermissions: 0o600], ofItemAtPath: target.path)
                #if os(iOS) || os(visionOS)
                try files.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: target.path)
                #endif
            }
            let size = try target.resourceValues(forKeys: [.fileSizeKey]).fileSize
            guard size == source.size else { throw LarkAttachment.Failure.invalid }
        }
        return LarkAttachment(id: source.id, kind: source.kind, file: target, name: source.name,
                              mime: source.mime, size: source.size, thumbnail: source.thumbnail, temporary: false)
    }

    private func attachment(_ value: Media) -> LarkAttachment {
        LarkAttachment(id: value.id, kind: value.kind,
                       file: payload(value.id, name: value.name),
                       name: value.name, mime: value.mime, size: value.size, thumbnail: value.thumbnail, temporary: false)
    }

    private func payload(_ id: UUID, name: String) -> URL {
        // Keep the extension for Quick Look, without trusting an imported file name as a path.
        let leaf = (name as NSString).lastPathComponent.replacingOccurrences(of: ":", with: "_")
        return directory.appending(path: "files/" + id.uuidString).appendingPathComponent("file-" + leaf)
    }

    private struct Record: Codable {
        /// modes (which reads only 1) would send them as they are.
        var version = 1
        var draft: LarkDraft
        var attachments: [Media]
        var items: [Outgoing]

        init(snapshot: LarkOutboxSnapshot) {
            draft = snapshot.draft
            attachments = snapshot.attachments.map(Media.init)
            items = snapshot.items.map(Outgoing.init)
            if items.contains(where: { $0.mode != nil }) { version = 2 }
        }
    }

    private struct Media: Codable {
        var id: UUID
        var kind: LarkAttachment.Kind
        var name: String
        var mime: String
        var size: Int
        var thumbnail: Data?

        init(_ value: LarkAttachment) {
            id = value.id; kind = value.kind; name = value.name; mime = value.mime
            size = value.size; thumbnail = value.thumbnail
        }
    }

    private struct Outgoing: Codable {
        var id: UUID
        var text: String
        var encrypted: LarkEncryptedPayload?
        var at: Date
        var state: LarkOutgoing.State
        var replyTo: LarkReplyTo?
        var thread: Bool
        var threadRoot: String?
        var attachment: Media?
        var messageID: String?
        var mentions: [LarkMention]?
        var attempted: Bool?
        var firstAt: Date?
        var mode: String?
        var prepared: String?

        init(_ value: LarkOutgoing) {
            mode = value.mode
            prepared = value.prepared
            encrypted = value.encrypted
            id = value.id; text = value.text; at = value.at; state = value.state; replyTo = value.replyTo
            thread = value.thread; threadRoot = value.threadRoot
            attachment = value.attachment.map(Media.init); messageID = value.messageID
            mentions = value.mentions.isEmpty ? nil : value.mentions
            attempted = value.attempted
            firstAt = value.firstAt
        }
    }
}
