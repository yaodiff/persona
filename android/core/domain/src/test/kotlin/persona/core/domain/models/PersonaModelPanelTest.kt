package persona.core.domain.models

import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.ModelPick
import persona.core.contracts.Session
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.AssistantLaunchers
import persona.core.contracts.api.ConversationSwitchRequest
import persona.core.contracts.api.ConversationSwitchState
import persona.core.contracts.api.LauncherDefault
import persona.core.contracts.api.LauncherModels
import persona.core.contracts.api.UnavailableLauncher
import persona.core.domain.FixtureReader

/**
 * A Persona chat's stored pick and its panel (the web's `lib/assistant-panel.test.ts` and `AssistantModelPill.test.tsx`,
 * #5055): the pick read from `model_pick`, where the setup comes from, the providers with the ones that can't run there,
 * and what a pick keeps.
 */
class PersonaModelPanelTest {
    private val chat = Session(machineId = "mac", sessionId = "c2", assistant = true, agent = "claude", launcher = "claude",
        model = "claude-opus-5-5", effort = "high", status = "idle", state = "idle")
    private val info = AssistantLaunchers(
        launchers = listOf(AgentLauncher("claude"), AgentLauncher("team", displayName = "Team API", model = "sonnet"), AgentLauncher("cx", agentType = "codex")),
        default = LauncherDefault("claude", "opus", "high"),
        unavailable = listOf(UnavailableLauncher("resp", displayName = "Responses API", kind = "api", code = "api_protocol", reason = "Speaks Responses")),
    )
    private val lists = mapOf(
        "claude" to LauncherModels("claude", listOf(AgentModel("default", "Default", resolved = "claude-opus-5-5", isDefault = true),
            AgentModel("opus", "Opus 5.5", resolved = "claude-opus-5-5"), AgentModel("sonnet", "Sonnet 4.5"))),
        "team" to LauncherModels("claude", emptyList()),
    )
    private fun read(name: String) = ModelList(read = true, list = lists[name])
    private fun of(session: Session?, main: Boolean = false, kept: Boolean = true, page: ModelPick? = null) =
        PersonaModelPanel.Chat(session, isMain = main, kept = kept, config = "claude", pagePick = page)

    private val sharedCase = FixtureReader.read("assistant-launchers").single().value
    private val sharedCandidates = sharedCase.getValue("candidates").jsonArray.map {
        JsonCodec.decodeFromJsonElement<AgentLauncher>(it)
    }
    private val sharedAssistantNames = sharedCase.getValue("assistant").jsonArray.map { it.jsonPrimitive.content }
    private val sharedInfo = AssistantLaunchers(
        launchers = sharedCandidates.filter { it.name in sharedAssistantNames },
        default = LauncherDefault("runtime"),
        unavailable = sharedCandidates.filterNot { it.name in sharedAssistantNames }.map {
            UnavailableLauncher(it.name, kind = "launcher", agent = it.agentType, code = "not_main_chat", reason = "Cannot run Assistant")
        },
    )

    @Test fun thePickIsReadFromModelPickWhereTheHubKeepsIt() {
        val picked = chat.copy(modelPick = ModelPick("team", "sonnet", "low"))
        assertEquals(ModelPick("team", "sonnet", "low"), PersonaModelPanel.pick(of(picked)))
        assertEquals(PersonaModelPanel.Setup("team", "sonnet", "low", PersonaModelPanel.Source.Pick), PersonaModelPanel.setup(of(picked), info))
        // an older hub: the page's own pick, never a stale model_pick
        assertNull(PersonaModelPanel.pick(of(picked, kept = false)))
        assertEquals(ModelPick("team"), PersonaModelPanel.pick(of(picked, kept = false, page = ModelPick("team"))))
        // a pick without an effort runs on the chat's
        assertEquals("high", PersonaModelPanel.setup(of(chat.copy(modelPick = ModelPick("team"))), info).effort)
    }

