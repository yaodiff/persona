import CoreGraphics
import CoreText
import CoreVideo
import Foundation

/// A synthetic Mac screen at its full pixel size: a window with a sidebar, a heading, paragraphs and a code block, the
/// kind of content a live screen mostly shows. Scenes move it the way people do: typing into a line, scrolling the page,
/// a video playing in part of it. Deterministic, so every run and every tier sees the same pictures.
final class Screen {
    let points: (width: Int, height: Int)
    let pixels: (width: Int, height: Int)
    private let scale: CGFloat
    private let page: CGImage
    private let tall: CGImage
    private let grain: [CGImage]

    /// Where typing goes and where the video plays, in points from the top left.
    static let typingLine = CGRect(x: 260, y: 560, width: 900, height: 22)
    static let videoRect = CGRect(x: 640, y: 120, width: 640, height: 360)
    /// Scrolling speed in points a second: a steady trackpad scroll.
    static let scrollSpeed = 240.0
    static let typingRate = 10.0 // characters a second
    static let videoFps = 30.0

    init(points: (width: Int, height: Int), pixels: (width: Int, height: Int)) {
        self.points = points
        self.pixels = pixels
        scale = CGFloat(pixels.width) / CGFloat(points.width)
        page = Self.drawPage(points: points, height: points.height, scale: scale, paragraphs: 6)
        tall = Self.drawPage(points: points, height: points.height * 4, scale: scale, paragraphs: 30)
        grain = (0..<8).map { Self.noise(seed: $0) }
    }

    // MARK: Scenes

    enum Scene: String, CaseIterable {
        case typing, scroll, video
        /// Scrolling for two seconds, then still: what idle refinement sharpens.
        case scrollStop = "scroll-stop"
    }

    /// What the scene shows at time `t`, as a key: the same key is the same picture (ScreenCaptureKit sends nothing then).
    func key(_ scene: Scene, at t: Double) -> Int {
        switch scene {
        case .typing:
            Int(t * Self.typingRate) * 2 + (Int(t * 2) % 2)
        case .scroll:
            Int((t * Self.scrollSpeed * Double(scale)).rounded())
        case .scrollStop:
            Int((min(t, 2) * Self.scrollSpeed * Double(scale)).rounded())
        case .video:
            Int(t * Self.videoFps)
        }
    }

    /// Draws the scene at time `t` into a BGRA buffer of the full pixel size.
    func render(_ scene: Scene, at t: Double, into buffer: CVPixelBuffer) {
        CVPixelBufferLockBaseAddress(buffer, [])
        defer { CVPixelBufferUnlockBaseAddress(buffer, []) }
        guard let ctx = CGContext(data: CVPixelBufferGetBaseAddress(buffer), width: pixels.width, height: pixels.height,
                                  bitsPerComponent: 8, bytesPerRow: CVPixelBufferGetBytesPerRow(buffer),
                                  space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)
        else { return }
        let full = CGRect(x: 0, y: 0, width: pixels.width, height: pixels.height)
        switch scene {
        case .scroll, .scrollStop:
            let offset = CGFloat(key(scene, at: t))
            let tallHeight = CGFloat(tall.height)
            // CG's origin is the bottom left: the page's top shows first, then it slides up.
            ctx.draw(tall, in: CGRect(x: 0, y: full.height - tallHeight + offset, width: full.width, height: tallHeight))
        case .typing:
            ctx.draw(page, in: full)
            drawTyping(ctx, t: t)
        case .video:
            ctx.draw(page, in: full)
            drawVideo(ctx, t: t)
        }
    }

    private func pixelRect(_ r: CGRect) -> CGRect {
        CGRect(x: r.minX * scale, y: CGFloat(pixels.height) - (r.minY + r.height) * scale, width: r.width * scale,
               height: r.height * scale)
    }

