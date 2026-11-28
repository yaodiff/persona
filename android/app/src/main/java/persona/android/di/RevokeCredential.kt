package persona.android.di

import android.util.Log
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.withTimeout
import persona.core.hub.HubFailure
import persona.core.hub.OkHttpHubConnection

/**
 * Revokes this device's credential on its hub (#5538, as Apple's HubSignOut.swift #1824): the hub then drops the
 * device's pushes and Live Activities. Best-effort: a hub that cannot be reached is not retried, and the caller's
 * local cleanup still runs. Returns true when the credential is gone (including 401/404 already revoked), false when
 * the hub could not be reached so the caller can remember the id for `replaces` on the next pairing.
 */
internal suspend fun revokeCredential(connection: OkHttpHubConnection, deviceId: String): Boolean {
    if (deviceId.isBlank()) return true
    return try {
        withTimeout(10_000L) {
            connection.request("/api/v1/auth/tokens/$deviceId", "DELETE")
        }
        true
    } catch (error: CancellationException) { throw error }
    catch (error: HubFailure) {
        if (error.status == 404 || error.status == 401) true
        else {
            Log.w("RevokeCredential", "Could not revoke device credential on hub: ${error.message}")
            false
        }
    } catch (error: Exception) {
        Log.w("RevokeCredential", "Could not revoke device credential on hub: ${error.message}")
        false
    }
}
