import SwiftUI

/// Which agent runs in a session, as its mark draws it (#6390, the web's `AgentMark`): Claude's, Codex's (OpenAI's),
/// TRAE's own mark, else the first letter of the agent's name on a tile; and the client it runs in, from the node's
/// `agent_surface`. An agent a runtime runs is known by its name too: a Paseo provider labelled TRAE draws TRAE's mark
/// (#8144). Pure data, so lists and tests read it without drawing.
nonisolated struct AgentKind: Equatable, Sendable {
    /// The mark's image in the asset catalog (`Theme.Brand`), nil for an agent with no mark of its own.
    var brand: String?
    /// The agent's name: the mark's accessible name and the source of the letter tile.
    var name: String
    var surface: Surface?

    enum Surface: String, Sendable {
        /// Claude's or Codex's desktop app.
        case desktop
        /// Its command line, in a terminal: the mark carries </>.
        case cli
    }

    init(brand: String?, name: String, surface: Surface?) {
        self.brand = brand
        self.name = name
        self.surface = surface
    }

    init(_ s: Session) {
        surface = s.agentSurface.flatMap(Surface.init(rawValue:))
        switch (s.agent ?? "").lowercased() {
        case _ where s.isCodex: brand = Theme.Brand.openAI; name = "Codex"
        case "trae": brand = Theme.Brand.trae; name = "TRAE"
        case "trae-cli", "traecli", "traex": brand = Theme.Brand.trae; name = Session.traeCLIName
        case _ where s.isClaude: brand = Theme.Brand.claude; name = "Claude Code"
        default:
            // A runtime's agent by the name the row gives it (Paseo's provider label, else its launcher, its provider's
            // id): one the app has a mark for draws it, any other its letter. Never the engine's own name.
            name = s.agentLabel
            brand = Self.brand(named: s.agent) ?? Self.brand(named: s.agentLabelHint)
                ?? (Session.providerEngines.contains((s.agent ?? "").lowercased()) ? Self.brand(named: s.launcher) : nil)
        }
    }

    init(_ harness: SubagentOverview.HarnessInfo) {
        surface = harness.agentSurface.flatMap(Surface.init(rawValue:))
        let lower = (harness.agent ?? "").lowercased()
        let isCodex = lower == "codex" || harness.sourceKind == "codex_app_server" || harness.sourceKind == "codex_history"
        let isClaude = !isCodex && (lower.isEmpty || lower == "claude")
        switch lower {
        case _ where isCodex: brand = Theme.Brand.openAI; name = "Codex"
        case "trae": brand = Theme.Brand.trae; name = "TRAE"
        case "trae-cli", "traecli", "traex": brand = Theme.Brand.trae; name = Session.traeCLIName
        case _ where isClaude: brand = Theme.Brand.claude; name = "Claude Code"
        default:
            name = harness.displayName
            brand = Self.brand(named: harness.agent) ?? Self.brand(named: harness.agentLabel)
                ?? Self.brand(named: harness.displayName)
                ?? (Session.providerEngines.contains(lower) ? Self.brand(named: harness.launcher) : nil)
        }
    }

    /// Shares and exports look up the displayed agent mark uniformly (no legacy letter override needed).
    static func prefersLetterMark(for session: Session?) -> Bool {
        false
    }

    /// The agent a token usage share names (`UsageAgentShare.agent`, `Session.agent`'s names, #9984): its mark and its
    /// product's name, the names every client uses (spec/fixtures/agent-names, checked by TokenCalendarTests); nil for
    /// "", the usage the hub stored before it kept agents apart.
    init?(usageAgent agent: String) {
        surface = nil
        switch agent.lowercased() {
        case "": return nil
        case "claude": brand = Theme.Brand.claude; name = "Claude Code"
        case "codex": brand = Theme.Brand.openAI; name = "Codex"
        case "trae": brand = Theme.Brand.trae; name = "TRAE"
        case "trae-cli", "traecli", "traex": brand = Theme.Brand.trae; name = Session.traeCLIName
        case "omp": brand = nil; name = "Oh My Pi"
        case "gemini": brand = Theme.Brand.gemini; name = "Gemini CLI"
        case "cursor": brand = Theme.Brand.cursor; name = "Cursor"
        default: brand = Self.brand(named: agent); name = agent // pi, opencode (their own lower case), a runtime's own
        }
    }

    /// The mark of an agent known by `name`, a provider's label or id ("TRAE", "TRAE CLI", "trae-cli"), with case, spaces,
    /// hyphens and underscores ignored; nil for one the app has no mark for.
    static func brand(named name: String?) -> String? {
        guard let name else { return nil }
        let product = name.split(separator: "·", maxSplits: 1).first ?? ""
        let key = product.lowercased().filter { !$0.isWhitespace && !".-_".contains($0) }
        switch key {
        case "claude", "claudecode": return Theme.Brand.claude
        case "codex", "codexcli": return Theme.Brand.openAI
        case "trae", "traecli", "traeagent", "traex": return Theme.Brand.trae
        case "opencode": return Theme.Brand.opencode
        case "pi", "piweb": return Theme.Brand.pi
        case "gemini", "geminicli": return Theme.Brand.gemini
        case "cursor", "cursoragent": return Theme.Brand.cursor
        case "grok", "grokbuild", "grokchat", "grokcli": return Theme.Brand.grok
        case "agy", "antigravity", "antigravitycli", "googleantigravity": return Theme.Brand.agy
        case "dsh", "deepseekharness": return Theme.Brand.dsh
        case "kimi", "kimicli": return Theme.Brand.kimi
        case "copilot", "copilotcli", "githubcopilot", "githubcopilotcli": return Theme.Brand.copilot
        default: return nil
        }
    }

    /// The letter on the tile of an agent with no mark: the name's first letter or digit, upper case.
    var letter: String {
        name.first(where: { $0.isLetter || $0.isNumber }).map { String($0).uppercased() } ?? "?"
    }

    /// "Claude Code, command line": the mark's accessible name.
    var spoken: String {
        switch surface {
        case .cli: String(localized: "\(name), command line", comment: "An agent's mark: the agent, run from its command line")
        case .desktop: String(localized: "\(name), desktop app", comment: "An agent's mark: the agent, run in its desktop app")
        case nil: name
        }
    }
}

