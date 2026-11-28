import CoreImage
import XCTest
import simd

/// Changed documents and pictures in the Git tab compare their versions (#1299) instead of 「没有文本改动」.
///   PORT=18785 MOCK_AGENTS=1 MOCK_FILES=1 MOCK_COMPARE=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18785 xcodebuild test … -only-testing:PersonaUITests/CompareFileTests
final class CompareFileTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 MOCK_FILES=1 MOCK_COMPARE=1 (PERSONA_MOCK_HUB)")
        }
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "compare \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    @MainActor private func row(_ name: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", name)).firstMatch
    }

    /// Sample the picture itself so a correct missing-version label cannot hide a stale preview.
    @MainActor private func averageColor(_ element: XCUIElement) throws -> SIMD3<Float> {
        let image = CIImage(cgImage: try XCTUnwrap(element.screenshot().image.cgImage))
        let average = image.applyingFilter("CIAreaAverage", parameters: [kCIInputExtentKey: CIVector(cgRect: image.extent)])
        var rgba = [UInt8](repeating: 0, count: 4)
        CIContext().render(average, toBitmap: &rgba, rowBytes: 4, bounds: average.extent,
                           format: .RGBA8, colorSpace: CGColorSpace(name: CGColorSpace.sRGB))
        return SIMD3(Float(rgba[0]), Float(rgba[1]), Float(rgba[2]))
    }

    @MainActor func testDocumentsAndPicturesCompare() throws {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001", "-openPane", "git"]
        app.launchPinned()
        let docx = row("季度报告.docx")
        XCTAssertTrue(docx.waitForExistence(timeout: 25), "the changed Word file is not in the Git tab")
        docx.tap()
        XCTAssertTrue(app.descendants(matching: .any)["compare-text"].waitForExistence(timeout: 20), "the Word file was not compared")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "+10.2%")).firstMatch.exists, "the new text is missing")
        sleep(1)
        shot("docx")
        app.buttons["sheet-close"].firstMatch.tap()
        let deck = row("季度回顾.pptx")
        XCTAssertTrue(deck.waitForExistence(timeout: 10))
        deck.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "幻灯片")).firstMatch.waitForExistence(timeout: 20),
                      "the deck's changes are not under their slide")
        sleep(1)
        shot("pptx")
        app.buttons["sheet-close"].firstMatch.tap()
        let picture = row("banner.png")
        XCTAssertTrue(picture.waitForExistence(timeout: 10))
        picture.tap()
        XCTAssertTrue(app.descendants(matching: .any)["compare-image"].waitForExistence(timeout: 20), "the picture was not compared")
        sleep(1)
        shot("image")
        let previews = app.images.matching(identifier: "compare-image")
        XCTAssertEqual(previews.count, 2)
        let index = try averageColor(previews.element(boundBy: 0))
        let workingTree = try averageColor(previews.element(boundBy: 1))
        XCTAssertNotEqual(index, workingTree)
        app.buttons["滑块"].firstMatch.tap()
        let divider = app.sliders.firstMatch
        XCTAssertTrue(divider.waitForExistence(timeout: 5))
        for position in [0.2, 0.8, 0.5] {
            divider.adjust(toNormalizedSliderPosition: position)
        }
        shot("image-slider")
        app.buttons["并排"].firstMatch.tap()
        XCTAssertFalse(divider.exists)
        app.buttons["滑块"].firstMatch.tap()
        XCTAssertTrue(divider.waitForExistence(timeout: 5))

        app.buttons["已暂存"].firstMatch.tap()
        XCTAssertTrue(app.staticTexts["这个版本里还没有"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["打开旧版"].isEnabled)
        XCTAssertTrue(app.buttons["打开新版"].isEnabled)
        XCTAssertEqual(previews.count, 1)
        let staged = try averageColor(previews.firstMatch)
        XCTAssertLessThan(simd_distance(staged, index), simd_distance(staged, workingTree))
        app.buttons["sheet-close"].firstMatch.tap()

        let commit = row("Show the city name")
        if !commit.isHittable { app.swipeUp(velocity: .slow) }
        XCTAssertTrue(commit.waitForExistence(timeout: 5))
        commit.tap()
        // The working-tree row can still be in the accessibility tree behind the commit sheet.
        let committedPicture = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@ AND label CONTAINS %@", "D", "banner.png")).firstMatch
        XCTAssertTrue(committedPicture.waitForExistence(timeout: 10))
        committedPicture.tap()
        XCTAssertTrue(app.staticTexts["这个版本里已删除"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["打开旧版"].isEnabled)
        XCTAssertFalse(app.buttons["打开新版"].isEnabled)
        XCTAssertEqual(previews.count, 1)
        let parent = try averageColor(previews.firstMatch)
        XCTAssertLessThan(simd_distance(parent, index), simd_distance(parent, workingTree))
    }
}
