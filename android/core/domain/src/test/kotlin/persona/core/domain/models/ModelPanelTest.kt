package persona.core.domain.models

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** The panel's groups, rows and what it keeps (the web's `lib/model-panel.test.ts`, #5055). */
class ModelPanelTest {
    private val opus = PanelModel("opus", "Opus 5.5", resolved = "claude-opus-5-5", efforts = listOf("low", "medium", "high"), defaultEffort = "high")
    private val sonnet = PanelModel("sonnet", "Sonnet 4.5", efforts = listOf("low", "medium", "high"))
    private val glm = PanelModel("glm-5.3", "GLM 5.3")
    private val claude = PanelProvider("claude", "claude", "claude", listOf(opus, sonnet))
    private val work = PanelProvider("work", "Work", "claude", listOf(glm, PanelModel("opus", "Opus 5.5")), typable = true)
    private val codex = PanelProvider("cx", "cx", "codex", null, unavailable = PanelReason.OtherEngine("claude", "codex"))
    private val state = PanelState(current = PanelConfig("claude", "claude-opus-5-5", "high"), recommended = listOf("claude"))

    private fun sections(view: PanelView = PanelView.All, query: String = "", prefs: PanelPrefs = PanelPrefs(), s: PanelState = state) =
        ModelPanel.sections(listOf(claude, work, codex), s, view, query, prefs)

    @Test fun desktopEffortHasNoEditDestination() {
        val row = sections().first().rows.first()
        assertEquals(PanelRow.EffortEdit.Live, row.effortEdit(live = true))
        assertEquals(PanelRow.EffortEdit.Preference, row.effortEdit(live = false))

        val desktop = row.copy(model = row.model.copy(sessionEffortOnly = true))
        assertEquals(PanelRow.EffortEdit.ReadOnly, desktop.effortEdit(live = true))
        assertEquals(PanelRow.EffortEdit.ReadOnly, desktop.effortEdit(live = false))
    }

    @Test fun allStartsWithRecommendedThenEachProvider() {
        val all = sections()
        assertEquals(listOf("recommended", "provider:work", "provider:cx"), all.map { it.key })
        // the model that runs first (by what its alias stands for), then the rest of the session's own launcher
        assertEquals(listOf("opus", "sonnet"), all[0].rows.map { it.model.id })
        assertTrue(all[0].rows[0].current)
        assertEquals(listOf("glm-5.3", "opus"), all[1].rows.map { it.model.id })
        assertFalse("another launcher's same alias is not what runs", all[1].rows[1].current)
        // a provider that can't take a pick has no rows, only its header and reason
        assertTrue(all[2].rows.isEmpty())
    }

    @Test fun aChatNotStartedHasNoRecommendedGroup() {
        val all = sections(s = state.copy(starting = true))
        assertEquals(listOf("provider:claude", "provider:work", "provider:cx"), all.map { it.key })
    }

    @Test fun aRowStandsForWhatRunsOrItsOwnSetup() {
        val prefs = PanelPrefs().setOverride("claude", "sonnet", PanelOverride(effort = "low"))
        val rows = sections(prefs = prefs).first().rows
        assertEquals(PanelConfig("claude", "opus", "high"), rows[0].setup)
        assertEquals("its override's effort", PanelConfig("claude", "sonnet", "low"), rows[1].setup)
        assertTrue(rows[1].customized)
        // without one: the effort running now when the model takes it
        assertEquals(PanelConfig("claude", "sonnet", "high"), sections().first().rows[1].setup)
        // a model without efforts takes none
        assertNull(sections()[1].rows[0].setup.effort)
    }

    @Test fun aProviderViewAndTheSearch() {
        assertEquals(listOf("provider:work"), sections(PanelView.Provider("work")).map { it.key })
        val found = sections(query = "sonnet").flatMap { it.rows }
        assertEquals(listOf("sonnet"), found.filter { it.kind == PanelRow.Kind.Model }.map { it.model.id })
        // a launcher none of whose models answers offers the words as an id to use
        assertEquals(listOf("work"), found.filter { it.kind == PanelRow.Kind.Typed }.map { it.provider.id })
        // the provider's name answers too
        assertEquals(listOf("glm-5.3", "opus"), sections(query = "work").flatMap { it.rows }.filter { it.kind == PanelRow.Kind.Model }.map { it.model.id })
    }

    @Test fun anIdTypedIsOfferedWhereNoModelAnswersIt() {
        val typed = sections(query = "glm-6-preview").flatMap { it.rows }.filter { it.kind == PanelRow.Kind.Typed }
        assertEquals(listOf("work"), typed.map { it.provider.id })
        assertEquals(PanelConfig("work", "glm-6-preview"), typed.single().setup)
        // a word that names a listed model is a search, not an id
        assertTrue(sections(query = "glm").flatMap { it.rows }.none { it.kind == PanelRow.Kind.Typed })
        // words are no id
        assertNull(ModelPanel.typedModelId("two words"))
        assertNull(ModelPanel.typedModelId("/model"))
        assertEquals("~latest", ModelPanel.typedModelId(" ~latest "))
    }

