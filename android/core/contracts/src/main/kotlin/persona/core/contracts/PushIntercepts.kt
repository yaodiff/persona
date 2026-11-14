package persona.core.contracts

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** One push the hub held in low-distraction mode. Optional destination fields allow rolling upgrades. */
@Serializable
data class PushIntercept(
    val at: String,
    val level: String,
    val reason: String,
    val summary: String? = null,
    val title: String? = null,
    val body: String? = null,
    @SerialName("related_key") val relatedKey: String? = null,
    val manager: String? = null,
    val also: List<String>? = null,
    @SerialName("todo_id") val todoId: String? = null,
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    val group: String? = null,
    val url: String? = null,
)

@Serializable
data class LnModeResponse(val enabled: Boolean)

@Serializable
data class PushInterceptsResponse(val intercepts: List<PushIntercept>)
