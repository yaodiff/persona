package persona.core.contracts.api

import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put

/** Pairing-scoped assistant actions; the composition root supplies the active transport. */
interface AssistantManagement {
    /** Actions bound to one assistant, even if the page's selection changes; null names the default assistant. */
    fun forManager(manager: String?): AssistantManagement = this

    suspend fun warm()
    /**
     * What the main conversation still needs (GET /api/v1/assistant/readiness, hub feature `assistant_readiness`);
     * [probe] really runs a one-word check (it costs a little, the hub reuses one from the last 30 seconds).
     */
    suspend fun readiness(probe: Boolean = false): AssistantReadiness = throw UnsupportedOperationException("Readiness unavailable")
    suspend fun dismissReadiness(dismissed: Boolean): Unit = throw UnsupportedOperationException("Readiness unavailable")
    // Where the assistant runs (Settings › Assistant). Defaults keep transports and fakes written before the page compiling.
    suspend fun config(): AssistantConfig = throw UnsupportedOperationException("Assistant settings unavailable")
    suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig = throw UnsupportedOperationException("Assistant settings unavailable")
    /** The launchers a machine offers (GET /api/v1/machines/{id}/launchers). */
    suspend fun launchers(machineId: String): List<AgentLauncher> = throw UnsupportedOperationException("Assistant settings unavailable")
    /** Only launchers the hub accepts for a Persona conversation; New Agent keeps using [launchers]. */
    suspend fun assistantLaunchers(machineId: String): List<AgentLauncher> = launchers(machineId)
    /** The models of [launcher], null on a hub without the list. */
    suspend fun launcherModels(machineId: String, launcher: String): LauncherModels? = null
    /**
     * [assistantLaunchers] with the rest of the hub's answer: the main chat's default and what can't run Persona chats
     * there, with why (`GET /api/v1/machines/{id}/launchers?purpose=assistant`): the model panel's providers (#5033, #5055).
     */
    suspend fun assistantLauncherList(machineId: String): AssistantLaunchers = AssistantLaunchers(assistantLaunchers(machineId))
    /**
     * Keeps [pick] as what the conversation's next messages run on (`PUT /api/v1/assistant/conversations/{id}/model`,
     * feature `assistant_model_pick`); answers the pick the hub kept. A refusal throws the hub's failure with its words
     * (409 `launcher_out_of_scope`, `launcher_own_service`, `launcher_missing`, `launcher_other_kind`; 400 for a launcher
     * that can't run assistant chats).
     */
    suspend fun setModelPick(session: SessionKey, pick: persona.core.contracts.ModelPick): persona.core.contracts.ModelPick =
        throw UnsupportedOperationException("Model pick unavailable")
    /** The conversation follows the default again (`DELETE …/conversations/{id}/model`): Follow default, Undo pick. */
    suspend fun clearModelPick(session: SessionKey): Unit = throw UnsupportedOperationException("Model pick unavailable")
    suspend fun endConversation(session: SessionKey)
    /**
     * Compacts an assistant conversation's context now (POST /api/v1/assistant/conversations/{id}/compact, hub feature
     * `assistant_compact`); its progress comes back on the session's `compaction`. A hub that refuses (409: `busy`,
     * `compacting`, `gone`, `unsupported`) throws [AssistantRefused] with its own words.
     */
    suspend fun compactConversation(session: SessionKey): Unit = throw UnsupportedOperationException("Compact context unavailable")
    /**
     * Archives an assistant conversation (POST /api/v1/assistant/conversations/{id}/archive): off the list, its data
     * kept; a new message to it brings it back. The hub refuses the main conversation.
     */
    suspend fun archiveConversation(session: SessionKey): Unit = throw UnsupportedOperationException("Archive unavailable")
    /** Brings an archived conversation back into the list (POST /api/v1/assistant/conversations/{id}/unarchive). */
    suspend fun unarchiveConversation(session: SessionKey): Unit = throw UnsupportedOperationException("Unarchive unavailable")
    /**
     * The archived conversations, newest first ("Archived (N)", iOS `archivedConversations`): those `GET /api/v1/assistant?archived=1`
     * marks `archived`, never a health chat. Only the ones their computer still lists (docs/assistant/assistant.md
     * "Managing its own conversations").
     */
    suspend fun archivedConversations(): List<Session> = throw UnsupportedOperationException("Archived chats unavailable")
    /**
     * Chats found by what was said in them (`GET /api/v1/assistant/search?q=&limit=`, hub feature `assistant_search`).
     * Null when this manager has no such route (404, an older build): the app keeps filtering titles and last messages.
     */
    suspend fun searchChats(query: String, limit: Int = 30): AssistantSearchAnswer? =
        throw UnsupportedOperationException("Assistant search unavailable")
    /**
     * Renames a conversation (POST /api/v1/sessions/{mid}/{sid}/rename with `title`): the hub keeps it as the user's own
     * title, which automatic titles no longer replace (docs/assistant/assistant.md "Chat titles").
     */
    suspend fun renameConversation(session: SessionKey, title: String): Unit = throw UnsupportedOperationException("Rename unavailable")
    /**
     * Makes a conversation a leader, 常驻 (POST /api/v1/assistant/directions/{id}/make, #4331): the hub keeps it for every
     * device, and its row comes and goes with the hub's `directions` message (the drawer reads the tree again). It refuses
     * the main conversation, a chat that is already one, and any past the most it allows.
     */
    suspend fun makeDirection(session: SessionKey): Unit = throw UnsupportedOperationException("Directions unavailable")
    /**
     * Ends a conversation's leadership (POST /api/v1/assistant/directions/{id}/end, #4331): it leaves 常驻; the tasks it
     * handed out keep their supervisor. The conversation itself stays, an ordinary chat now.
     */
    suspend fun endDirection(session: SessionKey): Unit = throw UnsupportedOperationException("Directions unavailable")
    /**
     * Puts the main conversation away for a new one (POST /api/v1/assistant/main/new, hub feature `assistant_main_new`):
     * it is renamed and archived, and the next message to the main conversation starts a new one. [carrySummary] brings
     * what the old one left; [expect] is the main conversation on screen (a hub where another device replaced it since
     * refuses with `main_changed`, as [AssistantRefused]; `busy` while it answers or compacts).
     */
    suspend fun newMainConversation(carrySummary: Boolean, expect: String?): NewMainResult =
        throw UnsupportedOperationException("New main chat unavailable")
    /**
     * Clears the main conversation's context in place (POST /api/v1/assistant/main/clear, hub feature `assistant_main_clear`,
     * #5095): the hub stops a turn it is on and puts it away as [newMainConversation] does, carrying nothing over, so the
     * next message to the main conversation starts a new one. [expect] as there (`main_changed` as [AssistantRefused]).
     * Answers the conversation put away (empty when there was none).
     */
    suspend fun clearMainConversation(expect: String?): String =
        throw UnsupportedOperationException("Clear context unavailable")
    /** Scheduled tasks (GET /api/v1/assistant/tasks): one table of once / daily / interval tasks, docs/assistant/assistant.md "Scheduled tasks". */
    suspend fun tasks(): List<ScheduledTask>
    /** Creates [task] (no id yet, POST) or saves what the user edits on it (PUT); returns it as the hub has it. */
    suspend fun saveTask(task: ScheduledTask): ScheduledTask
    suspend fun deleteTask(id: String)
    /** "Run now": the conversation the run went to. */
    suspend fun runTask(id: String): SessionKey
    /** "New conversation next time": the next run starts a conversation of its own (POST tasks/{id}/fresh). */
    suspend fun freshTask(id: String): ScheduledTask
    suspend fun runs(): List<Run>
    suspend fun notes(): AssistantNotes
    suspend fun setNotes(text: String)
    suspend fun memory(): List<MemoryFile>
    suspend fun saveMemory(name: String, content: String)
    suspend fun deleteMemory(name: String)
    /**
     * The skills on the machine that runs the assistant (GET /api/v1/skills), read only. A hub that cannot read them
     * (no assistant machine, the node did not answer) throws [AssistantRefused] with its own words.
     */
    suspend fun skills(): AssistantSkills = throw UnsupportedOperationException("Skills unavailable")
    /**
     * What "Always allow" remembered in an assistant conversation (GET /api/v1/assistant/conversations/{id}/always,
     * docs/assistant/assistant.md "Always allow in this session"). Null when there is nothing to show here: a hub
     * without the route, or a device that may not use it (404 / 403).
     */
    suspend fun alwaysGrants(session: SessionKey): List<AlwaysGrant>? = null
    /**
     * Revokes one of them (DELETE …/always/{hash}); idempotent. True when the grant is gone but the conversation's
     * running process could not be reset, so it keeps the permission until the user restarts it (`restart_required`).
     */
    suspend fun revokeAlwaysGrant(session: SessionKey, keyHash: String): Boolean =
        throw UnsupportedOperationException("Always-allowed permissions unavailable")
}

