import CryptoKit
import Foundation

// The live screen's video over UDP (#7873, docs/extensions/computer-use.md "Video over UDP (#7873)"): the phone's end
// of the packets the node's internal/modules/screen-share/screenlink sends and takes on a UDP flow. The AEAD is a `ScreenUDPCipher`:
// CryptoKit's AES-256-GCM (`ScreenUDPAESGCM`), or a test's own. spec/fixtures/screen-udp holds both ends to the same
// bytes.

public enum ScreenUDPWire {
    public static let version: UInt8 = 1
    /// Version, 8-byte flow id, u64 packet number.
    public static let headerLength = 17
    public static let tagLength = 16
    /// A packet stays inside Tailscale's 1,280-byte MTU with IPv6 and UDP headers.
    public static let maxPacket = 1200
    /// A data body's fields before its fragment: kind, channel, message offset, length, block, index, data and parity
    /// shards, flags.
    public static let dataHead = 21
    /// The most of a message one packet carries.
    public static let fragment = maxPacket - headerLength - tagLength - dataHead
    /// The most data shards in one Reed-Solomon block.
    public static let blockShards = 128
    /// The most parity per data shard the node sends, ten-thousandths.
    public static let maxParity = 4000
    /// A hello's body after its kind is at least this long, so the node's answer is never larger.
    public static let helloMin = 32
    /// A sealer stops here rather than ever reuse a nonce.
    public static let packetLimit: UInt64 = 1 << 62
    /// The TLS 1.3 exporter's label for a flow's keys; the flow id is its context.
    public static let exporterLabel = "EXPORTER-persona-screen-udp-1"
    /// Node → phone key (32) and IV (12), then phone → node key and IV.
    public static let keyMaterialLength = 88

    /// Data flags: a frame, which the phone may give up for a keyframe.
    public static let droppable: UInt8 = 1

    enum Kind: UInt8 {
        case hello = 1, data, ack, nack, skip, use, resumed, reset, ping, pong, want
    }
}

/// One direction's AEAD: seals a body under a nonce with the header as additional data (ciphertext then the 16-byte
/// tag), and opens one.
public protocol ScreenUDPCipher: Sendable {
    func seal(_ body: Data, nonce: Data, header: Data) -> Data?
    func open(_ sealed: Data, nonce: Data, header: Data) -> Data?
}

/// AES-256-GCM, as the node's crypto/cipher: the ciphertext then the 16-byte tag, the header authenticated.
public struct ScreenUDPAESGCM: ScreenUDPCipher {
    let key: SymmetricKey

    public init(key: Data) { self.key = SymmetricKey(data: key) }

    public func seal(_ body: Data, nonce: Data, header: Data) -> Data? {
        guard let n = try? AES.GCM.Nonce(data: nonce), let box = try? AES.GCM.seal(body, using: key, nonce: n, authenticating: header)
        else { return nil }
        return box.ciphertext + box.tag
    }

    public func open(_ sealed: Data, nonce: Data, header: Data) -> Data? {
        guard sealed.count >= 16, let n = try? AES.GCM.Nonce(data: nonce),
              let box = try? AES.GCM.SealedBox(nonce: n, ciphertext: sealed.dropLast(16), tag: sealed.suffix(16)) else { return nil }
        return try? AES.GCM.open(box, using: key, authenticating: header)
    }
}

/// A flow's keys split from the exporter's material: the phone opens with `toPhone` and seals with `toNode`.
public struct ScreenUDPKeyMaterial: Equatable, Sendable {
    public var toPhoneKey: Data
    public var toPhoneIV: Data
    public var toNodeKey: Data
    public var toNodeIV: Data

    public init?(_ material: Data) {
        guard material.count == ScreenUDPWire.keyMaterialLength else { return nil }
        let m = [UInt8](material)
        toPhoneKey = Data(m[0 ..< 32])
        toPhoneIV = Data(m[32 ..< 44])
        toNodeKey = Data(m[44 ..< 76])
        toNodeIV = Data(m[76 ..< 88])
    }
}

public enum ScreenUDPPacket {
    /// The nonce for packet `number`: the 12-byte IV with the number XORed into its last 8 bytes, big-endian.
    /// Returns empty data if `iv` is not 12 bytes rather than trapping out of bounds.
    public static func nonce(iv: Data, number: UInt64) -> Data {
        guard iv.count == 12 else { return Data() }
        var n = [UInt8](iv)
        for i in 0 ..< 8 { n[4 + i] ^= UInt8(truncatingIfNeeded: number >> (56 - 8 * UInt64(i))) }
        return Data(n)
    }

