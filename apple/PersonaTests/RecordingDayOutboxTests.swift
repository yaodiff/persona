import Foundation
import Testing
@testable import Persona

private typealias Outbox = RecordingDayOutbox

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z
private let today = UUID(uuidString: "00000000-0000-0000-0000-00000000d001")!
private let yesterday = UUID(uuidString: "00000000-0000-0000-0000-00000000d000")!

/// `seconds` after the anchor (before it when negative).
private func time(_ seconds: Double) -> Date { anchor.addingTimeInterval(seconds) }

private func utc(_ text: String) throws -> Date { try Date.ISO8601FormatStyle().parse(text) }

private func chunkKey(_ seq: Int, of recording: UUID = today) -> Outbox.Key { .chunk(recording, seq: seq) }

private func eventKey(_ n: Int, of recording: UUID = today) -> Outbox.Key { .event(recording, n: n) }

/// A chunk with `seconds` of audio that ended `end` seconds after the anchor; a 10 s live chunk fits a message.
private func chunk(_ seq: Int, endsAt end: Double, seconds: Double = RecordingDayContract.liveChunkSeconds,
                   bytes: Int = 42_000, of recording: UUID = today) -> Outbox.Item {
    Outbox.Item(key: chunkKey(seq, of: recording), startedAt: time(end - seconds), finishedAt: time(end), frameBytes: bytes)
}

/// A minute of audio, recorded out of reach: too big for a message.
private func minute(_ seq: Int, endsAt end: Double) -> Outbox.Item {
    chunk(seq, endsAt: end, seconds: RecordingDayContract.backlogChunkSeconds, bytes: 240_000)
}

/// Event `n` at `at` seconds after the anchor, of a recording that began `recordingStart` seconds after it.
private func event(_ n: Int, at moment: Double, recordingStart: Double = -3_600, of recording: UUID = today) -> Outbox.Item {
    Outbox.Item(key: eventKey(n, of: recording), startedAt: time(recordingStart), finishedAt: time(moment), frameBytes: 0)
}

private extension RecordingDayOutbox {
    /// `plan` with the phone in reach at full power and nothing in flight or with the system: each test sets what it is
    /// about.
    mutating func planned(_ items: [Item], at now: Date = anchor, reachable: Bool = true, lowPower: Bool = false,
                          suspending: Bool = false, inFlight: Set<Key> = [], system: Set<Key> = []) -> [Action] {
        plan(items, now: now, reachable: reachable, lowPower: lowPower, suspending: suspending, inFlight: inFlight,
             withSystem: system)
    }

    /// Plans, then reports every action as sent, as the watch's transport does.
    mutating func send(_ items: [Item], at now: Date = anchor, reachable: Bool = true, lowPower: Bool = false,
                       suspending: Bool = false, inFlight: Set<Key> = [], system: Set<Key> = []) -> [Action] {
        let actions = planned(items, at: now, reachable: reachable, lowPower: lowPower, suspending: suspending,
                              inFlight: inFlight, system: system)
        for action in actions { sent(action, at: now) }
        return actions
    }
}

/// The plan of an outbox that has sent nothing yet.
private func firstPlan(_ items: [Outbox.Item], at now: Date = anchor, reachable: Bool = true, lowPower: Bool = false,
                       suspending: Bool = false, inFlight: Set<Outbox.Key> = [], system: Set<Outbox.Key> = []) -> [Outbox.Action] {
    var outbox = Outbox()
    return outbox.planned(items, at: now, reachable: reachable, lowPower: lowPower, suspending: suspending,
                          inFlight: inFlight, system: system)
}

/// The watch's all-day transport decisions (docs/assistant/day-recap.md「Chunks」and「Acknowledgements and retries」):
/// which chunk or event goes, when, and which way. Pure, so nonisolated.
struct RecordingDayOutboxTests {
    // MARK: Live path

    @Test func aLiveChunkGoesByMessageAtOnce() {
        var outbox = Outbox()
        let fresh = chunk(0, endsAt: 0)
        let first = outbox.send([fresh])
        #expect(first == [.message(chunkKey(0))])
        // Sent, it waits for its reply instead of going again.
        let waiting = outbox.planned([fresh], at: time(1))
        #expect(waiting == [])
        // The acknowledgement takes it out; the next chunk goes the same way.
        outbox.acked([chunkKey(0)])
        let next = outbox.planned([chunk(1, endsAt: 10)], at: time(10))
        #expect(next == [.message(chunkKey(1))])
    }

    @Test func atMostThreeChunkMessagesAreInFlight() {
        var outbox = Outbox()
        let chunks = (0..<5).map { chunk($0, endsAt: Double($0) * 10) }
        let first = outbox.send(chunks, at: time(40))
        #expect(first == [.message(chunkKey(0)), .message(chunkKey(1)), .message(chunkKey(2))])
        let full = outbox.planned(chunks, at: time(40))
        #expect(full == [])
        outbox.acked([chunkKey(0)])
        let freed = outbox.planned(Array(chunks.dropFirst()), at: time(41))
        #expect(freed == [.message(chunkKey(3))], "one acknowledgement frees one place")
        // Messages the transport is still framing count too.
        let behind = firstPlan(chunks, at: time(40), inFlight: [chunkKey(0), chunkKey(1)])
        #expect(behind == [.message(chunkKey(2))])
    }

