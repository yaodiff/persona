package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.contracts.api.*
import kotlinx.coroutines.launch
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import java.time.LocalTime
import java.time.ZoneId
import java.time.format.TextStyle

@Composable
internal fun DevicesPage(
    controller: DeviceSettingsState,
    available: Boolean,
    onBack: () -> Unit,
    footer: @Composable () -> Unit = {},
    /** "Connect a new device"'s hub calls (a code for another device); null where it cannot be made (demo mode). */
    connect: DeviceSettings? = null,
) {
    val features = LocalHubFeatures.current
    val feedback = LocalAppFeedback.current
    val invalidBrowserLogin = stringResource(R.string.settings_browser_login_invalid)
    var loginRequest by remember { mutableStateOf<BrowserLoginRequest?>(null) }
    val scanner = androidx.activity.compose.rememberLauncherForActivityResult(com.journeyapps.barcodescanner.ScanContract()) { result ->
        result.contents?.let { value ->
            val request = browserLoginRequest(value)
            if (request != null) loginRequest = request
            else feedback?.notice(invalidBrowserLogin)
        }
    }
    val state by controller.state.collectAsState()
    val scope = rememberCoroutineScope()
    loginRequest?.let { request -> if (connect != null) BrowserLoginApproval(connect, request, scope) { if (loginRequest == request) loginRequest = null } }
    var pending by remember { mutableStateOf<PairedDevice?>(null) }
    var connecting by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(controller) { if (available) controller.loadDevices() }
    if (connecting && connect != null && available) {
        ConnectDevicePage(connect, onBack = { connecting = false }, onPaired = { scope.launch { controller.loadDevices() } })
        return
    }
    JListPage(title = stringResource(R.string.settings_devices_title), onBack = onBack) {
        if (!available) item { JEmptyState(stringResource(R.string.settings_devices_unavailable), stringResource(R.string.settings_check_pairing_and_hub)) }
        else {
            if (connect != null && "browser_qr_login" in features) item("scan-browser") {
                JButton(stringResource(R.string.settings_browser_login_scan), {
                    scanner.launch(com.journeyapps.barcodescanner.ScanOptions().setCaptureActivity(PairingScanActivity::class.java)
                        .addExtra(PairingScanActivity.DisablePhoto, true)
                        .setOrientationLocked(false).setDesiredBarcodeFormats(com.journeyapps.barcodescanner.ScanOptions.QR_CODE).setPrompt("").setBeepEnabled(false))
                }, kind = JButtonKind.Secondary, icon = PersonaIcons.scanCode)
            }
            if (connect != null) item("connect") {
                JGroupItem(GroupPosition.Single, Modifier.padding(bottom = Tokens.Space.m)) {
                    JRow(stringResource(R.string.settings_onb_connect_title), leadingIcon = PhIcons.QrCode,
                        subtitle = stringResource(R.string.settings_onb_connect_row), onClick = { connecting = true })
                }
            }
            if (state.devices == null && state.busy) item { JLoading(text = stringResource(R.string.settings_devices_loading)) }
            if (state.devices?.devices?.isEmpty() == true) item { JEmptyState(stringResource(R.string.settings_devices_empty), stringResource(R.string.settings_devices_empty_detail)) }
            val onlineIds = state.devices?.onlineIds.orEmpty()
            val groups = state.devices?.let(::groupPairedDevices).orEmpty()
            groups.forEach { group ->
                item("group-${group.kind.name}") {
                    JSectionHeader(stringResource(group.kind.titleRes()), count = group.devices.size)
                }
                items(group.devices, key = { it.id }) { device ->
                    val current = device.id == state.devices?.currentId
                    val online = !current && device.id in onlineIds
                    JGroupItem(GroupPosition.Single, Modifier.padding(bottom = Tokens.Space.m)) {
                        JRow(device.name.ifBlank { stringResource(R.string.settings_devices_unnamed) }, leadingIcon = device.deviceIcon(),
                            value = when {
                                current -> stringResource(R.string.settings_devices_this)
                                online -> stringResource(R.string.settings_devices_online)
                                else -> null
                            },
                            subtitle = stringResource(R.string.settings_devices_platform_role, device.platformLabel(current),
                                stringResource(when (device.role) {
                                    "full" -> R.string.settings_devices_role_full
                                    "answer" -> R.string.settings_devices_role_answer
                                    "scoped" -> R.string.settings_devices_role_scoped
                                    else -> R.string.settings_devices_role_unknown
                                })))
                        SettingsCaption(if (device.lastSeen.isNullOrBlank() && !device.createdAt.isNullOrBlank()) stringResource(R.string.settings_devices_added, lastUsed(device.createdAt))
                            else stringResource(R.string.settings_devices_last_used, lastUsed(device.lastSeen)), Modifier.padding(horizontal = Tokens.Space.m))
                        JButton(stringResource(if (current) R.string.settings_devices_revoke_this else R.string.settings_devices_revoke), { pending = device }, Modifier.padding(Tokens.Space.m),
                            kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = !state.busy && !state.needsCleanup)
                    }
                }
            }
            item { Action(stringResource(if (state.busy) R.string.settings_devices_working else R.string.settings_devices_refresh), enabled = !state.busy && !state.needsCleanup) { scope.launch { controller.loadDevices() } } }
            // Revoked credentials stay listed, greyed, for reference; they cannot connect or be revoked again.
            val revoked = state.devices?.revoked.orEmpty()
            if (revoked.isNotEmpty()) item("revoked-title") { JSectionHeader(stringResource(R.string.settings_devices_revoked), count = revoked.size) }
            itemsIndexed(revoked, key = { _, device -> "revoked-${device.id}" }) { index, device ->
                JGroupItem(GroupPosition.of(index, revoked.size)) {
                    JRow(device.name.ifBlank { stringResource(R.string.settings_devices_unnamed) }, leadingIcon = device.deviceIcon(), enabled = false,
                        subtitle = stringResource(R.string.settings_devices_revoked_at, lastUsed(device.revokedAt)))
                }
            }
            if (state.devices != null) item("credentials-note") {
                SettingsCaption(stringResource(R.string.settings_devices_note),
                    Modifier.padding(vertical = Tokens.Space.m))
            }
        }
        item { footer() }
    }
    pending?.let { device ->
        val current = device.id == state.devices?.currentId
        JDialog(onDismiss = { pending = null }, title = stringResource(if (current) R.string.settings_devices_revoke_this_title else R.string.settings_devices_revoke_title),
            text = stringResource(if (current) R.string.settings_devices_revoke_this_text else R.string.settings_devices_revoke_text),
            destructive = true, confirmEnabled = !state.busy,
            confirm = stringResource(R.string.settings_devices_revoke) to { pending = null; scope.launch { controller.revoke(device) }; Unit })
    }
}

