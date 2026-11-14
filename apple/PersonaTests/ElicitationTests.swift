import Foundation
import Testing
@testable import Persona
import PersonaWire

/// MCP forms and links on the iPhone (#2322, docs/protocol/protocol.md "MCP elicitation"): the same rules as
/// web/src/lib/elicitation.test.ts, the wire samples, the receipts, the fields' order, the hello that asks the hub for
/// these cards, and the places that must never answer them (the watch, Siri).
@MainActor
struct ElicitationTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    /// The web test's schema, in its order.
    private static let schema = #"""
    {"type": "object", "required": ["name", "count"], "properties": {
      "name": {"type": "string", "title": "名字", "minLength": 2, "default": "张三"},
      "count": {"type": "integer", "minimum": 1, "maximum": 5},
      "ratio": {"type": "number", "multipleOf": 0.1},
      "ok": {"type": "boolean", "default": true},
      "color": {"type": "string", "enum": ["r", "g"], "enumNames": ["红", "绿"]},
      "tags": {"type": "array", "items": {"type": "string", "enum": ["a", "b", "c"]}, "maxItems": 2},
      "token": {"type": "string", "format": "password", "default": "never"},
      "mail": {"type": "string", "format": "email"},
      "day": {"type": "string", "format": "date"}}}
    """#

    private static func json(_ text: String) throws -> JSONValue { try JSON.decoder.decode(JSONValue.self, from: Data(text.utf8)) }

    /// A form card as a WebSocket `interaction` event delivers it (so its fields keep the server's order).
    private static func form(_ schema: String = schema, extra: String = "") throws -> Interaction {
        let event = #"{"type": "interaction", "interaction": {"id": "ix_1", "machine_id": "m", "session_id": "s", "kind": "elicitation", "mcp_server_name": "example-mcp""#
            + extra + #", "elicitation": {"mode": "form", "message": "请填写", "requested_schema": "# + schema + "}}}"
        guard case .interaction(let ix)? = ServerEvent.decode(Data(event.utf8)) else { throw CocoaError(.coderReadCorrupt) }
        return ix
    }

    private static func link(_ url: String?) -> Interaction {
        Interaction(id: "ix_2", machineID: "m", sessionID: "s", kind: .elicitation,
                    elicitation: ElicitationRequest(mode: "url", message: "请打开链接完成确认", url: url))
    }

    private static let url = "https://example.com/authorize"

    // MARK: The form

    @Test func fieldsRequiredAndDefaultsWithoutTheSecret() throws {
        let ix = try Self.form()
        let schema = ix.elicitation?.requestedSchema
        let list = Elicitation.fields(schema, order: ix.elicitation?.fieldOrder)
        #expect(list.map(\.key) == ["name", "count", "ratio", "ok", "color", "tags", "token", "mail", "day"])
        #expect(list[0].title == "名字" && list[0].required)
        #expect(list.first { $0.key == "token" }?.secret == true)
        #expect(list.first { $0.key == "tags" }?.choices == [.string("a"), .string("b"), .string("c")])
        #expect(list.first { $0.key == "color" }?.label(.string("g")) == "绿")
        #expect(Elicitation.defaults(schema) == ["name": .string("张三"), "ok": .bool(true)])
        #expect(Elicitation.supported(schema))
    }

    @Test func checksEachFieldAndReadsNumbersFromText() throws {
        let schema = try Self.json(Self.schema)
        let v = Elicitation.validate(schema, values: ["name": .string("李四"), "count": .string("3"), "ratio": .string("0.3"),
                                                      "tags": .array([.string("a"), .string("b")]), "mail": .string("me@example.com"),
                                                      "day": .string("2026-02-28")])
        #expect(v.valid)
        #expect(v.content == ["name": .string("李四"), "count": .number(3), "ratio": .number(0.3), "tags": .array([.string("a"), .string("b")]),
                              "mail": .string("me@example.com"), "day": .string("2026-02-28")])
        let bad = Elicitation.validate(schema, values: ["name": .string("李"), "count": .string("1.5"), "ratio": .string("0.35"),
                                                        "tags": .array([.string("a"), .string("b"), .string("c")]), "mail": .string("nope"),
                                                        "day": .string("2026-02-30")])
        #expect(!bad.valid)
        #expect(Set(bad.errors.keys) == ["count", "day", "mail", "name", "ratio", "tags"])
        #expect(bad.errors["name"]?.text == "请检查填写内容和格式")
        #expect(Elicitation.validate(schema, values: [:]).errors == ["name": .required, "count": .required])
        #expect(Elicitation.Problem.required.text == "请填写这一项")
        #expect(Elicitation.validate(schema, values: ["name": .string("李四"), "count": .string("9")]).errors["count"] != nil)
        #expect(Elicitation.validate(schema, values: ["name": .string("李四"), "count": .string("3"), "color": .string("b")]).errors["color"] != nil)
        // a default number, as the form starts with it
        #expect(Elicitation.validate(schema, values: ["name": .string("李四"), "count": .number(2)]).valid)
    }

    @Test func formatsAndBounds() throws {
        let schema = try Self.json(#"""
        {"type": "object", "properties": {"at": {"type": "string", "format": "date-time"}, "site": {"type": "string", "format": "uri"},
          "n": {"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1}, "word": {"type": "string", "maxLength": 3}}}
        """#)
        func errors(_ values: [String: JSONValue]) -> Set<String> { Set(Elicitation.validate(schema, values: values).errors.keys) }
        #expect(errors(["at": .string("2026-01-01T09:00:00Z"), "site": .string("https://example.com"), "n": .string("0.5"), "word": .string("一二三")]).isEmpty)
        #expect(errors(["at": .string("2026-01-01T09:00:00.123+08:00")]).isEmpty)
        #expect(errors(["at": .string("2026-01-01 09:00"), "site": .string("example.com"), "n": .string("1"), "word": .string("一二三四")])
                == ["at", "site", "n", "word"])
        #expect(errors(["n": .string("0")]) == ["n"])
        #expect(errors(["n": .string("abc")]) == ["n"])
    }

    @Test func aSchemaOutsideTheSubsetCannotBeSubmitted() throws {
        for text in [#"{"type": "object", "properties": {}, "oneOf": []}"#,
                     #"{"type": "object", "properties": {"x": {"type": "object"}}}"#,
                     #"{"type": "object", "properties": {"x": {"type": "string", "pattern": "^a"}}}"#,
                     #"{"type": "object", "properties": {"x": {"type": "string", "format": "ipv4"}}}"#,
                     #"{"type": "object", "properties": {}, "required": ["missing"]}"#,
                     #"{"type": "object", "properties": {"x": {"type": "array", "items": {"type": "string"}}}}"#] {
            #expect(!Elicitation.supported(try Self.json(text)), "\(text)")
        }
        #expect(!Elicitation.supported(nil))
        let ix = try Self.form(#"{"type": "object", "properties": {"x": {"type": "string", "pattern": "^a"}}}"#)
        #expect(Elicitation.resolution(ix, .accept, values: ["x": .string("a")]) == nil)
        #expect(Elicitation.resolution(ix, .decline)?.action == "decline")
    }

    @Test func submitDeclineAndCancel() throws {
        let ix = try Self.form()
        #expect(Elicitation.resolution(ix, .accept, values: ["name": .string("李"), "count": .string("2")]) == nil)
        let accepted = Elicitation.resolution(ix, .accept, values: ["name": .string("李四"), "count": .string("2"), "ok": .bool(false)])
        #expect(accepted?.action == "accept")
        #expect(accepted?.content == ["name": .string("李四"), "count": .number(2), "ok": .bool(false)])
        #expect(Elicitation.resolution(ix, .decline)?.action == "decline")
        #expect(Elicitation.resolution(ix, .cancel)?.action == "cancel")
        var question = ix
        question.kind = .question
        #expect(Elicitation.resolution(question, .decline) == nil)
    }

    /// What goes to the hub: the action, with a form's accept its content, and nothing else.
    @Test func theAnswerOnTheWire() throws {
        let ix = try Self.form()
        let accept = try #require(Elicitation.resolution(ix, .accept, values: ["name": .string("李四"), "count": .string("2"), "tags": .array([.string("a")])]))
        let sent = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(accept)) as? [String: Any])
        #expect(Set(sent.keys) == ["action", "content"])
        let content = try #require(sent["content"] as? [String: Any])
        #expect(content["name"] as? String == "李四" && content["count"] as? Int == 2 && content["tags"] as? [String] == ["a"])
        let declined = try #require(Elicitation.resolution(ix, .decline))
        let decline = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(declined)) as? [String: Any])
        #expect(decline as? [String: String] == ["action": "decline"])
    }

    // MARK: The link

    @Test func aLinkContinuesOnlyOnceThisCardOpenedIt() {
        let ix = Self.link(Self.url)
        #expect(Elicitation.resolution(ix, .accept) == nil)
        #expect(Elicitation.resolution(ix, .accept, opened: Self.url)?.action == "accept")
        #expect(Elicitation.resolution(ix, .accept, opened: Self.url)?.content == nil)
        #expect(Elicitation.resolution(Self.link(Self.url + "?new=1"), .accept, opened: Self.url) == nil)
        #expect(Elicitation.resolution(ix, .cancel)?.action == "cancel")
    }

    @Test func onlyHTTPLinksWithAHostAndNoAccount() {
        #expect(Elicitation.urlTarget(Self.url) == Self.url)
        #expect(Elicitation.urlHost(Self.url) == "example.com")
        for bad in ["javascript:alert(1)", "https:///missing-host", "https:example.com", "https://user:pw@example.com/", "ftp://example.com/", ""] {
            #expect(Elicitation.urlTarget(bad) == nil, "\(bad)")
            #expect(Elicitation.resolution(Self.link(bad), .accept, opened: bad) == nil)
        }
        #expect(Elicitation.urlTarget(nil) == nil)
    }

    // MARK: Words

    @Test func whoAsks() throws {
        #expect(Elicitation.sourceLines(try Self.form(extra: #", "agent_type": "worker", "mcp_config_source": "project""#))
                == ["来自子 Agent · worker", "来自 MCP 服务器 example-mcp（配置来源：project）"])
        #expect(Elicitation.sourceLines(try Self.form()) == ["来自 MCP 服务器 example-mcp"])
        #expect(Elicitation.serverLabel("plugin_persona") == "Persona")
        #expect(Elicitation.serverLabel("plugin_docs") == "docs")
        let ix = try Self.form()
        #expect(ix.title == "MCP 表单等你填写")
        #expect(Self.link(Self.url).title == "MCP 链接等你确认")
        #expect(ix.askLine(includeToolName: true) == "请填写")
        #expect(ix.requiresInput)
    }

    @Test func receiptsSayWhatWasFilledInButNeverTheSecret() throws {
        let ix = try Self.form()
        let lines = Answered.answerLines(ix, Resolution(action: "accept", content: ["name": .string("李四"), "ok": .bool(false), "color": .string("g"),
                                                                                    "tags": .array([.string("a"), .string("b")]), "token": .string("x")]))
        #expect(lines == [AnswerLine(label: "名字", answer: "李四"), AnswerLine(label: "ok", answer: "否"), AnswerLine(label: "color", answer: "绿"),
                          AnswerLine(label: "tags", answer: "a，b"), AnswerLine(label: "token", answer: "（已输入）")])
        #expect(Answered.answerLines(ix, Resolution(action: "decline")) == [AnswerLine(label: "", answer: "已拒绝")])
        #expect(Answered.answerLines(ix, Resolution(action: "cancel")) == [AnswerLine(label: "", answer: "已取消")])
        #expect(Answered.answerLines(ix, Resolution(action: "accept")) == [AnswerLine(label: "", answer: "已提交")])
        #expect(Answered.answerLines(Self.link(Self.url), Resolution(action: "accept")) == [AnswerLine(label: "", answer: "已打开链接，继续")])
        #expect(Answered.answerLines(ix, .dismissed) == [AnswerLine(label: "", answer: "交回电脑处理")])
    }

    @Test func aDraftKeepsWhatWasTypedAndOpenedButNeverTheSecret() throws {
        let schema = try Self.json(Self.schema)
        let draft = Elicitation.keepable(Elicitation.Draft(values: ["name": .string("李四"), "token": .string("secret")], opened: "https://example.com/a"),
                                         schema: schema)
        #expect(draft == Elicitation.Draft(values: ["name": .string("李四")], opened: "https://example.com/a"))
        ElicitationDrafts.keep(draft, for: "g|ix_d", pending: [])
        #expect(ElicitationDrafts.draft("g|ix_d") == draft)
        // a card no longer pending is forgotten with the next one kept
        ElicitationDrafts.keep(draft, for: "g|ix_e", pending: ["g|ix_e"])
        #expect(ElicitationDrafts.draft("g|ix_d") == nil)
        #expect(ElicitationDrafts.draft("g|ix_e") == draft)
    }

    // MARK: The fields' order

    @Test func theFieldsKeepTheServersOrderFromTheEventAndTheList() throws {
        let keys = ["zeta", "alpha", "mid", "b", "a", "env", "replicas", "notify", "regions", "owner", "token"]
        let properties = keys.map { #""\#($0)": {"type": "string"}"# }.joined(separator: ", ")
        let schema = #"{"type": "object", "properties": {"# + properties + "}}"
        let ix = try Self.form(schema)
        #expect(ix.elicitation?.fieldOrder == keys)
        #expect(Elicitation.fields(ix.elicitation?.requestedSchema, order: ix.elicitation?.fieldOrder).map(\.key) == keys)
        // the pending list's shape, with another card around it
        let list = #"{"interactions": [{"id": "q", "machine_id": "m", "session_id": "s", "kind": "question"}, {"id": "ix_9", "machine_id": "m", "session_id": "s", "kind": "elicitation", "elicitation": {"mode": "form", "requested_schema": "#
            + schema + "}}]}"
        let decoded = try JSON.decoder.decode(APIClient.InteractionsResponse.self, from: Data(list.utf8)).interactions
        let ordered = Elicitation.withFieldOrders(decoded, from: Data(list.utf8))
        #expect(ordered.map(\.id) == ["q", "ix_9"])
        #expect(ordered[1].elicitation?.fieldOrder == keys)
        // without an order (broken JSON next to it): by key, never a crash
        #expect(Elicitation.withFieldOrders(decoded, from: Data(list.dropLast(3).utf8))[1].elicitation?.fieldOrder == nil)
        #expect(Elicitation.fields(ix.elicitation?.requestedSchema).map(\.key) == keys.sorted())
    }

    @Test func theOrderSurvivesTheAppsOwnCache() throws {
        let ix = try Self.form()
        let again = try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(ix))
        #expect(again.elicitation?.fieldOrder == ix.elicitation?.fieldOrder)
        #expect(again.elicitation?.fieldOrder?.first == "name")
    }

    // MARK: The wire samples

    @Test func decodesTheWireSamples() throws {
        struct Sample: Decodable { var name: String; var input: JSONValue }
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/wire/interactions.json"))
        let samples = try JSONDecoder().decode([Sample].self, from: data)
        func event(_ name: String) throws -> Interaction {
            guard let sample = samples.first(where: { $0.name == name }),
                  case .interaction(let ix)? = ServerEvent.decode(try JSON.encoder.encode(sample.input)) else { throw CocoaError(.coderReadCorrupt) }
            return ix
        }
        let form = try event("interaction_elicitation_form")
        #expect(form.kind == .elicitation && form.id == "ix_elicitation_form")
        #expect(form.mcpServerName == "example-mcp" && form.agentID == "agent_example" && form.agentType == "worker")
        #expect(form.elicitation?.isForm == true && form.elicitation?.message == "请确认示例请求")
        #expect(Elicitation.supported(form.elicitation?.requestedSchema))
        #expect(Elicitation.fields(form.elicitation?.requestedSchema).map(\.key) == ["choice"])
        #expect(Elicitation.resolution(form, .accept, values: ["choice": .string("yes")])?.content == ["choice": .string("yes")])
        let url = try event("interaction_elicitation_url")
        #expect(url.kind == .elicitation && url.elicitation?.isURL == true)
        #expect(url.elicitation?.url == "https://example.com/confirm" && url.elicitation?.elicitationID == "request_example")
        #expect(Elicitation.urlHost(url.elicitation?.url) == "example.com")
    }

    // MARK: Who gets these cards

    /// The hub sends these cards only to a connection that declared them, and lists them in a REST answer only to a
    /// client whose X-Persona-Client matches that connection's hello.
    @Test func onlyTheIOSAppDeclaresItWithTheHeadersPlatformAndVersion() {
        let app = RequestClientMetadata(platform: .iOS, surface: .app, version: "1.2.3+4")
        #expect(app.platform == "ios-app" && app.version == "1.2.3+4")
        #expect(app.platform + "/" + app.version == app.value)
        #expect(LiveConnection.helloFeatures(app).contains("elicitation"))
        #expect(LiveConnection.helloFeatures(app).contains(HubNaming.feature))
        for other in [RequestClientMetadata(platform: .macOS, surface: .app, version: "1"), RequestClientMetadata(platform: .visionOS, surface: .app, version: "1"),
                      RequestClientMetadata(platform: .watchOS, surface: .app, version: "1"), RequestClientMetadata(platform: .iOS, surface: .systemExtension, version: "1")] {
            #expect(!LiveConnection.helloFeatures(other).contains("elicitation"), "\(other.value)")
        }
    }

    /// The apps that draw a chat declare rings (#3068); the watch and the extensions don't, and get stacked bars.
    @Test func theChatAppsDeclareRings() {
        for app in [RequestClientMetadata(platform: .iOS, surface: .app, version: "1"), RequestClientMetadata(platform: .macOS, surface: .app, version: "1"),
                    RequestClientMetadata(platform: .visionOS, surface: .app, version: "1")] {
            #expect(LiveConnection.helloFeatures(app).contains("persona_card_ring"), "\(app.value)")
        }
        for other in [RequestClientMetadata(platform: .watchOS, surface: .app, version: "1"), RequestClientMetadata(platform: .iOS, surface: .systemExtension, version: "1")] {
            #expect(!LiveConnection.helloFeatures(other).contains("persona_card_ring"), "\(other.value)")
        }
    }

    /// The watch can't fill them in, and a watch build without the kind would lose the whole summary over one.
    @Test func theWatchSummaryLeavesThemOut() throws {
        let model = AppModel()
        model.registry = HubRegistry(hubs: [PairedHub(id: "elicitbox", config: ServerConfig(url: URL(string: "http://hub.example.com")!, token: "t"))])
        model.buildLinks()
        var form = try Self.form()
        form.machineID = "elicitbox"
        let question = Interaction(id: "ix_q", machineID: "elicitbox", sessionID: "s", kind: .question)
        model.replaceInteractions("", [question, form])
        #expect(model.watchSummary().interactions.map(\.id) == ["ix_q"])
    }

    @Test func siriNeverAnswersThem() throws {
        let ix = try Self.form()
        #expect(ApproveFirstIntent.resolution(for: ix) == nil)
        #expect(ApproveFirstIntent.resolution(for: Self.link(Self.url)) == nil)
        #expect(ApproveFirstIntent.doneText(for: ix) == IntentText.needsApp(.elicitation))
    }
}
