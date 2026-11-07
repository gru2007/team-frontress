//========= Team Frontress ====================================================//
//
// The Frontress menu's state, from the game. See fui_tf.h.
//
//=============================================================================
#include "cbase.h"

// Source's Assert and min/max macros would rewrite RmlUi's declarations.
#pragma push_macro( "Assert" )
#undef Assert
#include "tier0/valve_minmax_off.h"
#include "fui.h"
#include "tier0/valve_minmax_on.h"
#pragma pop_macro( "Assert" )

#include "fui_tf.h"
#include "fui_stage.h"
#include "fui_host.h"
#include "fui_social.h"

#include "frontress_gc.h"
#include "tf_gc_client.h"
#include "tf_gc_status.h"
#include "tf_item_inventory.h"
#include "tf_lobby_server.h"
#include "tf_party.h"
#include "tf_partyclient.h"
#include "tf_rating_data.h"
#include "econ_item_system.h"
#include "filesystem.h"
#include "steam/steam_api.h"
#include "vgui/ILocalize.h"
#include "c_team.h"
#include "c_tf_player.h"
#include "tf_gamerules.h"

#include <map>
#include <set>

// memdbgon must be the last include file in a .cpp file!!!
#include "tier0/memdbgon.h"


namespace
{

using fui::Node;

const float k_flRefreshShown = 0.25f;	// menu on screen
const float k_flRefreshHidden = 1.0f;	// in game: only the queue matters
const float k_flWarPoll = 10.0f;
const float k_flRecordPoll = 30.0f;
const float k_flQuestPoll = 30.0f;
const float k_flToastSeconds = 6.0f;

ConVar fui_fake_party( "fui_fake_party", "0", FCVAR_NONE, "Fill the menu's party with this many made-up members (0-5), to work on the lobby alone." );
ConVar fui_fake_skins( "fui_fake_skins", "1", FCVAR_NONE, "Made-up party members (fui_fake_party) wear war paints, unusual effects and paint, to check how skins draw." );

std::string UTF8( const wchar_t *pwsz )
{
	if ( !pwsz )
		return std::string();
	char sz[ 512 ];
	V_UnicodeToUTF8( pwsz, sz, sizeof( sz ) );
	return sz;
}

std::string Localized( const char *pszToken )
{
	if ( !pszToken || !pszToken[ 0 ] )
		return std::string();
	const wchar_t *pwsz = g_pVGuiLocalize->Find( pszToken );
	return pwsz ? UTF8( pwsz ) : std::string( pszToken[ 0 ] == '#' ? pszToken + 1 : pszToken );
}

std::string SteamName( const CSteamID &steamID )
{
	ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
	if ( !pFriends )
		return std::string();
	if ( steamapicontext->SteamUser() && steamapicontext->SteamUser()->GetSteamID() == steamID )
		return pFriends->GetPersonaName();
	return pFriends->GetFriendPersonaName( steamID );
}

// "avatar:<id>" once Steam has the picture, "" until then (the renderer would
// cache a missing one).
std::string Avatar( const CSteamID &steamID )
{
	ISteamFriends *pFriends = steamapicontext ? steamapicontext->SteamFriends() : NULL;
	if ( !pFriends || pFriends->GetLargeFriendAvatar( steamID ) <= 0 )
		return std::string();
	return "avatar:" + std::to_string( steamID.ConvertToUint64() );
}

CSteamID LocalSteamID()
{
	return ( steamapicontext && steamapicontext->SteamUser() ) ? steamapicontext->SteamUser()->GetSteamID() : CSteamID();
}

struct ClassName_t
{
	const char *pszID;
	int iClass;
};
const ClassName_t k_Classes[] = {
	{ "scout", TF_CLASS_SCOUT }, { "soldier", TF_CLASS_SOLDIER }, { "pyro", TF_CLASS_PYRO },
	{ "demoman", TF_CLASS_DEMOMAN }, { "heavy", TF_CLASS_HEAVYWEAPONS }, { "engineer", TF_CLASS_ENGINEER },
	{ "medic", TF_CLASS_MEDIC }, { "sniper", TF_CLASS_SNIPER }, { "spy", TF_CLASS_SPY },
};

int ClassIndex( const std::string &id )
{
	for ( const ClassName_t &c : k_Classes )
	{
		if ( id == c.pszID )
			return c.iClass;
	}
	return TF_CLASS_SOLDIER;
}

struct Slot_t
{
	const char *pszID;
	int iPosition;
	const char *pszKind;	// what fits: a weapon slot, "cosmetic" or "taunt"
};
const Slot_t k_Slots[] = {
	{ "primary", LOADOUT_POSITION_PRIMARY, "primary" },
	{ "secondary", LOADOUT_POSITION_SECONDARY, "secondary" },
	{ "melee", LOADOUT_POSITION_MELEE, "melee" },
	{ "cosmetic1", LOADOUT_POSITION_HEAD, "cosmetic" },
	{ "cosmetic2", LOADOUT_POSITION_MISC, "cosmetic" },
	{ "cosmetic3", LOADOUT_POSITION_MISC2, "cosmetic" },
	{ "taunt", LOADOUT_POSITION_TAUNT, "taunt" },
};

const Slot_t &SlotByID( const std::string &id )
{
	for ( const Slot_t &slot : k_Slots )
	{
		if ( id == slot.pszID )
			return slot;
	}
	return k_Slots[ 0 ];
}

bool Fits( const CEconItemView *pItem, int iClass, const Slot_t &slot )
{
	const GameItemDefinition_t *pDef = pItem ? pItem->GetStaticData() : NULL;
	if ( !pDef || !pDef->CanBeUsedByClass( iClass ) )
		return false;
	return pDef->GetEquipType() == EQUIP_TYPE_CLASS &&
		AreSlotsConsideredIdentical( pDef->GetEquipType(), pDef->GetLoadoutSlot( iClass ), slot.iPosition );
}

const char *QualityName( int iQuality )
{
	switch ( iQuality )
	{
	case AE_UNIQUE: return "unique";
	case AE_STRANGE: return "strange";
	case AE_VINTAGE: return "vintage";
	case AE_RARITY1: return "genuine";
	case AE_UNUSUAL: return "unusual";
	case AE_COLLECTORS: return "collectors";
	case AE_HAUNTED: return "haunted";
	default: return "normal";
	}
}

// A GET that hands back the body, one at a time.
class CFuiFetch
{
public:
	CFuiFetch() : m_hRequest( INVALID_HTTPREQUEST_HANDLE ), m_bDone( false ), m_flNext( 0.f ) {}

	void Poll( const char *pszPath, float flInterval )
	{
		if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE || Plat_FloatTime() < m_flNext )
			return;
		m_flNext = (float)Plat_FloatTime() + flInterval;
		ISteamHTTP *pHTTP = SteamHTTP();
		const char *pszBase = FrontressGC()->GetBaseURL();
		if ( !pHTTP || !pszBase || !pszBase[ 0 ] )
			return;
		CUtlString strBase( pszBase );
		strBase.StripTrailingSlash();
		HTTPRequestHandle h = pHTTP->CreateHTTPRequest( k_EHTTPMethodGET, CFmtStr1024( "%s%s", strBase.Get(), pszPath ) );
		if ( h == INVALID_HTTPREQUEST_HANDLE )
			return;
		pHTTP->SetHTTPRequestNetworkActivityTimeout( h, 10 );
		SteamAPICall_t hCall = k_uAPICallInvalid;
		if ( !pHTTP->SendHTTPRequest( h, &hCall ) )
		{
			pHTTP->ReleaseHTTPRequest( h );
			return;
		}
		m_hRequest = h;
		m_call.Set( hCall, this, &CFuiFetch::OnDone );
	}

	void Soon() { m_flNext = 0.f; }

	bool Take( Node &out )
	{
		if ( !m_bDone )
			return false;
		m_bDone = false;
		return Node::Parse( m_strBody, out ) && out.IsObject();
	}

