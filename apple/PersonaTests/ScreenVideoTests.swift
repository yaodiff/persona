#if canImport(PersonaScreenKit)
import CoreVideo
import Foundation
import PersonaScreenKit
import Testing
@testable import Persona

/// The live screen's video channel on the phone (#3954): the caps it asks for, how long it waits to try again, the words
/// it falls back with, decoding the mock's fixture (scripts/mock/screen_video/fixture.bin, make_fixture.sh), the stream
/// against a script, and the link switching the picture to video and back.
struct ScreenVideoTests {
    @Test func theCapFollowsTheNetworkAndThePower() {
        #expect(ScreenVideoBudget.of(expensive: false, constrained: false, lowPower: false, thermal: .nominal) == .init(maxKbps: 12_000, fps: 60))
        #expect(ScreenVideoBudget.of(expensive: true, constrained: false, lowPower: false, thermal: .fair).maxKbps == 4_000, "cellular")
        #expect(ScreenVideoBudget.of(expensive: true, constrained: true, lowPower: false, thermal: .nominal).maxKbps == 1_500, "Low Data Mode")
        #expect(ScreenVideoBudget.of(expensive: false, constrained: false, lowPower: true, thermal: .nominal).fps == 15, "Low Power Mode")
        #expect(ScreenVideoBudget.of(expensive: false, constrained: false, lowPower: false, thermal: .serious).fps == 15)
        #expect(ScreenVideoBudget.of(expensive: false, constrained: false, lowPower: false, thermal: .critical).fps == 15)
    }

    /// With quality tiers (#6396), mobile data follows the user's choice: Data Saver (the default) 1.5 Mbit/s at 1×,
    /// Clearer 4; Low Data Mode is always Data Saver; Wi-Fi is unchanged. Without tiers, as before.
    @Test func mobileDataFollowsTheChoiceWithTiers() {
        func of(_ expensive: Bool, _ constrained: Bool, _ m: ScreenMobileData?) -> ScreenVideoBudget {
            ScreenVideoBudget.of(expensive: expensive, constrained: constrained, lowPower: false, thermal: .nominal, mobileData: m)
        }
        #expect(of(true, false, .saver) == .init(maxKbps: 1_500, fps: 60, maxScale: 1))
        #expect(of(true, false, .clearer) == .init(maxKbps: 4_000, fps: 60))
        #expect(of(true, true, .clearer) == .init(maxKbps: 1_500, fps: 60, maxScale: 1), "Low Data Mode")
        #expect(of(false, false, .saver) == .init(maxKbps: 12_000, fps: 60), "Wi-Fi: the choice is for mobile data only")
        #expect(of(true, false, nil) == .init(maxKbps: 4_000, fps: 60), "no tiers: as before")
    }

    @Test func theWaitDoublesToFiveMinutes() {
        var b = ScreenVideoBackoff()
        let waits = (0..<7).map { _ in b.take() }
        #expect(waits == [.seconds(30), .seconds(60), .seconds(120), .seconds(240), .seconds(300), .seconds(300), .seconds(300)])
        b.reset()
        #expect(b.take() == .seconds(30))
    }

    /// Plain words: why VNC shows, never a protocol or a codec.
    @Test func theFallbackSaysWhyInPlainWords() {
        #expect(ScreenVideoFallback.paused("locked") == .locked)
        #expect(ScreenVideoFallback.paused("login_window") == .locked)
        #expect(ScreenVideoFallback.paused("permission") == .notAllowed)
        #expect(ScreenVideoFallback.paused("display_gone") == .lost)
        #expect(ScreenVideoFallback.paused("busy") == .lost)
        for f in [ScreenVideoFallback.lost, .locked, .notAllowed] {
            for word in ["RFB", "HEVC", "H.264", "codec", "stream", "helper"] {
                #expect(!f.message.localizedCaseInsensitiveContains(word), "\(f): \(f.message)")
            }
        }
    }

