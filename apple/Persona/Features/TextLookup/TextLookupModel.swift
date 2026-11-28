import Foundation
import Observation
import PersonaWire

// MARK: - Wire (docs/apple/ios.md "Word lookup (text_lookup)", #10977)

/// `GET /api/v1/text-lookup`: whether an enabled extension looks words up, which one, and the words saved in it.
nonisolated struct TextLookupState: Decodable, Equatable, Sendable {
    var available = false
    var extensionID: String?
    var contribution: String?
    var name: String?
    var underlineSaved = false
    var words: [String] = []

    static let unavailable = TextLookupState()

    enum CodingKeys: String, CodingKey {
        case available, contribution, name, words
        case extensionID = "extension"
        case underlineSaved = "underline_saved"
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        available = try c.decodeIfPresent(Bool.self, forKey: .available) ?? false
        extensionID = try c.decodeIfPresent(String.self, forKey: .extensionID)
        contribution = try c.decodeIfPresent(String.self, forKey: .contribution)
        name = try c.decodeIfPresent(String.self, forKey: .name)
        underlineSaved = try c.decodeIfPresent(Bool.self, forKey: .underlineSaved) ?? false
        words = try c.decodeIfPresent([String].self, forKey: .words) ?? []
    }
}

/// `POST /api/v1/text-lookup/lookup`: what the extension knows about a word.
nonisolated struct TextLookupEntry: Decodable, Equatable, Sendable {
    nonisolated struct Sense: Decodable, Equatable, Sendable, Hashable {
        var pos: String?
        var text: String
    }

    var found: Bool
    var word: String
    var lemma: String?
    var phonetic: String?
    var senses: [Sense]
    var saved: Bool

    enum CodingKeys: String, CodingKey { case found, word, lemma, phonetic, senses, saved }

    init(found: Bool, word: String, lemma: String? = nil, phonetic: String? = nil, senses: [Sense] = [], saved: Bool = false) {
        self.found = found; self.word = word; self.lemma = lemma; self.phonetic = phonetic; self.senses = senses; self.saved = saved
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        found = try c.decodeIfPresent(Bool.self, forKey: .found) ?? false
        word = try c.decodeIfPresent(String.self, forKey: .word) ?? ""
        lemma = try c.decodeIfPresent(String.self, forKey: .lemma)
        phonetic = try c.decodeIfPresent(String.self, forKey: .phonetic)
        senses = try c.decodeIfPresent([Sense].self, forKey: .senses) ?? []
        saved = try c.decodeIfPresent(Bool.self, forKey: .saved) ?? false
    }

    /// The form saved and underlined: the lemma when the extension gave one.
    var key: String { TextLookupModel.normalized(lemma.flatMap { $0.isEmpty ? nil : $0 } ?? word) }
}

private nonisolated struct TextLookupSaved: Decodable, Sendable { var saved: Bool }
private nonisolated struct TextLookupRequest: Encodable, Sendable {
    var word: String
    var context: String?
    var source: String?
}

// MARK: - Model

/// The app's side of the `text_lookup` extension point (#10977): whether a tap on a word looks it up, the lookups and
/// the saved words. One per app, for the hub-level group (`AppModel.textLookup`). Inert until the hub's hello carries
/// `text_lookup` and `GET /api/v1/text-lookup` says `available`: until then `active` is false and every reading surface
/// draws its words exactly as before. Refetched on `text_lookup_changed`, on extension changes, on (re)connect and on
/// coming back to the front (at most once a `foregroundInterval`). All IO is `async` through `APIClient` (off the main
/// thread); the observed state changes only when an answer differs (P5, P7).
@Observable final class TextLookupModel {
    struct Deps {
        var api: () -> APIClient?
        /// The current hub's hello features.
        var features: () -> Set<String>
        var now: () -> Date = Date.init
    }

    static let feature = "text_lookup"
    static let path = "/api/v1/text-lookup"
    /// Coming back to the front reads the state again at most this often.
    static let foregroundInterval: TimeInterval = 60
    /// Lookups kept in memory (P11).
    static let cacheLimit = 200

