//========= Copyright Team Frontress, All rights reserved. ====================//
//
// Purpose: The wire protocol between the game server and the game client for
//			Easy Anti-Cheat (EOS Anti-Cheat, client-server mode).
//
// EAC does not ship its own networking. Its client and server halves hand
// opaque messages to the game, and the game delivers them over its own
// connection. In Source that connection is:
//
//   server -> client   the "FrontressAC" user message, reliable;
//   client -> server   a "FrontressAC" KeyValues command (ServerCmdKeyValues),
//                      which is reliable as well.
//
// A user message carries at most 255 bytes and an EAC message up to 512, so
// both directions split payloads into numbered chunks and reassemble them.
// Everything a client sends is untrusted: the server bounds every size and
// drops anything that does not fit the protocol.
//
// The handshake, once the client is fully in game (ClientActive):
//
//   server  HELLO                      "this server is protected"
//   client  TOKEN (EOS ID token, JWT)  "this is my EOS Product User ID"
//   server  verifies the token belongs to the player's Steam account,
//           registers the client with EAC; from then on
//   both    DATA                       opaque EAC traffic
//   client  UNAVAILABLE                "I cannot run anti-cheat, here is why"
//
// This header compiles in every build, with or without the EOS SDK: a client
// built without anti-cheat still has to tell a protected server so, rather
// than leave it waiting for a token that never comes.
//
//=============================================================================//

#ifndef FRONTRESS_ANTICHEAT_SHARED_H
#define FRONTRESS_ANTICHEAT_SHARED_H
#ifdef _WIN32
#pragma once
#endif

#include "utlvector.h"
#include "utlstring.h"

#define FRONTRESS_AC_USERMSG		"FrontressAC"
#define FRONTRESS_AC_KV_NAME		"FrontressAC"

// Bumped when either side changes in a way the other must know about.
#define FRONTRESS_AC_PROTOCOL		1

enum EFrontressACServerMsg
{
	k_EFrontressACServerMsg_Hello = 1,	// byte protocol, byte flags
	k_EFrontressACServerMsg_Data = 2,	// short seq, byte index, byte count, byte len, bytes
};

// HELLO flags.
#define FRONTRESS_AC_HELLO_ENFORCED		( 1 << 0 )	// unprotected clients are removed

enum EFrontressACClientMsg
{
	k_EFrontressACClientMsg_Token = 1,			// chunked "<platform>\n<product user id>\n<EOS ID token JWT>"
	k_EFrontressACClientMsg_Data = 2,			// chunked EAC message
	k_EFrontressACClientMsg_Unavailable = 3,	// "r": one of the reasons below
};

// Why a client cannot take part in a protected session. Sent as a string so
// the server log and the GC record read on their own.
#define FRONTRESS_AC_UNAVAILABLE_NOT_BUILT		"client_built_without_anticheat"
#define FRONTRESS_AC_UNAVAILABLE_NOT_PROTECTED	"client_not_started_through_bootstrapper"
#define FRONTRESS_AC_UNAVAILABLE_LOGIN_FAILED	"eos_login_failed"
#define FRONTRESS_AC_UNAVAILABLE_SESSION_FAILED	"anticheat_session_failed"

// EAC's own upper bound is EOS_ANTICHEAT*_MAX_MESSAGE_SIZE (512). These are
// what the receiving side accepts, with room for an SDK that raises it.
#define FRONTRESS_AC_MAX_DATA_BYTES			2048
#define FRONTRESS_AC_MAX_TOKEN_BYTES		8192

// Server -> client: a user message is at most 255 bytes; 6 go to the header.
#define FRONTRESS_AC_USERMSG_CHUNK_BYTES	240
// Client -> server: base64 text per KeyValues command.
#define FRONTRESS_AC_KV_CHUNK_BYTES			600
#define FRONTRESS_AC_MAX_CHUNKS				32

