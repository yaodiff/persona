package persona.feature.inbox

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeLeft
import androidx.compose.ui.test.swipeRight
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.AppStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.SwipeTone
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.lang.reflect.Proxy
import kotlin.math.abs

/**
 * The Inbox's rows swipe as iOS's do (`InboxView`, #4210 T1): one swipe, from the end, Done in the positive tone or Move Back
 * with the undo arrow in the accent tone on a done item; none while a write for the row is out or items are being picked;
 * nothing towards the start, and never the hand-off. The item row in each state. A screen reader reaches the same action
 * from the row's Actions menu.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxRowSwipeTest {
    @get:Rule val rule = createComposeRule()

    private enum class Kind(val title: String) { Item("Synthetic item") }

    private val repository = Proxy.newProxyInstance(InboxRepository::class.java.classLoader, arrayOf(InboxRepository::class.java)) { proxy, method, args ->
        when (method.name) {
            "equals" -> proxy === args[0]
            "hashCode" -> System.identityHashCode(proxy)
            "toString" -> "RowSwipeRepository"
            else -> error("Unexpected repository access: ${method.name}")
        }
    } as InboxRepository
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private var done by mutableStateOf(false)
    private var busy by mutableStateOf(false)
    private var picking by mutableStateOf(false)
    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var dones = 0
    private var handOffs = 0
    private var opened = 0
    private lateinit var palette: persona.core.designsystem.PersonaPalette

    private val item = InboxRecord(InboxItem("lark:f1:7", kind = "dm", title = Kind.Item.title, sender = "Alex Example", preview = "Synthetic preview",
        tier = "action", version = "v1"), source = "lark", feedId = "f1", isGroup = false)

    private fun mount() {
        val selection = { InboxRowSelection(checked = false, selectable = true, onToggle = { opened++ }, onStart = {}) }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                palette = PersonaTheme.palette
                Box(Modifier.testTag("row").width(400.dp).background(PersonaTheme.palette.background)) {
                    val chosen = if (picking) selection() else null
                    InboxItemRow(item, repository, navigator, ReplyComposers(repository, rememberCoroutineScope()), GroupPosition.Single,
                        done = done, busy = busy, onlineSince = null, onDone = { dones++ }, onHandOff = { handOffs++ }, selection = chosen)
                }
            }
        }
        rule.waitForIdle()
    }

    private fun row(kind: Kind): SemanticsNodeInteraction = rule.onNode(hasText(kind.title) and hasClickAction())
    private fun swipeEnd(kind: Kind) { row(kind).performTouchInput { swipeLeft() }; rule.waitForIdle() }
    private fun swipeStart(kind: Kind) { row(kind).performTouchInput { swipeRight() }; rule.waitForIdle() }

    /**
     * Dragged to the middle of the row and held: what the gap shows. A row with no swipe takes the drag as a plain press that
     * moves, which becomes a long-press after 400 ms, so the whole drag is kept well under it.
     */
    private fun hold(kind: Kind, towardsTheEnd: Boolean = true) {
        row(kind).performTouchInput {
            down(center)
            moveTo(center + androidx.compose.ui.geometry.Offset(if (towardsTheEnd) -width * .08f else width * .08f, 0f), 50)
            moveTo(center + androidx.compose.ui.geometry.Offset(if (towardsTheEnd) -width * .35f else width * .35f, 0f), 100)
        }
        rule.waitForIdle()
    }
    private fun release(kind: Kind) { row(kind).performTouchInput { cancel() }; rule.waitForIdle() }

    private fun actionLabels(kind: Kind): List<String> =
        row(kind).fetchSemanticsNode().config.getOrNull(SemanticsActions.CustomActions).orEmpty().map { it.label }

    // ---- the action each state asks for -----------------------------------------------------------------------------

    @Test fun anOpenRowAsksForDoneInThePositiveTone() {
        val swipe = inboxRowSwipe("Done", done = false, busy = false, selecting = false) {}
        assertNotNull(swipe)
        assertEquals("Done", swipe!!.label)
        assertEquals(PersonaIcons.done, swipe.icon)
        assertEquals(SwipeTone.Positive, swipe.tone)
        assertEquals(Status.Unread, swipe.status)
        assertTrue(!swipe.destructive)
    }

    @Test fun aDoneRowAsksForMoveBackWithTheUndoArrowInTheAccentTone() {
        val swipe = inboxRowSwipe("Move Back", done = true, busy = false, selecting = false) {}
        assertNotNull(swipe)
        assertEquals("Move Back", swipe!!.label)
        assertEquals(PersonaIcons.undo, swipe.icon)
        assertEquals(SwipeTone.Accent, swipe.tone)
    }

    @Test fun aBusyRowOrOnePickedFromAsksForNothing() {
        for (done in listOf(false, true)) {
            assertNull(inboxRowSwipe("Done", done, busy = true, selecting = false) {})
            assertNull(inboxRowSwipe("Done", done, busy = false, selecting = true) {})
            assertNull(inboxRowSwipe("Done", done, busy = true, selecting = true) {})
        }
    }

    @Test fun theActionIsTheRowsOwnDoneCallbackAndNeverTheHandOff() {
        var called = 0
        inboxRowSwipe("Done", done = false, busy = false, selecting = false) { called++ }!!.onTrigger()
        assertEquals(1, called)
    }

    // ---- the gesture, on both kinds of row ------------------------------------------------------------------------

    private fun anOpenRowSwipesDoneFromTheEndAndNothingTowardsTheStart(kind: Kind) {
        mount()
        swipeStart(kind)
        assertEquals("nothing towards the start", 0, dones)
        rule.onNodeWithText("Done").assertDoesNotExist()
        hold(kind, towardsTheEnd = false)
        rule.onNodeWithText("Hand to AI").assertDoesNotExist()
        rule.onNodeWithText("Done").assertDoesNotExist()
        release(kind)
        hold(kind)
        rule.onNodeWithText("Done").assertExists()
        rule.onNodeWithText("Hand to AI").assertDoesNotExist()
        release(kind)
        assertEquals("a held drag files nothing", 0, dones)
        swipeEnd(kind)
        assertEquals(1, dones)
        assertEquals("the hand-off is not a swipe", 0, handOffs)
        assertEquals(0, opened)
    }

    @Test fun anOpenItemSwipesDoneFromTheEndOnly() = anOpenRowSwipesDoneFromTheEndAndNothingTowardsTheStart(Kind.Item)

    private fun aDoneRowSwipesMoveBackOnTheSameSwipe(kind: Kind) {
        done = true
        mount()
        swipeStart(kind)
        assertEquals(0, dones)
        hold(kind)
        rule.onNodeWithText("Move Back").assertExists()
        rule.onNodeWithText("Done").assertDoesNotExist()
        release(kind)
        swipeEnd(kind)
        assertEquals(1, dones)
        assertEquals(0, handOffs)
    }

    @Test fun aDoneItemSwipesMoveBackOnTheSameSwipe() = aDoneRowSwipesMoveBackOnTheSameSwipe(Kind.Item)

    private fun aBusyRowAndOnePickedFromHaveNoSwipe(kind: Kind) {
        busy = true
        mount()
        for (toward in listOf(true, false)) {
            hold(kind, towardsTheEnd = toward)
            rule.onNodeWithText("Done").assertDoesNotExist()
            rule.onNodeWithText("Hand to AI").assertDoesNotExist()
            release(kind)
        }
        swipeEnd(kind)
        swipeStart(kind)
        assertEquals("busy: no swipe", 0, dones)
        busy = false
        picking = true
        rule.waitForIdle()
        for (toward in listOf(true, false)) {
            hold(kind, towardsTheEnd = toward)
            rule.onNodeWithText("Done").assertDoesNotExist()
            rule.onNodeWithText("Hand to AI").assertDoesNotExist()
            release(kind)
        }
        swipeEnd(kind)
        swipeStart(kind)
        assertEquals("picking: no swipe", 0, dones)
        assertEquals(0, handOffs)
        // The row still answers a tap: while picking it picks (a sideways drag that ends on it counts as one, as it always did).
        val picked = opened
        row(kind).performClick()
        rule.waitForIdle()
        assertEquals(picked + 1, opened)
    }

    @Test fun aBusyItemAndOnePickedFromHaveNoSwipe() = aBusyRowAndOnePickedFromHaveNoSwipe(Kind.Item)

    // ---- a screen reader --------------------------------------------------------------------------------------------

    private fun aScreenReaderGetsTheSameActionOnTheRow(kind: Kind) {
        mount()
        assertEquals(listOf("Done"), actionLabels(kind))
        row(kind).fetchSemanticsNode().config.getOrNull(SemanticsActions.CustomActions)!!.single().action()
        rule.waitForIdle()
        assertEquals(1, dones)
        done = true
        rule.waitForIdle()
        assertEquals("a done row offers Move Back", listOf("Move Back"), actionLabels(kind))
        busy = true
        rule.waitForIdle()
        assertEquals("a busy row offers none", emptyList<String>(), actionLabels(kind))
        busy = false
        picking = true
        rule.waitForIdle()
        assertEquals("a row being picked from offers none", emptyList<String>(), actionLabels(kind))
        assertEquals(0, handOffs)
    }

    @Test fun aScreenReaderGetsTheItemsAction() = aScreenReaderGetsTheSameActionOnTheRow(Kind.Item)

    // ---- the colours, on Glass only -----------------------------------------------------------------------------

    private fun fillAtTheEnd(kind: Kind): Int {
        hold(kind)
        val image = rule.onNodeWithTag("row").captureToImage().asAndroidBitmap()
        val at = image.getPixel(image.width - 6, image.height / 2)
        release(kind)
        return at
    }

    private fun assertNear(expected: Color, actual: Int, message: String) {
        val want = expected.toArgb()
        for (shift in listOf(16, 8, 0)) {
            val delta = abs(((want shr shift) and 0xFF) - ((actual shr shift) and 0xFF))
            assertTrue("$message: expected ${Integer.toHexString(want)}, drawn ${Integer.toHexString(actual)}", delta <= 3)
        }
    }

    private fun glassFillsWithTheTint(kind: Kind) {
        mount()
        for (night in listOf(false, true)) {
            dark = night
            done = false
            rule.waitForIdle()
            assertNear(palette.positive, fillAtTheEnd(kind), "Done is the positive green on Glass, dark=$night")
            done = true
            rule.waitForIdle()
            assertNear(palette.accent, fillAtTheEnd(kind), "Move Back is the accent on Glass, dark=$night")
        }
    }

    private fun theOtherStylesKeepTheNeutralBacking(kind: Kind, appearance: AppStyle) {
        style = appearance
        mount()
        for (night in listOf(false, true)) {
            dark = night
            done = false
            rule.waitForIdle()
            assertNear(palette.statusSurface, fillAtTheEnd(kind), "$appearance keeps its neutral backing for Done, dark=$night")
            done = true
            rule.waitForIdle()
            assertNear(palette.statusSurface, fillAtTheEnd(kind), "$appearance keeps its neutral backing for Move Back, dark=$night")
        }
    }

    @Test fun glassFillsAnItemsSwipeWithTheTint() = glassFillsWithTheTint(Kind.Item)
    @Test fun briefKeepsAnItemsNeutralBacking() = theOtherStylesKeepTheNeutralBacking(Kind.Item, AppStyle.Brief)
    @Test fun boardKeepsAnItemsNeutralBacking() = theOtherStylesKeepTheNeutralBacking(Kind.Item, AppStyle.Board)
}
