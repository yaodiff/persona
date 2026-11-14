#if os(iOS)
import SwiftUI

/// Settings › Knowledge Selection (`KnowledgeSelectionModel`): the chats and documents with a setting of their own
/// first (they come before every rule), then the chat rules and the document rules, a section per priority level.
/// A row opens its options in a sheet; a level's menu reorders it. Every change is saved at once.
struct KnowledgeSelectionView: View {
    @Environment(AppModel.self) private var app
    @State private var model: KnowledgeSelectionModel?
    /// Organize knowledge points by topic (#7251), the page's first group.
    @State private var topics: KnowledgeTopicsModel?
    @State private var editingRule: RuleRef?
    @State private var editingItem: ItemRef?
    @State private var picking: PickRef?

    var body: some View {
        SettingsForm {
            if let topics { KnowledgeTopicsSection(model: topics) }
            if let error = model?.error {
                Section { LoadProblem(text: error) }
            }
            switch model?.unsupported {
            case .hub?: notice(unsupportedText(.hub))
            case .document?: notice(unsupportedText(.document))
            case nil: EmptyView()
            }
            if let selection = model?.selection {
                Group {
                    itemsSection(selection)
                    levels(.messages, selection)
                    levels(.docs, selection)
                }
                .disabled(model?.saving == true)
            }
        }
        .overlay { if model?.selection == nil && model?.error == nil && model?.unsupported == nil { ProgressView() } }
        .themedGroupedPage()
        .detailTitle(String(localized: "Knowledge Selection"))
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $editingRule) { ref in
            if let model {
                NavigationStack {
                    KnowledgeRuleEditor(part: ref.part, name: ref.name, opened: ref.rule, problem: editorProblem) {
                        await model.setRule(ref.name, in: ref.part, to: $0)
                    }
                }
            }
        }
        .sheet(item: $editingItem) { ref in
            if let model {
                NavigationStack {
                    KnowledgeItemEditor(opened: ref.item, isNew: ref.isNew, inSheet: true, problem: editorProblem,
                                        save: model.setItem, remove: remove) { editingItem = nil }
                }
            }
        }
        .sheet(item: $picking) { ref in
            if let model {
                NavigationStack { KnowledgeItemPicker(kind: ref.kind, model: model, saveProblem: editorProblem, remove: remove) }
            }
        }
        .task {
            if model == nil { model = KnowledgeSelectionModel(deps: deps) }
            if topics == nil {
                let api = app.api?.currentAssistantSettings()
                topics = KnowledgeTopicsModel(deps: AssistantDeps(api: { api }, report: { app.show($0) }, toast: { app.toast = $0 },
                                                                  open: { app.openAssistantConversation($0) }))
            }
            await topics?.load()
            await model?.load()
            #if DEBUG
            // `-openKnowledgeSelection rule | chat | doc` opens the page with that sheet up (screenshots).
            switch UserDefaults.standard.string(forKey: "openKnowledgeSelection") {
            case "rule" where editingRule == nil && model?.selection != nil:
                if let rule = model?.selection?[.messages].rules["mentions"] {
                    editingRule = RuleRef(part: .messages, name: "mentions", rule: rule)
                }
            case "chat" where picking == nil && model?.selection != nil: picking = PickRef(kind: .chat)
            case "doc" where picking == nil && model?.selection != nil: picking = PickRef(kind: .doc)
            default: break
            }
            #endif
        }
        .refreshable { await model?.load() }
    }

    private var deps: KnowledgeSelectionModel.Deps {
        let api = app.api?.currentAssistantSettings()
        let connected: () throws -> APIClient = {
            guard let api else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
            return api
        }
        let load: () async throws -> KBSelection = { try await connected().kbSelection() }
        let save: (KBSelection) async throws -> KBSelection = { try await connected().setKBSelection($0) }
        let entries: (KBSelectionItem.Kind) async throws -> [KBEntry] = { kind in
            try await connected().kbList(kind == .chat ? .chats : .docs).items
        }
        let meta: (KBSelectionItem.Kind, String) async throws -> JSONValue? = { kind, id in
            try await connected().kbDetail(kind == .chat ? KBKind.chats.rawValue : KBKind.docs.rawValue, id: id).detail.meta
        }
        return .init(load: load, save: save, entries: entries, meta: meta)
    }

    /// Removing a setting is undone from the toast: it goes back as it was.
    private func remove(_ item: KBSelectionItem) async {
        guard let model, await model.removeItem(item) else { return }
        app.toast = Toast(String(localized: "Setting removed"), style: .success, action: Toast.Action(title: String(localized: "Undo")) {
            Task { _ = await model.setItem(item) }
        })
    }

    private func notice(_ text: String) -> some View {
        Section {
            Label(text, systemImage: Theme.Icon.info).font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
    }

    private func unsupportedText(_ unsupported: KnowledgeSelectionModel.Unsupported) -> String {
        switch unsupported {
        case .hub:
            String(localized: "This hub doesn't have the knowledge selection yet. Update the hub, then choose here what it keeps.")
        case .document:
            String(localized: "This hub's selection has settings this version of the app doesn't know. Update the app to change it here; nothing was changed.")
        }
    }

    /// The reason a save did not finish, shown inside the sheet that is still holding the draft.
    private var editorProblem: String? {
        if let error = model?.error { return error }
        guard let unsupported = model?.unsupported else { return nil }
        return unsupportedText(unsupported)
    }

    // MARK: Per-item settings

    private func itemsSection(_ selection: KBSelection) -> some View {
        Section {
            ForEach(selection.items ?? [], id: \.key) { item in itemRow(item) }
            Button { picking = PickRef(kind: .chat) } label: {
                HStack(spacing: Theme.Space.s) {
                    ThemedIcon(Theme.Icon.add)
                    Text(String(localized: "Add Chat…")).lineLimit(1).minimumScaleFactor(0.5)
                }
            }
            .accessibilityIdentifier("knowledge-selection-add-chat")
            Button { picking = PickRef(kind: .doc) } label: {
                HStack(spacing: Theme.Space.s) {
                    ThemedIcon(Theme.Icon.add)
                    Text(String(localized: "Add Document…")).lineLimit(1).minimumScaleFactor(0.5)
                }
            }
            .accessibilityIdentifier("knowledge-selection-add-doc")
        } header: {
            Text(String(localized: "Individual Chats and Documents"))
        } footer: {
            Text(String(localized: "A setting here comes before every rule below. Removing it puts the chat or document back under the rules."))
        }
    }

    private func itemRow(_ item: KBSelectionItem) -> some View {
        Button { editingItem = ItemRef(item: item, isNew: false) } label: {
            HStack(spacing: Theme.Space.m) {
                ThemedIcon(item.kind == .chat ? KBKind.chats.symbol : KBKind.docs.symbol)
                    .foregroundStyle(.secondary)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(item.name ?? item.id).lineLimit(1)
                    Text(item.source).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: Theme.Space.s)
                Text(KnowledgeSelectionText.itemValue(item)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
            }
            .contentShape(.rect)
        }
        .foregroundStyle(.primary)
        .accessibilityIdentifier("knowledge-selection-item-\(item.id)")
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) { Task { await remove(item) } } label: {
                Label(String(localized: "Remove Setting"), systemImage: Theme.Icon.delete)
            }
        }
        .contextMenu {
            Button(role: .destructive) { Task { await remove(item) } } label: {
                Label(String(localized: "Remove Setting"), systemImage: Theme.Icon.delete)
            }
        }
    }

    // MARK: Rules

    @ViewBuilder private func levels(_ part: KBSelection.Part, _ selection: KBSelection) -> some View {
        let order = selection[part].order
        ForEach(Array(order.enumerated()), id: \.offset) { index, level in
            Section {
                ForEach(level, id: \.self) { name in
                    if let rule = selection[part].rules[name] { ruleRow(part, name, rule) }
                }
            } header: {
                levelHeader(part, index: index, level: level, count: order.count)
            } footer: {
                if index == order.count - 1 { Text(footer(part)) }
            }
        }
    }

    private func levelHeader(_ part: KBSelection.Part, index: Int, level: [String], count: Int) -> some View {
        HStack(spacing: Theme.Space.s) {
            Text(levelTitle(part, index: index))
            Spacer(minLength: Theme.Space.s)
            Menu {
                levelMenu(part, index: index, level: level, count: count)
            } label: {
                ThemedIcon(Theme.Icon.moreMenu).tapTargetFrame(alignment: .trailing)
            }
            .accessibilityLabel(String(localized: "Reorder Priority \(index + 1)"))
            .accessibilityIdentifier("knowledge-selection-\(part.rawValue)-level-\(index + 1)")
        }
    }

    /// The first level of a part names the part too, so the two lists of levels read apart.
    private func levelTitle(_ part: KBSelection.Part, index: Int) -> String {
        guard index == 0 else { return String(localized: "Priority \(index + 1)") }
        return part == .messages ? String(localized: "Chat Rules · Priority 1") : String(localized: "Document Rules · Priority 1")
    }

    @ViewBuilder private func levelMenu(_ part: KBSelection.Part, index: Int, level: [String], count: Int) -> some View {
        if let first = level.first {
            Button { Task { await model?.moveLevel(of: first, in: part, by: -1) } } label: {
                Label(String(localized: "Move Up"), systemImage: Theme.Icon.up)
            }
            .disabled(index == 0)
            Button { Task { await model?.moveLevel(of: first, in: part, by: 1) } } label: {
                Label(String(localized: "Move Down"), systemImage: Theme.Icon.down)
            }
            .disabled(index == count - 1)
        }
        if index > 0 {
            Divider()
            ForEach(level, id: \.self) { name in
                Button { Task { await model?.regroup(name, in: part, joinPrevious: true) } } label: {
                    Label(String(localized: "Merge \(KnowledgeSelectionText.ruleTitle(name, in: part)) into Priority \(index)"), systemImage: Theme.Icon.merged)
                }
            }
        }
        if level.count > 1 {
            Divider()
            ForEach(level, id: \.self) { name in
                Button { Task { await model?.regroup(name, in: part, joinPrevious: false) } } label: {
                    Label(String(localized: "Give \(KnowledgeSelectionText.ruleTitle(name, in: part)) Its Own Level"), systemImage: Theme.Icon.splitDown)
                }
            }
        }
    }

    private func ruleRow(_ part: KBSelection.Part, _ name: String, _ rule: KBSelectionRule) -> some View {
        Button { editingRule = RuleRef(part: part, name: name, rule: rule) } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(KnowledgeSelectionText.ruleTitle(name, in: part))
                    if let detail = KnowledgeSelectionText.ruleDetail(name, in: part) {
                        Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                Text(KnowledgeSelectionText.ruleValue(rule)).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
            }
            .contentShape(.rect)
        }
        .foregroundStyle(.primary)
        .accessibilityIdentifier("knowledge-selection-\(part.rawValue)-\(name)")
    }

    private func footer(_ part: KBSelection.Part) -> String {
        switch part {
        case .messages:
            String(localized: "Levels are checked from the top. The first level with a matching rule decides: the message is stored if any matching rule there is on, and not stored if they are all off. Lower levels are not checked. Turning a rule off only stops new messages; what is stored stays until its days run out.")
        case .docs:
            String(localized: "Documents follow the same order. Only days you set here add a deletion date; with none, documents are kept as before.")
        }
    }
}

