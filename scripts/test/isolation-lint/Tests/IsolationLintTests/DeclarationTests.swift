import XCTest
import IsolationLintCore

/// Where a declaration is isolated, and so where a blocking call written in it runs. Every expectation here was
/// checked against the compiler (Swift 6 language mode, `-default-isolation MainActor`,
/// `NonisolatedNonsendingByDefault`) by running the same shapes and reading `Thread.isMainThread`.
final class DeclarationTests: XCTestCase {
    // MARK: Targets that default to MainActor

    func testUnannotatedDeclarationsOfAnAppTargetAreOnTheMainActor() {
        expect("""
        final class Model {
            func load() { _ = try? Data(contentsOf: url) } // main
            func loadLater() async { _ = try? Data(contentsOf: url) } // main
            var cached: Data? { try? Data(contentsOf: url) } // main
            init() { _ = try? Data(contentsOf: url) } // main
            static func shared() { _ = try? Data(contentsOf: url) } // main
        }
        struct Row: Sendable, Codable, Identifiable {
            var id = 1
            func read() { _ = try? Data(contentsOf: url) } // main
        }
        func free() { _ = try? Data(contentsOf: url) } // main
        extension URL {
            func read() { _ = try? Data(contentsOf: self) } // main
        }
        protocol Source { }
        extension Source {
            func read() { _ = try? Data(contentsOf: url) } // main
        }
        """)
    }

    func testTheReasonNamesTheTargetThatMakesItMainActor() {
        let why = reason("""
        final class Model {
            func load() { _ = try? Data(contentsOf: url) }
        }
        """)
        XCTAssertTrue(why.contains("MainActor by default in target App"), why)
    }

    func testTheSameDeclarationsInATargetWithNoDefaultIsolationRunWhereTheyAreCalled() {
        expect("""
        final class Store: Sendable {
            func fromMain() { _ = try? Data(contentsOf: url) } // main
            func fromDetached() { _ = try? Data(contentsOf: url) } // off
            func later() async { _ = try? Data(contentsOf: url) } // off
            @MainActor func onMain() { _ = try? Data(contentsOf: url) } // main
        }
        func free() { _ = try? Data(contentsOf: url) } // off
        @MainActor func screen(_ store: Store) async {
            store.fromMain()
            store.onMain()
            await store.later()
            Task.detached {
                store.fromDetached()
                free()
            }
        }
        """, in: package)
    }

    func testAFileCompiledByTwoTargetsIsOnTheMainActorIfEitherMakesItSo() {
        var project = ProjectMap()
        project.addTarget(app, sources: ["Shared"])
        project.addTarget(TargetConfig(name: "Extension", defaultMain: false, nonsending: false), sources: ["Shared"])
        let source = prelude + """
        struct Snapshot {
            static func load() -> Data? { try? Data(contentsOf: url) }
        }
        func refresh() { Task.detached { _ = Snapshot.load() } }
        """
        let findings = Lint(files: [.init(path: "Shared/Snapshot.swift", source: source)], project: project).findings()
        XCTAssertEqual(findings.map(\.verdict), [.main])
    }

    // MARK: nonisolated

    func testANonisolatedFunctionRunsWhereItIsCalled() {
        expect("""
        final class Model {
            nonisolated func fromMain() { _ = try? Data(contentsOf: url) } // main
            nonisolated func fromDetached() { _ = try? Data(contentsOf: url) } // off
            nonisolated func fromBoth() { _ = try? Data(contentsOf: url) } // main
            func screen() {
                fromMain()
                fromBoth()
                Task.detached {
                    self.fromDetached()
                    self.fromBoth()
                }
            }
        }
        """)
    }

    func testANonisolatedAsyncFunctionRunsOnItsCallerWhereTheTargetSaysSo() {
        // NonisolatedNonsendingByDefault (app targets): awaited from the main actor, it stays there.
        expect("""
        final class Model {
            nonisolated func load() async { _ = try? Data(contentsOf: url) } // main
            nonisolated(nonsending) func loadToo() async { _ = try? Data(contentsOf: url) } // main
            nonisolated func loadOff() async { _ = try? Data(contentsOf: url) } // off
            func screen() async {
                await load()
                await loadToo()
                await Task.detached { await self.loadOff() }.value
            }
        }
        """)
        // Without it (packages), a nonisolated async function always leaves its caller's actor.
        expect("""
        final class Store: Sendable {
            nonisolated func load() async { _ = try? Data(contentsOf: url) } // off
            func loadToo() async { _ = try? Data(contentsOf: url) } // off
        }
        @MainActor func screen(_ store: Store) async {
            await store.load()
            await store.loadToo()
        }
        """, in: package)
    }

