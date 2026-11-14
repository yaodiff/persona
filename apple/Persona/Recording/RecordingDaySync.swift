import Foundation
import Observation
import os
import PersonaWire

// All-day recording's "phone to hub" (docs/assistant/day-recap.md「Phone to hub」): each day the watch records becomes
// one hub recording of kind `day` (more parts when a day outgrows one), created with a `PUT`, grown by appends by
// position, and replaced by a `PUT` only to repair it. Everything here is decided from the day's `RecordingDayRecord`
// (its parts' counters, the sentences' positions) and written back to it before the request that depends on it, so a
// crash, a lost answer or a retry never sends a sentence under two positions. Positions themselves are given in
// the transcriber's save (`RecordingDayUpload.place`), in the same write that marks a chunk transcribed. Only on a hub
// that lists `recording_day`.

// MARK: - Wire

/// What reconciliation (`GET /api/v1/recordings`) lists for one hub recording of a day (docs/assistant/day-recap.md
/// 「Reconciliation」). A field it didn't carry is nil and changes nothing here.
nonisolated struct RecordingDayHubRow: Sendable, Equatable {
    var id: UUID
    var revision: Int64? = nil
    var part: Int? = nil
    /// The hub's count of the recording's positions, and the revision of its last `PUT`. A day row without `base` comes
    /// from an older manager: nothing is sent to it.
    var next: Int? = nil
    var base: Int64? = nil
    var recapState: String? = nil
    /// Its newest recap is final, and when its first final recap was written (the phone's audio is kept 3 days after).
    var recapFinal: Bool? = nil
    var recapFinalAt: Date? = nil
}

/// A sentence as the hub stores it: wall-clock seconds since the day's `started_at`, pauses included.
nonisolated struct RecordingDaySegment: Codable, Sendable, Equatable {
    var start: Double
    var end: Double
    var text: String
}

nonisolated struct RecordingDayBookmark: Codable, Sendable, Equatable {
    var at: Double
    /// "" for a bookmark, "meeting" for a meeting mark [ext].
    var label: String
}

/// What a create, a repair and an append all say beside the segments; the last one the hub stored is remembered so an
/// append that would change none of it isn't sent.
nonisolated struct RecordingDayMeta: Sendable, Equatable {
    var endedAt: Date?
    var duration: Double
    var bookmarks: [RecordingDayBookmark]
    var coverage: RecordingDayCoverage
    var complete: Bool
}

/// `PUT /api/v1/recordings/{id}` of a day part: its creation (no segments) or a repair (every positioned sentence,
/// empty ones included, so the hub's count equals the phone's after it).
nonisolated struct RecordingDayPutBody: Encodable, Sendable {
    var revision: Int64
    var day: String
    var tz: String
    var part: Int
    var startedAt: Date
    var language: String
    var meta: RecordingDayMeta
    var segments: [RecordingDaySegment]

    private nonisolated enum CodingKeys: String, CodingKey {
        case revision, kind, day, tz, part, duration, language, device, title, bookmarks, coverage, segments, complete
        case startedAt = "started_at", endedAt = "ended_at"
    }

    func encode(to encoder: Encoder) throws {
        let zone = RecordingDayWire.zone(tz) ?? .gmt
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(revision, forKey: .revision)
        try c.encode("day", forKey: .kind)
        try c.encode(day, forKey: .day)
        try c.encode(tz, forKey: .tz)
        try c.encode(part, forKey: .part)
        try c.encode(RecordingDayWire.timestamp(startedAt, in: zone), forKey: .startedAt)
        try c.encodeIfPresent(meta.endedAt.map { RecordingDayWire.timestamp($0, in: zone) }, forKey: .endedAt)
        try c.encode(meta.duration, forKey: .duration)
        try c.encode(language, forKey: .language)
        try c.encode("watch", forKey: .device)
        try c.encode("", forKey: .title)
        try c.encode(meta.bookmarks, forKey: .bookmarks)
        try c.encode(meta.coverage, forKey: .coverage)
        try c.encode(segments, forKey: .segments)
        try c.encode(meta.complete, forKey: .complete)
    }
}

/// `POST /api/v1/recordings/{id}/segments`: positions `from…` on the base of the last `PUT`, with the other fields whole.
/// The title is left out: absent means unchanged, and the hub names the day.
nonisolated struct RecordingDayAppendBody: Encodable, Sendable {
    var base: Int64
    var revision: Int64
    var from: Int
    var segments: [RecordingDaySegment]
    var tz: String
    var meta: RecordingDayMeta

    private nonisolated enum CodingKeys: String, CodingKey {
        case base, revision, from, segments, duration, bookmarks, coverage, complete
        case endedAt = "ended_at"
    }

    func encode(to encoder: Encoder) throws {
        let zone = RecordingDayWire.zone(tz) ?? .gmt
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(base, forKey: .base)
        try c.encode(revision, forKey: .revision)
        try c.encode(from, forKey: .from)
        try c.encode(segments, forKey: .segments)
        try c.encode(meta.duration, forKey: .duration)
        try c.encodeIfPresent(meta.endedAt.map { RecordingDayWire.timestamp($0, in: zone) }, forKey: .endedAt)
        try c.encode(meta.bookmarks, forKey: .bookmarks)
        try c.encode(meta.coverage, forKey: .coverage)
        try c.encode(meta.complete, forKey: .complete)
    }
}

/// The `200` of a `PUT` (`stored`, `stale`, `expired`, with the base it set and the hub's count) and of an append
/// (`stored`, `expired`, with the hub's count). `revision` is the hub's stored revision after the request.
nonisolated struct RecordingDayAnswer: Decodable, Sendable {
    var revision: Int64?
    var base: Int64?
    var next: Int?
    var status: String?
}

/// How the upload answers a refused or failed request, by HTTP status and the contract's code.
nonisolated enum RecordingDayReaction: Equatable, Sendable {
    /// `409 base_changed`: a `PUT` the phone may or may not have sent replaced the text (`base`, the hub's `next`).
    case baseChanged(base: Int64?, next: Int?)
    /// `409 segments_gap`: `from` was above the hub's count; send from `next`.
    case segmentsGap(next: Int?)
    /// `404 recording_not_found`: create it (again).
    case notFound
    /// A `404` without that code: the route is missing, an older manager. Nothing goes until its next hello.
    case unsupported
    /// `400`: the hub refused this request. That part stops, its problem shown, and is never sent again unchanged.
    case rejected(String)
    /// `413 too_large`: the body was over 1 MiB; fewer segments per append.
    case tooLarge
    /// `413 day_full`: the part is full at the hub's `next`; the day continues in a next part.
    case dayFull(next: Int?)
    /// `409 not_day`, `409 kind_changed`: the id is another kind of recording there. Never written over.
    case conflict(String)
    /// `403`: read-only or not allowed; paused until the app comes to the front, a hello, or a retry.
    case forbidden
    /// `410`: this part was deleted on the hub, so the whole day goes.
    case deleted
    case cancelled
    /// `503`, no network, a timeout, anything else: again later, with a backoff.
    case retry
}

nonisolated extension RecordingDayReaction {
    init(_ error: Error) {
        if error.isCancellation {
            self = .cancelled
            return
        }
        guard let api = error as? APIError else {
            self = .retry
            return
        }
        let code = api.fields["code"] ?? ""
        switch (api.status, code) {
        case (409, "base_changed"): self = .baseChanged(base: Self.int64(api, "base"), next: Self.int(api, "next"))
        case (409, "segments_gap"): self = .segmentsGap(next: Self.int(api, "next"))
        case (409, _): self = .conflict(code.isEmpty ? "conflict" : code)
        case (404, "recording_not_found"): self = .notFound
        case (404, _): self = .unsupported
        case (400, _): self = .rejected(code.isEmpty ? "rejected" : code)
        case (413, "day_full"): self = .dayFull(next: Self.int(api, "next"))
        case (413, _): self = .tooLarge
        case (403, _): self = .forbidden
        case (410, _): self = .deleted
        default: self = .retry
        }
    }

    /// A number in the error's body: the client keeps it as text (`APIError.fields`), its JSON in `details`.
    private static func int(_ error: APIError, _ key: String) -> Int? {
        error.fields[key].flatMap { Int($0) } ?? error.details?[key]?.int
    }

    private static func int64(_ error: APIError, _ key: String) -> Int64? {
        error.fields[key].flatMap { Int64($0) } ?? error.details?[key]?.double.flatMap { Int64(exactly: $0) }
    }
}

