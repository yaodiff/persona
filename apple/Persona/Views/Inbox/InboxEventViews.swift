import BeautifulUI
import SwiftUI

// Inbox phase 1 (docs/assistant/inbox.md): what an event adds to the inbox's screens — the row's mark and the detail's
// blocks. On iPhone and iPad they run from the current state and processing conversation through the AI summary, suggestion,
// background recap, chain and related items. The Mac keeps its own summary and its original state, recap, suggestion, chain
// and related order. The iPhone draws them as a page of their own (`InboxEventPage`, the source behind 'view original'); the
// Mac in the detail's action column (`MacInboxActions`), beside the source. An older hub's item without these fields never
// reaches them.

// MARK: - Row

/// What an open row says under its summary: the suggestion's kind and first line, In Progress's state, the 'preparing a suggestion' line
/// for an urgent one, or that the AI failed.
struct InboxEventMarkLine: View {
    var mark: InboxEventText.Mark

    var body: some View {
        switch mark {
        case .preparing:
            HStack(spacing: Theme.Space.sm) {
                InboxSpinner()
                Text("AI is preparing a suggestion").font(Theme.Font.labelStrong).foregroundStyle(Theme.ai).lineLimit(1)
            }
        case .working(let word):
            (Text(Image(systemName: Theme.Icon.inProgress)) + Text(" " + word))
                .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
        case .suggestion(let kind, let line, let symbol):
            (Text(Image(systemName: symbol)) + Text(" " + kind).fontWeight(.semibold) + Text("  " + line).foregroundStyle(.secondary))
                .font(Theme.Font.label).foregroundStyle(Theme.ai).lineLimit(1)
        case .failed(let words):
            (Text(Image(systemName: Theme.Icon.error)) + Text(" " + words))
                .font(Theme.Font.label).foregroundStyle(Theme.danger).lineLimit(1)
        }
    }
}

/// The small spinner of the inbox's rows, in the current style.
struct InboxSpinner: View {
    var body: some View {
        if AppStyle.current.usesBeautifulUI { BUISpinner(size: Theme.Size.dotXL) } else { ProgressView().controlSize(.mini) }
    }
}

/// The one rendering of `InboxNow.symbol(for:)`, used by both the list row and the event detail.
struct InboxNowSymbol: View {
    var symbol: InboxNow.Symbol?

    var body: some View {
        switch symbol {
        case .time?:
            ThemedIcon(Theme.Icon.time).font(Theme.Font.label).foregroundStyle(.secondary)
                .accessibilityIdentifier("inbox.now.symbol.time")
        case .spinner?:
            InboxSpinner().accessibilityIdentifier("inbox.now.symbol.spinner")
        case .needsYou?:
            ThemedIcon(Theme.Icon.needsYou).font(Theme.Font.label).foregroundStyle(StatusPalette.needsYou.text)
                .accessibilityIdentifier("inbox.now.symbol.needsYou")
        case .failed?:
            ThemedIcon(Theme.Icon.needsYou).font(Theme.Font.label).foregroundStyle(Theme.danger)
                .accessibilityIdentifier("inbox.now.symbol.failed")
        case nil:
            EmptyView()
        }
    }
}

// MARK: - Blocks

/// The event's blocks, one under another. With `showsSummary`, the order is state, summary, suggestion (or notice), recap,
/// chain and related items; without it, the Mac's original state, recap, suggestion (or notice), chain and related order stays.
/// `showsConversation` adds the processing conversation after the state when the hub supplies its session.
struct InboxEventBlocks: View {
    var item: InboxItem
    var model: InboxSuggestionModel
    /// The iPhone detail puts its AI summary below the state and its background recap after the suggestion.
    var showsSummary = false
    /// The iPhone detail also links to the processing conversation when the hub supplies one.
    var showsConversation = false
    /// The source is on screen beside them (the Mac): a link to just the source opens nothing more.
    var sourceShown = false
    /// Narrow (the Mac's strip over the item): recap cut to a few lines.
    var compact = false
    var edit: () -> Void
    /// How another item / the source at a message opens; nil pushes it on the surrounding stack (the Mac's column is
    /// beside the detail's stack, not in it).
    var openItem: ((InboxItem) -> Void)?
    var openSource: ((InboxSourceRoute) -> Void)?
    @Environment(AppModel.self) private var appModel
    @Environment(\.pushInboxChat) private var pushChat
    @State private var saying = false
    @State private var words = ""

    private var canSay: Bool { !item.isDone && item.currentSuggestion == nil }
    private var failed: Bool { canSay && (item.now == "failed" || item.handling?.state == "failed") }
    private var nowSymbol: InboxNow.Symbol? { InboxNow.symbol(for: item) }
    private var handlingConversationKey: SessionKey? {
        showsConversation ? Self.conversationKey(for: item) : nil
    }

