import SwiftUI

// A morning or evening routine's facts in its conversation (the `brief` item, #2320; docs/extensions/bots.md "Morning and
// evening briefings"): what waits for the user, the last hours, what is to be verified, the questions that can wait and
// what is in progress. The same sections, rows and totals as the web's card and the Lark card; in compact width the done
// and failed rows fold into their totals behind a tap.

/// A routine's facts card.
struct BriefCard: View {
    var brief: BriefBody
    /// The hub group of the conversation it is in.
    var group: String = ""
    /// The conversation whose feed has it: what its buttons ask goes to that conversation's manager.
    var conversation: String = ""
    @Environment(\.horizontalSizeClass) private var widthClass
    /// Compact width: the done and failed rows show once this is on.
    @State private var pastOpen = false
    @Environment(\.chatPin) private var pin
    /// What the chat's pin knows the "Last N hours" line by (this view's, kept while it is drawn).
    @State private var pastKey = "brief-past-" + UUID().uuidString

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            Label(brief.title.isEmpty ? String(localized: "Briefing") : brief.title, systemImage: Theme.Icon.today)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            waiting
            Divider()
            past
            Divider()
            accept
            if !brief.later.isEmpty {
                Divider()
                later
            }
            if brief.progressTotal > 0 {
                Divider()
                progress
            }
            if !brief.setup.isEmpty {
                Divider()
                BriefSetupSection(lines: brief.setup)
            }
        }
        .card(padding: Theme.Space.l)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.card")
    }

    // MARK: Sections

    private var waiting: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if brief.waitingUnknown {
                BriefHead(symbol: Theme.Icon.waitsForYou, tint: AgentState.needsYou.tone.text, title: String(localized: "Waiting for you (?)"))
                BriefNote(String(localized: "Cards waiting for you weren't read yet (Persona had just started). Look in Agents."))
            } else {
                BriefHead(symbol: Theme.Icon.waitsForYou, tint: AgentState.needsYou.tone.text, title: String(localized: "Waiting for you (\(brief.waiting.count))"))
                if brief.waiting.isEmpty { BriefNote(String(localized: "Nothing.")) }
                // Only a hub from before the rows' chat needs the pending cards looked through.
                let pending = brief.waiting.contains { $0.sessionID == nil } ? AppModel.shared.pendingInteractions : []
                ForEach(brief.waiting, id: \.id) { row in
                    BriefChatRow(row: row, chat: BriefBody.chat(for: row, waiting: true, group: group, pending: pending))
                }
            }
        }
    }

    private var past: some View {
        let shown = brief.done.count + brief.failed.count
        let folds = widthClass == .compact && shown > 0
        let title = String(localized: "Last \(brief.hours) hours: \(brief.doneTotal) done · \(brief.failedTotal) not through")
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            if folds {
                Button {
                    // In the chat the line stays where it is and the rows open under it (#3757).
                    toggleInPlace(pin, pastKey, animation: Theme.Motion.state, closing: pastOpen) { pastOpen.toggle() }
                } label: {
                    HStack(spacing: Theme.Space.s) {
                        BriefHead(symbol: Theme.Icon.time, tint: Theme.accent, title: title)
                        Spacer(minLength: Theme.Space.s)
                        ThemedIcon(pastOpen ? Theme.Icon.collapse : Theme.Icon.expand)
                            .font(Theme.Font.labelStrong)
                            .foregroundStyle(.tertiary)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityValue(pastOpen ? String(localized: "Expanded") : String(localized: "Collapsed"))
                .accessibilityIdentifier("brief.past")
                .chatPinned(pastKey)
            } else {
                BriefHead(symbol: Theme.Icon.time, tint: Theme.accent, title: title)
            }
            if shown == 0 { BriefNote(String(localized: "Nothing.")) }
            if !folds || pastOpen {
                ForEach(Array(brief.failed.enumerated()), id: \.offset) { _, f in
                    BriefMarkLine(symbol: Theme.Icon.errorFilled, tint: Theme.danger, title: f.title, detail: TodayFailedRow.line(f))
                }
                ForEach(brief.done, id: \.id) { d in
                    Button { TasksStore.shared.detailTask = d.id } label: {
                        BriefMarkLine(symbol: Theme.Icon.successFilled, tint: Theme.positive, title: d.title, detail: d.at.map(Self.clock), opens: true)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("brief.done.\(d.id)")
                }
                BriefMore(n: brief.pastMore)
            }
        }
    }

    private var accept: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            BriefHead(symbol: Theme.Icon.success, tint: Theme.positive, title: String(localized: "To verify (\(brief.accept.count))"))
            if brief.accept.isEmpty { BriefNote(String(localized: "Nothing.")) }
            ForEach(brief.accept, id: \.self) { row in
                BriefAcceptRow(row: row, group: group)
            }
        }
    }

    private var later: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            BriefHead(symbol: Theme.Icon.question, tint: .secondary, title: String(localized: "Can wait (\(brief.laterTotal))"))
            ForEach(brief.later, id: \.id) { row in
                BriefLaterRow(row: row, group: group, conversation: conversation,
                              chat: BriefBody.chat(for: row, waiting: false, group: group, pending: []))
            }
            BriefMore(n: brief.laterMore)
        }
    }

    private var progress: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            BriefHead(symbol: Theme.Icon.working, tint: AgentState.working.tone.text, title: String(localized: "In progress (\(brief.progressTotal))"))
            ForEach(brief.progress, id: \.id) { p in
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.m) {
                        Text(p.title).font(Theme.Font.secondaryMedium).frame(maxWidth: .infinity, alignment: .leading)
                        BriefOpenButton(title: p.title) { TasksStore.shared.detailTask = p.id }
                    }
                    if let stage = TodoStage(hub: p.stage) {
                        TodoStageBar(stage: stage)
                    } else if let stage = p.stage?.trimmingCharacters(in: .whitespacesAndNewlines), !stage.isEmpty {
                        BriefNote(stage) // a stage this build doesn't know: the hub's word for it
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
            BriefMore(n: brief.progressMore)
        }
    }

    private static func clock(_ date: Date) -> String {
        date.formatted(.dateTime.hour(.defaultDigits(amPM: .omitted)).minute())
    }
}

