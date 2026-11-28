package persona.feature.agents

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.ImageLimits
import persona.core.contracts.Machine
import persona.core.contracts.PaseoAgent
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import persona.core.domain.models.ModelPanel
import persona.feature.chat.ChatLinkContext
import persona.feature.chat.ComposerUploadLimiter
import persona.feature.chat.ForwardMessage
import persona.core.domain.sort.AgentProjectFolder
import persona.feature.chat.ForwardPlacements
import persona.feature.chat.ForwardPlacement
import persona.feature.chat.PhotoCandidate
import persona.feature.chat.composerPhotoRoom
import persona.feature.chat.planComposerPhotos
import persona.feature.chat.R as ChatR
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import kotlinx.coroutines.channels.Channel
import java.time.Instant
import java.time.temporal.ChronoUnit
import java.util.UUID

/** The catalog source and engine `host` of a Paseo workspace (#7259), and the `kind` its worktrees are made by (#8856). */
private const val PaseoHost = "paseo"

/** The same for an Orca workspace: Orca makes a new Agent's worktree as a child of the one picked (#17050). */
private const val OrcaHost = "orca"

/** The same host a fresh start uses: the explicit workspace host, or the selected computer's default. */
private fun Machine.supportsOmpStartModel(host: String?): Boolean {
    val enabled = engineHosts
    if (enabled != null) {
        val target = host ?: engineDefault
        return (target == "herdr" || target == "orca") && target in enabled
    }
    val legacyHerdr = engine.isEmpty() || engine == "herdr" || engine == "herdr-both"
    if (legacyHerdr) return host == null || host == "herdr"
    return engine == "orca" && (host == null || host == "orca")
}

/** What the Git status of the folder picked is asked by: the computer, the folder, a space, and the engine asked. */
private data class RepoQuery(val machineId: String, val cwd: String, val space: AgentSpacePreset?, val kind: String?)

/** A new tab in an existing herdr workspace: machine and directory are fixed. */
internal data class AgentSpacePreset(val machineId: String, val workspaceId: String, val cwd: String, val name: String)

/**
 * What a caller fills in ahead (the knowledge base's "Open Agent"): [machineId] and [cwd] are picked when that machine is
 * online, [prompt] starts the first message; all of it can still be changed. [context] goes before the first message as it
 * is (a forwarded message's block, #3901). [forwardedPhotos] is the matching in-memory handoff from that forward.
 */
internal data class NewAgentPrefill(val machineId: String? = null, val cwd: String? = null, val prompt: String? = null,
    val context: String? = null, val forwardedPhotos: List<ForwardPlacement> = ForwardPlacements.takeNewAgent(context))

/** The forward block a required first message may stand in for: one the card can read. Null for no context, or unreadable. */
internal fun usableForward(context: String?): String? =
    context?.takeIf { it.isNotBlank() }?.takeIf { ForwardMessage.read(it) != null }

/** The hub's "New Agent" model use (docs/assistant/model-config.md), read once per form. */
internal sealed interface NewAgentModelConfig {
    /** Still reading. */
    data object Unknown : NewAgentModelConfig
    /** An older hub: a launcher is picked here as before. */
    data object Unsupported : NewAgentModelConfig
    /** New agents follow [detail] unless a launcher is picked for this start. */
    data class Follow(val detail: ModelPurposeDetail) : NewAgentModelConfig
    /** The hub has it but it could not be read: a launcher must be picked, or the options reloaded. */
    data class Failed(val reason: UiText) : NewAgentModelConfig
}

/**
 * The model list of the launcher that will run ([launcher] on [machineId]), for the "Model" row. [list] stays while it
 * is asked again; null while the first answer is on its way, after a failure, and from hubs without the list.
 */
internal data class NewAgentModels(
    val machineId: String = "",
    val launcher: String = "",
    val list: LauncherModels? = null,
    val loading: Boolean = false,
    val error: UiText? = null,
)

