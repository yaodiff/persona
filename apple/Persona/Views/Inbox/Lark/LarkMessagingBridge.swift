import Foundation
import SwiftUI
import PersonaWire

/// The messaging extension point under the Feishu screens (#2138, docs/extensions/extension-messaging.md): while the hub has a
/// messaging source, the `lark…` requests of APIClient go to `/api/v1/messaging/<source>/…` and what comes back is
/// history by the display settings, cards) is the extension's now; this only reshapes. Stage 3a of the migration:
/// the screens move onto the generic models next, and this goes with the old API.
nonisolated enum MessagingSources {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var byHub: [String: String] = [:]
    nonisolated(unsafe) private static var byManager: [String: String] = [:]
    nonisolated(unsafe) private static var users: [String: (user: String, at: Date)] = [:]

    /// The source the Feishu screens use on this hub, if it has one. Kept across launches: a launch that opens a chat
    /// before the hub's hello is read already asks the source, not the old /api/v1/lark.
    static func source(for hub: URL) -> String? {
        lock.lock(); defer { lock.unlock() }
        let k = key(hub)
        if let s = byHub[k] { return s.isEmpty ? nil : s }
        let s = UserDefaults.standard.string(forKey: "messagingSource." + k) ?? ""
        byHub[k] = s
        return s.isEmpty ? nil : s
    }

    /// Records the hub's source; true when it changed (what is on screen came from the other one).
    @discardableResult static func set(_ source: String?, for hub: URL) -> Bool {
        let was = Self.source(for: hub)
        lock.lock(); defer { lock.unlock() }
        let k = key(hub)
        byHub[k] = source ?? ""
        guard was != source else { return false }
        if let source { UserDefaults.standard.set(source, forKey: "messagingSource." + k) } else { UserDefaults.standard.removeObject(forKey: "messagingSource." + k) }
        return true
    }

    /// A named manager's source on this hub: each manager has its own extensions, so its own sources (docs/assistant/managers.md
    /// "Clients"). Read through it when its chat opens (`APIClient.readMessagingSource`), kept for this launch; nil until
    /// one is found. The default's is `source(for:)`.
    static func source(for hub: URL, manager: String?) -> String? {
        guard let manager else { return source(for: hub) }
        lock.lock(); defer { lock.unlock() }
        let s = byManager[key(hub) + "@" + manager] ?? ""
        return s.isEmpty ? nil : s
    }

    /// The named manager's source was found this launch: its chats don't ask again.
    static func knows(_ hub: URL, manager: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return byManager[key(hub) + "@" + manager] != nil
    }

    /// Only a source found is kept: with none (an extension restarting lists none for a moment) the next open asks again.
    static func set(_ source: String, for hub: URL, manager: String) {
        lock.lock(); defer { lock.unlock() }
        byManager[key(hub) + "@" + manager] = source
    }

    /// One hub however its address is written (with or without a trailing /): its scheme, host, port and path.
    private static func key(_ url: URL) -> String {
        var path = url.path
        while path.hasSuffix("/") { path.removeLast() }
        return "\(url.scheme ?? "")://\(url.host ?? ""):\(url.port ?? 0)\(path)"
    }

    /// `GET me`'s user, by hub, account and manager: what the screens call the account id (outbox, pins, history checks).
    /// Nil when not read yet, or read longer than `maxAge` ago.
    static func user(_ key: String, maxAge: TimeInterval = .infinity) -> String? {
        lock.lock(); defer { lock.unlock() }
        guard let u = users[key], Date().timeIntervalSince(u.at) < maxAge else { return nil }
        return u.user
    }

    static func remember(_ user: String?, _ key: String) {
        lock.lock(); defer { lock.unlock() }
        users[key] = user.map { ($0, Date()) }
    }
}

extension APIClient {
    /// The messaging source the Feishu requests go to, the named manager's own; nil: the old /api/v1/lark.
    var messagingSource: String? { MessagingSources.source(for: config.url, manager: managerID) }