private struct RuleRef: Identifiable, Hashable {
    var part: KBSelection.Part
    var name: String
    /// Keep the opened rule so an unsupported response cannot turn the presented sheet into an empty page.
    var rule: KBSelectionRule
    var id: String { part.rawValue + "/" + name }
}

private struct ItemRef: Identifiable, Hashable {
    var item: KBSelectionItem
    var isNew: Bool
    var id: String { item.key }
}

private struct PickRef: Identifiable, Hashable {
    var kind: KBSelectionItem.Kind
    var id: String { kind.rawValue }
}

/// A number a rule or a per-item setting keeps: its name, then a field that is empty for the hub's default.
private struct KnowledgeNumberField: View {
    var title: String
    var prompt: String
    var accessibilityID: String
    @Binding var text: String

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(title)
            TextField(title, text: $text, prompt: Text(prompt))
                .keyboardType(.numberPad)
                .multilineTextAlignment(.leading)
                .monospacedDigit()
                .accessibilityIdentifier(accessibilityID)
        }
    }
}

/// One rule's options: whether it stores what it matches, for how many days, and what only it has (mentions: whether
/// an @all counts; your messages: how many around each are kept with it). Nothing is saved until Save.
private struct KnowledgeRuleEditor: View {
    @Environment(\.dismiss) private var dismiss
    let part: KBSelection.Part
    let name: String
    let opened: KBSelectionRule
    let problem: String?
    var save: (KBSelectionRule) async -> Bool
    @State private var on: Bool
    @State private var days: String
    @State private var attachmentDays: String
    @State private var includeAll: Bool
    @State private var context: String
    @State private var saving = false

