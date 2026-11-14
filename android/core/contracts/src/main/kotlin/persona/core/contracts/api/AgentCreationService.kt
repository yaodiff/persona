package persona.core.contracts.api

import persona.core.contracts.CoreText
import persona.core.contracts.Session
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.json.*

/** Bound to one hub and pairing generation; never reuse it after pairing changes. */
interface AgentCreationService {
    val scope: String
    suspend fun launchers(machineId: String): List<AgentLauncher>
    suspend fun directories(machineId: String, path: String = ""): AgentDirectories
    suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult
    suspend fun operation(operationId: String): AgentStartResult

    /** The receipt of a start on [machineId]: a service over several hubs asks the hub that owns the machine. */
    suspend fun operation(operationId: String, machineId: String): AgentStartResult = operation(operationId)

    /**
     * The hub's "New Agent" model use (`agent.new`, docs/assistant/model-config.md): when it has one, a new agent follows it unless
     * a launcher is picked for this start (the request then carries an empty launcher). Null from hubs without
     * unified model configuration.
     */
    suspend fun newAgentModel(): ModelPurposeDetail? = null

    /**
     * The models [launcher]'s agent offers in its own /model menu, for "New Agent"'s model picker: the node keeps the
     * list a while, [refresh] asks the CLI again. Null from hubs without the list (404).
     */
    suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean = false): LauncherModels? = null

    /**
     * What [cwd] is in Git terms (`POST …/worktree/status`, docs/protocol/protocol.md): null when the computer can't say
     * (an older node or hub, offline, an error), and then New Agent offers no worktree. [kind]: the engine the agent
     * opens in or ran in, which makes its worktree (`Session.worktreeKind`); null, the computer's default.
     */
    suspend fun worktreeStatus(machineId: String, cwd: String, kind: String? = null): WorktreeStatus? = null

    /**
     * A worktree for a new agent from the repository [cwd] is in (`…/worktree/add`); an empty [branch] is the node's pick,
     * [kind] the engine the agent opens in, which makes it (null: the computer's default).
     */
    suspend fun addWorktree(machineId: String, cwd: String, nameHint: String, branch: String, kind: String? = null): WorktreeAdded =
        throw UnsupportedOperationException("Worktrees unavailable")

    /**
     * Removes a worktree Persona made (`…/worktree/remove`); [force] only after the user confirmed what it loses, by the
     * engine the agent ran in ([kind]).
     */
    suspend fun removeWorktree(machineId: String, path: String, deleteBranch: Boolean, force: Boolean, kind: String? = null): WorktreeRemoved =
        throw UnsupportedOperationException("Worktrees unavailable")

    /**
     * [machineId]'s rows of the hub's workspace catalog (`GET /api/v1/workspaces`, #7259), for New Agent's workspace
     * choice: empty from an older hub (404) and on any failure, which then shows no choice.
     */
    suspend fun workspaces(machineId: String): List<AgentWorkspace> = emptyList()
}

/**
 * One workspace of the hub's catalog (`GET /api/v1/workspaces`, docs/protocol/protocol.md): a start with
 * `workspace_id` = [sourceId] and `host` = [source] runs inside it, in [cwd]. [projectId] is scoped to the machine and
 * [source]; [workspaceKind] `git` or `folder`; [available] / [online] false when it can't take an agent now.
 */
@Serializable
data class AgentWorkspace(
    val id: String = "",
    @SerialName("machine_id") val machineId: String = "",
    val source: String = "",
    @SerialName("source_id") val sourceId: String = "",
    val title: String = "",
    val cwd: String = "",
    val branch: String? = null,
    val head: String? = null,
    @SerialName("parent_source_id") val parentSourceId: String? = null,
    val index: Int? = null,
    @SerialName("project_id") val projectId: String? = null,
    @SerialName("project_name") val projectName: String? = null,
    @SerialName("project_path") val projectPath: String? = null,
    @SerialName("is_main") val isMain: Boolean? = null,
    @SerialName("workspace_kind") val workspaceKind: String? = null,
    val online: Boolean? = null,
    val available: Boolean? = null,
    val owned: Boolean? = null,
)

/**
 * What `worktree/status` says of a folder (server proto.WorktreeStatus, #4532): [path] is set for any folder in a
 * repository (its top level), [worktree] only for a linked worktree, [ours] only for one Persona made; [checkouts] is
 * where a new agent's worktree of this repository goes ("" from an older node); [dirty] changed files, [unmerged]
 * commits on its branch that [mainBranch] lacks.
 */
