import SwiftUI

/// The native Next-message suggestions switch. It reads through the current assistant-scoped client, shows a change
/// optimistically, writes only `next_suggestions`, and keeps late work from an older assistant off the page.
@Observable @MainActor final class NextSuggestionsModel {
    typealias Deps = AssistantDeps

    /// nil means the hub has not answered or does not have the setting; false is a supporting hub's off value.
    private(set) var value: Bool?
    /// A successful answer landed. This distinguishes an older hub's nil from a read still in flight or a failed one.
    private(set) var loaded = false
    private(set) var saving = false

    @ObservationIgnored private let deps: Deps
    /// The assistant this page first read. `restart` moves it to the newly selected assistant.
    @ObservationIgnored private var client: APIClient?
    /// Invalidates an older read or write. Only one write runs at a time (`saving`).
    @ObservationIgnored private var revision = 0

    private var api: APIClient? { client ?? deps.api() }

    init(deps: Deps = .live) { self.deps = deps }

    func load() async {
        guard let api, !saving else { return }
        if client == nil { client = api }
        revision += 1
        let read = revision
        loaded = false
        do {
            let state = try await api.assistant()
            guard read == revision else { return }
            value = state.nextSuggestions
            loaded = true
        } catch {
            guard read == revision else { return }
            deps.report(error)
        }
    }

    /// Another assistant is current while the profile stays open: clear its value and invalidate its pending work.
    func restart() async {
        revision += 1
        client = nil
        value = nil
        loaded = false
        saving = false
        await load()
    }

    func set(_ on: Bool) async {
        guard let api, let was = value, !saving else { return }
        revision += 1
        let write = revision
        value = on
        saving = true
        defer { if write == revision { saving = false } }
        do {
            let state = try await api.setAssistant(nextSuggestions: on)
            guard write == revision else { return }
            // Match the web: the response is authoritative, with the requested value as compatibility fallback.
            value = state.nextSuggestions ?? on
        } catch {
            guard write == revision else { return }
            value = was
            deps.report(error)
        }
    }
}
