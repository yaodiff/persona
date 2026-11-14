package persona.core.rustcore

/** Test-only JNI entry points. The production RustCore and NativeBridge API stays unchanged. */
internal object NativeDiagnostics {
    @JvmStatic external fun enableDiagnostics(handle: Long)

    @JvmStatic external fun flushDiagnostic(handle: Long, timeoutMillis: Int): Boolean

    @JvmStatic external fun diagnostics(handle: Long): LongArray?
}
