import Foundation

/// Unsigned big integers just big enough for Apple Remote Desktop's Diffie-Hellman sign-in (RFBAuth): an odd modulus
/// of a few thousand bits at most (macOS 26 sends 4096), numbers in and out as big-endian bytes, and modular
/// exponentiation by Montgomery multiplication (CIOS) with a 4-bit window, so no general division is needed.
///
/// The inner loop works on 64-bit limbs through raw buffers. On arrays of 32-bit limbs a sign-in's two exponentiations
/// with a 4096-bit prime took 48 s in a Debug build and 0.6 s optimized (#7809); these take 1.5 s and 0.06 s one after
/// the other, and RFBAuth runs them side by side.
nonisolated struct RFBBigUInt: Equatable, Sendable {
    /// Little-endian 64-bit limbs.
    var limbs: [UInt64]

    init(limbs: [UInt64]) { self.limbs = limbs.isEmpty ? [0] : limbs }

    /// From big-endian bytes.
    init(bytes: [UInt8]) {
        var out: [UInt64] = []
        var i = bytes.count
        while i > 0 {
            var limb: UInt64 = 0
            for k in Swift.max(0, i - 8)..<i { limb = limb << 8 | UInt64(bytes[k]) }
            out.append(limb)
            i -= 8
        }
        self.init(limbs: out)
    }

    /// Big-endian, left-padded with zeros (or cut to its low bytes) to count.
    func bytes(count: Int) -> [UInt8] {
        var out = [UInt8](repeating: 0, count: count)
        for (i, limb) in limbs.enumerated() {
            for k in 0..<8 {
                let at = count - 1 - (i * 8 + k)
                if at >= 0 { out[at] = UInt8(truncatingIfNeeded: limb >> (8 * UInt64(k))) }
            }
        }
        return out
    }

    var bitWidth: Int {
        guard let top = limbs.lastIndex(where: { $0 != 0 }) else { return 0 }
        return top * 64 + (64 - limbs[top].leadingZeroBitCount)
    }

    func bit(_ i: Int) -> Bool { i / 64 < limbs.count && limbs[i / 64] >> UInt64(i % 64) & 1 == 1 }

    /// base^exp mod m, for an odd m.
    static func modPow(_ base: RFBBigUInt, _ exp: RFBBigUInt, _ m: RFBBigUInt) -> RFBBigUInt {
        precondition(m.limbs[0] & 1 == 1, "Montgomery needs an odd modulus")
        let n = (m.limbs.lastIndex(where: { $0 != 0 }) ?? 0) + 1
        let mod = Array(m.limbs[0..<n])
        // m' = -m⁻¹ mod 2⁶⁴, by Newton's iteration (each step doubles the bits that are right).
        var inv: UInt64 = 1
        for _ in 0..<6 { inv = inv &* (2 &- mod[0] &* inv) }
        let mPrime = 0 &- inv

        let rr = powerOf2(128 * n, mod) // R² mod m, R = 2^(64n)
        let one = powerOf2(64 * n, mod) // 1 in Montgomery form
        var unit = [UInt64](repeating: 0, count: n)
        unit[0] = 1
        // base, odd powers and all: base^0 … base^15 in Montgomery form.
        var table = [[UInt64]](repeating: one, count: 16)
        var scratch = [UInt64](repeating: 0, count: n + 2)
        table[1] = mul(reduce(base, mod), rr, mod, mPrime, &scratch)
        for w in 2..<16 { table[w] = mul(table[w - 1], table[1], mod, mPrime, &scratch) }

        var acc = one
        let bits = exp.bitWidth
        var at = (bits + 3) / 4 * 4 // from the top window down, 4 bits at a time
        mod.withUnsafeBufferPointer { mp in
            scratch.withUnsafeMutableBufferPointer { sp in
                acc.withUnsafeMutableBufferPointer { ap in
                    let a = ap.baseAddress!, mm = mp.baseAddress!, t = sp.baseAddress!
                    while at > 0 {
                        at -= 4
                        for _ in 0..<4 { montgomery(a, a, a, mm, mPrime, n, t) }
                        let w = (exp.bit(at + 3) ? 8 : 0) | (exp.bit(at + 2) ? 4 : 0) | (exp.bit(at + 1) ? 2 : 0) | (exp.bit(at) ? 1 : 0)
                        if w != 0 { table[w].withUnsafeBufferPointer { montgomery(a, a, $0.baseAddress!, mm, mPrime, n, t) } }
                    }
                    unit.withUnsafeBufferPointer { montgomery(a, a, $0.baseAddress!, mm, mPrime, n, t) } // out of Montgomery form
                }
            }
        }
        return RFBBigUInt(limbs: acc)
    }

    /// a·b·R⁻¹ mod m on arrays.
    private static func mul(_ a: [UInt64], _ b: [UInt64], _ m: [UInt64], _ mPrime: UInt64, _ scratch: inout [UInt64]) -> [UInt64] {
        var r = [UInt64](repeating: 0, count: m.count)
        r.withUnsafeMutableBufferPointer { rp in
            a.withUnsafeBufferPointer { ap in
                b.withUnsafeBufferPointer { bp in
                    m.withUnsafeBufferPointer { mp in
                        scratch.withUnsafeMutableBufferPointer { sp in
                            montgomery(rp.baseAddress!, ap.baseAddress!, bp.baseAddress!, mp.baseAddress!, mPrime, m.count, sp.baseAddress!)
                        }
                    }
                }
            }
        }
        return r
    }

    /// r = a·b·R⁻¹ mod m (CIOS) for a, b < m, n limbs each; t is n + 2 limbs of scratch. r may be a or b.
    private static func montgomery(_ r: UnsafeMutablePointer<UInt64>, _ a: UnsafePointer<UInt64>, _ b: UnsafePointer<UInt64>,
                                   _ m: UnsafePointer<UInt64>, _ mPrime: UInt64, _ n: Int, _ t: UnsafeMutablePointer<UInt64>) {
        t.update(repeating: 0, count: n + 2)
        var i = 0
        while i < n {
            let ai = a[i]
            var c: UInt64 = 0
            var j = 0
            while j < n { // t += a[i]·b
                let (hi, lo) = ai.multipliedFullWidth(by: b[j])
                let (s1, o1) = lo.addingReportingOverflow(t[j])
                let (s2, o2) = s1.addingReportingOverflow(c)
                t[j] = s2
                c = hi &+ (o1 ? 1 : 0) &+ (o2 ? 1 : 0)
                j += 1
            }
            var (s, o) = t[n].addingReportingOverflow(c)
            t[n] = s
            t[n + 1] = o ? 1 : 0
            // t = (t + q·m) / 2⁶⁴, q chosen so the low limb is zero.
            let q = t[0] &* mPrime
            var (hi, lo) = q.multipliedFullWidth(by: m[0])
            (_, o) = lo.addingReportingOverflow(t[0])
            c = hi &+ (o ? 1 : 0)
            j = 1
            while j < n {
                (hi, lo) = q.multipliedFullWidth(by: m[j])
                let (s1, o1) = lo.addingReportingOverflow(t[j])
                let (s2, o2) = s1.addingReportingOverflow(c)
                t[j - 1] = s2
                c = hi &+ (o1 ? 1 : 0) &+ (o2 ? 1 : 0)
                j += 1
            }
            (s, o) = t[n].addingReportingOverflow(c)
            t[n - 1] = s
            t[n] = t[n + 1] &+ (o ? 1 : 0)
            i += 1
        }
        // t < 2m: once more m off when t ≥ m.
        var geq = t[n] != 0
        if !geq {
            geq = true
            var k = n - 1
            while k >= 0 {
                if t[k] != m[k] {
                    geq = t[k] > m[k]
                    break
                }
                k -= 1
            }
        }
        if geq {
            var borrow = false
            for k in 0..<n {
                let (d1, b1) = t[k].subtractingReportingOverflow(m[k])
                let (d2, b2) = d1.subtractingReportingOverflow(borrow ? 1 : 0)
                r[k] = d2
                borrow = b1 || b2
            }
        } else {
            r.update(from: t, count: n)
        }
    }

    /// x mod m, a bit at a time from the top (once per sign-in, for the server's numbers).
    private static func reduce(_ x: RFBBigUInt, _ m: [UInt64]) -> [UInt64] {
        var r = [UInt64](repeating: 0, count: m.count + 1)
        for i in stride(from: x.bitWidth - 1, through: 0, by: -1) {
            shiftIn(&r, x.bit(i))
            if geq(r, m) { subtract(&r, m) }
        }
        return Array(r[0..<m.count])
    }

    /// (2^bits) mod m.
    private static func powerOf2(_ bits: Int, _ m: [UInt64]) -> [UInt64] {
        var r = [UInt64](repeating: 0, count: m.count + 1)
        r[0] = 1
        if geq(r, m) { subtract(&r, m) }
        for _ in 0..<bits {
            shiftIn(&r, false)
            if geq(r, m) { subtract(&r, m) }
        }
        return Array(r[0..<m.count])
    }

    private static func shiftIn(_ r: inout [UInt64], _ bit: Bool) {
        var carry: UInt64 = bit ? 1 : 0
        for k in 0..<r.count {
            let next = r[k] >> 63
            r[k] = r[k] << 1 | carry
            carry = next
        }
    }

    private static func geq(_ a: [UInt64], _ b: [UInt64]) -> Bool {
        for i in stride(from: Swift.max(a.count, b.count) - 1, through: 0, by: -1) {
            let x = i < a.count ? a[i] : 0, y = i < b.count ? b[i] : 0
            if x != y { return x > y }
        }
        return true
    }

    private static func subtract(_ a: inout [UInt64], _ b: [UInt64]) {
        var borrow = false
        for i in 0..<a.count {
            let (d1, b1) = a[i].subtractingReportingOverflow(i < b.count ? b[i] : 0)
            let (d2, b2) = d1.subtractingReportingOverflow(borrow ? 1 : 0)
            a[i] = d2
            borrow = b1 || b2
        }
    }
}