    /// The processing conversation attached to the item, shared by the view and its tests.
    static func conversationKey(for item: InboxItem) -> SessionKey? {
        item.handling?.sessionKey
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.l) {
            statusAndInstruction
            if showsSummary {
                summary
                suggestionOrNotice
                recap
            } else {
                recap
                suggestionOrNotice
            }
            InboxChainSection(item: item, sourceShown: sourceShown, openItem: openItem, openSource: openSource)
            InboxRelatedSection(item: item, openItem: openItem)
        }
        #if DEBUG
        // `-showInboxSuggestionStale YES`: confirm the suggestion as first shown after 6 s (bump it on the mock hub
        // meanwhile, `/mock/inbox_events/bump`), for 「建议已更新」 screenshots.
        .task {
            guard UserDefaults.standard.bool(forKey: "showInboxSuggestionStale"), !compact, let s = item.currentSuggestion else { return }
            try? await Task.sleep(for: .seconds(6))
            await model.confirm(s)
        }
        #endif
    }

    /// The short AI summary, below the state rather than above the metadata on iPhone and iPad.
    @ViewBuilder private var summary: some View {
        if let s = item.summary, !s.isEmpty {
            (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + s))
                .font(Theme.Font.secondary)
                .lineHeight(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityIdentifier("inbox.summary")
        }
    }

    /// The manager's longer background recap, after the suggestion on iPhone and before it on the Mac.
    @ViewBuilder private var recap: some View {
        if let recap = item.recap, !recap.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                InboxBlockTitle(title: String(localized: "Recap"))
                Text(recap)
                    .font(Theme.Font.secondary)
                    .lineHeight(.secondary)
                    .lineLimit(compact ? 3 : nil)
                    .fixedSize(horizontal: false, vertical: !compact)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("inbox.recap")
        }
    }

    @ViewBuilder private var suggestionOrNotice: some View {
        if !item.isDone, let s = item.currentSuggestion {
            InboxSuggestionCard(item: item, suggestion: s, model: model, edit: edit)
                .id(s.version)
        } else if let notice = model.notice {
            Label(notice, systemImage: Theme.Icon.info).font(Theme.Font.captionMedium).foregroundStyle(Theme.caution).lineLimit(1)
        }
    }

    /// The processing state, its processing-conversation link, and the no-suggestion instruction in the same place as the
    /// web's status actions. Without a link, the status and instruction button keep their original shared row.
    @ViewBuilder private var statusAndInstruction: some View {
        if canSay {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: failed ? .top : .center, spacing: Theme.Space.s) {
                    status
                    Spacer(minLength: 0)
                    instructionButton
                }
                .disabled(model.busy != nil)
                if saying && !failed { instructionEditor }
                if let error = model.error {
                    Label(error, systemImage: Theme.Icon.error)
                        .font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        .accessibilityIdentifier("inbox.instruction.error")
                }
            }
        } else {
            status
        }
    }

    @ViewBuilder private var instructionButton: some View {
        if failed {
            InboxSuggestionButton(title: InboxEventText.tryAgain, symbol: Theme.Icon.retry, prominent: false,
                                  spinning: model.busy == "instruct") { instruct(InboxEventText.retryInstruction) }
                .accessibilityIdentifier("inbox.instruction.retry")
        } else if !saying {
            InboxSuggestionButton(title: InboxEventText.saySomething, symbol: Theme.Icon.instructed, prominent: false, spinning: false) {
                saying = true
            }
            .accessibilityIdentifier("inbox.instruction.say")
        }
    }

    /// The processing conversation opens through the same shell route as a chain's conversation.
    private func conversation(_ key: SessionKey) -> some View {
        Button {
            InboxEventOpen.session(key, manager: true, owner: item.manager, model: appModel, push: pushChat)
        } label: {
            HStack(spacing: Theme.Space.xs) {
                Text(String(localized: "Conversation"))
                    .lineLimit(1)
                    .fixedSize()
                ThemedIcon(Theme.Icon.disclosure)
            }
            .font(Theme.Font.captionMedium)
            .foregroundStyle(Theme.accent)
            .contentShape(.rect)
            .tapTargetOutset()
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("inbox.now.conversation")
    }

    /// The shared current-state sentence when the hub supplies a key; otherwise the old processing / done line.
    @ViewBuilder private var status: some View {
        if let words = InboxNow.text(for: item) {
            if let key = handlingConversationKey {
                statusWithConversation(key: key) {
                    InboxNowSymbol(symbol: nowSymbol)
                } content: {
                    Text(words)
                        .font(Theme.Font.secondary)
                        .lineHeight(.secondary)
                        .foregroundStyle(nowSymbol == .failed ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.secondary))
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("inbox.now")
            } else {
                HStack(alignment: .top, spacing: Theme.Space.sm) {
                    InboxNowSymbol(symbol: nowSymbol)
                    Text(words)
                        .font(Theme.Font.secondary)
                        .lineHeight(.secondary)
                        .foregroundStyle(nowSymbol == .failed ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.secondary))
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("inbox.now")
            }
        } else if let filed = InboxEventText.doneBy(item) {
            if let key = handlingConversationKey {
                statusWithConversation(key: key) {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.positive)
                } content: {
                    Text(filed).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.positive).lineLimit(1)
                }
            } else {
                Label(filed, systemImage: Theme.Icon.done).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.positive).lineLimit(1)
            }
        } else if !item.isDone, let mark = InboxEventText.mark(item) {
            switch mark {
            case .suggestion:
                if let key = handlingConversationKey { conversation(key) } // the card says the state
            case .failed(let words):
                if let key = handlingConversationKey {
                    statusWithConversation(key: key) {
                        ThemedIcon(Theme.Icon.error).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger)
                    } content: {
                        Text(words).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger).lineLimit(1)
                        if let why = item.handling?.error, !why.isEmpty {
                            Text(why).font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    }
                } else {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Label(words, systemImage: Theme.Icon.error).font(Theme.Font.secondaryStrong).foregroundStyle(Theme.danger).lineLimit(1)
                        if let why = item.handling?.error, !why.isEmpty {
                            Text(why).font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    }
                }
            default:
                if let key = handlingConversationKey {
                    markWithConversation(mark, key: key)
                } else {
                    InboxEventMarkLine(mark: mark)
                }
            }
        }
    }

    /// The conversation sits under the words, aligned with the text rather than the state symbol.
    @ViewBuilder private func statusWithConversation<Leading: View, Content: View>(
        key: SessionKey,
        @ViewBuilder leading: () -> Leading,
        @ViewBuilder content: () -> Content
    ) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.sm) {
            leading()
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                content()
                conversation(key)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    /// The older processing mark keeps its icon and words together when no conversation is present.
    @ViewBuilder private func markWithConversation(_ mark: InboxEventText.Mark, key: SessionKey) -> some View {
        switch mark {
        case .preparing:
            statusWithConversation(key: key) {
                InboxSpinner()
            } content: {
                Text("AI is preparing a suggestion").font(Theme.Font.labelStrong).foregroundStyle(Theme.ai).lineLimit(1)
            }
        case .working(let word):
            statusWithConversation(key: key) {
                ThemedIcon(Theme.Icon.inProgress).font(Theme.Font.label).foregroundStyle(.secondary)
            } content: {
                Text(word).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
        default:
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                InboxEventMarkLine(mark: mark)
                conversation(key)
            }
        }
    }

    /// The same input pattern as the suggestion editor, but version zero addresses an item with no suggestion.
    private var instructionEditor: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            TextField(String(localized: "Say a word, e.g. “Don't reply like that, ask about the time first”"), text: $words, axis: .vertical)
                .lineLimit(1...3)
                .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
                .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
                .disabled(model.busy != nil)
                .onSubmit { instruct(words) }
                .accessibilityIdentifier("inbox.instruction.text")
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                InboxSuggestionButton(title: String(localized: "Cancel"), prominent: false, spinning: false) {
                    words = ""
                    saying = false
                }
                .accessibilityIdentifier("inbox.instruction.cancel")
                InboxSuggestionButton(title: InboxEventText.saySomething, prominent: true, spinning: model.busy == "instruct") {
                    instruct(words)
                }
                .disabled(model.busy != nil || words.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("inbox.instruction.send")
            }
        }
        .accessibilityElement(children: .contain)
    }

    private func instruct(_ text: String) {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !words.isEmpty else { return }
        Task {
            guard let outcome = await model.instruct(.withoutSuggestion, words) else { return }
            switch outcome {
            case .done(_), .stale(_):
                self.words = ""
                saying = false
            case .failed:
                break // the error stays beside the recoverable words
            }
        }
    }
}

