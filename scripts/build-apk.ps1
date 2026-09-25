# Builds the OpenFlux APK on Windows: compiles the Go client for every Android
# ABI with the NDK, drops it into jniLibs, then runs the Gradle build.
#   powershell -ExecutionPolicy Bypass -File scripts/build-apk.ps1
#   powershell ... -Task assembleRelease
#   powershell ... -SkipNative
param(
    [string]$Task = "assembleDebug",
    [switch]$SkipNative
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
$app = "$repo\android"
$SDK = 'D:\Android\sdk'
$NDK = "$SDK\ndk\27.2.12479018"
$JDK = 'C:\Program Files\Microsoft\jdk-17.0.20.101-hotspot'
$env:JAVA_HOME = $JDK
$env:ANDROID_HOME = $SDK
$env:ANDROID_SDK_ROOT = $SDK

if (-not $SkipNative) {
    $clang = "$NDK\toolchains\llvm\prebuilt\windows-x86_64\bin\clang.exe"
    if (-not (Test-Path $clang)) { throw "NDK clang not found at $clang (run scripts/setup-android-sdk.ps1)" }
    $outRoot = "$app\app\src\main\jniLibs"
    $api = 26
    $targets = @(
        @{ abi = 'arm64-v8a';   goarch = 'arm64'; target = "aarch64-linux-android" },
        @{ abi = 'armeabi-v7a'; goarch = 'arm';   target = "armv7a-linux-androideabi" },
        @{ abi = 'x86_64';     goarch = 'amd64'; target = "x86_64-linux-android" }
    )
    foreach ($t in $targets) {
        $out = "$outRoot\$($t.abi)\libopenflux.so"
        New-Item -ItemType Directory -Force -Path (Split-Path $out) | Out-Null
        Write-Host "==> go build $($t.abi)"
        $env:GOOS = 'android'; $env:GOARCH = $t.goarch; $env:CGO_ENABLED = '1'
        $env:CC = "$clang --target=$($t.target)$api"
        if ($t.goarch -eq 'arm') { $env:GOARM = '7' } else { Remove-Item Env:\GOARM -ErrorAction SilentlyContinue }
        Push-Location $repo
        try {
            & go build -trimpath -ldflags "-s -w -checklinkname=0 -extldflags=-Wl,-z,max-page-size=16384" -o $out .
            if ($LASTEXITCODE -ne 0) { throw "go build failed for $($t.abi)" }
        } finally { Pop-Location }
    }
    $commit = (& git -C $repo rev-parse --short HEAD).Trim()
    "OpenFlux $commit`n$(& go version)" | Set-Content "$app\app\src\main\openflux-version.txt"
}

Write-Host "==> gradle $Task"
Push-Location $app
try {
    & "$app\gradlew.bat" $Task --no-daemon
    if ($LASTEXITCODE -ne 0) { throw "gradle failed" }
} finally { Pop-Location }
Write-Host "APK-READY"
