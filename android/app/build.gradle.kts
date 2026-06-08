import java.util.Properties

plugins {
    id("com.android.application")
}

data class GoAndroidTarget(
    val abi: String,
    val goArch: String,
    val goArm: String? = null,
    val clangTriple: String,
)

val mihomoRootDir = rootProject.projectDir.parentFile
val generatedJniLibsDir = layout.buildDirectory.dir("generated/jniLibs/mihomo")
val androidApi = 33
val goAndroidTargets =
    listOf(
        GoAndroidTarget("arm64-v8a", "arm64", clangTriple = "aarch64-linux-android${androidApi}-clang"),
        GoAndroidTarget("armeabi-v7a", "arm", "7", "armv7a-linux-androideabi${androidApi}-clang"),
        GoAndroidTarget("x86_64", "amd64", clangTriple = "x86_64-linux-android${androidApi}-clang"),
        GoAndroidTarget("x86", "386", clangTriple = "i686-linux-android${androidApi}-clang"),
    )

val keystorePropsFile = rootProject.file("keystore.properties")
val keystoreProps =
    if (keystorePropsFile.exists()) {
        Properties().apply { keystorePropsFile.inputStream().use { load(it) } }
    } else {
        null
    }
val localPropsFile = rootProject.file("local.properties")
val localProps =
    if (localPropsFile.exists()) {
        Properties().apply { localPropsFile.inputStream().use { load(it) } }
    } else {
        Properties()
    }
val androidSdkDirProvider =
    providers
        .environmentVariable("ANDROID_HOME")
        .orElse(providers.environmentVariable("ANDROID_SDK_ROOT"))
        .map { file(it) }
        .orElse(
            providers.provider {
                localProps.getProperty("sdk.dir")?.let { file(it) }
                    ?: file("${System.getProperty("user.home")}/Android/Sdk")
            },
        )
val androidNdkDirProvider =
    providers
        .environmentVariable("ANDROID_NDK_HOME")
        .orElse(providers.environmentVariable("ANDROID_NDK_ROOT"))
        .map { file(it) }
        .orElse(
            providers.provider {
                localProps.getProperty("ndk.dir")?.let { return@provider file(it) }
                val ndkRoot = androidSdkDirProvider.get().resolve("ndk")
                ndkRoot.listFiles()
                    ?.filter { it.isDirectory }
                    ?.maxByOrNull { it.name }
                    ?: ndkRoot.resolve("28.0.13004108")
            },
        )

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

    sourceSets {
        getByName("main") {
            jniLibs.srcDir(generatedJniLibsDir.get().asFile)
        }
    }
}

val buildMihomoPluginLibraries =
    tasks.register<Exec>("buildMihomoPluginLibraries") {
        inputs.files(
            fileTree(mihomoRootDir) {
                include("**/*.go")
                include("go.mod")
                include("go.sum")
                exclude("android/**")
                exclude("bin/**")
                exclude("**/build/**")
                exclude("**/.git/**")
            },
        )
        outputs.dir(generatedJniLibsDir)

        val outputRoot = generatedJniLibsDir.get().asFile
        val ndkDir = androidNdkDirProvider.get()
        val ndkBinDir = ndkDir.resolve("toolchains/llvm/prebuilt/linux-x86_64/bin")
        val gitVersion =
            providers.exec {
                workingDir = mihomoRootDir
                commandLine("git", "rev-parse", "--short", "HEAD")
            }.standardOutput.asText.map { it.trim().ifBlank { "dev" } }.getOrElse("dev")

        doFirst {
            outputRoot.mkdirs()
            require(ndkBinDir.exists()) { "Android NDK clang directory not found: ${ndkBinDir.absolutePath}" }
        }

        val buildScript =
            buildString {
                appendLine("set -euo pipefail")
                appendLine("cd '${mihomoRootDir.absolutePath}'")
                goAndroidTargets.forEach { target ->
                    val abiOutDir = outputRoot.resolve(target.abi)
                    val outFile = abiOutDir.resolve("libmihomo_plugin.so")
                    appendLine("mkdir -p '${abiOutDir.absolutePath}'")
                    append("GOOS=android GOARCH=${target.goArch} CGO_ENABLED=1 ")
                    if (target.goArm != null) {
                        append("GOARM=${target.goArm} ")
                    }
                    append("CC='${ndkBinDir.resolve(target.clangTriple).absolutePath}' ")
                    appendLine(
                        "go build -tags 'with_gvisor' -trimpath -buildmode=c-shared " +
                            "-ldflags '-X \"github.com/metacubex/mihomo/constant.Version=${gitVersion}\" -X \"github.com/metacubex/mihomo/constant.BuildTime=android-plugin\" -w -s -buildid=' " +
                            "-o '${outFile.absolutePath}' .",
                    )
                    appendLine("rm -f '${abiOutDir.resolve("libmihomo_plugin.h").absolutePath}'")
                }
            }
        commandLine("bash", "-lc", buildScript)
    }

tasks.named("preBuild") {
    dependsOn(buildMihomoPluginLibraries)
}
