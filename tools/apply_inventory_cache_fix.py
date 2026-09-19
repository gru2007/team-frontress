#!/usr/bin/env python3
"""One-time, fail-closed patch for the Valve inventory / local matchmaking SO cache race.

Run from repository root. It requires the exact reviewed main-branch anchors and
will fail rather than silently modifying a different revision.
"""
from pathlib import Path
import sys

ROOT = Path.cwd()
paths = {
    'gc_h': ROOT / 'src/game/client/tf/tf_gc_client.h',
    'gc_cpp': ROOT / 'src/game/client/tf/tf_gc_client.cpp',
    'mm_h': ROOT / 'src/game/client/tf/frontress/tf_mm_backend.h',
    'mm_cpp': ROOT / 'src/game/client/tf/frontress/tf_mm_backend.cpp',
    'econ': ROOT / 'src/game/shared/econ/econ_item_inventory.cpp',
}
files = {key: p.read_text(encoding='utf-8') for key, p in paths.items()}


def replace(key, before, after, count=1):
    found = files[key].count(before)
    if found != count:
        raise RuntimeError(f'{paths[key]}: expected {count} occurrences, got {found}: {before[:110]!r}')
    files[key] = files[key].replace(before, after)


def insert_once(key, before, insertion):
    replace(key, before, before + insertion)


