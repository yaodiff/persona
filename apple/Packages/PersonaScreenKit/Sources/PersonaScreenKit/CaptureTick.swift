import Foundation

/// The capture source's once-a-second check (#3954): pause when the Mac locks or leaves the console, start again when
/// it comes back, and send a heartbeat while the screen is still (ScreenCaptureKit sends nothing when nothing
/// changes, so the phone would otherwise mistake a quiet screen for a dead stream). Pure, so it tests anywhere; the
/// source calls it on its queue and carries out the action.
public enum CaptureTick {
    public enum Action: Equatable {
        case none
        /// Stop capture and send `paused` with this reason.
        case pause(String)
        /// The Mac is back: send `resumed` and start capture again (a fresh `hello`, `format` and keyframe).
        case resume
        /// Send a `heartbeat`: nothing went out for a second.
        case heartbeat
    }

    /// How long without a frame before a heartbeat.
    public static let heartbeatMicros: UInt64 = 1_000_000

    /// - Parameters:
    ///   - session: why the console session stops capture now (`ConsoleSession.reason`), or nil.
    ///   - paused: why capture is stopped, as sent in `paused`; nil while it runs.
    ///   - capturing: a capture stream is open.
    ///   - sinceLastSent: µs since the last frame or heartbeat went out.
    public static func decide(session: String?, paused: String?, capturing: Bool, sinceLastSent: UInt64) -> Action {
        if let session {
            // Pause once. Already paused (for this or for another reason, such as no permission): nothing to add.
            return paused == nil ? .pause(session) : .none
        }
        if paused == ConsoleSession.locked || paused == ConsoleSession.loginWindow {
            return .resume // only a pause the session caused ends with it; no permission stays paused
        }
        if paused == nil, capturing, sinceLastSent >= heartbeatMicros {
            return .heartbeat
        }
        return .none
    }
}

/// Whether the console session lets capture run, from the dictionary `CGSessionCopyCurrentDictionary()` returns.
public enum ConsoleSession {
    public static let locked = "locked"
    public static let loginWindow = "login_window"

    /// nil when capture can run; else the `paused` reason: `login_window` when this user isn't at the console (another
    /// user, the login window, or no session at all), `locked` for the lock screen or the screen saver.
    public static func reason(from session: [String: Any]?) -> String? {
        guard let session else { return loginWindow }
        // kCGSessionOnConsoleKey's value, spelled out so this compiles without CoreGraphics.
        if (session["kCGSSessionOnConsoleKey"] as? Bool) == false { return loginWindow }
        // The key loginwindow sets while the lock screen or screen saver is up; not in the SDK's headers.
        if (session["CGSSessionScreenIsLocked"] as? Bool) == true { return locked }
        return nil
    }
}