internal data class NewAgentState(
    val initialized: Boolean = false,
    val space: AgentSpacePreset? = null,
    val project: AgentProjectFolder? = null,
    val machineId: String = "",
    val startMachine: Machine? = null,
    val selection: AgentDefaults = AgentDefaults(),
    val launchers: List<AgentLauncher> = emptyList(),
    /**
     * What the hub lists off on this computer (#8109, its `unavailable`): the Persona-managed providers their own-service
     * rule leaves out here, with why. Shown after the launchers ([leftOutLaunchers]), never picked; empty from an older hub.
     */
    val unavailable: List<UnavailableLauncher> = emptyList(),
    /** Agent presets of the model profile in use on this computer (#2225); empty on an older hub. */
    val tiers: List<PickerTier> = emptyList(),
    /** The profile [tiers] belongs to, for the picker heading. */
    val profile: PickerTierProfile? = null,
    /** The preset whose launcher, model and effort fill this start; a manual edit to any of them clears the mark. */
    val pickedTier: PickerTier? = null,
    /** Where a start that keeps the default opens, as the hub says (#17650, [opensAsOrcaChat]); null: where it always did. */
    val defaultOpens: persona.core.contracts.api.DefaultOpens? = null,
    val recent: List<AgentRecentDirectory> = emptyList(),
    val browser: AgentDirectories? = null,
    val browsing: Boolean = false,
    val browsePath: String = "",
    val prompt: String = "",
    /** A block that goes before the first message (a forward, #3901), drawn over it as a card; null: none. */
    val context: String? = null,
    val loading: Boolean = false,
    val starting: Boolean = false,
    val pending: AgentStartOperation? = null,
    val created: Session? = null,
    val error: AgentText? = null,
    val modelConfig: NewAgentModelConfig = NewAgentModelConfig.Unknown,
    /** The prefilled machine was not online, so another one was picked (named here for the form to say so). */
    val prefillOffline: String? = null,
    val models: NewAgentModels = NewAgentModels(),
    /**
     * The model for this start, listed by the launcher or typed for omp. Empty follows the default.
     */
    val model: String = "",
    /** This hub's recent setups, newest first; the "Recent" cards are the first ones whose machine is listed. */
    val recentSetups: List<RecentSetup> = emptyList(),
    /** The recent setup a card filled the form with, until a field is changed. */
    val pickedSetup: RecentSetup? = null,
    /** Goes up each time a card has filled the form: the form then puts the cursor in the first message. */
    val focusPrompt: Int = 0,
    /** Claude Code's ultracode for this start (#582, iOS NewChatView): off each time the form opens, never saved. */
    val ultracode: Boolean = false,
    /**
     * The permission mode picked for a launcher that lists its own ([providerModes], #8261): "" its own approval, or one
     * it lists (Allow everything). Picked on this form and never remembered, so a mode left from a Claude Code start
     * never reaches such a start; back to "" each time the form opens.
     */
    val providerMode: String = "",
    /** The thinking effort picked for such a launcher, one its model lists ([providerEfforts]); "" its own. Never remembered. */
    val providerEffort: String = "",
    /** What `worktree/status` says of the folder picked (#4532); null while asking or when the computer can't say. */
    val repo: WorktreeStatus? = null,
    /** Start it in a worktree of its own, on [worktreeBranch] (empty: the node's pick). */
    val inWorktree: Boolean = false,
    val worktreeBranch: String = "",
    /**
     * Photos and files for the first message (#2321, iOS NewAgentAttachments), each uploaded to [attachedOn] when picked
     * and sent the way the chat composer sends them ([NewAgentAttachments.firstMessage]).
     */
    val attachments: List<persona.feature.chat.ComposerAttachment> = emptyList(),
    /** The computer [attachments] are uploaded to; "" before the first. */
    val attachedOn: String = "",
    /** Why files went (they are on the computer picked before), or why picks were not taken; cleared by the next pick. */
    val attachNote: AgentText? = null,
    /** The selected computer's Orca and Paseo workspaces by project (#7259, [NewAgentWorkspaces]); empty: none shown. */
    val workspaces: List<NewAgentWorkspaceSection> = emptyList(),
    /** The catalog workspace picked: the start goes into it (its folder, `workspace_id` and `host`); null: by folder. */
    val workspace: persona.core.contracts.api.AgentWorkspace? = null,
    /** The folder picked before [workspace], which "Choose by folder" brings back. */
    val folderBeforeWorkspace: String = "",
) {
    val locked get() = !initialized || starting || pending != null || created != null
    /**
     * Its first message goes as an ordinary message once the start is answered, so it takes attachments: Claude Code, and
     * Paseo's other providers ([PaseoAgent], #8143) as iOS and the web send theirs. Codex and other CLIs take theirs with
     * the start as text, and take none (iOS `FirstMessage.hold`, web `launcher_no_attachments`).
     */
    val firstMessageAfterStart: Boolean get() = agentType == "claude" || agentType == PaseoAgent
    val takesAttachments: Boolean get() = firstMessageAfterStart
    /** What the attachments hold Start back for, said under it (iOS `FirstMessage.hold`); null: nothing. */
    val attachmentsHold: AgentText? get() = when {
        attachments.isEmpty() -> null
        !takesAttachments -> AgentText(R.string.agents_attach_launcher_hold, agentType.agentLabel())
        attachments.any { it.path == null } -> AgentText(R.string.agents_attach_uploading_hold)
        else -> null
    }
    /**
     * The folder is in a Git repository and this is not a tab in an existing space: the worktree switch shows (#4532).
     * A workspace picked (#7259, the start goes into it) offers it only when its engine makes the worktree
     * ([worktreeEngine]): Paseo as a workspace of its own (#8856), Orca as a child of the one picked (#17050).
     */
    val worktreeOffered: Boolean get() = space == null && (workspace == null || worktreeEngine != null) && repo?.path?.isNotEmpty() == true
    /**
     * The engine of the workspace picked when it makes that workspace's worktrees itself (Paseo, Orca): the `kind` for
     * `worktree/status` and `add`; null for none picked, or another engine's.
     */
    val worktreeEngine: String? get() = workspace?.source?.takeIf { it == PaseoHost || it == OrcaHost }
    val follow: ModelPurposeDetail? get() = (modelConfig as? NewAgentModelConfig.Follow)?.detail
    /** An empty launcher follows the model configuration, when the hub has one. */
    val following: Boolean get() = follow != null && selection.launcher.isEmpty()
    /**
     * The Orca chat this start opens as while the computer starts new Agents as Orca chats (#17650): the hub's
     * [defaultOpens], while the start keeps the default (no launcher, preset or model picked, no other engine's workspace,
     * no ultracode) with a permission mode the chat takes; null when it opens where it always did. The hub decides where
     * a start goes; this only checks that this one still keeps the default.
     */
    val opensAsOrcaChat: persona.core.contracts.api.DefaultOpens? get() {
        val opens = defaultOpens ?: return null
        if (!following || pickedTier != null || sentModel != null || ultracode && agentType == "claude") return null
        if (workspace?.source?.let { it != OrcaHost } == true) return null
        val mode = if (claudeModes) selection.mode else ""
        return opens.takeIf { mode.isEmpty() || mode in it.permissionModes }
    }
    /** The launcher the "New Agent" configuration resolves to on this machine, when it is one of its launchers. */
    val configuredLauncher: AgentLauncher? get() = follow?.purpose?.effective?.provider?.removePrefix("launcher.")
        ?.let { name -> launchers.firstOrNull { it.name == name } }
    val agentType: String get() = if (following) configuredLauncher?.agentType ?: "claude"
        else launchers.firstOrNull { it.name == selection.launcher }?.agentType ?: "claude"
    /** The launcher that will run, whose models the "Model" row lists: the one picked, or the configuration's. */
    val modelLauncher: String get() = if (following) configuredLauncher?.name.orEmpty() else selection.launcher
    /** [modelLauncher] as this computer lists it; null before its launchers are read or when it lists no such one. */
    val runningLauncher: AgentLauncher? get() = launchers.firstOrNull { it.name == modelLauncher }
    /** A separately registered runtime can also call its agent omp. Its advertised catalogue and options belong to it. */
    val ompCli: Boolean get() = runningLauncher?.let {
        it.agentType == "omp" && (it.source in nativeLauncherSources || ProtocolCompatibility.managedProvider(it.source))
    } == true
    val nativeOmpLauncher: Boolean get() = runningLauncher?.let {
        it.agentType == "omp" && it.name == "omp" && it.source in nativeLauncherSources
    } == true
    val ompStart: Boolean get() = nativeOmpLauncher &&
        startMachine?.supportsOmpStartModel(workspace?.source?.takeIf { space == null && it.isNotEmpty() }) == true
    /** What people call [modelLauncher]: its display name (Paseo's label for its provider, #8261), else its name. */
    val modelLauncherTitle: String get() = runningLauncher?.title ?: modelLauncher
    /**
     * The permission modes a start on [runningLauncher] takes when they are not Claude Code's (#8261, the node's
     * `permission_modes`: Paseo's Grok, OpenCode or Codex): the form offers its own approval and, where it lists that too,
     * Allow everything ([providerMode]), and the efforts its model lists ([providerEfforts]). Null for Claude Code's
     * launchers and for every other one, which takes neither.
     */
    val providerModes: List<String>? get() = runningLauncher?.permissionModes?.takeIf { it.isNotEmpty() && !ompCli }
    /** Claude Code's five modes, its efforts and ultracode, remembered per computer: a Claude Code launcher's start. */
    val claudeModes: Boolean get() = agentType == "claude" && providerModes == null
    /** Older computers still require Codex's first message, including when following model configuration. */
    val promptRequired: Boolean get() = agentType == "codex" &&
        launchers.firstOrNull { it.name == modelLauncher }?.promptOptional != true
    /** A native omp node advertises per-start model support independently of the number of suggestions. */
    val modelList: LauncherModels? get() = models.list?.takeIf {
        models.machineId == machineId && models.launcher == modelLauncher &&
            if (ompCli) ompStart && (it.agent.isEmpty() || it.agent == "omp") && (it.supportsStartModel || it.locked != null)
            else it.models.isNotEmpty() || it.locked != null
    }
    /** There is a list and a pick reaches the agent (the launcher does not pass its own --model). */
    val canPickModel: Boolean get() = modelList.let { it != null && it.locked == null }
    val pickedModel: AgentModel? get() = model.takeIf { it.isNotEmpty() && canPickModel }
        ?.let { id -> modelList?.models?.firstOrNull { it.id == id } }
    fun typedModel(id: String): String? = if (ompStart && canPickModel) ModelPanel.typedModelId(id) else null
    /**
     * The `model` a start sends: a manual pick omits the default, while a preset's default stays explicit so Model
     * Settings cannot silently replace it. A model the launcher cannot take is left out and explained by [presetLeftOut].
     */
    val sentModel: String? get() {
        val listed = pickedModel ?: return typedModel(model)
        return listed.id.takeIf { !listed.isDefault || !pickedTier?.model.isNullOrEmpty() }
    }
    /** Model configuration › New Agent's model: what runs with no pick when its launcher is the one that runs. */
    val configuredModel: String get() = follow?.takeIf { configuredLauncher?.name == modelLauncher && (!ompCli || following) }
        ?.purpose?.effective?.model.orEmpty()
    /** The thinking efforts the model that will run takes, weakest first; null: not known, every level is offered. */
    val modelEfforts: List<String>? get() {
        val list = modelList ?: return null
        val running = pickedModel ?: if (configuredModel.isEmpty()) list.models.firstOrNull { it.isDefault }
            else list.models.firstOrNull { it.id == configuredModel }
        return running?.efforts
    }
    /** The efforts a start on a launcher that lists its modes takes (#8261): only those its model lists, none while not known. */
    val providerEfforts: List<String> get() = if (providerModes == null) emptyList() else modelEfforts.orEmpty()
    /** The effort a start sends under the existing Claude Code / provider rules; an unsupported preset effort is omitted. */
    val sentEffort: String? get() {
        val efforts = modelEfforts
        return if (claudeModes) selection.effort.takeIf { it.isNotEmpty() && (efforts == null || it in efforts) }
            else providerEffort.takeIf { it.isNotEmpty() && it in providerEfforts }
    }
    /** A preset with an explicit model or effort waits for the list that can validate it before Start becomes available. */
    val presetNeedsModels: Boolean get() {
        val tier = pickedTier ?: return false
        if (ompCli && !ompStart) return false
        return (!tier.model.isNullOrEmpty() || !tier.effort.isNullOrEmpty()) && models.loading &&
            models.machineId == machineId && models.launcher == modelLauncher
    }
    /** What of the picked preset cannot reach the agent, once its model list has answered; empty while that list loads. */
    val presetLeftOut: List<AgentText> get() {
        val tier = pickedTier ?: return emptyList()
        if (models.loading || tier.launcher != modelLauncher) return emptyList()
        return buildList {
            val model = tier.model
            if (!model.isNullOrEmpty() && sentModel != model)
                add(AgentText(R.string.agents_new_preset_left_out_model, model))
            val effort = tier.effort
            if (!effort.isNullOrEmpty() && sentEffort != effort)
                add(AgentText(R.string.agents_new_preset_left_out_effort, effort))
        }
    }
}

