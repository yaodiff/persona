import XCTest
import IsolationLintCore

/// Where a closure runs: the forms that leave the actor, the ones that stay, the ones that come back, and the ones
/// the lint follows to where the closure is called. The expectations were checked against the compiler like the ones
/// in DeclarationTests.
final class ClosureTests: XCTestCase {
    // MARK: Forms that leave the actor

    func testTaskDetachedLeavesTheMainActor() {
        expect("""
        final class Model {
            func screen() {
                Task.detached { _ = try? Data(contentsOf: url) } // off
                Task.detached(priority: .utility) { _ = try? Data(contentsOf: url) } // off
                Task.detached(operation: { _ = try? Data(contentsOf: url) }) // off
                Task.detached {
                    [1].forEach { _ in _ = try? Data(contentsOf: url) } // off
                }
            }
        }
        """)
    }

    func testTheReasonSaysWhichFormLeftTheActor() {
        let why = reason("""
        func screen() {
            Task.detached { _ = try? Data(contentsOf: url) }
        }
        """)
        XCTAssertTrue(why.contains("leaves the actor: Task.detached"), why)
    }

    func testThreadsAndDispatchQueuesThatAreNotTheMainOne() {
        expect("""
        final class Model {
            private let queue = DispatchQueue(label: "model.io")
            private static let shared = DispatchQueue(label: "model.shared", qos: .utility)
            func screen() {
                Thread.detachNewThread { _ = try? Data(contentsOf: url) } // off
                DispatchQueue.global().async { _ = try? Data(contentsOf: url) } // off
                DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now()) { _ = try? Data(contentsOf: url) } // off
                queue.async { _ = try? Data(contentsOf: url) } // off
                self.queue.asyncAfter(deadline: .now()) { _ = try? Data(contentsOf: url) } // off
                Self.shared.async { _ = try? Data(contentsOf: url) } // off
            }
        }
        """)
    }

    func testAQueueTheFileDoesNotCreateIsNotTakenForABackgroundQueue() {
        // `queue` may be the main queue: in main-actor code the closure is reported, elsewhere it is unknown.
        expect("""
        final class Model {
            func screen(_ queue: DispatchQueue) {
                queue.async { _ = try? Data(contentsOf: url) } // main
            }
            nonisolated func work(_ queue: DispatchQueue) {
                queue.async { _ = try? Data(contentsOf: url) } // unknown
            }
        }
        """)
        // A name that is a labelled queue in one place and something else in another is not a queue of its own.
        expect("""
        final class Model {
            private let queue = DispatchQueue(label: "model.io")
            func screen() {
                let queue = DispatchQueue.main
                queue.async { _ = try? Data(contentsOf: url) } // main
            }
        }
        """)
    }

    func testTaskGroupChildrenAndAsyncLetRunInChildTasks() {
        expect("""
        final class Model {
            func screen() async {
                await withTaskGroup(of: Void.self) { group in
                    _ = try? Data(contentsOf: url) // main
                    group.addTask { _ = try? Data(contentsOf: url) } // off
                    _ = group.addTaskUnlessCancelled { _ = try? Data(contentsOf: url) } // off
                }
                try? await withThrowingTaskGroup(of: Void.self) { group in
                    group.addTask { _ = try? Data(contentsOf: url) } // off
                }
                await withDiscardingTaskGroup { group in
                    group.addTask { _ = try? Data(contentsOf: url) } // off
                }
                async let data = try? Data(contentsOf: url) // off
                _ = await data
            }
        }
        """)
    }

    func testAddTaskOutsideATaskGroupIsNotTakenForOne() {
        expect("""
        final class Model {
            func screen(_ pool: Pool) {
                pool.addTask { _ = try? Data(contentsOf: url) } // main
            }
            nonisolated func work(_ pool: Pool) {
                pool.addTask { _ = try? Data(contentsOf: url) } // unknown
            }
        }
        """)
    }