    /// What the hub said last; `.unavailable` while the feature is off or before the first answer.
    private(set) var state = TextLookupState.unavailable
    /// The saved words, lowercased, for Save / Saved and the underline.
    private(set) var saved: Set<String> = []
    /// Changes when `saved` does: what a view's memo of its underlined ranges is keyed by.
    private(set) var savedRevision = 0

    /// Words are tappable: the hub has the feature and an enabled extension answers.
    var active: Bool { state.available }
    /// Underline saved words (the extension's setting, off by default).
    var underlines: Bool { active && state.underlineSaved && !saved.isEmpty }
    /// The provider's name as the extension gives it ("Relingo"); a neutral word without one.
    var providerName: String {
        guard let name = state.name?.trimmingCharacters(in: .whitespacesAndNewlines), !name.isEmpty else {
            return String(localized: "Dictionary [Text Lookup]")
        }
        return name
    }

    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var reading: Task<Void, Never>?
    @ObservationIgnored private var readAgain = false
    /// Bumped when a read under way no longer answers for the current hub: an older read's answer is dropped.
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var lastRead: Date?
    @ObservationIgnored private var cache: [String: TextLookupEntry] = [:]
    @ObservationIgnored private var cacheOrder: [String] = []
    /// Requests sent, for the tests (`GET` reads).
    @ObservationIgnored private(set) var reads = 0

    init(deps: Deps) { self.deps = deps }

    nonisolated static func normalized(_ word: String) -> String {
        word.trimmingCharacters(in: .whitespacesAndNewlines).replacingOccurrences(of: "\u{2019}", with: "'").lowercased()
    }

    // MARK: State

    /// Reads the state again: on a hello, `text_lookup_changed` and extension changes. A read already on its way is
    /// followed by one more, so the last change is never missed; several changes meanwhile make one read.
    func refresh() {
        guard deps.features().contains(Self.feature), deps.api() != nil else {
            stopReading()
            apply(.unavailable)
            return
        }
        if reading != nil { readAgain = true; return }
        let current = generation
        reading = Task { [weak self] in
            await self?.read(current)
        }
    }

    /// Another hub was picked: nothing of the last one's (its state, saved words, lookups or a read still on its way)
    /// answers for the new one, which is then read from scratch.
    func reset() {
        stopReading()
        lastRead = nil
        clearCache()
        apply(.unavailable)
        refresh()
    }

    private func stopReading() {
        generation += 1
        reading?.cancel()
        reading = nil
        readAgain = false
    }

    /// Back in front: read again unless that was done within `foregroundInterval`.
    func foreground() {
        if let last = lastRead, deps.now().timeIntervalSince(last) < Self.foregroundInterval, deps.features().contains(Self.feature) { return }
        refresh()
    }

    /// Waits for the reads under way (tests).
    func settle() async {
        while let task = reading { await task.value }
    }

    private func read(_ current: Int) async {
        repeat {
            readAgain = false
            guard let api = deps.api(), deps.features().contains(Self.feature) else { apply(.unavailable); break }
            reads += 1
            lastRead = deps.now()
            do {
                let state = try await api.get(Self.path, as: TextLookupState.self)
                // Stopped, or the hub changed while this was out: the answer is the old hub's.
                if Task.isCancelled || current != generation { return }
                apply(state)
            } catch {
                // An older manager (404) or one that cannot answer now: nothing is tappable, nothing breaks.
                if Task.isCancelled || current != generation { return }
                apply(.unavailable)
            }
        } while readAgain
        if current == generation { reading = nil }
    }

    private func apply(_ new: TextLookupState) {
        // Another extension or contribution answers now: its definitions and saved state are its own.
        if new.extensionID != state.extensionID || new.contribution != state.contribution { clearCache() }
        if new != state { state = new }
        let words = Set((new.available ? new.words : []).map(Self.normalized))
        if words != saved { saved = words; savedRevision += 1 }
        if !new.available { clearCache() }
    }

