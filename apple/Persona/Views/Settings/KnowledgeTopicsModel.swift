import SwiftUI

/// Settings › Knowledge Selection's "Organize knowledge points by topic" (#7251, the web's `KnowledgeTopicsSwitch`): the
/// assistant's `kb_topics`, on unless the hub says off. Read through the current assistant's client, shown changed at
/// once, written alone, and put back with the reason when the hub refuses. Late work from an older read or write is dropped.
@Observable @MainActor final class KnowledgeTopicsModel {
    typealias Deps = AssistantDeps

    /// nil until the hub answered.
    private(set) var value: Bool?
    private(set) var saving = false
    private(set) var problem: String?

    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var revision = 0

    init(deps: Deps) { self.deps = deps }

    func load() async {
        guard let api = deps.api(), !saving else { return }
        revision += 1
        let read = revision
        do {
            let state = try await api.assistant()
            guard read == revision else { return }
            value = state.kbTopics ?? true
            problem = nil
        } catch {
            guard read == revision, let shown = error.shown else { return }
            problem = String(localized: "Couldn't read the setting: \(shown)")
        }
    }

    func set(_ on: Bool) async {
        guard let api = deps.api(), let was = value, !saving else { return }
        revision += 1
        let write = revision
        value = on
        saving = true
        problem = nil
        defer { if write == revision { saving = false } }
        var body = APIClient.AssistantConfigBody()
        body.kbTopics = on
        do {
            let state = try await api.setAssistant(body)
            guard write == revision else { return }
            value = state.kbTopics ?? on
        } catch {
            guard write == revision else { return }
            value = was
            deps.toast(Toast(String(localized: "Couldn't save: \(error.shown ?? error.localizedDescription)"), style: .error))
        }
    }
}

#if os(iOS)
/// The switch and the way to the map, at the top of Knowledge Selection.
struct KnowledgeTopicsSection: View {
    var model: KnowledgeTopicsModel

    var body: some View {
        Section {
            Toggle(isOn: Binding(get: { model.value ?? true }, set: { on in Task { await model.set(on) } })) {
                Text("Organize Knowledge Points by Topic")
            }
            .disabled(model.value == nil || model.saving)
            .accessibilityIdentifier("kb.topics.switch")
            NavigationLink { KnowledgeMapView() } label: {
                Label("Open the Knowledge Map", systemImage: Theme.Icon.knowledgeMap).lineLimit(1)
            }
            .accessibilityIdentifier("kb.topics.map")
            if let problem = model.problem { LoadProblem(text: problem) }
        } header: {
            Text("Knowledge Points")
        } footer: {
            Text("In the background, Persona pulls durable knowledge (concepts, decisions, how-tos, pitfalls, facts) out of your project timelines and chats and files it by topic, using the knowledge base's model. Turning it off stops new curation; topics already organized stay.")
        }
    }
}
#endif