	void Cancel()
	{
		m_call.Cancel();
		if ( m_hRequest != INVALID_HTTPREQUEST_HANDLE && SteamHTTP() )
			SteamHTTP()->ReleaseHTTPRequest( m_hRequest );
		m_hRequest = INVALID_HTTPREQUEST_HANDLE;
	}

private:
	void OnDone( HTTPRequestCompleted_t *pInfo, bool bIOFailure )
	{
		ISteamHTTP *pHTTP = SteamHTTP();
		m_hRequest = INVALID_HTTPREQUEST_HANDLE;
		if ( !pHTTP )
			return;
		if ( !bIOFailure && pInfo->m_bRequestSuccessful && pInfo->m_eStatusCode == k_EHTTPStatusCode200OK )
		{
			uint32 unSize = 0;
			if ( pHTTP->GetHTTPResponseBodySize( pInfo->m_hRequest, &unSize ) && unSize < 4 * 1024 * 1024 )
			{
				m_strBody.resize( unSize );
				if ( pHTTP->GetHTTPResponseBodyData( pInfo->m_hRequest, (uint8 *)&m_strBody[ 0 ], unSize ) )
					m_bDone = true;
			}
		}
		pHTTP->ReleaseHTTPRequest( pInfo->m_hRequest );
	}

	HTTPRequestHandle m_hRequest;
	CCallResult< CFuiFetch, HTTPRequestCompleted_t > m_call;
	std::string m_strBody;
	bool m_bDone;
	float m_flNext;
};

} // namespace

struct CFuiTF::Impl
{
	fui::Runtime *pRuntime = nullptr;
	float flNextRefresh = 0.f;
	float flNextLocker = 0.f;
	CFuiFetch war;
	CFuiFetch record;
	CFuiFetch history;
	std::string strQueuedMode;			// what the play button asked for
	CUtlVector< itemid_t > lockerItems;	// locker.items index -> item id

	// Party looks, by SteamID: from the coordinator, or made up (fui_fake_party).
	struct Look_t
	{
		std::string cls;
		int iTeam = TF_TEAM_RED;
		std::vector< FuiLookItem_t > items;
		std::string tag;		// clan tag, from the coordinator
		bool bGold = false;
		bool bSup = false;		// supports the developers
	};
	std::map< uint64, Look_t > looks;
	std::string strSentLook;
	float flNextLookSend = 0.f;
	double flLastLookSend = 0.;

	CFuiFetch quests;
	std::set< std::string > questsDone;		// keys already finished, for toasts
	bool bQuestsKnown = false;

	std::set< uint64 > partySeen;			// members already greeted, for toasts
	bool bPartyKnown = false;

	struct Toast_t
	{
		float flExpires;
		std::string kind, title, body, icon;
	};
	std::vector< Toast_t > toasts;
	bool bToastsDirty = false;
	int nToastID = 0;

	void Toast( const std::string &kind, const std::string &title, const std::string &body, const std::string &icon )
	{
		toasts.push_back( { (float)Plat_FloatTime() + k_flToastSeconds, kind, title, body, icon } );
		if ( toasts.size() > 4 )
			toasts.erase( toasts.begin() );
		bToastsDirty = true;
	}

	void SendOwnLook();
	void OpenClassic( const std::string &what );
	void RefreshSettings();
	void FakeParty( std::vector< CSteamID > &members );
	void RefreshQuests( Node &doc );
	void RefreshHistory( const Node &doc, const CSteamID &owner );
	void RefreshMatch();
	void RefreshToasts();

	std::string Str( const char *pszPath )
	{
		Node *pNode = pRuntime->Get( pszPath );
		return pNode ? pNode->AsString() : std::string();
	}

	void RefreshApp();
	void RefreshPlayerAndParty();
	void RefreshQueue();
	bool StartQueue( const std::string &mode, const std::string *pFront = nullptr );
	void RefreshModes();
	void RefreshLocker();
	void TakeFetches();
};

//-----------------------------------------------------------------------------
void CFuiTF::Impl::RefreshApp()
{
	const CTFGCStatus::Status_t &status = TFGCStatus()->GetStatus();
	Node app;
	const char *pszURL = FrontressGC()->GetBaseURL();
	if ( FrontressGC() && FrontressGC()->BConnected() )
		app[ "gc" ] = "online";
	else if ( !pszURL || !pszURL[ 0 ] || ( status.bChecked && !status.bValid ) )
		app[ "gc" ] = "offline";
	else
		app[ "gc" ] = "connecting";
	app[ "online" ] = status.nOnlinePlayers;
	app[ "matches_live" ] = status.nLiveMatches;
	app[ "no_servers" ] = status.bValid && status.bServerCapacityKnown && status.nFreeServers == 0;
	app[ "version" ] = status.strName.Get();
	// The 3D characters (and the background level or the match, when there
	// is one) are behind the menu: the documents leave the middle to them.
	app[ "world" ] = true;
	static ConVarRef fui_menu_3d( "fui_menu_3d" );
	app[ "models" ] = fui_menu_3d.IsValid() && fui_menu_3d.GetBool();
	pRuntime->Set( "app", std::move( app ) );
}

static const char *ClassID( int iClass )
{
	for ( const ClassName_t &c : k_Classes )
	{
		if ( c.iClass == iClass )
			return c.pszID;
	}
	return "soldier";
}

void CFuiTF::Impl::RefreshPlayerAndParty()
{
	const CSteamID me = LocalSteamID();
	pRuntime->Set( "player.name", Node( SteamName( me ) ) );
	pRuntime->Set( "player.avatar", Node( Avatar( me ) ) );
	const int nMyLevel = (int)( pRuntime->Get( "player.level" ) ? pRuntime->Get( "player.level" )->AsNumber( 1 ) : 1 );

	const CTFParty *pParty = GTFPartyClient() ? GTFPartyClient()->GetActiveParty() : NULL;
	std::vector< CSteamID > ids;
	CSteamID leader = me;
	if ( pParty && pParty->GetNumMembers() > 0 )
	{
		for ( int i = 0; i < pParty->GetNumMembers(); ++i )
			ids.push_back( pParty->GetMember( i ) );
		leader = pParty->GetLeader();
	}
	else
		ids.push_back( me );
	FakeParty( ids );

	// Fortnite's order: you in the middle, the others either side of you.
	std::vector< CSteamID > order;
	order.push_back( me );
	for ( const CSteamID &id : ids )
	{
		if ( id != me )
			order.push_back( id );
	}

	Node members;
	members.SetArray();
	std::set< uint64 > present;
	for ( size_t n = 0; n < order.size(); ++n )
	{
		const CSteamID &id = order[ n ];
		const uint64 ul = id.ConvertToUint64();
		present.insert( ul );
		Node &m = members.Append();
		const bool bMe = id == me;
		auto itLook = looks.find( ul );
		const bool bFake = ul < 76561197960265728ull;
		std::string name = bFake ? std::string( "" ) : SteamName( id );
		static const char *s_FakeNames[] = { "Pootis", "Spycrab", "Dispenser", "Mumbles", "Doktor" };
		if ( bFake )
			name = s_FakeNames[ ( ul - 1 ) % 5 ];
		m[ "name" ] = name;
		m[ "id" ] = std::to_string( ul );
		m[ "avatar" ] = bFake ? "avatar:" + std::to_string( ul ) : Avatar( id );
		m[ "me" ] = bMe;
		m[ "level" ] = bMe ? nMyLevel : ( bFake ? (int)( 3 + ul * 17 % 90 ) : 0 );
		m[ "leader" ] = id == leader;
		m[ "ready" ] = true;	// TF2 parties have no ready check
		m[ "key" ] = bMe ? "me" : "member:" + std::to_string( ul );
		m[ "class" ] = bMe ? Str( "ui.class" ) : ( itLook != looks.end() ? itLook->second.cls : std::string( "soldier" ) );
		m[ "slot" ] = (int)n;
		m[ "has_look" ] = true;
		m[ "tag" ] = bMe ? FuiSocialMyTag() : ( itLook != looks.end() ? itLook->second.tag : std::string() );
		m[ "gold" ] = !bMe && itLook != looks.end() && itLook->second.bGold;
		m[ "sup" ] = bMe ? FuiSocialMySupporter() : ( itLook != looks.end() && itLook->second.bSup );
		if ( bFake && fui_fake_skins.GetBool() )
		{
			// Made-up members are in your clan, and the first one supports us.
			m[ "tag" ] = ul <= 2 ? FuiSocialMyTag() : std::string();
			m[ "sup" ] = ul == 1;
		}

		if ( bPartyKnown && !partySeen.count( ul ) && !bMe )
			Toast( "party", name, pRuntime->Translate( "fui_toast_joined" ), "avatar:" + std::to_string( ul ) );
	}
	partySeen = present;
	bPartyKnown = true;

	const int nMax = 6;
	Node empty;
	empty.SetArray();
	for ( int i = (int)members.Size(); i < nMax; ++i )
		empty.Append( Node( i ) );

	Node party;
	party[ "max" ] = nMax;
	party[ "count" ] = (int)members.Size();
	party[ "leader" ] = leader == me;
	party[ "members" ].Assign( std::move( members ) );
	party[ "empty" ].Assign( std::move( empty ) );
	pRuntime->Set( "party", std::move( party ) );
}

