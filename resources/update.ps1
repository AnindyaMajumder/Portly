# Update Portly to the latest release. The binary verifies and replaces itself.
$ErrorActionPreference = 'Stop'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$binary = Join-Path $directory 'portly.exe'
if (-not (Test-Path -PathType Leaf $binary)) {
    $found = Get-Command portly -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $found) {
        throw "portly is not installed. Install it with install.ps1 from https://github.com/AnindyaMajumder/Portly"
    }
    $binary = $found.Source
}
# portly prints its own errors and next steps
& $binary update --yes
