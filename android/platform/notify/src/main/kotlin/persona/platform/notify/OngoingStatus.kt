package persona.platform.notify

import android.os.Build
import androidx.annotation.StringRes
import persona.core.contracts.FleetState
import persona.core.contracts.Interaction
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.domain.answer.AnswerEntry
import persona.core.domain.answer.ApprovalAction
import persona.core.domain.answer.ApprovalPolicy
import persona.core.domain.answer.Authentication
import persona.core.domain.answer.needsFieldValues
import persona.core.domain.fleet.FleetSummary
import persona.core.domain.fleet.FleetText
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.conflate
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.flow

/**
 * The ongoing notification (the iPhone Live Activity's counterpart, docs/android/android.md "Ongoing summary notification"): the hub's fleet
 * summed up when there is something to show, otherwise the connection line. Only visible content takes part in
 * equality, so the throttled updates skip anything the user would not see change.
 */
internal data class OngoingStatus(
    /** The fleet's headline; empty when [titleRes] says the connection's state instead. */
    val title: String,
    /** The first agent, or the request [action] settles: "name · what it is doing"; without lock screen details only "An approval is waiting". */
    val text: String? = null,
    /** The other counts: "3 working · 1 finished". */
    val subText: String? = null,
    /** Expanded: up to three agents with where they run, then "+N more"; without lock screen details, kinds and counts. */
    val lines: String? = null,
    /** The lock screen's version: counts and "An approval is waiting", never names or content. */
    val publicText: String? = null,
    val criticalText: String? = null,
    val needsYou: Int = 0,
    val working: Int = 0,
    val unread: Int = 0,
    /** Only working agents left: the clock runs from when the longest turn started (epoch milliseconds). */
    val workingSince: Long? = null,
    /** The one agent a tap opens; null opens Agents. */
    val open: SessionKey? = null,
    /** The first permission or plan, settled from the notification's buttons. */
    val action: OngoingAction? = null,
    /** The connection line, as a string resource. */
    @StringRes val titleRes: Int? = null,
) {
    /** The fleet summed up, not the connection line. */
    val summary: Boolean get() = criticalText != null

    /** [sdkFull]: Build.VERSION.SDK_INT_FULL; promoted notifications came in Android 16 QPR1 (36.1), not 36.0. */
    fun shouldPromote(sdkFull: Int, allowed: Boolean, enabled: Boolean = true): Boolean =
        sdkFull >= Build.VERSION_CODES_FULL.BAKLAVA_1 && allowed && enabled && criticalText != null

    companion object {
        /**
         * [quiet]: only finished agents have been left for a while (hub fleetDoneLinger): back to the connection line.
         * [details]: the device's "Show details on lock screen". Off (the default), the body and the expanded list say only what kinds of
         * requests wait and how many, never an agent's name, place or what it asks: a phone set to show all notification
         * content on its lock screen shows the private version there too.
         * [pending]: the open cards, to tell a permission with fields to fill in (no Allow, #4987).
         */
        fun from(
            state: ConnectionState, fleet: FleetState, quiet: Boolean = false, details: Boolean = false,
            pending: List<Interaction> = emptyList(),
        ): OngoingStatus {
            // A disconnected snapshot must not look like current work or expose an old request.
            if (state != ConnectionState.Online || FleetSummary.isEmpty(fleet) || quiet) return connection(state)
            val needsYou = fleet.needsYou.coerceAtLeast(0)
            val working = fleet.working.coerceAtLeast(0)
            val unread = fleet.unread.coerceAtLeast(0)
            val shown = fleet.items.take(MAX_ITEMS)
            val actionable = FleetSummary.firstActionable(fleet)
            val action = actionable?.let { item ->
                val card = pending.firstOrNull { it.id == item.interaction }
                OngoingAction.of(item.kind, item.interaction.orEmpty(), FleetSummary.key(item),
                    // A fleet row can arrive before its card. Until the card is known, open the app rather than guessing
                    // that this is an ordinary permission; a secret hand-off must never gain a quick Allow.
                    needsFieldValues = card?.needsFieldValues ?: true,
                    secretHandoff = card?.isSecretHandoff ?: (item.kind == "permission"), createdAt = card?.createdAt)
            }
            // With buttons, the body is the request they settle (like iOS), not an older question above it.
            val first = if (action != null) actionable else shown.firstOrNull()
            val firstRequest = (if (action != null) actionable else shown.firstOrNull(FleetSummary::needsYou))
                ?.let(FleetText::anonymous)?.shown
            val text: String?
            val lines: String?
            if (details) {
                val hidden = FleetSummary.hidden(fleet, shown.size)
                text = (first?.let(FleetText::row) ?: FleetText.headline(fleet)).shown
                lines = (shown.map(FleetText::rowWithPlace) + listOfNotNull(hidden.takeIf { it > 0 }?.let(FleetText::more))).map { it.shown }
                    .joinToString("\n").ifEmpty { null }
            } else {
                // The counts of working and finished agents are the subtext already; only requests get lines of their own.
                val requests = shown.filter(FleetSummary::needsYou)
                text = firstRequest
                lines = (FleetText.anonymousRequests(requests) +
                    listOfNotNull((needsYou - requests.size).takeIf { it > 0 }?.let(FleetText::more))).map { it.shown }
                    .joinToString("\n").ifEmpty { null }?.takeIf { it != text }
            }
            val publicText = listOf(firstRequest.orEmpty(), FleetText.rest(fleet).shown)
                .filter(String::isNotEmpty).joinToString(" · ").ifEmpty { null }
            val critical = when {
                needsYou > 0 -> "💬${compact(needsYou)}"
                working > 0 -> "⚙${compact(working)}"
                unread > 0 -> "●${compact(unread)}"
                else -> null
            }
            return OngoingStatus(
                title = FleetText.headline(fleet).shown, text = text,
                subText = FleetText.rest(fleet).shown.ifEmpty { null }, lines = lines, publicText = publicText,
                criticalText = critical, needsYou = needsYou, working = working, unread = unread,
                workingSince = fleet.workingSince?.takeIf { FleetSummary.phase(fleet) == FleetSummary.WORKING && it > 0 }
                    ?.let { (it * 1000).toLong() },
                open = FleetSummary.single(fleet),
                action = action,
            )
        }

        /** Only the connection line: no fleet to show. */
        fun connection(state: ConnectionState) = OngoingStatus("", titleRes = ongoingTitle(state))

        const val MAX_ITEMS = 3

        private fun compact(count: Int) = if (count > 999) "999+" else count.toString()
    }
}

