# TC2 HTML settings, as data

Extracted from the built Astro bundle
(`game/tc2/loose/resource/html/_astro/SettingsView.*.js`), which is the only
copy of it in this repository -- the source project is elsewhere. This is the
list, not a design: 22 groups, 107 settings, and what each one
actually drives.

It exists so the settings can be rebuilt somewhere they are reachable. The HTML
menu is off by default (`tf_main_menu_html 0`), so today none of this is on
screen; the stock VGUI options are, and they are a flat pile with none of these
groupings.

Regenerate with `tools/dump_html_settings.py`.


## HUD

- **Fast Weapon Switch** `hud_fastswitch` — checkbox
  - If set, you can change weapons without using the weapon selection menu.
- **Enable Minimal HUD** `cl_hud_minmode` — checkbox
  - Minimal HUD mode uses a smaller, denser HUD so that you can see more. Some explanatory information is removed as well, so you should be familiar with the standard HUD before you turn on Minimal HUD.
- **HUD Aspect Ratio** `cl_hud_aspect` — one of: `1`, `2`, `3`, `4`, `0`
  - Will constrain certain elements of the HUD to fit to the center of the screen based upon the aspect ratio chosen.
- **Hide HUD During Freezecam Screenshots** `hud_freezecamhide` — checkbox
  - If set, the HUD will be hidden during freezecam screenshots.
- **Disable Floating Health Bar** `tf_hud_target_id_disable_floating_health` — checkbox
  - If set, removes the TargetID HealthBar floating over the character's head.
- **TargetID Translucency** `tf_hud_target_id_alpha` — slider 0..255 step 1
  - Set the translucency of the TargetID name plate
- **Mouse Input on the Scoreboard** `tf_scoreboard_mouse_mode` — one of: `0`, `2`, `1`
  - Control mouse input behavior when displaying the scoreboard. While mouse interaction is active, your camera will be locked until the scoreboard is closed.
- **Display Ping Values as Text on the Scoreboard** `tf_scoreboard_ping_as_text` — checkbox
  - If set, ping values on the scoreboard will be displayed as numbers instead of colored bars.
- **Display Alternate Class Icons on the Scoreboard** `tf_scoreboard_alt_class_icons` — checkbox
  - If set, alternate class icons will be displayed on the scoreboard.
- **Use the Team Status Display in the HUD** `tf_use_match_hud` — checkbox
  - If set, you will see information about both teams at the top of the HUD.
- **Show Pop-up Alerts While In-Game** `cl_notifications_show_ingame` — checkbox
  - If set, you'll receive in-game notification popups for various events, like trade requests. If unset, these notifications will only be visible when you return to the main menu.
- **Featured Class on Main Menu** `tf_mainmenu_class_highlight` — one of: `0`, `1`, `2`, `3`, `4`, `5`, `6`, `7`, `8`, `9`, `-1`
  - Choose which class is featured on the main menu.
- **Chat History Lines** `hud_chat_history_lines` — slider 10..100 step 1
  - The number of lines to keep in the chat history. Setting this higher will allow you to scroll back further to see previous messages, but may cause performance issues on some computers.
- **Enable Chat Notification Sound** `cl_hud_chat_notification` — checkbox
  - If set, you'll hear a sound whenever a player sends a new message in game chat.

## Hitsounds

- **Hitsounds** `` — header
- **Enable Hitsounds** `tf_dingalingaling` — checkbox
  - If set, you'll hear a 'hit sound' that's played whenever you damage an enemy.
- **Hitsound Volume** `tf_dingaling_volume` — slider 0..1 step 0.01
  - The volume for 'hit sounds'.
- **Hitsound Type** `tf_dingalingaling_effect` — one of: `6`, `0`, `1`, `2`, `3`, `4`, `5`, `7`, `8`, `0`
  - Select the audio effect to play when damaging enemies.
- **Hitsound Pitch Min Damage** `tf_dingaling_pitchmindmg` — slider 1..255 step 0.01
  - Hit sound pitch for attacks that deal 10 damage or less. Pitch scales between high and low values. (Recommended: 155)
- **Hitsound Pitch Max Damage** `tf_dingaling_pitchmaxdmg` — slider 1..255 step 0.01
  - Hit sound pitch for attacks that deal 150 damage or more. Pitch scales between high and low values. (Recommended: 50)
- **Killsounds** `` — header
- **Enable Killsounds** `tf_dingalingaling_lasthit` — checkbox
  - If set, you'll hear a last hit sound when one of your attacks kills an enemy.
