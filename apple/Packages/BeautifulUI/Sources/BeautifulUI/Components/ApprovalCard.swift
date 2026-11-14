import SwiftUI

/// A question the agent asks before acting.
public struct ApprovalQuestion: Identifiable, Sendable {
    public enum Kind: Sendable {
        /// Radio choices; picking one advances on its own.
        case single
        /// Checkboxes; waits for Continue.
        case multiple
    }

    /// One answer to pick: its words, an optional line under them and an optional tag (a host's "Recommended").
    public struct Choice: Hashable, Sendable {
        public var label: String
        public var detail: String?
        public var tag: String?

        public init(_ label: String, detail: String? = nil, tag: String? = nil) {
            self.label = label
            self.detail = detail
            self.tag = tag
        }
    }

    public var id: String
    public var prompt: String
    public var kind: Kind
    public var choices: [Choice]
    /// A short label above the prompt, e.g. the topic of the question.
    public var eyebrow: String?
    /// Whether the person can type their own answer instead (the "Something else…" field).
    public var allowsCustom: Bool
    /// The free-text field's placeholder for this question; `nil` uses the card's.
    public var customPlaceholder: String?

    public var options: [String] { choices.map(\.label) }

    public init(id: String? = nil, _ prompt: String, kind: Kind = .single, options: [String]) {
        self.init(id: id, prompt, kind: kind, choices: options.map { Choice($0) })
    }

    public init(
        id: String? = nil,
        _ prompt: String,
        kind: Kind = .single,
        choices: [Choice],
        eyebrow: String? = nil,
        allowsCustom: Bool = true,
        customPlaceholder: String? = nil
    ) {
        self.id = id ?? prompt
        self.prompt = prompt
        self.kind = kind
        self.choices = choices
        self.eyebrow = eyebrow
        self.allowsCustom = allowsCustom
        self.customPlaceholder = customPlaceholder
    }
}

/// What the person answered: picked option indexes and free text, per question index.
public struct ApprovalAnswers: Sendable, Equatable {
    public var picks: [Int: [Int]] = [:]
    public var custom: [Int: String] = [:]

    public init(picks: [Int: [Int]] = [:], custom: [Int: String] = [:]) {
        self.picks = picks
        self.custom = custom
    }

    public func hasAnswer(_ question: Int) -> Bool {
        !(picks[question] ?? []).isEmpty || !(custom[question] ?? "").trimmingCharacters(in: .whitespaces).isEmpty
    }

    /// Applies a tap on `option`: single-choice questions replace the pick and clear free
    /// text; multiple-choice questions toggle it.
    public mutating func toggle(_ option: Int, in question: Int, kind: ApprovalQuestion.Kind) {
        let current = picks[question] ?? []
        switch kind {
        case .single:
            picks[question] = [option]
            custom[question] = ""
        case .multiple:
            picks[question] = current.contains(option) ? current.filter { $0 != option } : current + [option]
        }
    }

    /// Typing free text on a single-choice question drops its pick.
    public mutating func setCustom(_ text: String, in question: Int, kind: ApprovalQuestion.Kind) {
        custom[question] = text
        if kind == .single { picks[question] = [] }
    }
}

/// How a host fits the card in.
public struct ApprovalCardOptions: Sendable {
    /// The card's own surface and shadow; off when the host's panel already is one.
    public var drawsSurface = true
    /// The original's 320 pt; `nil` fills the host's width.
    public var maxWidth: CGFloat? = 320
    /// A host's vertical cap. Set, the questions scroll while the footer stays outside that scroll; `nil` keeps the
    /// card's original, wholly laid-out body. The cap includes the footer.
    public var maxHeight: CGFloat?
    /// Reject option activation after a drag. Off unless the host also guards the option action with this state.
    public var rejectsDraggedPicks = false
    /// Picking a single-choice answer moves on (and on the last question, sends) after a beat. Off, the
    /// person sends with the button — for answers that are costly to get wrong, such as an approval.
    public var autoAdvance = true
    /// The × that closes the card.
    public var showsDismiss = true
    /// "Start over" on the sent state.
    public var allowsStartOver = true
    /// The send button shows a spinner and the card takes no input (the host is submitting).
    public var isSending = false

