import Observation
import PersonaKit
import SwiftUI
import PersonaWire

// Settings › Channels (docs/extensions/channels.md): every way of talking to Persona — the apps, the Feishu bot, WeChat, /agent in
// Feishu, voice, the chat app bots plugins contribute — as GET /api/v1/channels lists them; one channel's page (its
// switch, binding a step at a time, where its messages go, what it does, a test message); and the source of a message
// that came from one (its client_msg_id is `<channel>-<message id>`). Only with a hub whose features have `channels`.

extension APIClient {
    func channel(_ id: String) async throws -> HubChannelDetail { try await get("/api/v1/channels/\(id)") }

    /// Changes only what is given: the switch, where messages go, features by id.
    func updateChannel(_ id: String, enabled: Bool? = nil, target: ChannelTarget? = nil, features: [String: Bool]? = nil) async throws -> HubChannelDetail {
        struct Body: Encodable {
            var enabled: Bool?
            var target: ChannelTarget?
            var features: [String: Bool]?
        }
        let body = Body(enabled: enabled, target: target, features: features)
        return try JSON.decoder.decode(HubChannelDetail.self, from: try await request("PUT", "/api/v1/channels/\(id)", body: body))
    }

    /// One step of binding (the connectors' sign-in steps, docs/extensions/connectors.md): "" starts over.
    func bindChannel(_ id: String, step: String, values: [String: String]) async throws -> ConnectorPrompt {
        struct Body: Encodable { var step: String; var values: [String: String] }
        return try await post("/api/v1/channels/\(id)/bind", body: Body(step: step, values: values), timeout: 90)
    }

    func unbindChannel(_ id: String) async throws -> HubChannelDetail {
        try JSON.decoder.decode(HubChannelDetail.self, from: try await request("DELETE", "/api/v1/channels/\(id)/binding"))
    }

    /// A line to the owner only, to see the channel reaches them.
    func testChannel(_ id: String) async throws { _ = try await post("/api/v1/channels/\(id)/test", timeout: 40) }
}

/// What channels are called and say in the settings and the conversation.
enum ChannelText {
    static func state(_ c: HubChannel) -> String {
        switch c.state {
        case "connected": String(localized: "Connected [V N C Page 5be032]")
        case "error": String(localized: "Something Went Wrong")
        case "disabled": String(localized: "Disabled")
        case "not_installed": String(localized: "Not Installed")
        default: String(localized: "Not Connected")
        }
    }

    /// The dot beside the state: green connected, red broken, gray otherwise.
    static func tone(_ c: HubChannel?) -> Color {
        switch c?.state {
        case "connected": Theme.positive
        case "error": Theme.danger
        default: .secondary
        }
    }

    /// The row's second line: the error, the apps' ends, the account, else what it is.
    static func subtitle(_ c: HubChannel) -> String {
        if c.state == "error", let e = c.error, !e.isEmpty { return e }
        if c.isApp, !c.surfaces.isEmpty { return c.surfaces.joined(separator: " · ") }
        if let name = c.account?.name, !name.isEmpty, !c.isMarket { return name }
        return c.description ?? ""
    }

    struct Section: Identifiable, Equatable {
        var title: String
        var footer: String
        var channels: [HubChannel]
        var id: String { title }
    }

    /// The list's three sections (the empty ones left out): the apps, the installed ones, those a market has.
    static func sections(_ list: [HubChannel]) -> [Section] {
        [Section(title: "Persona", footer: String(localized: "Wherever you talk to Persona, it's the same chat. Answer questions and approvals right on their cards."),
                 channels: list.filter(\.isApp)),
         Section(title: String(localized: "Connected [Channels Model fa1876]"), footer: String(localized: "Messages sent to the bot in these places go to Persona, and answers and agent questions go back there. Only the one bound person is recognized."),
                 channels: list.filter { !$0.isApp && !$0.isMarket }),
         Section(title: String(localized: "Available [Channels Model 6473b1]"), footer: String(localized: "Extensions in the extension market that add channels. Install one, then bind it here."), channels: list.filter(\.isMarket))]
            .filter { !$0.channels.isEmpty }
    }

    /// Settings' Channels row: how many are connected, and how many went wrong.
    static func summary(_ list: [HubChannel]?) -> String {
        guard let list else { return "" }
        let on = list.filter { !$0.isApp && $0.state == "connected" }.count
        let bad = list.filter { $0.state == "error" }.count
        return bad > 0 ? String(localized: "\(on) connected · \(bad) with errors") : String(localized: "\(on) connected")
    }