    @Test func aChunkTooBigForAMessageGoesAsAFile() {
        let big = minute(0, endsAt: 0)
        let live = chunk(1, endsAt: 10)
        #expect(firstPlan([big, live], at: time(10)) == [.file(chunkKey(0)), .message(chunkKey(1))])
        // The whole live envelope stays under the limit: one of exactly the limit is a file.
        let exact = chunk(2, endsAt: 0, bytes: RecordingDayContract.maxMessageBytes)
        let under = chunk(3, endsAt: 0, bytes: RecordingDayContract.maxMessageBytes - 1)
        #expect(firstPlan([exact]) == [.file(chunkKey(2))])
        #expect(firstPlan([under]) == [.message(chunkKey(3))])
        #expect(!Outbox.fitsMessage(RecordingDayContract.maxMessageBytes) && Outbox.fitsMessage(RecordingDayContract.maxMessageBytes - 1),
                "the transport checks a framed envelope the same way")
        // Becoming reachable takes back only what is under the limit.
        var outbox = Outbox()
        outbox.reachabilityChanged(true)
        let reclaimed = outbox.planned([exact, under], system: [exact.key, under.key])
        #expect(reclaimed == [.cancel(chunkKey(3)), .message(chunkKey(3))])
    }

    // MARK: Background path

    @Test func outOfReachChunksGoAsFilesAsSoonAsTheyAreFinished() {
        let fresh = minute(0, endsAt: 0)
        #expect(firstPlan([fresh], reachable: false) == [.file(chunkKey(0))], "no wait for more")
        let chunks = (0..<6).map { minute($0, endsAt: Double($0) * 60) }
        #expect(firstPlan(chunks.reversed(), at: time(300), reachable: false) == chunks.map { .file($0.key) }, "oldest first")
        // A chunk that fits a message still goes as a file while the phone is away.
        #expect(firstPlan([chunk(9, endsAt: 0)], reachable: false) == [.file(chunkKey(9))])
        // Every finished chunk, however many the system holds already: one left to a later pass would wait for the
        // app's next run if it ended without a hand-off.
        let hour = (0..<60).map { minute($0, endsAt: Double($0) * 60) }
        let queued = (100..<160).map { minute($0, endsAt: Double($0) * 60) }
        let all = firstPlan(hour + queued, at: time(10_000), reachable: false, system: Set(queued.map(\.key)))
        #expect(all == hour.map { .file($0.key) })
        let events = (0..<60).map { event($0, at: Double($0)) }
        #expect(firstPlan(events, reachable: false) == events.map { .userInfo($0.key) }, "events too")
    }

    @Test func becomingReachableTakesSmallChunksBackFromTheSystem() {
        let small = (0..<4).map { chunk($0, endsAt: -40 + Double($0) * 10) }
        let big = minute(4, endsAt: -100)
        let everything = Set((small + [big]).map(\.key))
        // Without a change of reachability, transfers with the system stay there.
        #expect(firstPlan(small + [big], system: everything) == [])

        var outbox = Outbox()
        outbox.reachabilityChanged(false)
        outbox.reachabilityChanged(true)
        let reclaimed = outbox.send(small + [big], system: everything)
        let cancels = small.map { Outbox.Action.cancel($0.key) }
        #expect(reclaimed == cancels + [.message(chunkKey(0)), .message(chunkKey(1)), .message(chunkKey(2))],
                "cancelled oldest first and sent live; the minute stays a file")
        // The last one goes once a place frees, and nothing is taken back twice.
        outbox.acked([chunkKey(0)])
        let rest = outbox.planned(Array(small.dropFirst()) + [big], at: time(1), system: [big.key])
        #expect(rest == [.message(chunkKey(3))])

        // A hand-off just after keeps them with the system; they come back live once the app runs in reach.
        var suspending = Outbox()
        suspending.reachabilityChanged(true)
        #expect(suspending.planned(small, suspending: true, system: Set(small.map(\.key))) == [])
        #expect(suspending.planned(small, at: time(1), system: Set(small.map(\.key))).prefix(4) == ArraySlice(cancels))
    }

    @Test func aCancelledTransfersReportDoesNotHoldTheChunkBack() {
        var outbox = Outbox()
        let live = chunk(0, endsAt: 0)
        outbox.sent(.file(chunkKey(0)), at: time(-100))
        outbox.reachabilityChanged(true)
        let reclaimed = outbox.send([live], system: [chunkKey(0)])
        #expect(reclaimed == [.cancel(chunkKey(0)), .message(chunkKey(0))])
        // The system reports the cancelled transfer as failed: the message is what counts now.
        outbox.transferFinished(chunkKey(0), failed: true, at: time(1))
        let waiting = outbox.planned([live], at: time(2))
        #expect(waiting == [])
        #expect(outbox.nextWake(after: time(2)) == time(RecordingDayContract.ackTimeout), "only its reply's timeout")

        // Cancelled while every message place was taken: the failure report doesn't keep it off the live path.
        var crowded = Outbox()
        let flying = (1...3).map { chunk($0, endsAt: 0) }
        crowded.sent(.file(chunkKey(0)), at: time(-100))
        crowded.reachabilityChanged(true)
        let cancelled = crowded.planned([live] + flying, inFlight: Set(flying.map(\.key)), system: [chunkKey(0)])
        #expect(cancelled == [.cancel(chunkKey(0))])
        crowded.transferFinished(chunkKey(0), failed: true, at: time(1))
        crowded.acked([chunkKey(1)])
        let resent = crowded.planned([live] + flying.dropFirst(), at: time(2), inFlight: Set(flying.dropFirst().map(\.key)))
        #expect(resent == [.message(chunkKey(0))])
    }

    // MARK: Events

    @Test func eventsGoByMessageOrUserInfoInTheirOrder() {
        let paused = event(1, at: 20)
        let started = event(0, at: 10)
        let audio = chunk(0, endsAt: 5)
        let items = [audio, paused, started]
        #expect(firstPlan(items) == [.message(eventKey(0)), .message(eventKey(1)), .message(chunkKey(0))],
                "a recording's events in n order, before its chunks")
        #expect(firstPlan(items, reachable: false) == [.userInfo(eventKey(0)), .userInfo(eventKey(1)), .file(chunkKey(0))])
        #expect(firstPlan(items, suspending: true) == [.userInfo(eventKey(0)), .userInfo(eventKey(1)), .file(chunkKey(0))])
        // Low Power Mode holds chunk messages, never an event.
        #expect(firstPlan([started, audio], at: time(5), lowPower: true) == [.message(eventKey(0))])
        // Events have message places of their own: three chunk messages awaiting replies don't hold one back.
        let flying = (1...3).map { chunk($0, endsAt: Double($0) * 10) }
        #expect(firstPlan(flying + [started], inFlight: Set(flying.map(\.key))) == [.message(eventKey(0))])
        let many = (0..<5).map { event($0, at: Double($0)) }
        #expect(firstPlan(many) == (0..<3).map { .message(eventKey($0)) })
        // Yesterday's recording began earlier: its events go first.
        let closed = event(7, at: -100, recordingStart: -90_000, of: yesterday)
        #expect(firstPlan([started, closed]) == [.message(eventKey(7, of: yesterday)), .message(eventKey(0))])
    }

    @Test func anEventTheSessionQueuedIsNotSentAgainWhileItWaits() {
        var outbox = Outbox()
        let started = event(0, at: 0)
        #expect(outbox.send([started]) == [.message(eventKey(0))])
        // The message failed and the session queued the event as user info instead.
        let queued = outbox.planned([started], at: time(31), system: [eventKey(0)])
        #expect(queued == [], "not sent again while the system holds it")
        // Nothing reports the end of a user info transfer, so the transport looks again a minute later, and again.
        #expect(outbox.nextWake(after: time(31)) == time(31 + Outbox.userInfoCheckSeconds))
        let later = outbox.planned([started], at: time(91), system: [eventKey(0)])
        #expect(later == [])
        #expect(outbox.nextWake(after: time(91)) == time(151))
        // Found gone from the system's queue without an acknowledgement: it gets a minute from then, not from when it
        // was last seen there, and then goes again after its backoff.
        let left = outbox.planned([started], at: time(151))
        #expect(left == [])
        #expect(outbox.nextWake(after: time(151)) == time(211))
        let overdue = outbox.planned([started], at: time(211))
        #expect(overdue == [])
        #expect(outbox.nextWake(after: time(211)) == time(213))
        let again = outbox.planned([started], at: time(213))
        #expect(again == [.message(eventKey(0))])
    }

    @Test func aQueuedEventIsFollowedUpByTheOutboxsWakesAlone() throws {
        // Out of reach and with the recording stopped, nothing else starts a pass: the wakes must reach the resend.
        var outbox = Outbox()
        let paused = event(0, at: 0)
        #expect(outbox.send([paused], reachable: false) == [.userInfo(eventKey(0))])
        let delivered = time(600)
        var now = anchor
        var resentAt: Date?
        for _ in 0..<30 {
            now = try #require(outbox.nextWake(after: now), "a wake is due while the event waits")
            // The system holds it for ten minutes, then delivers it; no acknowledgement comes.
            let actions = outbox.planned([paused], at: now, reachable: false, system: now < delivered ? [eventKey(0)] : [])
            if !actions.isEmpty {
                #expect(actions == [.userInfo(eventKey(0))])
                resentAt = now
                break
            }
        }
        let resent = try #require(resentAt)
        let latest = delivered.addingTimeInterval(Outbox.userInfoCheckSeconds + RecordingDayContract.fileAckTimeout
                                                  + RecordingDayContract.backoffFirst)
        #expect(resent > delivered && resent <= latest)

        // When the system's report of the transfer does reach the outbox, a failure goes again after its backoff.
        var reported = Outbox()
        _ = reported.send([paused], reachable: false)
        _ = reported.planned([paused], at: time(5), reachable: false, system: [eventKey(0)])
        reported.transferFinished(eventKey(0), failed: true, at: time(10))
        #expect(reported.nextWake(after: time(10)) == time(12))
        #expect(reported.planned([paused], at: time(12), reachable: false) == [.userInfo(eventKey(0))])
    }

    // MARK: Low Power Mode

    @Test func lowPowerHoldsTheLivePathAtMostFiveMinutes() {
        var outbox = Outbox()
        // Six live chunks; the first started 10 s before the anchor and ended at it.
        let chunks = (0..<6).map { chunk($0, endsAt: Double($0) * 10) }
        let held = outbox.planned(chunks, at: time(50), lowPower: true)
        #expect(held == [])
        let release = time(Outbox.lowPowerHoldSeconds)
        #expect(outbox.nextWake(after: time(50)) == release, "the transport wakes when the hold ends")
        let almost = outbox.planned(chunks, at: release.addingTimeInterval(-0.1), lowPower: true)
        #expect(almost == [])

        let released = outbox.send(chunks, at: release, lowPower: true)
        #expect(released == [.message(chunkKey(0)), .message(chunkKey(1)), .message(chunkKey(2))])
        #expect(release.timeIntervalSince(chunks[0].startedAt) == RecordingAudioSpec.lowPowerTransferBatchSeconds,
                "the oldest audio leaves five minutes after it was recorded, not later")
        // The rest of the batch follows, although the oldest of it ended only 260 s ago.
        outbox.acked([chunkKey(0), chunkKey(1), chunkKey(2)])
        let rest = outbox.planned(Array(chunks.dropFirst(3)), at: release.addingTimeInterval(1), lowPower: true)
        #expect(rest == [.message(chunkKey(3)), .message(chunkKey(4)), .message(chunkKey(5))])
        // A chunk finished after the release starts the next hold.
        let later = outbox.planned([chunk(6, endsAt: 295)], at: time(296), lowPower: true)
        #expect(later == [])
        #expect(outbox.nextWake(after: time(296)) == time(295 + Outbox.lowPowerHoldSeconds))
        // Without Low Power Mode nothing waits.
        #expect(firstPlan(chunks, at: time(50)) == [.message(chunkKey(0)), .message(chunkKey(1)), .message(chunkKey(2))])
    }

    // MARK: Handing off

    @Test func handingOffQueuesEverythingWithTheSystem() {
        var outbox = Outbox()
        let failing = chunk(0, endsAt: -40)
        let flying = chunk(1, endsAt: -30)
        let queued = chunk(2, endsAt: -20)
        let cut = chunk(3, endsAt: 0)
        let paused = event(0, at: 0)
        outbox.sent(.message(chunkKey(0)), at: time(-35))
        outbox.messageFailed(chunkKey(0), at: time(-1))
        outbox.sent(.message(chunkKey(1)), at: time(-1))
        // On the live path no chunk would go now: a backoff, a message awaiting its reply, a file with the system, and
        // Low Power Mode holding the chunk the pause just cut.
        let live = outbox.planned([failing, flying, queued, cut], lowPower: true, inFlight: [chunkKey(1)], system: [chunkKey(2)])
        #expect(live == [])
        // Before the app stops running, all of it goes to the system, except what is already there.
        let all = [failing, flying, queued, cut, paused]
        let handed = outbox.send(all, lowPower: true, suspending: true, inFlight: [chunkKey(1)], system: [chunkKey(2)])
        #expect(handed == [.userInfo(eventKey(0)), .file(chunkKey(0)), .file(chunkKey(1)), .file(chunkKey(3))])
        let again = outbox.planned(all, at: time(1), suspending: true, system: [chunkKey(2)])
        #expect(again == [], "nothing is queued twice")
        // However many transfers the system holds, a hand-off queues the rest: nothing would send them until the app runs
        // again.
        let crowded = (100..<150).map { chunk($0, endsAt: -50) }
        let handedAnyway = firstPlan(crowded + [cut, paused], suspending: true, system: Set(crowded.map(\.key)))
        #expect(handedAnyway == [.userInfo(eventKey(0)), .file(chunkKey(3))])
    }

    // MARK: Retries

    @Test func anUnacknowledgedEventGoesAgainWithABackoffDoublingToFiveMinutes() {
        var outbox = Outbox()
        let item = event(0, at: 0)
        var sentAt = anchor
        let first = outbox.send([item], at: sentAt)
        #expect(first == [.message(eventKey(0))])
        for delay in [2.0, 4, 8, 16, 32, 64, 128, 256, 300, 300] {
            let overdue = sentAt.addingTimeInterval(RecordingDayContract.ackTimeout)
            let waiting = outbox.planned([item], at: overdue.addingTimeInterval(-0.001))
            #expect(waiting == [], "waits for the reply")
            let backingOff = outbox.planned([item], at: overdue)
            #expect(backingOff == [], "overdue: backs off \(delay) s")
            #expect(outbox.nextWake(after: overdue) == overdue.addingTimeInterval(delay))
            let early = outbox.planned([item], at: overdue.addingTimeInterval(delay - 0.001))
            #expect(early == [])
            sentAt = overdue.addingTimeInterval(delay)
            let again = outbox.send([item], at: sentAt)
            #expect(again == [.message(eventKey(0))])
        }
    }

    @Test func aMessageErrorBacksOffAndIsNotCountedTwice() {
        var outbox = Outbox()
        let item = chunk(0, endsAt: 0)
        _ = outbox.send([item])
        outbox.messageFailed(chunkKey(0), at: time(1))
        let early = outbox.planned([item], at: time(2.999))
        #expect(early == [])
        let retried = outbox.send([item], at: time(3))
        #expect(retried == [.message(chunkKey(0))])
        // Overdue first, its error after: one failure, so the wait is the second failure's 4 s.
        let overdue = outbox.planned([item], at: time(33))
        #expect(overdue == [])
        outbox.messageFailed(chunkKey(0), at: time(34))
        #expect(outbox.nextWake(after: time(34)) == time(37))
        let third = outbox.planned([item], at: time(37))
        #expect(third == [.message(chunkKey(0))])
    }

    @Test func becomingReachableRetriesAtOnce() {
        var outbox = Outbox()
        let item = chunk(0, endsAt: 0)
        for _ in 0..<5 {
            outbox.sent(.message(chunkKey(0)), at: anchor)
            outbox.messageFailed(chunkKey(0), at: anchor)
        }
        #expect(outbox.nextWake(after: anchor) == time(32), "five failures: a 32 s backoff")
        let backingOff = outbox.planned([item], at: time(1))
        #expect(backingOff == [])
        outbox.reachabilityChanged(false)
        outbox.reachabilityChanged(true)
        let retried = outbox.send([item], at: time(1))
        #expect(retried == [.message(chunkKey(0))])
        outbox.messageFailed(chunkKey(0), at: time(2))
        #expect(outbox.nextWake(after: time(2)) == time(4), "the backoff starts again from 2 s")

        // A message that went out just before the link dropped goes again too, without waiting for its timeout.
        var dropped = Outbox()
        let other = chunk(1, endsAt: 0)
        _ = dropped.send([other])
        dropped.reachabilityChanged(false)
        dropped.reachabilityChanged(true)
        let resent = dropped.planned([other], at: time(1), inFlight: [chunkKey(1)])
        #expect(resent == [.message(chunkKey(1))])
    }

    @Test func aFileGoesAgainOnlyAfterAFailureOrAMinuteWithoutAnAcknowledgement() {
        var outbox = Outbox()
        let item = minute(0, endsAt: 0)
        let first = outbox.send([item])
        #expect(first == [.file(chunkKey(0))])
        // While the system holds it, never again, however long: the phone may be away for hours.
        let held = outbox.planned([item], at: time(7_200), system: [chunkKey(0)])
        #expect(held == [])
        #expect(outbox.nextWake(after: time(7_200)) == nil, "the system reports the end of a file transfer")
        // Delivered: its acknowledgement gets a minute, then it counts as failed and goes after its backoff.
        outbox.transferFinished(chunkKey(0), failed: false, at: time(7_200))
        let waiting = outbox.planned([item], at: time(7_259))
        #expect(waiting == [])
        let silent = outbox.planned([item], at: time(7_260))
        #expect(silent == [])
        let resent = outbox.send([item], at: time(7_262))
        #expect(resent == [.file(chunkKey(0))])
        // A transfer the system reports failed goes again after its backoff (the second failure: 4 s).
        outbox.transferFinished(chunkKey(0), failed: true, at: time(7_270))
        let early = outbox.planned([item], at: time(7_273))
        #expect(early == [])
        let again = outbox.planned([item], at: time(7_274))
        #expect(again == [.file(chunkKey(0))])

        // After a relaunch the system reports a transfer this outbox never saw: delivered, it waits for its
        // acknowledgement.
        var relaunched = Outbox()
        relaunched.transferFinished(chunkKey(0), failed: false, at: anchor)
        let quiet = relaunched.planned([item], at: time(1))
        #expect(quiet == [])

        // One found gone from the system's queue before any report goes again a minute after it was found gone.
        var unreported = Outbox()
        _ = unreported.send([item])
        let seen = unreported.planned([item], at: time(100), system: [chunkKey(0)])
        #expect(seen == [])
        let gone = unreported.planned([item], at: time(159))
        #expect(gone == [])
        #expect(unreported.nextWake(after: time(159)) == time(219))
        let expired = unreported.planned([item], at: time(219))
        #expect(expired == [])
        let back = unreported.planned([item], at: time(221))
        #expect(back == [.file(chunkKey(0))])
        // The system's report of a failure, coming after it was found gone, still counts.
        var late = Outbox()
        _ = late.send([item])
        _ = late.planned([item], at: time(100), system: [chunkKey(0)])
        _ = late.planned([item], at: time(101))
        late.transferFinished(chunkKey(0), failed: true, at: time(102))
        #expect(late.nextWake(after: time(102)) == time(104))
    }

    @Test func aChunkWhoseMessagesFailGoesAsAFileFiveMinutesBehind() {
        var outbox = Outbox()
        // Recorded from 10 s before the anchor: five minutes behind at 290 s.
        let item = chunk(0, endsAt: 0)
        _ = outbox.send([item])
        outbox.messageFailed(chunkKey(0), at: time(285))
        let backingOff = outbox.planned([item], at: time(286))
        #expect(backingOff == [])
        #expect(outbox.nextWake(after: time(286)) == time(287))
        #expect(outbox.send([item], at: time(287)) == [.message(chunkKey(0))])
        outbox.messageFailed(chunkKey(0), at: time(288))
        let beforeItsRetry = outbox.planned([item], at: time(289))
        #expect(beforeItsRetry == [])
        #expect(outbox.nextWake(after: time(289)) == time(Outbox.liveDelaySeconds - 10), "wakes when it is overdue")
        let file = outbox.send([item], at: time(290))
        #expect(file == [.file(chunkKey(0))], "the message backoff doesn't hold the file back")
        // With the system now: no message for it until the phone becomes reachable again.
        let held = outbox.planned([item], at: time(300), system: [chunkKey(0)])
        #expect(held == [])

        // A retry still awaiting its reply when the chunk turns five minutes behind goes as a file then, not at its
        // reply's timeout.
        var retrying = Outbox()
        _ = retrying.send([item])
        retrying.messageFailed(chunkKey(0), at: time(280))
        #expect(retrying.send([item], at: time(282)) == [.message(chunkKey(0))])
        let flying = retrying.planned([item], at: time(289), inFlight: [chunkKey(0)])
        #expect(flying == [])
        #expect(retrying.nextWake(after: time(289)) == time(Outbox.liveDelaySeconds - 10), "the deadline, not the reply's")
        let overtaken = retrying.send([item], at: time(290), inFlight: [chunkKey(0)])
        #expect(overtaken == [.file(chunkKey(0))])
        // A first try is no retry: it left on time, and only its reply's timeout moves it.
        var first = Outbox()
        let behind = chunk(1, endsAt: -295)
        #expect(first.send([behind]) == [.message(chunkKey(1))])
        #expect(first.planned([behind], at: time(1), inFlight: [chunkKey(1)]) == [])
        #expect(first.nextWake(after: time(1)) == time(RecordingDayContract.ackTimeout))
        let timedOut = first.planned([behind], at: time(RecordingDayContract.ackTimeout), inFlight: [chunkKey(1)])
        #expect(timedOut == [.file(chunkKey(1))])

        // Age alone never moves a chunk off the live path: a backlog from before a relaunch goes live.
        #expect(firstPlan([chunk(1, endsAt: -3_600)]) == [.message(chunkKey(1))])
        // A failed message doesn't hold back the file once the phone is out of reach either.
        var away = Outbox()
        _ = away.send([item])
        away.messageFailed(chunkKey(0), at: time(1))
        let queued = away.planned([item], at: time(2), reachable: false)
        #expect(queued == [.file(chunkKey(0))])
    }

    @Test func theLiveLinkCountsAsDownAfterThreeChunkMessagesFailInARow() {
        var outbox = Outbox()
        #expect(outbox.liveUp(reachable: true, now: anchor) && !outbox.liveUp(reachable: false, now: anchor))
        let chunks = (0..<3).map { chunk($0, endsAt: Double($0)) }
        _ = outbox.send(chunks)
        outbox.messageFailed(chunkKey(0), at: time(1))
        outbox.messageFailed(chunkKey(1), at: time(1))
        #expect(outbox.liveUp(reachable: true, now: time(1)))
        outbox.messageFailed(chunkKey(2), at: time(1))
        #expect(!outbox.liveUp(reachable: true, now: time(1)), "three in a row")
        outbox.linkAnswered()
        #expect(outbox.liveUp(reachable: true, now: time(1)), "an answer from the phone")

        // Replies that never come count too.
        var silent = Outbox()
        _ = silent.send(chunks)
        let overdue = time(RecordingDayContract.ackTimeout)
        _ = silent.planned(chunks, at: overdue)
        #expect(!silent.liveUp(reachable: true, now: overdue))
        silent.reachabilityChanged(true)
        #expect(silent.liveUp(reachable: true, now: overdue), "a change of reachability starts over")

        // The phone answering without the chunk (it couldn't store it) is no sign of a broken link, nor is an event.
        var refused = Outbox()
        _ = refused.send(chunks + [event(0, at: 0)])
        for seq in 0..<3 { refused.messageFailed(chunkKey(seq), at: time(1), answered: true) }
        _ = refused.planned(chunks + [event(0, at: 0)], at: overdue)
        #expect(refused.liveUp(reachable: true, now: overdue))
    }

    /// The live path down while the session keeps saying the phone is reachable (its app took too long to wake): the
    /// minute-long chunks cut meanwhile never go by message, so nothing would show it works again. After
    /// `liveProbeSeconds` the capture cuts a short chunk again, whose message tries it; the transport wakes for that.
    @Test func aLiveLinkDownIsTriedAgainAfterACoolDown() throws {
        var outbox = Outbox()
        let chunks = (0..<3).map { chunk($0, endsAt: Double($0)) }
        _ = outbox.send(chunks)
        let overdue = time(RecordingDayContract.ackTimeout)
        _ = outbox.planned(chunks, at: overdue)
        #expect(!outbox.liveUp(reachable: true, now: overdue))
        let probe = overdue.addingTimeInterval(Outbox.liveProbeSeconds)
        #expect(!outbox.liveUp(reachable: true, now: probe.addingTimeInterval(-1)))
        #expect(outbox.liveUp(reachable: true, now: probe), "a short chunk tries the live path again")
        #expect(!outbox.liveUp(reachable: false, now: probe), "never while the session says the phone is away")
        // With nothing else to wait for, the transport wakes then to tell the capture.
        outbox.acked(chunks.map(\.key))
        _ = outbox.planned([], at: overdue)
        #expect(outbox.nextWake(after: overdue) == probe)

        // The probe's message fails too: down again, for as long.
        let tried = chunk(3, endsAt: probe.timeIntervalSince(anchor) + 10)
        _ = outbox.send([tried], at: probe.addingTimeInterval(10))
        outbox.messageFailed(tried.key, at: probe.addingTimeInterval(12))
        #expect(!outbox.liveUp(reachable: true, now: probe.addingTimeInterval(12)))
        let next = probe.addingTimeInterval(12 + Outbox.liveProbeSeconds)
        #expect(outbox.liveUp(reachable: true, now: next))
        // Its reply instead brings the path back for good.
        outbox.linkAnswered()
        #expect(outbox.liveUp(reachable: true, now: probe.addingTimeInterval(13)))
    }

    // MARK: Order and keeping

    @Test func theOldestGoesFirstAcrossDays() {
        let lastOfYesterday = chunk(7, endsAt: -600, of: yesterday)
        let closedYesterday = event(3, at: -595, recordingStart: -90_000, of: yesterday)
        let first = chunk(0, endsAt: -580)
        let second = chunk(1, endsAt: -570)
        let items = [second, closedYesterday, first, lastOfYesterday]
        #expect(firstPlan(items) == [.message(eventKey(3, of: yesterday)), .message(chunkKey(7, of: yesterday)),
                                     .message(chunkKey(0)), .message(chunkKey(1))])
        #expect(firstPlan(items, reachable: false) == [.userInfo(eventKey(3, of: yesterday)), .file(chunkKey(7, of: yesterday)),
                                                       .file(chunkKey(0)), .file(chunkKey(1))])
    }

    @Test func thePlannerNeverDropsAChunk() throws {
        var outbox = Outbox()
        let old = chunk(0, endsAt: -3 * 86_400)
        let away = outbox.planned([old], reachable: false)
        #expect(away == [.file(chunkKey(0))])
        let near = outbox.planned([old])
        #expect(near == [.message(chunkKey(0))])
        // However often it fails, it comes back once its backoff ends.
        var now = anchor
        for _ in 0..<20 {
            let actions = outbox.send([old], at: now, reachable: false)
            #expect(actions == [.file(chunkKey(0))])
            outbox.transferFinished(chunkKey(0), failed: true, at: now)
            now = try #require(outbox.nextWake(after: now))
        }
    }

    @Test func anAcknowledgedChunkIsForgotten() {
        var outbox = Outbox()
        let item = chunk(0, endsAt: 0)
        _ = outbox.send([item])
        outbox.messageFailed(chunkKey(0), at: anchor)
        #expect(outbox.nextWake(after: anchor) != nil)
        outbox.acked([chunkKey(0)])
        #expect(outbox.nextWake(after: anchor) == nil)
        // Offered again (its acknowledgement didn't reach the library), it goes at once: no backoff, no reply awaited.
        let again = outbox.planned([item])
        #expect(again == [.message(chunkKey(0))])
        // One no longer offered (acknowledged another way, erased, lost) is forgotten as well.
        _ = outbox.send([item])
        _ = outbox.planned([])
        #expect(outbox.nextWake(after: anchor) == nil)
    }

    @Test func backoffDoublesFromTwoSecondsToFiveMinutes() {
        #expect((1...10).map(Outbox.backoff) == [2, 4, 8, 16, 32, 64, 128, 256, 300, 300])
        #expect(Outbox.backoff(1_000) == RecordingDayContract.backoffMax)
    }

    // MARK: Items and envelopes

    @Test func chunkItemsComeFromTheirRecords() throws {
        let record = RecordingSegment(index: 4, start: 40, duration: 10, startedAt: time(40), level: -30)
        let item = try #require(Outbox.item(record, of: today, frameBytes: 41_000))
        #expect(item == Outbox.Item(key: chunkKey(4), startedAt: time(40), finishedAt: time(50), frameBytes: 41_000))
        var unclocked = record
        unclocked.startedAt = nil
        #expect(Outbox.item(unclocked, of: today, frameBytes: 41_000) == nil, "no header can describe it")
        var long = record
        long.duration = 601
        #expect(Outbox.item(long, of: today, frameBytes: 41_000) == nil)
        #expect(Outbox.fitsMessage(Outbox.estimatedFrameBytes(seconds: RecordingDayContract.liveChunkSeconds)),
                "a live chunk fits a message")
        #expect(!Outbox.fitsMessage(Outbox.estimatedFrameBytes(seconds: RecordingDayContract.backlogChunkSeconds)))
    }

    @Test func envelopesAreMeasuredExactly() {
        let header = RecordingDayHeader(recording: today, recordingStartedAt: time(-3_600), tz: "Asia/Shanghai", day: "2027-01-15",
                                        seq: 1_234, startedAt: time(0.25), duration: 10, level: -38.5)
        let audio = Data(count: 40_000)
        #expect(Outbox.frameBytes(audio: audio.count, header: header) == header.frame(audio: audio).count)
        // Ten seconds at the recording's bit rate in its container, with the header: under the live limit.
        let file = Int(RecordingDayContract.liveChunkSeconds) * RecordingAudioSpec.bitRate / 8 + 2_048
        #expect(Outbox.fitsMessage(Outbox.frameBytes(audio: file, header: header)))
        #expect(Outbox.frameBytes(audio: file, header: header) <= Outbox.estimatedFrameBytes(seconds: 10), "the estimate is generous")
    }

    @Test func eventItemsKeepTheirRecordingsOrder() {
        func item(_ event: RecordingDayEvent) -> Outbox.Item {
            Outbox.item(RecordingDayEventEnvelope(recording: today, recordingStartedAt: time(-3_600), tz: "UTC", day: "2027-01-15",
                                                  event: event))
        }
        let paused = item(.paused(3, at: time(10), reason: RecordingDayContract.Reason.interrupted))
        #expect(paused == Outbox.Item(key: eventKey(3), startedAt: time(-3_600), finishedAt: time(10), frameBytes: 0))
        let erase = item(.erase(4, id: UUID(), from: time(-600), to: time(0), seqs: [1, 2]))
        #expect(erase.finishedAt == time(0))
        let lost = item(.lost(5, seqs: 7...9))
        #expect(lost.finishedAt == time(-3_600), "without times, its recording's start")
    }

    // MARK: Days

    @Test func aDayClosesAtItsOwnMidnight() throws {
        // 2026-11-01 lasts 25 hours in New York, 2026-03-08 23.
        for (day, midnight) in [("2026-11-01", "2026-11-02T05:00:00Z"), ("2026-03-08", "2026-03-09T04:00:00Z")] {
            var entry = dayEntry(day: day, tz: "America/New_York")
            let end = try utc(midnight)
            #expect(Outbox.closingTime(of: entry, now: end.addingTimeInterval(-1)) == nil, "\(day) still lasts")
            #expect(Outbox.closingTime(of: entry, now: end) == end)
            #expect(Outbox.closingTime(of: entry, now: end.addingTimeInterval(3 * 86_400)) == end, "days later, its own midnight")
            entry.closedAt = end
            #expect(Outbox.closingTime(of: entry, now: end) == nil, "a closed day stays as it is")
        }
        // A recording's zone is fixed: one begun in Shanghai ends at Shanghai's midnight, wherever the watch is now.
        let shanghai = dayEntry(day: "2026-11-12", tz: "Asia/Shanghai")
        let midnight = try utc("2026-11-12T16:00:00Z")
        #expect(Outbox.closingTime(of: shanghai, now: midnight.addingTimeInterval(-1)) == nil)
        #expect(Outbox.closingTime(of: shanghai, now: midnight) == midnight)
        let clip = RecordingEntry(id: UUID(), createdAt: time(-86_400 * 3), source: .watch)
        #expect(Outbox.closingTime(of: clip, now: anchor) == nil)
    }

    @Test func unacknowledgedChunksAreKeptTheirDaysAfterTheDay() throws {
        let november = dayEntry(day: "2026-11-01", tz: "America/New_York")
        let week = try utc("2026-11-09T05:00:00Z")
        let oneDay = try utc("2026-11-03T05:00:00Z")
        #expect(Outbox.ageLimit(of: november, keepDays: 7) == week)
        #expect(Outbox.ageLimit(of: november, keepDays: 1) == oneDay)
        // The day after the clocks went forward lasts 23 hours, and counts as one.
        let march = dayEntry(day: "2026-03-07", tz: "America/New_York")
        let shortDay = try utc("2026-03-09T04:00:00Z")
        #expect(Outbox.ageLimit(of: march, keepDays: 1) == shortDay)
        // The phone's value is read within 1…7 days; 0 is forever, which the watch keeps as long as it may
        // (#16577: "`0` means forever, while the watch stays capped at 7 days"; #16642).
        #expect(Outbox.ageLimit(of: november, keepDays: 0) == week)
        #expect(Outbox.ageLimit(of: november, keepDays: 30) == Outbox.ageLimit(of: november, keepDays: 7))
        var undated = november
        undated.day = nil
        #expect(Outbox.ageLimit(of: undated, keepDays: 7) == nil)
    }

    @Test func aSettledDayGoesUnlessAPhoneEraseMayBeAskedAgain() {
        var settled = dayEntry(day: "2027-01-14", tz: "UTC")
        settled.closedAt = time(-28_800)
        settled.nextSeq = 3
        settled.prunedBelow = 3
        #expect(Outbox.isRemovable(settled, now: anchor))
        // Just closed: "Erase the last 10 minutes" right after midnight still reaches it, and it needs its own event.
        var justClosed = settled
        justClosed.closedAt = time(-60)
        #expect(!Outbox.isRemovable(justClosed, now: anchor))
        #expect(!Outbox.isRemovable(justClosed, now: time(RecordingRolling.eraseMinutes * 60 - 60)))
        #expect(Outbox.isRemovable(justClosed, now: time(-60 + Outbox.keepClosedSeconds)))
        var asked = settled
        asked.dayErases = [RecordingDayEraseDone(id: UUID(), seqs: [[1, 1]], at: time(-86_400))]
        #expect(!Outbox.isRemovable(asked, now: anchor), "a repeated erase request still gets its seqs")
        let weekLater = time(RecordingDayContract.eraseMemoryDays * 86_400)
        #expect(Outbox.isRemovable(asked, now: weekLater))
        var open = settled
        open.closedAt = nil
        var unacknowledged = settled
        unacknowledged.dayEvents = [.closed(0, at: time(-28_800), seqCount: 3)]
        var short = settled
        short.prunedBelow = 2
        for entry in [open, unacknowledged, short] { #expect(!Outbox.isRemovable(entry, now: anchor)) }
    }

    @Test func onlyDaysWithADayAndAZoneCanGo() {
        #expect(Outbox.isSendable(dayEntry(day: "2027-01-15", tz: "UTC")))
        #expect(!Outbox.isSendable(dayEntry(day: "2027-1-15", tz: "UTC")))
        #expect(!Outbox.isSendable(dayEntry(day: "2027-01-15", tz: "Mars/Olympus")))
        var undated = dayEntry(day: "2027-01-15", tz: "UTC")
        undated.timeZone = nil
        #expect(!Outbox.isSendable(undated))
        #expect(!Outbox.isSendable(RecordingEntry(id: UUID(), createdAt: anchor, source: .watch, kind: .rolling)))
    }

    /// A pass of an app that may be suspended any moment (neither in front nor recording) hands everything to the
    /// system, like a hand-off: a message's reply would die with it, and chunks taken back from the system to go live
    /// would wait for its next run. Taking them back waits for a pass of an app that keeps running.
    @Test func anAppThatMaySoonBeSuspendedHandsOffAndTakesNothingBack() {
        let later = time(60)
        #expect(Outbox.suspends(handingOff: true, handOffUntil: .distantPast, now: anchor, appActive: true, recording: true))
        #expect(Outbox.suspends(handingOff: false, handOffUntil: later, now: anchor, appActive: true, recording: false),
                "just after a hand-off")
        #expect(!Outbox.suspends(handingOff: false, handOffUntil: anchor, now: anchor, appActive: true, recording: false))
        #expect(!Outbox.suspends(handingOff: false, handOffUntil: .distantPast, now: anchor, appActive: false, recording: true),
                "the microphone keeps it running")
        #expect(Outbox.suspends(handingOff: false, handOffUntil: .distantPast, now: anchor, appActive: false, recording: false),
                "out of the front, not recording")

        // Back in reach while the app is about to be suspended: the small chunks stay with the system.
        let small = (0..<5).map { chunk($0, endsAt: -50 + Double($0) * 10) }
        let held = Set(small.prefix(4).map(\.key))
        var outbox = Outbox()
        outbox.reachabilityChanged(true)
        let suspending = outbox.send(small, suspending: true, system: held)
        #expect(suspending == [.file(chunkKey(4))], "nothing cancelled, the rest queued")
        // Once the app keeps running, they come back to go live.
        let running = outbox.planned(small, at: time(1), system: Set(small.map(\.key)))
        #expect(running == small.map { .cancel($0.key) } + (0..<3).map { .message(chunkKey($0)) })
    }

    @Test func aDayHoldsSomethingUnacknowledgedWhileAChunkOrAnEventIsLeft() {
        var empty = dayEntry(day: "2027-01-15", tz: "UTC")
        empty.closedAt = anchor
        var chunked = empty
        chunked.segments = [RecordingSegment(index: 0, start: 0, duration: 10, startedAt: anchor)]
        var evented = empty
        evented.dayEvents = [.closed(0, at: anchor, seqCount: 1)]
        #expect(!Outbox.holdsUnacknowledged([empty]) && !Outbox.holdsUnacknowledged([]))
        #expect(Outbox.holdsUnacknowledged([empty, chunked]) && Outbox.holdsUnacknowledged([evented]))
    }

    /// The transport looks a chunk's file up off the main actor: its exact envelope, or no file (the chunk is lost).
    @Test func aChunksFileIsMeasuredOrFoundGone() throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent("day-measure-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: folder) }
        let header = RecordingDayHeader(recording: today, recordingStartedAt: anchor, tz: "UTC", day: "2027-01-15", seq: 0,
                                        startedAt: anchor, duration: 10, level: -30)
        let file = folder.appendingPathComponent(RecordingSegment.fileName(0))
        try Data(repeating: 7, count: 41_000).write(to: file)
        #expect(Outbox.measure(audio: file, header: header) == .bytes(Outbox.frameBytes(audio: 41_000, header: header)))
        #expect(Outbox.measure(audio: folder.appendingPathComponent(RecordingSegment.fileName(1)), header: header) == .missing)
    }

    @Test func theWatchStateSaysWhoStoppedTheMicrophone() {
        #expect(Outbox.watchState(allDay: true, recording: true, interrupted: false, dayOpen: true, intent: true) == .recording)
        #expect(Outbox.watchState(allDay: true, recording: false, interrupted: true, dayOpen: true, intent: true) == .interrupted)
        #expect(Outbox.watchState(allDay: true, recording: false, interrupted: false, dayOpen: true, intent: true) == .paused)
        #expect(Outbox.watchState(allDay: false, recording: false, interrupted: false, dayOpen: true, intent: true) == .stopped,
                "the system or a crash stopped it while the user wants it on")
        #expect(Outbox.watchState(allDay: false, recording: false, interrupted: false, dayOpen: true, intent: false) == .off,
                "Stop for Today")
        #expect(Outbox.watchState(allDay: false, recording: false, interrupted: false, dayOpen: false, intent: true) == .off)
    }

    private func dayEntry(day: String, tz: String) -> RecordingEntry {
        var entry = RecordingEntry(id: today, createdAt: time(-3_600), source: .watch, kind: .day)
        entry.day = day
        entry.timeZone = tz
        return entry
    }
}

