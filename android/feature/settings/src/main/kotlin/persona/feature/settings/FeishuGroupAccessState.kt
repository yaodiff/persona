package persona.feature.settings

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.FeishuGroupAccess
import persona.core.contracts.api.FeishuGroupAccessMode
import persona.core.contracts.api.FeishuGroupAccessPolicy
import persona.core.contracts.api.FeishuGroupAccessSettings
import persona.core.contracts.api.FeishuGroupChat
import persona.core.contracts.api.FeishuGroupRequest
import persona.core.contracts.api.FeishuGroupUser
import persona.core.contracts.api.FeishuGuestToolOption
import persona.core.designsystem.failureReasonText
import persona.core.hub.HubFailure

/**
 * Settings › Lark bot › Group access (#16848, web `lib/feishu-group-access.ts`): the policy as the hub last sent it, and
 * what a change is doing. A hub from before the route ([available] false) hides the section.
 */
internal data class FeishuGroupAccessState(
    /** Null: not read yet, hidden (an older hub, or an unpaired bot), or the last read failed. */
    val access: FeishuGroupAccess? = null,
    val loaded: Boolean = false,
    val loadError: ResText? = null,
    val error: ResText? = null,
    /** A change is under way: the page keeps what it shows and waits for the hub's answer. */
    val saving: Boolean = false,
) {
    /** The section is drawn: the hub has the route and the bot is paired. */
    val shown: Boolean get() = access?.paired == true
}

/** The questions-per-group-per-hour choices the picker offers (web `HOURLY_CHOICES`). */
internal val FeishuGroupHourlyChoices = listOf(5, 10, 20, 40, 60, 120)

/** The picker's choices in order, with the value in force included even when it is not one of the standard ones. */
internal fun feishuGroupHourlyChoices(current: Int): List<Int> =
    (FeishuGroupHourlyChoices + current).distinct().sorted()

/** The one line under the mode picker: what the mode lets people do. */
internal fun feishuGroupModeDetail(mode: String): Int = when (mode) {
    FeishuGroupAccessMode.Allowlist -> R.string.settings_feishu_group_detail_allowlist
    FeishuGroupAccessMode.EveryoneInAllowedChats -> R.string.settings_feishu_group_detail_everyone
    else -> R.string.settings_feishu_group_detail_owner_only
}

/** A list's id, an open id or a chat id, so the four lists share one add and remove. */
private fun FeishuGroupUser.idOf() = openId
private fun FeishuGroupChat.idOf() = chatId

/** The list without the entry named [id]. */
internal fun <T> withoutGroupMember(list: List<T>, id: String, idOf: (T) -> String): List<T> = list.filter { idOf(it) != id }

/** The list with [entry] added; an entry already there is replaced, so a label can be corrected. */
internal fun <T> withGroupMember(list: List<T>, entry: T, idOf: (T) -> String): List<T> =
    withoutGroupMember(list, idOf(entry), idOf) + entry

internal fun hasGroupMember(list: List<FeishuGroupUser>, id: String) = list.any { it.openId == id }
internal fun hasGroupChat(list: List<FeishuGroupChat>, id: String) = list.any { it.chatId == id }

/** [guestTools] with [name] added or removed. */
internal fun withGuestTool(policy: FeishuGroupAccessPolicy, name: String, on: Boolean): FeishuGroupAccessPolicy {
    val without = policy.guestTools.filter { it != name }
    return policy.copy(guestTools = if (on) without + name else without)
}

/**
 * The policy's guest tools that are still installed. The hub refuses a whole PUT naming a tool it no longer offers, so a
 * removed connector's tool is dropped on the next save rather than blocking every change.
 */
internal fun usableGuestTools(policy: FeishuGroupAccessPolicy, available: List<FeishuGuestToolOption>): FeishuGroupAccessPolicy {
    val names = available.map { it.name }.toSet()
    return policy.copy(guestTools = policy.guestTools.filter { it in names })
}

/** Allowing a recent request: the person and their group go on the allowed lists. Entries already listed keep their labels. */
internal fun allowGroupRequest(policy: FeishuGroupAccessPolicy, request: FeishuGroupRequest): FeishuGroupAccessPolicy {
    val user = FeishuGroupUser(request.openId, request.name?.takeIf { it.isNotBlank() })
    val chat = FeishuGroupChat(request.chatId, request.chatName?.takeIf { it.isNotBlank() })
    return policy.copy(
        allowedUsers = if (hasGroupMember(policy.allowedUsers, request.openId)) policy.allowedUsers else policy.allowedUsers + user,
        allowedChats = if (hasGroupChat(policy.allowedChats, request.chatId)) policy.allowedChats else policy.allowedChats + chat,
    )
}

/** Blocking a recent request: the person goes on the blocked people, whatever the mode. */
internal fun blockGroupRequest(policy: FeishuGroupAccessPolicy, request: FeishuGroupRequest): FeishuGroupAccessPolicy {
    val user = FeishuGroupUser(request.openId, request.name?.takeIf { it.isNotBlank() })
    return policy.copy(blockedUsers = withGroupMember(policy.blockedUsers, user, FeishuGroupUser::idOf))
}

