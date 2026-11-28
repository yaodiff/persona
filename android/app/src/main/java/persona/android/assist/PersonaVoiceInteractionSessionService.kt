package persona.android.assist

import android.os.Bundle
import android.service.voice.VoiceInteractionSession
import android.service.voice.VoiceInteractionSessionService

/** Hosts active digital-assistant sessions for [PersonaVoiceInteractionService]. */
class PersonaVoiceInteractionSessionService : VoiceInteractionSessionService() {
    override fun onNewSession(args: Bundle?): VoiceInteractionSession = PersonaVoiceInteractionSession(this)
}
