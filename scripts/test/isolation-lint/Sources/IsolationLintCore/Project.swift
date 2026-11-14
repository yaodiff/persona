import Foundation

/// How one target compiles a file: what an unannotated declaration is isolated to, and where a `nonisolated async`
/// function runs.
public struct TargetConfig: Hashable, Sendable {
    /// The target or package, for messages.
    public var name: String
    /// `SWIFT_DEFAULT_ACTOR_ISOLATION: MainActor` (`-default-isolation MainActor`, SE-0466).
    public var defaultMain: Bool
    /// `NonisolatedNonsendingByDefault` (SE-0461, part of `SWIFT_APPROACHABLE_CONCURRENCY`): a `nonisolated async`
    /// function runs on its caller's actor instead of the global executor.
    public var nonsending: Bool

    public init(name: String, defaultMain: Bool, nonsending: Bool) {
        self.name = name
        self.defaultMain = defaultMain
        self.nonsending = nonsending
    }
}

public struct ProjectError: Error, CustomStringConvertible {
    public var description: String
    init(_ text: String) { description = text }
}

/// Which targets compile which files. Built from `xcodegen dump --type json` of each project spec and from the SwiftPM
/// manifests under the scanned folder; nothing is guessed from a file's path.
public struct ProjectMap: Sendable {
    struct Source: Sendable {
        var path: String          // repo-relative file or folder
        var excludes: [String]    // globs relative to `path`
    }
    struct Target: Sendable {
        var config: TargetConfig
        var sources: [Source]
    }
    var targets: [Target] = []

    public init() {}

    /// One target over the given files or folders (repo-relative); for tests and small trees.
    public mutating func addTarget(_ config: TargetConfig, sources: [String]) {
        targets.append(Target(config: config, sources: sources.map { Source(path: $0, excludes: []) }))
    }

    /// The targets that compile `file` (repo-relative), each once.
    public func configs(for file: String) -> [TargetConfig] {
        var seen = Set<TargetConfig>(), out: [TargetConfig] = []
        for target in targets where target.sources.contains(where: { Self.covers($0, file) }) {
            if seen.insert(target.config).inserted { out.append(target.config) }
        }
        return out
    }

    static func covers(_ source: Source, _ file: String) -> Bool {
        if source.path == file { return true }
        let prefix = source.path.hasSuffix("/") ? source.path : source.path + "/"
        guard file.hasPrefix(prefix) else { return false }
        let rest = String(file.dropFirst(prefix.count))
        return !source.excludes.contains { excluded($0, rest) }
    }

    /// XcodeGen's `excludes`: a glob relative to the source folder; a folder's match takes what is under it.
    static func excluded(_ glob: String, _ path: String) -> Bool {
        let parts = path.split(separator: "/").map(String.init)
        for end in 1...max(parts.count, 1) where end <= parts.count {
            if globMatches(glob, parts[0..<end].joined(separator: "/")) { return true }
        }
        return false
    }

    static func globMatches(_ glob: String, _ path: String) -> Bool {
        var pattern = "^"
        var i = glob.startIndex
        while i < glob.endIndex {
            let c = glob[i]
            if glob[i...].hasPrefix("**/") {
                pattern += "(?:.*/)?"
                i = glob.index(i, offsetBy: 3)
                continue
            }
            if glob[i...].hasPrefix("**") {
                pattern += ".*"
                i = glob.index(i, offsetBy: 2)
                continue
            }
            switch c {
            case "*": pattern += "[^/]*"
            case "?": pattern += "[^/]"
            default: pattern += NSRegularExpression.escapedPattern(for: String(c))
            }
            i = glob.index(after: i)
        }
        pattern += "$"
        return path.range(of: pattern, options: .regularExpression) != nil
    }

    // MARK: XcodeGen

