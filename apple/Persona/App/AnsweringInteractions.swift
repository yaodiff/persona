import Foundation
import PersonaWire

private nonisolated func sameAnswerOccurrence(_ lhs: String?, _ rhs: String?) -> Bool {
    if lhs == rhs { return true }
    guard let lhs, let rhs else { return false }
    return JSON.compareOccurrenceTimes(lhs, rhs) == .orderedSame
}

/// The cards being answered right now (`AppModel.resolve`), and which of them the hub ended while the answer was on
/// its way (#3922). A screen card whose dialog changed (409 `prompt_changed`) or whose keys did not reach the computer
/// is over on the hub: it says `interaction_resolved` before its error comes back, and a card for what is on the screen
/// now follows. Put back after that event, the old card stayed for good. A Codex card the node could not answer stays
/// pending on the hub, without that event, so it goes back.
nonisolated struct AnsweringInteractions {
    private struct Flight {
        var occurrence: String?
        var endedByHub = false
    }

    private var inFlight: [String: Flight] = [:]

    mutating func begin(_ uid: String, occurrence: String? = nil) {
        inFlight[uid] = Flight(occurrence: occurrence)
    }

    /// The hub said this card is over. A legacy ID-only event applies to the
    /// current flight; an occurrence-bearing event applies only to its own.
    mutating func hubEnded(_ uid: String, occurrence: String? = nil) {
        guard var flight = inFlight[uid], occurrence == nil || sameAnswerOccurrence(flight.occurrence, occurrence) else { return }
        flight.endedByHub = true
        inFlight[uid] = flight
    }

    /// This card's answer is on its way.
    func contains(_ uid: String) -> Bool { inFlight[uid] != nil }

    /// Whether this occurrence's failed answer puts its card back. An older
    /// request never consults or restores over a replacement's flight state.
    func putsBack(_ uid: String, occurrence: String? = nil) -> Bool {
        guard let flight = inFlight[uid] else { return true }
        guard sameAnswerOccurrence(flight.occurrence, occurrence) else { return false }
        return !flight.endedByHub
    }

    mutating func finish(_ uid: String, occurrence: String? = nil) {
        guard let flight = inFlight[uid], sameAnswerOccurrence(flight.occurrence, occurrence) else { return }
        inFlight[uid] = nil
    }
}

/// What a question card shows while its answer is on its way (#6279): the card stays where it is, greyed, until the hub
/// has the answer; a failed answer leaves it with its picks and the error. One op id per answer: the same answer sent
/// again (Retry) goes with the id it had, so the hub does it once; a different answer is a new one.
nonisolated struct AnswerAttempts {
    enum Progress: Equatable {
        case sending
        case failed(String)
    }

    private struct Attempt {
        var op: UUID
        var resolution: Resolution
        var occurrence: String?
        var progress: Progress
    }

    private var attempts: [String: Attempt] = [:]

    /// Starts sending `resolution` for the card; nil when its answer is already on its way (a second tap sends nothing).
    /// An exact proposal reply also carries its occurrence: a same-ID replacement gets a new owner event, while the old
    /// request's completion can finish only the old attempt.
    mutating func begin(_ uid: String, _ resolution: Resolution, occurrence: String? = nil, newOp: () -> UUID = UUID.init) -> UUID? {
        if let a = attempts[uid], sameAnswerOccurrence(a.occurrence, occurrence) {
            if a.progress == .sending { return nil }
            if a.resolution == resolution {
                attempts[uid]?.progress = .sending
                return a.op
            }
        }
        let op = newOp()
        attempts[uid] = Attempt(op: op, resolution: resolution, occurrence: occurrence, progress: .sending)
        return op
    }

    /// Whether this request still owns the card's latest attempt.
    func owns(_ uid: String, op: UUID) -> Bool { attempts[uid]?.op == op }

    /// This request is over: only its exact operation may clear the attempt.
    @discardableResult
    mutating func finish(_ uid: String, op: UUID) -> Bool {
        guard attempts[uid]?.op == op else { return false }
        attempts[uid] = nil
        return true
    }

    /// This request failed: only its own occurrence may record the error.
    mutating func fail(_ uid: String, _ message: String, occurrence: String? = nil) {
        guard let attempt = attempts[uid], sameAnswerOccurrence(attempt.occurrence, occurrence) else { return }
        attempts[uid]?.progress = .failed(message)
    }

    /// A hub event ends the card. Legacy ID-only events have no occurrence to
    /// match; occurrence-bearing events clean up only their own failed attempt.
    mutating func finishEvent(_ uid: String, occurrence: String? = nil) {
        guard let attempt = attempts[uid], occurrence == nil || sameAnswerOccurrence(attempt.occurrence, occurrence) else { return }
        attempts[uid] = nil
    }

    func progress(_ uid: String) -> Progress? { attempts[uid]?.progress }

    /// The answer a failed card sends again.
    func retryable(_ uid: String) -> Resolution? {
        guard let a = attempts[uid], case .failed = a.progress else { return nil }
        return a.resolution
    }
}
