import Foundation

/// One step of a two-finger scroll, sent as pixels (#7775, docs/extensions/computer-use.md "Pixel scrolling"). The phone
/// sends it inside the RFB stream to its computer's node as a client message of type 200 (unassigned in IANA's RFB
/// client-to-server registry); the node takes it out, so Screen Sharing never sees it, and hands it unchanged to the Mac's
/// Persona Input helper, which posts it as a continuous scroll event. 16 bytes, big-endian:
///
///     0      u8   type, 200
///     1      u8   version, 1
///     2      u8   kind: 0 hello (the phone can scroll by pixels; the node may ask for the helper's permission once),
///                 1 scroll
///     3      u8   scroll phase while the fingers are down: 0 none, 1 began, 2 changed, 4 ended, 8 cancelled
///     4      u8   momentum phase for the coast: 0 none, 1 begin, 2 continue, 3 end
///     5…7         reserved, 0 (read past)
///     8…11   f32  dy, Mac points; > 0 shows what is above (as the RFB wheel's up and CGEvent's first axis)
///     12…15  f32  dx, Mac points; > 0 shows what is to the left
///
/// The phases are CGScrollPhase's and CGMomentumScrollPhase's values, so the helper copies them into the event.
public struct ScrollRecord: Equatable, Sendable {
    public static let messageType: UInt8 = 200
    public static let version: UInt8 = 1
    public static let size = 16

    public enum Kind: UInt8, Sendable { case hello = 0, scroll = 1 }
    public enum Phase: UInt8, Sendable { case none = 0, began = 1, changed = 2, ended = 4, cancelled = 8 }
    public enum Momentum: UInt8, Sendable { case none = 0, begin = 1, `continue` = 2, end = 3 }

    public enum DecodeError: Error, Equatable {
        case length(Int), type(UInt8), version(UInt8), kind(UInt8), phase(UInt8), momentum(UInt8)
    }

    public var kind: Kind
    public var phase: Phase
    public var momentum: Momentum
    public var dy: Float
    public var dx: Float

    public init(kind: Kind = .scroll, phase: Phase = .none, momentum: Momentum = .none, dy: Float = 0, dx: Float = 0) {
        self.kind = kind
        self.phase = phase
        self.momentum = momentum
        self.dy = dy
        self.dx = dx
    }

    /// The phone's hello: it can scroll by pixels.
    public static let hello = ScrollRecord(kind: .hello)

    public func encoded() -> [UInt8] {
        var out: [UInt8] = [Self.messageType, Self.version, kind.rawValue, phase.rawValue, momentum.rawValue, 0, 0, 0]
        for v in [dy, dx] {
            let bits = v.bitPattern
            out += [UInt8(bits >> 24 & 0xff), UInt8(bits >> 16 & 0xff), UInt8(bits >> 8 & 0xff), UInt8(bits & 0xff)]
        }
        return out
    }

    /// Exactly one record's bytes. Another version is refused, not guessed at.
    public static func decode<C: Collection>(_ bytes: C) throws -> ScrollRecord where C.Element == UInt8 {
        let b = Array(bytes)
        guard b.count == size else { throw DecodeError.length(b.count) }
        guard b[0] == messageType else { throw DecodeError.type(b[0]) }
        guard b[1] == version else { throw DecodeError.version(b[1]) }
        guard let kind = Kind(rawValue: b[2]) else { throw DecodeError.kind(b[2]) }
        guard let phase = Phase(rawValue: b[3]) else { throw DecodeError.phase(b[3]) }
        guard let momentum = Momentum(rawValue: b[4]) else { throw DecodeError.momentum(b[4]) }
        func f32(_ at: Int) -> Float {
            Float(bitPattern: UInt32(b[at]) << 24 | UInt32(b[at + 1]) << 16 | UInt32(b[at + 2]) << 8 | UInt32(b[at + 3]))
        }
        return ScrollRecord(kind: kind, phase: phase, momentum: momentum, dy: f32(8), dx: f32(12))
    }
}

/// Records out of a byte stream that may arrive in any pieces (the helper's socket): whole ones as they complete, the
/// rest kept for the next piece.
public struct ScrollRecordReader: Sendable {
    private var pending: [UInt8] = []

    public init() {}

    public mutating func push<C: Collection>(_ bytes: C) throws -> [ScrollRecord] where C.Element == UInt8 {
        pending += bytes
        var out: [ScrollRecord] = []
        while pending.count >= ScrollRecord.size {
            out.append(try ScrollRecord.decode(pending.prefix(ScrollRecord.size)))
            pending.removeFirst(ScrollRecord.size)
        }
        return out
    }
}
