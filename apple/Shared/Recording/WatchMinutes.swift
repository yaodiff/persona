import Foundation
import PersonaWire

/// Meetings' minutes as the watch shows them (docs/apple/recording.md「Apple Watch」): a short version of the hub's minutes
/// (key points, decisions, action items) the phone relays in the WatchConnectivity application context. The watch never
/// talks to the hub for them.
nonisolated struct WatchMinutes: Codable, Hashable, Sendable {
    nonisolated struct Meeting: Codable, Hashable, Sendable, Identifiable {
        enum State: String, Codable, Sendable { case writing, ready, failed }
        var id: UUID
        var title: String
        var date: Date
        var state: State
        var points: [String] = []
        var decisions: [String] = []
        var actions: [String] = []
    }

    var meetings: [Meeting] = []

    /// The application context key.
    static let key = "personaMinutes"
    /// The newest meetings only, a few items each, each cut short: the context is meant for state, and it all reads on a wrist.
    static let maxMeetings = 8
    static let maxItems = 4
    static let maxChars = 120

    /// The short version of a recording's minutes (nil: not written yet, or `failed`).
    static func meeting(id: UUID, title: String, date: Date, minutes: RecordingMinutes?, failed: Bool = false) -> Meeting {
        guard let minutes else { return Meeting(id: id, title: title, date: date, state: failed ? .failed : .writing) }
        func cut(_ s: String) -> String { s.count > maxChars ? String(s.prefix(maxChars - 1)) + "…" : s }
        func lines(_ items: [RecordingMinutes.Item], _ line: (RecordingMinutes.Item) -> String) -> [String] {
            items.prefix(maxItems).map { cut(line($0)) }
        }
        return Meeting(id: id, title: (minutes.title?.isEmpty == false ? minutes.title : nil) ?? title, date: date, state: .ready,
                       points: lines(minutes.points) { $0.text },
                       decisions: lines(minutes.decisions) { $0.text },
                       actions: lines(minutes.actions) { item in
                           [item.who.map { $0 + ": " }, item.text, item.by.map { " · " + $0 }].compactMap { $0 }.joined()
                       })
    }

    /// Newest first, at most `maxMeetings`.
    init(meetings: [Meeting] = []) {
        self.meetings = Array(meetings.sorted { $0.date > $1.date }.prefix(Self.maxMeetings))
    }

    var data: Data? { try? JSON.encoder.encode(self) }

    init?(data: Data?) {
        guard let data, let value = try? JSON.decoder.decode(Self.self, from: data) else { return nil }
        self = value
    }
}
