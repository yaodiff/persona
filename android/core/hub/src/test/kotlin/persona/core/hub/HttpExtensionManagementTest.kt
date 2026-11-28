package persona.core.hub

import persona.core.contracts.api.ExtensionConfigRejected
import persona.core.contracts.api.ExtensionPreview
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiUnsupported
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class HttpExtensionManagementTest {
    // Most of these talk to a hub from before #1844 (/api/v1/plugins and the old keys), the one the routes were first
    // written for; aNewHubAnswersUnderExtensions is the same calls on a newer hub.
    private fun <T> withHub(legacy: Boolean = true, block: suspend (MockWebServer, HttpExtensionManagement) -> T): T = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpExtensionManagement({ path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }, device = { device }, legacy = legacy))
        }
    }

    @Test fun aNewHubAnswersUnderExtensions() = withHub(legacy = false) { server, api ->
        server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"example-usage","name":"Example Usage","kind":"ui","status":"running"}],
            "can_install":true,"markets":true}"""))
        val listing = api.listing()
        assertEquals("/api/v1/extensions", (server.takeRequest().path).zhAny())
        assertEquals(listOf("example-usage"), (listing.extensions.map { it.id }).zhAny())
        assertTrue(listing.canInstall && listing.markets)

        server.enqueue(MockResponse().setBody("""{"ui":[{"extension":"example-usage","extension_name":"Example Usage","id":"usage","title":"用量",
            "placement":"nav_page"}]}"""))
        val place = api.uiPlaces("").single()
        assertEquals("/api/v1/extensions/-/ui", (server.takeRequest().path).zhAny())
        assertEquals("example-usage", (place.extension).zhAny())
        assertEquals("Example Usage", (place.extensionName).zhAny())

        server.enqueue(MockResponse().setBody(detail))
        api.extension("example-notes")
        assertEquals("/api/v1/extensions/example-notes/-/?entry_lists=1", (server.takeRequest().path).zhAny())

        server.enqueue(MockResponse().setBody("""{"markets":[{"id":"m1","spec":"github:example/team","name":"团队","extensions":3,
            "signature":{"state":"verified","key_id":"32ed46cdbe349656"}}]}"""))
        val market = api.markets().single()
        assertEquals("/api/v1/extensions/-/markets", (server.takeRequest().path).zhAny())
        assertEquals(3, market.extensions)
        assertTrue(market.signature!!.verified)
        assertFalse(market.official)

        // The official market (#2351): the hub lists it first and says so; the order is kept as the hub gave it.
        server.enqueue(MockResponse().setBody("""{"markets":[{"id":"o1","spec":"github:example/official","name":"官方扩展","extensions":2,"official":true},
            {"id":"m1","spec":"github:example/team","name":"团队","extensions":3}]}"""))
        val ordered = api.markets()
        server.takeRequest()
        assertEquals(listOf("o1", "m1"), (ordered.map { it.id }).zhAny())
        assertTrue(ordered[0].official)
        assertFalse(ordered[1].official)

        server.enqueue(MockResponse().setBody("""{"markets":[],"extensions":[{"market":"m1","id":"linuxdo","name":"LINUX DO","version":"0.1.0"}]}"""))
        assertEquals(listOf("linuxdo"), (api.catalog("").extensions.map { it.id }).zhAny())
        assertEquals("/api/v1/extensions/-/catalog", (server.takeRequest().path).zhAny())

        server.enqueue(MockResponse().setBody("""{"preview_id":"pv1","permissions_digest":"d1","market":"m1"}"""))
        api.previewFromMarket("m1", "linuxdo")
        server.takeRequest().let {
            assertEquals("/api/v1/extensions/-/catalog/preview", (it.path).zhAny())
            assertEquals("""{"market":"m1","extension":"linuxdo"}""", (it.body.readUtf8()).zhAny())
        }
    }

    @Test fun catalogLogosAndPopularityRemainOptionalAndKeepTheHubOrder() = withHub(legacy = false) { server, api ->
        val timestamp = "2026-11-11T08:00:00Z"
        val fields = """"icon_url":"https://extensions.example.com/logo.png","downloads":4294967296,"downloads_updated_at":"$timestamp""""
        server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"popular","name":"Zeta",$fields},{"id":"zero","downloads":0},{"id":"old"}]}"""))
        val installed = api.listing().extensions
        server.takeRequest()
        assertEquals(listOf("popular", "zero", "old"), installed.map { it.id })
        assertEquals("https://extensions.example.com/logo.png", installed[0].iconUrl)
        assertEquals(4294967296L, installed[0].downloads)
        assertEquals(timestamp, installed[0].downloadsUpdatedAt)
        assertEquals(0L, installed[1].downloads)
        assertNull(installed[2].downloads)
        assertNull(installed[2].iconUrl)
        assertNull(installed[2].downloadsUpdatedAt)

        server.enqueue(MockResponse().setBody("""{"id":"popular",$fields}"""))
        val detail = api.extension("popular").extension
        server.takeRequest()
        assertEquals(installed[0].iconUrl, detail.iconUrl)
        assertEquals(installed[0].downloads, detail.downloads)
        assertEquals(timestamp, detail.downloadsUpdatedAt)

        server.enqueue(MockResponse().setBody("""{"markets":[{"id":"m1","downloads_updated_at":"$timestamp"},{"id":"older-market"}],"extensions":[
            {"id":"popular","name":"Zeta",$fields},{"id":"alpha","name":"Alpha","downloads":12},
            {"id":"zero","downloads":0},{"id":"old"}]}"""))
        val catalog = api.catalog()
        server.takeRequest()
        assertEquals(listOf("popular", "alpha", "zero", "old"), catalog.extensions.map { it.id })
        assertEquals(timestamp, catalog.markets[0].downloadsUpdatedAt)
        assertNull(catalog.markets[1].downloadsUpdatedAt)
        assertEquals(installed[0].iconUrl, catalog.extensions[0].iconUrl)
        assertEquals(installed[0].downloads, catalog.extensions[0].downloads)
        assertEquals(timestamp, catalog.extensions[0].downloadsUpdatedAt)
        assertEquals(0L, catalog.extensions[2].downloads)
        assertNull(catalog.extensions[3].downloads)

        server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"old"}]}"""))
        val old = api.catalog()
        server.takeRequest()
        assertTrue(old.markets.isEmpty())
        assertNull(old.extensions.single().iconUrl)
        assertNull(old.extensions.single().downloads)
    }

    @Test fun downloadCountsDecodeAsIntegersWithoutTruncation() = withHub(legacy = false) { server, api ->
        server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"maximum","downloads":9223372036854775807}]}"""))
        assertEquals(Long.MAX_VALUE, api.catalog().extensions.single().downloads)
        server.takeRequest()
        for (value in listOf("1.5", "9223372036854775808")) {
            server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"invalid","downloads":$value}]}"""))
            assertTrue(runCatching { api.catalog() }.exceptionOrNull() is HubFailure)
            server.takeRequest()
        }
    }

    @Test fun savesAnExplicitReferenceAndRemovesAConfirmedHandoffRule() = withHub { server, api ->
        server.enqueue(MockResponse().setBody(detail))
        api.setSecretReference("example-notes", "api_key", "op://Dev/Notes/new")
        server.takeRequest().let {
            assertEquals("PUT", it.method)
            assertEquals("/api/v1/plugins/example-notes/-/secrets/api_key?entry_lists=1", it.path)
            assertEquals("""{"reference":"op://Dev/Notes/new"}""", it.body.readUtf8())
        }
        server.enqueue(MockResponse().setBody(detail))
        api.removeHandoffRule("example-vault", "0123456789abcdef")
        server.takeRequest().let {
            assertEquals("DELETE", it.method)
            assertEquals("/api/v1/plugins/example-vault/-/handoff-rules/0123456789abcdef?entry_lists=1", it.path)
        }
        assertTrue(runCatching { api.removeHandoffRule("example-vault", "../bad") }.exceptionOrNull() is IllegalArgumentException)
    }

    @Test fun modernRoutesSaveAReferenceAndRemoveAHandoffRule() = withHub(legacy = false) { server, api ->
        server.enqueue(MockResponse().setBody(detail))
        api.setSecretReference("example-notes", "api_key", "op://Dev/Notes/new")
        server.takeRequest().let {
            assertEquals("PUT", it.method)
            assertEquals("/api/v1/extensions/example-notes/-/secrets/api_key?entry_lists=1", it.path)
            assertEquals("""{"reference":"op://Dev/Notes/new"}""", it.body.readUtf8())
        }
        server.enqueue(MockResponse().setBody(detail))
        api.removeHandoffRule("example-vault", "0123456789abcdef")
        server.takeRequest().let {
            assertEquals("DELETE", it.method)
            assertEquals("/api/v1/extensions/example-vault/-/handoff-rules/0123456789abcdef?entry_lists=1", it.path)
        }
    }

    @Test fun entryListOptInAndPreservingImmediateWrites() = withHub(legacy = false) { server, api ->
        repeat(4) { server.enqueue(MockResponse().setBody(detail)) }
        api.setConfig("example-notes", buildJsonObject { put("x", 1) })
        server.takeRequest().let {
            assertEquals("PUT", it.method)
            assertEquals("/api/v1/extensions/example-notes/-/config?entry_lists=1", it.path)
            assertEquals("""{"config":{"x":1}}""", it.body.readUtf8())
        }
        api.setConfigPreservingEntries("example-notes", buildJsonObject { put("x", 2) })
        server.takeRequest().let {
            assertEquals("PUT", it.method)
            assertEquals("/api/v1/extensions/example-notes/-/config?entry_lists=1&preserve_entries=1", it.path)
            assertEquals("""{"config":{"x":2}}""", it.body.readUtf8())
        }
        val entryId = "entry_0123456789abcdef0123456789"
        api.setSecret("example-notes", entryId, "placeholder-secret-5678")
        server.takeRequest().let {
            assertEquals("PUT", it.method)
            assertEquals("/api/v1/extensions/example-notes/-/secrets/$entryId?entry_lists=1", it.path)
            assertEquals("""{"value":"placeholder-secret-5678"}""", it.body.readUtf8())
        }
        api.clearSecret("example-notes", entryId)
        server.takeRequest().let {
            assertEquals("DELETE", it.method)
            assertEquals("/api/v1/extensions/example-notes/-/secrets/$entryId?entry_lists=1", it.path)
        }

        server.enqueue(MockResponse().setResponseCode(400).setBody(
            """{"error":"accounts[0].address: must be a URL","code":"invalid_config","field":"accounts[0].address","message":"must be a URL"}"""))
        val refused = runCatching { api.setConfig("example-notes", JsonObject(emptyMap())) }.exceptionOrNull() as ExtensionConfigRejected
        assertEquals("accounts[0].address", refused.field)
        assertEquals("must be a URL", refused.message)
        assertEquals("/api/v1/extensions/example-notes/-/config?entry_lists=1", server.takeRequest().path)
    }

    // What the phone says of itself in every view's context; a fixed one so the bodies are exact.
    private val device = ExtensionUiContext(tz = "Asia/Shanghai", locale = "zh-Hans-CN", platform = "android",
        uiVersion = 12)
    private val deviceJson = """"tz":"Asia/Shanghai","locale":"zh-Hans-CN","platform":"android","ui_version":12"""

    private val detail = """{"id":"example-notes","name":"Example Notes","kind":"connectors","status":"needs_setup","version":"1.2.0",
        "source":"installed","enabled":true,"description":"同步笔记","contributes":{"connectors":["examplenotes"]},
        "needs_setup":["密钥「API Key」"],"publisher":"example","homepage":"https://example.com/p","origin":"dir:/opt/p",
        "previous_version":"1.1.0","read_only":false,
        "form":{"fields":[{"name":"base_url","type":"string","title":"服务地址","format":"uri","default":"https://notes.example.com","required":true},
          {"name":"mode","type":"string","enum":[{"value":"fast","title":"只要新的"},{"value":"safe"}]},
          {"name":"tags","type":"array","items":{"type":"string","enum":[{"value":"a","title":"甲"}]},"min_items":1},
          {"name":"proxy","type":"object","title":"代理","fields":[{"name":"port","type":"integer","default":8080,"minimum":1}]}]},
        "config":{"folders":["工作"]},
        "secrets":[{"name":"api_key","title":"API Key","required":true,"set":false}],
        "permissions":[{"key":"runs_code","title":"在 hub 机器上运行程序","detail":"不在沙箱里"}],
        "unknown_key":1}"""

    @Test fun listsTheExtensionsWithTheNewFieldsAndToleratesAnOlderHub() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"plugins":[
            {"id":"voice","name":"语音","kind":"voice","status":"running"},
            {"id":"example-notes","name":"Example Notes","kind":"connectors","status":"needs_setup","source":"installed",
             "enabled":true,"needs_setup":["密钥「API Key」"],"contributes":{"connectors":["examplenotes"]},"warnings":["w"]}],
            "can_install":true}"""))
        val list = api.extensions()
        assertEquals("/api/v1/plugins", (server.takeRequest().path).zhAny())
        assertEquals(listOf("voice", "example-notes"), (list.map { it.id }).zhAny())
        assertNull(list[0].source)
        assertEquals("installed", (list[1].source).zhAny())
        assertEquals(listOf("密钥「API Key」"), (list[1].needsSetup).zhAny())
        assertEquals(mapOf("connectors" to listOf("examplenotes")), list[1].contributes)
        assertEquals(listOf("w"), (list[1].warnings).zhAny())
    }

    @Test fun decodesTheDetailFromOneObject() = withHub { server, api ->
        server.enqueue(MockResponse().setBody(detail))
        val d = api.extension("example-notes")
        assertEquals("/api/v1/plugins/example-notes/-/?entry_lists=1", (server.takeRequest().path).zhAny())
        assertEquals("Example Notes", (d.extension.name).zhAny())
        assertEquals("1.1.0", (d.previousVersion).zhAny())
        assertFalse(d.readOnly)
        assertEquals(listOf("base_url", "mode", "tags", "proxy"), (d.form.map { it.name }).zhAny())
        assertEquals(JsonPrimitive("https://notes.example.com"), d.form[0].fallback)
        assertEquals(listOf("只要新的", "safe"), (d.form[1].enum!!.map { it.label }).zhAny())
        assertEquals("甲", (d.form[2].items!!.enum!!.single().label).zhAny())
        assertEquals(1, d.form[2].minItems)
        assertEquals(JsonPrimitive(8080), d.form[3].fields!!.single().fallback)
        assertEquals(buildJsonObject { putJsonArray("folders") { add("工作") } }, d.config)
        assertFalse(d.secrets.single().set)
        assertEquals("runs_code", (d.permissions.single().key).zhAny())
    }

    @Test fun aHubJsonExtensionHasNoConfigOrSecrets() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"id":"voice","name":"语音","kind":"voice","status":"running","source":"hub.json","read_only":true}"""))
        val d = api.extension("voice")
        assertTrue(d.readOnly)
        assertTrue(d.form.isEmpty() && d.config.isEmpty() && d.secrets.isEmpty())
    }

    @Test fun switchesSavesAndSetsSecrets() = withHub { server, api ->
        repeat(4) { server.enqueue(MockResponse().setBody(detail)) }
        api.setEnabled("example-notes", false)
        server.takeRequest().let {
            assertEquals("PUT", (it.method).zhAny()); assertEquals("/api/v1/plugins/example-notes/-/enabled?entry_lists=1", (it.path).zhAny())
            assertEquals("""{"enabled":false}""", (it.body.readUtf8()).zhAny())
        }
        api.setConfig("example-notes", buildJsonObject { put("base_url", "https://x.example.com") })
        server.takeRequest().let {
            assertEquals("PUT", (it.method).zhAny()); assertEquals("/api/v1/plugins/example-notes/-/config?entry_lists=1", (it.path).zhAny())
            assertEquals("""{"config":{"base_url":"https://x.example.com"}}""", (it.body.readUtf8()).zhAny())
        }
        api.setSecret("example-notes", "api_key", "s3cret")
        server.takeRequest().let {
            assertEquals("PUT", (it.method).zhAny()); assertEquals("/api/v1/plugins/example-notes/-/secrets/api_key?entry_lists=1", (it.path).zhAny())
            assertEquals("""{"value":"s3cret"}""", (it.body.readUtf8()).zhAny())
        }
        api.clearSecret("example-notes", "api_key")
        server.takeRequest().let { assertEquals("DELETE", (it.method).zhAny()); assertEquals("/api/v1/plugins/example-notes/-/secrets/api_key?entry_lists=1", (it.path).zhAny()) }
    }

    @Test fun aRefusedFieldComesBackAsItsPathAndReason() = withHub { server, api ->
        server.enqueue(MockResponse().setResponseCode(400).setBody(
            """{"error":"proxy.host: 要是主机名","code":"invalid_config","field":"proxy.host","message":"要是主机名"}"""))
        val refused = runCatching { api.setConfig("example-notes", JsonObject(emptyMap())) }.exceptionOrNull()
        assertTrue(refused is ExtensionConfigRejected)
        assertEquals("proxy.host", ((refused as ExtensionConfigRejected).field).zhAny())
        assertEquals("要是主机名", (refused.message).zhAny())
        server.takeRequest()

        // Another 400 is the hub's error as usual.
        server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"bad json"}"""))
        val other = runCatching { api.setConfig("example-notes", JsonObject(emptyMap())) }.exceptionOrNull() as HubFailure
        assertEquals(400, other.status)
        assertEquals("bad json", (other.message).zhAny())
    }

    // OkHttpHubConnection.request throws the hub's error itself instead of returning the response (found on the
    // emulator: the refused field showed as a page error); the field must survive that.
    @Test fun aRefusedFieldSurvivesTheConnectionThrowingFirst() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            val api = HttpExtensionManagement({ path, method, body ->
                val response = http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
                if (!response.isSuccessful) throw response.use { HubFailure.from(it) }
                response
            }, legacy = true)
            server.enqueue(MockResponse().setResponseCode(400).setBody(
                """{"error":"base_url: 要是一个网址","code":"invalid_config","field":"base_url","message":"要是一个网址"}"""))
            val refused = runCatching { api.setConfig("example-notes", JsonObject(emptyMap())) }.exceptionOrNull() as ExtensionConfigRejected
            assertEquals("base_url", (refused.field).zhAny())
            assertEquals("要是一个网址", (refused.message).zhAny())
        }
    }

    @Test fun refusesIdsAndNamesThatWouldBecomeAnotherPath() = withHub { server, api ->
        assertTrue(runCatching { api.extension("..") }.exceptionOrNull() is IllegalArgumentException)
        assertTrue(runCatching { api.setSecret("example-notes", "..", "x") }.exceptionOrNull() is IllegalArgumentException)
        assertTrue(runCatching { api.clearSecret("a/b", "api_key") }.exceptionOrNull() is IllegalArgumentException)
        assertEquals(0, server.requestCount)
    }

    @Test fun anUnreadableAnswerIsAProtocolError() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"nope":true}"""))
        assertEquals(HubError.Protocol, (runCatching { api.extensions() }.exceptionOrNull() as HubFailure).kind)
        server.enqueue(MockResponse().setBody("not json"))
        assertEquals(HubError.Protocol, (runCatching { api.extension("x") }.exceptionOrNull() as HubFailure).kind)
    }

    @Test fun theListSaysWhetherTheHubHasMarkets() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"plugins":[],"can_install":true,"markets":true}"""))
        val listing = api.listing()
        assertTrue(listing.markets && listing.canInstall)
        server.enqueue(MockResponse().setBody("""{"plugins":[]}"""))
        assertFalse(api.listing().markets)
    }

    // Its pages are its extension_page and nav_page UI (both whole pages); a launcher's section or a card is not one.
    @Test fun theDetailListsItsPagesOnly() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"id":"example-gateway","name":"网关","status":"running","origin":"market:3f2a9c1d0b/example-gateway",
            "ui":[{"plugin":"example-gateway","id":"home","title":"概况","placement":"plugin_page"},
                  {"plugin":"example-gateway","id":"panel","title":"面板","placement":"launcher_section"},
                  {"plugin":"example-gateway","id":"usage","title":"用量","placement":"nav_page","icon":"chart"},
                  {"plugin":"example-gateway","id":"today","title":"今天","placement":"overview_card","opens":"usage"}]}"""))
        val d = api.extension("example-gateway")
        assertEquals(listOf("home", "usage"), (d.ui.map { it.id }).zhAny())
        assertTrue(d.fromMarket)
    }

    @Test fun asksWhereExtensionsDrawAndDrawsWithTheContext() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"ui":[{"plugin":"example-gateway","id":"panel","title":"Example 网关","placement":"launcher_section"}]}"""))
        val places = api.uiPlaces("launcher_section", "work key", "workstation")
        assertEquals("/api/v1/plugins/-/ui?placement=launcher_section&launcher=work%20key&machine=workstation", (server.takeRequest().path).zhAny())
        assertEquals("panel", (places.single().id).zhAny())

        server.enqueue(MockResponse().setBody("""{"view":{"title":"T","state":"s1","sections":[{"title":"概况","items":[
            {"type":"metric","label":"余额","value":"¥ 1","tone":"positive"},
            {"type":"progress","label":"今日","ratio":0.72,"tone":"caution"},
            {"type":"row","title":"Key","badge":{"text":"有效","tone":"positive"},"action":{"id":"key:work"}},
            {"type":"button","title":"停用","role":"destructive","action":{"id":"disable","confirm":"确定？"}},
            {"type":"field","name":"plan","label":"套餐","kind":"choice","value":"team","choices":[{"value":"solo","title":"个人"},{"value":"team"}]},
            {"type":"link","title":"后台","url":"https://example.com"},{"type":"divider"},{"type":"text","text":"x","style":"code"},
            {"type":"newer_kind","whatever":1}]}]}}"""))
        val view = api.renderUi("example-gateway", "panel", ExtensionUiContext("work", "workstation"), state = null)
        server.takeRequest().let {
            assertEquals("POST", (it.method).zhAny())
            assertEquals("/api/v1/plugins/example-gateway/-/ui/panel", (it.path).zhAny())
            assertEquals("""{"context":{"launcher":"work","machine":"workstation",$deviceJson}}""", (it.body.readUtf8()).zhAny())
        }
        val items = view.sections.single().items
        assertEquals("s1", (view.state).zhAny())
        assertEquals(0.72, items[1].ratio!!, 0.0)
        assertEquals("有效", (items[2].badge!!.text).zhAny())
        assertEquals("确定？", (items[3].action!!.confirm).zhAny())
        assertEquals(listOf("个人", "team"), (items[4].choices!!.map { it.label }).zhAny())
        assertEquals("newer_kind", (items.last().type).zhAny())
    }

    @Test fun pressesAButtonWithTheStateAndValues() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"toast":"已保存","open_url":"https://example.com/k"}"""))
        val result = api.uiAction("example-gateway", "home", "save", state = "tab=overview", values = mapOf("note" to "hi", "auto" to "true"))
        assertEquals("""{"context":{$deviceJson},"state":"tab=overview","action":"save","values":{"note":"hi","auto":"true"}}""",
            (server.takeRequest().body.readUtf8()).zhAny())
        assertEquals("已保存", (result.toast).zhAny())
        assertEquals("https://example.com/k", (result.openUrl).zhAny())
        assertNull(result.view)
    }

    // docs/extensions/extensions.md "UI slots": without a placement the hub lists every slot but launcher sections, with badges.
    @Test fun listsTheSlotsAtOnce() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"ui":[
            {"plugin":"example-gateway","plugin_name":"Example 网关面板","id":"usage","title":"用量","placement":"nav_page","size":"full",
             "icon":"chart","badge":{"text":"3","tone":"danger"}},
            {"plugin":"example-gateway","id":"today","title":"今天的用量","placement":"overview_card","size":"compact","opens":"usage"},
            {"plugin":"example-gateway","id":"gpu","title":"网关调用","placement":"machine_section","later":{"x":1}}]}"""))
        val places = api.uiPlaces("")
        assertEquals("/api/v1/plugins/-/ui", (server.takeRequest().path).zhAny())
        assertEquals(listOf("nav_page", "overview_card", "machine_section"), (places.map { it.placement }).zhAny())
        assertEquals("Example 网关面板", (places[0].extensionName).zhAny())
        assertEquals("chart", (places[0].icon).zhAny())
        assertEquals("3", (places[0].badge?.text).zhAny())
        assertEquals("danger", (places[0].badge?.tone).zhAny())
        assertEquals("compact", (places[1].size).zhAny())
        assertEquals("usage", (places[1].opens).zhAny())
        assertNull(places[2].badge)
    }

    // A card asks for the compact size, a machine's section names the machine; the phone's own context goes along.
    @Test fun drawsASlotWithItsContext() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"view":{"sections":[],"refresh_after":300}}"""))
        val view = api.renderUi("example-gateway", "today", ExtensionUiContext(size = "compact"))
        assertEquals("""{"context":{"size":"compact",$deviceJson}}""", (server.takeRequest().body.readUtf8()).zhAny())
        assertEquals(300, view.refreshAfter)
        server.enqueue(MockResponse().setBody("""{"view":{"sections":[]}}"""))
        api.renderUi("example-gateway", "gpu", ExtensionUiContext(machine = "workstation"), state = "range=7d")
        assertEquals("""{"context":{"machine":"workstation",$deviceJson},"state":"range=7d"}""", (server.takeRequest().body.readUtf8()).zhAny())
        // A segmented choice: the action with {name: value}.
        server.enqueue(MockResponse().setBody("""{}"""))
        api.uiAction("example-gateway", "usage", "range", ExtensionUiContext(size = "full"), values = mapOf("range" to "30d"))
        assertEquals("""{"context":{"size":"full",$deviceJson},"action":"range","values":{"range":"30d"}}""", (server.takeRequest().body.readUtf8()).zhAny())
    }

    // An inbox item's page (`inbox_item`, #1683): listed by its placement with the connector it draws for, drawn and
    // pressed with the item's id in the context. An older hub lists nothing there.
    @Test fun drawsAnInboxItemsPageWithTheItem() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"ui":[{"plugin":"larkext","plugin_name":"飞书","id":"event","title":"事件",
            "placement":"inbox_item","size":"full","connector":"lark"}]}"""))
        val places = api.uiPlaces("inbox_item")
        assertEquals("/api/v1/plugins/-/ui?placement=inbox_item", (server.takeRequest().path).zhAny())
        assertEquals("lark", (places.single().connector).zhAny())
        assertEquals("event", (places.single().id).zhAny())

        server.enqueue(MockResponse().setBody("""{"view":{"title":"周四的评审","sections":[{"items":[{"type":"text","text":"x"}]}],"state":"s"}}"""))
        val view = api.renderUi("larkext", "event", ExtensionUiContext(item = "lark:F1:1051"))
        server.takeRequest().let {
            assertEquals("/api/v1/plugins/larkext/-/ui/event", (it.path).zhAny())
            assertEquals("""{"context":{"item":"lark:F1:1051",$deviceJson}}""", (it.body.readUtf8()).zhAny())
        }
        assertEquals("周四的评审", (view.title).zhAny())

        // "View in session": the item's source, the one app link the hub lets this slot answer.
        server.enqueue(MockResponse().setBody("""{"open_url":"persona://inbox/lark:F1:1051/source"}"""))
        assertEquals("persona://inbox/lark:F1:1051/source",
            (api.uiAction("larkext", "event", "open_chat", ExtensionUiContext(item = "lark:F1:1051"), state = "s").openUrl).zhAny())
        assertEquals("""{"context":{"item":"lark:F1:1051",$deviceJson},"state":"s","action":"open_chat"}""", (server.takeRequest().body.readUtf8()).zhAny())

        server.enqueue(MockResponse().setBody("""{"ui":[]}"""))
        assertTrue(api.uiPlaces("inbox_item").isEmpty())

        // A device that may only read and answer is refused (403): the item opens its own page.
        server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"this device can only read and answer"}"""))
        val refused = runCatching { api.renderUi("larkext", "event", ExtensionUiContext(item = "lark:F1:1051")) }.exceptionOrNull()
        assertTrue(refused is persona.core.contracts.api.ExtensionUiForbidden)
        assertEquals("this device can only read and answer", (refused?.message).zhAny())
    }

    @Test fun theDeviceContextIsThePhones() {
        val context = HttpExtensionManagement.deviceContext()
        assertEquals("android", (context.platform).zhAny())
        // Android renders the version 12 map and the version 11 scene.
        assertEquals(12, context.uiVersion)
        assertTrue(context.unsupported.isNullOrEmpty())
        assertEquals(java.util.TimeZone.getDefault().id, context.tz)
        assertFalse(context.locale.isNullOrEmpty())
    }

    // The locale's language is the app's (what Accept-Language says), not the phone's; the region stays the phone's.
    @Test fun theDeviceContextLocaleFollowsTheAppLanguage() {
        val before = java.util.Locale.getDefault()
        try {
            java.util.Locale.setDefault(java.util.Locale.forLanguageTag("en-US"))
            persona.core.contracts.ClientLanguage.current = persona.core.contracts.AppLanguage.Ja
            assertEquals("ja-US", HttpExtensionManagement.deviceContext().locale)
        } finally {
            java.util.Locale.setDefault(before)
            persona.core.contracts.ClientLanguage.current = persona.core.contracts.AppLanguage.En
        }
    }

    // UI version 2's items read; one this app cannot even decode (a newer hub's, a field of another type) is left out
    // and the rest of the view stays: never a blank page.
    @Test fun readsTheNewItemsAndSkipsWhatItCannotRead() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"view":{"refresh_after":600,"sections":[{"items":[
            {"type":"segmented","name":"range","value":"7d","action":{"id":"range"},"choices":[{"value":"1d","title":"今天"},{"value":"7d"}]},
            {"type":"stat_grid","stats":[{"label":"今天","value":"1.2M","detail":"+12%","tone":"caution"},{"label":"费用","value":"¥36.20"}]},
            {"type":"chart","kind":"bar","unit":"tokens","stacked":true,"title":"近 7 天","series":[
              {"name":"输入","points":[{"label":"11-01","value":1200000,"display":"1.2M"},{"label":"11-02","value":-3.5}]},
              {"name":"输出","tone":"positive","points":[{"label":"11-01","value":3},{"label":"11-02","value":4}]}]},
            {"type":"chart","kind":"line","series":"not a list"},
            {"type":"future","ratio":"not a number"},
            "not even an object",
            {"type":"text","text":"still here"}]}]}}"""))
        val view = api.renderUi("example-gateway", "usage")
        val items = view.sections.single().items
        assertEquals(listOf("segmented", "stat_grid", "chart", "text"), (items.map { it.type }).zhAny())
        assertEquals(600, view.refreshAfter)
        assertEquals(listOf("今天", "7d"), (items[0].choices!!.map { it.label }).zhAny())
        assertEquals("caution", (items[1].stats!![0].tone).zhAny())
        val chart = items[2]
        assertEquals(true, chart.stacked)
        assertEquals("tokens", (chart.unit).zhAny())
        assertEquals(-3.5, chart.series!![0].points[1].value, 0.0)
        assertEquals("1.2M", (chart.series!![0].points[0].display).zhAny())
        assertEquals("positive", (chart.series!![1].tone).zhAny())
    }

    // #1339: an extension that says the launcher's gateway is not its own answers 404 unsupported; any other 404 is an error.
    @Test fun anUnsupportedGatewayIsItsOwnFailure() = withHub { server, api ->
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not my gateway","code":"unsupported"}"""))
        assertTrue(runCatching { api.renderUi("example-gateway", "other", ExtensionUiContext("work")) }.exceptionOrNull() is ExtensionUiUnsupported)
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not my gateway","code":"unsupported"}"""))
        assertTrue(runCatching { api.uiAction("example-gateway", "other", "refresh") }.exceptionOrNull() is ExtensionUiUnsupported)
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"no such plugin","code":"gone"}"""))
        val other = runCatching { api.renderUi("gone", "home") }.exceptionOrNull()
        assertTrue(other is HubFailure && other.status == 404)
        // A machine's section for a machine this hub does not know.
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"没有这台机器","code":"not_found"}"""))
        assertTrue(runCatching { api.renderUi("example-gateway", "gpu", ExtensionUiContext(machine = "elsewhere")) }.exceptionOrNull()
            is persona.core.contracts.api.ExtensionUiNotFound)
        server.enqueue(MockResponse().setResponseCode(502).setBody("""{"error":"扩展画的界面这个 hub 画不了","code":"plugin_failed"}"""))
        assertEquals("扩展画的界面这个 hub 画不了", ((runCatching { api.renderUi("x", "home") }.exceptionOrNull() as HubFailure).message).zhAny())
    }

    @Test fun marketsAndTheirTokens() = withHub { server, api ->
        val market = """{"id":"3f2a9c1d0b","spec":"github:example/team","name":"团队","has_token":true,"plugins":2,"warnings":["w"],"added_at":"2026-11-02T08:00:00Z"}"""
        server.enqueue(MockResponse().setBody("""{"markets":[$market]}"""))
        val list = api.markets()
        assertEquals("/api/v1/plugins/-/markets", (server.takeRequest().path).zhAny())
        assertTrue(list.single().hasToken)
        assertEquals(listOf("w"), (list.single().warnings).zhAny())

        server.enqueue(MockResponse().setBody(market))
        api.addMarket("github:example/team", "tok")
        server.takeRequest().let {
            assertEquals("POST", (it.method).zhAny()); assertEquals("/api/v1/plugins/-/markets", (it.path).zhAny())
            assertEquals("""{"spec":"github:example/team","token":"tok"}""", (it.body.readUtf8()).zhAny())
        }
        server.enqueue(MockResponse().setBody(market))
        api.addMarket("https://example.com/p/", "")
        assertEquals("""{"spec":"https://example.com/p/"}""", (server.takeRequest().body.readUtf8()).zhAny())
        server.enqueue(MockResponse().setBody(market))
        api.refreshMarket("3f2a9c1d0b")
        server.takeRequest().let { assertEquals("POST", (it.method).zhAny()); assertEquals("/api/v1/plugins/-/markets/3f2a9c1d0b/refresh", (it.path).zhAny()) }
        server.enqueue(MockResponse().setBody(market))
        api.setMarketToken("3f2a9c1d0b", "")
        server.takeRequest().let {
            assertEquals("PUT", (it.method).zhAny()); assertEquals("/api/v1/plugins/-/markets/3f2a9c1d0b/token", (it.path).zhAny())
            assertEquals("""{"token":""}""", (it.body.readUtf8()).zhAny())
        }
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        api.removeMarket("3f2a9c1d0b")
        server.takeRequest().let { assertEquals("DELETE", (it.method).zhAny()); assertEquals("/api/v1/plugins/-/markets/3f2a9c1d0b", (it.path).zhAny()) }

        server.enqueue(MockResponse().setResponseCode(400).setBody("""{"error":"读不到：私有的，要填 token","code":"market_failed"}"""))
        assertEquals("读不到：私有的，要填 token", ((runCatching { api.addMarket("github:example/private") }.exceptionOrNull() as HubFailure).message).zhAny())
        assertTrue(runCatching { api.removeMarket("../x") }.exceptionOrNull() is IllegalArgumentException)
    }

    @Test fun theCatalogAndInstallingFromIt() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"markets":[],"platform":"linux-amd64","plugins":[
            {"market":"m1","market_name":"Example","id":"example-gateway","name":"网关","version":"1.1.0","tags":["额度"],
             "contributes":{"ui":["home"]},"permissions":[{"key":"runs_code","title":"运行程序"}],"installed":"1.0.0",
             "installed_source":"installed","from_this_market":true,"upgradable":true},
            {"market":"m1","id":"w","name":"W","version":"0.2.0","tags":[],"contributes":{},"permissions":[],"problem":"没有包"}]}"""))
        val catalog = api.catalog("网关 面板", "m1", refresh = true)
        assertEquals("/api/v1/plugins/-/catalog?q=%E7%BD%91%E5%85%B3%20%E9%9D%A2%E6%9D%BF&market=m1&refresh=1", (server.takeRequest().path).zhAny())
        assertEquals("linux-amd64", (catalog.platform).zhAny())
        assertTrue(catalog.extensions[0].upgradable && catalog.extensions[0].fromThisMarket)
        assertEquals("没有包", (catalog.extensions[1].problem).zhAny())
        server.enqueue(MockResponse().setBody("""{"markets":[],"plugins":[]}"""))
        api.catalog()
        assertEquals("/api/v1/plugins/-/catalog", (server.takeRequest().path).zhAny())

        server.enqueue(MockResponse().setBody("""{"preview_id":"pv1","manifest":{"id":"example-gateway","name":"网关","version":"1.1.0","contributes":{}},
            "permissions":[{"key":"runs_code","title":"运行程序"}],"permissions_digest":"d1","expires_at":"2026-11-04T07:10:00Z",
            "upgrade":true,"from":"1.0.0","more_permissions":true,"added_permissions":[{"key":"hub_api","title":"完整使用 hub"}],"market":"m1"}"""))
        val preview = api.previewFromMarket("m1", "example-gateway")
        assertEquals("""{"market":"m1","plugin":"example-gateway"}""", (server.takeRequest().body.readUtf8()).zhAny())
        assertTrue(preview.upgrade && preview.morePermissions)
        assertEquals("hub_api", (preview.addedPermissions!!.single().key).zhAny())
        assertEquals("1.1.0", (preview.manifest!!.version).zhAny())

        server.enqueue(MockResponse().setBody(detail))
        api.upgrade("example-gateway", preview)
        server.takeRequest().let {
            assertEquals("/api/v1/plugins/example-gateway/-/upgrade", (it.path).zhAny())
            assertEquals("""{"preview_id":"pv1","permissions_digest":"d1"}""", (it.body.readUtf8()).zhAny())
        }
        server.enqueue(MockResponse().setBody(detail))
        api.install(ExtensionPreview("pv2", permissionsDigest = "d2"))
        server.takeRequest().let {
            assertEquals("/api/v1/plugins/-/install", (it.path).zhAny())
            assertEquals("""{"preview_id":"pv2","permissions_digest":"d2"}""", (it.body.readUtf8()).zhAny())
        }
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        api.uninstall("example-gateway", keepData = true)
        server.takeRequest().let { assertEquals("DELETE", (it.method).zhAny()); assertEquals("/api/v1/plugins/example-gateway/-/?keep_data=1", (it.path).zhAny()) }
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        api.uninstall("example-gateway", keepData = false)
        assertEquals("/api/v1/plugins/example-gateway/-/", (server.takeRequest().path).zhAny())
        // Like the web: an uninstall keeps the data unless told otherwise.
        server.enqueue(MockResponse().setBody("""{"ok":true}"""))
        api.uninstall("example-gateway")
        assertEquals("/api/v1/plugins/example-gateway/-/?keep_data=1", (server.takeRequest().path).zhAny())
    }

    @Test fun autoUpgradesAndAnExtensionsUpgradeLines() = withHub(legacy = false) { server, api ->
        server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"example-ui","name":"Example UI","status":"running","auto_upgrade":true,
            "upgrade":{"id":"example-ui","market":"m1","from":"1.0.0","to":"1.2.0","state":"needs_permission",
            "added_permissions":[{"key":"net","title":"访问更多网络","detail":"api.example.com"}]},
            "upgrade_note":{"kind":"rolled_back","from":"1.0.0","to":"1.1.0","at":"2026-11-04T06:00:00Z"}}]}"""))
        val extension = api.extensions().single()
        server.takeRequest()
        assertEquals(true, extension.autoUpgrade)
        assertEquals("needs_permission", (extension.upgrade!!.state).zhAny())
        assertEquals("net", (extension.upgrade!!.addedPermissions!!.single().key).zhAny())
        assertEquals("rolled_back", (extension.upgradeNote!!.kind).zhAny())

        server.enqueue(MockResponse().setBody("""{"auto":true,"upgrades":[],"off":[]}"""))
        assertEquals(true, api.autoUpgrades())
        assertEquals("/api/v1/extensions/-/upgrades", (server.takeRequest().path).zhAny())

        server.enqueue(MockResponse().setBody("""{"auto":false}"""))
        assertEquals(false, api.setAutoUpgrades(false))
        server.takeRequest().let {
            assertEquals("PUT", (it.method).zhAny())
            assertEquals("""{"auto":false}""", (it.body.readUtf8()).zhAny())
        }

        server.enqueue(MockResponse().setBody(detail))
        api.setAutoUpgrade("example-ui", false)
        server.takeRequest().let {
            assertEquals("/api/v1/extensions/example-ui/-/auto-upgrade?entry_lists=1", (it.path).zhAny())
            assertEquals("""{"auto":false}""", (it.body.readUtf8()).zhAny())
        }

        // A hub from before auto-upgrades has no route: no switch.
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
        assertNull(api.autoUpgrades())
    }

    @Test fun readsAMachineListAndWhoseProgramItPicks() = withHub(legacy = false) { server, api ->
        server.enqueue(MockResponse().setBody("""{"id":"cua","name":"操控电脑","status":"running","nodes":[{"machine_id":"m-a","state":"running","version":"0.3.0"}],
            "node_machines":"machines","form":{"fields":[{"name":"machines","type":"array","title":"在哪些 Mac 上用",
            "items":{"type":"string","format":"machine","os":["darwin"]},"confirm":"扩展开着时这台 Mac 不会自动锁屏。"}]},"config":{"machines":["m-a"]}}"""))
        val d = api.extension("cua")
        server.takeRequest()
        val field = d.form.single()
        assertTrue(field.items!!.machines)
        assertEquals(listOf("darwin"), (field.items!!.os).zhAny())
        assertEquals("扩展开着时这台 Mac 不会自动锁屏。", (field.confirm).zhAny())
        assertEquals("machines", (d.nodeMachines).zhAny())
        assertEquals("running", (d.extension.nodes!!.single().state).zhAny())
    }

    @Test fun extensionUiCallsUseAReadTimeoutBeyondTheHubsUiTimeout() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            val timeouts = mutableListOf<Pair<String, Long?>>()
            val api = HttpExtensionManagement({ path, method, body, timeout ->
                timeouts += path to timeout
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }, device = { device }, legacy = false)

            server.enqueue(MockResponse().setBody("""{"extensions":[],"can_install":false,"markets":false}"""))
            api.listing()
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"view":{"title":"Markets","sections":[]}}"""))
            assertEquals("Markets", api.renderUi("example-markets", "watchlist").title)
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"view":{"title":"Markets","sections":[]}}"""))
            api.uiAction("example-markets", "watchlist", "refresh")
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"view":{"title":"Subscriptions","sections":[]}}"""))
            api.renderUi(persona.core.contracts.api.SUBSCRIPTIONS_UI, "pages")
            server.takeRequest()

            assertTrue(HttpExtensionManagement.UI_READ_TIMEOUT_SECONDS >= 30L)
            assertEquals(
                listOf(
                    "/api/v1/extensions" to null,
                    "/api/v1/extensions/example-markets/-/ui/watchlist" to HttpExtensionManagement.UI_READ_TIMEOUT_SECONDS,
                    "/api/v1/extensions/example-markets/-/ui/watchlist" to HttpExtensionManagement.UI_READ_TIMEOUT_SECONDS,
                    "/api/v1/subscriptions/-/ui" to HttpExtensionManagement.UI_READ_TIMEOUT_SECONDS,
                ),
                timeouts,
            )
        }
    }
}
