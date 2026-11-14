import Foundation
import Testing
@testable import Persona
import PersonaWire

struct LarkEncryptionTests {
    private func message(_ state: String = "decrypted") throws -> LarkMessage {
        let json = """
        {"id":"m1","position":"1","sender_name":"成员","is_mine":false,"type":"TEXT",
         "text":"private body legacy","recalled":false,"reply_count":0,
         "parts":[{"kind":"text","text":"private body legacy"}],
         "olk":{"version":1,"state":"\(state)","bodyParts":[{"kind":"text","text":"private body"}],
                "publicParts":[{"kind":"text","text":"稍等"}],"keyHandout":true},
         "reply_to":{"id":"m0","sender_name":"成员","text":"private quote",
           "olk":{"version":1,"state":"decrypted","bodyParts":[{"kind":"text","text":"private quote"}],
                  "publicParts":[{"kind":"text","text":"我看看"}]}}}
        """
        return try JSON.decoder.decode(LarkMessage.self, from: Data(json.utf8))
    }

    @Test func authorizedBodyCoverAndHandoutStayDistinct() throws {
        let m = try message()
        #expect(m.displayText == "private body")
        #expect(m.olk?.publicParts.first?.text == "稍等")
        #expect(m.olk?.sharesKey == true)
        #expect(m.replyTo?.displayText == "private quote")
    }

    @Test(arguments: ["locked", "forbidden", "not-configured", "missing-key", "failed", "unavailable", "future-state"])
    func closedResponsesNeverUseBodyOrHandout(_ state: String) throws {
        let m = try message(state)
        #expect(m.displayText == "稍等")
        #expect(m.olk?.sharesKey == false)
    }

    @Test func unknownVersionsDoNotExposePlaintext() throws {
        var m = try message()
        m.olk?.version = 2
        #expect(m.displayText == "稍等")
        #expect(m.olk?.isOpen == false)
    }

    @Test func diskSnapshotsRemovePlaintextInBothLegacyAndStructuredFields() throws {
        let m = try message()
        let data = try JSON.encoder.encode(m)
        #expect(!String(decoding: data, as: UTF8.self).contains("private"))
        let cached = try JSON.decoder.decode(LarkMessage.self, from: data)
        #expect(cached.text == "稍等")
        #expect(cached.parts?.first?.text == "稍等")
        #expect(cached.olk?.bodyParts == nil)
        #expect(cached.olk?.sharesKey == false)
        #expect(cached.replyTo?.displayText == "加密消息 · 仅显示公开内容：我看看")
        #expect(m.displayText == "private body")
    }

    @Test @MainActor func persistedReplyDraftDoesNotStoreDecryptedQuote() throws {
        let draft = LarkDraft(text: "reply", replyTo: LarkReply.quote(try message()))
        let data = try JSON.encoder.encode(draft)
        #expect(!String(decoding: data, as: UTF8.self).contains("private"))
        #expect(try JSON.decoder.decode(LarkDraft.self, from: data).replyTo?.displayText == "加密消息 · 仅显示公开内容：稍等")
    }

    @Test func handoutOnlyAndEmptyClosedMessagesHaveReadableFallbacks() throws {
        var m = try message()
        m.olk?.bodyParts = []
        #expect(m.displayText == "分发了本群的加密钥匙")
        m.olk?.state = "locked"
        m.olk?.publicParts = []
        #expect(m.displayText == "加密消息")
    }

    @Test(arguments: [
        #"{"version":1,"state":"locked"}"#,
        #"{"version":1,"state":"locked","publicParts":null}"#,
        #"{"version":1,"state":"locked","publicParts":{}}"#,
        #"{"state":"decrypted","bodyParts":[{"kind":"text","text":"private"}]}"#,
        #"{"version":"future","state":"decrypted","bodyParts":[{"kind":"text","text":"private"}]}"#,
        #"{"version":1,"bodyParts":[{"kind":"text","text":"private"}]}"#,
        #""invalid""#,
    ])
    @MainActor func partialEncryptionDoesNotDropTheMessagePage(_ projection: String) throws {
        let json = """
        {"messages":[
          {"id":"plain","position":"1","sender_name":"成员","is_mine":false,"type":"TEXT","text":"普通消息","recalled":false,"reply_count":0},
          {"id":"encrypted","position":"2","sender_name":"成员","is_mine":false,"type":"TEXT","text":"private legacy","recalled":false,"reply_count":0,"olk":\(projection)}
        ],"has_older":false}
        """
        let page = try JSON.decoder.decode(APIClient.LarkMessages.self, from: Data(json.utf8))
        #expect(page.messages.count == 2)
        #expect(page.messages[0].displayText == "普通消息")
        #expect(page.messages[1].displayText == "加密消息")
        #expect(page.messages[1].olk?.isOpen == false)
        #expect(page.messages[1].olk?.publicParts == [])
    }

    @Test func cachedCoverClearlyDiffersFromAuthorizedBody() throws {
        let cached = try #require(message().olk?.cached)
        #expect(!cached.isOpen)
        #expect(cached.coverLabel == "加密消息 · 仅显示公开内容")
        #expect(cached.visibleParts.first?.text == "稍等")
    }

    @Test(arguments: ["locked", "missing-key", "forbidden", "unavailable", "future-state"])
    func closedQuoteLabelsItsCover(_ state: String) throws {
        var quote = try #require(message().replyTo)
        quote.olk?.state = state
        let label = state == "unavailable" ? "加密消息 · 仅显示公开内容" : "加密消息 · 对外展示"
        #expect(quote.displayText == "\(label)：我看看")
        quote.olk?.publicParts = []
        #expect(quote.displayText == "加密消息")
    }

    @Test func oldServersAndOrdinaryMessagesKeepTheirOriginalText() throws {
        var m = try message()
        m.olk = nil
        m.replyTo = nil
        m.text = "加密群密钥分发消息"
        let decoded = try JSON.decoder.decode(LarkMessage.self, from: JSON.encoder.encode(m))
        #expect(decoded == m)
        #expect(decoded.displayText == "加密群密钥分发消息")
    }

    @Test(arguments: ["image", "emotion"])
    func mediaOnlyCoverUsesOneLabelInLiveAndCachedQuotes(_ kind: String) throws {
        var quote = try #require(message().replyTo)
        quote.olk?.state = "locked"
        quote.olk?.publicParts = [.init(kind: kind, url: "/api/v1/lark/media/mock-cover")]
        #expect(quote.displayText == "加密消息 · 对外展示")
        let cached = try JSON.decoder.decode(LarkReplyTo.self, from: JSON.encoder.encode(quote))
        #expect(cached.displayText == "加密消息 · 仅显示公开内容")
        #expect(cached.olk?.bodyParts == nil)
    }
}