// Made-up party members, so the lobby can be built and tested alone.
void CFuiTF::Impl::FakeParty( std::vector< CSteamID > &members )
{
	const int nFake = clamp( fui_fake_party.GetInt(), 0, 5 );
	for ( int n = 1; n <= nFake; ++n )
	{
		const uint64 ul = (uint64)n;
		members.push_back( CSteamID( ul ) );
		if ( looks.count( ul ) )
			continue;

		Look_t look;
		static const int s_Classes[] = { TF_CLASS_MEDIC, TF_CLASS_HEAVYWEAPONS, TF_CLASS_SCOUT, TF_CLASS_ENGINEER, TF_CLASS_SPY };
		const int iClass = s_Classes[ ( n - 1 ) % 5 ];
		look.cls = ClassID( iClass );
		look.iTeam = TF_TEAM_RED;

		// Two hats for the class, the same ones every time.
		CUtlVector< uint16 > candidates;
		const CEconItemSchema::ItemDefinitionMap_t &defs = GetItemSchema()->GetItemDefinitionMap();
		FOR_EACH_MAP_FAST( defs, i )
		{
			const GameItemDefinition_t *pDef = dynamic_cast< const GameItemDefinition_t * >( defs[ i ] );
			if ( !pDef || pDef->IsHidden() || pDef->IsBaseItem() || !pDef->CanBeUsedByClass( iClass ) )
				continue;
			const int iSlot = pDef->GetLoadoutSlot( iClass );
			if ( iSlot != LOADOUT_POSITION_HEAD && iSlot != LOADOUT_POSITION_MISC )
				continue;
			if ( !pDef->GetInventoryImage() || !pDef->GetInventoryImage()[ 0 ] )
				continue;
			candidates.AddToTail( (uint16)pDef->GetDefinitionIndex() );
		}
		if ( candidates.Count() )
		{
			candidates.Sort( []( const uint16 *a, const uint16 *b ) { return (int)*a - (int)*b; } );
			FuiLookItem_t hat, misc;
			hat.iDef = candidates[ ( n * 7919 ) % candidates.Count() ];
			misc.iDef = candidates[ ( n * 104729 + 13 ) % candidates.Count() ];
			if ( fui_fake_skins.GetBool() )
			{
				// Unusual effects and paint, the way a real backpack has them.
				static const uint32 s_Unusual[] = { 13, 17, 10, 14, 29 };	// burning flames, sunbeams, ...
				static const uint32 s_Paint[] = { 15185211, 12073019, 8208497, 1315860, 15787660 };
				hat.iQuality = AE_UNUSUAL;
				hat.iUnusual = s_Unusual[ ( n - 1 ) % 5 ];
				misc.iPaint = s_Paint[ ( n - 1 ) % 5 ];
			}
			look.items.push_back( hat );
			look.items.push_back( misc );
		}
		if ( fui_fake_skins.GetBool() )
		{
			// A war-painted weapon (the decorated ones, 15000+), held up front.
			CUtlVector< FuiLookItem_t > painted;
			FOR_EACH_MAP_FAST( defs, i )
			{
				const GameItemDefinition_t *pDef = dynamic_cast< const GameItemDefinition_t * >( defs[ i ] );
				if ( !pDef || pDef->GetDefinitionIndex() < 15000 || pDef->GetDefinitionIndex() >= 16000 || !pDef->CanBeUsedByClass( iClass ) )
					continue;
				CEconItemView item;
				item.Init( pDef->GetDefinitionIndex(), AE_PAINTKITWEAPON, AE_USE_SCRIPT_VALUE, 0 );
				uint32 iPaintKit = 0;
				if ( !item.IsValid() || !GetPaintKitDefIndex( &item, &iPaintKit ) )
					continue;
				FuiLookItem_t it;
				it.iDef = (uint16)pDef->GetDefinitionIndex();
				it.iQuality = AE_PAINTKITWEAPON;
				it.iPaintKit = iPaintKit;
				it.flWear = 0.2f;
				it.iSheen = 1 + n % 7;
				painted.AddToTail( it );
			}
			if ( painted.Count() )
			{
				painted.Sort( []( const FuiLookItem_t *a, const FuiLookItem_t *b ) { return (int)a->iDef - (int)b->iDef; } );
				look.items.push_back( painted[ ( n * 31 ) % painted.Count() ] );
			}
		}
		looks[ ul ] = look;
	}
}

static const char *ModeForGroup( ETFMatchGroup eGroup )
{
	switch ( eGroup )
	{
	case k_eTFMatchGroup_Ladder_6v6: return "ranked";
	case k_eTFMatchGroup_Casual_12v12: return "casual";
	default: return "casual";
	}
}

// The war is fought in the casual queue: the coordinator decides which of
// its matches are war battles (config "war.match_groups").
static ETFMatchGroup GroupForMode( const std::string &mode )
{
	if ( mode == "ranked" )
		return k_eTFMatchGroup_Ladder_6v6;
	return k_eTFMatchGroup_Casual_12v12;
}

void CFuiTF::Impl::RefreshQueue()
{
	CTFGCStatus *pStatus = TFGCStatus();
	Node queue;
	switch ( pStatus->GetState() )
	{
	case k_eTFMMState_Searching: queue[ "state" ] = "searching"; break;
	case k_eTFMMState_MatchReady: queue[ "state" ] = "found"; break;
	case k_eTFMMState_Preparing: queue[ "state" ] = "preparing"; break;
	case k_eTFMMState_Connecting: queue[ "state" ] = "joining"; break;
	case k_eTFMMState_InMatch: queue[ "state" ] = "live"; break;
	default: queue[ "state" ] = "idle"; break;
	}
	const ETFMatchGroup eGroup = pStatus->GetQueuedMatchGroup();
	std::string mode = ModeForGroup( eGroup );
	if ( mode == "casual" && strQueuedMode == "war" )
		mode = "war";
	queue[ "mode" ] = mode;
	queue[ "seconds" ] = (int)pStatus->GetQueueSeconds();
	const int nParty = GTFPartyClient() ? GTFPartyClient()->GetNumPartyMembers() : 1;
	queue[ "players" ] = pStatus->GetState() == k_eTFMMState_Searching ?
		Max( nParty, pStatus->GetQueuePlayerCount() ) : pStatus->GetQueuePlayerCount();
	queue[ "needed" ] = pStatus->GetQueueNeededCount();

	CTFGSLobby *pLobby = GTFGCClientSystem()->GetLobby();
	const char *pszMap = pLobby ? pLobby->GetMapName() : "";
	const MapDef_t *pMap = ( pszMap && pszMap[ 0 ] ) ? GetItemSchema()->GetMasterMapDefByName( pszMap ) : NULL;
	queue[ "map" ] = pMap ? Localized( pMap->pszMapNameLocKey ) : std::string( pszMap ? pszMap : "" );
	queue[ "map_image" ] = ( pszMap && pszMap[ 0 ] ) ? std::string( "vgui/maps/menu_thumb_" ) + pszMap : std::string();
	pRuntime->Set( "queue", std::move( queue ) );
}

bool CFuiTF::Impl::StartQueue( const std::string &mode, const std::string *pFront )
{
	const auto error = [this]( const char *key ) {
		Toast( "error", pRuntime->Translate( "fui_err_title" ), pRuntime->Translate( key ), "" );
		return false;
	};
	if ( !FrontressGC() || !FrontressGC()->BConnected() || !GTFPartyClient() || !GCClientSystem() )
		return error( "fui_gc_offline" );
	const auto group = GroupForMode( mode );
	if ( !TFGCStatus()->BGroupOffered( group ) )
		return error( "fui_queue_unavailable" );
	if ( !GTFPartyClient()->GetActiveParty() )
		return error( "fui_queue_party_sync" );
	if ( !GTFPartyClient()->BControllingPartyActions() )
		return error( "fui_queue_leader" );
	if ( TFGCStatus()->GetState() != k_eTFMMState_Idle )
		return error( "fui_queue_active" );
	if ( mode == "war" )
	{
		if ( Str( "player.war_side" ).empty() )
		{
			pRuntime->Navigate( "war" );
			return error( "fui_war_choose_hint" );
		}
	}
	if ( group == k_eTFMatchGroup_Casual_12v12 )
	{
		GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( 51003 );
		const std::string front = pFront ? *pFront : "";
		msg.Body().set_msg( "{\"front\":\"" + front + "\"}" );
		if ( !GCClientSystem()->BSendMessage( msg ) )
			return error( "fui_gc_offline" );
		GTFPartyClient()->MutLocalGroupCriteria().SetLateJoin( true );
	}
	strQueuedMode = mode;
	GTFPartyClient()->RequestQueueForMatch( group );
	RefreshQueue();
	return true;
}

