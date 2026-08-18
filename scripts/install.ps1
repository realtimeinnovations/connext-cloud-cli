# Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
# No duplications, whole or partial, manual or electronic, may be made
# without express written permission.  Any such copies, or revisions thereof,
# must display this notice unaltered.
# This code contains trade secrets of Real-Time Innovations, Inc.

#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$InstallDir = "$env:LOCALAPPDATA\Programs\rticloud"
)

$ErrorActionPreference = "Stop"

$Owner  = "realtimeinnovations"
$Repo   = "connext-cloud-cli"
$Binary = "rticloud"

function Get-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        "AMD64" { return "amd64" }
        "ARM64" { return "arm64" }
        default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
    }
}

function Resolve-Version {
    if ($Version -ne "latest") { return $Version }
    $response = Invoke-WebRequest -Uri "https://github.com/$Owner/$Repo/releases/latest" -MaximumRedirection 0 -ErrorAction SilentlyContinue -UseBasicParsing
    $location = $response.Headers["Location"]
    return $location -replace ".*/", ""
}

function Confirm-Checksum {
    param([string]$FilePath, [string]$ArchiveName, [string]$ChecksumsPath)
    $expected = (Get-Content $ChecksumsPath | Where-Object { $_ -match "\s+$([regex]::Escape($ArchiveName))$" }) -replace "\s.*", ""
    if (-not $expected) { throw "Checksum not found for $ArchiveName" }
    $actual = (Get-FileHash -Algorithm SHA256 $FilePath).Hash.ToLower()
    if ($actual -ne $expected) {
        throw "Checksum mismatch for $ArchiveName`nexpected: $expected`nactual:   $actual"
    }
}

function Install-Completions {
    # The installer may run under -ExecutionPolicy Bypass, so Get-ExecutionPolicy
    # returns the process-level override rather than the persistent system policy.
    # Walk the scopes that govern a fresh terminal session (GPO first, then registry)
    # and use those to decide whether the profile will actually be loadable.
    $blockedPolicies = @('Restricted', 'AllSigned')
    $persistentScopes = @('MachinePolicy', 'UserPolicy', 'CurrentUser', 'LocalMachine')
    # Default for Windows PowerShell 5.x when nothing is configured is Restricted.
    $persistentPolicy = 'Restricted'
    foreach ($scope in $persistentScopes) {
        $p = Get-ExecutionPolicy -Scope $scope
        if ($p -ne 'Undefined') {
            $persistentPolicy = $p
            break
        }
    }

    if ($persistentPolicy -in $blockedPolicies) {
        Write-Host ""
        Write-Host "Skipping PowerShell completions: script execution is disabled ($persistentPolicy)."
        Write-Host "To enable completions, run the following and then re-run this installer:"
        Write-Host "  Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser"
        return
    }

    $line = "`n$Binary completion powershell | Out-String | Invoke-Expression"
    if (-not (Test-Path $PROFILE)) {
        New-Item -ItemType File -Path $PROFILE -Force | Out-Null
    }
    if (-not (Select-String -Path $PROFILE -SimpleMatch "$Binary completion powershell" -Quiet)) {
        Add-Content -Path $PROFILE -Value $line
        Write-Host "PowerShell completions added to $PROFILE"
    }
}

