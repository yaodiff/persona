import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A row's second line in words (#10465): every key of spec/fixtures/todo-view/README.md "Line keys" is said in en,
/// zh-Hans and ja, never as the raw key; times become words; a key this build does not know draws no line.
struct TodoLineTextTests {
    private static let readme = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures/todo-view/README.md")
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let calendar = Calendar(identifier: .gregorian)

    /// The keys of the README's "Line keys" table.
    private static func readmeKeys() throws -> Set<String> {
        let text = try String(contentsOf: readme, encoding: .utf8)
        let section = try #require(text.components(separatedBy: "### Line keys").dropFirst().first)
            .components(separatedBy: "\n## ").first ?? ""
        let row = try NSRegularExpression(pattern: #"^\| `([a-z_]+)` \|"#, options: .anchorsMatchLines)
        return Set(row.matches(in: section, range: NSRange(section.startIndex..., in: section)).map {
            String(section[Range($0.range(at: 1), in: section)!])
        })
    }

    /// Every argument a key may take, so each key's fullest sentence is said.
    private func args(_ key: String) -> [String: String] {
        let at = "2026-11-17T09:00:00Z"
        return ["error": "Desk Mac is offline", "run": "Widget", "text": "Which ryokan?", "recommend": "Ryokan Hanami",
                "default_at": at, "reason": "A reviewer to merge", "until": at, "resets_at": at, "on": "agent", "at": at,
                "by": "auto"]
    }

    @Test func unknownLineAndArgumentsSurviveAnOlderReader() throws {
        let bytes = Data(#"{"key":"future_line","args":{"future_time":"one day"}}"#.utf8)
        let line = try JSONDecoder().decode(TodoLine.self, from: bytes)
        #expect(line.args?["future_time"] == "one day")
        #expect(TodoLineText.said(line, now: now) == nil)
        #expect(try JSONDecoder().decode(TodoLine.self, from: JSONEncoder().encode(line)) == line)
    }

    @Test func everyReadmeKeyIsSaid() throws {
        let keys = try Self.readmeKeys()
        #expect(keys.count > 20, "read the README's table")
        #expect(keys == Set(TodoLineText.keys.map(\.rawValue)), "the README and the app say the same keys")
    }

    @Test(arguments: ["en", "zh-Hans", "ja"]) func inEveryLanguage(_ language: String) throws {
        let words = TodoWords(locale: Locale(identifier: language))
        for line in TodoLineText.keys.sorted(by: { $0.rawValue < $1.rawValue }) {
            let key = line.rawValue
            let said = try #require(TodoLineText.said(TodoLine(key: key, args: args(key)), since: now.addingTimeInterval(-86_400 * 2),
                                                      now: now, words: words, calendar: calendar), "\(key)")
            let text = said.spoken(separator: words.listSeparator)
            #expect(!text.isEmpty, "\(key)")
            #expect(text != key && (!text.contains(key) || !key.contains("_")), "\(key) said as its raw key: \(text)")
            #expect(!text.contains("["), "\(key) kept its catalog context: \(text)")
            #expect(!text.contains("2026-11-17"), "\(key) said a time as written: \(text)")
            // The hub's own words (its error, a reason) stay as sent; ours are in the language.
            let ours = [said.lead, said.rest].compactMap { $0 }.filter { !args(key).values.contains($0) && $0 != "Persona" && $0 != "Widget" }
            if language != "en", let first = ours.first {
                #expect(first.unicodeScalars.contains { $0.value >= 0x3040 }, "\(key) in \(language): \(first)")
            }
        }
    }

