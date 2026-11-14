@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts

import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.decodeFromJsonElement

// Inbox phase 1 (docs/assistant/inbox.md"Events, handling chains, and suggestions"): an inbox item grows into an event. Every field is optional and an
// older hub sends none of them, so an item without them reads exactly as before.

/** The processing conversation bound to an event (`handling`). */
@KeepGeneratedSerializer
@Serializable(with = InboxHandlingSerializer::class)
data class InboxHandling(
    val owner: String = "",
    @WireEnum("looking", "agent", "waiting", "done", "failed", "unknown") val state: String = "unknown",
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    @WireTime val since: String? = null,
    val error: String? = null,
) {
    /** The manager, a sub-session or the other side is on it: the event is In progress. */
    val working: Boolean get() = state in setOf("looking", "agent", "waiting")
}

/** The one current suggestion (`suggestion`); confirming sends [version] back. */
@KeepGeneratedSerializer
@Serializable(with = InboxSuggestionSerializer::class)
data class InboxSuggestion(
    val version: Long = 0,
    @WireEnum("reply", "agent", "conclusion", "unknown") val kind: String = "unknown",
    val text: String = "",
    val note: String? = null,
    /** Needs judgment: confirming hands it back to the processing conversation instead of running it. */
    val handback: Boolean = false,
    @SerialName("machine_id") val machineId: String? = null,
    @WireTime val at: String? = null,
)

/** Who a follow-up waits on, what happens next, and when to look again (`follow`). */
@KeepGeneratedSerializer
@Serializable(with = InboxFollowSerializer::class)
data class InboxFollow(
    val who: String? = null,
    val next: String? = null,
    @WireTime @SerialName("check_at") val checkAt: String? = null,
)

/** One message of a `received` entry. */
@KeepGeneratedSerializer
@Serializable(with = InboxChainMessageSerializer::class)
data class InboxChainMessage(val sender: String = "", val text: String = "", @WireTime val at: String? = null)

/**
 * Where tapping a chain entry goes. `source` with [position] (Feishu), [uid] (mail) or [url] (a connector's), or just
 * the item's source; `session` the conversation [machineId] / [sessionId]; `item` another inbox item [id].
 */
@KeepGeneratedSerializer
@Serializable(with = InboxChainLinkSerializer::class)
data class InboxChainLink(
    val type: String = "",
    /** A Feishu message position; the hub writes it as a string, a number is read too. */
    val position: JsonPrimitive? = null,
    val uid: Long? = null,
    val url: String? = null,
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    val id: String? = null,
) {
    val positionValue: Long? get() = position?.contentOrNull?.toLongOrNull()
}

/** One key call of the Handling chain; a [kind] this app does not know is drawn from [text] with a generic icon. */
@KeepGeneratedSerializer
@Serializable(with = InboxChainEntrySerializer::class)
data class InboxChainEntry(
    val id: String = "",
    @WireTime val at: String? = null,
    val kind: String = "",
    val text: String = "",
    val edited: Boolean = false,
    val detail: String? = null,
    val count: Int? = null,
    val sender: String? = null,
    val messages: List<InboxChainMessage> = emptyList(),
    val link: InboxChainLink? = null,
    val via: String? = null,
)

/** Another item this one is Related (`related`): each goes its own way, tapping one opens it. */
@KeepGeneratedSerializer
@Serializable(with = InboxRelatedSerializer::class)
data class InboxRelated(val id: String = "", val title: String = "", val source: String = "")

/** An item's event fields, as [inboxEventFields] reads them; older hubs omit each one. */
data class InboxEventFields(
    /** The section of an open item (action / working / fyi / noise); null from an older hub (then the tier decides). */
    val status: String? = null,
    val urgent: Boolean = false,
    val recap: String? = null,
    val handling: InboxHandling? = null,
    val suggestion: InboxSuggestion? = null,
    val chain: List<InboxChainEntry> = emptyList(),
    val related: List<InboxRelated> = emptyList(),
    /** Who filed a done item (`done_by`: user, replied, dismissed, manager…); an existing field, read here for the event page. */
    val doneBy: String? = null,
    val where: String? = null,
    val follow: InboxFollow? = null,
    /** A hub state key; unknown keys stay intact so the UI can keep its own state words. */
    val now: String? = null,
) {
    /** The hub sent any of them: the item opens as an event (Summary, Recap, Suggestion, Handling chain) rather than straight into its source. */
    val present: Boolean get() = status != null || recap != null || handling != null || suggestion != null || chain.isNotEmpty()

    companion object {
        val None = InboxEventFields()
    }
}

private inline fun <reified T> JsonElement?.decoded(): T? =
    this?.let { runCatching { JsonCodec.decodeFromJsonElement<T>(it) }.getOrNull() }

/**
 * The event fields of an inbox item's JSON. Each is read on its own and a malformed one is left out, so is a malformed
 * chain entry (the others stay); an unknown status reads as none (the tier decides), while unknown chain kinds and now
 * state keys are kept.
 */
fun inboxEventFields(item: JsonObject): InboxEventFields = InboxEventFields(
    status = (item["status"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.takeIf { it in InboxStatuses },
    urgent = (item["urgent"] as? JsonPrimitive)?.takeIf { !it.isString }?.content == "true",
    recap = (item["recap"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() },
    handling = item["handling"].decoded<InboxHandling>(),
    suggestion = item["suggestion"].decoded<InboxSuggestion>()?.takeIf { it.text.isNotBlank() || it.kind == "conclusion" },
    chain = (item["chain"] as? JsonArray).orEmpty().mapNotNull { it.decoded<InboxChainEntry>() },
    related = (item["related"] as? JsonArray).orEmpty().mapNotNull { it.decoded<InboxRelated>() }.filter { it.id.isNotBlank() },
    doneBy = (item["done_by"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() },
    where = (item["where"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() },
    follow = item["follow"].decoded<InboxFollow>(),
    now = (item["now"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.takeIf { it.isNotBlank() },
)

/** The sections an open item can be in (`status`). */
val InboxStatuses = setOf("action", "working", "fyi", "noise")

object InboxHandlingSerializer : WireSerializer<InboxHandling>(InboxHandling.generatedSerializer())
object InboxSuggestionSerializer : WireSerializer<InboxSuggestion>(InboxSuggestion.generatedSerializer())
object InboxFollowSerializer : WireSerializer<InboxFollow>(InboxFollow.generatedSerializer())
object InboxChainMessageSerializer : WireSerializer<InboxChainMessage>(InboxChainMessage.generatedSerializer())
object InboxChainLinkSerializer : WireSerializer<InboxChainLink>(InboxChainLink.generatedSerializer())
object InboxChainEntrySerializer : WireSerializer<InboxChainEntry>(InboxChainEntry.generatedSerializer())
object InboxRelatedSerializer : WireSerializer<InboxRelated>(InboxRelated.generatedSerializer())