// MARK: - Pure decisions

/// What the upload sends and in what order, decided from the record alone (unit tested).
nonisolated enum RecordingDayUpload {
    /// Where a placed sentence is: its chunk and its index in the chunk's `sentences`.
    nonisolated struct Ref: Sendable, Equatable {
        var seq: Int
        var index: Int
    }

    /// A part's last stored append: when, and how many positions it had then. Text that was waiting already goes on at
    /// once (a backlog in batches); text placed since waits out the throttle.
    nonisolated struct LastAppend: Sendable, Equatable {
        var at: Date
        var count: Int
    }

    /// The next thing a part needs.
    nonisolated enum Step: Sendable, Equatable {
        /// A create or repair `PUT` (one decided on and not stored yet goes again, with its revision).
        case put
        /// Text without a position (from a build before positions were given in the transcriber's save): place it.
        case assign
        /// Send positions `from..<to` (also a retry of ones assigned before).
        case append(from: Int, to: Int)
        /// Only the other fields changed (coverage, duration, the end): an append without segments.
        case meta
        /// Nothing until then (the append throttle).
        case wait(Date)
        case done
    }

    /// An append's body stays under the contract's 1 MiB with room for its coverage and bookmarks.
    static let maxAppendBytes = 900_000
    /// A sentence longer than this (UTF-8) is split before it gets a position (docs/assistant/day-recap.md「Appending」).
    static let maxSentenceBytes = 64 * 1024
    /// What one hub recording of a day takes (docs/assistant/day-recap.md「Parts」): past it the day goes on in a next part.
    static let maxPartSegments = 20_000, maxPartTextBytes = 4 * 1024 * 1024
    /// The contract's part numbers.
    static let maxParts = 99
    /// JSON around one segment's text (`{"start":…,"end":…,"text":""}`), counted generously.
    private static let segmentOverhead = 80

    /// The revision for a new request: the phone's clock in milliseconds, above every revision this part sent, its base,
    /// and every one the hub was seen to hold.
    static func revision(_ part: RecordingDayPart, now: Date, floor: Int64 = 0) -> Int64 {
        max(millis(now), part.maxSentRevision + 1, (part.base ?? 0) + 1, max(floor, part.hubRevision ?? 0) + 1)
    }

    static func millis(_ date: Date) -> Int64 {
        let ms = (date.timeIntervalSince1970 * 1000).rounded()
        return ms.isFinite ? Int64(ms) : 0
    }

    /// The sentences placed in `part`, by position, and whether no position is held twice.
    static func layout(_ record: RecordingDayRecord, part: Int) -> (placed: [Int: Ref], clean: Bool) {
        var placed: [Int: Ref] = [:]
        var clean = true
        for (seq, chunk) in record.chunks {
            let positions = chunk.positions ?? []
            for index in 0..<min(positions.count, chunk.sentences?.count ?? 0) where positions[index].part == part {
                if placed.updateValue(Ref(seq: seq, index: index), forKey: positions[index].pos) != nil { clean = false }
            }
        }
        return (placed, clean)
    }

    /// The step `record.parts[index]` needs next. `isLast`: the part new text goes to. `lastAppend`: this part's last
    /// stored append; `sentMeta`: what its last stored request said. `force`: no throttle (the catch-up, a day or part
    /// that ended). `limit`: segments per append (fewer after `413 too_large`).
    static func step(_ record: RecordingDayRecord, partIndex index: Int, isLast: Bool, now: Date, lastAppend: LastAppend?,
                     sentMeta: RecordingDayMeta?, force: Bool, limit: Int = RecordingDayContract.maxAppendSegments) -> Step {
        let part = record.parts[index]
        guard part.created, part.pendingPut == nil, !part.needsRepair else { return .put }
        let layout = layout(record, part: part.part)
        // Each position below the count held exactly once, none past it: anything else only a full replacement mends
        // (a position is never given again except by a `PUT`).
        guard layout.clean, layout.placed.count == part.count,
              layout.placed.keys.allSatisfy({ (0..<part.count).contains($0) }) else { return .put }
        let throttled = !force && part.endedAt == nil && record.closedAt == nil
            && lastAppend.map { now.timeIntervalSince($0.at) < RecordingDayContract.appendInterval } == true
        let due = lastAppend?.at.addingTimeInterval(RecordingDayContract.appendInterval) ?? now
        if part.next < part.count {
            let to = batchEnd(record, placed: layout.placed, from: part.next, count: part.count, limit: limit)
            // Text placed since the last append waits out the throttle, so a recording day is appended to at most every
            // `appendInterval`; text that was waiting already (a backlog going in batches) goes on at once.
            let placedSince = lastAppend.map { part.next >= $0.count } ?? true
            return throttled && placedSince ? .wait(due) : .append(from: part.next, to: to)
        }
        if isLast, record.chunks.values.contains(where: \.hasUnplacedText) { return .assign }
        if part.complete { return .done }
        guard meta(record, partIndex: index, sent: part.next, now: now) != sentMeta else { return .done }
        return throttled ? .wait(due) : .meta
    }

    /// The end of a batch from `from`: at most `limit` segments and `maxAppendBytes` of text.
    static func batchEnd(_ record: RecordingDayRecord, placed: [Int: Ref], from: Int, count: Int, limit: Int) -> Int {
        var end = from
        var bytes = 0
        while end < count, end - from < max(1, limit) {
            let size = placed[end].flatMap { record.chunks[$0.seq]?.sentences?[$0.index].text.utf8.count } ?? 0
            if end > from, bytes + size + segmentOverhead > maxAppendBytes { break }
            bytes += size + segmentOverhead
            end += 1
        }
        return end
    }

    /// The segments at positions `from..<to` of `part`, in position order; nil when one is missing.
    static func segments(_ record: RecordingDayRecord, placed: [Int: Ref], from: Int, to: Int) -> [RecordingDaySegment]? {
        var out: [RecordingDaySegment] = []
        out.reserveCapacity(max(0, to - from))
        for pos in from..<max(from, to) {
            guard let ref = placed[pos], let chunk = record.chunks[ref.seq], let sentences = chunk.sentences,
                  sentences.indices.contains(ref.index) else { return nil }
            out.append(segment(sentences[ref.index], of: chunk, since: record.startedAt))
        }
        return out
    }

    /// A chunk-relative sentence on the day's clock: wall-clock seconds since `started_at`, pauses included (the
    /// contract: "started_at + start is the moment it was said"). Every part keeps part 1's `started_at` and a chunk
    /// never starts before it, so a time is never negative; `max(0, …)` only guards a time no JSON can carry.
    static func segment(_ sentence: RecordingSentence, of chunk: RecordingDayChunk, since base: Date) -> RecordingDaySegment {
        let offset = chunk.startedAt.timeIntervalSince(base)
        let start = max(0, finite(offset + sentence.start, or: offset))
        return RecordingDaySegment(start: start, end: max(start, finite(offset + sentence.end, or: start)), text: sentence.text)
    }

    /// Gives every sentence without a position the next positions of the part that takes the day's new text (part 1,
    /// made here when there is none yet), long ones split first: called by the transcriber inside the update that saves
    /// the text, so the text and its positions reach the disk in one write (docs/assistant/day-recap.md「Appending」).
    static func place(_ record: inout RecordingDayRecord) {
        guard record.chunks.values.contains(where: \.hasUnplacedText) else { return }
        if record.parts.isEmpty { record.parts = [RecordingDayPart(hubID: record.id, part: 1, startedAt: record.startedAt)] }
        guard let index = takingIndex(record) else { return }
        assign(&record, partIndex: index)
    }

    /// The part new text goes to: the highest one the hub hasn't deleted.
    static func takingIndex(_ record: RecordingDayRecord) -> Int? {
        record.parts.indices.filter { !record.parts[$0].deleted }.max { record.parts[$0].part < record.parts[$1].part }
    }

    /// Gives every sentence without a position the next positions of `record.parts[index]`, chunks in seq order and
    /// sentences in their order; a sentence over `maxSentenceBytes` is split first (it has no position yet, so nothing
    /// on the hub moves).
    static func assign(_ record: inout RecordingDayRecord, partIndex index: Int) {
        let part = record.parts[index].part
        var count = record.parts[index].count
        for seq in record.chunks.keys.sorted() {
            guard var chunk = record.chunks[seq], chunk.hasUnplacedText, var sentences = chunk.sentences else { continue }
            var positions = chunk.positions ?? []
            let placed = positions.count
            sentences = Array(sentences[..<placed]) + sentences[placed...].flatMap { split($0) }
            while positions.count < sentences.count {
                positions.append(RecordingDayPosition(part: part, pos: count))
                count += 1
            }
            chunk.sentences = sentences
            chunk.positions = positions
            record.chunks[seq] = chunk
        }
        record.parts[index].count = count
    }

    /// A sentence over `maxBytes` of UTF-8 as pieces under it, cut at the last space before the limit when there is one
    /// (else at a character boundary), each piece with its share of the sentence's time by length.
    static func split(_ sentence: RecordingSentence, maxBytes: Int = maxSentenceBytes) -> [RecordingSentence] {
        let total = sentence.text.utf8.count
        guard total > maxBytes, maxBytes > 0 else { return [sentence] }
        var pieces: [Substring] = []
        var rest = sentence.text[...]
        while rest.utf8.count > maxBytes {
            var cut = rest.startIndex
            var bytes = 0
            for index in rest.indices {
                let size = rest[index].utf8.count
                guard bytes + size <= maxBytes else { break }
                bytes += size
                cut = rest.index(after: index)
            }
            if let space = rest[..<cut].lastIndex(where: \.isWhitespace), space > rest.startIndex { cut = rest.index(after: space) }
            // A single character wider than the limit (it can't be: a character is a few bytes) still moves on.
            if cut == rest.startIndex { cut = rest.index(after: cut) }
            pieces.append(rest[..<cut])
            rest = rest[cut...]
        }
        pieces.append(rest)
        let span = finite(sentence.end - sentence.start, or: 0)
        var done = 0
        return pieces.map { piece in
            let start = sentence.start + span * Double(done) / Double(total)
            done += piece.utf8.count
            let end = sentence.start + span * Double(done) / Double(total)
            return RecordingSentence(start: start, end: end, text: String(piece))
        }
    }

    /// Numbers `part`'s sentences `0..<n` in their current order (a `PUT` carries them so), and returns n. Holes
    /// close and positions past a chunk's sentences (left by a build that crashed mid-write) go; on a clean layout
    /// nothing changes. Only a `PUT` may do this: the hub's copy is replaced by the same request.
    @discardableResult static func renumber(_ record: inout RecordingDayRecord, part: Int) -> Int {
        var refs: [(pos: Int, seq: Int, index: Int)] = []
        for (seq, chunk) in record.chunks {
            guard var positions = chunk.positions else { continue }
            let kept = chunk.sentences?.count ?? 0
            if positions.count > kept {
                positions.removeLast(positions.count - kept)
                record.chunks[seq]?.positions = positions.isEmpty ? nil : positions
            }
            for (index, position) in positions.enumerated() where position.part == part { refs.append((position.pos, seq, index)) }
        }
        refs.sort { ($0.pos, $0.seq, $0.index) < ($1.pos, $1.seq, $1.index) }
        for (offset, ref) in refs.enumerated() { record.chunks[ref.seq]?.positions?[ref.index].pos = offset }
        return refs.count
    }

    /// `413 day_full` on an append: the positions of `part` from `held` on never reached the hub, so they move to the
    /// part `target`, after its count, in their order. A chunk's positions stay paired with its sentences in place.
    static func move(_ record: inout RecordingDayRecord, part: Int, from held: Int, to target: Int) {
        guard let t = record.parts.firstIndex(where: { $0.part == target }) else { return }
        var refs: [(pos: Int, seq: Int, index: Int)] = []
        for (seq, chunk) in record.chunks {
            for (index, position) in (chunk.positions ?? []).enumerated() where position.part == part && position.pos >= held {
                refs.append((position.pos, seq, index))
            }
        }
        refs.sort { ($0.pos, $0.seq, $0.index) < ($1.pos, $1.seq, $1.index) }
        var count = record.parts[t].count
        for ref in refs {
            record.chunks[ref.seq]?.positions?[ref.index] = RecordingDayPosition(part: target, pos: count)
            count += 1
        }
        record.parts[t].count = count
    }

    /// The part to move into on an append's `413 day_full`: the part after `number` that takes new text, made with a new
    /// id when `number` was the last (on the day's clock, `started_at` part 1's). nil past the contract's 99 parts.
    static func nextPart(_ record: inout RecordingDayRecord, after number: Int, newID: UUID) -> Int? {
        if let taking = takingIndex(record), record.parts[taking].part > number { return record.parts[taking].part }
        let next = (record.parts.map(\.part).max() ?? number) + 1
        guard next <= maxParts else { return nil }
        record.parts.append(RecordingDayPart(hubID: newID, part: next, startedAt: record.startedAt))
        return next
    }

    /// A repair `PUT` of part `number` answered `413 day_full` (docs/assistant/day-recap.md「Parts」): that part and the
    /// ones after it are packed again in position order, the refused part with fewer sentences than it was refused with
    /// and the others with what the hub's limits allow, reusing their ids in order and new ones only after the last.
    /// Every part whose sentences changed gets a `PUT` (`needsRepair`); every part but the last is closed. The plan is
    /// the record itself, written before any of those `PUT`s, so a crash carries it on. False when it can't be done (a
    /// refused part of one sentence, or past 99 parts): nothing changed then.
    static func repartition(_ record: inout RecordingDayRecord, from number: Int, refused: Int, now: Date,
                            newID: () -> UUID) -> Bool {
        let numbers = record.parts.filter { $0.part >= number && !$0.expired && !$0.deleted }.map(\.part).sorted()
        guard numbers.first == number else { return false }
        let affected = Set(numbers)
        var refs: [(part: Int, pos: Int, seq: Int, index: Int, bytes: Int)] = []
        for (seq, chunk) in record.chunks {
            let sentences = chunk.sentences ?? []
            for (index, position) in (chunk.positions ?? []).enumerated() where index < sentences.count && affected.contains(position.part) {
                let bytes = sentences[index].text.trimmingCharacters(in: .whitespacesAndNewlines).utf8.count
                refs.append((position.part, position.pos, seq, index, bytes))
            }
        }
        refs.sort { ($0.part, $0.pos, $0.seq, $0.index) < ($1.part, $1.pos, $1.seq, $1.index) }
        // Fewer than it was refused with, and never more than the hub takes in one part: a part an offline phone filled
        // past the limit (`assign` doesn't stop at it) fits at the first `PUT` instead of after several refused ones.
        let firstCap = min(maxPartSegments, refused - max(1, refused / 10))
        guard firstCap > 0 else { return false }
        var groups: [[Ref]] = []
        var rest = refs[...]
        var cap = firstCap
        while !rest.isEmpty {
            var taken = 0, bytes = 0
            for ref in rest {
                guard taken < cap, bytes + ref.bytes <= maxPartTextBytes else { break }
                taken += 1
                bytes += ref.bytes
            }
            guard taken > 0 else { return false }
            groups.append(rest.prefix(taken).map { Ref(seq: $0.seq, index: $0.index) })
            rest = rest.dropFirst(taken)
            cap = maxPartSegments
        }
        while groups.count < numbers.count { groups.append([]) }
        let highest = record.parts.map(\.part).max() ?? number
        let extra = groups.count - numbers.count
        guard highest + extra <= maxParts else { return false }
        let targets = numbers + (0..<extra).map { highest + 1 + $0 }
        var before: [Int: [Ref]] = [:]
        for ref in refs { before[ref.part, default: []].append(Ref(seq: ref.seq, index: ref.index)) }
        for (g, group) in groups.enumerated() {
            let target = targets[g]
            for (pos, ref) in group.enumerated() { record.chunks[ref.seq]?.positions?[ref.index] = RecordingDayPosition(part: target, pos: pos) }
            if !record.parts.contains(where: { $0.part == target }) {
                record.parts.append(RecordingDayPart(hubID: newID(), part: target, startedAt: record.startedAt))
            }
            guard let i = record.parts.firstIndex(where: { $0.part == target }) else { continue }
            if before[target, default: []] != group || !record.parts[i].created { record.parts[i].needsRepair = true }
            record.parts[i].count = group.count
            record.parts[i].next = min(record.parts[i].next, group.count)
            if g < groups.count - 1 { record.parts[i].endedAt = record.parts[i].endedAt ?? now }
        }
        return true
    }

    /// What a request for `record.parts[index]` says beside its segments, the hub holding its positions below `sent`
    /// after it. Every part carries the day's duration and bookmarks: parts are storage, not stretches of the day.
    static func meta(_ record: RecordingDayRecord, partIndex index: Int, sent: Int, now: Date) -> RecordingDayMeta {
        let part = record.parts[index]
        let coverage = RecordingDayCoverage.compute(record: record, part: part, sentToPositionBelow: sent, now: now)
        let complete = RecordingDayCoverage.isComplete(record: record, part: part, sentToPositionBelow: sent, coverage: coverage)
        let duration = record.chunks.values.reduce(0.0) { sum, chunk in
            chunk.duration > 0 && !chunk.isErasedOrLost ? sum + chunk.duration : sum
        }
        let bookmarks = record.marks.map { mark in
            RecordingDayBookmark(at: max(0, finite(mark.at.timeIntervalSince(record.startedAt), or: 0)), label: mark.label)
        }.sorted { ($0.at, $0.label) < ($1.at, $1.label) }
        return RecordingDayMeta(endedAt: part.endedAt ?? record.closedAt, duration: duration, bookmarks: bookmarks,
                                coverage: coverage, complete: complete)
    }

    private static func finite(_ value: Double, or fallback: Double) -> Double { value.isFinite ? value : fallback }
}

