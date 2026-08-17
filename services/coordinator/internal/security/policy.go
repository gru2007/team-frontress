// Package security defines the match integrity policy the coordinator hands to
// hosts, and the cryptographic primitives that keep hosts and clients honest.
//
// The threat model for a P2P match is that the host is an ordinary player, so
// the host is not trusted. Three layers cover that:
//
//  1. Policy — the coordinator dictates the convar state a match must run
//     under. The host applies it and reports back what it actually observes;
//     every client independently reports the same replicated convars. A host
//     that lies about sv_cheats disagrees with its own clients.
//
//  2. Join tokens — only SteamIDs on the roster can connect, proven by an HMAC
//     the host can verify but not forge for other people.
//
//  3. Result corroboration — a host's reported score only advances the war if
//     enough non-host players report the same thing.
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/greyline-frontress/coordinator/internal/wire/pb"
)

// ConVarRule constrains one convar for the duration of a match.
type ConVarRule struct {
	Name string `json:"name"`
	// Value is the exact required value. Mutually exclusive with Min/Max.
	Value string `json:"value,omitempty"`
	// Min/Max bound a numeric convar. Used for the client settings that have a
	// legitimate range but an exploitable extreme.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// EnforceOnHost: the host sets this before the map loads and refuses to
	// start if it cannot.
	EnforceOnHost bool `json:"enforce_on_host,omitempty"`
	// QueryOnClients: the host polls each client with
	// IServerPluginHelpers::StartQueryCvarValue and reports the answers.
	QueryOnClients bool `json:"query_on_clients,omitempty"`
	// Fatal: a violation aborts the match instead of accumulating a flag.
	Fatal bool `json:"fatal,omitempty"`
	// Why documents the rule for operators; not sent on the wire.
	Why string `json:"why,omitempty"`
}

// Policy is the full integrity contract for a match.
type Policy struct {
	Version              uint32       `json:"version"`
	ConVars              []ConVarRule `json:"convars"`
	BlockedCommands      []string     `json:"blocked_commands"`
	BlockCheatConVars    bool         `json:"block_cheat_convars"`
	RequirePureServer    bool         `json:"require_pure_server"`
	PureLevel            uint32       `json:"pure_level"`
	AllowPlugins         bool         `json:"allow_plugins"`
	AllowBots            bool         `json:"allow_bots"`
	AllowRCON            bool         `json:"allow_rcon"`
	ClientQueryIntervalS uint32       `json:"client_query_interval_s"`
}