    // MARK: Forms that stay

    func testATaskStartsWhereItIsWritten() {
        expect("""
        final class Model {
            func screen() {
                Task { _ = try? Data(contentsOf: url) } // main
                Task(priority: .utility) { _ = try? Data(contentsOf: url) } // main
                Task.immediate { _ = try? Data(contentsOf: url) } // main
                Task { @Sendable in _ = try? Data(contentsOf: url) } // main
            }
            nonisolated func work() {
                Task { _ = try? Data(contentsOf: url) } // off
            }
            nonisolated func workLater() async {
                Task { _ = try? Data(contentsOf: url) } // off
            }
        }
        nonisolated struct Worker {
            func start() {
                Task { _ = try? Data(contentsOf: url) } // off
            }
        }
        actor Cache {
            func start() {
                Task { _ = try? Data(contentsOf: url) } // off
            }
        }
        """)
    }

    func testATaskInsideADetachedTaskIsOffTheMainActor() {
        expect("""
        func screen() {
            Task.detached {
                Task { _ = try? Data(contentsOf: url) } // off
            }
        }
        """)
    }

    func testClosuresThatRunBeforeTheCallReturnRunWhereTheCallDoes() {
        expect("""
        final class Model {
            func screen(_ urls: [URL]) async {
                urls.forEach { _ = try? Data(contentsOf: $0) } // main
                _ = urls.map { try? Data(contentsOf: $0) } // main
                _ = urls.compactMap { try? Data(contentsOf: $0) } // main
                    .filter { _ in (try? Data(contentsOf: url)) != nil } // main
                _ = (urls + urls).map { try? Data(contentsOf: $0) } // main
                await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
                    _ = try? Data(contentsOf: url) // main
                    c.resume()
                }
                _ = { try? Data(contentsOf: url) }() // main
            }
            nonisolated func work(_ urls: [URL]) {
                urls.forEach { _ = try? Data(contentsOf: $0) } // off
                [url, url].forEach { _ = try? Data(contentsOf: $0) } // off
                (urls + urls).forEach { _ = try? Data(contentsOf: $0) } // off
            }
            func start(_ urls: [URL]) {
                Task.detached { self.work(urls) }
            }
        }
        """)
    }

    func testALazySequenceKeepsItsClosure() {
        expect("""
        nonisolated func work(_ urls: [URL]) -> Any {
            urls.lazy.map { try? Data(contentsOf: $0) } // unknown
        }
        nonisolated func workToo(_ urls: [URL]) -> Any {
            (urls.lazy).map { try? Data(contentsOf: $0) } // unknown
        }
        nonisolated func start(_ urls: [URL]) {
            _ = work(urls)
            _ = workToo(urls)
        }
        """)
    }

    func testAPublishersOrAnAsyncSequencesOperatorIsNotTakenForACollections() {
        // Both keep the closure and run it where their values are produced, which the lint cannot see. Without the
        // import, or outside the `for await`, the same lines would be cleared: every caller is off the main actor.
        expect("""
        import Combine
        nonisolated func watch(_ subject: PassthroughSubject<URL, Never>) -> AnyCancellable {
            subject.map { try? Data(contentsOf: $0) }.sink { _ in } // unknown
        }
        func start(_ subject: PassthroughSubject<URL, Never>) {
            Task.detached { _ = watch(subject) }
        }
        """)
        expect("""
        nonisolated func watch(_ stream: AsyncStream<URL>) async {
            for await _ in stream.map({ try? Data(contentsOf: $0) }) { } // unknown
        }
        func start(_ stream: AsyncStream<URL>) {
            Task.detached { await watch(stream) }
        }
        """)
        expect("""
        nonisolated func read(_ urls: [URL]) -> [Data?] {
            urls.map { try? Data(contentsOf: $0) } // off
        }
        func start(_ urls: [URL]) {
            Task.detached { _ = read(urls) }
        }
        """)
    }

