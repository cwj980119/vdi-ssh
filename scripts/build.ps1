param([string]$GoExe = '')
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
if (-not $GoExe) {
    $taskLocalGo = Join-Path $taskRoot '.tools\go\bin\go.exe'
    if (Test-Path -LiteralPath $taskLocalGo) { $GoExe = $taskLocalGo }
    else { $GoExe = (Get-Command go -ErrorAction Stop).Source }
}
$GoExe = (Resolve-Path -LiteralPath $GoExe).Path
Push-Location -LiteralPath $taskRoot
$taskPriorGoPath = $env:GOPATH
$taskPriorGoCache = $env:GOCACHE
try {
    $env:GOPATH = Join-Path $taskRoot '.tools\gopath'
    $env:GOCACHE = Join-Path $taskRoot '.tools\gocache'
    & $GoExe mod verify
    if ($LASTEXITCODE -ne 0) { throw 'Module verification failed' }
    & $GoExe vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    & $GoExe test -count=1 -timeout 90s ./...
    if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
    $taskOutput = Join-Path $taskRoot 'dist\VDI-SSH'
    New-Item -ItemType Directory -Path $taskOutput -Force | Out-Null
    & $GoExe build -trimpath -ldflags '-s -w' -o (Join-Path $taskOutput 'vdi-ssh.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'Build failed' }
    Copy-Item -LiteralPath (Join-Path $taskRoot 'README.ko.md') -Destination $taskOutput
    Copy-Item -LiteralPath (Join-Path $taskRoot 'Start-VDI-Host.ps1') -Destination $taskOutput
    Copy-Item -LiteralPath (Join-Path $taskRoot 'Start-VDI-Host.cmd') -Destination $taskOutput
    $taskLicenses = Join-Path $taskOutput 'licenses'
    New-Item -ItemType Directory -Path $taskLicenses -Force | Out-Null
    $taskGoRoot = & $GoExe env GOROOT
    Copy-Item -LiteralPath (Join-Path $taskGoRoot 'LICENSE') -Destination (Join-Path $taskLicenses 'Go-LICENSE.txt') -Force
    foreach ($taskModule in @('golang.org/x/crypto','golang.org/x/sys','github.com/pkg/sftp','github.com/kr/fs')) {
        $taskModuleDir = & $GoExe list -m -f '{{.Dir}}' $taskModule
        if ($LASTEXITCODE -ne 0) { throw 'Cannot find module license' }
        $taskLicenseName = $taskModule.Replace('/','-') + '-LICENSE.txt'
        Copy-Item -LiteralPath (Join-Path $taskModuleDir 'LICENSE') -Destination (Join-Path $taskLicenses $taskLicenseName) -Force
    }
    & $GoExe version -m (Join-Path $taskOutput 'vdi-ssh.exe') | ForEach-Object { $_ -replace '^.*vdi-ssh\.exe:', 'vdi-ssh.exe:' } | Set-Content -LiteralPath (Join-Path $taskOutput 'BUILD-INFO.txt') -Encoding utf8
    $taskHash = (Get-FileHash -LiteralPath (Join-Path $taskOutput 'vdi-ssh.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    ($taskHash + '  vdi-ssh.exe') | Set-Content -LiteralPath (Join-Path $taskOutput 'SHA256SUMS.txt') -Encoding ascii
    $taskZip = Join-Path $taskRoot 'dist\VDI-SSH-windows-x64.zip'
    Compress-Archive -LiteralPath $taskOutput -DestinationPath $taskZip -Force
    $taskDistribution = Join-Path $taskRoot 'distribution'
    New-Item -ItemType Directory -Path $taskDistribution -Force | Out-Null
    Copy-Item -LiteralPath $taskZip -Destination $taskDistribution -Force
    $taskZipHash = (Get-FileHash -LiteralPath $taskZip -Algorithm SHA256).Hash.ToLowerInvariant()
    ($taskZipHash + '  VDI-SSH-windows-x64.zip') | Set-Content -LiteralPath (Join-Path $taskDistribution 'SHA256SUMS.txt') -Encoding ascii
    Get-FileHash -LiteralPath $taskZip -Algorithm SHA256
    Write-Output "Built: $taskZip"
} finally {
    $env:GOPATH = $taskPriorGoPath
    $env:GOCACHE = $taskPriorGoCache
    Pop-Location
}
