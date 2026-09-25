# Installs the minimal Android SDK needed to build the APK headlessly:
# platform-tools, platform 34, build-tools 34, NDK r27 (for the Go CGO build).
$ErrorActionPreference = 'Stop'
$SDK = 'D:\Android\sdk'
$JDK = 'C:\Program Files\Microsoft\jdk-17.0.20.101-hotspot'
$env:JAVA_HOME = $JDK
$env:ANDROID_HOME = $SDK
$env:ANDROID_SDK_ROOT = $SDK

New-Item -ItemType Directory -Force -Path $SDK | Out-Null
$zip = "$env:TEMP\cmdline-tools.zip"
if (-not (Test-Path "$SDK\cmdline-tools\latest\bin\sdkmanager.bat")) {
    Write-Host "downloading command-line tools..."
    Invoke-WebRequest -Uri 'https://dl.google.com/android/repository/commandlinetools-win-11076708_latest.zip' -OutFile $zip
    $tmp = "$env:TEMP\cmdt"
    if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
    Expand-Archive -Path $zip -DestinationPath $tmp
    New-Item -ItemType Directory -Force -Path "$SDK\cmdline-tools" | Out-Null
    if (Test-Path "$SDK\cmdline-tools\latest") { Remove-Item -Recurse -Force "$SDK\cmdline-tools\latest" }
    Move-Item "$tmp\cmdline-tools" "$SDK\cmdline-tools\latest"
    Remove-Item -Recurse -Force $tmp
}
Write-Host "accepting licenses..."
& "$SDK\cmdline-tools\latest\bin\sdkmanager.bat" --licenses | Out-Null
Write-Host "installing packages..."
& "$SDK\cmdline-tools\latest\bin\sdkmanager.bat" "platform-tools" "platforms;android-34" "build-tools;34.0.0" "ndk;27.2.12479018"
Write-Host "SDK-READY"
