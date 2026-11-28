package persona.core.domain.models

/**
 * The unified model panel's data (#5055; the web's `lib/model-panel.ts` from #5032, after Cindy's model selector;
 * docs/web/web.md "Model pill and model panel"): the providers a chat can run on and their models, the setup that runs
 * now, and what the panel keeps on the phone (favorites, recent setups, per-model customizations). Each chat kind builds
 * them from its own sources ([AgentModelPanel], [PersonaModelPanel]); the design system's panel draws them.
 */

/** One model of a provider, as the panel shows and picks it. */
data class PanelModel(
    /** What a pick sends (Claude Code's aliases too: opus, sonnet…). */
    val id: String,
    val name: String,
    val description: String? = null,
    /** The concrete model an alias stands for under this provider. */
    val resolved: String? = null,
    /** Other spellings folded into this row; each still selects it. Empty on an older hub. */
    val aliases: List<String> = emptyList(),
    /** Thinking efforts it takes, weakest first; empty: no effort control. */
    val efforts: List<String> = emptyList(),
    /** The effort it runs with when none is set: the slider's "Recommended" stop. */
    val defaultEffort: String? = null,
    /** An id typed before for this provider: listed after its own models. */
    val custom: Boolean = false,
    /** It stands for whatever its provider runs when no model is given (a launcher's default entry). */
    val providerDefault: Boolean = false,
    /** The current Desktop model reports its session value independently of saved choices. */
    val sessionEffortOnly: Boolean = false,
)

/** Why a provider's rows can't be picked here, in the words the UI gives each. */
sealed interface PanelReason {
    /** Another launcher of the engine in a running Agent, on a hub without `launcher_switch`. */
    data object OtherProvider : PanelReason
    /** Another engine: "Switching from [from] to [to] needs a new conversation" (engine ids: claude, codex…). */
    data class OtherEngine(val from: String, val to: String) : PanelReason
    /** The session's own launcher where its agent can't change the model while it runs (a Codex thread). */
    data class ModelFixed(val engine: String) : PanelReason
    /** The launcher fixes its own `--model`. */
    data object Locked : PanelReason
    /** The agent works or waits on the user: picks wait for the turn to end. */
    data object Busy : PanelReason
    /** The chat's provider is not on its computer any more. */
    data object ProviderGone : PanelReason
    /** This connection cannot receive separate task follow-up instructions. */
    data object SupervisionUnavailable : PanelReason
    /** The hub's own words (a provider that can't run Persona chats there). */
    data class Hub(val words: String) : PanelReason
}

/** One provider: a launcher (rc-file alias, PATH wrapper, built-in claude, Persona-managed) or a hosted provider. */
data class PanelProvider(
    /** Launcher name: what a pick sends as the provider. */
    val id: String,
    /** What the rail, the group header and the rows call it. */
    val name: String,
    /** The engine it runs (claude, codex…). */
    val engine: String,
    /** null: not read yet. */
    val models: List<PanelModel>? = null,
    val loading: Boolean = false,
    /** Reading the list failed: the group says so and offers Retry. */
    val error: String? = null,
    /** The launcher fixes its own model: its rows can't be picked. */
    val locked: Boolean = false,
    /** None of its models can be picked here, and why. */
    val unavailable: PanelReason? = null,
    /** It takes a model id typed into the search ("Use “…”"). */
    val typable: Boolean = false,
)

/** A setup: what a pick applies, what a favorite or a recent entry keeps. */
data class PanelConfig(val provider: String, val model: String, val effort: String? = null, val fast: Boolean? = null) {
    /** Without empty parts: no effort rather than "", no Fast rather than false. */
    fun clean(): PanelConfig = PanelConfig(provider, model, effort?.takeIf(String::isNotEmpty), fast?.takeIf { it })

    /** The same setup: provider, model, effort and Fast all alike (an unset part and an empty one are alike). */
    fun same(other: PanelConfig?): Boolean = other != null && provider == other.provider && model == other.model &&
        effort.orEmpty() == other.effort.orEmpty() && (fast == true) == (other.fast == true)
}

