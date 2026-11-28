import Foundation
import Observation

/// The all-day recording consent (docs/assistant/day-recap.md「Retention and privacy」): before the first all-day
/// recording the iPhone shows the consent sheet, and the watch records only once the user agreed to its current
/// version. The agreement is kept with the sheet's version and when it was given, so a new version asks again. It
/// rarely changes, so it lives in user defaults. The watch learns it through `RecordingDayPhoneCapabilities`
/// (`recordingDay` 1, else 0); a recording already running goes on under the agreement it started with.
@MainActor @Observable final class RecordingDayConsent {
    static let shared = RecordingDayConsent()

    /// The sheet's version. Raise it when what the sheet says changes in a way the user must agree to again. 2: the
    /// hub's split retentions (#16444), whose text may now be kept forever by default.
    nonisolated static let version = 2
    nonisolated static let versionKey = "recording.day.consentVersion"
    nonisolated static let agreedAtKey = "recording.day.consentAt"

    /// The version the user agreed to; nil until they did.
    private(set) var agreedVersion: Int?
    private(set) var agreedAt: Date?

    /// The user agreed to this sheet (or to a newer one, on a build from before it).
    var agreed: Bool { (agreedVersion ?? 0) >= Self.version }

    /// Called after the agreement changed. `RecordingDayPhoneCapabilities` sets it, to tell the watch.
    @ObservationIgnored var didChange: (() -> Void)?
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let now: () -> Date

    init(defaults: UserDefaults = .standard, now: @escaping () -> Date = { .now }) {
        self.defaults = defaults
        self.now = now
        let version = defaults.integer(forKey: Self.versionKey)
        agreedVersion = version > 0 ? version : nil
        agreedAt = defaults.object(forKey: Self.agreedAtKey) as? Date
    }

    /// The user tapped Agree on the sheet as this build shows it.
    func agree() {
        guard !agreed else { return }
        let at = now()
        defaults.set(Self.version, forKey: Self.versionKey)
        defaults.set(at, forKey: Self.agreedAtKey)
        agreedVersion = Self.version
        agreedAt = at
        didChange?()
    }

    #if DEBUG
    /// Samples and UI tests start from a phone whose user has not agreed yet.
    func forget() {
        guard agreedVersion != nil || agreedAt != nil else { return }
        defaults.removeObject(forKey: Self.versionKey)
        defaults.removeObject(forKey: Self.agreedAtKey)
        agreedVersion = nil
        agreedAt = nil
        didChange?()
    }
    #endif
}

/// What the phone tells the watch about all-day recording (docs/assistant/day-recap.md「Watch and phone capabilities」):
/// `recordingDay` (1 once the user agreed, else 0), `recordingDayHub` (the hub lists `recording_day`) and
/// `recordingDayKeepDays` (`min(7, audio_days)`, 7 until the phone knows and when audio is forever). Pushed when the receiver starts (WatchSync
/// keeps it and sends it at every activation) and whenever one of them changes; neither goes through the hub. It also
/// hands `audio_days` to the day store, whose audio deadline counts it.
@MainActor final class RecordingDayPhoneCapabilities {
    static let shared = RecordingDayPhoneCapabilities()

    /// The hub lists `recording_day`, as its last `hello` said.
    private(set) var hub: Bool
    /// The hub's `audio_days` once reconciliation said it (0 is forever).
    private(set) var audioDays: Int?

    private let consent: RecordingDayConsent
    private let store: RecordingDayStore
    private let send: (RecordingDayCapabilities) -> Void
    private var sent: RecordingDayCapabilities?

    /// - Parameters:
    ///   - hub, audioDays: what the phone knew at its last run (the hub flag cached from its last `hello`, and
    ///     `RecordingSync`'s stored retention), for a launch in the background before any hub answered.
    ///   - send: WatchSync's push of the application context keys.
    init(consent: RecordingDayConsent = .shared, store: RecordingDayStore = .shared,
         hub: Bool = RecordingDayDefaults.offered([]), audioDays: Int? = RecordingDayPhoneCapabilities.storedAudioDays(),
         send: @escaping (RecordingDayCapabilities) -> Void = {
             WatchSync.shared.pushRecordingDay(consent: $0.consent, hub: $0.hub, keepDays: $0.keepDays)
         }) {
        self.consent = consent
        self.store = store
        self.hub = hub
        self.audioDays = audioDays
        self.send = send
        store.useAudioDays(audioDays)
        consent.didChange = { [weak self] in self?.push() }
    }

    /// What the watch is told now.
    var current: RecordingDayCapabilities {
        RecordingDayCapabilities(consent: consent.agreed, hub: hub,
                                 keepDays: RecordingDayContract.keepDays(audioDays: audioDays))
    }

    /// At the receiver's start: what the watch gets once the session activates.
    func start() {
        push()
    }

    /// The hub's `hello` said whether it lists `recording_day` (`AppModel`).
    func hubChanged(offered: Bool) {
        hub = offered
        push()
    }

    /// Reconciliation learned the hub's `audio_days` (`RecordingSync`); nil while unknown, 0 forever.
    func audioDaysChanged(_ days: Int?) {
        audioDays = days
        store.useAudioDays(days)
        push()
    }

    /// The retention `RecordingSync` stored at its last reconciliation; nil before the hub ever said it.
    static func storedAudioDays() -> Int? {
        let sync = RecordingSync.shared
        return sync.store.daysKnown ? sync.audioRetentionDays : nil
    }

    /// Sends what changed since the last push (WatchSync also skips a context that already says it).
    private func push() {
        let next = current
        guard next != sent else { return }
        sent = next
        send(next)
    }
}
