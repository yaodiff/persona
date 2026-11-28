import SwiftUI

/// "Group Access" on the Lark Bot page (#16848; the web's FeishuGroupAccess.tsx): who besides the owner may @ the bot in a
/// group, the groups and people that may, the people and groups that never may, the read-only tools a guest may use, and
/// how many questions one group may put in an hour. A hub without the route (an older one) sends nothing, and nothing is
/// drawn.
struct FeishuGroupAccessSections: View {
    var model: FeishuGroupAccessModel

    var body: some View {
        Group {
            if let access = model.access, access.paired {
                modeSection(access)
                if access.policy.mode != .ownerOnly { groupsSection(access) }
                if access.policy.mode == .allowlist { peopleSection(access) }
                if !access.recentRequests.isEmpty { requestsSection(access) }
                blockedSection(access)
                if access.policy.mode != .ownerOnly { toolsSection(access) }
            } else if let e = model.loadError {
                Section(String(localized: "Group Access")) {
                    LoadProblem(text: e, labeled: false)
                    Button { Task { await model.load() } } label: { Text("Try Again [Group Access]").lineLimit(1) }
                }
            }
        }
        .sheet(item: Binding(get: { model.adding }, set: { model.adding = $0 })) { kind in
            FeishuGroupAccessAddSheet(model: model, kind: kind)
        }
    }

    // MARK: Sections

    private func modeSection(_ access: FeishuGroupAccess) -> some View {
        Section {
            Picker(selection: Binding(get: { access.policy.mode }, set: { mode in Task { await model.setMode(mode) } })) {
                ForEach(FeishuGroupAccessMode.allCases, id: \.self) { Text(Self.modeName($0)).tag($0) }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Who Can Ask the Bot in Groups")
                    Text(FeishuGroupAccessText.modeDetail(access.policy.mode)).font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            .disabled(model.saving)
            if access.policy.mode != .ownerOnly {
                Text("Guests get a separate assistant with none of your data, your tasks or your computers. It can use only the tools you turn on below.")
                    .font(Theme.Font.caption).foregroundStyle(Theme.cautionText)
            }
            Picker(selection: Binding(get: { access.policy.perChatHourly }, set: { n in Task { await model.setPerChatHourly(n) } })) {
                ForEach(FeishuGroupAccessText.hourlyChoices(access.policy.perChatHourly), id: \.self) { Text("\($0)").tag($0) }
            } label: {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text("Questions per Group per Hour")
                    Text("The most questions one group can put to the bot in an hour, from everyone in it.")
                        .font(Theme.Font.caption).foregroundStyle(.secondary)
                }
            }
            .disabled(model.saving)
            if let e = model.error, !e.isEmpty {
                LoadProblem(text: e, labeled: false)
            }
        } header: {
            Text("Group Access")
        } footer: {
            Text("Who besides you may @ the bot in a group, and what they may use. Blocked people and groups are refused in every mode.")
        }
    }

    private func groupsSection(_ access: FeishuGroupAccess) -> some View {
        memberSection(title: String(localized: "Allowed Groups"), footer: String(localized: "The bot answers guests only in these groups."),
                      empty: String(localized: "No group allowed yet."), rows: access.policy.allowedChats.map { ($0.chatID, $0.name) },
                      full: access.policy.allowedChats.count >= access.limits.listMax, add: .allowedGroup) { id in
            Task { await model.removeAllowedGroup(id) }
        }
    }

    private func peopleSection(_ access: FeishuGroupAccess) -> some View {
        memberSection(title: String(localized: "Allowed People"), footer: String(localized: "Only these people can @ the bot in an allowed group."),
                      empty: String(localized: "No one allowed yet."), rows: access.policy.allowedUsers.map { ($0.openID, $0.name) },
                      full: access.policy.allowedUsers.count >= access.limits.listMax, add: .allowedPerson) { id in
            Task { await model.removeAllowedPerson(id) }
        }
    }

    private func requestsSection(_ access: FeishuGroupAccess) -> some View {
        Section {
            ForEach(access.recentRequests) { r in requestRow(r) }
        } header: {
            Text("Recent Requests")
        } footer: {
            Text("People who @ the bot where you have not allowed them. Allow or block them here.")
        }
    }

    private func blockedSection(_ access: FeishuGroupAccess) -> some View {
        Section {
            memberRows(label: String(localized: "People [Group Access]"), rows: access.policy.blockedUsers.map { ($0.openID, $0.name) },
                       full: access.policy.blockedUsers.count >= access.limits.listMax, add: .blockedPerson) { id in
                Task { await model.removeBlockedPerson(id) }
            }
            memberRows(label: String(localized: "Groups [Group Access]"), rows: access.policy.blockedChats.map { ($0.chatID, $0.name) },
                       full: access.policy.blockedChats.count >= access.limits.listMax, add: .blockedGroup) { id in
                Task { await model.removeBlockedGroup(id) }
            }
        } header: {
            Text("Blocked")
        } footer: {
            Text("Blocked always wins, in every mode.")
        }
    }