/**
 * An id the hub will keep: trimmed, 1–100 characters, no whitespace (hub `checkGroupMembers`). Empty is not typed yet.
 * A list that already names it, or that is full, cannot take it either.
 */
internal fun feishuGroupIdError(id: String, taken: Boolean, full: Boolean): Int? {
    val trimmed = id.trim()
    return when {
        trimmed.isEmpty() -> null
        trimmed.length > 100 || trimmed.any { it.isWhitespace() } -> R.string.settings_feishu_group_id_invalid
        taken -> R.string.settings_feishu_group_id_taken
        full -> R.string.settings_feishu_group_list_full
        else -> null
    }
}

/**
 * The section's state over [api] (web `FeishuGroupAccess`): every change is read-modify-write of the whole policy at the
 * revision on screen. A 409 reloads once and says the policy changed elsewhere; a failed save leaves the page as it was.
 */
internal class FeishuGroupAccessModel(private val api: FeishuGroupAccessSettings) {
    private val mutable = MutableStateFlow(FeishuGroupAccessState())
    val state = mutable.asStateFlow()
    // Answers are taken in the order their requests were made: a read that started before a change never undoes it.
    private var issued = 0L
    private var shown = 0L

    private fun accept(n: Long, access: FeishuGroupAccess?) {
        if (n < shown) return
        shown = n
        mutable.update { it.copy(access = access) }
    }

    suspend fun load() {
        val n = ++issued
        try {
            accept(n, api.read())
            mutable.update { it.copy(loaded = true, loadError = null) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            if (n >= shown) mutable.update { it.copy(loaded = true, loadError = ResText.of(R.string.settings_feishu_group_load_failed, failureReasonText(error))) }
        }
    }

    /** Writes the policy on screen after [change], at its revision. False when there is nothing to write or the hub refused. */
    suspend fun save(change: (FeishuGroupAccessPolicy) -> FeishuGroupAccessPolicy): Boolean {
        val now = state.value.access?.takeIf { it.paired } ?: return false
        if (state.value.saving) return false
        val n = ++issued
        mutable.update { it.copy(saving = true) }
        return try {
            val next = api.update(now.revision, usableGuestTools(change(now.policy), now.availableTools))
            accept(n, next)
            mutable.update { it.copy(error = null) }
            true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            val conflict = error is HubFailure && error.status == 409
            mutable.update { it.copy(error = ResText.of(if (conflict) R.string.settings_feishu_group_conflict else R.string.settings_feishu_group_save_failed,
                *if (conflict) emptyArray() else arrayOf(failureReasonText(error)))) }
            if (conflict) reload()
            false
        } finally { mutable.update { it.copy(saving = false) } }
    }

    /** The policy again, after a stale write: the page shows what the hub has now. */
    private suspend fun reload() {
        val n = ++issued
        try { accept(n, api.read()) } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { } // the conflict is already said; the next load reads it
    }

    suspend fun setMode(mode: String) = save { it.copy(mode = mode) }

    suspend fun addUser(list: GroupList, id: String, name: String) = save { policy ->
        val user = FeishuGroupUser(id.trim(), name.trim().ifEmpty { null })
        policy.withUsers(list) { withGroupMember(it, user, FeishuGroupUser::idOf) }
    }

    suspend fun removeUser(list: GroupList, id: String) = save { it.withUsers(list) { users -> withoutGroupMember(users, id, FeishuGroupUser::idOf) } }

    suspend fun addChat(list: GroupList, id: String, name: String) = save { policy ->
        val chat = FeishuGroupChat(id.trim(), name.trim().ifEmpty { null })
        policy.withChats(list) { withGroupMember(it, chat, FeishuGroupChat::idOf) }
    }

    suspend fun removeChat(list: GroupList, id: String) = save { it.withChats(list) { chats -> withoutGroupMember(chats, id, FeishuGroupChat::idOf) } }

    suspend fun allow(request: FeishuGroupRequest) = save { allowGroupRequest(it, request) }

    suspend fun block(request: FeishuGroupRequest) = save { blockGroupRequest(it, request) }

    suspend fun setTool(name: String, on: Boolean) = save { withGuestTool(it, name, on) }

    suspend fun setPerChatHourly(value: Int) = save { it.copy(perChatHourly = value) }
}

/** Which of the four lists an add or remove is for. */
internal enum class GroupList { Allowed, Blocked }

private fun FeishuGroupAccessPolicy.withUsers(list: GroupList, change: (List<FeishuGroupUser>) -> List<FeishuGroupUser>) = when (list) {
    GroupList.Allowed -> copy(allowedUsers = change(allowedUsers))
    GroupList.Blocked -> copy(blockedUsers = change(blockedUsers))
}

private fun FeishuGroupAccessPolicy.withChats(list: GroupList, change: (List<FeishuGroupChat>) -> List<FeishuGroupChat>) = when (list) {
    GroupList.Allowed -> copy(allowedChats = change(allowedChats))
    GroupList.Blocked -> copy(blockedChats = change(blockedChats))
}
