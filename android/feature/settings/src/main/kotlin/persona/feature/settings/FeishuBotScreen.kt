package persona.feature.settings

import android.content.res.Resources
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import kotlinx.coroutines.launch
import persona.core.contracts.api.FeishuBotSettings
import persona.core.contracts.api.FeishuBotStatus
import persona.core.designsystem.*
import java.time.Instant

/** The bot's state in words (web `lib/feishu-bot.ts` stateText, iOS `FeishuBotModel.stateText`). */
internal fun feishuBotState(res: Resources, status: FeishuBotStatus?, loading: Boolean): String = when {
    status == null -> res.getString(if (loading) R.string.settings_feishu_bot_state_checking else R.string.settings_feishu_bot_state_unreadable)
    !status.enabled -> res.getString(R.string.settings_feishu_bot_state_off)
    !status.hasSecret -> res.getString(R.string.settings_feishu_bot_state_no_credentials)
    !status.connected -> status.error?.takeIf { it.isNotBlank() }?.let { res.getString(R.string.settings_feishu_bot_state_error, it) }
        ?: res.getString(R.string.settings_feishu_bot_state_connecting)
    status.owner == null -> res.getString(R.string.settings_feishu_bot_state_connected_unpaired)
    else -> res.getString(R.string.settings_feishu_bot_state_connected)
}

private fun feishuBotStatus(status: FeishuBotStatus?): Status = when {
    status == null || !status.enabled -> Status.Offline
    status.connected && status.owner != null -> Status.Unread
    else -> Status.Idle
}

/**
 * Settings › Lark bot (#2806, iOS `FeishuBotView`, docs/extensions/bots.md): how the bot is doing and its switch, its app
 * credentials (verified by the hub before it keeps them), the agents chatted with in Feishu and how to make the bot.
 * Pairing, its features and a test message are on its channel's page (Settings › Channels › Lark bot), which links here.
 */
