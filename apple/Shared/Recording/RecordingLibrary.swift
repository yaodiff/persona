import Foundation
import Observation
import os

/// Recordings are not a cache to clear. Each gets its own directory and the index is written atomically; shared by phone and watch.
@Observable
final class RecordingLibrary {
    static let shared = RecordingLibrary()
    nonisolated static let directory = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("Recordings", isDirectory: true)
    private(set) var entries: [RecordingEntry] = []
    private(set) var error: String?
    @ObservationIgnored nonisolated let root: URL
    @ObservationIgnored private var deleted: Set<UUID> = [] {
        didSet { let snapshot = deleted; tombstones.withLock { $0 = snapshot } }
    }
    /// A copy of `deleted` for readers off the main actor (the phone stores all-day chunks on the WatchConnectivity
    /// queue). Updated before a removal deletes any file, so a chunk stored during it is refused or cleaned up.
    @ObservationIgnored private nonisolated let tombstones = OSAllocatedUnfairLock(initialState: Set<UUID>())
    /// Day recordings' seq counters (`allocateSeq`), here so the watch's writer can take a seq on its own queue and
    /// `closeDay` on main sees every seq handed out, also one whose chunk is still on its way to `addSegment`.
    @ObservationIgnored private nonisolated let daySeqs = OSAllocatedUnfairLock(initialState: [UUID: DaySeqCounter]())
    /// Called after a recording is saved (the phone syncs its text to the knowledge base from here).
    @ObservationIgnored var didChange: ((UUID) -> Void)?
    /// The launch's removal of day folders an exit left without their entry (`removeOrphanedDays`), off the main actor;
    /// nil when there were none.
    @ObservationIgnored private(set) var orphanCleanup: Task<Void, Never>?

