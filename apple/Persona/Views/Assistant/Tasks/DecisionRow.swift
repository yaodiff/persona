import SwiftUI

/// Answering a decision on its row (#3804, docs/assistant/todos.md "Decisions the owner can answer"): which option of
/// a round is armed, which answer is on its way, and what became of it. Kept here by round, not in the row, so the same
/// decision drawn in the Needs You tray and on the task board says the same thing, and a row
/// drawn again (scrolled away and back) still says what happened. Every answer goes through
/// `POST /api/v1/todos/{id}/accept`, as the recommendation's Take always did.
@MainActor @Observable final class DecisionAnswers {
    /// Where answering a round stands.
    enum State: Equatable, Sendable {
        /// Its request is out: `answer` names the button that shows it ("option:1", "response:explain_first").
        case busy(answer: String)
        /// The hub took one of the fixed answers: nothing was chosen and the round may still wait (`explain_first`
        /// keeps it open until the decision is asked again), so the row says "Sent back to Persona" and keeps its
        /// options, Other… and ⋯. Only an option, or the hub ending the round, ends the row.
        case sentBack
        /// The hub took an option; the row says this instead of its buttons.
        case answered(String)
        /// 409: that round was over (asked again since, or answered elsewhere).
        case alreadyHandled

        /// The round is over for this row: no button sends anything again.
        var ended: Bool {
            switch self {
            case .answered, .alreadyHandled: true
            case .busy, .sentBack: false
            }
        }
    }

    static let shared = DecisionAnswers()

    private(set) var states: [String: State] = [:]
    /// The option a first tap armed, for a decision that takes two (`TodoAsk.needsConfirmation`).
    private(set) var armed: [String: Int] = [:]
    /// The hub of the conversation the row is in.
    @ObservationIgnored private let api: (String) -> APIClient?
    /// Says why an answer did not go; the buttons work again.
    @ObservationIgnored private let failed: (String) -> Void
    /// After an answer was taken (or found already handled): the to-do's state changed on the hub.
    @ObservationIgnored private let taken: () -> Void

    init(api: @escaping (String) -> APIClient? = { AppModel.shared.api($0) },
         failed: @escaping (String) -> Void = { AppModel.shared.toast = Toast($0, style: .error) },
         taken: @escaping () -> Void = { TasksStore.shared.reloadTodos() }) {
        self.api = api
        self.failed = failed
        self.taken = taken
    }

    /// A round of a to-do on a hub: the same round on two surfaces is one answer.
    static func key(todo: String, ask: TodoAsk, group: String) -> String { "\(group)|\(todo)|\(ask.askedAt)" }

    func state(todo: String, ask: TodoAsk, group: String) -> State? { states[Self.key(todo: todo, ask: ask, group: group)] }
    func armedOption(todo: String, ask: TodoAsk, group: String) -> Int? { armed[Self.key(todo: todo, ask: ask, group: group)] }

    /// A tap on option `index`. A step that cannot be undone, or that other people will see, takes two: the first
    /// arms the option ("Confirm: …"), the second on the same one sends it. Any other decision sends at once.
    func choose(_ index: Int, of ask: TodoAsk, todo: String, group: String) async {
        let options = ask.decisionOptions
        guard options.indices.contains(index) else { return }
        let key = Self.key(todo: todo, ask: ask, group: group)
        guard Self.free(states[key]) else { return }
        if ask.needsConfirmation, armed[key] != index {
            armed[key] = index
            return
        }
        let label = options[index].label
        await send(key, group: group, answer: "option:\(index)", done: .answered(String(localized: "Answered: \(label)"))) {
            try await $0.acceptTodo(todo, askedAt: ask.askedAt, option: label)
        }
    }

