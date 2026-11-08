@echo off
pushd %~dp0
rem -enablefakeip is not passed here: it belongs in the Steam launch options,
rem which arrive in %* below. See the comment in tc2.sh for why a dedicated
rem server must never get it.
rem Easy Anti-Cheat: start through the anti-cheat bootstrapper, which loads the
rem anti-cheat and then runs tc2_win64.exe with these arguments. -noeac starts
rem the game directly (offline and modding; protected servers refuse it).
set GAME_EXE=tc2_win64.exe
if exist start_protected_game.exe set GAME_EXE=start_protected_game.exe
echo %* | find /i "-noeac" >nul && set GAME_EXE=tc2_win64.exe
start .\%GAME_EXE% -steam -particles 1 -condebug -nobreakpad -nominidumps %* +ip 0.0.0.0
popd
