package persona.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.api.FeishuGroupAccess
import persona.core.contracts.api.FeishuGroupAccessMode
import persona.core.contracts.api.FeishuGroupChat
import persona.core.contracts.api.FeishuGroupRequest
import persona.core.contracts.api.FeishuGroupUser
import persona.core.contracts.api.FeishuGuestToolOption
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons

/**
 * Settings › Lark bot › Group access (#16848, web `FeishuGroupAccess.tsx`) as items of the bot's page: nothing on a hub
 * without the route, and nothing while the bot is unpaired. Its calls run in [scope], the page's.
 */
internal fun LazyListScope.feishuGroupAccessItems(model: FeishuGroupAccessModel, state: FeishuGroupAccessState, scope: CoroutineScope) {
    val failed = state.loadError
    val access = state.access
    if (access == null || !access.paired) {
        if (failed == null || !state.loaded) return
        item("group-access-header") { JSectionHeader(stringResource(R.string.settings_feishu_group_title), Modifier.padding(top = Tokens.Space.l)) }
        item("group-access-failed") {
            JNotice(failed.text(), status = Status.NeedsYou, actionLabel = stringResource(R.string.settings_lark_sync_retry),
                onAction = { scope.launch { model.load() } })
        }
        return
    }
    val policy = access.policy
    val groupsOn = policy.mode != FeishuGroupAccessMode.OwnerOnly
    val peopleOn = policy.mode == FeishuGroupAccessMode.Allowlist
    val off = state.saving
    item("group-access-mode") { GroupMode(model, access, off, scope) }
    if (groupsOn) item("group-access-groups") {
        MemberList(model, scope, off, stringResource(R.string.settings_feishu_group_groups),
            stringResource(R.string.settings_feishu_group_groups_footer), chats = true, list = GroupList.Allowed,
            chatsOf = policy.allowedChats, full = policy.allowedChats.size >= access.limits.listMax)
    }
    if (peopleOn) item("group-access-people") {
        MemberList(model, scope, off, stringResource(R.string.settings_feishu_group_people),
            stringResource(R.string.settings_feishu_group_people_footer), chats = false, list = GroupList.Allowed,
            users = policy.allowedUsers, full = policy.allowedUsers.size >= access.limits.listMax)
    }
    if (access.recentRequests.isNotEmpty()) item("group-access-requests") {
        GroupRequests(model, access.recentRequests, off, scope)
    }
    item("group-access-blocked") { GroupBlocked(model, access, off, scope) }
    if (groupsOn) item("group-access-tools") { GroupTools(model, policy.guestTools, access.availableTools, access.limits.guestToolsMax, off, scope) }
    state.error?.let { error -> item("group-access-error") { JNotice(error.text(), Modifier.padding(top = Tokens.Space.s), status = Status.NeedsYou) } }
}