@Serializable
data class WorktreeStatus(
    val worktree: Boolean = false,
    val ours: Boolean = false,
    val path: String = "",
    val branch: String = "",
    val root: String = "",
    @SerialName("main_branch") val mainBranch: String = "",
    val checkouts: String = "",
    val dirty: Int = 0,
    val unmerged: Int = 0,
)

/** What `worktree/add` made: the checkout the agent starts in. */
@Serializable
data class WorktreeAdded(val path: String, val branch: String = "", val root: String? = null)

/** What `worktree/remove` did. */
@Serializable
data class WorktreeRemoved(val removed: Boolean = false, @SerialName("branch_deleted") val branchDeleted: Boolean = false)

/** What a launcher's agent lists in its /model menu (`GET …/launchers/{name}/models`). No [models]: it has no list. */
@Serializable
data class LauncherModels(
    /** What the launcher runs: claude, codex, traex, a runtime's. */
    val agent: String = "",
    val models: List<AgentModel> = emptyList(),
    /** Why a model picked here would not reach the agent (the launcher passes its own --model); null: it would. */
    val locked: String? = null,
)

@Serializable
data class AgentModel(
    /** What a start sends as `model`. */
    val id: String,
    val name: String = "",
    val description: String? = null,
    /** The concrete model an alias stands for with this launcher's settings (opus → its model id). */
    val resolved: String? = null,
    /** What the agent runs when it is given no model: picking it is the same as picking none. */
    @SerialName("default") val isDefault: Boolean = false,
    /** The thinking efforts it takes, weakest first; empty: no effort setting. */
    val efforts: List<String> = emptyList(),
    @SerialName("default_effort") val defaultEffort: String? = null,
    /** An id the user typed for this launcher before (feature `typed_models`, #1644): listed after its own models. */
    val custom: Boolean = false,
) {
    val label: String get() = modelName(id, name)
}

private val gptModelId = Regex("^gpt-([a-z0-9]+(?:\\.[a-z0-9]+)*(?:-[a-z0-9]+(?:\\.[a-z0-9]+)*)*)$", RegexOption.IGNORE_CASE)

/** Display only: explicit names win, and GPT versions and variants retain every component of their id. */
fun modelName(id: String?, name: String? = null): String {
    val raw = id.orEmpty()
    if (!name.isNullOrBlank() && !name.equals(raw, ignoreCase = true)) return name
    val label = name?.takeIf { it.isNotBlank() } ?: raw
    val model = gptModelId.matchEntire(label)?.groupValues?.get(1) ?: return label
    return model.split('-').mapIndexed { index, part ->
        if (index == 0) part else part.replaceFirstChar { it.titlecase(java.util.Locale.ROOT) }
    }.joinToString(" ")
}

@OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)
@KeepGeneratedSerializer
@Serializable(with = AgentLauncherSerializer::class)
data class AgentLauncher(
    val name: String,
    @SerialName("base_url") val baseUrl: String? = null,
    val model: String? = null,
    @SerialName("agent") val agentType: String = "claude",
    /** A runtime's own declaration that it can run the assistant; false or absent on other launchers. */
    val assistant: Boolean = false,
    /** Its health on this computer (docs/server/launcher-health.md); null when never checked or from an older hub. */
    val health: LauncherHealth? = null,
    /** A Persona-managed provider's own name (feature `launcher_auto_name`), shown over [name]; null for the others. */
    @SerialName("display_name") val displayName: String? = null,
    /** This computer can open Codex before its first message; absent on older nodes. */
    @SerialName("prompt_optional") val promptOptional: Boolean = false,
) {
    /** What people read for it: its display name, else its startup name (the web's `providerTitle`). */
    val title: String get() = displayName?.trim()?.takeIf(String::isNotEmpty) ?: name
}

/** Prefer the configured agent type, falling back to the hub's `agent` field, as on the web. */
@OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)
object AgentLauncherSerializer : JsonTransformingSerializer<AgentLauncher>(AgentLauncher.generatedSerializer()) {
    override fun transformDeserialize(element: JsonElement): JsonElement {
        val fields = element as? JsonObject ?: return element
        val agent = sequenceOf("agent_type", "agent").mapNotNull { fields[it] as? JsonPrimitive }
            .firstOrNull { it != JsonNull && it.content.isNotEmpty() } ?: JsonPrimitive("claude")
        return JsonObject((fields - "agent_type") + ("agent" to agent))
    }
}

