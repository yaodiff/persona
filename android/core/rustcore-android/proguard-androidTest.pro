# The instrumentation runner loads these tests by their exact class names. The test APK's own R8 has no
# ordinary Kotlin caller for them, so retain only the entry points (not the whole generated codec graph).
-keep class persona.core.rustcore.ContinuityBindingFixtureTest { *; }
-keep class persona.core.rustcore.RustCoreDeviceBenchmark { *; }
-keep class persona.core.rustcore.RustCoreDeviceAttribution { *; }
-keep class persona.core.rustcore.RustCoreDeviceControl { *; }
-keep,allowoptimization class persona.core.rustcore.NativeDiagnostics {
    public static native void enableDiagnostics(long);
    public static native boolean flushDiagnostic(long, int);
    public static native long[] diagnostics(long);
}