    init(part: KBSelection.Part, name: String, opened: KBSelectionRule, problem: String?,
         save: @escaping (KBSelectionRule) async -> Bool) {
        self.part = part
        self.name = name
        self.opened = opened
        self.problem = problem
        self.save = save
        _on = State(initialValue: opened.on)
        _days = State(initialValue: opened.days.map(String.init) ?? "")
        _attachmentDays = State(initialValue: opened.attachmentDays.map(String.init) ?? "")
        _includeAll = State(initialValue: opened.includeAll ?? true)
        _context = State(initialValue: opened.context.map(String.init) ?? "")
    }

    private var countsAll: Bool { part == .messages && name == "mentions" }
    private var hasContext: Bool { part == .messages && name == "mine" }

    /// The rule as the fields have it; nil while a number is out of range. A value the user did not touch stays as
    /// the hub sent it (an @all that was never set stays unset).
    private var draft: KBSelectionRule? {
        let kept = KnowledgeSelectionText.number(days, in: KBSelection.dayRange)
        let attachments = KnowledgeSelectionText.number(attachmentDays, in: KBSelection.dayRange)
        let around = KnowledgeSelectionText.number(context, in: KBSelection.contextRange)
        guard kept.isValid, attachments.isValid, around.isValid else { return nil }
        var rule = opened
        rule.on = on
        rule.days = kept.value
        if part == .messages { rule.attachmentDays = attachments.value }
        if countsAll, includeAll != (opened.includeAll ?? true) { rule.includeAll = includeAll }
        if hasContext { rule.context = around.value }
        return rule
    }

