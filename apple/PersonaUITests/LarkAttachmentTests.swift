import XCTest

/// Mock-only sends; the same draft, preview, outbox and transport as a picked attachment.
final class LarkAttachmentFlowTests: XCTestCase {
    /// Each test starts with no Feishu draft or outbox from the tests before it (`-showLarkSendFresh`, #1961): an
    /// earlier test's failed or unconfirmed file stayed in 「设计评审」 and a later test tapped that one instead of its own.
    /// Only on the first launch: a relaunch inside a test checks what the disk kept.
    private static let fresh = ["-showLarkSendFresh", "YES"]

    @MainActor private func open(_ draft: String, quote: String? = nil) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs mock Hub") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-demo.on", "NO",                                "-inbox.source", "", "-inbox.kind", "mention", "-openInboxFirst", "NO",
                               "-showLarkAttachmentDraft", draft] + (quote.map { ["-showLarkQuoteDraft", $0] } ?? []) + Self.fresh
        app.launchPinned()
        let chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        expandSource(app)
        if !["corrupt", "legacy"].contains(draft) {
            XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "移除 ")).firstMatch.waitForExistence(timeout: 15))
        }
        return app
    }

    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"]
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original-source control did not appear")
        source.tapCenter()
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor private func relaunchWithoutDraft(_ app: XCUIApplication) {
        XCUIDevice.shared.press(.home)
        XCTAssertTrue(app.wait(for: .runningBackground, timeout: 5))
        app.terminate()
        for flag in ["-showLarkAttachmentDraft", Self.fresh[0]] {
            if let i = app.launchArguments.firstIndex(of: flag) { app.launchArguments.removeSubrange(i...i + 1) }
        }
        app.launchPinned()
        let chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        expandSource(app)
    }

    @MainActor func testPhotoDraftSurvivesTerminationWithoutFixtureReinjection() throws {
        let app = try open("photo")
        let field = app.textFields["lark.box"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tapCenter(); field.typeText("Keep my photo draft")
        let complete = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", "Keep my photo draft"), object: field)
        XCTAssertEqual(XCTWaiter.wait(for: [complete], timeout: 5), .completed,
                       "confirm the keyboard delivered the complete draft before terminating")
        shot(app, "lark-draft-before-restart")
        // The restart path deliberately has no fixture: both text and bytes must come from disk.
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["移除 照片.jpg"].waitForExistence(timeout: 10))
        XCTAssertEqual(field.value as? String, "Keep my photo draft")
        shot(app, "lark-draft-restored")
        app.buttons["预览 照片.jpg"].tapCenter()
        XCTAssertTrue(app.buttons["关闭"].waitForExistence(timeout: 8))
        shot(app, "lark-restored-photo-preview")
        app.buttons["关闭"].tapCenter()
        XCTAssertTrue(app.buttons["移除 照片.jpg"].waitForExistence(timeout: 5))
        app.buttons["移除 照片.jpg"].tapCenter()
        app.buttons["发送"].tapCenter()
        XCTAssertTrue(app.buttons["lark.send"].waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForNonExistence(timeout: 15))
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["添加附件"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["移除 照片.jpg"].exists)
        XCTAssertEqual(field.value as? String, "回复 设计评审")
        app.terminate()
    }

    @MainActor func testFailedFileSurvivesTerminationAndRetry() throws {
        let app = try open("fail")
        app.buttons["发送"].tapCenter()
        let failed = app.buttons["lark.outgoing.failed"]
        XCTAssertTrue(failed.waitForExistence(timeout: 15))
        relaunchWithoutDraft(app)
        XCTAssertTrue(failed.waitForExistence(timeout: 15))
        XCTAssertTrue(app.buttons["预览 retry-once.txt"].exists)
        shot(app, "lark-failure-restored")
        failed.tapCenter()
        app.buttons["重发"].tapCenter()
        XCTAssertTrue(failed.waitForNonExistence(timeout: 15))
        XCTAssertTrue(app.buttons["预览 retry-once.txt"].waitForNonExistence(timeout: 15))
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["添加附件"].waitForExistence(timeout: 10))
        XCTAssertFalse(failed.exists)
        XCTAssertFalse(app.buttons["预览 retry-once.txt"].exists)
        shot(app, "lark-retry-restored-once")
        app.terminate()
    }

    @MainActor func testLostFileReceiptOffersCheckingAndRestartReconciles() throws {
        let app = try open("receipt")
        app.buttons["发送"].tapCenter()
        let unknown = app.buttons["lark.outgoing.unconfirmed"]
        XCTAssertTrue(unknown.waitForExistence(timeout: 15))
        shot(app, "lark-file-unconfirmed")
        unknown.tapCenter()
        XCTAssertTrue(app.buttons["检查发送结果"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["重发"].exists)
        XCTAssertFalse(app.buttons["放回输入框"].exists)
        shot(app, "lark-file-check-only")
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["添加附件"].waitForExistence(timeout: 10))
        XCTAssertTrue(unknown.waitForNonExistence(timeout: 10))
        XCTAssertFalse(app.buttons["预览 lost-receipt.txt"].exists)
        app.terminate()
    }

    @MainActor func testUnreadableDraftRecoveryReturnsToUsableComposer() throws {
        let app = try open("corrupt")
        XCTAssertTrue(app.buttons["恢复输入"].waitForExistence(timeout: 12))
        shot(app, "lark-draft-unreadable")
        app.buttons["恢复输入"].tapCenter()
        XCTAssertTrue(app.buttons["清除草稿并重新开始"].waitForExistence(timeout: 5))
        shot(app, "lark-draft-recovery-confirm")
        app.buttons["清除草稿并重新开始"].tapCenter()
        XCTAssertTrue(app.buttons["恢复输入"].waitForNonExistence(timeout: 8))
        let input = app.textFields["lark.box"]
        XCTAssertTrue(input.waitForExistence(timeout: 5))
        XCTAssertTrue(input.isEnabled)
        input.tapCenter(); input.typeText("Recovered draft")
        XCTAssertTrue(app.buttons["lark.send"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["lark.send"].isEnabled)
        shot(app, "lark-draft-recovered")
        app.terminate()
    }

    @MainActor func testPhotoPreviewPullDownRemoveAndPicker() throws {
        let app = try open("photo")
        shot(app, "lark-photo-draft")
        app.buttons["预览 照片.jpg"].tapCenter()
        XCTAssertTrue(app.buttons["关闭"].waitForExistence(timeout: 8))
        shot(app, "lark-photo-preview")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).press(forDuration: 0.1,
            thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.85)))
        XCTAssertTrue(app.buttons["移除 照片.jpg"].waitForExistence(timeout: 5))
        app.buttons["移除 照片.jpg"].tapCenter()
        XCTAssertFalse(app.buttons["移除 照片.jpg"].exists)
        XCTAssertTrue(app.buttons["lark.send"].waitForNonExistence(timeout: 3), "removing the last attachment hides the empty send button")
        app.buttons["添加附件"].tapCenter()
        XCTAssertTrue(app.buttons["照片"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["文件"].exists)
        app.buttons["照片"].tapCenter()
        let cancel = app.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel"])).firstMatch
        XCTAssertTrue(cancel.waitForExistence(timeout: 8)); cancel.tapCenter()
        XCTAssertTrue(app.buttons["添加附件"].waitForExistence(timeout: 5))
        app.terminate()
    }

    @MainActor func testPhotoSendReconcilesOnce() throws {
        let app = try open("photo")
        XCTAssertTrue(app.buttons["lark.send"].isEnabled, "a photo can be sent without typing text")
        app.buttons["发送"].tapCenter()
        XCTAssertFalse(app.buttons["移除 照片.jpg"].exists)
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForExistence(timeout: 3))
        shot(app, "lark-photo-sending")
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForNonExistence(timeout: 15))
        XCTAssertTrue(app.buttons["预览 照片.jpg"].waitForNonExistence(timeout: 10))
        let latest = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "回到最新")).firstMatch
        let reachedBottom = latest.waitForNonExistence(timeout: 5)
        shot(app, "lark-photo-sent")
        XCTAssertTrue(reachedBottom, "a receipt must keep the just-sent image at the bottom")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)).press(forDuration: 0.1,
            thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75)))
        XCTAssertTrue(latest.waitForExistence(timeout: 5), "scrolling back into history must still offer the latest jump")
        app.terminate()
    }

    @MainActor func testFileFailureRestoreAndRetry() throws {
        let app = try open("fail")
        shot(app, "lark-file-draft")
        app.buttons["预览 retry-once.txt"].tapCenter()
        let done = app.buttons.matching(NSPredicate(format: "label IN %@", ["关闭", "Close", "完成", "Done"])).firstMatch
        XCTAssertTrue(done.waitForExistence(timeout: 8))
        XCTAssertTrue(app.buttons["QLTextItemViewControllerBarSearchRightButtonAccessibilityIdentifier"].waitForExistence(timeout: 8))
        shot(app, "lark-file-preview")
        // Quick Look is hosted out of process; use its screen frame, not its remote coordinate origin.
        let closeFrame = done.frame
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: closeFrame.midX, dy: closeFrame.midY)).tap()
        XCTAssertTrue(done.waitForNonExistence(timeout: 5))
        app.buttons["发送"].tapCenter()
        let failed = app.buttons["lark.outgoing.failed"]
        let didFail = failed.waitForExistence(timeout: 12)
        shot(app, "lark-file-failed")
        XCTAssertTrue(didFail)
        // Leaving the chat does not discard the failed file or require picking it again.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5)).press(forDuration: 0.1,
            thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)))
        let chat = app.staticTexts["设计评审"].firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 8)); chat.tapCenter()
        expandSource(app)
        XCTAssertTrue(failed.waitForExistence(timeout: 10))
        failed.tapCenter()
        XCTAssertTrue(app.buttons["重发"].waitForExistence(timeout: 5))
        app.buttons["重发"].tapCenter()
        // A fast retry may finish while the menu dismisses; assert the durable outcome, not a minimum spinner time.
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForNonExistence(timeout: 15))
        XCTAssertTrue(failed.waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.buttons["预览 retry-once.txt"].waitForNonExistence(timeout: 10))
        shot(app, "lark-file-retried")
        app.terminate()
    }

    @MainActor func testAcceptedFileWithoutIDCanRemoveOnlyLocalReceipt() throws {
        let app = try open("idless")
        app.buttons["发送"].tapCenter()
        let receipt = app.buttons["lark.outgoing.sentReceipt"]
        XCTAssertTrue(receipt.waitForExistence(timeout: 15))
        relaunchWithoutDraft(app)
        XCTAssertTrue(receipt.waitForExistence(timeout: 15))
        receipt.tapCenter()
        XCTAssertTrue(app.buttons["移除本机记录"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["重发"].exists)
        shot(app, "lark-idless-file-receipt")
        app.buttons["移除本机记录"].tapCenter()
        XCTAssertTrue(receipt.waitForNonExistence(timeout: 8))
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["添加附件"].waitForExistence(timeout: 10))
        XCTAssertFalse(receipt.exists)
        app.terminate()
    }

    @MainActor func testLegacyDraftRequiresAccountConfirmationThenSurvivesRestart() throws {
        let app = try open("legacy")
        XCTAssertTrue(app.staticTexts["发现旧版草稿"].waitForExistence(timeout: 12))
        XCTAssertFalse(app.buttons["移除 legacy-draft.txt"].exists)
        app.buttons["恢复"].tapCenter()
        XCTAssertTrue(app.buttons["确认属于当前账号，恢复"].waitForExistence(timeout: 5))
        shot(app, "lark-legacy-draft-confirm")
        app.buttons["确认属于当前账号，恢复"].tapCenter()
        XCTAssertTrue(app.buttons["移除 legacy-draft.txt"].waitForExistence(timeout: 8))
        XCTAssertEqual(app.textFields["lark.box"].value as? String, "Legacy draft")
        XCTAssertFalse(app.buttons["lark.outgoing.sending"].exists)
        relaunchWithoutDraft(app)
        XCTAssertTrue(app.buttons["移除 legacy-draft.txt"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["发现旧版草稿"].exists)
        XCTAssertEqual(app.textFields["lark.box"].value as? String, "Legacy draft")
        shot(app, "lark-legacy-draft-restored")
        app.buttons["移除 legacy-draft.txt"].tapCenter()
        app.buttons["发送"].tapCenter()
        XCTAssertTrue(app.buttons["lark.send"].waitForNonExistence(timeout: 5))
        app.terminate()
    }

    /// #1035: a photo quoting a message keeps the quote when the hub can carry it, and stays in the box with a reason
    /// when it cannot (an older Feishu extension would send it on its own). Run against both kinds of mock.
    @MainActor func testQuotedPhotoFollowsWhatTheHubCanCarry() async throws {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"])
        var health = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/health")))
        health.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let (data, _) = try await URLSession.shared.data(for: health)
        let features = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["features"] as? [String] ?? []
        let carries = features.contains("lark_attachment_reply")
        let app = try open("photo", quote: "m201")
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "标签栏的图标偏细")).firstMatch
            .waitForExistence(timeout: 10), "the box quotes the message")
        shot(app, carries ? "lark-photo-quote-draft" : "lark-photo-quote-old-draft")
        app.buttons["发送"].tapCenter()
        if carries {
            XCTAssertTrue(app.buttons["移除 照片.jpg"].waitForNonExistence(timeout: 5))
            let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch
            XCTAssertTrue(sending.waitForNonExistence(timeout: 15))
            let (uploads, _) = try await URLSession.shared.data(from: try XCTUnwrap(URL(string: hub + "/mock/lark/uploads")))
            let sent = try XCTUnwrap(JSONSerialization.jsonObject(with: uploads) as? [String: [String: Any]])
            XCTAssertTrue(sent.values.contains { $0["reply_to"] as? String == "m201" && $0["thread"] as? Bool == false },
                          "the photo went out quoting the message, not on its own: \(sent)")
            try await Task.sleep(for: .seconds(2))
            shot(app, "lark-photo-quote-sent")
        } else {
            XCTAssertTrue(app.staticTexts["需要更新 Hub 上的飞书扩展，才能引用消息发送图片和文件"].waitForExistence(timeout: 5))
            XCTAssertTrue(app.buttons["移除 照片.jpg"].exists, "the photo stays in the box")
            XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "标签栏的图标偏细")).firstMatch.exists,
                          "and so does the quote")
            shot(app, "lark-photo-quote-old-plugin")
        }
        app.terminate()
    }

    // MARK: Pictures and files in a thread (#1035)

    private func hubCarriesAttachmentReplies() async throws -> Bool {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"])
        var health = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/health")))
        health.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let (data, _) = try await URLSession.shared.data(for: health)
        return ((try JSONSerialization.jsonObject(with: data) as? [String: Any])?["features"] as? [String] ?? []).contains("lark_attachment_reply")
    }

    private func uploads() async throws -> [String: [String: Any]] {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"])
        let (data, _) = try await URLSession.shared.data(from: try XCTUnwrap(URL(string: hub + "/mock/lark/uploads")))
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: [String: Any]])
    }

    /// 「周末徒步群」 at its thread (60 replies), the thread's box with `draft` picked.
    @MainActor private func openThread(_ draft: String) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs mock Hub") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-demo.on", "NO",                                "-openInboxItem", "lark:f3:300", "-showLarkThreadAttachmentDraft", draft] + Self.fresh
        app.launchPinned()
        expandSource(app)
        let thread = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "条回复")).firstMatch
        XCTAssertTrue(thread.waitForExistence(timeout: 20), "the chat did not open at its thread")
        thread.tapCenter()
        XCTAssertTrue(app.textFields["lark.thread.box"].waitForExistence(timeout: 10), "the thread sheet has a box")
        return app
    }

    @MainActor func testThreadPhotoGoesIntoTheThreadWhenTheHubCanCarryIt() async throws {
        let carries = try await hubCarriesAttachmentReplies()
        let app = try openThread("photo")
        let picked = app.buttons["移除 照片.jpg"]
        guard carries else {
            // An older extension would send it to the chat on its own: the thread offers no attachments.
            XCTAssertFalse(picked.waitForExistence(timeout: 5), "no attachment in a thread without the hub's support")
            shot(app, "lark-thread-photo-old-plugin")
            app.terminate(); return
        }
        XCTAssertTrue(picked.waitForExistence(timeout: 10))
        shot(app, "lark-thread-photo-draft")
        let before = try await uploads().count
        app.buttons["lark.thread.send"].tapCenter()
        XCTAssertTrue(picked.waitForNonExistence(timeout: 5))
        var sent: [String: Any]?
        for _ in 0..<60 where sent == nil {
            let now = try await uploads()
            if now.count > before { sent = now.values.first { $0["thread"] as? Bool == true && ($0["thread_root"] as? String)?.isEmpty == false } }
            if sent == nil { try await Task.sleep(for: .milliseconds(250)) }
        }
        XCTAssertNotNil(sent, "the photo went into the thread, with its root")
        try await Task.sleep(for: .seconds(3))
        shot(app, "lark-thread-photo-sent")
        app.terminate()
    }

    @MainActor func testFailedThreadFileRetriesOnceIntoTheThread() async throws {
        guard try await hubCarriesAttachmentReplies() else { throw XCTSkip("needs the hub's support") }
        let app = try openThread("fail")
        XCTAssertTrue(app.buttons["移除 retry-once.txt"].waitForExistence(timeout: 10))
        app.buttons["lark.thread.send"].tapCenter()
        let failed = app.buttons["lark.outgoing.failed"]
        XCTAssertTrue(failed.waitForExistence(timeout: 15), "the first try fails and says so in the thread")
        shot(app, "lark-thread-file-failed")
        failed.tapCenter()
        XCTAssertTrue(app.buttons["重发"].waitForExistence(timeout: 5))
        app.buttons["重发"].tapCenter()
        XCTAssertTrue(failed.waitForNonExistence(timeout: 15), "the retry goes out")
        let sent = try await uploads().values.filter { $0["thread"] as? Bool == true }
        XCTAssertFalse(sent.isEmpty)
        shot(app, "lark-thread-file-retried")
        app.terminate()
    }
}
