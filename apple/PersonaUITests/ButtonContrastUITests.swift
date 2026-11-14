import UIKit
import XCTest

/// Secondary `.themedButton()`s on every surface (#6157). `-showButtonContrast YES` (ButtonContrastLab.swift) draws one in
/// the accent, one tinted `Theme.danger` and one with the destructive role on each kind of fill a secondary button sits on
/// in the app: two screens of places, then a sheet at half and at full height. This test does it in every style, with
/// three theme colors, in light and dark.
/// - Each label is measured against the button's own fill in the screenshot, as #6157's `contrast.swift` does: the fill
///   is the most common color, the label the 2 % of pixels farthest from it. The glass fill is the system's, not a token
///   `StyleSpecTests` could check.
/// - Every label must reach 3:1, `StyleSpecTests`' floor for the accent read as a link.
/// - The destructive-role button's label must be nearer the danger button's than the accent one's in the same place: red
///   in every style (#7042).
///
/// Against scripts/mock/mock_hub.py (`PERSONA_MOCK_HUB`), so no pairing screen covers the page.
/// - `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots and `contrast.tsv`.
/// - `TEST_RUNNER_PERSONA_PHASE=before` only measures.
/// - `TEST_RUNNER_PERSONA_CONTRAST_STYLES=glass,lark` and `TEST_RUNNER_PERSONA_CONTRAST_COLORS=default,coral` narrow the runs.
final class ButtonContrastUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private static let floor = 3.0
    private static let tints = ["accent", "danger", "destructive"]
    private static let firstPlaces = ["page", "grouped", "card", "themeCard", "surface"]
    private static let secondPlaces = ["list", "well", "wash", "bar", "black"]
    private static let sheetPlaces = ["sheet", "sheetSurface", "sheetCard", "sheetThemeCard", "sheetList"]

    @MainActor func testSecondaryButtonsReadOnEverySurface() throws {
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let styles = list("PERSONA_CONTRAST_STYLES") ?? ["glass", "beautiful", "lark", "clay", "island"]
        let colors = list("PERSONA_CONTRAST_COLORS") ?? ["default", "coral", "teal"]
        let measureOnly = env["PERSONA_PHASE"] == "before"
        var rows = ["look\tstyle\tcolor\tplace\ttint\tfill\tlabel\tratio"], low: [String] = [], unlike: [String] = []
        var first = true, lastLook = ""
        // The look outermost: the app icon follows it, and the system's alert about that comes only when it changes.
        for look in ["light", "dark"] {
            for style in styles {
                for color in colors {
                    let app = XCUIApplication()
                    app.launchArguments = ["-pairURL", url, "-showButtonContrast", "YES", "-ui.style", style, "-ui.scheme", look]
                        + (color == "default" ? [] : ["-ui.color", color])
                    app.launchPinned()
                    let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                    // The notification prompt comes once per install: waited for only at the first launch.
                    for label in ["允许", "Allow"] where first ? board.buttons[label].waitForExistence(timeout: 3) : board.buttons[label].exists {
                        board.buttons[label].tap()
                    }
                    first = false
                    if look != lastLook ? board.alerts.firstMatch.waitForExistence(timeout: 3) : board.alerts.firstMatch.exists {
                        for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                            board.alerts.buttons[label].tap(); sleep(1); break
                        }
                    }
                    lastLook = look
                    let name = "\(look)-\(style)-\(color)"
                    let opener = app.buttons["contrast.openSheet"]
                    XCTAssertTrue(app.buttons["contrast.page.accent"].waitForExistence(timeout: 20), "\(name): no lab:\n\(app.debugDescription)")
                    sleep(2) // the page settles
                    measure(app, places: Self.firstPlaces, shot: "\(name)-page", into: &rows, low: &low, unlike: &unlike)
                    app.buttons["contrast.next"].tap()
                    XCTAssertTrue(app.buttons["contrast.list.accent"].waitForExistence(timeout: 10), "\(name): no second screen")
                    sleep(1)
                    measure(app, places: Self.secondPlaces, shot: "\(name)-more", into: &rows, low: &low, unlike: &unlike)
                    // The sheet over the first screen, as over a page.
                    app.buttons["contrast.next"].tap()
                    XCTAssertTrue(app.buttons["contrast.page.accent"].waitForExistence(timeout: 10), "\(name): no first screen")

                    opener.tap()
                    XCTAssertTrue(app.buttons["contrast.sheet.accent"].waitForExistence(timeout: 10), "\(name): no sheet")
                    sleep(2) // the sheet is up
                    // At half height the lower places may be below the screen's edge: measured at full height only.
                    measure(app, places: Self.sheetPlaces, shot: "\(name)-half", suffix: "@half", partly: true, into: &rows, low: &low, unlike: &unlike)
                    app.buttons["contrast.expand"].tap()
                    sleep(2)
                    measure(app, places: Self.sheetPlaces, shot: "\(name)-full", suffix: "@full", into: &rows, low: &low, unlike: &unlike)
                    app.terminate()
                }
            }
        }
        let table = rows.joined(separator: "\n") + "\n"
        add(XCTAttachment(string: table))
        if let dir = env["PERSONA_SHOTS"] {
            try table.write(toFile: "\(dir)/contrast.tsv", atomically: true, encoding: .utf8)
        }
        if !measureOnly {
            XCTAssertTrue(low.isEmpty, "secondary buttons under \(Self.floor):1 against their own fill:\n\(low.joined(separator: "\n"))")
            XCTAssertTrue(unlike.isEmpty, "destructive-role buttons not drawn in Theme.danger:\n\(unlike.joined(separator: "\n"))")
        }
    }

    /// A comma-separated list from the environment; unset or empty (a script passing an unset variable) is nil, so every
    /// style or color runs rather than none.
    /// #7214: a Menu, a Link and a ShareLink styled with `.themedButton()` draw the style's background as a Button does
    /// (after #7129 a Menu drew none in the glass styles and Lark): each one's fill is nearer the Button's than the page
    /// beside them, in every style, light and dark. PERSONA_PHASE=before only measures and keeps the screenshots.
    @MainActor func testEveryKindOfControlDrawsTheButtonsBackground() throws {
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let styles = list("PERSONA_CONTRAST_STYLES") ?? ["glass", "beautiful", "lark", "clay", "island"]
        var rows = ["look\tstyle\tkind\tfill\tpage"], bare: [String] = []
        var first = true, lastLook = ""
        for look in ["light", "dark"] {
            for style in styles {
                let app = XCUIApplication()
                app.launchArguments = ["-pairURL", url, "-showButtonContrast", "YES", "-ui.style", style, "-ui.scheme", look]
                app.launchPinned()
                let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                for label in ["允许", "Allow"] where first ? board.buttons[label].waitForExistence(timeout: 3) : board.buttons[label].exists {
                    board.buttons[label].tap()
                }
                first = false
                if look != lastLook ? board.alerts.firstMatch.waitForExistence(timeout: 3) : board.alerts.firstMatch.exists {
                    for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                        board.alerts.buttons[label].tap(); sleep(1); break
                    }
                }
                lastLook = look
                let kinds = ["button", "menu", "link", "share"]
                let control = { (kind: String) in app.descendants(matching: .any)["contrast.kinds.\(kind)"].firstMatch }
                XCTAssertTrue(control("button").waitForExistence(timeout: 20), "\(look) \(style): no lab:\n\(app.debugDescription)")
                sleep(2) // the page settles
                let screenshot = app.screenshot()
                let window = app.windows.firstMatch.frame
                guard let pixels = Pixels(screenshot.pngRepresentation, pointWidth: window.width) else {
                    XCTFail("\(look) \(style): the screenshot did not decode"); continue
                }
                if let dir = env["PERSONA_SHOTS"] {
                    try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/kinds-\(look)-\(style).png"))
                }
                let row = app.descendants(matching: .any)["contrast.kinds"].firstMatch.frame
                let last = control("share").frame
                // The page: the row's free end, right of the last control.
                let page = pixels.contrast(in: CGRect(x: last.maxX + 12, y: last.minY, width: max(8, row.maxX - last.maxX - 24), height: last.height)).fill
                let button = pixels.contrast(in: control("button").frame).fill
                for kind in kinds {
                    let frame = control(kind).frame
                    guard control(kind).exists, window.contains(frame) else { XCTFail("\(look) \(style): \(kind) is not on the screen"); continue }
                    let fill = pixels.contrast(in: frame).fill
                    rows.append([look, style, kind, fill, page].joined(separator: "\t"))
                    // A style whose button background is as the page (none to lose) proves nothing either way.
                    if kind != "button", Pixels.distance(button, page) > 6, Pixels.distance(fill, button) >= Pixels.distance(fill, page) {
                        bare.append("\(look) \(style) \(kind): \(fill), the page \(page), a button \(button)")
                    }
                }
                app.terminate()
            }
        }
        let table = rows.joined(separator: "\n") + "\n"
        let attachment = XCTAttachment(string: table)
        attachment.name = "control-kinds.tsv"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] { try? table.write(toFile: "\(dir)/control-kinds.tsv", atomically: true, encoding: .utf8) }
        if env["PERSONA_PHASE"] != "before" {
            XCTAssertTrue(bare.isEmpty, "controls drawn without the style's background:\n" + bare.joined(separator: "\n"))
        }
    }

    private func list(_ key: String) -> [String]? {
        guard let value = env[key], !value.isEmpty else { return nil }
        return value.split(separator: ",").map(String.init)
    }

    /// One screenshot: every place's buttons that are on screen, each label against its own fill.
    @MainActor private func measure(_ app: XCUIApplication, places: [String], shot: String, suffix: String = "",
                                    partly: Bool = false, into rows: inout [String], low: inout [String],
                                    unlike: inout [String]) {
        let screenshot = app.screenshot()
        let window = app.windows.firstMatch.frame
        guard let pixels = Pixels(screenshot.pngRepresentation, pointWidth: window.width) else {
            XCTFail("\(shot): the screenshot did not decode"); return
        }
        if let dir = env["PERSONA_SHOTS"] {
            try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/contrast-\(shot).png"))
        }
        let parts = shot.split(separator: "-").map(String.init) // look, style, color, page|more|half|full
        for place in places {
            var labels: [String: String] = [:]
            for tint in Self.tints {
                let button = app.buttons["contrast.\(place).\(tint)"]
                guard button.exists, window.contains(button.frame) else {
                    if !partly { XCTFail("\(shot): contrast.\(place).\(tint) is not on the screen") }
                    continue
                }
                let m = pixels.contrast(in: button.frame)
                labels[tint] = m.label
                rows.append([parts[0], parts[1], parts[2], place + suffix, tint, m.fill, m.label, String(format: "%.2f", m.ratio)]
                    .joined(separator: "\t"))
                if m.ratio < Self.floor {
                    low.append("\(parts[0]) \(parts[1]) \(parts[2]) \(place + suffix) \(tint): \(m.label) on \(m.fill), \(String(format: "%.2f", m.ratio)):1")
                }
            }
            if let accent = labels["accent"], let danger = labels["danger"], let destructive = labels["destructive"],
               Pixels.distance(destructive, danger) >= Pixels.distance(destructive, accent) {
                unlike.append("\(parts[0]) \(parts[1]) \(parts[2]) \(place + suffix): destructive \(destructive), danger \(danger), accent \(accent)")
            }
        }
    }
}

