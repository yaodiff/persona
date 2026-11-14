# Rust resolves these Java/JVM methods by their stable names through JNI, not through a Kotlin call site.
# Keep the callback class and its signatures even when R8 sees no direct callers; do not broaden this to all codecs.
-keep,allowoptimization class persona.core.rustcore.HostBridge {
    public void changed(long);
    public void run(byte[]);
}
-keep,allowoptimization class persona.core.rustcore.NativeBridge {
    public static native long create(persona.core.rustcore.HostBridge, byte[]);
    public static native boolean dispatch(long, byte[]);
    public static native byte[] drain(long, java.lang.String);
    public static native boolean flush(long, int);
    public static native void destroy(long);
}
