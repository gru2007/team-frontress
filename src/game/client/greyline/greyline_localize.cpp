//========= GREYLINE FRONTRESS ============================================//
//
// Purpose: loads Greyline's own localization file.
//
// The server states a battle's place in the war as localization tokens rather
// than sentences (see greyline_briefing.cpp), which is the only way one server
// can brief a Russian and an English player correctly at the same time. Those
// tokens live in resource/greyline_%language%.txt and nothing else in the game
// knows to load them, so this does.
//
//=========================================================================//

#include "cbase.h"
#include "igamesystem.h"
#include <vgui/ILocalize.h>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"

//-----------------------------------------------------------------------------
class CGreylineLocalization : public CAutoGameSystem
{
public:
	CGreylineLocalization() : CAutoGameSystem( "CGreylineLocalization" ) {}

	virtual bool Init() OVERRIDE
	{
		if ( g_pVGuiLocalize )
		{
			// A missing file is not fatal: the tokens simply fall back to being
			// shown as-is, which is ugly but still tells the player which front
			// they are on. Failing to start the game over a text file would be
			// worse.
			g_pVGuiLocalize->AddFile( "resource/greyline_%language%.txt" );
		}
		return true;
	}
};

static CGreylineLocalization g_GreylineLocalization;