    public init() {}
}

/// Human-in-the-loop questions, one at a time: the card slides between questions and
/// resizes to fit, the step counter rolls like an odometer, single-choice answers advance on
/// their own and multi-select waits for Continue.
public struct ApprovalCard<Context: View>: View {
    var questions: [ApprovalQuestion]
    var skipLabel: String?
    var continueLabel: String
    var sendLabel: String
    var customPlaceholder: String
    var sentMessage: String
    /// Whole-card Skip, with the current answers so the host can restore them if it fails. `nil` keeps the original
    /// per-question `skipLabel` behavior and layout.
    var onSkip: ((ApprovalAnswers) -> Void)?
    var onSubmit: ((ApprovalAnswers) -> Void)?
    var onDismiss: (() -> Void)?
    var options: Options
    var context: (Int) -> Context

    @State private var index: Int
    @State private var answers: ApprovalAnswers
    @State private var sent: Bool
    @State private var open = true
    @State private var forward = true
    @State private var contentHeight: CGFloat = 0
    @State private var footerHeight: CGFloat = 0
    @State private var dragged = false
    @State private var advanceTask: Task<Void, Never>?
    @FocusState private var customFocused: Bool
    @Environment(\.buiTheme) private var theme

    public typealias Options = ApprovalCardOptions

    /// - Parameters:
    ///   - startAt / answers / submitted: resume a partly answered approval.
    ///   - skipLabel: `nil` hides Skip.
    ///   - context: a view under question `n`'s prompt, e.g. the command an approval is for.
    public init(
        questions: [ApprovalQuestion],
        startAt: Int = 0,
        answers: ApprovalAnswers = ApprovalAnswers(),
        submitted: Bool = false,
        skipLabel: String? = "Skip",
        continueLabel: String = "Continue",
        sendLabel: String = "Send",
        customPlaceholder: String = "Something else…",
        sentMessage: String = "Answers sent",
        options: Options = Options(),
        onSubmit: ((ApprovalAnswers) -> Void)? = nil,
        onDismiss: (() -> Void)? = nil,
        @ViewBuilder context: @escaping (Int) -> Context
    ) {
        self.options = options
        self.context = context
        self.questions = questions
        self.skipLabel = skipLabel
        self.continueLabel = continueLabel
        self.sendLabel = sendLabel
        self.customPlaceholder = customPlaceholder
        self.sentMessage = sentMessage
        self.onSkip = nil
        self.onSubmit = onSubmit
        self.onDismiss = onDismiss
        _index = State(initialValue: min(max(0, startAt), max(0, questions.count - 1)))
        _answers = State(initialValue: answers)
        _sent = State(initialValue: submitted)
    }

    private var isLast: Bool { index == questions.count - 1 }
    private static var slide: Animation { BUICurve(0.22, 1, 0.36, 1).animation(duration: 0.36) }

    public var body: some View {
        Group {
            if !open {
                Button(theme.strings.openApproval) {
                    withAnimation(BUIMotion.popIn) { open = true }
                }
                .buttonStyle(.bui(.secondary, fontSize: 12.5))
            } else if sent {
                sentView
            } else {
                card
            }
        }
        .frame(maxWidth: options.maxWidth ?? .infinity)
    }

    private var sentView: some View {
        HStack(spacing: 12) {
            HStack(spacing: 6) {
                // The host is still sending: a spinner, not the check, until it has the answer.
                if options.isSending {
                    BUISpinner(size: 14).frame(width: 18, height: 18)
                } else {
                    Circle()
                        .fill(theme.colors.green)
                        .overlay { BUIIcon(.check, size: 11, lineWidth: 3).foregroundStyle(.white) }
                        .frame(width: 18, height: 18)
                }
                Text(sentMessage)
                    .buiFont(BUITextStyle(size: 12.5, weight: .medium))
                    .foregroundStyle(options.isSending ? theme.colors.ink2 : theme.colors.green)
            }
            .padding(.leading, 4)
            .padding(.trailing, 10)
            .padding(.vertical, 4)
            .background(options.isSending ? Color.clear : theme.colors.greenTint, in: Capsule())
            if options.allowsStartOver, !options.isSending {
            Button {
                reset()
            } label: {
                Text(theme.strings.startOver)
                    .buiFont(BUITextStyle(size: 12, weight: .medium))
                    .foregroundStyle(theme.colors.ink3)
            }
            .buttonStyle(.plain)
            }
            Spacer(minLength: 0)
        }
        .buiEntrance(.popIn, duration: 0.26)
    }

