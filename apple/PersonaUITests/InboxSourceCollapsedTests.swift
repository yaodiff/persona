import XCTest

/// A non-event native source stays collapsed when its inbox detail opens. Only View original constructs the source and
/// lets it report what was seen. Run against scripts/mock/mock_hub.py without MOCK_INBOX_EVENTS (PERSONA_MOCK_HUB).
/// The mail test enforces the default mock fixture (no MOCK_INBOX_EVENTS or MOCK_MAIL) by checking UID 501's body first.
final class InboxSourceCollapsedTests: XCTestCase {
    private static let chatItem = "lark:f1:100"
    private static let chatFeed = "f1"
    private static let mailItem = "mail:acc1:t1:501"
    private static let mailAccount = "acc1"
    private static let mailUID = 501

    private var app: XCUIApplication!
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py without MOCK_INBOX_EVENTS (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "inbox-source-collapsed-failure"
            shot.lifetime = .keepAlways
            add(shot)
        }
        app?.terminate()
        super.tearDown()
    }

    @MainActor func testChatDetailReportsReadOnlyAfterViewOriginal() throws {
        try post("/api/v1/inbox/done", body: ["ids": [Self.chatItem], "done": false])
        let baseline = try chatReadReports().count
        openDetail(Self.chatItem)
        let toggle = sourceToggle()
        requireHittable(toggle, "View original on the collapsed chat detail")
        assertLabel("看原文", on: toggle, "the collapsed chat source toggle")
        XCTAssertFalse(app.textFields["lark.box"].exists, "the chat reader was built before View original")

        sleep(2)
        XCTAssertEqual(try chatReadReports().count, baseline,
                       "opening the chat detail reported a read before View original")

        toggle.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 10),
                      "View original did not build the chat reader:\n\(app.debugDescription)")
        let reports = try waitForReports(after: baseline, fetch: chatReadReports)
        XCTAssertGreaterThan(reports.count, baseline, "View original did not report the \(Self.chatFeed) chat as read")
        assertLabel("收起", on: toggle, "the expanded chat source toggle")
    }

    @MainActor func testMailDetailReportsSeenOnlyAfterViewOriginal() throws {
        let messageURL = try XCTUnwrap(URL(string: hub + "/api/v1/mail/messages/\(Self.mailAccount)/\(Self.mailUID)?mailbox=INBOX"))
        let messageData = try Data(contentsOf: messageURL)
        let response = try XCTUnwrap(JSONSerialization.jsonObject(with: messageData) as? [String: Any])
        let message = try XCTUnwrap(response["message"] as? [String: Any])
        guard let text = message["text"] as? String, text.contains("议程见附件，有问题随时说") else {
            throw XCTSkip("requires the default mock fixture without MOCK_INBOX_EVENTS or MOCK_MAIL; MOCK_MAIL=1 replaces mail UID 501's body")
        }
        try resetMailFixture()
        let baseline = try mailSeenReports().count
        openDetail(Self.mailItem)
        let toggle = sourceToggle()
        requireHittable(toggle, "View original on the collapsed mail detail")
        assertLabel("看原文", on: toggle, "the collapsed mail source toggle")
        let body = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "议程见附件，有问题随时说")).firstMatch
        XCTAssertFalse(body.exists, "the mail reader was built before View original")

        sleep(2)
        XCTAssertEqual(try mailSeenReports().count, baseline,
                       "opening the mail detail reported seen before View original")

        toggle.tap()
        XCTAssertTrue(body.waitForExistence(timeout: 10), "View original did not load the mail body:\n\(app.debugDescription)")
        let reports = try waitForReports(after: baseline, fetch: mailSeenReports)
        XCTAssertGreaterThan(reports.count, baseline,
                             "View original did not report mail UID \(Self.mailUID) as seen")
        assertLabel("收起", on: toggle, "the expanded mail source toggle")
    }

    @MainActor private func openDetail(_ item: String) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-dev.openChat", "", "-openSplitFresh", "YES",
                               "-inbox.kind", "", "-inbox.source", "", "-openInboxItem", item]
        app.launchPinned()
        acceptIconAlert()
        requireHittable(element("inbox.detail.done"), "the \(item) detail")
    }

    @MainActor private func sourceToggle() -> XCUIElement {
        element("inbox.detail.source")
    }

    @MainActor private func element(_ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    @MainActor private func requireHittable(_ element: XCUIElement, _ what: String, timeout: TimeInterval = 10) {
        for _ in 0..<6 where !(element.exists && element.isHittable) {
            app.swipeUp(velocity: .slow)
            _ = element.waitForExistence(timeout: 1)
        }
        let predicate = NSPredicate(format: "exists == true AND hittable == true")
        let expectation = XCTNSPredicateExpectation(predicate: predicate, object: element)
        let met = XCTWaiter().wait(for: [expectation], timeout: timeout) == .completed
        XCTAssertTrue(met, "\(what) is not on screen:\n\(app.debugDescription)")
    }

    @MainActor private func assertLabel(_ label: String, on element: XCUIElement, _ what: String,
                                        timeout: TimeInterval = 5) {
        // VoiceOver reads the source preview as well as the action.
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label CONTAINS %@", label), object: element)
        XCTAssertEqual(XCTWaiter().wait(for: [expectation], timeout: timeout), .completed,
                       "\(what) does not say \(label):\n\(app.debugDescription)")
    }

    /// Only f1's reports, not every chat the shared mock has served.
    private func chatReadReports() throws -> [[String: Any]] {
        try records("/mock/lark_reads", key: "reads").filter { $0["feed"] as? String == Self.chatFeed }
    }

    /// Restores the fixture through existing APIs so earlier readers and Done actions cannot suppress the seen write.
    private func resetMailFixture() throws {
        try post("/api/v1/inbox/done", body: ["ids": [Self.mailItem], "done": false])
        try post("/api/v1/mail/messages/\(Self.mailAccount)/\(Self.mailUID)/flags", body: ["mailbox": "INBOX", "seen": false])
    }

    private func post(_ path: String, body: [String: Any]) throws {
        let url = try XCTUnwrap(URL(string: hub + path))
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: "POST " + path)
        URLSession.shared.dataTask(with: request) { _, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
    }

    /// The mail area records flags here; its earlier route preempts the separate `/mock/mail_flags` writer.
    /// Only this message's seen writes; the unseen setup write, flag writes and other accounts or UIDs do not affect it.
    private func mailSeenReports() throws -> [[String: Any]] {
        try records("/mock/mail", key: "flags").filter {
            $0["account_id"] as? String == Self.mailAccount
                && ($0["uid"] as? NSNumber)?.intValue == Self.mailUID
                && $0["mailbox"] as? String == "INBOX"
                && $0["seen"] as? Bool == true
        }
    }

    private func records(_ path: String, key: String) throws -> [[String: Any]] {
        let url = try XCTUnwrap(URL(string: hub + path))
        let data = try Data(contentsOf: url)
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        return try XCTUnwrap(object[key] as? [[String: Any]])
    }

    private func waitForReports(after baseline: Int, timeout: TimeInterval = 10,
                                fetch: () throws -> [[String: Any]]) throws -> [[String: Any]] {
        let deadline = Date.now.addingTimeInterval(timeout)
        var current = try fetch()
        while current.count <= baseline, Date.now < deadline {
            usleep(250_000)
            current = try fetch()
        }
        return current
    }
}
