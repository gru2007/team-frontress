# Steamworks bridge for the macOS port

The Windows client runs inside Wine. The Steam client the player is logged into
is a native macOS process. This bridge is what connects them.

It replaces `steam_api64.dll` with a Wine builtin that forwards every call to
Valve's macOS `libsteam_api.dylib`, loaded into the same Wine process. Nothing
here contains any part of the Steamworks SDK: the library is found with
`dlopen` at runtime, from the path the launcher puts in
`TC2_NATIVE_STEAM_API`.

## Why in-process

Both halves of the bridge live in one address space, and that is the whole
reason this is tractable. A pointer the game passes in is still a valid pointer
on the Unix side, so nothing has to be marshalled, copied, or given a lifetime.
The only thing that genuinely differs across the boundary is the calling
convention — Microsoft x64 on the PE side, System V on the Unix side — and that
is handled by packing each call's arguments into a plain struct, crossing with
a single `__wine_unix_call`, and unpacking on the other side. An ABI problem
becomes a struct-layout problem, which both compilers agree on.

An out-of-process design (a native helper spoken to over a socket) was the
alternative. It fails on the same point: it would have to know the size and
direction of every buffer behind every pointer, and it would have to reverse-
proxy the interfaces the game itself implements. In-process gets both for free.

## What is generated and what is not

`generate.py` reads `src/public/steam/steam_api.json` — Valve's own
machine-readable description of the SDK — and emits everything mechanical:

| file | contents |
| --- | --- |
| `bridge_ifaces.h` | the interface id enum |
| `bridge_calls.h` | the call-code enum and one params struct per method |
| `pack_convert.h/.cpp` | packing mirrors and converters (see below) |
| `pe_thunks.cpp` | Microsoft-ABI vtable thunks, and the vtables |
| `pe_flat.cpp` | the flat `SteamAPI_ISteamX_Y` exports |
| `pe_local.cpp` | exports that never leave the PE side |
| `unix_thunks.cpp` | System V dispatch, one entry per call code |
| `params_list.h` | the struct list `verify-layout.sh` measures |
| `exports.def` | the DLL export table |

909 methods across 35 interfaces. Hand-written code is confined to the parts
that are genuinely irregular, in `src/`: the lifecycle and callback registry
(`pe_main.cpp`), the Unix-side core and the reverse proxies (`unix_main.cpp`),
and two packing converters for structs with private members
(`pack_manual.cpp`).

## Callbacks

The obvious implementation registers the game's `CCallbackBase` objects with
the native library. It cannot work: those objects have Microsoft-ABI vtables,
and the native library would call them System V.

So the bridge does not register them at all. It drives the SDK's **manual
dispatch** (`SteamAPI_ManualDispatch_*`), which delivers callbacks as plain
data, and runs the registry itself on the PE side. No vtable ever changes
hands, and `SteamAPI_RegisterCallback` / `SteamAPI_RunCallbacks` behave exactly
as the game expects.

Four interfaces do travel the other way — the server-browser response objects
the game implements — plus a handful of function-pointer callbacks. Those are
reverse-proxied by hand in `unix_main.cpp`, where a System V object reads the
PE object's vtable and calls through it with `__attribute__((ms_abi))`.

## The packing split

This is the part that makes a naive bridge quietly wrong. `steamclientpublic.h`
selects `#pragma pack(4)` on macOS and `#pragma pack(8)` on Windows, so the
same SDK struct has two different layouts. A `UserStatsReceived_t` the native
client fills in is *not* the `UserStatsReceived_t` the Windows game reads: 107
of the 243 structs the SDK describes have different field offsets, sizes, or
both.

Every struct therefore gets a pack-4 mirror (`u_X`) and a pair of field-by-
field converters, all compiled on the PE side where the real SDK type has the
Windows layout. Conversion happens at three edges: manual-dispatch callbacks,
call results, and any method parameter or return value that is one of these
structs.

A pack-8 mirror (`w_X`) is generated too, purely so the compiler can assert
that `steam_api.json` still describes the headers this is built against — size
and every public field offset. Nothing uses it at runtime, and if Valve ever
ships a JSON that disagrees with its own headers the build stops instead of
corrupting data.

## Building

```bash
./tools/macos-port/steam-bridge/build.sh
```

Needs `mingw-w64` (`brew install mingw-w64`) and Xcode's clang. Notably it does
**not** need a Wine source tree or `winebuild`.

Output is `dist/x86_64-windows/steam_api64.dll` and
`dist/x86_64-unix/steam_api64.so`.

The build fails if the DLL does not export every name the game's import library
references (checked against `src/lib/public/x64/steam_api64.lib`), or if it
imports a DLL Wine does not ship.

### What Wine requires of a builtin

Four things, each of which produces a different and fairly opaque failure when
it is missing. All four are verified working against Wine 11.16.

