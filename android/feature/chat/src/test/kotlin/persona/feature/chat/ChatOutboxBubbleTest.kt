package persona.feature.chat

import android.content.Context
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.SessionKey
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/**
 * An unsent message on Glass as iOS's `PendingBubble` (#4339 C9): its mark and small menu by state, and the long press's last
 * word.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ChatOutboxBubbleTest {
    @get:Rule val rule = createComposeRule()
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun eachStateHasIOSsMarkAndMenu() {
        assertEquals(OutboxMark.Sending, outboxMark(DeliveryState.Sending))
        assertEquals(OutboxMark.Waiting, outboxMark(DeliveryState.Queued))
        assertEquals(OutboxMark.Waiting, outboxMark(DeliveryState.WaitingForNetwork))
        assertEquals(OutboxMark.Failed, outboxMark(DeliveryState.Refused))
        assertEquals(OutboxMark.Failed, outboxMark(DeliveryState.Rejected))
        // What the phone has to check keeps Android's status line; a delivered one has no mark and no line.
        for (state in listOf(DeliveryState.Unknown, DeliveryState.Verifying, DeliveryState.NeedsConfirmation)) {
            assertNull("$state", outboxMark(state))
            assertFalse("$state", drawsAsPendingBubble(state))
        }
        assertNull(outboxMark(DeliveryState.Sent))
        assertTrue(drawsAsPendingBubble(DeliveryState.Sent))
        assertEquals(listOf(DeliveryAction.RetryNow, DeliveryAction.Cancel), outboxMarkActions(OutboxMark.Waiting))
        assertEquals(listOf(DeliveryAction.Retry, DeliveryAction.Delete), outboxMarkActions(OutboxMark.Failed))
        assertTrue(outboxMarkActions(OutboxMark.Sending).isEmpty())
        // Every action of a mark's menu is one its state allows.
        for (state in DeliveryState.entries) outboxMark(state)?.let { mark ->
            outboxMarkActions(mark).forEach { assertTrue("$state $it", state.allows(it)) }
        }
        assertTrue(DeliveryState.Queued.allows(DeliveryAction.RetryNow))
        assertFalse(DeliveryState.Refused.allows(DeliveryAction.RetryNow))
    }

    /** The long press ends in the mark's own word on Glass ("Don't send" while it waits), the status line's elsewhere. */
    @Test fun theLongPressSaysDontSendOnGlass() {
        val waiting = MessageTarget("m1", "words", true, "words", DeliveryState.WaitingForNetwork)
        fun last(extras: MessageExtras, target: MessageTarget = waiting) =
            messageMenuActions(target, writes = true, handOff = false, res, extras) {}.last().label
        assertEquals("Don't send", last(MessageExtras(delete = true, dontSend = true)))
        assertEquals("Cancel send", last(MessageExtras(delete = true)))
        assertEquals("Delete", last(MessageExtras(delete = true, dontSend = true), waiting.copy(pending = DeliveryState.Refused)))
    }

    /** The waiting mark's menu: Retry now, then Don't send; also TalkBack's actions on the mark. */
    @Test fun theMarksMenuActs() {
        val done = mutableListOf<DeliveryAction>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                OutboxBubble("Also add a line", "m1", DeliveryState.WaitingForNetwork, busy = false) { done += it }
            }
        }
        rule.onNodeWithText("Sends when connected").assertExists()
        val mark = rule.onNodeWithContentDescription("Waiting for network, will send once connected")
        assertEquals(listOf("Retry now", "Don't send"), mark.fetchSemanticsNode().config[SemanticsActions.CustomActions].map { it.label })
        mark.performClick()
        rule.onNodeWithText("Retry now").performClick()
        rule.waitForIdle()
        mark.performClick()
        rule.onNodeWithText("Don't send").performClick()
        rule.waitForIdle()
        assertEquals(listOf(DeliveryAction.RetryNow, DeliveryAction.Cancel), done)
    }

    /** One that did not go: a red mark whose menu is Resend and Delete; a read-only chat's mark has no menu. */
    @Test fun aFailedMarkOffersResendAndDelete() {
        val done = mutableListOf<DeliveryAction>()
        var readOnly by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = true, reduceMotion = true, style = AppStyle.Glass) {
                OutboxBubble("Refused words", "m2", DeliveryState.Refused, busy = false, onAction = if (readOnly) null else { a -> done += a })
            }
        }
        val mark = rule.onNodeWithContentDescription("Failed to send. Resend or delete.")
        mark.performClick()
        rule.onNodeWithText("Resend").performClick()
        rule.waitForIdle()
        mark.performClick()
        rule.onNodeWithText("Delete").performClick()
        rule.waitForIdle()
        assertEquals(listOf(DeliveryAction.Retry, DeliveryAction.Delete), done)
        readOnly = true
        rule.waitForIdle()
        assertNull(mark.fetchSemanticsNode().config.getOrNull(SemanticsActions.OnClick))
        rule.onNodeWithText("Sends when connected").assertDoesNotExist()
    }

    @Test fun aStoppedChatsMarkOnlyOffersDeleteIncludingTalkBack() {
        val done = mutableListOf<DeliveryAction>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                OutboxBubble("Refused words", "m2", DeliveryState.Refused, busy = false, canSend = false) { done += it }
            }
        }
        val mark = rule.onNodeWithContentDescription("Not sent")
        assertEquals(listOf("Delete"), mark.fetchSemanticsNode().config[SemanticsActions.CustomActions].map { it.label })
        mark.performClick()
        rule.onNodeWithText("Resend").assertDoesNotExist()
        rule.onNodeWithText("Delete").performClick()
        assertEquals(listOf(DeliveryAction.Delete), done)
        val labels = messageMenuActions(MessageTarget("m2", "Refused words", true, "Refused words", DeliveryState.Refused),
            writes = false, handOff = false, res, MessageExtras(delete = true)) {}.map { it.label }
        assertTrue(labels.contains("Copy"))
        assertFalse(labels.contains("Edit"))
    }

    @Test fun aStoppedChatsStatusOffersDeleteWithoutRetryOrEdit() {
        val done = mutableListOf<DeliveryAction>()
        var state by mutableStateOf(DeliveryState.Refused)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Brief) {
                OutboxStatus(OutboxMessage("m2", SessionKey("desk", "s"), "Refused words", state), false,
                    onAction = { done += it }, canSend = false)
            }
        }
        rule.onNodeWithText("Retry").assertDoesNotExist()
        rule.onNodeWithText("Edit").assertDoesNotExist()
        rule.onNodeWithText("Delete").performClick()
        assertEquals(listOf(DeliveryAction.Delete), done)
        rule.runOnIdle { state = DeliveryState.Queued }
        rule.onNodeWithText("Edit").assertDoesNotExist()
        rule.onNodeWithText("Retry").assertDoesNotExist()
    }
}
