import Foundation

/// A local transcript preference, independent of the hub's BotMux session visibility.
public nonisolated enum BotmuxDisplayMode: String, CaseIterable, Sendable {
    case decoded = "decode"
    case original = "all"

    public static let storageKey = "chat.botmuxMode"

    /// Missing and future values keep the useful default.
    public init(storedValue: String?) {
        self = storedValue.flatMap(Self.init(rawValue:)) ?? .decoded
    }

    public var next: Self { self == .decoded ? .original : .decoded }
}

public nonisolated enum BotmuxDisplay {
    /// Only a whole assistant text record is a sentinel. Quoted examples, mixed prose and tool output are content.
    public static func hidesSentinel(_ text: String?, role: String, mode: BotmuxDisplayMode) -> Bool {
        mode == .decoded && role == "assistant"
            && text?.trimmingCharacters(in: .whitespacesAndNewlines) == "BOTMUX_NOTHING_TO_SEND"
    }

    public static func needsReplyPlaceholder(kind: String, text: String, attachments: Int = 0) -> Bool {
        kind == "reply" && attachments == 0 && text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
}
