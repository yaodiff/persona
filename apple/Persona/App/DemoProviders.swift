import Foundation
import PersonaWire
// i18n-ignore-file: demo mode stand-in for the hub: its made-up replies and errors, as the hub words them

/// Demo Mode's Settings › Model Providers (#2455): a provider kept in Persona, two from a made-up ~/.zshrc and a wrapper script on
/// a machine's PATH (#5954), and the wizard's and the
/// import's answers. Nothing reaches a gateway: Test Connection and Get Model List answer with made-up values, and no key is kept.
@MainActor
final class DemoProviders {
    private var managed: [String: JSONValue] = [:]
    private var order: [String] = []

    static let catalog: [JSONValue] = [
        ["id": "claude-opus-4-1", "name": "Claude Opus", "context_window": 200_000, "efforts": ["low", "medium", "high", "xhigh", "max"], "default_effort": "high"],
        ["id": "claude-sonnet-4-5", "name": "Claude Sonnet", "context_window": 1_000_000, "efforts": ["low", "medium", "high"], "default_effort": "medium"],
        ["id": "claude-haiku-4-5", "name": "Claude Haiku", "context_window": 200_000],
        ["id": "glm-5", "name": "GLM-5", "context_window": 200_000, "efforts": ["low", "high"]],
        ["id": "kimi-k2", "name": "Kimi K2", "context_window": 256_000]
    ]

    init() { reset() }

    func reset() {
        managed = ["demo_gateway": [
            "name": "demo_gateway", "source": "persona", "kind": "launcher", "api": "messages", "base_url": "https://gateway.example.com",
            "model": "glm-5", "has_key": true, "auth": "bearer", "has_headers": true,
            "models": [Self.catalog[3], Self.catalog[4]],
            "share": "all", "delivery": "on_demand", "own_service": "skip"
        ]]
        order = ["demo_gateway"]
    }

    private static let zshrc: [JSONValue] = [
        ["name": "cc_glm", "source": "zshrc", "base_url": "https://gateway.example.com", "model": "glm-5", "machines": ["macbook"]],
        ["name": "cc_kimi", "source": "zshrc", "base_url": "https://kimi.example.com", "model": "kimi-k2", "machines": ["macbook", "studio"]],
        ["name": "claude-work", "source": "path", "base_url": "https://work.example.com", "model": "glm-5", "machines": ["macbook"],
         "definitions": ["macbook": """
            # ~/.local/bin/claude-work
            #!/bin/sh
            export ANTHROPIC_BASE_URL="https://work.example.com"
            export ANTHROPIC_AUTH_TOKEN=•••
            unset HTTP_PROXY HTTPS_PROXY
            exec claude --settings "$HOME/.claude/work.json" "$@"
            """]]
    ]

    /// The machine pickers' 「默认：…」 (agent.new's effective value).
    static let launcherDefault: JSONValue = ["provider": "launcher.claude", "model": "opus"]

    /// GET under /api/v1/launchers; nil: not one of these.
    func get(_ p: [String], _ q: [String: String]) throws -> JSONValue? {
        guard p.first == "launchers" else { return nil }
        switch p.count {
        case 1: return ["launchers": .array(order.compactMap { managed[$0] } + Self.zshrc)]
        case 2 where p[1] == "presets": return ["presets": .array([])]
        case 2 where p[1] == "import": return try preview(q["machine"] ?? "")
        case 2:
            guard let l = managed[p[1]] ?? Self.zshrc.first(where: { $0["name"]?.string == p[1] }) else { return nil }
            return ["launcher": l]
        default: return nil
        }
    }

