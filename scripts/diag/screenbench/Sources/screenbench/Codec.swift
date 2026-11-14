import CoreMedia
import CoreVideo
import Foundation
import VideoToolbox

/// Persona Screen's encoder (`ScreenCaptureSource.makeEncoder`, apple/PersonaLink): the same VideoToolbox settings, so
/// the bench's bytes and pictures are the helper's. Each frame is drained before the next goes in (the helper's
/// encoder holds at most one frame anyway: `MaxFrameDelayCount` 0).
final class Encoder {
    let session: VTCompressionSession
    private(set) var kbps: Int
    /// Frames the encoder dropped (rate control out of budget) or failed, and the last failure's status.
    private(set) var dropped = 0
    private(set) var lastStatus: OSStatus = noErr

    init?(codec: String, width: Int, height: Int, fps: Int, kbps: Int) {
        let type = codec == "hevc" ? kCMVideoCodecType_HEVC : kCMVideoCodecType_H264
        let spec = [kVTVideoEncoderSpecification_EnableLowLatencyRateControl: kCFBooleanTrue] as CFDictionary
        var made: VTCompressionSession?
        guard VTCompressionSessionCreate(allocator: nil, width: Int32(width), height: Int32(height), codecType: type,
                                         encoderSpecification: spec, imageBufferAttributes: nil, compressedDataAllocator: nil,
                                         outputCallback: nil, refcon: nil, compressionSessionOut: &made) == noErr,
              let session = made else { return nil }
        func set(_ key: CFString, _ value: CFTypeRef) { VTSessionSetProperty(session, key: key, value: value) }
        set(kVTCompressionPropertyKey_RealTime, kCFBooleanTrue)
        set(kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse)
        set(kVTCompressionPropertyKey_MaxFrameDelayCount, NSNumber(value: 0))
        set(kVTCompressionPropertyKey_ProfileLevel,
            codec == "hevc" ? kVTProfileLevel_HEVC_Main_AutoLevel : kVTProfileLevel_H264_High_AutoLevel)
        set(kVTCompressionPropertyKey_ExpectedFrameRate, NSNumber(value: fps))
        set(kVTCompressionPropertyKey_MaxKeyFrameIntervalDuration, NSNumber(value: 10))
        set(kVTCompressionPropertyKey_AverageBitRate, NSNumber(value: kbps * 1000))
        VTCompressionSessionPrepareToEncodeFrames(session)
        self.session = session
        self.kbps = kbps
    }

    deinit { VTCompressionSessionInvalidate(session) }

    func setKbps(_ k: Int) {
        kbps = k
        VTSessionSetProperty(session, key: kVTCompressionPropertyKey_AverageBitRate, value: NSNumber(value: k * 1000))
    }

    /// One frame in, its encoded sample out (nil when the encoder dropped it).
    func encode(_ image: CVPixelBuffer, pts: CMTime, keyframe: Bool = false) -> CMSampleBuffer? {
        let box = Box()
        let options = keyframe ? [kVTEncodeFrameOptionKey_ForceKeyFrame: kCFBooleanTrue] as CFDictionary : nil
        let submitted = VTCompressionSessionEncodeFrame(session, imageBuffer: image, presentationTimeStamp: pts, duration: .invalid,
                                                        frameProperties: options, infoFlagsOut: nil) { status, _, sample in
            box.set(status, sample)
        }
        VTCompressionSessionCompleteFrames(session, untilPresentationTimeStamp: .invalid)
        let (status, sample) = box.get()
        lastStatus = submitted != noErr ? submitted : status
        if sample == nil { dropped += 1 }
        return sample
    }

    private final class Box: @unchecked Sendable {
        private let lock = NSLock()
        private var status: OSStatus = noErr
        private var value: CMSampleBuffer?
        func set(_ s: OSStatus, _ v: CMSampleBuffer?) { lock.withLock { status = s; value = v } }
        func get() -> (OSStatus, CMSampleBuffer?) { lock.withLock { (status, value) } }
    }

    static func bytes(_ sample: CMSampleBuffer) -> Int { sample.dataBuffer.map { CMBlockBufferGetDataLength($0) } ?? 0 }
}

/// The phone's decoder (`ScreenVideoDecoder`): VideoToolbox, one frame at a time, NV12 out.
final class Decoder {
    private var session: VTDecompressionSession?
    private var format: CMFormatDescription?

    deinit { if let session { VTDecompressionSessionInvalidate(session) } }