    @Test fun withoutAPickTheMainChatFollowsTheDefaultAndAnotherKeepsItsOwn() {
        assertEquals(PersonaModelPanel.Setup("claude", "opus", "high", PersonaModelPanel.Source.Default), PersonaModelPanel.setup(of(chat, main = true), info))
        assertEquals(PersonaModelPanel.Setup("claude", "claude-opus-5-5", "high", PersonaModelPanel.Source.Own), PersonaModelPanel.setup(of(chat), info))
        // the new-chat page: the default
        assertEquals(PersonaModelPanel.Source.Default, PersonaModelPanel.setup(of(null), info).source)
        // a launcher the list no longer offers: the assistant's
        val gone = chat.copy(launcher = "old", model = "x")
        assertEquals(PersonaModelPanel.Setup("claude", null, "high", PersonaModelPanel.Source.Own), PersonaModelPanel.setup(of(gone), info))
    }

    @Test fun mainDefaultEffortAndModelApplyWithoutChangingOtherChatsOrPicks() {
        val own = info.copy(default = LauncherDefault("claude", "main-model", "low"))
        val main = PersonaModelPanel.setup(of(chat, main = true), own)
        assertEquals("main-model", main.model)
        assertEquals("low", main.effort)
        assertEquals("high", PersonaModelPanel.setup(of(chat), own).effort)
        assertEquals("opus", PersonaModelPanel.setup(of(null), info).model)
        val picked = chat.copy(modelPick = ModelPick("team", "picked", "medium"))
        assertEquals(PersonaModelPanel.Setup("team", "picked", "medium", PersonaModelPanel.Source.Pick),
            PersonaModelPanel.setup(of(picked, main = true), own))
    }

    @Test fun theProvidersEndWithWhatCantRunThere() {
        val setup = PersonaModelPanel.setup(of(chat), info)
        val panel = PersonaModelPanel.panel(setup, info, ::read, typed = true, running = true)
        assertEquals(listOf("claude", "team", "cx", "resp"), panel.providers.map { it.id })
        assertEquals("Team API", panel.providers[1].name)
        // no list to read: the model it is set up with
        assertEquals(listOf("sonnet"), panel.providers[1].models?.map { it.id })
        assertEquals(PanelReason.OtherEngine("claude", "codex"), panel.providers[2].unavailable)
        assertEquals(PanelReason.Hub("Speaks Responses"), panel.providers[3].unavailable)
        assertEquals("Responses API", panel.providers[3].name)
        assertEquals(PanelConfig("claude", "opus", "high"), panel.state.current)
        assertEquals(listOf("claude"), panel.state.recommended)
        assertTrue(panel.providers[0].typable)
        // the new-chat page runs nothing yet: every kind is offered
        assertNull(PersonaModelPanel.panel(setup, info, ::read, typed = true, running = false).providers[2].unavailable)
        // without typed ids on the hub, no "Use …"
        assertTrue(PersonaModelPanel.panel(setup, info, ::read, typed = false).providers.none { it.typable })
    }

    @Test fun aPickThatIsGoneStaysShownOff() {
        val setup = PersonaModelPanel.Setup("old", "x", null, PersonaModelPanel.Source.Pick)
        val first = PersonaModelPanel.panel(setup, info, ::read, typed = false).providers.first()
        assertEquals("old", first.id)
        assertEquals(PanelReason.ProviderGone, first.unavailable)
    }

    @Test fun anUnavailableDefaultKeepsItsNameAndReasonFromTheHub() {
        for (id in listOf("team", "claude")) {
            val unavailableOnly = AssistantLaunchers(
                default = LauncherDefault(id, "sonnet", "high"),
                unavailable = listOf(UnavailableLauncher(id, displayName = "Team API", kind = "api",
                    code = "api_protocol", reason = "Speaks Responses")),
            )
            val setup = PersonaModelPanel.setup(of(chat, main = true), unavailableOnly)
            val panel = PersonaModelPanel.panel(setup, unavailableOnly, ::read, typed = true, running = true)
            val provider = panel.providers.single()
            assertEquals(id, provider.id)
            assertEquals("Team API", provider.name)
            assertEquals(PanelReason.Hub("Speaks Responses"), provider.unavailable)
            assertEquals(PanelConfig(id, "sonnet", "high"), panel.state.current)
            assertTrue(panel.state.recommended.isEmpty())

            val section = ModelPanel.sections(panel.providers, panel.state, PanelView.All, "", PanelPrefs()).single()
            assertEquals(provider, section.provider)
            assertFalse(section.loading)
            assertTrue(section.rows.isEmpty())
        }
    }

