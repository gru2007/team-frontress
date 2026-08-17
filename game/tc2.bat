@echo off
pushd %~dp0
rem -enablefakeip: the engine only asks Steam for a FakeIP when this is set, and
rem a Greyline host is unreachable from outside without one. Harmless otherwise.
start .\tc2_win64.exe -steam -particles 1 -condebug -nobreakpad -nominidumps -enablefakeip %* +ip 127.0.0.1
popd
