package persona.android.assist

import android.os.Bundle
import android.service.voice.VoiceInteractionService

/**
 * Registers Persona as an Android Default Digital Assistant (`ROLE_ASSISTANT`), compatible with
 * system gesture triggers and Samsung One UI side-button long-press.
 *
 * When invoked from the keyguard, passes `showFlags = 0` (no screenshot or assist structure flags)
 * so [PersonaVoiceInteractionSession] presents its unlock prompt without capturing locked content.
 */
class PersonaVoiceInteractionService : VoiceInteractionService() {
    override fun onLaunchVoiceAssistFromKeyguard() {
        val _ = runCatching {
            showSession(Bundle(), 0)
        }
    }
}
