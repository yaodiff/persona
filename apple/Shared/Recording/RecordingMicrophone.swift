import Foundation

/// Dictation and long recordings share the microphone; a failed start must not stop the other user's audio session.
/// A voice call holds it too, but gives way (docs/assistant/voice.md 6.5): starting a recording or dictation ends the call instead
/// of failing while one runs.
@MainActor final class RecordingMicrophone {
    static let shared = RecordingMicrophone()
    private var owner: UUID?
    /// Ends the owner that gives way (a voice call), which releases the microphone.
    private var giveWay: (@MainActor () -> Void)?

    func claim(_ id: UUID) -> Bool {
        if let holder = owner, holder != id, let giveWay {
            self.giveWay = nil
            giveWay()
            // Giving way is the contract: an owner that is gone, or did not release, must not keep it for good.
            if owner == holder { owner = nil }
        }
        guard owner == nil else { return false }
        owner = id
        return true
    }

    /// Holds it until someone else claims it: `giveWay` then ends this owner, and must release it.
    func claim(_ id: UUID, givingWay giveWay: @escaping @MainActor () -> Void) -> Bool {
        guard owner == nil else { return false }
        owner = id
        self.giveWay = giveWay
        return true
    }

    /// Nobody holds it.
    var isFree: Bool { owner == nil }

    @discardableResult func release(_ id: UUID) -> Bool {
        guard owner == id else { return false }
        owner = nil
        giveWay = nil
        return true
    }
}