    /// One of the fixed answers ("You Decide", "This Isn't What I Asked For", "Tell Me the Background First"): never armed,
    /// nothing is taken, the task's supervisor hears it. The row stays answerable: "Tell Me the Background First"
    /// leaves the round open on the hub, and the others end it there, which the to-dos read after it says.
    func respond(_ response: TodoAsk.Response, to ask: TodoAsk, todo: String, group: String) async {
        let key = Self.key(todo: todo, ask: ask, group: group)
        guard Self.free(states[key]) else { return }
        await send(key, group: group, answer: "response:\(response.rawValue)", done: .sentBack) {
            try await $0.acceptTodo(todo, askedAt: ask.askedAt, response: response.rawValue)
        }
    }

    /// No request of the round is out and it has not ended: a tap may send.
    private static func free(_ state: State?) -> Bool { state == nil || state == .sentBack }

    private func send(_ key: String, group: String, answer: String, done: State, _ call: (APIClient) async throws -> Void) async {
        guard let api = api(group) else {
            let why = String(localized: "Not connected to a hub [Todo Store 04f822]")
            failed(String(localized: "Couldn't send your answer. \(why)"))
            return
        }
        let before = states[key]
        states[key] = .busy(answer: answer)
        do {
            try await call(api)
            states[key] = done
            armed[key] = nil
            taken()
        } catch let e as APIError where e.status == 409 {
            states[key] = .alreadyHandled
            armed[key] = nil
            taken()
        } catch {
            // A 502 (the choice did not reach the task's agent), a 503 or no answer: nothing was taken. The option
            // stays armed, so the next tap sends it again, and a fixed answer sent before still says so.
            states[key] = before
            failed(String(localized: "Couldn't send your answer. \(error.localizedDescription)"))
        }
    }
}

/// A to-do's open ask where the app lists what needs the owner (the Needs You tray, the task board): a decision with
/// options is answered on its row (`DecisionRow`); any other ask keeps its one button (`BriefAcceptRow`), as does a
/// decision from a hub that sends no options.
struct TodoAskRow: View {
    var todo: TodoItem
    var ask: TodoAsk
    var group: String
    var open: (() -> Void)? = nil
    /// Later and Don't handle belong on the row (the Needs You tray), as on `BriefAcceptRow` given its `decision`.
    var deferrable = false
    /// False where a line right above already says the title (a Needs You row opened in place).
    var showsTitle = true

    var body: some View {
        #if os(macOS)
        // The native Mac app is frozen (docs/apple/mac.md): it keeps the one button it had.
        BriefAcceptRow(row: ask.row(for: todo), group: group, open: open, decision: deferrable ? todo : nil, showsTitle: showsTitle)
        #else
        if ask.decisionOptions.isEmpty {
            BriefAcceptRow(row: ask.row(for: todo), group: group, open: open, decision: deferrable ? todo : nil, showsTitle: showsTitle)
        } else {
            DecisionRow(todo: todo, ask: ask, group: group, open: open, showsTitle: showsTitle, deferrable: deferrable)
        }
        #endif
    }
}

/// A decision that can be answered on its row (#3804; the web's views/assistant/DecisionRow.tsx): what is to be decided,
/// where the task came from and where it stands, what each option leads to (the recommended one first and marked),
/// what happens when nobody answers, then the options as buttons (the recommended one the only solid one), as many as
/// fit, and "Other…", which opens the task to answer in words. The options that do not fit and the answers that are no
/// option ("You Decide", "This Isn't What I Asked For", "Tell Me the Background First") are in its ⋯ menu.
struct DecisionRow: View {
    var todo: TodoItem
    var ask: TodoAsk
    var group: String
    var open: (() -> Void)? = nil
    /// False where a line right above already says the title (a Needs You row opened in place).
    var showsTitle = true
    /// Later and Don't handle under the options (the Needs You tray), as `BriefAcceptRow` has them for a decision.
    var deferrable = false
    var answers: DecisionAnswers = .shared
    /// Complete Directly (#3038) goes through the briefing's actions, as `BriefAcceptRow`'s button does.
    var actions: BriefActions = .shared

