import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai

/// A day's summary as `RecordingDaySummaries` computes it, with what each test changes.
private func summary(id: UUID = UUID(), day: String = "2027-01-15", sync: RecordingDaySyncState = .synced, recapState: String? = nil,
                     recorded: Double = 3600, lastAt: Date? = anchor.addingTimeInterval(3600),
                     transcribedTo: Date? = anchor.addingTimeInterval(3600), closedAt: Date? = nil, chunks: Int = 360,
                     waiting: Int = 0, skipped: Int = 0, gaps: [RecordingDayGap] = []) -> RecordingDaySummary {
    RecordingDaySummary(id: id, day: day, timeZone: "Asia/Shanghai", firstAt: anchor, lastAt: lastAt, closedAt: closedAt,
                        recordedSeconds: recorded, transcribedTo: transcribedTo, chunks: chunks, waiting: waiting, skipped: skipped,
                        failed: 0, gaps: gaps, marks: [], sync: sync, recapState: recapState, recapFinal: false, hubIDs: [],
                        complete: false)
}

/// A switch whose consent is a flag of the test's: agreed or not, and what was sent.
@MainActor private final class SwitchProbe {
    var consented: Bool
    var sent: [String] = []
    var agreements = 0
    var now = anchor

    init(consented: Bool = true) { self.consented = consented }

    func make(sleep: @escaping (Date) async throws -> Void = { _ in }) -> RecordingDaySwitch {
        RecordingDaySwitch(send: { self.sent.append($0) }, agreed: { self.consented },
                           agree: { self.consented = true; self.agreements += 1 }, now: { self.now }, sleep: sleep)
    }
}

private func watch(_ state: RecordingDayWatchState.State, since: Date? = anchor, pending: Int? = 0) -> RecordingDayWatchState {
    RecordingDayWatchState(recording: UUID(), day: "2027-01-15", state: state, since: since, next: 12, pending: pending)
}

/// A day row of the library: `day` in `zone`, begun at `createdAt`.
private func dayEntry(_ day: String, zone: String, createdAt: Date, id: UUID = UUID()) -> RecordingEntry {
    var entry = RecordingEntry(id: id, createdAt: createdAt, source: .watch)
    entry.kind = .day
    entry.day = day
    entry.timeZone = zone
    return entry
}

/// What the day board follows, as the app's observable models give it: the summaries, the library's rows and the day the
/// watch records.
@MainActor @Observable private final class DaySource {
    var byID: [UUID: RecordingDaySummary] = [:]
    var entries: [RecordingEntry] = []
    var watching: UUID?
}

/// Set when an observed value changed (`withObservationTracking`'s `onChange`, on the main thread here).
private nonisolated final class Touched: @unchecked Sendable {
    var value = false
}

/// Lets the board's observation hops run: each change reaches it through one main-actor task.
@MainActor private func settle(until done: () -> Bool = { false }) async {
    for _ in 0..<50 {
        if done() { return }
        await Task.yield()
    }
}

/// The All-Day Recording switch, the words of a day's rows and page, and which day is today's (docs/assistant/day-recap.md).
@MainActor struct RecordingDayStatusTests {
    // MARK: The switch

    @Test func theSwitchMirrorsTheWatch() {
        let at = anchor.addingTimeInterval(60)
        #expect(RecordingDayStatus.line(watch: nil, pending: nil, now: at) == .off)
        #expect(!RecordingDayStatus.isOn(watch: nil, pending: nil, now: at), "a watch that never said is off")
        let cases: [(RecordingDayWatchState.State, RecordingDayStatus.Line, Bool)] = [
            (.off, .off, false),
            (.recording, .recording(since: anchor), true),
            (.paused, .paused, true),
            (.interrupted, .interrupted, true),
            (.stopped, .stopped, false),
        ]
        for (state, line, on) in cases {
            #expect(RecordingDayStatus.line(watch: watch(state), pending: nil, now: at) == line, "\(state)")
            #expect(RecordingDayStatus.isOn(watch: watch(state), pending: nil, now: at) == on, "\(state)")
        }
        #expect(RecordingDayStatus.line(watch: watch(.recording, since: nil), pending: nil, now: at) == .recording(since: nil))
    }

