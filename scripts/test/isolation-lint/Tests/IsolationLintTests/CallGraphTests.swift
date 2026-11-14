import XCTest
import IsolationLintCore

/// Code that is not isolated runs where it is called. The lint follows calls by name, labels and the declared type of
/// what they are made on; where it cannot read that, the answer is `unknown`, never `off`.
final class CallGraphTests: XCTestCase {
    func testTheReasonListsEveryUseTheVerdictRestsOn() {
        let why = reason("""
        nonisolated func read() { _ = try? Data(contentsOf: url) }
        func screen() {
            Task.detached { read() }
            Task.detached { read() }
        }
        """)
        XCTAssertTrue(why.contains("every use the lint found is off the main actor (2: App/Sample.swift:5, App/Sample.swift:6)"), why)
    }

    func testOneCallFromTheMainActorIsEnough() {
        expect("""
        nonisolated func read() { _ = try? Data(contentsOf: url) } // main
        func screen() {
            Task.detached { read() }
            read()
        }
        """)
    }

    func testCallsAreFollowedThroughSeveralFunctions() {
        expect("""
        nonisolated enum Disk {
            static func read() -> Data? { try? Data(contentsOf: url) } // main
            static func cached() -> Data? { read() }
            static func value() -> Data? { cached() }
        }
        nonisolated enum Other {
            static func read() -> Data? { try? Data(contentsOf: url) } // off
            static func cached() -> Data? { read() }
        }
        func screen() {
            _ = Disk.value()
            Task.detached { _ = Other.cached() }
        }
        """)
    }

    func testFunctionsThatCallEachOtherSettle() {
        expect("""
        nonisolated func ping(_ n: Int) { _ = try? Data(contentsOf: url); if n > 0 { pong(n - 1) } } // off
        nonisolated func pong(_ n: Int) { ping(n) }
        func screen() {
            Task.detached { ping(3) }
        }
        """)
    }

    func testCallsAcrossFilesAndTargets() {
        expect([
            "Kit/Sources/Disk.swift": """
            public struct Disk: Sendable {
                public init() { }
                public func read() -> Data? { try? Data(contentsOf: url) } // main
                public func readOff() -> Data? { try? Data(contentsOf: url) } // off
            }
            """,
            "App/Screen.swift": """
            final class Screen {
                let disk = Disk()
                func show() { _ = disk.read() }
                func refresh() {
                    let disk = self.disk
                    Task.detached { _ = disk.readOff() }
                }
            }
            """,
        ], targets: ["App": app, "Kit": package])
    }

    func testAMemberOfAnotherTypeWithTheSameNameIsNotACall() {
        expect("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) } // off
        }
        nonisolated struct Label {
            func read() -> String { "" }
        }
        func screen(_ label: Label) {
            _ = label.read()
            _ = Label().read()
            Task.detached { _ = Disk().read() }
        }
        """)
    }

    func testLabelsTellOverloadsApart() {
        expect("""
        nonisolated struct Disk {
            func read(at path: String) -> Data? { try? Data(contentsOf: url) } // off
            func read(named name: String) -> Data? { try? Data(contentsOf: url) } // main
        }
        func screen(_ disk: Disk) {
            _ = disk.read(named: "")
            Task.detached { _ = disk.read(at: "") }
        }
        """)
    }

    func testAValueTypedByWhatItIsMadeFrom() {
        expect("""
        nonisolated final class Store: Sendable {
            static let shared = Store()
            func read() -> Data? { try? Data(contentsOf: url) } // main
            func make() -> Reader { Reader() }
        }
        nonisolated struct Reader {
            func load() -> Data? { try? Data(contentsOf: url) } // main
        }
        func screen() {
            let store = Store.shared
            _ = store.read()
            _ = store.make().load()
        }
        """)
    }

    // MARK: Unknown

    func testACallOnAValueTheLintCannotTypeIsUnknownNotOff() {
        // `$0` has no declaration to read: it may be a Disk, so the call may be on the main actor.
        expect("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) } // unknown
        }
        func screen(_ items: [Any]) {
            Task.detached { _ = Disk().read() }
            _ = items.map { ($0 as AnyObject).read() }
        }
        """)
        let why = reason("""
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) }
        }
        func screen(_ items: [Any]) {
            Task.detached { _ = Disk().read() }
            _ = items.map { ($0 as AnyObject).read() }
        }
        """)
        XCTAssertTrue(why.contains("may"), why)
    }

    func testACallOnAnUntypedValueNeverMakesAMainVerdict() {
        // The same call cannot prove the function is on the main actor either.
        let findings = lint(["App/Sample.swift": prelude + """
        nonisolated struct Disk {
            func read() -> Data? { try? Data(contentsOf: url) }
        }
        func screen(_ items: [Any]) {
            _ = items.map { ($0 as AnyObject).read() }
        }
        """])
        XCTAssertEqual(findings.map(\.verdict), [.unknown])
    }

    func testAFunctionHandedOnAsAValueIsUnknown() {
        expect("""
        nonisolated func read() { _ = try? Data(contentsOf: url) } // unknown
        nonisolated func schedule(_ queue: OperationQueue) {
            queue.addOperation(read)
        }
        """)
    }

    func testAFunctionHandedToAnInlineCallIsACall() {
        expect("""
        nonisolated func read(_ at: URL) -> Data? { try? Data(contentsOf: at) } // main
        nonisolated func readOff(_ at: URL) -> Data? { try? Data(contentsOf: at) } // off
        func screen(_ urls: [URL]) {
            _ = urls.compactMap(read)
            Task.detached { _ = urls.compactMap(readOff) }
        }
        """)
    }

    func testWhatObjectiveCCanCallIsUnknown() {
        expect("""
        import UIKit
        nonisolated final class Target: NSObject {
            @objc func fire() { _ = try? Data(contentsOf: url) } // unknown
            func viaSelector() { _ = try? Data(contentsOf: url) } // unknown
            func hook() -> Selector { #selector(viaSelector) }
        }
        """)
    }

    func testAnOverrideOfAClassTheLintHasNoSourceForIsUnknown() {
        expect("""
        nonisolated final class Work: Operation, @unchecked Sendable {
            override func main() { _ = try? Data(contentsOf: url) } // unknown
        }
        """)
    }

    func testADelegateCallbackOfAnSDKProtocolIsUnknown() {
        // Nothing in the scanned sources calls it, and the lint has no list of what `URLSessionDelegate` calls.
        expect("""
        nonisolated final class Listener: NSObject, URLSessionDataDelegate {
            func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
                _ = try? Data(contentsOf: url) // unknown
            }
        }
        """)
    }

    func testAFileNoTargetCompilesIsUnknown() {
        let findings = lint(["Loose/Sample.swift": prelude + "func read() { _ = try? Data(contentsOf: url) }\n"])
        XCTAssertEqual(findings.map(\.verdict), [.unknown])
        XCTAssertTrue(findings[0].reason.contains("no target or package the lint knows compiles this file"), findings[0].reason)
    }

    func testAKeyPathToANonisolatedPropertyIsUnknown() {
        expect("""
        nonisolated struct Entry {
            var data: Data? { try? Data(contentsOf: url) } // unknown
        }
        nonisolated func all(_ entries: [Entry]) -> [Data?] {
            entries.map(\\.data)
        }
        """)
    }
}