    @Test fun favoritesAndRecentKeepWholeSetups() {
        var prefs = PanelPrefs().addFavorite(PanelConfig("work", "glm-5.3"), "f1")
        prefs = prefs.addFavorite(PanelConfig("work", "glm-5.3", fast = false), "f2")
        assertEquals("the same setup is saved once", listOf("f1"), prefs.favorites.map { it.uid })
        val all = sections(prefs = prefs)
        assertEquals("favorites", all.first().key)
        assertEquals(listOf("f1"), sections(PanelView.Favorites, prefs = prefs).single().rows.map { it.favorite?.uid })

        prefs = (1..7).fold(prefs) { p, i -> p.pushRecent(PanelConfig("claude", "opus", if (i % 2 == 0) "low" else "high")) }
        assertEquals("one entry per setup, newest first", listOf("high", "low"), prefs.recent.map { it.effort })
        val recent = sections(PanelView.Recent, prefs = prefs).single().rows
        assertTrue("the setup that runs is lit", recent[0].current)
        assertFalse(recent[1].current)
        // a recent setup whose provider is gone is left out
        assertEquals(1, ModelPanel.sections(listOf(work), state, PanelView.Recent, "", PanelPrefs().pushRecent(PanelConfig("claude", "opus"))
            .pushRecent(PanelConfig("work", "glm-5.3"))).single().rows.size)
        assertEquals(PanelPrefs.RECENT_MAX, (1..9).fold(PanelPrefs()) { p, i -> p.pushRecent(PanelConfig("claude", "m$i")) }.recent.size)
    }

    @Test fun overridesComeAndGo() {
        val prefs = PanelPrefs().setOverride("claude", "opus", PanelOverride(effort = "low"))
        assertEquals(PanelOverride(effort = "low"), prefs.overrides[PanelPrefs.overrideKey("claude", "opus")])
        assertTrue(prefs.resetOverride("claude", "opus").overrides.isEmpty())
        assertTrue(prefs.setOverride("claude", "opus", PanelOverride(effort = "")).overrides.isEmpty())
    }

    @Test fun whyARowIsOff() {
        assertNull(ModelPanel.rowBlock(claude, state))
        assertEquals(PanelReason.Busy, ModelPanel.rowBlock(claude, state.copy(busy = PanelReason.Busy)))
        assertEquals(PanelReason.OtherEngine("claude", "codex"), ModelPanel.rowBlock(codex, state))
        assertEquals(PanelReason.Locked, ModelPanel.rowBlock(claude.copy(locked = true), state))
        assertEquals(ModelPanel.Loading, ModelPanel.rowBlock(work.copy(models = null, loading = true), state))
        // what runs can be picked back while a switch waits (that drops the switch), unless the agent is busy
        val waiting = state.copy(pending = PanelConfig("work", "glm-5.3"))
        assertNull(ModelPanel.rowBlock(claude.copy(locked = true), waiting, current = true))
        assertEquals(PanelReason.Busy, ModelPanel.rowBlock(claude, waiting.copy(busy = PanelReason.Busy), current = true))
    }

    @Test fun aModelOrProviderListedTwiceIsOneRow() {
        // Claude Code lists `default` twice when its settings say "model": "default" (#9998): two rows with one key
        // close the app as the panel opens
        val default = PanelModel("default", "Default (recommended)", resolved = "claude-opus-5-5", providerDefault = true)
        val twice = claude.copy(models = listOf(default, opus, sonnet, default.copy(name = "Default")))
        for (s in listOf(state, state.copy(current = PanelConfig("claude", "default")), state.copy(recommended = emptyList()), state.copy(starting = true)))
            for (view in listOf(PanelView.All, PanelView.Provider("claude"))) {
                val all = ModelPanel.sections(listOf(twice, twice), s, view, "", PanelPrefs())
                val keys = all.flatMap { listOf("h:" + it.key) + it.rows.map(PanelRow::key) }
                assertEquals(keys.distinct(), keys)
                assertEquals("the first of them stays", listOf("Default (recommended)"),
                    all.flatMap { it.rows }.filter { it.model.id == "default" }.map { it.model.name })
            }
    }

    @Test fun foldedAliasesSelectSearchAndCanonicalizeOneExistingRow() {
        val folded = PanelModel("claude-opus-5-5", "Opus 5.5", aliases = listOf("opus", "fable"))
        val provider = PanelProvider("claude", "claude", "claude", listOf(folded, sonnet), typable = true)
        val running = PanelState(current = PanelConfig("claude", "OPUS"), recommended = listOf("claude"))
        val rows = ModelPanel.sections(listOf(provider), running, PanelView.All, "", PanelPrefs()).flatMap { it.rows }
        assertEquals(1, rows.count { it.current })
        assertEquals("claude-opus-5-5", rows.single { it.current }.model.id)
        assertTrue(ModelPanel.matches(provider, folded, "FaBlE"))
        val found = ModelPanel.sections(listOf(provider), running, PanelView.All, "fable", PanelPrefs()).flatMap { it.rows }
        assertEquals(listOf("claude-opus-5-5"), found.map { it.model.id })
        assertTrue("a folded alias is not offered again as a typed id", found.none { it.kind == PanelRow.Kind.Typed })

        val recent = ModelPanel.sections(listOf(provider), PanelState(), PanelView.Recent, "",
            PanelPrefs().pushRecent(PanelConfig("claude", "fable"))).single().rows.single()
        assertEquals(PanelConfig("claude", "claude-opus-5-5"), recent.setup)
        val exact = PanelModel("opus", "Canonical Opus")
        val both = provider.copy(models = listOf(folded, exact))
        assertTrue(ModelPanel.runsNow(both, exact, PanelConfig("claude", "opus")))
        assertFalse("an alias never steals another row's exact id", ModelPanel.runsNow(both, folded, PanelConfig("claude", "opus")))
    }

    @Test fun aListBeingReadShowsItsSpinner() {
        val all = ModelPanel.sections(listOf(claude, work.copy(models = null, loading = true)), state, PanelView.All, "", PanelPrefs())
        assertTrue(all.last().loading)
        val failed = ModelPanel.sections(listOf(claude, work.copy(models = null, error = "offline")), state, PanelView.All, "", PanelPrefs())
        assertEquals("provider:work", failed.last().key)
        assertFalse(failed.last().loading)
    }
}
