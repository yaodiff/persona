import Foundation

// The live screen's session wire (#6398, docs/extensions/computer-use.md "Live screen routes (#6398)"): what the phone
// and the node's internal/screenlink say on every transport of a screen session, the hub's relays and the direct
// routes alike. The same framing as the video wire (u32 big-endian length, u8 type, body), so `ScreenVideoParser`
// splits it too; the channels' own bytes (RFB, the video wire) travel inside `data`. spec/fixtures/screen-link holds
// byte-exact cases both sides check.

public enum ScreenLinkWire {
    /// The WebSocket subprotocol the phone offers on the hub's two screen routes to ask for a session.
    public static let subprotocol = "persona-screen-link.1"
    /// The TLS application protocol of a direct connection.
    public static let alpn = "persona-screen/1"
    /// Channels: RFB is Screen Sharing's stream; each video stream is a channel of its own from `firstVideo` up.
    public static let rfb: UInt8 = 1
    public static let firstVideo: UInt8 = 2

    public enum Kind: UInt8 {
        case data = 1, ack, paths, attach, attached, ping, pong, use, resumed, close, reset
    }
}

/// What the node says about a session's routes: the token a transport attaches with and, when it has direct routes,
/// its candidate addresses, the port they listen on and the certificate's SHA-256 (base64url, no padding) to pin.
public struct ScreenLinkPaths: Codable, Equatable, Sendable {
    public struct Candidate: Codable, Equatable, Hashable, Sendable {
        /// `lan` or `tailscale` (`LinkStats.Kind`'s raw value).
        public var kind: String
        public var addr: String
        /// The node's prefix length for a LAN address, so the phone can tell whether it shares the network.
        public var prefix: Int?

        public init(kind: String, addr: String, prefix: Int? = nil) {
            self.kind = kind
            self.addr = addr
            self.prefix = prefix
        }
    }

    public var version: Int
    public var token: String
    public var cert: String?
    public var port: Int?
    public var candidates: [Candidate]
    /// Why there are no direct routes, when the node knows: `firewall`.
    public var unavailable: String?

    public init(version: Int = 1, token: String, cert: String? = nil, port: Int? = nil, candidates: [Candidate] = [],
                unavailable: String? = nil) {
        self.version = version
        self.token = token
        self.cert = cert
        self.port = port
        self.candidates = candidates
        self.unavailable = unavailable
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try c.decode(Int.self, forKey: .version)
        token = try c.decode(String.self, forKey: .token)
        cert = try c.decodeIfPresent(String.self, forKey: .cert)
        port = try c.decodeIfPresent(Int.self, forKey: .port)
        candidates = try c.decodeIfPresent([Candidate].self, forKey: .candidates) ?? []
        unavailable = try c.decodeIfPresent(String.self, forKey: .unavailable)
    }
}

/// One channel's offset in `use` and `resumed`.
public struct ScreenLinkOffset: Equatable, Sendable {
    public var channel: UInt8
    public var offset: UInt64

    public init(_ channel: UInt8, _ offset: UInt64) {
        self.channel = channel
        self.offset = offset
    }
}

/// One session message.
public enum ScreenLinkMessage: Equatable, Sendable {
    /// A channel's bytes from `offset` on.
    case data(channel: UInt8, offset: UInt64, bytes: Data)
    /// Everything of the channel up to `offset` arrived.
    case ack(channel: UInt8, offset: UInt64)
    case paths(ScreenLinkPaths)
    /// The first message of a transport joining a live session.
    case attach(token: String)
    case attached
    /// The phone's monotonic clock, echoed in the pong.
    case ping(UInt64)
    /// The ping's value, and the TCP packets the node retransmitted and sent on this transport since its last pong
    /// (0, 0 on a relay: unknown).
    case pong(UInt64, retransmitted: UInt32, sent: UInt32)
    /// Carry these channels on this transport from these offsets (what the phone has).
    case use([ScreenLinkOffset])
    /// What the node has of each channel: the phone sends on from there. Also a new video relay's first message, naming
    /// its channel at 0.
    case resumed([ScreenLinkOffset])
    /// The channel ends at `offset`.
    case close(channel: UInt8, offset: UInt64)
    /// The video channel goes on at `offset`, a message boundary: a partial message held is dropped.
    case reset(channel: UInt8, offset: UInt64)
    case unknown(type: UInt8, body: Data)

