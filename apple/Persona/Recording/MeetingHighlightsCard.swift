#if os(iOS)
import SwiftUI

/// What the meeting page's top card shows (#3862 phase 2b; docs/assistant/meeting-minutes.md「The meeting card」), worked
/// out here and not in a `body` (P4), pure, for the tests: for a meeting started with the highlights, the Highlights card
/// with the room's highlights once it set any, and its waiting line before them while Persona attends; phase 2's Live box
/// only where there can be no highlights (started without them, or Persona doesn't attend and none came). While the
/// keyboard is up each one folds.
nonisolated enum MeetingHighlights {
    nonisolated enum Mode: Equatable, Sendable {
        /// Phase 2's Live box; `folded`: only its newest line, without Full Transcript.
        case live(folded: Bool)
        /// The Highlights card's waiting line, before the first highlights; `folded`: without Full Transcript.
        case waiting(folded: Bool)
        /// The highlights; `folded`: only the newest, without Full Transcript.
        case highlights(folded: Bool)

        /// The keyboard is up: the card keeps only its newest line or highlight, or the waiting line.
        var folded: Bool {
            switch self {
            case .live(let folded), .waiting(let folded), .highlights(let folded): folded
            }
        }
    }

    /// One highlight as the card lists it: its time in the clock's words, and an identity that stays while the room
    /// sends the list again (its time and words; the same ones twice get their count), so a kept row is not rebuilt (P6).
    nonisolated struct Row: Identifiable, Equatable, Sendable {
        var id: String
        var text: String
        var at: Double
        var end: Double
        /// `RecordingFormat.clock(at)`: from the recording's start.
        var clock: String
    }

    /// The card for a meeting started with the highlights (`shows`, `RecordingAttendance.highlights`): the highlights once
    /// there are any, whatever the role is by then; before them the waiting line while Persona attends (`attending`, the
    /// rule below), else the Live box. A meeting started without them always has the Live box. While the keyboard is up
    /// (`writing`) the same card, folded.
    static func mode(shows: Bool, hasHighlights: Bool, attending: Bool, writing: Bool) -> Mode {
        let folded = writing
        guard shows else { return .live(folded: folded) }
        if hasHighlights { return .highlights(folded: folded) }
        return attending ? .waiting(folded: folded) : .live(folded: folded)
    }

    /// Persona attends the meeting and its room still gets it, for the waiting line: only then can highlights come. Built
    /// on the page's own rule for attending (`MeetingAsk.canAsk`, which the subtitle, the bar and the asks follow: the
    /// role isn't off, the phone hasn't stopped attending after an older manager's `404`, and `roomMissing`, the last
    /// upload's answer had no room, an older manager after a rollback), and no more: the phone still sends the meeting's
    /// lines (`stopped` nil: after `live_full` no batch reaches the room), the hub took the role (`refused`: it stored it
    /// as off, `RecordingSync.attendanceProblem`), and the room isn't unavailable. Each of those has its banner on the page
    /// (`MeetingPage.problems`), which a waiting line would contradict. A room not read yet, starting or ready attends.
    static func attending(_ entry: RecordingEntry, room: MeetingRoom?, roomMissing: Bool, refused: Bool) -> Bool {
        MeetingAsk.canAsk(entry, roomMissing: roomMissing) && entry.attendance?.stopped == nil && !refused
            && room?.known != .unavailable
    }

    /// The rows, in the order given (`MeetingConversation.applyHighlights` keeps them earliest first).
    static func rows(_ highlights: [MeetingHighlight]) -> [Row] {
        var used = Set<String>()
        return highlights.map { h in
            let key = "\(RecordingTimeline.millis(h.at))|\(h.text)"
            var id = key, n = 1
            while used.contains(id) {
                n += 1
                id = "\(key)#\(n)"
            }
            used.insert(id)
            return Row(id: id, text: h.text, at: h.at, end: h.end, clock: RecordingFormat.clock(h.at))
        }
    }

    /// The newest highlight: the latest `at`, the last given of those at the same time.
    static func newest(_ rows: [Row]) -> Row? {
        rows.enumerated().max { a, b in a.element.at != b.element.at ? a.element.at < b.element.at : a.offset < b.offset }?.element
    }

    /// The highlights the card shows: every one, or folded (the keyboard is up) the newest alone.
    static func shown(_ rows: [Row], folded: Bool) -> [Row] {
        guard folded else { return rows }
        return newest(rows).map { [$0] } ?? []
    }
}

