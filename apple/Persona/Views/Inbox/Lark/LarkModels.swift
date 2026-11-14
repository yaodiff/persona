import Foundation

// MARK: - Feishu (Lark gateway) wire types

nonisolated struct LarkConversation: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var name: String
    var avatar: String?
    var isGroup: Bool
    var isTopic: Bool?
    var unread: Int
    var markedUnread: Bool?
    var atMe: Bool
    var atAll: Bool?
    var muted: Bool
    var pinned: Bool?
    var flagged: Bool?
    var done: Bool?
    var tags: [String]?
    var preview: String
    var previewSender: String?
    var isMine: Bool
    var updatedAt: Date?
    var readPosition: String?
    var mentionPosition: String?
    /// Client only: open the chat at this message (a flagged message) instead of the first unread.
    var focus: String?
    /// Client only: the Feishu account the chat is in, sent as `ol_account` (docs/extensions/feishu.md「Multiple accounts」); nil: the hub's
    /// own. Kept in the caches.
    var account: Int?
    /// Client only: whose account it is, shown beside Lark while more than one is signed in.
    var accountName: String?

    enum CodingKeys: String, CodingKey {
        case id, name, avatar, unread, muted, pinned, flagged, done, tags, preview
        case account = "ol_account", accountName = "account_name"
        case isGroup = "is_group", isTopic = "is_topic", markedUnread = "marked_unread", atMe = "at_me", atAll = "at_all"
        case previewSender = "preview_sender", isMine = "is_mine", updatedAt = "updated_at"
        case readPosition = "read_position", mentionPosition = "mention_position"
    }

    init(id: String, name: String, avatar: String? = nil, isGroup: Bool, isTopic: Bool? = nil, unread: Int = 0, atMe: Bool = false,
         muted: Bool = false, preview: String = "", isMine: Bool = false, focus: String? = nil) {
        self.id = id
        self.name = name
        self.avatar = avatar
        self.isGroup = isGroup
        self.isTopic = isTopic
        self.unread = unread
        self.atMe = atMe
        self.muted = muted
        self.preview = preview
        self.isMine = isMine
        self.focus = focus
    }

    /// What the chat's caches and listeners go by: its feed, apart per account (one group can be in two of them).
    var key: String { Self.key(id, account: account) }
    static func key(_ feed: String, account: Int?) -> String { account.map { "a\($0)-\(feed)" } ?? feed }

    /// The number on the avatar: unread messages, or 1 for a chat marked unread.
    var badge: Int { unread > 0 ? unread : (markedUnread == true ? 1 : 0) }
    var isPinned: Bool { pinned == true }
    var isFlagged: Bool { flagged == true }
    var isDone: Bool { done == true }
}

extension LarkMessage {
    /// The StateCache name of a chat's latest page (the chat paints from it before the network answers). A named
    /// manager's page keeps its own under the same prefix, which `LarkSync.displayChanged` clears.
    nonisolated static func cacheName(_ c: LarkConversation, manager: String? = nil) -> String {
        "lark-chat-" + c.key + (manager.map { "@" + $0 } ?? "")
    }
}

/// One Feishu account signed in on the hub's Lark gateway (`GET /api/v1/lark/accounts`).
nonisolated struct LarkAccount: Codable, Identifiable, Hashable, Sendable {
    var id: Int
    var name: String?
    var avatar: String?
    var tenant: String?
    var signedIn: Bool
    var primary: Bool?
    var unread: Int
    var quarantined: Bool?

    enum CodingKeys: String, CodingKey {
        case id, name, avatar, tenant, primary, unread, quarantined
        case signedIn = "signed_in"
    }

    var title: String { name?.isEmpty == false ? name! : String(localized: "Account \(String(id))") }
}

/// A flagged message in Flagged, with the chat it belongs to.
nonisolated struct LarkFlaggedMessage: Codable, Identifiable, Hashable, Sendable {
    var feedID: String
    var chatName: String
    var chatAvatar: String?
    var flaggedAt: Date?
    var message: LarkMessage
    var id: String { message.id }

    enum CodingKeys: String, CodingKey {
        case message
        case feedID = "feed_id", chatName = "chat_name", chatAvatar = "chat_avatar", flaggedAt = "flagged_at"
    }
}

nonisolated struct LarkLabel: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var name: String
    var count: Int?
}

