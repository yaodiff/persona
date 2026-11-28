package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LifecycleEventEffect
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DigitalAssistantModelCatalog
import persona.core.contracts.api.DigitalAssistantModelChoice
import persona.core.contracts.api.DigitalAssistantModelSettings
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JListPage
import persona.core.designsystem.JNotice
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rowPadding

/**
 * Settings › Default digital assistant:
 * 1. System `ROLE_ASSISTANT` status and shortcut to Android's system digital assistant picker.
 * 2. Hub-scoped model preference for the floating quick-ask overlay, synced live with the overlay's
 *    model switcher and applied in place to the main conversation when sending from the quick window.
 */
@Composable
internal fun DigitalAssistantSettingsPage(
    modelSettings: DigitalAssistantModelSettings?,
    connection: ConnectionState,
    demo: Boolean,
    onBack: () -> Unit,
) {
    val context = LocalContext.current
    var defaultAssistant by remember(context) { mutableStateOf(DigitalAssistantSettings.isDefaultAssistant(context)) }
    LifecycleEventEffect(Lifecycle.Event.ON_RESUME) {
        defaultAssistant = DigitalAssistantSettings.isDefaultAssistant(context)
    }

    val choice by (modelSettings?.choice ?: remember { kotlinx.coroutines.flow.MutableStateFlow(DigitalAssistantModelChoice()) })
        .collectAsState()
    var reloadNonce by remember { mutableIntStateOf(0) }
    var catalog by remember { mutableStateOf(DigitalAssistantModelCatalog(loading = modelSettings != null && !demo)) }

    LaunchedEffect(modelSettings, connection, demo, reloadNonce) {
        if (modelSettings == null || demo) {
            catalog = DigitalAssistantModelCatalog(loading = false, hubAvailable = false)
            return@LaunchedEffect
        }
        if (connection != ConnectionState.Online) {
            catalog = DigitalAssistantModelCatalog(loading = false, hubAvailable = false)
            return@LaunchedEffect
        }
        catalog = catalog.copy(loading = true, error = null)
        catalog = modelSettings.loadCatalog(refresh = reloadNonce > 0)
    }

    val defaultModelLabel = stringResource(R.string.settings_digital_assistant_model_default)
    val selectedModelSummary = if (choice.isDefault) defaultModelLabel else choice.fullLabel.ifBlank { defaultModelLabel }

    JListPage(title = stringResource(R.string.settings_digital_assistant_title), onBack = onBack) {
        item("digital-assistant-system-header") {
            JSectionHeader(stringResource(R.string.settings_digital_assistant_system_header))
        }
        item("digital-assistant-system-role") {
            JGroupItem(GroupPosition.Single) {
                JRow(
                    title = stringResource(R.string.settings_digital_assistant_title),
                    leadingIcon = PersonaIcons.ai,
                    subtitle = stringResource(R.string.settings_digital_assistant_detail),
                    value = stringResource(
                        if (defaultAssistant) R.string.settings_digital_assistant_active
                        else R.string.settings_digital_assistant_inactive,
                    ),
                    onClick = { DigitalAssistantSettings.openSystemSettings(context) },
                    modifier = Modifier.testTag("digital-assistant-system-row"),
                )
            }
        }
        item("digital-assistant-model-header") {
            Row(
                Modifier.fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.SpaceBetween,
            ) {
                JSectionHeader(stringResource(R.string.settings_digital_assistant_model_header))
                if (modelSettings != null && !demo && connection == ConnectionState.Online && !catalog.loading) {
                    JTextButton(
                        text = stringResource(R.string.settings_digital_assistant_model_refresh),
                        onClick = { reloadNonce++ },
                        icon = PersonaIcons.retry,
                        modifier = Modifier.testTag("digital-assistant-model-refresh"),
                    )
                }
            }
        }
        item("digital-assistant-model-caption") {
            SettingsCaption(
                text = stringResource(R.string.settings_digital_assistant_model_caption, selectedModelSummary),
                modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.xs),
            )
        }
        item("digital-assistant-model-default") {
            JGroupItem(GroupPosition.Single) {
                JRow(
                    title = defaultModelLabel,
                    leadingIcon = PersonaIcons.home,
                    subtitle = stringResource(R.string.settings_digital_assistant_model_default_detail),
                    selected = choice.isDefault,
                    chevron = false,
                    trailing = if (choice.isDefault) {
                        {
                            JIcon(
                                icon = PersonaIcons.done,
                                contentDescription = stringResource(R.string.settings_digital_assistant_model_selected),
                                size = Tokens.Size.iconXS,
                                tint = PersonaTheme.palette.accent,
                            )
                        }
                    } else {
                        null
                    },
                    enabled = modelSettings != null && !demo,
                    onClick = { modelSettings?.selectDefault() },
                    modifier = Modifier.testTag("digital-assistant-model-default"),
                )
            }
        }

        if (modelSettings != null && !demo) {
            when {
                connection != ConnectionState.Online -> {
                    item("digital-assistant-model-offline") {
                        JNotice(
                            text = stringResource(R.string.settings_digital_assistant_model_offline),
                            status = Status.Offline,
                            icon = PersonaIcons.offline,
                            modifier = Modifier.padding(top = Tokens.Space.s),
                        )
                    }
                }

                catalog.loading -> {
                    item("digital-assistant-model-loading") {
                        Row(
                            Modifier
                                .fillMaxWidth()
                                .padding(horizontal = rowPadding, vertical = Tokens.Space.m),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                        ) {
                            JSpinner(Modifier.size(Tokens.Size.markS), color = PersonaTheme.palette.accent)
                            Text(
                                text = stringResource(R.string.settings_digital_assistant_model_loading),
                                style = PersonaTheme.type.caption,
                                color = PersonaTheme.palette.ink2,
                            )
                        }
                    }
                }

                catalog.error != null -> {
                    item("digital-assistant-model-error") {
                        JNotice(
                            text = catalog.error ?: stringResource(R.string.settings_digital_assistant_model_empty),
                            status = Status.NeedsYou,
                            icon = PersonaIcons.error,
                            actionLabel = stringResource(R.string.settings_digital_assistant_model_refresh),
                            onAction = { reloadNonce++ },
                            modifier = Modifier.padding(top = Tokens.Space.s),
                        )
                    }
                }

                catalog.groups.isEmpty() -> {
                    item("digital-assistant-model-empty") {
                        SettingsCaption(
                            text = stringResource(R.string.settings_digital_assistant_model_empty),
                            modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s),
                        )
                    }
                }

                else -> {
                    catalog.groups.forEach { group ->
                        item("digital-assistant-group-${group.launcher}") {
                            JSectionHeader(group.title)
                        }
                        group.options.forEachIndexed { index, option ->
                            val isSelected = choice.pick == option.pick
                            val title = option.modelLabel.ifBlank { group.title }
                            item("digital-assistant-opt-${group.launcher}-${option.pick.model.orEmpty()}-$index") {
                                JGroupItem(GroupPosition.of(index, group.options.size)) {
                                    JRow(
                                        title = title,
                                        leadingIcon = PersonaIcons.model,
                                        subtitle = option.description,
                                        selected = isSelected,
                                        chevron = false,
                                        trailing = if (isSelected) {
                                            {
                                                JIcon(
                                                    icon = PersonaIcons.done,
                                                    contentDescription = stringResource(R.string.settings_digital_assistant_model_selected),
                                                    size = Tokens.Size.iconXS,
                                                    tint = PersonaTheme.palette.accent,
                                                )
                                            }
                                        } else {
                                            null
                                        },
                                        onClick = { modelSettings.selectModel(option.toChoice()) },
                                        modifier = Modifier.testTag(
                                            "digital-assistant-model-${option.pick.launcher}-${option.pick.model.orEmpty()}",
                                        ),
                                    )
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}