// MARK: - The upload

/// Uploads every day the phone holds to the hub (docs/assistant/day-recap.md「Phone to hub」), one request at a time.
/// Follows the day store's changes (a chunk stored, text transcribed, an event, an erase): appends go at most every
/// `appendInterval` while the day records, at once once it ended. The hub's word on a day comes from `RecordingSync`'s
/// reconciliation (`reconciled`), the flag from each `hello` (`helloReceived`).
@MainActor @Observable final class RecordingDaySync {
    static let shared = RecordingDaySync()

    /// How a day's visit ended.
    nonisolated enum Outcome: Sendable, Equatable {
        /// Nothing more to send now.
        case done
        /// Look again then (the append throttle, a missing seq's wait).
        case later(Date)
        /// The hub or the network failed: again after a backoff.
        case failed
        /// Waits for something else: the flag, a hello, the app in front, the user.
        case blocked
        case cancelled
    }

    /// The day whose text is on its way now.
    private(set) var uploading: UUID?

    @ObservationIgnored private let store: RecordingDayStore
    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private let api: () -> APIClient?
    @ObservationIgnored private let features: () -> Set<String>
    @ObservationIgnored private let now: () -> Date
    @ObservationIgnored private let days: () -> Int
    @ObservationIgnored private let appState: RecordingDayAppState
    @ObservationIgnored private let noteParts: (UUID, [UUID]) -> Void
    @ObservationIgnored private let removed: (UUID) -> Void
    @ObservationIgnored private let removeEverywhere: (UUID) -> Void
    @ObservationIgnored private let recapFinal: () -> Void
    @ObservationIgnored private let debounce: Duration

