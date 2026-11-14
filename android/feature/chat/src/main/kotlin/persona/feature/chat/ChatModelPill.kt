package persona.feature.chat

import android.content.Context
import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.SnapshotStateMap
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.serialization.json.*
import persona.core.contracts.ModelPick
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AssistantLaunchers
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.LauncherModels
import persona.core.designsystem.*
import persona.core.domain.models.AgentModelPanel
import persona.core.domain.models.ModeStep
import persona.core.domain.models.ModelList
import persona.core.domain.models.PanelConfig
import persona.core.domain.models.PanelFavorite
import persona.core.domain.models.PanelNote
import persona.core.domain.models.PanelOverride
import persona.core.domain.models.PanelPrefs
import persona.core.domain.models.PanelReason
import persona.core.domain.models.PanelScope
import persona.core.domain.models.PersonaModelPanel
import persona.core.domain.models.PillSetup
import persona.core.domain.models.PillView

/**
 * What a chat's model pill and panel read (#5055, the web's `lib/session-models.ts`): the computers' launchers and each
 * launcher's /model list, kept for the page's life so a later opening shows them at once and reads them again behind,
 * at most three lists at a time (each may start the agent's CLI on the computer).
 */
internal class ModelLists(private val scope: CoroutineScope, private val errorText: (Exception) -> String) {
    private val reads = Semaphore(3)
    private val lists: SnapshotStateMap<String, ModelList> = mutableStateMapOf()
    private val running = mutableSetOf<String>()
    /** A computer's launchers (`agent.new`), by machine; absent until read. */
    val launchers: SnapshotStateMap<String, List<AgentLauncher>> = mutableStateMapOf()
    /** What a computer runs Persona chats on (`?purpose=assistant`), by machine; absent until read. */
    val assistant: SnapshotStateMap<String, AssistantLaunchers> = mutableStateMapOf()

    fun list(machine: String, launcher: String): ModelList = lists["$machine/$launcher"] ?: ModelList()

    /** Reads a launcher's list once, or [again] behind the one shown (an opening, Retry). */
    fun read(machine: String, launcher: String, again: Boolean = false, fetch: suspend () -> LauncherModels?) {
        val key = "$machine/$launcher"
        val had = lists[key]
        if (key in running || (had?.read == true && !again)) return
        running += key
        lists[key] = (had ?: ModelList()).copy(loading = true)
        scope.launch {
            try { lists[key] = ModelList(read = true, list = reads.withPermit { fetch() }) }
            catch (cancelled: CancellationException) { throw cancelled }
            // a list already shown stays: a newer one just didn't come
            catch (error: Exception) { lists[key] = if (had?.read == true) had.copy(loading = false) else ModelList(error = errorText(error)) }
            finally { running -= key }
        }
    }

    fun <T> readMachine(into: SnapshotStateMap<String, T>, machine: String, fetch: suspend () -> T, then: () -> Unit = {}) {
        val key = "m:$machine:" + System.identityHashCode(into)
        if (key in running) return
        running += key
        scope.launch {
            try { into[machine] = fetch(); then() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* the ones shown stay; with none, each group says it is loading */ }
            finally { running -= key }
        }
    }
}

/**
 * Favorites, Recent and per-model setups of one [scope], kept on this phone (the web keeps them in the browser,
 * `persona.modelPanel.<base>.agent.*`): the hub has no store for such preferences, so they don't follow you to another device.
 */
internal class PanelPrefsStore(context: Context, private val scope: PanelScope) {
    private val preferences = context.applicationContext.getSharedPreferences("persona.modelPanel", Context.MODE_PRIVATE)

    fun read(): PanelPrefs = runCatching { decode(preferences.getString(scope.key, null)) }.getOrNull() ?: PanelPrefs()
    fun write(prefs: PanelPrefs) { preferences.edit().putString(scope.key, encode(prefs).toString()).apply() }

