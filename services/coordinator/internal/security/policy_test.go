package security

import (
	"bytes"
	"testing"
)

func TestDefaultPolicyCoversTheObviousCheats(t *testing.T) {
	p := DefaultPolicy()
	for _, name := range []string{"sv_cheats", "sv_lan", "sv_allow_wait_command", "host_timescale", "r_drawothermodels", "mat_wireframe"} {
		r, ok := p.Rule(name)
		if !ok {
			t.Fatalf("policy has no rule for %s", name)
		}
		if !r.Fatal {
			t.Fatalf("%s should be a fatal rule", name)
		}
	}
	if !contains(p.BlockedCommands, "sv_cheats") || !contains(p.BlockedCommands, "wait") {
		t.Fatal("blocked command list is missing the basics")
	}
}

func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

func TestCheckExactValues(t *testing.T) {
	p := DefaultPolicy()
	cases := []struct {
		cvar, value string
		wantOK      bool
	}{
		{"sv_cheats", "0", true},
		{"sv_cheats", "0.0", true}, // the engine prints floats for some cvars
		{"sv_cheats", " 0 ", true}, // and pads them
		{"sv_cheats", "1", false},
		{"host_timescale", "1", true},
		{"host_timescale", "1.5", false},
		{"r_drawothermodels", "2", false}, // the classic wallhack value
		{"r_drawothermodels", "1", true},
		{"cl_interp_ratio", "2", true},
		{"cl_interp_ratio", "0", false},
		{"cl_interp_ratio", "9", false},
		{"mat_picmip", "-1", true},
		{"mat_picmip", "4", false},
		// Convars the policy says nothing about are not violations: a host must
		// not be able to manufacture one by reporting junk.
		{"cl_showfps", "5", true},
	}
	for _, tc := range cases {
		if _, expected, ok := p.Check(tc.cvar, tc.value); ok != tc.wantOK {
			t.Errorf("Check(%s=%q) = %v, want %v (expected %s)", tc.cvar, tc.value, ok, tc.wantOK, expected)
		}
	}
}

func TestNonNumericValueFailsARangeRule(t *testing.T) {
	p := DefaultPolicy()
	if _, _, ok := p.Check("cl_interp_ratio", "nonsense"); ok {
		t.Fatal("an unparseable value must not satisfy a numeric range rule")
	}
}

func TestDigestIsStableAndOrderIndependent(t *testing.T) {
	a := DefaultPolicy()
	b := DefaultPolicy()
	// Reversing the declaration order must not change the digest, or a host and
	// the coordinator could disagree over an identical policy.
	for i, j := 0, len(b.ConVars)-1; i < j; i, j = i+1, j-1 {
		b.ConVars[i], b.ConVars[j] = b.ConVars[j], b.ConVars[i]
	}
	if !bytes.Equal(a.Digest(), b.Digest()) {
		t.Fatal("digest changed when rule order changed")
	}
}

func TestDigestChangesWithContent(t *testing.T) {
	a := DefaultPolicy()
	before := a.Digest()

	b := DefaultPolicy()
	for i := range b.ConVars {
		if b.ConVars[i].Name == "sv_cheats" {
			b.ConVars[i].Value = "1"
		}
	}
	if bytes.Equal(before, b.Digest()) {
		t.Fatal("relaxing sv_cheats must change the digest")
	}

	c := DefaultPolicy()
	c.AllowRCON = true
	if bytes.Equal(before, c.Digest()) {
		t.Fatal("changing allow_rcon must change the digest")
	}
}

func TestToProtoCarriesDigestAndRules(t *testing.T) {
	p := DefaultPolicy()
	msg := p.ToProto()
	if !bytes.Equal(msg.GetDigest(), p.Digest()) {
		t.Fatal("wire policy carries a digest that does not match the policy")
	}
	if len(msg.GetConvars()) != len(p.ConVars) {
		t.Fatalf("wire policy has %d rules, policy has %d", len(msg.GetConvars()), len(p.ConVars))
	}
	for _, cv := range msg.GetConvars() {
		if cv.GetName() == "cl_interp_ratio" {
			if cv.Value != nil {
				t.Fatal("a range rule must not also send an exact value")
			}
			if cv.GetMinValue() != 1 || cv.GetMaxValue() != 5 {
				t.Fatalf("range not carried: [%v, %v]", cv.GetMinValue(), cv.GetMaxValue())
			}
		}
	}
}