- **Killsound Volume** `tf_dingaling_lasthit_volume` — slider 0..1 step 0.01
  - The volume for last hit sounds.
- **Killsound Type** `tf_dingalingaling_last_effect` — one of: `6`, `0`, `1`, `2`, `3`, `4`, `5`, `7`, `8`, `0`
  - Select the audio effect to play when killing an enemy.
- **Killsound Pitch Min Damage** `tf_dingaling_lasthit_pitchmindmg` — slider 1..255 step 0.01
  - Kill sound pitch for last hits that deal 10 damage or less. Pitch scales between high and low values. (Recommended: 155)
- **Killsound Pitch Max Damage** `tf_dingaling_lasthit_pitchmaxdmg` — slider 1..255 step 0.01
  - Kill sound pitch for last hits that deal 150 damage or more. Pitch scales between high and low values. (Recommended: 50)

## Damage

- **Enable Hitmarker** `cl_hitmarker` — checkbox
  - Display a hitmarker on your crosshair upon dealing damage or killing an enemy.
- **Enable Damage Numbers** `hud_combattext` — checkbox
  - If set, you'll see damage amounts appear over the heads of enemies whenever you damage them.
- **Combine Damage Numbers** `hud_combattext_batching` — one of: `0`, `2`, `1`
  - Controls how damage numbers are combined when multiple hits are made in quick succession.
- **Damage Number Combine Time** `hud_combattext_batching_window` — slider 0.1..1 step 0.05
  - The time window (in seconds) during which multiple damage numbers will be combined into a single number.
- **Large Damage Numbers** `hud_combattext_large` — checkbox
  - Whether to always increase the size of the damage numbers, or just upon a critical hit.
- **Damage Number Red** `hud_combattext_red` — slider 0..255 step 1
  - Change the color of text that appears over your target's head.
- **Damage Number Green** `hud_combattext_green` — slider 0..255 step 1
  - Change the color of text that appears over your target's head.
- **Damage Number Blue** `hud_combattext_blue` — slider 0..255 step 1
  - Change the color of text that appears over your target's head.
- **Enable Healing Numbers** `hud_combattext_healing` — checkbox
  - Shows health restored over heal targets similarly to damage numbers.

## Crosshairs

- **Crosshair Style** `cl_crosshair_file` — one of: ``, `crosshair1`, `crosshair2`, `crosshair3`, `crosshair4`, `crosshair5`, `crosshair6`, `crosshair7`, `default`
  - Select the style of the in-game crosshair.
- **Crosshair Scale** `cl_crosshair_scale` — slider 16..48 step 1
  - Adjust the scale/size of the crosshair.
- **Crosshair Red** `cl_crosshair_red` — slider 0..255 step 1
  - Adjust the red color channel of the crosshair.
- **Crosshair Green** `cl_crosshair_green` — slider 0..255 step 1
  - Adjust the green color channel of the crosshair.
- **Crosshair Blue** `cl_crosshair_blue` — slider 0..255 step 1
  - Adjust the blue color channel of the crosshair.
- **Crosshair Gap** `cl_crosshair_gap` — slider 0..20 step 1
  - Adjust the gap size between crosshair elements.

## Viewmodels

- **Viewmodel Field of View** `viewmodel_fov` — slider 54..70 step 1
  - The Field-of-View to use when drawing the first person view of your active weapon. Larger values will make the weapon smaller onscreen.
- **Flip Viewmodels** `cl_flipviewmodels` — checkbox
  - If set, the first person view of your active weapon will be drawn on the left hand side of the screen, instead of the right.
- **Use Minimized Viewmodels** `tf_use_min_viewmodels` — checkbox
  - If set, the first person view of your active weapon will be drawn using minimal screen space.

## General

- **Auto-Kill After Choosing a Player Class** `hud_classautokill` — checkbox
  - If set, then you'll immediately die whenever you change class while out in the field. If unset, you'll change to your new class the next time you respawn or touch a resupply cabinet.
- **Auto-Respawn After Loadout Changes in Respawn Rooms** `tf_respawn_on_loadoutchanges` — checkbox
  - If set, then you'll respawn immediately whenever you change your loadout while inside a respawn zone. If unset, your loadout changes will take effect the next time you respawn or touch a resupply cabinet.
- **Remember the Active Weapon Between Lives** `tf_remember_activeweapon` — checkbox
  - If set, you'll respawn holding the same weapon you were holding when you died (assuming you still have it equipped in your loadout).