    /// Reads the named manager's sources through it before its Feishu screens read anything, until one is found this launch
    /// (the default's are the hub's, `HubLink.refreshMessagingSources`). `features`: the hub's at its last hello.
    func readMessagingSource(features: Set<String>) async {
        guard let managerID, features.contains("messaging"), !MessagingSources.knows(config.url, manager: managerID),
              let raw = try? await request("GET", "/api/v1/messaging"),
              let list = try? JSON.decoder.decode(ExtensionMessagingSources.self, from: raw),
              let source = list.sources.first?.id else { return }
        MessagingSources.set(source, for: config.url, manager: managerID)
    }

    /// A path under the source.
    func messagingPath(_ source: String, _ rest: String) -> String { "/api/v1/messaging/\(source)/" + rest }

    /// The query with this client's account (the old `ol_account`), as the protocol's `account`.
    func messagingQuery(_ query: [String: String] = [:]) -> [String: String] {
        var q = query
        if let larkAccount { q["account"] = String(larkAccount) }
        return q
    }

    /// By hub, account and manager: two managers may be signed in as two users. The default's names none.
    private var userKey: String { config.url.absoluteString + "#" + (larkAccount.map(String.init) ?? "") + (managerID.map { "@" + $0 } ?? "") }

    /// The account's user (`GET me`), kept for the run (or read again once older than `maxAge`): the old screens'
    /// `account_id`.
    func messagingUser(_ source: String, refresh: Bool = false, maxAge: TimeInterval = .infinity) async throws -> String {
        if !refresh, let u = MessagingSources.user(userKey, maxAge: maxAge) { return u }
        let raw = try await request("GET", messagingPath(source, "me"), query: messagingQuery())
        guard let me = try JSONSerialization.jsonObject(with: raw) as? [String: Any], let user = me["user_id"] as? String, !user.isEmpty
        else { throw LarkOutboxIdentity.Failure.unavailable }
        MessagingSources.remember(user, userKey)
        return user
    }

    /// The account the requests name (the protocol's `account`); nil: the source's default.
    var messagingAccount: String? { larkAccount.map(String.init) }

    /// A GET under the source, reshaped and decoded.
    func messagingGet<T: Decodable>(_ source: String, _ rest: String, query: [String: String] = [:], as: T.Type = T.self,
                                    shape: (Any) -> Any) async throws -> T {
        let raw = try await request("GET", messagingPath(source, rest), query: messagingQuery(query))
        let json = try JSONSerialization.jsonObject(with: raw)
        try Self.messagingEcho(json, account: messagingAccount)
        return try JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: shape(json)))
    }

    /// The same off the main thread: pages of messages, as `getDecodedOffMain`.
    func messagingGetOffMain<T: Decodable & Sendable>(_ source: String, _ rest: String, query: [String: String] = [:], as: T.Type = T.self,
                                                      shape: @Sendable @escaping (Any) -> Any) async throws -> T {
        let raw = try await request("GET", messagingPath(source, rest), query: messagingQuery(query))
        let account = messagingAccount
        return try await Task.detached(priority: .userInitiated) {
            let json = try JSONSerialization.jsonObject(with: raw)
            try Self.messagingEcho(json, account: account)
            return try JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: shape(json)))
        }.value
    }

    /// An answer that says whose it is (`account`) must be the account asked for: another one's rows never land here.
    nonisolated static func messagingEcho(_ json: Any, account: String?) throws {
        guard let account, let echoed = (json as? [String: Any])?["account"] as? String, echoed != account else { return }
        throw APIError(status: 409, message: String(localized: "Lark account changed"), fields: ["code": "lark_account_changed"])
    }
}

