import Foundation
import PersonaWire

/// The watch reads live cards as text, retaining their creation positions and only newer view versions.
nonisolated struct WatchAssistantFeed {
    enum Line: Identifiable, Sendable {
        case message(WatchMessage)
        case view(FeedItem)
        var id: String {
            switch self { case .message(let message): "message:" + message.id; case .view(let item): "view:" + item.id }
        }
        var at: Date {
            switch self { case .message(let message): message.ts ?? .distantPast; case .view(let item): item.at }
        }
    }
    private(set) var items: [String: FeedItem] = [:]
    private(set) var views: [String: ExtensionAssistantView] = [:]
    private(set) var cursor: Date?

    mutating func receive(_ response: APIClient.FeedResponse, conversation: String) {
        guard !conversation.isEmpty else { return }
        for (id, view) in response.views ?? [:]
            where !id.isEmpty && id == view.id && view.conversation == conversation && view.version > 0 {
            if (views[id]?.version ?? 0) < view.version { views[id] = view }
        }
        for item in response.items where item.conversation == conversation {
            cursor = max(cursor ?? .distantPast, item.at)
            guard item.kind == .view, !item.id.isEmpty, let id = item.ref, !id.isEmpty, let view = item.assistantView,
                  id == view.id, view.conversation == conversation else { continue }
            if items[item.id] == nil { items[item.id] = item }
            if (views[id]?.version ?? 0) < view.version { views[id] = view }
        }
    }
    func fallback(_ item: FeedItem) -> String {
        guard let id = item.ref, let view = views[id] ?? item.assistantView,
              view.id == id, view.conversation == item.conversation else { return AssistantCardText.mark }
        return WatchText.shown(view.view.fallback ?? AssistantCardText.mark)
    }
    func lines(_ messages: [WatchMessage]) -> [Line] {
        (messages.map(Line.message) + items.values.map(Line.view)).sorted {
            if $0.at != $1.at { return $0.at < $1.at }
            return $0.id < $1.id
        }
    }
}