    @ObservationIgnored private var started = false
    @ObservationIgnored private var listening: Task<Void, Never>?
    @ObservationIgnored private var worker: Task<Void, Never>?
    @ObservationIgnored private var timer: Task<Void, Never>?
    /// Days to visit with the next pass.
    @ObservationIgnored private var dirty: Set<UUID> = []
    /// Days to visit again then.
    @ObservationIgnored private var waits: [UUID: Date] = [:]
    /// Days whose last visit failed, and when they may go again (5 s doubling to 10 min). A change to such a day
    /// waits for it too: the upload's own bookkeeping changes the day with every request, and a failing hub or network is
    /// not asked again at once.
    @ObservationIgnored private var retry: [UUID: Backoff] = [:]
    /// Days the last visit found with nothing to send (left out of a foreground sweep until they change).
    @ObservationIgnored private var settled: Set<UUID> = []
    /// Days with a hub recording this process saw created (reconciliation's absence check applies to them).
    @ObservationIgnored private var knownCreated: Set<UUID> = []
    /// Days an older manager refused since the last hello, and days with a part the hub refused (`400`) since then: a
    /// relaunch or a hello lets each try once more, never a change of the day alone.
    @ObservationIgnored private var unsupportedSeen: Set<UUID> = []
    @ObservationIgnored private var rejectedSeen: Set<UUID> = []
    /// `403`: nothing goes until the app is in front again, a hello, or a retry.
    @ObservationIgnored private(set) var paused = false
    /// What the last hello said about the flag; nil before the launch's first.
    @ObservationIgnored private var wasOffered: Bool?
    /// Per hub recording: its last stored append, when its last request was stored, what that said, the batch size
    /// after `413 too_large`, and the highest revision the hub was seen to hold.
    @ObservationIgnored private var lastAppend: [UUID: RecordingDayUpload.LastAppend] = [:]
    @ObservationIgnored private var lastStored: [UUID: Date] = [:]
    @ObservationIgnored private var sentMeta: [UUID: RecordingDayMeta] = [:]
    @ObservationIgnored private var batch: [UUID: Int] = [:]
    @ObservationIgnored private var hubRevision: [UUID: Int64] = [:]
    /// Reconciliation's word, applied at the day's next visit (one request at a time).
    @ObservationIgnored private var pendingRows: [UUID: (rows: [RecordingDayHubRow], at: Date)] = [:]
    @ObservationIgnored private var noted: [UUID: [UUID]] = [:]
    /// Per day, its created parts as the last visit left them, to tell whether reconciliation says anything new without
    /// reading the day.
    @ObservationIgnored private var believed: [UUID: [RecordingDayHubRow]] = [:]
    @ObservationIgnored private var grant: RecordingDayGrant?
    /// The catch-up runs (`drain`): no throttle, its task is the upload's time, and it alone starts the worker meanwhile.
    @ObservationIgnored private var draining = false

    private nonisolated struct Backoff: Sendable {
        var failures: Int
        var at: Date
    }

    /// Steps one part may take in one visit (a backlog of 20000 sentences is 40 appends).
    private static let maxSteps = 200
    /// Reconciliation's list may predate a request stored this long before it reached the upload (the list's own
    /// request): such a part's base and count are the phone's newer word, and it may not be listed yet.
    private static let listSlack: TimeInterval = 60
    /// `stale` answers to a `PUT` in one visit before backing off, and `PUT`s of one part in one visit (a create, a
    /// repair, one more after the hub's answer disagreed): past them a hub that keeps disagreeing waits for the backoff.
    private static let maxStale = 3, maxPuts = 4