/// From the protocol's shapes to the Feishu screens' (the inverse of what the extension does to the gateway's).
nonisolated enum MessagingShape {
    typealias Object = [String: Any]

    /// A media path under the source as the URL LarkMedia fetches with the hub's credential; https as it is.
    static func media(_ path: Any?, source: String, account: String?) -> String? {
        guard let p = path as? String, !p.isEmpty else { return nil }
        if p.hasPrefix("https://") || p.hasPrefix("http://") { return p }
        var url = "/api/v1/messaging/\(source)/" + p
        if let account { url += (url.contains("?") ? "&" : "?") + "account=" + account }
        return url
    }

    static func conversations(_ page: Any, source: String, account: String?, user: String?) -> Any {
        guard var o = page as? Object else { return page }
        o["conversations"] = (o["conversations"] as? [Object] ?? []).map { conversation($0, source: source, account: account) }
        if let user { o["account_id"] = user }
        o.removeValue(forKey: "account")
        return o
    }

    static func conversation(_ c: Object, source: String, account: String?) -> Object {
        let kind = c["kind"] as? String ?? "direct"
        var o: Object = ["id": c["id"] ?? "", "name": c["name"] ?? "", "is_group": kind == "group" || kind == "topic_group",
                         "is_topic": kind == "topic_group", "unread": c["unread"] ?? 0, "at_me": c["mentioned"] ?? false,
                         "muted": c["muted"] ?? false, "preview": c["preview"] ?? "", "is_mine": c["preview_mine"] ?? false]
        for (from, to) in [("marked_unread", "marked_unread"), ("pinned", "pinned"), ("preview_sender", "preview_sender"),
                           ("updated_at", "updated_at"), ("read_position", "read_position"), ("mention_position", "mention_position")] {
            if let v = c[from] { o[to] = v }
        }
        if let a = media(c["avatar"], source: source, account: account) { o["avatar"] = a }
        return o
    }

    static func page(_ p: Any, source: String, account: String?, user: String?) -> Any {
        guard var o = p as? Object else { return p }
        var messages = (o["messages"] as? [Object] ?? []).map { message($0, source: source, account: account) }
        if let root = o["root"] as? Object { messages.insert(message(root, source: source, account: account), at: 0) }
        o["messages"] = messages
        o.removeValue(forKey: "root")
        if let user { o["account_id"] = user }
        o.removeValue(forKey: "account")
        return o
    }

    static func message(_ m: Object, source: String, account: String?) -> Object {
        let sender = m["sender"] as? Object ?? [:]
        let senderKind = sender["kind"] as? String
        let parts = Self.parts(m["parts"], source: source, account: account)
        var o: Object = ["id": m["id"] ?? "", "position": m["position"] ?? "0", "sender_name": sender["name"] ?? "",
                         "is_mine": m["mine"] ?? false, "type": senderKind == "system" ? "SYSTEM" : "TEXT", "text": m["text"] ?? "",
                         "recalled": m["recalled"] ?? false, "parts": parts]
        if let v = sender["id"] { o["sender_id"] = v }
        if let a = media(sender["avatar"], source: source, account: account) { o["sender_avatar"] = a }
        if senderKind == "bot" { o["sender_type"] = "bot" }
        if (m["recalled"] as? Bool) == true, !parts.isEmpty { o["retained"] = true }
        let count = (m["thread"] as? Object)?["count"] as? Int ?? 0
        o["reply_count"] = count
        if count > 0 { o["thread_reply_count"] = count }
        for (from, to) in [("created_at", "created_at"), ("client_id", "client_id"), ("state", "local_state"), ("edited", "edited"),
                           ("read", "is_read"), ("decorations", "decorations")] {
            if let v = m[from] { o[to] = v }
        }
        // Not said is allowed (the source has chat_pins; the screens only offer pins on such a source).
        o["can_pin"] = m["can_pin"] ?? true
        o["can_unpin"] = m["can_unpin"] ?? true
        if let r = m["reply_to"] as? Object {
            // `recalled` marks a recalled message's kept words: shown with 「已撤回」 after them; a recall the source
            // does not show comes as its own summary (「消息已撤回」) without the mark (docs/extensions/extension-messaging.md「Conversations and messages」).
            o["reply_to"] = ["id": r["id"] ?? "", "sender_name": r["sender_name"] ?? "", "text": r["summary"] ?? "",
                             "recalled": r["recalled"] ?? false, "retained": r["recalled"] ?? false]
        }
        if let rs = m["reactions"] as? [Object] {
            o["reactions"] = rs.map { r in
                var x: Object = ["key": r["key"] ?? "", "count": r["count"] ?? 0]
                if let v = r["mine"] { x["mine"] = v }
                if let v = r["users"] { x["users"] = v }
                return x
            }
        }
        let sealed = (m["private"] as? Bool) == true
        if let hist = m["edit_history"] as? [Object] {
            o["edit_history"] = hist.map { v in
                let parts = Self.parts(v["parts"], source: source, account: account)
                var x: Object = ["text": v["text"] ?? "", "parts": parts]
                // A sealed message's earlier words are sealed as its body is (kept off the disk).
                if sealed { x = ["text": "", "olk": ["version": 1, "state": "decrypted", "publicParts": [Object](), "bodyParts": parts] as Object] }
                x["edited_at"] = v["edited_at"] ?? ""
                return x
            }
        }
        // A sealed message the extension opened: the screens' own sealed model keeps its body off the disk.
        if sealed {
            let cover = m["private_text"] as? String ?? ""
            o["olk"] = ["version": 1, "state": "decrypted", "publicParts": cover.isEmpty ? [] : [["kind": "text", "text": cover]],
                        "bodyParts": parts] as Object
            o["text"] = cover
        }
        return o
    }

    static func parts(_ raw: Any?, source: String, account: String?) -> [Object] {
        let list = raw as? [Object] ?? []
        var out: [Object] = []
        var lastBlock: (key: String, id: String)? = nil
        var lists: [ListRun] = [] // the lists the running list items are in, by level
        var n = 0
        // Each part's block chain as given (nil: in none), and the inline parts that did not say theirs.
        var chains: [[Object]?] = []
        var placed: Set<Int> = []
        /// The block chain of a part that says its block (text, and inline parts from a source that tells them).
        func chain(_ p: Object) -> [Object]? {
            guard let block = p["block"] as? String else {
                lastBlock = nil
                lists = []
                return nil
            }
            if block == "list_item" {
                // Consecutive items share their list (numbered on from it), a deeper one goes into the item before.
                let list = p["list"] as? Object
                let level = max(0, min(8, list?["level"] as? Int ?? 0))
                let ordered = (list?["ordered"] as? Bool) == true
                let item: String
                if let given = p["block_id"] as? String { item = given } else { n += 1; item = "m\(n)" }
                if lists.count > level + 1 { lists.removeLast(lists.count - level - 1) }
                while lists.count < level + 1 { n += 1; lists.append(ListRun(ordered: ordered, id: "l\(n)", item: "")) }
                if lists[level].ordered != ordered { n += 1; lists[level] = ListRun(ordered: ordered, id: "l\(n)", item: "") }
                if lists[level].item != item, let start = list?["start"], lists[level].item.isEmpty { lists[level].start = start }
                lists[level].item = item
                lastBlock = nil
                return Self.listChain(lists)
            }
            lists = []
            let id: String
            if let given = p["block_id"] as? String { id = given }
            else if let last = lastBlock, last.key == block { id = last.id }
            else { n += 1; id = "m\(n)" }
            lastBlock = (block, id)
            return blocks(block, list: nil, id: id)
        }
        for p in list {
            let text = p["text"] as? String ?? ""
            let kind = p["kind"] as? String
            let inline = kind == "mention" || kind == "link" || kind == "emoji"
            if inline, p["block"] == nil { placed.insert(out.count) }
            let given = kind == "text" || inline && p["block"] != nil ? chain(p) : nil
            switch kind {
            case "text":
                var meta: Object = [:]
                var marks: [String] = []
                for m in p["marks"] as? [String] ?? [] {
                    if m == "code" { meta["inlineCode"] = true } else { marks.append(m == "strike" ? "strikethrough" : m) }
                }
                if !marks.isEmpty { meta["marks"] = marks }
                if let given { meta["blocks"] = given }
                var x: Object = ["kind": "text", "text": text]
                // Words that link somewhere are a link.
                if let url = p["url"] as? String, !url.isEmpty { x = ["kind": "link", "text": text, "url": url] }
                if !meta.isEmpty { x["metadata"] = meta }
                out.append(x)
            case "mention":
                var x: Object = ["kind": "at", "text": text]
                if let u = p["user_id"] { x["refId"] = u }
                out.append(x)
            case "link":
                out.append(["kind": "link", "text": text, "url": p["url"] ?? ""])
            case "emoji":
                out.append(["kind": "emotion", "refId": p["key"] ?? text, "text": text])
            case "sticker":
                out.append(["kind": "emotion", "text": text, "url": media(p["media"], source: source, account: account) ?? ""])
            case "image":
                var meta: Object = [:]
                if let w = p["width"] { meta["width"] = w }
                if let h = p["height"] { meta["height"] = h }
                out.append(["kind": "image", "url": media(p["media"], source: source, account: account) ?? "", "metadata": meta])
            case "file":
                var meta: Object = [:]
                if let s = p["size"] { meta["size"] = String(describing: s) }
                if let e = p["ext"] { meta["extension"] = e }
                if (p["expired"] as? Bool) == true { meta["destroyed"] = true }
                var x: Object = ["kind": "file", "text": p["name"] ?? String(localized: "Files"), "metadata": meta]
                if let u = media(p["media"], source: source, account: account) { x["url"] = u }
                out.append(x)
            case "video", "audio":
                var meta: Object = ["mime": p["kind"] as? String == "audio" ? "audio/mpeg" : "video/mp4"]
                if let d = p["duration_ms"] { meta["duration"] = d }
                if let poster = p["poster"] as? Object {
                    meta["posterUrl"] = media(poster["media"], source: source, account: account)
                    if let w = poster["width"] { meta["posterWidth"] = w }
                    if let h = poster["height"] { meta["posterHeight"] = h }
                }
                out.append(["kind": "media", "url": media(p["media"], source: source, account: account) ?? "", "metadata": meta])
            case "code":
                var x: Object = ["kind": "code", "text": text]
                if let l = p["language"] { x["metadata"] = ["language": l] as Object }
                out.append(x)
            case "divider":
                out.append(["kind": "interactive", "text": "", "metadata": ["richTag": 32] as Object])
            case "system":
                out.append(["kind": "text", "text": text, "metadata": ["system": true] as Object])
            case "card":
                out.append(["kind": "card", "text": text, "metadata": ["variant": MessagingViewCard.variant, "view": p["view"] ?? [:]] as Object])
            case "forward":
                var meta: Object = ["variant": "merge-forward", "title": p["title"] ?? ""]
                if let ms = p["messages"] as? [Object] {
                    meta["messages"] = ms.map { forwarded($0, source: source, account: account) }
                }
                if let pv = p["previews"] as? [Object] {
                    meta["previews"] = pv.map { ["senderName": $0["sender_name"] ?? "", "text": $0["text"] ?? ""] as Object }
                }
                if let c = p["count"] { meta["messageCount"] = c }
                out.append(["kind": "card", "text": p["title"] ?? "", "metadata": meta])
            case "share":
                var meta: Object = ["variant": "share", "title": p["title"] ?? ""]
                if let s = p["subtitle"] { meta["eyebrow"] = s }
                if let img = p["image"] as? Object { meta["imageUrl"] = media(img["media"], source: source, account: account) }
                if let f = p["fields"] { meta["fields"] = f }
                out.append(["kind": "card", "text": p["title"] ?? "", "url": p["url"] ?? "", "metadata": meta])
            default:
                if !text.isEmpty { out.append(["kind": "text", "text": text]) }
            }
            if inline, let given, let last = out.indices.last {
                var meta = out[last]["metadata"] as? Object ?? [:]
                meta["blocks"] = given
                out[last]["metadata"] = meta
            }
            while chains.count < out.count { chains.append(given) }
        }
        // An @, a link or an emoji that did not say its block (a source that tells only the text's) is in the block of
        // the text around it: the one before it, or the one after when nothing before it is in a block (it starts the
        // message, or follows a picture). Without one the block would end there and the rest start a new line.
        for i in placed.sorted() {
            let before = (0..<i).reversed().first { !placed.contains($0) }.flatMap { chains[$0] }
            let after = (i + 1 ..< out.count).first { !placed.contains($0) }.flatMap { chains[$0] }
            guard let blocks = before ?? after else { continue }
            var meta = out[i]["metadata"] as? Object ?? [:]
            meta["blocks"] = blocks
            out[i]["metadata"] = meta
        }
        return out
    }

    /// One list a run of list items is in: its kind, its id, the item last in it (where a deeper list goes), its start.
    struct ListRun {
        var ordered: Bool
        var id: String
        var item: String
        var start: Any? = nil
    }

    /// The block chain of a list item: each enclosing list and the item in it, outermost first (`LarkPart.blocks`).
    static func listChain(_ lists: [ListRun]) -> [Object] {
        var chain: [Object] = []
        for l in lists {
            var node: Object = ["id": l.id, "type": l.ordered ? "ordered-list" : "unordered-list"]
            if let s = l.start { node["start"] = s }
            chain.append(node)
            chain.append(["id": l.item, "type": "list-item"])
        }
        return chain
    }

    /// The block chain a text part sits in, as the screens' rich text reads it (`LarkPart.blocks`).
    static func blocks(_ block: String, list: Object?, id: String) -> [Object] {
        switch block {
        case "heading1", "heading2", "heading3":
            return [["id": id, "type": "heading-" + String(block.last!)]]
        case "quote": return [["id": id, "type": "quote"]]
        case "code": return [["id": id, "type": "code-block"]]
        default: return [["id": id, "type": "paragraph"]]
        }
    }

    /// A message inside a forward, in the camelCase shape the merge-forward card reads.
    static func forwarded(_ m: Object, source: String, account: String?) -> Object {
        let sender = m["sender"] as? Object ?? [:]
        let body = parts(m["parts"], source: source, account: account)
        var o: Object = ["id": m["id"] ?? "", "senderName": sender["name"] ?? "", "textPreview": m["text"] ?? "",
                         "parts": body, "isRecalled": m["recalled"] ?? false]
        // A sealed message inside a forward is sealed as one in the chat is (`message`): its body only in memory, its
        // cover what the record says and what a cache keeps (JSONValue.larkCached keeps a record's olk cover only).
        if (m["private"] as? Bool) == true {
            let cover = m["private_text"] as? String ?? ""
            let coverParts: [Object] = cover.isEmpty ? [] : [["kind": "text", "text": cover]]
            o["olk"] = ["version": 1, "state": "decrypted", "publicParts": coverParts, "bodyParts": body] as Object
            o["textPreview"] = cover
        }
        if let a = media(sender["avatar"], source: source, account: account) { o["senderAvatarUrl"] = a }
        if let t = m["created_at"] { o["createdAt"] = t }
        return o
    }
}