    /// Adds the targets of one XcodeGen spec. `json` is `xcodegen dump --type json`; `directory` is the spec's folder,
    /// repo-relative (source paths in the spec are relative to it). Test bundles are left out: they ship nothing.
    public mutating func addXcodeGen(json: Data, directory: String) throws {
        guard let spec = try JSONSerialization.jsonObject(with: json) as? [String: Any] else {
            throw ProjectError("\(directory): the project dump is not a JSON object")
        }
        let project = spec["settings"] as? [String: Any] ?? [:]
        guard let targets = spec["targets"] as? [String: Any] else {
            throw ProjectError("\(directory): the project dump has no targets")
        }
        for (name, raw) in targets.sorted(by: { $0.key < $1.key }) {
            guard let target = raw as? [String: Any] else { continue }
            if let type = target["type"] as? String, type.hasPrefix("bundle.") { continue }
            let settings = target["settings"] as? [String: Any] ?? [:]
            /// The setting's value for every configuration: the target's, else the project's. A setting that differs
            /// between Debug and Release would make one file's isolation depend on the build, which the lint refuses.
            func setting(_ key: String) throws -> String? {
                for scope in [settings, project] {
                    if let configs = scope["configs"] as? [String: Any],
                       configs.values.contains(where: { ($0 as? [String: Any])?[key] != nil }) {
                        throw ProjectError("\(directory) target \(name): \(key) is set per configuration, which this lint does not understand")
                    }
                    if let base = scope["base"] as? [String: Any], let value = base[key] { return Self.text(value) }
                    if let value = scope[key], !(value is [String: Any]) { return Self.text(value) }
                }
                return nil
            }
            /// Every value the setting has anywhere (base and each configuration, target and project): compiler flags
            /// add up, so a feature turned on in one configuration counts.
            func everyValue(_ key: String) -> String {
                var out: [String] = []
                for scope in [settings, project] {
                    if let base = scope["base"] as? [String: Any], let value = base[key] { out.append(Self.text(value)) }
                    if let value = scope[key], !(value is [String: Any]) { out.append(Self.text(value)) }
                    for config in (scope["configs"] as? [String: Any] ?? [:]).values {
                        if let value = (config as? [String: Any])?[key] { out.append(Self.text(value)) }
                    }
                }
                return out.joined(separator: " ")
            }
            let flags = everyValue("OTHER_SWIFT_FLAGS")
            let isolation = try setting("SWIFT_DEFAULT_ACTOR_ISOLATION")
            // Xcode's Swift.xcspec: SWIFT_UPCOMING_FEATURE_NONISOLATED_NONSENDING_BY_DEFAULT defaults to
            // $(SWIFT_APPROACHABLE_CONCURRENCY) and passes `-enable-upcoming-feature NonisolatedNonsendingByDefault`.
            let approachable = Self.truthy(try setting("SWIFT_APPROACHABLE_CONCURRENCY"))
            let upcoming = try setting("SWIFT_UPCOMING_FEATURE_NONISOLATED_NONSENDING_BY_DEFAULT")
            let config = TargetConfig(
                name: name,
                defaultMain: isolation == "MainActor" || flags.contains("-default-isolation MainActor")
                    || flags.contains("-default-isolation=MainActor"),
                nonsending: upcoming.map { Self.truthy($0) } ?? approachable || flags.contains("NonisolatedNonsendingByDefault"))
            var sources: [Source] = []
            for entry in target["sources"] as? [Any] ?? [] {
                if let path = entry as? String {
                    sources.append(Source(path: Self.join(directory, path), excludes: []))
                } else if let entry = entry as? [String: Any], let path = entry["path"] as? String {
                    sources.append(Source(path: Self.join(directory, path), excludes: entry["excludes"] as? [String] ?? []))
                }
            }
            self.targets.append(Target(config: config, sources: sources))
        }
    }

    private static func text(_ value: Any) -> String {
        if let flag = value as? Bool { return flag ? "YES" : "NO" }
        if let list = value as? [Any] { return list.map { "\($0)" }.joined(separator: " ") }
        return "\(value)"
    }

    private static func truthy(_ value: String?) -> Bool {
        guard let value else { return false }
        return ["YES", "true", "1"].contains(value)
    }

    private static func join(_ directory: String, _ path: String) -> String {
        var parts = directory.split(separator: "/").map(String.init)
        for part in path.split(separator: "/") {
            if part == "." { continue }
            if part == "..", !parts.isEmpty { parts.removeLast(); continue }
            parts.append(String(part))
        }
        return parts.joined(separator: "/")
    }

    // MARK: SwiftPM

    /// Adds a SwiftPM package: everything under `directory` (repo-relative) but its tests. The manifest's text decides:
    /// a package has no default isolation unless a target says `.defaultIsolation(MainActor.self)`, and no
    /// `NonisolatedNonsendingByDefault` unless the manifest names it. One setting anywhere counts for the whole
    /// package, which errs towards the main actor.
    public mutating func addPackage(manifest: String, directory: String) {
        let squeezed = manifest.filter { !$0.isWhitespace }
        let config = TargetConfig(
            name: (directory as NSString).lastPathComponent,
            defaultMain: squeezed.contains("defaultIsolation(MainActor.self)"),
            nonsending: squeezed.contains("NonisolatedNonsendingByDefault"))
        targets.append(Target(config: config, sources: [Source(path: directory, excludes: ["Tests", "Tests/**"])]))
    }
}
