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
    $response = Invoke-WebRequest -Uri "https://github.com/$Owner/$Repo/releases/latest" -MaximumRedirection 0 -ErrorAction SilentlyContinue
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
    $current = [Environment]::GetEnvironmentVariable("PATH", "User")
    if ($current -split ";" -contains $Dir) { return }
    [Environment]::SetEnvironmentVariable("PATH", "$current;$Dir", "User")
    Write-Host ""
    Write-Host "$Dir added to your PATH (restart your terminal to take effect)."
}

$arch    = Get-Arch
$tag     = Resolve-Version
$archive = "${Repo}_windows_${arch}.zip"
$url     = "https://github.com/$Owner/$Repo/releases/download/$tag/$archive"
$checksumsUrl = "https://github.com/$Owner/$Repo/releases/download/$tag/checksums.txt"
$tmp     = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path "$($_.FullName)" }

try {
    Write-Host "Downloading $Binary $tag..."
    Invoke-WebRequest -Uri $url          -OutFile "$tmp\$archive"
    Invoke-WebRequest -Uri $checksumsUrl -OutFile "$tmp\checksums.txt"

    Confirm-Checksum -FilePath "$tmp\$archive" -ArchiveName $archive -ChecksumsPath "$tmp\checksums.txt"

    Expand-Archive -Path "$tmp\$archive" -DestinationPath $tmp -Force

    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir | Out-Null
    }

    Copy-Item -Path "$tmp\$Binary.exe" -Destination "$InstallDir\$Binary.exe" -Force

    Write-Host "Installed $Binary $tag to $InstallDir\$Binary.exe"

    Add-ToUserPath -Dir $InstallDir

    Install-Completions
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
