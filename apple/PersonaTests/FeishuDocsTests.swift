import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct FeishuDocsTests {
    private func decode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
        try JSON.decoder.decode(T.self, from: Data(json.utf8))
    }

    // MARK: Models

    @Test func decodesDriveAndDiscoverItems() throws {
        let page: FeishuDocsPage = try decode("""
        {"items":[
          {"id":"f1","token":"f1","kind":"folder","title":"文件夹","url":"https://open.feishu.cn/drive/folder/ExampleFolder","type":0,"owner":"A",
           "updatedAt":"2026-10-21T07:38:48.000Z","supported":true,"sharedFolder":true},
          {"id":"d1","title":"Rust","url":"https://open.feishu.cn/docx/ExampleDocument","type":22,"owner":"B","updatedAt":"","supported":true,
           "summary":"s","reason":"trending","views":12,"likes":3,"comments":1,
           "icon":{"type":"emoji","value":"📖"}},
          {"id":"p1","title":"Page","url":"https://x.larkoffice.com/page/p1","type":113,"updatedAt":"2026-10-25T11:33:38.000Z"}
        ],"nextCursor":"o:1|2","path":[{"token":"f0","name":"根"}]}
        """)
        #expect(page.items.count == 3)
        #expect(page.items[0].isFolder)
        #expect(page.items[1].docKind == .doc)
        #expect(page.items[1].updated == nil)   // "" is not 1970
        #expect(page.items[1].icon?.value == "📖")
        #expect(page.items[2].docKind == .page) // unknown type falls back to the link
        #expect(page.nextCursor == "o:1|2")
        #expect(page.path?.first?.name == "根")
    }

    @Test func kindsFromTypeAndURL() {
        #expect(FeishuDocsKind(type: 3, url: "") == .sheet)
        #expect(FeishuDocsKind(type: 8, url: "") == .bitable)
        #expect(FeishuDocsKind(type: 12, url: "") == .file)
        #expect(FeishuDocsKind(type: 99, url: "https://a.feishu.cn/base/x") == .bitable)
        #expect(FeishuDocsKind(type: 99, url: "https://a.feishu.cn/drive/folder/x") == .folder)
        #expect(FeishuDocsKind(type: 22, url: "", kind: "folder") == .folder)
        #expect(FeishuDocsKind.from(url: "https://a.larkoffice.com/wiki/abc") == .wiki)
        #expect(FeishuDocsFileKind.symbol(ext: nil, title: "report.PDF").hue == "red")
        #expect(FeishuDocsFileKind.symbol(ext: "md", title: "x").symbol == Theme.Icon.code)
    }

    @Test func decodesReadTreeAndFlagsBase() throws {
        let v: FeishuDocsView = try decode("""
        {"title":"T","url":"u","token":"t","content":{"type":"doc","content":[
          {"type":"heading","attrs":{"id":"doc-block-h1","level":1},"content":[{"type":"text","text":"Intro"}]},
          {"type":"paragraph","attrs":{"id":"doc-block-p"},"content":[{"type":"text","text":"Hi ","marks":[{"type":"bold"}]},
            {"type":"mention","attrs":{"kind":"user","label":"Ann","userId":"42"}}]}
        ]},"outline":[{"id":"doc-block-h1","title":"Intro","level":1}],"blockCount":2,"unsupportedTypes":[],
         "authority":{"copy":false,"print":false,"export":false},"owner":{"name":"O"}}
        """)
        #expect(v.content.children.count == 2)
        #expect(v.content.children[1].plainText == "Hi Ann")
        #expect(v.authority?.copy == false)
        #expect(!v.isBase && !v.isSpreadsheet)
        #expect(FeishuDocsTree.outline(v.content).first?.title == "Intro")
        #expect(FeishuDocsTree.rowID(for: "doc-block-h1", in: v.content) == "doc-block-h1")

        let base: FeishuDocsView = try decode(#"{"title":"B","url":"u","token":"t","content":{"type":"doc"},"outline":[],"base":{"token":"x"}}"#)
        #expect(base.isBase)
        let file: FeishuDocsView = try decode(#"{"title":"a.md","url":"u","token":"t","content":{"type":"doc"},"outline":[],"reader":"file","object":{"kind":"file","token":"tok","extension":"md"}}"#)
        #expect(file.object?.kind == "file" && file.object?.extension == "md")
    }

    // MARK: Lists

    @Test func sortsCommentThreads() throws {
        let c: FeishuDocsComments = try decode("""
        {"threads":[
          {"id":"old","whole":false,"resolved":false,"quote":"q","createdAt":"2026-01-01T00:00:00Z","replies":[
            {"id":"r","author":{"name":"A"},"content":[{"kind":"text","text":"x"}],"createdAt":"2026-01-01T00:00:00Z"}]},
          {"id":"done","whole":true,"resolved":true,"quote":"","replies":[
            {"id":"r2","author":{"name":"B"},"content":[{"kind":"text","text":"y"}],"createdAt":"2026-10-01T00:00:00Z"}]},
          {"id":"new","whole":false,"resolved":false,"quote":"q","createdAt":"2026-01-01T00:00:00Z","replies":[
            {"id":"r3","author":{"name":"C"},"content":[{"kind":"mention","text":"@D"},{"kind":"link","text":" l","href":"https://a"}],"createdAt":"2026-09-01T00:00:00Z"}]}
        ]}
        """)
        #expect(feishuDocsSortedThreads(c.threads).map(\.id) == ["new", "old", "done"])
        #expect(c.threads[2].replies[0].text == "@D l")
    }

    // MARK: Markdown

    @Test func preparesMarkdownExport() {
        let md = """
        # 智能纪要

        [Source](<https://open.feishu.cn/docx/ExampleDocument>)

        > 会议主题

        # 总结

        ![](<http://hub.example.com:3333/api/documents/whiteboard/W?origin=x>)

        ```
        # not a heading
        ```

        ## 待办 \\- 一

        - [ ] a
        """
        let body = FeishuDocsMarkdownExport.prepare(md, title: "智能纪要")
        #expect(!body.chunks.contains { $0.text.contains("[Source]") })
        #expect(!body.chunks.contains { $0.text.contains("# 智能纪要") })
        #expect(body.outline.map(\.title) == ["总结", "待办 - 一"])
        #expect(body.outline.map(\.level) == [1, 2])
        #expect(body.chunks.count == 3)
        #expect(body.chunks.allSatisfy { $0.table == nil })
        #expect(body.outline.allSatisfy { o in body.chunks.contains { $0.id == o.id } })
        #expect(body.images == ["/api/v1/lark/raw/documents/whiteboard/W?origin=x"])
        #expect(body.chunks[1].text.contains("# not a heading"))
    }

    @Test func splitsLongRunsAtBlockBoundaries() {
        let lines = (0..<10).map { "para \($0)\n" } + ["- item\n", "  continued\n", "| a | b |\n", "| - | - |\n", "| 1 | 2 |\n", "\n", "tail"]
        let body = FeishuDocsMarkdownExport.prepare(lines.joined(separator: "\n"), maxLines: 4)
        #expect(body.chunks.count > 1)
        // A table is its own chunk and never splits (blank lines between its rows end it, as in GFM).
        #expect(body.chunks.contains { $0.text.contains("| a | b |") })
        #expect(FeishuDocsMarkdownExport.headingLevel("### x") == 3)
        #expect(FeishuDocsMarkdownExport.headingLevel("#hashtag") == nil)
        #expect(FeishuDocsMarkdownExport.unescape(#"a\-b\+c\.d"#) == "a-b+c.d")
        #expect(FeishuDocsMarkdownExport.stripInline("**粗** [链接](u) `c`") == "粗 链接 c")
        #expect(FeishuDocsMarkdownExport.rewriteMedia("x https://h:3333/api/documents/media/k y /api/v1/lark/raw/z") == "x /api/v1/lark/raw/documents/media/k y /api/v1/lark/raw/z")
    }

    // MARK: Tree rendering logic

    @Test func numbersOrderedListsAcrossSiblings() {
        func ol(_ start: Int? = nil, restart: Bool = false) -> FeishuDocsNode {
            var a: [String: JSONValue] = ["restart": .bool(restart)]
            if let start { a["start"] = .number(Double(start)) }
            return FeishuDocsNode(type: "orderedList", attrs: a, content: [FeishuDocsNode(type: "listItem")])
        }
        let p = FeishuDocsNode(type: "paragraph")
        let nodes = [ol(), ol(), ol(), p, ol(), ol(5, restart: true), ol()]
        #expect(FeishuDocsTree.orderedNumbers(nodes) == [1, 2, 3, nil, 1, 5, 6])
    }

    @Test func laysOutTablesWithSpans() throws {
        // Row 1: A (rowspan 2) | B (colspan 2); Row 2: C | D; Row 3: E (short row).
        let t: FeishuDocsNode = try decode("""
        {"type":"table","content":[
          {"type":"tableRow","content":[{"type":"tableHeader","attrs":{"rowspan":2,"colwidth":[200]}},{"type":"tableHeader","attrs":{"colspan":2,"colwidth":[50,1000]}}]},
          {"type":"tableRow","content":[{"type":"tableCell"},{"type":"tableCell"}]},
          {"type":"tableRow","content":[{"type":"tableCell"}]}
        ]}
        """)
        let l = FeishuDocsTableLayout(t)
        #expect(l.columns == 3)
        #expect(l.rows[0].map(\.cell) == [0, 1])
        #expect(l.rows[0][1].colspan == 2 && l.rows[0][0].header)
        #expect(l.rows[1].map(\.cell) == [nil, 0, 1])       // column 0 covered by A
        #expect(l.rows[1].map(\.column) == [0, 1, 2])
        #expect(l.rows[2].map(\.cell) == [0, nil, nil])     // padded
        #expect(l.authorWidths == [200, 50, 1000])
        let natural = l.naturalWidths { _, _ in "" }
        #expect(natural == [200, 50, 1000])
        #expect(FeishuDocsTableSizing.width(natural, column: 1, colspan: 2) == 1050)
    }

    @Test func sizesTablesToFitOrScroll() {
        // Fits as is: natural widths, no stretching.
        #expect(FeishuDocsTableSizing.fit(natural: [100, 120], available: 366) == ([100, 120], false))
        // Too wide but few columns: narrow ones keep their width, wide ones share the rest; nothing scrolls.
        let a = FeishuDocsTableSizing.fit(natural: [60, 600], available: 366)
        #expect(a.widths == [60, 306] && !a.scrolls)
        let b = FeishuDocsTableSizing.fit(natural: [200, 1000, 90], available: 366)
        #expect(!b.scrolls && b.widths.reduce(0, +) <= 366 && b.widths[2] == 90 && b.widths[0] == 138)
        // Too many columns for a readable width: natural widths clamped, scrolls.
        let c = FeishuDocsTableSizing.fit(natural: [40, 500, 120, 120, 120], available: 366)
        #expect(c.scrolls && c.widths == [48, 280, 120, 120, 120])
        #expect(FeishuDocsTableSizing.estimate("abcd") == 48)
        #expect(FeishuDocsTableSizing.estimate("中文\nab") == 46)
    }

    @Test func parsesMarkdownTables() throws {
        let t = try #require(FeishuDocsMarkdownTable("""
        |  |  |  |
        | --- | :---: | --- |
        | v1.0 | 2023 \\| 11 | **彭** <br> [链接](<https://a>) |
        | only |
        """))
        #expect(t.header == nil)            // an all-empty header row is dropped
        #expect(t.columns == 3)
        #expect(t.rows[0] == ["v1.0", "2023 | 11", "**彭** <br> [链接](<https://a>)"])
        #expect(t.grid[1] == ["only", "", ""])
        #expect(FeishuDocsMarkdownTable.cellMarkdown("a<br/>b ![x](<u>)") == "a\nb [图片]")
        #expect(FeishuDocsMarkdownTable("no table") == nil)
        #expect(FeishuDocsMarkdownTable.isDelimiter("|---|:-:|"))
        #expect(!FeishuDocsMarkdownTable.isDelimiter("| a | b |"))

        let body = FeishuDocsMarkdownExport.prepare("""
        intro

        | A | B |
        | - | - |
        | 1 | 2 |
        after
        """)
        #expect(body.chunks.count == 3)
        #expect(body.chunks[1].table?.header == ["A", "B"])
        #expect(body.chunks[2].text == "after")
    }

    @Test func parsesCSSColors() throws {
        let a = try #require(FeishuDocsColor.parse("#F54A45"))
        #expect(abs(a.r - 245.0 / 255) < 0.001 && a.a == 1)
        let b = try #require(FeishuDocsColor.parse("rgba(255, 246, 122, 0.8)"))
        #expect(abs(b.a - 0.8) < 0.001 && abs(b.g - 246.0 / 255) < 0.001)
        #expect(FeishuDocsColor.parse("#abc")?.b == 1.0 * 0xcc / 255)
        #expect(FeishuDocsColor.parse("red") == nil)
        #expect(FeishuDocsColor.text("#1f2329") == nil)        // near-black text follows the system
        #expect(FeishuDocsColor.text("rgb(245, 74, 69)") != nil)
        #expect(FeishuDocsColor.background("rgba(0,0,0,0)") == nil)
    }

    @Test func errorCodesFromBothShapes() {
        #expect(FeishuDocsFailure.code(from: Data(#"{"error":{"code":"DOCUMENT_EXPORT_FORBIDDEN","message":"x"}}"#.utf8)) == "DOCUMENT_EXPORT_FORBIDDEN")
        #expect(FeishuDocsFailure.code(from: Data(#"{"error":"lark: bad path"}"#.utf8)) == "lark: bad path")
        #expect(FeishuDocsFailure(status: 422, code: "DOCUMENT_FORMAT_UNSUPPORTED").errorDescription?.contains("暂不支持") == true)
        #expect(FeishuDocsFailure(status: 504, code: nil).errorDescription?.contains("稍后") == true)
    }
}