    @Test func aStartIsShownOnItsWayUntilTheWatchAnswers() {
        let sent = RecordingDayStatus.Pending(start: true, at: anchor)
        let off = watch(.off)
        #expect(RecordingDayStatus.line(watch: off, pending: sent, now: anchor.addingTimeInterval(5)) == .starting)
        #expect(RecordingDayStatus.isOn(watch: off, pending: sent, now: anchor.addingTimeInterval(5)))
        #expect(RecordingDayStatus.line(watch: off, pending: sent, now: anchor.addingTimeInterval(119)) == .starting)
        // No answer in two minutes: the switch goes back to the watch's state and says so for a while, then lets it go.
        let late = anchor.addingTimeInterval(RecordingDayStatus.answerWait + 1)
        #expect(RecordingDayStatus.line(watch: off, pending: sent, now: late) == .noAnswer)
        #expect(!RecordingDayStatus.isOn(watch: off, pending: sent, now: late))
        let later = late.addingTimeInterval(RecordingDayStatus.noAnswerShown)
        #expect(RecordingDayStatus.line(watch: off, pending: sent, now: later) == .off)
        // The watch started: answered, whenever it comes.
        #expect(RecordingDayStatus.settle(sent, watch: watch(.recording)) == nil)
        #expect(RecordingDayStatus.line(watch: watch(.recording), pending: sent, now: late) == .recording(since: anchor))
        // Paused after a call counts as on: the start was carried out.
        #expect(RecordingDayStatus.settle(sent, watch: watch(.interrupted)) == nil)
    }

    @Test func aStopIsShownOnItsWayUntilTheWatchIsOff() {
        let sent = RecordingDayStatus.Pending(start: false, at: anchor)
        let recording = watch(.recording)
        #expect(RecordingDayStatus.line(watch: recording, pending: sent, now: anchor.addingTimeInterval(1)) == .stopping)
        #expect(!RecordingDayStatus.isOn(watch: recording, pending: sent, now: anchor.addingTimeInterval(1)))
        #expect(RecordingDayStatus.settle(sent, watch: watch(.off)) == nil)
        #expect(RecordingDayStatus.settle(sent, watch: nil) == nil, "a watch that never said is off")
    }

    /// The watch publishes its state again whenever its count of chunks waiting for the phone changes: that is no answer.
    @Test func aStateThatDoesNotMatchLeavesTheCommandWaiting() {
        let stop = RecordingDayStatus.Pending(start: false, at: anchor)
        #expect(RecordingDayStatus.settle(stop, watch: watch(.recording, pending: 3)) == stop)
        #expect(RecordingDayStatus.settle(stop, watch: watch(.paused, pending: 4)) == stop)
        let start = RecordingDayStatus.Pending(start: true, at: anchor)
        #expect(RecordingDayStatus.settle(start, watch: watch(.stopped)) == start)
    }

