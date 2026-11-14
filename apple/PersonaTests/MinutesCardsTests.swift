import Foundation
import Testing
import SwiftUI
@testable import Persona

/// A recording's Summary on iPhone (#5026, docs/apple/recording.md「Minutes」): which view the page shows, which cards the
/// Summary has and in what order, what an item carries, and the transcript lines it opens to (`RecordingMinutesCards`).
struct MinutesCardsTests {
    /// The made-up meeting's transcript (`RecordingSamples.pastMeeting`'s times).
    private let lines = [
        RecordingSentence(start: 2, end: 14, text: "intro"), RecordingSentence(start: 21, end: 38, text: "lock screen"),
        RecordingSentence(start: 44, end: 58, text: "decided"), RecordingSentence(start: 63, end: 80, text: "owner"),
        RecordingSentence(start: 86, end: 101, text: "not settled"), RecordingSentence(start: 105, end: 118, text: "next week"),
    ]

    private var minutes: RecordingMinutes {
        RecordingMinutes(title: "Weekly", when: "2027-01-15 16:00–17:00", place: "Room 3", attendees: ["Ann", "Bo", "Cy"],
                         points: [.init(text: "intro", at: 2), .init(text: "lock screen", at: 21)],
                         decisions: [.init(text: "decided", at: 44)],
                         actions: [.init(text: "owner", at: 63, who: "Ann", by: "Friday")],
                         openQuestions: [.init(text: "not settled", at: 86)])
    }

    // MARK: Which view

    /// No switch before the minutes are ready; then the Summary, unless the page was switched.
    @Test func summaryIsTheDefaultOnceTheMinutesAreReady() {
        #expect(RecordingMinutesCards.pane(picked: nil, ready: false) == nil)
        #expect(RecordingMinutesCards.pane(picked: .transcript, ready: false) == nil)
        #expect(RecordingMinutesCards.pane(picked: nil, ready: true) == .summary)
        #expect(RecordingMinutesCards.pane(picked: .transcript, ready: true) == .transcript)
        #expect(RecordingMinutesCards.pane(picked: .summary, ready: true) == .summary)
    }

    // MARK: Which cards

    @Test func cardsComeInTheSummarysOrder() {
        let summary = RecordingMinutesCards.summary(minutes, sentences: lines)
        #expect(summary.cards.map(\.kind) == [.points, .decisions, .open, .actions])
        #expect(summary.cards.map(\.items.count) == [2, 1, 1, 1])
        #expect(summary.cards.map(\.kind.symbol) == [Theme.Icon.keyPoint, Theme.Icon.decision, Theme.Icon.openQuestion, Theme.Icon.todo])
    }

    /// A group with nothing in it has no card; an item with no words is not a row.
    @Test func emptyGroupsAndBlankItemsAreLeftOut() {
        var some = minutes
        some.decisions = []
        some.openQuestions = [.init(text: "  \n", at: 90)]
        let summary = RecordingMinutesCards.summary(some, sentences: lines)
        #expect(summary.cards.map(\.kind) == [.points, .actions])
        #expect(RecordingMinutesCards.summary(RecordingMinutes(), sentences: lines).cards.isEmpty)
    }

    /// An item keeps its place in the hub's list as its id (a blank one before it still counts), its time, and for an
    /// action who and by when.
    @Test func itemsCarryTheirTimeAndByline() throws {
        var some = minutes
        some.points.insert(.init(text: ""), at: 0)
        let summary = RecordingMinutesCards.summary(some, sentences: lines)
        let points = try #require(summary.cards.first { $0.kind == .points })
        #expect(points.items.map(\.id) == ["points-1", "points-2"])
        let action = try #require(summary.cards.first { $0.kind == .actions }?.items.first)
        #expect(action.text == "owner")
        #expect(action.at == 63)
        #expect(action.byline == "Ann · Friday")
        #expect(points.items.first?.byline == nil)
    }