    func decode(_ sample: CMSampleBuffer) -> CVPixelBuffer? {
        guard let f = sample.formatDescription else { return nil }
        if format == nil || !CMFormatDescriptionEqual(format, otherFormatDescription: f) {
            if let session { VTDecompressionSessionInvalidate(session) }
            let attrs = [kCVPixelBufferPixelFormatTypeKey as String: kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
                         kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any]()] as CFDictionary
            var s: VTDecompressionSession?
            VTDecompressionSessionCreate(allocator: nil, formatDescription: f, decoderSpecification: nil, imageBufferAttributes: attrs,
                                         outputCallback: nil, decompressionSessionOut: &s)
            session = s
            format = f
        }
        guard let session else { return nil }
        var out: CVPixelBuffer?
        let lock = NSLock()
        VTDecompressionSessionDecodeFrame(session, sampleBuffer: sample, flags: [], infoFlagsOut: nil) { status, _, image, _, _ in
            if status == noErr { lock.withLock { out = image } }
        }
        return lock.withLock { out }
    }
}

/// Size and pixel format changes, as ScreenCaptureKit does them for the helper (full-size BGRA to the tier's NV12) and
/// back up for comparing with the full-size screen.
final class Scaler {
    private let session: VTPixelTransferSession

    init() {
        var s: VTPixelTransferSession?
        VTPixelTransferSessionCreate(allocator: nil, pixelTransferSessionOut: &s)
        session = s!
    }

    deinit { VTPixelTransferSessionInvalidate(session) }

    func convert(_ source: CVPixelBuffer, width: Int, height: Int,
                 format: OSType = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange) -> CVPixelBuffer {
        let out = Self.buffer(width: width, height: height, format: format)
        VTPixelTransferSessionTransferImage(session, from: source, to: out)
        return out
    }

    static func buffer(width: Int, height: Int, format: OSType) -> CVPixelBuffer {
        var pb: CVPixelBuffer?
        let attrs = [kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any]()] as CFDictionary
        CVPixelBufferCreate(nil, width, height, format, attrs, &pb)
        return pb!
    }
}

/// SSIM of two same-size NV12 pictures' luma, over 8×8 blocks (the standard constants for 8-bit samples).
enum SSIM {
    static func luma(_ a: CVPixelBuffer, _ b: CVPixelBuffer) -> Double {
        CVPixelBufferLockBaseAddress(a, .readOnly)
        CVPixelBufferLockBaseAddress(b, .readOnly)
        defer {
            CVPixelBufferUnlockBaseAddress(a, .readOnly)
            CVPixelBufferUnlockBaseAddress(b, .readOnly)
        }
        let w = min(CVPixelBufferGetWidthOfPlane(a, 0), CVPixelBufferGetWidthOfPlane(b, 0))
        let h = min(CVPixelBufferGetHeightOfPlane(a, 0), CVPixelBufferGetHeightOfPlane(b, 0))
        guard let pa = CVPixelBufferGetBaseAddressOfPlane(a, 0)?.assumingMemoryBound(to: UInt8.self),
              let pb = CVPixelBufferGetBaseAddressOfPlane(b, 0)?.assumingMemoryBound(to: UInt8.self) else { return 0 }
        let ra = CVPixelBufferGetBytesPerRowOfPlane(a, 0), rb = CVPixelBufferGetBytesPerRowOfPlane(b, 0)
        let c1 = 6.5025, c2 = 58.5225
        var total = 0.0, blocks = 0
        var y = 0
        while y + 8 <= h {
            var x = 0
            while x + 8 <= w {
                var sa = 0, sb = 0, saa = 0, sbb = 0, sab = 0
                for dy in 0..<8 {
                    let rowA = pa + (y + dy) * ra + x, rowB = pb + (y + dy) * rb + x
                    for dx in 0..<8 {
                        let va = Int(rowA[dx]), vb = Int(rowB[dx])
                        sa += va; sb += vb; saa += va * va; sbb += vb * vb; sab += va * vb
                    }
                }
                let n = 64.0
                let ma = Double(sa) / n, mb = Double(sb) / n
                let va = Double(saa) / n - ma * ma, vb = Double(sbb) / n - mb * mb, cov = Double(sab) / n - ma * mb
                total += ((2 * ma * mb + c1) * (2 * cov + c2)) / ((ma * ma + mb * mb + c1) * (va + vb + c2))
                blocks += 1
                x += 8
            }
            y += 8
        }
        return blocks > 0 ? total / Double(blocks) : 0
    }
}