/** One message a chat was found by (`docs/protocol/protocol.md` "Assistant chat search"). `ranges` are UTF-16 `[start, end)` in [snippet]. */
@Serializable
data class AssistantSearchHit(
    @SerialName("item_id") val itemId: String = "",
    val seq: Int = 0,
    val role: String = "",
    val ts: String? = null,
    val snippet: String = "",
    val ranges: List<List<Int>> = emptyList(),
)

/** One chat the manager's search returned. [titleMatch]: the title matched, so the row opens as usual, with no snippet. */
@Serializable
data class AssistantSearchResult(
    @SerialName("session_id") val sessionId: String = "",
    @SerialName("machine_id") val machineId: String = "",
    val title: String = "",
    val main: Boolean = false,
    val archived: Boolean = false,
    val origin: String? = null,
    @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("title_match") val titleMatch: Boolean = false,
    val hits: List<AssistantSearchHit> = emptyList(),
    @SerialName("more_hits") val moreHits: Int = 0,
)

@Serializable
data class AssistantSearchIndexed(val done: Int = 0, val total: Int = 0)

@Serializable
data class AssistantSearchAnswer(
    val results: List<AssistantSearchResult> = emptyList(),
    /** Some chats are not in the index yet, or it could not be searched this time. */
    val partial: Boolean = false,
    val indexed: AssistantSearchIndexed? = null,
)

