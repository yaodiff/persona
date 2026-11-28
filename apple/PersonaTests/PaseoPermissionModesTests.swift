import Foundation
import PersonaWire
import Testing
@testable import Persona

/// The permission modes a provider lists (#8261): Paseo's Grok, OpenCode or Codex say which modes a start and a running
/// chat take (`permission_modes`), and every client offers the same. New Agent: Use Launcher's (its own approval, sent as
/// no mode) and Allow Everything where listed, only the efforts its model lists, nothing remembered from a Claude Code
/// start. The chat: those two rows alone, its own approval never drawn as Ask First, a switch both ways that asks only
/// going into Allow Everything. Session Info: the mode in the same words, and no counts nobody gives.
@MainActor struct PaseoPermissionModesTests {
    private static func session(_ fields: String, status: String = "idle") throws -> Session {
        let json = #"{"machine_id":"m","session_id":"paseo-1","title":"t","cwd":"/w","status":"\#(status)","state":"\#(status)"\#(fields)}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private static let grok = Launcher(name: "grok", displayName: "Grok", model: "grok-4", agent: "paseo", source: "paseo",
                                       permissionModes: ["default", "bypassPermissions"])
    /// A provider with neither Auto Accept nor full access: its own approval is all it lists.
    private static let ownOnly = Launcher(name: "cursor", displayName: "Cursor", agent: "paseo", source: "paseo",
                                          permissionModes: ["default"])
    private static let paseoCodex = Launcher(name: "my-codex", agent: "codex", source: "paseo",
                                             permissionModes: ["default", "bypassPermissions"])
    private static let claude = Launcher(name: "my-claude", source: "paseo")
    private static let terminalTraex = Launcher(name: "traex", model: "默认", agent: "traex", source: "zshrc")
    private static let unknown = Launcher(name: "future-agent", agent: "future-agent", source: "zshrc")
    private static let terminalCodex = Launcher(name: "codex", agent: "codex", source: "zshrc")
    /// Grok from a computer older than the list: told by its agent instead (#8144).
    private static let olderGrok = Launcher(name: "grok", agent: "paseo", source: "paseo")

    /// A running Grok on its own approval, whose node lists both modes and keeps Claude Code's chips off.
    private static let grokRow = #","agent":"paseo","agent_label":"Grok","launcher":"grok","capabilities":{"read":true,"send":true,"modes":false,"permission_modes":["default","bypassPermissions"]}"#

    // MARK: Launchers

    @Test func aLauncherSaysItsNameAndTheModesItTakes() throws {
        let json = #"[{"name":"grok","agent":"paseo","source":"paseo","display_name":"Grok","permission_modes":["default","bypassPermissions"]},{"name":"claude"},{"name":"odd","display_name":7,"permission_modes":"all"}]"#
        let list = try JSON.decoder.decode([Launcher].self, from: Data(json.utf8))
        #expect(list.map(\.title) == ["Grok", "Claude Code", "odd"])
        #expect(list[0].permissionModes == ["default", "bypassPermissions"])
        #expect(list[1].displayName == nil && list[1].permissionModes == nil)
        // A name or a list this app cannot read is left out, never the launcher.
        #expect(list[2].displayName == nil && list[2].permissionModes == nil)
        // A blank name goes by the startup name.
        #expect(Launcher(name: "x", displayName: "  ").title == "x")
    }

    @Test func theSearchFindsALauncherByItsTitle() {
        let fast = Launcher(name: "p_1", displayName: "Grok Fast", agent: "paseo", source: "paseo", permissionModes: ["default"])
        let list = [fast, Self.claude]
        #expect(LauncherChoices.shown(list, query: "fast").map(\.name) == ["p_1"])
        #expect(LauncherChoices.shown(list, query: "p_1").map(\.name) == ["p_1"])
        #expect(LauncherChoices.shown(list, query: "claude").map(\.name) == ["my-claude"])
    }

    // MARK: New Agent

