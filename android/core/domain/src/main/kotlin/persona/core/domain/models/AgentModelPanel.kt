package persona.core.domain.models

import persona.core.contracts.AgentState
import persona.core.contracts.PaseoAgent
import persona.core.contracts.Session
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.LauncherModels
import persona.core.domain.sort.canChangeEffortOnly
import persona.core.domain.sort.canChangeModes
import persona.core.domain.sort.canSend
import persona.core.domain.sort.hasSource

/** One launcher's model list as read: [read] once an answer came ([list] null from an older hub without lists). */
data class ModelList(
    val read: Boolean = false,
    val list: LauncherModels? = null,
    /** A read is on its way. */
    val loading: Boolean = false,
    /** The last read failed and there is no list to show. */
    val error: String? = null,
)

/** One setup as the pill draws it. */
data class PillSetup(
    /** The engine (claude, codex…). */
    val engine: String,
    /** The provider (launcher) as people read it: its letter tile and words. */
    val provider: String,
    /** The model's name; null while not known. */
    val model: String? = null,
    val effort: String? = null,
)

/** What the pill says: what runs ([setup]), and while the list is read the first time ([loading]) or that failed ([failed]). */
data class PillView(val setup: PillSetup, val loading: Boolean = false, val failed: Boolean = false)

/** A panel's providers and state. */
data class PanelData(val providers: List<PanelProvider>, val state: PanelState)

/** One `POST …/modes` of a pick, in the order [AgentModelPanel.pickPlan] gives. */
sealed interface ModeStep {
    data class Model(val model: String) : ModeStep
    data class Effort(val effort: String) : ModeStep
    /** A switch to another launcher with the user's next message, or the session's own to drop one that waits. */
    data class Launcher(val launcher: String, val from: String, val model: String? = null, val effort: String? = null, val agent: String? = null) : ModeStep
}

/**
 * The model pill and panel of an Agent's chat (#5055, the web's `lib/session-models.ts` and `lib/session-panel.ts`):
 * the computer's launchers are the providers, each with the models its agent lists in its own /model menu, and the setup
 * the session runs now; and what a pick sends. The session's own launcher switches its model at once. On a hub that
 * switches a running Agent to another launcher ([LAUNCHER_SWITCH]), a Claude Code Agent in a terminal may also pick
 * another launcher of its engine: the pick is recorded and the user's next message from the chat carries it out (the agent
 * restarts on that launcher and keeps the conversation). With [ENGINE_SWITCH], eligible terminal Agents can hand off
 * between Claude Code and Codex in the same terminal.
 */
object AgentModelPanel {
    /** The hub switches a running Agent to another launcher (docs/protocol/protocol.md "Switching a running Agent to another provider"). */
    const val LAUNCHER_SWITCH = "launcher_switch"
    const val ENGINE_SWITCH = "engine_switch"

    /** Claude Code's efforts (lib/session-actions.ts `EFFORTS`): what a model nobody describes may take. */
    val CLAUDE_EFFORTS = listOf("low", "medium", "high", "xhigh", "max")

    /** The launcher the session runs on, or the built-in launcher for its engine when none was named. */
    fun launcher(s: Session): String = s.effectiveLauncher

    /** The engine a session runs, as the marks name it: each of Paseo's other providers its own ([launcherEngine]). */
    fun engine(s: Session): String = when {
        s.isCodex -> "codex"
        s.isClaude -> "claude"
        else -> launcherEngine(s.launcher.orEmpty(), s.agent)
    }

    /** An agent name as an engine id: "" (an older hub) is Claude Code. */
    fun engineId(agent: String?): String = agent?.trim()?.lowercase()?.takeIf(String::isNotEmpty) ?: "claude"

    /**
     * The engine launcher [name] runs, from the agent it starts ([agent]): Paseo's catch-all [PaseoAgent] (every provider
     * but Claude Code and Codex) is no engine of its own, so each of its providers is one, by its launcher's name (#8143).
     * The marks and the panel's words then name that provider, never Paseo, and moving to another needs a new
     * conversation, as moving to another engine does.
     */
    fun launcherEngine(name: String, agent: String?): String =
        engineId(agent).let { kind -> if (kind == PaseoAgent && name.isNotEmpty()) name else kind }

    /**
     * What the pill and the panel call the engines that are a provider of their own ([launcherEngine]): the session's
     * own by its agent's name (the provider's label, [Session.agentLabel]), the computer's other ones by their launcher's.
     */
    fun engineNames(s: Session, launchers: List<AgentLauncher>?): Map<String, String> = buildMap {
        launchers.orEmpty().filter { engineId(it.agentType) == PaseoAgent && it.name.isNotEmpty() }.forEach { put(it.name, it.title) }
        if (engineId(s.agent) == PaseoAgent && !s.launcher.isNullOrEmpty()) put(engine(s), s.agentLabel)
    }

