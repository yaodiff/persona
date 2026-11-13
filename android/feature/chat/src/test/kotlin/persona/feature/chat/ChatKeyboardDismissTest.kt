package persona.feature.chat

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.click
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeDown
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.chatlist.ChatList
import persona.core.chatlist.ChatListEffects
import persona.core.chatlist.ChatListState
import persona.core.chatlist.rememberChatListState
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ComposerFrame
import persona.core.designsystem.HomeChrome
import persona.core.designsystem.JPage
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.NavItem
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PhIcons

/**
 * The conversation's messages under a focused composer (#3056): a tap on a reply's blank width puts the keyboard away,
 * as on the rest of the page; the reply's long press (its menu), its link and the field do their own thing and leave it
 * up. Dragging the list puts it away too (#4210, iOS `ChatView`'s scroll phase), a field in a request card included,
 * while a scroll the page starts, with no finger on the list, leaves it.
 *
 * Robolectric is not in touch mode, where Android hands the focus straight back to the first field after it is
 * cleared, so a drag is told by what was asked of the focus manager and the keyboard, and by the field losing focus
 * once, not by the field staying unfocused.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-mdpi")
class ChatKeyboardDismissTest {
    @get:Rule val rule = createComposeRule()

    private var focused by mutableStateOf(false)
    private var style by mutableStateOf(AppStyle.Glass)
    private val opened = mutableListOf<String>()
    private var focusLost = 0
    private var cardFocused = false
    private var cardFocusLost = 0
    private val probe = KeyboardProbe()
    private lateinit var chatList: ChatListState
    private lateinit var scope: CoroutineScope
    private val menu = MessageMenuState()
    private val actions = ChatRowActions(SessionKey("mac", "s1"), fullText = { it.text.orEmpty() }, openPath = null,
        photos = null, pictures = { emptyList() }, openImages = { _, _ -> }, menu = menu, retryLineId = null, onRetry = {})

    private fun setConversation() {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalChatRowActions provides actions, LocalUriHandler provides object : UriHandler {
                    override fun openUri(uri: String) { opened += uri }
                }) { probe.Around {
                    var text by remember { mutableStateOf("") }
                    var cardText by remember { mutableStateOf("") }
                    scope = rememberCoroutineScope()
                    JPage("对话", bottomBar = {
                        Column {
                            // A request card's answer field, above the composer as the pending stack puts its cards.
                            BasicTextField(cardText, { cardText = it }, Modifier.fillMaxWidth().testTag("card-field").onFocusChanged {
                                if (cardFocused && !it.isFocused) cardFocusLost++
                                cardFocused = it.isFocused
                            })
                            ComposerFrame {
                                BasicTextField(text, { text = it }, Modifier.fillMaxWidth().testTag("field").onFocusChanged {
                                    if (focused && !it.isFocused) focusLost++
                                    focused = it.isFocused
                                })
                            }
                        }
                    }) { padding ->
                        // The page's own effects, which watch the user's finger on the list (newest row first).
                        chatList = rememberChatListState()
                        ChatListEffects(chatList, listOf("short", "link") + (0 until 40).map { "older-$it" }, ready = true,
                            onFreeze = {}, onFollow = {}, loadOlder = {})
                        ChatList(chatList, Modifier.fillMaxSize().testTag("list"),
                            contentPadding = PaddingValues(top = padding.calculateTopPadding(), bottom = padding.calculateBottomPadding())) {
                            item("short") { MessageContent(Item("short", role = "assistant", kind = "text", text = "好的"), user = false) }
                            item("link") { MessageContent(Item("link", role = "assistant", kind = "text", text = "见 [文档](https://example.com/doc)"), user = false) }
                            items(40, key = { "older-$it" }) { MessageContent(Item("older-$it", role = "assistant", kind = "text", text = "更早的回复 $it"), user = false) }
                        }
                    }
                } }
            }
        }
        focusField()
    }

    private fun focusField() {
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.waitForIdle()
        assertTrue("the field takes focus when tapped", focused)
    }

    @Test fun tapBesideAShortReplyClearsFocusInEveryStyle() {
        setConversation()
        for (each in AppStyle.entries) {
            style = each
            rule.waitForIdle()
            focusField()
            // The reply spans the row; past its words the row is blank.
            rule.onNodeWithText("好的").performTouchInput { click(centerRight.copy(x = centerRight.x + 200.dp.toPx())) }
            rule.waitForIdle()
            assertFalse("a tap on a reply's blank width clears the focus in $style", focused)
            assertNull("a tap opens no menu", menu.target)
        }
    }

    @Test fun longPressOpensTheMenuAndKeepsFocus() {
        setConversation()
        rule.onNodeWithText("好的").performTouchInput { longClick() }
        rule.waitForIdle()
        assertNotNull(menu.target)
        assertEquals("short", menu.target?.id)
        assertTrue(focused)
    }

    // #5724: without it, a screen reader reaches nothing in the menu but "Quote" (and only where quoting is offered).
    @Test fun accessibilityLongClickOpensTheMenu() {
        setConversation()
        rule.onNode(hasAnyDescendant(hasText("好的")) and SemanticsMatcher("long click labelled 更多操作") {
            it.config.getOrNull(SemanticsActions.OnLongClick)?.label == "更多操作"
        }).performSemanticsAction(SemanticsActions.OnLongClick)
        rule.waitForIdle()
        assertEquals("short", menu.target?.id)
    }

    @Test fun linkInAReplyOpensAndKeepsFocus() {
        setConversation()
        // The link's own node: on the JVM a glyph is a pixel or two wide, too narrow to aim at by offset.
        rule.onNode(hasClickAction() and hasAnyAncestor(hasText("见 文档")), useUnmergedTree = true).performTouchInput { click() }
        rule.waitForIdle()
        assertEquals(listOf("https://example.com/doc"), opened)
        assertTrue(focused)
    }

    // Dragging the conversation puts the keyboard away, in every style, as iOS's `.interacting` scroll phase does.
    @Test fun draggingTheConversationPutsTheKeyboardAwayInEveryStyle() {
        setConversation()
        for (each in AppStyle.entries) {
            style = each
            rule.waitForIdle()
            focusField()
            val lost = focusLost
            probe.reset()
            rule.onNodeWithTag("list").performTouchInput { swipeDown() }
            rule.waitForIdle()
            assertEquals("a drag takes the focus from the composer in $each", lost + 1, focusLost)
            assertTrue("a drag asks the focus to clear, as a tap does, in $each: ${probe.focusClears}",
                probe.focusClears.isNotEmpty() && probe.focusClears.none { it })
            assertTrue("a drag hides the keyboard in $each", probe.hides > 0)
            assertNull("a drag opens no menu", menu.target)
        }
    }

    @Test fun draggingTheOtherWayDoesTheSame() {
        setConversation()
        rule.onNodeWithTag("list").performTouchInput { swipeUp() }
        rule.waitForIdle()
        assertEquals(1, focusLost)
        assertTrue(probe.hides > 0)
    }

    // The pending stack's cards sit above the composer, outside the list; the drag takes the focus from their field too.
    @Test fun draggingTakesTheFocusFromAFieldInARequestCardToo() {
        setConversation()
        rule.onNodeWithTag("card-field").performTouchInput { click() }
        rule.waitForIdle()
        assertTrue("the card's field takes focus when tapped", cardFocused)
        probe.reset()
        rule.onNodeWithTag("list").performTouchInput { swipeDown() }
        rule.waitForIdle()
        assertEquals(1, cardFocusLost)
        assertTrue(probe.hides > 0)
    }

    // A scroll the page starts (following the newest message, a jump to a message or a search hit) has no finger on the
    // list: whoever is typing keeps the keyboard.
    @Test fun aScrollThePageStartsKeepsFocusAndTheKeyboard() {
        setConversation()
        probe.reset()
        scope.launch { chatList.list.scrollToItem(20) }
        rule.waitForIdle()
        assertEquals("the list moved", 20, chatList.list.firstVisibleItemIndex)
        scope.launch { chatList.list.animateScrollToItem(5) }
        rule.waitForIdle()
        assertEquals("and moved again, animated", 5, chatList.list.firstVisibleItemIndex)
        scope.launch { chatList.list.scrollToItem(0) }
        rule.waitForIdle()
        assertTrue("the field keeps the focus", focused)
        assertEquals("the focus was never cleared", emptyList<Boolean>(), probe.focusClears)
        assertEquals("nothing hid the keyboard", 0, probe.hides)
    }

    // A tap is still a tap: on blank space it puts the keyboard away as before, once; on the field or as a long press it does not.
    @Test fun aTapOnBlankSpaceStillHidesTheKeyboardOnce() {
        setConversation()
        probe.reset()
        rule.onNodeWithText("好的").performTouchInput { click(centerRight.copy(x = centerRight.x + 200.dp.toPx())) }
        rule.waitForIdle()
        assertFalse(focused)
        assertEquals(listOf(false), probe.focusClears)
        assertEquals(1, probe.hides)
    }

    @Test fun aTapOnTheFieldOrALongPressNeverHidesTheKeyboard() {
        setConversation()
        probe.reset()
        rule.onNodeWithTag("field").performTouchInput { click() }
        rule.onNodeWithText("好的").performTouchInput { longClick() }
        rule.waitForIdle()
        assertTrue(focused)
        assertEquals(emptyList<Boolean>(), probe.focusClears)
        assertEquals(0, probe.hides)
    }

    @Test fun tappingTheRealComposerKeepsItsFieldFocused() {
        lateinit var composer: ChatComposerState
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                composer = rememberChatComposerState()
                CompositionLocalProvider(LocalHomeChrome provides HomeChrome(
                    listOf(NavItem("home", "Home", PhIcons.House)), "home", onSelect = {}, leading = {})) {
                    JPage("对话", bottomBar = {
                        ChatComposer(SessionKey("box", "s1"), composer, working = false, sending = false, canClear = false,
                            service = null, onSend = {}, onCommand = {}, onNotice = {}, modeHint = "默认模式",
                            accessory = { Text("建议操作", Modifier.testTag("suggestion")) })
                    }) { _ -> }
                }
            }
        }
        rule.onNodeWithTag("suggestion").assertDoesNotExist()
        rule.onNode(hasSetTextAction()).performTouchInput { click() }
        rule.waitForIdle()
        assertTrue(composer.focused)
        rule.onNodeWithTag("suggestion").assertExists()
        rule.onNode(hasSetTextAction()).performTextInput("a")
        rule.waitForIdle()
        assertEquals("a", composer.value.text)
        assertTrue(composer.focused)
        rule.onNodeWithTag("suggestion").assertExists()
    }
}
