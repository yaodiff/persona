package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

/** New agents follow the "New Agent" model configuration and start in the background (docs/protocol/protocol.md "New session"). */
class AgentCreationModelTest {
    @Test fun anAsyncStartWithoutLauncherFollowsTheConfigurationAndCarriesTheNameHint() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"session":{"machine_id":"mac","session_id":"new","status":"idle","starting":true}}"""))
            val result = service.start(AgentStartRequest("mac", "/code", "", async = true, nameHint = "看下首页"),
                "00000000-0000-4000-8000-000000000003")
            val created = (result as AgentStartResult.Created).session
            assertTrue(created.starting)
            val body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
            assertEquals("", (body.getValue("launcher").jsonPrimitive.content).zhAny())
            assertEquals(true, body.getValue("async").jsonPrimitive.boolean)
            assertEquals("看下首页", (body.getValue("name_hint").jsonPrimitive.content).zhAny())
            assertFalse(body.containsKey("prompt"))
        }
    }

    @Test fun theNewAgentPurposeIsReadAndOldHubsHaveNone() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"purpose":{"id":"agent.new","name":"新建 Agent","kind":"launcher",
                "effective":{"provider":"launcher.claude","model":"model-chat"},"revision":1},
                "providers":[{"id":"launcher.claude","name":"claude"}]}"""))
            val detail = requireNotNull(service.newAgentModel())
            assertEquals(listOf("claude", "model-chat"), (detail.purpose.summaryParts(detail.providers)).zhAny())
            val request = server.takeRequest()
            assertEquals("/api/v1/model-config/purposes/agent.new", (request.path).zhAny())
            assertEquals("Bearer test-token", (request.getHeader("Authorization")).zhAny())
            server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
            assertNull(service.newAgentModel())
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"error":"bad gateway"}"""))
            assertEquals(502, (runCatching { service.newAgentModel() }.exceptionOrNull() as HubFailure).status)
        }
    }

    @Test fun aLaunchersModelsAreReadAskedAgainAndAbsentOnOldHubs() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"agent":"claude","models":[
                {"id":"default","name":"Default (recommended)","description":"Use the default model","resolved":"claude-opus-5-5",
                 "default":true,"efforts":["low","medium","high","xhigh","max"]},
                {"id":"haiku","name":"Haiku 5.5","resolved":"claude-haiku-5-5"},
                {"id":"gateway/glm-5.3","name":"glm-5.3","description":"From gateway","efforts":["low","medium","high"],"future":1}]}"""))
            val list = requireNotNull(service.launcherModels("machine/one", "my launcher"))
            assertEquals("claude", (list.agent).zhAny())
            assertNull(list.locked)
            assertEquals(listOf("default", "haiku", "gateway/glm-5.3"), (list.models.map { it.id }).zhAny())
            assertEquals(listOf(true, false, false), list.models.map { it.isDefault })
            assertEquals("claude-opus-5-5", (list.models[0].resolved).zhAny())
            assertEquals(emptyList<String>(), (list.models[1].efforts).zhAny())
            assertEquals(listOf("low", "medium", "high"), (list.models[2].efforts).zhAny())
            val first = server.takeRequest()
            assertEquals("/api/v1/machines/machine%2Fone/launchers/my%20launcher/models", (first.requestUrl?.encodedPath).zhAny())
            assertNull(first.requestUrl?.queryParameter("refresh"))
            assertEquals("Bearer test-token", (first.getHeader("Authorization")).zhAny())

            server.enqueue(MockResponse().setBody("""{"agent":"codex","locked":"启动器固定了模型参数，不能覆盖具体模型","models":[
                {"id":"gpt-5.5","name":"GPT-5.5","default":true,"efforts":["low","medium"],"default_effort":"medium"}]}"""))
            val codex = requireNotNull(service.launcherModels("mac", "codex", refresh = true))
            assertEquals("启动器固定了模型参数，不能覆盖具体模型", (codex.locked).zhAny())
            assertEquals("medium", (codex.models.single().defaultEffort).zhAny())
            assertEquals("1", (server.takeRequest().requestUrl?.queryParameter("refresh")).zhAny())

            // An agent without a list, a hub without the endpoint, and a CLI that did not answer.
            server.enqueue(MockResponse().setBody("""{"agent":"traex","models":[]}"""))
            assertEquals(LauncherModels("traex"), service.launcherModels("mac", "traex"))
            server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
            assertNull(service.launcherModels("mac", "claude"))
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"error":"claude: command not found"}"""))
            val failure = runCatching { service.launcherModels("mac", "claude") }.exceptionOrNull() as HubFailure
            assertEquals(502, failure.status)
            assertEquals("claude: command not found", (failure.message).zhAny())
        }
    }

    @Test fun aFriendlyGptLabelStillSendsTheOriginalModelId() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"agent":"codex","models":[{"id":"gpt-6.1-sol","name":"GPT-6.1-sol"}]}"""))
            val model = requireNotNull(service.launcherModels("box", "test-launcher")).models.single()
            assertEquals("6.1 Sol", (model.label).zhAny())
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"session":{"machine_id":"box","session_id":"gpt"}}"""))
            service.start(AgentStartRequest("box", "/srv/app", "test-launcher", model = model.id),
                "00000000-0000-4000-8000-000000000006")
            val body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
            assertEquals("gpt-6.1-sol", (body.getValue("model").jsonPrimitive.content).zhAny())
            assertEquals("test-launcher", (body.getValue("launcher").jsonPrimitive.content).zhAny())
        }
    }

    @Test fun aPickedModelGoesWithTheStartAndNoneIsLeftOut() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"session":{"machine_id":"mac","session_id":"a"}}"""))
            service.start(AgentStartRequest("mac", "/code", "claude", model = "gateway/glm-5.3"), "00000000-0000-4000-8000-000000000004")
            val picked = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
            assertEquals("gateway/glm-5.3", (picked.getValue("model").jsonPrimitive.content).zhAny())
            server.enqueue(MockResponse().setBody("""{"session":{"machine_id":"mac","session_id":"b"}}"""))
            service.start(AgentStartRequest("mac", "/code", "claude"), "00000000-0000-4000-8000-000000000005")
            assertFalse(JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject.containsKey("model"))
        }
    }

    /** A new agent's own worktree (#4532): status, add and remove, their bodies, and an older hub's status is none. */
    @Test fun worktreesAreAskedForMadeAndRemoved() = runBlocking {
        MockWebServer().use { server ->
            val service = HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"worktree":true,"ours":true,"path":"/wt/x","branch":"persona/x","root":"/code",
                "main_branch":"main","checkouts":"/wt","dirty":2,"unmerged":1,"extra":true}"""))
            assertEquals(WorktreeStatus(true, true, "/wt/x", "persona/x", "/code", "main", "/wt", 2, 1), service.worktreeStatus("mac", "/wt/x"))
            server.takeRequest().let {
                assertEquals("/api/v1/machines/mac/worktree/status", it.path)
                assertEquals("""{"cwd":"/wt/x"}""", it.body.readUtf8())
            }
            server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
            assertNull(service.worktreeStatus("mac", "/code"))
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"path":"/wt/persona-fix","branch":"persona/fix","root":"/code"}"""))
            assertEquals(WorktreeAdded("/wt/persona-fix", "persona/fix", "/code"), service.addWorktree("mac", "/code", "Fix it", " "))
            assertEquals(buildJsonObject { put("cwd", "/code"); put("name_hint", "Fix it") },
                JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
            server.enqueue(MockResponse().setBody("""{"removed":true,"branch_deleted":true}"""))
            assertEquals(WorktreeRemoved(true, true), service.removeWorktree("mac", "/wt/x", deleteBranch = true, force = false))
            server.takeRequest().let {
                assertEquals("/api/v1/machines/mac/worktree/remove", it.path)
                assertEquals(buildJsonObject { put("path", "/wt/x"); put("delete_branch", true) }, JsonCodec.parseToJsonElement(it.body.readUtf8()))
            }
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"a live agent is in it","code":"worktree_in_use"}"""))
            assertEquals("worktree_in_use", (runCatching { service.removeWorktree("mac", "/wt/x", false, false) }.exceptionOrNull() as HubFailure).code)
            server.takeRequest()
            // The engine the agent opens in or ran in, which makes and removes its worktree, goes as the kind.
            server.enqueue(MockResponse().setBody("""{"worktree":true,"ours":false,"path":"/wt/x","dirty":0,"unmerged":0}"""))
            service.worktreeStatus("mac", "/wt/x", "paseo")
            assertEquals(buildJsonObject { put("cwd", "/wt/x"); put("kind", "paseo") }, JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
            server.enqueue(MockResponse().setBody("""{"path":"/p/persona-fix","branch":"persona/fix","root":"/code"}"""))
            service.addWorktree("mac", "/code", "Fix it", "", "paseo")
            assertEquals(buildJsonObject { put("cwd", "/code"); put("name_hint", "Fix it"); put("kind", "paseo") },
                JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
            server.enqueue(MockResponse().setBody("""{"removed":true,"branch_deleted":false}"""))
            service.removeWorktree("mac", "/p/persona-fix", deleteBranch = false, force = false, kind = "paseo")
            assertEquals(buildJsonObject { put("path", "/p/persona-fix"); put("kind", "paseo") }, JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
        }
    }
}
