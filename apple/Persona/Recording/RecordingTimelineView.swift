#if os(iOS)
import SwiftUI

/// The live sheet's timeline (docs/design/proposals/meeting-recording.md section 9): the text as it is transcribed,
/// bookmarks and Persona's exchanges, in one stream that follows its newest item until the reader scrolls away, then offers
/// Jump to Latest with a count of what arrived. The order, the ids and the follow rules are pure (`RecordingTimeline`,
/// `RecordingFollow`); this draws them.
struct RecordingTimelineRows: View {
    var entry: RecordingEntry
    var exchanges: [MeetingAsk.Exchange]
    var paused: Bool
    /// Whether the Jump to Latest button should show, and how many arrived: reported to the page that draws it.
    @Binding var items: [RecordingTimeline.Item]
    /// Rows lit for a moment (`Theme.accentTint`, and selected for VoiceOver): the meeting page's transcript opened at a
    /// highlight's minute (#3862 phase 2b, `RecordingTimeline.focus`). Every other page passes none.
    var lit: Set<RecordingTimeline.Item.ID> = []
    /// The hub's lines in place of the phone's sentences: the meeting page's transcript in hub mode while the page shows
    /// the hub's (#3862 phase 5, `MeetingTranscription.shown`). Nil, the phone's own: every other page, and a fallback.
    var hubLines: [MeetingHubLine]? = nil

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var capture = RecordingCapture.shared

    var body: some View {
        // A clock for the gap marker's number, not for the rows: the rows are the same between its beats.
        TimelineView(.periodic(from: .now, by: Theme.Recording.gapRefresh)) { _ in
            // The hub's lines come within one window and its caption says what is said now: no gap marker under them.
            let built = RecordingTimeline.make(sentences: hubLines.map(RecordingTimeline.sentences(hub:)) ?? RecordingTimeline.sentences(of: entry),
                                               marks: entry.marks, asks: exchanges.map { .init(id: $0.id, at: $0.at) },
                                               transcribedUntil: hubLines == nil ? RecordingTimeline.transcribedUntil(of: entry) : capture.timeline,
                                               now: capture.timeline)
            rows(built)
                .onChange(of: built, initial: true) { items = built }
        }
    }

    private func rows(_ built: [RecordingTimeline.Item]) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            if !built.contains(where: { if case .sentence = $0.kind { true } else { false } }) {
                listening
            }
            ForEach(built) { item in
                row(item)
                    // The row's own id, so a page can scroll to it (`ScrollViewReader`).
                    .id(item.id)
                    .transition(reduceMotion ? .opacity : .opacity.combined(with: .offset(y: Theme.Recording.arrivalOffset)))
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        // A chunk of sentences lands in one change, so it arrives as one group.
        .animation(reduceMotion ? nil : Theme.Motion.reveal, value: built.map(\.id))
    }

    @ViewBuilder private func row(_ item: RecordingTimeline.Item) -> some View {
        switch item.kind {
        case .sentence(let text):
            sentence(at: item.at, text: text, lit: lit.contains(item.id))
        case .bookmark:
            Label(String(localized: "Bookmark at \(RecordingFormat.clock(item.at))"), systemImage: Theme.Icon.pin)
                .font(Theme.Font.secondaryMedium).foregroundStyle(.secondary)
                .accessibilityIdentifier("recording-timeline-bookmark")
        case .exchange(let id):
            if let exchange = exchanges.first(where: { $0.id == id }) {
                if exchange.id == exchanges.last?.id || exchange.answer == nil {
                    MeetingExchangeRow(exchange: exchange, entry: entry)
                        .padding(Theme.Space.m)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .surface(in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                } else {
                    // Only the newest exchange is a card: an older, answered one is one line, so the text stays on screen.
                    Label(exchange.question ?? String(localized: "What was just said"), systemImage: Theme.Icon.question)
                        .font(Theme.Font.secondaryMedium).foregroundStyle(.secondary).lineLimit(1)
                        .accessibilityIdentifier("recording-timeline-exchange-older")
                }
            }
        case .gap:
            gap(since: item.at)
        }
    }

    private func sentence(at time: Double, text: String, lit: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.m) { stamp(time); words(text) }
        // Lit: the tint reaches a little past the words, so the row's own place doesn't move when it comes and goes.
        .background {
            if lit {
                RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous)
                    .fill(Theme.accentTint)
                    .padding(-Theme.Space.xs)
            }
        }
        .accessibilityElement(children: .combine)
        // Lit is said, not only tinted (DESIGN_SYSTEM.md 6.3): VoiceOver reads the target lines as selected while lit.
        .accessibilityAddTraits(lit ? .isSelected : [])
        .accessibilityIdentifier(lit ? "recording-timeline-sentence-lit" : hubLines == nil ? "recording-timeline-sentence" : "recording-timeline-hub-line")
    }

    private func stamp(_ time: Double) -> some View {
        Text(RecordingFormat.clock(time)).font(Theme.Font.caption.monospacedDigit()).foregroundStyle(.secondary)
    }

    private func words(_ text: String) -> some View {
        Text(text).font(Theme.Font.body).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
    }

    /// Before the first sentence: what to expect, honestly.
    private var listening: some View {
        Label {
            Text(paused ? String(localized: "Paused") : String(localized: "Listening… Text appears here about every half minute."))
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
        } icon: {
            ThemedIcon(Theme.Icon.voiceListening).foregroundStyle(.secondary)
        }
        .accessibilityIdentifier("recording-timeline-listening")
    }

    /// Audio recorded since the last text came in: how much is waiting.
    private func gap(since transcribed: Double) -> some View {
        let seconds = RecordingTimeline.gapSeconds(now: capture.timeline, transcribedUntil: transcribed) ?? 0
        let text = seconds >= 60 ? String(localized: "Transcribing the last \(seconds / 60) min") : String(localized: "Transcribing the last \(seconds) s")
        return Label(text, systemImage: Theme.Icon.time)
            .font(Theme.Font.caption).foregroundStyle(.secondary)
            .accessibilityIdentifier("recording-timeline-gap")
    }
}

/// Jump to Latest: the chat's round button that takes the reader back to the newest item, with the number that arrived.
struct RecordingJumpButton: View {
    var unread: Int
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.down).font(Theme.Font.labelStrong)
                if unread > 0 {
                    Text("\(unread) new").font(Theme.Font.captionMedium).lineLimit(1)
                        .contentTransition(.numericText(value: Double(unread)))
                }
            }
            .foregroundStyle(Theme.accent)
            .padding(.horizontal, unread > 0 ? Theme.Space.m : Theme.Space.flush)
            .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
            .surface(interactive: true, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(unread > 0 ? String(localized: "\(unread) new, jump to latest") : String(localized: "Jump to Latest"))
        .accessibilityIdentifier("recording-jump-latest")
    }
}
#endif
