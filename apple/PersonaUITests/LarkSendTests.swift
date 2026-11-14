import XCTest

/// Sending in a Feishu chat does not wait for Feishu (docs/apple/ios.md「Lark message rendering」): the message is on screen at once
/// with a spinner beside it, Feishu's own copy takes its place, and one that did not go out has a failed mark that offers
/// Resend; replies (quoted in the chat, or in a thread sheet) go the same way. The marks are found by their identifiers
/// (lark.outgoing.sending / .failed), not their wording. Against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB), whose Feishu send takes 2 seconds (longer where a case asks it to) and fails for a text with "#fail".
final class LarkSendTests: XCTestCase {
    private var hub: String!
    private var slowedSend = false

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// A failed case leaves the screen it failed on in PERSONA_SHOTS (the attachments only record events).
    /// No draft and no failed or unsent message from an earlier run (`-showLarkSendFresh`, #1627): they are kept on
    /// disk, and an earlier run's failed messages filled the chat and its leftover draft hid the thread box's placeholder.
    static let fresh = ["-showLarkSendFresh", "YES"]

    override func tearDown() {
        if slowedSend { _ = try? mockSendDelay(nil) } // the cases after this one see the mock's own 2 s
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"], (testRun?.totalFailureCount ?? 0) > 0 {
            let name = name.components(separatedBy: " ").last?.trimmingCharacters(in: CharacterSet(charactersIn: "]")) ?? "test"
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("failed-\(name).png"))
        }
        super.tearDown()
    }

    /// GET /mock/lark_send_delay?s=… sets how long the mock's Feishu send takes from now on; no s puts its start value back.
    private func mockSendDelay(_ seconds: Double?) throws {
        let data = try Data(contentsOf: URL(string: hub + "/mock/lark_send_delay" + (seconds.map { "?s=\($0)" } ?? ""))!)
        let state = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        XCTAssertNotNil(state?["seconds"], "no /mock/lark_send_delay in this mock (an older scripts/mock/mock_hub.py?)")
    }

    /// For a case that has to see the sending spinner: after a tap XCTest waits for the app to settle, which on a busy
    /// machine took more than the mock's 2 s, and the send was done before the first look (#2578).
    private func slowSend() throws {
        try mockSendDelay(8)
        slowedSend = true
    }

    @MainActor private func expandSource(_ app: XCUIApplication) {
        let source = app.buttons["inbox.detail.source"]
        XCTAssertTrue(source.waitForExistence(timeout: 20), "the original-source control did not appear")
        source.tap()
    }

    @MainActor private func openChat() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "mention", "-inbox.source", "", "-openInboxItem", "lark:f2:200"] + Self.fresh
        app.launchPinned()
        expandSource(app)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "The new icon set")).firstMatch.waitForExistence(timeout: 20),
                      "the 设计评审 chat did not open")
        return app
    }

    @MainActor private func send(_ app: XCUIApplication, _ text: String) {
        let field = app.textFields.matching(NSPredicate(format: "placeholderValue BEGINSWITH %@", "回复")).firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText(text)
        app.buttons["发送"].tap()
    }

    /// `element` is the end of the list, whole above the box (and the quote bar over it), not under them and not left
    /// further up. The list glides there, so this waits for it a moment.
    /// The end is the list's content end, not a distance from the box: a chat opened at a message (an inbox item, the first unread)
    /// keeps its rows at the top when they fit (`LarkChatView.openingAnchor`), and without the soft keyboard (a
    /// simulator that has seen a hardware keyboard) the 「设计评审」 chat fits, its last row ~98 pt above the box (#1891).
    @MainActor private func assertAtEnd(_ app: XCUIApplication, _ element: XCUIElement, box boxID: String = "lark.box",
                                        _ what: String, file: StaticString = #filePath, line: UInt = #line) {
        // Any type: the box grows to several lines (TextField(axis: .vertical)) and is not always a text field.
        let box = app.descendants(matching: .any).matching(identifier: boxID).firstMatch
        let bar = app.descendants(matching: .any).matching(identifier: "lark.replyBar").firstMatch
        var seen = "not seen"
        let deadline = Date().addingTimeInterval(3)
        repeat {
            // Snapshots, not .frame: a sending mark goes away when the send is done, between asking and reading.
            let es = try? element.snapshot(), bs = try? box.snapshot(), whole = try? app.snapshot()
            if es == nil || bs == nil { seen = "it there: \(es != nil), the box there: \(bs != nil)" }
            if let e = es, let b = bs, let whole {
                let floor = (try? bar.snapshot())?.frame.minY ?? b.frame.minY
                let end = Self.listEnd(of: e, in: whole, floor: floor)
                seen = "it \(e.frame), the box \(b.frame), " + end.why
                if end.ok { return }
            }
            usleep(100_000)
        } while Date() < deadline
        XCTFail("\(what): \(seen)", file: file, line: line)
    }

    /// Whether `e` is the last row of its list, whole above `floor`, with the list at its end: the content ends at
    /// most `rowEnd` below `e` (its bubble, read mark and the list's padding) and not under `floor`, and either ends
    /// right above `floor` (scrolled to the end) or all of it is on screen (it fits; nothing is above to scroll back).
    @MainActor private static func listEnd(of e: XCUIElementSnapshot, in app: XCUIElementSnapshot, floor: CGFloat) -> (ok: Bool, why: String) {
        let rowEnd: CGFloat = 40, nearFloor: CGFloat = 60, rounding: CGFloat = 2
        guard floor - e.frame.maxY >= -rounding else { return (false, "under the box by \(e.frame.maxY - floor) pt") }
        guard let content = content(around: e, in: app) else { return (false, "its list not found") }
        let c = content.frame
        let why = "the list's content \(c)"
        guard c.maxY - e.frame.maxY <= rowEnd else { return (false, why + ": rows below it") }
        guard c.maxY <= floor + rounding else { return (false, why + ": ends under the box") }
        guard floor - c.maxY <= nearFloor || c.minY >= 0 else { return (false, why + ": left further up") }
        return (true, why)
    }

    /// The content of the scroll view that holds `e`: its child that `e` is in, on the way down the tree. Not the tallest
    /// child whose frame holds `e`'s: the chat's scroll view also has two children as big as the window, and the content
    /// seemed to end 414 pt under the last message (#6044).
    @MainActor private static func content(around e: XCUIElementSnapshot, in node: XCUIElementSnapshot,
                                           under top: XCUIElementSnapshot? = nil) -> XCUIElementSnapshot? {
        if node.elementType == e.elementType, node.frame == e.frame, node.label == e.label { return top }
        for child in node.children {
            if let found = content(around: e, in: child, under: node.elementType == .scrollView ? child : top) { return found }
        }
        return nil
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor func testMessageShowsAtOnceThenItsCopyTakesOver() throws {
        try slowSend()
        let app = openChat()
        let text = "下午三点前我看一下 " + UUID().uuidString.prefix(8)
        send(app, text)
        let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch
        XCTAssertTrue(sending.waitForExistence(timeout: 1), "the message is not on screen while it goes")
        XCTAssertTrue(app.staticTexts[text].exists)
        // The spinner's own place beside the bubble is testSendMarksSitBesideTheBubble's; here the message's.
        assertAtEnd(app, app.staticTexts[text], "the sending message shows right above the box")
        let field = app.textFields.matching(NSPredicate(format: "placeholderValue BEGINSWITH %@", "回复")).firstMatch
        XCTAssertNotEqual(field.value as? String, text, "the box is cleared at once")
        shot(app, "lark-sending")
        XCTAssertTrue(sending.waitForNonExistence(timeout: 15), "it never went out")
        sleep(3) // the copy replaces it: one message with the text, not two
        shot(app, "lark-sent")
        let copies = app.staticTexts.matching(NSPredicate(format: "label == %@", text))
        XCTAssertEqual(copies.count, 1, copies.debugDescription)
    }

    /// The last message stays whole above the box as the keyboard comes up and as the quote bar comes up
    /// (#672: the keyboard and the quote bar used to cover it).
    /// In the chat with 「韩梅梅」, which no test sends to: its last message stays the mock's.
    @MainActor func testLastMessageStaysAboveKeyboardAndQuoteBar() {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f1:100"] + Self.fresh
        app.launchPinned()
        expandSource(app)
        XCTAssertTrue(app.staticTexts["能赶上，下午三点前给你确认"].waitForExistence(timeout: 20), "the 韩梅梅 chat did not open")
        // Scope each lookup by a visible message instead of choosing among every scroll view: the keyboard can change
        // which one is first. Re-resolve the list after the swipe so the older anchor may leave the lazy hierarchy;
        // querying the last row through its list also excludes the quote bar's summary.
        let list = app.scrollViews.containing(.staticText, identifier: "能赶上，下午三点前给你确认").firstMatch
        list.swipeUp() // at the end, as after reading the unread ones
        sleep(1)
        let currentList = app.scrollViews.containing(.staticText, identifier: "文档我晚上看").firstMatch
        let last = currentList.staticTexts["新包发你了"].firstMatch
        XCTAssertTrue(last.waitForExistence(timeout: 5), "the chat's last message is there")
        let field = app.textFields["lark.box"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        // Typing brings the soft keyboard up (a tap alone can leave it off screen); no text is left as a draft.
        field.typeText("好" + XCUIKeyboardKey.delete.rawValue)
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "the keyboard comes up")
        sleep(1) // it slides up
        XCTAssertLessThan(keyboard.frame.minY, app.frame.maxY - 100, "the keyboard is on screen")
        assertAtEnd(app, last, "with the keyboard up the last message shows above the box")
        shot(app, "lark-keyboard-last")
        last.press(forDuration: 1.2)
        let reply = app.buttons["回复"]
        XCTAssertTrue(reply.waitForExistence(timeout: 3), "the long-press menu has 回复")
        reply.tap()
        XCTAssertTrue(app.staticTexts["回复 我"].waitForExistence(timeout: 3), "the quote bar is up")
        sleep(1) // and the keyboard with it
        let quotedList = app.scrollViews.containing(.staticText, identifier: "文档我晚上看").firstMatch
        let quotedLast = quotedList.staticTexts["新包发你了"].firstMatch
        shot(app, "lark-keyboard-quote")
        assertAtEnd(app, quotedLast, "the quote bar does not cover the last message")
        // The quote stays as the chat's draft: the next run quotes the same message. (The bar going away as the
        // reply is sent: testQuotedReplyShowsTheQuoteWhileItGoes.)
    }

    /// The failed mark beside `text`'s bubble, on its line: failed messages are kept (LarkOutboxStore, #713), so earlier
    /// runs' failed ones may be on screen too and the mark alone is not one element.
    @MainActor private func failedMark(_ app: XCUIApplication, beside text: String, timeout: TimeInterval = 10) -> XCUIElement? {
        let bubble = app.staticTexts[text]
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if bubble.exists {
                let marks = app.buttons.matching(identifier: "lark.outgoing.failed").allElementsBoundByIndex
                if let mark = marks.min(by: { abs($0.frame.midY - bubble.frame.midY) < abs($1.frame.midY - bubble.frame.midY) }),
                   abs(mark.frame.midY - bubble.frame.midY) < 12 { return mark }
            }
            usleep(200_000)
        } while Date() < deadline
        return nil
    }

    @MainActor func testFailedMessageOffersRetry() {
        let app = openChat()
        let text = "#fail 这条发不出去 " + UUID().uuidString.prefix(6)
        send(app, text)
        guard let failed = failedMark(app, beside: text) else { return XCTFail("a failed send says so") }
        shot(app, "lark-failed")
        failed.tap()
        XCTAssertTrue(app.buttons["重发"].waitForExistence(timeout: 3))
        XCTAssertTrue(app.buttons["放回输入框"].exists)
        app.buttons["删除"].tap()
        XCTAssertTrue(app.staticTexts[text].waitForNonExistence(timeout: 3), "删除 takes the failed message away")
    }

    /// The send marks sit beside the bubble, like Feishu: the failed mark left of the failed message on its line, and
    /// spinners while later ones go. Two more messages push the failed one up out from under the box for the shot.
    @MainActor func testSendMarksSitBesideTheBubble() throws {
        let app = openChat()
        let failedText = "#fail 旁边是标记 " + UUID().uuidString.prefix(6)
        send(app, failedText)
        guard let failed = failedMark(app, beside: failedText) else { return XCTFail("a failed send has its mark") }
        let bubble = app.staticTexts[failedText]
        XCTAssertTrue(bubble.exists)
        XCTAssertLessThanOrEqual(failed.frame.maxX, bubble.frame.minX, "the mark is left of the bubble")
        XCTAssertLessThan(abs(failed.frame.midY - bubble.frame.midY), 12, "on the bubble's line, not under it")
        try slowSend() // after the failed one: it only has to fail
        send(app, "第二条 " + UUID().uuidString.prefix(6))
        send(app, "第三条 " + UUID().uuidString.prefix(6))
        let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending")
        XCTAssertTrue(sending.firstMatch.waitForExistence(timeout: 2), "the ones going have spinners")
        shot(app, "lark-marks")
        XCTAssertTrue(failed.exists, "the failed one stays until it is dealt with")
        // Deal with it: the next runs should not find this one.
        failed.tap()
        if app.buttons["删除"].waitForExistence(timeout: 3) { app.buttons["删除"].tap() }
    }

    /// One that failed stays where it was written: a message sent after it goes out and its copy comes in under it,
    /// not over it (#921).
    @MainActor func testALaterMessageGoesUnderOneThatFailed() {
        let app = openChat()
        let tag = UUID().uuidString.prefix(6)
        let failedText = "#fail 先发的 \(tag)", laterText = "后发的 \(tag)"
        send(app, failedText)
        XCTAssertTrue(app.buttons["lark.outgoing.failed"].waitForExistence(timeout: 15), "the first one failed")
        send(app, laterText)
        let later = app.staticTexts.matching(NSPredicate(format: "label == %@", laterText))
        let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch
        XCTAssertTrue(sending.waitForNonExistence(timeout: 15), "the second one went out")
        let settled = NSPredicate { _, _ in later.count == 1 }
        wait(for: [XCTNSPredicateExpectation(predicate: settled, object: nil)], timeout: 25) // its copy took its place
        let failed = app.staticTexts[failedText]
        XCTAssertTrue(failed.exists && app.buttons["lark.outgoing.failed"].exists, "the failed one is still there")
        XCTAssertLessThan(failed.frame.minY, later.firstMatch.frame.minY, "the later message is under the failed one")
        shot(app, "lark-failed-then-sent")
    }

    /// Long press: a bar of the quick reactions on top of the menu (six across), one tap reacts; the menu's own items
    /// are still there. The mock's quick list is THUMBSUP HEART SMILE LAUGH SURPRISED CRY OK DONE.
    @MainActor func testLongPressBarReactsInOneTap() {
        let app = openChat()
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "我先看了一下")).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 5))
        sleep(2) // the emoji catalogue and the quick list come in after the chat opens
        target.press(forDuration: 1.2)
        let heart = app.buttons["爱心"]
        XCTAssertTrue(heart.waitForExistence(timeout: 3), "the bar is in the long-press menu")
        for label in ["赞", "微笑", "大笑", "惊讶", "流泪"] {
            XCTAssertTrue(app.buttons[label].exists, "\(label) is in the bar")
        }
        XCTAssertFalse(app.buttons["OK"].exists, "six in the bar, not seven")
        XCTAssertTrue(app.buttons["回复"].exists && app.buttons["表情回复"].exists && app.buttons["拷贝"].exists,
                      "the menu's items are still there")
        shot(app, "lark-quick-bar")
        heart.tap()
        sleep(1)
        shot(app, "lark-quick-reacted")
        XCTAssertTrue(heart.waitForNonExistence(timeout: 3), "the menu closes")
        // The chip under the message: its emoji's label and the count (LarkReactionChip).
        let chip = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "爱心")).firstMatch
        XCTAssertTrue(chip.waitForExistence(timeout: 3), "one tap reacts: the message has the reaction\n\(app.debugDescription)")
    }

    /// Reply in a message's long-press menu: a quote bar over the box, and the message goes out quoting it.
    @MainActor func testQuotedReplyShowsTheQuoteWhileItGoes() {
        let app = openChat()
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "我先看了一下")).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 5))
        target.press(forDuration: 1.2)
        let reply = app.buttons["回复"]
        XCTAssertTrue(reply.waitForExistence(timeout: 3), "the long-press menu has 回复")
        reply.tap()
        let bar = app.staticTexts["回复 李雷"]
        XCTAssertTrue(bar.waitForExistence(timeout: 3), "the quote bar names who is answered")
        XCTAssertTrue(app.buttons["取消回复"].exists)
        shot(app, "lark-reply-bar")
        let text = "我来改 " + UUID().uuidString.prefix(8)
        send(app, text)
        // The mock takes 2 s to send: what shows within a second is the local row, with its spinner.
        let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending").firstMatch
        XCTAssertTrue(sending.waitForExistence(timeout: 2), "the reply is not on screen while it goes") // the sheet's own animation first
        XCTAssertTrue(app.staticTexts[text].exists)
        XCTAssertTrue(bar.waitForNonExistence(timeout: 2), "the quote bar goes with the message")
        assertAtEnd(app, app.staticTexts[text], "the sending reply shows right above the box")
        // The quote at the top of the bubble is a button (it goes back to the message, #986), its label who and what.
        let quotes = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "李雷：我先看了一下"))
        XCTAssertTrue(quotes.firstMatch.exists, "the sending row quotes the message")
        shot(app, "lark-reply-sending")
        sleep(6) // sent, and the copy replaces it: one reply, quoting the message
        let copies = app.staticTexts.matching(NSPredicate(format: "label == %@", text))
        XCTAssertEqual(copies.count, 1, copies.debugDescription)
        XCTAssertTrue(quotes.firstMatch.exists, "the copy quotes the message too")
    }

    /// 「周末徒步群」, whose box only the draft and send-button tests type into: a draft a failed run left there stays out
    /// of the other tests' way. That draft goes out first (to the mock), so the box starts empty, unless the test
    /// opens the chat to find its own draft.
    @MainActor private func openHikingChat(sendingLeftover: Bool = true, fresh: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f3:300"] + (fresh ? Self.fresh : [])
        app.launchPinned()
        expandSource(app)
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "那我也带一个")).firstMatch
        XCTAssertTrue(target.waitForExistence(timeout: 20), "the 周末徒步群 chat did not open")
        if sendingLeftover, app.buttons["lark.send"].exists { app.buttons["lark.send"].tap() }
        return app
    }

    /// Like Feishu, the send button is only there with something to send; the empty box takes the whole row.
    @MainActor func testSendButtonComesWithWords() {
        let app = openHikingChat()
        let send = app.buttons["lark.send"]
        let box = app.textFields["lark.box"]
        XCTAssertTrue(box.waitForExistence(timeout: 5))
        XCTAssertTrue(send.waitForNonExistence(timeout: 3), "no send button by an empty box")
        shot(app, "lark-box-empty")
        box.tap()
        box.typeText("  ")
        XCTAssertFalse(send.exists, "spaces alone are nothing to send")
        box.typeText("周六见")
        XCTAssertTrue(send.waitForExistence(timeout: 2), "the send button comes with the words")
        shot(app, "lark-box-words")
        box.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 5))
        XCTAssertTrue(send.waitForNonExistence(timeout: 2), "and goes with them")
    }

    /// @ in a group's box lists the members; picking one writes `@name ` and the message goes out mentioning them
    /// (the mock keeps the mention as an at part with their user id, as Feishu does).
    @MainActor func testAtPicksSomeoneToMention() throws {
        let app = openHikingChat()
        let box = app.textFields["lark.box"]
        XCTAssertTrue(box.waitForExistence(timeout: 5))
        box.tap()
        box.typeText("@")
        let picker = app.descendants(matching: .any)["lark.mentionPicker"] // a scroll view once the list is long
        XCTAssertTrue(picker.waitForExistence(timeout: 5), "@ lists the members")
        XCTAssertTrue(app.buttons["@所有人"].exists, "所有人 comes first")
        shot(app, "lark-mention-picker")
        box.typeText("M")
        let mia = app.buttons["@Mia"]
        XCTAssertTrue(mia.waitForExistence(timeout: 5), "the lookup narrows to Mia")
        mia.tap()
        XCTAssertEqual(app.textFields["lark.box"].value as? String, "@Mia ", "picking writes the name and a space")
        XCTAssertTrue(picker.waitForNonExistence(timeout: 3))
        let words = "周六见 " + UUID().uuidString.prefix(6)
        app.textFields["lark.box"].typeText(words)
        app.buttons["lark.send"].tap()
        sleep(6) // the mock takes 2 s to send, then the copy comes in
        shot(app, "lark-mention-sent")

        // What the mock got: the message with an at part for Mia's user id.
        var request = URLRequest(url: URL(string: hub + "/api/v1/lark/conversations/f3/messages")!)
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let done = expectation(description: "messages")
        var body = Data()
        URLSession.shared.dataTask(with: request) { data, _, _ in body = data ?? Data(); done.fulfill() }.resume()
        wait(for: [done], timeout: 10)
        let messages = (try JSONSerialization.jsonObject(with: body) as? [String: Any])?["messages"] as? [[String: Any]] ?? []
        let sent = try XCTUnwrap(messages.last { ($0["text"] as? String) == "@Mia " + words }, "the message reached the mock")
        let parts = sent["parts"] as? [[String: Any]] ?? []
        XCTAssertTrue(parts.contains { $0["kind"] as? String == "at" && $0["refId"] as? String == "u-Mia" }, "\(parts)")
    }

    /// @ typed where the cursor is, not at the end: the picker comes up there and the name goes in there.
    @MainActor func testAtWhereTheCursorIsPicksSomeone() {
        let app = openHikingChat()
        let box = app.textFields["lark.box"]
        XCTAssertTrue(box.waitForExistence(timeout: 5))
        box.tap()
        box.typeText("一起去")
        // The cursor into the text, then @ there. A long press puts the cursor where it is (a tap snaps to the word's
        // end, and 「一起去」 is one word: the @ went in after it, #1627); where exactly is the text's layout, so the
        // checks follow where the @ went in.
        // Right after typing the field does not always take a press as moving the cursor (the app saw no new cursor
        // at all, #1627): wait a moment, and press again when the @ still went in at the end.
        var typed = ""
        for attempt in 0..<3 {
            sleep(2)
            box.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5)).press(forDuration: 0.8)
            sleep(1) // the loupe goes away
            box.typeText("@")
            typed = box.value as? String ?? ""
            if !typed.hasSuffix("@") || attempt == 2 { break }
            box.typeText(XCUIKeyboardKey.delete.rawValue)
        }
        XCTAssertEqual(typed.count, 4)
        XCTAssertFalse(typed.hasSuffix("@"), "the @ went in at the cursor, not at the end: \(typed)")
        let at = typed.firstIndex(of: "@").map { typed.distance(from: typed.startIndex, to: $0) } ?? 0
        func spliced(_ insert: String) -> String {
            var t = typed
            t.replaceSubrange(t.index(t.startIndex, offsetBy: at)...t.index(t.startIndex, offsetBy: at), with: insert)
            return t
        }
        let mia = app.buttons["@Mia"]
        XCTAssertTrue(mia.waitForExistence(timeout: 5), "@ before other words still lists the members")
        shot(app, "lark-mention-cursor-picker")
        mia.tap()
        XCTAssertEqual(box.value as? String, spliced("@Mia "), "the name goes in at the cursor, not at the end")
        box.typeText("周六")
        XCTAssertEqual(box.value as? String, spliced("@Mia 周六"), "and the cursor is right after it")
        shot(app, "lark-mention-cursor-picked")
        // Leave the box empty for the next run.
        tapAfterTyping(app.buttons["lark.send"])
        XCTAssertTrue(app.textFields.matching(NSPredicate(format: "value == %@", spliced("@Mia 周六"))).firstMatch.waitForNonExistence(timeout: 3))
        sleep(3)
    }

    /// What is typed and not sent is still in the box, quote and all, after the app is gone and back (LarkDrafts).
    @MainActor func testDraftOutlivesTheApp() {
        var app = openHikingChat()
        let target = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "那我也带一个")).firstMatch
        target.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["回复"].waitForExistence(timeout: 3), "the long-press menu has 回复")
        app.buttons["回复"].tap()
        XCTAssertTrue(app.staticTexts["回复 Mia"].waitForExistence(timeout: 3))
        let text = "草稿" + UUID().uuidString.prefix(8)
        let box = app.textFields.matching(NSPredicate(format: "placeholderValue BEGINSWITH %@", "回复")).firstMatch
        XCTAssertTrue(box.waitForExistence(timeout: 5))
        box.typeText(text) // Reply focused it
        sleep(3) // written a moment after the last keystroke
        app.terminate()

        app = openHikingChat(sendingLeftover: false, fresh: false) // the draft kept on disk is the point
        let back = app.textFields.matching(NSPredicate(format: "value == %@", text)).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 20), "the draft is back in the box")
        XCTAssertTrue(app.staticTexts["回复 Mia"].exists, "the quote comes back with it")
        shot(app, "lark-draft-back")

        // Sending it empties the box and drops the draft (the next run starts from an empty box).
        app.buttons["lark.send"].tap()
        XCTAssertTrue(back.waitForNonExistence(timeout: 3), "the box is cleared when the draft is sent")
        XCTAssertTrue(app.staticTexts["回复 Mia"].waitForNonExistence(timeout: 3))
        sleep(3)
    }

    /// Three messages sent one after another each go out in about the send's own time (the mock's 2 s), in the order
    /// written: none waits behind the one before for its copy (#851: the second went out after 24 s, the third after
    /// more than 30). In the chat and in a thread sheet.
    @MainActor func testMessagesSentInARowDoNotWaitForEachOther() {
        let app = openChat()
        let tag = String(UUID().uuidString.prefix(6))
        let failedBefore = failed(app)
        for i in 1...3 { send(app, "连发第 \(i) 条 " + tag) }
        assertAllOut(app, sent: (1...3).map { "连发第 \($0) 条 " + tag }, within: 12, failedBefore: failedBefore, "in the chat")
        let thread = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        thread.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f3:300"] + Self.fresh
        thread.launchPinned()
        expandSource(thread)
        let open = thread.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "条回复")).firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 20), "the 周末徒步群 chat did not open at its thread")
        open.tap()
        let field = thread.textFields.matching(NSPredicate(format: "placeholderValue == %@", "回复话题")).firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "the thread sheet has a box")
        XCTAssertTrue(thread.staticTexts["60 条回复"].waitForExistence(timeout: 10), "the thread's latest page did not load")
        let threadFailedBefore = failed(thread)
        let box = thread.textFields["lark.thread.box"] // the placeholder goes once it holds text
        for i in 1...3 {
            box.tap()
            box.typeText("话题连发第 \(i) 条 " + tag)
            tapAfterTyping(thread.buttons["lark.thread.send"])
        }
        assertAllOut(thread, sent: (1...3).map { "话题连发第 \($0) 条 " + tag }, within: 12, failedBefore: threadFailedBefore, "in the thread sheet")
    }

    /// A tap right after typeText is sometimes lost: the button never sees it (#1627). Give the field a moment.
    @MainActor private func tapAfterTyping(_ button: XCUIElement) {
        sleep(1)
        button.tap()
    }

    /// Failed marks on screen (the outbox keeps failed messages of earlier runs).
    @MainActor private func failed(_ app: XCUIApplication) -> Int {
        app.descendants(matching: .any).matching(identifier: "lark.outgoing.failed").count
    }

    /// No message is still going `within` seconds of the last send (three in a row at 2 s each: about 6), none failed.
    @MainActor private func assertAllOut(_ app: XCUIApplication, sent: [String], within seconds: TimeInterval, failedBefore: Int, _ where_: String,
                                         file: StaticString = #filePath, line: UInt = #line) {
        let sending = app.descendants(matching: .any).matching(identifier: "lark.outgoing.sending")
        // That they went: their bubbles (a mini ProgressView is not always in XCUITest's tree, #1627).
        for text in sent {
            XCTAssertTrue(app.staticTexts[text].waitForExistence(timeout: 2), "\(where_): \(text) is not on screen", file: file, line: line)
        }
        let start = Date()
        while sending.count > 0, Date().timeIntervalSince(start) < seconds { usleep(250_000) }
        XCTAssertEqual(sending.count, 0, "\(where_): still going \(Int(Date().timeIntervalSince(start))) s after the last send", file: file, line: line)
        XCTAssertEqual(failed(app), failedBefore, "\(where_): one failed",
                       file: file, line: line)
    }

    /// The thread sheet has its own box: a reply there goes into the thread, shows at once, and its copy takes over.
    @MainActor func testThreadSheetRepliesInTheThread() {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", "lark:f3:300"] + Self.fresh
        app.launchPinned()
        expandSource(app)
        let thread = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "条回复")).firstMatch
        XCTAssertTrue(thread.waitForExistence(timeout: 20), "the 周末徒步群 chat did not open at its thread")
        thread.tap()
        let field = app.textFields.matching(NSPredicate(format: "placeholderValue == %@", "回复话题")).firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "the thread sheet has a box")
        XCTAssertTrue(app.staticTexts["60 条回复"].waitForExistence(timeout: 10), "the thread's latest page did not load")
        let text = "我也报名坐地铁 " + UUID().uuidString.prefix(8) // no full-width comma: typing it dropped the characters before it
        field.tap()
        field.typeText(text)
        let box = app.textFields["lark.thread.box"]
        XCTAssertEqual(box.value as? String, text, "the box holds what was typed")
        shot(app, "lark-thread-box")
        tapAfterTyping(app.buttons["lark.thread.send"])
        // The reply itself, not its spinner: a mini ProgressView is not always in XCUITest's tree (#1627).
        XCTAssertTrue(app.staticTexts[text].waitForExistence(timeout: 1), "the reply is not on screen while it goes")
        XCTAssertTrue(["", "回复话题"].contains(box.value as? String ?? ""), "the box empties when the reply goes") // an empty field reports its placeholder, or not
        assertAtEnd(app, app.staticTexts[text], box: "lark.thread.box", "the sending reply shows right above the thread's box")
        shot(app, "lark-thread-sending")
        sleep(6) // sent, and the newest page brings its copy in its place
        XCTAssertEqual(app.staticTexts.matching(NSPredicate(format: "label == %@", text)).count, 1)
        shot(app, "lark-thread-sent")
    }
}

