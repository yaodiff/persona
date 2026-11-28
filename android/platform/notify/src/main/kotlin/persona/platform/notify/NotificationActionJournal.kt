package persona.platform.notify

import android.content.Context
import dagger.hilt.android.qualifiers.ApplicationContext
import java.security.MessageDigest
import java.util.UUID
import javax.inject.Inject
import javax.inject.Singleton

/** Stores only request digests and operation IDs, never answers or rejection reasons. */
@Singleton
class NotificationActionJournal @Inject constructor(@ApplicationContext context: Context) {
    private val preferences = context.getSharedPreferences("notification_actions", Context.MODE_PRIVATE)

    @Synchronized
    internal fun claim(id: String, createdAt: String? = null): Boolean {
        val digest = digest(id, createdAt)
        if (preferences.contains(digest)) return false
        // Commit before the HTTP write so process death cannot turn a second tap into a replay.
        check(preferences.edit().putString(digest, UUID.randomUUID().toString()).commit()) {
            "Could not persist notification operation"
        }
        return true
    }

    /**
     * Whether this phone already claimed [id], that is, sent its answer or was about to; only reads, never claims. A request
     * gone from the hub that this phone claimed was most likely answered by this phone, not on the computer (#2483).
     */
    @Synchronized
    internal fun claimed(id: String, createdAt: String? = null): Boolean =
        preferences.contains(digest(id, createdAt)) || createdAt != null && preferences.contains(digest(id))

    private fun digest(id: String, createdAt: String? = null) = MessageDigest.getInstance("SHA-256")
        .digest((if (createdAt == null) id else "$id\u0000$createdAt").toByteArray())
        .joinToString("") { "%02x".format(it) }
}