    /// - Parameters:
    ///   - features: the hub's features from its last `hello` (empty: not known, the cached flag decides).
    ///   - days: the hub's `recording_days` (`RecordingSync`): an older day is not uploaded.
    ///   - noteParts: a day's hub ids past its first part, for deletes (`RecordingSync.noteDayParts`).
    ///   - removed: a day was removed here (cancels its transcription).
    ///   - removeEverywhere: the hub deleted a part of a day (`410`): the whole day goes, here, on the hub and on the
    ///     watch (`RecordingSync.delete`, which comes back through `removedHere`).
    ///   - recapFinal: a day's final recap time became known (the keeper's 3-day clock).
    init(store: RecordingDayStore = .shared, library: RecordingLibrary = .shared,
         api: @escaping () -> APIClient? = { AppModel.shared.api },
         features: @escaping () -> Set<String> = { AppModel.shared.hubFeatures },
         now: @escaping () -> Date = { .now },
         days: @escaping () -> Int = { RecordingSync.shared.store.days },
         appState: RecordingDayAppState = RecordingDaySystemState.shared,
         noteParts: @escaping (UUID, [UUID]) -> Void = { RecordingSync.shared.noteDayParts($0, $1) },
         removed: @escaping (UUID) -> Void = { RecordingDayTranscriber.shared.cancel($0) },
         removeEverywhere: @escaping (UUID) -> Void = { try? RecordingSync.shared.delete($0, day: true) },
         recapFinal: @escaping () -> Void = { RecordingDayKeeper.shared.deadlineChanged() },
         debounce: Duration = .seconds(1)) {
        self.store = store; self.library = library; self.api = api; self.features = features; self.now = now
        self.days = days; self.appState = appState; self.noteParts = noteParts; self.removed = removed
        self.removeEverywhere = removeEverywhere; self.recapFinal = recapFinal; self.debounce = debounce
    }

    // MARK: Triggers

    /// Follows the day store from now on (launch, before WatchConnectivity activates). Idempotent.
    func start() {
        guard !started else { return }
        started = true
        let changes = store.changes
        listening = Task { [weak self] in
            for await id in changes { self?.changed(id) }
        }
    }

    /// A day changed (a chunk, its text, an event, an erase, its library row): visited soon.
    func changed(_ id: UUID) {
        settled.remove(id)
        dirty.insert(id)
        kick()
    }

    /// The app came to the front: a `403` pause ends, a failed day goes now, and every day not known to be done is
    /// visited.
    func foreground() {
        paused = false
        releaseBackoff()
        dirty.formUnion(dayIDs().filter { !settled.contains($0) })
        kick()
    }

    /// The app went to the background: a pass in progress goes on only inside a grant; none given stops it now (its
    /// request is cancelled, and the day goes again when the upload may work again).
    func background() {
        guard let worker, !draining else { return }
        guard ensureGrant() else {
            worker.cancel()
            return
        }
    }

    /// The hub's `hello` (hub group only): the pause ends, a day an older manager refused and a part the hub refused are
    /// tried once more, a failed one goes now, and when the flag is new (or this is the launch's first hello) every day
    /// is visited: one may have waited days for it. A reconnect to the same hub visits nothing else.
    func helloReceived(offered: Bool) {
        let refused = unsupportedSeen.union(rejectedSeen)
        unsupportedSeen = []
        rejectedSeen = []
        paused = false
        defer { wasOffered = offered }
        guard offered else { return }
        if wasOffered != true {
            settled = []
            dirty.formUnion(dayIDs())
        }
        dirty.formUnion(refused)
        releaseBackoff()
        kick()
    }

    /// Reconciliation's rows of day recordings, by the local day each belongs to (`RecordingSync.reconcile`, and the
    /// day's page as it polls the list). A local day may be named with no rows when the hub listed none of its parts: a
    /// part this phone created that the hub doesn't list any more (a new hub) is created again.
    func reconciled(_ rows: [UUID: [RecordingDayHubRow]]) {
        let at = now()
        for (day, list) in rows where !list.isEmpty || knownCreated.contains(day) {
            // The hub says what the phone already holds (the usual case, every ten minutes): no day to read.
            guard !agrees(day, list) else { continue }
            pendingRows[day] = (list, at)
            dirty.insert(day)
        }
        kick()
    }

    /// Whether reconciliation's rows for a day are exactly its parts as the phone last left them.
    private func agrees(_ day: UUID, _ rows: [RecordingDayHubRow]) -> Bool {
        guard let parts = believed[day], !parts.isEmpty, parts.count == rows.count else { return false }
        let byID = Dictionary(rows.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        return parts.allSatisfy { part in
            guard let row = byID[part.id] else { return false }
            return row.base == part.base && row.next == part.next && row.recapState == part.recapState
                && (row.recapFinal ?? part.recapFinal) == part.recapFinal && (row.recapFinalAt ?? part.recapFinalAt) == part.recapFinalAt
        }
    }

    /// The day was deleted here (`RecordingSync.delete`: by the user, or because the hub deleted a part of it) or purged
    /// (`RecordingDayKeeper`): forgotten by the upload, its transcription stopped, the store's copy let go of, and the
    /// watch asked to delete its copy until it confirms (docs/assistant/day-recap.md「Deleting」; the store keeps that
    /// request outside the day's folder, so it outlives the delete).
    func removedHere(_ id: UUID) {
        forget(id)
        removed(id)
        store.didRemove(id)
        let store = store
        Task { await store.requestWatchDelete(id) }
    }

    /// The day's language changed (`RecordingDayTranscriber.setLocale`, after its chunks were reset). Only a `PUT` carries
    /// a language, so every part the hub has, or is being given, gets a repair decided now and on disk before this
    /// returns (a relaunch still sends it), whether or not any of its text changed: audio retention deleted may have left
    /// every sentence as it was, and a silent day has none. Its revision is above a `PUT` in flight, so that one's
    /// answer doesn't clear it.
    func languageChanged(_ id: UUID) async {
        let at = now()
        let floors = hubRevision
        await store.update(id) { record in
            for i in record.parts.indices where !record.parts[i].expired && !record.parts[i].deleted
                && (record.parts[i].created || record.parts[i].pendingPut != nil) {
                let part = record.parts[i]
                let revision = max(RecordingDayUpload.revision(part, now: at, floor: floors[part.hubID] ?? 0), (part.pendingPut ?? 0) + 1)
                record.parts[i].pendingPut = revision
                record.parts[i].maxSentRevision = max(part.maxSentRevision, revision)
            }
        }
        _ = await store.flush(id)
        changed(id)
    }

    /// A pass soon (several changes in a row become one), when a day may go now and the upload may work: in front, or in
    /// the background inside a grant. While the catch-up runs, it alone starts passes (`drain`).
    func kick(immediately: Bool = false) {
        guard worker == nil, !draining, !dueDays().isEmpty else { return }
        guard appState.inForeground || ensureGrant() else { return }
        startWorker(immediately: immediately)
    }

    /// Visits one day now, without debounce, throttle or backoff wait (tests).
    @discardableResult func syncNow(_ id: UUID, force: Bool = false) async -> Outcome {
        while let worker { await worker.value }
        let outcome = await visit(id, force: force)
        note(outcome, for: id)
        return outcome
    }

    /// Returns once no pass is running (tests).
    func whenIdle() async {
        while let worker { await worker.value }
    }

    /// The background catch-up: every day the phone holds, throttle aside, inside the catch-up's own time; a day that
    /// failed before gets one more try. True when something is left to send. Cancelled, nothing starts again after it.
    func drain() async -> Bool {
        draining = true
        defer { draining = false }
        dirty.formUnion(await store.ids())
        releaseBackoff()
        while !Task.isCancelled {
            // A pass already going (in front, or in a grant) becomes the catch-up's; the next ones start here.
            if worker == nil {
                guard !dueDays().isEmpty else { break }
                startWorker(immediately: true)
            }
            guard let pass = worker else { break }
            await withTaskCancellationHandler { await pass.value } onCancel: { pass.cancel() }
        }
        return !retry.isEmpty || !waits.isEmpty || !dirty.isEmpty
    }

    // MARK: Passes

    @discardableResult private func startWorker(immediately: Bool) -> Task<Void, Never> {
        let wait = immediately ? nil : debounce
        let task = Task<Void, Never> { [weak self] in await self?.runPasses(debounce: wait) }
        worker = task
        return task
    }

    /// The upload's grant of background time: the one it holds, or a new one; false when the system gives none.
    private func ensureGrant() -> Bool {
        if grant == nil { grant = appState.beginGrant(named: "recording-day-sync") { [weak self] in self?.grantExpired() } }
        return grant != nil
    }

    /// Whether the next visit may go: the app in front, the upload's grant, or the catch-up.
    private var mayWork: Bool { draining || appState.inForeground || grant != nil }

    /// Visits the days that may go, until none is left. Ends at once when its time does: the app left the front without a
    /// grant, the grant or the catch-up ran out. The cleanup starts nothing; what is left waits for its timer or the
    /// next change, foreground, hello or catch-up.
    private func runPasses(debounce: Duration?) async {
        var interrupted = false
        defer { finish() }
        if let debounce { try? await Task.sleep(for: debounce) }
        while !interrupted {
            let ids = dueDays()
            guard !ids.isEmpty else { return }
            dirty.subtract(ids)
            for (i, id) in ids.enumerated() {
                guard !Task.isCancelled, mayWork else {
                    dirty.formUnion(ids[i...])
                    interrupted = true
                    break
                }
                note(await visit(id, force: draining), for: id)
            }
        }
    }

    private func finish() {
        worker = nil
        grant?.end()
        grant = nil
        scheduleWake()
    }

    /// The grant ran out: stop at once (the request in flight is cancelled; its day is visited again later).
    private func grantExpired() {
        grant = nil
        worker?.cancel()
    }

    private func note(_ outcome: Outcome, for id: UUID) {
        waits[id] = nil
        switch outcome {
        case .done:
            retry[id] = nil
            settled.insert(id)
        case .later(let at):
            retry[id] = nil
            // More steps than one visit takes: on with the next pass, after the other days.
            if at <= now() { dirty.insert(id) } else { waits[id] = at }
        case .failed:
            backOff(id)
        case .cancelled:
            // Stopped because its time ran out: again with the next pass. A request the system cancelled by itself counts
            // as a failure, so it can't come round again at once for ever.
            if Task.isCancelled || !mayWork { dirty.insert(id) } else { backOff(id) }
        case .blocked:
            retry[id] = nil
        }
    }

    private func backOff(_ id: UUID) {
        let failures = (retry[id]?.failures ?? 0) + 1
        retry[id] = Backoff(failures: failures, at: now().addingTimeInterval(Self.backoff(after: failures)))
    }

    /// The wait after `failures` failed visits of a day in a row: 5 s, doubling to 10 min.
    static func backoff(after failures: Int) -> TimeInterval {
        min(600, 5 * pow(2, Double(min(max(failures, 1), 8) - 1)))
    }

    /// Failed days may go at once (the app in front, a hello, the catch-up). Their count stays, so another failure waits
    /// longer than the last.
    private func releaseBackoff() {
        let at = now()
        for id in Array(retry.keys) { retry[id]?.at = at }
        dirty.formUnion(retry.keys)
    }

    /// Dirty days that may go now, in id order: a day waiting out its backoff doesn't, whatever changed.
    private func dueDays() -> [UUID] {
        let at = now()
        return dirty.filter { retry[$0].map { $0.at <= at } ?? true }.sorted { $0.uuidString < $1.uuidString }
    }

    /// A timer for the soonest throttle or gap wait, or backoff.
    private func scheduleWake() {
        timer?.cancel()
        timer = nil
        guard let at = (Array(waits.values) + retry.values.map(\.at)).min() else { return }
        let delay = max(0, at.timeIntervalSince(now()))
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            self?.wake()
        }
    }

