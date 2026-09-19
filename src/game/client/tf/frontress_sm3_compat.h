// Team Frontress: fatal Shader Model 3.0 diagnostics.
// Keep the UTF-8 BOM: MSVC must decode Russian wide-string literals as UTF-8.
// No runtime bypass: basic world/model shaders require SM3 programs.
#ifndef FRONTRESS_SM3_COMPAT_H
#define FRONTRESS_SM3_COMPAT_H

#include "materialsystem/imaterialsystem.h"
#include "materialsystem/imaterialsystemhardwareconfig.h"

#if defined( _WIN32 ) && !defined( _X360 )
// Avoid pulling the Windows registry/multimedia convenience headers into
// clientmode_tf.cpp; Source declares several APIs with the same names.
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#define FRONTRESS_DEFINED_WIN32_LEAN_AND_MEAN
#endif
#include <windows.h>
#ifdef FRONTRESS_DEFINED_WIN32_LEAN_AND_MEAN
#undef WIN32_LEAN_AND_MEAN
#undef FRONTRESS_DEFINED_WIN32_LEAN_AND_MEAN
#endif
// Windows maps these unqualified names to CreateEventA/PlaySoundA, which
// corrupts IGameEventManager2::CreateEvent and vgui::ISurface::PlaySound.
#ifdef CreateEvent
#undef CreateEvent
#endif
#ifdef PlaySound
#undef PlaySound
#endif
#include <string>

namespace frontress_sm3
{
inline std::wstring ToWide( const char *text )
{
    if ( !text || !*text )
        return L"Unknown";

    UINT codepage = CP_UTF8;
    int length = MultiByteToWideChar( codepage, MB_ERR_INVALID_CHARS, text, -1, NULL, 0 );
    if ( length <= 0 )
    {
        codepage = CP_ACP;
        length = MultiByteToWideChar( codepage, 0, text, -1, NULL, 0 );
    }
    if ( length <= 0 )
        return L"Unknown";

    std::wstring result( length, L'\0' );
    MultiByteToWideChar( codepage, codepage == CP_UTF8 ? MB_ERR_INVALID_CHARS : 0,
                         text, -1, &result[0], length );
    result.resize( length - 1 );
    return result;
}

inline std::wstring DetectDisplayName( const MaterialAdapterInfo_t &adapter )
{
    std::wstring name = ToWide( adapter.m_pDriverName );
    if ( !adapter.m_VendorID || !adapter.m_DeviceID )
        return name;

    wchar_t vendor[16], deviceId[16];
    swprintf_s( vendor, ARRAYSIZE( vendor ), L"VEN_%04X", adapter.m_VendorID );
    swprintf_s( deviceId, ARRAYSIZE( deviceId ), L"DEV_%04X", adapter.m_DeviceID );

    DISPLAY_DEVICEW display = {};
    display.cb = sizeof( display );
    for ( DWORD index = 0; EnumDisplayDevicesW( NULL, index, &display, 0 ); ++index )
    {
        if ( wcsstr( display.DeviceID, vendor ) && wcsstr( display.DeviceID, deviceId ) &&
             display.DeviceString[0] )
            return display.DeviceString;
        display = DISPLAY_DEVICEW();
        display.cb = sizeof( display );
    }
    return name;
}
} // namespace frontress_sm3
#endif

inline void Frontress_HandleMissingShaderModel3( IMaterialSystem *system,
                                                 IMaterialSystemHardwareConfig *hardware )
{
    if ( !hardware || hardware->SupportsShaderModel_3_0() )
        return;

    const int activeDx = hardware->GetDXSupportLevel();
    const int maxDx = hardware->GetMaxDXSupportLevel();
    const char *backend = GetRenderBackendName( DetermineRenderBackend() );
    MaterialAdapterInfo_t adapter = {};
    int adapterIndex = -1;
    if ( system && system->GetDisplayAdapterCount() > 0 )
    {
        adapterIndex = system->GetCurrentAdapter();
        if ( adapterIndex >= 0 && adapterIndex < system->GetDisplayAdapterCount() )
            system->GetDisplayAdapterInfo( adapterIndex, adapter );
    }

    Warning( "Team Frontress: SM3 requirement failed; adapter=%d driver=%s, "
             "VEN=%04X DEV=%04X, dx=%d, max_dx=%d, adapter_max_dx=%d, renderer=%s\n",
             adapterIndex, adapter.m_pDriverName, adapter.m_VendorID, adapter.m_DeviceID,
             activeDx, maxDx, adapter.m_nMaxDXSupportLevel, backend );

#if defined( _WIN32 ) && !defined( _X360 )
    const bool russian = PRIMARYLANGID( GetUserDefaultUILanguage() ) == LANG_RUSSIAN;
    const std::wstring gpu = adapterIndex >= 0
        ? frontress_sm3::DetectDisplayName( adapter ) : L"Unknown";
    wchar_t details[512];
    _snwprintf_s( details, ARRAYSIZE( details ), _TRUNCATE,
                russian ? L"\n\nВидеокарта: %ls\nДрайвер: %ls\nID: %04X:%04X\nАдаптер: %d\nРендерер: %ls\nDX: %d; максимум: %d (адаптер: %d)\nSM3: не обнаружен"
                        : L"\n\nGraphics adapter: %ls\nDriver: %ls\nID: %04X:%04X\nAdapter: %d\nRenderer: %ls\nDX: %d; maximum: %d (adapter: %d)\nSM3: not detected",
                gpu.c_str(), frontress_sm3::ToWide( adapter.m_pDriverName ).c_str(),
                adapter.m_VendorID, adapter.m_DeviceID, adapterIndex,
                frontress_sm3::ToWide( backend ).c_str(), activeDx, maxDx,
                adapter.m_nMaxDXSupportLevel );

    const wchar_t *title = russian ? L"TEAM FRONTRESS — несовместимая графика"
                                   : L"TEAM FRONTRESS — graphics compatibility";
    std::wstring message = russian
        ? L"Движок не обнаружил Shader Model 3.0. Он необходим для базовых шейдеров мира и персонажей."
        : L"The engine did not detect Shader Model 3.0. It is required by the core world and character shaders.";
    message += details;
    message += russian
        ? L"\n\nЗапуск остановлен, чтобы избежать повреждённой графики и вылета."
          L" Проверьте драйвер и выбранную видеокарту. Если видеокарта не поддерживает SM3 аппаратно,"
          L" смена настроек графики не добавит эту возможность.\n\nНажмите ОК для выхода."
        : L"\n\nStartup has been stopped to avoid broken rendering and crashes."
          L" Check the graphics driver and selected GPU. If the GPU lacks SM3 hardware support,"
          L" graphics settings cannot add it.\n\nPress OK to exit.";
    MessageBoxW( NULL, message.c_str(), title, MB_OK | MB_ICONERROR | MB_SYSTEMMODAL );
    ExitProcess( 1 );
#else
    Error( "Team Frontress requires Shader Model 3.0. GPU driver: %s; VEN=%04X DEV=%04X; "
           "DX=%d max=%d adapter_max=%d backend=%s",
           adapter.m_pDriverName, adapter.m_VendorID, adapter.m_DeviceID,
           activeDx, maxDx, adapter.m_nMaxDXSupportLevel, backend );
#endif
}

#endif // FRONTRESS_SM3_COMPAT_H
