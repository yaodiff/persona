import SwiftUI

/// Claude Code's permission modes and effort levels as the app shows them (server: node/modes.go).
nonisolated enum SessionModes {
    struct Option: Identifiable, Hashable {
        var id: String
        var title: String
        var detail: String
        var symbol: String
    }

    struct SpeedOption: Identifiable, Hashable {
        var id: String
        var title: String
        var detail: String
        var symbol: String
    }

    static let permissionModes: [Option] = [
        Option(id: "default", title: String(localized: "Ask First"), detail: String(localized: "Asks you before editing files or running commands"), symbol: Theme.Icon.askFirst),
        Option(id: "acceptEdits", title: String(localized: "Auto-Edit"), detail: String(localized: "Edits files without asking, still asks before commands"), symbol: Theme.Icon.editFile),
        Option(id: "plan", title: String(localized: "Plan Only"), detail: String(localized: "Reads code and writes a plan, acts only once you approve"), symbol: Theme.Icon.plan),
        Option(id: "auto", title: String(localized: "Auto [Plan Card Permission]", comment: "Permission mode: the agent judges the risk itself"), detail: String(localized: "Judges the risk itself and does safe things right away"), symbol: Theme.Icon.autoMode),
        Option(id: "bypassPermissions", title: String(localized: "Allow Everything"), detail: String(localized: "Never asks. Only available when starting"), symbol: Theme.Icon.allowEverything),
    ]

    /// "auto" is the model's own default level (node/modes.go). Replies are recorded at the level they actually ran at,
    /// so after the next reply the chip shows that level instead.
    static let effortLevels: [Option] = [
        Option(id: "auto", title: String(localized: "Auto", comment: "Thinking effort level"), detail: String(localized: "The model's own default level"), symbol: Theme.Icon.effortAuto),
        Option(id: "low", title: String(localized: "Low", comment: "Thinking effort level"), detail: String(localized: "Fastest, fine for simple things"), symbol: "gauge.with.dots.needle.0percent"),
        Option(id: "medium", title: String(localized: "Medium [Session Modes a567bd]", comment: "Thinking effort level"), detail: String(localized: "A bit faster"), symbol: Theme.Icon.context),
        Option(id: "high", title: String(localized: "High [Session Modes b1c278]", comment: "Thinking effort level"), detail: String(localized: "Default for most tasks"), symbol: Theme.Icon.effort),
        Option(id: "xhigh", title: String(localized: "Extra High", comment: "Thinking effort level"), detail: String(localized: "More careful, but slower and pricier"), symbol: "gauge.with.dots.needle.67percent"),
        Option(id: "max", title: String(localized: "Max", comment: "Thinking effort level"), detail: String(localized: "Slowest and most careful"), symbol: "gauge.with.dots.needle.100percent"),
    ]

    /// Codex's reasoning efforts (node/codex_effort.go); a thread offers its model's, in `Session.efforts`. Their symbols
    /// come from Claude Code's levels without auto (`startEffortLevels`), so `[0]` is low.
    static let codexEffortLevels: [Option] = [
        Option(id: "minimal", title: String(localized: "Minimal", comment: "Thinking effort level"), detail: String(localized: "Barely thinks, fastest"), symbol: startEffortLevels[0].symbol),
        Option(id: "low", title: String(localized: "Low", comment: "Thinking effort level"), detail: String(localized: "Fast, fine for simple things"), symbol: startEffortLevels[0].symbol),
        Option(id: "medium", title: String(localized: "Medium [Session Modes a567bd]", comment: "Thinking effort level"), detail: String(localized: "Balanced speed"), symbol: startEffortLevels[1].symbol),
        Option(id: "high", title: String(localized: "High [Session Modes b1c278]", comment: "Thinking effort level"), detail: String(localized: "More careful"), symbol: startEffortLevels[2].symbol),
        Option(id: "xhigh", title: String(localized: "Extra High", comment: "Thinking effort level"), detail: String(localized: "More careful, but slower and pricier"), symbol: startEffortLevels[3].symbol),
        Option(id: "max", title: String(localized: "Max", comment: "Thinking effort level"), detail: String(localized: "Slowest and most careful"), symbol: startEffortLevels[4].symbol),
        Option(id: "ultra", title: String(localized: "Ultra", comment: "Thinking effort level"), detail: String(localized: "Most careful, and splits work across subagents on its own"), symbol: startEffortLevels[4].symbol),
    ]

    /// Modes a running session can switch to (bypassPermissions only at start, see node/modes.go).
    static let switchableModes = permissionModes.filter { $0.id != "bypassPermissions" }

    /// Levels for something not started yet: auto starts without --effort, which is what Use Launcher's already does.
    static let startEffortLevels = effortLevels.filter { $0.id != "auto" }

    /// Every mode for an assistant conversation (the main one too, new or running): the node restarts its Claude in
    /// Allow Everything (#2416), so the option doesn't say "only when new".
    static let assistantModes = permissionModes.map { o in
        o.id == "bypassPermissions" ? Option(id: o.id, title: o.title, detail: String(localized: "Edits files and runs commands without asking"), symbol: o.symbol) : o
    }

    /// The modes a running session can switch to: every one for an assistant conversation, for Antigravity (`agy`, which
    /// switches directly via ACP, matching the web's `switchableModes`), for an agent that can take Allow Everything while
    /// it runs (`bypass_available`, as the web offers it, #1379) and for one the node restarts in it (`bypassRestarts`,
    /// #6513), `switchableModes` otherwise. Allow Everything says under its name when it does not take effect at once
    /// (`bypassNote`).
    static func switchableModes(_ session: Session?) -> [Option] {
        guard let s = session, s.assistant || s.agent == "agy" || s.bypassAvailable || bypassRestarts(s) else { return switchableModes }
        guard let note = bypassNote(s) else { return assistantModes }
        return assistantModes.map { o in o.id == ModeID.bypass ? Option(id: o.id, title: o.title, detail: note, symbol: o.symbol) : o }
    }

    // MARK: Allow Everything by a restart (#6476 on the web, #6513 here)

    /// Claude Code in a terminal pane the phone types into (the web's `paneClaude`): not the assistant, not a source's
    /// thread, a pane, and the node lets it send and drive the terminal.
    static func paneClaude(_ s: Session) -> Bool {
        !s.assistant && !s.hasSource && s.paneID != nil && s.isClaude && s.canSend && s.canTerminal
    }

    /// Allow Everything for a terminal Claude Code that cannot reach it with shift+tab (started without a bypass flag):
    /// the node restarts the session in it once its turn is over, resuming the same conversation (node/mode_restart.go;
    /// the web's `bypassRestarts`).
    static func bypassRestarts(_ s: Session) -> Bool {
        !s.assistant && s.agent != "agy" && s.canChangeModes && paneClaude(s)
            && !s.bypassAvailable && s.permissionMode != ModeID.bypass
    }

    /// What Allow Everything says under its name where picking it does not take effect at once, nil where it does: the
    /// restart it waits for, or the one it will make; an assistant conversation's next message (the web's `bypassNote`).
    static func bypassNote(_ s: Session) -> String? {
        if s.bypassPending { return pendingNote }
        if bypassRestarts(s) { return String(localized: "Restarts the session to apply it once this turn ends; the conversation carries on") }
        if s.assistant, s.permissionMode != ModeID.bypass { return String(localized: "Takes effect from the next message") }
        return nil
    }

    /// A restart into Allow Everything waits for the turn to end (`bypass_pending`): the mode buttons keep the mode the
    /// agent still runs in and say this beside it.
    static var pendingNote: String { String(localized: "The session restarts in Allow Everything once this turn ends") }
    static func pending(_ s: Session?) -> String? { s?.bypassPending == true ? pendingNote : nil }

    /// `/permissions` in the composer's "/" list (the web's, #6476): where a session's mode can be switched from here,
    /// Persona's own conversations included (all five modes, Allow Everything from the next message), as on the web and
    /// Android. It opens the mode list with every mode, Plan Only included; nothing is sent to the agent.
    static let permissionsCommand = "/permissions"
    static func offersPermissionsCommand(_ s: Session?) -> Bool {
        guard let s else { return false }
        return s.canChangeModes && (s.assistant || !s.setsEffortOnly)
    }

    /// `/permissions` alone in the box, as the list fills it in or as typed.
    static func isPermissionsCommand(_ draft: String) -> Bool {
        draft.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() == permissionsCommand
    }

    /// Whether sending `draft` opens the mode list instead of sending a message: `/permissions` alone, where it is
    /// offered, with nothing attached (photos, videos, quoted contexts, review comments). With anything attached it is a
    /// message like any other, so what was quoted or commented is never dropped (#7996 review).
    static func opensPermissions(_ draft: String, offered: Bool, attached: Bool) -> Bool {
        offered && !attached && isPermissionsCommand(draft)
    }

    struct CommandRow: Identifiable, Hashable {
        var option: Option
        var enabled: Bool
        var id: String { option.id }
    }

    /// The mode list `/permissions` opens: every mode the session can switch to, the current one too when it is not
    /// among them, then Allow Everything disabled with its reason where the agent cannot take it while it runs.
    static func commandRows(_ s: Session) -> [CommandRow] {
        var rows = listed(switchableModes(s), current: s.permissionMode).map { CommandRow(option: $0, enabled: true) }
        if let o = bypassUnavailable(s) { rows.append(CommandRow(option: o, enabled: false)) }
        return rows
    }

    /// The switchable options plus the current one when it is not among them (bypassPermissions, an unknown mode).
    static func listed(_ list: [Option], current: String?) -> [Option] {
        guard let current, !list.contains(where: { $0.id == current }) else { return list }
        return list + [mode(current) ?? .init(id: current, title: current, detail: "", symbol: Theme.Icon.help)]
    }

    // MARK: The modes an agent lists (#8261)

    /// A mode of those a session or a launcher lists (`Session.listedModes`, `StartModes.listed`: Paseo's Grok, OpenCode
    /// or Codex): its own approval is Use Launcher's, which may already allow everything, never Ask First; Allow
    /// Everything says it can be switched back; any other as Claude Code names it, an unknown one by its id.
    static func listedOption(_ id: String) -> Option {
        switch id {
        case ModeID.launcherDefault:
            Option(id: id, title: String(localized: "Use Launcher's"),
                   detail: String(localized: "Its own approval settings decide what it asks, and may already allow everything"),
                   symbol: Theme.Icon.permissions)
        case ModeID.bypass:
            Option(id: id, title: String(localized: "Allow Everything"),
                   detail: String(localized: "Approves everything without asking; can be switched back in the chat"),
                   symbol: Theme.Icon.allowEverything)
        default:
            mode(id) ?? Option(id: id, title: id, detail: "", symbol: Theme.Icon.help)
        }
    }

    /// The listed mode a session runs in: the one it reports when listed, else its own (no mode reported is its own).
    static func listedNow(_ s: Session, listed: [String]) -> String {
        if let m = s.permissionMode, listed.contains(m) { return m }
        return ModeID.launcherDefault
    }

    /// The mode a session runs in as its menus tick it: a listed session's always one of its own (`listedNow`).
    static func current(_ s: Session?) -> String? {
        guard let s else { return nil }
        guard let ids = s.listedModes else { return s.permissionMode }
        return listedNow(s, listed: ids)
    }

    /// The menus' rows for a running session: those a session lists alone (`listedOption`), otherwise the modes it can
    /// switch to plus the current one (`switchableModes`, `listed`).
    static func menuOptions(_ s: Session?) -> [Option] {
        if let ids = s?.listedModes { return ids.map { listedOption($0) } }
        return listed(switchableModes(s), current: s?.permissionMode)
    }

    /// A mode can be picked now: Claude Code's (`canChangeModes`), or one of those the session lists.
    static func switchable(_ s: Session) -> Bool { s.canChangeModes || s.canSwitchListedModes }

    /// The mode chip's symbol: the current mode's, a listed session's own approval the plain permissions symbol;
    /// Ask First's while no mode is known, as the chip has always drawn it.
    static func chipSymbol(_ s: Session?) -> String {
        if s?.listedModes != nil, let now = current(s) { return listedOption(now).symbol }
        return mode(s?.permissionMode)?.symbol ?? Theme.Icon.askFirst
    }

    /// The current mode's name: a listed session's in its own words.
    static func currentLabel(_ s: Session) -> String? {
        guard s.listedModes != nil else { return modeLabel(s.permissionMode) }
        return current(s).map { listedOption($0).title }
    }

    /// "Permission Mode: …" for a session (`modeHeading`), a listed one's mode in its own words.
    static func modeHeading(session s: Session) -> String {
        guard s.listedModes != nil, let label = currentLabel(s) else { return modeHeading(s.permissionMode) }
        return String(localized: "Permission Mode: \(label)")
    }

    /// Session Info's word for the mode the session reported: a listed session's in its own words, no mode being its own.
    static func infoLabel(_ id: String?, session: Session?) -> String? {
        guard let ids = session?.listedModes else { return modeLabel(id) }
        return listedOption(id.flatMap { ids.contains($0) ? $0 : nil } ?? ModeID.launcherDefault).title
    }

    // MARK: The Agents header's mode (#3719)

    /// How much a mode lets the agent do without asking, the tone the header draws its symbol in (`Theme.ModeTone`).
    enum HeaderTone: Equatable, Sendable { case asks, plans, acts, allowsAll, unknown }

    struct HeaderLook: Equatable, Sendable {
        var symbol: String
        var tone: HeaderTone
    }

    /// The header's look for a mode: the mode menu's own symbol, and its tone. A mode not reported yet, or one this app
    /// does not know, is the plain permissions symbol: never a guessed Ask First, which would make an agent started in
    /// Allow Everything look safe.
    static func headerLook(_ id: String?) -> HeaderLook {
        let tone: HeaderTone = switch id ?? "" {
        case "default": .asks
        case "plan": .plans
        case "acceptEdits", "auto": .acts
        case ModeID.bypass: .allowsAll
        default: .unknown
        }
        return HeaderLook(symbol: tone == .unknown ? Theme.Icon.permissions : mode(id)?.symbol ?? Theme.Icon.permissions, tone: tone)
    }

    /// The header's look for a session's mode: a listed session on its own approval (#8261) is the plain permissions
    /// symbol in no tone, as a mode not known, never Ask First's raised hand: its own settings may allow everything.
    static func headerLook(session s: Session) -> HeaderLook {
        let now = current(s)
        if s.listedModes != nil, now == ModeID.launcherDefault { return headerLook(nil as String?) }
        return headerLook(now)
    }

    /// The header shows an agent's mode when there is one to see or to change: Claude Code's, before its first prompt
    /// too (it can be set), and the modes an agent lists (#8261), on its own approval too. Not an assistant conversation
    /// (its page has its own bar), nor an agent whose mode can be neither read nor changed (Codex, TRAE CLI, runtimes
    /// without modes).
    static func showsInHeader(_ s: Session) -> Bool {
        !s.assistant && (s.permissionMode != nil || s.canChangeModes || s.listedModes != nil)
    }

    enum ModeID {
        static let bypass = "bypassPermissions"
        /// Claude Code's Ask First, and the own approval of an agent that lists its modes (#8261, Use Launcher's).
        static let launcherDefault = "default"
    }

    /// What switching a running agent to a mode asks first (#3719): going into Allow Everything, and leaving it when
    /// the agent cannot come back to it from here (`bypass_available` false: only a new start picks it). An assistant
    /// conversation switches without asking, as on the web.
    enum Confirmation: Equatable, Sendable {
        case allowEverything
        case leaveAllowEverything
    }

    /// Allow Everything as a disabled row of the header's menu, saying why, where the agent cannot take it while it runs.
    static func bypassUnavailable(_ s: Session) -> Option? {
        guard !s.assistant, s.agent != "agy", s.canChangeModes, !s.bypassAvailable, s.permissionMode != ModeID.bypass, !bypassRestarts(s) else { return nil }
        return mode(ModeID.bypass)
    }

    /// The modes a session lists (#8261) are a switch both ways: only going into Allow Everything asks.
    /// Assistant conversations and Antigravity (`agy`) switch directly without asking, as on the web.
    static func confirmation(_ s: Session?, to mode: String) -> Confirmation? {
        guard let s, !s.assistant, s.agent != "agy", mode != current(s) else { return nil }
        if mode == ModeID.bypass { return .allowEverything }
        if s.listedModes != nil { return nil }
        if s.permissionMode == ModeID.bypass, !s.bypassAvailable { return .leaveAllowEverything }
        return nil
    }

    /// Whether picking `mode` sends `session.modes`: always for another mode, and for the checked mode while a restart
    /// into Allow Everything waits (`bypass_pending`) so the node drops the restart without switching to a third mode
    /// first (#7996 review).
    static func appliesPick(_ s: Session?, to mode: String) -> Bool {
        mode != current(s) || s?.bypassPending == true
    }

    /// Mirrors the web's `applyModes` optimistic update after `POST …/modes` succeeds: a restart into Allow Everything
    /// sets `bypassPending` while keeping the running mode; any other permission pick updates `permissionMode` and
    /// clears `bypassPending` (#7996 review).
    static func applyOptimistic(_ s: inout Session, permissionMode: String) {
        if permissionMode == ModeID.bypass, bypassRestarts(s) {
            s.bypassPending = true
        } else {
            s.permissionMode = permissionMode
            s.bypassPending = false
        }
    }

    static func mode(_ id: String?) -> Option? { permissionModes.first { $0.id == id } }
    static func effort(_ id: String?) -> Option? { effortLevels.first { $0.id == id } ?? codexEffortLevels.first { $0.id == id } }

    /// A running session's effort choices: its own levels when the hub lists them (a Codex thread on its app-server),
    /// Claude Code's otherwise; a level this app does not know yet keeps its id.
    static func effortOptions(_ session: Session?) -> [Option] {
        guard let ids = session?.efforts, !ids.isEmpty else { return effortLevels }
        return ids.map { id in codexEffortLevels.first { $0.id == id } ?? Option(id: id, title: id, detail: "", symbol: Theme.Icon.effort) }
    }

    /// Current-conversation Codex speed, only for the main Assistant chat and only from advertised native tiers. The
    /// native names are not interface copy: Standard and Fast use the same plain localized labels as the web.
    static func speedOptions(_ session: Session?, mainConversation: Bool) -> [SpeedOption] {
        guard mainConversation, session?.assistant == true, let tiers = session?.serviceTiers else { return [] }
        let ids = Set(tiers.map(\.id))
        var options: [SpeedOption] = []
        if ids.contains("default") {
            options.append(SpeedOption(id: "default", title: String(localized: "Standard"), detail: "", symbol: Theme.Icon.topicPerformance))
        }
        if ids.contains("priority") {
            options.append(SpeedOption(id: "priority", title: String(localized: "Fast"), detail: String(localized: "Uses more quota"), symbol: Theme.Icon.topicPerformance))
        }
        return options
    }

    /// Why a visible Speed action cannot be changed. Working is not a restriction: the acknowledged tier applies next turn.
    static func speedUnavailable(_ session: Session) -> String? {
        if session.state == .offline { return StateName.notRunning.text }
        if session.starting == true { return String(localized: "Starting [Session Modes 087739]") }
        return session.canSend ? nil : String(localized: "This session is view-only")
    }

    /// The model can be switched on the running session (#3119): Claude Code in a herdr pane, which takes `/model`, or a
    /// session whose node takes a model (`Session.switchesModel`, #6294). Codex and TRAE CLI in a terminal pick theirs in a
    /// full-screen menu the phone cannot drive; the assistant's has its own chip.
    static func switchesModel(_ s: Session?) -> Bool { s?.switchesModel == true }

    /// Whether the menus offer model rows now: Claude Code in a pane always (its `/model` aliases until its launcher's list
    /// is read); a session whose node takes the model once that list is read, since the fixed aliases are Claude Code's
    /// words, not its runtime's (`Session.picksModelByModes`). `list`: `RunningModelLists.models(_:_:)`.
    static func offersModels(_ s: Session?, list: [AgentModel]?) -> Bool {
        switchesModel(s) && (s?.picksModelByModes != true || list != nil)
    }

    /// Why the model cannot be switched right now (nil when it can): the node refuses the same cases.
    static func modelBusy(_ s: Session?) -> String? {
        switch s?.state {
        case .working?: String(localized: "Working. Switch models after it stops.")
        case .needsYou?: String(localized: "Waiting for your answer. Switch models after you reply.")
        case .offline?: StateName.notRunning.text
        default: s?.starting == true ? String(localized: "Starting [Session Modes 087739]") : nil
        }
    }

    /// Use a named launcher's own catalog. A manually started Codex may report only its engine and surface.
    static func launcher(_ s: Session?) -> String {
        if let launcher = s?.launcher, !launcher.isEmpty { return launcher }
        guard let s, s.isCodex else { return "claude" }
        return s.spaceKind == Launcher.codexAppSource || s.agentSurface == "desktop" ? "codex-app" : "codex"
    }

    /// What applying a model pick sends: the alias, unless the session already runs it (Default too, while it runs a
    /// typed ID: that is not the default, #1644).
    @MainActor static func modelPatch(_ picked: String, current: String?, list: [AgentModel]? = nil) -> String? {
        picked == PromptItems.runningModelID(current, list: list) ? nil : picked
    }

    static func modeTint(_ id: String?) -> Color {
        switch id {
        // Neutral: amber is the working state and would read as Waiting for you. Only "allow everything" warns.
        case "bypassPermissions": Theme.danger
        default: .secondary
        }
    }

    /// "Ask First" for known modes, the raw value otherwise (Codex approval policies, newer Claude Code modes).
    /// For a mode or effort the session has not reported.
    static var unknown: String { String(localized: "Unknown [Session Modes 4d8c1c]") }
    static func modeLabel(_ id: String?) -> String? { id.map { mode($0)?.title ?? $0 } }
    /// The permission mode menu's title and its button's accessibility label: "Permission Mode: Ask First" for a mode
    /// the session reported, just "Permission Mode" before it has one (#5596), as the header capsule shows it. An agent
    /// has no mode before its first prompt (it comes from the transcript) though the mode can already be changed.
    static func modeHeading(_ id: String?) -> String {
        guard let label = modeLabel(id), !label.isEmpty else { return String(localized: "Permission Mode") }
        return String(localized: "Permission Mode: \(label)")
    }
    static func effortLabel(_ id: String?) -> String? { id.map { effort($0)?.title ?? $0 } }

    struct EffortChange: Equatable, Sendable {
        var label: String
        var failed: Bool
        var symbol: String { failed ? Theme.Icon.error : Theme.Icon.time }
        var spoken: String {
            failed ? String(localized: "Thinking effort change failed") : String(localized: "Thinking effort after this turn: \(label)")
        }
    }

    static func effortChange(_ session: Session?) -> EffortChange? {
        guard let label = effortLabel(session?.pendingEffort) else { return nil }
        return EffortChange(label: label, failed: session?.effortChangeFailed == true)
    }

    /// The shell composer's model button's two words (#1091, #1117), ChatGPT's "6.1 Sol Medium": the model, drawn in the text's
    /// ink, and the effort, in the quieter ink after it.
    struct ComposerTitle: Equatable, Sendable {
        var model: String
        var effort: String?
        /// The workflow mark after the effort (#1307): ultracode on, or a Codex thread at "ultra".
        var mark: Ultracode.Mark? = nil
        var effortChange: EffortChange? = nil
        /// For VoiceOver and the UI tests: "Opus 5.5 High", "Opus 5.5 High, ultracode on".
        var spoken: String {
            let words = effort.map { "\(model) \($0)" } ?? model
            return [words, mark?.spoken, effortChange?.spoken].compactMap { $0 }
                .joined(separator: String(localized: ", ", comment: "List separator"))
        }
    }

    /// "Opus 5.5" + "High"; the model alone; "Thinking" + "High" with no model known; nil with neither.
    static func composerTitle(model: String?, effort: String?) -> ComposerTitle? {
        switch (model, effort) {
        case let (m?, e): ComposerTitle(model: m, effort: e)
        case let (nil, e?): ComposerTitle(model: String(localized: "Thinking [Message Views 8cf010]", comment: "Composer button title when no model is known: thinking effort follows"), effort: e)
        case (nil, nil): nil
        }
    }

    /// GPT display names only: preserve the GPT- prefix, version, and every variant without changing the agent wire id.
    static func gptModelName(_ model: String?) -> String? {
        guard let raw = Fmt.model(model)?.trimmingCharacters(in: .whitespacesAndNewlines),
              raw.lowercased().hasPrefix("gpt-") else { return nil }
        let parts = raw.dropFirst(4).split(separator: "-", omittingEmptySubsequences: false)
        guard !parts.isEmpty, parts.allSatisfy({ !$0.isEmpty }) else { return nil }
        return "GPT-" + parts.map { part in
            guard let first = part.first, first.isLetter else { return String(part) }
            return String(first).uppercased() + part.dropFirst().lowercased()
        }.joined(separator: " ")
    }

    /// The composer displays effort separately from Gemini's family and variant.
    private static func geminiModelName(_ raw: String) -> String? {
        let id = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard id.hasPrefix("gemini-") else { return nil }
        var parts = id.split(separator: "-", omittingEmptySubsequences: false)
        guard parts.allSatisfy({ !$0.isEmpty && !$0.contains(where: \.isWhitespace) }) else { return nil }
        if parts.count > 2, let suffix = parts.last, ["low", "medium", "high", "xhigh", "max"].contains(String(suffix)) {
            parts.removeLast()
        }
        return parts.map { $0.capitalized }.joined(separator: " ")
    }

    /// Model display names for the composer. The session keeps its original model ID.
    static func modelName(_ model: String?) -> String? {
        guard let model, !model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              let raw = Fmt.model(model) else { return nil }
        if let gpt = gptModelName(raw) { return gpt }
        if let gemini = geminiModelName(raw) { return gemini }
        // A context-window suffix ("[1m]") is not part of the version; the name keeps no 1M marker.
        let lower = raw.lowercased().replacingOccurrences(of: #"\[[^\]]*\]$"#, with: "", options: .regularExpression)
        for family in ["fable", "opus", "sonnet", "haiku"] {
            guard let r = lower.range(of: family) else { continue }
            // The version follows the family in today's ids; a date (8 digits) and older ids' leading version do not.
            let after = lower[r.upperBound...].split { $0 == "-" || $0 == "." || $0 == "_" }
            let version = after.prefix { $0.allSatisfy(\.isNumber) && $0.count <= 2 }.prefix(2).joined(separator: ".")
            return family.capitalized + (version.isEmpty ? "" : " " + version)
        }
        return raw
    }
}

/// The two chips above the composer — permission mode and effort level, like the footer of Claude Code's TUI.
/// Tapping one changes it on the running session. A Codex thread on its app-server gets the effort alone, among its own
/// levels (`Session.setsEffortOnly`).
struct SessionModeBar: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?
    /// A mode picked here goes through the page's `requestMode` (which asks first where `SessionModes.confirmation`
    /// says so); nil applies it at once.
    var pick: ((String) -> Void)? = nil
    @State private var changing = false
    /// "Other Model…": a model ID typed by hand (#1644).
    @State private var typingModel = false

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SessionModeBar")
        #endif
        // A session that lists its own modes (#8261) is always in one of them: no mode reported is its own approval.
        let mode = SessionModes.current(session)
        let effort = session?.effort
        let effortChange = SessionModes.effortChange(session)
        HStack(spacing: Theme.Space.s) {
            if session?.setsEffortOnly != true || session?.canSwitchListedModes == true {
                Menu {
                    // A restart into Allow Everything waits for the turn to end (#6513): said over the modes, the chip keeps
                    // the mode it runs in.
                    if let pending = SessionModes.pending(session) { Text(pending) }
                    ForEach(SessionModes.menuOptions(session)) { o in
                        MenuChoice(id: o.id, selected: mode, reselect: session?.bypassPending == true, pick: { change(mode: $0) }) {
                            Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }
                        }
                    }
                } label: {
                    ModeChip(symbol: SessionModes.chipSymbol(session),
                             text: session.flatMap { SessionModes.currentLabel($0) } ?? String(localized: "Permission Mode"), tint: SessionModes.modeTint(mode), known: mode != nil)
                }
                .labelOnlyMenu()
                .accessibilityLabel(session.map { SessionModes.modeHeading(session: $0) } ?? SessionModes.modeHeading(mode))
            }
            Menu {
                // Claude Code's ultracode (#1307): its own switch under the effort, not a level of it (declared before it where
                // the menu lists its items the other way round, `UltracodeMenuSection.leadsEffort`).
                if UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
                Picker("Thinking Effort", selection: Binding(get: { effort ?? "" }, set: { new in change(effort: new) })) {
                    ForEach(SessionModes.effortOptions(session)) { o in
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                    }
                }
                .pickerStyle(.inline)
                if let effortChange {
                    Label(effortChange.spoken, systemImage: effortChange.symbol)
                }
                if let effort, effortChange != nil || model.canKeepOutboxEffort(key, effort: effort) {
                    Button("Keep Current Effort") { Task { await model.keepCurrentEffort(key, effort: effort) } }
                }
                if !UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
                // The model in the same chip (#3119, the web's #2434): Claude Code takes `/model`, typed by the node.
                // A session whose node takes the model (#6294) offers its launcher's list once read (`offersModels`).
                let list = RunningModelLists.shared.models(key, session)
                if SessionModes.offersModels(session, list: list) {
                    let busy = SessionModes.modelBusy(session)
                    Section("Model [Assistant View c98e11]") {
                        // A typed model (#1644) is a row of its own, by its ID: no alias is ticked for it.
                        Picker("Model [Assistant View c98e11]", selection: Binding(get: { PromptItems.runningModelID(session?.model, list: list) }, set: { id in change(model: id) })) {
                            ForEach(PromptItems.runningModels(session?.model, list: list), id: \.id) { m in
                                Text(m.name).tag(m.id)
                            }
                        }
                        .pickerStyle(.inline)
                        .disabled(busy != nil)
                        Button { typingModel = true } label: { Label("Other Model…", systemImage: Theme.Icon.keyboard) }
                            .disabled(busy != nil)
                            .accessibilityIdentifier("sessionModes.otherModel")
                        // A menu draws no section footers: the note is a row of its own (not tappable).
                        Text(busy ?? String(localized: "The model you pick also becomes the default for new sessions"))
                    }
                }
            } label: {
                ModeChip(symbol: SessionModes.effort(effort)?.symbol ?? Theme.Icon.effort,
                         text: effort.map { String(localized: "Thinking \(SessionModes.effortLabel($0) ?? $0) [Session Modes e77236]") } ?? String(localized: "Thinking Effort"), tint: .secondary, known: effort != nil,
                         mark: Ultracode.chipMark(session), effortChange: effortChange)
            }
            .labelOnlyMenu()
            .accessibilityLabel(effortSpoken)
            if changing { ProgressView().controlSize(.mini) }
            Spacer(minLength: 0)
        }
        .disabled(changing)
        .animation(Theme.Motion.standard, value: changing)
        .sheet(isPresented: $typingModel) { SessionTypedModelSheet(key: key, session: session) }
    }

    /// The effort chip read out, with ", ultracode on" while it carries the mark (#1307).
    private var effortSpoken: String {
        let effort = session?.effort
        let words = SessionModes.offersModels(session, list: RunningModelLists.shared.models(key, session))
            ? String(localized: "Thinking effort and model: \(SessionModes.effortLabel(effort) ?? String(localized: "Unknown [Session Modes 4d8c1c]")), \(SessionModes.modelName(session?.model) ?? String(localized: "Unknown model"))")
            : String(localized: "Thinking effort: \(SessionModes.effortLabel(effort) ?? String(localized: "Unknown [Session Modes 4d8c1c]"))")
        return [words, Ultracode.chipMark(session)?.spoken, SessionModes.effortChange(session)?.spoken]
            .compactMap { $0 }.joined(separator: String(localized: ", ", comment: "List separator"))
    }

    private func change(mode: String? = nil, effort: String? = nil, model picked: String? = nil) {
        if let mode, let pick {
            if SessionModes.appliesPick(session, to: mode) { pick(mode) }
            return
        }
        let list = RunningModelLists.shared.models(key, session)
        let newModel = picked.flatMap { SessionModes.modelPatch($0, current: session?.model, list: list) }
        if picked != nil, newModel == nil { return } // the model it runs already
        let modeChanged = mode.map { SessionModes.appliesPick(session, to: $0) } ?? false
        let effortChanged = effort.map { $0 != session?.effort || session?.pendingEffort != nil } ?? false
        guard modeChanged || effortChanged || newModel != nil, !changing else { return }
        changing = true
        Task {
            await model.setModes(key, permissionMode: mode, effort: effort, model: newModel)
            changing = false
        }
    }
}

