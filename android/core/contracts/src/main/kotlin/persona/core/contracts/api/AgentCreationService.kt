package persona.core.contracts.api

import persona.core.contracts.CoreText
import persona.core.contracts.PaseoAgent
import persona.core.contracts.Session
import persona.core.contracts.TraeCliDisplayName
import persona.core.contracts.isTraeCliId
import persona.core.contracts.isTraeCliLabel
import persona.core.contracts.unknownAgentName
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.json.*

/** Bound to one hub and pairing generation; never reuse it after pairing changes. */
interface AgentCreationService {
    val scope: String
    suspend fun launchers(machineId: String): List<AgentLauncher>

    /**
     * [launchers] with the rest of the hub's answer (`GET /api/v1/machines/{id}/launchers`): what New Agent's picker lists
     * off on that computer, with why (#8109: the Persona-managed providers their own-service rule leaves out where the
     * agent has a sign-in of its own), in the main chat's shape. Its `unavailable` is empty from an older hub; its
     * `default` is not read (New Agent reads [newAgentModel]).
     */
    suspend fun launcherList(machineId: String): AssistantLaunchers = AssistantLaunchers(launchers(machineId))
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
    /** The same purpose on the hub that owns a preselected computer. */
    suspend fun newAgentModel(machineId: String): ModelPurposeDetail? = newAgentModel()

    /**
     * The models [launcher]'s agent offers in its own /model menu, for "New Agent"'s model picker: the node keeps the
     * list a while, [refresh] asks the CLI again. Null from hubs without the list (404).
     */
    suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean = false): LauncherModels? = null
    /** A new start's host may offer or lock a different model from the machine's default host. */
    suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean, host: String?): LauncherModels? =
        launcherModels(machineId, launcher, refresh)

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

    /** A catalog refresh, including usable hub-cache rows when a source cannot refresh; transport failures propagate. */
    suspend fun workspaceCatalog(machineId: String): AgentWorkspaceCatalog = AgentWorkspaceCatalog(workspaces(machineId))

    /**
     * Creating, renaming and deleting an engine's worktrees (#17054, [WorktreeManagement]); null where the wiring has none,
     * which then offers none of those actions.
     */
    val worktrees: WorktreeManagement? get() = null
}

/** A complete response replaces the inventory. A partial response updates returned rows but does not remove missing ones. */
data class AgentWorkspaceCatalog(
    val workspaces: List<AgentWorkspace>,
    /** Completeness as used by the New Agent picker. The list tracks failures for each source separately. */
    val complete: Boolean = true,
    val failedSources: Set<String> = emptySet(),
    val unscopedFailure: Boolean = false,
)

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
    /** The source's workspace pin, independent of Persona's session/project preferences. */
    @SerialName("pinned_at") val pinnedAt: String? = null,
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

/**
 * What `worktree/add` made: the checkout the agent starts in, and [workspaceId] the workspace a runtime engine made for
 * it (Paseo, #8856), which the start goes into; null for a worktree a terminal host makes, or from an older node.
 */
@Serializable
data class WorktreeAdded(
    val path: String,
    val branch: String = "",
    val root: String? = null,
    @SerialName("workspace_id") val workspaceId: String? = null,
)

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
    /** The node accepts a per-start model on this launcher and host. Absent on older nodes. */
    @SerialName("supports_start_model") val supportsStartModel: Boolean = false,
)