private val nativeLauncherSources = setOf(null, "", "path", "zshrc", "builtin")

/** A model selected for herdr stays on that host, including when the computer's default changes. */
private fun NewAgentState.afterTargetChange(previous: NewAgentState): NewAgentState =
    if (model.isNotEmpty() && previous.ompStart && !ompStart) copy(model = "", pickedTier = null) else this

/**
 * The form kept consistent with the model list: a pick the list no longer has goes back to the default, and an
 * effort the model that will run does not take back to "Follow launcher".
 */
private fun NewAgentState.settled(): NewAgentState {
    var next = this
    // A preset keeps its requested values so sentModel / sentEffort and presetLeftOut can explain what cannot run.
    if (pickedTier != null) return this
    val list = modelList
    val lostOmpOverride = ompCli && !models.loading && models.error == null && !canPickModel
    if (model.isNotEmpty() && (lostOmpOverride || (list != null && list.models.none { it.id == model } && typedModel(model) == null)))
        next = next.copy(model = "")
    val efforts = next.modelEfforts
    if (efforts != null && next.selection.effort.isNotEmpty() && next.selection.effort !in efforts) {
        next = next.copy(selection = next.selection.copy(effort = ""))
    }
    if (next.providerEffort.isNotEmpty() && next.providerEffort !in next.providerEfforts) next = next.copy(providerEffort = "")
    return next
}

