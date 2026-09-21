# Creates a complete loose HudLayout.res override without modifying the source or VPK.
# Run from any directory: powershell -ExecutionPolicy Bypass -File game_clean/install_loose_hud_layout.ps1
$ErrorActionPreference = 'Stop'

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$source = Join-Path $repoRoot 'game_src/tc2/pak1/scripts/hudlayout.res'
$target = Join-Path $repoRoot 'game/tc2/loose/scripts/HudLayout.res'

if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
    throw "HUD layout source not found: $source"
}

# The engine loads the entire layout, so a file containing only the new entry
# would replace all existing entries and trigger more HUD assertions.
$layout = [System.IO.File]::ReadAllText($source)
if ($layout -notmatch '(?m)^\s*"?HudMenuVoiceSelection"?\s*$') {
    $voicePanel = @'
    "HudMenuVoiceSelection"
    {
        "fieldName" "HudMenuVoiceSelection"
        "visible" "1"
        "enabled" "1"
        "xpos" "c-200"
        "ypos" "c-150"
        "wide" "400"
        "tall" "300"
    }

'@
    # Insert the new section inside the existing outer KeyValues block.
    $closingBrace = [regex]::Match($layout, '\}\s*$')
    if (-not $closingBrace.Success) {
        throw 'Cannot find the final HudLayout.res closing brace.'
    }
    $layout = $layout.Insert($closingBrace.Index, "`r`n" + $voicePanel + "`r`n")
}

$directory = Split-Path -Parent $target
New-Item -ItemType Directory -Path $directory -Force | Out-Null
[System.IO.File]::WriteAllText($target, $layout, (New-Object System.Text.UTF8Encoding($false)))
Write-Host "Loose HUD override created: $target"
Write-Host 'pak1.vpk and game_src were not changed.'