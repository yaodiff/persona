import Foundation
import Observation
import os

// The phone's side of all-day recording (docs/assistant/day-recap.md「Watch to phone」): what it holds of each day
// recording the watch sends, and what the Recordings page reads of it.
//
// A day lives in the library's folder for its id (`<root>/<id>/`), next to the library's small index row
// (`recording.json`, `RecordingLibrary.adoptDay`), so deleting the recording deletes all of it:
// - `seg-NNN.m4a`: a chunk's audio, until an erase or retention deletes it;
// - `seg-NNN.json`: the chunk's header as it came (`RecordingDaySidecar`), written before the chunk is acknowledged,
//   also for a chunk settled without keeping its audio (inside an erased range, or past the day's audio deadline);
// - `events.log`: the watch's control events, one JSON envelope per line, each written before it is acknowledged;
// - `erases.json`: the ranges erased on this phone, written before the erase is carried out;
// - `day.json`: the whole `RecordingDayRecord` (the events applied, transcription, upload state), written by the
//   store's queue at most every couple of seconds after an edit, and on `flush`, never on the main actor; a chunk or
//   event that only arrived (its sidecar or `events.log` holds it) waits for the day's next write, at most ten minutes;
// - `journal.log`: what was written since `day.json` was (a seq whose sidecar, `events`, `erases`), so a recovery knows
//   what to merge in without trusting file dates; emptied by each write of `day.json`.
// The sidecars, the event log and the erases are the durable record: `day.json` is rebuilt from them (without its
// transcript) when it is missing or unreadable. The erases and deletes the phone asks the watch for, and its erases over a
// stretch of time for days still to come, wait in the recordings folder's `day-requests.json`, which outlives a deleted
// day's folder.
// Foundation and Observation only, so the Mac build compiles it too (the receiver that feeds it is iOS and visionOS
// only).

// MARK: - What the phone holds of a day

/// Where an appended sentence sits on the hub: its part and its position (the hub's `segments` order). A position is
/// assigned once, persisted before any request carries it, and changed only by a `PUT`.
nonisolated struct RecordingDayPosition: Codable, Sendable, Hashable {
    var part: Int
    var pos: Int
}