    public static func header(flow: Data, number: UInt64) -> Data {
        var h = Data([ScreenUDPWire.version])
        h.append(flow)
        h.append(ScreenUDPBytes.u64(number))
        return h
    }

    /// A packet's clear header: its flow id and number, or nil for something that isn't one.
    public static func read(_ packet: Data) -> (flow: Data, number: UInt64)? {
        let p = [UInt8](packet)
        guard p.count >= ScreenUDPWire.headerLength + ScreenUDPWire.tagLength + 1, p[0] == ScreenUDPWire.version else { return nil }
        return (Data(p[1 ..< 9]), ScreenUDPBytes.readU64(p, 9))
    }

    public static func seal(_ body: Data, cipher: ScreenUDPCipher, iv: Data, flow: Data, number: UInt64) -> Data? {
        guard iv.count == 12, flow.count == 8 else { return nil }
        let header = header(flow: flow, number: number)
        guard let sealed = cipher.seal(body, nonce: nonce(iv: iv, number: number), header: header) else { return nil }
        return header + sealed
    }

    /// The body of a packet whose header was read, or nil when it doesn't open (another key, or changed on the way).
    public static func open(_ packet: Data, cipher: ScreenUDPCipher, iv: Data, number: UInt64) -> Data? {
        let p = [UInt8](packet)
        guard p.count >= ScreenUDPWire.headerLength + ScreenUDPWire.tagLength + 1,
              p[0] == ScreenUDPWire.version,
              iv.count == 12 else { return nil }
        let header = Data(p[0 ..< ScreenUDPWire.headerLength])
        guard let body = cipher.open(Data(p[ScreenUDPWire.headerLength...]), nonce: nonce(iv: iv, number: number), header: header),
              !body.isEmpty else { return nil }
        return body
    }
}

/// Numbers and seals one direction's packets from 0, never repeating a number.
public struct ScreenUDPSealer: Sendable {
    public let cipher: ScreenUDPCipher
    public let iv: Data
    public let flow: Data
    public private(set) var next: UInt64

    public init(cipher: ScreenUDPCipher, iv: Data, flow: Data, next: UInt64 = 0) {
        self.cipher = cipher
        self.iv = iv
        self.flow = flow
        self.next = next
    }

    /// The sealed packet; nil once the numbers ran out (the flow must close).
    public mutating func seal(_ body: Data) -> Data? {
        guard next < ScreenUDPWire.packetLimit else { return nil }
        defer { next += 1 }
        return ScreenUDPPacket.seal(body, cipher: cipher, iv: iv, flow: flow, number: next)
    }
}

/// Accepts each packet number once: anything older than the 4,096 newest, or seen already, is a replay.
public struct ScreenUDPReplayWindow: Equatable, Sendable {
    public static let span: UInt64 = 4096
    private var top: UInt64 = 0 // the highest number accepted, +1
    private var bits = [UInt64](repeating: 0, count: 64)

    public init() {}

    public mutating func accept(_ number: UInt64) -> Bool {
        guard number < UInt64.max else { return false }
        let n = number + 1
        if n > top {
            if n - top >= Self.span {
                bits = [UInt64](repeating: 0, count: 64)
            } else {
                var i = top
                while i < n { clear(i); i += 1 }
            }
            top = n
        } else if top - n >= Self.span || has(number) {
            return false
        }
        set(number)
        return true
    }

    private func has(_ pn: UInt64) -> Bool { bits[Int((pn / 64) % 64)] & (1 << (pn % 64)) != 0 }
    private mutating func set(_ pn: UInt64) { bits[Int((pn / 64) % 64)] |= 1 << (pn % 64) }
    private mutating func clear(_ pn: UInt64) { bits[Int((pn / 64) % 64)] &= ~(1 << (pn % 64)) }
}

/// A data packet's fields.
public struct ScreenUDPFragmentHead: Equatable, Sendable {
    public var channel: UInt8
    /// The message's first byte in the channel.
    public var offset: UInt64
    public var length: UInt32
    public var block: UInt16
    /// Below `data`: a data shard; from it on, parity.
    public var index: UInt16
    public var data: UInt8
    public var parity: UInt8
    public var flags: UInt8

    public init(channel: UInt8, offset: UInt64, length: UInt32, block: UInt16, index: UInt16, data: UInt8, parity: UInt8, flags: UInt8) {
        self.channel = channel
        self.offset = offset
        self.length = length
        self.block = block
        self.index = index
        self.data = data
        self.parity = parity
        self.flags = flags
    }

