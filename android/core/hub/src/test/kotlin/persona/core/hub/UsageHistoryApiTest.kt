package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.api.UsageHistoryApi
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

class UsageHistoryApiTest {
    @Test fun daysUseTheSelectedManagerAndDecodeEveryTokenKind() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient()).forManager("work") as UsageHistoryApi
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"Asia/Shanghai","stored":true,"days":[
                {"date":"2026-11-16","in":1,"cache_write":2,"cache_read":4,"out":8,"req":3},
                {"date":"2026-11-17","in":10,"cache_write":20,"cache_read":40,"out":80,"req":5}
            ]}}"""))

            val answer = api.usageDays(140)
            assertEquals("Asia/Shanghai", answer.zone)
            assertTrue(answer.stored)
            assertEquals(listOf("2026-11-16", "2026-11-17"), answer.days.map { it.date })
            assertEquals(15L, answer.days[0].total)
            assertEquals(150L, answer.days[1].total)
            assertEquals(3, answer.days[0].requests)

            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/usage/days", request.requestUrl?.encodedPath)
            assertEquals("140", request.requestUrl?.queryParameter("days"))
            assertEquals("work", request.requestUrl?.queryParameter(ManagerQuery))
            assertEquals("work", request.getHeader(ManagerHeader))
        }
    }

    @Test fun hoursDecodeClockChangesMachineSharesAndTheTotal() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"America/New_York","stored":false,
                "hours":[
                    {"t":1760000000,"date":"2026-11-01","hour":1,"in":1,"cache_write":2,"cache_read":3,"out":4,"req":1},
                    {"t":1760003600,"date":"2026-11-01","hour":1,"in":5,"cache_write":6,"cache_read":7,"out":8,"req":2}
                ],
                "machines":[{"id":"old","name":"","in":6,"cache_write":8,"cache_read":10,"out":12,"req":3}],
                "total":{"in":6,"cache_write":8,"cache_read":10,"out":12,"req":3}
            }}"""))

            val answer = api.usageHours(7)
            assertFalse(answer.stored)
            assertEquals(listOf(1, 1), answer.hours.map { it.hour })
            assertEquals(listOf(1_760_000_000L, 1_760_003_600L), answer.hours.map { it.t })
            assertEquals(36L, answer.total.total)
            assertEquals(36L, answer.machines.single().total)
            assertEquals("", answer.machines.single().name)

            val request = server.takeRequest()
            assertEquals("/api/v1/usage/hours", request.requestUrl?.encodedPath)
            assertEquals("7", request.requestUrl?.queryParameter("days"))
        }
    }

    @Test fun hoursKeepAgentIdsAndAllFourTokenKindsInWireOrder() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"usage":{"agents":[
                {"agent":"claude","in":1,"cache_write":2,"cache_read":4,"out":8,"req":16},
                {"agent":"","cache_read":11},{"agent":"Acme-CLI","out":3},{"agent":"request-only","req":8}
            ]}}"""))

            val shares = api.usageHours().agents
            assertEquals(listOf("claude", "", "Acme-CLI", "request-only"), shares.map { it.agent })
            assertEquals(listOf(15L, 11L, 3L, 0L), shares.map { it.total })
            assertEquals(16, shares.first().requests)
            assertEquals("/api/v1/usage/hours?days=7", server.takeRequest().path)
        }
    }

    @Test fun oldAndEmptyHoursAnswersKeepThePageWithoutAgentShares() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            for (payload in listOf("""{"usage":{"total":{"in":7}}}""", """{"usage":{"total":{"in":7},"agents":[]}}""")) {
                server.enqueue(MockResponse().setBody(payload))
                val hours = api.usageHours()
                assertEquals(7L, hours.total.total)
                assertTrue(hours.agents.isEmpty())
            }
        }
    }

    @Test fun invalidRangesFailBeforeAnyNetworkRead() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            for (call in listOf<suspend () -> Unit>({ api.usageDays(0) }, { api.usageDays(372) }, { api.usageHours(8) })) {
                try {
                    call()
                    fail("invalid range must be rejected")
                } catch (_: IllegalArgumentException) {
                }
            }
            assertEquals(0, server.requestCount)
        }
    }

    @Test fun projectsDecodeRangesProjectsAndNotSplitForTheSelectedManager() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient()).forManager("work") as UsageHistoryApi
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"Asia/Shanghai","stored":true,"ranges":[
                {"days":1,"total":{"in":10,"cache_write":20,"cache_read":40,"out":80,"req":3},
                 "projects":[{"key":"repo:1","kind":"repo","group":"development","name":"acme/app","host":"git.example",
                              "machines":2,"share":0.6,"tokens":{"in":1,"cache_write":2,"cache_read":4,"out":8,"req":2}},
                             {"key":"odd","kind":"mystery","group":"mystery","name":"","machines":1,"share":0.1,"tokens":{"in":5}}],
                 "not_split":{"in":7,"cache_write":8}},
                {"days":3,"total":{},"projects":[],"not_split":{}},
                {"days":7,"total":{},"projects":[],"not_split":{}}
            ]}}"""))

            val answer = requireNotNull(api.usageProjects())
            assertEquals("Asia/Shanghai", answer.zone)
            assertTrue(answer.stored)
            assertEquals(listOf(1, 3, 7), answer.ranges.map { it.days })
            val project = answer.ranges.first().projects.first()
            assertEquals("acme/app", project.name)
            assertEquals("git.example", project.host)
            assertEquals(2, project.machines)
            assertEquals(0.6, project.share, 0.0)
            assertEquals(15L, project.tokens.total)
            assertEquals("mystery", answer.ranges.first().projects.last().kind)
            assertEquals(15L, answer.ranges.first().notSplit.total)

            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/usage/projects", request.requestUrl?.encodedPath)
            assertEquals("work", request.requestUrl?.queryParameter(ManagerQuery))
            assertEquals("work", request.getHeader(ManagerHeader))
        }
    }

    @Test fun anOlderHubWithoutProjectsHidesTheSectionWhileOtherErrorsFail() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
            assertNull(api.usageProjects())

            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"broken"}"""))
            try {
                api.usageProjects()
                fail("a non-404 project answer must fail")
            } catch (failure: HubFailure) {
                assertEquals(500, failure.status)
            }
        }
    }

    @Test fun projectDetailDecodesEveryBreakdownAndEncodesTheOpaqueKey() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"usage":{"zone":"Asia/Shanghai","stored":true,
                "project":{"key":"team/repo & app","kind":"repo","group":"development","name":"acme/app","host":"git.example",
                           "machines":2,"share":0.4,"tokens":{"in":5,"cache_write":6,"cache_read":7,"out":8}},
                "ranges":[{"days":1,"total":{"in":1,"cache_write":2,"cache_read":3,"out":4,"req":9},"share":0.25,"cache_hit":0.875,
                           "machines":[{"key":"mac","name":"MacBook","tokens":{"in":1}}],
                           "agents":[{"key":"codex","tokens":{"in":2}}],
                           "models":[{"key":"gpt-6.2-sol","tokens":{"in":3}}],
                           "launchers":[{"key":"","tokens":{"in":4}}],
                           "places":[{"machine":"mac","machine_name":"MacBook","place":"src/app","worktree":true,"tokens":{"in":4}}]},
                          {"days":3,"total":{},"share":0,"cache_hit":0},
                          {"days":7,"total":{},"share":0,"cache_hit":0}],
                "days":[{"date":"2026-11-16","tokens":{"in":2}},{"date":"2026-11-17","tokens":{"in":3}}],
                "hours":[{"t":1760000000,"hour":9,"tokens":{"in":4}}]}}"""))

            val answer = api.usageProjectDetail("team/repo & app")
            assertEquals("team/repo & app", answer.project.key)
            assertEquals(26L, answer.project.tokens.total)
            assertEquals(10L, answer.ranges.first().total.total)
            assertEquals(0.875, answer.ranges.first().cacheHit, 0.0)
            assertEquals("MacBook", answer.ranges.first().machines.single().name)
            assertEquals("codex", answer.ranges.first().agents.single().key)
            assertEquals("gpt-6.2-sol", answer.ranges.first().models.single().key)
            assertEquals("", answer.ranges.first().launchers.single().key)
            assertTrue(answer.ranges.first().places.single().worktree)
            assertEquals(listOf("2026-11-16", "2026-11-17"), answer.days.map { it.date })
            assertEquals(9, answer.hours.single().hour)

            val request = server.takeRequest()
            assertEquals("/api/v1/usage/projects/detail", request.requestUrl?.encodedPath)
            assertEquals("team/repo & app", request.requestUrl?.queryParameter("key"))
        }
    }

    @Test fun anUnknownProjectKeepsTheDetails404AsAnError() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"no such project"}"""))

            try {
                api.usageProjectDetail("gone")
                fail("a project no machine resolves to must fail")
            } catch (failure: HubFailure) {
                assertEquals(404, failure.status)
            }
        }
    }
}