    /**
     * Its model can be switched while it runs and takes messages. The node's capabilities.model wins where it says (a
     * runtime that takes a model through `POST …/modes`, #6294; the web's `canSwitchModel`); without it, Claude Code of
     * the user's own (the node types `/model`).
     */
    fun canSwitchModel(s: Session): Boolean {
        if (s.assistant || !s.canSend || s.agentState == AgentState.Offline || !s.isLive) return false
        return s.capabilities?.model ?: (!s.hasSource && s.isClaude)
    }

    /** What the pill and the panel can change now: its model, its effort. Neither: the pill is a label. */
    fun switches(s: Session): Pair<Boolean, Boolean> = canSwitchModel(s) to (s.isLive && (s.canChangeModes || s.canChangeEffortOnly))

    /** Why the model can't be switched right now: the agent works or waits on the user (the node refuses then too). */
    fun busy(s: Session): PanelReason? = PanelReason.Busy.takeIf { s.agentState == AgentState.Working || s.agentState == AgentState.NeedsYou }

    /**
     * Whether [s] may move to another launcher of its engine: on a hub that does it, a Claude Code Agent whose model the app
     * switches. Not a session with a source: the node moves no runtime's session to another launcher; it only picks a model
     * of its own.
     */
    fun switchesLauncher(s: Session, features: Set<String>): Boolean =
        LAUNCHER_SWITCH in features && !s.hasSource && engine(s) == "claude" && canSwitchModel(s)

    /** The handoff needs a live terminal the node drives, excluding assistant, runtime and app-server sessions. */
    fun switchesEngine(s: Session, features: Set<String>): Boolean =
        ENGINE_SWITCH in features && !s.assistant && !s.archived && !s.awaitingFirstMessage && !s.hasSource && s.isLive &&
            s.spaceKind != "codex_app" && s.agentState != AgentState.Offline && !s.paneId.isNullOrBlank() && s.canSend &&
            s.capabilities?.send != false && when (engine(s)) {
                "claude" -> canSwitchModel(s)
                "codex" -> true
                else -> false
            }

    private fun canPickProvider(s: Session, kind: String, features: Set<String>): Boolean =
        if (kind == engine(s)) switchesLauncher(s, features)
        else (kind == "claude" || kind == "codex") && switchesEngine(s, features)

    /** A new model of the already-confirmed target engine is a refinement of the same pending handoff. */
    fun needsEngineConfirmation(s: Session, step: ModeStep.Launcher): Boolean =
        step.agent != null && step.agent != pendingEngine(s)

    fun pendingEngine(s: Session): String? = s.pendingSwitch
        ?.takeIf { it.launcher.isNotEmpty() && it.launcher != launcher(s) }
        ?.agent?.takeIf { it.isNotBlank() && engineId(it) != engine(s) }?.let(::engineId)

    /** The model a launcher runs given none ([AgentModel.isDefault]). */
    fun defaultModel(models: List<AgentModel>): AgentModel? = models.firstOrNull { it.isDefault }

    /** The row of [models] the session runs: the one its model names (the default entry only when no other does); none reported: the default entry. */
    fun currentModelRow(models: List<AgentModel>, model: String?): AgentModel? {
        if (model.isNullOrEmpty()) return defaultModel(models)
        val hits = models.filter { ModelPanel.isCurrentModel(it.id, it.resolved, model, it.aliases) }
        return hits.firstOrNull { !it.isDefault } ?: hits.firstOrNull()
    }

    /** A model id said briefly where no list names it: Claude's as "Opus 5.5", GPT's as their list would, a gateway's path without its vendor. */
    fun modelLabel(id: String): String = persona.core.domain.lines.ModelChanges.replyModelName(id)

    /** A model of a list as a panel row ([listed] false: the model that runs, which its list doesn't name). */
    private fun panelModel(m: AgentModel, engine: String, current: Boolean, s: Session, listed: Boolean = true): PanelModel = PanelModel(
        id = m.id, name = m.label, description = m.description?.takeIf(String::isNotBlank), resolved = m.resolved?.takeIf(String::isNotBlank),
        aliases = m.aliases,
        efforts = when {
            current && s.efforts.isNotEmpty() -> s.efforts
            listed && !m.custom -> m.efforts
            engine == "claude" -> CLAUDE_EFFORTS
            else -> emptyList()
        },
        sessionEffortOnly = current && s.sourceKind == "claude_desktop",
        defaultEffort = m.defaultEffort?.takeIf(String::isNotBlank), custom = m.custom, providerDefault = m.isDefault,
    )

