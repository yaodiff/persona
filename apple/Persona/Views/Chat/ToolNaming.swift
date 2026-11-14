import Foundation
import PersonaWire

/// The words a tool call reads as when no table names it (#2986): a third-party MCP tool as "<Server> · <Tool>"
/// ("mcp__github__create_issue" is "GitHub · Create issue"), any other unknown tool as its name in words, a subject
/// picked from its input by the field that says the most, and its arguments as a short key/value list instead of
/// JSON. Pure and off the main actor: the chat's memos warm the lists in the background (`ToolTextMemo`).
nonisolated enum ToolNaming {
    // MARK: Names

    /// Words spelled their own way wherever they show up in a name ("github" is "GitHub", "pr" is "PR").
    static let spelled: [String: String] = [
        "api": "API", "ai": "AI", "aws": "AWS", "ci": "CI", "cli": "CLI", "csv": "CSV", "devtools": "DevTools", "dns": "DNS",
        "gcp": "GCP", "github": "GitHub", "gitlab": "GitLab", "graphql": "GraphQL", "html": "HTML", "http": "HTTP",
        "https": "HTTPS", "id": "ID", "ids": "IDs", "ide": "IDE", "ios": "iOS", "json": "JSON", "llm": "LLM",
        "lsp": "LSP", "macos": "macOS", "mcp": "MCP", "oauth": "OAuth", "openai": "OpenAI", "pdf": "PDF", "pr": "PR",
        "prs": "PRs", "sdk": "SDK", "sql": "SQL", "ssh": "SSH", "ui": "UI", "uri": "URI", "url": "URL", "urls": "URLs",
        "youtube": "YouTube",
    ]

    /// An identifier cut into words: at `_`, `-`, `.`, `/`, `:` and spaces, and where camel case turns
    /// ("getPRComments" is get, PR, Comments).
    static func words(_ identifier: String) -> [String] {
        let chars = Array(identifier)
        var out: [String] = []
        var word = ""
        for (n, c) in chars.enumerated() {
            if "_-./: ".contains(c) {
                if !word.isEmpty { out.append(word) }
                word = ""
                continue
            }
            if !word.isEmpty, c.isUppercase {
                let before = chars[n - 1]
                let lowerNext = n + 1 < chars.count && chars[n + 1].isLowercase
                // "fooBar" turns at B; "HTTPServer" keeps HTTP together and turns at the S before "erver".
                if before.isLowercase || before.isNumber || (before.isUppercase && lowerNext) {
                    out.append(word)
                    word = ""
                }
            }
            word.append(c)
        }
        if !word.isEmpty { out.append(word) }
        return out
    }

    /// One word as it reads inside a sentence: the spelled ones as spelled, an acronym as written, the rest lowercase.
    private static func plain(_ word: String) -> String {
        if let s = spelled[word.lowercased()] { return s }
        if word.count > 1, word.allSatisfy({ $0.isUppercase || $0.isNumber }) { return word }
        return word.lowercased()
    }

    /// An identifier in words, the first one capitalized: "create_issue" and "createIssue" are "Create issue",
    /// "getPRComments" is "Get PR comments".
    static func humanize(_ identifier: String) -> String {
        let w = words(identifier).map(plain)
        guard let first = w.first else { return "" }
        // "iOS" stays as spelled; any other first word starts with a capital.
        let head = spelled.values.contains(first) ? first : first.prefix(1).uppercased() + first.dropFirst()
        return ([head] + w.dropFirst()).joined(separator: " ")
    }

    /// An MCP server's name as a name: "github" is "GitHub", "chrome-devtools" "Chrome DevTools". A plugin's server
    /// ("plugin_<plugin>_<server>") and a claude.ai connector ("claude_ai_<name>") read as their own name, once.
    static func serverName(_ server: String) -> String {
        var s = server
        for prefix in ["plugin_", "claude_ai_"] where s.hasPrefix(prefix) && s.count > prefix.count {
            s.removeFirst(prefix.count)
        }
        var w = words(s)
        // "plugin_context7_context7": the plugin and its server share the name.
        let half = w.count / 2
        if half > 0, w.count % 2 == 0, w[..<half].map({ $0.lowercased() }) == w[half...].map({ $0.lowercased() }) {
            w = Array(w[..<half])
        }
        return w.map { word in
            spelled[word.lowercased()] ?? (word.prefix(1).uppercased() + word.dropFirst())
        }.joined(separator: " ")
    }

    /// Another server's MCP tool, not Persona's own (`PersonaToolNames`): no table here names it by its bare name.
    static func isOtherMCP(_ name: String) -> Bool {
        name.hasPrefix("mcp__") && !PersonaToolNames.isPersona(name)
    }

    /// The name the tool tables match on: a tool's own name ("Read", "agent_read" of mcp__persona__agent_read), or for
    /// another server's MCP tool its whole name, which no table has ("mcp__github__search" is not Grep's search).
    static func tableName(_ name: String) -> String {
        isOtherMCP(name) ? name : ToolPresentation.baseName(name)
    }

    /// "GitHub · Create issue" for another server's MCP tool; nil for any other tool and Persona's own.
    static func mcpLabel(_ name: String) -> String? {
        guard isOtherMCP(name), let server = PersonaToolNames.server(name) else { return nil }
        let tool = humanize(String(name.dropFirst(5 + server.count + 2)))
        let shown = serverName(server)
        return tool.isEmpty ? shown : shown + " · " + tool
    }

    // MARK: Subjects

    /// The fields that say what a call is about, the most telling first.
    static let subjectKeys = ["description", "title", "subject", "name", "file_path", "filePath", "notebook_path", "path",
                              "url", "uri", "query", "pattern", "command", "prompt"]
    private static let pathKeys: Set<String> = ["file_path", "filePath", "notebook_path", "path"]

    /// A string value with something in it (numbers and flags say little on their own).
    static func text(_ value: JSONValue?) -> String? {
        guard case .string(let s)? = value else { return nil }
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }

    /// The first line with words on it.
    static func firstLine(_ s: String) -> String {
        s.split(whereSeparator: \.isNewline).lazy.map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
    }

    /// A path's last part ("/a/b/Store.swift" is "Store.swift"); a path that ends in "/" keeps its folder's name.
    static func lastPathComponent(_ path: String) -> String {
        path.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? path
    }

    /// What an unknown tool's call is about: the first of `subjectKeys` it has (a path by its file's name), else its
    /// first string by field name; "" when it has none.
    static func subject(_ input: JSONValue) -> String {
        guard let o = input.object else { return "" }
        for key in subjectKeys {
            if let v = text(o[key]) { return pathKeys.contains(key) ? lastPathComponent(v) : firstLine(v) }
        }
        for key in o.keys.sorted() {
            if let v = text(o[key]) { return firstLine(v) }
        }
        return ""
    }

    /// The tools that only do bookkeeping (loading tools, reading the to-do list, listing agents): they say nothing
    /// about the work, so a run of steps shows them only when there is nothing else.
    static let quiet: Set<String> = ["ToolSearch", "WaitForMcpServers", "ListMcpResourcesTool", "TaskList", "TaskGet",
                                     "ListAgents", "CronList"]

    static func isQuiet(_ name: String) -> Bool { quiet.contains(tableName(name)) }

    /// Whether a call counts in a run's summary: any but bookkeeping, and bookkeeping that failed (its chip turns red).
    static func speaks(_ call: ToolCall) -> Bool { !isQuiet(call.name) || call.isError }

    /// A shell tool's command as its detail shows it ("$ go test ./..."); a Monitor that listens on a WebSocket shows
    /// its address. nil for any other tool.
    static func commandLine(_ name: String, _ input: JSONValue) -> String? {
        switch tableName(name) {
        case "Bash": return "$ " + (input["command"]?.string ?? "")
        case "PowerShell": return "PS> " + (input["command"]?.string ?? "")
        case "Monitor": return input["command"]?.string.map { "$ " + $0 } ?? input["ws"]?["url"]?.string
        default: return nil
        }
    }

    /// The lines a notebook cell's new source has (NotebookEdit carries only the new one).
    static func lineCount(_ s: String) -> Int {
        guard !s.isEmpty else { return 0 }
        let breaks = s.reduce(0) { $1 == "\n" ? $0 + 1 : $0 }
        return s.hasSuffix("\n") ? breaks : breaks + 1
    }

    // MARK: Arguments

    /// One argument of a call as its detail lists it.
    struct Argument: Identifiable, Hashable, Sendable {
        /// The field as the tool names it.
        var id: String
        /// The field in words ("file_path" is "File path").
        var label: String
        var value: String
        /// Set in the code face: a path, an id, a number, JSON.
        var code: Bool
        /// How many lines it may take: 1, or a few for text with lines of its own.
        var lines: Int
    }

    /// How much of a long value a line shows: the rest is in Raw Input.
    private static let valueCap = 300

    /// A call's input as a short key/value list: the telling fields first (`subjectKeys`), then the rest by name; text on
    /// one line (a few for text with lines of its own), lists of words joined, anything nested as one line of JSON.
    static func arguments(_ input: JSONValue, skipping: Set<String> = []) -> [Argument] {
        guard let o = input.object else { return [] }
        let first = subjectKeys.filter { o[$0] != nil }
        let rest = o.keys.filter { !first.contains($0) }.sorted()
        return (first + rest).filter { !skipping.contains($0) }.compactMap { key in
            guard let value = o[key], let shown = argument(value) else { return nil }
            return Argument(id: key, label: humanize(key), value: shown.value, code: shown.code, lines: shown.lines)
        }
    }

    private static func argument(_ value: JSONValue) -> (value: String, code: Bool, lines: Int)? {
        switch value {
        case .string(let s):
            // Paragraphs keep one break between them: a blank line would spend one of the few lines a value gets, and a
            // cut there shows no "…". Raw Input keeps the text as sent.
            let t = s.split(whereSeparator: \.isNewline).filter { !$0.allSatisfy(\.isWhitespace) }.joined(separator: "\n")
                .trimmingCharacters(in: .whitespacesAndNewlines)
            guard !t.isEmpty else { return nil }
            let multiline = t.contains(where: \.isNewline)
            // A token (a path, an id, a pattern) in the code face; a sentence someone wrote in the text face.
            let token = !t.contains(where: \.isWhitespace) && t.unicodeScalars.allSatisfy(\.isASCII)
            return (String(t.prefix(valueCap)), token, multiline ? 3 : 1)
        case .number, .bool:
            return (value.string ?? "", true, 1)
        case .null:
            return ("null", true, 1)
        case .array(let a):
            guard !a.isEmpty else { return nil }
            if a.allSatisfy({ if case .object = $0 { false } else if case .array = $0 { false } else { true } }) {
                return (String(a.compactMap(\.string).joined(separator: ", ").prefix(valueCap)), true, 1)
            }
            return (compact(value), true, 1)
        case .object(let o):
            guard !o.isEmpty else { return nil }
            return (compact(value), true, 1)
        }
    }

    /// Nested JSON on one line, its start.
    private static func compact(_ value: JSONValue) -> String {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard let data = try? enc.encode(value) else { return "" }
        return String(String(decoding: data, as: UTF8.self).prefix(valueCap))
    }
}
