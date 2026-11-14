import UIKit
import XCTest

/// The app's own VNC (#1343) against scripts/mock/mock_hub.py with MOCK_COMPUTER=ready MOCK_SCREEN=fake (its Screen
/// Sharing: ARD, me / mock-pass, ZRLE; it records the pointer, clicks, keys and the clipboard), MOCK_DEVICE_TOKEN
/// (TEST_RUNNER_PERSONA_SCREEN_HUB, TEST_RUNNER_PERSONA_FAKE_SCREEN=1): the small window (picture in picture) from VNC,
/// over the home screen, back to the whole page and down again; and on the page, the two modes, the clipboard both
/// ways, the keyboard and Help. TEST_RUNNER_PERSONA_VNC_PAUSE slows it down for a screen recording.
final class VNCTests: XCTestCase {
    /// The mock this test talks to: TEST_RUNNER_PERSONA_SCREEN_HUB's, or a second one for a test that needs its own.
    private var hubOverride: String?
    private var hub: String? { hubOverride ?? ProcessInfo.processInfo.environment["PERSONA_SCREEN_HUB"] }
    private var pause: UInt32 { UInt32(ProcessInfo.processInfo.environment["PERSONA_VNC_PAUSE"] ?? "") ?? 0 }

    @MainActor private func launch(_ args: [String]) throws -> XCUIApplication {
        guard let hub, ProcessInfo.processInfo.environment["PERSONA_FAKE_SCREEN"] == "1" else {
            throw XCTSkip("needs the mock's own Screen Sharing: MOCK_SCREEN=fake (PERSONA_FAKE_SCREEN=1)")
        }
        continueAfterFailure = false
        try mock(hub, "/mock/screen-password?p=mock-pass&clear=1")
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair] + args
        app.launchPinned()
        return app
    }

    /// The machine page's VNC, signed in once on the whole page (kept), then straight into the small window.
    @MainActor private func signedInMachinePage(_ more: [String] = []) throws -> (XCUIApplication, XCUIElement) {
        let app = try launch(["-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook", "-openScreenFresh", "YES"] + more)
        let vnc = app.buttons["computer-screen"].firstMatch
        XCTAssertTrue(vnc.waitForExistence(timeout: 30), "no VNC:\n\(app.debugDescription)")
        for _ in 0..<6 where !vnc.isHittable { app.swipeUp() }
        vnc.tap() // nothing kept: the whole page, to sign in
        let password = app.secureTextFields["vnc-password"].firstMatch
        XCTAssertTrue(password.waitForExistence(timeout: 30), "the sign-in:\n\(app.debugDescription)")
        shot(app, "vnc-signin")
        password.tap()
        // Return connects, as from a keyboard: at the largest text sizes Connect sits under the keyboard, and on an iPad
        // with a hardware keyboard typing hides the on-screen one and the form slides down under a tap aimed at it.
        password.typeText("mock-pass\n")
        XCTAssertTrue(live(app), "signed in")
        rest()
        return (app, vnc)
    }

    @MainActor func testTheSmallWindow() throws {
        let (app, vnc) = try signedInMachinePage()
        app.buttons["screen-close"].tap()
        XCTAssertTrue(vnc.waitForExistence(timeout: 10))
        rest()

        vnc.tap() // kept: the small window straight away
        let on = app.descendants(matching: .any)["vnc-pip-on"].firstMatch
        let page = app.descendants(matching: .any)["screen-drawer"].firstMatch
        let deadline = Date.now.addingTimeInterval(20)
        while Date.now < deadline, !on.exists, !page.exists { usleep(250_000) }
        if !on.exists, page.exists {
            // The simulators have no picture in picture (isPictureInPictureSupported is false): the whole page instead.
            XCTAssertTrue(live(app), "the page instead, live")
            throw XCTSkip("no picture in picture on this device: VNC opened the whole page instead")
        }
        XCTAssertTrue(on.exists, "the small window:\n\(app.debugDescription)")
        rest(3)
        shot(app, "vnc-pip-in-app")

        XCUIDevice.shared.press(.home) // to the home screen: it stays up
        rest(3)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let pip = springboard.otherElements.matching(NSPredicate(format: "identifier CONTAINS[c] 'pictureinpicture' OR label CONTAINS[c] '画中画' OR label CONTAINS[c] 'Picture in Picture'")).firstMatch
        let screen = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: screen)
        a.name = "vnc-pip-home"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("vnc-pip-home.png"))
        }
        if !pip.exists { print("SPRINGBOARD:\n\(springboard.debugDescription)") }
        XCTAssertTrue(pip.waitForExistence(timeout: 5), "the small window over the home screen:\n\(springboard.debugDescription)")

        // Its Restore: the whole page in the app.
        pip.tap()
        rest()
        let restore = springboard.buttons.matching(NSPredicate(format: "label CONTAINS[c] 'restore' OR label CONTAINS[c] '还原' OR identifier CONTAINS[c] 'restore'")).firstMatch
        if restore.waitForExistence(timeout: 3) { restore.tap() } else { pip.doubleTap() }
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 15), "back in the app")
        XCTAssertTrue(live(app), "the whole page, live:\n\(app.debugDescription)")
        rest(2)
        shot(app, "vnc-page")

        // Picture in Picture again from the page's button right of Close (#3075).
        app.buttons["vnc-pip"].tap()
        XCTAssertTrue(on.waitForExistence(timeout: 20), "the small window again")
        rest(3)
    }

    /// The small window comes up in the tap (#1839), Connecting… until the Mac's picture, then the picture; when the Mac
    /// hangs up, why. The simulators have no picture in picture: DEBUG -showPiPSource lays its source over the app.
    @MainActor func testTheSmallWindowConnectsInIt() throws {
        let (app, vnc) = try signedInMachinePage(["-showPiPSource", "YES"])
        let hub = try XCTUnwrap(self.hub)
        defer { try? mock(hub, "/mock/screen-password?slow=0&down=0") }
        app.buttons["screen-close"].tap()
        XCTAssertTrue(vnc.waitForExistence(timeout: 10))
        sleep(3) // the page's connection goes 2 s after it lets go (VNC.release): VNC makes a new one

        try mock(hub, "/mock/screen-password?slow=3") // the Mac answers late
        vnc.tap()
        let source = app.descendants(matching: .any)["vnc-pip-source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 1.5), "the small window at once:\n\(app.debugDescription)")
        XCTAssertEqual(source.label, "小窗：连接中")
        XCTAssertFalse(app.descendants(matching: .any)["screen-drawer"].exists, "not the page")
        shot(app, "vnc-pip-card-connecting")
        let live = NSPredicate(format: "label == '小窗：实时画面'")
        XCTAssertEqual(XCTWaiter().wait(for: [expectation(for: live, evaluatedWith: source)], timeout: 15), .completed, source.label)
        rest(2)
        shot(app, "vnc-pip-card-live")

        try mock(hub, "/mock/screen-password?down=1") // the Mac hangs up
        let failed = NSPredicate(format: "label == '小窗：没连上：连接断了'")
        XCTAssertEqual(XCTWaiter().wait(for: [expectation(for: failed, evaluatedWith: source)], timeout: 15), .completed, source.label)
        rest(2)
        shot(app, "vnc-pip-card-failed")
    }

    /// On the page: the two modes (the mock's Screen Sharing says where it was clicked), the clipboard both ways, the
    /// keyboard, Help.
    @MainActor func testThePage() throws {
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))

        // Mouse Mode: a tap clicks there.
        screen.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        rest()
        var seen = try mockState()
        let clicks = seen["clicks"] as? [[String: Any]] ?? []
        XCTAssertTrue(clicks.contains { $0["down"] as? Bool == true }, "a click: \(clicks)")
        // A drag: down, moved, up.
        screen.coordinate(withNormalizedOffset: CGVector(dx: 0.3, dy: 0.4))
            .press(forDuration: 0.1, thenDragTo: screen.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: 0.6)))
        rest()
        shot(app, "vnc-mouse")

        // Trackpad Mode, from …
        app.buttons["vnc-more"].tap()
        app.buttons["触控板模式"].firstMatch.tap()
        XCTAssertTrue(screen.value as? String ?? "" == "" || (screen.value as? String ?? "").contains("mode=trackpad"), "\(String(describing: screen.value))")
        screen.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: screen.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.55)))
        rest()
        app.buttons["vnc-more"].tap()
        app.buttons["指针居中"].firstMatch.tap()
        rest()
        seen = try mockState()
        let pointer = seen["pointer"] as? [Int] ?? []
        XCTAssertEqual(pointer.prefix(2), [240, 150], "指针居中: the middle of the Mac's 480×300")
        shot(app, "vnc-trackpad")
        app.buttons["vnc-more"].tap()
        app.buttons["鼠标模式"].firstMatch.tap()

        // Copy to iPhone: the Mac copies, the phone gets it; then Copy to Mac's Clipboard Only gives it back (the phone's own
        // clipboard, so no Allow Paste to answer), which shows the phone had it.
        try mock(hub!, "/mock/screen-password?clear=1&clip=" + "copied on the Mac".addingPercentEncoding(withAllowedCharacters: .alphanumerics)!)
        rest()
        app.buttons["vnc-clipboard"].tap()
        app.buttons["复制到 iPhone"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["已复制 MacBook 的剪贴板"].waitForExistence(timeout: 5), "the toast:\n\(app.debugDescription)")
        shot(app, "vnc-clipboard")
        app.buttons["vnc-clipboard"].tap()
        app.buttons["只放进 Mac 的剪贴板"].firstMatch.tap()
        _ = answerPaste(app, allow: true)
        rest()
        seen = try mockState()
        XCTAssertEqual(seen["clipboard"] as? String, "copied on the Mac", "back on the Mac:\n\(app.debugDescription)")

        // The keyboard: typing goes to the Mac; ⌘ from the row.
        try mock(hub!, "/mock/screen-password?clear=1")
        app.buttons["vnc-keyboard"].tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard:\n\(app.debugDescription)")
        app.typeText("hello") // to what has the keyboard (the page's catcher: a tap on it could land on I'm Done)
        rest()
        XCTAssertTrue(app.buttons["vnc-key-cmd"].exists && app.buttons["vnc-key-esc"].exists, "the row of the Mac's keys")
        shot(app, "vnc-keyboard")
        seen = try mockState()
        XCTAssertEqual(seen["typed"] as? String, "hello")
        app.buttons["vnc-keyboard"].tap()

        // Picture in Picture Diagnostics (#1839): what this device can and what the small window did, step by step.
        app.buttons["vnc-more"].tap()
        app.buttons["小窗诊断"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["vnc-pip-diagnostics"].firstMatch.waitForExistence(timeout: 5), "小窗诊断")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "点了 VNC")).firstMatch.exists,
                      "the steps:\n\(app.debugDescription)")
        rest()
        shot(app, "vnc-pip-diagnostics")
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(app.buttons["vnc-help"].waitForExistence(timeout: 5))

        // Help.
        app.buttons["vnc-help"].tap()
        let helpPage = app.descendants(matching: .any)["vnc-help-page"].firstMatch
        XCTAssertTrue(helpPage.waitForExistence(timeout: 5))
        for _ in 0..<3 where !app.staticTexts["触控板模式"].exists { helpPage.swipeUp() } // below the fold on an iPhone (#6196's Landscape row)
        XCTAssertTrue(app.staticTexts["触控板模式"].exists, "\(app.debugDescription)")
        rest(2)
        shot(app, "vnc-help")
        app.descendants(matching: .any)["vnc-help-page"].firstMatch.swipeUp()
        XCTAssertTrue(app.staticTexts["我完成了"].waitForExistence(timeout: 5), "its last part:\n\(app.debugDescription)")
        rest()
        app.buttons["vnc-help-close"].tap()
        rest()
    }

    /// The top bar's round buttons share one shape: Help and More (⋯) as tall and as wide as Close.
    @MainActor func testTopBarButtonsKeepTheirShape() throws {
        let (app, _) = try signedInMachinePage()
        let close = app.buttons["screen-close"].firstMatch, help = app.buttons["vnc-help"].firstMatch, more = app.buttons["vnc-more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 10), "\(app.debugDescription)")
        XCTAssertTrue(close.exists && help.exists, "\(app.debugDescription)")
        rest()
        shot(app, "vnc-top-bar")
        let c = close.frame
        for (name, b) in [("Help", help.frame), ("More", more.frame)] {
            XCTAssertEqual(b.height, c.height, accuracy: 1, "\(name) as tall as Close: \(b) vs \(c)")
            XCTAssertEqual(b.width, c.width, accuracy: 1, "\(name) as wide as Close: \(b) vs \(c)")
        }
    }

    /// One display of a Mac with two (#80), against the mock started with MOCK_SCREEN_DISPLAYS=2 (its Screen Sharing lists
    /// both, ExtendedDesktopSize) or MOCK_SCREEN_DISPLAYS=2:node (MacBook's status lists them, as a node reads them), and
    /// TEST_RUNNER_PERSONA_SCREEN_DISPLAYS=2: All Displays shows both side by side; Display 2 shows only the green one,
    /// a tap in its middle clicks the middle of the second display's pixels and Center Pointer centers on it; Display 1
    /// brings the pointer onto it, where Trackpad Mode clicks; the choice is kept for the Mac across a relaunch.
    @MainActor func testOneDisplay() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SCREEN_DISPLAYS"] == "2" else {
            throw XCTSkip("needs the mock with two displays: MOCK_SCREEN_DISPLAYS=2 (TEST_RUNNER_PERSONA_SCREEN_DISPLAYS=2)")
        }
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))
        app.buttons["vnc-more"].tap()
        app.buttons["鼠标模式"].firstMatch.tap() // a tap clicks where it lands
        choose(app, "全部显示器")
        rest(2)
        // All Displays: 960 × 300, display 1 (blue) on the left, display 2 (green) on the right; sampled on their
        // backgrounds, clear of the windows and the pointer in the middle.
        let both = pictureRect(screen.frame, CGSize(width: 960, height: 300))
        XCTAssertTrue(isBlue(color(app, both, 24, 150, of: CGSize(width: 960, height: 300))), "display 1 on the left")
        XCTAssertTrue(isGreen(color(app, both, 936, 150, of: CGSize(width: 960, height: 300))), "display 2 on the right")
        shot(app, "vnc-displays-all")

        app.buttons["vnc-more"].tap()
        app.buttons["显示器"].firstMatch.tap()
        XCTAssertTrue(app.buttons["显示器 2"].firstMatch.waitForExistence(timeout: 5), "the choice:\n\(app.debugDescription)")
        shot(app, "vnc-displays-menu")
        app.buttons["显示器 2"].firstMatch.tap()
        rest(2)
        try displayTwo(app, screen)
        shot(app, "vnc-displays-one")

        // Display 1 in Trackpad Mode: choosing it brings the pointer over from display 2, and a tap clicks where it is.
        choose(app, "显示器 1")
        rest()
        XCTAssertEqual((try mockState()["pointer"] as? [Int] ?? []).prefix(2), [240, 150], "the pointer onto display 1")
        app.buttons["vnc-more"].tap()
        app.buttons["触控板模式"].firstMatch.tap()
        try mock(hub!, "/mock/screen-password?clear=1")
        screen.tap()
        rest()
        let clicks = try mockState()["clicks"] as? [[String: Any]] ?? []
        XCTAssertEqual(clicks.first { $0["down"] as? Bool == true }?["x"] as? Int, 240, "Trackpad Mode clicks on display 1: \(clicks)")
        app.buttons["vnc-more"].tap()
        app.buttons["鼠标模式"].firstMatch.tap()
        choose(app, "显示器 2")
        rest()

        // Kept for this Mac: back on Display 2 after a relaunch (the sign-in is kept too).
        app.terminate()
        let again = try launch(["-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook"])
        let vnc = again.buttons["computer-screen"].firstMatch
        XCTAssertTrue(vnc.waitForExistence(timeout: 30))
        for _ in 0..<6 where !vnc.isHittable { again.swipeUp() }
        vnc.tap()
        XCTAssertTrue(live(again), "the page again, live")
        rest(2)
        try displayTwo(again, again.descendants(matching: .any)["vnc-screen"].firstMatch)
        choose(again, "全部显示器")
    }

    /// Display 2 alone: green across the picture (no blue from display 1), a tap in the middle clicks (720, 150), and
    /// Center Pointer puts the pointer there.
    @MainActor private func displayTwo(_ app: XCUIApplication, _ screen: XCUIElement) throws {
        let one = CGSize(width: 480, height: 300), r = pictureRect(screen.frame, one)
        // Below its window, from edge to edge.
        for x in [6.0, 240, 474] { XCTAssertTrue(isGreen(color(app, r, x, 280, of: one)), "display 2 at x \(x)") }
        try mock(hub!, "/mock/screen-password?clear=1")
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: r.midX, dy: r.midY)).tap()
        rest()
        let clicks = try mockState()["clicks"] as? [[String: Any]] ?? []
        let down = clicks.first { $0["down"] as? Bool == true }
        XCTAssertEqual(Double(down?["x"] as? Int ?? -1), 720, accuracy: 3, "the middle of display 2: \(clicks)")
        XCTAssertEqual(Double(down?["y"] as? Int ?? -1), 150, accuracy: 3, "\(clicks)")
        app.buttons["vnc-more"].tap()
        app.buttons["指针居中"].firstMatch.tap()
        rest()
        XCTAssertEqual((try mockState()["pointer"] as? [Int] ?? []).prefix(2), [720, 150], "Center Pointer: the middle of display 2")
    }

    /// … › Displays › `label`.
    @MainActor private func choose(_ app: XCUIApplication, _ label: String) {
        app.buttons["vnc-more"].tap()
        app.buttons["显示器"].firstMatch.tap()
        let item = app.buttons[label].firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5), "\(label):\n\(app.debugDescription)")
        item.tap()
    }

    /// Where a picture of this size is drawn in the screen's frame (aspect-fit, not zoomed).
    private func pictureRect(_ frame: CGRect, _ size: CGSize) -> CGRect {
        let k = min(frame.width / size.width, frame.height / size.height)
        let w = size.width * k, h = size.height * k
        return CGRect(x: frame.midX - w / 2, y: frame.midY - h / 2, width: w, height: h)
    }

    /// The screenshot's color at pixel (x, y) of a picture of this size drawn in `rect`.
    @MainActor private func color(_ app: XCUIApplication, _ rect: CGRect, _ x: CGFloat, _ y: CGFloat, of size: CGSize) -> (r: Int, g: Int, b: Int) {
        let image = app.screenshot().image
        guard let cg = image.cgImage else { return (0, 0, 0) }
        let scale = CGFloat(cg.width) / image.size.width
        let p = CGPoint(x: (rect.minX + x / size.width * rect.width) * scale, y: (rect.minY + y / size.height * rect.height) * scale)
        var px = [UInt8](repeating: 0, count: 4)
        let ctx = CGContext(data: &px, width: 1, height: 1, bitsPerComponent: 8, bytesPerRow: 4, space: CGColorSpaceCreateDeviceRGB(),
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        ctx?.draw(cg, in: CGRect(x: -p.x.rounded(.down), y: p.y.rounded(.down) - CGFloat(cg.height) + 1, width: CGFloat(cg.width), height: CGFloat(cg.height)))
        return (Int(px[0]), Int(px[1]), Int(px[2]))
    }

    /// The mock's display 1 is a blue gradient, display 2 a green one.
    private func isBlue(_ c: (r: Int, g: Int, b: Int)) -> Bool { c.b > c.g + 30 && c.b > c.r + 30 }
    private func isGreen(_ c: (r: Int, g: Int, b: Int)) -> Bool { c.g > c.b + 30 && c.g > c.r + 30 }

    /// The frame rate and latency the page measures (MOCK_SCREEN_ANIMATE=1: a box moving at ~30 frames a second, its
    /// frames stamped when sent): printed, for the record.
    @MainActor func testMeasure() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_VNC_MEASURE"] == "1" else { throw XCTSkip("PERSONA_VNC_MEASURE=1 with MOCK_SCREEN_ANIMATE=1") }
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        var samples: [String] = []
        for _ in 0..<10 {
            sleep(1)
            samples.append(screen.value as? String ?? "")
        }
        print("VNC-MEASURE " + samples.joined(separator: " | "))
    }

    /// How far two-finger drags scroll and how evenly their notches go out (#7775), with PERSONA_VNC_SCROLL_MEASURE=1:
    /// 300 pt up the picture slowly (200 pt a second), at a medium speed (600) and fast (1500), each reaching its speed
    /// in 50 ms and lifted moving. Once any coast has stopped, the mock says how many notches its Screen Sharing got (a
    /// `scroll` click each) and when each reached its relay (`wheel_in`: when the app sent it); printed as a
    /// SCROLL-MEASURE line a drag: how many, over how long, and how many went out together (within 2 ms of each other).
    @MainActor func testScrollMeasure() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_VNC_SCROLL_MEASURE"] == "1" else { throw XCTSkip("PERSONA_VNC_SCROLL_MEASURE=1") }
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))
        let step = 1.0 / 60
        for (name, speed) in [("slow", 200.0), ("medium", 600.0), ("fast", 1500.0)] {
            try mock(hub!, "/mock/screen-password?clear=1")
            let r = screen.frame
            var ys: [CGFloat] = []
            for i in 0... {
                let t = Double(i) * step
                let travel = t < 0.05 ? speed * t * t / 0.1 : speed * (t - 0.025)
                ys.append(r.midY + 150 - min(travel, 300))
                if travel >= 300 { break }
            }
            try TouchScript.twoFingers(ys.map { CGPoint(x: r.midX - 40, y: $0) }, ys.map { CGPoint(x: r.midX + 40, y: $0) }, step: step)
            sleep(4) // any coast has stopped
            let state = try mockState()
            let notches = (state["clicks"] as? [[String: Any]] ?? []).filter { $0["scroll"] != nil }.count
            let times = (state["wheel_in"] as? [Double] ?? []).sorted()
            var together: [Int] = []
            for (i, t) in times.enumerated() {
                if i > 0, t - times[i - 1] < 0.002 { together[together.count - 1] += 1 } else { together.append(1) }
            }
            print(String(format: "SCROLL-MEASURE %@ speed=%.0f notches=%d sent=%d over=%.2fs sends=%d most-together=%d", name, speed,
                         notches, times.count, (times.last ?? 0) - (times.first ?? 0), together.count, together.max() ?? 0))
            // The simulator plays these touches faster than scripted, and lifts after a pause: how far each goes says
            // little about the speeds named. What holds whatever the speed: it scrolled, and every notch sent arrived.
            XCTAssertGreaterThan(notches, 0, "\(name): the scroll reached the Mac")
            XCTAssertEqual(notches, times.count, "\(name): every notch sent arrived")
        }
    }

    // MARK: #3075

    /// Picture in Picture is a button of its own right of Close, as round as it, and no longer inside ⋯. The simulators
    /// have no picture in picture: DEBUG -showPiPSource YES offers it, and a tap asks for it (Picture in Picture
    /// Diagnostics says so).
    @MainActor func testPictureInPictureIsRightOfClose() throws {
        let (app, _) = try signedInMachinePage(["-showPiPSource", "YES"])
        let close = app.buttons["screen-close"].firstMatch, pip = app.buttons["vnc-pip"].firstMatch
        XCTAssertTrue(pip.waitForExistence(timeout: 10), "no Picture in Picture button:\n\(app.debugDescription)")
        let c = close.frame, p = pip.frame
        XCTAssertGreaterThanOrEqual(p.minX, c.maxX, "right of Close: \(p) vs \(c)")
        XCTAssertLessThanOrEqual(p.minX - c.maxX, 12, "next to Close: \(p) vs \(c)")
        XCTAssertEqual(p.midY, c.midY, accuracy: 1, "on Close's line")
        XCTAssertEqual(p.width, c.width, accuracy: 1)
        XCTAssertEqual(p.height, c.height, accuracy: 1)
        XCTAssertEqual(pip.label, "小窗")
        rest()
        shot(app, "vnc-pip-button"); hold("vnc-pip-button")

        app.buttons["vnc-more"].tap()
        XCTAssertTrue(app.buttons["小窗诊断"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label == '小窗' AND identifier != 'vnc-pip'")).firstMatch.exists,
                       "not inside ⋯ too:\n\(app.debugDescription)")
        app.buttons["小窗诊断"].tap()
        let diagnostics = app.descendants(matching: .any)["vnc-pip-diagnostics"].firstMatch
        XCTAssertTrue(diagnostics.waitForExistence(timeout: 5))
        XCTAssertFalse(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "要小窗")).firstMatch.exists, "not asked yet")
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(pip.waitForExistence(timeout: 5))
        pip.tap()
        rest()
        app.buttons["vnc-more"].tap()
        app.buttons["小窗诊断"].tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "要小窗")).firstMatch.waitForExistence(timeout: 5),
                      "the tap asked for the small window:\n\(app.debugDescription)")
        app.buttons["关闭"].firstMatch.tap()
    }

    /// At the largest text size (#3075) every button of both bars stays on the screen, the name gets a line of its own
    /// under the buttons rather than being squeezed to a letter, and Picture in Picture is still a tap away: next to
    /// Close where the row has room for it, else in ⋯.
    @MainActor func testTheTopBarFitsAtTheLargestText() throws {
        let (app, _) = try signedInMachinePage(["-showPiPSource", "YES", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        let more = app.buttons["vnc-more"].firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 10), "no ⋯:\n\(app.debugDescription)")
        let screen = app.windows.firstMatch.frame
        var buttons: [CGRect] = []
        for id in ["screen-close", "vnc-pip", "vnc-help", "vnc-more", "vnc-clipboard", "vnc-rotate", "vnc-keyboard"] {
            let b = app.buttons[id].firstMatch
            if id == "vnc-pip" || id == "vnc-rotate", !b.exists { continue } // vnc-rotate: an app from before #6196
            XCTAssertTrue(screen.contains(b.frame), "\(id) on the screen: \(b.frame) in \(screen)")
            buttons.append(b.frame)
        }
        let phase = app.descendants(matching: .any)["screen-phase"].firstMatch
        XCTAssertTrue(phase.exists && screen.contains(phase.frame), "the name on the screen: \(phase.frame)")
        XCTAssertFalse(buttons.contains { $0.intersects(phase.frame) }, "the name clear of the buttons: \(phase.frame) vs \(buttons)")
        let pipButton = app.buttons["vnc-pip"].exists
        rest()
        shot(app, "vnc-largest-text"); hold("vnc-largest-text")
        more.tap()
        let item = app.buttons.matching(NSPredicate(format: "label == '小窗' AND identifier != 'vnc-pip'")).firstMatch
        XCTAssertEqual(item.waitForExistence(timeout: pipButton ? 2 : 5), !pipButton, "Picture in Picture next to Close or in ⋯, once:\n\(app.debugDescription)")
        shot(app, "vnc-largest-text-more")
    }

    /// On its side at the largest text size with the keyboard up (#3075) the leading rail is short: Close, Help and ⋯
    /// stay above the keyboard, and Picture in Picture goes into ⋯ when it has no room under Close.
    @MainActor func testTheRailFitsAtTheLargestTextWithTheKeyboardUp() throws {
        try railMenusWithTheKeyboardUp(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"],
                                       name: "vnc-largest-text-landscape-keyboard")
    }

    /// On its side with the keyboard up at the default text size, where the rails close up (#5289): ⋯ and the clipboard
    /// open their menus and the app keeps running.
    @MainActor func testTheRailMenusOpenWithTheKeyboardUp() throws {
        try railMenusWithTheKeyboardUp([], name: "vnc-landscape-keyboard-menus")
    }

    /// Turned on its side with the keyboard up: Close, Help, ⋯, the clipboard, the rotate button (#6196) and the keyboard
    /// button in sight above the keyboard, then ⋯ and the clipboard open their menus. A menu hides the keyboard and the rails get room; the app
    /// keeps running (#5289: a rail that put other views in for the roomier way lost the menu's button under its morph
    /// and the app stopped), and Picture in Picture is in one place, under Close or in ⋯.
    @MainActor private func railMenusWithTheKeyboardUp(_ more: [String], name: String) throws {
        let (app, _) = try signedInMachinePage(["-showPiPSource", "YES"] + more)
        defer { XCUIDevice.shared.orientation = .portrait }
        let window = app.windows.firstMatch, menu = app.buttons["vnc-more"].firstMatch, clipboard = app.buttons["vnc-clipboard"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 10), "no ⋯:\n\(app.debugDescription)")
        XCUIDevice.shared.orientation = .landscapeLeft
        let wide = NSPredicate { _, _ in window.frame.width > window.frame.height }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: wide, object: nil)], timeout: 10), .completed,
                       "the page did not turn: \(window.frame)")
        rest(2)
        hold(name + "-before-keyboard")
        let keys = app.keyboards.firstMatch
        func keyboardUp(_ when: String) {
            for _ in 0..<2 where !keys.waitForExistence(timeout: 3) { app.buttons["vnc-keyboard"].firstMatch.tap() }
            XCTAssertTrue(keys.waitForExistence(timeout: 5), "the keyboard \(when):\n\(app.debugDescription)")
            rest()
        }
        keyboardUp("at first")
        let top = keys.frame.minY
        for id in ["screen-close", "vnc-pip", "vnc-help", "vnc-more", "vnc-clipboard", "vnc-rotate", "vnc-keyboard"] {
            let b = app.buttons[id].firstMatch
            if id == "vnc-pip" || id == "vnc-rotate", !b.exists { continue } // vnc-rotate: an app from before #6196
            XCTAssertTrue(b.isHittable && window.frame.contains(b.frame) && b.frame.maxY <= top + 1,
                          "\(id) in sight above the keyboard: \(b.frame), the keyboard from \(top) in \(window.frame)")
        }
        shot(app, name); hold(name)

        menu.tap()
        // Its first item: at the largest text sizes the rest are below the menu's fold.
        let first = app.buttons["鼠标模式"].firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 5), "⋯ opened:\n\(app.debugDescription)")
        rest()
        XCTAssertEqual(app.state, .runningForeground, "the app went on ⋯")
        let item = app.buttons.matching(NSPredicate(format: "label == '小窗' AND identifier != 'vnc-pip'")).firstMatch
        XCTAssertNotEqual(app.buttons["vnc-pip"].exists, item.exists, "Picture in Picture under Close or in ⋯, once:\n\(app.debugDescription)")
        shot(app, name + "-more"); hold(name + "-more")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.15)).tap()
        XCTAssertTrue(first.waitForNonExistence(timeout: 5), "⋯ closed")

        keyboardUp("after ⋯")
        clipboard.tap()
        XCTAssertTrue(app.buttons["从 iPhone 粘贴"].firstMatch.waitForExistence(timeout: 5), "the clipboard opened:\n\(app.debugDescription)")
        rest()
        XCTAssertEqual(app.state, .runningForeground, "the app went on the clipboard")
        shot(app, name + "-clipboard"); hold(name + "-clipboard")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.15)).tap()
        XCTAssertTrue(app.buttons["从 iPhone 粘贴"].firstMatch.waitForNonExistence(timeout: 5), "the clipboard closed")
        XCTAssertEqual(app.state, .runningForeground)
    }

    /// On its side (#3075): the page turns to either landscape, the picture takes the height between two rails of buttons
    /// inside the safe area (Close then Picture in Picture down the leading one), and a tap lands on the Mac where the
    /// finger is. Upright again and closed, the app stays upright when the phone turns.
    @MainActor func testLandscape() throws {
        let app = try launch(["-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook", "-openScreenFresh", "YES", "-showPiPSource", "YES"])
        defer { XCUIDevice.shared.orientation = .portrait }
        let vnc = app.buttons["computer-screen"].firstMatch
        XCTAssertTrue(vnc.waitForExistence(timeout: 30), "no VNC:\n\(app.debugDescription)")
        for _ in 0..<6 where !vnc.isHittable { app.swipeUp() }
        vnc.tap()
        let password = app.secureTextFields["vnc-password"].firstMatch
        XCTAssertTrue(password.waitForExistence(timeout: 30), "the sign-in:\n\(app.debugDescription)")
        let window = app.windows.firstMatch
        let wide = NSPredicate { _, _ in window.frame.width > window.frame.height }
        let tall = NSPredicate { _, _ in window.frame.width < window.frame.height }

        // Signed in on its side: the form scrolls between the rails, over the keyboard too.
        XCUIDevice.shared.orientation = .landscapeLeft
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: wide, object: nil)], timeout: 10), .completed,
                       "the page did not turn: \(window.frame)")
        rest(2)
        shot(app, "vnc-landscape-signin"); hold("vnc-landscape-signin")
        password.tap()
        password.typeText("mock-pass")
        rest()
        shot(app, "vnc-landscape-signin-keyboard"); hold("vnc-landscape-signin-keyboard")
        let connect = app.buttons["vnc-connect"].firstMatch
        if !connect.isHittable { app.scrollViews.firstMatch.swipeUp() }
        connect.tap()
        XCTAssertTrue(live(app), "signed in on its side:\n\(app.debugDescription)")
        if app.keyboards.firstMatch.exists { app.buttons["vnc-keyboard"].tap(); app.buttons["vnc-keyboard"].tap() }

        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        let close = app.buttons["screen-close"].firstMatch, pip = app.buttons["vnc-pip"].firstMatch
        let keyboard = app.buttons["vnc-keyboard"].firstMatch, clipboard = app.buttons["vnc-clipboard"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))
        XCUIDevice.shared.orientation = .portrait
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: tall, object: nil)], timeout: 10), .completed)
        rest(2)
        let upright = screen.frame
        shot(app, "vnc-portrait"); hold("vnc-portrait")
        for (name, orientation) in [("left", UIDeviceOrientation.landscapeLeft), ("right", .landscapeRight)] {
            XCUIDevice.shared.orientation = orientation
            XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: wide, object: nil)], timeout: 10), .completed,
                           "\(name): the page did not turn: \(window.frame)")
            rest(2)
            let w = window.frame, s = screen.frame
            XCTAssertGreaterThan(s.width, s.height, "\(name): the picture lies wide: \(s)")
            XCTAssertGreaterThan(s.height, w.height * 0.85, "\(name): the picture takes the height: \(s) in \(w)")
            let picture = Self.fit(CGSize(width: 480, height: 300), in: s), uprightPicture = Self.fit(CGSize(width: 480, height: 300), in: upright)
            XCTAssertGreaterThan(picture.width, uprightPicture.width * 1.4, "\(name): the picture larger than upright: \(picture) vs \(uprightPicture)")
            // The rails: Close then Picture in Picture down the leading side, the clipboard and the keyboard down the
            // trailing one, all beside the picture and on screen.
            for b in [close, pip] {
                XCTAssertLessThanOrEqual(b.frame.maxX, s.minX + 1, "\(name): \(b.identifier) left of the picture: \(b.frame) vs \(s)")
            }
            for b in [clipboard, keyboard] {
                XCTAssertGreaterThanOrEqual(b.frame.minX, s.maxX - 1, "\(name): \(b.identifier) right of the picture: \(b.frame) vs \(s)")
            }
            for b in [close, pip, clipboard, keyboard] {
                XCTAssertTrue(w.contains(b.frame) && b.isHittable, "\(name): \(b.identifier) on screen: \(b.frame) in \(w)")
            }
            XCTAssertGreaterThan(pip.frame.minY, close.frame.maxY - 1, "\(name): Picture in Picture under Close")
            XCTAssertEqual(pip.frame.midX, close.frame.midX, accuracy: 1)
            shot(app, "vnc-landscape-\(name)"); hold("vnc-landscape-\(name)")

            // A tap a quarter of the way across and down the Mac's 480×300 lands at (120, 75).
            try mock(hub!, "/mock/screen-password?clear=1")
            let fitted = Self.fit(CGSize(width: 480, height: 300), in: s)
            let at = CGPoint(x: fitted.minX + fitted.width / 4, y: fitted.minY + fitted.height / 4)
            app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: at.x, dy: at.y)).tap()
            rest()
            let clicks = try mockState()["clicks"] as? [[String: Any]] ?? []
            let down = try XCTUnwrap(clicks.first { $0["down"] as? Bool == true }, "\(name): no click: \(clicks)")
            XCTAssertEqual(Double(down["x"] as? Int ?? -1), 120, accuracy: 3, "\(name): \(down)")
            XCTAssertEqual(Double(down["y"] as? Int ?? -1), 75, accuracy: 3, "\(name): \(down)")

            // The keyboard up: the rails still reach their buttons (they scroll when they do not fit).
            keyboard.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "\(name): the keyboard")
            rest()
            XCTAssertTrue(keyboard.isHittable && close.isHittable, "\(name): Keyboard and Close reachable over the keyboard")
            shot(app, "vnc-landscape-\(name)-keyboard"); hold("vnc-landscape-\(name)-keyboard")
            keyboard.tap()
            XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5))
        }

        // Closed on its side: the app is upright again at once, and stays so however the phone is held.
        close.tap()
        XCTAssertTrue(vnc.waitForExistence(timeout: 10))
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: tall, object: nil)], timeout: 10), .completed,
                       "upright again: \(window.frame)")
        XCUIDevice.shared.orientation = .portrait
        rest()
        XCUIDevice.shared.orientation = .landscapeLeft
        rest(2)
        XCTAssertLessThan(window.frame.width, window.frame.height, "the app turned with the page closed: \(window.frame)")
        shot(app, "vnc-closed-stays-upright"); hold("vnc-closed-stays-upright")
    }

    /// The Mac's picture fitted in a view, as VNCScreenUIView fits it.
    private static func fit(_ size: CGSize, in r: CGRect) -> CGRect {
        let k = min(r.width / size.width, r.height / size.height)
        let w = size.width * k, h = size.height * k
        return CGRect(x: r.midX - w / 2, y: r.midY - h / 2, width: w, height: h)
    }

    /// Paste from iPhone (#3075): what the phone has, Chinese, emoji, a shell's special characters, tabs, several lines
    /// and some kilobytes of it, goes onto the Mac's clipboard whole through its node, and ⌘V follows only once it is
    /// there (the mock writes down the clipboard's writes and the keys in the order they came). The text reaches the
    /// phone's clipboard by Copy to iPhone (the app's own, so iOS asks nothing); another app's text that iOS will not
    /// hand over (the simulators refuse Allow Paste without showing it) says so, not that there is none.
    @MainActor func testPasteFromIPhone() throws {
        let (app, _) = try signedInMachinePage()
        let line = "$HOME \"double\" 'single' \\ `tick` ! & | ; <> * ? ~\ttab 中文 😀👍🏽 — “quoted” …"
        let texts = ["echo hello", (1...3).map { "line \($0): \(line)" }.joined(separator: "\n"),
                     (1...120).map { String(format: "%04d %@", $0, line) }.joined(separator: "\n")]
        for (i, text) in texts.enumerated() {
            try mockPost("/api/v1/machines/macbook/computer/clipboard", ["text": text]) // the Mac copied it…
            app.buttons["vnc-clipboard"].tap()
            app.buttons["复制到 iPhone"].firstMatch.tap() // …and the phone has it
            XCTAssertTrue(app.staticTexts["已复制 MacBook 的剪贴板"].waitForExistence(timeout: 10), "text \(i): on the phone")
            try mock(hub!, "/mock/screen-password?clear=1")
            rest()
            app.buttons["vnc-clipboard"].tap()
            app.buttons["从 iPhone 粘贴"].firstMatch.tap()
            XCTAssertTrue(app.staticTexts["已粘贴到 MacBook"].waitForExistence(timeout: 10), "text \(i): the toast:\n\(app.debugDescription)")
            let events = try mockState()["events"] as? [[String: Any]] ?? []
            let writes = events.enumerated().filter { $0.element["clipboard"] != nil }
            XCTAssertEqual(writes.count, 1, "text \(i): one write: \(writes.map(\.element))")
            XCTAssertEqual(writes.first?.element["via"] as? String, "node", "text \(i): through the node, which answers once it is there")
            XCTAssertEqual(writes.first?.element["clipboard"] as? String, text, "text \(i): whole (\(text.utf8.count) bytes)")
            let cmd = events.firstIndex { $0["key"] as? Int == 0xffeb && $0["down"] as? Bool == true }
            let v = events.firstIndex { $0["key"] as? Int == 0x76 && $0["down"] as? Bool == true }
            XCTAssertNotNil(v, "text \(i): ⌘V")
            if let w = writes.first?.offset, let v, let cmd { XCTAssertTrue(w < cmd && cmd < v, "text \(i): ⌘V after the write: \(events)") }
            XCTAssertEqual(events.filter { $0["key"] as? Int == 0xffeb }.map { $0["down"] as? Bool }, [true, false], "text \(i): ⌘ down then up: \(events)")
            rest()
        }
        shot(app, "vnc-pasted")

        // Another app's text: iOS asks first (Allow Paste) and the page keeps going meanwhile. Refused, the page says why
        // and nothing goes to the Mac; allowed, it is pasted. (Simulators before iOS 27 refused without asking.)
        let pasted = app.staticTexts["已粘贴到 MacBook"]
        let refused = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "读不到这台 iPhone 的剪贴板")).firstMatch
        XCTAssertTrue(pasted.waitForNonExistence(timeout: 15), "the last paste's toast has gone")
        UIPasteboard.general.string = "copied in another app"
        for allow in [false, true] {
            try mock(hub!, "/mock/screen-password?clear=1")
            app.buttons["vnc-clipboard"].tap()
            app.buttons["从 iPhone 粘贴"].firstMatch.tap()
            let asked = answerPaste(app, allow: allow)
            let toast = allow && asked ? pasted : refused
            XCTAssertTrue(toast.waitForExistence(timeout: 10), "allow \(allow), asked \(asked): pasted, or why not (never “no text”):\n\(app.debugDescription)")
            let clipboard = try mockState()["clipboard"] as? String
            XCTAssertEqual(clipboard, allow && asked ? "copied in another app" : "", "allow \(allow): the Mac's clipboard")
            shot(app, allow ? "vnc-paste-allowed" : "vnc-paste-refused")
            XCTAssertTrue(toast.waitForNonExistence(timeout: 15))
        }
    }

    /// The Mac's pointer reads over black, white, gray and a dark terminal (#3075), against a second mock started with
    /// MOCK_SCREEN_BG=black, its address in TEST_RUNNER_PERSONA_SCREEN_BG: a 1920×1080 Mac sending no pointer, as macOS's
    /// does (the app's own arrow, which was solid black); with MOCK_SCREEN_CURSOR=1 it sends macOS's arrow, which drawn at
    /// the picture's scale was 4 points tall. A screenshot over each for the eye, all four taken before any check.
    @MainActor func testThePointerReads() throws {
        guard let bgHub = ProcessInfo.processInfo.environment["PERSONA_SCREEN_BG"], bgHub.hasPrefix("http") else {
            throw XCTSkip("needs a mock with plain backgrounds: MOCK_SCREEN_BG=black, its address in TEST_RUNNER_PERSONA_SCREEN_BG")
        }
        hubOverride = bgHub
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))
        var drawn: [(String, String)] = []
        for bg in ["black", "white", "gray", "terminal"] {
            try mock(bgHub, "/mock/screen-password?bg=\(bg)")
            rest(2)
            shot(app, "vnc-pointer-\(bg)"); hold("vnc-pointer-\(bg)")
            drawn.append((bg, screen.value as? String ?? ""))
        }
        for (bg, value) in drawn {
            let size = value.split(separator: ";").first { $0.hasPrefix("pointer=") }?.dropFirst("pointer=".count).split(separator: "x").compactMap { Double($0) } ?? []
            XCTAssertEqual(size.count, 2, value)
            XCTAssertGreaterThanOrEqual(size.last ?? 0, 22, "\(bg): the pointer is drawn readable: \(value)")
        }
    }

    // MARK: #6196

    /// The rotate button: right of the clipboard on an iPhone, as large; a tap turns the page on its side, where it sits
    /// under the clipboard down the trailing rail and says it turns back, and the page stays on its side however the phone
    /// is held; there ⋯ and the clipboard open with the keyboard up (#5289). A second tap turns it upright, and it stays
    /// so. Closed and opened again, the page turns with the phone as before.
    @MainActor func testTheRotateButton() throws {
        let (app, vnc) = try signedInMachinePage()
        defer { XCUIDevice.shared.orientation = .portrait }
        let window = app.windows.firstMatch
        let wide = NSPredicate { _, _ in window.frame.width > window.frame.height }
        let tall = NSPredicate { _, _ in window.frame.width < window.frame.height }
        func turned(_ p: NSPredicate) -> XCTWaiter.Result { XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: nil)], timeout: 10) }
        let rotate = app.buttons["vnc-rotate"].firstMatch, clipboard = app.buttons["vnc-clipboard"].firstMatch
        let keyboard = app.buttons["vnc-keyboard"].firstMatch, menu = app.buttons["vnc-more"].firstMatch
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(rotate.waitForExistence(timeout: 10), "no rotate button:\n\(app.debugDescription)")
        var c = clipboard.frame, r = rotate.frame
        XCTAssertGreaterThanOrEqual(r.minX, c.maxX, "right of the clipboard: \(r) vs \(c)")
        XCTAssertLessThanOrEqual(r.minX - c.maxX, 16, "next to the clipboard: \(r) vs \(c)")
        XCTAssertEqual(r.midY, c.midY, accuracy: 1, "on the clipboard's line")
        XCTAssertEqual(r.width, c.width, accuracy: 1)
        XCTAssertEqual(r.height, c.height, accuracy: 1)
        XCTAssertEqual(rotate.label, "转为横屏")
        rest()
        shot(app, "vnc-rotate-portrait"); hold("vnc-rotate-portrait")

        rotate.tap()
        XCTAssertEqual(turned(wide), .completed, "the button did not turn the page: \(window.frame)")
        rest(2)
        c = clipboard.frame; r = rotate.frame
        let s = screen.frame
        XCTAssertGreaterThanOrEqual(r.minX, s.maxX - 1, "down the trailing rail: \(r) vs \(s)")
        XCTAssertGreaterThanOrEqual(r.minY, c.maxY - 1, "under the clipboard: \(r) vs \(c)")
        XCTAssertLessThanOrEqual(r.minY - c.maxY, 16, "next to the clipboard: \(r) vs \(c)")
        XCTAssertEqual(r.midX, c.midX, accuracy: 1)
        XCTAssertTrue(window.frame.contains(r) && rotate.isHittable, "on screen: \(r)")
        XCTAssertEqual(rotate.label, "转为竖屏")
        XCTAssertGreaterThan(s.height, window.frame.height * 0.85, "the picture takes the height: \(s)")
        shot(app, "vnc-rotate-landscape"); hold("vnc-rotate-landscape")

        // Turned by the button it stays on its side, the phone held upright or not.
        XCUIDevice.shared.orientation = .landscapeLeft
        rest(2)
        XCUIDevice.shared.orientation = .portrait
        rest(2)
        XCTAssertGreaterThan(window.frame.width, window.frame.height, "kept on its side with the phone upright: \(window.frame)")

        // The keyboard up on its side (#5289): ⋯ and the clipboard open their menus and the app keeps running.
        func keyboardUp(_ when: String) {
            for _ in 0..<2 where !app.keyboards.firstMatch.waitForExistence(timeout: 3) { keyboard.tap() }
            XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard \(when):\n\(app.debugDescription)")
            rest()
        }
        keyboardUp("at first")
        XCTAssertTrue(rotate.isHittable, "the rotate button reachable over the keyboard: \(rotate.frame)")
        shot(app, "vnc-rotate-landscape-keyboard"); hold("vnc-rotate-landscape-keyboard")
        menu.tap()
        XCTAssertTrue(app.buttons["指针居中"].firstMatch.waitForExistence(timeout: 5), "⋯ opened:\n\(app.debugDescription)")
        XCTAssertEqual(app.state, .runningForeground, "the app went on ⋯")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.15)).tap()
        XCTAssertTrue(app.buttons["指针居中"].firstMatch.waitForNonExistence(timeout: 5), "⋯ closed")
        keyboardUp("after ⋯")
        clipboard.tap()
        XCTAssertTrue(app.buttons["从 iPhone 粘贴"].firstMatch.waitForExistence(timeout: 5), "the clipboard opened:\n\(app.debugDescription)")
        XCTAssertEqual(app.state, .runningForeground, "the app went on the clipboard")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.15)).tap()
        XCTAssertTrue(app.buttons["从 iPhone 粘贴"].firstMatch.waitForNonExistence(timeout: 5), "the clipboard closed")
        if app.keyboards.firstMatch.exists { keyboard.tap() }
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5))

        // The second tap: upright, and kept so with the phone on its side.
        rotate.tap()
        XCTAssertEqual(turned(tall), .completed, "the button did not turn the page back: \(window.frame)")
        rest(2)
        XCTAssertEqual(rotate.label, "转为横屏")
        XCUIDevice.shared.orientation = .landscapeLeft
        rest(2)
        XCTAssertLessThan(window.frame.width, window.frame.height, "kept upright with the phone on its side: \(window.frame)")

        // Closed (the app upright), then opened again: it turns with the phone as before the tap.
        app.buttons["screen-close"].tap()
        XCTAssertTrue(vnc.waitForExistence(timeout: 10))
        XCUIDevice.shared.orientation = .portrait
        rest(2)
        XCTAssertLessThan(window.frame.width, window.frame.height, "upright with the page closed: \(window.frame)")
        vnc.tap() // kept: the small window where there is one, else (the simulators) the whole page
        guard rotate.waitForExistence(timeout: 20) else { throw XCTSkip("the small window instead of the page: nothing to turn") }
        XCTAssertTrue(live(app), "the page again, live")
        XCUIDevice.shared.orientation = .landscapeLeft
        XCTAssertEqual(turned(wide), .completed, "opened again, it turns with the phone: \(window.frame)")
        XCUIDevice.shared.orientation = .portrait
        XCTAssertEqual(turned(tall), .completed)
    }

    /// Two fingers: in Trackpad Mode they only scroll, so a scroll whose fingers drift 30% apart, and a pinch, leave the
    /// picture as it was while the scroll reaches the Mac. In Mouse Mode the same scroll scrolls and does not zoom, and a
    /// pinch zooms; ⋯ › Zoom to Fit goes back. ⋯ › Zoom In zooms Trackpad Mode in, and the picture follows the pointer
    /// to the Mac's edge. TEST_RUNNER_PERSONA_VNC_PAUSE slows it down for a screen recording (with MOCK_SCREEN_SCROLL=1
    /// the Mac's window shows the scroll).
    @MainActor func testTwoFingers() throws {
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(screen.waitForExistence(timeout: 10))
        func more(_ label: String) { // an item of ⋯
            app.buttons["vnc-more"].tap()
            app.buttons[label].firstMatch.tap()
            rest()
        }
        func scrolls() throws -> Int { (try mockState()["clicks"] as? [[String: Any]] ?? []).filter { $0["scroll"] != nil }.count }
        // A scroll up the middle of the view whose fingers open from 80 to 104 points.
        func driftingScroll() throws {
            let r = screen.frame, steps = 30, by: CGFloat = 150
            var a: [CGPoint] = [], b: [CGPoint] = []
            for i in 0...steps {
                let k = CGFloat(i) / CGFloat(steps), half = (80 + 24 * k) / 2, y = r.midY + by / 2 - by * k
                a.append(CGPoint(x: r.midX - half, y: y))
                b.append(CGPoint(x: r.midX + half, y: y))
            }
            try TouchScript.twoFingers(a, b, step: 0.03) // about a second: slow enough to see in a recording
            rest(2)
        }

        // TEST_RUNNER_PERSONA_HOLD: the host records Trackpad Mode, then Mouse Mode (hold-vnc-rec-start / -end, one
        // recording each).
        more("触控板模式")
        hold("vnc-rec-start")
        try mock(hub!, "/mock/screen-password?clear=1")
        try driftingScroll()
        XCTAssertGreaterThan(try scrolls(), 0, "Trackpad Mode: the scroll reached the Mac")
        XCTAssertEqual(readout(screen).zoom, 1, accuracy: 0.01, "Trackpad Mode: the scroll did not zoom")
        hold("vnc-trackpad-scrolled")
        screen.pinch(withScale: 4, velocity: 3) // XCUITest's fingers start ~12 pt apart: the first 10 pt only decide
        rest(2)
        XCTAssertEqual(readout(screen).zoom, 1, accuracy: 0.01, "Trackpad Mode: a pinch does not zoom")
        shot(app, "vnc-trackpad-two-fingers"); hold("vnc-trackpad-pinched")
        hold("vnc-rec-end")

        more("鼠标模式")
        hold("vnc-rec-start")
        try mock(hub!, "/mock/screen-password?clear=1")
        try driftingScroll()
        XCTAssertGreaterThan(try scrolls(), 0, "Mouse Mode: the scroll reached the Mac")
        XCTAssertEqual(readout(screen).zoom, 1, accuracy: 0.01, "Mouse Mode: the scroll did not zoom")
        hold("vnc-mouse-scrolled")
        screen.pinch(withScale: 4, velocity: 3) // XCUITest's fingers start ~12 pt apart: the first 10 pt only decide
        rest(2)
        XCTAssertGreaterThan(readout(screen).zoom, 1.5, "Mouse Mode: a pinch zooms")
        shot(app, "vnc-mouse-pinched"); hold("vnc-mouse-pinched")
        // Zoomed in, two fingers move the picture.
        let before = readout(screen).at
        try TouchScript.twoFingers((0...20).map { CGPoint(x: screen.frame.midX - 40 - CGFloat($0) * 4, y: screen.frame.midY) },
                                   (0...20).map { CGPoint(x: screen.frame.midX + 40 - CGFloat($0) * 4, y: screen.frame.midY) }, step: 0.03)
        rest(2)
        XCTAssertLessThan(readout(screen).at.x, before.x - 20, "zoomed in, two fingers moved the picture: \(before) → \(readout(screen).at)")
        hold("vnc-mouse-moved")
        more("还原")
        XCTAssertEqual(readout(screen).zoom, 1, accuracy: 0.01, "Zoom to Fit")
        hold("vnc-rec-end")

        // Trackpad Mode zoomed in from ⋯: the picture follows the pointer to the Mac's right edge.
        more("触控板模式")
        more("指针居中")
        app.buttons["vnc-more"].tap()
        XCTAssertTrue(app.buttons["放大"].firstMatch.waitForExistence(timeout: 5), "Zoom In offered while fitted")
        XCTAssertFalse(app.buttons["还原"].firstMatch.exists, "and not Zoom to Fit")
        rest()
        shot(app, "vnc-more-fitted"); hold("vnc-more-fitted")
        app.buttons["放大"].firstMatch.tap()
        rest()
        XCTAssertEqual(readout(screen).zoom, 2, accuracy: 0.01, "Zoom In")
        app.buttons["vnc-more"].tap()
        XCTAssertTrue(app.buttons["还原"].firstMatch.waitForExistence(timeout: 5), "Zoom to Fit offered once zoomed in")
        XCTAssertFalse(app.buttons["放大"].firstMatch.exists, "and not Zoom In")
        rest()
        shot(app, "vnc-more-zoomed"); hold("vnc-more-zoomed")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.8)).tap() // outside the menu: it closes
        XCTAssertTrue(app.buttons["还原"].firstMatch.waitForNonExistence(timeout: 5), "⋯ closed")
        rest()
        for _ in 0..<4 {
            screen.coordinate(withNormalizedOffset: CGVector(dx: 0.15, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: screen.coordinate(withNormalizedOffset: CGVector(dx: 0.85, dy: 0.5)))
            rest()
        }
        let pointer = try mockState()["pointer"] as? [Int] ?? []
        XCTAssertGreaterThan(pointer.first ?? 0, 470, "the pointer at the Mac's right edge: \(pointer)")
        let at = readout(screen).at
        XCTAssertTrue(at.x >= 0 && at.x <= screen.frame.width + 1, "the picture followed it: drawn at \(at) in \(screen.frame)")
        shot(app, "vnc-trackpad-zoomed"); hold("vnc-trackpad-zoomed")
        more("还原")
        more("鼠标模式")
    }

    // MARK: #6333

    /// The small window's source (DEBUG -showPiPSource shows it; the simulators have no picture in picture): it lies
    /// exactly over the page's picture, where the system's transition starts and Restore ends, upright and turned by
    /// the rotate button. With the page closed, the source goes; the Mac's VNC opened straight into the small window
    /// puts it in the top trailing corner, not the bottom-left one a copy of the picture used to pop up from.
    @MainActor func testTheSourceLiesOverThePicture() throws {
        let (app, vnc) = try signedInMachinePage(["-showPiPSource", "YES"])
        defer { XCUIDevice.shared.orientation = .portrait }
        let window = app.windows.firstMatch
        let source = app.descendants(matching: .any)["vnc-pip-source"].firstMatch
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 10), "the source:\n\(app.debugDescription)")
        func over(_ when: String) {
            let picture = Self.fit(CGSize(width: 480, height: 300), in: screen.frame), s = source.frame
            XCTAssertEqual(s.minX, picture.minX, accuracy: 1.5, "\(when): the source \(s) over the picture \(picture)")
            XCTAssertEqual(s.minY, picture.minY, accuracy: 1.5, "\(when): the source \(s) over the picture \(picture)")
            XCTAssertEqual(s.width, picture.width, accuracy: 1.5, "\(when): the source \(s) over the picture \(picture)")
            XCTAssertEqual(s.height, picture.height, accuracy: 1.5, "\(when): the source \(s) over the picture \(picture)")
        }
        func turned(_ wide: Bool) -> XCTWaiter.Result {
            let p = NSPredicate { _, _ in (window.frame.width > window.frame.height) == wide }
            return XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: nil)], timeout: 10)
        }
        rest()
        over("upright")
        app.buttons["vnc-rotate"].tap()
        XCTAssertEqual(turned(true), .completed, "on its side: \(window.frame)")
        rest(2)
        over("on its side")
        app.buttons["vnc-rotate"].tap()
        XCTAssertEqual(turned(false), .completed, "upright again: \(window.frame)")
        rest(2)
        over("upright again")

        app.buttons["screen-close"].tap()
        XCTAssertTrue(source.waitForNonExistence(timeout: 10), "the page closed without the small window: the source goes")
        XCTAssertTrue(vnc.waitForExistence(timeout: 10))
        sleep(3) // the page's connection goes 2 s after it lets go (VNC.release): VNC makes a new one
        vnc.tap() // kept: straight into the small window
        XCTAssertTrue(source.waitForExistence(timeout: 10), "the small window's source:\n\(app.debugDescription)")
        let w = window.frame, s = source.frame
        XCTAssertLessThan(s.maxY, w.midY, "in the top half: \(s) in \(w)")
        XCTAssertGreaterThan(s.midX, w.midX, "on the trailing side: \(s) in \(w)")
    }

    /// Into the small window from the page and back, twice, for a screen recording of the transition (#6333; the host
    /// records between hold-vnc-rec-start and -end). Needs the system's picture in picture, which the iPhone simulators
    /// lack: a stock iPad simulator, with TEST_RUNNER_PERSONA_PIP_ROUNDS=1.
    @MainActor func testPictureInPictureRoundTrips() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_PIP_ROUNDS"] == "1" else {
            throw XCTSkip("needs picture in picture (a stock iPad simulator) and TEST_RUNNER_PERSONA_PIP_ROUNDS=1")
        }
        let (app, _) = try signedInMachinePage()
        let on = app.descendants(matching: .any)["vnc-pip-on"].firstMatch, button = app.buttons["vnc-pip"].firstMatch
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        // SpringBoard's small window is a window of its own (identifier PIP-SBInteractionPassThroughView on iOS 27).
        let pip = springboard.windows.matching(NSPredicate(format: "identifier BEGINSWITH 'PIP' OR label == 'Picture in Picture' OR label == '画中画'")).firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 10), "no Picture in Picture on this device:\n\(app.debugDescription)")
        sleep(2)
        hold("vnc-rec-start")
        for round in 1...2 {
            button.tap()
            XCTAssertTrue(on.waitForExistence(timeout: 20), "round \(round): the small window:\n\(app.debugDescription)")
            XCTAssertTrue(pip.waitForExistence(timeout: 10), "round \(round): the system's small window:\n\(springboard.debugDescription)")
            sleep(3)
            pip.tap() // its controls
            restoreButton(springboard, in: pip.frame, round: round).tap()
            XCTAssertTrue(on.waitForNonExistence(timeout: 15), "round \(round): back on the page")
            XCTAssertTrue(live(app), "round \(round): the page, live")
            sleep(3)
        }
        hold("vnc-rec-end")
    }

    /// The small window's button back to the app, among the controls a tap on it shows (not Close, which ends it). Every
    /// button there goes in the log, so a system that names it differently says what to look for.
    @MainActor private func restoreButton(_ springboard: XCUIApplication, in frame: CGRect, round: Int) -> XCUIElement {
        let back = NSPredicate(format: "label CONTAINS[c] 'restore' OR identifier CONTAINS[c] 'restore' OR label CONTAINS[c] '还原' OR label CONTAINS[c] 'full screen' OR label CONTAINS[c] '全屏'")
        let match = springboard.buttons.matching(back).firstMatch
        if match.waitForExistence(timeout: 3) { return match }
        let near = springboard.buttons.allElementsBoundByIndex.filter { $0.exists && frame.insetBy(dx: -8, dy: -8).intersects($0.frame) }
        let listed = near.map { "\($0.identifier) | \($0.label) | \($0.frame)" }.joined(separator: "\n")
        print("PIP-CONTROLS round \(round):\n\(listed)")
        let other = near.first { b in !["close", "关闭", "stop", "play", "pause", "skip", "播放", "暂停"].contains { b.label.lowercased().contains($0) || b.identifier.lowercased().contains($0) } }
        XCTAssertNotNil(other, "round \(round): no button back to the app on the small window:\n\(listed)\n\(springboard.debugDescription)")
        return other!
    }

    /// The DEBUG readout of the page's picture (VNCScreenUIView.place): its zoom (1: fitted) and where the pointer is
    /// drawn, in the view's points.
    @MainActor private func readout(_ screen: XCUIElement) -> (zoom: Double, at: CGPoint) {
        let parts = (screen.value as? String ?? "").split(separator: ";")
        func field(_ k: String) -> String? { parts.first { $0.hasPrefix(k + "=") }.map { String($0.dropFirst(k.count + 1)) } }
        let at = field("at")?.split(separator: ",").compactMap { Double($0) } ?? []
        return (Double(field("zoom") ?? "") ?? -1, at.count == 2 ? CGPoint(x: at[0], y: at[1]) : CGPoint(x: -1, y: -1))
    }

    // MARK: Helpers

    /// #3954: the video channel (the mock with MOCK_SCREEN_VIDEO=1, TEST_RUNNER_PERSONA_SCREEN_VIDEO=1). RFB signs in,
    /// then video takes the picture (the phone sent `start` with its cap, and reports frames); the Mac locking puts VNC
    /// back and says so in plain words, unlocking brings video back and the line goes; no permission says where to turn
    /// it on (one sentence); the stream dropping puts VNC back again.
    @MainActor func testVideoTakesThePictureAndFallsBack() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SCREEN_VIDEO"] == "1" else {
            throw XCTSkip("needs the mock's video channel: MOCK_SCREEN_VIDEO=1 (PERSONA_SCREEN_VIDEO=1)")
        }
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(picture(screen, "video"), "video took the picture: \(screen.value ?? "")")
        let state = try mockJSON("/mock/screen-video")
        let start = (state["starts"] as? [[String: Any]])?.first
        XCTAssertEqual(start?["max_kbps"] as? Int, 12_000, "Wi-Fi's cap: \(state)")
        XCTAssertEqual(start?["codecs"] as? [String], ["hevc", "h264"])
        XCTAssertGreaterThan(state["reports"] as? Int ?? 0, 0, "the phone reports frames")
        func says(_ words: String) -> XCUIElement { app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch }
        XCTAssertFalse(app.descendants(matching: .any)["screen-fallback"].exists, "nothing to explain")
        shot(app, "vnc-video")

        try mock(hub!, "/mock/screen-video?pause=locked")
        XCTAssertTrue(picture(screen, "vnc"), "locked: VNC again")
        XCTAssertTrue(says("Mac 锁屏了").waitForExistence(timeout: 5), "why:\n\(app.debugDescription)")
        shot(app, "vnc-video-locked")
        try mock(hub!, "/mock/screen-video?resume=1")
        XCTAssertTrue(picture(screen, "video"), "unlocked: video again")
        XCTAssertTrue(says("Mac 锁屏了").waitForNonExistence(timeout: 5))

        // The Mac took Persona Screen's permission away: the one reason the user fixes, so the line says where.
        try mock(hub!, "/mock/screen-video?pause=permission")
        XCTAssertTrue(picture(screen, "vnc"), "not allowed: VNC again")
        let how = app.buttons["screen-fallback-help"].firstMatch // the whole line: why, and how to turn it on
        XCTAssertTrue(how.waitForExistence(timeout: 5), "how to turn it on:\n\(app.debugDescription)")
        XCTAssertTrue(how.label.contains("还没允许视频"), "why: \(how.label)")
        shot(app, "vnc-video-not-allowed")
        how.tap()
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 5))
        XCTAssertTrue(alert.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "录屏与系统录音")).firstMatch.exists,
                      "where: \(alert.debugDescription)")
        shot(app, "vnc-video-how")
        alert.buttons.firstMatch.tap()
        try mock(hub!, "/mock/screen-video?resume=1")
        XCTAssertTrue(picture(screen, "video"), "allowed again: video")
        XCTAssertTrue(how.waitForNonExistence(timeout: 5), "the line goes with video back")

        try mock(hub!, "/mock/screen-video?drop=1")
        XCTAssertTrue(picture(screen, "vnc"), "dropped: VNC again")
        XCTAssertTrue(says("视频断了").waitForExistence(timeout: 5), "why:\n\(app.debugDescription)")
        shot(app, "vnc-video-lost")
    }

    /// #6398: the stats overlay's Route row (the demo's numbers: a direct LAN route, its round trip and loss, and why the
    /// live screen moved, on its own line), for review in light and dark, at the default size and at xxxLarge.
    @MainActor func testTheOverlayShowsTheRoute() throws {
        for (scheme, size) in [("light", "L"), ("dark", "L"), ("light", "XXXL"), ("dark", "XXXL")] {
            let (app, _) = try signedInMachinePage(["-showScreenStatsDemo", "YES", "-ui.scheme", scheme,
                                                    "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategory\(size)"])
            let overlay = app.descendants(matching: .any)["screen-stats"].firstMatch
            XCTAssertTrue(overlay.waitForExistence(timeout: 10), "the overlay:\n\(app.debugDescription)")
            XCTAssertTrue(overlay.label.contains("6 ms"), "the route's round trip: \(overlay.label)")
            for source in [app, XCUIApplication(bundleIdentifier: "com.apple.springboard")] { // the icon follows the look
                let alert = source.alerts.containing(NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")).firstMatch
                if alert.exists, alert.buttons.firstMatch.exists { alert.buttons.firstMatch.tap() }
            }
            shot(app, "vnc-overlay-route-\(scheme)-\(size == "L" ? "default" : "xxxl")")
            app.terminate()
        }
    }

    /// #6398: a phone that asks the hub for a screen session (direct routes on, `-showScreenRoutes`) and a hub that
    /// doesn't offer one (the mock, like an older hub or node) keep today's live screen through the same raw streams:
    /// RFB signs in and, with the mock's video channel, video takes the picture and reports frames.
    @MainActor func testAskingForAScreenSessionKeepsTheRawStreams() throws {
        let (app, _) = try signedInMachinePage(["-showScreenRoutes", "YES"])
        guard ProcessInfo.processInfo.environment["PERSONA_SCREEN_VIDEO"] == "1" else { return }
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(picture(screen, "video"), "video took the picture: \(screen.value ?? "")")
        XCTAssertGreaterThan(try mockJSON("/mock/screen-video")["reports"] as? Int ?? 0, 0, "the phone reports frames")
    }

    /// #3954: the not-allowed line at the largest text size: the words and "How to turn it on" wrap, nothing cut off.
    @MainActor func testVideoNotAllowedAtTheLargestText() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SCREEN_VIDEO"] == "1" else {
            throw XCTSkip("needs the mock's video channel: MOCK_SCREEN_VIDEO=1 (PERSONA_SCREEN_VIDEO=1)")
        }
        let (app, _) = try signedInMachinePage(["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(picture(screen, "video"), "video took the picture: \(screen.value ?? "")")
        try mock(hub!, "/mock/screen-video?pause=permission")
        let how = app.buttons["screen-fallback-help"].firstMatch
        XCTAssertTrue(how.waitForExistence(timeout: 10), "how to turn it on:\n\(app.debugDescription)")
        XCTAssertTrue(app.windows.firstMatch.frame.contains(how.frame), "on the screen: \(how.frame)")
        shot(app, "vnc-video-not-allowed-ax")
        try mock(hub!, "/mock/screen-video?resume=1")
    }

    /// #3954: Connection Stats over the video breaks the latency down: the helper's capture, encode and network (the
    /// mock's `timing`, its network half measured from the phone's reports) and the phone's decode, with the total first.
    @MainActor func testTheOverlayBreaksTheLatencyDown() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SCREEN_VIDEO"] == "1" else {
            throw XCTSkip("needs the mock's video channel: MOCK_SCREEN_VIDEO=1 (PERSONA_SCREEN_VIDEO=1)")
        }
        let (app, _) = try signedInMachinePage()
        let screen = app.descendants(matching: .any)["vnc-screen"].firstMatch
        XCTAssertTrue(picture(screen, "video"), "video took the picture: \(screen.value ?? "")")
        app.buttons["vnc-more"].tap()
        let toggle = app.descendants(matching: .any)["screen-stats-toggle"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 5), "Connection Stats in ⋯:\n\(app.debugDescription)")
        toggle.tap()
        let stats = app.descendants(matching: .any)["screen-stats"].firstMatch
        XCTAssertTrue(stats.waitForExistence(timeout: 5), "the overlay:\n\(app.debugDescription)")
        let words = ["视频", "经 hub 中转", "fps", "HEVC", "采集", "编码", "网络", "解码"]
        let deadline = Date.now.addingTimeInterval(8)
        while Date.now < deadline, !words.allSatisfy(stats.label.contains) { usleep(250_000) }
        XCTAssertTrue(words.allSatisfy(stats.label.contains), "every segment: \(stats.label)")
        shot(app, "vnc-video-stats")
        app.buttons["screen-stats-close"].firstMatch.tap() // kept per Mac: off again for the other tests
        XCTAssertTrue(stats.waitForNonExistence(timeout: 5))
    }

    /// #3954: a Mac whose helper macOS hasn't asked about yet (a second mock with MOCK_SCREEN_VIDEO=missing, its address
    /// in TEST_RUNNER_PERSONA_SCREEN_VIDEO_MISSING): VNC with the not-allowed line, and "How to turn it on" says the Mac
    /// is about to show a window, not where in System Settings (Persona Screen isn't in that list until it asked).
    @MainActor func testNotAskedYetSaysAWindowWillShow() throws {
        guard let missingHub = ProcessInfo.processInfo.environment["PERSONA_SCREEN_VIDEO_MISSING"], missingHub.hasPrefix("http") else {
            throw XCTSkip("needs a mock with MOCK_SCREEN_VIDEO=missing, its address in TEST_RUNNER_PERSONA_SCREEN_VIDEO_MISSING")
        }
        hubOverride = missingHub
        let (app, _) = try signedInMachinePage()
        let how = app.buttons["screen-fallback-help"].firstMatch
        XCTAssertTrue(how.waitForExistence(timeout: 10), "the not-allowed line:\n\(app.debugDescription)")
        XCTAssertTrue((app.descendants(matching: .any)["vnc-screen"].firstMatch.value as? String ?? "").contains("picture=vnc"))
        how.tap()
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 5))
        XCTAssertTrue(alert.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "会弹一个窗口")).firstMatch.exists,
                      "a window will show: \(alert.debugDescription)")
        shot(app, "vnc-video-not-asked")
        alert.buttons.firstMatch.tap()
    }

    /// #6558: Connection Stats over VNC shows the round trip to the hub on a line of its own, timed on the app's own
    /// hub connection (the mock answers its pings), as the web's overlay does.
    @MainActor func testTheOverlayShowsTheRoundTripToTheHub() throws {
        let (app, _) = try signedInMachinePage()
        app.buttons["vnc-more"].tap()
        let toggle = app.descendants(matching: .any)["screen-stats-toggle"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 5), "Connection Stats in ⋯:\n\(app.debugDescription)")
        toggle.tap()
        let stats = app.descendants(matching: .any)["screen-stats"].firstMatch
        XCTAssertTrue(stats.waitForExistence(timeout: 5), "the overlay:\n\(app.debugDescription)")
        let words = ["VNC", "经 hub 中转", "到 hub 往返"]
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline, !words.allSatisfy(stats.label.contains) { usleep(250_000) }
        XCTAssertTrue(words.allSatisfy(stats.label.contains), "the round trip to the hub: \(stats.label)")
        shot(app, "vnc-stats-hub")
        app.buttons["screen-stats-close"].firstMatch.tap() // kept per Mac: off again for the other tests
        XCTAssertTrue(stats.waitForNonExistence(timeout: 5))
    }

    /// The picture the screen shows, as its DEBUG value says (`picture=video|vnc`), within 20 s.
    @MainActor private func picture(_ screen: XCUIElement, _ want: String) -> Bool {
        let deadline = Date.now.addingTimeInterval(20)
        while Date.now < deadline {
            if (screen.value as? String ?? "").contains("picture=\(want)") { return true }
            usleep(250_000)
        }
        return false
    }

    @MainActor private func live(_ app: XCUIApplication) -> Bool {
        let phase = app.descendants(matching: .any)["screen-phase"].firstMatch
        let deadline = Date.now.addingTimeInterval(30)
        // Down a rail (on its side) the phase is the dot's value.
        while Date.now < deadline { if phase.exists, (phase.label + (phase.value as? String ?? "")).contains("已连接") { return true }; usleep(250_000) }
        return false
    }

    /// iOS asks before the app reads what another app (here the test runner) copied. Answers Allow Paste or Don't Allow
    /// Paste if it asks, and says whether it did. The question is the system's (SpringBoard), looked for in the app too.
    @MainActor private func answerPaste(_ app: XCUIApplication, allow: Bool) -> Bool {
        for owner in [XCUIApplication(bundleIdentifier: "com.apple.springboard"), app] {
            let yes = owner.buttons.matching(NSPredicate(format: "label IN %@", ["允许粘贴", "Allow Paste"])).firstMatch
            guard yes.waitForExistence(timeout: 3) else { continue }
            if allow { yes.tap(); return true }
            let no = owner.buttons.matching(NSPredicate(format: "label CONTAINS %@ OR label BEGINSWITH %@", "不允许", "Don")).firstMatch
            XCTAssertTrue(no.exists, "Don't Allow Paste next to Allow Paste:\n\(owner.debugDescription)")
            if no.exists { no.tap() }
            return true
        }
        return false
    }

    private func rest(_ seconds: UInt32 = 1) { sleep(pause > 0 ? seconds * pause : (seconds > 1 ? 1 : 0)); usleep(300_000) }

    private func mock(_ hub: String, _ path: String) throws {
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: URL(string: hub + path)!) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
    }

    /// POST JSON to the mock's API as the app would (its device token).
    private func mockPost(_ path: String, _ body: [String: String]) throws {
        var req = URLRequest(url: URL(string: hub! + path)!)
        req.httpMethod = "POST"
        req.setValue("Bearer \(UITestToken.device)", forHTTPHeaderField: "Authorization")
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: req) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
    }

    private func mockState() throws -> [String: Any] { try mockJSON("/mock/screen-password") }

    /// One of the mock's own JSON answers.
    private func mockJSON(_ path: String) throws -> [String: Any] {
        var out: [String: Any] = [:]
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: URL(string: hub! + path)!) { data, _, _ in
            out = (data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }) ?? [:]
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return out
    }

    /// TEST_RUNNER_PERSONA_HOLD=<seconds>: stays this long so the host can take `xcrun simctl io <udid> screenshot`
    /// (XCUITest's own comes out scrambled in landscape), in light and dark (`simctl ui <udid> appearance`); a file
    /// `hold-<name>` in PERSONA_SHOTS says what to name it, and goes once taken.
    private func hold(_ name: String) {
        guard let seconds = ProcessInfo.processInfo.environment["PERSONA_HOLD"].flatMap(UInt32.init),
              let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let look = ProcessInfo.processInfo.environment["PERSONA_LOOK"].map { "-" + $0 } ?? ""
        FileManager.default.createFile(atPath: dir + "/hold-\(name)-\(UITestStyle.name)\(look)", contents: nil)
        sleep(seconds)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let look = ProcessInfo.processInfo.environment["PERSONA_LOOK"].map { "-" + $0 } ?? ""
        let full = "\(name)-\(UITestStyle.name)\(look)"
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = full
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(full + ".png"))
        }
    }
}
