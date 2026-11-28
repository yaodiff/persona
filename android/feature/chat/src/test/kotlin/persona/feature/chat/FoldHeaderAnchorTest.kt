package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.chatlist.ChatList
import persona.core.chatlist.ChatListEffects
import persona.core.chatlist.ChatListState
import persona.core.chatlist.holdPosition
import persona.core.contracts.MessageSource
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.WorkGroup
import persona.core.domain.tasks.BackgroundRound

/**
 * Opening a fold must keep its header where the finger left it. The list is reversed: rows that open under the header
 * are inserted at lower indices and, while following the newest end, grow toward the top of the screen.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class FoldHeaderAnchorTest {
    @get:Rule val rule = createComposeRule()

    private enum class Kind(val id: String, val label: String) {
        Work("work-1", "Did 3 steps"),
        Assistant("assistant-1", "Checked 3 things"),
        Background("bg-1", ""),
    }

    private var kind by mutableStateOf(Kind.Work)
    private var extraRows by mutableIntStateOf(0)
    private var loading by mutableStateOf(false)
    private lateinit var state: ChatListState

    private fun rows(kind: Kind, expanded: Boolean, extra: Int): List<String> {
        val newer = (0 until 4).map { "newer-$it" }
        val steps = if (expanded) (0 until 4 + extra).map { "step-$it" } else emptyList()
        val older = (0 until 18).map { "older-$it" }
        return newer + steps + kind.id + older
    }

    private fun setHarness() {
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, reduceMotion = true) {
                key(kind) {
                    val list = persona.core.chatlist.rememberChatListState()
                    state = list
                    LaunchedEffect(loading) { list.noteExpansionLoading(loading) }
                    val current = rows(kind, kind.id in list.expanded, extraRows)
                    ChatListEffects(list, current, ready = true, onFreeze = {}, onFollow = {}, loadOlder = {})
                    ChatList(list, Modifier.width(400.dp).height(520.dp).testTag("chat"),
                        contentPadding = PaddingValues(0.dp)) {
                        items(current, key = { it }) { row ->
                            when {
                                row == kind.id -> FoldHeader(kind, kind.id in list.expanded) {
                                    val opened = kind.id !in list.expanded
                                    val anchored = when {
                                        opened -> list.beginToggle(kind.id, riseToTop = list.followsEnd)
                                        !list.followsEnd -> list.beginToggle(kind.id, riseToTop = false)
                                        else -> false
                                    }
                                    if (!anchored) list.endExpansion()
                                    list.setExpanded(
                                        if (opened) list.expanded + kind.id else list.expanded - kind.id,
                                    )
                                    if (opened) list.noteExpansionLoading(loading)
                                }
                                row.startsWith("step-") -> Box(
                                    Modifier.fillMaxWidth().height(72.dp).testTag(row),
                                ) { Text("Expanded step $row") }
                                else -> Box(Modifier.fillMaxWidth().height(72.dp).testTag(row)) { Text(row) }
                            }
                        }
                    }
                }
            }
        }
    }

    @Composable
    private fun FoldHeader(kind: Kind, open: Boolean, onToggle: () -> Unit) {
        when (kind) {
            Kind.Work -> WorkGroup("Did 3 steps", open, onToggle, Modifier.testTag("header")) {}
            Kind.Assistant -> AssistantFoldLine("Checked 3 things", open, Modifier.testTag("header"), onToggle)
            Kind.Background -> BackgroundRoundLine(
                BackgroundRound(kind.id, MessageSource("scheduled_task", title = "Morning report"), false, open, emptyList()),
                onToggle = { onToggle() },
            )
        }
    }

    private fun waitForList() {
        rule.waitUntil(8_000) {
            this::state.isInitialized && state.restored && state.list.layoutInfo.totalItemsCount > 0
        }
        rule.waitForIdle()
    }

    private fun switchTo(next: Kind) {
        if (this::state.isInitialized && kind == next) {
            waitForList()
            return
        }
        val previous = if (this::state.isInitialized) state else null
        kind = next
        rule.waitUntil(8_000) { this::state.isInitialized && state !== previous && state.restored }
        rule.waitForIdle()
    }

    private fun clickHeader(kind: Kind) {
        when (kind) {
            Kind.Work, Kind.Assistant -> rule.onNode(
                hasClickAction() and hasContentDescription(kind.label, substring = true),
            ).performClick()
            Kind.Background -> rule.onNodeWithTag("background-round-${kind.id}").performClick()
        }
    }

    private fun tapHeader(kind: Kind) {
        clickHeader(kind)
        rule.waitUntil(8_000) { kind.id in state.expanded }
    }

    private fun placeHeaderAwayFromTheNewestEnd(kind: Kind) {
        val index = rows(kind, expanded = false, extra = 0).indexOf(kind.id)
        rule.runOnIdle {
            runBlocking {
                state.list.scrollToItem(index, 0)
                state.list.scrollBy(-state.list.layoutInfo.viewportEndOffset * 0.45f)
            }
            state.holdPosition()
        }
        rule.waitForIdle()
        assertFalse(state.followsEnd)
    }

    private fun headerOffset(): Int? =
        state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == kind.id }?.offset

    @Test fun ordinaryExpansionKeepsEveryFoldHeaderInPlace() {
        setHarness()
        waitForList()
        for (each in Kind.entries) {
            switchTo(each)
            extraRows = 0
            loading = false
            waitForList()
            placeHeaderAwayFromTheNewestEnd(each)
            val before = headerOffset()
            assertNotNull("${each.name} header must be on screen before the tap", before)

            tapHeader(each)
            rule.waitUntil(8_000) { !state.expanding }
            rule.waitForIdle()

            val after = headerOffset()
            assertNotNull("${each.name} header must stay on screen", after)
            assertEquals("${each.name} header moved", before!!.toFloat(), after!!.toFloat(), 2f)
            rule.onNodeWithTag("step-3").assertIsDisplayed()

            // The header is still the collapse target once the fold is read.
            clickHeader(each)
            rule.waitUntil(8_000) { each.id !in state.expanded }
            rule.waitForIdle()
            assertNotNull("${each.name} header disappeared when the fold closed", headerOffset())
            rule.onNodeWithTag("step-3").assertDoesNotExist()
        }
    }

    @Test fun rowsArrivingAfterTheTapDoNotPushTheHeaderAway() {
        kind = Kind.Work
        extraRows = 0
        loading = true
        setHarness()
        waitForList()
        placeHeaderAwayFromTheNewestEnd(kind)
        val before = headerOffset()
        assertNotNull(before)

        tapHeader(kind)
        rule.waitUntil(8_000) { state.expanding }
        rule.waitUntil(8_000) { headerOffset() != null }
        val afterFirstLayout = headerOffset()
        assertNotNull("the opened header is still laid out", afterFirstLayout)
        assertEquals(before!!.toFloat(), afterFirstLayout!!.toFloat(), 2f)
        val countBefore = state.list.layoutInfo.totalItemsCount

        // The hub fold arrives later: the list inserts more rows below the header and the anchor has to correct again.
        extraRows = 9
        loading = false
        rule.waitUntil(8_000) { !state.expanding }
        rule.waitForIdle()

        val afterSecondGrowth = headerOffset()
        assertNotNull(afterSecondGrowth)
        assertEquals("second growth moved the header", before.toFloat(), afterSecondGrowth!!.toFloat(), 2f)
        assertTrue("the second growth inserted rows", state.list.layoutInfo.totalItemsCount > countBefore)
    }

    @Test fun expansionAtTheNewestEndKeepsTheHeaderInTheViewport() {
        extraRows = 20
        loading = false
        setHarness()
        waitForList()
        assertTrue(state.followsEnd)
        assertNotNull(headerOffset())

        for (each in Kind.entries) {
            switchTo(each)
            extraRows = 20
            waitForList()
            assertTrue("${each.name} starts at the newest end", state.followsEnd)
            tapHeader(each)
            assertFalse("${each.name} kept following the newest end after the tap", state.followsEnd)
            rule.waitUntil(8_000) { !state.expanding }
            rule.waitForIdle()

            val info = state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == kind.id }
            assertNotNull("${each.name} header was pushed out of the viewport", info)
            val top = state.list.layoutInfo.viewportEndOffset - (info!!.offset + info.size)
            assertTrue("${each.name} header top $top is above the viewport", top >= -1)
            assertTrue("${each.name} header bottom ${state.list.layoutInfo.viewportEndOffset - info.offset} is below the viewport",
                state.list.layoutInfo.viewportEndOffset - info.offset <= state.list.layoutInfo.viewportEndOffset + 1)
            assertFalse("${each.name} no longer follows the newest end after its header was held", state.followsEnd)
        }
    }
}