    @Test fun whatAPickKeeps() {
        val current = PanelConfig("claude", "opus", "high")
        assertEquals(ModelPick("team", "sonnet"), PersonaModelPanel.pickOf(PanelConfig("team", "sonnet"), lists["team"], current))
        // a launcher's default entry keeps no model
        assertEquals(ModelPick("claude", null, "low"), PersonaModelPanel.pickOf(PanelConfig("claude", "default", "low"), lists["claude"], current))
        // the setup the chat runs now keeps nothing new; an id the hub would refuse neither
        assertNull(PersonaModelPanel.pickOf(current, lists["claude"], current))
        assertNull(PersonaModelPanel.pickOf(PanelConfig("team", "two words"), lists["team"], current))
    }

    @Test fun thePillSaysWhatTheNextMessageRunsOn() {
        val picked = chat.copy(modelPick = ModelPick("team"))
        val setup = PersonaModelPanel.setup(of(picked), info)
        val pill = PersonaModelPanel.pill(of(picked), setup, info, read("team"))
        // no list names it: the model it is set up with, said briefly
        assertEquals(PillSetup("claude", "Team API", "sonnet", "high"), pill.setup)
        val main = PersonaModelPanel.pill(of(chat, main = true), PersonaModelPanel.setup(of(chat, main = true), info), info, read("claude"))
        assertEquals(PillSetup("claude", "Claude Code", "Opus 5.5", "high"), main.setup)
    }

    @Test fun aSavedAliasSelectsTheFoldedRowWithoutADuplicatePick() {
        val folded = AgentModel("claude-opus-5-5", "Opus 5.5", aliases = listOf("opus", "fable", "sonnet", "haiku"),
            efforts = listOf("low", "high"))
        val foldedList = LauncherModels("claude", listOf(folded, AgentModel("claude-sonnet-4-5", "Sonnet 4.5")))
        val picked = chat.copy(modelPick = ModelPick("claude", "opus", "high"))
        val page = of(picked, main = true)
        val setup = PersonaModelPanel.setup(page, info)
        val panel = PersonaModelPanel.panel(setup, info,
            { name -> if (name == "claude") ModelList(read = true, list = foldedList) else read(name) }, typed = true)
        assertEquals(PanelConfig("claude", "claude-opus-5-5", "high"), panel.state.current)
        assertEquals(listOf("claude-opus-5-5", "claude-sonnet-4-5"), panel.providers.first().models?.map { it.id })
        assertEquals(listOf("opus", "fable", "sonnet", "haiku"), panel.providers.first().models?.first()?.aliases)
        val rows = ModelPanel.sections(panel.providers, panel.state, PanelView.All, "", PanelPrefs()).flatMap { it.rows }
        assertEquals(1, rows.count { it.current })
        assertEquals("claude-opus-5-5", rows.single { it.current }.model.id)
        assertNull(PersonaModelPanel.pickOf(rows.single { it.current }.setup, foldedList, panel.state.current))
        assertEquals("Opus 5.5", PersonaModelPanel.pill(page, setup, info, ModelList(read = true, list = foldedList)).setup.model)
    }