/// One chunk of a day recording as the phone holds it: the watch's header, when it first arrived, and how far
/// transcription and the upload got.
nonisolated struct RecordingDayChunk: Sendable, Equatable {
    /// Transcription: `waiting` until it runs, then `transcribed`, `skipped` (quieter than `silenceDBFS`) or `failed`
    /// (after `maxAttempts` tries). An erased or lost chunk needs none, whatever it says (`needsTranscription`).
    nonisolated enum State: String, Codable, Sendable {
        case waiting, transcribed, skipped, failed

        /// A state a newer build wrote reads as `waiting`: transcribing it again loses nothing.
        init(from decoder: Decoder) throws {
            self = State(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .waiting
        }
    }

    var seq: Int
    /// The wall clock of the chunk's first sample.
    var startedAt: Date
    /// Seconds of audio.
    var duration: Double
    /// Mean RMS level in dBFS (`RecordingDayLevel`); nil when the watch didn't know it.
    var level: Double? = nil
    /// When the phone first stored it: how long a seq before it has been missing, and the latency the loopback test reads.
    var receivedAt: Date
    var state: State = .waiting
    /// Transcription runs that failed on this chunk (`maxAttempts`).
    var attempts = 0
    /// The text, at seconds from `startedAt`; nil until transcribed. A sentence an erase reached keeps its place here
    /// with no text when it has a position (`positions`), and is gone when it has none.
    var sentences: [RecordingSentence]? = nil
    /// `seg-NNN.m4a` is (or may still be) on disk. An erased or lost chunk keeps it set only until the file is gone.
    var hasAudio = false
    /// Where each of the first `positions.count` sentences went on the hub, in order; nil until the upload or the
    /// transcriber assigns them. Never more than `sentences`.
    var positions: [RecordingDayPosition]? = nil
    /// An erase reached it: its audio is deleted and it is never transcribed (docs/assistant/day-recap.md「Erasing」).
    var erased = false
    /// It came after the day's audio deadline: acknowledged and settled as lost, its audio never kept
    /// (docs/assistant/day-recap.md「Retention and privacy」).
    var lost = false
}

/// One hub recording of a day: the first part has the watch's id, a day too long for one recording continues in more
/// (docs/assistant/day-recap.md「Parts」). Kept and changed by the upload (`RecordingDaySync`), except `needsRepair`,
/// which an erase sets.
nonisolated struct RecordingDayPart: Sendable, Equatable {
    var hubID: UUID
    var part: Int
    /// The day's clock (`recording_started_at`): every part keeps part 1's.
    var startedAt: Date
    /// When the part was closed early (`413 day_full`), or the day recording ended.
    var endedAt: Date? = nil
    /// The create `PUT` was stored.
    var created = false
    /// The revision of the last `PUT`, which appends name.
    var base: Int64? = nil
    /// Positions below this are on the hub; positions below `count` are assigned.
    var next = 0
    var count = 0
    /// Every request's revision is above this.
    var maxSentRevision: Int64 = 0
    /// The hub's stored revision, as its last answer said.
    var hubRevision: Int64? = nil
    /// A repair `PUT` decided on (its revision), sent again after a relaunch until stored.
    var pendingPut: Int64? = nil
    /// An erase took text from a sentence of this part that has a position, which the hub may hold: only a `PUT`
    /// (keeping every other position, an empty segment in place of each erased one) takes it out there. The upload
    /// clears it in the same update that decides that `PUT` (`pendingPut`), so an erase after that sets it again.
    var needsRepair = false
    var complete = false
    /// The hub keeps no text of it any more (`200 expired`).
    var expired = false
    /// Deleted on the hub (`410`, or reconciliation's `deleted`).
    var deleted = false
    /// The hub refused this part (`400`): nothing goes to it until something changes here or a new `hello` comes.
    var problem: String? = nil
    /// Reconciliation's `recap_state`, `recap_final` and `recap_final_at` (when its first final recap was written: the
    /// audio's 3-day clock).
    var recapState: String? = nil
    var recapFinal = false
    var recapFinalAt: Date? = nil
}

/// A stretch the microphone was closed: from a `paused` event to the next `started`, or to the end of the recording.
nonisolated struct RecordingDayPause: Codable, Sendable, Equatable {
    var from: Date
    /// nil while it lasts.
    var to: Date?
    /// `RecordingDayContract.Reason`, or a reason this build doesn't know, kept as it came.
    var reason: String
}

/// Chunks the watch reported it could not keep (a `lost` event), with their times when it knew them.
nonisolated struct RecordingDayLoss: Codable, Sendable, Equatable {
    var fromSeq: Int
    var toSeq: Int
    var from: Date?
    var to: Date?

    var seqs: ClosedRange<Int> { fromSeq...max(fromSeq, toSeq) }
}

/// A wall-clock range erased from the day: by the watch (an `erase` event) or on this phone (`requestErase`).
nonisolated struct RecordingDayErasure: Codable, Sendable, Equatable {
    /// The erase's id; nil from an event that carried none.
    var id: UUID?
    var from: Date
    var to: Date
}

/// A bookmark or a meeting mark [ext] (`mark` event), at its wall-clock moment. `label` is
/// `RecordingDayEvent.Label.bookmark` or `.meeting`.
nonisolated struct RecordingDayMark: Codable, Sendable, Equatable {
    var at: Date
    var label: String
}

/// Everything the phone holds of one day recording (`day.json`).
nonisolated struct RecordingDayRecord: Sendable, Equatable {
    var id: UUID
    /// The local date, `YYYY-MM-DD`, in the recording's own time zone (the header's `tz`).
    var day: String
    var timeZone: String
    /// When the watch began recording the day (`recording_started_at`): the clock of every part on the hub.
    var startedAt: Date
    var chunks: [Int: RecordingDayChunk] = [:]
    /// Seqs the watch reported lost (`lost` events) that the phone doesn't hold.
    var lost: Set<Int> = []
    /// The `lost` events as reported, for the times of their gaps.
    var losses: [RecordingDayLoss] = []
    /// The erased ranges, kept for the whole day: a chunk that arrives inside one is dropped.
    var erasures: [RecordingDayErasure] = []
    /// Seqs an erase deleted on the watch before the phone had them (an `erase` event's or an answer's `seqs`): settled.
    var erasedSeqs: Set<Int> = []
    /// When the microphone was closed and why, from the `paused` and `started` events.
    var pauses: [RecordingDayPause] = []
    var marks: [RecordingDayMark] = []
    /// The `closed` event: when the recording ended, and that no chunk at or after `seqCount` will come.
    var closedAt: Date? = nil
    var seqCount: Int? = nil
    /// Events held until the ones before them arrive, by `n`; every `n` below `appliedEvents` has been applied.
    var events: [Int: RecordingDayEvent] = [:]
    var appliedEvents = 0
    var parts: [RecordingDayPart] = []
    /// Why the upload stopped for good (`409 not_day`…), for the day's page.
    var problem: String? = nil
    /// The hub refused the day routes (an older manager): nothing is sent until its next `hello`.
    var unsupportedUntilHello = false
}

// MARK: - Taking in what the watch sends

nonisolated extension RecordingDayChunk {
    /// A chunk as it first arrives.
    init(header: RecordingDayHeader, receivedAt: Date) {
        self.init(seq: header.seq, startedAt: header.startedAt, duration: header.duration, level: header.level,
                  receivedAt: receivedAt)
    }

    var endedAt: Date { startedAt.addingTimeInterval(duration.isFinite ? max(0, duration) : 0) }
    /// No audio and no transcription belong to it any more.
    var isErasedOrLost: Bool { erased || lost }
    /// Audio the transcriber still has to read.
    var needsTranscription: Bool { !isErasedOrLost && hasAudio && state == .waiting }
    /// Its text is final: transcribed (a silent chunk counts, recorded silence is no gap), or none belongs to it (erased,
    /// lost). One the phone failed to transcribe is not: its words never reached the hub.
    var hasSettledText: Bool { isErasedOrLost || state == .transcribed || state == .skipped }
    /// Sentences without a hub position yet: text the hub doesn't have, whatever the parts' counters say.
    var hasUnplacedText: Bool { (positions?.count ?? 0) < (sentences?.count ?? 0) }

    /// Whether the chunk holds audio from the range: half-open, like the watch's (`RecordingSegment.overlapsDay`).
    func overlaps(_ from: Date, _ to: Date) -> Bool {
        RecordingDayRecord.spans(startedAt, endedAt, overlap: from, to)
    }

    /// A sentence's wall-clock span: `startedAt` plus its chunk-relative times (an unreadable time is the chunk's start).
    func span(of sentence: RecordingSentence) -> (start: Date, end: Date) {
        let start = sentence.start.isFinite ? sentence.start : 0
        let end = sentence.end.isFinite ? max(start, sentence.end) : start
        return (startedAt.addingTimeInterval(start), startedAt.addingTimeInterval(end))
    }

    /// Deletes the text said between `from` and `to`: a sentence with a position keeps it, with no text (the hub's
    /// positions must not move), any other goes. Returns the parts whose placed text changed.
    mutating func dropText(from: Date, to: Date) -> Set<Int> {
        guard let held = sentences, !held.isEmpty else { return [] }
        let placed = positions?.count ?? 0
        var kept: [RecordingSentence] = []
        var repaired: Set<Int> = []
        for (index, sentence) in held.enumerated() {
            let span = span(of: sentence)
            guard RecordingDayRecord.spans(span.start, span.end, overlap: from, to) else {
                kept.append(sentence)
                continue
            }
            // Placed sentences are a prefix (`positions` pairs with `sentences` in order), so dropping only later ones
            // keeps every pair.
            guard index < placed else { continue }
            if !sentence.text.isEmpty, let part = positions?[index].part { repaired.insert(part) }
            kept.append(RecordingSentence(start: sentence.start, end: sentence.end, text: ""))
        }
        if kept != held { sentences = kept }
        return repaired
    }

    /// Settled without its audio (`RecordingDaySidecar.Settled`).
    mutating func settle(_ settled: RecordingDaySidecar.Settled) {
        switch settled {
        case .erased: erased = true
        case .lost: lost = true
        }
        hasAudio = false
    }
}

nonisolated extension RecordingDayRecord {
    /// A day as its first chunk names it.
    init(header: RecordingDayHeader) {
        self.init(id: header.recording, day: header.day, timeZone: header.tz, startedAt: header.recordingStartedAt)
    }

    /// A day as its first event names it (an event can come before any chunk).
    init(envelope: RecordingDayEventEnvelope) {
        self.init(id: envelope.recording, day: envelope.day, timeZone: envelope.tz, startedAt: envelope.recordingStartedAt)
    }

    /// Puts a chunk in: it is held now, neither lost nor missing.
    mutating func put(_ chunk: RecordingDayChunk, recordingStartedAt: Date) {
        chunks[chunk.seq] = chunk
        lost.remove(chunk.seq)
        erasedSeqs.remove(chunk.seq)
        // The same in every chunk; the earliest wins should a watch ever disagree with itself.
        startedAt = min(startedAt, recordingStartedAt)
    }

    /// How the phone settles a chunk it doesn't hold, before keeping anything of it: one an erase reached is dropped,
    /// one after the day's audio deadline is lost; nil to keep it.
    func settlement(for chunk: RecordingDayChunk, now: Date, recordingDays: Int?) -> RecordingDaySidecar.Settled? {
        if erasedSeqs.contains(chunk.seq) || erasures.contains(where: { chunk.overlaps($0.from, $0.to) }) { return .erased }
        if let deadline = audioDeadline(recordingDays: recordingDays), now >= deadline { return .lost }
        return nil
    }

    /// Whether `start…end` (a point when they are equal) overlaps `from…to`: half-open for a span, so one that ends
    /// where the range begins holds none of it.
    static func spans(_ start: Date, _ end: Date, overlap from: Date, _ to: Date) -> Bool {
        let end = max(start, end)
        return start == end ? (from <= start && start <= to) : (start < to && from < end)
    }

    // MARK: Control events

    /// Keeps an event the watch sent until it can be applied; false when it was had already (applied, or held).
    mutating func hold(_ event: RecordingDayEvent) -> Bool {
        guard event.n >= appliedEvents, events[event.n] == nil else { return false }
        events[event.n] = event
        return true
    }

    /// Applies the held events that come next, strictly in `n` order and each once; a later one waits for the ones
    /// before it (docs/assistant/day-recap.md「Control events」). Returns the seqs whose audio an erase deleted.
    mutating func applyHeldEvents() -> Set<Int> {
        var gone = Set<Int>()
        while let event = events.removeValue(forKey: appliedEvents) {
            gone.formUnion(apply(event))
            appliedEvents += 1
        }
        return gone
    }

    /// One event. A type this build doesn't know, or a known one missing what it needs, is applied as nothing: holding it
    /// back would hold back every later event.
    private mutating func apply(_ event: RecordingDayEvent) -> Set<Int> {
        switch event.type {
        case .started:
            if let at = event.at { endPause(at: at) }
        case .paused:
            if let at = event.at { beginPause(at: at, reason: event.reason ?? RecordingDayContract.Reason.paused) }
        case .lost:
            recordLoss(event)
        case .erase:
            return erase(id: event.id, from: event.from, to: event.to, seqs: event.erasedSeqs)
        case .closed:
            close(at: event.at, seqCount: event.seqCount)
        case .mark:
            if let at = event.at { marks.append(RecordingDayMark(at: at, label: event.label ?? RecordingDayEvent.Label.bookmark)) }
        default:
            break
        }
        return []
    }

    /// The microphone closed; a second `paused` while closed changes nothing (the first says when and why).
    private mutating func beginPause(at: Date, reason: String) {
        guard pauses.last.map({ $0.to != nil }) ?? true else { return }
        pauses.append(RecordingDayPause(from: at, to: nil, reason: reason))
    }

    /// The microphone opened again (or the recording ended): the open pause ends.
    private mutating func endPause(at: Date) {
        guard let last = pauses.indices.last, pauses[last].to == nil else { return }
        pauses[last].to = max(at, pauses[last].from)
    }

    private mutating func recordLoss(_ event: RecordingDayEvent) {
        guard let seqs = event.lostSeqs else { return }
        losses.append(RecordingDayLoss(fromSeq: seqs.lowerBound, toSeq: seqs.upperBound, from: event.from, to: event.to))
        // A seq the phone holds stays: its copy is the chunk (seqs are never reused), and deleting it would turn a
        // delivery hiccup into lost audio and text. The bound keeps a malformed range from counting forever.
        for seq in RecordingDayRanges.expand([[seqs.lowerBound, seqs.upperBound]]) where !isAccounted(seq) {
            lost.insert(seq)
        }
    }

    /// The recording ended (the first `closed` says when); an open pause ends with it.
    private mutating func close(at: Date?, seqCount: Int?) {
        let end = closedAt ?? at ?? dayEnd ?? startedAt
        closedAt = end
        if let seqCount { self.seqCount = seqCount }
        endPause(at: end)
    }

    // MARK: Erasing

    /// Erases a wall-clock range and settles seqs the watch erased (docs/assistant/day-recap.md「Erasing」): the range is
    /// kept for the day, every chunk overlapping it loses its audio and is never transcribed, and every sentence
    /// overlapping it loses its text (a placed one keeps its position with no text, and its part needs a repair `PUT`).
    /// A seq in `seqs` the phone holds is erased too; one it doesn't is settled. Returns the seqs whose audio file must
    /// go. Applying the same erase again changes nothing.
    @discardableResult
    mutating func erase(id: UUID?, from: Date?, to: Date?, seqs: some Sequence<Int>) -> Set<Int> {
        var gone = Set<Int>()
        if let from, let to, from <= to {
            let erasure = RecordingDayErasure(id: id, from: from, to: to)
            if !erasures.contains(erasure) { erasures.append(erasure) }
            for seq in Array(chunks.keys) {
                guard var chunk = chunks[seq] else { continue }
                if chunk.overlaps(from, to) { chunk.erased = true }
                needRepair(chunk.dropText(from: from, to: to))
                if chunk.erased, chunk.hasAudio { gone.insert(seq) }
                chunks[seq] = chunk
            }
        }
        for seq in seqs {
            guard var chunk = chunks[seq] else {
                erasedSeqs.insert(seq)
                lost.remove(seq)
                continue
            }
            chunk.erased = true
            if chunk.hasAudio { gone.insert(seq) }
            chunks[seq] = chunk
        }
        return gone
    }

    private mutating func needRepair(_ partNumbers: Set<Int>) {
        guard !partNumbers.isEmpty else { return }
        for i in parts.indices where partNumbers.contains(parts[i].part) { parts[i].needsRepair = true }
    }

    /// An erase is final whatever an edit writes: a transcription that started before it and saves its text after it
    /// gets none of the erased text in, an erased or settled chunk stays so (and its seq accounted for, even when an
    /// edit dropped it), and no erased range or settled seq is forgotten.
    mutating func keepSettled(from held: RecordingDayRecord) {
        // Most edits come to a day nothing was erased from: nothing to keep then.
        guard !held.erasures.isEmpty || !held.erasedSeqs.isEmpty || held.chunks.values.contains(where: \.isErasedOrLost) else { return }
        for erasure in held.erasures where !erasures.contains(erasure) { erasures.append(erasure) }
        erasedSeqs.formUnion(held.erasedSeqs.subtracting(chunks.keys))
        var changed: [Int] = []
        for (seq, before) in held.chunks {
            var chunk = chunks[seq] ?? before
            if before.erased { chunk.erased = true }
            if before.lost { chunk.lost = true }
            if before.isErasedOrLost { chunk.hasAudio = chunk.hasAudio && before.hasAudio }
            if chunk != before { changed.append(seq) }
            chunks[seq] = chunk
        }
        changed += chunks.keys.filter { held.chunks[$0] == nil }
        for seq in changed {
            guard var chunk = chunks[seq], chunk.sentences != nil else { continue }
            for erasure in erasures { _ = chunk.dropText(from: erasure.from, to: erasure.to) }
            chunks[seq] = chunk
        }
    }

    // MARK: What the day is

    /// The recording's time zone; nil for an identifier this device doesn't know.
    var zone: TimeZone? { RecordingDayWire.zone(timeZone) }

    /// The midnight that ends the recording's day, in its own zone (a 23- or 25-hour day ends where it does).
    var dayEnd: Date? { zone.flatMap { RecordingDayCalendar.end(of: day, in: $0) } }

    /// The span the recording covers: from its start to its end, or to its day's midnight while open.
    var span: DateInterval? {
        guard let end = closedAt ?? dayEnd, end >= startedAt else { return nil }
        return DateInterval(start: startedAt, end: end)
    }

    /// When the day's final recap was written: once every part the hub keeps has one, the latest of them (the phone's
    /// audio serves them all). nil until then.
    var recapFinalAt: Date? {
        let kept = parts.filter { !$0.deleted && !$0.expired }
        guard !kept.isEmpty, kept.allSatisfy({ $0.recapFinalAt != nil }) else { return nil }
        return kept.compactMap(\.recapFinalAt).max()
    }

    /// The phone's deadline for the day's audio (docs/assistant/day-recap.md「Retention and privacy」): the earliest of
    /// 3 days after its final recap, 7 days after its day, and the hub's `recording_days` after its day (each counted
    /// from the day's midnight in its zone). Audio is deleted then, and a chunk that arrives later is settled as lost.
    /// nil while none can be told (a date this device can't read and no final recap).
    func audioDeadline(recordingDays: Int?) -> Date? {
        var deadlines: [Date] = []
        if let end = dayEnd {
            deadlines.append(end.addingTimeInterval(RecordingDayContract.audioMaxDays * Self.secondsPerDay))
            if let recordingDays { deadlines.append(end.addingTimeInterval(Double(recordingDays) * Self.secondsPerDay)) }
        }
        if let recap = recapFinalAt {
            deadlines.append(recap.addingTimeInterval(RecordingDayContract.audioDaysAfterRecap * Self.secondsPerDay))
        }
        return deadlines.min()
    }

    static let secondsPerDay: TimeInterval = 86_400

    /// The seqs the day is known to have: `seq_count` once closed, else one past the highest seq the phone heard of.
    var knownSeqEnd: Int {
        seqCount ?? (max(chunks.keys.max() ?? -1, lost.max() ?? -1, erasedSeqs.max() ?? -1) + 1)
    }

    /// Whether a seq is accounted for: held, reported lost, or erased before the phone had it.
    func isAccounted(_ seq: Int) -> Bool {
        chunks[seq] != nil || lost.contains(seq) || erasedSeqs.contains(seq)
    }

    /// Seqs below `knownSeqEnd` the phone neither holds nor has settled: the watch still has them (a `pending` gap).
    var missingSeqs: [Int] { (0..<knownSeqEnd).filter { !isAccounted($0) } }

    /// Sentence positions still held in a part: below the part's `count`, a hole is a position whose sentence went.
    func positionsHeld(in part: Int) -> Int {
        chunks.values.reduce(0) { sum, chunk in sum + (chunk.positions ?? []).count(where: { $0.part == part }) }
    }

    /// The hub's copy of the text differs from the phone's, or soon will: the day isn't created there, a part has a
    /// repair waiting or positions assigned but not sent, sentences have no position yet, or a position lost its
    /// sentence. Parts the hub keeps no text of, or refused, are left out: nothing more goes to them.
    var hasUnsentText: Bool {
        guard !parts.isEmpty else { return !chunks.isEmpty || closedAt != nil }
        let live = parts.filter { !$0.expired && !$0.deleted && $0.problem == nil }
        guard !live.isEmpty else { return false }
        if live.contains(where: { !$0.created || $0.pendingPut != nil || $0.needsRepair || $0.next < $0.count }) { return true }
        if chunks.values.contains(where: \.hasUnplacedText) { return true }
        return live.contains { positionsHeld(in: $0.part) < $0.count }
    }

    /// The day recording ended, every seq below its count is held, lost or erased, every chunk held is transcribed (or
    /// erased or lost), and the hub has all the text (docs/assistant/day-recap.md「The recording」). A chunk the phone
    /// could not transcribe keeps the day from being complete: the hub's cutoff final recap covers such a day.
    var isComplete: Bool {
        guard closedAt != nil, seqCount != nil, missingSeqs.isEmpty else { return false }
        return chunks.values.allSatisfy(\.hasSettledText) && !hasUnsentText
    }

    /// The recap's state across the day's parts: failed when one failed, else pending while one is being written, else
    /// ready; nil before any is due. An unknown state of the hub's is shown as it is.
    var recapState: String? {
        let states = parts.filter { !$0.deleted }.compactMap(\.recapState)
        for state in ["failed", "pending", "ready"] where states.contains(state) { return state }
        return states.last
    }

    /// Every part the hub keeps has a final recap ("Final recap" rather than "So far").
    var recapFinal: Bool {
        let kept = parts.filter { !$0.deleted && !$0.expired }
        return !kept.isEmpty && kept.allSatisfy(\.recapFinal)
    }

    /// The hub recordings of the day, in part order.
    var hubIDs: [UUID] { parts.filter { !$0.deleted }.sorted { $0.part < $1.part }.map(\.hubID) }
}

// MARK: - day.json

// Hand-written so that a field a newer build adds, or one missing from an older file, never makes the whole day
// unreadable: a day that can't be read is rebuilt from its sidecars and events, without its transcript.

nonisolated extension RecordingDayChunk: Codable {
    private nonisolated enum CodingKeys: String, CodingKey {
        case seq, startedAt, duration, level, receivedAt, state, attempts, sentences, hasAudio, positions, erased, lost
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(seq: try c.decode(Int.self, forKey: .seq),
                  startedAt: try c.decode(Date.self, forKey: .startedAt),
                  duration: try c.decode(Double.self, forKey: .duration),
                  level: try? c.decodeIfPresent(Double.self, forKey: .level),
                  receivedAt: (try? c.decodeIfPresent(Date.self, forKey: .receivedAt)) ?? .distantPast,
                  state: (try? c.decodeIfPresent(State.self, forKey: .state)) ?? .waiting,
                  attempts: (try? c.decodeIfPresent(Int.self, forKey: .attempts)) ?? 0,
                  sentences: try c.decodeIfPresent([RecordingSentence].self, forKey: .sentences),
                  hasAudio: (try? c.decodeIfPresent(Bool.self, forKey: .hasAudio)) ?? false,
                  positions: try? c.decodeIfPresent([RecordingDayPosition].self, forKey: .positions),
                  erased: (try? c.decodeIfPresent(Bool.self, forKey: .erased)) ?? false,
                  lost: (try? c.decodeIfPresent(Bool.self, forKey: .lost)) ?? false)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(seq, forKey: .seq)
        try c.encode(startedAt, forKey: .startedAt)
        try c.encode(duration, forKey: .duration)
        try c.encodeIfPresent(level.flatMap { $0.isFinite ? $0 : nil }, forKey: .level)
        try c.encode(receivedAt, forKey: .receivedAt)
        try c.encode(state, forKey: .state)
        try c.encode(attempts, forKey: .attempts)
        try c.encodeIfPresent(sentences, forKey: .sentences)
        try c.encode(hasAudio, forKey: .hasAudio)
        try c.encodeIfPresent(positions, forKey: .positions)
        try c.encode(erased, forKey: .erased)
        try c.encode(lost, forKey: .lost)
    }
}

nonisolated extension RecordingDayPart: Codable {
    private nonisolated enum CodingKeys: String, CodingKey {
        case hubID, part, startedAt, endedAt, created, base, next, count, maxSentRevision, hubRevision, pendingPut, needsRepair
        case complete, expired, deleted, problem, recapState, recapFinal, recapFinalAt
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(hubID: try c.decode(UUID.self, forKey: .hubID),
                  part: try c.decode(Int.self, forKey: .part),
                  startedAt: try c.decode(Date.self, forKey: .startedAt),
                  endedAt: try c.decodeIfPresent(Date.self, forKey: .endedAt),
                  created: try c.decodeIfPresent(Bool.self, forKey: .created) ?? false,
                  base: try c.decodeIfPresent(Int64.self, forKey: .base),
                  next: try c.decodeIfPresent(Int.self, forKey: .next) ?? 0,
                  count: try c.decodeIfPresent(Int.self, forKey: .count) ?? 0,
                  maxSentRevision: try c.decodeIfPresent(Int64.self, forKey: .maxSentRevision) ?? 0,
                  hubRevision: try c.decodeIfPresent(Int64.self, forKey: .hubRevision),
                  pendingPut: try c.decodeIfPresent(Int64.self, forKey: .pendingPut),
                  needsRepair: try c.decodeIfPresent(Bool.self, forKey: .needsRepair) ?? false,
                  complete: try c.decodeIfPresent(Bool.self, forKey: .complete) ?? false,
                  expired: try c.decodeIfPresent(Bool.self, forKey: .expired) ?? false,
                  deleted: try c.decodeIfPresent(Bool.self, forKey: .deleted) ?? false,
                  problem: try? c.decodeIfPresent(String.self, forKey: .problem),
                  recapState: try? c.decodeIfPresent(String.self, forKey: .recapState),
                  recapFinal: (try? c.decodeIfPresent(Bool.self, forKey: .recapFinal)) ?? false,
                  recapFinalAt: try? c.decodeIfPresent(Date.self, forKey: .recapFinalAt))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(hubID, forKey: .hubID)
        try c.encode(part, forKey: .part)
        try c.encode(startedAt, forKey: .startedAt)
        try c.encodeIfPresent(endedAt, forKey: .endedAt)
        try c.encode(created, forKey: .created)
        try c.encodeIfPresent(base, forKey: .base)
        try c.encode(next, forKey: .next)
        try c.encode(count, forKey: .count)
        try c.encode(maxSentRevision, forKey: .maxSentRevision)
        try c.encodeIfPresent(hubRevision, forKey: .hubRevision)
        try c.encodeIfPresent(pendingPut, forKey: .pendingPut)
        try c.encode(needsRepair, forKey: .needsRepair)
        try c.encode(complete, forKey: .complete)
        try c.encode(expired, forKey: .expired)
        try c.encode(deleted, forKey: .deleted)
        try c.encodeIfPresent(problem, forKey: .problem)
        try c.encodeIfPresent(recapState, forKey: .recapState)
        try c.encode(recapFinal, forKey: .recapFinal)
        try c.encodeIfPresent(recapFinalAt, forKey: .recapFinalAt)
    }
}

nonisolated extension RecordingDayRecord: Codable {
    /// The layout of `day.json` this build writes: 2 since the control events (1 was the draft's chunk markers).
    static let fileVersion = 2

    private nonisolated enum CodingKeys: String, CodingKey {
        case version, id, day, timeZone, startedAt, chunks, lost, losses, erasures, erasedSeqs, pauses, marks, closedAt
        case seqCount, events, appliedEvents, parts, problem, unsupportedUntilHello
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // Chunks and held events are lists on disk (a dictionary keyed by Int would be one flat array of keys and values).
        let chunks = try c.decodeIfPresent([RecordingDayChunk].self, forKey: .chunks) ?? []
        let events = (try? c.decodeIfPresent([RecordingDayEvent].self, forKey: .events)) ?? []
        let startedAt = try c.decodeIfPresent(Date.self, forKey: .startedAt) ?? chunks.map(\.startedAt).min()
        self.init(id: try c.decode(UUID.self, forKey: .id),
                  day: try c.decode(String.self, forKey: .day),
                  timeZone: try c.decode(String.self, forKey: .timeZone),
                  startedAt: startedAt ?? Date(timeIntervalSince1970: 0),
                  chunks: Dictionary(chunks.map { ($0.seq, $0) }, uniquingKeysWith: { _, last in last }),
                  lost: Set((try? c.decodeIfPresent([Int].self, forKey: .lost)) ?? []),
                  losses: (try? c.decodeIfPresent([RecordingDayLoss].self, forKey: .losses)) ?? [],
                  erasures: try c.decodeIfPresent([RecordingDayErasure].self, forKey: .erasures) ?? [],
                  erasedSeqs: Set(try c.decodeIfPresent([Int].self, forKey: .erasedSeqs) ?? []),
                  pauses: (try? c.decodeIfPresent([RecordingDayPause].self, forKey: .pauses)) ?? [],
                  marks: (try? c.decodeIfPresent([RecordingDayMark].self, forKey: .marks)) ?? [],
                  closedAt: try? c.decodeIfPresent(Date.self, forKey: .closedAt),
                  seqCount: try? c.decodeIfPresent(Int.self, forKey: .seqCount),
                  events: Dictionary(events.map { ($0.n, $0) }, uniquingKeysWith: { _, last in last }),
                  appliedEvents: (try? c.decodeIfPresent(Int.self, forKey: .appliedEvents)) ?? 0,
                  parts: try c.decodeIfPresent([RecordingDayPart].self, forKey: .parts) ?? [],
                  problem: try? c.decodeIfPresent(String.self, forKey: .problem),
                  unsupportedUntilHello: (try? c.decodeIfPresent(Bool.self, forKey: .unsupportedUntilHello)) ?? false)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(Self.fileVersion, forKey: .version)
        try c.encode(id, forKey: .id)
        try c.encode(day, forKey: .day)
        try c.encode(timeZone, forKey: .timeZone)
        try c.encode(startedAt, forKey: .startedAt)
        try c.encode(chunks.values.sorted { $0.seq < $1.seq }, forKey: .chunks)
        try c.encode(lost.sorted(), forKey: .lost)
        try c.encode(losses, forKey: .losses)
        try c.encode(erasures, forKey: .erasures)
        try c.encode(erasedSeqs.sorted(), forKey: .erasedSeqs)
        try c.encode(pauses, forKey: .pauses)
        try c.encode(marks, forKey: .marks)
        try c.encodeIfPresent(closedAt, forKey: .closedAt)
        try c.encodeIfPresent(seqCount, forKey: .seqCount)
        try c.encode(events.values.sorted { $0.n < $1.n }, forKey: .events)
        try c.encode(appliedEvents, forKey: .appliedEvents)
        try c.encode(parts, forKey: .parts)
        try c.encodeIfPresent(problem, forKey: .problem)
        try c.encode(unsupportedUntilHello, forKey: .unsupportedUntilHello)
    }
}

/// What a day's span needs from `day.json`, read without decoding the day's chunks and text.
private nonisolated struct RecordingDayHead: Decodable {
    var id: UUID
    var day: String
    var timeZone: String
    var startedAt: Date
    var closedAt: Date?

    var span: DateInterval? {
        var record = RecordingDayRecord(id: id, day: day, timeZone: timeZone, startedAt: startedAt)
        record.closedAt = closedAt
        return record.span
    }
}

/// A chunk's durable record on the phone (`seg-NNN.json`, beside its audio), written before the chunk is acknowledged:
/// its header as it came, when it first arrived, and whether the phone settled it without keeping its audio.
nonisolated struct RecordingDaySidecar: Sendable, Equatable {
    /// Why a chunk was acknowledged without its audio being kept.
    nonisolated enum Settled: String, Codable, Sendable {
        /// Inside an erased range, or a seq an erase deleted: dropped (erase comes before deduplication).
        case erased
        /// After the day's audio deadline: discarded and counted as lost.
        case lost
    }

    var header: RecordingDayHeader
    var receivedAt: Date
    var settled: Settled? = nil
}

nonisolated extension RecordingDaySidecar: Codable {
    private nonisolated enum CodingKeys: String, CodingKey {
        case header, settled
        case receivedAt = "received_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(header: try c.decode(RecordingDayHeader.self, forKey: .header),
                  receivedAt: Date(timeIntervalSince1970: try c.decode(Double.self, forKey: .receivedAt)),
                  settled: try? c.decodeIfPresent(Settled.self, forKey: .settled))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(header, forKey: .header)
        try c.encode(receivedAt.timeIntervalSince1970, forKey: .receivedAt)
        try c.encodeIfPresent(settled, forKey: .settled)
    }
}

