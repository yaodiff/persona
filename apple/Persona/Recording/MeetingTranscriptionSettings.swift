#if os(iOS)
import Foundation
import Network
import Observation

/// The network the phone is on now, for hub transcription's Wi-Fi rule (#3862 phase 5, `MeetingTranscriptionRule.mayStream`):
/// one `NWPathMonitor`, read on a queue of its own and handed to the main actor only when the kind changes.
@MainActor @Observable
final class MeetingNetwork {
    static let shared = MeetingNetwork()

    /// Unknown, so not Wi-Fi, until the monitor's first answer: with **Hub transcription on Wi-Fi only** nothing goes
    /// before the phone knows it is on Wi-Fi.
    private(set) var kind: MeetingNetworkKind = .unknown
    /// The monitor's answers as they come, on its own queue: what the Wi-Fi rule decides on, since `kind` reaches the main
    /// actor a hop later (a socket retried in between never goes on the path that just left).
    nonisolated let latest = MeetingNetworkState()
    @ObservationIgnored private var monitor: NWPathMonitor?

    func start() {
        guard monitor == nil else { return }
        let monitor = NWPathMonitor()
        self.monitor = monitor
        let state = latest
        monitor.pathUpdateHandler = { @Sendable path in
            let kind = MeetingNetwork.kind(path)
            state.set(kind)
            Task { @MainActor in
                if MeetingNetwork.shared.kind != kind { MeetingNetwork.shared.kind = kind }
            }
        }
        monitor.start(queue: DispatchQueue(label: "persona.meeting-network"))
    }

    /// A path's kind: none without a usable path; metered on cellular or a network the system marks expensive (a phone's
    /// hotspot); else unmetered (Wi-Fi, wired).
    nonisolated static func kind(_ path: NWPath) -> MeetingNetworkKind {
        guard path.status == .satisfied else { return .none }
        if path.usesInterfaceType(.cellular) || path.isExpensive { return .metered }
        return .unmetered
    }
}

/// The phone's path as the Wi-Fi rule reads it (`MeetingNetwork.latest`): its kind now, and the last moment it was on
/// cellular or an expensive network. A tunnel's own connection stays on the path it was made on, so with **Hub
/// transcription on Wi-Fi only** one that began before that moment may still run over cellular
/// (`MeetingTranscriptionRule.mayStream(wifiOnly:network:tunnel:meteredUntil:)`).
nonisolated final class MeetingNetworkState: @unchecked Sendable {
    private let lock = NSLock()
    private var current: MeetingNetworkKind = .unknown
    private var leftMetered: Date?

    var kind: MeetingNetworkKind { lock.withLock { current } }

    /// The last moment the path was on cellular or an expensive network: now while it is, nil when it wasn't since launch.
    var meteredUntil: Date? { lock.withLock { current == .metered ? Date() : leftMetered } }

    /// The monitor answered `kind` at `now`.
    func set(_ kind: MeetingNetworkKind, at now: Date = Date()) {
        lock.withLock {
            if current == .metered, kind != .metered { leftMetered = now }
            current = kind
        }
    }
}

/// Settings for hub transcription (#3862 phase 5, docs/assistant/meeting-minutes.md「Choosing the mode」): the hub's
/// `meeting_transcription_default`, `meeting_glossary` and `meeting_transcription_extension` (`GET` / `PUT
/// /api/v1/assistant`, full credentials), its read-only `meeting_transcription_ready`, and the iPhone's own **Hub
/// transcription on Wi-Fi only** (off by default, kept on this phone). Load → show → edit → save: a change shows at once
/// and goes back if the hub refuses it. The data for the settings screen; the screen itself is drawn elsewhere.
@MainActor @Observable
final class MeetingTranscriptionSettings {
    struct Deps {
        /// The assistant's settings as the page captured them (`APIClient.currentAssistantSettings`).
        var api: () -> APIClient?
        var store: UserDefaults = .standard
        /// The app's copy of the assistant settings is read again (only when it is the same hub and manager).
        var saved: (APIClient) async -> Void = { _ in }
        /// The Wi-Fi rule changed: the uplink follows it at once.
        var wifiOnlyChanged: (Bool) -> Void = { _ in }
        /// The hub's extensions (`GET /api/v1/plugins`), for the contributions that can transcribe (`choices`).
        var plugins: (APIClient) async throws -> [HubExtension] = { _ in [] }
        /// The speech service's name as read (`service`): the meeting page's privacy line names it too.
        var serviceRead: (String?) -> Void = { _ in }
    }

