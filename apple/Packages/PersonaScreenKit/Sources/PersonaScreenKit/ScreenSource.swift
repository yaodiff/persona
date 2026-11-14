import Foundation

/// A source of encoded video for one stream: the capture-and-encode side of the helper. The real source
/// (`ScreenCaptureKit` + `VideoToolbox`) lands in the PoC on a real Mac; the skeleton ships `UnavailableScreenSource`
/// so the helper runs end to end and the phone cleanly falls back to RFB until capture exists.
///
/// A source writes helper → phone messages through `onMessage`, which may be called from a capture thread, so the
/// caller's closure must be safe to call from any thread.
public protocol ScreenSource: AnyObject {
    /// Set once before `start`. The helper frames each message and writes it to the stream socket.
    var onMessage: ((ScreenVideoMessage) -> Void)? { get set }

    /// Begin (or re-begin) capture for these parameters. The source answers with `hello`, then `format` and a
    /// keyframe, or with `paused` when it cannot capture (no permission, locked, no such display).
    func start(_ start: StartMessage)

    /// Apply new parameters (bitrate, fps, display, or `paused`) without tearing the stream down.
    func update(_ update: UpdateMessage)

    /// Encode the next frame as a keyframe (after a decoding error, or a layer that missed the last one).
    func requestKeyframe()

    /// A receiver report, driving bandwidth adaptation.
    func receive(report: ReceiverReport)

    /// Stop capture and release the encoder. The helper calls this when the stream closes.
    func stop()
}

/// A source that cannot capture: it answers `start` with `paused`, so the phone stays on RFB. The skeleton uses it
/// until the real capture source exists; the node also effectively behaves this way by returning `video_unavailable`
/// before the helper is reached.
public final class UnavailableScreenSource: ScreenSource {
    public var onMessage: ((ScreenVideoMessage) -> Void)?
    private let reason: String

    /// - Parameter reason: a `PausedMessage` reason, "permission" by default (Screen Recording not granted).
    public init(reason: String = "permission") {
        self.reason = reason
    }

    public func start(_ start: StartMessage) {
        onMessage?(.paused(PausedMessage(reason: reason)))
    }

    public func update(_ update: UpdateMessage) {}
    public func requestKeyframe() {}
    public func receive(report: ReceiverReport) {}
    public func stop() {}
}

/// Routes a phone → helper message to the source. Helper → phone types are ignored: the helper never receives them.
public enum HelperDispatch {
    public static func handle(_ message: ScreenVideoMessage, source: ScreenSource) {
        switch message {
        case let .start(s): source.start(s)
        case let .update(u): source.update(u)
        case .keyframe: source.requestKeyframe()
        case let .report(r): source.receive(report: r)
        case .hello, .format, .frame, .heartbeat, .paused, .resumed, .timing, .encoding, .unknown:
            break
        }
    }
}