    var body: some View {
        SettingsForm {
            if let problem {
                Section { LoadProblem(text: problem) }
            }
            Section {
                Toggle(String(localized: "Store What Matches"), isOn: $on)
                    .accessibilityIdentifier("knowledge-rule-on")
            } footer: {
                Text(KnowledgeSelectionText.ruleDetail(name, in: part) ?? "")
            }
            Section {
                KnowledgeNumberField(title: String(localized: "Days to Keep Text"), prompt: String(localized: "Default [Prompt Composer 844b8c]"),
                                     accessibilityID: "knowledge-rule-days", text: $days)
                if part == .messages {
                    KnowledgeNumberField(title: String(localized: "Days to Keep Attachments"), prompt: String(localized: "Default [Prompt Composer 844b8c]"),
                                         accessibilityID: "knowledge-rule-attachment-days", text: $attachmentDays)
                }
            } footer: {
                Text(part == .messages
                     ? String(localized: "1 to 36500 days. Empty uses the hub's retention settings. Attachments are never kept longer than their text.")
                     : String(localized: "1 to 36500 days. Empty adds no deletion date."))
            }
            if countsAll {
                Section {
                    Toggle(String(localized: "Count @all as a Mention"), isOn: $includeAll)
                        .accessibilityIdentifier("knowledge-rule-include-all")
                }
            }
            if hasContext {
                Section {
                    KnowledgeNumberField(title: String(localized: "Messages Kept Around Yours"), prompt: "50",
                                         accessibilityID: "knowledge-rule-context", text: $context)
                } footer: {
                    Text(String(localized: "0 to 200 messages before and after each one you send, so your own make sense later. Empty is 50."))
                }
            }
            if draft == nil {
                Section { LoadProblem(text: String(localized: "Use whole numbers in the ranges above, or leave a field empty.")) }
            }
        }
        .themedGroupedPage()
        .navigationTitle(KnowledgeSelectionText.ruleTitle(name, in: part))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: draft != opened) { dismiss() }
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button { Task { await persist() } } label: {
                    if saving { ProgressView() } else { Text(String(localized: "Save")).lineLimit(1) }
                }
                .disabled(draft == nil || draft == opened || saving)
                .accessibilityIdentifier("knowledge-rule-save")
            }
        }
        .interactiveDismissDisabled(saving)
    }

    private func persist() async {
        guard let draft else { return }
        saving = true
        defer { saving = false }
        if await save(draft) {
            Haptics.success()
            dismiss()
        }
    }
}

