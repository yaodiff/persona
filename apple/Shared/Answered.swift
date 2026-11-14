import Foundation

// Shared answered interaction formatting and status handling (docs/protocol/protocol.md, #1688).
// Aligns with web/src/lib/answered.ts and Android HubInteractionCenter.

nonisolated struct ResolveResult: Codable, Sendable {
    var ok: Bool?
    var already: Bool?
    var status: String?
    var resolution: Resolution?
}

nonisolated struct AnswerLine: Hashable, Sendable {
    var label: String
    var answer: String
}

nonisolated enum Answered {
    static var modeLabels: [String: String] {
        [
            "acceptEdits": String(localized: "Auto-Accept Edits"),
            "bypassPermissions": String(localized: "Allow Everything"),
            "default": String(localized: "Confirm each step")
        ]
    }

    static func answerLines(_ ix: Interaction, _ r: Resolution) -> [AnswerLine] {
        if r.cancel == true { return [AnswerLine(label: "", answer: String(localized: "Question closed without an answer"))] }
        if r.dismiss == true { return [AnswerLine(label: "", answer: String(localized: "Handed back to the computer"))] }
        switch ix.kind {
        case .question:
            let answers = r.answers ?? [:]
            return (ix.questions ?? []).map { q in
                AnswerLine(label: q.question, answer: answers[q.question] ?? String(localized: "(No answer)"))
            }
        case .plan:
            if r.approve == true {
                let mode = r.mode ?? ""
                let text = modeLabels[mode].map { String(localized: "Approve (\($0))", comment: "Plan approved, with the permission mode") } ?? String(localized: "Approve [Notification Actions 8cbe69]")
                return [AnswerLine(label: "", answer: text)]
            }
            if let feedback = r.feedback, !feedback.isEmpty {
                return [AnswerLine(label: "", answer: String(localized: "Send back: \(feedback)"))]
            }
            return [AnswerLine(label: "", answer: String(localized: "Send Back [Notification Actions a20f13]"))]
        case .permission:
            if r.allow == true {
                return [AnswerLine(label: "", answer: r.always == true ? String(localized: "Always Allow") : String(localized: "Allow [Notification Actions ce7ef2]"))]
            }
            if r.allow == false {
                return [AnswerLine(label: "", answer: String(localized: "Deny"))]
            }
            return [AnswerLine(label: "", answer: String(localized: "Done [Inbox View 59f6c8]"))]
        case .elicitation:
            return elicitationLines(ix, r)
        default:
            let options = ix.dialog?.options ?? []
            func labelOf(_ key: String) -> String {
                options.first(where: { $0.key == key })?.label ?? key
            }
            let isSecret = ix.secret == true || r.text == "<redacted>"
            let typed = (r.text?.isEmpty == false) ? (isSecret ? String(localized: "(Entered)") : r.text!) : nil
            var picked: [String] = []
            if let opts = r.options {
                picked = opts.map(labelOf)
            } else if let opt = r.option {
                picked = [labelOf(opt)]
            }
            let all = (picked + (typed.map { [$0] } ?? [])).filter { !$0.isEmpty }
            let combined = all.joined(separator: String(localized: ", ", comment: "Separator between items in a list"))
            return [AnswerLine(label: "", answer: combined.isEmpty ? String(localized: "Done [Inbox View 59f6c8]") : combined)]
        }
    }

    /// An MCP form or link: how it was answered, and for a form what was filled in (a secret only says it was), in the
    /// form's order.
    private static func elicitationLines(_ ix: Interaction, _ r: Resolution) -> [AnswerLine] {
        switch r.action {
        case Elicitation.Action.decline.rawValue: return [AnswerLine(label: "", answer: String(localized: "Declined"))]
        case Elicitation.Action.cancel.rawValue: return [AnswerLine(label: "", answer: String(localized: "Canceled"))]
        case Elicitation.Action.accept.rawValue: break
        default: return [AnswerLine(label: "", answer: String(localized: "Done [Inbox View 59f6c8]"))]
        }
        if ix.elicitation?.isURL == true { return [AnswerLine(label: "", answer: String(localized: "Opened the link, continued"))] }
        let content = r.content ?? [:]
        let lines = Elicitation.fields(ix.elicitation?.requestedSchema, order: ix.elicitation?.fieldOrder).compactMap { f in
            content[f.key].map { AnswerLine(label: f.title, answer: Elicitation.shown(f, $0)) }
        }
        return lines.isEmpty ? [AnswerLine(label: "", answer: String(localized: "Submitted"))] : lines
    }

    static func endedText(_ status: String?) -> String {
        switch status {
        case "expired":
            return String(localized: "The question expired; your answer wasn't delivered")
        case "cancelled":
            return String(localized: "The question was withdrawn on the computer; your answer wasn't delivered")
        case "dismissed":
            return String(localized: "The question was handed back to the computer; your answer wasn't delivered")
        default:
            return String(localized: "The question is closed; your answer wasn't delivered")
        }
    }
}
