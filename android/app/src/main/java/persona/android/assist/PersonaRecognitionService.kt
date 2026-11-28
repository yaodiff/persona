package persona.android.assist

import android.content.Intent
import android.speech.RecognitionService
import android.speech.SpeechRecognizer

/**
 * Minimal RecognitionService referenced by `@xml/voice_interaction_service` to satisfy Android's
 * `VoiceInteractionServiceInfo` parser across API 30+ without registering Persona as a system STT engine.
 */
class PersonaRecognitionService : RecognitionService() {
    override fun onStartListening(recognizerIntent: Intent?, listener: Callback?) {
        val _ = runCatching { listener?.error(SpeechRecognizer.ERROR_CLIENT) }
    }

    override fun onCancel(listener: Callback?) = Unit

    override fun onStopListening(listener: Callback?) {
        val _ = runCatching { listener?.error(SpeechRecognizer.ERROR_CLIENT) }
    }
}