/// The meeting page's top card (#3862 phase 2b; docs/design/DESIGN_SYSTEM.md「Meeting page (#3862)」): for a meeting
/// started with the highlights, the Highlights card (the room's highlights, or its waiting line before the first while
/// Persona attends), the gist of the meeting so far in the room's words with the transcript one tap away; phase 2's Live
/// box as it was where there can be no highlights. A change of card cross-fades in place on `Theme.Motion.state` (at once
/// with Reduce Motion). The page hands it the meeting's highlights as it reads them, so this card observes nothing of
/// `MeetingConversation`, and its rows are listed as it is made (at most four, as `MeetingTopicsRow` lists the topics):
/// the first frame is already the right card, so nothing animates as the page opens or switches layout. The transcript a
/// highlight's time pushes is the page's, kept while the page is open. A new or changed highlight is never announced.
struct MeetingTopCard: View {
    var entry: RecordingEntry
    var exchanges: [MeetingAsk.Exchange]
    /// Persona attends and its room still gets the meeting (`MeetingHighlights.attending`): the waiting line before the
    /// first highlights.
    var attending: Bool
    /// The composer has the keyboard (the page passes it at the regular sizes only).
    var writing: Bool
    /// The transcript pushed at a highlight's time (the page's `navigationDestination`).
    @Binding var transcriptAt: MeetingTranscriptFocus?
    private let rows: [MeetingHighlights.Row]

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// `highlights`: the room's, as the page read them (none for a meeting started without them).
    init(entry: RecordingEntry, exchanges: [MeetingAsk.Exchange], highlights: [MeetingHighlight], attending: Bool, writing: Bool,
         transcriptAt: Binding<MeetingTranscriptFocus?>) {
        self.entry = entry
        self.exchanges = exchanges
        self.attending = attending
        self.writing = writing
        _transcriptAt = transcriptAt
        rows = MeetingHighlights.rows(highlights)
    }

    var body: some View {
        let mode = MeetingHighlights.mode(shows: entry.attendance?.highlights == true, hasHighlights: !rows.isEmpty,
                                          attending: attending, writing: writing)
        // One over the other while they fade, from the top: in a stack of their own the outgoing card would keep its slot
        // until it is gone, and push the thread down meanwhile. Only a change of the mode animates (the first highlights,
        // the keyboard, the role turned off before any), never the first frame. The waiting line and the highlights are
        // one card, so only what is inside it changes.
        ZStack(alignment: .top) {
            switch mode {
            case .live(let folded):
                MeetingLiveBox(entry: entry, exchanges: exchanges, folded: folded)
                    .transition(.opacity)
            case .waiting, .highlights:
                MeetingHighlightsCard(id: entry.id, exchanges: exchanges, rows: rows, folded: mode.folded,
                                      transcriptAt: $transcriptAt)
                    .transition(.opacity)
            }
        }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: mode)
    }
}

/// The Highlights card: "Highlights", then each highlight with its time, earliest first, or before the first the
/// waiting line, and Full Transcript at the trailing side. No transcript lines: during the meeting the owner reads the
/// points, and the words as said are one tap away. Folded (the keyboard is up), only the newest highlight or the waiting
/// line, without Full Transcript. It takes only what it draws (P5): the recording's id for Full Transcript, not the
/// recording, so a save of its transcript (each segment, each revision) gives it nothing new and doesn't redraw it. In
/// hub mode (#3862 phase 5) the notice that transcription fell back to this iPhone sits under its title, as in the Live
/// box; the hub's lines and caption are on the Full Transcript page.
private struct MeetingHighlightsCard: View {
    var id: UUID
    var exchanges: [MeetingAsk.Exchange]
    var rows: [MeetingHighlights.Row]
    var folded: Bool
    /// The page's transcript pushed at a highlight's time.
    @Binding var transcriptAt: MeetingTranscriptFocus?

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Text("Highlights [Meeting]").font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                .accessibilityAddTraits(.isHeader)
            // Hub transcription fell back to this iPhone (#3862 phase 5): said here as in the Live box, though the card shows
            // no lines, since the transcript one tap away is the phone's own then. Only that row reads the store.
            MeetingTranscriptionFallbackRow(id: id)
            // The waiting line and the first highlights cross-fade one over the other, from the top.
            ZStack(alignment: .topLeading) {
                if rows.isEmpty {
                    Text("Persona is listening. Highlights come after a few minutes.")
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .accessibilityIdentifier("meeting-highlights-waiting")
                        .transition(.opacity)
                } else {
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        ForEach(MeetingHighlights.shown(rows, folded: folded)) { row in
                            highlight(row)
                                .transition(.opacity)
                        }
                    }
                    .transition(.opacity)
                }
            }
            if !folded {
                HStack {
                    Spacer(minLength: Theme.Space.flush)
                    MeetingFullTranscriptLink(id: id, exchanges: exchanges)
                }
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.top, Theme.Space.s)
        // Full Transcript's own 44 pt height closes the card, as it closes the Live box; folded, without it, a margin does.
        .padding(.bottom, folded ? Theme.Space.s : Theme.Space.flush)
        .frame(maxWidth: .infinity, alignment: .leading)
        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("meeting-highlights")
    }

    /// A highlight: its time as the minutes cards' chip, then its words (`Theme.Recording.highlightLines` lines at most,
    /// cut at the end; VoiceOver reads them whole).
    @ViewBuilder private func highlight(_ row: MeetingHighlights.Row) -> some View {
        let words = Text(row.text).font(Theme.Font.body)
            .lineLimit(Theme.Recording.highlightLines)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) { time(row); words }
        // The words and their time together; the time stays its own button (a `.combine` would hide it from the tests).
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("meeting-highlight")
    }

    /// Its time from the recording's start, the minutes cards' time: a tap opens the transcript at its minute, lit.
    private func time(_ row: MeetingHighlights.Row) -> some View {
        Button { transcriptAt = MeetingTranscriptFocus(at: row.at, end: row.end) } label: {
            Chip(text: row.clock, look: .tint(Theme.accent), compact: true)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(String(localized: "Show at \(row.clock) in the transcript"))
        .accessibilityIdentifier("meeting-highlight-time")
    }
}
#endif
