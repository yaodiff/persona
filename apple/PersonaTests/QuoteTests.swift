import Foundation
import Testing
@testable import Persona

/// Quotes as Telegram's replies (#986): a quoted message carries its id through the `<persona-context>` tag, so the
/// sent bubble leads back to it; what the user picked shows as quotes, what the hub put in a Feishu /agent request folds.
struct QuoteTests {
    @Test func aQuotedMessageKeepsItsIDThroughTheTag() {
        let ref = ContextRef.quote("PR #42 看完了：逻辑没问题", from: "Claude", msg: "item-\"7\"")
        let parsed = ContextRef.parse(ref.xml + "\n\n按这个改")
        #expect(parsed.rest == "按这个改")
        #expect(parsed.refs.count == 1)
        #expect(parsed.refs[0].kind == "quote")
        #expect(parsed.refs[0].msg == "item-\"7\"")
        #expect(parsed.refs[0].text == "PR #42 看完了：逻辑没问题")
    }

    @Test func aTagWithoutTheIDReadsAsBefore() {
        let ref = ContextRef(kind: "context", title: "页面", url: "https://example.com/a", text: "选中的文字")
        #expect(!ref.xml.contains("msg="))
        #expect(ContextRef.parse(ref.xml).refs.first?.msg == "")
    }

    @Test func headingsSayWhatIsQuoted() {
        #expect(ContextRef.quote("x", from: "我", msg: "1").quoteHeading == "引用 你的消息")
        #expect(ContextRef.quote("x", from: "Codex", msg: "1").quoteHeading == "引用 Codex")
        let page = ContextRef(kind: "context", title: "时间线", url: "https://example.com/t", text: "一段")
        #expect(page.quoteHeading == "引用选中的文字 · 时间线")
        let whole = ContextRef(kind: "context", title: "时间线", url: "https://example.com/t", text: "（整篇）")
        #expect(whole.quoteHeading == "引用 时间线")
        #expect(whole.quoteSummary == "example.com")
        let session = ContextRef(kind: "context", title: "照片同步", url: "persona://session/m/s",
                                 text: "另一个会话：照片同步（Claude）\n目录：~/code/photos")
        #expect(session.quoteHeading == "引用会话 照片同步")
        #expect(session.quoteSummary == "目录：~/code/photos")
    }

    @Test func theHubsContextFoldsAndPicksAreQuotes() {
        let picks = [ContextRef.quote("x", from: "Claude", msg: "1"),
                     ContextRef(kind: "context", title: "录音", url: "", text: "转写")]
        #expect(ContextRef.quotes(picks)?.count == 2)
        let agent = picks + [ContextRef(kind: "instructions", title: "给 Agent 的说明", url: "", text: "…")]
        #expect(ContextRef.quotes(agent) == nil)
        #expect(ContextRef.quotes([]) == nil)
    }
}

/// Select Text › Quote (#4549): only the words picked go in the tag, which still leads back to the message and says it
/// is a part of it (`part="selection"`); a whole message's quote is written as before.
struct SelectionQuoteTests {
    @Test func aSelectionQuoteCarriesOnlyThePickedWordsAndTheID() {
        let ref = ContextRef.quote("逻辑没问题", from: "Claude", msg: "item-7", part: ContextRef.selectionPart)
        #expect(ref.xml.contains(#" msg="item-7" part="selection">"#))
        let parsed = ContextRef.parse(ref.xml + "\n\n这里再看看")
        #expect(parsed.rest == "这里再看看")
        #expect(parsed.refs.count == 1)
        #expect(parsed.refs[0].kind == "quote")
        #expect(parsed.refs[0].msg == "item-7")
        #expect(parsed.refs[0].part == ContextRef.selectionPart)
        #expect(parsed.refs[0].text == "逻辑没问题")
        #expect(parsed.refs[0].quoteHeading == "引用选中的文字 · Claude")
    }

    @Test func aWholeMessageQuoteHasNoPart() {
        let whole = ContextRef.quote("PR #42 看完了：逻辑没问题", from: "Claude", msg: "item-7")
        #expect(!whole.xml.contains("part="))
        #expect(ContextRef.parse(whole.xml).refs.first?.part == nil)
        #expect(whole.quoteHeading == "引用 Claude")
    }

