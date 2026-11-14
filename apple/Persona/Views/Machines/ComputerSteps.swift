import Foundation
import PersonaWire

/// What a tool that operates a Mac does, in words (#1323, #1343): the assistant's computer_run / computer_state and
/// an agent's own Cua Driver MCP tools (mcp__cua-driver__click…). 「点 AC → 6 → × → 7 → =」 rather than its JSON.
nonisolated enum ComputerSteps {
    /// A tool operates a Mac: the assistant's, or Cua Driver's own MCP.
    static func isComputerTool(_ name: String) -> Bool {
        let base = ToolPresentation.baseName(name)
        return base == "computer_run" || base == "computer_state" || isCuaTool(name)
    }

    static func isCuaTool(_ name: String) -> Bool {
        let lower = name.lowercased()
        return lower.hasPrefix("mcp__cua") || lower.contains("cua-driver") || lower.contains("cua_driver")
    }

    /// The whole call in a line: "Click AC → 6 → × → 7 → =", "Type “hello” → press return", "Look at Calculator", "Run script: …".
    static func describe(_ name: String, input: JSONValue) -> String {
        let base = ToolPresentation.baseName(name)
        switch base {
        case "computer_state":
            if let app = input["app"]?.string, !app.isEmpty { return String(localized: "Look at \(app)", comment: "A computer tool step: look at an app's window") }
            return String(localized: "Look at the whole screen")
        case "computer_run":
            if let steps = input["steps"]?.array, !steps.isEmpty {
                return join(steps.compactMap { step(of: $0, defaultApp: input["app"]?.string) })
            }
            if let script = input["script"]?.string {
                let first = firstLine(script.trimmingCharacters(in: .whitespacesAndNewlines))
                return first.isEmpty ? String(localized: "Run script") : String(localized: "Run script: \(first)")
            }
            return String(localized: "Control Computer")
        default:
            // Cua Driver's own tool: its name is the action, its input the target.
            let tool = base.replacingOccurrences(of: "-", with: "_")
            if tool == "run_steps", let steps = input["steps"]?.array {
                return join(steps.compactMap { step(of: $0, defaultApp: input["app"]?.string) })
            }
            if tool == "run_script" { return String(localized: "Run script") }
            return action(tool, args: input) ?? tool
        }
    }

    /// Steps in a line, clicks after a click joined by → alone (Click AC → 6 → ×).
    static func join(_ words: [String]) -> String {
        var out: [String] = []
        var clicking = false
        for w in words {
            let click = w.hasPrefix(clickVerb)
            out.append(click && clicking ? String(w.dropFirst(clickVerb.count)) : w)
            clicking = click
        }
        return out.joined(separator: " → ")
    }

    /// One run_steps step: {"click": {…}} or {"tool": "click", "args": {…}}; a check alone is 「检查」.
    static func step(of step: JSONValue, defaultApp: String?) -> String? {
        guard case .object(let o) = step else { return nil }
        if let tool = o["tool"]?.string { return action(tool, args: o["args"] ?? .object([:])) }
        for (key, args) in o where !["wait_for", "expect", "args", "tool"].contains(key) {
            return action(key, args: args)
        }
        return o["expect"] != nil || o["wait_for"] != nil ? String(localized: "Check", comment: "A computer tool step that only checks the screen") : nil
    }

    /// A click's verb, kept once for a run of clicks by `join` (Click AC → 6 → ×): the target follows it directly.
    static var clickVerb: String { String(localized: "Click ", comment: "Click verb prefix, followed by a button name; a run of clicks shows it once: 点 AC → 6 → ×") }

    /// One action: its verb and what it acts on.
    static func action(_ tool: String, args: JSONValue) -> String? {
        let target = targetName(args) ?? String(localized: "a spot", comment: "An unnamed spot on screen that a click acts on")
        switch tool {
        case "click": return clickVerb + target
        case "double_click": return String(localized: "Double-click \(target)")
        case "right_click": return String(localized: "Right-click \(target)")
        case "type_text":
            let text = short(args["text"]?.string ?? "")
            return String(localized: "Type “\(text)”")
        case "set_value":
            let value = short(args["value"]?.string ?? "")
            return String(localized: "Fill in “\(value)”")
        case "press_key":
            let k = key(args["key"]?.string ?? "")
            return String(localized: "Press \(k)", comment: "A computer tool step: press a key or shortcut")
        case "hotkey":
            let keys = (args["keys"]?.array?.compactMap(\.string) ?? []).map(key).joined()
            return String(localized: "Press \(keys)", comment: "A computer tool step: press a key or shortcut")
        case "scroll": return String(localized: "Scroll")
        case "drag": return String(localized: "Drag")
        case "launch_app":
            let app = args["name"]?.string ?? args["bundle_id"]?.string ?? "App"
            return String(localized: "Open \(app) [Git Hub Thread View eb8679]", comment: "A computer tool step: launch an app")
        case "bring_to_front":
            if let app = args["app"]?.string ?? args["name"]?.string { return String(localized: "Switch to \(app)") }
            return String(localized: "Bring to front")
        case "get_window_state": return String(localized: "Look at the window")
        case "get_desktop_state", "screenshot": return String(localized: "Take screenshot")
        case "get_accessibility_tree", "list_windows", "list_apps": return String(localized: "See what's on screen")
        case "zoom": return String(localized: "Zoom in")
        default: return nil
        }
    }

    /// What a step's target is called: a button's name (Calculator's are words: Multiply → ×), else none.
    static func targetName(_ args: JSONValue) -> String? {
        guard let name = args["name"]?.string ?? args["label"]?.string ?? args["text"]?.string, !name.isEmpty else { return nil }
        return symbols[name] ?? short(name)
    }

    /// Calculator's buttons read as on its keys.
    static let symbols: [String: String] = [
        "All Clear": "AC", "Clear": "C", "Multiply": "×", "Divide": "÷", "Add": "+", "Plus": "+", "Subtract": "−",
        "Minus": "−", "Equals": "=", "Percent": "%", "Decimal": ".", "Change Sign": "±", "Negate": "±",
    ]

    static func key(_ k: String) -> String {
        switch k.lowercased() {
        case "cmd", "command": "⌘"
        case "shift": "⇧"
        case "option", "alt": "⌥"
        case "ctrl", "control": "⌃"
        case "return", "enter": "⏎"
        case "escape", "esc": "esc"
        case "tab": "⇥"
        case "space": String(localized: "Space [Computer Steps bfa4a6]", comment: "The space bar key")
        case "delete", "backspace": "⌫"
        default: k.count == 1 ? k.uppercased() : k
        }
    }

    static func firstLine(_ s: String) -> String {
        s.split(separator: "\n", maxSplits: 1, omittingEmptySubsequences: true).first.map(String.init) ?? s
    }

    static func short(_ s: String) -> String {
        let one = firstLine(s)
        return one.count > 24 ? String(one.prefix(23)) + "…" : one
    }

    /// The Mac a tool's `machine` names, as the user knows it (MacBook, not macbook): matched by id or name as the hub
    /// does, in `group` first; what the tool said when no machine goes by it.
    static func machineName(_ named: String, in machines: [Machine], group: String = "") -> String {
        let match = { (m: Machine) in m.id == named || m.name.caseInsensitiveCompare(named) == .orderedSame }
        return (machines.first { $0.group == group && match($0) } ?? machines.first(where: match))?.name ?? named
    }
}