/// One element of a message as the Lark gateway projects it (`MessagePart`): text runs, mentions, links, images,
/// emoji, files, media, code and cards. `metadata` stays raw JSON: cards carry their whole element tree there.
nonisolated struct LarkPart: Codable, Hashable, Sendable {
    var kind: String
    var text: String?
    var url: String?
    var refId: String?
    var metadata: JSONValue?

    enum CodingKeys: String, CodingKey { case kind, text, url, refId, metadata }

    func meta(_ key: String) -> JSONValue? { metadata?[key] }
    func metaString(_ key: String) -> String? { meta(key)?.string }
    func metaBool(_ key: String) -> Bool { meta(key)?.bool ?? false }

    /// Ancestor chain of a structured rich-text leaf (paragraph / list / quote / heading), outermost first.
    var blocks: [LarkRichBlock] {
        guard let list = meta("blocks")?.array else { return [] }
        var out: [LarkRichBlock] = []
        for item in list {
            guard let id = item["id"]?.string, let type = item["type"]?.string else { return [] }
            out.append(LarkRichBlock(id: id, type: type, listType: item["listType"]?.int ?? 0, start: item["start"]?.int ?? 1))
        }
        return out
    }
}

nonisolated struct LarkRichBlock: Hashable, Sendable {
    var id: String
    var type: String
    var listType: Int
    var start: Int
}

nonisolated struct LarkReplyTo: Codable, Hashable, Sendable {
    var olk: LarkEncryption? = nil
    var id: String
    var senderName: String
    var text: String
    var recalled: Bool?
    var retained: Bool? = nil
    var parts: [LarkPart]?

    enum CodingKeys: String, CodingKey {
        case id, text, recalled, retained, parts, olk
        case senderName = "sender_name"
    }
}

nonisolated struct LarkReaction: Codable, Hashable, Sendable {
    var key: String
    var count: Int
    var mine: Bool?
    var users: [String]?
}

nonisolated struct LarkMessage: Codable, Identifiable, Hashable, Sendable {
    var canPin: Bool? = nil
    var canUnpin: Bool? = nil
    var deleted: Bool? = nil
    var threadID: String? = nil
    var threadKind: String? = nil
    var olk: LarkEncryption? = nil
    var id: String
    var position: String
    var senderID: String?
    var senderName: String
    var senderAvatar: String?
    var senderType: String?
    var isMine: Bool
    var type: String
    var text: String
    var createdAt: Date?
    var recalled: Bool
    var edited: Bool?
    var retained: Bool? = nil
    var editHistory: [LarkEditVersion]? = nil
    var replyCount: Int
    var threadReplyCount: Int?
    var badgeCount: Int?
    var atMe: Bool?
    var isRead: Bool?
    var parts: [LarkPart]?
    var replyTo: LarkReplyTo?
    var reactions: [LarkReaction]?
    var clientID: String?
    var localState: String?
    var decorations: [LarkDecoration]? = nil

    enum CodingKeys: String, CodingKey {
        case id, position, type, text, recalled, retained, edited, parts, reactions, olk, deleted, decorations
        case canPin = "can_pin", canUnpin = "can_unpin", threadID = "thread_id", threadKind = "thread_kind"
        case editHistory = "edit_history"
        case senderID = "sender_id", senderName = "sender_name", senderAvatar = "sender_avatar", senderType = "sender_type"
        case isMine = "is_mine", createdAt = "created_at", replyCount = "reply_count"
        case threadReplyCount = "thread_reply_count", replyTo = "reply_to"
        case badgeCount = "badge_count", atMe = "at_me", isRead = "is_read"
        case clientID = "client_id", localState = "local_state"
    }

    var isSystem: Bool { type == "SYSTEM" || (parts?.first?.metaBool("system") ?? false) }
}

/// A line under a message's body, from its messaging source: words, a symbol of the fixed set, a tone.
nonisolated struct LarkDecoration: Codable, Hashable, Sendable {
    var text: String
    var icon: String?
    var tone: String?
}

/// A message inside a merge-forward card: the Lark gateway's own `MessageView` shape (camelCase), media already
/// pointing at the hub.
nonisolated struct LarkForwardedMessage: Decodable, Identifiable, Hashable, Sendable {
    var id: String
    var senderName: String?
    var senderAvatarUrl: String?
    var createdAt: Date?
    var parts: [LarkPart]?
    var textPreview: String?
    var isRecalled: Bool?
    var hasRetainedRecallContent: Bool?
    var olk: LarkEncryption?
}

