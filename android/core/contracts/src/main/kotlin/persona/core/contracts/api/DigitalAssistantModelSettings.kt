package persona.core.contracts.api

import kotlinx.coroutines.flow.StateFlow
import persona.core.contracts.ModelPick

/**
 * Locally persisted model choice for Android's Digital Assistant floating overlay, scoped per
 * paired Hub. A null [pick] means "Default (Main chat)".
 */
data class DigitalAssistantModelChoice(
    val pick: ModelPick? = null,
    val modelLabel: String = "",
    val launcherLabel: String = "",
) {
    val isDefault: Boolean get() = pick == null

    /** Concise label for compact header pills (model name, falling back to launcher label or id). */
    val compactLabel: String
        get() = modelLabel.ifBlank { pick?.model.orEmpty() }
            .ifBlank { launcherLabel }
            .ifBlank { pick?.launcher.orEmpty() }

    /** Descriptive label for settings rows and picker summaries. */
    val fullLabel: String
        get() {
            val model = modelLabel.ifBlank { pick?.model.orEmpty() }
            val launcher = launcherLabel.ifBlank { pick?.launcher.orEmpty() }
            return when {
                launcher.isNotBlank() && model.isNotBlank() && !launcher.equals(model, ignoreCase = true) ->
                    "$launcher · $model"
                model.isNotBlank() -> model
                else -> launcher
            }
        }
}

/** One selectable model option loaded from the connected Hub's assistant launcher catalog. */
data class DigitalAssistantModelOption(
    val pick: ModelPick,
    val modelLabel: String,
    val launcherLabel: String,
    val description: String? = null,
    val isDefaultForLauncher: Boolean = false,
) {
    fun toChoice(): DigitalAssistantModelChoice = DigitalAssistantModelChoice(
        pick = pick,
        modelLabel = modelLabel,
        launcherLabel = launcherLabel,
    )
}

/** One provider/launcher group inside the Digital Assistant model picker. */
data class DigitalAssistantModelGroup(
    val launcher: String,
    val title: String,
    val options: List<DigitalAssistantModelOption>,
)

/** Loaded assistant model catalog from the currently connected Hub. */
data class DigitalAssistantModelCatalog(
    val loading: Boolean = false,
    val groups: List<DigitalAssistantModelGroup> = emptyList(),
    val error: String? = null,
    val hubAvailable: Boolean = true,
)

/**
 * Device-local model settings for Android's Default Digital Assistant floating overlay.
 * Shared live between the overlay's header model pill and Settings › Default digital assistant.
 */
interface DigitalAssistantModelSettings {
    val choice: StateFlow<DigitalAssistantModelChoice>
    fun selectDefault()
    fun selectModel(choice: DigitalAssistantModelChoice)
    suspend fun loadCatalog(refresh: Boolean = false): DigitalAssistantModelCatalog
}