// MARK: The Feishu screens' requests over the source

/// What `compose` answered: ready with a token, or a view the user fills first.
enum MessagingComposeStep: Sendable {
    case ready(String)
    case ask(ExtensionView)
}

extension APIClient {
    /// The protocol's error codes as the Feishu screens know them (not signed in, the account changed).
    func messagingCall<T>(_ body: () async throws -> T) async throws -> T {
        do { return try await body() } catch var e as APIError {
            switch e.code {
            case "not_signed_in": e.fields["code"] = "lark_not_logged_in"
            case "account_changed": e.fields["code"] = "lark_account_changed"
            default: break
            }
            throw e
        }
    }

    /// A mode that prepares (sealed): the source's `compose`, with what the user filled in so far.
    func messagingCompose(_ source: String, _ feed: String, mode: String, text: String, replyTo: String?,
                          values: [String: String]?) async throws -> MessagingComposeStep {
        struct Body: Encodable { var mode: String; var text: String; var reply_to: String?; var values: [String: String]? }
        struct Answer: Decodable { var ready: Bool?; var token: String?; var view: ExtensionView? }
        let raw = try await messagingCall {
            try await request("POST", messagingPath(source, "conversations/\(feed)/compose"), query: messagingQuery(),
                              body: Body(mode: mode, text: text, reply_to: replyTo, values: values))
        }
        let a = try JSONDecoder().decode(Answer.self, from: raw) // the generated View types carry their own keys
        if a.ready == true, let token = a.token { return .ready(token) }
        if let view = a.view { return .ask(view) }
        throw APIError(status: 502, message: String(localized: "The encryption service didn't say what to do next. Try again later."))
    }