**The marker, with its NUL.** Wine decides a module is a builtin by comparing
`sizeof("Wine builtin DLL")` bytes at offset `0x40` in the DOS stub — seventeen
bytes, because the C expression includes the terminating NUL. mingw-w64 leaves
the rest of "This program cannot be run in DOS mode" sitting at `0x50`, so a
sixteen-byte stamp is rejected with `found in WINEDLLPATH but not a builtin,
ignoring` and the DLL fails to load. `mark-builtin.py` writes all seventeen and
clears the rest of the stub, matching what winebuild emits.

**No imports Wine lacks.** `-static-libgcc -static-libstdc++` still leaves a
dynamic import of `libwinpthread-1.dll`, which Wine does not have. A builtin
with an unresolvable import fails as `ERROR_MOD_NOT_FOUND`, indistinguishable
from the DLL not being there at all. The link uses plain `-static`.

**A file in `system32`.** Wine only goes looking for a builtin *after* the DLL
name has resolved to a file on the Windows side, and the prefix's `system32` is
populated when the prefix is created — before this DLL was added to the runtime.
The launcher copies the builtin into `system32` on every start; Wine then maps
the real builtin from the runtime directory and pairs it with its unixlib.

**The right way to reach the Unix side.** Wine 11 does not export
`__wine_unix_call` from ntdll. It exports `__wine_unix_call_dispatcher`, a
*variable* holding the function pointer, which is what `wine/unixlib.h` calls
through. The bridge reads that variable and falls back to `__wine_unix_call`
for builds that have it.

The handle itself comes from
`NtQueryVirtualMemory(..., MemoryWineUnixFuncs, ...)` on this module, which is
what pairs `x86_64-windows/steam_api64.dll` with
`x86_64-unix/steam_api64.so`.

## Verifying

```bash
./tools/macos-port/steam-bridge/verify-layout.sh
```

The design rests on both compilers laying out every params struct identically.
This measures all 909 of them with clang, turns the measurements into
`static_assert`s, and compiles those with mingw-w64. It is what caught the
packing split in the first place.

The probe is built for the host architecture, not forced to x86_64: everything
in these structs is a fixed-width integer, a float or a pointer, so macOS lays
them out the same way on both architectures and an Apple silicon runner does
not need Rosetta.

## Verified working

Against Wine 11.16 with the real macOS Steam client, from a cold prefix, the
bridge reaches native Steam and returns real data:

```text
steam_bridge: unix half attached, handle 208b95000
steam_bridge/unix: attached to .../Resources/steam/libsteam_api.dylib
[S_API] SteamAPI_Init(): Loaded '.../Steam/Contents/MacOS/steamclient.dylib' OK.
steam_bridge: wrapped ISteamApps 00007f89f7f77810 as 000000000003b710
SteamAPI_Init()            = 1
BIsSubscribed()            = 1
GetCurrentGameLanguage()   = english
GetAppInstallDir(440)      = 81 '/Users/.../steamapps/common/Team Fortress 2'
GetSteamID()               = 765611983179xxxxx
```

That covers every shape of call the design depends on: a bool return, a
`const char *` return, a `CSteamID` returned by value across the ABI boundary,
an out-buffer written by native Steam and read by the Windows process, and
`SteamAPI_RunCallbacks` pumping manual dispatch.

## Debugging

Set `TC2_STEAM_BRIDGE_DEBUG=1` to get a running log on stderr from both halves
— the dylib it attached to, every interface it wrapped, and every call it
declined to forward. The launcher captures stderr, so it lands in
`~/Library/Logs/Team Frontress/launcher.log`.

To check the native side on its own, before suspecting Wine at all:

```bash
./tools/macos-port/build-steamworks-probe.sh /tmp/steamworks-probe
/tmp/steamworks-probe /path/to/redistributable_bin/osx/libsteam_api.dylib
```

That calls `SteamAPI_InitSafe`, obtains `ISteamApps008`, and asks it whether app
440 is installed and where — proving the native backend independently of any
ABI conversion.

## Known gaps

Deliberate, and all in areas Source 2013 does not reach:

- **`ISteamNetworkingFakeUDPPort`** is only forward-declared in the shipped
  headers, so it has no vtable to build. Its exports are stubs.
- **`ISteamClient::GetISteamGenericInterface`** picks an interface by a runtime
  string; there is no static type to build a vtable from.
- **`SteamNetworkingMessage_t`** is owned by Steam and freed through a function
  pointer inside itself, so a converted copy would be the wrong object. It is
  passed through unconverted, which makes `ISteamNetworkingSockets` and
  `ISteamNetworkingMessages` unreliable.
- **`SteamNetworkingConfigValue_t`**, **`InputAnalogActionData_t`**,
  **`SteamInputActionEvent_t`** and the two `SteamDatagram*` structs hide a
  union that `steam_api.json` flattens to its largest member, so they get no
  packing conversion.
- **Arrays of a struct whose two layouts differ** are passed through raw. No
  method the engine calls takes one; the SDK's array parameters are all of
  types that pack identically.
- **Interface versions other than the ones in this SDK** are matched by prefix
  and bridged with this SDK's vtable, with a warning in the log.