- **Remember the 'Previous Weapon' Between Lives** `tf_remember_lastswitched` — checkbox
  - If set, respawning won't affect the weapon you'll switch to when you hit your 'previous weapon' key. If unset, your 'previous weapon' will always be set to be your secondary weapon when you respawn.

## Outlines

- **Enable Outlines** `glow_outline_effect_enable` — checkbox
  - If set, glow effects will be enabled during the match for objectives like Payload carts, CTF intelligence briefcases, and teammates after respawn.
- **Enable teammate glow effects after respawn** `tf_enable_glows_after_respawn` — checkbox
  - If set, you will see outlines of teammates for a short time after respawning.
- **Disable outlines while spectating** `tf_spec_xray_disable` — checkbox
  - If set, outlines of teammates will be disabled while you are dead or spectating.

## Spectating

- **Show Non-Standard Items on Spectated Player** `cl_spec_carrieditems` — checkbox
  - If set, you'll be shown the loadout items being used by the player you're spectating.
- **Use Advanced Spectator HUD in Tournament Mode** `cl_use_tournament_specgui` — checkbox
  - The Advanced Spectator HUD is used in tournament mode only, and is designed to show you more information in 6 v 6 matches.
- **Spectator TargetID Location** `tf_spectator_target_location` — one of: `0`, `1`, `2`, `3`
  - Select the screen location for the target health and nameplate when spectating.
- **When Spectating, View World From the Eyes of a Pyro** `tf_spectate_pyrovision` — checkbox
  - If set, world will be viewed under Pyrovision when spectating

## Recording

- **Recording Mode** `ds_enable` — one of: `0`, `1`, `2`, `3`, `4`
  - Control when gameplay demos are automatically recorded.
- **Play Sounds For Start/Stop Events** `ds_sound` — checkbox
  - If set, sounds will play when demo recording starts or stops
- **Log Events** `ds_log` — checkbox
  - If set, all events are logged to the general _events.txt file and to a specific .json file that contains the events for each associated .dem file
- **Location of the In-Game Notifications** `ds_notify` — one of: `0`, `1`, `2`
  - Choose where to display notifications when demo recording starts or stops.
- **Auto-Save a Scoreboard Screenshot at the End of a Match** `ds_screens` — checkbox
  - Auto-save a screenshot of the scoreboard at the end of a match.
- **Min Killstreak Count** `ds_min_streak` — slider 0..30 step 1
  - This is the minimum killstreak count before the killstreaks are logged. 0 to disable killstreak logging.
- **Max Time Between Kills** `ds_kill_delay` — slider 5..1000 step 1
  - This is the maximum time between kills before the killstreak count is reset to zero.
- **Auto-Delete Recordings** `ds_autodelete` — checkbox
  - Auto-delete recordings with no associated bookmark or kill streak events

## Networking

- **Network Quality** `sg_net` — one of: `0`, `1`, `2`
  - Select how the game should account for your network quality. Lower settings may increase latency but improve overall stability.
- **Bandwidth** `rate` — one of: `16000`, `80000`, `131072`, `196608`, `262144`, `327680`, `393216`, `524288`, `786432`
  - Select the bandwidth setting that best matches your internet download speed.
- **Custom Files** `cl_downloadfilter` — one of: `all`, `nosounds`, `mapsonly`, `none`
  - When a game server tries to download custom content to your computer, you can control what files to allow to download.
- **Show Network Information** `net_graph` — one of: `0`, `1`, `4`
  - If set, network performance information such as ping and packet loss will be displayed on the HUD.

## Display

- **Display Mode** `videomode_mode` — one of: `0`, `1`, `2`
  - Select the windowed or fullscreen mode for the display.
- **Display Size** `videomode_size` — one of: `0`
  - Select the screen resolution / display size.
- **Apply Display Settings** `videomode` — button
  - Apply changes made to the display mode or resolution.
- **V-Sync** `mat_vsync` — checkbox
  - Synchronize the game's frame rate with the monitor's refresh rate to prevent screen tearing.
- **Maximum frames per second allowed** `game_fps_max` — slider 60..1000 step 1
  - Set the maximum frames per second (FPS) limit during gameplay.
- **Main menu maximum frames per second allowed** `ui_fps_max` — slider 60..240 step None
  - Set the maximum frames per second (FPS) limit while in the main menu.

## Graphics

- **Brightness** `mat_monitorgamma` — slider 1.6..2.6 step 0.05
  - Adjust the monitor gamma/brightness level. Only works in exclusive fullscreen.
