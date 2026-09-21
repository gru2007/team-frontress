# Restore the stock matchmaking top bar without modifying the source resource or pak1.vpk.
# Run from the repository root:
# powershell -ExecutionPolicy Bypass -File .\game_clean\install_loose_matchmaking_dashboard.ps1
$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$source = Join-Path $repoRoot 'game_src\tc2\pak1\resource\ui\MatchMakingDashboard.res'
$target = Join-Path $repoRoot 'game\tc2\loose\resource\ui\MatchMakingDashboard.res'
$gameinfo = Join-Path $repoRoot 'game\tc2\gameinfo.txt'

if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "Matchmaking dashboard source not found: $source"
}
if (-not (Test-Path -LiteralPath $gameinfo -PathType Leaf)) {
    throw "gameinfo.txt not found: $gameinfo"
}

# VGUI-specific resource lookups need the vgui search-path ID on the loose directory.
$oldMount = 'game+mod+custom_mod |gameinfo_path|loose'
$newMount = 'game+mod+vgui+custom_mod |gameinfo_path|loose'
$info = [System.IO.File]::ReadAllText($gameinfo)
if (-not $info.Contains($newMount)) {
    if (-not $info.Contains($oldMount)) {
        throw "Expected loose SearchPaths entry not found in $gameinfo. No changes made."
    }
    Copy-Item -LiteralPath $gameinfo -Destination "$gameinfo.bak" -Force
    $info = $info.Replace($oldMount, $newMount)
    [System.IO.File]::WriteAllText($gameinfo, $info, (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "Enabled VGUI resource search in: $gameinfo"
}

# The complete resource defines TopBar with visible=0, but the C++ dashboard
# never changes TopBar visibility. Patch only that field, retaining every other control.
$layout = [System.IO.File]::ReadAllText($source)
$topBarVisible = [regex]::new('(?s)("TopBar"\s*\{.*?"visible"\s*)"0"')
if ($topBarVisible.Matches($layout).Count -ne 1) {
    throw 'Could not find exactly one invisible TopBar in the source resource. No resource was installed.'
}
$layout = $topBarVisible.Replace($layout, '${1}"1"', 1)

$directory = Split-Path -Parent $target
New-Item -ItemType Directory -Path $directory -Force | Out-Null
if (Test-Path -LiteralPath $target -PathType Leaf) {
    Copy-Item -LiteralPath $target -Destination "$target.bak" -Force
    Write-Host "Backed up prior override to: $target.bak"
}
[System.IO.File]::WriteAllText($target, $layout, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "Installed full dashboard resource with TopBar visible: $target"
Write-Host 'Restart the game to load the resource. This fixes only the hidden top bar, not GC connectivity.'