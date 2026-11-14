import XCTest

/// A non-event inbox item's source (decisions line 13a): opening the detail builds neither `LarkChatView` nor the mail
/// reader, so nothing is reported read; View original builds it and reports what is on screen, and Collapse takes it
/// away again. A reused detail column starts collapsed for the next item. Run against scripts/mock/mock_hub.py in its
/// default shape: without MOCK_INBOX_EVENTS (those items are events) and without MOCK_MAIL (which gives UID 501 a
/// different body); the mail case checks its fixture and skips rather than waiting for text that cannot come.
///
/// The reused-column case needs its own hub with one assistant (`MOCK_MANAGERS=0`, a hub from before the roster):
///   PORT=<port> MOCK_MANAGERS=0 python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_INBOX_HUB=http://127.0.0.1:<port>
/// and skips without it: on the default mock the second item belongs to another assistant, which the Inbox does not list.
///
/// The chat's read reports are read from `/mock/lark_reads`; the mail's seen writes from `GET /mock/mail`'s flags, where
/// the mock keeps them (`/mock/mail_flags` is filled by a handler the mail area's own answer always pre-empts, so it
/// stays empty however often the app writes).
final class InboxCollapsedSourceTests: XCTestCase {
    private static let chatItem = "lark:f1:100"
    private static let chatTitle = "韩梅梅"
    private static let chatFeed = "f1"
    private static let otherItem = "lark:f3:300"
    private static let otherTitle = "周末徒步群"
    private static let mailItem = "mail:acc1:t1:501"
    private static let mailAccount = "acc1"
    private static let mailUID = 501
    /// The mail's own words, past its preview: only the reader draws them.
    private static let mailBody = "议程见附件，有问题随时说"

