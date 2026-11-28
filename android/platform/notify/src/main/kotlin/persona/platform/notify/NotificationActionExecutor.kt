package persona.platform.notify

import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.InteractionGoneException
import persona.core.designsystem.PendingStatus
import persona.core.domain.answer.RequestExpiry
import persona.core.domain.sort.canApprove
import java.time.Instant
import kotlin.coroutines.cancellation.CancellationException

/**
 * [NotSent]: the hub could not be asked before the claim, so nothing was sent and the button can be tapped again (#5008).
 * [AnsweredHere]: the request is no longer pending and this phone had already claimed it (a second tap before the first one's
 * notification went away), so it was this phone's own answer: nothing to say, as for [Submitted], not [Gone]'s "Handled on
 * the computer" (#2483).
 */
internal enum class NotificationActionResult { Submitted, Expired, Gone, Locked, Invalid, Duplicate, ReadOnly, NotSent, AnsweredHere }

/** Claim immediately before sending; a lost response must never cause another resolve. */
internal class NotificationActionExecutor(
    private val center: InteractionCenter,
    private val claim: (String) -> Boolean,
    private val now: () -> Instant = Instant::now,
    /**
     * The session a request belongs to, when known. One whose source does not let the phone approve (a Codex thread not
     * joined live) is answered on the computer; an unknown one is left to the hub, which refuses the same.
     */
    private val session: suspend (SessionKey) -> Session? = { null },
    /** Whether this phone already claimed a request; only reads, never claims ([NotificationActionJournal.claimed]). */
    private val claimed: (String) -> Boolean = { false },
    /** Exact-occurrence durable claims used by the real journal; legacy callers retain the ID-only functions above. */
    private val claimExact: ((String, String) -> Boolean)? = null,
    private val claimedExact: ((String, String) -> Boolean)? = null,
) {
    /**
     * A notification button's executor ([ActionReceiver]): its claims and its check for a request this phone already
     * answered both go to this phone's [journal] (#2483). [onClaimed] hears each claim it makes.
     */
    constructor(center: InteractionCenter, journal: NotificationActionJournal, session: suspend (SessionKey) -> Session?,
        onClaimed: () -> Unit = {}) :
        this(center, { id -> journal.claim(id).also { if (it) onClaimed() } }, session = session, claimed = journal::claimed,
            claimExact = { id, occurrence -> journal.claim(id, occurrence).also { if (it) onClaimed() } },
            claimedExact = { id, occurrence -> journal.claimed(id, occurrence) })

    suspend fun execute(
        id: String,
        key: String,
        reply: String?,
        /** Whether the phone is locked; may wait for an unlock in progress to land (lockedAfterUnlockSettles). */
        locked: suspend () -> Boolean,
        onInteraction: (Interaction) -> Unit = {},
        expiresAt: String? = null,
        createdAt: String? = null,
        onProgress: (PendingStatus) -> Unit = {},
    ): NotificationActionResult {
        val cached = center.pending.value.firstOrNull { it.id == id }
        // A prepared card may have a newer server deadline than the notification's stale timestamp. The mandatory
        // refresh below still decides its actual expiry; an old-hub or uncached action keeps the fail-closed shortcut.
        if (cached?.canClarify != true && RequestExpiry.expired(expiresAt, now())) return NotificationActionResult.Expired
        if (cached != null && createdAt != null && cached.createdAt != createdAt) return NotificationActionResult.Gone
        cached?.let { onInteraction(it) }
        onProgress(PendingStatus.Verifying)
        // Always query the hub: a locally pending request may already be handled elsewhere. Its own hub group, since another
        // group answering would leave this one's stale card to be claimed (#5697).
        try { center.refresh(id) }
        catch (e: CancellationException) { throw e }
        catch (_: Exception) { return NotificationActionResult.NotSent }
        val ix = center.pending.value.firstOrNull { it.id == id }
            ?: return if (createdAt != null && claimedExact?.invoke(id, createdAt) == true || claimed(id)) {
                NotificationActionResult.AnsweredHere
            } else NotificationActionResult.Gone
        // The system may deliver an old notification after a same-ID replacement. A refresh proving that the displayed
        // occurrence is gone ends this action; it must never rebind the tap to the replacement. Legacy notifications
        // without an occurrence retain their previous ID-only behavior.
        if (createdAt != null && ix.createdAt != createdAt) return NotificationActionResult.Gone
        onInteraction(ix)
        if (NotificationPolicy.expired(ix, now())) return NotificationActionResult.Expired
        if (session(ix.sessionKey)?.canApprove == false) return NotificationActionResult.ReadOnly
        val spec = NotificationPolicy.actions(ix).firstOrNull { it.key == key } ?: return NotificationActionResult.Invalid
        if (spec.authenticationRequired && locked()) return NotificationActionResult.Locked
        val resolution = NotificationPolicy.resolution(ix, key, reply) ?: return NotificationActionResult.Invalid
        if (NotificationPolicy.expired(ix, now())) return NotificationActionResult.Expired
        val claimedNow = if (createdAt != null && claimExact != null) claimExact.invoke(id, createdAt) else claim(id)
        if (!claimedNow) return NotificationActionResult.Duplicate
        onProgress(PendingStatus.Sending)
        // Persistence and notification updates may cross the deadline; check at the transport boundary.
        if (NotificationPolicy.expired(ix, now())) return NotificationActionResult.Expired
        try { center.resolveOccurrence(ix, resolution) }
        catch (_: InteractionGoneException) { return NotificationActionResult.Gone }
        return NotificationActionResult.Submitted
    }
}
