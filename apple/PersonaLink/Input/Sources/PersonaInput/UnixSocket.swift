import Foundation
#if canImport(Darwin)
import Darwin
#endif

/// The node's per-session Unix socket (0600, made for this helper, gone once it connected): connect, say the token,
/// then read and write bytes until the node closes it. As Persona Screen's, kept apart so the two helpers share no code
/// whose change would change the other's binary.
final class UnixSocket {
    private let fd: Int32

    private init(fd: Int32) { self.fd = fd }

    enum Failure: Error { case open(Int32), connect(Int32), write(Int32), read(Int32), pathTooLong }

    static func connect(path: String) throws -> UnixSocket {
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw Failure.open(errno) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let capacity = MemoryLayout.size(ofValue: addr.sun_path)
        let bytes = Array(path.utf8)
        guard bytes.count < capacity else {
            Darwin.close(fd)
            throw Failure.pathTooLong
        }
        withUnsafeMutablePointer(to: &addr.sun_path) { tuple in
            tuple.withMemoryRebound(to: UInt8.self, capacity: capacity) { dst in
                for (i, b) in bytes.enumerated() { dst[i] = b }
                dst[bytes.count] = 0
            }
        }
        let size = socklen_t(MemoryLayout<sockaddr_un>.size)
        let result = withUnsafePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, size) }
        }
        guard result == 0 else {
            let code = errno
            Darwin.close(fd)
            throw Failure.connect(code)
        }
        return UnixSocket(fd: fd)
    }

    func write(_ bytes: [UInt8]) throws {
        var at = 0
        while at < bytes.count {
            let n = bytes[at...].withUnsafeBytes { Darwin.write(fd, $0.baseAddress, $0.count) }
            if n < 0 {
                if errno == EINTR { continue }
                throw Failure.write(errno)
            }
            at += n
        }
    }

    /// What came next (empty once the node closed it).
    func read() throws -> [UInt8] {
        var buffer = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = buffer.withUnsafeMutableBytes { Darwin.read(fd, $0.baseAddress, $0.count) }
            if n < 0 {
                if errno == EINTR { continue }
                throw Failure.read(errno)
            }
            return Array(buffer[0..<n])
        }
    }

    func close() { Darwin.close(fd) }
}
