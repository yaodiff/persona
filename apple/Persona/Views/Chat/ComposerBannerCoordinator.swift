import Foundation

/// Secondary advisory banners that may sit above the Assistant composer.
/// Critical prompts (stopped agent, read-only/offline state, pending confirmations or questions, turn/start failures,
/// live retries, and blocking readiness cards) are intentionally excluded and are never suppressed by this enum.
nonisolated enum ComposerAdvisoryBanner: Equatable, Sendable {
    case modelFallback
    case ultracodeLost
    case compaction
    case pushHealth
    case voiceEnded
    case suggestion
}

/// Arbitrates secondary advisory banners above the Assistant composer to at most one slot at a time, while preserving
/// every critical prompt unconditionally.
nonisolated enum ComposerBannerCoordinator {
    /// Protected prompts that must remain visible and can never be crowded out by single-slot arbitration.
    struct CriticalPrompts: Equatable, Sendable {
        var stoppedAgent = false
        var terminalOnly = false
        var readOnlySource = false
        var missingSession = false
        var liveRetry = false
        var offlineAgent = false
        var managerOffline = false
        var startingOrError = false
        var attention = false
        var failure = false
        var readinessOrFailure = false
        var pendingConfirmation = false

        var any: Bool {
            stoppedAgent || terminalOnly || readOnlySource || missingSession || liveRetry || offlineAgent
                || managerOffline || startingOrError || attention || failure || readinessOrFailure
                || pendingConfirmation
        }
    }

    /// Candidate secondary advisories that share a single slot above the Assistant composer.
    struct Advisories: Equatable, Sendable {
        var modelFallback = false
        var ultracodeLost = false
        var compaction = false
        var pushHealth = false
        var voiceEnded = false
        var suggestion = false
    }

    struct Inputs: Equatable, Sendable {
        var critical = CriticalPrompts()
        var advisories = Advisories()
    }

    /// Returns the single secondary advisory banner to show, or `nil` when none should show.
    /// Critical prompts themselves are never gated or hidden by this coordinator; when any critical prompt is active,
    /// lower-priority advisories (push health, ended voice call, reply suggestion) yield so critical prompts stay clear.
    static func advisory(for inputs: Inputs) -> ComposerAdvisoryBanner? {
        if inputs.advisories.modelFallback { return .modelFallback }
        if inputs.advisories.ultracodeLost { return .ultracodeLost }
        if inputs.advisories.compaction { return .compaction }
        guard !inputs.critical.any else { return nil }
        if inputs.advisories.pushHealth { return .pushHealth }
        if inputs.advisories.voiceEnded { return .voiceEnded }
        if inputs.advisories.suggestion { return .suggestion }
        return nil
    }
}
