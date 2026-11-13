import Foundation
import Testing
@testable import Persona

/// A terminal that asked for its screen and heard nothing (#6327). Open Terminal Here's `pane:` terminal has no session
/// row, so it was never subscribed and sat on Taking over… for good; a takeover that went with a node connection that
/// dropped waited the same way. Against the real AppModel on a socket of the test's own, a hub that streams terminals
/// and lets the phone drive them.
@MainActor @Suite(.serialized)
struct TerminalTakeoverTests {
    private let pane = SessionKey(machineID: "m", sessionID: "pane:w1:p3")
    private let grid = TerminalGridSize(cols: 44, rows: 30)

    /// What the app sent over the socket.
    final class Sent {
        var messages: [[String: Any]] = []
        func of(_ type: String) -> [[String: Any]] { messages.filter { $0["type"] as? String == type } }
    }

    final class Clock {
        var date = Date()
        func advance(_ s: TimeInterval) { date = date.addingTimeInterval(s) }
    }

    private func app() throws -> (AppModel, SyncSocket, Sent) {
        let hub = FakeHub()
        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "terminal-takeover-" + UUID().uuidString))
        let socket = SyncSocket()
        let sent = Sent()
        socket.onReceive = { sent.messages.append($0) }
        model.linkHooksForTests = (transport: transport, socket: { socket })
        model.connectOnly(.demo)
        deliver(socket, #"{"type":"hello","features":["terminal_stream","terminal_control"]}"#)
        return (model, socket, sent)
    }

    private func deliver(_ socket: SyncSocket, _ json: String) { socket.deliver?(Data(json.utf8)) }

    private func machines(_ socket: SyncSocket, online: Bool) {
        deliver(socket, #"{"type":"machines","machines":[{"id":"m","name":"Mac Studio","kind":"ssh","online":\#(online)}]}"#)
    }

    /// A feed on the phone's grid that wants to drive its terminal, on the test's clock.
    private func feed(_ model: AppModel, _ clock: Clock = Clock()) -> TerminalFeed {
        let feed = TerminalFeed(key: pane)
        feed.now = { clock.date }
        feed.wantsControl = true
        feed.setGrid(grid, model)
        return feed
    }

    @Test func aPaneAddressHasATerminalOfItsOwn() {
        #expect(pane.isPane)
        #expect(AppModel.terminalUsable(nil, pane))
        #expect(!AppModel.terminalUsable(nil, SessionKey(machineID: "m", sessionID: "s1")), "a session not listed yet waits for its row")
    }

    @Test func aPaneTerminalTakesOverAndTakesKeys() async throws {
        let (model, socket, sent) = try app()
        let feed = feed(model)
        await feed.tick(model)
        let sub = try #require(sent.of("terminal_subscribe").last, "the subscribe never went out: Taking over… for good")
        #expect(sub["session_id"] as? String == pane.sessionID)
        #expect(sub["control"] as? [String: Int] == ["cols": 44, "rows": 30])
        #expect(feed.control == .starting && feed.waitingSince != nil)

        deliver(socket, #"{"type":"terminal_control","machine_id":"m","session_id":"pane:w1:p3","state":"active","cols":44,"rows":30}"#)
        deliver(socket, #"{"type":"terminal","machine_id":"m","session_id":"pane:w1:p3","epoch":"e1","seq":1,"full":true,"cols":44,"rows":30,"data":"JCA="}"#)
        #expect(feed.drives && feed.loaded && feed.waitingSince == nil)

        feed.type("ls\r", model)
        #expect(sent.of("terminal_input").last?["text"] as? String == "ls\r")
    }

    @Test func anUnansweredTakeoverSaysWhyAndAsksAgain() async throws {
        let (model, _, sent) = try app()
        let clock = Clock()
        let feed = feed(model, clock)
        await feed.tick(model)
        #expect(sent.of("terminal_subscribe").count == 1)

        clock.advance(9.5)
        await feed.tick(model)
        #expect(feed.failure == nil && sent.of("terminal_unsubscribe").isEmpty, "still within the wait")

        clock.advance(1)
        await feed.tick(model)
        #expect(sent.of("terminal_unsubscribe").count == 1, "the hub drops it: the next subscribe has it ask the node again")
        #expect(feed.failure == TerminalFeed.notAnswering(nil))
        #expect(feed.loaded, "the reason, not a spinner")
        #expect(feed.control == .starting, "still taking it over between tries: the size chip says so, not Wrap")

        clock.advance(1.1) // the backoff's first second
        await feed.tick(model)
        let again = try #require(sent.of("terminal_subscribe").dropFirst().first)
        #expect(again["epoch"] == nil, "from a whole frame")
        #expect(again["control"] as? [String: Int] == ["cols": 44, "rows": 30])
        #expect(feed.control == .starting)
    }

    @Test func retryDropsTheUnansweredSubscriptionFirst() async throws {
        let (model, _, sent) = try app()
        let feed = feed(model)
        await feed.tick(model)
        sent.messages = []
        feed.retryNow(model)
        #expect(sent.messages.compactMap { $0["type"] as? String } == ["terminal_unsubscribe", "terminal_subscribe"])
    }

    @Test func theComputerBackOnlineSubscribesAtOnce() async throws {
        let (model, socket, sent) = try app()
        machines(socket, online: false)
        let clock = Clock()
        let feed = feed(model, clock)
        await feed.tick(model)
        for wait in [1.0, 2, 4, 8] { // the hub answers each at once while the node is gone; the backoff grows
            deliver(socket, #"{"type":"terminal","machine_id":"m","session_id":"pane:w1:p3","error":"m: offline"}"#)
            clock.advance(wait + 0.1)
            await feed.tick(model)
        }
        deliver(socket, #"{"type":"terminal","machine_id":"m","session_id":"pane:w1:p3","error":"m: offline"}"#)
        let machine = try #require(model.machine(pane.machineRef))
        #expect(!machine.online && feed.failure == TerminalFeed.notAnswering(machine), "in the app's words, not the hub's")
        #expect(feed.failure?.contains("Mac Studio") == true)
        #expect(feed.control == .starting, "it takes the terminal over once the computer is back")
        sent.messages = []
        clock.advance(1)
        await feed.tick(model)
        #expect(sent.of("terminal_subscribe").isEmpty, "the next try is 15 seconds away")

        machines(socket, online: true)
        clock.advance(0.25)
        await feed.tick(model)
        let sub = try #require(sent.of("terminal_subscribe").first, "back online: subscribe again now")
        #expect(sub["control"] as? [String: Int] == ["cols": 44, "rows": 30])
    }
}
