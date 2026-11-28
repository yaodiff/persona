import Testing
@testable import Persona

/// Single-slot advisory arbitration above the Assistant composer (#16165): critical prompts ("Agent stopped",
/// confirmations, errors, offline/readiness states) are never gated or suppressed, while secondary advisories share
/// one slot in strict priority order and low-priority advisories yield whenever a critical prompt is active.
struct ComposerBannerCoordinatorTests {
    @Test func emptyInputsShowNoAdvisory() {
        #expect(ComposerBannerCoordinator.advisory(for: .init()) == nil)
    }

    @Test func secondaryAdvisoriesFollowStrictSingleSlotPriority() {
        var advisories = ComposerBannerCoordinator.Advisories(
            modelFallback: true,
            ultracodeLost: true,
            compaction: true,
            pushHealth: true,
            voiceEnded: true,
            suggestion: true
        )
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .modelFallback)

        advisories.modelFallback = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .ultracodeLost)

        advisories.ultracodeLost = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .compaction)

        advisories.compaction = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .pushHealth)

        advisories.pushHealth = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .voiceEnded)

        advisories.voiceEnded = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == .suggestion)

        advisories.suggestion = false
        #expect(ComposerBannerCoordinator.advisory(for: .init(advisories: advisories)) == nil)
    }

    /// Every critical prompt ("Agent stopped", "needs confirmation", offline, failures, live retries, readiness)
    /// marks `critical.any` and forces background advisories (`pushHealth`, `voiceEnded`, `suggestion`) to yield so
    /// critical prompts can never be crowded out.
    @Test func criticalPromptsNeverYieldAndSuppressLowPriorityAdvisories() {
        let lowPriority = ComposerBannerCoordinator.Advisories(
            pushHealth: true,
            voiceEnded: true,
            suggestion: true
        )
        let criticalCases: [(String, (inout ComposerBannerCoordinator.CriticalPrompts) -> Void)] = [
            ("stoppedAgent", { $0.stoppedAgent = true }),
            ("terminalOnly", { $0.terminalOnly = true }),
            ("readOnlySource", { $0.readOnlySource = true }),
            ("missingSession", { $0.missingSession = true }),
            ("liveRetry", { $0.liveRetry = true }),
            ("offlineAgent", { $0.offlineAgent = true }),
            ("managerOffline", { $0.managerOffline = true }),
            ("startingOrError", { $0.startingOrError = true }),
            ("attention", { $0.attention = true }),
            ("failure", { $0.failure = true }),
            ("readinessOrFailure", { $0.readinessOrFailure = true }),
            ("pendingConfirmation", { $0.pendingConfirmation = true }),
        ]
        for (name, enable) in criticalCases {
            var critical = ComposerBannerCoordinator.CriticalPrompts()
            enable(&critical)
            #expect(critical.any, "\(name) must register as a protected critical prompt")
            #expect(
                ComposerBannerCoordinator.advisory(for: .init(critical: critical, advisories: lowPriority)) == nil,
                "\(name) must suppress low-priority advisories so it is never crowded out"
            )
        }
    }

    @Test func actionableRuntimeAdvisoriesStillArbitrateToOneSlotWithCriticalPrompt() {
        let critical = ComposerBannerCoordinator.CriticalPrompts(pendingConfirmation: true)
        let advisories = ComposerBannerCoordinator.Advisories(
            modelFallback: true,
            ultracodeLost: true,
            compaction: true,
            pushHealth: true
        )
        #expect(ComposerBannerCoordinator.advisory(for: .init(critical: critical, advisories: advisories)) == .modelFallback)
    }
}
