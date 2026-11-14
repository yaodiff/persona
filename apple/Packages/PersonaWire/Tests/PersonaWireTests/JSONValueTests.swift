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

    @Test func parsesGoTimestamps() {
        #expect(JSON.parseDate("2026-11-15T10:20:30.123456789Z") == Date(timeIntervalSince1970: 1_794_738_030.123))
        #expect(!Date(timeIntervalSince1970: 0).isMeaningful)
    }
}