    func testSyncOnAQueueThatIsNotTheMainOneRunsOnTheCallingThread() {
        expect("""
        final class Model {
            private let queue = DispatchQueue(label: "model.io")
            func screen() {
                queue.sync { _ = try? Data(contentsOf: url) } // main
                DispatchQueue.global().sync { _ = try? Data(contentsOf: url) } // main
            }
            nonisolated func work() {
                queue.sync { _ = try? Data(contentsOf: url) } // off
            }
            func start() {
                Task.detached { self.work() }
            }
        }
        """)
    }

    // MARK: Forms that come back to the main actor

    func testWaysBackToTheMainActorFromCodeThatLeftIt() {
        expect("""
        func screen() {
            Task.detached {
                await MainActor.run { _ = try? Data(contentsOf: url) } // main
                DispatchQueue.main.async { _ = try? Data(contentsOf: url) } // main
                DispatchQueue.main.asyncAfter(deadline: .now()) { _ = try? Data(contentsOf: url) } // main
                DispatchQueue.main.sync { _ = try? Data(contentsOf: url) } // main
                OperationQueue.main.addOperation { _ = try? Data(contentsOf: url) } // main
                Task { @MainActor in _ = try? Data(contentsOf: url) } // main
            }
            Task.detached { @MainActor in _ = try? Data(contentsOf: url) } // main
        }
        """)
    }

    func testAMainActorClosureInATargetWithNoDefaultIsolation() {
        expect("""
        func work() {
            Task { @MainActor in _ = try? Data(contentsOf: url) } // main
            Task { _ = try? Data(contentsOf: url) } // off
            DispatchQueue.main.async { _ = try? Data(contentsOf: url) } // main
        }
        """, in: package)
    }

    func testAConcurrentClosureLeaves() {
        expect("""
        func screen() {
            Task { @concurrent in _ = try? Data(contentsOf: url) } // off
        }
        """)
    }

    // MARK: Explicit @Sendable

    func testAnExplicitlySendableClosureIsNotIsolatedToTheCodeAroundIt() {
        expect("""
        nonisolated final class Box: @unchecked Sendable {
            var handler: (@Sendable () -> Void)?
        }
        final class Model {
            let box = Box()
            func screen() {
                // Kept and called from a detached task: it runs there.
                box.handler = { @Sendable in _ = try? Data(contentsOf: url) } // off
                // Run in place: it runs on its caller.
                [1].forEach { @Sendable _ in _ = try? Data(contentsOf: url) } // main
                // A Task in a closure that is not isolated starts on the global executor.
                [1].forEach { @Sendable _ in
                    Task { _ = try? Data(contentsOf: url) } // off
                }
            }
            func fire() {
                let box = self.box
                Task.detached { box.handler?() }
            }
        }
        """)
    }

    func testAClosureMadeSendableOnlyByWhereItGoesIsStillReported() {
        // The compiler makes this literal `@Sendable` from the property's type, so it really runs off the main actor;
        // the lint reads the literal alone and reports it. A false positive, never a false negative.
        expect("""
        nonisolated final class Box: @unchecked Sendable {
            var handler: (@Sendable () -> Void)?
        }
        final class Model {
            let box = Box()
            func screen() {
                box.handler = { _ = try? Data(contentsOf: url) } // main
            }
            func fire() {
                let box = self.box
                Task.detached { box.handler?() }
            }
        }
        """)
    }

    // MARK: Closures handed to functions of the scanned sources

