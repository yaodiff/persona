import CoreMedia
import CoreVideo
import Foundation
import PersonaScreenKit

// screenbench (#6396): every quality tier × scene × end of its bitrate band, encoded the way Persona Screen encodes,
// decoded the way the phone decodes. See Package.swift; docs/extensions/computer-use.md "Quality tiers".
//
//   swift run -c release --package-path scripts/diag/screenbench screenbench [options]
//     --points 1512x982 --pixels 3024x1964   the Mac's display (default: a 14" MacBook Pro)
//     --max-size 2868 --cap 12000 --fps 60   the phone's long edge, bitrate cap (kbit/s) and frame-rate cap
//     --bias auto|sharper|smoother           which ladder
//     --seconds 6 --scenes typing,scroll,video,scroll-stop --codec hevc|h264
//     --json <file>                          one JSON line per run, appended
//
// Prints a table per run and, per tier, the data an hour of a typical mix uses at the band's floor and ceiling.

struct Options {
    var points = (width: 1512, height: 982)
    var pixels = (width: 3024, height: 1964)
    var maxSize = 2868, cap = 12_000, maxFps = 60, seconds = 6.0
    var bias = TierLadder.Bias.auto
    var scenes = Screen.Scene.allCases
    var codec = "hevc"
    var json: String?

    init(_ args: [String]) {
        var i = 0
        func next() -> String { i += 1; return i < args.count ? args[i] : "" }
        func pair(_ s: String) -> (width: Int, height: Int) {
            let p = s.split(separator: "x").compactMap { Int($0) }
            return p.count == 2 ? (p[0], p[1]) : (0, 0)
        }
        while i < args.count {
            switch args[i] {
            case "--points": points = pair(next())
            case "--pixels": pixels = pair(next())
            case "--max-size": maxSize = Int(next()) ?? maxSize
            case "--cap": cap = Int(next()) ?? cap
            case "--fps": maxFps = Int(next()) ?? maxFps
            case "--seconds": seconds = Double(next()) ?? seconds
            case "--bias": bias = TierLadder.Bias(rawValue: next()) ?? bias
            case "--scenes": scenes = next().split(separator: ",").compactMap { Screen.Scene(rawValue: String($0)) }
            case "--codec": codec = next()
            case "--json": json = next()
            default: FileHandle.standardError.write(Data("screenbench: unknown option \(args[i])\n".utf8)); exit(2)
            }
            i += 1
        }
    }
}

struct Run: Encodable {
    var tier: String, scale: Double, fps: Int, width: Int, height: Int
    var scene: String, end: String, targetKbps: Int
    /// What went out while the scene moved, the opening keyframe left out, in kbit/s.
    var kbps: Double
    var keyframeKB: Double, frames: Int
    var ssimMean: Double, ssimMin: Double
    /// The last moving picture, scaled back up to the display's pixels, against the display: what resolution costs.
    var fullSsim: Double
    /// After each idle-refinement pass: SSIM against what was encoded, against the display, and the pass's KB.
    var refined: [[Double]]
}

func run(_ tier: TierLadder.Tier, scene: Screen.Scene, end: String, kbps target: Int, screen: Screen, options: Options) -> Run? {
    guard let encoder = Encoder(codec: options.codec, width: tier.width, height: tier.height, fps: tier.fps, kbps: target) else {
        FileHandle.standardError.write(Data("screenbench: no \(options.codec) encoder at \(tier.width)x\(tier.height)\n".utf8))
        return nil
    }
    let decoder = Decoder(), scaler = Scaler()
    let full = Scaler.buffer(width: screen.pixels.width, height: screen.pixels.height, format: kCVPixelFormatType_32BGRA)
    let moving = scene == .scrollStop ? 2.0 : options.seconds
    var lastKey: Int?, frames = 0, bytes = 0, keyframe = 0
    var ssims: [Double] = []
    var lastDecoded: CVPixelBuffer?
    let ticks = Int(options.seconds * Double(tier.fps))
    for i in 0..<ticks {
        let t = Double(i) / Double(tier.fps)
        let key = screen.key(scene, at: t)
        if key == lastKey { continue } // nothing changed: ScreenCaptureKit sends no frame
        lastKey = key
        screen.render(scene, at: t, into: full)
        let input = scaler.convert(full, width: tier.width, height: tier.height)
        guard let sample = encoder.encode(input, pts: CMTime(value: CMTimeValue(i), timescale: CMTimeScale(tier.fps)),
                                          keyframe: frames == 0) else { continue }
        if ProcessInfo.processInfo.environment["SCREENBENCH_TRACE"] != nil {
            FileHandle.standardError.write(Data("trace \(label(tier)) \(scene.rawValue) \(end) t=\(String(format: "%.2f", t)) bytes=\(Encoder.bytes(sample)) dropped-so-far=\(encoder.dropped)\n".utf8))
        }
        let b = Encoder.bytes(sample)
        if frames == 0 { keyframe = b } else { bytes += b }
        frames += 1
        if let decoded = decoder.decode(sample) {
            ssims.append(SSIM.luma(input, decoded))
            lastDecoded = decoded
        }
    }
    guard let lastDecoded else { return nil }
    let reference = scaler.convert(full, width: screen.pixels.width, height: screen.pixels.height)
    func fullSize(_ decoded: CVPixelBuffer) -> Double {
        SSIM.luma(reference, scaler.convert(decoded, width: screen.pixels.width, height: screen.pixels.height))
    }
    let fullSsim = fullSize(lastDecoded)

    // Idle refinement, as the helper does it (Refinement): the newest picture again at the boosted bitrate, 300 ms after
    // the last change and then every 200 ms, three passes; a pass the encoder drops is tried again, up to its attempts.
    // The screen's newest picture is the last one rendered, even if the encoder dropped it.
    var refined: [[Double]] = []
    let newest = scaler.convert(full, width: tier.width, height: tier.height)
    var tries = 0, at = Double(ticks) / Double(tier.fps) + 0.3
    while refined.count < Refinement.passes, tries < Refinement.attempts {
        tries += 1
        encoder.setKbps(Refinement.kbps(target: target))
        let sample = encoder.encode(newest, pts: CMTime(seconds: at, preferredTimescale: 600))
        encoder.setKbps(target)
        at += 0.2
        if ProcessInfo.processInfo.environment["SCREENBENCH_TRACE"] != nil {
            FileHandle.standardError.write(Data("trace refine try \(tries) at=\(at) bytes=\(sample.map(Encoder.bytes) ?? -1)\n".utf8))
        }
        guard let sample, let decoded = decoder.decode(sample) else { continue }
        refined.append([round3(SSIM.luma(newest, decoded)), round3(fullSize(decoded)), round1(Double(Encoder.bytes(sample)) / 1000)])
    }
    if encoder.dropped > 0 {
        FileHandle.standardError.write(Data("screenbench: \(label(tier)) \(scene.rawValue) \(end): \(encoder.dropped) frames dropped, last status \(encoder.lastStatus)\n".utf8))
    }

    return Run(tier: label(tier), scale: tier.scale, fps: tier.fps, width: tier.width, height: tier.height,
               scene: scene.rawValue, end: end, targetKbps: target,
               kbps: round1(Double(bytes) * 8 / 1000 / moving), keyframeKB: round1(Double(keyframe) / 1000), frames: frames,
               ssimMean: round3(ssims.reduce(0, +) / Double(max(ssims.count, 1))), ssimMin: round3(ssims.min() ?? 0),
               fullSsim: round3(fullSsim), refined: refined)
}

