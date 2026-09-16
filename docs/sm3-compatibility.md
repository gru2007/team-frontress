# Shader Model 3.0: diagnostics and experimental startup

## This is **not** an SM2 compatibility renderer

Source 1 here does not use a standalone optional "shader graph" that can simply be disabled. Two basic material families depend on Shader Model 3 programs:

- `src/materialsystem/stdshaders/vertexlitgeneric_dx9_helper.cpp` includes `vertexlit_and_unlit_generic_{vs30,ps30}.inc` and bump variants, and selects those shaders for ordinary model materials.
- `src/materialsystem/stdshaders/lightmappedgeneric_dx9_helper.cpp` includes `lightmappedgeneric_{vs30,ps30}.inc` for world materials.

Removing the original `Error()` does **not** compile missing SM2 alternatives or provide an SM2 renderer. Implementing actual SM2 support would require alternative shader code, compiled shader combinations, shader selection/fallback logic, material and map testing, and potentially changes in the graphics backend. Do not advertise it as a supported feature.

## What this branch actually changes

On Windows, if `SupportsShaderModel_3_0()` is false, the startup error now reports the active material-system adapter, human-readable GPU description when exposed by Windows, driver string, PCI vendor/device identifiers, rendering backend and DX support levels. The same details are logged as a warning. The dialog uses Russian if the current Windows user UI language's primary language is Russian and English otherwise. The built-in Yes/No buttons follow Windows localization.

An **experimental continue** option appears only when *both* the adapter and hardware configuration report maximum DX levels of at least 95. This is a conservative heuristic for a possible incorrect capability report, **not proof of SM3 support**. Selecting Yes suppresses the startup guard for that run and reduces optional bump maps, specular, shadows, particle complexity and motion blur. It does not substitute the core vs30/ps30 programs. The default answer is No. If the maximum levels are below 95, continuation is unavailable.

Potential results: missing or corrupted textures, broken lighting, black screen, a crash, or no improvement. Some adjusted console variables can persist; players should restore their graphics settings if necessary. An application that exits or crashes inside material-system startup **before** this client initialization will not reach the dialog. On other operating systems, the original fatal behavior remains, now with technical diagnostics.

The Windows rendering backend in this code defaults to Direct3D 9; Vulkan is selected explicitly with `-vulkan`. Do not assume that switching APIs or specifying `-dxlevel 95` can create unsupported hardware features. Note that the client currently sets `mat_dxlevel` to 100 later during initialization.

## QA matrix before shipping

1. Windows Russian UI, an SM3-capable card with normal detection: no dialog; normal launch.
2. Windows English UI, an SM3-capable card with normal detection: no dialog; normal launch.
3. Windows Russian UI, intentionally spoofed negative capability check on an otherwise supported GPU: accurate GPU data, Russian dialog, No exits, Yes attempts to proceed.
4. Windows English UI, same negative-check injection: corresponding English dialog and controls.
5. Real non-SM3 adapter / maximum DX below 95: no continue option; diagnostic dialog and clean exit.
6. Multiple adapters: verify the dialog identifies the active rendering adapter, not an unrelated display GPU.
7. Test actual gameplay after opting in: map and model materials, HUD, particles, water, cloaking and menus. Report GPU and rendering API in any resulting crash issue.
8. Check Windows x64 compile/link for the Win32 UI functions and both non-Windows compile paths. Run a complete packaged build and game smoke test; the Python integration test alone is not sufficient.

**Release gate:** keep this experimental behind a draft PR until the complete Windows client build and real-GPU smoke tests pass. True SM2 support is out of scope.