/** What the chat runs and may switch to right now. */
data class PanelState(
    /** What runs now; null while unknown. */
    val current: PanelConfig? = null,
    /** A switch accepted for the next message (a launcher switch). */
    val pending: PanelConfig? = null,
    /** Providers whose models switch without a restart: Recommended is the current model, then their other models. */
    val recommended: List<String> = emptyList(),
    /** Why no pick applies right now: rows are off and this is the note on top. */
    val busy: PanelReason? = null,
    /** A warning line on top of the list: a waiting switch's consequence ([PanelNote]). */
    val note: PanelNote? = null,
    /** The choice is for a chat that hasn't started yet: no Recommended group. */
    val starting: Boolean = false,
)

/** The panel's warning line. */
sealed interface PanelNote {
    /** "Switches on your next message: the Agent restarts on [provider] and keeps this conversation". */
    data class Pending(val provider: String) : PanelNote
    /** "Switching engines rebuilds context and may lose content". */
    data object Engine : PanelNote
}

/** Where favorites, recent setups and customizations are kept apart: an Agent's providers are not a Persona chat's. */
enum class PanelScope(val key: String) { Agent("agent"), Assistant("assistant") }

/** A favorite: a copy of a whole setup, told apart by [uid]. */
data class PanelFavorite(val uid: String, val setup: PanelConfig)

/** A model's own setup, changed in its Customize page: what a pick of its row sends instead of the recommended one. */
data class PanelOverride(val effort: String? = null, val fast: Boolean? = null)

/** What the panel keeps on the phone for one scope. */
data class PanelPrefs(
    val favorites: List<PanelFavorite> = emptyList(),
    val recent: List<PanelConfig> = emptyList(),
    val overrides: Map<String, PanelOverride> = emptyMap(),
) {
    /** [config] saved as a copy at the end; an identical setup already saved is not saved twice. */
    fun addFavorite(config: PanelConfig, uid: String): PanelPrefs =
        if (favorites.any { it.setup.same(config) }) this else copy(favorites = favorites + PanelFavorite(uid, config.clean()))

    fun removeFavorite(uid: String): PanelPrefs = copy(favorites = favorites.filterNot { it.uid == uid })

    /** After [config] applied: it moves to the front of Recent (once), [RECENT_MAX] at most. */
    fun pushRecent(config: PanelConfig): PanelPrefs =
        copy(recent = (listOf(config.clean()) + recent.filterNot { it.same(config) }).take(RECENT_MAX))

    /** A Customize edit of [model]'s own setup (a part set to null is dropped; nothing left: no override). */
    fun setOverride(provider: String, model: String, patch: PanelOverride): PanelPrefs {
        val key = overrideKey(provider, model)
        val had = overrides[key]
        val merged = PanelOverride(patch.effort ?: had?.effort, patch.fast ?: had?.fast)
        val kept = PanelOverride(merged.effort?.takeIf(String::isNotEmpty), merged.fast)
        return copy(overrides = if (kept.effort == null && kept.fast == null) overrides - key else overrides + (key to kept))
    }

    /** "Reset to recommended": the model's override goes. */
    fun resetOverride(provider: String, model: String): PanelPrefs = copy(overrides = overrides - overrideKey(provider, model))

    companion object {
        /** Recent keeps this many setups, most recent first. */
        const val RECENT_MAX = 5
        fun overrideKey(provider: String, model: String) = "$provider\n$model"
    }
}

/** What the list shows: one rail cell's view. A search shows All whatever cell is on. */
sealed interface PanelView {
    data object Recent : PanelView
    data object Favorites : PanelView
    data object All : PanelView
    data class Provider(val id: String) : PanelView
}

