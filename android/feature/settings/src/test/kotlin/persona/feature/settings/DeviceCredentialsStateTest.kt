package persona.feature.settings

import persona.core.contracts.api.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class DeviceCredentialsStateTest {
    private open class Api(var list: PairedDevices = PairedDevices(emptyList(), null)) : DeviceSettings {
        var failure: Exception? = null
        override suspend fun devices(): PairedDevices { failure?.let { throw it }; return list }
        override suspend fun revoke(credentialId: String) = Unit
        override suspend fun preferences() = DeviceNotificationPreferences()
        override suspend fun savePreferences(preferences: DeviceNotificationPreferences) = preferences
    }

    @Test fun aRevokedDeviceMovesToTheRevokedListAndHubsWithoutCredentialsSayWhy() = runBlocking {
        val phone = PairedDevice("phone", "示例手机", "phone", "full")
        val watch = PairedDevice("watch", "示例手表", "watch", "answer")
        val old = PairedDevice("old", "旧手机", "phone", "full", revokedAt = "2026-01-02T00:00:00Z")
        val api = Api(PairedDevices(listOf(phone, watch), phone.id, listOf(old)))
        val controller = DeviceSettingsState(api) {}
        controller.loadDevices()
        controller.revoke(watch)
        val devices = controller.state.value.devices!!
        assertEquals(listOf("phone"), devices.devices.map { it.id })
        assertEquals(listOf("watch", "old"), devices.revoked.map { it.id })
        assertNotNull(devices.revoked.first().revokedAt)

        api.failure = DeviceCredentialsUnavailable(persona.core.contracts.CoreText.plain("这个 hub 版本还不支持设备凭据。"))
        val unsupported = DeviceSettingsState(api) {}
        unsupported.loadDevices()
        assertEquals(SettingsUiText.Raw("这个 hub 版本还不支持设备凭据。"), unsupported.state.value.message)
        assertNull(unsupported.state.value.devices)
        api.failure = IllegalStateException("boom")
        unsupported.loadDevices()
        assertEquals(SettingsUiText.Res(R.string.settings_devices_load_failed), unsupported.state.value.message)
    }

    @Test fun groupPairedDevicesMatchesTaxonomyAndSortsCurrentOnlineRecent() {
        val paired = PairedDevices(
            devices = listOf(
                PairedDevice("phone-old", "Old Phone", "phone", "full", lastSeen = "2026-11-01T10:00:00Z"),
                PairedDevice("phone-live", "Live Phone", "phone", "full", lastSeen = "2026-10-01T10:00:00Z"),
                PairedDevice("phone-me", "This Phone", "phone", "full", lastSeen = "2026-09-01T10:00:00Z"),
                PairedDevice("web-1", "Browser", "web", "full", lastSeen = "2026-11-05T10:00:00Z"),
                PairedDevice("link-off", "Link Off", "link", "link", lastSeen = "2026-11-06T10:00:00Z"),
                PairedDevice("link-on", "Link On", "link", "link", lastSeen = "2026-11-01T10:00:00Z"),
                PairedDevice("scoped-1", "Scoped", "web", "scoped", lastSeen = "2026-11-06T10:00:00Z"),
            ),
            currentId = "phone-me",
            onlineIds = setOf("phone-live", "link-on"),
        )
        val groups = groupPairedDevices(paired)
        assertEquals(
            listOf(DeviceGroupKind.Devices, DeviceGroupKind.Browsers, DeviceGroupKind.Links, DeviceGroupKind.Integrations),
            groups.map { it.kind },
        )
        assertEquals(listOf("phone-me", "phone-live", "phone-old"), groups[0].devices.map { it.id })
        assertEquals(listOf("web-1"), groups[1].devices.map { it.id })
        assertEquals(listOf("link-on", "link-off"), groups[2].devices.map { it.id })
        assertEquals(listOf("scoped-1"), groups[3].devices.map { it.id })
    }
}