    func testAConcurrentFunctionLeavesTheActor() {
        expect("""
        final class Model {
            @concurrent nonisolated func decode() async { _ = try? Data(contentsOf: url) } // off
            @concurrent func decodeToo() async { _ = try? Data(contentsOf: url) } // off
            func screen() async {
                await decode()
                await decodeToo()
            }
        }
        """)
    }

    func testMembersOfANonisolatedTypeRunWhereTheyAreCalled() {
        expect("""
        nonisolated struct Reader {
            func fromMain() { _ = try? Data(contentsOf: url) } // main
            func fromDetached() { _ = try? Data(contentsOf: url) } // off
            static func make() { _ = try? Data(contentsOf: url) } // off
            struct Nested {
                func read() { _ = try? Data(contentsOf: url) } // off
            }
        }
        nonisolated extension Reader {
            func alsoFree() { _ = try? Data(contentsOf: url) } // off
            struct InExtension {
                func read() { _ = try? Data(contentsOf: url) } // off
            }
        }
        func screen() {
            Reader().fromMain()
            Task.detached {
                Reader().fromDetached()
                Reader.make()
                Reader.Nested().read()
                Reader().alsoFree()
                Reader.InExtension().read()
            }
        }
        """)
    }

    func testAPlainExtensionOfANonisolatedTypeIsBackOnTheMainActor() {
        // `nonisolated` on a type does not reach its unannotated extensions in a MainActor-by-default target.
        expect("""
        nonisolated struct Reader { }
        extension Reader {
            func read() { _ = try? Data(contentsOf: url) } // main
            struct Inner {
                func read() { _ = try? Data(contentsOf: url) } // main
            }
        }
        func screen() {
            Task.detached {
                await Reader().read()
                await Reader.Inner().read()
            }
        }
        """)
    }

    func testANonisolatedMemberNobodyCallsIsUnknown() {
        let why = reason("""
        final class Model {
            nonisolated func neverCalled() { _ = try? Data(contentsOf: url) }
        }
        """)
        expect("""
        final class Model {
            nonisolated func neverCalled() { _ = try? Data(contentsOf: url) } // unknown
        }
        """)
        XCTAssertTrue(why.contains("no call"), why)
    }

    // MARK: Explicit @MainActor

    func testExplicitMainActorInATargetWithNoDefaultIsolation() {
        expect("""
        @MainActor final class Model {
            func load() { _ = try? Data(contentsOf: url) } // main
            static func make() { _ = try? Data(contentsOf: url) } // main
            nonisolated func free() { _ = try? Data(contentsOf: url) } // off
            struct Nested {
                func read() { _ = try? Data(contentsOf: url) } // off
            }
        }
        extension Model {
            func more() { _ = try? Data(contentsOf: url) } // main
        }
        struct Plain: Sendable { }
        @MainActor extension Plain {
            func read() { _ = try? Data(contentsOf: url) } // main
        }
        @MainActor func free() { _ = try? Data(contentsOf: url) } // main
        struct Mixed: Sendable {
            @MainActor var cached: Data? { try? Data(contentsOf: url) } // main
            @MainActor init(load: Bool) { _ = try? Data(contentsOf: url) } // main
        }
        func work(_ model: Model) {
            Task.detached {
                model.free()
                Model.Nested().read()
            }
        }
        """, in: package)
    }

    func testANonisolatedMemberOfAMainActorTypeIsNotOnIt() {
        expect("""
        @MainActor final class Model {
            nonisolated func free() { _ = try? Data(contentsOf: url) } // off
            nonisolated var cached: Data? { try? Data(contentsOf: url) } // off
            func screen() {
                Task.detached {
                    self.free()
                    _ = self.cached
                }
            }
        }
        """)
    }

