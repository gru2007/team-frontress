package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gru2007/team-frontress/services/coordinator/internal/gcparty"
	"github.com/gru2007/team-frontress/services/coordinator/internal/gcproto"
	"github.com/gru2007/team-frontress/services/coordinator/internal/steamauth"
	"github.com/gru2007/team-frontress/services/coordinator/internal/wire"
)

// gcMaxBody bounds one FGC1 batch. A client's own control messages (party
// invites, queue requests, chat) are a few hundred bytes each; a batch of
// them is nowhere near this.
const gcMaxBody = 256 << 10

// gcLongPollSecs is k_unLongPollSecs, confirmed from frontress_gc.h: the
// client holds one request open this long waiting for async pushes (match
// found, chat, invites) before it reopens a fresh one.
const gcLongPollSecs = 20 * time.Second

// resultOK/resultFail are Steam's own generic EResult values (k_EResultOK=1,
// k_EResultFail=2), not a TF2-specific enum -- these are stable across every
// Steamworks/GC integration and safe to rely on without deriving them from
// this game's sources specifically.
const (
	resultOK   int32 = 1
	resultFail int32 = 2
)

// handleGCSession is the coordinator's entire custom GC transport: one HTTP
// endpoint standing in for Steam's UDP/TCP CM connection. The client posts
// one FGC1 batch of outbound messages and receives one FGC1 batch back,
// long-polled so async pushes (match found, invites, chat) do not need a
// second channel.
//
// This is the only place Valve's own GC is replaced end to end -- everything
// it carries (party, lobby, matchmaking) is served by gcparty.Manager and
// internal/mm. The one real-Valve surface this coordinator keeps is Steam
// inventory retrieval, which never touches this endpoint at all: it stays a
// direct Steam Web API / GC econ call the client makes itself.
func (s *Server) handleGCSession(w http.ResponseWriter, r *http.Request) {
	if s.gcParty == nil {
		writeErr(w, http.StatusServiceUnavailable, "this coordinator does not run the GC transport")
		return
	}

	claimed := wire.SteamID(r.Header.Get("X-Frontress-SteamID"))
	if !steamauth.ValidSteamID(claimed) {
		writeErr(w, http.StatusBadRequest, "missing or malformed X-Frontress-SteamID")
		return
	}
	kind := r.Header.Get("X-Frontress-Kind")
	if kind == "" {
		kind = "client"
	}

	ticket, token := parseGCAuthorization(r.Header.Get("Authorization"))
	var steamID wire.SteamID
	switch kind {
	case "server":
		// A dedicated server speaks for itself, not for a Steam identity --
		// it authenticates with the pool's shared secret, the same one
		// /v1/gs/* already requires.
		if !s.serverSecretOK(token) {
			writeErr(w, http.StatusUnauthorized, "bad server secret")
			return
		}
		steamID = claimed
	case "client":
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		verified, err := s.verifier.Verify(ctx, claimed, ticket)
		cancel()
		if err != nil {
			if errors.Is(err, steamauth.ErrRejected) {
				s.log.Warn("gc: rejected session", "claimed", claimed, "err", err)
				writeErr(w, http.StatusUnauthorized, "steam did not accept that identity")
				return
			}
			s.log.Error("gc: identity check unavailable", "err", err)
			writeErr(w, http.StatusBadGateway, "identity check is unavailable")
			return
		}
		steamID = verified
	default:
		writeErr(w, http.StatusBadRequest, "X-Frontress-Kind must be client or server")
		return
	}

	id, err := strconv.ParseUint(string(steamID), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not a SteamID64")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, gcMaxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "batch too large")
		return
	}
	msgs, err := gcproto.DecodeBatch(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "malformed FGC1 batch: "+err.Error())
		return
	}

	var out [][]byte
	sawHello := false
	for _, raw := range msgs {
		parsed, perr := gcproto.ParseMessage(raw)
		if perr != nil {
			s.log.Warn("gc: dropping unparseable message", "steam_id", steamID, "err", perr)
			continue
		}
		if kind == "server" {
			out = append(out, s.dispatchGCServer(id, parsed)...)
			continue
		}
		if parsed.EMsg == gcproto.EMsgGCClientHello {
			sawHello = true
			continue
		}
		out = append(out, s.dispatchGCClient(id, parsed)...)
	}

	if sawHello {
		welcome := gcproto.BuildMessage(gcproto.EMsgGCClientWelcome, &gcproto.ProtoBufHeader{}, (&gcproto.ClientWelcome{Version: gcproto.U32(1)}).Marshal())
		name := ""
		if s.players != nil {
			name = s.players.Get(steamID).Name
		}
		snapshot := s.envelopeReplies(id, s.gcParty.OnConnect(id, name))
		out = append(out, welcome)
		out = append(out, snapshot...)
	}

	// Long-poll: hold the connection open for whatever is left of the
	// budget waiting for an async push (match found, invite, chat) rather
	// than making the client re-poll in a tight loop. A session that only
	// sent ClientHello/replies already has something to send back
	// immediately above, but there is no harm in also giving it a chance at
	// a push that landed in the same instant.
	pushes := s.gcParty.Drain(r.Context(), id, gcLongPollSecs)
	out = append(out, s.envelopeReplies(id, pushes)...)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if len(out) == 0 {
		_, _ = w.Write(gcproto.EmptyBatch())
		return
	}
	_, _ = w.Write(gcproto.EncodeBatch(out))
}

