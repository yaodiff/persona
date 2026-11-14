import Foundation
import os
import Testing
@testable import Persona
import PersonaWire

@MainActor struct RecordingTranscriptionTests {
    @Test func uploadCarriesTextAndTimestampsOnly() throws {
        var entry = RecordingEntry(id: UUID(), createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .watch,
                                   title: "现场记录", active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30,
            sentences: [RecordingSentence(start: 12, end: 16, text: "下周检查进度。"), RecordingSentence(start: 20, end: 21, text: "  ")])]
        entry.marks = [12]
        let body = RecordingUpload(entry)
        #expect(body.complete)
        #expect(body.device == "watch")
        #expect(body.segments.map(\.text) == ["下周检查进度。"]) // blank text dropped
        #expect(body.bookmarks.map(\.at) == [12])
        #expect(body.endedAt == entry.createdAt.addingTimeInterval(30))
        let json = String(decoding: try JSON.encoder.encode(body), as: UTF8.self)
        #expect(json.contains("\"started_at\""))
        #expect(!json.contains(".m4a"))
        #expect(!json.contains(entry.id.uuidString))
    }

    @Test func incompleteOrSilentRecordingIsNotUploadable() {
        var entry = RecordingEntry(id: UUID(), createdAt: .now, source: .phone, active: false, expectedSegments: 2)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [])]
        #expect(!RecordingSync.uploadable(entry))
        #expect(!RecordingUpload(entry).complete)
        entry.expectedSegments = 1
        #expect(entry.transcribedAll)
        #expect(!RecordingSync.uploadable(entry))
    }

    @Test func sentencesSplitOnPunctuationAndPause() {
        let raw = RecordingSentenceBuilder.build([
            (text: "第一句。", start: 0, end: 2), (text: "第二", start: 3, end: 4),
            (text: "句话", start: 4, end: 5), (text: "接下来", start: 8, end: 10),
        ])
        #expect(raw.map(\.text) == ["第一句。", "第二句话", "接下来"])
        #expect(raw.map(\.start) == [0, 3, 8])
    }

    @Test func resumesOnlyUnfinishedSegments() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var entry = try lib.create(source: .phone)
        entry.active = false; entry.expectedSegments = 2
        entry.segments = [
            RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 0, end: 3, text: "已保存")]),
            RecordingSegment(index: 1, start: 30, duration: 30),
        ]
        try lib.save(entry)
        try Data([0]).write(to: lib.audio(entry.id, 1))
        let runner = RecordingTranscription(library: lib) { url, _ in
            #expect(url.lastPathComponent == "seg-001.m4a")
            return [RecordingSentence(start: 2, end: 4, text: "继续")]
        }
        runner.enqueue(entry.id)
        for _ in 0..<100 where lib.entry(entry.id)?.transcribedAll != true { try await Task.sleep(for: .milliseconds(10)) }
        #expect(lib.entry(entry.id)?.sentences.map(\.text) == ["已保存", "继续"])
        #expect(lib.entry(entry.id)?.sentences.last?.start == 32)
        #expect(RecordingLibrary(root: root).entry(entry.id)?.transcribedAll == true)
    }

    /// A segment whose audio is gone gets no text, and the ones after it are still transcribed (a long recording used to
    /// stop at it for good).
    @Test func missingAudioDoesNotHoldUpTheRest() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var entry = try lib.create(source: .phone)
        entry.active = false; entry.expectedSegments = 3
        entry.segments = (0..<3).map { RecordingSegment(index: $0, start: Double($0) * 60, duration: 60) }
        try lib.save(entry)
        try Data([0]).write(to: lib.audio(entry.id, 0))
        try Data([0]).write(to: lib.audio(entry.id, 2)) // segment 1's audio is missing
        let runner = RecordingTranscription(library: lib) { url, _ in [RecordingSentence(start: 1, end: 2, text: url.lastPathComponent)] }
        runner.enqueue(entry.id)
        for _ in 0..<100 where lib.entry(entry.id)?.transcribedAll != true { try await Task.sleep(for: .milliseconds(10)) }
        #expect(lib.entry(entry.id)?.transcribedAll == true)
        #expect(lib.entry(entry.id)?.sentences.map(\.text) == ["seg-000.m4a", "seg-002.m4a"])
        #expect(runner.errors[entry.id] == nil)
    }

    /// All-day recording's lane waits for the clips (`whenIdle`), also inside the catch-up task: when that task's time
    /// runs out, its wait must end at once, however long a clip's transcription (or model download) still takes.
    @Test func aCancelledWaitForTheClipsEndsWhileOneIsStillTranscribing() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var entry = try lib.create(source: .phone)
        entry.active = false; entry.expectedSegments = 1
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30)]
        try lib.save(entry)
        try Data([0]).write(to: lib.audio(entry.id, 0))
        // A transcription that takes as long as the test says, whatever cancellation asks of it.
        let held = OSAllocatedUnfairLock(initialState: true)
        let runner = RecordingTranscription(library: lib) { _, _ in
            while held.withLock({ $0 }) { try? await Task.sleep(for: .milliseconds(2)) }
            return [RecordingSentence(start: 0, end: 1, text: "done")]
        }
        runner.enqueue(entry.id)
        for _ in 0..<500 where runner.current != entry.id { try await Task.sleep(for: .milliseconds(2)) }
        #expect(runner.current == entry.id)
        // So a broken fix fails rather than hangs.
        let safety = Task { try? await Task.sleep(for: .seconds(10)); held.withLock { $0 = false } }
        defer { safety.cancel() }

        let waiter = Task { await runner.whenIdle() }
        for _ in 0..<20 { await Task.yield() }
        waiter.cancel()
        await waiter.value
        #expect(runner.current == entry.id, "returned while the clip was still being transcribed")

        // Uncancelled, it still waits for the end.
        held.withLock { $0 = false }
        await runner.whenIdle()
        #expect(runner.current == nil && lib.entry(entry.id)?.transcribedAll == true)
    }

    @Test func pullTranscribesOnlyItsRangeAndAFailureCanBeRetried() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let entry = try lib.create(source: .watch, kind: .rolling)
        let base = Date(timeIntervalSince1970: 1_800_000_000)
        for i in 0..<4 {
            lib.addSegment(entry.id, RecordingSegment(index: i, start: Double(i) * 60, duration: 60,
                                                      startedAt: base.addingTimeInterval(Double(i) * 60)))
            try Data([0]).write(to: lib.audio(entry.id, i))
        }
        // Segments 1 and 2 only.
        let range = base.addingTimeInterval(70)...base.addingTimeInterval(170)
        let calls = OSAllocatedUnfairLock(initialState: [String]())
        let runner = RecordingTranscription(library: lib) { url, _ in
            let first = calls.withLock { $0.append(url.lastPathComponent); return $0.count == 1 }
            if first { throw CocoaError(.fileReadUnknown) }
            return [RecordingSentence(start: 1, end: 2, text: url.lastPathComponent)]
        }
        runner.pull(entry.id, range: range)
        for _ in 0..<200 where runner.errors[entry.id] == nil { try await Task.sleep(for: .milliseconds(10)) }
        var current = try #require(lib.entry(entry.id))
        let failed = current.pullState(from: range.lowerBound, to: range.upperBound,
                                       transcribing: runner.current == entry.id, error: runner.errors[entry.id])
        guard case .failed = failed else { Issue.record("expected a failure, got \(failed)"); return }
        #expect(failed.message != nil)
        #expect(current.pullState(from: range.lowerBound, to: range.upperBound, transcribing: false, error: runner.errors[entry.id]) != .ready)

        // Retrying is pulling the same range again.
        runner.pull(entry.id, range: range)
        #expect(runner.errors[entry.id] == nil)
        for _ in 0..<200 where lib.entry(entry.id)?.pullState(from: range.lowerBound, to: range.upperBound, transcribing: false, error: nil) != .ready {
            try await Task.sleep(for: .milliseconds(10))
        }
        current = try #require(lib.entry(entry.id))
        #expect(calls.withLock { $0 } == ["seg-001.m4a", "seg-001.m4a", "seg-002.m4a"])
        #expect(current.segments.map { $0.sentences == nil } == [true, false, false, true])
        #expect(current.pullState(from: range.lowerBound, to: range.upperBound, transcribing: false, error: nil) == .ready)
        #expect(current.pulledSentences(from: range.lowerBound, to: range.upperBound).map(\.1).contains("seg-002.m4a"))
    }

    @Test func pullStateWithoutTextSaysWhy() {
        let base = Date(timeIntervalSince1970: 1_800_000_000)
        var entry = RecordingEntry(id: UUID(), createdAt: base, source: .watch, kind: .rolling)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 60, startedAt: base)]
        let to = base.addingTimeInterval(30)
        #expect(entry.pullState(from: base, to: to, transcribing: false, error: nil) == .waiting)
        #expect(entry.pullState(from: base, to: to, transcribing: true, error: nil) == .transcribing)
        #expect(entry.pullState(from: base, to: to, transcribing: false, error: "x") == .failed("x"))
        #expect(entry.pullState(from: base.addingTimeInterval(600), to: base.addingTimeInterval(700),
                                transcribing: false, error: nil) == .noAudio)
        entry.segments[0].sentences = []
        #expect(entry.pullState(from: base, to: to, transcribing: false, error: nil) == .silent)
    }
}
