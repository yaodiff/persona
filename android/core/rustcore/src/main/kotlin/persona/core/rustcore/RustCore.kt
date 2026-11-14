package persona.core.rustcore

import java.util.concurrent.atomic.AtomicLong
import persona.core.rustcore.wire.Action
import persona.core.rustcore.wire.ChatPatch
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.Effect

/**
 * The client's side of the shared core. Both calls arrive on the core's worker thread: keep them short and hand work to
 * the client's own threads.
 */
interface CoreHost {
    /** Something changed at [version]: pull patches with [RustCore.drain] on the next frame. */
    fun changed(version: Long)

    /** Targeted wake for adapters; existing version-only hosts keep the old callback. */
    fun changedChat(chat: String, version: Long) = changed(version)

    /** A request or timer for the client to run. */
    fun run(effect: Effect)
}

/**
 * One running core (docs/plans/plan-rust-core.md). [dispatch] only queues the action's bytes, so it is safe on the main
 * thread; [drain] takes the patch the worker already built.
 */
class RustCore(config: Config, host: CoreHost) : AutoCloseable {
    private val handle = AtomicLong(
        NativeBridge.create(HostBridge(host), config.bincodeSerialize()).also {
            check(it != 0L) { "The core did not start" }
        },
    )

    fun dispatch(action: Action): Boolean = NativeBridge.dispatch(live(), action.bincodeSerialize())

    /** The chat's changes since the last drain. */
    fun drain(chat: String): ChatPatch = ChatPatch.bincodeDeserialize(
        checkNotNull(NativeBridge.drain(live(), chat)) { "The core returned no patch" },
    )

    /** Waits until queued actions are applied. For tests; never on the main thread. */
    fun flush(timeoutMillis: Int): Boolean = NativeBridge.flush(live(), timeoutMillis)

    override fun close() {
        val current = handle.getAndSet(0L)
        if (current != 0L) NativeBridge.destroy(current)
    }

    private fun live(): Long = handle.get().also { check(it != 0L) { "The core is closed" } }
}

/** What the JNI side calls, by these exact names and signatures (native/app-core/crates/persona-core-ffi/src/jni.rs). */
internal class HostBridge(private val host: CoreHost) {
    fun changed(version: Long) = host.changed(version)

    fun changedChat(chat: String, version: Long) = host.changedChat(chat, version)

    fun run(effect: ByteArray) = host.run(Effect.bincodeDeserialize(effect))
}

/** The JNI entry points of libpersona_core. */
internal object NativeBridge {
    init {
        // Unit tests load a host build; the app loads the library packaged for its ABI.
        System.getProperty("persona.core.library")?.let(System::load) ?: System.loadLibrary("persona_core")
    }

    @JvmStatic external fun create(host: HostBridge, config: ByteArray): Long

    @JvmStatic external fun dispatch(handle: Long, action: ByteArray): Boolean

    @JvmStatic external fun drain(handle: Long, chat: String): ByteArray?

    @JvmStatic external fun flush(handle: Long, timeoutMillis: Int): Boolean

    @JvmStatic external fun destroy(handle: Long)

    /** Projects the Agents list from a JSON input string. Pure function, no runtime handle needed. */
    @JvmStatic external fun projectSort(input: String): String
}
