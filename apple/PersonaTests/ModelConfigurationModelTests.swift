import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct ModelConfigurationModelTests {
    private static let purpose = #"{"id":"agent.new","category_id":"agent.new","uses":[{"name":"AI 分身执行"}],"name":"Agents","group":"默认模型","kind":"launcher","override":{},"effective":{"provider":"launcher.strong","model":"model-a"},"revision":0,"fields":["provider","model"],"provider_ids":["launcher.strong"],"inherited_from":"助手"}"#
    private static let providers = #"[{"id":"launcher.strong","name":"strong","kind":"launcher","model":"model-a"},{"id":"legacy.ai","name":"hub API","kind":"api","model":"model-b"}]"#
    private static let detail = #"{"purpose":\#(purpose),"providers":\#(providers)}"#
    private static let lark = #"{"id":"lark.agent","category_id":"agent.new","parent":"agent.new","name":"飞书 /agent","group":"Agents","kind":"launcher","override":{},"effective":{"provider":"launcher.strong","model":"model-a","effort":"medium"},"revision":7,"fields":["provider","model","effort"],"provider_ids":["launcher.strong","legacy.ai"],"inherited_from":"Agents"}"#
    private static let larkDetail = #"{"purpose":\#(lark),"providers":\#(providers)}"#
    /// The Inbox's AI twin as a hub that lets it be set apart sends it (#4232).
    private static let twin = lark.replacingOccurrences(of: "lark.agent", with: "inbox.agent").replacingOccurrences(of: "飞书 /agent", with: "AI 分身执行")
        .replacingOccurrences(of: #""revision":7"#, with: #""revision":0"#)
    private static let twinDetail = #"{"purpose":\#(twin),"providers":\#(providers)}"#

    private func make(_ hub: FakeHub) -> ModelConfigurationModel { .init(deps: .init(api: { hub.client })) }

    @Test func loadsCategoriesAndReadOnlyScopeFromTheServer() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(Self.purpose)],"providers":\#(Self.providers)}"#)
        let page = make(hub)
        await page.load()
        #expect(page.loaded && page.error == nil)
        #expect(page.categories.map(\.id) == ["agent.new"])
        #expect(page.categories.first?.uses?.first?.name == "AI 分身执行")
        #expect(page.summary(page.purposes[0]) == "strong · model-a")
        #expect(page.modelSummary(page.purposes[0]) == "model-a")
    }

    @Test func writableChildStaysIndependentWithoutUsesAndAppearsInTheCatalog() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(Self.purpose),\#(Self.lark)],"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: Self.larkDetail)
        let page = make(hub)
        await page.load()
        #expect(page.categories.map(\.id) == ["agent.new"])
        #expect(page.independentPurposes.map(\.id) == ["lark.agent"])
        await page.load(purposeID: "lark.agent")
        let purpose = try #require(page.purpose)
        #expect(page.error == nil && purpose.id == "lark.agent" && purpose.uses == nil)
        #expect(purpose.categoryID == "agent.new" && purpose.parent == "agent.new")
        #expect(purpose.revision == 7 && page.isLarkAgent)
        #expect(hub.sent("GET", "/api/v1/model-config/purposes/agent.new").isEmpty)
        #expect(page.candidates.map(\.id) == ["launcher.strong", "legacy.ai"])
        #expect(page.inheritanceStatus(purpose) == String(localized: "Follow Agents"))
    }

    @Test func returningFromTheModelPickerKeepsTheCompleteIndependentDraft() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: Self.larkDetail)
        let page = make(hub)
        await page.loadIfNeeded(purposeID: "lark.agent")
        let draft = ModelBinding(provider: "launcher.strong", model: "lark-model", effort: "high")
        page.draft = draft
        await page.loadIfNeeded(purposeID: "lark.agent")
        #expect(page.draft == draft && page.dirty)
        #expect(hub.sent("GET", "/api/v1/model-config/purposes/lark.agent").count == 1)
        await page.load(purposeID: "lark.agent")
        #expect(!page.dirty && page.draft.isEmpty)
        #expect(hub.sent("GET", "/api/v1/model-config/purposes/lark.agent").count == 2)
    }

    @Test func aLegacyRedirectIsAlsoLoadedOnlyOnceWhenReturningFromItsPicker() async {
        let hub = FakeHub()
        let legacy = Self.lark.replacingOccurrences(of: #"["provider","model","effort"]"#, with: "[]")
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: #"{"purpose":\#(legacy),"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = make(hub)
        await page.loadIfNeeded(purposeID: "lark.agent")
        page.draft.model = "unsaved-category-model"
        await page.loadIfNeeded(purposeID: "lark.agent")
        #expect(page.purpose?.id == "agent.new" && page.draft.model == "unsaved-category-model")
        #expect(hub.sent("GET", "/api/v1/model-config/purposes/agent.new").count == 1)
    }

    @Test func independentSaveAndResetUseTheirOwnRevisionAndExplicitNulls() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: Self.larkDetail)
        var saved = try JSON.decoder.decode(ModelPurposeDetail.self, from: Data(Self.larkDetail.utf8))
        var updates = 0
        hub.on("PATCH", "/api/v1/model-config/purposes/lark.agent") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as! [String: Any]
            #expect(Set(body.keys) == ["revision", "provider", "model", "effort"])
            #expect(body["revision"] as? Int == saved.purpose.revision)
            if saved.purpose.revision == 7 {
                #expect(body["provider"] as? String == "launcher.strong")
                #expect(body["model"] as? String == "lark-model")
                #expect(body["effort"] as? String == "high")
                saved.purpose.override = .init(provider: "launcher.strong", model: "lark-model", effort: "high")
                saved.purpose.effective = saved.purpose.override
                saved.purpose.inheritedFrom = nil
            } else {
                #expect(saved.purpose.revision == 8)
                #expect(body["provider"] is NSNull && body["model"] is NSNull && body["effort"] is NSNull)
                saved.purpose.override = .init()
                saved.purpose.effective = .init(provider: "launcher.strong", model: "model-a", effort: "medium")
                saved.purpose.inheritedFrom = "Agents"
            }
            saved.purpose.revision += 1
            let purpose = String(decoding: try JSON.encoder.encode(saved.purpose), as: UTF8.self)
            return Data(#"{"purpose":\#(purpose),"providers":\#(Self.providers)}"#.utf8)
        }
        let page = ModelConfigurationModel(deps: .init(api: { hub.client }, updated: { updates += 1 }))
        await page.load(purposeID: "lark.agent")
        page.chooseProvider("launcher.strong")
        page.draft.model = " lark-model "
        page.draft.effort = " high "
        #expect(await page.save())
        #expect(page.purpose?.id == "lark.agent" && page.purpose?.revision == 8)
        #expect(page.inheritanceStatus(try #require(page.purpose)) == String(localized: "Separately configured"))
        page.restoreInheritance()
        #expect(page.draft.isEmpty && page.dirty)
        #expect(await page.save())
        let reset = try #require(page.purpose)
        #expect(reset.id == "lark.agent" && reset.revision == 9 && reset.override.isEmpty)
        #expect(reset.effective.model == "model-a" && reset.effective.effort == "medium")
        #expect(page.inheritanceStatus(reset) == String(localized: "Follow Agents"))
        #expect(!page.dirty && updates == 2)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/lark.agent").count == 2)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/agent.new").isEmpty)
    }

    /// The AI twin is set apart like Lark /agent: listed beside it, its own record and revision, Follow Agents to reset.
    @Test func theAITwinIsAnotherIndependentUseOfAgents() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(Self.purpose),\#(Self.twin),\#(Self.lark)],"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/inbox.agent", json: Self.twinDetail)
        var saved = try JSON.decoder.decode(ModelPurposeDetail.self, from: Data(Self.twinDetail.utf8))
        hub.on("PATCH", "/api/v1/model-config/purposes/inbox.agent") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as! [String: Any]
            #expect(body["revision"] as? Int == saved.purpose.revision)
            if body["provider"] is NSNull {
                saved.purpose.override = .init()
                saved.purpose.inheritedFrom = "Agents"
            } else {
                saved.purpose.override = .init(provider: body["provider"] as? String, model: body["model"] as? String, effort: body["effort"] as? String)
                saved.purpose.inheritedFrom = nil
            }
            saved.purpose.revision += 1
            let purpose = String(decoding: try JSON.encoder.encode(saved.purpose), as: UTF8.self)
            return Data(#"{"purpose":\#(purpose),"providers":\#(Self.providers)}"#.utf8)
        }
        let page = make(hub)
        await page.load()
        #expect(page.categories.map(\.id) == ["agent.new"])
        #expect(page.independentPurposes.map(\.id) == ["inbox.agent", "lark.agent"])
        #expect(page.independentPurposes.allSatisfy(ModelConfigurationModel.followsAgents))
        #expect(!ModelConfigurationModel.followsAgents(page.categories[0]))
        await page.load(purposeID: "inbox.agent")
        let twin = try #require(page.purpose)
        #expect(twin.id == "inbox.agent" && twin.revision == 0 && page.isAgentsOverride && !page.isLarkAgent)
        #expect(page.inheritanceStatus(twin) == String(localized: "Follow Agents"))
        page.chooseProvider("launcher.strong")
        page.draft.model = "twin-model"
        page.draft.effort = "low"
        #expect(await page.save())
        #expect(page.purpose?.revision == 1)
        #expect(page.inheritanceStatus(try #require(page.purpose)) == String(localized: "Separately configured"))
        page.restoreInheritance()
        #expect(await page.save())
        let reset = try #require(page.purpose)
        #expect(reset.revision == 2 && reset.override.isEmpty)
        #expect(page.inheritanceStatus(reset) == String(localized: "Follow Agents"))
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/inbox.agent").count == 2)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/agent.new").isEmpty)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/lark.agent").isEmpty)
    }

    /// A hub from before #4232 keeps the twin a read-only use: it is not listed apart and opens Agents.
    @Test func anOlderHubsTwinStaysAUseOfAgents() async {
        let hub = FakeHub()
        let readOnly = Self.twin.replacingOccurrences(of: #"["provider","model","effort"]"#, with: "[]")
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(Self.purpose),\#(Self.lark)],"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/inbox.agent", json: #"{"purpose":\#(readOnly),"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = make(hub)
        await page.load()
        #expect(page.independentPurposes.map(\.id) == ["lark.agent"])
        await page.load(purposeID: "inbox.agent")
        #expect(page.purpose?.id == "agent.new" && !page.isAgentsOverride)
    }

    @Test func independentConflictRetainsAllDraftFieldsAndBlocksAnotherWrite() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: Self.larkDetail)
        hub.on("PATCH", "/api/v1/model-config/purposes/lark.agent") { _ in throw APIError(status: 409, message: "changed") }
        let page = make(hub)
        await page.load(purposeID: "lark.agent")
        let draft = ModelBinding(provider: "legacy.ai", model: "lark-model", effort: "high")
        page.draft = draft
        #expect(await page.save() == false)
        #expect(page.conflict && page.dirty && page.draft == draft && page.purpose?.revision == 7)
        #expect(await page.save() == false)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/lark.agent").count == 1)
        await page.load(purposeID: "lark.agent")
        #expect(!page.conflict && !page.dirty && page.purpose?.id == "lark.agent")
    }

    @Test func launcherEffortIsSupportedForMessagesProvidersButDirectAPIRulesStayTheSame() async {
        let hub = FakeHub()
        let providers = #"[{"id":"legacy.ai","name":"Messages API","kind":"api","api":"messages"},{"id":"api.responses","name":"Responses API","kind":"api","api":"responses"}]"#
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: #"{"purpose":\#(Self.lark),"providers":\#(providers)}"#)
        let apiPurpose = Self.lark.replacingOccurrences(of: "lark.agent", with: "api.child").replacingOccurrences(of: "\"kind\":\"launcher\"", with: "\"kind\":\"api\"")
        hub.on("GET", "/api/v1/model-config/purposes/api.child", json: #"{"purpose":\#(apiPurpose),"providers":\#(providers)}"#)
        let page = make(hub)
        await page.load(purposeID: "lark.agent")
        page.chooseProvider("legacy.ai")
        #expect(page.allowsEffort)
        await page.load(purposeID: "api.child")
        page.chooseProvider("legacy.ai")
        #expect(!page.allowsEffort)
        page.chooseProvider("api.responses")
        #expect(page.allowsEffort)
    }

    @Test func legacyChildWithoutExplicitCapabilitiesStillRedirects() async {
        let hub = FakeHub()
        let legacy = Self.lark.replacingOccurrences(of: #","fields":["provider","model","effort"]"#, with: "")
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: #"{"purpose":\#(legacy),"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = make(hub)
        await page.load(purposeID: "lark.agent")
        #expect(page.purpose?.id == "agent.new" && page.error == nil)
    }

    @Test func oldPurposeLinksOpenTheirCategoryInsteadOfAnIndividualEditor() async {
        let hub = FakeHub()
        let use = #"{"id":"lark.agent","category_id":"agent.new","name":"飞书 /agent","group":"Agents","kind":"launcher","override":{},"effective":{},"revision":0,"fields":[]}"#
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: #"{"purpose":\#(use),"providers":\#(Self.providers)}"#)
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = make(hub)
        await page.load(purposeID: "lark.agent")
        #expect(page.error == nil && page.purpose?.id == "agent.new")
        #expect(page.purpose?.supports("model") == true)
        #expect(hub.sent("GET", "/api/v1/model-config/purposes/agent.new").count == 1)
    }

    @Test func oldHubDeepLinksCannotReintroduceIndividualEditing() async {
        let hub = FakeHub()
        let old = #"{"id":"lark.agent","name":"飞书 /agent","group":"对话","kind":"launcher","override":{},"effective":{},"revision":0,"fields":["provider","model"]}"#
        hub.on("GET", "/api/v1/model-config/purposes/lark.agent", json: #"{"purpose":\#(old),"providers":[]}"#)
        let page = make(hub)
        await page.load(purposeID: "lark.agent")
        #expect(page.purpose == nil && page.error?.contains("更新服务器") == true)
        #expect(await page.save() == false)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/lark.agent").isEmpty)
    }

    @Test func aPreCategoryCatalogRequiresAHubUpdate() async {
        let hub = FakeHub()
        let old = #"{"id":"assistant","name":"主对话","group":"对话","kind":"launcher","override":{},"effective":{},"revision":0}"#
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(old)],"providers":[]}"#)
        let page = make(hub)
        await page.load()
        #expect(page.categories.isEmpty)
        #expect(page.error?.contains("更新服务器") == true)
    }

    /// A use whose connection the hub holds (docs/assistant/model-config.md「Upstream rate limits」): the time as Go writes it, counted down.
    @Test func decodesAHeldConnectionAndCountsItDown() async throws {
        let held = #"{"id":"inbox.triage","category_id":"inbox.triage","uses":[{"name":"收件箱分拣"}],"name":"信息筛选","group":"默认模型","kind":"api","override":{},"effective":{"provider":"legacy.ai"},"revision":0,"status":"额度用完，23 分钟后重试","limit":{"reason":"quota","until":"2026-11-02T20:13:00.123456789+08:00"}}"#
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config", json: #"{"purposes":[\#(held)],"providers":\#(Self.providers)}"#)
        let page = make(hub)
        await page.load()
        let limit = try #require(page.purposes.first?.limit)
        #expect(limit.reason == "quota")
        let until = limit.until
        #expect(abs(until.timeIntervalSince1970 - 1_793_621_580.123) < 0.01)
        #expect(limit.text(now: until.addingTimeInterval(-23 * 60 + 1)) == "额度用完，23 分钟后重试")
        #expect(limit.text(now: until.addingTimeInterval(-45)) == "额度用完，45 秒后重试")
        #expect(limit.text(now: until.addingTimeInterval(1)) == "额度用完，稍后重试")
        #expect(ModelLimit(reason: "busy", until: until).text(now: until.addingTimeInterval(-5)) == "并发已满，5 秒后重试")
        #expect(page.purposes.first?.status == "额度用完，23 分钟后重试")
    }

    @Test func legacyHubCannotBecomeAnEmptyEditableConfiguration() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config") { _ in throw APIError(status: 404, message: "not found") }
        let page = make(hub)
        await page.load()
        #expect(page.loaded && page.error?.contains("更新服务器") == true)
        #expect(!page.loading && page.purpose == nil)
        #expect(await page.save() == false)
        #expect(hub.sent("PATCH", "/api/v1/model-config/purposes/agent.new").isEmpty)
    }

    @Test func candidateProvidersRespectThePurposeCapabilities() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        let page = make(hub)
        await page.load(purposeID: "agent.new")
        #expect(page.candidates.map(\.id) == ["launcher.strong"])
        #expect(page.purpose?.supports("effort") == false)
        #expect(!page.dirty)
    }

    @Test func changingProvidersDoesNotCarryTheOldModelOrEffort() async {
        let page = make(FakeHub())
        page.draft = .init(provider: "old", model: "model-a", effort: "high")
        page.chooseProvider("new")
        #expect(page.draft == .init(provider: "new"))
        page.chooseProvider("")
        #expect(page.draft.isEmpty)
    }

    @Test func saveSendsTheRevisionNullInheritanceAndOnlySupportedFields() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        hub.on("PATCH", "/api/v1/model-config/purposes/agent.new") { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as! [String: Any]
            #expect(body["revision"] as? Int == 0)
            #expect(body["provider"] is NSNull)
            #expect(body["model"] as? String == "model-c")
            #expect(body["effort"] == nil)
            return Data(Self.detail.utf8)
        }
        let page = make(hub)
        await page.load(purposeID: "agent.new")
        page.draft.model = " model-c "
        page.draft.effort = "high"
        #expect(await page.save())
        #expect(page.error == nil && !page.dirty && !page.saving)
    }

    @Test func failedSaveKeepsTheDraftAndConflictRequiresReload() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/model-config/purposes/agent.new", json: Self.detail)
        hub.on("PATCH", "/api/v1/model-config/purposes/agent.new") { _ in throw APIError(status: 409, message: "changed") }
        let page = make(hub)
        await page.load(purposeID: "agent.new")
        page.draft.model = "my-model"
        #expect(await page.save() == false)
        #expect(page.draft.model == "my-model" && page.conflict && page.dirty)
        #expect(!page.saving)
        await page.load(purposeID: "agent.new")
        #expect(!page.conflict && page.error == nil && !page.dirty)
    }

    @Test func aTaskTimeEditDoesNotOverwriteCentralModelChoices() throws {
        var task = APIClient.ScheduledTask(id: "task_1", title: "早报", prompt: "写早报")
        task.launcher = "old"
        task.effort = "low"
        task.includesModelSettings = false
        let body = try JSONSerialization.jsonObject(with: JSON.encoder.encode(task)) as! [String: Any]
        #expect(body["launcher"] == nil && body["effort"] == nil)
        #expect(body["title"] as? String == "早报")
    }
}
