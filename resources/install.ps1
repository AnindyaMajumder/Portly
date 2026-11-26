# Install Portly for the current user. Verifies the checksum.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repository = 'https://github.com/AnindyaMajumder/Portly'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'amd64' } 'ARM64' { 'arm64' } 'x86' { '386' } default { throw "unsupported processor $_" } }

# The latest release, from GitHub's redirect like install.sh, which has no rate limit
$version = "$env:PORTLY_VERSION".TrimStart('v')
if (-not $version) {
    $request = [System.Net.WebRequest]::Create("$repository/releases/latest")
    $request.AllowAutoRedirect = $false
    $response = $request.GetResponse()
    $version = ($response.Headers['Location'] -split '/v')[-1]
    $response.Close()
}
if ($version -notmatch '^[0-9A-Za-z.-]+$') { throw "could not find the latest release, see $repository/releases" }

$temporary = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $archive = "portly_${version}_windows_${arch}.zip"
    foreach ($file in $archive, 'checksums.txt') {
        try {
            Invoke-WebRequest -UseBasicParsing "$repository/releases/download/v$version/$file" -OutFile (Join-Path $temporary $file)
        } catch {
            throw "could not download $file. Check PORTLY_VERSION and your connection, or see $repository/releases"
        }
    }
    $line = Get-Content (Join-Path $temporary 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $archive }
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $temporary $archive)).Hash
    if (-not $line -or ($line -split '\s+')[0] -ne $actual) { throw 'checksum does not match, nothing was installed' }
    Expand-Archive (Join-Path $temporary $archive) $temporary
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    Move-Item -Force (Join-Path $temporary 'portly.exe') (Join-Path $directory 'portly.exe')
    Write-Output "Installed portly $version in $directory"
    if (($env:PATH -split ';') -notcontains $directory) {
        Write-Output "$directory is not on your PATH. Add it, then open a new terminal:"
        Write-Output "  [Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ';$directory', 'User')"
    }
} finally {
    Remove-Item -Recurse -Force $temporary
}
