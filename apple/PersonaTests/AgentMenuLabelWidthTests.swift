import PersonaWire
import Testing
import UIKit
@testable import Persona

@MainActor struct AgentMenuLabelWidthTests {
    // Budget a compact 240 pt menu with 16 pt margins and 32 pt for the icon and gap.
    private let titleWidth: CGFloat = 240 - 2 * 16 - 32
    private let font = UIFont.preferredFont(forTextStyle: .body,
                                            compatibleWith: UITraitCollection(preferredContentSizeCategory: .large))

    // At the largest accessibility setting a native iPhone action menu wraps “Rename Agent”,
    // while “Auto Rename” still fits. Keep every command within that observed one-line budget.
    private let accessibilityMenuFont = UIFont.systemFont(ofSize: 24)

    private func text(_ resource: LocalizedStringResource, language: String) -> String {
        var localized = resource
        localized.locale = Locale(identifier: language)
        return String(localized: localized)
    }

    private func check(_ resource: LocalizedStringResource, language: String) {
        let title = text(resource, language: language)
        let width = ceil((title as NSString).size(withAttributes: [.font: font]).width)
        #expect(!title.isEmpty && title.rangeOfCharacter(from: .newlines) == nil)
        #expect(!title.contains("…") && !title.contains("..."), "\(language): \(title)")
        let attributes: [NSAttributedString.Key: Any] = [.font: accessibilityMenuFont]
        let largeWidth = ceil((title as NSString).size(withAttributes: attributes).width)
        let largeBudget = ceil(("Auto Rename" as NSString).size(withAttributes: attributes).width) + 3
        #expect(largeWidth <= largeBudget, "\(language): ‘\(title)’ needs \(largeWidth) pt of \(largeBudget) pt at large text")
        #expect(width <= titleWidth, "\(language): ‘\(title)’ needs \(width) pt of \(titleWidth) pt")
    }

    @Test(arguments: [99, 100])
    func countedCommandsKeepExactCountsForVoiceOver(_ count: Int) throws {
        let json = #"{"machine_id":"m","session_id":"s","title":"Test","cwd":"/work","status":"idle","running_subagents":\#(count)}"#
        let session = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        let facts = AgentActionFacts(session: session, sideCount: count)
        #expect(facts.subagentCount == count)
        #expect(facts.sideCount == count)
        let badge = count == 99 ? "99" : "99+"
        for id in [AgentActionItem.ID.subagents, .sideSessions] {
            #expect(text(id.menuTitle(in: facts), language: "en").contains(badge))
        }
    }

    @Test(arguments: ["en", "zh-Hans", "ja"])
    func commandTitlesFitOnOneLine(_ language: String) throws {
        let json = #"{"machine_id":"m","session_id":"s","title":"Example","cwd":"/work","status":"idle","state":"idle","running_subagents":999}"#
        let session = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        // Prove the requested catalog language is actually being measured in this test process.
        #expect(text("Auto Rename", language: language) == ["en": "Auto Rename", "zh-Hans": "自动命名", "ja": "自動命名"][language])
        for selected in [false, true] {
            let facts = AgentActionFacts(session: session, pinned: selected, muted: selected,
                                        paneBadges: selected ? [.git: "99+"] : [:], sideCount: 999,
                                        terminalResume: selected ? .available : .noEngine)
            for id in AgentActionItem.ID.allCases {
                check(id.menuTitle(in: facts), language: language)
            }
        }
        check(AgentActionMenu.moreTitle, language: language)
        check(MoveToChoices.onItsOwn, language: language)
        for command in WorktreeSidebarMenu.Command.allCases {
            check(command.title, language: language)
        }
    }
}