// MARK: - Requests to the watch

/// The erases and deletes the phone asked the watch for that the watch has not confirmed yet
/// (docs/assistant/day-recap.md「Erasing」「Deleting」): sent again at launch and whenever the watch shows up, until its
/// answer comes or the watch can hold nothing of that day any more.
nonisolated struct RecordingDayWatchRequests: Codable, Sendable, Equatable {
    nonisolated struct Erase: Codable, Sendable, Equatable {
        var recording: UUID
        var id: UUID
        var from: Date
        var to: Date
        var until: Date
    }

    nonisolated struct Delete: Codable, Sendable, Equatable {
        var recording: UUID
        var until: Date
    }

    /// An erase over a stretch of time (`RecordingDayStore.requestErase(from:to:)`), kept for the day recordings the phone
    /// has nothing of yet: one the watch began at midnight whose first chunk is still on its way gets it when that chunk
    /// or an event of it arrives, here and as an `Erase` to the watch (docs/assistant/day-recap.md「Erasing」: an erase
    /// that spans midnight applies to both day recordings).
    nonisolated struct RangeErase: Codable, Sendable, Equatable {
        var id: UUID
        var from: Date
        var to: Date
        var until: Date
        /// Every day the phone held when it was asked has it; nil until then, so a launch after a crash in between
        /// erases them.
        var done: Bool? = nil
    }

    /// What to send now.
    nonisolated struct Pending: Sendable, Equatable {
        var erases: [RecordingDayEraseRequest] = []
        var deletes: [RecordingDayDelete] = []
    }

    var erases: [Erase] = []
    var deletes: [Delete] = []
    /// nil in a file from before.
    var ranges: [RangeErase]? = nil
}

nonisolated extension RecordingDayWatchRequests {
    /// A request is never older than its day, and the watch keeps nothing of a day longer than `maxKeepDays` after it
    /// (`recordingDayKeepDays`): past this a request has nothing left to reach.
    static let lifetime = Double(RecordingDayContract.maxKeepDays + 1) * RecordingDayRecord.secondsPerDay

    /// Drops the requests that expired; whether any did.
    mutating func prune(now: Date) -> Bool {
        let before = (erases.count, deletes.count, ranges?.count ?? 0)
        erases.removeAll { $0.until <= now }
        deletes.removeAll { $0.until <= now }
        ranges?.removeAll { $0.until <= now }
        if ranges?.isEmpty == true { ranges = nil }
        return before != (erases.count, deletes.count, ranges?.count ?? 0)
    }

    var pending: Pending {
        Pending(erases: erases.map { RecordingDayEraseRequest(recording: $0.recording, id: $0.id, from: $0.from, to: $0.to) },
                deletes: deletes.map { RecordingDayDelete(recording: $0.recording) })
    }
}

/// Hands every value to each stream taken from it (each `stream` is a new subscription), from any thread. Bursts are
/// not coalesced here.
nonisolated final class RecordingDayBroadcast<Element: Sendable>: Sendable {
    private let subscribers = OSAllocatedUnfairLock(initialState: [UUID: AsyncStream<Element>.Continuation]())

    var stream: AsyncStream<Element> {
        let (stream, continuation) = AsyncStream.makeStream(of: Element.self)
        let key = UUID()
        subscribers.withLock { $0[key] = continuation }
        continuation.onTermination = { @Sendable [weak self] _ in
            self?.subscribers.withLock { $0[key] = nil }
        }
        return stream
    }

    func yield(_ value: Element) {
        for continuation in subscribers.withLock({ Array($0.values) }) { continuation.yield(value) }
    }
}

// MARK: - The store

