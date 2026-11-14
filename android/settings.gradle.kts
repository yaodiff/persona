pluginManagement {
    includeBuild("build-logic")
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("androidx\\..*")
                includeGroup("com.google.testing.platform")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("androidx\\..*")
                includeGroup("com.google.testing.platform")
            }
        }
        mavenCentral()
    }
}
rootProject.name = "Persona"
include(":core:demo")
include(":feature:machines")
include(":feature:files")
include(":feature:knowledge")
include(":app", ":core:contracts", ":core:domain", ":core:terminal", ":core:rustcore", ":core:rustcore-android")

include(
    ":core:navigation", ":core:hub", ":core:data", ":core:designsystem", ":core:chatlist",
    ":feature:chat", ":feature:agents", ":feature:assistant", ":feature:inbox", ":feature:settings",
    ":platform:notify",
)
include(":feature:terminal")
include(":platform:phonetools")
include(":platform:shortcuts")
include(":feature:voice")
include(":platform:capsule")