/// One chat's or document's own setting: not read at all, left to the rules, or stored whole (then for how many days).
/// Nothing is saved until Save (Add for a new one).
private struct KnowledgeItemEditor: View {
    let opened: KBSelectionItem
    let isNew: Bool
    /// Its own sheet (Cancel in the bar) rather than pushed from the picker (the bar's back).
    let inSheet: Bool
    let problem: String?
    var save: (KBSelectionItem) async -> Bool
    var remove: (KBSelectionItem) async -> Void
    /// Closes the sheet it is in.
    var close: () -> Void
    @State private var mode: KBSelectionItem.Mode
    @State private var days: String
    @State private var saving = false

    init(opened: KBSelectionItem, isNew: Bool, inSheet: Bool, problem: String?, save: @escaping (KBSelectionItem) async -> Bool,
         remove: @escaping (KBSelectionItem) async -> Void, close: @escaping () -> Void) {
        self.opened = opened
        self.isNew = isNew
        self.inSheet = inSheet
        self.problem = problem
        self.save = save
        self.remove = remove
        self.close = close
        _mode = State(initialValue: opened.mode)
        _days = State(initialValue: opened.days.map(String.init) ?? "")
    }

    /// The setting as the fields have it; nil while the days are out of range.
    private var draft: KBSelectionItem? {
        let kept = KnowledgeSelectionText.number(days, in: KBSelection.dayRange)
        guard mode != .all || kept.isValid else { return nil }
        var item = opened
        item.mode = mode
        if mode == .all { item.days = kept.value }
        return item
    }

    var body: some View {
        SettingsForm {
            if let problem {
                Section { LoadProblem(text: problem) }
            }
            Section {
                LabeledContent(String(localized: "Source"), value: opened.source)
            } header: {
                Text(opened.name ?? opened.id)
            }
            Section {
                Picker(String(localized: "What to Store"), selection: $mode) {
                    ForEach(KBSelectionItem.Mode.allCases, id: \.self) { m in
                        Text(KnowledgeSelectionText.modeTitle(m, kind: opened.kind)).tag(m)
                    }
                }
                .pickerStyle(.inline)
                .labelsHidden()
                .accessibilityIdentifier("knowledge-item-mode")
            } footer: {
                Text(KnowledgeSelectionText.modeDetail(mode, kind: opened.kind))
            }
            if mode == .all {
                Section {
                    KnowledgeNumberField(title: String(localized: "Days to Keep"), prompt: String(localized: "Default [Prompt Composer 844b8c]"),
                                         accessibilityID: "knowledge-item-days", text: $days)
                } footer: {
                    if draft == nil {
                        Text(String(localized: "Use a whole number from 1 to 36500, or leave it empty.")).foregroundStyle(Theme.danger)
                    }
                }
            }
            if !isNew {
                Section {
                    Button(role: .destructive) {
                        Task {
                            await remove(opened)
                            close()
                        }
                    } label: {
                        Label(String(localized: "Remove Setting"), systemImage: Theme.Icon.delete).lineLimit(1)
                    }
                    .accessibilityIdentifier("knowledge-item-remove")
                }
            }
        }
        .themedGroupedPage()
        .navigationTitle(opened.kind == .chat ? String(localized: "Chat Setting") : String(localized: "Document Setting"))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: draft != opened, when: inSheet) { close() }
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button { Task { await persist() } } label: {
                    if saving {
                        ProgressView()
                    } else {
                        Text(isNew ? String(localized: "Add [Beautiful U I Bridge 7a8a11]") : String(localized: "Save")).lineLimit(1)
                    }
                }
                .disabled(draft == nil || (!isNew && draft == opened) || saving)
                .accessibilityIdentifier("knowledge-item-save")
            }
        }
        .interactiveDismissDisabled(saving)
    }

    private func persist() async {
        guard let draft else { return }
        saving = true
        defer { saving = false }
        if await save(draft) {
            Haptics.success()
            close()
        }
    }
}