    /** A typed id reaches the agent: Claude Code or Codex, and a launcher that doesn't pass its own `--model`. */
    fun typedModelOffered(engine: String, list: LauncherModels?): Boolean = (engine == "claude" || engine == "codex") && list?.locked == null

    /**
     * The panel's providers and state for [s]: [launchers] the computer's (null before they are read), [listOf] what was
     * read for each, [features] the hub's. The session's own launcher is always there, at its place in the list or first.
     */
    fun panel(s: Session, launchers: List<AgentLauncher>?, listFor: (String) -> ModelList, features: Set<String>): PanelData {
        val own = launcher(s)
        val engine = engine(s)
        val modelSwitch = canSwitchModel(s)
        val switching = switchesLauncher(s, features) || switchesEngine(s, features)
        val all = launchers.orEmpty()
        val names = if (all.any { it.name == own }) all.map { it.name } else listOf(own) + all.map { it.name }
        val ownList = listFor(own).list
        val row = currentModelRow(ownList?.models.orEmpty(), s.model)
        val providers = names.map { name ->
            val e = listFor(name)
            val mine = name == own
            val launcherEntry = all.firstOrNull { it.name == name }
            val kind = if (mine) engine else launcherEngine(name, e.list?.agent?.takeIf(String::isNotEmpty) ?: launcherEntry?.agentType)
            val other = !mine && canPickProvider(s, kind, features)
            var models: List<PanelModel>? = null
            var loading = false
            var error: String? = null
            if (!e.read) {
                if (e.error != null) error = e.error else loading = true
                // the model it runs stays there while its list is read or couldn't be: its Customize still works
                if (mine && !s.model.isNullOrEmpty()) models = listOf(panelModel(AgentModel(s.model!!, modelLabel(s.model!!)), kind, true, s, listed = false))
            } else {
                var list = e.list?.models.orEmpty().map { panelModel(it, kind, mine && it == row, s) }
                // a launcher that fixes its own --model takes no model with a switch: its default entry, only
                if (other && e.list?.locked != null) list = list.filter { it.id == defaultModel(e.list.models)?.id }
                // the model it runs, when the list doesn't name it
                if (mine && row == null && !s.model.isNullOrEmpty())
                    list = list + panelModel(AgentModel(s.model!!, modelLabel(s.model!!)), kind, true, s, listed = false)
                models = list
            }
            val unavailable = when {
                other -> null
                !mine -> if (kind == engine) PanelReason.OtherProvider else PanelReason.OtherEngine(engine, kind)
                !modelSwitch -> PanelReason.ModelFixed(engine)
                else -> null
            }
            PanelProvider(id = name, name = launcherEntry?.title ?: name, engine = kind, models = models, loading = loading, error = error,
                locked = mine && modelSwitch && e.list?.locked != null, unavailable = unavailable,
                typable = (other || (mine && modelSwitch)) && typedModelOffered(kind, e.list))
        }
        val model = row?.id ?: s.model
        val ps = s.pendingSwitch?.takeIf { it.launcher.isNotEmpty() && it.launcher != own }
        val state = PanelState(
            current = model?.takeIf(String::isNotEmpty)?.let { PanelConfig(own, it, s.effort).clean() },
            pending = ps?.let { PanelConfig(it.launcher, it.model?.takeIf(String::isNotEmpty) ?: defaultModel(listFor(it.launcher).list?.models.orEmpty())?.id.orEmpty(), it.effort).clean() },
            // the models that switch at once: the session's own launcher's
            recommended = if (modelSwitch) listOf(own) else emptyList(),
            busy = if (modelSwitch || switching) busy(s) else null,
            note = ps?.let { if (!it.agent.isNullOrEmpty() && engineId(it.agent) != engine) PanelNote.Engine else PanelNote.Pending(providerName(it.launcher, all)) },
        )
        return PanelData(providers, state)
    }

    private fun providerName(name: String, launchers: List<AgentLauncher>) = launchers.firstOrNull { it.name == name }?.title ?: name

