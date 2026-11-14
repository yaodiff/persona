import SwiftUI
import PersonaWire

/// An MCP server's form or link (docs/protocol/protocol.md "MCP elicitation"; the rules are `Elicitation`'s, as on the
/// web and Android). A form in the subset the app can draw: a row per field, Decline / Submit, and Cancel under them. A
/// form outside it: Decline / Hand Back to Computer, so the terminal answers it. A link: who asks, where it goes and the
/// whole URL, Open Link in the app's link card (as every link in a conversation), and Continue only once this card
/// opened it. The panel's ⋯ › Handle in Terminal on Computer hands any of them back, as on every card.
struct ElicitationForm: View {
    @Environment(AppModel.self) private var model
    /// The chat's link card; nil outside a chat, where this card opens its own (`link`).
    @Environment(\.openLink) private var openLink
    var interaction: Interaction
    /// What is filled in (number fields hold their text) and the link this card opened: its draft, else the defaults.
    @State private var values: [String: JSONValue]
    @State private var opened: String?
    @State private var sending = false
    @State private var link: LinkTarget?

    init(interaction: Interaction) {
        self.interaction = interaction
        let draft = ElicitationDrafts.draft(interaction.uid)
        _values = State(initialValue: draft?.values ?? Elicitation.defaults(interaction.elicitation?.requestedSchema))
        _opened = State(initialValue: draft?.opened)
    }