// MARK: - Recommended setup

/// The week's recommended setup (#5085, #5804; docs/assistant/assistant.md "Recommended setup"): the hub's own counts
/// and words, drawn as they came. Open Settings is all a line does, and only where the app has its page: nothing is
/// turned on from here.
private struct BriefSetupSection: View {
    var lines: [BriefBody.Setup]

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            BriefHead(symbol: Theme.Icon.settings, tint: .secondary, title: String(localized: "Recommended setup"))
            BriefNote(String(localized: "From Persona's own records of the last 7 days, unless a line names its own period. Nothing was changed: each of these stays as it is until you change it yourself."))
            ForEach(lines) { line in
                BriefSetupRow(line: line, open: action(line))
            }
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.setup")
    }

    /// Open Settings for a line whose mechanism is turned on at a page of this app; nil otherwise, and the line has
    /// no button.
    private func action(_ line: BriefBody.Setup) -> (() -> Void)? {
        guard let page = line.page else { return nil }
        return { BriefSetupPages.open(page) }
    }
}

/// One mechanism: the hub's whole line, and Open Settings when the app has its page.
private struct BriefSetupRow: View {
    var line: BriefBody.Setup
    var open: (() -> Void)?

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(line.text).font(Theme.Font.secondary)
            }
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            if let open {
                Button(action: open) { Text("Open Settings [Brief Setup]").lineLimit(1) }
                    .font(Theme.Font.captionStrong)
                    .themedButton()
                    .accessibilityIdentifier("brief.setup.open.\(line.id)")
            }
        }
        .padding(.vertical, Theme.Space.xs)
        // A container, so its Open Settings keeps its own identifier (`brief.setup.open.<id>`) under the row's.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("brief.setup.\(line.id)")
    }
}

