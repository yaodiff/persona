import Foundation
import PersonaWire

// How the socket comes back after a drop without the app noticing (docs/operations/hub-restart.md「Clients tolerate short disconnects」, #1634):
// when to retry, how long the titles keep saying nothing while it does, and where the hub's numbered broadcasts left
// off, so a reconnect replays only what was missed instead of fetching everything again. Pure: LiveConnection drives
// it, the tests drive it directly (the web's src/lib/reconnect.ts is the same rules).

/// A socket that went while it was connected.
nonisolated struct LinkDrop: Equatable, Sendable {
    /// When it went.
    var at: Date
    /// The hub said it was restarting: a `hub_restarting` message, or close code 1001.
    var restart: Bool

    /// How long a drop stays invisible: an ordinary one, and one the hub announced.
    static let grace: TimeInterval = 8
    static let restartGrace: TimeInterval = 30
    /// How long the quick retries after a restart go on before the ordinary backoff takes over.
    static let restartFast: TimeInterval = 30

    /// Until when the app keeps looking connected.
    var graceUntil: Date { at.addingTimeInterval(restart ? Self.restartGrace : Self.grace) }

    func inGrace(at now: Date) -> Bool { now < graceUntil }

    /// The wait before the next attempt. `attempt`: attempts so far in the current phase (0 = the first after the
    /// drop, or after the quick phase ended); `rand` in [0, 1). A restart retries right away (150–400 ms), then every
    /// 0.5–1 s for `restartFast`, never counting as a failure; anything else retries fast once (300–600 ms), then
    /// backs off as before (1 s doubling to 20 s, ±20 %).
    static func retryDelay(attempt: Int, restart: Bool, sinceDrop: TimeInterval, rand: Double) -> TimeInterval {
        if restart && sinceDrop < restartFast { return attempt == 0 ? 0.15 + rand * 0.25 : 0.5 + rand * 0.5 }
        if attempt == 0 { return 0.3 + rand * 0.3 }
        return min(20, pow(2, Double(attempt - 1))) * (0.8 + rand * 0.4)
    }
}

/// Where this app is in the hub's numbered broadcasts (`event_stream`, `event_seq`; docs/protocol/protocol.md「Event resume」):
/// kept across reconnects, forgotten with another hub or pairing (not on disk: a relaunch fetches everything anyway).
nonisolated struct EventCursor: Equatable, Sendable {
    var stream = ""
    var seq = 0

    /// What the socket's URL carries to resume (both or nothing).
    var queryItems: [URLQueryItem] {
        guard !stream.isEmpty else { return [] }
        return [URLQueryItem(name: "event_stream", value: stream), URLQueryItem(name: "event_seq", value: String(seq))]
    }

    /// The cursor after a hello. Another stream (or a hub that numbers nothing) starts over at the hello's number. On
    /// the same stream a resumed hello leaves it where it was: the replay that follows moves it, so a drop in the
    /// middle of the replay resumes from the last one that arrived. A full refresh covers everything up to the hello's.
    func afterHello(stream: String?, seq: Int?, resumed: Bool) -> EventCursor {
        guard let stream, !stream.isEmpty else { return EventCursor() }
        if stream != self.stream { return EventCursor(stream: stream, seq: seq ?? 0) }
        if resumed { return self }
        return EventCursor(stream: stream, seq: max(self.seq, seq ?? 0))
    }

    /// The cursor after any other message: a numbered broadcast moves it forward.
    func afterEvent(_ eventSeq: Int?) -> EventCursor {
        guard !stream.isEmpty, let eventSeq, eventSeq > seq else { return self }
        return EventCursor(stream: stream, seq: eventSeq)
    }
}