    private var request: ElicitationRequest? { interaction.elicitation }
    private var schema: JSONValue? { request?.requestedSchema }
    private var isLink: Bool { request?.isURL == true }
    private var canFill: Bool { request?.isForm == true && Elicitation.supported(schema) }
    private var fields: [Elicitation.Field] { Elicitation.fields(schema, order: request?.fieldOrder) }
    private var ready: Bool { Elicitation.resolution(interaction, .accept, values: values, opened: opened) != nil }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            let sources = Elicitation.sourceLines(interaction)
            if !sources.isEmpty {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    ForEach(sources, id: \.self) { Text($0) }
                }
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            }
            if let message = request?.message?.trimmingCharacters(in: .whitespacesAndNewlines), !message.isEmpty {
                Text(Self.inline(message))
                    .font(Theme.Font.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if isLink {
                linkDetails
            } else if canFill {
                VStack(alignment: .leading, spacing: Theme.Space.l) {
                    let errors = Elicitation.validate(schema, values: values).errors
                    ForEach(fields) { f in row(f, error: errors[f.key]) }
                }
            } else {
                Text("This form has fields the app can't show yet. Hand it back to answer it in the terminal on your computer.")
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            buttons
        }
        // One container per card: two cards on screen each have their Decline and Cancel.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("elicitation." + interaction.id)
        .onChange(of: values) { save() }
        .onChange(of: opened) { save() }
        #if os(iOS) || os(visionOS)
        .sheet(item: $link) { t in LinkSheet(target: t, key: interaction.isSessionless ? nil : interaction.sessionKey) }
        #endif
    }

    // MARK: The link

    @ViewBuilder private var linkDetails: some View {
        let target = Elicitation.urlTarget(request?.url)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if (interaction.mcpServerName ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    Text("MCP server: \(String(localized: "Not provided"))")
                }
                Text("Opens: \(Elicitation.urlHost(request?.url) ?? String(localized: "Invalid link"))")
            }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
            Text(verbatim: request?.url ?? "")
                .font(Theme.Font.code)
                .padding(Theme.Space.m)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .textSelection(.enabled)
                .accessibilityIdentifier("elicitation.url")
            Text("Opening the link doesn't finish it. Complete the page, then come back and tap Continue.")
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: Theme.Space.s) {
                Button { open(target) } label: {
                    HStack(spacing: Theme.Space.sm) {
                        ThemedIcon(Theme.Icon.link)
                        Text("Open Link").lineLimit(1)
                    }
                }
                .themedButton(prominent: opened == nil)
                .disabled(target == nil || sending)
                .accessibilityIdentifier("elicitation.open")
                if opened != nil {
                    ThemedIcon(Theme.Icon.done)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(Theme.positive)
                        .accessibilityLabel("Link opened")
                        .accessibilityIdentifier("elicitation.opened")
                }
            }
        }
    }

    /// Opens exactly that link in the link card, and lets Continue on.
    private func open(_ target: String?) {
        guard let target, let url = URL(string: target) else { return }
        if let openLink { openLink(url) } else { link = LinkTarget(url: url) }
        opened = target
    }

    // MARK: The form

    private func row(_ f: Elicitation.Field, error: Elicitation.Problem?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text(f.required ? String(localized: "\(f.title) (required)") : String(localized: "\(f.title) (optional)"))
                .font(Theme.Font.labelStrong)
            if let d = f.description, !d.isEmpty {
                Text(d).font(Theme.Font.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            control(f)
            // What is wrong with a value given; a required field not filled in yet says so in its title.
            if values[f.key] != nil, let error {
                Text(error.text).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                    .accessibilityIdentifier("elicitation.error." + f.key)
            }
            if f.secret {
                Text("Not saved. Type it again if this card closes.").font(Theme.Font.caption).foregroundStyle(.secondary)
            }
            if !f.required, values[f.key] != nil {
                Button { values[f.key] = nil } label: { Text("Clear").lineLimit(1) }
                    .font(Theme.Font.caption)
                    .accessibilityIdentifier("elicitation.clear." + f.key)
            }
        }
    }

    @ViewBuilder private func control(_ f: Elicitation.Field) -> some View {
        if f.type == "boolean" {
            // Yes / No, neither picked until one is: an optional yes/no left alone is not sent.
            SegmentedChoice(f.title, [(value: Bool?.some(true), label: String(localized: "Yes")), (value: Bool?.some(false), label: String(localized: "No"))],
                            selection: Binding(get: { if case .bool(let b)? = values[f.key] { b } else { nil } },
                                               set: { values[f.key] = $0.map { JSONValue.bool($0) } }))
                .accessibilityIdentifier("elicitation.field." + f.key)
        } else if !f.choices.isEmpty {
            let several = f.type == "array"
            VStack(spacing: Theme.Space.flush) {
                ForEach(Array(f.choices.enumerated()), id: \.offset) { i, choice in
                    if i > 0 { Divider().padding(.leading, Theme.Space.l) }
                    ElicitationChoice(label: f.label(choice), picked: picked(f, choice), several: several) { pick(f, choice) }
                        .accessibilityIdentifier("elicitation.choice." + f.key + "." + String(i))
                }
            }
            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
        } else {
            let text = Binding(get: { Self.text(values[f.key]) }, set: { values[f.key] = $0.isEmpty ? nil : .string($0) })
            Group {
                if f.secret {
                    // A key or a password for the MCP server, never offered to the system's passwords (#1598).
                    SecretField(f.title, text: text, identifier: "elicitation.field." + f.key)
                } else {
                    TextField(text: text, prompt: Text(verbatim: Self.example(f) ?? f.title)) { Text(verbatim: f.title) }
                        #if os(iOS) || os(visionOS)
                        .keyboardType(Self.keyboard(f))
                        .textInputAutocapitalization(f.type == "string" && f.format == nil ? .sentences : .never)
                        #endif
                        .accessibilityIdentifier("elicitation.field." + f.key)
                }
            }
            .interactionField()
        }
    }

    private func picked(_ f: Elicitation.Field, _ choice: JSONValue) -> Bool {
        if f.type == "array" { return values[f.key]?.array?.contains(choice) == true }
        return values[f.key] == choice
    }

    private func pick(_ f: Elicitation.Field, _ choice: JSONValue) {
        Haptics.tap()
        guard f.type == "array" else { values[f.key] = choice; return }
        var list = values[f.key]?.array ?? []
        if let i = list.firstIndex(of: choice) { list.remove(at: i) } else { list.append(choice) }
        // In the schema's order, as the choices are listed.
        list = f.choices.filter(list.contains)
        values[f.key] = list.isEmpty ? nil : .array(list)
    }

    // MARK: Answering

    private var buttons: some View {
        VStack(spacing: Theme.Space.s) {
            HStack(spacing: Theme.Space.m) {
                Button { act(.decline) } label: {
                    Text("Decline").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                }
                .themedButton()
                .accessibilityIdentifier("elicitation.decline")
                if isLink || canFill {
                    Button { act(.accept) } label: {
                        Text(isLink ? String(localized: "elicitation.continue", defaultValue: "Continue") : String(localized: "Submit"))
                            .lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                    }
                    .themedButton(prominent: true)
                    .disabled(!ready)
                    .accessibilityIdentifier(isLink ? "elicitation.continue" : "elicitation.submit")
                } else {
                    Button { handBack() } label: {
                        Text("Hand Back to Computer").lineLimit(1).minimumScaleFactor(0.8).frame(maxWidth: .infinity)
                    }
                    .themedButton(prominent: true)
                    .accessibilityIdentifier("elicitation.handback")
                }
            }
            .controlSize(.large)
            Button { act(.cancel) } label: { Text("Cancel").lineLimit(1) }
                #if os(macOS)
                .buttonStyle(.link)
                #endif
                .font(Theme.Font.caption)
                .frame(maxWidth: .infinity)
                .accessibilityIdentifier("elicitation.cancel")
        }
        .disabled(sending)
    }

    private func act(_ action: Elicitation.Action) {
        guard !sending, let resolution = Elicitation.resolution(interaction, action, values: values, opened: opened) else { return }
        sending = true
        // A secret goes once: gone from the card as it is sent.
        for f in fields where f.secret { values[f.key] = nil }
        Task {
            await model.resolve(interaction, resolution)
            sending = false
        }
    }

    private func handBack() {
        guard !sending else { return }
        sending = true
        Task {
            await model.resolve(interaction, .dismissed)
            sending = false
        }
    }

    // MARK: The draft

    private func save() {
        ElicitationDrafts.keep(Elicitation.keepable(Elicitation.Draft(values: values, opened: opened), schema: schema),
                               for: interaction.uid, pending: Set(model.interactions.keys))
    }

    // MARK: Helpers

    /// The message with its inline Markdown (bold, code, links), else as written.
    private static func inline(_ message: String) -> AttributedString {
        (try? AttributedString(markdown: message, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)))
            ?? AttributedString(message)
    }

    /// What a text or number field shows: what was typed, or a default as written.
    private static func text(_ value: JSONValue?) -> String {
        switch value {
        case .string(let s)?: s
        case .number(let n)?: abs(n) < 1e15 && n == n.rounded() ? String(Int64(n)) : String(n)
        default: ""
        }
    }

    /// The shape a date is typed in, where the field has no title of its own to show.
    private static func example(_ f: Elicitation.Field) -> String? {
        switch f.format {
        case "date": "2026-01-01"
        case "date-time": "2026-01-01T09:00:00Z"
        default: nil
        }
    }

    #if os(iOS) || os(visionOS)
    /// A keyboard with a return key throughout: the number pads have none, which leaves no way out of the field.
    private static func keyboard(_ f: Elicitation.Field) -> UIKeyboardType {
        if f.type == "number" || f.type == "integer" { return .numbersAndPunctuation }
        switch f.format {
        case "email": return .emailAddress
        case "uri": return .URL
        case "date", "date-time": return .numbersAndPunctuation
        default: return .default
        }
    }
    #endif
}

