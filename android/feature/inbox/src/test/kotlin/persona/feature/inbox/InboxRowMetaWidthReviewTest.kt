package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.InboxEventFields
import persona.core.contracts.InboxFollow
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.lang.reflect.Proxy

/**
 * The row with `where` and a shared state word: the state sits at the third line's trailing side, so a long one must not
 * take the whole line away from `where · sender: preview` (`spec/fixtures/inbox/row.json`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi")
class InboxRowMetaWidthReviewTest {
    @get:Rule val rule = createComposeRule()
    private val repository = Proxy.newProxyInstance(InboxRepository::class.java.classLoader, arrayOf(InboxRepository::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args[0]
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "MetaWidthRepository"
            else -> error("Unexpected repository access: ${method.name}")
        }
    } as InboxRepository
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    /** The drawn layout of the one node whose text contains [text]. */
    private fun layoutOf(text: String, context: String): androidx.compose.ui.text.TextLayoutResult {
        val nodes = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes()
        assertEquals("$context: one node holds $text", 1, nodes.size)
        val layouts = mutableListOf<androidx.compose.ui.text.TextLayoutResult>()
        nodes.single().config[SemanticsActions.GetTextLayoutResult].action!!(layouts)
        return layouts.single()
    }

    @Test fun theThirdLineStaysWholeAndTheStateSitsUnderIt() {
        // F-7 (the lead's ruling): the state is its own line under the third line, in every width, as the web's
        // EventMark is; only a state longer than the whole column is ellipsized. The shared showcase's first row at
        // phone width is the case that used to lose its third line entirely.
        val thirdLine = "订单群 · 张三：好的，我看一下新的"
        val state = "等张三确认新报价 · 明天 09:00 再看：问更正后的报价行不行"
        var style by mutableStateOf(AppStyle.Glass)
        val showcase = InboxRecord(
            InboxItem("sc-1", kind = "group", title = "春季订单的报价", sender = "张三", preview = "好的，我看一下新的",
                summary = "张三发现税率错了，正在等他确认更正后的报价", tier = "action", updatedAt = "2026-01-01T11:21:00Z",
                status = "working", where = "订单群"),
            source = "chat", isGroup = true, groupName = "订单群", count = 3,
            event = InboxEventFields(status = "working", now = "waiting",
                follow = InboxFollow(who = "张三确认新报价", next = "问更正后的报价行不行", checkAt = "2026-01-02T09:00:00Z")),
        )
        rule.setContent {
            CompositionLocalProvider(LocalInboxClock provides { java.time.Instant.parse("2026-01-01T12:00:00Z") },
                LocalInboxZone provides java.time.ZoneOffset.UTC) {
                PersonaTheme(style = style, darkTheme = false, reduceMotion = true) {
                    CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, 1f)) {
                        Box(Modifier.width(400.dp).background(PersonaTheme.palette.background)) {
                            InboxItemRow(showcase, repository, navigator, ReplyComposers(repository, rememberCoroutineScope()),
                                GroupPosition.Single, done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        // The third line is whole: no ellipsis, every glyph inside its width.
        val line = layoutOf(thirdLine, "the third line")
        assertFalse("the third line must not be cut: ${line.layoutInput.text.text}", line.hasVisualOverflow)
        // The state has a line of its own, under it, with real width. Its own text may be longer than the column
        // (the ruling allows an ellipsis there), so what is checked is that it was given the line and starts in view.
        val label = layoutOf(state, "the state")
        assertTrue("the state must get a line of its own (was ${label.size})", label.size.width > 200f)
        assertTrue("the state must start inside the column", label.getBoundingBox(0).left >= 0f)
        val lineNode = rule.onAllNodesWithText(thirdLine, useUnmergedTree = true).fetchSemanticsNodes().single()
        val stateNode = rule.onAllNodesWithText(state, useUnmergedTree = true).fetchSemanticsNodes().single()
        assertTrue("the state sits under the third line", stateNode.boundsInRoot.top >= lineNode.boundsInRoot.bottom - 1f)
    }

}

/** The row's time with a title: same line at the default text size, under the title once the text grows (from 1.5×). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w360dp-h800dp-xhdpi")
class InboxRowTitleTimeReviewTest {
    @get:Rule val rule = createComposeRule()
    private val repository = Proxy.newProxyInstance(InboxRepository::class.java.classLoader, arrayOf(InboxRepository::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args[0]
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "TitleTimeRepository"
            else -> error("Unexpected repository access: ${method.name}")
        }
    } as InboxRepository
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    // The row formats in the device's zone and clock, so the expected string is computed the same way.
    private val res = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val time = displayTime("2026-11-05T14:30:00Z", java.util.Locale.ENGLISH, res)
    private fun record() = InboxRecord(
        InboxItem("time-1", kind = "group", title = "Approve the spring launch checklist and confirm every owner",
            sender = "Alex", preview = "Can we move it to Friday?", tier = "action", updatedAt = "2026-11-05T14:30:00Z"),
        source = "chat", isGroup = true, groupName = "Launch group", count = 1,
        event = InboxEventFields(where = "Launch group", now = "confirm_reply"),
    )

    @Test fun theTimeSitsUnderTheTitleOnlyOnceTheTextGrows() {
        var scale by mutableStateOf(1f)
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale)) {
                PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                    Box(Modifier.width(360.dp).background(PersonaTheme.palette.background)) {
                        InboxItemRow(record(), repository, navigator, ReplyComposers(repository, rememberCoroutineScope()),
                            GroupPosition.Single, done = false, busy = false, onlineSince = null, onDone = {}, onHandOff = {})
                    }
                }
            }
        }
        assertTrue(time.isNotBlank())
        // Default size: one line for the title and the time at its trailing edge, as every row has always been.
        rule.waitForIdle()
        val titleOne = rule.onAllNodesWithText(record().item.title, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot
        val timeOne = rule.onAllNodesWithText(time, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot
        assertTrue("the time shares the title's line at 1x: $titleOne / $timeOne", timeOne.top < titleOne.bottom)

        // At 1.5× and more the time moves below the title rather than squeezing its first glyph away.
        rule.runOnIdle { scale = 2f }
        rule.waitForIdle()
        val titleTwo = rule.onAllNodesWithText(record().item.title, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot
        val timeTwo = rule.onAllNodesWithText(time, useUnmergedTree = true).fetchSemanticsNodes().single().boundsInRoot
        assertTrue("the time must sit below the title at 2x: $titleTwo / $timeTwo", timeTwo.top >= titleTwo.bottom)
    }
}
