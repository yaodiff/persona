import XCTest

/// Rename, move, select, move to the trash and Undo in a machine's home (#315, docs/protocol/protocol.md "Changing files"),
/// against scripts/mock/mock_hub.py started with MOCK_FILES=1 MOCK_FILE_OPS=1:
///   TEST_RUNNER_PERSONA_FILES_HUB=http://127.0.0.1:<port>
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves a screenshot of each step there. The mock's home has code/ and notes.txt.
final class FolderEditTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_FILES_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_FILES=1 MOCK_FILE_OPS=1 (PERSONA_FILES_HUB)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook"]
        app.launchPinned()
    }

    private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            let device = UIDevice.current.userInterfaceIdiom == .pad ? "-ipad" : ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + device + look + ".png"))
        }
    }

    private func openHome() {
        let files = app.buttons["machine.files"]
        XCTAssertTrue(files.waitForExistence(timeout: 40), "no 文件 row on the machine page")
        for _ in 0..<6 where !files.isHittable { app.swipeUp(velocity: .slow) }
        files.tap()
        XCTAssertTrue(app.staticTexts["notes.txt"].waitForExistence(timeout: 15))
    }

    func testRenameMoveTrashAndUndo() {
        openHome()

        // Long press: the changes under Save / Copy Path.
        app.staticTexts["notes.txt"].press(forDuration: 1.2)
        let rename = app.buttons["重命名"]
        XCTAssertTrue(rename.waitForExistence(timeout: 5), "no 重命名 in the menu")
        XCTAssertTrue(app.buttons["移动"].exists && app.buttons["移到废纸篓"].exists, "no 移动 / 移到废纸篓 in the menu")
        shot("edit-menu")
        rename.tap()

        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 5), "no rename alert")
        let field = alert.textFields.firstMatch
        XCTAssertEqual(field.value as? String, "notes.txt", "the field does not start with the old name")
        shot("edit-rename")
        field.tap()
        // Return commits the letters (the simulator's keyboard may be pinyin, which holds them as a composition).
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 12) + "todo.txt\n")
        if alert.buttons["重命名"].exists { alert.buttons["重命名"].tap() }
        XCTAssertTrue(app.staticTexts["todo.txt"].waitForExistence(timeout: 15), "the renamed file never showed up")
        XCTAssertFalse(app.staticTexts["notes.txt"].exists, "the old name is still listed")

        // Move into code/ with the folder picker.
        app.staticTexts["todo.txt"].press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["移动"].waitForExistence(timeout: 5))
        app.buttons["移动"].tap()
        let here = app.buttons["files.move.here"]
        XCTAssertTrue(here.waitForExistence(timeout: 10), "no folder picker")
        XCTAssertFalse(here.isEnabled, "moving to the folder it is in is offered")
        let picker = app.collectionViews["files.move.list"]
        XCTAssertTrue(picker.staticTexts["code"].waitForExistence(timeout: 10))
        shot("edit-move")
        picker.staticTexts["code"].tap()
        XCTAssertTrue(app.staticTexts["weather"].waitForExistence(timeout: 10), "code/ did not open in the picker")
        XCTAssertTrue(app.buttons["files.move.here"].isEnabled)
        shot("edit-move-code")
        app.buttons["files.move.here"].tap()
        XCTAssertTrue(app.staticTexts["已把「todo.txt」移到 code"].waitForExistence(timeout: 10), "no moved toast")
        shot("edit-moved")
        XCTAssertTrue(app.staticTexts["todo.txt"].waitForNonExistence(timeout: 15), "the moved file is still in the home")

        // Select, trash with the confirmation, Undo from the toast.
        app.buttons["files.more"].tap()
        XCTAssertTrue(app.buttons["选择"].waitForExistence(timeout: 5), "no 选择 in the ⋯ menu")
        app.buttons["选择"].tap()
        XCTAssertTrue(app.staticTexts["files.select.count"].waitForExistence(timeout: 5), "no selection bar")
        XCTAssertFalse(app.buttons["files.select.trash"].isEnabled, "trash offered with nothing selected")
        app.staticTexts["code"].tap()
        XCTAssertEqual(app.staticTexts["files.select.count"].label, "已选 1 项")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "selected == true")).firstMatch.waitForExistence(timeout: 5), "the row is not marked selected")
        sleep(1) // the checkmark fades in
        shot("edit-select")
        app.buttons["files.select.trash"].tap()
        let confirm = app.alerts["把「code」移到废纸篓？"]
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "no confirmation before the trash")
        shot("edit-trash-confirm")
        confirm.buttons["移到废纸篓"].tap()
        XCTAssertTrue(app.buttons["toast-action"].waitForExistence(timeout: 10), "no Undo on the toast")
        XCTAssertTrue(app.staticTexts["code"].waitForNonExistence(timeout: 10), "code/ is still listed")
        shot("edit-trashed")
        app.buttons["toast-action"].tap()
        XCTAssertTrue(app.staticTexts["code"].waitForExistence(timeout: 15), "Undo did not put code/ back")
        shot("edit-restored")
    }

    func testRemoteCopyProgressLight() {
        remoteProgress("light", copying: true)
    }

    func testRemoteCompressProgressDark() {
        remoteProgress("dark", copying: false)
    }

    func testRemoteCopyProgressLightLargestText() {
        remoteProgress("light", copying: true, largestText: true)
    }

    func testRemoteCompressProgressDarkLargestText() {
        remoteProgress("dark", copying: false, largestText: true)
    }

    func testRemoteCopyProgressResumesAfterEarlyReturn() {
        openHome()
        app.staticTexts["notes.txt"].press(forDuration: 1.2)
        let action = app.buttons["创建副本"]
        XCTAssertTrue(action.waitForExistence(timeout: 5), "no Duplicate in the row menu")
        action.tap()

        // Leave before the first one-second poll can make the row visible. Returning must resume the POST's polling
        // even though there is no transfers bar whose onAppear could do it.
        let back = app.navigationBars.buttons.firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 5), "no way back to the machine")
        back.tap()
        XCTAssertTrue(app.buttons["machine.files"].waitForExistence(timeout: 10), "the machine page did not return")
        sleep(2)
        openHome()

        let cancel = app.buttons["取消复制 notes.txt"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 8), "the running copy did not regain progress and Cancel")
        cancel.tap()
        XCTAssertTrue(cancel.waitForNonExistence(timeout: 5), "the cancelled operation stayed in the transfer bar")
        XCTAssertFalse(app.staticTexts["notes 2.txt"].waitForExistence(timeout: 2), "the cancelled copy left its output")
    }

    /// The app switcher, Control Center or a call only pause the browser's progress polling (#6113): the system's Save to
    /// Files (.fileMover) and the downloaded file it moves are still there after the app goes to the background and back.
    func testSaveToFilesSurvivesTheAppGoingToTheBackground() {
        openHome()
        app.staticTexts["notes.txt"].press(forDuration: 1.2)
        let save = app.buttons["存到「文件」"]
        XCTAssertTrue(save.waitForExistence(timeout: 5), "no 存到「文件」 in the menu")
        save.tap()
        // The place picker runs in another process: its buttons are not in this app's tree, but it covers the browser.
        let upload = app.buttons["files.upload"]
        let covered = expectation(for: NSPredicate(format: "hittable == false"), evaluatedWith: upload)
        wait(for: [covered], timeout: 30)
        sleep(2)
        shot("save-picker")

        XCUIDevice.shared.press(.home) // active → inactive → background
        XCTAssertTrue(app.wait(for: .runningBackground, timeout: 10) || app.wait(for: .runningBackgroundSuspended, timeout: 10),
                      "the app did not go to the background")
        app.activate()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 10), "the app did not come back")
        sleep(3)
        shot("save-picker-after-background")
        XCTAssertTrue(upload.exists, "the browser went away")
        XCTAssertFalse(upload.isHittable, "going to the background closed Save to Files and dropped the download")

        // Save into On My iPhone (the picker's Save, top right). On My iPhone must not hold a notes.txt already, or the
        // picker asks to replace it: the run script removes it first and checks afterwards that the file arrived.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.876, dy: 0.126)).tap()
        let saved = expectation(for: NSPredicate(format: "hittable == true"), evaluatedWith: upload)
        wait(for: [saved], timeout: 20)
        shot("save-picker-saved")
    }

    /// MOCK_FILE_OP_DELAY keeps the original POST open: the bar appears after one second and Cancel leaves no output.
    private func remoteProgress(_ scheme: String, copying: Bool, largestText: Bool = false) {
        app.terminate()
        app.launchArguments += ["-ui.scheme", scheme]
        if largestText { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        openHome()

        app.staticTexts["notes.txt"].press(forDuration: 1.2)
        let action = app.buttons[copying ? "创建副本" : "压缩"]
        if largestText { for _ in 0..<4 where !action.exists { app.swipeUp() } }
        XCTAssertTrue(action.waitForExistence(timeout: 5), "no remote operation in the row menu:\n\(app.debugDescription)")
        action.tap()
        let cancel = app.buttons[copying ? "取消复制 notes.txt" : "取消压缩 notes.txt"]
        XCTAssertTrue(cancel.waitForExistence(timeout: 10), "no progress bar with Cancel:\n\(app.debugDescription)")
        sleep(2) // past the one-second threshold, with a polled byte count on screen
        shot((copying ? "copy-progress" : "compress-progress") + (largestText ? "-ax" : ""))
        cancel.tap()
        XCTAssertTrue(cancel.waitForNonExistence(timeout: 5), "the cancelled operation stayed in the transfer bar")
        XCTAssertFalse(app.staticTexts[copying ? "notes 2.txt" : "notes.zip"].waitForExistence(timeout: 2),
                       "the cancelled operation left its output behind")
    }

    /// iPad: files dragged in from another app outline the folder (the DEBUG `-showFilesDrop`; a drag from another
    /// app can't be driven from here).
    func testDropOutlinesTheFolder() throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        app.terminate()
        app.launchArguments += ["-showFilesDrop", "YES"]
        app.launchPinned()
        openHome()
        XCTAssertTrue(app.otherElements["files.drop"].waitForExistence(timeout: 5) || app.images["files.drop"].exists, "no drop outline")
        shot("edit-drop")
    }
}
