import Foundation
import PersonaWire
import Testing
@testable import Persona

/// The shared four cases for an existing source entry and its empty-state mention (#17399), with old-hub fallback.
@MainActor struct InboxSourceVisibilityTests {
    @Test func offeredAndStoredKeepsEveryEntryAndTheOriginalMessage() {
        let shown = InboxSourceVisibility(larkOffered: true, mailOffered: true,
                                          stored: InboxStoredSources(lark: true, mail: true))
        #expect(shown.lark && shown.mail)
        #expect(shown.shows(kind: "mail"))
        #expect(shown.selection(forKind: "mail") == "mail")
        #expect(shown.caughtUpMessage == String(localized: "New Lark messages and emails show up here."))
    }

    @Test func offeredWithoutStoredItemsStillShowsEveryEntry() {
        let shown = InboxSourceVisibility(larkOffered: true, mailOffered: true,
                                          stored: InboxStoredSources(lark: false, mail: false))
        #expect(shown.lark && shown.mail)
        #expect(shown.shows(kind: "mail"))
        #expect(shown.selection(forKind: "mail") == "mail")
        #expect(shown.caughtUpMessage == String(localized: "New Lark messages and emails show up here."))
    }

    @Test func storedItemsKeepTheirSourceWithoutItsCapability() {
        let lark = InboxSourceVisibility(larkOffered: false, mailOffered: false,
                                         stored: InboxStoredSources(lark: true, mail: false))
        #expect(lark.lark && !lark.mail)
        #expect(lark.caughtUpMessage == String(localized: "New Lark messages show up here."))
        let mail = InboxSourceVisibility(larkOffered: false, mailOffered: false,
                                         stored: InboxStoredSources(lark: false, mail: true))
        #expect(!mail.lark && mail.mail)
        #expect(mail.shows(kind: "mail"))
        #expect(mail.selection(forKind: "mail") == "mail")
        #expect(mail.caughtUpMessage == String(localized: "New emails show up here."))
    }

    @Test func neitherCapabilityNorStoredItemsHidesBothMentions() {
        let hidden = InboxSourceVisibility(larkOffered: false, mailOffered: false,
                                           stored: InboxStoredSources(lark: false, mail: false))
        #expect(!hidden.lark && !hidden.mail)
        #expect(!hidden.shows(kind: "mail"))
        #expect(hidden.selection(forKind: "mail") == "")
        #expect(hidden.selection(forKind: "group") == "group")
        #expect(hidden.caughtUpMessage == String(localized: "New messages show up here."))
        // @Me, DMs and Group are item-kind filters across sources, not Lark- or mail-only entries (#17399, #17814).
        #expect(InboxPage.kinds.map(\.0) == ["mention", "dm", "group", "mail", "agent"])
        #expect(InboxPage.kinds.filter { hidden.shows(kind: $0.0) }.map(\.0) == ["mention", "dm", "group", "agent"])
    }

    @Test func missingPresenceKeepsTodaysBehavior() {
        let old = InboxSourceVisibility(larkOffered: false, mailOffered: false, stored: nil)
        #expect(old.lark && old.mail)
        #expect(old.shows(kind: "mail"))
        #expect(old.selection(forKind: "mail") == "mail")
        #expect(old.caughtUpMessage == String(localized: "New Lark messages and emails show up here."))
    }

    @Test func listingDecodesPresenceAndUnknown() throws {
        let full = try JSON.decoder.decode(InboxListing.self, from: Data(
            #"{"items":[],"stored_sources":{"lark":true,"mail":false}}"#.utf8))
        #expect(full.storedSources?.lark == true)
        #expect(full.storedSources?.mail == false)
        let delta = try JSON.decoder.decode(InboxListing.self, from: Data(
            #"{"items":[],"delta":true,"stored_sources":{"lark":false,"mail":true}}"#.utf8))
        #expect(delta.storedSources?.lark == false)
        #expect(delta.storedSources?.mail == true)
        let missing = try JSON.decoder.decode(InboxListing.self, from: Data(#"{"items":[]}"#.utf8))
        #expect(missing.storedSources == nil)
        let null = try JSON.decoder.decode(InboxListing.self, from: Data(#"{"items":[],"stored_sources":null}"#.utf8))
        #expect(null.storedSources == nil)
    }
}