/// The shell composer's model menu for a running conversation (#1091): the model (a choice where Claude Code takes
/// `/model`, otherwise the one it runs), the effort, and the permission mode a level down. Effort and mode are the same
/// choices as the `SessionModeBar` used above platform composers.
struct SessionModesMenu: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?
    /// Claude Code in a herdr pane: picking a model sends `/model <alias>`; a session whose node takes the model (#6294,
    /// `Session.picksModelByModes`) gets the pick through POST …/modes.
    var picksModel = false
    /// Effort and permission mode can change (Claude Code, the assistant; not Codex or TRAE CLI).
    var setsModes = true
    /// Only the effort can change, among the session's own levels (a Codex thread on its app-server, `Session.setsEffortOnly`).
    var setsEffortOnly = false
    /// The hub-named main Assistant conversation: the only place this menu offers current-conversation Codex Speed.
    var mainConversation = false
    /// "Other Model…" where a model can be picked: the composer opens `SessionTypedModelSheet` (#1644).
    var otherModel: (() -> Void)? = nil
    /// An Assistant conversation: opens its own model sheet (#6542, the per-conversation pick, #5033). Nil on
    /// older hubs, which keep the row read-only.
    var assistantPick: (() -> Void)? = nil
    /// What this conversation's next message runs on, from the same effective setup as the composer title (#6911 M7):
    /// the menu row names this, not the process's last model. Nil when the conversation picker is unavailable.
    var assistantModelName: String? = nil
    /// A mode picked here goes through the page's `requestMode` (`SessionModeBar.pick`); nil applies it at once.
    var pick: ((String) -> Void)? = nil

    /// The button's title for this conversation; the effort carries the workflow mark while ultracode is on (#1307).
    static func title(_ session: Session?, setsModes: Bool) -> SessionModes.ComposerTitle? {
        let showsEffort = setsModes || session?.sourceKind == Launcher.claudeDesktopSource
        var title = SessionModes.composerTitle(model: SessionModes.modelName(session?.model),
                                               effort: showsEffort ? SessionModes.effortLabel(session?.effort) : nil)
        if setsModes {
            title?.mark = Ultracode.chipMark(session)
            title?.effortChange = SessionModes.effortChange(session)
        }
        return title
    }

    var body: some View {
        let mode = session?.permissionMode
        let effort = session?.effort
        let speedOptions = SessionModes.speedOptions(session, mainConversation: mainConversation)
        let selectedSpeed = session?.serviceTier ?? ""
        let changingSpeed = model.serviceTierChanges.contains(key)
        // The modes an agent that is not Claude Code's lists (#8261): its own approval and Allow Everything, a switch both ways.
        let listsModes = !setsModes && session?.canSwitchListedModes == true
        // Allow Everything is said here, not on the composer's button (#1117). First: a menu opening upward from the button puts
        // its first item next to it, where a menu too tall for the screen is never cut off.
        if setsModes || listsModes, mode == "bypassPermissions" {
            Section { BypassWarning() }
        }
        Section("Model [Assistant View c98e11]") {
            if picksModel {
                let list = RunningModelLists.shared.models(key, session)
                // First, next to the button the menu opens up from: a launcher's list can be long enough to scroll
                // (#3754), and the last item would be out of reach at the far end.
                if let otherModel {
                    // Goes through POST …/modes (the node's checks), not a typed `/model` message.
                    let busy = SessionModes.modelBusy(session)
                    Button(action: otherModel) {
                        Label { Text("Other Model…"); if let busy { Text(busy) } } icon: { ThemedIcon(Theme.Icon.keyboard) }
                    }
                    .disabled(busy != nil)
                    .accessibilityIdentifier("sessionModes.otherModel")
                }
                // A typed model (#1644) is a row of its own, by its ID: no alias is ticked for it, and Default switches back.
                // Switches, not an inline picker, so the section's title is drawn over them (#4322, `MenuChoice`).
                let running = PromptItems.runningModelID(session?.model, list: list)
                ForEach(PromptItems.runningModels(session?.model, list: list), id: \.id) { m in
                    MenuChoice(id: m.id, selected: running, pick: { id in
                        guard case .send(let text) = PromptItems.modelPick(id, running: session?.model, list: list) else { return }
                        // A session whose node takes the model (#6294) gets it through POST …/modes, never as a message.
                        if session?.picksModelByModes == true { Task { await model.setModes(key, model: id) }; return }
                        model.sendNow(key, text: text, images: []) // photos and quotes stay for the message
                    }) {
                        Text(m.name)
                    }
                }
            } else if let assistantPick {
                // The current Assistant conversation (#6542): this row opens its own model sheet (#5033).
                // It names the next message's effective model, the one source the composer title uses (#6911 M7).
                Button(action: assistantPick) {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("Change Model…")
                                .font(Theme.Font.heading)
                                .lineLimit(1)
                            Text(assistantModelName ?? SessionModes.modelName(session?.model) ?? String(localized: "Unknown model"))
                                .font(Theme.Font.label)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.agentChat)
                    }
                }
                .accessibilityIdentifier("sessionModes.assistantModel")
            } else {
                Text(SessionModes.modelName(session?.model) ?? String(localized: "Unknown model")) // what it runs; changed where it was started
            }
        }
        if !speedOptions.isEmpty {
            let unavailable = session.flatMap { SessionModes.speedUnavailable($0) }
            Section {
                ForEach(speedOptions) { option in
                    let traits: AccessibilityTraits = option.id == selectedSpeed ? .isSelected : []
                    MenuChoice(id: option.id, selected: selectedSpeed, pick: changeSpeed) {
                        Label { Text(option.title) } icon: { ThemedIcon(option.symbol) }
                    }
                    .accessibilityAddTraits(traits)
                    .accessibilityIdentifier("sessionModes.speed.\(option.id)")
                    if !option.detail.isEmpty { Text(option.detail) }
                }
                Text("Applies next turn")
                if changingSpeed { Text("Changing Speed…").lineLimit(1) }
                if let unavailable { Text(unavailable).lineLimit(1) }
            } header: {
                Text("Speed").accessibilityIdentifier("sessionModes.speed")
            }
            .disabled(changingSpeed || unavailable != nil)
        }
        if setsModes || setsEffortOnly {
            // Claude Code's ultracode (#1307): its own switch under the effort, not a level of it (declared before it where
            // the menu lists its items the other way round, `UltracodeMenuSection.leadsEffort`).
            if UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
            Section("Thinking Effort") { // switches, so the title is drawn (#4322, `MenuChoice`)
                let effortChange = SessionModes.effortChange(session)
                if let effortChange {
                    Label(effortChange.spoken, systemImage: effortChange.symbol)
                }
                if let effort, effortChange != nil || model.canKeepOutboxEffort(key, effort: effort) {
                    Button("Keep Current Effort") { Task { await model.keepCurrentEffort(key, effort: effort) } }
                }
                ForEach(SessionModes.effortOptions(session)) { o in
                    MenuChoice(id: o.id, selected: effort ?? "", pick: { change(effort: $0) }) {
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }
                    }
                }
            }
            if !UltracodeMenuSection.leadsEffort { UltracodeMenuSection(key: key, session: session) }
        } else if session?.sourceKind == Launcher.claudeDesktopSource {
            Section("Thinking Effort") {
                Text(SessionModes.effortLabel(effort) ?? String(localized: "Unknown [Session Modes 4d8c1c]"))
            }
        }
        if setsModes {
            // A restart into Allow Everything waits for the turn to end (#6513): said by the mode's row, which keeps the
            // mode it runs in.
            if let pending = SessionModes.pending(session) { Text(pending) }
            Menu {
                let list = SessionModes.switchableModes(session)
                ForEach(list + (mode.flatMap { m in list.contains { $0.id == m } ? nil : SessionModes.mode(m) }.map { [$0] } ?? [])) { o in
                    MenuChoice(id: o.id, selected: mode, reselect: session?.bypassPending == true, pick: { change(mode: $0) }) {
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }
                    }
                }
            } label: {
                Label(SessionModes.modeHeading(mode), systemImage: SessionModes.mode(mode)?.symbol ?? Theme.Icon.askFirst)
            }
        } else if listsModes, let s = session {
            Picker(selection: Binding(get: { SessionModes.current(s) ?? "" }, set: { new in change(mode: new) })) {
                ForEach(SessionModes.menuOptions(s)) { o in
                    Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id)
                }
            } label: {
                Label(SessionModes.modeHeading(session: s), systemImage: SessionModes.chipSymbol(s))
            }
            .pickerStyle(.menu)
        }
    }

    private func changeSpeed(_ tier: String) {
        guard tier != session?.serviceTier, !model.serviceTierChanges.contains(key) else { return }
        Task { await model.setServiceTier(key, tier: tier) }
    }

    private func change(mode: String? = nil, effort: String? = nil) {
        if let mode, let pick {
            if SessionModes.appliesPick(session, to: mode) { pick(mode) }
            return
        }
        let modeChanged = mode.map { SessionModes.appliesPick(session, to: $0) } ?? false
        let effortChanged = effort.map { $0 != session?.effort || session?.pendingEffort != nil } ?? false
        guard modeChanged || effortChanged else { return }
        Task { await model.setModes(key, permissionMode: mode, effort: effort) }
    }
}