    func testAClosureRunsWhereTheFunctionItIsHandedToCallsIt() {
        expect("""
        nonisolated func inBackground(_ work: @escaping @Sendable () -> Void) {
            Task.detached { work() }
        }
        nonisolated func onMain(_ work: @escaping @Sendable () -> Void) {
            DispatchQueue.main.async { work() }
        }
        nonisolated func inPlace(_ work: () -> Void) {
            work()
        }
        @MainActor func onActor(_ work: @escaping @MainActor () -> Void) { }
        nonisolated func caller() {
            inBackground { _ = try? Data(contentsOf: url) } // off
            onMain { _ = try? Data(contentsOf: url) } // main
            inPlace { _ = try? Data(contentsOf: url) } // off
        }
        nonisolated func other() {
            onActor { _ = try? Data(contentsOf: url) } // main
        }
        func start() {
            Task.detached {
                caller()
                other()
            }
        }
        """)
    }

    func testAFunctionThatHandsItsClosureOnIsUnknown() {
        expect("""
        nonisolated func later(_ work: @escaping @Sendable () -> Void) {
            DispatchQueue.global().async(execute: work)
        }
        nonisolated func caller() {
            later { _ = try? Data(contentsOf: url) } // unknown
        }
        """)
    }

    // MARK: Closures kept in properties

    func testAClosureKeptInAPropertyRunsWhereThePropertyIsCalled() {
        expect("""
        nonisolated struct Step: Sendable {
            var run: @Sendable () -> Void
        }
        nonisolated struct Hook: Sendable {
            var fire: @Sendable () -> Void
        }
        nonisolated func steps() -> (Step, Hook) {
            let step = Step(run: { _ = try? Data(contentsOf: url) }) // off
            let hook = Hook(fire: { _ = try? Data(contentsOf: url) }) // main
            return (step, hook)
        }
        func screen(_ step: Step, _ hook: Hook) {
            hook.fire()
            Task.detached { step.run() }
        }
        """)
    }

    func testAKeptClosureThatIsReadWithoutBeingCalledIsUnknown() {
        expect("""
        nonisolated struct Step: Sendable {
            var run: @Sendable () -> Void
        }
        nonisolated func make() -> Step {
            Step(run: { _ = try? Data(contentsOf: url) }) // unknown
        }
        nonisolated func hand(_ step: Step, to queue: OperationQueue) {
            queue.addOperation(step.run)
        }
        """)
    }

    func testAClosureKeptInALocalVariableIsUnknownOutsideMainActorCode() {
        expect("""
        final class Model {
            func screen() {
                let work = { _ = try? Data(contentsOf: url) } // main
                work()
            }
            nonisolated func free() {
                let work = { _ = try? Data(contentsOf: url) } // unknown
                Task.detached { work() }
            }
        }
        """)
    }

    // MARK: Closures handed to functions the lint has no source for

    func testAClosureHandedToAnUnknownFunctionIsNeverCleared() {
        // In main-actor code it is reported (it stays on the main actor unless the parameter is `@Sendable`, which
        // only the SDK's declaration says). Anywhere else the lint cannot tell and says so.
        expect("""
        final class Model {
            func screen() {
                URLSession.shared.dataTask(with: url) { _, _, _ in _ = try? Data(contentsOf: url) }.resume() // main
            }
            nonisolated func observe() {
                _ = NotificationCenter.default.addObserver(forName: .init("x"), object: nil, queue: .main) { _ in
                    _ = try? Data(contentsOf: url) // unknown
                }
            }
        }
        """)
        let why = reason("""
        nonisolated func observe() {
            _ = NotificationCenter.default.addObserver(forName: .init("x"), object: nil, queue: nil) { _ in _ = try? Data(contentsOf: url) }
        }
        """)
        XCTAssertTrue(why.contains("the lint does not know where NotificationCenter.default.addObserver runs its closure"), why)
    }

    func testTheCancellationHandlerRunsOnTheThreadThatCancels() {
        expect("""
        nonisolated func work() async {
            await withTaskCancellationHandler {
                _ = try? Data(contentsOf: url) // off
            } onCancel: {
                _ = try? Data(contentsOf: url) // unknown
            }
        }
        func start() {
            Task.detached { await work() }
        }
        """)
    }
}
