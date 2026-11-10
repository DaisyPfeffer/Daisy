param(
    [string]$IdmExePath = ""
)

$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This icon extraction script must be run on Windows."
}

Add-Type -AssemblyName System.Drawing

$repoRoot = Split-Path -Parent $PSScriptRoot

if ([string]::IsNullOrWhiteSpace($IdmExePath)) {
    $candidates = @()

    $programFiles = [Environment]::GetFolderPath("ProgramFiles")
    $programFilesX86 = [Environment]::GetFolderPath("ProgramFilesX86")
    $programW6432 = $env:ProgramW6432

    if ($programFiles) {
        $candidates += (Join-Path $programFiles "Internet Download Manager\IDMan.exe")
    }
    if ($programFilesX86) {
        $candidates += (Join-Path $programFilesX86 "Internet Download Manager\IDMan.exe")
    }
    if ($programW6432) {
        $candidates += (Join-Path $programW6432 "Internet Download Manager\IDMan.exe")
    }

    $registryKeys = @(
        "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\IDMan.exe",
        "HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\App Paths\IDMan.exe",
        "HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\IDMan.exe"
    )

    foreach ($key in $registryKeys) {
        try {
            $registeredPath = (Get-Item -LiteralPath $key).GetValue("")
            if ($registeredPath) {
                $candidates += [string]$registeredPath
            }
        } catch {
            # Missing registry entries are expected.
        }
    }

    $IdmExePath = $candidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
}

if ([string]::IsNullOrWhiteSpace($IdmExePath) -or -not (Test-Path -LiteralPath $IdmExePath -PathType Leaf)) {
    throw "Could not find IDMan.exe. Install IDM or pass -IdmExePath 'C:\Program Files (x86)\Internet Download Manager\IDMan.exe'."
}

$resolvedExe = (Resolve-Path -LiteralPath $IdmExePath).Path
$icoDir = Join-Path $repoRoot "installer"
$pngDir = Join-Path $repoRoot "internal\gui\assets"
$icoPath = Join-Path $icoDir "godm.ico"
$pngPath = Join-Path $pngDir "app.png"

New-Item -ItemType Directory -Force -Path $icoDir, $pngDir | Out-Null

$icon = [System.Drawing.Icon]::ExtractAssociatedIcon($resolvedExe)
if ($null -eq $icon) {
    throw "Windows could not extract an application icon from: $resolvedExe"
}

$sourceBitmap = $null
$outputBitmap = $null
$graphics = $null
$icoStream = $null

try {
    $icoStream = [System.IO.File]::Create($icoPath)
    $icon.Save($icoStream)
    $icoStream.Flush()
    $icoStream.Dispose()
    $icoStream = $null

    $sourceBitmap = $icon.ToBitmap()
    $outputBitmap = New-Object System.Drawing.Bitmap(64, 64, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [System.Drawing.Graphics]::FromImage($outputBitmap)
    $graphics.Clear([System.Drawing.Color]::Transparent)
    $graphics.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::HighQuality
    $graphics.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $graphics.DrawImage($sourceBitmap, 0, 0, 64, 64)
    $outputBitmap.Save($pngPath, [System.Drawing.Imaging.ImageFormat]::Png)
}
finally {
    if ($null -ne $graphics) { $graphics.Dispose() }
    if ($null -ne $outputBitmap) { $outputBitmap.Dispose() }
    if ($null -ne $sourceBitmap) { $sourceBitmap.Dispose() }
    if ($null -ne $icon) { $icon.Dispose() }
    if ($null -ne $icoStream) { $icoStream.Dispose() }
}

Write-Host "IDM icon extracted from: $resolvedExe"
Write-Host "Runtime icon: $pngPath"
Write-Host "Windows EXE icon: $icoPath"