    private var card: some View {
        VStack(spacing: 0) {
            if let maxHeight = options.maxHeight {
                ScrollView {
                    questionArea.onGeometryChange(for: CGFloat.self) { $0.size.height } action: { contentHeight = $0 }
                }
                .scrollBounceBehavior(.basedOnSize)
                .frame(height: questionCap(maxHeight))
            } else {
                questionArea
            }
            footer
                .padding(.horizontal, options.drawsSurface ? 0 : -10)
                .padding(.top, options.drawsSurface ? 0 : 2)
                // A capped card includes the whole footer in its measured height; uncapped hosts keep the original bleed.
                .padding(.bottom, options.drawsSurface || options.maxHeight != nil ? 0 : -10)
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { footerHeight = $0 }
        }
        .disabled(options.isSending)
        .modifier(CardSurface(active: options.drawsSurface))
        .overlay(alignment: .topTrailing) {
            if options.showsDismiss {
            Button {
                withAnimation(BUIMotion.standard) { open = false }
                onDismiss?()
            } label: {
                BUIIcon(.close, size: 14, lineWidth: 2.2)
            }
            .buttonStyle(IconButtonStyle())
            .padding(options.drawsSurface ? 10 : -2)
            .accessibilityLabel(theme.strings.dismiss)
            }
        }
        .buiEntrance(.fadeUp, duration: 0.38)
        .deliberateTap($dragged, enabled: options.rejectsDraggedPicks)
    }

    private var questionArea: some View {
        ZStack(alignment: .topLeading) {
            question(questions[index], at: index)
                .id(index)
                .transition(.asymmetric(
                    insertion: .move(edge: forward ? .bottom : .top).combined(with: .opacity),
                    removal: .move(edge: forward ? .top : .bottom).combined(with: .opacity)
                ))
        }
        .frame(maxWidth: .infinity, alignment: .topLeading)
        .clipped()
        .padding(options.drawsSurface ? 12 : 0)
    }

    private func questionCap(_ maxHeight: CGFloat) -> CGFloat {
        let available = max(maxHeight - footerHeight, 0)
        return min(contentHeight == 0 ? available : contentHeight, available)
    }