/// The phone's all-day chunks and events and everything known about them, behind one serial queue: the receiver's
/// stores, the transcriber's and the upload's edits, erases, retention and deletion all happen in one order, so nothing
/// older ever lands after something newer, and nothing written for a day recreates it after the user deleted it.
///
/// The receiver's calls are synchronous, from WatchConnectivity's queue: a chunk or an event is on disk when they
/// return, and only then acknowledged. Everyone else awaits. Never call the synchronous ones on the main actor.
nonisolated final class RecordingDayStore: @unchecked Sendable {
    nonisolated enum StoreResult: Sendable, Equatable {
        /// A seq the phone didn't hold, with its audio.
        case stored
        /// A seq the phone didn't hold, settled without keeping its audio: inside an erased range (or erased on the
        /// watch), or after the day's audio deadline (lost).
        case settled
        /// Already held: acknowledged again, stored once.
        case duplicate
        /// The day was deleted here: acknowledged, so the watch lets go of it, and nothing kept.
        case refused

        /// The day changed.
        var changed: Bool { self == .stored || self == .settled }
    }

    nonisolated enum EventResult: Sendable, Equatable {
        /// Applied, with any later ones it was holding.
        case applied
        /// Kept until the events before it arrive.
        case held
        /// Applied or held already: acknowledged again, applied once.
        case duplicate
        /// The day was deleted here: acknowledged and dropped.
        case refused

        /// The day changed.
        var changed: Bool { self == .applied || self == .held }
    }

    nonisolated enum Failure: Error, Equatable {
        /// Not a header the watch could have written.
        case invalidHeader
        /// A chunk that should carry audio came without it.
        case missingAudio
        /// Not an event the watch could have written.
        case invalidEvent
    }

    static let shared = RecordingDayStore(root: RecordingLibrary.directory, isDeleted: { id in
        RecordingDayStore.appLibrary.withLock { $0 }?.isDeletedSnapshot(id) ?? false
    })

    /// The app library whose tombstones `shared` checks. It is the main actor's, so `shared` can't reach it by itself;
    /// `RecordingDayReceiver.start()` hands it over at launch, before WatchConnectivity can deliver anything.
    private static let appLibrary = OSAllocatedUnfairLock<RecordingLibrary?>(initialState: nil)

    @MainActor static func useTombstones(of library: RecordingLibrary) {
        appLibrary.withLock { $0 = library }
    }

    let root: URL
    private let isDeleted: @Sendable (UUID) -> Bool
    private let now: @Sendable () -> Date
    private let writeDelay: TimeInterval
    private let arrivalWriteDelay: TimeInterval
    private let queue = DispatchQueue(label: "persona.recording-day-store", qos: .utility)
    private let dayChanges = RecordingDayBroadcast<UUID>()
    private let requestChanges = RecordingDayBroadcast<Void>()
    /// The hub's `recording_days` once known (`useRecordingDays`), for the audio deadline.
    private let recordingDays = OSAllocatedUnfairLock<Int?>(initialState: nil)

    // Only touched on `queue`.
    private var cache: [UUID: RecordingDayRecord] = [:]
    private var lastUse: [UUID: Int] = [:]
    private var uses = 0
    private var dirty: Set<UUID> = []
    private var writeScheduled = false
    private var arrivalWriteScheduled = false
    /// When a refused chunk or event last asked the watch to delete its day (`refuse`), per day.
    private var refusals: [UUID: Date] = [:]
    /// Days this process has brought up to their folder (`reconcile`): every file it writes for them afterwards stays in
    /// `cache` until a later `day.json` holds it, so from then on `day.json` alone is current.
    private var reconciled: Set<UUID> = []
    /// Every folder was brought up to date once (`sweep`): from then on a day without `day.json` is one this process
    /// holds in `cache`.
    private var swept = false
    /// Days whose folder held more than their `day.json` (or had none), not yet returned by `recoverOnLaunch`.
    private var unreported: Set<UUID> = []
    /// `day-requests.json`, once read.
    private var requests: RecordingDayWatchRequests?
    /// `requests` is what `day-requests.json` holds; false after a write of it failed, until one succeeds.
    private var requestsSaved = true
    /// Days erased here while their request to the watch couldn't be written: their journal keeps `asksEntry` through
    /// the day's writes until `day-requests.json` is written, so a launch after the app ended asks the watch again.
    private var unasked: Set<UUID> = []

    /// Days kept decoded at once (today's, the one transcribing, the one uploading, the one on screen); a day is a few
    /// MB of text, and fourteen are kept.
    private static let cacheLimit = 4
    private static let dayFile = "day.json"
    private static let eventsFile = "events.log"
    private static let erasesFile = "erases.json"
    private static let unreadableFile = "day-unreadable.json"
    private static let journalFile = "journal.log"
    private static let requestsFile = "day-requests.json"
    /// When the audio's retention pass last ran (`claimRetentionPass`), across launches.
    private static let retentionFile = "day-retention.json"
    /// The journal's lines for an append to `events.log` and a write of `erases.json` (the others are seqs).
    private static let eventsEntry = "events"
    private static let erasesEntry = "erases"
    /// The journal's line for an erase whose request to the watch is not on disk (`unasked`).
    private static let asksEntry = "asks"
    /// How much earlier than `day.json` a file may be dated and still be merged in as possibly newer. The journal is
    /// what recovery relies on; file dates only add to it, for a journal line a crash of the whole system lost.
    private static let clockSlack: TimeInterval = 5
    /// A chunk or an event of a day deleted here asks the watch again to delete that day at most this often.
    static let refusalInterval: TimeInterval = 60
    /// Chunks arrive while the phone is locked in a pocket, and are transcribed then: every file of a day stays readable
    /// and writable once the phone was unlocked after a restart, whatever the app's default protection becomes.
    private static let writing: Data.WritingOptions = [.atomic, .completeFileProtectionUntilFirstUserAuthentication]

    /// - Parameters:
    ///   - root: the recordings folder (the library's).
    ///   - isDeleted: the library's tombstones, readable from any thread (`RecordingLibrary.isDeletedSnapshot`).
    ///   - writeDelay: how long changes gather before `day.json` is written.
    ///   - arrivalWriteDelay: the longest a chunk or an event that a sidecar or `events.log` already holds waits for
    ///     `day.json` (a chunk arrives every ten seconds all day, and rewriting a day of text for each adds nothing a
    ///     recovery doesn't already get from the journal); any other write of the day, `flush` and `flushAll` take it
    ///     sooner.
    init(root: URL, isDeleted: @escaping @Sendable (UUID) -> Bool, now: @escaping @Sendable () -> Date = { .now },
         writeDelay: TimeInterval = 2, arrivalWriteDelay: TimeInterval = 600) {
        self.root = root
        self.isDeleted = isDeleted
        self.now = now
        self.writeDelay = writeDelay
        self.arrivalWriteDelay = max(writeDelay, arrivalWriteDelay)
    }

    func folder(_ id: UUID) -> URL { root.appendingPathComponent(id.uuidString, isDirectory: true) }
    func audioURL(_ id: UUID, seq: Int) -> URL { folder(id).appendingPathComponent(RecordingSegment.fileName(seq)) }
    private func sidecarURL(_ id: UUID, seq: Int) -> URL { folder(id).appendingPathComponent(String(format: "seg-%03d.json", seq)) }
    private func dayURL(_ id: UUID) -> URL { folder(id).appendingPathComponent(Self.dayFile) }
    private func eventsURL(_ id: UUID) -> URL { folder(id).appendingPathComponent(Self.eventsFile) }
    private func erasesURL(_ id: UUID) -> URL { folder(id).appendingPathComponent(Self.erasesFile) }
    private func journalURL(_ id: UUID) -> URL { folder(id).appendingPathComponent(Self.journalFile) }
    private var requestsURL: URL { root.appendingPathComponent(Self.requestsFile) }

    /// The hub's `recording_days` (nil until known): a day's audio deadline counts it. Callable from any thread.
    func useRecordingDays(_ days: Int?) {
        recordingDays.withLock { $0 = days }
    }

    // MARK: Receiver side (synchronous, any queue but the main one)

    /// A chunk from `sendMessageData` (`audio`: the envelope's audio part).
    func store(header: RecordingDayHeader, audio: Data?) throws -> StoreResult {
        try queue.sync { try commit(header, audio: audio.map(Audio.data)) }
    }

    /// A chunk from `transferFile`: the file is moved in before this returns (the system deletes it right after).
    func store(header: RecordingDayHeader, movingFile: URL) throws -> StoreResult {
        try queue.sync { try commit(header, audio: .file(movingFile)) }
    }

    /// A control event: on disk (`events.log`) before this returns, applied in order with the ones before it.
    func store(event envelope: RecordingDayEventEnvelope) throws -> EventResult {
        try queue.sync { try commitEvent(envelope) }
    }

    /// Whether the audio's retention pass (`RecordingDayKeeper`) may run now, counted across launches: it reads every day
    /// the phone holds, and WatchConnectivity can relaunch the app in the background several times an hour. Says yes at
    /// most once per `interval`, and records `now` when it does.
    func claimRetentionPass(now: Date, every interval: TimeInterval) async -> Bool {
        await onQueue {
            let url = self.root.appendingPathComponent(Self.retentionFile)
            if let data = try? Data(contentsOf: url),
               let last = try? JSONDecoder().decode(Date.self, from: data),
               now >= last, now.timeIntervalSince(last) < interval {
                return false
            }
            try? JSONEncoder().encode(now).write(to: url, options: .atomic)
            return true
        }
    }

    #if DEBUG
    /// Screenshots and UI tests (`-showDaySample`): forgets what an earlier run asked the watch about the made-up days
    /// `ids` and every erase over a stretch of time, which there is no watch to confirm. Otherwise an erase made by an
    /// earlier run (kept `RecordingDayWatchRequests.lifetime`) would erase the same day built again at those hours.
    /// `ranges`: also every erase over a stretch of time (only while a sample is being built: they belong to no day, so a
    /// real one kept for a day still to come would go too).
    func forgetSampleRequests(_ ids: Set<UUID>, ranges: Bool) async {
        await onQueue {
            self.updateRequests { requests in
                requests.erases.removeAll { ids.contains($0.recording) }
                requests.deletes.removeAll { ids.contains($0.recording) }
                if ranges { requests.ranges = nil }
            }
        }
    }
    #endif

    // MARK: Everyone else

    /// The day as the phone holds it; nil for a day it doesn't have, or one deleted.
    func record(_ id: UUID) async -> RecordingDayRecord? {
        await onQueue { self.forgetIfDeleted(id) ? nil : self.load(id) }
    }

    /// Changes a day (the transcriber's text, the upload's state), on the store's queue against its newest state. An edit
    /// that changes nothing writes and announces nothing; a day the phone doesn't hold (or deleted) is left alone. An
    /// erase stays final whatever the edit writes (`keepSettled`). Written with the next coalesced write; `flush` when it
    /// must be on disk before something else happens.
    func update(_ id: UUID, _ edit: @escaping @Sendable (inout RecordingDayRecord) -> Void) async {
        await onQueue { self.apply(edit, to: id) }
    }

    /// Writes the day's `day.json` now if it changed since the last write, and returns once it is on disk: true then (or
    /// when there was nothing to write), false when the write failed or the day is gone. The upload sends positions only
    /// after this said true.
    @discardableResult func flush(_ id: UUID) async -> Bool {
        await onQueue { self.write(id) }
    }

    /// Writes every day changed since its last write (the app is about to be suspended).
    func flushAll() async {
        await onQueue { self.writeDirty() }
    }

    /// Deletes these chunks' audio (retention); their records, text and sidecars stay. A chunk keeps `hasAudio` until its
    /// file is really gone, so a deletion that fails is tried again by the next retention pass. True when every file is
    /// gone.
    @discardableResult func removeAudio(_ id: UUID, seqs: [Int]) async -> Bool {
        await onQueue { self.dropAudio(id, seqs: seqs) }
    }

    /// Every day the phone holds, deleted ones left out. The first call in a process brings every folder up to date first
    /// (what `recoverOnLaunch` does), so a day a crash left without `day.json` is listed even before the launch's
    /// recovery ran (a background task that starts before it).
    func ids() async -> [UUID] {
        await onQueue {
            if !self.swept { self.sweep() }
            return self.dayIDs()
        }
    }

    /// At launch: brings each day's record up to its folder. A crash can leave chunks and events that were stored and
    /// acknowledged only in their sidecars and `events.log` (no `day.json` yet, or an older one), audio whose sidecar was
    /// never written (never acknowledged: the watch sends it again), or audio an erase didn't get to delete. A day
    /// something reads first (a chunk WatchConnectivity delivers before this runs) is brought up to its folder by that
    /// read, and reported here too. Returns the days that changed and weren't returned before; their `day.json` is
    /// written before this returns.
    func recoverOnLaunch() async -> [UUID] {
        await onQueue { self.recoverAll() }
    }

    /// The library deleted this day (`RecordingLibrary.remove`, which tombstones it first): forgets it now and tells
    /// everyone following `changes`, so its summary, transcription and upload let go of it without waiting for their next
    /// read. Callable from any thread. The watch's copy is `requestWatchDelete`'s.
    func didRemove(_ id: UUID) {
        queue.async {
            _ = self.forgetIfDeleted(id)
            self.dayChanges.yield(id)
        }
    }

    /// Lets go of every decoded day that is on disk as it is (a memory warning); each is read again when next needed.
    /// Callable from any thread.
    func trimMemory() {
        queue.async {
            for id in Array(self.cache.keys) where !self.dirty.contains(id) {
                self.cache[id] = nil
                self.lastUse[id] = nil
            }
        }
    }

    /// The id of each day that changed (a chunk or event stored, an edit, an erase, a recovery, a removal), for as long as
    /// the stream is iterated. Each access is a new subscription, so the transcriber, the upload and the summaries each
    /// get every change.
    var changes: AsyncStream<UUID> { dayChanges.stream }

    // MARK: Erasing and deleting on the phone

    /// Erases a wall-clock range of a day here and on the watch (docs/assistant/day-recap.md「Erasing」): the range is
    /// kept, the audio and sentences overlapping it go now (`RecordingDayRecord.erase`; the upload's repair `PUT`
    /// follows `needsRepair`), and the watch is asked to erase its copies until it confirms (`watchRequests`). True once
    /// the erase and the request to the watch are both on disk; false for a day the phone doesn't hold, an empty range,
    /// or a failed write. The request is taken as soon as `erases.json` holds the erase, before the erase is carried out
    /// and `day.json` written, so what fails after it never loses it: it is still sent while the app runs and written
    /// again with every later look at the requests, and a launch after a crash in between asks it again from
    /// `erases.json` (`merge`). An erase over midnight goes to every day it touches (`requestErase(from:to:)`), with the
    /// same `requestID`.
    func requestErase(_ id: UUID, from: Date, to: Date, requestID: UUID = UUID()) async -> Bool {
        await onQueue { self.commitErase(id, from: from, to: to, requestID: requestID) }
    }

    /// Erases a wall-clock range from every day it touches, as one erase with one id (docs/assistant/day-recap.md
    /// 「Erasing」: an erase that spans midnight applies to both day recordings). The days are the ones the store holds
    /// now, so a day whose first chunk only just came is among them; the range is also kept (`day-requests.json`) for a
    /// day the phone has nothing of yet, which gets it when its first chunk or event arrives (`eraseRanges`). Returns
    /// the days `requestErase` erased now, in the order they began.
    func requestErase(from: Date, to: Date, requestID: UUID = UUID()) async -> [UUID] {
        await onQueue { self.commitErase(from: from, to: to, requestID: requestID) }
    }

    /// The watch carried out an erase the phone asked for: the chunks it deleted before the phone had them are settled,
    /// and the request is done.
    func watchErased(_ answer: RecordingDayErased) async {
        await onQueue { self.commitWatchErased(answer) }
    }

    /// Asks the watch to delete its copy of a day the phone deleted (docs/assistant/day-recap.md「Deleting」), until it
    /// confirms. Kept outside the day's folder, so it outlives the delete; any erase still waiting for that day is
    /// dropped. True once the request is on disk; when it can't be written it is still sent while the app runs and
    /// written again with every later look at the requests.
    @discardableResult func requestWatchDelete(_ id: UUID) async -> Bool {
        await onQueue { self.commitWatchDelete(id) }
    }

    /// The watch deleted its copy.
    func watchDeleted(_ answer: RecordingDayDeleted) async {
        await onQueue { _ = self.updateRequests { $0.deletes.removeAll { $0.recording == answer.recording } } }
    }

    /// What the watch still has to confirm, to send again (expired requests dropped). Requests a write failed for are
    /// written again first.
    func watchRequests() async -> RecordingDayWatchRequests.Pending {
        await onQueue { self.currentRequests().pending }
    }

    /// A request was added: the receiver sends it at once.
    var watchRequestChanges: AsyncStream<Void> { requestChanges.stream }

    // MARK: On the queue

    private nonisolated enum Audio {
        case data(Data)
        case file(URL)

        var isEmpty: Bool {
            if case .data(let data) = self { return data.isEmpty }
            return false
        }
    }

    private func onQueue<T: Sendable>(_ work: @escaping @Sendable () -> T) async -> T {
        await withCheckedContinuation { continuation in
            queue.async { continuation.resume(returning: work()) }
        }
    }

    private func commit(_ header: RecordingDayHeader, audio: Audio?) throws -> StoreResult {
        guard header.valid else { throw Failure.invalidHeader }
        let id = header.recording
        // A deleted recording's chunk is acknowledged and dropped (delete comes before deduplication).
        guard !forgetIfDeleted(id) else {
            refuse(id)
            return .refused
        }
        var record = load(id) ?? RecordingDayRecord(header: header)
        // An erase over a stretch of time this day's first chunk came after (erase comes before deduplication too).
        try eraseRanges(&record)
        // Held already: any erase since was applied to the held copy, and audio is never taken twice (retention may have
        // deleted it on purpose).
        guard record.chunks[header.seq] == nil else { return .duplicate }
        var chunk = RecordingDayChunk(header: header, receivedAt: now())
        let settled = record.settlement(for: chunk, now: chunk.receivedAt, recordingDays: recordingDays.withLock { $0 })
        if let settled {
            chunk.settle(settled)
        } else {
            // The audio first: a sidecar is never without its audio. A crash in between leaves audio without a sidecar,
            // which was never acknowledged; the resend replaces it, and recovery deletes it.
            guard let audio, !audio.isEmpty else { throw Failure.missingAudio }
            try makeFolder(id)
            try put(audio, at: audioURL(id, seq: header.seq))
            chunk.hasAudio = true
        }
        try writeSidecar(RecordingDaySidecar(header: header, receivedAt: chunk.receivedAt, settled: settled), id)
        record.put(chunk, recordingStartedAt: header.recordingStartedAt)
        // A delete that landed while this wrote: the library removed the folder (or is about to), and what was just
        // written must not bring the day back.
        guard !removeIfDeleted(id) else {
            refuse(id)
            return .refused
        }
        // The sidecar holds it: `day.json` takes it with the day's next write.
        keep(record, durable: true)
        return settled == nil ? .stored : .settled
    }

    private func commitEvent(_ envelope: RecordingDayEventEnvelope) throws -> EventResult {
        guard envelope.valid else { throw Failure.invalidEvent }
        let id = envelope.recording
        guard !forgetIfDeleted(id) else {
            refuse(id)
            return .refused
        }
        var record = load(id) ?? RecordingDayRecord(envelope: envelope)
        try eraseRanges(&record)
        guard record.hold(envelope.event) else {
            // Sent again because its acknowledgement never came: an erase whose audio deletion failed tries once more.
            if envelope.event.type == .erase { try purgeAudio(id) }
            return .duplicate
        }
        try makeFolder(id)
        try note(Self.eventsEntry, in: id)
        try appendLine(envelope.data, to: eventsURL(id))
        let applied = record.appliedEvents
        let gone = record.applyHeldEvents()
        guard !removeIfDeleted(id) else {
            refuse(id)
            return .refused
        }
        // `events.log` holds it; an erase (its text gone, its audio about to be) is written soon all the same.
        keep(record, durable: envelope.event.type != .erase && gone.isEmpty)
        // Last, once the record holds the erase, so its text is gone whatever happens to the files. An event is
        // acknowledged only with its audio deleted: when that fails this throws, and the copy the watch sends again tries
        // once more (`purgeAudio`).
        try deleteAudio(id, seqs: gone)
        return record.appliedEvents > applied ? .applied : .held
    }

    private func commitErase(_ id: UUID, from: Date, to: Date, requestID: UUID) -> Bool {
        guard from <= to, from.timeIntervalSince1970.isFinite, to.timeIntervalSince1970.isFinite,
              !forgetIfDeleted(id), var record = load(id) else { return false }
        return (try? eraseHere(&record, RecordingDayErasure(id: requestID, from: from, to: to))) ?? false
    }

    /// Erases a range from a day here and asks the watch to erase it there (`requestErase`), in the order that keeps
    /// each step whatever fails after it: `erases.json` first, so a recovery always erases it again; then the request
    /// to the watch, before anything else can fail, so it waits for the watch even when the day's own write fails or
    /// the app ends; then the erase itself, written to `day.json` at once (the user asked for it). Throws when
    /// `erases.json` could not be written (nothing was done then); returns whether the request and the day are on disk.
    /// `record` is the day as the store now holds it.
    private func eraseHere(_ record: inout RecordingDayRecord, _ erasure: RecordingDayErasure) throws -> Bool {
        let id = record.id
        let until = now().addingTimeInterval(RecordingDayWatchRequests.lifetime)
        guard !record.erasures.contains(erasure) else { return askWatchToErase(id, erasure, until: until) }
        var erases = readLocalErases(id)
        if !erases.contains(erasure) { erases.append(erasure) }
        try makeFolder(id)
        try note(Self.erasesEntry, in: id)
        try JSONEncoder().encode(erases).write(to: erasesURL(id), options: Self.writing)
        let asked = askWatchToErase(id, erasure, until: until)
        if !asked {
            // Only in memory: the journal says so through the day's writes, until the request is written.
            unasked.insert(id)
            try? note(Self.asksEntry, in: id)
        }
        let gone = record.erase(id: erasure.id, from: erasure.from, to: erasure.to, seqs: [])
        guard !removeIfDeleted(id) else { return false }
        keep(record)
        guard write(id) else { return false }
        // A file that can't go now goes with the next write of the day (`purgeLeftoverAudio`).
        try? deleteAudio(id, seqs: gone)
        if let held = cache[id] { record = held }
        return asked
    }

    /// Asks the watch to erase a range of a day until it confirms (`watchRequests`); whether the request is on disk.
    @discardableResult private func askWatchToErase(_ id: UUID, _ erasure: RecordingDayErasure, until: Date) -> Bool {
        guard let requestID = erasure.id else { return false }
        return updateRequests { requests in
            guard !requests.erases.contains(where: { $0.recording == id && $0.id == requestID }) else { return }
            requests.erases.append(.init(recording: id, id: requestID, from: erasure.from, to: erasure.to, until: until))
        }
    }

    /// `requestErase(from:to:)`: the days are found and erased in one turn of the queue, so no chunk lands in between.
    /// The range is kept first (`RecordingDayWatchRequests.RangeErase`), for a day that turns up later and for a launch
    /// after a crash before every day held had it.
    private func commitErase(from: Date, to: Date, requestID: UUID) -> [UUID] {
        guard from <= to, from.timeIntervalSince1970.isFinite, to.timeIntervalSince1970.isFinite else { return [] }
        // A day a crash left without day.json is one too.
        if !swept { sweep() }
        let until = now().addingTimeInterval(RecordingDayWatchRequests.lifetime)
        updateRequests { requests in
            guard !(requests.ranges ?? []).contains(where: { $0.id == requestID }) else { return }
            let range = RecordingDayWatchRequests.RangeErase(id: requestID, from: from, to: to, until: until)
            requests.ranges = (requests.ranges ?? []) + [range]
        }
        let touched = heldDays(overlapping: from, to)
        let erased = touched.filter { commitErase($0, from: from, to: to, requestID: requestID) }
        if erased.count == touched.count { rangeDone(requestID) }
        return erased
    }

    /// The days the store holds whose span overlaps `from…to`, in the order they began.
    private func heldDays(overlapping from: Date, _ to: Date) -> [UUID] {
        var touched: [(id: UUID, start: Date)] = []
        for id in dayIDs() {
            guard let span = span(of: id), span.start < to, from < span.end else { continue }
            touched.append((id, span.start))
        }
        return touched.sorted { ($0.start, $0.id.uuidString) < ($1.start, $1.id.uuidString) }.map(\.id)
    }

    private func rangeDone(_ requestID: UUID) {
        updateRequests { requests in
            guard let i = requests.ranges?.firstIndex(where: { $0.id == requestID }) else { return }
            requests.ranges?[i].done = true
        }
    }

    /// The phone's erases over a stretch of time that are still kept, for days the phone had nothing of when they were
    /// asked (`commitErase(from:to:)`).
    private func pendingRanges() -> [RecordingDayWatchRequests.RangeErase] {
        if requests == nil { requests = readRequests() }
        let at = now()
        return (requests?.ranges ?? []).filter { $0.until > at }
    }

    /// A day whose first chunk or event comes after an erase over a stretch of time that reaches into it gets that
    /// erase, here and on the watch, before anything of it is kept: a day the watch began at midnight whose chunks were
    /// still on their way when the user erased the last ten minutes. Throws when the erase could not be written down
    /// (the chunk or event is then not acknowledged, and comes again).
    private func eraseRanges(_ record: inout RecordingDayRecord) throws {
        let ranges = pendingRanges()
        guard !ranges.isEmpty, let span = record.span else { return }
        for range in ranges where span.start < range.to && range.from < span.end {
            let erasure = RecordingDayErasure(id: range.id, from: range.from, to: range.to)
            guard !record.erasures.contains(erasure) else { continue }
            _ = try eraseHere(&record, erasure)
        }
    }

    /// A launch after a crash (or a failed write) cut an erase over a stretch of time short: the days the phone held
    /// that don't have it yet get it now.
    private func eraseUnfinishedRanges() {
        for range in pendingRanges() where range.done != true {
            let erasure = RecordingDayErasure(id: range.id, from: range.from, to: range.to)
            var finished = true
            for id in heldDays(overlapping: range.from, range.to) {
                guard !forgetIfDeleted(id), var record = load(id), !record.erasures.contains(erasure) else { continue }
                if (try? eraseHere(&record, erasure)) != true { finished = false }
            }
            if finished { rangeDone(range.id) }
        }
    }

    /// A day's span, without decoding its text when it isn't in memory: an erase looks at every day the phone holds.
    private func span(of id: UUID) -> DateInterval? {
        if let record = cache[id] { return record.span }
        if let data = try? Data(contentsOf: dayURL(id)),
           let head = try? Self.dayDecoder().decode(RecordingDayHead.self, from: data), head.id == id {
            return head.span
        }
        return load(id)?.span
    }

    private func commitWatchErased(_ answer: RecordingDayErased) {
        if !forgetIfDeleted(answer.recording), var record = load(answer.recording),
           record.erasures.contains(where: { $0.id == answer.erased }) {
            let held = record
            let gone = record.erase(id: nil, from: nil, to: nil, seqs: answer.erasedSeqs)
            if record != held {
                keep(record)
                // The settled seqs are on disk before the request is dropped: the watch answers a repeat the same way.
                guard write(answer.recording) else { return }
                try? deleteAudio(answer.recording, seqs: gone)
            }
        }
        updateRequests { $0.erases.removeAll { $0.recording == answer.recording && $0.id == answer.erased } }
    }

    @discardableResult private func commitWatchDelete(_ id: UUID) -> Bool {
        let until = now().addingTimeInterval(RecordingDayWatchRequests.lifetime)
        return updateRequests { requests in
            requests.erases.removeAll { $0.recording == id }
            guard !requests.deletes.contains(where: { $0.recording == id }) else { return }
            requests.deletes.append(.init(recording: id, until: until))
        }
    }

    /// A chunk or an event of a day deleted here came: the watch still holds that day, so it is asked (again) to delete
    /// it, on the strength of the library's tombstone and before the acknowledgement goes. The request the delete made
    /// may never have reached `day-requests.json` (the app ended before it, or its write failed), or the delete came a
    /// way that asked nothing of the watch; without this the watch would go on recording into the deleted day, every
    /// chunk acknowledged and dropped. At most once per `refusalInterval` per day; a request still waiting is left as it
    /// is (the receiver sends it again, `RecordingDayIntake`'s `refused`).
    private func refuse(_ id: UUID) {
        let at = now()
        if let last = refusals[id], at >= last, at.timeIntervalSince(last) < Self.refusalInterval { return }
        refusals[id] = at
        commitWatchDelete(id)
    }

    /// The pending requests, read once; expired ones dropped, and written when that or an earlier write left the file
    /// behind what is held here.
    private func currentRequests() -> RecordingDayWatchRequests {
        if requests == nil { requests = readRequests() }
        var current = requests ?? RecordingDayWatchRequests()
        if current.prune(now: now()) || !requestsSaved { saveRequests(current) }
        return current
    }

    /// Changes the pending requests; written, and announced when they changed. Kept in memory even when the write fails,
    /// so they are still sent while the app runs, and written again with the next look at them (a request asked for
    /// again finds it already held, and must not take that for being on disk). Whether they are on disk now.
    @discardableResult private func updateRequests(_ edit: (inout RecordingDayWatchRequests) -> Void) -> Bool {
        let held = currentRequests()
        var next = held
        edit(&next)
        guard next != held else { return requestsSaved }
        saveRequests(next)
        requestChanges.yield(())
        return requestsSaved
    }

    private func saveRequests(_ next: RecordingDayWatchRequests) {
        requests = next
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            try JSONEncoder().encode(next).write(to: requestsURL, options: Self.writing)
            requestsSaved = true
        } catch {
            requestsSaved = false
        }
    }

    private func readRequests() -> RecordingDayWatchRequests {
        guard let data = try? Data(contentsOf: requestsURL) else { return RecordingDayWatchRequests() }
        return (try? JSONDecoder().decode(RecordingDayWatchRequests.self, from: data)) ?? RecordingDayWatchRequests()
    }

    /// The day as it now is, kept and announced; written with the next coalesced write. `durable`: all that changed is
    /// a chunk or an event its sidecar or `events.log` already holds (and the journal names), which a recovery merges in
    /// anyway, so `day.json` takes it with the day's next write of any kind, or at the latest `arrivalWriteDelay` on.
    private func keep(_ record: RecordingDayRecord, durable: Bool = false) {
        cache[record.id] = record
        touch(record.id)
        markDirty(record.id, soon: !durable)
        dayChanges.yield(record.id)
    }

    /// The day's folder, created when missing with the protection its files get (`writing`).
    private func makeFolder(_ id: UUID) throws {
        try FileManager.default.createDirectory(at: folder(id), withIntermediateDirectories: true,
                                                attributes: [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication])
    }

    private func put(_ audio: Audio, at url: URL) throws {
        switch audio {
        case .data(let data):
            try data.write(to: url, options: Self.writing)
        case .file(let source):
            // A copy from an earlier try whose sidecar was never written would stop the move.
            if FileManager.default.fileExists(atPath: url.path) { try FileManager.default.removeItem(at: url) }
            try FileManager.default.moveItem(at: source, to: url)
            // A moved file keeps the protection it was delivered with; the transcriber reads it while the phone is locked.
            try? FileManager.default.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                                                   ofItemAtPath: url.path)
        }
    }

    /// The chunk's sidecar, noted in the journal first: a crash between the two leaves a line for a sidecar that is
    /// missing, which merging ignores.
    private func writeSidecar(_ sidecar: RecordingDaySidecar, _ id: UUID) throws {
        try makeFolder(id)
        try note(String(sidecar.header.seq), in: id)
        try JSONEncoder().encode(sidecar).write(to: sidecarURL(id, seq: sidecar.header.seq), options: Self.writing)
    }

    /// Appends a line to the day's journal (a seq whose sidecar is about to be written, or `eventsEntry` /
    /// `erasesEntry`): what `day.json` may not hold yet, for `reconcile`. File dates can't say that: the clock that dates
    /// them can be set back between two writes, and a file dated before `day.json` would be skipped, an erase in it with
    /// it. The folder exists.
    private func note(_ entry: String, in id: UUID) throws {
        try appendLine(Data(entry.utf8), to: journalURL(id))
    }

    /// One line at the end of a file, created (with the day's protection) when missing. A crash can cut the last line
    /// short, and readers skip it; the next line then starts on a line of its own, or it would join the cut one and be
    /// skipped with it (an event acknowledged on the strength of this write, lost at the next recovery). A file that
    /// exists but can't be opened throws rather than being written over.
    private func appendLine(_ line: Data, to url: URL) throws {
        let newline = UInt8(ascii: "\n")
        var data = Data()
        guard FileManager.default.fileExists(atPath: url.path) else {
            data.append(line)
            data.append(newline)
            try data.write(to: url, options: Self.writing)
            return
        }
        let handle = try FileHandle(forUpdating: url)
        defer { try? handle.close() }
        let end = try handle.seekToEnd()
        if end > 0 {
            try handle.seek(toOffset: end - 1)
            if try handle.read(upToCount: 1)?.first != newline { data.append(newline) }
            try handle.seekToEnd()
        }
        data.append(line)
        data.append(newline)
        try handle.write(contentsOf: data)
    }

    /// What the journal names: the seqs whose sidecars were written since `day.json` was, and whether events or local
    /// erases were. A line a crash cut short is skipped.
    private func readJournal(_ id: UUID) -> Unsaved {
        guard let data = try? Data(contentsOf: journalURL(id)) else { return Unsaved() }
        var unsaved = Unsaved()
        for line in String(decoding: data, as: UTF8.self).split(separator: "\n") {
            if line == Self.eventsEntry {
                unsaved.events = true
            } else if line == Self.erasesEntry {
                unsaved.erases = true
            } else if line == Self.asksEntry {
                unsaved.asks = true
            } else if let seq = Int(line), seq >= 0 {
                unsaved.seqs.insert(seq)
            }
        }
        return unsaved
    }

    /// Deletes these chunks' audio files and notes it in the record; done for a file that is gone, whether this deleted
    /// it or it never was there. Any other failure throws: an erase must not be acknowledged while its audio is still on
    /// the phone. What did go is noted before it throws.
    private func deleteAudio(_ id: UUID, seqs: Set<Int>) throws {
        guard !seqs.isEmpty else { return }
        var failure: Error?
        var deleted: [Int] = []
        for seq in seqs.sorted() {
            do {
                try deleteAudioFile(id, seq: seq)
                deleted.append(seq)
            } catch {
                failure = failure ?? error
            }
        }
        if var record = cache[id], deleted.contains(where: { record.chunks[$0]?.hasAudio == true }) {
            for seq in deleted { record.chunks[seq]?.hasAudio = false }
            keep(record)
        }
        if let failure { throw failure }
    }

    private func deleteAudioFile(_ id: UUID, seq: Int) throws {
        let url = audioURL(id, seq: seq)
        do {
            try FileManager.default.removeItem(at: url)
        } catch {
            guard FileManager.default.fileExists(atPath: url.path) else { return }
            throw error
        }
    }

    /// Deletes what erased or lost chunks left on disk (a deletion that failed before); throws while one can't go.
    private func purgeAudio(_ id: UUID) throws {
        guard let record = load(id) else { return }
        try deleteAudio(id, seqs: Set(record.chunks.values.filter { $0.isErasedOrLost && $0.hasAudio }.map(\.seq)))
    }

    private func apply(_ edit: (inout RecordingDayRecord) -> Void, to id: UUID) {
        guard !forgetIfDeleted(id), let record = load(id) else { return }
        var next = record
        edit(&next)
        next.id = id
        next.keepSettled(from: record)
        guard next != record else { return }
        keep(next)
    }

    private func dropAudio(_ id: UUID, seqs: [Int]) -> Bool {
        // A deleted day's folder goes with it (the library's), its audio too.
        guard !forgetIfDeleted(id), load(id) != nil else { return true }
        // Only the files that went are noted as gone (`deleteAudio`): one that stays keeps `hasAudio`, so the retention
        // pass that finds it past its deadline again tries again.
        let removed = (try? deleteAudio(id, seqs: Set(seqs))) != nil
        // Written at once: a recovery after a crash trusts a current day.json, which would still say the audio is there.
        write(id)
        return removed
    }

    // MARK: Loading, recovery

    /// What `day.json` may not hold yet.
    private nonisolated struct Unsaved {
        var seqs: Set<Int> = []
        var events = false
        var erases = false
        /// An erase's request to the watch may not be on disk, though `day.json` holds the erase (`unasked`).
        var asks = false

        var isEmpty: Bool { seqs.isEmpty && !events && !erases && !asks }
    }

    /// The day from memory, else from its folder. The first read of a day in this process brings it up to its folder
    /// (`reconcile`), whether the launch's recovery gets there first or a chunk, an event, an edit or a reader does: a
    /// crash can leave sidecars and events newer than `day.json`, and a chunk merged into the older record, or an edit
    /// written over it, would bury them, an erase among them. Later reads trust `day.json`, else rebuild the day from its
    /// folder.
    private func load(_ id: UUID) -> RecordingDayRecord? {
        if let record = cache[id] {
            touch(id)
            return record
        }
        let reconciliation = reconciled.contains(id) ? nil : reconcile(id)
        let read = reconciliation?.record ?? readDayFile(id)
        let rebuilt = read == nil ? rebuild(id) : nil
        guard let record = read ?? rebuilt else { return nil }
        cache[id] = record
        touch(id)
        if reconciliation?.changed == true {
            // Written and announced like the days `recoverOnLaunch` recovers, and reported by it.
            unreported.insert(id)
            markDirty(id)
            dayChanges.yield(id)
        } else if rebuilt != nil {
            markDirty(id)
        }
        trimCache()
        return record
    }

    /// A day without a readable `day.json`, from its whole folder: the app ended before the day's first write, or a
    /// build that reads it differently wrote it (`readDayFile` set it aside).
    private func rebuild(_ id: UUID) -> RecordingDayRecord? {
        let listing = list(id)
        guard listing.isDay else { return nil }
        var record: RecordingDayRecord?
        merge(listing, Unsaved(events: true, erases: true), allSidecars: true, into: &record, id)
        return record
    }

    private func readDayFile(_ id: UUID) -> RecordingDayRecord? {
        guard let data = try? Data(contentsOf: dayURL(id)) else { return nil }
        if let record = try? Self.dayDecoder().decode(RecordingDayRecord.self, from: data), record.id == id { return record }
        // Written by a build that reads it differently: kept aside, not overwritten, and the day rebuilt from its folder.
        let aside = folder(id).appendingPathComponent(Self.unreadableFile)
        try? FileManager.default.removeItem(at: aside)
        try? FileManager.default.moveItem(at: dayURL(id), to: aside)
        return nil
    }

    private func readSidecar(_ url: URL) -> RecordingDaySidecar? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(RecordingDaySidecar.self, from: data)
    }

    /// The events the watch sent for the day, in the order they came; a line a crash cut short is skipped.
    private func readEvents(_ id: UUID) -> [RecordingDayEventEnvelope] {
        guard let data = try? Data(contentsOf: eventsURL(id)) else { return [] }
        return data.split(separator: UInt8(ascii: "\n")).compactMap { RecordingDayEventEnvelope(data: Data($0)) }
    }

    private func readLocalErases(_ id: UUID) -> [RecordingDayErasure] {
        guard let data = try? Data(contentsOf: erasesURL(id)) else { return [] }
        return (try? JSONDecoder().decode([RecordingDayErasure].self, from: data)) ?? []
    }

    /// A folder's files by kind, with when each sidecar, `day.json`, `events.log` and `erases.json` were last written.
    private nonisolated struct Listing {
        var sidecars: [Int: (url: URL, modified: Date)] = [:]
        var audio: [Int: URL] = [:]
        var dayWrittenAt: Date?
        var eventsWrittenAt: Date?
        var erasesWrittenAt: Date?
        var hasJournal = false

        /// A day recording's folder (clips and rolling recordings share the root).
        var isDay: Bool { !sidecars.isEmpty || dayWrittenAt != nil || eventsWrittenAt != nil || erasesWrittenAt != nil }

        /// What is dated at or after `date`.
        func written(since date: Date) -> Unsaved {
            Unsaved(seqs: Set(sidecars.filter { $0.value.modified >= date }.keys),
                    events: eventsWrittenAt.map { $0 >= date } ?? false,
                    erases: erasesWrittenAt.map { $0 >= date } ?? false)
        }
    }

    private func list(_ id: UUID) -> Listing {
        let keys: [URLResourceKey] = [.contentModificationDateKey]
        let files = (try? FileManager.default.contentsOfDirectory(at: folder(id), includingPropertiesForKeys: keys)) ?? []
        var listing = Listing()
        for file in files {
            let name = file.lastPathComponent
            let modified = (try? file.resourceValues(forKeys: Set(keys)))?.contentModificationDate ?? .distantPast
            switch name {
            case Self.dayFile: listing.dayWrittenAt = modified
            case Self.eventsFile: listing.eventsWrittenAt = modified
            case Self.erasesFile: listing.erasesWrittenAt = modified
            case Self.journalFile: listing.hasJournal = true
            default:
                if let seq = Self.seq(in: name, ext: "json") {
                    listing.sidecars[seq] = (file, modified)
                } else if let seq = Self.seq(in: name, ext: "m4a") {
                    listing.audio[seq] = file
                }
            }
        }
        return listing
    }

    /// The seq in `seg-NNN.<ext>`.
    private static func seq(in name: String, ext: String) -> Int? {
        guard name.hasPrefix("seg-"), name.hasSuffix("." + ext),
              let seq = Int(name.dropFirst(4).dropLast(ext.count + 1)), seq >= 0 else { return nil }
        return seq
    }

    /// Merges what the folder holds into `record` (created from the first sidecar or event when there is none): the
    /// sidecars of `unsaved.seqs` (all of them with `allSidecars`), the events of `events.log` and the local erases when
    /// `unsaved` names them, then brings the audio flags to the files on disk. Merging what the record already holds
    /// changes nothing: a held seq is never stored twice, an event never applied twice, an erase never repeated.
    private func merge(_ listing: Listing, _ unsaved: Unsaved, allSidecars: Bool, into record: inout RecordingDayRecord?, _ id: UUID) {
        for (seq, file) in listing.sidecars.sorted(by: { $0.key < $1.key }) where allSidecars || unsaved.seqs.contains(seq) {
            // A sidecar names its own recording and seq; one that doesn't is not this day's.
            guard let sidecar = readSidecar(file.url), sidecar.header.recording == id, sidecar.header.seq == seq else { continue }
            var current = record ?? RecordingDayRecord(header: sidecar.header)
            if current.chunks[seq] == nil {
                var chunk = RecordingDayChunk(header: sidecar.header, receivedAt: sidecar.receivedAt)
                if let settled = sidecar.settled { chunk.settle(settled) } else { chunk.hasAudio = listing.audio[seq] != nil }
                current.put(chunk, recordingStartedAt: sidecar.header.recordingStartedAt)
            }
            record = current
        }
        if unsaved.events {
            for envelope in readEvents(id) where envelope.recording == id {
                var current = record ?? RecordingDayRecord(envelope: envelope)
                _ = current.hold(envelope.event)
                record = current
            }
        }
        guard var current = record else { return }
        _ = current.applyHeldEvents()
        if unsaved.erases || unsaved.asks {
            for erasure in readLocalErases(id) {
                let held = current.erasures.contains(erasure)
                if !held { current.erase(id: erasure.id, from: erasure.from, to: erasure.to, seqs: []) }
                // An erase the app ended in the middle of, or one whose request couldn't be written: its request to the
                // watch may not be on disk. Asked again for as long as the watch can hold anything of that time (a
                // repeat is answered the same way).
                guard !held || unsaved.asks else { continue }
                let until = erasure.to.addingTimeInterval(RecordingDayWatchRequests.lifetime)
                if until > now(), !askWatchToErase(id, erasure, until: until) { unasked.insert(id) }
            }
        }
        for (seq, chunk) in current.chunks {
            let onDisk = listing.audio[seq] != nil
            if chunk.isErasedOrLost, onDisk {
                // An erase the app ended in the middle of; if this fails too, the next write of the day tries again.
                let gone = (try? deleteAudioFile(id, seq: seq)) != nil
                current.chunks[seq]?.hasAudio = !gone
            } else if chunk.hasAudio != onDisk {
                current.chunks[seq]?.hasAudio = onDisk
            }
        }
        record = current
    }

    /// `recoverOnLaunch`: every folder brought up to date, then the days that changed since the last call.
    private func recoverAll() -> [UUID] {
        sweep()
        eraseUnfinishedRanges()
        let changed = unreported.filter { !forgetIfDeleted($0) }
        unreported = []
        return changed.sorted { $0.uuidString < $1.uuidString }
    }

    /// Brings every folder this process hasn't read yet up to date, writing each day that changed (`reconcile`), and
    /// writes the days an earlier read brought up to date (a chunk came in before this ran). Each is on disk when this
    /// returns, and noted for `recoverOnLaunch`. Folders already read are skipped, so running it again is cheap.
    private func sweep() {
        for id in unreported.sorted(by: { $0.uuidString < $1.uuidString }) where !forgetIfDeleted(id) { write(id) }
        let folders = (try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)) ?? []
        for folder in folders {
            // A deleted day's folder is the library's to remove (its init does).
            guard let id = UUID(uuidString: folder.lastPathComponent), !reconciled.contains(id), !forgetIfDeleted(id) else { continue }
            let reconciliation = reconcile(id)
            guard reconciliation.changed, let record = reconciliation.record else { continue }
            cache[id] = record
            touch(id)
            dirty.insert(id)
            // One at a time, so at most `cacheLimit` days stay decoded however many a crash left behind.
            write(id)
            dayChanges.yield(id)
            trimCache()
            unreported.insert(id)
        }
        swept = true
    }

    /// Brings one day up to its folder, the first time this process reads it: the `day.json` an earlier run wrote, with
    /// the sidecars, events and erases written after it merged in (all of them without a readable one). Returns the
    /// record when it read one, and whether the folder changed it; no record when the folder is no day's, or when
    /// `day.json` holds it all (not even decoded then: fourteen days of text at every launch).
    private func reconcile(_ id: UUID) -> (record: RecordingDayRecord?, changed: Bool) {
        reconciled.insert(id)
        var listing = list(id)
        guard listing.isDay else { return (nil, false) }
        // Audio without its sidecar was never acknowledged: the watch still has it and sends it again. (Nothing of this
        // process is half-written here: a chunk's audio and sidecar are written in one turn of the queue.)
        for (seq, url) in listing.audio where listing.sidecars[seq] == nil {
            try? FileManager.default.removeItem(at: url)
            listing.audio[seq] = nil
        }
        let unsaved = unsavedChanges(listing, id)
        if listing.dayWrittenAt != nil, unsaved.isEmpty { return (nil, false) }
        let current = listing.dayWrittenAt == nil ? nil : readDayFile(id)
        // Without a readable record everything counts; with one, only what it may not hold.
        var merged = current
        let all = current == nil
        merge(listing, all ? Unsaved(events: true, erases: true) : unsaved, allSidecars: all, into: &merged, id)
        // Asked again and on disk, with nothing else new: the journal has nothing left to say (no write of the day
        // follows to remove it), so later launches don't ask again.
        if unsaved.asks, merged == current, !unasked.contains(id) { try? FileManager.default.removeItem(at: journalURL(id)) }
        return (merged, merged != current)
    }

    /// What `day.json` may not hold: what the journal names (written after it, whatever the dates), and any file dated
    /// after it or a little before (in case a crash of the whole system lost a journal line).
    private func unsavedChanges(_ listing: Listing, _ id: UUID) -> Unsaved {
        var unsaved = listing.hasJournal ? readJournal(id) : Unsaved()
        guard let since = listing.dayWrittenAt?.addingTimeInterval(-Self.clockSlack) else { return unsaved }
        let dated = listing.written(since: since)
        unsaved.seqs.formUnion(dated.seqs)
        unsaved.events = unsaved.events || dated.events
        unsaved.erases = unsaved.erases || dated.erases
        return unsaved
    }

    private func dayIDs() -> [UUID] {
        let folders = (try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)) ?? []
        var ids = Set(cache.keys)
        for folder in folders {
            guard let id = UUID(uuidString: folder.lastPathComponent),
                  FileManager.default.fileExists(atPath: folder.appendingPathComponent(Self.dayFile).path) else { continue }
            ids.insert(id)
        }
        var out: [UUID] = []
        for id in ids where !forgetIfDeleted(id) { out.append(id) }
        return out.sorted { $0.uuidString < $1.uuidString }
    }

    // MARK: Writing, deletion, the cache

    /// The day differs from its `day.json` (kept in `cache` until written): written with the coalesced write
    /// `writeDelay` on, or, for an arrival only (`soon` false), with whichever write comes first, at the latest
    /// `arrivalWriteDelay` on.
    private func markDirty(_ id: UUID, soon: Bool = true) {
        dirty.insert(id)
        if soon {
            guard !writeScheduled else { return }
            writeScheduled = true
            queue.asyncAfter(deadline: .now() + writeDelay) { [weak self] in
                guard let self else { return }
                self.writeScheduled = false
                self.writeDirty()
            }
        } else {
            guard !writeScheduled, !arrivalWriteScheduled else { return }
            arrivalWriteScheduled = true
            queue.asyncAfter(deadline: .now() + arrivalWriteDelay) { [weak self] in
                guard let self else { return }
                self.arrivalWriteScheduled = false
                self.writeDirty()
            }
        }
    }

    private func writeDirty() {
        for id in dirty.sorted(by: { $0.uuidString < $1.uuidString }) { write(id) }
        trimCache()
    }

    /// Writes the day's `day.json` if it changed since its last write. True when it is on disk as the store holds it;
    /// false when the write failed (it stays dirty for the next change or flush; the sidecars and events still hold
    /// everything acknowledged) or the day was deleted.
    @discardableResult private func write(_ id: UUID) -> Bool {
        guard !forgetIfDeleted(id) else { return false }
        guard dirty.contains(id) else { return true }
        guard let record = purgeLeftoverAudio(id) else {
            dirty.remove(id)
            return false
        }
        do {
            let data = try Self.dayEncoder().encode(record)
            try makeFolder(id)
            try data.write(to: dayURL(id), options: Self.writing)
        } catch {
            return false
        }
        dirty.remove(id)
        // `day.json` now holds every line of the journal: the record in `cache` took in everything this process wrote,
        // and what an earlier run left was merged in when the day was first read (`reconcile`). One left behind only
        // costs a few file reads at the next launch.
        try? FileManager.default.removeItem(at: journalURL(id))
        if unasked.contains(id) {
            if requestsSaved { unasked.remove(id) } else { try? note(Self.asksEntry, in: id) }
        }
        return !removeIfDeleted(id)
    }

    /// The cached day after deleting the audio its erased or lost chunks still have on disk (a deletion that failed
    /// earlier); what can't go stays noted for the next write.
    private func purgeLeftoverAudio(_ id: UUID) -> RecordingDayRecord? {
        guard var record = cache[id] else { return nil }
        for chunk in record.chunks.values where chunk.isErasedOrLost && chunk.hasAudio {
            if (try? deleteAudioFile(id, seq: chunk.seq)) != nil { record.chunks[chunk.seq]?.hasAudio = false }
        }
        cache[id] = record
        return record
    }

    /// `day.json`'s coders: a time that is not a number (a transcriber's bad value) is written as text rather than
    /// failing every later write of the day.
    private static func dayEncoder() -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.nonConformingFloatEncodingStrategy = .convertToString(positiveInfinity: "+Infinity", negativeInfinity: "-Infinity", nan: "NaN")
        return encoder
    }

    private static func dayDecoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.nonConformingFloatDecodingStrategy = .convertFromString(positiveInfinity: "+Infinity", negativeInfinity: "-Infinity", nan: "NaN")
        return decoder
    }

    /// The day was deleted: forget it here (the library removes its folder).
    private func forgetIfDeleted(_ id: UUID) -> Bool {
        guard isDeleted(id) else { return false }
        forget(id)
        return true
    }

    /// The day was deleted while this queue wrote into its folder: remove what was written.
    private func removeIfDeleted(_ id: UUID) -> Bool {
        guard isDeleted(id) else { return false }
        try? FileManager.default.removeItem(at: folder(id))
        forget(id)
        return true
    }

    private func forget(_ id: UUID) {
        cache[id] = nil
        lastUse[id] = nil
        dirty.remove(id)
    }

    private func touch(_ id: UUID) {
        uses += 1
        lastUse[id] = uses
    }

    /// Drops the least recently used days that are on disk as they are. Days whose arrivals wait for their write
    /// (`markDirty`) are written first when there are more of them than the limit (a backlog of several days), so no
    /// more than `cacheLimit` stay decoded for long.
    private func trimCache() {
        guard cache.count > Self.cacheLimit else { return }
        let clean = cache.keys.filter { !dirty.contains($0) }.sorted { (lastUse[$0] ?? 0) < (lastUse[$1] ?? 0) }
        for id in clean.prefix(cache.count - Self.cacheLimit) {
            cache[id] = nil
            lastUse[id] = nil
        }
        guard cache.count > Self.cacheLimit else { return }
        let waiting = cache.keys.sorted { (lastUse[$0] ?? 0) < (lastUse[$1] ?? 0) }
        for id in waiting.prefix(cache.count - Self.cacheLimit) where write(id) {
            cache[id] = nil
            lastUse[id] = nil
        }
    }
}

