import SwiftUI

// Standing authorizations (#7002; the web's Settings › Assistant › Standing authorizations, #4509): go-aheads the owner
// gave for future tasks on one computer and folder, which Persona then answers for them until they are revoked. The
// list reads `GET /api/v1/assistant/standing-authorizations`, a revoke sends `DELETE …/{id}` after a confirmation. The
// hub has no route to restore one (a new grant is made only through the main chat's approval), so there is no Undo.

/// One grant as the hub lists it. Every field may be missing; an odd one is left out without the rest.
nonisolated struct StandingAuthorization: Decodable, Hashable, Identifiable, Sendable {
    var id: String
    var machineID: String
    var directory: String
    var actions: [String]
    var conditions: [String]
    /// The owner's own words when they granted it.
    var quote: String
    /// Where the grant came from, in the hub's words.
    var source: String
    var grantedAt: Date?
    /// Whether the authorized work can be undone (not whether the grant can be revoked: every one can).
    var reversible: Bool

    enum CodingKeys: String, CodingKey {
        case id, directory, actions, conditions, quote, source, reversible
        case machineID = "machine_id", grantedAt = "granted_at"
    }

    init(id: String, machineID: String = "", directory: String = "", actions: [String] = [], conditions: [String] = [],
         quote: String = "", source: String = "", grantedAt: Date? = nil, reversible: Bool = false) {
        self.id = id
        self.machineID = machineID
        self.directory = directory
        self.actions = actions
        self.conditions = conditions
        self.quote = quote
        self.source = source
        self.grantedAt = grantedAt
        self.reversible = reversible
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = (try? c.decodeIfPresent(String.self, forKey: .id)).flatMap { $0 } ?? ""
        machineID = (try? c.decodeIfPresent(String.self, forKey: .machineID)).flatMap { $0 } ?? ""
        directory = (try? c.decodeIfPresent(String.self, forKey: .directory)).flatMap { $0 } ?? ""
        actions = (try? c.decodeIfPresent([String].self, forKey: .actions)).flatMap { $0 } ?? []
        conditions = (try? c.decodeIfPresent([String].self, forKey: .conditions)).flatMap { $0 } ?? []
        quote = (try? c.decodeIfPresent(String.self, forKey: .quote)).flatMap { $0 } ?? ""
        source = (try? c.decodeIfPresent(String.self, forKey: .source)).flatMap { $0 } ?? ""
        grantedAt = (try? c.decodeIfPresent(Date.self, forKey: .grantedAt)).flatMap { $0 }
        reversible = (try? c.decodeIfPresent(Bool.self, forKey: .reversible)).flatMap { $0 } ?? false
    }

    /// The folder by its name, as the owner knows it ("weather"); the whole path on the detail page.
    var folderName: String {
        let name = (directory as NSString).lastPathComponent
        return name.isEmpty ? directory : name
    }
}

nonisolated struct StandingAuthorizationList: Decodable, Sendable {
    var revision: Int
    var authorizations: [StandingAuthorization]

    enum CodingKeys: String, CodingKey { case revision, authorizations }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        revision = (try? c.decodeIfPresent(Int.self, forKey: .revision)).flatMap { $0 } ?? 0
        // A grant without an id can't be revoked, so it isn't listed.
        authorizations = ((try? c.decodeIfPresent([StandingAuthorization].self, forKey: .authorizations)).flatMap { $0 } ?? [])
            .filter { !$0.id.isEmpty }
    }
}

extension APIClient {
    /// The current assistant's standing authorizations. 403: this device has no full access.
    func standingAuthorizations() async throws -> StandingAuthorizationList {
        try await get("/api/v1/assistant/standing-authorizations", as: StandingAuthorizationList.self)
    }

    /// Revokes one. The hub answers `{"ok":true,"revision"}`, also for one that is gone already.
    func revokeStandingAuthorization(_ id: String) async throws {
        let part = id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#"))) ?? id
        _ = try await request("DELETE", "/api/v1/assistant/standing-authorizations/\(part)")
    }
}

