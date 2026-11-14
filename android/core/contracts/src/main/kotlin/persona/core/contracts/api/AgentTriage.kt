package persona.core.contracts.api

import persona.core.contracts.CoreTextException
import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Settings › "Session triage" (docs/assistant/agent-work.md "Switches"): what the hub does on its own when an agent stops. All off until the user
 * turns them on: the triage calls a model on every stopped turn, and continuing / waking run a turn of the agent.
 */
@Serializable
data class TriageSwitches(
    val enabled: Boolean = false,
    @SerialName("auto_continue") val autoContinue: Boolean = false,
    val wake: Boolean = false,
    val decide: Boolean = false,
    /**
     * Remind When Seen, Unanswered (#5096): push once when an agent the user looked at still waits on their answer. Rules only, no
     * tokens, and apart from [enabled]. Absent (an older hub, or never set) is on: read it through [remindsReply]; a
     * change always sends it, since a value equal to the default would not be encoded.
     */
    @SerialName("reply_reminder") val replyReminder: Boolean? = null,
    /**
     * Ask for a closeout (#3612, docs/assistant/agent-work.md "Closeout"): hold an agent's stop once when a turn that did
     * work ends without one. Null from a hub that predates it, which has no such switch; a PUT without it keeps the hub's
     * value, and a change always sends it.
     */
    val closeout: Boolean? = null,
    /**
     * Ask for the status afterwards (#3616, docs/assistant/agent-work.md "Closeout"): ask once about work already done when an
     * unsupervised agent stops without a closeout. Null from a hub that predates it, which has no such switch; a PUT
     * without it keeps the hub's value, and a change always sends it.
     */
    @SerialName("closeout_ask") val closeoutAsk: Boolean? = null,
) {
    val remindsReply: Boolean get() = replyReminder != false
}

/** GET / PUT /api/v1/agents/triage on the active pairing; [TriageUnsupported] from hubs without it. */
interface AgentTriage {
    suspend fun switches(): TriageSwitches
    suspend fun put(value: TriageSwitches): TriageSwitches
}

class TriageUnsupported : CoreTextException(CoreText.of(Key.TriageUnsupported))