    @Test fun sharedFixtureOffersTheSameEligibleChoicesForMainAndNewChats() {
        assertEquals(listOf("runtime", "codex-gw", "team-api"), sharedCandidates.map { it.name })
        val mainChat = chat.copy(launcher = "runtime", agent = "claude", model = null)
        for ((page, running) in listOf(of(null) to false, of(mainChat, main = true) to true)) {
            val setup = PersonaModelPanel.setup(page, sharedInfo)
            val panel = PersonaModelPanel.panel(setup, sharedInfo, ::read, typed = true, running = running)
            val expected = if (running) sharedAssistantNames - "codex-gw" else sharedAssistantNames
            assertEquals(expected, panel.providers.filter { it.unavailable == null }.map { it.id })
            assertEquals(
                if (running) PanelReason.OtherEngine("claude", "codex") else null,
                panel.providers.single { it.id == "codex-gw" }.unavailable,
            )
        }
    }

    @Test fun sharedLegacyPlaceholderMeansTheLaunchersDefaultNotAConcreteModel() {
        val page = of(null)
        val setup = PersonaModelPanel.setup(page, sharedInfo)
        assertEquals(PersonaModelPanel.Setup("runtime", source = PersonaModelPanel.Source.Default), setup)
        val panel = PersonaModelPanel.panel(setup, sharedInfo, ::read, typed = true)
        val runtime = panel.providers.single { it.id == "runtime" }
        assertFalse(runtime.models.orEmpty().any { it.id == "默认" || it.name == "默认" })
        assertNull(panel.state.current)
        assertNull(PersonaModelPanel.pill(page, setup, sharedInfo, read("runtime")).setup.model)
    }