    public var droppable: Bool { flags & ScreenUDPWire.droppable != 0 }
}

public struct ScreenUDPNack: Equatable, Sendable {
    public var offset: UInt64
    public var block: UInt16
    public var index: UInt16

    public init(offset: UInt64, block: UInt16, index: UInt16) {
        self.offset = offset
        self.block = block
        self.index = index
    }
}

/// A packet's body.
public enum ScreenUDPMessage: Equatable, Sendable {
    /// The phone's hello: the node's challenge it answers (0: none).
    case helloPhone(echo: UInt64)
    /// The node's: a challenge for an address not proved yet (validated false), or the proof confirmed.
    case helloNode(challenge: UInt64, validated: Bool)
    case data(ScreenUDPFragmentHead, fragment: Data)
    case ack(channel: UInt8, offset: UInt64)
    case nack(channel: UInt8, [ScreenUDPNack])
    case want(channel: UInt8, from: UInt64, to: UInt64)
    case skip(channel: UInt8, offset: UInt64)
    case use([ScreenLinkOffset])
    case resumed([ScreenLinkOffset])
    case reset(channel: UInt8, offset: UInt64)
    /// The phone's clock (µs), the parity it asks for (ten-thousandths per data shard) and its pacing (kbit/s, 0: the
    /// node's).
    case ping(micros: UInt64, parity: UInt16, paceKbps: UInt32)
    /// The ping's clock, then the node's bytes sent so far as data, parity and again (whole packets).
    case pong(micros: UInt64, dataBytes: UInt64, parityBytes: UInt64, resentBytes: UInt64)

    public enum Failure: Error, Equatable { case malformed }

    public func encoded() -> Data {
        typealias B = ScreenUDPBytes
        func kind(_ k: ScreenUDPWire.Kind) -> Data { Data([k.rawValue]) }
        switch self {
        case let .helloPhone(echo):
            return kind(.hello) + B.u64(echo) + Data(count: ScreenUDPWire.helloMin - 8)
        case let .helloNode(challenge, validated):
            return kind(.hello) + B.u64(challenge) + Data([validated ? 1 : 0])
        case let .data(h, fragment):
            return kind(.data) + Data([h.channel]) + B.u64(h.offset) + B.u32(h.length) + B.u16(h.block) + B.u16(h.index)
                + Data([h.data, h.parity, h.flags]) + fragment
        case let .ack(ch, off): return kind(.ack) + Data([ch]) + B.u64(off)
        case let .skip(ch, off): return kind(.skip) + Data([ch]) + B.u64(off)
        case let .reset(ch, off): return kind(.reset) + Data([ch]) + B.u64(off)
        case let .nack(ch, entries):
            return entries.reduce(into: kind(.nack) + Data([ch])) { $0 += B.u64($1.offset) + B.u16($1.block) + B.u16($1.index) }
        case let .want(ch, from, to): return kind(.want) + Data([ch]) + B.u64(from) + B.u64(to)
        case let .use(entries): return entries.reduce(into: kind(.use)) { $0 += Data([$1.channel]) + B.u64($1.offset) }
        case let .resumed(entries): return entries.reduce(into: kind(.resumed)) { $0 += Data([$1.channel]) + B.u64($1.offset) }
        case let .ping(micros, parity, pace): return kind(.ping) + B.u64(micros) + B.u16(parity) + B.u32(pace)
        case let .pong(micros, data, parity, resent): return kind(.pong) + B.u64(micros) + B.u64(data) + B.u64(parity) + B.u64(resent)
        }
    }