    /// "How to turn it on" (#3954): before the node has asked macOS, its alert is about to show on the Mac; once asked
    /// (or when the grant was taken away later), Persona Screen is in System Settings' list.
    @Test func howToAllowDependsOnWhetherMacOSAsked() {
        let window = ScreenVideoFallback.howToAllow(permission: "missing", asked: false)
        let settings = ScreenVideoFallback.howToAllow(permission: "missing", asked: true)
        #expect(window != settings)
        #expect(ScreenVideoFallback.howToAllow(permission: "granted", asked: false) == settings, "taken away while streaming")
        #expect(ScreenVideoFallback.howToAllow(permission: nil, asked: false) == settings)
        for text in [window, settings] {
            #expect(text.contains("Persona Screen"))
            for word in ["RFB", "HEVC", "helper", "TCC"] { #expect(!text.contains(word), "\(text)") }
        }
    }

    @Test func theFixtureDecodes() throws {
        let messages = try Self.messages()
        guard case let .hello(hello) = messages[0], case let .format(format) = messages[1] else {
            Issue.record("the fixture starts with hello and format")
            return
        }
        let decoder = ScreenVideoDecoder(codec: hello.codec)
        try decoder.setFormat(format)
        var decoded = 0
        for case let .frame(f) in messages {
            let pb = try decoder.decode(f)
            #expect(CVPixelBufferGetWidth(pb) == hello.width && CVPixelBufferGetHeight(pb) == hello.height)
            decoded += 1
        }
        #expect(decoded == 90)
    }

    @Test func aFrameBeforeItsFormatDoesNotDecode() throws {
        let messages = try Self.messages()
        guard case let .frame(f) = messages[2] else { Issue.record("a frame third"); return }
        #expect(throws: ScreenVideoDecoder.Failure.noPicture) { try ScreenVideoDecoder(codec: "hevc").decode(f) }
    }

    /// `start` out first; then hello, pictures (a burst shows only its newest) and a report for every frame; the stream
    /// closing ends it.
    @Test func aStreamShowsPicturesAndReportsEveryFrame() async throws {
        let t = VideoScript(try Self.chunks(4), holds: false)
        let seen = Events()
        let s = ScreenVideoStream(transport: t, start: StartMessage(display: nil, maxSize: 1920, fps: 60, maxKbps: 12_000, codecs: ["hevc", "h264"])) {
            seen.add($0)
        }
        s.run()
        try await seen.until { $0.last?.hasPrefix("ended") == true }
        let events = seen.all
        #expect(events.first == "hello hevc 960x600")
        let pictures = events.filter { $0 == "picture 960x600" }.count
        #expect(pictures >= 1 && pictures <= 4, "\(events)")
        #expect(events.last == "ended closed")
        func reports() -> Int { t.sent.filter { $0.count > 4 && $0[4] == ScreenVideoWire.Kind.report.rawValue }.count }
        for _ in 0..<100 where reports() < 90 { try await Task.sleep(for: .milliseconds(10)) } // each goes on its own task
        #expect(t.sent.first?[4] == ScreenVideoWire.Kind.start.rawValue)
        #expect(reports() == 90, "a report a frame")
    }

    @Test func aQuietStreamEnds() async throws {
        let t = VideoScript([try Self.prefix(messages: 2)], holds: true)
        let seen = Events()
        let s = ScreenVideoStream(transport: t, start: Self.start, silence: .milliseconds(300)) { seen.add($0) }
        s.run()
        try await seen.until { $0.last?.hasPrefix("ended") == true }
        #expect(seen.all == ["hello hevc 960x600", "ended silent"])
    }

    /// Paused capture sends no heartbeats: the stream waits for `resumed` instead of counting the silence.
    @Test func aPausedStreamWaitsForResumed() async throws {
        var bytes = try Self.prefix(messages: 3)
        bytes += Array(ScreenVideoMessage.paused(PausedMessage(reason: "locked")).framed())
        let t = VideoScript([bytes], holds: true)
        let seen = Events()
        let s = ScreenVideoStream(transport: t, start: Self.start, silence: .milliseconds(200)) { seen.add($0) }
        s.run()
        try await seen.until { $0.contains("paused locked") }
        try await Task.sleep(for: .milliseconds(800))
        #expect(!seen.all.contains { $0.hasPrefix("ended") }, "\(seen.all)")
        s.close()
    }

    /// The helper's `timing` comes through as it is (a chunk's picture, the newest of its burst, follows its other
    /// messages, so the order within a chunk is not the wire's).
    @Test func theHelpersTimingComesThrough() async throws {
        var bytes = try Self.prefix(messages: 4)
        bytes += Array(ScreenVideoMessage.timing(TimingMessage(captureMs: 0, encodeMs: 5.5, networkMs: 7, kbps: 900, fps: 30)).framed())
        let t = VideoScript([bytes], holds: true)
        let seen = Events()
        let s = ScreenVideoStream(transport: t, start: Self.start) { seen.add($0) }
        s.run()
        try await seen.until { $0.contains { $0.hasPrefix("timing") } }
        #expect(seen.all.contains("timing encode 5.5 network 7.0"), "\(seen.all)")
        #expect(seen.all.first == "hello hevc 960x600")
        #expect(s.receivedBytes == bytes.count, "every byte that came is counted")
        s.close()
    }

    /// The overlay's reading (#3954): at most once a second, frames shown and megabits that came over it, the median
    /// decode, and the helper's segments; the total only when encode, network and decode are all known.
    @Test func theOverlayReadsOnceASecond() {
        var tally = ScreenVideoTally()
        let t0 = ContinuousClock.now
        #expect(tally.reading(at: t0, received: 0) == nil, "no picture yet")
        for (i, ms) in [4.0, 6.0, 5.0, 30.0].enumerated() { tally.picture(decodeMs: ms, at: t0.advanced(by: .milliseconds(250 * i))) }
        #expect(tally.reading(at: t0.advanced(by: .milliseconds(900)), received: 200_000) == nil, "not a second yet")
        let first = tally.reading(at: t0.advanced(by: .seconds(1)), received: 250_000)
        #expect(first == ScreenVideoReading(fps: 4, mbps: 2, captureMs: nil, encodeMs: nil, networkMs: nil, decodeMs: 5.5))
        #expect(first?.totalMs == nil, "no helper timing (a helper before it): no total")
        tally.helper(TimingMessage(captureMs: 0, encodeMs: 5, networkMs: 8, kbps: 1000, fps: 2))
        tally.picture(decodeMs: 3, at: t0.advanced(by: .milliseconds(1500)))
        tally.picture(decodeMs: 5, at: t0.advanced(by: .milliseconds(1700)))
        let second = tally.reading(at: t0.advanced(by: .seconds(2)), received: 375_000)
        #expect(second == ScreenVideoReading(fps: 2, mbps: 1, captureMs: 0, encodeMs: 5, networkMs: 8, decodeMs: 4))
        #expect(second?.totalMs == 17, "capture 0 + encode 5 + network 8 + decode 4")
    }

    /// The round trip to the hub the overlay shows (#6558): the median of the last five from the last 20 s, as the web
    /// overlay's, so one slow answer doesn't make it jump and an old one isn't shown.
    @Test func theHubRoundTripIsTheMedianOfTheRecentOnes() {
        let t0 = Date(timeIntervalSince1970: 1_000)
        var trips = HubRoundTrips()
        #expect(trips.median(at: t0) == nil, "none yet: no number")
        trips.add(ms: .nan, at: t0)
        trips.add(ms: -1, at: t0)
        #expect(trips.median(at: t0) == nil, "not a time: not counted")
        trips.add(ms: 20, at: t0)
        #expect(trips.median(at: t0) == 20)
        trips.add(ms: 400, at: t0.addingTimeInterval(2)) // one slow answer
        trips.add(ms: 22, at: t0.addingTimeInterval(4))
        #expect(trips.median(at: t0.addingTimeInterval(4)) == 22)
        for i in 0..<HubRoundTrips.kept { trips.add(ms: 30, at: t0.addingTimeInterval(6 + Double(i))) }
        #expect(trips.median(at: t0.addingTimeInterval(10)) == 30, "only the last five count")
        #expect(trips.median(at: t0.addingTimeInterval(10 + HubRoundTrips.fresh + 1)) == nil, "stale: the app was away")
    }

    /// Bitrates as the overlay writes them: two decimals under 1 Mbps, so a small stream doesn't read as 0.0 (the web's
    /// rule).
    @Test func smallBitratesKeepTwoDecimals() {
        #expect(ScreenStats.mbpsText(12.34) == "12.3 Mbps")
        #expect(ScreenStats.mbpsText(1) == "1.0 Mbps")
        #expect(ScreenStats.mbpsText(0.034) == "0.03 Mbps")
    }

    @Test func aFrameThatDoesNotDecodeEnds() async throws {
        var bytes = try Self.prefix(messages: 2)
        bytes += Array(ScreenVideoMessage.frame(VideoFrame(number: 1, captureMicros: 0, flags: .keyframe, sample: Data([0, 0, 0, 4, 1, 2, 3, 4]))).framed())
        let t = VideoScript([bytes], holds: true)
        let seen = Events()
        let s = ScreenVideoStream(transport: t, start: Self.start) { seen.add($0) }
        s.run()
        try await seen.until { $0.last?.hasPrefix("ended") == true }
        #expect(seen.all.last == "ended undecodable")
    }

    /// The link: RFB signed in and video wanted opens the stream; the first picture makes video the picture (RFB's
    /// updates held); the stream ending puts RFB back, says why, and tries again in 30 s. A new RFB connection starts
    /// afresh, with nothing to explain.
    @MainActor @Test func theLinkSwitchesToVideoAndBack() async throws {
        let link = ScreenVideoLink()
        var shows: [Bool] = []
        link.onShowing = { shows.append($0) }
        let t = VideoScript(try Self.chunks(4), holds: false)
        var opened: [ScreenVideoLink.Ready] = []
        link.transport = { r in opened.append(r); return t }
        link.want(.init(display: nil, rect: nil))
        #expect(opened.isEmpty, "no RFB yet")
        link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
        #expect(opened.map(\.pair) == ["0123"])
        for _ in 0..<300 where link.fallback == nil { try await Task.sleep(for: .milliseconds(10)) }
        #expect(shows == [true, false])
        #expect(link.fallback == .lost)
        #expect(!link.showing)
        let wait = (link.retryAt ?? .distantPast).timeIntervalSinceNow
        #expect(wait > 25 && wait <= 30)
        link.rfb(nil)
        #expect(link.retryAt == nil, "a new connection tries at once")
        #expect(link.fallback == .lost, "kept: the page shows it only while VNC is live, so a reconnection doesn't blink it")
    }

    /// The line goes by itself once video is back: the Mac locked (`paused`), then unlocked (`resumed`, a fresh
    /// hello, format and keyframe).
    @MainActor @Test func theLineGoesWhenVideoIsBack() async throws {
        let link = ScreenVideoLink()
        var said: [ScreenVideoFallback?] = []
        link.onFallback = { said.append($0) }
        let t = VideoScript([try Self.prefix(messages: 4)], holds: true)
        link.transport = { _ in t }
        link.want(.init(display: nil, rect: nil))
        link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
        try await Self.until { link.showing }
        t.feed(Array(ScreenVideoMessage.paused(PausedMessage(reason: "locked")).framed()))
        try await Self.until { link.fallback == .locked }
        #expect(!link.showing)
        t.feed(Array(ScreenVideoMessage.resumed.framed()) + (try Self.prefix(messages: 4)))
        try await Self.until { link.showing }
        #expect(link.fallback == nil)
        #expect(said == [.locked, nil])
        link.want(nil)
    }

    /// ⋯ › Quality only once the helper says it has quality tiers (#6396): the fixture's helper, from before them, says
    /// nothing, so none is offered; a hello with `features: ["tiers"]` offers it.
    @MainActor @Test func qualityWaitsForAHelperWithTiers() async throws {
        let link = ScreenVideoLink()
        let t = VideoScript([try Self.prefix(messages: 4)], holds: true)
        link.transport = { _ in t }
        link.want(.init(display: nil, rect: nil))
        link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
        try await Self.until { link.showing }
        #expect(!link.tiers)
        guard case var .hello(h) = try Self.messages()[0] else { Issue.record("the fixture starts with hello"); return }
        h.features = ["tiers"]
        t.feed(Array(ScreenVideoMessage.hello(h).framed()))
        try await Self.until { link.tiers }
        link.want(nil)
    }

    /// ⋯ › Quality needs this app's side of the tiers too (#6396): a release can pair a helper with tiers and an app
    /// without them, and then nothing is offered. This app has them.
    @Test func qualityAlsoWaitsForThisAppsTiers() {
        #expect(ScreenTiers.appSupported, "the iPhone's tier policy is in (#6396)")
        #expect(ScreenTiers.offered(helperHasTiers: true))
        #expect(!ScreenTiers.offered(helperHasTiers: true, appSupported: false))
        #expect(ScreenTiers.offered(helperHasTiers: true, appSupported: true))
        #expect(!ScreenTiers.offered(helperHasTiers: false, appSupported: true))
    }

    /// A helper with tiers (#6396): its hello starts the policy, whose first tier goes to the helper as an update
    /// (scale, frame rate, band and target) and to the link as an event. An older helper gets no tier at all.
    @Test func aHelperWithTiersGetsATier() async throws {
        guard case var .hello(h) = try Self.messages()[0] else { Issue.record("the fixture starts with hello"); return }
        h.features = ["tiers"]
        h.points = [1512, 982]
        h.pixels = [3024, 1964]
        let rest = try Self.messages().dropFirst().prefix(3).flatMap { Array($0.framed()) }
        let t = VideoScript([Array(ScreenVideoMessage.hello(h).framed()) + rest], holds: true)
        let seen = Events()
        let context = ScreenVideoTiers.Context(bias: .auto, budget: .init(maxKbps: 12_000, fps: 60), maxSize: 2868,
                                               link: LinkStats(kind: .relay, via: .wifi), remembered: nil)
        let s = ScreenVideoStream(transport: t, start: Self.start, tiers: ScreenVideoTiers(context)) { seen.add($0) }
        s.run()
        try await seen.until { $0.contains { $0.hasPrefix("tier") } }
        #expect(seen.all.contains("tier 2268x1472@60 band 4006-12000 target 4006 start"), "\(seen.all)")
        let updates = t.sent.filter { $0.count > 4 && $0[4] == ScreenVideoWire.Kind.update.rawValue }
        var parser = ScreenVideoParser()
        let raws = try updates.flatMap { try parser.push(Data($0)) }
        let sent = try raws.map { try ScreenVideoMessage.decode(type: $0.type, body: $0.body) }
        #expect(sent.contains(.update(UpdateMessage(fps: 60, maxKbps: 12_000, scale: 1.5, minKbps: 4_006, targetKbps: 4_006))))
        s.close()

        let old = VideoScript([try Self.prefix(messages: 4)], holds: true)
        let quiet = Events()
        let o = ScreenVideoStream(transport: old, start: Self.start, tiers: ScreenVideoTiers(context)) { quiet.add($0) }
        o.run()
        try await quiet.until { $0.contains { $0.hasPrefix("picture") } }
        #expect(!quiet.all.contains { $0.hasPrefix("tier") })
        #expect(!old.sent.contains { $0.count > 4 && $0[4] == ScreenVideoWire.Kind.update.rawValue }, "nothing new for an old helper")
        o.close()
    }

    /// Connection Stats' tier line (#6396): the Quality choice, the scale and frame rate; and why it last changed, in
    /// plain words, with how long ago.
    @MainActor @Test func theTierInPlainWords() {
        let t = TierLadder.Tier(scale: 1.5, fps: 60, width: 2268, height: 1472, minKbps: 4_006, maxKbps: 12_000)
        #expect(ScreenTierText.value(.auto, t) == "\(ScreenQuality.auto.label) · 1.5× · 60 fps")
        let one = TierLadder.Tier(scale: 1, fps: 15, width: 1512, height: 982, minKbps: 200, maxKbps: 1_782)
        #expect(ScreenTierText.value(.sharper, one).hasSuffix(" · 1× · 15 fps"))
        let at = Date(timeIntervalSince1970: 1_000)
        #expect(ScreenTierText.reason(.linkSlowed, at: at, now: at.addingTimeInterval(12))
            == ScreenTierText.words(.linkSlowed) + " · " + String(localized: "\(12) s ago"))
        #expect(ScreenTierText.reason(.choice, at: at, now: at.addingTimeInterval(150)).hasSuffix(String(localized: "\(2) min ago")))
        for r in [TierReason.start, .linkSlowed, .linkSteady, .networkChanged, .phoneWarm, .phoneBehind, .choice] {
            #expect(!ScreenTierText.words(r).isEmpty)
        }
    }

