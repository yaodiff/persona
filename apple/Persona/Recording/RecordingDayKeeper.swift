import Foundation

// All-day recording's retention on the phone (docs/assistant/day-recap.md「Retention and privacy」): a day's audio goes at
// the earliest of 3 days after its first final recap, 7 days after its day and `audio_days` after its day, whether or
// not its text reached the hub (the store holds the same deadline for audio that arrives late,
// `RecordingDayRecord.audioDeadline`); the whole day (audio, text, upload state, the library's row) goes once it began
// more than `audio_days` ago, like the hub's phone copies. Forever (`0`) never purges the day. Never reads health data.

/// When a day's audio and the day itself go, from its record alone (unit tested).
nonisolated enum RecordingDayRetention {
    static let day = RecordingDayRecord.secondsPerDay

    /// The seqs whose audio is due at `now`: every chunk still holding audio once the day's deadline passed, transcribed
    /// or not (an untranscribed one's time stays an `untranscribed` gap). `audioDays`: the hub's, nil while unknown;
    /// 0 is forever and still keeps the 7-day maximum.
    static func audioDue(_ record: RecordingDayRecord, now: Date, audioDays: Int?) -> [Int] {
        guard let deadline = record.audioDeadline(audioDays: audioDays), deadline <= now else { return [] }
        return record.chunks.values.filter(\.hasAudio).map(\.seq).sorted()
    }

    /// The day's audio deadline when it is still ahead and audio is left for it, to wake for.
    static func nextDeadline(_ record: RecordingDayRecord, now: Date, audioDays: Int?) -> Date? {
        guard record.chunks.values.contains(where: \.hasAudio),
              let deadline = record.audioDeadline(audioDays: audioDays), deadline > now else { return nil }
        return deadline
    }

    /// Whether a day that began at `start` (`recording_started_at`, how the hub counts its window) is older than `days`
    /// (the hub's `audio_days`): the phone drops its own copies. Forever (`0`) never expires.
    static func isExpired(start: Date, now: Date, days: Int) -> Bool {
        days > 0 && start < now.addingTimeInterval(-Double(days) * day)
    }
}

/// Applies `RecordingDayRetention` to every day the phone holds: soon after every launch (also one in the background to
/// take a chunk, and hourly while chunks keep arriving: `RecordingDayReceiver`), when the app comes to the front, in
/// the catch-up task, at once when a final recap's time becomes known (it can bring a deadline that passed already),
/// and at the next deadline while the app runs.
@MainActor final class RecordingDayKeeper {
    static let shared = RecordingDayKeeper()

    private let store: RecordingDayStore
    private let library: RecordingLibrary
    private let audioDays: () -> Int?
    private let now: () -> Date
    private let forget: (UUID) -> Void
    private let debounce: Duration
    private var running = false
    private var again = false
    /// A run asked for while one was under way, or while one waits for its debounce, goes whatever the last run's time.
    private var forceNext = false
    private var pending: Task<Void, Never>?
    private var timer: Task<Void, Never>?
    private var lastRun: Date?

    /// Runs asked for by triggers are at least this far apart; a deadline or `run(force:)` doesn't wait.
    private static let minimumGap: TimeInterval = 3600
    /// Looked at again at least this often while the app runs.
    private static let maximumGap: TimeInterval = 6 * 3600

    /// - Parameters:
    ///   - audioDays: the hub's `audio_days` once it said it (`RecordingSync`), nil before: until then no day is purged
    ///     for its age (the hub may keep them longer than the default), and the audio's deadline is the other two.
    ///   - forget: a purged day's transcription and upload let go of it, and the watch is asked to delete its copy.
    ///   - debounce: how long triggers in a row gather into one run.
    init(store: RecordingDayStore = .shared, library: RecordingLibrary = .shared,
         audioDays: @escaping () -> Int? = { RecordingSync.shared.store.daysKnown ? RecordingSync.shared.audioRetentionDays : nil },
         now: @escaping () -> Date = { .now },
         forget: @escaping (UUID) -> Void = { RecordingDaySync.shared.removedHere($0) },
         debounce: Duration = .seconds(2)) {
        self.store = store; self.library = library; self.audioDays = audioDays; self.now = now; self.forget = forget
        self.debounce = debounce
    }

    /// A run soon (several triggers in a row share it), unless one ran within the hour; `force`: whatever the last run's
    /// time.
    func schedule(force: Bool = false) {
        if force { forceNext = true }
        guard pending == nil else { return }
        let debounce = debounce
        pending = Task { [weak self] in
            try? await Task.sleep(for: debounce)
            guard let self, !Task.isCancelled else { return }
            self.pending = nil
            let force = self.forceNext
            self.forceNext = false
            await self.run(force: force)
        }
    }

    /// A day's final recap time became known (`RecordingDaySync`): its audio's 3-day clock may have run out already (a
    /// phone that was offline learns it late), so a run goes soon whatever the last one's time, and sets the wake for
    /// the new deadline.
    func deadlineChanged() {
        schedule(force: true)
    }

    /// Deletes the audio that is due and purges the days past `audio_days`. `force`: whatever the last run's time
    /// (the catch-up, a deadline).
    func run(force: Bool = false) async {
        if !force, let last = lastRun, now().timeIntervalSince(last) < Self.minimumGap, last <= now() { return }
        guard !running else {
            again = true
            if force { forceNext = true }
            return
        }
        running = true
        defer {
            running = false
            if again {
                again = false
                schedule(force: forceNext)
            }
        }
        lastRun = now()
        let window = audioDays()
        var soonest: Date?
        let ids = await store.ids()
        for id in ids {
            guard let record = await store.record(id) else { continue }
            if let window, RecordingDayRetention.isExpired(start: record.startedAt, now: now(), days: window) {
                purge(id)
                continue
            }
            let due = RecordingDayRetention.audioDue(record, now: now(), audioDays: window)
            if !due.isEmpty { await store.removeAudio(id, seqs: due) }
            if let next = RecordingDayRetention.nextDeadline(record, now: now(), audioDays: window) {
                soonest = min(soonest ?? next, next)
            }
        }
        // A day row whose chunks are gone (its folder emptied by hand, a crash before the first chunk was written).
        if let window {
            let held = Set(ids)
            for entry in library.entries where entry.isDay && !held.contains(entry.id)
                && RecordingDayRetention.isExpired(start: entry.createdAt, now: now(), days: window) {
                purge(entry.id)
            }
        }
        wake(at: min(soonest ?? .distantFuture, now().addingTimeInterval(Self.maximumGap)))
    }

    /// The whole day goes: its folder (audio, sidecars, text, upload state) with the library's row, tombstoned so a late
    /// chunk never brings it back. The hub trims its own copy.
    private func purge(_ id: UUID) {
        try? library.remove(id)
        forget(id)
    }

    private func wake(at date: Date) {
        timer?.cancel()
        let delay = max(1, date.timeIntervalSince(now()))
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await self?.run(force: true)
        }
    }
}