extension APIClient {
    nonisolated struct LarkConversations: Decodable, Sendable {
        var conversations: [LarkConversation]
        var nextCursor: String?
        var accountID: String? = nil
        enum CodingKeys: String, CodingKey { case conversations; case nextCursor = "next_cursor", accountID = "account_id" }

        /// Filter one generic page without losing its account stamp or the way to older matching chats.
        func unreadOnly() -> Self {
            var page = self
            page.conversations = conversations.filter { $0.badge > 0 }
            return page
        }
    }
    nonisolated struct LarkMessages: Decodable, Sendable {
        var messages: [LarkMessage]
        var hasOlder: Bool
        var hasNewer: Bool?
        var olderCursor: String?
        var newerCursor: String?
        /// The chat is a topic group and these are its topics (the hub found out even when the request did not say so).
        var topic: Bool?
        var accountID: String? = nil
        enum CodingKeys: String, CodingKey {
            case messages, topic
            case accountID = "account_id"
            case hasOlder = "has_older", hasNewer = "has_newer", olderCursor = "older_cursor", newerCursor = "newer_cursor"
        }
    }
    struct LarkEmojis: Decodable {
        struct Emoji: Decodable { var label: String; var url: String }
        struct Group: Decodable, Hashable { var title: String; var keys: [String] }
        var emojis: [String: Emoji]
        var groups: [Group]?
    }
    struct LarkFlags: Decodable {
        var conversations: [LarkConversation]
        var messages: [LarkFlaggedMessage]
    }
    struct LarkChatLabels: Decodable {
        var labelIDs: [String]
        var labels: [LarkLabel]
        enum CodingKeys: String, CodingKey { case labels; case labelIDs = "label_ids" }
    }
    /// What to change on a chat: Done, Pin, Flag, Mute, labels. Nil fields stay as they are.
    struct LarkSettings: Encodable {
        var done: Bool?
        var pinned: Bool?
        var flagged: Bool?
        var muted: Bool?
        var labelIDs: [String]?
        enum CodingKeys: String, CodingKey { case done, pinned, flagged, muted; case labelIDs = "label_ids" }
    }
    struct LarkReadReport: Encodable {
        var position: String
        var badgeCount: Int?
        var messageIDs: [String]?
        enum CodingKeys: String, CodingKey { case position; case badgeCount = "badge_count", messageIDs = "message_ids" }
    }

    struct LarkAccounts: Codable, Sendable {
        var accounts: [LarkAccount]
        /// lark.account: the one requests without `ol_account` are about.
        var `default`: Int
    }
    /// The accounts on the hub's Lark gateway; a hub from before there were several answers 404.
    func larkAccounts() async throws -> LarkAccounts {
        if let source = messagingSource {
            return try await messagingCall {
                try await messagingGet(source, "accounts") { MessagingShape.accounts($0, source: source) }
            }
        }
        return try await get("/api/v1/lark/accounts")
    }

