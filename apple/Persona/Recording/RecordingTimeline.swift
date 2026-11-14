#if os(iOS)
import Foundation

/// The live sheet's one stream (docs/design/proposals/meeting-recording.md section 9): transcript sentences, bookmarks and
/// Persona's exchanges, in the order they happened on the recording's clock. Pure: plain inputs in, rows out, so the
/// ordering and the ids can be tested without a view.
///
/// Text is honest about its latency: a sentence exists only once its segment is closed and transcribed (about every 30 s
/// in front), while a bookmark is stored at once and an exchange is stamped when it is asked. So the stream is not
/// append-only. What is newer than the last transcribed audio sits below a gap marker, in time order, and never between
/// sentences; when the segment lands its sentences arrive as one group above the marker and what the marker held
/// re-sorts by time.
nonisolated enum RecordingTimeline {
    nonisolated struct Sentence: Equatable, Sendable {
        var segment: Int
        var start: Double
        var text: String
        /// A line of the hub's live log (#3862 phase 5, hub mode): its position there, never renumbered, which is its
        /// row's id; nil for the phone's own sentences.
        var line: Int? = nil
    }

    /// A question asked of Persona, at its time on the recording's clock. The row looks the exchange up by `id`.
    nonisolated struct Ask: Equatable, Sendable {
        var id: UUID
        var at: Double
    }

    nonisolated struct Item: Identifiable, Equatable, Sendable {
        /// Stable across appends: a sentence by its segment and start (a segment transcribed again may change it, which is
        /// accepted), a bookmark by its place in the recording's marks (they only grow), an exchange by its own id.
        nonisolated enum ID: Hashable, Sendable {
            /// `occurrence`: the nth sentence with the same segment and start (0 for the first), so two never share an id.
            case sentence(segment: Int, millis: Int, occurrence: Int = 0)
            /// A line of the hub's live log, by its position (#3862 phase 5).
            case line(Int)
            case bookmark(Int)
            case exchange(UUID)
            case gap
        }
        nonisolated enum Kind: Equatable, Sendable {
            case sentence(String)
            /// The bookmark's number, from 1.
            case bookmark(Int)
            case exchange(UUID)
            /// Audio recorded since the last transcribed segment: the row says how much, from the clock.
            case gap
        }
        var id: ID
        /// On the recording's clock, seconds.
        var at: Double
        var kind: Kind
    }

    /// The stream. `transcribedUntil`: the end of the last segment that has its text (0: none yet). `now`: the recording's
    /// clock. Without any sentence yet there is no marker (the page says it is listening instead) and everything is in time
    /// order.
    static func make(sentences: [Sentence], marks: [Double], asks: [Ask], transcribedUntil: Double, now: Double) -> [Item] {
        var items: [Item] = []
        items.reserveCapacity(sentences.count + marks.count + asks.count + 1)
        var seen: [SentenceKey: Int] = [:]
        for s in sentences {
            if let n = s.line {
                items.append(Item(id: .line(n), at: s.start, kind: .sentence(s.text)))
                continue
            }
            let key = SentenceKey(segment: s.segment, millis: millis(s.start))
            let occurrence = seen[key, default: 0]
            seen[key] = occurrence + 1
            items.append(Item(id: .sentence(segment: key.segment, millis: key.millis, occurrence: occurrence), at: s.start, kind: .sentence(s.text)))
        }
        for (index, mark) in marks.enumerated() {
            items.append(Item(id: .bookmark(index), at: mark, kind: .bookmark(index + 1)))
        }
        for ask in asks {
            items.append(Item(id: .exchange(ask.id), at: ask.at, kind: .exchange(ask.id)))
        }
        // Time, then sentence before bookmark before exchange, then the order given (a stable sort).
        let indexed = items.enumerated().sorted { a, b in
            if a.element.at != b.element.at { return a.element.at < b.element.at }
            if rank(a.element) != rank(b.element) { return rank(a.element) < rank(b.element) }
            return a.offset < b.offset
        }
        var sorted = indexed.map(\.element)
        guard !sentences.isEmpty, gapSeconds(now: now, transcribedUntil: transcribedUntil) != nil else { return sorted }
        let marker = Item(id: .gap, at: transcribedUntil, kind: .gap)
        let place = sorted.firstIndex { $0.at > transcribedUntil } ?? sorted.count
        sorted.insert(marker, at: place)
        return sorted
    }

    private struct SentenceKey: Hashable { var segment: Int; var millis: Int }

    /// The sentences of `segments` in order, each with the segment it came from; Persona's echo
    /// (`RecordingAttendance.echo`) is left out, as everywhere the transcript is shown.
    static func sentences(of entry: RecordingEntry) -> [Sentence] {
        entry.segments.sorted { $0.index < $1.index }.flatMap { segment in
            (segment.sentences ?? []).compactMap { line -> Sentence? in
                if let attendance = entry.attendance, !attendance.spoken.isEmpty, attendance.echo(line) { return nil }
                return Sentence(segment: segment.index, start: line.start, text: line.text)
            }
        }
    }

    /// The hub's lines in place of the phone's sentences (#3862 phase 5: the meeting's transcript while the page shows the
    /// hub's, `MeetingTranscription.shown`), in the live log's order; a placeholder position (no words) is skipped, as
    /// every client skips it.
    static func sentences(hub lines: [MeetingHubLine]) -> [Sentence] {
        lines.sorted { $0.n < $1.n }.compactMap { line in
            line.text.isEmpty ? nil : Sentence(segment: 0, start: line.start, text: line.text, line: line.n)
        }
    }

    /// The end of the audio whose text is in: the last segment with sentences (an empty list is text too: silence).
    static func transcribedUntil(of entry: RecordingEntry) -> Double {
        entry.segments.filter { $0.sentences != nil }.map { $0.start + $0.duration }.max() ?? 0
    }

    /// How many seconds of audio wait for their text, rounded down to the step so the number moves a few times a minute;
    /// nil when it is less than the minimum (the segment is about to land).
    static func gapSeconds(now: Double, transcribedUntil: Double) -> Int? {
        let wait = Int(max(0, now - transcribedUntil))
        guard wait >= Theme.Recording.gapMinimum else { return nil }
        return wait / Theme.Recording.gapStep * Theme.Recording.gapStep
    }

    /// Where the transcript opens at a highlight's minute (#3862 phase 2b): the row to scroll to and the rows to light.
    nonisolated struct Focus: Equatable, Sendable {
        var scroll: Item.ID
        var lit: Set<Item.ID>
    }

    /// The transcript at a highlight's minute, `at` to `end` (pure, for the tests): every sentence of `items` that starts
    /// in it, lit, the first of them scrolled to; with none there (the hub's lines aren't the phone's, phase 5), the one
    /// `RecordingMinutes.sentenceIndex` picks for `at`. Nil without a sentence. Compared in milliseconds, as the rows' ids
    /// are, so a time that went through the hub and back still finds its line.
    static func focus(at: Double, end: Double, in items: [Item]) -> Focus? {
        guard at.isFinite else { return nil }
        let sentences = items.filter { if case .sentence = $0.kind { true } else { false } }
        let from = millis(at), to = millis(end.isFinite ? max(at, end) : at)
        let inside = sentences.filter { (from...to).contains(millis($0.at)) }
        if let first = inside.first { return Focus(scroll: first.id, lit: Set(inside.map(\.id))) }
        let starts = sentences.map { RecordingSentence(start: $0.at, end: $0.at, text: "") }
        guard let index = RecordingMinutes.sentenceIndex(at: at, in: starts) else { return nil }
        return Focus(scroll: sentences[index].id, lit: [sentences[index].id])
    }

    static func millis(_ seconds: Double) -> Int {
        guard seconds.isFinite else { return 0 }
        return Int((seconds * 1000).rounded())
    }

    private static func rank(_ item: Item) -> Int {
        switch item.kind {
        case .sentence: 0
        case .bookmark: 1
        case .exchange: 2
        case .gap: 3
        }
    }
}

// MARK: Following the newest item

/// Whether the timeline follows its newest item, and how many items arrived since the reader scrolled away. Pure, so the
/// rules can be tested: the first drag upward stops following (a programmatic scroll never does), items that arrive while
/// away are counted, and reaching the end by hand or Jump to Latest follows again.
nonisolated struct RecordingFollow: Equatable, Sendable {
    private(set) var following = true
    /// The items on screen's list when the reader detached: what is not in it is new.
    private var seen: Set<RecordingTimeline.Item.ID> = []

    /// The reader's finger took the list: stop following, remembering what there is now.
    mutating func detach(items: [RecordingTimeline.Item]) {
        guard following else { return }
        following = false
        seen = Set(items.map(\.id))
    }

    /// The list is at its newest end (reached by hand), or Jump to Latest took it there.
    mutating func attach() {
        following = true
        seen = []
    }

    /// Items that arrived since the reader scrolled away; 0 while following. The gap marker is not an item that arrives.
    func unread(in items: [RecordingTimeline.Item]) -> Int {
        guard !following else { return 0 }
        return items.filter { $0.id != .gap && !seen.contains($0.id) }.count
    }

    /// The Jump to Latest button shows while the reader is away from the newest item.
    var showsJump: Bool { !following }
}
#endif
