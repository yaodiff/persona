package persona.core.domain.models

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.LauncherModels

/**
 * An Agent chat's pill and panel (the web's `lib/session-panel.test.ts`, #5055): the providers, the switch to another
 * launcher only on a hub with `launcher_switch`, what a pick sends and the pending switch the pill and the panel show.
 */
class AgentModelPanelTest {
    private val agent = Session(machineId = "m1", sessionId = "s1", agent = "claude", launcher = "example", model = "claude-opus-5-5",
        effort = "high", status = "idle", state = "idle", paneId = "w1:p1")
    private val launchers = listOf(AgentLauncher("example"), AgentLauncher("work", displayName = "Work"), AgentLauncher("cx", agentType = "codex"))
    private val lists = mapOf(
        "example" to LauncherModels("claude", listOf(AgentModel("default", "Default", resolved = "claude-opus-5-5", isDefault = true),
            AgentModel("opus", "Opus 5.5", resolved = "claude-opus-5-5", efforts = listOf("low", "high")), AgentModel("sonnet", "Sonnet 4.5"))),
        "work" to LauncherModels("claude", listOf(AgentModel("default", "Default", isDefault = true), AgentModel("glm-5.3", "GLM 5.3"))),
        "cx" to LauncherModels("codex", listOf(AgentModel("gpt-5.5", "GPT-5.5"))),
    )
    private fun read(name: String) = ModelList(read = true, list = lists[name])
    private val switching = setOf(AgentModelPanel.LAUNCHER_SWITCH)

    @Test fun desktopEffortComesFromTheSession() {
        val list = LauncherModels("claude", listOf(AgentModel("opus", "Opus", efforts = listOf("low", "high"), defaultEffort = "high")))
        fun shown(effort: String?): String {
            val session = agent.copy(sourceKind = "claude_desktop", model = "opus", paneId = null, effort = effort)
            val panel = AgentModelPanel.panel(session, launchers, { ModelList(read = true, list = list) }, emptySet())
            val provider = panel.providers.first()
            val model = provider.models!!.single()
            assertTrue(model.sessionEffortOnly)
            val setup = ModelPanel.rowSetup(provider, model, panel.state, PanelOverride(effort = "low"))
            return PanelRow("current", PanelRow.Kind.Model, provider, model, setup, current = true).shownEffort
        }
        assertEquals("", shown(null))
        assertEquals("high", shown("high"))
        assertEquals("auto", shown("auto"))
    }

    @Test fun anotherLauncherIsOffWithoutTheSwitch() {
        val panel = AgentModelPanel.panel(agent, launchers, ::read, emptySet())
        assertEquals(listOf("example", "work", "cx"), panel.providers.map { it.id })
        assertNull(panel.providers[0].unavailable)
        assertEquals(PanelReason.OtherProvider, panel.providers[1].unavailable)
        assertEquals(PanelReason.OtherEngine("claude", "codex"), panel.providers[2].unavailable)
        assertEquals("Work", panel.providers[1].name)
        // what runs: the alias row that stands for it, not the default entry
        assertEquals(PanelConfig("example", "opus", "high"), panel.state.current)
        assertEquals(listOf("example"), panel.state.recommended)
        assertNull(AgentModelPanel.pickPlan(agent, PanelConfig("work", "glm-5.3"), { lists[it] }, emptySet()))
    }

    @Test fun withTheSwitchAnotherLauncherOfTheEngineIsAPickForTheNextMessage() {
        val panel = AgentModelPanel.panel(agent, launchers, ::read, switching)
        assertNull(panel.providers[1].unavailable)
        assertTrue(panel.providers[1].typable)
        assertEquals("another engine still needs a new conversation", PanelReason.OtherEngine("claude", "codex"), panel.providers[2].unavailable)
        assertEquals(listOf(ModeStep.Launcher("work", from = "example", model = "glm-5.3", effort = "high")),
            AgentModelPanel.pickPlan(agent, PanelConfig("work", "glm-5.3", "high"), { lists[it] }, switching))
        // its default entry sends no model
        assertEquals(listOf(ModeStep.Launcher("work", from = "example")),
            AgentModelPanel.pickPlan(agent, PanelConfig("work", "default"), { lists[it] }, switching))
        // not for an Agent whose model can't be switched (offline, a Persona chat, Codex)
        assertNull(AgentModelPanel.pickPlan(agent.copy(status = "offline", state = "offline"), PanelConfig("work", "glm-5.3"), { lists[it] }, switching))
        assertFalse(AgentModelPanel.switchesLauncher(agent.copy(assistant = true), switching))
        assertFalse(AgentModelPanel.switchesLauncher(agent.copy(agent = "codex"), switching))
    }

