package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Item
import persona.core.contracts.Todo
import persona.core.contracts.TodoAsk
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.WorkItem

class TaskSummaryTextTest {
    @Test fun parseTaskVerdictStripsLeadingSystemEmojiAndClassifiesTone() {
        assertEquals(ParsedVerdict(VerdictTone.Done, "做完了：已合未部署，按下面验收"), parseTaskVerdict("✅ 做完了：已合未部署，按下面验收"))
        assertEquals(ParsedVerdict(VerdictTone.Done, "Done: deployed"), parseTaskVerdict("\u2705\uFE0F Done: deployed"))
        assertEquals(ParsedVerdict(VerdictTone.Cannot, "Cannot be done: blocked"), parseTaskVerdict("⛔ Cannot be done: blocked"))
        assertEquals(ParsedVerdict(VerdictTone.Decide, "Needs your decision: pick one"), parseTaskVerdict("🤔 Needs your decision: pick one"))
        assertEquals(ParsedVerdict(VerdictTone.Neutral, "Please verify the release."), parseTaskVerdict("Please verify the release."))
    }

    @Test fun trayVerdictTextAdaptsBelowHintsAcrossLocales() {
        val zh = trayVerdictText("✅ 做完了：已合未部署，按下面验收", "待验收")
        assertEquals(VerdictTone.Done, zh.tone)
        assertEquals("做完了：已合未部署，待验收", zh.text)

        val zhDeployed = trayVerdictText("✅ 做完了：已部署，按下面验收", "待验收")
        assertEquals("做完了：已部署，待验收", zhDeployed.text)

        val enMerged = trayVerdictText("✅ Done: merged, not deployed yet; check it below", "ready to verify")
        assertEquals(VerdictTone.Done, enMerged.tone)
        assertEquals("Done: merged, not deployed yet, ready to verify", enMerged.text)

        val enVerify = trayVerdictText("✅ Done: please verify the result below", "ready to verify")
        assertEquals("Done: ready to verify", enVerify.text)

        val jaMerged = trayVerdictText("✅ 完了：マージ済み・未デプロイ。下の手順で確認してください", "確認待ち")
        assertEquals(VerdictTone.Done, jaMerged.tone)
        assertEquals("完了：マージ済み・未デプロイ・確認待ち", jaMerged.text)

        val plain = trayVerdictText("Please acknowledge the completed conclusion.", "ready to verify")
        assertEquals(VerdictTone.Neutral, plain.tone)
        assertEquals("Please acknowledge the completed conclusion.", plain.text)
    }

    @Test fun agentMetadataLineProducesCompactTagWithoutRawPaths() {
        assertEquals(
            "Workstation · sample-net · Antigravity",
            agentMetadataLine("Workstation", "C:\\Users\\demo\\Workspace\\sample-net", "Antigravity"),
        )
        assertEquals(
            "Laptop · shop · Claude",
            agentMetadataLine("Laptop", "/Users/demo/code/shop/", "Claude"),
        )
        assertEquals(
            "Workstation · Codex",
            agentMetadataLine("Workstation", "C:\\", "Codex"),
        )
    }

    @Test fun sanitizeSummaryMarkdownStripsFileLinksAbsolutePathsAndClippedUrls() {
        val raw = "1. **P1 修复确认**：[SampleVpnService.kt](file:///C:/Users/demo/Workspace/sample-net/app/src/main/java/dev/example/app/service/SampleVpnService.kt..."
        val parsed = ReceiptMarkdown.parse(sanitizeSummaryMarkdown(raw))
        assertEquals("1. P1 修复确认：SampleVpnService.kt", parsed.text)
        assertFalse(parsed.text.contains("file:///"))
        assertFalse(parsed.text.contains("C:/Users"))
        assertFalse(parsed.text.contains("**"))

        val complete = "Checked [Main.kt](file:///Users/demo/src/Main.kt#L12) and C:\\Users\\demo\\work\\Build.gradle plus /home/demo/app/Net.kt and [PR #7](https://github.com/o/r/pull/7)"
        val cleanComplete = ReceiptMarkdown.parse(sanitizeSummaryMarkdown(complete))
        assertEquals("Checked Main.kt and Build.gradle plus Net.kt and PR #7", cleanComplete.text)
        assertEquals(listOf("https://github.com/o/r/pull/7"), cleanComplete.spans.filter { it.mark == ReceiptMarkdown.Mark.Link }.map { it.url })
        assertEquals("1. P1 修复确认：SampleVpnService.kt", plainSummaryText(raw))
        assertEquals("", plainSummaryText(null))
    }

    @Test fun insertIdentifierBreakHintsAddsSoftBreaksOnlyInLongNonUrlTokens() {
        val (hinted, map) = insertIdentifierBreakHints("Call ParcelFileDescriptor.fromFd(fd).use and https://example.com/a.b.c.d.e.f")
        assertTrue(hinted.contains("ParcelFileDescriptor.\u200BfromFd(\u200Bfd).\u200Buse"))
        assertTrue(hinted.contains("https://example.com/a.b.c.d.e.f"))
        assertFalse(hinted.substringAfter("https://").contains('\u200B'))
        assertEquals(hinted.length, map.last())
    }

    @Test fun shouldShowTaskReceiptInDetailSuppressesReceiptsWhenOwnerAskIsActive() {
        val askTodo = Todo("t1", "Task", ask = TodoAsk(outcome = "done", verdict = "Done", askedAt = "2026-11-26T00:00:00Z"))
        val plainTodo = Todo("t1", "Task")

        assertFalse(shouldShowTaskReceiptInDetail(askTodo))
        assertTrue(shouldShowTaskReceiptInDetail(plainTodo))
        assertTrue(askTodo.hasOwnerPrimaryAction())
        assertFalse(plainTodo.hasOwnerPrimaryAction())
    }

    @Test fun hasVisibleCompactStepsDistinguishesThinkingOnlyFromInspectableSteps() {
        val thinkingOnly = ChatLine.Work(
            id = "w1",
            steps = listOf(WorkItem.Thought(LineItem(Item("t1", 1, role = "assistant", kind = "thinking", text = "hmm")))),
            live = true,
            start = null,
            end = null,
            open = false,
        )
        assertFalse(thinkingOnly.hasVisibleCompactSteps(hidesThoughts = true))
        assertTrue(thinkingOnly.hasVisibleCompactSteps(hidesThoughts = false))

        val emptyLive = ChatLine.Work("w2", emptyList(), live = true, start = null, end = null, open = false)
        assertFalse(emptyLive.hasVisibleCompactSteps(hidesThoughts = true))

        val withNarration = ChatLine.Work(
            id = "w3",
            steps = listOf(WorkItem.Narration(LineItem(Item("n1", 2, role = "assistant", kind = "text", text = "Checking...")))),
            live = false,
            start = null,
            end = null,
            open = false,
        )
        assertTrue(withNarration.hasVisibleCompactSteps(hidesThoughts = true))
    }
}