    private func question(_ question: ApprovalQuestion, at qIndex: Int) -> some View {
        let picked = answers.picks[qIndex] ?? []
        return VStack(alignment: .leading, spacing: 10) {
            VStack(alignment: .leading, spacing: 4) {
                if let eyebrow = question.eyebrow {
                    Text(eyebrow)
                        .buiFont(BUITextStyle(size: 11.5, weight: .medium))
                        .foregroundStyle(theme.colors.ink3)
                }
                Text(question.prompt)
                    .buiFont(BUITextStyle(size: 14, weight: .medium))
                    .foregroundStyle(theme.colors.ink)
                    .padding(.trailing, options.showsDismiss ? 28 : 0)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
            }
            context(qIndex)
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(question.choices.enumerated()), id: \.offset) { option, choice in
                    let on = picked.contains(option)
                    Button {
                        if !dragged { pick(option, question: question) }
                    } label: {
                        HStack(alignment: choice.detail == nil ? .center : .top, spacing: 6) {
                            Indicator(kind: question.kind, on: on)
                                .padding(.top, choice.detail == nil ? 0 : 1)
                            VStack(alignment: .leading, spacing: 3) {
                                HStack(spacing: 6) {
                                    Text(choice.label)
                                        .buiFont(BUITextStyle(size: 13, lineHeight: 1))
                                        .foregroundStyle(on ? theme.colors.ink : theme.colors.ink2)
                                        .fixedSize(horizontal: false, vertical: true)
                                    if let tag = choice.tag { BUIPill(tag, tone: .accent) }
                                }
                                if let detail = choice.detail, !detail.isEmpty {
                                    Text(detail)
                                        .buiFont(BUITextStyle(size: 12, lineHeight: 1.35))
                                        .foregroundStyle(theme.colors.ink3)
                                        .fixedSize(horizontal: false, vertical: true)
                                }
                            }
                            Spacer(minLength: 0)
                        }
                        .padding(.leading, 4)
                        .padding(.trailing, 8)
                        .padding(.vertical, 4)
                        .contentShape(.rect(cornerRadius: theme.radii.control))
                    }
                    .buttonStyle(PressHighlightStyle(radius: theme.radii.control, color: theme.colors.hover))
                    .accessibilityAddTraits(on ? .isSelected : [])
                }
                if question.allowsCustom {
                TextField(question.customPlaceholder ?? customPlaceholder, text: Binding(
                    get: { answers.custom[qIndex] ?? "" },
                    set: { text in answers.setCustom(text, in: qIndex, kind: question.kind) }
                ))
                .buiFont(BUITextStyle(size: 13, lineHeight: 1))
                .foregroundStyle(theme.colors.ink)
                .focused($customFocused)
                .submitLabel(isLast ? .send : .next)
                .onSubmit { if answers.hasAnswer(index) { advance() } }
                .padding(.leading, 10)
                .padding(.trailing, 8)
                .padding(.vertical, 4)
                .accessibilityLabel(theme.strings.customAnswer)
                }
            }
        }
    }

    private var footer: some View {
        HStack(spacing: 12) {
            if let onSkip, let skipLabel {
                Button {
                    guard !options.isSending else { return }
                    advanceTask?.cancel()
                    customFocused = false
                    onSkip(answers)
                } label: {
                    Text(skipLabel).lineLimit(1)
                }
                .buttonStyle(.bui(.ghost, growsWithText: true))
                // The original's compact pills are 27 pt; whole-card Skip keeps a full finger's target.
                .frame(minHeight: 44 * max(1, theme.typeScale * 0.9))
                .contentShape(.rect)
                .accessibilityIdentifier("question-skip")
            }
            if questions.count > 1 {
            HStack(spacing: 4) {
                stepButton(.up, disabled: index <= 0) { go(to: index - 1) }
                    .accessibilityLabel(theme.strings.previousQuestion)
                Text("\(index + 1) / \(questions.count)")
                    .buiFont(BUITextStyle(size: 12, weight: .medium, lineHeight: 1, tracking: -0.008))
                    .monospacedDigit()
                    .foregroundStyle(theme.colors.ink3)
                    .contentTransition(.numericText(countsDown: !forward))
                stepButton(.down, disabled: isLast) { go(to: index + 1) }
                    .accessibilityLabel(theme.strings.nextQuestion)
            }
            }
            Spacer(minLength: 0)
            HStack(spacing: 6) {
                if let skipLabel, onSkip == nil {
                Button(skipLabel) {
                    if isLast {
                        withAnimation(BUIMotion.standard) { open = false }
                        onDismiss?()
                    } else {
                        go(to: index + 1)
                    }
                }
                .buttonStyle(.bui(.ghost, growsWithText: options.maxHeight != nil))
                }
                Button { advance() } label: {
                    HStack(spacing: 6) {
                        if options.isSending && isLast { BUISpinner(size: 11) }
                        Text(isLast ? sendLabel : continueLabel).lineLimit(1)
                    }
                }
                    .buttonStyle(.bui(.accent, growsWithText: options.maxHeight != nil))
                    .disabled(!answers.hasAnswer(index))
            }
            .padding(.trailing, -2)
        }
        .padding(10)
    }

    private enum StepDirection { case up, down }

    private func stepButton(_ direction: StepDirection, disabled: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            BUIIcon(direction == .up ? BUIGlyph("M18 15l-6-6-6 6") : .chevronDown, size: 14)
                .foregroundStyle(theme.colors.ink3)
                .frame(width: 18, height: 18)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.3 : 1)
    }

    // MARK: Actions

    private func pick(_ option: Int, question: ApprovalQuestion) {
        withAnimation(.easeInOut(duration: 0.2)) {
            answers.toggle(option, in: index, kind: question.kind)
        }
        guard question.kind == .single, options.autoAdvance else { return }
        advanceTask?.cancel()
        advanceTask = Task {
            try? await Task.sleep(for: .milliseconds(480))
            guard !Task.isCancelled else { return }
            advance()
        }
    }

    private func go(to next: Int) {
        advanceTask?.cancel()
        let target = min(max(next, 0), questions.count - 1)
        guard target != index else { return }
        forward = target > index
        customFocused = false
        withAnimation(Self.slide) { index = target }
    }

    private func advance() {
        if isLast {
            advanceTask?.cancel()
            customFocused = false
            withAnimation(BUIMotion.popIn) { sent = true }
            onSubmit?(answers)
        } else {
            go(to: index + 1)
        }
    }

    private func reset() {
        withAnimation(BUIMotion.popIn) {
            index = 0
            answers = ApprovalAnswers()
            sent = false
            open = true
        }
    }
}