//-----------------------------------------------------------------------------
// Base64, so binary EAC messages can travel as KeyValues strings.
//-----------------------------------------------------------------------------
inline void FrontressAC_Base64Encode( const uint8 *pData, int cubData, CUtlString &out )
{
	static const char s_rgchAlphabet[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
	CUtlVector< char > buf;
	buf.EnsureCapacity( ( ( cubData + 2 ) / 3 ) * 4 + 1 );
	for ( int i = 0; i < cubData; i += 3 )
	{
		const uint32 n = ( (uint32)pData[i] << 16 ) |
		                 ( i + 1 < cubData ? (uint32)pData[i + 1] << 8 : 0 ) |
		                 ( i + 2 < cubData ? (uint32)pData[i + 2] : 0 );
		buf.AddToTail( s_rgchAlphabet[ ( n >> 18 ) & 63 ] );
		buf.AddToTail( s_rgchAlphabet[ ( n >> 12 ) & 63 ] );
		buf.AddToTail( i + 1 < cubData ? s_rgchAlphabet[ ( n >> 6 ) & 63 ] : '=' );
		buf.AddToTail( i + 2 < cubData ? s_rgchAlphabet[ n & 63 ] : '=' );
	}
	buf.AddToTail( '\0' );
	out = buf.Base();
}

// Appends the decoded bytes to out. False on anything that is not base64.
inline bool FrontressAC_Base64Decode( const char *psz, CUtlVector< uint8 > &out )
{
	if ( !psz )
		return false;
	uint32 accum = 0;
	int bits = 0;
	for ( const char *p = psz; *p; ++p )
	{
		const char c = *p;
		int v;
		if ( c >= 'A' && c <= 'Z' ) v = c - 'A';
		else if ( c >= 'a' && c <= 'z' ) v = c - 'a' + 26;
		else if ( c >= '0' && c <= '9' ) v = c - '0' + 52;
		else if ( c == '+' ) v = 62;
		else if ( c == '/' ) v = 63;
		else if ( c == '=' ) break;
		else return false;
		accum = ( accum << 6 ) | (uint32)v;
		bits += 6;
		if ( bits >= 8 )
		{
			bits -= 8;
			out.AddToTail( (uint8)( ( accum >> bits ) & 0xFF ) );
		}
	}
	return true;
}

//-----------------------------------------------------------------------------
// Reassembles one chunked payload at a time. Chunks of a payload arrive in
// order on a reliable channel, so anything out of order is a protocol error
// (or a hostile client) and resets the buffer.
//-----------------------------------------------------------------------------
class CFrontressACReassembly
{
public:
	CFrontressACReassembly() : m_nSeq( -1 ), m_nNext( 0 ), m_nCount( 0 ) {}

	void Reset() { m_nSeq = -1; m_nNext = 0; m_nCount = 0; m_buf.RemoveAll(); }

	enum EResult { k_EIncomplete, k_EComplete, k_EError };

	EResult Add( int nSeq, int nIndex, int nCount, const uint8 *pData, int cubData, int cubMax )
	{
		if ( nCount <= 0 || nCount > FRONTRESS_AC_MAX_CHUNKS || nIndex < 0 || nIndex >= nCount || cubData < 0 )
		{
			Reset();
			return k_EError;
		}
		if ( nIndex == 0 )
		{
			Reset();
			m_nSeq = nSeq;
			m_nCount = nCount;
		}
		else if ( nSeq != m_nSeq || nIndex != m_nNext || nCount != m_nCount )
		{
			Reset();
			return k_EError;
		}
		if ( m_buf.Count() + cubData > cubMax )
		{
			Reset();
			return k_EError;
		}
		m_buf.AddMultipleToTail( cubData, pData );
		m_nNext = nIndex + 1;
		return m_nNext == m_nCount ? k_EComplete : k_EIncomplete;
	}

	const CUtlVector< uint8 > &Data() const { return m_buf; }

private:
	int m_nSeq;
	int m_nNext;
	int m_nCount;
	CUtlVector< uint8 > m_buf;
};

#endif // FRONTRESS_ANTICHEAT_SHARED_H