function Add-ToUserPath {
    param([string]$Dir)

    # The user PATH is persisted in the registry and is inherited only by new
    # processes. Keep that update best-effort: the CLI is usable by its full
    # path even on managed machines where changing environment variables is
    # prohibited.
    $normalizedDir = $Dir.Trim().TrimEnd('\')
    $persisted = $false

    try {
        $current = [Environment]::GetEnvironmentVariable("PATH", "User")
        $entries = if ([string]::IsNullOrWhiteSpace($current)) {
            @()
        } else {
            @($current -split ";" | ForEach-Object { $_.Trim().TrimEnd('\') })
        }

        if ($entries -notcontains $normalizedDir) {
            $newPath = if ([string]::IsNullOrWhiteSpace($current)) { $Dir } else { "$current;$Dir" }
            [Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
            Write-Host "$Dir added to your user PATH."
        } else {
            Write-Host "$Dir is already in your user PATH."
        }
        $persisted = $true
    } catch {
        Write-Warning "Could not add $Dir to your user PATH: $($_.Exception.Message)"
    }

    # Make the command available when this installer is run directly in an
    # interactive PowerShell session. This cannot alter a parent cmd.exe that
    # launched PowerShell with `powershell -Command`.
    try {
        $sessionEntries = @($env:PATH -split ";" | ForEach-Object { $_.Trim().TrimEnd('\') })
        if ($sessionEntries -notcontains $normalizedDir) {
            $env:PATH = if ([string]::IsNullOrWhiteSpace($env:PATH)) { $Dir } else { "$env:PATH;$Dir" }
        }
    } catch {
        Write-Warning "Could not update PATH for this PowerShell session: $($_.Exception.Message)"
    }

    # Notify Explorer and other interested applications that the persisted
    # environment changed. Existing terminal processes still need reopening.
    if ($persisted) {
        try {
            if (-not ("RticloudInstaller.NativeMethods" -as [type])) {
                Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

namespace RticloudInstaller {
    public static class NativeMethods {
        [DllImport("user32.dll", CharSet = CharSet.Auto, SetLastError = true)]
        public static extern IntPtr SendMessageTimeout(
            IntPtr hWnd, uint msg, UIntPtr wParam, string lParam,
            uint flags, uint timeout, out UIntPtr result);
    }
}
'@
            }
            [UIntPtr]$result = [UIntPtr]::Zero
            [void][RticloudInstaller.NativeMethods]::SendMessageTimeout(
                [IntPtr]0xffff, 0x001A, [UIntPtr]::Zero, "Environment", 0x0002, 5000, [ref]$result)
        } catch {
            # The registry update is already complete; notification failure is
            # not a reason to fail a successful installation.
        }
    }

    return $persisted
}

$arch    = Get-Arch
$tag     = Resolve-Version
$archive = "${Repo}_windows_${arch}.zip"
$url     = "https://github.com/$Owner/$Repo/releases/download/$tag/$archive"
$checksumsUrl = "https://github.com/$Owner/$Repo/releases/download/$tag/checksums.txt"
$tmp     = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path "$($_.FullName)" }

try {
    Write-Host "Downloading $Binary $tag..."
    Invoke-WebRequest -Uri $url          -OutFile "$tmp\$archive"  -UseBasicParsing
    Invoke-WebRequest -Uri $checksumsUrl -OutFile "$tmp\checksums.txt" -UseBasicParsing

    Confirm-Checksum -FilePath "$tmp\$archive" -ArchiveName $archive -ChecksumsPath "$tmp\checksums.txt"

    Expand-Archive -Path "$tmp\$archive" -DestinationPath $tmp -Force

    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir | Out-Null
    }
    $InstallDir = (Resolve-Path -LiteralPath $InstallDir).Path

    Copy-Item -Path "$tmp\$Binary.exe" -Destination "$InstallDir\$Binary.exe" -Force

    Write-Host "Installed $Binary $tag to $InstallDir\$Binary.exe"

    $pathPersisted = Add-ToUserPath -Dir $InstallDir

    Write-Host ""
    Write-Host "Next step:"
    Write-Host "  $Binary configure"
    Write-Host ""
    Write-Host "Other existing terminal windows must be closed and reopened before using $Binary."
    Write-Host "If this terminal does not recognize $Binary, run it directly:"
    Write-Host "  & `"$InstallDir\$Binary.exe`" configure"
    if (-not $pathPersisted) {
        Write-Host ""
        Write-Host "To add it manually, add this directory to your user PATH:"
        Write-Host "  $InstallDir"
    }

    try {
        Install-Completions
    } catch {
        # Completion setup is optional and must not make a successful CLI
        # installation appear to have failed.
        Write-Warning "Could not install PowerShell completions: $($_.Exception.Message)"
    }
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