// parseGCAuthorization splits "Steam <hex ticket>" or "Token <secret>" into
// (ticket, token); at most one of the two return values is non-empty.
func parseGCAuthorization(header string) (ticket, token string) {
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok {
		return "", ""
	}
	switch scheme {
	case "Steam":
		return rest, ""
	case "Token":
		return "", rest
	default:
		return "", ""
	}
}

// envelopeReplies wraps every queued gcparty.Push in ProtoBufMsgHeader_t
// framing. These are all GC -> client pushes with no job to correlate, so
// the header carries nothing but what BuildMessage needs.
func (s *Server) envelopeReplies(steamID uint64, pushes []gcparty.Push) [][]byte {
	if len(pushes) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(pushes))
	for _, p := range pushes {
		out = append(out, gcproto.BuildMessage(p.EMsg, &gcproto.ProtoBufHeader{}, p.Body))
	}
	return out
}

// replyHeader carries job correlation back to the client when its request
// set JobIDSource: that is how the client's own request/response pairing
// works, mirroring CJobMgr on the real GC.
func replyHeader(req *gcproto.ProtoBufHeader, result int32) *gcproto.ProtoBufHeader {
	h := &gcproto.ProtoBufHeader{EResult: &result}
	if req != nil && req.JobIDSource != nil && *req.JobIDSource != gcproto.NoJobID {
		jid := *req.JobIDSource
		h.JobIDTarget = &jid
	}
	return h
}

