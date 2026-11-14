plugins {
    id("persona.android.application")
    id("persona.compose")
    id("persona.hilt")
    id("org.jetbrains.kotlin.plugin.serialization")
}

// The application id is personal (docs/operations/config.md): scripts/android.sh passes it from ~/.config/persona/config.json.
val appId = providers.gradleProperty("personaApplicationId")
    .getOrElse("com.example.persona")
// A different label lets two builds sit side by side on one phone (with different application ids).
val appLabel = providers.gradleProperty("personaAppLabel")
    .getOrElse("Persona")
// Gmail's Google sign-in: the redirect scheme of the hub's Google OAuth client (docs/extensions/mail.md "Android").
val googleOAuthScheme = providers.gradleProperty("personaGoogleOAuthScheme")
    .orNull?.takeIf { it.isNotBlank() }
    ?: System.getenv("PERSONA_GOOGLE_OAUTH_SCHEME")?.takeIf { it.isNotBlank() }
    ?: "persona.oauth.unset"

// The same language resources drive the contracts, packaged translations, and Android 13's per-app picker.
val languageFiles = rootProject.fileTree("i18n") { include("*.properties") }
val languageTags = languageFiles.files.map { it.nameWithoutExtension }.sorted()
abstract class GenerateLocaleConfig : DefaultTask() {
    @get:InputFiles @get:PathSensitive(PathSensitivity.RELATIVE)
    abstract val languages: ConfigurableFileCollection
    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty

    @TaskAction fun generate() {
        val tags = languages.files.map { it.nameWithoutExtension }.sorted()
        require(tags.all { it.matches(Regex("[a-z]{2,3}(-[A-Za-z0-9]{2,8})*")) }) { "Invalid language tag" }
        val output = outputDirectory.get().file("xml/locales_config.xml").asFile
        output.parentFile.mkdirs()
        output.writeText("<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
            "<locale-config xmlns:android=\"http://schemas.android.com/apk/res/android\">\n" +
            tags.joinToString("\n") { "    <locale android:name=\"$it\" />" } + "\n</locale-config>\n")
    }
}
val generateLocaleConfig by tasks.registering(GenerateLocaleConfig::class) {
    languages.from(languageFiles)
    outputDirectory.set(layout.buildDirectory.dir("generated/languages/res"))
}
androidComponents.onVariants { variant ->
    variant.sources.res?.addGeneratedSourceDirectory(generateLocaleConfig, GenerateLocaleConfig::outputDirectory)
}

android {
    namespace = "persona.android"
    testOptions { unitTests { isIncludeAndroidResources = true } }
    lint {
        baseline = file("lint-baseline.xml")
    }
    // Only the app's languages (docs/protocol/i18n.md): a library's French strings would mix into an English screen.
    androidResources {
        localeFilters += languageTags.map { it.substringBefore('-') }
    }
    defaultConfig {
        applicationId = appId
        versionCode = providers.gradleProperty("personaVersionCode").orElse("1").get().toInt().also {
            require(it in 1..2100000000) { "Invalid Android version code" }
        }
        versionName = providers.gradleProperty("personaVersionName").orElse("0.1").get()
        manifestPlaceholders["appLabel"] = appLabel
        manifestPlaceholders["googleOAuthScheme"] = googleOAuthScheme
    }
    buildTypes {
        release {
            // R8 shrinks and optimizes without obfuscating (proguard-rules.pro), so error reports keep class names
            // (docs/android/android.md "Release shrinking"; scripts/release/check_android_shrink.py checks the APK).
            // A rollback to false also changes scripts/release/test_check_android_shrink.py, which pins this line.
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }
}

dependencies {
    implementation(project(":core:contracts"))
    implementation(project(":core:domain"))
    implementation(project(":core:terminal"))
    // Phase 1 packages the binding only; production continuity remains native and no core is created on launch.
    implementation(project(":core:rustcore-android"))
    implementation(project(":core:navigation"))
    implementation(project(":core:hub"))
    implementation(project(":core:data"))
    implementation(project(":core:chatlist"))
    implementation(project(":core:designsystem"))
    implementation(libs.androidx.material3.adaptive)
    implementation(libs.androidx.material3.adaptive.layout)
    implementation(libs.androidx.material3.adaptive.navigation3)
    implementation(project(":feature:chat"))
    implementation(project(":feature:agents"))
    implementation(project(":feature:assistant"))
    implementation(project(":feature:inbox"))
    implementation(project(":feature:settings"))
    implementation(project(":feature:terminal"))
    implementation(project(":feature:machines"))
    implementation(project(":feature:files"))
    implementation(project(":feature:knowledge"))
    implementation(project(":feature:voice"))
    implementation(project(":platform:phonetools"))
    implementation(project(":core:demo"))
    implementation(libs.room.runtime)
    implementation(project(":platform:notify"))
    implementation(project(":platform:shortcuts"))
    implementation(project(":platform:capsule"))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.compose.preview)
    implementation(libs.coil.compose)
    debugImplementation(libs.compose.tooling)

    implementation(libs.androidx.work)
    implementation(libs.androidx.core)
    implementation(libs.androidx.activity)
    implementation(libs.androidx.lifecycle.compose)
    implementation(libs.androidx.lifecycle.process)
    implementation(libs.androidx.navigation3.ui)
    implementation(libs.androidx.lifecycle.navigation3)
    implementation(libs.coroutines.android)
    implementation(libs.serialization.json)
    implementation(libs.okhttp)
    testImplementation(libs.junit)
    testImplementation(libs.coroutines.test)
    testImplementation(libs.turbine)
    testImplementation("com.squareup.okhttp3:mockwebserver:${libs.versions.okhttp.get()}")
    testImplementation(platform(libs.compose.bom))
    testImplementation("androidx.compose.ui:ui-test-junit4")
    testImplementation("androidx.compose.ui:ui-test-manifest")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test.ext:junit:1.3.0")
}