/// The words for a grant: the web's labels (`settings-standing` in the web app), and a sentence for the list's row.
enum StandingAuthorizationText {
    /// An action as a label of its own ("Merge changes"); one this build doesn't know stays the hub's word.
    static func action(_ action: String) -> String {
        switch action {
        case "merge": String(localized: "Merge changes [Standing Authorization]")
        case "push": String(localized: "Push changes [Standing Authorization]")
        case "deploy-test": String(localized: "Deploy to a test environment [Standing Authorization]")
        case "deploy-production": String(localized: "Deploy to production [Standing Authorization]")
        case "send": String(localized: "Send messages [Standing Authorization]")
        case "delete": String(localized: "Delete data [Standing Authorization]")
        case "spend": String(localized: "Spend money [Standing Authorization]")
        default: action
        }
    }

    /// An action inside a sentence ("merge changes").
    private static func phrase(_ action: String) -> String {
        switch action {
        case "merge": String(localized: "merge changes [Standing Authorization phrase]")
        case "push": String(localized: "push changes [Standing Authorization phrase]")
        case "deploy-test": String(localized: "deploy to a test environment [Standing Authorization phrase]")
        case "deploy-production": String(localized: "deploy to production [Standing Authorization phrase]")
        case "send": String(localized: "send messages [Standing Authorization phrase]")
        case "delete": String(localized: "delete data [Standing Authorization phrase]")
        case "spend": String(localized: "spend money [Standing Authorization phrase]")
        default: "“\(action)”"
        }
    }

    /// What the grant lets Persona do, inside a sentence: "merge changes and deploy to a test environment".
    static func actionsPhrase(_ grant: StandingAuthorization) -> String {
        grant.actions.map(phrase).formatted(.list(type: .and))
    }

    /// The same as a line of its own: "Merge changes and deploy to a test environment".
    static func actions(_ grant: StandingAuthorization) -> String {
        let list = actionsPhrase(grant)
        guard let first = list.first else { return String(localized: "No actions recorded [Standing Authorization]") }
        return first.uppercased() + list.dropFirst()
    }

    /// A condition in words; one this build doesn't know (the owner's own) stays as written.
    static func condition(_ condition: String) -> String {
        switch condition {
        case "ci_green": String(localized: "All checks pass [Standing Authorization]")
        case "no_conflicts": String(localized: "No merge conflicts [Standing Authorization]")
        default: condition
        }
    }

    /// Where a grant came from, in the app's language. The hub keeps it as words, not a code: the sentence it writes
    /// itself for a suggested rule confirmed with Always (`assistant-rule_suggest.source`, in the owner's language when it
    /// was made), or what the assistant wrote ("Main conversation"). Those it knows in en, zh-Hans or ja are worded
    /// again here; anything else stays as written.
    static func source(_ raw: String) -> String {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if let n = suggestedRuleCount(text) {
            return String(localized: "Suggested rule: the same answer \(n) times in 14 days, confirmed with Always. [Standing Authorization source]")
        }
        let key = text.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: ".。 "))
        switch key {
        case "main conversation", "the main conversation", "main chat", "the main chat",
             "主对话", "主会话", "主聊天", "メインチャット", "メインの会話", "メイン会話": // i18n-ignore: the words a grant's source may hold
            return String(localized: "Main chat [Standing Authorization source]")
        case "conversation", "a conversation", "chat", "a chat", "对话", "会话", "会話", "チャット": // i18n-ignore: the words a grant's source may hold
            return String(localized: "A chat [Standing Authorization source]")
        case "settings", "设置", "設定": // i18n-ignore: the words a grant's source may hold
            return String(localized: "Settings [Standing Authorization source]")
        default:
            return text
        }
    }

    /// The count in the hub's suggested-rule sentence, in any of its three languages; nil for other words.
    private static func suggestedRuleCount(_ text: String) -> Int? {
        let patterns = [
            #"^Suggested rule: the same answer (\d+) times in 14 days, confirmed with Always\.$"#,
            #"^建议的规则：14 天内 (\d+) 次相同的回答，点「总是」确认。$"#, // i18n-ignore: the hub's sentence
            #"^提案されたルール：14 日間に同じ回答が (\d+) 回あり、「常に」で確認されました。$"#, // i18n-ignore: the hub's sentence
        ]
        for pattern in patterns {
            if let match = text.firstMatch(of: try! Regex(pattern)), let n = match.output[1].substring.flatMap({ Int($0) }) { return n }
        }
        return nil
    }

    /// "In weather on MacBook".
    static func place(_ grant: StandingAuthorization, machine: String) -> String {
        String(localized: "In \(grant.folderName) on \(machine) [Standing Authorization]")
    }

    /// "Only when all checks pass and no merge conflicts", nil without conditions.
    static func conditions(_ grant: StandingAuthorization) -> String? {
        guard !grant.conditions.isEmpty else { return nil }
        let list = grant.conditions.map(condition).formatted(.list(type: .and))
        return String(localized: "Only when: \(list) [Standing Authorization]")
    }

    static func granted(_ date: Date?) -> String? {
        date.map { String(localized: "Since \($0.formatted(date: .abbreviated, time: .omitted)) [Standing Authorization]") }
    }

    /// What revoking changes, for its confirmation: the scope, that done work stays, how to grant it again.
    static func revokeMessage(_ grant: StandingAuthorization, machine: String) -> String {
        String(localized: "Persona will ask you again before it does this in \(grant.folderName) on \(machine): \(actionsPhrase(grant)). Work already done isn't affected. To grant it again, ask Persona in the main chat.")
    }
}

