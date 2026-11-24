# Install Portly for the current user. Verifies the checksum.
$ErrorActionPreference = 'Stop'
$repository = 'https://github.com/AnindyaMajumder/Portly'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } 'x86' { '386' } default { throw "unsupported processor $_" } }

$version = $env:PORTLY_VERSION
if (-not $version) {
    $latest = Invoke-RestMethod 'https://api.github.com/repos/AnindyaMajumder/Portly/releases/latest'
    $version = $latest.tag_name.TrimStart('v')
}
if ($version -notmatch '^[0-9A-Za-z.-]+$') { throw 'could not find the latest version' }

$temporary = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $archive = "portly_${version}_windows_${arch}.zip"
    foreach ($file in $archive, 'checksums.txt') {
        Invoke-WebRequest -UseBasicParsing "$repository/releases/download/v$version/$file" -OutFile (Join-Path $temporary $file)
    }
    $line = Get-Content (Join-Path $temporary 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive }
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $temporary $archive)).Hash
    if (-not $line -or ($line -split '\s+')[0] -ne $actual) { throw 'checksum does not match, nothing was installed' }
    Expand-Archive (Join-Path $temporary $archive) $temporary
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    Move-Item -Force (Join-Path $temporary 'portly.exe') (Join-Path $directory 'portly.exe')
    Write-Output "Installed portly $version in $directory"
    if (($env:PATH -split ';') -notcontains $directory) { Write-Output "Add $directory to your PATH" }
} finally {
    Remove-Item -Recurse -Force $temporary
}