/// The latest time a chat's model operated a Mac (#1343): which Mac, the step in words, still running or when it
/// ended. The chat page shows it with the running work and opens that Mac's live screen from it.
nonisolated struct ComputerActivity: Hashable, Sendable {
    var machine: MachineRef
    var machineName: String
    var step: String
    var running: Bool
    /// When the call started (its tool use) and, done, its result.
    var at: Date
    var toolUseID: String

    /// How long a finished one stays up before it fades.
    static let lingers: TimeInterval = 6

    /// Showing at now: running, or ended a moment ago.
    func shown(at now: Date) -> Bool { running || now.timeIntervalSince(at) < Self.lingers }

    /// The chat's latest computer tool call in items, looked for among the newest `window` items; nil when there is
    /// none or its Mac is not known.
    static func latest(in items: [Item], chat: MachineRef, machines: [Machine], window: Int = 300) -> ComputerActivity? {
        var resultAt: [String: Date] = [:]
        for item in items.suffix(window).reversed() {
            if item.kind == .toolResult, let id = item.toolUseID { resultAt[id] = item.ts ?? .distantPast }
            guard item.kind == .toolUse, let name = item.toolName, ComputerSteps.isComputerTool(name), let id = item.toolUseID else { continue }
            let input = item.toolInput ?? .object([:])
            guard let ref = ScreenEntry.machine(name, input: input, chat: chat, machines: machines) else { return nil }
            let done = resultAt[id]
            return ComputerActivity(machine: ref, machineName: machines.first { $0.ref == ref }?.name ?? ref.id,
                                    step: ComputerSteps.describe(name, input: input), running: done == nil,
                                    at: done ?? item.ts ?? .now, toolUseID: id)
        }
        return nil
    }
}
