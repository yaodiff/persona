@file:OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)

package persona.core.contracts

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.json.JsonElement

@KeepGeneratedSerializer
@Serializable(with = MachineSystemSerializer::class)
data class MachineSystem(
    val os: String = "", val distro: String = "", val like: String = "", val name: String = "",
    val version: String = "", val build: String = "", val kernel: String = "", val arch: String = "",
    val model: String = "", val cpu: String = "", val cores: Int = 0, val memory: Long = 0,
    @WireTime(unixSeconds = true) @SerialName("boot_time") val bootTime: Double? = null,
)

@KeepGeneratedSerializer
@Serializable(with = SubagentSerializer::class)
data class Subagent(
    val id: String,
    @SerialName("session_id") val sessionId: String = "",
    val type: String = "", val description: String? = null,
    @SerialName("tool_use_id") val toolUseId: String? = null,
    val running: Boolean = false, val background: Boolean = false,
    @WireTime @SerialName("started_at") val startedAt: String? = null,
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("last_text") val lastText: String? = null,
    /**
     * How many tool calls it made and its latest one, the model it ran on (its own transcript's, never the one its call
     * asked for) and its latest reply's tokens; the subagent that launched it ([parentId], absent when the session did).
     * Each absent from older hubs (docs/protocol/protocol.md, a session's `subagents`).
     */
    val steps: Int? = null,
    @SerialName("last_tool") val lastTool: WorkflowTool? = null,
    val model: String? = null,
    val tokens: Long? = null,
    @SerialName("parent_id") val parentId: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = TaskNoteSerializer::class)
data class TaskNote(
    @WireEnum("shell", "monitor", "agent", "task", "unknown") val kind: String = "unknown",
    @WireEnum("running", "completed", "failed", "killed", "stopped", "event", "ended", "waiting", "unknown") val status: String = "unknown",
    val title: String = "",
    @SerialName("task_id") val taskId: String? = null,
    @SerialName("tool_use_id") val toolUseId: String? = null,
    @SerialName("exit_code") val exitCode: Int? = null,
    @SerialName("output_file") val outputFile: String? = null,
    val detail: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = FoldSerializer::class)
data class Fold(
    val items: Int = 0, val tools: Int = 0, val thoughts: Int = 0,
    @WireTime val until: String? = null,
    @SerialName("through_seq") val throughSeq: Int = 0,
    @SerialName("through_id") val throughId: String = "",
    val tasks: List<FoldTaskSnapshot> = emptyList(),
    /** The folded run belongs to a supervision round the user must see (`fold_supervision.go`); older hubs omit it. */
    @SerialName("supervision_user") val supervisionUser: Boolean? = null,
    /** The images this run of work looked at (docs/protocol/protocol.md, "Images viewed this turn"); older hubs omit it. */
    val images: List<FoldImage> = emptyList(),
)

/**
 * An image a folded run looked at (`fold.images`, docs/protocol/protocol.md "Images viewed this turn"): its image item
 * ([itemId], absent for a picture file Read opened whose result kept no image), the call and tool, where it came from
 * (`read`, `screenshot`, `browser`, `mcp`, `tool`), the file Read read, the machine a screenshot named, and its shape.
 */
@Serializable
data class FoldImage(
    @SerialName("item_id") val itemId: String? = null,
    @SerialName("tool_use_id") val toolUseId: String = "",
    val tool: String = "",
    val source: String = "tool",
    val path: String? = null,
    val machine: String? = null,
    val mime: String? = null,
    val size: Int? = null,
    val width: Int? = null,
    val height: Int? = null,
)

@Serializable
data class FoldTaskSnapshot(
    val replace: String? = null,
    @SerialName("fallback_status") val fallbackStatus: String? = null,
    val entries: List<FoldTask> = emptyList(),
    @SerialName("source_item_id") val sourceItemId: String,
    @SerialName("source_tool_use_id") val sourceToolUseId: String,
    @SerialName("source_seq") val sourceSeq: Int,
)

@Serializable
data class FoldTask(
    val id: String,
    val subject: String? = null,
    val description: String? = null,
    val status: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = ItemsResponseSerializer::class)
data class ItemsResponse(
    val items: List<Item> = emptyList(),
    @SerialName("has_more") val hasMore: Boolean = false,
    val history: String? = null,
    @SerialName("from_seq") val fromSeq: Int? = null,
    @SerialName("to_seq") val toSeq: Int? = null,
    @SerialName("more_after") val moreAfter: Boolean = false,
    val more: Boolean = false,
)