    func testTypesThatAreMainActorInTheSDK() {
        // A SwiftUI view is on the main actor in a package too, and so is an extension of `View`.
        expect("""
        import SwiftUI
        struct Row: View {
            var body: some View { Text("") }
            func load() { _ = try? Data(contentsOf: url) } // main
        }
        extension View {
            func load() { _ = try? Data(contentsOf: url) } // main
        }
        final class Controller: UIViewController {
            func load() { _ = try? Data(contentsOf: url) } // main
        }
        """, in: package)
    }

    // MARK: What the compiler infers

    func testErrorAndCodingKeyTypesAreInferredNonisolated() {
        // A type that conforms, on its own declaration, to a protocol that inherits from `SendableMetatype`.
        expect("""
        enum Failure: Error {
            case missing
            func read() { _ = try? Data(contentsOf: url) } // off
            struct Detail {
                func read() { _ = try? Data(contentsOf: url) } // off
            }
        }
        extension Failure {
            func more() { _ = try? Data(contentsOf: url) } // off
        }
        struct Broken: LocalizedError {
            func read() { _ = try? Data(contentsOf: url) } // off
        }
        enum Keys: String, CodingKey {
            case a
            func read() { _ = try? Data(contentsOf: url) } // off
            static func all() { _ = try? Data(contentsOf: url) } // off
        }
        func screen() {
            Task.detached {
                Failure.missing.read()
                Failure.Detail().read()
                Failure.missing.more()
                Broken().read()
                Keys.a.read()
                Keys.all()
            }
        }
        """)
    }

    func testSendableAndAConformanceInAnExtensionDoNotMakeATypeNonisolated() {
        expect("""
        protocol Local: Sendable { }
        struct ViaLocal: Local {
            func read() { _ = try? Data(contentsOf: url) } // main
        }
        struct Late { }
        extension Late: Error {
            func read() { _ = try? Data(contentsOf: url) } // main
        }
        func screen() {
            Task.detached {
                await ViaLocal().read()
                await Late().read()
            }
        }
        """)
    }

    func testASubclassOfANonisolatedClassIsNonisolatedButItsPlainExtensionIsNot() {
        expect("""
        nonisolated class Base {
            func over() { }
        }
        final class Sub: Base {
            override func over() { _ = try? Data(contentsOf: url) } // unknown
            func own() { _ = try? Data(contentsOf: url) } // off
        }
        extension Sub {
            func more() { _ = try? Data(contentsOf: url) } // main
        }
        func screen() {
            Task.detached {
                Sub().own()
                await Sub().more()
            }
        }
        """)
    }

    // MARK: Actors

    func testActorMembersAreOffTheMainActor() {
        expect("""
        actor Cache {
            func read() { _ = try? Data(contentsOf: url) } // off
            func viaLocal() {
                func work() { _ = try? Data(contentsOf: url) } // off
                work()
            }
            func viaAsyncLet() async {
                async let data = try? Data(contentsOf: url) // off
                _ = await data
            }
        }
        extension Cache {
            func more() { _ = try? Data(contentsOf: url) } // off
        }
        @globalActor actor Disk { static let shared = Disk() }
        @Disk func onDisk() { _ = try? Data(contentsOf: url) } // off
        @Disk final class DiskStore {
            func read() { _ = try? Data(contentsOf: url) } // off
        }
        func screen() async {
            await Cache().read()
            await onDisk()
        }
        """)
    }

    func testWhatAnActorDoesNotIsolateRunsWhereItIsCalled() {
        // Statics, `nonisolated` members and nested types of an actor are not on the actor.
        expect("""
        actor Cache {
            static func fromMain() { _ = try? Data(contentsOf: url) } // main
            static func fromDetached() { _ = try? Data(contentsOf: url) } // off
            nonisolated func free() { _ = try? Data(contentsOf: url) } // main
            struct Entry {
                func read() { _ = try? Data(contentsOf: url) } // main
            }
        }
        func screen() {
            Cache.fromMain()
            Cache().free()
            Cache.Entry().read()
            Task.detached { Cache.fromDetached() }
        }
        """)
    }

    // MARK: Protocol witnesses

