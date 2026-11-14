import Foundation
import PersonaWire

// An extension's sign-in as the hub shows it (docs/extensions/extensions.md「Sign-in」, #4507), here for the push that carries
// it (`extension_auth`, LiveConnection); the page and the store are the app's (Persona/Views/Settings/ExtensionAuth.swift).

/// An extension's sign-in as the hub shows it: GET …/-/auth, `ExtensionView.auth`, the `extension_auth` event.
nonisolated struct ExtensionAuth: Codable, Hashable, Sendable {
    /// signed_out | pending | signed_in | expired | error | unknown (the program does not run, or was not asked yet).
    enum State: String, Codable, Sendable {
        case signedOut = "signed_out", pending, signedIn = "signed_in", expired, error, unknown

        /// A state this app does not know reads as not known.
        init(from decoder: Decoder) throws {
            self = State(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
        }
    }

    var title: String
    var description: String?
    /// The extension's own sign-in page, for apps without this section (this app does not list it).
    var page: String?
    var state: State
    var account: String?
    /// Why it is in error, or what is signed in, for people.
    var message: String?
    var expiresAt: Date?
    var checkedAt: Date?

    enum CodingKeys: String, CodingKey {
        case title, description, page, state, account, message
        case expiresAt = "expires_at", checkedAt = "checked_at"
    }

    init(title: String, state: State, account: String? = nil, message: String? = nil, page: String? = nil) {
        self.title = title
        self.state = state
        self.account = account
        self.message = message
        self.page = page
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        description = try c.decodeIfPresent(String.self, forKey: .description)
        page = try c.decodeIfPresent(String.self, forKey: .page)
        state = try c.decodeIfPresent(State.self, forKey: .state) ?? .unknown
        account = try c.decodeIfPresent(String.self, forKey: .account)
        message = try c.decodeIfPresent(String.self, forKey: .message)
        expiresAt = try? c.decodeIfPresent(Date.self, forKey: .expiresAt)
        checkedAt = try? c.decodeIfPresent(Date.self, forKey: .checkedAt)
    }

    /// Not signed in, or no longer: the list's row and the page ask the user to.
    var needsSignIn: Bool { state == .signedOut || state == .expired }
    /// Signed in: Sign Out shows, the button says Sign In Again.
    var isSignedIn: Bool { state == .signedIn }
}
