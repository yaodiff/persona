// Makes scripts/mock/screen_video/fixture.bin and fixture-h264.bin, the mock hub's live-screen video (#3954): a made-up
// desktop drawn here (no real screen), encoded the way Persona Screen encodes (no frame reordering; one keyframe at the
// start), written as the helper's wire messages: `hello`, `format`, then one `frame` per picture. The second argument
// names the codec: `hevc` (the default; H.264 when this Mac has no HEVC encoder) or `h264`, which the mock plays to a
// client that offers only H.264, as a browser without HEVC does (#6558). Built with PersonaScreenKit's own sources by
// make_fixture.sh, which is how to make them again.
import CoreGraphics
import CoreMedia
import CoreText
import Foundation
import VideoToolbox

@main
struct MakeFixture {
    /// The mock's own Screen Sharing is 480×300 (16:10): the same shape, so the video lies exactly over its picture.
    static let width = 960, height = 600, fps = 30, seconds = 3

    static func main() throws {
        let out = URL(fileURLWithPath: CommandLine.arguments[1])
        let only = CommandLine.arguments.count > 2 ? CommandLine.arguments[2] : nil
        let encoded = try encode(only: only)
        var data = ScreenVideoMessage.hello(HelloMessage(version: ScreenVideoWire.protocolVersion, codec: encoded.codec,
                                                         display: 1, width: width, height: height)).framed()
        data.append(ScreenVideoMessage.format(FormatMessage(parameterSets: encoded.sets)).framed())
        for (i, f) in encoded.frames.enumerated() {
            let frame = VideoFrame(number: UInt32(i + 1), captureMicros: UInt64(i) * 1_000_000 / UInt64(fps),
                                   flags: f.key ? .keyframe : [], sample: f.bytes)
            data.append(ScreenVideoMessage.frame(frame).framed())
        }
        try data.write(to: out)
        print("\(out.lastPathComponent): \(encoded.codec), \(encoded.frames.count) frames, \(data.count) bytes")
    }

    final class Sink: @unchecked Sendable {
        var frames: [(key: Bool, bytes: Data)] = []
        var sets: [Data] = []
        var failed: OSStatus = noErr
    }