    @Test func theLineLooksAgainWhenAWaitRunsOut() {
        let sent = RecordingDayStatus.Pending(start: true, at: anchor)
        let off = watch(.off)
        #expect(RecordingDayStatus.nextChange(watch: off, pending: sent, now: anchor) == anchor.addingTimeInterval(RecordingDayStatus.answerWait))
        let noAnswer = anchor.addingTimeInterval(RecordingDayStatus.answerWait + 1)
        #expect(RecordingDayStatus.nextChange(watch: off, pending: sent, now: noAnswer)
                == anchor.addingTimeInterval(RecordingDayStatus.answerWait + RecordingDayStatus.noAnswerShown))
        #expect(RecordingDayStatus.nextChange(watch: off, pending: sent, now: noAnswer.addingTimeInterval(RecordingDayStatus.noAnswerShown)) == nil)
        #expect(RecordingDayStatus.nextChange(watch: watch(.recording), pending: sent, now: anchor) == nil, "answered: nothing to wait for")
    }

    @Test func theSwitchSendsTheCommandAndWaitsForTheWatch() {
        let probe = SwitchProbe()
        let control = probe.make()
        control.set(true)
        #expect(probe.sent == [RecordingDayContract.Command.start])
        #expect(control.pending == RecordingDayStatus.Pending(start: true, at: anchor))
        #expect(control.asking == nil, "agreed already: no sheet")
        control.watchChanged(watch(.off, pending: 2))
        #expect(control.pending != nil, "still off: not answered")
        control.watchChanged(watch(.recording))
        #expect(control.pending == nil)
        control.set(false)
        #expect(probe.sent == [RecordingDayContract.Command.start, RecordingDayContract.Command.stop])
        #expect(control.pending?.start == false)
    }

    // MARK: Consent

    /// Nothing records before the user agreed (docs/assistant/day-recap.md「Retention and privacy」): turning the switch
    /// on asks first, and the start goes only once they agree.
    @Test func aStartBeforeTheConsentAsksFirstAndGoesOnAgree() {
        let probe = SwitchProbe(consented: false)
        let control = probe.make()
        control.set(true)
        #expect(control.asking == .start)
        #expect(probe.sent.isEmpty, "nothing goes to the watch before the user agreed")
        #expect(control.pending == nil)
        #expect(!RecordingDayStatus.isOn(watch: nil, pending: control.pending, now: anchor), "the switch stays off meanwhile")
        control.consentAnswered(agreed: true)
        #expect(control.asking == nil)
        #expect(probe.agreements == 1)
        #expect(probe.sent == [RecordingDayContract.Command.start])
        #expect(control.pending == RecordingDayStatus.Pending(start: true, at: anchor))
        // The sheet's own dismissal after Agree is no second answer.
        control.consentAnswered(agreed: false)
        #expect(probe.agreements == 1 && probe.sent.count == 1)
    }

    @Test func notNowLeavesEverythingAsItWas() {
        let probe = SwitchProbe(consented: false)
        let control = probe.make()
        control.set(true)
        control.consentAnswered(agreed: false)
        #expect(control.asking == nil)
        #expect(probe.agreements == 0 && probe.sent.isEmpty && control.pending == nil)
        // Stopping never asks: a recording already running goes on under the agreement it started with.
        control.set(false)
        #expect(control.asking == nil)
        #expect(probe.sent == [RecordingDayContract.Command.stop])
    }

    /// The Agree row (the watch says "Agree on your iPhone to start"): agreeing starts nothing here, the start is the
    /// watch's tap.
    @Test func theAgreeRowOnlyAgrees() {
        let probe = SwitchProbe(consented: false)
        let control = probe.make()
        control.askConsent()
        #expect(control.asking == .agree)
        control.consentAnswered(agreed: true)
        #expect(probe.agreements == 1)
        #expect(probe.sent.isEmpty)
        #expect(control.pending == nil)
        // Without the sheet up, an answer does nothing.
        control.consentAnswered(agreed: true)
        #expect(probe.agreements == 1)
    }

    /// Deleting the day the watch records stops it there (the delete reaches the watch): the switch shows the stop on
    /// its way without a command of its own, until the watch says it is off.
    @Test func deletingTheRecordedDayShowsTheStopWithoutSendingOne() {
        let probe = SwitchProbe()
        let control = probe.make()
        control.deletedRecordedDay()
        #expect(probe.sent.isEmpty)
        #expect(RecordingDayStatus.line(watch: watch(.recording), pending: control.pending, now: anchor) == .stopping)
        control.watchChanged(watch(.off))
        #expect(control.pending == nil)
    }

    /// The Today section's follow: an unanswered start reads "on its way", then "no answer" after two minutes, then the
    /// watch's state ten minutes later; each change is waited for in turn, and the follow ends when none is left.
    @Test func theSwitchLooksAgainAtEveryDeadlineUntilNoneIsLeft() async {
        var clock = anchor
        var looks: [Date] = []
        var waits: [Date] = []
        let control = RecordingDaySwitch(send: { _ in }, agreed: { true }, agree: {}, now: { clock }, sleep: { until in
            looks.append(clock)
            waits.append(until)
            clock = until
        })
        control.set(true)
        let off = watch(.off)
        await control.follow { off }
        let gaveUp = anchor.addingTimeInterval(RecordingDayStatus.answerWait)
        let letGo = gaveUp.addingTimeInterval(RecordingDayStatus.noAnswerShown)
        #expect(waits == [gaveUp, letGo])
        #expect(looks.map { RecordingDayStatus.line(watch: off, pending: control.pending, now: $0) } == [.starting, .noAnswer])
        #expect(control.lookedAt == letGo)
        #expect(RecordingDayStatus.line(watch: off, pending: control.pending, now: control.lookedAt) == .off,
                "ten minutes on, the line is the watch's again")
        // Entering the section again later starts from the time it is then, with nothing left to wait for.
        clock = letGo.addingTimeInterval(3600)
        await control.follow { off }
        #expect(control.lookedAt == clock)
        #expect(waits.count == 2)
    }

    @Test func theFollowStopsWhenAnsweredOrCancelled() async {
        var waits = 0
        let answered = RecordingDaySwitch(send: { _ in }, agreed: { true }, agree: {}, now: { anchor }, sleep: { _ in waits += 1 })
        answered.set(false)
        await answered.follow { nil }
        #expect(waits == 0, "a watch that never said is off: the stop is done")
        let cancelled = RecordingDaySwitch(send: { _ in }, agreed: { true }, agree: {}, now: { anchor }, sleep: { _ in
            waits += 1
            throw CancellationError()
        })
        cancelled.set(true)
        await cancelled.follow { watch(.off) }
        #expect(waits == 1)
        #expect(cancelled.lookedAt == anchor, "a cancelled wait moves nothing on")
    }

    // MARK: A day's word

    @Test func theRecapLeadsOnceTheHubHasOneUnderWay() {
        #expect(RecordingDayStatus.word(summary(recapState: "ready", waiting: 4)) == .recapReady)
        #expect(RecordingDayStatus.word(summary(recapState: "pending")) == .recapWriting)
        #expect(RecordingDayStatus.word(summary(recapState: "failed")) == .recapFailed)
        // An upload that stopped for good is said first: nothing newer reaches the recap.
        #expect(RecordingDayStatus.word(summary(sync: .problem, recapState: "ready")) == .notAccepted)
        // A state a newer hub knows reads as no recap yet.
        #expect(RecordingDayStatus.word(summary(recapState: "queued")) == .synced)
    }

    @Test func withoutARecapTheWordSaysWhereTheTextIs() {
        let pending = [RecordingDayGap(from: 600, to: 720, reason: RecordingDayGap.Reason.pending)]
        #expect(RecordingDayStatus.word(summary(recorded: 0, chunks: 0)) == .noAudio)
        #expect(RecordingDayStatus.word(summary(recorded: 0, chunks: 0, gaps: pending)) == .waitingForWatch)
        #expect(RecordingDayStatus.word(summary(waiting: 3, gaps: pending)) == .waitingToTranscribe)
        #expect(RecordingDayStatus.word(summary(gaps: pending)) == .waitingForWatch)
        #expect(RecordingDayStatus.word(summary(sync: .notOffered)) == .keptOnPhone)
        #expect(RecordingDayStatus.word(summary(sync: .unsupported)) == .keptOnPhone)
        #expect(RecordingDayStatus.word(summary(sync: .expired)) == .pastRetention)
        #expect(RecordingDayStatus.word(summary(sync: .pending)) == .waitingToSync)
        #expect(RecordingDayStatus.word(summary(chunks: 30, skipped: 30)) == .noSpeech)
        #expect(RecordingDayStatus.word(summary()) == .synced)
    }

    @Test func wordsCarryTheirTone() {
        #expect(RecordingDayStatus.Word.recapReady.tone == .positive)
        #expect(RecordingDayStatus.Word.synced.tone == .positive)
        #expect(RecordingDayStatus.Word.recapWriting.tone == .accent)
        #expect(RecordingDayStatus.Word.recapFailed.tone == .danger)
        #expect(RecordingDayStatus.Word.notAccepted.tone == .danger)
        #expect(RecordingDayStatus.Word.keptOnPhone.tone == .secondary)
        #expect(RecordingDayStatus.Word.waitingToTranscribe.tone == .secondary)
    }

    @Test func progressSaysHowFarTheDayIsTranscribed() {
        #expect(RecordingDayStatus.progress(summary(recorded: 0)) == nil)
        #expect(RecordingDayStatus.progress(summary(recorded: 900, transcribedTo: nil)) == .recorded(seconds: 900))
        let to = anchor.addingTimeInterval(600)
        #expect(RecordingDayStatus.progress(summary(recorded: 900, transcribedTo: to)) == .transcribed(seconds: 900, to: to))
        // From the recording's start to the end of its last audio; nothing before any audio.
        #expect(RecordingDayStatus.span(summary()) == anchor..<anchor.addingTimeInterval(3600))
        #expect(RecordingDayStatus.span(summary(lastAt: nil)) == nil)
        #expect(RecordingDayStatus.span(summary(lastAt: anchor.addingTimeInterval(-5))) == anchor..<anchor)
    }

    /// "Erase Last 10 Minutes" is for today's recording while it records, in the day's own zone.
    @Test func onlyTodaysOpenRecordingOffersTheErase() {
        let at = anchor // 2027-01-15 16:00 in Shanghai
        #expect(RecordingDayStatus.offersErase(summary(), now: at))
        #expect(!RecordingDayStatus.offersErase(summary(closedAt: at.addingTimeInterval(-60)), now: at), "closed: deleted or past midnight")
        #expect(!RecordingDayStatus.offersErase(summary(day: "2027-01-14"), now: at), "yesterday's")
        // 23:59:30 there is still today; 00:00:30 the next day is not.
        let lateEvening = JSON.parseDate("2027-01-15T23:59:30+08:00")!
        #expect(RecordingDayStatus.offersErase(summary(), now: lateEvening))
        #expect(!RecordingDayStatus.offersErase(summary(), now: lateEvening.addingTimeInterval(60)))
    }

    @Test func gapsAddUpByReasonInTheirOrder() {
        let gaps = [
            RecordingDayGap(from: 0, to: 60, reason: "paused"),
            RecordingDayGap(from: 100, to: 400, reason: "interrupted"),
            RecordingDayGap(from: 500, to: 530, reason: "paused"),
            RecordingDayGap(from: 600, to: 600, reason: "erased"),
            RecordingDayGap(from: 700, to: 760, reason: "untranscribed"),
            RecordingDayGap(from: 800, to: 810, reason: "a-newer-reason"),
            RecordingDayGap(from: 900, to: 1000, reason: "lost"),
        ]
        #expect(RecordingDayStatus.gapTotals(gaps) == [
            .init(reason: .paused, seconds: 90),
            .init(reason: .interrupted, seconds: 300),
            .init(reason: .lost, seconds: 100),
            .init(reason: .untranscribed, seconds: 60),
            .init(reason: .other, seconds: 10),
        ])
        #expect(RecordingDayStatus.GapReason("pending") == .pending)
        #expect(RecordingDayStatus.GapReason("stopped") == .stopped)
    }

    // MARK: Today

    @Test func todayIsTheWatchsDayElseTheLatestBegun() {
        let shanghai = "Asia/Shanghai" // anchor is 2027-01-15 16:00 there
        let yesterday = dayEntry("2027-01-14", zone: shanghai, createdAt: anchor.addingTimeInterval(-86_400))
        let morning = dayEntry("2027-01-15", zone: shanghai, createdAt: anchor.addingTimeInterval(-6 * 3600))
        let afternoon = dayEntry("2027-01-15", zone: shanghai, createdAt: anchor.addingTimeInterval(-3600))
        let clip = RecordingEntry(id: UUID(), createdAt: anchor, source: .phone)
        let entries = [clip, afternoon, yesterday, morning]
        #expect(RecordingDayStatus.todayID(entries, watching: nil, now: anchor) == afternoon.id)
        #expect(RecordingDayStatus.todayID(entries, watching: morning.id, now: anchor) == morning.id)
        #expect(RecordingDayStatus.todayID(entries, watching: yesterday.id, now: anchor) == afternoon.id, "the watch's day isn't today's")
        #expect(RecordingDayStatus.todayID([clip, yesterday], watching: nil, now: anchor) == nil)
    }

    /// Each day is today in its own zone, across a DST change: 2026-11-01 in New York has 25 hours, 2026-03-08 has 23.
    @Test func todayFollowsTheDaysOwnZoneAcrossDST() {
        let newYork = "America/New_York"
        let fallBack = dayEntry("2026-11-01", zone: newYork, createdAt: Date(timeIntervalSince1970: 1_793_538_000)) // 2026-11-01 08:00 EST
        // 23:30 EST on Nov 1 (04:30 UTC on Nov 2): still that day there.
        #expect(RecordingDayStatus.todayID([fallBack], watching: nil, now: Date(timeIntervalSince1970: 1_793_593_800)) == fallBack.id)
        // 00:00 EST on Nov 2: the next day.
        #expect(RecordingDayStatus.todayID([fallBack], watching: nil, now: Date(timeIntervalSince1970: 1_793_595_600)) == nil)
        let springForward = dayEntry("2026-03-08", zone: newYork, createdAt: Date(timeIntervalSince1970: 1_772_971_200)) // 08:00 EDT
        // 23:59 EDT on Mar 8 (03:59 UTC on Mar 9).
        #expect(RecordingDayStatus.todayID([springForward], watching: nil, now: Date(timeIntervalSince1970: 1_773_028_740)) == springForward.id)
        #expect(RecordingDayStatus.isToday(day: "2026-03-08", timeZone: newYork, now: Date(timeIntervalSince1970: 1_773_028_740)))
        #expect(!RecordingDayStatus.isToday(day: "2026-03-08", timeZone: newYork, now: Date(timeIntervalSince1970: 1_773_028_800)))
    }

    /// Each day rolls over at its own midnight: the board waits for the first of them.
    @Test func theNextMidnightIsTheFirstOfTheDaysZones() {
        let shanghai = TimeZone(identifier: "Asia/Shanghai")!, newYork = TimeZone(identifier: "America/New_York")!
        // anchor: 2027-01-15 16:00 in Shanghai, 03:00 in New York.
        let shanghaiMidnight = JSON.parseDate("2027-01-16T00:00:00+08:00")!
        #expect(RecordingDayStatus.nextMidnight(after: anchor, zones: [newYork, shanghai]) == shanghaiMidnight)
        #expect(RecordingDayStatus.nextMidnight(after: anchor, zones: [newYork]) == JSON.parseDate("2027-01-16T00:00:00-05:00"))
        // A DST day: 2026-11-01 in New York has 25 hours.
        let fallBack = Date(timeIntervalSince1970: 1_793_538_000) // 2026-11-01 08:00 EST
        #expect(RecordingDayStatus.nextMidnight(after: fallBack, zones: [newYork]) == Date(timeIntervalSince1970: 1_793_595_600))
    }

    // MARK: The Today section's caution

    /// "This hub doesn't write day recaps yet" only once it is about something: a day, the agreement, or the switch on.
    @Test func aStoppedUploadSaysWhy() {
        let generic = RecordingDayStatus.problemText(nil)
        #expect(RecordingDayStatus.problemText("rejected") == generic, "a 400's code has no words of its own")
        let own = ["too_large", "day_full", "not_day"].map(RecordingDayStatus.problemText)
        #expect(Set(own + [generic]).count == 4)
        #expect(RecordingDayStatus.problemText("kind_changed") == RecordingDayStatus.problemText("not_day"))
        for text in own + [generic] { #expect(!text.isEmpty) }
    }

    @Test func theHubCautionWaitsUntilAllDayRecordingIsUsed() {
        #expect(!RecordingDayStatus.showsHubUpdate(offered: false, sync: nil, hasDay: false, agreed: false, on: false),
                "a watch user who never used the mode keeps the page as it was")
        #expect(RecordingDayStatus.showsHubUpdate(offered: false, sync: nil, hasDay: false, agreed: true, on: false))
        #expect(RecordingDayStatus.showsHubUpdate(offered: false, sync: nil, hasDay: false, agreed: false, on: true))
        #expect(RecordingDayStatus.showsHubUpdate(offered: false, sync: .notOffered, hasDay: true, agreed: false, on: false))
        #expect(RecordingDayStatus.showsHubUpdate(offered: true, sync: .unsupported, hasDay: true, agreed: true, on: true),
                "listed, but its manager refused the day routes")
        #expect(!RecordingDayStatus.showsHubUpdate(offered: true, sync: .synced, hasDay: true, agreed: true, on: true))
    }

    // MARK: Clock times

    /// Every clock time of a day is written by one formatter in the zone of the recording it belongs to: a span's ends,
    /// the switch's "since" and the Today row's "transcribed to" read the same for the same moment.
    @Test func everyClockTimeOfADayReadsTheSame() {
        let shanghai = TimeZone(identifier: "Asia/Shanghai")!
        let morning = JSON.parseDate("2027-01-15T09:12:00+08:00")!
        let evening = JSON.parseDate("2027-01-15T19:07:00+08:00")!
        let nine = RecordingDayStatus.clock(morning, in: shanghai)
        #expect(nine.contains("9") && nine.contains("12"))
        #expect(RecordingDayStatus.spanText(morning..<evening, in: shanghai)
                == "\(nine)–\(RecordingDayStatus.clock(evening, in: shanghai))", "the span's start as every other time")
        #expect(RecordingDayStatus.Line.recording(since: morning).text(in: shanghai).contains(nine))
        #expect(RecordingDayStatus.Progress.transcribed(seconds: 600, to: morning).text(in: shanghai).contains(nine))
        // In the recording's zone, not the phone's: 09:12 in Shanghai is 20:12 the day before in New York.
        let newYork = TimeZone(identifier: "America/New_York")!
        #expect(RecordingDayStatus.clock(morning, in: newYork) == RecordingDayStatus.clock(JSON.parseDate("2027-01-15T20:12:00+08:00")!, in: shanghai))
        // A span within one minute is that minute, not "9:12–9:12".
        #expect(RecordingDayStatus.spanText(morning..<morning.addingTimeInterval(30), in: shanghai) == nine)
        // Written today: the clock time; another day: the date too.
        #expect(RecordingDayStatus.writtenText(evening, now: evening.addingTimeInterval(60), in: shanghai)
                == RecordingDayStatus.clock(evening, in: shanghai))
        #expect(RecordingDayStatus.writtenText(evening, now: evening.addingTimeInterval(86_400), in: shanghai)
                != RecordingDayStatus.clock(evening, in: shanghai))
        // The transcript's lines add the seconds.
        let withSeconds = RecordingDayStatus.clock(morning.addingTimeInterval(5), in: shanghai, seconds: true)
        #expect(withSeconds != nine && withSeconds.contains("05"))
    }

    // MARK: The board the views read

    /// A day's cell changes only with that day (P5): today's chunks don't redraw another day's row or page.
    @Test func aDaysCellChangesOnlyWithThatDay() async {
        let past = UUID(), today = UUID()
        let source = DaySource()
        let gaps = [RecordingDayGap(from: 0, to: 60, reason: "paused"), RecordingDayGap(from: 100, to: 400, reason: "interrupted")]
        source.byID = [past: summary(id: past, day: "2027-01-14", gaps: gaps), today: summary(id: today)]
        let board = RecordingDayBoard(summaries: { source.byID }, entries: { source.entries }, watching: { source.watching },
                                      installed: { false }, clock: { anchor }, sleep: { _ in throw CancellationError() })
        let pastCell = board.cell(past), todayCell = board.cell(today)
        #expect(pastCell.summary?.id == past)
        #expect(pastCell.gapTotals == RecordingDayStatus.gapTotals(gaps), "worked out with the summary, not in a body")
        let touched = Touched()
        withObservationTracking { _ = pastCell.summary; _ = pastCell.gapTotals } onChange: { touched.value = true }
        // A chunk of today's recording.
        var next = source.byID[today]!
        next.recordedSeconds += 10
        next.chunks += 1
        source.byID[today] = next
        await settle { todayCell.summary == next }
        #expect(todayCell.summary == next)
        await settle()
        #expect(!touched.value, "today's chunk redrew a past day")
        #expect(board.cell(today) === todayCell, "one cell per day")
        // The past day's own change does reach it.
        source.byID[past] = nil
        await settle { pastCell.summary == nil }
        #expect(touched.value)
        #expect(pastCell.summary == nil && pastCell.gapTotals.isEmpty)
    }

    /// Which day is today's is worked out when the library's rows or the watch's day change, and again at midnight, when
    /// it moves `now` on: yesterday leaves the Today section and its page's Erase goes, with nothing else changing.
    @Test func todayFollowsTheLibraryTheWatchAndMidnight() async {
        let shanghai = "Asia/Shanghai" // anchor is 2027-01-15 16:00 there
        let morning = dayEntry("2027-01-15", zone: shanghai, createdAt: anchor.addingTimeInterval(-6 * 3600))
        let afternoon = dayEntry("2027-01-15", zone: shanghai, createdAt: anchor.addingTimeInterval(-3600))
        let source = DaySource()
        source.entries = [morning]
        var clock = anchor
        var waits: [Date] = []
        let board = RecordingDayBoard(summaries: { source.byID }, entries: { source.entries }, watching: { source.watching },
                                      installed: { false }, clock: { clock }, sleep: { until in
            waits.append(until)
            if waits.count > 1 { throw CancellationError() }
            clock = until
        })
        #expect(board.todayID == morning.id)
        source.entries = [morning, afternoon]
        await settle { board.todayID == afternoon.id }
        #expect(board.todayID == afternoon.id, "a newer day row")
        source.watching = morning.id
        await settle { board.todayID == morning.id }
        #expect(board.todayID == morning.id, "the one the watch records")
        let open = summary(id: morning.id)
        #expect(RecordingDayStatus.offersErase(open, now: board.now))

        await board.followMidnight()
        let midnight = JSON.parseDate("2027-01-16T00:00:00+08:00")!
        #expect(waits == [midnight, JSON.parseDate("2027-01-17T00:00:00+08:00")!])
        #expect(board.now == midnight)
        #expect(board.todayID == nil, "yesterday's day isn't today's after midnight")
        #expect(!RecordingDayStatus.offersErase(open, now: board.now))
        // A page that comes back later finds the midnight already moved: nothing moves twice.
        await board.followMidnight()
        #expect(board.now == midnight)
    }

    /// A day of another zone comes while the page waits for a later midnight: the wait moves to the new day's own
    /// midnight, so that day leaves Today then, not at the old zone's.
    @Test func aDayOfAnotherZoneMovesTheMidnightWaitedFor() async {
        // anchor is 2027-01-15 03:00 in New York, 16:00 in Shanghai: Shanghai's midnight comes 13 hours sooner.
        let newYork = dayEntry("2027-01-15", zone: "America/New_York", createdAt: anchor.addingTimeInterval(-3600))
        let shanghai = dayEntry("2027-01-15", zone: "Asia/Shanghai", createdAt: anchor)
        let newYorkMidnight = JSON.parseDate("2027-01-16T00:00:00-05:00")!
        let shanghaiMidnight = JSON.parseDate("2027-01-16T00:00:00+08:00")!
        let source = DaySource()
        source.entries = [newYork]
        var clock = anchor
        var waits: [Date] = []
        let board = RecordingDayBoard(summaries: { source.byID }, entries: { source.entries }, watching: { source.watching },
                                      installed: { false }, clock: { clock }, sleep: { until in
            waits.append(until)
            switch waits.count {
            case 1: try await Task.sleep(for: .seconds(600)) // woken by the sooner midnight
            case 2: clock = until
            default: throw CancellationError()
            }
        })
        let follow = Task { await board.followMidnight() }
        await settle { waits.count == 1 }
        source.entries = [newYork, shanghai]
        await settle { board.todayID == shanghai.id }
        #expect(board.todayID == shanghai.id)
        for _ in 0..<10 { await settle { waits.count >= 3 } }
        await follow.value
        #expect(waits == [newYorkMidnight, shanghaiMidnight, newYorkMidnight])
        #expect(board.now == shanghaiMidnight)
        #expect(board.todayID == newYork.id, "Shanghai's day left Today at Shanghai's midnight")
    }

    /// Whether a paired watch has Persona isn't observable: the Recordings page looks again, ever less often, until one is.
    @Test func theWatchIsLookedForUntilItHasPersona() async {
        var installed = false
        var waits: [TimeInterval] = []
        let board = RecordingDayBoard(summaries: { [:] }, entries: { [] }, watching: { nil }, installed: { installed },
                                      clock: { anchor }, sleep: { until in
            waits.append(until.timeIntervalSince(anchor))
            if waits.count == 4 { installed = true }
        })
        #expect(!board.watchAppInstalled)
        await board.followWatch()
        #expect(waits == [1, 2, 4, 8])
        #expect(board.watchAppInstalled)
        // A watch that records a day has Persona, whatever the session said.
        let source = DaySource()
        let other = RecordingDayBoard(summaries: { [:] }, entries: { [] }, watching: { source.watching }, installed: { false },
                                      clock: { anchor }, sleep: { _ in })
        source.watching = UUID()
        await settle { other.watchAppInstalled }
        #expect(other.watchAppInstalled)
    }

    /// The day's name is the library's for an untitled day (one test of the localized key, `Day · %@`).
    @Test func aDaysTitleIsItsDateInItsZone() {
        let zone = TimeZone(identifier: "Asia/Shanghai")!
        let start = RecordingDayCalendar.start(of: "2027-01-15", in: zone)!
        let date = start.formatted(Date.FormatStyle(date: .abbreviated, time: .omitted, timeZone: zone))
        #expect(RecordingDayStatus.title(day: "2027-01-15", timeZone: "Asia/Shanghai") == String(localized: "Day · \(date)"))
        var entry = dayEntry("2027-01-15", zone: "Asia/Shanghai", createdAt: anchor)
        entry.title = ""
        #expect(RecordingDayStatus.title(day: "2027-01-15", timeZone: "Asia/Shanghai") == entry.displayTitle)
        #expect(RecordingDayStatus.title(day: "not-a-day", timeZone: "Asia/Shanghai") == "not-a-day")
    }
}