    private func drawTyping(_ ctx: CGContext, t: Double) {
        let line = pixelRect(Self.typingLine)
        ctx.setFillColor(CGColor(gray: 1, alpha: 1))
        ctx.fill(line)
        let typed = String(Self.typingText.prefix(Int(t * Self.typingRate)))
        let font = CTFontCreateWithName("Helvetica" as CFString, 13 * scale, nil)
        let text = NSAttributedString(string: typed, attributes: [.init(kCTFontAttributeName as String): font,
                                                                  .init(kCTForegroundColorAttributeName as String): CGColor(gray: 0.1, alpha: 1)])
        let ctLine = CTLineCreateWithAttributedString(text)
        ctx.textPosition = CGPoint(x: line.minX, y: line.minY + 5 * scale)
        CTLineDraw(ctLine, ctx)
        if Int(t * 2) % 2 == 0 { // the caret blinks twice a second
            let width = CTLineGetTypographicBounds(ctLine, nil, nil, nil)
            ctx.setFillColor(CGColor(gray: 0.1, alpha: 1))
            ctx.fill(CGRect(x: line.minX + CGFloat(width) + 1 * scale, y: line.minY + 2 * scale, width: 1.5 * scale,
                            height: 17 * scale))
        }
    }

    private func drawVideo(_ ctx: CGContext, t: Double) {
        let frame = Double(Int(t * Self.videoFps)) / Self.videoFps
        let r = pixelRect(Self.videoRect)
        ctx.saveGState()
        ctx.clip(to: r)
        // A slow colour sweep, shapes moving across it and film grain: a natural video's mix of smooth and busy.
        let hue = CGFloat(frame.truncatingRemainder(dividingBy: 12) / 12)
        let colors = [Self.color(hue: hue, light: 0.55), Self.color(hue: (hue + 0.3).truncatingRemainder(dividingBy: 1), light: 0.3)]
        let gradient = CGGradient(colorsSpace: CGColorSpace(name: CGColorSpace.sRGB), colors: colors as CFArray, locations: [0, 1])!
        ctx.drawLinearGradient(gradient, start: CGPoint(x: r.minX, y: r.minY), end: CGPoint(x: r.maxX, y: r.maxY), options: [])
        for i in 0..<7 {
            let phase = frame * (0.6 + Double(i) * 0.15) + Double(i)
            let x = r.minX + (CGFloat(sin(phase)) * 0.4 + 0.5) * r.width
            let y = r.minY + (CGFloat(cos(phase * 1.3)) * 0.4 + 0.5) * r.height
            let d = (40 + CGFloat(i) * 14) * scale
            ctx.setFillColor(Self.color(hue: CGFloat(i) / 7, light: 0.7))
            ctx.fillEllipse(in: CGRect(x: x - d / 2, y: y - d / 2, width: d, height: d))
        }
        let tile = grain[Int(t * Self.videoFps) % grain.count]
        ctx.setAlpha(0.12)
        ctx.draw(tile, in: CGRect(x: r.minX, y: r.minY, width: CGFloat(tile.width) * 2, height: CGFloat(tile.height) * 2), byTiling: true)
        ctx.restoreGState()
    }

    // MARK: The page

    private static let words = """
        the screen shows a window with a document open and a sidebar of recent files while a build runs in the terminal \
        below the text wraps at the edge and the code block keeps its own colours so every size of type is on the page \
        meeting notes project plan quarterly review release checklist design system tokens colours spacing typography
        """.split(separator: " ").map(String.init)

    static let typingText = "Hello from the phone: this line is being typed one character at a time, as people do. "
        + "It keeps going so the caret has somewhere to move for the whole scene, word after word after word."

    private static func sentence(_ seed: inout UInt64, words count: Int) -> String {
        var out: [String] = []
        for _ in 0..<count {
            seed = seed &* 6364136223846793005 &+ 1442695040888963407
            out.append(words[Int(seed >> 33) % words.count])
        }
        return out.joined(separator: " ").capitalizedFirst + "."
    }