    @Test func linesHaveTheirTones() {
        let say = { (key: String, args: [String: String]) in
            TodoLineText.said(TodoLine(key: key, args: args), now: self.now, words: TodoWords(locale: Locale(identifier: "en")),
                              calendar: self.calendar)
        }
        let decision = say("decision", ["text": "Which ryokan?", "recommend": "Ryokan Hanami"])
        #expect(decision == TodoLineText.Said(lead: "Which ryokan?", rest: "Ryokan Hanami recommended", tone: .needs))
        #expect(say("failed_start", ["error": "Desk Mac is offline"])?.tone == .danger)
        #expect(say("finished", [:])?.tone == .ready)
        #expect(say("stopped_unfinished", ["run": "Widget", "text": "the migration test"])
                    == TodoLineText.Said(lead: "Widget stopped before it finished", rest: "the migration test", tone: .needs),
                "said as an interrupted run, then what its closeout says")
        #expect(say("stopped_unfinished", ["run": "Widget"]) == say("interrupted", ["run": "Widget"]))
        #expect(say("persona_working", ["text": "writing part 2 of 3"]) == TodoLineText.Said(lead: "Persona", rest: "writing part 2 of 3"))
        #expect(say("decision", ["text": "Which of 3?", "step": "Ryokan"])?.lead == "Ryokan" + TodoTimeWords.separator + "Which of 3?",
                "a step's line names it")
        #expect(TodoTimeWords.separator == "\u{00A0}· ", "the dot stays with the word before it when a line wraps")
        #expect(say("no_such_key", [:]) == nil, "a key this build does not know is not said")
        #expect(TodoLineText.said(nil, now: now) == nil)
    }

    /// Persona's own pick says when: "on" a day, "at" a time today (proposal 3.4: "… Hanami on Fri unless you answer").
    @Test func aDefaultSaysOnADayAndAtATime() {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let say = { (at: Date) in
            TodoLineText.said(TodoLine(key: "decision_default", args: ["recommend": "Ryokan Hanami", "default_at": at.formatted(.iso8601)]),
                              now: self.now, words: TodoWords(locale: Locale(identifier: "en_US")), calendar: utc)?.rest ?? ""
        }
        let later = say(now.addingTimeInterval(86_400 * 2))
        #expect(later.hasPrefix("Persona picks Ryokan Hanami on ") && later.hasSuffix(" unless you answer"), "\(later)")
        let today = say(now.addingTimeInterval(3600))
        #expect(today.hasPrefix("Persona picks Ryokan Hanami at ") && today.contains(":"), "\(today)")
    }

    /// Absolute times become words relative to now, in the model, never in a body: a line's today by the clock, the week
    /// by its day, further by its date; the row's end says what its time is ("Due Fri", "16m ago"), an age never a day.
    @Test func timesAreWords() {
        let words = TodoWords(locale: Locale(identifier: "en_US"))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let hour = TodoTimeWords.day(now.addingTimeInterval(-60), now: now, words: words, calendar: utc)
        #expect(hour.contains(":"))
        let weekday = TodoTimeWords.day(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc)
        #expect(weekday.count <= 4 && !weekday.contains(":"))
        #expect(TodoTimeWords.age(now.addingTimeInterval(-16 * 60), now: now, words: words, calendar: utc) == "16m ago")
        #expect(TodoTimeWords.age(now.addingTimeInterval(-3 * 3600), now: now, words: words, calendar: utc) == "3h ago")
        #expect(TodoTimeWords.age(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc) == "2d ago")
        #expect(TodoTimeWords.age(now.addingTimeInterval(-86_400 * 40), now: now, words: words, calendar: utc) == "40d ago")
        #expect(TodoTimeWords.due(now, now: now, words: words, calendar: utc) == "Due today")
        let friday = TodoTimeWords.due(now.addingTimeInterval(86_400 * 3), now: now, words: words, calendar: utc)
        #expect(friday.hasPrefix("Due ") && friday.count <= 7, "\(friday)")
        #expect(TodoTimeWords.due(now.addingTimeInterval(86_400 * 30), now: now, words: words, calendar: utc).hasPrefix("Due "))
        #expect(TodoTimeWords.spokenAge(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc) == "2 days ago")
    }

    /// The row's time words in every language: none keeps its catalog context, Chinese and Japanese say them in their
    /// own script.
    @Test(arguments: ["en", "zh-Hans", "ja"]) func theRowsTimesInEveryLanguage(_ language: String) {
        let words = TodoWords(locale: Locale(identifier: language))
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let said = [TodoTimeWords.due(now, now: now, words: words, calendar: utc),
                    TodoTimeWords.due(now.addingTimeInterval(86_400 * 3), now: now, words: words, calendar: utc),
                    TodoTimeWords.age(now.addingTimeInterval(-16 * 60), now: now, words: words, calendar: utc),
                    TodoTimeWords.age(now.addingTimeInterval(-3 * 3600), now: now, words: words, calendar: utc),
                    TodoTimeWords.age(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc),
                    TodoTimeWords.spokenAge(now.addingTimeInterval(-86_400 * 2), now: now, words: words, calendar: utc)]
        for text in said {
            #expect(!text.contains("["), "\(language): \(text)")
            if language != "en" { #expect(text.unicodeScalars.contains { $0.value >= 0x3040 }, "\(language): \(text)") }
        }
        // A due day at a row's end never reads as a parked row's "until …" on line 2, a day's name or a date.
        for day in [now.addingTimeInterval(86_400 * 3), now.addingTimeInterval(86_400 * 30)] {
            let until = words("until \(TodoTimeWords.day(day, now: now, words: words, calendar: utc)) [To-dos Line]")
            #expect(TodoTimeWords.due(day, now: now, words: words, calendar: utc) != until, "\(language): \(until)")
        }
    }
}

