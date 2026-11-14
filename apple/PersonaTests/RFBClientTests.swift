import CryptoKit
import Foundation
import Testing
@testable import Persona

/// The native client of a Mac's Screen Sharing (#1343, RFBClient): its big numbers and Apple Remote Desktop's sign-in
/// against values from Python and openssl, ZRLE against a Python encoder, and a whole session against a script.
struct RFBClientTests {
    private func hex(_ h: String) -> [UInt8] {
        var out: [UInt8] = []
        var i = h.startIndex
        while i < h.endIndex {
            let j = h.index(i, offsetBy: 2)
            out.append(UInt8(h[i..<j], radix: 16)!)
            i = j
        }
        return out
    }

    @Test func bigNumbersRaiseAsPythonDoes() {
        for (m, b, e, r) in [("c73edc0e062f1bbf", "c06f48b1c767860e", "5097e84776148e24", "5f8f9e4952ecc963"), ("8311c1784d714bb9ed556ade19584f2e5a0b358909bb152ed0870a9a24322aaeff612a3d9692a4fdd1f549df4ee222e13aba601b129c51033f040b2cfbaf10b901fcb091c33728bb98a0c6b642447c9b2258e8b25868c3aef324a75b12ed94026a8ee61ec3bc275cf92a8eba7b89f87d0daff69adf26111074a02ff8d9233513", "02", "31c2fb578482d82536371ad1be9de161c00a2f435c9a865de4e3d77d87077375db3eeadc00e5f951f4253ac3995e254bcbbad1e7ea453921d68267f390f8692d25e552cce8ebe695c037cfe16d66c4fb55fa17170465f52a368e977ec09d0d75b6a26bd2b6d9f43a6eb9d2bfb43e0732e6633022775c9ae89b82b20771ba2a69", "4342828b9034c022dfaef526f672eaddc0ef457add1acff3c743a978535276ac3cc0560b82b38d710090ef26df5383f648997056449959bdc149a6dded277d86a85fde119929c1ae55e14c801089f21d04f7c772691199ce2ccbec730bde4faaa106a0e6d2c493e34e392757819e30e2ad804fb1f9a73f12b222f8321380442c")] {
            let mod = RFBBigUInt(bytes: hex(m))
            let got = RFBBigUInt.modPow(RFBBigUInt(bytes: hex(b)), RFBBigUInt(bytes: hex(e)), mod).bytes(count: m.count / 2)
            #expect(got == hex(r))
        }
    }

    /// Apple Remote Desktop's sign-in (type 30): this side's public key and the sealed account and password, as Python
    /// and openssl make them from the same numbers.
    @Test func ardSealsAsOpensslDoes() throws {
        let r = try RFBAuth.ard(generator: hex("0002"), prime: hex("d0ebe5c2d786efe5e895dea028ef45e0fff91e751298deb0069e277d3e1ffbe3eeb1c97efe1932a285b94cdffe55088b01f282cb7627070a14d138affd22bb4222527dbda43e7740604d45f265aec90a0c68ec5541dce77ffa17fea535c3212dd3b9c9d9a754ac3e9f3344d507b07fa39c6ab7104a08c720cede24428a013fdb"), serverKey: hex("63d1aee04108a5cd70818fec07b48ab607e0ac25706cc12b697d8b637f6f01cf75126102e78f6af8071d851ac10ad9759eb73a3558b372a3f192a80c56ebad42fd08ea86627ff67b417c356b36bed58396f3fdf95f3837461a847ff25a4dc91236b6b3b2b63afce05934ee154448593c9daeb7cb908ef7f859f554b0bf3a7c8f"),
                                account: "me", password: "pa55·word", privateKey: hex("000000a8dcd76e4c738614c3835ad74554cc5b4da537e7fa78d6908a2fb5dbaaa49fc06d8613fccc2dd8e2984d0b5dea817b0c43bff96bffea99502168e3d0ff075bc9d4c506cd391e579345508a9bfbb30aca6a3e43e74f236a20591491532c81ca906740850c914fcd2bf5e0881e164f01e6ced44ff2e2211cc5a64f27576f"), fill: hex("f25585561105efa7f11a79e07c42ee7a409441e4b1e0ac2baf2fd32f83486baa39ef367a5eef6ee6bdd09e4c82f561a9a0eb614dcccfa4d4b1785f4f8423b828f44b87ef5205b0301ec5558f08948deb40d864b9c9d2a28809b471a80124d2987f9cd95f4370d2842322deeb638712bfea55d606316893f9790024f96cf6f910"))
        #expect(r.publicKey == hex("05320ae34f1f649ed263ecaef4cc6fad48551242f52beefa7ada7a2ce9ac2adecb495a366081e248ad22be223d3e0b001c544c9af7efc3ea8bdc353b3f37ba1b54cb5d9c5ce75ae4dd96b368a71714d7643615f4236cb3227ed1b60a409a4ab1e9186380f1a857fc64fe01c4176673e69f5cd02ea1714931bf8cf184f79b773c"))
        #expect(r.sealed == hex("a1cddf9b7dc6631270a7f6c9acf4012e27d086b607259eed2257ff1e16baf218cee1c421c0cabf02b0b70c75e9978368b4d8b7854b72fea96bbf2c90d482371ea25c2f1b5e22c1f9c94a29f37b670759fc0bc1efcdfd558c2f4e39ecd916d5536e8f9c3a0395734092b4ebe44ac3f16957fe1a9c374337499e96445f26f8d20a"))
    }