@KeepGeneratedSerializer
@Serializable(with = SessionsResponseSerializer::class)
data class SessionsResponse(val sessions: List<Session> = emptyList())
@KeepGeneratedSerializer
@Serializable(with = MachinesResponseSerializer::class)
data class MachinesResponse(val machines: List<Machine> = emptyList())
@KeepGeneratedSerializer
@Serializable(with = InteractionsResponseSerializer::class)
data class InteractionsResponse(val interactions: List<Interaction> = emptyList())
@KeepGeneratedSerializer
@Serializable(with = FleetResponseSerializer::class)
data class FleetResponse(val fleet: FleetState)

/** Fleet deliberately uses camelCase, unlike the other hub models. */
@KeepGeneratedSerializer
@Serializable(with = FleetStateSerializer::class)
data class FleetState(
    @WireEnum("needsYou", "working", "done", "", "unknown") val phase: String = "",
    val needsYou: Int = 0, val working: Int = 0, val unread: Int = 0,
    val items: List<FleetItem> = emptyList(),
    @WireTime(unixSeconds = true) val workingSince: Double? = null,
    val details: Boolean = false, val assistant: Boolean = false,
    @WireEnum("status", "tpm", "tokens", "requests", "assistant", "load", "timer", "none", "unknown") val left: String = "status",
    @WireEnum("status", "tpm", "tokens", "requests", "assistant", "load", "timer", "none", "unknown") val right: String = "status",
    val stats: FleetStats? = null,
)

@KeepGeneratedSerializer
@Serializable(with = FleetItemSerializer::class)
data class FleetItem(
    val id: String, val title: String = "", val place: String = "",
    @WireEnum("permission", "plan", "question", "screen", "working", "unread", "unknown") val kind: String = "unknown",
    val detail: String = "", val interaction: String? = null,
    @WireTime(unixSeconds = true) val since: Double? = null,
)

@KeepGeneratedSerializer
@Serializable(with = FleetStatsSerializer::class)
data class FleetStats(val tpm: Long = 0, val tokens: Long = 0, val requests: Long = 0, val loads: List<FleetLoad> = emptyList())
@KeepGeneratedSerializer
@Serializable(with = FleetLoadSerializer::class)
data class FleetLoad(val id: String, val name: String = "", val percent: Int = 0)

@KeepGeneratedSerializer
@Serializable(with = InboxResponseSerializer::class)
data class InboxResponse(val items: List<InboxItem> = emptyList())
@KeepGeneratedSerializer
@Serializable(with = InboxItemSerializer::class)
data class InboxItem(
    val id: String,
    @WireEnum("lark", "mail", "unknown") val source: String = "unknown",
    @WireEnum("dm", "mention", "group", "bot", "mail", "moment", "unknown") val kind: String = "unknown",
    val account: String = "",
    @SerialName("account_name") val accountName: String = "",
    val title: String = "", val sender: String? = null, val avatar: String? = null,
    val preview: String = "", val count: Int = 0,
    @WireTime @SerialName("started_at") val startedAt: String? = null,
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    @WireEnum("action", "fyi", "noise", "unknown") val tier: String? = null,
    val summary: String? = null, val reason: String? = null, val read: Boolean? = null,
    @WireTime @SerialName("done_at") val doneAt: String? = null,
    @SerialName("done_by") val doneBy: String? = null,
    val lark: InboxLark? = null, val mail: InboxMail? = null, val moment: InboxMoment? = null,
    val agent: InboxAgent? = null,
    val context: String? = null,
    // Inbox phase 1 (InboxEvents.kt); the app reads them with inboxEventFields, which keeps the good entries of a chain.
    @WireEnum("action", "working", "fyi", "noise", "unknown") val status: String? = null,
    val urgent: Boolean? = null,
    val recap: String? = null,
    val handling: InboxHandling? = null,
    val suggestion: InboxSuggestion? = null,
    val chain: List<InboxChainEntry> = emptyList(),
    val related: List<InboxRelated> = emptyList(),
    val where: String? = null,
    val follow: InboxFollow? = null,
    val now: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = InboxMomentSerializer::class)