    /// The header's lines: the minutes' own title, when · where, and who was there; nothing for what they leave out.
    @Test func headerLines() {
        let summary = RecordingMinutesCards.summary(minutes, sentences: lines)
        #expect(summary.title == "Weekly")
        #expect(summary.facts == "2027-01-15 16:00–17:00 · Room 3")
        #expect(["Ann", "Bo", "Cy"].allSatisfy { summary.attendees?.contains($0) == true })
        let bare = RecordingMinutesCards.summary(RecordingMinutes(title: "", place: "Room 3", points: [.init(text: "a")]), sentences: [])
        #expect(bare.title == nil)
        #expect(bare.facts == "Room 3")
        #expect(bare.attendees == nil)
    }

    /// When is the app's own date style from `start` / `end` (#4875), as the minutes' sections write it; the hub's `when`
    /// only without them.
    @Test func headerWhenIsTheAppsDateStyle() throws {
        var dated = minutes
        dated.start = try #require(JSON.parseDate("2027-01-15T16:00:00+08:00"))
        dated.end = try #require(JSON.parseDate("2027-01-15T17:00:00+08:00"))
        let summary = RecordingMinutesCards.summary(dated, sentences: lines)
        let when = try #require(dated.whenLine)
        #expect(summary.facts == when + " · Room 3")
        #expect(summary.facts?.contains("2027-01-15 16:00") == false)
    }

    /// An item that cites a meeting photo (#3862 phase 3) carries its number, for the card's thumbnail.
    @Test func itemsCarryTheirCitedPhoto() throws {
        var some = minutes
        some.points.append(.init(text: "the whiteboard", at: 30, photo: 3))
        let summary = RecordingMinutesCards.summary(some, sentences: lines)
        let points = try #require(summary.cards.first { $0.kind == .points })
        #expect(points.items.map(\.photo) == [nil, nil, 3])
    }

    /// Action items as to-dos (#4062): the hub's name of each and whether it is one; Add All sends those left, only for
    /// numbered minutes, and never one without a name.
    @Test func actionItemsCarryWhatAddToToDosSends() throws {
        var some = minutes
        some.version = 2
        some.actions = [.init(text: "one", at: 63, id: "a1"), .init(text: "two", at: 70, id: "a2", todoID: "t9"),
                        .init(text: "three", at: 75, id: "a3"), .init(text: "four", at: 80, id: "")]
        let summary = RecordingMinutesCards.summary(some, sentences: lines)
        let actions = try #require(summary.cards.first { $0.kind == .actions })
        #expect(actions.items.map(\.action) == ["a1", "a2", "a3", nil])
        #expect(actions.items.map(\.added) == [false, true, false, false])
        #expect(summary.offered)
        #expect(summary.addable == ["a1", "a3"])
        // A point's id is not an action's.
        some.points = [.init(text: "p", at: 2, id: "p1")]
        #expect(RecordingMinutesCards.summary(some, sentences: lines).cards.first?.items.first?.action == nil)
        // Minutes the hub doesn't number offer none.
        some.version = nil
        let old = RecordingMinutesCards.summary(some, sentences: lines)
        #expect(!old.offered)
        #expect(old.addable.isEmpty)
    }

    // MARK: The lines an item opens to

    /// The line its time picks, one before it and two after it (`Theme.Recording.contextBefore` / `contextAfter`).
    @Test func contextIsTheLineAndItsNeighbours() throws {
        let summary = RecordingMinutesCards.summary(minutes, sentences: lines)
        let decision = try #require(summary.cards.first { $0.kind == .decisions }?.items.first)
        #expect(decision.context.map(\.id) == [1, 2, 3, 4])
        #expect(decision.context.map(\.text) == ["lock screen", "decided", "owner", "not settled"])
        #expect(decision.context.filter(\.said).map(\.id) == [2])
        #expect(decision.context.first?.start == 21)
    }