/// The mark of the agent in a session, in a square box of `side` points (scaled with the text size) that lines a
/// column of rows up; drawn before the state dot, never in its place. Claude's mark keeps its coral, the one-color marks
/// take the text color (light and dark, a selected row); an agent with no mark gets its letter on a tile of the same
/// box. One run from its command line carries a </> badge on the lower right, ringed in the page's color so it reads
/// on any row.
struct AgentMark: View {
    var kind: AgentKind
    var side: CGFloat = Theme.Size.markS

    init(_ session: Session, side: CGFloat = Theme.Size.markS) {
        kind = AgentKind(session)
        self.side = side
    }

    init(kind: AgentKind, side: CGFloat = Theme.Size.markS) {
        self.kind = kind
        self.side = side
    }

    var body: some View {
        ZStack {
            if let brand = kind.brand {
                BrandMark(brand, side: side).foregroundStyle(.secondary)
            } else {
                Text(kind.letter)
                    .font(Theme.Font.labelHeavy)
                    .minimumScaleFactor(0.5)
                    .lineLimit(1)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: side * 0.92, height: side * 0.92)
                    .background(Theme.Fill.chip, in: RoundedRectangle(cornerRadius: Theme.Radius.thumbnail, style: .continuous))
            }
        }
        .scaledFrame(width: side, height: side)
        .overlay(alignment: .bottomTrailing) {
            if kind.surface == .cli { CommandLineBadge(side: side * 0.62).offset(x: side * 0.22, y: side * 0.18) }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(kind.spoken)
        .accessibilityIdentifier("agent.mark")
    }
}

/// The command line's badge (#6390): </> on a small dark tile, ringed in the page's color.
private struct CommandLineBadge: View {
    var side: CGFloat

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.thumbnail, style: .continuous)
        Image(systemName: Theme.Icon.commandLine)
            .resizable()
            .scaledToFit()
            .fontWeight(.bold)
            .foregroundStyle(Theme.pageBackground)
            .padding(side * 0.18)
            .scaledFrame(width: side, height: side)
            .background(Color.primary, in: shape)
            .overlay(shape.strokeBorder(Theme.pageBackground, lineWidth: Theme.Size.line))
            .accessibilityHidden(true)
    }
}