    private var app: XCUIApplication!
    private var hub: String!
    private var regularWidth = false
    private var layout: String { regularWidth ? "regular" : "phone" }

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
            shot.name = "inbox-collapsed-source-failure-\(layout)"
            shot.lifetime = .keepAlways
            add(shot)
        }
        app?.terminate()
        super.tearDown()
    }

    @MainActor func testChatIsBuiltOnlyAfterViewOriginal() throws {
        try resetFixture(Self.chatItem)
        let baseline = try chatReadReports().count
        openDetail(Self.chatItem)
        let toggle = element("inbox.detail.source")
        requireHittable(toggle, "View original on the collapsed chat")
        assertLabel("看原文", on: toggle, "the collapsed chat source toggle")
        XCTAssertFalse(app.textFields["lark.box"].exists,
                       "the chat was built before View original:\n\(app.debugDescription)")

        sleep(2) // the chat reports a read 400 ms after its rows come on screen
        XCTAssertEqual(try chatReadReports().count, baseline, "opening the chat detail reported a read before View original")

        toggle.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 15),
                      "View original did not build the chat:\n\(app.debugDescription)")
        assertLabel("收起", on: toggle, "the expanded chat source toggle")
        XCTAssertGreaterThan(try waitForReports(after: baseline, fetch: chatReadReports).count, baseline,
                             "View original did not report the \(Self.chatFeed) chat as read")

        toggle.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForNonExistence(timeout: 5), "Collapse left the chat on the page")
        assertLabel("看原文", on: toggle, "the collapsed chat source toggle again")
    }

    @MainActor func testMailIsBuiltOnlyAfterViewOriginal() throws {
        try requireDefaultMailFixture()
        try resetFixture(Self.mailItem)
        let baseline = try mailSeenReports().count
        openDetail(Self.mailItem)
        let toggle = element("inbox.detail.source")
        requireHittable(toggle, "View original on the collapsed mail")
        assertLabel("看原文", on: toggle, "the collapsed mail source toggle")
        XCTAssertFalse(mailBodyElement().exists, "the mail reader was built before View original:\n\(app.debugDescription)")

        sleep(2)
        XCTAssertEqual(try mailSeenReports().count, baseline, "opening the mail detail reported it seen before View original")

        toggle.tap()
        XCTAssertTrue(mailBodyElement().waitForExistence(timeout: 15),
                      "View original did not build the mail reader:\n\(app.debugDescription)")
        XCTAssertGreaterThan(try waitForReports(after: baseline, fetch: mailSeenReports).count, baseline,
                             "View original did not report mail UID \(Self.mailUID) as seen")
        assertLabel("收起", on: toggle, "the expanded mail source toggle")

        toggle.tap()
        XCTAssertTrue(mailBodyElement().waitForNonExistence(timeout: 5), "Collapse left the mail reader on the page")
    }

    /// Collapsed, the page is still the chat's page: it names the chat in its bar, as the chat itself did before it was
    /// behind View original.
    @MainActor func testCollapsedChatDetailStillNamesTheChat() throws {
        try resetFixture(Self.chatItem)
        openDetail(Self.chatItem)
        XCTAssertTrue(app.navigationBars[Self.chatTitle].firstMatch.waitForExistence(timeout: 10),
                      "the collapsed chat detail does not name the chat:\n\(app.debugDescription)")
        XCTAssertFalse(app.textFields["lark.box"].exists, "the chat was built for its title")
    }

    /// Regular width keeps one detail column for every item: an expanded source must not stay open for the next item.
    /// It goes the way a person does: the Inbox opened from the Assistant sidebar, an item picked from its list, View
    /// original, Back to that same list, then the next item. Needs its own hub, `MOCK_MANAGERS=0` (see the class): the
    /// default mock puts the second item under another assistant, which the Inbox does not list.
    @MainActor func testAnotherItemOpensCollapsed() throws {
        let inboxHub = try singleAssistantHub()
        try resetFixture(Self.chatItem, on: inboxHub)
        launchInbox(inboxHub)
        try requireWide()
        guard let first = listRow(Self.chatTitle) else {
            return XCTFail("no \(Self.chatTitle) row in the Inbox:\n\(app.debugDescription)")
        }
        first.tapCenter()
        let toggle = element("inbox.detail.source")
        requireHittable(toggle, "View original on the collapsed chat")
        toggle.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 15), "View original did not build the chat")

        backToInbox()
        guard let nextRow = listRow(Self.otherTitle) else {
            return XCTFail("no \(Self.otherTitle) row in the Inbox after Back:\n\(app.debugDescription)")
        }
        nextRow.tapCenter()
        let next = element("inbox.detail.source")
        requireHittable(next, "\(Self.otherTitle)'s source toggle")
        assertLabel("看原文", on: next, "the next item's source toggle")
        XCTAssertFalse(app.textFields["lark.box"].exists, "the previous chat stayed open on the next item")
        // The detail's own bar names the item open: the list it came from is behind the page, not beside it.
        XCTAssertTrue(app.navigationBars[Self.otherTitle].firstMatch.waitForExistence(timeout: 10),
                      "the detail did not change to \(Self.otherTitle):\n\(app.debugDescription)")

        // And the new item collapses again like any other.
        next.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForExistence(timeout: 15), "View original did not build the next chat")
        next.tap()
        XCTAssertTrue(app.textFields["lark.box"].waitForNonExistence(timeout: 5), "Collapse left the next chat on the page")
        assertLabel("看原文", on: next, "the next item's source toggle after Collapse")
    }

    // MARK: - Driving the app

    /// The Inbox as the Assistant sidebar opens it (the placeholder beside the list), from a paired launch.
    @MainActor private func launchInbox(_ inboxHub: String) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + inboxHub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-dev.openChat", "",
                               "-openSplitFresh", "YES", "-inbox.kind", "", "-inbox.source", ""]
        app.launchPinned()
        acceptIconAlert()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10), "no app window")
        regularWidth = app.windows.firstMatch.frame.width > 600
        requireHittable(element("sidebar-inbox"), "Inbox in the Assistant sidebar")
        element("sidebar-inbox").tap()
        requireHittable(element("inbox.more"), "the Inbox's own toolbar")
    }

    /// Back as a finger does it, from the leading edge of the window (a regular-width navigation bar spans it).
    @MainActor private func backToInbox() {
        sleep(1) // once the push has settled
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.0, dy: 0.5))
            .press(forDuration: 0.1,
                   thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.1)
        requireHittable(element("inbox.more"), "the Inbox list after Back")
    }

    /// The dedicated hub: `PERSONA_INBOX_HUB` must name a second mock started with `MOCK_MANAGERS=0` (a hub from before
    /// the roster, whose Inbox lists every item — the default mock files the second one under another assistant the
    /// Inbox leaves out). Anything else skips this one case; the other three keep running on the default hub.
    private func singleAssistantHub() throws -> String {
        guard let inboxHub = ProcessInfo.processInfo.environment["PERSONA_INBOX_HUB"] else {
            throw XCTSkip("needs a second scripts/mock/mock_hub.py with MOCK_MANAGERS=0 (TEST_RUNNER_PERSONA_INBOX_HUB): "
                          + "the default mock's second item is another assistant's")
        }
        // Its own hub, not this one under another name (a trailing slash, another spelling of the host).
        guard Self.origin(inboxHub) != Self.origin(hub) else {
            throw XCTSkip("TEST_RUNNER_PERSONA_INBOX_HUB names the default hub: the reused-column case needs a second one")
        }
        // Before the roster there is no /api/v1/managers at all (404). A one-row roster (MOCK_MANAGERS=1) answers 200,
        // and that hub still files and filters items by assistant.
        let roster = status(try XCTUnwrap(URL(string: inboxHub + "/api/v1/managers")))
        guard roster == 404 else {
            throw XCTSkip("the hub at TEST_RUNNER_PERSONA_INBOX_HUB has a manager roster (status \(roster)): start it with MOCK_MANAGERS=0")
        }
        let data = try Data(contentsOf: try XCTUnwrap(URL(string: inboxHub + "/api/v1/inbox")))
        let items = (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["items"] as? [[String: Any]] ?? []
        guard let other = items.first(where: { $0["id"] as? String == Self.otherItem }),
              ["assistant", ""].contains(other["manager"] as? String ?? "") else {
            throw XCTSkip("the hub at TEST_RUNNER_PERSONA_INBOX_HUB files \(Self.otherItem) under another assistant: "
                          + "start it with MOCK_MANAGERS=0")
        }
        return inboxHub
    }

    /// A hub's identity, not its spelling: scheme and host in either case, `localhost`, `127.0.0.1` and `::1` the same
    /// machine, and the scheme's own port when the URL leaves it out. `…` and `…/` are one hub; `localhost` and
    /// `127.0.0.1` on one port are one too.
    static func origin(_ raw: String) -> String {
        guard let u = URLComponents(string: raw) else { return raw.lowercased() }
        let scheme = (u.scheme ?? "").lowercased()
        var host = (u.host ?? "").lowercased()
        if ["localhost", "127.0.0.1", "::1", "[::1]"].contains(host) { host = "loopback" }
        let port = u.port ?? (scheme == "https" ? 443 : scheme == "http" ? 80 : -1)
        return "\(scheme)://\(host):\(port)"
    }

    /// What the request answered, written on URLSession's thread and read after the expectation: a reference box, so no
    /// captured `var` crosses threads.
    private final class Answer: @unchecked Sendable {
        var code = -1
    }

    /// A GET's answer, as the mock gives it, waited for off the test's thread.
    private func status(_ url: URL) -> Int {
        let answered = expectation(description: "GET " + url.path)
        let answer = Answer()
        URLSession.shared.dataTask(with: url) { _, response, _ in
            answer.code = (response as? HTTPURLResponse)?.statusCode ?? -1
            answered.fulfill()
        }.resume()
        wait(for: [answered], timeout: 10)
        XCTAssertGreaterThanOrEqual(answer.code, 0, "no answer from \(url)")
        return answer.code
    }

    @MainActor private func openDetail(_ item: String) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-dev.openChat", "", "-openSplitFresh", "YES",
                               "-inbox.kind", "", "-inbox.source", "", "-openInboxItem", item]
        app.launchPinned()
        acceptIconAlert()
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 10), "no app window")
        regularWidth = app.windows.firstMatch.frame.width > 600
        requireHittable(element("inbox.detail.done"), "the \(item) detail")
    }

    /// The default mock's mail 501 carries the body this case looks for; MOCK_MAIL gives it another. Said plainly
    /// rather than waited on as a timeout.
    private func requireDefaultMailFixture() throws {
        let data = try Data(contentsOf: try XCTUnwrap(
            URL(string: hub + "/api/v1/mail/messages/\(Self.mailAccount)/\(Self.mailUID)?mailbox=INBOX")))
        let message = try XCTUnwrap(
            (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["message"] as? [String: Any],
            "no mail \(Self.mailUID) in this mock")
        let text = message["text"] as? String ?? ""
        guard text.contains(Self.mailBody) else {
            throw XCTSkip("needs the default scripts/mock/mock_hub.py: mail \(Self.mailUID) has no 「\(Self.mailBody)」 (MOCK_MAIL on?)")
        }
    }

    /// An item's row in the Inbox list, scrolled to: the Inbox is the page the sidebar opened, and its items push over
    /// it, so on that page a row's title is the only one (the open page's own title is in its bar, not here).
    @MainActor private func listRow(_ title: String) -> XCUIElement? {
        let deadline = Date.now.addingTimeInterval(20)
        repeat {
            let rows = app.staticTexts.matching(identifier: title).allElementsBoundByIndex
            if let row = rows.first(where: { $0.isHittable }) { return row }
            app.swipeUp(velocity: .slow) // a section below the first screen
            usleep(250_000)
        } while Date.now < deadline
        return nil
    }

    @MainActor private func requireWide() throws {
        guard regularWidth else { throw XCTSkip("needs an iPad or an open Duo: the reused detail column") }
    }

    @MainActor private func mailBodyElement() -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", Self.mailBody)).firstMatch
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
        // The button reads as its preview and its action: the action is the part to look for, not the whole label.
        let expectation = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label CONTAINS %@", label), object: element)
        XCTAssertEqual(XCTWaiter().wait(for: [expectation], timeout: timeout), .completed,
                       "\(what) does not say \(label):\n\(app.debugDescription)")
    }

    // MARK: - What the mock was told

    /// The item open again as unhandled, and the mail unseen: Done or a reader in the shared mock by an earlier run
    /// must not keep this one from reporting what is on screen (a done item's toolbar has no Done either).
    private func resetFixture(_ item: String, on hub: String? = nil) throws {
        try post("/api/v1/inbox/done", body: ["ids": [item], "done": false], to: hub ?? self.hub)
        if item == Self.mailItem {
            try post("/api/v1/mail/messages/\(Self.mailAccount)/\(Self.mailUID)/flags",
                     body: ["mailbox": "INBOX", "seen": false], to: hub ?? self.hub)
        }
    }

    private func post(_ path: String, body: [String: Any], to hub: String) throws {
        let url = try XCTUnwrap(URL(string: hub + path))
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let answered = expectation(description: "POST " + path)
        URLSession.shared.dataTask(with: request) { _, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            answered.fulfill()
        }.resume()
        wait(for: [answered], timeout: 10)
    }

    /// Only this chat's reports: the mock is shared and other runs' chats report too.
    private func chatReadReports() throws -> [[String: Any]] {
        try records("/mock/lark_reads", key: "reads").filter { $0["feed"] as? String == Self.chatFeed }
    }

    /// Only this message's seen writes; the reset's unseen write, other accounts and other UIDs are left out.
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
