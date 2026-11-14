package persona.feature.chat

import androidx.compose.runtime.*
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.*
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JChip
import persona.core.designsystem.icons.PersonaIcons
import kotlinx.coroutines.CancellationException

/** An existing Persona conversation's launcher/model pick, sent with each following message. */
@Composable
internal fun ConversationLauncherChip(
    api: AssistantManagement,
    machineId: String,
    sessionId: String,
    sessionLauncher: String?,
    pickedLauncher: String?,
    pickedModel: String?,
    onPick: (launcher: String?, model: String?) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    var config by remember(api) { mutableStateOf<AssistantConfig?>(null) }
    var launchers by remember(api, machineId) { mutableStateOf<List<AgentLauncher>?>(null) }
    var launchersFailed by remember(api, machineId) { mutableStateOf(false) }
    var models by remember(api, machineId) { mutableStateOf<LauncherModels?>(null) }
    var modelsLoading by remember(api, machineId) { mutableStateOf(false) }
    var modelsFailed by remember(api, machineId) { mutableStateOf(false) }
    val main = config?.main == sessionId
    val configured = config?.launcher?.takeIf(String::isNotBlank)
    val effective = pickedLauncher
        ?: (if (main) configured ?: sessionLauncher else sessionLauncher ?: configured)
            ?.takeIf(String::isNotBlank) ?: "claude"

    LaunchedEffect(open, api, machineId) {
        try { config = api.config() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { }
        if (!open) return@LaunchedEffect
        launchers = null
        launchersFailed = false
        try { launchers = api.assistantLaunchers(machineId) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { launchersFailed = true }
    }
    LaunchedEffect(open, api, machineId, effective) {
        if (!open && pickedModel == null) return@LaunchedEffect
        models = null
        modelsLoading = true
        modelsFailed = false
        try { models = api.launcherModels(machineId, effective) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { modelsFailed = true }
        modelsLoading = false
    }

    // Only the main conversation has a per-conversation model pick (#6911): another assistant conversation draws no
    // chip and follows the default or what it started with. All state and reads above stay declared unconditionally.
    if (!main) return

    val defaultLabel = stringResource(R.string.chat_conversation_launcher_default_named, effective)
    val overridden = pickedLauncher != null || pickedModel != null
    val pickedModelLabel = pickedModel?.let { id ->
        models?.models?.firstOrNull { it.id == id }?.label
            ?: stringResource(R.string.chat_conversation_model_selected)
    }
    val label = if (overridden) listOfNotNull(effective, pickedModelLabel).joinToString(" · ") else defaultLabel
    JChip(label, onClick = { open = true }, icon = PersonaIcons.model, selected = overridden)
    if (!open) return

    val launcherSection = stringResource(R.string.chat_conversation_launcher_section)
    val modelSection = stringResource(R.string.chat_conversation_model_section)
    val useDefault = stringResource(R.string.chat_conversation_launcher_default)
    val launchersLoading = stringResource(R.string.chat_conversation_launchers_loading)
    val launchersFailedLabel = stringResource(R.string.chat_conversation_launchers_failed)
    val noLaunchers = stringResource(R.string.chat_conversation_launchers_none)
    val defaultModel = stringResource(R.string.chat_conversation_model_default)
    val modelsLoadingLabel = stringResource(R.string.chat_conversation_models_loading)
    val modelsFailedLabel = stringResource(R.string.chat_conversation_models_failed)
    val modelsUnavailable = stringResource(R.string.chat_conversation_models_unavailable)
    val actions = buildList {
        add(JAction(useDefault, section = launcherSection, checked = !overridden) { onPick(null, null) })
        when {
            launchers == null -> add(JAction(if (launchersFailed) launchersFailedLabel else launchersLoading, enabled = false) {})
            launchers.orEmpty().isEmpty() -> add(JAction(noLaunchers, enabled = false) {})
            else -> launchers.orEmpty().forEach { launcher ->
                val text = listOfNotNull(launcher.name, launcherModel(launcher)).joinToString(" · ")
                add(JAction(text, checked = launcher.name == pickedLauncher) { onPick(launcher.name, null) })
            }
        }
        add(JAction(defaultModel, section = modelSection, checked = pickedModel == null) { onPick(pickedLauncher, null) })
        when {
            modelsLoading -> add(JAction(modelsLoadingLabel, enabled = false) {})
            modelsFailed -> add(JAction(modelsFailedLabel, enabled = false) {})
            models == null -> add(JAction(modelsUnavailable, enabled = false) {})
            else -> models?.models?.filterNot(AgentModel::isDefault).orEmpty().forEach { model ->
                add(JAction(model.label, checked = model.id == pickedModel) { onPick(pickedLauncher, model.id) })
            }
        }
    }
    JActionSheet(actions, onDismiss = { open = false }, title = stringResource(R.string.chat_conversation_launcher_title),
        note = stringResource(R.string.chat_conversation_launcher_note))
}