@Composable
internal fun NotificationPreferences(
    controller: DeviceSettingsState,
    available: Boolean,
    desktops: List<persona.core.contracts.HubDesktop> = emptyList(),
    onDesktops: (() -> Unit)? = null,
): LazyListScope.() -> Unit {
    val state by controller.state.collectAsState()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    LaunchedEffect(controller) {
        if (available) {
            controller.loadPreferences()
            controller.loadLowDistraction()
        }
    }
    // The system's switch for the whole app (iOS: "Notifications are off" first): read again whenever the page comes back.
    var systemOff by remember { mutableStateOf(false) }
    androidx.lifecycle.compose.LifecycleEventEffect(androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
        systemOff = !androidx.core.app.NotificationManagerCompat.from(context).areNotificationsEnabled()
    }
    val prefs = state.preferences?.takeIf { available }
    val enabled = !state.busy && !state.needsCleanup
    return {
        if (systemOff) item("system-off") {
            JNotice(stringResource(R.string.settings_notify_system_off_text), Modifier.padding(top = Tokens.Space.s),
                title = stringResource(R.string.settings_notify_system_off), status = Status.NeedsYou, icon = PhIcons.BellSimple,
                actionLabel = stringResource(R.string.settings_notify_system_settings), onAction = { PhoneReadiness.open(context, listOf(notificationSettings)) })
        }
        item { JSectionHeader(stringResource(R.string.settings_notify_prefs_header)) }
        if (!available) item { JEmptyState(stringResource(R.string.settings_notify_prefs_unavailable), stringResource(R.string.settings_check_pairing_and_hub)) }
        if (prefs != null) {
            item {
                JGroupItem(GroupPosition.Single) {
                    JSwitchRow(stringResource(R.string.settings_notify_needs_you), prefs.input, { scope.launch { controller.savePreferences(prefs.copy(input = it)) } },
                        subtitle = stringResource(R.string.settings_notify_needs_you_subtitle), leadingIcon = PhIcons.BellSimple, enabled = enabled)
                }
            }
            item { SettingsCaption(stringResource(R.string.settings_notify_needs_you_footer), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s)) }
            item { JSectionHeader(stringResource(R.string.settings_notify_done_header)) }
            DoneNotification.entries.forEachIndexed { index, mode ->
                item {
                    JGroupItem(GroupPosition.of(index, DoneNotification.entries.size)) {
                        JRow(stringResource(when (mode) { DoneNotification.Off -> R.string.settings_notify_done_off; DoneNotification.Long -> R.string.settings_notify_done_long; DoneNotification.Always -> R.string.settings_notify_done_always }),
                            enabled = enabled, chevron = false,
                            onClick = { scope.launch { controller.savePreferences(prefs.copy(done = mode)) } },
                            trailing = { JRadio(selected = prefs.done == mode, onClick = null, enabled = enabled) })
                    }
                }
            }
            item {
                Column(Modifier.padding(vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    SettingsCaption(stringResource(R.string.settings_notify_seen_note))
                    prefs.longTurnSeconds?.let { SettingsCaption(pluralStringResource(R.plurals.settings_notify_long_turn, it, it)) }
                    prefs.doneDelaySeconds?.let { SettingsCaption(pluralStringResource(R.plurals.settings_notify_done_delay, it, it)) }
                }
            }
            if (prefs.quiet != null) quietHoursItems(controller)
            item {
                JGroupItem(GroupPosition.of(0, 2)) {
                    JSwitchRow(stringResource(R.string.settings_notify_live), prefs.liveActivity, { scope.launch { controller.savePreferences(prefs.copy(liveActivity = it)) } }, enabled = enabled)
                    SettingsCaption(stringResource(R.string.settings_notify_live_about),
                        Modifier.padding(horizontal = Tokens.Space.m).padding(bottom = Tokens.Space.m))
                }
            }
            item {
                JGroupItem(GroupPosition.of(1, 2)) {
                    JSwitchRow(stringResource(R.string.settings_notify_lock_details), prefs.lockDetails, { scope.launch { controller.savePreferences(prefs.copy(lockDetails = it)) } }, enabled = enabled)
                    SettingsCaption(stringResource(R.string.settings_notify_lock_details_about), Modifier.padding(horizontal = Tokens.Space.m).padding(bottom = Tokens.Space.m))
                }
            }
        }
        if (prefs?.muteWhileDesktop != null) desktopMuteItems(controller, desktops, onDesktops)
        else if (desktops.isNotEmpty() && onDesktops != null) item("desktops-presence") {
            // Older hubs still expose signed-in computers even when phone muting preferences are unavailable.
            DesktopPresenceRow(desktops, muted = false, selected = false, position = GroupPosition.Single, onClick = onDesktops)
        }
        // The hub's switch, for every device (#8157): after this phone's own rows.
        val lowDistraction = state.lowDistraction?.takeIf { available }
        if (lowDistraction != null) item("low-distraction") {
            Column(Modifier.padding(top = Tokens.Space.m)) {
                JGroupItem(GroupPosition.Single) {
                    JSwitchRow(stringResource(R.string.settings_notify_low_distraction), lowDistraction, { scope.launch { controller.saveLowDistraction(it) } },
                        subtitle = stringResource(R.string.settings_notify_low_distraction_subtitle), enabled = enabled)
                }
                SettingsCaption(stringResource(R.string.settings_notify_low_distraction_footer), Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
            }
        }
        if (available && state.preferences == null) item {
            Action(stringResource(if (state.busy) R.string.settings_reading else R.string.settings_retry_read), enabled = !state.busy) { scope.launch { controller.loadPreferences() } }
        }
        if (state.busy && state.preferences != null) item { SettingsCaption(stringResource(R.string.settings_saving)) }
    }
}

/**
 * This phone's quiet hours (#1863, iOS `quietHoursSection`): a switch, then From / Until. Each change sends only `quiet`, in
 * this phone's time zone; hours set in another zone say which until they are changed here.
 */
private fun LazyListScope.quietHoursItems(controller: DeviceSettingsState) {
    item("quiet") {
        val state by controller.state.collectAsState()
        val scope = rememberCoroutineScope()
        val quiet = state.preferences?.quiet ?: return@item
        val enabled = !state.busy && !state.needsCleanup
        // Which end is being picked: true From, false Until.
        var picking by remember { mutableStateOf<Boolean?>(null) }
        val rows = if (quiet.enabled) 3 else 1
        Column(Modifier.padding(top = Tokens.Space.s)) {
            JGroupItem(GroupPosition.of(0, rows)) {
                JSwitchRow(stringResource(R.string.settings_notify_quiet), quiet.enabled, { on -> scope.launch { controller.saveQuietHours(quiet.copy(enabled = on)) } },
                    subtitle = if (quiet.enabled) null else stringResource(R.string.settings_notify_quiet_off), leadingIcon = PhIcons.Moon, enabled = enabled)
            }
            if (quiet.enabled) {
                JGroupItem(GroupPosition.of(1, rows)) {
                    JRow(stringResource(R.string.settings_notify_quiet_start), value = quiet.start, enabled = enabled, onClick = { picking = true })
                }
                JGroupItem(GroupPosition.of(2, rows)) {
                    JRow(stringResource(R.string.settings_notify_quiet_end), value = quiet.end, enabled = enabled, onClick = { picking = false })
                }
            }
        }
        picking?.let { start ->
            val clock = if (start) quiet.start else quiet.end
            JTimePickerDialog(QuietHours.minutes(clock)?.let { LocalTime.of(it / 60, it % 60) }, { time ->
                val next = QuietHours.clock(time.hour * 60 + time.minute)
                if (next != clock) scope.launch { controller.saveQuietHours(if (start) quiet.copy(start = next) else quiet.copy(end = next)) }
            }, { picking = null }, title = stringResource(if (start) R.string.settings_notify_quiet_start else R.string.settings_notify_quiet_end))
        }
    }
    item("quiet-note") {
        val state by controller.state.collectAsState()
        val zone = state.preferences?.quiet?.tz?.let { elsewhere(it) }
        SettingsCaption(if (zone != null) stringResource(R.string.settings_notify_quiet_footer_zone, zone) else stringResource(R.string.settings_notify_quiet_footer),
            Modifier.padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.s))
    }
}