    // MARK: Lookups

    /// What a lookup of `word` failed on, in words the card shows.
    struct Failure: LocalizedError, Equatable {
        var message: String
        var errorDescription: String? { message }
    }

    /// Looks `word` up (a cached answer comes back at once).
    func lookup(_ word: String, context: String?) async throws -> TextLookupEntry {
        let key = Self.normalized(word)
        if var hit = cache[key] {
            hit.saved = saved.contains(hit.key) || saved.contains(key)
            return hit
        }
        guard active, let api = deps.api() else { throw Failure(message: Self.unavailableText) }
        let current = generation
        let provider = (state.extensionID, state.contribution)
        do {
            var entry = try await api.post(Self.path + "/lookup", body: TextLookupRequest(word: word, context: context.flatMap { $0.isEmpty ? nil : $0 }),
                                           timeout: 15, as: TextLookupEntry.self)
            if entry.word.isEmpty { entry.word = word }
            if sameTarget(generation: current, provider: provider) {
                remember(key, entry)
                // The extension's word on whether it is saved wins over this copy.
                if entry.saved != (saved.contains(entry.key) || saved.contains(key)) { mark(entry.key, saved: entry.saved) }
            }
            return entry
        } catch let e as APIError where e.code == "text_lookup_unavailable" {
            // Turned off since the last read: the words go back to being only words.
            if sameTarget(generation: current, provider: provider) { refresh() }
            throw Failure(message: Self.unavailableText)
        }
    }

    /// Saves the word (`entry.key`) to the extension's list; the state shows it at once and goes back on failure.
    func save(_ entry: TextLookupEntry, context: String?, source: String?) async throws {
        guard let api = deps.api() else { throw Failure(message: Self.unavailableText) }
        let current = generation
        let provider = (state.extensionID, state.contribution)
        mark(entry.key, saved: true)
        do {
            let answer = try await api.post(Self.path + "/words",
                                            body: TextLookupRequest(word: entry.key, context: context.flatMap { $0.isEmpty ? nil : $0 }, source: source),
                                            as: TextLookupSaved.self)
            if sameTarget(generation: current, provider: provider) { mark(entry.key, saved: answer.saved) }
        } catch {
            if sameTarget(generation: current, provider: provider) { mark(entry.key, saved: false) }
            throw error
        }
    }

    /// Removes the word from the extension's list; it comes back on failure.
    func remove(_ entry: TextLookupEntry) async throws {
        guard let api = deps.api() else { throw Failure(message: Self.unavailableText) }
        let current = generation
        let provider = (state.extensionID, state.contribution)
        mark(entry.key, saved: false)
        do {
            let escaped = entry.key.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? entry.key
            _ = try await api.request("DELETE", Self.path + "/words/" + escaped)
            if sameTarget(generation: current, provider: provider) { mark(entry.key, saved: false) }
        } catch {
            if sameTarget(generation: current, provider: provider) { mark(entry.key, saved: true) }
            throw error
        }
    }

    private func sameTarget(generation: Int, provider: (String?, String?)) -> Bool {
        generation == self.generation && state.available && state.extensionID == provider.0 && state.contribution == provider.1
    }

    func isSaved(_ entry: TextLookupEntry) -> Bool { saved.contains(entry.key) || saved.contains(Self.normalized(entry.word)) }

    private func mark(_ key: String, saved on: Bool) {
        var words = saved
        if on { words.insert(key) } else { words.remove(key) }
        if words != saved { saved = words; savedRevision += 1 }
    }

    private func remember(_ key: String, _ entry: TextLookupEntry) {
        if cache[key] == nil { cacheOrder.append(key) }
        cache[key] = entry
        while cacheOrder.count > Self.cacheLimit { cache[cacheOrder.removeFirst()] = nil }
    }

    private func clearCache() {
        cache.removeAll()
        cacheOrder.removeAll()
    }

    static var unavailableText: String { String(localized: "Word lookup is turned off.") }
}