// MARK: - What the Recordings page reads

/// A stretch of a day with no text, and why: the contract's `coverage.gaps[]` shape, in seconds since the day's start.
nonisolated struct RecordingDayGap: Codable, Sendable, Equatable {
    /// The contract's gap reasons; an unknown one (a newer watch's `paused` reason) is kept as it is.
    nonisolated enum Reason {
        static let paused = RecordingDayContract.Reason.paused
        static let interrupted = RecordingDayContract.Reason.interrupted
        static let stopped = RecordingDayContract.Reason.stopped
        /// The watch still holds the audio.
        static let pending = "pending"
        /// The watch could not keep it, or it came after the day's audio deadline.
        static let lost = "lost"
        /// The user erased it.
        static let erased = "erased"
        /// [ext] The phone could not transcribe it after `maxAttempts` tries.
        static let untranscribed = "untranscribed"
    }

    var from: Double
    var to: Double
    var reason: String
}

nonisolated extension RecordingDayGap {
    /// Gaps of one reason this close are one (the watch's clocks differ by a fraction of a second); never erased ones.
    static let tolerance = 1.0

    /// The day's stretches without text, as seconds since `base` (the day's start), in time order with touching ones of
    /// one reason merged (docs/assistant/day-recap.md「The recording」): the microphone closed (from the control events,
    /// never inferred from silence or from time between chunks; one still open runs to the recording's end, else to
    /// `now`), seqs the watch still holds (`pending`, from the end of the chunk before to the start of the chunk after),
    /// lost seqs (their reported times when known) and chunks, erased ranges exactly, and chunks the phone could not
    /// transcribe. A pending seq next to a lost one is a gap of its own. Recorded silence is not a gap. Gaps of different
    /// reasons may overlap; the hub mends that.
    static func gaps(in record: RecordingDayRecord, since base: Date, now: Date) -> [RecordingDayGap] {
        var out: [RecordingDayGap] = []
        func add(_ from: Date, _ to: Date, _ reason: String) {
            let start = max(0, from.timeIntervalSince(base)), end = to.timeIntervalSince(base)
            if end > start { out.append(RecordingDayGap(from: start, to: end, reason: reason)) }
        }
        for pause in record.pauses { add(pause.from, pause.to ?? record.closedAt ?? max(pause.from, now), pause.reason) }
        for erasure in record.erasures { add(erasure.from, erasure.to, Reason.erased) }
        for chunk in record.chunks.values {
            if chunk.lost {
                add(chunk.startedAt, chunk.endedAt, Reason.lost)
            } else if !chunk.erased, chunk.state == .failed {
                add(chunk.startedAt, chunk.endedAt, Reason.untranscribed)
            }
        }
        missing(in: record, since: base, add)
        return merged(out)
    }

    /// What the phone knows of a seq it doesn't hold.
    private nonisolated enum Absence: Equatable {
        /// The watch still holds it.
        case pending
        /// The watch reported it lost, with the times of its loss when the report gave both.
        case lost(from: Date?, to: Date?)
        /// An erase deleted it on the watch: inside the erase's range, already a gap.
        case erased
    }

    private static func absence(of seq: Int, in record: RecordingDayRecord) -> Absence {
        if record.erasedSeqs.contains(seq) { return .erased }
        guard record.lost.contains(seq) else { return .pending }
        let loss = record.losses.last { $0.seqs.contains(seq) && $0.from != nil && $0.to != nil }
        return .lost(from: loss?.from, to: loss?.to)
    }

    /// The seqs the phone doesn't hold, between the chunks it does: each run of one kind (pending, lost, and lost with
    /// the same reported times) is its own gap, so audio the watch still holds and audio gone for good are never
    /// mistaken for each other.
    private static func missing(in record: RecordingDayRecord, since base: Date, _ add: (Date, Date, String) -> Void) {
        let end = max(record.knownSeqEnd, (record.chunks.keys.max() ?? -1) + 1)
        var previousEnd = base
        var runs: [Absence] = []
        for seq in 0..<end {
            guard let chunk = record.chunks[seq] else {
                let absence = absence(of: seq, in: record)
                if runs.last != absence { runs.append(absence) }
                continue
            }
            between(runs, after: previousEnd, before: chunk.startedAt, add)
            runs = []
            previousEnd = max(previousEnd, chunk.endedAt)
        }
        between(runs, after: previousEnd, before: record.closedAt, add)
    }

    /// The gaps of the runs of missing seqs, in seq order, from the end of the chunk before (`after`) to the start of the
    /// chunk after (`before`; nil past the last chunk of an open day, where only reported times reach). A loss with
    /// reported times takes them, cut to the chunks around it (its range can cover a chunk the phone holds); a run
    /// without times takes the stretch the runs with times leave it, in the chunks' bounds.
    private static func between(_ runs: [Absence], after: Date, before: Date?, _ add: (Date, Date, String) -> Void) {
        let timed: [(from: Date, to: Date)?] = runs.map { run in
            guard case .lost(let from?, let to?) = run else { return nil }
            let span = (from: max(from, after), to: min(to, before ?? to))
            // Times outside the chunks around them contradict the seqs: the stretch between the chunks is all that is
            // known then.
            return span.to > span.from ? span : nil
        }
        var start = after
        for (index, run) in runs.enumerated() {
            if let span = timed[index] {
                add(span.from, span.to, Reason.lost)
                start = max(start, span.to)
                continue
            }
            if case .erased = run { continue }
            let next = timed[(index + 1)...].lazy.compactMap { $0?.from }.first ?? before
            add(start, next ?? start, run == .pending ? Reason.pending : Reason.lost)
        }
    }

    /// In time order, gaps of one reason that overlap or touch joined (gaps of different reasons are left as they are),
    /// and those `tolerance` apart too, except erased ranges: they are kept exactly, never stretched over time nobody
    /// erased (the recap's withdrawal reads them).
    private static func merged(_ gaps: [RecordingDayGap]) -> [RecordingDayGap] {
        var out: [RecordingDayGap] = []
        for reason in Set(gaps.map(\.reason)) {
            let slack = reason == Reason.erased ? 0 : tolerance
            var open: RecordingDayGap?
            for gap in gaps.filter({ $0.reason == reason }).sorted(by: { ($0.from, $0.to) < ($1.from, $1.to) }) {
                if var current = open, gap.from - current.to <= slack {
                    current.to = max(current.to, gap.to)
                    open = current
                } else {
                    if let open { out.append(open) }
                    open = gap
                }
            }
            if let open { out.append(open) }
        }
        return out.sorted { ($0.from, $0.to, $0.reason) < ($1.from, $1.to, $1.reason) }
    }
}