public extension ApprovalCard where Context == EmptyView {
    init(
        questions: [ApprovalQuestion],
        startAt: Int = 0,
        answers: ApprovalAnswers = ApprovalAnswers(),
        submitted: Bool = false,
        skipLabel: String? = "Skip",
        continueLabel: String = "Continue",
        sendLabel: String = "Send",
        customPlaceholder: String = "Something else…",
        sentMessage: String = "Answers sent",
        options: Options = Options(),
        onSubmit: ((ApprovalAnswers) -> Void)? = nil,
        onDismiss: (() -> Void)? = nil
    ) {
        self.init(questions: questions, startAt: startAt, answers: answers, submitted: submitted, skipLabel: skipLabel,
                  continueLabel: continueLabel, sendLabel: sendLabel, customPlaceholder: customPlaceholder,
                  sentMessage: sentMessage, options: options, onSubmit: onSubmit, onDismiss: onDismiss) { _ in EmptyView() }
    }

    /// Whole-card cancellation is a separate, explicitly labeled overload: adding it to the original initializer would
    /// let an existing unlabeled trailing closure bind to Skip instead of Submit.
    init(
        questions: [ApprovalQuestion],
        startAt: Int = 0,
        answers: ApprovalAnswers = ApprovalAnswers(),
        submitted: Bool = false,
        skipLabel: String? = "Skip",
        continueLabel: String = "Continue",
        sendLabel: String = "Send",
        customPlaceholder: String = "Something else…",
        sentMessage: String = "Answers sent",
        options: Options = Options(),
        onSkip: ((ApprovalAnswers) -> Void)?,
        onSubmit: ((ApprovalAnswers) -> Void)? = nil,
        onDismiss: (() -> Void)? = nil
    ) {
        self.init(questions: questions, startAt: startAt, answers: answers, submitted: submitted, skipLabel: skipLabel,
                  continueLabel: continueLabel, sendLabel: sendLabel, customPlaceholder: customPlaceholder,
                  sentMessage: sentMessage, options: options, onSubmit: onSubmit, onDismiss: onDismiss)
        self.onSkip = onSkip
    }
}

private struct CardSurface: ViewModifier {
    var active: Bool
    @Environment(\.buiTheme) private var theme

    func body(content: Content) -> some View {
        if active { content.buiSurface(radius: theme.radii.card, elevation: .card) } else { content }
    }
}

/// 16 pt radio or checkbox: `bg-ink` with a canvas dot or check when on, a 1.5 pt
/// `line-strong` ring when off.
private struct Indicator: View {
    var kind: ApprovalQuestion.Kind
    var on: Bool
    @Environment(\.buiTheme) private var theme

    var body: some View {
        let shape = kind == .single ? AnyShape(Circle()) : AnyShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
        ZStack {
            shape.fill(on ? theme.colors.ink : .clear)
            if !on {
                shape.stroke(theme.colors.lineStrong, lineWidth: 1.5).padding(0.75)
            }
            if kind == .single {
                Circle()
                    .fill(theme.colors.canvas)
                    .frame(width: 6, height: 6)
                    .scaleEffect(on ? 1 : 0)
            } else {
                BUIIcon(.check, size: 12, lineWidth: 3)
                    .foregroundStyle(on ? theme.colors.canvas : .clear)
            }
        }
        .frame(width: 16, height: 16)
        .animation(.easeInOut(duration: 0.2), value: on)
    }
}
