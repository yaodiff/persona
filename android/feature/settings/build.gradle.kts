plugins {
    id("persona.android.library")
    // Its tests expect CoreText in Simplified Chinese (CoreTextFallback).
    id("persona.coretext.zh")
    id("persona.compose")
    id("persona.hilt")
}

android {
    namespace = "persona.feature.settings"
    // Plugins' views are rendered on the JVM (Robolectric's native graphics) for their pixel checks and screenshots.
    // The screenshots share one test process; retain the bounded heap it needs rather than the 512 MB default.
    testOptions {
        unitTests {
            isIncludeAndroidResources = true
            all {
                it.maxHeapSize = "1g"
                // Robolectric's API 36 shared-memory setup accesses the JDK file-descriptor bridge.
                it.jvmArgs(
                    "--add-opens=java.base/java.io=ALL-UNNAMED",
                    "--add-exports=java.base/jdk.internal.access=ALL-UNNAMED"
                )
            }
        }
    }
    defaultConfig {
        manifestPlaceholders["googleOAuthScheme"] = "persona.oauth.unset"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
    }
    // #13282 shares the exact reviewed generated source and immutable adapter with instrumentation only.
    // The ordinary main/debug/release source sets never compile either file.
    sourceSets.named("androidTest") {
        java.srcDir("src/test/kotlin")
        java.filter.include(
            "persona/feature/settings/AboutBuildSnapshot.kt",
            "persona/feature/settings/generated/AboutBuildRow.kt",
        )
    }
}

dependencies {
    implementation(libs.androidx.activity)
    implementation(libs.androidx.lifecycle.compose)
    implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.11.0")
    implementation(libs.androidx.core)
    implementation(libs.zxing)
    implementation(libs.okhttp)
    // An extension's view (UI version 3): markdown, and a row's avatar or thumbnail loaded from https.
    implementation(libs.markdown)
    implementation(libs.coil.compose)
    implementation(libs.coil.network.okhttp)
    // An assistant card's html page: its own WebView and the bridge's message listener (AssistantHtml.kt).
    implementation(libs.androidx.webkit)
    implementation(libs.compose.preview)
    testImplementation(libs.junit)
    // An inbox item's page in pictures: the inbox's header over a plugin's body (InboxItemShotsTest).
    testImplementation(project(":feature:inbox"))
    testImplementation(libs.coroutines.test)
    testImplementation(platform(libs.compose.bom))
    testImplementation("androidx.compose.ui:ui-test-junit4")
    testImplementation("androidx.compose.ui:ui-test-manifest")
    testImplementation("org.robolectric:robolectric:4.17")
    testImplementation("androidx.test.ext:junit:1.3.0")
    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
    implementation(project(":core:contracts"))
    implementation(project(":core:data"))
    implementation(project(":core:hub"))
    implementation(project(":core:domain"))
    implementation(project(":core:designsystem"))
    implementation(project(":core:navigation"))
    implementation(project(":core:moduleapi"))
    // The link card for the welcome page's public guide (PairingScreen.kt); feature:chat does not depend on this module.
    implementation(project(":feature:chat"))
}
