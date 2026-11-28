package persona.core.contracts.api

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.HubScope
import persona.core.contracts.Session
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.KSerializer
import kotlinx.serialization.Transient
import kotlinx.serialization.descriptors.PrimitiveKind
import kotlinx.serialization.descriptors.PrimitiveSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonObject

/** The kinds of the knowledge base, in the order the list offers them (docs/assistant/knowledge.md). */
@Serializable
enum class KnowledgeKind(val title: CoreText) {
    @SerialName("projects") Projects(CoreText.of(Key.KnowledgeProjects)),
    @SerialName("chats") Chats(CoreText.of(Key.KnowledgeChats)),
    @SerialName("people") People(CoreText.of(Key.KnowledgePeople)),
    @SerialName("docs") Docs(CoreText.of(Key.KnowledgeDocs)),
    @SerialName("meetings") Meetings(CoreText.of(Key.KnowledgeMeetings)),
    @SerialName("events") Events(CoreText.of(Key.KnowledgeEvents)),
    @SerialName("recordings") Recordings(CoreText.of(Key.KnowledgeRecordings)),
    @SerialName("topics") Topics(CoreText.of(Key.KnowledgeTopics));

    val wire: String get() = name.lowercase()
    companion object {
        fun fromWire(value: String): KnowledgeKind? = entries.firstOrNull { it.wire == value }
    }
}

@Serializable
data class KnowledgeEntry(
    val kind: String,
    val id: String,
    val name: String,
    @SerialName("one_line") val oneLine: String? = null,
    val importance: String? = null,
    val relevance: Int = 0,
    val tags: List<String>? = null,
    val updated: String? = null,
    val extra: String? = null,
    /** A document's, meeting's or event's connector; null on other kinds and from an older hub. */
    val source: String? = null,
    /** A recording's imported meeting, as the meetings list has its id; null before a match and from an older hub. */
    @SerialName("meeting_id") val meetingId: String? = null,
    /** A recording whose in-flight state nothing can settle, and why ([RecordingPaused]); null when it is not paused. */
    val paused: RecordingPaused? = null,
    /** Client-only: the imported meeting this recording stands for in the merged Meetings list. */
    @Transient val imported: KnowledgeEntry? = null,
    /** A project's parent as it shows (#12927); null at the top level and from an older hub. Read only on Android. */
    val parent: String? = null,
    /** A project's milestone flags (#12927); null without any and from an older hub. Read only on Android. */
    val milestones: List<KnowledgeMilestone>? = null,
) {
    val key: String get() = "$kind/$id"
}

@Serializable
data class KnowledgeList(val items: List<KnowledgeEntry> = emptyList(), val tags: List<String>? = null)

@Serializable
data class KnowledgeDetail(
    val kind: String,
    val id: String,
    val path: String,
    val meta: JsonObject? = null,
    val summary: String? = null,
    val log: List<String>? = null,
    val recent: String? = null,
    val links: List<KnowledgeEntry>? = null,
    /** A recording's transcript (recordings only); the same text as [recent], kept apart line by line. */
    val segments: List<KnowledgeSegment>? = null,
    /** A recording's meeting minutes, once the hub wrote them (#2738, docs/assistant/meeting-minutes.md); `meta.minutes_state` says where newer ones stand. */
    val minutes: RecordingMinutes? = null,
    /** A topic's knowledge points (topics only, docs/assistant/knowledge.md "Topics"). */
    val points: List<TopicPoint>? = null,
    /** A recording whose in-flight state nothing can settle, and why ([RecordingPaused]); null when it is not paused. */
    val paused: RecordingPaused? = null,
)

/**
 * Why a recording's in-flight state (an upload not complete, minutes or a day recap pending, a transcription on the
 * hub) is not moving: the hub's `paused` on a recording's list entry and detail, derived when read and never stored
 * (#13407). A closed set: a reason this app does not know reads as [Unknown], never as one it does.
 */
@Serializable(with = RecordingPausedSerializer::class)
enum class RecordingPaused(val wire: String) {
    /** `module_off`: the recording module is off on this hub, so nothing takes the recording up until it is back. */
    ModuleOff("module_off"),
    /** A reason a newer hub names; the page says nothing it cannot stand behind and behaves as before. */
    Unknown("unknown");

