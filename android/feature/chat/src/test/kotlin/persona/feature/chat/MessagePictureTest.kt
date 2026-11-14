package persona.feature.chat

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Session
import persona.core.designsystem.AgentMarkKind
import java.time.Instant

/**
 * What a message's picture is made of (iOS `ShareCardWidthTests`, `MessageShareCard`): the words, cut past 3,000
 * characters; who said it and where; its time; and how wide the card is drawn.
 */
class MessagePictureTest {
    @Test fun plainTextKeepsAPhonesWidth() {
        assertEquals(ShareCardWidth.BASE, ShareCardWidth.width(0f, 600f, CHROME))
        // A table or code that fits in the phone's width changes nothing.
        assertEquals(ShareCardWidth.BASE, ShareCardWidth.width(250f, 600f, CHROME))
    }

    @Test fun tablesAndCodeWidenItToTheirOwnWidthUpToTheCap() {
        assertEquals(400f + CHROME, ShareCardWidth.width(400f, 900f, CHROME))
        assertEquals(ShareCardWidth.CAP, ShareCardWidth.width(2000f, 900f, CHROME))
    }

    @Test fun aThinStripWidensTowardAReadableLine() {
        val base = ShareCardWidth.BASE
        assertEquals(base, ShareCardWidth.width(0f, ShareCardWidth.TALLEST * base, CHROME))
        val tall = ShareCardWidth.width(0f, 1400f, CHROME)
        assertTrue(tall > base && tall < ShareCardWidth.CAP)
        assertEquals(ShareCardWidth.CAP, ShareCardWidth.width(0f, 20000f, CHROME))
    }

    @Test fun theWideBlocksAreTablesAndFencedCode() {
        val text = """
            Intro line | not a table

            | A | B |
            | --- | :-: |
            | 1 | 2 |

            - a list
            ```swift
            let a = 1
            ```
            After.
        """.trimIndent()
        assertEquals(listOf("| A | B |\n| --- | :-: |\n| 1 | 2 |", "```swift\nlet a = 1\n```"), ShareCardBlocks.wide(text))
        assertEquals(emptyList<String>(), ShareCardBlocks.wide("Just words\nand more"))
        assertEquals(listOf("```\nnever closed"), ShareCardBlocks.wide("```\nnever closed"))
        assertTrue(ShareCardBlocks.isDelimiter("---|---") && ShareCardBlocks.isDelimiter("| :--- | ---: |"))
        assertFalse(ShareCardBlocks.isDelimiter("| a | b |") || ShareCardBlocks.isDelimiter("---"))
        // Inline code is not a fence; a longer fence is closed only by one as long.
        assertEquals(emptyList<String>(), ShareCardBlocks.wide("```code``` inline"))
        assertEquals(listOf("````md\n```\ninner\n```\n````"), ShareCardBlocks.wide("````md\n```\ninner\n```\n````\nafter"))
        assertTrue(ShareCardBlocks.fenced("~~~\nx\n~~~"))
        assertFalse(ShareCardBlocks.fenced("| A | B |\n| --- | --- |"))
    }

    /** A card's JSON is drawn as a card, as wide as the picture: it never widens it as code would (#7261). */
    @Test fun cardsAreNotWideBlocks() {
        val card = "```persona-view\n{\"fallback\":\"f\",\"sections\":[]}\n```"
        assertEquals(emptyList<String>(), ShareCardBlocks.wide(card))
        assertEquals(emptyList<String>(), ShareCardBlocks.wide("~~~Persona-Card extra\n{}\n~~~"))
        assertEquals(listOf("```swift\nlet a = 1\n```"), ShareCardBlocks.wide(card + "\n```swift\nlet a = 1\n```"))
        assertTrue(ShareCardBlocks.isCard("```persona-view") && !ShareCardBlocks.isCard("```json"))
    }

    /** The cut never splits a card: one it would cut through goes whole, so its half JSON never shows (#7261). */
    @Test fun aLongAnswerIsNeverCutInsideACard() {
        val words = "w".repeat(SHARE_CARD_MAX_CHARS - 10)
        val card = "```persona-view\n{\"fallback\":\"Build done.\",\"sections\":[{\"items\":[{\"type\":\"progress\",\"label\":\"Build\",\"ratio\":1}]}]}\n```"
        assertEquals("$words\n\n…", shareCardText("$words\n\n$card\n\nAfter."))
        // A card before the cut stays whole.
        val tail = "t".repeat(SHARE_CARD_MAX_CHARS)
        assertEquals("$card\n\n" + tail.take(SHARE_CARD_MAX_CHARS - card.length - 2) + "\n\n…", shareCardText("$card\n\n$tail"))
    }

    @Test fun aVeryLongAnswerIsCut() {
        assertEquals("words", shareCardText("\n  words \n"))
        val long = "a".repeat(SHARE_CARD_MAX_CHARS + 10)
        assertEquals("a".repeat(SHARE_CARD_MAX_CHARS) + "\n\n…", shareCardText(long))
        assertEquals("a".repeat(SHARE_CARD_MAX_CHARS), shareCardText("a".repeat(SHARE_CARD_MAX_CHARS)))
    }

    @Test fun thePictureSaysWhoSaidItWhereAndWhen() {
        val claude = Session(machineId = "mac", sessionId = "s1", spaceName = "persona", slotName = "fix login", agent = "claude")
        val source = shareCardSource(claude, "Persona")
        assertEquals("persona › fix login", source.source)
        assertEquals(AgentMarkKind.Claude, source.mark?.kind)
        val reply = messagePicture(MessageTarget("r1", "Done: the tests pass.", user = false, time = "2026-11-12T06:05:00Z"), source)
        assertEquals("Done: the tests pass.", reply.text)
        assertFalse(reply.user)
        assertEquals(claude.agentLabel, reply.agent)
        assertEquals(Instant.parse("2026-11-12T06:05:00Z"), reply.time)
        // Your own message, and one with no time the hub could give.
        val mine = messagePicture(MessageTarget("u1", "Run the tests", user = true), source)
        assertTrue(mine.user)
        assertNull(mine.time)
        assertNull(messagePicture(MessageTarget("u2", "x", user = true, time = "0001-01-01T00:00:00Z"), source).time)
        // Persona's own conversations: Persona answers, from nowhere in particular.
        val assistant = shareCardSource(claude.copy(assistant = true), "Persona")
        assertEquals(ShareCardSource("Persona", null, ""), assistant)
        val codex = shareCardSource(Session(machineId = "mac", sessionId = "s2", agent = "codex"), "Persona")
        assertEquals(AgentMarkKind.Codex, codex.mark?.kind)
        assertEquals("", codex.source)
    }

    private companion object {
        /** The 24 dp margin and the 24 dp padding on each side. */
        const val CHROME = 96f
    }
}