/// The page's state and its two requests (MVVM, `AssistantDeps`), so tests run it against a `FakeHub`. It keeps the client
/// of the assistant it first read for: a revoke after another assistant became current still goes to the one shown.
@Observable @MainActor final class StandingAuthorizationsModel {
    typealias Deps = AssistantDeps

    enum Failure: Equatable {
        /// This device has no full access (403): the hub lists none to it.
        case forbidden
        case unavailable(String)
    }

    /// nil until the first read answers.
    private(set) var grants: [StandingAuthorization]?
    /// Why there is no list; with one on screen a failed refresh says so in a toast instead.
    private(set) var failure: Failure?
    /// Revocations sent and not answered yet: a read meanwhile leaves them out, so a row doesn't come back while it goes.
    private(set) var revoking: Set<String> = []
    /// Grants this page revoked. A read the hub answered before a revoke landed still lists it; the hub never lists
    /// the same id again (an old id can't be added back), so the page leaves these out for as long as it is open.
    @ObservationIgnored private var revoked: Set<String> = []

    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var client: APIClient?
    /// The latest read: an older one that answers after it doesn't land.
    @ObservationIgnored private var reads = 0

    init(deps: Deps = .live) { self.deps = deps }

    private var api: APIClient? {
        if client == nil { client = deps.api()?.currentAssistantSettings() }
        return client
    }

    func load() async {
        guard let api else {
            failure = .unavailable(String(localized: "Not connected to the hub yet"))
            return
        }
        reads += 1
        let read = reads
        do {
            let list = try await api.standingAuthorizations()
            guard read == reads else { return }
            grants = list.authorizations.filter { !revoking.contains($0.id) && !revoked.contains($0.id) }
            failure = nil
        } catch {
            // The page leaving cancels the read: nothing to show.
            guard read == reads, !error.isCancellation else { return }
            if (error as? APIError)?.status == 403 {
                grants = nil
                failure = .forbidden
                return
            }
            let text = error.shown ?? String(localized: "Can't reach the hub")
            if grants != nil {
                deps.toast(Toast(String(localized: "Didn't refresh: \(text)"), style: .error))
            } else {
                failure = .unavailable(text)
            }
        }
    }

    /// Takes the grant off the list at once and has the hub revoke it; a failure puts it back where it was and says it
    /// is still in effect. The caller has confirmed.
    func revoke(_ grant: StandingAuthorization) async {
        guard let api, var list = grants, let at = list.firstIndex(where: { $0.id == grant.id }), !revoking.contains(grant.id) else { return }
        list.remove(at: at)
        grants = list
        revoking.insert(grant.id)
        defer { revoking.remove(grant.id) }
        do {
            try await api.revokeStandingAuthorization(grant.id)
            revoked.insert(grant.id)
            deps.toast(Toast(String(localized: "Authorization revoked"), style: .success))
        } catch {
            if var now = grants, !now.contains(where: { $0.id == grant.id }) {
                now.insert(grant, at: min(at, now.count))
                grants = now
            }
            deps.toast(Toast(String(localized: "Couldn't revoke this authorization. It's still in effect. Check the connection and try again."), style: .error))
        }
    }
}