// MARK: - Parts

/// A section's heading: its mark in a tinted well, what it is and how many.
private struct BriefHead: View {
    var symbol: String
    var tint: Color
    var title: String

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(symbol)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(tint)
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                .background(tint.opacity(0.14), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            Text(title).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
        }
        .padding(.top, Theme.Space.xs)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

/// A line of small gray text: Nothing, a note, a stage.
private struct BriefNote: View {
    var text: String
    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text).font(Theme.Font.caption).foregroundStyle(.secondary)
    }
}

/// "and 3 more": the rows past what the hub listed.
private struct BriefMore: View {
    var n: Int

    var body: some View {
        if n > 0 { BriefNote(String(localized: "and \(n) more")) }
    }
}

/// A done or failed to-do: its mark, its title, and when (and why).
private struct BriefMarkLine: View {
    var symbol: String
    var tint: Color
    var title: String
    var detail: String?
    /// A tap opens it: a chevron says so.
    var opens = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(symbol).font(Theme.Font.label).foregroundStyle(tint)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryMedium)
                if let detail { Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if opens { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary) }
        }
        .padding(.vertical, Theme.Space.xxs)
        .contentShape(.rect)
    }
}

/// How long it waited, whether the bot nudged, who asked.
private func briefMeta(_ row: BriefBody.Row) -> String {
    let wait = Duration.seconds(max(row.waitedS, 60)).formatted(.units(allowed: [.days, .hours, .minutes], width: .narrow, maximumUnitCount: 2))
    var parts = [String(localized: "Waited \(wait)")]
    if row.nudged { parts.append(String(localized: "nudged")) }
    if let from = row.from?.trimmingCharacters(in: .whitespacesAndNewlines), !from.isEmpty { parts.append(from) }
    return parts.joined(separator: " · ")
}

/// Open: the to-do or the Agent's chat.
private struct BriefOpenButton: View {
    var title: String
    /// A finger's height (44 pt) for the buttons of a row that has the decision's ones beside it.
    var tall = false
    var open: () -> Void

    var body: some View {
        Button(action: open) { Text("Open").lineLimit(1).tapTargetHeightIf(tall) }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .accessibilityLabel(String(localized: "Open: \(title)"))
    }
}

/// A card waiting for an answer: what it asks, how long it waited, and Open when the app knows its chat.
private struct BriefChatRow: View {
    var row: BriefBody.Row
    var chat: SessionKey?

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(row.title).font(Theme.Font.secondaryMedium)
                Text(briefMeta(row)).font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if let chat { BriefOpenButton(title: row.title) { AppModel.shared.open(chat) } }
        }
        .padding(.vertical, Theme.Space.xs)
    }
}

/// A to-do to verify: its title, verdict, what changed and the recommendation, then Open and its button (Accept, Got It or
/// Take Recommendation), which turns into what happened. A decision the owner can put off also has Later and Don't
/// handle. Where the buttons do not fit in one row, the secondary ones wrap in their own row and the main one has the
/// line below them to itself (docs/design/task-cards.md §3.4); there is no menu to hide them in.
struct BriefAcceptRow: View {
    var row: BriefBody.Row
    var group: String
    var open: (() -> Void)? = nil
    /// The to-do the row is about, where Later and Don't handle belong on it (the tray and the board); nil in a brief card,
    /// which has no such buttons.
    var decision: TodoItem? = nil
    /// False where a line right above already says the title (a Needs You row opened in place).
    var showsTitle = true
    private var actions: BriefActions { .shared }

