# Renames the app package/namespace from the upstream project to ours and
# rewrites every Kotlin/Java/manifest/gradle reference, so our APK installs
# side by side with the official build (different applicationId) instead of
# fighting it over the same package name.
$ErrorActionPreference = 'Stop'
$root = 'D:\OpenFlux-Modern\android'
$old = 'io.github.p1neapplexpress.openflux'
$newPkg = 'io.github.demyasha.openflux'

# 1) move the source trees (main java, tests, aidl)
function Move-Tree([string]$from, [string]$to) {
    if (-not (Test-Path $from)) { return }
    $parent = Split-Path $to -Parent
    New-Item -ItemType Directory -Force -Path $parent | Out-Null
    if (Test-Path $to) { Remove-Item -Recurse -Force $to }
    Move-Item $from $to -Force
}
Move-Tree "$root\app\src\main\java\io\github\p1neapplexpress\openflux" "$root\app\src\main\java\io\github\demyasha\openflux"
Move-Tree "$root\app\src\test\java\io\github\p1neapplexpress\openflux" "$root\app\src\test\java\io\github\demyasha\openflux"
Move-Tree "$root\app\src\main\aidl\io\github\p1neapplexpress\openflux"   "$root\app\src\main\aidl\io\github\demyasha\openflux"
Get-ChildItem "$root\app\src" -Recurse -Directory -ErrorAction SilentlyContinue |
    Where-Object { $_.FullName -match 'p1neapplexpress' } |
    Sort-Object { $_.FullName.Length } -Descending |
    ForEach-Object { if (-not (Get-ChildItem $_.FullName -Force)) { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue } }

# 2) rewrite references in text files
$files = Get-ChildItem $root -Recurse -Include *.kt,*.java,*.xml,*.kts,*.pro,*.aidl -File
$changed = 0
foreach ($f in $files) {
    $text = Get-Content $f.FullName -Raw
    if ($text -match [regex]::Escape($old)) {
        $text = $text.Replace($old, $newPkg)
        [IO.File]::WriteAllText($f.FullName, $text)
        $changed++
    }
}
Write-Host "package renamed to $newPkg ($changed of $($files.Count) files changed)"