/// "Other Model…" for a running Claude Code session (#1644): a model ID typed by hand, with the IDs typed before for its
/// launcher on that computer, goes through POST …/modes (the node types `/model <id>` behind its own checks), never as a
/// message.
struct SessionTypedModelSheet: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var session: Session?

    var body: some View {
        TypedModelSheet(machine: key.machineRef, launcher: SessionModes.launcher(session),
                        initial: PromptItems.unlistedModel(session?.model, list: RunningModelLists.shared.models(key, session)) ?? "",
                        note: String(localized: "The model you pick also becomes the default for new sessions")) { id in
            Task { await model.setModes(key, model: id) }
        }
    }
}

/// The model menu's warning while everything is allowed (Allow Everything): red (a destructive item: its words and its shield),
/// as the composer's button used to be. Picking it does nothing; the permission mode is chosen a few rows up.
struct BypassWarning: View {
    var body: some View {
        Button(role: .destructive) {} label: {
            Label {
                Text("Allow Everything")
                Text("Edits files and runs commands without asking")
            } icon: {
                // A menu draws its images in the accent unless they carry their own colors.
                ThemedIcon(Theme.Icon.allowEverything).symbolRenderingMode(.palette).foregroundStyle(Theme.danger)
            }
        }
        .tint(Theme.danger)
    }
}

