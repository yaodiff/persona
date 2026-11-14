import Foundation
import Testing
@testable import Persona
import PersonaWire

struct WatchMessageTests {
    private func item(_ json: String) throws -> Item { try JSON.decoder.decode(Item.self, from: Data(json.utf8)) }

    /// The session page keeps the few messages it shows, not the hub's page: a page of 80 items, most of them the
    /// agent's work with nested tool input, was all kept and compared on every reload (#6256).
    @Test func aPageKeepsOnlyTheNewestMessagesThePageShows() throws {
        var page: [Item] = []
        for n in 0..<20 {
            page += [
                try item(#"{"id":"u\#(n)","seq":\#(4 * n),"role":"user","kind":"text","text":"问题 \#(n)"}"#),
                try item(#"{"id":"t\#(n)","seq":\#(4 * n + 1),"role":"assistant","kind":"tool_use","tool_name":"Bash","tool_input":{"command":"ls","nested":{"a":[1,{"b":{"c":[true,"d"]}}]}}}"#),
                try item(#"{"id":"r\#(n)","seq":\#(4 * n + 2),"role":"user","kind":"tool_result","text":"ok"}"#),
                try item(#"{"id":"a\#(n)","seq":\#(4 * n + 3),"role":"assistant","kind":"text","text":"回答 \#(n)"}"#),
            ]
        }
        page.append(try item(#"{"id":"empty","seq":80,"role":"assistant","kind":"text","text":"<pasted_content></pasted_content>"}"#))
        #expect(WatchMessage.latest(page).map(\.id) == ["u17", "a17", "u18", "a18", "u19", "a19"])
    }

    /// What its bubble reads comes along, read once: Persona's message for the model (here off an older hub's text),
    /// the words as the watch shows them, and where it came from.
    @Test func aMessageKeepsWhatItsBubbleReads() throws {
        let page = [
            try item(#"{"id":"run","seq":1,"role":"user","kind":"text","text":"⏰ 盯部署（定时任务，每 30 分钟）\n（又到时间了，还是这个任务自己的对话：前几次的结果都在上面，接着做，和上次比有变化的地方说清楚。）\n\n看部署好了没有。"}"#),
            try item(#"{"id":"sent","seq":2,"role":"user","kind":"text","text":"**看看** CI","client_msg_id":"wechat-1","from":"构建"}"#),
        ]
        let kept = WatchMessage.latest(page)
        #expect(kept.map { $0.injected?.title } == ["盯部署", nil])
        #expect(kept.last?.text == "看看 CI" && kept.last?.clientMsgID == "wechat-1" && kept.last?.from == "构建")
    }
}
