package persona.feature.agents

import persona.core.designsystem.matches
import androidx.lifecycle.viewModelScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.io.IOException

/** "New Agent"'s model picker: the list of the launcher that will run, the pick sent once, efforts it takes. */
/** Robolectric for the strings: Simplified Chinese, the language the expected text is in. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class NewAgentModelTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    private val machines = MutableStateFlow(listOf(Machine("first", online = true), Machine("second", online = true)))
    private val all = listOf("low", "medium", "high", "xhigh", "max")
    private val claude = LauncherModels("claude", listOf(
        AgentModel("default", "Default (recommended)", resolved = "claude-opus-5-5", isDefault = true, efforts = all),
        AgentModel("opus", "Opus 5.5", resolved = "claude-opus-5-5", efforts = all),
        AgentModel("sonnet", "Sonnet 5.5", resolved = "claude-sonnet-5-5", efforts = listOf("low", "medium", "high", "max")),
        AgentModel("haiku", "Haiku 5.5", resolved = "claude-haiku-5-5"),
        AgentModel("gateway/glm-5.3", "glm-5.3", description = "From gateway", efforts = listOf("low", "medium", "high")),
    ))
    private val codex = LauncherModels("codex", listOf(
        AgentModel("gpt-5.5", "GPT-5.5", isDefault = true, efforts = listOf("low", "medium", "high"), defaultEffort = "medium"),
        AgentModel("gpt-5.5-mini", "GPT-5.5-Mini", efforts = listOf("low", "medium")),
    ))

    private fun test(block: suspend TestScope.() -> Unit) = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        try { block() } finally { Dispatchers.resetMain() }
    }

    private fun service() = Service().apply {
        available = listOf(AgentLauncher("claude"), AgentLauncher("sonnetcc"), AgentLauncher("codex", agentType = "codex"), AgentLauncher("traex", agentType = "traex"))
        lists = mapOf("claude" to claude, "sonnetcc" to claude.copy(locked = "启动器固定了模型参数"), "codex" to codex,
            "traex" to LauncherModels("traex"))
    }

    @Test fun theDefaultIsPickedAndNothingIsSentUntilAnotherModelIs() = test {
        val service = service()
        val store = MemoryStore()
        val model = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            val state = model.state.value
            assertEquals("claude", state.selection.launcher)
            assertEquals(listOf(Call("first", "claude", false)), service.calls)
            assertEquals(claude, state.modelList)
            assertTrue(state.canPickModel)
            assertEquals("", state.model)
            assertNull(state.sentModel)
            assertEquals("claude-opus-5-5", defaultModelDetail(res, state))
            model.pickModel("gateway/glm-5.3")
            assertEquals("glm-5.3", model.state.value.pickedModel?.label)
            assertEquals("gateway/glm-5.3", model.state.value.sentModel)
            // The default entry is no pick, like none.
            model.pickModel("default")
            assertEquals("", model.state.value.model)
            assertNull(model.state.value.sentModel)
            model.submit()
            advanceUntilIdle()
            assertNull(service.request?.model)
        } finally { model.viewModelScope.cancel() }
        store.operation = null
        val picked = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            picked.pickModel("opus")
            picked.submit()
            advanceUntilIdle()
            assertEquals("opus", service.request?.model)
        } finally { picked.viewModelScope.cancel() }
        // Never remembered: the next form starts from the default again.
        store.operation = null
        val next = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            assertEquals("claude", next.state.value.selection.launcher)
            assertEquals("", next.state.value.model)
        } finally { next.viewModelScope.cancel() }
    }

    @Test fun anotherLauncherOrMachineGoesBackToTheDefaultAndReadsItsList() = test {
        val service = service()
        val store = MemoryStore()
        val model = NewAgentViewModel(service, store, machines)
        try {
            advanceUntilIdle()
            model.pickModel("opus")
            model.selection(model.state.value.selection.copy(launcher = "codex"))
            assertEquals("", model.state.value.model)
            // Loading: the old launcher's list is not shown for the new one.
            assertNull(model.state.value.modelList)
            assertTrue(model.state.value.models.loading)
            advanceUntilIdle()
            assertEquals(codex, model.state.value.modelList)
            model.pickModel("gpt-5.5-mini")
            // A mode or effort change keeps the pick and does not read the list again.
            model.selection(model.state.value.selection.copy(mode = "plan"))
            assertEquals("gpt-5.5-mini", model.state.value.model)
            model.selectMachine("second")
            assertEquals("", model.state.value.model)
            advanceUntilIdle()
            assertEquals(listOf(Call("first", "claude", false), Call("first", "codex", false), Call("second", "claude", false)), service.calls)
            assertEquals("claude", model.state.value.modelLauncher)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun effortsFollowTheModelThatWillRun() = test {
        val service = service()
        val model = NewAgentViewModel(service, MemoryStore().apply { saved["first"] = AgentDefaults("/code", "claude", effort = "xhigh") }, machines)
        try {
            advanceUntilIdle()
            // No pick: the default entry's efforts, which take the remembered one.
            assertEquals(all, model.state.value.modelEfforts)
            assertEquals("xhigh", model.state.value.selection.effort)
            assertEquals(listOf("", "low", "medium", "high", "xhigh", "max"), effortChoices(model.state.value.modelEfforts).map { it.first })
            // Sonnet does not take xhigh: back to "Follow launcher", and its levels only.
            model.pickModel("sonnet")
            assertEquals("", model.state.value.selection.effort)
            assertEquals(listOf("", "low", "medium", "high", "max"), effortChoices(model.state.value.modelEfforts).map { it.first })
            model.selection(model.state.value.selection.copy(effort = "max"))
            model.pickModel("opus")
            assertEquals("max", model.state.value.selection.effort)
            // Haiku has no effort setting.
            model.pickModel("haiku")
            assertEquals(emptyList<String>(), model.state.value.modelEfforts)
            assertEquals("", model.state.value.selection.effort)
            model.submit()
            advanceUntilIdle()
            assertEquals("haiku", service.request?.model)
            assertNull(service.request?.effort)
        } finally { model.viewModelScope.cancel() }
        // Without a list every level stays offered.
        assertEquals(effortLevels, effortChoices(null))
    }

    @Test fun codexSendsTheModelAndNeverAnEffort() = test {
        val service = service()
        val model = NewAgentViewModel(service, MemoryStore().apply { saved["first"] = AgentDefaults("/code", "codex", effort = "high") }, machines)
        try {
            advanceUntilIdle()
            assertEquals("codex", model.state.value.agentType)
            assertEquals("GPT-5.5", defaultModelDetail(res, model.state.value))
            model.pickModel("gpt-5.5-mini")
            model.prompt("修一下登录")
            model.submit()
            advanceUntilIdle()
            assertEquals("gpt-5.5-mini", service.request?.model)
            assertNull(service.request?.effort)
            assertEquals("codex", service.request?.agentType)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun aLockedLauncherOrOneWithoutAListTakesNoPick() = test {
        val service = service()
        val model = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            model.selection(model.state.value.selection.copy(launcher = "sonnetcc"))
            advanceUntilIdle()
            val locked = model.state.value
            assertEquals("启动器固定了模型参数", locked.modelList?.locked)
            assertFalse(locked.canPickModel)
            model.pickModel("opus")
            assertEquals("", model.state.value.model)
            model.submit()
            advanceUntilIdle()
            assertNull(service.request?.model)
        } finally { model.viewModelScope.cancel() }
        val traex = NewAgentViewModel(service, MemoryStore().apply { saved["first"] = AgentDefaults("/code", "traex") }, machines)
        try {
            advanceUntilIdle()
            assertEquals("traex", traex.state.value.models.launcher)
            assertNull(traex.state.value.modelList)
            assertNull(traex.state.value.modelEfforts)
        } finally { traex.viewModelScope.cancel() }
    }

    @Test fun olderHubsAndFailuresNeverHoldUpStarting() = test {
        val old = service().apply { lists = emptyMap() }
        val model = NewAgentViewModel(old, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            // 404: no list, no error, the form as before.
            assertNull(model.state.value.modelList)
            assertNull(model.state.value.models.error)
            assertFalse(model.state.value.models.loading)
            model.submit()
            advanceUntilIdle()
            assertEquals(1, old.starts)
            assertNull(old.request?.model)
        } finally { model.viewModelScope.cancel() }

        val failing = service().apply { failure = IOException("reset") }
        val failed = NewAgentViewModel(failing, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            assertEquals("连不上 hub", failed.state.value.models.error?.resolve(res))
            assertNull(failed.state.value.modelList)
            failed.submit()
            advanceUntilIdle()
            assertEquals(1, failing.starts)
            assertNull(failing.request?.model)
        } finally { failed.viewModelScope.cancel() }

        val retried = NewAgentViewModel(failing, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            assertNotNull(retried.state.value.models.error)
            // Retrying asks the CLI again; the row shows it loading meanwhile.
            val gate = CompletableDeferred<Unit>()
            failing.failure = null
            failing.gate = gate
            retried.reloadModels()
            advanceUntilIdle()
            assertTrue(retried.state.value.models.loading)
            assertNull(retried.state.value.models.error)
            gate.complete(Unit)
            advanceUntilIdle()
            assertEquals(claude, retried.state.value.modelList)
            assertEquals(Call("first", "claude", true), failing.calls.last())
        } finally { retried.viewModelScope.cancel() }
    }

    @Test fun aRefreshKeepsTheListAndDropsAPickItNoLongerHas() = test {
        val service = service()
        val model = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            model.pickModel("gateway/glm-5.3")
            service.lists = service.lists + ("claude" to claude.copy(models = claude.models.dropLast(1)))
            val gate = CompletableDeferred<Unit>()
            service.gate = gate
            model.reloadModels()
            advanceUntilIdle()
            assertTrue(model.state.value.models.loading)
            assertEquals("gateway/glm-5.3", model.state.value.sentModel)
            gate.complete(Unit)
            advanceUntilIdle()
            assertEquals("", model.state.value.model)
            assertEquals(4, model.state.value.modelList?.models?.size)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun followingTheConfigurationListsItsLauncherAndItsModel() = test {
        val service = service().apply {
            model = ModelPurposeDetail(ModelPurpose("agent.new", effective = ModelBinding("launcher.claude", "sonnet")),
                listOf(ModelProvider("launcher.claude", "claude")))
        }
        val model = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            val state = model.state.value
            assertTrue(state.following)
            assertEquals("claude", state.modelLauncher)
            assertEquals(claude, state.modelList)
            // With no pick "Model configuration"'s model runs, so its efforts are the ones offered.
            assertEquals("模型配置：sonnet", defaultModelDetail(res, state))
            assertEquals(listOf("low", "medium", "high", "max"), state.modelEfforts)
            // A launcher picked for this start runs its own default.
            model.selection(state.selection.copy(launcher = "codex"))
            advanceUntilIdle()
            assertEquals("", model.state.value.configuredModel)
            assertEquals(codex, model.state.value.modelList)
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun modelsReadAndSearchAsTheListShowsThem() {
        val (default, opus, _, _, glm) = claude.models
        assertEquals("default → claude-opus-5-5", modelDetail(default))
        assertEquals("opus → claude-opus-5-5", modelDetail(opus))
        assertEquals("gateway/glm-5.3", modelDetail(glm))
        assertNull(modelDetail(codex.models[0]))
        assertNull(modelDetail(AgentModel("sonnet", "")))
        assertTrue(glm.matches("GATEWAY"))
        assertTrue(glm.matches(" from "))
        assertTrue(opus.matches("claude-opus"))
        assertFalse(opus.matches("haiku"))
        assertTrue(opus.matches(""))
    }

    private data class Call(val machine: String, val launcher: String, val refresh: Boolean)

    private fun ompModels(models: String = "[]") = JsonCodec.decodeFromString<LauncherModels>(
        """{"agent":"omp","models":$models,"supports_start_model":true}""")

    @Test fun ompAcceptsAnUnlistedModelWithOrWithoutSuggestionsAndKeepsItsOwnOtherDefaults() = test {
        val launcher = AgentLauncher("omp", agentType = "omp", permissionModes = listOf("default", "plan"))
        for (catalogue in listOf(ompModels(), ompModels("""[{"id":"studio/listed"}]"""))) {
            val service = service().apply {
                available = listOf(launcher)
                lists = mapOf(launcher.name to catalogue)
            }
            val former = RecentSetup("first", "/code", launcher.name, "previous/model", "omp")
            val store = MemoryStore(listOf(former)).apply { saved["first"] = AgentDefaults("/code", launcher.name, "plan", "high") }
            val form = NewAgentViewModel(service, store, machines)
            try {
                advanceUntilIdle()
                assertTrue(form.state.value.canPickModel)
                assertNull(form.state.value.sentModel)
                form.useRecent(former)
                advanceUntilIdle()
                assertEquals("previous/model", form.state.value.sentModel)
                assertFalse(form.state.value.claudeModes)
                assertNull(form.state.value.providerModes)
                form.providerMode("plan")
                form.providerEffort("high")
                form.ultracode(true)
                form.pickModel("openai/gpt-5.2")
                form.reloadModels()
                advanceUntilIdle()
                assertEquals("openai/gpt-5.2", form.state.value.sentModel)
                form.pickModel("")
                assertNull(form.state.value.sentModel)
                if (catalogue.models.isNotEmpty()) form.pickModel("opus")
                form.submit()
                advanceUntilIdle()
                val request = requireNotNull(service.request)
                assertEquals("omp", request.agentType)
                assertEquals(if (catalogue.models.isEmpty()) null else "opus", request.model)
                assertNull(request.permissionMode)
                assertNull(request.effort)
                assertNull(request.ultracode)
            } finally { form.viewModelScope.cancel() }
        }
    }

    @Test fun ompRespectsListedModelsAndALockedLauncher() = test {
        val launcher = AgentLauncher("omp", agentType = "omp")
        val list = ompModels("""[{"id":"studio/local","name":"Local model"}]""")
        val service = service().apply {
            available = listOf(launcher)
            lists = mapOf(launcher.name to list)
        }
        val form = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            form.pickModel("studio/local")
            assertEquals("Local model", form.state.value.pickedModel?.label)
            form.pickModel("unlisted/compact")
            assertEquals("unlisted/compact", form.state.value.sentModel)
            service.lists = mapOf(launcher.name to list.copy(locked = "The launcher fixes its model."))
            form.reloadModels()
            advanceUntilIdle()
            assertFalse(form.state.value.canPickModel)
            form.pickModel("opus")
            form.submit()
            advanceUntilIdle()
            assertNull(requireNotNull(service.request).model)
        } finally { form.viewModelScope.cancel() }
    }
    @Test fun ompModelsFollowTheRealLauncherAndCurrentHostAcrossWorkspaceAndMachineChanges() = test {
        fun machine(hosts: String, preferred: String) = JsonCodec.decodeFromString<Machine>(
            """{"id":"first","online":true,"engine":"herdr-both","engine_hosts":$hosts,"engine_default":"$preferred"}""")
        val both = """["herdr","herdr-core","orca"]"""
        val live = MutableStateFlow(listOf(machine(both, "herdr")))
        val catalogue = ompModels("""[{"id":"studio/listed"}]""")
        val service = service().apply {
            available = listOf(AgentLauncher("omp", agentType = "omp"), AgentLauncher("other", agentType = "omp"))
            lists = mapOf("omp" to catalogue, "other" to catalogue)
        }
        val form = NewAgentViewModel(service, MemoryStore(), live)
        try {
            advanceUntilIdle()
            form.pickModel("studio/listed")
            assertEquals("studio/listed", form.state.value.sentModel)
            form.selection(form.state.value.selection.copy(launcher = "other"))
            advanceUntilIdle()
            assertFalse(form.state.value.canPickModel)
            form.pickModel("studio/listed")
            assertNull(form.state.value.sentModel)
            form.selection(form.state.value.selection.copy(launcher = "omp"))
            advanceUntilIdle()
            form.pickModel("studio/typed")
            val workspace = AgentWorkspace(machineId = "first", source = "orca", sourceId = "project", cwd = "/code")
            form.pickWorkspace(workspace)
            assertTrue(form.state.value.canPickModel)
            assertEquals("studio/typed", form.state.value.sentModel)
            form.pickWorkspace(workspace.copy(source = "paseo"))
            assertFalse(form.state.value.canPickModel)
            assertNull(form.state.value.sentModel)
            form.chooseByFolder()
            assertTrue(form.state.value.canPickModel)
            assertNull(form.state.value.sentModel)
            live.value = listOf(machine(both, "herdr-core"))
            advanceUntilIdle()
            assertFalse(form.state.value.canPickModel)
            form.pickWorkspace(workspace.copy(source = "herdr"))
            assertTrue(form.state.value.canPickModel)
            form.pickWorkspace(workspace.copy(source = "orca"))
            assertTrue(form.state.value.canPickModel)
            form.chooseByFolder()
            live.value = listOf(Machine("first", online = true, engine = "orca"))
            advanceUntilIdle()
            assertTrue(form.state.value.canPickModel)
            live.value = listOf(Machine("first", online = true, engine = "herdr-both"))
            advanceUntilIdle()
            assertTrue(form.state.value.canPickModel)
            form.pickModel("studio/typed")
            live.value = listOf(machine("[]", ""))
            // Submit even before the collector redraws the old machine. The request must use its current host.
            form.submit()
            advanceUntilIdle()
            assertFalse(form.state.value.canPickModel)
            assertNull(requireNotNull(service.request).model)
        } finally { form.viewModelScope.cancel() }
    }

    @Test fun ompModelsUseOnlyTheNativeLauncherSource() = test {
        for (source in listOf("zshrc", "path", "builtin", "persona")) {
            val service = service().apply {
                available = listOf(JsonCodec.decodeFromString<AgentLauncher>(
                    """{"name":"omp","agent":"omp","source":"$source"}"""))
                lists = mapOf("omp" to ompModels("""[{"id":"studio/listed"}]"""))
            }
            val form = NewAgentViewModel(service, MemoryStore(), machines)
            try {
                advanceUntilIdle()
                val native = source in setOf("zshrc", "path", "builtin")
                assertEquals(source, native, form.state.value.canPickModel)
                form.pickModel("studio/listed")
                assertEquals(source, if (native) "studio/listed" else null, form.state.value.sentModel)
            } finally { form.viewModelScope.cancel() }
        }
    }

    @Test fun ompRuntimeKeepsItsOwnAdvertisedModelsAndOptionsWithoutNativeTyping() = test {
        for (source in listOf("omp-provider", "paseo")) {
            val service = service().apply {
                available = listOf(JsonCodec.decodeFromString<AgentLauncher>(
                    """{"name":"omp","agent":"omp","source":"$source","permission_modes":["default","plan"]}"""))
                lists = mapOf("omp" to LauncherModels("omp", listOf(AgentModel("runtime-model", efforts = listOf("high")))))
            }
            val live = MutableStateFlow(listOf(Machine("first", online = true, engine = "paseo")))
            val form = NewAgentViewModel(service, MemoryStore(), live)
            try {
                advanceUntilIdle()
                assertTrue(form.state.value.canPickModel)
                form.pickModel("unlisted/model")
                assertNull(form.state.value.sentModel)
                form.pickModel("runtime-model")
                assertEquals(listOf("default", "plan"), form.state.value.providerModes)
                form.providerMode("plan")
                form.providerEffort("high")
                form.submit()
                advanceUntilIdle()
                val start = requireNotNull(service.request)
                assertEquals("runtime-model", start.model)
                assertEquals("plan", start.permissionMode)
                assertEquals("high", start.effort)
                assertNull(start.ultracode)
            } finally { form.viewModelScope.cancel() }
        }
    }

    @Test fun ompKeepsTheComputerDefaultWhenTheNodeLacksOrLocksStartModels() = test {
        val preset = PickerTier("Review", "omp", model = "studio/listed")
        val legacy = LauncherModels("omp", listOf(AgentModel("studio/listed")))
        val refused = JsonCodec.decodeFromString<LauncherModels>(
            """{"agent":"omp","models":[{"id":"studio/listed"}],"supports_start_model":false}""")
        for (catalogue in listOf(null, LauncherModels("omp"), legacy, refused, ompModels().copy(locked = "Pinned"),
            ompModels().copy(agent = "codex"))) {
            val service = service().apply {
                available = listOf(AgentLauncher("omp", agentType = "omp"))
                tiers = listOf(preset)
                lists = catalogue?.let { mapOf("omp" to it) }.orEmpty()
            }
            val form = NewAgentViewModel(service, MemoryStore(), machines)
            try {
                advanceUntilIdle()
                assertFalse(form.state.value.canPickModel)
                form.pickModel("studio/listed")
                form.pickModel("studio/typed")
                assertNull(form.state.value.sentModel)
                form.pickTier(preset)
                advanceUntilIdle()
                assertNull(form.state.value.sentModel)
                assertTrue(form.state.value.presetLeftOut.isNotEmpty())
                form.submit()
                advanceUntilIdle()
                val request = requireNotNull(service.request)
                assertEquals("omp", request.launcher)
                assertNull(request.model)
            } finally { form.viewModelScope.cancel() }
        }
    }

    @Test fun ompRefreshFailureWithdrawsThePreviousCapabilityUntilTheNodeConfirmsItAgain() = test {
        val service = service().apply {
            available = listOf(AgentLauncher("omp", agentType = "omp"))
            lists = mapOf("omp" to ompModels())
        }
        val form = NewAgentViewModel(service, MemoryStore(), machines)
        try {
            advanceUntilIdle()
            form.pickModel("studio/selected")
            assertEquals("studio/selected", form.state.value.sentModel)
            service.failure = IOException("Computer unavailable")
            form.reloadModels()
            advanceUntilIdle()
            assertNotNull(form.state.value.models.error)
            assertFalse(form.state.value.canPickModel)
            assertNull(form.state.value.sentModel)
            service.failure = null
            form.reloadModels()
            advanceUntilIdle()
            assertTrue(form.state.value.canPickModel)
            form.pickModel("studio/selected")
            service.failure = IOException("Computer unavailable")
            form.reloadModels()
            advanceUntilIdle()
            form.submit()
            advanceUntilIdle()
            assertNull(requireNotNull(service.request).model)
        } finally { form.viewModelScope.cancel() }
    }

    private class MemoryStore(private val recents: List<RecentSetup> = emptyList()) : AgentCreationStore {
        var operation: AgentStartOperation? = null
        val saved = mutableMapOf<String, AgentDefaults>()
        override suspend fun recentSetups(scope: String) = recents
        override suspend fun lastMachine(scope: String) = "first"
        override suspend fun defaults(scope: String, machine: String) = saved[machine] ?: AgentDefaults()
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) { saved[machine] = defaults }
        override suspend fun pending(scope: String) = operation
        override suspend fun pending(scope: String, value: AgentStartOperation?) { operation = value }
    }

    private class Service : AgentCreationService {
        override val scope = "hub/pairing"
        var starts = 0
        var request: AgentStartRequest? = null
        var available = listOf(AgentLauncher("claude"))
        var tiers = emptyList<PickerTier>()
        var model: ModelPurposeDetail? = null
        /** Absent launchers answer like an older hub (null). */
        var lists = emptyMap<String, LauncherModels>()
        var failure: Exception? = null
        var gate: CompletableDeferred<Unit>? = null
        val calls = mutableListOf<Call>()
        override suspend fun launchers(machineId: String) = available
        override suspend fun launcherList(machineId: String) = AssistantLaunchers(launchers(machineId), tiers = tiers)
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path.ifEmpty { "/code" })
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            starts++
            this.request = request
            return AgentStartResult.Created(Session(machineId = request.machineId, sessionId = "created"))
        }
        override suspend fun operation(operationId: String): AgentStartResult = AgentStartResult.Unknown
        override suspend fun newAgentModel(): ModelPurposeDetail? = model
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean): LauncherModels? {
            calls += Call(machineId, launcher, refresh)
            gate?.await()
            failure?.let { throw it }
            return lists[launcher]
        }
    }
}