/// One of a menu's choices as a switch, on for the current one (#3719, #4322). An iOS menu draws no section header over
/// an inline picker's options, whether the section is around the picker or inside it, and does over switches
/// (docs/lessons/ios.md): a titled list of choices is these in a titled `Section`. Picking the current one again does
/// nothing unless `reselect` is true (e.g. calling off a queued restart into Allow Everything, #7996 review); another
/// one calls `pick` with its ID.
struct MenuChoice<Content: View>: View {
    var id: String
    var selected: String?
    var reselect = false
    var pick: (String) -> Void
    @ViewBuilder var label: () -> Content

    var body: some View {
        Toggle(isOn: Binding(get: { id == selected }, set: { on in
            if (on && id != selected) || (!on && reselect && id == selected) { pick(id) }
        })) { label() }
    }
}

/// The agent's permission mode at the end of the Agents header's second row (#3719), as Claude Code keeps its mode in its
/// footer: the mode's symbol in its tone (`SessionModes.headerLook`), Allow Everything in the warning red. Its words are in
/// the menu, the VoiceOver label and the pointer's tooltip. A tap opens the mode menu; a pick goes through `pick` (the
/// page's `requestMode`, which asks first where `SessionModes.confirmation` says so).
struct HeaderModeButton: View, Equatable {
    var session: Session
    var changing = false
    var pick: (String) -> Void