    companion object {
        private fun config(c: PanelConfig) = buildJsonObject {
            put("provider", c.provider); put("model", c.model)
            c.effort?.let { put("effort", it) }
            c.fast?.let { put("fast", it) }
        }
        private fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull
        private fun configOf(o: JsonObject) = PanelConfig(o.text("provider").orEmpty(), o.text("model").orEmpty(), o.text("effort"),
            (o["fast"] as? JsonPrimitive)?.booleanOrNull)

        fun encode(p: PanelPrefs): JsonObject = buildJsonObject {
            put("favorites", JsonArray(p.favorites.map { f -> JsonObject(config(f.setup) + ("uid" to JsonPrimitive(f.uid))) }))
            put("recent", JsonArray(p.recent.map(::config)))
            put("overrides", JsonObject(p.overrides.mapValues { (_, o) -> buildJsonObject { o.effort?.let { put("effort", it) }; o.fast?.let { put("fast", it) } } }))
        }

        fun decode(raw: String?): PanelPrefs {
            // what an older or damaged copy left is no reason to lose the panel: it starts empty
            val o = raw?.let { runCatching { JsonCodecs.json.parseToJsonElement(it) }.getOrNull() as? JsonObject } ?: return PanelPrefs()
            return PanelPrefs(
                favorites = (o["favorites"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }
                    .mapNotNull { f -> f.text("uid")?.let { PanelFavorite(it, configOf(f)) } }.filter { it.setup.provider.isNotEmpty() },
                recent = (o["recent"] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let(::configOf) }.filter { it.provider.isNotEmpty() },
                overrides = (o["overrides"] as? JsonObject).orEmpty().mapValues { (_, v) ->
                    val e = v as? JsonObject
                    PanelOverride(e?.text("effort"), (e?.get("fast") as? JsonPrimitive)?.booleanOrNull)
                },
            )
        }
    }
}

private object JsonCodecs { val json = Json { ignoreUnknownKeys = true } }

@Composable
internal fun rememberPanelPrefs(scope: PanelScope): MutableState<PanelPrefs> {
    val context = LocalContext.current
    val store = remember(context, scope) { PanelPrefsStore(context, scope) }
    val state = remember(store) { mutableStateOf(store.read()) }
    LaunchedEffect(store) { snapshotFlow { state.value }.collect { store.write(it) } }
    return state
}

/** An engine's mark (claude, codex…) as the pill and the rows draw it. */
internal fun engineMark(engine: String): AgentMarkSpec = when (engine) {
    "claude" -> AgentMarkSpec(AgentMarkKind.Claude, "Claude Code", null)
    "codex" -> AgentMarkSpec(AgentMarkKind.Codex, "Codex", null)
    "trae", "trae-cli", "traecli" -> AgentMarkSpec(AgentMarkKind.Trae, "TRAE", null)
    else -> AgentMarkSpec(null, engine, null)
}

/** An effort as the pill says it: Very high is "Extra" there (the web's pill-only word), the rest their names. */
internal fun pillEffort(id: String, res: Resources): String =
    if (id == "xhigh") res.getString(R.string.chat_modelpill_effort_xhigh_short) else SessionModes.effortLabel(id, res) ?: id

/** The panel's words of a chat: why rows are off, the efforts' and engines' names, the waiting switch's note. */
internal fun panelWords(res: Resources): ModelPanelWords = ModelPanelWords(
    reason = { reason ->
        when (reason) {
            PanelReason.OtherProvider -> res.getString(R.string.chat_modelpanel_other_provider)
            is PanelReason.OtherEngine -> res.getString(R.string.chat_modelpanel_other_engine, engineMark(reason.from).name, engineMark(reason.to).name)
            is PanelReason.ModelFixed -> res.getString(R.string.chat_modelpanel_model_fixed, engineMark(reason.engine).name)
            PanelReason.Locked -> res.getString(R.string.chat_modelpanel_locked)
            PanelReason.Busy -> res.getString(R.string.chat_modelpanel_busy)
            PanelReason.ProviderGone -> res.getString(R.string.chat_modelpill_provider_gone)
            is PanelReason.Hub -> reason.words
        }
    },
    effort = { SessionModes.effortLabel(it, res) ?: it },
    engine = { engineMark(it).name },
    note = { note ->
        when (note) {
            is PanelNote.Pending -> res.getString(R.string.chat_modelpanel_pending_note, note.provider)
            PanelNote.Engine -> res.getString(R.string.chat_modelpanel_engine_note)
        }
    },
    mark = { engineMark(it) },
)

/** The pill's content and spoken words for a setup ([pending]: the next message's, while a switch waits). */
internal fun pillContent(view: PillView, pending: PillSetup?, ultracode: Boolean, res: Resources): Pair<ModelPillContent, String> {
    val shown = pending ?: view.setup
    val model = shown.model
    val content = ModelPillContent(shown.provider, model, engineMark(shown.engine), shown.effort?.let { pillEffort(it, res) },
        ultracode = ultracode && pending == null, pending = pending != null,
        loading = res.getString(R.string.chat_modelpill_loading).takeIf { view.loading && pending == null },
        failed = view.failed && pending == null)
    val effort = shown.effort?.let { SessionModes.effortLabel(it, res) ?: it }
    val loading = content.loading
    val spoken = when {
        loading != null -> loading
        content.failed -> res.getString(R.string.chat_modelpill_failed)
        pending != null -> if (effort != null) res.getString(R.string.chat_modelpill_spoken_pending_effort, pending.provider, model.orEmpty(), effort)
            else res.getString(R.string.chat_modelpill_spoken_pending, pending.provider, model.orEmpty())
        effort != null -> res.getString(R.string.chat_modelpill_spoken_effort, model ?: shown.provider, effort)
        else -> res.getString(R.string.chat_modelpill_spoken, model ?: shown.provider)
    }
    return content to spoken
}

/**
 * A switch drawn at once on this page (a pick recorded, or dropped with [switch] null) until the session's next push
 * ([rev] the row's revision when it was drawn) says what the hub has.
 */
internal data class DrawnSwitch(val switch: PendingSwitch?, val rev: Long?)

/** [session] as drawn: a switch this page drew stands until the row moves past it. */
internal fun DrawnSwitch?.over(session: Session): Session =
    if (this == null || session.rev != rev) session else session.copy(pendingSwitch = switch)

/** The user's next message from the chat carries out a launcher switch waiting on [session] ([on]: where the pill shows it). */
internal fun switchesNext(session: Session?, on: Boolean): Boolean = on && session != null &&
    session.pendingSwitch?.launcher?.let { it.isNotEmpty() && it != AgentModelPanel.launcher(session) } == true

/**
 * An Agent chat's model pill and its panel (#5055, the web's `ModelPill` and `lib/session-panel.ts`; Glass): what runs,
 * or the switch the next message carries out; a tap opens the panel. A pick on the session's own launcher switches its
 * model at once (`POST …/modes {model}` then `{effort}`); on a hub with `launcher_switch` a Claude Code Agent may pick a
 * model of another launcher of its engine, which is recorded for the user's next message ([onSwitch] draws it at once).
 * The flyout's effort of the model that runs applies at once, and ultracode is there too.
 */
@Composable
internal fun AgentModelPill(
    session: Session,
    state: ChatChromeState,
    creation: AgentCreationService?,
    online: Boolean,
    width: ModelPillWidth,
    onSwitch: (DrawnSwitch) -> Unit,
    onOverlay: (Boolean) -> Unit,
    openRequested: Boolean = false,
    onOpenHandled: () -> Unit = {},
    onConfigure: (() -> Unit)? = null,
) {
    val res = LocalResources.current
    val features = LocalHubFeatures.current
    val scope = rememberCoroutineScope()
    val lists = remember(creation) { ModelLists(scope) { operationFeedback(it).text.resolve(res) } }
    val machine = session.machineId
    val own = AgentModelPanel.launcher(session)
    var open by remember { mutableStateOf(false) }
    var picking by remember { mutableStateOf(false) }
    var prefs by rememberPanelPrefs(PanelScope.Agent)
    LaunchedEffect(openRequested) { if (openRequested) { open = true; onOpenHandled() } }
    LaunchedEffect(open) { onOverlay(open) }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    LaunchedEffect(machine, own, creation) { creation?.let { c -> lists.read(machine, own) { c.launcherModels(machine, own) } } }
    // the launcher a waiting switch moves to: its list names the model the next message runs on
    val switchTo = session.pendingSwitch?.launcher?.takeIf { it.isNotEmpty() && it != own }
    LaunchedEffect(machine, switchTo, creation) { val to = switchTo ?: return@LaunchedEffect; creation?.let { c -> lists.read(machine, to) { c.launcherModels(machine, to) } } }
    LaunchedEffect(open, creation, machine) {
        val c = creation ?: return@LaunchedEffect
        if (!open) return@LaunchedEffect
        // each opening reads every list again behind the ones shown, the session's own first
        lists.read(machine, own, again = true) { c.launcherModels(machine, own) }
        lists.readMachine(lists.launchers, machine, { c.launchers(machine) }) {
            lists.launchers[machine].orEmpty().filter { it.name != own }.forEach { l -> lists.read(machine, l.name, again = true) { c.launcherModels(machine, l.name) } }
        }
    }
    val launchers = lists.launchers[machine]
    val view = AgentModelPanel.pill(session, lists.list(machine, own), launchers)
    val pending = AgentModelPanel.pendingPill(session, { lists.list(machine, it) }, launchers, res.getString(R.string.chat_modelpill_default_model))
    val (content, spoken) = pillContent(view, pending, Ultracode.on(session), res)
    val (model, effort) = AgentModelPanel.switches(session)
    val editable = online && state.available && (model || effort)
    JModelPill(content, spoken, if (model || effort) ({ open = true }) else null, Modifier.testTag("model-pill"), width = width, open = open,
        enabled = editable && !picking)
    if (!open) return
    val panel = AgentModelPanel.panel(session, launchers, { lists.list(machine, it) }, features)
    fun step(s: ModeStep) = when (s) {
        is ModeStep.Model -> ChatAction.Modes(model = s.model)
        is ModeStep.Effort -> ChatAction.Modes(effort = s.effort)
        is ModeStep.Launcher -> ChatAction.Modes(launcher = s.launcher, from = s.from, model = s.model, effort = s.effort)
    }
    fun run(steps: List<ModeStep>, done: (Boolean) -> Unit) {
        if (steps.isEmpty()) return done(true)
        if (!state.available) return done(false)
        state.run(session, step(steps.first())) { ok -> if (ok) run(steps.drop(1), done) else done(false) }
    }
    var ultracodeAsk by remember(session.key) { mutableStateOf<Ultracode.Ask?>(null) }
    val latest by rememberUpdatedState(session)
    LaunchedEffect(session, ultracodeAsk) { ultracodeAsk?.let { ask -> if (Ultracode.spent(session, ask)) ultracodeAsk = null } }
    JModelPanel(panel.providers, panel.state, prefs, { prefs = it }, remember(res) { panelWords(res) },
        onPick = { config ->
            val plan = AgentModelPanel.pickPlan(session, config, { lists.list(machine, it).list }, features) ?: return@JModelPanel
            picking = true
            run(plan) { ok ->
                picking = false
                if (!ok) return@run
                prefs = prefs.pushRecent(config)
                open = false
                plan.filterIsInstance<ModeStep.Launcher>().firstOrNull()?.let { s ->
                    if (s.launcher == s.from) onSwitch(DrawnSwitch(null, session.rev))
                    else {
                        onSwitch(DrawnSwitch(PendingSwitch(s.launcher, s.model, s.effort), session.rev))
                        val target = launchers.orEmpty().firstOrNull { it.name == s.launcher }?.title ?: s.launcher
                        val modelName = s.model?.let { id -> lists.list(machine, s.launcher).list?.models?.firstOrNull { it.id == id }?.label ?: AgentModelPanel.modelLabel(id) }
                        state.notice = res.getString(R.string.chat_modelpanel_switch_scheduled, listOfNotNull(target, modelName).joinToString(" · "))
                    }
                }
            }
        },
        onDismiss = { open = false }, picking = picking,
        onApplyCurrent = if (effort && editable) ({ e -> state.run(session, ChatAction.Modes(effort = e)) }) else null,
        currentExtra = if (Ultracode.offered(session, features)) ({
            UltracodeSwitchRow(session, ultracodeAsk, editable) { on ->
                ultracodeAsk = Ultracode.Ask(on)
                state.run(session, ChatAction.Modes(ultracode = on)) { ok -> ultracodeAsk = if (ok) Ultracode.Ask(on, sending = false, answered = latest) else null }
            }
        }) else null,
        onReload = { provider -> creation?.let { c -> lists.read(machine, provider, again = true) { c.launcherModels(machine, provider) } } },
        onConfigure = onConfigure)
}

/**
 * A pick this page drew for a Persona chat ([pick] null: dropped) until the session's next push ([rev] the row's
 * revision when it was drawn) says what the hub kept.
 */
internal data class DrawnPick(val pick: ModelPick?, val rev: Long?)

/**
 * A Persona chat's model pill and its panel (#5055, the web's `AssistantModelPill` and `AssistantModelPanel` from #5033;
 * Glass): what the chat's next message runs on and, in the panel's strip, where that comes from. On a hub with
 * `assistant_model_pick` a pick is kept for the conversation (`PUT …/model`, drawn at once, put back with the hub's
 * words if it says no) and Follow default / Undo pick drops it (`DELETE`); on an older hub the pick lives on this page
 * ([pagePick]) and goes with each message. [session] null: the new-chat page, whose pick goes with the start.
 */
@Composable
fun PersonaModelPill(
    session: Session?,
    machineId: String,
    management: AssistantManagement?,
    width: ModelPillWidth,
    pagePick: ModelPick?,
    onPagePick: (ModelPick?) -> Unit,
    onNotice: (String) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    onOverlay: (Boolean) -> Unit = {},
    onConfigure: (() -> Unit)? = null,
    /** An effort on an older hub, which keeps it on the session (`POST …/modes`); null: not offered. */
    onEffort: ((String) -> Unit)? = null,
) {
    val res = LocalResources.current
    val features = LocalHubFeatures.current
    val scope = rememberCoroutineScope()
    val lists = remember(management) { ModelLists(scope) { operationFeedback(it).text.resolve(res) } }
    var open by remember { mutableStateOf(false) }
    var prefs by rememberPanelPrefs(PanelScope.Assistant)
    var config by remember(management) { mutableStateOf<persona.core.contracts.api.AssistantConfig?>(null) }
    var drawn by remember(session?.key) { mutableStateOf<DrawnPick?>(null) }
    LaunchedEffect(open) { onOverlay(open) }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    LaunchedEffect(management) {
        try { config = management?.config() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
    }
    LaunchedEffect(management, machineId, open, session?.rev) {
        val api = management ?: return@LaunchedEffect
        if (machineId.isEmpty()) return@LaunchedEffect
        // A hub push that bumps the conversation (rev) re-reads even while the panel is closed, so the pill keeps
        // matching the effective Main chat model after a hub-side change (#6542). With no rev to watch, read once and
        // let opening the panel refresh it; the in-flight guard in readMachine drops a duplicate read.
        if (!open && session?.rev == null && lists.assistant[machineId] != null) return@LaunchedEffect
        lists.readMachine(lists.assistant, machineId, { api.assistantLauncherList(machineId) })
    }
    val kept = PersonaModelPanel.MODEL_PICK in features
    val shownSession = session?.let { s -> drawn?.takeIf { it.rev == s.rev }?.let { s.copy(modelPick = it.pick) } ?: s }
    val chat = PersonaModelPanel.Chat(shownSession, isMain = session != null && config?.main == session.sessionId, kept = kept,
        config = config?.launcher, pagePick = pagePick)
    // Only the main conversation has a per-conversation model pick (#6911): a leader, the background supervisor or
    // another assistant conversation draws no pill and follows the default or what it started with. The new-chat page
    // (session null) keeps its starter pick, which goes with the main conversation's start.
    if (session != null && !chat.isMain) return
    val info = lists.assistant[machineId]
    val setup = PersonaModelPanel.setup(chat, info)
    LaunchedEffect(management, machineId, setup.provider) {
        val api = management ?: return@LaunchedEffect
        if (machineId.isNotEmpty()) lists.read(machineId, setup.provider) { api.launcherModels(machineId, setup.provider) }
    }
    LaunchedEffect(open, management, machineId, info) {
        val api = management ?: return@LaunchedEffect
        if (!open) return@LaunchedEffect
        info?.launchers.orEmpty().forEach { l -> lists.read(machineId, l.name, again = true) { api.launcherModels(machineId, l.name) } }
    }
    val view = PersonaModelPanel.pill(chat, setup, info, lists.list(machineId, setup.provider))
    val (content, spoken) = pillContent(view, null, false, res)
    JModelPill(content, spoken, if (management != null) ({ open = true }) else null, modifier.testTag("model-pill"), width = width, open = open,
        enabled = enabled)
    if (!open) return
    val running = session?.agent?.isNotBlank() == true
    val panel = PersonaModelPanel.panel(setup, info, { lists.list(machineId, it) }, typed = PersonaModelPanel.TYPED_MODELS in features,
        engine = session?.let(AgentModelPanel::engine) ?: "claude", running = running)
    fun keep(pick: ModelPick?, after: () -> Unit = {}) {
        val s = session
        if (s == null || !kept) { onPagePick(pick); after(); return }
        val before = drawn
        drawn = DrawnPick(pick, s.rev)
        scope.launch {
            try {
                if (pick == null) management?.clearModelPick(s.key) else management?.setModelPick(s.key, pick)
                after()
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { drawn = before; onNotice(operationFeedback(error).text.resolve(res)) }
        }
    }
    val named = listOfNotNull(info?.launchers?.firstOrNull { it.name == setup.provider }?.title ?: setup.provider,
        panel.providers.firstOrNull { it.id == setup.provider }?.models?.firstOrNull { it.id == panel.state.current?.model }?.name ?: view.setup.model)
        .joinToString(" · ")
    JModelPanel(panel.providers, panel.state, prefs, { prefs = it }, remember(res) { panelWords(res) },
        onPick = { picked ->
            val pick = PersonaModelPanel.pickOf(picked, lists.list(machineId, picked.provider).list, panel.state.current)
            if (pick == null) { open = false; return@JModelPanel }
            prefs = prefs.pushRecent(picked)
            keep(pick)
            // an older hub keeps the effort on the session (what the effort chip did)
            if ((!kept || session == null) && pick.effort != null && pick.effort != session?.effort) onEffort?.invoke(pick.effort!!)
            open = false
        },
        onDismiss = { open = false },
        header = { PickStrip(chat, setup, named) { keep(null) } },
        onApplyCurrent = panel.state.current?.let { now -> { effort: String ->
            PersonaModelPanel.pickOf(now.copy(effort = effort), lists.list(machineId, now.provider).list, null)?.let { pick ->
                if (session != null && kept) keep(pick) else { onPagePick(pick); onEffort?.invoke(effort) }
            }
            Unit
        } },
        onReload = { provider -> management?.let { api -> lists.read(machineId, provider, again = true) { api.launcherModels(machineId, provider) } } },
        onConfigure = onConfigure)
}

/** The strip on top of a Persona chat's panel (the web's `PickStrip`): where the setup comes from and, for a pick, the way back. */
@Composable
private fun PickStrip(chat: PersonaModelPanel.Chat, setup: PersonaModelPanel.Setup, named: String, onBack: () -> Unit) {
    val palette = PersonaTheme.palette
    val text = when {
        chat.session == null -> stringResource(R.string.chat_modelpill_header_new, named)
        setup.source == PersonaModelPanel.Source.Pick ->
            stringResource(if (chat.kept) R.string.chat_modelpill_header_picked else R.string.chat_modelpill_header_picked_page, named)
        setup.source == PersonaModelPanel.Source.Default -> stringResource(R.string.chat_modelpill_header_default, named)
        else -> stringResource(R.string.chat_modelpill_header_own, named)
    }
    // the main chat and the new one go back to the default; another chat's pick is undone (it then keeps what it runs)
    val back = stringResource(if (chat.session != null && !chat.isMain) R.string.chat_modelpill_undo_pick else R.string.chat_modelpill_follow_default)
    Column(Modifier.fillMaxWidth().testTag("pick-strip")) {
        Row(Modifier.fillMaxWidth().padding(horizontal = pagePadding, vertical = Tokens.Space.xs),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(text, Modifier.weight(1f), style = PersonaTheme.type.micro, color = palette.ink2)
            if (setup.source == PersonaModelPanel.Source.Pick) JTextButton(back, onBack)
        }
        JDivider()
    }
}