    companion object {
        fun fromWire(value: String): RecordingPaused = entries.firstOrNull { it != Unknown && it.wire == value } ?: Unknown
    }
}

object RecordingPausedSerializer : KSerializer<RecordingPaused> {
    override val descriptor = PrimitiveSerialDescriptor("persona.core.contracts.api.RecordingPaused", PrimitiveKind.STRING)
    override fun deserialize(decoder: Decoder): RecordingPaused = RecordingPaused.fromWire(decoder.decodeString())
    override fun serialize(encoder: Encoder, value: RecordingPaused) = encoder.encodeString(value.wire)
}

/** A source a knowledge point was said in; [day] is YYYY-MM-DD. */
@Serializable
data class TopicSource(val kind: String = "", val id: String = "", val day: String? = null)

/**
 * One knowledge point of a topic. [path] is the subtopics below the topic, outermost first (0 to 2); [kind] is concept,
 * decision, practice, pitfall, fact or question, and the hub may add more; [why] is a decision's reason or a pitfall's
 * cause; [confirmed] is the day it was last confirmed (YYYY-MM-DD).
 */
@Serializable
data class TopicPoint(
    val id: String = "",
    val path: List<String>? = null,
    val kind: String = "",
    val text: String = "",
    val why: String? = null,
    val sources: List<TopicSource>? = null,
    @SerialName("when") val confirmed: String? = null,
)

/**
 * A recording's minutes as the hub wrote them (docs/assistant/meeting-minutes.md "Field contract"). Every item's [MinutesItem.at]
 * is seconds from the recording's start, the clock of [KnowledgeSegment.start]. Sections with nothing in them are empty.
 */
@Serializable
data class RecordingMinutes(
    /** Increases each time they are rewritten; sent back when confirming to-dos. */
    val version: Int = 0,
    /** The recording revision they were made from: below `meta.revision` while newer ones are being written. */
    val revision: Int = 0,
    val title: String? = null,
    /** "2026-11-10 14:00–15:10", in the user's time zone: shown only when [start] / [end] are missing (an older hub). */
    @SerialName("when") val time: String? = null,
    /** RFC 3339 with the user's offset (#4713); the app formats them itself. */
    val start: String? = null,
    val end: String? = null,
    val where: String? = null,
    val attendees: List<String> = emptyList(),
    val points: List<MinutesItem> = emptyList(),
    val decisions: List<MinutesItem> = emptyList(),
    val actions: List<MinutesAction> = emptyList(),
    val open: List<MinutesItem> = emptyList(),
    /** The transcript was too long and its end wasn't read. */
    val partial: Boolean = false,
    /** The same minutes as Markdown: what Copy sends. */
    val markdown: String = "",
) {
    val isEmpty: Boolean get() = points.isEmpty() && decisions.isEmpty() && actions.isEmpty() && open.isEmpty()
}

/** A key point, decision or open question; [who] only when the transcript or the calendar supports it. */
@Serializable
data class MinutesItem(val text: String = "", val at: Double? = null, val who: String? = null)

/** An action item: a suggested to-do until the user confirms it, then [todoId]. [by] is the deadline as spoken; [dueDate] only when it resolves to one date. */
@Serializable
data class MinutesAction(
    val id: String = "",
    val who: String? = null,
    val what: String = "",
    val by: String? = null,
    @SerialName("due_date") val dueDate: String? = null,
    val at: Double? = null,
    @SerialName("todo_id") val todoId: String? = null,
) {
    val added: Boolean get() = !todoId.isNullOrBlank()
}

/**
 * One line of a recording's transcript: [start] and [end] are seconds from the recording's `started_at`. [echo]: the line
 * is Persona's own answer as the meeting's microphone heard it (#5905, docs/assistant/meeting-minutes.md "Persona's echo"),
 * kept on the hub but not what the meeting said.
 */
@Serializable
data class KnowledgeSegment(val start: Double = 0.0, val end: Double = 0.0, val text: String = "", val echo: Boolean = false)

