package persona.core.data

import persona.core.contracts.Item
import persona.core.contracts.ItemsResponse
import persona.core.contracts.SessionKey
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test

/** Search reads a folded range without opening it: pages stop, an empty `more` page stops, and the first copy of an id wins. */
@OptIn(ExperimentalCoroutinesApi::class)
class ChatLoadRangeTest {
    @Test fun loadRangeStopsAtThePageCapDropsAnEmptyMorePageAndKeepsTheFirstCopy() = runTest {
        val session = SessionKey("m", "s")
        val transport = FakeChatTransport()
        val repository = HubChatRepository(transport, FakeConnection(), FakeItemsCache(CachedChat()), FakeSessions(), backgroundScope)

        var step = 0
        transport.fetch = {
            step++
            if (step == 1) ItemsResponse(listOf(Item("a", 1)), more = true)
            else ItemsResponse(emptyList(), more = true)
        }
        assertEquals(listOf("a"), repository.loadRange(session, 0, 50).map { it.id })
        assertEquals(2, step)

        var seq = 0
        var pages = 0
        transport.fetch = {
            pages++
            ItemsResponse(listOf(Item("p$seq", seq)), more = true).also { seq++ }
        }
        val capped = repository.loadRange(session, 0, 10_000)
        assertEquals(20, pages)
        assertEquals((0 until 20).map { "p$it" }, capped.map { it.id })

        var page = 0
        transport.fetch = {
            page++
            if (page == 1) ItemsResponse(listOf(Item("a", 1, text = "first")), more = true)
            else ItemsResponse(listOf(Item("a", 2, text = "second"), Item("b", 3)), more = false)
        }
        val rows = repository.loadRange(session, 1, 10)
        assertEquals(listOf("a", "b"), rows.map { it.id })
        assertEquals("first", rows[0].text)
    }
}
