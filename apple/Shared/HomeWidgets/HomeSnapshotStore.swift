#if os(iOS)
import Foundation
import OSLog

/// Where the app leaves the widgets' snapshot: `Widgets/snapshot.json` in the App Group container it shares with the
/// widget extension (the one `FleetLookStore` uses; docs/apple/home-widgets.md「Data: the snapshot」). Readable on the
/// Lock Screen once the phone was unlocked after boot. Tests pass a directory of their own.
nonisolated struct HomeSnapshotStore: Sendable {
    static let fileName = "snapshot.json"
    static let log = Logger(subsystem: "persona", category: "widgets")

    /// `Widgets/` in the container; nil when this bundle is not signed with the group (nothing is written, and the
    /// widgets ask to open Persona).
    var root: URL?

    static var shared: HomeSnapshotStore {
        HomeSnapshotStore(root: FleetLookStore.container?.appending(path: "Widgets", directoryHint: .isDirectory))
    }

    /// The snapshot, or nil when there is none, it cannot be read, or another version wrote it.
    func read() -> HomeSnapshot? {
        guard let url = root?.appending(path: Self.fileName), let data = try? Data(contentsOf: url) else { return nil }
        guard let snapshot = try? JSONDecoder().decode(HomeSnapshot.self, from: data), snapshot.version == HomeSnapshot.version else {
            Self.log.notice("widget snapshot unreadable (\(data.count) bytes): read as none")
            return nil
        }
        return snapshot
    }

    /// Writes the encoded snapshot whole (atomically), protected until the first unlock after boot.
    func write(_ data: Data) throws {
        guard let root else { throw CocoaError(.fileNoSuchFile) }
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try data.write(to: root.appending(path: Self.fileName), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }
}
#endif
