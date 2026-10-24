plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
}

android {
    namespace = "ir.chador.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "ir.chador.app"
        minSdk = 24
        targetSdk = 35
        versionCode = 1
        versionName = "1.0.0"
        // Address of the Chador server. Override without editing files:  ./gradlew assembleRelease -PapiBaseUrl=https://other.example.ir/
        val api = (project.findProperty("apiBaseUrl") as String?) ?: "https://shch.badr-group.ir/"
        buildConfigField("String", "API_BASE_URL", "\"${api.trimEnd('/')}/\"")
    }

    buildTypes {
        debug {
            // Defaults to the production server; for a local backend from the emulator use -PapiBaseUrlDebug=http://10.0.2.2:8080/
            val dbg = (project.findProperty("apiBaseUrlDebug") as String?) ?: "https://shch.badr-group.ir/"
            buildConfigField("String", "API_BASE_URL", "\"${dbg.trimEnd('/')}/\"")
            applicationIdSuffix = ".debug"
            versionNameSuffix = "-debug"
        }
        release {
            // ML Kit's barcode library ships ~5 MB of native code per CPU; x86 is emulator-only.
            ndk { abiFilters += listOf("arm64-v8a", "armeabi-v7a") }
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
        isCoreLibraryDesugaringEnabled = true
    }
    kotlinOptions { jvmTarget = "17" }

    packaging { resources.excludes += "/META-INF/{AL2.0,LGPL2.1}" }
    lint { abortOnError = false }
}

dependencies {
    implementation(project(":core"))
    coreLibraryDesugaring(libs.desugar.jdk.libs)

    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.viewmodel.compose)
    implementation(libs.androidx.lifecycle.runtime.compose)
    implementation(libs.androidx.navigation.compose)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    implementation(libs.androidx.compose.material.icons)
    debugImplementation(libs.androidx.compose.ui.tooling)

    implementation(libs.androidx.camera.core)
    implementation(libs.androidx.camera.camera2)
    implementation(libs.androidx.camera.lifecycle)
    implementation(libs.androidx.camera.view)
    implementation(libs.mlkit.barcode)

    implementation(libs.androidx.work.runtime)
}