    /// At the transcript's ends it keeps what there is; a time between two lines picks the one said by then.
    @Test func contextStaysInsideTheTranscript() {
        #expect(RecordingMinutesCards.context(at: 2, in: lines, before: 1, after: 2).map(\.id) == [0, 1, 2])
        #expect(RecordingMinutesCards.context(at: 110, in: lines, before: 1, after: 2).map(\.id) == [4, 5])
        #expect(RecordingMinutesCards.context(at: 50, in: lines, before: 1, after: 2).filter(\.said).map(\.id) == [2])
        #expect(RecordingMinutesCards.context(at: 44, in: lines, before: 0, after: 0).map(\.id) == [2])
    }

    /// No time, or no transcript: nothing to open.
    @Test func contextNeedsATimeAndATranscript() {
        #expect(RecordingMinutesCards.context(at: nil, in: lines, before: 1, after: 2).isEmpty)
        #expect(RecordingMinutesCards.context(at: 44, in: [], before: 1, after: 2).isEmpty)
        #expect(RecordingMinutesCards.context(at: .nan, in: lines, before: 1, after: 2).isEmpty)
        var untimed = minutes
        untimed.points = [.init(text: "said sometime")]
        let summary = RecordingMinutesCards.summary(untimed, sentences: lines)
        #expect(summary.cards.first?.items.first?.context.isEmpty == true)
    }

    // MARK: The summary card (#6563)

    private typealias Line = RecordingMinutes.Overview.Line

    /// The made-up meeting's minutes with an overview: two groups, the first with a topic of six bullets.
    private var rich: RecordingMinutes {
        var m = minutes
        m.version = 2
        m.actions = [.init(text: "owner", at: 63, who: "Ann", by: "Friday", id: "a1"),
                     .init(text: "done one", at: 70, id: "a2", todoID: "t9")]
        m.overview = .init(headline: "Lock screen ships", conclusions: [Line(text: "Ships Friday", strong: ["Friday"], at: 44)], groups: [
            .init(title: "Recording", topics: [
                .init(title: "Lock screen", icon: .progress, bullets: [
                    Line(text: "intro", at: 2), Line(text: "lock screen", strong: ["lock"], at: 21, who: "Ann"),
                    Line(text: "decided", at: 44, decision: true), Line(text: "four", at: 63), Line(text: "five", at: 86),
                    Line(text: "six", at: 105, photo: 3)]),
                .init(title: "Release", icon: .release, bullets: [Line(text: "release", at: 30)]),
            ]),
            .init(title: "Tooling", topics: [.init(title: "Tests", icon: .quality, bullets: [Line(text: "tests", at: 86)])]),
        ])
        return m
    }

    /// The layout is settled where the cards are worked out: the summary card only for minutes with an overview, the
    /// four cards' groups kept for Action Items and Open Questions, and what Add All sends unchanged.
    @Test func summaryCardOnlyWithAnOverview() throws {
        #expect(RecordingMinutesCards.summary(minutes, sentences: lines).overview == nil)
        let summary = RecordingMinutesCards.summary(rich, sentences: lines)
        let o = try #require(summary.overview)
        #expect(o.headline == "Lock screen ships")
        #expect(summary.cards.map(\.kind) == [.points, .decisions, .open, .actions])
        #expect(summary.addable == ["a1"])
        // The four cards come out the same with or without it.
        var plain = rich
        plain.overview = nil
        let legacy = RecordingMinutesCards.summary(plain, sentences: lines)
        #expect(legacy.cards == summary.cards && legacy.title == summary.title && legacy.facts == summary.facts)
        #expect(legacy.attendees == summary.attendees && legacy.addable == summary.addable && legacy.offered == summary.offered)
    }