    static func hasProblem(_ list: [HubChannel]?) -> Bool { list?.contains { $0.state == "error" } ?? false }

    static func target(_ t: ChannelTarget?) -> String {
        switch t?.kind {
        case "new": String(localized: "New chat (starts with the next message)")
        case "conversation": t?.title?.isEmpty == false ? t!.title! : String(localized: "A chat")
        case "agent": t?.title?.isEmpty == false ? "Agent · " + t!.title! : String(localized: "An agent")
        default: String(localized: "Main Chat")
        }
    }

    /// What the channel can do, in a few words a line.
    static func capabilities(_ c: HubChannel.Capabilities) -> [String] {
        var out: [String] = []
        let takes = [String(localized: "text"), c.imagesIn == true ? String(localized: "images") : nil,
                     c.filesIn == true ? String(localized: "Files") : nil, c.voiceIn == true ? String(localized: "voice") : nil]
        let kinds = takes.compactMap { $0 }.joined(separator: String(localized: "、", comment: "Separator between list items"))
        out.append(String(localized: "Receives \(kinds)", comment: "What a channel receives: text, images, files, voice"))
        out.append(c.format == "plain" ? String(localized: "Answers are plain text") : String(localized: "Answers are formatted (Markdown)"))
        if let n = c.maxText, n > 0 { out.append(String(localized: "Up to \(n) characters per message; longer ones are split")) }
        if c.asks == "text" { out.append(String(localized: "Answer questions and approvals with a number or text")) } else if c.asks == "buttons" { out.append(String(localized: "Answer questions and approvals with buttons")) }
        if c.typing == true { out.append(String(localized: "Shows “typing” while working")) }
        if c.progress == true { out.append(String(localized: "Shows progress while working")) }
        return out
    }

    /// The bind section's heading and first button.
    static func bindTitle(_ c: HubChannel) -> String {
        switch c.bind {
        case "qr": String(localized: "Bind with QR Code")
        case "code": String(localized: "Pair")
        default: String(localized: "Bind")
        }
    }

    /// The settings page a channel's `settings_url` opens; nil for one this app does not know.
    static func page(_ url: String?, manager: String? = nil) -> SettingsPage? {
        guard let url, url.hasPrefix("settings/") else { return nil }
        let rest = url.dropFirst("settings/".count)
        switch rest {
        case "sent", "connectors/-sent": return .sent
        case "connectors": return .connectors
        case "connectors/feishu-bot": return .feishuBot
        case "connectors/feishu": return .feishu()
        case "model-config": return .modelConfiguration
        default:
            // settings/extensions/<id>, or settings/plugins/<id> as hubs from before the rename write it (#1844).
            for prefix in ["extensions/", "plugins/"] where rest.hasPrefix(prefix) {
                let id = String(rest.dropFirst(prefix.count))
                if !id.isEmpty, !id.contains("/") { return .plugin(id, manager: manager) }
            }
            if rest.hasPrefix("connectors/"), case let id = String(rest.dropFirst("connectors/".count)), !id.isEmpty, !id.contains("/") {
                return .connector(id, manager: manager)
            }
            return nil
        }
    }

    /// The symbol of a channel without a brand's mark, by its kind.
    static func symbol(_ c: HubChannel) -> String {
        switch c.kind {
        case "app": Theme.Icon.ai
        case "voice": Theme.Icon.voiceChannel
        case "mention": Theme.Icon.mention
        default: Theme.Icon.chatBot
        }
    }

    /// A channel's brand mark (`Theme.Brand`): Feishu's for the Feishu bot and /agent; nil for the rest (WeChat's mark
    /// and its terms are undecided, as on the web).
    static func brand(_ id: String) -> String? {
        switch id {
        case "feishu", "lark_agent": Theme.Brand.lark
        case "telegram": Theme.Brand.telegram
        default: nil
        }
    }
}

/// The hub's channels as the conversations need them (names and marks for 「来自 …」), fetched once per hub and again
/// when the settings change one. Empty on a hub without `channels`.
@Observable final class ChannelDirectory {
    static let shared = ChannelDirectory()

    private(set) var list: [HubChannel] = []
    @ObservationIgnored private var key: String?
    @ObservationIgnored private var inflight: Task<Void, Never>?

