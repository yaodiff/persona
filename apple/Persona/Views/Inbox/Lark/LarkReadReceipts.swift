import SwiftUI
import PersonaWire

/// The Lark gateway's read receipt of one of my messages: how many in the chat have read it and how many have not
/// (`POST /api/v1/lark/conversations/{id}/read-states`).
nonisolated struct LarkReadState: Codable, Hashable, Sendable {
    var messageID: String
    var readCount: Int
    var unreadCount: Int

    enum CodingKeys: String, CodingKey {
        case messageID = "message_id", readCount = "read_count", unreadCount = "unread_count"
    }

    /// Read by everyone it went to: it will not change, so it is not asked about again.
    var isFinal: Bool { unreadCount == 0 && readCount > 0 }
}

/// What sits beside a message of mine, like Feishu: Read / Unread in a one-to-one chat; in a group a ring that fills as
/// people read it, and a tick once everyone has.
nonisolated enum LarkReadMark: Equatable {
    case read, unread
    case allRead
    /// Some of a group have read it: `read` of `total`.
    case progress(read: Int, total: Int)

    /// Nil without a receipt (not asked yet, the Lark gateway has none, an old hub) or with nobody to read it: nothing is
    /// shown rather than a read state that was never reported.
    init?(state: LarkReadState?, isGroup: Bool) {
        guard let s = state, s.readCount >= 0, s.unreadCount >= 0, s.readCount + s.unreadCount > 0 else { return nil }
        if !isGroup {
            self = s.unreadCount == 0 ? .read : .unread
        } else if s.unreadCount == 0 {
            self = .allRead
        } else {
            self = .progress(read: s.readCount, total: s.readCount + s.unreadCount)
        }
    }

    /// The share of the ring that is filled.
    var fraction: Double {
        switch self {
        case .read, .allRead: 1
        case .unread: 0
        case .progress(let read, let total): total > 0 ? Double(read) / Double(total) : 0
        }
    }

    /// What VoiceOver reads.
    var label: String {
        switch self {
        case .read: String(localized: "Read [Lark Read Receipts efdd1a]")
        case .unread: String(localized: "Unread [Personal Agent Intents 4d0680]")
        case .allRead: String(localized: "All read")
        case .progress(let read, let total): String(localized: "\(total - read) unread [Lark Read Receipts 3df1e3]")
        }
    }
}

/// Which receipts of my messages to ask the hub for, and when. Only mine that are on screen; up to `batchLimit` ids in
/// one request, at most one request per chat every `minGap`; a message everyone has read is not asked about again.
/// A change in the chat asks again about mine on screen. An old hub without the route (404) is not asked again during
/// the visit.
nonisolated struct LarkReadReceipts {
    /// The Lark gateway's own cap per batch (the hub answers 400 above it).
    static let batchLimit = 100
    /// Opening a chat, scrolling and a burst of pushes make one request every few seconds, not one per row.
    static let minGap: TimeInterval = 3

    private(set) var states: [String: LarkReadState] = [:]
    /// Mine on screen now.
    private(set) var onScreen: Set<String> = []
    /// To ask about in the next request.
    private(set) var due: Set<String> = []
    private(set) var lastAsked: Date?
    private(set) var unsupported = false

    /// A message that has a receipt to ask for: mine, in Feishu already, not a system line or recalled.
    static func tracks(_ m: LarkMessage) -> Bool {
        m.isMine && !m.isSystem && !m.recalled && !m.id.isEmpty && !m.id.hasPrefix("outgoing-")
    }

    mutating func appeared(_ m: LarkMessage) {
        guard Self.tracks(m) else { return }
        onScreen.insert(m.id)
        if states[m.id]?.isFinal != true { due.insert(m.id) }
    }

    mutating func disappeared(_ id: String) {
        onScreen.remove(id)
        due.remove(id)
    }

    /// Something changed in the chat: mine on screen that not everyone has read are asked about again.
    mutating func chatChanged() {
        due.formUnion(onScreen.filter { states[$0]?.isFinal != true })
    }

    /// Seconds until the next request may go (0: now); nil when there is nothing to ask (or the hub cannot answer).
    func wait(now: Date) -> TimeInterval? {
        guard !unsupported, !due.isEmpty else { return nil }
        guard let lastAsked else { return 0 }
        return max(0, Self.minGap - now.timeIntervalSince(lastAsked))
    }

    /// The ids of the next request (at most `batchLimit`), taken off the due list; the request counts from `now`.
    mutating func take(now: Date) -> [String] {
        let ids = Array(due.sorted().prefix(Self.batchLimit))
        due.subtract(ids)
        lastAsked = now
        return ids
    }

    mutating func record(_ got: [LarkReadState]) {
        for s in got { states[s.messageID] = s }
    }

    /// The request failed. An old hub (no route) is not asked again; any other failure waits for the next change
    /// in the chat (no retry loop against a hub that keeps failing).
    mutating func failed(status: Int?) {
        if status == 404 || status == 405 {
            unsupported = true
            due.removeAll()
        }
    }
}

extension APIClient {
    /// Read receipts of my messages in a chat (at most `LarkReadReceipts.batchLimit` ids); ones the Lark gateway has no
    /// receipt for are left out.
    func larkReadStates(_ feed: String, ids: [String]) async throws -> [LarkReadState] {
        struct Body: Encodable { var message_ids: [String] }
        struct R: Decodable { var states: [LarkReadState]? }
        if let source = messagingSource {
            struct S: Decodable { var message_id: String; var read: Int; var unread: Int }
            struct M: Decodable { var states: [S] }
            let raw = try await messagingCall {
                try await request("POST", messagingPath(source, "conversations/\(feed)/read-states"), query: messagingQuery(), body: Body(message_ids: ids))
            }
            return try JSON.decoder.decode(M.self, from: raw).states.map { LarkReadState(messageID: $0.message_id, readCount: $0.read, unreadCount: $0.unread) }
        }
        return try await post("/api/v1/lark/conversations/\(feed)/read-states", body: Body(message_ids: ids), as: R.self).states ?? []
    }
}

/// The receipt beside my message (LarkReadMark): a word in a one-to-one chat, a ring or a tick in a group.
struct LarkReadMarkView: View {
    var mark: LarkReadMark

    var body: some View {
        Group {
            switch mark {
            case .read:
                Text("Read [Lark Read Receipts efdd1a]").font(Theme.Font.label).foregroundStyle(.secondary)
            case .unread:
                Text("Unread [Personal Agent Intents 4d0680]").font(Theme.Font.label).foregroundStyle(LarkColor.blue)
            case .allRead:
                Image(systemName: Theme.Icon.success).resizable().scaledToFit().foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.markS, height: Theme.Size.markS)
            case .progress:
                ZStack {
                    Circle().stroke(LarkColor.blue.opacity(0.25), lineWidth: Theme.Size.lineMedium)
                    Circle().trim(from: 0, to: mark.fraction)
                        .stroke(LarkColor.blue, style: StrokeStyle(lineWidth: Theme.Size.lineMedium, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                }
                .scaledFrame(width: Theme.Size.markS, height: Theme.Size.markS)
            }
        }
        .lineLimit(1)
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(mark.label)
        .accessibilityIdentifier("lark.readMark")
    }
}