void CFuiTF::Impl::RefreshModes()
{
	CTFGCStatus *pStatus = TFGCStatus();
	struct Mode_t
	{
		const char *pszID, *pszImage, *pszSize, *pszTag;
		bool bEnabled;
	};
	const bool bWar = pRuntime->Get( "war.fronts" ) && pRuntime->Get( "war.fronts" )->Size() > 0;
	const Mode_t modes[] = {
		{ "war", "vgui/main_menu/war_newspaper", "12 × 12", "#fui_tag_featured", bWar && pStatus->BGroupOffered( k_eTFMatchGroup_Casual_12v12 ) },
		{ "casual", "vgui/main_menu/main_menu_button_casual", "12 × 12", "", pStatus->BGroupOffered( k_eTFMatchGroup_Casual_12v12 ) },
		{ "ranked", "vgui/main_menu/main_menu_button_competitive", "6 × 6", "#fui_tag_ranked", pStatus->BGroupOffered( k_eTFMatchGroup_Ladder_6v6 ) },
		{ "mvm", "vgui/main_menu/main_menu_button_mvm", "6", "#fui_tag_soon", false },
		{ "community", "vgui/main_menu/main_menu_button_community_server", "", "", true },
		{ "training", "vgui/main_menu/main_menu_button_training", "1", "", true },
	};
	Node list;
	list.SetArray();
	for ( const Mode_t &m : modes )
	{
		Node &mode = list.Append();
		mode[ "id" ] = m.pszID;
		mode[ "name" ] = std::string( "#fui_mode_" ) + m.pszID;
		mode[ "desc" ] = std::string( "#fui_mode_" ) + m.pszID + "_desc";
		mode[ "image" ] = m.pszImage;
		mode[ "size" ] = m.pszSize;
		mode[ "tag" ] = m.bEnabled || !V_strcmp( m.pszTag, "#fui_tag_soon" ) ? m.pszTag : "";
		mode[ "enabled" ] = m.bEnabled;
		mode[ "players" ] = 0;
	}
	pRuntime->Set( "modes", std::move( list ) );

	// The casual map pool: what the coordinator runs, ticked as the party's
	// criteria say.
	Node maps;
	maps.SetArray();
	const CUtlVector< CUtlString > *pPool = pStatus->GetGroupMaps( k_eTFMatchGroup_Casual_12v12 );
	if ( pPool && GTFPartyClient() )
	{
		const CTFGroupMatchCriteria &criteria = GTFPartyClient()->GetLocalGroupCriteria();
		FOR_EACH_VEC( *pPool, i )
		{
			const MapDef_t *pMap = GetItemSchema()->GetMasterMapDefByName( ( *pPool )[ i ].Get() );
			if ( !pMap )
				continue;
			Node &map = maps.Append();
			map[ "id" ] = pMap->pszMapName;
			map[ "name" ] = Localized( pMap->pszMapNameLocKey );
			map[ "image" ] = std::string( "vgui/maps/menu_thumb_" ) + pMap->pszMapName;
			map[ "on" ] = criteria.IsCasualMapSelected( pMap->m_nDefIndex );
		}
	}
	pRuntime->Set( "maps", std::move( maps ) );
}

//-----------------------------------------------------------------------------
// The locker: the backpack, filtered to one class and slot.
//-----------------------------------------------------------------------------
static void ItemNode( const CEconItemView *pItem, Node &out )
{
	const GameItemDefinition_t *pDef = pItem->GetStaticData();
	out[ "name" ] = UTF8( pItem->GetItemName() );
	out[ "type" ] = Localized( pDef ? pDef->GetItemTypeName() : NULL );
	const char *pszImage = pDef ? pDef->GetInventoryImage() : NULL;
	out[ "icon" ] = ( pszImage && pszImage[ 0 ] ) ? std::string( pszImage ) + "_large" : std::string();
	out[ "quality" ] = QualityName( pItem->GetQuality() );
	out[ "level" ] = (int)pItem->GetItemLevel();
	out[ "desc" ] = Localized( pDef ? pDef->GetItemDesc() : NULL );
}

void CFuiTF::Impl::RefreshLocker()
{
	CTFInventoryManager *pManager = TFInventoryManager();
	CTFPlayerInventory *pInventory = pManager ? pManager->GetLocalTFInventory() : NULL;
	if ( !pInventory )
		return;

	const int iClass = ClassIndex( Str( "ui.class" ) );
	const Slot_t &current = SlotByID( Str( "ui.slot" ) );
	const std::string filter = Str( "locker.filter" );
	const int nPick = (int)( pRuntime->Get( "ui.pick" ) ? pRuntime->Get( "ui.pick" )->AsNumber( -1 ) : -1 );
	const bool bHadPick = lockerItems.IsValidIndex( nPick );
	const itemid_t selected = bHadPick ? lockerItems[ nPick ] : INVALID_ITEM_ID;

	lockerItems.RemoveAll();
	int iNewPick = -1;

	// What is worn, slot by slot.
	Node equipped;
	equipped.SetArray();
	itemid_t worn = INVALID_ITEM_ID;
	for ( const Slot_t &slot : k_Slots )
	{
		Node &entry = equipped.Append();
		entry[ "id" ] = slot.pszID;
		std::string label = pRuntime->Translate( std::string( "fui_slot_" ) + slot.pszKind );
		if ( !V_strcmp( slot.pszKind, "cosmetic" ) )
			label += std::string( " " ) + ( slot.pszID + 8 );
		entry[ "name" ] = label;
		CEconItemView *pItem = pManager->GetItemInLoadoutForClass( iClass, slot.iPosition );
		if ( pItem && pItem->IsValid() )
		{
			ItemNode( pItem, entry[ "item" ] );
			if ( &slot == &current )
				worn = pItem->GetItemID();
		}
		else
			entry[ "item" ].SetObject();
	}

	// What could go in the chosen slot: the stock item, then the backpack.
	Node view, detail;
	view.SetArray();
	detail.SetObject();
	auto offer = [ & ]( CEconItemView *pItem, itemid_t id ) {
		const char *pszQuality = QualityName( pItem->GetQuality() );
		if ( filter == "strange" && V_strcmp( pszQuality, "strange" ) )
			return;
		if ( filter == "rare" && ( !V_strcmp( pszQuality, "unique" ) || !V_strcmp( pszQuality, "strange" ) || !V_strcmp( pszQuality, "normal" ) ) )
			return;
		const int index = lockerItems.AddToTail( id );
		Node &entry = view.Append();
		ItemNode( pItem, entry );
		entry[ "index" ] = index;
		entry[ "equipped" ] = id == worn;
		if ( bHadPick && id == selected )
		{
			iNewPick = index;
			detail.Assign( entry.Clone() );
		}
	};

	CEconItemView *pBase = pManager->GetBaseItemForClass( iClass, current.iPosition );
	if ( pBase && pBase->IsValid() )
		offer( pBase, INVALID_ITEM_ID );
	for ( int i = 0; i < pInventory->GetItemCount(); ++i )
	{
		CEconItemView *pItem = pInventory->GetItem( i );
		if ( pItem && Fits( pItem, iClass, current ) )
			offer( pItem, pItem->GetItemID() );
	}

	pRuntime->Set( "ui.pick", Node( iNewPick ) );
	pRuntime->Set( "locker.slot_label", Node( pRuntime->Translate( std::string( "fui_slot_" ) + current.pszKind ) ) );
	pRuntime->Set( "locker.inventory_ready", Node( GTFGCClientSystem()->BValveInventoryReady() ) );
	pRuntime->Set( "locker.inventory_retrying", Node( GTFGCClientSystem()->BValveInventoryRetrying() ) );
	pRuntime->Set( "locker.inventory_count", Node( pInventory->GetItemCount() ) );
	pRuntime->Set( "locker.equipped", std::move( equipped ) );
	pRuntime->Set( "locker.count", Node( (int)view.Size() ) );
	pRuntime->Set( "locker.view", std::move( view ) );
	pRuntime->Set( "locker.detail", std::move( detail ) );
}