/// A block's small title (recap, chain, related items).
struct InboxBlockTitle: View {
    var title: String
    var body: some View {
        Text(title).font(Theme.Font.labelStrong).foregroundStyle(.secondary).lineLimit(1)
    }
}

// MARK: - Suggestion

/// The suggestion with its buttons right under it: send · edit · reject (a reply), start · edit · reject (an Agent),
/// got it · reject (a conclusion), confirm · edit · reject (one that needs judgment). edit opens `InboxSuggestionEditor`.
struct InboxSuggestionCard: View {
    var item: InboxItem
    var suggestion: InboxItem.Suggestion
    var model: InboxSuggestionModel
    var edit: () -> Void
    // A to-do suggestion's fields (#6480): its title, due day, All Day and time, sent as they stand
    @State private var todoTitle: String
    @State private var todoDay: Date?
    @State private var todoAllDay: Bool
    @State private var todoTime: Date

    init(item: InboxItem, suggestion: InboxItem.Suggestion, model: InboxSuggestionModel, edit: @escaping () -> Void) {
        self.item = item
        self.suggestion = suggestion
        self.model = model
        self.edit = edit
        let due = InboxDue.parts(suggestion.due)
        _todoTitle = State(initialValue: suggestion.text)
        _todoDay = State(initialValue: due.day)
        _todoAllDay = State(initialValue: due.time == nil)
        _todoTime = State(initialValue: due.time ?? Calendar.current.date(bySettingHour: 9, minute: 0, second: 0, of: .now) ?? .now)
    }

