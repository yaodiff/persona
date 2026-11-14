import SwiftUI

// The meeting recording page's tokens (docs/apple/recording.md, docs/design/proposals/meeting-recording.md section 7): how
// often the level meter is drawn, how large it is, and how a timeline row arrives. A file of their own, away from
// Theme.swift. Shared/, so the watch and the widgets compile them; they draw no recording UI.

nonisolated extension Theme.Motion {
    /// How often the level meter is drawn: 30 a second, as the voice orb and the avatar's halo. The smoothed input level
    /// changes about 20 times a second, so more frames add nothing, and a meeting can run for hours with the screen on.
    /// A clock drives it, not a repeating animation (a repeating animation leaks into the layout around it, `StatusDot`).
    static let recordingLevelFrame: TimeInterval = 1.0 / 30
}

nonisolated extension Theme.Size {
    /// The level meter's bars at the default text size: a bar's width, the gap between two, and the height they all
    /// share. Every bar has this height and is drawn with a vertical scale, so the meter moves only a transform and never
    /// relays out (docs/apple/ios.md, red line for loops).
    static let recordingMeterBar: CGFloat = 3
    static let recordingMeterGap: CGFloat = 3
    static let recordingMeterHeight: CGFloat = 44
    /// The widest a topic's chip on the meeting page gets (#3862 phase 2) at the default text size: a title of up to 80
    /// characters is cut there, so the row shows the topics around the current one rather than one long title.
    static let meetingTopicChip: CGFloat = 240
}

nonisolated extension Theme {
    /// The recording page's numbers that are not sizes, spaces or times.
    enum Recording {
        /// How many bars the level meter has.
        static let meterBars = 40
        /// The shortest a bar gets (a share of the meter's height): a silent microphone is a flat row of dots, so a
        /// meter that does not move still says it is there.
        static let meterFloor: Double = 0.08
        /// The one still frame of a meter that does not move (Reduce Motion, Low Power Mode): how tall the middle bar
        /// stands, as a share of the meter's height. The same at any level, so it says nothing about the microphone.
        static let meterStill: Double = 0.45
        /// How much of a bar's height the meter's ripple moves (the rest follows the level), and how fast it moves, in
        /// radians a second.
        static let meterRipple: Double = 0.35
        static let meterRippleSpeed: Double = 9
        /// A paused or ended meter and recording mark: dimmed, never gone.
        static let dimmed: Double = 0.35
        /// The typed question's field in the live bar (#4486): it grows to this many lines, then scrolls, so the
        /// timeline above keeps most of the room the keyboard leaves.
        static let askLines = 5
        /// Its send button while there are no words to send: there, so the row does not jump, and plainly unavailable.
        static let askSendIdle: Double = dimmed
        /// A row arriving in the timeline starts this far below its place (the `reveal` curve brings it up).
        static let arrivalOffset: CGFloat = Theme.Space.s
        /// The largest text size the big timer grows to: past it the time stays on one line, readable, instead of
        /// shrinking text to hide that it does not fit.
        static let timerLargestSize = DynamicTypeSize.accessibility2
        /// How many seconds of audio waiting for its text are worth a "Transcribing the last N s" marker, and the step
        /// the number moves in (so it changes a few times a minute, not each second).
        static let gapMinimum = 5
        static let gapStep = 5
        /// How often the spoken time (VoiceOver's value for the timer) is refreshed, and the marker's number.
        static let spokenTimeRefresh: TimeInterval = 60
        static let gapRefresh: TimeInterval = 5
        /// The meeting page (#3862): its title (the live header, the status card after the meeting) takes at most this
        /// many lines.
        static let pageTitleLines = 2
        /// How many of the newest transcript lines its Live box shows, and how many lines of text each may take: the
        /// box says what is being said now, the whole transcript is one tap away.
        static let liveLines = 3
        static let liveLineLength = 2
        /// The step, in seconds, of the Live box's "About N s behind" (so it changes a few times a minute).
        static let liveBehindStep = 5
        /// How many lines of text a highlight on the meeting page's top card takes at the regular text sizes (#3862 phase
        /// 2b), cut at the end (VoiceOver reads it whole): four highlights stay a glance, and the thread under the card,
        /// which alone scrolls there, keeps its height.
        static let highlightLines = 2
        /// How many exchanges Show Earlier brings of the room conversation: each batch of lines is a turn of its own,
        /// so the chat's two would bring little.
        static let earlierTurns = 6
        /// A photo in the meeting's thread: its thumbnail's side.
        static let photoSide: CGFloat = Theme.Size.box80
        /// The meeting's material (the follow-up's first message) opened: this many lines show until Show All, which a
        /// text longer than that, or than this many characters, offers.
        static let materialLines = 12
        static let materialPreview = 1200
        /// How often the page after the meeting asks the hub for the minutes while they are on their way, in seconds,
        /// as the wrap-up and the recording's page do.
        static let minutesPoll: TimeInterval = 15
        /// A meeting photo's thumbnail in the thread (#3862 phase 3), in points (its pixels: × the screen's scale): the
        /// side of the room's other pictures, so the two look alike, and large enough to tell one whiteboard from another.
        static let photoThumbSide: CGFloat = photoSide
        /// How many lines of a photo's caption, or of the text read in it, show beside its thumbnail: what it is about,
        /// not all of it (the room's note says what matters).
        static let photoTextLines = 3
        /// How many photos the library's picker takes at once: each is prepared and uploaded on its own, about a
        /// megabyte each, while the meeting records.
        static let photoPickLimit = 4
        /// How long "Persona is looking at the photo…" waits for the room's note after the photo was added, in seconds:
        /// a room that never writes one doesn't keep the spinner turning for the rest of the meeting.
        static let photoNoteWait: TimeInterval = 120
        /// A Summary item opened in place (#5026): the transcript line its time picks, with this many lines before it
        /// and after it. One before says what led to it; two after hold the reply or the follow-up, since an item is
        /// stamped as its sentence begins. Few enough that the card stays a summary.
        static let contextBefore = 1
        static let contextAfter = 2
        /// A topic card of the summary card (#6563) shows this many bullets, then Show N More opens the rest in place: a
        /// card stays a glance, and the hub's topics have two to four lines.
        static let topicBulletsShown = 4
        /// A summary-card item keeps its time and disclosure beside its words while the words have at least this many
        /// times their line's height of width (about eight Chinese characters a line), else they go on the line under
        /// the words (`MinutesItemLayout`): by the room the words have, which keep growing past the interface's cap.
        static let itemWordsColumn: CGFloat = 7
        /// How many tints the summary card's groups take in turn (`Theme.Hue.minutesGroups`).
        static let topicTints = Theme.Hue.minutesGroups.count
        /// How much the meeting page's Talk grows while it is held (#3862 phase 4): the finger covers it, so the change
        /// that says "Persona hears you" is its color and the words over the field; the size only confirms the touch.
        /// None under Reduce Motion.
        static let talkHeldScale: CGFloat = 1.06
    }
}
