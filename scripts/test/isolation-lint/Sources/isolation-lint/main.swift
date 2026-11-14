import Foundation
import IsolationLintCore

// isolation-lint: blocking calls that run on the main actor (docs/apple/performance.md, "Automated checks").
//
//   isolation-lint --root <repo> [--scan apple] [--baseline <file>] [--update] [--list] [--why] [--json <file>]
//
// Reads every Swift file under <repo>/<scan> that ships (tests are left out), asks XcodeGen which target compiles
// which file and with which default isolation, and gives each Photos fetch, keychain read, file read, directory
// listing and image decode a verdict: `main`, `unknown` or `off`.
//
//   --baseline <file>  compare the calls that are not `off` with the committed counts: PASS when they are equal, FAIL
//                      when a file has more (the baseline is never raised) or fewer (run again with --update)
//   --update           rewrite the baseline; refused when any count would go up
//   --list [--why]     print every call with its verdict, and with --why the chain of reasons behind it
//   --json <file>      the same as JSON
//
// Use it through scripts/test/isolation-lint.sh, which builds it first. The last line it prints starts with
// `isolation-lint: PASS` or `isolation-lint: FAIL`.

struct Failure: Error, CustomStringConvertible { var description: String }

func run(_ tool: String, _ arguments: [String], environment: [String: String]) throws -> Data {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    process.arguments = [tool] + arguments
    process.environment = environment
    let out = Pipe()
    process.standardOutput = out
    process.standardError = FileHandle.standardError
    try process.run()
    let data = out.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    guard process.terminationStatus == 0 else { throw Failure(description: "\(tool) \(arguments.joined(separator: " ")) exited \(process.terminationStatus)") }
    return data
}

/// Test targets and tooling: they run on purpose where they run and ship nothing.
let skippedParts: Set<String> = ["PersonaTests", "PersonaUITests", "PersonaWatchTests", "Tests", "verify"]
let skippedFolders: Set<String> = [".build", "DerivedData", "build", "node_modules", ".swiftpm"]