    /// After a size change in place (#6396), the overlay's resolution is what the encoder runs at (`encoding`), not
    /// what `hello` said.
    @MainActor @Test func theSizeFollowsEncoding() async throws {
        let link = ScreenVideoLink()
        let t = VideoScript([try Self.prefix(messages: 4)], holds: true)
        link.transport = { _ in t }
        link.want(.init(display: nil, rect: nil))
        link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
        try await Self.until { link.showing }
        let before = link.size
        t.feed(Array(ScreenVideoMessage.encoding(EncodingMessage(width: 1512, height: 982, fps: 30, scale: 1, minKbps: 891,
                                                                  maxKbps: 3_563)).framed()))
        try await Self.until { link.size == CGSize(width: 1512, height: 982) }
        #expect(before != link.size)
        link.want(nil)
    }

    /// Retries that fail as the first drop did keep the same line: it never blinks off and on between them.
    @MainActor @Test func aRetryThatFailsKeepsTheLineSteady() async throws {
        let link = ScreenVideoLink(backoff: .init(first: .milliseconds(40), longest: .milliseconds(80)))
        var said: [ScreenVideoFallback?] = []
        link.onFallback = { said.append($0) }
        var opens = 0
        let first = VideoScript([try Self.prefix(messages: 4)], holds: false)
        link.transport = { _ in
            opens += 1
            return opens == 1 ? first : nil // the hub or the node says no from then on
        }
        link.want(.init(display: nil, rect: nil))
        link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
        try await Self.until { opens >= 5 }
        #expect(said == [.lost], "\(said)")
        #expect(!link.showing)
        link.want(nil)
        #expect(said == [.lost, nil])
    }

