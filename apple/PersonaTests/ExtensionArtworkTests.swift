import Foundation
import ImageIO
import Testing
@testable import Persona
import PersonaWire

@MainActor struct ExtensionArtworkTests {
    @Test func optionalMetadataDecodesOnBothAPIPathsAndRanksDeterministically() async throws {
        for key in ["extensions", "plugins"] {
            let json = #"{"markets":[],"downloads_updated_at":"2026-11-11T12:00:00Z","\#(key)":[{"market":"m","id":"a","name":"Alpha","version":"1","downloads":3},{"market":"m","id":"z","name":"Zulu","version":"1","downloads":20,"icon_url":"https://images.example.com/z.png"},{"market":"m","id":"b","name":"alpha","version":"1","downloads":3},{"market":"m","id":"n","name":"Missing","version":"1"},{"market":"m","id":"zero","name":"Beta","version":"1","downloads":0}]}"#
            let catalog = try JSON.decoder.decode(ExtensionCatalog.self, from: Data(json.utf8))
            #expect(catalog.plugins.map(\.pluginID) == ["z", "a", "b", "zero", "n"])
            #expect(catalog.plugins.first?.iconURL == "https://images.example.com/z.png")
            #expect(catalog.downloadsUpdatedAt != nil)
            let hub = FakeHub()
            hub.on("GET", "/api/v1/plugins/-/catalog", json: json)
            let page = ExtensionMarketModel(deps: .init(api: { hub.client }))
            await page.load()
            #expect(page.items?.map(\.pluginID) == ["z", "a", "b", "zero", "n"])
        }
    }

