import Foundation

/// The hints of one window share one read (#9487; the web's `SessionList.invalidate`, #9042). A busy hub broadcasts a
/// `sessions_list` hint for each working agent's update, about one a second; reading after each one kept a list's
/// requests going back to back while nobody looked at it. The first read after a quiet window starts at once; one asked
/// for less than the window after the last started waits for the window's end, and the hints that arrive meanwhile
/// share it. The length is asked each time: the hub's lists use `length` while no Agents list is in sight and the
/// shorter `inSight` while one is (`RuntimeSessionLists.listInSight`, #16758), so what is on screen keeps its
/// timeliness (docs/apple/ios-state.md "Timely").
@MainActor final class HintWindow {
    /// The web's window: long enough to merge a busy fleet's hints, short enough that nothing looks stale on return.
    nonisolated static let length: Duration = .seconds(2)
    /// An Agents list in sight (#16758): a hint waits at most this long (about what a read already in flight cost before),
    /// so what is on screen stays within its 1 s limit's reach, while a busy fleet's hints share one read a second.
    nonisolated static let inSight: Duration = .seconds(1)

    private let length: @MainActor () -> Duration
    private let now: @MainActor () -> ContinuousClock.Instant
    private let sleep: @MainActor (Duration) async throws -> Void
    private var last: ContinuousClock.Instant?
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private var timer: Task<Void, Never>?

    init(length: @escaping @MainActor () -> Duration = { HintWindow.length },
         now: @escaping @MainActor () -> ContinuousClock.Instant = { .now },
         sleep: @escaping @MainActor (Duration) async throws -> Void = { try await Task.sleep(for: $0) }) {
        self.length = length
        self.now = now
        self.sleep = sleep
    }

    /// How long a read asked for now would wait: zero when the last one started a window or more ago (or none has).
    var remaining: Duration {
        let length = length()
        guard let last, length > .zero else { return .zero }
        return max(.zero, last.advanced(by: length) - now())
    }

    /// Waits for the window's end (or `release`) if a read started inside it, then counts the caller's read as started.
    /// Throws when cancelled while waiting; that read is not counted.
    func enter() async throws {
        let wait = remaining
        if wait > .zero {
            await withTaskCancellationHandler {
                await withCheckedContinuation { waiter in
                    waiters.append(waiter)
                    if timer == nil {
                        timer = Task { [weak self] in
                            do { try await self?.sleep(wait) } catch { return }
                            guard !Task.isCancelled else { return }
                            self?.open()
                        }
                    }
                }
            } onCancel: {
                Task { @MainActor [weak self] in self?.open() }
            }
            try Task.checkCancellation()
        }
        last = now()
    }

    /// The list is in sight again: a read waiting for the window goes now.
    func release() { open() }

    /// A new query or hub: its first read is not held back by the old one's.
    func reset() {
        last = nil
        open()
    }

    private func open() {
        timer?.cancel()
        timer = nil
        let all = waiters
        waiters = []
        for waiter in all { waiter.resume() }
    }
}
