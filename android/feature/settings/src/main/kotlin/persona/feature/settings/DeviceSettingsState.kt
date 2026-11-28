package persona.feature.settings

import androidx.annotation.StringRes
import persona.core.contracts.api.*
import persona.core.contracts.parseTime
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow

/** Grouping for Settings › Devices, aligned with web and Apple. */
enum class DeviceGroupKind { Devices, Browsers, Links, Integrations }

data class DeviceGroup(val kind: DeviceGroupKind, val devices: List<PairedDevice>)

fun deviceGroupOf(device: PairedDevice): DeviceGroupKind = when {
    device.role == "scoped" -> DeviceGroupKind.Integrations
    device.kind == "web" -> DeviceGroupKind.Browsers
    device.kind == "link" -> DeviceGroupKind.Links
    device.kind == "phone" || device.kind == "watch" || (device.kind == "other" && device.role == "full") -> DeviceGroupKind.Devices
    else -> DeviceGroupKind.Integrations
}

fun groupPairedDevices(devices: PairedDevices): List<DeviceGroup> {
    val buckets = DeviceGroupKind.entries.associateWith { mutableListOf<PairedDevice>() }
    for (device in devices.devices) {
        buckets.getValue(deviceGroupOf(device)).add(device)
    }
    val currentId = devices.currentId
    val onlineIds = devices.onlineIds
    val order = Comparator<PairedDevice> { a, b ->
        when {
            a.id == currentId && b.id != currentId -> -1
            b.id == currentId && a.id != currentId -> 1
            (a.id in onlineIds) != (b.id in onlineIds) -> if (a.id in onlineIds) -1 else 1
            else -> {
                val ta = (a.lastSeen?.takeIf(String::isNotBlank) ?: a.createdAt)?.let(::parseTime)?.toEpochMilli() ?: Long.MIN_VALUE
                val tb = (b.lastSeen?.takeIf(String::isNotBlank) ?: b.createdAt)?.let(::parseTime)?.toEpochMilli() ?: Long.MIN_VALUE
                tb.compareTo(ta).takeIf { it != 0 } ?: a.id.compareTo(b.id)
            }
        }
    }
    return DeviceGroupKind.entries.mapNotNull { kind ->
        val items = buckets.getValue(kind)
        if (items.isEmpty()) null else DeviceGroup(kind, items.sortedWith(order))
    }
}

data class DeviceSettingsUiState(
    val devices: PairedDevices? = null,
    val preferences: DeviceNotificationPreferences? = null,
    /** The hub's low-distraction switch (#8157), for every device; null until read and on a hub without it. */
    val lowDistraction: Boolean? = null,
    val busy: Boolean = false,
    val message: SettingsUiText? = null,
    val needsCleanup: Boolean = false,
)

/** All mutations are serialized on the UI dispatcher; the host owns scoped unpair cleanup. */
class DeviceSettingsState(private val api: DeviceSettings?, private val unpair: suspend () -> Unit) {
    private val mutable = MutableStateFlow(DeviceSettingsUiState())
    val state = mutable.asStateFlow()

    suspend fun loadDevices() = perform(R.string.settings_devices_load_failed) {
        try {
            mutable.value = mutable.value.copy(devices = requireNotNull(api).devices())
        } catch (unavailable: DeviceCredentialsUnavailable) {
            // Not a passing failure: say why this hub has no list (an older version, no database).
            mutable.value = mutable.value.copy(message = unavailable.message?.let(SettingsUiText::Raw))
        }
    }

    suspend fun loadPreferences() = perform(R.string.settings_notify_prefs_load_failed) {
        mutable.value = mutable.value.copy(preferences = requireNotNull(api).preferences())
    }

    /** Reads the hub's low-distraction switch; a hub without it, or one that does not answer, shows no row. */
    suspend fun loadLowDistraction() {
        val on = try { api?.lowDistraction() } catch (error: CancellationException) { throw error } catch (_: Exception) { null }
        mutable.value = mutable.value.copy(lowDistraction = on)
    }