    /// The timer fired: the days whose wait ended go, if the upload may work now (`kick`). With nothing due yet (the
    /// timer came early), the next one; with no time to work, the next change or foreground.
    private func wake() {
        timer = nil
        let at = now()
        for (id, date) in waits where date <= at {
            waits[id] = nil
            dirty.insert(id)
        }
        for (id, backoff) in retry where backoff.at <= at { dirty.insert(id) }
        if dueDays().isEmpty { scheduleWake() } else { kick(immediately: true) }
    }

    /// Day ids from the library's index rows (in memory; a day's record is read only when visited).
    private func dayIDs() -> [UUID] { library.entries.filter(\.isDay).map(\.id) }

    private func forget(_ id: UUID) {
        dirty.remove(id); waits[id] = nil; retry[id] = nil; settled.remove(id); knownCreated.remove(id)
        unsupportedSeen.remove(id); rejectedSeen.remove(id); pendingRows[id] = nil; noted[id] = nil; believed[id] = nil
    }

    // MARK: One day

    private func visit(_ id: UUID, force: Bool) async -> Outcome {
        if let rows = pendingRows.removeValue(forKey: id) { await apply(rows.rows, listedAt: rows.at, to: id) }
        guard !paused, let api = api(), !api.config.isDemo else { return .blocked }
        guard RecordingDayDefaults.offered(features()) else { return .blocked }
        guard let record = await store.record(id) else { return .done }
        guard record.problem == nil else { return .blocked }
        if record.unsupportedUntilHello {
            guard !unsupportedSeen.contains(id) else { return .blocked }
            // Refused before this hello (or this launch): one more try.
            await store.update(id) { $0.unsupportedUntilHello = false }
        }
        // Past the hub's window nothing is uploaded (it would answer `expired`); the keeper purges it.
        guard record.startedAt >= now().addingTimeInterval(-Double(days()) * RecordingDayRecord.secondsPerDay) else { return .done }
        await prepare(id, retryRejected: !rejectedSeen.contains(id))
        guard let prepared = await store.record(id) else { return .done }
        notePartsIfNeeded(id, prepared)
        var soonest: Date?
        var number = prepared.parts.map(\.part).min() ?? 1
        while let part = await store.record(id)?.parts.first(where: { $0.part == number }) {
            if !part.expired, !part.deleted, part.problem == nil {
                switch await visit(id, part: number, api: api, force: force) {
                case .done: break
                case .later(let at): soonest = min(soonest ?? at, at)
                case let other: return other
                }
            }
            number += 1
        }
        guard let latest = await store.record(id) else { return .done }
        if latest.parts.contains(where: \.created) { knownCreated.insert(id) }
        believed[id] = latest.parts.filter { $0.created && !$0.expired && !$0.deleted }.map {
            RecordingDayHubRow(id: $0.hubID, part: $0.part, next: $0.next, base: $0.base, recapState: $0.recapState,
                               recapFinal: $0.recapFinal, recapFinalAt: $0.recapFinalAt)
        }
        if let expiry = RecordingDayCoverage.nextGapExpiry(record: latest, now: now()) { soonest = min(soonest ?? expiry, expiry) }
        return soonest.map(Outcome.later) ?? .done
    }

    /// Before a day's parts go, in one change: its first part when it has none (a create `PUT` with no segments), every
    /// part on the day's clock (part 1's `started_at`; should the watch ever name an earlier start, each part is
    /// replaced with its times on the new clock), and a part the hub refused (`400`) let try again with a full
    /// replacement once per hello or launch, or when an erase changed its text since.
    private func prepare(_ id: UUID, retryRejected: Bool) async {
        await store.update(id) { record in
            if record.parts.isEmpty { record.parts = [RecordingDayPart(hubID: id, part: 1, startedAt: record.startedAt)] }
            for i in record.parts.indices {
                if record.parts[i].startedAt != record.startedAt {
                    record.parts[i].startedAt = record.startedAt
                    if record.parts[i].created { record.parts[i].needsRepair = true }
                }
                if record.parts[i].problem != nil, retryRejected || record.parts[i].needsRepair {
                    record.parts[i].problem = nil
                    record.parts[i].needsRepair = true
                }
            }
        }
    }