    private var options: [TodoAsk.Option] { ask.decisionOptions }
    private var state: DecisionAnswers.State? { answers.state(todo: todo.id, ask: ask, group: group) }
    private var armed: Int? { answers.armedOption(todo: todo.id, ask: ask, group: group) }
    /// Complete Directly's request for this round, or what it came to.
    private var completion: BriefActions.Outcome? { actions.outcome(accepting: todo.id, askedAt: ask.askedAt, group: group) }
    /// A request of this round is out, an answer's or Complete Directly's: every control of the row waits for it, so
    /// nothing is sent twice and an option cannot race the completion.
    var busy: Bool {
        if case .busy? = state { return true }
        return completion == .busy
    }
    /// A fixed answer went back to Persona (from this phone, or the hub says so): the decision may still wait, and the
    /// row keeps its controls.
    var sentBack: Bool { state == .sentBack || ask.response != nil }

    var body: some View {
        // Completed directly: the task is done, and the row goes as `BriefAcceptRow` does.
        if completion != .completed {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                if showsTitle {
                    Text(todo.title)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
                Text(ask.questionLine)
                    .font(Theme.Font.secondaryMedium)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("decision.question")
                if let background = ask.background, !background.isEmpty {
                    Text(background)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .accessibilityIdentifier("decision.background")
                }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    ForEach(Array(options.enumerated()), id: \.offset) { index, option in
                        leads(option, recommended: index == 0)
                    }
                }
                .padding(.top, Theme.Space.xxs)
                if let waits = DecisionWaitingLines.line(for: ask) {
                    Text(waits)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("decision.default")
                }
                // Held for the briefing (#5803): nothing was pushed for it, which is why it did not ring.
                if ask.isForBriefing, state?.ended != true { BriefHeldMark(id: todo.id) }
                footer.padding(.top, Theme.Space.xs)
                if deferrable, state?.ended != true, completion == nil || completion == .busy {
                    DecisionDeferButtons(todo: todo, busy: busy)
                }
                // A go-ahead the owner gave three times carries a suggested rule (#5802), under the row as on
                // `BriefAcceptRow`.
                RuleSuggestionLine(suggestion: ask.suggestion, card: .todo(todo.id, round: ask.askedAt), group: group)
            }
            .padding(.vertical, Theme.Space.xs)
            .accessibilityElement(children: .contain)
        }
    }

    /// An option and what choosing it leads to, the recommended one marked.
    private func leads(_ option: TodoAsk.Option, recommended: Bool) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.flush) {
            // The tag beside the label while both fit on its line, under it when not: beside a long label or at the
            // large text sizes the tag kept its width and squeezed the label to a character a line.
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    optionLabel(option)
                    if recommended { RecommendedTag() }
                }
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    optionLabel(option)
                    if recommended { RecommendedTag() }
                }
            }
            if let leadsTo = option.leadsTo, !leadsTo.isEmpty {
                Text(leadsTo).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(2)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func optionLabel(_ option: TodoAsk.Option) -> some View {
        Text(option.label).font(Theme.Font.captionStrong).lineLimit(2).fixedSize(horizontal: false, vertical: true)
    }

    // MARK: Answers

    @ViewBuilder private var footer: some View {
        switch state {
        case .answered(let text)?:
            outcome(text)
        case .alreadyHandled?:
            outcome(String(localized: "Already handled"))
        case .busy?, .sentBack?, nil:
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                // A fixed answer was given ("Tell Me the Background First"): the decision still waits, and can still
                // be answered here.
                if sentBack {
                    // A line of words, not a button: it wraps at the large text sizes rather than losing its end.
                    Text("Sent back to Persona")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                        .accessibilityIdentifier("decision.sent-back.\(todo.id)")
                }
                ViewThatFits(in: .horizontal) {
                    ForEach(Array(stride(from: options.count, to: 1, by: -1)), id: \.self) { shown in
                        HStack(spacing: Theme.Space.s) { controls(shown: shown) }
                            .fixedSize(horizontal: true, vertical: false)
                    }
                    // Alone and still wider than the row, the option gives way and cuts its label; it never wraps,
                    // and "Other…" and ⋯ keep theirs.
                    HStack(spacing: Theme.Space.s) { controls(shown: 1) }
                }
            }
        }
    }

    /// What the answer came to, in the buttons' place.
    private func outcome(_ text: String) -> some View {
        Text(text)
            .font(Theme.Font.captionMedium)
            .foregroundStyle(.secondary)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier("decision.outcome.\(todo.id)")
    }

    /// The options drawn as buttons when `shown` fit: the first ones, and always the armed one, so that the "Confirm: …"
    /// a first tap made (a longer label) never moves into the ⋯ menu; it takes the last place instead.
    func buttonIndices(shown: Int) -> [Int] {
        let count = min(max(shown, 1), options.count)
        guard count > 0 else { return [] }
        var indices = Array(0..<count)
        if let armed, options.indices.contains(armed), !indices.contains(armed) { indices[count - 1] = armed }
        return indices
    }

    /// The options as buttons (`buttonIndices`), "Other…", and the menu with the rest; each a finger's height
    /// (`buttonLabelHeight`), as `BriefAcceptRow`'s buttons and Later / Don't handle under them are.
    @ViewBuilder private func controls(shown: Int) -> some View {
        let indices = buttonIndices(shown: shown)
        ForEach(indices, id: \.self) { index in
            optionButton(options[index], index: index)
        }
        Button {
            if let open { open() } else { TasksStore.shared.detailTask = todo.id }
        } label: {
            Text("Other…").lineLimit(1).buttonLabelHeight()
        }
        .font(Theme.Font.captionStrong)
        .themedButton()
        .fixedSize()
        .disabled(busy)
        .accessibilityLabel(String(localized: "Open \(todo.title) to answer another way"))
        .accessibilityIdentifier("decision.other.\(todo.id)")
        Menu {
            menuItems(without: indices)
        } label: {
            // An icon-only themed button takes its glyph's size (docs/lessons/ios.md): a line of the buttons' own
            // text gives it their height, and the icon token its width. While Complete Directly is out it spins.
            Text(verbatim: " ").lineLimit(1).buttonLabelHeight()
                .scaledFrame(width: Theme.Size.icon)
                .overlay {
                    if completion == .busy { ProgressView().controlSize(.small) } else { ThemedIcon(Theme.Icon.more) }
                }
        }
        .font(Theme.Font.captionStrong)
        .themedButton()
        .fixedSize()
        .disabled(busy)
        .accessibilityLabel("More answers")
        .accessibilityIdentifier("decision.more.\(todo.id)")
    }

    private func label(_ option: TodoAsk.Option, index: Int) -> String {
        armed == index ? String(localized: "Confirm: \(option.label)") : option.label
    }

    private func optionButton(_ option: TodoAsk.Option, index: Int) -> some View {
        let sending = state == .busy(answer: "option:\(index)")
        return Button {
            Task { await answers.choose(index, of: ask, todo: todo.id, group: group) }
        } label: {
            // Its own height kept: a minimum-height frame alone gives way to a short proposal (the one column at the
            // accessibility sizes) and the words spill out of the capsule; Other… and ⋯ are fixed whole.
            Text(label(option, index: index)).lineLimit(1).buttonLabelHeight().fixedSize(horizontal: false, vertical: true)
                .opacity(sending ? 0 : 1)
                .overlay { if sending { ProgressView().controlSize(.small) } }
        }
        .font(Theme.Font.captionStrong)
        .themedButton(prominent: index == 0)
        .disabled(busy)
        .accessibilityLabel(index == 0 ? String(localized: "Choose \(option.label), the recommended option, for \(todo.title)")
                                       : String(localized: "Choose \(option.label) for \(todo.title)"))
        .accessibilityValue(armed == index ? String(localized: "Tap again to confirm") : "")
        .accessibilityIdentifier(armed == index ? "decision.option.\(index).armed" : "decision.option.\(index)")
    }

    /// The options that have no button, each with what it leads to; the fixed answers, each with what it does; and
    /// Complete Directly (#3038), which ends the task without sending anything to its agent.
    @ViewBuilder private func menuItems(without shown: [Int]) -> some View {
        ForEach(options.indices.filter { !shown.contains($0) }, id: \.self) { index in
            let option = options[index]
            Button {
                Task { await answers.choose(index, of: ask, todo: todo.id, group: group) }
            } label: {
                Text(label(option, index: index))
                if let leadsTo = option.leadsTo, !leadsTo.isEmpty { Text(leadsTo) }
            }
        }
        let responses = ask.fixedResponses
        if !responses.isEmpty {
            Section {
                ForEach(responses, id: \.self) { response in
                    Button {
                        Task { await answers.respond(response, to: ask, todo: todo.id, group: group) }
                    } label: {
                        Text(Self.title(response))
                        Text(Self.detail(response))
                    }
                }
            }
        }
        Section {
            Button {
                Task { await actions.completeDirectly(ask.row(for: todo), group: group) }
            } label: {
                Text("Complete Directly")
            }
        }
    }

    static func title(_ response: TodoAsk.Response) -> String {
        switch response {
        case .decideYourself: String(localized: "You Decide")
        case .notWhatIAsked: String(localized: "This Isn't What I Asked For")
        case .explainFirst: String(localized: "Tell Me the Background First")
        }
    }

    static func detail(_ response: TodoAsk.Response) -> String {
        switch response {
        case .decideYourself: String(localized: "Persona decides and records what it assumed.")
        case .notWhatIAsked: String(localized: "The work pauses until Persona checks it against your request.")
        case .explainFirst: String(localized: "Persona asks again with what this is and why.")
        }
    }
}

