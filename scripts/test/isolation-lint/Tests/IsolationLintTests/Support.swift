import XCTest
import IsolationLintCore

/// An app target as apple/project.yml sets them up: MainActor by default, `nonisolated async` runs on the caller.
let app = TargetConfig(name: "App", defaultMain: true, nonsending: true)
/// A SwiftPM package or a target with no default isolation.
let package = TargetConfig(name: "Kit", defaultMain: false, nonsending: false)

/// Every sample declares the URL its calls read, so the samples are valid Swift on their own.
let prelude = "import Foundation\nnonisolated let url = URL(fileURLWithPath: \"/x\")\n"

extension XCTestCase {
    /// Runs the lint over `files` (path -> source), each folder in `targets` being one target, and returns the
    /// findings.
    func lint(_ files: [String: String], targets: [String: TargetConfig] = ["App": app]) -> [Finding] {
        var project = ProjectMap()
        for (folder, config) in targets.sorted(by: { $0.key < $1.key }) { project.addTarget(config, sources: [folder]) }
        let inputs = files.sorted { $0.key < $1.key }.map { Lint.Input(path: $0.key, source: $0.value) }
        return Lint(files: inputs, project: project).findings()
    }

    /// Checks one file compiled by one target. Each line that makes a blocking call ends with the verdict the lint
    /// must give it, as a comment: `// main`, `// unknown` or `// off`. A call with no marker, a marker with no call
    /// and a different verdict all fail.
    @discardableResult
    func expect(_ source: String, in config: TargetConfig = app, file: StaticString = #filePath, line: UInt = #line) -> [Finding] {
        expect(["App/Sample.swift": source], targets: ["App": config], file: file, line: line)
    }

    @discardableResult
    func expect(_ files: [String: String], targets: [String: TargetConfig], file: StaticString = #filePath, line: UInt = #line) -> [Finding] {
        let findings = lint(files.mapValues { prelude + $0 }, targets: targets)
        var wanted: [String] = [], got: [String] = []
        for (path, source) in files.sorted(by: { $0.key < $1.key }) {
            for (index, text) in source.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
                for verdict in ["main", "unknown", "off"] where text.hasSuffix("// " + verdict) {
                    wanted.append("\(path):\(index + 1) \(verdict)")
                }
            }
        }
        let shift = prelude.split(separator: "\n", omittingEmptySubsequences: false).count - 1
        for finding in findings { got.append("\(finding.file):\(finding.line - shift) \(finding.verdict.rawValue)") }
        let why = findings.map { "\($0.file):\($0.line - shift) \($0.verdict.rawValue): \($0.reason)" }.joined(separator: "\n")
        XCTAssertEqual(got.sorted(), wanted.sorted(), "\n" + why, file: file, line: line)
        return findings
    }

    /// The reason the lint gives for the only call in `source`.
    func reason(_ source: String, in config: TargetConfig = app, file: StaticString = #filePath, line: UInt = #line) -> String {
        let findings = lint(["App/Sample.swift": prelude + source], targets: ["App": config])
        XCTAssertEqual(findings.count, 1, "the sample makes one blocking call", file: file, line: line)
        return findings.first?.reason ?? ""
    }
}