func main() throws -> Int32 {
    var root = FileManager.default.currentDirectoryPath
    var scan = "apple"
    var baselinePath: String?
    var jsonPath: String?
    var update = false, list = false, why = false
    var arguments = CommandLine.arguments.dropFirst()
    while let argument = arguments.popFirst() {
        switch argument {
        case "--root": root = arguments.popFirst() ?? root
        case "--scan": scan = arguments.popFirst() ?? scan
        case "--baseline": baselinePath = arguments.popFirst()
        case "--json": jsonPath = arguments.popFirst()
        case "--update": update = true
        case "--list": list = true
        case "--why": why = true
        default: throw Failure(description: "unknown argument \(argument)")
        }
    }
    let rootURL = URL(fileURLWithPath: root).standardizedFileURL
    let scanURL = rootURL.appendingPathComponent(scan)
    let prefix = rootURL.path.hasSuffix("/") ? rootURL.path : rootURL.path + "/"

    var swift: [String] = [], specs: [String] = [], manifests: [String] = []
    guard let walk = FileManager.default.enumerator(at: scanURL, includingPropertiesForKeys: [.isDirectoryKey]) else {
        throw Failure(description: "cannot read \(scanURL.path)")
    }
    for case let url as URL in walk {
        let name = url.lastPathComponent
        if (try? url.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true {
            if skippedFolders.contains(name) { walk.skipDescendants() }
            continue
        }
        let relative = String(url.standardizedFileURL.path.dropFirst(prefix.count))
        let parts = relative.split(separator: "/").map(String.init)
        if name == "project.yml" { specs.append(relative) }
        if name == "Package.swift" { manifests.append(relative); continue }
        guard name.hasSuffix(".swift"), skippedParts.isDisjoint(with: parts), parts.count > 1, !parts[1].hasSuffix("Tests") else { continue }
        swift.append(relative)
    }
    swift.sort(); specs.sort(); manifests.sort()

    var project = ProjectMap()
    var environment = ProcessInfo.processInfo.environment
    // project.yml's optional include (apple/meta-wearables.yml) adds a package and Info.plist keys, no sources or
    // Swift settings; XcodeGen needs the variable to have a value.
    if environment["PERSONA_META_WEARABLES"] == nil { environment["PERSONA_META_WEARABLES"] = "NO" }
    for spec in specs {
        let json = try run("xcodegen", ["dump", "--spec", rootURL.appendingPathComponent(spec).path, "--type", "json"], environment: environment)
        try project.addXcodeGen(json: json, directory: (spec as NSString).deletingLastPathComponent)
    }
    for manifest in manifests {
        let text = try String(contentsOf: rootURL.appendingPathComponent(manifest), encoding: .utf8)
        project.addPackage(manifest: text, directory: (manifest as NSString).deletingLastPathComponent)
    }

    // Read in parallel: opening a thousand files one after another is most of a release run.
    var sources = [String?](repeating: nil, count: swift.count)
    let lock = NSLock()
    let paths = swift
    DispatchQueue.concurrentPerform(iterations: paths.count) { i in
        let text = try? String(contentsOf: rootURL.appendingPathComponent(paths[i]), encoding: .utf8)
        lock.lock()
        sources[i] = text
        lock.unlock()
    }
    var inputs: [Lint.Input] = []
    for (path, source) in zip(swift, sources) {
        // A file that cannot be read cannot be cleared: stop instead of leaving it out.
        guard let source else { throw Failure(description: "cannot read \(path) as UTF-8") }
        inputs.append(Lint.Input(path: path, source: source))
    }
    let findings = Lint(files: inputs, project: project).findings()

    if let jsonPath {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        try encoder.encode(findings).write(to: URL(fileURLWithPath: jsonPath))
    }
    if list {
        for finding in findings {
            print("\(finding.file):\(finding.line) \(finding.rule) \(finding.verdict.rawValue)" + (why ? "\n    \(finding.reason)" : ""))
        }
    }

    let open = findings.filter { $0.verdict != .off }
    let total = { (verdict: Verdict) in findings.filter { $0.verdict == verdict }.count }
    let summary = "\(findings.count) blocking calls in \(swift.count) files: \(total(.main)) on the main actor, \(total(.unknown)) unknown, \(total(.off)) off it"

    guard let baselinePath else {
        print("isolation-lint: \(summary)")
        return 0
    }
    // A missing or unreadable baseline is an error, never an empty one: the committed counts only go down.
    let baselineURL = URL(fileURLWithPath: baselinePath)
    let counts = Baseline.counts(findings)
    let (grown, stale) = Baseline.compare(counts, try Baseline.decode(Data(contentsOf: baselineURL)))
    if update {
        guard grown.isEmpty else {
            print("isolation-lint: FAIL refusing to raise the baseline (move the call off the main actor):")
            for line in grown { print("  " + line) }
            return 1
        }
        try Baseline.encode(counts).write(to: baselineURL)
        print("isolation-lint: PASS baseline written; \(summary)")
        return 0
    }
    if !grown.isEmpty {
        print("isolation-lint: FAIL a blocking call was added on the main actor, or where the lint cannot tell (docs/apple/performance.md, P1, P2, P8):")
        for line in grown { print("  " + line) }
        for finding in open where grown.contains(where: { $0.hasPrefix(finding.file + ": " + finding.rule + " ") }) {
            print("  \(finding.file):\(finding.line) \(finding.rule) \(finding.verdict.rawValue)\n      \(finding.reason)")
        }
        return 1
    }
    if !stale.isEmpty {
        print("isolation-lint: FAIL the baseline is out of date (fewer calls on the main actor, or a call moved between main and unknown); run with --update and commit it:")
        for line in stale { print("  " + line) }
        return 1
    }
    print("isolation-lint: PASS \(summary)")
    return 0
}

do {
    exit(try main())
} catch {
    FileHandle.standardError.write(Data("isolation-lint: FAIL \(error)\n".utf8))
    exit(2)
}