    /// Video that keeps dropping soon after it comes back waits twice as long each time (30 s, 1 min, 2 min… in the
    /// app), so the picture and its line don't flip back and forth; video that stayed up starts the waits over.
    @MainActor @Test func videoThatKeepsDroppingWaitsLongerEachTime() async throws {
        for (stable, want) in [(Duration.seconds(60), [40, 80, 160]), (Duration.zero, [40, 40, 40])] {
            let link = ScreenVideoLink(backoff: .init(first: .milliseconds(40), longest: .seconds(1)), stable: stable)
            var waits: [Int] = []
            link.onRetry = { waits.append(Int($0.components.attoseconds / 1_000_000_000_000_000)) }
            let pieces = [try Self.prefix(messages: 4), Array(ScreenVideoMessage.heartbeat(HeartbeatMessage(last: 2)).framed())]
            link.transport = { _ in VideoScript(pieces, holds: false) } // a few frames, then it drops
            link.want(.init(display: nil, rect: nil))
            link.rfb(.init(group: "", machine: "macbook", pair: "0123"))
            try await Self.until(seconds: 5) { waits.count >= 3 }
            #expect(Array(waits.prefix(3)) == want, "stable \(stable): \(waits)")
            link.want(nil)
        }
    }

    /// Waits (polling the main actor) until `done`, at most `seconds`.
    @MainActor static func until(seconds: Double = 3, _ done: () -> Bool) async throws {
        let end = Date.now.addingTimeInterval(seconds)
        while !done(), Date.now < end { try await Task.sleep(for: .milliseconds(10)) }
        #expect(done(), "never came")
    }

