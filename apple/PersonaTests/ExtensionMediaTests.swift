import Foundation
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// UI version 6's pictures on iOS (#827, docs/extensions/extensions.md "Pictures through the hub"): the device's
/// credentials go only to the hub's image route signed for this page's extension, a picture loads through the page,
/// and the viewer gets its type from its bytes.
@MainActor
struct ExtensionMediaTests {
    @Test func onlyTheHubsImageRouteForThisExtension() {
        #expect(ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/eyJlIjoi.c2ln-_", owner: "home"))
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/other/-/image/abc", owner: "home"), "another extension's")
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/", owner: "home"), "no token")
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/a/b", owner: "home"), "another route under it")
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/a?x=1", owner: "home"))
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/%2e%2e", owner: "home"))
        #expect(!ExtensionViewModel.isHubPicture("https://example.com/api/v1/extensions/home/-/image/a", owner: "home"), "an address")
        #expect(!ExtensionViewModel.isHubPicture("/api/v1/extensions/home/-/image/a", owner: ""))
    }

    @Test func aPictureLoadsThroughThePage() async throws {
        let hub = FakeHub()
        let png = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A])
        hub.on("POST", "/api/v1/plugins/home/-/ui/page", json: #"{"view":{"sections":[]}}"#)
        hub.on("GET", "/api/v1/extensions/home/-/image/tok") { _ in png }
        let m = ExtensionViewModel(plugin: "home", ui: "page", deps: .init(api: { hub.client }))
        #expect(try await m.picture("/api/v1/extensions/home/-/image/tok", pixels: 600) == png)
        let sent = try #require(hub.sent("GET", "/api/v1/extensions/home/-/image/tok").last)
        #expect(sent.query["w"] == "600", "about as wide as it is drawn")
        await #expect(throws: (any Error).self) { try await m.picture("/api/v1/extensions/other/-/image/tok", pixels: 600) }
        #expect(hub.sent("GET", "/api/v1/extensions/other/-/image/tok").isEmpty, "never sent")
    }

    @Test func aPictureIsDecodedAtTheSizeDrawn() throws {
        let size = CGSize(width: 400, height: 200)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let png = try #require(UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            UIColor.orange.setFill()
            ctx.fill(CGRect(origin: .zero, size: size))
        }.pngData())
        let d = try #require(ExtensionPicture.decode(png, maxPixel: 100))
        let cg = try #require(d.image.cgImage)
        #expect(cg.width == 100 && cg.height == 50, "the longer side held to maxPixel, the shape kept")
        #expect(d.data == png && d.cost == 100 * 50 * 4 + png.count, "the bytes kept for the viewer, both counted")
        #expect(ExtensionPicture.decode(Data("not a picture".utf8), maxPixel: 100) == nil)
    }

    @Test func theViewerKnowsThePicturesType() {
        #expect(ExtensionImageItem.payload(Data([0x89, 0x50, 0x4E, 0x47, 1]))?.mime == "image/png")
        #expect(ExtensionImageItem.payload(Data([0xFF, 0xD8, 0xFF, 0xE0]))?.mime == "image/jpeg")
        #expect(ExtensionImageItem.payload(Data([0x47, 0x49, 0x46, 0x38]))?.mime == "image/gif")
        #expect(ExtensionImageItem.payload(Data("<svg".utf8)) == nil, "never SVG")
    }

    /// The home showcase's camera section is its picture, drawn under its title.
    @Test func theCamerasSectionHasItsPicture() throws {
        let showcase = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appending(path: "schema/extension/examples/showcase/home-demo.json")
        let v = try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: showcase))
        let cameras = try #require(ExtensionParts.of(v).first { $0.title == "摄像头" }) // i18n-ignore: the showcase's title
        #expect(cameras.items.contains { if case .image = $0.component { true } else { false } })
    }

    /// A section whose components this app all leaves out (ones it doesn't know) is not drawn as its title alone (#827);
    /// one with a footer, or sent with no components, keeps it.
    @Test func aTitleNeverStandsAloneOverNothing() throws {
        let v = try JSONDecoder().decode(ExtensionView.self, from: Data(#"""
        {"sections": [
          {"title": "Future", "items": [{"type": "hologram"}, {"type": "divider"}, {"type": "hologram"}]},
          {"title": "With a footer", "footer": "Shown on newer apps.", "items": [{"type": "hologram"}]},
          {"title": "Empty on purpose", "items": []},
          {"title": "Rows", "items": [{"type": "hologram"}, {"type": "row", "title": "One"}]}
        ]}
        """#.utf8))
        #expect(ExtensionParts.of(v).map(\.title) == ["With a footer", "Empty on purpose", "Rows"])
        #expect(ExtensionParts.of(v).last?.items.count == 1, "the row, without the unknown component")
        // The page's heading goes to the first section drawn.
        #expect(ExtensionParts.of(ExtensionView(sections: [v.sections[0], ExtensionSection(items: v.sections[3].items)]),
                                  heading: "Page").first?.title == "Page")
    }
}