    /// What the hub says, once read.
    nonisolated struct Values: Equatable, Sendable {
        var defaultMode: MeetingTranscriptionMode
        var glossary: [String]
        /// `""`: the built-in voice extension's contribution; else `<extension id>/<contribution id>`.
        var extensionRef: String
        var ready: Bool
    }

    /// Why a change didn't go.
    nonisolated enum Failure: Error, Equatable, Sendable {
        /// More than `maxTerms` terms.
        case tooManyTerms
        /// A term over `maxTermLength` characters (the first such one).
        case termTooLong(String)
        /// The hub refused or couldn't be reached: its words, or the app's for the error.
        case hub(String)
    }

    /// One transcription contribution the owner can pick.
    nonisolated struct Choice: Identifiable, Hashable, Sendable {
        /// `""` for the built-in one, else `<extension id>/<contribution id>`.
        var ref: String
        var extensionName: String
        var contribution: String
        var builtIn: Bool { ref.isEmpty }
        var id: String { ref }
    }

    static let maxTerms = 200
    static let maxTermLength = 40
    /// The built-in voice extension's id: its `transcription` contribution is the one `""` names.
    static let builtInExtension = "voice"

    /// nil until read, and from a hub without hub transcription (`offered` false).
    private(set) var values: Values?
    /// The hub has the settings (it returned them): a hub without `meeting_transcription` has none to show.
    private(set) var offered = false
    private(set) var loading = false
    private(set) var saving = false
    private(set) var failure: Failure?
    /// **Hub transcription on Wi-Fi only**.
    private(set) var wifiOnly: Bool
    /// The contributions that can transcribe, the built-in one first (`choices(_:)`); only the built-in one until the
    /// hub's extensions are read, and when they can't be.
    private(set) var choices: [Choice] = [Choice(ref: "", extensionName: "", contribution: "")]

    @ObservationIgnored let deps: Deps
    @ObservationIgnored private var api: APIClient?

    init(deps: Deps) {
        self.deps = deps
        wifiOnly = deps.store.bool(forKey: MeetingAttendanceDefaults.wifiOnlyKey)
    }

    /// `GET /api/v1/assistant`.
    func load() async {
        if api == nil { api = deps.api() }
        guard let api else { return }
        loading = true
        defer { loading = false }
        do {
            apply(try await api.assistant())
            failure = nil
        } catch {
            failure = .hub(Self.message(error))
            return
        }
        // The extension choice shows only with more than the built-in one; a list that can't be read hides it.
        guard offered, let list = try? await deps.plugins(api) else { return }
        let next = Self.choices(list)
        if choices != next { choices = next }
        deps.serviceRead(service)
    }

    /// The speech service a hub-mode meeting's audio goes to, by name, when the owner chose another extension's
    /// (the privacy texts name it, docs/assistant/meeting-minutes.md「Privacy per mode」); nil for the built-in one.
    var service: String? { values.flatMap { Self.service($0.extensionRef, choices: choices) } }

    func setDefault(_ mode: MeetingTranscriptionMode) async {
        guard var shown = values, shown.defaultMode != mode else { return }
        shown.defaultMode = mode
        await save(shown) { try await $0.setAssistant(meetingTranscriptionDefault: mode.rawValue) }
    }

    /// The glossary as edited (the list editor's rows): cleaned as the hub cleans it (`cleanGlossary`), refused here
    /// when the hub would refuse it, so nothing is sent that can't be kept. True once the hub has it (or had it already);
    /// false when it was refused, failed, or not sent because another change is still being saved (`saving`).
    @discardableResult
    func setGlossary(_ terms: [String]) async -> Bool {
        guard var shown = values else { return false }
        switch Self.cleanGlossary(terms) {
        case .failure(let problem):
            failure = problem
            return false
        case .success(let clean):
            guard clean != shown.glossary else { return true }
            shown.glossary = clean
            return await save(shown) { try await $0.setAssistant(meetingGlossary: clean) }
        }
    }

    func setExtension(_ ref: String) async {
        guard var shown = values, shown.extensionRef != ref else { return }
        shown.extensionRef = ref
        await save(shown) { try await $0.setAssistant(meetingTranscriptionExtension: ref) }
    }

    /// **Hub transcription on Wi-Fi only**: kept on this phone; the uplink follows it at once.
    func setWiFiOnly(_ on: Bool) {
        guard wifiOnly != on else { return }
        wifiOnly = on
        deps.store.set(on, forKey: MeetingAttendanceDefaults.wifiOnlyKey)
        deps.wifiOnlyChanged(on)
    }

