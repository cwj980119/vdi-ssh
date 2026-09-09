param(
    [string]$DataDir = "$env:APPDATA\VDISSH"
)

$ErrorActionPreference = 'Stop'
$taskExe = Join-Path $PSScriptRoot 'vdi-ssh.exe'
if (-not (Test-Path -LiteralPath $taskExe)) {
    throw "vdi-ssh.exe was not found next to this launcher: $taskExe"
}
if (-not (Test-Path -LiteralPath (Join-Path $DataDir 'config.json'))) {
    throw "Server configuration was not found: $DataDir. Run vdi-ssh.exe init and screen-init first."
}

& $taskExe serve-all --data-dir $DataDir
