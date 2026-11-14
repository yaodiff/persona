import XCTest
import IsolationLintCore

/// What counts as a blocking call: the five patterns of scripts/test/test_apple_perf_patterns.py that depend on the
/// thread they run on, with that script's `test_what_counts` lines carried over.
final class RuleTests: XCTestCase {
    /// (line, rule) of each call found in a main-actor function.
    func rules(_ body: String, file: StaticString = #filePath, line: UInt = #line) -> [String] {
        let source = "func sample() throws {\n" + body + "\n}\n"
        return lint(["App/Sample.swift": source]).map { "\($0.line - 1) \($0.rule)" }
    }

    func testWhatCounts() {
        XCTAssertEqual(rules("let r = PHAsset.fetchAssets(with: o)"), ["1 photos-fetch"])
        XCTAssertEqual(rules("let c = PHAssetCollection.fetchAssetCollections(with: .album, subtype: .any, options: nil)"), ["1 photos-fetch"])
        XCTAssertEqual(rules("let l = PHCollectionList.fetchTopLevelUserCollections(with: nil)"), ["1 photos-fetch"])
        XCTAssertEqual(rules("x()\nlet d = try Data(contentsOf: url)"), ["2 file-read"])
        XCTAssertEqual(rules("let s = try String(contentsOfFile: p, encoding: .utf8)"), ["1 file-read"])
        XCTAssertEqual(rules("let s = try String(contentsOf: url, encoding: .utf8)"), ["1 file-read"])
        XCTAssertEqual(rules("let d = NSData(contentsOfFile: p)\nlet s = try NSString(contentsOfFile: p, encoding: 4)"), ["1 file-read", "2 file-read"])
        XCTAssertEqual(rules("let i = UIImage(data: d)\nlet j = UIImage(contentsOfFile: p)"), ["1 image-decode", "2 image-decode"])
        XCTAssertEqual(rules("let i = NSImage(data: d)"), ["1 image-decode"])
        XCTAssertEqual(rules("try fm.contentsOfDirectory(at: u, includingPropertiesForKeys: nil)"), ["1 directory-listing"])
        XCTAssertEqual(rules("try FileManager.default.contentsOfDirectory(atPath: p)"), ["1 directory-listing"])
        XCTAssertEqual(rules("try fm.subpathsOfDirectory(atPath: p)"), ["1 directory-listing"])
        XCTAssertEqual(rules("let e = fm.enumerator(at: u, includingPropertiesForKeys: nil)\nlet f = fm.enumerator(atPath: p)"),
                       ["1 directory-listing", "2 directory-listing"])
        XCTAssertEqual(rules("SecItemCopyMatching(q as CFDictionary, &out)"), ["1 keychain-read"])
    }

    func testWhatDoesNotCount() {
        XCTAssertEqual(rules("// PHAsset.fetchAssets(with: o)\n/* SecItemCopyMatching(q, &o) */"), [])
        XCTAssertEqual(rules("let i = UIImage(named: \"a\")"), [])
        XCTAssertEqual(rules("let i = UIImage(systemName: \"a\")\nlet d = Data(count: 4)\nlet s = String(describing: x)"), [])
        XCTAssertEqual(rules("let text = \"Data(contentsOf: url)\""), [])
        XCTAssertEqual(rules("let e = items.enumerated()"), [])
    }

    func testFormsTheRegexCouldNotSee() {
        XCTAssertEqual(rules("let d = try Data.init(contentsOf: url)"), ["1 file-read"])
        XCTAssertEqual(rules("let d = try Foundation.Data(contentsOf: url)"), ["1 file-read"])
        XCTAssertEqual(rules("let d = try Data(\n    contentsOf: url)"), ["1 file-read"])
        XCTAssertEqual(rules("let i = datas.compactMap(UIImage.init(data:))"), ["1 image-decode"])
        XCTAssertEqual(rules("let d = urls.compactMap { try? Data.init(contentsOf: $0) }"), ["1 file-read"])
    }

    func testACommentNoLongerClearsACall() {
        // The ratchet took `// perf-ok: <reason>` on trust. The lint reads where the call runs and nothing else.
        let findings = lint(["App/Sample.swift": """
        func sample() {
            _ = PHAsset.fetchAssets(with: nil) // perf-ok: nonisolated loader
        }
        """])
        XCTAssertEqual(findings.map(\.verdict), [.main])
    }

    func testFindingsCarryTheirPlace() {
        let findings = lint(["App/Sample.swift": "func sample() {\n    _ = UIImage(data: d)\n}\n"])
        XCTAssertEqual(findings.count, 1)
        XCTAssertEqual(findings.first?.file, "App/Sample.swift")
        XCTAssertEqual(findings.first?.line, 2)
        XCTAssertEqual(findings.first?.column, 9)
        XCTAssertEqual(findings.first?.rule, "image-decode")
    }

    func testTwoRunsGiveTheSameAnswer() {
        let files = [
            "App/A.swift": prelude + "nonisolated func a() { _ = try? Data(contentsOf: url); b() }\nfunc screen() { a() }\n",
            "App/B.swift": "nonisolated func b() { _ = UIImage(data: d); a() }\nfunc other() { Task.detached { b() } }\n",
        ]
        XCTAssertEqual(lint(files), lint(files))
    }
}