/** One row of the list. */
data class PanelRow(
    /** Unique in the list. */
    val key: String,
    val kind: Kind,
    val provider: PanelProvider,
    val model: PanelModel,
    /** What picking it sends. */
    val setup: PanelConfig,
    /** A favorite row's favorite. */
    val favorite: PanelFavorite? = null,
    /** The favorite with the same setup (a recent row's star). */
    val starredUid: String? = null,
    /** It runs now: the selected fill. */
    val current: Boolean = false,
    /** A model row with an override of its own. */
    val customized: Boolean = false,
) {
    /** A model of a provider's list (also in Recommended), a favorite's copy, a recent setup, or "Use “…”" for a typed id. */
    enum class Kind { Model, Favorite, Recent, Typed }

    enum class EffortEdit { ReadOnly, Live, Preference }

    fun effortEdit(live: Boolean): EffortEdit = when {
        model.sessionEffortOnly -> EffortEdit.ReadOnly
        live -> EffortEdit.Live
        else -> EffortEdit.Preference
    }

    val reportsSessionEffort: Boolean get() = current && kind == Kind.Model && model.sessionEffortOnly
    val shownEffort: String get() = if (reportsSessionEffort) setup.effort.orEmpty() else ModelPanel.shownEffort(setup, model)
}

data class PanelSection(
    val key: String,
    val kind: Kind,
    val provider: PanelProvider? = null,
    val rows: List<PanelRow>,
    /** The provider's list is still being read: its header and a spinner. */
    val loading: Boolean = false,
) {
    enum class Kind { Favorites, Recent, Recommended, Provider }
}

object ModelPanel {
    /** Whether [model] of [provider] runs now ([current]): the row named exactly what runs when its list has one, else one an alias stands for. */
    fun runsNow(provider: PanelProvider, model: PanelModel, current: PanelConfig?): Boolean {
        if (current == null || current.provider != provider.id) return false
        val want = current.model.lowercase()
        if (provider.models.orEmpty().any { it.id.lowercase() == want }) return model.id.lowercase() == want
        return isCurrentModel(model.id, model.resolved, current.model, model.aliases)
    }

    /** Whether a model ([id], the model it stands for [resolved], or a folded [aliases] spelling) is [current], in any case. */
    fun isCurrentModel(id: String, resolved: String?, current: String?, aliases: List<String> = emptyList()): Boolean {
        val c = current?.lowercase() ?: return false
        return id.lowercase() == c || resolved?.lowercase() == c || aliases.any { it.lowercase() == c }
    }

    private fun takes(model: PanelModel, effort: String?) = effort?.takeIf { it.isNotEmpty() && it in model.efforts }

    /**
     * The setup a model's row stands for, and what picking it sends. The model that runs now shows what runs (its
     * effort, else its override's unless the chat hasn't started); any other its override's effort, else the effort
     * running now when it takes it, else none (its own default). Fast only where the model has it: as it runs, or its override.
     */
    fun rowSetup(provider: PanelProvider, model: PanelModel, state: PanelState, override: PanelOverride?): PanelConfig {
        val current = state.current
        val now = runsNow(provider, model, current)
        val effort = if (now && model.sessionEffortOnly) current?.effort
            else if (now) takes(model, current?.effort) ?: (if (state.starting) null else takes(model, override?.effort))
            else takes(model, override?.effort) ?: takes(model, current?.effort)
        val fast = if (now) current?.fast else override?.fast
        return PanelConfig(provider.id, model.id, effort, fast).clean()
    }

    /** A saved copy as it applies to the model now: an effort it no longer takes is left out. A list not read yet keeps the copy. */
    fun copySetup(copy: PanelConfig, model: PanelModel, listed: Boolean): PanelConfig =
        if (!listed) copy.clean() else PanelConfig(copy.provider, model.id, takes(model, copy.effort), copy.fast).clean()

    /** The effort a setup runs at: its own, else the model's default (the row and the slider show this). */
    fun shownEffort(setup: PanelConfig, model: PanelModel): String = setup.effort ?: model.defaultEffort.orEmpty()

    /**
     * Why a row of [provider] can't be picked now (null: it can): the chat is busy, the provider can't take a pick here,
     * the launcher fixes its own model, or its list is still being read ([Loading]). The model that runs now can always
     * be picked back while a switch waits, unless the chat is busy: that drops the switch.
     */
    fun rowBlock(provider: PanelProvider, state: PanelState, current: Boolean = false): PanelReason? {
        if (current && state.pending != null && state.busy == null) return null
        state.busy?.let { return it }
        provider.unavailable?.let { return it }
        if (provider.locked) return PanelReason.Locked
        if (provider.models == null) return Loading
        return null
    }