- **Field of View (FOV)** `fov_desired` — slider 75..90 step None
  - The field of view of your camera. Higher values allow you to see more of the world, but feel further away.
- **Motion Blur** `mat_motion_blur_enabled` — checkbox
  - Enable motion blur when looking or moving around quickly.
- **Show First Person Bullet Tracers** `r_drawtracers_firstperson` — checkbox
  - Draw bullet tracers in first person view.
- **Low Violence Mode** `violence_hblood` — checkbox
  - Disable blood and gibs (low violence mode).

## Graphics Quality

- **Shadows** `sg_shadows` — one of: `0`, `1`, `2`, `3`, `4`
  - Set the quality and complexity of rendering shadows.
- **Shader quality** `sg_shaders` — one of: `0`, `1`, `2`, `3`
  - Set the rendering shader quality.
- **Textures** `mat_picmip` — one of: `2`, `1`, `0`, `-1`
  - Set the texture quality level.
- **Model Quality** `sg_models` — one of: `0`, `1`, `2`, `3`
  - Set the model detail quality based on distance.
- **Effects** `sg_effects` — one of: `0`, `1`, `2`, `3`
  - Set the visual effects and particle systems quality.
- **Reflections** `sg_reflections` — one of: `0`, `1`, `2`, `3`
  - Set the reflections rendering quality.
- **Post Processing** `sg_postprocess` — one of: `0`, `1`, `2`
  - Set the post-processing effects quality.
- **Anti-Aliasing** `mat_antialias` — one of: `1`, `2`, `4`, `8`
  - Set the anti-aliasing level to smooth jagged edges.
- **Texture Filtering** `mat_forceaniso` — one of: `1`, `2`, `4`, `8`, `16`
  - Set the anisotropic texture filtering level for sharper textures at oblique angles.

## Advanced

- **Show FPS** `cl_showfps` — checkbox
  - Display current frames per second (FPS) on the screen.

## Volume

- **Main Volume** `volume` — slider 0..1 step 0.01
  - The main volume of the game. All other volume settings are relative to this.
- **Music Volume** `snd_musicvolume` — slider 0..1 step 0.01
  - The volume of music on the main menu and in-game.
- **Voice Receive Volume** `voice_scale` — slider 0..1 step 0.01
  - Adjust the volume of other players' voices.

## Output

- **Speaker Configuration** `snd_surround_speakers` — one of: `0`, `2`, `4`, `5`, `6`
  - Select the speaker layout configuration.

## Options

- **Sound Quality** `sg_sound` — one of: `0`, `1`, `2`
  - Select the sound sample quality level.
- **Enable 3D Sound** `dsp_enhance_stereo` — checkbox
  - If set, enables 3D stereo spatial effects. Recommended for headphones only.
- **Enable enhanced audio effects** `dsp_room` — checkbox
  - If set, enables enhanced audio effects based upon the size and type of the in-game environment you are standing in.
- **Play Sound in Desktop** `snd_mute_losefocus` — checkbox
  - Play sound even when the game is in the background

## Mouse

- **Mouse Sensitivity** `sensitivity` — slider 0.1..6 step 0.01
  - Adjust mouse sensitivity.
- **Zoom Sensitivity Ratio** `zoom_sensitivity_ratio` — slider 0.1..2 step 0.01
  - Adjusts the zoom sensitivity relative to your normal mouse sensitivity. For example, a value of 0.5 will halve the sensitivity while zoomed in.
- **Invert Mouse Y-Axis** `m_pitch` — checkbox
  - Invert the mouse Y-axis (up/down looking movement).
- **Enable Mouse Acceleration** `m_customaccel` — checkbox
  - Enable mouse acceleration.
- **Mouse Acceleration** `m_customaccel_exponent` — slider 1..1.4 step 0.01
  - Set the mouse acceleration exponent value.

## Keyboard

- **Edit Keybindings** `OpenOptionsDialog` — button
  - Open the game's legacy keybind editing options.

## Advanced

- **Enable Developer Console** `con_enable` — checkbox
  - Enable access to the developer console.

## Social

- **Enable text chat** `cl_enable_text_chat` — checkbox
  - If set, allow in-game text communication
- **Text Filter Settings** `open_chat_filter_settings` — button
  - Open Steam chat and text filter settings

## Voice

- **Enable voice chat** `voice_modenable` — checkbox
  - If set, allow in-game voice communication
- **Voice Receive Volume** `voice_scale` — slider 0..1 step 0.01
  - Adjust the volume of other players' voices.