    /// ZRLE: solid, packed palette (1 and 2 bits), raw, plain RLE and palette RLE tiles over two rectangles of one zlib
    /// stream, as Python's encoder drew them.
    @Test func zrleDrawsWhatWasEncoded() throws {
        let fb = RFBFramebuffer(width: 130, height: 72)
        let z = RFBZRLE()
        for (rect, y, h) in [("AAAAOnicYtTQOMH0v4GBgeG/xeM+MDouUwhGo/yRxWcBpgFgSrA5IfLr1y8BEEgAgQ0gcAAESBEDAAAA//8=", 0, 64), ("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", 64, 72 - 64)] {
            let bytes = [UInt8](Data(base64Encoded: rect)!)
            let n = Int(bytes[0]) << 24 | Int(bytes[1]) << 16 | Int(bytes[2]) << 8 | Int(bytes[3])
            try z.draw(try z.inflate(Array(bytes[4..<(4 + n)])), x: 0, y: y, w: 130, h: h, into: fb)
        }
        let digest = SHA256.hash(data: Data(bytes: fb.pixels, count: 130 * 72 * 4)).map { String(format: "%02x", $0) }.joined()
        #expect(digest == "7297662dcde95fa331069915030ec2edd3b6f208103e19afeadc6529d058b119")
    }

    /// A ZRLE rectangle the framebuffer cannot hold (#2373) is refused before any pixel is written, raw tiles included.
    @Test func zrleRefusesARectangleOutsideTheFramebuffer() {
        let fb = RFBFramebuffer(width: 8, height: 8)
        let z = RFBZRLE()
        let rawTile = [UInt8(0)] + [UInt8](repeating: 0xff, count: 4 * 4 * 3)
        for (x, y, w, h) in [(6, 0, 4, 4), (0, 6, 4, 4), (-1, 0, 4, 4), (0, 0, 9, 1), (0, 0, -1, 1)] {
            #expect(throws: RFBZRLE.Failure.tile) { try z.draw(rawTile, x: x, y: y, w: w, h: h, into: fb) }
        }
        #expect(Data(bytes: fb.pixels, count: 8 * 8 * 4) == Data(count: 8 * 8 * 4))
        // One that fits still draws.
        #expect(throws: Never.self) { try z.draw(rawTile, x: 4, y: 4, w: 4, h: 4, into: fb) }
        #expect(fb.pixel(7, 7) == [255, 255, 255, 255])
    }

    /// A pixel outside the framebuffer is dropped, not written past the buffer (#2373, behind draw's own check).
    @Test func framebufferDropsAPixelOutsideIt() {
        let fb = RFBFramebuffer(width: 4, height: 4)
        for (x, y) in [(-1, 0), (0, -1), (4, 0), (0, 4), (4, 4)] { fb.set(x, y, b: 1, g: 2, r: 3) }
        #expect(Data(bytes: fb.pixels, count: 4 * 4 * 4) == Data(count: 4 * 4 * 4))
        fb.set(3, 3, b: 1, g: 2, r: 3)
        #expect(fb.pixel(3, 3) == [1, 2, 3, 255])
    }

    /// macOS 26's Screen Sharing sends a 4096-bit prime (generator 5): both sides of the exchange agree, this side's
    /// public key is Python's, and the whole sign-in's arithmetic stays quick even in a Debug build (#7809: 48 s
    /// before, on 32-bit limbs in arrays).
    @Test func ardWithAFourKiloBitPrime() throws {
        let prime = hex(Self.prime4096), five = hex("0005")
        let mine = try RFBAuth.exchange(generator: five, prime: prime, serverKey: hex(Self.theirPublic4096), privateKey: hex(Self.mine4096))
        #expect(mine.publicKey == hex(Self.myPublic4096), "as Python's pow(5, x, p)")
        let theirs = try RFBAuth.exchange(generator: five, prime: prime, serverKey: mine.publicKey, privateKey: hex(Self.theirs4096))
        #expect(theirs.publicKey == hex(Self.theirPublic4096))
        #expect(mine.key == theirs.key, "one shared secret")
        let p = RFBBigUInt(bytes: prime)
        var pMinus1 = prime
        pMinus1[pMinus1.count - 1] -= 1 // odd: no borrow
        #expect(RFBBigUInt.modPow(RFBBigUInt(bytes: five), RFBBigUInt(bytes: pMinus1), p).bytes(count: 512) == [UInt8](repeating: 0, count: 511) + [1],
                "Fermat: 5^(p-1) = 1 mod p")

