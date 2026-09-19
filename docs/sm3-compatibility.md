# Shader Model 3.0 diagnostics and legacy-hardware research

## Current behavior: fail closed

Team Frontress needs SM3 for ordinary model materials (`src/materialsystem/stdshaders/vertexlitgeneric_dx9_helper.cpp`, `vertexlit_and_unlit_generic_{vs30,ps30}.inc` and bump variants) and world materials (`src/materialsystem/stdshaders/lightmappedgeneric_dx9_helper.cpp`, `lightmappedgeneric_{vs30,ps30}.inc`). This is not an optional shader graph. Removing `Error()` or turning off shadows does not create SM2 variants.

When `SupportsShaderModel_3_0()` is false, Windows reports the active material-system adapter, Windows GPU display description (when matched), driver, vendor/device IDs, active and maximum DX levels, renderer and failed capability check. The diagnostic is also logged. The dialogue uses the Windows user UI language (Russian or English), shows **OK only**, and exits. Non-Windows keeps a fatal diagnostic. A failure before client initialization can prevent the dialog from appearing at all.

`DetermineRenderBackend()` in `src/public/materialsystem/imaterialsystem.h` defaults to **Direct3D 9 on Windows**, Vulkan elsewhere; `-vulkan` explicitly chooses Vulkan. This is distinct from shipping DXVK. `ClientModeTFNormal::Init()` later sets `mat_dxlevel` to 100. `-dxlevel 95` does not manufacture SM3 capabilities.

## Translation versus feature emulation

Translation D3D9 -> OpenGL/Vulkan/Metal alone does not add GPU hardware features. It *can* expose SM3 to a D3D9 app when the alternative API/driver really implements the necessary shader features, or when the renderer executes the shaders on CPU. Every option requires an actual device-capabilities and render smoke test: translator startup and first map may fail before the SM3 diagnostic code is reached.

| Candidate | What it does | Applicability and limitation |
| --- | --- | --- |
| **SwiftShader `legacy-d3d9`** | Native Direct3D 9 DLL implemented on CPU; old source defaults to D3D pixel/vertex shader version 3.0. | Most direct *experimental Windows CPU-rendering* prototype. Legacy/unmaintained D3D9 branch; verify x64 DLL, initialization, SM3 caps, all Source extension usage, frame rate and redistribution. Extremely low FPS possible. Never auto-bundle without testing. |
| **WineD3D + Mesa llvmpipe** | Translates D3D9 to OpenGL; llvmpipe runs OpenGL shaders on CPU. | Good concept for experimentation, notably under Wine on Linux. Native Windows drop-in WineD3D bridges exist but may be unofficial and are not guaranteed to support this Source renderer. On Windows, Mesa's OpenGL DLL and matching dependencies must be architecture-correct; test in isolated game directory, not system directories. |
| **DXVK current** | D3D9 -> Vulkan translation. | Requires Vulkan 1.3 *plus* required extensions; not a way to run 2000s GPUs with no Vulkan. Can sometimes help old D3D9 driver issues on otherwise Vulkan-capable GPUs, not physical SM3 deficiencies. The game's existing `-vulkan` is another Vulkan route, not DXVK itself. |
| **DXVK 1.10.3** | Legacy D3D9 -> Vulkan translation. | Supports Vulkan 1.1 rather than current 1.3, useful only for a GPU with appropriate Vulkan support. No benefit when there is no Vulkan implementation. |
| **DXVK + lavapipe / SwiftShader Vulkan** | D3D9 -> Vulkan -> CPU software driver. | Research-only multi-layer option; Vulkan extension/feature compatibility and 32/64-bit ICD setup must be verified, and performance likely poor. Direct D3D9 SwiftShader is architecturally simpler for CPU testing. |
| **D3D9On12 / WARP** | Windows D3D9-to-D3D12 OS component / Microsoft's CPU rasterizer. | D3D9On12 ordinarily needs a working D3D12 path; separate forcing wrappers typically require a D3D12-compatible GPU. WARP can execute D3D9 on systems using Microsoft Basic Display Adapter, but Microsoft's docs say D3D9 cannot generally select WARP as a normal app-level option while an ordinary GPU driver is present. Test only rather than promising a universal toggle. |
| **DXMT** | D3D10/11 -> Metal for macOS/Wine. | Wrong API and platform for Windows D3D9 Team Frontress; not a substitute for SM3 support. |
| **Gallium Nine** | Mesa D3D9 frontend used with Wine/Linux. | Linux/Wine testing option, not a Windows drop-in; any selected driver still has to implement the shader features. |
| **Real SM2 engine path** | Add `vs20` / `ps20` shaders and proper fallback/material selection. | Only route to actual GPU execution on chips that lack SM3 and also lack usable Vulkan/OpenGL functionality. This is engine/rendering work, not a DLL swap; the world and model materials both need it. |

References (upstream/project documentation):

- SwiftShader legacy D3D9: https://swiftshader.googlesource.com/SwiftShader/+/refs/heads/legacy-d3d9 ; old implementation reports PS/VS 3.0 by default: https://android.googlesource.com/platform/external/swiftshader/+/fc065596cd8ecb9357a46d03cfdbaa42fe044c9f/src/D3D9/Direct3D9.cpp ; upstream D3D9 deprecation: https://groups.google.com/g/swiftshader/c/ode-pF6ROsk
- DXVK support: https://github.com/doitsujin/dxvk/wiki/Driver-support and https://github.com/doitsujin/dxvk
- Mesa llvmpipe Windows deployment: https://docs.mesa3d.org/drivers/llvmpipe.html ; Windows distribution packaging: https://github.com/pal1000/mesa-dist-win
- WineD3DBridge experimental Windows project: https://github.com/julianyuyu/WineD3DBridge
- D3D9On12: https://github.com/microsoft/D3D9On12 ; Microsoft WARP limits: https://learn.microsoft.com/en-us/windows/win32/direct3darticles/directx-warp ; example D3D9On12 forcing requirements: https://github.com/narzoul/ForceD3D9On12
- DXMT scope: https://github.com/3Shain/dxmt ; Gallium Nine docs: https://docs.mesa3d.org/gallium-nine.html

## Proposed experiments (NOT implemented in this PR)

1. Obtain the exact GPU model, operating system, Direct3D PS/VS caps, OpenGL and Vulkan driver support, process architecture, and Source logs. Distinguish a missing feature from a wrong selected adapter/driver.
2. Build the legacy SwiftShader D3D9 DLL in an isolated test installation; verify all relevant 64-bit/32-bit dependencies, PS/VS 3.0 caps and whether the engine starts at all. Capture video/FPS on a representative CPU; check licensing before distribution.
3. Independently test WineD3D + Mesa llvmpipe, first with a standalone D3D9 shader test, then Team Frontress; on Linux additionally test Gallium Nine.
4. On Vulkan-capable but driver-problematic machines, compare native `-dx9`, the engine's own `-vulkan`, and separately DXVK. Do not combine different DLL overrides into one run.
5. Test menu, map load, character models, transparent materials, lighting, particles, water, UI and a match. Log exact device caps and failure point. If no CPU pathway passes, design a genuine SM2 shader/material fallback as a separate project.

## Shipping gate

No continue-startup button, no experimental mode flag, and no modified persistent graphics variables. Keep draft PR open until a full Windows x64 client build, Windows Russian/English dialog check, non-Windows compile check, and real-hardware tests pass. This PR improves diagnostics only and does not claim legacy-GPU compatibility.