    /** Shown at once; the hub's answer then stands, and a refusal puts the switch back and says so. */
    suspend fun saveLowDistraction(on: Boolean) {
        val previous = mutable.value.lowDistraction
        if (mutable.value.busy || previous == null || previous == on) return
        perform(R.string.settings_notify_low_distraction_save_failed) {
            mutable.value = mutable.value.copy(lowDistraction = on)
            try {
                mutable.value = mutable.value.copy(lowDistraction = requireNotNull(api).saveLowDistraction(on))
            } catch (error: Exception) {
                mutable.value = mutable.value.copy(lowDistraction = previous)
                throw error
            }
        }
    }

    suspend fun savePreferences(next: DeviceNotificationPreferences) {
        if (mutable.value.busy || mutable.value.preferences == null) return
        val previous = mutable.value.preferences
        perform(R.string.settings_notify_prefs_save_failed) {
            mutable.value = mutable.value.copy(preferences = next)
            try {
                mutable.value = mutable.value.copy(preferences = requireNotNull(api).savePreferences(next))
            } catch (error: Exception) {
                mutable.value = mutable.value.copy(preferences = previous)
                throw error
            }
        }
    }

    /** Only `quiet` goes, in this phone's time zone [zone] (as the web and iOS save it): the other prefs stay as they are. */
    suspend fun saveQuietHours(next: QuietHours, zone: String = java.time.ZoneId.systemDefault().id) {
        val previous = mutable.value.preferences
        if (mutable.value.busy || previous?.quiet == null) return
        val quiet = next.copy(tz = zone)
        perform(R.string.settings_notify_prefs_save_failed) {
            mutable.value = mutable.value.copy(preferences = previous.copy(quiet = quiet))
            try {
                val saved = requireNotNull(api).saveQuietHours(quiet)
                mutable.value = mutable.value.copy(preferences = mutable.value.preferences?.copy(quiet = saved))
            } catch (error: Exception) {
                mutable.value = mutable.value.copy(preferences = previous)
                throw error
            }
        }
    }

    suspend fun revoke(device: PairedDevice) = perform(R.string.settings_devices_revoke_failed) {
        val devices = requireNotNull(mutable.value.devices)
        requireNotNull(api).revoke(device.id)
        mutable.value = mutable.value.copy(devices = devices.copy(devices = devices.devices.filterNot { it.id == device.id },
            revoked = listOf(device.copy(revokedAt = java.time.Instant.now().toString())) + devices.revoked))
        if (device.id == devices.currentId) cleanup()
    }

    /** Revokes another device's credential by id (a desktop app's "Sign out"): it has to pair again; the list is not needed. */
    suspend fun revokeCredential(id: String) = perform(R.string.settings_devices_revoke_failed) {
        if (id.isBlank()) return@perform
        requireNotNull(api).revoke(id)
        mutable.value.devices?.let { devices ->
            mutable.value = mutable.value.copy(devices = devices.copy(devices = devices.devices.filterNot { it.id == id }))
        }
    }

    suspend fun disconnect() = perform(R.string.settings_disconnect_failed) { cleanup() }

    private suspend fun cleanup() {
        mutable.value = mutable.value.copy(needsCleanup = true)
        unpair()
        mutable.value = DeviceSettingsUiState(message = SettingsUiText.Res(R.string.settings_disconnected))
    }

    private suspend fun perform(@StringRes failure: Int, block: suspend () -> Unit) {
        if (mutable.value.busy) return
        mutable.value = mutable.value.copy(busy = true, message = null)
        try { block() }
        catch (error: CancellationException) { throw error }
        catch (_: Exception) {
            mutable.value = mutable.value.copy(message = SettingsUiText.Res(if (mutable.value.needsCleanup)
                R.string.settings_cleanup_failed else failure))
        } finally { mutable.value = mutable.value.copy(busy = false) }
    }
}
