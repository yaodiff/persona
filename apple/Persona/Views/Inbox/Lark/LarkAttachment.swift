import Foundation
import ImageIO
import UniformTypeIdentifiers
import PersonaWire

/// Owns a private snapshot: picker access can end and the original can change without affecting a retry.
/// Temporary snapshots follow their last reference; durable copies are collected by LarkOutboxStore.
nonisolated final class LarkAttachment: Identifiable, Equatable, Sendable {
    static let fileLimit = 100 << 20
    static let imageLimit = 15 << 19
    static let selectionLimit = 4
    static let staging = LarkAttachmentStaging()
    /// Allow a bounded file upload on a slow uplink, with one minute for the response.
    static func uploadTimeout(bytes: Int) -> TimeInterval { max(180, TimeInterval(min(max(bytes, 0), fileLimit) / (128 << 10)) + 60) }
    enum Kind: String, Codable, Sendable { case image, file }
    let id: UUID
    let kind: Kind
    let file: URL
    let name: String
    let mime: String
    let size: Int
    let thumbnail: Data?
    private let temporary: Bool

    init(id: UUID = UUID(), kind: Kind, file: URL, name: String, mime: String, size: Int, thumbnail: Data? = nil,
         temporary: Bool = true) {
        self.id = id; self.temporary = temporary
        self.kind = kind; self.file = file; self.name = name; self.mime = mime; self.size = size; self.thumbnail = thumbnail
    }
    static func == (lhs: LarkAttachment, rhs: LarkAttachment) -> Bool { lhs.id == rhs.id }
    deinit { if temporary { try? FileManager.default.removeItem(at: file.deletingLastPathComponent()) } }

    enum Failure: LocalizedError {
        case invalid, empty, tooLarge, imageTooLarge
        var errorDescription: String? {
            switch self {
            case .invalid: String(localized: "Couldn't read the attachment. Choose it again.")
            case .empty: String(localized: "Can't send an empty file.")
            case .tooLarge: String(localized: "Files can't be larger than 100 MiB.")
            case .imageTooLarge: String(localized: "This image is too large. Choose a smaller one.")
            }
        }
    }

    static func stageFile(_ source: URL) async throws -> LarkAttachment {
        let scoped = source.startAccessingSecurityScopedResource()
        defer { if scoped { source.stopAccessingSecurityScopedResource() } }
        let values = try source.resourceValues(forKeys: [.isRegularFileKey, .fileSizeKey, .contentTypeKey])
        guard values.isRegularFile == true else { throw Failure.invalid }
        guard let size = values.fileSize, size > 0 else { throw Failure.empty }
        guard size <= fileLimit else { throw Failure.tooLarge }
        let copy = try await FileUploads.stage(source, directory: stagingDirectory())
        do {
            try Task.checkCancellation()
            let bytes = (try FileManager.default.attributesOfItem(atPath: copy.path)[.size] as? NSNumber)?.intValue ?? 0
            guard bytes > 0 else { throw Failure.empty }
            guard bytes <= fileLimit else { throw Failure.tooLarge }
            return LarkAttachment(kind: .file, file: copy, name: source.lastPathComponent,
                                  mime: values.contentType?.preferredMIMEType ?? "application/octet-stream", size: bytes)
        } catch {
            try? FileManager.default.removeItem(at: copy.deletingLastPathComponent())
            throw error
        }
    }

    /// Decode off the main actor, apply orientation, resize, and remove photo location metadata.
    static func stagePhoto(_ data: Data) async throws -> LarkAttachment {
        try await Task.detached(priority: .userInitiated) {
            guard let source = CGImageSourceCreateWithData(data as CFData, nil) else { throw Failure.invalid }
            func jpeg(maxPixel: Int, quality: Double) throws -> Data {
                let options: [CFString: Any] = [kCGImageSourceCreateThumbnailFromImageAlways: true,
                    kCGImageSourceCreateThumbnailWithTransform: true, kCGImageSourceThumbnailMaxPixelSize: maxPixel]
                guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else { throw Failure.invalid }
                let bytes = NSMutableData()
                guard let dest = CGImageDestinationCreateWithData(bytes, UTType.jpeg.identifier as CFString, 1, nil) else { throw Failure.invalid }
                CGImageDestinationAddImage(dest, image, [kCGImageDestinationLossyCompressionQuality: quality] as CFDictionary)
                guard CGImageDestinationFinalize(dest) else { throw Failure.invalid }
                return bytes as Data
            }
            let bytes = try jpeg(maxPixel: 2560, quality: 0.88)
            guard bytes.count <= imageLimit else { throw Failure.imageTooLarge }
            let thumb = try jpeg(maxPixel: 320, quality: 0.8)
            let dir = try stagingDirectory()
            let file = dir.appending(path: "photo.jpg")
            do { try bytes.write(to: file, options: .atomic) }
            catch { try? FileManager.default.removeItem(at: dir); throw error }
            return LarkAttachment(kind: .image, file: file, name: String(localized: "Photo.jpg"), mime: "image/jpeg", size: bytes.count, thumbnail: thumb)
        }.value
    }

    private static func stagingDirectory() throws -> URL {
        staging.sweep()
        var dir = staging.root.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var values = URLResourceValues()
        values.isExcludedFromBackup = true
        try dir.setResourceValues(values)
        #if os(iOS) || os(visionOS)
        try FileManager.default.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication], ofItemAtPath: dir.path)
        #endif
        return dir
    }

    func imagePayload() async throws -> ImagePayload {
        let file = file, mime = mime
        return try await Task.detached { ImagePayload(mime: mime, data: try Data(contentsOf: file).base64EncodedString()) }.value
    }
}