    var body: some View {
        let s = suggestion
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.sm) {
                Label(InboxEventText.kindTitle(s), systemImage: InboxEventText.symbol(s))
                    .font(Theme.Font.labelStrong).foregroundStyle(Theme.ai).lineLimit(1)
                Spacer(minLength: 0)
                if let at = s.at {
                    Text(LarkTime.short(at)).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize()
                }
            }
            if let notice = model.notice {
                Label(notice, systemImage: Theme.Icon.info).font(Theme.Font.captionMedium).foregroundStyle(Theme.caution).lineLimit(1)
                    .accessibilityIdentifier("inbox.suggestion.stale")
            }
            if s.kind == "todo" && !s.isHandback {
                InboxTodoFields(title: $todoTitle, day: $todoDay, allDay: $todoAllDay, time: $todoTime)
            } else {
                Text(s.text)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.primary)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            if let note = s.note, !note.isEmpty {
                Text(note).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(s.changes ?? [], id: \.self) { c in
                Label(DocChangeText.line(c, in: item.doc), systemImage: Theme.Icon.inProgress)
                    .font(Theme.Font.captionMedium).foregroundStyle(.secondary).lineLimit(1)
            }
            if let error = model.error {
                Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
            }
            buttons(s)
        }
        .padding(Theme.Space.m)
        .modifier(DraftSurface())
        .accessibilityIdentifier("inbox.suggestion")
    }

    /// Side by side, the main one last at the trailing edge (as the draft card had them); where that does not fit (the
    /// Mac's narrow action column) one under another, full width, the main one first.
    private func buttons(_ s: InboxItem.Suggestion) -> some View {
        let actions = InboxEventText.actions(s)
        return ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                ForEach(Array(actions.reversed().enumerated()), id: \.offset) { _, a in button(a, s, fill: false) }
            }
            VStack(spacing: Theme.Space.s) {
                ForEach(Array(actions.enumerated()), id: \.offset) { _, a in button(a, s, fill: true) }
            }
        }
        .disabled(model.busy != nil)
    }

    @ViewBuilder private func button(_ a: InboxEventText.Action, _ s: InboxItem.Suggestion, fill: Bool) -> some View {
        switch a {
        case .reject:
            InboxSuggestionButton(title: String(localized: "Reject"), prominent: false, spinning: model.busy == "reject", fill: fill) {
                Task { await model.reject(s) }
            }
            .accessibilityIdentifier("inbox.suggestion.reject")
        case .edit:
            InboxSuggestionButton(title: String(localized: "Edit [Inbox Event Views 37090f]"), prominent: false, spinning: false, fill: fill, perform: edit)
                .accessibilityIdentifier("inbox.suggestion.edit")
        case .confirm(let title):
            InboxSuggestionButton(title: title, prominent: true, spinning: model.busy == "confirm", fill: fill) {
                if s.kind == "todo" && !s.isHandback {
                    let due = InboxDue.string(day: todoDay, allDay: todoAllDay, time: todoTime)
                    Task { await model.confirm(s, text: todoTitle, due: due) }
                } else {
                    Task { await model.confirm(s) }
                }
            }
            .accessibilityIdentifier("inbox.suggestion.confirm")
        }
    }
}

/// One of the card's buttons: one line always, in the current style's buttons.
struct InboxSuggestionButton: View {
    var title: String
    var symbol: String? = nil
    var prominent: Bool
    var spinning: Bool
    /// As wide as the column (stacked buttons).
    var fill = false
    var perform: () -> Void

    var body: some View {
        let button = Button(action: perform) {
            HStack(spacing: Theme.Space.xs) {
                if spinning { InboxSpinner() }
                if let symbol { ThemedIcon(symbol).font(Theme.Font.label) }
                Text(title).lineLimit(1).fixedSize()
            }
            .frame(maxWidth: fill ? .infinity : nil)
        }
        if AppStyle.current.usesBeautifulUI {
            button.buttonStyle(.bui(prominent ? .accent : .secondary))
        } else {
            button.themedButton(prominent: prominent)
        }
    }
}

/// edit: the suggestion's words to change and confirm (send / start / confirm the changed text), or 'say a word' for the
/// manager instead (don't reply like that, ask about the time first), after which it suggests again.
struct InboxSuggestionEditor: View {
    @Environment(\.dismiss) private var dismiss
    var item: InboxItem
    var suggestion: InboxItem.Suggestion
    var model: InboxSuggestionModel
    @State private var text: String
    @State private var words = ""
    @FocusState private var editing: Bool

    init(item: InboxItem, suggestion: InboxItem.Suggestion, model: InboxSuggestionModel) {
        self.item = item
        self.suggestion = suggestion
        self.model = model
        _text = State(initialValue: suggestion.text)
    }