//-----------------------------------------------------------------------------
void CFuiTF::Impl::TakeFetches()
{
	Node doc;
	if ( war.Take( doc ) )
	{
		// Open the war screen on the operation people are needed at, unless
		// the player already picked a sector that still exists.
		std::string selected = Str( "ui.front" ), top;
		bool bKnown = false;
		if ( const Node *pTop = doc.Find( "top" ) )
			top = pTop->AsString();
		if ( const Node *pFronts = doc.Find( "fronts" ) )
		{
			for ( size_t i = 0; i < pFronts->Size(); ++i )
				bKnown = bKnown || ( pFronts->At( i )->Find( "id" ) && pFronts->At( i )->Find( "id" )->AsString() == selected );
		}
		pRuntime->Set( "war", std::move( doc ) );
		if ( !bKnown )
			pRuntime->Set( "ui.front", Node( top ) );
	}

	if ( record.Take( doc ) )
	{
		const Node *pLevel = doc.Find( "level" );
		const Node *pInto = doc.Find( "level_xp" );
		const Node *pNeed = doc.Find( "level_xp_total" );
		const double flNeed = pNeed ? MAX( 1.0, pNeed->AsNumber() ) : 1.0;
		pRuntime->Set( "player.level", Node( pLevel ? pLevel->AsNumber( 1 ) : 1.0 ) );
		pRuntime->Set( "player.xp", Node( pInto ? pInto->AsNumber() : 0.0 ) );
		pRuntime->Set( "player.xp_next", Node( flNeed ) );
		pRuntime->Set( "player.xp_frac", Node( pInto ? pInto->AsNumber() / flNeed : 0.0 ) );

		const double flMatches = doc.Find( "matches" ) ? doc.Find( "matches" )->AsNumber() : 0.0;
		const double flWins = doc.Find( "wins" ) ? doc.Find( "wins" )->AsNumber() : 0.0;
		pRuntime->Set( "career.matches", Node( flMatches ) );
		pRuntime->Set( "career.wins", Node( flMatches > 0 ? flWins / flMatches : 0.0 ) );

		Node ratings;
		ratings.SetArray();
		const double flRating = doc.Find( "rating" ) ? doc.Find( "rating" )->AsNumber() : 0.0;
		if ( flRating > 0 && flMatches > 0 )
		{
			Node &r = ratings.Append();
			r[ "mode" ] = "#fui_tag_ranked";
			r[ "rating" ] = flRating;
			r[ "delta" ] = 0;
			r[ "rank" ] = "";
			if ( const auto *rank = CTFRatingData::YieldingGetPlayerRatingDataBySteamID( LocalSteamID(), k_nMMRating_6v6_Rank ) )
			{
				const uint32 nRank = rank->Obj().rating_primary();
				r[ "rank" ] = nRank ? Localized( CFmtStr( "#TF_Competitive_Rank_%u", nRank ) ) :
					pRuntime->Translate( "fui_career_placement" );
			}
			r[ "frac" ] = clamp( ( flRating - 1000.0 ) / 1000.0, 0.0, 1.0 );
		}
		pRuntime->Set( "career.ratings", std::move( ratings ) );
	}
}

void CFuiTF::Impl::RefreshHistory( const Node &doc, const CSteamID &owner )
{
	Node historyRows;
	historyRows.SetArray();
	const std::string steamID = std::to_string( owner.ConvertToUint64() );
	const Node *matches = doc.Find( "matches" );
	for ( size_t i = 0; matches && i < matches->Size(); ++i )
	{
		const Node &match = *matches->At( i );
		const Node *result = match.Find( "result" );
		const Node *players = result ? result->Find( "players" ) : nullptr;
		for ( size_t j = 0; players && j < players->Size(); ++j )
		{
			const Node &player = *players->At( j );
			const Node *id = player.Find( "steam_id" );
			if ( !id || id->AsString() != steamID )
				continue;

			const auto number = []( const Node &obj, const char *key ) -> double {
				const Node *value = obj.Find( key );
				return value ? value->AsNumber() : 0.0;
			};
			const Node *map = match.Find( "map" );
			const std::string name = map ? map->AsString() : "";
			const MapDef_t *pMap = GetItemSchema()->GetMasterMapDefByName( name.c_str() );
			const Node *campaign = match.Find( "campaign" );
			const bool bWar = campaign && !campaign->AsString().empty();
			const Node *date = match.Find( "ended_at" );
			const Node *excluded = player.Find( "excluded_from_progression" );
			const int nStatus = (int)number( *result, "status" );
			const int nWinner = (int)number( *result, "winning_team" );
			const int nTeam = (int)number( player, "team" );
			const bool bCounted = ( nStatus == 0 || nStatus == 7 ) && ( !excluded || !excluded->AsBool() );

			Node &row = historyRows.Append();
			row[ "map" ] = pMap ? Localized( pMap->pszMapNameLocKey ) : name;
			row[ "image" ] = std::string( "vgui/maps/menu_thumb_" ) + name;
			row[ "mode" ] = std::string( "#fui_mode_" ) +
				( bWar ? "war" : ModeForGroup( (ETFMatchGroup)(int)number( match, "match_group" ) ) );
			row[ "when" ] = date ? date->AsString().substr( 0, 10 ) : "";
			row[ "result" ] = !bCounted ? "uncounted" :
				( nWinner == TF_TEAM_RED || nWinner == TF_TEAM_BLUE ) ? ( nWinner == nTeam ? "win" : "loss" ) : "draw";
			const double red = number( *result, "red_score" ), blu = number( *result, "blue_score" );
			if ( red < 4294967295.0 && blu < 4294967295.0 )
				row[ "score" ] = std::to_string( (uint32)red ) + " : " + std::to_string( (uint32)blu );
			row[ "kills" ] = number( player, "kills" );
			row[ "deaths" ] = number( player, "deaths" );
			row[ "has_stats" ] = true;
			row[ "xp" ] = number( player, "xp_change" );
			row[ "delta" ] = number( player, "display_rating_change" );
			break;
		}
	}
	pRuntime->Set( "career.history", std::move( historyRows ) );
}

//-----------------------------------------------------------------------------
// Looks
//-----------------------------------------------------------------------------
// The game's own windows the UI opens, by name; nothing else gets through.
void CFuiTF::Impl::OpenClassic( const std::string &what )
{
	static const struct
	{
		const char *pszName;
		const char *pszCommand;
	} s_Windows[] = {
		{ "options", "gamemenucommand OpenOptionsDialog" },
		{ "advanced", "opentf2options" },
		{ "achievements", "gamemenucommand OpenAchievementsDialog" },
		{ "workshop", "OpenSteamWorkshopDialog" },
		{ "backpack", "open_charinfo_backpack" },
		{ "crafting", "open_charinfo_crafting" },
		{ "armory", "open_charinfo_armory" },
		{ "loadout", "open_charinfo" },
		{ "servers", "gamemenucommand openserverbrowser" },
		{ "create", "gamemenucommand OpenCreateMultiplayerGameDialog" },
		{ "mute", "gamemenucommand OpenMutePlayerDialog" },
		{ "vote", "callvote" },
		{ "console", "con_enable 1; showconsole" },
	};
	for ( const auto &window : s_Windows )
	{
		if ( what != window.pszName )
			continue;
		FrontressUIYield();
		pRuntime->Set( "ui.modal", Node( "" ) );
		engine->ClientCmd_Unrestricted( CFmtStr( "%s\n", window.pszCommand ) );
		return;
	}
}

// What the settings window offers: the HUDs installed (resource/fui/huds).
void CFuiTF::Impl::RefreshSettings()
{
	Node settings;
	settings.SetObject();
	static ConVarRef fui_hud_style( "fui_hud_style" );
	static ConVarRef fui_menu_3d( "fui_menu_3d" );
	settings[ "menu_3d" ] = Node( fui_menu_3d.IsValid() && fui_menu_3d.GetBool() );
	settings[ "hud_style" ] = Node( fui_hud_style.IsValid() ? fui_hud_style.GetString() : "auto" );
	Node &huds = settings[ "huds" ];
	huds.SetArray();
	huds.Append( Node( "auto" ) );
	FileFindHandle_t h;
	for ( const char *pszName = g_pFullFileSystem->FindFirstEx( "resource/fui/huds/*", "GAME", &h ); pszName; pszName = g_pFullFileSystem->FindNext( h ) )
	{
		if ( pszName[ 0 ] == '.' || !g_pFullFileSystem->FindIsDirectory( h ) )
			continue;
		bool bSeen = false;
		for ( size_t i = 0; i < huds.Size(); ++i )
			bSeen = bSeen || huds.At( i )->AsString() == pszName;
		if ( !bSeen )
			huds.Append( Node( pszName ) );
	}
	g_pFullFileSystem->FindClose( h );
	pRuntime->Set( "settings", std::move( settings ) );
}

