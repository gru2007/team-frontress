# What D9MT needs from CrossOver, on a Wine that is not CrossOver

D9MT is developed against CrossOver's Wine, and it reaches into two things
CodeWeavers add to it that the LGPL runtime this port ships does not have. Both
are small, and neither needs a patched Wine: they are provided here instead.

Build them with `./build.sh`; `build-depot.sh` does that on its own unless
`WINE_COMPAT_DIR` points at a build that already exists.

## `d9mtmetal_dll.c` → `d9mtmetal.dll`

d9mt links its `d9mtmetal.dll` against an import library generated from
CrossOver's `ntdll`, which exports `__wine_unix_call`. Upstream Wine does not
export it — there it is winecrt0's one-line wrapper around the
`__wine_unix_call_dispatcher` pointer — so the import becomes a stub and the
client aborts on the first Metal unix call, after Steam is up and before a
frame is drawn:

```text
wine: Call from 00006FFFFFF66798 to unimplemented function
      ntdll.dll.__wine_unix_call, aborting
```

The rebuild goes through the dispatcher, which every Wine exports, CrossOver's
included, and resolves both ntdll entry points with `GetProcAddress` so that
neither is an import that can fail. It is a drop-in for the package's own
`x86_64-windows/d9mtmetal.dll` and pairs with the package's unchanged
`x86_64-unix/d9mtmetal.so`.

This one belongs upstream in the d9mt build, and the copy here goes away when it
lands there.

## `macdrv_shim.m` → `libmacdrvshim.dylib`

DXMT's `winemetal.so` gets the Metal layer of a window through
`dlsym(RTLD_DEFAULT, "macdrv_functions")`, a table CrossOver's `winemac.so`
exports. Upstream's exports nothing but its two unix call tables, and its
fallback — `get_win_data`, `macdrv_view_create_metal_view` and friends as
individual symbols — finds nothing either. The result is a client that runs
perfectly and never presents:

```text
err: d9mt: Presenter: CreateMetalViewFromHWND failed
err: d9mt: Presenter: deferred surface creation after error VK_ERROR_SURFACE_LOST_KHR
```

Nothing behind that table is private to Wine, though. The window is an NSWindow
in `[NSApp windows]` carrying its `HWND` on its own `-hwnd` property, its
content view is the `WineContentView`, and `-newMetalViewWithDevice:` is the
very method upstream's own `macdrv_view_create_metal_view` calls. So this
library answers the lookup in terms of AppKit and the Objective-C runtime, and
the launcher puts it in the Wine process with `DYLD_INSERT_LIBRARIES`.

`winemetal` casts what `get_win_data` returns to CrossOver's
`struct macdrv_win_data` and reads the fourth field. Upstream's struct has since
lost the `cocoa_view`/`client_cocoa_view` pair, which would make that a wild
pointer rather than a missing symbol — so the struct handed out here is our own,
in CrossOver's layout, and Wine's is never touched.

The two class names and the one selector are the whole dependency on Wine
internals. If a future Wine renames them the shim says so in the log
(`macdrv shim: the content view of hwnd ... is ...`) instead of failing silently.

## The alternative

The other way to get all of this is to ship a Wine built from CodeWeavers'
published CrossOver sources, which are LGPL like Wine itself. That is a
supported thing to do, and it is what d9mt is tested against — but it means
building and carrying a Wine of our own, and the pieces here are what let the
port stay on an upstream build.
