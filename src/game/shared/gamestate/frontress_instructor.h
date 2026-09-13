//========= Copyright Valve Corporation, All rights reserved. ============//
//
// Frontress-native contextual instructor API.
//===========================================================================//

#pragma once

#ifdef CLIENT_DLL

// Show one contextual campaign hint. A visible hint with the same replacement
// key is replaced in-place; otherwise a lower-priority hint cannot interrupt a
// higher-priority one.
void FrontressInstructor_Show( const char *pszTitle,
                               const char *pszText,
                               int iPriority = 50,
                               float flDuration = 6.0f,
                               const char *pszReplaceKey = "campaign" );
void FrontressInstructor_Hide( const char *pszReplaceKey = NULL );

#endif // CLIENT_DLL