    /// Groups take the four tints in turn by their place; an untitled group has no heading; ids are by place.
    @Test func groupsTakeTheTintsInTurn() throws {
        var m = rich
        let one = RecordingMinutes.Overview.TopicGroup(title: "G", topics: [.init(title: "T", bullets: [Line(text: "x", at: 2)])])
        m.overview?.groups = [one, one, one, one, one, .init(title: " ", topics: one.topics)]
        let o = try #require(RecordingMinutesCards.summary(m, sentences: lines).overview)
        #expect(o.groups.map(\.hue) == [0, 1, 2, 3, 0, 1])
        #expect(o.groups.map(\.title) == ["G", "G", "G", "G", "G", nil])
        #expect(o.groups.map(\.id) == ["group-0", "group-1", "group-2", "group-3", "group-4", "group-5"])
        #expect(o.groups.flatMap(\.topics).map(\.hue) == [0, 1, 2, 3, 0, 1])
        #expect(Theme.Recording.topicTints == 4 && Theme.Hue.minutesGroups.count == 4)
        let r = try #require(RecordingMinutesCards.summary(rich, sentences: lines).overview)
        #expect(r.groups.flatMap(\.topics).map(\.id) == ["topic-0-0", "topic-0-1", "topic-1-0"])
        #expect(r.groups[0].topics[0].bullets.map(\.id) == (0..<6).map { "topic-0-0-\($0)" })
        #expect(r.conclusions.map(\.id) == ["conclusion-0"])
    }

    /// Each icon has its own symbol; `general` stands for what the app does not know.
    @Test func topicIconsMapToTheirSymbols() throws {
        let symbols = RecordingMinutes.TopicIcon.allCases.map(RecordingMinutesCards.symbol)
        #expect(Set(symbols).count == symbols.count)
        #expect(RecordingMinutesCards.symbol(.general) == Theme.Icon.topicGeneral)
        #expect(RecordingMinutesCards.symbol(.people) == Theme.Icon.people)
        let o = try #require(RecordingMinutesCards.summary(rich, sentences: lines).overview)
        #expect(o.groups.flatMap(\.topics).map(\.symbol) == [Theme.Icon.topicProgress, Theme.Icon.topicRelease, Theme.Icon.topicQuality])
    }

    /// A topic shows four bullets, then Show N More opens the rest.
    @Test func topicsFoldAfterFour() throws {
        let o = try #require(RecordingMinutesCards.summary(rich, sentences: lines).overview)
        let topics = o.groups.flatMap(\.topics)
        #expect(Theme.Recording.topicBulletsShown == 4)
        #expect(topics.map(\.shown) == [4, 1, 1])
        #expect(topics.map(\.folded) == [2, 0, 0])
    }

    /// A bullet: its words with their bold places, its time and transcript lines as an item's, its photo; a point its
    /// speaker, a decision the word Decision. A conclusion opens no lines.
    @Test func bulletsAndConclusionsCarryWhatTheyShow() throws {
        let o = try #require(RecordingMinutesCards.summary(rich, sentences: lines).overview)
        let bullets = o.groups[0].topics[0].bullets
        #expect(bullets.map(\.text) == ["intro", "lock screen", "decided", "four", "five", "six"])
        #expect(bullets.map(\.decision) == [false, false, true, false, false, false])
        #expect(bullets.map(\.byline) == [nil, "Ann", String(localized: "Decision [Minutes Overview]"), nil, nil, nil])
        #expect(bullets[2].context.filter(\.said).map(\.id) == [2])
        #expect(bullets[2].context.map(\.id) == [1, 2, 3, 4])
        #expect(bullets[5].photo == 3)
        let words = try #require(bullets[1].words)
        #expect(String(words.characters) == "lock screen")
        #expect(words.runs.map { $0.inlinePresentationIntent } == [.stronglyEmphasized, nil])
        let conclusion = try #require(o.conclusions.first)
        #expect(conclusion.at == 44 && conclusion.context.isEmpty && conclusion.byline == nil)
        #expect(conclusion.words.map { String($0.characters) } == "Ships Friday")
        // Bullets are never actions: an action's to-do is the Action Items card's.
        #expect(bullets.allSatisfy { $0.action == nil && !$0.added && $0.todo == nil })
        let actions = try #require(RecordingMinutesCards.summary(rich, sentences: lines).cards.first { $0.kind == .actions })
        #expect(actions.items.map(\.todo) == [nil, "t9"])
    }

