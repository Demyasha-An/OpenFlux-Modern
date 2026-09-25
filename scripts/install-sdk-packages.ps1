$ErrorActionPreference = 'Continue'
$env:JAVA_HOME = 'C:\Program Files\Microsoft\jdk-17.0.20.101-hotspot'
$env:ANDROID_HOME = 'D:\Android\sdk'
$env:ANDROID_SDK_ROOT = 'D:\Android\sdk'
$sm = 'D:\Android\sdk\cmdline-tools\latest\bin\sdkmanager.bat'
$pkgs = @('platform-tools','platforms;android-34','build-tools;34.0.0','ndk;27.2.12479018')
& $sm --licenses | Out-Null
& $sm @pkgs
if ($LASTEXITCODE -eq 0) { 'SDK-READY' } else { "SDK-FAILED $LASTEXITCODE" }
