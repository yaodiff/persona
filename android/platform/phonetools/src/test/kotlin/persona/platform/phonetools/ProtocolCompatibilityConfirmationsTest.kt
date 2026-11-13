package persona.platform.phonetools

import android.content.Context
import android.content.Intent
import android.net.Uri
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ProtocolCompatibilityConfirmationsTest {
    @Test fun existingImmutableNotificationActionsStillReachOnlyTheLiveConfirmation() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        for ((action, scheme) in listOf("jarvis.phone.confirm" to "jarvis-phone", "persona.phone.confirm" to "persona-phone")) {
            val token = "test-confirmation"
            var result: Boolean? = null
            PhoneConfirmations.callbacks[token] = { result = it }
            try {
                PhoneConfirmationReceiver().onReceive(context, Intent(action)
                    .setData(Uri.parse("$scheme://confirmation/$token/false"))
                    .putExtra("token", token).putExtra("allowed", false))
                assertEquals(false, result)
                result = null
                PhoneConfirmationReceiver().onReceive(context, Intent(action).putExtra("token", "expired-token"))
                assertNull(result)
            } finally { PhoneConfirmations.callbacks.remove(token) }
        }
    }

    /**
     * A stale notification's delete intent (jarvis-phone or persona-phone scheme, from before an upgrade) only
     * ends the confirmation its token names. With no live confirmation under that token nothing happens, so a
     * late "Clear all" cannot answer the next write's card.
     */
    @Test fun aStaleDeleteIntentDoesNotAnswerALaterConfirmation() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        for ((action, scheme) in listOf("jarvis.phone.confirm" to "jarvis-phone", "persona.phone.confirm" to "persona-phone")) {
            val staleToken = "stale-token"
            val liveToken = "live-token"
            var dismissed = false
            PhoneConfirmations.dismissals[liveToken] = { dismissed = true }
            try {
                PhoneConfirmationReceiver().onReceive(context, Intent(action)
                    .setData(Uri.parse("$scheme://confirmation/$staleToken/dismissed"))
                    .putExtra("token", staleToken).putExtra(PhoneConfirmations.DISMISSED, true))
                assertFalse("a dismissal for an unknown token must not touch the live confirmation", dismissed)
                // The live token's own dismissal still reaches it.
                PhoneConfirmationReceiver().onReceive(context, Intent(action)
                    .setData(Uri.parse("$scheme://confirmation/$liveToken/dismissed"))
                    .putExtra("token", liveToken).putExtra(PhoneConfirmations.DISMISSED, true))
                assertTrue(dismissed)
            } finally { PhoneConfirmations.dismissals.remove(liveToken) }
        }
    }
}