    func messagingConversations(_ source: String, cursor: String?, pinned: Bool) async throws -> LarkConversations {
        try await messagingCall {
            // Whose list it is: the old API said so with every list; asked again when older than half a minute, so
            // another person signed in on the same account is not taken for the one before.
            let user = try await messagingUser(source, maxAge: cursor == nil ? 30 : .infinity)
            let account = messagingAccount
            return try await messagingGet(source, pinned ? "pinned" : "conversations",
                                          query: pinned ? [:] : ["cursor": cursor ?? "", "limit": "60"]) {
                MessagingShape.conversations($0, source: source, account: account, user: user)
            }
        }
    }

    func messagingMessages(_ source: String, _ id: String, before: String?, after: String?, around: String?, topic: Bool,
                           thread: Bool = false, limit: Int = 40) async throws -> LarkMessages {
        try await messagingCall {
            let user = try await messagingUser(source)
            let account = messagingAccount
            let rest = thread ? "messages/\(id)/thread" : "conversations/\(id)/messages"
            return try await messagingGetOffMain(source, rest, query: ["before": before ?? "", "after": after ?? "", "around": around ?? "",
                                                               "limit": String(limit), "topic": topic ? "1" : ""]) {
                MessagingShape.page($0, source: source, account: account, user: user)
            }
        }
    }