data class InboxMoment(
    @SerialName("post_id") val postId: String,
    val notice: String? = null,
    @SerialName("reply_to") val replyTo: String? = null,
    @SerialName("my_post") val myPost: Boolean? = null,
    @SerialName("post_text") val postText: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = InboxLarkSerializer::class)
data class InboxLark(
    @SerialName("feed_id") val feedId: String,
    @SerialName("is_group") val isGroup: Boolean = false,
    @SerialName("is_topic") val isTopic: Boolean? = null,
    @SerialName("start_position") val startPosition: String? = null,
    @SerialName("mention_position") val mentionPosition: String? = null,
    @SerialName("at_all") val atAll: Boolean? = null,
)

@KeepGeneratedSerializer
@Serializable(with = InboxMailSerializer::class)
data class InboxMail(
    @SerialName("account_id") val accountId: String,
    val mailbox: String = "", val uids: List<Long> = emptyList(),
    @SerialName("message_id") val messageId: String? = null,
    val from: MailAddress? = null, val direct: Boolean? = null, val thread: String? = null,
)
@KeepGeneratedSerializer
@Serializable(with = MailAddressSerializer::class)
data class MailAddress(val name: String = "", val email: String = "")

@KeepGeneratedSerializer
@Serializable(with = InboxAgentSerializer::class)
data class InboxAgent(
    @WireEnum("skipped", "queued", "running", "drafted", "failed", "unknown") val state: String = "unknown",
    val task: String? = null, val reason: String? = null,
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    val draft: String? = null, val note: String? = null, val error: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkMessagesSerializer::class)
data class LarkMessages(
    val messages: List<LarkMessage> = emptyList(),
    @SerialName("has_older") val hasOlder: Boolean = false,
    @SerialName("has_newer") val hasNewer: Boolean = false,
    @SerialName("older_cursor") val olderCursor: String? = null,
    @SerialName("newer_cursor") val newerCursor: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkMessageSerializer::class)
data class LarkMessage(
    val id: String, val position: String = "",
    @SerialName("sender_id") val senderId: String? = null,
    @SerialName("sender_name") val senderName: String = "",
    @SerialName("sender_avatar") val senderAvatar: String? = null,
    @SerialName("sender_type") val senderType: String? = null,
    @SerialName("is_mine") val isMine: Boolean = false,
    val type: String = "", val text: String = "",
    @WireTime @SerialName("created_at") val createdAt: String? = null,
    val recalled: Boolean = false, val edited: Boolean? = null,
    @SerialName("reply_count") val replyCount: Int = 0,
    @SerialName("thread_reply_count") val threadReplyCount: Int? = null,
    @SerialName("badge_count") val badgeCount: Int? = null,
    @SerialName("at_me") val atMe: Boolean? = null,
    @SerialName("is_read") val isRead: Boolean? = null,
    val parts: List<LarkPart>? = null,
    @SerialName("reply_to") val replyTo: LarkReplyTo? = null,
    val reactions: List<LarkReaction>? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkPartSerializer::class)
data class LarkPart(
    val kind: String, val text: String? = null, val url: String? = null,
    val refId: String? = null, val metadata: JsonElement? = null,
)
@KeepGeneratedSerializer
@Serializable(with = LarkReplyToSerializer::class)
data class LarkReplyTo(
    val id: String, @SerialName("sender_name") val senderName: String = "", val text: String = "",
    val recalled: Boolean? = null, val parts: List<LarkPart>? = null,
)
@KeepGeneratedSerializer
@Serializable(with = LarkReactionSerializer::class)
data class LarkReaction(val key: String, val count: Int = 0, val mine: Boolean? = null, val users: List<String>? = null)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsPostSerializer::class)
data class LarkMomentsPost(
    val id: String = "",
    val userName: String? = null,
    val userId: String? = null,
    val userAvatarUrl: String? = null,
    val content: LarkMomentsContent? = null,
    val images: List<LarkMomentsImage>? = null,
    val reactionGroups: List<LarkMomentsReactionGroup>? = null,
    val reactionCount: Int? = null,
    val commentCount: Int? = null,
    val createTime: Double? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsImageSerializer::class)
