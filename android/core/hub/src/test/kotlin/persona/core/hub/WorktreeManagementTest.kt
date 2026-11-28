package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** Orca's worktree calls on Android (#17054): the routes and bodies the web sends, and how their answers read. */
class WorktreeManagementTest {
    private fun service(server: MockWebServer) =
        requireNotNull(HttpAgentCreationService("hub/pairing", server.url("/").toString(), "test-token", OkHttpClient()).worktrees)
    private fun body(server: MockWebServer): Pair<String, JsonObject> = server.takeRequest().let { request ->
        "${request.method} ${request.path}" to JsonCodec.parseToJsonElement(request.body.readUtf8().ifEmpty { "{}" }).jsonObject
    }
    private fun JsonObject.text(key: String) = this[key]?.jsonPrimitive?.content

    @Test fun prepareAsksForTheComposerAndReadsIt() = runBlocking {
        MockWebServer().use { server ->
            val api = service(server)
            server.enqueue(MockResponse().setBody("""{"root":"/code/app","repoName":"app","branch":"main","path":"/code/.orca","choose_parent":true,
                "automatic_name":true,"composer":{"project":"app","base_ref":"origin/main","refs":["origin/main","origin/dev"],
                "presets":[{"id":"p1","name":"Docs","directories":["docs"]}],"capabilities":["create","source.github","sparse_presets"]}}"""))
            val info = api.prepareWorktree("mac", "repo::/code/app", "orca", composer = true)
            val (route, sent) = body(server)
            assertEquals("POST /api/v1/machines/mac/worktree/prepare", route)
            assertEquals(mapOf("workspace_id" to "repo::/code/app", "kind" to "orca", "composer" to "true"),
                sent.mapValues { it.value.jsonPrimitive.content })
            assertTrue(info.chooseParent && info.automaticName)
            assertEquals(listOf("origin/main", "origin/dev"), info.composer?.refs)
            assertEquals("Docs", info.composer?.presets?.single()?.name)
            assertTrue(info.composer!!.has("source.github"))

            // An older node answers the basic form: no composer, nothing advanced offered.
            server.enqueue(MockResponse().setBody("""{"root":"/code/app","repoName":"app","branch":"","path":"/code/.orca"}"""))
            assertNull(api.prepareWorktree("mac", "repo::/code/app", "orca", composer = false).composer)
            assertEquals("false", body(server).second.text("composer"))
        }
    }

