package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import persona.core.contracts.Session

/** Filters must be repeated unchanged while following a frozen page or delta cursor. */
data class SessionListFilter(
    val state: String? = null,
    val machineId: String? = null,
    val project: String? = null,
    val group: String? = null,
    val source: String? = null,
    val provider: String? = null,
    val query: String = "",
    /** Local policy: the caller derives membership the paged API cannot express (#6108). Never sent as a query. */
    val requiresCompleteMembership: Boolean = false,
    /** Runtime lists fold children when sessions_hierarchy is advertised. Complete membership keeps their rows. */
    val roots: Boolean = true,
    val parentSessionId: String? = null,
)

@Serializable
data class SessionListPage(
    val sessions: List<Session> = emptyList(),
    val remove: List<String> = emptyList(),
    @SerialName("has_more") val hasMore: Boolean = false,
    @SerialName("next_cursor") val nextCursor: String? = null,
    val revision: String = "",
    val total: Int = 0,
)

@Serializable
data class SessionListCounts(
    val total: Int = 0,
    val machine: Map<String, Int> = emptyMap(),
    val project: Map<String, Int> = emptyMap(),
    val group: Map<String, Int> = emptyMap(),
    val state: Map<String, Int> = emptyMap(),
    val revision: String = "",
    /**
     * How many of [state]'s working only wait in the background (#6045): a client that shows them apart, as the
     * "Waiting in the background" filter, takes them out of its Working count. 0 from older hubs.
     */
    @SerialName("background_waiting") val backgroundWaiting: Int = 0,
)

/** An older hub cannot serve bounded lists; callers may use its legacy list API. */
class SessionListsUnsupported : Exception()

/** The hub no longer retains the snapshot/checkpoint; callers must load a new baseline. */
class SessionListExpired : Exception()
