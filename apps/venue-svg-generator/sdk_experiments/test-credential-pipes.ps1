$ErrorActionPreference = 'Stop'
$helper = Join-Path $PSScriptRoot 'windows-credential.ps1'
$testTarget = 'Venue_SVG_Generator/Test/PipeProbe'
$fake = [Text.Encoding]::UTF8.GetBytes('self-test-pipe-placeholder-value')
$hash = [BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash($fake)).Replace('-','').ToLowerInvariant()
$set = $false
try {
    & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action SetTest -Target $testTarget | Out-Null
    $set = $true
    $first = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action Status -Target $testTarget | ConvertFrom-Json
    if ($first.status -ne 'Present') { throw 'Test target was not Present.' }
    $agent = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'run-approved-test.ps1') -CheckCredentialPipe -CredentialTarget $testTarget -ExpectedHash $hash
    if ($LASTEXITCODE -ne 0) { throw 'Agent launcher credential pipe failed.' }
    $vision = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'run-ladvi-approved-test.ps1') -CheckCredentialPipe -CredentialTarget $testTarget -ExpectedHash $hash
    if ($LASTEXITCODE -ne 0) { throw 'Vision launcher credential pipe failed.' }
    $delete = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action Delete -Target $testTarget | ConvertFrom-Json
    $set = $false
    $last = & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action Status -Target $testTarget | ConvertFrom-Json
    if ($last.status -ne 'Missing') { throw 'Test target was not deleted.' }
    [Console]::Out.WriteLine('{"status":"PASS","operations":["SetTest","Status","AgentPipe","VisionPipe","Delete","StatusMissing"],"api_calls":0}')
} finally {
    if ($set) { & powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $helper -Action Delete -Target $testTarget | Out-Null }
}