    @Test fun createCarriesItsReceiptAndOnlyTheFieldsSet() = runBlocking {
        MockWebServer().use { server ->
            val api = service(server)
            val id = "00000000-0000-4000-8000-000000000017"
            server.enqueue(MockResponse().setBody("""{"workspace_id":"repo::/code/.orca/login","parent_workspace_id":"repo::/code/app",
                "path":"/code/.orca/login","branch":"feat/login","terminal":"pane:orca:t1:l1"}"""))
            val outcome = api.createWorktree("mac", WorktreeCreateRequest("repo::/code/app", "orca", composer = true, name = "login",
                baseRef = "origin/main", noParent = false, note = "", sourceId = "pr:12", sourceUrl = "https://example.com/pr/12"), id)
            val request = server.takeRequest()
            assertEquals("/api/v1/machines/mac/worktree/create", request.path)
            assertEquals(id, request.getHeader("X-Persona-Op-Id"))
            val sent = JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject
            assertEquals(setOf("workspace_id", "kind", "composer", "name", "base_ref", "source_id", "source_url"), sent.keys)
            assertEquals(true, sent["composer"]?.jsonPrimitive?.boolean)
            val created = (outcome as WorktreeCreateOutcome.Created).worktree
            assertEquals("/code/.orca/login", created.path)
            assertEquals("repo::/code/app", created.parentWorkspaceId)

            server.enqueue(MockResponse().setBody("""{"state":"pending"}"""))
            assertEquals(WorktreeCreateOutcome.Pending, api.createWorktree("mac", WorktreeCreateRequest("w", branch = "b", path = "/p"), id))
            assertEquals(setOf("workspace_id", "branch", "path"), body(server).second.keys)
            server.enqueue(MockResponse().setBody("""{"state":"unknown"}"""))
            assertEquals(WorktreeCreateOutcome.Unknown, api.createWorktree("mac", WorktreeCreateRequest("w", autoName = true, noParent = true), id))
            assertEquals(mapOf("workspace_id" to "w", "no_parent" to "true", "auto_name" to "true"), body(server).second.mapValues { it.value.jsonPrimitive.content })

            // The receipt is read by its id; a refusal keeps the hub's code and what it says reached the computer.
            server.enqueue(MockResponse().setBody("""{"state":"done","kind":"worktree_create"}"""))
            assertEquals("done", api.worktreeReceipt("mac", id))
            assertEquals("GET /api/v1/ops/$id", body(server).first)
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"the computer is offline","code":"machine_offline","delivered":"none"}"""))
            val refused = runCatching { api.createWorktree("mac", WorktreeCreateRequest("w", branch = "b", path = "/p"), id) }.exceptionOrNull()
            assertEquals("none", hubCodedFailure(refused!!)?.delivered)
        }
    }

    @Test fun renameAndAPreviewedDeleteSendTheWebsBodies() = runBlocking {
        MockWebServer().use { server ->
            val api = service(server)
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals("", api.renameWorktree("mac", "w1", "orca", "Login form"))
            assertEquals("POST /api/v1/machines/mac/worktree/rename" to buildJsonObject {
                put("workspace_id", "w1"); put("kind", "orca"); put("name", "Login form")
            }, body(server))

            server.enqueue(MockResponse().setBody("""{"workspace_id":"w1","kind":"orca","host_id":"local","instance_id":"i1","path":"/code/w1",
                "branch":"feat/w1","dirty":3,"folder":false,"preview_token":"t1"}"""))
            val preview = api.previewWorktreeDeletion("mac", "w1", "orca")
            assertEquals(WorktreeDeletionPreview("w1", "orca", "local", "i1", "/code/w1", "feat/w1", 3, false, "t1"), preview)
            assertEquals(buildJsonObject { put("workspace_id", "w1"); put("kind", "orca"); put("preview", true) }, body(server).second)

            server.enqueue(MockResponse().setBody("""{"workspace_id":"w1","removed":true,"state":"removed","branch_deleted":true}"""))
            val result = api.deleteWorktree("mac", WorktreeDeletion("w1", "orca", "t1", force = true, deleteBranch = true))
            assertTrue(result.removed)
            assertEquals(buildJsonObject {
                put("workspace_id", "w1"); put("kind", "orca"); put("preview_token", "t1"); put("force", true); put("delete_branch", true)
            }, body(server).second)

            server.enqueue(MockResponse().setBody("""{"workspace_id":"w1","removed":false,"state":"present"}"""))
            assertEquals("present", api.deleteWorktree("mac", WorktreeDeletion("w1", "orca", "t1", reconcile = true)).state)
            assertEquals(buildJsonObject { put("workspace_id", "w1"); put("kind", "orca"); put("preview_token", "t1"); put("reconcile", true) },
                body(server).second)
        }
    }

    @Test fun sourcesPresetsAndAccessRead() = runBlocking {
        MockWebServer().use { server ->
            val api = service(server)
            server.enqueue(MockResponse().setBody("""{"items":[{"type":"pr","number":12,"title":"Fix","url":"https://example.com/pr/12"}],
                "errors":{"github":{"message":"rate limited"}},"warning":"partial"}"""))
            val found = api.worktreeSources("mac", "w1", "orca", "fix")
            assertEquals("pr:12", found.items.single().sourceId)
            assertEquals("partial · rate limited", found.message)
            assertEquals("fix", body(server).second.text("query"))

            server.enqueue(MockResponse().setBody("""{"preset":{"id":"p2","name":"Site","directories":["site","docs"]}}"""))
            assertEquals(SparsePreset("p2", "Site", listOf("site", "docs")), api.saveWorktreePreset("mac", "w1", "orca", "Site", "site\ndocs"))
            assertEquals("site\ndocs", body(server).second.text("directories"))

            server.enqueue(MockResponse().setBody("""{"tokens":[{"id":"d1","role":"full"},{"id":"d2","role":"limited"}],"current":"d1"}"""))
            assertTrue(api.fullAccess("mac"))
            assertEquals("GET /api/v1/auth/tokens", body(server).first)
            server.enqueue(MockResponse().setBody("""{"tokens":[{"id":"d2","role":"limited"}],"current":"d2"}"""))
            assertFalse(api.fullAccess("mac"))
            server.takeRequest()
            // The hub's own token (no device) is full; a limited device is refused the route.
            server.enqueue(MockResponse().setBody("""{"tokens":[],"current":""}"""))
            assertTrue(api.fullAccess("mac"))
            server.takeRequest()
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"full device access required"}"""))
            assertFalse(api.fullAccess("mac"))
        }
    }
}
