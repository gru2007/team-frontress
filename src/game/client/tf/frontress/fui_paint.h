//========= Team Frontress ====================================================//
//
// Stock HUD panels whose part the Frontress HUD draws (a data-replaces word,
// see fui_hud.h) but which must keep running as before: they still decide
// when they are shown and keep their text and timers up to date, and the HUD
// reads them. They only stop painting.
//
//     class CHudAlert : public CHudElement, public EditablePanel
//     {
//         DECLARE_CLASS_SIMPLE( CHudAlert, EditablePanel );
//         FRONTRESS_HUD_PAINTS( "notices" )
//         ...
//
//=============================================================================
#ifndef FUI_PAINT_H
#define FUI_PAINT_H
#ifdef _WIN32
#pragma once
#endif

extern bool FrontressHudReplaces( const char *pszPart );

#define FRONTRESS_HUD_PAINTS( part ) \
public: \
	virtual void PaintTraverse( bool Repaint, bool allowForce = true ) OVERRIDE \
	{ \
		if ( FrontressHudReplaces( part ) ) \
			return; \
		BaseClass::PaintTraverse( Repaint, allowForce ); \
	} \
public:

#endif // FUI_PAINT_H