/// Which target compiles which file, and with which default isolation: read from the project, never from the path.
final class ProjectTests: XCTestCase {
    /// The shape `xcodegen dump --type json` prints.
    let spec = """
    {
      "name": "Sample",
      "settings": {
        "base": {"SWIFT_DEFAULT_ACTOR_ISOLATION": "MainActor", "SWIFT_APPROACHABLE_CONCURRENCY": true, "SWIFT_VERSION": "6.0"},
        "configs": {"Release": {"SWIFT_COMPILATION_MODE": "wholemodule"}}
      },
      "targets": {
        "App": {
          "type": "application",
          "sources": [{"path": "App", "excludes": ["**/*.md", "Generated", "Legacy/Old*.swift"]}, {"path": "Shared"}]
        },
        "Notify": {
          "type": "app-extension",
          "settings": {"base": {"SWIFT_DEFAULT_ACTOR_ISOLATION": "nonisolated"}},
          "sources": [{"path": "Notify"}, {"path": "Shared/Push.swift"}, "../Common"]
        },
        "AppTests": {
          "type": "bundle.unit-test",
          "sources": [{"path": "AppTests"}]
        }
      }
    }
    """

    func project() throws -> ProjectMap {
        var project = ProjectMap()
        try project.addXcodeGen(json: Data(spec.utf8), directory: "apple")
        return project
    }

    func testTargetsTakeTheProjectsSettingUnlessTheySetTheirOwn() throws {
        let project = try project()
        XCTAssertEqual(project.configs(for: "apple/App/Screen.swift"), [TargetConfig(name: "App", defaultMain: true, nonsending: true)])
        XCTAssertEqual(project.configs(for: "apple/Notify/Service.swift"), [TargetConfig(name: "Notify", defaultMain: false, nonsending: true)])
    }

    func testAFileSharedByTwoTargetsHasBoth() throws {
        let project = try project()
        XCTAssertEqual(project.configs(for: "apple/Shared/Push.swift").map(\.name), ["App", "Notify"])
        XCTAssertEqual(project.configs(for: "apple/Shared/Other.swift").map(\.name), ["App"])
    }

    func testExcludesSourcePathsOutsideTheSpecFolderAndTestBundles() throws {
        let project = try project()
        XCTAssertEqual(project.configs(for: "apple/App/Generated/Wire.swift"), [])
        XCTAssertEqual(project.configs(for: "apple/App/Legacy/OldScreen.swift"), [])
        XCTAssertEqual(project.configs(for: "apple/App/Legacy/NewScreen.swift").map(\.name), ["App"])
        XCTAssertEqual(project.configs(for: "Common/Util.swift").map(\.name), ["Notify"])
        XCTAssertEqual(project.configs(for: "apple/AppTests/ScreenTests.swift"), [])
        XCTAssertEqual(project.configs(for: "apple/Elsewhere/File.swift"), [])
    }

    func testTheSameSourceIsOnTheMainActorInOneTargetAndNotInTheOther() throws {
        let source = prelude + "func load() { _ = try? Data(contentsOf: url) }\nfunc start() { Task.detached { load() } }\n"
        let project = try project()
        let inApp = Lint(files: [.init(path: "apple/App/Load.swift", source: source)], project: project).findings()
        let inExtension = Lint(files: [.init(path: "apple/Notify/Load.swift", source: source)], project: project).findings()
        XCTAssertEqual(inApp.map(\.verdict), [.main])
        XCTAssertEqual(inExtension.map(\.verdict), [.off])
    }

    func testCompilerFlagsCount() throws {
        let flags = """
        {"targets": {"Tool": {"type": "tool", "sources": ["Tool"],
          "settings": {"base": {"OTHER_SWIFT_FLAGS": "-default-isolation MainActor -enable-upcoming-feature NonisolatedNonsendingByDefault"}}}}}
        """
        var project = ProjectMap()
        try project.addXcodeGen(json: Data(flags.utf8), directory: "")
        XCTAssertEqual(project.configs(for: "Tool/main.swift"), [TargetConfig(name: "Tool", defaultMain: true, nonsending: true)])
    }

