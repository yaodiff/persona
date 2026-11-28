import Foundation
import PersonaModuleAPI
import PersonaWire
import Testing
@testable import Persona

/// The iPhone follows the hub's `module.recording` flag (#15214; the owner's rule, #13430): with the module off nothing
/// here starts or changes a recording, and what the user already has stays listed, readable and theirs to delete. With
/// the module on, or a hub that publishes no module flags, nothing changes.
@MainActor struct RecordingModuleOffTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    // MARK: The allowance

    /// The one rule (`HubModules.allows`), for the recording flag: off only with `modules` and without `module.recording`.
    @Test func theAllowanceFollowsTheHubsAnswer() {
        #expect(RecordingModule.descriptor.feature == "module.recording")
        #expect(RecordingAllowance(hub: .everyOn) == .on, "nothing heard, or an older hub")
        #expect(RecordingAllowance(hub: HubModules(hello: ["file_upload", "recording_day"])) == .on, "no marker: an older hub")
        #expect(RecordingAllowance(hub: HubModules(hello: ["modules", "module.recording"])) == .on)
        #expect(RecordingAllowance(hub: HubModules(hello: ["modules", "module.voice"])) == .off)
        #expect(RecordingAllowance(hub: HubModules(hello: ["modules"])) == .off)
        #expect(RecordingAllowance(hub: HubModules(stored: ["module.voice"])) == .off, "the remembered answer, before this launch's hello")
        #expect(RecordingAllowance(hub: HubModules(stored: nil)) == .on)
        #expect(RecordingAllowance.on.offersWrites && !RecordingAllowance.off.offersWrites)
    }

    // MARK: Entry points

    @Test func theRoomOffersOnlyReadingWithTheModuleOff() {
        let off = MeetingRoomOffers.of(UUID(), module: .off)
        #expect(off == .moduleOff)
        #expect(!off.ask && !off.justMe && !off.bookmark && !off.photos && !off.persona)
        #expect(!off.pageMenu && !off.privateMeeting && !off.handOff && !off.followup)
        #expect(!off.rename && !off.photoDelete && !off.liveWatch)
        #expect(off.afterMenu, "the way to the recording's page, where Delete is, stays")
        #expect(!off.replayClock)
    }

    @Test func theRoomIsUnchangedWithTheModuleOn() {
        let on = MeetingRoomOffers.of(UUID(), module: .on)
        #expect(on == .meeting)
        #expect(on == MeetingRoomOffers.of(UUID()))
        #expect(on.ask && on.justMe && on.bookmark && on.photos && on.persona && on.pageMenu && on.privateMeeting)
        #expect(on.handOff && on.followup && on.afterMenu && on.rename && on.photoDelete && on.liveWatch)
        // A replayed tape is on no hub: the module changes nothing for it.
        #expect(MeetingRoomOffers.of(ReplayedMeeting.id, module: .off) == .replay)
        #expect(MeetingRoomOffers.of(ReplayedMeeting.id, module: .on) == .replay)
        #expect(MeetingRoomOffers.replay.liveWatch && MeetingRoomOffers.replay.photoDelete, "as before")
    }

    @Test func theAllDaySwitchNeverTurnsOnWithTheModuleOff() {
        #expect(RecordingDayTodaySection.showsSwitch(.on, on: false))
        #expect(RecordingDayTodaySection.showsSwitch(.on, on: true))
        #expect(!RecordingDayTodaySection.showsSwitch(.off, on: false), "nothing new starts")
        #expect(RecordingDayTodaySection.showsSwitch(.off, on: true), "one that runs can still be turned off")
    }

    @Test func minutesWritesAreNotOfferedWithTheModuleOff() async {
        let hub = FakeHub()
        let entry = doneMeeting()
        hub.on("GET", kbPath(entry.id), json: detail(state: "ready", minutes: true))
        let off = RecordingMinutesModel(api: { hub.client }, recording: { .off })
        await off.load(entry, synced: true)
        guard case .ready = off.state else { Issue.record("stored minutes stay readable: \(off.state)"); return }
        #expect(!off.offersWrites)
        #expect(!off.canRegenerate(entry, synced: true))
        let on = RecordingMinutesModel(api: { hub.client }, recording: { .on })
        await on.load(entry, synced: true)
        #expect(on.offersWrites)
        #expect(on.canRegenerate(entry, synced: true))
        #expect(on.state == off.state, "the same minutes either way")
    }

    // MARK: Delete

    @Test func theDeleteRouteFollowsTheModule() {
        let id = UUID(uuidString: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE")!
        #expect(RecordingSync.deletePath(id, recording: .on) == "/api/v1/recordings/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
        #expect(RecordingSync.deletePath(id, recording: .off) == "/api/v1/kb/recordings/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
    }

    /// Off: the delete goes through the knowledge base's own route, and the module's routes (the list, the upload) are
    /// never asked, so no "sync isn't supported" problem shows.
    @Test func withTheModuleOffDeleteUsesTheCoreRouteAndNothingElseIsAsked() async throws {
        let (lib, hub, root) = files()
        defer { try? FileManager.default.removeItem(at: root) }
        let gone = try transcribed(lib)
        let kept = try transcribed(lib)
        hub.on("DELETE", kbPath(gone.id), json: #"{"deleted": true}"#)
        let sync = RecordingSync(library: lib, api: { hub.client }, now: { self.now }, audioDaysChanged: { _ in }, recording: { .off })
        try sync.delete(gone.id)
        await sync.syncNow()
        #expect(hub.sent("DELETE", kbPath(gone.id)).count == 1)
        #expect(sync.store.deletes.isEmpty)
        #expect(sync.problem == nil)
        let others = hub.requests.filter { !($0.method == "DELETE" && $0.path == kbPath(gone.id)) }
        #expect(others.isEmpty, "\(others.map { "\($0.method) \($0.path)" })")
        // The one never synced stays on this iPhone, listed and readable.
        #expect(lib.entry(kept.id) != nil)
        #expect(sync.status(kept) == .pending)
    }

    /// The Core route's answers (`kb_recording_routes.go`): `deleted: false` (it no longer has it) settles the delete too;
    /// a refusal, or an older hub's 404, leaves it pending and said.
    @Test func theCoreRoutesAnswersSettleOrKeepTheDelete() async throws {
        let (lib, hub, root) = files()
        defer { try? FileManager.default.removeItem(at: root) }
        let sync = RecordingSync(library: lib, api: { hub.client }, now: { self.now }, audioDaysChanged: { _ in }, recording: { .off })
        let already = try transcribed(lib)
        hub.on("DELETE", kbPath(already.id), json: #"{"deleted": false}"#)
        try sync.delete(already.id)
        await sync.syncNow()
        #expect(sync.store.deletes.isEmpty)

        let refused = try transcribed(lib)
        hub.on("DELETE", kbPath(refused.id)) { _ in throw APIError(status: 403, message: "Read-only", fields: ["code": "kb_read_only"]) }
        try sync.delete(refused.id)
        await sync.syncNow()
        #expect(sync.store.deletes == [refused.id])
        #expect(sync.problem?.contains(String(localized: "Pending deletions have not been removed from Knowledge yet.")) == true)

        // No route at all (a hub from before the Core delete): a 404 confirms nothing.
        let (lib2, hub2, root2) = files()
        defer { try? FileManager.default.removeItem(at: root2) }
        let sync2 = RecordingSync(library: lib2, api: { hub2.client }, now: { self.now }, audioDaysChanged: { _ in }, recording: { .off })
        let unknown = try transcribed(lib2)
        try sync2.delete(unknown.id)
        await sync2.syncNow()
        #expect(sync2.store.deletes == [unknown.id])

        // Already gone (410) is a confirmation on either route.
        let (lib3, hub3, root3) = files()
        defer { try? FileManager.default.removeItem(at: root3) }
        let sync3 = RecordingSync(library: lib3, api: { hub3.client }, now: { self.now }, audioDaysChanged: { _ in }, recording: { .off })
        let goneThere = try transcribed(lib3)
        hub3.on("DELETE", kbPath(goneThere.id)) { _ in throw APIError(status: 410, message: "deleted", fields: ["code": "recording_deleted"]) }
        try sync3.delete(goneThere.id)
        await sync3.syncNow()
        #expect(sync3.store.deletes.isEmpty)
    }

    /// On: today's route and today's pass (the list first, then the delete), exactly.
    @Test func withTheModuleOnDeleteUsesTheModulesRouteAsBefore() async throws {
        let (lib, hub, root) = files()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        let path = "/api/v1/recordings/\(entry.id.uuidString.lowercased())"
        hub.on("GET", "/api/v1/recordings", json: #"{"days": 14, "recordings": [], "deleted": []}"#)
        hub.on("DELETE", path, json: #"{"deleted": true}"#)
        let sync = RecordingSync(library: lib, api: { hub.client }, now: { self.now }, audioDaysChanged: { _ in }, recording: { .on })
        try sync.delete(entry.id)
        await sync.syncNow()
        #expect(hub.sent("GET", "/api/v1/recordings").count == 1)
        #expect(hub.sent("DELETE", path).count == 1)
        #expect(hub.sent("DELETE", kbPath(entry.id)).isEmpty)
        #expect(sync.store.deletes.isEmpty)
    }

    // MARK: Paused

    @Test func pausedIsAClosedTypeWithAnUnknown() throws {
        #expect(RecordingPause(wire: "module_off") == .moduleOff)
        #expect(RecordingPause(wire: "quota") == .unknown("quota"))
        #expect(RecordingPause(wire: nil) == nil && RecordingPause(wire: "") == nil)
        #expect(RecordingPause.moduleOff.isModuleOff && !RecordingPause.unknown("quota").isModuleOff)
        // It goes back as it came.
        for pause in [RecordingPause.moduleOff, .unknown("quota")] {
            #expect(try JSONDecoder().decode(RecordingPause.self, from: JSONEncoder().encode(pause)) == pause)
        }
        // A value that is no string is an unknown reason, never a failed decode.
        #expect(try JSONDecoder().decode([RecordingPause].self, from: Data("[7]".utf8)) == [.unknown("")])
    }

    @Test func theListEntryAndTheDetailDecodePaused() throws {
        let list = try JSON.decoder.decode(KBList.self, from: Data("""
        {"items": [
          {"kind": "recordings", "id": "r1", "name": "Weekly", "relevance": 0, "paused": "module_off"},
          {"kind": "recordings", "id": "r2", "name": "Review", "relevance": 0},
          {"kind": "recordings", "id": "r3", "name": "Later", "relevance": 0, "paused": "something_new"},
          {"kind": "recordings", "id": "r4", "name": "Odd", "relevance": 0, "paused": 7},
          {"kind": "projects", "id": "p", "name": "Project", "relevance": 0, "paused": "module_off"}
        ]}
        """.utf8))
        let paused: [RecordingPause?] = list.items.map { $0.paused }
        #expect(paused == [RecordingPause.moduleOff, nil, RecordingPause.unknown("something_new"), RecordingPause.unknown(""), RecordingPause.moduleOff])
        // Only a recording paused for the module shows the mark; an unknown reason shows none.
        let tags: [String?] = list.items.map { $0.pausedTag }
        #expect(tags == [String(localized: "Paused"), nil, nil, nil, nil])

        func detail(_ paused: String) throws -> KBDetail {
            try JSON.decoder.decode(KBDetailResponse.self, from: Data(#"{"detail": {"kind": "recordings", "id": "r1", "path": "recordings/r1"\#(paused)}}"#.utf8)).detail
        }
        #expect(try detail(#", "paused": "module_off""#).paused == .moduleOff)
        #expect(try detail("").paused == nil)
        #expect(try detail(#", "paused": "something_new""#).paused == .unknown("something_new"))
    }

    /// Pending minutes the hub holds back are paused, not "Writing…", and the 15-second poll stops.
    @Test func pausedMinutesAreNotWritingAndThePollStops() async {
        let hub = FakeHub()
        let entry = doneMeeting()
        hub.on("GET", kbPath(entry.id), json: detail(state: "pending", paused: "module_off"))
        // Even before the app's own hello says so: the hub's word on the recording is enough.
        let model = RecordingMinutesModel(api: { hub.client }, recording: { .on })
        await model.load(entry, synced: true)
        #expect(model.state == .paused)
        #expect(model.pause == .moduleOff)
        #expect(!model.waiting, "the page's follow ends here: no second request")
        #expect(hub.sent("GET", kbPath(entry.id)).count == 1)
        #expect(RecordingWrapUp.minutes(model.state) == .paused)
    }

    /// Minutes shown while newer ones were pending when the module went off: they stay, without "Updating…" for ever.
    @Test func shownMinutesStayWhileTheirRewriteIsPaused() async {
        let hub = FakeHub()
        let entry = doneMeeting()
        hub.on("GET", kbPath(entry.id), json: detail(state: "pending", minutes: true, paused: "module_off"))
        let model = RecordingMinutesModel(api: { hub.client }, recording: { .off })
        await model.load(entry, synced: true)
        guard case .ready = model.state else { Issue.record("the minutes went: \(model.state)"); return }
        #expect(!model.updating && !model.waiting)
    }

    /// A reason this app doesn't know neither crashes nor reads as the module being off: the page is as it was.
    @Test func anUnknownPausedValueChangesNothing() async {
        let hub = FakeHub()
        let entry = doneMeeting()
        hub.on("GET", kbPath(entry.id), json: detail(state: "pending", paused: "something_new"))
        let model = RecordingMinutesModel(api: { hub.client }, recording: { .on })
        await model.load(entry, synced: true)
        #expect(model.state == .writing)
        #expect(model.pause == .unknown("something_new"))
        #expect(model.waiting)
    }

    /// The module on and nothing paused: exactly today's states and today's poll.
    @Test func withTheModuleOnNothingChanges() async {
        let hub = FakeHub()
        let entry = doneMeeting()
        hub.on("GET", kbPath(entry.id), json: detail(state: "pending"))
        let model = RecordingMinutesModel(api: { hub.client }, recording: { .on })
        await model.load(entry, synced: true)
        #expect(model.state == .writing && model.pause == nil && model.waiting)
        typealias M = RecordingMinutesModel
        #expect(M.state(minutes: nil, status: "pending", meeting: true, synced: true) == .writing)
        #expect(M.state(minutes: nil, status: "pending", meeting: true, synced: true, pause: .unknown("x")) == .writing)
        #expect(M.state(minutes: nil, status: "pending", meeting: true, synced: true, pause: .moduleOff) == .paused)
        #expect(M.state(minutes: nil, status: "failed", meeting: true, synced: true, pause: .moduleOff) == .failed)
        for state in [M.State.none, .waitingForText, .waitingForSync, .writing, .failed, .unsupported, .paused] {
            #expect(M.held(state, recording: .on) == state)
        }
    }

    /// The app knows the module is off (its hello), and the recording never reached the hub: nothing is on its way, so
    /// nothing says it is, and nothing is asked again.
    @Test func withTheModuleOffNothingWaits() async {
        typealias M = RecordingMinutesModel
        #expect(M.held(.waitingForText, recording: .off) == .paused)
        #expect(M.held(.waitingForSync, recording: .off) == .paused)
        #expect(M.held(.writing, recording: .off) == .paused)
        #expect(M.held(.none, recording: .off) == .none)
        #expect(M.held(.failed, recording: .off) == .failed)
        #expect(M.held(.unsupported, recording: .off) == .unsupported)
        // Only on this iPhone: the hub has no such recording (404).
        let hub = FakeHub()
        let model = M(api: { hub.client }, recording: { .off })
        await model.load(doneMeeting(), synced: false)
        #expect(model.state == .paused)
        #expect(!model.waiting)
    }

    @Test func theWrapUpEndsOnPausedMinutes() {
        let held = RecordingPhase.Held(isMeeting: true, active: false, receivedAll: true, noAudio: false, transcribedAll: true,
                                       transcribing: false, transcriptionError: nil, sync: .synced, hubAvailable: true, minutes: .paused)
        #expect(RecordingPhase.derive(.init(held: held)) == .done(hasMinutes: false))
    }

    @Test func theWatchRelayStopsAskingForPausedMinutes() {
        let a = UUID(), b = UUID()
        #expect(MinutesRelay.stillWriting([(a, .writing), (b, .ready)], paused: [], recording: .on))
        #expect(MinutesRelay.stillWriting([(a, nil)], paused: [], recording: .on), "not read yet counts as being written")
        #expect(!MinutesRelay.stillWriting([(a, .writing), (b, .ready)], paused: [a], recording: .on))
        #expect(!MinutesRelay.stillWriting([(a, .writing)], paused: [], recording: .off))
        #expect(!MinutesRelay.stillWriting([(a, .ready), (b, .failed)], paused: [], recording: .on))
    }

    // MARK: Fixtures

    private func kbPath(_ id: UUID) -> String { "/api/v1/kb/recordings/\(id.uuidString.lowercased())" }

    private func files() -> (RecordingLibrary, FakeHub, URL) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        return (RecordingLibrary(root: root), FakeHub(), root)
    }

    private func transcribed(_ lib: RecordingLibrary) throws -> RecordingEntry {
        var entry = RecordingEntry(id: UUID(), createdAt: now, source: .phone, active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 1, end: 3, text: "Check the export next week.")])]
        try lib.save(entry)
        return lib.entry(entry.id)!
    }

    /// A meeting recorded, transcribed and done: what the page loads minutes for.
    private func doneMeeting() -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly", active: false, expectedSegments: 1)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: [RecordingSentence(start: 1, end: 9, text: "Bo adds the test.")])]
        return e
    }

    private func detail(state: String, minutes: Bool = false, paused: String? = nil) -> String {
        let pausedField = paused.map { #", "paused": "\#($0)""# } ?? ""
        let minutesField = minutes ? #", "minutes": {"version": 2, "points": [{"text": "Export", "at": 1}]}"# : ""
        return #"{"detail": {"meta": {"minutes_state": "\#(state)"}\#(minutesField)\#(pausedField)}}"#
    }
}