data class LarkMomentsImage(val url: String? = null, val width: Double? = null, val height: Double? = null)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsReactionGroupSerializer::class)
data class LarkMomentsReactionGroup(val type: String = "", val count: Int = 0, val selfInvolved: Boolean = false)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsContentSerializer::class)
data class LarkMomentsContent(val text: String? = null, val segments: List<LarkMomentsSegment>? = null)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsSegmentSerializer::class)
data class LarkMomentsSegment(val kind: String? = null, val text: String? = null, val key: String? = null, val emojiKey: String? = null)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsCommentSerializer::class)
data class LarkMomentsComment(
    val id: String = "",
    val userName: String? = null,
    val userId: String? = null,
    val userAvatarUrl: String? = null,
    val content: LarkMomentsContent? = null,
    val createTime: Double? = null,
    val replyCommentId: String? = null,
    val replyUserName: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkMomentsCommentsResponseSerializer::class)
data class LarkMomentsCommentsResponse(val comments: List<LarkMomentsComment> = emptyList())

@KeepGeneratedSerializer
@Serializable(with = LarkUrlPreviewSerializer::class)
data class LarkUrlPreview(
    val title: String? = null,
    val host: String? = null,
    val url: String? = null,
    val card: LarkUrlPreviewCard? = null,
)

@KeepGeneratedSerializer
@Serializable(with = LarkUrlPreviewCardSerializer::class)
data class LarkUrlPreviewCard(
    val author: String? = null,
    val avatarUrl: String? = null,
    val body: String? = null,
    val footer: String? = null,
    val subtitle: String? = null,
)

@KeepGeneratedSerializer
@Serializable(with = EndpointSerializer::class)
data class Endpoint(
    val url: String,
    @WireEnum("ipv6", "name", "lan", "ipv4", "unknown") val kind: String = "unknown",
    val port: Int? = null,
    @WireEnum("https", "http", "unknown") val scheme: String? = null,
    @WireTime @SerialName("seen_at") val seenAt: String? = null,
)
@KeepGeneratedSerializer
@Serializable(with = EndpointsResponseSerializer::class)
data class EndpointsResponse(
    @SerialName("hub_id") val hubId: String? = null,
    @SerialName("tls_port") val tlsPort: Int? = null,
    val fp: String? = null, val endpoints: List<Endpoint> = emptyList(),
)
@KeepGeneratedSerializer
@Serializable(with = HealthSerializer::class)
data class Health(
    val ok: Boolean,
    val version: String? = null, val features: List<String> = emptyList(),
    @SerialName("hub_id") val hubId: String? = null,
    @SerialName("tls_port") val tlsPort: Int? = null,
    val fp: String? = null,
    @SerialName("hub_name") val hubName: String? = null,
)
@KeepGeneratedSerializer
@Serializable(with = HeartbeatSerializer::class)
data class Heartbeat(val interval: Int, @SerialName("pong_timeout") val pongTimeout: Int)

/** Keep refusal details flat and preserve absent delivery/retry information as null. */
@KeepGeneratedSerializer
@Serializable(with = ErrorBodySerializer::class)
data class ErrorBody(
    val error: String,
    val code: String? = null, val retryable: Boolean? = null,
    @WireEnum("none", "unknown", "yes") val delivered: String? = null,
    val draft: String? = null, val clearable: String? = null,
    /** The input the hub refused (an extension setting's dotted path, `invalid_config`) and why, without the path. */
    val field: String? = null, val message: String? = null,
    /** What the node said when the hub put [error] in the app's language (#5514); a structured detail reads as absent. */
    val detail: String? = null,
)