    /// send / start / confirm: the card's main button.
    private var confirmTitle: String {
        for a in InboxEventText.actions(suggestion) { if case .confirm(let t) = a { return t } }
        return String(localized: "Confirm")
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                if let note = suggestion.note, !note.isEmpty {
                    (Text(Image(systemName: Theme.Icon.ai)).foregroundStyle(Theme.ai) + Text(" " + note))
                        .font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(4)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, Theme.Space.l)
                        .padding(.vertical, Theme.Space.m)
                }
                TextEditor(text: $text)
                    .focused($editing)
                    .scrollContentBackground(.hidden)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.top, Theme.Space.s)
                    .disabled(model.busy != nil)
                    .accessibilityIdentifier("inbox.suggestion.text")
                if let error = model.error {
                    Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(.horizontal, Theme.Space.l).padding(.bottom, Theme.Space.sm)
                }
                Divider()
                HStack(spacing: Theme.Space.s) {
                    TextField(String(localized: "Say a word, e.g. “Don't reply like that, ask about the time first”"), text: $words, axis: .vertical)
                        .lineLimit(1...3)
                        .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
                        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card))
                        .onSubmit { instruct() }
                        .accessibilityIdentifier("inbox.suggestion.words")
                    Button(action: instruct) {
                        HStack(spacing: Theme.Space.xs) {
                            if model.busy == "instruct" { ProgressView().controlSize(.mini) }
                            Text("Tell AI").lineLimit(1).fixedSize()
                        }
                    }
                    .buttonStyle(.bordered)
                    .disabled(model.busy != nil || words.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .accessibilityIdentifier("inbox.suggestion.instruct")
                }
                .padding(.horizontal, Theme.Space.l)
                .padding(.vertical, Theme.Space.m)
            }
            .themedPage()
            .navigationTitle(InboxEventText.kindTitle(suggestion))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button {
                        let changed = text
                        Task { if await model.confirm(suggestion, text: changed).map(Self.closes) == true { dismiss() } }
                    } label: { Text(confirmTitle).lineLimit(1) }
                    .disabled(model.busy != nil || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .accessibilityIdentifier("inbox.suggestion.editConfirm")
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private func instruct() {
        let w = words
        Task { if await model.instruct(suggestion, w).map(Self.closes) == true { dismiss() } }
    }

    /// Done, or the suggestion moved on (the card says 「建议已更新」 and shows the new one): the editor closes. A failure
    /// keeps it open with the words, to try again.
    private static func closes(_ o: InboxSuggestionOutcome) -> Bool {
        if case .failed = o { return false }
        return true
    }
}

// MARK: - chain

nonisolated extension InboxEventText {
    /// The newest entry for each linked Agent session, across the whole chain rather than only its visible rows.
    internal static func liveStateEntries(_ chain: [InboxItem.ChainEntry]) -> Set<String> {
        var latest: [SessionKey: String] = [:]
        for e in chain where e.kind == "agent" || e.kind == "agent_stopped" {
            guard let key = e.link?.sessionKey else { continue }
            latest[key] = e.id
        }
        return Set(latest.values)
    }

    /// Plain reported detail has no expansion to reveal it, so it stays readable in full instead of being clipped.
    internal static func chainLineLimit(_ e: InboxItem.ChainEntry, open: Bool) -> Int? {
        open || (e.detailIsInLine && e.link == nil && (e.messages ?? []).isEmpty) ? nil : 2
    }
}

