# ExcelLens Windows x64 release builder with pinned, vendored pure-Go dependencies.
# Run with: powershell -NoProfile -ExecutionPolicy Bypass -File .\upgrade-build.ps1
[CmdletBinding()]
param(
    [string]$GoExe = 'go',
    [string]$UpxExe = 'upx',
    [string]$DeliveryRoot = '',
    [switch]$StageOnly,
    [switch]$TaggedRelease
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location -LiteralPath $Root

$versionSource = Get-Content -LiteralPath (Join-Path $Root 'internal/buildinfo/version.go') -Raw
if ($versionSource -notmatch 'const Version = "(\d+\.\d+\.\d+)"') { throw 'Application version is missing.' }
$Version = $Matches[1]
$moduleSource = Get-Content -LiteralPath (Join-Path $Root 'go.mod') -Raw
if ($moduleSource -notmatch '(?m)^go ([0-9.]+)\s*$') { throw 'Go toolchain version is missing.' }
$RequiredGo = 'go' + $Matches[1]
$ValidationDir = Join-Path $Root ('validation/' + $Version)
$CheckReport = if ($StageOnly) { Join-Path $ValidationDir 'private/candidate-check.json' } else { Join-Path $ValidationDir 'check.json' }
$PackageRoot = $Root
if ($DeliveryRoot) {
    $destination = Get-Item -LiteralPath $DeliveryRoot -ErrorAction Stop
    if (!$destination.PSIsContainer -or ($destination.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'DeliveryRoot must be a local ExcelLens checkout directory.'
    }
    $PackageRoot = $destination.FullName
    $destinationModule = Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'go.mod')
    $destinationVersion = Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'internal/buildinfo/version.go')
    if ($destinationModule -notmatch '(?m)^module excellens\s*$' -or
        $destinationVersion -notmatch ('const Version = "' + [regex]::Escape($Version) + '"')) {
        throw 'DeliveryRoot must be an ExcelLens checkout for this release version.'
    }
}
$ReleaseDir = Join-Path $PackageRoot 'release'
$StageDir = Join-Path $PackageRoot ('release.stage.' + [guid]::NewGuid().ToString('N'))
$BackupDir = Join-Path $PackageRoot ('release.previous.' + [guid]::NewGuid().ToString('N'))
# Bound every directory later moved or recursively removed to this workspace.
foreach ($releasePath in @($ReleaseDir, $StageDir, $BackupDir)) {
    if ((Split-Path -Parent ([System.IO.Path]::GetFullPath($releasePath))) -ne $PackageRoot) {
        throw "Release path escaped the project directory: $releasePath"
    }
    if ((Test-Path -LiteralPath $releasePath) -and ((Get-Item -LiteralPath $releasePath).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Release path is a reparse point: $releasePath"
    }
}

function Assert-Succeeded {
    param([string]$Description)
    if ($LASTEXITCODE -ne 0) { throw "$Description failed (exit code $LASTEXITCODE)." }
}

if ($TaggedRelease) {
    $releaseTag = 'v' + $Version
    $taggedCommit = (& git rev-parse --verify ($releaseTag + '^{commit}') | Out-String).Trim()
    Assert-Succeeded 'Resolve release tag'
    $headCommit = (& git rev-parse --verify HEAD | Out-String).Trim()
    Assert-Succeeded 'Resolve build checkout'
    if ($headCommit -ne $taggedCommit) { throw "Build checkout must match $releaseTag." }
    $pythonExe = (Get-Command python -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
}

# Never silently use an older compiler or pull code dependencies over the network.
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:CGO_ENABLED = '0'
$env:GOFLAGS = '-mod=vendor'
[Environment]::SetEnvironmentVariable('GOOS', $null, 'Process')
[Environment]::SetEnvironmentVariable('GOARCH', $null, 'Process')

Write-Host '[1/8] Checking Go compiler, UPX and dependencies...'
$upxCommand = Get-Command -Name $UpxExe -CommandType Application -ErrorAction Stop | Select-Object -First 1
$UpxExe = $upxCommand.Source
$upxVersionText = (& $UpxExe --version | Out-String).Trim()
Assert-Succeeded 'upx --version'
$upxVersion = ($upxVersionText -split '\r?\n')[0]
$goVersion = (& $GoExe version | Out-String).Trim()
Assert-Succeeded 'go version'
if (!$PSBoundParameters.ContainsKey('GoExe') -and $goVersion -notmatch "\b$([regex]::Escape($RequiredGo))\b") {
    # Go installs independently selected toolchains as verified modules. Use an
    # existing Windows toolchain when the system launcher is one patch behind.
    $moduleCache = (& $GoExe env GOMODCACHE | Out-String).Trim()
    Assert-Succeeded 'go env GOMODCACHE'
    $cachedGo = Join-Path $moduleCache ("golang.org/toolchain@v0.0.1-$RequiredGo.windows-amd64/bin/go.exe")
    if (Test-Path -LiteralPath $cachedGo -PathType Leaf) {
        $GoExe = $cachedGo
        $goVersion = (& $GoExe version | Out-String).Trim()
        Assert-Succeeded 'cached go version'
    }
}
if ($goVersion -notmatch "\b$([regex]::Escape($RequiredGo))\b") {
    throw "Only Go $RequiredGo may build this release. Found: $goVersion"
}
if ($goVersion -notmatch 'windows/amd64') {
    throw "This script must run with the Windows AMD64 Go toolchain. Found: $goVersion"
}
if (!(Test-Path -LiteralPath (Join-Path $Root 'vendor/modules.txt'))) {
    throw 'Vendored dependencies are missing. Use the complete source distribution.'
}
# Vendor mode validates go.mod/modules.txt consistency without downloading or
# requiring the developer's module cache. Versions and sums remain pinned.
& $GoExe list -deps ./... | Out-Null
Assert-Succeeded 'offline vendored dependency validation'

Write-Host '[2/8] Running the shared project quality gate...'
& $GoExe run ./tools/project check -report $CheckReport
Assert-Succeeded 'project check'

Write-Host '[3/8] Go, Node, package boundaries, metadata and documentation checks passed.'

Write-Host '[4/8] Refreshing Windows application icon and DPI manifest...'
& $GoExe run ./tools/icon -ico internal/branding/icon.ico -manifest cmd/excellens/app.manifest -o cmd/excellens/rsrc_windows_amd64.syso
Assert-Succeeded 'icon and manifest resource generation'

New-Item -ItemType Directory -Force -Path $StageDir | Out-Null
try {
    Write-Host '[5/8] Building and inspecting new Windows x64 executable...'
    if ($TaggedRelease) {
        # The icon generator has run. Include its tracked Windows resource,
        # embedded assets and untracked inputs in the build provenance check.
        $inputs = @('cmd','internal','vendor','tools','go.mod','go.sum','build.ps1')
        & git diff --quiet $taggedCommit -- @inputs
        Assert-Succeeded "Check tracked build inputs against $releaseTag"
        $extras = @(& git ls-files --others --exclude-standard -- @inputs)
        Assert-Succeeded 'Check for untracked build inputs'
        if ($extras.Count) { throw "Build inputs include files outside $releaseTag." }
    }
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $exe = Join-Path $StageDir 'ExcelLens.exe'
    & $GoExe build -trimpath -buildvcs=false -ldflags '-s -w -H=windowsgui' -o $exe ./cmd/excellens
    Assert-Succeeded 'Go Windows build'
    if (!(Test-Path -LiteralPath $exe)) { throw 'Output EXE was not produced.' }
    $buildMetadata = (& $GoExe version -m $exe | Out-String)
    Assert-Succeeded 'go version -m'
    if ($buildMetadata -notmatch "\b$([regex]::Escape($RequiredGo))\b" -or
        $buildMetadata -notmatch 'GOOS=windows' -or
        $buildMetadata -notmatch 'GOARCH=amd64' -or
        $buildMetadata -notmatch 'CGO_ENABLED=0') {
        throw "EXE build metadata did not match the required target: $buildMetadata"
    }
    $stream = [System.IO.File]::OpenRead($exe)
    try {
        if ($stream.ReadByte() -ne 77 -or $stream.ReadByte() -ne 90) {
            throw 'Output file is not a Windows PE executable.'
        }
    } finally { $stream.Dispose() }
    $unpackedBytes = (Get-Item -LiteralPath $exe).Length

    Write-Host '[6/8] Compressing executable with UPX --best --lzma...'
    # Compress the staged EXE before hashing/packaging and promoting it to
    # release/ExcelLens.exe, so a failed pack never replaces the last release.
    & $UpxExe --best --lzma $exe
    Assert-Succeeded 'upx --best --lzma'
    & $UpxExe -t $exe
    Assert-Succeeded 'upx integrity check'
    & $GoExe run ./tools/icon -check-exe $exe
    Assert-Succeeded 'packed executable DPI manifest verification'
    $packedBytes = (Get-Item -LiteralPath $exe).Length

    if ($TaggedRelease -or -not $StageOnly) {
        Write-Host 'Running the release performance matrix...'
        $benchArgs = @(('--go=' + $GoExe), ('--out=' + (Join-Path $ValidationDir 'private/release-performance.json')))
        $baseline = Join-Path $ValidationDir 'performance-baseline.json'
        if (!(Test-Path -LiteralPath $baseline -PathType Leaf)) { $benchArgs += '--save-baseline=' + $baseline }
        & node (Join-Path $Root 'tools/bench/release.cjs') @benchArgs
        Assert-Succeeded 'Release performance measurements and source fidelity'
    }

    Write-Host '[7/8] Writing clean release archive and checksums...'
    $fileNames = @(Get-Content -LiteralPath (Join-Path $Root 'tools/release-files.json') -Raw | ConvertFrom-Json | ForEach-Object { $_.Replace('{version}', $Version) })
    foreach ($file in $fileNames) {
        $target = Join-Path $StageDir $file
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $target) | Out-Null
        Copy-Item -LiteralPath (Join-Path $Root $file) -Destination $target -Force
    }
    Copy-Item -LiteralPath (Join-Path $Root 'internal/branding/icon.svg') -Destination (Join-Path $StageDir 'ExcelLens_icon.svg') -Force
    Copy-Item -LiteralPath (Join-Path $Root 'internal/branding/icon.ico') -Destination (Join-Path $StageDir 'ExcelLens_icon.ico') -Force
    $hash = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText((Join-Path $StageDir 'SHA256SUMS.txt'), "$hash  ExcelLens.exe`r`n", [System.Text.Encoding]::ASCII)
    [System.IO.File]::WriteAllText((Join-Path $StageDir 'BUILD_INFO.txt'), "ExcelLens $Version`r`nToolchain: $goVersion`r`nTarget: windows/amd64`r`nCGO: disabled`r`nDependencies: pinned pure-Go modules (offline vendor mode)`r`nPacker: $upxVersion --best --lzma (integrity checked)`r`nExecutable bytes before UPX: $unpackedBytes`r`nExecutable bytes after UPX: $packedBytes`r`n", [System.Text.Encoding]::UTF8)
    $files = @('ExcelLens.exe') + $fileNames + @('ExcelLens_icon.svg', 'ExcelLens_icon.ico', 'SHA256SUMS.txt', 'BUILD_INFO.txt')
    $files = $files | ForEach-Object { Join-Path $StageDir $_ }
    $zip = Join-Path $StageDir "ExcelLens_${Version}_Windows_x64_Full.zip"
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::Open($zip, [System.IO.Compression.ZipArchiveMode]::Create)
    try {
        foreach ($file in $files) {
            $relative = $file.Substring($StageDir.Length + 1).Replace('\', '/')
            [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($archive, $file, $relative, [System.IO.Compression.CompressionLevel]::Optimal) | Out-Null
        }
    } finally { $archive.Dispose() }
    if (!(Test-Path -LiteralPath $zip)) { throw 'Release ZIP was not produced.' }
    # Verify every packaged file against staging before replacing the old
    # release, including the exact compressed executable and its checksum.
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $package = [System.IO.Compression.ZipFile]::OpenRead($zip)
    try {
        if ($package.Entries.Count -ne $files.Count) { throw 'Release ZIP entry count did not match staging.' }
        foreach ($stagedFile in $files) {
            $relative = $stagedFile.Substring($StageDir.Length + 1).Replace('\', '/')
            $entry = $package.GetEntry($relative)
            if ($null -eq $entry) { throw "Release ZIP is missing $stagedFile" }
            $entryStream = $entry.Open()
            $hasher = [System.Security.Cryptography.SHA256]::Create()
            try {
                $entryHash = [BitConverter]::ToString($hasher.ComputeHash($entryStream)).Replace('-', '').ToLowerInvariant()
            } finally { $entryStream.Dispose(); $hasher.Dispose() }
            $stagedHash = (Get-FileHash -LiteralPath $stagedFile -Algorithm SHA256).Hash.ToLowerInvariant()
            if ($entryHash -ne $stagedHash) { throw "Release ZIP content mismatch: $stagedFile" }
        }
    } finally { $package.Dispose() }

    Write-Host '[8/8] Finalizing the verified package...'
    if ($TaggedRelease) {
        $measurements = Join-Path $ValidationDir 'private/release-performance.json'
        & $pythonExe (Join-Path $Root 'tools/qa/verify_release.py') $Version --finalize --ref $taggedCommit --dir $StageDir --performance-report $measurements
        Assert-Succeeded 'Verify tagged source, Windows archive and checksums before delivery'
    }
    # Do not replace an executable that is still running.
    $running = @(Get-Process -Name 'ExcelLens' -ErrorAction SilentlyContinue).Count -gt 0
    if ($StageOnly -or $running) {
        $artifactRoot = Join-Path $PackageRoot 'artifacts'
        $artifactDir = Join-Path $artifactRoot ($Version + '-verified-' + [guid]::NewGuid().ToString('N'))
        if ((Split-Path -Parent ([IO.Path]::GetFullPath($artifactDir))) -ne $artifactRoot) { throw 'Artifact path escaped the project.' }
        New-Item -ItemType Directory -Force -Path $artifactRoot | Out-Null
        Move-Item -LiteralPath $StageDir -Destination $artifactDir
        $StageDir = ''
        if ($StageOnly) {
            Write-Host "Verified candidate package saved to $artifactDir."
        } else {
            Write-Host "Verified package saved to $artifactDir. The running application and existing release were preserved."
        }
        return
    }
    if (Test-Path -LiteralPath $ReleaseDir) {
        Move-Item -LiteralPath $ReleaseDir -Destination $BackupDir -ErrorAction Stop
    }
    try {
        Move-Item -LiteralPath $StageDir -Destination $ReleaseDir -ErrorAction Stop
    } catch {
        if (Test-Path -LiteralPath $BackupDir) {
            Move-Item -LiteralPath $BackupDir -Destination $ReleaseDir -ErrorAction Stop
        }
        throw
    }
    if (Test-Path -LiteralPath $BackupDir) {
        Remove-Item -LiteralPath $BackupDir -Recurse -Force -ErrorAction Stop
    }
    $StageDir = ''
    Write-Host "SUCCESS: $(Join-Path $ReleaseDir ('ExcelLens_' + $Version + '_Windows_x64_Full.zip'))"
    Write-Host "Executable: $(Join-Path $ReleaseDir 'ExcelLens.exe')"
    Write-Host "SHA256: $hash"
} finally {
    # Clean partial staging products; keep the prior release on any failed check.
    if ($StageDir -and (Test-Path -LiteralPath $StageDir)) {
        Remove-Item -LiteralPath $StageDir -Recurse -Force
    }
}
