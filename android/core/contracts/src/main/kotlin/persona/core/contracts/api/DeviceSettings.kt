package persona.core.contracts.api

import persona.core.contracts.CoreTextException
import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText

data class PairedDevice(
    val id: String,
    val name: String,
    val kind: String,
    val role: String,
    val lastSeen: String? = null,
    val platform: String? = null,
    val createdAt: String? = null,
    /** Set on credentials that were revoked; they can no longer connect or be revoked again. */
    val revokedAt: String? = null,
)

/** [devices] are the credentials in use; [revoked] the ones already revoked; [onlineIds] are currently connected. */
data class PairedDevices(
    val devices: List<PairedDevice>,
    val currentId: String?,
    val revoked: List<PairedDevice> = emptyList(),
    val onlineIds: Set<String> = emptySet(),
)

/** The hub cannot list device credentials: too old a version (404), or no database (503). [message] says which. */
class DeviceCredentialsUnavailable(text: CoreText) : CoreTextException(text)

enum class DoneNotification(val wire: String) { Off("off"), Long("long"), Always("always") }

data class DeviceNotificationPreferences(
    val input: Boolean = true,
    val done: DoneNotification = DoneNotification.Long,
    val lockDetails: Boolean = false,
    val longTurnSeconds: Int? = null,
    val doneDelaySeconds: Int? = null,
    val liveActivity: Boolean = true,
    /** What the hub has of this device's Settings › Translation; null from a hub without it. */
    val translate: HubTranslatePrefs? = null,
    /** "Silence phone": no "Needs you" / "Done" pushes while a desktop app is connected; null from a hub without it. */
    val muteWhileDesktop: Boolean? = null,
    /** The managers whose pushes this device does not get (设置 › 管理者 › 推送通知); null from a hub without them. */
    val mutedManagers: List<String>? = null,
    /** This device's quiet hours (设置 › 通知 › 免打扰时段); null from a hub without them. */
    val quiet: QuietHours? = null,
)

/**
 * A device's quiet hours (the prefs' `quiet`, docs/protocol/protocol.md): no "Agent finished" inside the window, and
 * "Needs you" without a sound. [start] and [end] are "HH:MM" in [tz] (an IANA zone; null: the user's zone the hub
 * knows); the window may cross midnight, and start = end is no window. Never set, the hub sends empty times: the web's
 * defaults stand in.
 */
data class QuietHours(
    val enabled: Boolean = false,
    val start: String = DEFAULT_START,
    val end: String = DEFAULT_END,
    val tz: String? = null,
) {
    companion object {
        const val DEFAULT_START = "23:00"
        const val DEFAULT_END = "07:00"

        /** "HH:MM" as minutes after midnight; null when it is not a time. */
        fun minutes(clock: String): Int? {
            val parts = clock.split(":")
            if (parts.size != 2) return null
            val h = parts[0].toIntOrNull() ?: return null
            val m = parts[1].toIntOrNull() ?: return null
            return if (h in 0..23 && m in 0..59) h * 60 + m else null
        }

        /** Minutes after midnight as "HH:MM". */
        fun clock(minutes: Int): String = Math.floorMod(minutes, 1440).let { "%02d:%02d".format(java.util.Locale.ROOT, it / 60, it % 60) }
    }
}

/** A browser sign-in request as this device sees it. [number] is present only for the device that allowed it. */
data class BrowserLoginDetails(
    val name: String,
    val source: String,
    val ageSeconds: Int,
    /** Null from a hub before number matching; such a hub keeps the legacy successful-Allow dismissal. */
    val state: String? = null,
    val network: String? = null,
    val number: String? = null,
)

/** Bound to one pairing; preferenceDeviceId is the ID used by notification registration. */
interface DeviceSettings {
    suspend fun browserLogin(id: String, hub: String, allow: Boolean? = null): BrowserLoginDetails =
        throw UnsupportedOperationException("browser-login")

    suspend fun devices(): PairedDevices
    suspend fun revoke(credentialId: String)
    suspend fun preferences(): DeviceNotificationPreferences
    suspend fun savePreferences(preferences: DeviceNotificationPreferences): DeviceNotificationPreferences

    /**
     * Settings › Devices › "Connect a new device": `POST /api/v1/auth/pair-codes` with the page address of this hub (docs/protocol/protocol.md "Temporary pairing"),
     * a one-time code for another app (its QR rows) and the browser sign-in link.
     */
    suspend fun pairCode(): AppPairCode = throw UnsupportedOperationException("pair-codes")

    /**
     * 设置 › 管理者 › 推送通知: `PUT /api/v1/devices/{id}/prefs {"muted_managers": [...]}` (only that field: the others
     * stay as they are); the hub's list as it is now.
     */
    suspend fun saveMutedManagers(ids: List<String>): List<String> = throw UnsupportedOperationException("muted_managers")

    /**
     * 设置 › 通知 › 免打扰时段: `PUT /api/v1/devices/{id}/prefs {"quiet": {...}}` (only that field); the hub's window as it
     * is now.
     */
    suspend fun saveQuietHours(quiet: QuietHours): QuietHours = throw UnsupportedOperationException("quiet")

    /**
     * 设置 › 通知 › 低打扰模式 (#8157): the hub's push grading switch, `GET /api/v1/push/ln-mode`, for every device of the
     * owner, not this one alone; null from a hub without it (404 / 405), which shows no row.
     */
    suspend fun lowDistraction(): Boolean? = null

    /** `PUT /api/v1/push/ln-mode {"enabled": on}`; the hub's switch as it is now. */
    suspend fun saveLowDistraction(on: Boolean): Boolean = throw UnsupportedOperationException("ln-mode")
}

/** A QR code the hub drew: [rows] of `0` / `1`, no quiet zone. */
data class QrImage(val size: Int, val rows: List<String>) {
    /** Whether it can be drawn: square, only 0 and 1. */
    val valid: Boolean get() = size > 0 && rows.size == size && rows.all { r -> r.length == size && r.all { it == '0' || it == '1' } }
}

/**
 * A one-time pairing code (ten minutes, traded once): [pairUrl] and its [qr] for an app, [webUrl] and [webQr] for a
 * browser (an older hub leaves the QR codes and the web link out). [loopbackOnly]: the hub's address this phone uses is
 * one only it can open (an SSH tunnel's 127.0.0.1), so the browser link does nothing elsewhere.
 */
data class AppPairCode(
    val token: String,
    /** Epoch milliseconds; 0 when the hub's time could not be read. */
    val expiresAt: Long,
    val pairUrl: String,
    val qr: QrImage? = null,
    val webUrl: String? = null,
    val webQr: QrImage? = null,
    val loopbackOnly: Boolean = false,
)