    /** [rowBlock]'s answer for a list not read yet: nothing to say, the group shows a spinner. */
    val Loading: PanelReason = PanelReason.Hub("")

    /**
     * Whether a row's Customize opens. Recent rows and "Use “…”" have none. The model that runs now has one whenever the
     * chat applies its edits at once ([live]), even while picks are off; any other row only while it can be picked.
     */
    fun rowCustomizable(row: PanelRow, state: PanelState, live: Boolean): Boolean = when {
        row.kind == PanelRow.Kind.Recent || row.kind == PanelRow.Kind.Typed -> false
        row.kind == PanelRow.Kind.Model && row.current && live -> true
        else -> rowBlock(row.provider, state) == null && row.model.efforts.isNotEmpty()
    }

    /** The search (every word, any case) over the model's names, folded aliases, description and provider's name. */
    fun matches(provider: PanelProvider, model: PanelModel, query: String): Boolean = matchesWords(
        listOfNotNull(model.name, model.id, model.resolved, model.description, provider.name) + model.aliases, query)

    private fun matchesWords(fields: List<String>, query: String): Boolean {
        val words = query.trim().lowercase().split(Regex("""\s+""")).filter(String::isNotEmpty)
        if (words.isEmpty()) return true
        val text = fields.joinToString(" ").lowercase()
        return words.all { it in text }
    }

    private class Resolved(val provider: PanelProvider, val model: PanelModel, val listed: Boolean)

    /** A saved copy's model among the providers: null when its provider is gone, or its read list lacks it (unless it takes typed ids). */
    private fun resolve(providers: List<PanelProvider>, copy: PanelConfig): Resolved? {
        val provider = providers.firstOrNull { it.id == copy.provider } ?: return null
        val models = provider.models ?: return Resolved(provider, PanelModel(copy.model, copy.model), false)
        models.firstOrNull { isCurrentModel(it.id, it.resolved, copy.model, it.aliases) }?.let { return Resolved(provider, it, true) }
        if (provider.typable) return Resolved(provider, PanelModel(copy.model, copy.model, custom = true), false)
        return null
    }