    @Test fun theOtherEngineIsSupportedInBothDirectionsOnlyWithTheFeature() {
        val features = setOf(AgentModelPanel.ENGINE_SWITCH)
        val pick = PanelConfig("cx", "gpt-5.5")
        val claude = AgentModelPanel.panel(agent, launchers, ::read, features)
        assertNull(claude.providers.single { it.id == "cx" }.unavailable)
        assertEquals(PanelReason.OtherProvider, claude.providers.single { it.id == "work" }.unavailable)
        val step = ModeStep.Launcher("cx", "example", "gpt-5.5", agent = "codex")
        assertEquals(listOf(step), AgentModelPanel.pickPlan(agent, pick, { lists[it] }, features, launchers))
        assertTrue(AgentModelPanel.needsEngineConfirmation(agent, step))
        val pending = agent.copy(pendingSwitch = PendingSwitch("cx", "gpt-5.5", agent = "codex"))
        assertFalse(AgentModelPanel.needsEngineConfirmation(pending, step))
        assertEquals(emptyList<ModeStep>(), AgentModelPanel.pickPlan(pending, pick, { lists[it] }, features, launchers))
        assertEquals(PanelNote.Engine, AgentModelPanel.panel(pending, launchers, ::read, features).state.note)
        assertEquals(PillSetup("codex", "cx", AgentModelPanel.modelLabel("gpt-5.5")), AgentModelPanel.pendingPill(pending, ::read, launchers, "Default"))
        assertEquals(listOf(ModeStep.Launcher("example", "example")),
            AgentModelPanel.pickPlan(pending, PanelConfig("example", "opus", "high"), { lists[it] }, features, launchers))

        val codex = agent.copy(agent = "codex", launcher = null, model = "gpt-5.5")
        assertEquals(listOf(ModeStep.Launcher("example", "codex", "opus", agent = "claude")),
            AgentModelPanel.pickPlan(codex, PanelConfig("example", "opus"), { lists[it] }, features, launchers))
        assertNull(AgentModelPanel.pickPlan(codex, PanelConfig("example", "opus"), { lists[it] }, switching, launchers))
    }

    @Test fun unsupportedOrBusySessionsCannotScheduleAHandoff() {
        val features = setOf(AgentModelPanel.ENGINE_SWITCH)
        val codex = agent.copy(agent = "codex", launcher = "cx")
        val pick = PanelConfig("example", "opus")
        val unsupported = listOf(
            codex.copy(assistant = true),
            codex.copy(archived = true),
            codex.copy(awaitingFirstMessage = true),
            codex.copy(spaceKind = "codex_app"),
            codex.copy(paneId = null),
            codex.copy(sourceKind = "codex_app_server"),
            codex.copy(sourceKind = "runtime"),
            codex.copy(state = "offline"),
            codex.copy(capabilities = SessionCapabilities(send = false)),
            codex.copy(state = "working"),
            codex.copy(state = "needs_you"),
        )
        for (session in unsupported) {
            assertNull("unexpected pick for $session", AgentModelPanel.pickPlan(session, pick, { lists[it] }, features, launchers))
        }
        assertNull(AgentModelPanel.pickPlan(agent, PanelConfig("ghost", "gpt-5.5"), { lists["cx"] }, features, launchers))
    }

    @Test fun aLauncherThatFixesItsModelTakesOnlyItsDefault() {
        val locked = lists + ("work" to lists.getValue("work").copy(locked = "--model is set"))
        val panel = AgentModelPanel.panel(agent, launchers, { ModelList(read = true, list = locked[it]) }, switching)
        assertEquals(listOf("default"), panel.providers[1].models?.map { it.id })
        assertNull(AgentModelPanel.pickPlan(agent, PanelConfig("work", "glm-5.3"), { locked[it] }, switching))
    }