    func testAMemberThatMeetsAMainActorRequirementIsOnTheMainActor() {
        // Inside a `nonisolated` type too: the witness takes the requirement's isolation unless it says otherwise.
        expect("""
        @MainActor protocol Refreshing { func refresh() }
        protocol Loading { func load() }
        nonisolated protocol Free { func free() }
        nonisolated struct Worker: Refreshing, Loading, Free {
            func refresh() { _ = try? Data(contentsOf: url) } // main
            func load() { _ = try? Data(contentsOf: url) } // main
            func free() { _ = try? Data(contentsOf: url) } // unknown
            func other() { _ = try? Data(contentsOf: url) } // off
        }
        nonisolated struct Quiet: Loading {
            nonisolated func load() { _ = try? Data(contentsOf: url) } // unknown
        }
        func screen() {
            Task.detached { Worker().other() }
        }
        """)
    }

    func testACallThroughTheProtocolReachesTheTypesThatConform() {
        expect("""
        nonisolated protocol Free { func free() }
        nonisolated struct Worker: Free {
            func free() { _ = try? Data(contentsOf: url) } // main
        }
        func screen(_ worker: any Free) {
            worker.free()
        }
        """)
        expect("""
        nonisolated protocol Free { func free() }
        nonisolated struct Worker: Free {
            func free() { _ = try? Data(contentsOf: url) } // off
        }
        func screen(_ worker: any Free) {
            Task.detached { worker.free() }
        }
        """)
    }

    // MARK: Properties, initializers, deinit

    func testPropertyInitializersRunWhereTheirOwnerIs() {
        expect("""
        final class Model {
            static let shared: Data? = try? Data(contentsOf: url) // main
            let stored: Data? = { try? Data(contentsOf: url) }() // main
            lazy var later: Data? = { try? Data(contentsOf: url) }() // main
            var computed: Data? { try? Data(contentsOf: url) } // main
        }
        let global: Data? = try? Data(contentsOf: url) // main
        """)
    }

    func testANonisolatedInitializerRunsWhereItIsCalled() {
        expect("""
        final class Model {
            nonisolated init(path: String) { _ = try? Data(contentsOf: url) } // off
            nonisolated init(name: String) { _ = try? Data(contentsOf: url) } // main
        }
        func screen() {
            _ = Model(name: "")
            Task.detached { _ = Model(path: "") }
        }
        """)
    }

    func testDeinitIsUnknownUnlessItIsIsolated() {
        // A deinit runs on whichever thread drops the last reference.
        expect("""
        final class Model {
            deinit { _ = try? Data(contentsOf: url) } // unknown
        }
        final class Pinned {
            isolated deinit { _ = try? Data(contentsOf: url) } // main
        }
        """)
    }

    func testADefaultArgumentRunsInTheCaller() {
        expect("""
        final class Model {
            func show(_ data: Data? = try? Data(contentsOf: url)) { } // main
            @concurrent nonisolated func decode(_ data: Data? = try? Data(contentsOf: url)) async { } // off
            func screen() {
                show()
                Task.detached { await self.decode() }
            }
        }
        """)
    }

    // MARK: Local declarations

    func testALocalFunctionRunsWhereItsFunctionDoes() {
        expect("""
        final class Model {
            func onMain() {
                func work() { _ = try? Data(contentsOf: url) } // main
                work()
            }
            nonisolated func free() {
                func work() { _ = try? Data(contentsOf: url) } // off
                work()
            }
            nonisolated func sendable() {
                @Sendable func work() { _ = try? Data(contentsOf: url) } // off
                Task.detached { work() }
            }
            func screen() {
                sendable()
                Task.detached { self.free() }
            }
        }
        """)
    }

    func testATypeDeclaredInsideAFunction() {
        // In a main-actor function it is MainActor by default. In a nonisolated one the compiler makes it
        // nonisolated; the lint does not follow values of local types and says unknown.
        expect("""
        final class Model {
            func onMain() {
                struct Local {
                    func read() { _ = try? Data(contentsOf: url) } // main
                }
                Local().read()
            }
            nonisolated func free() {
                struct Local {
                    func read() { _ = try? Data(contentsOf: url) } // unknown
                }
                Local().read()
            }
        }
        """)
    }
}
