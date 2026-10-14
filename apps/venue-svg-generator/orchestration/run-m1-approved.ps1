param(
  [Parameter(Mandatory=$true)][string]$Task,
  [Parameter(Mandatory=$true)][string]$Worktree,
  [Parameter(Mandatory=$true)][string]$RuntimeRoot,
  [switch]$Approved,
  [string]$CredentialTarget = 'Venue_SVG_Generator/Anthropic/APIKey'
)
$ErrorActionPreference = 'Stop'
if (-not $Approved) { throw 'M1 paid execution requires explicit -Approved.' }
if ($CredentialTarget -ne 'Venue_SVG_Generator/Anthropic/APIKey') { throw 'Only the approved local Credential Manager target is allowed.' }
$root = Split-Path -Parent $PSScriptRoot
$runner = Join-Path $root 'orchestration\m1-write-task-runner.mjs'
$taskPath = Join-Path $root $Task
if (-not (Test-Path -LiteralPath $taskPath)) { throw 'Task packet not found.' }
if (-not (Test-Path -LiteralPath $Worktree)) { throw 'Dedicated worktree not found.' }
$runtime = (Resolve-Path -LiteralPath $RuntimeRoot).Path
if (-not (Test-Path -LiteralPath (Join-Path $runtime 'sdk_experiments\package.json'))) { throw 'Runtime SDK package not found.' }
$expectedRuntime = (Resolve-Path -LiteralPath (Join-Path (Split-Path -Parent $root) 'Venue_SVG_Generator')).Path
if ($runtime -ne $expectedRuntime) { throw 'RuntimeRoot must be the main Venue_SVG_Generator checkout.' }
if ((Resolve-Path -LiteralPath $Worktree).Path -eq $runtime) { throw 'RuntimeRoot must be separate from the writable worktree.' }
$env:M1_RUNTIME_ROOT = $runtime
$node = (Get-Command node -ErrorAction Stop).Source
& $node $runner --task $Task --worktree $Worktree --preflight --approved
if ($LASTEXITCODE -ne 0) { throw 'M1 preflight failed; credential was not read.' }
$helper = Join-Path $runtime 'sdk_experiments\windows-credential.ps1'
$secret = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action GetForPipe -Target $CredentialTarget
if ($LASTEXITCODE -ne 0 -or $null -eq $secret) { throw 'Credential Manager retrieval failed.' }
if ($secret -is [Array]) { $secret = $secret -join '' }
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $node
$psi.Arguments = ('"{0}" --task "{1}" --worktree "{2}" --approved --key-stdin' -f $runner, $Task, $Worktree)
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
