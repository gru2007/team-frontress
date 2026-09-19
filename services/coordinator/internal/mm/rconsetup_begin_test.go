package mm

import "testing"

func TestClassifyMatchBeginReply(t *testing.T) {
	const matchID = "0123456789abcdef"
	tests := []struct {
		name      string
		response  string
		supported bool
		accepted  bool
	}{
		{"exact acknowledgement", "TFMM_MATCH_BEGIN_OK 0123456789abcdef\n", true, true},
		{"case-insensitive match ID", "server log\nTFMM_MATCH_BEGIN_OK 0123456789ABCDEF\n", true, true},
		{"failed command", "TFMM_MATCH_BEGIN_FAILED 0123456789abcdef\n", true, false},
		{"wrong match", "TFMM_MATCH_BEGIN_OK fedcba9876543210\n", true, false},
		{"missing acknowledgement", "", true, false},
		{"unmodified server", "Unknown command \"tf_mm_match_begin\"\n", false, false},
		{"unrelated unknown command", "Unknown command \"tf_mm_match_add\"\n", true, false},
		{"embedded acknowledgement", "prefix TFMM_MATCH_BEGIN_OK 0123456789abcdef suffix\n", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supported, accepted := classifyMatchBeginReply(tt.response, matchID)
			if supported != tt.supported || accepted != tt.accepted {
				t.Fatalf("classifyMatchBeginReply(%q, %q) = (%v, %v), want (%v, %v)",
					tt.response, matchID, supported, accepted, tt.supported, tt.accepted)
			}
		})
	}
}
