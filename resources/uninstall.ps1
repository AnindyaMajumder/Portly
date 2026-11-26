# Remove Portly installed by install.ps1.
$ErrorActionPreference = 'Stop'
$directory = if ($env:PORTLY_INSTALL_DIR) { $env:PORTLY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Portly' }
$binary = Join-Path $directory 'portly.exe'
if (-not (Test-Path -PathType Leaf $binary)) { throw "portly is not installed in $directory" }
if ((& $binary --version) -notmatch '^portly ') { throw "$binary is not portly, so it was left alone" }
Remove-Item -Force $binary
Remove-Item -Force -ErrorAction SilentlyContinue "$binary.old"
if (-not (Get-ChildItem -Force $directory)) { Remove-Item $directory }
Write-Output "Removed portly from $directory"