/// A row's content (#10465): a line this build cannot say still draws the section's mark and word; VoiceOver hears the
/// state, the line (with how many asks) and the time in full words; the row's end says no time a line already says.
struct TodoRowContentTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let words = TodoWords(locale: Locale(identifier: "en_US"))

    private func filed(_ id: String, _ section: TodoListSection) -> TodoFiled {
        TodoFiled(id: id, manager: nil, section: section, closed: false, rank: 0, inactive: false, byView: true, quiet: false,
                  partOf: nil, project: nil, asks: [])
    }

    private func view(_ section: TodoListSection, key: String, asks: [OwnerAsk] = []) -> TodoSummary {
        TodoSummary(phase: TodoPhase(rawValue: section.rawValue), section: section, reason: TodoReason.agentRunning,
                    line: TodoLine(key: key), rank: 0, asks: asks)
    }

    @Test(arguments: [TodoListSection.needsYou, TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished])
    func aLineThisBuildCannotSayKeepsTheMark(_ section: TodoListSection) {
        let c = TodoRowContentLogic.content(filed("a", section), item: TodoItem(id: "a", title: "A"),
                                            view: view(section, key: "computer_offline"), now: now, words: words)
        #expect(c.mark != nil, "\(section): the mark is drawn on line 2")
        #expect(c.said?.lead != nil, "\(section): the section's word in place of the line")
    }

    @MainActor @Test func theRowIsSpokenInFullWords() {
        let item = TodoItem(id: "a", title: "Plan the Kyoto trip", updatedAt: now.addingTimeInterval(-16 * 60))
        let asks = [OwnerAsk(id: "todo:decision@1", kind: "plan", home: "todo"), OwnerAsk(id: "todo:decision@2", kind: "decision", home: "todo")]
        let c = TodoRowContentLogic.content(filed("a", TodoListSection.needsYou), item: item,
                                            view: view(TodoListSection.needsYou, key: "decision", asks: asks), now: now, words: words)
        #expect(c.trailing == "16m ago")
        let drawn = c.said.map { String(TodoListRow.attributed($0).characters) }
        #expect(drawn?.hasSuffix(TodoTimeWords.separator + "2 asks") == true, "line 2 ends with how many asks: \(String(describing: drawn))")
        #expect(c.spoken.hasPrefix("Plan the Kyoto trip, "), "\(c.spoken)")
        #expect(c.spoken.contains(StateName.todoNeedsYou), "the state its mark draws: \(c.spoken)")
        #expect(!c.spoken.contains("Review") && c.spoken.contains("2 asks"), "no door to speak: \(c.spoken)")
        #expect(c.spoken.hasSuffix("16 minutes ago"), "the time in words, never \"16m\": \(c.spoken)")
    }

    /// A high-priority to-do: "!" before its title in the caution tone, "High priority" said after the title; none once
    /// it is closed, nor on normal and low ones.
    @MainActor @Test func aHighPriorityRowIsMarked() {
        let high = TodoItem(id: "a", title: "Fix the login", priority: "high")
        let c = TodoRowContentLogic.content(filed("a", TodoListSection.todo), item: high, view: nil, now: now, words: words)
        #expect(c.highPriority && c.spoken.hasPrefix("Fix the login, High priority"), "\(c.spoken)")
        #expect(String(TodoListRow.title(c).characters) == "!\u{00A0}Fix the login")
        let low = TodoItem(id: "a", title: "Fix the login", priority: "low")
        #expect(!TodoRowContentLogic.content(filed("a", TodoListSection.todo), item: low, view: nil, now: now, words: words).highPriority)
        var closed = filed("a", TodoListSection.done)
        closed.closed = true
        let done = TodoRowContentLogic.content(closed, item: high, view: nil, now: now, words: words)
        #expect(!done.highPriority && String(TodoListRow.title(done).characters) == "Fix the login")
    }

    /// A line that says a time ("since Tue") leaves the row's end without one; a due day stays; a closed row says how long
    /// ago it closed.
    @Test func theRowsEndSaysNoTimeTwice() {
        let item = TodoItem(id: "a", title: "Fix the login", updatedAt: now.addingTimeInterval(-3600))
        var waiting = view(TodoListSection.waiting, key: "pr_open")
        waiting.since = now.addingTimeInterval(-86_400 * 2).formatted(.iso8601)
        let c = TodoRowContentLogic.content(filed("a", TodoListSection.waiting), item: item, view: waiting, now: now, words: words)
        #expect(c.said?.timed == true && c.trailing == nil, "\(String(describing: c.trailing))")
        #expect(!c.spoken.contains("ago"), "\(c.spoken)")
        let due = TodoItem(id: "a", title: "Fix the login", dueAt: now.addingTimeInterval(86_400 * 2))
        let dated = TodoRowContentLogic.content(filed("a", TodoListSection.waiting), item: due, view: waiting, now: now, words: words)
        #expect(dated.trailing?.hasPrefix("Due ") == true, "a due day is not what the line says")
        var closed = filed("d", TodoListSection.done)
        closed.closed = true
        let done = TodoRowContentLogic.content(closed, item: TodoItem(id: "d", title: "Booked", doneAt: now.addingTimeInterval(-7200)),
                                               view: nil, now: now, words: words)
        #expect(done.trailing == "2h ago" && done.spoken.hasSuffix("Done 2 hours ago"), "\(done.spoken)")
    }
}