    @Test fun theOwnLauncherSwitchesAtOnceInOrder() {
        assertEquals(listOf(ModeStep.Model("sonnet")),
            AgentModelPanel.pickPlan(agent, PanelConfig("example", "sonnet"), { lists[it] }, emptySet()))
        assertEquals(listOf(ModeStep.Model("sonnet"), ModeStep.Effort("low")),
            AgentModelPanel.pickPlan(agent, PanelConfig("example", "sonnet", "low"), { lists[it] }, emptySet()))
        // the model that runs, picked again with its effort: nothing to send
        assertEquals(emptyList<ModeStep>(), AgentModelPanel.pickPlan(agent, PanelConfig("example", "opus", "high"), { lists[it] }, emptySet()))
        // a waiting switch is dropped first, by naming the own launcher
        val waiting = agent.copy(pendingSwitch = PendingSwitch("work", "glm-5.3"))
        assertEquals(listOf(ModeStep.Launcher("example", "example")),
            AgentModelPanel.pickPlan(waiting, PanelConfig("example", "opus", "high"), { lists[it] }, switching))
        // an id typed by hand goes as it is; one the hub would refuse doesn't
        assertEquals(listOf(ModeStep.Model("claude-opus-5-6")),
            AgentModelPanel.pickPlan(agent, PanelConfig("example", "claude-opus-5-6"), { lists[it] }, emptySet()))
        assertNull(AgentModelPanel.pickPlan(agent, PanelConfig("example", "two words"), { lists[it] }, emptySet()))
    }

    @Test fun theSameSwitchAgainSendsNothing() {
        val waiting = agent.copy(pendingSwitch = PendingSwitch("work", "glm-5.3", "high"))
        assertEquals(emptyList<ModeStep>(), AgentModelPanel.pickPlan(waiting, PanelConfig("work", "glm-5.3", "high"), { lists[it] }, switching))
    }

    @Test fun aWaitingSwitchIsThePanelsPendingSetupAndItsNote() {
        val waiting = agent.copy(pendingSwitch = PendingSwitch("work", effort = "max"))
        val state = AgentModelPanel.panel(waiting, launchers, ::read, switching).state
        assertEquals(PanelConfig("work", "default", "max"), state.pending)
        assertEquals(PanelNote.Pending("Work"), state.note)
        val pill = AgentModelPanel.pendingPill(waiting, ::read, launchers, "Default model")
        assertEquals(PillSetup("claude", "Work", "Default", "max"), pill)
        assertNull("its own launcher is no switch", AgentModelPanel.pendingPill(agent.copy(pendingSwitch = PendingSwitch("example")), ::read, launchers, "x"))
    }

    @Test fun picksWaitWhileTheAgentWorks() {
        val working = agent.copy(status = "working", state = "working")
        assertEquals(PanelReason.Busy, AgentModelPanel.panel(working, launchers, ::read, switching).state.busy)
        assertNull(AgentModelPanel.panel(agent, launchers, ::read, switching).state.busy)
    }

