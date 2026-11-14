import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct ConnectionStatusTests {
    typealias Phase = ConnectionStatus.Phase

    @Test func phases() {
        let p = ConnectionStatus.phase(network:link:stalled:catchingUp:)
        #expect(p(true, .idle, false, false) == .online) // not paired or in the background: nothing to say
        #expect(p(false, .idle, false, false) == .online)
        #expect(p(false, .connected, false, false) == .waitingForNetwork) // no network wins over a socket that has not noticed
        #expect(p(false, .failed("x"), false, false) == .waitingForNetwork)
        #expect(p(true, .connecting, false, false) == .connecting)
        #expect(p(true, .failed("x"), false, true) == .connecting)
        #expect(p(true, .connected, true, false) == .connecting) // stalled: a weak network
        #expect(p(true, .connected, false, true) == .updating)
        #expect(p(true, .connected, false, false) == .online)
    }

    @Test func titles() {
        #expect([Phase.waitingForNetwork, .connecting, .updating].map(\.title) == ["等待网络", "连接中…", "更新中…"])
    }

    /// What the title shows is also what VoiceOver reads (#3441): Connecting… becomes Hub updating… only while the hub restarts.
    @Test func labelTextWhileTheHubRestarts() {
        #expect(ConnectionLabel.text(.connecting, hubRestarting: true) == "hub 正在更新…")
        #expect(ConnectionLabel.text(.connecting, hubRestarting: false) == "连接中…")
        #expect(ConnectionLabel.text(.waitingForNetwork, hubRestarting: true) == "等待网络")
        #expect(ConnectionLabel.text(.rejected, hubRestarting: true) == "配对已失效")
    }

    @Test func detailFacts() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        let facts = ConnectionDetails.facts(phase: .connecting, link: .failed("Could not connect to the server."),
                                            nextTry: now.addingTimeInterval(3.4), lastOnline: now.addingTimeInterval(-75),
                                            rtt: nil, outbox: 2, now: now)
        #expect(facts == ["上次失败：Could not connect to the server.", "4 秒后重试", "上次连上：1 分钟前", "2 条消息连上后自动发送"])
        #expect(ConnectionDetails.facts(phase: .online, link: .connected, nextTry: nil, lastOnline: now, rtt: 0.0423,
                                        outbox: 0, now: now) == ["延迟 42 毫秒"])
        #expect(ConnectionDetails.ago(now.addingTimeInterval(-12), now: now) == "12 秒前")
    }

    @Test func pendingMessagesSurviveAsJSON() throws {
        var msg = PendingMessage(text: "断网时写的", images: [])
        msg.state = .waiting
        let back = try JSON.decoder.decode(PendingMessage.self, from: JSON.encoder.encode(msg))
        #expect(back == msg)
    }

    @Test func offlineErrorsAreQuiet() {
        for code in [URLError.notConnectedToInternet, .networkConnectionLost, .cannotConnectToHost, .cannotFindHost] {
            #expect(URLError(code).isOffline)
            #expect(URLError(code).shown == nil)
        }
        #expect(!APIError(status: 502, message: "machine offline").isOffline)
        #expect(SendOutcome.waiting.state == .waiting)
    }
}