/** One button on the ongoing notification: answers a request through ActionReceiver, or opens its conversation. */
internal data class OngoingButton(@StringRes val label: Int, val key: String?, val authenticationRequired: Boolean)

/**
 * The first permission or plan and its buttons, from the lock screen entry of the shared policy: "Deny" / "Allow" for a
 * permission, "View" / "Approve" for a plan (acceptEdits). Saying yes needs an unlocked phone; saying no does not.
 */
internal data class OngoingAction(val kind: String, val interaction: String, val session: SessionKey?, val buttons: List<OngoingButton>, val createdAt: String? = null) {
    companion object {
        /** [needsFieldValues]: allowing needs values this app has no form for, so only Deny (#4987). */
        fun of(kind: String, interaction: String, session: SessionKey?, needsFieldValues: Boolean = false,
            secretHandoff: Boolean = false, createdAt: String? = null): OngoingAction? {
            if (interaction.isEmpty()) return null
            val policy = ApprovalPolicy.actions(AnswerEntry.LockScreen, kind, locked = true).associateBy { it.id }
            fun answer(id: ApprovalAction, @StringRes label: Int) = policy[id]?.let {
                OngoingButton(label, id.wire, it.authentication != Authentication.None)
            }
            val buttons = when (kind) {
                "permission" -> if (secretHandoff) listOf(OngoingButton(R.string.notify_action_view, null, false))
                else listOfNotNull(answer(ApprovalAction.Deny, R.string.notify_action_deny),
                    if (needsFieldValues) null else answer(ApprovalAction.Allow, R.string.notify_action_allow))
                "plan" -> listOfNotNull(policy[ApprovalAction.OpenApp]?.let { OngoingButton(R.string.notify_action_view, null, false) },
                    answer(ApprovalAction.ApproveEdits, R.string.notify_action_approve))
                else -> emptyList()
            }
            return OngoingAction(kind, interaction, session, buttons, createdAt).takeIf { buttons.isNotEmpty() }
        }
    }
}

/** Keep the newest pending value; continuous input cannot postpone delivery indefinitely. */
internal fun <T> Flow<T>.throttleOngoing(intervalMillis: Long = 2_500): Flow<T> = flow {
    require(intervalMillis > 0)
    this@throttleOngoing.distinctUntilChanged().conflate().distinctUntilChanged().collect {
        emit(it)
        delay(intervalMillis)
    }
}