// DefaultPolicy is the MVP contract. Fatal rules are the ones that make a match
// meaningless if broken; the rest are flagged and reviewed.
//
// Convars that live in the engine binary rather than the game DLL (sv_cheats,
// sv_pure, sv_lan, sv_allow_wait_command, sv_consistency, sv_allowdownload) are
// still reachable through ICvar::FindVar on both ends, which is how the game
// module applies and reads them.
func DefaultPolicy() *Policy {
	f := func(v float64) *float64 { return &v }
	return &Policy{
		Version: 1,
		ConVars: []ConVarRule{
			// --- host-side, fatal ------------------------------------------
			{Name: "sv_cheats", Value: "0", EnforceOnHost: true, QueryOnClients: true, Fatal: true,
				Why: "cheat-flagged commands and convars; replicated, so clients can corroborate"},
			{Name: "sv_lan", Value: "0", EnforceOnHost: true, Fatal: true,
				Why: "sv_lan 1 skips Steam auth for joining clients"},
			{Name: "sv_allow_wait_command", Value: "0", EnforceOnHost: true, Fatal: true,
				Why: "the wait command enables scripted rapid-fire and alias loops"},
			{Name: "host_timescale", Value: "1", EnforceOnHost: true, QueryOnClients: true, Fatal: true,
				Why: "alters simulation rate for everyone on the server"},
			{Name: "sv_password", Value: "", EnforceOnHost: true, Fatal: true,
				Why: "admission is controlled by coordinator-issued join tokens, not a shared password"},
			{Name: "tf_bot_quota", Value: "0", EnforceOnHost: true, Fatal: true,
				Why: "bots must not pad a ranked-war match"},
			{Name: "sv_allow_votes", Value: "0", EnforceOnHost: true, Fatal: true,
				Why: "vote-kick is an obvious grief vector when the host is a player"},

			// --- host-side, flagged ----------------------------------------
			{Name: "sv_consistency", Value: "1", EnforceOnHost: true,
				Why: "forces clients to run the server's versions of critical files"},
			{Name: "sv_allowdownload", Value: "0", EnforceOnHost: true,
				Why: "a player-run host should not be able to push files to clients"},
			{Name: "sv_allowupload", Value: "0", EnforceOnHost: true,
				Why: "same, in the other direction"},
			{Name: "sv_alltalk", Value: "0", EnforceOnHost: true,
				Why: "competitive consistency between matches"},
			{Name: "mp_tournament", Value: "0", EnforceOnHost: true,
				Why: "tournament mode changes round flow the war layer does not model yet"},
			{Name: "sv_maxupdaterate", Min: f(66), Max: f(66), EnforceOnHost: true,
				Why: "pin the rate so match conditions are comparable across hosts"},
			{Name: "sv_client_min_interp_ratio", Min: f(1), Max: f(1), EnforceOnHost: true,
				Why: "blocks cl_interp_ratio 0 lag-compensation abuse"},

			// --- client-side, queried by the host --------------------------
			{Name: "r_drawothermodels", Value: "1", QueryOnClients: true, Fatal: true,
				Why: "value 2 renders players through walls"},
			{Name: "mat_wireframe", Value: "0", QueryOnClients: true, Fatal: true,
				Why: "wireframe rendering sees through geometry"},
			{Name: "mat_fullbright", Value: "0", QueryOnClients: true,
				Why: "removes darkness as concealment"},
			{Name: "r_drawdetailprops", Min: f(0), Max: f(1), QueryOnClients: true,
				Why: "disabling foliage detail is a visibility advantage"},
			{Name: "cl_interp_ratio", Min: f(1), Max: f(5), QueryOnClients: true,
				Why: "out-of-range interpolation abuses lag compensation"},
			{Name: "mat_picmip", Min: f(-1), Max: f(2), QueryOnClients: true,
				Why: "extreme picmip flattens textures into flat colour, aiding target ID"},
			{Name: "r_drawviewmodel", Min: f(0), Max: f(1), QueryOnClients: true,
				Why: "sanity check that the query channel itself is answering"},
		},
		// Refused client-side while a match is live. The host also rejects
		// these if they arrive as client commands.
		BlockedCommands: []string{
			"sv_cheats",
			"wait",
			"alias",
			"exec",
			"rcon",
			"rcon_password",
			"host_timescale",
			"changelevel",
			"map",
			"sv_pure",
			"plugin_load",
			"plugin_unload",
			"unbindall",
			"mp_forcewin",
			"mp_scrambleteams",
			"mp_switchteams",
			"bot",
			"tf_bot_add",
			"tf_bot_kick",
			"ent_create",
			"ent_fire",
			"ent_remove",
			"give",
			"noclip",
			"god",
			"impulse",
			"firstperson",
			"thirdperson",
			"record",
			"stopsound",
		},
		BlockCheatConVars:    true,
		RequirePureServer:    true,
		PureLevel:            2,
		AllowPlugins:         false,
		AllowBots:            false,
		AllowRCON:            false,
		ClientQueryIntervalS: 45,
	}
}

// LoadPolicy reads a policy override from a JSON file.
func LoadPolicy(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}
	p := &Policy{}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("parse policy: %w", err)
	}
	if len(p.ConVars) == 0 {
		return nil, fmt.Errorf("policy %s has no convar rules", path)
	}
	return p, nil
}