/// One choice of a field: a circle with a check when picked, as the question card draws its options.
private struct ElicitationChoice: View {
    var label: String
    var picked: Bool
    var several: Bool
    var action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: Theme.Space.m) {
                ZStack {
                    Circle().fill(picked ? Theme.accent : Theme.Fill.chip)
                    if picked {
                        ThemedIcon(Theme.Icon.done).font(Theme.Font.labelHeavy).dynamicTypeSize(...DynamicTypeSize.xxLarge).foregroundStyle(.white)
                    }
                }
                .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                Text(label).font(Theme.Font.body).foregroundStyle(.primary).multilineTextAlignment(.leading)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
            .background(picked ? Theme.Fill.chip : .clear)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(picked ? .isSelected : [])
        .accessibilityHint(several ? String(localized: "Select multiple") : "")
    }
}

/// What each MCP card has typed and opened, by card (`Elicitation.Draft`), so that folding the card or leaving the chat
/// loses nothing: in memory only, never a secret, forgotten once its card is no longer pending.
enum ElicitationDrafts {
    private static var drafts: [String: Elicitation.Draft] = [:]

    static func draft(_ uid: String) -> Elicitation.Draft? { drafts[uid] }

    static func keep(_ draft: Elicitation.Draft, for uid: String, pending: Set<String>) {
        drafts = drafts.filter { pending.contains($0.key) }
        drafts[uid] = draft
    }
}
