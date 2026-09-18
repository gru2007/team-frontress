//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Lightweight native instructor for Team Frontress campaign context.
// This deliberately does not import Momentum/Mapbase's locator stack: TC2 does
// not ship that subsystem. Instead it provides the part Frontress needs now --
// contextual, prioritised, replaceable campaign lessons in the native HUD.
//===========================================================================//

#include "cbase.h"

#ifdef CLIENT_DLL

#include "frontress_instructor.h"
#include "hud.h"
#include "hudelement.h"
#include "hud_macros.h"
#include "iclientmode.h"
#include "vgui/IScheme.h"
#include "vgui/ISurface.h"
#include "vgui_controls/Label.h"

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

ConVar frontress_instructor_enable( "frontress_instructor_enable", "1", FCVAR_ARCHIVE,
    "Show contextual Team Frontress campaign hints." );
ConVar frontress_instructor_round_hints( "frontress_instructor_round_hints", "1", FCVAR_ARCHIVE,
    "Show automatic Frontress lessons for round and objective events." );

class CHudFrontressInstructor : public vgui::Panel, public CHudElement
{
    DECLARE_CLASS_SIMPLE( CHudFrontressInstructor, vgui::Panel );

public:
    CHudFrontressInstructor( const char *pElementName )
        : BaseClass( NULL, "HudFrontressInstructor" ),
          CHudElement( pElementName ),
          m_pTitle( NULL ),
          m_pText( NULL ),
          m_flHideAt( 0.0f ),
          m_iPriority( 0 ),
          m_bShownWarIntro( false )
    {
        SetParent( g_pClientMode->GetViewport() );
        SetPaintBackgroundEnabled( true );
        SetBgColor( Color( 24, 20, 17, 232 ) );
        SetVisible( false );

        m_pTitle = new vgui::Label( this, "FrontressInstructorTitle", "" );
        m_pText = new vgui::Label( this, "FrontressInstructorText", "" );
        m_pTitle->SetContentAlignment( vgui::Label::a_center );
        m_pText->SetContentAlignment( vgui::Label::a_center );
        m_pText->SetWrap( true );
        m_szReplaceKey[0] = '\0';

        s_pInstance = this;
    }

    ~CHudFrontressInstructor()
    {
        if ( s_pInstance == this )
            s_pInstance = NULL;
    }

    void Init() OVERRIDE
    {
        ListenForGameEvent( "teamplay_round_start" );
        ListenForGameEvent( "teamplay_point_captured" );
        ListenForGameEvent( "teamplay_overtime_begin" );
        ListenForGameEvent( "teamplay_round_win" );
    }

    void LevelInit() OVERRIDE
    {
        m_bShownWarIntro = false;
        HideHint();
    }

    void LevelShutdown() OVERRIDE
    {
        HideHint();
    }

    void Reset() OVERRIDE
    {
        // Reset is also called on ordinary respawns. Keep the one-per-map lesson
        // state, but clear a stale panel so it never survives a HUD reset.
        HideHint();
    }

    void FireGameEvent( IGameEvent *pEvent ) OVERRIDE
    {
        if ( !pEvent || !frontress_instructor_enable.GetBool() || !frontress_instructor_round_hints.GetBool() )
            return;

        const char *pszName = pEvent->GetName();
        if ( !Q_stricmp( pszName, "teamplay_round_start" ) && !m_bShownWarIntro )
        {
            m_bShownWarIntro = true;
            ShowHint( "GLOBAL WAR",
                      "This battle is one operation in the persistent RED vs BLU war. Open the War Map to see the sector, front line and deployment.",
                      20, 7.0f, "war-intro" );
            return;
        }

        if ( !Q_stricmp( pszName, "teamplay_point_captured" ) )
        {
            const char *pszPoint = pEvent->GetString( "cpname", "objective" );
            char szText[384];
            Q_snprintf( szText, sizeof( szText ),
                        "%s was captured. Tactical objectives decide this battle; the final result is what moves the persistent campaign front.",
                        pszPoint && pszPoint[0] ? pszPoint : "An objective" );
            ShowHint( "FRONT PRESSURE", szText, 35, 4.5f, "objective" );
            return;
        }

        if ( !Q_stricmp( pszName, "teamplay_overtime_begin" ) )
        {
            ShowHint( "FRONT CONTESTED",
                      "Overtime means the operation is still contested. Hold the objective: only the confirmed battle result changes the campaign.",
                      55, 5.0f, "operation-state" );
            return;
        }

        if ( !Q_stricmp( pszName, "teamplay_round_win" ) )
        {
            ShowHint( "BATTLE COMPLETE",
                      "The local battle is complete. The coordinator can now apply its result to the sector and publish the updated front on the War Map.",
                      70, 6.0f, "battle-result" );
        }
    }

