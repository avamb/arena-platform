param(
  [Parameter(Mandatory=$true)][string]$Task,
  [switch]$Approved,
  [string]$CredentialTarget = 'Venue_SVG_Generator/Anthropic/APIKey'
)
$ErrorActionPreference = 'Stop'
if (-not $Approved) { throw 'Paid orchestration requires explicit -Approved.' }
if ($CredentialTarget -ne 'Venue_SVG_Generator/Anthropic/APIKey') { throw 'Only the production Anthropic Credential Manager target is allowed.' }
$root = Split-Path -Parent $PSScriptRoot
$runner = Join-Path $root 'orchestration\sdk-task-runner.mjs'
$taskPath = Join-Path $root $Task
if (-not (Test-Path -LiteralPath $taskPath)) { throw 'Task packet not found.' }
$node = (Get-Command node -ErrorAction Stop).Source
& $node $runner --task $Task --preflight --approved
if ($LASTEXITCODE -ne 0) { throw 'Orchestration preflight failed; credential was not read.' }
$helper = Join-Path $root 'sdk_experiments\windows-credential.ps1'
$secret = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action GetForPipe -Target $CredentialTarget
if ($LASTEXITCODE -ne 0 -or $null -eq $secret) { throw 'Credential Manager retrieval failed.' }
if ($secret -is [Array]) { $secret = $secret -join '' }
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $node
$psi.Arguments = ('"{0}" --task "{1}" --approved --key-stdin' -f $runner, $Task)
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$p = New-Object System.Diagnostics.Process
$p.StartInfo = $psi
try {
  [void]$p.Start()
  $p.StandardInput.Write([string]$secret)
  $p.StandardInput.Close()
  $out = $p.StandardOutput.ReadToEnd()
  $err = $p.StandardError.ReadToEnd()
  $p.WaitForExit()
  if ($out) { [Console]::Out.Write($out) }
  if ($err) { [Console]::Error.Write($err) }
  exit $p.ExitCode
} finally {
  $secret = $null
  $p.Dispose()
}
