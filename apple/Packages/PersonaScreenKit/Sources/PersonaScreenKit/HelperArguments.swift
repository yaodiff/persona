import Foundation

/// How Persona Screen was launched. The node starts it through LaunchServices
/// (`open -g -n -b <id>.screen --args --socket <path> --token <t>`), so the helper is its own responsible process
/// for macOS's privacy checks (`design-3954.poc.md`). `--request-permission` is the one-off that asks macOS for
/// Screen Recording and exits.
public struct HelperArguments: Equatable {
    public enum Mode: Equatable {
        /// Connect to the node's Unix socket, send `token` as the first line, then carry the stream.
        case stream(socket: String, token: String)
        /// Call `CGRequestScreenCaptureAccess()` once and exit; macOS shows its alert on the Mac's screen.
        case requestPermission
        /// Print whether Screen Recording is granted (`CGPreflightScreenCaptureAccess()`, which asks nothing) and exit:
        /// what the node reports as `screen_video.permission`.
        case preflight
    }

    public var mode: Mode
    /// The display to capture, or nil for the main display (the `start` message may override it).
    public var display: Int?

    public init(mode: Mode, display: Int? = nil) {
        self.mode = mode
        self.display = display
    }

    public enum ParseError: Error, Equatable {
        /// A flag that takes a value had none after it.
        case missingValue(String)
        /// A value did not parse (e.g. `--display x`).
        case badValue(String)
        /// Neither `--request-permission`, `--preflight` nor a `--socket`/`--token` pair was given.
        case noMode
    }

    /// Parses the arguments after the program name.
    public static func parse(_ argv: [String]) throws -> HelperArguments {
        var socket: String?
        var token: String?
        var requestPermission = false
        var preflight = false
        var display: Int?

        var i = 0
        func value(for flag: String) throws -> String {
            guard i + 1 < argv.count else { throw ParseError.missingValue(flag) }
            i += 1
            return argv[i]
        }

        while i < argv.count {
            let arg = argv[i]
            switch arg {
            case "--socket": socket = try value(for: arg)
            case "--token": token = try value(for: arg)
            case "--request-permission": requestPermission = true
            case "--preflight": preflight = true
            case "--display":
                let raw = try value(for: arg)
                guard let n = Int(raw) else { throw ParseError.badValue(arg) }
                display = n
            default:
                break // unknown flags are ignored, so an older node can pass newer switches
            }
            i += 1
        }

        if requestPermission {
            return HelperArguments(mode: .requestPermission, display: display)
        }
        if preflight {
            return HelperArguments(mode: .preflight, display: display)
        }
        if let socket, let token {
            return HelperArguments(mode: .stream(socket: socket, token: token), display: display)
        }
        throw ParseError.noMode
    }
}
