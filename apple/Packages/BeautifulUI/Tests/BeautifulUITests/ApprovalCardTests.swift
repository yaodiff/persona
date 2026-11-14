import SwiftUI
import Testing
@testable import BeautifulUI

@Suite struct ApprovalAnswersTests {
    @Test func singleChoiceReplacesPick() {
        var a = ApprovalAnswers(custom: [0: "maybe"])
        a.toggle(1, in: 0, kind: .single)
        a.toggle(2, in: 0, kind: .single)
        #expect(a.picks[0] == [2])
        #expect(a.custom[0] == "")
    }

    @Test func multipleChoiceToggles() {
        var a = ApprovalAnswers()
        a.toggle(0, in: 1, kind: .multiple)
        a.toggle(2, in: 1, kind: .multiple)
        a.toggle(0, in: 1, kind: .multiple)
        #expect(a.picks[1] == [2])
    }

    @Test func freeTextCountsAsAnAnswerAndDropsASinglePick() {
        var a = ApprovalAnswers(picks: [0: [1]])
        #expect(a.hasAnswer(0))
        a.setCustom("   ", in: 0, kind: .single)
        #expect(!a.hasAnswer(0))
        a.setCustom("Two", in: 0, kind: .single)
        #expect(a.hasAnswer(0))
        #expect(a.picks[0] == [])
    }
}

@MainActor @Suite struct ApprovalCardTests {
    @Test func legacyTrailingClosuresStayOnSubmit() {
        let plain = ApprovalCard(questions: [ApprovalQuestion("Continue?", options: ["Yes"])]) { (_: ApprovalAnswers) in }
        #expect(plain.onSubmit != nil)
        #expect(plain.onSkip == nil)

        let contextual = ApprovalCard(questions: [ApprovalQuestion("Continue?", options: ["Yes"])]) { (_: ApprovalAnswers) in }
            context: { _ in EmptyView() }
        #expect(contextual.onSubmit != nil)
        #expect(contextual.onSkip == nil)

        let skipping = ApprovalCard(questions: [ApprovalQuestion("Continue?", options: ["Yes"])], onSkip: { _ in })
        #expect(skipping.onSkip != nil)
        #expect(skipping.onSubmit == nil)
    }
}