    void ShowHint( const char *pszTitle, const char *pszText, int iPriority,
                   float flDuration, const char *pszReplaceKey )
    {
        if ( !frontress_instructor_enable.GetBool() || !pszText || !pszText[0] )
            return;

        const bool bSameKey = pszReplaceKey && pszReplaceKey[0] &&
            !Q_stricmp( pszReplaceKey, m_szReplaceKey );

        if ( IsVisible() && !bSameKey && iPriority < m_iPriority )
            return;

        Q_strncpy( m_szReplaceKey, pszReplaceKey ? pszReplaceKey : "", sizeof( m_szReplaceKey ) );
        m_iPriority = iPriority;
        m_flHideAt = gpGlobals->curtime + MAX( flDuration, 1.0f );

        m_pTitle->SetText( pszTitle && pszTitle[0] ? pszTitle : "GLOBAL WAR" );
        m_pText->SetText( pszText );
        SetVisible( true );
        InvalidateLayout( true );
    }

    void HideHint( const char *pszReplaceKey = NULL )
    {
        if ( pszReplaceKey && pszReplaceKey[0] && Q_stricmp( pszReplaceKey, m_szReplaceKey ) )
            return;

        SetVisible( false );
        m_flHideAt = 0.0f;
        m_iPriority = 0;
        m_szReplaceKey[0] = '\0';
    }

    void ApplySchemeSettings( vgui::IScheme *pScheme ) OVERRIDE
    {
        BaseClass::ApplySchemeSettings( pScheme );
        SetBorder( pScheme->GetBorder( "ButtonDepressedBorder" ) );

        vgui::HFont hTitle = pScheme->GetFont( "HudFontMediumBold", true );
        vgui::HFont hText = pScheme->GetFont( "HudHintText", true );
        if ( hTitle != vgui::INVALID_FONT ) m_pTitle->SetFont( hTitle );
        if ( hText != vgui::INVALID_FONT ) m_pText->SetFont( hText );

        m_pTitle->SetFgColor( Color( 232, 181, 99, 255 ) );
        m_pText->SetFgColor( Color( 222, 211, 187, 255 ) );
    }

    void PerformLayout() OVERRIDE
    {
        BaseClass::PerformLayout();

        int iScreenWide, iScreenTall;
        vgui::surface()->GetScreenSize( iScreenWide, iScreenTall );
        const int iWide = MIN( 660, MAX( 360, iScreenWide - 48 ) );
        const int iTall = 104;
        SetBounds( ( iScreenWide - iWide ) / 2, MAX( 28, iScreenTall / 10 ), iWide, iTall );
        m_pTitle->SetBounds( 14, 8, iWide - 28, 28 );
        m_pText->SetBounds( 22, 36, iWide - 44, iTall - 44 );
    }

    void OnThink() OVERRIDE
    {
        BaseClass::OnThink();
        if ( IsVisible() && m_flHideAt > 0.0f && gpGlobals->curtime >= m_flHideAt )
            HideHint();
    }

    static CHudFrontressInstructor *Get() { return s_pInstance; }

private:
    vgui::Label *m_pTitle;
    vgui::Label *m_pText;
    float m_flHideAt;
    int m_iPriority;
    bool m_bShownWarIntro;
    char m_szReplaceKey[64];

    static CHudFrontressInstructor *s_pInstance;
};

CHudFrontressInstructor *CHudFrontressInstructor::s_pInstance = NULL;
DECLARE_HUDELEMENT( CHudFrontressInstructor );

void FrontressInstructor_Show( const char *pszTitle, const char *pszText,
                               int iPriority, float flDuration,
                               const char *pszReplaceKey )
{
    if ( CHudFrontressInstructor::Get() )
        CHudFrontressInstructor::Get()->ShowHint( pszTitle, pszText, iPriority, flDuration, pszReplaceKey );
}

void FrontressInstructor_Hide( const char *pszReplaceKey )
{
    if ( CHudFrontressInstructor::Get() )
        CHudFrontressInstructor::Get()->HideHint( pszReplaceKey );
}

CON_COMMAND_F( frontress_hint_test, "Show a test Frontress campaign instructor hint.", FCVAR_CLIENTDLL )
{
    FrontressInstructor_Show( "FOUNDRY 17",
        "BLU has broken the outer line. Winning this battle can advance the persistent front.",
        100, 8.0f, "test" );
}

CON_COMMAND_F( frontress_hint_hide, "Hide the current Frontress campaign instructor hint.", FCVAR_CLIENTDLL )
{
    FrontressInstructor_Hide();
}

#endif // CLIENT_DLL