    /// Saves `next` (shown at once, put back if the hub refuses it): true once the hub kept it; false when it refused,
    /// failed, or another change is still being saved (one at a time: this one isn't sent).
    @discardableResult
    private func save(_ next: Values, _ put: (APIClient) async throws -> APIClient.AssistantState) async -> Bool {
        if api == nil { api = deps.api() }
        guard let api, !saving else { return false }
        let before = values
        values = next
        saving = true
        defer { saving = false }
        do {
            apply(try await put(api))
            failure = nil
            deps.serviceRead(service)
            await deps.saved(api)
            return true
        } catch {
            values = before
            failure = .hub(Self.message(error))
            return false
        }
    }

    private func apply(_ st: APIClient.AssistantState) {
        guard let mode = st.meetingTranscriptionDefault else {
            offered = false
            values = nil
            return
        }
        offered = true
        let next = Values(defaultMode: MeetingTranscriptionMode(rawValue: mode) ?? .device, glossary: st.meetingGlossary ?? [],
                          extensionRef: st.meetingTranscriptionExtension ?? "", ready: st.meetingTranscriptionReady ?? false)
        if values != next { values = next }
        MeetingAttendanceDefaults.remember(transcriptionDefault: next.defaultMode, ready: next.ready, deps.store)
    }

    // MARK: Pure

    /// The glossary as the hub keeps it (`cleanGlossary` there): each term on one line with its runs of white space made
    /// one space, blanks and repeats (case-insensitive; the first spelling stays) dropped; more than `maxTerms`, or a term
    /// over `maxTermLength` characters (Unicode scalars, as the hub counts), is refused.
    static func cleanGlossary(_ terms: [String]) -> Result<[String], Failure> {
        var out: [String] = []
        var seen = Set<String>()
        for raw in terms {
            let term = raw.split(whereSeparator: { $0.isWhitespace }).joined(separator: " ")
            if term.unicodeScalars.count > maxTermLength { return .failure(.termTooLong(term)) }
            let key = term.lowercased()
            guard !term.isEmpty, !seen.contains(key) else { continue }
            seen.insert(key)
            out.append(term)
            if out.count > maxTerms { return .failure(.tooManyTerms) }
        }
        return .success(out)
    }

    /// The transcription contributions of the hub's extensions, the built-in one first (`""`), then the others in the
    /// order listed. The choice is shown only when there is more than the built-in one (`showsChoice`).
    static func choices(_ extensions: [HubExtension]) -> [Choice] {
        var out = [Choice(ref: "", extensionName: extensions.first { $0.id == builtInExtension }?.name ?? "", contribution: "")]
        for ext in extensions where ext.id != builtInExtension {
            for cid in ext.contributes?["transcription"] ?? [] where !cid.isEmpty {
                let ref = "\(ext.id)/\(cid)"
                if !out.contains(where: { $0.ref == ref }) { out.append(Choice(ref: ref, extensionName: ext.name, contribution: cid)) }
            }
        }
        return out
    }

    static func showsChoice(_ choices: [Choice]) -> Bool { choices.contains { !$0.builtIn } }

    /// The name of the extension `ref` names (`<extension id>/<contribution id>`), nil for the built-in one (`""`); one
    /// the hub doesn't list (yet) goes by its id, so the privacy text still names it.
    static func service(_ ref: String, choices: [Choice]) -> String? {
        guard !ref.isEmpty else { return nil }
        if let known = choices.first(where: { $0.ref == ref }), !known.extensionName.isEmpty { return known.extensionName }
        return String(ref.split(separator: "/").first ?? Substring(ref))
    }

    /// A choice's words in the picker: the built-in one as such, another by its extension's name, with the contribution
    /// when that extension offers several.
    static func label(_ choice: Choice, in choices: [Choice]) -> String {
        if choice.builtIn { return String(localized: "Built-In [Meeting Transcription]") }
        let siblings = choices.filter { !$0.builtIn && $0.extensionName == choice.extensionName }
        return siblings.count > 1 ? "\(choice.extensionName) · \(choice.contribution)" : choice.extensionName
    }

    /// The hub's words when it gave any, else the app's for the error.
    static func message(_ error: Error) -> String {
        if let api = error as? APIError { return api.localizedDescription }
        return error.localizedDescription
    }
}

extension MeetingTranscriptionSettings.Deps {
    /// The app's: the assistant settings the Persona page shows, the app's copy read again after a save.
    static var live: Self {
        Self(api: { AppModel.shared.api?.currentAssistantSettings() },
             saved: { api in
                 let model = AppModel.shared
                 if api.sameSettingsScope(as: model.api) { await model.refreshAssistant() }
             },
             wifiOnlyChanged: { MeetingTranscription.shared.wifiOnlyChanged($0) },
             plugins: { try await $0.plugins() },
             serviceRead: { MeetingTranscription.shared.noteService($0) })
    }
}
#endif
