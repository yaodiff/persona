import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The composer's @ list (#2319): the token before the caret, what a pick leaves in the draft, and the rows in the
/// web's order (web/src/lib/mentions.test.ts covers the same rules).
@MainActor
struct MentionMenuTests {
    private func session(_ id: String, title: String, cwd: String = "/Users/me/code/app", agent: String = "claude",
                         assistant: Bool = false, updated: TimeInterval, last: String = "") -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(title)","cwd":"\(cwd)","status":"idle","state":"idle",
         "agent":"\(agent)","assistant":\(assistant),"last_text":"\(last)",
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    private func tool(_ seq: Int, _ input: [String: JSONValue]) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSince1970: TimeInterval(seq)), role: "assistant", kind: .toolUse,
             text: nil, toolUseID: "t\(seq)", toolName: "Read", toolInput: .object(input), isError: nil, image: nil)
    }

    private func entry(_ name: String, dir: Bool = false) -> FileEntry { FileEntry(name: name, dir: dir, size: 0) }

    private func workspace(_ id: String, machine: String = "macbook", title: String, cwd: String, branch: String = "",
                           online: Bool = true, available: Bool = true, group: String = "") -> HostWorkspace {
        var w = HostWorkspace(machineID: machine, source: "herdr", sourceID: id, title: title, cwd: cwd,
                              branch: branch, online: online, available: available)
        w.group = group
        return w
    }

    private func workspaceValue(_ row: MentionRow) -> HostWorkspace? {
        if case .workspace(let w) = row.kind { return w }
        return nil
    }

    // MARK: - The token

    @Test func aTokenStartsAtTheStartOrAfterWhitespace() {
        #expect(MentionToken.at("@", caret: 1) == MentionToken(start: 0, end: 1, query: ""))
        #expect(MentionToken.at("look at @src", caret: 12) == MentionToken(start: 8, end: 12, query: "src"))
        #expect(MentionToken.at("first line\n@Read", caret: 16)?.query == "Read")
        #expect(MentionToken.at("看一下 @文件", caret: 7)?.query == "文件")
        // An e-mail address, a second @, or a space after the word: no list.
        #expect(MentionToken.at("me@example", caret: 10) == nil)
        #expect(MentionToken.at("@a@b", caret: 4) == nil)
        #expect(MentionToken.at("@src ", caret: 5) == nil)
        #expect(MentionToken.at("no mention", caret: 10) == nil)
    }

    @Test func theTokenIsWhereTheCaretIs() {
        // The caret right after the query, with more text after it.
        #expect(MentionToken.at("@src and more", caret: 4) == MentionToken(start: 0, end: 4, query: "src"))
        // Within the word: only what is before the caret is the query.
        #expect(MentionToken.at("@src", caret: 2)?.query == "s")
        // Offsets count UTF-16: an emoji before the token takes two.
        #expect(MentionToken.at("👋 @a", caret: 5) == MentionToken(start: 3, end: 5, query: "a"))
        // An offset inside the emoji is no caret, and one past the end is none either.
        #expect(MentionToken.at("👋", caret: 1) == nil)
        #expect(MentionToken.at("@a", caret: 9) == nil)
    }

    @Test func aSelectedRangeIsNotTyping() {
        let text = "see @src"
        #expect(MentionToken.at(text, selection: .cursor(utf16: 8))?.query == "src")
        #expect(MentionToken.at(text, selection: nil)?.query == "src") // none reported yet: the end
        let range = TextSelection(range: text.index(text.startIndex, offsetBy: 4)..<text.endIndex)
        #expect(MentionToken.at(text, selection: range) == nil)
    }

    @Test func markedTextHoldsTheList() {
        let view = UITextView(frame: CGRect(x: 0, y: 0, width: 300, height: 44))
        view.insertText("@")
        #expect(!TypedText.composing(view))
        view.setMarkedText("wen", selectedRange: NSRange(location: 3, length: 0))
        #expect(TypedText.composing(view))
        view.unmarkText()
        #expect(!TypedText.composing(view))
        #expect(!TypedText.composing(nil))
    }

    // MARK: - What a pick leaves

    @Test func aPickReplacesTheToken() {
        let token = MentionToken(start: 4, end: 9, query: "Prov")
        let file = token.replacing(in: "see @Prov now", with: "@WeatherWidget/Provider.swift ")
        #expect(file.text == "see @WeatherWidget/Provider.swift now") // the space after the token is not doubled
        #expect(file.caret == 34)
        let folder = token.replacing(in: "see @Prov", with: "@Sources/")
        #expect(folder.text == "see @Sources/")
        #expect(folder.caret == folder.text.utf16.count)
        // A session: the token goes, the quote carries it.
        let session = token.replacing(in: "see @Prov", with: "")
        #expect(session.text == "see ")
        #expect(session.caret == 4)
    }

    @Test func thePlusMenuTypesAnAtThatOpensTheList() {
        let empty = MentionToken.typing(in: "", caret: 0)
        #expect(empty.text == "@")
        #expect(MentionToken.at(empty.text, caret: empty.caret)?.query == "")
        let after = MentionToken.typing(in: "look at", caret: 7)
        #expect(after.text == "look at @")
        #expect(after.caret == 9)
        #expect(MentionToken.typing(in: "look ", caret: 5).text == "look @")
        let middle = MentionToken.typing(in: "foobar", caret: 3)
        #expect(middle.text == "foo @bar")
        #expect(MentionToken.at(middle.text, caret: middle.caret)?.query == "")
    }

    // MARK: - The rows

    @Test func theChatsFilesNewestFirstOnce() {
        let items = [tool(1, ["file_path": .string("/a/old.swift")]), tool(2, ["notebook_path": .string("/a/n.ipynb")]),
                     tool(3, ["file_path": .string("/a/old.swift")]), tool(4, ["pattern": .string("x")])]
        #expect(MentionRows.usedPaths(items) == ["/a/old.swift", "/a/n.ipynb"])
        let many = (1...20).map { tool($0, ["file_path": .string("/a/f\($0)")]) }
        #expect(MentionRows.usedPaths(many).count == 12)
        #expect(MentionRows.usedPaths(many).first == "/a/f20")
        // The prompt bar's file rows read the same paths.
        #expect(PromptItems.files(items, cwd: "/a").map(\.insert) == ["@old.swift ", "@n.ipynb "])
    }

    @Test func aSlashBrowsesThatFolder() {
        let cwd = "/Users/me/code/app/"
        #expect(MentionRows.browseTarget("src", cwd: cwd) == nil)
        let rel = MentionRows.browseTarget("src/Vi", cwd: cwd)
        #expect(rel?.dir == "/Users/me/code/app/src")
        #expect(rel?.typed == "src/")
        #expect(rel?.filter == "Vi")
        #expect(MentionRows.browseTarget("/", cwd: cwd)?.dir == "/")
        #expect(MentionRows.browseTarget("/etc/ho", cwd: cwd)?.dir == "/etc")
        #expect(MentionRows.browseTarget("~/", cwd: nil)?.dir == "~")
        #expect(MentionRows.browseTarget("~/code/", cwd: nil)?.dir == "~/code")
        // A relative path with no folder to start from (a new conversation): nothing to list.
        #expect(MentionRows.browseTarget("src/", cwd: nil) == nil)
        #expect(MentionRows.folder(for: "", cwd: cwd) == cwd)
        #expect(MentionRows.folder(for: "src/", cwd: cwd) == "/Users/me/code/app/src")
        #expect(MentionRows.folder(for: "", cwd: "") == nil)
    }

    @Test func filesThenTheFolderThenSessions() {
        let cwd = "/Users/me/code/app"
        let used = ["/Users/me/code/app/Sources/Store.swift", "/Users/me/code/app/README.md", "/tmp/out.log"]
        let listing = [entry("README.md"), entry(".gitignore"), entry("Sources", dir: true), entry("Package.swift"), entry("docs", dir: true)]
        let sessions = [session("s1", title: "Blog", cwd: "/Users/me/code/blog", updated: 5),
                        session("self", title: "This chat", updated: 9),
                        session("pa", title: "Persona", assistant: true, updated: 8),
                        session("s2", title: "Orders", cwd: "/home/me/shop", agent: "codex", updated: 7)]
        let rows = MentionRows.rows(query: "", cwd: cwd, used: used, listing: listing, sessions: sessions,
                                    except: SessionKey(machineID: "m", sessionID: "self"))
        #expect(rows.map(\.name) == ["Store.swift", "README.md", "out.log", "Sources/", "docs/", "Package.swift", "Orders", "Blog"])
        let usedNote = String(localized: "Used in this session"), folderNote = String(localized: "Folder"), topNote = String(localized: "Session folder")
        #expect(rows.map(\.detail) == ["Sources", usedNote, "/tmp", folderNote, folderNote, topNote, "Codex · shop", "Claude · blog"])
        #expect(rows[0].kind == .file(insert: "@Sources/Store.swift ", folder: false))
        #expect(rows[2].kind == .file(insert: "@/tmp/out.log ", folder: false))
        #expect(rows[3].kind == .file(insert: "@Sources/", folder: true))
        #expect(rows[5].kind == .file(insert: "@Package.swift ", folder: false))
        #expect(rows[6].kind == .session(SessionKey(machineID: "m", sessionID: "s2")))
    }

    @Test func typingFilters() {
        let cwd = "/Users/me/code/app"
        let listing = [entry("README.md"), entry("Sources", dir: true), entry("docs", dir: true), entry("resources", dir: true)]
        let sessions = [session("s1", title: "Blog", cwd: "/Users/me/code/blog", updated: 5),
                        session("s2", title: "Orders", cwd: "/home/me/shop", agent: "codex", updated: 7)]
        // Names that start with the query first.
        #expect(MentionRows.rows(query: "so", cwd: cwd, used: [], listing: listing, sessions: [], except: nil).map(\.name)
                == ["Sources/", "resources/"])
        // A session matches by its title, or by its agent and folder.
        #expect(MentionRows.rows(query: "BLOG", cwd: cwd, used: [], listing: listing, sessions: sessions, except: nil).map(\.name) == ["Blog"])
        #expect(MentionRows.rows(query: "codex", cwd: cwd, used: [], listing: listing, sessions: sessions, except: nil).map(\.name) == ["Orders"])
        // Hidden files only when asked for.
        let hidden = [entry(".env"), entry("env.sample")]
        #expect(MentionRows.rows(query: "env", cwd: cwd, used: [], listing: hidden, sessions: [], except: nil).map(\.name) == ["env.sample"])
        #expect(MentionRows.rows(query: ".e", cwd: cwd, used: [], listing: hidden, sessions: [], except: nil).map(\.name) == [".env"])
        // A file the chat used is not listed again among the folder's.
        #expect(MentionRows.rows(query: "read", cwd: cwd, used: [cwd + "/README.md"], listing: listing, sessions: [], except: nil).map(\.id)
                == ["used:/Users/me/code/app/README.md"])
    }

    @Test func browsingListsTheFolderAlone() {
        let listing = [entry("main.swift"), entry("App", dir: true), entry("Models", dir: true), entry("app.json")]
        let sessions = [session("s1", title: "App", updated: 5)]
        let rows = MentionRows.rows(query: "Sources/", cwd: "/Users/me/code/app", used: ["/Users/me/code/app/x.swift"],
                                    listing: listing, sessions: sessions, except: nil)
        #expect(rows.map(\.name) == ["App/", "Models/", "main.swift", "app.json"])
        #expect(rows.allSatisfy { $0.detail == "Sources/" })
        #expect(rows[0].kind == .file(insert: "@Sources/App/", folder: true))
        #expect(rows[2].kind == .file(insert: "@Sources/main.swift ", folder: false))
        let filtered = MentionRows.rows(query: "Sources/ap", cwd: "/Users/me/code/app", used: [], listing: listing, sessions: [], except: nil)
        #expect(filtered.map(\.name) == ["App/", "app.json"])
        // Not read yet: no rows (the list says it is reading).
        #expect(MentionRows.rows(query: "Sources/", cwd: "/Users/me/code/app", used: [], listing: nil, sessions: sessions, except: nil).isEmpty)
    }

    @Test func atMostEightSessionsAndFortyRows() {
        let sessions = (1...12).map { session("s\($0)", title: "Task \($0)", updated: TimeInterval($0)) }
        let rows = MentionRows.rows(query: "", cwd: nil, used: [], listing: nil, sessions: sessions, except: nil)
        #expect(rows.count == 8)
        #expect(rows.first?.name == "Task 12")
        let big = (1...60).map { entry("f\($0)") }
        #expect(MentionRows.rows(query: "f/", cwd: "/a", used: [], listing: big, sessions: [], except: nil).count == 40)
    }

    // MARK: - Workspaces

    @Test func workspacesMatchEveryFieldAndShortenOnlyTheDetail() {
        let w = workspace("1", title: "  ", cwd: "/data2/home/me/code/weather-app/", branch: "Feature/One")
        let rows = MentionRows.workspaceRows([w], query: "macbook") { $0.id == "macbook" ? "MacBook" : $0.id }
        #expect(rows.map(\.name) == ["weather-app"])
        #expect(rows.first?.detail == "MacBook · ~/code/weather-app/ · Feature/One")
        #expect(rows.first?.id == "workspace:macbook/herdr:1:/data2/home/me/code/weather-app")
        for query in ["WEATHER", "/DATA2/HOME", "macbook", "feature/one"] {
            #expect(MentionRows.workspaceRows([w], query: query, machineName: { _ in "MacBook" }).count == 1, "\(query)")
        }
        #expect(MentionRows.workspaceRows([w], query: "herdr:1", machineName: { _ in "MacBook" }).isEmpty)
        #expect(MentionRows.shortPath("/Users/me/code/app") == "~/code/app")
        #expect(MentionRows.shortPath("/home/me/code/app") == "~/code/app")
        #expect(MentionRows.shortPath("/data/home/me/code/app") == "~/code/app")
        #expect(MentionRows.shortPath("/data2/home/me/code/app") == "~/code/app")
        #expect(MentionRows.shortPath("/private/tmp/app") == "/private/tmp/app")
        let ref = MentionRows.workspaceContext(w) { _ in "MacBook" }
        #expect(ref.extra?["cwd"] == w.cwd)
        #expect(ref.text.contains(w.cwd))
    }

    @Test func workspaceRankingIsStableAndTheFirstFolderWinsBeforeMatching() {
        let all = [
            workspace("a", title: "Alpha local", cwd: "/work/a", available: false),
            workspace("b", title: "Beta", cwd: "/work/b", branch: "alpha"),
            workspace("c", title: "Zed", cwd: "/work/alpha-logs"),
            workspace("d", machine: "server", title: "Alpha remote", cwd: "/work/a", available: false),
            workspace("e", title: "Alpha duplicate", cwd: "/work/a/"),
            workspace("f", machine: "studio", title: "Alpha available", cwd: "/work/c"),
            workspace("empty", title: "Alpha folderless", cwd: ""),
        ]
        let rows = MentionRows.workspaceRows(all, query: "alpha") { $0.id }
        #expect(rows.map(\.name) == ["Alpha available", "Beta", "Zed", "Alpha local", "Alpha remote"])
        let unrelated = workspace("first", title: "Unrelated", cwd: "/same")
        let matchingDuplicate = workspace("second", title: "Alpha", cwd: "/same/")
        #expect(MentionRows.workspaceRows([unrelated, matchingDuplicate], query: "alpha") { $0.id }.isEmpty)
        let many = (0..<12).map { workspace("\($0)", title: "Workspace \($0)", cwd: "/work/\($0)") }
        #expect(MentionRows.workspaceRows(many, query: "") { $0.id }.count == 8)
        let collidingIDs = [workspace("same", title: "One", cwd: "/one"), workspace("same", title: "Two", cwd: "/two")]
        let identities = MentionRows.workspaceRows(collidingIDs, query: "") { $0.id }.map(\.id)
        #expect(Set(identities).count == 2)
    }

    @Test func workspacesSitBetweenTheFolderAndSessions() {
        let w = workspace("1", title: "weather-app", cwd: "/Users/me/code/weather-app", branch: "main")
        let workspaceRows = MentionRows.workspaceRows([w], query: "") { _ in "MacBook" }
        let s = session("s1", title: "Blog", updated: 5)
        let rows = MentionRows.rows(query: "", cwd: "/Users/me/code/app", used: ["/Users/me/code/app/README.md"],
                                    listing: [entry("Sources", dir: true)], sessions: [s], except: nil,
                                    workspaces: workspaceRows)
        #expect(rows.map(\.name) == ["README.md", "Sources/", "weather-app", "Blog"])
        #expect(rows.map(\.group) == [0, 0, 1, 2])
        #expect(rows[2].isContext && !rows[2].isSession)
        #expect(workspaceValue(rows[2]) == w)
        // A slash browses that folder alone, even with a matching workspace loaded.
        let browsing = MentionRows.rows(query: "Sources/", cwd: "/Users/me/code/app", used: [],
                                        listing: [entry("main.swift")], sessions: [s], except: nil,
                                        workspaces: workspaceRows)
        #expect(browsing.map(\.name) == ["main.swift"])
    }

    @Test func workspaceAttributesRoundTripAndOldDraftsDecode() throws {
        let w = workspace("1", title: " A & <B> ", cwd: "/Users/me/\"quoted\"&", branch: "feature/<x>")
        let ref = MentionRows.workspaceContext(w) { _ in "Mac & Co" }
        #expect(ref.kind == "workspace" && ref.url.isEmpty)
        #expect(ref.extra == ["machine": "macbook", "cwd": w.cwd, "branch": w.branch])
        #expect(ref.xml.hasPrefix("<persona-context kind=\"workspace\" title=\"A &amp; &lt;B&gt;\" url=\"\" machine=\"macbook\" cwd=\"/Users/me/&quot;quoted&quot;&amp;\" branch=\"feature/&lt;x&gt;\">"))
        let parsed = ContextRef.parse(ref.xml)
        #expect(parsed.refs.count == 1)
        #expect(parsed.refs.first?.extra == ref.extra)
        #expect(parsed.refs.first?.text == ref.text)
        #expect(parsed.refs.first?.xml == ref.xml)
        #expect(ContextRef.message([ref], body: "Continue").hasSuffix("</persona-context>\n\nContinue"))
        let noBranch = MentionRows.workspaceContext(workspace("2", title: "No branch", cwd: "/tmp/no-branch")) { $0.id }
        #expect(!noBranch.xml.contains(" branch="))
        let decoded = try JSONDecoder().decode(ContextRef.self, from: JSONEncoder().encode(ref))
        #expect(decoded.extra == ref.extra)
        let legacy = #"{"id":"6F9619FF-8B86-D011-B42D-00C04FC964FF","kind":"workspace","title":"Old","url":"","text":"Old draft","msg":""}"#
        #expect(try JSONDecoder().decode(ContextRef.self, from: Data(legacy.utf8)).extra == nil)
    }

    @MainActor @Test func workspacesLoadOncePerOpeningAndFailuresWithoutCacheLeaveNoRows() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/workspaces", json: #"{"workspaces":[{"machine_id":"m","source":"herdr","source_id":"1","title":"One","cwd":"/one"}]}"#)
        let cache = MentionWorkspaceCache()
        let store = MentionWorkspaces()
        store.update(query: "", machines: [], cache: cache)
        await store.load(api: hub.client, group: "group-a")
        await store.load(api: hub.client, group: "group-a")
        #expect(hub.sent("GET", "/api/v1/workspaces").count == 1)
        #expect(store.rows.count == 1)
        #expect(store.rows.first.flatMap(workspaceValue)?.group == "group-a")
        store.update(query: "not-a-workspace", machines: [], cache: cache)
        #expect(store.rows.isEmpty)
        #expect(hub.sent("GET", "/api/v1/workspaces").count == 1)
        let reopened = MentionWorkspaces()
        reopened.update(query: "", machines: [], cache: cache)
        await reopened.load(api: hub.client, group: "group-a")
        #expect(hub.sent("GET", "/api/v1/workspaces").count == 2)

        for status in [404, 503] {
            let failing = FakeHub()
            if status == 503 {
                failing.on("GET", "/api/v1/workspaces") { _ in throw APIError(status: 503, message: "manager away") }
            }
            let fallback = MentionWorkspaces()
            await fallback.load(api: failing.client, group: "")
            #expect(fallback.rows.isEmpty)
            #expect(failing.sent("GET", "/api/v1/workspaces").count == 1)
        }
        let noAPI = MentionWorkspaces()
        await noAPI.load(api: nil, group: "")
        #expect(noAPI.rows.isEmpty)
    }

    @MainActor @Test func laterOpeningsKeepTheHubsLastGoodWorkspacesUntilADifferentAnswer() async {
        let hub = FakeHub(), cache = MentionWorkspaceCache()
        hub.on("GET", "/api/v1/workspaces", json: #"{"workspaces":[{"machine_id":"m","source":"herdr","source_id":"1","title":"One","cwd":"/one"}]}"#)
        let first = MentionWorkspaces()
        first.update(query: "", machines: [], cache: cache)
        #expect(first.rows.isEmpty, "a hub that never answered has no cached rows")
        await first.load(api: hub.client, group: "a")
        #expect(first.rows.map(\.name) == ["One"])

        hub.on("GET", "/api/v1/workspaces", json: #"{"workspaces":[{"machine_id":"m","source":"herdr","source_id":"1","title":"Two","cwd":"/two"}]}"#)
        let gate = MentionWorkspaceResponseGate()
        var delayed = hub.client
        let answer = delayed.transport!
        delayed.transport = { request in
            let data = try await answer(request)
            await gate.hold()
            return data
        }
        let second = MentionWorkspaces()
        second.update(query: "", machines: [], cache: cache)
        #expect(second.rows.map(\.name) == ["One"], "the second opening projects the cache before its request")
        let loading = Task { await second.load(api: delayed, group: "a") }
        await gate.started()
        #expect(second.rows.map(\.name) == ["One"], "a pending answer cannot move the session rows")
        #expect(hub.sent("GET", "/api/v1/workspaces").count == 2)
        await gate.release()
        await loading.value
        #expect(second.rows.map(\.name) == ["Two"], "a different answer replaces the cached rows")

        hub.on("GET", "/api/v1/workspaces") { _ in throw APIError(status: 503, message: "manager away") }
        let failed = MentionWorkspaces()
        failed.update(query: "", machines: [], cache: cache)
        #expect(failed.rows.map(\.name) == ["Two"])
        await failed.load(api: hub.client, group: "a")
        #expect(failed.rows.map(\.name) == ["Two"], "a failed read keeps the last good list")

        let otherHub = FakeHub(url: "https://other.invalid"), otherCache = MentionWorkspaceCache()
        otherHub.on("GET", "/api/v1/workspaces", json: #"{"workspaces":[{"machine_id":"m","source":"herdr","source_id":"2","title":"Other","cwd":"/other"}]}"#)
        let other = MentionWorkspaces()
        other.update(query: "", machines: [], cache: otherCache)
        #expect(other.rows.isEmpty, "two hubs never see each other's cached rows")
        await other.load(api: otherHub.client, group: "b")
        #expect(other.rows.map(\.name) == ["Other"])
        #expect(other.rows.first.flatMap(workspaceValue)?.group == "b")
        #expect(failed.rows.map(\.name) == ["Two"])
    }

    @Test func aPickedSessionIsQuoted() {
        let s = session("s1", title: "Blog", cwd: "/Users/me/code/blog", updated: 5, last: "Done.")
        let ref = PromptItems.quote(s)
        #expect(ref.title == "Blog")
        #expect(ref.url == "persona://session/m/s1")
        #expect(ref.xml.hasPrefix("<persona-context kind=\"context\" title=\"Blog\" url=\"persona://session/m/s1\""))
        #expect(ref.text.contains("/Users/me/code/blog"))
        // The prompt bar's pick quotes the same.
        let source = PromptItems.sessions([s], except: nil)[0]
        #expect(PromptItems.quote(source, in: [s])?.text == ref.text)
    }
}

/// Holds one workspace response after FakeHub has decoded it, proving what a later opening shows before the answer.
private actor MentionWorkspaceResponseGate {
    private var pending: CheckedContinuation<Void, Never>?
    private var arrival: CheckedContinuation<Void, Never>?
    private var hasStarted = false

    func hold() async {
        hasStarted = true
        await withCheckedContinuation { continuation in
            pending = continuation
            arrival?.resume()
            arrival = nil
        }
    }

    func started() async {
        if hasStarted { return }
        await withCheckedContinuation { arrival = $0 }
    }

    func release() {
        pending?.resume()
        pending = nil
    }
}