/// A screenshot's pixels in sRGB, and #6157's measurement of one button in it.
private struct Pixels {
    let width: Int, height: Int, scale: CGFloat
    private var buffer: [UInt8]

    init?(_ png: Data, pointWidth: CGFloat) {
        guard let image = UIImage(data: png)?.cgImage, pointWidth > 0 else { return nil }
        width = image.width
        height = image.height
        scale = CGFloat(image.width) / pointWidth
        buffer = [UInt8](repeating: 0, count: width * height * 4)
        let drawn: Bool = buffer.withUnsafeMutableBytes { raw in
            guard let context = CGContext(data: raw.baseAddress, width: image.width, height: image.height, bitsPerComponent: 8,
                                          bytesPerRow: image.width * 4, space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                          bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            context.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
            return true
        }
        if !drawn { return nil }
    }

    private func pixel(_ x: Int, _ y: Int) -> (Double, Double, Double) {
        let i = (y * width + x) * 4
        return (Double(buffer[i]), Double(buffer[i + 1]), Double(buffer[i + 2]))
    }

    private static func luminance(_ c: (Double, Double, Double)) -> Double {
        func l(_ v: Double) -> Double { let s = v / 255; return s <= 0.04045 ? s / 12.92 : pow((s + 0.055) / 1.055, 2.4) }
        return 0.2126 * l(c.0) + 0.7152 * l(c.1) + 0.0722 * l(c.2)
    }

    /// The distance between two `#RRGGBB` colors in sRGB.
    static func distance(_ a: String, _ b: String) -> Double {
        func rgb(_ hex: String) -> [Double] {
            let v = Int(hex.dropFirst(), radix: 16) ?? 0
            return [Double(v >> 16 & 0xFF), Double(v >> 8 & 0xFF), Double(v & 0xFF)]
        }
        return zip(rgb(a), rgb(b)).map { ($0 - $1) * ($0 - $1) }.reduce(0, +).squareRoot()
    }

    private static func hex(_ c: (Double, Double, Double)) -> String {
        String(format: "#%02X%02X%02X", Int(c.0.rounded()), Int(c.1.rounded()), Int(c.2.rounded()))
    }

    /// The button's middle, clear of its rounded ends and its rim, where the glass draws a highlight that would read as
    /// the label: the fill is the most common color there, the label the 2 % of pixels farthest from it.
    func contrast(in frame: CGRect) -> (fill: String, label: String, ratio: Double) {
        let inner = frame.insetBy(dx: min(frame.height / 2, frame.width / 4), dy: frame.height * 0.2)
        let x0 = max(0, Int(inner.minX * scale)), x1 = min(width, Int(inner.maxX * scale))
        let y0 = max(0, Int(inner.minY * scale)), y1 = min(height, Int(inner.maxY * scale))
        guard x1 > x0, y1 > y0 else { return ("-", "-", 0) }
        var counts: [Int: Int] = [:]
        for y in y0..<y1 {
            for x in x0..<x1 {
                let p = pixel(x, y)
                counts[(Int(p.0) / 4) << 16 | (Int(p.1) / 4) << 8 | Int(p.2) / 4, default: 0] += 1
            }
        }
        let top = counts.max { $0.value < $1.value }!.key
        let fill = (Double((top >> 16) & 255) * 4 + 2, Double((top >> 8) & 255) * 4 + 2, Double(top & 255) * 4 + 2)
        let lf = Self.luminance(fill)
        var far: [(Double, (Double, Double, Double))] = []
        for y in y0..<y1 {
            for x in x0..<x1 {
                let p = pixel(x, y)
                far.append((abs(Self.luminance(p) - lf) + abs(p.2 - fill.2) / 2550, p))
            }
        }
        far.sort { $0.0 > $1.0 }
        let n = max(1, far.count / 50)
        var label = (0.0, 0.0, 0.0)
        for i in 0..<n { label.0 += far[i].1.0; label.1 += far[i].1.1; label.2 += far[i].1.2 }
        label = (label.0 / Double(n), label.1 / Double(n), label.2 / Double(n))
        let (a, b) = (Self.luminance(label), lf)
        return (Self.hex(fill), Self.hex(label), (max(a, b) + 0.05) / (min(a, b) + 0.05))
    }
}
