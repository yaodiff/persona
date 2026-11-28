import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The disk record keeps a cursor only with the exact hub list it names; anything uncertain reads whole after a launch.
@MainActor
struct InboxStoreCacheTests {
    private func item(_ id: String, at: TimeInterval) throws -> InboxItem {
        let json = """
        {"id":"\(id)","source":"mail","kind":"mail","account":"a","account_name":"A","title":"\(id)","preview":"","count":1,
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: at)))","manager":"assistant"}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    @Test func recordRoundTripsItemsCursorAndHub() throws {
        let hub = try #require(URL(string: "https://hub-one.example.com"))
        let items = [try item("a", at: 200), try item("b", at: 100)]
        let saved = InboxStore.Saved(items: items, cursor: "m1.abc", hub: hub)
        let restored = try JSON.decoder.decode(InboxStore.Saved.self, from: JSON.encoder.encode(saved))
        #expect(restored.items == items)
        #expect(restored.cursor == "m1.abc")
        #expect(restored.hub == hub)
        #expect(restored.storedCursor?.value == "m1.abc")
        #expect(restored.storedCursor?.hub == hub)
    }

    @Test func legacyBareArrayLoadsWithoutACursor() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let items = [try item("legacy", at: 100)]
        StateCache.save("inbox-items", items, in: folder)
        for _ in 0..<100 {
            if StateCache.load("inbox-items", as: [InboxItem].self, in: folder) != nil { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        #expect(StateCache.load("inbox-items", as: [InboxItem].self, in: folder) == items)
        let restored = InboxStore.loadSaved(in: folder)
        #expect(restored.items == items)
        #expect(restored.cursor == nil && restored.hub == nil)
        #expect(restored.storedCursor == nil)
    }

    @Test func localEditsAndPendingMarksWithholdTheCursor() throws {
        let hub = try #require(URL(string: "https://hub-one.example.com"))
        let cursor = (value: "m1.clean", hub: hub)
        let items = [try item("local", at: 100)]
        let clean = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                         itemsRevision: 7, marksEmpty: true)
        #expect(clean.cursor == cursor.value && clean.hub == hub)
        let edited = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                          itemsRevision: 8, marksEmpty: true)
        #expect(edited.items == items)
        #expect(edited.cursor == nil && edited.hub == nil)
        let marked = InboxStore.makeSaved(items: items, cursor: cursor, cursorRevision: 7,
                                          itemsRevision: 7, marksEmpty: false)
        #expect(marked.cursor == nil && marked.hub == nil)
        let empty = InboxStore.makeSaved(items: [], cursor: cursor, cursorRevision: 0,
                                         itemsRevision: 0, marksEmpty: true)
        #expect(empty.cursor == nil && empty.hub == nil)
        #expect(InboxStore.Saved(items: items, cursor: "incomplete", hub: nil).storedCursor == nil)
    }

    @Test func aDeltaCannotCertifyADirtyOrChangedBase() {
        #expect(InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                 baseUnchanged: true, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: false,
                                                  baseUnchanged: true, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                  baseUnchanged: false, marksEmpty: true))
        #expect(!InboxStore.listingIsCleanForDisk(isDelta: true, baseWasClean: true,
                                                  baseUnchanged: true, marksEmpty: false))
        #expect(InboxStore.listingIsCleanForDisk(isDelta: false, baseWasClean: false,
                                                 baseUnchanged: false, marksEmpty: true))
    }

    @Test func aSavedCursorIsUsedOnlyForItsOwnHub() throws {
        let one = try #require(URL(string: "https://hub-one.example.com"))
        let two = try #require(URL(string: "https://hub-two.example.com"))
        let saved = InboxStore.Saved(items: [try item("a", at: 100)], cursor: "m1.private", hub: one)
        #expect(InboxStore.since(cursor: saved.storedCursor, for: one) == "m1.private")
        #expect(InboxStore.since(cursor: saved.storedCursor, for: two) == InboxListing.noCursor)
    }
}

/// Holds a reply after the real API client has sent its request, even when the caller cancels.
@MainActor private final class HeldInboxReply {
    var reply: CheckedContinuation<Data, Error>?

    func hold() async throws -> Data {
        try await withCheckedThrowingContinuation { reply = $0 }
    }

    func waitUntilRequested() async throws {
        let deadline = ContinuousClock.now + .seconds(3)
        while reply == nil, ContinuousClock.now < deadline {
            try await Task.sleep(for: .milliseconds(10))
        }
        _ = try #require(reply)
    }
}

extension InboxStoreCacheTests {
    private func listing(_ title: String) -> Data {
        Data("""
        {"items":[{"id":"same","source":"mail","kind":"mail","account":"a","account_name":"A",        "title":"\(title)","preview":"","count":1}],"cursor":"m1.current",        "triage":{"state":"ok","reason":"\(title)","waiting":1}}
        """.utf8)
    }

    @Test func switchingBackWhileInboxRequestsAreOutstandingKeepsOnlyTheLatestVisit() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let store = InboxStore(saved: .init(items: [], cursor: nil, hub: nil), cacheFolder: folder)
        let a = FakeHub(url: "https://a.example")
        let b = FakeHub(url: "https://b.example")
        let oldA = HeldInboxReply(), oldB = HeldInboxReply(), newA = HeldInboxReply()
        a.onAsync("GET", "/api/v1/inbox") { _ in try await oldA.hold() }
        b.onAsync("GET", "/api/v1/inbox") { _ in try await oldB.hold() }
        let first = store.reload(from: a.client)
        try await oldA.waitUntilRequested()
        store.unsentReplies["same"] = "A draft"

        store.forget()
        #expect(!store.loading && !store.listed && store.unsentReplies.isEmpty)
        let second = store.reload(from: b.client)
        try await oldB.waitUntilRequested()
        store.forget()
        a.onAsync("GET", "/api/v1/inbox") { _ in try await newA.hold() }
        let third = store.reload(from: a.client)
        try await newA.waitUntilRequested()

        oldA.reply?.resume(returning: listing("First visit"))
        await first.value
        oldB.reply?.resume(throwing: URLError(.timedOut))
        await second.value
        #expect(store.loading && !store.listed)
        #expect(store.items.isEmpty && store.triage == nil && store.error == nil)
        // A reconnect during the third read still coalesces, even after the first two tasks' defers have run.
        let repeated = store.reload(from: a.client)
        newA.reply?.resume(returning: listing("Current visit"))
        await third.value
        await repeated.value
        #expect(a.sent("GET", "/api/v1/inbox").count == 2)
        #expect(store.listed && !store.loading && store.error == nil)
        #expect(store.items.map(\.title) == ["Current visit"])
        #expect(store.triage?.reason == "Current visit")
    }
}

extension InboxStoreCacheTests {
    @Test func coldLaunchOnlyRestoresAnAttributableInboxCache() throws {
        let first = PairedHub(id: "a", config: .init(url: URL(string: "https://one.example")!, token: "a"), group: "")
        let second = PairedHub(id: "b", config: .init(url: URL(string: "https://two.example")!, token: "b"), group: "g2")
        let registry = HubRegistry(hubs: [first, second])
        let items = [try item("same", at: 100)]
        // Local action removes cursor eligibility; a written pairing survives both that action and a route change.
        let saved = InboxStore.makeSaved(items: items, cursor: nil, cursorRevision: nil, itemsRevision: 4, marksEmpty: false,
                                         owner: InboxStore.Saved.owner(of: first))
        let record = try JSON.decoder.decode(InboxStore.Saved.self, from: JSON.encoder.encode(saved))
        #expect(record.forHub(group: "", registry: registry, demo: false).items == items)
        #expect(record.forHub(group: "g2", registry: registry, demo: false).items.isEmpty)
        #expect(record.forHub(group: "", registry: registry, demo: true).items.isEmpty)
        var moved = first
        moved.config.url = URL(string: "https://replacement-address.example")!
        #expect(record.forHub(group: "", registry: .init(hubs: [moved]), demo: false).items == items)

        let legacy = InboxStore.Saved(items: items, cursor: "m1.cursor", hub: first.config.url)
        #expect(legacy.forHub(group: "", registry: registry, demo: false).items == items)
        #expect(legacy.forHub(group: "g2", registry: registry, demo: false).items.isEmpty)
        var duplicate = second
        duplicate.config.url = first.config.url
        #expect(legacy.forHub(group: "", registry: .init(hubs: [first, duplicate]), demo: false).items.isEmpty)
        let unowned = InboxStore.Saved(items: items, cursor: nil, hub: nil)
        #expect(unowned.forHub(group: "", registry: .init(hubs: [first]), demo: false).items.isEmpty)
    }
}
