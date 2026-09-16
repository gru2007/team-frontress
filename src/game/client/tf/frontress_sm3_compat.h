// Team Frontress shader capability diagnostics.
// IMPORTANT: This is NOT a Shader Model 2 renderer. The world and player
// shaders still require SM3. A bypass is offered only if the adapter reports
// a DX9.0c-class maximum but the engine's current capability check disagrees.
#ifndef FRONTRESS_SM3_COMPAT_H
#define FRONTRESS_SM3_COMPAT_H

#include "materialsystem/imaterialsystem.h"
#include "materialsystem/imaterialsystemhardwareconfig.h"
#include "convar.h"

#if defined( _WIN32 ) && !defined( _X360 )
#include <windows.h>
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

inline void SetOptionalSetting( const char *name, int value )
{
    ConVarRef setting( name );
    if ( setting.IsValid() )
        setting.SetValue( value );
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

    // A DX level is NOT direct proof of PS/VS 3 support. This gate merely
    // avoids offering a plainly impossible bypass to DX8/SM2 adapters.
    const bool canTry = adapterIndex >= 0 &&
        adapter.m_nMaxDXSupportLevel >= 95 && maxDx >= 95;

    Warning( "Team Frontress: SM3 detection failed; adapter=%d driver=%s, "
             "VEN=%04X DEV=%04X, dx=%d, max_dx=%d, adapter_max_dx=%d, "
             "renderer=%s, bypass_offered=%d\n",
             adapterIndex, adapter.m_pDriverName, adapter.m_VendorID, adapter.m_DeviceID,
             activeDx, maxDx, adapter.m_nMaxDXSupportLevel, backend, canTry ? 1 : 0 );

#if defined( _WIN32 ) && !defined( _X360 )
    const bool russian = PRIMARYLANGID( GetUserDefaultUILanguage() ) == LANG_RUSSIAN;
    std::wstring gpu = frontress_sm3::DetectDisplayName( adapter );
    wchar_t details[512];
    swprintf_s( details, ARRAYSIZE( details ),
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

    if ( canTry )
    {
        message += russian
            ? L"\n\nМаксимальный DX-уровень адаптера указывает, что возможна ошибка определения возможностей."
              L"\n\nДА — продолжить запуск в ЭКСПЕРИМЕНТАЛЬНОМ режиме с уменьшенными эффектами."
              L"\nНЕТ — выйти (выбрано по умолчанию)."
              L"\n\nЭто не поддержка SM2: возможны отсутствующие текстуры, артефакты, чёрный экран и вылет."
              L" Настройки могут сохраниться; восстановите их вручную при необходимости."
            : L"\n\nThe adapter's maximum DX level suggests a possible capability-detection error."
              L"\n\nYES — continue in EXPERIMENTAL mode with reduced effects."
              L"\nNO — exit (default)."
              L"\n\nThis is not SM2 support: missing textures, artifacts, a black screen or a crash are possible."
              L" Settings may persist; restore them manually if necessary.";
        const int answer = MessageBoxW( NULL, message.c_str(), title,
                                       MB_YESNO | MB_ICONWARNING | MB_DEFBUTTON2 | MB_SYSTEMMODAL );
        if ( answer == IDYES )
        {
            // Reduce some optional effects only. These settings CANNOT replace
            // the SM3 shader binaries used by the basic scene renderer.
            frontress_sm3::SetOptionalSetting( "mat_motion_blur_enabled", 0 );
            frontress_sm3::SetOptionalSetting( "mat_specular", 0 );
            frontress_sm3::SetOptionalSetting( "mat_bumpmap", 0 );
            frontress_sm3::SetOptionalSetting( "mat_reduceparticles", 1 );
            frontress_sm3::SetOptionalSetting( "r_shadows", 0 );
            Warning( "Team Frontress: experimental SM3 detection bypass accepted; no SM2 shader fallback exists.\n" );
            return;
        }
    }
    else
    {
        message += russian
            ? L"\n\nЭкспериментальный режим недоступен: адаптер не сообщает о DX 9.0c+."
              L" Попробуйте обновить драйвер, выбрать дискретную видеокарту или другой графический API."
            : L"\n\nExperimental mode is unavailable: the adapter does not report DX 9.0c+."
              L" Update the driver, select a discrete GPU, or try another rendering API.";
        MessageBoxW( NULL, message.c_str(), title, MB_OK | MB_ICONERROR | MB_SYSTEMMODAL );
    }
    ExitProcess( 1 ); // The legacy path calls Error() here, which is also fatal.
#else
    Error( "Team Frontress requires Shader Model 3.0. GPU driver: %s; VEN=%04X DEV=%04X; "
           "DX=%d max=%d adapter_max=%d backend=%s",
           adapter.m_pDriverName, adapter.m_VendorID, adapter.m_DeviceID,
           activeDx, maxDx, adapter.m_nMaxDXSupportLevel, backend );
#endif
}

#endif // FRONTRESS_SM3_COMPAT_H