    /**
     * What applying [config] sends, in order. On the session's own launcher: a switch that waits is dropped first, then
     * the model, then the effort, each only when it differs from the session's. On another launcher of the engine (a hub
     * with [LAUNCHER_SWITCH], or the other engine with [ENGINE_SWITCH]): one request recording the switch, with the model
     * unless it is that launcher's default entry
     * and the effort when the setup has one; nothing when that very switch waits already. null: it can't be applied here.
     */
    fun pickPlan(
        s: Session, config: PanelConfig, listFor: (String) -> LauncherModels?, features: Set<String>,
        providers: List<AgentLauncher>? = null,
    ): List<ModeStep>? {
        val own = launcher(s)
        if (config.provider != own) {
            val entry = providers?.firstOrNull { it.name == config.provider }
            if (providers != null && entry == null) return null
            return switchPlan(s, config, listFor(config.provider), entry, features)
        }
        val models = listFor(own)?.models.orEmpty()
        val row = models.firstOrNull { it.id == config.model }
        val current = currentModelRow(models, s.model)
        val same = if (row != null) row == current || ModelPanel.isCurrentModel(row.id, row.resolved, s.model, row.aliases) else config.model == s.model
        if (!same && (!canSwitchModel(s) || (row == null && ModelPanel.typedModelId(config.model) == null))) return null
        return buildList {
            if (s.pendingSwitch?.launcher?.let { it.isNotEmpty() && it != own } == true) add(ModeStep.Launcher(own, own))
            if (!same) add(ModeStep.Model(config.model))
            config.effort?.takeIf { it != s.effort }?.let { add(ModeStep.Effort(it)) }
        }
    }

    private fun switchPlan(
        s: Session, config: PanelConfig, list: LauncherModels?, entry: AgentLauncher?, features: Set<String>,
    ): List<ModeStep>? {
        val declared = list?.agent?.takeIf(String::isNotEmpty) ?: entry?.agentType
        val kind = declared?.let { launcherEngine(config.provider, it) } ?: return null
        if (!canPickProvider(s, kind, features) || busy(s) != null) return null
        val row = list?.models?.firstOrNull { it.id == config.model }
        if (row == null && ModelPanel.typedModelId(config.model) == null) return null
        val model = config.model.takeUnless { row?.isDefault == true }
        if (model != null && list?.locked != null) return null
        val step = ModeStep.Launcher(config.provider, launcher(s), model, config.effort, kind.takeUnless { it == engine(s) })
        val ps = s.pendingSwitch
        if (ps != null && ps.launcher == step.launcher && ps.agent?.takeIf(String::isNotEmpty) == step.agent && ps.model?.takeIf(String::isNotEmpty) == step.model &&
            ps.effort?.takeIf(String::isNotEmpty) == step.effort) return emptyList()
        return listOf(step)
    }

    /** What the pill says: the engine, the launcher and the model the session runs (its list's name once read), the effort. */
    fun pill(s: Session, own: ModelList, launchers: List<AgentLauncher>?): PillView {
        val row = currentModelRow(own.list?.models.orEmpty(), s.model)
        val model = row?.label ?: s.model?.takeIf(String::isNotEmpty)?.let(::modelLabel)
        return PillView(PillSetup(engine(s), providerName(launcher(s), launchers.orEmpty()), model, s.effort?.takeIf(String::isNotEmpty)),
            loading = !own.read && own.loading && model == null, failed = !own.read && own.error != null && model == null)
    }

    /**
     * The switch waiting for the next message, as the pill draws it: that launcher, the model picked (its list's name once
     * read; none picked: its default), the effort picked or the one it runs with now. null: none waits.
     */
    fun pendingPill(s: Session, listFor: (String) -> ModelList, launchers: List<AgentLauncher>?, defaultModelName: String): PillSetup? {
        val ps = s.pendingSwitch?.takeIf { it.launcher.isNotEmpty() && it.launcher != launcher(s) } ?: return null
        val models = listFor(ps.launcher).list?.models.orEmpty()
        val row = currentModelRow(models, ps.model)
        val engine = ps.agent?.takeIf(String::isNotEmpty)?.let(::engineId) ?: engine(s)
        val model = row?.label ?: ps.model?.takeIf(String::isNotEmpty)?.let(::modelLabel) ?: defaultModelName
        val effort = ps.effort?.takeIf(String::isNotEmpty) ?: if (engine != engine(s)) row?.defaultEffort else s.effort
        return PillSetup(engine, providerName(ps.launcher, launchers.orEmpty()), model, effort?.takeIf(String::isNotEmpty))
    }

    /** A message refused because the switch it was to carry out first could not be (409 with a `switch_*` code): the switch is dropped. */
    fun switchRefused(status: Int?, code: String?): Boolean = status == 409 && code?.startsWith("switch_") == true
}