    func testATargetWithNoSettingHasNoDefaultIsolation() throws {
        var project = ProjectMap()
        try project.addXcodeGen(json: Data(#"{"targets": {"Tool": {"type": "tool", "sources": ["Tool"]}}}"#.utf8), directory: "")
        XCTAssertEqual(project.configs(for: "Tool/main.swift"), [TargetConfig(name: "Tool", defaultMain: false, nonsending: false)])
    }

    func testIsolationSetPerConfigurationIsRefused() {
        // One file would be on the main actor in Debug and off it in Release; the lint does not guess.
        let perConfiguration = """
        {"targets": {"App": {"type": "application", "sources": ["App"],
          "settings": {"configs": {"Debug": {"SWIFT_DEFAULT_ACTOR_ISOLATION": "MainActor"}}}}}}
        """
        var project = ProjectMap()
        XCTAssertThrowsError(try project.addXcodeGen(json: Data(perConfiguration.utf8), directory: "apple")) { error in
            XCTAssertTrue("\(error)".contains("SWIFT_DEFAULT_ACTOR_ISOLATION is set per configuration"), "\(error)")
        }
        XCTAssertThrowsError(try project.addXcodeGen(json: Data("[]".utf8), directory: "apple"))
        XCTAssertThrowsError(try project.addXcodeGen(json: Data("{}".utf8), directory: "apple"))
    }

    func testAPackageHasNoDefaultIsolationUnlessItsManifestSaysSo() {
        var project = ProjectMap()
        project.addPackage(manifest: """
        // swift-tools-version: 6.2
        import PackageDescription
        let package = Package(name: "Kit", targets: [.target(name: "Kit"), .testTarget(name: "KitTests", dependencies: ["Kit"])])
        """, directory: "apple/Packages/Kit")
        project.addPackage(manifest: """
        // swift-tools-version: 6.2
        import PackageDescription
        let package = Package(name: "UI", targets: [.target(name: "UI", swiftSettings: [
            .defaultIsolation(MainActor.self), .enableUpcomingFeature("NonisolatedNonsendingByDefault"),
        ])])
        """, directory: "apple/Packages/UI")
        XCTAssertEqual(project.configs(for: "apple/Packages/Kit/Sources/Kit/Store.swift"), [TargetConfig(name: "Kit", defaultMain: false, nonsending: false)])
        XCTAssertEqual(project.configs(for: "apple/Packages/Kit/Tests/KitTests/StoreTests.swift"), [])
        XCTAssertEqual(project.configs(for: "apple/Packages/UI/Sources/UI/Card.swift"), [TargetConfig(name: "UI", defaultMain: true, nonsending: true)])
    }
}

/// The gate: counts per file and rule that may go down and never up.
final class BaselineTests: XCTestCase {
    func finding(_ file: String, _ rule: String, _ verdict: Verdict) -> Finding {
        Finding(file: file, line: 1, column: 1, rule: rule, verdict: verdict, reason: "")
    }

    func testCallsOffTheMainActorAreNotCounted() {
        let counts = Baseline.counts([
            finding("a.swift", "file-read", .main), finding("a.swift", "file-read", .main), finding("a.swift", "file-read", .unknown),
            finding("a.swift", "image-decode", .off), finding("b.swift", "file-read", .off),
        ])
        XCTAssertEqual(counts, ["a.swift": ["file-read": ["main": 2, "unknown": 1]]])
    }

    func testCompare() {
        func compare(_ now: Baseline.Counts, _ was: Baseline.Counts) -> [[String]] {
            let result = Baseline.compare(now, was)
            return [result.grown, result.stale]
        }
        XCTAssertEqual(compare(["a": ["x": ["main": 2]]], ["a": ["x": ["main": 1]]]), [["a: x 1 -> 2"], []])
        XCTAssertEqual(compare(["b": ["x": ["main": 1]]], [:]), [["b: x 0 -> 1"], []])
        XCTAssertEqual(compare([:], ["a": ["x": ["main": 1]]]), [[], ["a: x 1 -> 0"]])
        XCTAssertEqual(compare(["a": ["x": ["main": 1]]], ["a": ["x": ["main": 1]]]), [[], []])
        // A file that moves a call to another rule grew in one and shrank in the other.
        XCTAssertEqual(compare(["a": ["y": ["main": 1]]], ["a": ["x": ["main": 1]]]), [["a: y 0 -> 1"], ["a: x 1 -> 0"]])
    }

    func testUnknownCountsLikeMain() {
        // A call the lint stops understanding is not a fix, and a new call it cannot place fails like one on the
        // main actor.
        XCTAssertEqual(Baseline.compare(["a": ["x": ["unknown": 1]]], [:]).grown, ["a: x 0 -> 1"])
        XCTAssertEqual(Baseline.compare(["a": ["x": ["main": 1, "unknown": 1]]], ["a": ["x": ["main": 1]]]).grown, ["a: x 1 -> 2"])
        // The same number split differently is not growth, but the committed file must say what is true.
        let moved = Baseline.compare(["a": ["x": ["unknown": 1]]], ["a": ["x": ["main": 1]]])
        XCTAssertEqual(moved.grown, [])
        XCTAssertEqual(moved.stale, ["a: x 1 main -> 1 unknown"])
    }

    func testTheBaselineRoundTripsInAStableOrder() throws {
        let counts: Baseline.Counts = ["b.swift": ["file-read": ["main": 1]], "a/c.swift": ["image-decode": ["unknown": 2, "main": 1]]]
        let data = try Baseline.encode(counts)
        XCTAssertEqual(try Baseline.decode(data), counts)
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertTrue(text.hasSuffix("}\n"))
        XCTAssertTrue(text.contains("\"a/c.swift\""), text)
        XCTAssertLessThan(text.range(of: "a/c.swift")!.lowerBound, text.range(of: "b.swift")!.lowerBound)
        XCTAssertThrowsError(try Baseline.decode(Data("not json".utf8)))
    }
}
