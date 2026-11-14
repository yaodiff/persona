// The shared Rust core's Android binding (docs/plans/plan-rust-core.md): the JNI bridge, a typed wrapper and the codecs
// persona-core-typegen generates from the core's wire types. Unit tests load a host build of the same library.
plugins {
    id("persona.jvm.library")
}

dependencies {
    testImplementation(testFixtures(project(":core:domain")))
}

val coreWorkspace = rootProject.layout.projectDirectory.dir("../native/app-core")
val hostLibrary = coreWorkspace.file(
    "target/debug/" + when {
        org.gradle.internal.os.OperatingSystem.current().isMacOsX -> "libpersona_core.dylib"
        org.gradle.internal.os.OperatingSystem.current().isWindows -> "persona_core.dll"
        else -> "libpersona_core.so"
    }
)

// Cargo is incremental, so this runs every time and costs little when nothing changed. Needs rustup; the workspace's
// rust-toolchain.toml picks the compiler.
val buildHostCore by tasks.registering(Exec::class) {
    workingDir(coreWorkspace)
    commandLine("cargo", "build", "--locked", "-p", "persona-core-ffi", "--features", "test-support")
    outputs.file(hostLibrary)
    outputs.upToDateWhen { false }
}

val releaseHostLibrary = coreWorkspace.file("target/boundary-measurement-target/release/libpersona_core.so")
val buildReleaseHostCore by tasks.registering(Exec::class) {
    workingDir(coreWorkspace)
    environment("CARGO_TARGET_DIR", coreWorkspace.file("target/boundary-measurement-target").asFile.absolutePath)
    commandLine("cargo", "build", "--locked", "--release", "-p", "persona-core-ffi", "--features", "test-support")
    outputs.file(releaseHostLibrary)
    outputs.upToDateWhen { false }
}
// Linux-only host diagnostic; keep the measurement release target separate from normal release builds.
// Ordinary tests exclude the opt-in measurement classes; this task always produces their raw reports.
val boundaryCostTest by tasks.registering(Test::class) {
    group = "verification"
    description = "Host-only segmented Runtime/JNI/codec reference; not a device acceptance benchmark."
    testClassesDirs = sourceSets.test.get().output.classesDirs
    classpath = sourceSets.test.get().runtimeClasspath
    dependsOn(buildReleaseHostCore)
    inputs.files(buildReleaseHostCore).withPropertyName("releaseHostCore").withPathSensitivity(PathSensitivity.NONE)
    systemProperty("persona.core.library", releaseHostLibrary.asFile.absolutePath)
    systemProperty("persona.core.boundary.library", releaseHostLibrary.asFile.absolutePath)
    environment("PERSONA_MEASUREMENT_OUTPUT", layout.buildDirectory.dir("reports/boundary-cost").get().asFile.absolutePath)
    environment("PERSONA_CODEC_COMPARISON_OUTPUT", layout.buildDirectory.dir("reports/codec-comparison").get().asFile.absolutePath)
    environment("PERSONA_MEASUREMENT_NONCE", providers.gradleProperty("personaBoundaryNonce").orElse("boundary-cost-default").get())
    outputs.upToDateWhen { false }
    doFirst {
        println("boundaryCostTest native library: ${releaseHostLibrary.asFile.absolutePath}")
    }
    filter { includeTestsMatching("persona.core.rustcore.RustCoreBoundaryCostTest") }
    filter { includeTestsMatching("persona.core.rustcore.KotlinCodecComparisonTest") }
}

tasks.withType<Test>().configureEach {
    // The library's contents, not just its path, decide whether the tests are up to date.
    inputs.files(buildHostCore).withPropertyName("hostCore").withPathSensitivity(PathSensitivity.NONE)
    systemProperty("persona.core.library", hostLibrary.asFile.absolutePath)
    if (name == "test") {
        filter {
            excludeTestsMatching("persona.core.rustcore.RustCoreBoundaryCostTest")
            excludeTestsMatching("persona.core.rustcore.KotlinCodecComparisonTest")
        }
    }
    if (name == "boundaryCostTest") {
        systemProperty("persona.core.library", releaseHostLibrary.asFile.absolutePath)
    }
}