/// Where the day's text stands with the hub, for the day's rows and page.
nonisolated enum RecordingDaySyncState: String, Sendable, Equatable, CaseIterable {
    /// The hub doesn't list `recording_day`: the text stays on this phone until it does.
    case notOffered
    /// The hub refused the day routes (an older manager): nothing is sent until its next `hello`.
    case unsupported
    /// The upload stopped for good (`RecordingDayRecord.problem`, or the hub refused a part).
    case problem
    /// Text is on its way to the hub.
    case pending
    /// The hub has everything transcribed so far.
    case synced
    /// The hub keeps no text of the day any more (past `recording_days`, or deleted there).
    case expired
}

nonisolated extension RecordingDaySyncState {
    init(_ record: RecordingDayRecord, hubOffered: Bool) {
        let kept = record.parts.filter { !$0.expired && !$0.deleted }
        if record.problem != nil || kept.contains(where: { $0.problem != nil }) {
            self = .problem
        } else if record.unsupportedUntilHello {
            self = .unsupported
        } else if !hubOffered {
            self = .notOffered
        } else if !record.parts.isEmpty, kept.isEmpty {
            self = .expired
        } else {
            self = record.hasUnsentText ? .pending : .synced
        }
    }
}

/// What the Recordings page shows of a day, computed off the main actor from its record.
nonisolated struct RecordingDaySummary: Sendable, Equatable {
    var id: UUID
    var day: String
    var timeZone: String
    /// When the day's recording began on the watch (`recording_started_at`): the day's clock.
    var firstAt: Date
    /// When its last audio the phone holds ends.
    var lastAt: Date?
    /// When the recording ended (its `closed` event); nil while it is open.
    var closedAt: Date?
    /// Seconds of audio the phone holds or held (erased and lost chunks left out).
    var recordedSeconds: Double
    /// The end of the transcribed audio: every chunk the phone holds up to here is transcribed (or needs no text).
    var transcribedTo: Date?
    /// Chunks received with their audio (erased and lost ones left out), then by transcription state.
    var chunks: Int
    var waiting: Int
    var skipped: Int
    var failed: Int
    /// Seconds since `firstAt` (`RecordingDayGap.gaps(in:since:now:)`).
    var gaps: [RecordingDayGap]
    var marks: [RecordingDayMark]
    var sync: RecordingDaySyncState
    var recapState: String?
    /// Every part's newest recap is final.
    var recapFinal: Bool
    var hubIDs: [UUID]
    var complete: Bool
    /// Why the upload stopped for good (`sync == .problem`): the day's code, else the first refused part's.
    var problem: String? = nil
}