    init(root: URL = RecordingLibrary.directory) {
        self.root = root
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            if let data = try? Data(contentsOf: root.appendingPathComponent("deleted.json")),
               let ids = try? JSONDecoder().decode(Set<UUID>.self, from: data) { deleted = ids }
            let tombstoned = deleted
            tombstones.withLock { $0 = tombstoned }
            let dirs = try FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil)
            var unnamed: [URL] = []
            entries = dirs.compactMap { dir in
                guard let id = UUID(uuidString: dir.lastPathComponent) else { return nil }
                // A deleted recording whose folder came back (a write already under way when it was deleted).
                guard !tombstoned.contains(id) else {
                    try? FileManager.default.removeItem(at: dir)
                    return nil
                }
                let data: Data
                do { data = try Data(contentsOf: dir.appendingPathComponent(Self.entryFileName)) } catch {
                    if (error as? CocoaError)?.code == .fileReadNoSuchFile { unnamed.append(dir) }
                    return nil
                }
                guard let entry = try? JSONDecoder().decode(RecordingEntry.self, from: data),
                      entry.id.uuidString == dir.lastPathComponent else { return nil }
                return entry
            }.sorted { $0.createdAt > $1.createdAt }
            let counters = entries.filter(\.isDay).map { ($0.id, $0.nextIndex) }
            daySeqs.withLock { $0 = Dictionary(counters, uniquingKeysWith: max).mapValues { DaySeqCounter(next: $0) } }
            if !unnamed.isEmpty {
                let folders = unnamed
                orphanCleanup = Task.detached(priority: .utility) { RecordingLibrary.removeOrphanedDays(folders) }
            }
        } catch { self.error = String(localized: "Can't read the recordings folder") }
    }

    /// Each recording's info in its folder.
    nonisolated static let entryFileName = "recording.json"

    nonisolated func folder(_ id: UUID) -> URL { root.appendingPathComponent(id.uuidString, isDirectory: true) }
    nonisolated func audio(_ id: UUID, _ index: Int) -> URL { folder(id).appendingPathComponent(RecordingSegment.fileName(index)) }
    func entry(_ id: UUID) -> RecordingEntry? { entries.first { $0.id == id } }

    /// A new recording. `id` / `createdAt` / `day` / `timeZone` are for all-day recordings: the watch names the next
    /// day's recording before midnight, and its writer may already have taken seqs and put audio in that folder, which
    /// stay. A day recording starts its seq and event counters here, saved before its microphone opens. An id that
    /// already has an entry returns that entry unchanged.
    @discardableResult func create(source: RecordingEntry.Source, locale: String = "zh-CN", title: String = "",
                                   meeting: RecordingMeeting? = nil, kind: RecordingEntry.Kind = .clip, id: UUID = UUID(),
                                   createdAt: Date = .now, day: String? = nil, timeZone: String? = nil) throws -> RecordingEntry {
        if let existing = entry(id) { return existing }
        // A clip stays nil on disk, as entries from before kinds were.
        var entry = RecordingEntry(id: id, createdAt: createdAt, source: source, locale: locale, kind: kind == .clip ? nil : kind)
        entry.title = title
        entry.meeting = meeting
        entry.day = day
        entry.timeZone = timeZone
        if kind == .day {
            entry.nextSeq = 0
            entry.nextEvent = 0
        }
        try save(entry)
        return entry
    }

    func save(_ entry: RecordingEntry) throws {
        guard !deleted.contains(entry.id) else { throw CocoaError(.fileNoSuchFile) }
        var entry = entry
        // Strictly later than the copy it replaces, even within one millisecond or after the clock went back.
        let floor = (self.entry(entry.id)?.updatedAt ?? entry.updatedAt ?? entry.createdAt).addingTimeInterval(0.001)
        entry.updatedAt = max(Date.now, floor)
        if entry.isDay, entry.nextSeq != nil { entry.nextSeq = syncSeqCounter(entry) }
        try FileManager.default.createDirectory(at: folder(entry.id), withIntermediateDirectories: true)
        try JSONEncoder().encode(entry).write(to: folder(entry.id).appendingPathComponent(Self.entryFileName), options: .atomic)
        entries.removeAll { $0.id == entry.id }
        entries.append(entry)
        entries.sort { $0.createdAt > $1.createdAt }
        error = nil
        didChange?(entry.id)
    }

    #if DEBUG
    /// Screenshots and UI tests put the same made-up recording back after a run deleted it.
    func restore(_ id: UUID) {
        guard deleted.remove(id) != nil else { return }
        try? JSONEncoder().encode(deleted).write(to: root.appendingPathComponent("deleted.json"), options: .atomic)
    }
    #endif

    /// Whether this id was deleted here (its tombstone stops late watch segments from recreating it).
    func isDeleted(_ id: UUID) -> Bool { deleted.contains(id) }

    /// `isDeleted` from any thread: the phone's day store asks before and after it writes a chunk.
    nonisolated func isDeletedSnapshot(_ id: UUID) -> Bool { tombstones.withLock { $0.contains(id) } }

    func change(_ id: UUID, _ edit: (inout RecordingEntry) -> Void) {
        guard var entry = entry(id) else { return }
        edit(&entry)
        do { try save(entry) } catch { self.error = String(localized: "The recording info wasn't saved. Keep the app installed and try again.") }
    }

    func addSegment(_ id: UUID, _ segment: RecordingSegment) {
        guard let current = entry(id) else { return }
        // A segment already registered keeps its audio: the writer re-adds its finished segments when capture stops.
        guard !current.segments.contains(where: { $0.index == segment.index }) else { return }
        // Segments that finish after being pruned or erased are dropped: for a day recording, a seq the phone
        // acknowledged or that was erased or reported lost (all tombstoned), which the writer's replay at stop would
        // otherwise bring back.
        guard !current.isPruned(segment.index), !current.isErased(segment) else {
            try? FileManager.default.removeItem(at: audio(id, segment.index))
            return
        }
        change(id) { entry in
            guard !entry.segments.contains(where: { $0.index == segment.index }) else { return }
            entry.segments.append(segment)
            entry.segments.sort { $0.index < $1.index }
        }
    }

    /// The end message and the audio can arrive in any order; a repeated message never overwrites a transcribed segment.
    func receive(_ transfer: RecordingTransfer) throws {
        guard transfer.valid else { throw CocoaError(.fileReadCorruptFile) }
        // A watch segment arriving late or retried after a delete must not recreate the deleted recording, but is still acknowledged so the watch cleans up.
        if deleted.contains(transfer.id) {
            try? FileManager.default.removeItem(at: folder(transfer.id))
            return
        }
        var entry = entry(transfer.id) ?? RecordingEntry(id: transfer.id, createdAt: transfer.createdAt, source: .watch,
                                                          locale: transfer.locale, kind: transfer.kind)
        entry.marks = Array(Set(entry.marks + transfer.marks)).sorted()
        // A meeting started on the watch: the phone names it after the calendar (`MeetingRecorder.tagWatchRecording`).
        if transfer.meeting == true, entry.meeting == nil { entry.meeting = RecordingMeeting() }
        if let erased = transfer.erased, !erased.isEmpty {
            var merged = entry.erased ?? []
            for range in erased where !merged.contains(range) { merged.append(range) }
            entry.erased = merged.isEmpty ? nil : merged
            let snapshot = entry
            for segment in entry.segments where snapshot.isErased(segment) {
                try? FileManager.default.removeItem(at: audio(entry.id, segment.index))
            }
            entry.segments.removeAll { snapshot.isErased($0) }
        }
        if var segment = transfer.segment, !entry.segments.contains(where: { $0.index == segment.index }) {
            segment.sentences = nil; segment.delivered = false
            // The watch holds bookmarked segments it couldn't send for longer; one arriving after newer segments
            // were pruned still gets checked instead of falling into the tombstone.
            let tombstoned = entry.isPruned(segment.index) && !RecordingRolling.nearMark(segment, marks: entry.marks)
            if tombstoned || entry.isErased(segment) {
                try? FileManager.default.removeItem(at: audio(entry.id, segment.index))
            } else {
                entry.segments.append(segment)
                entry.segments.sort { $0.index < $1.index }
            }
        }
        if let total = transfer.total {
            entry.active = false; entry.expectedSegments = total
            if entry.endedAt == nil { entry.endedAt = .now }
        }
        try save(entry)
    }

    func remove(_ id: UUID) throws {
        var next = deleted; next.insert(id)
        try JSONEncoder().encode(next).write(to: root.appendingPathComponent("deleted.json"), options: .atomic)
        deleted = next
        if FileManager.default.fileExists(atPath: folder(id).path) { try FileManager.default.removeItem(at: folder(id)) }
        entries.removeAll { $0.id == id }
        daySeqs.withLock { $0[id] = nil }
    }

    /// Rolling recordings drop expired segments and remember which indexes went, so late copies of them never return while a segment that never arrived is not tombstoned.
    func prune(_ id: UUID, indexes: [Int]) {
        guard !indexes.isEmpty, entry(id) != nil else { return }
        let gone = Set(indexes)
        change(id) { Self.tombstone(gone, in: &$0) }
        for index in gone { try? FileManager.default.removeItem(at: audio(id, index)) }
    }

    /// Drops these indexes' records and remembers them in `prunedBelow` / `prunedAbove`, compacted, so they are never
    /// used or taken back again.
    private static func tombstone(_ gone: Set<Int>, in entry: inout RecordingEntry) {
        guard !gone.isEmpty else { return }
        entry.segments.removeAll { gone.contains($0.index) }
        var floor = entry.prunedBelow ?? 0
        var above = Set(entry.prunedAbove ?? []).union(gone.filter { $0 >= floor })
        while above.remove(floor) != nil { floor += 1 }
        entry.prunedBelow = floor
        entry.prunedAbove = above.isEmpty ? nil : above.sorted()
    }

    /// Segments kept for bookmarks, heart-rate spikes, or mood. Already-kept ones stay as they are.
    func keep(_ id: UUID, _ keeps: [Int: RecordingKeep]) {
        guard !keeps.isEmpty else { return }
        change(id) { entry in
            for position in entry.segments.indices where entry.segments[position].kept == nil {
                if let keep = keeps[entry.segments[position].index] { entry.segments[position].kept = keep }
            }
        }
    }

    /// Erase a wall-clock range; later syncs that land in it are refused too.
    func erase(_ id: UUID, from: Date, to: Date) {
        let range = RecordingErase(from: from, to: to)
        guard range.valid, entry(id) != nil else { return }
        var removed: [Int] = []
        change(id) { entry in
            entry.erased = (entry.erased ?? []) + [range]
            removed = entry.segments.filter { $0.overlaps(from, to) }.map(\.index)
            entry.segments.removeAll { $0.overlaps(from, to) }
        }
        for index in removed { try? FileManager.default.removeItem(at: audio(id, index)) }
    }

    /// Save the chosen segments as a normal clip: indexes renumbered, transcript and bookmarks shifted.
    func saveClip(from id: UUID, indexes: [Int]) throws -> RecordingEntry {
        guard let original = entry(id) else { throw CocoaError(.fileNoSuchFile) }
        let chosen = Set(indexes)
        let picked = original.segments.filter { chosen.contains($0.index) && FileManager.default.fileExists(atPath: audio(id, $0.index).path) }
            .sorted { $0.index < $1.index }
        var clip = RecordingEntry(id: UUID(), createdAt: picked.first?.startedAt ?? .now, source: original.source, locale: original.locale)
        try FileManager.default.createDirectory(at: folder(clip.id), withIntermediateDirectories: true)
        var placed: [(old: RecordingSegment, start: Double)] = []
        var start = 0.0
        for segment in picked {
            do { try FileManager.default.copyItem(at: audio(id, segment.index), to: audio(clip.id, placed.count)) } catch { continue }
            let shift = start - segment.start
            var next = segment
            next.index = placed.count
            next.start = start
            next.delivered = true
            next.kept = nil
            next.sentences = segment.sentences?.map { RecordingSentence(start: $0.start + shift, end: $0.end + shift, text: $0.text) }
            placed.append((segment, start))
            clip.segments.append(next)
            start += segment.duration
        }
        guard !clip.segments.isEmpty else {
            try? FileManager.default.removeItem(at: folder(clip.id))
            throw CocoaError(.fileNoSuchFile)
        }
        clip.marks = original.marks.compactMap { mark in
            placed.first { mark >= $0.old.start && mark < $0.old.start + $0.old.duration }.map { $0.start + mark - $0.old.start }
        }.sorted()
        clip.active = false
        clip.expectedSegments = clip.segments.count
        clip.endDelivered = true
        try save(clip)
        return clip
    }

    /// Hourhand's cleanup after an abnormal exit: keep only finalized segments, delete half-written ones with no tail.
    /// A day recording keeps its seqs and stays open (the day goes on with the same id when recording starts again);
    /// the ids of those left open are returned. What the exit cut short becomes control events (`recoverDay`).
    @discardableResult func recover(source: RecordingEntry.Source) -> [UUID] {
        var open: [UUID] = []
        for entry in entries where entry.source == source && (entry.active || entry.isDay) {
            let leftovers = leftoverFiles(entry)
            if entry.isDay {
                recoverDay(entry, leftovers: leftovers)
                if entry.active { open.append(entry.id) }
                continue
            }
            for file in leftovers { try? FileManager.default.removeItem(at: file.url) }
            var ended = entry
            ended.active = false
            ended.expectedSegments = ended.nextIndex
            ended.endedAt = .now
            do { try save(ended) } catch { self.error = String(localized: "The end of the last recording wasn't saved") }
        }
        return open
    }

    /// What `recover` cleans up in a recording's folder: audio files without a record (half-written when the app ended,
    /// or left by a removal it cut short) and the seq counter's marks (`markSeqCounter`).
    private func leftoverFiles(_ entry: RecordingEntry) -> [LeftoverFile] {
        let known = Set(entry.segments.map { RecordingSegment.fileName($0.index) })
        let keys: Set<URLResourceKey> = [.creationDateKey, .contentModificationDateKey]
        let files = (try? FileManager.default.contentsOfDirectory(at: folder(entry.id), includingPropertiesForKeys: Array(keys))) ?? []
        return files.compactMap { url in
            let name = url.lastPathComponent
            let nextSeq = Self.seqMark(fileName: name)
            guard nextSeq != nil || (url.pathExtension == "m4a" && !known.contains(name)) else { return nil }
            let values = try? url.resourceValues(forKeys: keys)
            return LeftoverFile(url: url, index: nextSeq == nil ? RecordingSegment.index(fileName: name) : nil, nextSeq: nextSeq,
                                created: values?.creationDate, modified: values?.contentModificationDate)
        }
    }
}

