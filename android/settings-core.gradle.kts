// JVM-only build of the :core module (no Android SDK needed).
//   ./gradlew -c settings-core.gradle.kts :core:test
// Android Studio ignores this file; it lets the logic be tested in CI or on any machine with just a JDK.
//
// MAVEN_MIRROR can point at a Maven Central mirror (some networks block Gradle's user agent on the default host).
val mirror = providers.environmentVariable("MAVEN_MIRROR").orNull

pluginManagement {
    val m = providers.environmentVariable("MAVEN_MIRROR").orNull
    repositories {
        if (m != null) maven(m)
        mavenCentral()
        gradlePluginPortal()
    }
}
dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        if (mirror != null) maven(mirror)
        mavenCentral()
    }
}
rootProject.name = "chador-android-core"
include(":core")
