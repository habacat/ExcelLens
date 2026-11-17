# One-step pinned toolchain setup + verified ExcelLens Windows release build.
# Uses a temporary official Go distribution; does not uninstall Go used by other projects.
[CmdletBinding()]
param([string]$UpxExe = 'upx')
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$toolchain = Get-Content -LiteralPath (Join-Path $Root 'tools/go-toolchain.json') -Raw | ConvertFrom-Json
$moduleSource = Get-Content -LiteralPath (Join-Path $Root 'go.mod') -Raw
if ($moduleSource -notmatch '(?m)^go ([0-9.]+)\s*$' -or $Matches[1] -ne $toolchain.version) { throw 'Toolchain download receipt does not match go.mod.' }
$RequiredVersion = 'go' + $toolchain.version
$VersionPattern = '\b' + [regex]::Escape($RequiredVersion) + '\b'
$ArchiveName = $RequiredVersion + '.windows-amd64.zip'
$ExpectedSHA256 = $toolchain.windowsAMD64SHA256

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -or -not [Environment]::Is64BitOperatingSystem) {
    throw 'This upgrade builder requires Windows 10/11 x64.'
}

$existing = Get-Command -Name 'go.exe' -CommandType Application -ErrorAction SilentlyContinue
$GoExe = $null
if ($null -ne $existing) {
    $versionText = (& $existing.Source version | Out-String).Trim()
    if ($LASTEXITCODE -eq 0 -and $versionText -match $VersionPattern -and $versionText -match 'windows/amd64') {
        $GoExe = $existing.Source
        Write-Host "Using installed $versionText"
    }
}

$temporary = $null
$originalPath = $env:Path
try {
    if (-not $GoExe) {
        $temporary = Join-Path ([System.IO.Path]::GetTempPath()) ('ExcelLens-toolchain-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Force -Path $temporary | Out-Null
        $archive = Join-Path $temporary $ArchiveName
        # Windows PowerShell 5.1 may otherwise negotiate obsolete TLS defaults.
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        $downloadUrls = @(
            "https://go.dev/dl/$ArchiveName",
            "https://dl.google.com/go/$ArchiveName"
        )
        $downloaded = $false
        $oldProgress = $ProgressPreference
        $ProgressPreference = 'SilentlyContinue'
        try {
            foreach ($url in $downloadUrls) {
                Write-Host "Downloading official $RequiredVersion toolchain from $url"
                try {
                    Invoke-WebRequest -Uri $url -OutFile $archive -UseBasicParsing -ErrorAction Stop
                    $downloaded = $true
                    break
                } catch {
                    Write-Warning "Could not download from this mirror: $($_.Exception.Message)"
                    if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
                }
            }
        } finally { $ProgressPreference = $oldProgress }
        if (-not $downloaded) { throw 'Both official Go download sources failed. No previous release has been removed.' }
        $actualSHA256 = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualSHA256 -ne $ExpectedSHA256) {
            throw "Go archive SHA256 mismatch. Expected $ExpectedSHA256; got $actualSHA256. Aborting."
        }
        Write-Host 'Toolchain SHA256 matches the official Go release page.'
        Expand-Archive -LiteralPath $archive -DestinationPath $temporary -Force
        Remove-Item -LiteralPath $archive -Force
        $GoExe = Join-Path $temporary 'go/bin/go.exe'
        if (!(Test-Path -LiteralPath $GoExe)) { throw 'Downloaded archive did not contain go/bin/go.exe.' }
        $env:Path = (Join-Path $temporary 'go/bin') + ';' + $originalPath
    }
    $versionText = (& $GoExe version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $versionText -notmatch $VersionPattern) {
        throw "Invalid toolchain: $versionText"
    }
    Write-Host "Verified compiler: $versionText"
    & (Join-Path $Root 'build.ps1') -GoExe $GoExe -UpxExe $UpxExe
    if (-not $?) { throw 'ExcelLens release build did not complete.' }
} finally {
    $env:Path = $originalPath
    if ($temporary -and (Test-Path -LiteralPath $temporary)) {
        $resolvedTemporary = [IO.Path]::GetFullPath($temporary)
        $expectedParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
        if ((Split-Path -Parent $resolvedTemporary) -ne $expectedParent -or (Split-Path -Leaf $resolvedTemporary) -notlike 'ExcelLens-toolchain-*') { throw 'Unexpected toolchain cleanup path.' }
        Remove-Item -LiteralPath $temporary -Recurse -Force
    }
}