nonisolated struct LarkSendReceipt: Decodable, Sendable {
    var messageID: String?
    enum CodingKeys: String, CodingKey { case messageID = "message_id" }

    enum Failure: LocalizedError {
        case missing
        var errorDescription: String? { String(localized: "No valid send result yet. Check again later.") }
    }
}

extension APIClient {
    /// `replyTo`: the message it quotes, or with `thread` the one it answers in its thread (`threadRoot`, the thread's
    /// first message, places a file there). Only when the hub says `lark_attachment_reply` (#1035): an older plugin
    /// drops these and sends the attachment on its own.
    func larkSendAttachment(_ feed: String, attachment: LarkAttachment, clientID: String, replyTo: String? = nil,
                            thread: Bool = false, threadRoot: String? = nil) async throws -> LarkSendReceipt {
        let root = "/api/v1/lark/conversations/\(feed)"
        var receipt: LarkSendReceipt
        if let source = messagingSource {
            var query = messagingQuery(["client_id": clientID, "name": attachment.name, "mime": attachment.mime,
                                        "kind": attachment.kind == .image ? "image" : "file"])
            if let replyTo { query["reply_to"] = replyTo }
            if replyTo != nil, thread { query["thread"] = "1" }
            let data = try await messagingCall {
                try await uploadBytes(file: attachment.file, path: messagingPath(source, "conversations/\(feed)/attachments"), query: query,
                                      timeout: LarkAttachment.uploadTimeout(bytes: attachment.size))
            }
            receipt = try JSON.decoder.decode(LarkSendReceipt.self, from: data)
        } else {
        switch attachment.kind {
        case .image:
            struct Body: Encodable {
                var client_id: String; var name: String; var mime: String; var data: Data
                var reply_to: String?; var thread: Bool?
            }
            let bytes = try Data(contentsOf: attachment.file)
            receipt = try await post(root + "/images", body: Body(client_id: clientID, name: attachment.name, mime: attachment.mime, data: bytes,
                                                                  reply_to: replyTo, thread: replyTo != nil && thread ? true : nil), timeout: 180)
        case .file:
            var query = ["client_id": clientID, "name": attachment.name, "mime": attachment.mime]
            if let replyTo { query["reply_to"] = replyTo }
            if replyTo != nil, thread { query["thread_root"] = threadRoot ?? replyTo }
            let data = try await uploadBytes(file: attachment.file, path: root + "/files", query: query,
                                            timeout: LarkAttachment.uploadTimeout(bytes: attachment.size))
            receipt = try JSON.decoder.decode(LarkSendReceipt.self, from: data)
        }
        }
        // A successful file upload may omit message_id. It is already accepted and must never be offered for retry.
        if attachment.kind == .file, receipt.messageID?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false {
            receipt.messageID = nil
            return receipt
        }
        guard let id = receipt.messageID, !id.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              attachment.kind != .file || !id.hasPrefix("local:") else { throw LarkSendReceipt.Failure.missing }
        return receipt
    }
}

/// Picker staging is never referenced by a durable record: the outbox copies payloads to its own directory.
/// Sweep once before any new staging, including when startup cleanup races with the first picker.
nonisolated final class LarkAttachmentStaging: @unchecked Sendable {
    let root: URL
    private let lock = NSLock()
    private var swept = false

    init(root: URL = URL.applicationSupportDirectory.appending(path: "lark-attachment-staging")) { self.root = root }

    func sweep() {
        lock.withLock {
            guard !swept else { return }
            swept = true
            let files = FileManager.default
            for child in (try? files.contentsOfDirectory(at: root, includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey])) ?? [] {
                guard UUID(uuidString: child.lastPathComponent) != nil,
                      let values = try? child.resourceValues(forKeys: [.isDirectoryKey, .isSymbolicLinkKey]),
                      values.isDirectory == true, values.isSymbolicLink != true else { continue }
                try? files.removeItem(at: child)
            }
        }
    }
}