// Digest is a stable SHA-256 over the policy's semantic content. The host
// echoes it in every integrity report, so a host running a policy other than
// the one it was handed is detectable.
func (p *Policy) Digest() []byte {
	h := sha256.New()
	writeU32(h, p.Version)
	rules := append([]ConVarRule(nil), p.ConVars...)
	sort.Slice(rules, func(i, j int) bool { return rules[i].Name < rules[j].Name })
	for _, r := range rules {
		h.Write([]byte(r.Name))
		h.Write([]byte{0})
		h.Write([]byte(r.Value))
		h.Write([]byte{0})
		h.Write([]byte(formatBound(r.Min)))
		h.Write([]byte{0})
		h.Write([]byte(formatBound(r.Max)))
		h.Write([]byte{0})
		h.Write([]byte{boolByte(r.EnforceOnHost), boolByte(r.QueryOnClients), boolByte(r.Fatal)})
	}
	cmds := append([]string(nil), p.BlockedCommands...)
	sort.Strings(cmds)
	for _, c := range cmds {
		h.Write([]byte(c))
		h.Write([]byte{0})
	}
	h.Write([]byte{
		boolByte(p.BlockCheatConVars),
		boolByte(p.RequirePureServer),
		boolByte(p.AllowPlugins),
		boolByte(p.AllowBots),
		boolByte(p.AllowRCON),
	})
	writeU32(h, p.PureLevel)
	writeU32(h, p.ClientQueryIntervalS)
	return h.Sum(nil)
}

// ToProto renders the policy for the wire, digest included.
func (p *Policy) ToProto() *pb.CMsgSecurityPolicy {
	out := &pb.CMsgSecurityPolicy{
		Version:              proto.Uint32(p.Version),
		BlockedCommands:      append([]string(nil), p.BlockedCommands...),
		BlockCheatCvars:      proto.Bool(p.BlockCheatConVars),
		RequirePureServer:    proto.Bool(p.RequirePureServer),
		PureLevel:            proto.Uint32(p.PureLevel),
		AllowPlugins:         proto.Bool(p.AllowPlugins),
		AllowBots:            proto.Bool(p.AllowBots),
		AllowRcon:            proto.Bool(p.AllowRCON),
		ClientQueryIntervalS: proto.Uint32(p.ClientQueryIntervalS),
		Digest:               p.Digest(),
	}
	for _, r := range p.ConVars {
		cv := &pb.CMsgConVarRule{
			Name:           proto.String(r.Name),
			EnforceOnHost:  proto.Bool(r.EnforceOnHost),
			QueryOnClients: proto.Bool(r.QueryOnClients),
			Fatal:          proto.Bool(r.Fatal),
		}
		if r.Min == nil && r.Max == nil {
			cv.Value = proto.String(r.Value)
		} else {
			if r.Min != nil {
				cv.MinValue = proto.Float64(*r.Min)
			}
			if r.Max != nil {
				cv.MaxValue = proto.Float64(*r.Max)
			}
		}
		out.Convars = append(out.Convars, cv)
	}
	return out
}

// Rule finds a rule by convar name.
func (p *Policy) Rule(name string) (ConVarRule, bool) {
	for _, r := range p.ConVars {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return ConVarRule{}, false
}

// Check evaluates one observed convar value against the policy. It returns the
// matching rule and a non-empty explanation when the value is out of contract.
// Observations for convars the policy says nothing about are ignored, so a host
// cannot manufacture violations by reporting junk.
func (p *Policy) Check(name, value string) (rule ConVarRule, expected string, ok bool) {
	r, found := p.Rule(name)
	if !found {
		return ConVarRule{}, "", true
	}
	if r.Min == nil && r.Max == nil {
		if numericEqual(value, r.Value) {
			return r, r.Value, true
		}
		return r, r.Value, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return r, boundsText(r), false
	}
	if r.Min != nil && f < *r.Min {
		return r, boundsText(r), false
	}
	if r.Max != nil && f > *r.Max {
		return r, boundsText(r), false
	}
	return r, boundsText(r), true
}

// numericEqual compares convar values the way the engine does: "0", "0.0" and
// "  0 " are the same value, but non-numeric values compare as strings.
func numericEqual(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		return fa == fb
	}
	return false
}

