import Foundation
import SwiftUI

/// Finding a Persona chat by what was said in it (#6284, docs/protocol/protocol.md "Assistant chat search (#6282)"): the
/// current assistant's `GET /api/v1/assistant/search`, asked while the sidebar's search (or the open iPhone Duo's full
/// search) has words in it, 200 ms after the last key, each query cancelling the one before. The local matches (titles,
/// last messages: `AssistantSidebarSearch`) show at once and the manager's results merge in; without the hub's
/// `assistant_search` feature, or on a 404 from the manager (an older build), the local matches are the whole answer.
nonisolated struct AssistantSearchAnswer: Decodable, Equatable, Sendable {
    struct Hit: Decodable, Equatable, Hashable, Sendable {
        var itemID: String
        var seq: Int
        var role: String
        var snippet: String
        /// `[start, end)` of each match in the snippet, UTF-16.
        var ranges: [[Int]]

        enum CodingKeys: String, CodingKey { case itemID = "item_id", seq, role, snippet, ranges }
    }

    struct Result: Decodable, Equatable, Sendable {
        var sessionID: String
        var machineID: String
        var title: String
        var archived: Bool?
        var titleMatch: Bool?
        var hits: [Hit]

        enum CodingKeys: String, CodingKey {
            case sessionID = "session_id", machineID = "machine_id", title, archived, titleMatch = "title_match", hits
        }
    }

    var results: [Result]
    /// Some chats are not in the manager's index yet, or it could not be searched this time.
    var partial: Bool
}

/// A search's rows: the manager's results first (each with its newest hit), then the local matches it did not return.
nonisolated enum AssistantSearchMerge {
    struct Row: Identifiable, Equatable {
        var session: Session
        /// The message it was found by; nil for a title or a local match (it opens as usual).
        var hit: AssistantSearchAnswer.Hit?
        var id: SessionKey { session.key }
    }

    /// `known`: every conversation the app has (the sidebar's and the archived ones); a result it does not have is left
    /// out, as the sidebar can open only what it knows.
    static func rows(local: [Session], answer: AssistantSearchAnswer?, known: [Session]) -> [Row] {
        guard let answer else { return local.map { Row(session: $0) } }
        var byID: [String: Session] = [:]
        for s in known where byID[s.sessionID] == nil { byID[s.sessionID] = s }
        var out: [Row] = []
        var seen = Set<String>()
        for r in answer.results {
            guard let s = byID[r.sessionID], seen.insert(r.sessionID).inserted else { continue }
            out.append(Row(session: s, hit: r.titleMatch == true ? nil : r.hits.first))
        }
        for s in local where seen.insert(s.sessionID).inserted { out.append(Row(session: s)) }
        return out
    }

    /// Characters kept before the first match. A row's snippet is one line of about 17 characters on a phone, and the
    /// manager's snippet can start up to 40 characters before the match, which would cut the match itself off.
    static let lead = 8

    /// `lead` at a text size: fewer as the text grows, so the match stays on the line (the sidebar's rows stop growing
    /// at xxxLarge, #6813).
    static func lead(for size: DynamicTypeSize) -> Int {
        if size <= .large { return lead }
        if size <= .xxLarge { return 5 }
        return 2
    }

    /// The snippet with its matches strongly emphasized (the ranges clipped to it, in order), starting `lead`
    /// characters before the first match.
    static func snippet(_ hit: AssistantSearchAnswer.Hit, lead: Int = lead) -> AttributedString {
        let (shown, ranges) = led(hit.snippet, hit.ranges, lead: lead)
        var text = AttributedString(shown)
        let utf16 = shown.utf16
        for r in ranges where r.count == 2 {
            let lo = min(max(0, r[0]), utf16.count), hi = min(max(lo, r[1]), utf16.count)
            guard lo < hi, let a = String.Index(utf16.index(utf16.startIndex, offsetBy: lo), within: shown),
                  let b = String.Index(utf16.index(utf16.startIndex, offsetBy: hi), within: shown),
                  let range = Range(a..<b, in: text) else { continue }
            text[range].inlinePresentationIntent = .stronglyEmphasized
        }
        return text
    }

    /// `s` from `lead` characters before its first match, "…" in front when cut, with the ranges (UTF-16) moved to match.
    static func led(_ s: String, _ ranges: [[Int]], lead: Int = lead) -> (String, [[Int]]) {
        let utf16 = s.utf16
        guard let first = ranges.filter({ $0.count == 2 }).map({ $0[0] }).min(), first > 0, first <= utf16.count,
              let at = String.Index(utf16.index(utf16.startIndex, offsetBy: first), within: s) else { return (s, ranges) }
        let before = s[..<at]
        guard before.count > lead + 1 else { return (s, ranges) }
        let from = before.index(before.endIndex, offsetBy: -lead)
        let shift = utf16.distance(from: utf16.startIndex, to: from) - 1 // "…" is one UTF-16 unit
        return ("…" + s[from...], ranges.map { $0.count == 2 ? [$0[0] - shift, $0[1] - shift] : $0 })
    }
}

/// A found row's second line (#6284): the sentence that matched, its match bold, from `AssistantSearchMerge.lead(for:)`
/// characters before it at this text size. Its own view, so only it reads the text size.
struct SearchSaidLine: View {
    @Environment(\.dynamicTypeSize) private var size
    let hit: AssistantSearchAnswer.Hit

    var body: some View {
        Text(AssistantSearchMerge.snippet(hit, lead: AssistantSearchMerge.lead(for: size)))
            .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
    }
}

/// The search's state for one sidebar: the answer for the query it was asked for.
@MainActor @Observable final class AssistantChatSearch {
    private(set) var answer: AssistantSearchAnswer?
    private(set) var answered = ""
    @ObservationIgnored private var task: Task<Void, Never>?
    /// The hubs and managers that answered 404 (a build from before the search): not asked again this run.
    @ObservationIgnored private var unsupported = Set<String>()

    static let feature = "assistant_search"
    static let settle: Duration = .milliseconds(200)

    /// The answer when it is for `query`; nil while it is being asked, or never.
    func answer(for query: String) -> AssistantSearchAnswer? {
        answered == Self.trimmed(query) && !answered.isEmpty ? answer : nil
    }

    /// Asks for `query` once the keys settle (`on`: the hub lists the feature); a blank query or off forgets the answer.
    func search(_ query: String, api: APIClient?, on: Bool) {
        task?.cancel()
        let q = Self.trimmed(query)
        guard on, let api, !q.isEmpty else {
            answer = nil
            answered = ""
            return
        }
        let key = api.config.url.absoluteString + "|" + (api.managerID ?? api.shownManager ?? "")
        guard !unsupported.contains(key) else { return }
        task = Task { [weak self] in
            try? await Task.sleep(for: Self.settle)
            guard !Task.isCancelled else { return }
            do {
                let a: AssistantSearchAnswer = try await api.get("/api/v1/assistant/search", query: ["q": q, "limit": "30"])
                guard !Task.isCancelled else { return }
                self?.answer = a
                self?.answered = q
            } catch let e as APIError where e.status == 404 {
                self?.unsupported.insert(key)
            } catch {
                // offline or failed: the local matches stay the answer
            }
        }
    }

    nonisolated static func trimmed(_ s: String) -> String { s.trimmingCharacters(in: .whitespacesAndNewlines) }
}