    @Test func aLauncherThatListsItsModesOffersOnlyThose() {
        #expect(StartModes.listed(Self.grok) == ["default", "bypassPermissions"])
        #expect(StartModes.limited(Self.grok) && StartModes.limited(Self.paseoCodex) && StartModes.limited(Self.ownOnly))
        #expect(StartModes.modes(for: Self.grok).map(\.id) == ["bypassPermissions"])
        #expect(StartModes.modes(for: Self.paseoCodex).map(\.id) == ["bypassPermissions"])
        // Its own approval alone: no choice to make, so no field.
        #expect(StartModes.modes(for: Self.ownOnly).isEmpty)
        // Its Allow Everything can be switched back in the chat, unlike Claude Code's start-only one.
        let listedBypass = SessionModes.listedOption("bypassPermissions")
        #expect(StartModes.detail("bypassPermissions", for: Self.grok) == listedBypass.detail)
        #expect(listedBypass.detail != SessionModes.mode("bypassPermissions")?.detail)
        #expect(listedBypass.title == SessionModes.mode("bypassPermissions")?.title)
        #expect(StartModes.detail("", for: Self.grok) == nil)
        // Claude Code's launchers keep their five.
        #expect(StartModes.listed(Self.claude) == nil && !StartModes.limited(Self.claude))
        #expect(StartModes.modes(for: Self.claude).map(\.id) == SessionModes.permissionModes.map(\.id))
        #expect(StartModes.detail("plan", for: Self.claude) == SessionModes.mode("plan")?.detail)
    }

    /// Its own approval goes as no mode, and a mode remembered from a Claude Code start never reaches it.
    @Test func onlyAllowEverythingIsEverSent() {
        for mode in ["", "default", "plan", "acceptEdits", "auto"] {
            #expect(StartModes.sent(mode, for: Self.grok) == nil, "\(mode)")
            #expect(StartModes.sent(mode, for: Self.paseoCodex) == nil, "\(mode)")
            #expect(StartModes.sent(mode, for: Self.ownOnly) == nil, "\(mode)")
        }
        #expect(StartModes.sent("bypassPermissions", for: Self.grok) == "bypassPermissions")
        #expect(StartModes.sent("bypassPermissions", for: Self.paseoCodex) == "bypassPermissions")
        #expect(StartModes.sent("bypassPermissions", for: Self.ownOnly) == nil)
    }

    @Test func itTakesOnlyTheEffortsItsModelLists() {
        #expect(StartModes.efforts(known: nil, for: Self.grok).isEmpty)
        #expect(StartModes.efforts(known: nil, for: Self.paseoCodex).isEmpty)
        #expect(StartModes.efforts(known: ["low", "high"], for: Self.paseoCodex).map(\.id) == ["low", "high"])
        #expect(StartModes.efforts(known: nil, for: Self.claude).map(\.id) == SessionModes.startEffortLevels.map(\.id))
    }

    /// #1842: a discovered terminal Traex takes no Claude Code permission mode, effort or ultracode, even when Claude's
    /// remembered Plan Only and an effort list are present. This is not Paseo's Allow Everything-only rule.
    @Test func discoveredTerminalTraexHasNoClaudeStartControls() {
        let traex = Self.terminalTraex
        #expect(StartModes.listed(traex) == nil)
        #expect(StartModes.modes(for: traex).isEmpty)
        #expect(StartModes.detail("plan", for: traex) == nil)
        #expect(StartModes.efforts(known: nil, for: traex).isEmpty)
        #expect(StartModes.efforts(known: ["low", "high"], for: traex).isEmpty)
        for mode in ["default", "acceptEdits", "plan", "auto", "bypassPermissions"] {
            #expect(StartModes.sent(mode, for: traex) == nil, "\(mode)")
        }
        #expect(!Ultracode.startOffered(features: [Ultracode.feature], agent: "traex"))
    }