    /// The header re-evaluates on every hub push for its session (its rev, activity, tokens…); the button only when what
    /// it draws or offers changes. `pick` is the page's `requestMode`, which reads the session when it runs.
    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.changing == b.changing && a.session.agent == b.session.agent
            && a.session.permissionMode == b.session.permissionMode
            && a.session.canChangeModes == b.session.canChangeModes && a.session.bypassAvailable == b.session.bypassAvailable
            && a.session.assistant == b.session.assistant && a.session.status.isLive == b.session.status.isLive
            && a.session.bypassPending == b.session.bypassPending
            && SessionModes.bypassRestarts(a.session) == SessionModes.bypassRestarts(b.session)
            && a.session.listedModes == b.session.listedModes && a.session.canSwitchListedModes == b.session.canSwitchListedModes
    }

    /// The capsule is as tall as the row's tabs; its 44 pt target overhangs the row by this much above and below.
    static let overhang = (Theme.Size.tapTarget - Theme.Size.wellS) / 2

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("HeaderModeButton")
        #endif
        // A session that lists its own modes (#8261) has those two rows alone, one of them always on: no mode reported is
        // its own approval, drawn as a mode not known (`headerLook(session:)`).
        let mode = SessionModes.current(session)
        let words = SessionModes.modeHeading(session: session)
        let switchable = SessionModes.switchable(session)
        // A restart into Allow Everything waits for the turn to end (#6513): the capsule keeps the mode it runs in, and
        // the menu's title, VoiceOver and the pointer's tooltip say the restart is coming.
        let pending = SessionModes.pending(session)
        Menu {
            // "Permission Mode: …" heads the list, as on the web and Android. The modes are switches, the current one on:
            // an iOS menu draws no section header over an inline picker's options, whether the section is around the
            // picker or inside it (docs/lessons/ios.md), and does over switches (the composer's ultracode section).
            Section {
                ForEach(SessionModes.menuOptions(session)) { o in
                    MenuChoice(id: o.id, selected: mode, reselect: session.bypassPending, pick: pick) {
                        Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }
                    }
                }
                .disabled(!switchable)
                // Allow Everything where this agent cannot take it while it runs: there, and why under it (a menu draws
                // no footers; it keeps an item's second `Text`, not a `Label`'s).
                if let o = SessionModes.bypassUnavailable(session) {
                    Button {} label: { Text(o.title); Text(o.detail); Theme.Icon.image(o.symbol) }.disabled(true)
                }
            } header: {
                // Why nothing can be picked goes under the title: words, not a grayed item.
                Text([words, !switchable && !session.status.isLive ? StateName.notRunning.text : nil, pending].compactMap { $0 }.joined(separator: "\n"))
            }
        } label: {
            HeaderModeCapsule(look: SessionModes.headerLook(session: session), changing: changing)
        }
        .labelOnlyMenu()
        .disabled(changing)
        .accessibilityLabel(words)
        .accessibilityHint(pending ?? "")
        .accessibilityIdentifier("chat.permissionMode")
        .help(pending.map { words + "\n" + $0 } ?? words)
        .padding(.vertical, -Self.overhang)
    }
}