object MachineSystemSerializer : WireSerializer<MachineSystem>(MachineSystem.generatedSerializer())
object SubagentSerializer : WireSerializer<Subagent>(Subagent.generatedSerializer())
object TaskNoteSerializer : WireSerializer<TaskNote>(TaskNote.generatedSerializer())
object FoldSerializer : WireSerializer<Fold>(Fold.generatedSerializer())
object ItemsResponseSerializer : WireSerializer<ItemsResponse>(ItemsResponse.generatedSerializer())
object SessionsResponseSerializer : WireSerializer<SessionsResponse>(SessionsResponse.generatedSerializer())
object MachinesResponseSerializer : WireSerializer<MachinesResponse>(MachinesResponse.generatedSerializer())
object InteractionsResponseSerializer : WireSerializer<InteractionsResponse>(InteractionsResponse.generatedSerializer())
object FleetResponseSerializer : WireSerializer<FleetResponse>(FleetResponse.generatedSerializer())
object FleetStateSerializer : WireSerializer<FleetState>(FleetState.generatedSerializer())
object FleetItemSerializer : WireSerializer<FleetItem>(FleetItem.generatedSerializer())
object FleetStatsSerializer : WireSerializer<FleetStats>(FleetStats.generatedSerializer())
object FleetLoadSerializer : WireSerializer<FleetLoad>(FleetLoad.generatedSerializer())
object InboxResponseSerializer : WireSerializer<InboxResponse>(InboxResponse.generatedSerializer())
object InboxItemSerializer : WireSerializer<InboxItem>(InboxItem.generatedSerializer())
object InboxMomentSerializer : WireSerializer<InboxMoment>(InboxMoment.generatedSerializer())
object InboxLarkSerializer : WireSerializer<InboxLark>(InboxLark.generatedSerializer())
object InboxMailSerializer : WireSerializer<InboxMail>(InboxMail.generatedSerializer())
object MailAddressSerializer : WireSerializer<MailAddress>(MailAddress.generatedSerializer())
object InboxAgentSerializer : WireSerializer<InboxAgent>(InboxAgent.generatedSerializer())
object LarkMessagesSerializer : WireSerializer<LarkMessages>(LarkMessages.generatedSerializer())
object LarkMessageSerializer : WireSerializer<LarkMessage>(LarkMessage.generatedSerializer())
object LarkPartSerializer : WireSerializer<LarkPart>(LarkPart.generatedSerializer())
object LarkReplyToSerializer : WireSerializer<LarkReplyTo>(LarkReplyTo.generatedSerializer())
object LarkReactionSerializer : WireSerializer<LarkReaction>(LarkReaction.generatedSerializer())
object LarkMomentsPostSerializer : WireSerializer<LarkMomentsPost>(LarkMomentsPost.generatedSerializer())
object LarkMomentsImageSerializer : WireSerializer<LarkMomentsImage>(LarkMomentsImage.generatedSerializer())
object LarkMomentsReactionGroupSerializer : WireSerializer<LarkMomentsReactionGroup>(LarkMomentsReactionGroup.generatedSerializer())
object LarkMomentsContentSerializer : WireSerializer<LarkMomentsContent>(LarkMomentsContent.generatedSerializer())
object LarkMomentsSegmentSerializer : WireSerializer<LarkMomentsSegment>(LarkMomentsSegment.generatedSerializer())
object LarkMomentsCommentSerializer : WireSerializer<LarkMomentsComment>(LarkMomentsComment.generatedSerializer())
object LarkMomentsCommentsResponseSerializer : WireSerializer<LarkMomentsCommentsResponse>(LarkMomentsCommentsResponse.generatedSerializer())
object LarkUrlPreviewSerializer : WireSerializer<LarkUrlPreview>(LarkUrlPreview.generatedSerializer())
object LarkUrlPreviewCardSerializer : WireSerializer<LarkUrlPreviewCard>(LarkUrlPreviewCard.generatedSerializer())
object EndpointSerializer : WireSerializer<Endpoint>(Endpoint.generatedSerializer())
object EndpointsResponseSerializer : WireSerializer<EndpointsResponse>(EndpointsResponse.generatedSerializer())
object HealthSerializer : WireSerializer<Health>(Health.generatedSerializer())
object HeartbeatSerializer : WireSerializer<Heartbeat>(Heartbeat.generatedSerializer())
object ErrorBodySerializer : WireSerializer<ErrorBody>(ErrorBody.generatedSerializer())

/** Where a [Workflow] run stands; a status this app does not know reads as [Completed] (docs/protocol/protocol.md). */
enum class WorkflowStatus { Running, Completed, Failed, Killed, Stalled }

/**
 * A Claude Code Workflow run (the Workflow tool, `proto.Workflow`): a script driving many agents in the background.
 * [agentsStarted] is not the total, which is only known once it ends: progress reads done / started. [endedAt] is
 * absent while it runs.
 */
