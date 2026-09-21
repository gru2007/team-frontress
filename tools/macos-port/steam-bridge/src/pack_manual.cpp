/* Packing converters that cannot be generated.
 *
 * servernetadr_t and gameserveritem_t keep members private, so a field-by-field
 * copy is not available to generated code.  Both expose accessors for exactly
 * those members, which is what these use.
 */

#include "bridge_pe.h"

void steam_bridge_u2w_servernetadr_t( const struct u_servernetadr_t *u, servernetadr_t *w )
{
    w->Init( u->m_unIP, u->m_usQueryPort, u->m_usConnectionPort );
}

void steam_bridge_w2u_servernetadr_t( const servernetadr_t *w, struct u_servernetadr_t *u )
{
    u->m_unIP = w->GetIP();
    u->m_usQueryPort = w->GetQueryPort();
    u->m_usConnectionPort = w->GetConnectionPort();
}

void steam_bridge_u2w_gameserveritem_t( const struct u_gameserveritem_t *u, gameserveritem_t *w )
{
    steam_bridge_u2w_servernetadr_t( &u->m_NetAdr, &w->m_NetAdr );
    w->m_nPing = u->m_nPing;
    w->m_bHadSuccessfulResponse = u->m_bHadSuccessfulResponse;
    w->m_bDoNotRefresh = u->m_bDoNotRefresh;
    memcpy( w->m_szGameDir, u->m_szGameDir, sizeof(w->m_szGameDir) );
    memcpy( w->m_szMap, u->m_szMap, sizeof(w->m_szMap) );
    memcpy( w->m_szGameDescription, u->m_szGameDescription, sizeof(w->m_szGameDescription) );
    w->m_nAppID = u->m_nAppID;
    w->m_nPlayers = u->m_nPlayers;
    w->m_nMaxPlayers = u->m_nMaxPlayers;
    w->m_nBotPlayers = u->m_nBotPlayers;
    w->m_bPassword = u->m_bPassword;
    w->m_bSecure = u->m_bSecure;
    w->m_ulTimeLastPlayed = u->m_ulTimeLastPlayed;
    w->m_nServerVersion = u->m_nServerVersion;
    w->SetName( u->m_szServerName );
    memcpy( w->m_szGameTags, u->m_szGameTags, sizeof(w->m_szGameTags) );
    w->m_steamID = u->m_steamID;
}

void steam_bridge_w2u_gameserveritem_t( const gameserveritem_t *w, struct u_gameserveritem_t *u )
{
    steam_bridge_w2u_servernetadr_t( &w->m_NetAdr, &u->m_NetAdr );
    u->m_nPing = w->m_nPing;
    u->m_bHadSuccessfulResponse = w->m_bHadSuccessfulResponse;
    u->m_bDoNotRefresh = w->m_bDoNotRefresh;
    memcpy( u->m_szGameDir, w->m_szGameDir, sizeof(u->m_szGameDir) );
    memcpy( u->m_szMap, w->m_szMap, sizeof(u->m_szMap) );
    memcpy( u->m_szGameDescription, w->m_szGameDescription, sizeof(u->m_szGameDescription) );
    u->m_nAppID = w->m_nAppID;
    u->m_nPlayers = w->m_nPlayers;
    u->m_nMaxPlayers = w->m_nMaxPlayers;
    u->m_nBotPlayers = w->m_nBotPlayers;
    u->m_bPassword = w->m_bPassword;
    u->m_bSecure = w->m_bSecure;
    u->m_ulTimeLastPlayed = w->m_ulTimeLastPlayed;
    u->m_nServerVersion = w->m_nServerVersion;

    const char *name = w->GetName();
    strncpy( u->m_szServerName, name ? name : "", sizeof(u->m_szServerName) - 1 );
    u->m_szServerName[sizeof(u->m_szServerName) - 1] = 0;

    memcpy( u->m_szGameTags, w->m_szGameTags, sizeof(u->m_szGameTags) );
    u->m_steamID = w->m_steamID;
}