/// Add Chat… / Add Document…: the knowledge base's chats or documents by name; picking one opens its setting. A chat
/// or document the knowledge base has nothing of yet is not listed.
private struct KnowledgeItemPicker: View {
    @Environment(\.dismiss) private var dismiss
    let kind: KBSelectionItem.Kind
    let model: KnowledgeSelectionModel
    let saveProblem: String?
    var remove: (KBSelectionItem) async -> Void
    @State private var entries: [KBEntry]?
    @State private var query = ""
    @State private var problem: String?
    /// The entry whose source identity is being read.
    @State private var opening: String?
    @State private var picked: ItemRef?

    private var shown: [KBEntry] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let entries, !q.isEmpty else { return entries ?? [] }
        return entries.filter { $0.name.localizedCaseInsensitiveContains(q) || $0.id.localizedCaseInsensitiveContains(q) }
    }

    var body: some View {
        List {
            if let problem {
                Section { LoadProblem(text: problem) }
            }
            Section {
                ForEach(shown) { entry in row(entry) }
            } footer: {
                if entries?.isEmpty == false {
                    Text(kind == .chat
                         ? String(localized: "Chats the knowledge base already has something of. Mail, issues and pull requests are always stored.")
                         : String(localized: "Documents the knowledge base already has."))
                }
            }
        }
        .themedPage()
        .overlay {
            if entries == nil && problem == nil {
                ProgressView()
            } else if entries?.isEmpty == true {
                EmptyStateView(symbol: kind == .chat ? KBKind.chats.symbol : KBKind.docs.symbol,
                               title: kind == .chat ? String(localized: "No Chats Yet") : String(localized: "No Documents Yet"),
                               message: String(localized: "Once the knowledge base has stored something from one, you can give it a setting of its own here."))
            }
        }
        .pageSearchable(text: $query, prompt: String(localized: "Name"))
        .navigationTitle(kind == .chat ? String(localized: "Add Chat") : String(localized: "Add Document"))
        .navigationBarTitleDisplayMode(.inline)
        .sheetCancel(unsaved: false) { dismiss() }
        .navigationDestination(item: $picked) { ref in
            KnowledgeItemEditor(opened: ref.item, isNew: ref.isNew, inSheet: false, problem: saveProblem,
                                save: model.setItem, remove: remove) { dismiss() }
        }
        .task { await load() }
    }

    private func row(_ entry: KBEntry) -> some View {
        Button { Task { await pick(entry) } } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(entry.name.isEmpty ? entry.id : entry.name).lineLimit(1)
                    if let line = entry.oneLine, !line.isEmpty {
                        Text(line).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if opening == entry.id { ProgressView() }
            }
            .contentShape(.rect)
        }
        .foregroundStyle(.primary)
        .disabled(opening != nil)
        .accessibilityIdentifier("knowledge-pick-\(entry.id)")
    }

    private func load() async {
        guard entries == nil else { return }
        do {
            entries = try await model.entries(kind)
            problem = nil
        } catch {
            if !error.isCancellation {
                let why = error.shown ?? String(localized: "Can't reach the hub")
                problem = String(localized: "Couldn't load: \(why)")
            }
        }
    }

    private func pick(_ entry: KBEntry) async {
        opening = entry.id
        defer { opening = nil }
        do {
            let item = try await model.item(for: entry, kind: kind)
            problem = nil
            Haptics.tap()
            picked = ItemRef(item: item, isNew: model.selection?.item(key: item.key) == nil)
        } catch {
            if !error.isCancellation { problem = KnowledgeSelectionText.pickError(error) }
        }
    }
}
#endif
