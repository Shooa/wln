$ErrorActionPreference = "Stop"

$Repository = "Shooa/wln"
$InstallDir = if ($env:WLN_INSTALL_DIR) {
    $env:WLN_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "Programs\wln"
}

$Release = Invoke-RestMethod `
    -Headers @{ Accept = "application/vnd.github+json"; "User-Agent" = "wln-installer" } `
    -Uri "https://api.github.com/repos/$Repository/releases/latest"
$Tag = [string]$Release.tag_name
$Version = $Tag.TrimStart("v")
if (-not $Version -or $Tag -eq $Version) {
    throw "wln installer: could not determine the latest release"
}

$Architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($Architecture) {
    "x64" { $TargetArch = "amd64" }
    "arm64" { $TargetArch = "arm64" }
    default { throw "wln installer: unsupported architecture $Architecture" }
}

$Archive = "wln_${Version}_windows_${TargetArch}.zip"
$DownloadBase = "https://github.com/$Repository/releases/download/$Tag"
$TemporaryDir = Join-Path ([System.IO.Path]::GetTempPath()) ("wln-install-" + [guid]::NewGuid().ToString("N"))

try {
    New-Item -ItemType Directory -Path $TemporaryDir | Out-Null
    $ArchivePath = Join-Path $TemporaryDir $Archive
    $SumsPath = Join-Path $TemporaryDir "SHA256SUMS"

    Write-Host "Downloading wln $Version for windows/$TargetArch..."
    Invoke-WebRequest -UseBasicParsing -Uri "$DownloadBase/$Archive" -OutFile $ArchivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$DownloadBase/SHA256SUMS" -OutFile $SumsPath

    $ChecksumLine = Get-Content $SumsPath | Where-Object { $_ -match "\s(?:\./)?$([regex]::Escape($Archive))$" } | Select-Object -First 1
    if (-not $ChecksumLine) {
        throw "wln installer: release checksum is missing"
    }
    $Expected = ($ChecksumLine -split "\s+")[0].ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) {
        throw "wln installer: SHA-256 checksum mismatch"
    }

    $ExtractDir = Join-Path $TemporaryDir "extracted"
    Expand-Archive -Path $ArchivePath -DestinationPath $ExtractDir
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force -Path (Join-Path $ExtractDir "wln.exe") -Destination (Join-Path $InstallDir "wln.exe")

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $PathEntries = @($UserPath -split ";" | Where-Object { $_ })
    if ($PathEntries -notcontains $InstallDir) {
        $NewPath = (@($PathEntries) + $InstallDir) -join ";"
        [Environment]::SetEnvironmentVariable("Path", $NewPath, "User")
        $env:Path = "$env:Path;$InstallDir"
        Write-Host "Added $InstallDir to the user PATH. Open a new terminal to use it."
    }
    Write-Host "Installed wln $Version to $(Join-Path $InstallDir 'wln.exe')"
} finally {
    if (Test-Path $TemporaryDir) {
        Remove-Item -Recurse -Force $TemporaryDir
    }
}