    // MARK: Fixture

    static let start = StartMessage(display: nil, maxSize: 1920, fps: 60, maxKbps: 12_000, codecs: ["hevc", "h264"])

    static func fixture() throws -> Data {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try Data(contentsOf: root.appendingPathComponent("scripts/mock/screen_video/fixture.bin"))
    }

    static func messages() throws -> [ScreenVideoMessage] {
        var p = ScreenVideoParser()
        return try p.push(try fixture()).map { try ScreenVideoMessage.decode(type: $0.type, body: $0.body) }
    }

    /// The fixture's first `count` messages, framed.
    static func prefix(messages count: Int) throws -> [UInt8] {
        try messages().prefix(count).flatMap { Array($0.framed()) }
    }

    /// The fixture's bytes in `n` pieces cut anywhere (a message may span two).
    static func chunks(_ n: Int) throws -> [[UInt8]] {
        let all = [UInt8](try fixture())
        let size = (all.count + n - 1) / n
        return stride(from: 0, to: all.count, by: size).map { Array(all[$0..<min($0 + size, all.count)]) }
    }
}

/// The stream's events as words, in order.
private final class Events: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [String] = []
    var all: [String] { lock.withLock { events } }

    func add(_ e: ScreenVideoStream.Event) {
        let word: String
        switch e {
        case let .hello(h): word = "hello \(h.codec) \(h.width)x\(h.height)"
        case let .picture(pb, _): word = "picture \(CVPixelBufferGetWidth(pb))x\(CVPixelBufferGetHeight(pb))"
        case let .paused(why): word = "paused \(why)"
        case .resumed: word = "resumed"
        case let .timing(t): word = "timing encode \(t.encodeMs ?? -1) network \(t.networkMs ?? -1)"
        case let .tier(c):
            word = "tier \(c.tier.width)x\(c.tier.height)@\(c.tier.fps) band \(c.tier.minKbps)-\(c.tier.maxKbps) target \(c.targetKbps) \(c.reason.rawValue)"
        case let .encoding(e): word = "encoding \(e.width)x\(e.height)@\(e.fps)"
        case let .ended(why): word = "ended \(why)"
        }
        lock.withLock { events.append(word) }
    }

    func until(_ done: ([String]) -> Bool) async throws {
        for _ in 0..<500 where !done(all) { try await Task.sleep(for: .milliseconds(10)) }
        #expect(done(all), "never came: \(all)")
    }
}

/// A video stream's other side, from a script: each receive is the next chunk; then it closes, or with `holds` stays
/// open and quiet.
private final class VideoScript: RFBTransport, @unchecked Sendable {
    private let lock = NSLock()
    private var chunks: [[UInt8]]
    private let holds: Bool
    private var _sent: [[UInt8]] = []
    var sent: [[UInt8]] { lock.withLock { _sent } }

    init(_ chunks: [[UInt8]], holds: Bool) {
        self.chunks = chunks
        self.holds = holds
    }

    func send(_ bytes: [UInt8]) async throws { lock.withLock { _sent.append(bytes) } }

    /// More from the other side, later (a `holds` script).
    func feed(_ bytes: [UInt8]) { lock.withLock { chunks.append(bytes) } }

    func receive() async throws -> [UInt8] {
        try await Task.sleep(for: .milliseconds(5))
        while holds, lock.withLock({ chunks.isEmpty }) { try await Task.sleep(for: .milliseconds(20)) }
        return try lock.withLock {
            guard !chunks.isEmpty else { throw URLError(.networkConnectionLost) }
            return chunks.removeFirst()
        }
    }

    func close() {}
}
#endif