// dispatchGCClient routes one inbound client->GC message to gcparty.Manager
// and frames whatever synchronous reply the real protocol defines. Messages
// with no confirmed response type in the real protocol are fire-and-forget:
// their effect (if any) arrives later as an async push instead.
func (s *Server) dispatchGCClient(steamID uint64, parsed *gcproto.ParsedMessage) [][]byte {
	m := s.gcParty
	switch parsed.EMsg {
	case gcproto.EMsgGCPartySetOptions:
		req, err := gcproto.UnmarshalPartySetOptions(parsed.Body)
		if err != nil {
			s.log.Warn("gc: bad PartySetOptions", "steam_id", steamID, "err", err)
			return nil
		}
		_, err = m.SetOptions(steamID, req)
		return oneReply(gcproto.EMsgGCPartySetOptionsResponse, parsed.Header, err, &gcproto.PartySetOptionsResponse{})

	case gcproto.EMsgGCPartyQueueForMatch:
		req, err := gcproto.UnmarshalPartyQueueForMatch(parsed.Body)
		if err != nil {
			s.log.Warn("gc: bad PartyQueueForMatch", "steam_id", steamID, "err", err)
			return nil
		}
		_, err = m.QueueForMatch(steamID, req)
		if err != nil {
			s.log.Warn("gc: QueueForMatch failed", "steam_id", steamID, "err", err)
		}
		return oneReply(gcproto.EMsgGCPartyQueueForMatchResponse, parsed.Header, err, &gcproto.PartyQueueForMatchResponse{})

	case gcproto.EMsgGCPartyRemoveFromQueue:
		req, err := gcproto.UnmarshalPartyRemoveFromQueue(parsed.Body)
		if err != nil {
			return nil
		}
		_, err = m.RemoveFromQueue(steamID, req)
		return oneReply(gcproto.EMsgGCPartyRemoveFromQueueResponse, parsed.Header, err, &gcproto.PartyRemoveFromQueueResponse{})

	case gcproto.EMsgGCExitMatchmaking:
		req, err := gcproto.UnmarshalExitMatchmaking(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.ExitMatchmaking(steamID, req)
		return nil

	case gcproto.EMsgGCPartyInvitePlayer:
		req, err := gcproto.UnmarshalPartyInvitePlayer(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.InvitePlayer(steamID, req)
		return nil

	case gcproto.EMsgGCPartyRequestJoinPlayer:
		req, err := gcproto.UnmarshalPartyRequestJoinPlayer(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.RequestJoinPlayer(steamID, req)
		return nil

	case gcproto.EMsgGCPartyClearPendingPlayer:
		// Declines/withdraws a pending invite or join-request. Accepting one
		// is not a separate EMsg in the real protocol: it happens when the
		// other side's own PartyInvitePlayer/PartyRequestJoinPlayer lands on
		// a pending entry that already names them, which gcparty.Manager
		// treats as mutual consent and merges immediately (see
		// Manager.InvitePlayer/RequestJoinPlayer).
		req, err := gcproto.UnmarshalPartyClearPendingPlayer(parsed.Body)
		if err != nil {
			return nil
		}
		_, err = m.ClearPendingPlayer(steamID, req)
		return oneReply(gcproto.EMsgGCPartyClearPendingPlayerResponse, parsed.Header, err, &gcproto.PartyClearPendingPlayerResponse{})

	case gcproto.EMsgGCPartyClearOtherPartyRequest:
		req, err := gcproto.UnmarshalPartyClearOtherPartyRequest(parsed.Body)
		if err != nil {
			return nil
		}
		_, err = m.ClearOtherPartyRequest(steamID, req)
		return oneReply(gcproto.EMsgGCPartyClearOtherPartyRequestResp, parsed.Header, err, &gcproto.PartyClearOtherPartyRequestResponse{})

	case gcproto.EMsgGCPartyPromoteToLeader:
		req, err := gcproto.UnmarshalPartyPromoteToLeader(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.PromoteToLeader(steamID, req)
		return nil

	case gcproto.EMsgGCPartyKickMember:
		req, err := gcproto.UnmarshalPartyKickMember(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.KickMember(steamID, req)
		return nil

	case gcproto.EMsgGCPartySendChat:
		req, err := gcproto.UnmarshalPartySendChat(parsed.Body)
		if err != nil {
			return nil
		}
		_ = m.SendChat(steamID, req)
		return nil

	case gcproto.EMsgGCAcceptLobbyInvite:
		req, err := gcproto.UnmarshalAcceptLobbyInvite(parsed.Body)
		if err != nil {
			return nil
		}
		_, err = m.AcceptLobbyInvite(steamID, req)
		return oneReply(gcproto.EMsgGCAcceptLobbyInviteReply, parsed.Header, err, &gcproto.AcceptLobbyInviteReply{})

	case gcproto.EMsgGCReadyUp:
		// CMsgReadyUp's body is entirely commented out in the real SDK
		// ("Lobby ready state not in use") -- there is no field layout to
		// unmarshal and no real response EMsg. This is an intentional no-op,
		// not a missing handler: acknowledge receipt in the log and drop it.
		s.log.Debug("gc: ReadyUp received (no-op, unused in real protocol)", "steam_id", steamID)
		return nil

	default:
		s.log.Debug("gc: no handler for client EMsg, ignoring", "emsg", parsed.EMsg, "steam_id", steamID)
		return nil
	}
}

// dispatchGCServer routes the small set of game-server -> GC messages this
// coordinator implements. A dedicated server is not a party member, so it
// gets no push queue of its own; kicks act on whichever party the lobby
// belongs to.
func (s *Server) dispatchGCServer(actingID uint64, parsed *gcproto.ParsedMessage) [][]byte {
	switch parsed.EMsg {
	case gcproto.EMsgGCGameServerKickingLobby:
		req, err := gcproto.UnmarshalGameServerKickingLobby(parsed.Body)
		if err != nil || req.LobbyID == nil {
			return nil
		}
		s.gcParty.KickLobby(*req.LobbyID)
		return oneReply(gcproto.EMsgGCGameServerKickingLobbyResponse, parsed.Header, nil, &gcproto.GameServerKickingLobbyResponse{})

	case gcproto.EMsgGCNewMatchForLobbyRequest:
		req, err := gcproto.UnmarshalNewMatchForLobbyRequest(parsed.Body)
		if err != nil {
			return nil
		}
		ok := s.gcParty.NewMatchForLobby(req)
		return oneReply(gcproto.EMsgGCNewMatchForLobbyResponse, parsed.Header, nil, &gcproto.NewMatchForLobbyResponse{Success: gcproto.Bl(ok)})

	case gcproto.EMsgGCProcessMatchVoteKick:
		req, err := gcproto.UnmarshalProcessMatchVoteKick(parsed.Body)
		if err != nil {
			return nil
		}
		rip := s.gcParty.ProcessVoteKick(req)
		// dispatchGCServer has no async outbox for servers (only clients get
		// one via Drain's long-poll), so both the response and, when the
		// kick is approved, the resulting KickPlayerFromLobby push have to
		// go back in this same HTTP round-trip rather than queued.
		out := []([]byte){gcproto.BuildMessage(gcproto.EMsgGCProcessMatchVoteKickResponse, replyHeader(parsed.Header, resultOK), (&gcproto.ProcessMatchVoteKickResponse{Rip: gcproto.Bl(rip)}).Marshal())}
		if rip && req.TargetSteamID != nil {
			out = append(out, gcproto.BuildMessage(gcproto.EMsgGCKickPlayerFromLobby, &gcproto.ProtoBufHeader{}, (&gcproto.KickPlayerFromLobby{TargetID: req.TargetSteamID}).Marshal()))
		}
		return out

	default:
		s.log.Debug("gc: no handler for server EMsg, ignoring", "emsg", parsed.EMsg, "steam_id", actingID)
		return nil
	}
}

// marshaler is any of the response structs' Marshal() []byte method; every
// gcproto message type already implements this via its own Marshal.
type marshaler interface{ Marshal() []byte }

// oneReply frames a single synchronous reply, choosing EResult by whether
// the handler returned an error. body is still sent on error: it is always
// the well-formed zero-value response (every *Response type in this package
// carries no fields), so a nil-body distinction is unnecessary -- the
// header's EResult is what tells the client whether to trust it.
func oneReply(emsg gcproto.EMsg, req *gcproto.ProtoBufHeader, err error, body marshaler) [][]byte {
	result := resultOK
	if err != nil {
		result = resultFail
	}
	return [][]byte{gcproto.BuildMessage(emsg, replyHeader(req, result), body.Marshal())}
}