/** The name of [tz] when it isn't this phone's zone (the hours were set somewhere else); null when it is, or unknown. */
@Composable
private fun elsewhere(tz: String): String? {
    val zone = runCatching { ZoneId.of(tz) }.getOrNull() ?: return null
    if (zone.id == ZoneId.systemDefault().id) return null
    return zone.getDisplayName(TextStyle.FULL, currentLocale)
}

@Composable
internal fun AboutSettingsPage(
    commit: BuildCommit? = rememberBuildCommit(),
    developing: DevelopingDiagnostics? = null,
    onExport: () -> Unit = {},
    /** A test-only Build-row comparison; null preserves the existing handwritten row. */
    buildRow: (@Composable () -> Unit)? = null,
) {
    val context = LocalContext.current
    val info = remember(context) { context.packageManager.getPackageInfo(context.packageName, 0) }
    val count = if (developing == null) 3 else 4
    JGroupItem(GroupPosition.of(0, count)) { JRow(stringResource(R.string.settings_about_version), value = info.versionName ?: stringResource(R.string.settings_not_provided)) }
    JGroupItem(GroupPosition.of(1, count)) {
        if (buildRow == null) {
            JRow(stringResource(R.string.settings_about_build), trailing = { Text("${info.longVersionCode}", style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) })
        } else {
            buildRow()
        }
    }
    JGroupItem(GroupPosition.of(2, count)) { AboutBuildCommitRow(commit) }
    developing?.let { DevelopingPersonaAbout(it, GroupPosition.of(3, count), onExport) }
}

