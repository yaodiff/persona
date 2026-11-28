import PersonaModuleAPI
import SwiftUI

/// How much of mail this hub offers (#15995): the hub's `module.mail` flag, read by the one rule every module follows
/// (`HubModules.allows`). Stored-only only when the hub publishes module flags (`modules`) and not this one; an older
/// hub and a hub never heard from are live.
///
/// With the hub's mail module off every route under `/api/v1/mail` answers 404 and no new mail arrives. A mail's full
/// body, its HTML and its attachments are read from the mail server each time it is opened and are never stored, so
/// they cannot be shown then. What the hub did store for a mail's Inbox item (subject, sender, date, a preview and the
/// newest mail's text, `context` of `GET /api/v1/inbox/{id}`) is the user's and stays readable (the owner's rule,
/// #13430): Core routes keep serving it.
///
/// This one type is the whole decision "the whole message" or "the stored text because mail is off": pages take it,
/// none compares a flag or a string.
nonisolated enum MailAccess: Hashable, Sendable {
    /// Mail works: the message is read from the mail server, and everything that starts or changes mail is offered.
    case live
    /// The hub's mail module is off: what the hub stored is shown, and nothing that needs the mail server is offered.
    case storedOnly

    /// The hub's answer as the access.
    init(hub: HubModules) {
        self = hub.allows(MailModule.descriptor) ? .live : .storedOnly
    }

    /// Accounts, New Email, the mail's own reply, flags, attachments and its translation are offered.
    var offersMail: Bool { self == .live }

    /// Whether a reply to this Inbox item can go out: a mail's reply is sent by the mail module, whichever route asks
    /// for it (the Inbox's reply, the AI's draft, a suggested reply); every other source is untouched.
    func offersReply(to item: InboxItem) -> Bool { self == .live || item.mail == nil }

    /// A suggestion card's buttons for this item: a suggested reply to a mail keeps only Reject while mail is off (Send
    /// and Edit would send a mail); everything else is the usual set.
    func suggestionActions(_ s: InboxItem.Suggestion, for item: InboxItem) -> [InboxEventText.Action] {
        let usual = InboxEventText.actions(s)
        guard s.kind == "reply", !s.isHandback, !offersReply(to: item) else { return usual }
        return usual.filter { $0 == .reject }
    }
}

/// Mail as the hub's module flags see it. It is not a packaged module on Apple (its pages are in
/// `apple/Persona/Views/Mail`), so it is not in `Modules.builtIn`; it has the module's id, and so its flag.
enum MailModule {
    /// `mail`, gated by `module.mail`. It registers nothing, so it names no slot.
    nonisolated static let descriptor = ModuleDescriptor(id: "mail", hubGated: true, slots: [])

    /// The access of the hub the app follows. Observed through the module registry: a view that reads it draws again
    /// when a hello changes the hub's answer (at most once per hello; a set lookup, cheap in `body`:
    /// docs/apple/performance.md P4, P5).
    @MainActor static var access: MailAccess { MailAccess(hub: ModuleState.shared.registry.hub) }

    /// The one line Settings › Connectors › Mail shows while the module is off.
    static var settingsOffLine: String {
        String(localized: "Mail is turned off on the hub machine, so accounts can't be added or changed here. Mail already in your Inbox stays.")
    }
}

/// Settings › Connectors › Mail: itself while mail is live, else the one line. It reads the access where it draws, so
/// the page behind it does not depend on the module registry and asks the hub nothing while mail is off.
struct MailSettingsGate<Content: View>: View {
    @ViewBuilder var content: () -> Content

    var body: some View {
        if MailModule.access.offersMail {
            content()
        } else {
            ModuleOffPage(title: String(localized: "Mail [Mail Views 730752]"), line: MailModule.settingsOffLine)
        }
    }
}