/// The chain: collapsed to the three newest entries with 'show all (N)'; each row time · source icon · words, and a tap goes
/// where its link says (the source at that message, a conversation, another item) or opens its detail in place.
struct InboxChainSection: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openURL) private var openURL
    @Environment(\.pushInboxChat) private var pushChat
    var item: InboxItem
    var sourceShown = false
    var openItem: ((InboxItem) -> Void)?
    var openSource: ((InboxSourceRoute) -> Void)?
    @State private var all = false
    @State private var opened: Set<String> = []
    /// The to-do a chain entry links (the row was added to to-dos, #6480), opened in a sheet.
    @State private var todo: TodoDetailRoute?
    /// The whole chain of an item listed with part of it (`InboxItem.chainCut`, #4742), read when the section appears.
    @State private var whole: [InboxItem.ChainEntry]?

    var body: some View {
        let chain = item.chainCut ? whole ?? item.chain ?? [] : item.chain ?? []
        let liveStateEntries = InboxEventText.liveStateEntries(chain)
        let count = max(chain.count, item.chainCount ?? 0)
        if !chain.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    InboxBlockTitle(title: String(localized: "Timeline [Inbox Event Views 49991a]"))
                    Spacer(minLength: 0)
                    if count > 3 {
                        Button(all ? String(localized: "Collapse") : String(localized: "Show all (\(count))")) {
                            withAnimation(Theme.Motion.layout) { all.toggle() }
                        }
                        .font(Theme.Font.captionMedium)
                        .lineLimit(1)
                        .buttonStyle(.borderless)
                        .accessibilityIdentifier("inbox.chain.all")
                    }
                }
                ForEach(InboxEventText.chainRows(chain, expanded: all)) { e in
                    entry(e, showsLiveState: liveStateEntries.contains(e.id))
                }
            }
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("inbox.chain")
            .task(id: CutChain(key: item.key, count: item.chainCount)) { await readWhole() }
            .sheet(item: $todo) { r in
                NavigationStack { TodoDetailView(todoID: r.id).macSheetDone { todo = nil } }
                #if os(macOS)
                .macSheet()
                #endif
            }
        }
    }

    private struct CutChain: Hashable {
        var key: String
        var count: Int?
    }

    /// A cut chain's entries from the item itself; on failure the newest entry stays, as listed.
    private func readWhole() async {
        guard item.chainCut, let api = model.api?.manager(model.itemManager(item.manager)) else { return }
        if let full = try? await api.inboxItem(item.id).chain, !Task.isCancelled { whole = full }
    }

    @ViewBuilder private func entry(_ e: InboxItem.ChainEntry, showsLiveState: Bool) -> some View {
        let target = InboxChainTarget.of(e, sourceShown: sourceShown)
        let open = opened.contains(e.id)
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                tappable(e, target) { line(e, target: target, open: open, showsLiveState: showsLiveState) }
                // An entry that goes somewhere and has messages too: they open here with the chevron.
                if target.navigates && e.expandable {
                    Button { toggle(e) } label: {
                        ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand)
                            .font(Theme.Font.labelHeavy).foregroundStyle(.secondary)
                            .scaledFrame(width: Theme.Size.tapTarget, alignment: .trailing)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(open ? String(localized: "Collapse") : String(localized: "Expand"))
                }
            }
            if open { expansion(e).transition(.opacity) }
        }
        .padding(.vertical, Theme.Space.xs)
        .accessibilityValue(e.id)
    }

    private func line(_ e: InboxItem.ChainEntry, target: InboxChainTarget, open: Bool, showsLiveState: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            Text(LarkTime.short(e.at)).font(Theme.Font.caption).foregroundStyle(.secondary).monospacedDigit().lineLimit(1).fixedSize()
            chainIcon(e)
            if e.kind == "reported" {
                Chip(text: InboxEventText.assistantSays, look: .neutral, compact: true)
                    .accessibilityIdentifier("inbox.chain.reported")
            }
            Text(InboxEventText.chainText(e))
                .font(Theme.Font.secondary)
                .foregroundStyle(e.kind == "reported" ? .secondary : .primary)
                .lineLimit(InboxEventText.chainLineLimit(e, open: open))
                .frame(maxWidth: .infinity, alignment: .leading)
            if showsLiveState, let key = e.link?.sessionKey {
                InboxChainAgentState(key: key)
            }
            switch target {
            case .inline:
                ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand).font(Theme.Font.labelHeavy).foregroundStyle(.tertiary)
            case .nothing:
                EmptyView()
            default:
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.tertiary)
            }
        }
        .contentShape(.rect)
    }

    @ViewBuilder private func chainIcon(_ e: InboxItem.ChainEntry) -> some View {
        if let brand = sourceBrand(e) {
            BrandMark(brand, side: Theme.Size.iconXS).foregroundStyle(tint(e))
        } else {
            ThemedIcon(InboxEventText.symbol(e))
                .font(Theme.Font.label)
                .foregroundStyle(tint(e))
                .scaledFrame(width: Theme.Size.iconXS)
        }
    }

    /// A `via` source id uses that source's own mark, not the item's by assumption; a generic source keeps its symbol.
    private func sourceBrand(_ e: InboxItem.ChainEntry) -> String? {
        guard InboxEventText.iconMeaning(e) == "source", let via = e.via else { return nil }
        return via == "lark" ? Theme.Brand.lark : ConnectorText.brand(via)
    }

    /// The row as what it opens: a page pushed on the stack, a conversation, a link; or itself expanding.
    @ViewBuilder private func tappable<L: View>(_ e: InboxItem.ChainEntry, _ target: InboxChainTarget, @ViewBuilder label: () -> L) -> some View {
        switch target {
        case .source(let position, let uid):
            let route = InboxSourceRoute(item: item, position: position, uid: uid)
            if let openSource {
                Button { openSource(route) } label: { label() }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("inbox.chain.source")
            } else {
                NavigationLink(value: route) { label() }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("inbox.chain.source")
            }
        case .url(let u):
            Button { openURL(u) } label: { label() }.buttonStyle(.plain)
        case .session(let key, let manager):
            Button { InboxEventOpen.session(key, manager: manager, owner: item.manager, model: model, push: pushChat) } label: { label() }
                .buttonStyle(.plain)
                .accessibilityIdentifier("inbox.chain.session")
        case .item(let id):
            if let it = InboxStore.shared.items.first(where: { $0.key == InboxItem.key(id: id, manager: item.manager) }) {
                if let openItem {
                    Button { openItem(it) } label: { label() }.buttonStyle(.plain)
                } else {
                    NavigationLink(value: it) { label() }.buttonStyle(.plain)
                }
            } else {
                label() // not in the inbox any more
            }
        case .todo(let id):
            Button { todo = TodoDetailRoute(id: id) } label: { label() }.buttonStyle(.plain)
                .accessibilityIdentifier("inbox.chain.todo")
        case .inline:
            Button { toggle(e) } label: { label() }.buttonStyle(.plain)
                .accessibilityIdentifier("inbox.chain.expand")
        case .nothing:
            label()
        }
    }

    private func toggle(_ e: InboxItem.ChainEntry) {
        withAnimation(opened.contains(e.id) ? Theme.Motion.conceal : Theme.Motion.reveal) {
            if opened.contains(e.id) { opened.remove(e.id) } else { opened.insert(e.id) }
        }
    }

    /// What opens in place: the batch's messages (who: what), or the detail (the tier and reason, the words).
    private func expansion(_ e: InboxItem.ChainEntry) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(Array((e.messages ?? []).enumerated()), id: \.offset) { _, m in
                Text(m.sender.map { String(localized: "\($0): \(m.text)", comment: "Who wrote it: what they wrote") } ?? m.text)
                    .font(Theme.Font.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let d = InboxEventText.expansionDetail(e) {
                Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(Theme.Space.s)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control))
    }

    private func tint(_ e: InboxItem.ChainEntry) -> Color {
        switch e.kind {
        case "failed", "rejected": Theme.danger
        case "confirmed", "sent", "concluded", "done": Theme.positive
        case "handling", "agent", "instructed": Theme.ai
        default: .secondary
        }
    }
}

