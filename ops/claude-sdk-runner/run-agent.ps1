# run-agent.ps1 — лаунчер для run-agent.mjs.
# Достаёт API-ключ Anthropic из Windows Credential Manager (запись
# Venue_SVG_Generator/Anthropic/APIKey, создана helper-ом генератора) и подаёт его
# node-процессу через stdin. Ключ не попадает в аргументы, файлы и окружение PowerShell.
#
# Пример:
#   .\run-agent.ps1 -PromptFile C:\tmp\task.md -Cwd C:\Projects\arena_new_master -Mode read -Model claude-sonnet-5-5 -MaxBudgetUsd 2
#
# Все остальные параметры пробрасываются в run-agent.mjs как есть (см. шапку .mjs).
param(
    [Parameter(Mandatory = $true)][string]$PromptFile,
    [string]$Cwd = (Get-Location).Path,
    [ValidateSet('read', 'edit')][string]$Mode = 'read',
    [string]$Model = 'claude-sonnet-5-5',
    [int]$MaxTurns = 30,
    [double]$MaxBudgetUsd = 3,
    [ValidateSet('low', 'medium', 'high')][string]$Effort = 'medium',
    [int]$TimeoutMin = 30,
    [string]$Out = '',
    [string]$CredentialTarget = 'Venue_SVG_Generator/Anthropic/APIKey',
    [string]$CredentialHelper = 'C:\Projects\Venue_SVG_Generator\sdk_experiments\windows-credential.ps1'
)
$ErrorActionPreference = 'Stop'
$runner = Join-Path $PSScriptRoot 'run-agent.mjs'

$helperArgs = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $CredentialHelper,
    '-Action', 'GetForPipe', '-Target', $CredentialTarget)
$secret = & powershell.exe @helperArgs
if ($LASTEXITCODE -ne 0 -or $null -eq $secret) { throw 'Credential Manager retrieval failed.' }
if ($secret -is [Array]) { $secret = $secret -join '' }

$nodeArgs = @($runner, '--prompt-file', $PromptFile, '--cwd', $Cwd, '--mode', $Mode, '--model', $Model,
    '--max-turns', "$MaxTurns", '--max-budget-usd', "$MaxBudgetUsd", '--effort', $Effort, '--timeout-min', "$TimeoutMin")
if ($Out -ne '') { $nodeArgs += @('--out', $Out) }

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = (Get-Command node -ErrorAction Stop).Source
$psi.Arguments = ($nodeArgs | ForEach-Object { '"' + ($_.Replace('"', '\"')) + '"' }) -join ' '
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$process = New-Object System.Diagnostics.Process
$process.StartInfo = $psi
try {
    [void]$process.Start()
    $process.StandardInput.Write([string]$secret)
    $process.StandardInput.Close()
    $stdout = $process.StandardOutput.ReadToEnd()
    $stderr = $process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($stdout) { [Console]::Out.Write($stdout) }
    if ($stderr) { [Console]::Error.Write($stderr) }
    exit $process.ExitCode
} finally {
    $secret = $null
    $process.Dispose()
}