    static func encode(only: String?) throws -> (codec: String, sets: [Data], frames: [(key: Bool, bytes: Data)]) {
        for (codec, type) in [("hevc", kCMVideoCodecType_HEVC), ("h264", kCMVideoCodecType_H264)] where only == nil || only == codec {
            var session: VTCompressionSession?
            let sink = Sink()
            let made = VTCompressionSessionCreate(allocator: nil, width: Int32(width), height: Int32(height), codecType: type,
                                                  encoderSpecification: nil, imageBufferAttributes: nil,
                                                  compressedDataAllocator: nil, outputCallback: nil, refcon: nil,
                                                  compressionSessionOut: &session)
            guard made == noErr, let session else { continue }
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AllowFrameReordering, value: kCFBooleanFalse)
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AverageBitRate, value: 600_000 as CFNumber)
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_MaxKeyFrameInterval, value: (fps * seconds) as CFNumber)
            VTSessionSetProperty(session, key: kVTCompressionPropertyKey_ExpectedFrameRate, value: fps as CFNumber)
            for i in 0 ..< fps * seconds {
                let pb = try picture(i)
                let t = CMTime(value: CMTimeValue(i), timescale: CMTimeScale(fps))
                VTCompressionSessionEncodeFrame(session, imageBuffer: pb, presentationTimeStamp: t, duration: .invalid,
                                                frameProperties: nil, infoFlagsOut: nil) { status, _, sample in
                    guard status == noErr, let sample else { sink.failed = status; return }
                    take(sample, codec: codec, into: sink)
                }
            }
            VTCompressionSessionCompleteFrames(session, untilPresentationTimeStamp: .invalid)
            VTCompressionSessionInvalidate(session)
            if sink.failed == noErr, !sink.sets.isEmpty, sink.frames.count == fps * seconds { return (codec, sink.sets, sink.frames) }
        }
        throw NSError(domain: "make_fixture", code: 1, userInfo: [NSLocalizedDescriptionKey: "no encoder"])
    }

    static func take(_ sample: CMSampleBuffer, codec: String, into sink: Sink) {
        let attachments = CMSampleBufferGetSampleAttachmentsArray(sample, createIfNecessary: false) as? [[CFString: Any]]
        let key = !((attachments?.first?[kCMSampleAttachmentKey_NotSync] as? Bool) ?? false)
        if sink.sets.isEmpty, let format = CMSampleBufferGetFormatDescription(sample) {
            var count = 0
            if codec == "hevc" {
                CMVideoFormatDescriptionGetHEVCParameterSetAtIndex(format, parameterSetIndex: 0, parameterSetPointerOut: nil,
                                                                   parameterSetSizeOut: nil, parameterSetCountOut: &count, nalUnitHeaderLengthOut: nil)
            } else {
                CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, parameterSetIndex: 0, parameterSetPointerOut: nil,
                                                                   parameterSetSizeOut: nil, parameterSetCountOut: &count, nalUnitHeaderLengthOut: nil)
            }
            for i in 0 ..< count {
                var p: UnsafePointer<UInt8>?
                var n = 0
                if codec == "hevc" {
                    CMVideoFormatDescriptionGetHEVCParameterSetAtIndex(format, parameterSetIndex: i, parameterSetPointerOut: &p,
                                                                       parameterSetSizeOut: &n, parameterSetCountOut: nil, nalUnitHeaderLengthOut: nil)
                } else {
                    CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, parameterSetIndex: i, parameterSetPointerOut: &p,
                                                                       parameterSetSizeOut: &n, parameterSetCountOut: nil, nalUnitHeaderLengthOut: nil)
                }
                if let p { sink.sets.append(Data(bytes: p, count: n)) }
            }
        }
        guard let block = CMSampleBufferGetDataBuffer(sample) else { return }
        var length = 0
        var pointer: UnsafeMutablePointer<Int8>?
        CMBlockBufferGetDataPointer(block, atOffset: 0, lengthAtOffsetOut: nil, totalLengthOut: &length, dataPointerOut: &pointer)
        if let pointer { sink.frames.append((key, Data(bytes: pointer, count: length))) }
    }

    /// Frame `i`: a dark desktop, a window with a title bar, and a block sliding across it, so a screenshot of the
    /// video channel is told apart from the mock's plain RFB screen at a glance.
    static func picture(_ i: Int) throws -> CVPixelBuffer {
        var pb: CVPixelBuffer?
        CVPixelBufferCreate(nil, width, height, kCVPixelFormatType_32BGRA,
                            [kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any]()] as CFDictionary, &pb)
        guard let pb else { throw NSError(domain: "make_fixture", code: 2) }
        CVPixelBufferLockBaseAddress(pb, [])
        defer { CVPixelBufferUnlockBaseAddress(pb, []) }
        let ctx = CGContext(data: CVPixelBufferGetBaseAddress(pb), width: width, height: height, bitsPerComponent: 8,
                            bytesPerRow: CVPixelBufferGetBytesPerRow(pb), space: CGColorSpaceCreateDeviceRGB(),
                            bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)!
        let w = CGFloat(width), h = CGFloat(height)
        ctx.setFillColor(CGColor(red: 0.13, green: 0.20, blue: 0.33, alpha: 1))
        ctx.fill(CGRect(x: 0, y: 0, width: w, height: h))
        let window = CGRect(x: w * 0.12, y: h * 0.14, width: w * 0.76, height: h * 0.68)
        ctx.setFillColor(CGColor(gray: 0.96, alpha: 1))
        ctx.addPath(CGPath(roundedRect: window, cornerWidth: 10, cornerHeight: 10, transform: nil))
        ctx.fillPath()
        ctx.setFillColor(CGColor(gray: 0.86, alpha: 1))
        ctx.fill(CGRect(x: window.minX, y: window.maxY - 28, width: window.width, height: 18))
        for (k, c) in [(0, CGColor(red: 1, green: 0.37, blue: 0.34, alpha: 1)), (1, CGColor(red: 1, green: 0.74, blue: 0.18, alpha: 1)),
                       (2, CGColor(red: 0.16, green: 0.79, blue: 0.25, alpha: 1))] {
            ctx.setFillColor(c)
            ctx.fillEllipse(in: CGRect(x: window.minX + 12 + CGFloat(k) * 18, y: window.maxY - 20, width: 11, height: 11))
        }
        let label = NSAttributedString(string: "Video", attributes: [
            kCTFontAttributeName as NSAttributedString.Key: CTFontCreateWithName("Helvetica-Bold" as CFString, 64, nil),
            kCTForegroundColorAttributeName as NSAttributedString.Key: CGColor(gray: 0.2, alpha: 1)])
        ctx.textPosition = CGPoint(x: window.minX + 40, y: window.midY)
        CTLineDraw(CTLineCreateWithAttributedString(label), ctx)
        let t = CGFloat(i) / CGFloat(fps * seconds)
        ctx.setFillColor(CGColor(red: 0.04, green: 0.52, blue: 1, alpha: 1))
        ctx.fill(CGRect(x: window.minX + 40 + (window.width - 160) * t, y: window.minY + 40, width: 80, height: 50))
        return pb
    }
}
