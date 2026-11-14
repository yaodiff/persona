import Foundation
import Testing
@testable import Persona

/// Settings' Live Screen row (#6398, #6396, #7775): there only when the page can change something for a computer here,
/// so someone using only the assistant never sees it. Scroll Speed applies to every computer that shows its live screen.
@MainActor
struct LiveScreenRowTests {
    private func machine(_ extra: String) throws -> Machine {
        try JSON.decoder.decode(Machine.self, from: Data("{\"id\":\"macbook\",\"name\":\"MacBook\",\"kind\":\"ssh\",\"online\":true\(extra)}".utf8))
    }

    @Test func noComputerWithALiveScreenNoRow() throws {
        #expect(!LiveScreenSettingsView.isShown(machines: []), "no computer")
        #expect(!LiveScreenSettingsView.isShown(machines: [try machine("")]), "a computer without its live screen")
        #expect(!LiveScreenSettingsView.isShown(machines: [try machine(",\"computer\":{\"enabled\":false,\"screen_sharing\":true}")]),
                "the extension off")
    }

    @Test func anyLiveScreenHasTheRow() throws {
        let screen = try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true}")
        #expect(LiveScreenSettingsView.isShown(machines: [screen]), "Scroll Speed applies to it, with or without the video channel")
        let video = try machine(",\"computer\":{\"enabled\":true,\"screen_sharing\":true,\"screen_video\":{\"version\":1}}")
        #expect(LiveScreenSettingsView.isShown(machines: [video]), "and quality on mobile data too")
    }

    /// Scroll Speed: the default unless chosen, and what each choice does to the distance.
    @Test func scrollSpeedScalesTheDistance() {
        #expect(VNCScrollSpeed(rawValue: "default") == .normal && VNCScrollSpeed(rawValue: "odd") == nil)
        #expect(VNCScrollSpeed.slower.factor < 1 && VNCScrollSpeed.normal.factor == 1 && VNCScrollSpeed.faster.factor > 1)
        #expect(VNCScrollSpeed.allCases.map(\.rawValue) == ["slower", "default", "faster"], "Slower, Default, Faster, in that order")
    }
}
