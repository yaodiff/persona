package persona.core.hub

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class HttpChatActionsTest {
    @Test fun rejectedMenuOperationRetainsStructuredFailure() = runBlocking {
        val failure = HubFailure(HubError.Server, "提示文字", status = 409, code = "stale_session", delivered = "none", retryable = false)
        val actions = HttpChatActions { _, _, _ -> throw failure }
        val result = actions.execute(Session("machine", "session"), ChatAction.Close) as ChatActionResult.Failed
        assertSame(failure, result.cause)
    }

    @Test fun menuRequestsPreserveMuteSettingsEncodePathsAndVerifyUncertainClose() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            val actions = HttpChatActions { path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute().also {
                    if (!it.isSuccessful) throw it.use { response -> HubFailure.from(response) }
                }
            }
            val session = Session(machineId = "machine", sessionId = "session/with#separator", spaceName = "space")
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(session, ChatAction.Rename("新标题")))
            val rename = server.takeRequest()
            assertEquals("/api/v1/sessions/machine/session%2Fwith%23separator/rename", (rename.path).zhAny())
            assertEquals("新标题", (JsonCodec.parseToJsonElement(rename.body.readUtf8()).jsonObject["title"]!!.jsonPrimitive.content).zhAny())
            server.enqueue(MockResponse().setBody("""{"spaces":["machine/space"],"sessions":["machine/other"],"allowed":[],"future":true}"""))
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(session, ChatAction.Mute(false)))
            assertEquals("GET", (server.takeRequest().method).zhAny())
            val mute = server.takeRequest()
            assertEquals("PUT", (mute.method).zhAny())
            val payload = JsonCodec.parseToJsonElement(mute.body.readUtf8()).jsonObject
            assertEquals("machine/other", (payload["sessions"]!!.jsonArray.single().jsonPrimitive.content).zhAny())
            assertEquals("machine/space", (payload["spaces"]!!.jsonArray.single().jsonPrimitive.content).zhAny())
            assertEquals(session.key.path, payload["allowed"]!!.jsonArray.single().jsonPrimitive.content)
            assertEquals(JsonPrimitive(true), payload["future"])
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"delivered":"unknown"}"""))
            assertEquals(ChatActionResult.Unknown, actions.execute(session, ChatAction.Close))
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"sessions":[]}"""))
            assertEquals(ChatActionResult.Done, actions.execute(session, ChatAction.Close))
            assertEquals("GET", (server.takeRequest().method).zhAny())
            assertEquals(5, server.requestCount)
        }
    }

    private fun actions(server: MockWebServer): HttpChatActions {
        val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
        return HttpChatActions { path, method, body ->
            http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
        }
    }

    @Test fun ultracodeGoesAloneAndAQueuedSwitchVerifies() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(Session("m", "s1"), ChatAction.Modes(ultracode = true)))
            val request = server.takeRequest()
            assertEquals("/api/v1/sessions/m/s1/modes", request.path)
            assertEquals(buildJsonObject { put("ultracode", true) }, JsonCodec.parseToJsonElement(request.body.readUtf8()))
            // The hub's row: queued, the node still saw it off; an older hub's row has none of the fields (unknown, not off).
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"m","session_id":"s1","ultracode":false,"ultracode_pending":true,"ultracode_note":"等这一轮结束"}]}"""))
            assertEquals(ChatActionResult.Done, actions.verify(Session("m", "s1"), ChatAction.Modes(ultracode = true)))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"m","session_id":"s1"}]}"""))
            assertEquals(ChatActionResult.Unknown, actions.verify(Session("m", "s1"), ChatAction.Modes(ultracode = true)))
            val old = JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s1"}""")
            assertEquals(Triple<Boolean?, String?, Boolean>(null, null, false), Triple(old.ultracode, old.ultracodeNote, old.ultracodePending))
            val seen = JsonCodec.decodeFromString<Session>("""{"session_id":"s1","ultracode":false,"ultracode_note":"x","ultracode_pending":true}""")
            assertEquals(Triple<Boolean?, String?, Boolean>(false, "x", true), Triple(seen.ultracode, seen.ultracodeNote, seen.ultracodePending))
        }
    }

    /**
     * #5055: a pick on another launcher records a switch (`launcher` with `from`, the model and effort for it), verified by
     * the row's `pending_switch`; the session's own launcher drops one; a model switches alone on its own launcher.
     */
    @Test fun aLauncherSwitchIsRecordedWithWhereTheAppSawItAndVerified() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val agent = Session("m", "s1", launcher = "example")
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(agent, ChatAction.Modes(launcher = "work", from = "example", model = "glm-5.3", effort = "high")))
            assertEquals(buildJsonObject { put("launcher", "work"); put("from", "example"); put("model", "glm-5.3"); put("effort", "high") },
                JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(agent, ChatAction.Modes(model = "sonnet")))
            assertEquals(buildJsonObject { put("model", "sonnet") }, JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
        }
        val waiting = JsonCodec.decodeFromString<Session>("""{"machine_id":"m","session_id":"s1","launcher":"example","effort":"low",
            "pending_switch":{"launcher":"work","model":"glm-5.3","effort":"high","at":"2026-11-12T10:00:00Z"}}""")
        assertTrue("the switch waits: its model and effort are the switch's", modesApplied(waiting, ChatAction.Modes(launcher = "work", from = "example", model = "glm-5.3", effort = "high")))
        assertFalse(modesApplied(waiting, ChatAction.Modes(launcher = "other", from = "example")))
        assertTrue("carried out by now", modesApplied(waiting.copy(launcher = "work", pendingSwitch = null), ChatAction.Modes(launcher = "work", from = "example")))
        assertFalse("its own launcher drops it", modesApplied(waiting, ChatAction.Modes(launcher = "example", from = "example")))
        assertTrue(modesApplied(waiting.copy(pendingSwitch = null), ChatAction.Modes(launcher = "example", from = "example")))
        assertTrue(modesApplied(waiting.copy(model = "SONNET"), ChatAction.Modes(model = "sonnet")))
    }

    /** The complete row a summary leaves out (`sessions_lookup`): one exact lookup, its machine kept. */
    @Test fun theCompleteRowIsOneExactLookup() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("""{"sessions":[{"session_id":"s/1","launcher":"example","pending_switch":{"launcher":"work"}}],"total":1}"""))
            val row = actions.completeRow(Session("m", "s/1"))
            val request = server.takeRequest()
            assertEquals("/api/v1/sessions?summary=0&limit=1&machine_id=m&session_id=s%2F1", request.path)
            assertEquals(SessionKey("m", "s/1"), row?.key)
            assertEquals("work", row?.pendingSwitch?.launcher)
            server.enqueue(MockResponse().setBody("""{"sessions":[]}"""))
            assertNull(actions.completeRow(Session("m", "gone")))
        }
    }

    /** #582: the after-resume notice is dismissed with `ultracode_dismiss_lost` and verified by the notice being gone. */
    @Test fun theAfterResumeNoticeIsDismissed() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(Session("m", "s1"), ChatAction.Modes(dismissUltracodeLost = true)))
            assertEquals(buildJsonObject { put("ultracode_dismiss_lost", true) }, JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"m","session_id":"s1","ultracode_lost_at":"2026-11-11T12:00:00Z"}]}"""))
            assertEquals(ChatActionResult.Unknown, actions.verify(Session("m", "s1"), ChatAction.Modes(dismissUltracodeLost = true)))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"m","session_id":"s1"}]}"""))
            assertEquals(ChatActionResult.Done, actions.verify(Session("m", "s1"), ChatAction.Modes(dismissUltracodeLost = true)))
            assertEquals("2026-11-11T12:00:00Z", JsonCodec.decodeFromString<Session>("""{"session_id":"s","ultracode_lost_at":"2026-11-11T12:00:00Z"}""").ultracodeLostAt)
        }
    }

    @Test fun forkPostsTheReplyAndReturnsTheNewSession() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("""{"session_id":"new-1"}"""))
            assertEquals("new-1", actions.fork(Session("m", "s1"), "abc#3"))
            val request = server.takeRequest()
            assertEquals("/api/v1/sessions/m/s1/fork", request.path)
            assertEquals("abc#3", JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject["at_item_id"]!!.jsonPrimitive.content)
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"busy"}"""))
            val refused = try { actions.fork(Session("m", "s1"), "abc#3"); null } catch (e: Exception) { e }
            assertTrue(refused is HubFailure)
        }
    }

    @Test fun rewindSendsWhichMessageAndNamesWhyItDidNot() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val target = RewindTarget("run the tests", occurrence = 2, promptsAfter = 1)
            server.enqueue(MockResponse().setBody("""{"prompt":"run the tests","restore":"conversation","code_restored":false,"input_cleared":true}"""))
            val done = actions.rewind(Session("m", "s1"), target, RewindRestore.Conversation)
            assertTrue(done.inputCleared)
            val body = JsonCodec.parseToJsonElement(server.takeRequest().body.readUtf8()).jsonObject
            assertEquals(listOf("run the tests", "2", "1", "conversation"),
                listOf("prompt", "occurrence", "prompts_after", "restore").map { body[it]!!.jsonPrimitive.content })
            suspend fun failure(code: Int, json: String): RewindFailure? {
                server.enqueue(MockResponse().setResponseCode(code).setBody(json))
                val error = try { actions.rewind(Session("m", "s1"), target, RewindRestore.CodeAndConversation); null }
                    catch (e: Exception) { e }
                server.takeRequest()
                return error as? RewindFailure
            }
            assertEquals(RewindFailure.Reason.HubUnsupported, failure(404, "{}")?.reason)
            assertEquals(RewindFailure.Reason.NeedsFullDevice, failure(403, """{"error":"full device"}""")?.reason)
            val refused = failure(409, """{"error":"the message is not in /rewind","code":"rewind_mismatch"}""")
            assertEquals(RewindFailure.Reason.Refused, refused?.reason)
            assertEquals("the message is not in /rewind", refused?.hubMessage)
        }
    }

    @Test fun deleteSendsTheSessionsDeleteAndVerifiesItLeftTheList() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val session = Session(machineId = "m", sessionId = "s/1", status = "offline")
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            assertEquals(ChatActionResult.Done, actions.execute(session, ChatAction.Delete))
            val request = server.takeRequest()
            assertEquals("DELETE", (request.method).zhAny())
            assertEquals("/api/v1/sessions/m/s%2F1", (request.path).zhAny())
            assertEquals(0L, request.bodySize)
            // The hub refuses one that runs again: reported, not retried or left uncertain.
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"这个 Agent 还在运行，先关闭再删除"}"""))
            assertTrue(actions.execute(session, ChatAction.Delete) is ChatActionResult.Failed)
            server.takeRequest()
            // A lost answer is verified against the session list: gone from it is done.
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"delivered":"unknown"}"""))
            assertEquals(ChatActionResult.Unknown, actions.execute(session, ChatAction.Delete))
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"sessions":[]}"""))
            assertEquals(ChatActionResult.Done, actions.verify(session, ChatAction.Delete))
            assertEquals("GET", (server.takeRequest().method).zhAny())
            assertEquals(4, server.requestCount)
        }
    }

    @Test fun attachPostsToTheSessionAndVerifiesTheAttachedFlag() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val session = Session(machineId = "m", sessionId = "opaque", sourceKind = "codex_app_server",
                capabilities = SessionCapabilities(read = true, send = true, attach = true))
            server.enqueue(MockResponse().setBody("{}"))
            assertEquals(ChatActionResult.Done, actions.execute(session, ChatAction.Attach))
            val request = server.takeRequest()
            assertEquals("POST", (request.method).zhAny())
            assertEquals("/api/v1/sessions/m/opaque/attach", (request.path).zhAny())
            // A lost answer is verified against the session list, not sent again.
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"delivered":"unknown"}"""))
            assertEquals(ChatActionResult.Unknown, actions.execute(session, ChatAction.Attach))
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"sessions":[{"machine_id":"m","session_id":"opaque","attached":true}]}"""))
            assertEquals(ChatActionResult.Done, actions.verify(session, ChatAction.Attach))
            assertEquals("GET", (server.takeRequest().method).zhAny())
            assertEquals(3, server.requestCount)
        }
    }

    @Test fun queueEditsSendEveryIndexKeptAndReturnWhatWasQueued() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val session = Session(machineId = "m", sessionId = "s/1")
            server.enqueue(MockResponse().setBody("""{"queue":["先跑测试","再提交",1]}"""))
            assertEquals(listOf("先跑测试", "再提交"), (actions.editQueue(session, listOf(1)).queue).zhAny())
            val edit = server.takeRequest()
            assertEquals("POST", (edit.method).zhAny())
            assertEquals("/api/v1/sessions/m/s%2F1/queue", (edit.path).zhAny())
            assertEquals("""{"keep":[1]}""", (edit.body.readUtf8()).zhAny())
            // Deleting the only queued message keeps nothing: the empty list goes out.
            server.enqueue(MockResponse().setBody("""{"queue":["只有一条"]}"""))
            actions.editQueue(session, emptyList())
            assertEquals("""{"keep":[]}""", (server.takeRequest().body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"队列已经被取走","code":"queue_changed","delivered":"none"}"""))
            val failure = runCatching { actions.editQueue(session, emptyList()) }.exceptionOrNull() as HubFailure
            assertEquals("queue_changed", (failure.code).zhAny())
            assertEquals(3, server.requestCount)
        }
    }

    @Test fun aPersonaQueueEditReturnsEachMessagesPhotosBesideItsWords() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            val session = Session(machineId = "m", sessionId = "a1", assistant = true)
            // The hub's answer: images[i] are message i's photos, null for a message without (spec/fixtures/wire/queue.json).
            server.enqueue(MockResponse().setBody("""{"queue":["看看这张","再查一遍"],"images":[[{"mime":"image/png","data":"AAEC"}],null]}"""))
            val edit = actions.editQueue(session, listOf(1))
            assertEquals(listOf("看看这张", "再查一遍"), edit.queue.zhAny())
            assertEquals(listOf(ImagePayload("image/png", "AAEC")), edit.imagesOf(0))
            assertEquals(emptyList<ImagePayload>(), edit.imagesOf(1))
            assertEquals(emptyList<ImagePayload>(), edit.imagesOf(2))
            // Words only (no photos at all, an Agent's queue, an older hub): the key is not there.
            server.enqueue(MockResponse().setBody("""{"queue":["先跑测试"]}"""))
            val plain = actions.editQueue(session, emptyList())
            assertEquals(listOf("先跑测试"), plain.queue.zhAny())
            assertEquals(emptyList<List<ImagePayload>?>(), plain.images)
            // A photo list that is not one drops alone: the words still come back.
            server.enqueue(MockResponse().setBody("""{"queue":["先跑测试"],"images":"none"}"""))
            assertEquals(listOf("先跑测试"), actions.editQueue(session, emptyList()).queue.zhAny())
        }
    }

    @Test fun sendNowPostsTheQueueRouteOnTheSlowClientAndKeepsTheHubsWords() = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            val slow = mutableListOf<String>()
            val actions = HttpChatActions({ path, method, body ->
                slow += path
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }) { _, _, _ -> error("send-now waits for the node: the slow client") }
            val session = Session(machineId = "m", sessionId = "s/1")
            server.enqueue(MockResponse().setBody("""{"sent":true}"""))
            actions.sendQueuedNow(session)
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/sessions/m/s%2F1/queue/send-now", request.path)
            assertEquals(0L, request.bodySize)
            assertEquals(listOf("/api/v1/sessions/m/s%2F1/queue/send-now"), slow)
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"Nothing is queued any more","code":"nothing_queued","delivered":"none"}"""))
            val failure = runCatching { actions.sendQueuedNow(session) }.exceptionOrNull() as HubFailure
            assertEquals("nothing_queued", failure.code)
            assertEquals(409, failure.status)
            assertEquals("Nothing is queued any more", failure.message)
            assertEquals(2, server.requestCount)
        }
    }

    @Test fun infoReadsTheSessionRoute() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("""{"session_id":"s","model":"claude-opus","context_tokens":48200,"turns":6}"""))
            val info = actions.info(Session(machineId = "m", sessionId = "s"))
            assertEquals("claude-opus", (info.model).zhAny())
            assertEquals(48200L, info.contextTokens)
            assertEquals(6, info.turns)
            val request = server.takeRequest()
            assertEquals("GET", (request.method).zhAny())
            assertEquals("/api/v1/sessions/m/s/info", (request.path).zhAny())
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"session not found"}"""))
            assertEquals(404, (runCatching { actions.info(Session(machineId = "m", sessionId = "gone")) }.exceptionOrNull() as HubFailure).status)
        }
    }

    /** Session info's questions the assistant answered (iOS `DelegatedHistoryModel`): the session's cards, a row it can't read left out. */
    @Test fun interactionHistoryReadsTheSessionsCards() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("""{"interactions":[
                {"interaction":{"id":"q1","machine_id":"m","session_id":"s","kind":"question",
                  "questions":[{"question":"Which branch?","options":[{"label":"main"},{"label":"dev"}]}]},
                 "status":"resolved","resolution":{"answers":{"Which branch?":"main"},"answered_by":"assistant","reason":"main is what CI builds"},
                 "resolved_at":"2026-11-12T01:02:03Z","future":1},
                {"status":"resolved"},
                {"interaction":{"id":"p1","kind":"permission"},"status":"resolved","resolution":{"allow":true}}
            ]}"""))
            val rows = actions.interactionHistory(Session(machineId = "m", sessionId = "s", manager = "work"))
            assertEquals(listOf("q1", "p1"), rows.map { it.interaction.id })
            assertEquals("assistant", rows[0].resolution?.answeredBy)
            assertEquals("main is what CI builds", rows[0].resolution?.reason)
            assertEquals("2026-11-12T01:02:03Z", rows[0].resolvedAt)
            val request = server.takeRequest()
            assertEquals("/api/v1/interactions/history?machine_id=m&session_id=s&limit=500&manager=work", request.path)
            // A hub that keeps the history from this device, or has none, gives none rather than an error.
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"error":"forbidden"}"""))
            assertEquals(emptyList<InteractionRecord>(), actions.interactionHistory(Session(machineId = "m", sessionId = "s")))
            server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
            assertEquals(emptyList<InteractionRecord>(), actions.interactionHistory(Session(machineId = "m", sessionId = "s")))
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"assistant offline","code":"assistant_offline"}"""))
            assertEquals(503, (runCatching { actions.interactionHistory(Session(machineId = "m", sessionId = "s")) }.exceptionOrNull() as HubFailure).status)
        }
    }

    @Test fun catalogPricesPostsIdsAndKeepsAModelThatHasNone() = runBlocking {
        MockWebServer().use { server ->
            val actions = actions(server)
            server.enqueue(MockResponse().setBody("""{"models":[{"id":"claude-opus-5-5","name":"Opus","price":{"input":4,"output":20,"cache_read":0.2}},{"id":"claude-haiku-5-5","name":"Haiku"}]}"""))
            val prices = actions.catalogPrices(listOf("claude-opus-5-5", "claude-haiku-5-5"))
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/launchers/catalog", request.path)
            assertEquals(listOf("claude-opus-5-5", "claude-haiku-5-5"),
                JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject["ids"]!!.jsonArray.map { it.jsonPrimitive.content })
            assertEquals(4.0, prices["claude-opus-5-5"]!!.input)
            assertEquals(0.2, prices["claude-opus-5-5"]!!.cacheRead)
            assertTrue(prices.containsKey("claude-haiku-5-5"))
            assertNull(prices["claude-haiku-5-5"])
            assertFalse(prices.containsKey("left-out"))
        }
    }
}