func boundsText(r ConVarRule) string {
	lo, hi := "-inf", "+inf"
	if r.Min != nil {
		lo = formatBound(r.Min)
	}
	if r.Max != nil {
		hi = formatBound(r.Max)
	}
	return "[" + lo + ", " + hi + "]"
}

func formatBound(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func writeU32(h interface{ Write([]byte) (int, error) }, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	h.Write(b[:])
}

// ---------------------------------------------------------------------------
// Keys, tokens and signatures
// ---------------------------------------------------------------------------

// Keyring derives per-match secrets from the coordinator's root secret, so a
// leaked match secret reveals nothing about other matches.
type Keyring struct {
	root []byte
}

func NewKeyring(rootSecret string) *Keyring {
	sum := sha256.Sum256([]byte(rootSecret))
	return &Keyring{root: sum[:]}
}

// MatchSecret derives the secret shared with a single match's host.
func (k *Keyring) MatchSecret(matchID string) []byte {
	m := hmac.New(sha256.New, k.root)
	m.Write([]byte("greyline/match-secret/v1\x00"))
	m.Write([]byte(matchID))
	return m.Sum(nil)
}

// JoinToken authorises exactly one SteamID to join exactly one match, on one
// side, until it expires. The host verifies it with the match secret; it cannot
// mint a token for a SteamID the coordinator did not place on the roster
// because the coordinator will not accept an unrostered player in the result.
func JoinToken(matchSecret []byte, matchID string, steamID uint64, side int32, expiryMs uint64) []byte {
	m := hmac.New(sha256.New, matchSecret)
	m.Write([]byte("greyline/join/v1\x00"))
	m.Write([]byte(matchID))
	m.Write([]byte{0})
	writeU64(m, steamID)
	writeU64(m, uint64(uint32(side)))
	writeU64(m, expiryMs)
	return m.Sum(nil)
}

// VerifyJoinToken is the mirror of JoinToken, used in tests and by any
// server-side reimplementation.
func VerifyJoinToken(matchSecret []byte, matchID string, steamID uint64, side int32, expiryMs uint64, token []byte) bool {
	return hmac.Equal(JoinToken(matchSecret, matchID, steamID, side, expiryMs), token)
}

// SignResult covers the fields that decide the war outcome. Player scorelines
// are deliberately outside the signature: they are cosmetic, and including them
// would make the signature fragile against harmless ordering differences.
func SignResult(matchSecret []byte, matchID string, outcome int32, red, blu, durationS uint32) []byte {
	m := hmac.New(sha256.New, matchSecret)
	m.Write([]byte("greyline/result/v1\x00"))
	m.Write([]byte(matchID))
	m.Write([]byte{0})
	writeU64(m, uint64(uint32(outcome)))
	writeU64(m, uint64(red))
	writeU64(m, uint64(blu))
	writeU64(m, uint64(durationS))
	return m.Sum(nil)
}

// VerifyResult checks a host's result signature in constant time.
func VerifyResult(matchSecret []byte, matchID string, outcome int32, red, blu, durationS uint32, sig []byte) bool {
	return hmac.Equal(SignResult(matchSecret, matchID, outcome, red, blu, durationS), sig)
}

// SignIntegrityReport covers the policy digest and the violation set, which is
// what the coordinator acts on.
func SignIntegrityReport(matchSecret []byte, matchID string, policyDigest []byte, violationCount int) []byte {
	m := hmac.New(sha256.New, matchSecret)
	m.Write([]byte("greyline/integrity/v1\x00"))
	m.Write([]byte(matchID))
	m.Write([]byte{0})
	m.Write(policyDigest)
	writeU64(m, uint64(violationCount))
	return m.Sum(nil)
}

// VerifyIntegrityReport mirrors SignIntegrityReport.
func VerifyIntegrityReport(matchSecret []byte, matchID string, policyDigest []byte, violationCount int, sig []byte) bool {
	return hmac.Equal(SignIntegrityReport(matchSecret, matchID, policyDigest, violationCount), sig)
}

func writeU64(h interface{ Write([]byte) (int, error) }, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	h.Write(b[:])
}