/** [agents]: the live, non-assistant sessions working in a project's code directories (projects only). */
@Serializable
data class KnowledgeDetailResponse(
    val detail: KnowledgeDetail,
    val agents: List<Session>? = null,
    /** A project's place in the project tree (#12927); null from an older hub. Read only on Android. */
    val tree: KnowledgeProjectTree? = null,
)

/** A milestone flag on a project (#12927); every field optional so a later field or a damaged flag never fails a list. */
@Serializable
data class KnowledgeMilestone(val id: String? = null, val title: String? = null, val target: String? = null, val state: String? = null)

/** A project as the tree names it: a parent, one above it, one under it. */
@Serializable
data class KnowledgeProjectRef(val id: String = "", val name: String? = null, val status: String? = null, val children: Int? = null)

/** `tree` of a project's detail (`GET /api/v1/kb/projects/{id}`, #12927). */
@Serializable
data class KnowledgeProjectTree(
    val parent: KnowledgeProjectRef? = null,
    val ancestors: List<KnowledgeProjectRef>? = null,
    val children: List<KnowledgeProjectRef>? = null,
    val milestones: List<KnowledgeMilestone>? = null,
    val depth: Int? = null,
)

/**
 * Tagging set by hand (`PUT /api/v1/kb/{kind}/{id}`); the hub keeps it over its automatic tagging. Only the fields
 * that are set go on the wire; an empty [tags] list is sent, and clears the tags.
 */
@Serializable
data class KnowledgeUpdate(
    val importance: String? = null,
    val relevance: Int? = null,
    val tags: List<String>? = null,
    val status: String? = null,
    @SerialName("project_kind") val projectKind: String? = null,
)

@Serializable
data class KnowledgeHit(val path: String, val line: String)

@Serializable
data class KnowledgeSearch(val hits: List<KnowledgeHit> = emptyList())

/** One meeting hand-off as the hub keeps it, in `detail.meta.handoffs` and the write's answer (#8852). */
@Serializable
data class MeetingHandoff(
    val id: String,
    val at: String,
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val title: String? = null,
    val transcript: Boolean? = null,
    /** `sent`, or `failed` with [problem]. */
    val state: String,
    val problem: String? = null,
)

/** The owner's choices for `POST /api/v1/recordings/{id}/handoff`; text and transcript are always on the wire. */
@Serializable
data class MeetingHandoffRequest(
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    val title: String,
    val text: String,
    val transcript: Boolean,
)

/** An immutable repository belongs to exactly one pairing generation. */
interface KnowledgeRepository {
    val scope: HubScope
    suspend fun list(kind: KnowledgeKind, tag: String? = null): KnowledgeList
    suspend fun detail(kind: KnowledgeKind, id: String): KnowledgeDetail
    suspend fun search(query: String): List<KnowledgeHit>

    /** The detail together with the Agents working on it; repositories without them answer the detail alone. */
    suspend fun entry(kind: KnowledgeKind, id: String): KnowledgeDetailResponse = KnowledgeDetailResponse(detail(kind, id))

    /** Saves hand-made tagging; see [KnowledgeUpdate]. */
    suspend fun update(kind: KnowledgeKind, id: String, update: KnowledgeUpdate): Unit =
        throw UnsupportedOperationException("Knowledge tagging unavailable")

    /** Asks for a recording's minutes, new or again (`POST /api/v1/recordings/{id}/minutes`, #2738): `409` while its transcript isn't complete. */
    suspend fun makeMinutes(id: String): Unit = throw UnsupportedOperationException("Minutes unavailable")

    /**
     * Makes the minutes' [actions] (their ids) to-dos (`POST /api/v1/recordings/{id}/minutes/todos`): [version] is the minutes'
     * as shown; `409 minutes_stale` when they were rewritten since (read them again first).
     */
    suspend fun addMinutesTodos(id: String, version: Int, actions: List<String>): Unit = throw UnsupportedOperationException("Minutes unavailable")

    /** Hands a finished recording to an Agent; the answer is the try the hub kept, sent or failed. */
    suspend fun handOff(id: String, request: MeetingHandoffRequest): MeetingHandoff =
        throw UnsupportedOperationException("Meeting handoff unavailable")
}

/** The composition root clears this before disposing a pairing's HTTP client. */
interface KnowledgeConnection {
    val current: StateFlow<KnowledgeRepository?>
}
