import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct SessionRenameFormTests {
    private func session(host: String, agent: String) throws -> Session {
        let fields: [String: Any] = ["machine_id": "fixture", "session_id": "agent", "title": "Conversation",
                                    "cwd": "/code/project", "status": "idle", "pane_id": host + ":pane",
                                    "slot_name": "Tab", "agent": agent]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    @Test func orcaAgentsHaveOneNameAndNeverSendADuplicateTabRename() throws {
        for agent in ["codex", "claude"] {
            let session = try session(host: "orca", agent: agent)
            #expect(!SessionRenameForm.showsTab(session))
            let body = SessionRenameForm.body(session, tab: "Changed tab", title: "New conversation")
            let json = try #require(JSONSerialization.jsonObject(with: JSONEncoder().encode(body)) as? [String: String])
            #expect(json == ["title": "New conversation"])
        }
    }

    @Test func legacyHerdrClaudeKeepsItsTwoIndependentNames() throws {
        let session = try session(host: "herdr", agent: "claude")
        #expect(SessionRenameForm.showsTab(session))
        #expect(SessionRenameForm.tabField(session) == "标签名（herdr tab）")
        #expect(SessionRenameForm.tabNote(session).contains("herdr"))
        let body = SessionRenameForm.body(session, tab: "New tab", title: session.title)
        #expect(body.tabName == "New tab" && body.title == nil)
        let unchanged = SessionRenameForm.body(session, tab: "Tab", title: session.title)
        #expect(unchanged.tabName == nil && unchanged.title == nil)
    }

    /// Tern renames its tabs as herdr does (#2154): a Claude there edits both names, and the dialog names Tern.
    @Test func ternClaudeRenamesItsTernTab() throws {
        let session = try session(host: "tern", agent: "claude")
        #expect(session.hostKind == "tern")
        #expect(SessionRenameForm.showsTab(session))
        #expect(SessionRenameForm.tabField(session) == "标签名（Tern tab）")
        #expect(SessionRenameForm.tabNote(session).contains("Tern") && !SessionRenameForm.tabNote(session).contains("herdr"))
        let body = SessionRenameForm.body(session, tab: "deploy", title: "Ship it")
        #expect(body.tabName == "deploy" && body.title == "Ship it")
        let codex = try self.session(host: "tern", agent: "codex")
        #expect(!SessionRenameForm.showsTab(codex), "only Claude's conversation and tab are named apart")
        #expect(SessionRenameForm.body(codex, tab: "Changed tab", title: "New").tabName == nil)
    }

    @Test func aSpaceRenameSaysWhatElseItRenames() throws {
        func group(_ kind: String?) throws -> SpaceGroup {
            var fields: [String: Any] = ["machine_id": "fixture", "session_id": "agent", "title": "Conversation", "cwd": "/code/project",
                                         "status": "idle", "space_id": "s1", "space_name": "project"]
            if let kind { fields["space_kind"] = kind }
            let s = try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
            return SpaceGroup(id: "fixture/s1", machineID: "fixture", spaceID: "s1", name: "project", index: 0, sessions: [s])
        }
        #expect(SpaceRenameForm.note(try group("tern")) == "同时改 Tern 里这个 session 的名字。")
        #expect(SpaceRenameForm.note(try group("herdr")) == "同时改 herdr 里这个 workspace 的名字。")
        #expect(SpaceRenameForm.note(try group(nil)) == SpaceRenameForm.note(try group("herdr")), "a node that does not say is herdr")
        #expect(SpaceRenameForm.note(nil) == SpaceRenameForm.note(try group("herdr")))
    }
}
