import XCTest

/// What one push costs (docs/apple/ios-state.md「Cost of one push」): on each page at rest, pushes that change one field of one
/// agent, as the real hub sends them (the machine's whole session list), and the view bodies they make run
/// (`-perfCounts YES`), per push. `rev`: the transcript grew, nothing shown changed (most of the real hub's pushes);
/// `last`: one agent's last line. Reports them (PERF lines, attachments) and fails over the budget (docs/apple/ios-state.md
/// 「第 5 步」): after steps 1–3 such a push draws only the leaf views that show the field that changed.
///   PORT=18797 MOCK_AGENTS=1 MOCK_CHURN=1 MOCK_LONG_HISTORY=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_CHURN_HUB=http://127.0.0.1:18797 xcodebuild test … -only-testing:PersonaUITests/RenderBudgetTests
final class RenderBudgetTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!
    private static let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private static let persona = "server/b2e4d6f8-0000-4000-8000-000000000004"
    private static let pushes = 5
    /// Bodies per push each page may run, by push: what steps 1–3 left (the Agents list: the row's time, and for a
    /// last line the row; the chats: nothing).
    private static let budget: [String: [String: Double]] = [
        "Agents list": ["rev": 1, "last": 2],
        "Agent chat": ["rev": 0, "last": 0],
        "Persona chat": ["rev": 0, "last": 0],
    ]
    /// One stray body in the five pushes (a row's time ticking over while measured) is not a regression; two are.
    private static let slack = 0.25

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_CHURN_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 MOCK_CHURN=1 MOCK_LONG_HISTORY=1 (PERSONA_CHURN_HUB)")
        }
        hub = h
        _ = mock("reset")
    }

    private func launch(_ args: [String]) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        sleep(3)
        app.terminate()
        app.launchArguments = ["-openAgentsFresh", "YES", "-openChatsFresh", "YES", "-perfCounts", "YES"] + args
        app.launchPinned()
        sleep(8)
    }

    @discardableResult
    private func mock(_ what: String) -> [String: Any] {
        var out: [String: Any] = [:]
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: URL(string: hub + "/mock/" + what)!) { data, _, _ in
            out = (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return out
    }

    private func bodies() -> [String: Int] {
        let probe = app.staticTexts["perf.counts"]
        XCTAssertTrue(probe.waitForExistence(timeout: 5), "perf.counts (launched with -perfCounts YES)")
        let d = (try? JSONSerialization.jsonObject(with: Data(probe.label.utf8))) as? [String: Any] ?? [:]
        return d["bodies"] as? [String: Int] ?? [:]
    }

    /// The counts once nothing runs for a second: a chat still loading its history or laying out its rows right after
    /// launch is not a push's cost (it showed up as bubbles in whichever push was measured first).
    private func settled() -> [String: Int] {
        var last = bodies()
        for _ in 0..<20 {
            sleep(1)
            let now = bodies()
            if now == last { return now }
            last = now
        }
        XCTFail("the page never stopped drawing")
        return last
    }

    /// Bodies per push, by view, for `pushes` pushes of `what`, a second apart.
    private func measure(_ page: String, _ what: String) {
        let before = settled()
        for _ in 0..<Self.pushes {
            mock("bump?what=" + what)
            sleep(1)
        }
        let after = bodies()
        let per = after.compactMap { k, v -> (String, Double)? in
            let d = Double(v - (before[k] ?? 0)) / Double(Self.pushes)
            return d > 0 ? (k, d) : nil
        }.sorted { $0.1 > $1.1 }
        let total = per.reduce(0) { $0 + $1.1 }
        let text = String(format: "%@, one `%@` push: %.1f bodies (%d views): ", page, what, total, per.count)
            + per.map { String(format: "%@ %.1f", $0.0, $0.1) }.joined(separator: ", ")
        print("PERF " + text)
        let att = XCTAttachment(string: text)
        att.name = "budget \(page) \(what)"
        att.lifetime = .keepAlways
        add(att)
        guard let limit = Self.budget[page]?[what] else { return XCTFail("no budget for \(page) / \(what)") }
        XCTAssertLessThanOrEqual(total, limit + Self.slack, "\(page): one `\(what)` push draws \(text); budget \(limit)")
    }

    /// What one switch between Assistant and Agents may draw again (#5438, docs/apple/performance.md P5 and P7): the pages
    /// stay built, so a switch moves them and swaps the top bars, and no page's own body has a reason to run. Before, a
    /// switch ran the Agents list's body three or four times (it read the app's tab, the strip's `moving` and the chat on
    /// screen), the chat page's two or three times (`@Environment(\.dismiss)`), `RootView`'s once (the tab, read in
    /// `ShellView`'s initial state) and the composer bar's on every frame of the slide (a window frame kept in `@State`).
    /// Switched by the frame probe's cue, as `PerfAssistantTests/testTabSwitchAssistantAgentsByCue`.
    func testShellSwitch() {
        launch(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", Self.persona, "-dev.openChat", "",
                "-showFrameProbe", "YES"])
        let switches = 4
        func flip() {
            CFNotificationCenterPostNotification(CFNotificationCenterGetDarwinNotifyCenter(),
                                                 CFNotificationName("com.persona.perf.shell.switch" as CFString), nil, nil, true)
            sleep(2)
        }
        flip(); flip() // Agents is built by its first visit, and both pages have been in front once
        let before = settled()
        for _ in 0..<switches { flip() }
        let after = bodies()
        let per = after.compactMap { k, v -> (String, Double)? in
            let d = Double(v - (before[k] ?? 0)) / Double(switches)
            return d > 0 ? (k, d) : nil
        }.sorted { $0.1 > $1.1 }
        let text = "one Assistant ⇄ Agents switch: " + per.map { String(format: "%@ %.1f", $0.0, $0.1) }.joined(separator: ", ")
        print("PERF " + text)
        let att = XCTAttachment(string: text)
        att.name = "budget shell switch"
        att.lifetime = .keepAlways
        add(att)
        let drawn = Dictionary(uniqueKeysWithValues: per)
        for view in ["RootView", "ChatsView", "AgentsList", "ChatView", "Composer", "AgentsFieldPlace"] {
            XCTAssertLessThanOrEqual(drawn[view] ?? 0, Self.slack, "\(view) ran on a switch that changes nothing it draws: \(text)")
        }
        // The bar follows the keyboard's focus and the draft; a slide is neither. Three leaves room for the two bars'
        // own changes at the ends of the slide (it was 30 and more).
        XCTAssertLessThanOrEqual(drawn["ShellComposerBar"] ?? 0, 3 + Self.slack, "the composer bar ran during the slide: \(text)")
    }

    func testAgentsList() {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", ""])
        // The bumped agent is a working one, and its row has to be on screen: below the asks (five of them since the
        // mock's inbox grew, #622) it was not built, nothing was drawn and the push cost 0 (#633). Only Working.
        let working = app.buttons["agents.filter.working"].firstMatch
        XCTAssertTrue(working.waitForExistence(timeout: 10), "the 工作中 filter")
        working.tap()
        sleep(2)
        measure("Agents list", "rev")
        measure("Agents list", "last")
    }

    func testAgentChat() {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", Self.widget])
        measure("Agent chat", "rev")
        measure("Agent chat", "last")
    }

    func testPersonaChat() {
        launch(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", Self.persona])
        measure("Persona chat", "rev")
        measure("Persona chat", "last")
    }
}
