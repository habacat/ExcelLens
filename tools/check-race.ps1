# Developer-only Windows race check; leaves the installed compiler and
# persistent environment unchanged. Production build.ps1 disables CGO.
[CmdletBinding()]
param(
    [string]$GoExe = 'go',
    [string]$Compiler = 'gcc',
    [string]$CompilerToolsDir = '',
    [string]$ReportPath = ''
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$originalLocation = Get-Location
$compilerPath = (Get-Command -Name $Compiler -CommandType Application -ErrorAction Stop).Source
$previousEnv = @{}
foreach ($key in @('CC', 'CGO_LDFLAGS', 'PATH')) {
    $previousEnv[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    Set-Location -LiteralPath $projectRoot
    $env:CC = '"' + $compilerPath.Replace('\', '/') + '"'
    $env:PATH = (Split-Path -Parent $compilerPath) + [IO.Path]::PathSeparator + $previousEnv['PATH']
    if ($CompilerToolsDir) {
        $toolFolder = Get-Item -LiteralPath $CompilerToolsDir -ErrorAction Stop
        if (!$toolFolder.PSIsContainer) { throw 'CompilerToolsDir must be a directory.' }
        $prefixFlag = '"-B' + $toolFolder.FullName.Replace('\', '/').TrimEnd('/') + '/"'
        $env:CC += ' ' + $prefixFlag
        # Go feature probes omit extra CC arguments. LDFLAGS must carry the
        # same helper prefix so the race linker's ASLR policy is detected.
        $env:CGO_LDFLAGS = ($prefixFlag + ' ' + $previousEnv['CGO_LDFLAGS']).Trim()
        $env:PATH = $toolFolder.FullName + [IO.Path]::PathSeparator + $env:PATH
    }
    if (!$ReportPath) {
        $applicationVersion = (& $GoExe run ./tools/project version | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw 'Cannot read the application version.' }
        $ReportPath = Join-Path $projectRoot ('validation/' + $applicationVersion + '/check-race.json')
    }
    & $GoExe run ./tools/project check -race -report $ReportPath
    if ($LASTEXITCODE -ne 0) { throw 'Project race checks failed; inspect the receipt and output.' }
} finally {
    foreach ($key in $previousEnv.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previousEnv[$key], 'Process')
    }
    Set-Location -LiteralPath $originalLocation
}
