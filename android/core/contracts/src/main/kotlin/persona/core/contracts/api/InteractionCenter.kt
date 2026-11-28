package persona.core.contracts.api

import persona.core.contracts.DelegatedAnswer
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.emptyFlow

interface InteractionCenter {
    val pending: StateFlow<List<Interaction>>
    /**
     * Requests the hub just pushed that no card showed before, as they arrive: the app plays attention for each, as iOS
     * does (`Delegated.announces`). A snapshot that lists a request, again or for the first time, is not an arrival.
     */
    val arrivals: Flow<Interaction> get() = emptyFlow()
    suspend fun refresh()
    /** [refresh], as far as request [id] is concerned: merged over hub groups, only the group it belongs to (#5697). */
    suspend fun refresh(id: String) = refresh()
    suspend fun resolve(id: String, resolution: Resolution)
    /** Hide this card on this device durably, without contacting or answering its source. */
    suspend fun discardLocal(id: String) { error("Local dismissal is unavailable") }
    /** [resolve], with what the hub said about it ([ResolveOutcome]). */
    suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome {
        resolve(id, resolution)
        return ResolveOutcome()
    }
    /** A full-card hand-off approval declares itself on this request alone; all other calls keep the old path. */
    suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution)

    /** Answers the occurrence the caller displayed, not a same-ID replacement that arrived before its coroutine ran. */
    suspend fun resolveOccurrence(interaction: Interaction, resolution: Resolution, handoffCard: Boolean = false): ResolveOutcome =
        resolveOutcome(interaction.id, resolution, handoffCard)

    /** Replies to the exact proposal and removes it only after the hub confirms that same target. */
    suspend fun replyOwnerApproval(interaction: Interaction, phrase: String): Unit =
        throw UnsupportedOperationException("Exact owner replies unavailable")

    /**
     * Questions the assistant answered for the user, oldest first (iOS `AppModel.delegatedAnswers`): from
     * `interaction_resolved` with `answered_by: "assistant"`, or the hub's `already` reply to an answer of this device's
     * that came second. Each stays for its chat until put away ([dropDelegated]); the oldest go first past a bound.
     */
    val delegated: StateFlow<List<DelegatedAnswer>> get() = NoDelegated
    /** What the user changed an answer to, by card id ([reanswer] here, or `interaction_reanswered` from any device). */
    val reanswered: StateFlow<Map<String, Map<String, String>>> get() = NoReanswers
    /** Puts an answered question's card away; what was changed stays in [reanswered]. */
    fun dropDelegated(id: String) {}
    /**
     * Change Answer (POST /api/v1/interactions/{id}/reanswer): [answers] (only the changed ones) go to the Agent as a
     * message, through the card's [manager]; noted in [reanswered] once the hub took them.
     */
    suspend fun reanswer(id: String, answers: Map<String, String>, manager: String?): Unit =
        throw UnsupportedOperationException("Change Answer unavailable")
}

private val NoDelegated = MutableStateFlow(emptyList<DelegatedAnswer>()).asStateFlow()
private val NoReanswers = MutableStateFlow(emptyMap<String, Map<String, String>>()).asStateFlow()