// The coordinator shows the party what we look like: the lobby class and
// what that class wears.
void CFuiTF::Impl::SendOwnLook()
{
	if ( !FrontressGC() || !FrontressGC()->BConnected() || !GCClientSystem() )
	{
		strSentLook.clear();
		return;
	}
	if ( Plat_FloatTime() < flNextLookSend )
		return;
	flNextLookSend = (float)Plat_FloatTime() + 1.f;

	// Each item with what makes it look like itself (war paint, wear, effect,
	// paint), so the party sees the real thing.
	const std::string cls = Str( "ui.class" );
	const int iClass = ClassIndex( cls );
	Node look;
	look[ "class" ] = cls;
	look[ "team" ] = "red";
	Node &items = look[ "items" ];
	items.SetArray();
	CTFInventoryManager *pManager = TFInventoryManager();
	for ( int i = 0; pManager && i < CLASS_LOADOUT_POSITION_COUNT; ++i )
	{
		CEconItemView *pItem = pManager->GetItemInLoadoutForClass( iClass, i );
		if ( !pItem || !pItem->IsValid() || pItem->GetStaticData()->IsBaseItem() )
			continue;
		FuiLookItem_t it;
		FuiLookItemRead( pItem, it );
		Node &e = items.Append();
		e[ "d" ] = (int)it.iDef;
		if ( it.iQuality ) e[ "q" ] = (int)it.iQuality;
		if ( it.iPaintKit ) { e[ "pk" ] = (double)it.iPaintKit; e[ "w" ] = it.flWear; }
		if ( it.ulSeed ) e[ "sd" ] = std::to_string( it.ulSeed );
		if ( it.iPaint ) e[ "p" ] = (double)it.iPaint;
		if ( it.iPaint2 ) e[ "p2" ] = (double)it.iPaint2;
		if ( it.iUnusual ) e[ "u" ] = (double)it.iUnusual;
		if ( it.iSheen ) e[ "ks" ] = (double)it.iSheen;
		if ( it.bAustralium ) e[ "au" ] = true;
		if ( it.bFestive ) e[ "f" ] = true;
		if ( it.iStyle ) e[ "st" ] = (int)it.iStyle;
	}
	const std::string json = look.Dump();
	if ( json == strSentLook && Plat_FloatTime() - flLastLookSend < 30. )
		return;

	GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( 51001 );	// services/gc/internal/gc/looks.go
	msg.Body().set_msg( json.c_str() );
	if ( GCClientSystem()->BSendMessage( msg ) )
	{
		strSentLook = json;
		flLastLookSend = Plat_FloatTime();
	}
}

bool CFuiTF::GetLook( const std::string &key, FuiLook_t &out )
{
	if ( !m_pImpl )
		return false;
	Impl &d = *m_pImpl;
	if ( key == "me" )
	{
		out.iClass = ClassIndex( d.Str( "ui.class" ) );
		out.iTeam = TF_TEAM_RED;
		out.bLocalLoadout = true;
		return true;
	}
	if ( !key.compare( 0, 6, "class:" ) )
	{
		out.iClass = ClassIndex( key.substr( 6 ) );
		out.iTeam = d.Str( "classsel.team" ) == "blu" ? TF_TEAM_BLUE : TF_TEAM_RED;
		out.bLocalLoadout = true;
		return true;
	}
	if ( !key.compare( 0, 7, "member:" ) )
	{
		auto it = d.looks.find( V_atoui64( key.c_str() + 7 ) );
		if ( it == d.looks.end() )
		{
			out.iClass = TF_CLASS_SOLDIER;
			out.iTeam = TF_TEAM_RED;
			out.bLocalLoadout = false;
			out.vecItems.RemoveAll();
			return true;
		}
		const Impl::Look_t &look = it->second;
		out.iClass = ClassIndex( look.cls );
		out.iTeam = look.iTeam;
		out.bLocalLoadout = false;
		out.vecItems.RemoveAll();
		for ( const FuiLookItem_t &item : look.items )
			out.vecItems.AddToTail( item );
		return true;
	}
	return false;
}

void CFuiTF::OnPartyLooks( const char *pszJSON )
{
	if ( !m_pImpl || !pszJSON )
		return;
	Node doc;
	if ( !Node::Parse( pszJSON, doc ) || !doc.IsObject() )
		return;
	for ( size_t i = 0; i < doc.MemberCount(); ++i )
	{
		const uint64 ul = V_atoui64( doc.MemberName( i ).c_str() );
		const Node &entry = doc.Member( i );
		if ( !ul || !entry.IsObject() || ul == LocalSteamID().ConvertToUint64() )
			continue;
		Impl::Look_t look;
		look.cls = entry.Find( "class" ) ? entry.Find( "class" )->AsString() : "soldier";
		look.iTeam = ( entry.Find( "team" ) && entry.Find( "team" )->AsString() == "blu" ) ? TF_TEAM_BLUE : TF_TEAM_RED;
		if ( const Node *pItems = entry.Find( "items" ) )
		{
			for ( size_t k = 0; k < pItems->Size() && k < 16; ++k )
			{
				const Node &e = *pItems->At( k );
				auto num = [ &e ]( const char *pszKey ) { const Node *p = e.Find( pszKey ); return p ? p->AsNumber() : 0.0; };
				FuiLookItem_t it;
				it.iDef = (uint16)( e.IsObject() ? num( "d" ) : e.AsNumber() );
				it.iQuality = (uint8)num( "q" );
				it.iPaintKit = (uint32)num( "pk" );
				it.flWear = (float)num( "w" );
				it.ulSeed = e.Find( "sd" ) ? V_atoui64( e.Find( "sd" )->AsString().c_str() ) : 0;
				it.iPaint = (uint32)num( "p" );
				it.iPaint2 = (uint32)num( "p2" );
				it.iUnusual = (uint32)num( "u" );
				it.iSheen = (uint32)num( "ks" );
				it.bAustralium = num( "au" ) != 0;
				it.bFestive = num( "f" ) != 0;
				it.iStyle = (uint8)num( "st" );
				if ( it.iDef )
					look.items.push_back( it );
			}
		}
		look.tag = entry.Find( "tag" ) ? entry.Find( "tag" )->AsString() : std::string();
		look.bGold = entry.Find( "gold" ) && entry.Find( "gold" )->AsBool();
		look.bSup = entry.Find( "sup" ) && entry.Find( "sup" )->AsBool();
		m_pImpl->looks[ ul ] = look;
	}
}

void FuiOnPartyLooks( const char *pszJSON )
{
	FuiTF().OnPartyLooks( pszJSON );
}

//-----------------------------------------------------------------------------
// Quests (GET /v1/player/<id>/quests)
//-----------------------------------------------------------------------------
static const char *QuestPhoto( const std::string &kind, const std::string &cls )
{
	static std::string s_Photo;
	if ( kind == "class_wins" && !cls.empty() )
	{
		s_Photo = "vgui/contracts/photo_" + ( cls == "demoman" ? std::string( "demo" ) : cls ) + "1";
		return s_Photo.c_str();
	}
	if ( kind == "kills" )
		return "vgui/contracts/photo_headhunter";
	if ( kind == "war_wins" )
		return "vgui/contracts/photo_any_cp";
	if ( kind == "healing" )
		return "vgui/contracts/photo_medic1";
	if ( kind == "damage" )
		return "vgui/contracts/photo_powerhouse1";
	if ( kind == "wins" || kind == "matches" )
		return "vgui/contracts/photo_group1";
	return "vgui/contracts/photo_play_any_class";
}

