package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import java.io.File
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.Tokens
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** "Discard" under a message whose delivery is unknown, in the timeline (beside "Retry" / "Check message") and in "Check message"'s sheet. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class OutboxStatusTest {
    @get:Rule val rule = createComposeRule()

    private val message = OutboxMessage("m", SessionKey("box", "s"), "帮我看看日程", DeliveryState.NeedsConfirmation)

    private fun show(state: DeliveryState, details: Boolean, actions: MutableList<DeliveryAction>) = rule.setContent {
        PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
            OutboxStatus(message.copy(state = state), busy = false, onAction = { actions += it },
                onDetails = if (details) ({}) else null)
        }
    }

    @Test fun theTimelineOffersDiscardAfterRetryAndCheck() {
        val actions = mutableListOf<DeliveryAction>()
        show(DeliveryState.NeedsConfirmation, details = true, actions)
        rule.onNodeWithText("重试").assertExists()
        rule.onNodeWithText("核对消息").assertExists()
        rule.onNodeWithText("丢弃").performClick()
        assertEquals(listOf(DeliveryAction.Discard), actions)
    }

    @Test fun theCheckSheetOffersDiscardToo() {
        val actions = mutableListOf<DeliveryAction>()
        // The sheet draws the same status without "Check message" (it is that page).
        show(DeliveryState.Unknown, details = false, actions)
        rule.onAllNodesWithText("核对消息").assertCountEquals(0)
        rule.onNodeWithText("丢弃").performClick()
        assertEquals(listOf(DeliveryAction.Discard), actions)
    }

    @Test
    @GraphicsMode(GraphicsMode.Mode.NATIVE)
    fun drawsDiscardInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.testTag("shot").width(360.dp).background(PersonaTheme.palette.background)
                    .padding(Tokens.Space.l)) {
                    OutboxStatus(message, busy = false, onAction = {}, onDetails = {})
                    OutboxStatus(message, busy = false, onAction = {})
                }
            }
        }
        val directory = File("build/outbox-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.onAllNodesWithText("丢弃").assertCountEquals(2)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(directory, "outbox-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                org.junit.Assert.assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }

    @Test fun aMessageThatIsStillGoingHasNoDiscard() {
        show(DeliveryState.Queued, details = true, mutableListOf())
        rule.onAllNodesWithText("丢弃").assertCountEquals(0)
    }

    @Test fun resendConfirmationDialogClosesWhenStateLeavesNeedsConfirmation() {
        var state by mutableStateOf(DeliveryState.NeedsConfirmation)
        val actions = mutableListOf<DeliveryAction>()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                OutboxStatus(message.copy(state = state), busy = false, onAction = { actions += it }, onDetails = {})
            }
        }
        rule.onNodeWithText("重试").performClick()
        rule.onNodeWithText("重发这条消息？").assertExists()
        rule.runOnIdle { state = DeliveryState.Sent }
        rule.onAllNodesWithText("重发这条消息？").assertCountEquals(0)
        assertEquals(emptyList<DeliveryAction>(), actions)
    }
}
