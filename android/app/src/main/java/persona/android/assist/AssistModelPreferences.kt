package persona.android.assist

import android.app.Application
import android.content.Context
import android.content.SharedPreferences
import javax.inject.Inject
import javax.inject.Singleton
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import org.json.JSONObject
import persona.android.PersonaApp
import persona.android.R
import persona.android.ui.AppServices
import persona.core.contracts.ModelPick
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.contracts.api.DigitalAssistantModelGroup
import persona.core.contracts.api.DigitalAssistantModelOption
import persona.core.contracts.api.DigitalAssistantModelSettings
import persona.core.contracts.api.LauncherModels
import persona.feature.chat.ChatWiringSource

/**
 * Local per-Hub persistence for Android's Digital Assistant floating overlay model preference.
 * Stores only non-sensitive launcher/model identifiers and display names in private app prefs.
 */
internal class AssistModelPreferences(private val prefs: SharedPreferences) {
    constructor(context: Context) : this(
        context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE),
    )

    fun read(hubScope: String): DigitalAssistantModelChoice {
        val raw = prefs.getString(keyFor(hubScope), null) ?: return DigitalAssistantModelChoice()
        return decodeChoice(raw) ?: DigitalAssistantModelChoice()
    }

    fun write(hubScope: String, choice: DigitalAssistantModelChoice) {
        val key = keyFor(hubScope)
        if (choice.pick == null) {
            prefs.edit().remove(key).apply()
        } else {
            prefs.edit().putString(key, encodeChoice(choice)).apply()
        }
    }

    fun clear(hubScope: String) {
        prefs.edit().remove(keyFor(hubScope)).apply()
    }

    private fun keyFor(hubScope: String): String =
        "$KEY_PREFIX${hubScope.trim().ifEmpty { DEFAULT_SCOPE }}"

    companion object {
        private const val PREFS_NAME = "persona_assist_model_prefs"
        private const val KEY_PREFIX = "overlay_model:"
        private const val DEFAULT_SCOPE = "default"

        internal fun encodeChoice(choice: DigitalAssistantModelChoice): String {
            val pick = choice.pick ?: return ""
            return JSONObject().apply {
                put("launcher", pick.launcher)
                pick.model?.takeIf(String::isNotBlank)?.let { put("model", it) }
                pick.effort?.takeIf(String::isNotBlank)?.let { put("effort", it) }
                choice.modelLabel.takeIf(String::isNotBlank)?.let { put("modelLabel", it) }
                choice.launcherLabel.takeIf(String::isNotBlank)?.let { put("launcherLabel", it) }
            }.toString()
        }

        internal fun decodeChoice(raw: String): DigitalAssistantModelChoice? {
            if (raw.isBlank()) return null
            val obj = runCatching { JSONObject(raw) }.getOrNull() ?: return null
            val launcher = obj.optString("launcher").trim()
            if (launcher.isEmpty()) return null
            val model = obj.optString("model").trim().ifEmpty { null }
            val effort = obj.optString("effort").trim().ifEmpty { null }
            val modelLabel = obj.optString("modelLabel").trim()
            val launcherLabel = obj.optString("launcherLabel").trim()
            return DigitalAssistantModelChoice(
                pick = ModelPick(launcher = launcher, model = model, effort = effort),
                modelLabel = modelLabel,
                launcherLabel = launcherLabel,
            )
        }
    }
}

/**
 * Pure mapper converting a Hub's assistant launchers and per-launcher model lists into grouped
 * picker options for both the floating overlay model pill and Settings › Default digital assistant.
 */
internal fun buildDigitalAssistantModelGroups(
    launchers: List<AgentLauncher>,
    modelsByLauncher: Map<String, LauncherModels?>,
): List<DigitalAssistantModelGroup> = launchers.mapNotNull { launcher ->
    val launcherName = launcher.name.trim()
    if (launcherName.isEmpty()) return@mapNotNull null
    val title = launcher.title.ifBlank { launcherName }
    val info = modelsByLauncher[launcherName]
    val unlockedModels = if (info != null && info.locked.isNullOrBlank()) info.models else emptyList()
    val options = if (unlockedModels.isNotEmpty()) {
        val defaultModelId = launcher.model?.trim().orEmpty()
        unlockedModels
            .filter { it.id.isNotBlank() }
            .distinctBy { it.id }
            .map { model ->
                DigitalAssistantModelOption(
                    pick = ModelPick(launcher = launcherName, model = model.id),
                    modelLabel = model.name.trim().ifBlank { model.id },
                    launcherLabel = title,
                    description = model.description?.trim()?.takeIf(String::isNotEmpty),
                    isDefaultForLauncher = defaultModelId.isNotEmpty() && model.id == defaultModelId,
                )
            }
    } else {
        val defaultModel = launcher.model?.trim()?.takeIf(String::isNotEmpty)
        listOf(
            DigitalAssistantModelOption(
                pick = ModelPick(launcher = launcherName, model = defaultModel),
                modelLabel = defaultModel.orEmpty(),
                launcherLabel = title,
                description = null,
                isDefaultForLauncher = true,
            ),
        )
    }
    if (options.isEmpty()) return@mapNotNull null
    DigitalAssistantModelGroup(
        launcher = launcherName,
        title = title,
        options = options,
    )
}

