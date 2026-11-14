import Foundation
import PersonaWire

// Clipboard sharing between the phone and a computer (#3064): the hub relays to the computer's node, which reads and
// writes its system clipboard. Plain data types, compiled into every target (watchOS 10 included).

/// Whether a computer can share its clipboard (`Machine.clipboard`; docs/operations/clipboard.md): `supported` says its
/// node can reach a clipboard at all, `enabled` is the user's per-computer switch (off by default), `reason` says why not
/// in the reader's language and `reasonCode` the same as a stable word. A hub without the feature sends no `clipboard`:
/// nil, which reads as unsupported and off everywhere.
nonisolated struct MachineClipboard: Codable, Hashable, Sendable {
    var supported = false
    var enabled = false
    var reason: String? = nil
    /// `no_display`, `no_tool`, `no_session`, `os`, `node_outdated`, or a word this app does not know.
    var reasonCode: String? = nil

    enum CodingKeys: String, CodingKey {
        case supported, enabled, reason
        case reasonCode = "reason_code"
    }

    init(supported: Bool = false, enabled: Bool = false, reason: String? = nil, reasonCode: String? = nil) {
        self.supported = supported
        self.enabled = enabled
        self.reason = reason
        self.reasonCode = reasonCode
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        supported = (try? c.decodeIfPresent(Bool.self, forKey: .supported)) ?? false
        enabled = (try? c.decodeIfPresent(Bool.self, forKey: .enabled)) ?? false
        reason = try? c.decodeIfPresent(String.self, forKey: .reason)
        reasonCode = try? c.decodeIfPresent(String.self, forKey: .reasonCode)
    }

    /// Supported and turned on: Send and Get can be used.
    var usable: Bool { supported && enabled }

    /// Why it can't, in words: the hub's own, else by its code (a hub that sent only the code), else nil.
    var why: String? {
        if let reason, !reason.isEmpty { return reason }
        switch reasonCode {
        case "no_display": return String(localized: "This computer has no desktop session, so it has no clipboard.")
        case "no_tool": return String(localized: "This computer has no clipboard tool installed (wl-clipboard or xclip).")
        case "no_session": return String(localized: "Persona Link runs outside the desktop session on this computer.")
        case "os": return String(localized: "Clipboard sharing isn't available on this operating system.")
        case "node_outdated": return String(localized: "Update Persona Link on this computer to share its clipboard.")
        default: return nil
        }
    }
}

/// What one clipboard may carry to the hub (docs/operations/clipboard.md): checked here before sending, so a too big
/// clipboard is said at once instead of after the upload (the hub answers 413 `too_large` past them).
nonisolated enum ClipboardLimits {
    /// Text, in UTF-8 bytes.
    static let textBytes = 4 << 20
    /// An image's bytes (before base64).
    static let imageBytes = 7 << 20
    /// JPEG qualities tried, in order, for an image whose PNG is too big.
    static let jpegQualities: [Double] = [0.85, 0.7, 0.5]

    /// Whether the hub would refuse it as too large.
    static func tooLarge(_ content: ClipboardContent) -> Bool {
        switch content.kind {
        case .text: (content.text?.utf8.count ?? 0) > textBytes
        case .image: (content.image.map { decodedSize($0.data) } ?? 0) > imageBytes
        case .empty, .concealed: false
        }
    }

    /// The bytes behind a base64 string, without decoding it.
    static func decodedSize(_ base64: String) -> Int {
        let padding = base64.hasSuffix("==") ? 2 : base64.hasSuffix("=") ? 1 : 0
        return max(0, base64.utf8.count / 4 * 3 - padding)
    }
}

/// An image on a clipboard, as the hub carries it: its MIME type and the bytes in base64.
nonisolated struct ClipboardImage: Codable, Hashable, Sendable {
    var mime: String
    var data: String

    init(mime: String, data: String) {
        self.mime = mime
        self.data = data
    }

    init(mime: String, bytes: Data) {
        self.init(mime: mime, data: bytes.base64EncodedString())
    }

    var bytes: Data? { Data(base64Encoded: data) }
}

/// What a computer's clipboard holds (`GET /api/v1/machines/{mid}/clipboard`). `concealed`: a password manager marked it
/// hidden, so the node does not hand it out.
nonisolated struct ClipboardContent: Codable, Hashable, Sendable {
    nonisolated enum Kind: String, Codable, Sendable {
        case text, image, empty, concealed
    }

    var kind: Kind
    var text: String? = nil
    var image: ClipboardImage? = nil

    init(kind: Kind, text: String? = nil, image: ClipboardImage? = nil) {
        self.kind = kind
        self.text = text
        self.image = image
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        text = try? c.decodeIfPresent(String.self, forKey: .text)
        image = try? c.decodeIfPresent(ClipboardImage.self, forKey: .image)
        // A kind this app does not know reads by what came with it.
        let raw = (try? c.decodeIfPresent(String.self, forKey: .kind)) ?? ""
        kind = Kind(rawValue: raw) ?? (image != nil ? .image : text != nil ? .text : .empty)
    }

    static func text(_ s: String) -> ClipboardContent { ClipboardContent(kind: .text, text: s) }
    static func image(_ i: ClipboardImage) -> ClipboardContent { ClipboardContent(kind: .image, image: i) }
}

