package persona.feature.settings

import android.app.role.RoleManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import android.service.voice.VoiceInteractionService

/**
 * Checks whether Persona currently holds Android's Default Digital Assistant role (`ROLE_ASSISTANT`)
 * and opens the system digital assistant settings (`Settings.ACTION_VOICE_INPUT_SETTINGS`).
 */
internal object DigitalAssistantSettings {
    const val VOICE_INTERACTION_SERVICE_CLASS = "persona.android.assist.PersonaVoiceInteractionService"

    fun isDefaultAssistant(context: Context): Boolean {
        val roleHeld = runCatching {
            val roleManager = context.getSystemService(RoleManager::class.java)
            roleManager != null &&
                roleManager.isRoleAvailable(RoleManager.ROLE_ASSISTANT) &&
                roleManager.isRoleHeld(RoleManager.ROLE_ASSISTANT)
        }.getOrDefault(false)
        if (roleHeld) return true
        return runCatching {
            VoiceInteractionService.isActiveService(
                context,
                ComponentName(context.packageName, VOICE_INTERACTION_SERVICE_CLASS),
            )
        }.getOrDefault(false)
    }

    fun candidateIntents(packageName: String): List<Intent> = listOf(
        Intent(Settings.ACTION_VOICE_INPUT_SETTINGS),
        Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS),
        Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.fromParts("package", packageName, null)),
    )

    fun openSystemSettings(context: Context): Boolean = openSystemSettings(context.packageName) { intent ->
        context.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }

    internal fun openSystemSettings(packageName: String, start: (Intent) -> Unit): Boolean {
        for (intent in candidateIntents(packageName)) {
            val opened = runCatching { start(intent); true }.getOrDefault(false)
            if (opened) return true
        }
        return false
    }
}
