import java.util.Properties
import com.android.build.api.variant.HasAndroidTest
import com.android.build.api.variant.HasAndroidTestBuilder

// Android packaging for the shared Rust core. :core:rustcore remains a pure JVM module so its real host-JNI

// fixture replay stays intact; this thin Android library carries only the native libraries and device verification.
// Nothing here constructs RustCore or loads its library: an adapter owns that lazily when a later phase uses it.
plugins {
    id("persona.android.library")
    id("org.jetbrains.kotlin.plugin.serialization")
}

val repositoryRoot = providers.gradleProperty("personaRepoRoot")
    .getOrElse(rootProject.layout.projectDirectory.dir("..").asFile.absolutePath)
val coreWorkspace = rootProject.layout.projectDirectory.dir("../native/app-core")
val coreInputs = files(
    coreWorkspace.file("Cargo.toml"),
    coreWorkspace.file("Cargo.lock"),
    coreWorkspace.file("rust-toolchain.toml"),
    fileTree(coreWorkspace.dir("crates")) { include("**/*.rs", "**/Cargo.toml", "**/*.h") },
    coreWorkspace.file("scripts/build-android.sh"),
)
val nativeOutput = objects.directoryProperty().convention(layout.buildDirectory.dir("native/jni"))
val nativeReports = objects.directoryProperty().convention(layout.buildDirectory.dir("reports/native"))
val nativeSymbols = objects.directoryProperty().convention(layout.buildDirectory.dir("native/symbols"))
val sdk = providers.environmentVariable("ANDROID_HOME")
    .orElse(providers.environmentVariable("ANDROID_SDK_ROOT"))
    .orElse(providers.gradleProperty("sdk.dir"))
    .orElse(providers.provider {
        val local = rootProject.file("local.properties")
        if (!local.isFile) null else Properties().apply { local.inputStream().use(::load) }.getProperty("sdk.dir")
    })
val resolvedSdk = requireNotNull(sdk.orNull) { "Set ANDROID_HOME or ANDROID_SDK_ROOT to package the Android core" }
val ndkVersion = "29.0.14206865"

abstract class BuildAndroidCore : Exec() {
    @get:OutputDirectory abstract val jniDirectory: DirectoryProperty
    @get:OutputDirectory abstract val symbolsDirectory: DirectoryProperty
    @get:OutputDirectory abstract val reportsDirectory: DirectoryProperty

    override fun exec() {
        val sdk = requireNotNull(System.getenv("ANDROID_HOME") ?: System.getenv("ANDROID_SDK_ROOT")) {
            "Set ANDROID_HOME or ANDROID_SDK_ROOT for the pinned Android NDK"
        }
        require(File("$sdk/ndk/29.0.14206865/source.properties").isFile) {
            "Install Android NDK 29.0.14206865 before building the shared core"
        }
        super.exec()
    }
}
val buildAndroidCore by tasks.registering(BuildAndroidCore::class) {
    description = "Builds stripped and symbol-retaining arm64-v8a and x86_64 Rust libraries with the pinned NDK."
    inputs.files(coreInputs).withPropertyName("rustCore").withPathSensitivity(PathSensitivity.RELATIVE)
    inputs.property("ndkVersion", ndkVersion)
    inputs.property("ndkHost", "linux-x86_64")
    val ndkRoot = "$resolvedSdk/ndk/$ndkVersion"
    val llvm = "$ndkRoot/toolchains/llvm/prebuilt/linux-x86_64"
    inputs.files(fileTree("$ndkRoot/sysroot") { include("**/*") })
        .withPropertyName("ndkSysroot").withPathSensitivity(PathSensitivity.RELATIVE)
    inputs.files(fileTree(llvm) {
        include("bin/aarch64-linux-android30-clang", "bin/x86_64-linux-android30-clang", "bin/llvm-ar",
            "bin/llvm-strip", "bin/llvm-readelf", "bin/lld", "bin/ld.lld", "bin/clang", "bin/clang++")
    }).withPropertyName("ndkTools").withPathSensitivity(PathSensitivity.RELATIVE)
    val rustSysroot = providers.exec { commandLine("rustc", "+1.98.0", "--print", "sysroot") }.standardOutput.asText.get().trim()
    inputs.files(fileTree("$rustSysroot/lib/rustlib/aarch64-linux-android/lib") { include("**/*") },
        fileTree("$rustSysroot/lib/rustlib/x86_64-linux-android/lib") { include("**/*") },
        "$rustSysroot/bin/rustc", "$rustSysroot/bin/cargo")
        .withPropertyName("rustToolchain").withPathSensitivity(PathSensitivity.RELATIVE)
    outputs.dirs(nativeOutput, nativeReports, nativeSymbols)
    jniDirectory.set(nativeOutput)
    symbolsDirectory.set(nativeSymbols)
    reportsDirectory.set(nativeReports)
    // Gradle discovers an explicit SDK from ANDROID_HOME/ANDROID_SDK_ROOT or the command-line sdk.dir.
    // No machine-specific local.properties or global Cargo configuration is read by build-android.sh.
    environment("ANDROID_HOME", resolvedSdk)
    environment("ANDROID_NDK_HOME", "$resolvedSdk/ndk/$ndkVersion")
    workingDir(coreWorkspace)
    commandLine("bash", coreWorkspace.file("scripts/build-android.sh").asFile.absolutePath,
        nativeOutput.get().asFile.absolutePath, nativeSymbols.get().asFile.absolutePath, nativeReports.get().asFile.absolutePath)
    outputs.cacheIf { true }
}