    /**
     * The list for a view (Cindy's buildUnifiedListSections). All: Favorites, Recommended (the model that runs now, then
     * the other models of the providers in [PanelState.recommended], which leave their own group; none for a chat not
     * started yet), then one group per provider (an emptied group is left out; one whose list is being read shows a
     * spinner). A provider's view: its favorites, then its models. Favorites: the favorites only. Recent: the last setups
     * applied. A search looks through every provider, and a typed model id none of whose models answers is offered as
     * "Use “…”" in each provider that takes a pick here and takes typed ids. A provider or a model listed twice (Claude
     * Code lists `default` twice when its settings name it, #9998) is one row, the first: a key used twice in the list
     * closes the app.
     */
    fun sections(providers: List<PanelProvider>, state: PanelState, chosen: PanelView, query: String, prefs: PanelPrefs): List<PanelSection> {
        val q = query.trim()
        val view = if (q.isNotEmpty()) PanelView.All else chosen
        fun inView(p: PanelProvider) = view !is PanelView.Provider || p.id == view.id
        val out = mutableListOf<PanelSection>()

        if (view == PanelView.Recent) {
            val rows = prefs.recent.withIndex().mapNotNull { (i, c) ->
                val r = resolve(providers, c) ?: return@mapNotNull null
                val setup = copySetup(c, r.model, r.listed)
                val starred = prefs.favorites.firstOrNull { f ->
                    f.setup.provider == setup.provider && isCurrentModel(r.model.id, r.model.resolved, f.setup.model, r.model.aliases) &&
                        copySetup(f.setup, r.model, r.listed).same(setup)
                }
                val now = state.current
                val current = now != null && runsNow(r.provider, r.model, now) && setup.effort.orEmpty() == now.effort.orEmpty() &&
                    (setup.fast == true) == (now.fast == true)
                PanelRow("recent:$i", PanelRow.Kind.Recent, r.provider, r.model, setup, starredUid = starred?.uid, current = current)
            }.take(PanelPrefs.RECENT_MAX)
            return if (rows.isEmpty()) emptyList() else listOf(PanelSection("recent", PanelSection.Kind.Recent, rows = rows))
        }

        val favorites = prefs.favorites.mapNotNull { f ->
            val r = resolve(providers, f.setup) ?: return@mapNotNull null
            if (!inView(r.provider) || !matches(r.provider, r.model, q)) return@mapNotNull null
            PanelRow("fav:${f.uid}", PanelRow.Kind.Favorite, r.provider, r.model, copySetup(f.setup, r.model, r.listed), favorite = f)
        }
        if (favorites.isNotEmpty()) out += PanelSection("favorites", PanelSection.Kind.Favorites, rows = favorites)
        if (view == PanelView.Favorites) return out

        val shown = providers.filter(::inView).distinctBy { it.id }
        val groups = LinkedHashMap<String, List<PanelRow>>()
        for (p in shown) {
            val models = p.models ?: continue
            groups[p.id] = models.distinctBy { it.id }.filter { matches(p, it, q) }.map { m ->
                val override = prefs.overrides[PanelPrefs.overrideKey(p.id, m.id)]
                PanelRow("model:${p.id}\n${m.id}", PanelRow.Kind.Model, p, m, rowSetup(p, m, state, override),
                    current = runsNow(p, m, state.current), customized = override != null)
            }
        }

        if (view == PanelView.All && !state.starting) {
            val now = state.current?.let { c -> groups[c.provider]?.firstOrNull { it.current } }
            val promoted = listOfNotNull(now) + shown.filter { it.id in state.recommended }
                .flatMap { p -> groups[p.id].orEmpty().filter { it !== now } }
            if (promoted.isNotEmpty()) {
                out += PanelSection("recommended", PanelSection.Kind.Recommended, rows = promoted)
                val moved = promoted.toSet()
                groups.keys.toList().forEach { id -> groups[id] = groups[id].orEmpty().filterNot { it in moved } }
            }
        }

        val typed = typedModelId(q)
        for (p in shown) {
            if (p.models == null) {
                // a provider that can't take a pick here has no list to wait for; a list that failed says so (not under a search)
                val waiting = p.unavailable == null && !p.locked && (p.loading || p.error == null)
                if (waiting) out += PanelSection("provider:${p.id}", PanelSection.Kind.Provider, p, emptyList(), loading = true)
                else if (q.isEmpty()) out += PanelSection("provider:${p.id}", PanelSection.Kind.Provider, p, emptyList())
                continue
            }
            val rows = groups[p.id].orEmpty().toMutableList()
            // only in a provider that takes a pick here, and only when none of its models answers the search by its own words
            if (typed != null && p.typable && p.unavailable == null && !p.locked &&
                p.models.none { matchesWords(listOfNotNull(it.name, it.id, it.resolved, it.description) + it.aliases, q) }) {
                val model = PanelModel(typed, typed, custom = true)
                rows += PanelRow("typed:${p.id}\n$typed", PanelRow.Kind.Typed, p, model, PanelConfig(p.id, typed))
            }
            if (rows.isNotEmpty()) out += PanelSection("provider:${p.id}", PanelSection.Kind.Provider, p, rows, loading = p.loading)
        }
        return out
    }

    /**
     * What is typed in the search, as a model id: one word of letters, digits and `._:/@[]-` (the hub's
     * `proto.ValidModelID`, never a command or arguments); null for words, blanks and anything else.
     */
    fun typedModelId(query: String): String? = query.trim().takeIf { it.length <= MAX_MODEL_ID && modelId.matches(it) }

    /** `proto.MaxModelIDLen`; the rule's characters are all one byte, so characters count as bytes. */
    private const val MAX_MODEL_ID = 256
    // a letter or digit first (or `~` and then one: a gateway's "latest" alias), then letters, digits and `. _ - / : @ [ ]`
    private val modelId = Regex("""^(?:[A-Za-z0-9]|~[A-Za-z0-9])[A-Za-z0-9._:/@\[\]-]*$""")
}
