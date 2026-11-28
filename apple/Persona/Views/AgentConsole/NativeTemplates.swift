import Foundation

/// The shared Persona template format on iOS (#17129, schema/template.schema.json). Device looks stay on this device;
/// this first native reader accepts the complete format but previews hub/navigation parts as unsupported rather than
/// claiming it changed them. The console's external conversations live inside Agents, not in a fourth tab.
nonisolated struct PersonaTemplate: Equatable, Sendable {
    struct Browser: Equatable, Sendable {
        var phoneNav: String?
        var mainChat: String?
        var agentsView: String?
        var settingsView: String?
        var sidebarView: String?
    }

    var id: String
    var name: [String: String]
    var description: [String: String]
    var browser: Browser?
    var hasNavigation = false
    var hasExtensions = false
    var hasSettings = false

    var isDefault: Bool { id == "default" }

    static let `default` = PersonaTemplate(
        id: "default",
        name: ["en": "Default", "zh-Hans": "默认", "ja": "デフォルト"],
        description: [
            "en": "Persona as it comes. Restores every native Agent console choice.",
            "zh-Hans": "Persona 原本的样子，恢复所有原生 Agent 控制台选项。",
            "ja": "Persona の標準の状態に戻し、Agent コンソールの選択をすべて解除します。",
        ],
        browser: Browser(phoneNav: "switch", mainChat: "full", agentsView: "full", settingsView: "full", sidebarView: "full")
    )

    func localized(_ values: [String: String], language: String = Locale.preferredLanguages.first ?? "en") -> String {
        let lang = language.hasPrefix("zh") ? "zh-Hans" : language.hasPrefix("ja") ? "ja" : "en"
        return values[lang] ?? values["en"] ?? values.values.first ?? ""
    }

    var title: String { localized(name) }
    var detail: String { localized(description) }

    enum ParseError: Error, Equatable, LocalizedError {
        case empty, invalidJSON, invalidBase64, noTemplateParameter, notObject, version, id, name, unknownPart(String)
        case browser(String), tooLarge

        var errorDescription: String? {
            switch self {
            case .empty: String(localized: "Paste a template link or its JSON.")
            case .invalidJSON: String(localized: "The template is not valid JSON.")
            case .invalidBase64, .noTemplateParameter: String(localized: "The link does not contain a readable template.")
            case .notObject: String(localized: "A template must be a JSON object.")
            case .version: String(localized: "This template version is not supported.")
            case .id: String(localized: "The template id must use lowercase letters, digits and dashes.")
            case .name: String(localized: "The template needs a name.")
            case .unknownPart(let part): String(format: String(localized: "Unknown template part: %@"), part)
            case .browser(let part): String(format: String(localized: "Invalid browser setting: %@"), part)
            case .tooLarge: String(localized: "The template is too large.")
            }
        }
    }

    static func parse(_ input: String) throws -> PersonaTemplate {
        let trimmed = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { throw ParseError.empty }
        guard trimmed.utf8.count <= 200_000 else { throw ParseError.tooLarge }
        if trimmed == "default" { return .default }
        let object: [String: Any]
        if trimmed.hasPrefix("{") {
            object = try decodeObject(Data(trimmed.utf8))
        } else if let components = URLComponents(string: trimmed), components.scheme != nil {
            guard let encoded = components.queryItems?.first(where: { $0.name == "template" })?.value else {
                throw ParseError.noTemplateParameter
            }
            return try parse(encoded)
        } else {
            var base64 = trimmed.replacingOccurrences(of: "-", with: "+").replacingOccurrences(of: "_", with: "/")
            base64 += String(repeating: "=", count: (4 - base64.count % 4) % 4)
            guard let data = Data(base64Encoded: base64) else { throw ParseError.invalidBase64 }
            object = try decodeObject(data)
        }
        return try parse(object)
    }

    private static func decodeObject(_ data: Data) throws -> [String: Any] {
        let raw: Any
        do { raw = try JSONSerialization.jsonObject(with: data) } catch { throw ParseError.invalidJSON }
        guard let object = raw as? [String: Any] else { throw ParseError.notObject }
        return object
    }

    private static func parse(_ raw: [String: Any]) throws -> PersonaTemplate {
        guard raw["persona_template"] as? Int == 1 else { throw ParseError.version }
        guard let id = raw["id"] as? String,
              id.range(of: #"^[a-z0-9][a-z0-9-]{0,63}$"#, options: .regularExpression) != nil else { throw ParseError.id }
        guard let name = try localized(raw["name"]), !name.isEmpty else { throw ParseError.name }
        let known: Set<String> = ["persona_template", "id", "name", "description", "browser", "navigation", "extensions", "settings"]
        if let unknown = raw.keys.first(where: { !known.contains($0) }) { throw ParseError.unknownPart(unknown) }
        var browser: Browser?
        if let value = raw["browser"] {
            guard let object = value as? [String: Any] else { throw ParseError.browser("browser") }
            let allowed: Set<String> = ["phone_nav", "sidebar_icons", "modules_off", "main_chat", "agents_view", "settings_view", "sidebar_view"]
            if let unknown = object.keys.first(where: { !allowed.contains($0) }) { throw ParseError.unknownPart("browser.\(unknown)") }
            func mode(_ key: String, _ values: Set<String>) throws -> String? {
                guard let value = object[key] else { return nil }
                guard let value = value as? String, values.contains(value) else { throw ParseError.browser(key) }
                return value
            }
            browser = Browser(
                phoneNav: try mode("phone_nav", ["switch", "tabs"]),
                mainChat: try mode("main_chat", ["full", "plain"]),
                agentsView: try mode("agents_view", ["full", "active"]),
                settingsView: try mode("settings_view", ["full", "short"]),
                sidebarView: try mode("sidebar_view", ["full", "agents"])
            )
            if object["modules_off"] != nil || object["sidebar_icons"] != nil { /* accepted, not applicable natively */ }
        }
        return PersonaTemplate(
            id: id, name: name, description: try localized(raw["description"]) ?? [:], browser: browser,
            hasNavigation: raw["navigation"] != nil, hasExtensions: raw["extensions"] != nil, hasSettings: raw["settings"] != nil
        )
    }

    private static func localized(_ raw: Any?) throws -> [String: String]? {
        guard let raw else { return nil }
        if let text = raw as? String, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return ["en": text, "zh-Hans": text, "ja": text]
        }
        guard let object = raw as? [String: Any] else { throw ParseError.name }
        var out: [String: String] = [:]
        for (key, value) in object {
            guard ["en", "zh-Hans", "ja"].contains(key), let text = value as? String,
                  !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { throw ParseError.name }
            out[key] = text
        }
        return out.isEmpty ? nil : out
    }
}