func label(_ t: TierLadder.Tier) -> String {
    let s = t.scale == t.scale.rounded() ? String(Int(t.scale)) : String(t.scale)
    return "\(s)x\(t.fps)"
}

func round1(_ v: Double) -> Double { (v * 10).rounded() / 10 }
func round3(_ v: Double) -> Double { (v * 1000).rounded() / 1000 }

let options = Options(Array(CommandLine.arguments.dropFirst()))
let screen = Screen(points: options.points, pixels: options.pixels)
// The codec's ladder, as the phone's and the browser's: its bands follow `TierLadder.bitsFactor`.
let ladder = TierLadder.make(points: options.points, pixels: options.pixels, maxSize: options.maxSize, bias: options.bias,
                             capKbps: options.cap, maxFps: options.maxFps, codec: options.codec)
print("screenbench: \(options.codec), display \(options.points.width)x\(options.points.height) pt / "
    + "\(options.pixels.width)x\(options.pixels.height) px, phone cap \(options.maxSize) px \(options.cap) kbit/s, \(options.bias) ladder")
print("| tier | size | scene | end | target kbit/s | sent kbit/s | keyframe KB | SSIM mean / min | full-size SSIM | refined SSIM / full-size (KB a pass) |")
print("|---|---|---|---|---|---|---|---|---|---|")
var all: [Run] = []
let jsonOut = options.json.flatMap { path -> FileHandle? in
    if !FileManager.default.fileExists(atPath: path) { FileManager.default.createFile(atPath: path, contents: nil) }
    let h = FileHandle(forWritingAtPath: path)
    h?.seekToEndOfFile()
    return h
}
for tier in ladder {
    for scene in options.scenes {
        for (end, kbps) in [("floor", tier.minKbps), ("ceiling", tier.maxKbps)] {
            guard let r = run(tier, scene: scene, end: end, kbps: kbps, screen: screen, options: options) else { continue }
            all.append(r)
            let refined = r.refined.map { "\($0[0]) / \($0[1]) (\($0[2]))" }.joined(separator: ", ")
            print("| \(r.tier) | \(r.width)x\(r.height) | \(r.scene) | \(r.end) | \(r.targetKbps) | \(r.kbps) | \(r.keyframeKB) | "
                + "\(r.ssimMean) / \(r.ssimMin) | \(r.fullSsim) | \(refined) |")
            if let jsonOut, let line = try? JSONEncoder().encode(r) {
                jsonOut.write(line + Data("\n".utf8))
            }
        }
    }
}

// An hour of a typical mix: 40% still (a still moment every 10 s, refined), 25% typing, 20% scrolling, 15% video.
print("\n| tier | end | MB an hour (40% still, 25% typing, 20% scrolling, 15% video) |")
print("|---|---|---|")
for tier in ladder {
    for end in ["floor", "ceiling"] {
        let runs = all.filter { $0.tier == label(tier) && $0.end == end }
        func kbps(_ s: Screen.Scene) -> Double? { runs.first { $0.scene == s.rawValue }?.kbps }
        guard let typing = kbps(.typing), let scroll = kbps(.scroll), let video = kbps(.video) else { continue }
        let refineKB = runs.first { $0.scene == Screen.Scene.scrollStop.rawValue }?.refined.reduce(0) { $0 + $1[2] } ?? 0
        let moving = (0.25 * typing + 0.20 * scroll + 0.15 * video) * 3600 / 8 / 1000 // MB
        let still = 0.40 * 3600 / 10 * refineKB / 1000
        print("| \(label(tier)) | \(end) | \(round1(moving + still)) |")
    }
}