/// What a message off the socket says about the connection itself, besides its event (decoded with it, off the main
/// thread): its number in the broadcast stream, and for a hello where the stream is and whether it resumed.
nonisolated struct FrameMeta: Equatable, Sendable {
    var type = ""
    var eventSeq: Int?
    var eventStream: String?
    var resumed = false

    /// The hub is about to restart (`hub_restarting`): the close that follows is a planned one.
    var isRestarting: Bool { type == "hub_restarting" }

    private struct Fields: Decodable {
        var type: String?
        var eventSeq: Int?
        var eventStream: String?
        var resumed: Bool?
        enum CodingKeys: String, CodingKey {
            case type, resumed
            case eventSeq = "event_seq", eventStream = "event_stream"
        }
    }

    /// The frames that can say anything here, found without parsing the rest (session lists are large and come every
    /// second while agents work: no second full decode for them).
    static func read(_ data: Data) -> FrameMeta {
        guard data.range(of: Data(#""event_"#.utf8)) != nil || data.range(of: Data(#""hub_restarting""#.utf8)) != nil else {
            return FrameMeta()
        }
        return decode(data)
    }

    static func decode(_ data: Data) -> FrameMeta {
        guard let f = try? JSON.decoder.decode(Fields.self, from: data) else { return FrameMeta() }
        return FrameMeta(type: f.type ?? "", eventSeq: f.eventSeq, eventStream: f.eventStream, resumed: f.resumed ?? false)
    }
}

// MARK: Writes through a gap

/// Writes that survive a hub restart (docs/operations/hub-restart.md「Queued writes, idempotent replay」): one op id per logical operation, sent
/// as `X-Persona-Op-Id`, so the hub does it once however often it arrives; a request that died on the way goes again with
/// the same id, for up to `limit`, before the caller shows its failure.
nonisolated enum OpRetry {
    static let header = "X-Persona-Op-Id"
    /// How long a write keeps trying through a gap before it fails.
    static let limit: TimeInterval = 60

    /// The wait before attempt `n` (1-based): 0.5 s, 1 s, 2 s, then every 4 s.
    static func delay(_ n: Int) -> TimeInterval { min(4, 0.5 * pow(2, Double(max(0, n - 1)))) }

    /// Runs `run` until it answers, fails with something other than a gap, `limit` has passed, or `keepTrying` says
    /// stop (then the last error is thrown). `run` must send the same op every time: the caller makes its id once.
    /// `keepTrying` is asked after each failure and again after each wait, and the wait itself ends early once it says
    /// stop: a message whose drop's grace ran out goes to the outbox then, not after the next try.
    /// `wait` sleeps until the time is up or its second argument (`keepTrying`) says stop (tests pass one that does not
    /// sleep).
    static func untilDelivered<T>(limit: TimeInterval = OpRetry.limit, now: () -> Date = Date.init,
                                  keepTrying: @escaping () -> Bool = { true },
                                  wait: (TimeInterval, _ keepTrying: @escaping () -> Bool) async -> Void = { await OpRetry.sleep($0, while: $1) },
                                  _ run: () async throws -> T) async throws -> T {
        let start = now()
        for n in 1... {
            do {
                return try await run()
            } catch {
                let left = limit - now().timeIntervalSince(start)
                guard error.isGap, left > 0, keepTrying() else { throw error }
                await wait(min(delay(n), left), keepTrying)
                guard keepTrying() else { throw error }
            }
        }
        fatalError("unreachable")
    }

    /// Sleeps `seconds` in short steps, waking early once `keepTrying` says stop.
    static func sleep(_ seconds: TimeInterval, while keepTrying: @escaping () -> Bool) async {
        let end = Date.now.addingTimeInterval(seconds)
        while keepTrying() {
            let left = end.timeIntervalSinceNow
            guard left > 0 else { return }
            try? await Task.sleep(for: .seconds(min(left, 0.2)))
        }
    }
}

nonisolated extension Error {
    /// The request may not have reached a running hub: no network, the connection refused or reset (the old process
    /// going), a timeout, something in front of the hub answering 502 / 503 / 504, or the hub saying the op is still
    /// running elsewhere (`pending`). The hub's own answers (with a `code`, or saying what was delivered) are not gaps.
    var isGap: Bool {
        if isOffline { return true }
        if let u = self as? URLError, u.code == .timedOut { return true }
        let ns = self as NSError
        if ns.domain == NSURLErrorDomain && ns.code == NSURLErrorTimedOut { return true }
        guard let api = self as? APIError else { return false }
        if api.code == OpReceipt.pendingCode { return true }
        return [502, 503, 504].contains(api.status) && api.code == nil && api.fields["delivered"] == nil
    }
}

/// The hub's answer about an op (`GET /api/v1/ops/{id}`, or a repeated write's `202 {"state": …}`).
nonisolated enum OpReceipt {
    /// `APIError.code` the client gives a `202 {"state":"pending"}`: the first try is still running; try again.
    static let pendingCode = "op_pending"
    /// … and a `202 {"state":"unknown"}`: nobody knows whether it ran (do not send it again on your own).
    static let unknownCode = "op_unknown"
}

/// What a reconnect fetches again (AppModel.didConnect): everything after a full one; after a resumed one only the
/// open chats' subscriptions (`since_seq`) and the phone's registration, which the socket needs anyway.
nonisolated struct ReconnectPlan: Equatable, Sendable {
    var resumed: Bool
    /// Pull the interactions, inbox, todos, tasks, Feishu, the agent lists, and say Updating… meanwhile.
    var refetch: Bool { !resumed }
}