@Composable
private fun GroupMode(model: FeishuGroupAccessModel, access: FeishuGroupAccess, off: Boolean, scope: CoroutineScope) {
    val policy = access.policy
    val modes = FeishuGroupAccessMode.All
    val hours = feishuGroupHourlyChoices(policy.perChatHourly)
    Column {
        JSectionHeader(stringResource(R.string.settings_feishu_group_title), Modifier.padding(top = Tokens.Space.l))
        JGroupItem(GroupPosition(first = true, last = false)) {
            ChoiceRow(stringResource(R.string.settings_feishu_group_mode), stringResource(modeLabel(policy.mode)), off, modes.map { mode ->
                JAction(stringResource(modeLabel(mode)), checked = mode == policy.mode) {
                    if (mode != policy.mode) scope.launch { model.setMode(mode) }
                }
            })
        }
        JGroupItem(GroupPosition(first = false, last = false)) {
            Text(stringResource(feishuGroupModeDetail(policy.mode)), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s),
                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        }
        if (policy.mode != FeishuGroupAccessMode.OwnerOnly) {
            JGroupItem(GroupPosition(first = false, last = false)) {
                Text(stringResource(R.string.settings_feishu_group_guest_caution), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s).testTag("group-access-caution"),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        }
        JGroupItem(GroupPosition(first = false, last = true)) {
            ChoiceRow(stringResource(R.string.settings_feishu_group_per_hour), "${policy.perChatHourly}", off, hours.map { value ->
                JAction("$value", checked = value == policy.perChatHourly) {
                    if (value != policy.perChatHourly) scope.launch { model.setPerChatHourly(value) }
                }
            }, subtitle = stringResource(R.string.settings_feishu_group_per_hour_hint))
        }
        SettingsCaption(stringResource(R.string.settings_feishu_group_footer), Modifier.padding(vertical = Tokens.Space.s))
    }
}

/** A setting picked from a menu: the value sits at the end, and the choices open from it. One line, so it never wraps. */
@Composable
private fun ChoiceRow(title: String, value: String, off: Boolean, actions: List<JAction>, subtitle: String? = null) {
    var menu by remember { mutableStateOf(false) }
    Box {
        JRow(title, subtitle = subtitle, value = value, enabled = !off, onClick = { menu = true })
        JMenu(menu, { menu = false }, actions)
    }
}

private fun modeLabel(mode: String) = when (mode) {
    FeishuGroupAccessMode.Allowlist -> R.string.settings_feishu_group_mode_allowlist
    FeishuGroupAccessMode.EveryoneInAllowedChats -> R.string.settings_feishu_group_mode_everyone
    else -> R.string.settings_feishu_group_mode_owner_only
}

@Composable
private fun MemberList(
    model: FeishuGroupAccessModel, scope: CoroutineScope, off: Boolean, title: String, footer: String,
    chats: Boolean, list: GroupList, users: List<FeishuGroupUser> = emptyList(), chatsOf: List<FeishuGroupChat> = emptyList(),
    full: Boolean,
) {
    val count = if (chats) chatsOf.size else users.size
    var adding by remember { mutableStateOf(false) }
    Column {
        JSectionHeader(title, Modifier.padding(top = Tokens.Space.l))
        if (count == 0) JGroupItem(GroupPosition(first = true, last = false)) {
            JRow(stringResource(if (chats) R.string.settings_feishu_group_none_groups else R.string.settings_feishu_group_none_people))
        }
        users.forEachIndexed { i, user ->
            JGroupItem(GroupPosition(first = i == 0, last = false)) {
                MemberRow(user.name?.takeIf { it.isNotBlank() } ?: user.openId, user.name?.let { user.openId }, off) {
                    scope.launch { model.removeUser(list, user.openId) }
                }
            }
        }
        chatsOf.forEachIndexed { i, chat ->
            JGroupItem(GroupPosition(first = i == 0, last = false)) {
                MemberRow(chat.name?.takeIf { it.isNotBlank() } ?: chat.chatId, chat.name?.let { chat.chatId }, off) {
                    scope.launch { model.removeChat(list, chat.chatId) }
                }
            }
        }
        JGroupItem(GroupPosition(first = false, last = true)) {
            JRow(stringResource(if (chats) R.string.settings_feishu_group_add_group else R.string.settings_feishu_group_add_person),
                leadingIcon = PersonaIcons.add, enabled = !off && !full, onClick = { adding = true })
        }
        SettingsCaption(footer, Modifier.padding(vertical = Tokens.Space.s))
        if (adding) AddMemberSheet(chats, off, taken = { id ->
            if (chats) chatsOf.any { it.chatId == id } else users.any { it.openId == id }
        }, full = full, onDismiss = { adding = false }) { id, name ->
            scope.launch {
                val saved = if (chats) model.addChat(list, id, name) else model.addUser(list, id, name)
                if (saved) adding = false
            }
        }
    }
}

@Composable
private fun MemberRow(title: String, subtitle: String?, off: Boolean, onRemove: () -> Unit) {
    JRow(title, subtitle = subtitle, enabled = !off, trailing = {
        JIconButton(PersonaIcons.delete, stringResource(R.string.settings_feishu_group_remove, title), onRemove, enabled = !off)
    })
}

@Composable
private fun AddMemberSheet(chats: Boolean, off: Boolean, taken: (String) -> Boolean, full: Boolean, onDismiss: () -> Unit, onAdd: (String, String) -> Unit) {
    var id by remember { mutableStateOf("") }
    var name by remember { mutableStateOf("") }
    val error = feishuGroupIdError(id, taken(id.trim()), full)
    val valid = id.isNotBlank() && error == null
    JSheet(onDismiss, title = stringResource(if (chats) R.string.settings_feishu_group_add_group else R.string.settings_feishu_group_add_person), scrollable = true) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JTextField(id, { id = it }, Modifier.fillMaxWidth(), label = stringResource(if (chats) R.string.settings_feishu_group_group_id else R.string.settings_feishu_group_person_id),
                placeholder = if (chats) "oc_…" else "ou_…", // i18n-ignore: Lark's own id prefixes, same on iOS / web
                supportingText = error?.let { stringResource(it) }, isError = error != null, singleLine = true, enabled = !off,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Ascii, autoCorrectEnabled = false))
            JTextField(name, { name = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_feishu_group_name), singleLine = true, enabled = !off)
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
                JButton(stringResource(R.string.settings_cancel), onDismiss, kind = JButtonKind.Secondary, size = JButtonSize.Medium, enabled = !off)
                JButton(stringResource(R.string.settings_feishu_group_add), { onAdd(id.trim(), name.trim()) }, size = JButtonSize.Medium,
                    loading = off, enabled = !off && valid)
            }
        }
    }
}

@Composable
private fun GroupRequests(model: FeishuGroupAccessModel, requests: List<FeishuGroupRequest>, off: Boolean, scope: CoroutineScope) {
    Column {
        JSectionHeader(stringResource(R.string.settings_feishu_group_requests), Modifier.padding(top = Tokens.Space.l))
        requests.forEachIndexed { i, request ->
            JGroupItem(GroupPosition.of(i, requests.size)) { RequestRow(request, off) { allow ->
                scope.launch { if (allow) model.allow(request) else model.block(request) }
            } }
        }
        SettingsCaption(stringResource(R.string.settings_feishu_group_requests_footer), Modifier.padding(vertical = Tokens.Space.s))
    }
}

