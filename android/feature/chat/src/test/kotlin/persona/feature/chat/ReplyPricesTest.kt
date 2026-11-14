package persona.feature.chat

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.ModelPrice
import persona.core.contracts.Session
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions

/** One process-wide book: a second chat does not ask again, and a later load sends on the client it was given. */
class ReplyPricesTest {
    @Test fun theCatalogIsAskedOnceAndTheNextLoadUsesTheNewClient() = runBlocking {
        ReplyPrices.book.forget()
        val calls = mutableListOf<String>()
        val first = actions { ids ->
            calls += "first:" + ids.joinToString(",")
            ids.associateWith { ModelPrice(input = 4.0, output = 20.0) }
        }
        val second = actions { ids ->
            calls += "second:" + ids.joinToString(",")
            ids.associateWith { ModelPrice(input = 2.0, output = 10.0) }
        }
        ReplyPrices.load(first, listOf("claude-opus-5-5"))
        ReplyPrices.load(first, listOf("claude-opus-5-5"))
        ReplyPrices.load(second, listOf("claude-sonnet-5-5"))
        assertEquals(listOf("first:claude-opus-5-5", "second:claude-sonnet-5-5"), calls)
        assertEquals(4.0, ReplyPrices.book.priceOf("claude-opus-5-5")!!.input!!, 0.0)
        assertEquals(2.0, ReplyPrices.book.priceOf("claude-sonnet-5-5")!!.input!!, 0.0)
        ReplyPrices.book.forget()
    }

    private fun actions(prices: suspend (List<String>) -> Map<String, ModelPrice?>) = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun catalogPrices(ids: List<String>) = prices(ids)
    }
}
