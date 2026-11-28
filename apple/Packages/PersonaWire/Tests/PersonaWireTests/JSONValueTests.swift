import Foundation
import Testing

@testable import PersonaWire

@Suite struct JSONValueTests {
    @Test func roundTripsKeepingKeys() throws {
        let raw = #"{"a_b":[1,"x",null,true],"c":{"d":2.5}}"#
        let value = try JSONDecoder().decode(JSONValue.self, from: Data(raw.utf8))
        #expect(value["a_b"]?.array?.count == 4)
        #expect(value["c"]?["d"]?.double == 2.5)
        let again = try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(value))
        #expect(again == value)
    }

    @Test func comparesOccurrenceTimesWithoutTruncatingNanoseconds() {
        #expect(JSON.compareOccurrenceTimes("2026-11-15T10:20:30.000000001Z", "2026-11-15T10:20:30.000000002Z") == .orderedAscending)
        #expect(JSON.compareOccurrenceTimes("2026-11-15T18:20:30.000000002+08:00", "2026-11-15T10:20:30.000000001Z") == .orderedDescending)
        #expect(JSON.compareOccurrenceTimes("2026-11-15T18:20:30.000000001+08:00", "2026-11-15T10:20:30.000000001Z") == .orderedSame)
        #expect(JSON.compareOccurrenceTimes("not a time", "2026-11-15T10:20:30Z") == nil)
    }
}