    /// One page of a feed scope: `chats` (Messages), `unread`, `done` (Done), `label:<id>`.
    func larkConversations(scope: String = "chats", cursor: String? = nil) async throws -> LarkConversations {
        if let source = messagingSource, scope == "chats" { return try await messagingConversations(source, cursor: cursor, pinned: false) }
        return try await get("/api/v1/lark/conversations", query: ["scope": scope, "cursor": cursor ?? "", "limit": "60"])
    }
    /// The browser opts into account checks without changing inbox or prefetch reads.
    func larkConversationPage(cursor: String?, accountID: String, scope: String = "chats") async throws -> LarkConversations {
        if let source = messagingSource {
            let page = try await messagingConversations(source, cursor: cursor, pinned: false)
            // The generic messaging contract has no scope parameter. Keep its cursor even on a read-only page.
            return scope == "unread" ? page.unreadOnly() : page
        }
        return try await get("/api/v1/lark/conversations", query: ["scope": scope, "cursor": cursor ?? "", "limit": "60",
            "account_bound": "1", "account_id": accountID])
    }
    func larkPinnedSnapshot() async throws -> LarkConversations {
        if let source = messagingSource { return try await messagingConversations(source, cursor: nil, pinned: true) }
        return try await get("/api/v1/lark/pinned", query: ["account_bound": "1"])
    }
    func larkPinned() async throws -> [LarkConversation] {
        if let source = messagingSource { return try await messagingConversations(source, cursor: nil, pinned: true).conversations }
        return try await get("/api/v1/lark/pinned", as: LarkConversations.self).conversations
    }
    func larkLabels() async throws -> [LarkLabel] {
        struct R: Decodable { var labels: [LarkLabel] }
        return try await get("/api/v1/lark/labels", as: R.self).labels
    }
    func larkUnreadCount() async throws -> Int {
        struct R: Decodable { var count: Int }
        return try await get("/api/v1/lark/unread-count", as: R.self).count
    }
    func larkFlags() async throws -> LarkFlags { try await get("/api/v1/lark/flags") }
    func larkChatLabels(_ id: String) async throws -> LarkChatLabels { try await get("/api/v1/lark/conversations/\(id)/labels") }
    func larkUpdate(_ id: String, _ settings: LarkSettings) async throws {
        try await post("/api/v1/lark/conversations/\(id)/settings", body: settings)
    }
    struct LarkUnread: Encodable { var unread: Bool }
    func larkSetUnread(_ id: String, unread: Bool) async throws {
        try await post("/api/v1/lark/conversations/\(id)/unread", body: LarkUnread(unread: unread))
    }
    /// A page of a chat: the latest (no cursor), older than `before`, newer than `after`, or around `around`;
    /// a topic group's threads with `topic`.
    /// Decoded off the main thread: an open chat asks again on every change in it, and a page of cards takes a few ms.
    func larkMessages(_ id: String, before: String? = nil, after: String? = nil, around: String? = nil, topic: Bool = false) async throws -> LarkMessages {
        if let source = messagingSource {
            return try await messagingMessages(source, id, before: before, after: after, around: around, topic: topic)
        }
        return try await getDecodedOffMain("/api/v1/lark/conversations/\(id)/messages",
                                    query: ["before": before ?? "", "after": after ?? "", "around": around ?? "", "limit": "40", "topic": topic ? "1" : ""])
    }
    /// Replies in one message's thread (decoded off the main thread, like `larkMessages`).
    /// A page of a thread, oldest first: the newest `limit` replies, or those before `before`. The Lark gateway puts the thread's
    /// first message (a topic) in front of every page (`replies(of:)` drops it).
    func larkThread(_ messageID: String, before: String? = nil, limit: Int = 60) async throws -> LarkMessages {
        if let source = messagingSource {
            return try await messagingMessages(source, messageID, before: before, after: nil, around: nil, topic: false, thread: true, limit: limit)
        }
        return try await getDecodedOffMain("/api/v1/lark/messages/\(messageID)/thread", query: ["before": before ?? "", "limit": String(limit)])
    }
    struct LarkText: Encodable {
        var text: String
        /// The same for every try of one message, so the hub sends it once however often it is retried.
        var clientID: String?
        /// Who Feishu notifies; each one's `@name` is in `text`. Left out when nobody is mentioned (an older hub
        /// ignores it anyway).
        var mentions: [LarkMention]?
        enum CodingKeys: String, CodingKey { case text, mentions, clientID = "client_id" }
    }
    /// The Lark gateway can take a while to confirm a send; a retry after an early timeout is what sent twice.
    static let larkSendTimeout: TimeInterval = 45
    @discardableResult
    func larkSend(_ id: String, text: String, clientID: String? = nil, mentions: [LarkMention] = []) async throws -> LarkSendReceipt {
        if let source = messagingSource { return try await messagingSend(source, id, text: text, clientID: clientID, mentions: mentions) }
        return try await post("/api/v1/lark/conversations/\(id)/messages",
                       body: LarkText(text: text, clientID: clientID, mentions: mentions.isEmpty ? nil : mentions),
                       timeout: Self.larkSendTimeout)
    }
    struct LarkReplyBody: Encodable {
        var text: String
        /// True: a reply in the message's thread; false: a quoted reply in its chat.
        var thread: Bool
        var clientID: String?
        var mentions: [LarkMention]?
        enum CodingKeys: String, CodingKey { case text, thread, mentions, clientID = "client_id" }
    }
    /// A reply to one message: quoted in its chat, or in its thread. Same idempotency and timeout as `larkSend`.
    @discardableResult
    func larkReply(messageID: String, text: String, thread: Bool, clientID: String? = nil, mentions: [LarkMention] = [],
                   feed: String? = nil) async throws -> LarkSendReceipt {
        if let source = messagingSource, let feed {
            return try await messagingSend(source, feed, text: text, clientID: clientID, mentions: mentions, replyTo: messageID, thread: thread)
        }
        return try await post("/api/v1/lark/messages/\(messageID)/replies",
                       body: LarkReplyBody(text: text, thread: thread, clientID: clientID, mentions: mentions.isEmpty ? nil : mentions),
                       timeout: Self.larkSendTimeout)
    }
    struct LarkMembers: Decodable { var members: [LarkMember] }
    /// A chat's members for the @ picker: all of them for an empty `query`, else the hub's search (name, group
    /// nickname, pinyin).
    func larkMembers(_ id: String, query: String, limit: Int = 30) async throws -> LarkMembers {
        if let source = messagingSource {
            return try await messagingCall {
                try await messagingGet(source, "conversations/\(id)/members", query: ["q": query, "limit": String(limit)]) { [account = messagingAccount] raw in
                    guard var o = raw as? [String: Any] else { return raw }
                    o["members"] = (o["members"] as? [[String: Any]] ?? []).map { m in
                        var x: [String: Any] = ["user_id": m["id"] ?? "", "name": m["name"] ?? ""]
                        if let a = m["avatar"] { x["avatar"] = MessagingShape.media(a, source: source, account: account) }
                        return x
                    }
                    return o
                }
            }
        }
        return try await get("/api/v1/lark/conversations/\(id)/members", query: ["q": query, "limit": String(limit)])
    }
    /// Reports what the reader has seen (never sent just for opening a chat).
    func larkRead(_ id: String, _ report: LarkReadReport) async throws {
        if let source = messagingSource {
            struct Body: Encodable { var position: String; var message_ids: [String]? }
            // A source takes at most 100 ids at a time (docs/extensions/extension-messaging.md): a long read goes in turns.
            let ids = report.messageIDs ?? []
            let batches: [[String]?] = ids.isEmpty ? [report.messageIDs]
                : stride(from: 0, to: ids.count, by: 100).map { Array(ids[$0 ..< min($0 + 100, ids.count)]) }
            for batch in batches {
                _ = try await messagingCall {
                    try await request("POST", messagingPath(source, "conversations/\(id)/read"), query: messagingQuery(),
                                      body: Body(position: report.position, message_ids: batch))
                }
            }
            return
        }
        try await post("/api/v1/lark/conversations/\(id)/read", body: report)
    }
    func larkEmojis() async throws -> LarkEmojis {
        if let source = messagingSource {
            return try await messagingGet(source, "emojis") { raw in
                guard var o = raw as? [String: Any], let e = o["emojis"] as? [String: [String: Any]] else { return raw }
                // Looked up lowercased (LarkMedia.emoji), as the old API sent them; the keys sent back (reactions, the
                // picker's groups) keep the source's own spelling.
                o["emojis"] = Dictionary(e.map { k, v in
                    (k.lowercased(), ["label": v["label"] ?? "", "url": MessagingShape.media(v["url"], source: source, account: nil) ?? ""] as [String: Any])
                }, uniquingKeysWith: { a, _ in a })
                return o
            }
        }
        return try await get("/api/v1/lark/emojis")
    }
    /// Presses a callback button on a card, as the user.
    func larkCardAction(message: String, action: String) async throws {
        try await post("/api/v1/lark/messages/\(message)/card-actions/\(action)")
    }

