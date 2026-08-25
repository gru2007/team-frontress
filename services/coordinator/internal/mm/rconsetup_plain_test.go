package mm

import "testing"

func TestClassifyMatchAddPlainReply(t *testing.T) {
	supported, accepted := classifyMatchAddReply("TFMM_MATCH_ADD_PLAIN 0000000000001234\n")
	if !supported || !accepted {
		t.Fatalf("plain fallback = (%v, %v), want (true, true)", supported, accepted)
	}
}
