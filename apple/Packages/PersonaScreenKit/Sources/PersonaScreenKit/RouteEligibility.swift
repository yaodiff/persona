import Foundation

// Which of the phone's interfaces a candidate may be dialed on (#6398), so no traffic leaves the network it belongs
// to: a LAN address only from Wi-Fi or Ethernet on the same subnet, a Tailscale address only through the phone's own
// Tailscale tunnel. Never over cellular, another VPN or a proxy's tunnel. The app pins the connection to the interface
// this names (`NWParameters.requiredInterface`).

/// One of the phone's interfaces as eligibility needs it.
public struct PhoneInterface: Equatable, Sendable {
    public struct Address: Equatable, Sendable {
        /// 4 bytes (IPv4) or 16 (IPv6).
        public var bytes: [UInt8]
        public var prefix: Int

        /// An address written out ("192.0.2.4", "fd00::1"), nil when it isn't one.
        public init?(_ text: String, prefix: Int) {
            guard let b = IPBytes.parse(text) else { return nil }
            bytes = b
            self.prefix = prefix
        }
    }

    public var name: String
    public var via: LinkStats.Via
    public var addresses: [Address]

    public init(name: String, via: LinkStats.Via, addresses: [Address]) {
        self.name = name
        self.via = via
        self.addresses = addresses
    }
}

public enum RouteEligibility {
    /// The interface `candidate` may be dialed on, nil when it may not be dialed at all.
    public static func interface(for candidate: ScreenLinkPaths.Candidate, among interfaces: [PhoneInterface]) -> PhoneInterface? {
        guard let ip = IPBytes.parse(candidate.addr) else { return nil }
        switch LinkStats.Kind(rawValue: candidate.kind) {
        case .lan?:
            guard !IPBytes.isUnspecifiedOrLoopback(ip) else { return nil }
            return interfaces.first { i in
                (i.via == .wifi || i.via == .wired) && i.addresses.contains { a in
                    a.bytes.count == ip.count
                        && a.prefix > 0
                        && !IPBytes.isUnspecifiedOrLoopback(a.bytes)
                        && IPBytes.sameNetwork(a.bytes, ip, prefix: a.prefix)
                }
            }
        case .tailscale?:
            guard let range = IPBytes.tailscale(ip) else { return nil }
            return interfaces.first { i in
                i.name.hasPrefix("utun") && i.addresses.contains { a in
                    a.bytes.count == ip.count && IPBytes.sameNetwork(a.bytes, range.bytes, prefix: range.prefix)
                }
            }
        default:
            return nil // direct IPv6 over the internet: not in v1; an unknown kind: never
        }
    }
}

/// Address arithmetic, without Network.framework (PersonaScreenKit is Foundation only).
enum IPBytes {
    static func parse(_ text: String) -> [UInt8]? {
        guard !text.utf8.contains(0) else { return nil }
        var v4 = in_addr()
        if inet_pton(AF_INET, text, &v4) == 1 { return withUnsafeBytes(of: &v4) { Array($0) } }
        var v6 = in6_addr()
        if inet_pton(AF_INET6, text, &v6) == 1 { return withUnsafeBytes(of: &v6) { Array($0) } }
        return nil
    }

    /// Whether a and b share their first `prefix` bits.
    static func sameNetwork(_ a: [UInt8], _ b: [UInt8], prefix: Int) -> Bool {
        let (maxBits, overflow) = a.count.multipliedReportingOverflow(by: 8)
        guard !overflow, a.count == b.count, prefix >= 0, prefix <= maxBits else { return false }
        var bits = prefix
        for i in 0..<a.count where bits > 0 {
            let mask: UInt8 = bits >= 8 ? 0xff : UInt8(truncatingIfNeeded: 0xff << (8 - bits))
            if a[i] & mask != b[i] & mask { return false }
            bits -= 8
        }
        return true
    }

    /// Unspecified (`0.0.0.0/8`, `::`) or loopback (`127.0.0.0/8`, `::1`, or IPv4-mapped equivalents).
    static func isUnspecifiedOrLoopback(_ ip: [UInt8]) -> Bool {
        if ip.count == 4 {
            return ip[0] == 127 || ip[0] == 0
        }
        if ip.count == 16 {
            if ip.allSatisfy({ $0 == 0 }) { return true }
            if ip[0..<15].allSatisfy({ $0 == 0 }) && ip[15] == 1 { return true }
            if ip[0..<10].allSatisfy({ $0 == 0 }) && ip[10] == 0xff && ip[11] == 0xff {
                return isUnspecifiedOrLoopback(Array(ip[12..<16]))
            }
        }
        return false
    }

    /// Tailscale's range holding ip: 100.64.0.0/10 or fd7a:115c:a1e0::/48; nil outside both.
    static func tailscale(_ ip: [UInt8]) -> (bytes: [UInt8], prefix: Int)? {
        let v4: [UInt8] = [100, 64, 0, 0]
        let v6: [UInt8] = [0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0] + [UInt8](repeating: 0, count: 10)
        if ip.count == 4, sameNetwork(ip, v4, prefix: 10) { return (v4, 10) }
        if ip.count == 16, sameNetwork(ip, v6, prefix: 48) { return (v6, 48) }
        return nil
    }
}