@Composable
internal fun FeishuBotPage(api: FeishuBotSettings?, onBack: () -> Unit, larkBot: LarkBotEntry = LarkBotEntry.Offered) {
    val res = LocalResources.current
    // The hub's Lark module is off (#15995): nothing here can work, so nothing is asked of the hub and no control shows.
    if (larkBot == LarkBotEntry.ModuleOff) {
        LarkBotOffPage(stringResource(R.string.settings_feishu_bot_title), onBack)
        return
    }
    if (api == null) {
        JListPage(title = stringResource(R.string.settings_feishu_bot_title), onBack = onBack) {
            item { JNotice(stringResource(R.string.settings_src_channels_demo)) }
        }
        return
    }
    val model = remember(api) { FeishuBotModel(api) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    LaunchedEffect(model) { model.load() }
    val status = state.status
    // The daily sync into the knowledge base (#15392): read again with the bot, so a new chat bot shows there too.
    val sync = remember(api) { api.sync?.let { LarkSyncModel(it, scope) } }
    val syncState = sync?.state?.collectAsState()?.value
    LaunchedEffect(sync, status?.appId) { sync?.load() }
    if (sync != null && syncState != null) LarkSyncEffects(sync, syncState)
    // Group access (#16848): who else may @ the bot. Hidden until the bot is paired and the hub has the route.
    val groupAccess = remember(api) { api.groupAccess?.let { FeishuGroupAccessModel(it) } }
    val groupState = groupAccess?.state?.collectAsState()?.value
    LaunchedEffect(groupAccess, status?.owner) { if (status?.owner != null) groupAccess?.load() }
    JListPage(title = stringResource(R.string.settings_feishu_bot_title), onBack = onBack, largeTitle = false,
        refreshing = state.loading && status != null, onRefresh = { scope.launch { model.load(); sync?.load(); if (status?.owner != null) groupAccess?.load() } }) {
        item("status") {
            Column {
                JGroupItem(GroupPosition(first = true, last = false), Modifier.padding(top = Tokens.Space.s)) {
                    JRow(status?.botName?.takeIf(String::isNotBlank) ?: stringResource(R.string.settings_feishu_bot_title),
                        leading = { JBrandMark(Brand.Lark, Tokens.Space.xl) },
                        trailing = { JStateLabel(feishuBotState(res, status, state.loading), feishuBotStatus(status),
                            problem = status?.enabled == true && !status.connected && !status.error.isNullOrBlank()) })
                }
                JGroupItem(GroupPosition(first = false, last = status?.lastEventAt?.takeIf { it > 0 } == null)) {
                    JSwitchRow(stringResource(R.string.settings_feishu_bot_enable), status?.enabled == true,
                        { on -> scope.launch { model.setEnabled(on) } }, enabled = status != null && !state.saving)
                }
                status?.lastEventAt?.takeIf { it > 0 }?.let { at ->
                    JGroupItem(GroupPosition(first = false, last = true)) {
                        JRow(stringResource(R.string.settings_feishu_bot_last_event), value = agoText(res, Instant.ofEpochSecond(at)))
                    }
                }
                state.error?.let { JNotice(it.resolve(res), Modifier.padding(top = Tokens.Space.s), status = Status.NeedsYou) }
                SettingsCaption(stringResource(R.string.settings_feishu_bot_footer), Modifier.padding(vertical = Tokens.Space.s))
            }
        }
        item("credentials") {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JSectionHeader(stringResource(R.string.settings_feishu_bot_credentials))
                JTextField(state.appId, { model.edit(appId = it) }, Modifier.fillMaxWidth(), label = "App ID", // i18n-ignore: the Feishu / Lark console's own name, same on iOS / web
                    placeholder = stringResource(R.string.settings_feishu_bot_required), singleLine = true,
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, autoCorrectEnabled = false))
                JTextField(state.appSecret, { model.edit(appSecret = it) }, Modifier.fillMaxWidth(), label = "App Secret", // i18n-ignore: the Feishu / Lark console's own name, same on iOS / web
                    placeholder = stringResource(if (state.needsSecret) R.string.settings_feishu_bot_required else R.string.settings_feishu_bot_secret_saved),
                    supportingText = if (state.appSecret.isNotEmpty()) stringResource(R.string.settings_feishu_bot_secret_filled) else null,
                    singleLine = true, visualTransformation = PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password, autoCorrectEnabled = false))
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    Text(stringResource(R.string.settings_feishu_bot_platform), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2, maxLines = 1)
                    val domains = listOf("feishu", "lark") + listOf(state.domain).filter { it != "feishu" && it != "lark" }
                    JSegmented(domains.map { JSegment(when (it) {
                        "feishu" -> stringResource(R.string.settings_feishu_bot_platform_feishu)
                        "lark" -> stringResource(R.string.settings_feishu_bot_platform_lark)
                        else -> it
                    }) }, domains.indexOf(state.domain).coerceAtLeast(0), { model.edit(domain = domains[it]) })
                }
                JButton(stringResource(R.string.settings_feishu_bot_verify_save), {
                    scope.launch { if (model.saveCredentials()) feedback?.notice(res.getString(R.string.settings_feishu_bot_credentials_saved)) }
                }, Modifier.fillMaxWidth(), loading = state.saving, enabled = state.canSave)
                if (state.unpairs) SettingsCaption(stringResource(R.string.settings_feishu_bot_unpairs))
                SettingsCaption(stringResource(if (status?.fromConfig == true) R.string.settings_feishu_bot_credentials_from_config
                    else R.string.settings_feishu_bot_credentials_secret_note))
            }
        }
        status?.agents?.takeIf { it.isNotEmpty() }?.let { agents ->
            item("agents-header") { JSectionHeader(stringResource(R.string.settings_feishu_bot_agents), Modifier.padding(top = Tokens.Space.l)) }
            agents.forEachIndexed { i, agent ->
                item("agent-$i-${agent.machineId}-${agent.sessionId}") {
                    JGroupItem(GroupPosition.of(i, agents.size)) { JRow(agent.title, value = agent.machineId) }
                }
            }
        }
        if (sync != null && syncState != null) larkSyncItems(sync, syncState, scope)
        if (status?.owner != null && groupAccess != null && groupState != null) feishuGroupAccessItems(groupAccess, groupState, scope)
        item("howto") {
            Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JSectionHeader(stringResource(R.string.settings_feishu_bot_howto))
                listOf(R.string.settings_feishu_bot_step_create_app, R.string.settings_feishu_bot_step_permissions,
                    R.string.settings_feishu_bot_step_sync_permissions, R.string.settings_feishu_bot_step_events, R.string.settings_feishu_bot_step_publish).forEachIndexed { i, step ->
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                        Text("${i + 1}", style = PersonaTheme.type.caption, color = PersonaTheme.palette.accent, maxLines = 1)
                        Text(stringResource(step), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    }
                }
                SettingsCaption(stringResource(R.string.settings_feishu_bot_howto_footer))
            }
        }
    }
}