/**
 * What the assistant's computer lists for Persona chats (`GET /api/v1/machines/{mid}/launchers?purpose=assistant`,
 * docs/protocol/protocol.md): the launchers that can run them, the main chat's default (Model configuration › Main chat)
 * and what can't run them there, with the hub's reason ([UnavailableLauncher], feature `assistant_model_pick`).
 */
@Serializable
data class AssistantLaunchers(
    val launchers: List<AgentLauncher> = emptyList(),
    val default: LauncherDefault? = null,
    val unavailable: List<UnavailableLauncher> = emptyList(),
)

/** The model a launcher names, short. Older nodes used the Chinese display placeholder for no model. */
fun launcherModel(launcher: AgentLauncher): String? = launcher.model
    ?.takeUnless { it == "默认" } // i18n-ignore: legacy wire value, never displayed
    ?.substringAfterLast('/')
    ?.takeIf(String::isNotBlank)

/** A purpose's effective setup ([provider] is a launcher's name). */
@Serializable
data class LauncherDefault(val provider: String = "", val model: String? = null, val effort: String? = null)

/**
 * A launcher or provider that can't run Persona chats on that computer (#5033): [kind] `launcher` or `api`, [code] why
 * (`not_main_chat`, `api_protocol`, `launcher_out_of_scope`, `launcher_own_service`) and [reason] in the request's language.
 */
@Serializable
data class UnavailableLauncher(
    val name: String,
    @SerialName("display_name") val displayName: String? = null,
    val kind: String = "",
    val agent: String? = null,
    val api: String? = null,
    val code: String = "",
    val reason: String = "",
) {
    val title: String get() = displayName?.trim()?.takeIf(String::isNotEmpty) ?: name
}

/**
 * A launcher's health on one computer as the hub records it (docs/server/launcher-health.md): [state] working, failing,
 * repairing or blocked; [kind] the failure's class (auth, model_route, cli_outdated, timeout, not_in_terminal…);
 * [error] the hub's redacted text.
 */
@Serializable
data class LauncherHealth(
    val state: String = "",
    val kind: String? = null,
    val error: String? = null,
    @SerialName("checked_at") val checkedAt: String? = null,
    @SerialName("resets_at") val resetsAt: String? = null,
)

@Serializable
data class AgentDirectory(val name: String, val path: String, @SerialName("is_git") val isGit: Boolean = false)

@Serializable
data class AgentRecentDirectory(val path: String, val name: String = "", @SerialName("last_used") val lastUsed: String? = null)

@Serializable
data class AgentDirectories(
    val path: String,
    val parent: String = "",
    val entries: List<AgentDirectory> = emptyList(),
    val recent: List<AgentRecentDirectory> = emptyList(),
)

@Serializable
data class AgentStartRequest(
    @SerialName("machine_id") val machineId: String,
    val cwd: String,
    /** Empty: the hub picks the launcher from the "New Agent" model configuration. */
    val launcher: String,
    val prompt: String? = null,
    @SerialName("permission_mode") val permissionMode: String? = null,
    val effort: String? = null,
    /** Opens a new tab in this herdr workspace instead of a new space, or starts in a catalog workspace of [host] (#7259). */
    @SerialName("workspace_id") val workspaceId: String? = null,
    @SerialName("agent_type") val agentType: String? = null,
    @SerialName("resume_session_id") val resumeSessionId: String? = null,
    /** The hub answers at once with a `starting` session and starts the agent in the background. */
    val async: Boolean? = null,
    /** Start Claude Code with ultracode on (#582, docs/protocol/protocol.md "Claude Code ultracode"); null leaves it off. */
    val ultracode: Boolean? = null,
    /** What the tab is named after when the first message goes as an ordinary message rather than [prompt]. */
    @SerialName("name_hint") val nameHint: String? = null,
    /** The model picked for this start (an id of [AgentCreationService.launcherModels]); null: the default one. */
    val model: String? = null,
    @SerialName("todo_id") val todoId: String? = null,
    /** The engine whose workspace [workspaceId] is (a catalog row's `source`, #7259); null: the computer's default. */
    val host: String? = null,
)

sealed interface AgentStartResult {
    data class Created(val session: Session) : AgentStartResult
    data object Pending : AgentStartResult
    data object Unknown : AgentStartResult
    /** Only an authoritative failed receipt permits editing and a new operation. */
    data class Rejected(val message: CoreText) : AgentStartResult
}