@Serializable
data class AgentModel(
    /** What a start sends as `model`. */
    val id: String,
    val name: String = "",
    val description: String? = null,
    /** The concrete model an alias stands for with this launcher's settings (opus → its model id). */
    val resolved: String? = null,
    /** Other spellings the hub folded into this row; a saved alias selects this row, not a duplicate. */
    val aliases: List<String> = emptyList(),
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

/** Display only: explicit names win, and GPT versions and variants preserve the GPT- prefix and every component. */
fun modelName(id: String?, name: String? = null): String {
    val raw = id.orEmpty()
    if (!name.isNullOrBlank() && !name.equals(raw, ignoreCase = true)) return name
    val label = name?.takeIf { it.isNotBlank() } ?: raw
    val model = gptModelId.matchEntire(label)?.groupValues?.get(1) ?: return label
    return "GPT-" + model.split('-').mapIndexed { index, part ->
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
    /**
     * Its own name, shown over [name]: a Persona-managed provider's (feature `launcher_auto_name`), or the label a
     * runtime gives its provider (Paseo's "Grok" for `grok`, #8261); null for the others.
     */
    @SerialName("display_name") val displayName: String? = null,
    /** This computer can open Codex before its first message; absent on older nodes. */
    @SerialName("prompt_optional") val promptOptional: Boolean = false,
    /**
     * The permission modes a start on it takes when its agent is not Claude Code (#8261, the node's `permission_modes`:
     * Paseo's Grok, OpenCode or Codex): `default`, its own approval, sent as no mode, and `bypassPermissions` where it has
     * an Allow everything. Null for Claude Code's launchers, which take its five, and for every other one, which takes none.
     */
    @SerialName("permission_modes") val permissionModes: List<String>? = null,
    /** Where the computer found the launcher, for example path, zshrc, or a managed provider. */
    val source: String? = null,
    /** Whether a runtime launcher can receive separate task follow-up supervision instructions. */
    @SerialName("supervision_policy") val supervisionPolicy: Boolean = false,
) {
    /** What people read for it: its display name, else its startup name (the web's `providerTitle`). */
    val title: String get() = launcherTitle(name, agentType, displayName?.trim()?.takeIf(String::isNotEmpty), baseUrl, source)
}

/**
 * Prefer the configured agent type, falling back to the hub's `agent` field, as on the web. A `permission_modes` that is
 * not a list of words is dropped rather than losing the launcher.
 */
@OptIn(kotlinx.serialization.ExperimentalSerializationApi::class)
object AgentLauncherSerializer : JsonTransformingSerializer<AgentLauncher>(AgentLauncher.generatedSerializer()) {
    override fun transformDeserialize(element: JsonElement): JsonElement {
        val fields = element as? JsonObject ?: return element
        val agent = sequenceOf("agent_type", "agent").mapNotNull { fields[it] as? JsonPrimitive }
            .firstOrNull { it != JsonNull && it.content.isNotEmpty() } ?: JsonPrimitive("claude")
        val modes = fields["permission_modes"]
        val kept = if (modes == null || (modes is JsonArray && modes.all { it is JsonPrimitive && it.isString })) fields
            else fields - "permission_modes"
        return JsonObject((kept - "agent_type") + ("agent" to agent))
    }
}

/**
 * What the assistant's computer lists for Persona chats (`GET /api/v1/machines/{mid}/launchers?purpose=assistant`,
 * docs/protocol/protocol.md): the launchers that can run them, the main chat's default (Model configuration › Main chat)
 * and what can't run them there, with the hub's reason ([UnavailableLauncher], feature `assistant_model_pick`). Without
 * a purpose (New Agent, [AgentCreationService.launcherList]) the same shape: the computer's launchers and the providers
 * its own-service rule leaves out there (#8109).
 */
@Serializable
data class AssistantLaunchers(
    val launchers: List<AgentLauncher> = emptyList(),
    val default: LauncherDefault? = null,
    val unavailable: List<UnavailableLauncher> = emptyList(),
    /** The active model profile's Agent presets whose launcher this computer has; empty on an older hub. */
    val tiers: List<PickerTier> = emptyList(),
    /** The profile [tiers] belongs to; null when there is no active profile or on an older hub. */
    @SerialName("tiers_profile") val tiersProfile: PickerTierProfile? = null,
    /**
     * Where a start that keeps the default opens while the computer starts new Agents as Orca chats (#17650); null while
     * it opens where it always did, and from an older hub.
     */
    @SerialName("default_opens") val defaultOpens: DefaultOpens? = null,
)

/**
 * `default_opens` of New Agent's launchers (#17650, docs/integrations/engines.md "Orca chats as New Agent's default"): the
 * Orca chat launcher a start that keeps the default opens as, its name, the permission modes with which it does (another
 * mode keeps it in a terminal), the model and effort that go with it, and [dropped] when the default's model or effort
 * does not (Orca's own runs). The hub decides; the form only checks that a start still keeps the default.
 */
@Serializable
data class DefaultOpens(
    val launcher: String,
    @SerialName("display_name") val displayName: String = "",
    @SerialName("permission_modes") val permissionModes: List<String> = emptyList(),
    val model: String? = null,
    val effort: String? = null,
    val dropped: Boolean = false,
) {
    val title: String get() = displayName.ifEmpty { launcher }
}

/** A named Agent launch preset from the active model profile (#2225). */
@Serializable
data class PickerTier(
    val name: String,
    val launcher: String,
    val model: String? = null,
    val effort: String? = null,
    val description: String? = null,
)

/** The model profile whose Agent presets a computer's launcher list offers. */
@Serializable
data class PickerTierProfile(val id: String, val name: String)

/** The explicit model a launcher names. Older nodes used the Chinese display placeholder for no model. */
fun launcherExplicitModel(launcher: AgentLauncher): String? = launcher.model
    ?.takeUnless { it.isBlank() || it == "默认" } // i18n-ignore: legacy wire value, never displayed

/** The model a launcher names, short. */
fun launcherModel(launcher: AgentLauncher): String? = launcherExplicitModel(launcher)
    ?.substringAfterLast('/')
    ?.takeIf(String::isNotBlank)

/** A purpose's effective setup ([provider] is a launcher's name). */
@Serializable
data class LauncherDefault(val provider: String = "", val model: String? = null, val effort: String? = null)

/**
 * A launcher or provider that can't run Persona chats on that computer (#5033), or, for New Agent, can't start Agents
 * there (#8109): [kind] `launcher` or `api`, [code] why (`not_main_chat`, `api_protocol`, `launcher_out_of_scope`,
 * `launcher_own_service`) and [reason] in the request's language.
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
    val title: String get() = launcherTitle(name, agent, displayName?.trim()?.takeIf(String::isNotEmpty))
}

private val BUILTIN_LAUNCHER_TITLES = mapOf(
    "claude" to "Claude Code",
    "codex" to "Codex",
    "claude-desktop" to "Claude Desktop",
    "codex-app" to "Codex App",
    "traex" to TraeCliDisplayName,
    "traecli" to TraeCliDisplayName,
    "trae-cli" to TraeCliDisplayName,
    "agy" to "Antigravity CLI",
    "omp" to "Oh My Pi",
    "cursor" to "Cursor",
    "gemini" to "Gemini CLI",
)

internal fun launcherTitle(
    name: String, agent: String?, displayName: String?, baseUrl: String? = null, source: String? = null,
): String {
    // Older hubs identify the built-in launcher by its base URL marker before supplying an agent field.
    val legacyMachineCli = name == "traex" && baseUrl == "(traex)" &&
        (source.isNullOrEmpty() || source == "path" || source == "zshrc" || source == "builtin")
    return when {
        agent == PaseoAgent && isTraeCliId(name) -> unknownAgentName(agent, displayName, name)
        (legacyMachineCli || isTraeCliId(agent)) && isTraeCliId(name) &&
            (displayName == null || isTraeCliLabel(displayName)) -> TraeCliDisplayName
        displayName != null && !displayName.equals(name, ignoreCase = true) -> displayName
        else -> BUILTIN_LAUNCHER_TITLES[name.lowercase(java.util.Locale.ROOT)] ?: displayName ?: name
    }
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
    /** The model picked for this start, from the launcher's list or typed for omp; null follows the default. */
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
