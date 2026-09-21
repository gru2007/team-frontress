# Install the VGUI main-menu resource outside pak1.vpk and make the loose path visible to VGUI.
# Run from repository root:
# powershell -ExecutionPolicy Bypass -File .\game_clean\install_loose_vgui_mainmenu.ps1
$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$source = Join-Path $root 'game_src\tc2\pak1\resource\ui\mainmenuoverride.res'
$target = Join-Path $root 'game\tc2\loose\resource\ui\mainmenuoverride.res'
$gameinfo = Join-Path $root 'game\tc2\gameinfo.txt'

if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "VGUI main-menu resource not found: $source"
}
if (-not (Test-Path -LiteralPath $gameinfo -PathType Leaf)) {
    throw "gameinfo.txt not found: $gameinfo"
}

# A VGUI lookup can use the vgui path ID. Merely putting a file in a directory
# mounted as game+mod+custom_mod does not make that directory a vgui search path.
$oldMount = 'game+mod+custom_mod |gameinfo_path|loose'
$newMount = 'game+mod+vgui+custom_mod |gameinfo_path|loose'
$info = [System.IO.File]::ReadAllText($gameinfo)
if (-not $info.Contains($newMount)) {
    if (-not $info.Contains($oldMount)) {
        throw "Expected loose SearchPaths entry not found in $gameinfo; no changes made."
    }
    Copy-Item -LiteralPath $gameinfo -Destination "$gameinfo.bak" -Force
    $info = $info.Replace($oldMount, $newMount)
    [System.IO.File]::WriteAllText($gameinfo, $info, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "Added VGUI path ID for loose resources in: $gameinfo"
} else {
    Write-Host 'VGUI loose search path already configured.'
}

$directory = Split-Path -Parent $target
New-Item -ItemType Directory -Path $directory -Force | Out-Null
if (Test-Path -LiteralPath $target -PathType Leaf) {
    $backup = "$target.bak"
    Copy-Item -LiteralPath $target -Destination $backup -Force
    Write-Host "Backed up existing loose main menu: $backup"
}
Copy-Item -LiteralPath $source -Destination $target -Force
Write-Host "Installed loose VGUI main menu: $target"
Write-Host 'pak1.vpk was not modified. Completely restart the game with cl_mainmenu_webui 0 to test the VGUI menu.'