void CFuiTF::Impl::RefreshQuests( Node &doc )
{
	Node out;
	int nDone = 0, nTotal = 0;
	for ( const char *pszList : { "daily", "weekly" } )
	{
		Node list;
		list.SetArray();
		const Node *pIn = doc.Find( pszList );
		for ( size_t i = 0; pIn && i < pIn->Size(); ++i )
		{
			const Node &q = *pIn->At( i );
			Node &e = list.Append();
			const std::string kind = q.Find( "kind" ) ? q.Find( "kind" )->AsString() : "";
			const std::string cls = q.Find( "class" ) ? q.Find( "class" )->AsString() : "";
			const double target = q.Find( "target" ) ? MAX( 1.0, q.Find( "target" )->AsNumber() ) : 1.0;
			const double progress = q.Find( "progress" ) ? q.Find( "progress" )->AsNumber() : 0.0;
			const bool bDone = q.Find( "done" ) && q.Find( "done" )->AsBool();
			const std::string key = q.Find( "key" ) ? q.Find( "key" )->AsString() : "";
			e[ "key" ] = key;
			e[ "kind" ] = kind;
			e[ "class" ] = cls;
			e[ "target" ] = target;
			e[ "progress" ] = progress;
			e[ "frac" ] = clamp( progress / target, 0.0, 1.0 );
			e[ "xp" ] = q.Find( "xp" ) ? q.Find( "xp" )->AsNumber() : 0.0;
			e[ "done" ] = bDone;
			e[ "weekly" ] = !V_strcmp( pszList, "weekly" );
			e[ "photo" ] = QuestPhoto( kind, cls );
			++nTotal;
			nDone += bDone;
			if ( bDone && bQuestsKnown && !questsDone.count( key ) )
				Toast( "quest", pRuntime->Translate( "fui_toast_quest" ), "+" + std::to_string( (int)e[ "xp" ].AsNumber() ) + " XP", std::string( "tex:" ) + QuestPhoto( kind, cls ) );
			if ( bDone )
				questsDone.insert( key );
		}
		out[ pszList ].Assign( std::move( list ) );
	}
	bQuestsKnown = true;
	out[ "daily_left" ] = doc.Find( "daily_left" ) ? doc.Find( "daily_left" )->AsNumber() : 0.0;
	out[ "weekly_left" ] = doc.Find( "weekly_left" ) ? doc.Find( "weekly_left" )->AsNumber() : 0.0;
	out[ "done" ] = nDone;
	out[ "total" ] = nTotal;
	pRuntime->Set( "quests", std::move( out ) );
}

//-----------------------------------------------------------------------------
// The match we are in: for the pause screen and the HUD.
//-----------------------------------------------------------------------------
void CFuiTF::Impl::RefreshMatch()
{
	Node match;
	const bool bInGame = engine->IsInGame() && !engine->IsLevelMainMenuBackground();
	match[ "active" ] = bInGame;
	if ( bInGame )
	{
		char szMap[ MAX_PATH ];
		V_FileBase( engine->GetLevelName(), szMap, sizeof( szMap ) );
		const MapDef_t *pMap = GetItemSchema()->GetMasterMapDefByName( szMap );
		match[ "map" ] = pMap ? Localized( pMap->pszMapNameLocKey ) : std::string( szMap );
		match[ "map_id" ] = szMap;
		match[ "image" ] = std::string( "vgui/maps/menu_thumb_" ) + szMap;
		C_Team *pRed = GetGlobalTeam( TF_TEAM_RED );
		C_Team *pBlu = GetGlobalTeam( TF_TEAM_BLUE );
		match[ "red" ] = pRed ? pRed->Get_Score() : 0;
		match[ "blu" ] = pBlu ? pBlu->Get_Score() : 0;
		C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
		const int iTeam = pLocal ? pLocal->GetTeamNumber() : TEAM_UNASSIGNED;
		match[ "team" ] = iTeam == TF_TEAM_BLUE ? "blu" : iTeam == TF_TEAM_RED ? "red" : "none";
		const int iClass = ( pLocal && pLocal->GetPlayerClass() ) ? pLocal->GetPlayerClass()->GetClassIndex() : TF_CLASS_UNDEFINED;
		match[ "class" ] = iClass > TF_CLASS_UNDEFINED && iClass < TF_LAST_NORMAL_CLASS ? ClassID( iClass ) : "";
		match[ "official" ] = TFGameRules() && TFGameRules()->IsMatchTypeCasual();
	}
	pRuntime->Set( "match", std::move( match ) );
	pRuntime->Set( "ui.in_game", Node( bInGame ) );
}

void CFuiTF::Impl::RefreshToasts()
{
	const float flNow = (float)Plat_FloatTime();
	const size_t nBefore = toasts.size();
	toasts.erase( std::remove_if( toasts.begin(), toasts.end(), [ flNow ]( const Toast_t &t ) { return t.flExpires < flNow; } ), toasts.end() );
	if ( !bToastsDirty && toasts.size() == nBefore )
		return;
	bToastsDirty = false;
	Node list;
	list.SetArray();
	for ( const Toast_t &t : toasts )
	{
		Node &e = list.Append();
		e[ "kind" ] = t.kind;
		e[ "title" ] = t.title;
		e[ "body" ] = t.body;
		e[ "icon" ] = t.icon;
	}
	pRuntime->Set( "toasts", std::move( list ) );
}

//-----------------------------------------------------------------------------
void CFuiTF::Attach( fui::Runtime &runtime )
{
	delete m_pImpl;
	m_pImpl = new Impl;
	m_pImpl->pRuntime = &runtime;
}

void CFuiTF::Detach()
{
	FuiSocialShutdown();
	if ( m_pImpl )
	{
		m_pImpl->war.Cancel();
		m_pImpl->record.Cancel();
		m_pImpl->history.Cancel();
		m_pImpl->quests.Cancel();
	}
	delete m_pImpl;
	m_pImpl = nullptr;
}

void CFuiTF::Update( fui::Runtime &runtime, bool bShown )
{
	if ( !m_pImpl || m_pImpl->pRuntime != &runtime )
		Attach( runtime );
	Impl &d = *m_pImpl;

	const float flNow = (float)Plat_FloatTime();
	if ( flNow < d.flNextRefresh )
		return;
	d.flNextRefresh = flNow + ( bShown ? k_flRefreshShown : k_flRefreshHidden );

	d.RefreshQueue();
	d.RefreshMatch();
	FuiSocialUpdate( runtime, bShown );
	d.RefreshToasts();

	// Quests show in the HUD too, so they are kept fresh in a match.
	const CSteamID me = LocalSteamID();
	if ( me.IsValid() )
		d.quests.Poll( CFmtStr( "/v1/player/%llu/quests", me.ConvertToUint64() ), k_flQuestPoll );
	Node questDoc;
	if ( d.quests.Take( questDoc ) )
		d.RefreshQuests( questDoc );
	d.SendOwnLook();

	if ( !bShown )
		return;

	d.RefreshApp();
	d.RefreshPlayerAndParty();
	d.RefreshModes();

	d.war.Poll( "/v1/war", k_flWarPoll );
	if ( me.IsValid() )
		d.record.Poll( CFmtStr( "/v1/player/%llu", me.ConvertToUint64() ), k_flRecordPoll );
	d.TakeFetches();

	if ( runtime.Screen() == "career" && me.IsValid() )
	{
		d.history.Poll( CFmtStr( "/v1/matches?steam_id=%llu&state=ended&limit=6", me.ConvertToUint64() ), 5.f );
		Node historyDoc;
		if ( d.history.Take( historyDoc ) )
			d.RefreshHistory( historyDoc, me );
	}

	if ( runtime.Screen() == "locker" && flNow >= d.flNextLocker )
	{
		d.flNextLocker = flNow + 2.f;
		d.RefreshLocker();
	}
}

void CFuiTF::OnNavigate( const std::string &screen )
{
	if ( !m_pImpl )
		return;
	if ( screen == "locker" )
		m_pImpl->RefreshLocker();
	if ( screen == "war" )
		m_pImpl->war.Soon();
	if ( screen == "career" )
	{
		m_pImpl->history.Soon();
		m_pImpl->record.Soon();
	}
	FuiSocialNavigate( *m_pImpl->pRuntime, screen );
}

void CFuiTF::Toast( const std::string &kind, const std::string &title, const std::string &body, const std::string &icon )
{
	if ( m_pImpl )
		m_pImpl->Toast( kind, title, body, icon );
}