    /// One part's steps until it has nothing more to send now.
    private func visit(_ id: UUID, part number: Int, api: APIClient, force: Bool) async -> Outcome {
        var stale = 0, puts = 0
        for _ in 0..<Self.maxSteps {
            guard !Task.isCancelled, mayWork else { return .cancelled }
            guard let record = await store.record(id), let index = record.parts.firstIndex(where: { $0.part == number }) else { return .done }
            let part = record.parts[index]
            guard !part.expired, !part.deleted, part.problem == nil, record.problem == nil, !record.unsupportedUntilHello else { return .done }
            let isLast = RecordingDayUpload.takingIndex(record) == index
            let limit = batch[part.hubID] ?? RecordingDayContract.maxAppendSegments
            let step = RecordingDayUpload.step(record, partIndex: index, isLast: isLast, now: now(), lastAppend: lastAppend[part.hubID],
                                               sentMeta: sentMeta[part.hubID], force: force || record.closedAt != nil, limit: limit)
            let flow: Flow
            switch step {
            case .done: return .done
            case .wait(let at): return .later(at)
            case .assign:
                await store.update(id) { record in
                    guard let i = RecordingDayUpload.takingIndex(record), record.parts[i].part == number else { return }
                    RecordingDayUpload.assign(&record, partIndex: i)
                }
                continue
            case .put:
                puts += 1
                guard puts <= Self.maxPuts else { return .failed }
                flow = await put(id, part: number, api: api, stale: &stale)
            case .append(let from, let to):
                flow = await append(id, part: number, from: from, to: to, api: api)
            case .meta:
                flow = await append(id, part: number, from: part.next, to: part.next, api: api)
            }
            switch flow {
            case .again: continue
            case .stop(let outcome): return outcome
            }
        }
        // A part still busy after this many steps: the rest goes with the next visit.
        return .later(now())
    }

    private nonisolated enum Flow: Sendable {
        case again
        case stop(Outcome)
    }

    // MARK: Requests

