import Foundation
import XCTest
@testable import PersonaScreenKit

final class HelperArgumentsTests: XCTestCase {
    func testStreamMode() throws {
        let args = try HelperArguments.parse(["--socket", "/tmp/s.sock", "--token", "abc123"])
        XCTAssertEqual(args.mode, .stream(socket: "/tmp/s.sock", token: "abc123"))
        XCTAssertNil(args.display)
    }

    func testRequestPermission() throws {
        let args = try HelperArguments.parse(["--request-permission"])
        XCTAssertEqual(args.mode, .requestPermission)
    }

    func testRequestPermissionWinsOverSocket() throws {
        let args = try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--request-permission"])
        XCTAssertEqual(args.mode, .requestPermission)
    }

    /// `--preflight` reads the permission without asking (the node's `screen_video.permission`); asking wins over it.
    func testPreflight() throws {
        XCTAssertEqual(try HelperArguments.parse(["--preflight"]).mode, .preflight)
        XCTAssertEqual(try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--preflight"]).mode, .preflight)
        XCTAssertEqual(try HelperArguments.parse(["--preflight", "--request-permission"]).mode, .requestPermission)
    }

    func testDisplay() throws {
        let args = try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--display", "3"])
        XCTAssertEqual(args.display, 3)
    }

    func testUnknownFlagsIgnored() throws {
        let args = try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--future", "x"])
        XCTAssertEqual(args.mode, .stream(socket: "/tmp/s", token: "t"))
    }

    func testMissingValueThrows() {
        XCTAssertThrowsError(try HelperArguments.parse(["--socket"])) { error in
            XCTAssertEqual(error as? HelperArguments.ParseError, .missingValue("--socket"))
        }
    }

    func testBadDisplayThrows() {
        XCTAssertThrowsError(try HelperArguments.parse(["--request-permission", "--display", "x"])) { error in
            XCTAssertEqual(error as? HelperArguments.ParseError, .badValue("--display"))
        }
    }

    func testNoModeThrows() {
        XCTAssertThrowsError(try HelperArguments.parse(["--token", "t"])) { error in
            XCTAssertEqual(error as? HelperArguments.ParseError, .noMode)
        }
    }
}

final class ScreenSourceTests: XCTestCase {
    func testUnavailableSourcePausesOnStart() {
        let source = UnavailableScreenSource(reason: "permission")
        var messages: [ScreenVideoMessage] = []
        source.onMessage = { messages.append($0) }
        HelperDispatch.handle(.start(StartMessage(display: nil, maxSize: 900, fps: 30, maxKbps: 2000, codecs: ["hevc"])), source: source)
        XCTAssertEqual(messages, [.paused(PausedMessage(reason: "permission"))])
    }

    func testDispatchRoutesPhoneToHelperOnly() {
        let source = RecordingSource()
        HelperDispatch.handle(.keyframe, source: source)
        HelperDispatch.handle(.report(ReceiverReport(lastFrame: 1, captureMicros: 0, arrivalMicros: 0, bytesSinceLast: 0, flags: [])), source: source)
        // A helper → phone message must not reach the source.
        HelperDispatch.handle(.resumed, source: source)
        XCTAssertEqual(source.calls, ["keyframe", "report"])
    }

    private final class RecordingSource: ScreenSource {
        var onMessage: ((ScreenVideoMessage) -> Void)?
        var calls: [String] = []
        func start(_ start: StartMessage) { calls.append("start") }
        func update(_ update: UpdateMessage) { calls.append("update") }
        func requestKeyframe() { calls.append("keyframe") }
        func receive(report: ReceiverReport) { calls.append("report") }
        func stop() { calls.append("stop") }
    }
}
