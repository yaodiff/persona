import CoreGraphics
import Foundation
import Testing
@testable import Persona

/// The small window's transition (#6333) without the system's picture in picture: where its source lies (over the
/// page's picture, else the top trailing corner, never the bottom-left corner it popped up from), and Restore's
/// completion, answered once and in time even with the main thread busy.
@MainActor struct VNCPiPTransitionTests {
    private let window = CGRect(x: 0, y: 0, width: 402, height: 874)
    private let size = CGSize(width: 192, height: 120)

    @Test func overThePagesPicture() {
        let picture = CGRect(x: 0, y: 311, width: 402, height: 251)
        #expect(VNCPiPPlacement.source(page: picture, in: window, safeTop: 62, size: size, margin: 16) == picture)
        // Zoomed in, the whole picture runs off the screen: the source does too, so what shows is where it starts.
        let zoomed = CGRect(x: -402, y: 60, width: 1206, height: 754)
        #expect(VNCPiPPlacement.source(page: zoomed, in: window, safeTop: 62, size: size, margin: 16) == zoomed)
    }

    @Test func withNoPageInTheTopTrailingCorner() {
        let r = VNCPiPPlacement.source(page: nil, in: window, safeTop: 62, size: size, margin: 16)
        #expect(r == CGRect(x: 402 - 192 - 16, y: 62 + 16, width: 192, height: 120))
        #expect(r.maxY < window.midY, "not the bottom-left corner it used to pop up from")
        // A page not laid out yet (no size) counts as none.
        #expect(VNCPiPPlacement.source(page: .zero, in: window, safeTop: 62, size: size, margin: 16) == r)
    }

    @Test func restoreAnswersOnce() {
        let restore = VNCPiPRestore(), answers = Answers()
        #expect(!restore.pending)
        restore.arm(after: .seconds(30)) { answers.add($0) }
        #expect(restore.pending)
        restore.finish() // the page's picture is in place
        restore.finish() // the restore is over
        #expect(answers.list == [true])
        #expect(!restore.pending)
    }

    @Test func aSecondRestoreAnswersTheFirst() {
        let restore = VNCPiPRestore(), first = Answers(), second = Answers()
        restore.arm(after: .seconds(30)) { first.add($0) }
        restore.arm(after: .seconds(30)) { second.add($0) }
        #expect(first.list == [true] && second.list.isEmpty)
        restore.finish()
        #expect(first.list == [true] && second.list == [true])
    }

    /// The page opening can keep the main thread busy (10 s on a loaded simulator): the wait runs out all the same.
    @Test func theWaitRunsOutWhileTheMainThreadIsBusy() {
        let restore = VNCPiPRestore(), answers = Answers()
        restore.arm(after: .milliseconds(50)) { answers.add($0) }
        Thread.sleep(forTimeInterval: 0.5)
        #expect(answers.list == [true])
        #expect(!restore.pending)
    }

    @Test func anOldWaitLeavesANewRestoreAlone() {
        let restore = VNCPiPRestore(), first = Answers(), second = Answers()
        restore.arm(after: .milliseconds(50)) { first.add($0) }
        restore.finish()
        restore.arm(after: .seconds(30)) { second.add($0) }
        Thread.sleep(forTimeInterval: 0.3) // the first wait runs out now
        #expect(first.list == [true] && second.list.isEmpty && restore.pending)
        restore.finish()
        #expect(second.list == [true])
    }
}

/// What a restore answered, from whichever thread answers it.
private final class Answers: @unchecked Sendable {
    private let lock = NSLock()
    private var answers: [Bool] = []
    var list: [Bool] { lock.withLock { answers } }
    func add(_ answer: Bool) { lock.withLock { answers.append(answer) } }
}
