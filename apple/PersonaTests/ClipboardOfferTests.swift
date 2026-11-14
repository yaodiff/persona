import Foundation
import Testing
@testable import Persona
import PersonaWire

/// "Send what you just copied to …?" (#3064): when it is offered, and to which computer.
struct ClipboardOfferTests {
    private func machine(_ id: String, online: Bool = true, clipboard: MachineClipboard? = MachineClipboard(supported: true, enabled: true),
                         trust: String? = nil) -> Machine {
        var m = Machine(id: id, name: id, kind: "ssh", online: online)
        m.clipboard = clipboard
        m.trust = trust
        return m
    }

    @Test func offersOnlyWhenOnChangedAndNotEmpty() {
        #expect(ClipboardOffer.shouldOffer(enabled: true, seen: 4, now: 5, hasContent: true))
        #expect(!ClipboardOffer.shouldOffer(enabled: false, seen: 4, now: 5, hasContent: true), "off by default")
        #expect(!ClipboardOffer.shouldOffer(enabled: true, seen: 5, now: 5, hasContent: true), "nothing new was copied")
        #expect(!ClipboardOffer.shouldOffer(enabled: true, seen: 4, now: 5, hasContent: false), "the copy was cleared")
        #expect(!ClipboardOffer.shouldOffer(enabled: true, seen: nil, now: 5, hasContent: true), "no baseline yet")
        #expect(!ClipboardOffer.shouldOffer(enabled: true, seen: 4, now: 5, hasContent: true, assistive: true),
                "VoiceOver / Switch Control can't reach the toast's button: the computer page is the way")
    }

    @Test func picksTheLastComputerThatCanTakeIt() {
        let off = machine("off", clipboard: MachineClipboard(supported: true, enabled: false))
        let unsupported = machine("linux", clipboard: MachineClipboard(supported: false, enabled: true))
        let asleep = machine("asleep", online: false)
        let pending = machine("pending", trust: "pending")
        let a = machine("a"), b = machine("b")
        let all = [off, unsupported, asleep, pending, machine("old", clipboard: nil), a, b]
        #expect(ClipboardOffer.target(all, preferred: nil)?.id == "a")
        #expect(ClipboardOffer.target(all, preferred: b.ref.path)?.id == "b")
        #expect(ClipboardOffer.target(all, preferred: asleep.ref.path)?.id == "a", "the last one is offline: the first ready one")
        #expect(ClipboardOffer.target([off, unsupported, asleep], preferred: nil) == nil)
    }

    /// Everything is off until the user turns it on (#3064): an older hub's machine (no `clipboard`) is never offered or
    /// usable, an empty state is off and unsupported, and the offer setting is off when never set.
    @Test func everythingIsOffByDefault() throws {
        let old = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"mac","name":"Mac","kind":"local","online":true}"#.utf8))
        #expect(old.clipboard == nil && old.clipboard?.usable != true)
        #expect(ClipboardOffer.target([old], preferred: old.ref.path) == nil)
        let empty = try JSON.decoder.decode(MachineClipboard.self, from: Data("{}".utf8))
        #expect(!empty.supported && !empty.enabled && !empty.usable)
        #expect(MachineClipboard() == empty)
        let fresh = try #require(UserDefaults(suiteName: "clipboard-offer-default-\(UUID().uuidString)"))
        #expect(!fresh.bool(forKey: ClipboardOffer.enabledKey))
        #expect(!ClipboardOffer.shouldOffer(enabled: fresh.bool(forKey: ClipboardOffer.enabledKey), seen: 1, now: 2, hasContent: true))
    }
}