    /// The summary card leaves out what the page's header already says of the meeting; the four cards keep it all.
    @Test func summaryCardFactsAreWhatTheHeaderDoesNotSay() throws {
        let all = try #require(RecordingMinutesCards.summary(rich, sentences: lines).overview)
        #expect(all.facts == "2027-01-15 16:00–17:00 · Room 3")
        #expect(["Ann", "Bo", "Cy"].allSatisfy { all.attendees?.contains($0) == true })
        let some = try #require(RecordingMinutesCards.summary(rich, sentences: lines, shown: .init(when: true, attendees: true)).overview)
        #expect(some.facts == "Room 3" && some.attendees == nil)
        // One symbol per meaning, as the page's header: the calendar with a time, the pin for the place alone.
        #expect(all.factsSymbol == Theme.Icon.calendarEvent && some.factsSymbol == Theme.Icon.place)
        var placeOnly = rich
        placeOnly.start = nil; placeOnly.end = nil; placeOnly.when = nil
        let unsaid = try #require(RecordingMinutesCards.summary(placeOnly, sentences: lines).overview)
        #expect(unsaid.facts == "Room 3" && unsaid.factsSymbol == Theme.Icon.place)
        let timeOnly = try #require(RecordingMinutesCards.summary(rich, sentences: lines, shown: .init(place: true)).overview)
        #expect(timeOnly.facts == "2027-01-15 16:00–17:00" && timeOnly.factsSymbol == Theme.Icon.calendarEvent)
        let none = RecordingMinutesCards.summary(rich, sentences: lines, shown: .init(when: true, place: true, attendees: true))
        #expect(none.overview?.facts == nil && none.overview?.attendees == nil)
        #expect(none.facts == "2027-01-15 16:00–17:00 · Room 3" && none.attendees != nil)
        // What `RecordingMeetingFacts` draws for a calendar event.
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        let event = RecordingMeeting(eventID: "e", title: "Weekly", start: start, end: start.addingTimeInterval(3600), location: "",
                                     attendees: ["Ann"])
        #expect(RecordingMinutesCards.Shown(event) == .init(when: true, place: false, attendees: true))
        #expect(RecordingMinutesCards.Shown(nil) == .init())
    }

    /// A topic's tile goes above its title at the accessibility sizes (the contract's largest text sizes), beside it
    /// below them (MinutesOverviewUITests measures both).
    @MainActor @Test func theTileGoesAboveTheTitleAtTheLargestSizes() {
        #expect(!MinutesTopicCard.tileAbove(.large) && !MinutesTopicCard.tileAbove(.xxxLarge))
        #expect(MinutesTopicCard.tileAbove(.accessibility1) && MinutesTopicCard.tileAbove(.accessibility5))
    }

    /// A topic card keeps its fold and open bullets while what it says stays (a transcript refresh brings new lines
    /// around a bullet), and starts over when rewritten minutes say something else in its place.
    @MainActor @Test func aTopicCardStartsOverOnlyWhenItsWordsChange() {
        func topic(_ text: String, context: [RecordingMinutesCards.Line] = []) -> RecordingMinutesCards.Topic {
            .init(id: "topic-0-0", title: "Export", symbol: Theme.Icon.topicGeneral, hue: 0,
                  bullets: [.init(id: "topic-0-0-0", text: text, at: 60, context: context)], shown: 4)
        }
        let refreshed = topic("Export ships Friday", context: [.init(id: 3, start: 58, text: "Friday it is", said: true)])
        #expect(topic("Export ships Friday").says == refreshed.says)
        #expect(topic("Export ships Friday").says != topic("Export slips a week").says)
    }