    private static func drawPage(points: (width: Int, height: Int), height: Int, scale: CGFloat, paragraphs: Int) -> CGImage {
        let w = Int(CGFloat(points.width) * scale), h = Int(CGFloat(height) * scale)
        let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0,
                            space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)!
        ctx.scaleBy(x: scale, y: scale)
        let H = CGFloat(height)
        func fill(_ r: CGRect, _ gray: CGFloat) {
            ctx.setFillColor(CGColor(gray: gray, alpha: 1))
            ctx.fill(CGRect(x: r.minX, y: H - r.maxY, width: r.width, height: r.height))
        }
        func text(_ s: String, _ r: CGRect, font: String, size: CGFloat, gray: CGFloat) {
            let f = CTFontCreateWithName(font as CFString, size, nil)
            let a = NSAttributedString(string: s, attributes: [.init(kCTFontAttributeName as String): f,
                                                               .init(kCTForegroundColorAttributeName as String): CGColor(gray: gray, alpha: 1)])
            let frame = CTFramesetterCreateFrame(CTFramesetterCreateWithAttributedString(a), CFRange(),
                                                 CGPath(rect: CGRect(x: r.minX, y: H - r.maxY, width: r.width, height: r.height),
                                                        transform: nil), nil)
            CTFrameDraw(frame, ctx)
        }
        fill(CGRect(x: 0, y: 0, width: CGFloat(points.width), height: H), 1)
        fill(CGRect(x: 0, y: 0, width: CGFloat(points.width), height: 28), 0.92) // title bar
        fill(CGRect(x: 0, y: 28, width: 220, height: H - 28), 0.95) // sidebar
        var seed: UInt64 = 7
        for i in 0..<(height / 24) {
            text(sentence(&seed, words: 3), CGRect(x: 16, y: 44 + CGFloat(i) * 24, width: 196, height: 20), font: "Helvetica",
                 size: 13, gray: 0.2)
        }
        var y: CGFloat = 52
        text("Quarterly review", CGRect(x: 260, y: y, width: 900, height: 30), font: "Helvetica-Bold", size: 22, gray: 0.05)
        y += 44
        for p in 0..<paragraphs {
            let body = (0..<5).map { _ in sentence(&seed, words: 14) }.joined(separator: " ")
            text(body, CGRect(x: 260, y: y, width: 360, height: 120), font: "Helvetica", size: 13, gray: 0.12)
            y += 130
            if p % 2 == 1 {
                fill(CGRect(x: 260, y: y, width: 360, height: 110), 0.12) // a code block, light on dark
                let code = (0..<6).map { i in "  let value\(i) = compute(\"\(sentence(&seed, words: 2))\", \(i * 7))" }
                    .joined(separator: "\n")
                text(code, CGRect(x: 270, y: y + 8, width: 350, height: 100), font: "Menlo", size: 11, gray: 0.85)
                y += 124
            }
            text("Small print: " + sentence(&seed, words: 18), CGRect(x: 260, y: y, width: 360, height: 30), font: "Helvetica",
                 size: 10, gray: 0.35)
            y += 36
        }
        return ctx.makeImage()!
    }

    private static func noise(seed: Int) -> CGImage {
        let side = 128
        var bytes = [UInt8](repeating: 0, count: side * side)
        var s = UInt64(seed + 1) &* 2862933555777941757
        for i in bytes.indices {
            s = s &* 6364136223846793005 &+ 1442695040888963407
            bytes[i] = UInt8(truncatingIfNeeded: s >> 56)
        }
        let provider = CGDataProvider(data: Data(bytes) as CFData)!
        return CGImage(width: side, height: side, bitsPerComponent: 8, bitsPerPixel: 8, bytesPerRow: side,
                       space: CGColorSpaceCreateDeviceGray(), bitmapInfo: CGBitmapInfo(rawValue: 0), provider: provider,
                       decode: nil, shouldInterpolate: false, intent: .defaultIntent)!
    }

    private static func color(hue: CGFloat, light: CGFloat) -> CGColor {
        let r = light + 0.35 * cos(2 * .pi * hue), g = light + 0.35 * cos(2 * .pi * (hue - 1.0 / 3)),
            b = light + 0.35 * cos(2 * .pi * (hue - 2.0 / 3))
        return CGColor(srgbRed: min(max(r, 0), 1), green: min(max(g, 0), 1), blue: min(max(b, 0), 1), alpha: 1)
    }
}

private extension String {
    var capitalizedFirst: String { prefix(1).uppercased() + dropFirst() }
}