android {
    namespace = "persona.core.rustcore.android"
    defaultConfig {
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        consumerProguardFiles("consumer-rules.pro")
        ndk { abiFilters += setOf("arm64-v8a", "x86_64") }
    }
    buildTypes {
        named("release") {
            isMinifyEnabled = true
            androidTest.enableMinification = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "consumer-rules.pro")
        }
        named("debug") {
            // The debug test APK remains available for ordinary replay; optimized replay uses the enabled release test.
            androidTest.enableMinification = false
        }
    }
    packaging {
        jniLibs {
            // A compressed .so has to be extracted at install time; the packaged library is page-aligned and mapped.
            useLegacyPackaging = false
        }
    }
}

// Copy the existing host test's exact assertions rather than maintaining a weaker device-only replay. The only change
// is how fixtures are read: the device cannot walk the repository, so the same JSON files are test APK assets.
val continuityFixtureTest = rootProject.file("core/rustcore/src/test/kotlin/persona/core/rustcore/ContinuityBindingFixtureTest.kt")
abstract class PrepareDeviceReplay : DefaultTask() {
    @get:InputFile @get:PathSensitive(PathSensitivity.RELATIVE)
    abstract val hostReplay: RegularFileProperty
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty

    @TaskAction fun copy() {
        val source = hostReplay.get().asFile.readText().replace(
            "import persona.core.domain.FixtureReader", "import persona.core.rustcore.DeviceFixtureReader as FixtureReader",
        )
        outputDirectory.get().asFile.deleteRecursively()
        val output = outputDirectory.get().file("persona/core/rustcore/ContinuityBindingFixtureTest.kt").asFile
        output.parentFile.mkdirs()
        output.writeText(source)
    }
}
val prepareDeviceReplay by tasks.registering(PrepareDeviceReplay::class) {
    hostReplay.set(continuityFixtureTest)
    outputDirectory.set(layout.buildDirectory.dir("generated/deviceReplay"))
}
abstract class PrepareContinuityFixtures : DefaultTask() {
    @get:InputFiles @get:PathSensitive(PathSensitivity.RELATIVE)
    abstract val fixtureFiles: ConfigurableFileCollection
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty

    @TaskAction fun copy() {
        val root = outputDirectory.get().asFile
        fixtureFiles.files.forEach { source ->
            val target = File(root, "continuity/" + source.name)
            target.parentFile.mkdirs()
            source.copyTo(target, overwrite = true)
        }
    }
}
val prepareContinuityFixtures by tasks.registering(PrepareContinuityFixtures::class) {
    fixtureFiles.from(fileTree("$repositoryRoot/spec/fixtures/continuity") { include("*.json") })
    outputDirectory.set(layout.buildDirectory.dir("generated/continuityAssets"))
}
abstract class PrepareCoreLicenses : Exec() {
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty
}
val prepareCoreLicenses by tasks.registering(PrepareCoreLicenses::class) {
    description = "Collects every locked public Cargo dependency's license notices into the Android assets."
    inputs.files(coreWorkspace.file("Cargo.lock"), coreWorkspace.file("scripts/android-licenses.py"),
        rootProject.file("LICENSE"), fileTree(coreWorkspace.dir("licenses")) { include("**/*") })
    val notices = layout.buildDirectory.dir("generated/notices")
    outputDirectory.set(notices)
    outputs.dir(notices)
    val registry = providers.environmentVariable("CARGO_HOME").map { "$it/registry/src" }
        .orElse(providers.systemProperty("user.home").map { "$it/.cargo/registry/src" })
    workingDir(coreWorkspace)
    commandLine("python3", "-I", coreWorkspace.file("scripts/android-licenses.py"),
        "--workspace", coreWorkspace, "--registry-src", registry.get(), "--output", outputDirectory.get().asFile.absolutePath)
    outputs.cacheIf { true }
}
androidComponents {
    beforeVariants { variant ->
        if (variant is HasAndroidTestBuilder && variant.name == "release") {
            variant.enableAndroidTest = true
        }
    }
    onVariants { variant ->
        variant.sources.jniLibs?.addGeneratedSourceDirectory(buildAndroidCore) { it.jniDirectory }
        variant.sources.assets?.addGeneratedSourceDirectory(prepareCoreLicenses) { it.outputDirectory }
        if (variant is HasAndroidTest) {
            variant.androidTest?.proguardFiles?.add(layout.projectDirectory.file("proguard-androidTest.pro"))
            variant.androidTest?.sources?.java?.addGeneratedSourceDirectory(prepareDeviceReplay) { it.outputDirectory }
            variant.androidTest?.sources?.assets?.addGeneratedSourceDirectory(prepareContinuityFixtures) { it.outputDirectory }
        }
    }
}

dependencies {
    api(project(":core:rustcore"))
    // Instrumentation verifies the same private JNI boundary as RustCore. Compile the tests as a friend of
    // :core:rustcore rather than making NativeBridge public or adding a production diagnostic API.
    val rustCoreJar = layout.buildDirectory.file("../../rustcore/build/libs/rustcore.jar")
    tasks.matching { it.name == "compileReleaseAndroidTestKotlin" }.configureEach {
        val compile = this as org.jetbrains.kotlin.gradle.tasks.KotlinJvmCompile
        compile.dependsOn(":core:rustcore:jar")
        compile.inputs.file(rustCoreJar).withPropertyName("rustCoreFriendJar")
        compile.compilerOptions.freeCompilerArgs.add(
            rustCoreJar.map { "-Xfriend-paths=${it.asFile.absolutePath}" },
        )
    }
    androidTestImplementation(testFixtures(project(":core:domain")))
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation("com.google.errorprone:error_prone_annotations:2.44.0")
}