nonisolated extension RecordingDaySummary {
    init(record: RecordingDayRecord, hubOffered: Bool, now: Date) {
        let ordered = record.chunks.values.sorted { $0.seq < $1.seq }
        let kept = ordered.filter { !$0.isErasedOrLost }
        var transcribedTo: Date?
        for chunk in ordered {
            if chunk.needsTranscription { break }
            transcribedTo = max(transcribedTo ?? chunk.endedAt, chunk.endedAt)
        }
        self.init(id: record.id, day: record.day, timeZone: record.timeZone, firstAt: record.startedAt,
                  lastAt: kept.map(\.endedAt).max(),
                  closedAt: record.closedAt,
                  recordedSeconds: kept.reduce(0) { $0 + max(0, $1.duration) },
                  transcribedTo: transcribedTo,
                  chunks: kept.count,
                  waiting: kept.count(where: { $0.state == .waiting }),
                  skipped: kept.count(where: { $0.state == .skipped }),
                  failed: kept.count(where: { $0.state == .failed }),
                  gaps: RecordingDayGap.gaps(in: record, since: record.startedAt, now: now),
                  marks: record.marks.sorted { $0.at < $1.at },
                  sync: RecordingDaySyncState(record, hubOffered: hubOffered),
                  recapState: record.recapState,
                  recapFinal: record.recapFinal,
                  hubIDs: record.hubIDs,
                  complete: record.isComplete,
                  problem: record.problem
                      ?? record.parts.first { !$0.expired && !$0.deleted && $0.problem != nil }?.problem)
    }

    /// The span the recording covers: from its start to its end, or to its day's midnight while open.
    var span: DateInterval? {
        let zone = RecordingDayWire.zone(timeZone)
        guard let end = closedAt ?? zone.flatMap({ RecordingDayCalendar.end(of: day, in: $0) }), end >= firstAt else { return nil }
        return DateInterval(start: firstAt, end: end)
    }
}