if '--verify' not in sys.argv:
    # Preserve Valve's SDK inventory implementation and expose its provenance.
    insert_once('gc_h', '\tvoid LocalInventoryChanged();\n',
        '\n\t// True only after ISDK/GetInventory supplied a genuine inventory subscription.\n'
        '\t// A matchmaking-only SO cache must never make the backpack appear ready.\n'
        '\tbool BValveInventoryReady() const { return m_WebapiInventory.m_bValveInventoryReady; }\n')
    insert_once('gc_h', '\t\tHTTPRequestHandle m_hInventoryRequest = INVALID_HTTPREQUEST_HANDLE;\n',
        '\t\tbool m_bValveInventoryReady = false; // not equivalent to BIsSubscribed()\n')

    # WebAPI owns initial subscription. Matchmaking may bootstrap only after a grace period.
    insert_once('mm_h', '\tbool       m_bSubscribedToCache;\n',
        '\tfloat      m_flInventoryBootstrapStart; // monotonic time, -1 until first attempt\n')
    replace('mm_cpp', '\t, m_bSubscribedToCache( false )\n',
        '\t, m_bSubscribedToCache( false )\n\t, m_flInventoryBootstrapStart( -1.0f )\n')
    replace('mm_cpp',
        '\tm_coordinator.Cancel();\n\n\tif ( m_bSubscribedToCache )',
        '\tm_coordinator.Cancel();\n\tm_flInventoryBootstrapStart = -1.0f;\n\n\tif ( m_bSubscribedToCache )')
    replace('mm_cpp',
        '\t// Something else -- the web-API inventory fetch, most likely -- just\n'
        '\t// replaced the contents of our cache. Anything we had published is gone,\n'
        '\t// so publish it again rather than leaving the UI looking at nothing.\n'
        '\tMMDbg( "local SO cache (re)subscribed, republishing party and lobby\\n" );\n'
        '\tm_bPartyPublished = false;\n'
        '\tm_bLobbyPublished = false;\n'
        '\tPublishParty();\n'
        '\tif ( m_eState == k_eTFMMState_MatchReady || m_eState == k_eTFMMState_Connecting ||\n'
        '\t     m_eState == k_eTFMMState_InMatch )\n'
        '\t{\n'
        '\t\tPublishLobby();\n'
        '\t}\n',
        '\t// AddLocalSOCache can call listeners while replacing the entire cache.\n'
        '\t// Defer writes until Update() to avoid mutating it during that callback.\n'
        '\tMMDbg( "local SO cache (re)subscribed; republishing MM objects next frame\\n" );\n'
        '\tm_bPartyPublished = false;\n'
        '\tm_bLobbyPublished = false;\n'
        '\tm_vecPublishedRatingTypes.RemoveAll();\n')
    replace('mm_cpp',
        '\t\t// Everything downstream reads the party object, so it has to exist\n'
        '\t\t// before the first frame of UI does.\n'
        '\t\tPublishParty();\n',
        '\t\t// The Valve inventory normally subscribes the cache. Publication\n'
        '\t\t// is retried below; after a grace period MM may bootstrap locally.\n')
    insert_once('mm_cpp',
        '\t// The party object tracks the Steam lobby, which changes underneath us.\n',
        '\t// Retry initial publication even for solo parties, and restore MM objects\n'
        '\t// after Valve replaces our optional fallback cache.\n'
        '\tif ( !m_bPartyPublished )\n'
        '\t\tPublishParty();\n'
        '\tif ( !m_bLobbyPublished &&\n'
        '\t     ( m_eState == k_eTFMMState_MatchReady || m_eState == k_eTFMMState_Connecting ||\n'
        '\t       m_eState == k_eTFMMState_InMatch ) )\n'
        '\t\tPublishLobby();\n'
        '\tif ( m_progress.bValid && m_vecPublishedRatingTypes.Count() == 0 )\n'
        '\t\tPublishRatings();\n\n')
    replace('mm_cpp',
        '\tGCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( false );\n'
        '\tif ( pCache && pCache->BIsSubscribed() )\n'
        '\t\treturn true;\n\n'
        '\tconst CSteamID steamID = LocalSteamID();',
        '\tGCSDK::CGCClientSharedObjectCache *pCache = GetLocalCache( false );\n'
        '\tif ( pCache && pCache->BIsSubscribed() )\n'
        '\t\treturn true;\n\n'
        '\t// Once a genuine Valve inventory was loaded, never synthesize a new\n'
        '\t// subscription for it after a temporary cache unsubscribe.\n'
        '\tif ( GTFGCClientSystem()->BValveInventoryReady() )\n'
        '\t\treturn false;\n\n'
        '\t// Never pre-empt a normal Valve inventory response. If the WebAPI is\n'
        '\t// unavailable, bootstrap MM independently after a short grace period.\n'
        '\tif ( m_flInventoryBootstrapStart < 0.0f )\n'
        '\t\tm_flInventoryBootstrapStart = Plat_FloatTime();\n'
        '\tif ( Plat_FloatTime() - m_flInventoryBootstrapStart < 10.0f )\n'
        '\t\treturn false;\n\n'
        '\tconst CSteamID steamID = LocalSteamID();')
    replace('mm_cpp',
        '\tMMDbg( "subscribed the local SO cache ourselves\\n" );',
        '\tMMDbg( "Valve inventory not available after grace period; bootstrapped MM-only SO cache\\n" );')

    # A locally bootstrapped MM cache must never supply a WebAPI conditional version.
    replace('gc_cpp',
        '\t\t// If we already have an so cache for this user, include its version so we don\'t send the whole cache if it\'s unchanged\n'
        '\t\tCGCClientSharedObjectCache* pExistingSOCache = GetSOCache( SteamUser()->GetSteamID() );\n'
        '\t\tif( pExistingSOCache && pExistingSOCache->BIsSubscribed() )',
        '\t\t// A locally bootstrapped MM cache is NOT a Valve inventory. Only\n'
        '\t\t// send conditional versions after accepting a real WebAPI payload.\n'
        '\t\tCGCClientSharedObjectCache* pExistingSOCache = GetSOCache( SteamUser()->GetSteamID() );\n'
        '\t\tif ( state.m_bValveInventoryReady && pExistingSOCache && pExistingSOCache->BIsSubscribed() )')
    replace('gc_cpp',
        '\t\tif ( !SteamHTTP()->SendHTTPRequest( state.m_hInventoryRequest, &callResult ) )\n'
        '\t\t{\n'
        '\t\t\tDevWarning("Steam inventory request failed.\\n");\n'
        '\t\t\tstate.Backoff();\n'
        '\t\t\treturn;\n'
        '\t\t}',
        '\t\tif ( !SteamHTTP()->SendHTTPRequest( state.m_hInventoryRequest, &callResult ) )\n'
        '\t\t{\n'
        '\t\t\tDevWarning("[inventory] SendHTTPRequest failed; retrying.\\n");\n'
        '\t\t\tSteamHTTP()->ReleaseHTTPRequest( state.m_hInventoryRequest );\n'
        '\t\t\tstate.m_hInventoryRequest = INVALID_HTTPREQUEST_HANDLE;\n'
        '\t\t\tstate.Backoff();\n'
        '\t\t\treturn;\n'
        '\t\t}')
    replace('gc_cpp',
        '\tif ( bIOFailure || !pInfo )\n'
        '\t{\n'
        '\t\tAssert( false );\n\n'
        '\t\t// Failed to communicate with steam\n',
        '\tif ( bIOFailure || !pInfo )\n'
        '\t{\n'
        '\t\tWarning( "[inventory] WebAPI transport failure; retrying.\\n" );\n'
        '\t\tstate.Backoff();\n'
        '\t\tstate.m_eState = kWebapiInventoryState_RequestInventory;\n\n'
        '\t\t// Failed to communicate with steam\n')
    replace('gc_cpp',
        '\t\tDevWarning("Steam inventory request failed.\\n");\n'
        '\t\tSteamHTTP()->ReleaseHTTPRequest( pInfo->m_hRequest );',
        '\t\tWarning( "[inventory] WebAPI HTTP %d (request successful=%d); retrying.\\n",\n'
        '\t\t         (int)pInfo->m_eStatusCode, (int)pInfo->m_bRequestSuccessful );\n'
        '\t\tSteamHTTP()->ReleaseHTTPRequest( pInfo->m_hRequest );')
    replace('gc_cpp',
        '\tif ( !userSteamID.IsValid() || userSteamID.GetEAccountType() != k_EAccountTypeIndividual || userSteamID.GetEUniverse() != GetUniverse() )',
        '\tif ( !userSteamID.IsValid() || userSteamID != SteamUser()->GetSteamID() ||\n'
        '\t     userSteamID.GetEAccountType() != k_EAccountTypeIndividual || userSteamID.GetEUniverse() != GetUniverse() )')
    replace('gc_cpp',
        '\t\tCGCClientSharedObjectCache *pSOCache = GetGCClient()->AddLocalSOCache( userSteamID, bufMsgSubscription.Base(), bufMsgSubscription.TellPut() );\n'
        '\t\tif ( !pSOCache )\n'
        '\t\t{\n'
        '\t\t\tWarning( "Inventory response failed to create SO cache (probably protobuf didn\'t parse)\\n" );\n'
        '\t\t\treturn;\n'
        '\t\t}\n\n'
        '\t\t// Version should match the one they said we have\n'
        '\t\tAssert( pSOCache->GetVersion() == pValues->GetChildUInt64Value( "version" ) );',
        '\t\t// AddLocalSOCache notifies listeners synchronously: mark the source\n'
        '\t\t// before the callback so CPlayerInventory can distinguish this\n'
        '\t\t// subscription from a synthetic matchmaking-only one.\n'
        '\t\tconst bool bWasReady = state.m_bValveInventoryReady;\n'
        '\t\tstate.m_bValveInventoryReady = true;\n'
        '\t\tCGCClientSharedObjectCache *pSOCache = GetGCClient()->AddLocalSOCache( userSteamID, bufMsgSubscription.Base(), bufMsgSubscription.TellPut() );\n'
        '\t\tif ( !pSOCache || !pSOCache->BIsSubscribed() ||\n'
        '\t\t     pSOCache->GetVersion() != pValues->GetChildUInt64Value( "version" ) )\n'
        '\t\t{\n'
        '\t\t\tstate.m_bValveInventoryReady = bWasReady;\n'
        '\t\t\tWarning( "[inventory] Valve SO cache failed to load or version mismatched; retrying.\\n" );\n'
        '\t\t\treturn;\n'
        '\t\t}\n'
        '\t\tMsg( "[inventory] Valve SO cache loaded, version %llu.\\n",\n'
        '\t\t     (unsigned long long)pSOCache->GetVersion() );')
    replace('gc_cpp',
        '\t\t// Cache up to date.  Validate version matches\n'
        '\t\tCGCClientSharedObjectCache* pSOCache = GetGCClient()->FindSOCache( userSteamID, false );\n'
        '\t\tAssert( pSOCache );\n'
        '\t\tif( pSOCache )\n'
        '\t\t{\n'
        '\t\t\tAssert( pSOCache->GetVersion() == pValues->GetChildUInt64Value( "version" ) );\n'
        '\t\t}',
        '\t\t// A version-only response is meaningful only for a *previously*\n'
        '\t\t// authenticated inventory; never accept an MM bootstrap cache.\n'
        '\t\tCGCClientSharedObjectCache* pSOCache = GetGCClient()->FindSOCache( userSteamID, false );\n'
        '\t\tif ( !state.m_bValveInventoryReady || !pSOCache || !pSOCache->BIsSubscribed() ||\n'
        '\t\t     pSOCache->GetVersion() != pValues->GetChildUInt64Value( "version" ) )\n'
        '\t\t{\n'
        '\t\t\tWarning( "[inventory] Version-only reply without matching Valve inventory; retrying full fetch.\\n" );\n'
        '\t\t\tstate.m_bValveInventoryReady = false;\n'
        '\t\t\treturn;\n'
        '\t\t}')
    insert_once('gc_cpp',
        'CTFGCClientSystem *GTFGCClientSystem() { return &s_TFGCClientSystem; }\n',
        '\n// Inventory readiness is separate from locally emulated GC connectivity.\n'
        'bool BTFWebapiInventoryReady() { return GTFGCClientSystem()->BValveInventoryReady(); }\n')

    # Local MM-only SO cache events must not announce that Steam items arrived.
    insert_once('econ', 'using namespace GCSDK;\n',
        '\n#ifdef TF_CLIENT_DLL\n'
        '// Implemented by tf_gc_client.cpp; never use the MM-only cache to signal\n'
        '// Steam inventory readiness. Other inventories and game servers unaffected.\n'
        'extern bool BTFWebapiInventoryReady();\n'
        '#endif\n')
    replace('econ',
        'void CPlayerInventory::SOCacheSubscribed( const CSteamID & steamIDOwner, GCSDK::ESOCacheEvent eEvent )\n'
        '{\n'
        '\t// Make sure we expect notifications about this guy\n'
        '\tAssert( steamIDOwner == m_OwnerID );\n'
        '\tif ( steamIDOwner != m_OwnerID )\n'
        '\t\treturn;\n',
        'void CPlayerInventory::SOCacheSubscribed( const CSteamID & steamIDOwner, GCSDK::ESOCacheEvent eEvent )\n'
        '{\n'
        '\t// Make sure we expect notifications about this guy\n'
        '\tAssert( steamIDOwner == m_OwnerID );\n'
        '\tif ( steamIDOwner != m_OwnerID )\n'
        '\t\treturn;\n\n'
        '#ifdef TF_CLIENT_DLL\n'
        '\t// The MM backend may subscribe a party-only cache if Valve is down.\n'
        '\t// Do not purge items or dispatch econ_inventory_connected for it.\n'
        '\tif ( InventoryManager()->GetLocalInventory() == this && !BTFWebapiInventoryReady() )\n'
        '\t\treturn;\n'
        '#endif\n')

    for key, file_path in paths.items():
        file_path.write_text(files[key], encoding='utf-8')
    print('Applied Valve inventory / MM cache changes to', len(paths), 'files')