/// One Agent step's live state, reading only its own session cell rather than the session list (performance P5).
private struct InboxChainAgentState: View {
    @Environment(AppModel.self) private var model
    var key: SessionKey

    var body: some View {
        if let session = model.session(model.inHub(key)) {
            Text(session.state.label)
                .font(Theme.Font.caption)
                .foregroundStyle(session.state.tone.text)
                .lineLimit(1)
                .fixedSize()
                .accessibilityIdentifier("inbox.chain.agentState")
        }
    }
}

/// related items: other items this one has to do with, each going its own way; a tap opens that item.
struct InboxRelatedSection: View {
    var item: InboxItem
    var openItem: ((InboxItem) -> Void)?

    var body: some View {
        let related = item.related ?? []
        if !related.isEmpty {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                InboxBlockTitle(title: String(localized: "Related"))
                ForEach(related) { r in
                    let label = HStack(spacing: Theme.Space.s) {
                        ThemedIcon(Theme.Icon.link).font(Theme.Font.label).foregroundStyle(.secondary)
                            .scaledFrame(width: Theme.Size.iconXS)
                        Text(r.title).font(Theme.Font.secondary).foregroundStyle(.primary).lineLimit(1)
                        Spacer(minLength: 0)
                        ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelHeavy).foregroundStyle(.tertiary)
                    }
                    .padding(.vertical, Theme.Space.xs)
                    .contentShape(.rect)
                    if let it = InboxStore.shared.items.first(where: { $0.key == InboxItem.key(id: r.id, manager: item.manager) }) {
                        if let openItem {
                            Button { openItem(it) } label: { label }.buttonStyle(.plain)
                        } else {
                            NavigationLink(value: it) { label }.buttonStyle(.plain)
                        }
                    } else {
                        label.opacity(0.6)
                    }
                }
            }
        }
    }
}

/// Opening a chain's conversation: the processing one is the assistant's (Persona's own conversations), a sub-session
/// an Agent's. On the iPhone's shell either is pushed over the item instead (`pushInboxChat`).
@MainActor enum InboxEventOpen {
    /// `owner`: the item's manager, which a conversation not listed yet is read through (`InboxChatRoute`, or the
    /// Persona page's `assistantItemManagers`).
    static func session(_ key: SessionKey, manager: Bool, owner: String?, model: AppModel, push: ((InboxChatRoute) -> Void)? = nil) {
        if let push {
            push(InboxChatRoute(key: model.inHub(key), manager: owner))
        } else if manager && model.session(key) == nil {
            model.openAssistantConversation(model.inHub(key), manager: owner)
        } else {
            model.open(key)
        }
    }
}

/// A conversation an inbox item links to (the AI's handling, an Agent it started), pushed over the item. `manager`: the
/// item's, which a conversation not listed yet is read for.
struct InboxChatRoute: Hashable {
    var key: SessionKey
    var manager: String?
}

extension EnvironmentValues {
    /// The iPhone shell's Assistant stack: a conversation an inbox item links to goes on top of the item, so Back comes
    /// back to it (#3114). Nil elsewhere, where the conversation opens on its own page.
    @Entry var pushInboxChat: ((InboxChatRoute) -> Void)? = nil
}

// MARK: - view original

/// The item's source page (the Feishu chat, the mail, what a plugin draws), at a chain entry's message.
struct InboxSourceRoute: Hashable {
    var item: InboxItem
    var position: String?
    var uid: UInt32?
}

/// The source as the inbox detail drew it before events: 'view original', a chain entry's link.
struct InboxSourcePage: View {
    /// What the page draws, in the source's existing order. The native-chat branch skips the extension's intermediate page.
    @MainActor enum Selection: Equatable {
        case chat(LarkConversation)
        case slot(Slot)

        enum Slot: Equatable {
            case document, connector, moment, mail, empty
        }

        static func of(item: InboxItem, position: String? = nil) -> Selection {
            if item.doc != nil { return .slot(.document) }
            if item.connector != nil { return .slot(.connector) }
            if item.moment != nil { return .slot(.moment) }
            if var conversation = item.larkConversation {
                if let position { conversation.focus = position }
                return .chat(conversation)
            }
            if item.mail != nil { return .slot(.mail) }
            return .slot(.empty)
        }
    }