    /// The message with its length prefix, ready to send.
    public func framed() -> Data {
        switch self {
        case let .data(ch, off, bytes): return Self.frame(.data, Self.head(ch, off) + bytes)
        case let .ack(ch, off): return Self.frame(.ack, Self.head(ch, off))
        case let .paths(p): return Self.frame(.paths, (try? JSONEncoder().encode(p)) ?? Data())
        case let .attach(token): return Self.frame(.attach, (try? JSONEncoder().encode(["token": token])) ?? Data())
        case .attached: return Self.frame(.attached, Data())
        case let .ping(t): return Self.frame(.ping, Self.u64(t))
        case let .pong(t, r, s): return Self.frame(.pong, Self.u64(t) + Self.u32(r) + Self.u32(s))
        case let .use(entries): return Self.frame(.use, entries.reduce(into: Data()) { $0 += Self.head($1.channel, $1.offset) })
        case let .resumed(entries): return Self.frame(.resumed, entries.reduce(into: Data()) { $0 += Self.head($1.channel, $1.offset) })
        case let .close(ch, off): return Self.frame(.close, Self.head(ch, off))
        case let .reset(ch, off): return Self.frame(.reset, Self.head(ch, off))
        case let .unknown(type, body): return ScreenVideoWire.frame(type, body: body)
        }
    }

    /// A framed message's type and body (as `ScreenVideoParser` splits them) as a message. A type this side doesn't
    /// know is `.unknown`, skipped by the caller.
    public static func decode(type: UInt8, body: Data) throws -> ScreenLinkMessage {
        guard let kind = ScreenLinkWire.Kind(rawValue: type) else { return .unknown(type: type, body: body) }
        let b = [UInt8](body)
        switch kind {
        case .data:
            let (ch, off) = try head(b)
            return .data(channel: ch, offset: off, bytes: Data(b[9...]))
        case .ack:
            let (ch, off) = try head(b)
            return .ack(channel: ch, offset: off)
        case .close:
            let (ch, off) = try head(b)
            return .close(channel: ch, offset: off)
        case .reset:
            let (ch, off) = try head(b)
            return .reset(channel: ch, offset: off)
        case .paths:
            do { return .paths(try JSONDecoder().decode(ScreenLinkPaths.self, from: body)) } catch { throw ScreenVideoError.badJSON }
        case .attach:
            guard let token = (try? JSONDecoder().decode([String: String].self, from: body))?["token"] else {
                throw ScreenVideoError.badJSON
            }
            return .attach(token: token)
        case .attached:
            return .attached
        case .ping:
            guard b.count >= 8 else { throw ScreenVideoError.truncated }
            return .ping(readU64(b, 0))
        case .pong:
            guard b.count >= 16 else { throw ScreenVideoError.truncated }
            return .pong(readU64(b, 0), retransmitted: readU32(b, 8), sent: readU32(b, 12))
        case .use, .resumed:
            guard b.count % 9 == 0 else { throw ScreenVideoError.truncated }
            let entries = stride(from: 0, to: b.count, by: 9).map { ScreenLinkOffset(b[$0], readU64(b, $0 + 1)) }
            return kind == .use ? .use(entries) : .resumed(entries)
        }
    }

    private static func frame(_ kind: ScreenLinkWire.Kind, _ body: Data) -> Data { ScreenVideoWire.frame(kind.rawValue, body: body) }

    private static func head(_ ch: UInt8, _ off: UInt64) -> Data { Data([ch]) + u64(off) }

    private static func head(_ b: [UInt8]) throws -> (UInt8, UInt64) {
        guard b.count >= 9 else { throw ScreenVideoError.truncated }
        return (b[0], readU64(b, 1))
    }

    private static func u64(_ v: UInt64) -> Data { Data((0..<8).map { UInt8(truncatingIfNeeded: v >> (56 - 8 * $0)) }) }
    private static func u32(_ v: UInt32) -> Data { Data((0..<4).map { UInt8(truncatingIfNeeded: v >> (24 - 8 * $0)) }) }

    private static func readU64(_ b: [UInt8], _ at: Int) -> UInt64 { (0..<8).reduce(UInt64(0)) { $0 << 8 | UInt64(b[at + $1]) } }
    private static func readU32(_ b: [UInt8], _ at: Int) -> UInt32 { (0..<4).reduce(UInt32(0)) { $0 << 8 | UInt32(b[at + $1]) } }
}
