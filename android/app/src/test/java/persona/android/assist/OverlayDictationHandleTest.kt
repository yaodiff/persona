package persona.android.assist

import android.app.Application
import android.content.Intent
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowSpeechRecognizer

@RunWith(RobolectricTestRunner::class)
@Config(application = Application::class, sdk = [35])
class OverlayDictationHandleTest {
    private val stoppedMessage = "Voice input stopped"

    private fun dictationIntent(language: String = "zh-CN"): Intent =
        Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            putExtra(RecognizerIntent.EXTRA_LANGUAGE, language)
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
        }

    private fun wordsBundle(text: String): Bundle = Bundle().apply {
        putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf(text))
    }

    @Test
    fun partialResultsUpdateLivePreviewIncludingUnstableTextAndIgnoreBlankPartials() {
        val engines = mutableListOf<FakeOverlaySpeechEngine>()
        val handle = OverlayDictationHandle(
            preferOnDevice = false,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { null },
            createSystemEngine = { FakeOverlaySpeechEngine().also { engines += it } },
            intent = dictationIntent("zh-CN"),
            onHeard = { {} },
            onError = { {} },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        assertTrue(handle.listening)
        assertEquals("", handle.partialText)

        // Google NetworkSpeechRecognizer often starts with unstable text before stabilizing RESULTS_RECOGNITION.
        engines.single().emitPartial(stable = "", unstable = "今天")
        assertEquals("今天", handle.partialText)

        engines.single().emitPartial(stable = "今天天气", unstable = "怎么样")
        assertEquals("今天天气怎么样", handle.partialText)

        // Duplicate unstable suffix must not double up.
        engines.single().emitPartial(stable = "今天天气怎么样", unstable = "怎么样")
        assertEquals("今天天气怎么样", handle.partialText)

        // Trailing blank partial bundles must never wipe out the latest recognized partial text.
        engines.single().emitPartial(stable = "   ", unstable = "")
        assertEquals("今天天气怎么样", handle.partialText)
    }

    @Test
    fun finishStopsListeningWithoutDestroyingThenDeliversFinalOnResultsAndDestroys() {
        val engines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = false,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { null },
            createSystemEngine = { FakeOverlaySpeechEngine().also { engines += it } },
            intent = dictationIntent("zh-CN"),
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        handle.start()
        engines.single().emitPartial("明天帮我查一下")
        assertEquals("明天帮我查一下", handle.partialText)

        // Graceful finish must only stop mic capture so the recognizer can flush final results.
        handle.finish()
        assertEquals(1, engines.single().stopCalls)
        assertEquals(0, engines.single().destroyCalls)
        assertTrue("Handle stays active while awaiting final onResults", handle.listening)
        assertEquals("明天帮我查一下", handle.partialText)
        assertEquals(listOf(OVERLAY_DICTATION_FINISH_TIMEOUT_MS), timeouts.scheduledDelays)
        assertTrue(heard.isEmpty())

        // Repeated finish() while already finishing is idempotent and must not double-stop.
        handle.finish()
        assertEquals(1, engines.single().stopCalls)
        assertEquals(0, engines.single().destroyCalls)
        assertEquals(1, timeouts.scheduledDelays.size)

        // Final onResults arrives -> delivers words once, cancels timer, then destroys engine.
        engines.single().emitResults("明天帮我查一下北京天气")
        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(1, engines.single().destroyCalls)
        assertEquals(1, timeouts.cancelledCount)
        assertEquals(listOf("明天帮我查一下北京天气"), heard)
        assertTrue(errors.isEmpty())

        // Stale duplicate callbacks or timeout firing after completion must never double-deliver.
        engines.single().emitResults("重复回调")
        engines.single().emitError(SpeechRecognizer.ERROR_SERVER)
        timeouts.fireLatest()
        assertEquals(listOf("明天帮我查一下北京天气"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun finishTimeoutCommitsLatestPartialTextAndDestroysWithoutDoubleDelivery() {
        val engines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = false,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { null },
            createSystemEngine = { FakeOverlaySpeechEngine().also { engines += it } },
            intent = dictationIntent("zh-CN"),
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        handle.start()
        engines.single().emitPartial("总结一下今天会议")
        handle.finish()
        assertEquals(0, engines.single().destroyCalls)

        // Engine never sends onResults within 2.5s -> timeout commits latest partial text and destroys.
        timeouts.fireLatest()
        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(1, engines.single().destroyCalls)
        assertEquals(listOf("总结一下今天会议"), heard)
        assertTrue(errors.isEmpty())

        // Late onResults arriving after timeout must be ignored so words are never committed twice.
        engines.single().emitResults("总结一下今天会议")
        assertEquals(listOf("总结一下今天会议"), heard)
    }

    @Test
    fun finishNoMatchAndSpeechTimeoutCommitLatestPartialTextWithoutError() {
        val engines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = false,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { null },
            createSystemEngine = { FakeOverlaySpeechEngine().also { engines += it } },
            intent = dictationIntent("zh-CN"),
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        // Case 1: ERROR_NO_MATCH during finishing rescues non-empty partialText.
        handle.start()
        engines.last().emitPartial("打开日程")
        handle.finish()
        engines.last().emitError(SpeechRecognizer.ERROR_NO_MATCH)
        assertFalse(handle.listening)
        assertEquals(1, engines.last().destroyCalls)
        assertEquals(listOf("打开日程"), heard)
        assertTrue(errors.isEmpty())

        // Case 2: ERROR_SPEECH_TIMEOUT during finishing rescues non-empty partialText.
        handle.start()
        engines.last().emitPartial("提醒我开会")
        handle.finish()
        engines.last().emitError(SpeechRecognizer.ERROR_SPEECH_TIMEOUT)
        assertFalse(handle.listening)
        assertEquals(1, engines.last().destroyCalls)
        assertEquals(listOf("打开日程", "提醒我开会"), heard)
        assertTrue(errors.isEmpty())

        // Case 3: ERROR_NO_MATCH during finishing with empty partialText stops quietly without error.
        handle.start()
        handle.finish()
        engines.last().emitError(SpeechRecognizer.ERROR_NO_MATCH)
        assertFalse(handle.listening)
        assertEquals(1, engines.last().destroyCalls)
        assertEquals(listOf("打开日程", "提醒我开会"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun cancelDestroysImmediatelyAndIgnoresLatePartialOrFinalCallbacks() {
        val engines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = false,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { null },
            createSystemEngine = { FakeOverlaySpeechEngine().also { engines += it } },
            intent = dictationIntent("zh-CN"),
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        // Cancel while actively listening (e.g. overlay dismissed / ON_STOP / swipe cancel).
        handle.start()
        val firstEngine = engines.last()
        firstEngine.emitPartial("不要提交这段话")
        handle.cancel()

        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(1, firstEngine.stopCalls)
        assertEquals(1, firstEngine.destroyCalls)
        assertTrue(heard.isEmpty())

        firstEngine.emitPartial("迟到预览")
        firstEngine.emitResults("迟到结果")
        firstEngine.emitError(SpeechRecognizer.ERROR_SERVER)
        assertEquals("", handle.partialText)
        assertTrue(heard.isEmpty())
        assertTrue(errors.isEmpty())

        // Cancel while in Finishing state cancels timeout, destroys immediately, and drops partial text.
        handle.start()
        val secondEngine = engines.last()
        secondEngine.emitPartial("收尾中被取消")
        handle.finish()
        assertEquals(0, secondEngine.destroyCalls)

        handle.cancel()
        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(1, secondEngine.destroyCalls)
        assertEquals(1, timeouts.cancelledCount)

        timeouts.fireLatest()
        secondEngine.emitResults("取消后迟到结果")
        assertTrue(heard.isEmpty())
        assertTrue(errors.isEmpty())
    }

    @Test
    fun onDeviceLanguageNotSupportedFallsBackToSystemPartialPreviewAndReleaseFinishDeliversWords() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        // Full S25+ flow: hold -> on-device error 12 -> system fallback -> live partial -> release finish -> final result.
        handle.start()
        assertTrue(handle.listening)
        assertEquals(1, onDeviceEngines.size)
        assertEquals(0, systemEngines.size)

        onDeviceEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)

        assertEquals(1, onDeviceEngines.single().stopCalls)
        assertEquals(1, onDeviceEngines.single().destroyCalls)
        assertEquals(1, systemEngines.size)
        assertEquals(1, systemEngines.single().startedIntents.size)
        assertSame(intent, systemEngines.single().startedIntents.single())
        assertTrue("Fallback should keep listening seamlessly", handle.listening)
        assertTrue("Seamless fallback must not surface an error notice", errors.isEmpty())

        systemEngines.single().emitPartial("你好")
        assertEquals("你好", handle.partialText)

        handle.finish()
        assertEquals(1, systemEngines.single().stopCalls)
        assertEquals(0, systemEngines.single().destroyCalls)
        assertTrue(handle.listening)

        systemEngines.single().emitResults("你好，帮我看下日历")
        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(listOf("你好，帮我看下日历"), heard)
        assertTrue(errors.isEmpty())
        assertEquals(1, systemEngines.single().destroyCalls)

        // Subsequent hold in the same session goes directly to the system default recognizer.
        handle.start()
        assertTrue(handle.listening)
        assertEquals(1, onDeviceEngines.size)
        assertEquals(2, systemEngines.size)

        handle.finish()
        systemEngines.last().emitResults("第二句语音")
        assertFalse(handle.listening)
        assertEquals(listOf("你好，帮我看下日历", "第二句语音"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun onDeviceLanguageUnavailableFallsBackToSystemRecognizerAndFinishDeliversResults() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        val timeouts = FakeTimeoutScheduler()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = timeouts::schedule,
        )

        handle.start()
        onDeviceEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE)

        assertEquals(1, onDeviceEngines.single().destroyCalls)
        assertEquals(1, systemEngines.size)
        assertTrue(handle.listening)
        assertTrue(errors.isEmpty())

        systemEngines.single().emitPartial("downloaded pack")
        handle.finish()
        assertEquals(0, systemEngines.single().destroyCalls)
        systemEngines.single().emitResults("downloaded pack fallback")

        assertFalse(handle.listening)
        assertEquals(1, systemEngines.single().destroyCalls)
        assertEquals(listOf("downloaded pack fallback"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun onDeviceStartListeningFailureFallsBackToSystemRecognizer() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = {
                FakeOverlaySpeechEngine(
                    onStartListening = { _, _ -> throw IllegalStateException("on-device unavailable") },
                ).also { onDeviceEngines += it }
            },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        assertEquals(1, onDeviceEngines.size)
        assertEquals(1, onDeviceEngines.single().destroyCalls)
        assertEquals(1, systemEngines.size)
        assertTrue(handle.listening)
        assertTrue(errors.isEmpty())

        handle.finish()
        systemEngines.single().emitResults("recovered after start exception")
        assertFalse(handle.listening)
        assertEquals(listOf("recovered after start exception"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun onDeviceCreationFailureFallsBackToSystemRecognizer() {
        val intent = dictationIntent("zh-CN")
        var onDeviceAttempts = 0
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = {
                onDeviceAttempts++
                null
            },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        assertEquals(1, onDeviceAttempts)
        assertEquals(1, systemEngines.size)
        assertTrue(handle.listening)
        assertTrue(errors.isEmpty())

        systemEngines.single().emitResults("created system fallback")
        assertFalse(handle.listening)
        assertEquals(listOf("created system fallback"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun fallbackFailureNotifiesErrorOnceWithoutInfiniteRetry() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { {} },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        onDeviceEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)
        assertEquals(1, systemEngines.size)
        assertTrue(handle.listening)
        assertTrue(errors.isEmpty())

        // Even if the system fallback also fails with ERROR_LANGUAGE_NOT_SUPPORTED, stop once and report.
        systemEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)

        assertFalse(handle.listening)
        assertEquals(1, onDeviceEngines.size)
        assertEquals(1, systemEngines.size)
        assertEquals(1, systemEngines.single().destroyCalls)
        assertEquals(listOf(stoppedMessage), errors)
    }

    @Test
    fun fallbackStartFailureNotifiesErrorOnceWithoutInfiniteRetry() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = {
                FakeOverlaySpeechEngine(
                    onStartListening = { _, _ -> throw IllegalStateException("system recognizer busy") },
                ).also { systemEngines += it }
            },
            intent = intent,
            onHeard = { {} },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        onDeviceEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE)

        assertFalse(handle.listening)
        assertEquals(1, onDeviceEngines.size)
        assertEquals(1, systemEngines.size)
        assertEquals(1, systemEngines.single().destroyCalls)
        assertEquals(listOf(stoppedMessage), errors)
    }

    @Test
    fun noSystemRecognizerReportsErrorOnceWithoutFallback() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        var systemCreations = 0
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { false },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = {
                systemCreations++
                FakeOverlaySpeechEngine()
            },
            intent = intent,
            onHeard = { {} },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        onDeviceEngines.single().emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)

        assertFalse(handle.listening)
        assertEquals(0, systemCreations)
        assertEquals(1, onDeviceEngines.single().destroyCalls)
        assertEquals(listOf(stoppedMessage), errors)
    }

    @Test
    fun noMatchAndSpeechTimeoutStopQuietlyWithoutFallbackOrError() {
        val intent = dictationIntent("en-US")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        var systemCreations = 0
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = {
                systemCreations++
                FakeOverlaySpeechEngine()
            },
            intent = intent,
            onHeard = { {} },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        onDeviceEngines.last().emitError(SpeechRecognizer.ERROR_NO_MATCH)
        assertFalse(handle.listening)
        assertEquals(0, systemCreations)
        assertTrue(errors.isEmpty())

        // Silence timeout must not disable on-device recognition for later attempts.
        handle.start()
        assertEquals(2, onDeviceEngines.size)
        onDeviceEngines.last().emitError(SpeechRecognizer.ERROR_SPEECH_TIMEOUT)
        assertFalse(handle.listening)
        assertEquals(0, systemCreations)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun staleCallbacksFromDestroyedOnDeviceEngineAreIgnoredAfterFallback() {
        val intent = dictationIntent("zh-CN")
        val onDeviceEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = { FakeOverlaySpeechEngine().also { onDeviceEngines += it } },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        val staleOnDevice = onDeviceEngines.single()
        staleOnDevice.emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)

        // Late events from the torn-down on-device recognizer must not disturb the active fallback session.
        staleOnDevice.emitPartial("stale partial")
        staleOnDevice.emitError(SpeechRecognizer.ERROR_CLIENT)
        staleOnDevice.emitResults("stale text")

        assertTrue(handle.listening)
        assertEquals("", handle.partialText)
        assertEquals(0, systemEngines.single().destroyCalls)
        assertTrue(heard.isEmpty())
        assertTrue(errors.isEmpty())

        systemEngines.single().emitResults("fresh text")
        assertFalse(handle.listening)
        assertEquals(listOf("fresh text"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun synchronousOnErrorDuringStartListeningFallsBackCleanly() {
        val intent = dictationIntent("zh-CN")
        val systemEngines = mutableListOf<FakeOverlaySpeechEngine>()
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle(
            preferOnDevice = true,
            hasSystemRecognizer = { true },
            createOnDeviceEngine = {
                FakeOverlaySpeechEngine(
                    onStartListening = { engine, _ ->
                        engine.emitError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)
                    },
                )
            },
            createSystemEngine = { FakeOverlaySpeechEngine().also { systemEngines += it } },
            intent = intent,
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
            scheduleTimeout = FakeTimeoutScheduler()::schedule,
        )

        handle.start()
        assertTrue(handle.listening)
        assertEquals(1, systemEngines.size)
        assertTrue(errors.isEmpty())

        systemEngines.single().emitResults("sync fallback ok")
        assertFalse(handle.listening)
        assertEquals(listOf("sync fallback ok"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test
    fun forContextPlatformEngineStreamsPartialsWaitsForFinalOnFinishThenDestroys() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()

        val handle = OverlayDictationHandle.forContext(
            context = app,
            preferOnDevice = true,
            intent = dictationIntent("en-US"),
            onHeard = { { words -> heard += words } },
            onError = { { message -> errors += message } },
            stoppedMessage = stoppedMessage,
        )

        handle.start()
        assertTrue(handle.listening)
        val recognizer = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotNull(recognizer)
        val shadow = shadowOf(recognizer)

        shadow.triggerOnPartialResults(wordsBundle("platform live"))
        assertEquals("platform live", handle.partialText)

        handle.finish()
        assertFalse("finish() must not destroy recognizer before final onResults arrives", shadow.isDestroyed)
        assertTrue(handle.listening)

        shadow.triggerOnResults(wordsBundle("platform recognizer result"))

        assertFalse(handle.listening)
        assertEquals("", handle.partialText)
        assertTrue(shadow.isDestroyed)
        assertEquals(listOf("platform recognizer result"), heard)
        assertTrue(errors.isEmpty())
    }

    private class FakeTimeoutScheduler {
        val scheduledDelays = mutableListOf<Long>()
        var cancelledCount: Int = 0
            private set
        private var latestAction: (() -> Unit)? = null
        private var latestCancelled: Boolean = false

        fun schedule(delayMs: Long, action: () -> Unit): () -> Unit {
            scheduledDelays += delayMs
            latestAction = action
            latestCancelled = false
            val scheduled = action
            return {
                if (latestAction === scheduled && !latestCancelled) {
                    latestCancelled = true
                    cancelledCount++
                }
            }
        }

        fun fireLatest() {
            val action = latestAction
            if (action != null && !latestCancelled) {
                latestCancelled = true
                action()
            }
        }
    }

    private class FakeOverlaySpeechEngine(
        private val onStartListening: ((FakeOverlaySpeechEngine, Intent) -> Unit)? = null,
    ) : OverlaySpeechEngine {
        var listener: RecognitionListener? = null
            private set
        val startedIntents = mutableListOf<Intent>()
        var stopCalls: Int = 0
            private set
        var destroyCalls: Int = 0
            private set

        override fun setRecognitionListener(listener: RecognitionListener) {
            this.listener = listener
        }

        override fun startListening(intent: Intent) {
            startedIntents += intent
            onStartListening?.invoke(this, intent)
        }

        override fun stopListening() {
            stopCalls++
        }

        override fun destroy() {
            destroyCalls++
        }

        fun emitError(error: Int) {
            listener?.onError(error)
        }

        fun emitPartial(stable: String, unstable: String = "") {
            val bundle = Bundle().apply {
                putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf(stable))
                if (unstable.isNotEmpty()) {
                    putStringArrayList("android.speech.extra.UNSTABLE_TEXT", arrayListOf(unstable))
                }
            }
            listener?.onPartialResults(bundle)
        }

        fun emitResults(vararg words: String) {
            val bundle = Bundle().apply {
                putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, ArrayList(words.toList()))
            }
            listener?.onResults(bundle)
        }
    }
}