/// Why a clipboard call did not go through, from the hub's answer (docs/operations/clipboard.md): the switch is off (403
/// `clipboard_disabled`), this device may not (403 `read_only`), the computer cannot (409 `clipboard_unsupported`), it is
/// offline (503), too big (413 `too_large`, or checked here first), an image the hub could not read (400
/// `clipboard_bad_image`), the computer could not read or write it (502 `clipboard_failed`), or something else (its own
/// words).
nonisolated enum ClipboardFailure: Equatable, Sendable {
    case disabled
    case readOnly
    case unsupported(String?)
    case offline
    case tooLarge
    case badImage
    case failed(String)
    case other(String)

    static let disabledCode = "clipboard_disabled"
    static let readOnlyCode = "read_only"
    static let unsupportedCode = "clipboard_unsupported"
    static let tooLargeCode = "too_large"
    static let badImageCode = "clipboard_bad_image"
    static let failedCode = "clipboard_failed"

    init(_ error: Error) {
        if let api = error as? APIError {
            switch api.code {
            case Self.readOnlyCode:
                self = .readOnly
                return
            case Self.tooLargeCode:
                self = .tooLarge
                return
            case Self.badImageCode:
                self = .badImage
                return
            case Self.failedCode:
                self = .failed(api.message)
                return
            default: break
            }
            if api.status == 413 {
                self = .tooLarge
                return
            }
            if api.code == Self.disabledCode || (api.status == 403 && api.code == nil) {
                self = .disabled
                return
            }
            if api.code == Self.unsupportedCode || api.status == 409 {
                self = .unsupported(api.fields["reason"] ?? (api.message.isEmpty ? nil : api.message))
                return
            }
            if api.status == 503 {
                self = .offline
                return
            }
        }
        self = .other(error.localizedDescription)
    }

    /// One sentence for a toast or Siri.
    func sentence(machine name: String) -> String {
        switch self {
        case .disabled: String(localized: "Clipboard sharing is off for \(name). Turn it on on the computer's page in Persona.")
        case .unsupported(let reason?): String(localized: "\(name) can't share its clipboard: \(reason)")
        case .unsupported(nil): String(localized: "\(name) can't share its clipboard.")
        case .offline: String(localized: "\(name) is offline: it may be asleep, or Persona Link isn't running on it.")
        case .readOnly: String(localized: "This device can't use clipboard sharing. Use your phone or a signed-in computer.")
        case .tooLarge: String(localized: "Too large to send: text up to 4 MB, images up to 7 MB.")
        case .badImage: String(localized: "This image couldn't be read. Copy it again, or save it and send it as a file.")
        case .failed(let message) where !message.isEmpty: String(localized: "\(name) couldn't use its clipboard: \(message)")
        case .failed: String(localized: "\(name) couldn't use its clipboard. Try again.")
        case .other(let message): message
        }
    }
}

extension APIClient {
    /// The computer's clipboard now (text, an image, or why there is nothing).
    func clipboard(machine: String) async throws -> ClipboardContent {
        try JSON.decoder.decode(ClipboardContent.self, from: await request("GET", "/api/v1/machines/\(machine)/clipboard", timeout: 60))
    }

    /// Replaces the computer's clipboard with text or an image (PNG or JPEG; 204). Too large is refused here, before
    /// anything is sent, as the hub would refuse it (413 `too_large`).
    func setClipboard(machine: String, _ content: ClipboardContent) async throws {
        if ClipboardLimits.tooLarge(content) {
            throw APIError(status: 413, message: ClipboardFailure.tooLarge.sentence(machine: machine),
                           fields: ["code": ClipboardFailure.tooLargeCode])
        }
        struct Body: Encodable {
            var text: String?
            var image: ClipboardImage?
        }
        let body = content.kind == .image ? Body(text: nil, image: content.image) : Body(text: content.text ?? "", image: nil)
        _ = try await request("PUT", "/api/v1/machines/\(machine)/clipboard", body: body, timeout: 60)
    }

    /// The computer's clipboard state now (`Machine.clipboard`), for a page opened before the machine list caught up.
    func clipboardSettings(machine: String) async throws -> MachineClipboard {
        try JSON.decoder.decode(MachineClipboard.self, from: await request("GET", "/api/v1/machines/\(machine)/clipboard/settings"))
    }

    /// The computer page's switch: lets this computer share its clipboard, or stops it. The hub pushes `machines` too.
    func setClipboardEnabled(machine: String, enabled: Bool) async throws -> MachineClipboard {
        struct Body: Encodable { var enabled: Bool }
        return try JSON.decoder.decode(MachineClipboard.self,
                                       from: await request("PUT", "/api/v1/machines/\(machine)/clipboard/settings", body: Body(enabled: enabled)))
    }
}
