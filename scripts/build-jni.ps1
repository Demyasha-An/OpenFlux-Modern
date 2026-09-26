$ErrorActionPreference = 'Stop'
$ndk = 'D:\Android\sdk\ndk\27.2.12479018'
$app = 'D:\OpenFlux-Modern\android\app\src\main'
Set-Location $app
$env:NDK_PROJECT_PATH = $app
& "$ndk\ndk-build.cmd" NDK_PROJECT_PATH=$app APP_BUILD_SCRIPT=$app\jni\Android.mk NDK_APPLICATION_MK=$app\jni\Application.mk NDK_OUT=$app\obj NDK_LIBS_OUT=$app\libs
if ($LASTEXITCODE -ne 0) { throw "ndk-build failed" }
foreach ($p in @('armeabi-v7a','arm64-v8a','x86','x86_64')) {
    $dir = "$app\libs\$p"
    if (-not (Test-Path $dir)) { continue }
    New-Item -ItemType Directory -Force -Path "$app\jniLibs\$p" | Out-Null
    Copy-Item "$dir\tun2socks" "$app\jniLibs\$p\libtun2socks.so" -Force
    Copy-Item "$dir\pdnsd" "$app\jniLibs\$p\libpdnsd.so" -Force
    Copy-Item "$dir\libsystem.so" "$app\jniLibs\$p\libsystem.so" -Force
    "installed $p"
}
Remove-Item -Recurse -Force "$app\libs","$app\obj" -ErrorAction SilentlyContinue
'JNI-BUILT'
