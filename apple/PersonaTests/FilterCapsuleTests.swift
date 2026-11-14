import CoreGraphics
import Testing
@testable import Persona

struct FilterCapsuleTests {
    @Test func compactLabelsKeepTwoGraphemesAndAnEllipsis() {
        #expect(FilterCapsuleLabel.compact("完成未看") == "完成…")
        #expect(FilterCapsuleLabel.compact("Working") == "Wo…")
        #expect(FilterCapsuleLabel.compact("作業中") == "作業…")
        #expect(FilterCapsuleLabel.compact("全部") == "全部")
        #expect(FilterCapsuleLabel.compact("👩🏽‍💻👨‍👩‍👧‍👦abc") == "👩🏽‍💻👨‍👩‍👧‍👦…")
    }

    @Test func focusUsesTheVisibleViewportNotOffscreenItems() {
        let frames = ["offscreen": CGRect(x: -160, y: 0, width: 100, height: 44),
                      "left": CGRect(x: 0, y: 0, width: 60, height: 44),
                      "center": CGRect(x: 75, y: 0, width: 80, height: 44),
                      "right": CGRect(x: 170, y: 0, width: 80, height: 44)]
        #expect(FilterCapsuleLabel.centered(frames: frames, width: 240) == "center")
        #expect(FilterCapsuleLabel.centered(frames: [:], width: 240) == nil)
    }
}