private fun DeviceGroupKind.titleRes(): Int = when (this) {
    DeviceGroupKind.Devices -> R.string.settings_devices_group_devices
    DeviceGroupKind.Browsers -> R.string.settings_devices_group_browsers
    DeviceGroupKind.Links -> R.string.settings_devices_group_links
    DeviceGroupKind.Integrations -> R.string.settings_devices_group_integrations
}

private fun PairedDevice.deviceIcon() = when (kind) {
    "phone", "watch" -> PersonaIcons.phone
    "web" -> PersonaIcons.safari
    "link" -> PersonaIcons.laptop
    else -> PersonaIcons.credentials
}

@Composable
private fun PairedDevice.platformLabel(current: Boolean): String = when {
    current -> "Android"
    platform == "android" || kind == "android" -> "Android"
    platform == "ios" || platform == "iphone" || kind == "iphone" -> "iPhone"
    platform == "watchos" || kind == "watch" -> stringResource(R.string.settings_devices_watch)
    kind == "web" -> stringResource(R.string.settings_devices_web)
    kind == "link" -> stringResource(R.string.settings_devices_link)
    kind == "phone" -> stringResource(R.string.settings_devices_phone_unknown)
    else -> stringResource(R.string.settings_devices_platform_unknown)
}

@Composable
private fun lastUsed(value: String?): String {
    if (value.isNullOrBlank()) return stringResource(R.string.settings_devices_no_record)
    // parseTime also reads offsets (+08:00), which Instant.parse rejects on Android 12 and 13.
    return persona.core.contracts.parseTime(value)?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, currentLocale) }
        ?: stringResource(R.string.settings_devices_no_time)
}
