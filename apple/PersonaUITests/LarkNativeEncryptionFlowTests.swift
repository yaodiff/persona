import XCTest

/// All passwords, ciphertext and sends go to the isolated mock Hub.
final class LarkNativeEncryptionFlowTests: XCTestCase {
    @MainActor private func open(inbox: Bool = false) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs native encryption mock") }
        // The old /api/v1/lark's cover picker and unlock sheet; a messaging source asks through compose
        // (MessagingSealedFlowTests, #2138).
        if ProcessInfo.processInfo.environment["PERSONA_MESSAGING"] == "1" { throw XCTSkip("the old /api/v1/lark only") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", inbox ? "mail" : "feishu", "-demo.on", "NO",
            "-integrations.lark.enabled", "YES",
            "-inbox.source", "", "-inbox.kind", "mention", "-openInboxFirst", "NO"]
        app.launchPinned()
        if !inbox {
            let messages = app.buttons.matching(identifier: "shell-feishu").firstMatch
            XCTAssertTrue(messages.waitForExistence(timeout: 15))
            messages.tapCenter()
        }
        let chat = inbox ? app.staticTexts["设计评审"].firstMatch : app.buttons["feishu-chat-f2"]
        XCTAssertTrue(chat.waitForExistence(timeout: 20)); chat.tapCenter()
        if inbox {
            let source = app.buttons["inbox.detail.source"].firstMatch
            XCTAssertTrue(source.waitForExistence(timeout: 15), "the original source control did not appear")
            source.tapCenter()
        }
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 15))
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name; attachment.lifetime = .keepAlways; add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    private func resetMock(locked: Bool = true, failOnce: Bool = false, unavailable: String = "") async throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"], let url = URL(string: hub + "/test/lark-encryption/reset") else { throw XCTSkip("needs native encryption mock") }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"; request.httpBody = try JSONSerialization.data(withJSONObject: ["locked": locked, "fail_once": failOnce, "unavailable": unavailable])
        let (_, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
    }

    @MainActor func testUpgradeAndAuthorizationChecksKeepDraftAndRecover() async throws {
        try await resetMock(unavailable: "hub")
        let app = try open()
        let field = app.textFields["lark.box"]
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.98, dy: 0.8)).tap()
        if let value = field.value as? String, !value.hasPrefix("回复 ") { field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: value.count)) }
        XCTAssertTrue((field.value as? String ?? "").isEmpty || field.value as? String == field.placeholderValue)
        field.typeText("Keep this private draft")
        for (kind, text) in [("hub", "升级 Hub 后"), ("gateway", "升级飞书网关后"), ("authorization", "尚未开启手机加密")] {
            try await resetMock(unavailable: kind)
            app.buttons["lark.encryption.mode"].tapCenter()
            app.buttons["lark.encryption.encrypted"].tapCenter()
            let notice = app.alerts["暂时无法使用加密消息"]
            XCTAssertTrue(notice.waitForExistence(timeout: 10))
            XCTAssertTrue(notice.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", text)).firstMatch.exists)
            XCTAssertFalse(app.staticTexts["not found"].exists)
            shot(app, "native-encryption-unavailable-" + kind)
            try await resetMock()
            notice.buttons["重新检查"].tapCenter()
            XCTAssertTrue(notice.waitForNonExistence(timeout: 10))
            XCTAssertEqual(field.value as? String, "Keep this private draft")
            XCTAssertTrue(app.buttons["lark.encryption.cover"].isEnabled)
        }
        app.buttons["lark.encryption.cover"].tapCenter()
        XCTAssertTrue(app.buttons["下周再聊"].waitForExistence(timeout: 10))
        shot(app, "native-encryption-upgrade-recovered")
        app.buttons["取消"].tapCenter()
        XCTAssertEqual(field.value as? String, "Keep this private draft")
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.98, dy: 0.8)).tap()
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: "Keep this private draft".count))
        app.buttons["lark.encryption.mode"].tapCenter(); app.buttons["lark.encryption.plain"].tapCenter()
        app.terminate()
    }

    @MainActor func testMessageEncryptionUnlockCoverAndSend() async throws {
        try await resetMock()
        let app = try open()
        let mode = app.buttons["lark.encryption.mode"]
        XCTAssertTrue(mode.waitForExistence(timeout: 8))
        mode.tapCenter(); app.buttons["lark.encryption.plain"].tapCenter()
        shot(app, "native-encryption-ordinary")
        mode.tapCenter(); app.buttons["lark.encryption.encrypted"].tapCenter()
        XCTAssertFalse(app.buttons["添加附件"].isEnabled)
        app.buttons["lark.encryption.cover"].tapCenter()
        let cover = app.buttons["下周再聊"].firstMatch
        XCTAssertTrue(cover.waitForExistence(timeout: 8))
        shot(app, "native-encryption-covers")
        cover.tapCenter()
        let field = app.textFields["lark.box"]
        field.tapCenter()
        if let value = field.value as? String, !value.hasPrefix("回复 ") { field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: value.count)) }
        field.typeText("Encrypted UI fixture")
        app.buttons["lark.send"].tapCenter()
        let password = app.secureTextFields["lark.encryption.password"]
        XCTAssertTrue(password.waitForExistence(timeout: 10))
        shot(app, "native-encryption-unlock")
        password.tapCenter(); password.typeText("wrong-synthetic")
        app.buttons["lark.encryption.unlock"].tapCenter()
        XCTAssertTrue(app.staticTexts["密码不正确，请重试"].waitForExistence(timeout: 10))
        XCTAssertEqual(password.value as? String, "密钥库密码")
        password.tapCenter(); password.typeText("synthetic-password")
        app.buttons["lark.encryption.unlock"].tapCenter()
        XCTAssertTrue(password.waitForNonExistence(timeout: 10))
        // A real password kept as one (AutoFill may fill it in), alone in its sheet and emptied on Unlock: iOS does not offer
        // to save it (#1598).
        PasswordPrompt.assertNone(app, "the Lark unlock password")
        XCTAssertTrue(app.buttons["lark.send"].waitForNonExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["Encrypted UI fixture"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForNonExistence(timeout: 15))
        shot(app, "native-encryption-sent")
        app.terminate()
    }

    @MainActor func testRetryKeepsEncryptedMessageAndCover() async throws {
        try await resetMock(locked: false, failOnce: true)
        let app = try open()
        app.buttons["lark.encryption.mode"].tapCenter(); app.buttons["lark.encryption.encrypted"].tapCenter()
        app.buttons["lark.encryption.cover"].tapCenter()
        let cover = app.buttons["午饭后再确认"].firstMatch
        XCTAssertTrue(cover.waitForExistence(timeout: 8)); cover.tapCenter()
        app.textFields["lark.box"].tapCenter(); app.textFields["lark.box"].typeText("Encrypted retry fixture")
        app.buttons["lark.send"].tapCenter()
        let failed = app.buttons["lark.outgoing.failed"]
        XCTAssertTrue(failed.waitForExistence(timeout: 15))
        shot(app, "native-encryption-retry")
        failed.tapCenter(); app.buttons["重发"].tapCenter()
        XCTAssertTrue(failed.waitForNonExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["Encrypted retry fixture"].firstMatch.waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch.waitForNonExistence(timeout: 15))
        app.terminate()
    }

    @MainActor func testEncryptedDraftRestoresAndCannotDowngradeInInbox() async throws {
        try await resetMock()
        let app = try open()
        app.buttons["lark.encryption.mode"].tapCenter(); app.buttons["lark.encryption.encrypted"].tapCenter()
        let field = app.textFields["lark.box"]
        field.tapCenter()
        if let value = field.value as? String, !value.hasPrefix("回复 ") { field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: value.count)) }
        field.typeText("Private draft fixture")
        XCUIDevice.shared.press(.home)
        XCTAssertTrue(app.wait(for: .runningBackground, timeout: 5)); app.terminate()
        let inbox = try open(inbox: true)
        XCTAssertTrue(inbox.buttons["前往消息，继续编辑加密草稿"].waitForExistence(timeout: 10))
        XCTAssertFalse(inbox.buttons["lark.encryption.mode"].exists)
        XCTAssertFalse(inbox.buttons["lark.send"].isEnabled)
        shot(inbox, "native-encryption-inbox-protected")
        inbox.buttons["前往消息，继续编辑加密草稿"].tapCenter()
        let chat = inbox.buttons["feishu-chat-f2"]
        XCTAssertTrue(chat.waitForExistence(timeout: 10)); chat.tapCenter()
        let restored = inbox
        XCTAssertTrue(restored.textFields["lark.box"].waitForExistence(timeout: 10))
        let restoredField = try XCTUnwrap(restored.textFields.matching(identifier: "lark.box").allElementsBoundByIndex.first(where: \.isHittable))
        XCTAssertEqual(restoredField.value as? String, "Private draft fixture")
        XCTAssertTrue(restored.buttons["lark.encryption.cover"].exists)
        shot(restored, "native-encryption-draft-restored")
        restoredField.tapCenter()
        restoredField.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: "Private draft fixture".count))
        restored.buttons["lark.encryption.mode"].tapCenter(); restored.buttons["lark.encryption.plain"].tapCenter()
        XCUIDevice.shared.press(.home)
        XCTAssertTrue(restored.wait(for: .runningBackground, timeout: 5)); restored.terminate()
        let plainInbox = try open(inbox: true)
        XCTAssertFalse(plainInbox.buttons["lark.encryption.mode"].exists)
        XCTAssertFalse(plainInbox.buttons["前往消息，继续编辑加密草稿"].exists)
        XCTAssertTrue(plainInbox.buttons["添加附件"].isEnabled)
        shot(plainInbox, "native-encryption-inbox-unchanged")
        plainInbox.terminate()
    }
}
