import Foundation
import Testing
@testable import Persona
import PersonaWire

struct DemoKnowledgeSearchTests {
    private func entry(_ id: String, _ name: String, _ summary: String, tags: [String] = []) -> JSONValue {
        ["kind": "projects", "id": .string(id), "name": .string(name), "one_line": .string(summary),
         "relevance": 80, "tags": .array(tags.map(JSONValue.string))]
    }

    @Test func searchesNamesSummariesAndTagsWithTheRealWireShape() throws {
        let kb: JSONValue = ["projects": [entry("notes", "Résumé notes", "A release checklist", tags: ["sample"])]]
        struct Response: Decodable { var results: [KBResult]; var hits: [KBHit] }
        for query in ["resume", "RELEASE", "sample"] {
            let json = DemoKnowledgeSearch.answer(query, kb: kb)
            let decoded = try JSON.decoder.decode(Response.self, from: JSON.encoder.encode(json))
            #expect(decoded.results.count == 1)
            #expect(decoded.results[0].entry.id == "notes")
            #expect(decoded.results[0].count == 1)
            #expect(decoded.hits.count == 1)
            #expect(decoded.hits[0].entry?.id == "notes")
            #expect(decoded.results[0].inName == (query == "resume"))
        }
    }

    @Test func emptyMissingAndUnknownSearchesStayEmpty() {
        let kb: JSONValue = ["projects": [entry("notes", "Sample notes", "A release checklist")]]
        for query in ["", "   ", "not-present", String(repeating: "x", count: 201)] {
            #expect(DemoKnowledgeSearch.answer(query, kb: kb)["results"]?.array?.isEmpty == true)
        }
        #expect(DemoKnowledgeSearch.answer("sample", kb: nil)["hits"]?.array?.isEmpty == true)
    }

    @Test func resultCountIsBoundedAndStable() {
        let kb: JSONValue = ["projects": .array((0..<30).map { entry("p\($0)", "Sample \($0)", "Sample summary") })]
        let first = DemoKnowledgeSearch.answer("sample", kb: kb)
        #expect(first == DemoKnowledgeSearch.answer("sample", kb: kb))
        #expect(first["results"]?.array?.count == DemoKnowledgeSearch.resultLimit)
        #expect(first["hits"]?.array?.count == DemoKnowledgeSearch.resultLimit * 2)
    }
}