    @Test func legacyCatalogUsesNamesAndOptionalArtworkDoesNotBecomeRequired() throws {
        let json = #"{"plugins":[{"market":"m","id":"z","name":"Zulu","version":"1"},{"market":"m","id":"a","name":"Alpha","version":"1"}]}"#
        let catalog = try JSON.decoder.decode(ExtensionCatalog.self, from: Data(json.utf8))
        #expect(catalog.plugins.map(\.pluginID) == ["a", "z"])
        #expect(catalog.plugins.allSatisfy { $0.iconURL == nil && $0.downloads == nil })
        #expect(catalog.downloadsUpdatedAt == nil)
        let installed = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"a","name":"Alpha","kind":"ui","status":"running","icon_url":"https://images.example.com/a.png","downloads":20}"#.utf8))
        #expect(installed.iconURL == "https://images.example.com/a.png" && installed.downloads == 20)
        let old = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"a","name":"Alpha","kind":"ui","status":"running"}"#.utf8))
        #expect(old.iconURL == nil && old.downloads == nil)
    }

    @Test func hubCopiesTheSnapshotTimeToMarketsEntriesAndInstalledRecords() throws {
        let timestamp = "2026-11-11T12:00:00Z"
        let json = #"{"markets":[{"id":"m","spec":"https://extensions.example.com/","name":"Example","downloads_updated_at":"\#(timestamp)"}],"extensions":[{"market":"m","id":"a","name":"Alpha","version":"1","downloads":1099511627776,"downloads_updated_at":"\#(timestamp)"}]}"#
        let catalog = try JSON.decoder.decode(ExtensionCatalog.self, from: Data(json.utf8))
        #expect(catalog.downloadsUpdatedAt == nil, "the hub places a separate timestamp on each market and entry")
        #expect(catalog.markets.first?.downloadsUpdatedAt == catalog.plugins.first?.downloadsUpdatedAt)
        #expect(catalog.plugins.first?.downloadsUpdatedAt != nil)
        #expect(catalog.plugins.first?.downloads == 1 << 40, "the hub's int64 total is not narrowed to Int32")
        let installed = try JSON.decoder.decode(HubExtension.self, from: Data(#"{"id":"a","name":"Alpha","kind":"ui","status":"running","downloads_updated_at":"\#(timestamp)"}"#.utf8))
        #expect(installed.downloadsUpdatedAt == catalog.plugins.first?.downloadsUpdatedAt)
    }

    @Test func installedListUsesTheSamePopularityDefinitionAndRetainsArtwork() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins", json: #"{"plugins":[{"id":"a","name":"Alpha","kind":"ui","status":"running"},{"id":"z","name":"Zulu","kind":"ui","status":"running","downloads":10,"icon_url":"https://images.example.com/z.png"}],"markets":true}"#)
        let page = ExtensionsModel(deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.plugins?.map(\.id) == ["z", "a"])
        #expect(page.plugins?.first?.iconURL == "https://images.example.com/z.png")
        #expect(page.error == nil)
    }

    @Test func publicArtworkRejectsRelativeHTTPAndCredentialURLs() {
        for value in [nil, "", "/logo.png", "http://images.example.com/a.png", "https:///a.png", "https://user:password@images.example.com/a.png", "file:///logo.png"] as [String?] {
            #expect(ExtensionImageCache.url(value) == nil)
        }
        #expect(ExtensionImageCache.url("https://images.example.com/a.png") != nil)
        let configuration = ExtensionImageCache.session.configuration
        #expect(!configuration.httpShouldSetCookies && configuration.httpCookieStorage == nil)
        #expect(configuration.urlCredentialStorage == nil)
        #expect(configuration.httpAdditionalHeaders?.isEmpty != false)
    }

    private static func png(side: Int) throws -> Data {
        let context = try #require(CGContext(data: nil, width: side, height: side, bitsPerComponent: 8,
            bytesPerRow: side * 4, space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        let image = try #require(context.makeImage())
        let data = NSMutableData()
        let destination = try #require(CGImageDestinationCreateWithData(data, "public.png" as CFString, 1, nil))
        CGImageDestinationAddImage(destination, image, nil)
        #expect(CGImageDestinationFinalize(destination))
        return data as Data
    }

    @Test func thumbnailsAreDownsampledAndNeverFallBackToFullSizeDecoding() throws {
        let data = try Self.png(side: 2048)
        let thumbnail = try #require(ExtensionImageCache.decode(data, maxPixel: 96))
        #expect(thumbnail.image.width == 96 && thumbnail.image.height == 96)
        #expect(thumbnail.cost == thumbnail.image.bytesPerRow * 96)
        #expect(ExtensionImageCache.decode(Data("not an image".utf8), maxPixel: 96) == nil)
        #expect(ExtensionImageCache.decode(Data(count: ExtensionImageCache.byteLimit + 1), maxPixel: 96) == nil)
    }

    private actor Fetcher {
        var calls = 0
        let data: Data
        init(data: Data) { self.data = data }
        func fetch(_ url: URL) async throws -> (Data, URLResponse) {
            calls += 1
            await Task.yield()
            return (data, HTTPURLResponse(url: url, statusCode: 200, httpVersion: nil, headerFields: nil)!)
        }
    }

    @Test func simultaneousRequestsCoalesceAndDecodedCacheIncludesPixelSize() async throws {
        let fetcher = Fetcher(data: try Self.png(side: 256))
        let cache = ExtensionImageCache(deps: .init(fetch: { try await fetcher.fetch($0) }))
        let url = URL(string: "https://images.example.com/logo.png")!
        async let first = cache.image(url, maxPixel: 32)
        async let second = cache.image(url, maxPixel: 32)
        let images = await (first, second)
        #expect(images.0?.width == 32 && images.1?.width == 32)
        #expect(await fetcher.calls == 1)
        #expect(await cache.image(url, maxPixel: 32)?.width == 32)
        #expect(await fetcher.calls == 1)
        #expect(await cache.image(url, maxPixel: 64)?.width == 64)
        #expect(await fetcher.calls == 2)
    }

    @Test func failedResponsesAndInvalidDataUseFallback() async {
        let url = URL(string: "https://images.example.com/missing.png")!
        let cache = ExtensionImageCache(deps: .init(fetch: { url in
            (Data(), HTTPURLResponse(url: url, statusCode: 404, httpVersion: nil, headerFields: nil)!)
        }))
        #expect(await cache.image(url, maxPixel: 32) == nil)
        #expect(await cache.image(URL(string: "http://images.example.com/logo.png")!, maxPixel: 32) == nil)
    }
}
