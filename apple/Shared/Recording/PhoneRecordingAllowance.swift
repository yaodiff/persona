import Foundation

/// Whether the iPhone's hub lets new recordings be made, as the phone tells the Apple Watch (#15214,
/// docs/apple/recording.md「Apple Watch」). The watch never talks to the hub about modules: the phone puts its own
/// answer (`RecordingModule.allowance`, the hub's `module.recording` flag) into the WatchConnectivity application
/// context under `contextKey`, and the watch keeps the word in its defaults.
///
/// A closed set with an explicit unknown. Only `off` takes anything away: a phone from before the key (absent), a word
/// a newer phone writes that this build doesn't know, and a value that is no string all leave the watch as it was.
/// Foundation only: the watch's widget extension compiles this file for the Record complication.
///
/// The same word reaches the iPhone's own extensions (#15214): the Home Screen and Lock Screen widgets and the Control
/// read it from the widgets' snapshot (`HomeSnapshot.recordingModule`), the recording's Live Activity from its content
/// state (`RecordingActivityAttributes.ContentState.module`). There too absent means allowed, and it is decoded
/// leniently: a word or a value this build doesn't know is unknown, never a failed file or state.
nonisolated enum PhoneRecordingAllowance: Hashable, Sendable, Codable {
    /// The hub takes recordings, or says nothing about modules; also what an absent key means.
    case allowed
    /// The hub's recording module is off: a recording made now could never be synced.
    case off
    /// A word this build doesn't know, as the phone wrote it. Treated as allowed.
    case unknown(String)

    /// The application context key (iPhone → watch). A string: `allowedWire` or `offWire`.
    static let contextKey = "recordingModule"
    /// Where the watch keeps the word: its own defaults (its views read them and redraw when the word changes) and its
    /// App Group's (the Record complication). Absent until a phone says it.
    static let storeKey = "recording.phoneModule"
    static let allowedWire = "allowed"
    static let offWire = "off"
    /// The Record complication's WidgetKit kind, so the watch app reloads only its timeline when the word changes.
    static let recordWidgetKind = "persona.watch.meeting-recording"

    /// From the stored or sent word; nil (absent) is allowed.
    init(wire: String?) {
        switch wire {
        case nil, Self.allowedWire: self = .allowed
        case Self.offWire: self = .off
        case let other?: self = .unknown(other)
        }
    }

    /// From an application context's value: absent is allowed, a value that is no string is unknown.
    init(context value: Any?) {
        guard let value else {
            self = .allowed
            return
        }
        self.init(wire: value as? String ?? String(describing: value))
    }

    /// The word as sent and stored.
    var wire: String {
        switch self {
        case .allowed: Self.allowedWire
        case .off: Self.offWire
        case .unknown(let word): word
        }
    }

    /// As a single word (`wire`). Read leniently: a value that is no string is an unknown word, never a failed decode.
    init(from decoder: Decoder) throws {
        let container = try? decoder.singleValueContainer()
        if let word = try? container?.decode(String.self) { self.init(wire: word) } else { self = .unknown("") }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(wire)
    }

    /// New recordings, and marks in a running one, are offered: everything but `off`.
    var offersWrites: Bool { self != .off }

    /// What the watch last heard, from the defaults it keeps it in.
    static func stored(in defaults: UserDefaults? = .standard) -> PhoneRecordingAllowance {
        PhoneRecordingAllowance(wire: defaults?.string(forKey: storeKey))
    }

    /// The watch App Group's defaults, shared by the watch app and its widget extension; nil without the group (an
    /// unsigned build), where the complication stays as it always was.
    static func groupDefaults() -> UserDefaults? {
        guard let group = Bundle.main.object(forInfoDictionaryKey: "PersonaAppGroup") as? String, !group.isEmpty else { return nil }
        return UserDefaults(suiteName: group)
    }
}

/// What the watch's recording pages offer for an allowance (#15214). With the hub's recording module off nothing new
/// starts and nothing is marked; what stops the microphone or removes the user's own audio always stays, so a recording
/// that was running when the module went off can be paused, ended and saved: its file stays on the watch and goes to
/// the iPhone as before.
nonisolated struct WatchRecordingOffers: Equatable, Sendable {
    /// Record with its type and language, Rolling, All-Day Recording's Start, the complication's one-tap start and a
    /// start the phone asks for.
    let starts: Bool
    /// Bookmark and Mark Meeting in a recording that runs: they add to what the hub would have to take.
    let marks: Bool
    /// The one line saying meeting recording is off on this hub.
    let saysOff: Bool
    /// Pause and Continue of a recording that runs, Stop & Save, Stop for Today, and erasing the last 10 minutes:
    /// always. They stop the microphone or delete the user's own audio and send nothing new to the hub.
    let stopsAndErases = true

    init(_ recording: PhoneRecordingAllowance) {
        starts = recording.offersWrites
        marks = recording.offersWrites
        saysOff = !recording.offersWrites
    }
}
