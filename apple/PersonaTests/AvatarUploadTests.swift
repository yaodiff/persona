import CoreGraphics
import Foundation
import ImageIO
import Testing
import UniformTypeIdentifiers
@testable import Persona
import PersonaWire

/// New from Photo (#2405, docs/apple/persona.md "Uploading an image"): offered only by hubs with `look_uploads`, the crop
/// sent square and upright, the hub's refusals shown as it words them, the new avatar chosen here, and deleting one.
@MainActor
struct AvatarUploadTests {
    private static let id = "u-abc12345"

    private static func manifest(_ id: String = id, name: String = "豆豆") -> String {
        let states = ["idle", "thinking", "working", "needs_you", "offline"].map { #""\#($0)":{"file":"dot/\#($0).png"}"# }.joined(separator: ",")
        return #"""
        {"format":1,"id":"\#(id)","name":"\#(name)","avatar":{"sizes":{"64":"avatar/64.png","128":"avatar/128.png","256":"avatar/256.png","512":"avatar/512.png"},"safe":1},
         "dot":{"size":144,"states":{\#(states)}},"app_icon_fallback":"persona","source":{"kind":"upload","uploaded_at":"2026-11-09T08:00:00Z"}}
        """#
    }

    private static func look(_ id: String = id) -> String {
        #"{"id":"\#(id)","source":"hub","name":"豆豆","app_icon":"persona","rev":"r1","bytes":10,"manifest":\#(manifest(id)),"uploaded":true}"#
    }

    /// A hub that lists `ids` as uploaded packages and serves their files.
    private static func serve(_ ids: [String], on hub: FakeHub) throws {
        let list = ids.map { #"{"id":"\#($0)","rev":"r1","bytes":10,"manifest":\#(manifest($0))}"# }.joined(separator: ",")
        hub.on("GET", "/api/v1/personas", json: #"{"personas":[\#(list)]}"#)
        for id in ids {
            for f in try JSONDecoder().decode(PersonaManifest.self, from: Data(manifest(id).utf8)).files {
                hub.on("GET", "/api/v1/personas/\(id)/\(f)", json: "png:\(f)")
            }
        }
    }

    private func store() -> (PersonaStore, UserDefaults) {
        let root = FileManager.default.temporaryDirectory.appending(path: "avatar-uploads-\(UUID().uuidString)")
        let defaults = UserDefaults(suiteName: "avatar-uploads-\(UUID().uuidString)")!
        return (PersonaStore(root: root, defaults: defaults), defaults)
    }

    private func uploads(_ hub: FakeHub, _ store: PersonaStore, features: Set<String> = [AvatarUploadsModel.feature],
                         demo: Bool = false) -> AvatarUploadsModel {
        AvatarUploadsModel(deps: .init(api: { hub.client }, features: { features }, isDemo: { demo }, store: { store },
                                       crop: { AvatarUploadModel(image: $0, deps: .init(api: { hub.client }, store: { store })) }))
    }

    /// A w×h image: a gradient, opaque unless `alpha`.
    private static func image(_ w: Int, _ h: Int, alpha: Bool = false) -> CGImage {
        let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0, space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: (alpha ? CGImageAlphaInfo.premultipliedLast : CGImageAlphaInfo.noneSkipLast).rawValue)!
        for x in stride(from: 0, to: w, by: 8) {
            ctx.setFillColor(CGColor(srgbRed: Double(x) / Double(w), green: 0.4, blue: 0.6, alpha: alpha ? 0.5 : 1))
            ctx.fill(CGRect(x: x, y: 0, width: 8, height: h))
        }
        return ctx.makeImage()!
    }

    private static func encoded(_ image: CGImage, _ type: UTType = .png, orientation: Int? = nil) -> Data {
        let out = NSMutableData()
        let dest = CGImageDestinationCreateWithData(out, type.identifier as CFString, 1, nil)!
        CGImageDestinationAddImage(dest, image, orientation.map { [kCGImagePropertyOrientation: $0] as CFDictionary })
        #expect(CGImageDestinationFinalize(dest))
        return out as Data
    }

    // MARK: Offered

    @Test func offeredOnlyByHubsThatMakeThem() {
        let hub = FakeHub()
        let (s, _) = store()
        #expect(uploads(hub, s).available)
        #expect(!uploads(hub, s, features: ["sessions_list"]).available) // an older hub: no entry
        #expect(!uploads(hub, s, demo: true).available) // Demo Mode has only the default

        let made = Persona(info: PersonaInfo(id: Self.id, rev: "r1", bytes: 1,
                                             manifest: try! JSONDecoder().decode(PersonaManifest.self, from: Data(Self.manifest().utf8))))
        #expect(made.isUploaded)
        #expect(uploads(hub, s).canDelete(made))
        #expect(!uploads(hub, s, features: []).canDelete(made))
        #expect(!uploads(hub, s).canDelete(.builtin))
        let placed = Persona(info: PersonaInfo(id: "nwverse", rev: "r1", bytes: 1, manifest: try! PersonaTests.decode()))
        #expect(!placed.isUploaded && !uploads(hub, s).canDelete(placed))
    }

    @Test func sourceIsReadLeniently() throws {
        #expect(try JSONDecoder().decode(PersonaManifest.self, from: Data(Self.manifest().utf8)).isUploaded)
        // Another maker's `source` of another shape still lists the package; it just isn't an upload.
        let odd = PersonaTests.manifest.replacingOccurrences(of: #""name":"奶蛙","#, with: #""name":"奶蛙","source":"studio","#)
        let m = try PersonaTests.decode(odd)
        #expect(m.problem == nil && !m.isUploaded)
        #expect(try !PersonaTests.decode().isUploaded)
        // Kept through the phone's index.json.
        let again = try JSONDecoder().decode(PersonaManifest.self, from: JSONEncoder().encode(
            try JSONDecoder().decode(PersonaManifest.self, from: Data(Self.manifest().utf8))))
        #expect(again.isUploaded)
    }

    // MARK: The image

    @Test func picturesLoadUpright() throws {
        // Stored 300×120 with EXIF orientation 6 (rotate 90° to show): upright it is 120×300.
        let data = Self.encoded(Self.image(300, 120), .jpeg, orientation: 6)
        let img = try AvatarImage.load(data)
        #expect(img.width == 120 && img.height == 300)
    }

    @Test func refusesWhatCannotBeAnAvatar() {
        #expect(throws: AvatarImage.Problem.self) { try AvatarImage.load(Data("not an image".utf8)) }
        #expect(throws: AvatarImage.Problem.self) { try AvatarImage.load(Self.encoded(Self.image(40, 300))) } // under 64 px a side
    }

    @Test func theCropIsSquareInsideTheImageAndAtMost1024() throws {
        var crop = AvatarCrop(width: 3000, height: 2000)
        #expect(crop.rect == CGRect(x: 500, y: 0, width: 2000, height: 2000)) // centered, the shorter side across
        crop = crop.panned(by: CGSize(width: 10_000, height: 0), viewSide: 300) // dragged far right: the left edge
        #expect(crop.rect.minX == 0)
        crop = crop.zoomed(to: 2) // about the square's center
        #expect(crop.rect == CGRect(x: 500, y: 500, width: 1000, height: 1000))
        crop = crop.panned(by: CGSize(width: -150, height: -150), viewSide: 300) // half the window: 500 px of the image
        #expect(crop.rect == CGRect(x: 1000, y: 1000, width: 1000, height: 1000))
        crop = crop.panned(by: CGSize(width: -3000, height: 0), viewSide: 300) // past the right edge: stops there
        #expect(crop.rect.maxX == 3000)
        // Never under the hub's 64 px, never past 8×.
        #expect(AvatarCrop(width: 3000, height: 2000).zoomed(to: 100).zoom == AvatarCrop.zoomLimit)
        #expect(AvatarCrop(width: 200, height: 100).zoomed(to: 100).rect.width == 64)
        #expect(AvatarCrop(width: 64, height: 64).maxZoom == 1)

        let big = Self.image(2400, 1600)
        let out = try #require(AvatarImage.render(big, rect: AvatarCrop(width: 2400, height: 1600).rect))
        #expect(out.width == 1024 && out.height == 1024)
        let small = try #require(AvatarImage.render(Self.image(300, 200), rect: AvatarCrop(width: 300, height: 200).rect))
        #expect(small.width == 200 && small.height == 200) // not scaled up
    }

    @Test func photosGoAsJPEGWhenLargeAndTransparencyStaysPNG() throws {
        // Noise, like a detailed photo: its PNG is several megabytes.
        let noisy: CGImage = {
            var g = SystemRandomNumberGenerator()
            let bytes = Data((0..<(1024 * 1024 * 4)).map { _ in UInt8.random(in: 0...255, using: &g) })
            return CGImage(width: 1024, height: 1024, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: 1024 * 4,
                           space: CGColorSpace(name: CGColorSpace.sRGB)!, bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.noneSkipLast.rawValue),
                           provider: CGDataProvider(data: bytes as CFData)!, decode: nil, shouldInterpolate: false, intent: .defaultIntent)!
        }()
        let photo = try #require(AvatarImage.encode(noisy))
        #expect(photo.starts(with: [0xFF, 0xD8])) // JPEG
        let plain = try #require(AvatarImage.encode(Self.image(256, 256)))
        #expect(plain.starts(with: [0x89, 0x50, 0x4E, 0x47])) // PNG
        let clear = try #require(AvatarImage.encode(Self.image(256, 256, alpha: true)))
        #expect(clear.starts(with: [0x89, 0x50, 0x4E, 0x47]))
    }

    @Test func namesAreCutAsTheHubCounts() {
        let (s, _) = store()
        let m = AvatarUploadModel(image: Self.image(100, 100), deps: .init(api: { nil }, store: { s }))
        m.name = String(repeating: "豆", count: 50)
        #expect(m.name.count == AvatarUploadModel.maxName)
        // A family emoji is 7 scalars: cut before it, never through it.
        m.name = String(repeating: "a", count: 36) + "👨‍👩‍👧‍👦"
        #expect(m.name == String(repeating: "a", count: 36))
        m.subtitle = String(repeating: "x", count: 100)
        #expect(m.subtitle.count == AvatarUploadModel.maxSubtitle)
        m.name = "   "
        #expect(!m.canUpload) // a name is required
    }

    // MARK: Upload

    @Test func uploadsTheCropAndChoosesItHere() async throws {
        let hub = FakeHub()
        let (s, defaults) = store()
        try Self.serve([], on: hub)
        await s.refresh(hub.client)
        let model = try #require(await uploads(hub, s).picked(Self.encoded(Self.image(1600, 1200))))
        model.name = "  豆豆 "
        model.subtitle = "  "
        hub.on("POST", "/api/v1/looks/uploads") { _ in
            #expect(model.uploading && !model.canUpload) // one at a time
            try Self.serve([Self.id], on: hub) // the hub has written it by now
            return Data(#"{"look":\#(Self.look()),"current":{"id":"\#(Self.id)","effective":"\#(Self.id)"}}"#.utf8)
        }
        #expect(await model.upload())
        #expect(!(await model.upload())) // done: a second tap sends nothing
        let sent = try #require(hub.sent("POST", "/api/v1/looks/uploads").first?.body)
        #expect(hub.sent("POST", "/api/v1/looks/uploads").count == 1)

        let body = try #require(JSONSerialization.jsonObject(with: sent) as? [String: Any])
        #expect(body["name"] as? String == "豆豆")
        #expect(body["subtitle"] == nil) // blank: left out
        #expect(body["choose"] as? Bool == true) // the other devices follow
        let image = try #require((body["image"] as? String).flatMap { Data(base64Encoded: $0) })
        let src = try #require(CGImageSourceCreateWithData(image as CFData, nil))
        let props = try #require(CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any])
        #expect(props[kCGImagePropertyPixelWidth] as? Int == 1024 && props[kCGImagePropertyPixelHeight] as? Int == 1024)

        // On the phone and chosen here.
        #expect(model.made?.id == Self.id && model.error == nil)
        #expect(s.current.id == Self.id && s.chosenID == Self.id)
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == Self.id)
        #expect(s.isOnPhone(s.current))
    }

    @Test func aSubtitleIsSentTrimmed() async throws {
        let hub = FakeHub()
        let (s, _) = store()
        try Self.serve([], on: hub)
        let model = AvatarUploadModel(image: Self.image(200, 200), deps: .init(api: { hub.client }, store: { s }))
        model.name = "豆豆"
        model.subtitle = " 圆滚滚 "
        hub.on("POST", "/api/v1/looks/uploads") { _ in Data(#"{"look":\#(Self.look())}"#.utf8) }
        _ = await model.upload()
        let sent = try #require(hub.sent("POST", "/api/v1/looks/uploads").first?.body)
        let body = try JSONDecoder().decode([String: JSONValue].self, from: sent)
        #expect(body["subtitle"]?.string == "圆滚滚")
    }

    @Test func refusalsShowTheHubsWords() async throws {
        let hub = FakeHub()
        let (s, defaults) = store()
        try Self.serve([], on: hub)
        let model = AvatarUploadModel(image: Self.image(200, 200), deps: .init(api: { hub.client }, store: { s }))
        model.name = "豆豆"
        hub.on("POST", "/api/v1/looks/uploads") { _ in
            throw APIError(status: 409, message: "最多只能有 50 个上传的形象，先删掉一些", fields: ["code": "too_many"])
        }
        #expect(!(await model.upload()))
        #expect(model.error == "最多只能有 50 个上传的形象，先删掉一些")
        #expect(model.canUpload && !model.uploading) // can try again
        #expect(s.current.isBuiltin && defaults.string(forKey: PersonaStore.chosenKey) == nil)

        // No answer at all: a line that says so, not nothing.
        hub.on("POST", "/api/v1/looks/uploads") { _ in throw URLError(.timedOut) }
        #expect(!(await model.upload()))
        #expect(model.error == AvatarUploadModel.message(for: URLError(.timedOut)) && model.error?.isEmpty == false)

        let offline = AvatarUploadModel(image: Self.image(200, 200), deps: .init(api: { nil }, store: { s }))
        offline.name = "豆豆"
        #expect(!(await offline.upload()))
        #expect(offline.error != nil)
    }

    /// A refresh under way when the hub made the avatar (Settings opening, a reconnect) asked for the list before it
    /// was there, and the hub's `personas` broadcast joins that refresh: the list is read again after the hub answered.
    @Test func aRefreshUnderWayWhenTheHubMadeItIsReadAgain() async throws {
        let hub = FakeHub()
        let (s, _) = store()
        try Self.serve([], on: hub)
        let held = HeldList()
        let api = held.client(hub)
        let early = Task { await s.refresh(api) }
        try await held.untilAsked()
        let model = AvatarUploadModel(image: Self.image(200, 200), deps: .init(api: { api }, store: { s }))
        model.name = "豆豆"
        hub.on("POST", "/api/v1/looks/uploads") { _ in
            try Self.serve([Self.id], on: hub)
            held.answer() // the list asked for before, without it
            Task { await s.refresh(api) } // the broadcast
            return Data(#"{"look":\#(Self.look())}"#.utf8)
        }
        #expect(await model.upload())
        await early.value
        #expect(s.current.id == Self.id && s.chosenID == Self.id && s.isOnPhone(s.current))
        #expect(model.done && model.error == nil)
    }

    /// Made on the hub but not on this device (the list or a file did not come): the sheet stays and says so, and
    /// trying again only downloads it; the hub never gets the image twice.
    @Test func madeButNotDownloadedIsTriedAgainWithoutAnotherUpload() async throws {
        let hub = FakeHub()
        let (s, defaults) = store()
        try Self.serve([], on: hub)
        let model = AvatarUploadModel(image: Self.image(200, 200), deps: .init(api: { hub.client }, store: { s }))
        model.name = "豆豆"
        hub.on("POST", "/api/v1/looks/uploads") { _ in
            hub.on("GET", "/api/v1/personas") { _ in throw URLError(.networkConnectionLost) }
            return Data(#"{"look":\#(Self.look())}"#.utf8)
        }
        #expect(!(await model.upload())) // no list
        #expect(model.made?.id == Self.id && !model.done)
        #expect(model.error == String(localized: "The avatar was made, but this device couldn't download it. Try again."))
        #expect(model.canUpload) // the button tries again
        #expect(s.current.isBuiltin && defaults.string(forKey: PersonaStore.chosenKey) == nil)

        try Self.serve([Self.id], on: hub)
        hub.on("GET", "/api/v1/personas/\(Self.id)/avatar/512.png") { _ in throw URLError(.networkConnectionLost) }
        #expect(!(await model.upload())) // listed, but a file did not come
        #expect(s.current.isBuiltin && model.canUpload && model.error != nil)

        try Self.serve([Self.id], on: hub)
        #expect(await model.upload())
        #expect(hub.sent("POST", "/api/v1/looks/uploads").count == 1)
        #expect(s.current.id == Self.id && s.chosenID == Self.id)
        #expect(model.done && model.error == nil && !model.canUpload)
    }

    // MARK: Delete

    @Test func deletingTheChosenOneBringsTheDefaultBack() async throws {
        let hub = FakeHub()
        let (s, defaults) = store()
        try Self.serve([Self.id, "u-other001"], on: hub)
        await s.refresh(hub.client)
        await s.choose(try #require(s.offered.first { $0.id == Self.id }), api: hub.client)
        #expect(s.current.id == Self.id)

        let model = uploads(hub, s)
        hub.on("DELETE", "/api/v1/looks/uploads/\(Self.id)") { _ in
            try Self.serve(["u-other001"], on: hub)
            return Data(#"{"current":{"id":"persona","effective":"persona"}}"#.utf8)
        }
        #expect(await model.delete(try #require(s.offered.first { $0.id == Self.id })))
        #expect(hub.sent("DELETE", "/api/v1/looks/uploads/\(Self.id)").count == 1)
        #expect(s.current.isBuiltin && s.chosenID == Persona.builtinID)
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == Persona.builtinID)
        #expect(s.offered.map(\.id) == [Persona.builtinID, "u-other001"])

        // Another one, not chosen: the choice stays.
        await s.choose(try #require(s.offered.first { $0.id == "u-other001" }), api: hub.client)
        try Self.serve(["u-other001", "u-third001"], on: hub)
        await s.refresh(hub.client)
        hub.on("DELETE", "/api/v1/looks/uploads/u-third001") { _ in Data(#"{"current":{"id":"u-other001","effective":"u-other001"}}"#.utf8) }
        #expect(await model.delete(try #require(s.offered.first { $0.id == "u-third001" })))
        #expect(s.chosenID == "u-other001")
    }

    /// A refresh under way when the hub deleted it still lists it: the list is read again after the delete.
    @Test func aDeleteReadsTheListAgain() async throws {
        let hub = FakeHub()
        let (s, _) = store()
        try Self.serve([Self.id, "u-other001"], on: hub)
        await s.refresh(hub.client)
        let p = try #require(s.offered.first { $0.id == Self.id })
        await s.choose(p, api: hub.client)
        let held = HeldList()
        let api = held.client(hub)
        let early = Task { await s.refresh(api) }
        try await held.untilAsked()
        let model = AvatarUploadsModel(deps: .init(api: { api }, features: { [AvatarUploadsModel.feature] }, isDemo: { false },
                                                   store: { s }))
        hub.on("DELETE", "/api/v1/looks/uploads/\(Self.id)") { _ in
            try Self.serve(["u-other001"], on: hub)
            held.answer() // the list asked for before, with it
            Task { await s.refresh(api) } // the broadcast
            return Data(#"{"current":{"id":"persona","effective":"persona"}}"#.utf8)
        }
        #expect(await model.delete(p))
        await early.value
        #expect(s.offered.map(\.id) == [Persona.builtinID, "u-other001"])
        #expect(s.current.isBuiltin && s.chosenID == Persona.builtinID)
    }

    @Test func aRefusedDeleteChangesNothing() async throws {
        let hub = FakeHub()
        let (s, _) = store()
        try Self.serve([Self.id], on: hub)
        await s.refresh(hub.client)
        let p = try #require(s.offered.first { $0.id == Self.id })
        await s.choose(p, api: hub.client)
        hub.on("DELETE", "/api/v1/looks/uploads/\(Self.id)") { _ in
            throw APIError(status: 409, message: "这里只能删除用上传的图片做的形象", fields: ["code": "not_uploaded"])
        }
        let model = uploads(hub, s)
        #expect(!(await model.delete(p)))
        #expect(model.error == "这里只能删除用上传的图片做的形象")
        #expect(s.current.id == Self.id)

        // Already gone from the hub (404): as deleted.
        hub.on("DELETE", "/api/v1/looks/uploads/\(Self.id)") { _ in
            try Self.serve([], on: hub)
            throw APIError(status: 404, message: "这个形象已经不在 hub 上了", fields: ["code": "not_found"])
        }
        #expect(await model.delete(p))
        #expect(model.error == nil && s.current.isBuiltin)

        // Only uploaded ones, and only on hubs that make them: nothing is sent for the others.
        let placed = Persona(info: PersonaInfo(id: "nwverse", rev: "r1", bytes: 1, manifest: try PersonaTests.decode()))
        #expect(!(await model.delete(placed)))
        #expect(!(await uploads(hub, s, features: []).delete(p)))
        #expect(hub.sent("DELETE", "/api/v1/looks/uploads/nwverse").isEmpty)
    }

    @Test func aPickThatIsNotAnImageSaysWhy() async {
        let hub = FakeHub()
        let (s, _) = store()
        let model = uploads(hub, s)
        #expect(await model.picked(Data("hello".utf8)) == nil)
        #expect(model.error?.isEmpty == false)
        #expect(await model.picked(Self.encoded(Self.image(300, 200))) != nil)
        #expect(model.error == nil)
    }
}

/// The first GET /api/v1/personas through `client(_:)` answered late, with the list as it was when it was asked: a
/// refresh that was under way when the hub changed.
@MainActor
private final class HeldList {
    private var asked = false
    private var gate: CheckedContinuation<Void, Never>?
    private var answered = false

    func client(_ hub: FakeHub) -> APIClient {
        var c = hub.client
        let answer = c.transport!
        c.transport = { [self] req in
            guard req.method == "GET", req.path == "/api/v1/personas", await first() else { return try await answer(req) }
            let then = try await answer(req)
            await held()
            return then
        }
        return c
    }

    private func first() -> Bool {
        defer { asked = true }
        return !asked
    }

    private func held() async {
        if !answered { await withCheckedContinuation { gate = $0 } }
    }

    /// Waits (at most 5 s) until the list was asked for and is being held.
    func untilAsked() async throws {
        for _ in 0..<500 where gate == nil { try await Task.sleep(for: .milliseconds(10)) }
        try #require(gate != nil, "the list was never asked for")
    }

    func answer() {
        answered = true
        gate?.resume()
        gate = nil
    }
}
