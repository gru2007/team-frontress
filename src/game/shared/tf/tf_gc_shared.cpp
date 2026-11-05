//========= Copyright Valve Corporation, All rights reserved. ============//

#include "cbase.h"
#include "tf_gc_shared.h"
#include "gcsdk/webapi_response.h"

bool DecodeTFInventoryCache( const CWebAPIValues &msg, CUtlBuffer &buffer )
{
	// Valve sends the serialized SO cache as a base64 JSON string.
	if ( msg.GetType() == k_EWebAPIValueType_String )
	{
		CUtlString encoded;
		msg.GetStringValue( encoded );
		const uint32 cchEncoded = encoded.Length();
		if ( !cchEncoded )
			return false;

		buffer.EnsureCapacity( cchEncoded );
		uint32 cubDecoded = cchEncoded;
		if ( !Base64Decode( encoded.Get(), cchEncoded, (uint8 *)buffer.Base(), &cubDecoded, false ) )
			return false;

		buffer.SeekPut( CUtlBuffer::SEEK_HEAD, cubDecoded );
	}
	else if ( msg.GetType() == k_EWebAPIValueType_BinaryBlob )
	{
		msg.GetBinaryValue( buffer );
	}
	else
	{
		return false;
	}
	return buffer.TellPut() > 0;
}

IJobReliableMessage::BYldSendMessageAndGetReply_t IJobReliableMessage::SendAndWaitForReply(
	CProtoBufMsgBase &msg, CProtoBufMsgBase &reply, MsgType_t eReply )
{
	msg.ExpectingReply( GetJobID() );
	if ( !GCClientSystem()->BSendMessage( msg ) )
		return BYLDREPLY_SEND_FAILED;

	SetJobTimeout( k_ReliableMsg_unReplyTimeoutSecs );
	IMsgNetPacket *pPacket = nullptr;
	if ( !BYieldingWaitForMsg( &pPacket ) )
		return BYLDREPLY_TIMEOUT;

	reply.InitFromPacket( pPacket );
	return reply.GetEMsg() == eReply ? BYLDREPLY_SUCCESS : BYLDREPLY_MSG_TYPE_MISMATCH;
}

//-----------------------------------------------------------------------------
void CReliableMessageQueue::Enqueue( IJobReliableMessage *pReliable )
{
	pReliable->OnAddedToQueue( this );

	if ( !m_pCurrentConfirmJob )
	{
		m_pCurrentConfirmJob = pReliable;
		pReliable->StartJobDelayed( nullptr );
	}
	else
	{
		// Queue, confirm jobs will kick next in queue as necessary
		m_queuePendingConfirmJobs.Insert( pReliable );
	}
}

//-----------------------------------------------------------------------------
int CReliableMessageQueue::NumPendingMessages() const
{
	Assert( !m_queuePendingConfirmJobs.Count() || m_pCurrentConfirmJob );
	return ( m_pCurrentConfirmJob ? 1 : 0 ) + m_queuePendingConfirmJobs.Count();
}

//-----------------------------------------------------------------------------
void CReliableMessageQueue::OnReliableMessageStalled( IJobReliableMessage *pReliable )
{
	if ( m_pCurrentConfirmJob != pReliable )
	{
		AssertMsg( m_pCurrentConfirmJob == pReliable, "OnReliableMessageStalled from non-current message, is bad" );
		return;
	}

	m_bCurrentMessageStalled = true;
}

//-----------------------------------------------------------------------------
void CReliableMessageQueue::OnReliableMessageComplete( IJobReliableMessage *pReliable )
{
	if ( m_pCurrentConfirmJob != pReliable )
	{
		AssertMsg( m_pCurrentConfirmJob == pReliable,
		           "OnReliableMessageComplete from a job that isn't head of queue, something very wrong" );
		return;
	}

	m_bCurrentMessageStalled = false;

	if ( m_queuePendingConfirmJobs.Count() )
	{
		// Kick off next job
		m_pCurrentConfirmJob = m_queuePendingConfirmJobs.RemoveAtHead();
		m_pCurrentConfirmJob->StartJob( nullptr );
	}
	else
	{
		m_pCurrentConfirmJob = nullptr;
	}
}

//-----------------------------------------------------------------------------
const IJobReliableMessage *CReliableMessageQueue::CurrentMessage() const
{
	return static_cast< const IJobReliableMessage *>( m_pCurrentConfirmJob );
}
