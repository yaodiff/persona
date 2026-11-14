import Foundation
import PersonaWire

/// What the assistant's iphone_do does, in words (docs/extensions/iphone.md): "Tap item 3 → Type “hello” → Press Home".
/// Steps as the node core reads them: a target is `{"n"}`, `{"label", "role"?}` or a point `{"x", "y"}`.
nonisolated enum IPhoneSteps {
    static func describe(_ input: JSONValue) -> String? {
        guard let steps = input["steps"]?.array, !steps.isEmpty else { return nil }
        return steps.compactMap(step).joined(separator: " → ")
    }

    /// One step: {"action": "tap", "target": {"n": 3}}, {"action": "type", "text": "…"}…; an action this app does not
    /// know reads as its name.
    static func step(_ step: JSONValue) -> String? {
        guard let action = step["action"]?.string, !action.isEmpty else { return nil }
        let target = targetName(step["target"])
        switch action {
        case "tap":
            if step["count"]?.int == 2 { return String(localized: "Double-tap \(target)") }
            return String(localized: "Tap \(target)", comment: "An iPhone step: tap an element or a spot")
        case "long_press": return String(localized: "Long-press \(target)")
        case "drag": return String(localized: "Drag \(target)")
        case "swipe":
            switch step["direction"]?.string {
            case "up": return String(localized: "Swipe up")
            case "down": return String(localized: "Swipe down")
            case "left": return String(localized: "Swipe left")
            case "right": return String(localized: "Swipe right")
            default: return String(localized: "Swipe")
            }
        case "type":
            // Only its length, as on the approval card: the text may be a password.
            let count = (step["text"]?.string ?? "").count
            return String(localized: "Type \(count) characters", comment: "An iPhone step: type text; only its length is shown")
        case "key":
            let k = ComputerSteps.key(step["key"]?.string ?? "")
            return String(localized: "Press \(k)", comment: "A computer tool step: press a key or shortcut")
        case "button":
            switch step["button"]?.string {
            case "home": return String(localized: "Press Home")
            case "lock": return String(localized: "Press the side button")
            case "volume_up": return String(localized: "Turn the volume up")
            case "volume_down": return String(localized: "Turn the volume down")
            default: return String(localized: "Press a button")
            }
        case "launch":
            let app = step["app"]?.string ?? "App"
            return String(localized: "Open \(app) [Git Hub Thread View eb8679]", comment: "A computer tool step: launch an app")
        case "terminate":
            let app = step["app"]?.string ?? "App"
            return String(localized: "Close \(app)")
        case "wait": return String(localized: "Wait")
        default: return action
        }
    }

    /// What a step acts on: an element by its label, else its number; a point; else a spot on the screen.
    static func targetName(_ target: JSONValue?) -> String {
        if let label = target?["label"]?.string, !label.isEmpty { return "“\(ComputerSteps.short(label))”" }
        if let n = target?["n"]?.int, n > 0 { return String(localized: "item \(n)", comment: "An iPhone step's target: the numbered element of the last look") }
        if let x = target?["x"]?.double, let y = target?["y"]?.double { return "(\(Int(x.rounded())), \(Int(y.rounded())))" }
        return String(localized: "a spot", comment: "An unnamed spot on screen that a click acts on")
    }
}
