package persona.platform.notify

import persona.core.contracts.GroupKeys
import persona.core.contracts.Interaction
import persona.core.contracts.InteractionEvent
import persona.core.contracts.InteractionResolvedEvent
import persona.core.contracts.NotifyEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.DoneNotification
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.SessionsRepository
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.buffer
import kotlinx.coroutines.flow.filter
import java.time.Instant

/**
 * The events [NotificationDelivery.accept] acts on, taken off the hub's event flow at once and handed on in order (#4038).
 * The merged event flow has no buffer: `emit` returns only after every subscriber took the event, so a subscriber busy
 * with one (accept reads the preferences and the cards over the network) held up every other live event, chat
 * streaming included, until it was done.
 */
internal fun Flow<ServerEvent>.notificationEvents(): Flow<ServerEvent> =
    filter { it is NotifyEvent || it is InteractionEvent || it is InteractionResolvedEvent }.buffer(Channel.UNLIMITED)

/** Live alerts require hub-approved notify events; [catchUp] is the bounded fallback when no live service can run. */
internal class NotificationDelivery(
    private val interactions: InteractionCenter,
    private val sessions: SessionsRepository,
    private val suppressed: (SessionKey) -> Boolean,
    /** Catch-up supplies its own hub group's preferences; live passes null so Notifier rechecks the current setting. */
    private val postInteraction: (Interaction, Boolean, DeviceNotificationPreferences?) -> Unit,
    private val postNotify: (NotifyEvent) -> Unit,
    private val retract: (String) -> Unit,
    private val preferences: suspend () -> DeviceNotificationPreferences? = { DeviceNotificationPreferences() },
    private val notified: () -> Set<String> = { emptySet() },
    private val desktopOnline: (String) -> Boolean? = { null },
    private val now: () -> Instant = Instant::now,
    private val catchUpPreferences: suspend () -> Map<String?, DeviceNotificationPreferences> = {
        preferences()?.let { mapOf<String?, DeviceNotificationPreferences>(null to it) }.orEmpty()
    },
    /** Only notifications showing now, not the delivered/dismissed history used to suppress a second arrival. */
    private val showing: () -> Set<String> = notified,
    private val retractOccurrence: ((String, String) -> Unit)? = null,
    private val notifiedOccurrences: () -> Map<String, String?> = { emptyMap() },
) {
    private fun retractCard(id: String, createdAt: String?) {
        if (createdAt != null && retractOccurrence != null) retractOccurrence.invoke(id, createdAt) else retract(id)
    }

    suspend fun accept(event: ServerEvent) {
        when (event) {
            is InteractionResolvedEvent -> retractCard(event.id, event.createdAt)
            is InteractionEvent -> {
                // A current snapshot is not a new arrival. Only redraw a notification this device already delivered,
                // with the server's latest deadline and without notifying again for an unseen card.
                if (event.interaction.isDisplayMirror || !event.interaction.canClarify || event.interaction.id !in showing()) return
                interactions.refresh()
                val current = interactions.pending.value.firstOrNull { it.id == event.interaction.id } ?: return
                if (NotificationPolicy.expired(current)) { retractCard(current.id, current.createdAt); return }
                val session = current.sessionKey.takeIf { it.machineId.isNotBlank() && it.sessionId.isNotBlank() }
                if (session == null || !suppressed(session)) postInteraction(current, false, null)
            }
            is NotifyEvent -> {
                val session = NotificationPolicy.session(event)
                if (session != null && (suppressed(session) || quiet(session))) return
                if (event.kind != "interaction" && NotificationPolicy.channel(event) == null) return
                val settings = preferences() ?: return
                if ((event.kind == "interaction" || event.kind == "alert" || event.kind == "reply") && !settings.input) return
                if (event.kind == "done" && settings.done == DoneNotification.Off) return
                // Long/Always and session mute/seen suppression remain the hub's decision.
                if (session != null && event.kind != "assistant" && sessions.sessions.value.none { it.key == session }) sessions.refresh()
                if (event.kind == "interaction") {
                    val id = event.interactionId ?: return
                    interactions.refresh()
                    // A card outside any session (`personad ask` in a plain terminal, #4515) has an empty session id: it
                    // is found by the event's own ids, and with no conversation of its own to be looking at, never held back.
                    val key = SessionKey(event.machineId, event.sessionId)
                    val ix = interactions.pending.value.firstOrNull { it.id == id && it.sessionKey == key } ?: return
                    if (ix.isDisplayMirror) return
                    if (NotificationPolicy.expired(ix)) { retractCard(id, ix.createdAt); return }
                    if (session == null || !suppressed(session)) postInteraction(ix, event.quiet, null)
                } else if (session == null || !suppressed(session)) postNotify(event)
            }
            else -> Unit
        }
    }

    /**
     * Fetches the current pending cards once when Android could not run the resident service. Null preferences and a
     * failed refresh throw so WorkManager retries; no card is treated as absent merely because startup is incomplete.
     */
    suspend fun catchUp() {
        val settings = catchUpPreferences()
        check(settings.isNotEmpty()) { "Notification preferences are not ready" }
        if (settings.values.none { it.input }) return
        sessions.refresh()
        interactions.refresh()
        val currentTime = now()
        val pending = interactions.pending.value.filterNot { it.isDisplayMirror }.map { ix ->
            ix to checkNotNull(settings[GroupKeys.namespaceOf(ix.machineId)]) {
                "Notification preferences are not ready for the request's hub group"
            }
        }
        val alreadyNotified = notified()
        val occurrences = notifiedOccurrences()
        val pendingIds = pending.mapTo(HashSet()) { it.first.id }
        // A resolved occurrence clears only its own alert and history, never a same-ID replacement's.
        ((alreadyNotified + occurrences.keys) - pendingIds).forEach { retractCard(it, occurrences[it]) }
        pending.forEach { (ix, groupSettings) ->
            if (NotificationPolicy.expired(ix, currentTime)) {
                retractCard(ix.id, ix.createdAt)
                return@forEach
            }
            val delivered = if (ix.createdAt != null && occurrences.isNotEmpty()) occurrences[ix.id] == ix.createdAt else ix.id in alreadyNotified
            if (
                !groupSettings.input ||
                delivered ||
                (ix.manager ?: "assistant") in groupSettings.mutedManagers.orEmpty()
            ) return@forEach
            if (groupSettings.muteWhileDesktop == true && desktopOnline(ix.machineId) == true) return@forEach
            val session = ix.sessionKey.takeIf { it.machineId.isNotBlank() && it.sessionId.isNotBlank() }
            if (session != null && (suppressed(session) || quiet(session))) return@forEach
            postInteraction(ix, NotificationPolicy.hushForCatchUp(groupSettings.quiet, currentTime), groupSettings)
        }
    }

    /** A quiet task's agent (#1731): the hub sends no alert for it; one from an older hub is not shown either. */
    private fun quiet(session: SessionKey) = sessions.sessions.value.any { it.key == session && it.quiet }
}