/// `HeaderModeButton`'s drawing: an icon-only capsule as tall as the header's tabs, tinted as `ModeChip` tints a mode
/// that is not the quiet one; a small spinner in place of the symbol while a change is on its way.
struct HeaderModeCapsule: View {
    var look: SessionModes.HeaderLook
    var changing = false

    static func color(_ tone: SessionModes.HeaderTone) -> Color {
        switch tone {
        case .asks, .unknown: .secondary
        case .plans: Theme.ModeTone.plans
        case .acts: Theme.ModeTone.acts
        case .allowsAll: Theme.ModeTone.allowsAll
        }
    }

    var body: some View {
        let tone = Self.color(look.tone)
        let tinted = look.tone != .asks && look.tone != .unknown
        ZStack {
            ThemedIcon(look.symbol).font(Theme.Font.labelStrong).foregroundStyle(tone).opacity(changing ? 0 : 1)
            if changing { ProgressView().controlSize(.mini) }
        }
        .padding(.horizontal, Theme.Space.m)
        .scaledFrame(height: Theme.Size.wellS)
        .background(tinted ? AnyShapeStyle(tone.opacity(0.12)) : AnyShapeStyle(Theme.Fill.chip), in: .capsule)
        .frame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Size.tapTarget)
        .contentShape(.rect)
        .animation(Theme.Motion.standard, value: changing)
        .animation(Theme.Motion.standard, value: look)
    }
}