    var body: some View {
        if actions.outcome(accepting: row, group: group) != .completed {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if showsTitle { Text(row.title).font(Theme.Font.secondaryMedium) }
                if let verdict = row.verdict, !verdict.isEmpty { Text(verdict).font(Theme.Font.caption) }
                if let detail = row.detail, !detail.isEmpty { Text(detail).font(Theme.Font.caption).foregroundStyle(.secondary) }
                if let option = row.recommend, !option.isEmpty {
                    Text("Recommended: \(option)").font(Theme.Font.caption).foregroundStyle(.secondary)
                }
                Text(briefMeta(row)).font(Theme.Font.caption).foregroundStyle(.secondary)
                if row.forBriefing { BriefHeldMark(id: row.id) }
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: Theme.Space.s) {
                        secondaryActions
                        primaryAction
                    }
                    .fixedSize(horizontal: true, vertical: false)
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        FlowLayoutRow(spacing: Theme.Space.s) { secondaryActions }
                        primaryAction.frame(maxWidth: .infinity)
                    }
                }
                // wide: the row sits at the trailing edge, as it did (ViewThatFits takes the chosen row's own width)
                .frame(maxWidth: .infinity, alignment: .trailing)
                .padding(.top, Theme.Space.xs)
                RuleSuggestionLine(suggestion: row.suggestion, card: .todo(row.id, round: row.askedAt ?? ""), group: group)
            }
            .padding(.vertical, Theme.Space.xs)
        }
    }

    /// Later and Don't handle are for a decision still waiting on the owner (the web's `TaskDecision`, not a plain verify row).
    private var deferrable: TodoItem? {
        guard let decision, row.isDecision || decision.supervision?.state == "needs_user" else { return nil }
        let outcome = actions.outcome(accepting: row, group: group)
        return outcome == nil || outcome == .busy ? decision : nil
    }

    @ViewBuilder private var secondaryActions: some View {
        BriefOpenButton(title: row.title, tall: true) {
            if let open { open() } else { TasksStore.shared.detailTask = row.id }
        }
        let outcome = actions.outcome(accepting: row, group: group)
        if row.isDecision, outcome == nil || outcome == .busy {
            Button {
                Task { await actions.completeDirectly(row, group: group) }
            } label: {
                Text("Complete Directly").lineLimit(1).buttonLabelHeight()
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .disabled(row.askedAt?.isEmpty != false || outcome == .busy)
            .accessibilityLabel(String(localized: "Complete directly: \(row.title)"))
            .accessibilityIdentifier("brief.complete.\(row.id)")
        }
        if let todo = deferrable {
            let busy = TasksStore.shared.busyDecisions.contains(todo.id)
            Button {
                withAnimation(Theme.Motion.state) { TasksStore.shared.laterDecision(todo) }
            } label: {
                Text("Later [Task Views 9d2ca2]").lineLimit(1).buttonLabelHeight()
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .disabled(busy || TasksStore.decisionKey(todo) == nil)
            .accessibilityLabel(String(localized: "Later: \(row.title)"))
            .accessibilityIdentifier("decision.later.\(row.id)")
            Button {
                Task { await TasksStore.shared.dismissDecision(todo) }
            } label: {
                Text("Do not handle [Task Draft]").lineLimit(1).buttonLabelHeight()
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .disabled(busy || todo.decisionReview == nil)
            .accessibilityLabel(String(localized: "Don't handle: \(row.title)"))
            .accessibilityIdentifier("decision.dismiss.\(row.id)")
        }
    }

    @ViewBuilder private var primaryAction: some View {
        if let take = row.take { button(take) }
    }

    @ViewBuilder private func button(_ take: BriefBody.Row.Take) -> some View {
        switch actions.outcome(accepting: row, group: group) {
        case .completed?: EmptyView()
        case .taken(let took)?: BriefOutcomeText(text: Self.taken(took), id: row.id)
        case .alreadyHandled?: BriefOutcomeText(text: String(localized: "Already handled"), id: row.id)
        case .hubTooOld?: BriefOutcomeText(text: String(localized: "The hub is too old for this button"), id: row.id)
        case .marked?, .busy?, nil:
            let busy = actions.outcome(accepting: row, group: group) == .busy
            Button {
                Task { await actions.accept(row, group: group) }
            } label: {
                Text(Self.label(take)).lineLimit(1).opacity(busy ? 0 : 1).buttonLabelHeight().frame(maxWidth: .infinity)
                    .overlay { if busy { ProgressView().controlSize(.small) } }
            }
            .font(Theme.Font.captionStrong)
            .themedButton(prominent: true)
            .disabled(row.askedAt?.isEmpty != false || busy)
            .accessibilityLabel(take == .recommendation ? String(localized: "Take the recommendation for \(row.title)") : String(localized: "Accept: \(row.title)"))
            .accessibilityIdentifier("brief.accept.\(row.id)")
        }
    }

    private static func label(_ take: BriefBody.Row.Take) -> String {
        switch take {
        case .accept: String(localized: "Accept")
        case .gotIt: String(localized: "Got It")
        case .recommendation: String(localized: "Take Recommendation")
        }
    }

    private static func taken(_ take: BriefBody.Row.Take) -> String {
        switch take {
        case .accept: String(localized: "Accepted")
        case .gotIt: String(localized: "Noted")
        case .recommendation: String(localized: "Recommendation taken")
        }
    }
}

/// A decision that can wait (#4930, #5803): nothing was pushed for it, it comes with the next briefing. Only a to-do's
/// own row carries it (`TodoAsk.row`); a briefing's rows never do.
struct BriefHeldMark: View {
    var id: String

    var body: some View {
        Label {
            Text("Comes with your next briefing").lineLimit(1)
        } icon: {
            ThemedIcon(Theme.Icon.today)
        }
        .font(Theme.Font.caption)
        .foregroundStyle(.secondary)
        .accessibilityIdentifier("brief.held.\(id)")
    }
}

/// A question an Agent left for the briefing: what it asks, how long it waited, Open when the app knows its chat, and
/// Handled, which turns into what happened.
private struct BriefLaterRow: View {
    var row: BriefBody.Row
    var group: String
    var conversation: String = ""
    var chat: SessionKey?
    private var actions: BriefActions { .shared }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(row.title).font(Theme.Font.secondaryMedium)
            Text(briefMeta(row)).font(Theme.Font.caption).foregroundStyle(.secondary)
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                if let chat { BriefOpenButton(title: row.title) { AppModel.shared.open(chat) } }
                handled
            }
            .padding(.top, Theme.Space.xs)
        }
        .padding(.vertical, Theme.Space.xs)
    }

    @ViewBuilder private var handled: some View {
        switch actions.outcome(marking: row, group: group) {
        case .marked?: BriefOutcomeText(text: String(localized: "Marked as handled"), id: row.id)
        case .alreadyHandled?: BriefOutcomeText(text: String(localized: "Already handled"), id: row.id)
        case .hubTooOld?: BriefOutcomeText(text: String(localized: "The hub is too old for this button"), id: row.id)
        case .completed?, .taken?, .busy?, nil:
            let busy = actions.outcome(marking: row, group: group) == .busy
            Button {
                Task { await actions.markHandled(row, group: group, conversation: conversation) }
            } label: {
                Text("Handled").lineLimit(1).opacity(busy ? 0 : 1)
                    .overlay { if busy { ProgressView().controlSize(.small) } }
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .accessibilityLabel(String(localized: "Mark as handled: \(row.title)"))
            .accessibilityIdentifier("brief.later.\(row.id)")
        }
    }
}

/// What a button came to, in its place.
private struct BriefOutcomeText: View {
    var text: String
    var id: String

    var body: some View {
        Text(text)
            .font(Theme.Font.captionMedium)
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .accessibilityIdentifier("brief.outcome.\(id)")
    }
}

private extension View {
    @ViewBuilder func tapTargetHeightIf(_ tall: Bool) -> some View {
        if tall { buttonLabelHeight() } else { self }
    }
}

extension View {
    /// A label tall enough that the glass button around it (about 14 pt of its own padding) is a finger's height
    /// (also the decision row's Later and Don't handle, DecisionRow.swift).
    func buttonLabelHeight() -> some View {
        scaledFrame(minHeight: Theme.Size.tapTarget - 14).frame(minHeight: Theme.Size.tapTarget - 14)
    }
}