@KeepGeneratedSerializer
@Serializable(with = WorkflowSerializer::class)
data class Workflow(
    @SerialName("task_id") val taskId: String = "",
    @SerialName("run_id") val runId: String? = null,
    val name: String? = null,
    val summary: String? = null,
    @WireEnum("running", "completed", "failed", "killed", "stalled", "unknown") val status: String = "unknown",
    val running: Boolean = false,
    val phases: List<String>? = null,
    val phase: String? = null,
    /** 1-based place of [phase] in [phases]; 0 or absent: not known. */
    @SerialName("phase_index") val phaseIndex: Int? = null,
    @SerialName("phase_count") val phaseCount: Int? = null,
    /** The Workflow call that launched it: the chat draws the run's card in that call's place (#3435). */
    @SerialName("tool_use_id") val toolUseId: String? = null,
    /**
     * Per-phase counts over all its agents, not only the listed ones, in [phases] order then undeclared phases ("" =
     * not known). Older hubs send none.
     */
    @SerialName("phase_stats") val phaseStats: List<WorkflowPhaseStats>? = null,
    @SerialName("agents_started") val agentsStarted: Int = 0,
    @SerialName("agents_done") val agentsDone: Int = 0,
    /** Agents the journal says failed, apart from [agentsDone] (older hubs counted them as done). */
    @SerialName("agents_failed") val agentsFailed: Int = 0,
    @SerialName("agents_running") val agentsRunning: Int = 0,
    /** All its agents' latest usage summed, not cumulative consumption. */
    val tokens: Long? = null,
    /** All its agents' tool calls. */
    val calls: Int? = null,
    @WireTime @SerialName("started_at") val startedAt: String? = null,
    @WireTime @SerialName("ended_at") val endedAt: String? = null,
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    /** A failed or killed run's error, its first line. */
    val error: String? = null,
    /** Its agents in the order they started (the hub keeps the latest 60). */
    val agents: List<WorkflowAgent>? = null,
) {
    val state: WorkflowStatus get() = when (status) {
        "running" -> WorkflowStatus.Running
        "failed" -> WorkflowStatus.Failed
        "killed" -> WorkflowStatus.Killed
        "stalled" -> WorkflowStatus.Stalled
        else -> WorkflowStatus.Completed
    }
    val isRunning: Boolean get() = state == WorkflowStatus.Running
    /** Its script's name, else its summary; null when it has neither. */
    val title: String? get() = name?.trim()?.takeIf(String::isNotEmpty) ?: summary?.trim()?.takeIf(String::isNotEmpty)
    /** [phaseIndex] when it points into [phases]. */
    val currentPhase: Int? get() = phaseIndex?.takeIf { it in 1..phases.orEmpty().size }
}

/** One phase of a [Workflow] counted over all its agents; [title] "" holds the agents whose phase is not known. */
@KeepGeneratedSerializer
@Serializable(with = WorkflowPhaseStatsSerializer::class)
data class WorkflowPhaseStats(
    val title: String = "",
    val started: Int = 0,
    val done: Int = 0,
    /** Absent from hubs before #5511, which counted failed agents as done. */
    val failed: Int = 0,
    val running: Int = 0,
)

/** One agent of a [Workflow]; its transcript reads like a subagent's, as the session [sessionId] (`<parent>@<id>`). */
@KeepGeneratedSerializer
@Serializable(with = WorkflowAgentSerializer::class)
data class WorkflowAgent(
    val id: String = "",
    @SerialName("session_id") val sessionId: String = "",
    val label: String? = null,
    val phase: String? = null,
    val running: Boolean = false,
    /**
     * "running", "done", "failed" (the journal says it failed) or "stopped" (it had not come back when its run ended);
     * absent from older hubs.
     */
    @WireEnum("running", "done", "failed", "stopped", "unknown") val status: String? = null,
    @WireTime @SerialName("started_at") val startedAt: String? = null,
    /** Absent while it runs: its last exchange, or its run's end when it was stopped. */
    @WireTime @SerialName("ended_at") val endedAt: String? = null,
    @WireTime @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("last_text") val lastText: String? = null,
    val model: String? = null,
    /** Its most recent tool call, kept after its final reply. */
    @SerialName("latest_tool") val latestTool: WorkflowTool? = null,
    /** Its latest usage, not a cumulative total. */
    val tokens: Long? = null,
    val calls: Int? = null,
)

/** A [WorkflowAgent]'s or a [Subagent]'s latest tool call: its [name] and what it does ([summary], in the reader's language). */
@KeepGeneratedSerializer
@Serializable(with = WorkflowToolSerializer::class)
data class WorkflowTool(
    val id: String? = null,
    val name: String = "",
    val summary: String? = null,
)
