import Foundation

/// The #3954 live-screen video wire protocol: message framing and byte helpers, shared by the Persona Screen
/// helper and (later) the iOS app. Pure bytes — no capture, no sockets. See `design-3954.protocol.md`.
///
/// Each message on the stream is: a u32 big-endian length (the bytes after this field, i.e. 1 type byte + body),
/// a u8 type, then the body. The node and the hub relay these bytes without parsing them, so either side may add
/// message types the other skips by length.
public enum ScreenVideoWire {
    /// The protocol version the helper speaks, sent in `hello` and reported as `screen_video.version`. 0 means no video.
    public static let protocolVersion = 1

    /// A declared length over this closes the stream (design: "At most 4 MB").
    public static let maxMessageBytes = 4 * 1024 * 1024

    /// Message types. Helper → phone: 1–8. Phone → helper: 16–19.
    public enum Kind: UInt8 {
        case hello = 1
        case format = 2
        case frame = 3
        case heartbeat = 4
        case paused = 5
        case resumed = 6
        case timing = 7
        case encoding = 8
        case start = 16
        case report = 17
        case keyframe = 18
        case update = 19
    }

    /// One framed message as it comes off the wire, before its body is decoded.
    public struct RawMessage: Equatable, Sendable {
        public var type: UInt8
        public var body: Data
        public init(type: UInt8, body: Data) {
            self.type = type
            self.body = body
        }
    }

    /// Frames one message: a u32 length prefix, the type byte, then the body.
    public static func frame(_ type: UInt8, body: Data = Data()) -> Data {
        var out = Data(capacity: 5 + body.count)
        out.appendBigEndian(UInt32(1 + body.count))
        out.append(type)
        out.append(body)
        return out
    }
}

/// What can go wrong decoding the wire.
public enum ScreenVideoError: Error, Equatable {
    /// A length prefix was below 1 or above `maxMessageBytes`.
    case messageTooLong(Int)
    /// A body ended before a field it promised.
    case truncated
    /// A control body was not the JSON the type requires.
    case badJSON
}

/// Buffers stream bytes and yields whole messages as they complete. A length over `maxMessageBytes` throws, so a
/// confused or hostile peer cannot make the helper or the app allocate without bound.
public struct ScreenVideoParser {
    private var buffer: [UInt8] = []

    public init() {}

    /// Bytes of a message that hasn't come whole yet: a frame spread over several reads (the phone times large ones,
    /// `ReceiverSignals.frame(firstByteMicros:)`).
    public var pendingBytes: Int { buffer.count }

    /// Appends `bytes` and returns every message that is now complete. Call it with each chunk read from the socket.
    public mutating func push(_ bytes: Data) throws -> [ScreenVideoWire.RawMessage] {
        buffer.append(contentsOf: bytes)
        var out: [ScreenVideoWire.RawMessage] = []
        var i = 0
        while buffer.count - i >= 4 {
            let length = Int(buffer[i]) << 24 | Int(buffer[i + 1]) << 16 | Int(buffer[i + 2]) << 8 | Int(buffer[i + 3])
            if length < 1 || length > ScreenVideoWire.maxMessageBytes {
                throw ScreenVideoError.messageTooLong(length)
            }
            if buffer.count - i - 4 < length {
                break
            }
            let type = buffer[i + 4]
            let body = Data(buffer[(i + 5) ..< (i + 4 + length)])
            out.append(ScreenVideoWire.RawMessage(type: type, body: body))
            i += 4 + length
        }
        if i > 0 {
            buffer.removeFirst(i)
        }
        return out
    }
}

/// Reads fixed-width big-endian fields and byte runs from a message body, bounds-checked.
struct ByteReader {
    private let bytes: [UInt8]
    private var i = 0

    init(_ data: Data) {
        bytes = [UInt8](data)
    }

    var remaining: Int { bytes.count - i }

    mutating func u8() throws -> UInt8 {
        guard remaining >= 1 else { throw ScreenVideoError.truncated }
        defer { i += 1 }
        return bytes[i]
    }

    mutating func u16() throws -> UInt16 {
        guard remaining >= 2 else { throw ScreenVideoError.truncated }
        defer { i += 2 }
        return UInt16(bytes[i]) << 8 | UInt16(bytes[i + 1])
    }

    mutating func u32() throws -> UInt32 {
        guard remaining >= 4 else { throw ScreenVideoError.truncated }
        defer { i += 4 }
        return UInt32(bytes[i]) << 24 | UInt32(bytes[i + 1]) << 16 | UInt32(bytes[i + 2]) << 8 | UInt32(bytes[i + 3])
    }

    mutating func u64() throws -> UInt64 {
        guard remaining >= 8 else { throw ScreenVideoError.truncated }
        var v: UInt64 = 0
        for _ in 0 ..< 8 {
            v = (v << 8) | UInt64(bytes[i])
            i += 1
        }
        return v
    }

    mutating func take(_ n: Int) throws -> Data {
        guard n >= 0, remaining >= n else { throw ScreenVideoError.truncated }
        defer { i += n }
        return Data(bytes[i ..< i + n])
    }

    mutating func rest() -> Data {
        defer { i = bytes.count }
        return Data(bytes[i ..< bytes.count])
    }
}

extension Data {
    mutating func appendBigEndian(_ v: UInt16) {
        append(UInt8(truncatingIfNeeded: v >> 8))
        append(UInt8(truncatingIfNeeded: v))
    }

    mutating func appendBigEndian(_ v: UInt32) {
        append(UInt8(truncatingIfNeeded: v >> 24))
        append(UInt8(truncatingIfNeeded: v >> 16))
        append(UInt8(truncatingIfNeeded: v >> 8))
        append(UInt8(truncatingIfNeeded: v))
    }

    mutating func appendBigEndian(_ v: UInt64) {
        for shift in stride(from: 56, through: 0, by: -8) {
            append(UInt8(truncatingIfNeeded: v >> UInt64(shift)))
        }
    }
}
