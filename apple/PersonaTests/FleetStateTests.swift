import Foundation
import Testing
@testable import Persona
import PersonaWire

struct FleetStateTests {
    /// Exactly what hub/liveactivity_test.go pins as the FleetState JSON, read the way ActivityKit reads a push:
    /// a plain JSONDecoder (no date strategy), so times must be plain numbers.
    @Test func decodesTheHubsJSONWithAPlainDecoder() throws {
        let json = #"{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"api","place":"","kind":"working","detail":"","since":1761700000}],"workingSince":1761700000,"details":false}"#
        let s = try JSONDecoder().decode(FleetState.self, from: Data(json.utf8))
        #expect(s.phase == .working)
        #expect(s.working == 1)
        #expect(s.items.first?.kind == .working)
        #expect(s.workingSinceDate == Date(timeIntervalSince1970: 1761700000))
        #expect(s.items.first?.key?.machine == "m")
        #expect(s.items.first?.key?.session == "s")
    }

    @Test func decodesTheExactInteractionOccurrence() throws {
        let occurrence = "2026-01-02T03:04:05.000000001Z"
        let item = try JSONDecoder().decode(FleetState.Item.self, from: Data(
            #"{"id":"m/s","kind":"permission","interaction":"ix","interactionCreatedAt":"2026-01-02T03:04:05.000000001Z"}"#.utf8))
        #expect(item.interactionCreatedAt == occurrence)
        #expect(try JSONDecoder().decode(FleetState.Item.self, from: JSONEncoder().encode(item)).interactionCreatedAt == occurrence)
        let legacy = try JSONDecoder().decode(FleetState.Item.self, from: Data(
            #"{"id":"m/s","kind":"permission","interaction":"ix"}"#.utf8))
        #expect(legacy.interactionCreatedAt == nil)
    }

    /// A newer hub must never kill the activity: unknown phases / kinds and missing fields decode to something.
    @Test func lenientDecoding() throws {
        let s = try JSONDecoder().decode(FleetState.self, from: Data(#"{"phase":"later","items":[{"id":"a/b","kind":"teleport"}]}"#.utf8))
        #expect(s.phase == .none)
        #expect(s.isEmpty)
        #expect(s.items.first?.kind == .screen)
        let empty = try JSONDecoder().decode(FleetState.self, from: Data("{}".utf8))
        #expect(empty == FleetState())
    }

    /// A background workflow's item exactly as hub/liveactivity_test.go TestFleetBackgroundItemJSON pins it (#3437): it
    /// says 「后台」 where the place goes, its line is the workflow, and the bar has the line's counts.
    @Test func decodesABackgroundItem() throws {
        let json = #"{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"audit","place":"","kind":"working","detail":"parity-audit · Verify · 169/185","background":true,"progress":{"done":169,"started":185},"since":1761700000}],"workingSince":1761700000,"details":false}"#
        let s = try JSONDecoder().decode(FleetState.self, from: Data(json.utf8))
        let item = try #require(s.items.first)
        #expect(item.background && item.kind == .working && !item.needsYou)
        #expect(item.progress == .init(done: 169, started: 185))
        #expect(item.progress?.fraction == 169.0 / 185.0)
        #expect(item.sinceDate == Date(timeIntervalSince1970: 1761700000))
        #expect(FleetText.line(item) == "parity-audit · Verify · 169/185")
        #expect(FleetText.background == "后台")
    }

    /// An older hub sends neither field (a plain working row as before); a bar in a shape this app cannot read drops
    /// only the bar, never the activity.
    @Test func backgroundFieldsAreOptional() throws {
        let old = try JSONDecoder().decode(FleetState.Item.self, from: Data(#"{"id":"m/s","kind":"working","detail":"x"}"#.utf8))
        #expect(!old.background && old.progress == nil)
        #expect(FleetText.line(old) == "工作中")
        let odd = try JSONDecoder().decode(FleetState.Item.self,
                                           from: Data(#"{"id":"m/s","kind":"working","background":"yes","progress":[1,2],"detail":"d"}"#.utf8))
        #expect(odd.id == "m/s" && !odd.background && odd.progress == nil && odd.detail == "d")
        // No agent started yet: nothing to fill, no bar. Done past started (a racing count) fills it, no more.
        #expect(FleetState.Item.Progress(done: 0, started: 0).fraction == nil)
        #expect(FleetState.Item.Progress(done: 9, started: 4).fraction == 1)
        // Background without a line (an older hub's): a working row as before.
        let bare = FleetState.Item(id: "m/s", title: "t", kind: .working, background: true)
        #expect(FleetText.line(bare) == "工作中")
    }

    /// The expanded island keeps its "+2" (as the lock screen does) and packs its rows to make room for it when two rows
    /// of two lines (background work, requests) come first: at the usual gap the "+2" was cut in half (#3437).
    @Test func islandPacksTwoTallRowsToKeepTheMoreLine() {
        let audit = FleetState.Item(id: "a/1", title: "audit", kind: .working, detail: "parity-audit · 1/2", background: true)
        let sweep = FleetState.Item(id: "a/2", title: "sweep", kind: .working, detail: "docs-sweep · 3/8", background: true)
        let plain = FleetState.Item(id: "a/3", title: "plain", kind: .working)
        let ask = FleetState.Item(id: "a/4", title: "ask", kind: .question)
        #expect(audit.runsInBackground && audit.twoLines && !plain.twoLines && ask.twoLines)
        let tall = FleetState(phase: .working, working: 3, unread: 1, items: [audit, sweep, plain])
        #expect(tall.islandPacked(showing: 2) && tall.hidden(showing: 2) == 2) // the background sample: "+2", packed
        #expect(FleetState(phase: .needsYou, needsYou: 2, working: 1, items: [ask, ask, plain]).islandPacked(showing: 2))
        #expect(!FleetState(phase: .working, working: 4, items: [audit, plain, sweep]).islandPacked(showing: 2)) // one tall row
        #expect(!FleetState(phase: .working, working: 2, items: [audit, sweep]).islandPacked(showing: 2)) // no "+2" to fit
        // An older hub's background item has no line: a one-line working row.
        let bare = FleetState.Item(id: "a/5", title: "bare", kind: .working, background: true)
        #expect(!bare.runsInBackground && !bare.twoLines)
    }

    @Test func roundTripsThroughItsOwnEncoding() throws {
        let s = FleetState(phase: .needsYou, needsYou: 1, items: [.init(id: "m/s", title: "t", kind: .plan, detail: "d", interaction: "ix"),
                                                                  .init(id: "m/w", title: "w", kind: .working, detail: "run · 1/2", since: 1761700000,
                                                                        background: true, progress: .init(done: 1, started: 2))], details: true)
        #expect(try JSONDecoder().decode(FleetState.self, from: JSONEncoder().encode(s)) == s)
    }

    @Test func hiddenAndActionable() {
        let s = FleetState(phase: .needsYou, needsYou: 2, working: 3, unread: 1, items: [
            .init(id: "a/1", title: "q", kind: .question, interaction: "x"),
            .init(id: "a/2", title: "p", kind: .permission, interaction: "y"),
            .init(id: "a/3", title: "w", kind: .working),
        ])
        #expect(s.hidden(showing: 2) == 4)
        #expect(s.hidden(showing: 10) == 3)
        #expect(s.firstActionable?.id == "a/2") // questions need the screen
    }

    @Test func wording() {
        #expect(FleetText.headline(FleetState(phase: .needsYou, needsYou: 2)) == "2 个需要你")
        #expect(FleetText.headline(FleetState(phase: .working, working: 3)) == "3 个工作中")
        #expect(FleetText.headline(FleetState(phase: .done, unread: 1)) == "1 个完成未看")
        #expect(FleetText.line(.init(id: "a/b", title: "", kind: .permission, detail: "Bash: ls")) == "授权：Bash: ls")
        #expect(FleetText.line(.init(id: "a/b", title: "", kind: .plan)) == "计划等你批准")
        #expect(FleetText.anonymous(.init(id: "a/b", title: "secret", kind: .permission)) == "有个授权等你")
        #expect(FleetText.more(4) == "+4 个")
    }

    @Test func deepLinks() throws {
        let url = try #require(FleetLink.url(.init(id: "box/abc-123", title: "", kind: .working)))
        #expect(url.absoluteString == "persona://open?machine=box&session=abc-123")
        let back = try #require(FleetLink.session(url))
        #expect(back.machine == "box" && back.session == "abc-123")
        #expect(FleetLink.session(URL(string: "persona://open")!) == nil)
        #expect(FleetLink.session(URL(string: "persona://pair?url=x&token=y")!) == nil)
    }

    @Test func personaDeepLinksRoundTripIgnoringCase() throws {
        for scheme in ["persona", "PERSONA"] {
            let session = try #require(FleetLink.session(URL(string: "\(scheme)://open?machine=box&session=abc-123")!))
            #expect(session.machine == "box" && session.session == "abc-123")
            #expect(FleetLink.session(URL(string: "\(scheme)://open?machine=box")!) == nil)
            #expect(FleetLink.session(URL(string: "\(scheme)://pair?machine=box&session=abc-123")!) == nil)
        }
        for other in ["other-product", "personas", "personad"] {
            #expect(FleetLink.session(URL(string: "\(other)://open?machine=box&session=abc-123")!) == nil)
        }
        #expect(FleetLink.session(URL(string: "https://open?machine=box&session=abc-123")!) == nil)
        #expect(FleetLink.url(nil)?.absoluteString == "persona://open")
    }

    /// A tap on the lock screen opens what it shows (#4318): its one request, else the same as the whole activity.
    @Test func lockScreenTapOpensTheRequestItShows() {
        let permission = FleetState.Item(id: "box/a", title: "A", kind: .permission, interaction: "ix_a")
        let question = FleetState.Item(id: "box/q", title: "Q", kind: .question)
        let working = FleetState.Item(id: "box/w", title: "W", kind: .working)
        func tap(_ s: FleetState, stale: Bool = false) -> String? { FleetLink.lockScreen(s, stale: stale)?.absoluteString }

        // The request with buttons is the one row, names shown or not.
        let buttons = FleetState(phase: .needsYou, needsYou: 1, working: 1, items: [working, permission])
        #expect(tap(buttons) == "persona://open?machine=box&session=a")
        var named = buttons
        named.details = true
        #expect(tap(named) == "persona://open?machine=box&session=a")
        // Stale: no buttons, two rows drawn, so just the app.
        #expect(tap(named, stale: true) == "persona://open")
        // Names hidden: the one nameless line (a question is waiting) is the first agent's.
        let asks = FleetState(phase: .needsYou, needsYou: 1, working: 1, items: [question, working])
        #expect(tap(asks) == FleetLink.url(question)?.absoluteString)
        #expect(tap(asks, stale: true) == FleetLink.url(question)?.absoluteString)
        // Rows, or only counts: as the whole activity.
        var rows = asks
        rows.details = true
        #expect(tap(rows) == "persona://open")
        #expect(tap(FleetState(phase: .working, working: 2, items: [working, working])) == "persona://open")
        #expect(tap(FleetState(phase: .working, working: 1, items: [working])) == FleetLink.url(working)?.absoluteString)
    }

    // MARK: Island

    /// What hub forDevice pushes to a device with preset b, read with a plain decoder like ActivityKit does.
    @Test func decodesIslandFields() throws {
        let json = #"{"phase":"working","needsYou":0,"working":2,"unread":1,"items":[],"details":false,"assistant":true,"left":"status","right":"tpm","stats":{"tpm":12000,"loads":[{"id":"m","name":"M","percent":35}]}}"#
        let s = try JSONDecoder().decode(FleetState.self, from: Data(json.utf8))
        #expect(s.right == .tpm && s.assistant && s.stats?.tpm == 12000 && s.stats?.loads.first?.percent == 35)
        let odd = try JSONDecoder().decode(FleetState.self, from: Data(#"{"left":"cost","stats":{"tpm":"x"}}"#.utf8))
        #expect(odd.left == .status) // an unknown slot (newer hub) is status
    }

    @Test func statusSides() {
        // Preset a: the most urgent count on the left, the next one on the right.
        let working = FleetState(phase: .working, working: 3, unread: 1)
        #expect(IslandCell.sides(working) == (.working(3), .unread(1)))
        let needs = FleetState(phase: .needsYou, needsYou: 2, working: 3, unread: 1)
        #expect(IslandCell.sides(needs) == (.needsYou(2), .working(3)))
        let done = FleetState(phase: .done, unread: 2)
        #expect(IslandCell.sides(done) == (.unread(2), .empty))
        #expect(IslandCell.minimal(needs) == .needsYou(2))
    }

    @Test func numberSides() {
        let stats = FleetState.Stats(tpm: 12000, tokens: 42_000_000, requests: 220, loads: [.init(id: "a", name: "A", percent: 30)])
        var s = FleetState(phase: .working, working: 3, unread: 1, assistant: true, right: .tpm, stats: stats)
        #expect(IslandCell.sides(s) == (.working(3), .tpm(12000)))
        // Needs you takes the left side whatever was chosen there; a status right side then shows the next count.
        s = FleetState(phase: .needsYou, needsYou: 1, working: 3, left: .assistant, right: .status, stats: stats)
        #expect(IslandCell.sides(s) == (.needsYou(1), .working(3)))
        s.needsYou = 0
        s.phase = .working
        #expect(IslandCell.sides(s) == (.assistant(false), .working(3)))
        s.left = .load
        s.right = .tokens
        #expect(IslandCell.sides(s) == (.load(name: "A", percent: 30), .tokens(42_000_000)))
        s.right = .none
        #expect(IslandCell.sides(s).right == .empty)
    }

    @Test func tailoredLikeTheHub() {
        let stats = FleetState.Stats(tpm: 1, loads: [.init(id: "a", percent: 30), .init(id: "b", percent: 70)])
        let s = FleetState(phase: .working, working: 1, stats: stats)
        let busiest = s.tailored(island: IslandPrefs(preset: .custom, left: .status, right: .load), details: true)
        #expect(busiest.right == .load && busiest.details && busiest.stats?.loads.map(\.id) == ["b"])
        let chosen = s.tailored(island: IslandPrefs(preset: .custom, left: .status, right: .load, machine: "a"), details: false)
        #expect(chosen.stats?.loads.map(\.id) == ["a"])
        #expect(s.tailored(island: IslandPrefs(preset: .c), details: false).right == .tokens)
    }

    @Test func shortNumbers() {
        #expect(FleetText.short(980) == "980")
        #expect(FleetText.short(1000) == "1k")
        #expect(FleetText.short(1200) == "1.2k")
        #expect(FleetText.short(12000) == "12k")
        #expect(FleetText.short(4_200_000) == "4.2M")
        #expect(FleetText.short(42_000_000) == "42M")
        #expect(FleetText.short(999_499) == "999k")
        #expect(FleetText.short(999_500) == "1M")
        #expect(FleetText.short(999_999) == "1M")
        #expect(FleetText.short(999_499_999) == "999M")
        #expect(FleetText.short(999_600_000) == "1B")
        #expect(FleetText.stat(.tpm(12000)) == "12k/分")
        #expect(FleetText.stat(.tokens(42_000_000)) == "今日 42M")
    }

    /// The island's load is load per core, in Machines' words (#12519): the hub's percent (load ÷ cores × 100) read as a
    /// CPU percentage, and past 100 it looked broken. On the main actor for the compact island's `IslandPair`.
    @Test @MainActor func loadReadsPerCore() throws {
        #expect(FleetText.perCore(percent: 110) == "1.1")
        #expect(FleetText.perCore(percent: 0) == "0.0")
        #expect(FleetText.perCore(percent: 650) == "6.5") // not capped: threads wait for a core
        // An older hub's steps of 5 round one way, not by how a Double stores 1.35 or 1.45.
        #expect(FleetText.perCore(percent: 135) == "1.4")
        #expect(FleetText.perCore(percent: 145) == "1.5")
        #expect(FleetText.stat(.load(name: "", percent: 110)) == "负载 每核 1.1")
        #expect(FleetText.stat(.load(name: "Studio", percent: 110)) == "Studio 每核 1.1")
        let pair = try #require(IslandPair(.load(name: "", percent: 110)))
        #expect(pair.text == "1.1")
        // VoiceOver reads the compact island's "1.1" beside the CPU symbol as its words, not "CPU, 1.1".
        #expect(pair.spoken == "负载 每核 1.1")
        let count = try #require(IslandPair(.working(3)))
        #expect(count.spoken == nil) // a count reads as drawn
        // The island draws the figure Machines draws for the same raw load (MetricFormat.perCore): the hub rounds load
        // per core to tenths (fleet_live.go loadPercent). Exact ties (1.45) are left out: a Double holds them just
        // below or above, and a real load average is never one.
        for hundredths in 0...400 where hundredths % 10 != 5 {
            let load = Double(hundredths) / 100
            let percent = Int((load * 10).rounded()) * 10
            #expect(FleetText.perCore(percent: percent) == MetricFormat.perCore(load), "\(load) per core")
        }
    }

    /// The app sends every island field (empty when unset): the hub merges, so a left-out field would keep its old value.
    @Test func islandPrefsCoding() throws {
        let json = String(decoding: try JSONEncoder().encode(IslandPrefs(preset: .b)), as: UTF8.self)
        #expect(json.contains(#""machine":"""#) && json.contains(#""left":"""#) && json.contains(#""preset":"b""#))
        let p = try JSONDecoder().decode(NotifyPrefs.self, from: Data(#"{"input":true,"done":"long","island":{"preset":"custom","left":"assistant","right":"load","machine":""}}"#.utf8))
        #expect(p.island.sides == (.assistant, .load) && p.island.machine == nil)
        #expect(try JSONDecoder().decode(NotifyPrefs.self, from: Data("{}".utf8)).island.preset == .a)
    }

    /// #12982: without names on the Lock Screen a request has no Allow / Deny (as the hub's forDevice): nobody can tell
    /// who asks or what approving does. With names the buttons stay.
    @Test func noButtonsWithoutDetails() {
        let ask = FleetState.Item(id: "m/s", title: "Infra lead", kind: .permission, detail: "Mark the to-do done",
                                  interaction: "ix_1", interactionCreatedAt: "2026-01-02T03:04:05.000000001Z")
        let s = FleetState(phase: .needsYou, needsYou: 1, items: [ask])
        let hidden = s.tailored(island: IslandPrefs(), details: false)
        #expect(hidden.firstActionable == nil)
        #expect(hidden.items.first?.interactionCreatedAt == nil)
        #expect(hidden.needsYou == 1 && hidden.items.count == 1)
        #expect(FleetLink.lockScreen(hidden, stale: false) == FleetLink.url(ask)) // a tap opens the request in the app
        #expect(s.tailored(island: IslandPrefs(), details: true).firstActionable?.interaction == "ix_1")
        #expect(s.tailored(island: IslandPrefs(), details: true).firstActionable?.interactionCreatedAt == ask.interactionCreatedAt)
    }
}
