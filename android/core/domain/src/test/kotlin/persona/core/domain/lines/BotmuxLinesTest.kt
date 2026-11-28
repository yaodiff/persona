package persona.core.domain.lines

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Botmux
import persona.core.contracts.Fold as WireFold
import persona.core.contracts.Item

/**
 * What an exchange of a BotMux session sent to Lark (docs/protocol/protocol.md "BotMux messages", web `botmuxReplies`):
 * its successful `botmux send` calls and the replies its folded work lists, as one line after its work and its last
 * words (matching web `SegmentView`); and BotMux's "nothing to send" answer.
 */
class BotmuxLinesTest {
    private fun item(seq: Int, kind: String, role: String = "assistant", text: String? = null, id: String? = null,
        botmux: Botmux? = null, error: Boolean? = null, fold: WireFold? = null) =
        LineItem(Item("$seq#0", seq, role = role, kind = kind, text = text, toolUseId = id, toolName = "Bash".takeIf { kind == "tool_use" },
            isError = error, botmux = botmux, fold = fold))

    private fun reply(text: String, final: Boolean = false) = Botmux("reply", text, final = final)

    private val exchange = listOf(
        item(1, "text", "user", "<user_message>\nship it\n</user_message>", botmux = Botmux("message", "ship it", "Alex Chen", "user")),
        item(2, "tool_use", id = "a", text = "botmux send 'Building'", botmux = reply("Building")),
        item(3, "tool_result", "user", "sent", id = "a"),
        // failed, and still running: neither reached Lark
        item(4, "tool_use", id = "b", text = "botmux send 'Oops'", botmux = reply("Oops")),
        item(5, "tool_result", "user", "error", id = "b", error = true),
        item(6, "tool_use", id = "c", text = "botmux send 'Later'", botmux = reply("Later")),
        // another call is no reply
        item(7, "tool_use", id = "d", text = "ls"),
        item(8, "tool_result", "user", "ok", id = "d"),
        item(9, "fold", fold = WireFold(items = 4, tools = 2, throughSeq = 12, throughId = "12#0",
            botmux = listOf(reply("Tests pass"), Botmux("message", "not a reply"), reply("", final = true)))),
        item(13, "text", text = "BOTMUX_NOTHING_TO_SEND"),
    )

    @Test fun repliesFollowTheLastWords() {
        val lines = chatLines(exchange).lines
        val kinds = lines.map { line ->
            when (line) {
                is ChatLine.Row -> line.row.type
                is ChatLine.Work -> "work"
                is ChatLine.BotmuxReplies -> "botmux"
                else -> "other"
            }
        }
        assertEquals(listOf("user", "work", "reply", "botmux"), kinds)
        val replies = lines.filterIsInstance<ChatLine.BotmuxReplies>().single()
        assertEquals("bm-1#0", replies.id)
        assertEquals(listOf("2#0", "9#0:0", "9#0:2"), replies.replies.map { it.id })
        assertEquals(listOf("Building", "Tests pass", ""), replies.replies.map { it.reply.text })
        assertTrue(replies.replies.last().reply.final)
    }

    /** Open work keeps the line after its steps and the turn's last words. */
    @Test fun openWorkKeepsTheRepliesAfterItsStepsAndLastWords() {
        val lines = chatLines(exchange, LineOptions(openWork = setOf("w-1#0"))).lines
        val at = lines.indexOfFirst { it is ChatLine.BotmuxReplies }
        val answerAt = lines.indexOfFirst { it is ChatLine.Row && it.row.type == "reply" }
        assertTrue(lines.subList(0, at).any { it is ChatLine.End })
        assertTrue(answerAt in 0 until at)
    }

    @Test fun anExchangeWithoutRepliesHasNoLine() {
        val plain = listOf(item(1, "text", "user", "hello"), item(2, "tool_use", id = "a", text = "ls"),
            item(3, "tool_result", "user", "ok", id = "a"), item(4, "text", text = "done"))
        assertTrue(chatLines(plain).lines.none { it is ChatLine.BotmuxReplies })
    }

    @Test fun nothingToSendIsOnlyTheMarker() {
        fun answer(text: String, role: String = "assistant", kind: String = "text") = Item("x", role = role, kind = kind, text = text)
        assertTrue(isBotmuxNothingToSend(answer("BOTMUX_NOTHING_TO_SEND")))
        assertTrue(isBotmuxNothingToSend(answer("  BOTMUX_NOTHING_TO_SEND\n")))
        assertFalse(isBotmuxNothingToSend(answer("The guide explains BOTMUX_NOTHING_TO_SEND here.")))
        assertFalse(isBotmuxNothingToSend(answer("BOTMUX_NOTHING_TO_SEND", role = "user")))
        assertFalse(isBotmuxNothingToSend(answer("BOTMUX_NOTHING_TO_SEND", kind = "thinking")))
    }
}
