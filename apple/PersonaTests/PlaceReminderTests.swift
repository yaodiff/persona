import Foundation
import Testing
@testable import Persona
import PersonaWire

/// #6564: a reminder that goes off on arriving at or leaving a place, and the tools that change and delete reminders.
@MainActor
struct PlaceReminderTests {
    private func json(_ text: String) throws -> JSONValue {
        try JSONDecoder().decode(JSONValue.self, from: Data(text.utf8))
    }

    @Test func aPlaceParsesWithADefaultRadius() throws {
        let place = try PlaceTrigger.parse(json(#"{"title":"Office","latitude":31.2,"longitude":121.5,"trigger":"leave"}"#))
        #expect(place == PlaceTrigger(title: "Office", latitude: 31.2, longitude: 121.5, radius: 150, kind: .leave))
    }

    @Test func theRadiusStaysWhereIOSCanWatchIt() throws {
        let small = try PlaceTrigger.parse(json(#"{"title":"Home","latitude":1,"longitude":2,"trigger":"arrive","radius_meters":10}"#))
        let large = try PlaceTrigger.parse(json(#"{"title":"Home","latitude":1,"longitude":2,"trigger":"arrive","radius_meters":90000}"#))
        #expect(small?.radius == 100 && large?.radius == 2000)
    }

    @Test func noPlaceIsNotAnError() throws {
        #expect(try PlaceTrigger.parse(nil) == nil)
        #expect(try PlaceTrigger.parse(.null) == nil)
    }

    @Test func aBrokenPlaceIsRefusedNotIgnored() throws {
        for bad in [#"{"latitude":1,"longitude":2,"trigger":"arrive"}"#,
                    #"{"title":"Home","latitude":91,"longitude":2,"trigger":"arrive"}"#,
                    #"{"title":"Home","latitude":1,"trigger":"arrive"}"#,
                    #"{"title":"Home","latitude":1,"longitude":2,"trigger":"near"}"#] {
            #expect(throws: ToolFailure.self) { try PlaceTrigger.parse(json(bad)) }
        }
    }

    @Test func itRoundTripsAsTheListShowsIt() throws {
        let place = PlaceTrigger(title: "Home", latitude: 31.25, longitude: 121.5, radius: 200, kind: .arrive)
        #expect(try PlaceTrigger.parse(place.json) == place)
    }

    @Test func theApprovalCardSaysWhenItGoesOff() {
        let leave = PlaceTrigger(title: "Office", latitude: 0, longitude: 0, radius: 150, kind: .leave).summary
        let arrive = PlaceTrigger(title: "Home", latitude: 0, longitude: 0, radius: 150, kind: .arrive).summary
        #expect(leave.contains("Office") && arrive.contains("Home") && leave != arrive)
    }

    @Test func theChangeCardListsWhatChanges() throws {
        let target = RemindersTool.CompletionTarget(title: "Buy milk", list: nil, due: nil)
        let args = try json(#"{"id":"r1","due":null,"location":{"title":"Home","latitude":1,"longitude":2,"trigger":"arrive"}}"#)
        let summary = RemindersTool.changeSummary(id: "r1", target: target, args: args)
        #expect(summary.contains("Buy milk") && summary.contains("Home"))
        #expect(summary.contains(String(localized: "\\nNo due date")))
        let cleared = RemindersTool.changeSummary(id: "r1", target: target, args: try json(#"{"id":"r1","location":null}"#))
        #expect(cleared.contains(String(localized: "\\nNo longer goes off at a place")))
    }

    @Test func aCardForAReminderThatIsGoneSaysSo() {
        let missing = RemindersTool.completeSummary(id: "r9", target: nil)
        #expect(RemindersTool.deleteSummary(id: "r9", target: nil) == missing)
        #expect(RemindersTool.changeSummary(id: "r9", target: nil, args: .object([:])) == missing)
    }

    private func object(_ content: [ToolContent]) throws -> JSONValue {
        try json(content.compactMap(\.text).joined(separator: "\n"))
    }

    /// Through the real EventKit store: needs Reminders access granted to the app on the simulator first
    /// (`xcrun simctl privacy <sim> grant reminders <bundle id>`), so it runs only when asked
    /// (`TEST_RUNNER_PERSONA_EVENTKIT_ROUNDTRIP=1`).
    @Test(.enabled(if: ProcessInfo.processInfo.environment["PERSONA_EVENTKIT_ROUNDTRIP"] != nil))
    func aPlaceReminderRoundTripsThroughReminders() async throws {
        let created = try object(await RemindersTool.create(json("""
        {"title":"Buy milk (place test)","location":{"title":"Office","latitude":31.23,"longitude":121.47,"trigger":"leave"}}
        """)))
        let id = try #require(created["id"]?.string)
        #expect(try PlaceTrigger.parse(created["location"]) == PlaceTrigger(title: "Office", latitude: 31.23, longitude: 121.47,
                                                                             radius: 150, kind: .leave))
        let listed = try object(await RemindersTool.list(.object([:])))
        let row = try #require(listed["reminders"]?.array?.first { $0["id"]?.string == id })
        #expect(row["location"]?["trigger"]?.string == "leave")

        let changed = try object(await RemindersTool.update(json("""
        {"id":"\(id)","location":{"title":"Home","latitude":31.2,"longitude":121.4,"trigger":"arrive","radius_meters":200}}
        """)))
        #expect(changed["location"]?["title"]?.string == "Home" && changed["location"]?["trigger"]?.string == "arrive")
        let cleared = try object(await RemindersTool.update(json(#"{"id":"\#(id)","location":null}"#)))
        #expect(cleared["location"] == nil)

        _ = try await RemindersTool.delete(.object(["id": .string(id)]))
        let after = try object(await RemindersTool.list(.object([:])))
        #expect(after["reminders"]?.array?.contains { $0["id"]?.string == id } == false)
    }
}