    // #6294 (the web's canSwitchModel): a runtime's session switches its model where its node says so, on its own launcher
    // only (POST …/modes {model}, then {effort}); the node moves no runtime's session to another launcher.
    @Test fun aRuntimesSessionSwitchesItsModelWhereItsNodeSays() {
        val paseo = agent.copy(sourceKind = "runtime", paneId = null,
            capabilities = SessionCapabilities(read = true, send = true, interrupt = true, approve = true, model = true))
        assertTrue(AgentModelPanel.canSwitchModel(paseo))
        assertTrue(AgentModelPanel.switches(paseo).first)
        assertFalse(AgentModelPanel.switchesLauncher(paseo, switching))
        assertEquals(listOf(ModeStep.Model("sonnet"), ModeStep.Effort("low")),
            AgentModelPanel.pickPlan(paseo, PanelConfig("example", "sonnet", "low"), { lists[it] }, switching))
        assertNull(AgentModelPanel.pickPlan(paseo, PanelConfig("work", "glm-5.3"), { lists[it] }, switching))
        val panel = AgentModelPanel.panel(paseo, launchers, ::read, switching)
        assertNull(panel.providers.first { it.id == "example" }.unavailable)
        assertEquals(PanelReason.OtherProvider, panel.providers.first { it.id == "work" }.unavailable)
        assertEquals(listOf("example"), panel.state.recommended)
        // The node's no, no sending, not running, or a Persona chat: its model stays.
        assertFalse(AgentModelPanel.canSwitchModel(paseo.copy(capabilities = paseo.capabilities!!.copy(model = false))))
        assertFalse(AgentModelPanel.canSwitchModel(paseo.copy(capabilities = paseo.capabilities!!.copy(send = false))))
        assertFalse(AgentModelPanel.canSwitchModel(paseo.copy(status = "offline", state = "offline")))
        assertFalse(AgentModelPanel.canSwitchModel(paseo.copy(assistant = true)))
        assertEquals(PanelReason.ModelFixed("claude"),
            AgentModelPanel.panel(paseo.copy(capabilities = paseo.capabilities!!.copy(model = false)), launchers, ::read, switching)
                .providers.first { it.id == "example" }.unavailable)
        // An older node says nothing: a session with a source keeps its model, as before; any agent the node says yes for switches.
        assertFalse(AgentModelPanel.canSwitchModel(paseo.copy(capabilities = SessionCapabilities(read = true, send = true))))
        assertTrue(AgentModelPanel.canSwitchModel(paseo.copy(agent = "agy")))
        // Claude Code in a terminal, nothing said: as before.
        assertTrue(AgentModelPanel.canSwitchModel(agent) && AgentModelPanel.switchesLauncher(agent, switching))
    }

    // #8143: Paseo calls every provider but Claude Code and Codex "paseo"; each is an engine of its own, named and marked as
    // itself: the panel never says "paseo", and moving to another provider or to Claude Code needs a new conversation.
    @Test fun eachOfPaseosOtherProvidersIsItsOwnEngine() {
        val caps = SessionCapabilities(read = true, send = true, interrupt = true, approve = true, modes = false, model = true)
        val grok = agent.copy(agent = "paseo", agentLabelHint = "My Grok", launcher = "grok", model = "grok-5", effort = null,
            sourceKind = "runtime", spaceKind = "paseo", paneId = null, capabilities = caps)
        val paseo = listOf(AgentLauncher("claude"), AgentLauncher("grok", agentType = "paseo"), AgentLauncher("kimi", agentType = "paseo"),
            AgentLauncher("codex", agentType = "codex"))
        val models = mapOf("grok" to LauncherModels("paseo", listOf(AgentModel("grok-5", "Grok 5", isDefault = true, efforts = listOf("low", "high")))),
            "kimi" to LauncherModels("paseo", listOf(AgentModel("kimi-k3", "Kimi K3"))))
        assertEquals("grok", AgentModelPanel.engine(grok))
        assertEquals(listOf("claude", "grok", "kimi", "codex"), paseo.map { AgentModelPanel.launcherEngine(it.name, it.agentType) })
        val panel = AgentModelPanel.panel(grok, paseo, { ModelList(read = true, list = models[it]) }, switching)
        assertEquals(listOf("claude", "grok", "kimi", "codex"), panel.providers.map { it.engine })
        assertNull(panel.providers.first { it.id == "grok" }.unavailable)
        assertEquals(PanelReason.OtherEngine("grok", "claude"), panel.providers.first { it.id == "claude" }.unavailable)
        assertEquals(PanelReason.OtherEngine("grok", "kimi"), panel.providers.first { it.id == "kimi" }.unavailable)
        assertEquals(listOf("low", "high"), panel.providers.first { it.id == "grok" }.models?.single()?.efforts)
        assertEquals(PillSetup("grok", "grok", "Grok 5", null), AgentModelPanel.pill(grok, ModelList(read = true, list = models["grok"]), paseo).setup)
        // Its own models and their efforts switch at once.
        assertEquals(listOf(ModeStep.Effort("high")), AgentModelPanel.pickPlan(grok, PanelConfig("grok", "grok-5", "high"), { models[it] }, switching))
        assertNull(AgentModelPanel.pickPlan(grok, PanelConfig("kimi", "kimi-k3"), { models[it] }, switching))
        // What the words call them: its own by its label, the others by their launcher's; Claude Code and Codex keep theirs.
        assertEquals(mapOf("grok" to "My Grok", "kimi" to "kimi"), AgentModelPanel.engineNames(grok, paseo))
        assertEquals(mapOf("grok" to "grok", "kimi" to "kimi"), AgentModelPanel.engineNames(agent, paseo))
        // A row with no launcher at all stays the catch-all, never Claude Code.
        assertEquals("paseo", AgentModelPanel.engine(grok.copy(launcher = null)))
    }