    /// No headline: the card heads with the minutes' title (the page decides whether it repeats the header).
    @Test func noHeadlineHeadsWithTheTitle() throws {
        var m = rich
        m.overview?.headline = nil
        let summary = RecordingMinutesCards.summary(m, sentences: lines)
        #expect(summary.overview?.headline == nil)
        #expect(summary.title == "Weekly")
    }

    // MARK: The shared fixture

    /// The mock hub, the web and this app read the same cases (spec/fixtures/minutes-overview/cases.json, #6563): which
    /// layout, and for the summary card its headline, conclusions and groups with every text's bold segments, time,
    /// photo, speaker and decision mark, after the contract's cleaning. A broken overview never fails the minutes.
    @Test func sharedFixtureCases() throws {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appending(path: "spec/fixtures/minutes-overview/cases.json")
        let root = try #require(try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any])
        let cases = try #require(root["cases"] as? [[String: Any]])
        #expect(cases.count >= 10, "the fixture has \(cases.count) cases")
        func line(_ l: RecordingMinutes.Overview.Line, bullet: Bool) -> [String: Any] {
            var out: [String: Any] = [
                "segments": RecordingMinutes.boldParts(l.text, strong: l.strong).map { ["text": $0.text, "bold": $0.bold] as [String: Any] },
                "at": l.at.map { $0 as Any } ?? NSNull(), "photo": l.photo.map { $0 as Any } ?? NSNull(),
            ]
            if bullet {
                out["who"] = l.who.map { $0 as Any } ?? NSNull()
                out["decision"] = l.decision
            }
            return out
        }
        for c in cases {
            let name = c["name"] as? String ?? "?"
            let input = try #require((c["input"] as? [String: Any])?["minutes"], "\(name)")
            let minutes = try JSONDecoder().decode(RecordingMinutes.self, from: JSONSerialization.data(withJSONObject: input))
            let expected = try #require(c["expected"] as? [String: Any], "\(name)")
            let summary = RecordingMinutesCards.summary(minutes, sentences: [])
            #expect((summary.overview == nil ? "legacy" : "overview") == expected["layout"] as? String, "\(name)")
            // The lists are read whatever the overview's shape.
            let raw = input as? [String: Any]
            #expect(minutes.points.count == (raw?["points"] as? [Any])?.count ?? 0, "\(name)")
            #expect(minutes.decisions.count == (raw?["decisions"] as? [Any])?.count ?? 0, "\(name)")
            guard let o = minutes.overview else {
                #expect(expected["overview"] is NSNull || expected["overview"] == nil, "\(name)")
                continue
            }
            let got: [String: Any] = [
                "headline": o.headline.map { $0 as Any } ?? NSNull(),
                "conclusions": o.conclusions.map { line($0, bullet: false) },
                "groups": o.groups.map { g in
                    ["title": g.title, "topics": g.topics.map { t in
                        ["title": t.title, "icon": t.icon.rawValue, "bullets": t.bullets.map { line($0, bullet: true) }] as [String: Any]
                    }] as [String: Any]
                },
            ]
            let want = try #require(expected["overview"] as? [String: Any], "\(name)")
            #expect(NSDictionary(dictionary: got).isEqual(to: want), "\(name):\n got \(got)\n want \(want)")
            // The page's bold runs are the same segments.
            let first = try #require(o.groups.first?.topics.first?.bullets.first)
            let words = try #require(summary.overview?.groups.first?.topics.first?.bullets.first?.words)
            let runs = words.runs.map { (String(words[$0.range].characters), $0.inlinePresentationIntent == .stronglyEmphasized) }
            // Runs side by side in the same weight are one run.
            let parts = RecordingMinutes.boldParts(first.text, strong: first.strong).filter { !$0.text.isEmpty }
                .reduce(into: [RecordingMinutes.BoldPart]()) { out, p in
                    if out.last?.bold == p.bold { out[out.count - 1].text += p.text } else { out.append(p) }
                }
            #expect(runs.map(\.0) == parts.map(\.text) && runs.map(\.1) == parts.map(\.bold), "\(name)")
        }
    }
}