/// The days as the Recordings page and the day's page read them, and the watch's state for the Today row. Changes
/// from the store are gathered for `coalesce` before the summaries are recomputed off the main actor, and a summary is
/// assigned only when it changed: a chunk arrives every ten seconds all day.
@MainActor @Observable final class RecordingDaySummaries {
    static let shared = RecordingDaySummaries()

    private(set) var byID: [UUID: RecordingDaySummary] = [:]
    /// The last state the watch published (`recordingDayWatch`); nil until it has.
    private(set) var watch: RecordingDayWatchState?

    /// Today's day, each day judged in its own time zone: the one the watch is recording if that is today's, else the
    /// latest begun.
    var today: RecordingDaySummary? {
        let at = now()
        let current = byID.values.filter { summary in
            RecordingDayCalendar.day(of: at, in: RecordingDayWire.zone(summary.timeZone) ?? .autoupdatingCurrent) == summary.day
        }
        if let id = watch?.recording, let recording = current.first(where: { $0.id == id }) { return recording }
        return current.max { ($0.firstAt, $0.id.uuidString) < ($1.firstAt, $1.id.uuidString) }
    }

    @ObservationIgnored private let store: RecordingDayStore
    @ObservationIgnored private let hubOffered: @Sendable () -> Bool
    @ObservationIgnored private let now: @Sendable () -> Date
    @ObservationIgnored private let coalesce: Duration
    @ObservationIgnored private var waiting: Set<UUID> = []
    @ObservationIgnored private var pass: Task<Void, Never>?
    @ObservationIgnored private var listening: Task<Void, Never>?

    /// - Parameter hubOffered: whether the hub takes day recordings (the flag cached from its last `hello`).
    init(store: RecordingDayStore = .shared, hubOffered: @escaping @Sendable () -> Bool = { RecordingDayDefaults.offered([]) },
         now: @escaping @Sendable () -> Date = { .now }, coalesce: Duration = .seconds(1)) {
        self.store = store
        self.hubOffered = hubOffered
        self.now = now
        self.coalesce = coalesce
    }

    /// Follows the store from now on: a day that changes is summarized again. Reads no other day; `readAll` does that,
    /// once the app is in front (a launch in the background to take a chunk shows nothing and doesn't decode fourteen
    /// days for it, P9). Idempotent.
    func start() {
        guard listening == nil else { return }
        let changes = store.changes
        listening = Task { [weak self] in
            for await id in changes { self?.changed(id) }
        }
    }

    /// A day changed, or was deleted (its summary goes): recomputed with the next pass.
    func changed(_ id: UUID) {
        waiting.insert(id)
        schedule()
    }

    /// Every day the phone holds, read and summarized: the receiver's call, once per launch with the app in front. Days a
    /// crash left without `day.json` are included even before the launch's recovery is done (`RecordingDayStore.ids`).
    func readAll() {
        Task { [weak self] in
            guard let self else { return }
            let ids = await self.store.ids()
            self.waiting.formUnion(ids)
            self.waiting.formUnion(self.byID.keys)
            self.schedule()
        }
    }

    /// The days already summarized, again: the hub's flag changed (it changes every day's `sync`). Days not read yet get
    /// the new flag when `readAll` reads them, so this reads no more than is already shown, in the background too.
    func refreshAll() {
        waiting.formUnion(byID.keys)
        schedule()
    }

    /// The watch's state (`WatchSync.onDayWatchState`).
    func receive(watch state: RecordingDayWatchState) {
        guard state != watch else { return }
        watch = state
    }

    /// Erases a wall-clock range from every day it touches (an erase over midnight reaches both days' recordings), here
    /// and on the watch, as one erase (`RecordingDayStore.requestErase(from:to:)`). The store finds the days, not these
    /// summaries: a day whose first chunk came a moment ago may not be summarized yet. Returns the days erased.
    func requestErase(from: Date, to: Date) async -> [UUID] {
        await store.requestErase(from: from, to: to)
    }

    /// Recomputes these days now (a pass calls it; tests await it).
    func refresh(_ ids: Set<UUID>) async {
        guard !ids.isEmpty else { return }
        let store = self.store, offered = self.hubOffered, now = self.now
        let computed = await Task.detached(priority: .utility) {
            let hub = offered(), at = now()
            var out: [UUID: RecordingDaySummary] = [:]
            for id in ids {
                if let record = await store.record(id) { out[id] = RecordingDaySummary(record: record, hubOffered: hub, now: at) }
            }
            return out
        }.value
        var next = byID
        for id in ids { next[id] = computed[id] }
        if next != byID { byID = next }
    }

    private func schedule() {
        guard pass == nil, !waiting.isEmpty else { return }
        pass = Task { [weak self, delay = self.coalesce] in
            try? await Task.sleep(for: delay)
            guard let self else { return }
            let ids = self.waiting
            self.waiting = []
            await self.refresh(ids)
            self.pass = nil
            self.schedule()
        }
    }
}