/// A file `recover` found in a recording's folder besides its records, with what the file system says of when it was
/// written.
private nonisolated struct LeftoverFile {
    var url: URL
    /// An audio file's seq.
    var index: Int?
    /// A seq counter's mark: the counter it holds.
    var nextSeq: Int?
    var created: Date?
    var modified: Date?
}

/// A day recording's seq counter (`RecordingLibrary.allocateSeq`).
private nonisolated struct DaySeqCounter: Sendable {
    /// The next seq to hand out.
    var next: Int
    /// The counter of the mark this process last wrote in the recording's folder, which the next seq replaces.
    var marked: Int? = nil
}

// MARK: - All-day recordings on the watch (docs/assistant/day-recap.md「The day recording on the watch」)

extension RecordingLibrary {
    /// The seq for a day recording's next chunk, from any queue: the writer takes it on its own just before it opens
    /// the chunk's file. The counter is on disk before the seq is returned (`markSeqCounter`), so a crash at any point
    /// after, even before the file opens or any save of the entry, leaves `recover` the seqs to report lost, and none is
    /// handed out twice (docs/assistant/day-recap.md「The day recording on the watch」). After a relaunch `recover` must
    /// run before the first seq is taken, as capture does when it starts.
    nonisolated func allocateSeq(_ id: UUID) -> Int {
        let (seq, replaced) = daySeqs.withLock { counters in
            var counter = counters[id] ?? DaySeqCounter(next: 0)
            let taken = (counter.next, counter.marked)
            counter.next += 1
            counter.marked = counter.next
            counters[id] = counter
            return taken
        }
        markSeqCounter(id, seq + 1, replacing: replaced)
        return seq
    }