    @Test fun aCodexThreadKeepsItsModel() {
        val codex = agent.copy(agent = "codex", launcher = "cx", model = "gpt-5.5", efforts = listOf("low", "medium", "high"))
        val panel = AgentModelPanel.panel(codex, launchers, ::read, switching)
        assertEquals(PanelReason.ModelFixed("codex"), panel.providers.first { it.id == "cx" }.unavailable)
        assertEquals("its own levels", listOf("low", "medium", "high"), panel.providers.first { it.id == "cx" }.models?.single()?.efforts)
        assertTrue(panel.state.recommended.isEmpty())
    }

    @Test fun thePillNamesWhatRuns() {
        assertEquals(PillView(PillSetup("claude", "example", "Opus 5.5", "high")), AgentModelPanel.pill(agent, read("example"), launchers))
        // before the list: a short form of the id
        assertEquals("Opus 5.5", AgentModelPanel.pill(agent, ModelList(loading = true), null).setup.model)
        val unknown = agent.copy(model = null)
        assertTrue(AgentModelPanel.pill(unknown, ModelList(loading = true), null).loading)
        assertTrue(AgentModelPanel.pill(unknown, ModelList(error = "offline"), null).failed)
    }

    @Test fun aModelTheListLacksStaysListed() {
        val odd = agent.copy(model = "vendor/glm-5.1")
        val own = AgentModelPanel.panel(odd, launchers, ::read, emptySet()).providers.first()
        assertEquals("vendor/glm-5.1", own.models?.last()?.id)
        // while its list is read, what runs is there already
        val reading = AgentModelPanel.panel(agent, launchers, { ModelList(loading = true) }, emptySet()).providers.first()
        assertEquals(listOf("claude-opus-5-5"), reading.models?.map { it.id })
        assertTrue(reading.loading)
    }

    @Test fun aSavedAliasSelectsTheFoldedRowAndSendsNoSecondSwitch() {
        val folded = AgentModel("claude-opus-5-5", "Opus 5.5", aliases = listOf("opus", "fable"), efforts = listOf("low", "high"))
        val own = LauncherModels("claude", listOf(folded, AgentModel("sonnet", "Sonnet 4.5")))
        val saved = agent.copy(model = "opus")
        val panel = AgentModelPanel.panel(saved, launchers, { name -> if (name == "example") ModelList(read = true, list = own) else read(name) }, emptySet())
        assertEquals(PanelConfig("example", "claude-opus-5-5", "high"), panel.state.current)
        assertEquals(listOf("claude-opus-5-5", "sonnet"), panel.providers.first().models?.map { it.id })
        assertEquals(emptyList<ModeStep>(), AgentModelPanel.pickPlan(saved,
            PanelConfig("example", "claude-opus-5-5", "high"), { if (it == "example") own else lists[it] }, emptySet()))
        assertEquals("Opus 5.5", AgentModelPanel.pill(saved, ModelList(read = true, list = own), launchers).setup.model)

        val waiting = saved.copy(pendingSwitch = PendingSwitch("work", "fable"))
        assertEquals("Opus 5.5", AgentModelPanel.pendingPill(waiting, { ModelList(read = true, list = own) }, launchers, "Default")?.model)
    }

    @Test fun switchRefusalsAreTheSwitches() {
        assertTrue(AgentModelPanel.switchRefused(409, "switch_failed"))
        assertFalse(AgentModelPanel.switchRefused(409, "input_draft"))
        assertFalse(AgentModelPanel.switchRefused(400, "switch_stale"))
    }
}