internal class NewAgentViewModel(
    /** Also the Agents list's way to a worktree's status and removal on Close Agent / Delete Chat (#4532). */
    internal val service: AgentCreationService,
    private val store: AgentCreationStore,
    private val machines: StateFlow<List<Machine>>,
    /** Sends the first message as an ordinary one once the hub answered an async start; null keeps it in `prompt`. */
    private val sender: MessageSender? = null,
    /** Lists a started session at once, so its chat has metadata before the next `sessions` push. */
    private val adopt: suspend (Session) -> Unit = {},
    private val prefill: NewAgentPrefill? = null,
    private val now: () -> Instant = Instant::now,
    /** The subscription use of the hub a computer belongs to, for ultracode's quota line (#582); null: none drawn. */
    internal val usage: (suspend (machineId: String) -> List<persona.core.contracts.api.UsageAccount>)? = null,
    /** The hub took an Agent this form started afresh (not a resume): the GitHub star card counts it (#5761). */
    private val launched: () -> Unit = {},
    /** How attachments reach the computer picked (#2321); null: the form offers none (no hub services, tests). */
    private val uploads: NewAgentUploads? = null,
) : ViewModel() {
    private val mutable = MutableStateFlow(NewAgentState())
    val state = mutable.asStateFlow()
    private var machineJob: Job? = null
    private var browseJob: Job? = null
    private var modelsJob: Job? = null
    private var workspacesJob: Job? = null
    private val uploadJobs = mutableMapOf<String, Job>()
    private val uploadLimiter = ComposerUploadLimiter()
    private var prefillPhotos = prefill?.forwardedPhotos.orEmpty()
    // Parsing the route gives its cards new UI IDs. The source URL and its message ID survive that round trip.
    private val ChatLinkContext.photoSource get() = url to msg
    private val photoSources = mutableMapOf<String, Pair<String, String>>()
    init {
        // Whether the folder is in a Git repository, a moment after it stops changing (iOS `loadRepo`): New Agent's
        // worktree switch shows only then, and never for a tab in an existing space.
        viewModelScope.launch {
            // A Paseo or Orca workspace picked asks its engine, which says where its worktree goes (#8856, #17050).
            mutable.map { RepoQuery(it.machineId, it.selection.cwd, it.space, it.worktreeEngine) }
                .distinctUntilChanged().collectLatest { (machine, cwd, space, kind) ->
                mutable.update { it.copy(repo = null) }
                if (space != null || cwd.isBlank() || machines.value.none { it.id == machine && it.online }) return@collectLatest
                delay(400)
                val status = service.worktreeStatus(machine, cwd, kind)
                mutable.update { if (it.machineId == machine && it.selection.cwd == cwd) it.copy(repo = status) else it }
            }
        }
        viewModelScope.launch {
            try {
                val pending = store.pending(service.scope)
                val recents = recentSetups()
                if (pending != null) {
                    discardPrefillExcept(emptySet())
                    val r = pending.request
                    // Another agent's mode and effort are its launcher's own picks (#8261), never Claude Code's.
                    val claude = r.agentType == null
                    val mode = r.permissionMode.orEmpty()
                    val effort = r.effort.orEmpty()
                    // A forwarded start's first message holds its card: the form shows that card again and what was typed after it.
                    val restored = pending.firstMessage ?: r.prompt.orEmpty()
                    // Its forward's card again, and the rest of the first message as it was saved: any other block and
                    // what was typed. Without a forward, the saved message is the prompt as it is.
                    val (blocks, rest) = ForwardMessage.splitAll(restored)
                    val forward = blocks.takeIf { it.isNotEmpty() }?.joinToString("\n") { it.xml() }
                    // A start of something else keeps its first message as it was sent, other blocks included.
                    val typed = if (forward != null) rest else restored
                    mutable.value = NewAgentState(initialized = true, machineId = r.machineId,
                        selection = AgentDefaults(r.cwd, r.launcher, if (claude) mode else "", if (claude) effort else ""),
                        providerMode = if (claude) "" else mode, providerEffort = if (claude) "" else effort, model = r.model.orEmpty(),
                        prompt = typed, context = forward, pending = pending, error = AgentText(R.string.agents_new_error_pending),
                        recentSetups = recents)
                } else {
                    val last = store.lastMachine(service.scope)
                    mutable.update { it.copy(initialized = true, prompt = prefill?.prompt.orEmpty(), context = prefill?.context, recentSetups = recents) }
                    machines.collect { list ->
                        if (state.value.machineId.isNotEmpty()) mutable.update { state ->
                            state.copy(startMachine = list.firstOrNull { it.id == state.machineId }).afterTargetChange(state)
                        }
                        if (state.value.machineId.isEmpty() && !state.value.locked) {
                            val space = state.value.space
                            val project = state.value.project
                            val wanted = prefill?.machineId?.takeIf { it.isNotBlank() }
                            val fixedMachine = space?.machineId ?: project?.machineId
                            val pick = if (fixedMachine != null) list.firstOrNull { it.id == fixedMachine && it.online }
                                else (wanted?.let { id -> list.firstOrNull { it.id == id && it.online } }
                                    ?: list.firstOrNull { it.id == last && it.online } ?: list.firstOrNull { it.online })
                            if (pick != null) {
                                if (space == null && wanted != null && pick.id != wanted) {
                                    mutable.update { it.copy(prefillOffline = list.firstOrNull { m -> m.id == wanted }?.displayName ?: wanted) }
                                }
                                selectMachine(pick.id)
                            }
                        }
                    }
                }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = AgentText(R.string.agents_new_error_record)) } }
        }
    }
    /** Opens the usual form, optionally in a space or with a project's computer and folder selected. */
    fun startIn(space: AgentSpacePreset?, project: AgentProjectFolder? = null) {
        val current = state.value
        if (current.pending != null || current.created != null || current.starting) return
        // A provider's mode and effort are picked each time the form opens (#8261), never kept from the last one.
        mutable.update { it.copy(providerMode = "", providerEffort = "") }
        if (project == null && current.space == space && current.project == null) return
        machineJob?.cancel()
        browseJob?.cancel()
        workspacesJob?.cancel()
        val target = space?.machineId ?: project?.machineId
        mutable.update { it.copy(space = space, project = project, machineId = if (target != null) "" else it.machineId,
            browsing = false, browser = null, loading = false, error = null, workspaces = emptyList(), workspace = null, folderBeforeWorkspace = "",
            inWorktree = it.inWorktree && project == null, worktreeBranch = it.worktreeBranch.takeIf { project == null }.orEmpty(),
            modelConfig = if (project != null || it.project != null) NewAgentModelConfig.Unknown else it.modelConfig) }
        val machine = target ?: current.machineId
        if (current.initialized && machine.isNotEmpty()) selectMachine(machine)
    }
    fun selectMachine(id: String) = load(id)
    /** Picks machine [id] with its remembered defaults, or with [setup]'s fields once its options are read. */
    private fun load(id: String, setup: RecentSetup? = null) {
        if (state.value.locked) return
        val machine = machines.value.firstOrNull { it.id == id && it.online } ?: return
        state.value.space?.let { if (it.machineId != id) return }
        moveAttachments(id)
        machineJob?.cancel()
        browseJob?.cancel()
        modelsJob?.cancel()
        mutable.update { it.copy(machineId = id, startMachine = machine, selection = AgentDefaults(), launchers = emptyList(), unavailable = emptyList(), defaultOpens = null,
            tiers = emptyList(), profile = null, pickedTier = null, recent = emptyList(),
            browser = null, browsing = false, loading = true, error = null, models = NewAgentModels(), model = "", pickedSetup = null,
            workspaces = emptyList(), workspace = null, folderBeforeWorkspace = "", providerMode = "", providerEffort = "") }
        val forwarded = prefillPhotos
        prefillPhotos = emptyList()
        var lost = 0
        for ((ref, photos) in forwarded) {
            lost += photos.lost + attach(photos.attachments, showLimit = false, forward = ref)
        }
        if (lost > 0) notice(AgentText(if (lost == 1) ChatR.string.chat_forward_photos_lost_one
            else ChatR.string.chat_forward_photos_lost_other, lost))
        loadWorkspaces(id)
        machineJob = viewModelScope.launch {
            try {
                val defaults = store.defaults(service.scope, id)
                val project = state.value.project
                val known = state.value.modelConfig.takeIf {
                    project == null && (it is NewAgentModelConfig.Follow || it is NewAgentModelConfig.Unsupported)
                }
                val (listed, dirs, config) = coroutineScope {
                    val l = async { service.launcherList(id) }
                    val d = async { service.directories(id) }
                    // A project may be on a different hub from the one currently selected in Agents.
                    val c = async { known ?: modelConfig(id.takeIf { project != null }) }
                    Triple(l.await(), d.await(), c.await())
                }
                val prefilledCwd = state.value.project?.takeIf { it.machineId == id }?.path
                    ?: prefill?.cwd?.takeIf { prefill.machineId == id && it.isNotBlank() }
                val launchers = listed.launchers
                val launcher = defaultLauncher(config, defaults, launchers)
                mutable.update { it.copy(loading = false, launchers = launchers, unavailable = listed.unavailable,
                    tiers = listed.tiers, profile = listed.tiersProfile, pickedTier = null, defaultOpens = listed.defaultOpens,
                    recent = dirs.recent.take(6), modelConfig = config,
                    selection = defaults.copy(cwd = it.space?.cwd ?: prefilledCwd ?: defaults.cwd.ifEmpty { dirs.recent.firstOrNull()?.path ?: dirs.path },
                        launcher = launcher),
                    model = RecentSetups.model(it.recentSetups, id, launcher),
                    error = (config as? NewAgentModelConfig.Failed)?.let { failed -> AgentText(R.string.agents_new_error_model_config, failed.reason) })
                    .let { next -> when {
                        setup != null -> next.filled(setup)
                        next.ompCli -> next.copy(model = "") // Only an explicitly chosen recent setup reuses its omp model.
                        else -> next
                    } } }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(loading = false, error = AgentText(R.string.agents_new_error_load)) } }
            loadModels()
        }
    }
    /**
     * Reads [machineId]'s catalog workspaces (#7259) once per computer picked; an answer for a computer no longer picked
     * is dropped. A failure, an older hub or a computer without Orca or Paseo workspaces shows no choice; nothing is picked.
     */
    private fun loadWorkspaces(machineId: String) {
        workspacesJob?.cancel()
        if (state.value.space != null) return
        workspacesJob = viewModelScope.launch {
            val rows = try { service.workspaces(machineId) } catch (e: CancellationException) { throw e } catch (_: Exception) { emptyList() }
            val sections = NewAgentWorkspaces.sections(rows, machineId)
            mutable.update { if (it.machineId == machineId && it.space == null) it.copy(workspaces = sections) else it }
        }
    }
    /**
     * Starts in [workspace], one of the rows offered: its folder replaces the one picked until "Choose by folder". The
     * worktree switch goes off, as on the web: one turned on for another choice never carries over (#8856).
     */
    fun pickWorkspace(workspace: persona.core.contracts.api.AgentWorkspace) {
        val current = state.value
        if (current.locked || current.loading || current.space != null || workspace.machineId != current.machineId) return
        browseJob?.cancel()
        mutable.update { it.copy(workspace = workspace, folderBeforeWorkspace = if (it.workspace == null) it.selection.cwd else it.folderBeforeWorkspace,
            selection = it.selection.copy(cwd = workspace.cwd), browsing = false, browser = null, error = null, pickedSetup = null,
            repo = it.repo.takeIf { _ -> workspace.cwd == it.selection.cwd }, inWorktree = false).afterTargetChange(it) }
    }
    /** "Choose by folder": no workspace, the folder picked before it back, and the worktree switch off. */
    fun chooseByFolder() {
        if (state.value.locked) return
        mutable.update {
            if (it.workspace == null) it
            else {
                val cwd = it.folderBeforeWorkspace.ifEmpty { it.selection.cwd }
                it.copy(workspace = null, folderBeforeWorkspace = "", selection = it.selection.copy(cwd = cwd),
                    repo = it.repo.takeIf { _ -> cwd == it.selection.cwd }, inWorktree = false).afterTargetChange(it)
            }
        }
    }
    /** A recent folder: the start goes there, by folder (any workspace picked is no longer, nor its worktree switch). */
    fun useFolder(path: String) {
        if (state.value.locked || state.value.loading) return
        mutable.update { it.copy(workspace = null, folderBeforeWorkspace = "", inWorktree = it.inWorktree && it.workspace == null)
            .afterTargetChange(it) }
        selection(state.value.selection.copy(cwd = path))
    }
    /**
     * Reads the model list of the launcher that will run, unless it is already there (or on its way) for that
     * launcher; [refresh] asks the launcher's CLI again. It never holds up starting: the default stays possible.
     */
    private fun loadModels(refresh: Boolean = false) {
        val current = state.value
        val machine = current.machineId
        val launcher = current.modelLauncher
        val same = current.models.machineId == machine && current.models.launcher == launcher
        if (same && !refresh) return
        modelsJob?.cancel()
        if (machine.isEmpty() || launcher.isEmpty()) {
            mutable.update { it.copy(models = NewAgentModels(machine, launcher)) }
            return
        }
        mutable.update { it.copy(models = NewAgentModels(machine, launcher, list = it.models.list.takeIf { same }, loading = true)) }
        modelsJob = viewModelScope.launch {
            val next = try {
                val nativeOmp = current.nativeOmpLauncher
                // Its catalogue and lock come from herdr, including on a multi-host computer.
                val list = service.launcherModels(machine, launcher, refresh, "herdr".takeIf { nativeOmp })
                NewAgentModels(machine, launcher, list = list)
            } catch (e: CancellationException) { throw e }
            catch (error: Exception) {
                // A previous native capability cannot authorize a pick after this node no longer confirms it.
                val cached = state.value.models.list.takeIf { same && !current.nativeOmpLauncher }
                NewAgentModels(machine, launcher, list = cached, error = failureReasonText(error))
            }
            mutable.update { if (it.machineId == machine && it.modelLauncher == launcher) it.copy(models = next).settled() else it }
        }
    }
    /**
     * A "Recent" card: fills the machine, folder, launcher, model, permission mode and effort from [setup], then asks for
     * the cursor in the first message. It never starts; an offline machine's card, or one in a space's form, does nothing.
     */
    fun useRecent(setup: RecentSetup) {
        val current = state.value
        if (current.locked || current.space != null || machines.value.none { it.id == setup.machine && it.online }) return
        if (current.machineId == setup.machine && !current.loading && current.launchers.isNotEmpty()) {
            browseJob?.cancel()
            mutable.update { it.copy(browsing = false, browser = null, error = null).filled(setup).settled() }
            loadModels()
        } else load(setup.machine, setup)
    }
    /** "Remove from Recent" on a card. */
    fun removeRecent(setup: RecentSetup) = changeRecent { RecentSetups.remove(it, setup) }
    /** "Clear Recent Setups". */
    fun clearRecent() = changeRecent { emptyList() }
    private fun changeRecent(change: (List<RecentSetup>) -> List<RecentSetup>) {
        viewModelScope.launch {
            try {
                val kept = store.updateRecentSetups(service.scope, change)
                mutable.update { it.copy(recentSetups = kept, pickedSetup = it.pickedSetup?.takeIf { picked -> kept.any(picked::same) }) }
            } catch (e: CancellationException) { throw e } catch (_: Exception) { }
        }
    }
    /** The cards are a shortcut: when they can't be read, the form works without them. */
    private suspend fun recentSetups(): List<RecentSetup> = try { store.recentSetups(service.scope) }
        catch (e: CancellationException) { throw e } catch (_: Exception) { emptyList() }
    /**
     * Keeps a start of this form in the recent setups; failing to write them never hides the session it started. Mode
     * and effort only for Claude Code, as on the web: another agent's are picked each time (#8261).
     */
    private suspend fun recordRecent(request: AgentStartRequest) {
        val claude = request.agentType == null
        val setup = RecentSetup(request.machineId, request.cwd, request.launcher, request.model.orEmpty(), request.agentType ?: "claude",
            if (claude) request.permissionMode.orEmpty() else "", if (claude) request.effort.orEmpty() else "",
            now().truncatedTo(ChronoUnit.SECONDS).toString())
        try {
            val kept = store.updateRecentSetups(service.scope) { RecentSetups.record(it, setup) }
            mutable.update { it.copy(recentSetups = kept) }
        } catch (e: CancellationException) { throw e } catch (_: Exception) { }
    }
    /** Asks the launcher's CLI for its models again (the node keeps the list a while). */
    fun reloadModels() {
        if (!state.value.locked) loadModels(refresh = true)
    }
    /** Applies a whole Agent preset; a blocked launcher requires the help sheet's explicit [allowBlocked]. */
    fun pickTier(tier: PickerTier, allowBlocked: Boolean = false) {
        val current = state.value
        if (current.locked || current.loading) return
        val picked = current.tiers.firstOrNull { it.name == tier.name } ?: return
        val launcher = current.launchers.firstOrNull { it.name == picked.launcher } ?: return
        if (launcher.block != null && !allowBlocked) return
        mutable.update {
            it.copy(selection = it.selection.copy(launcher = picked.launcher, effort = picked.effort.orEmpty()),
                providerEffort = picked.effort.orEmpty(), model = picked.model.orEmpty(), pickedTier = picked,
                error = null, pickedSetup = null)
        }
        loadModels()
    }

    /** Picks the model for this start; the default entry, or "", is no pick. */
    fun pickModel(id: String) {
        val current = state.value
        if (current.locked || !current.canPickModel) return
        val entry = current.modelList?.models?.firstOrNull { it.id == id }
        val model = if (entry == null) current.typedModel(id).orEmpty() else entry.id.takeUnless { entry.isDefault }.orEmpty()
        mutable.update { it.copy(model = model, pickedTier = if (model == it.model) it.pickedTier else null,
            error = null, pickedSetup = null).settled() }
    }
    private suspend fun modelConfig(machineId: String?): NewAgentModelConfig = try {
        val purpose = if (machineId == null) service.newAgentModel() else service.newAgentModel(machineId)
        purpose?.let { NewAgentModelConfig.Follow(it) } ?: NewAgentModelConfig.Unsupported
    } catch (e: CancellationException) { throw e }
    catch (error: Exception) { NewAgentModelConfig.Failed(failureReasonText(error)) }
    fun selection(value: AgentDefaults) {
        if (state.value.locked || state.value.loading) return
        // Another launcher has its own models: back to its default, and its list.
        mutable.update { val folder = value.cwd != it.selection.cwd
            val next = it.copy(selection = value, error = null, pickedSetup = null,
                pickedTier = if (value.launcher == it.selection.launcher && value.effort == it.selection.effort) it.pickedTier else null,
                // another folder is a start by folder, not in the workspace picked (#7259)
                workspace = it.workspace.takeUnless { folder }, folderBeforeWorkspace = if (folder) "" else it.folderBeforeWorkspace,
                // another folder: its repository is asked again (the switch never shows one folder's answer for another)
                repo = it.repo.takeIf { _ -> value.cwd == it.selection.cwd })
            (if (next.modelLauncher != it.modelLauncher) next.copy(model = "") else next.afterTargetChange(it)).settled() }
        loadModels()
    }
    fun forwardContext(value: String?) {
        if (state.value.locked) return
        val remaining = ForwardMessage.readAll(value.orEmpty()).mapTo(mutableSetOf()) { it.photoSource }
        discardPrefillExcept(remaining)
        photoSources.filterValues { it !in remaining }.keys.forEach(::drop)
        mutable.update { it.copy(context = value) }
    }

    private fun discardPrefillExcept(remaining: Set<Pair<String, String>>) {
        val (keep, removed) = prefillPhotos.partition { it.ref.photoSource in remaining }
        prefillPhotos = keep
        removed.forEach { handoff -> handoff.photos.attachments.forEach { uploads?.discard?.invoke(it) } }
    }

    fun prompt(value: String) { if (!state.value.locked) mutable.update { it.copy(prompt = value) } }
    fun ultracode(on: Boolean) { if (!state.value.locked && state.value.ultracode != on) mutable.update { it.copy(ultracode = on, error = null) } }
    /** The permission mode for a launcher that lists its own (#8261): "" its own approval, or one it lists. */
    fun providerMode(value: String) {
        if (!state.value.locked && !state.value.loading) mutable.update { it.copy(providerMode = value, error = null, pickedSetup = null) }
    }
    /** The thinking effort for such a launcher: "" its own, or one its model lists. */
    fun providerEffort(value: String) {
        if (!state.value.locked && !state.value.loading) mutable.update {
            it.copy(providerEffort = value, pickedTier = if (value == it.providerEffort) it.pickedTier else null,
                error = null, pickedSetup = null).settled()
        }
    }
    fun inWorktree(on: Boolean) { if (!state.value.locked) mutable.update { it.copy(inWorktree = on, error = null) } }
    fun worktreeBranch(value: String) { if (!state.value.locked) mutable.update { it.copy(worktreeBranch = value) } }
    fun browse(path: String = state.value.selection.cwd) {
        if (state.value.locked || state.value.space != null) return
        browseJob?.cancel()
        val machine = state.value.machineId
        mutable.update { it.copy(browsing = true, browser = null, browsePath = path, loading = true, error = null) }
        browseJob = viewModelScope.launch {
            try {
                val dirs = service.directories(machine, path)
                mutable.update { it.copy(browser = dirs, loading = false) }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(loading = false, error = AgentText(R.string.agents_new_error_browse)) } }
        }
    }
    fun closeBrowser(use: Boolean = false) {
        browseJob?.cancel()
        mutable.update { it.copy(browsing = false, browser = null, loading = false, error = null,
            selection = if (use && it.browser != null) it.selection.copy(cwd = it.browser.path) else it.selection,
            pickedSetup = if (use && it.browser != null) null else it.pickedSetup,
            // a folder chosen in the browser is a start by folder (#7259)
            workspace = if (use && it.browser != null) null else it.workspace,
            folderBeforeWorkspace = if (use && it.browser != null) "" else it.folderBeforeWorkspace).afterTargetChange(it) }
    }
    fun resume(session: Session) {
        val current = state.value
        if (!current.initialized || current.starting || current.created != null) return
        current.pending?.let {
            if (it.request.machineId == session.machineId && it.request.resumeSessionId == session.sessionId) submit()
            else mutable.update { it.copy(error = AgentText(R.string.agents_new_error_other_pending)) }
            return
        }
        if (session.cwd.isBlank() || machines.value.none { it.id == session.machineId && it.online }) {
            mutable.update { it.copy(error = AgentText(R.string.agents_new_error_resume_offline)) }
            return
        }
        machineJob?.cancel()
        browseJob?.cancel()
        mutable.update { it.copy(starting = true, loading = false, error = null) }
        viewModelScope.launch {
            try {
                val type = session.agent ?: "claude"
                val available = service.launchers(session.machineId)
                val launcher = session.launcher?.takeIf { name -> available.any { it.name == name && it.agentType == type } }
                    ?: available.firstOrNull { it.agentType == type && session.launcher.isNullOrBlank() }?.name
                if (launcher == null) {
                    mutable.update { it.copy(error = AgentText(R.string.agents_new_error_launcher_gone)) }
                    return@launch
                }
                execute(AgentStartOperation(UUID.randomUUID().toString(), AgentStartRequest(
                    session.machineId, session.cwd, launcher,
                    agentType = type.takeIf { it != "claude" }, resumeSessionId = session.sessionId,
                )), fresh = true)
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = if (it.pending == null) AgentText(R.string.agents_new_error_resume) else AgentText(R.string.agents_new_error_resume_pending)) } }
            finally { mutable.update { it.copy(starting = false) } }
        }
    }
    fun submit() {
        val form = state.value
        val current = form.copy(startMachine = machines.value.firstOrNull { it.id == form.machineId })
        if (!current.initialized || current.starting || current.loading || current.created != null || current.presetNeedsModels) return
        val previous = current.pending
        val agentType = current.agentType
        if (previous == null && (current.selection.cwd.isBlank() || (current.selection.launcher.isBlank() && !current.following) ||
                current.startMachine?.online != true ||
                (current.space != null && current.space.machineId != current.machineId) ||
                (current.promptRequired && current.prompt.isBlank() && usableForward(current.context) == null) || current.attachmentsHold != null)) return
        mutable.update { it.copy(starting = true, error = null) }
        viewModelScope.launch {
            try {
                // In a worktree of its own (#4532): made first; a failure says why, and nothing starts.
                // From a Paseo or Orca workspace picked (#8856, #17050), its engine makes it from the workspace's folder:
                // Paseo as a workspace of its own, Orca as a child of the one picked.
                val worktree = if (previous == null && current.worktreeOffered && current.inWorktree) try {
                    service.addWorktree(current.machineId, current.selection.cwd,
                        AgentWorktree.nameHint(current.prompt, current.selection.cwd), current.worktreeBranch,
                        current.worktreeEngine)
                } catch (e: CancellationException) { throw e }
                catch (e: Exception) {
                    // A refusal says why in the node's words (git's, for worktree_failed), as iOS does; anything else as usual.
                    val words = e.message?.takeIf { hubCodedFailure(e)?.status == 409 && it.isNotBlank() }
                    mutable.update { it.copy(error = AgentText(R.string.agents_worktree_add_failed, words ?: persona.core.designsystem.operationFeedback(e).text)) }
                    return@launch
                } else null
                val operation = previous ?: newOperation(current, agentType, worktree)
                execute(operation, fresh = previous == null)
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = if (it.pending == null) AgentText(R.string.agents_new_error_save) else AgentText(R.string.agents_new_error_no_result)) } }
            finally { mutable.update { it.copy(starting = false) } }
        }
    }
    /**
     * Claude starts in the background (docs/protocol/protocol.md "New session"): the hub answers with a starting session, which
     * opens at once, and the first message goes as an ordinary message (on screen at once, sent once Claude is ready),
     * its id kept with the operation so it is sent once whatever happens to this process. A Paseo provider's first
     * message goes the same way once the hub answers ([NewAgentState.firstMessageAfterStart], iOS and the web). Codex
     * takes its first message with the start, and so does any start without a sender.
     */
    private fun newOperation(current: NewAgentState, agentType: String, worktree: WorktreeAdded? = null): AgentStartOperation {
        val typed = current.prompt.trim().takeIf { it.isNotEmpty() }
        // A forward goes first, what is typed after it (iOS NewChat's contextBody); the name comes from what is typed.
        val forwarded = usableForward(current.context)
        val async = sender != null && current.firstMessageAfterStart
        // The photos and the uploaded files go with the first message as the composer writes them (#2321); the tab is
        // named from what was typed, as on iOS.
        val first = if (async && current.attachments.isNotEmpty())
            NewAgentAttachments.firstMessage(listOfNotNull(forwarded, current.prompt.takeIf { it.isNotBlank() }).joinToString("\n\n"),
                current.attachments, uploads?.photosOnly?.invoke().orEmpty(), uploads?.filesOnly?.invoke().orEmpty()).takeIf { it.isNotEmpty() }
        else listOfNotNull(forwarded, typed).joinToString("\n\n").takeIf { it.isNotEmpty() }
        // A catalog workspace picked (#7259): the start goes into it, by its engine's id, in its folder; with a worktree
        // made from it (#8856), into the workspace made for the worktree, on the same engine.
        val workspace = current.workspace?.takeIf { current.space == null }
        val workspaceId = current.space?.workspaceId ?: if (worktree != null) worktree.workspaceId.takeIf { workspace != null } else workspace?.sourceId
        // Claude Code's mode and effort (its effort only one the model that will run takes); a launcher that lists its own
        // modes (#8261) takes one of those picked here, its own approval going as none, and an effort its model lists;
        // Codex and the rest take neither.
        val claude = current.claudeModes
        val mode = if (claude) current.selection.mode.takeIf { it.isNotEmpty() }
            else current.providerMode.takeIf { it != "default" && current.providerModes?.contains(it) == true }
        val effort = current.sentEffort
        val request = AgentStartRequest(
            current.machineId, worktree?.path ?: workspace?.cwd ?: current.selection.cwd, current.selection.launcher,
            first.takeUnless { async }, mode, effort,
            workspaceId, agentType.takeIf { it != "claude" }, async = true.takeIf { async },
            nameHint = typed.takeIf { async }, model = current.sentModel,
            ultracode = true.takeIf { current.ultracode && agentType == "claude" },
            host = workspace?.source,
        )
        return AgentStartOperation(UUID.randomUUID().toString(), request,
            firstMessage = first.takeIf { async }, firstMessageId = UUID.randomUUID().toString().takeIf { async && first != null },
            folder = current.selection.cwd.takeIf { worktree != null })
    }
    private suspend fun execute(operation: AgentStartOperation, fresh: Boolean) {
        if (fresh) {
            store.pending(service.scope, operation)
            mutable.update { it.copy(pending = operation) }
        }
        val result = if (fresh) service.start(operation.request, operation.id) else service.operation(operation.id, operation.request.machineId)
        when (result) {
            is AgentStartResult.Created -> {
                require(result.session.machineId == operation.request.machineId && result.session.sessionId.isNotBlank())
                // Recents and defaults keep the folder picked, not the worktree made from it (#4532).
                val request = operation.folder?.let { operation.request.copy(cwd = it) } ?: operation.request
                val session = result.session
                try { adopt(session) } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                // The GitHub star card's count (#5761), once the hub takes it, as on the web: only a start sent now, never
                // a resume, and never a start checked again (it may have counted before the answer went missing).
                if (fresh && operation.request.resumeSessionId == null) launched()
                val first = operation.firstMessage
                val firstId = operation.firstMessageId
                if (first != null && firstId != null) {
                    val queued = try { checkNotNull(sender).send(session.key, first, firstId); true }
                        catch (e: CancellationException) { throw e } catch (_: Exception) { false }
                    if (!queued) {
                        // The record stays: checking again queues the same message under the same id, never a second one.
                        mutable.update { it.copy(error = AgentText(R.string.agents_new_error_first_message)) }
                        return
                    }
                }
                // Recent setups (#2134): a tab in an existing space too, as on iOS, the Mac and the web; a resume is not a setup.
                if (request.resumeSessionId == null) recordRecent(request)
                // A tab in an existing space must not replace the machine's remembered directory.
                if (request.workspaceId == null && request.resumeSessionId == null) {
                    // A Codex (or any non-Claude) start keeps its mode and effort to itself (a provider's are picked each
                    // time, #8261): the machine keeps the ones its next Claude Code starts with, as on iOS and the web (#1898).
                    val claude = request.agentType == null
                    val kept = if (!claude) store.defaults(service.scope, request.machineId) else null
                    store.saveDefaults(service.scope, request.machineId, AgentDefaults(request.cwd, request.launcher,
                        if (claude) request.permissionMode.orEmpty() else kept?.mode.orEmpty(),
                        if (claude) request.effort.orEmpty() else kept?.effort.orEmpty()))
                }
                // Keep the receipt until navigation is acknowledged, including across process death.
                mutable.update { it.copy(created = session) }
            }
            is AgentStartResult.Rejected -> {
                store.pending(service.scope, null)
                // Refused for ultracode (an old node, a CLI that can't take it): the switch goes off, so Retry starts without it.
                val ultra = operation.request.ultracode == true
                mutable.update { it.copy(pending = null, ultracode = it.ultracode && !ultra,
                    error = if (ultra) AgentText(R.string.agents_new_ultracode_failed, result.message.shown) else AgentText.raw(result.message.shown)) }
            }
            AgentStartResult.Pending -> mutable.update { it.copy(error = AgentText(R.string.agents_new_error_starting)) }
            AgentStartResult.Unknown -> mutable.update { it.copy(error = AgentText(R.string.agents_new_error_unknown)) }
        }
    }
    /** Whether this form can take attachments at all: it has a way to the computer and sends Claude's first message itself. */
    val attachable: Boolean get() = uploads != null && sender != null
    /** Whether [machineId]'s hub can take uploads now (none offline or in the demo): the add menu shows only then. */
    fun canUpload(machineId: String): Boolean = machineId.isNotEmpty() && uploads?.service?.invoke(machineId) != null
    /** The hub's image limits for the selected computer and agent (defaults to [ImageLimits.Standard]). */
    fun imageLimits(): ImageLimits = uploads?.imageLimits?.invoke(state.value.machineId, state.value.agentType) ?: ImageLimits.Standard
    /** How many more inline photos fit by count alone. */
    fun photoRoom(): Int = composerPhotoRoom(state.value.attachments, imageLimits())
    /** How many more ordinary files fit; this is separate from inline photos. */
    fun fileRoom(): Int = (NewAgentAttachments.MAX_FILES - state.value.attachments.count { !it.media && it.uploadState != DraftUploadState.Invalid }).coerceAtLeast(0)
    /** Selection room for the photo picker (inline photos plus file slots pictures can spill into). */
    fun photoPickerRoom(): Int = photoRoom() + if (canUpload(state.value.machineId)) fileRoom() else 0
    private val noticeChannel = Channel<AgentText>(Channel.BUFFERED)
    /** What the form's toast says (iOS `model.toast`): a pick not taken, an upload that failed. */
    val notices: Flow<AgentText> = noticeChannel.receiveAsFlow()
    fun notice(text: AgentText) { noticeChannel.trySend(text) }
    /** Picked photos with measured scaled sizes join the form, spilling overflow photos into file attachments. */
    fun attachPhotos(candidates: List<PhotoCandidate>, showLimit: Boolean = true, forward: ChatLinkContext? = null): Int {
        val uploads = uploads
        val current = state.value
        if (uploads == null || candidates.isEmpty() || current.locked || !current.takesAttachments || current.machineId.isEmpty()) {
            candidates.forEach { uploads?.discard?.invoke(it.attachment) }
            return candidates.size
        }
        val limits = imageLimits()
        val plan = planComposerPhotos(current.attachments, candidates, limits, canSpill = canUpload(current.machineId))
        plan.refused.forEach(uploads.discard)
        if (showLimit) emitPhotoPlanNotices(plan, limits)
        if (plan.added.isEmpty()) return plan.refused.size
        forward?.photoSource?.let { source -> plan.added.forEach { photoSources[it.id] = source } }
        mutable.update { it.copy(attachments = it.attachments + plan.added, attachedOn = it.machineId, attachNote = null) }
        plan.added.forEach { upload(it.id) }
        return plan.refused.size
    }
    /** Picked photos and files join the form and go to the computer picked, three at a time. Returns how many did not fit. */
    fun attach(picked: List<persona.feature.chat.ComposerAttachment>, showLimit: Boolean = true, forward: ChatLinkContext? = null): Int {
        val uploads = uploads
        val current = state.value
        if (uploads == null || picked.isEmpty() || current.locked || !current.takesAttachments || current.machineId.isEmpty()) {
            picked.forEach { uploads?.discard?.invoke(it) }
            return picked.size
        }
        val (photos, other) = picked.partition { it.image }
        val limits = imageLimits()
        val photoPlan = if (photos.isNotEmpty()) {
            planComposerPhotos(current.attachments, photos.map { PhotoCandidate(it, it.scaledBytes) }, limits, canSpill = canUpload(current.machineId))
        } else null
        val afterPhotos = current.attachments + photoPlan?.added.orEmpty()
        var files = (NewAgentAttachments.MAX_FILES - afterPhotos.count { !it.media && it.uploadState != DraftUploadState.Invalid }).coerceAtLeast(0)
        val acceptedOther = other.filter { attachment ->
            if (attachment.video) false else (files > 0).also { if (it) files-- }
        }
        val refusedOther = other.filterNot { it in acceptedOther }
        photoPlan?.refused.orEmpty().forEach(uploads.discard)
        refusedOther.forEach(uploads.discard)
        if (showLimit) {
            if (photoPlan != null) emitPhotoPlanNotices(photoPlan, limits)
            if (refusedOther.isNotEmpty()) notice(AgentText(ChatR.string.chat_composer_file_limit))
        }
        val accepted = photoPlan?.added.orEmpty() + acceptedOther
        if (accepted.isEmpty()) return (photoPlan?.refused?.size ?: 0) + refusedOther.size
        forward?.photoSource?.let { source -> accepted.forEach { photoSources[it.id] = source } }
        mutable.update { it.copy(attachments = it.attachments + accepted, attachedOn = it.machineId, attachNote = null) }
        accepted.forEach { upload(it.id) }
        return (photoPlan?.refused?.size ?: 0) + refusedOther.size
    }
    private fun emitPhotoPlanNotices(plan: persona.feature.chat.ComposerPhotoPlan, limits: ImageLimits) {
        if (plan.spilledByCount > 0) notice(
            if (plan.spilledByCount == 1) AgentText(ChatR.string.chat_composer_photos_as_files_one, limits.perMessage)
            else AgentText(ChatR.string.chat_composer_photos_as_files_other, limits.perMessage, plan.spilledByCount)
        )
        if (plan.spilledByBytes > 0) notice(
            if (plan.spilledByBytes == 1) AgentText(ChatR.string.chat_composer_photos_bytes_as_files_one)
            else AgentText(ChatR.string.chat_composer_photos_bytes_as_files_other, plan.spilledByBytes)
        )
        if (plan.refusedByCount > 0) notice(
            if (plan.spilledByCount > 0) AgentText(ChatR.string.chat_composer_file_limit)
            else AgentText(R.string.agents_attach_photo_limit, limits.perMessage)
        )
        if (plan.refusedByBytes > 0) notice(
            if (plan.spilledByBytes > 0) AgentText(ChatR.string.chat_composer_file_limit)
            else AgentText(ChatR.string.chat_composer_photos_too_large)
        )
    }
    /** A failed file stays in the form; Retry goes through the same three-slot pump. */
    fun retryAttachment(id: String) {
        val attachment = state.value.attachments.firstOrNull { it.id == id } ?: return
        if (attachment.error == null || attachment.uploadState == DraftUploadState.Invalid || state.value.locked) return
        mutable.update { current ->
            current.copy(attachments = current.attachments.map {
                if (it.id == id) it.copy(error = null, uploadState = DraftUploadState.Pending) else it
            })
        }
        upload(id)
    }
    /** ✕ on a photo or a file: an upload still going stops. */
    fun removeAttachment(id: String) {
        if (state.value.locked) return
        drop(id)
    }
    /**
     * The form closed without starting (iOS: the sheet's attachments go with it, `cancelUploads`): uploads still going stop
     * and the photos taken for it leave the cache. Nothing once a start is on its way: its first message is written.
     */
    fun closeAttachments() {
        if (state.value.starting || state.value.pending != null) return
        clearAttachments()
    }
    private fun clearAttachments() {
        discardPrefillExcept(emptySet())
        photoSources.clear()
        uploadJobs.values.toList().forEach { it.cancel() }
        uploadJobs.clear()
        val gone = state.value.attachments
        mutable.update { it.copy(attachments = emptyList(), attachedOn = "", attachNote = null) }
        gone.forEach { uploads?.discard?.invoke(it) }
    }
    override fun onCleared() {
        clearAttachments()
        super.onCleared()
    }
    private fun drop(id: String) {
        photoSources.remove(id)
        uploadJobs.remove(id)?.cancel()
        val gone = state.value.attachments.firstOrNull { it.id == id } ?: return
        mutable.update { it.copy(attachments = it.attachments.filterNot { a -> a.id == id }) }
        uploads?.discard?.invoke(gone)
    }
    private fun upload(id: String) {
        val uploads = uploads ?: return
        val current = state.value
        val attachment = current.attachments.firstOrNull { it.id == id } ?: return
        if (attachment.path != null) return
        val machine = current.attachedOn
        uploadJobs.remove(id)?.cancel()
        // The services of the hub group this computer belongs to (bindingsFor): the group's own machine id goes up.
        val service = uploads.service(machine)
        if (service == null) {
            if (attachment.image) {
                // The existing photo UI has no retained failure row; ordinary files do.
                drop(id)
            } else {
                // The picker can return after the hub went away. Keep the document and make Retry explain the offline failure.
                val error = ComposerOperationException(persona.core.contracts.CoreText.of(
                    persona.core.contracts.CoreText.Key.ComposerUploadOffline))
                notice(uploadFailure(attachment.name, error))
                mutable.update { s ->
                    s.copy(attachments = s.attachments.map {
                        if (it.id == id) it.copy(error = uploads.errorText(attachment.name, error), uploadState = DraftUploadState.Failed) else it
                    })
                }
            }
            return
        }
        val job = viewModelScope.launch {
            try {
                val path = uploadLimiter.withSlot {
                    mutable.update { s ->
                        s.copy(attachments = s.attachments.map {
                            if (it.id == id) it.copy(error = null, uploadState = DraftUploadState.Uploading) else it
                        })
                    }
                    val limits = imageLimits()
                    val onStaged: suspend (Long) -> Unit = { size ->
                        mutable.update { s ->
                            s.copy(attachments = s.attachments.map {
                                if (it.id == id) {
                                    if (it.image) {
                                        val staged = size.takeIf { v -> v > 0L }
                                        it.copy(size = staged ?: it.size, scaledBytes = staged ?: it.scaledBytes)
                                    } else it.copy(size = size)
                                } else it
                            })
                        }
                    }
                    uploads.uploadWithLimits?.invoke(service, persona.core.contracts.SessionKey(machine, ""), attachment, limits, onStaged)
                        ?: uploads.upload(service, persona.core.contracts.SessionKey(machine, ""), attachment, onStaged)
                }
                // Another computer picked meanwhile: this one's path is no use there.
                mutable.update { s ->
                    if (s.attachedOn != machine) s
                    else s.copy(attachments = s.attachments.map { if (it.id == id) it.copy(path = path, uploadState = DraftUploadState.Uploaded) else it })
                }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                if (state.value.attachedOn != machine || state.value.attachments.none { it.id == id }) return@launch
                // A failed upload stays as a row with its reason and Retry; its local content is not discarded.
                notice(uploadFailure(attachment.name, e))
                if (attachment.image) {
                    drop(id)
                    return@launch
                }
                mutable.update { s ->
                    s.copy(attachments = s.attachments.map {
                        if (it.id == id) it.copy(error = uploads.errorText(attachment.name, e), uploadState = DraftUploadState.Failed) else it
                    })
                }
            }
        }
        uploadJobs[id] = job
        // An older cancelled job must not unregister its replacement for the same attachment id.
        job.invokeOnCompletion { uploadJobs.remove(id, job) }
    }
    /**
     * Another computer was picked (iOS `computerChanged`): files are on the one picked before, so they come out with a
     * note saying so, and uploads still going stop. Photos stay, as on iOS; here they are paths on a computer too, so
     * they go up again to the new one.
     */
    private fun moveAttachments(to: String) {
        val current = state.value
        val from = current.attachedOn
        if (from.isEmpty() || from == to) return
        uploadJobs.values.toList().forEach { it.cancel() }
        uploadJobs.clear()
        val files = current.attachments.filterNot { it.image }
        val photos = current.attachments.filter { it.image }.map { it.copy(path = null, error = null, uploadState = DraftUploadState.Pending) }
        val name = machines.value.firstOrNull { it.id == from }?.displayName ?: from
        mutable.update { it.copy(attachments = photos, attachedOn = if (photos.isEmpty()) "" else to,
            attachNote = if (files.isNotEmpty()) AgentText(R.string.agents_attach_moved, name) else it.attachNote) }
        files.forEach { uploads?.discard?.invoke(it) }
        photos.forEach { upload(it.id) }
    }
    fun opened() {
        viewModelScope.launch { acknowledge() }
    }
    /** The new chat is open: the start's record goes. For a page about to leave, which takes its scope with it. */
    suspend fun acknowledge() {
        try {
            store.pending(service.scope, null)
            mutable.update { it.copy(pending = null, created = null, prompt = "", context = null, providerMode = "", providerEffort = "") }
            clearAttachments()
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { mutable.update { it.copy(error = AgentText(R.string.agents_new_error_cleanup)) } }
    }
}

