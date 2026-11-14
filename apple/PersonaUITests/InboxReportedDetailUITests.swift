import XCTest

/// Rendered-height proof for plain reported details on compact and regular layouts.
/// Run against Fixtures/reported_detail_hub.py (PERSONA_MOCK_HUB). On baseline production,
/// reported-long is capped at the same two-line height as reported-short and fails the
/// five-line assertion. TEST_RUNNER_PERSONA_SHOTS=<dir> keeps the closed and open screens;
/// TEST_RUNNER_PERSONA_UI_SCHEME=dark captures the dark rendering;
/// TEST_RUNNER_PERSONA_TEXT_SIZE=<UIContentSizeCategory name> runs at that text size.
final class InboxReportedDetailUITests: XCTestCase {
    private static let short = "reported-short"
    private static let long = "reported-long"
    private static let controls = ["control-plain", "reported-link", "reported-messages"]
    private static let referenceLines = ["L", "M", "N", "O", "P"]

    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testOnlyPlainReportedDetailGrowsBeyondTwoLines() throws {
        let app = try open()
        let all = app.buttons["inbox.chain.all"].firstMatch
        XCTAssertTrue(all.waitForExistence(timeout: 10), "the five-row fixture has no Show all control")
        reveal(all, in: app, hittable: true)
        all.tap()
        sleep(1) // Let the existing Show all animation finish before reading rendered frames.

        var heights: [String: CGFloat] = [:]
        var longTextHeight: CGFloat?
        for id in [Self.short, Self.long] + Self.controls {
            let row = entry(app, id)
            reveal(row, in: app)
            let height = row.frame.height
            XCTAssertGreaterThan(height, 0, "\(id) has no rendered row")
            heights[id] = height
            print("REPORTED-DETAIL \(layout) \(id) closed height=\(height)")
            if id == Self.long {
                let text = detailText(app, row: id, lines: Self.referenceLines)
                reveal(text, in: app)
                longTextHeight = text.frame.height
                print("REPORTED-DETAIL \(layout) \(id) closed Text height=\(text.frame.height)")
                shot(app, "plain-detail")
            }
        }
        shot(app, "controls-closed")

        let short = try XCTUnwrap(heights[Self.short])
        let long = try XCTUnwrap(heights[Self.long])
        XCTAssertGreaterThan(long, short + 8,
                             "five explicit lines must render taller than the two-line plain reported row; short=\(short), long=\(long)")
        for id in Self.controls {
            let capped = try XCTUnwrap(heights[id])
            XCTAssertEqual(capped, short, accuracy: 1,
                           "\(id) must retain the old two-line cap; short=\(short), actual=\(capped)")
            XCTAssertLessThan(capped, long - 8, "\(id) grew with the plain reported detail")
        }

        // A reported row's independent source link remains a real navigation control.
        let source = app.buttons["inbox.chain.source"].firstMatch
        reveal(source, in: app, hittable: true)
        XCTAssertTrue(source.isHittable, "the reported source link is no longer independently usable")

        // Measure the messages row while closed above; only now exercise its old expansion.
        let expand = app.buttons["inbox.chain.expand"].firstMatch
        reveal(expand, in: app, hittable: true)
        expand.tap()
        let marker = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "Expansion marker")).firstMatch
        XCTAssertTrue(marker.waitForExistence(timeout: 5), "the reported row's messages did not expand")
        reveal(marker, in: app)
        sleep(1)
        let openMessages = marker.frame.maxY - expand.frame.minY
        XCTAssertGreaterThan(openMessages, try XCTUnwrap(heights["reported-messages"]) + 8,
                             "opening messages must add their expansion below the still-capped row")

        // Opening this reported row uncaps its main Text. It has the same font and identical five single-character lines
        // as reported-long, so its main Text is an uncapped same-font reference; neither its trailing chevron nor its
        // expansion enters this measurement.
        let uncapped = detailText(app, row: "reported-messages", lines: Self.referenceLines)
        reveal(uncapped, in: app)
        let uncappedHeight = uncapped.frame.height
        print("REPORTED-DETAIL \(layout) reported-messages open Text height=\(uncappedHeight)")
        XCTAssertEqual(try XCTUnwrap(longTextHeight), uncappedHeight, accuracy: 1,
                       "L/M/N/O/P must render within 1 point of the full five-line height of the expanded same-font reference; a four-line cap is one line shorter")
        let summarize = app.buttons["handoff-chip-summarize"].firstMatch
        XCTAssertTrue(summarize.exists, "the summarize chip is missing before the messages-open capture")
        // The chip's top minus the bar's existing vertical padding bounds the whole chip-and-composer footer.
        let barVerticalPadding: CGFloat = 8
        var footerTop = summarize.frame.minY - barVerticalPadding
        let keyboard = app.keyboards.firstMatch
        if keyboard.exists {
            footerTop = min(footerTop, keyboard.frame.minY)
        }
        reveal(marker, in: app, above: footerTop)
        sleep(1) // Let the existing slow scroll settle before the strict final-capture checks.
        XCTAssertTrue(ready(uncapped, in: app, hittable: false, above: footerTop),
                      "the uncapped L/M/N/O/P Text is not fully visible above the chip-and-composer footer:\n\(app.debugDescription)")
        XCTAssertTrue(ready(marker, in: app, hittable: false, above: footerTop),
                      "Expansion marker is not fully visible above the chip-and-composer footer:\n\(app.debugDescription)")
        shot(app, "messages-open")
    }

    @MainActor private func open() throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs apple/PersonaUITests/Fixtures/reported_detail_hub.py (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let environment = ProcessInfo.processInfo.environment
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", environment["PERSONA_UI_SCHEME"] ?? "light",
                               "-tab", "mail", "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", "",
                               "-openInboxItem", "reported-detail-fixture"]
            + (environment["PERSONA_TEXT_SIZE"].map { ["-UIPreferredContentSizeCategoryName", $0] } ?? [])
        app.launchPinned()
        acceptIconAlert()
        regularWidth = app.windows.firstMatch.frame.width > 600
        XCTAssertTrue(app.descendants(matching: .any)["inbox.chain"].firstMatch.waitForExistence(timeout: 20),
                      "no reported-detail timeline:\n\(app.debugDescription)")
        return app
    }

    @MainActor private func entry(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        let chain = app.descendants(matching: .any)["inbox.chain"]
        return chain.descendants(matching: .any)
            .matching(NSPredicate(format: "value == %@", id))
            .matching(NSPredicate(format: "elementType == %d OR (elementType == %d AND label CONTAINS %@)",
                                  XCUIElement.ElementType.button.rawValue,
                                  XCUIElement.ElementType.staticText.rawValue, "\n"))
            .firstMatch
    }

    @MainActor private func detailText(_ app: XCUIApplication, row id: String, lines: [String]) -> XCUIElement {
        let row = entry(app, id)
        var texts: XCUIElementQuery
        if row.elementType == .button {
            texts = row.staticTexts
        } else {
            texts = app.descendants(matching: .any)["inbox.chain"].staticTexts
                .matching(NSPredicate(format: "value == %@", id))
        }
        for line in lines {
            texts = texts.matching(NSPredicate(format: "label CONTAINS %@", line))
        }
        return texts.firstMatch
    }

    /// Finds lazy rows above or below the viewport without relying on their accessibility text.
    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication, hittable: Bool = false,
                                   above bottom: CGFloat? = nil) {
        for upward in [true, false] {
            for _ in 0..<8 {
                if ready(element, in: app, hittable: hittable, above: bottom) { return }
                if upward { app.swipeUp(velocity: .slow) } else { app.swipeDown(velocity: .slow) }
            }
        }
        XCTAssertTrue(ready(element, in: app, hittable: hittable, above: bottom),
                      "row or control is not visible\(hittable ? " and usable" : ""):\n\(app.debugDescription)")
    }

    @MainActor private func ready(_ element: XCUIElement, in app: XCUIApplication, hittable: Bool,
                                  above bottom: CGFloat? = nil) -> Bool {
        guard element.exists, element.frame.height > 0 else { return false }
        let frame = element.frame
        let window = app.windows.firstMatch.frame
        if let bottom {
            return window.contains(frame) && frame.maxY <= bottom && (!hittable || element.isHittable)
        }
        let center = CGPoint(x: frame.midX, y: frame.midY)
        return window.contains(center) && (!hittable || element.isHittable)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot().pngRepresentation
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = "\(name)-\(layout)"
        attachment.lifetime = .keepAlways
        add(attachment)
        let environment = ProcessInfo.processInfo.environment
        if let directory = environment["PERSONA_SHOTS"] {
            let appearance = environment["PERSONA_SHOTS_APPEARANCE"] ?? environment["PERSONA_UI_SCHEME"] ?? "light"
            let size = environment["PERSONA_TEXT_SIZE"].map {
                "-" + $0.replacingOccurrences(of: "[^A-Za-z0-9._-]", with: "_", options: .regularExpression)
            } ?? ""
            let file = "reported-detail-\(name)-\(layout)-\(UITestStyle.name)-\(appearance)\(size).png"
            try? png.write(to: URL(fileURLWithPath: directory).appending(path: file))
        }
    }
}
