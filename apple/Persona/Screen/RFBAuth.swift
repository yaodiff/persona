import CommonCrypto
import CryptoKit
import Foundation

/// How the native client signs in to a Mac's Screen Sharing (RFBClient): the security types it speaks, and the one
/// piece of crypto each needs. macOS offers Apple Remote Desktop's (30) to an account and password; the mock's own
/// Screen Sharing offers VeNCrypt's Plain (19 then 256), which the tests use; None (1) needs nothing.
nonisolated enum RFBAuth {
    static let none: UInt8 = 1
    static let veNCrypt: UInt8 = 19
    static let ard: UInt8 = 30
    static let plain: UInt32 = 256

    /// What this client can do, in the order it takes them when the server offers several.
    static let preferred: [UInt8] = [ard, veNCrypt, none]

    enum Failure: Error, Equatable { case crypto }

    /// The longest Diffie-Hellman prime accepted, 4096 bits: macOS 26 sends that (older ones 1024).
    static let maxPrimeBytes = 512

    /// Apple Remote Desktop's sign-in (security type 30), from the server's generator, prime and public key: the 128
    /// bytes sealing the account and password, then this side's public key, to send in that order. The shared
    /// secret's MD5 is the AES-128 key (ECB); the account and password take 64 bytes each, NUL-ended, the rest random.
    /// privateKey and fill are for the tests (random otherwise). `exchange` then `seal`.
    static func ard(generator: [UInt8], prime: [UInt8], serverKey: [UInt8], account: String, password: String,
                    privateKey: [UInt8]? = nil, fill: [UInt8]? = nil) throws -> (sealed: [UInt8], publicKey: [UInt8]) {
        let k = try exchange(generator: generator, prime: prime, serverKey: serverKey, privateKey: privateKey)
        return (try seal(account: account, password: password, key: k.key, fill: fill), k.publicKey)
    }

    /// The Diffie-Hellman half of the sign-in, which needs no account or password, so the client works it out while
    /// the form waits for them (#7809): this side's public key and the AES key. The prime comes from the server: it
    /// must be odd (Montgomery's modPow traps otherwise) and at most maxPrimeBytes long (the work grows with the cube of
    /// its length). The two exponentiations run side by side.
    static func exchange(generator: [UInt8], prime: [UInt8], serverKey: [UInt8], privateKey: [UInt8]? = nil) throws
        -> (publicKey: [UInt8], key: [UInt8]) {
        guard (1...maxPrimeBytes).contains(prime.count), prime[prime.count - 1] & 1 == 1 else { throw Failure.crypto }
        let length = prime.count
        let p = RFBBigUInt(bytes: prime)
        let x = RFBBigUInt(bytes: privateKey ?? random(length))
        let bases = [RFBBigUInt(bytes: generator), RFBBigUInt(bytes: serverKey)]
        let out = Results()
        DispatchQueue.concurrentPerform(iterations: 2) { i in out.set(i, RFBBigUInt.modPow(bases[i], x, p).bytes(count: length)) }
        return (out.get(0), Array(Insecure.MD5.hash(data: out.get(1))))
    }

    /// The account and password sealed with the exchange's key.
    static func seal(account: String, password: String, key: [UInt8], fill: [UInt8]? = nil) throws -> [UInt8] {
        var block = fill ?? random(128)
        let user = Array(account.utf8.prefix(63)), pass = Array(password.utf8.prefix(63))
        block.replaceSubrange(0..<user.count, with: user)
        block[user.count] = 0
        block.replaceSubrange(64..<(64 + pass.count), with: pass)
        block[64 + pass.count] = 0
        return try aesECB(key: key, block)
    }

    /// The two exponentiations' results, written from two threads.
    private final class Results: @unchecked Sendable {
        private let lock = NSLock()
        private var values: [[UInt8]] = [[], []]
        func set(_ i: Int, _ v: [UInt8]) { lock.withLock { values[i] = v } }
        func get(_ i: Int) -> [UInt8] { lock.withLock { values[i] } }
    }

    /// VeNCrypt's Plain: the lengths, then the account and password as they are.
    static func plainCredentials(account: String, password: String) -> [UInt8] {
        let user = Array(account.utf8), pass = Array(password.utf8)
        return be32(UInt32(user.count)) + be32(UInt32(pass.count)) + user + pass
    }

    static func be32(_ v: UInt32) -> [UInt8] { [UInt8(v >> 24), UInt8(v >> 16 & 0xff), UInt8(v >> 8 & 0xff), UInt8(v & 0xff)] }

    private static func random(_ count: Int) -> [UInt8] {
        var out = [UInt8](repeating: 0, count: count)
        _ = SecRandomCopyBytes(kSecRandomDefault, count, &out)
        return out
    }

    private static func aesECB(key: [UInt8], _ data: [UInt8]) throws -> [UInt8] {
        var out = [UInt8](repeating: 0, count: data.count + kCCBlockSizeAES128)
        var moved = 0
        let status = CCCrypt(CCOperation(kCCEncrypt), CCAlgorithm(kCCAlgorithmAES), CCOptions(kCCOptionECBMode),
                             key, key.count, nil, data, data.count, &out, out.count, &moved)
        guard status == kCCSuccess else { throw Failure.crypto }
        return Array(out[0..<moved])
    }
}
