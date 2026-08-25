package mm

import "testing"

func TestClassifyMatchAddReply(t *testing.T) {
	tests := []struct {
		name      string
		out       string
		supported bool
		accepted  bool
	}{
		{
			name:      "new seat accepted",
			out:       "TFMM_MATCH_ADD_OK 0000000000001234 1\n",
			supported: true,
			accepted:  true,
		},
		{
			name:      "idempotent replay accepted",
			out:       "TFMM_MATCH_ADD_OK 0000000000001234 0\n",
			supported: true,
			accepted:  true,
		},
		{
			name:      "server rejected update",
			out:       "TFMM_MATCH_ADD_FAILED 0000000000001234\n",
			supported: true,
			accepted:  false,
		},
		{
			name:      "unmodified server",
			out:       "Unknown command: tf_mm_match_add\n",
			supported: false,
			accepted:  false,
		},
		{name: "empty reply is not success", supported: true, accepted: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supported, accepted := classifyMatchAddReply(tt.out)
			if supported != tt.supported || accepted != tt.accepted {
				t.Fatalf("classifyMatchAddReply(%q) = (%v, %v), want (%v, %v)", tt.out, supported, accepted, tt.supported, tt.accepted)
			}
		})
	}
}
