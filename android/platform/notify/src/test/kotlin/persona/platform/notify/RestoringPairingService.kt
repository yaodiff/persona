package persona.platform.notify

import java.lang.reflect.Proxy
import kotlin.coroutines.Continuation
import kotlin.coroutines.intrinsics.COROUTINE_SUSPENDED
import kotlin.coroutines.resume
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import org.robolectric.util.ReflectionHelpers
import persona.core.contracts.api.PairingRequest
import persona.core.contracts.api.PairingService

/**
 * A pairing whose restored credentials become visible only when [completeRestore] is called.
 *
 * The proxy deliberately implements the suspend restore contract even before PairingService declares it. That keeps these
 * regression tests compilable on the broken code: current readers never invoke restore and fail their behavioral assertions,
 * while a fixed reader's restore(Continuation) is suspended here without blocking a thread.
 */
internal class RestoringPairingService {
    val paired = MutableStateFlow(false)
    val restoreStarted = CompletableDeferred<Unit>()
    private val restored = CompletableDeferred<Unit>()
    var resumeRestore: CompletableDeferred<Unit>? = null
    var restoreCalls = 0
        private set
    var failure: RuntimeException? = null

    val service: PairingService = Proxy.newProxyInstance(
        PairingService::class.java.classLoader,
        arrayOf(PairingService::class.java),
    ) { proxy, method, arguments ->
        when (method.name) {
            "isPaired" -> paired
            "restore" -> {
                restoreCalls++
                failure?.let { throw it }
                restoreStarted.complete(Unit)
                val nested = resumeRestore
                val wait = if (nested != null && Throwable().stackTrace.any {
                    it.className == NotificationRuntime::class.java.name && it.methodName == "resume"
                }) nested else restored
                if (wait.isCompleted) Unit
                else {
                    @Suppress("UNCHECKED_CAST")
                    val continuation = arguments!!.last() as Continuation<Unit>
                    wait.invokeOnCompletion { continuation.resume(Unit) }
                    COROUTINE_SUSPENDED
                }
            }
            "pair" -> throw UnsupportedOperationException("Pairing is outside this test: ${arguments?.firstOrNull() as? PairingRequest}")
            "unpair" -> throw UnsupportedOperationException("Unpairing is outside this test")
            "toString" -> "RestoringPairingService(calls=$restoreCalls)"
            "hashCode" -> System.identityHashCode(proxy)
            "equals" -> proxy === arguments?.firstOrNull()
            else -> throw UnsupportedOperationException("Unexpected PairingService call: ${method.name}")
        }
    } as PairingService

    fun completeRestore() {
        paired.value = true
        restored.complete(Unit)
    }

    suspend fun awaitRestore() = restoreStarted.awaitReal()
}

internal fun resetForegroundStartsForTest() {
    val starts = HubService::class.java.getDeclaredField("starts").apply { isAccessible = true }.get(null)
    ReflectionHelpers.setField(starts, "pending", 0)
    ReflectionHelpers.setField(starts, "stopAsked", false)
}

/** Receivers use Dispatchers.IO; await their signals with a real deadline, not virtual-time polling or Thread.sleep. */
internal suspend fun CompletableDeferred<Unit>.awaitReal() {
    withContext(Dispatchers.IO) {
        withTimeout(5_000) { await() }
    }
}

internal suspend fun awaitRealCondition(message: String, condition: () -> Boolean) {
    withContext(Dispatchers.IO) {
        withTimeout(5_000) {
            while (!condition()) kotlinx.coroutines.yield()
        }
    }
}