/// What the consent sheet says (docs/assistant/day-recap.md「Retention and privacy」), point by point.
@MainActor struct RecordingDayConsentSheetTests {
    @Test func everyPointOfTheContractIsThereInItsOrder() {
        #expect(RecordingDayConsentPoint.allCases == [.everyoneIsRecorded, .audioStaysOnDevices, .textGoesToTheModel,
                                                      .readersOfKnowledge, .howLongItIsKept])
        let symbols = RecordingDayConsentPoint.allCases.map(\.symbol)
        #expect(Set(symbols).count == symbols.count, "one mark per point")
        for point in RecordingDayConsentPoint.allCases {
            #expect(!point.text(transcriptDays: 14).isEmpty, "\(point)")
        }
    }

    /// The text's retention is the hub's `recording_days` (one test of the localized key).
    @Test func theRetentionPointSaysTheHubsDays() {
        #expect(RecordingDayConsentPoint.howLongItIsKept.text(transcriptDays: 30)
                == String(localized: "The text is kept for \(30) days, and the day's recap as long as meeting minutes, which is forever by default."))
        #expect(RecordingDayConsentPoint.howLongItIsKept.text(transcriptDays: 30) != RecordingDayConsentPoint.howLongItIsKept.text(transcriptDays: 14))
    }
}