/** One remembered "Always allow": the tool it covers and when; only the key's hash is stored, not the exact scope. */
@Serializable
data class AlwaysGrant(
    @SerialName("key_hash") val keyHash: String = "",
    @SerialName("tool_name") val toolName: String = "",
    @SerialName("created_at") val createdAt: String? = null,
)

/**
 * One scheduled task (docs/protocol/protocol.md `Task`). Read in full from the hub; [editable] is what the app writes back, so the
 * hub's own fields (last / next run, its own conversation, who made it) are never sent.
 */
@Serializable
data class ScheduledTask(
    val id: String = "",
    val title: String = "",
    val prompt: String = "",
    /** [ONCE] / [DAILY] / [INTERVAL]. */
    @SerialName("repeat") val repeats: String = ONCE,
    /** once: when; interval: the next run. */
    val at: String? = null,
    /** daily: "HH:MM" in the user's time zone. */
    val time: String? = null,
    /** daily: 1 = Monday … 7 = Sunday; empty = every day. */
    val weekdays: List<Int> = emptyList(),
    @SerialName("every_minutes") val everyMinutes: Int = 0,
    val enabled: Boolean = false,
    /** The conversation it came from (on [machineId]): runs go into it. Empty = the task's own conversation. */
    @SerialName("machine_id") val machineId: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    @SerialName("permission_mode") val permissionMode: String? = null,
    val launcher: String? = null,
    val effort: String? = null,
    @SerialName("created_by") val createdBy: String? = null,
    @SerialName("created_at") val createdAt: String? = null,
    @SerialName("last_run") val lastRun: String? = null,
    @SerialName("last_session_id") val lastSessionId: String? = null,
    @SerialName("last_status") val lastStatus: String? = null,
    @SerialName("last_error") val lastError: String? = null,
    @SerialName("next_run") val nextRun: String? = null,
    /** The task's own conversation, kept by the hub; empty = the next run starts one. */
    @SerialName("run_machine_id") val runMachineId: String? = null,
    @SerialName("run_session_id") val runSessionId: String? = null,
    /**
     * "Push when done": "" (or absent) = push after every run, [PUSH_ASKED] = only what a run sends with notify_user. The hub
     * offers it with the [PUSH_FEATURE] hello feature; an older one leaves it out.
     */
    val push: String? = null,
) {
    val pushesOnlyAsked: Boolean get() = push == PUSH_ASKED

    /**
     * The fields `PUT /api/v1/assistant/tasks/{id}` saves; "" = the default. `push` goes only when the task has one, so a
     * hub without it is never sent it and an unchanged one keeps what it has.
     */
    fun editable(): JsonObject = buildJsonObject {
        if (id.isNotBlank()) put("id", id)
        put("title", title)
        put("prompt", prompt)
        put("repeat", repeats)
        at?.takeIf { it.isNotBlank() }?.let { put("at", it) }
        put("time", time.orEmpty())
        put("weekdays", JsonArray(weekdays.map(::JsonPrimitive)))
        put("every_minutes", everyMinutes)
        put("enabled", enabled)
        put("session_id", sessionId.orEmpty())
        put("permission_mode", permissionMode.orEmpty())
        push?.let { put("push", it) }
    }

    companion object {
        const val ONCE = "once"
        const val DAILY = "daily"
        const val INTERVAL = "interval"
        /** "Only when it asks to" (docs/assistant/assistant.md "Push after running"). */
        const val PUSH_ASKED = "asked"
        /** The hello feature of a hub that reads and writes [push]. */
        const val PUSH_FEATURE = "task_push"
    }
}

