#if DEBUG
// i18n-ignore-file: DEBUG-only made-up knowledge-base sample for screenshots
import Foundation
import PersonaWire

/// Knowledge-base search for screenshots of the demo (DEBUG only): the demo's search answers nothing, so its hits, an
/// entry's matches and a chat's day were never seen. Only with `-showKnowledgeSearch <text>` or `-showKnowledgeDay YES`
/// does `DemoHub` answer from here; everything is made up.
enum DemoKBSample {
    static var asked: Bool {
        let d = UserDefaults.standard
        return d.string(forKey: "showKnowledgeSearch") != nil || d.bool(forKey: "showKnowledgeDay")
    }

    static let chatID = "weather-team"
    static let chatName = "天气 App 小组"

    /// The answer for `p` (the path after /api/v1), nil when this sample has nothing to say about it.
    /// `asked`: whether a launch flag asked for it (tests pass it in).
    static func answer(_ p: [String], _ q: [String: String], kb: JSONValue?, asked: Bool = asked, now: Date = .now) -> JSONValue? {
        guard asked else { return nil }
        let today = day(now, 0), earlier = day(now, -2)
        switch (p.first ?? "", p.count) {
        case ("kb-search", 1):
            let weather = kb?["projects"]?.array?.first(where: { $0["id"]?.string == "weather" }) ?? .object([:])
            let han = kb?["people"]?.array?.first(where: { $0["id"]?.string == "han-meimei" }) ?? .object([:])
            let results: [JSONValue] = [
                ["entry": weather, "count": 4, "hits": [
                    hit("projects/weather/summary.md", "- 主屏幕小组件这周上线，深色图标还在做", 3),
                    hit("projects/weather/log.md", "- \(today) 14:20 · Agent · 给天气 App 加主屏幕小组件 · macbook · 小组件的时间线改成每 15 分钟刷新", 8),
                ]],
                ["entry": chatEntry, "count": 6, "hits": [
                    hit("chats/\(chatID)/messages/\(today).md", "- 14:03 韩梅梅: 小组件的截图我发群里了，周五前定稿", 4),
                    hit("chats/\(chatID)/messages/\(earlier).md", "- 10:41 李雷: 小组件的深色图标我今晚出两版", 2),
                ]],
                ["entry": han, "count": 1, "hits": [
                    hit("people/han-meimei/summary.md", "- 负责小组件上线的发布说明", 2),
                ]],
            ]
            return ["hits": .array([]), "results": .array(results)]
        case ("kb", 3) where p[1] == "chats" && p[2] == chatID:
            let detail: JSONValue = [
                "kind": "chats", "id": .string(chatID), "path": .string("chats/\(chatID)"),
                "meta": ["name": .string(chatName), "importance": "P1", "relevance": 80, "one_line": "天气 App 的产品、设计、开发在这里对进度"],
                "summary": "# 天气 App 小组\n\n天气 App 的产品、设计、开发在这里对进度。", "log": .array([]), "links": .array([]),
            ]
            var out: [String: JSONValue] = ["detail": detail, "agents": .array([])]
            if q["q"] != nil {
                out["matches"] = [
                    match(today, 4, "- 14:03 韩梅梅: 小组件的截图我发群里了，周五前定稿",
                          before: ["- 13:58 李雷: 新图标放到设计稿第三页了"], after: ["- 14:05 我: 好，我下午过一遍"]),
                    match(today, 8, "- 16:30 我: 小组件的时间线改成每 15 分钟刷新了，耗电看着还行",
                          before: ["- 16:12 韩梅梅: 测试那边说刷新有点慢"], after: ["- 16:31 韩梅梅: 收到"]),
                    match(earlier, 2, "- 10:41 李雷: 小组件的深色图标我今晚出两版",
                          before: ["- 10:38 韩梅梅: 深色模式下图标太亮了"], after: []),
                ]
            }
            return .object(out)
        case ("kb", 5) where p[1] == "chats" && p[2] == chatID && p[3] == "days":
            let messages: [String]
            if p[4] == earlier {
                messages = ["- 10:38 韩梅梅: 深色模式下图标太亮了", "- 10:41 李雷: 小组件的深色图标我今晚出两版",
                            "- 10:45 我: 可以，顺便看看锁屏上的样子"]
            } else {
                messages = ["- 09:12 韩梅梅: 早，今天把发布说明的初稿过一下", "- 09:20 我: 好，十点后有空",
                            "- 13:58 李雷: 新图标放到设计稿第三页了", "- 14:03 韩梅梅: 小组件的截图我发群里了，周五前定稿",
                            "  三张：浅色、深色、锁屏", "- 14:05 我: 好，我下午过一遍", "- 16:12 韩梅梅: 测试那边说刷新有点慢",
                            "- 16:30 我: 小组件的时间线改成每 15 分钟刷新了，耗电看着还行", "- 16:31 韩梅梅: 收到"]
            }
            // Line numbers as in a day file: a continuation line is not a message of its own.
            var n = 0
            var rows: [JSONValue] = []
            for m in messages {
                n += 1
                if m.hasPrefix("  "), case .object(var last) = rows.popLast() {
                    last["text"] = .string((last["text"]?.string ?? "") + "\n" + m.trimmingCharacters(in: .whitespaces))
                    rows.append(.object(last))
                } else {
                    rows.append(["n": .number(Double(n)), "text": .string(m)])
                }
            }
            let isEarlier = p[4] == earlier
            return ["day": .string(p[4]), "messages": .array(rows),
                    "prev": .string(isEarlier ? "" : earlier), "next": .string(isEarlier ? today : "")]
        default:
            return nil
        }
    }

    private static var chatEntry: JSONValue {
        ["kind": "chats", "id": .string(chatID), "name": .string(chatName), "one_line": "天气 App 的产品、设计、开发在这里对进度",
         "importance": "P1", "relevance": 80, "tags": .array([])]
    }

    private static func hit(_ path: String, _ line: String, _ n: Int) -> JSONValue {
        ["path": .string(path), "line": .string(line), "n": .number(Double(n))]
    }

    private static func match(_ day: String, _ n: Int, _ line: String, before: [String], after: [String]) -> JSONValue {
        ["path": .string("chats/\(chatID)/messages/\(day).md"), "n": .number(Double(n)), "line": .string(line), "day": .string(day),
         "before": .array(before.map(JSONValue.string)), "after": .array(after.map(JSONValue.string))]
    }

    /// "yyyy-MM-dd", `offset` days from `now`.
    static func day(_ now: Date, _ offset: Int) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd"
        return f.string(from: Calendar.current.date(byAdding: .day, value: offset, to: now) ?? now)
    }
}
#endif