/// One tappable chip of `SessionModeBar` / the assistant's starter row.
struct ModeChip: View {
    var symbol: String
    var text: String
    var tint: Color
    var known = true
    /// The workflow mark after the label (#1307): ultracode on, or a Codex thread at "ultra". The chip's menu says it
    /// to VoiceOver (`SessionModeBar.effortSpoken`).
    var mark: Ultracode.Mark? = nil
    var effortChange: SessionModes.EffortChange? = nil

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(symbol).font(Theme.Font.labelStrong)
            Text(text).lineLimit(1)
            if mark != nil {
                UltracodeMark(font: Theme.Font.labelStrong, spoken: nil).transition(.opacity)
            }
            if let effortChange {
                ThemedIcon(effortChange.symbol).foregroundStyle(.secondary).accessibilityHidden(true)
            }
            ThemedIcon(Theme.Icon.choices).font(Theme.Font.labelHeavy).imageScale(.small).opacity(0.6)
        }
        .animation(UltracodeMark.motion, value: mark)
        .font(Theme.Font.labelMedium)
        .foregroundStyle(known ? (tint == .secondary ? Color.primary : tint) : Color.secondary)
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.sm)
        .background((known && tint != .secondary ? tint : Color.primary).opacity(known && tint != .secondary ? 0.12 : 0.06), in: .capsule)
        .contentShape(.capsule)
    }
}