    /// Loads the list for this hub once (a hub without `channels`: nothing, the marks never show).
    func ensure(_ api: APIClient?, features: Set<String>) {
        guard let api, features.contains("channels") else {
            if !list.isEmpty { list = [] }
            key = nil
            return
        }
        let k = api.config.url.absoluteString + "|" + api.group
        guard key != k, inflight == nil else { return }
        inflight = Task { @MainActor in
            defer { inflight = nil }
            guard let l = try? await api.channels() else { return }
            list = l
            key = k
        }
    }

    /// The settings changed a channel (or one came or went): taken again next time.
    func forget() { key = nil }

    func update(_ l: [HubChannel]) { list = l }
}

/// Settings › Channels: the list.
@Observable final class ChannelsModel {
    struct Deps {
        var api: () -> APIClient?
        static var live: Deps { .init(api: { AppModel.shared.api }) }
    }

    private(set) var channels: [HubChannel]?
    private(set) var loading = false
    private(set) var error: String?
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var context: APIClient?
    @ObservationIgnored private var read = 0
    private(set) var sections: [ChannelText.Section] = []

    init(deps: Deps = .live) { self.deps = deps }

    func load() async {
        guard let api = deps.api()?.currentAssistantSettings() else { return }
        if !api.sameSettingsScope(as: context) { channels = nil; sections = []; error = nil }
        context = api
        read += 1
        let revision = read
        loading = true
        defer { if revision == read { loading = false } }
        do {
            let l = try await api.channels()
            guard revision == read, !Task.isCancelled, api.sameSettingsScope(as: deps.api()) else { return }
            channels = l
            sections = ChannelText.sections(l)
            error = nil
        } catch {
            guard revision == read, !Task.isCancelled, api.sameSettingsScope(as: deps.api()) else { return }
            self.error = error.localizedDescription
        }
    }
}

/// Settings › Channels › one: its switch, where its messages go, its features, unbinding and a test message. Binding a
/// step at a time is `ConnectorSignInModel` on the channel's bind (`ChannelModel.binding`).
@Observable final class ChannelModel {
    struct Deps {
        var api: () -> APIClient?
        static var live: Deps { .init(api: { AppModel.shared.api }) }
    }

    let id: String
    let manager: String?
    private(set) var detail: HubChannelDetail?
    private(set) var loading = false
    private(set) var busy = false
    private(set) var error: String?
    /// What the test message did, for people.
    private(set) var testResult: String?
    @ObservationIgnored private let deps: Deps

    init(id: String, manager: String? = nil, deps: Deps = .live) {
        self.id = id
        self.manager = manager
        self.deps = deps
    }

    private var api: APIClient? { deps.api()?.manager(manager) }

    var channel: HubChannel? { detail?.channel }

    /// The sign-in steps' deps for this channel's binding: the same steps as a connector's, on POST …/bind.
    static func bindingDeps(_ deps: Deps, report: @escaping (Error) -> Void) -> ConnectorDeps {
        ConnectorDeps(api: deps.api, report: report, login: { api, id, step, values in
            try await api.bindChannel(id, step: step, values: values)
        })
    }

    func load(quietly: Bool = false) async {
        guard !loading else { return }
        if !quietly { loading = true }
        defer { loading = false }
        await run { api in try await api.channel(self.id) }
    }

    func setEnabled(_ on: Bool) async {
        await change { api in try await api.updateChannel(self.id, enabled: on) }
    }

    func setTarget(_ t: ChannelTarget) async {
        await change { api in try await api.updateChannel(self.id, target: t) }
    }

    func setFeature(_ fid: String, on: Bool) async {
        await change { api in try await api.updateChannel(self.id, features: [fid: on]) }
    }

    func unbind() async {
        await change { api in try await api.unbindChannel(self.id) }
    }

    func test() async {
        guard let api else { return }
        busy = true
        defer { busy = false }
        do {
            try await api.testChannel(id)
            let place = channel?.name ?? String(localized: "the other end", comment: "The channel, when its name is unknown")
            testResult = String(localized: "Sent. Check \(place) to see if it arrived.")
        } catch {
            let why = error.shown ?? error.localizedDescription
            testResult = String(localized: "Couldn't send: \(why)")
        }
    }

    private func change(_ call: @escaping (APIClient) async throws -> HubChannelDetail) async {
        busy = true
        defer { busy = false }
        if await run(call) { ChannelDirectory.shared.forget() }
    }

    @discardableResult private func run(_ call: @escaping (APIClient) async throws -> HubChannelDetail) async -> Bool {
        do {
            guard let api else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            detail = try await call(api)
            error = nil
            return true
        } catch {
            self.error = error.shown ?? error.localizedDescription
            return false
        }
    }
}