/**
 * The form filled from a recent setup on its machine, whose options are read: a launcher it no longer has (or following a
 * configuration this hub can't give) keeps the machine's, and then the model its default. A Codex setup has no mode or
 * effort, so the machine's remembered Claude ones stay.
 */
private fun NewAgentState.filled(setup: RecentSetup): NewAgentState {
    val known = if (setup.launcher.isEmpty()) follow != null else launchers.any { it.name == setup.launcher }
    val launcher = if (known) setup.launcher else selection.launcher
    val claude = setup.agentType == "claude"
    return copy(
        selection = selection.copy(cwd = setup.cwd, launcher = launcher, mode = if (claude) setup.mode else selection.mode,
            effort = if (claude) setup.effort else selection.effort),
        model = if (known) setup.model else "", pickedSetup = setup, pickedTier = null, focusPrompt = focusPrompt + 1,
        // a recent setup starts by its folder (#7259)
        workspace = null, folderBeforeWorkspace = "",
    )
}

/**
 * The launcher a machine starts with: none (follow the "New Agent" configuration) when the hub has one, so a remembered
 * launcher never overrides it (docs/assistant/model-config.md); nothing when that configuration could not be read; otherwise the
 * machine's remembered launcher or its first.
 */
internal fun defaultLauncher(config: NewAgentModelConfig, defaults: AgentDefaults, launchers: List<AgentLauncher>): String = when (config) {
    is NewAgentModelConfig.Follow, is NewAgentModelConfig.Failed -> ""
    // A launcher blocked on this computer is not where the form starts (#4530): the last one used unless it is blocked.
    else -> initialLauncher(defaults.launcher.takeIf { it.isNotEmpty() }, launchers)
}