        let started = ContinuousClock.now
        _ = try RFBAuth.ard(generator: five, prime: prime, serverKey: hex(Self.theirPublic4096), account: "me", password: "pw")
        let took = ContinuousClock.now - started
        #expect(took < .seconds(10), "a sign-in's arithmetic took \(took) (about 1 s in a Debug build on an Apple silicon Mac)")
    }

    // A 4096-bit prime, two private exponents and their public keys (generator 5), from Python.
    private static let prime4096 = "a5ced960618438a1ae09e10c09a5c28c67cee1b50a05378902506c29358322f8cd18848b9698e428fd7369d724f44efd81885c450e0bcd476911e203adc76e480781b25f1dbdca9c53bfbd52d5e67d0de0e76110f0ea61f05029b63036e6522f2354e9df6c20335cdabc9040fa85b820563663d90a211bb3031c62cb18c1e9038bb053fcb0e51e1c39e48a34087b01c91c9684e9e0b5209bc58bf40400e6893691043e3a635ad701655380f5633c6c0b9fba540f94088eb2120de946460cf782ca4466fd0e339e475761822140ff88e4d8d06629ada65e1886488cb4543f3e598b61d745f3a696dc17703c3f82765b7ceb216ca2262819c4c9f535407b34f9a164282ab61a9951949f0336e1896a7e35a9214bc642953d6cb6de50f9b0b9e32be9e6ec55250e25bcd9c09fce0805861d4e6df10ed8e1e32ec39c7269093ea7420ce1f9603c871f7fd86c3514d76fda762fe253e357f24886aa8bee7466e18abcc13f4d5b3ff7438d879274a44745fd5dddb658883fa198faef14e2c3583d338891a76e70115b591424129d869fdce0bcb242f6436e0ac654241ef0ddd4cd22f6044742d54ef1bd5b42414c155b964b6405ad02bb585add7113738c06df82385c68017c93b2744ec65d95803a2f4cf939bd110650e6c83b26992387c18d63b48b72830e7983b938a183209a4d71d6529cfd5d21b969e401ce549d8a2b03fd3027"
    private static let mine4096 = "1adcfeebe82c33cab10a72bf32152fe63f04a3ad4eefb40eaac6c20bbbfbb0987e55d6dda0e5081f90cbe8e006dbc080b7c478c3c0bf247664eb9b3c3e4e6462f3490e29be113172da4d8f2eb46f9f8a3148b2abe79e82dc67beeec6ff4a6a7a58337e2c84abb4d7237226a81375a1c7235972b04e4cc7b0fd908476608afd5b4c846a1389831ad55bcc8a4ce9b631bbd7735bb5332a3d26a04b63cbab83767a47c980693370ebeb1d42bb2ee9d3fcabacc6aeb9c7e340f55be4c877fa9137b3fad5b8a1cd2cb18646a1ed6a6e8d9fec23ad473ca0b727a9bdbe2412e99051b0ce9bb166bfb9afebfa49fc52128cf7dbd2b0e12df551c204820c4c2aefa5fea49e0c6a1ba5f470c3ef0d23cd146df51d40021878439a6851b9256951611c2c0132ec0edc363979b18a4d0582ae0dfb883b3932c06e920a778db0ae4219ef6833b74c5c27e5a44d76cc68c2ec53fd5903914e23f08a44773b2c446ba5eba7ec2ca3d0cb6181b1b5f02575055a0f36d1306c06c01a879f53600acffd875a781edfb72a13efa1650d2965a9e64760a43afeaea166ece3eef6627e66206d920f0babdc4f83ee7268ffe57679e6dc71b9ef0dde1434647fd2b26ff6bef26e87307792390d28a165a9a9c8ed85960b8e7735c1b34c38f95d9fb7c6facadb12da44cde4138f99e2b24e75140a59f5572bc9f654825be3699bc272751a45497316c68add"
    private static let theirs4096 = "2e4c808bd701af6d64cbddc8c31d2f335ccc8816a7aea4e4eb2d9b74c7bc37bcf556fa2c240fdbb70720b221e2400c3f976149ba8e059d02d71b21d4f245873365b6303b77d8ce03792637ee30bf6f46e3a5f9d93eec056d46cd043639e61147961e80dce8793bbf6e531ba4922038873b3edfa4047ae5e9c4d07593e31148d9c7c9684432d6b7fab2f6dcc1980bdbcbad05d695d1d3704d56d4a34c81ed8ce99cba9c8f4e88247bb2ab2fa8f07485628766d2d1b6a323bd6b65238985407f47d2883dc33f623717dc1ceeaa8612cf3c6414abd965e6575259a9ad34b47f56ea19ffb73ead7b66184a50933d543bb5843f05174e15ea6f352248d24d3b12a13f68ecfb88b940c142d40a362ccca3ba97f06358af3fd3be80e4ae2c3d7cc827cab24d0d7f1176c7513ba90bbc3f4acfb5acc431e42d0d49c5ac8761f1e08e6d8c821a0d0ee134b3c5445081a06d3a8b8540c0700aa60fe1a3442879a9e8df235d9d18e978ed7215559b8a1b47464270aa6b4845de8df7824607be6d4ca592dfc11b7ae60b40edb8270fc8932301121f5efc7cb88a01ba4c060cdfbbcd8ea7f324fab250806bc1c217c6dea13fe8b665a909841568e9edcb26e52dedfae0ff150b1b85698923f4fd3c1706a01c6b66459e1481c5d0c23f71906e912bbd6c7992778dc9f0721c492a2c8cefb1fab944d55a14854d6164ef0add4fc386fec7ec8123"
    private static let myPublic4096 = "3db496fa0b5afba34538733a65e8f6290fddbd171d61a99d9756afac5b29a066090a10364db9ce2abc053e3fb2998609fae8dd9f5db6f6cd76cd66aa260e9ba3fee660ac985db43f6565b51a44bc728bf6d85bf7bc590f10bcaff5b414a0a7d35868d0502568b32492f292724aad3de8bfa327146c6579e0ea850a1e63eed6efc82ca32f5b6d0f2b8b5a2fc5e3f7d600370c00d2242009d89d62d55dc18b5c7a103f2cdf082536742d2fc4efd84ab6df4c9a36856dee72dda3a24c9c95bd88fc9affddf2a725754381085fa08ff96dc8c0a4bd266ca72402a558d65f46468952450a9c4b138f272e749a15017efc896135d93b97552824d8e42e245ff7c8a9143fe1fe14f45feafaa4c3e5714c19ca2947e28aae97681ab7af24ea7cb675973e3d1b5fe03af915f3a451f5e6605269c0d381cd3283a49b3b6dd4e4e9d73205a3cfceab82680ac410d90569c28160f4a266ae715cc79c7ef987ae9859f431fbf0c08032b9402719ac707b3b46f045cbcbc9ab408ff60725204c7fd7b0532ca877a841ee9bbb4b05ee7aba142cbe1a88537f7aeb9f81bf0d36e53094ce251cdbc7a5c9b74d637f57cbe060c9f28fa5706babde0e2eaef52f5dbcf6074d032d62aa5ff9ac209f63bf4795166412969890407c474c46459f66695b7b27a0b36d6f0d8b22616b0c018ed9565d137b76cd9b6f5f788bcec5021f7dcedc3fa9102bc827"
    private static let theirPublic4096 = "483cc0303a2b4441f4ebb08dafbdd621f2fe56941a2684fc724716c4511c56ee8ee662f59086c546dd3c9786585a4df8754e112a6e2f421c05cce996183cfc2eb531e6c9072937984a3de5059c4f1aa9a83cccb45d641285451c4f8054c419f1a353a66525491811e0273956862e9a1f0c54b222f7d0aef8205496034e9cf28f8fdf12d801aede36b47f03b470b395882e9a75ce914bcf4f333ff2c47a866994b925c8a41d51c1ec2ea3f93aa627127d391c5ba81d59da7db0c70d46e45253d47c20f586785357be762904aa872316961f36a98ef971ed6c387217d0822aa7dfb473fe890b45e7530f4079e1317acd247824a4f3eab8d8fffbb16550f1f87246742e2f15130c2230ea2ddc898954e80159db27598cecd315e1c9610616059248cadf2e74d7238fcbfd24f0ddfb8fe1bf4ec26480f19bd2727dbc511f4f2cee1f8154d3d80aeccc817f51f46cba828ed34782ac6a0aeb28b8186242945db46a4b0162b42376b0bea3325bc82e949bf39899fa7b881251ccb3bf4500519d567486a537e6c9ffae07019dee9981091746b4c45a7ddb4481f3a2e11c59baf7caa40aca4b233db0e12079d7282e600aca9ad735fff9ce5a73b0e49779db8ba114cc51a5c6e3517cd1ddb950ba2e85ed30c43a797274ccd61a19abf9d2226193107d27885ee21a8b77e32e1af0bb06a2338696646864414c06006fc8ced76e3e9b21ac"

    /// A server's prime that is empty, even or too long (#2373) is refused rather than trapping or spinning in modPow.
    @Test func ardRefusesABadPrime() {
        for prime in [[], hex("d0ebe5c2"), [UInt8](repeating: 0xff, count: RFBAuth.maxPrimeBytes + 1)] {
            #expect(throws: RFBAuth.Failure.crypto) {
                try RFBAuth.ard(generator: hex("0002"), prime: prime, serverKey: prime, account: "me", password: "pw")
            }
        }
    }

    /// A whole session against a script of what a server says: VeNCrypt Plain with the account and password asked for,
    /// the pixel format and encodings asked, two ZRLE rectangles, CopyRect and Raw, the clipboard; and what the client
    /// sent back, in order.
    @Test func aSessionAgainstAScript() async throws {
        let zr1 = [UInt8](Data(base64Encoded: "AAAAOnicYtTQOMH0v4GBgeG/xeM+MDouUwhGo/yRxWcBpgFgSrA5IfLr1y8BEEgAgQ0gcAAESBEDAAAA//8=")!), zr2 = [UInt8](Data(base64Encoded: "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")!)
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        func rect(_ x: Int, _ y: Int, _ w: Int, _ h: Int, _ e: Int32) -> [UInt8] { u16(x) + u16(y) + u16(w) + u16(h) + RFBAuth.be32(UInt32(bitPattern: e)) }
        var s0: [UInt8] = u16(130) + u16(72); s0 += [UInt8](repeating: 0, count: 16); s0 += RFBAuth.be32(8); s0 += Array("Mock Mac".utf8)
        var s1: [UInt8] = [0, 0] + u16(2); s1 += rect(0, 0, 130, 64, 16); s1 += zr1; s1 += rect(0, 64, 130, 72 - 64, 16); s1 += zr2
        var s2: [UInt8] = [0, 0] + u16(2); s2 += rect(130 - 1, 72 - 1, 1, 1, 1); s2 += u16(0) + u16(0); s2 += rect(1, 0, 1, 1, 0); s2 += [9, 8, 7, 0]
        var s3: [UInt8] = [3, 0, 0, 0]; s3 += RFBAuth.be32(5); s3 += Array("hello".utf8)
        let a1: [UInt8] = [1] + RFBAuth.be32(256)
        let script: [[UInt8]] = [Array("RFB 003.889\n".utf8), [1, 19], [0, 2], [0], a1, RFBAuth.be32(0), s0, s1, s2, s3]
        let t = ScriptedTransport(script)
        let seen = Seen()
        let client = RFBClient(transport: t) { await seen.asked(); return .init(account: "me", password: "mock-pass") }
        client.onState = { s in Task { await seen.state(s) } }
        client.onFrame = { fb, _ in let p = fb.pixel(0, 0), q = fb.pixel(130 - 1, 72 - 1), r = fb.pixel(1, 0); Task { await seen.frame(p, q, r) } }
        client.onCutText = { s in Task { await seen.cut(s) } }
        client.start()
        try await seen.until { $0.ended }

        let sent = t.sent
        #expect(sent[0] == Array("RFB 003.008\n".utf8) && sent[1] == [19] && sent[2] == [0, 2] && sent[3] == RFBAuth.be32(256))
        var plain: [UInt8] = RFBAuth.be32(2); plain += RFBAuth.be32(9); plain += Array("me".utf8); plain += Array("mock-pass".utf8)
        #expect(sent[4] == plain, "Plain: the account and password")
        #expect(sent[5] == [1], "shared")
        #expect(sent[6] == [0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0], "32-bit BGRA")
        #expect(sent[7].prefix(4) == [2, 0, 0, 7] && sent[7].count == 4 + 7 * 4)
        var whole: [UInt8] = [3, 0, 0, 0, 0, 0]; whole += u16(130); whole += u16(72)
        #expect(sent[8] == whole, "the whole screen first")
        #expect(sent[9][1] == 1 && sent[10][1] == 1, "then what changed")
        let s = await seen.snapshot
        #expect(s.asked == 1)
        #expect(s.states.contains(.live(width: 130, height: 72, name: "Mock Mac")))
        #expect(s.frames.count == 2)
        let first = s.frames[0], last = s.frames[1]
        #expect(first.0 == [40, 40, 200, 255], "ZRLE's solid tile, BGRA")
        #expect(last.1 == first.0, "CopyRect from (0, 0)")
        #expect(last.2 == [9, 8, 7, 0], "Raw")
        #expect(s.cuts == ["hello"])
    }

    /// ARD (type 30): the client answers the server's numbers with 128 sealed bytes and its own public key of the
    /// prime's length; a refused sign-in comes out as refused, with the Mac's reason.
    @Test func ardAndARefusal() async throws {
        let prime = hex("d0ebe5c2d786efe5e895dea028ef45e0fff91e751298deb0069e277d3e1ffbe3eeb1c97efe1932a285b94cdffe55088b01f282cb7627070a14d138affd22bb4222527dbda43e7740604d45f265aec90a0c68ec5541dce77ffa17fea535c3212dd3b9c9d9a754ac3e9f3344d507b07fa39c6ab7104a08c720cede24428a013fdb"), key = hex("63d1aee04108a5cd70818fec07b48ab607e0ac25706cc12b697d8b637f6f01cf75126102e78f6af8071d851ac10ad9759eb73a3558b372a3f192a80c56ebad42fd08ea86627ff67b417c356b36bed58396f3fdf95f3837461a847ff25a4dc91236b6b3b2b63afce05934ee154448593c9daeb7cb908ef7f859f554b0bf3a7c8f")
        let script: [[UInt8]] = [Array("RFB 003.889\n".utf8), [4, 30, 33, 36, 35], [0, 2, 0, 128] + prime + key,
                                 RFBAuth.be32(1) + RFBAuth.be32(21) + Array("Authentication failed".utf8)]
        let t = ScriptedTransport(script)
        let seen = Seen()
        let client = RFBClient(transport: t) { .init(account: "me", password: "x") }
        client.onState = { s in Task { await seen.state(s) } }
        client.start()
        try await seen.until { $0.ended }
        #expect(t.sent[1] == [30], "ARD out of what macOS offers")
        #expect(t.sent[2].count == 128 + 128)
        #expect(await seen.snapshot.states.contains(.refused("Authentication failed")))
    }

    /// DesktopSize (-223) mid-session (#2372): the framebuffer takes the new size, onResize says so before the frame
    /// drawn at it, and a later update draws on the new screen; the session keeps its pointer on that screen.
    @Test func desktopSizeMidSession() async throws {
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        func rect(_ x: Int, _ y: Int, _ w: Int, _ h: Int, _ e: Int32) -> [UInt8] { u16(x) + u16(y) + u16(w) + u16(h) + RFBAuth.be32(UInt32(bitPattern: e)) }
        let resize: [UInt8] = [0, 0] + u16(1) + rect(0, 0, 200, 100, -223)
        let raw: [UInt8] = [0, 0] + u16(1) + rect(199, 99, 1, 1, 0) + [1, 2, 3, 0]
        let t = ScriptedTransport([Array("RFB 003.889\n".utf8), [1, 1], RFBAuth.be32(0),
                                   [0, 4, 0, 3] + [UInt8](repeating: 0, count: 16) + RFBAuth.be32(0), resize, raw])
        let seen = Seen()
        let client = RFBClient(transport: t) { nil }
        client.onState = { s in Task { await seen.state(s) } }
        client.onResize = { w, h in Task { await seen.resized(w, h) } }
        client.onFrame = { fb, _ in
            let size = [UInt8(fb.width), UInt8(fb.height)], corner = fb.width == 200 ? fb.pixel(199, 99) : []
            Task { await seen.frame(size, corner, []) }
        }
        client.start()
        try await seen.until { $0.ended }
        let s = await seen.snapshot
        #expect(s.states.contains(.live(width: 4, height: 3, name: "")))
        #expect(s.resizes.count == 1 && s.resizes.first == [200, 100])
        #expect(s.frames.map(\.0) == [[200, 100], [200, 100]], "both frames at the new size")
        #expect(s.frames.last?.1 == [1, 2, 3, 0], "Raw at the new screen's last pixel")
        #expect(RFBSession.clamped(CGPoint(x: 1000, y: -5), to: CGSize(width: 200, height: 100)) == CGPoint(x: 199, y: 0))
        #expect(RFBSession.clamped(CGPoint(x: 150, y: 80), to: CGSize(width: 100, height: 50)) == CGPoint(x: 99, y: 49))
    }

    /// The session follows a smaller screen mid-session (#2372): its size is the new one, not the one it went live with,
    /// and the pointer (the middle of the old screen) is kept on the new one, without anything sent for it.
    @MainActor @Test func sessionFollowsDesktopSize() async throws {
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        // built in steps: one long `+` chain does not type-check in time on Swift 6.2 (#2867, as #2096)
        var resize: [UInt8] = [0, 0] + u16(1); resize += u16(0) + u16(0); resize += u16(100) + u16(50)
        resize += RFBAuth.be32(UInt32(bitPattern: -223))
        var server: [UInt8] = u16(300) + u16(200); server += [UInt8](repeating: 0, count: 16); server += RFBAuth.be32(0)
        let t = ScriptedTransport([Array("RFB 003.889\n".utf8), [1, 1], RFBAuth.be32(0), server, resize], holds: true)
        let session = RFBSession(ref: MachineRef(id: "mac"))
        defer { session.close() }
        session.run(RFBClient(transport: t) { nil })
        for _ in 0..<300 where session.size != CGSize(width: 100, height: 50) { try await Task.sleep(for: .milliseconds(10)) }
        #expect(session.phase == .live)
        #expect(session.size == CGSize(width: 100, height: 50), "the size it went live with: \(session.size)")
        #expect(session.pointer == CGPoint(x: 99, y: 49))
        try await Task.sleep(for: .milliseconds(200))
        #expect(!t.sent.contains { $0.first == 5 }, "no PointerEvent sent for the resize")
    }

    /// #7775: the wheel sends every notch it is given, each pressed and let go where it was asked (it once dropped any
    /// past 20 in a call; VNCWheel now sends a few each display frame).
    @MainActor @Test func sessionScrollsEveryNotch() async throws {
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        var server: [UInt8] = u16(300) + u16(200); server += [UInt8](repeating: 0, count: 16); server += RFBAuth.be32(0)
        let t = ScriptedTransport([Array("RFB 003.889\n".utf8), [1, 1], RFBAuth.be32(0), server], holds: true)
        let session = RFBSession(ref: MachineRef(id: "mac"))
        defer { session.close() }
        session.run(RFBClient(transport: t) { nil })
        for _ in 0..<300 where session.phase != .live { try await Task.sleep(for: .milliseconds(10)) }
        #expect(session.phase == .live)
        session.scroll(notches: -30, at: CGPoint(x: 10, y: 20))
        session.scroll(notches: 2, at: CGPoint(x: 10, y: 20))
        func pointer() -> [[UInt8]] { t.sent.filter { $0.first == 5 } }
        for _ in 0..<300 where pointer().count < 64 { try await Task.sleep(for: .milliseconds(10)) }
        var down: [UInt8] = []
        for _ in 0..<30 { down += [16, 0] }
        #expect(pointer().map { $0[1] } == down + [8, 0, 8, 0], "30 down and 2 up, each pressed and let go")
        #expect(pointer().allSatisfy { Array($0[2...]) == [0, 10, 0, 20] })
    }

    /// ExtendedDesktopSize (-308, #80): asked for; the server's screens come with the first update, in framebuffer pixels,
    /// before the rectangles drawn after them; a later one at another size resizes first; one whose status is not 0 (an
    /// answer to a resize request, which this client never makes) changes nothing.
    @Test func extendedDesktopSizeGivesTheScreens() async throws {
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        func rect(_ x: Int, _ y: Int, _ w: Int, _ h: Int, _ e: Int32) -> [UInt8] { u16(x) + u16(y) + u16(w) + u16(h) + RFBAuth.be32(UInt32(bitPattern: e)) }
        func screen(_ id: UInt32, _ x: Int, _ w: Int) -> [UInt8] { RFBAuth.be32(id) + u16(x) + u16(0) + u16(w) + u16(2) + RFBAuth.be32(0) }
        var server: [UInt8] = u16(8) + u16(2); server += [UInt8](repeating: 0, count: 16); server += RFBAuth.be32(0)
        var first: [UInt8] = [0, 0] + u16(2); first += rect(0, 0, 8, 2, -308); first += [2, 0, 0, 0]
        first += screen(7, 0, 4); first += screen(9, 4, 4); first += rect(7, 1, 1, 1, 0); first += [1, 2, 3, 0]
        var refused: [UInt8] = [0, 0] + u16(1); refused += rect(1, 3, 99, 99, -308); refused += [1, 0, 0, 0]; refused += screen(1, 0, 99)
        var wider: [UInt8] = [0, 0] + u16(1); wider += rect(0, 0, 12, 2, -308); wider += [3, 0, 0, 0]
        wider += screen(7, 0, 4); wider += screen(9, 4, 4); wider += screen(11, 8, 4)
        let t = ScriptedTransport([Array("RFB 003.889\n".utf8), [1, 1], RFBAuth.be32(0), server, first, refused, wider])
        let seen = Seen()
        let client = RFBClient(transport: t) { nil }
        client.onState = { s in Task { await seen.state(s) } }
        client.onResize = { w, h in Task { await seen.resized(w, h) } }
        client.onScreens = { screens in Task { await seen.gotScreens(screens) } }
        client.onFrame = { fb, _ in let p = fb.pixel(7, 1), w = UInt8(fb.width); Task { await seen.frame([w], p, []) } }
        client.start()
        try await seen.until { $0.ended }

        let encodings = try #require(t.sent.first { $0.first == 2 })
        let asked = stride(from: 4, to: encodings.count, by: 4).map { i -> Int32 in
            let b = encodings[i..<i + 4].map { UInt32($0) }
            return Int32(bitPattern: b[0] << 24 | b[1] << 16 | b[2] << 8 | b[3])
        }
        #expect(asked.contains(-308), "ExtendedDesktopSize asked for: \(asked)")
        let s = await seen.snapshot
        let two = [RFBClient.Screen(id: 7, frame: CGRect(x: 0, y: 0, width: 4, height: 2)), RFBClient.Screen(id: 9, frame: CGRect(x: 4, y: 0, width: 4, height: 2))]
        #expect(s.screens == [two, two + [RFBClient.Screen(id: 11, frame: CGRect(x: 8, y: 0, width: 4, height: 2))]])
        #expect(s.resizes == [[12, 2]], "the same size is no resize, and the refused answer none either")
        #expect(s.frames.map(\.0) == [[8], [12]])
        #expect(s.frames.first?.1 == [1, 2, 3, 0], "the Raw rectangle after the screens")
    }

    /// The pointer, keys and clipboard go out once signed in, in the order they came (a key's down before its up).
    @Test func pointerKeysAndClipboardOutInOrder() async throws {
        let t = ScriptedTransport([Array("RFB 003.889\n".utf8), [1, 1], RFBAuth.be32(0),
                                   [0, 4, 0, 3] + [UInt8](repeating: 0, count: 16) + RFBAuth.be32(0)], holds: true)
        let seen = Seen()
        let client = RFBClient(transport: t) { nil }
        client.onState = { s in Task { await seen.state(s) } }
        client.pointer(x: 1, y: 1, buttons: 0) // before the sign-in: held until it is done, not in the middle of it
        client.start()
        try await seen.until { $0.states.contains { if case .live = $0 { true } else { false } } }
        var out: [[UInt8]] = [[5, 0, 0, 1, 0, 1]]
        for i in 0..<40 {
            client.pointer(x: 300 + i, y: 2, buttons: 1)
            client.key(0xff0d, down: true)
            client.key(0xff0d, down: false)
            out += [[5, 1, 1, UInt8(44 + i), 0, 2], [4, 1, 0, 0, 0, 0, 0xff, 0x0d], [4, 0, 0, 0, 0, 0, 0xff, 0x0d]]
        }
        client.cutText("hi é")
        out.append([6, 0, 0, 0, 0, 0, 0, 4] + [104, 105, 32, 0xe9])
        try await Task.sleep(for: .milliseconds(300))
        client.close()
        #expect(t.sent.filter { $0.first == 4 || $0.first == 5 || $0.first == 6 } == out)
    }

    /// #3954: while the video channel shows the picture, an update that comes asks for no next one; released, the held
    /// request goes out once (incremental: the Mac sends what changed since its last update).
    @Test func heldUpdatesAskForNoMore() async throws {
        func u16(_ v: Int) -> [UInt8] { [UInt8(v >> 8), UInt8(v & 0xff)] }
        var s0: [UInt8] = u16(2) + u16(2); s0 += [UInt8](repeating: 0, count: 16); s0 += RFBAuth.be32(4); s0 += Array("Mock".utf8)
        // In typed steps: as one sum Xcode 26.0 could not type-check it in time.
        var raw: [UInt8] = [0, 0] + u16(1)
        raw += u16(0) + u16(0)
        raw += u16(1) + u16(1)
        raw += RFBAuth.be32(0) + [1, 2, 3, 0]
        let a1: [UInt8] = [1] + RFBAuth.be32(256)
        let script: [[UInt8]] = [Array("RFB 003.889\n".utf8), [1, 19], [0, 2], [0], a1, RFBAuth.be32(0), s0, raw, raw]
        let t = ScriptedTransport(script, holds: true)
        let seen = Seen()
        let client = RFBClient(transport: t) { .init(account: "me", password: "mock-pass") }
        client.onFrame = { _, _ in Task { await seen.frame([], [], []) } }
        client.holdUpdates(true)
        client.start()
        for _ in 0..<300 where await seen.snapshot.frames.count < 2 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(await seen.snapshot.frames.count == 2)
        func requests() -> [UInt8] { t.sent.filter { $0.count == 10 && $0[0] == 3 }.map { $0[1] } }
        #expect(requests() == [0], "only the first, whole one")
        client.holdUpdates(false)
        for _ in 0..<100 where requests().count < 2 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(requests() == [0, 1], "the held one, once")
        client.holdUpdates(false)
        try await Task.sleep(for: .milliseconds(50))
        #expect(requests() == [0, 1], "nothing more was held")
        client.close()
    }

    /// #3954: the screen-sharing stream carries a pair, which the video channel binds to. A WebSocket URL with it; none
    /// without one (the URL as before); and a fresh, unguessable one each connection.
    @Test func theConnectURLCarriesAPair() throws {
        let http = try #require(URL(string: "https://hub.example.com/api/v1/machines/macbook/ports/5900/connect"))
        let paired = RFBWebSocket.url(http, pair: "0123abcd")
        #expect(paired.absoluteString == "wss://hub.example.com/api/v1/machines/macbook/ports/5900/connect?pair=0123abcd")
        #expect(RFBWebSocket.url(http, pair: nil).absoluteString == "wss://hub.example.com/api/v1/machines/macbook/ports/5900/connect")
        #expect(RFBWebSocket.url(http, pair: "").query == nil, "an empty pair is no pair")
        let plain = try #require(URL(string: "http://192.0.2.1:8787/api/v1/machines/m/ports/5900/connect"))
        #expect(RFBWebSocket.url(plain, pair: "p").scheme == "ws", "a hub over http is ws")

        let a = RFBWebSocket.newPair(), b = RFBWebSocket.newPair()
        #expect(a.count == 32 && a.allSatisfy(\.isHexDigit), "16 random bytes in hex: \(a)")
        #expect(a != b, "a new pair each connection")
    }
}