    func messagingSend(_ source: String, _ feed: String, text: String, clientID: String?, mentions: [LarkMention],
                       replyTo: String? = nil, thread: Bool = false, mode: String? = nil, prepared: String? = nil) async throws -> LarkSendReceipt {
        struct Body: Encodable {
            var client_id: String; var text: String; var mentions: [LarkMention]?
            var reply_to: String?; var thread: Bool?; var mode: String?; var prepared: String?; var expect_user: String?
        }
        return try await messagingCall {
            let user = try? await messagingUser(source)
            let body = Body(client_id: clientID ?? UUID().uuidString.lowercased(), text: text, mentions: mentions.isEmpty ? nil : mentions,
                            reply_to: replyTo, thread: replyTo != nil && thread ? true : nil, mode: mode, prepared: prepared, expect_user: user)
            let raw = try await request("POST", messagingPath(source, "conversations/\(feed)/messages"), query: messagingQuery(),
                                        body: body, timeout: Self.larkSendTimeout)
            return try JSON.decoder.decode(LarkSendReceipt.self, from: raw)
        }
    }

}

nonisolated extension MessagingShape {
    /// A profile as the person sheet reads it (the gateway's camelCase card).
    static func profile(_ raw: Any, source: String, account: String?) -> Any {
        guard let p = raw as? Object else { return raw }
        var o: Object = ["displayName": p["name"] ?? ""]
        if let a = media(p["avatar"], source: source, account: account) { o["avatarUrl"] = a }
        if let c = media(p["cover"], source: source, account: account) { o["coverUrl"] = c }
        if let d = p["description"] { o["description"] = d }
        // The line under the name (a note, another name): the name stays the title.
        if let s = p["subtitle"] { o["enName"] = s }
        if p["kind"] as? String == "bot" { o["userType"] = "bot" }
        if let s = p["status"] { o["customStatus"] = ["title": s] as Object }
        o["fields"] = (p["fields"] as? [Object] ?? []).enumerated().map { i, f in
            var x: Object = ["key": "F\(i)", "label": f["label"] ?? "", "value": f["value"] ?? ""] // type: the gateway's number, unused
            if let person = f["person"] as? String { x["url"] = "lark://client/profile?uid=" + person }
            else if let u = f["url"] { x["url"] = u }
            return x
        }
        return o
    }

    static func urlPreview(_ raw: Any, source: String, account: String?) -> Any {
        guard let p = raw as? Object else { return raw }
        var o: Object = ["url": p["url"] ?? ""]
        if let t = p["title"] { o["title"] = t }
        if let h = p["host"] { o["host"] = h }
        var card: Object = [:]
        if let s = p["summary"] { card["body"] = s }
        if let a = p["author"] as? Object {
            card["author"] = a["name"] ?? ""
            if let av = media(a["avatar"], source: source, account: account) { card["avatarUrl"] = av }
        }
        if !card.isEmpty { o["card"] = card }
        return o
    }

    /// The source's accounts as the account chips read them: numbers for ids, unread 0 when left out, avatars under
    /// the source as the account they are (the default one goes without `account`).
    static func accounts(_ raw: Any, source: String) -> Any {
        guard var o = raw as? Object else { return raw }
        let main = o["default"] as? String ?? "1"
        o["accounts"] = (o["accounts"] as? [Object] ?? []).map { a in
            var x = a
            let id = a["id"] as? String ?? ""
            x["id"] = Int(id) ?? 0
            x["unread"] = a["unread"] ?? 0
            x["avatar"] = media(a["avatar"], source: source, account: id == main ? nil : id)
            return x
        }
        o["default"] = Int(main) ?? 1
        return o
    }

    /// A post's or comment's parts as the Moments sheet reads them: `{text, segments}` (text runs and emoji).
    static func momentsContent(_ raw: Any?) -> Object {
        var segments: [Object] = []
        for part in raw as? [Object] ?? [] {
            if part["kind"] as? String == "emoji" { segments.append(["kind": "emoji", "text": part["text"] ?? "", "key": part["key"] ?? ""]) }
            else if let t = part["text"] as? String { segments.append(["kind": "text", "text": t]) }
        }
        return ["text": segments.compactMap { $0["text"] as? String }.joined(), "segments": segments]
    }

    /// A post as the Moments sheet reads it.
    static func post(_ raw: Any, source: String, account: String?) -> Any {
        guard let p = raw as? Object else { return raw }
        let author = p["author"] as? Object ?? [:]
        var o: Object = ["id": p["id"] ?? "", "userName": author["name"] ?? "", "content": momentsContent(p["parts"])]
        if let id = author["id"] { o["userId"] = id }
        if let a = media(author["avatar"], source: source, account: account) { o["userAvatarUrl"] = a }
        o["images"] = (p["images"] as? [Object] ?? []).map { i in
            var x: Object = ["url": media(i["media"], source: source, account: account) ?? ""]
            if let w = i["width"] { x["width"] = w }
            if let h = i["height"] { x["height"] = h }
            return x
        }
        let reactions = p["reactions"] as? [Object] ?? []
        o["reactionGroups"] = reactions.map { ["type": $0["key"] ?? "", "count": $0["count"] ?? 0, "selfInvolved": $0["mine"] ?? false] as Object }
        o["reactionCount"] = reactions.reduce(0) { $0 + ($1["count"] as? Int ?? 0) }
        if let c = p["comment_count"] { o["commentCount"] = c }
        if let t = p["created_at"] as? String, let d = JSON.parseDate(t) { o["createTime"] = d.timeIntervalSince1970 * 1000 }
        if let c = p["categories"] { o["categoryNames"] = c }
        return o
    }

    static func postComments(_ raw: Any, source: String, account: String?) -> Any {
        guard var o = raw as? Object else { return raw }
        o["comments"] = (o["comments"] as? [Object] ?? []).map { c in
            let author = c["author"] as? Object ?? [:]
            var x: Object = ["id": c["id"] ?? "", "userName": author["name"] ?? "", "content": momentsContent(c["parts"])]
            if let id = author["id"] { x["userId"] = id }
            if let a = media(author["avatar"], source: source, account: account) { x["userAvatarUrl"] = a }
            if let t = c["created_at"] as? String, let d = JSON.parseDate(t) { x["createTime"] = d.timeIntervalSince1970 * 1000 }
            if (c["hot"] as? Bool) == true { x["isHot"] = true }
            return x
        }
        return o
    }
}

/// A message's decorations from its source, under the body (#2138).
struct LarkDecorationsView: View {
    var decorations: [LarkDecoration]

    var body: some View {
        ForEach(Array(decorations.prefix(3).enumerated()), id: \.offset) { _, d in
            Label(d.text, systemImage: Self.symbol(d.icon))
                .font(Theme.Font.caption).foregroundStyle(d.tone.map { ExtensionUIStyle.tone($0) } ?? .secondary)
                .accessibilityIdentifier("lark.decoration")
        }
    }

    static func symbol(_ icon: String?) -> String {
        switch icon {
        case "lock": Theme.Icon.locked
        case "shield": Theme.Icon.protection
        default: Theme.Icon.info
        }
    }
}