/**
 * Singleton runtime owning the Digital Assistant floating overlay's Hub-scoped model preference
 * and Hub model catalog loading. Shared between [PersonaVoiceInteractionSession], [AssistActivity],
 * and Settings › Default digital assistant so changes sync immediately in both places.
 */
@Singleton
class AssistModelRuntime @Inject constructor(
    application: Application,
    private val services: AppServices,
    private val wiring: ChatWiringSource,
) : DigitalAssistantModelSettings {
    private val app = application as PersonaApp
    private val store = AssistModelPreferences(application)
    private val activeScope = MutableStateFlow(services.session.value?.identity.orEmpty())
    private val mutableChoice = MutableStateFlow(store.read(activeScope.value))
    override val choice: StateFlow<DigitalAssistantModelChoice> = mutableChoice.asStateFlow()

    init {
        app.scope.launch {
            services.session.collect { session ->
                val scopeId = session?.identity.orEmpty()
                activeScope.value = scopeId
                mutableChoice.value = store.read(scopeId)
            }
        }
    }

    override fun selectDefault() {
        val scopeId = currentScopeId()
        store.clear(scopeId)
        mutableChoice.value = DigitalAssistantModelChoice()
    }

    override fun selectModel(choice: DigitalAssistantModelChoice) {
        val scopeId = currentScopeId()
        if (choice.pick == null) {
            store.clear(scopeId)
            mutableChoice.value = DigitalAssistantModelChoice()
            return
        }
        store.write(scopeId, choice)
        mutableChoice.value = choice
    }

    override suspend fun loadCatalog(refresh: Boolean): DigitalAssistantModelCatalog {
        val session = services.session.value
            ?: return DigitalAssistantModelCatalog(
                loading = false,
                hubAvailable = false,
                error = app.getString(R.string.app_assist_hub_unpaired_title),
            )
        if (session.demo != null) {
            return DigitalAssistantModelCatalog(
                loading = false,
                hubAvailable = false,
            )
        }
        val activeBinding = wiring.current.value?.takeIf { it.scope.path == session.identity }
        val connState = (activeBinding?.connection ?: session.connection).state.value
        if (activeBinding == null || connState != ConnectionState.Online) {
            return DigitalAssistantModelCatalog(
                loading = false,
                hubAvailable = false,
                error = app.getString(R.string.app_assist_hub_offline_title),
            )
        }
        val management = activeBinding.assistantManagement
        return try {
            val machineId = resolveAssistantMachineId(session, management)
                ?: return DigitalAssistantModelCatalog(
                    loading = false,
                    hubAvailable = true,
                    groups = emptyList(),
                    error = app.getString(R.string.app_assist_model_load_failed),
                )
            val launchers = management.assistantLauncherList(machineId, refresh).launchers
            val modelsByLauncher = coroutineScope {
                launchers.map { launcher ->
                    async(Dispatchers.IO) {
                        val models = try {
                            management.launcherModels(machineId, launcher.name)
                        } catch (cancelled: CancellationException) {
                            throw cancelled
                        } catch (_: Exception) {
                            null
                        }
                        launcher.name to models
                    }
                }.awaitAll().toMap()
            }
            DigitalAssistantModelCatalog(
                loading = false,
                groups = buildDigitalAssistantModelGroups(launchers, modelsByLauncher),
                error = null,
                hubAvailable = true,
            )
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            DigitalAssistantModelCatalog(
                loading = false,
                groups = emptyList(),
                error = error.message?.takeIf(String::isNotBlank)
                    ?: app.getString(R.string.app_assist_model_load_failed),
                hubAvailable = true,
            )
        }
    }

    private fun currentScopeId(): String =
        services.session.value?.identity ?: activeScope.value

    private suspend fun resolveAssistantMachineId(
        session: persona.android.ui.ShellSession,
        management: persona.core.contracts.api.AssistantManagement,
    ): String? {
        val assistantSnapshot = session.assistant.assistant.value
        val direct = assistantSnapshot.mainSession?.machineId?.takeIf(String::isNotBlank)
            ?: assistantSnapshot.conversations.firstOrNull { it.machineId.isNotBlank() }?.machineId
        if (direct != null) return direct
        val fromConfig = try {
            management.config().machineId.takeIf(String::isNotBlank)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            null
        }
        if (fromConfig != null) return fromConfig
        val machines = session.sessions.machines.value
        return machines.firstOrNull { it.online && it.id.isNotBlank() }?.id
            ?: machines.firstOrNull { it.id.isNotBlank() }?.id
    }
}
