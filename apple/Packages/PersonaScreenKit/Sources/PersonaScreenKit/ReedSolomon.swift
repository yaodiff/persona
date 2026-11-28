import Foundation

/// Reed-Solomon erasure coding for the video over UDP (#7873): the node's `internal/modules/screen-share/screenlink` rs.go, the same code.
/// Systematic, over GF(2⁸) with x⁸ + x⁴ + x³ + x² + 1; parity row i, data column j of a block with k data shards is
/// 1 / ((k + i) XOR j), a Cauchy matrix, so any k of a block's k + m shards rebuild it.
public enum ReedSolomon {
    public enum Failure: Error, Equatable { case tooFewShards, badBlock }

    static let exp: [UInt8] = {
        var e = [UInt8](repeating: 0, count: 510)
        var x = 1
        for i in 0 ..< 255 {
            e[i] = UInt8(x)
            e[i + 255] = UInt8(x)
            x <<= 1
            if x & 0x100 != 0 { x ^= 0x11d }
        }
        return e
    }()

    static let log: [Int] = {
        var l = [Int](repeating: 0, count: 256)
        for i in 0 ..< 255 { l[Int(exp[i])] = i }
        return l
    }()

    @inline(__always) static func mul(_ a: UInt8, _ b: UInt8) -> UInt8 {
        a == 0 || b == 0 ? 0 : exp[log[Int(a)] + log[Int(b)]]
    }

    @inline(__always) static func inv(_ a: UInt8) -> UInt8 { a == 0 ? 0 : exp[255 - log[Int(a)]] }

    static func coefficient(k: Int, row i: Int, column j: Int) -> UInt8 { inv(UInt8(k + i) ^ UInt8(j)) }

    /// The 256 products of c, for a whole shard at a time.
    static func table(_ c: UInt8) -> [UInt8] { (0 ..< 256).map { mul(c, UInt8($0)) } }

    /// m parity shards for `data`, k shards of one length.
    public static func encode(_ data: [[UInt8]], parity m: Int) throws -> [[UInt8]] {
        let k = data.count
        guard k > 0, k <= 256, m >= 0, m <= 256 - k, let n = data.first?.count,
              data.allSatisfy({ $0.count == n }) else { throw Failure.badBlock }
        return (0 ..< m).map { i in
            var p = [UInt8](repeating: 0, count: n)
            for (j, d) in data.enumerated() {
                let t = table(coefficient(k: k, row: i, column: j))
                for b in 0 ..< n { p[b] ^= t[Int(d[b])] }
            }
            return p
        }
    }

    /// Fills in the missing data shards of a block: `shards` holds k data then m parity shards, nil where missing, the
    /// present ones of one length. Needs at least k present; parity shards are left as they are.
    public static func reconstruct(_ shards: inout [[UInt8]?], data k: Int) throws {
        guard k > 0, shards.count >= k, shards.count <= 256 else { throw Failure.badBlock }
        var expectedLength: Int?
        for shard in shards {
            guard let shard else { continue }
            if let n = expectedLength, shard.count != n { throw Failure.badBlock }
            expectedLength = shard.count
        }
        let missing = (0 ..< k).filter { shards[$0] == nil }
        guard !missing.isEmpty else { return }
        var rows: [Int] = []
        for (i, s) in shards.enumerated() {
            guard s != nil, rows.count < k else { continue }
            rows.append(i)
        }
        guard rows.count == k, let n = expectedLength else { throw Failure.tooFewShards }
        let a: [[UInt8]] = rows.map { row in
            row < k ? (0 ..< k).map { $0 == row ? 1 : 0 } : (0 ..< k).map { coefficient(k: k, row: row - k, column: $0) }
        }
        let inverse = try invert(a)
        for j in missing {
            var out = [UInt8](repeating: 0, count: n)
            for (r, row) in rows.enumerated() {
                let c = inverse[j][r]
                guard c != 0, let s = shards[row] else { continue }
                let t = table(c)
                for b in 0 ..< n { out[b] ^= t[Int(s[b])] }
            }
            shards[j] = out
        }
    }

    /// Gauss-Jordan over GF(2⁸).
    static func invert(_ a: [[UInt8]]) throws -> [[UInt8]] {
        let k = a.count
        guard k > 0, a.allSatisfy({ $0.count == k }) else { throw Failure.badBlock }
        var m = a.enumerated().map { r, row in row + (0 ..< k).map { $0 == r ? 1 : 0 } }
        for c in 0 ..< k {
            guard let p = (c ..< k).first(where: { m[$0][c] != 0 }) else { throw Failure.badBlock }
            m.swapAt(c, p)
            let v = m[c][c]
            if v != 1 {
                let t = table(inv(v))
                m[c] = m[c].map { t[Int($0)] }
            }
            for r in 0 ..< k where r != c && m[r][c] != 0 {
                let t = table(m[r][c])
                let pivot = m[c]
                for x in 0 ..< 2 * k { m[r][x] ^= t[Int(pivot[x])] }
            }
        }
        return m.map { Array($0[k...]) }
    }
}