@Serializable
data class Run(
    val id: String = "",
    val kind: String = "",
    val title: String = "",
    val at: String? = null,
    val status: String = "",
    val error: String? = null,
    val manual: Boolean = false,
    @SerialName("session_id") val sessionId: String? = null,
    @SerialName("machine_id") val machineId: String? = null,
    /** The routine's or follow-up's id; one routine's history is the runs that carry it. */
    @SerialName("ref_id") val refId: String = "",
    /** When it was meant to run; [at] after this is how late it ran. */
    val due: String? = null,
)

@Serializable
data class AssistantNotes(
    val notes: String? = null,
    @SerialName("notes_effective") val effective: String = "",
    @SerialName("machine_id") val machineId: String = "",
)

@Serializable
data class MemoryFile(val name: String = "", val content: String = "", @SerialName("updated_at") val updatedAt: String? = null)

/** What "New main chat" did: the conversation put away ([archived], empty when there was none) and whether any of it carries over. */
data class NewMainResult(val archived: String = "", val carried: Boolean = false)

/** The hub refused an assistant action (409 with a `code`); [message] is the hub's own words, fit to show as they are. */
class AssistantRefused(val code: String, message: String) : Exception(message)

/** One skill as the assistant's machine lists it: a directory with SKILL.md; [source] is `user` or `builtin`. */
@Serializable
data class AgentSkill(val name: String = "", val description: String? = null, val source: String = "user") {
    val builtin: Boolean get() = source == "builtin"
}

/** GET /api/v1/skills: the machine that runs the assistant and its skills. */
@Serializable
data class AssistantSkills(
    @SerialName("machine_id") val machineId: String = "",
    val machine: String? = null,
    val skills: List<AgentSkill> = emptyList(),
)