/// Assistant Profile › Standing Authorizations: each grant in plain words; a swipe, its menu or its page revokes it,
/// always after a confirmation.
struct StandingAuthorizationsView: View {
    @State private var page = StandingAuthorizationsModel()
    /// The grant whose revoke is being confirmed from the list.
    @State private var confirming: StandingAuthorization?

    var body: some View {
        List {
            if let grants = page.grants {
                if grants.isEmpty {
                    EmptyStateView(symbol: Theme.Icon.standingAuthorization, title: String(localized: "None yet"),
                                   message: String(localized: "Persona asks you before it merges, pushes, deploys, sends messages, deletes data or spends money. When you tell it in a chat to always go ahead somewhere, that go-ahead is listed here."))
                        .listRowBackground(Color.clear)
                        .accessibilityIdentifier("standing.empty")
                } else {
                    Section {
                        ForEach(grants) { grant in
                            NavigationLink {
                                StandingAuthorizationDetail(grant: grant) { revoked in Task { await page.revoke(revoked) } }
                            } label: {
                                StandingAuthorizationRow(grant: grant)
                            }
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                Button { confirming = grant } label: {
                                    Label(String(localized: "Revoke [Standing Authorizations]"), systemImage: Theme.Icon.revoke)
                                }
                                .tint(Theme.danger)
                            }
                            .contextMenu {
                                Button(role: .destructive) { confirming = grant } label: {
                                    Label(String(localized: "Revoke… [Standing Authorizations]"), systemImage: Theme.Icon.revoke)
                                }
                            }
                            .accessibilityIdentifier("standing.\(grant.id)")
                        }
                    } footer: {
                        Text("Persona does these without asking you first, until you revoke them. Swipe left on one to revoke it.")
                    }
                }
            } else if let failure = page.failure {
                switch failure {
                case .forbidden:
                    EmptyStateView(symbol: Theme.Icon.locked, title: String(localized: "Needs full access [Standing Authorizations]"),
                                   message: String(localized: "Only a device with full access can see and revoke standing authorizations."))
                        .listRowBackground(Color.clear)
                        .accessibilityIdentifier("standing.forbidden")
                case .unavailable(let text):
                    // A button, not only pull to refresh: the Mac's list has no pull.
                    EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Couldn't load standing authorizations"), message: text,
                                   action: (title: String(localized: "Retry [Components b8784c]"), run: { Task { await page.load() } }))
                        .listRowBackground(Color.clear)
                        .accessibilityIdentifier("standing.failed")
                }
            } else {
                ProgressView().frame(maxWidth: .infinity).listRowBackground(Color.clear)
            }
        }
        .animation(Theme.Motion.layout, value: page.grants)
        .themedPage()
        .pageTitle(String(localized: "Standing Authorizations"))
        .task { await page.load() }
        .refreshable { await page.load() }
        .standingRevokeConfirmation($confirming) { grant in Task { await page.revoke(grant) } }
    }
}

/// A grant in the list: what it allows, where, under which conditions and since when.
private struct StandingAuthorizationRow: View {
    var grant: StandingAuthorization

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(StandingAuthorizationText.actions(grant)).font(Theme.Font.body)
            Text(StandingAuthorizationText.place(grant, machine: TaskPlace.machineName(grant.machineID)))
                .font(Theme.Font.caption).foregroundStyle(.secondary)
            let more = [StandingAuthorizationText.conditions(grant), StandingAuthorizationText.granted(grant.grantedAt)].compactMap { $0 }
            if !more.isEmpty {
                Text(more.joined(separator: " · ")).font(Theme.Font.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, Theme.Space.xxs)
        .accessibilityElement(children: .combine)
    }
}