void CFuiTF::OnCommand( const std::string &name, const std::vector< std::string > &args )
{
	if ( !m_pImpl )
		return;
	Impl &d = *m_pImpl;
	fui::Runtime &ui = *d.pRuntime;
	const std::string arg = args.empty() ? std::string() : args[ 0 ];

	if ( FuiSocialCommand( ui, name, args ) )
		return;
	if ( name == "queue" )
	{
		if ( arg == "community" )
			d.OpenClassic( "servers" );
		else if ( arg == "training" )
			d.OpenClassic( "create" );
		else if ( arg == "casual" || arg == "ranked" || arg == "war" )
			d.StartQueue( arg );
	}
	else if ( name == "war_side" )
	{
		if ( ( arg == "red" || arg == "blu" ) && FrontressGC() && FrontressGC()->BConnected() && GCClientSystem() )
		{
			GCSDK::CProtoBufMsg< CMsgPartyChatMsg > msg( 51003 );
			msg.Body().set_msg( ( "{\"side\":\"" + arg + "\"}" ).c_str() );
			GCClientSystem()->BSendMessage( msg );
		}
	}
	else if ( name == "war_join" )
	{
		std::string front;
		for ( char c : arg )
		{
			if ( isalnum( (unsigned char)c ) || c == '-' || c == '_' )
				front += c;
		}
		if ( !front.empty() && d.StartQueue( "war", &front ) )
			ui.Set( "ui.mode", Node( "war" ) );
	}
	else if ( name == "unqueue" )
	{
		const ETFMatchGroup eGroup = TFGCStatus()->GetQueuedMatchGroup();
		if ( GTFPartyClient() && eGroup != k_eTFMatchGroup_Invalid )
			GTFPartyClient()->CancelMatchQueueRequest( eGroup );
	}
	else if ( name == "accept" )
		GTFGCClientSystem()->JoinMMMatch();
	else if ( name == "decline" )
		GTFGCClientSystem()->AbandonCurrentMatch( false );
	else if ( name == "select_mode" )
	{
		const fui::Node *pModes = ui.Get( "modes" );
		for ( size_t i = 0; pModes && i < pModes->Size(); ++i )
		{
			const fui::Node &mode = *pModes->At( i );
			if ( mode.Find( "id" )->AsString() == arg && mode.Find( "enabled" )->AsBool() )
				ui.Set( "ui.mode", fui::Node( arg ) );
		}
	}
	else if ( name == "toggle_map" && GTFPartyClient() )
	{
		const MapDef_t *pMap = GetItemSchema()->GetMasterMapDefByName( arg.c_str() );
		if ( pMap )
		{
			CTFGroupMatchCriteria &criteria = GTFPartyClient()->MutLocalGroupCriteria();
			criteria.SetCasualMapSelected( pMap->m_nDefIndex, !criteria.IsCasualMapSelected( pMap->m_nDefIndex ) );
			d.RefreshModes();
		}
	}
	else if ( name == "locker_class" || name == "lobby_class" )
	{
		if ( arg != ClassID( ClassIndex( arg ) ) )
			return;
		ui.Set( "ui.class", Node( arg ) );
		ui.Set( "ui.pick", Node( -1 ) );
		d.RefreshPlayerAndParty();
		if ( name == "locker_class" )
			d.RefreshLocker();
	}
	else if ( name == "locker_slot" )
	{
		ui.Set( "ui.slot", fui::Node( arg ) );
		ui.Set( "ui.pick", fui::Node( -1 ) );
		d.RefreshLocker();
	}
	else if ( name == "locker_filter" )
	{
		ui.Set( "locker.filter", fui::Node( arg ) );
		d.RefreshLocker();
	}
	else if ( name == "locker_pick" )
	{
		ui.Set( "ui.pick", fui::Node( atoi( arg.c_str() ) ) );
		d.RefreshLocker();
	}
	else if ( name == "equip" )
	{
		const int index = atoi( arg.c_str() );
		if ( d.lockerItems.IsValidIndex( index ) && TFInventoryManager() )
		{
			const Slot_t &slot = SlotByID( d.Str( "ui.slot" ) );
			if ( !TFInventoryManager()->EquipItemInLoadout( ClassIndex( d.Str( "ui.class" ) ), slot.iPosition, d.lockerItems[ index ] ) )
			{
				d.Toast( "error", ui.Translate( "fui_err_title" ), ui.Translate( "fui_locker_equip_failed" ), "" );
				return;
			}
			ui.Set( "ui.pick", Node( index ) );
			d.RefreshLocker();
		}
	}
	else if ( name == "invite" )
	{
		FuiSocialCommand( ui, "open_invites", {} );
	}
	else if ( name == "settings" )
		d.OpenClassic( "options" );
	else if ( name == "classic" )
		d.OpenClassic( arg );
	else if ( name == "open_settings" )
	{
		d.RefreshSettings();
		ui.Set( "ui.modal", Node( "settings" ) );
	}
	else if ( name == "toggle_menu_3d" )
	{
		static ConVarRef fui_menu_3d( "fui_menu_3d" );
		if ( fui_menu_3d.IsValid() )
		{
			fui_menu_3d.SetValue( !fui_menu_3d.GetBool() );
			ui.Set( "settings.menu_3d", Node( fui_menu_3d.GetBool() ) );
		}
	}
	else if ( name == "hud_style" )
	{
		std::string style;
		for ( char c : arg )
		{
			if ( isalnum( (unsigned char)c ) || c == '_' || c == '-' )
				style += c;
		}
		if ( !style.empty() )
		{
			engine->ClientCmd_Unrestricted( ( "fui_hud_style " + style + "\n" ).c_str() );
			ui.Set( "settings.hud_style", Node( style ) );
		}
	}
	else if ( name == "quit" )
		engine->ClientCmd_Unrestricted( "quit\n" );
	// In a match.
	else if ( name == "resume" )
		engine->ClientCmd_Unrestricted( "gameui_hide\n" );
	else if ( name == "disconnect" )
	{
		const CTFGSLobby *pLobby = GTFGCClientSystem()->GetLobby();
		ui.Set( "ui.leave_lobby", Node( pLobby ? std::to_string( pLobby->GetGroupID() ) : "" ) );
		ui.Set( "ui.modal", Node( "leave_match" ) );
	}
	else if ( name == "confirm_disconnect" )
	{
		const CTFGSLobby *pLobby = GTFGCClientSystem()->GetLobby();
		const std::string lobby = pLobby ? std::to_string( pLobby->GetGroupID() ) : "";
		ui.Set( "ui.modal", Node( "" ) );
		if ( lobby != d.Str( "ui.leave_lobby" ) )
			return;
		if ( pLobby )
			GTFGCClientSystem()->AbandonCurrentMatch();
		else
			engine->DisconnectInternal();
	}
	else if ( name == "changeteam" )
	{
		engine->ClientCmd_Unrestricted( "gameui_hide\n" );
		engine->ClientCmd( "changeteam\n" );
	}
	else if ( name == "changeclass" )
	{
		engine->ClientCmd_Unrestricted( "gameui_hide\n" );
		C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
		ShowClassSelect( pLocal ? pLocal->GetTeamNumber() : TF_TEAM_RED );
	}
	else if ( name == "pick_class" )
	{
		// joinclass takes TF2's own names.
		const std::string cls = arg == "heavy" ? std::string( "heavyweapons" ) : arg == "random" ? std::string( "random" ) : arg;
		engine->ClientCmd( CFmtStr( "joinclass %s\n", cls.c_str() ) );
		if ( arg != "random" )
			ui.Set( "ui.class", fui::Node( arg ) );
		FrontressUISetClassSelect( false );
	}
	else if ( name == "close_classes" )
		FrontressUISetClassSelect( false );
	else
		DevMsg( "[fui] unknown command '%s'\n", name.c_str() );
}

bool CFuiTF::ShowClassSelect( int iTeam )
{
	if ( !m_pImpl || !FrontressUISetClassSelect( true ) )
		return false;
	fui::Runtime &ui = *m_pImpl->pRuntime;
	ui.Set( "classsel.team", fui::Node( iTeam == TF_TEAM_BLUE ? "blu" : "red" ) );
	C_TFPlayer *pLocal = C_TFPlayer::GetLocalTFPlayer();
	const int iClass = ( pLocal && pLocal->GetPlayerClass() ) ? pLocal->GetPlayerClass()->GetClassIndex() : TF_CLASS_UNDEFINED;
	const std::string current = iClass > TF_CLASS_UNDEFINED && iClass < TF_LAST_NORMAL_CLASS ? ClassID( iClass ) : std::string();
	ui.Set( "classsel.current", fui::Node( current ) );
	ui.Set( "ui.class_hover", fui::Node( current.empty() ? m_pImpl->Str( "ui.class" ) : current ) );
	return true;
}

CFuiTF &FuiTF()
{
	static CFuiTF s_FuiTF;
	return s_FuiTF;
}