    /// A create or repair `PUT`: its revision and numbering are decided and on disk before it goes; one decided
    /// before (a crash, a lost answer) goes again with its revision. Deciding it takes in every repair asked for so far
    /// (`needsRepair`); an erase after that asks again. A part the hub never stored a `PUT` of is created empty
    /// (docs/assistant/day-recap.md「The recording」) and its text follows by appends; a repair carries every position.
    private func put(_ id: UUID, part number: Int, api: APIClient, stale: inout Int) async -> Flow {
        let at = now()
        let floors = hubRevision
        await store.update(id) { record in
            guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
            if record.parts[i].pendingPut == nil {
                let revision = RecordingDayUpload.revision(record.parts[i], now: at, floor: floors[record.parts[i].hubID] ?? 0)
                record.parts[i].pendingPut = revision
                record.parts[i].maxSentRevision = max(record.parts[i].maxSentRevision, revision)
            }
            record.parts[i].needsRepair = false
            record.parts[i].count = RecordingDayUpload.renumber(&record, part: number)
        }
        guard await store.flush(id), let record = await store.record(id),
              let index = record.parts.firstIndex(where: { $0.part == number }), let revision = record.parts[index].pendingPut else {
            return .stop(.failed)
        }
        let part = record.parts[index]
        let carried = part.base == nil ? 0 : part.count
        let layout = RecordingDayUpload.layout(record, part: number)
        guard let segments = RecordingDayUpload.segments(record, placed: layout.placed, from: 0, to: carried) else {
            return .stop(.failed)
        }
        let meta = RecordingDayUpload.meta(record, partIndex: index, sent: carried, now: now())
        let language = library.entry(id)?.locale ?? RecordingDefaults.locale
        let body = RecordingDayPutBody(revision: revision, day: record.day, tz: record.timeZone, part: number,
                                       startedAt: record.startedAt, language: language, meta: meta, segments: segments)
        let data: Data
        do {
            data = try await send(id, api, "PUT", Self.path(part.hubID), body)
        } catch {
            return await react(RecordingDayReaction(error), id: id, part: number, appending: false, refused: segments.count)
        }
        // A 200 is the hub's word whatever its body; one it can't read counts as stored.
        let answer = try? JSON.decoder.decode(RecordingDayAnswer.self, from: data)
        noteHub(answer?.revision, part.hubID)
        switch answer?.status {
        case "expired":
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                record.parts[i].expired = true
                record.parts[i].pendingPut = nil
            }
            return .again
        case "stale":
            // Not success for a day recording: the hub holds a newer revision. The same replacement again, above it.
            stale += 1
            guard stale < Self.maxStale else { return .stop(.failed) }
            let floor = max(answer?.revision ?? 0, revision, hubRevision[part.hubID] ?? 0)
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }), record.parts[i].pendingPut == revision else { return }
                let bumped = max(floor, record.parts[i].maxSentRevision) + 1
                record.parts[i].pendingPut = bumped
                record.parts[i].maxSentRevision = bumped
            }
            return .again
        default:
            let sent = segments.count
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }), record.parts[i].pendingPut == revision else { return }
                record.parts[i].created = true
                record.parts[i].base = answer?.base ?? revision
                // The hub's count after a full replacement is what it carried; a different word goes again from there.
                record.parts[i].next = min(max(0, answer?.next ?? sent), sent)
                record.parts[i].hubRevision = max(record.parts[i].hubRevision ?? 0, answer?.revision ?? revision)
                record.parts[i].pendingPut = nil
                record.parts[i].complete = meta.complete
            }
            sentMeta[part.hubID] = meta
            lastStored[part.hubID] = now()
            return .again
        }
    }

    /// An append of positions `from..<to` (none: only the other fields), on the base of the last stored `PUT`.
    private func append(_ id: UUID, part number: Int, from: Int, to: Int, api: APIClient) async -> Flow {
        guard let record = await store.record(id), let index = record.parts.firstIndex(where: { $0.part == number }),
              let base = record.parts[index].base else { return .stop(.done) }
        let part = record.parts[index]
        let layout = RecordingDayUpload.layout(record, part: number)
        guard let segments = RecordingDayUpload.segments(record, placed: layout.placed, from: from, to: to) else {
            return .stop(.failed)
        }
        let revision = RecordingDayUpload.revision(part, now: now(), floor: hubRevision[part.hubID] ?? 0)
        // On disk before it goes: after a crash and a clock set back, the next revision is still above every one the
        // hub may have seen.
        await store.update(id) { record in
            guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
            record.parts[i].maxSentRevision = max(record.parts[i].maxSentRevision, revision)
        }
        guard await store.flush(id) else { return .stop(.failed) }
        let meta = RecordingDayUpload.meta(record, partIndex: index, sent: to, now: now())
        let body = RecordingDayAppendBody(base: base, revision: revision, from: from, segments: segments, tz: record.timeZone, meta: meta)
        let data: Data
        do {
            data = try await send(id, api, "POST", Self.path(part.hubID) + "/segments", body)
        } catch {
            return await react(RecordingDayReaction(error), id: id, part: number, appending: true, refused: segments.count)
        }
        let answer = try? JSON.decoder.decode(RecordingDayAnswer.self, from: data)
        noteHub(answer?.revision, part.hubID)
        if answer?.status == "expired" {
            // The day is past the hub's window, or was trimmed there: nothing of it is written any more.
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                record.parts[i].expired = true
            }
            return .again
        }
        let hubNext = answer?.next ?? to
        await store.update(id) { record in
            guard let i = record.parts.firstIndex(where: { $0.part == number }), record.parts[i].base == base else { return }
            record.parts[i].hubRevision = max(record.parts[i].hubRevision ?? 0, answer?.revision ?? revision)
            guard hubNext == to else {
                // The hub holds positions this phone never sent, or fewer than it just stored: the logs disagree, and
                // only a replacement puts them in step again (docs/assistant/day-recap.md「Appending」).
                record.parts[i].needsRepair = true
                return
            }
            record.parts[i].next = hubNext
            if meta.complete { record.parts[i].complete = true }
        }
        if hubNext == to { sentMeta[part.hubID] = meta }
        lastAppend[part.hubID] = RecordingDayUpload.LastAppend(at: now(), count: part.count)
        lastStored[part.hubID] = now()
        return .again
    }

    /// What a refusal or failure changes. The part's state is written before anything is sent again. `refused`:
    /// the segments the refused request carried.
    private func react(_ reaction: RecordingDayReaction, id: UUID, part number: Int, appending: Bool, refused: Int) async -> Flow {
        switch reaction {
        case .cancelled:
            return .stop(.cancelled)
        case .retry:
            return .stop(.failed)
        case .baseChanged(let base, let next):
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                if let base, base > 0, base == record.parts[i].pendingPut || base == record.parts[i].base {
                    // The phone's own latest replacement is the base (its answer may have been lost): on from the hub's count.
                    Self.adopt(&record.parts[i], base: base, next: next)
                } else {
                    // A base this phone can't vouch for (a restore, another device, a lost meta): replace the text.
                    record.parts[i].needsRepair = true
                }
            }
            return .again
        case .segmentsGap(let next):
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                if let next, next >= 0, next <= record.parts[i].count {
                    record.parts[i].next = next
                } else {
                    record.parts[i].needsRepair = true
                }
            }
            return .again
        case .notFound:
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                record.parts[i].created = false
            }
            return .again
        case .unsupported:
            unsupportedSeen.insert(id)
            await store.update(id) { $0.unsupportedUntilHello = true }
            return .stop(.blocked)
        case .rejected(let code):
            // That part stops, shown, until a hello or a launch lets it try once more, or an erase changes it.
            rejectedSeen.insert(id)
            await store.update(id) { record in
                guard let i = record.parts.firstIndex(where: { $0.part == number }) else { return }
                record.parts[i].problem = code
            }
            return .stop(.done)
        case .tooLarge:
            guard appending, let hubID = await store.record(id)?.parts.first(where: { $0.part == number })?.hubID else {
                return await halt(id, problem: "too_large")
            }
            let size = batch[hubID] ?? RecordingDayContract.maxAppendSegments
            // One sentence alone over the limit can never go: the day stops with a problem rather than retry for ever.
            guard size > 1 else { return await halt(id, problem: "too_large") }
            batch[hubID] = size / 2
            return .again
        case .dayFull(let next):
            let moved = appending ? await startNextPart(id, after: number, hubNext: next)
                                  : await repartition(id, from: number, refused: refused)
            return moved ? .again : await halt(id, problem: "day_full")
        case .conflict(let code):
            return await halt(id, problem: code)
        case .forbidden:
            paused = true
            return .stop(.blocked)
        case .deleted:
            // Deleted on the hub (docs/assistant/day-recap.md「Deleting」): the whole day goes, every part of it.
            removeEverywhere(id)
            return .stop(.done)
        }
    }

    private func halt(_ id: UUID, problem: String) async -> Flow {
        await store.update(id) { $0.problem = problem }
        return .stop(.blocked)
    }

    private nonisolated static func adopt(_ part: inout RecordingDayPart, base: Int64, next: Int?) {
        part.created = true
        part.base = base
        part.pendingPut = nil
        if let next, next > part.count {
            part.needsRepair = true
        } else if let next, next >= 0 {
            part.next = next
        }
    }

    /// The hub's stored revision, as an answer said it: every later request goes above it.
    private func noteHub(_ revision: Int64?, _ hubID: UUID) {
        if let revision { hubRevision[hubID] = max(hubRevision[hubID] ?? 0, revision) }
    }

    /// An append's `413 day_full`: that part is closed (its next request says `ended_at`, and its `pending` gaps
    /// go to the next part), and every position from the hub's count on, which it didn't store, moves to the next part
    /// with the text still to come. The new part and its id are on disk before its `PUT`, so a crash creates no second
    /// one. False past the contract's 99 parts.
    private func startNextPart(_ id: UUID, after number: Int, hubNext: Int?) async -> Bool {
        let hubID = UUID()
        let at = now()
        // The edit runs on the store's queue: what it decided comes back through this.
        let moved = OSAllocatedUnfairLock(initialState: false)
        await store.update(id) { record in
            guard let i = record.parts.firstIndex(where: { $0.part == number }),
                  let target = RecordingDayUpload.nextPart(&record, after: number, newID: hubID) else { return }
            let held = max(0, min(hubNext ?? record.parts[i].next, record.parts[i].count))
            RecordingDayUpload.move(&record, part: number, from: held, to: target)
            record.parts[i].count = held
            record.parts[i].next = min(record.parts[i].next, held)
            record.parts[i].endedAt = record.parts[i].endedAt ?? at
            moved.withLock { $0 = true }
        }
        _ = await store.flush(id)
        if let record = await store.record(id) { notePartsIfNeeded(id, record) }
        return moved.withLock { $0 }
    }

    /// A repair `PUT`'s `413 day_full`: the durable repartition plan (`RecordingDayUpload.repartition`), then a `PUT` of
    /// every part it changed.
    private func repartition(_ id: UUID, from number: Int, refused: Int) async -> Bool {
        let at = now()
        let planned = OSAllocatedUnfairLock(initialState: false)
        await store.update(id) { record in
            let done = RecordingDayUpload.repartition(&record, from: number, refused: refused, now: at, newID: { UUID() })
            planned.withLock { $0 = done }
        }
        _ = await store.flush(id)
        if let record = await store.record(id) { notePartsIfNeeded(id, record) }
        return planned.withLock { $0 }
    }

    /// Reconciliation's word on the day's parts: the recap's state and final time, and the hub's base and count. On the
    /// same base a count below the phone's is sent again from there; another base, a count above the phone's, or a part
    /// the hub no longer lists (`absent`) is replaced. A base equal to a replacement decided on is that replacement,
    /// stored. A row without a base is an older manager's: nothing goes to the day until a hello.
    private func apply(_ rows: [RecordingDayHubRow], listedAt: Date, to id: UUID) async {
        let byID = Dictionary(rows.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        for row in rows { noteHub(row.revision, row.id) }
        let absentMatters = knownCreated.contains(id)
        let older = rows.contains { $0.base == nil }
        if older { unsupportedSeen.insert(id) }
        let before = await store.record(id)?.parts ?? []
        // Parts with a request stored since about when the list was made: the list may not show it yet.
        let newer = Set(before.map(\.hubID).filter { hubID in
            lastStored[hubID].map { $0 >= listedAt.addingTimeInterval(-Self.listSlack) } ?? false
        })
        await store.update(id) { record in
            if older { record.unsupportedUntilHello = true }
            for i in record.parts.indices where !record.parts[i].expired && !record.parts[i].deleted {
                let fresh = newer.contains(record.parts[i].hubID)
                guard let row = byID[record.parts[i].hubID] else {
                    if absentMatters, !fresh, record.parts[i].created, record.parts[i].pendingPut == nil { record.parts[i].created = false }
                    continue
                }
                record.parts[i].recapState = row.recapState
                if let final = row.recapFinal { record.parts[i].recapFinal = final }
                if let finalAt = row.recapFinalAt { record.parts[i].recapFinalAt = finalAt }
                guard !fresh, let base = row.base, let next = row.next else { continue }
                if let pending = record.parts[i].pendingPut {
                    if base == pending { Self.adopt(&record.parts[i], base: base, next: next) }
                    continue
                }
                guard record.parts[i].created else { continue }
                if base != record.parts[i].base || next > record.parts[i].count {
                    record.parts[i].needsRepair = true
                } else if next < record.parts[i].next {
                    record.parts[i].next = next
                }
            }
        }
        // The first final recap's time is the audio's 3-day clock.
        let after = await store.record(id)?.parts ?? []
        if after.contains(where: { part in part.recapFinalAt != nil && before.first { $0.hubID == part.hubID }?.recapFinalAt != part.recapFinalAt }) {
            recapFinal()
        }
    }

    /// The day's hub ids past its first part, for `RecordingSync`'s deletes, once per change.
    private func notePartsIfNeeded(_ id: UUID, _ record: RecordingDayRecord) {
        let more = record.parts.filter { $0.hubID != id }.map(\.hubID)
        guard !more.isEmpty, noted[id] != more else { return }
        noted[id] = more
        noteParts(id, more)
    }

    private static func path(_ hubID: UUID) -> String { "/api/v1/recordings/\(hubID.uuidString.lowercased())" }

    /// One request for day `id`, shown as uploading while it is out. Sent off the main actor: a repair's body is the
    /// whole part's text, encoded where the UI isn't.
    private func send(_ id: UUID, _ api: APIClient, _ method: String, _ path: String,
                      _ body: some Encodable & Sendable) async throws -> Data {
        uploading = id
        defer { uploading = nil }
        let request = Task.detached(priority: .utility) { try await api.request(method, path, body: body, timeout: 60) }
        return try await withTaskCancellationHandler { try await request.value } onCancel: { request.cancel() }
    }
}
