package persona.platform.notify

import android.app.KeyguardManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.core.app.RemoteInput
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout

/** Resolve through the shared interaction center; ambiguous failures are never retried automatically. */
open class ActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val keyguard = context.getSystemService(KeyguardManager::class.java)
        if (intent.action == RESUME) {
            // "Resume" needs an unlock, which the system reports a moment after it let the button through.
            val pending = goAsync()
            CoroutineScope(SupervisorJob() + Dispatchers.Main).launch {
                try {
                    if (!lockedAfterUnlockSettles({ keyguard.isDeviceLocked })) Notify.resume(context).join()
                } finally { pending.finish() }
            }
            return
        }
        if (intent.action != RESOLVE) return
        val id = intent.getStringExtra(EXTRA_ID) ?: return
        val action = intent.getStringExtra(EXTRA_ACTION) ?: return
        val createdAt = intent.getStringExtra(EXTRA_CREATED_AT)
        val reply = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(EXTRA_REPLY)?.toString()
        val dependencies = Notify.dependencies(context)
        val pending = goAsync()
        CoroutineScope(SupervisorJob() + Dispatchers.IO).launch {
            var session = intent.getStringExtra(EXTRA_MACHINE)?.let { machine ->
                intent.getStringExtra(EXTRA_SESSION)?.let { persona.core.contracts.SessionKey(machine, it) }
            }
            val assistant = if (intent.hasExtra(EXTRA_ASSISTANT)) intent.getBooleanExtra(EXTRA_ASSISTANT, false) else null
            // Taps on the ongoing notification: no "Checking" / "Sending" notice of its own (that would pop a new heads-up when no
            // "Needs you" notification is showing); results and failures still get one.
            val quiet = intent.getBooleanExtra(EXTRA_QUIET, false)
            // Nothing goes to the hub before the claim: a failure up to then (hub unreachable, the timeout) was not sent.
            var claimed = false
            var request: persona.core.contracts.Interaction? = null
            try {
                // A bounded receiver lifetime is a timeout, not a delivery guarantee.
                withTimeout(8_000) {
                    dependencies.pairing().restore()
                    val sessions = dependencies.sessions()
                    // Claims and the second-tap check share this phone's journal: a request gone from the hub that it
                    // claimed was its own answer, with no notice (#2483).
                    val result = NotificationActionExecutor(
                        dependencies.interactions(), dependencies.actionJournal(),
                        session = { key ->
                            // A cold start may not have the list yet: one refresh, then the hub decides.
                            sessions.sessions.value.firstOrNull { it.key == key } ?: run {
                                try { sessions.refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                                sessions.sessions.value.firstOrNull { it.key == key }
                            }
                        },
                        onClaimed = { claimed = true },
                    ).execute(id, action, reply,
                        locked = { lockedAfterUnlockSettles({ keyguard.isDeviceLocked }) },
                        onInteraction = { session = it.sessionKey; request = it },
                        expiresAt = intent.getStringExtra(EXTRA_EXPIRES_AT),
                        createdAt = createdAt,
                        onProgress = { if (!quiet) dependencies.notifier().pending(id, session, it, assistant, createdAt) },
                    )
                    if (result == NotificationActionResult.NotSent) {
                        dependencies.notifier().notSent(id, request, session, assistant, createdAt)
                        return@withTimeout
                    }
                    if (result != NotificationActionResult.Locked) dependencies.notifier().retract(id, createdAt)
                    if (result != NotificationActionResult.Submitted) {
                        dependencies.notifier().actionResult(id, session, result, assistant, createdAt)
                    }
                }
            } catch (failure: Exception) {
                if (claimed) dependencies.notifier().failure(id, session, assistant, createdAt)
                else dependencies.notifier().notSent(id, request, session, assistant, createdAt)
                if (failure is CancellationException) throw failure
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        internal const val RESOLVE = "persona.notify.resolve"
        internal const val RESUME = "persona.notify.resume"
        internal const val EXTRA_MACHINE = "machine_id"
        internal const val EXTRA_SESSION = "session_id"
        internal const val EXTRA_ASSISTANT = "assistant"
        internal const val EXTRA_ID = "interaction_id"
        internal const val EXTRA_ACTION = "resolution_action"
        internal const val EXTRA_REPLY = "resolution_reply"
        internal const val EXTRA_EXPIRES_AT = "expires_at"
        internal const val EXTRA_CREATED_AT = "created_at"
        internal const val EXTRA_QUIET = "quiet"
    }
}