/// Mode and effort for something not started yet (a new agent or assistant conversation): "" keeps the launcher's own.
struct NewSessionModePicker: View {
    @Binding var mode: String
    @Binding var effort: String
    var modes: [SessionModes.Option] = SessionModes.permissionModes
    var efforts: [SessionModes.Option] = SessionModes.startEffortLevels

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            Menu {
                Picker("Permission Mode", selection: $mode) {
                    Text("Use Launcher's").tag("")
                    ForEach(modes) { o in Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id) }
                }
                .pickerStyle(.inline)
            } label: {
                ModeChip(symbol: SessionModes.mode(mode)?.symbol ?? Theme.Icon.askFirst,
                         text: SessionModes.mode(mode)?.title ?? String(localized: "Permissions: Default"), tint: SessionModes.modeTint(mode), known: !mode.isEmpty)
            }
            .labelOnlyMenu()
            Menu {
                Picker("Thinking Effort", selection: $effort) {
                    Text("Use Launcher's").tag("")
                    ForEach(efforts) { o in Label { Text(o.title); Text(o.detail) } icon: { ThemedIcon(o.symbol) }.tag(o.id) }
                }
                .pickerStyle(.inline)
            } label: {
                ModeChip(symbol: SessionModes.effort(effort)?.symbol ?? Theme.Icon.effort,
                         text: SessionModes.effort(effort).map { String(localized: "Thinking \($0.title) [Session Modes e77236]") } ?? String(localized: "Thinking: Default"), tint: .secondary, known: !effort.isEmpty)
            }
            .labelOnlyMenu()
        }
    }
}

/// The mode list `/permissions` opens from the composer (#6513, the web's #6476): a menu can't be opened from code, so the
/// same choices as the header's mode menu (`SessionModes.commandRows`), Plan Only included, in a short sheet. The current
/// mode is checked; Allow Everything says under its name when it takes effect later (a restart once the turn ends) or
/// why it can't be picked; a restart that waits is said at the bottom. A pick closes the sheet and goes through the page
/// (`pick`, which asks first where `SessionModes.confirmation` says so). Nothing is sent to the agent.
struct PermissionModeSheet: View {
    /// The session is read from the model each time, never kept: a mode change or a queued restart that arrives while the
    /// sheet is open redraws its check, rows, note and footer, and a pick goes by the mode it runs in then (#7996 review).
    var key: SessionKey
    var pick: (String) -> Void
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        if let session = model.session(key) { sheet(session) } else { Color.clear.onAppear { dismiss() } }
    }

    private func sheet(_ session: Session) -> some View {
        let mode = SessionModes.current(session)
        let rows = SessionModes.commandRows(session)
        return NavigationStack {
            Form {
                Section {
                    ForEach(rows) { row in
                        Button {
                            if SessionModes.appliesPick(model.session(key), to: row.option.id) { pick(row.option.id) }
                            dismiss()
                        } label: {
                            PermissionModeRow(option: row.option, checked: row.option.id == mode)
                        }
                        // the row draws its own inks (a form tints a default button's label with the accent)
                        .buttonStyle(.plain)
                        .disabled(!row.enabled)
                        .accessibilityIdentifier("permissions.\(row.option.id)")
                        .accessibilityAddTraits(row.option.id == mode ? .isSelected : [])
                    }
                } footer: {
                    // Only when Allow Everything isn't already carrying the same pending sentence (#7996 review).
                    if let pending = SessionModes.pending(session), !rows.contains(where: { $0.option.detail == pending }) {
                        Text(pending)
                    }
                }
            }
            .navigationTitle(SessionModes.modeHeading(session: session))
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                SheetClose(id: "permissions.close") { dismiss() }
            }
        }
        // Tall enough for Allow Everything and its note at every language's line breaks (medium cut them off in English).
        .presentationDetents([.large])
        .accessibilityIdentifier("permissions.sheet")
    }
}

/// One mode in `PermissionModeSheet`: its symbol, name and what it does, a check on the current one; Allow Everything in
/// the warning red.
private struct PermissionModeRow: View {
    var option: SessionModes.Option
    var checked: Bool
    @Environment(\.isEnabled) private var enabled

    var body: some View {
        let danger = option.id == SessionModes.ModeID.bypass
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(option.symbol).font(Theme.Font.bodyStrong)
                .foregroundStyle(danger && enabled ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.secondary))
                .scaledFrame(width: Theme.Size.icon)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(option.title).font(Theme.Font.body)
                    .foregroundStyle(!enabled ? AnyShapeStyle(.tertiary) : danger ? AnyShapeStyle(Theme.danger) : AnyShapeStyle(.primary))
                    .lineLimit(1)
                if !option.detail.isEmpty {
                    Text(option.detail).font(Theme.Font.secondary)
                        .foregroundStyle(enabled ? AnyShapeStyle(.secondary) : AnyShapeStyle(.tertiary))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer(minLength: Theme.Space.s)
            if checked {
                ThemedIcon(Theme.Icon.done).font(Theme.Font.bodyStrong).foregroundStyle(.tint)
            }
        }
        .contentShape(.rect)
    }
}