    /// The seqs handed out for a day recording: every seq below it belongs to a chunk, recorded or being recorded.
    nonisolated func allocatedSeqs(_ id: UUID) -> Int { daySeqs.withLock { $0[id]?.next ?? 0 } }

    /// Puts a day recording's counter on disk as an empty file named after it (`next-seq-12`) in its folder, so
    /// `recover` reads it off the listing it makes anyway and a launch reads no file for it. The new mark is made before
    /// the one it replaces goes, so a crash in between leaves the larger counter, and allocations racing each other at
    /// worst leave an extra, smaller mark: the largest mark is always at least every seq handed out plus one. When even
    /// an empty file can't be written (a full disk) the seq is still used: the chunk's own file, if the writer can make
    /// it, is the evidence then. A deleted recording gets none, since its folder must not come back.
    private nonisolated func markSeqCounter(_ id: UUID, _ next: Int, replacing old: Int?) {
        guard !isDeletedSnapshot(id) else { return }
        let mark = folder(id).appendingPathComponent(Self.seqMarkPrefix + String(next))
        // The folder is missing only before a recording's first seq (the next day's, named before its entry exists).
        if !FileManager.default.createFile(atPath: mark.path, contents: nil) {
            try? FileManager.default.createDirectory(at: folder(id), withIntermediateDirectories: true)
            FileManager.default.createFile(atPath: mark.path, contents: nil)
        }
        guard let old, old != next else { return }
        try? FileManager.default.removeItem(at: folder(id).appendingPathComponent(Self.seqMarkPrefix + String(old)))
    }

