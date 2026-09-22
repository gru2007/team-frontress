@echo off
pushd %~dp0
rem -enablefakeip is not passed here: it belongs in the Steam launch options,
rem which arrive in %* below. See the comment in tc2.sh for why a dedicated
rem server must never get it.
start .\tc2_win64.exe -steam -particles 1 -condebug -nobreakpad -nominidumps %* +ip 0.0.0.0
popd