    /// The Traex boundary must not become a rule for every non-Claude launcher, nor for a Paseo launcher named Traex.
    @Test func onlyDiscoveredTerminalTraexLosesThoseStartControls() {
        for launcher in [Self.claude, Self.terminalCodex, Self.unknown] {
            #expect(StartModes.modes(for: launcher).map(\.id) == SessionModes.permissionModes.map(\.id), "\(launcher.name)")
            #expect(StartModes.efforts(known: nil, for: launcher).map(\.id) == SessionModes.startEffortLevels.map(\.id), "\(launcher.name)")
            #expect(StartModes.sent("plan", for: launcher) == "plan", "\(launcher.name)")
        }
        // The existing Paseo controls stay provider-led, including a launcher whose startup name happens to be Traex.
        let paseoNamedTraex = Launcher(name: "traex", agent: "paseo", source: "paseo",
                                       permissionModes: ["default", "bypassPermissions"])
        for launcher in [Self.grok, Self.paseoCodex, paseoNamedTraex] {
            #expect(StartModes.modes(for: launcher).map(\.id) == ["bypassPermissions"], "\(launcher.name)")
            #expect(StartModes.sent("plan", for: launcher) == nil, "\(launcher.name)")
            #expect(StartModes.sent("bypassPermissions", for: launcher) == "bypassPermissions", "\(launcher.name)")
            #expect(StartModes.efforts(known: nil, for: launcher).isEmpty, "\(launcher.name)")
            #expect(StartModes.efforts(known: ["low"], for: launcher).map(\.id) == ["low"], "\(launcher.name)")
        }
        #expect(Ultracode.startOffered(features: [Ultracode.feature], agent: "claude"))
        #expect(!Ultracode.startOffered(features: [Ultracode.feature], agent: "codex"))
    }

    /// A computer older than the list says nothing: #8144's rule by the agent still holds there.
    @Test func anOlderComputersProviderKeepsItsRule() {
        #expect(StartModes.listed(Self.olderGrok) == nil)
        #expect(StartModes.limited(Self.olderGrok))
        #expect(StartModes.modes(for: Self.olderGrok).map(\.id) == ["bypassPermissions"])
        // Its chat cannot switch there: Claude Code's words for Allow Everything.
        #expect(StartModes.detail("bypassPermissions", for: Self.olderGrok) == SessionModes.mode("bypassPermissions")?.detail)
        #expect(!StartModes.limited(Launcher(name: "my-codex", agent: "codex", source: "paseo")))
    }

    // MARK: A running chat