/// Later (this app session) and Don't handle (kept by the hub, undoable) under a decision's options in the Needs You
/// tray (docs/design/task-cards.md D9): the same two buttons `BriefAcceptRow` has for a decision without options.
private struct DecisionDeferButtons: View {
    var todo: TodoItem
    /// An answer of the row is out.
    var busy: Bool
    private var store: TasksStore { .shared }

    var body: some View {
        let waiting = busy || store.busyDecisions.contains(todo.id)
        FlowLayoutRow(spacing: Theme.Space.s) {
            Button {
                withAnimation(Theme.Motion.state) { store.laterDecision(todo) }
            } label: {
                Text("Later [Task Views 9d2ca2]").lineLimit(1).buttonLabelHeight()
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .disabled(waiting || TasksStore.decisionKey(todo) == nil)
            .accessibilityLabel(String(localized: "Later: \(todo.title)"))
            .accessibilityIdentifier("decision.later.\(todo.id)")
            Button {
                Task { await store.dismissDecision(todo) }
            } label: {
                Text("Do not handle [Task Draft]").lineLimit(1).buttonLabelHeight()
            }
            .font(Theme.Font.captionStrong)
            .themedButton()
            .disabled(waiting || todo.decisionReview == nil)
            .accessibilityLabel(String(localized: "Don't handle: \(todo.title)"))
            .accessibilityIdentifier("decision.dismiss.\(todo.id)")
        }
    }
}

/// "Taken by default at 17:30 · If it waits: …" for each round, worked out once (docs/apple/performance.md P4): the
/// date parsing and formatting stay out of the `body` of a row the tray and the board redraw on every
/// push. Kept by round and day (the line names the date on another day); a handful of rounds are open at a time.
@MainActor enum DecisionWaitingLines {
    private struct Key: Hashable {
        var asked: String
        var deadline: String?
        var defaultAction: String?
        var cost: String?
        var day: Date
    }

    private struct Line { var text: String? }
    private static var lines: [Key: Line] = [:]

    static func line(for ask: TodoAsk, now: Date = .now, calendar: Calendar = .current) -> String? {
        let key = Key(asked: ask.askedAt, deadline: ask.deadline, defaultAction: ask.defaultAction, cost: ask.costOfWaiting,
                      day: calendar.startOfDay(for: now))
        if let hit = lines[key] { return hit.text }
        let text = ask.waitingLine(now: now, calendar: calendar)
        if lines.count >= 64 { lines.removeAll() }
        lines[key] = Line(text: text)
        return text
    }
}
