import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The images a turn's work looked at (`ViewedImages`, docs/protocol/protocol.md「Images viewed this turn」), by the hub's rules, from
/// the work itself and from a fold that hides it; and where their strip goes (`ChatLines`): between the work line and
/// the answer, also while the turn runs.
@MainActor
struct ViewedImagesTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil,
                      input: [String: JSONValue]? = nil, image: ImagePayload? = nil, error: Bool? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool,
             toolInput: input.map { .object($0) }, isError: error, image: image)
    }

    /// A lazy picture, as a page carries it (ChatStore fills in the ref).
    private func png(_ w: Int, _ h: Int, size: Int, item: String) -> ImagePayload {
        var p = ImagePayload(mime: "image/png", data: "")
        p.lazy = true
        p.size = size
        p.width = w
        p.height = h
        p.ref = "m1/s1/" + item
        return p
    }

    private func blocks(_ items: [Item]) -> [ChatBlock] { ChatStore.rebuild(items, settled: nil).blocks }

    private func kinds(_ lines: [ChatLine]) -> [String] {
        lines.map { line in
            switch line.content {
            case .row(.user): "user"
            case .row(.assistant): "answer"
            case .work: "work"
            case .viewed: "viewed"
            case .userImages: "photos"
            default: "other"
            }
        }
    }

    private func viewed(_ lines: [ChatLine]) -> [ViewedImage] {
        lines.compactMap { if case .viewed(_, let images) = $0.content { images } else { nil } }.flatMap { $0 }
    }

    /// The images results carried (a Read's is that file), a picture Read opened whose result carried none (only its
    /// path), each once in the order they came: not the same file again, not an unchanged screen looked at again, not
    /// a file that is no picture, not a failed Read, not the photo the user sent.
    private var turn: [Item] {
        var photo = item(2, .image, role: "user")
        photo.image = ImagePayload(mime: "image/png", data: "aGVsbG8=")
        return [
            item(1, .text, role: "user", text: "看看首页"),
            photo,
            item(3, .toolUse, tool: "Read", id: "a", input: ["file_path": .string("/w/shot.png")]),
            item(4, .toolResult, role: "user", text: "", id: "a"),
            item(5, .image, role: "user", id: "a", image: png(4, 3, size: 100, item: "5#0")),
            item(6, .toolUse, tool: "mcp__persona__computer_state", id: "b", input: ["machine": .string("m1"), "app": .string("Notes")]),
            item(7, .toolResult, role: "user", text: "ok", id: "b"),
            item(8, .image, role: "user", id: "b", image: png(16, 10, size: 900, item: "8#0")),
            item(9, .toolUse, tool: "Read", id: "c", input: ["file_path": .string("/w/shot.png")]),
            item(10, .toolResult, role: "user", text: "", id: "c"),
            item(11, .image, role: "user", id: "c", image: png(4, 3, size: 100, item: "11#0")),
            item(12, .toolUse, tool: "Read", id: "d", input: ["file_path": .string("/w/big.JPG")]),
            item(13, .toolResult, role: "user", text: "", id: "d"),
            item(14, .toolUse, tool: "Read", id: "e", input: ["file_path": .string("/w/main.swift")]),
            item(15, .toolResult, role: "user", text: "", id: "e"),
            item(16, .toolUse, tool: "Read", id: "f", input: ["file_path": .string("/w/gone.png")]),
            item(17, .toolResult, role: "user", text: "no such file", id: "f", error: true),
            item(18, .toolUse, tool: "mcp__persona__browser_look", id: "g", input: ["machine": .string("m2")]),
            item(19, .toolResult, role: "user", text: "ok", id: "g"),
            item(20, .image, role: "user", id: "g", image: png(8, 5, size: 300, item: "20#0")),
            item(21, .toolUse, tool: "mcp__persona__computer_state", id: "h", input: ["machine": .string("m1")]),
            item(22, .toolResult, role: "user", text: "ok", id: "h"),
            item(23, .image, role: "user", id: "h", image: png(16, 10, size: 900, item: "23#0")),
            item(24, .text, text: "看完了"),
        ]
    }

    @Test func whatTheWorkLookedAtInOrderEachOnce() {
        let lines = ChatLines(openWork: []).build(blocks(turn), live: nil)
        #expect(kinds(lines) == ["user", "photos", "work", "viewed", "answer"])
        let images = viewed(lines)
        #expect(images.map(\.source) == [.read, .screenshot, .read, .browser])
        #expect(images.map(\.path) == ["/w/shot.png", nil, "/w/big.JPG", nil])
        #expect(images.map(\.machine) == [nil, "m1", nil, "m2"])
        #expect(images.map { $0.payload?.ref } == ["m1/s1/5#0", "m1/s1/8#0", nil, "m1/s1/20#0"])
    }

    /// A page that folded the work (fold_work) still has its images: the fold says what its run looked at, and the
    /// strip is the same one the work itself gives once the line is opened.
    @Test func aFoldGivesTheSameStripAsItsWork() {
        let images: [FoldImage] = [
            FoldImage(itemID: "5#0", toolUseID: "a", tool: "Read", source: "read", path: "/w/shot.png", mime: "image/png", size: 100, width: 4, height: 3),
            FoldImage(itemID: "8#0", toolUseID: "b", tool: "mcp__persona__computer_state", source: "screenshot", machine: "m1",
                      mime: "image/png", size: 900, width: 16, height: 10),
            FoldImage(toolUseID: "d", tool: "Read", source: "read", path: "/w/big.JPG"),
            FoldImage(itemID: "20#0", toolUseID: "g", tool: "mcp__persona__browser_look", source: "browser", machine: "m2",
                      mime: "image/png", size: 300, width: 8, height: 5),
        ]
        var fold = item(3, .fold)
        fold.id = "fold:3#0"
        fold.fold = Fold(items: 21, tools: 8, thoughts: 0, until: nil, throughSeq: 23, throughID: "23#0", images: images)
        let page = ChatStore.withImageRefs([turn[0], turn[1], fold, turn.last!], SessionKey(machineID: "m1", sessionID: "s1"))
        let folded = viewed(ChatLines(openWork: []).build(blocks(page), live: nil))
        let open = viewed(ChatLines(openWork: []).build(blocks(turn), live: nil))
        #expect(folded.map(\.id) == open.map(\.id))
        #expect(folded.map { $0.payload?.ref } == open.map { $0.payload?.ref })
        #expect(folded.map(\.source) == open.map(\.source))
        #expect(folded.map { $0.payload?.width } == [4, 16, nil, 8])
    }

    /// While the turn runs the strip comes after the work line, before the words so far, and keeps its id as pictures
    /// come in (it grows sideways in place); a call still reading is not in it yet.
    @Test func whileTheTurnRuns() {
        let running = Array(turn.prefix(8)) + [item(9, .text, text: "截好了，再看一张"),
                                              item(10, .toolUse, tool: "Read", id: "c", input: ["file_path": .string("/w/next.png")])]
        let b = blocks(running)
        guard case .turn(let t)? = b.last else { Issue.record("no turn"); return }
        let lines = ChatLines(openWork: []).build(b, live: t.id)
        #expect(kinds(lines) == ["user", "photos", "work", "viewed", "answer"])
        #expect(viewed(lines).count == 2)

        let more = running + [item(11, .toolResult, role: "user", text: "", id: "c"),
                              item(12, .image, role: "user", id: "c", image: png(2, 2, size: 40, item: "12#0"))]
        let later = ChatLines(openWork: []).build(blocks(more), live: t.id)
        #expect(viewed(later).count == 3)
        #expect(later.first { if case .viewed = $0.content { true } else { false } }?.id
                == lines.first { if case .viewed = $0.content { true } else { false } }?.id)
    }

    /// A turn whose work looked at nothing has no strip.
    @Test func noImagesNoStrip() {
        let lines = ChatLines(openWork: []).build(blocks([item(1, .text, role: "user", text: "跑一下"), item(2, .toolUse, tool: "Bash", id: "a"),
                                                          item(3, .toolResult, role: "user", text: "ok", id: "a"), item(4, .text, text: "好了")]), live: nil)
        #expect(kinds(lines) == ["user", "work", "answer"])
    }

    @Test func documentsAreCardsTheRestChips() {
        #expect(["/w/报告.docx", "/w/表.XLSX", "/w/a.pptx", "/w/a.pdf", "/w/a.key"].allSatisfy(DocumentCards.isDocument))
        #expect(!["/w/a.png", "/w/a.swift", "/w/README.md"].contains(where: DocumentCards.isDocument))
        #expect(DocumentCards.kind("/w/报告.docx") == "Word 文档")
        #expect(DocumentCards.kind("/w/表.XLSX") == "Excel 表格")
        #expect(DocumentCards.kind("/w/a.pptx") == "PowerPoint 演示文稿")
    }
}