    /// Workspaces are user-picked contexts. Their empty URL cannot collide with an ordinary quote or another workspace.
    @Test func workspaceContextsAndMentionDeduplication() {
        let one = ContextRef(kind: "workspace", title: "One", url: "", text: "Workspace: One\nComputer: Mac\nFolder: /one",
                             extra: ["machine": "m", "cwd": "/one"])
        let two = ContextRef(kind: "workspace", title: "Two", url: "", text: "Workspace: Two\nComputer: Mac\nFolder: /two",
                             extra: ["machine": "m", "cwd": "/two"])
        let quote = ContextRef.quote("Earlier", from: "Claude", msg: "m1")
        #expect(one.isQuote)
        #expect(one.quoteHeading == "工作区 · One")
        #expect(one.quoteSummary == "Computer: Mac Folder: /one")
        let withQuote = ContextRef.addingMention(one, to: [quote]) ?? []
        #expect(withQuote.count == 2)
        #expect(ContextRef.addingMention(quote, to: [one])?.count == 2)
        #expect(ContextRef.addingMention(one, to: withQuote) == nil)
        #expect(ContextRef.addingMention(two, to: withQuote)?.count == 3)
        let changed = ContextRef(kind: "workspace", title: one.title, url: "", text: one.text + "\nBranch: next")
        #expect(ContextRef.addingMention(changed, to: [one])?.count == 2)
        let samePage = ContextRef(kind: "context", title: "Same", url: "https://example.com", text: "one")
        let samePageAgain = ContextRef(kind: "context", title: "Same again", url: "https://example.com", text: "two")
        #expect(ContextRef.addingMention(samePageAgain, to: [samePage]) == nil)
    }

    /// A quote saved over the box by an older build (no `part`) still loads.
    @Test func aSavedQuoteWithoutAPartStillLoads() throws {
        let json = #"{"id":"6F9619FF-8B86-D011-B42D-00C04FC964FF","kind":"quote","title":"Claude","url":"","text":"x","msg":"1"}"#
        let ref = try JSONDecoder().decode(ContextRef.self, from: Data(json.utf8))
        #expect(ref.part == nil)
        #expect(ref.msg == "1")
    }

    /// The same message goes over the box once as a whole and once per picked part; a message without an id each time.
    @Test func eachQuoteOfAMessageGoesInOnce() {
        let whole = ContextRef.quote("整条", from: "Claude", msg: "m1")
        let part = ContextRef.quote("一段", from: "Claude", msg: "m1", part: ContextRef.selectionPart)
        let one = ContextRef.adding(whole, to: []) ?? []
        #expect(one.count == 1)
        #expect(ContextRef.adding(whole, to: one) == nil)
        let two = ContextRef.adding(part, to: one) ?? []
        #expect(two.count == 2)
        #expect(ContextRef.adding(part, to: two) == nil)
        let other = ContextRef.quote("另一段", from: "Claude", msg: "m1", part: ContextRef.selectionPart)
        #expect(ContextRef.adding(other, to: two)?.count == 3)
        let unnamed = ContextRef.quote("整条", from: "Claude", msg: "")
        #expect(ContextRef.adding(unnamed, to: [unnamed])?.count == 2)
    }

    /// Select Text's Quote hands the chat the picked words, the message's id and whose it is; no Quote when the chat
    /// takes none.
    @MainActor @Test func selectTextQuotesThePickedWords() {
        let menu = MessageMenu()
        let page = SelectableText(text: "整条消息的全部内容", plain: true, msg: "m1")
        #expect(menu.selectionQuote(of: page) == nil)
        var got: [(String, String, Bool, String?)] = []
        menu.quote = { got.append(($0, $1, $2, $3)) }
        menu.selectionQuote(of: page)?("全部内容")
        #expect(got.count == 1)
        #expect(got.first?.0 == "全部内容")
        #expect(got.first?.1 == "m1")
        #expect(got.first?.2 == true)
        #expect(got.first?.3 == ContextRef.selectionPart)
    }
}