    var route: InboxSourceRoute
    var accounts: [MailAccount]
    @Environment(AppModel.self) private var model
    @State private var slot: InboxItemSlotModel

    init(route: InboxSourceRoute, accounts: [MailAccount]) {
        self.route = route
        self.accounts = accounts
        _slot = State(initialValue: InboxItemSlotModel(item: route.item))
    }

    private var item: InboxItem { route.item }
    private var selection: Selection { .of(item: item, position: route.position) }

    var body: some View {
        Group {
            switch selection {
            case .chat(let conversation):
                LarkChatView(conversation: conversation, manager: model.linkManager(item.manager))
            case .slot(let plain):
                InboxItemSlotContent(item: item, slot: slot) { content(plain) }
            }
        }
        .navigationTitle(item.title)
    }

    @ViewBuilder private func content(_ body: Selection.Slot) -> some View {
        switch body {
        case .document:
            InboxDocDetailView(item: item)
        case .connector:
            InboxConnectorDetailView(item: item)
        case .moment:
            if let moment = item.moment {
                LarkMomentsPostView(postID: moment.postID, pushed: true, manager: model.linkManager(item.manager))
            }
        case .mail:
            if let mail {
                MailDetailView(summary: mail, accounts: accounts)
            } else {
                EmptyStateView(symbol: Theme.Icon.info, title: item.title, message: item.preview)
            }
        case .empty:
            EmptyStateView(symbol: Theme.Icon.info, title: item.title, message: item.preview)
        }
    }

    /// The mail the link names, else the thread's newest.
    private var mail: MailSummary? {
        guard let uid = route.uid, let m = item.mail else { return item.mailSummary }
        return MailSummary(accountID: m.accountID, mailbox: m.mailbox, uid: uid, messageID: uid == m.uids.last ? m.messageID : nil,
                           subject: item.title, from: m.from, date: item.updatedAt, snippet: item.preview, seen: true)
    }
}

// MARK: - iPhone

/// One event opened on the iPhone or iPad: metadata with 'view original'; the current state, processing conversation and
/// instruction; the AI summary; the suggestion and its buttons; the background recap; the chain and related items; then the
/// thread line. Hand to AI (with Draft Reply among its chips) is the bar along the bottom (`InboxHandOffBar`). The source is
/// a push away (view original, the received entry).
struct InboxEventPage: View {
    var item: InboxItem
    var accounts: [MailAccount]
    var draft: () -> Void
    @State private var suggestion: InboxSuggestionModel
    @State private var editing = false
    @State private var line: InboxTree?

    init(item: InboxItem, accounts: [MailAccount], draft: @escaping () -> Void) {
        self.item = item
        self.accounts = accounts
        self.draft = draft
        _suggestion = State(initialValue: InboxSuggestionModel.live(item.id, manager: item.manager))
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.l) {
                header
                InboxEventBlocks(item: item, model: suggestion, showsSummary: true, showsConversation: true, edit: { editing = true })
                InboxLineSection(tree: line)
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.l)
            .frame(maxWidth: Theme.Size.readable, alignment: .leading)
            .frame(maxWidth: .infinity)
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            InboxHandOffBar(item: item, draft: canDraft ? draft : nil,
                            draftTitle: item.agentDraft == nil ? nil : String(localized: "View Draft"))
        }
        .themedPage()
        .inboxLine(item, tree: $line)
        .sheet(isPresented: $editing) {
            if let s = item.currentSuggestion {
                InboxSuggestionEditor(item: item, suggestion: s, model: suggestion)
            }
        }
        #if DEBUG
        // `-showInboxSuggestionEdit YES`: the editor of the suggestion (screenshots).
        .task { if UserDefaults.standard.bool(forKey: "showInboxSuggestionEdit"), item.currentSuggestion != nil { editing = true } }
        #endif
    }

    /// Where it happens and who said the newest line, with 'view original' at the trailing edge.
    private var header: some View {
        HStack(spacing: Theme.Space.s) {
            Text(InboxEventPage.from(item))
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .accessibilityIdentifier("inbox.detail.meta")
            Spacer(minLength: 0)
            NavigationLink(value: InboxSourceRoute(item: item, position: nil, uid: nil)) {
                Label("View original", systemImage: Theme.Icon.openDocument).font(Theme.Font.captionMedium).lineLimit(1).fixedSize()
            }
            .buttonStyle(.borderless)
            .accessibilityIdentifier("inbox.viewSource")
        }
    }

    /// Where it happens and who said the newest line; without `where`, the source and title as before.
    static func from(_ it: InboxItem) -> String {
        if let meta = InboxNow.meta(it, lang: interfaceTextLocale.identifier) { return meta }
        let source = it.isLark ? String(localized: "Lark [Inbox Event Views 7e4419]") : it.accountName
        return [source, it.title].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// Draft Reply is offered when the item can be answered from here and no suggested reply is there already.
    private var canDraft: Bool {
        let canReply = item.connector.map { $0.canReply == true } ?? true
        return canReply && item.currentSuggestion?.kind != "reply" && !item.isDone
    }
}
