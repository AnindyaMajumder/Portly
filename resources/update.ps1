# Update Portly to the latest release. The binary verifies and replaces itself.
$ErrorActionPreference = 'Stop'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$binary = Join-Path $directory 'portly.exe'
if (-not (Test-Path -PathType Leaf $binary)) {
    throw "portly is not installed in $directory. Install it with install.ps1 from https://github.com/AnindyaMajumder/Portly"
}
& $binary update --yes
if ($LASTEXITCODE -ne 0) { throw "update failed, exit status $LASTEXITCODE" }
