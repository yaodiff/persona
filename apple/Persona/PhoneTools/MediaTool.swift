import Foundation
#if os(iOS)
import MusicKit
#endif
import PersonaWire

/// Music on the iPhone (#2284): Apple Music plays through the system player (MusicKit), which really plays; Spotify and
/// YouTube are only opened at a search, and the result says opened, not playing.
enum MediaTool {
    static var tools: [PhoneTool] {
        #if os(iOS)
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_play",
                    description: """
                    Search Apple Music and play the best match in the Music app on the iPhone: a song (default), an \
                    album or a playlist. Needs an Apple Music subscription to play from the catalog; without one the \
                    tool says so. Returns what was queued and whether it is playing.
                    """,
                    inputSchema: Schema.object([
                        "query": Schema.string("What to play, e.g. a song and artist, an album, or a mood such as focus coding."),
                        "kind": Schema.string("song (default), album or playlist.", values: ["song", "album", "playlist"]),
                    ], required: ["query"])
                ),
                source: .media,
                run: { args in try await play(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_control",
                    description: """
                    Control the Music app's player on the iPhone: play, pause, next, previous, or status (what is \
                    playing). Only Apple Music's player: other apps cannot be controlled.
                    """,
                    inputSchema: Schema.object([
                        "action": Schema.string("play, pause, next, previous or status.", values: ["play", "pause", "next", "previous", "status"]),
                    ], required: ["action"])
                ),
                source: .media,
                run: { args in try await control(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_music_create_playlist",
                    description: """
                    Create a playlist in the user's Apple Music library from catalog songs, then play it on the iPhone. \
                    Pass one search per song (title and artist); each search adds its best catalog match. The user \
                    approves the playlist on the phone before anything is created. Returns the playlist, the songs \
                    added, any searches that found nothing, and whether it is playing. Needs an Apple Music \
                    subscription that can play the catalog.
                    """,
                    inputSchema: Schema.object([
                        "name": Schema.string("Playlist name."),
                        "description": Schema.string("Optional playlist description."),
                        "songs": Schema.array("One search per song: title and artist.", of: Schema.string("A song search, e.g. Midnight City M83.")),
                        "play": Schema.boolean("Play the new playlist now; default true."),
                    ], required: ["name", "songs"])
                ),
                source: .media,
                confirmation: { args in playlistConfirmation(args) },
                run: { args in try await createPlaylist(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_media_open",
                    description: """
                    Open a search in Spotify or YouTube (their app when installed, otherwise the web page). This only \
                    opens the search: nothing plays until the user picks something, so say it was opened, not that \
                    it is playing. Persona must be open on the iPhone.
                    """,
                    inputSchema: Schema.object([
                        "service": Schema.string("spotify or youtube.", values: ["spotify", "youtube"]),
                        "query": Schema.string("What to search for."),
                    ], required: ["service", "query"])
                ),
                source: .media,
                run: { args in try await openSearch(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_music_snapshot",
                    description: """
                    Read a snapshot of the iPhone's Music app as JSON: what is playing, recently played songs and \
                    library playlists. Pass the object unchanged to the Music extension's applemusic_iphone_sync \
                    tool so the Music page shows it. This only reads; it never changes the library or playback.
                    """,
                    inputSchema: Schema.object([
                        "recent_limit": Schema.integer("How many recently played songs, 1 to 50; default 20.", min: 1, max: 50),
                        "playlist_limit": Schema.integer("How many library playlists, 1 to 50; default 25.", min: 1, max: 50),
                    ])
                ),
                source: .media,
                run: { args in try await snapshot(args) }
            ),
        ]
        #else
        []
        #endif
    }

    /// The page a search opens in another service's app (a universal link: the app when installed, else the web).
    nonisolated static func searchURL(service: String, query: String) -> URL? {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-._~"))
        let term = query.addingPercentEncoding(withAllowedCharacters: allowed) ?? query
        switch service {
        case "spotify": return URL(string: "https://open.spotify.com/search/\(term)")
        case "youtube": return URL(string: "https://www.youtube.com/results?search_query=\(term)")
        default: return nil
        }
    }

    #if os(iOS)
    static func play(_ args: JSONValue) async throws -> [ToolContent] {
        guard let query = ToolArgs.string(args, "query") else { throw ToolFailure(.invalidArgument, "query is required") }
        let kind = ToolArgs.string(args, "kind") ?? "song"
        try MediaAccess.require()
        let subscription = try await MusicSubscription.current
        guard subscription.canPlayCatalogContent else {
            throw ToolFailure(.unavailable, "This Apple Account has no Apple Music subscription that can play the catalog, so nothing was played.")
        }
        let player = SystemMusicPlayer.shared
        let title: String
        let artist: String?
        switch kind {
        case "album":
            var request = MusicCatalogSearchRequest(term: query, types: [Album.self])
            request.limit = 1
            guard let album = try await request.response().albums.first else { throw notFound(query) }
            player.queue = [album]
            title = album.title
            artist = album.artistName
        case "playlist":
            var request = MusicCatalogSearchRequest(term: query, types: [Playlist.self])
            request.limit = 1
            guard let playlist = try await request.response().playlists.first else { throw notFound(query) }
            player.queue = [playlist]
            title = playlist.name
            artist = playlist.curatorName
        case "song":
            var request = MusicCatalogSearchRequest(term: query, types: [Song.self])
            request.limit = 1
            guard let song = try await request.response().songs.first else { throw notFound(query) }
            player.queue = [song]
            title = song.title
            artist = song.artistName
        default:
            throw ToolFailure(.invalidArgument, "kind must be song, album or playlist")
        }
        try await player.play()
        return Output.json(Output.object([
            ("kind", .string(kind)),
            ("title", .string(title)),
            ("artist", Output.opt(artist)),
            ("playback", .string(status(player.state.playbackStatus))),
            ("app", .string("Music")),
        ]))
    }

    /// The playlist a create call asks for, checked before the confirmation card and again before running.
    struct PlaylistDraft: Sendable {
        var name: String
        var note: String?
        var songs: [String]
        var play: Bool

        static func parse(_ args: JSONValue) throws -> PlaylistDraft {
            guard let rawName = ToolArgs.string(args, "name") else { throw ToolFailure(.invalidArgument, "name is required") }
            let name = rawName.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !name.isEmpty, name.count <= 100 else { throw ToolFailure(.invalidArgument, "name must be 1–100 characters") }
            guard case .array(let items)? = args["songs"] else { throw ToolFailure(.invalidArgument, "songs must be a list of searches") }
            let songs = items.compactMap { $0.string?.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
            guard !songs.isEmpty, songs.count <= 30 else { throw ToolFailure(.invalidArgument, "pass 1–30 songs") }
            guard songs.allSatisfy({ $0.count <= 200 }) else { throw ToolFailure(.invalidArgument, "a song search is at most 200 characters") }
            let note = ToolArgs.string(args, "description")?.trimmingCharacters(in: .whitespacesAndNewlines)
            let play = args["play"]?.bool ?? true
            return PlaylistDraft(name: name, note: (note?.isEmpty == false) ? note : nil, songs: songs, play: play)
        }
    }

    /// The confirmation card's words for a playlist write.
    static func playlistConfirmation(_ args: JSONValue) -> String {
        guard let draft = try? PlaylistDraft.parse(args) else {
            return String(localized: "Invalid parameters; nothing will be created.")
        }
        if draft.play {
            return String(localized: "Create the playlist “\(draft.name)” with \(draft.songs.count) songs and play it on the iPhone?")
        }
        return String(localized: "Create the playlist “\(draft.name)” with \(draft.songs.count) songs on the iPhone?")
    }

    /// Searches the catalog for each song, creates the playlist in the user's library and, unless play is off, starts it.
    static func createPlaylist(_ args: JSONValue) async throws -> [ToolContent] {
        let draft = try PlaylistDraft.parse(args)
        try MediaAccess.require()
        let subscription = try await MusicSubscription.current
        guard subscription.canPlayCatalogContent else {
            throw ToolFailure(.unavailable, "This Apple Account has no Apple Music subscription that can play the catalog, so no playlist was created.")
        }
        var found: [Song] = []
        var missing: [String] = []
        for query in draft.songs {
            var request = MusicCatalogSearchRequest(term: query, types: [Song.self])
            request.limit = 1
            if let song = try await request.response().songs.first {
                found.append(song)
            } else {
                missing.append(query)
            }
        }
        guard !found.isEmpty else {
            throw ToolFailure(.invalidArgument, "Apple Music found nothing for \(missing.joined(separator: "; ")).")
        }
        let playlist: Playlist
        do {
            playlist = try await MusicLibrary.shared.createPlaylist(name: draft.name, description: draft.note, items: found)
        } catch {
            throw ToolFailure(.unavailable, "Apple Music could not create the playlist: \(error.localizedDescription)")
        }
        var playback = "not_playing"
        if draft.play {
            let player = SystemMusicPlayer.shared
            player.queue = [playlist]
            do {
                try await player.play()
                playback = status(player.state.playbackStatus)
            } catch {
                playback = "failed: \(error.localizedDescription)"
            }
        }
        return Output.json(Output.object([
            ("playlist", Output.object([
                ("id", .string(playlist.id.rawValue)),
                ("name", .string(playlist.name)),
                ("url", Output.opt(playlist.url?.absoluteString)),
            ])),
            ("added", .array(found.map { song in
                Output.object([
                    ("title", .string(song.title)),
                    ("artist", Output.opt(song.artistName)),
                ])
            })),
            ("missing", .array(missing.map { .string($0) })),
            ("playback", .string(playback)),
        ]))
    }

    private static func notFound(_ query: String) -> ToolFailure {
        ToolFailure(.invalidArgument, "Apple Music found nothing for \"\(query)\"")
    }

    static func control(_ args: JSONValue) async throws -> [ToolContent] {
        guard let action = ToolArgs.string(args, "action") else { throw ToolFailure(.invalidArgument, "action is required") }
        try MediaAccess.require()
        let player = SystemMusicPlayer.shared
        switch action {
        case "play": try await player.play()
        case "pause": player.pause()
        case "next": try await player.skipToNextEntry()
        case "previous": try await player.skipToPreviousEntry()
        case "status": break
        default: throw ToolFailure(.invalidArgument, "action must be play, pause, next, previous or status")
        }
        let entry = player.queue.currentEntry
        return Output.json(Output.object([
            ("action", .string(action)),
            ("playback", .string(status(player.state.playbackStatus))),
            ("title", Output.opt(entry?.title)),
            ("subtitle", Output.opt(entry?.subtitle)),
        ]))
    }

    static func snapshot(_ args: JSONValue) async throws -> [ToolContent] {
        try MediaAccess.require()
        let recentLimit = ToolArgs.int(args, "recent_limit", fallback: 20, min: 1, max: 50)
        let playlistLimit = ToolArgs.int(args, "playlist_limit", fallback: 25, min: 1, max: 50)
        let player = SystemMusicPlayer.shared
        var now: JSONValue?
        if let entry = player.queue.currentEntry {
            var artist: String?
            var album: String?
            var durationMS: Int?
            func remember(_ artistName: String, _ albumTitle: String?, _ duration: TimeInterval?) {
                artist = artistName
                album = albumTitle
                durationMS = duration.map { Int(($0 * 1000).rounded()) }
            }
            if case .song(let song) = entry.item {
                remember(song.artistName, song.albumTitle, song.duration)
            } else if case .musicVideo(let video) = entry.item {
                remember(video.artistName, video.albumTitle, video.duration)
            } else if let song = entry.transientItem as? Song {
                remember(song.artistName, song.albumTitle, song.duration)
            } else if let video = entry.transientItem as? MusicVideo {
                remember(video.artistName, video.albumTitle, video.duration)
            }
            now = Output.object([
                ("title", .string(entry.title)),
                ("artist", Output.opt(artist)),
                ("album", Output.opt(album)),
                ("state", .string(status(player.state.playbackStatus))),
                ("position_ms", .number((player.playbackTime * 1000).rounded())),
                ("duration_ms", Output.opt(durationMS)),
                ("artwork_url", Output.opt(entry.artwork?.url(width: 400, height: 400)?.absoluteString)),
            ])
        }
        var recent: [JSONValue] = []
        var recentError: String?
        do {
            var request = MusicRecentlyPlayedRequest<Song>()
            request.limit = recentLimit
            let response = try await request.response()
            for song in response.items { recent.append(songSnapshot(song)) }
        } catch {
            recentError = error.localizedDescription
        }
        var playlists: [JSONValue] = []
        var playlistError: String?
        do {
            var request = MusicLibraryRequest<Playlist>()
            request.limit = playlistLimit
            let response = try await request.response()
            for playlist in response.items {
                playlists.append(Output.object([
                    ("id", .string(playlist.id.rawValue)),
                    ("title", .string(playlist.name)),
                    ("description", Output.opt(playlist.standardDescription)),
                    ("curator", Output.opt(playlist.curatorName)),
                    ("artwork_url", Output.opt(playlist.artwork?.url(width: 400, height: 400)?.absoluteString)),
                    ("url", Output.opt(playlist.url?.absoluteString)),
                ]))
            }
        } catch {
            playlistError = error.localizedDescription
        }
        if now == nil && recent.isEmpty && playlists.isEmpty {
            throw ToolFailure(.unavailable, recentError ?? playlistError ?? "Apple Music returned no music.")
        }
        return Output.json(Output.object([
            ("synced_at", .string(ISO8601DateFormatter().string(from: Date()))),
            ("now_playing", now),
            ("recent", .array(recent)),
            ("playlists", .array(playlists)),
            ("errors", Output.opt([recentError, playlistError].compactMap { $0 }.joined(separator: "; "))),
        ]))
    }

    private static func songSnapshot(_ song: Song) -> JSONValue {
        Output.object([
            ("id", .string(song.id.rawValue)),
            ("title", .string(song.title)),
            ("artist", Output.opt(song.artistName)),
            ("album", Output.opt(song.albumTitle)),
            ("duration_ms", Output.opt(song.duration.map { Int($0 * 1000) })),
            ("artwork_url", Output.opt(song.artwork?.url(width: 400, height: 400)?.absoluteString)),
            ("url", Output.opt(song.url?.absoluteString)),
            ("isrc", Output.opt(song.isrc)),
            ("played_at", Output.opt(song.lastPlayedDate.map { ISO8601DateFormatter().string(from: $0) })),
        ])
    }

    static func openSearch(_ args: JSONValue) async throws -> [ToolContent] {
        guard let service = ToolArgs.string(args, "service"), let query = ToolArgs.string(args, "query") else {
            throw ToolFailure(.invalidArgument, "service and query are required")
        }
        guard let url = searchURL(service: service, query: query) else {
            throw ToolFailure(.invalidArgument, "service must be spotify or youtube")
        }
        try Foreground.require("open \(service)")
        guard await Foreground.open(url) else { throw ToolFailure(.unavailable, "iOS did not open \(service).") }
        return Output.json(Output.object([
            ("opened", .string(service)),
            ("query", .string(query)),
            ("playing", .bool(false)),
            ("note", .string("Only the search was opened; nothing plays until the user picks something.")),
        ]))
    }

    private static func status(_ status: MusicPlayer.PlaybackStatus) -> String {
        switch status {
        case .playing: "playing"
        case .paused: "paused"
        case .stopped: "stopped"
        case .interrupted: "interrupted"
        case .seekingForward, .seekingBackward: "seeking"
        @unknown default: "unknown"
        }
    }
    #endif
}

#if os(iOS)
/// Apple Music's permission (`NSAppleMusicUsageDescription`). Catalog requests also need the MusicKit service on the
/// App ID (docs/apple/phone-tools.md).
enum MediaAccess {
    static func status() -> PhoneAuthStatus {
        switch MusicAuthorization.currentStatus {
        case .authorized: .granted
        case .denied, .restricted: .denied
        case .notDetermined: .notDetermined
        @unknown default: .notDetermined
        }
    }

    static func request() async -> PhoneAuthStatus {
        _ = await MusicAuthorization.request()
        return status()
    }

    static func require() throws {
        switch status() {
        case .granted, .limited, .notNeeded: return
        case .denied: throw ToolFailure(.denied, "Apple Music")
        case .notDetermined: throw ToolFailure(.denied, "Apple Music access has not been requested yet")
        }
    }
}
#endif