    /// The name of a seq counter's mark (`markSeqCounter`), followed by the counter.
    private nonisolated static let seqMarkPrefix = "next-seq-"

    /// Removes the day folders an exit left without their recording.json, given the folders a launch found without one.
    /// Only the watch's day writer makes such a folder: at midnight it moves to the next day's folder, takes its first
    /// seq there (the counter's mark) and fills it before the main thread makes its entry, so an app that ends in
    /// between leaves audio that no entry names, that `recover` never sees and that nobody was told of. Its id is never
    /// named again, so it would stay past every retention limit; it goes instead. A folder without a seq counter's mark
    /// stays: the phone keeps a day's chunks in their folder before the day's index row is written, and a watch's
    /// rolling segment arrives before its entry. Run off the main actor (it lists folders), at launch, before any writer
    /// could name a folder. The day the writer left keeps its own `recover`: its end and what it lost.
    nonisolated static func removeOrphanedDays(_ folders: [URL]) {
        let files = FileManager.default
        for folder in folders {
            guard !files.fileExists(atPath: folder.appendingPathComponent(entryFileName).path),
                  let names = try? files.contentsOfDirectory(atPath: folder.path),
                  names.contains(where: { seqMark(fileName: $0) != nil }) else { continue }
            try? files.removeItem(at: folder)
        }
    }

