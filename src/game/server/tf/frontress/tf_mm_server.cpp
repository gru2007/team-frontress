// Keep the existing server coordinator implementation in this translation unit.
// The original source is retained verbatim in tf_mm_server_impl.inc so the
// readiness probe can be tested in isolation without changing the GC flow.
#include "tf_mm_server_impl.inc"

// The coordinator must not send players to a strict matchmaking server merely
// because the lobby was published. Ask the *same admission gate* used by the
// engine whether every initially assigned SteamID is allowed to connect.
//
// tf_mm_match_ready <match_id_hex> <map> <steamid:team,...>
CON_COMMAND( tf_mm_match_ready, "Check that all assigned players may join this matchmaking match." )
{
	if ( args.ArgC() != 4 )
	{
		Msg( "TFMM_MATCH_READY_FAILED invalid_arguments\n" );
		return;
	}

	const uint64 ulMatchID = ParseHex64( args[1] );
	if ( !ulMatchID )
	{
		Msg( "TFMM_MATCH_READY_FAILED %s invalid_match_id\n", args[1] );
		return;
	}

	CTFMMServer *pBackend = TFMMServer();
	if ( !pBackend->BHaveMatch() )
	{
		Msg( "TFMM_MATCH_READY_PENDING %s lobby_not_published\n", args[1] );
		return;
	}
	if ( pBackend->GetMatchID() != ulMatchID )
	{
		Msg( "TFMM_MATCH_READY_FAILED %s wrong_lobby\n", args[1] );
		return;
	}

	CTFGCServerSystem *pGC = GTFGCClientSystem();
	const CMatchInfo *pMatch = pGC ? pGC->GetMatch() : NULL;
	if ( !pGC || !pGC->IsMMServerModeActive() || !pMatch )
	{
		Msg( "TFMM_MATCH_READY_PENDING %s match_not_built\n", args[1] );
		return;
	}
	if ( pMatch->m_nMatchID != ulMatchID )
	{
		Msg( "TFMM_MATCH_READY_FAILED %s wrong_match_info\n", args[1] );
		return;
	}

	static ConVarRef tf_mm_strict( "tf_mm_strict" );
	if ( tf_mm_strict.GetInt() != 1 )
	{
		Msg( "TFMM_MATCH_READY_FAILED %s roster_gate_disabled\n", args[1] );
		return;
	}
	if ( !TFGameRules() || V_stricmp( STRING( gpGlobals->mapname ), args[2] ) != 0 )
	{
		Msg( "TFMM_MATCH_READY_PENDING %s loading_map\n", args[1] );
		return;
	}

	CUtlVector< TFMMSeat_t > vecSeats;
	ParseSeats( args[3], vecSeats );
	if ( vecSeats.Count() == 0 )
	{
		Msg( "TFMM_MATCH_READY_FAILED %s empty_roster\n", args[1] );
		return;
	}

	FOR_EACH_VEC( vecSeats, i )
	{
		const CSteamID steamID( vecSeats[i].ulSteamID );
		if ( !steamID.IsValid() || !pGC->SteamIDAllowedToConnect( steamID ) )
		{
			Msg( "TFMM_MATCH_READY_PENDING %s waiting_for_reservations\n", args[1] );
			return;
		}
	}

	Msg( "TFMM_MATCH_READY_OK %s\n", args[1] );
}