/// One line of the native template preview. Unsupported items are explicit; Apply never reports them as changed.
nonisolated struct NativeTemplateItem: Identifiable, Equatable, Sendable {
    enum Status: Equatable, Sendable { case change, unchanged, unsupported }
    var id: String
    var title: String
    var before: String
    var after: String
    var status: Status
    var note: String?
}

@MainActor @Observable final class NativeTemplateStore {
    static let shared = NativeTemplateStore()
    private static let enabledKey = "persona.ios.console.enabled"
    private static let nameKey = "persona.ios.console.name"
    private let defaults: UserDefaults

    var enabled: Bool { didSet { defaults.set(enabled, forKey: Self.enabledKey) } }
    var templateName: String { didSet { defaults.set(templateName, forKey: Self.nameKey) } }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        enabled = defaults.bool(forKey: Self.enabledKey)
        templateName = defaults.string(forKey: Self.nameKey) ?? ""
    }

    var agentsConsole: Bool { enabled }
    var plainMainChat: Bool { enabled }
    var shortSettings: Bool { enabled }

    func plan(_ template: PersonaTemplate) -> [NativeTemplateItem] {
        if template.isDefault {
            return [item("navigation", String(localized: "Agent console"), enabled ? String(localized: "On") : String(localized: "Off"),
                         String(localized: "Off"), !enabled)]
        }
        let b = template.browser
        var out: [NativeTemplateItem] = []
        // One native shell choice covers the four web looks. External is a segment inside Agents, not its own tab.
        if b?.phoneNav != nil || b?.mainChat != nil || b?.agentsView != nil || b?.settingsView != nil || b?.sidebarView != nil {
            out.append(item("console", String(localized: "Agent console"), enabled ? String(localized: "On") : String(localized: "Off"),
                            String(localized: "On"), !enabled,
                            String(localized: "Main chat, Agents and Settings. Running and external conversations are together in Agents.")))
        }
        if b?.mainChat == "plain" {
            out.append(item("chat", String(localized: "Main chat"), String(localized: "Everything"), String(localized: "Questions, replies and work needing you"), false))
        }
        if b?.agentsView == "active" {
            out.append(item("agents", String(localized: "Agents"), String(localized: "Everything"), String(localized: "Running agents by computer"), false))
        }
        if b?.settingsView == "short" {
            out.append(item("settings", String(localized: "Settings"), String(localized: "Every category"), String(localized: "Common categories and More settings"), false))
        }
        if template.hasNavigation {
            out.append(.init(id: "navigation-part", title: String(localized: "Hub navigation"), before: "", after: "",
                             status: .unsupported, note: String(localized: "iPhone does not apply the hub sidebar arrangement. The three native tabs are applied instead.")))
        }
        if template.hasExtensions {
            out.append(.init(id: "extensions", title: String(localized: "Extensions"), before: "", after: "", status: .unsupported,
                             note: String(localized: "Apply extension changes from web or Settings › Extensions on this iPhone.")))
        }
        if template.hasSettings {
            out.append(.init(id: "server-settings", title: String(localized: "Assistant settings"), before: "", after: "", status: .unsupported,
                             note: String(localized: "Server settings are not changed by this iPhone preview.")))
        }
        return out
    }

    @discardableResult func apply(_ template: PersonaTemplate) -> Bool {
        if template.isDefault {
            enabled = false
            templateName = ""
            return true
        }
        let b = template.browser
        guard b?.mainChat == "plain", b?.agentsView == "active", b?.settingsView == "short" else { return false }
        enabled = true
        templateName = template.title
        return true
    }

    private func item(_ id: String, _ title: String, _ before: String, _ after: String, _ unchanged: Bool, _ note: String? = nil) -> NativeTemplateItem {
        .init(id: id, title: title, before: before, after: after, status: unchanged ? .unchanged : .change, note: note)
    }
}

nonisolated enum NativeTemplateLink {
    /// A shared web Settings link (`…/settings?tab=general&template=…`) or `persona://template?template=…`.
    static func parameter(_ url: URL) -> String? {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let value = components.queryItems?.first(where: { $0.name == "template" })?.value, !value.isEmpty else { return nil }
        if url.scheme == "persona" { return value }
        return components.path.hasSuffix("/settings") ? value : nil
    }
}