    /// The counter a file name holds, nil for a file that isn't a seq counter's mark.
    private nonisolated static func seqMark(fileName: String) -> Int? {
        guard fileName.hasPrefix(seqMarkPrefix), let next = Int(fileName.dropFirst(seqMarkPrefix.count)), next > 0 else { return nil }
        return next
    }

    /// A day entry's counter as it is saved: the larger of what it says and what was handed out, which stays the
    /// counter from then on.
    private func syncSeqCounter(_ entry: RecordingEntry) -> Int {
        let floor = entry.nextIndex
        return daySeqs.withLock { counters in
            var counter = counters[entry.id] ?? DaySeqCounter(next: 0)
            counter.next = max(counter.next, floor)
            counters[entry.id] = counter
            return counter.next
        }
    }

    /// Adds a control event: `make` gets its `n`, and it is saved with the entry in the same write, kept until the phone
    /// acknowledges it (docs/assistant/day-recap.md「Control events」). `started` and `paused` also keep `gapReason`.
    /// nil when there is no such day recording or the save failed: then there is no event.
    @discardableResult func appendDayEvent(_ id: UUID, _ make: (Int) -> RecordingDayEvent) -> RecordingDayEvent? {
        var made: RecordingDayEvent?
        guard commitDay(id, { made = Self.append(make, to: &$0) }) else { return nil }
        return made
    }

    /// The phone acknowledged these events (by `n`): the watch forgets them. Writes nothing for ones already gone.
    func ackDayEvents(_ id: UUID, ns: some Sequence<Int>) {
        let acked = Set(ns)
        guard !acked.isEmpty, entry(id)?.dayEvents?.contains(where: { acked.contains($0.n) }) == true else { return }
        commitDay(id) { entry in
            entry.dayEvents?.removeAll { acked.contains($0.n) }
            if entry.dayEvents?.isEmpty == true { entry.dayEvents = nil }
        }
    }

    /// The phone acknowledged these chunks: their audio and records go at once, and their seqs are tombstoned so they
    /// are never used or taken back again (docs/assistant/day-recap.md「Acknowledgements and retries」). Returns the
    /// seqs that went; nothing is written for seqs already gone.
    @discardableResult func ackDayChunks(_ id: UUID, seqs: some Sequence<Int>) -> [Int] {
        guard let current = entry(id), current.isDay else { return [] }
        let held = Set(current.segments.map(\.index)).intersection(seqs)
        guard !held.isEmpty, removeDayChunks(id, held) else { return [] }
        return held.sorted()
    }

    /// Erases `from…to` from the chunks the watch still holds (docs/assistant/day-recap.md「Erasing」): every registered
    /// chunk with audio in the range loses its audio and record, and its seq is tombstoned. Returns their seqs, the
    /// `seqs` of the `erase` event or of the answer to the phone. The caller cuts the chunk being written first, so all
    /// of the range is registered. `request`: the phone's erase id, saved with the seqs in the same write as the
    /// removal; a repeated request returns the saved seqs and erases nothing again.
    @discardableResult func eraseDayChunks(_ id: UUID, from: Date, to: Date, request: UUID? = nil, now: Date = .now) -> [Int] {
        guard RecordingErase(from: from, to: to).valid, let current = entry(id), current.isDay else { return [] }
        if let request, let done = current.dayErases?.first(where: { $0.id == request }) {
            return RecordingDayRanges.expand(done.seqs)
        }
        let hit = Set(current.segments.filter { $0.overlapsDay(from, to) }.map(\.index))
        guard !hit.isEmpty || request != nil else { return [] }
        let erased = removeDayChunks(id, hit) { entry in
            guard let request else { return }
            let forgotten = now.addingTimeInterval(-RecordingDayContract.eraseMemoryDays * 86_400)
            entry.dayErases = (entry.dayErases ?? []).filter { $0.at > forgotten }
                + [RecordingDayEraseDone(id: request, seqs: RecordingDayRanges.ranges(hit), at: now)]
        }
        return erased ? hit.sorted() : []
    }