    @Test fun switchNeededDistinguishesSameEngineNativeFromCrossEngineAndRuntimeAdapters() {
        val nativeSession = chat.copy(launcher = "claude", agent = "claude")
        assertFalse(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("claude", source = "builtin"), "claude"))
        assertFalse(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("team", source = "zshrc"), "claude"))
        assertFalse(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("managed", source = "persona"), "claude"))
        assertTrue(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("codex", agentType = "codex", source = "path"), "codex"))
        assertTrue(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("agy", agentType = "agy", source = "agy"), "agy"))
        assertTrue(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("hosted-claude", agentType = "claude", source = "runtime-one"), "claude"))
        assertTrue(PersonaModelPanel.switchNeeded(nativeSession, AgentLauncher("persona_ext_custom", agentType = "claude"), "claude"))

        val runtimeSession = nativeSession.copy(sourceKind = "runtime", sourceInstanceId = "runtime-one")
        assertFalse(PersonaModelPanel.switchNeeded(runtimeSession, AgentLauncher("other", source = "runtime-one"), "claude"))
        assertTrue(PersonaModelPanel.switchNeeded(runtimeSession, AgentLauncher("other", source = "runtime-two"), "claude"))
        assertTrue(PersonaModelPanel.switchNeeded(runtimeSession, AgentLauncher("team", source = "zshrc"), "claude"))

        // Codex threads identified via source_kind without an explicit agent field still require switching to Claude.
        val codexAppSession = chat.copy(launcher = "codex", agent = null, sourceKind = "codex_app_server")
        assertTrue(PersonaModelPanel.switchNeeded(codexAppSession, AgentLauncher("claude", agentType = "claude", source = "builtin"), "claude"))
        assertFalse(PersonaModelPanel.switchNeeded(codexAppSession, AgentLauncher("codex-work", agentType = "codex", source = "path"), "codex"))
        // Engine aliases ("claude-code", "codex-cli") normalize identically to their canonical engine IDs.
        val aliasedClaude = chat.copy(launcher = "claude", agent = "claude-code")
        assertFalse(PersonaModelPanel.switchNeeded(aliasedClaude, AgentLauncher("team", agentType = "claude", source = "zshrc"), "claude"))
    }

    @Test fun switchingCapabilityEnablesOtherEnginesAndSupervisionFiltersIneligibleConnections() {
        val launchers = AssistantLaunchers(
            launchers = listOf(
                AgentLauncher("claude", source = "builtin"),
                AgentLauncher("codex", agentType = "codex", source = "path"),
                AgentLauncher("agy", agentType = "agy", displayName = "Antigravity CLI", source = "agy", assistant = true, supervisionPolicy = false),
                AgentLauncher("opencode", agentType = "opencode", source = "opencode", assistant = true, supervisionPolicy = true),
            ),
            default = LauncherDefault("claude", "opus", "high"),
        )
        val setup = PersonaModelPanel.setup(of(chat, main = true), launchers)

        val noSwitch = PersonaModelPanel.panel(setup, launchers, ::read, typed = true, running = true, switching = false)
        assertEquals(PanelReason.OtherEngine("claude", "codex"), noSwitch.providers.single { it.id == "codex" }.unavailable)
        assertEquals(PanelReason.OtherEngine("claude", "agy"), noSwitch.providers.single { it.id == "agy" }.unavailable)

        val canSwitch = PersonaModelPanel.panel(setup, launchers, ::read, typed = true, running = true, switching = true, supervision = false)
        assertNull(canSwitch.providers.single { it.id == "codex" }.unavailable)
        assertNull(canSwitch.providers.single { it.id == "agy" }.unavailable)
        assertNull(canSwitch.providers.single { it.id == "opencode" }.unavailable)

        val supervised = PersonaModelPanel.panel(setup, launchers, ::read, typed = true, running = true, switching = true, supervision = true)
        assertNull(supervised.providers.single { it.id == "claude" }.unavailable)
        assertNull(supervised.providers.single { it.id == "codex" }.unavailable)
        assertEquals(PanelReason.SupervisionUnavailable, supervised.providers.single { it.id == "agy" }.unavailable)
        assertNull(supervised.providers.single { it.id == "opencode" }.unavailable)
    }

    @Test fun buildSwitchRequestValidatesSegmentsStateAndCarriesSessionFields() {
        val session = chat.copy(permissionMode = "acceptEdits")
        val pick = ModelPick("agy", "gemini-3", "high")
        assertNull(PersonaModelPanel.buildSwitchRequest(session, pick, ConversationSwitchState(enabled = false, epoch = 2), "op-1"))
        assertNull(PersonaModelPanel.buildSwitchRequest(session, pick, ConversationSwitchState(enabled = true, epoch = 0), "op-1"))
        assertNull(PersonaModelPanel.buildSwitchRequest(session, pick, ConversationSwitchState(enabled = true, epoch = 2), ""))
        assertEquals(
            ConversationSwitchRequest(
                operationId = "op-1",
                expectedEpoch = 2,
                sourceMachineId = "mac",
                machineId = "mac",
                launcher = "agy",
                model = "gemini-3",
                effort = "high",
                permissionMode = "acceptEdits",
            ),
            PersonaModelPanel.buildSwitchRequest(session, pick, ConversationSwitchState(enabled = true, epoch = 2), "op-1"),
        )
    }

    @Test fun mainChatAgentMismatchKeepsActualRunningLauncherUntilConfirmedSwitch() {
        val runtimeInfo = AssistantLaunchers(
            launchers = listOf(
                AgentLauncher("claude", source = "builtin"),
                AgentLauncher("hosted-claude", agentType = "claude", source = "runtime-one"),
            ),
            default = LauncherDefault("hosted-claude", "opus", "high"),
        )
        val setup = PersonaModelPanel.setup(of(chat, main = true), runtimeInfo)
        assertEquals(PersonaModelPanel.Setup("claude", "claude-opus-5-5", "high", PersonaModelPanel.Source.Own), setup)
    }

    @Test fun leaderWithFollowsDefaultTracksDefaultWithoutAffectingOrdinaryNonMainChat() {
        val leaderChat = PersonaModelPanel.Chat(chat, isMain = false, kept = true, config = "claude", followsDefault = true)
        val sideChat = PersonaModelPanel.Chat(chat, isMain = false, kept = true, config = "claude", followsDefault = false)
        assertEquals(PersonaModelPanel.Source.Default, PersonaModelPanel.setup(leaderChat, info).source)
        assertEquals(PersonaModelPanel.Source.Own, PersonaModelPanel.setup(sideChat, info).source)
    }
}
