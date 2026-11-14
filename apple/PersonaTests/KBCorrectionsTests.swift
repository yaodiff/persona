import Foundation
import Testing
@testable import Persona

/// Correcting an entry by hand (#6997): confirmed facts and removing a link, the same writes and the same rule for which
/// links can go as the web's (`web/src/lib/kb-relationships.ts`, cases from `Relationships.test.tsx`).
@MainActor
struct KBCorrectionsTests {
    private func entry(_ kind: String, _ id: String? = nil) -> KBEntry {
        KBEntry(kind: kind, id: id ?? (kind == "recordings" ? "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" : kind), name: "Example \(kind)", relevance: 0)
    }

    private func detail(_ e: KBEntry, _ meta: [String: JSONValue], links: [KBEntry] = []) -> KBDetail {
        KBDetail(kind: e.kind, id: e.id, path: "", meta: .object(meta), links: links)
    }

    private func list(_ s: [String]) -> JSONValue { .array(s.map { .string($0) }) }

    private func member(_ e: KBEntry, _ field: String) -> String { field == "items" ? "\(e.kind)/\(e.id)" : e.id }

    private func json(_ c: KBCorrection) throws -> String {
        let enc = JSONEncoder()
        enc.outputFormatting = .sortedKeys
        return String(decoding: try enc.encode(c), as: UTF8.self)
    }

    private struct Mapping {
        var a: KBEntry, b: KBEntry, af: String, bf: String, delta: String, reverse: Bool
    }

    private var mappings: [Mapping] {
        let project = entry("projects"), person = entry("people"), chat = entry("chats")
        return [
            Mapping(a: project, b: person, af: "people", bf: "projects", delta: "remove_people", reverse: false),
            Mapping(a: project, b: chat, af: "chats", bf: "projects", delta: "remove_chats", reverse: false),
            Mapping(a: chat, b: person, af: "people", bf: "chats", delta: "remove_people", reverse: false),
            Mapping(a: person, b: chat, af: "chats", bf: "people", delta: "remove_chats", reverse: false),
        ] + ["people", "chats", "docs", "meetings", "events", "recordings"].map { kind in
            Mapping(a: entry(kind), b: project, af: "projects", bf: kind == "people" || kind == "chats" ? kind : "items",
                    delta: "remove_projects", reverse: false)
        } + ["docs", "meetings", "events", "recordings"].map { kind in
            Mapping(a: project, b: entry(kind), af: "items", bf: "projects", delta: "remove_projects", reverse: true)
        }
    }

    @Test func aLinkHeldInTheEntrysListIsRemovedWithTheWebsUpdate() throws {
        for m in mappings {
            let d = detail(m.a, [m.af: list([member(m.b, m.af), "unrelated"])], links: [m.b])
            let r = try #require(KBUnlink.removal(d, name: m.a.name, linked: m.b), "\(m.a.kind) → \(m.b.kind)")
            let target = m.reverse ? m.b : m.a
            #expect(r.targetKind == target.kind && r.targetID == target.id)
            #expect(try json(r.update) == "{\"\(m.delta)\":[\"\(m.reverse ? m.a.id : m.b.id)\"]}")
            #expect(r.first.field.rawValue == m.af && r.second.field.rawValue == m.bf)
            #expect(r.first.member == member(m.b, m.af) && r.second.member == member(m.a, m.bf))
            #expect(r.question.contains(m.a.name) && r.question.contains(m.b.name))
        }
    }

    @Test func derivedMalformedOrUnsupportedLinksStayNavigationOnly() {
        let project = entry("projects"), person = entry("people"), chat = entry("chats")
        let cases: [(KBEntry, KBEntry, [String: JSONValue])] = [
            (project, person, [:]),
            (project, person, ["people": .string("people")]),
            (project, person, ["people": .array([.string(person.id), .number(7)])]),
            (project, person, ["people": list(["someone-else"])]),
            (project, entry("projects", "other-project"), ["projects": list(["other-project"])]),
            (entry("docs"), chat, ["where": .string(chat.id), "chats": list([chat.id])]),
            (entry("meetings"), person, ["people": list([person.id])]),
        ]
        for (a, b, meta) in cases {
            #expect(KBUnlink.removal(detail(a, meta, links: [b]), name: a.name, linked: b) == nil, "\(a.kind) → \(b.kind) \(meta)")
        }
    }

    @Test func requestsCarryTheSourceIDAndListsTheFolderName() throws {
        // The entry's own id is in meta.id; its folder (the detail's id) is the hub's SafeID of it.
        let a = KBDetail(kind: "people", id: "ou_a_b", path: "", meta: .object(["id": .string("ou:a b"), "projects": list(["p"])]))
        let r = try #require(KBUnlink.removal(a, name: "A", linked: KBEntry(kind: "projects", id: "p", name: "P", relevance: 0)))
        #expect(r.targetID == "ou:a b" && r.first.canonicalID == "ou_a_b" && r.second.member == "ou:a b")
        #expect(KBUnlink.canonicalID(kind: "docs", id: "._a b/c?._") == "a_b_c")
        #expect(KBUnlink.canonicalID(kind: "recordings", id: "AAAA-BB") == "aaaa-bb")
        #expect(KBUnlink.canonicalID(kind: "people", id: "张三@x.com") == "张三@x.com")
        // 120 bytes at most, never half a character: 41 three-byte characters keep 40.
        #expect(KBUnlink.canonicalID(kind: "chats", id: String(repeating: "群", count: 41)) == String(repeating: "群", count: 40))
    }

    @Test func removalIsConfirmedOnlyWhenBothListsAreClearAndTheUsersOwn() throws {
        let project = entry("projects"), person = entry("people")
        let d = detail(project, ["people": list([person.id])], links: [person])
        let r = try #require(KBUnlink.removal(d, name: project.name, linked: person))
        let done = [detail(project, ["people": list(["unrelated"]), "locked_fields": list(["name", "people"])]),
                    detail(person, ["locked_fields": list(["projects"])])]
        #expect(r.confirmed(done))
        // The other end still names it.
        #expect(!r.confirmed([done[0], detail(person, ["projects": list([project.id]), "locked_fields": list(["projects"])])]))
        // Removed but not protected (a hub from before the lock).
        #expect(!r.confirmed([detail(project, ["people": list([])]), done[1]]))
        // An extension still holds the list.
        #expect(!r.confirmed([done[0], detail(person, ["locked_fields": list(["projects"]),
                                                       "provenance": .object(["projects": .object(["by": .string("ext:x")])])])]))
        // Another entry was read back, or one end could not be read.
        #expect(!r.confirmed([done[0], detail(entry("people", "someone-else"), ["locked_fields": list(["projects"])])]))
        #expect(!r.confirmed([done[0], nil]))
    }

    @Test func confirmedFactsAreWrittenExplicitlyAndClearedWithAnEmptyString() throws {
        #expect(try json(KBCorrection(pinned: "")) == #"{"pinned":""}"#)
        #expect(try json(KBCorrection(pinned: "Works on Tuesdays")) == #"{"pinned":"Works on Tuesdays"}"#)
        #expect(try json(KBCorrection()) == "{}")
        let r = KBDetailResponse(detail: detail(entry("people"), ["name": .string("A"), "pinned": .string("old")]))
        #expect(KBFacts.text(r.detail.meta) == "old")
        let cleared = KBFacts.with("", in: r)
        #expect(KBFacts.text(cleared.detail.meta) == "" && cleared.detail.meta?["name"] == .string("A"))
        #expect(KBFacts.text(.object(["pinned": .number(3)])) == "" && KBFacts.text(nil) == "")
    }
}
