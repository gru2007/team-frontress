//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Frontress-native contextual instructor API.
//
// The persistent-war rules own the meaning of a hint.  This module only owns
// presentation, replacement and priority.  Keeping those responsibilities
// separate makes it possible to feed the same events from the coordinator,
// matchmaking or map/game rules without putting campaign logic in the HUD.
//===========================================================================//

#pragma once

#ifdef CLIENT_DLL

void FrontressInstructor_Show( const char *pszTitle,
                               const char *pszText,
                               int iPriority = 50,
                               float flDuration = 6.0f,
                               const char *pszReplaceKey = "campaign" );
void FrontressInstructor_Hide( const char *pszReplaceKey = NULL );

#endif // CLIENT_DLL
