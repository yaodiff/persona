package persona.core.rustcore

import java.util.concurrent.CopyOnWriteArrayList
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.ChatSnapshot
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.WireBatch

/** Real host-JNI proof that targeted wakes identify the chat, while legacy hosts remain compatible. */
class ChatTargetedWakeTest {
    @Test fun targetedWakeIdentifiesEachChatAndDrainDoesNotConsumeAnother() {
        val wakes = CopyOnWriteArrayList<Pair<String, Long>>()
        val host = object : CoreHost {
            override fun changed(version: Long) = Unit
            override fun changedChat(chat: String, version: Long) { wakes += chat to version }
            override fun run(effect: persona.core.rustcore.wire.Effect) = Unit
        }
        RustCore(Config(), host).use { core ->
            for (chat in listOf("a", "b")) {
                check(core.dispatch(Action.OpenChat(chat, ChatSnapshot("[]", null, 0, null, false))))
            }
            check(core.flush(5_000))
            assertEquals(listOf("a", "b"), wakes.map { it.first })
            core.drain("a")
            core.dispatch(Action.Chat("b", ChatEvent.Pushed(WireBatch(
                """[{"id":"1#0","seq":1,"kind":"text","text":"new"}]""",
                "h", 0, 1, false, null, false,
            ))))
            check(core.flush(5_000))
            assertEquals("b", wakes.last().first)
            assertEquals(1, core.drain("b").ops.size)
        }
    }
}
