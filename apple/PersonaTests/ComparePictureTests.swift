import ImageIO
import Testing
import UIKit
import UniformTypeIdentifiers
@testable import Persona

@MainActor struct ComparePictureTests {
    private func png(width: Int, height: Int) -> Data {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let bounds = CGRect(x: 0, y: 0, width: width, height: height)
        return UIGraphicsImageRenderer(bounds: bounds, format: format).pngData { context in
            UIColor.red.setFill()
            context.fill(bounds)
        }
    }

    @Test func downsamplesLargeRevisionsAndKeepsSmallPicturesAtSourceSize() async {
        let wide = png(width: 4096, height: 1024)
        let tall = png(width: 24, height: 48)
        let pictures = await ComparePictures.prepare(old: wide, new: tall, maxPixel: 512)
        #expect(pictures.old?.size == CGSize(width: 512, height: 128))
        #expect(pictures.new?.size == CGSize(width: 24, height: 48))
    }

    @Test func comparesTheSameFrameAsOpeningAMultiImageHEIC() async throws {
        let bytes = NSMutableData()
        let destination = try #require(CGImageDestinationCreateWithData(bytes, UTType.heic.identifier as CFString, 2, nil))
        for (index, width) in [80, 96].enumerated() {
            let frame = try #require(UIImage(data: png(width: width, height: 48))?.cgImage)
            CGImageDestinationAddImage(destination, frame, [kCGImagePropertyPrimaryImage: index == 1] as CFDictionary)
        }
        try #require(CGImageDestinationFinalize(destination))
        let data = bytes as Data
        let source = try #require(CGImageSourceCreateWithData(data as CFData, nil))
        #expect(CGImageSourceGetPrimaryImageIndex(source) == 1)
        let original = try #require(UIImage(data: data))
        let pictures = await ComparePictures.prepare(old: data, new: nil, maxPixel: 128)
        #expect(pictures.old?.size == original.size)
    }

    @Test func changedAndAbsentVersionsHaveTheirOwnPreview() async {
        let bytes = png(width: 64, height: 32)
        let added = await ComparePictures.prepare(old: nil, new: bytes, maxPixel: 128)
        let deleted = await ComparePictures.prepare(old: bytes, new: nil, maxPixel: 128)
        let unreadable = await ComparePictures.prepare(old: Data([0]), new: bytes, maxPixel: 128)
        #expect(added.old == nil && added.new != nil)
        #expect(deleted.old != nil && deleted.new == nil)
        #expect(unreadable.old == nil && unreadable.new != nil)
    }
}
