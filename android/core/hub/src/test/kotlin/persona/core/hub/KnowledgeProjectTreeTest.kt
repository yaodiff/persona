package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.api.KnowledgeKind
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** The project tree's fields (#12927) decode when the hub sends them, and are null from an older hub. */
class KnowledgeProjectTreeTest {
    @Test fun listAndDetailCarryTheTreeOrNothing() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"items":[
                {"kind":"projects","id":"seeds","name":"Seeds","parent":"garden-app",
                 "milestones":[{"id":"m1","title":"Beta out","target":"2026-12-15","state":"open"},{"later":1}]},
                {"kind":"projects","id":"docs-site","name":"Docs site"}]}"""))
            val items = repository.list(KnowledgeKind.Projects).items
            assertEquals("garden-app", items[0].parent)
            assertEquals("Beta out", items[0].milestones?.first()?.title)
            assertEquals(2, items[0].milestones?.size)
            assertNull(items[1].parent)
            assertNull(items[1].milestones)
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"press-kit","path":""},
                "tree":{"parent":{"id":"store-listing","name":"Store listing"},"ancestors":[{"id":"q4-launch"},{"id":"store-listing"}],
                "children":[],"milestones":[],"depth":3}}"""))
            val tree = repository.entry(KnowledgeKind.Projects, "press-kit").tree
            assertEquals("store-listing", tree?.parent?.id)
            assertEquals(listOf("q4-launch", "store-listing"), tree?.ancestors?.map { it.id })
            assertEquals(3, tree?.depth)
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"docs-site","path":""}}"""))
            assertNull(repository.entry(KnowledgeKind.Projects, "docs-site").tree)
        }
    }
}