    /// Gives up on these chunks (an unreadable file, one past `recordingDayKeepDays`): their audio and records go,
    /// their seqs are tombstoned, and one `lost` event per run of consecutive seqs tells the phone, with the run's times
    /// when the records have them, in the same write. Seqs already gone, or never handed out, are skipped.
    @discardableResult func loseDayChunks(_ id: UUID, seqs: some Sequence<Int>) -> [RecordingDayEvent] {
        guard let current = entry(id), current.isDay else { return [] }
        let counter = max(current.nextIndex, allocatedSeqs(id))
        let lost = Set(seqs).filter { (0..<counter).contains($0) && !current.isPruned($0) }
        guard !lost.isEmpty else { return [] }
        let records = Dictionary(current.segments.map { ($0.index, $0) }, uniquingKeysWith: { first, _ in first })
        var events: [RecordingDayEvent] = []
        let removed = removeDayChunks(id, lost) { entry in
            events = Self.reportLost(lost, in: &entry) { (records[$0]?.startedAt, records[$0]?.endedAt) }
        }
        return removed ? events : []
    }

    /// Ends a day recording (its midnight, or later when it was left open): not active any more, `closedAt`, and a
    /// `closed` event whose `seqCount` is every seq handed out, a chunk still on its way to `addSegment` included.
    /// Chunks keep arriving and going to the phone after it. A day already closed gets no second event. Returns the
    /// event.
    @discardableResult func closeDay(_ id: UUID, at: Date) -> RecordingDayEvent? {
        guard let current = entry(id), current.isDay else { return nil }
        guard current.closedAt == nil else {
            if current.active { commitDay(id) { $0.active = false } }
            return nil
        }
        let count = max(current.nextIndex, allocatedSeqs(id))
        var closed: RecordingDayEvent?
        let saved = commitDay(id) { entry in
            entry.nextSeq = max(entry.nextSeq ?? 0, count)
            entry.active = false
            entry.closedAt = at
            closed = Self.append({ .closed($0, at: at, seqCount: count) }, to: &entry)
        }
        return saved ? closed : nil
    }

    /// After the app ended without closing the day recording's chunks: every seq handed out (the counter's mark says how
    /// many, also those no save counted) that has no record and no tombstone (its chunk was being written, or finished
    /// but not registered, or its file never opened) becomes a `lost` event, and the counter stays. A seq with a file
    /// is lost from the file's creation to its last write; one whose file never opened held no audio, so it is lost for
    /// no time, at the last moment the recording is known to have run (without times the phone would count the whole
    /// stop as lost). A recording whose microphone was open (no `paused` since its last `started`) also gets the
    /// `paused` event (`stopped`) the exit never wrote, at that same moment. Only `recover` adds that event, so capture
    /// must not add another for the same exit.
    private func recoverDay(_ entry: RecordingEntry, leftovers: [LeftoverFile]) {
        let registered = Set(entry.segments.map(\.index))
        let files = Dictionary(leftovers.compactMap { file in file.index.map { ($0, file) } }, uniquingKeysWith: { first, _ in first })
        let marked = leftovers.compactMap(\.nextSeq).max() ?? 0
        let counter = max(entry.nextIndex, allocatedSeqs(entry.id), (files.keys.max() ?? -1) + 1, marked)
        let lost = Set((0..<counter).filter { !registered.contains($0) && !entry.isPruned($0) })
        let crashed = entry.active && entry.closedAt == nil && entry.gapReason == nil
        // Every time here is no later than the exit: the last save, the chunks' ends, the last writes to the files and
        // the last seq taken (its mark's).
        let lastHeard = ([entry.createdAt, entry.updatedAt].compactMap { $0 } + entry.segments.compactMap(\.endedAt)
            + leftovers.compactMap(\.modified)).max() ?? entry.createdAt
        let saved = commitDay(entry.id) { day in
            day.nextSeq = max(day.nextSeq ?? 0, counter)
            Self.tombstone(lost, in: &day)
            Self.reportLost(lost, in: &day) { seq in files[seq].map { ($0.created, $0.modified) } ?? (lastHeard, lastHeard) }
            if crashed { Self.append({ .paused($0, at: lastHeard, reason: RecordingDayContract.Reason.stopped) }, to: &day) }
        }
        // Only once the events and the counter are saved: until then the files are what says these seqs were used.
        guard saved else { return }
        for file in leftovers { try? FileManager.default.removeItem(at: file.url) }
    }

