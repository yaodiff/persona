import XCTest

/// The composer's dictation in the main conversation (the shell's mic tapped, not held): the bar coming in, ten
/// seconds of listening, and the two ways out (cancel, and send). Words come from `-showDictationSample` (a simulator
/// has no microphone); the bar's levels follow them. Mock: the `assistant` suite's (`scripts/app/perf-test.sh composer`).
///
/// Each phase gets its own frame label, so one run yields `enter`, `listen`, `cancel` and `send` samples. The taps
/// inside a measured window hit points found before it (an element query snapshots the long conversation's
/// accessibility tree on the app's main thread). Between phases the test prints `dictation-mark <phase> <start|end>
/// <unix seconds>` so a host-side CPU sampler can cut its samples to the listening windows.
final class PerfDictationTests: PerfTestCase {
    private static let words = Array(repeating: "看一下 这个 页面 滚动 的时候 为什么 会卡 顺便 测一下 输入 的 延迟", count: 4)
        .joined(separator: " ")

    @MainActor private func openMain() {
        launch(["-dev.openChat", "", "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", PerfAssistantTests.main,
                "-holdChromeOpen", "YES", "-perfCounts", "YES", "-showDictationSample", Self.words])
        see(app.buttons["assistant-sidebar-button"], "the main conversation", timeout: 30)
        see(any("shell-composer-mic"), "the composer's mic")
        sleep(2)
    }

    @MainActor private func point(_ e: XCUIElement) -> XCUICoordinate {
        let f = e.frame
        return app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.midX, dy: f.midY))
    }

    /// Frames between the start and stop cues, under `label`.
    @MainActor private func frames(_ label: String, _ block: () -> Void) {
        mock("frames_label?name=PerfDictationTests/" + label)
        print("dictation-mark \(label) start \(Date().timeIntervalSince1970)")
        darwinPost("com.persona.perf.frames.start")
        block()
        darwinPost("com.persona.perf.frames.stop")
        print("dictation-mark \(label) end \(Date().timeIntervalSince1970)")
        usleep(500_000)
    }

    /// The bar's way out named `id`; a build without the bar stops with the mic (cancel) or sends with send.
    @MainActor private func exit(_ id: String, orOld old: String) -> XCUICoordinate {
        let bar = any(id)
        return point(bar.waitForExistence(timeout: 2) ? bar : any(old))
    }

    @MainActor func testDictationBarFrames() throws {
        for _ in 0..<iterations {
            openMain()
            let mic = point(any("shell-composer-mic"))
            frames("enter") {
                mic.tap()
                usleep(1_200_000)
            }
            frames("listen") { sleep(10) }
            let cancel = exit("dictation-bar-cancel", orOld: "shell-composer-mic")
            frames("cancel") {
                cancel.tap()
                usleep(1_200_000)
            }
            sleep(1)
            point(any("shell-composer-mic")).tap()
            sleep(3)
            let send = exit("dictation-bar-send", orOld: "shell-composer-send")
            frames("send") {
                send.tap()
                usleep(1_500_000)
            }
            app.terminate()
        }
        sleep(1)
        var report: [String: Any] = [:]
        for label in ["enter", "listen", "cancel", "send"] {
            let runs = (mock("frames?name=PerfDictationTests/" + label)["runs"] as? [[String: Any]]) ?? []
            report[label] = runs
            XCTAssertFalse(runs.isEmpty, "no frames for \(label)")
        }
        if let data = try? JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted, .sortedKeys]) {
            let a = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
            a.name = "dictation-frames.json"
            a.lifetime = .keepAlways
            add(a)
            if let dir = ProcessInfo.processInfo.environment["PERSONA_PERF_OUT"] {
                try? data.write(to: URL(fileURLWithPath: dir).appending(path: "dictation-frames.json"))
            }
        }
    }

    /// Profiling aid (not a measurement): the bar opened and cancelled eight times, for `sample` on the host.
    @MainActor func testDictationBarCancelLoop() throws {
        openMain()
        let mic = point(any("shell-composer-mic"))
        mic.tap()
        sleep(2)
        let cancel = point(any("dictation-bar-cancel"))
        cancel.tap()
        sleep(1)
        print("dictation-mark loop start \(Date().timeIntervalSince1970)")
        for _ in 0..<8 {
            mic.tap()
            sleep(2)
            cancel.tap()
            sleep(1)
        }
        print("dictation-mark loop end \(Date().timeIntervalSince1970)")
    }

    /// For the screen recording, at a person's pace: tap the mic, speak, stop (the words go into the draft); clear it;
    /// tap, speak, cancel; tap, speak, send.
    @MainActor func testDictationBarShowcase() throws {
        openMain()
        print("dictation-mark showcase start \(Date().timeIntervalSince1970)")
        any("shell-composer-mic").tap()
        sleep(4)
        let stop = any("dictation-bar-stop")
        (stop.waitForExistence(timeout: 1) ? stop : any("shell-composer-mic")).tap()
        sleep(3)
        any("shell-composer-mic").tap()
        sleep(3)
        let cancel = any("dictation-bar-cancel")
        if cancel.waitForExistence(timeout: 1) { cancel.tap() } else { any("shell-composer-mic").tap() }
        sleep(2)
        any("shell-composer-mic").tap()
        sleep(3)
        let send = any("dictation-bar-send")
        (send.waitForExistence(timeout: 1) ? send : any("shell-composer-send")).tap()
        sleep(3)
        print("dictation-mark showcase end \(Date().timeIntervalSince1970)")
    }
}