func TestMatchSecretsAreDistinctPerMatch(t *testing.T) {
	k := NewKeyring("root-secret")
	a := k.MatchSecret("match-a")
	b := k.MatchSecret("match-b")
	if bytes.Equal(a, b) {
		t.Fatal("two matches must not share a secret")
	}
	if !bytes.Equal(a, k.MatchSecret("match-a")) {
		t.Fatal("match secret derivation must be deterministic")
	}
	if k2 := NewKeyring("other-root"); bytes.Equal(a, k2.MatchSecret("match-a")) {
		t.Fatal("a different root secret must produce a different match secret")
	}
}

func TestJoinTokenBindsEverythingThatMatters(t *testing.T) {
	secret := NewKeyring("root").MatchSecret("m1")
	const expiry = 1_700_000_000_000
	tok := JoinToken(secret, "m1", 76561198000000001, 1, expiry)

	if !VerifyJoinToken(secret, "m1", 76561198000000001, 1, expiry, tok) {
		t.Fatal("a freshly minted token must verify")
	}
	// Every bound field must break verification when changed.
	if VerifyJoinToken(secret, "m2", 76561198000000001, 1, expiry, tok) {
		t.Fatal("token is not bound to the match id")
	}
	if VerifyJoinToken(secret, "m1", 76561198000000002, 1, expiry, tok) {
		t.Fatal("token is not bound to the SteamID")
	}
	if VerifyJoinToken(secret, "m1", 76561198000000001, 2, expiry, tok) {
		t.Fatal("token is not bound to the side")
	}
	if VerifyJoinToken(secret, "m1", 76561198000000001, 1, expiry+1, tok) {
		t.Fatal("token is not bound to the expiry")
	}
	other := NewKeyring("root").MatchSecret("m9")
	if VerifyJoinToken(other, "m1", 76561198000000001, 1, expiry, tok) {
		t.Fatal("a token from one match must not verify under another match's secret")
	}
}

func TestResultSignatureCoversTheScoreline(t *testing.T) {
	secret := NewKeyring("root").MatchSecret("m1")
	sig := SignResult(secret, "m1", 1, 5, 3, 1200)

	if !VerifyResult(secret, "m1", 1, 5, 3, 1200, sig) {
		t.Fatal("a valid result signature must verify")
	}
	if VerifyResult(secret, "m1", 2, 5, 3, 1200, sig) {
		t.Fatal("flipping the winner must invalidate the signature")
	}
	if VerifyResult(secret, "m1", 1, 6, 3, 1200, sig) {
		t.Fatal("changing the score must invalidate the signature")
	}
	if VerifyResult(secret, "m1", 1, 5, 3, 1201, sig) {
		t.Fatal("changing the duration must invalidate the signature")
	}
	if VerifyResult(secret, "m1", 1, 5, 3, 1200, nil) {
		t.Fatal("an absent signature must not verify")
	}
}

func TestIntegritySignature(t *testing.T) {
	secret := NewKeyring("root").MatchSecret("m1")
	digest := DefaultPolicy().Digest()
	sig := SignIntegrityReport(secret, "m1", digest, 2)

	if !VerifyIntegrityReport(secret, "m1", digest, 2, sig) {
		t.Fatal("valid integrity signature must verify")
	}
	if VerifyIntegrityReport(secret, "m1", digest, 0, sig) {
		t.Fatal("dropping violations from the report must invalidate the signature")
	}
	tampered := append([]byte(nil), digest...)
	tampered[0] ^= 0xff
	if VerifyIntegrityReport(secret, "m1", tampered, 2, sig) {
		t.Fatal("a swapped policy digest must invalidate the signature")
	}
}