/// A server that says what it was given, in order, then closes; what the client sent is kept.
private final class ScriptedTransport: RFBTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var script: [[UInt8]]
    private var _sent: [[UInt8]] = []
    var sent: [[UInt8]] { lock.withLock { _sent } }

    /// holds: once the script is said, it stays open (and quiet) instead of closing.
    private let holds: Bool

    init(_ script: [[UInt8]], holds: Bool = false) {
        self.script = script
        self.holds = holds
    }

    func send(_ bytes: [UInt8]) async throws { lock.withLock { _sent.append(bytes) } }

    func receive() async throws -> [UInt8] {
        try await Task.sleep(for: .milliseconds(2))
        while holds, lock.withLock({ script.isEmpty }) { try await Task.sleep(for: .milliseconds(20)) }
        return try lock.withLock {
            guard !script.isEmpty else { throw URLError(.networkConnectionLost) }
            return script.removeFirst()
        }
    }

    func close() {}
}

private actor Seen {
    struct Snapshot {
        var asked = 0
        var states: [RFBClient.State] = []
        var frames: [([UInt8], [UInt8], [UInt8])] = []
        var cuts: [String] = []
        var resizes: [[Int]] = []
        var screens: [[RFBClient.Screen]] = []
        var ended: Bool { states.contains { if case .failed = $0 { true } else if case .refused = $0 { true } else { $0 == .closed } } }
    }

    private(set) var snapshot = Snapshot()
    func asked() { snapshot.asked += 1 }
    func state(_ s: RFBClient.State) { snapshot.states.append(s) }
    func frame(_ a: [UInt8], _ b: [UInt8], _ c: [UInt8]) { snapshot.frames.append((a, b, c)) }
    func cut(_ s: String) { snapshot.cuts.append(s) }
    func resized(_ w: Int, _ h: Int) { snapshot.resizes.append([w, h]) }
    func gotScreens(_ s: [RFBClient.Screen]) { snapshot.screens.append(s) }

    func until(_ done: (Snapshot) -> Bool) async throws {
        for _ in 0..<500 where !done(snapshot) { try await Task.sleep(for: .milliseconds(10)) }
        #expect(done(snapshot), "the session did not end")
        try await Task.sleep(for: .milliseconds(50)) // the callbacks' tasks
    }
}
