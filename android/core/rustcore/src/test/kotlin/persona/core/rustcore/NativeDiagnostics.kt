package persona.core.rustcore

/** Host-measurement JNI entry points; production RustCore and NativeBridge stay unchanged. */
internal object NativeDiagnostics {
    @JvmStatic external fun lease(handle: Long): Boolean
    @JvmStatic external fun enableDiagnostics(handle: Long)
    @JvmStatic external fun diagnostics(handle: Long): LongArray?
}
