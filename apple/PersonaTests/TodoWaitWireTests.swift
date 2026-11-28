import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Shared wire fixtures and presentation tests for server-emitted `TodoWait` (#16168).
@MainActor struct TodoWaitWireTests {
    private static let fixtureURL = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "spec/fixtures/todo-wait/todo_wait.json")

    private let now = Date(timeIntervalSince1970: 1_795_000_000) // 2026-11-18T11:06:40Z

    @Test func generatedTypeRoundTripsAllSharedFixtures() throws {
        let data = try Data(contentsOf: Self.fixtureURL)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        let cases = root["cases"] as? [[String: Any]] ?? []
        #expect(!cases.isEmpty)

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        var seenKinds: Set<TodoWaitKind> = []
        var seenBlockers: Set<TodoWaitPRBlocker> = []

        for c in cases {
            let name = c["name"] as? String ?? ""
            let expected = c["expected"] as? [String: Any] ?? [:]
            guard let sample = expected["wait"] as? [String: Any] else { continue }
            let raw = try JSONSerialization.data(withJSONObject: sample, options: [.sortedKeys, .withoutEscapingSlashes])
            let decoded = try JSONDecoder().decode(TodoWait.self, from: raw)
            if case .unknown = decoded.kind {
                Issue.record("\(name): unexpected unknown kind \(decoded.kind.rawValue)")
            }
            seenKinds.insert(decoded.kind)
            for b in decoded.prBlockers ?? [] {
                seenBlockers.insert(b)
            }
            let written = try encoder.encode(decoded)
            #expect(written == raw, "\(name)")
        }

        #expect(seenKinds == Set(TodoWaitKind.all))
        #expect(seenBlockers == Set(TodoWaitPRBlocker.all))
    }

    @Test func unknownKindAndBlockerPreserveRawAndReportDefect() throws {
        let raw = Data(#"{"kind":"future_wait","pr_blockers":["future_blocker"],"waiting_on":"Upstream"}"#.utf8)
        let wait = try JSONDecoder().decode(TodoWait.self, from: raw)
        #expect(wait.kind == .unknown("future_wait"))
        #expect(wait.prBlockers == [.unknown("future_blocker")])
        #expect(wait.waitingOn == "Upstream")

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        #expect(try encoder.encode(wait) == raw)
    }

    @Test func todoItemDecodesValidWaitAndDropsMalformedWaitWithoutLosingRow() throws {
        let validJSON = Data(#"""
        {"id":"t1","title":"Confirm venue","wait":{"kind":"person_reply","waiting_on":"Alex","reason":"Waiting for room quote","until":"2026-11-20T18:00:00Z"}}
        """#.utf8)
        let valid = try JSON.decoder.decode(TodoItem.self, from: validJSON)
        #expect(valid.wait?.kind == .personReply)
        #expect(valid.wait?.waitingOn == "Alex")
        #expect(valid.wait?.reason == "Waiting for room quote")
        #expect(valid.statusLabel(.notStarted) == StateName.taskWaiting.text)
        #expect(valid.shownState(.notStarted) == .waiting)

        // Strict TodoWait decoding rejects missing required `kind`; TodoItem drops it to nil rather than failing the row.
        let malformedJSON = Data(#"{"id":"t2","title":"Broken wait","wait":{}}"#.utf8)
        let malformed = try JSON.decoder.decode(TodoItem.self, from: malformedJSON)
        #expect(malformed.id == "t2" && malformed.wait == nil)

        // Older hubs omit `wait`; clients do not derive it from raw supervision.
        let legacyJSON = Data(#"{"id":"t3","title":"Legacy row","supervision":{"state":"parked"}}"#.utf8)
        let legacy = try JSON.decoder.decode(TodoItem.self, from: legacyJSON)
        #expect(legacy.wait == nil)
    }

    @Test(arguments: ["en", "zh-Hans", "ja"]) func allKindsAndBlockersLocalizeCleanly(_ language: String) {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let words = TodoWaitWords(locale: Locale(identifier: language))

        for kind in TodoWaitKind.all {
            let label = TodoWaitText.kindLabel(kind, words: words)
            #expect(!label.isEmpty && !label.contains("["), "\(language) \(kind.rawValue): \(label)")
            let sample = TodoWait(
                kind: kind,
                waitingOn: "Alex",
                reason: "Confirm launch slot",
                wakeCondition: "Alex replies",
                wakeAt: kind == .date || kind == .usageLimit ? "2026-11-18T15:00:00Z" : nil,
                until: "2026-11-20T18:00:00Z",
                since: "2026-11-18T09:00:00Z",
                prBlockers: kind == .prMerge ? TodoWaitPRBlocker.all : nil
            )
            let row = TodoWaitText.rowSummary(sample, now: now, words: words, calendar: utc)
            #expect(!row.lead.isEmpty && !row.lead.contains("["), "\(language) \(kind.rawValue) lead: \(row.lead)")
            if let timing = row.timing {
                #expect(!timing.contains("["), "\(language) \(kind.rawValue) timing: \(timing)")
            }
            let detail = TodoWaitText.detail(sample, now: now, words: words, calendar: utc)
            #expect(!detail.kindTitle.contains("["), "\(language) \(kind.rawValue) detail kind")
            if language != "en" {
                #expect(label.unicodeScalars.contains { $0.value >= 0x3040 }, "\(language) \(kind.rawValue): \(label)")
            }
        }

        for blocker in TodoWaitPRBlocker.all {
            let text = TodoWaitText.blockerLabel(blocker, words: words) ?? ""
            #expect(!text.isEmpty && !text.contains("["), "\(language) \(blocker.rawValue): \(text)")
            if language != "en" {
                #expect(text.unicodeScalars.contains { $0.value >= 0x3040 }, "\(language) \(blocker.rawValue): \(text)")
            }
        }
        #expect(TodoWaitText.blockerLabel(.unknown("future"), words: words) == nil)
    }

    @Test func detailDeduplicatesEqualWakeAtAndUntilAndSourceContactURL() {
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        let words = TodoWaitWords(locale: Locale(identifier: "en_US"))

        let dateWait = TodoWait(
            kind: .date,
            waitingOn: "Launch day",
            reason: "Publish changelog",
            wakeAt: "2026-11-25T09:00:00Z",
            until: "2026-11-25T09:00:00Z"
        )
        let dateDetail = TodoWaitText.detail(dateWait, now: now, words: words, calendar: utc)
        #expect(dateDetail.wakeAtText != nil)
        #expect(dateDetail.untilText == nil, "identical wake_at and until must not render twice")

        let replyWait = TodoWait(
            kind: .personReply,
            waitingOn: "Alex",
            reason: "Confirm booking",
            until: "2026-11-20T18:00:00Z",
            url: "https://mail.example.com/threads/1",
            contact: TodoWaitContact(channel: "email", sender: "Persona", sentAt: "2026-11-18T09:00:00Z",
                                     url: "https://mail.example.com/threads/1", request: "Asked about room")
        )
        let replyDetail = TodoWaitText.detail(replyWait, now: now, words: words, calendar: utc)
        #expect(replyDetail.contact?.url?.absoluteString == "https://mail.example.com/threads/1")
        #expect(replyDetail.sourceURL == nil, "duplicate contact URL should not render a second source link")
    }
}
