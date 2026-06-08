plugins {
    id("com.android.application")
}

val keystorePropsFile = rootProject.file("keystore.properties")
val keystoreProps =
    if (keystorePropsFile.exists()) {
        java.util.Properties().apply { load(keystorePropsFile.inputStream()) }
    } else {
        null
    }

android {
    namespace = "info.loveyu.m2m"
    compileSdk = 36

    defaultConfig {
        applicationId = "info.loveyu.m2m"
        minSdk = 33
        targetSdk = 36
        versionCode = (project.findProperty("versionCode") as String?)?.toIntOrNull() ?: 1
        versionName = project.findProperty("versionName") as String? ?: "dev"
    }

    if (keystoreProps != null) {
        signingConfigs {
            create("release") {
                storeFile = file(keystoreProps["STORE_FILE"] as String)
                storePassword = keystoreProps["STORE_PASSWORD"] as String
                keyAlias = keystoreProps["KEY_ALIAS"] as String
                keyPassword = keystoreProps["KEY_PASSWORD"] as String
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (keystoreProps != null) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
}