    // MARK: People, reactions, link previews, Moments

    func larkProfile(_ userID: String, feedID: String? = nil) async throws -> LarkProfile {
        if let source = messagingSource {
            return try await messagingCall {
                try await messagingGet(source, "users/\(userID)", query: ["conversation": feedID ?? ""]) { [account = messagingAccount] raw in
                    MessagingShape.profile(raw, source: source, account: account)
                }
            }
        }
        return try await get("/api/v1/lark/users/\(userID)", query: ["feed_id": feedID ?? ""])
    }
    /// The 1:1 chat with a user (created when there is none yet).
    func larkDirectChat(_ userID: String) async throws -> String {
        if let source = messagingSource {
            struct R: Decodable { var conversation_id: String }
            return try await messagingCall {
                try JSON.decoder.decode(R.self, from: await request("POST", messagingPath(source, "users/\(userID)/chat"),
                                                                    query: messagingQuery(), body: [String: String]())).conversation_id
            }
        }
        struct R: Decodable { var feed_id: String }
        return try await post("/api/v1/lark/users/\(userID)/chat", as: R.self).feed_id
    }
    struct LarkReact: Encodable { var key: String; var remove: Bool }
    func larkReact(_ messageID: String, key: String, remove: Bool) async throws {
        if let source = messagingSource {
            _ = try await messagingCall {
                try await request("POST", messagingPath(source, "messages/\(messageID)/reactions"), query: messagingQuery(),
                                  body: LarkReact(key: key, remove: remove))
            }
            return
        }
        try await post("/api/v1/lark/messages/\(messageID)/reactions", body: LarkReact(key: key, remove: remove))
    }
    func larkQuickReactions() async throws -> [String] {
        struct R: Decodable { var keys: [String] }
        if let source = messagingSource { return try await messagingGet(source, "reactions/quick", as: R.self) { $0 }.keys }
        return try await get("/api/v1/lark/reactions/quick", as: R.self).keys
    }
    func larkURLPreview(message: String, url: String) async throws -> LarkURLPreview {
        if let source = messagingSource {
            return try await messagingGet(source, "messages/\(message)/url-preview", query: ["url": url]) { [account = messagingAccount] raw in
                MessagingShape.urlPreview(raw, source: source, account: account)
            }
        }
        return try await get("/api/v1/lark/messages/\(message)/url-preview", query: ["url": url])
    }
    func larkMomentsPost(_ id: String) async throws -> LarkMomentsPost {
        if let source = messagingSource {
            return try await messagingGet(source, "posts/\(id)") { [account = messagingAccount] in MessagingShape.post($0, source: source, account: account) }
        }
        return try await get("/api/v1/lark/moments/posts/\(id)")
    }
    func larkMomentsComments(_ id: String) async throws -> [LarkMomentsComment] {
        struct R: Decodable { var comments: [LarkMomentsComment] }
        if let source = messagingSource {
            return try await messagingGet(source, "posts/\(id)/comments", as: R.self) { [account = messagingAccount] in
                MessagingShape.postComments($0, source: source, account: account)
            }.comments
        }
        return try await get("/api/v1/lark/moments/posts/\(id)/comments", as: R.self).comments
    }
}

