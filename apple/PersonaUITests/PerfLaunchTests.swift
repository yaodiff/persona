import XCTest

/// Cold and warm application launches against the launch lane's large mock data (#4266).
/// Unlike the other performance suites, these tests never enable `FrameProbe` or read `FrameStats`.
///
/// Cold resets the saved pairing and deletes the development snapshot before every launch with the existing DEBUG
/// `-openWelcome` switch and the test runner's access to its own simulator's data container, then pairs in `AppDelegate`.
/// Warm verifies the large data and durable snapshot saved by an unmeasured seed launch. Both measure fresh processes to
/// the first displayed frame, not a resume. XCTest discards its own warm-up, so cold here means fresh pairing/snapshot
/// state with warm OS caches, not a rebooted-device cold launch. Simulator results do not enforce the 400 ms device budget.
final class PerfLaunchTests: PerfTestCase {
    @MainActor private func prepareLaunch(pairing: Bool, probes: Bool = false, fresh: Bool = false) {
        app = XCUIApplication()
        app.launchArguments = ["-dev.openChat", "", "-app.tab", "assistant", "-tab", "assistant",
                               "-openAssistant", PerfAssistantTests.main]
        if pairing { app.launchArguments += ["-pairURL", pair] }
        if probes { app.launchArguments += ["-probes", "YES", "-loadAll", "YES"] }
        if fresh { app.launchArguments += ["-openWelcome", "YES"] }
    }

    /// The host-side UI-test runner reads only this lane's assigned simulator; it never boots or creates one. Resolve
    /// the container each time: installing the app and its first seed launch can replace the data-container UUID.
    @MainActor private func snapshotURL() -> URL? {
        guard let udid = ProcessInfo.processInfo.environment["PERSONA_PERF_SIM"],
              let bundleID = ProcessInfo.processInfo.environment["PERSONA_PERF_BUNDLE"], !bundleID.isEmpty,
              let home = ProcessInfo.processInfo.environment["PERSONA_PERF_SIM_HOME"], !home.isEmpty else {
            XCTFail("the launch lane must supply the simulator, bundle identity and host home directory to the test runner")
            return nil
        }
        let root = URL(fileURLWithPath: home).appending(path: "Library/Developer/CoreSimulator/Devices/\(udid)/data/Containers/Data/Application")
        let manager = FileManager.default
        guard let containers = try? manager.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) else {
            XCTFail("could not list the assigned simulator's data containers")
            return nil
        }
        for container in containers {
            let metadataURL = container.appending(path: ".com.apple.mobile_container_manager.metadata.plist")
            guard let data = try? Data(contentsOf: metadataURL),
                  let metadata = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any],
                  metadata["MCMMetadataIdentifier"] as? String == bundleID else { continue }
            return container.appending(path: "Library/Caches/state/dev.json")
        }
        XCTFail("could not locate the launch app's data container in the assigned simulator")
        return nil
    }

    /// Unlike StateCache.remove's intentionally asynchronous app-side cleanup, this is a pre-launch test barrier.
    @MainActor private func removeSnapshot() {
        guard let url = snapshotURL() else { return }
        try? FileManager.default.removeItem(at: url)
        XCTAssertFalse(FileManager.default.fileExists(atPath: url.path), "the cold-launch snapshot still exists")
    }

    @MainActor private func measureLaunch(_ screenshotName: String, resetSnapshot: Bool = false) {
        var firstLaunch = true
        measure(metrics: [XCTApplicationLaunchMetric(waitUntilResponsive: false)], options: options) {
            if resetSnapshot {
                app.terminate() // wait for the previous process and its detached writers before deleting its snapshot
                removeSnapshot()
            }
            app.launchPinned()
            if firstLaunch {
                allowNotifications()
                acceptIconAlert()
                firstLaunch = false
            }
            see(app.buttons["assistant-sidebar-button"].firstMatch, "the measured Assistant page", timeout: 30)
        }
        let screenshot = XCTAttachment(screenshot: app.screenshot())
        screenshot.name = screenshotName
        screenshot.lifetime = .keepAlways
        add(screenshot)
    }

    /// Probes are enabled only for this unmeasured seed: encoding them would otherwise become launch work of its own.
    @MainActor private func waitForFullAssistant() {
        // 60 added exchanges plus the base exchange: fold_work=1 gives each a user, work and answer item (3 × 61).
        eventually("the full folded Assistant history", within: 60) {
            let value = app.probeEnvelope("chat-rows", page: "assistant")?["v"] as? [String: Any]
            let items = (value?["items"] as? NSNumber)?.intValue ?? 0
            return items >= 183 ? [] : ["only \(items) of all 183 folded long-history items are present"]
        }
    }

    /// A complete atomic file with the expected entity counts is the write barrier; a sleep is not one.
    @MainActor private func verifyWarmSnapshot() {
        var counts: (machines: Int, sessions: Int)?
        var diagnostic = "no snapshot URL"
        let deadline = Date.now.addingTimeInterval(30)
        while Date.now < deadline {
            if let url = snapshotURL() {
                do {
                    let data = try Data(contentsOf: url)
                    let object = try JSONSerialization.jsonObject(with: data)
                    guard let object = object as? [String: Any],
                          let machines = object["machines"] as? [Any], let sessions = object["sessions"] as? [Any] else {
                        diagnostic = "snapshot has no machine and session arrays"
                        usleep(100_000)
                        continue
                    }
                    counts = (machines.count, sessions.count)
                    diagnostic = "machines=\(machines.count) sessions=\(sessions.count)"
                    if machines.count >= 1 && sessions.count >= 120 { break }
                } catch {
                    diagnostic = "\(url.path): \(error.localizedDescription); exists=\(FileManager.default.fileExists(atPath: url.path))"
                }
            }
            usleep(100_000)
        }
        guard let counts, counts.machines >= 1, counts.sessions >= 120 else {
            XCTFail("the warm seed did not durably save the large snapshot: \(diagnostic)")
            return
        }
        let evidence = XCTAttachment(string: "machines=\(counts.machines) sessions=\(counts.sessions)")
        evidence.name = "warm-snapshot-counts"
        evidence.lifetime = .keepAlways
        add(evidence)
    }

    @MainActor func testColdLaunch() throws {
        prepareLaunch(pairing: true, fresh: true)
        app.terminate()
        removeSnapshot()
        measureLaunch("cold-launch", resetSnapshot: true)
    }

    @MainActor func testWarmLaunch() throws {
        prepareLaunch(pairing: true, probes: true, fresh: true)
        app.terminate() // the final cold process (or an earlier test) can still have a detached snapshot writer
        removeSnapshot()
        app.launchPinned()
        allowNotifications()
        acceptIconAlert()
        see(app.buttons["assistant-sidebar-button"].firstMatch, "the paired Assistant page", timeout: 30)
        waitForFullAssistant()
        XCUIDevice.shared.press(.home) // didEnterBackground flushes the snapshot; terminate() alone is not graceful
        verifyWarmSnapshot()
        app.terminate()
        prepareLaunch(pairing: false) // saved pairing: no adopt(), registry write or link rebuild in the measured launch
        measureLaunch("warm-launch")
    }
}