    /// POST / PUT / DELETE under /api/v1/launchers; nil: not one of these.
    func change(_ method: String, _ p: [String], _ body: JSONValue) throws -> JSONValue? {
        guard p.first == "launchers" else { return nil }
        switch (method, p.count) {
        case ("POST", 2) where p[1] == "test":
            let model = body["model"]?.string ?? ""
            return ["ok": true, "ms": 820, "model": .string(model)]
        case ("POST", 2) where p[1] == "discover":
            return ["models": .array(Self.catalog)]
        case ("POST", 2) where p[1] == "import":
            let names = body["providers"]?.array?.compactMap(\.string) ?? []
            var created: [JSONValue] = []
            for n in names where managed[n] == nil {
                managed[n] = ["name": .string(n), "source": "persona", "kind": "launcher", "base_url": "https://gateway.example.com",
                              "model": "glm-5", "has_key": true, "origin": .string("zshrc:\(n)@\(body["machine"]?.string ?? "")"),
                              "models": [Self.catalog[3]]]
                order.append(n)
                created.append(.string(n))
            }
            return ["created": .array(created), "updated": .array([])]
        case ("POST", 1), ("PUT", 2):
            guard case .object(var l) = body, let name = l["name"]?.string, !name.isEmpty else {
                throw APIError(status: 400, message: "缺少名字")
            }
            let old = managed[name]?.object ?? [:]
            // Like the hub: the key and the headers are kept, never sent back.
            let key = l.removeValue(forKey: "api_key")?.string
            let headers = l.removeValue(forKey: "headers")
            l["source"] = "persona"
            l["has_key"] = .bool(key?.isEmpty == false || old["has_key"]?.bool == true)
            Self.scope(&l, old: old, isNew: method == "POST")
            switch headers {
            case .some(.string(let h)): l["has_headers"] = .bool(!h.isEmpty)
            default: l["has_headers"] = old["has_headers"] ?? false
            }
            if managed[name] == nil { order.append(name) }
            managed[name] = .object(l)
            return ["ok": true]
        case ("DELETE", 2):
            managed[p[1]] = nil
            order.removeAll { $0 == p[1] }
            return ["ok": true]
        default: return nil
        }
    }

    /// Like the hub's device scope (#2333): a new provider gets all + on demand + skip (use for a direct API or with
    /// `assign`), a change keeps what it leaves out, and per-computer keys come back only as `key_machines`.
    private static func scope(_ l: inout [String: JSONValue], old: [String: JSONValue], isNew: Bool) {
        let defaults: [String: JSONValue] = isNew
            ? ["share": "all", "delivery": "on_demand",
               "own_service": .string(l["kind"]?.string == "api" || l["assign"] != nil ? "use" : "skip")]
            : old.filter { ["share", "scope_machines", "delivery", "own_service"].contains($0.key) }
        for (k, v) in defaults where l[k] == nil { l[k] = v }
        if l["share"]?.string == "none" || (l["scope_machines"]?.array ?? []).isEmpty { l["scope_machines"] = nil }
        var keyed = old["key_machines"]?.array?.compactMap(\.string) ?? []
        for (id, key) in l.removeValue(forKey: "machine_keys")?.object ?? [:] {
            keyed.removeAll { $0 == id }
            if key.string?.isEmpty == false { keyed.append(id) }
        }
        l["key_machines"] = keyed.isEmpty ? nil : .array(keyed.sorted().map { .string($0) })
        l["assign"] = nil
    }

    private func preview(_ machine: String) throws -> JSONValue {
        // The demo's Mac mini stands for a computer whose personad is too old to read ~/.zshrc.
        if machine == "mini" { throw APIError(status: 409, message: "这台机器上的 personad 太旧，不能从 ~/.zshrc 导入，需要重新部署", fields: ["code": "node_outdated"]) }
        return ["machine": .string(machine), "providers": [
            ["name": "cc_glm", "base_url": "https://gateway.example.com", "auth": "bearer", "has_key": true,
             "models": [Self.catalog[3], ["id": "glm-5-air", "context_window": 128_000]], "model": "glm-5", "aliases": ["cc_glm", "cc_glm_air"],
             "existing": "demo_gateway"],
            ["name": "cc_kimi", "base_url": "https://kimi.example.com", "auth": "api_key", "has_key": true,
             "models": [Self.catalog[4]], "model": "kimi-k2", "aliases": ["cc_kimi"]]
        ]]
    }
}