/// A person's profile card (the Lark gateway's `UserProfileDetail`, the fields the card shows).
nonisolated struct LarkProfile: Decodable, Sendable {
    struct Field: Decodable, Hashable, Sendable {
        var key: String
        var label: String?
        var type: Int?
        var value: JSONValue?
        var url: String?
    }
    struct Action: Decodable, Hashable, Sendable {
        var key: String
        var label: String?
        var enabled: Bool?
    }
    struct Status: Decodable, Sendable { var title: String? }
    struct WorkStatus: Decodable, Sendable { var status: Int? }
    struct Relation: Decodable, Sendable {
        var external: Bool?
        var tenantName: String?
        var memo: String?
    }
    var displayName: String?
    var enName: String?
    var alias: String?
    var anotherName: String?
    var groupNickname: String?
    var avatarUrl: String?
    var coverUrl: String?
    var description: String?
    var medalName: String?
    var timezone: String?
    var userType: String?
    var customStatus: Status?
    var workStatus: WorkStatus?
    var relation: Relation?
    var fields: [Field]?
    var actions: [Action]?
}

/// The card Feishu shows under a link (a Moments post: author, excerpt, counts).
nonisolated struct LarkURLPreview: Decodable, Sendable {
    struct Card: Decodable, Sendable {
        var author: String?
        var avatarUrl: String?
        var body: String?
        var footer: String?
        var subtitle: String?
    }
    var title: String?
    var host: String?
    var url: String?
    var card: Card?
}

/// One Moments post.
nonisolated struct LarkMomentsPost: Decodable, Sendable {
    struct Segment: Decodable, Sendable { var kind: String?; var text: String?; var key: String?; var emojiKey: String? }
    struct Content: Decodable, Sendable { var text: String?; var segments: [Segment]? }
    struct Image: Decodable, Hashable, Sendable { var url: String?; var width: Double?; var height: Double? }
    struct ReactionGroup: Decodable, Hashable, Sendable { var type: String; var count: Int; var selfInvolved: Bool? }
    var id: String
    var userName: String?
    var userId: String?
    var userAvatarUrl: String?
    var content: Content?
    var images: [Image]?
    var reactionGroups: [ReactionGroup]?
    var reactionCount: Int?
    var commentCount: Int?
    var createTime: Double?
    var categoryNames: [String]?
}

nonisolated struct LarkMomentsComment: Decodable, Identifiable, Sendable {
    var id: String
    var userName: String?
    var userId: String?
    var userAvatarUrl: String?
    var content: LarkMomentsPost.Content?
    var createTime: Double?
    var reactionCount: Int?
    var isHot: Bool?
}