    @Test func aRowThatListsTwoModesSwitchesBetweenThem() throws {
        let own = try Self.session(Self.grokRow)
        #expect(own.listedModes == ["default", "bypassPermissions"])
        #expect(own.canSwitchListedModes && !own.canChangeModes && SessionModes.switchable(own))
        #expect(SessionModes.showsInHeader(own))
        #expect(SessionModes.menuOptions(own).map(\.id) == ["default", "bypassPermissions"])
        #expect(SessionModes.bypassUnavailable(own) == nil)
        // No mode reported is its own approval, in its own words, never Ask First.
        #expect(SessionModes.current(own) == "default")
        #expect(SessionModes.currentLabel(own) == String(localized: "Use Launcher's"))
        #expect(SessionModes.currentLabel(own) != SessionModes.mode("default")?.title)
        #expect(SessionModes.modeHeading(session: own) == String(localized: "Permission Mode: \(String(localized: "Use Launcher's"))"))
        // Drawn as a mode not known: the plain shield in no tone, not the raised hand.
        #expect(SessionModes.headerLook(session: own) == .init(symbol: Theme.Icon.permissions, tone: .unknown))
        #expect(SessionModes.chipSymbol(own) == Theme.Icon.permissions)
        // Going into Allow Everything asks first; its own approval is where it is already.
        #expect(SessionModes.confirmation(own, to: "bypassPermissions") == .allowEverything)
        #expect(SessionModes.confirmation(own, to: "default") == nil)

        let all = try Self.session(Self.grokRow + #","permission_mode":"bypassPermissions""#)
        #expect(SessionModes.current(all) == "bypassPermissions")
        #expect(SessionModes.headerLook(session: all) == .init(symbol: Theme.Icon.allowEverything, tone: .allowsAll))
        #expect(SessionModes.modeHeading(session: all) == String(localized: "Permission Mode: \(String(localized: "Allow Everything"))"))
        // Leaving it does not ask: it can be turned on again from here.
        #expect(SessionModes.confirmation(all, to: "default") == nil)
        #expect(SessionModes.confirmation(all, to: "bypassPermissions") == nil)

        // A mode it does not list counts as its own.
        let odd = try Self.session(Self.grokRow + #","permission_mode":"plan""#)
        #expect(SessionModes.current(odd) == "default")
    }

    @Test func onlyARowWithAChoiceListsItsModes() throws {
        // Its own approval alone: nothing to switch, nothing in the header.
        let one = try Self.session(#","agent":"paseo","capabilities":{"read":true,"send":true,"modes":false,"permission_modes":["default"]}"#)
        #expect(one.listedModes == nil && !SessionModes.showsInHeader(one))
        // Where Claude Code's modes stand they win: a Claude Code row from a node that does not say, and a row given them.
        let claudeRow = try Self.session(#","agent":"claude","capabilities":{"read":true,"send":true,"permission_modes":["default","bypassPermissions"]}"#)
        let given = try Self.session(#","agent":"paseo","capabilities":{"read":true,"send":true,"modes":true,"permission_modes":["default","bypassPermissions"]}"#)
        #expect(claudeRow.listedModes == nil && given.listedModes == nil)
        // A Persona conversation never.
        let assistant = try Self.session(#","assistant":true,"capabilities":{"read":true,"send":true,"modes":false,"permission_modes":["default","bypassPermissions"]}"#)
        #expect(assistant.listedModes == nil)
        // A list this app cannot read is no list.
        let unread = try Self.session(#","agent":"paseo","capabilities":{"read":true,"send":true,"modes":false,"permission_modes":"all"}"#)
        #expect(unread.listedModes == nil && unread.capabilities?.send == true)
        // Stopped: its mode is shown, without a switch.
        let stopped = try Self.session(Self.grokRow, status: "offline")
        #expect(stopped.listedModes != nil && !stopped.canSwitchListedModes && !SessionModes.switchable(stopped))
        #expect(SessionModes.showsInHeader(stopped))
    }

    /// Claude Code's rows are as they were.
    @Test func claudeCodesRowsAreUnchanged() throws {
        let claude = try Self.session(#","agent":"claude","permission_mode":"default""#)
        #expect(claude.listedModes == nil)
        #expect(SessionModes.current(claude) == "default")
        #expect(SessionModes.menuOptions(claude).map(\.id) == ["default", "acceptEdits", "plan", "auto"])
        #expect(SessionModes.headerLook(session: claude) == SessionModes.headerLook("default"))
        #expect(SessionModes.chipSymbol(claude) == Theme.Icon.askFirst)
        #expect(SessionModes.modeHeading(session: claude) == SessionModes.modeHeading("default"))
        #expect(SessionModes.confirmation(claude, to: "bypassPermissions") == .allowEverything)
        let oneWay = try Self.session(#","agent":"claude","permission_mode":"bypassPermissions""#)
        #expect(SessionModes.confirmation(oneWay, to: "plan") == .leaveAllowEverything)
    }

    // MARK: Session Info

    @Test func sessionInfoNamesTheModeInTheSameWords() throws {
        let row = try Self.session(Self.grokRow)
        #expect(SessionModes.infoLabel(nil, session: row) == String(localized: "Use Launcher's"))
        #expect(SessionModes.infoLabel("", session: row) == String(localized: "Use Launcher's"))
        #expect(SessionModes.infoLabel("bypassPermissions", session: row) == String(localized: "Allow Everything"))
        // Claude Code's as before.
        let claude = try Self.session(#","agent":"claude""#)
        #expect(SessionModes.infoLabel("default", session: claude) == SessionModes.modeLabel("default"))
        #expect(SessionModes.infoLabel(nil, session: claude) == nil)
        #expect(SessionModes.infoLabel("default", session: nil) == SessionModes.modeLabel("default"))
    }

    @Test func sessionInfoSaysWhenNobodyGivesItsCounts() throws {
        let unknown = #"{"session_id":"paseo-1","context_tokens":0,"output_tokens":0,"compactions":0,"turns":0,"usage_unknown":true}"#
        #expect(try JSON.decoder.decode(APIClient.SessionInfo.self, from: Data(unknown.utf8)).usageUnknown == true)
        // An older node says nothing: the counts are shown as before.
        let older = #"{"session_id":"s1","context_tokens":5,"output_tokens":1,"compactions":0,"turns":2}"#
        #expect(try JSON.decoder.decode(APIClient.SessionInfo.self, from: Data(older.utf8)).usageUnknown == nil)
    }
}