# Verify source-level invariants on the actual patched checkout.
files = {key: p.read_text(encoding='utf-8') for key, p in paths.items()}
assert 'GetAuthTicketForWebApi( "tf2sdk" )' in files['gc_cpp']
assert 'webapi/ISDK/GetInventory/v0001' in files['gc_cpp']
assert 'state.m_bValveInventoryReady && pExistingSOCache && pExistingSOCache->BIsSubscribed()' in files['gc_cpp']
assert 'state.m_bValveInventoryReady = true;\n\t\tCGCClientSharedObjectCache *pSOCache = GetGCClient()->AddLocalSOCache' in files['gc_cpp']
assert 'state.m_eState = kWebapiInventoryState_RequestInventory;' in files['gc_cpp'].split('if ( bIOFailure || !pInfo )', 1)[1].split('if ( pInfo->m_hRequest !=', 1)[0]
assert 'if ( Plat_FloatTime() - m_flInventoryBootstrapStart < 10.0f )' in files['mm_cpp']
assert 'm_vecPublishedRatingTypes.RemoveAll();' in files['mm_cpp']
assert 'if ( InventoryManager()->GetLocalInventory() == this && !BTFWebapiInventoryReady() )' in files['econ']
assert 'CInventoryManager::SendItemSystemConnectedEvent();' in files['econ']
assert 'if ( !m_bPartyPublished )\n\t\tPublishParty();' in files['mm_cpp']
assert 'if ( !m_bLobbyPublished &&' in files['mm_cpp']
assert 'if ( m_progress.bValid && m_vecPublishedRatingTypes.Count() == 0 )' in files['mm_cpp']
print('PASS: 12 source-level invariants, SDK inventory pipeline preserved')