/// What the watch keeps of its day recordings and how acknowledgements and time change it, on a temporary library.
@MainActor struct RecordingDayWatchLedgerTests {
    private func library() -> RecordingLibrary {
        RecordingLibrary(root: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString))
    }

    /// An open day recording on the watch, in UTC.
    private func day(_ lib: RecordingLibrary, createdAt: Date = anchor, day: String = "2027-01-15") throws -> UUID {
        try lib.create(source: .watch, kind: .day, createdAt: createdAt, day: day, timeZone: "UTC").id
    }

    /// Chunk `seq`, `duration` seconds from `start` seconds after `anchor`, with its audio on disk.
    private func chunk(_ lib: RecordingLibrary, _ id: UUID, _ seq: Int, at start: Double, duration: Double = 10) throws {
        try FileManager.default.createDirectory(at: lib.folder(id), withIntermediateDirectories: true)
        try Data([UInt8(seq % 256)]).write(to: lib.audio(id, seq))
        lib.addSegment(id, RecordingSegment(index: seq, start: start, duration: duration, startedAt: time(start), level: -30))
    }

    private func hasAudio(_ lib: RecordingLibrary, _ id: UUID, _ seq: Int) -> Bool {
        FileManager.default.fileExists(atPath: lib.audio(id, seq).path)
    }

    @Test func anAcknowledgementTakesChunksAndEventsOffTheWatch() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        for seq in 0..<3 { try chunk(lib, id, seq, at: Double(seq) * 10) }
        lib.appendDayEvent(id) { .started($0, at: anchor) }
        lib.appendDayEvent(id) { .mark($0, at: time(5), label: RecordingDayEvent.Label.bookmark) }
        let ledger = RecordingDayWatchLedger(library: lib)
        let taken = ledger.apply(RecordingDayAck(recording: id, seqs: [0, 1, 9], events: [0, 4]))
        #expect(taken.seqs == [0, 1] && taken.events == [0], "only what the watch held")
        let entry = try #require(lib.entry(id))
        #expect(entry.segments.map(\.index) == [2] && entry.dayEvents?.map(\.n) == [1])
        #expect(!hasAudio(lib, id, 0) && !hasAudio(lib, id, 1) && hasAudio(lib, id, 2), "acknowledged audio goes at once")
        let again = ledger.apply(RecordingDayAck(recording: id, seqs: [0, 1], events: [0]))
        #expect(again.seqs.isEmpty && again.events.isEmpty)
        let stranger = ledger.apply(RecordingDayAck(recording: UUID(), seqs: [2], events: [1]))
        #expect(stranger.seqs.isEmpty && stranger.events.isEmpty && lib.entry(id)?.segments.count == 1)
    }

    @Test func aClosedDayGoesOnceThePhoneHasAllOfIt() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let ledger = RecordingDayWatchLedger(library: lib)
        let id = try day(lib)
        try chunk(lib, id, 0, at: 0)
        let midnight = try utc("2027-01-16T00:00:00Z")
        let closed = try #require(lib.closeDay(id, at: midnight))
        #expect(ledger.removeSettled(now: midnight).isEmpty, "its chunk and its closed event are still to go")
        ledger.apply(RecordingDayAck(recording: id, seqs: [0], events: [closed.n]))
        #expect(ledger.removeSettled(now: midnight.addingTimeInterval(60)).isEmpty,
                "an erase of the last minutes on the watch can still reach it")
        let gone = midnight.addingTimeInterval(Outbox.keepClosedSeconds)
        #expect(RecordingDayWatchLedger(library: lib, holding: id).removeSettled(now: gone).isEmpty,
                "never from under the capture")
        #expect(ledger.removeSettled(now: gone) == [id])
        #expect(lib.entry(id) == nil && lib.isDeleted(id))

        // A day the phone erased part of stays a week, to answer the same request again.
        let erased = try day(lib)
        try chunk(lib, erased, 0, at: 0)
        #expect(lib.eraseDayChunks(erased, from: anchor, to: time(5), request: UUID(), now: anchor) == [0])
        let end = try #require(lib.closeDay(erased, at: midnight))
        ledger.apply(RecordingDayAck(recording: erased, events: [end.n]))
        #expect(ledger.removeSettled(now: time(86_400)).isEmpty)
        #expect(ledger.removeSettled(now: time(RecordingDayContract.eraseMemoryDays * 86_400)) == [erased])
    }

    @Test func staleDaysCloseAtTheirOwnMidnightUnlessTheMicrophoneRecordsIntoThem() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let stale = try day(lib, createdAt: time(-50_000), day: "2027-01-14")
        try chunk(lib, stale, 0, at: -40_000)
        let recorded = try day(lib, createdAt: time(-30_000), day: "2027-01-14")
        let current = try day(lib)
        let midnight = try utc("2027-01-15T00:00:00Z")
        let ledger = RecordingDayWatchLedger(library: lib, recordingInto: recorded, holding: recorded)
        #expect(ledger.closeStaleDays(now: anchor) == [stale])
        let entry = try #require(lib.entry(stale))
        #expect(entry.closedAt == midnight && !entry.active && entry.dayEvents == [.closed(0, at: midnight, seqCount: 1)])
        #expect(lib.entry(recorded)?.closedAt == nil, "its writer cuts at the midnight and the capture closes it")
        #expect(lib.entry(current)?.closedAt == nil, "today goes on")
        #expect(ledger.closeStaleDays(now: anchor).isEmpty, "closed once")
        // Paused in it, the capture records nothing into it: it ends now rather than at the next Continue.
        let paused = RecordingDayWatchLedger(library: lib, holding: recorded)
        #expect(paused.closeStaleDays(now: anchor) == [recorded])
        #expect(lib.entry(recorded)?.closedAt == midnight)
    }

    @Test func chunksPastTheAgeLimitAreReportedLost() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let old = try day(lib, createdAt: time(-9 * 86_400), day: "2027-01-06")
        try chunk(lib, old, 0, at: -9 * 86_400)
        try chunk(lib, old, 1, at: -9 * 86_400 + 10)
        let recent = try day(lib, createdAt: time(-7 * 86_400), day: "2027-01-08")
        try chunk(lib, recent, 0, at: -7 * 86_400)
        let ledger = RecordingDayWatchLedger(library: lib)
        let lost = ledger.ageOut(now: anchor, keepDays: 7)
        #expect(lost == [.lost(0, seqs: 0...1, from: time(-9 * 86_400), to: time(-9 * 86_400 + 20))])
        #expect(lib.entry(old)?.segments.isEmpty == true && !hasAudio(lib, old, 0) && !hasAudio(lib, old, 1))
        #expect(lib.entry(old)?.dayEvents == lost, "kept until the phone acknowledges it")
        #expect(lib.entry(recent)?.segments.count == 1 && hasAudio(lib, recent, 0), "seven days after its day: not yet")
        #expect(ledger.ageOut(now: anchor, keepDays: 7).isEmpty, "reported once")
        // A phone keeping transcripts for a day only lets the watch keep chunks a day.
        #expect(ledger.ageOut(now: anchor, keepDays: 1).count == 1 && lib.entry(recent)?.segments.isEmpty == true)
    }
}
