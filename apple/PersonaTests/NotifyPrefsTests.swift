import Foundation
import Testing
@testable import Persona
import PersonaWire

struct NotifyPrefsTests {
    @Test func decodesWhatTheHubSends() throws {
        let json = #"{"input":false,"done":"always","live_activity":false,"lock_details":true,"long_turn_seconds":90,"done_delay_seconds":60}"#
        let p = try JSON.decoder.decode(NotifyPrefs.self, from: Data(json.utf8))
        #expect(p.input == false)
        #expect(p.done == .always)
        #expect(p.longTurnSeconds == 90)
        #expect(p.doneDelaySeconds == 60)
        #expect(p.liveActivity == false)
        #expect(p.lockDetails == true)
    }

    /// An older hub, or a value this app does not know yet: the defaults, not an error page.
    @Test func missingOrUnknownFallsBackToDefaults() throws {
        let p = try JSON.decoder.decode(NotifyPrefs.self, from: Data(#"{"done":"sometimes"}"#.utf8))
        #expect(p == NotifyPrefs(input: true, done: .long))
        #expect(p.liveActivity == true && p.lockDetails == false) // the activity is on, names hidden, by default
    }

    /// Only the choices go back; the hub's timings are read-only.
    @Test func encodesOnlyTheChoices() throws {
        var p = NotifyPrefs(input: true, done: .off)
        p.longTurnSeconds = 60
        let obj = try JSONSerialization.jsonObject(with: try JSON.encoder.encode(p)) as? [String: Any]
        #expect(obj?["done"] as? String == "off")
        #expect(obj?["input"] as? Bool == true)
        #expect(obj?["long_turn_seconds"] == nil)
        #expect(obj?["live_activity"] as? Bool == true)
        #expect(obj?["lock_details"] as? Bool == false)
    }

    @Test func durationWording() {
        #expect(NotifyPrefs.duration(nil) == "1 分钟")
        #expect(NotifyPrefs.duration(120) == "2 分钟")
        #expect(NotifyPrefs.duration(90) == "90 秒")
    }
}
