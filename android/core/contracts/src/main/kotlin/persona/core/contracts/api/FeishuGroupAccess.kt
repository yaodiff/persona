package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Settings › Lark bot › Group access (docs/extensions/bots.md "Group access for teammates", #16848): who besides the owner may
 * @ the bot in a group, and which read-only connector tools a guest may use. `GET` / `PUT /api/v1/bots/feishu/group-access`.
 * A hub from before this route answers [read] with null (404), and the section hides. Clients read the whole policy,
 * change it and write the whole policy back at the [FeishuGroupAccess.revision] they read; a stale revision is 409.
 */
interface FeishuGroupAccessSettings {
    /** `GET …/group-access`; null when the hub has no such route. */
    suspend fun read(): FeishuGroupAccess?

    /** `PUT …/group-access`: the whole policy, and the revision it was read at. */
    suspend fun update(revision: Long, policy: FeishuGroupAccessPolicy): FeishuGroupAccess
}

/** Who besides the owner may @ the bot in a group. */
object FeishuGroupAccessMode {
    const val OwnerOnly = "owner_only"
    const val Allowlist = "allowlist"
    const val EveryoneInAllowedChats = "everyone_in_allowed_chats"
    val All = listOf(OwnerOnly, Allowlist, EveryoneInAllowedChats)
}

/** A person on one of the policy's lists: a Lark open id (`ou_…`) and the owner's optional label. */
@Serializable data class FeishuGroupUser(
    @SerialName("open_id") val openId: String = "",
    val name: String? = null,
)

/** A group on one of the policy's lists: a Lark chat id (`oc_…`) and the owner's optional label. */
@Serializable data class FeishuGroupChat(
    @SerialName("chat_id") val chatId: String = "",
    val name: String? = null,
)

/** The whole policy a client reads and writes back. Arrays are present (never null); an absent mode is owner-only. */
@Serializable data class FeishuGroupAccessPolicy(
    val mode: String = FeishuGroupAccessMode.OwnerOnly,
    @SerialName("allowed_users") val allowedUsers: List<FeishuGroupUser> = emptyList(),
    @SerialName("allowed_chats") val allowedChats: List<FeishuGroupChat> = emptyList(),
    @SerialName("blocked_users") val blockedUsers: List<FeishuGroupUser> = emptyList(),
    @SerialName("blocked_chats") val blockedChats: List<FeishuGroupChat> = emptyList(),
    @SerialName("guest_tools") val guestTools: List<String> = emptyList(),
    @SerialName("per_chat_hourly") val perChatHourly: Int = 20,
)

/** A read-only connector tool the owner may give guests; [connector] is the connector's display name. */
@Serializable data class FeishuGuestToolOption(val name: String = "", val connector: String = "")

/** A recent @bot the policy refused (memory only): whom to allow or block, without looking ids up. [at] is unix seconds. */
@Serializable data class FeishuGroupRequest(
    @SerialName("open_id") val openId: String = "",
    val name: String? = null,
    @SerialName("chat_id") val chatId: String = "",
    @SerialName("chat_name") val chatName: String? = null,
    val at: Long = 0,
)

/** The limits the hub enforces, so the page can stop at them instead of learning from a 400. */
@Serializable data class FeishuGroupAccessLimits(
    @SerialName("list_max") val listMax: Int = 200,
    @SerialName("guest_tools_max") val guestToolsMax: Int = 8,
    @SerialName("per_chat_hourly_min") val perChatHourlyMin: Int = 1,
    @SerialName("per_chat_hourly_max") val perChatHourlyMax: Int = 120,
    @SerialName("per_chat_hourly_default") val perChatHourlyDefault: Int = 20,
)

/** The body of `GET` and `PUT /api/v1/bots/feishu/group-access`. */
@Serializable data class FeishuGroupAccess(
    val revision: Long = 0,
    /** The bot has an owner; an unpaired bot has no group access to show. */
    val paired: Boolean = false,
    val policy: FeishuGroupAccessPolicy = FeishuGroupAccessPolicy(),
    @SerialName("available_tools") val availableTools: List<FeishuGuestToolOption> = emptyList(),
    @SerialName("recent_requests") val recentRequests: List<FeishuGroupRequest> = emptyList(),
    val limits: FeishuGroupAccessLimits = FeishuGroupAccessLimits(),
)