    /// One `lost` event per run of consecutive seqs, from the earliest start known in the run to its latest end.
    @discardableResult
    private static func reportLost(_ seqs: Set<Int>, in entry: inout RecordingEntry,
                                   times: (Int) -> (from: Date?, to: Date?)) -> [RecordingDayEvent] {
        RecordingDayRanges.runs(seqs).map { run in
            let known = run.map(times)
            let (from, to) = (known.compactMap(\.from).min(), known.compactMap(\.to).max())
            return append({ .lost($0, seqs: run, from: from, to: to) }, to: &entry)
        }
    }

    /// Numbers and adds an event to an entry being edited; `started` clears `gapReason`, `paused` sets it.
    @discardableResult
    private static func append(_ make: (Int) -> RecordingDayEvent, to entry: inout RecordingEntry) -> RecordingDayEvent {
        let n = entry.nextEvent ?? (entry.dayEvents?.map(\.n).max() ?? -1) + 1
        var event = make(n)
        event.n = n
        entry.nextEvent = n + 1
        entry.dayEvents = (entry.dayEvents ?? []) + [event]
        switch event.type {
        case .started: entry.gapReason = nil
        case .paused: entry.gapReason = event.reason ?? RecordingDayContract.Reason.paused
        default: break
        }
        return event
    }

    /// Drops these chunks' records with a tombstone in one write with `edit`; their audio goes after it, so a crash in
    /// between leaves only files `recover` deletes, never a record without its audio.
    @discardableResult
    private func removeDayChunks(_ id: UUID, _ seqs: Set<Int>, _ edit: (inout RecordingEntry) -> Void = { _ in }) -> Bool {
        guard commitDay(id, { entry in
            Self.tombstone(seqs, in: &entry)
            edit(&entry)
        }) else { return false }
        for seq in seqs { try? FileManager.default.removeItem(at: audio(id, seq)) }
        return true
    }

    /// One write of a day entry: `edit` changes the current copy, saved only when it changed anything. False when
    /// there is no such day recording or the save failed.
    @discardableResult
    private func commitDay(_ id: UUID, _ edit: (inout RecordingEntry) -> Void) -> Bool {
        guard let current = entry(id), current.isDay else { return false }
        var next = current
        edit(&next)
        guard next != current else { return true }
        do {
            try save(next)
            return true
        } catch {
            self.error = String(localized: "The recording info wasn't saved. Keep the app installed and try again.")
            return false
        }
    }

    /// This device's newest open day recording for a local date.
    func openDay(source: RecordingEntry.Source, day: String) -> RecordingEntry? {
        entries.first { $0.isDay && $0.active && $0.source == source && $0.day == day }
    }

    /// This device's open day recordings, newest first: today's, and a past day not closed yet.
    func openDays(source: RecordingEntry.Source) -> [RecordingEntry] {
        entries.filter { $0.isDay && $0.active && $0.source == source }
    }

    /// Phone: the index row of a day recording the watch sends. It has no segments: the chunks and their text live in
    /// the day store, in the same folder, which `remove` deletes with it (and its tombstone, which never expires,
    /// keeps it deleted). Written only when something changed (a chunk arrives every few seconds). `createdAt` is the
    /// recording's `recording_started_at` and only moves earlier. `closed` ends it and never reopens it. nil for a
    /// deleted id, which stays deleted.
    @discardableResult func adoptDay(id: UUID, day: String, timeZone: String, createdAt: Date, closed: Bool = false) -> RecordingEntry? {
        guard !deleted.contains(id) else { return nil }
        let current = entry(id)
        // A new row starts in the phone's recording language, as a recording started on it would.
        var next = current ?? RecordingEntry(id: id, createdAt: createdAt, source: .watch,
                                             locale: UserDefaults.standard.string(forKey: RecordingDefaults.localeKey) ?? RecordingDefaults.locale,
                                             kind: .day)
        next.kind = .day
        next.day = day
        next.timeZone = timeZone
        next.createdAt = min(next.createdAt, createdAt)
        if closed { next.active = false }
        guard next != current else { return current }
        do { try save(next) } catch {
            self.error = String(localized: "The recording info wasn't saved. Keep the app installed and try again.")
            return nil
        }
        return entry(id)
    }
}
