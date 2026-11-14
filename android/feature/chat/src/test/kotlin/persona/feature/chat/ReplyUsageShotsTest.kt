package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ModelTokens
import persona.core.domain.lines.Subagents
import persona.core.domain.lines.TurnCost
import persona.core.domain.lines.UsageSummary
import persona.core.domain.lines.ModelShare
import persona.core.domain.lines.formatCost
import persona.core.domain.lines.usageCard
import persona.core.domain.lines.usageTotals
import java.io.File

/**
 * A finished reply's cost or tokens under its time (#5470), in Glass, Brief and Board, light and dark.
 * [theUsageSheetOpens] taps the label and captures the sheet itself. PNGs in `build/reply-usage-shots/`
 * (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1600dp-xhdpi")
class ReplyUsageShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/reply-usage-shots").also { it.mkdirs() }

    @Test fun costOrTokensUnderTheReply() {
        val opus = ModelTokens("claude-opus-5-5", input = 2, output = 3400, cacheRead = 1_200_000, cacheWrite = 9000)
        val priced = UsageSummary(listOf(opus), first = true)
        val pricedCost = TurnCost(0.12, listOf(ModelShare("claude-opus-5-5", 0.12)))
        val unpriced = UsageSummary(listOf(
            ModelTokens("claude-opus-5-5", input = 100, output = 23_300),
            ModelTokens("claude-haiku-5-5", output = 10),
        ))
        val personaCost = TurnCost(0.09, listOf(ModelShare("claude-opus-5-5", 0.09)))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        lateinit var worth: String
        lateinit var tokens: String
        lateinit var approx: String
        lateinit var details: List<String>
        rule.setContent {
            val res = LocalResources.current
            val card = usageCard(priced, pricedCost, 65_000)
            worth = res.getString(R.string.chat_reply_worth, formatCost(pricedCost.total))
            tokens = res.getString(R.string.chat_reply_tokens, Subagents.tokens(usageTotals(unpriced.models).total))
            approx = res.getString(R.string.chat_reply_approx, formatCost(personaCost.total))
            details = replyUsageLines(res, card)
            val pricedView = ReplyUsageView(worth, details, res.getString(R.string.chat_reply_details))
            val tokenView = ReplyUsageView(tokens, replyUsageLines(res, usageCard(unpriced, null)),
                res.getString(R.string.chat_reply_details))
            val personaView = ReplyUsageView(approx, personaUsageLines(res, personaCost), res.getString(R.string.chat_reply_persona_title))
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
                    MessageContent(Item("a1", role = "assistant", kind = "text", text = "已经修好。", ts = "2026-11-12T01:59:01Z"), user = false)
                    ReplyUsageFooter(Item("a1", ts = "2026-11-12T01:59:01Z"), pricedView)
                    MessageContent(Item("a2", role = "assistant", kind = "text", text = "两个模型都用了。", ts = "2026-11-12T02:01:00Z"), user = false)
                    ReplyUsageFooter(Item("a2", ts = "2026-11-12T02:01:00Z"), tokenView)
                    MessageContent(Item("a3", role = "assistant", kind = "text", text = "好的。", ts = "2026-11-12T02:02:00Z"), user = false)
                    ReplyUsageFooter(Item("a3", ts = "2026-11-12T02:02:00Z"), personaView)
                    ReplyUsageDetails(pricedView.lines)
                    ReplyUsageDetails(personaView.lines)
                }
            }
        }
        rule.waitForIdle()
        assertEquals("价值 $0.12", worth)
        assertEquals("23.4k tokens", tokens)
        assertEquals("约 $0.09", approx)
        assertEquals("本轮明细", details.first())
        assertEquals("本轮 token 价值：$0.12", details[1])
        rule.onNodeWithText(worth).assertIsDisplayed()
        rule.onNodeWithText(tokens).assertIsDisplayed()
        rule.onNodeWithText(approx).assertIsDisplayed()
        rule.onNodeWithText(details.first()).assertIsDisplayed()
        rule.onNodeWithText(details[1]).assertIsDisplayed()
        rule.onNodeWithText("这条回复约 $0.09，按公开价格估算").assertIsDisplayed()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s
            dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "reply-usage-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    /** Tap the worth label. The sheet is a dialog root, so the capture is that root, not the page under it. */
    @Test fun theUsageSheetOpens() {
        val priced = UsageSummary(listOf(
            ModelTokens("claude-opus-5-5", input = 2, output = 3400, cacheRead = 1_200_000, cacheWrite = 9000),
        ))
        val pricedCost = TurnCost(0.12, listOf(ModelShare("claude-opus-5-5", 0.12)))
        var dark by mutableStateOf(false)
        rule.setContent {
            val res = LocalResources.current
            val view = ReplyUsageView(
                res.getString(R.string.chat_reply_worth, formatCost(pricedCost.total)),
                replyUsageLines(res, usageCard(priced, pricedCost, 65_000)),
                res.getString(R.string.chat_reply_details),
            )
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    ReplyUsageFooter(Item("a1", ts = "2026-11-12T01:59:01Z"), view)
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("价值 $0.12").performClick()
        rule.mainClock.advanceTimeBy(1_000)
        rule.waitForIdle()
        rule.onNode(isDialog()).assertExists()
        rule.onNodeWithText("本轮明细").assertIsDisplayed()
        rule.onNodeWithText("本轮 token 价值：$0.12").assertIsDisplayed()
        for (d in listOf(false, true)) {
            dark = d
            rule.mainClock.advanceTimeBy(500)
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var other = 0
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) {
                if (bitmap.getPixel(x, y) != background) other++
            }
            assertTrue("glass/${if (d) "dark" else "light"} sheet drew nothing", other > 20)
            File(out(), "reply-usage-sheet-glass-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
