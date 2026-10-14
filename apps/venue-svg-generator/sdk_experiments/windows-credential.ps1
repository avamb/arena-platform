[CmdletBinding()]
param(
    [ValidateSet('Set','GetForPipe','Status','Delete','SelfTest','SetTest')]
    [string]$Action,
    [string]$Target = 'Venue_SVG_Generator/Anthropic/APIKey'
)

$ErrorActionPreference = 'Stop'
$DefaultTarget = 'Venue_SVG_Generator/Anthropic/APIKey'
$TestPrefix = 'Venue_SVG_Generator/Test/'

if ($Target -ne $DefaultTarget -and -not $Target.StartsWith($TestPrefix, [StringComparison]::Ordinal)) {
    throw 'Target is outside the Venue_SVG_Generator credential namespace.'
}
if ($Target.Contains("`0") -or $Target.Contains('/') -eq $false) { throw 'Invalid target.' }

if (-not ('VenueCredential.Native' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;

namespace VenueCredential {
  public static class Native {
    public const uint CRED_TYPE_GENERIC = 1;
    public const uint CRED_PERSIST_LOCAL_MACHINE = 2;
    public const uint ERROR_NOT_FOUND = 1168;

    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
    public struct CREDENTIAL {
      public uint Flags;
      public uint Type;
      public IntPtr TargetName;
      public IntPtr Comment;
      public System.Runtime.InteropServices.ComTypes.FILETIME LastWritten;
      public uint CredentialBlobSize;
      public IntPtr CredentialBlob;
      public uint Persist;
      public uint AttributeCount;
      public IntPtr Attributes;
      public IntPtr TargetAlias;
      public IntPtr UserName;
    }

    [DllImport("advapi32.dll", EntryPoint="CredWriteW", CharSet=CharSet.Unicode, SetLastError=true)]
    public static extern bool CredWrite(ref CREDENTIAL credential, uint flags);
    [DllImport("advapi32.dll", EntryPoint="CredReadW", CharSet=CharSet.Unicode, SetLastError=true)]
    public static extern bool CredRead(string target, uint type, uint flags, out IntPtr credential);
    [DllImport("advapi32.dll", EntryPoint="CredDeleteW", CharSet=CharSet.Unicode, SetLastError=true)]
    public static extern bool CredDelete(string target, uint type, uint flags);
    [DllImport("advapi32.dll", EntryPoint="CredFree")]
    public static extern void CredFree(IntPtr credential);
  }
}
'@
}

function Convert-Win32Error([int]$Code) {
    if ($Code -eq [VenueCredential.Native]::ERROR_NOT_FOUND) { return 'missing' }
    return "Windows Credential Manager operation failed (code $Code)."
}

function Read-CredentialSecret([string]$Name) {
    $native = [IntPtr]::Zero
    if (-not [VenueCredential.Native]::CredRead($Name, [VenueCredential.Native]::CRED_TYPE_GENERIC, 0, [ref]$native)) {
        $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
        throw (Convert-Win32Error $code)
    }
    try {
        $value = [Runtime.InteropServices.Marshal]::PtrToStructure($native, [type][VenueCredential.Native+CREDENTIAL])
        if ($value.CredentialBlobSize -eq 0 -or $value.CredentialBlob -eq [IntPtr]::Zero) { throw 'Credential is empty.' }
        $bytes = New-Object byte[] ([int]$value.CredentialBlobSize)
        [Runtime.InteropServices.Marshal]::Copy($value.CredentialBlob, $bytes, 0, $bytes.Length)
        return [Text.Encoding]::UTF8.GetString($bytes)
    } finally {
        [VenueCredential.Native]::CredFree($native)
    }
}

function Write-CredentialSecret([string]$Name, [string]$Secret, [bool]$ValidateApiFormat) {
    if ($ValidateApiFormat -and $Secret -notmatch '^sk-ant-[A-Za-z0-9_-]{20,}$') { throw 'Credential format rejected.' }
    $targetPtr = [Runtime.InteropServices.Marshal]::StringToCoTaskMemUni($Name)
    $userPtr = [Runtime.InteropServices.Marshal]::StringToCoTaskMemUni('venue-svg-agent')
    $bytes = [Text.Encoding]::UTF8.GetBytes($Secret)
    $blobPtr = [Runtime.InteropServices.Marshal]::AllocHGlobal($bytes.Length)
    try {
        [Runtime.InteropServices.Marshal]::Copy($bytes, 0, $blobPtr, $bytes.Length)
        $credential = New-Object VenueCredential.Native+CREDENTIAL
        $credential.Type = [VenueCredential.Native]::CRED_TYPE_GENERIC
        $credential.TargetName = $targetPtr
        $credential.UserName = $userPtr
        $credential.CredentialBlobSize = [uint32]$bytes.Length
        $credential.CredentialBlob = $blobPtr
        $credential.Persist = [VenueCredential.Native]::CRED_PERSIST_LOCAL_MACHINE
        if (-not [VenueCredential.Native]::CredWrite([ref]$credential, 0)) {
            throw (Convert-Win32Error ([Runtime.InteropServices.Marshal]::GetLastWin32Error()))
        }
    } finally {
        [Array]::Clear($bytes, 0, $bytes.Length)
        [Runtime.InteropServices.Marshal]::FreeHGlobal($blobPtr)
        [Runtime.InteropServices.Marshal]::FreeCoTaskMem($targetPtr)
        [Runtime.InteropServices.Marshal]::FreeCoTaskMem($userPtr)
    }
}

function Test-CredentialPresent([string]$Name) {
    $native = [IntPtr]::Zero
    $ok = [VenueCredential.Native]::CredRead($Name, [VenueCredential.Native]::CRED_TYPE_GENERIC, 0, [ref]$native)
    if ($native -ne [IntPtr]::Zero) { [VenueCredential.Native]::CredFree($native) }
    if ($ok) { return $true }
    $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
    if ($code -eq [VenueCredential.Native]::ERROR_NOT_FOUND) { return $false }
    throw (Convert-Win32Error $code)
}

function Remove-Credential([string]$Name) {
    if (-not [VenueCredential.Native]::CredDelete($Name, [VenueCredential.Native]::CRED_TYPE_GENERIC, 0)) {
        $code = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
        if ($code -ne [VenueCredential.Native]::ERROR_NOT_FOUND) { throw (Convert-Win32Error $code) }
    }
}

function Write-PipeSecret([string]$Secret) {
    if (-not [Console]::IsOutputRedirected) { throw 'GetForPipe refuses an interactive terminal.' }
    $bytes = [Text.Encoding]::UTF8.GetBytes($Secret)
    try { [Console]::OpenStandardOutput().Write($bytes, 0, $bytes.Length) }
    finally { [Array]::Clear($bytes, 0, $bytes.Length) }
}

function Invoke-SelfTest {
    $name = 'Venue_SVG_Generator/Test/LocalSelfTest'
    if (Test-CredentialPresent $name) { throw 'Self-test target already exists; refusing to overwrite it.' }
    $passed = $false
    try {
        Write-CredentialSecret $name 'self-test-placeholder-value' $false
        $present = Test-CredentialPresent $name
        $read = Read-CredentialSecret $name
        if (-not $present -or $read -cne 'self-test-placeholder-value') { throw 'Self-test value mismatch.' }
        Remove-Credential $name
        $missing = -not (Test-CredentialPresent $name)
        if (-not $missing) { throw 'Self-test delete did not produce Missing.' }
        $passed = $true
    } finally {
        Remove-Credential $name
    }
    if (-not $passed) { throw 'Credential Manager self-test failed.' }
    [Console]::Out.WriteLine('{"status":"PASS","operations":["Set","Status","Get","Delete"],"scope":"current-user","api_calls":0}')
}

switch ($Action) {
    'Set' {
        $secure = Read-Host 'Anthropic API key (hidden; never paste into chat)' -AsSecureString
        $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
        try {
            $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr)
            Write-CredentialSecret $DefaultTarget $plain $true
        } finally {
            [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
            $secure.Dispose()
            Remove-Variable plain -ErrorAction SilentlyContinue
        }
        [Console]::Out.WriteLine('{"status":"Present","operation":"Set","scope":"current-user"}')
    }
    'GetForPipe' {
        $secret = Read-CredentialSecret $Target
        try { Write-PipeSecret $secret }
        finally { $secret = $null }
    }
    'Status' {
        $present = Test-CredentialPresent $Target
        $status = if ($present) { 'Present' } else { 'Missing' }
        [Console]::Out.WriteLine((ConvertTo-Json @{ status=$status; target=$Target; type='Generic'; scope='current-user' } -Compress))
    }
    'Delete' {
        Remove-Credential $Target
        [Console]::Out.WriteLine((ConvertTo-Json @{ status='Missing'; target=$Target; operation='Delete'; scope='current-user' } -Compress))
    }
    'SetTest' {
        if (-not $Target.StartsWith($TestPrefix, [StringComparison]::Ordinal)) { throw 'SetTest is restricted to the test namespace.' }
        Write-CredentialSecret $Target 'self-test-pipe-placeholder-value' $false
        [Console]::Out.WriteLine('{"status":"Present","operation":"SetTest","scope":"current-user"}')
    }
    'SelfTest' { Invoke-SelfTest }
}
