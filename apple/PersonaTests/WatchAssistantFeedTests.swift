import Foundation
import Testing
@testable import Persona
import PersonaWire

struct WatchAssistantFeedTests {
    private func view(_ version: Int64, conversation: String = "conversation", fallback: String = "发布进行中") -> ExtensionAssistantView {
        ExtensionAssistantView(id: "view_progress", conversation: conversation, version: version, view: ExtensionView(fallback: fallback))
    }
    private func item(_ view: ExtensionAssistantView, at: Date = Date(timeIntervalSince1970: 100)) throws -> FeedItem {
        let data = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(view))
        return FeedItem(id: "feed_progress", conversation: view.conversation, at: at, kind: .view, ref: view.id, body: .object(["view": data]))
    }
    @Test func aSnapshotUpdatesTheOriginalRowWithoutMovingOrDuplicatingIt() throws {
        var state = WatchAssistantFeed()
        let creation = try item(view(1))
        state.receive(.init(items: [creation], views: ["view_progress": view(1)]), conversation: "conversation")
        state.receive(.init(items: [], views: ["view_progress": view(2, fallback: "发布完成")]), conversation: "conversation")
        state.receive(.init(items: [creation], views: ["view_progress": view(1)]), conversation: "conversation")
        #expect(state.items.count == 1 && state.items[creation.id]?.at == creation.at)
        #expect(state.fallback(creation) == "发布完成")
        #expect(state.views["view_progress"]?.version == 2)
    }
    @Test func invalidOwnershipAndUnplacedSnapshotsNeverBecomeRows() throws {
        var state = WatchAssistantFeed()
        let other = view(9, conversation: "other")
        state.receive(.init(items: [try item(other)], views: ["view_progress": other]), conversation: "conversation")
        #expect(state.items.isEmpty && state.views.isEmpty)
        state.receive(.init(items: [], views: ["view_progress": view(2)]), conversation: "conversation")
        #expect(state.lines([]).isEmpty)
    }
    @Test func cursorFollowsAllFeedItemsEvenWhenOnlyCardsAreDrawn() throws {
        var state = WatchAssistantFeed()
        let time = Date(timeIntervalSince1970: 200)
        state.receive(.init(items: [FeedItem(id: "other-line", conversation: "conversation", at: time, kind: .unknown)]), conversation: "conversation")
        #expect(state.cursor == time && state.items.isEmpty)
        state.receive(.init(items: []), conversation: "conversation")
        #expect(state.cursor == time)
    }
    @Test func textAndCardRowsShareChronologicalOrderAndSeparateIdentities() throws {
        var state = WatchAssistantFeed()
        let creation = try item(view(1))
        state.receive(.init(items: [creation]), conversation: "conversation")
        let before = WatchMessage(id: "before", ts: Date(timeIntervalSince1970: 90), role: "assistant", text: "之前", fromCard: false)
        var after = before
        after.id = creation.id; after.ts = Date(timeIntervalSince1970: 110)
        #expect(state.lines([after, before]).map(\.id) == ["message:before", "view:feed_progress", "message:feed_progress"])
    }
    @Test func HTMLStillShowsOnlyThePlainTextFallback() throws {
        var state = WatchAssistantFeed()
        var html = view(1, fallback: "选择 B，得分更高")
        html.view.sections = [ExtensionSection(items: [.html(ExtensionHtmlComponent(html: "<button>JSON 不应出现</button>"))])]
        let creation = try item(html)
        state.receive(.init(items: [creation], views: ["view_progress": html]), conversation: "conversation")
        #expect(state.fallback(creation) == "选择 B，得分更高")
    }
}
