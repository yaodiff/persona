package persona.platform.shortcuts

import android.appwidget.AppWidgetManager
import android.content.Context
import androidx.core.content.edit
import org.json.JSONObject
import persona.core.contracts.SessionKey

/** Target conversation kind bound to a conversation-facing home-screen widget instance. */
enum class ConversationWidgetKind(val wire: String) {
    Main("main"),
    Leader("leader"),
    Assistant("assistant"),
    Agent("agent");

    companion object {
        fun fromWire(value: String?): ConversationWidgetKind =
            entries.firstOrNull { it.wire == value } ?: Main
    }
}

/** Per-`appWidgetId` conversation binding persisted in [ConversationWidgetPrefs]. */
data class ConversationWidgetBinding(
    val pairing: String,
    val machineId: String,
    val sessionId: String,
    val titleSnapshot: String,
    val kind: ConversationWidgetKind,
) {
    val sessionKey: SessionKey?
        get() = if (machineId.isNotBlank() && sessionId.isNotBlank()) SessionKey(machineId, sessionId) else null

    val isMain: Boolean
        get() = kind == ConversationWidgetKind.Main || sessionKey == null

    /** Stable cache key for isolating per-conversation snapshots inside [MainChatWidgetStore]. */
    val conversationKey: String
        get() = if (isMain) ConversationWidgetPrefs.MAIN_CONVERSATION_KEY
        else "${kind.wire}:${machineId}:${sessionId}"

    companion object {
        fun main(pairing: String = ""): ConversationWidgetBinding = ConversationWidgetBinding(
            pairing = pairing,
            machineId = "",
            sessionId = "",
            titleSnapshot = "",
            kind = ConversationWidgetKind.Main,
        )
    }
}

/**
 * Lightweight per-`appWidgetId` persistence for [MainChatWidget] and [AskPersonaWidget] in
 * SharedPreferences (`conversation_widget_bindings`). An unbound widget (no entry) defaults to the
 * main conversation so existing home-screen instances remain 100% backwards-compatible.
 */
internal object ConversationWidgetPrefs {
    const val PREFS_NAME = "conversation_widget_bindings"
    const val MAIN_CONVERSATION_KEY = "main"
    private const val KEY_PREFIX = "widget_"

    fun read(
        context: Context,
        appWidgetId: Int,
        activePairing: String? = null,
    ): ConversationWidgetBinding? {
        if (appWidgetId == AppWidgetManager.INVALID_APPWIDGET_ID) return null
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val raw = prefs.getString(keyFor(appWidgetId), null) ?: return null
        val binding = decode(raw) ?: return null
        val scope = activePairing?.takeIf(String::isNotBlank)
        if (scope != null && binding.pairing.isNotBlank() && binding.pairing != scope) return null
        return binding
    }

    fun save(context: Context, appWidgetId: Int, binding: ConversationWidgetBinding) {
        if (appWidgetId == AppWidgetManager.INVALID_APPWIDGET_ID) return
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        prefs.edit {
            val _ = putString(keyFor(appWidgetId), encode(binding))
        }
    }

    fun delete(context: Context, appWidgetIds: IntArray) {
        if (appWidgetIds.isEmpty()) return
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        prefs.edit {
            for (id in appWidgetIds) {
                if (id != AppWidgetManager.INVALID_APPWIDGET_ID) {
                    val _ = remove(keyFor(id))
                }
            }
        }
    }

    fun restore(context: Context, oldWidgetIds: IntArray?, newWidgetIds: IntArray?) {
        if (oldWidgetIds == null || newWidgetIds == null) return
        val count = minOf(oldWidgetIds.size, newWidgetIds.size)
        if (count == 0) return
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val migrated = LinkedHashMap<Int, String>()
        val toRemove = ArrayList<Int>(count)
        for (i in 0 until count) {
            val oldId = oldWidgetIds[i]
            val newId = newWidgetIds[i]
            if (oldId == AppWidgetManager.INVALID_APPWIDGET_ID || newId == AppWidgetManager.INVALID_APPWIDGET_ID) continue
            val raw = prefs.getString(keyFor(oldId), null) ?: continue
            migrated[newId] = raw
            toRemove += oldId
        }
        if (migrated.isEmpty()) return
        prefs.edit {
            for (oldId in toRemove) {
                val _ = remove(keyFor(oldId))
            }
            for ((newId, raw) in migrated) {
                val _ = putString(keyFor(newId), raw)
            }
        }
    }

    fun allBindings(context: Context, activePairing: String? = null): Map<Int, ConversationWidgetBinding> {
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        val scope = activePairing?.takeIf(String::isNotBlank)
        val result = LinkedHashMap<Int, ConversationWidgetBinding>()
        for ((key, value) in prefs.all) {
            if (!key.startsWith(KEY_PREFIX)) continue
            val id = key.removePrefix(KEY_PREFIX).toIntOrNull() ?: continue
            val raw = value as? String ?: continue
            val binding = decode(raw) ?: continue
            if (scope != null && binding.pairing.isNotBlank() && binding.pairing != scope) continue
            result[id] = binding
        }
        return result
    }

    fun clearAll(context: Context) {
        val prefs = context.applicationContext.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        prefs.edit { val _ = clear() }
    }

    internal fun encode(binding: ConversationWidgetBinding): String = JSONObject().apply {
        val _ = put("pairing", binding.pairing)
        val _ = put("machine_id", binding.machineId)
        val _ = put("session_id", binding.sessionId)
        val _ = put("title_snapshot", binding.titleSnapshot)
        val _ = put("kind", binding.kind.wire)
    }.toString()

    internal fun decode(raw: String): ConversationWidgetBinding? = runCatching {
        val obj = JSONObject(raw)
        ConversationWidgetBinding(
            pairing = obj.optString("pairing", ""),
            machineId = obj.optString("machine_id", ""),
            sessionId = obj.optString("session_id", ""),
            titleSnapshot = obj.optString("title_snapshot", ""),
            kind = ConversationWidgetKind.fromWire(obj.optString("kind", ConversationWidgetKind.Main.wire)),
        )
    }.getOrNull()

    private fun keyFor(appWidgetId: Int): String = "$KEY_PREFIX$appWidgetId"
}