    private func toolsSection(_ access: FeishuGroupAccess) -> some View {
        Section {
            if access.availableTools.isEmpty {
                Text("No read-only connector tools are installed").font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            ForEach(access.availableTools) { tool in
                let on = access.policy.guestTools.contains(tool.name)
                SettingsSwitchRow(title: FeishuGroupAccessText.toolLabel(tool), symbol: Theme.Icon.plugins, iconColor: Theme.accent,
                                  isOn: Binding(get: { on }, set: { next in Task { await model.setTool(tool.name, on: next) } }))
                .disabled(model.saving || (!on && access.policy.guestTools.count >= access.limits.guestToolsMax))
                .accessibilityIdentifier("group-access-tool-\(tool.name)")
            }
        } header: {
            Text("Guest Tools")
        } footer: {
            Text("Read-only connector tools a guest may use to answer. Tools that change anything are never offered.")
        }
    }

    // MARK: Rows

    /// A list of people or groups in a section of its own: the rows, swipe to remove, and Add.
    private func memberSection(title: String, footer: String, empty: String, rows: [(String, String?)], full: Bool,
                               add: FeishuGroupAccessModel.AddKind, remove: @escaping (String) -> Void) -> some View {
        Section {
            memberRows(label: nil, rows: rows, empty: empty, full: full, add: add, remove: remove)
        } header: {
            Text(title)
        } footer: {
            Text(footer)
        }
    }

    private func memberRows(label: String?, rows: [(String, String?)], empty: String? = nil, full: Bool,
                            add: FeishuGroupAccessModel.AddKind, remove: @escaping (String) -> Void) -> some View {
        Group {
            if let label {
                Text(label).font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            if rows.isEmpty, let empty {
                Text(empty).font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            ForEach(rows, id: \.0) { id, name in
                memberRow(id: id, name: name)
                    .swipeActions {
                        Button(role: .destructive) { remove(id) } label: {
                            Label(String(localized: "Remove \(FeishuGroupAccessText.title(name: name, id: id))"), systemImage: Theme.Icon.delete)
                        }
                    }
            }
            Button { model.adding = add } label: {
                Label(Self.addTitle(add), systemImage: Theme.Icon.add).lineLimit(1)
            }
            .disabled(model.saving || full)
            .accessibilityIdentifier("group-access-add-\(Self.addID(add))")
        }
    }

    private func memberRow(id: String, name: String?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(FeishuGroupAccessText.title(name: name, id: id)).font(Theme.Font.body).lineLimit(1)
            if let name, !name.isEmpty {
                Text(id).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
        }
        .accessibilityIdentifier("group-access-member-\(id)")
    }

    private func requestRow(_ r: FeishuGroupRequest) -> some View {
        let who = FeishuGroupAccessText.title(name: r.name, id: r.openID)
        return HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(who).font(Theme.Font.body).lineLimit(1)
                Text(FeishuGroupAccessText.title(name: r.chatName, id: r.chatID)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: Theme.Space.s)
            Menu {
                Button { Task { await model.allow(r) } } label: { Label("Allow", systemImage: Theme.Icon.done) }
                Button(role: .destructive) { Task { await model.block(r) } } label: { Label("Block", systemImage: Theme.Icon.close) }
            } label: {
                Label(String(localized: "Actions for \(who)"), systemImage: Theme.Icon.more).labelStyle(.iconOnly)
            }
            .disabled(model.saving)
            .accessibilityIdentifier("group-access-request-\(r.openID)")
        }
    }

    private static func modeName(_ mode: FeishuGroupAccessMode) -> String {
        switch mode {
        case .ownerOnly: String(localized: "Only Me")
        case .allowlist: String(localized: "People I Allow")
        case .everyoneInAllowedChats: String(localized: "Everyone in Allowed Groups")
        }
    }

    private static func addTitle(_ kind: FeishuGroupAccessModel.AddKind) -> String {
        switch kind {
        case .allowedGroup, .blockedGroup: String(localized: "Add Group")
        case .allowedPerson, .blockedPerson: String(localized: "Add Person")
        }
    }

    private static func addID(_ kind: FeishuGroupAccessModel.AddKind) -> String {
        switch kind {
        case .allowedGroup: "group"
        case .allowedPerson: "person"
        case .blockedGroup: "blocked-group"
        case .blockedPerson: "blocked-person"
        }
    }
}

/// The sheet that adds one group or one person: an id (oc_… or ou_…) and an optional name the owner sees.
private struct FeishuGroupAccessAddSheet: View {
    var model: FeishuGroupAccessModel
    var kind: FeishuGroupAccessModel.AddKind
    @State private var id = ""
    @State private var name = ""
    @Environment(\.dismiss) private var dismiss

    private var group: Bool { kind == .allowedGroup || kind == .blockedGroup }
    private var valid: Bool { FeishuGroupAccessText.validID(id) != nil }

    var body: some View {
        NavigationStack {
            SettingsForm {
                Section {
                    TextField(group ? String(localized: "Group ID") : String(localized: "Open ID"), text: $id, prompt: Text(group ? "oc_…" : "ou_…"))
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        .font(Theme.Font.code)
                        .accessibilityIdentifier("group-access-id")
                    TextField(String(localized: "Name (Optional)"), text: $name)
                        .accessibilityIdentifier("group-access-name")
                } footer: {
                    Text(group ? String(localized: "The group's id, as Lark shows it, and a name so you recognize it later.")
                        : String(localized: "The person's open id, and a name so you recognize them later."))
                }
            }
            .themedPage()
            .navigationTitle(group ? String(localized: "Add Group") : String(localized: "Add Person"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                SheetClose { dismiss() }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        Task { _ = await model.add(id: id, name: name) }
                    } label: {
                        if model.saving { ProgressView() } else { Text("Add").lineLimit(1) }
                    }
                    .disabled(!valid || model.saving)
                    .accessibilityIdentifier("group-access-confirm")
                }
            }
        }
    }
}

extension FeishuGroupAccessModel.AddKind: Identifiable {
    var id: String {
        switch self {
        case .allowedGroup: "allowed-group"
        case .allowedPerson: "allowed-person"
        case .blockedGroup: "blocked-group"
        case .blockedPerson: "blocked-person"
        }
    }
}