/** A recent request, with Allow and Block in one menu so the row keeps a single action and its text never wraps. */
@Composable
private fun RequestRow(request: FeishuGroupRequest, off: Boolean, onChoose: (allow: Boolean) -> Unit) {
    val who = request.name?.takeIf { it.isNotBlank() } ?: request.openId
    val where = request.chatName?.takeIf { it.isNotBlank() } ?: request.chatId
    var menu by remember { mutableStateOf(false) }
    Box {
        JRow(who, subtitle = where, enabled = !off, trailing = {
            JIconButton(PersonaIcons.more, stringResource(R.string.settings_feishu_group_request_actions, who), { menu = true }, enabled = !off)
        })
        JMenu(menu, { menu = false }, listOf(
            JAction(stringResource(R.string.settings_feishu_group_allow)) { onChoose(true) },
            JAction(stringResource(R.string.settings_feishu_group_block), destructive = true) { onChoose(false) },
        ))
    }
}

@Composable
private fun GroupBlocked(model: FeishuGroupAccessModel, access: FeishuGroupAccess, off: Boolean, scope: CoroutineScope) {
    val policy = access.policy
    val max = access.limits.listMax
    Column {
        JSectionHeader(stringResource(R.string.settings_feishu_group_blocked), Modifier.padding(top = Tokens.Space.l))
        BlockedList(stringResource(R.string.settings_feishu_group_blocked_people), chats = false, off, policy.blockedUsers.size >= max,
            users = policy.blockedUsers, onRemove = { scope.launch { model.removeUser(GroupList.Blocked, it) } },
            onAdd = { id, name -> model.addUser(GroupList.Blocked, id, name) })
        BlockedList(stringResource(R.string.settings_feishu_group_blocked_groups), chats = true, off, policy.blockedChats.size >= max,
            chatsOf = policy.blockedChats, onRemove = { scope.launch { model.removeChat(GroupList.Blocked, it) } },
            onAdd = { id, name -> model.addChat(GroupList.Blocked, id, name) })
        SettingsCaption(stringResource(R.string.settings_feishu_group_blocked_footer), Modifier.padding(vertical = Tokens.Space.s))
    }
}

@Composable
private fun BlockedList(
    label: String, chats: Boolean, off: Boolean, full: Boolean,
    users: List<FeishuGroupUser> = emptyList(), chatsOf: List<FeishuGroupChat> = emptyList(),
    onRemove: (String) -> Unit, onAdd: suspend (String, String) -> Boolean,
) {
    var adding by remember { mutableStateOf(false) }
    val rows = if (chats) chatsOf.size else users.size
    val scope = rememberCoroutineScope()
    Column(Modifier.padding(bottom = Tokens.Space.s)) {
        Text(label, Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.xs), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1)
        users.forEachIndexed { i, user ->
            JGroupItem(GroupPosition(first = i == 0, last = false)) {
                MemberRow(user.name?.takeIf { it.isNotBlank() } ?: user.openId, user.name?.let { user.openId }, off) { onRemove(user.openId) }
            }
        }
        chatsOf.forEachIndexed { i, chat ->
            JGroupItem(GroupPosition(first = i == 0, last = false)) {
                MemberRow(chat.name?.takeIf { it.isNotBlank() } ?: chat.chatId, chat.name?.let { chat.chatId }, off) { onRemove(chat.chatId) }
            }
        }
        JGroupItem(GroupPosition(first = rows == 0, last = true)) {
            JRow(stringResource(if (chats) R.string.settings_feishu_group_add_group else R.string.settings_feishu_group_add_person),
                leadingIcon = PersonaIcons.add, enabled = !off && !full, onClick = { adding = true })
        }
        if (adding) AddMemberSheet(chats, off, taken = { id ->
            if (chats) chatsOf.any { it.chatId == id } else users.any { it.openId == id }
        }, full = full, onDismiss = { adding = false }) { id, name ->
            scope.launch { if (onAdd(id, name)) adding = false }
        }
    }
}

@Composable
private fun GroupTools(
    model: FeishuGroupAccessModel, selected: List<String>, available: List<FeishuGuestToolOption>, max: Int, off: Boolean, scope: CoroutineScope,
) {
    Column {
        JSectionHeader(stringResource(R.string.settings_feishu_group_tools), Modifier.padding(top = Tokens.Space.l))
        if (available.isEmpty()) JGroupItem(GroupPosition.Single) {
            JRow(stringResource(R.string.settings_feishu_group_no_tools))
        } else available.forEachIndexed { i, tool ->
            val on = tool.name in selected
            val label = stringResource(R.string.settings_feishu_group_tool_label, tool.connector, tool.name)
            JGroupItem(GroupPosition.of(i, available.size)) {
                JSwitchRow(label, on, { next -> scope.launch { model.setTool(tool.name, next) } }, enabled = !off && (on || selected.size < max))
            }
        }
        SettingsCaption(stringResource(R.string.settings_feishu_group_tools_footer), Modifier.padding(vertical = Tokens.Space.s))
    }
}