    /// A body as the phone (`fromNode` true) or the node reads it. A kind this side doesn't know is malformed: the
    /// flow's kinds are fixed by its version.
    public static func decode(_ body: Data, fromNode: Bool) throws -> ScreenUDPMessage {
        typealias B = ScreenUDPBytes
        let b = [UInt8](body)
        guard let first = b.first, let kind = ScreenUDPWire.Kind(rawValue: first) else { throw Failure.malformed }
        func need(_ n: Int) throws { if b.count < n { throw Failure.malformed } }
        func entries(from: Int, size: Int) throws -> [Int] {
            guard (b.count - from) % size == 0 else { throw Failure.malformed }
            return Array(stride(from: from, to: b.count, by: size))
        }
        switch kind {
        case .hello:
            if fromNode {
                try need(10)
                return .helloNode(challenge: B.readU64(b, 1), validated: b[9] != 0)
            }
            try need(1 + ScreenUDPWire.helloMin)
            return .helloPhone(echo: B.readU64(b, 1))
        case .data:
            try need(ScreenUDPWire.dataHead)
            let h = ScreenUDPFragmentHead(channel: b[1], offset: B.readU64(b, 2), length: B.readU32(b, 10), block: B.readU16(b, 14),
                                          index: B.readU16(b, 16), data: b[18], parity: b[19], flags: b[20])
            guard h.data > 0, Int(h.data) <= ScreenUDPWire.blockShards,
                  Int(h.data) + Int(h.parity) <= 256,
                  Int(h.index) < Int(h.data) + Int(h.parity) else { throw Failure.malformed }
            return .data(h, fragment: Data(b[ScreenUDPWire.dataHead...]))
        case .ack, .skip, .reset:
            try need(10)
            let ch = b[1], off = B.readU64(b, 2)
            return kind == .ack ? .ack(channel: ch, offset: off) : kind == .skip ? .skip(channel: ch, offset: off) : .reset(channel: ch, offset: off)
        case .nack:
            try need(2)
            return .nack(channel: b[1], try entries(from: 2, size: 12).map {
                ScreenUDPNack(offset: B.readU64(b, $0), block: B.readU16(b, $0 + 8), index: B.readU16(b, $0 + 10))
            })
        case .want:
            try need(18)
            return .want(channel: b[1], from: B.readU64(b, 2), to: B.readU64(b, 10))
        case .use, .resumed:
            let e = try entries(from: 1, size: 9).map { ScreenLinkOffset(b[$0], B.readU64(b, $0 + 1)) }
            return kind == .use ? .use(e) : .resumed(e)
        case .ping:
            try need(15)
            return .ping(micros: B.readU64(b, 1), parity: B.readU16(b, 9), paceKbps: B.readU32(b, 11))
        case .pong:
            try need(33)
            return .pong(micros: B.readU64(b, 1), dataBytes: B.readU64(b, 9), parityBytes: B.readU64(b, 17), resentBytes: B.readU64(b, 25))
        }
    }
}

/// How a message is cut into Reed-Solomon blocks: shards of `ScreenUDPWire.fragment` bytes, at most
/// `ScreenUDPWire.blockShards` to a block; a block's shards are that long, or the message's length when it is one short
/// shard.
public enum ScreenUDPLayout {
    public struct Block: Equatable, Sendable {
        /// The message's shard index of the block's first data shard.
        public var first: Int
        public var data: Int
        public var shardLength: Int
    }

    public static func blocks(length n: Int) -> [Block] {
        guard n >= 0 else { return [] }
        if n == 0 { return [Block(first: 0, data: 1, shardLength: 0)] }
        let f = ScreenUDPWire.fragment
        let shards = (n - 1) / f + 1
        return stride(from: 0, to: shards, by: ScreenUDPWire.blockShards).map { first in
            Block(first: first, data: min(ScreenUDPWire.blockShards, shards - first), shardLength: min(f, n - first * f))
        }
    }

    /// The parity shards the node sends for a block of `data` shards at `ratio` (ten-thousandths).
    public static func parity(data k: Int, ratio: Int) -> Int {
        guard ratio > 0, k > 0, k < 256 else { return 0 }
        let r = min(ratio, ScreenUDPWire.maxParity)
        return min((r * k + 9999) / 10000 + 1, 256 - k)
    }
}

enum ScreenUDPBytes {
    static func u16(_ v: UInt16) -> Data { Data([UInt8(v >> 8), UInt8(truncatingIfNeeded: v)]) }
    static func u32(_ v: UInt32) -> Data { Data((0 ..< 4).map { UInt8(truncatingIfNeeded: v >> (24 - 8 * UInt32($0))) }) }
    static func u64(_ v: UInt64) -> Data { Data((0 ..< 8).map { UInt8(truncatingIfNeeded: v >> (56 - 8 * UInt64($0))) }) }
    static func readU16(_ b: [UInt8], _ i: Int) -> UInt16 { UInt16(b[i]) << 8 | UInt16(b[i + 1]) }
    static func readU32(_ b: [UInt8], _ i: Int) -> UInt32 { (0 ..< 4).reduce(UInt32(0)) { $0 << 8 | UInt32(b[i + $1]) } }
    static func readU64(_ b: [UInt8], _ i: Int) -> UInt64 { (0 ..< 8).reduce(UInt64(0)) { $0 << 8 | UInt64(b[i + $1]) } }
}