/// One grant in full: what, where, under which conditions, the owner's words, since when and for how long, and Revoke.
private struct StandingAuthorizationDetail: View {
    var grant: StandingAuthorization
    /// Revokes it; the page closes first, so the list shows the row leaving.
    var revoke: (StandingAuthorization) -> Void
    @State private var confirming: StandingAuthorization?
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        SettingsForm {
            Section {
                ForEach(Array(grant.actions.enumerated()), id: \.offset) { _, action in
                    Text(StandingAuthorizationText.action(action))
                }
            } header: {
                Text("Persona may do this without asking")
            }
            Section {
                LabeledContent(String(localized: "Computer [Standing Authorization]"), value: TaskPlace.machineName(grant.machineID))
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Folder [Standing Authorization]")
                    Text(grant.directory).font(Theme.Font.code).foregroundStyle(.secondary).textSelection(.enabled)
                }
                .accessibilityElement(children: .combine)
            } header: {
                Text("Where [Standing Authorization]")
            }
            Section {
                if grant.conditions.isEmpty {
                    Text("No conditions [Standing Authorization]").foregroundStyle(.secondary)
                } else {
                    ForEach(Array(grant.conditions.enumerated()), id: \.offset) { _, condition in
                        Text(StandingAuthorizationText.condition(condition))
                    }
                }
            } header: {
                Text("Only when [Standing Authorization]")
            } footer: {
                Text("Persona checks these itself each time. If it can't, it asks you as usual.")
            }
            if !grant.quote.isEmpty {
                Section {
                    Text(grant.quote).textSelection(.enabled)
                } header: {
                    Text("Your words [Standing Authorization]")
                }
            }
            Section {
                if let at = grant.grantedAt {
                    LabeledContent(String(localized: "Granted [Standing Authorization]"), value: at.formatted(date: .abbreviated, time: .shortened))
                }
                LabeledContent(String(localized: "Lasts [Standing Authorization]"), value: String(localized: "Until you revoke it"))
                if !grant.source.isEmpty {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text("From [Standing Authorization]")
                        Text(StandingAuthorizationText.source(grant.source)).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .combine)
                }
                LabeledContent(String(localized: "Work Can Be Undone"),
                               value: grant.reversible ? String(localized: "Yes [Standing Authorization]") : String(localized: "No [Standing Authorization]"))
            } footer: {
                Text("Whether what Persona does with this can be undone. The authorization itself can always be revoked.")
            }
            Section {
                FormAction {
                    Button(role: .destructive) { confirming = grant } label: {
                        Text("Revoke Authorization…").lineLimit(1).minimumScaleFactor(0.8).formActionWidth()
                    }
                    .accessibilityIdentifier("standing.revoke")
                }
            }
        }
        .themedGroupedPage()
        .pageTitle(String(localized: "Standing Authorization"))
        .navigationBarTitleDisplayMode(.inline)
        .standingRevokeConfirmation($confirming) { grant in
            dismiss()
            revoke(grant)
        }
    }
}

/// Asks before a revoke: what stops, that done work stays, how to grant it again; a destructive button and Cancel.
private struct StandingRevokeConfirmation: ViewModifier {
    @Binding var grant: StandingAuthorization?
    var revoke: (StandingAuthorization) -> Void

    func body(content: Content) -> some View {
        content.confirmationDialog(String(localized: "Revoke this authorization?"),
                                   isPresented: Binding(get: { grant != nil }, set: { if !$0 { grant = nil } }),
                                   titleVisibility: .visible, presenting: grant) { grant in
            Button(String(localized: "Revoke Authorization"), role: .destructive) { revoke(grant) }
                .accessibilityIdentifier("standing.confirm")
            Button(String(localized: "Cancel"), role: .cancel) {}
        } message: { grant in
            Text(StandingAuthorizationText.revokeMessage(grant, machine: TaskPlace.machineName(grant.machineID)))
        }
    }
}

private extension View {
    func standingRevokeConfirmation(_ grant: Binding<StandingAuthorization?>, revoke: @escaping (StandingAuthorization) -> Void) -> some View {
        modifier(StandingRevokeConfirmation(grant: grant, revoke: revoke))
    }
}
